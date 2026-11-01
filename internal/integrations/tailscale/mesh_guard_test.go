package tailscale_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/integrations/tailscale"
	"github.com/nebulactrl/nebulactrl/internal/mesh/meshtest"
)

// meshUse puts the workspace's mesh into use in one of the ways the guard counts
// and returns the function that ends it.
type meshUse struct {
	name  string
	start func(t *testing.T, e *policyEnv) (end func())
}

func meshUses() []meshUse {
	return []meshUse{
		{"the mesh is enabled", func(t *testing.T, e *policyEnv) func() {
			require.NoError(t, e.q.EnsureMeshSettings(bg, e.ws))
			_, err := e.q.UpdateMeshSettings(bg, db.UpdateMeshSettingsParams{WorkspaceID: e.ws, Enabled: new(true)})
			require.NoError(t, err)
			return func() {
				_, err := e.q.UpdateMeshSettings(bg, db.UpdateMeshSettingsParams{WorkspaceID: e.ws, Enabled: new(false)})
				require.NoError(t, err)
			}
		}},
		{"a server still has a mesh node", func(t *testing.T, e *policyEnv) func() {
			srv, err := e.q.CreateServer(bg, db.CreateServerParams{WorkspaceID: e.ws, Name: "edge"})
			require.NoError(t, err)
			_, err = e.pool.Exec(bg, `INSERT INTO server_mesh (server_id, workspace_id, hostname, state) VALUES ($1, $2, 'nc-edge', 'removing')`, srv.ID, e.ws)
			require.NoError(t, err)
			return func() {
				_, err := e.pool.Exec(bg, `DELETE FROM server_mesh WHERE server_id = $1`, srv.ID)
				require.NoError(t, err)
			}
		}},
		{"a link node exists", func(t *testing.T, e *policyEnv) func() {
			a, err := e.q.CreateServer(bg, db.CreateServerParams{WorkspaceID: e.ws, Name: "a"})
			require.NoError(t, err)
			b, err := e.q.CreateServer(bg, db.CreateServerParams{WorkspaceID: e.ws, Name: "b"})
			require.NoError(t, err)
			prj, err := e.q.CreateProject(bg, db.CreateProjectParams{WorkspaceID: e.ws, Name: "Shop", Slug: "shop"})
			require.NoError(t, err)
			env, err := e.q.CreateEnvironment(bg, db.CreateEnvironmentParams{ProjectID: prj.ID, Name: "production", Slug: "production"})
			require.NoError(t, err)
			_, err = e.pool.Exec(bg, `INSERT INTO mesh_links (workspace_id, environment_id, server_id, remote_server_id, purpose, hostname) VALUES ($1, $2, $3, $4, 'service', 'nc-link')`, e.ws, env.ID, a.ID, b.ID)
			require.NoError(t, err)
			return func() {
				_, err := e.pool.Exec(bg, `DELETE FROM mesh_links WHERE workspace_id = $1`, e.ws)
				require.NoError(t, err)
			}
		}},
	}
}

func requireInUse(t *testing.T, err error, verb string) {
	t.Helper()
	ae, ok := apierr.As(err)
	require.True(t, ok, "not an API error: %v", err)
	require.Equal(t, apierr.CodeIntegrationInUse, ae.Code)
	require.Equal(t, 409, ae.Status)
	require.Contains(t, ae.Error(), "the server mesh uses this integration")
	require.True(t, strings.HasSuffix(ae.Error(), "before it is "+verb), "message: %s", ae.Error())
}

// Delete and a Put that changes which tailnet the integration points at are
// refused with 409 integration_in_use while the mesh is built on it, however it
// is in use; they work again once the mesh is gone.
func TestMeshInUseBlocksDeleteAndTailnetChange(t *testing.T) {
	h := meshtest.NewHeadscale(t)
	other := meshtest.NewHeadscale(t)
	for _, use := range meshUses() {
		t.Run(use.name, func(t *testing.T) {
			e := newPolicyEnv(t, "", h.Client())
			e.putHeadscale(e.admin, h, tailscale.HeadscaleAPIKey)
			end := use.start(t, e)

			requireInUse(t, e.svc.Delete(e.admin), "removed")
			_, err := e.svc.Get(e.admin)
			require.NoError(t, err)

			_, err = e.svc.Put(e.admin, tailscale.PutInput{
				Provider: tailscale.ProviderHeadscale, LoginServer: other.URL, ClientSecret: other.APIKey(), HeadscaleCredential: tailscale.HeadscaleAPIKey,
				DefaultTags: []string{"tag:nebula-svc"}, AllowedTags: []string{"tag:nebula-svc", "tag:nebula-mesh"},
			})
			requireInUse(t, err, "changed")
			v, err := e.svc.Get(e.admin)
			require.NoError(t, err)
			require.Equal(t, h.URL, v.LoginServer, "the integration was not changed")

			end()
			_, err = e.svc.Put(e.admin, tailscale.PutInput{
				Provider: tailscale.ProviderHeadscale, LoginServer: other.URL, ClientSecret: other.APIKey(), HeadscaleCredential: tailscale.HeadscaleAPIKey,
				DefaultTags: []string{"tag:nebula-svc"}, AllowedTags: []string{"tag:nebula-svc", "tag:nebula-mesh"},
			})
			require.NoError(t, err)
			require.NoError(t, e.svc.Delete(e.admin))
		})
	}
}

// An edit that keeps the tailnet and the mesh tag allowed is not a change of
// the tailnet: it goes through while the mesh is in use. Dropping the mesh tag
// from the allowed tags would break key minting, so it is refused.
func TestMeshInUseAllowsHarmlessEditsButNotDroppingTheMeshTag(t *testing.T) {
	h := meshtest.NewHeadscale(t)
	e := newPolicyEnv(t, "", h.Client())
	e.putHeadscale(e.admin, h, tailscale.HeadscaleAPIKey)
	use := meshUses()[0]
	end := use.start(t, e)

	// The secret is omitted (kept); only the tags change, and the mesh tag stays allowed.
	_, err := e.svc.Put(e.admin, tailscale.PutInput{
		Provider: tailscale.ProviderHeadscale, LoginServer: h.URL, HeadscaleCredential: tailscale.HeadscaleAPIKey,
		DefaultTags: []string{"tag:nebula-svc"}, AllowedTags: []string{"tag:nebula-svc", "tag:nebula-mesh", "tag:extra"},
	})
	require.NoError(t, err)

	_, err = e.svc.Put(e.admin, tailscale.PutInput{
		Provider: tailscale.ProviderHeadscale, LoginServer: h.URL, HeadscaleCredential: tailscale.HeadscaleAPIKey,
		DefaultTags: []string{"tag:nebula-svc"}, AllowedTags: []string{"tag:nebula-svc"},
	})
	requireInUse(t, err, "changed")
	v, err := e.svc.Get(e.admin)
	require.NoError(t, err)
	require.Contains(t, v.AllowedTags, "tag:nebula-mesh", "the refused edit changed nothing")

	end()
	_, err = e.svc.Put(e.admin, tailscale.PutInput{
		Provider: tailscale.ProviderHeadscale, LoginServer: h.URL, HeadscaleCredential: tailscale.HeadscaleAPIKey,
		DefaultTags: []string{"tag:nebula-svc"}, AllowedTags: []string{"tag:nebula-svc"},
	})
	require.NoError(t, err, "with the mesh off the mesh tag may be dropped")
}

// The guard counts the workspace's own mesh only.
func TestMeshInUseOfAnotherTeamDoesNotBlock(t *testing.T) {
	h := meshtest.NewHeadscale(t)
	e := newPolicyEnv(t, "", h.Client())
	e.putHeadscale(e.admin, h, tailscale.HeadscaleAPIKey)
	ws2, admin2 := e.workspace("b@example.com")
	other := meshtest.NewHeadscale(t)
	e.putHeadscale(admin2, other, tailscale.HeadscaleAPIKey)
	require.NoError(t, e.q.EnsureMeshSettings(bg, ws2))
	_, err := e.q.UpdateMeshSettings(bg, db.UpdateMeshSettingsParams{WorkspaceID: ws2, Enabled: new(true)})
	require.NoError(t, err)

	require.NoError(t, e.svc.Delete(e.admin), "the first team's mesh is off")
	requireInUse(t, e.svc.Delete(admin2), "removed")
}
