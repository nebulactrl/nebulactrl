//go:build integration

package integration

import (
	"encoding/json"
	"net/http"
	"regexp"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

var ipv4RE = regexp.MustCompile(`\b\d{1,3}(?:\.\d{1,3}){3}\b`)

const marker = "nebulactrl-itest-marker-7731"

// 14. traffic analytics: 50 requests through Traefik show up in the service's
// analytics within 90s, and only aggregates (no raw client IP / User-Agent) are stored.
func TestTrafficAnalytics(t *testing.T) {
	requireTier(t, tierFull)
	t.Parallel()
	s := SharedStack(t)
	svc, px := liveV1(t, s, nil)
	px.UA = marker + " (Macintosh; Intel Mac OS X 10_15_7)"

	// Baseline so the assertion measures these requests, not the warm-up ones.
	base := s.analytics(svc.ID).Obj("summary").Int("requests")

	start := time.Now()
	for i := 0; i < 50; i++ {
		code, body, err := px.GetRetry("/")
		require.NoError(t, err)
		require.Equal(t, 200, code)
		require.Equal(t, "v1", body)
	}

	var rep J
	var elapsed time.Duration
	ok := eventually(150*time.Second, 3*time.Second, func() bool {
		rep = s.analytics(svc.ID)
		if rep.Obj("summary").Int("requests")-base >= 50 {
			elapsed = time.Since(start)
			return true
		}
		return false
	})
	require.True(t, ok, "analytics never reported 50 new requests (base %d): %v", base, rep.Obj("summary"))
	t.Logf("50 requests visible in analytics after %s", elapsed.Round(time.Second))
	require.LessOrEqual(t, elapsed, 90*time.Second, "analytics took longer than 90s")

	sum := rep.Obj("summary")
	require.GreaterOrEqual(t, sum.Int("requests"), 50)
	require.Greater(t, sum["p95Ms"], float64(0), "p95 must be > 0: %v", sum)
	var s2xx int
	pts, _ := rep["series"].([]any)
	for _, p := range pts {
		s2xx += J(p.(map[string]any)).Int("s2xx")
	}
	require.GreaterOrEqual(t, s2xx, 50, "2xx count in series: %v", rep["series"])
	var haveHost bool
	for _, h := range itemsOf(rep["topHosts"]) {
		if h.Str("k") == s.Host() {
			haveHost = true
		}
	}
	require.True(t, haveHost, "topHosts: %v", rep["topHosts"])

	// The server-wide view agrees.
	sr := s.Do("GET", "/servers/"+s.ServerID+"/analytics?range=24h", nil)
	require.Equal(t, http.StatusOK, sr.Status, string(sr.Raw))
	require.GreaterOrEqual(t, sr.JSON().Obj("summary").Int("requests"), 50)

	// Privacy: nothing identifying a client is stored in the traffic tables.
	dump := s.dumpTables([]string{"traffic_1m", "traffic_1h", "traffic_1d"})
	require.Contains(t, dump, s.Host(), "traffic rows should be keyed by host")
	require.NotContains(t, dump, marker, "raw User-Agent persisted")
	require.NotContains(t, dump, "Macintosh", "raw User-Agent persisted")
	require.NotContains(t, dump, "Go-http-client", "raw User-Agent persisted")
	require.Empty(t, ipv4RE.FindAllString(dump, -1), "an IPv4 address was persisted in the traffic tables")
	require.NotContains(t, dump, "::1")
	// And not in the API output either.
	for _, raw := range [][]byte{jsonBytes(rep), sr.Raw} {
		require.NotContains(t, string(raw), marker)
		require.Empty(t, ipv4RE.FindAllString(string(raw), -1), "IPv4 in analytics API output")
	}
}

func (s *Stack) analytics(serviceID string) J {
	s.t.Helper()
	r := s.Do("GET", "/services/"+serviceID+"/analytics?range=24h", nil)
	require.Equal(s.t, http.StatusOK, r.Status, string(r.Raw))
	return r.JSON()
}

func itemsOf(v any) []J {
	arr, _ := v.([]any)
	out := make([]J, 0, len(arr))
	for _, e := range arr {
		m, _ := e.(map[string]any)
		out = append(out, J(m))
	}
	return out
}

func jsonBytes(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
