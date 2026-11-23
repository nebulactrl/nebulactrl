package autoban

import (
	"context"
	"fmt"
	"net/netip"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agent/store"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

var t0 = time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type fixture struct {
	t   *testing.T
	st  *store.Store
	dir string
	clk *clock
	e   *Engine
	log []string
	mu  sync.Mutex
}

func (f *fixture) logs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.log...)
}

func newFixture(t *testing.T, mod ...func(*Config)) *fixture {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	f := &fixture{t: t, st: st, dir: dir, clk: &clock{t: t0}}
	f.e = f.open(st, mod...)
	return f
}

func (f *fixture) open(st *store.Store, mod ...func(*Config)) *Engine {
	cfg := Config{
		Store: st, Now: f.clk.Now,
		Log: func(svc, line string) {
			f.mu.Lock()
			f.log = append(f.log, svc+": "+line)
			f.mu.Unlock()
		},
		Debounce: 10 * time.Millisecond, SendInterval: 10 * time.Millisecond, AckTimeout: 200 * time.Millisecond,
	}
	for _, m := range mod {
		m(&cfg)
	}
	return New(cfg)
}

func policy(threshold int, allow ...string) Policy {
	return Policy{
		ServiceID: "svc",
		AutoBan:   agentprotocol.AutoBan{Enabled: true, Threshold: threshold, WindowSeconds: 600, DurationSeconds: 3600, Allow: allow},
		Strikes:   func(id string) bool { return id == "std1" || id == "r1" },
	}
}

func (f *fixture) policies(p Policy) { f.e.SetPolicies(map[string]Policy{"route": p}) }

func (f *fixture) hit(ip, rule string) {
	f.e.Observe(Hit{RouteID: "route", BlockedBy: rule, ClientIP: ip, Status: 403, Time: f.clk.Now()})
}

func (f *fixture) hits(n int, ip, rule string) {
	for range n {
		f.hit(ip, rule)
	}
}

// run starts the engine's loops and stops them, waiting, before the store closes.
func (f *fixture) run() {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); f.e.Run(ctx) }()
	f.t.Cleanup(func() { cancel(); <-done })
}

func (f *fixture) events() []agentprotocol.BanEvent {
	_, ev, err := f.st.PendingBanEvents(0)
	require.NoError(f.t, err)
	return ev
}

func kinds(evs []agentprotocol.BanEvent) []string {
	var out []string
	for _, e := range evs {
		k := e.Kind
		if e.Reason != "" {
			k += ":" + e.Reason
		}
		out = append(out, k)
	}
	return out
}

func TestThresholdWithinWindowBans(t *testing.T) {
	f := newFixture(t)
	f.policies(policy(3))
	f.hits(2, "203.0.113.7", "std1")
	require.Empty(t, f.e.Active())
	f.hit("203.0.113.7", "r1")
	act := f.e.Active()
	require.Len(t, act, 1)
	b := act[0]
	require.Equal(t, "203.0.113.7/32", b.Prefix)
	require.Equal(t, "svc", b.ServiceID)
	require.Equal(t, "route", b.RouteID)
	require.Equal(t, 3, b.Strikes)
	require.Equal(t, map[string]int{"std1": 2, "r1": 1}, b.Rules)
	require.Equal(t, t0.Add(time.Hour), b.ExpiresAt)
	require.Equal(t, []string{"ban"}, kinds(f.events()))
	require.Len(t, f.logs(), 1)
	require.Contains(t, f.logs()[0], "203.0.113.7/32")

	gb := f.e.Bans()
	require.Equal(t, []agentprotocol.GuardBan{{Prefix: "203.0.113.7/32", ExpiresAt: t0.Add(time.Hour)}}, gb.Services["route"].Bans)
}

func TestStrikesOutsideTheWindowDoNotCount(t *testing.T) {
	f := newFixture(t)
	p := policy(3)
	p.AutoBan.WindowSeconds = 60
	f.policies(p)
	// Strikes at 0 s, 30 s and 70 s: the first has left the window by the third.
	f.hit("203.0.113.7", "std1")
	f.clk.Advance(30 * time.Second)
	f.hit("203.0.113.7", "std1")
	f.clk.Advance(40 * time.Second)
	f.hit("203.0.113.7", "std1")
	require.Empty(t, f.e.Active())
	f.hit("203.0.113.7", "std1")
	require.Len(t, f.e.Active(), 1, "three strikes inside one window")
}

func TestReplayedRequestOlderThanTheWindowIsNotAStrike(t *testing.T) {
	f := newFixture(t)
	f.policies(policy(3))
	for range 5 {
		f.e.Observe(Hit{RouteID: "route", BlockedBy: "std1", ClientIP: "203.0.113.7", Status: 403, Time: t0.Add(-time.Hour)})
	}
	require.Empty(t, f.e.Active())
}

func TestBanIsNotExtendedAndBannedTrafficDoesNotStrike(t *testing.T) {
	f := newFixture(t)
	f.policies(policy(3))
	f.hits(3, "203.0.113.7", "std1")
	f.clk.Advance(30 * time.Minute)
	f.hits(10, "203.0.113.7", "std1")
	act := f.e.Active()
	require.Len(t, act, 1)
	require.Equal(t, t0.Add(time.Hour), act[0].ExpiresAt, "no extension while active")
	require.Equal(t, []string{"ban"}, kinds(f.events()))
}

func TestBanExpiresAndTheAddressCanBeBannedAgain(t *testing.T) {
	f := newFixture(t)
	f.policies(policy(3))
	f.hits(3, "203.0.113.7", "std1")
	f.clk.Advance(time.Hour)
	require.Empty(t, f.e.Bans().Services["route"].Bans, "an expired ban is not enforced even before the sweep")
	f.e.Sweep()
	require.Empty(t, f.e.Active())
	evs := f.events()
	require.Equal(t, []string{"ban", "end:expired"}, kinds(evs))
	require.Equal(t, t0.Add(time.Hour), evs[1].At)
	require.Equal(t, evs[0].Ban.ID, evs[1].Ban.ID)
	got, err := f.st.LoadBans()
	require.NoError(t, err)
	require.Empty(t, got)

	f.hits(3, "203.0.113.7", "std1")
	require.Len(t, f.e.Active(), 1)
}

func TestBanThatExpiredBeforeTheSweepIsReplacedByAStrike(t *testing.T) {
	f := newFixture(t)
	f.policies(policy(3))
	f.hits(3, "203.0.113.7", "std1")
	f.clk.Advance(2 * time.Hour)
	f.hits(3, "203.0.113.7", "std1")
	require.Equal(t, []string{"ban", "end:expired", "ban"}, kinds(f.events()))
	require.Len(t, f.e.Active(), 1)
}

func TestIPv6IsBannedAsItsSlash64(t *testing.T) {
	f := newFixture(t)
	f.policies(policy(3))
	f.hit("2001:db8:1:2::1", "std1")
	f.hit("2001:db8:1:2:aaaa::2", "std1")
	f.hit("2001:db8:1:2::3", "std1")
	act := f.e.Active()
	require.Len(t, act, 1)
	require.Equal(t, "2001:db8:1:2::/64", act[0].Prefix)
	// An IPv4-mapped address is the IPv4 address.
	f.hits(3, "::ffff:198.51.100.9", "std1")
	require.Len(t, f.e.Active(), 2)
	require.Contains(t, []string{f.e.Active()[0].Prefix, f.e.Active()[1].Prefix}, "198.51.100.9/32")
}

func TestExemptAndAllowListedAddressesNeverStrike(t *testing.T) {
	f := newFixture(t)
	f.policies(policy(3, "198.51.100.0/24"))
	for _, ip := range []string{"127.0.0.1", "10.1.2.3", "172.16.0.9", "192.168.1.1", "100.64.0.1", "169.254.1.1", "::1", "fe80::1", "fc00::1", "0.0.0.0", "198.51.100.77", "not-an-ip", ""} {
		f.hits(5, ip, "std1")
	}
	require.Empty(t, f.e.Active())
	require.Empty(t, f.events())
}

func TestExemptCanBeReplaced(t *testing.T) {
	f := newFixture(t, func(c *Config) { c.Exempt = func(netip.Addr) bool { return false } })
	f.policies(policy(3))
	f.hits(3, "192.168.65.1", "std1")
	require.Len(t, f.e.Active(), 1, "a private visitor is banned once the exemption is lifted")
}

func TestOnlyStrikingRulesCount(t *testing.T) {
	cond := func(field string) []agentprotocol.FirewallCondition {
		return []agentprotocol.FirewallCondition{{Field: field, Op: agentprotocol.FirewallOpIn, Values: []string{"x"}}}
	}
	fw := &agentprotocol.Firewall{Rules: []agentprotocol.FirewallRule{
		{ID: "path", Action: agentprotocol.FirewallBlock, Conditions: []agentprotocol.FirewallCondition{{Field: agentprotocol.FirewallFieldPath, Op: agentprotocol.FirewallOpPrefix, Values: []string{"/x"}}}},
		{ID: "mixed", Action: agentprotocol.FirewallBlock, Conditions: append(cond(agentprotocol.FirewallFieldIP), agentprotocol.FirewallCondition{Field: agentprotocol.FirewallFieldMethod, Op: agentprotocol.FirewallOpIn, Values: []string{"POST"}})},
		{ID: "iponly", Action: agentprotocol.FirewallBlock, Conditions: cond(agentprotocol.FirewallFieldIP)},
		{ID: "country", Action: agentprotocol.FirewallBlock, Conditions: cond(agentprotocol.FirewallFieldCountry)},
		{ID: "ipcountry", Action: agentprotocol.FirewallBlock, Conditions: append(cond(agentprotocol.FirewallFieldIP), cond(agentprotocol.FirewallFieldCountry)...)},
		{ID: "empty", Action: agentprotocol.FirewallBlock},
		{ID: "allowpath", Action: agentprotocol.FirewallAllow, Conditions: []agentprotocol.FirewallCondition{{Field: agentprotocol.FirewallFieldPath, Op: agentprotocol.FirewallOpPrefix, Values: []string{"/ok"}}}},
		{ID: "off", Disabled: true, Action: agentprotocol.FirewallBlock, Conditions: []agentprotocol.FirewallCondition{{Field: agentprotocol.FirewallFieldPath, Op: agentprotocol.FirewallOpPrefix, Values: []string{"/off"}}}},
	}}
	ab := &agentprotocol.AutoBan{Enabled: true, Threshold: 3, WindowSeconds: 600, DurationSeconds: 3600}
	routes := []agentprotocol.Route{
		{ServiceID: "route", RouteExt: agentprotocol.RouteExt{Firewall: fw, AutoBan: ab}},
		{ServiceID: "disabled", RouteExt: agentprotocol.RouteExt{AutoBan: &agentprotocol.AutoBan{Threshold: 3, WindowSeconds: 600, DurationSeconds: 3600}}},
		{ServiceID: "none"},
		{ServiceID: "tcp", RouteExt: agentprotocol.RouteExt{Protocol: agentprotocol.ProtoTCP, AutoBan: ab}},
	}
	pol := PoliciesFromRoutes(routes)
	require.Len(t, pol, 1)
	require.Equal(t, "route", pol["route"].ServiceID)

	for rule, strikes := range map[string]bool{
		"std1": true, "stdsecrets": true, "path": true, "mixed": true,
		"iponly": false, "country": false, "ipcountry": false, "empty": false,
		"allowpath": false, "off": false, "unknown": false, "autoban": false, "": false,
	} {
		f := newFixture(t)
		f.e.SetRoutes(routes)
		f.hits(3, "203.0.113.7", rule)
		if strikes {
			require.Len(t, f.e.Active(), 1, "rule %q strikes", rule)
		} else {
			require.Empty(t, f.e.Active(), "rule %q does not strike", rule)
		}
	}

	// Only 403s strike, and only for a route with a policy.
	f := newFixture(t)
	f.e.SetRoutes(routes)
	for range 5 {
		f.e.Observe(Hit{RouteID: "route", BlockedBy: "std1", ClientIP: "203.0.113.7", Status: 200, Time: t0})
		f.e.Observe(Hit{RouteID: "disabled", BlockedBy: "std1", ClientIP: "203.0.113.7", Status: 403, Time: t0})
		f.e.Observe(Hit{RouteID: "", BlockedBy: "std1", ClientIP: "203.0.113.7", Status: 403, Time: t0})
	}
	require.Empty(t, f.e.Active())
}

func TestPolicyDisabledOrRouteGoneLiftsBans(t *testing.T) {
	f := newFixture(t)
	f.e.SetPolicies(map[string]Policy{"route": policy(3), "other": {ServiceID: "svc2", AutoBan: policy(3).AutoBan}})
	f.hits(3, "203.0.113.7", "std1")
	f.e.Observe(Hit{RouteID: "other", BlockedBy: "std1", ClientIP: "203.0.113.8", Status: 403, Time: t0})
	f.e.Observe(Hit{RouteID: "other", BlockedBy: "std1", ClientIP: "203.0.113.8", Status: 403, Time: t0})
	f.e.Observe(Hit{RouteID: "other", BlockedBy: "std1", ClientIP: "203.0.113.8", Status: 403, Time: t0})
	require.Len(t, f.e.Active(), 2)

	// "route" is disabled, "other" disappears from the table.
	off := policy(3)
	off.AutoBan.Enabled = false
	f.e.SetPolicies(map[string]Policy{"route": off})
	require.Empty(t, f.e.Active())
	require.Empty(t, f.e.Bans().Services)
	evs := f.events()
	require.ElementsMatch(t, []string{"ban", "ban", "end:lifted", "end:lifted"}, kinds(evs))
	got, err := f.st.LoadBans()
	require.NoError(t, err)
	require.Empty(t, got)

	// The strikes of the lifted routes are gone: two strikes after re-enabling do not ban.
	f.policies(policy(3))
	f.hits(2, "203.0.113.9", "std1")
	require.Empty(t, f.e.Active())
}

func TestAllowListCoveringABanLiftsIt(t *testing.T) {
	f := newFixture(t)
	f.policies(policy(3))
	f.hits(3, "203.0.113.7", "std1")
	f.hits(3, "2001:db8:1:2::1", "std1")
	f.hits(3, "198.51.100.5", "std1")
	require.Len(t, f.e.Active(), 3)

	// A /128 inside a banned /64 does not lift it (the guard lets that address through);
	// a covering prefix does.
	f.policies(policy(3, "203.0.113.0/24", "2001:db8:1:2::5"))
	left := f.e.Active()
	require.Len(t, left, 2)
	require.ElementsMatch(t, []string{"2001:db8:1:2::/64", "198.51.100.5/32"}, []string{left[0].Prefix, left[1].Prefix})
	require.Equal(t, []string{"203.0.113.0/24", "2001:db8:1:2::5/128"}, f.e.Bans().Services["route"].Allow)
	require.Equal(t, 1, countKind(f.events(), "end:lifted"))

	f.policies(policy(3, "2001:db8::/32"))
	require.Len(t, f.e.Active(), 1)
	require.Equal(t, "198.51.100.5/32", f.e.Active()[0].Prefix)
}

func countKind(evs []agentprotocol.BanEvent, kind string) int {
	n := 0
	for _, k := range kinds(evs) {
		if k == kind {
			n++
		}
	}
	return n
}

func TestUnban(t *testing.T) {
	f := newFixture(t)
	f.policies(policy(3))
	f.hits(3, "203.0.113.7", "std1")
	id := f.e.Active()[0].ID

	require.ErrorIs(t, f.e.Unban("other", id), ErrNotFound, "another service's ban id")
	require.ErrorIs(t, f.e.Unban("svc", "nope"), ErrNotFound)
	require.NoError(t, f.e.Unban("svc", id))
	require.Empty(t, f.e.Active())
	require.Equal(t, []string{"ban", "end:unbanned"}, kinds(f.events()))
	require.ErrorIs(t, f.e.Unban("svc", id), ErrNotFound)

	// Unbanned means counting starts over.
	f.hits(2, "203.0.113.7", "std1")
	require.Empty(t, f.e.Active())
	f.hit("203.0.113.7", "std1")
	require.Len(t, f.e.Active(), 1)
}

func TestStrikeKeysAreBoundedLeastRecentlyStruckFirst(t *testing.T) {
	f := newFixture(t, func(c *Config) { c.Limits.StrikeKeys = 2 })
	f.policies(policy(3))
	f.hit("203.0.113.1", "std1")
	f.hit("203.0.113.2", "std1")
	f.hit("203.0.113.1", "std1") // 1 is now the more recent
	f.hit("203.0.113.3", "std1") // evicts 2
	f.e.mu.Lock()
	require.Len(t, f.e.strikes, 2)
	f.e.mu.Unlock()
	f.hit("203.0.113.1", "std1")
	require.Len(t, f.e.Active(), 1, "address 1 kept its strikes")
	f.hits(2, "203.0.113.2", "std1")
	require.Len(t, f.e.Active(), 1, "address 2 lost its strike and starts over")
}

func TestStrikeSlotsAreBounded(t *testing.T) {
	f := newFixture(t, func(c *Config) { c.Limits.StrikeSlots = 5 })
	f.policies(policy(100))
	for i := range 20 {
		f.hits(2, fmt.Sprintf("203.0.113.%d", i+1), "std1")
	}
	f.e.mu.Lock()
	defer f.e.mu.Unlock()
	require.LessOrEqual(t, f.e.slots, 5)
	total := 0
	for _, el := range f.e.strikes {
		total += len(el.Value.(*strikeEntry).strikes)
	}
	require.Equal(t, f.e.slots, total, "the slot count matches the strikes held")
}

func TestBanCapsPerServiceAndPerAgent(t *testing.T) {
	f := newFixture(t, func(c *Config) { c.Limits.PerService, c.Limits.PerAgent = 2, 3 })
	f.e.SetPolicies(map[string]Policy{
		"route": {ServiceID: "svc", AutoBan: policy(3).AutoBan, Strikes: policy(3).Strikes},
		"b":     {ServiceID: "svc2", AutoBan: policy(3).AutoBan, Strikes: policy(3).Strikes},
		"c":     {ServiceID: "svc3", AutoBan: policy(3).AutoBan, Strikes: policy(3).Strikes},
	})
	ban := func(route, ip string) {
		for range 3 {
			f.e.Observe(Hit{RouteID: route, BlockedBy: "std1", ClientIP: ip, Status: 403, Time: f.clk.Now()})
		}
	}
	ban("route", "203.0.113.1")
	ban("route", "203.0.113.2")
	ban("route", "203.0.113.3")
	require.Len(t, f.e.Active(), 2, "two bans per service")
	ban("b", "203.0.113.4")
	require.Len(t, f.e.Active(), 3)
	ban("c", "203.0.113.5")
	require.Len(t, f.e.Active(), 3, "three bans per agent")
	require.Equal(t, 3, countKind(f.events(), "ban"), "refused bans queue no event")
}

func TestBansSurviveARestartAndExpiredOnesAreEnded(t *testing.T) {
	f := newFixture(t)
	f.policies(policy(3))
	f.hits(3, "203.0.113.1", "std1")
	f.clk.Advance(30 * time.Minute)
	f.hits(3, "203.0.113.2", "std1")
	before := f.e.Active()
	require.Len(t, before, 2)
	require.NoError(t, f.st.Close())

	st, err := store.Open(f.dir)
	require.NoError(t, err)
	defer st.Close()
	f.clk.Advance(40 * time.Minute) // the first ban (1 h) has expired, the second has 20 min left
	e2 := f.open(st)
	after := e2.Active()
	require.Len(t, after, 1)
	require.Equal(t, before[1], after[0])
	gb := e2.Bans()
	require.Equal(t, []agentprotocol.GuardBan{{Prefix: "203.0.113.2/32", ExpiresAt: before[1].ExpiresAt}}, gb.Services["route"].Bans,
		"the guard file can be written before the first Apply")

	_, evs, err := st.PendingBanEvents(0)
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"ban", "ban", "end:expired"}, kinds(evs))
	for _, ev := range evs {
		if ev.Kind == agentprotocol.BanEventEnd {
			require.Equal(t, before[0].ID, ev.Ban.ID)
			require.Equal(t, before[0].ExpiresAt, ev.At)
		}
	}

	// Before the first policy set nothing is lifted; the first one that lacks the route lifts.
	require.Len(t, e2.Active(), 1)
	e2.SetPolicies(nil)
	require.Empty(t, e2.Active())
}

func TestOnChangeIsDebounced(t *testing.T) {
	f := newFixture(t, func(c *Config) { c.Debounce = 50 * time.Millisecond })
	var n atomic.Int32
	f.e.OnChange(func() { n.Add(1) })
	f.policies(policy(3))
	f.hits(3, "203.0.113.1", "std1")
	f.hits(3, "203.0.113.2", "std1")
	require.Eventually(t, func() bool { return n.Load() >= 1 }, time.Second, 5*time.Millisecond)
	time.Sleep(100 * time.Millisecond)
	require.EqualValues(t, 1, n.Load(), "policy set, two bans: one notification")

	f.policies(policy(3)) // unchanged: nothing to push
	time.Sleep(100 * time.Millisecond)
	require.EqualValues(t, 1, n.Load())
	f.policies(policy(3, "198.51.100.0/24")) // the allow list changed
	require.Eventually(t, func() bool { return n.Load() == 2 }, time.Second, 5*time.Millisecond)
}

func TestBansIsDeterministic(t *testing.T) {
	f := newFixture(t)
	f.policies(policy(3, "198.51.100.0/24", "192.0.2.1"))
	f.hits(3, "203.0.113.9", "std1")
	f.hits(3, "203.0.113.1", "std1")
	f.hits(3, "2001:db8::1", "std1")
	a, b := f.e.Bans(), f.e.Bans()
	require.Equal(t, a, b)
	rb := a.Services["route"]
	require.Equal(t, []string{"198.51.100.0/24", "192.0.2.1/32"}, rb.Allow)
	require.Equal(t, []string{"2001:db8::/64", "203.0.113.1/32", "203.0.113.9/32"}, []string{rb.Bans[0].Prefix, rb.Bans[1].Prefix, rb.Bans[2].Prefix})
}

// fakeSender records ban frames; Connected is switchable.
type fakeSender struct {
	up     atomic.Bool
	frames chan agentprotocol.BanFrame
}

func newFakeSender() *fakeSender {
	s := &fakeSender{frames: make(chan agentprotocol.BanFrame, 64)}
	s.up.Store(true)
	return s
}
func (s *fakeSender) AutoBans(f agentprotocol.BanFrame) { s.frames <- f }
func (s *fakeSender) Connected() bool                   { return s.up.Load() }

func (s *fakeSender) next(t *testing.T) agentprotocol.BanFrame {
	t.Helper()
	select {
	case f := <-s.frames:
		return f
	case <-time.After(3 * time.Second):
		require.FailNow(t, "no ban frame")
		return agentprotocol.BanFrame{}
	}
}

func (s *fakeSender) none(t *testing.T) {
	t.Helper()
	select {
	case f := <-s.frames:
		require.FailNow(t, "unexpected frame", "%+v", f)
	case <-time.After(150 * time.Millisecond):
	}
}

func TestOutboxIsSentResentUntilAckedAndSnapshotAfterReconnect(t *testing.T) {
	snd := newFakeSender()
	f := newFixture(t, func(c *Config) { c.Sender = snd })
	f.policies(policy(3))
	f.hits(3, "203.0.113.1", "std1")

	f.run()

	// First frame: a snapshot (every connection starts with one) carrying the ban once.
	fr := snd.next(t)
	require.True(t, fr.Snapshot)
	require.Equal(t, []string{"svc"}, fr.Services)
	require.Equal(t, []string{"ban"}, kinds(fr.Events))
	require.Equal(t, f.e.Active()[0].ID, fr.Events[0].Ban.ID)

	// No ack: the same events come again, in a frame with a new seq.
	fr2 := snd.next(t)
	require.Greater(t, fr2.Seq, fr.Seq)
	require.Equal(t, kinds(fr.Events), kinds(fr2.Events))
	require.Equal(t, fr.Events[0].EventID, fr2.Events[0].EventID, "a resent event keeps its id")

	// An ack of an older frame does not count.
	f.e.Ack(fr.Seq)
	fr3 := snd.next(t)
	require.Greater(t, fr3.Seq, fr2.Seq)

	f.e.Ack(fr3.Seq)
	require.Eventually(t, func() bool { return len(f.events()) == 0 }, 2*time.Second, 10*time.Millisecond, "acked events leave the outbox")
	snd.none(t)

	// A new event goes out alone (no snapshot) and is deleted on ack.
	id := f.e.Active()[0].ID
	require.NoError(t, f.e.Unban("svc", id))
	fr4 := snd.next(t)
	require.False(t, fr4.Snapshot)
	require.Equal(t, []string{"end:unbanned"}, kinds(fr4.Events))
	f.e.Ack(fr4.Seq)
	require.Eventually(t, func() bool { return len(f.events()) == 0 }, 2*time.Second, 10*time.Millisecond)

	// A ban made while disconnected waits; the reconnect sends a snapshot with it.
	snd.up.Store(false)
	f.hits(3, "203.0.113.2", "std1")
	time.Sleep(60 * time.Millisecond)
	snd.none(t)
	snd.up.Store(true)
	fr5 := snd.next(t)
	require.True(t, fr5.Snapshot)
	require.Equal(t, []string{"svc"}, fr5.Services)
	require.Equal(t, []string{"ban"}, kinds(fr5.Events), "the pending event and the active ban are one event")
	f.e.Ack(fr5.Seq)
	require.Eventually(t, func() bool { return len(f.events()) == 0 }, 2*time.Second, 10*time.Millisecond)

	// Reconnect with nothing pending: still a snapshot, built from the active set.
	snd.up.Store(false)
	time.Sleep(60 * time.Millisecond)
	snd.up.Store(true)
	fr6 := snd.next(t)
	require.True(t, fr6.Snapshot)
	require.Equal(t, []string{"ban"}, kinds(fr6.Events))
	require.Equal(t, "203.0.113.2/32", fr6.Events[0].Ban.Prefix)
	f.e.Ack(fr6.Seq)
	snd.none(t)
}

func TestSnapshotWithNoServicesSendsNothing(t *testing.T) {
	snd := newFakeSender()
	f := newFixture(t, func(c *Config) { c.Sender = snd })
	f.run()
	snd.none(t)
}

func TestRunSweepsExpiredBans(t *testing.T) {
	f := newFixture(t, func(c *Config) { c.SweepInterval = 10 * time.Millisecond })
	f.policies(policy(3))
	f.hits(3, "203.0.113.1", "std1")
	f.run()
	f.clk.Advance(2 * time.Hour)
	require.Eventually(t, func() bool { return len(f.e.Active()) == 0 && countKind(f.events(), "end:expired") == 1 }, 2*time.Second, 10*time.Millisecond)
}

func TestSweepDropsIdleStrikes(t *testing.T) {
	f := newFixture(t)
	f.policies(policy(3))
	f.hits(2, "203.0.113.1", "std1")
	f.clk.Advance(11 * time.Minute)
	f.e.Sweep()
	f.e.mu.Lock()
	defer f.e.mu.Unlock()
	require.Empty(t, f.e.strikes)
	require.Zero(t, f.e.slots)
}
