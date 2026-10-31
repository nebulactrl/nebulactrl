package health

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// probeSpec is a check of typ whose 50 × unit timeout (1 s here: the package
// shrinks unit to 20 ms) is plenty for loopback probes.
func probeSpec(typ string, path string) agentprotocol.HealthcheckSpec {
	return agentprotocol.HealthcheckSpec{Type: typ, Path: path, TimeoutSeconds: 50}
}

func TestProbeOnceHTTPPassesOn2xxAnd3xx(t *testing.T) {
	for name, status := range map[string]int{"ok": 200, "no content": 204, "redirect": 302} {
		t.Run(name, func(t *testing.T) {
			var gotPath atomic.Value
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotPath.Store(r.URL.Path)
				if status == 302 {
					http.Redirect(w, r, "/login", status)
					return
				}
				w.WriteHeader(status)
			}))
			defer srv.Close()

			ok, took, err := ProbeOnce(context.Background(), probeSpec(TypeHTTP, "/up"), serverPort(t, srv), []string{"127.0.0.1"})
			require.NoError(t, err)
			require.True(t, ok)
			require.GreaterOrEqual(t, took, time.Duration(0))
			require.Less(t, took, 5*time.Second)
			require.Equal(t, "/up", gotPath.Load(), "a redirect is a pass and is not followed")
		})
	}
}

func TestProbeOnceHTTPFailsOnErrorStatusWithoutTryingFallbacks(t *testing.T) {
	var hits atomic.Int32
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(503)
	}))
	defer bad.Close()

	// The first address answers with a server error: the probe fails there.
	ok, took, err := ProbeOnce(context.Background(), probeSpec(TypeHTTP, ""), serverPort(t, bad), []string{"127.0.0.1", "localhost"})
	require.False(t, ok)
	require.Zero(t, took)
	require.ErrorContains(t, err, "HTTP 503")
	require.EqualValues(t, 1, hits.Load())
}

func TestProbeOnceFallsBackToTheNextAddress(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()

	ok, _, err := ProbeOnce(context.Background(), probeSpec(TypeHTTP, "/"), serverPort(t, srv), []string{"unresolvable.invalid", "127.0.0.1"})
	require.NoError(t, err)
	require.True(t, ok, "the container IP failed, the next candidate answered")
}

func TestProbeOnceReportsEveryFailedAddress(t *testing.T) {
	ok, _, err := ProbeOnce(context.Background(), probeSpec(TypeTCP, ""), refusedPort(t), []string{"127.0.0.1", "unresolvable.invalid"})
	require.False(t, ok)
	require.ErrorContains(t, err, "refused")
	require.ErrorContains(t, err, "unresolvable.invalid")
}

func TestProbeOnceTCP(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer l.Close()
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	port := l.Addr().(*net.TCPAddr).Port

	ok, took, err := ProbeOnce(context.Background(), probeSpec(TypeTCP, ""), port, []string{"127.0.0.1"})
	require.NoError(t, err)
	require.True(t, ok)
	require.GreaterOrEqual(t, took, time.Duration(0))

	// The spec's own port wins over the default.
	spec := probeSpec(TypeTCP, "")
	spec.Port = port
	ok, _, err = ProbeOnce(context.Background(), spec, refusedPort(t), []string{"127.0.0.1"})
	require.NoError(t, err)
	require.True(t, ok)

	ok, _, err = ProbeOnce(context.Background(), probeSpec(TypeTCP, ""), refusedPort(t), []string{"127.0.0.1"})
	require.False(t, ok)
	require.Error(t, err)
}

func TestProbeOnceHasNothingToProbeForNonNetworkChecks(t *testing.T) {
	for name, spec := range map[string]agentprotocol.HealthcheckSpec{
		"docker":              {Type: TypeDocker},
		"exec":                {Type: TypeExec},
		"none":                {Type: TypeNone},
		"empty":               {},
		"unknown":             {Type: "carrier-pigeon", Port: 80},
		"http without a port": {Type: TypeHTTP},
		"tcp without a port":  {Type: TypeTCP},
	} {
		t.Run(name, func(t *testing.T) {
			ok, _, err := ProbeOnce(context.Background(), spec, 0, []string{"127.0.0.1"})
			require.False(t, ok)
			require.ErrorIs(t, err, ErrNoNetworkProbe)
		})
	}

	ok, _, err := ProbeOnce(context.Background(), probeSpec(TypeHTTP, "/"), 80, nil)
	require.False(t, ok)
	require.ErrorContains(t, err, "no address")
}

func TestProbeOnceHonoursTheContext(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-block }))
	defer srv.Close()
	defer close(block)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	began := time.Now()
	ok, _, err := ProbeOnce(ctx, probeSpec(TypeHTTP, "/"), serverPort(t, srv), []string{"127.0.0.1"})
	require.False(t, ok)
	require.Error(t, err)
	require.Less(t, time.Since(began), 3*time.Second)
}

func TestProbeOnceHonoursTheSpecTimeout(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-block }))
	defer srv.Close()
	defer close(block)

	spec := agentprotocol.HealthcheckSpec{Type: TypeHTTP, TimeoutSeconds: 3} // 3 × unit = 60 ms here
	began := time.Now()
	ok, _, err := ProbeOnce(context.Background(), spec, serverPort(t, srv), []string{"127.0.0.1"})
	require.False(t, ok)
	require.Error(t, err)
	require.Less(t, time.Since(began), 3*time.Second)
}

func TestProbeWithTLS(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	port := serverPort(t, srv)

	pass := Probe{Spec: probeSpec(TypeHTTP, "/health"), DefaultPort: port, Addrs: []string{"127.0.0.1"}, TLS: &tls.Config{InsecureSkipVerify: true}} //nolint:gosec // httptest certificate
	ok, _, err := pass.Once(context.Background())
	require.NoError(t, err)
	require.True(t, ok)

	// An unverifiable certificate fails the probe.
	strict := pass
	strict.TLS = &tls.Config{}
	ok, _, err = strict.Once(context.Background())
	require.False(t, ok)
	require.ErrorContains(t, err, "certificate")

	// Speaking plain http to a TLS port fails.
	plain := pass
	plain.TLS = nil
	ok, _, err = plain.Once(context.Background())
	require.False(t, ok)
	require.Error(t, err)

	page := pass
	page.Spec.Path = "/missing"
	ok, _, err = page.Once(context.Background())
	require.False(t, ok)
	require.ErrorContains(t, err, "HTTP 404")
}

func TestProbeWithTLSAgainstAPlainServerFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	p := Probe{Spec: probeSpec(TypeHTTP, "/"), DefaultPort: serverPort(t, srv), Addrs: []string{"127.0.0.1"}, TLS: &tls.Config{InsecureSkipVerify: true}} //nolint:gosec // test
	ok, _, err := p.Once(context.Background())
	require.False(t, ok)
	require.Error(t, err)
}

func TestProbeOnceLeavesWaitIntact(t *testing.T) {
	// Wait and ProbeOnce share probeAddrs: a deploy-time check still passes
	// after consecutive successes and reports the answering address.
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

// refusedPort is a loopback port nothing listens on.
func refusedPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := l.Addr().(*net.TCPAddr).Port
	require.NoError(t, l.Close())
	return port
}
