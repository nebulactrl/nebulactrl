package api

import (
	"encoding/json"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/stretchr/testify/require"
)

func TestWithScopes(t *testing.T) {
	op := WithScopes(WithIdempotency(huma.Operation{OperationID: "x"}), "projects:read", "servers:read")
	require.True(t, IsScoped(&op))
	require.True(t, IsIdempotent(&op), "composes with WithIdempotency")
	require.False(t, IsPublic(&op))
	require.Equal(t, []string{"projects:read", "servers:read"}, ScopesOf(&op))
	require.Equal(t, []map[string][]string{
		{SchemeBearer: {"projects:read", "servers:read"}},
		{SchemeCookie: {"projects:read", "servers:read"}},
	}, op.Security)

	got := ScopesOf(&op)
	got[0] = "mutated"
	require.Equal(t, "projects:read", ScopesOf(&op)[0], "ScopesOf returns a copy")
}

func TestWithScopesEmptyMeansAnyAuthenticated(t *testing.T) {
	op := WithScopes(huma.Operation{OperationID: "me"})
	require.True(t, IsScoped(&op))
	require.NotNil(t, ScopesOf(&op))
	require.Empty(t, ScopesOf(&op))

	raw, err := json.Marshal(op.Security)
	require.NoError(t, err)
	require.JSONEq(t, `[{"bearer":[]},{"cookie":[]}]`, string(raw), "empty scopes serialize as [], not null")
}

func TestPublicAndUnwrapped(t *testing.T) {
	op := Public(huma.Operation{OperationID: "setup"})
	require.True(t, IsPublic(&op))
	require.False(t, IsScoped(&op))
	require.Equal(t, []map[string][]string{{}}, op.Security)

	bare := huma.Operation{OperationID: "bare"}
	require.False(t, IsScoped(&bare))
	require.False(t, IsPublic(&bare))
	require.Nil(t, ScopesOf(&bare))
	require.Nil(t, ScopesOf(nil))
}
