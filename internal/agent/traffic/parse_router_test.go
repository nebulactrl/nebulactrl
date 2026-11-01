package traffic

import (
	"encoding/json"
	"testing"

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
		id, redirect, ok := serviceFromRouter(name)
		require.Equal(t, w.ok, ok, name)
		if w.ok {
			require.Equal(t, svcA, id, name)
			require.Equal(t, w.redirect, redirect, name)
		}
	}
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
