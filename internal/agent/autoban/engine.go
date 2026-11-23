// Package autoban is the agent side of the firewall auto-ban: it counts the
// requests the firewall blocked per visitor address (strikes), bans an address
// that trips Threshold strikes within the policy's window, and keeps the bans
// durable, enforced (the edge guard reads Bans) and reported to the control
// plane (BanFrame events, resent until acknowledged).
package autoban

import (
	"container/list"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/agent/store"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Bounds of the engine's memory and of the unacknowledged event queue.
const (
	// MaxStrikeKeys bounds the (route, address prefix) pairs with strikes; the
	// least recently struck is evicted first.
	MaxStrikeKeys = 100_000
	// MaxStrikeSlots bounds the strikes held over all keys (8 bytes each); a
	// threshold of 1000 would otherwise let the keys hold 100 million.
	MaxStrikeSlots = 2_000_000
	// MaxBansPerService and MaxBansPerAgent bound the active bans. Past a
	// limit nobody is banned and a warning is logged at most once a minute.
	MaxBansPerService = 5_000
	MaxBansPerAgent   = 20_000
	// MaxOutbox bounds the ban events kept for an unreachable control plane.
	MaxOutbox = 50_000

	// SweepInterval is how often expired bans and idle strikes are dropped.
	SweepInterval = 30 * time.Second
	// DebounceDelay delays the change notification so a burst of bans is one push.
	DebounceDelay = 200 * time.Millisecond

	sendInterval = 2 * time.Second
	ackWait      = 30 * time.Second
	framePage    = 200
	warnEvery    = time.Minute
	maxRuleIdx   = 4096
)

// ErrNotFound is returned by Unban for a ban that is not active.
var ErrNotFound = errors.New("ban not found")

// Policy is the auto-ban policy of one route, with the rules that count.
type Policy struct {
	// ServiceID owns the route; it is reported on the route's bans.
	ServiceID string
	AutoBan   agentprotocol.AutoBan
	// Strikes reports whether a request blocked by the rule id counts as a
	// strike. nil counts the standard protection only.
	Strikes func(ruleID string) bool
}

// PoliciesFromRoutes returns the policies of the routes that have an enabled
// auto-ban, keyed by route id (Route.ServiceID, the id of the guard route).
// A strike is a request blocked by the standard protection (id "std…") or by
// a block rule with a condition on something other than the address or the
// country: those two are policy, not attacks. Allow rules never strike.
func PoliciesFromRoutes(routes []agentprotocol.Route) map[string]Policy {
	out := map[string]Policy{}
	for _, r := range routes {
		if r.IsL4() || r.AutoBan == nil || !r.AutoBan.Enabled {
			continue
		}
		out[r.ServiceID] = Policy{ServiceID: r.ServiceID, AutoBan: *r.AutoBan, Strikes: StrikeRules(r.Firewall)}
	}
	return out
}

// StrikeRules returns the strike predicate of a firewall (see PoliciesFromRoutes).
func StrikeRules(fw *agentprotocol.Firewall) func(ruleID string) bool {
	ids := map[string]bool{}
	for _, r := range fw.Active() {
		if r.Action != agentprotocol.FirewallBlock {
			continue
		}
		for _, c := range r.Conditions {
			if c.Field != agentprotocol.FirewallFieldIP && c.Field != agentprotocol.FirewallFieldCountry {
				ids[r.ID] = true
				break
			}
		}
	}
	return func(id string) bool { return isStandard(id) || ids[id] }
}

func isStandard(id string) bool { return strings.HasPrefix(id, "std") }

// Hit is an access-log request the engine looks at. RouteID is the id of the
// route that served it: the service id of its router (traffic.Event.ServiceID).
type Hit struct {
	RouteID   string
	BlockedBy string // id of the firewall rule that blocked the request
	ClientIP  string
	Status    int
	Time      time.Time // request start
}

// Store is the durable state; *store.Store implements it.
type Store interface {
	LoadBans() ([]agentprotocol.Ban, error)
	ApplyBans(store.BanChange) error
	PendingBanEvents(limit int) (keys [][]byte, events []agentprotocol.BanEvent, err error)
	DeleteBanEvents(keys [][]byte) error
	TrimBanEvents(keep int) (int, error)
}

// Sender queues a ban frame for the control plane. *conn.Client implements it.
type Sender interface {
	AutoBans(agentprotocol.BanFrame)
	Connected() bool
}

// Limits overrides the package bounds (tests); zero keeps the default.
type Limits struct {
	StrikeKeys, StrikeSlots, PerService, PerAgent, Outbox int
}

// Config configures an Engine. Store is required.
type Config struct {
	Store  Store
	Sender Sender // nil: events stay in the outbox
	Logger *slog.Logger
	Now    func() time.Time
	// Log receives one line per ban start and end for the owning service
	// (the service's log history). nil skips it.
	Log    func(serviceID, line string)
	Limits Limits
	// Debounce, SweepInterval, SendInterval and AckTimeout override the
	// defaults (tests).
	Debounce, SweepInterval, SendInterval, AckTimeout time.Duration
}

type policyState struct {
	Policy
	allow    []netip.Prefix
	allowStr []string
}

func (p *policyState) allows(a netip.Addr) bool {
	for _, pr := range p.allow {
		if pr.Contains(a) {
			return true
		}
	}
	return false
}

// covers reports whether the allow list contains the whole ban prefix.
func (p *policyState) covers(b netip.Prefix) bool {
	for _, pr := range p.allow {
		if pr.Bits() <= b.Bits() && pr.Contains(b.Addr()) {
			return true
		}
	}
	return false
}

type strikeKey struct {
	route  string
	prefix netip.Prefix
}

type banKey = strikeKey

// strike is one counted request: seconds since strikeEpoch and the interned
// rule id.
type strike struct {
	at   uint32
	rule uint16
}

var strikeEpoch = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)

type strikeEntry struct {
	key     strikeKey
	strikes []strike
}

type banState struct {
	agentprotocol.Ban
	prefix netip.Prefix
}

// batch collects what one operation changes, to be stored once and reported
// after the engine's lock is released.
type batch struct {
	change store.BanChange
	logs   []logLine
}

type logLine struct{ service, text string }

// Engine detects, holds and reports bans. Create it with New and run it with Run.
type Engine struct {
	cfg Config
	log *slog.Logger
	now func() time.Time
	lim Limits

	mu          sync.Mutex
	policies    map[string]*policyState
	policiesSet bool
	strikes     map[strikeKey]*list.Element
	lru         *list.List // of *strikeEntry, most recently struck first
	slots       int
	ruleIdx     map[string]uint16
	ruleNames   []string
	bans        map[banKey]*banState
	byID        map[string]*banState
	perRoute    map[string]int
	warnAt      time.Time
	refused     int // bans not made since the last warning

	cmu      sync.Mutex // guards the change notification
	onChange func()
	timer    *time.Timer

	kick chan struct{}
	acks chan uint64
	seq  uint64 // owned by the send loop
}

// New returns an Engine holding the bans stored by an earlier run. A ban that
// expired meanwhile is ended (reason expired) and reported.
func New(cfg Config) *Engine {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	lim := cfg.Limits
	for _, d := range []struct {
		v *int
		d int
	}{{&lim.StrikeKeys, MaxStrikeKeys}, {&lim.StrikeSlots, MaxStrikeSlots}, {&lim.PerService, MaxBansPerService}, {&lim.PerAgent, MaxBansPerAgent}, {&lim.Outbox, MaxOutbox}} {
		if *d.v <= 0 {
			*d.v = d.d
		}
	}
	for _, d := range []struct {
		v *time.Duration
		d time.Duration
	}{{&cfg.Debounce, DebounceDelay}, {&cfg.SweepInterval, SweepInterval}, {&cfg.SendInterval, sendInterval}, {&cfg.AckTimeout, ackWait}} {
		if *d.v <= 0 {
			*d.v = d.d
		}
	}
	e := &Engine{
		cfg: cfg, log: cfg.Logger.With("component", "autoban"), now: now, lim: lim,
		policies: map[string]*policyState{},
		strikes:  map[strikeKey]*list.Element{}, lru: list.New(),
		ruleIdx: map[string]uint16{}, ruleNames: []string{""},
		bans: map[banKey]*banState{}, byID: map[string]*banState{}, perRoute: map[string]int{},
		kick: make(chan struct{}, 1), acks: make(chan uint64, 1),
	}
	e.load()
	return e
}

func (e *Engine) load() {
	stored, err := e.cfg.Store.LoadBans()
	if err != nil {
		e.log.Error("load bans", "err", err)
		return
	}
	now := e.now()
	var bt batch
	e.mu.Lock()
	for _, b := range stored {
		p, err := netip.ParsePrefix(b.Prefix)
		if err != nil || b.ID == "" || b.RouteID == "" {
			bt.change.Delete = append(bt.change.Delete, b) // unusable: forget it
			continue
		}
		bs := &banState{Ban: b, prefix: p}
		if old := e.bans[banKey{b.RouteID, p}]; old != nil {
			e.endLocked(old, agentprotocol.BanEndLifted, now, &bt)
		}
		e.bans[banKey{b.RouteID, p}] = bs
		e.byID[b.ID] = bs
		e.perRoute[b.RouteID]++
		if !now.Before(b.ExpiresAt) {
			e.endLocked(bs, agentprotocol.BanEndExpired, b.ExpiresAt, &bt)
		}
	}
	e.commitLocked(&bt)
	e.mu.Unlock()
	e.finish(&bt)
}

// OnChange sets the function called, debounced, after the content of Bans
// may have changed. The proxy manager uses it to push the bans file.
func (e *Engine) OnChange(fn func()) {
	e.cmu.Lock()
	e.onChange = fn
	e.cmu.Unlock()
}

func (e *Engine) notify() {
	e.cmu.Lock()
	defer e.cmu.Unlock()
	if e.timer != nil {
		return
	}
	e.timer = time.AfterFunc(e.cfg.Debounce, func() {
		e.cmu.Lock()
		e.timer = nil
		fn := e.onChange
		e.cmu.Unlock()
		if fn != nil {
			fn()
		}
	})
}

// SetRoutes is SetPolicies(PoliciesFromRoutes(routes)): the proxy manager
// calls it with the route table it has applied.
func (e *Engine) SetRoutes(routes []agentprotocol.Route) { e.SetPolicies(PoliciesFromRoutes(routes)) }

// SetPolicies replaces the policies, keyed by route id. A route that lost its
// policy (disabled, or gone from the table) has its bans lifted, as has one
// whose allow list now covers a ban.
func (e *Engine) SetPolicies(policies map[string]Policy) {
	next := make(map[string]*policyState, len(policies))
	for id, p := range policies {
		if !p.AutoBan.Enabled {
			continue
		}
		next[id] = compile(p)
	}
	now := e.now()
	var bt batch
	e.mu.Lock()
	changed := len(next) != len(e.policies)
	for id, p := range next {
		if old := e.policies[id]; old == nil || !slices.Equal(old.allowStr, p.allowStr) {
			changed = true
		}
	}
	first := !e.policiesSet
	e.policies, e.policiesSet = next, true
	var lift []*banState
	for _, b := range e.bans {
		if p := next[b.RouteID]; p == nil || p.covers(b.prefix) {
			lift = append(lift, b)
		}
	}
	for _, b := range lift {
		e.endLocked(b, agentprotocol.BanEndLifted, now, &bt)
	}
	for el := e.lru.Front(); el != nil; {
		nxt := el.Next()
		if ent := el.Value.(*strikeEntry); e.policies[ent.key.route] == nil {
			e.dropStrikesLocked(el)
		}
		el = nxt
	}
	e.commitLocked(&bt)
	e.mu.Unlock()
	e.finish(&bt)
	if changed || first {
		e.notify()
	}
}

func compile(p Policy) *policyState {
	ps := &policyState{Policy: p}
	for _, s := range p.AutoBan.Normalize().Allow {
		if pr, err := netip.ParsePrefix(s); err == nil {
			pr = pr.Masked()
			ps.allow = append(ps.allow, pr)
			ps.allowStr = append(ps.allowStr, pr.String())
		}
	}
	if ps.Strikes == nil {
		ps.Strikes = isStandard
	}
	return ps
}

// Observe counts one access-log request. The caller applies the same replay
// gate as the traffic aggregation, so a restart that re-reads the log does not
// count a request twice. Only a 403 blocked by a rule that counts, for a
// visitor that is neither exempt nor allow-listed nor already banned, is a
// strike.
func (e *Engine) Observe(h Hit) {
	if h.Status != 403 || h.BlockedBy == "" || h.BlockedBy == agentprotocol.FirewallRuleAutoBan {
		return
	}
	addr, err := netip.ParseAddr(h.ClientIP)
	if err != nil {
		return
	}
	addr = addr.WithZone("").Unmap()
	if agentprotocol.ExemptAddr(addr) {
		return
	}
	now := e.now()
	var bt batch
	e.mu.Lock()
	e.observeLocked(h, addr, now, &bt)
	e.commitLocked(&bt)
	e.mu.Unlock()
	e.finish(&bt)
}

func (e *Engine) observeLocked(h Hit, addr netip.Addr, now time.Time, bt *batch) {
	p := e.policies[h.RouteID]
	if p == nil || !p.Strikes(h.BlockedBy) || p.allows(addr) {
		return
	}
	prefix := agentprotocol.BanPrefix(addr)
	if b := e.bans[banKey{h.RouteID, prefix}]; b != nil {
		if now.Before(b.ExpiresAt) {
			return
		}
		e.endLocked(b, agentprotocol.BanEndExpired, b.ExpiresAt, bt)
	}
	window := time.Duration(p.AutoBan.WindowSeconds) * time.Second
	t := h.Time
	if t.IsZero() || t.After(now) {
		t = now
	}
	if t.Before(now.Add(-window)) {
		return // a replayed request older than the window
	}
	key := strikeKey{h.RouteID, prefix}
	var ent *strikeEntry
	if el := e.strikes[key]; el != nil {
		e.lru.MoveToFront(el)
		ent = el.Value.(*strikeEntry)
	} else {
		ent = &strikeEntry{key: key}
		e.strikes[key] = e.lru.PushFront(ent)
	}
	// Drop what left the window, then add.
	cut := toStrikeTime(t.Add(-window))
	n := 0
	for n < len(ent.strikes) && ent.strikes[n].at < cut {
		n++
	}
	if n > 0 {
		ent.strikes = ent.strikes[:copy(ent.strikes, ent.strikes[n:])]
		e.slots -= n
	}
	ent.strikes = append(ent.strikes, strike{at: toStrikeTime(t), rule: e.ruleIndex(h.BlockedBy)})
	e.slots++
	if over := len(ent.strikes) - p.AutoBan.Threshold; over > 0 {
		ent.strikes = ent.strikes[:copy(ent.strikes, ent.strikes[over:])]
		e.slots -= over
	}
	if len(ent.strikes) >= p.AutoBan.Threshold {
		e.banLocked(p, h.RouteID, prefix, ent, now, bt)
	}
	for (len(e.strikes) > e.lim.StrikeKeys || e.slots > e.lim.StrikeSlots) && e.lru.Len() > 1 {
		e.dropStrikesLocked(e.lru.Back())
	}
}

func toStrikeTime(t time.Time) uint32 {
	s := int64(t.Sub(strikeEpoch) / time.Second)
	if s < 0 {
		return 0
	}
	return uint32(s)
}

func (e *Engine) ruleIndex(id string) uint16 {
	if i, ok := e.ruleIdx[id]; ok {
		return i
	}
	if len(e.ruleNames) >= maxRuleIdx {
		return 0
	}
	i := uint16(len(e.ruleNames))
	e.ruleNames = append(e.ruleNames, id)
	e.ruleIdx[id] = i
	return i
}

func (e *Engine) dropStrikesLocked(el *list.Element) {
	ent := el.Value.(*strikeEntry)
	e.slots -= len(ent.strikes)
	delete(e.strikes, ent.key)
	e.lru.Remove(el)
}

func (e *Engine) banLocked(p *policyState, route string, prefix netip.Prefix, ent *strikeEntry, now time.Time, bt *batch) {
	if e.perRoute[route] >= e.lim.PerService || len(e.bans) >= e.lim.PerAgent {
		e.refused++
		if now.Sub(e.warnAt) >= warnEvery {
			e.log.Warn("auto-ban limit reached: addresses are not banned", "service_id", p.ServiceID,
				"service_bans", e.perRoute[route], "agent_bans", len(e.bans), "refused", e.refused)
			e.warnAt, e.refused = now, 0
		}
		return
	}
	rules := map[string]int{}
	for _, s := range ent.strikes {
		rules[e.ruleNames[s.rule]]++
	}
	if n := rules[""]; n > 0 { // interned past the table: reported as other
		delete(rules, "")
		rules["other"] += n
	}
	b := &banState{prefix: prefix, Ban: agentprotocol.Ban{
		ID: uuid.NewString(), ServiceID: p.ServiceID, RouteID: route, Prefix: prefix.String(),
		StartedAt: now, ExpiresAt: now.Add(time.Duration(p.AutoBan.DurationSeconds) * time.Second),
		Strikes: len(ent.strikes), Rules: rules,
	}}
	e.dropStrikesLocked(e.strikes[strikeKey{route, prefix}])
	e.bans[banKey{route, prefix}] = b
	e.byID[b.ID] = b
	e.perRoute[route]++
	bt.change.Put = append(bt.change.Put, b.Ban)
	bt.change.Events = append(bt.change.Events, banEvent(b.Ban, agentprotocol.BanEventBan, "", now))
	bt.logs = append(bt.logs, logLine{b.ServiceID, fmt.Sprintf("auto-ban: banned %s for %s after %d blocked requests (%s)",
		b.Prefix, b.ExpiresAt.Sub(b.StartedAt).Round(time.Second), b.Strikes, ruleList(b.Rules))})
}

func ruleList(rules map[string]int) string {
	ids := make([]string, 0, len(rules))
	for id := range rules {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for i, id := range ids {
		ids[i] = fmt.Sprintf("%s x%d", id, rules[id])
	}
	return "rules: " + strings.Join(ids, ", ")
}

// banEvent builds the event of a ban. Its id derives from the ban id and the
// kind, so a resent or re-derived event is the same event to the control plane.
func banEvent(b agentprotocol.Ban, kind, reason string, at time.Time) agentprotocol.BanEvent {
	return agentprotocol.BanEvent{
		EventID: uuid.NewSHA1(uuid.NameSpaceOID, []byte(b.ID+":"+kind)).String(),
		Kind:    kind, At: at, Reason: reason, Ban: b,
	}
}

// endLocked removes a ban and queues its end event.
func (e *Engine) endLocked(b *banState, reason string, at time.Time, bt *batch) {
	delete(e.bans, banKey{b.RouteID, b.prefix})
	delete(e.byID, b.ID)
	if e.perRoute[b.RouteID]--; e.perRoute[b.RouteID] <= 0 {
		delete(e.perRoute, b.RouteID)
	}
	bt.change.Delete = append(bt.change.Delete, b.Ban)
	bt.change.Events = append(bt.change.Events, banEvent(b.Ban, agentprotocol.BanEventEnd, reason, at))
	bt.logs = append(bt.logs, logLine{b.ServiceID, fmt.Sprintf("auto-ban: %s %s", b.Prefix, endText(reason))})
}

func endText(reason string) string {
	switch reason {
	case agentprotocol.BanEndExpired:
		return "ban expired"
	case agentprotocol.BanEndUnbanned:
		return "unbanned"
	default:
		return "ban lifted (policy changed)"
	}
}

// commitLocked stores a batch. A store error is logged: the in-memory state
// is the truth for this run, and the next change or restart re-derives what it
// can.
func (e *Engine) commitLocked(bt *batch) {
	if err := e.cfg.Store.ApplyBans(bt.change); err != nil {
		e.log.Error("store bans", "err", err)
	}
}

// finish runs what must happen outside the lock: the log lines, the change
// notification and waking the sender.
func (e *Engine) finish(bt *batch) {
	if len(bt.change.Put) == 0 && len(bt.change.Delete) == 0 && len(bt.change.Events) == 0 {
		return
	}
	if e.cfg.Log != nil {
		for _, l := range bt.logs {
			e.cfg.Log(l.service, l.text)
		}
	}
	e.notify()
	if len(bt.change.Events) > 0 {
		select {
		case e.kick <- struct{}{}:
		default:
		}
	}
}

// Unban ends the active ban with the id on the service (reason unbanned).
func (e *Engine) Unban(serviceID, banID string) error {
	var bt batch
	e.mu.Lock()
	b := e.byID[banID]
	if b == nil || b.ServiceID != serviceID {
		e.mu.Unlock()
		return ErrNotFound
	}
	e.endLocked(b, agentprotocol.BanEndUnbanned, e.now(), &bt)
	e.commitLocked(&bt)
	e.mu.Unlock()
	e.finish(&bt)
	return nil
}

// Active returns the active bans, oldest first.
func (e *Engine) Active() []agentprotocol.Ban {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.activeLocked(e.now())
}

func (e *Engine) activeLocked(now time.Time) []agentprotocol.Ban {
	out := make([]agentprotocol.Ban, 0, len(e.bans))
	for _, b := range e.bans {
		if now.Before(b.ExpiresAt) {
			out = append(out, b.Ban)
		}
	}
	slices.SortFunc(out, func(a, b agentprotocol.Ban) int {
		if c := a.StartedAt.Compare(b.StartedAt); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	return out
}

// Bans returns what the edge guard enforces: the allow list of every route
// with an auto-ban and the unexpired bans, in a stable order.
func (e *Engine) Bans() agentprotocol.GuardBans {
	e.mu.Lock()
	defer e.mu.Unlock()
	now := e.now()
	out := agentprotocol.GuardBans{Services: map[string]agentprotocol.GuardRouteBans{}}
	for id, p := range e.policies {
		out.Services[id] = agentprotocol.GuardRouteBans{Allow: slices.Clone(p.allowStr)}
	}
	for _, b := range e.bans {
		if !now.Before(b.ExpiresAt) {
			continue
		}
		r := out.Services[b.RouteID]
		r.Bans = append(r.Bans, agentprotocol.GuardBan{Prefix: b.Prefix, ExpiresAt: b.ExpiresAt})
		out.Services[b.RouteID] = r
	}
	for id, r := range out.Services {
		slices.SortFunc(r.Bans, func(a, b agentprotocol.GuardBan) int { return strings.Compare(a.Prefix, b.Prefix) })
		out.Services[id] = r
	}
	return out
}

// Sweep ends the bans that expired and drops strikes that left their window.
func (e *Engine) Sweep() {
	now := e.now()
	var bt batch
	e.mu.Lock()
	var expired []*banState
	for _, b := range e.bans {
		if !now.Before(b.ExpiresAt) {
			expired = append(expired, b)
		}
	}
	for _, b := range expired {
		e.endLocked(b, agentprotocol.BanEndExpired, b.ExpiresAt, &bt)
	}
	for el := e.lru.Back(); el != nil; {
		prev := el.Prev()
		ent := el.Value.(*strikeEntry)
		p := e.policies[ent.key.route]
		if p == nil || len(ent.strikes) == 0 ||
			ent.strikes[len(ent.strikes)-1].at < toStrikeTime(now.Add(-time.Duration(p.AutoBan.WindowSeconds)*time.Second)) {
			e.dropStrikesLocked(el)
		}
		el = prev
	}
	e.commitLocked(&bt)
	e.mu.Unlock()
	e.finish(&bt)
	if n, err := e.cfg.Store.TrimBanEvents(e.lim.Outbox); err != nil {
		e.log.Warn("trim ban events", "err", err)
	} else if n > 0 {
		e.log.Warn("dropped unsent ban events beyond the bound", "events", n)
	}
}

// SetSender sets the sender of ban frames. Call it before Run.
func (e *Engine) SetSender(s Sender) { e.cfg.Sender = s }

// Ack records the control plane's acknowledgement of a ban frame.
func (e *Engine) Ack(seq uint64) {
	select {
	case e.acks <- seq:
	default:
		select { // keep the newest
		case <-e.acks:
		default:
		}
		select {
		case e.acks <- seq:
		default:
		}
	}
}

// Run sweeps expired bans and delivers ban events to the control plane until
// ctx ends.
func (e *Engine) Run(ctx context.Context) {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		t := time.NewTicker(e.cfg.SweepInterval)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				e.Sweep()
			case <-ctx.Done():
				return
			}
		}
	}()
	e.sendLoop(ctx)
	wg.Wait()
	e.cmu.Lock()
	if e.timer != nil {
		e.timer.Stop()
		e.timer = nil
	}
	e.cmu.Unlock()
}

// sendLoop delivers the outbox while the control plane is connected. The
// first frame of every connection is a snapshot of the active bans, so a
// control plane that missed events (or lost them) converges.
func (e *Engine) sendLoop(ctx context.Context) {
	if e.cfg.Sender == nil {
		<-ctx.Done()
		return
	}
	t := time.NewTicker(e.cfg.SendInterval)
	defer t.Stop()
	needSnap, wasUp := true, false
	for {
		up := e.cfg.Sender.Connected()
		if up && !wasUp {
			needSnap = true
		}
		wasUp = up
		if up {
			for ctx.Err() == nil && e.sendOnce(ctx, &needSnap) {
			}
		}
		select {
		case <-t.C:
		case <-e.kick:
		case <-ctx.Done():
			return
		}
	}
}

// sendOnce sends one frame and waits for its ack. It reports whether more may
// be pending.
func (e *Engine) sendOnce(ctx context.Context, needSnap *bool) bool {
	limit := framePage
	if *needSnap {
		limit = 0
	}
	keys, events, err := e.cfg.Store.PendingBanEvents(limit)
	if err != nil {
		e.log.Error("read ban events", "err", err)
		return false
	}
	frame := agentprotocol.BanFrame{Events: events}
	if *needSnap {
		frame = e.snapshotFrame(events)
		frame.Snapshot = len(frame.Services) > 0
	}
	if len(frame.Events) == 0 && !frame.Snapshot {
		*needSnap = false
		return false
	}
	e.seq++
	frame.Seq = e.seq
	select { // forget a stale ack
	case <-e.acks:
	default:
	}
	e.cfg.Sender.AutoBans(frame)
	if !e.waitAck(ctx, frame.Seq) {
		return false
	}
	if err := e.cfg.Store.DeleteBanEvents(keys); err != nil {
		e.log.Error("delete sent ban events", "err", err)
		return false
	}
	if frame.Snapshot {
		*needSnap = false
	}
	return len(keys) >= framePage
}

// snapshotFrame adds to the pending events a ban event for every active ban
// that is not among them, and lists the services the snapshot covers: those
// with an auto-ban policy and those holding a ban.
func (e *Engine) snapshotFrame(pending []agentprotocol.BanEvent) agentprotocol.BanFrame {
	have := make(map[string]bool, len(pending))
	for _, ev := range pending {
		have[ev.EventID] = true
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	svcs := map[string]bool{}
	for _, p := range e.policies {
		if p.ServiceID != "" {
			svcs[p.ServiceID] = true
		}
	}
	events := slices.Clone(pending)
	for _, b := range e.activeLocked(e.now()) {
		svcs[b.ServiceID] = true
		if ev := banEvent(b, agentprotocol.BanEventBan, "", b.StartedAt); !have[ev.EventID] {
			events = append(events, ev)
		}
	}
	services := make([]string, 0, len(svcs))
	for s := range svcs {
		services = append(services, s)
	}
	slices.Sort(services)
	return agentprotocol.BanFrame{Events: events, Services: services}
}

func (e *Engine) waitAck(ctx context.Context, seq uint64) bool {
	timer := time.NewTimer(e.cfg.AckTimeout)
	defer timer.Stop()
	for {
		select {
		case got := <-e.acks:
			if got == seq {
				return true
			}
		case <-timer.C:
			e.log.Warn("ban frame not acknowledged; will resend", "seq", seq)
			return false
		case <-ctx.Done():
			return false
		}
	}
}
