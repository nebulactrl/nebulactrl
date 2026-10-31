//go:build integration

package integration

import (
	"fmt"
	"net/http"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// SetReplicas sets the manual replica count (PUT /services/{id}/scaling). On a
// deployed service it enqueues a scale_service command; on an undeployed one it
// only stores the setting.
func (s *Stack) SetReplicas(serviceID string, n int) J {
	s.t.Helper()
	r := s.Do("PUT", "/services/"+serviceID+"/scaling", map[string]any{"replicas": n})
	require.Equal(s.t, http.StatusOK, r.Status, string(r.Raw))
	return r.JSON()
}

// waitRunning waits until exactly n containers of the service run and (when
// the service has a scaling view) the control plane reports n replicas ready.
func (s *Stack) waitRunning(t *testing.T, serviceID string, n int, timeout time.Duration) {
	t.Helper()
	ok := eventually(timeout, time.Second, func() bool {
		return len(s.RunningContainers(serviceID)) == n && s.Service(serviceID).Obj("scaling").Int("ready") == n
	})
	require.True(t, ok, "want %d running replicas, got %d containers; scaling: %v",
		n, len(s.RunningContainers(serviceID)), s.Service(serviceID).Obj("scaling"))
}

// replicaContainers maps replica index → container ID of the running containers.
func (s *Stack) replicaContainers(serviceID string) map[string]string {
	out := map[string]string{}
	for _, c := range s.RunningContainers(serviceID) {
		out[c.Labels[agentprotocol.LabelReplica]] = c.ID
	}
	return out
}

// distinctHosts requests /hostname n times and returns the hostnames seen.
func distinctHosts(t *testing.T, px *ProxyClient, n int) map[string]int {
	t.Helper()
	seen := map[string]int{}
	for range n {
		code, body, err := px.Get("/hostname")
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, code, body)
		seen[body]++
	}
	return seen
}

// TestReplicasLoadBalance: replicas=3 starts three labeled containers behind one
// route, Traefik spreads requests over them, and a rolling v1→v2 deploy under
// load drops no request.
func TestReplicasLoadBalance(t *testing.T) {
	requireTier(t, tierFull)
	t.Parallel()
	app := appFixture(t)
	s := SharedStack(t)
	svc := s.NewService(SvcSpec{Name: "lb", Image: app.AppImage, Vars: map[string]string{"VERSION": "v1"}, Domain: s.Host()})
	sv := s.SetReplicas(svc.ID, 3)
	require.Equal(t, 3, sv.Obj("scaling").Int("replicas"))

	d := s.DeployOK(svc.ID)
	require.Equal(t, "rolling", d.Str("strategy"))

	cs := s.RunningContainers(svc.ID)
	require.Len(t, cs, 3, "one container per replica")
	want := agentprotocol.ContainerNames(svc.Slug, d.Str("id"), 3)
	var got []string
	for _, c := range cs {
		got = append(got, c.Name)
		require.Equal(t, d.Str("id"), c.Labels[agentprotocol.LabelDeployment])
	}
	sort.Strings(got)
	require.Equal(t, want, got)
	require.Len(t, s.replicaContainers(svc.ID), 3, "replica labels 0,1,2")

	px := NewProxyClient(s.Host())
	px.WaitBody(t, "/", "v1", 30*time.Second)
	seen := distinctHosts(t, px, 60)
	t.Logf("hostnames seen through Traefik: %v", seen)
	require.GreaterOrEqual(t, len(seen), 2, "Traefik should balance over several replicas: %v", seen)

	s.waitRunning(t, svc.ID, 3, 90*time.Second)
	o := s.Service(svc.ID).Obj("observed")
	require.Equal(t, agentprotocol.StateRunning, o.Str("state"), "%v", o)
	require.Equal(t, agentprotocol.HealthHealthy, o.Str("health"), "%v", o)

	// Rolling v1 → v2 under load: no failed request, v1 hands over to v2.
	load := StartLoad(px, "/", 20*time.Millisecond)
	load.WaitMore(t, 60, 30*time.Second)
	s.SetVars(svc.ID, map[string]string{"VERSION": "v2"}, nil)
	d2 := s.WaitDeployment(s.Deploy(svc.ID), 4*time.Minute)
	require.Equal(t, "succeeded", d2.Str("status"), "%v\n%s", d2, s.DeploymentLogs(d2.Str("id")))
	px.WaitBody(t, "/", "v2", 15*time.Second)
	load.WaitMore(t, 60, 30*time.Second)
	res := load.Stop()

	t.Logf("requests=%d non2xx=%d errors=%d %s", res.Total, res.Non2xx, res.Errors, res.Summary())
	require.Greater(t, res.Total, 100, "load generator barely ran")
	require.Zero(t, res.Non2xx, "non-2xx during the replica rollout: %s", res.Summary())
	require.Zero(t, res.Errors, "connection errors during the replica rollout: %s", res.Summary())
	firstV2 := indexOf(res.Bodies, "v2")
	require.GreaterOrEqual(t, indexOf(res.Bodies, "v1"), 0, "never saw v1")
	require.GreaterOrEqual(t, firstV2, 0, "never saw v2")
	require.Less(t, lastIndexOf(res.Bodies, "v1"), firstV2+1, "v1 answered after v2 took over")

	// The old set is retired; the new one keeps all three replicas.
	require.True(t, eventually(60*time.Second, time.Second, func() bool {
		cs := s.RunningContainers(svc.ID)
		if len(cs) != 3 {
			return false
		}
		for _, c := range cs {
			if c.Labels[agentprotocol.LabelDeployment] != d2.Str("id") {
				return false
			}
		}
		return true
	}), "old replicas still running: %d", len(s.RunningContainers(svc.ID)))
}

// TestScaleService: 1 → 3 → 1 on a live service through scale_service. Existing
// replicas are never restarted or redeployed, and no request fails meanwhile.
func TestScaleService(t *testing.T) {
	requireTier(t, tierFull)
	t.Parallel()
	app := appFixture(t)
	s := SharedStack(t)
	svc := s.NewService(SvcSpec{Name: "scale", Image: app.AppImage, Vars: map[string]string{"VERSION": "v1"}, Domain: s.Host()})
	d := s.DeployOK(svc.ID)
	px := NewProxyClient(s.Host())
	px.WaitBody(t, "/", "v1", 30*time.Second)

	first := s.replicaContainers(svc.ID)
	require.Len(t, first, 1)
	replica0 := first["0"]
	require.NotEmpty(t, replica0)

	load := StartLoad(px, "/hostname", 20*time.Millisecond)
	load.WaitMore(t, 50, 30*time.Second)

	// 1 → 3
	s.SetReplicas(svc.ID, 3)
	s.waitRunning(t, svc.ID, 3, 2*time.Minute)
	reps := s.replicaContainers(svc.ID)
	require.Len(t, reps, 3)
	require.Equal(t, replica0, reps["0"], "replica 0 is the original container: not restarted or replaced")
	for _, c := range s.RunningContainers(svc.ID) {
		require.Equal(t, d.Str("id"), c.Labels[agentprotocol.LabelDeployment], "replicas belong to the current deployment")
	}
	load.WaitMore(t, 75, 30*time.Second)
	seen := distinctHosts(t, px, 60)
	t.Logf("hostnames after scale-up: %v", seen)
	require.GreaterOrEqual(t, len(seen), 2, "new replicas receive traffic: %v", seen)

	// 3 → 1
	s.SetReplicas(svc.ID, 1)
	s.waitRunning(t, svc.ID, 1, 2*time.Minute)
	after := s.replicaContainers(svc.ID)
	require.Equal(t, map[string]string{"0": replica0}, after, "scale-down keeps the lowest replica, untouched")
	load.WaitMore(t, 50, 30*time.Second)
	res := load.Stop()

	t.Logf("requests=%d non2xx=%d errors=%d %s", res.Total, res.Non2xx, res.Errors, res.Summary())
	require.Greater(t, res.Total, 100, "load generator barely ran")
	require.Zero(t, res.Non2xx, "non-2xx while scaling: %s", res.Summary())
	require.Zero(t, res.Errors, "connection errors while scaling: %s", res.Summary())
	hosts := map[string]bool{}
	for _, b := range res.Bodies {
		hosts[b] = true
	}
	require.GreaterOrEqual(t, len(hosts), 2, "load saw the added replicas")

	// Scaling never creates a deployment.
	r := s.Do("GET", "/services/"+svc.ID+"/deployments", nil)
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	require.Len(t, r.JSON().Items(), 1, "scaling reuses the current release")
	require.Equal(t, 1, s.Service(svc.ID).Obj("scaling").Int("replicas"))
}

// TestRoutePushesDoNotRevertADeployOrScale: route tables the control plane
// sends for other reasons (here: maintenance toggles of a second service on the
// same server) are computed from its database, which still names the previous
// deployment or replica count until the agent's result arrives. They must not
// put retired containers back into the upstreams of a service that is rolling or
// scaling (a sustained 502: the retired replicas were stopped right after), and
// must not drop the route of a service whose first deployment was just routed
// (404 pages, then 502 once the proxy left its network). Both happened in
// TestReplicasLoadBalance and TestScaleService whenever another test pushed
// routes at the same time.
func TestRoutePushesDoNotRevertADeployOrScale(t *testing.T) {
	requireTier(t, tierFull)
	t.Parallel()
	app := appFixture(t)
	s := SharedStack(t)
	a := s.NewService(SvcSpec{Name: "pusha", Image: app.AppImage, Vars: map[string]string{"VERSION": "v1"}, Domain: s.Host()})
	s.SetReplicas(a.ID, 3)
	s.DeployOK(a.ID)
	b := s.NewService(SvcSpec{Name: "pushb", Image: app.AppImage, Vars: map[string]string{"VERSION": "b"}, Domain: s.Host("b")})
	s.DeployOK(b.ID)
	px := NewProxyClient(s.Host())
	px.WaitBody(t, "/", "v1", 30*time.Second)
	s.waitRunning(t, a.ID, 3, 90*time.Second)

	stop := make(chan struct{})
	var churn sync.WaitGroup
	churn.Add(1)
	go func() {
		defer churn.Done()
		for i := 0; ; i++ {
			// Every call queues a sync_routes with the server's whole table.
			r := s.Do("PUT", "/services/"+b.ID+"/maintenance", map[string]any{"enabled": i%2 == 0, "title": "churn"})
			if r.Status != http.StatusOK {
				t.Errorf("maintenance toggle: %d %s", r.Status, r.Raw)
				return
			}
			select {
			case <-stop:
				return
			case <-time.After(500 * time.Millisecond):
			}
		}
	}()

	load := StartLoad(px, "/", 20*time.Millisecond)
	load.WaitMore(t, 60, 30*time.Second)
	s.SetVars(a.ID, map[string]string{"VERSION": "v2"}, nil)
	d2 := s.WaitDeployment(s.Deploy(a.ID), 4*time.Minute)
	require.Equal(t, "succeeded", d2.Str("status"), "%v\n%s", d2, s.DeploymentLogs(d2.Str("id")))
	px.WaitBody(t, "/", "v2", 15*time.Second)
	load.WaitMore(t, 60, 30*time.Second)

	s.SetReplicas(a.ID, 1)
	s.waitRunning(t, a.ID, 1, 2*time.Minute)
	load.WaitMore(t, 60, 30*time.Second)
	s.SetReplicas(a.ID, 3)
	s.waitRunning(t, a.ID, 3, 2*time.Minute)
	load.WaitMore(t, 60, 30*time.Second)

	// A service whose first deployment has just been routed is absent from any
	// table queued before the control plane recorded that deployment: its route
	// must survive the pushes that follow right away.
	for i := range 2 {
		host := s.Host(fmt.Sprintf("c%d", i))
		c := s.NewService(SvcSpec{Name: fmt.Sprintf("pushc%d", i), Image: app.AppImage, Vars: map[string]string{"VERSION": "c"}, Domain: host})
		s.DeployOK(c.ID)
		pc := NewProxyClient(host)
		pc.WaitBody(t, "/", "c", 30*time.Second)
		lc := StartLoad(pc, "/", 20*time.Millisecond)
		lc.WaitMore(t, 150, 30*time.Second)
		rc := lc.Stop()
		require.Zero(t, rc.Non2xx+rc.Errors, "a new service lost its route to a push queued before its deployment was recorded: %s", rc.Summary())
	}

	res := load.Stop()
	close(stop)
	churn.Wait()
	t.Logf("requests=%d non2xx=%d errors=%d %s", res.Total, res.Non2xx, res.Errors, res.Summary())
	require.Greater(t, res.Total, 100, "load generator barely ran")
	require.Zero(t, res.Non2xx, "non-2xx while route tables were pushed during a deploy and scaling: %s", res.Summary())
	require.Zero(t, res.Errors, "connection errors while route tables were pushed during a deploy and scaling: %s", res.Summary())
	require.Less(t, lastIndexOf(res.Bodies, "v1"), indexOf(res.Bodies, "v2")+1, "v1 answered after v2 took over")
}
