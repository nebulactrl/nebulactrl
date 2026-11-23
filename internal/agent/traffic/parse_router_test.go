package traffic

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestServiceFromRouterPerDomainNames(t *testing.T) {
	type want struct {
		redirect, ok bool
	}
	cases := map[string]want{
		// per-domain routers
		"nc-" + svcA + "-daaaaaaaa-https@rest":       {false, true},
		"nc-" + svcA + "-d1b2c3d4e-https-local@rest": {false, true},
		"nc-" + svcA + "-dAbCd1234-https":            {false, true},
		"nc-" + svcA + "-daaaaaaaa-http@rest":        {true, true},
		"nc-" + svcA + "-daaaaaaaa-http-local@rest":  {true, true},
		"nc-" + svcA + "-daaaaaaaa-redir@rest":       {true, true},
		"nc-" + svcA + "-daaaaaaaa-tunnel@rest":      {false, true},
		"nc-pages-notfound-tunnel@rest":              {},
		// L4
		"nc-" + svcA + "-tcp@rest": {false, true},
		"nc-" + svcA + "-udp@rest": {false, true},
		// service-level routers of hosts without a domain rule
		"nc-" + svcA + "-https@rest":       {false, true},
		"nc-" + svcA + "-https-local@file": {false, true},
		"nc-" + svcA + "-http@file":        {true, true},
		"nc-" + svcA + "@file":             {false, true},
		// never a visit
		"nc-" + svcA + "-daaaaaaaa-probe@rest": {},
		"nc-" + svcA + "-probe@rest":           {},
		"nc-" + svcA + "-d-https@rest":         {},
		"nc-" + svcA + "-dshort-https@rest":    {},
		"nc-" + svcA + "-da`aaaaaa-https@rest": {},
		"nc-" + svcA + "-daaaaaaaa@rest":       {},
		"nc-" + svcA + "-daaaaaaaa-https-x":    {},
		"nc-gen@rest":                          {},
		"nc-https-redirect@rest":               {},
	}
	for name, w := range cases {
		id, redirect, fw, ok := serviceFromRouter(name)
		require.Equal(t, w.ok, ok, name)
		if w.ok {
			require.Equal(t, svcA, id, name)
			require.Equal(t, w.redirect, redirect, name)
		}
		require.Empty(t, fw, name, "no firewall router here")
	}
}

func TestServiceFromRouterFirewallRouters(t *testing.T) {
	type want struct {
		redirect bool
		fw       string
		ok       bool
	}
	cases := map[string]want{
		// firewallRouters names a copy <base>-fw<ruleId>
		"nc-" + svcA + "-https-fwabc123@file":                 {false, "abc123", true},
		"nc-" + svcA + "-https-local-fwabc123@file":           {false, "abc123", true},
		"nc-" + svcA + "-http-fwabc123@file":                  {true, "abc123", true},
		"nc-" + svcA + "-daaaaaaaa-https-fwabc123@file":       {false, "abc123", true},
		"nc-" + svcA + "-daaaaaaaa-https-local-fwabc123@file": {false, "abc123", true},
		"nc-" + svcA + "-daaaaaaaa-tunnel-fwabc123@file":      {false, "abc123", true},
		"nc-" + svcA + "-daaaaaaaa-http-fwabc123@file":        {true, "abc123", true},
		"nc-" + svcA + "-https-fwstdsecrets@file":             {false, "stdsecrets", true},
		"nc-" + svcA + "-https-fw0123456789abcdef@file":       {false, "0123456789abcdef", true},
		// a rule id starting with the d of a per-domain id does not confuse the parse
		"nc-" + svcA + "-https-fwd1@file": {false, "d1", true},
		// not a firewall router
		"nc-" + svcA + "-https-fw@file":                  {},
		"nc-" + svcA + "-https-fwABC@file":               {},
		"nc-" + svcA + "-https-fw0123456789abcdefg@file": {},
		"nc-" + svcA + "-https-fwa-b@file":               {},
		"nc-" + svcA + "-fwabc@file":                     {},
		"nc-" + svcA + "-probe-fwabc@file":               {},
		"nc-" + svcA + "-https-fwabc-https@file":         {},
	}
	for name, w := range cases {
		id, redirect, fw, ok := serviceFromRouter(name)
		require.Equal(t, w.ok, ok, name)
		if !w.ok {
			continue
		}
		require.Equal(t, svcA, id, name)
		require.Equal(t, w.redirect, redirect, name)
		require.Equal(t, w.fw, fw, name)
	}
}

func TestParseLineFirewallBlocks(t *testing.T) {
	svc := "nc-" + svcA + "@file"
	const rk = "nc-" + svcA + "-https-fwabc123@file"

	// A compiled block rule: the router copy ends at the pages server's 403 page
	// (or noop@internal without pages). The request is the service's, not unrouted.
	for _, blocked := range []string{"nc-pages@file", "noop@internal"} {
		ev, err := ParseLine(accessJSON(t, rk, blocked, 403))
		require.NoError(t, err, blocked)
		require.Equal(t, svcA, ev.ServiceID, blocked)
		require.Equal(t, "abc123", ev.BlockedBy, blocked)
		require.Equal(t, "/x", ev.Path, "the stored path is the one requested, not the rewritten /err/403")
	}
	// A block on the http router counts too, though that router redirects otherwise.
	ev, err := ParseLine(accessJSON(t, "nc-"+svcA+"-http-fwabc123@file", "nc-pages@file", 403))
	require.NoError(t, err)
	require.Equal(t, svcA, ev.ServiceID)
	require.Equal(t, "abc123", ev.BlockedBy)

	// An allow rule's copy keeps the service: a normal request, whatever its status.
	for _, status := range []int{200, 403, 502} {
		ev, err = ParseLine(accessJSON(t, rk, svc, status))
		require.NoError(t, err)
		require.Equal(t, svcA, ev.ServiceID)
		require.Empty(t, ev.BlockedBy, "status %d from the service is not a firewall block", status)
	}
	// The copy of a redirect router answers 3xx: still not a visit.
	_, err = ParseLine(accessJSON(t, "nc-"+svcA+"-http-fwabc123@file", "noop@internal", 308))
	require.ErrorIs(t, err, ErrIgnored)
	// Maintenance behind an allow copy answers 503 from the pages server: not a block.
	ev, err = ParseLine(accessJSON(t, rk, "nc-pages@file", 503))
	require.NoError(t, err)
	require.Equal(t, svcA, ev.ServiceID)
	require.Empty(t, ev.BlockedBy)
}

func accessJSONWith(t *testing.T, router, service string, status int, extra map[string]any) []byte {
	t.Helper()
	var m map[string]any
	require.NoError(t, json.Unmarshal(accessJSON(t, router, service, status), &m))
	for k, v := range extra {
		m[k] = v
	}
	b, err := json.Marshal(m)
	require.NoError(t, err)
	return b
}

func TestParseLineGuardBlockHeader(t *testing.T) {
	const router, svc = "nc-" + svcA + "-https@file", "nc-" + svcA + "@file"
	hdr := map[string]any{"downstream_X-Nebula-Firewall-Rule": "stdsecrets"}

	ev, err := ParseLine(accessJSONWith(t, router, svc, 403, hdr))
	require.NoError(t, err)
	require.Equal(t, svcA, ev.ServiceID)
	require.Equal(t, "stdsecrets", ev.BlockedBy)

	// Only a refusal is a block: a service that sets the header on a 200 is not.
	ev, err = ParseLine(accessJSONWith(t, router, svc, 200, hdr))
	require.NoError(t, err)
	require.Empty(t, ev.BlockedBy)

	// The id must look like one.
	for _, bad := range []string{"", "ABC", "a b", "0123456789abcdefg", "<script>"} {
		ev, err = ParseLine(accessJSONWith(t, router, svc, 403, map[string]any{"downstream_X-Nebula-Firewall-Rule": bad}))
		require.NoError(t, err)
		require.Empty(t, ev.BlockedBy, bad)
	}
	// No header, no attribution: an ordinary 403 of the service.
	ev, err = ParseLine(accessJSON(t, router, svc, 403))
	require.NoError(t, err)
	require.Empty(t, ev.BlockedBy)
}

func accessJSON(t *testing.T, router, service string, status int) []byte {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"DownstreamStatus": status, "Duration": 1_000_000, "RequestHost": "app.example.com", "RequestMethod": "GET",
		"RequestPath": "/x", "StartUTC": "2026-10-26T10:00:00Z", "RouterName": router, "ServiceName": service,
		"ClientHost": "203.0.113.5", "DownstreamContentSize": 10,
	})
	require.NoError(t, err)
	return b
}

func TestParseLinePerDomainRouters(t *testing.T) {
	svc := "nc-" + svcA + "@rest"

	ev, err := ParseLine(accessJSON(t, "nc-"+svcA+"-daaaaaaaa-https@rest", svc, 200))
	require.NoError(t, err)
	require.Equal(t, svcA, ev.ServiceID, "a per-domain https router is attributed to its service")

	// A redirect router ends at noop@internal: not a visit.
	_, err = ParseLine(accessJSON(t, "nc-"+svcA+"-daaaaaaaa-http@rest", "noop@internal", 308))
	require.ErrorIs(t, err, ErrIgnored)
	_, err = ParseLine(accessJSON(t, "nc-"+svcA+"-daaaaaaaa-redir@rest", "noop@internal", 301))
	require.ErrorIs(t, err, ErrIgnored)
	_, err = ParseLine(accessJSON(t, "nc-"+svcA+"-http@rest", "noop@internal", 308))
	require.ErrorIs(t, err, ErrIgnored)

	// The http router of a domain with forceHttps off or tlsMode none serves the
	// service itself: that is a real visit.
	ev, err = ParseLine(accessJSON(t, "nc-"+svcA+"-daaaaaaaa-http@rest", svc, 200))
	require.NoError(t, err)
	require.Equal(t, svcA, ev.ServiceID)

	// Unrouted traffic still parses with an empty service.
	ev, err = ParseLine(accessJSON(t, "", "", 404))
	require.NoError(t, err)
	require.Empty(t, ev.ServiceID)
}

// A request a compiled rule blocks used to land in the server's unrouted
// bucket (the -fw router matched no suffix); now it is the service's.
func TestCompiledBlockIsCountedForTheServiceNotUnrouted(t *testing.T) {
	a := newAgg()
	for _, line := range [][]byte{
		accessJSON(t, "nc-"+svcA+"-https-fwstdsecrets@file", "nc-pages@file", 403),
		accessJSON(t, "nc-"+svcA+"-daaaaaaaa-https-fwoffice@file", "noop@internal", 403),
		accessJSON(t, "nc-"+svcA+"-https@file", "nc-"+svcA+"@file", 200),
	} {
		ev, err := ParseLine(line)
		require.NoError(t, err)
		a.Add(ev)
	}
	out := a.Flush(time.Date(2026, 10, 26, 10, 5, 0, 0, time.UTC))
	require.Len(t, out, 1, "nothing under the unrouted service id")
	require.Equal(t, svcA, out[0].ServiceID)
	require.EqualValues(t, 3, out[0].Requests)
	require.EqualValues(t, 2, out[0].S4xx)
	require.Equal(t, map[string]int64{"stdsecrets": 1, "office": 1}, out[0].Blocked)
}
