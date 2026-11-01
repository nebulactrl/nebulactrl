//go:build integration

package integration

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// liveV1 deploys the fixture app as v1 behind the test's own hostname (s.Host())
// and waits until it serves.
func liveV1(t *testing.T, s *Stack, health map[string]any) (Service, *ProxyClient) {
	t.Helper()
	app := appFixture(t)
	svc := s.NewService(SvcSpec{
		Name: "web", Image: app.AppImage, Domain: s.Host(), Health: health,
		Vars: map[string]string{"VERSION": "v1"},
	})
	s.DeployOK(svc.ID)
	px := NewProxyClient(s.Host())
	px.WaitBody(t, "/", "v1", 30*time.Second)
	return svc, px
}

// 4. zero-downtime rolling deploy: 50 req/s through Traefik while v2 replaces v1.
func TestZeroDowntimeRolling(t *testing.T) {
	requireTier(t, tierSmoke)
	t.Parallel()
	s := SharedStack(t)
	svc, px := liveV1(t, s, nil)

	load := StartLoad(px, "/", 20*time.Millisecond)
	load.WaitMore(t, 60, 30*time.Second) // v1-only baseline

	s.SetVars(svc.ID, map[string]string{"VERSION": "v2"}, nil)
	d := s.WaitDeployment(s.Deploy(svc.ID), 3*time.Minute)
	require.Equal(t, "succeeded", d.Str("status"), "%v\n%s", d, s.DeploymentLogs(d.Str("id")))
	require.Equal(t, "rolling", d.Str("strategy"))
	px.WaitBody(t, "/", "v2", 10*time.Second)
	load.WaitMore(t, 60, 30*time.Second) // v2-only tail
	res := load.Stop()

	t.Logf("requests=%d non2xx=%d errors=%d", res.Total, res.Non2xx, res.Errors)
	require.Greater(t, res.Total, 100, "load generator barely ran")
	require.Zero(t, res.Non2xx, "non-2xx during rolling deploy: %v", res.Failures)
	require.Zero(t, res.Errors, "connection errors during rolling deploy: %v", res.Failures)

	require.GreaterOrEqual(t, indexOf(res.Bodies, "v1"), 0, "never saw v1")
	require.GreaterOrEqual(t, indexOf(res.Bodies, "v2"), 0, "never saw v2")

	// Once v2 has answered, the proxy must not send a new request to v1. The
	// requests are concurrent (one connection each), so tick order is not
	// arrival order: a later tick whose TLS handshake is quick overtakes an
	// earlier one that is still handshaking. The ordering that is guaranteed is
	// wall time: a request written after v2's first answer arrived after the
	// switch.
	switched, stale := StaleAfterSwitch(res.Answers, "v1", "v2")
	if first, last := indexOf(res.Bodies, "v2"), lastIndexOf(res.Bodies, "v1"); last > first {
		t.Logf("tick order interleaves (last v1 #%d, first v2 #%d), which is fine when those v1 requests reached the proxy before the switch", last, first)
	}
	require.Empty(t, stale, "v1 answered %d request(s) written after v2 took over (switch seen at %s)", len(stale), switched.Format("15:04:05.000"))
	for _, b := range res.Bodies {
		require.Contains(t, []string{"v1", "v2"}, b)
	}

	// The old container is retired.
	require.True(t, eventually(30*time.Second, time.Second, func() bool { return len(s.RunningContainers(svc.ID)) == 1 }),
		"old container still running: %d", len(s.RunningContainers(svc.ID)))
}

func indexOf(xs []string, v string) int {
	for i, x := range xs {
		if x == v {
			return i
		}
	}
	return -1
}

func lastIndexOf(xs []string, v string) int {
	for i := len(xs) - 1; i >= 0; i-- {
		if xs[i] == v {
			return i
		}
	}
	return -1
}

// 5. failed health: the new container never gets healthy; v1 keeps serving.
func TestFailedHealthKeepsOldServing(t *testing.T) {
	requireTier(t, tierSmoke)
	t.Parallel()
	s := SharedStack(t)
	svc, px := liveV1(t, s, map[string]any{"deadlineSeconds": 6})
	before := s.RunningContainers(svc.ID)
	require.Len(t, before, 1)

	load := StartLoad(px, "/", 20*time.Millisecond)
	s.SetVars(svc.ID, map[string]string{"VERSION": "v2", "FAIL_HEALTH": "1"}, nil)
	d := s.WaitDeployment(s.Deploy(svc.ID), 3*time.Minute)
	load.WaitMore(t, 15, 30*time.Second)
	res := load.Stop()

	require.Equal(t, "failed", d.Str("status"), "%v", d)
	require.Contains(t, d.Str("statusMessage"), "Previous release is still serving", "%v", d)
	logs := s.DeploymentLogs(d.Str("id"))
	require.Contains(t, logs, "Health check failed", logs)
	require.Contains(t, logs, "last 1 container log lines", "the failure carries the container's last log lines:\n"+logs)

	require.Zero(t, res.Non2xx, "failures while the bad release was rejected: %v", res.Failures)
	require.Zero(t, res.Errors, "%v", res.Failures)
	for _, b := range res.Bodies {
		require.Equal(t, "v1", b, "a request reached the unhealthy release")
	}
	after := s.RunningContainers(svc.ID)
	require.Len(t, after, 1)
	require.Equal(t, before[0].ID, after[0].ID, "v1 container must be untouched")
	require.Len(t, s.Containers(svc.ID), 1, "the failed container must be removed")
	require.Equal(t, s.Service(svc.ID).Str("currentDeploymentId"), before[0].Labels[agentprotocol.LabelDeployment])
}

// 6. verify failure: the route probe fails after the new route went live; the
// previous routes are restored, the new container removed, outcome rolled_back.
func TestVerifyFailureRollsBack(t *testing.T) {
	requireTier(t, tierFull)
	t.Parallel()
	s := SharedStack(t)
	svc, px := liveV1(t, s, nil)
	before := s.RunningContainers(svc.ID)
	require.Len(t, before, 1)

	// Inject the failure only after v1 is live (its own verify must pass).
	s.Fault.Fail(px.host, http.StatusBadGateway)
	t.Cleanup(func() { s.Fault.Clear(px.host) })
	s.SetVars(svc.ID, map[string]string{"VERSION": "v2"}, nil)
	d := s.WaitDeployment(s.Deploy(svc.ID), 3*time.Minute)
	s.Fault.Clear(px.host)

	require.Equal(t, "rolled_back", d.Str("status"), "%v\n%s", d, s.DeploymentLogs(d.Str("id")))
	logs := s.DeploymentLogs(d.Str("id"))
	require.True(t, strings.Contains(strings.ToLower(logs), "verif"), logs)

	px.WaitBody(t, "/", "v1", 20*time.Second)
	after := s.RunningContainers(svc.ID)
	require.Len(t, after, 1)
	require.Equal(t, before[0].ID, after[0].ID)
	require.Len(t, s.Containers(svc.ID), 1, "the rejected container must be removed")
	require.Equal(t, before[0].Labels[agentprotocol.LabelDeployment], s.Service(svc.ID).Str("currentDeploymentId"),
		"a rolled-back deployment must not become current")
}

// 7. rollback: deploy v2 over v1, then roll back to v1's release.
func TestRollback(t *testing.T) {
	requireTier(t, tierSmoke)
	t.Parallel()
	s := SharedStack(t)
	svc, px := liveV1(t, s, nil)
	v1 := s.Service(svc.ID).Str("currentDeploymentId")

	s.SetVars(svc.ID, map[string]string{"VERSION": "v2"}, nil)
	v2 := s.DeployOK(svc.ID).Str("id")
	px.WaitBody(t, "/", "v2", 20*time.Second)

	r := s.Do("POST", "/deployments/"+v1+"/rollback", nil)
	require.Equal(t, http.StatusAccepted, r.Status, string(r.Raw))
	rb := r.JSON()
	require.Contains(t, rb.Str("disclaimer"), "not rolled back")
	require.Equal(t, "rollback", rb.Str("trigger"))
	d := s.WaitDeployment(rb.Str("id"), 3*time.Minute)
	require.Equal(t, "succeeded", d.Str("status"), "%v\n%s", d, s.DeploymentLogs(d.Str("id")))
	require.Equal(t, v2, d.Str("rolledBackFrom"), "rolledBackFrom is the deployment that was running")
	require.NotEqual(t, v1, d.Str("id"), "a rollback is a new deployment of the old release")
	require.Equal(t, d.Obj("release").Str("id"), s.Deployment(v1).Str("releaseId"), "the rollback reuses v1's release")

	px.WaitBody(t, "/", "v1", 20*time.Second)
	require.Equal(t, d.Str("id"), s.Service(svc.ID).Str("currentDeploymentId"))
}
