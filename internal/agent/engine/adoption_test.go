package engine

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/internal/agent/health"
	"github.com/nebulactrl/nebulactrl/internal/agent/netpool"
	"github.com/nebulactrl/nebulactrl/internal/agent/proxy"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

const (
	adoptSvc = "5a5a5a5a-0000-4000-8000-0000000000aa"
	adoptCtr = "nc-old-abcdef12-0"
	adoptNet = "nebulactrl-env-abcdef12"
)

func adoptRoute() agentprotocol.Route {
	return agentprotocol.Route{ServiceID: adoptSvc, Hosts: []string{"old.example.com"}, Upstreams: []string{adoptCtr + ":80"}}
}

// adoptRig runs a host that holds a workload no control plane knows: a
// container of adoptSvc created an hour ago, on its environment network, with
// a live route; the orphan sweeps would remove all of it.
func adoptRig(t *testing.T) *rig {
	t.Helper()
	r := newRig(t, nil)
	r.eng.cfg.OrphanMinAge = time.Nanosecond
	c := r.dk.seed(adoptCtr, adoptSvc, "d3d3d3d3-0000-4000-8000-000000000003", true, time.Hour)
	c.Networks = map[string]docker.ContainerNetwork{adoptNet: {}}
	addNet(r, adoptNet, false, time.Hour)
	require.NoError(t, r.px.Apply([]agentprotocol.Route{adoptRoute()}))
	r.px.applies = nil
	return r
}

func TestAdoptionHoldKeepsUnknownContainersRoutesAndNetworks(t *testing.T) {
	r := adoptRig(t)
	addNet(r, "nebulactrl-env-99999999", false, time.Hour) // empty: swept without a hold when nothing keeps it
	task := r.dk.seed("nc-task-web-old00000", "", "", false, time.Hour)
	task.Labels = map[string]string{agentprotocol.LabelManaged: "true", agentprotocol.LabelTaskRun: "x"}

	require.NoError(t, r.eng.Reconcile(context.Background(), agentprotocol.Reconcile{KeepUnknown: true}))
	require.True(t, running(t, r.dk, adoptCtr), "a held server never sweeps an unknown service's container")
	require.Equal(t, []agentprotocol.Route{adoptRoute()}, r.px.Current(), "its route keeps serving")
	_, ok := r.dk.state(task.Name)
	require.True(t, ok, "task containers are kept while held")
	require.Empty(t, r.dk.netsRemoved, "no network is swept while held")

	// A route table pushed by command does not drop it either.
	_, pr := r.command(agentprotocol.KindSyncRoutes, agentprotocol.SyncRoutesPayload{Routes: []agentprotocol.Route{}}, nil)
	require.Equal(t, agentprotocol.OutcomeSucceeded, pr.Outcome, pr.Message)
	require.Equal(t, []agentprotocol.Route{adoptRoute()}, r.px.Current())
}

func TestAdoptionHoldYieldsToTheTablesOwnHosts(t *testing.T) {
	r := adoptRig(t)
	other := agentprotocol.Route{ServiceID: "other", Hosts: []string{"old.example.com"}, Upstreams: []string{"nc-other-aaaaaaaa-0:80"}}
	require.NoError(t, r.eng.Reconcile(context.Background(), agentprotocol.Reconcile{
		KeepUnknown: true,
		Services:    []agentprotocol.DesiredService{{ServiceID: "other", DeploymentID: "d", Replicas: 1, Routes: []agentprotocol.Route{other}}},
	}))
	require.Equal(t, []agentprotocol.Route{other}, r.px.Current(), "a kept route never collides with a host of the new table")
	require.True(t, running(t, r.dk, adoptCtr), "the container is kept all the same")
}

func TestAdoptionHoldEndsWithTheFrameThatNoLongerHoldsIt(t *testing.T) {
	r := adoptRig(t)
	require.NoError(t, r.eng.Reconcile(context.Background(), agentprotocol.Reconcile{KeepUnknown: true}))
	require.True(t, running(t, r.dk, adoptCtr))

	require.NoError(t, r.eng.Reconcile(context.Background(), agentprotocol.Reconcile{}))
	_, ok := r.dk.state(adoptCtr)
	require.False(t, ok, "without the hold the old orphan is swept as before")
	require.Empty(t, r.px.Current(), "and its route goes with it")
	require.Contains(t, r.dk.netsRemoved, adoptNet, "the emptied network is swept last")
}

func TestAdoptionUnmanagedServiceIsNeverSwept(t *testing.T) {
	r := adoptRig(t)
	require.NoError(t, r.eng.Reconcile(context.Background(), agentprotocol.Reconcile{Unmanaged: []string{adoptSvc}}))
	require.True(t, running(t, r.dk, adoptCtr))
	require.Equal(t, []agentprotocol.Route{adoptRoute()}, r.px.Current(), "the live route is kept")

	// Unmanaged does not hold the server: other unknown services are swept.
	stray := r.dk.seed("nc-stray-12345678-0", "5a5a5a5a-0000-4000-8000-0000000000bb", "d", true, time.Hour)
	require.NoError(t, r.eng.Reconcile(context.Background(), agentprotocol.Reconcile{Unmanaged: []string{adoptSvc}}))
	_, ok := r.dk.state(stray.Name)
	require.False(t, ok)
	require.True(t, running(t, r.dk, adoptCtr))

	// The host's own table wins on a host collision.
	other := agentprotocol.Route{ServiceID: "other", Hosts: []string{"old.example.com"}, Upstreams: []string{"nc-other-aaaaaaaa-0:80"}}
	require.NoError(t, r.eng.Reconcile(context.Background(), agentprotocol.Reconcile{
		Unmanaged: []string{adoptSvc},
		Services:  []agentprotocol.DesiredService{{ServiceID: "other", DeploymentID: "d", Replicas: 1, Routes: []agentprotocol.Route{other}}},
	}))
	require.Equal(t, []agentprotocol.Route{other}, r.px.Current())
	require.True(t, running(t, r.dk, adoptCtr))
}

// A daemon whose network listing, like Docker's, names only running containers.
type runningOnlyNets struct{ *fakeDocker }

func (d runningOnlyNets) NetworkList(ctx context.Context, prefix string, labels map[string]string) ([]docker.NetworkInfo, error) {
	nets, err := d.fakeDocker.NetworkList(ctx, prefix, labels)
	for i := range nets {
		var live []string
		for _, id := range nets[i].Containers {
			if c, ok := d.fakeDocker.state(id); ok && c.Running {
				live = append(live, id)
			}
		}
		nets[i].Containers = live
	}
	return nets, err
}

func TestAdoptionUnmanagedServiceKeepsTheNetworksOfItsStoppedContainers(t *testing.T) {
	dk := newFakeDocker()
	r := &rig{t: t, dk: dk, px: &fakeProxy{}, snd: &fakeSender{}, bld: &fakeBuilder{}}
	e, err := New(Config{
		Docker: runningOnlyNets{dk}, Proxy: r.px, Builder: r.bld, Sender: r.snd, Health: func(context.Context, health.Target) health.Result { return health.Result{Healthy: true} },
		Networks: hermeticPool(t, netpool.DefaultPool, netpool.DefaultPrefixLen),
	})
	require.NoError(t, err)
	r.eng = e

	c := dk.seed(adoptCtr, adoptSvc, "d", false, time.Hour) // stopped
	c.Networks = map[string]docker.ContainerNetwork{adoptNet: {}}
	addNet(r, adoptNet, false, time.Hour)
	addNet(r, "nebulactrl-env-99999999", false, time.Hour)

	e.applyAdoption(agentprotocol.Reconcile{Unmanaged: []string{adoptSvc}})
	require.NoError(t, e.sweepNetworks(context.Background()))
	require.Equal(t, []string{"nebulactrl-env-99999999"}, dk.netsRemoved, "only the network nobody keeps is swept")
	require.Contains(t, dk.nets, adoptNet)

	e.applyAdoption(agentprotocol.Reconcile{})
	require.NoError(t, e.sweepNetworks(context.Background()))
	require.Contains(t, dk.netsRemoved, adoptNet, "once managed no more it is an empty network like any other")
}

func TestManagedServiceIDsListsEveryManagedServiceOnce(t *testing.T) {
	r := newRig(t, nil)
	r.dk.seed("nc-b-11111111-0", "bbbbbbbb-0000-4000-8000-000000000002", "d", true, time.Hour)
	r.dk.seed("nc-b-11111111-1", "bbbbbbbb-0000-4000-8000-000000000002", "d", true, time.Hour)
	r.dk.seed("nc-a-22222222-0", "aaaaaaaa-0000-4000-8000-000000000001", "d", false, time.Hour) // stopped still counts
	proxy := r.dk.seed(agentprotocol.ProxyContainer, "", "", true, time.Hour)                   // infrastructure: no service
	proxy.Labels = map[string]string{agentprotocol.LabelManaged: "true"}
	foreign := r.dk.seed("redis", "", "", true, time.Hour)
	foreign.Labels = map[string]string{"app": "redis"}
	unmarked := r.dk.seed("nc-x-33333333-0", "cccccccc-0000-4000-8000-000000000003", "d", true, time.Hour)
	delete(unmarked.Labels, agentprotocol.LabelManaged)

	require.Equal(t, []string{"aaaaaaaa-0000-4000-8000-000000000001", "bbbbbbbb-0000-4000-8000-000000000002"},
		ManagedServiceIDs(context.Background(), r.dk))
	require.Empty(t, ManagedServiceIDs(context.Background(), newFakeDocker()))
}

// invDocker adds a volume listing to the fake daemon.
type invDocker struct {
	*fakeDocker
	vols []docker.VolumeInfo
}

func (d invDocker) VolumeList(context.Context) ([]docker.VolumeInfo, error) { return d.vols, nil }

func TestWorkloadInventoryReportsContainersVolumesAndRoutes(t *testing.T) {
	dk := newFakeDocker()
	px := &fakeProxy{}
	e, err := New(Config{
		Docker: invDocker{dk, []docker.VolumeInfo{{Name: "nc-vol-abcdef12-data"}, {Name: "unrelated"}}},
		Proxy:  px, Builder: &fakeBuilder{}, Sender: &fakeSender{}, Health: healthy,
		Networks: hermeticPool(t, netpool.DefaultPool, netpool.DefaultPrefixLen),
	})
	require.NoError(t, err)

	c := dk.seed(adoptCtr, adoptSvc, "d3d3d3d3-0000-4000-8000-000000000003", true, time.Hour)
	c.Image, c.ImageID = "nginx:1.27", "sha256:nginx"
	c.Env = []string{"PORT=80", "SECRET_KEY=hunter2", "PATH=/usr/bin"}
	c.Cmd = []string{"nginx", "-g", "daemon off;"}
	c.Health, c.RestartMode = "healthy", "unless-stopped"
	c.Networks = map[string]docker.ContainerNetwork{"b-net": {}, adoptNet: {}}
	c.Mounts = []docker.MountInfo{{Type: "volume", Name: "nc-vol-abcdef12-data", Source: "/var/lib/docker/volumes/x", Destination: "/data"}}
	dk.images["nginx:1.27"] = docker.ImageInfo{
		ID: "sha256:nginx", RepoTags: []string{"nginx:1.27"}, RepoDigests: []string{"nginx@sha256:abc"},
		Env: []string{"PATH=/usr/bin"}, Cmd: []string{"nginx", "-g", "daemon off;"},
	}
	infra := dk.seed(agentprotocol.ProxyContainer, "", "", true, time.Hour)
	infra.Labels = map[string]string{agentprotocol.LabelManaged: "true"}
	require.NoError(t, px.Apply([]agentprotocol.Route{adoptRoute()}))

	res := e.HandleCommand(context.Background(), agentprotocol.Command{ID: "inv-1", Kind: agentprotocol.KindWorkloadInventory, Payload: json.RawMessage(`{}`)})
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, res.Error)
	var inv agentprotocol.WorkloadInventory
	require.NoError(t, json.Unmarshal(res.Data, &inv))

	require.Len(t, inv.Containers, 1, "infrastructure is not an inventory entry")
	got := inv.Containers[0]
	require.Equal(t, adoptCtr, got.Name)
	require.Equal(t, "nginx:1.27", got.Image)
	require.Equal(t, "sha256:nginx", got.ImageID)
	require.True(t, got.Running)
	require.Equal(t, "healthy", got.Health)
	require.Equal(t, "unless-stopped", got.RestartMode)
	require.Equal(t, adoptSvc, got.Labels[agentprotocol.LabelService])
	require.Equal(t, c.Env, got.Env)
	require.Equal(t, c.Cmd, got.Cmd)
	require.Equal(t, []string{"b-net", adoptNet}, got.Networks)
	require.Equal(t, []agentprotocol.InventoryMount{{Type: "volume", Name: "nc-vol-abcdef12-data", Source: "/var/lib/docker/volumes/x", Destination: "/data"}}, got.Mounts)
	require.Equal(t, []string{"nginx:1.27"}, got.ImageRepoTags)
	require.Equal(t, []string{"nginx@sha256:abc"}, got.ImageRepoDigests)
	require.Equal(t, []string{"PATH=/usr/bin"}, got.ImageEnv)
	require.Equal(t, []string{"nginx", "-g", "daemon off;"}, got.ImageCmd)
	require.Equal(t, []string{"nc-vol-abcdef12-data"}, inv.Volumes, "only NebulaCtrl's volumes")
	require.Equal(t, []agentprotocol.Route{adoptRoute()}, inv.Routes)
	require.Contains(t, agentprotocol.EphemeralResultKinds, agentprotocol.KindWorkloadInventory)
}

func TestWorkloadInventoryOfAnEmptyHostIsEmptyLists(t *testing.T) {
	r := newRig(t, nil)
	res := r.eng.HandleCommand(context.Background(), agentprotocol.Command{ID: "inv-2", Kind: agentprotocol.KindWorkloadInventory, Payload: json.RawMessage(`{}`)})
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, res.Error)
	require.JSONEq(t, `{"containers":[],"volumes":[],"routes":[]}`, string(res.Data))
}

func l4Left(svc, proto string, port int, upstream string) agentprotocol.Route {
	return agentprotocol.Route{ServiceID: svc, Upstreams: []string{upstream}, RouteExt: agentprotocol.RouteExt{Protocol: proto, ListenPort: port}}
}

const adoptSvc2 = "5a5a5a5a-0000-4000-8000-0000000000bb"

// A preserved route never makes the new table fail to apply: it yields to a
// tcp/udp listen port the table already uses and to a route the proxy's
// validation would reject.
func TestAdoptionKeptRoutesNeverBreakTheNewTable(t *testing.T) {
	r := adoptRig(t)
	db := l4Left(adoptSvc2, "tcp", 5432, "nc-db-abcdef12-0:5432")
	dns := l4Left("5a5a5a5a-0000-4000-8000-0000000000cc", "udp", 5353, "nc-dns-abcdef12-0:53")
	dnsTCP := l4Left("5a5a5a5a-0000-4000-8000-0000000000cc", "tcp", 5353, "nc-dns-abcdef12-0:53")
	loopback := agentprotocol.Route{
		ServiceID: "5a5a5a5a-0000-4000-8000-0000000000dd", Hosts: []string{"nas.example.com"}, Upstreams: []string{"127.0.0.1:8080"},
		RouteExt: agentprotocol.RouteExt{Kind: agentprotocol.RouteKindExternal},
	}
	require.NoError(t, r.px.Apply([]agentprotocol.Route{adoptRoute(), db, dns, dnsTCP, loopback}))
	r.px.applies = nil

	// The new table exposes tcp 5432 itself.
	mine := l4Left("mine", "tcp", 5432, "nc-mine-aaaaaaaa-0:5432")
	require.NoError(t, r.eng.Reconcile(context.Background(), agentprotocol.Reconcile{
		KeepUnknown: true,
		Services:    []agentprotocol.DesiredService{{ServiceID: "mine", DeploymentID: "d", Replicas: 1, Routes: []agentprotocol.Route{mine}}},
	}))
	byID := map[string][]agentprotocol.Route{}
	for _, rt := range r.px.Current() {
		byID[rt.ServiceID] = append(byID[rt.ServiceID], rt)
	}
	require.Contains(t, byID, "mine")
	require.NotContains(t, byID, adoptSvc2, "a kept route yields to a listen port the table uses")
	require.Contains(t, byID, adoptSvc, "an http route without a clash stays")
	require.Len(t, byID["5a5a5a5a-0000-4000-8000-0000000000cc"], 2, "tcp and udp of one service are both kept")
	require.NotContains(t, byID, "5a5a5a5a-0000-4000-8000-0000000000dd", "an upstream the policy forbids is not kept")
	require.NoError(t, proxyValidate(r.px.Current()), "the merged table passes the proxy's validation")
}

func proxyValidate(routes []agentprotocol.Route) error {
	return proxy.ValidateRoutes(routes, agentprotocol.DefaultUpstreamPolicy())
}
