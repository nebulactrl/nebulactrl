package proxy

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// flakyTransport sits in front of fakeTraefik and refuses connections to the
// admin port, like a Traefik container that is being (re)started.
type flakyTransport struct {
	inner http.RoundTripper

	mu      sync.Mutex
	refuse  int      // admin requests still refused
	refused []string // methods of the refused requests, in order
	reqs    []string // methods of every admin request that reached the transport
	puts    int      // PUTs seen, refused or not
	onPut   func(n int, f *flakyTransport)
}

func (f *flakyTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Port() != strconv.Itoa(agentprotocol.ProxyAdminPort) {
		return f.inner.RoundTrip(r)
	}
	f.mu.Lock()
	f.reqs = append(f.reqs, r.Method)
	if r.Method == http.MethodPut {
		f.puts++
		if f.onPut != nil {
			f.onPut(f.puts, f)
		}
	}
	if f.refuse > 0 {
		f.refuse--
		f.refused = append(f.refused, r.Method)
		f.mu.Unlock()
		return nil, &net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", syscall.ECONNREFUSED)}
	}
	f.mu.Unlock()
	return f.inner.RoundTrip(r)
}

func (f *flakyTransport) setRefuse(n int) {
	f.mu.Lock()
	f.refuse = n
	f.mu.Unlock()
}

func (f *flakyTransport) requestCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.reqs)
}

func newFlakyManager(t *testing.T) (*Manager, *fakeTraefik, *flakyTransport) {
	t.Helper()
	m, _, tr := newTestManager(t, nil)
	ft := &flakyTransport{inner: tr}
	m.http = &http.Client{Transport: ft}
	m.adminRetryWindow, m.adminRetryInitial, m.adminRetryMax = 5*time.Second, time.Millisecond, 5*time.Millisecond
	return m, tr, ft
}

func TestApplyWaitsForAdminBeforeFirstPut(t *testing.T) {
	m, tr, ft := newFlakyManager(t)
	require.NoError(t, m.Apply(oneRoute("nc-app-1:80")))
	tr.mu.Lock()
	tr.puts = nil
	tr.mu.Unlock()
	ft.mu.Lock()
	ft.reqs, ft.puts = nil, 0
	ft.mu.Unlock()

	// Traefik is restarting: the admin refuses the next three requests.
	ft.setRefuse(3)
	require.NoError(t, m.Apply(oneRoute("nc-app-2:80")))

	require.Equal(t, []string{http.MethodGet, http.MethodGet, http.MethodGet}, ft.refused,
		"the readiness poll absorbs the refusals; the PUT is not sent into a closed port")
	require.Equal(t, 1, ft.puts, "exactly one PUT, after the admin came back")
	tr.mu.Lock()
	defer tr.mu.Unlock()
	require.Len(t, tr.puts, 1)
	require.Equal(t, "http://nc-app-2:80", tr.upstream)
}

func TestApplyRetriesAConnectionRefusedPut(t *testing.T) {
	m, tr, ft := newFlakyManager(t)
	require.NoError(t, m.Apply(oneRoute("nc-app-1:80")))

	// The admin answers the readiness poll, then goes away exactly for the PUT.
	ft.mu.Lock()
	ft.puts = 0
	ft.onPut = func(n int, f *flakyTransport) {
		if n == 1 {
			f.refuse = 3
		}
	}
	ft.mu.Unlock()
	require.NoError(t, m.Apply(oneRoute("nc-app-2:80")))

	ft.mu.Lock()
	require.GreaterOrEqual(t, ft.puts, 2, "the refused PUT was sent again")
	ft.mu.Unlock()
	tr.mu.Lock()
	defer tr.mu.Unlock()
	require.Equal(t, "http://nc-app-2:80", tr.upstream)
	require.Equal(t, oneRoute("nc-app-2:80"), m.Current())
}

func TestApplyDoesNotRetryARejectedConfig(t *testing.T) {
	m, tr, ft := newFlakyManager(t)
	require.NoError(t, m.Apply(oneRoute("nc-app-1:80")))
	ft.mu.Lock()
	ft.puts = 0
	ft.mu.Unlock()

	tr.mu.Lock()
	tr.rejectPUT = http.StatusBadRequest
	tr.mu.Unlock()
	started := time.Now()
	err := m.Apply(oneRoute("nc-app-2:80"))
	require.ErrorContains(t, err, "rejected config (400)")
	require.NotContains(t, err.Error(), "gave up")
	require.Less(t, time.Since(started), time.Second)

	ft.mu.Lock()
	defer ft.mu.Unlock()
	// One rejected PUT, then the restore of the previous table: never a retry of the 400.
	require.Equal(t, 2, ft.puts)
}

func TestApplyGivesUpOnAPermanentlyDownAdmin(t *testing.T) {
	m, _, ft := newFlakyManager(t)
	require.NoError(t, m.Apply(oneRoute("nc-app-1:80")))
	m.adminRetryWindow = 60 * time.Millisecond

	ft.setRefuse(1 << 30)
	err := m.Apply(oneRoute("nc-app-2:80"))
	require.Error(t, err)
	require.ErrorContains(t, err, "proxy admin unreachable")
	require.ErrorContains(t, err, "connection refused")
	require.ErrorContains(t, err, "gave up after")
	require.Equal(t, oneRoute("nc-app-1:80"), m.Current(), "the applied table is unchanged")
}

func TestRestoreFollowsTheSameRetryRules(t *testing.T) {
	m, tr, ft := newFlakyManager(t)
	require.NoError(t, m.Apply(oneRoute("nc-app-1:80")))

	// The new table is accepted but Traefik cannot build it; the admin then
	// goes down for the push that restores the previous table.
	tr.mu.Lock()
	tr.brokenURL = "nc-app-2"
	tr.mu.Unlock()
	ft.mu.Lock()
	ft.puts = 0
	ft.onPut = func(n int, f *flakyTransport) {
		if n == 2 { // the restore
			f.refuse = 3
		}
	}
	ft.mu.Unlock()

	err := m.Apply(oneRoute("nc-app-2:80"))
	require.ErrorContains(t, err, "disabled routers")
	require.NotContains(t, err.Error(), "restoring the previous route table also failed")

	tr.mu.Lock()
	defer tr.mu.Unlock()
	require.Equal(t, "http://nc-app-1:80", tr.upstream, "the previous table is live again")
	require.Equal(t, oneRoute("nc-app-1:80"), m.Current())
}

func TestIsTransientAdmin(t *testing.T) {
	wrap := func(e error) error {
		return &net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", e)}
	}
	for name, err := range map[string]error{
		"refused":    wrap(syscall.ECONNREFUSED),
		"reset":      wrap(syscall.ECONNRESET),
		"eof":        io.EOF,
		"unexpected": io.ErrUnexpectedEOF,
		"dns":        &net.DNSError{Err: "no such host", IsNotFound: true},
		"timeout":    context.DeadlineExceeded,
	} {
		require.True(t, isTransientAdmin(err), name)
	}
	for name, err := range map[string]error{
		"nil":      nil,
		"4xx":      errors.New("proxy rejected config (400): bad json"),
		"auth":     errors.New("proxy admin rejected the agent's credentials"),
		"canceled": context.Canceled,
	} {
		require.False(t, isTransientAdmin(err), name)
	}
}

// gateEngine blocks the next proxy container create, to hold a recreate open.
type gateEngine struct {
	*fakeEngine
	armed   bool
	entered chan struct{}
	release chan struct{}
}

func (g *gateEngine) ContainerCreate(ctx context.Context, spec docker.ContainerSpec) (string, error) {
	if g.armed && spec.Name == agentprotocol.ProxyContainer {
		g.armed = false
		close(g.entered)
		<-g.release
	}
	return g.fakeEngine.ContainerCreate(ctx, spec)
}

func TestApplyNeverRunsWhileTheContainerIsBeingReplaced(t *testing.T) {
	eng := &gateEngine{fakeEngine: &fakeEngine{}, entered: make(chan struct{}), release: make(chan struct{})}
	m, err := NewManager(Config{Engine: eng, HTTPPort: 8880, HTTPSPort: 8443, HTTPClient: &http.Client{}})
	require.NoError(t, err)
	tr := &fakeTraefik{user: AdminUser, password: m.adminSecret}
	ft := &flakyTransport{inner: tr}
	m.http = &http.Client{Transport: ft}
	require.NoError(t, m.Apply(oneRoute("nc-app-1:80")))

	// A static change recreates Traefik; hold the recreate between the old
	// container's removal and the new one's start.
	eng.armed = true
	m.SetACMEEmail("ops@example.com")
	first := make(chan error, 1)
	go func() { first <- m.Apply(oneRoute("nc-app-2:80")) }()
	<-eng.entered

	reqs := ft.requestCount()
	second := make(chan error, 1)
	go func() { second <- m.Apply(oneRoute("nc-app-3:80")) }()
	time.Sleep(150 * time.Millisecond)
	require.Equal(t, reqs, ft.requestCount(), "a concurrent Apply sends nothing to the admin while the container is replaced")
	select {
	case err := <-second:
		t.Fatalf("second Apply finished during the recreate: %v", err)
	default:
	}

	close(eng.release)
	require.NoError(t, <-first)
	require.NoError(t, <-second)
	require.Equal(t, oneRoute("nc-app-3:80"), m.Current(), "applies ran one after the other, in order")
	eng.mu.Lock()
	defer eng.mu.Unlock()
	require.Len(t, eng.created, 2)
}
