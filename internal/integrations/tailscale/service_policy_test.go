package tailscale_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/integrations/tailscale"
	"github.com/nebulactrl/nebulactrl/internal/mesh/meshtest"
)

// The Tailscale PolicyAPI of a workspace: read with the ETag, a conditional
// write, drift reported as ErrPolicyConflict, a missing scope as ErrAuth.
func TestPolicyAPIOfATailscaleIntegration(t *testing.T) {
	f := meshtest.NewTailscale(t, meshtest.WithScopes(meshtest.ScopePolicyRead))
	e := newPolicyEnv(t, f.URL, f.Client())

	_, err := e.svc.PolicyAPI(bg, e.ws)
	require.ErrorIs(t, err, tailscale.ErrNotConfigured)

	e.putTailscale(e.admin)
	api, err := e.svc.PolicyAPI(bg, e.ws)
	require.NoError(t, err)
	require.Equal(t, "tailscale", api.Provider())

	cur, err := api.Get(bg)
	require.NoError(t, err)
	require.Contains(t, cur.Raw, "the tailnet's own policy")
	require.NotEmpty(t, cur.Version)
	require.NoError(t, api.Check(bg, []byte(tsDoc)))

	_, err = api.Set(bg, []byte(tsDoc), cur.Version)
	require.ErrorIs(t, err, tailscale.ErrAuth, "policy_file:read cannot write")
	require.Empty(t, f.PolicyWrites())

	f.GrantScope(meshtest.ScopePolicy)
	set, err := api.Set(bg, []byte(tsDoc), cur.Version)
	require.NoError(t, err)
	require.NotEqual(t, cur.Version, set.Version)

	_, err = api.Set(bg, []byte("{\"acls\": []}"), cur.Version)
	require.ErrorIs(t, err, tailscale.ErrPolicyConflict, "the version is stale now")
	doc, _ := f.Policy()
	require.Equal(t, tsDoc, doc, "a stale write changed nothing")
	_, err = api.Set(bg, []byte("{\"acls\": []}"), "")
	require.ErrorIs(t, err, tailscale.ErrPolicyNotConditional)
}

// Managed pushes are refused when another team's integration points at the same
// tailnet: one team would rewrite the other's policy.
func TestSharedTailnetDetection(t *testing.T) {
	f := meshtest.NewTailscale(t)
	h := meshtest.NewHeadscale(t)
	e := newPolicyEnv(t, f.URL, f.Client())

	_, err := e.svc.SharedTailnet(bg, e.ws)
	require.ErrorIs(t, err, tailscale.ErrNotConfigured)

	e.putTailscale(e.admin)
	shared, err := e.svc.SharedTailnet(bg, e.ws)
	require.NoError(t, err)
	require.False(t, shared, "alone on the tailnet")

	ws2, admin2 := e.workspace("b@example.com")
	e.putTailscale(admin2)
	shared, err = e.svc.SharedTailnet(bg, e.ws)
	require.NoError(t, err)
	require.True(t, shared, "another team has the same tailnet")
	shared, err = e.svc.SharedTailnet(bg, ws2)
	require.NoError(t, err)
	require.True(t, shared)

	// A Headscale integration is a different tailnet, unless it points at the same login server.
	ws3, admin3 := e.workspace("c@example.com")
	e.putHeadscale(admin3, h, tailscale.HeadscaleAPIKey)
	shared, err = e.svc.SharedTailnet(bg, ws3)
	require.NoError(t, err)
	require.False(t, shared)
	ws4, admin4 := e.workspace("d@example.com")
	e.putHeadscale(admin4, h, tailscale.HeadscaleAPIKey)
	shared, err = e.svc.SharedTailnet(bg, ws3)
	require.NoError(t, err)
	require.True(t, shared, "the same Headscale login server")
	shared, err = e.svc.SharedTailnet(bg, ws4)
	require.NoError(t, err)
	require.True(t, shared)
}

func TestCoordinatorHealth(t *testing.T) {
	f := meshtest.NewTailscale(t)
	h := meshtest.NewHeadscale(t)
	e := newPolicyEnv(t, f.URL, f.Client())
	require.ErrorIs(t, e.svc.CoordinatorHealth(bg, e.ws), tailscale.ErrNotConfigured)

	e.putTailscale(e.admin)
	require.NoError(t, e.svc.CoordinatorHealth(bg, e.ws))
	f.SetDown(true)
	require.Error(t, e.svc.CoordinatorHealth(bg, e.ws))

	wsH, adminH := e.workspace("h@example.com")
	e.putHeadscale(adminH, h, tailscale.HeadscaleAPIKey)
	require.NoError(t, e.svc.CoordinatorHealth(bg, wsH))
	h.SetDown(true)
	require.Error(t, e.svc.CoordinatorHealth(bg, wsH))
	h.SetDown(false)
	require.NoError(t, e.svc.CoordinatorHealth(bg, wsH))
}
