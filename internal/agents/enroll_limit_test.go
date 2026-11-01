package agents

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Behind the reverse proxy every request's TCP peer is the proxy: enrolments
// are limited per visitor address, not all in one bucket.
func TestEnrollLimitIsPerVisitorBehindTheProxy(t *testing.T) {
	h := NewHub(nil, nil, nil, nil, nil)
	t.Cleanup(h.Close)
	enroll := func(visitor string) int {
		r := httptest.NewRequest(http.MethodPost, "/agent/v1/enroll", strings.NewReader("not json"))
		r.RemoteAddr = "10.0.0.2:4000" // the proxy
		r.Header.Set("X-Forwarded-For", visitor)
		w := httptest.NewRecorder()
		h.handleEnroll(w, r)
		return w.Code
	}
	for range 10 {
		require.Equal(t, http.StatusBadRequest, enroll("198.51.100.1"))
	}
	require.Equal(t, http.StatusTooManyRequests, enroll("198.51.100.1"))
	require.Equal(t, http.StatusBadRequest, enroll("198.51.100.2"), "another visitor has its own allowance")
}
