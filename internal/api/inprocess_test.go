package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/auth"
)

func tokenPrincipal() *auth.Principal {
	id := uuid.New()
	return &auth.Principal{Type: auth.TypeToken, TokenID: &id, Label: "ci", Scopes: []string{"projects:read"}}
}

func TestMarkInProcessOnlyChangesMarkedRequests(t *testing.T) {
	p := tokenPrincipal()

	// An ordinary request keeps its principal untouched.
	require.Same(t, p, markInProcess(context.Background(), p))

	// An in-process request gets a copy typed mcp with the client in the label;
	// the original is not modified and scopes are unchanged.
	ctx := WithInProcess(context.Background(), InProcess{Client: "claude-code"})
	got := markInProcess(ctx, p)
	require.NotSame(t, p, got)
	require.Equal(t, auth.TypeMCP, got.Type)
	require.Equal(t, "ci via claude-code", got.Label)
	require.Equal(t, p.Scopes, got.Scopes)
	require.Equal(t, auth.TypeToken, p.Type)
	require.Equal(t, "ci", p.Label)

	got = markInProcess(WithInProcess(context.Background(), InProcess{}), p)
	require.Equal(t, auth.TypeMCP, got.Type)
	require.Equal(t, "ci", got.Label)
}

// A header can never carry the marker: it lives in the request context, which
// net/http builds itself.
func TestNoHeaderOrQuerySetsTheInProcessMarker(t *testing.T) {
	var seen bool
	h := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		_, seen = inProcessFrom(r.Context())
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects?inProcess=1&actor=mcp", nil)
	for _, k := range []string{"X-Nebula-Actor", "X-Nebula-In-Process", "X-Mcp-Actor", "X-Internal"} {
		req.Header.Set(k, "mcp")
	}
	h.ServeHTTP(httptest.NewRecorder(), req)
	require.False(t, seen)
}
