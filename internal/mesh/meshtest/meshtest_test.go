package meshtest_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/integrations/tailscale"
	"github.com/nebulactrl/nebulactrl/internal/mesh/meshtest"
)

func hsClient(h *meshtest.Headscale, user string) *tailscale.HeadscaleClient {
	return tailscale.NewHeadscale(tailscale.HeadscaleConfig{LoginServer: h.URL, APIKey: h.APIKey(), User: user, HTTPClient: h.Client()})
}

func tsClient(f *meshtest.Tailscale) *tailscale.Client {
	return tailscale.New(tailscale.Config{
		BaseURL: f.URL, ClientID: meshtest.TailscaleClientID, ClientSecret: meshtest.TailscaleClientSecret, HTTPClient: f.Client(),
	})
}

// A key is single-use, tagged, and joins exactly one node; nodes list and delete.
func TestHeadscaleKeysAreSingleUseAndNodesJoinWithTheirTags(t *testing.T) {
	h := meshtest.NewHeadscale(t)
	c := hsClient(h, "")
	ctx := context.Background()

	k, err := c.MintPreAuthKey(ctx, []string{"tag:nebula-mesh"}, time.Hour)
	require.NoError(t, err)
	require.Len(t, h.ActiveKeys(), 1)

	n, err := h.JoinWithKey(k.Key, "nc-itest")
	require.NoError(t, err)
	require.Equal(t, []string{"tag:nebula-mesh"}, n.Tags)
	_, err = h.JoinWithKey(k.Key, "second")
	require.ErrorContains(t, err, "already used")
	require.Empty(t, h.ActiveKeys())

	nodes, err := c.Nodes(ctx)
	require.NoError(t, err)
	require.Len(t, nodes, 1)
	require.Equal(t, "nc-itest", nodes[0].Name)
	require.Equal(t, n.IPs[0], nodes[0].IPs[0])
	require.True(t, nodes[0].Online)

	require.NoError(t, c.DeleteNode(ctx, nodes[0].ID))
	require.Empty(t, h.Nodes())
	require.NoError(t, c.DeleteNode(ctx, nodes[0].ID), "a node that is gone is not an error")

	k2, err := c.MintPreAuthKey(ctx, []string{"tag:nebula-mesh"}, time.Hour)
	require.NoError(t, err)
	require.NoError(t, c.ExpirePreAuthKey(ctx, k2.ID))
	_, err = h.JoinWithKey(k2.Key, "late")
	require.ErrorContains(t, err, "expired")
}

// The policy API behaves like Headscale's: not found until the first PUT, a
// check that rejects garbage, and file mode refusing updates.
func TestHeadscalePolicyLifecycle(t *testing.T) {
	h := meshtest.NewHeadscale(t)
	c := hsClient(h, "")
	ctx := context.Background()

	_, err := c.GetPolicy(ctx)
	require.ErrorIs(t, err, tailscale.ErrPolicyNotFound)

	doc := "{\n // comment\n \"tagOwners\": {\"tag:nebula-mesh\": []},\n \"acls\": []\n}"
	require.NoError(t, c.CheckPolicy(ctx, doc))
	require.Error(t, c.CheckPolicy(ctx, "not json"))
	_, err = c.SetPolicy(ctx, "{\"acls\": 3}")
	require.Error(t, err)
	_, ok := h.Policy()
	require.False(t, ok, "a rejected policy must not be stored")

	p, err := c.SetPolicy(ctx, doc)
	require.NoError(t, err)
	require.Equal(t, doc, p.Raw)
	got, err := c.GetPolicy(ctx)
	require.NoError(t, err)
	require.Equal(t, doc, got.Raw)
	require.Equal(t, []string{doc}, h.PolicyHistory())

	file := meshtest.NewHeadscale(t, meshtest.WithPolicyMode(meshtest.PolicyModeFile))
	_, err = hsClient(file, "").SetPolicy(ctx, doc)
	require.True(t, errors.Is(err, tailscale.ErrPolicyFileMode), "got %v", err)
	require.Empty(t, file.PolicyHistory())
}

// The coordinator can be taken down and unauthenticated calls fail.
func TestHeadscaleDownAndAuth(t *testing.T) {
	h := meshtest.NewHeadscale(t)
	ctx := context.Background()
	require.NoError(t, tailscale.HealthCheck(ctx, h.Client(), h.URL))
	bad := tailscale.NewHeadscale(tailscale.HeadscaleConfig{LoginServer: h.URL, APIKey: "nope", HTTPClient: h.Client()})
	_, err := bad.Nodes(ctx)
	require.ErrorIs(t, err, tailscale.ErrAuth)

	h.SetDown(true)
	require.Error(t, tailscale.HealthCheck(ctx, h.Client(), h.URL))
	_, err = hsClient(h, "").Nodes(ctx)
	require.Error(t, err)
	h.SetDown(false)
	_, err = hsClient(h, "").Nodes(ctx)
	require.NoError(t, err)

	h.FailNext("GET", "/api/v1/node", 502)
	_, err = hsClient(h, "").Nodes(ctx)
	require.Error(t, err)
	_, err = hsClient(h, "").Nodes(ctx)
	require.NoError(t, err)
}

// Auth keys carry the requested capabilities; devices join with the key's tags.
func TestTailscaleKeysAndDevices(t *testing.T) {
	f := meshtest.NewTailscale(t)
	c := tsClient(f)
	ctx := context.Background()

	k, err := c.MintAuthKey(ctx, []string{"tag:nebula-mesh"}, 3600, "nebula mesh itest")
	require.NoError(t, err)
	got := f.Keys()
	require.Len(t, got, 1)
	require.False(t, got[0].Reusable)
	require.False(t, got[0].Ephemeral)
	require.True(t, got[0].Preauthorized)
	require.Equal(t, 3600, got[0].ExpirySeconds)

	d, err := f.JoinWithKey(k.Key, "nc-itest")
	require.NoError(t, err)
	require.Equal(t, []string{"tag:nebula-mesh"}, d.Tags)
	_, err = f.JoinWithKey(k.Key, "again")
	require.ErrorContains(t, err, "already used")

	devs, err := c.ListDevices(ctx)
	require.NoError(t, err)
	require.Len(t, devs, 1)
	require.NoError(t, c.DeleteDevice(ctx, devs[0].ID))
	require.Empty(t, f.Devices())

	f.FailKeys(1)
	_, err = c.MintAuthKey(ctx, []string{"tag:nope"}, 60, "")
	require.Error(t, err)
	require.Equal(t, 1, f.TokenExchanges(), "the OAuth token is exchanged once and cached")
}

// The policy endpoints need policy_file[:read]; writes are guarded by the ETag;
// a human edit in the console invalidates it.
func TestTailscalePolicyScopesAndEtag(t *testing.T) {
	f := meshtest.NewTailscale(t)
	c := tsClient(f)
	ctx := context.Background()

	_, err := c.GetPolicy(ctx)
	var ae *tailscale.APIError
	require.True(t, errors.As(err, &ae) && ae.Status == 403, "no policy scope yet: %v", err)

	f.GrantScope(meshtest.ScopePolicy)
	p, err := c.GetPolicy(ctx)
	require.NoError(t, err)
	require.Contains(t, p.Raw, "the tailnet's own policy", "comments survive the round trip")
	require.NotEmpty(t, p.Version)
	require.NoError(t, c.ValidatePolicy(ctx, []byte(p.Raw)))
	require.Error(t, c.ValidatePolicy(ctx, []byte("{nope")))

	next := "{\n // ours\n \"acls\": [],\n}\n"
	stored, err := c.SetPolicy(ctx, []byte(next), p.Version)
	require.NoError(t, err)
	require.NotEqual(t, p.Version, stored.Version)
	require.Equal(t, []string{next}, f.PolicyWrites())

	f.SetPolicyExternally("{\"acls\": []}")
	_, err = c.SetPolicy(ctx, []byte(next), stored.Version)
	require.ErrorIs(t, err, tailscale.ErrPolicyConflict)
}
