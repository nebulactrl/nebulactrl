package services

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/varexpr"
)

type extRefT = varexpr.ExtRef

func mustExtRef(t *testing.T, s string) varexpr.ExtRef {
	t.Helper()
	r, err := varexpr.ParseExtRef(s)
	require.NoError(t, err)
	return r
}

func refPtr(t *testing.T, s string) *varexpr.ExtRef {
	r := mustExtRef(t, s)
	return &r
}

// Integration scoping and kinds reach the resolver: a reference to an integration
// the project and environment may not use, or to one of the wrong kind, is an
// unresolved reference that names the reference and says why.
func TestResolverRefusesOutOfScopeAndWrongKindIntegrations(t *testing.T) {
	rc := ResolverContext{
		EnvSlug: "production", EnvName: "production",
		Integrations:     map[string]bool{"bao": true, "locked": false, "dp": true},
		IntegrationKinds: map[string]string{"bao": "vault", "locked": "vault", "dp": "doppler"},
	}
	r := NewResolverCtx(rc, []ServiceData{{Slug: "api", Port: 8080, Vars: map[string]VarDef{
		"OK":       {Value: "${{vault:bao/apps/db#password}}"},
		"DOPPLER":  {Value: "${{doppler:dp/backend/prd#DATABASE_URL}}"},
		"DEFAULTS": {Value: "${{doppler:dp#API_KEY}}"},
		"LOCKED":   {Value: "${{vault:locked/apps/db#password}}"},
		"WRONG":    {Value: "${{doppler:bao/backend/prd#K}}"},
		"WRONG2":   {Value: "${{vault:dp/apps/db#password}}"},
		"GONE":     {Value: "${{doppler:nope#K}}"},
	}}})
	res, probs, err := r.ResolveServiceLenient("api")
	require.NoError(t, err)

	// Doppler references defer like Vault ones: the stored value is the reference.
	require.Equal(t, "${{doppler:dp/backend/prd#DATABASE_URL}}", res.Vars["DOPPLER"])
	require.Equal(t, "${{doppler:dp#API_KEY}}", res.Vars["DEFAULTS"])
	require.True(t, res.Secret["DOPPLER"])
	require.Contains(t, res.Ext, "DOPPLER")
	require.Contains(t, res.Ext, "DEFAULTS")
	require.Contains(t, res.Ext, "OK")

	msg := map[string]string{}
	for _, p := range probs {
		require.Equal(t, "unresolved_reference", p.Code, p.Key)
		msg[p.Key] = p.Message
	}
	require.Len(t, msg, 4)
	require.Contains(t, msg["LOCKED"], "${{vault:locked/apps/db#password}}")
	require.Contains(t, msg["LOCKED"], `"locked" is not available to this project and environment`)
	require.Contains(t, msg["WRONG"], "${{doppler:bao/backend/prd#K}}")
	require.Contains(t, msg["WRONG"], `"bao" is a vault integration, which does not take doppler: references`)
	require.Contains(t, msg["WRONG2"], `"dp" is a doppler integration, which does not take vault: references`)
	require.Contains(t, msg["GONE"], "${{doppler:nope#K}}")
	require.Contains(t, msg["GONE"], `no secret manager integration named "nope"`)
}

func TestExtKindMatches(t *testing.T) {
	for kind, vaultOK := range map[string]bool{"vault": true, "infisical": true, "doppler": false} {
		require.Equal(t, vaultOK, ExtKindMatches(mustExtRef(t, "vault:i/p#k"), kind), kind)
		require.Equal(t, kind == "doppler", ExtKindMatches(mustExtRef(t, "doppler:i#k"), kind), kind)
	}
}

// FlushFetcher records what a fetcher kept; a fetcher without a log and nil are fine.
type loggingFetcher struct{ flushed int }

func (*loggingFetcher) Fetch(context.Context, extRefT) (string, error) { return "", nil }
func (l *loggingFetcher) Flush(context.Context)                        { l.flushed++ }

func TestMaterializeFlushesTheFetcherEvenOnFailure(t *testing.T) {
	snap := EnvSnapshot{Vars: map[string]string{}, Ext: map[string]ExtTpl{"A": {{Ref: refPtr(t, "vault:bao/a#k")}}}}
	f := &loggingFetcher{}
	_, _, err := snap.Materialize(context.Background(), &failingFetcher{loggingFetcher: f})
	require.Error(t, err)
	require.Equal(t, 1, f.flushed)

	// Nothing to fetch: the fetcher is never touched, so nothing is flushed.
	plain := EnvSnapshot{Vars: map[string]string{"A": "1"}}
	_, _, err = plain.Materialize(context.Background(), f)
	require.NoError(t, err)
	require.Equal(t, 1, f.flushed)

	FlushFetcher(context.Background(), nil)
	FlushFetcher(context.Background(), &fakeFetcher{})
}

type failingFetcher struct{ *loggingFetcher }

func (*failingFetcher) Fetch(context.Context, extRefT) (string, error) {
	return "", context.DeadlineExceeded
}
