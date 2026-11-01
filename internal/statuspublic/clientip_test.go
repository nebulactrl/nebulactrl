package statuspublic

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/clientip"
)

// A page served through Cloudflare reaches the listener with Cloudflare's edge
// address as the last X-Forwarded-For entry: visitors behind the same edge must
// not share one bucket.
func TestRateLimitKeysOnTheVisitorBehindCloudflare(t *testing.T) {
	edge := netip.MustParsePrefix("173.245.48.0/20")
	clientip.SetCloudflareRanges(func(a netip.Addr) bool { return edge.Contains(a) })
	t.Cleanup(func() { clientip.SetCloudflareRanges(nil) })

	_, _, h := oneHost()
	h.limiter = newLimiter(2)
	hit := func(visitor string) int {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Host = testHost
		r.RemoteAddr = "172.18.0.4:5555" // the control-plane host's proxy
		// What Traefik forwards for a Cloudflare request: the visitor Cloudflare saw, then the edge.
		r.Header.Set("X-Forwarded-For", visitor+", 173.245.48.10")
		r.Header.Set("CF-Connecting-IP", visitor)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}

	require.Equal(t, 200, hit("198.51.100.1"))
	require.Equal(t, 200, hit("198.51.100.1"))
	require.Equal(t, 429, hit("198.51.100.1"), "a visitor over the limit is limited")
	require.Equal(t, 200, hit("198.51.100.2"), "another visitor behind the same edge address is not")
}
