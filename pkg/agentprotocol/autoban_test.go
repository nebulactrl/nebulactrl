package agentprotocol

import (
	"encoding/json"
	"net/netip"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func validAutoBan() AutoBan {
	return AutoBan{Enabled: true, Threshold: 10, WindowSeconds: 600, DurationSeconds: 3600, Allow: []string{"203.0.113.0/24", "198.51.100.7", "2001:db8::/32"}}
}

func TestAutoBanValidateAcceptsLimitsInclusive(t *testing.T) {
	require.NoError(t, validAutoBan().Validate())
	for name, mut := range map[string]func(*AutoBan){
		"min": func(a *AutoBan) {
			a.Threshold, a.WindowSeconds, a.DurationSeconds = MinAutoBanThreshold, MinAutoBanWindow, MinAutoBanDuration
		},
		"max": func(a *AutoBan) {
			a.Threshold, a.WindowSeconds, a.DurationSeconds = MaxAutoBanThreshold, MaxAutoBanWindow, MaxAutoBanDuration
		},
		"disabled keeps limits": func(a *AutoBan) { a.Enabled = false },
		"no allow list":         func(a *AutoBan) { a.Allow = nil },
	} {
		t.Run(name, func(t *testing.T) {
			a := validAutoBan()
			mut(&a)
			require.NoError(t, a.Validate())
		})
	}
	// The pinned numbers of the contract.
	require.Equal(t, [6]int{3, 1000, 60, 86400, 300, 604800}, [6]int{
		MinAutoBanThreshold, MaxAutoBanThreshold, MinAutoBanWindow, MaxAutoBanWindow, MinAutoBanDuration, MaxAutoBanDuration})
}

func TestAutoBanValidateRejections(t *testing.T) {
	tooMany := make([]string, MaxAccessCIDRs+1)
	for i := range tooMany {
		tooMany[i] = "10.0." + strconv.Itoa(i/256) + "." + strconv.Itoa(i%256)
	}
	for name, tc := range map[string]struct {
		mut  func(*AutoBan)
		want string
	}{
		"threshold below":  {func(a *AutoBan) { a.Threshold = MinAutoBanThreshold - 1 }, "threshold"},
		"threshold above":  {func(a *AutoBan) { a.Threshold = MaxAutoBanThreshold + 1 }, "threshold"},
		"threshold zero":   {func(a *AutoBan) { a.Threshold = 0 }, "threshold"},
		"window below":     {func(a *AutoBan) { a.WindowSeconds = MinAutoBanWindow - 1 }, "window"},
		"window above":     {func(a *AutoBan) { a.WindowSeconds = MaxAutoBanWindow + 1 }, "window"},
		"duration below":   {func(a *AutoBan) { a.DurationSeconds = MinAutoBanDuration - 1 }, "duration"},
		"duration above":   {func(a *AutoBan) { a.DurationSeconds = MaxAutoBanDuration + 1 }, "duration"},
		"allow too many":   {func(a *AutoBan) { a.Allow = tooMany }, "at most"},
		"allow not an ip":  {func(a *AutoBan) { a.Allow = []string{"example.com"} }, "not a CIDR or IP"},
		"allow bad prefix": {func(a *AutoBan) { a.Allow = []string{"10.0.0.0/40"} }, "not a CIDR or IP"},
		"allow blank":      {func(a *AutoBan) { a.Allow = []string{" "} }, "not a CIDR or IP"},
	} {
		t.Run(name, func(t *testing.T) {
			a := validAutoBan()
			tc.mut(&a)
			err := a.Validate()
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.want)
		})
	}

	a := validAutoBan()
	a.Allow = tooMany[:MaxAccessCIDRs]
	require.NoError(t, a.Validate(), "exactly MaxAccessCIDRs entries is fine")
}

func TestAutoBanNormalizeCanonicalPrefixes(t *testing.T) {
	a := AutoBan{Allow: []string{
		"203.0.113.77/24",    // masked
		"198.51.100.7",       // bare v4
		"198.51.100.7/32",    // duplicate of the bare address
		"2001:db8::1",        // bare v6
		"2001:db8:0:1::5/64", // masked
		"::ffff:192.0.2.9",   // mapped, becomes v4
		"::ffff:192.0.2.0/120",
		"not-an-ip", // kept for Validate
	}}
	got := a.Normalize()
	require.Equal(t, []string{
		"203.0.113.0/24", "198.51.100.7/32", "2001:db8::1/128", "2001:db8:0:1::/64",
		"192.0.2.9/32", "192.0.2.0/24", "not-an-ip",
	}, got.Allow)
	require.Equal(t, "203.0.113.77/24", a.Allow[0])
	require.Nil(t, AutoBan{}.Normalize().Allow)

	// Idempotent.
	require.Equal(t, got, got.Normalize())
}

func TestAutoBanAllows(t *testing.T) {
	a := validAutoBan()
	for addr, want := range map[string]bool{
		"203.0.113.9":         true,
		"203.0.114.9":         false,
		"198.51.100.7":        true,
		"198.51.100.8":        false,
		"2001:db8:1:2::1":     true,
		"2001:db9::1":         false,
		"::ffff:203.0.113.9":  true, // mapped form of an allowed v4 address
		"::ffff:198.51.100.8": false,
	} {
		require.Equal(t, want, a.Allows(netip.MustParseAddr(addr)), addr)
	}
	require.False(t, a.Allows(netip.Addr{}), "invalid address")
	require.False(t, AutoBan{}.Allows(netip.MustParseAddr("203.0.113.9")), "empty list")
	require.True(t, AutoBan{Allow: []string{"::ffff:203.0.113.0/120"}}.Allows(netip.MustParseAddr("203.0.113.9")), "mapped entry")
	require.True(t, AutoBan{Allow: []string{"fe80::/10"}}.Allows(netip.MustParseAddr("fe80::1%eth0")), "zone ignored")
}

func TestBanPrefix(t *testing.T) {
	for in, want := range map[string]string{
		"203.0.113.7":                "203.0.113.7/32",
		"0.0.0.0":                    "0.0.0.0/32",
		"2001:db8:1:2:3:4:5:6":       "2001:db8:1:2::/64",
		"2001:db8:1:2:ffff:ffff::1":  "2001:db8:1:2::/64",
		"2001:db8:1:3::1":            "2001:db8:1:3::/64",
		"::ffff:203.0.113.7":         "203.0.113.7/32",
		"::ffff:10.1.2.3":            "10.1.2.3/32",
		"fe80::1%eth0":               "fe80::/64",
		"::1":                        "::/64",
		"2001:db8:1:2::":             "2001:db8:1:2::/64",
		"64:ff9b::203.0.113.7":       "64:ff9b::/64",
		"2001:db8:aaaa:bbbb:cccc::1": "2001:db8:aaaa:bbbb::/64",
	} {
		got := BanPrefix(netip.MustParseAddr(in))
		require.Equal(t, want, got.String(), in)
		require.Equal(t, got, got.Masked(), in)
	}
	require.Equal(t, netip.Prefix{}, BanPrefix(netip.Addr{}))

	// Two addresses of one /64 share a key; neighbouring /64s do not.
	a := BanPrefix(netip.MustParseAddr("2001:db8:1:2::1"))
	require.Equal(t, a, BanPrefix(netip.MustParseAddr("2001:db8:1:2:dead:beef:0:1")))
	require.NotEqual(t, a, BanPrefix(netip.MustParseAddr("2001:db8:1:3::1")))
	require.True(t, a.Contains(netip.MustParseAddr("2001:db8:1:2:9::9")))
}

func TestExemptAddr(t *testing.T) {
	for addr, want := range map[string]bool{
		// loopback
		"127.0.0.1": true, "127.255.255.254": true, "::1": true,
		// private
		"10.0.0.1": true, "10.255.255.255": true,
		"172.16.0.1": true, "172.31.255.255": true, "172.15.255.255": false, "172.32.0.1": false,
		"192.168.1.1": true, "192.167.255.255": false, "192.169.0.1": false,
		"fc00::1": true, "fd12:3456::1": true, "fe00::1": false,
		// CGNAT
		"100.64.0.1": true, "100.127.255.255": true, "100.63.255.255": false, "100.128.0.1": false,
		// link-local
		"169.254.1.1": true, "169.253.0.1": false, "fe80::1": true, "febf::1": true, "fec0::1": false,
		"fe80::1%eth0": true,
		// unspecified
		"0.0.0.0": true, "::": true,
		// multicast
		"224.0.0.1": true, "239.255.255.250": true, "ff02::1": true, "ff0e::1": true,
		// IPv4-mapped forms follow the v4 address
		"::ffff:10.0.0.1": true, "::ffff:127.0.0.1": true, "::ffff:203.0.113.7": false,
		// public
		"203.0.113.7": false, "8.8.8.8": false, "1.1.1.1": false, "2001:db8::1": false, "2606:4700::1111": false,
	} {
		require.Equal(t, want, ExemptAddr(netip.MustParseAddr(addr)), addr)
	}
	require.True(t, ExemptAddr(netip.Addr{}), "an invalid address is never banned")
}

func TestAutoBanContractNames(t *testing.T) {
	require.Equal(t, "bans", TypeBans)
	require.Equal(t, "bans_ack", TypeBansAck)
	require.Equal(t, "autoban_unban", KindAutoBanUnban)
	require.True(t, AllowedKinds[KindAutoBanUnban])
	require.Equal(t, "proxy.autoban", CapAutoBan)
	require.Equal(t, "autoban", FirewallRuleAutoBan)
}

func TestAutoBanJSONRoundTrip(t *testing.T) {
	a := validAutoBan()
	raw, err := json.Marshal(a)
	require.NoError(t, err)
	require.JSONEq(t, `{"enabled":true,"threshold":10,"windowSeconds":600,"durationSeconds":3600,"allow":["203.0.113.0/24","198.51.100.7","2001:db8::/32"]}`, string(raw))
	var back AutoBan
	require.NoError(t, json.Unmarshal(raw, &back))
	require.Equal(t, a, back)

	raw, err = json.Marshal(AutoBan{Threshold: 3, WindowSeconds: 60, DurationSeconds: 300})
	require.NoError(t, err)
	require.JSONEq(t, `{"enabled":false,"threshold":3,"windowSeconds":60,"durationSeconds":300}`, string(raw), "allow omitted when empty")

	// RouteExt embeds the policy flat and omits it when nil.
	r := Route{RouteExt: RouteExt{AutoBan: &a}}
	raw, err = json.Marshal(r)
	require.NoError(t, err)
	require.Contains(t, string(raw), `"autoBan":{"enabled":true`)
	var rb Route
	require.NoError(t, json.Unmarshal(raw, &rb))
	require.Equal(t, &a, rb.AutoBan)
	raw, err = json.Marshal(Route{})
	require.NoError(t, err)
	require.NotContains(t, string(raw), "autoBan")
}

func TestBanFramesJSONRoundTrip(t *testing.T) {
	at := time.Date(2026, 11, 23, 9, 0, 0, 0, time.UTC)
	ban := Ban{
		ID: "b1", ServiceID: "svc", RouteID: "route-1", Prefix: "2001:db8:1:2::/64",
		StartedAt: at, ExpiresAt: at.Add(time.Hour), Strikes: 10, Rules: map[string]int{"stdsecrets": 7, "bots": 3},
	}
	frame := BanFrame{
		Seq: 7,
		Events: []BanEvent{
			{EventID: "e1", Kind: BanEventBan, At: at, Ban: ban},
			{EventID: "e2", Kind: BanEventEnd, At: at.Add(time.Hour), Reason: BanEndExpired, Ban: ban},
		},
		Snapshot: true,
		Services: []string{"svc"},
	}
	raw, err := json.Marshal(frame)
	require.NoError(t, err)
	var back BanFrame
	require.NoError(t, json.Unmarshal(raw, &back))
	require.Equal(t, frame, back)
	require.Contains(t, string(raw), `"eventId":"e1"`)
	require.Contains(t, string(raw), `"expiresAt":"2026-11-23T10:00:00Z"`)

	raw, err = json.Marshal(BanFrame{Seq: 1, Events: []BanEvent{{EventID: "e", Kind: BanEventBan, At: at, Ban: Ban{ID: "b", Prefix: "203.0.113.7/32"}}}})
	require.NoError(t, err)
	require.NotContains(t, string(raw), "snapshot")
	require.NotContains(t, string(raw), "services")
	require.NotContains(t, string(raw), `"rules"`)
	require.NotContains(t, string(raw), `"reason"`)

	raw, err = json.Marshal(BanAck{Seq: 9})
	require.NoError(t, err)
	require.JSONEq(t, `{"seq":9}`, string(raw))

	raw, err = json.Marshal(AutoBanUnbanPayload{ServiceID: "svc", BanID: "b1"})
	require.NoError(t, err)
	require.JSONEq(t, `{"serviceId":"svc","banId":"b1"}`, string(raw))
	var p AutoBanUnbanPayload
	require.NoError(t, json.Unmarshal(raw, &p))
	require.Equal(t, AutoBanUnbanPayload{ServiceID: "svc", BanID: "b1"}, p)
}

func TestGuardBansJSON(t *testing.T) {
	doc := `{"services":{"r1":{"allow":["10.0.0.0/8"],"bans":[{"prefix":"203.0.113.7/32","expiresAt":"2026-11-23T10:00:00Z"}]}}}`
	var g GuardBans
	require.NoError(t, json.Unmarshal([]byte(doc), &g))
	require.Equal(t, GuardBans{Services: map[string]GuardRouteBans{"r1": {
		Allow: []string{"10.0.0.0/8"},
		Bans:  []GuardBan{{Prefix: "203.0.113.7/32", ExpiresAt: time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC)}},
	}}}, g)
	raw, err := json.Marshal(g)
	require.NoError(t, err)
	require.JSONEq(t, doc, string(raw))

	raw, err = json.Marshal(GuardBans{Services: map[string]GuardRouteBans{"r2": {}}})
	require.NoError(t, err)
	require.JSONEq(t, `{"services":{"r2":{}}}`, string(raw))
}
