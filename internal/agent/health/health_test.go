package health

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

func init() { unit = 20 * time.Millisecond }

func runningInfo() (docker.ContainerInfo, error) {
	return docker.ContainerInfo{Running: true, State: "running"}, nil
}

func inspectRunning(context.Context) (docker.ContainerInfo, error) { return runningInfo() }

func serverPort(t *testing.T, srv *httptest.Server) int {
	t.Helper()
	_, p, err := net.SplitHostPort(srv.Listener.Addr().String())
	require.NoError(t, err)
	n, err := strconv.Atoi(p)
	require.NoError(t, err)
	return n
}

func TestHTTPHealthyAfterConsecutiveSuccesses(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Path != "/up" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusNoContent) // 2xx
	}))
	defer srv.Close()

	res := Wait(context.Background(), Target{
		Spec:    agentprotocol.HealthcheckSpec{Type: TypeHTTP, Path: "/up", IntervalSeconds: 1, TimeoutSeconds: 3, DeadlineSeconds: 50, SuccessThreshold: 3},
		Addrs:   []string{"127.0.0.1"},
		Inspect: inspectRunning, DefaultPort: serverPort(t, srv),
	})
	require.True(t, res.Healthy, res.Reason)
	require.Equal(t, "127.0.0.1", res.Addr)
	require.GreaterOrEqual(t, int(hits.Load()), 3)
}

func TestHTTPRedirectCountsAsPassAndServerErrorDoesNot(t *testing.T) {
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/login", http.StatusFound)
	}))
	defer redirect.Close()
	res := Wait(context.Background(), Target{
		Spec:    agentprotocol.HealthcheckSpec{Type: TypeHTTP, IntervalSeconds: 1, DeadlineSeconds: 50, SuccessThreshold: 1},
		Addrs:   []string{"127.0.0.1"},
		Inspect: inspectRunning, DefaultPort: serverPort(t, redirect),
	})
	require.True(t, res.Healthy, res.Reason)

	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) }))
	defer broken.Close()
	res = Wait(context.Background(), Target{
		Spec:    agentprotocol.HealthcheckSpec{Type: TypeHTTP, IntervalSeconds: 1, DeadlineSeconds: 5, SuccessThreshold: 1},
		Addrs:   []string{"127.0.0.1"},
		Inspect: inspectRunning, DefaultPort: serverPort(t, broken),
	})
	require.False(t, res.Healthy)
	require.Contains(t, res.Reason, "did not pass")
	require.Contains(t, res.Reason, "HTTP 500")
}

func TestFallsBackToNextAddressWhenIPUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	res := Wait(context.Background(), Target{
		Spec:    agentprotocol.HealthcheckSpec{Type: TypeHTTP, IntervalSeconds: 1, DeadlineSeconds: 50, SuccessThreshold: 2},
		Addrs:   []string{"unresolvable.invalid", "127.0.0.1"},
		Inspect: inspectRunning, DefaultPort: serverPort(t, srv),
	})
	require.True(t, res.Healthy, res.Reason)
	require.Equal(t, "127.0.0.1", res.Addr)
}

func TestTCPCheck(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer l.Close()
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	_, p, _ := net.SplitHostPort(l.Addr().String())
	port, _ := strconv.Atoi(p)

	res := Wait(context.Background(), Target{
		Spec:  agentprotocol.HealthcheckSpec{Type: TypeTCP, Port: port, IntervalSeconds: 1, DeadlineSeconds: 50, SuccessThreshold: 2},
		Addrs: []string{"127.0.0.1"}, Inspect: inspectRunning,
	})
	require.True(t, res.Healthy, res.Reason)
}

func TestExitedContainerIsUnhealthyImmediately(t *testing.T) {
	res := Wait(context.Background(), Target{
		Spec: agentprotocol.HealthcheckSpec{Type: TypeHTTP, DeadlineSeconds: 1000}, DefaultPort: 1,
		Inspect: func(context.Context) (docker.ContainerInfo, error) {
			return docker.ContainerInfo{Running: false, State: "exited", ExitCode: 137, OOMKilled: true}, nil
		},
	})
	require.False(t, res.Healthy)
	require.Contains(t, res.Reason, "exited with code 137")
	require.Contains(t, res.Reason, "out of memory")
}

func TestRestartIsUnhealthy(t *testing.T) {
	var n atomic.Int32
	res := Wait(context.Background(), Target{
		Spec: agentprotocol.HealthcheckSpec{Type: TypeNone, DeadlineSeconds: 1000},
		Inspect: func(context.Context) (docker.ContainerInfo, error) {
			return docker.ContainerInfo{Running: true, RestartCount: int(n.Add(1)) - 1}, nil
		},
	})
	require.False(t, res.Healthy)
	require.Contains(t, res.Reason, "restarted")
}

func TestNoneIsHealthyAfterRunningTenUnitsWithWarning(t *testing.T) {
	start := time.Now()
	res := Wait(context.Background(), Target{
		Spec:    agentprotocol.HealthcheckSpec{Type: TypeNone, IntervalSeconds: 1, DeadlineSeconds: 1000},
		Inspect: inspectRunning,
	})
	require.True(t, res.Healthy)
	require.GreaterOrEqual(t, time.Since(start), 10*unit)
	require.Contains(t, res.Warning, "No health check configured")
}

func TestDockerTypeUsesImageHealthStatus(t *testing.T) {
	var n atomic.Int32
	res := Wait(context.Background(), Target{
		Spec: agentprotocol.HealthcheckSpec{Type: TypeDocker, IntervalSeconds: 1, DeadlineSeconds: 50, SuccessThreshold: 1},
		Inspect: func(context.Context) (docker.ContainerInfo, error) {
			status := "starting"
			if n.Add(1) > 2 {
				status = "healthy"
			}
			return docker.ContainerInfo{Running: true, Health: status}, nil
		},
	})
	require.True(t, res.Healthy, res.Reason)

	res = Wait(context.Background(), Target{
		Spec: agentprotocol.HealthcheckSpec{Type: TypeDocker, IntervalSeconds: 1, DeadlineSeconds: 5, SuccessThreshold: 1},
		Inspect: func(context.Context) (docker.ContainerInfo, error) {
			return docker.ContainerInfo{Running: true, Health: "unhealthy"}, nil
		},
	})
	require.False(t, res.Healthy)
	require.Contains(t, res.Reason, "docker health status is unhealthy")
}

func TestContextCancelStopsWait(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res := Wait(ctx, Target{
		Spec:    agentprotocol.HealthcheckSpec{Type: TypeNone, DeadlineSeconds: 1000},
		Inspect: func(c context.Context) (docker.ContainerInfo, error) { return runningInfo() },
	})
	require.False(t, res.Healthy)
}

func TestOneShotIsHealthyOnlyAfterACleanExit(t *testing.T) {
	var n atomic.Int32
	res := Wait(context.Background(), Target{
		Spec:    agentprotocol.HealthcheckSpec{Type: TypeNone, IntervalSeconds: 1, DeadlineSeconds: 1000},
		OneShot: true,
		Inspect: func(context.Context) (docker.ContainerInfo, error) {
			// Still running well past the 10-unit "none" threshold, then exits 0.
			if n.Add(1) < 40 {
				return runningInfo()
			}
			return docker.ContainerInfo{State: "exited", ExitCode: 0}, nil
		},
	})
	require.True(t, res.Healthy, res.Reason)
	require.GreaterOrEqual(t, n.Load(), int32(40), "a running one-shot is not healthy after 10 units")

	res = Wait(context.Background(), Target{
		Spec:    agentprotocol.HealthcheckSpec{Type: TypeNone, DeadlineSeconds: 1000},
		OneShot: true,
		Inspect: func(context.Context) (docker.ContainerInfo, error) {
			return docker.ContainerInfo{State: "exited", ExitCode: 3}, nil
		},
	})
	require.False(t, res.Healthy)
	require.Contains(t, res.Reason, "exited with code 3")
}

func TestExecTypeIsJudgedByTheContainerHealthStatus(t *testing.T) {
	var n atomic.Int32
	res := Wait(context.Background(), Target{
		Spec: agentprotocol.HealthcheckSpec{Type: TypeExec, Command: []string{"CMD", "true"}, IntervalSeconds: 1, DeadlineSeconds: 50, SuccessThreshold: 1},
		Inspect: func(context.Context) (docker.ContainerInfo, error) {
			h := "starting"
			if n.Add(1) > 2 {
				h = "healthy"
			}
			return docker.ContainerInfo{Running: true, State: "running", Health: h}, nil
		},
	})
	require.True(t, res.Healthy, res.Reason)
}

func TestTraceReportsEveryProbeWithStatusAndLatency(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable) // still starting
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	var lines []string
	res := Wait(context.Background(), Target{
		Spec:    agentprotocol.HealthcheckSpec{Type: TypeHTTP, Path: "/up", IntervalSeconds: 1, DeadlineSeconds: 50, SuccessThreshold: 2},
		Addrs:   []string{"127.0.0.1"},
		Inspect: inspectRunning, DefaultPort: serverPort(t, srv),
		Trace: func(l string) { lines = append(lines, l) },
	})
	require.True(t, res.Healthy, res.Reason)

	text := strings.Join(lines, "\n")
	require.Contains(t, text, "check http port=")
	require.Contains(t, text, "/up: HTTP 503 in ")
	require.Contains(t, text, "(fail: expected 200-399)")
	require.Contains(t, text, "/up: HTTP 200 in ")
	require.Contains(t, text, "(pass)")
	require.Contains(t, text, "pass 2/2 consecutive")
}

func TestTraceReportsAConnectionFailure(t *testing.T) {
	var lines []string
	res := Wait(context.Background(), Target{
		Spec:    agentprotocol.HealthcheckSpec{Type: TypeTCP, IntervalSeconds: 1, TimeoutSeconds: 1, DeadlineSeconds: 3, SuccessThreshold: 1},
		Addrs:   []string{"127.0.0.1"},
		Inspect: inspectRunning, DefaultPort: 1, // nothing listens on port 1
		Trace: func(l string) { lines = append(lines, l) },
	})
	require.False(t, res.Healthy)
	require.Contains(t, strings.Join(lines, "\n"), "probe tcp 127.0.0.1:1: failed after ")
}
