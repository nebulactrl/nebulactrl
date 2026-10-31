package mesh

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

func envOf(t *testing.T, f *fakeEngine, name string) []string {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.find(name)
	require.NotNil(t, c, name)
	return c.Env
}

func TestSyncProvisionsAServerNodeWithTheKey(t *testing.T) {
	f := newFake(statusOnline)
	var logged []string
	var mu sync.Mutex
	st := sync1(t, newManager(f), keysFor(srvID), func(s string) { mu.Lock(); logged = append(logged, s); mu.Unlock() }, provision(serverSpec()))

	require.True(t, st.TunDevice)
	require.Len(t, st.Nodes, 1)
	n := st.Nodes[0]
	require.Equal(t, agentprotocol.MeshStateOnline, n.State, n.Message)
	require.Equal(t, srvID, n.NodeID)
	require.Equal(t, agentprotocol.MeshKindServer, n.Kind)
	require.Equal(t, "nc-hetzner.tail1234.ts.net", n.FQDN, "the name is read back, never assumed")
	require.Equal(t, []string{"100.64.0.7", "fd7a:115c:a1e0::7"}, n.IPs)
	require.Equal(t, "nNODE1CNTRL", n.DeviceID)
	require.Len(t, n.Peers, 1, "only devices with the mesh tag are peers")
	require.Equal(t, "100.64.0.9", n.Peers[0].IP)
	require.True(t, n.Peers[0].Direct)

	require.Len(t, f.created, 1)
	require.Contains(t, f.created[0].Env, "TS_AUTHKEY="+testKey)
	require.True(t, f.volumes["nc-mesh-5e5e5e5e-state"] && f.volumes["nc-mesh-5e5e5e5e-config"])
	require.True(t, f.networks["nebulactrl-mesh"])
	require.Zero(t, f.pulls, "the pinned image is already local")
}

func TestSyncConnectsExposureNetworksLive(t *testing.T) {
	f := newFake(statusOnline)
	m := newManager(f)
	spec := serverSpec()
	spec.Exposures = append(spec.Exposures, agentprotocol.MeshExposure{
		ServiceID: "77777777-1111-2222-3333-444444444444", ServiceSlug: "db", EnvironmentID: envB,
		Purpose: agentprotocol.MeshPurposeReplication, ContainerPort: 5432, MeshPort: 15002, BlockEgress: true,
	})
	st := sync1(t, m, keysFor(srvID), nil, provision(spec))
	require.Equal(t, agentprotocol.MeshStateOnline, st.Nodes[0].State, st.Nodes[0].Message)
	require.True(t, f.networks["nebulactrl-env-bbbbbbbb-int"], "the internal network is ensured as internal")
	require.Equal(t, 1, f.count("network-internal nebulactrl-env-bbbbbbbb-int"))

	info, _ := f.ContainerInspect(context.Background(), "nc-mesh-5e5e5e5e")
	require.Contains(t, info.Networks, "nebulactrl-mesh")
	require.Contains(t, info.Networks, "nebulactrl-env-aaaaaaaa")
	require.Contains(t, info.Networks, "nebulactrl-env-bbbbbbbb-int")
	require.NotContains(t, info.Networks, "nebulactrl-env-bbbbbbbb", "a blocked database is only on -int")

	// The exposure of env B goes away: the node leaves that network, no recreate.
	spec.Exposures = spec.Exposures[:2]
	f.mark()
	sync1(t, m, nil, nil, spec)
	info, _ = f.ContainerInspect(context.Background(), "nc-mesh-5e5e5e5e")
	require.NotContains(t, info.Networks, "nebulactrl-env-bbbbbbbb-int")
	require.Contains(t, info.Networks, "nebulactrl-mesh", "the mesh bridge is never dropped")
	require.Equal(t, 1, f.count("disconnect nebulactrl-env-bbbbbbbb-int"))
	require.Zero(t, f.count("create "))
}

func TestSyncIsIdempotent(t *testing.T) {
	f := newFake(statusOnline)
	m := newManager(f)
	nodes := []agentprotocol.MeshNodeSpec{provision(serverSpec()), provision(linkSpec())}
	keys := keysFor(srvID, linkID)
	first := sync1(t, m, keys, nil, nodes...)
	for _, n := range first.Nodes {
		require.Equal(t, agentprotocol.MeshStateOnline, n.State, n.Message)
	}
	require.Len(t, f.created, 2)

	// The same complete set again (hello after a reconnect, with the same keys).
	f.mark()
	second := sync1(t, m, keys, nil, nodes...)
	require.Zero(t, f.count("create "), "%v", f.events)
	require.Zero(t, f.count("remove "))
	require.Zero(t, f.count("stop "))
	require.Zero(t, f.count("volume"), "state volumes are the device identities: never touched by a replay")
	require.Zero(t, f.count("copy "))
	require.Zero(t, f.count("connect "))
	require.Zero(t, f.count("disconnect "))
	require.Len(t, second.Nodes, 2)
	for i := range second.Nodes {
		require.Equal(t, first.Nodes[i].IPs, second.Nodes[i].IPs)
		require.Equal(t, agentprotocol.MeshStateOnline, second.Nodes[i].State, second.Nodes[i].Message)
	}
}

// A hello replays the desired set. The control plane may still mark the node
// as provisioning but the single-use key was spent: the node must not be wiped
// (that would burn a key and change its tailnet address).
func TestReplayWithoutOrWithAnotherKeyHandling(t *testing.T) {
	f := newFake(statusOnline)
	m := newManager(f)
	spec := provision(serverSpec())
	sync1(t, m, keysFor(srvID), nil, spec)
	f.mark()

	st := sync1(t, m, nil, nil, spec) // no key rides the replay
	require.Equal(t, agentprotocol.MeshStateOnline, st.Nodes[0].State, st.Nodes[0].Message)
	require.Zero(t, f.count("create "))
	require.Zero(t, f.count("volume- "))
	require.Zero(t, f.count("remove "))

	// A different key is an explicit re-provision: wipe state, enrol again.
	f.mark()
	other := map[string]string{agentprotocol.SecretMeshKeyPrefix + srvID: "tskey-auth-SECOND-0987654321"}
	st = sync1(t, m, other, nil, spec)
	require.Equal(t, agentprotocol.MeshStateOnline, st.Nodes[0].State, st.Nodes[0].Message)
	rm, vs, cr := f.index("remove nc-mesh-5e5e5e5e"), f.index("volume- nc-mesh-5e5e5e5e-state"), f.index("create ")
	require.True(t, rm >= 0 && vs > rm && cr > vs, "old node and state go before the new node is created: %v", f.events)
	require.Contains(t, envOf(t, f, "nc-mesh-5e5e5e5e"), "TS_AUTHKEY=tskey-auth-SECOND-0987654321")
}

func TestProvisionOfAMissingNodeNeedsAKey(t *testing.T) {
	f := newFake(statusOnline)
	st := sync1(t, newManager(f), nil, nil, provision(serverSpec()))
	require.Equal(t, agentprotocol.MeshStateNeedsAttn, st.Nodes[0].State)
	require.Contains(t, st.Nodes[0].Message, "needs an auth key")
	require.Empty(t, f.created)
	require.Empty(t, f.events, "nothing touched Docker")
}

func TestInvalidSpecNeverTouchesDockerAndIsNotRemoved(t *testing.T) {
	f := newFake(statusOnline)
	bad := serverSpec()
	bad.Hostname = "Bad Host"
	st := sync1(t, newManager(f), nil, nil, bad)
	require.Equal(t, agentprotocol.MeshStateNeedsAttn, st.Nodes[0].State)
	require.Contains(t, st.Nodes[0].Message, "Invalid mesh node")
	require.Empty(t, f.events)

	// A node that is listed but invalid keeps its container (not a stray).
	f2 := newFake(statusOnline)
	m := newManager(f2)
	sync1(t, m, keysFor(srvID), nil, provision(serverSpec()))
	f2.mark()
	bad = serverSpec()
	bad.Hostname = "Bad Host"
	sync1(t, m, nil, nil, bad)
	require.True(t, f2.has("nc-mesh-5e5e5e5e"))
	require.Zero(t, f2.count("remove "))

	// Listed twice.
	st = sync1(t, newManager(newFake(statusOnline)), keysFor(srvID), nil, provision(serverSpec()), provision(serverSpec()))
	require.Len(t, st.Nodes, 2)
	require.Contains(t, st.Nodes[1].Message, "listed twice")
}

// Sync is the COMPLETE set: mesh nodes that are not in it go (container and
// both volumes), everything that is not a mesh node stays.
func TestSyncRemovesStrayMeshNodesAndLeavesEverythingElseAlone(t *testing.T) {
	f := newFake(statusOnline)
	m := newManager(f)
	stray := "9c9c9c9c-1111-2222-3333-444444444444"
	f.seed("nc-meshl-9c9c9c9c", true, map[string]string{
		agentprotocol.LabelManaged: "true", agentprotocol.LabelMesh: stray, agentprotocol.LabelMeshKind: agentprotocol.MeshKindLink,
	})
	f.volumes["nc-mesh-9c9c9c9c-state"], f.volumes["nc-mesh-9c9c9c9c-config"] = true, true

	// Not mesh nodes: a service replica, the proxy, a service tailnet sidecar,
	// a container nobody manages, and a mesh-named container without the label.
	f.seed("nc-web-d1d1d1d1-0", true, map[string]string{agentprotocol.LabelManaged: "true", agentprotocol.LabelService: svcAPI})
	f.seed("nebulactrl-caddy", true, map[string]string{agentprotocol.LabelManaged: "true"})
	f.seed("nc-ts-5e5e5e5e", true, map[string]string{agentprotocol.LabelManaged: "true", agentprotocol.LabelTailnet: svcAPI})
	f.seed("postgres-of-the-user", true, map[string]string{"app": "db"})
	f.seed("nc-mesh-lookalike", true, map[string]string{})
	f.volumes["nc-vol-user-data"] = true

	st := sync1(t, m, keysFor(srvID), nil, provision(serverSpec()))
	require.Len(t, st.Nodes, 2)
	var removed *agentprotocol.MeshNodeStatus
	for i := range st.Nodes {
		if st.Nodes[i].NodeID == stray {
			removed = &st.Nodes[i]
		}
	}
	require.NotNil(t, removed)
	require.Equal(t, agentprotocol.MeshStateRemoved, removed.State)
	require.Equal(t, agentprotocol.MeshKindLink, removed.Kind)

	require.False(t, f.has("nc-meshl-9c9c9c9c"))
	require.False(t, f.volumes["nc-mesh-9c9c9c9c-state"] || f.volumes["nc-mesh-9c9c9c9c-config"])
	for _, keep := range []string{"nc-web-d1d1d1d1-0", "nebulactrl-caddy", "nc-ts-5e5e5e5e", "postgres-of-the-user", "nc-mesh-lookalike", "nc-mesh-5e5e5e5e"} {
		require.True(t, f.has(keep), keep)
	}
	require.True(t, f.volumes["nc-vol-user-data"])
	require.Equal(t, 1, f.count("remove "), "exactly the stray: %v", f.events)

	// An empty complete set removes every mesh node and nothing else.
	st = sync1(t, m, nil, nil)
	require.Len(t, st.Nodes, 1)
	require.Equal(t, agentprotocol.MeshStateRemoved, st.Nodes[0].State)
	require.False(t, f.has("nc-mesh-5e5e5e5e"))
	for _, keep := range []string{"nc-web-d1d1d1d1-0", "nebulactrl-caddy", "nc-ts-5e5e5e5e", "postgres-of-the-user", "nc-mesh-lookalike"} {
		require.True(t, f.has(keep), keep)
	}
}

func TestRemoveNamedNodesOnly(t *testing.T) {
	f := newFake(statusOnline)
	m := newManager(f)
	sync1(t, m, keysFor(srvID, linkID), nil, provision(serverSpec()), provision(linkSpec()))
	f.seed("nc-web-d1d1d1d1-0", true, map[string]string{agentprotocol.LabelManaged: "true", agentprotocol.LabelService: svcAPI})

	st := m.Remove(context.Background(), []string{linkID}, nil)
	require.Len(t, st.Nodes, 1)
	require.Equal(t, agentprotocol.MeshStateRemoved, st.Nodes[0].State)
	require.Equal(t, agentprotocol.MeshKindLink, st.Nodes[0].Kind)
	require.False(t, f.has("nc-meshl-1a1a1a1a"))
	require.False(t, f.volumes["nc-mesh-1a1a1a1a-state"])
	require.True(t, f.has("nc-mesh-5e5e5e5e"), "the other node stays")
	require.True(t, f.volumes["nc-mesh-5e5e5e5e-state"])
	require.True(t, f.has("nc-web-d1d1d1d1-0"))

	// Removing what is not there is fine; an invalid id never reaches Docker.
	f.mark()
	require.Equal(t, agentprotocol.MeshStateRemoved, m.Remove(context.Background(), []string{linkID}, nil).Nodes[0].State)
	bad := m.Remove(context.Background(), []string{"../../etc"}, nil)
	require.Equal(t, agentprotocol.MeshStateNeedsAttn, bad.Nodes[0].State)
	require.Zero(t, f.count("remove "))

	// A removed node is no longer desired.
	require.Nil(t, m.desiredSpec(linkID))
	require.NotNil(t, m.desiredSpec(srvID))
}

func TestLinkNodeAttachesUnderAliasesOnEnvAndInternalNetworks(t *testing.T) {
	f := newFake(statusOnline)
	m := newManager(f)
	st := sync1(t, m, keysFor(linkID), nil, provision(linkSpec()))
	n := st.Nodes[0]
	require.Equal(t, agentprotocol.MeshStateOnline, n.State, n.Message)
	require.NotNil(t, n.TargetReachable)
	require.True(t, *n.TargetReachable)
	require.True(t, f.networks["nebulactrl-env-aaaaaaaa"])
	require.True(t, f.networks["nebulactrl-env-aaaaaaaa-int"])
	require.Equal(t, 1, f.count("network-internal nebulactrl-env-aaaaaaaa-int"))

	info, _ := f.ContainerInspect(context.Background(), "nc-meshl-1a1a1a1a")
	require.Equal(t, []string{"api", "postgres"}, info.Networks["nebulactrl-env-aaaaaaaa"].Aliases)
	require.Equal(t, []string{"api", "postgres"}, info.Networks["nebulactrl-env-aaaaaaaa-int"].Aliases)
	require.Contains(t, envOf(t, f, "nc-meshl-1a1a1a1a"), "TS_TAILNET_TARGET_IP=100.64.0.9")
	require.Zero(t, f.count("copy "), "a link node has no serve config")
}

// A change of the provider set re-attaches the node, never recreates it.
func TestLinkAliasChangeReattachesOnlyWhenTheSetChanged(t *testing.T) {
	f := newFake(statusOnline)
	m := newManager(f)
	spec := linkSpec()
	sync1(t, m, keysFor(linkID), nil, provision(spec))

	// Same set in another order: nothing happens.
	f.mark()
	spec.Aliases = []string{"api", "postgres", "api"}
	sync1(t, m, nil, nil, spec)
	require.Zero(t, f.count("disconnect "))
	require.Zero(t, f.count("connect "))

	// A provider is added.
	f.mark()
	spec.Aliases = []string{"api", "cache", "postgres"}
	st := sync1(t, m, nil, nil, spec)
	require.Equal(t, agentprotocol.MeshStateOnline, st.Nodes[0].State, st.Nodes[0].Message)
	require.Equal(t, 2, f.count("disconnect "), "env and -int")
	require.Equal(t, 1, f.count("connect nebulactrl-env-aaaaaaaa nc-meshl-1a1a1a1a api,cache,postgres"), "%v", f.events)
	require.Equal(t, 1, f.count("connect nebulactrl-env-aaaaaaaa-int nc-meshl-1a1a1a1a api,cache,postgres"))
	require.Zero(t, f.count("create "))
	require.Zero(t, f.count("remove "))
	require.Zero(t, f.count("volume"))

	// The environment's -int network goes away (the last blocker left): leave it.
	f.mark()
	spec.HasInternalNet = false
	sync1(t, m, nil, nil, spec)
	require.Equal(t, 1, f.count("disconnect nebulactrl-env-aaaaaaaa-int"))
	require.Zero(t, f.count("create "))
}

func TestChangedConfigurationRecreatesButKeepsTheIdentityVolume(t *testing.T) {
	f := newFake(statusOnline)
	m := newManager(f)
	spec := linkSpec()
	sync1(t, m, keysFor(linkID), nil, provision(spec))
	f.mark()

	spec.TargetIP = "100.64.0.20" // the remote node was re-provisioned
	st := sync1(t, m, nil, nil, spec)
	require.Equal(t, agentprotocol.MeshStateOnline, st.Nodes[0].State, st.Nodes[0].Message)
	require.Equal(t, 1, f.count("create "))
	require.Zero(t, f.count("volume- "), "the state volume (the device identity) survives")
	for _, e := range f.created[0].Env {
		require.False(t, strings.HasPrefix(e, "TS_AUTHKEY="), "an existing identity needs no key")
	}
	require.Contains(t, f.created[0].Env, "TS_TAILNET_TARGET_IP=100.64.0.20")
	require.Equal(t, KeyFingerprint(testKey), f.created[0].Labels[labelKeyFP], "the provisioning marker is carried over so a replay stays a no-op")
}

func TestNodeStatesFromTailscale(t *testing.T) {
	f := newFake(statusLogin)
	st := sync1(t, newManager(f), keysFor(srvID), nil, provision(serverSpec()))
	require.Equal(t, agentprotocol.MeshStateNeedsAttn, st.Nodes[0].State)
	require.Contains(t, st.Nodes[0].Message, "Re-provision")
	require.Contains(t, st.Nodes[0].Message, "not logged in", "the last log lines explain why")

	require.Equal(t, agentprotocol.MeshStateOnline, ParseStatus([]byte(statusOnline), nil).State)
	require.Equal(t, agentprotocol.MeshStateNeedsAttn, ParseStatus([]byte(statusLogin), nil).State)
	require.Equal(t, agentprotocol.MeshStateOffline, ParseStatus([]byte(`{"BackendState":"Stopped"}`), nil).State)
	require.Equal(t, agentprotocol.MeshStateProvisioning, ParseStatus([]byte(`{"BackendState":"Starting"}`), nil).State)
	require.Equal(t, agentprotocol.MeshStateProvisioning, ParseStatus([]byte(`not json`), nil).State)

	all := ParseStatus([]byte(statusOnline), nil)
	require.Len(t, all.Peers, 2, "no tag filter: every peer")
	tagged := ParseStatus([]byte(statusOnline), []string{"tag:nebula-mesh"})
	require.Len(t, tagged.Peers, 1)
}

func TestMissingTunDeviceIsAClearMessage(t *testing.T) {
	f := newFake(statusOnline)
	f.createErr = errors.New(`error gathering device information while adding custom device "/dev/net/tun": no such file or directory`)
	st := sync1(t, newManager(f), keysFor(linkID), nil, provision(linkSpec()))
	require.Equal(t, agentprotocol.MeshStateNeedsAttn, st.Nodes[0].State)
	require.Contains(t, st.Nodes[0].Message, "/dev/net/tun is not available")

	// A server node needs no TUN: the same Docker error is just an error.
	st = sync1(t, newManager(f), keysFor(srvID), nil, provision(serverSpec()))
	require.Contains(t, st.Nodes[0].Message, "Could not create the mesh node")
}

// ---- key redaction ----

func TestKeyNeverReachesLogsStatusOrErrors(t *testing.T) {
	f := newFake(statusOnline)
	var mu sync.Mutex
	var logged []string
	logf := func(s string) { mu.Lock(); logged = append(logged, s); mu.Unlock() }

	// Docker echoes the env in its error.
	f.createErr = errors.New("docker rejected env TS_AUTHKEY=" + testKey)
	st := sync1(t, newManager(f), keysFor(srvID), logf, provision(serverSpec()))
	require.Equal(t, agentprotocol.MeshStateNeedsAttn, st.Nodes[0].State)
	require.NotContains(t, st.Nodes[0].Message, testKey, "the key never leaves the agent through an error")
	require.Contains(t, st.Nodes[0].Message, "••••••")

	// tailscaled prints the key in its log tail (it should not, but a failure
	// message must not become the leak).
	f2 := newFake(statusLogin)
	f2.logs = "2026-10-26T10:00:00.000000000Z login failed with authkey " + testKey + "\n"
	st = sync1(t, newManager(f2), keysFor(srvID), logf, provision(serverSpec()))
	require.NotContains(t, st.Nodes[0].Message, testKey)
	require.Contains(t, st.Nodes[0].Message, "••••••")

	f3 := newFake(statusOnline)
	sync1(t, newManager(f3), keysFor(linkID), logf, provision(linkSpec()))
	sync1(t, newManager(newFake(statusOnline)), keysFor(linkID), logf, provision(linkSpec()))

	mu.Lock()
	defer mu.Unlock()
	require.NotEmpty(t, logged)
	for _, l := range logged {
		require.NotContains(t, l, testKey, "every progress line is redacted")
	}
	for _, f := range []*fakeEngine{f, f2, f3} {
		for _, e := range f.events {
			require.NotContains(t, e, testKey)
		}
		for _, c := range f.copies {
			require.NotContains(t, c.content, testKey)
		}
	}
	for _, c := range f3.created {
		for k, v := range c.Labels {
			require.NotContains(t, k+v, testKey)
		}
	}
}

func TestHeartbeatStatusRedactsTheKeyAStillProvisionedNodeCarries(t *testing.T) {
	f := newFake(statusOnline)
	m := newManager(f)
	require.Nil(t, m.Status(context.Background()), "no nodes, no field")

	sync1(t, m, keysFor(srvID), nil, provision(serverSpec()))
	f.mu.Lock()
	c := f.find("nc-mesh-5e5e5e5e")
	c.Running, c.State, c.ExitCode, c.RestartCount = false, "exited", 1, 0
	f.mu.Unlock()
	f.logs = "2026-10-26T10:00:00.000000000Z fatal: invalid key " + testKey + "\n"
	m.forget(srvID)

	st := m.Status(context.Background())
	require.NotNil(t, st)
	require.Len(t, st.Nodes, 1)
	require.Equal(t, agentprotocol.MeshStateNeedsAttn, st.Nodes[0].State)
	require.Contains(t, st.Nodes[0].Message, "invalid key")
	require.NotContains(t, st.Nodes[0].Message, testKey)
	require.True(t, st.TunDevice)
}

func TestStatusReportsEveryNodeAndCachesTheExec(t *testing.T) {
	f := newFake(statusOnline)
	m := newManager(f)
	sync1(t, m, keysFor(srvID, linkID), nil, provision(serverSpec()), provision(linkSpec()))
	f.seed("nc-web-d1d1d1d1-0", true, map[string]string{agentprotocol.LabelManaged: "true", agentprotocol.LabelService: svcAPI})

	st := m.Status(context.Background())
	require.Len(t, st.Nodes, 2, "only mesh nodes")
	require.Equal(t, srvID, st.Nodes[0].NodeID, "sorted by container name")
	require.Equal(t, linkID, st.Nodes[1].NodeID)
	require.True(t, st.TunDevice)
}

// The cache makes a heartbeat cheap: a running node is asked at most every statusEvery.
func TestStatusIsCachedBetweenHeartbeats(t *testing.T) {
	f := newFake(statusOnline)
	m := newManager(f)
	sync1(t, m, keysFor(srvID), nil, provision(serverSpec()))
	m.Status(context.Background())
	before := f.execs
	m.Status(context.Background())
	require.Equal(t, before, f.execs)
}

func TestContainerNames(t *testing.T) {
	require.Equal(t, "nc-mesh-5e5e5e5e", ContainerName(serverSpec()))
	require.Equal(t, "nc-meshl-1a1a1a1a", ContainerName(linkSpec()))
}

var _ = docker.ErrNotFound
