package mesh_test

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agents"
	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/events"
	"github.com/nebulactrl/nebulactrl/internal/integrations/tailscale"
	"github.com/nebulactrl/nebulactrl/internal/jobs"
	"github.com/nebulactrl/nebulactrl/internal/mesh"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// principalOf is a token of the fixture's team holding only the scopes.
func (f *fixture) principalOf(scopes ...string) context.Context {
	p, _ := auth.PrincipalFrom(f.admin)
	return auth.WithPrincipal(bg, &auth.Principal{Type: auth.TypeToken, TokenID: new(uuid.New()), UserID: p.UserID, WorkspaceID: p.WorkspaceID, Label: "tok", Scopes: scopes})
}

func (f *fixture) setFacts(srv db.Server, facts agentprotocol.Facts) {
	f.t.Helper()
	raw, err := json.Marshal(facts)
	require.NoError(f.t, err)
	_, err = f.pool.Exec(bg, "UPDATE servers SET facts = $2 WHERE id = $1", srv.ID, raw)
	require.NoError(f.t, err)
}

// setSettings updates the team's mesh settings row (creating it first).
func (f *fixture) setSettings(set string, args ...any) {
	f.t.Helper()
	require.NoError(f.t, f.q.EnsureMeshSettings(bg, f.ws))
	_, err := f.pool.Exec(bg, "UPDATE mesh_settings SET "+set+" WHERE workspace_id = $1", append([]any{f.ws}, args...)...)
	require.NoError(f.t, err)
}

func (f *fixture) count(query string, args ...any) int {
	f.t.Helper()
	var n int
	require.NoError(f.t, f.pool.QueryRow(bg, query, args...).Scan(&n))
	return n
}

func (f *fixture) audits(action string) int {
	return f.count("SELECT count(*) FROM audit_events WHERE action = $1", action)
}

// syncsOf returns the sync_mesh payloads sent to one server, oldest first.
func (f *fixture) syncsOf(srv db.Server) []agentprotocol.SyncMeshPayload {
	f.t.Helper()
	var out []agentprotocol.SyncMeshPayload
	for _, c := range f.syncMeshCommands() {
		if c.ServerID != srv.ID {
			continue
		}
		var p agentprotocol.SyncMeshPayload
		require.NoError(f.t, json.Unmarshal(c.Payload, &p))
		out = append(out, p)
	}
	return out
}

func blockerCodes(plan mesh.ServerMeshPlan) []string {
	out := []string{}
	for _, b := range plan.Blockers {
		out = append(out, b.Code)
	}
	return out
}

func hasWarning(plan mesh.ServerMeshPlan, sub string) bool {
	return slices.ContainsFunc(plan.Warnings, func(w string) bool { return strings.Contains(w, sub) })
}

func TestServerMeshPlanNamesWhatEnablingCostsAndWhatBlocksIt(t *testing.T) {
	f := newFixture(t)
	dry := func() mesh.ServerMeshPlan {
		t.Helper()
		res, err := f.svc.SetServerMesh(f.admin, f.a.ID, mesh.ServerMeshInput{Enabled: true, DryRun: true})
		require.NoError(t, err)
		require.False(t, res.Applied, "a dry run applies nothing")
		require.False(t, res.Enabled)
		return res.Plan
	}

	plan := dry()
	require.Equal(t, 1, plan.DevicesAdded)
	require.True(t, plan.TunDevice)
	require.Empty(t, plan.Blockers)
	require.True(t, hasWarning(plan, "ACLs are advisory"), "%v", plan.Warnings)
	require.True(t, hasWarning(plan, "Adds 1 device; this team uses 0 tagged mesh devices."), "%v", plan.Warnings)

	// A host without /dev/net/tun can still join, with a warning about its own egress.
	f.setFacts(f.a, agentprotocol.Facts{OS: "linux", Capabilities: []string{agentprotocol.CapMesh}})
	plan = dry()
	require.False(t, plan.TunDevice)
	require.Empty(t, plan.Blockers)
	require.True(t, hasWarning(plan, "/dev/net/tun"), "%v", plan.Warnings)

	// An agent without the mesh capability blocks.
	f.setFacts(f.a, agentprotocol.Facts{OS: "linux", TunDevice: true})
	require.Equal(t, []string{apierr.CodeMeshAgentTooOld}, blockerCodes(dry()))
	f.setFacts(f.a, agentprotocol.Facts{OS: "linux", Capabilities: []string{agentprotocol.CapMesh}, TunDevice: true})

	f.setSettings("paused = true, paused_reason = 'redeploying headscale'")
	plan = dry()
	require.Equal(t, []string{apierr.CodeMeshPaused}, blockerCodes(plan))
	require.Contains(t, plan.Blockers[0].Message, "redeploying headscale")
	f.setSettings("paused = false, coordinator_ok = false")
	require.Equal(t, []string{apierr.CodeMeshCoordinatorDown}, blockerCodes(dry()))
	f.setSettings("coordinator_ok = true")

	f.setSettings("mesh_tag = 'tag:other'")
	plan = dry()
	require.Equal(t, []string{"mesh_tag_not_allowed"}, blockerCodes(plan))
	require.Contains(t, plan.Blockers[0].Message, "tag:other")
	f.setSettings("mesh_tag = $2", mesh.DefaultMeshTag)

	f.setSettings("acl_mode = 'off'")
	require.True(t, hasWarning(dry(), "ACL management is off"))
	f.setSettings("acl_mode = 'managed'")
	require.False(t, hasWarning(dry(), "advisory"))
	require.False(t, hasWarning(dry(), "ACL management is off"))
	f.setSettings("acl_mode = 'advisory'")

	// A pre-auth key cannot delete devices: said before anything is added.
	_, err := f.ts.Put(f.admin, tailscale.PutInput{
		Provider: tailscale.ProviderHeadscale, LoginServer: f.hs.URL, ClientSecret: "hskey-preauth-0000", HeadscaleCredential: tailscale.HeadscalePreauth,
		DefaultTags: []string{"tag:nebula-svc"}, AllowedTags: []string{"tag:nebula-svc", mesh.DefaultMeshTag},
	})
	require.NoError(t, err)
	require.True(t, hasWarning(dry(), "pre-auth key"))

	// Without any tailnet integration nothing can be added.
	require.NoError(t, f.ts.Delete(f.admin))
	require.Equal(t, []string{apierr.CodeMeshNotConfigured}, blockerCodes(dry()))

	// A server that already has its node adds nothing.
	f2 := newFixture(t)
	f2.enable(f2.a)
	res, err := f2.svc.SetServerMesh(f2.admin, f2.a.ID, mesh.ServerMeshInput{Enabled: true, DryRun: true})
	require.NoError(t, err)
	require.True(t, res.Enabled)
	require.Zero(t, res.Plan.DevicesAdded)
	require.Empty(t, res.Plan.Warnings)
}

func TestEnablingAServerIsRefusedWhileABlockerStandsAndStartsNothing(t *testing.T) {
	f := newFixture(t)
	f.setFacts(f.a, agentprotocol.Facts{OS: "linux", TunDevice: true})
	_, err := f.svc.SetServerMesh(f.admin, f.a.ID, mesh.ServerMeshInput{Enabled: true, AcceptMeshChanges: true})
	ae, ok := apierr.As(err)
	require.True(t, ok)
	require.Equal(t, http.StatusUnprocessableEntity, ae.Status)
	require.Equal(t, apierr.CodeMeshAgentTooOld, ae.Code)
	require.Zero(t, f.count("SELECT count(*) FROM server_mesh"))
	require.Empty(t, f.syncMeshCommands())
	require.Zero(t, f.audits("mesh.node.provision"))
	require.Zero(t, f.count("SELECT count(*) FROM mesh_settings WHERE enabled"), "the team's mesh stays off")

	// Scope and tenancy: servers:write on the caller's own servers only.
	_, err = f.svc.SetServerMesh(f.principalOf(policy.ScopeServersRead), f.b.ID, mesh.ServerMeshInput{Enabled: true, AcceptMeshChanges: true})
	requireAPICode(t, err, "forbidden")
	other, err := f.q.CreateWorkspace(bg, "other")
	require.NoError(t, err)
	stranger, err := f.q.CreateServer(bg, db.CreateServerParams{WorkspaceID: other.ID, Name: "elsewhere"})
	require.NoError(t, err)
	_, err = f.svc.SetServerMesh(f.admin, stranger.ID, mesh.ServerMeshInput{Enabled: true, AcceptMeshChanges: true})
	requireAPICode(t, err, "not_found")
	_, err = f.svc.SetServerMesh(f.admin, uuid.New(), mesh.ServerMeshInput{})
	requireAPICode(t, err, "not_found")
}

func TestEnablingTheFirstServerTurnsTheTeamsMeshOnOnceAndEnablingAgainChangesNothing(t *testing.T) {
	f := newFixture(t)
	res, err := f.svc.SetServerMesh(f.admin, f.a.ID, mesh.ServerMeshInput{Enabled: true, AcceptMeshChanges: true})
	require.NoError(t, err)
	require.True(t, res.Applied)
	require.True(t, res.Enabled)
	require.Equal(t, 1, f.audits("mesh.enable"))
	require.Equal(t, 1, f.audits("mesh.node.provision"))
	row, err := f.q.GetServerMesh(bg, f.a.ID)
	require.NoError(t, err)
	require.Equal(t, "nc-agent1", row.Hostname)
	require.Equal(t, "provisioning", row.State)
	require.True(t, row.NeedsReprovision)

	// The same request again is answered as applied and sends no second command.
	res, err = f.svc.SetServerMesh(f.admin, f.a.ID, mesh.ServerMeshInput{Enabled: true})
	require.NoError(t, err)
	require.True(t, res.Applied)
	require.True(t, res.Enabled)
	require.Len(t, f.syncsOf(f.a), 1)

	// A second server does not turn the team's mesh on again, and its sync lists only its own node.
	f.enable(f.b)
	require.Equal(t, 1, f.audits("mesh.enable"))
	syncs := f.syncsOf(f.b)
	require.Len(t, syncs, 1)
	require.Len(t, syncs[0].Nodes, 1)
	require.Equal(t, f.b.ID.String(), syncs[0].Nodes[0].NodeID)
	require.Equal(t, "nc-agent2", syncs[0].Nodes[0].Hostname)
}

func TestDisablingAServerRemovesItsNodeDeviceAndKeyAndEmptiesItsSet(t *testing.T) {
	f := newFixture(t)

	// A server without a node is already off.
	res, err := f.svc.SetServerMesh(f.admin, f.a.ID, mesh.ServerMeshInput{})
	require.NoError(t, err)
	require.True(t, res.Applied)
	require.False(t, res.Enabled)
	res, err = f.svc.SetServerMesh(f.admin, f.a.ID, mesh.ServerMeshInput{DryRun: true})
	require.NoError(t, err)
	require.False(t, res.Applied, "a dry run applies nothing")

	f.enable(f.a)
	cmds := f.syncMeshCommands()
	_, err = f.svc.SecretsForCommand(bg, cmds[0].Kind, cmds[0].Payload)
	require.NoError(t, err)
	require.Len(t, f.hs.ActiveKeys(), 1)
	joined := f.hs.JoinNode("nc-agent1", mesh.DefaultMeshTag)
	_, err = f.pool.Exec(bg, "UPDATE server_mesh SET device_id = $1, ips = $2, state = 'online'", joined.ID, joined.IPs)
	require.NoError(t, err)

	res, err = f.svc.SetServerMesh(f.admin, f.a.ID, mesh.ServerMeshInput{DryRun: true})
	require.NoError(t, err)
	require.True(t, res.Enabled)
	require.False(t, res.Applied)
	require.Empty(t, res.Plan.Blockers)
	_, err = f.q.GetServerMesh(bg, f.a.ID)
	require.NoError(t, err, "a dry run keeps the node")

	res, err = f.svc.SetServerMesh(f.admin, f.a.ID, mesh.ServerMeshInput{})
	require.NoError(t, err)
	require.True(t, res.Applied)
	require.False(t, res.Enabled)
	_, err = f.q.GetServerMesh(bg, f.a.ID)
	require.ErrorIs(t, err, pgx.ErrNoRows)
	syncs := f.syncsOf(f.a)
	require.Len(t, syncs, 2)
	require.Empty(t, syncs[1].Nodes, "the complete empty set removes every mesh container of the server")
	require.Equal(t, 1, f.count("SELECT count(*) FROM river_job WHERE kind = 'tailnet_delete_device'"))
	require.Equal(t, 1, f.count("SELECT count(*) FROM river_job WHERE kind = 'mesh_acl_apply'"))
	require.Equal(t, 1, f.audits("mesh.node.remove"))
	require.Empty(t, f.hs.ActiveKeys(), "the node's key is revoked with it")
}

func TestDisablingIsRefusedWhileLinksOrExposuresNeedTheNode(t *testing.T) {
	f := newFixture(t)
	pg1, _ := f.twoServices()
	ref := uuid.New()
	require.NoError(t, db.InTx(bg, f.pool, func(tx pgx.Tx) error {
		exp, err := f.svc.EnsureExposure(bg, tx, pg1, agentprotocol.MeshPurposeService, 5432)
		if err != nil {
			return err
		}
		link, err := f.svc.EnsureLink(bg, tx, f.env1.ID, f.a.ID, f.b.ID, agentprotocol.MeshPurposeService)
		if err != nil {
			return err
		}
		return f.svc.Grant(bg, tx, link.ID, exp.ID, ref, "reference")
	}))

	// The provider side holds the exposure, the consumer side runs the link node: both are in use.
	for _, srv := range []db.Server{f.a, f.b} {
		_, err := f.svc.SetServerMesh(f.admin, srv.ID, mesh.ServerMeshInput{})
		ae, ok := apierr.As(err)
		require.True(t, ok, srv.Name)
		require.Equal(t, http.StatusConflict, ae.Status, srv.Name)
		require.Equal(t, "mesh_server_in_use", ae.Code, srv.Name)
		_, err = f.q.GetServerMesh(bg, srv.ID)
		require.NoError(t, err, "a refused disable keeps the node")
	}
	_, err := f.svc.SetServerMesh(f.admin, f.b.ID, mesh.ServerMeshInput{})
	require.Contains(t, err.Error(), "1 link(s) and 1 exposed service(s)")
	_, err = f.svc.SetServerMesh(f.admin, f.a.ID, mesh.ServerMeshInput{})
	require.Contains(t, err.Error(), "1 link(s) and 0 exposed service(s)")

	// The dry run reports the same problem as a blocker instead of failing.
	res, err := f.svc.SetServerMesh(f.admin, f.b.ID, mesh.ServerMeshInput{DryRun: true})
	require.NoError(t, err)
	require.False(t, res.Applied)
	require.Equal(t, []string{"mesh_server_in_use"}, blockerCodes(res.Plan))

	// Once the last grant is released the node can go.
	require.NoError(t, db.InTx(bg, f.pool, func(tx pgx.Tx) error { return f.svc.Release(bg, tx, ref) }))
	res, err = f.svc.SetServerMesh(f.admin, f.b.ID, mesh.ServerMeshInput{})
	require.NoError(t, err)
	require.True(t, res.Applied)
}

func TestReprovisionServerSendsAFreshProvisionSyncAndRefusesWhatCannotBeRedone(t *testing.T) {
	f := newFixture(t)
	f.enable(f.b)

	err := f.svc.ReprovisionServer(f.admin, f.a.ID)
	requireAPICode(t, err, "not_applicable")
	requireAPICode(t, f.svc.ReprovisionServer(f.principalOf(policy.ScopeServersRead), f.b.ID), "forbidden")
	requireAPICode(t, f.svc.ReprovisionServer(f.admin, uuid.New()), "not_found")

	f.enable(f.a)
	// The node joined: the agent applied the first spec, so nothing asks for provisioning any more.
	_, err = f.pool.Exec(bg, "UPDATE server_mesh SET state = 'online', needs_reprovision = false, applied = '{\"x\":1}' WHERE server_id = $1", f.a.ID)
	require.NoError(t, err)
	before := len(f.syncsOf(f.a))

	f.setSettings("paused = true, paused_reason = 'maintenance'")
	err = f.svc.ReprovisionServer(f.admin, f.a.ID)
	requireAPICode(t, err, apierr.CodeMeshPaused)
	require.Len(t, f.syncsOf(f.a), before)
	f.setSettings("paused = false")

	f.setFacts(f.a, agentprotocol.Facts{OS: "linux", TunDevice: true})
	requireAPICode(t, f.svc.ReprovisionServer(f.admin, f.a.ID), apierr.CodeMeshAgentTooOld)
	f.setFacts(f.a, agentprotocol.Facts{OS: "linux", Capabilities: []string{agentprotocol.CapMesh}, TunDevice: true})

	require.NoError(t, f.svc.ReprovisionServer(f.admin, f.a.ID))
	row, err := f.q.GetServerMesh(bg, f.a.ID)
	require.NoError(t, err)
	require.Equal(t, "provisioning", row.State)
	require.True(t, row.NeedsReprovision)
	syncs := f.syncsOf(f.a)
	require.Len(t, syncs, before+1)
	last := syncs[len(syncs)-1]
	require.Len(t, last.Nodes, 1)
	require.True(t, last.Nodes[0].Provision, "the new sync asks for a fresh key")
	var meta string
	require.NoError(t, f.pool.QueryRow(bg, "SELECT metadata::text FROM audit_events WHERE action = 'mesh.node.provision' AND resource_id = $1 ORDER BY created_at DESC LIMIT 1", f.a.ID.String()).Scan(&meta))
	require.Contains(t, meta, `"reprovision": true`)

	// A revoked server has no agent left to do it.
	_, err = f.pool.Exec(bg, "UPDATE servers SET status = 'revoked' WHERE id = $1", f.a.ID)
	require.NoError(t, err)
	requireAPICode(t, f.svc.ReprovisionServer(f.admin, f.a.ID), "server_revoked")
}

// linkFixture is two enabled servers and a service link a -> b that serves a reference to pg1.
type linkFixture struct {
	*fixture
	pg1  uuid.UUID
	link mesh.Link
	exp  mesh.Exposure
	ref  uuid.UUID
}

func newLinkFixture(t *testing.T) *linkFixture {
	t.Helper()
	f := newFixture(t)
	pg1, _ := f.twoServices()
	lf := &linkFixture{fixture: f, pg1: pg1, ref: uuid.New()}
	require.NoError(t, db.InTx(bg, f.pool, func(tx pgx.Tx) error {
		var err error
		if lf.exp, err = f.svc.EnsureExposure(bg, tx, pg1, agentprotocol.MeshPurposeService, 5432); err != nil {
			return err
		}
		if lf.link, err = f.svc.EnsureLink(bg, tx, f.env1.ID, f.a.ID, f.b.ID, agentprotocol.MeshPurposeService); err != nil {
			return err
		}
		return f.svc.Grant(bg, tx, lf.link.ID, lf.exp.ID, lf.ref, "reference")
	}))
	return lf
}

func TestReprovisionLinkRecreatesTheLinkNodeAndNeedsTheServersScope(t *testing.T) {
	f := newLinkFixture(t)
	// The b node already has an address, so the link node is desired on a.
	_, err := f.pool.Exec(bg, "UPDATE server_mesh SET ips = '{100.64.0.2}', state = 'online' WHERE server_id = $1", f.b.ID)
	require.NoError(t, err)
	_, err = f.pool.Exec(bg, "UPDATE mesh_links SET state = 'online', needs_reprovision = false, applied = '{\"x\":1}' WHERE id = $1", f.link.ID)
	require.NoError(t, err)
	before := len(f.syncsOf(f.a))

	requireAPICode(t, f.svc.ReprovisionLink(f.principalOf(policy.ScopeServersRead), f.link.ID), "forbidden")
	requireAPICode(t, f.svc.ReprovisionLink(f.admin, uuid.New()), "not_found")

	f.setSettings("paused = true, paused_reason = 'maintenance'")
	requireAPICode(t, f.svc.ReprovisionLink(f.admin, f.link.ID), apierr.CodeMeshPaused)
	f.setSettings("paused = false")

	require.NoError(t, f.svc.ReprovisionLink(f.admin, f.link.ID))
	l, err := f.q.GetMeshLinkByID(bg, f.link.ID)
	require.NoError(t, err)
	require.Equal(t, "provisioning", l.State)
	require.True(t, l.NeedsReprovision)
	syncs := f.syncsOf(f.a)
	require.Len(t, syncs, before+1, "the consumer's server gets the new set")
	var linkNode *agentprotocol.MeshNodeSpec
	for i := range syncs[len(syncs)-1].Nodes {
		if n := &syncs[len(syncs)-1].Nodes[i]; n.Kind == agentprotocol.MeshKindLink {
			linkNode = n
		}
	}
	require.NotNil(t, linkNode, "the link node is in the set")
	require.True(t, linkNode.Provision)
	require.Equal(t, f.link.ID.String(), linkNode.NodeID)
	require.Equal(t, "100.64.0.2", linkNode.TargetIP)
	require.Equal(t, []string{"pg1"}, linkNode.Aliases)
	require.Equal(t, 3, f.audits("mesh.node.provision"), "one audit row per enabled server plus the link's own")
}

func TestUpdateSettingsNeedsTheRightScopeAndRefusesChangesThatOrphanNodes(t *testing.T) {
	f := newFixture(t)
	writer := f.principalOf(policy.ScopeServersRead, policy.ScopeServersWrite)

	// Turning the mesh on and off is servers:write; the ACL mode and the tag are admin.
	v, err := f.svc.UpdateSettings(writer, mesh.SettingsInput{Enabled: new(true), AutoJoin: new(true)})
	require.NoError(t, err)
	require.True(t, v.Enabled)
	require.True(t, v.AutoJoin)
	require.Equal(t, 1, f.audits("mesh.enable"))
	requireAPICode(t, errOf(f.svc.UpdateSettings(writer, mesh.SettingsInput{ACLMode: new(mesh.ModeOff)})), "forbidden")
	requireAPICode(t, errOf(f.svc.UpdateSettings(writer, mesh.SettingsInput{MeshTag: new("tag:x")})), "forbidden")
	requireAPICode(t, errOf(f.svc.UpdateSettings(f.principalOf(policy.ScopeServersRead), mesh.SettingsInput{Enabled: new(false)})), "forbidden")

	v, err = f.svc.UpdateSettings(writer, mesh.SettingsInput{Enabled: new(false)})
	require.NoError(t, err)
	require.False(t, v.Enabled)
	require.Equal(t, 1, f.audits("mesh.disable"))
	v, err = f.svc.UpdateSettings(writer, mesh.SettingsInput{AutoJoin: new(false)})
	require.NoError(t, err)
	require.Equal(t, 1, f.audits("mesh.settings.update"), "a change that flips nothing is a plain settings update")

	// ACL mode: only the three known modes; off is accepted and regenerates the ACL.
	requireAPICode(t, errOf(f.svc.UpdateSettings(f.admin, mesh.SettingsInput{ACLMode: new("sometimes")})), "invalid_acl_mode")
	v, err = f.svc.UpdateSettings(f.admin, mesh.SettingsInput{ACLMode: new(mesh.ModeOff)})
	require.NoError(t, err)
	require.Equal(t, mesh.ModeOff, v.ACLMode)
	require.Equal(t, 1, f.count("SELECT count(*) FROM river_job WHERE kind = 'mesh_acl_apply'"))

	// The mesh tag: a valid tag name that the integration allows; empty restores the default.
	requireAPICode(t, errOf(f.svc.UpdateSettings(f.admin, mesh.SettingsInput{MeshTag: new("nebula")})), "invalid_tag")
	requireAPICode(t, errOf(f.svc.UpdateSettings(f.admin, mesh.SettingsInput{MeshTag: new("tag:not allowed")})), "invalid_tag")
	requireAPICode(t, errOf(f.svc.UpdateSettings(f.admin, mesh.SettingsInput{MeshTag: new("tag:unlisted")})), "mesh_tag_not_allowed")
	v, err = f.svc.UpdateSettings(f.admin, mesh.SettingsInput{MeshTag: new("tag:nebula-svc")})
	require.NoError(t, err)
	require.Equal(t, "tag:nebula-svc", v.MeshTag)
	v, err = f.svc.UpdateSettings(f.admin, mesh.SettingsInput{MeshTag: new("")})
	require.NoError(t, err)
	require.Equal(t, mesh.DefaultMeshTag, v.MeshTag)

	// With a node, neither switching off nor re-tagging is possible.
	f.enable(f.a)
	requireAPICode(t, errOf(f.svc.UpdateSettings(f.admin, mesh.SettingsInput{Enabled: new(false)})), "mesh_in_use")
	requireAPICode(t, errOf(f.svc.UpdateSettings(f.admin, mesh.SettingsInput{MeshTag: new("tag:nebula-svc")})), "mesh_in_use")
	v, err = f.svc.View(f.admin)
	require.NoError(t, err)
	require.True(t, v.Enabled)
	require.Equal(t, mesh.DefaultMeshTag, v.MeshTag)
}

func errOf[T any](_ T, err error) error { return err }

func TestManagedAclModeNeedsAnIntegrationThatCanWriteThePolicy(t *testing.T) {
	f := newFixture(t)
	require.NoError(t, f.ts.Delete(f.admin))
	requireAPICode(t, errOf(f.svc.UpdateSettings(f.admin, mesh.SettingsInput{ACLMode: new(mesh.ModeManaged)})), apierr.CodeMeshNotConfigured)

	// A Headscale pre-auth key cannot write the policy.
	_, err := f.ts.Put(f.admin, tailscale.PutInput{
		Provider: tailscale.ProviderHeadscale, LoginServer: f.hs.URL, ClientSecret: "hskey-preauth-0000", HeadscaleCredential: tailscale.HeadscalePreauth,
		DefaultTags: []string{"tag:nebula-svc"}, AllowedTags: []string{"tag:nebula-svc", mesh.DefaultMeshTag},
	})
	require.NoError(t, err)
	requireAPICode(t, errOf(f.svc.UpdateSettings(f.admin, mesh.SettingsInput{ACLMode: new(mesh.ModeManaged)})), apierr.CodeMeshACLModeUnsupported)
	v, err := f.svc.View(f.admin)
	require.NoError(t, err)
	require.Equal(t, mesh.ModeAdvisory, v.ACLMode, "a refused switch stores nothing")
}

// answerDisp is a dispatcher whose agent answers a mesh_check at once.
type answerDisp struct {
	realDisp
	status  string
	outcome string
	errMsg  string
	cmdErr  string
	result  *agentprotocol.MeshCheckResult
	rawData json.RawMessage
	asked   []agentprotocol.MeshCheckPayload
}

func (d *answerDisp) Enqueue(ctx context.Context, tx pgx.Tx, c agents.NewCommand) (uuid.UUID, error) {
	id, err := d.realDisp.Enqueue(ctx, tx, c)
	if err != nil || c.Kind != agentprotocol.KindMeshCheck {
		return id, err
	}
	var p agentprotocol.MeshCheckPayload
	if err := json.Unmarshal(c.Payload, &p); err != nil {
		return id, err
	}
	d.asked = append(d.asked, p)
	if d.status == "" {
		return id, nil // nobody answers
	}
	fr := agentprotocol.ResultFrame{CommandID: id.String(), Outcome: d.outcome, Error: d.errMsg}
	if d.result != nil {
		fr.Data, _ = json.Marshal(d.result)
	}
	if d.rawData != nil {
		fr.Data = d.rawData
	}
	raw, _ := json.Marshal(fr)
	_, err = tx.Exec(ctx, "UPDATE agent_commands SET status = $2, result = $3, error = $4 WHERE id = $1", id, d.status, raw, d.cmdErr)
	return id, err
}

// withAgent returns a mesh service of the fixture's team that talks to disp.
func (f *fixture) withAgent(disp agents.Dispatcher) *mesh.Service {
	jm := jobs.New(f.pool, nil)
	svc := mesh.NewService(mesh.Deps{Pool: f.pool, Bus: events.NewBus(), Jobs: jm, Dispatcher: disp, Tailscale: f.ts})
	mesh.RegisterJobs(jm, svc)
	svc.SetAwaitPoll(time.Millisecond)
	return svc
}

func TestCheckServerAsksTheAgentOncePerPeerInOrderAndReportsEachAnswer(t *testing.T) {
	f := newFixture(t)
	f.enable(f.a)
	f.enable(f.b)
	disp := &answerDisp{status: "succeeded", outcome: agentprotocol.OutcomeSucceeded, result: &agentprotocol.MeshCheckResult{Result: agentprotocol.MeshCheckConnected, Message: "ok", LatencyMs: 12}}
	svc := f.withAgent(disp)

	// Only a server with a node can check.
	f3, err := f.q.CreateServer(bg, db.CreateServerParams{WorkspaceID: f.ws, Name: "agent3"})
	require.NoError(t, err)
	_, err = svc.CheckServer(f.admin, f3.ID, nil)
	requireAPICode(t, err, apierr.CodeMeshServerRequired)
	_, err = svc.CheckServer(f.principalOf(policy.ScopeServersRead), f.a.ID, nil)
	requireAPICode(t, err, "forbidden")
	_, err = svc.CheckServer(f.admin, f.a.ID, &f3.ID)
	requireAPICode(t, err, "not_found")

	res, err := svc.CheckServer(f.admin, f.a.ID, nil)
	require.NoError(t, err)
	require.Len(t, res, 1)
	require.Equal(t, f.b.ID, *res[0].PeerServerID)
	require.Equal(t, agentprotocol.MeshCheckConnected, res[0].Outcome)
	require.Equal(t, "ok", res[0].Message)
	require.Equal(t, 12, res[0].LatencyMs)
	require.Equal(t, []agentprotocol.MeshCheckPayload{{PeerNodeID: f.b.ID.String()}}, disp.asked)

	// Naming the peer asks about that one only.
	disp.asked = nil
	res, err = svc.CheckServer(f.admin, f.b.ID, &f.a.ID)
	require.NoError(t, err)
	require.Len(t, res, 1)
	require.Equal(t, f.a.ID, *res[0].PeerServerID)
	require.Equal(t, []agentprotocol.MeshCheckPayload{{PeerNodeID: f.a.ID.String()}}, disp.asked)

	// Three servers: every other node is asked, in id order.
	f.setFacts(f3, agentprotocol.Facts{OS: "linux", Capabilities: []string{agentprotocol.CapMesh}, TunDevice: true})
	f.enable(f3)
	disp.asked = nil
	res, err = svc.CheckServer(f.admin, f.a.ID, nil)
	require.NoError(t, err)
	want := []string{f.b.ID.String(), f3.ID.String()}
	slices.Sort(want)
	require.Len(t, res, 2)
	require.Equal(t, want, []string{res[0].PeerServerID.String(), res[1].PeerServerID.String()})
	require.Len(t, disp.asked, 2)

	// A lone server checks its own node.
	solo := newFixture(t)
	solo.enable(solo.a)
	sdisp := &answerDisp{status: "succeeded", outcome: agentprotocol.OutcomeSucceeded, result: &agentprotocol.MeshCheckResult{Result: agentprotocol.MeshCheckConnected}}
	res, err = solo.withAgent(sdisp).CheckServer(solo.admin, solo.a.ID, nil)
	require.NoError(t, err)
	require.Len(t, res, 1)
	require.Nil(t, res[0].PeerServerID)
	require.Equal(t, []agentprotocol.MeshCheckPayload{{}}, sdisp.asked)
}

func TestDisablingAServerSucceedsEvenWhenTheCoordinatorRefusesToExpireItsKey(t *testing.T) {
	f := newFixture(t)
	f.enable(f.a)
	cmds := f.syncMeshCommands()
	_, err := f.svc.SecretsForCommand(bg, cmds[0].Kind, cmds[0].Payload)
	require.NoError(t, err)
	require.Len(t, f.hs.ActiveKeys(), 1)

	f.hs.FailNext(http.MethodPost, "/api/v1/preauthkey/expire", http.StatusInternalServerError)
	res, err := f.svc.SetServerMesh(f.admin, f.a.ID, mesh.ServerMeshInput{})
	require.NoError(t, err, "the node is gone from the control plane even if the key could not be expired now")
	require.True(t, res.Applied)
	_, err = f.q.GetServerMesh(bg, f.a.ID)
	require.ErrorIs(t, err, pgx.ErrNoRows)
	require.Len(t, f.hs.ActiveKeys(), 1, "the unexpired key is left for the key sweeper")
}
