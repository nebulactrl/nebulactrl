package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

const (
	meshSrv   = "4d4d4d4d-1111-4222-8333-444444444444"
	meshLink  = "3c3c3c3c-1111-4222-8333-444444444444"
	meshStray = "2b2b2b2b-1111-4222-8333-444444444444"
	meshKey   = "tskey-auth-ENGINEMESH-SECRET-123456"
	meshOn    = `{"BackendState":"Running","Self":{"ID":"nMESH1","DNSName":"nc-hetzner.tail1.ts.net.","TailscaleIPs":["100.64.0.9"]}}`

	meshSrvCtr  = "nc-mesh-4d4d4d4d"
	meshLinkCtr = "nc-meshl-3c3c3c3c"
)

func meshRig(t *testing.T) (*rig, *bytes.Buffer) {
	t.Helper()
	r := newNetRig(t, nil, func(c *Config) { c.OrphanMinAge = time.Nanosecond })
	r.eng.mesh.WaitRunning, r.eng.mesh.Poll = 30*time.Millisecond, time.Millisecond
	r.dk.onExec = func(_ string, argv []string, _ docker.ExecSpec, stdout, _ io.Writer) (int, error) {
		if len(argv) >= 3 && strings.Join(argv[:3], " ") == "tailscale status --json" {
			_, _ = io.WriteString(stdout, meshOn)
		}
		if len(argv) >= 2 && argv[1] == "ping" {
			_, _ = io.WriteString(stdout, "pong from x (100.64.0.8) via 1.2.3.4:41641 in 3ms\n")
		}
		return 0, nil
	}
	var logs bytes.Buffer
	r.eng.log = slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return r, &logs
}

func meshServerSpec() agentprotocol.MeshNodeSpec {
	return agentprotocol.MeshNodeSpec{
		NodeID: meshSrv, Kind: agentprotocol.MeshKindServer, Provider: agentprotocol.TailnetProviderTailscale,
		Hostname: "nc-hetzner", Tags: []string{"tag:nebula-mesh"}, Provision: true,
		Exposures: []agentprotocol.MeshExposure{{
			ServiceID: svcID, ServiceSlug: "web", EnvironmentID: envID, Purpose: agentprotocol.MeshPurposeService,
			ContainerPort: 80, MeshPort: 80,
		}},
	}
}

func meshLinkSpec() agentprotocol.MeshNodeSpec {
	return agentprotocol.MeshNodeSpec{
		NodeID: meshLink, Kind: agentprotocol.MeshKindLink, Provider: agentprotocol.TailnetProviderTailscale,
		Hostname: "nc-e0e0e0e0-web-to-db", EnvironmentID: envID, RemoteServerID: meshStray, TargetIP: "100.64.0.20",
		Aliases: []string{"postgres"}, HasInternalNet: true, Provision: true,
	}
}

func meshKeys2(ids ...string) map[string]string {
	out := map[string]string{}
	for _, id := range ids {
		out[agentprotocol.SecretMeshKeyPrefix+id] = meshKey
	}
	return out
}

func (r *rig) meshCommand(kind string, payload any, secrets map[string]string) agentprotocol.ResultFrame {
	r.t.Helper()
	raw, err := json.Marshal(payload)
	require.NoError(r.t, err)
	return r.eng.HandleCommand(context.Background(), agentprotocol.Command{ID: "cmd-mesh", Kind: kind, Payload: raw, Secrets: secrets})
}

func (r *rig) meshStatus(res agentprotocol.ResultFrame) agentprotocol.MeshStatus {
	r.t.Helper()
	require.Equal(r.t, agentprotocol.OutcomeSucceeded, res.Outcome, res.Error)
	var st agentprotocol.MeshStatus
	require.NoError(r.t, json.Unmarshal(res.Data, &st))
	return st
}

func (r *rig) exists(name string) bool {
	_, ok := r.dk.state(name)
	return ok
}

// seedMeshStray adds a link node nobody desires (old enough for every sweep).
func seedMeshStray(r *rig) {
	r.dk.mu.Lock()
	defer r.dk.mu.Unlock()
	r.dk.seq++
	r.dk.containers["strayid"] = &docker.ContainerInfo{
		ID: "strayid", Name: "nc-meshl-2b2b2b2b", State: "running", Running: true, Created: time.Now().Add(-time.Hour),
		Labels: map[string]string{
			agentprotocol.LabelManaged: "true", agentprotocol.LabelMesh: meshStray, agentprotocol.LabelMeshKind: agentprotocol.MeshKindLink,
		},
	}
}

func (f *fakeDocker) removedList() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.removed...)
}

func (f *fakeDocker) volsGoneList() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.volsGone...)
}

func TestSyncMeshCommandBringsUpTheCompleteSet(t *testing.T) {
	r, logs := meshRig(t)
	res := r.meshCommand(agentprotocol.KindSyncMesh, agentprotocol.SyncMeshPayload{
		Nodes: []agentprotocol.MeshNodeSpec{meshServerSpec(), meshLinkSpec()},
	}, meshKeys2(meshSrv, meshLink))
	st := r.meshStatus(res)
	require.Len(t, st.Nodes, 2)
	for _, n := range st.Nodes {
		require.Equal(t, agentprotocol.MeshStateOnline, n.State, n.Message)
	}

	require.True(t, r.exists(meshSrvCtr))
	require.True(t, r.exists(meshLinkCtr))
	srv, _ := r.dk.state(meshSrvCtr)
	_, isReplica := srv.Labels[agentprotocol.LabelService]
	require.False(t, isReplica)
	require.Contains(t, srv.Env, "TS_AUTHKEY="+meshKey)

	// The secret never leaves in the result, the logs or an event.
	require.NotContains(t, string(res.Data), meshKey)
	require.NotContains(t, logs.String(), meshKey)
	require.NotContains(t, r.snd.allLogText(), meshKey)
	require.Contains(t, logs.String(), "provisioning")

	// The next sync of the same set is a no-op.
	before := len(r.dk.created)
	r.meshCommand(agentprotocol.KindSyncMesh, agentprotocol.SyncMeshPayload{
		Nodes: []agentprotocol.MeshNodeSpec{meshServerSpec(), meshLinkSpec()},
	}, meshKeys2(meshSrv, meshLink))
	require.Len(t, r.dk.created, before, "idempotent")
}

func TestSyncMeshRemovesStraysButNotServicesOrOtherInfrastructure(t *testing.T) {
	r, _ := meshRig(t)
	seedMeshStray(r)
	r.dk.seed(oldCtr, svcID, depV1, true, time.Hour)
	r.dk.seed("nebulactrl-caddy", "", "", true, time.Hour)
	r.dk.seed("user-db", "", "", true, time.Hour)

	st := r.meshStatus(r.meshCommand(agentprotocol.KindSyncMesh, agentprotocol.SyncMeshPayload{
		Nodes: []agentprotocol.MeshNodeSpec{meshServerSpec()},
	}, meshKeys2(meshSrv)))
	require.Len(t, st.Nodes, 2)
	require.False(t, r.exists("nc-meshl-2b2b2b2b"))
	require.Contains(t, r.dk.volsGoneList(), "nc-mesh-2b2b2b2b-state")
	for _, keep := range []string{oldCtr, "nebulactrl-caddy", "user-db", meshSrvCtr} {
		require.True(t, r.exists(keep), keep)
	}
}

func TestRemoveMeshCommand(t *testing.T) {
	r, _ := meshRig(t)
	r.meshStatus(r.meshCommand(agentprotocol.KindSyncMesh, agentprotocol.SyncMeshPayload{
		Nodes: []agentprotocol.MeshNodeSpec{meshServerSpec(), meshLinkSpec()},
	}, meshKeys2(meshSrv, meshLink)))

	st := r.meshStatus(r.meshCommand(agentprotocol.KindRemoveMesh, agentprotocol.RemoveMeshPayload{NodeIDs: []string{meshLink}}, nil))
	require.Len(t, st.Nodes, 1)
	require.Equal(t, agentprotocol.MeshStateRemoved, st.Nodes[0].State)
	require.False(t, r.exists(meshLinkCtr))
	require.True(t, r.exists(meshSrvCtr))
	require.Contains(t, r.dk.volsGoneList(), "nc-mesh-3c3c3c3c-state")

	require.Equal(t, agentprotocol.OutcomeFailed, r.meshCommand(agentprotocol.KindRemoveMesh, agentprotocol.RemoveMeshPayload{}, nil).Outcome)
}

func TestMeshCommandsRejectMalformedPayloads(t *testing.T) {
	r, _ := meshRig(t)
	for _, kind := range []string{agentprotocol.KindSyncMesh, agentprotocol.KindRemoveMesh, agentprotocol.KindMeshCheck} {
		res := r.eng.HandleCommand(context.Background(), agentprotocol.Command{ID: "c", Kind: kind, Payload: json.RawMessage(`"nope"`)})
		require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome, kind)
	}
	res := r.meshCommand(agentprotocol.KindMeshCheck, agentprotocol.MeshCheckPayload{}, nil)
	require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome)
	require.Contains(t, res.Error, "mesh_check")
}

func TestMeshCheckCommandReportsNoPathForAnUnknownLink(t *testing.T) {
	r, _ := meshRig(t)
	res := r.meshCommand(agentprotocol.KindMeshCheck, agentprotocol.MeshCheckPayload{LinkID: meshLink, Port: 15001}, nil)
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, res.Error)
	var cr agentprotocol.MeshCheckResult
	require.NoError(t, json.Unmarshal(res.Data, &cr))
	require.Equal(t, agentprotocol.MeshCheckNoPath, cr.Result)
}

// ---- reconcile (hello) ----

func TestReconcileMeshConvergesInTheBackground(t *testing.T) {
	r, logs := meshRig(t)
	seedMeshStray(r)
	want := agentprotocol.SyncMeshPayload{Nodes: []agentprotocol.MeshNodeSpec{meshServerSpec(), meshLinkSpec()}}

	require.NoError(t, r.eng.Reconcile(context.Background(), agentprotocol.Reconcile{Mesh: &want, Secrets: meshKeys2(meshSrv, meshLink)}))
	r.eng.bg.Wait()

	require.True(t, r.exists(meshSrvCtr))
	require.True(t, r.exists(meshLinkCtr))
	require.False(t, r.exists("nc-meshl-2b2b2b2b"), "the stray goes: the frame is the complete set")
	require.NotContains(t, logs.String(), meshKey)
	srv, _ := r.dk.state(meshSrvCtr)
	require.Contains(t, srv.Env, "TS_AUTHKEY="+meshKey)

	// The same frame again (the next hello): nothing is created or removed.
	created, removed := len(r.dk.created), len(r.dk.removedList())
	require.NoError(t, r.eng.Reconcile(context.Background(), agentprotocol.Reconcile{Mesh: &want, Secrets: meshKeys2(meshSrv, meshLink)}))
	r.eng.bg.Wait()
	require.Len(t, r.dk.created, created)
	require.Len(t, r.dk.removedList(), removed)
}

// A control-plane restart never removes a node: a hello frame without a mesh
// set changes nothing, and the sweeps that do run on every hello never see
// mesh containers.
func TestReconcileWithoutAMeshSetNeverRemovesANode(t *testing.T) {
	r, _ := meshRig(t)
	want := agentprotocol.SyncMeshPayload{Nodes: []agentprotocol.MeshNodeSpec{meshServerSpec(), meshLinkSpec()}}
	require.NoError(t, r.eng.Reconcile(context.Background(), agentprotocol.Reconcile{Mesh: &want, Secrets: meshKeys2(meshSrv, meshLink)}))
	r.eng.bg.Wait()
	seedMeshStray(r)
	removed := len(r.dk.removedList())

	// A fresh control plane that has not computed the mesh yet, with and
	// without desired services (the orphan sweep and the network sweep run).
	r.dk.seed("nc-gone-aaaaaaaa-0", "9f9f9f9f-0000-4000-8000-000000000009", "d9d9d9d9-0000-4000-8000-000000000009", true, time.Hour)
	require.NoError(t, r.eng.Reconcile(context.Background(), agentprotocol.Reconcile{}))
	require.False(t, r.exists("nc-gone-aaaaaaaa-0"), "the orphan sweep is active: it removes service orphans")
	p := basePayload()
	res, dr := r.deploy(p, nil)
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, dr.Message)
	require.NoError(t, r.eng.Reconcile(context.Background(), desiredFor(p, 1)))
	r.eng.bg.Wait()

	for _, keep := range []string{meshSrvCtr, meshLinkCtr, "nc-meshl-2b2b2b2b"} {
		require.True(t, r.exists(keep), keep)
		c, _ := r.dk.state(keep)
		require.True(t, c.Running, keep)
	}
	require.Contains(t, r.dk.removedList()[removed:], "nc-gone-aaaaaaaa-0")
	for _, n := range r.dk.removedList()[removed:] {
		require.False(t, strings.HasPrefix(n, "nc-mesh"), "removed %s", n)
	}
	require.NotContains(t, r.dk.netsRemoved, agentprotocol.MeshNetwork)
}

func TestReconcileWithAnEmptyMeshSetRemovesMeshNodesOnly(t *testing.T) {
	r, _ := meshRig(t)
	want := agentprotocol.SyncMeshPayload{Nodes: []agentprotocol.MeshNodeSpec{meshServerSpec()}}
	require.NoError(t, r.eng.Reconcile(context.Background(), agentprotocol.Reconcile{Mesh: &want, Secrets: meshKeys2(meshSrv)}))
	r.eng.bg.Wait()
	r.dk.seed("user-db", "", "", true, time.Hour)
	require.True(t, r.exists(meshSrvCtr))

	require.NoError(t, r.eng.Reconcile(context.Background(), agentprotocol.Reconcile{Mesh: &agentprotocol.SyncMeshPayload{}}))
	r.eng.bg.Wait()
	require.False(t, r.exists(meshSrvCtr), "mesh disabled: an explicit empty set removes the nodes")
	require.True(t, r.exists("user-db"))
}

// ---- the nc-svc alias ----

const meshAlias = "nc-svc-5e5e5e5e"

func TestDeployGivesTheExposedServiceTheMeshAliasOnEveryNetwork(t *testing.T) {
	r := newNetRig(t, nil, nil)
	p := basePayload()
	p.MeshAlias = meshAlias
	res, dr := r.deploy(p, nil)
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, dr.Message)

	c, _ := r.dk.state(newCtr)
	require.Equal(t, []string{"web", meshAlias}, c.Networks[netEnv].Aliases)

	// Egress blocked: the internal network only, with both aliases.
	r2 := newNetRig(t, nil, nil)
	p2 := basePayload()
	p2.MeshAlias, p2.BlockEgress = meshAlias, true
	res, dr = r2.deploy(p2, nil)
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, dr.Message)
	c, _ = r2.dk.state(newCtr)
	require.Equal(t, []string{netInt}, r2.dk.networksOf(newCtr))
	require.Equal(t, []string{"web", meshAlias}, c.Networks[netInt].Aliases)

	// Egress blocked elsewhere in the environment: both networks, alias on both.
	r3 := newNetRig(t, nil, nil)
	p3 := basePayload()
	p3.MeshAlias, p3.EnvHasEgressBlocked = meshAlias, true
	res, dr = r3.deploy(p3, nil)
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, dr.Message)
	c, _ = r3.dk.state(newCtr)
	require.Equal(t, []string{"web", meshAlias}, c.Networks[netEnv].Aliases)
	require.Equal(t, []string{"web", meshAlias}, c.Networks[netInt].Aliases)
}

func TestAServiceThatIsNotExposedCarriesNoMeshAlias(t *testing.T) {
	r := newNetRig(t, nil, nil)
	res, dr := r.deploy(basePayload(), nil)
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, dr.Message)
	c, _ := r.dk.state(newCtr)
	require.Equal(t, []string{"web"}, c.Networks[netEnv].Aliases)
}

func TestSharedNetworksCarryTheMeshAliasToo(t *testing.T) {
	r := newNetRig(t, nil, nil)
	p := basePayload()
	p.MeshAlias = meshAlias
	p.SharedNetworks = []agentprotocol.SharedNetworkAttachment{{Name: sharedNet, Alias: netAlias}}
	res, dr := r.deploy(p, nil)
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, dr.Message)
	c, _ := r.dk.state(newCtr)
	require.Contains(t, c.Networks[sharedNet].Aliases, meshAlias)
	require.Contains(t, c.Networks[netEnv].Aliases, meshAlias)
}

// The alias is part of the replica spec label, so a replica added later by the
// convergence loop is exposed exactly like its siblings, and a network attach
// on reconcile carries it.
func TestMeshAliasSurvivesTheSpecLabelAndAReconcileAttach(t *testing.T) {
	r := newNetRig(t, nil, nil)
	p := basePayload()
	p.MeshAlias = meshAlias
	res, dr := r.deploy(p, nil)
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, dr.Message)

	c, _ := r.dk.state(newCtr)
	back, ok := payloadFromContainer(c)
	require.True(t, ok)
	require.Equal(t, meshAlias, back.MeshAlias)

	desired := desiredFor(p, 1)
	desired.Services[0].EnvHasEgressBlocked = true
	require.NoError(t, r.eng.Reconcile(context.Background(), desired))
	c, _ = r.dk.state(newCtr)
	require.Equal(t, []string{netEnv, netInt}, r.dk.networksOf(newCtr))
	require.Equal(t, []string{"web", meshAlias}, c.Networks[netInt].Aliases, "the attach on reconcile keeps the exposure")
}

func TestDeploySucceededRefreshesTheServeTargets(t *testing.T) {
	r, _ := meshRig(t)
	r.previous() // v1 runs without the alias: the node forwards to its unique name
	lastServe := func() string {
		r.dk.mu.Lock()
		defer r.dk.mu.Unlock()
		for i := len(r.dk.copies) - 1; i >= 0; i-- {
			if strings.Contains(string(r.dk.copies[i].data), "serve.json") {
				return string(r.dk.copies[i].data)
			}
		}
		return ""
	}
	r.meshStatus(r.meshCommand(agentprotocol.KindSyncMesh, agentprotocol.SyncMeshPayload{
		Nodes: []agentprotocol.MeshNodeSpec{meshServerSpec()},
	}, meshKeys2(meshSrv)))
	require.Contains(t, lastServe(), oldCtr+":80")

	p := basePayload()
	p.MeshAlias = meshAlias
	res, dr := r.deploy(p, nil)
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, dr.Message)
	r.eng.ResultSent(context.Background(), agentprotocol.Command{Kind: agentprotocol.KindDeploy}, res)
	r.eng.bg.Wait()

	require.Contains(t, lastServe(), meshAlias+":80", "the node now forwards to the alias the new container carries")
	require.NotContains(t, lastServe(), oldCtr)
}
