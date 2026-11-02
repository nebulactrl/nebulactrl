package proxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// failStore is a memRouteStore whose calls fail by name.
type failStore struct {
	*memRouteStore
	fail map[string]error
}

func (s *failStore) err(op string) error { return s.fail[op] }

func (s *failStore) LoadRoutes() ([]agentprotocol.Route, error) {
	if err := s.err("LoadRoutes"); err != nil {
		return nil, err
	}
	return s.memRouteStore.LoadRoutes()
}

func (s *failStore) LoadACMEEmail() (string, error) {
	if err := s.err("LoadACMEEmail"); err != nil {
		return "", err
	}
	return s.memRouteStore.LoadACMEEmail()
}

func (s *failStore) LoadTrafficAnalytics() (bool, bool, error) {
	if err := s.err("LoadTrafficAnalytics"); err != nil {
		return false, false, err
	}
	return s.memRouteStore.LoadTrafficAnalytics()
}

func (s *failStore) LoadProxyAdminSecret() (string, error) {
	if err := s.err("LoadProxyAdminSecret"); err != nil {
		return "", err
	}
	return s.memRouteStore.LoadProxyAdminSecret()
}

func (s *failStore) LoadProxyConfig() (agentprotocol.ProxyConfig, bool, error) {
	if err := s.err("LoadProxyConfig"); err != nil {
		return agentprotocol.ProxyConfig{}, false, err
	}
	return s.memRouteStore.LoadProxyConfig()
}

func (s *failStore) SaveProxyAdminSecret(v string) error {
	if err := s.err("SaveProxyAdminSecret"); err != nil {
		return err
	}
	return s.memRouteStore.SaveProxyAdminSecret(v)
}

func (s *failStore) SaveRoutes(r []agentprotocol.Route) error {
	if err := s.err("SaveRoutes"); err != nil {
		return err
	}
	return s.memRouteStore.SaveRoutes(r)
}

func (s *failStore) SaveACMEEmail(v string) error {
	if err := s.err("SaveACMEEmail"); err != nil {
		return err
	}
	return s.memRouteStore.SaveACMEEmail(v)
}

func (s *failStore) SaveTrafficAnalytics(v bool) error {
	if err := s.err("SaveTrafficAnalytics"); err != nil {
		return err
	}
	return s.memRouteStore.SaveTrafficAnalytics(v)
}

func TestNewManagerRejectsMissingEngineAndBadAdminURL(t *testing.T) {
	_, err := NewManager(Config{})
	require.ErrorContains(t, err, "engine is required")

	_, err = NewManager(Config{Engine: &fakeEngine{}, AdminURL: "http://[::1"})
	require.ErrorContains(t, err, `invalid admin url "http://[::1"`)
}

func TestNewManagerDefaults(t *testing.T) {
	m, err := NewManager(Config{Engine: &fakeEngine{}})
	require.NoError(t, err)
	require.Equal(t, "http://127.0.0.1:9000", m.adminURL)
	require.Equal(t, 80, m.cfg.HTTPPort)
	require.Equal(t, 443, m.cfg.HTTPSPort)
	require.Equal(t, agentprotocol.ProxyAdminPort, m.cfg.AdminPort)
	require.Equal(t, agentprotocol.ProxyStarting, m.Status().Status)
	require.True(t, m.TrafficAnalytics(), "the access log is on until a setting says otherwise")

	// The default client never follows a redirect: a probe reports the upstream's own status.
	require.NotNil(t, m.http.CheckRedirect)
	require.Equal(t, http.ErrUseLastResponse, m.http.CheckRedirect(nil, nil))

	// A trailing slash of the admin URL is dropped so request paths join cleanly.
	m, err = NewManager(Config{Engine: &fakeEngine{}, AdminURL: "http://nebulactrl-traefik:9000/"})
	require.NoError(t, err)
	require.Equal(t, "http://nebulactrl-traefik:9000", m.adminURL)
}

func TestNewManagerReportsEveryStoreFailure(t *testing.T) {
	boom := errors.New("disk on fire")
	for op, want := range map[string]string{
		"LoadRoutes":           "load routes: disk on fire",
		"LoadACMEEmail":        "load acme email: disk on fire",
		"LoadTrafficAnalytics": "load traffic analytics setting: disk on fire",
		"LoadProxyAdminSecret": "load proxy admin secret: disk on fire",
		"LoadProxyConfig":      "load proxy config: disk on fire",
		"SaveProxyAdminSecret": "persist proxy admin secret: disk on fire",
	} {
		t.Run(op, func(t *testing.T) {
			st := &failStore{memRouteStore: &memRouteStore{}, fail: map[string]error{op: boom}}
			m, err := NewManager(Config{Engine: &fakeEngine{}, Store: st})
			require.Nil(t, m)
			require.EqualError(t, err, want)
			require.ErrorIs(t, err, boom)
		})
	}
}

func TestNewManagerRestoresPersistedSettingsWithoutOneShotTailnetOps(t *testing.T) {
	off := false
	st := &memRouteStore{
		email:   "ops@example.com",
		traffic: &off,
		cfg: &agentprotocol.ProxyConfig{
			HTTPPort: 8080,
			Tailnet:  &agentprotocol.TailnetSpec{Mode: agentprotocol.TailnetManaged, Provision: true, Remove: true},
		},
	}
	m, err := NewManager(Config{Engine: &fakeEngine{}, Store: st})
	require.NoError(t, err)

	require.Equal(t, "ops@example.com", m.ACMEEmail())
	require.False(t, m.TrafficAnalytics())
	cfg := m.ProxyConfig()
	require.Equal(t, 8080, cfg.HTTPPort)
	require.NotNil(t, cfg.Tailnet)
	require.False(t, cfg.Tailnet.Provision, "a restart must not provision the tailnet node again")
	require.False(t, cfg.Tailnet.Remove, "a restart must not remove the tailnet node again")
	require.True(t, st.cfg.Tailnet.Provision, "the caller's stored value is not mutated")
}

func TestApplyKeepsTheTableWhenPersistingFails(t *testing.T) {
	boom := errors.New("read-only filesystem")
	st := &failStore{memRouteStore: &memRouteStore{}, fail: map[string]error{
		"SaveRoutes": boom, "SaveACMEEmail": boom, "SaveTrafficAnalytics": boom,
	}}
	m, _, _, logs := newFleetManager(t, st, nil)

	require.NoError(t, m.Apply(twoRoutes()), "the table is live, so a persistence failure must not fail the apply")
	require.Equal(t, twoRoutes(), m.Current())
	for _, msg := range []string{"persist routes", "persist acme email", "persist traffic analytics setting"} {
		require.Contains(t, logs.String(), msg)
	}
	require.Contains(t, logs.String(), "read-only filesystem")
}

func TestGenerationIsTheLiveMarker(t *testing.T) {
	m, _, tr := newTestManager(t, nil)
	require.Zero(t, m.Generation(), "no push yet")
	require.NoError(t, m.Apply(oneRoute("a:80")))
	require.NotZero(t, m.Generation())
	require.Equal(t, GenerationHost(m.Generation()), tr.gen)
}

type tableSeqStore struct {
	*memRouteStore
	seq     uint64
	loadErr error
	saveErr error
}

func (s *tableSeqStore) SaveTableSeq(n uint64) error {
	if s.saveErr != nil {
		return s.saveErr
	}
	s.seq = n
	return nil
}

func (s *tableSeqStore) LoadTableSeq() (uint64, error) { return s.seq, s.loadErr }

func TestTableSeqPersistence(t *testing.T) {
	// A store without TableSeqStore keeps no record.
	m, _, _ := newTestManager(t, &memRouteStore{})
	m.SaveTableSeq(7)
	require.Zero(t, m.LoadTableSeq())

	st := &tableSeqStore{memRouteStore: &memRouteStore{}}
	m, _, _ = newTestManager(t, st)
	m.SaveTableSeq(42)
	require.Equal(t, uint64(42), st.seq)
	require.Equal(t, uint64(42), m.LoadTableSeq())

	// A broken store reads as "none" and a failed save does not panic.
	st.loadErr = errors.New("corrupt")
	require.Zero(t, m.LoadTableSeq())
	st.saveErr = errors.New("full")
	m.SaveTableSeq(43)
	require.Equal(t, uint64(42), st.seq, "the failed save leaves the record")

	// Without any store the record is simply absent.
	m, _, _ = newTestManager(t, nil)
	require.Zero(t, m.LoadTableSeq())
	m.SaveTableSeq(1)
}

// rtFunc is an http.RoundTripper made of a function.
type rtFunc func(*http.Request) (*http.Response, error)

func (f rtFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func jsonResponse(code int, header http.Header, body string) *http.Response {
	if header == nil {
		header = http.Header{}
	}
	return &http.Response{StatusCode: code, Header: header, Body: io.NopCloser(strings.NewReader(body))}
}

func TestListRoutersFollowsPagesAndRejectsBadReplies(t *testing.T) {
	m, _, _ := newTestManager(t, nil)

	var urls []string
	m.http = &http.Client{Transport: rtFunc(func(r *http.Request) (*http.Response, error) {
		urls = append(urls, r.URL.RequestURI())
		if u, p, ok := r.BasicAuth(); !ok || u != AdminUser || p != m.adminSecret {
			return jsonResponse(http.StatusUnauthorized, nil, ""), nil
		}
		switch r.URL.Query().Get("page") {
		case "1":
			return jsonResponse(200, http.Header{"X-Next-Page": {"2"}}, `[{"name":"a@rest","status":"enabled"}]`), nil
		default:
			// The last page points at itself.
			return jsonResponse(200, http.Header{"X-Next-Page": {"2"}}, `[{"name":"b@rest","status":"disabled","error":["bad"]}]`), nil
		}
	})}
	got, err := m.listRouters(context.Background(), APIRoutersPath)
	require.NoError(t, err)
	require.Equal(t, map[string]apiRouter{
		"a@rest": {Name: "a@rest", Status: "enabled"},
		"b@rest": {Name: "b@rest", Status: "disabled", Error: []string{"bad"}},
	}, got)
	require.Equal(t, []string{
		APIRoutersPath + "?per_page=500&page=1",
		APIRoutersPath + "?per_page=500&page=2",
	}, urls)

	reply := func(resp *http.Response, err error) {
		m.http = &http.Client{Transport: rtFunc(func(*http.Request) (*http.Response, error) { return resp, err })}
	}
	reply(jsonResponse(500, nil, "oops"), nil)
	_, err = m.listRouters(context.Background(), APIRoutersPath)
	require.EqualError(t, err, "proxy admin returned 500 for router list")

	reply(jsonResponse(200, nil, "not json"), nil)
	_, err = m.listRouters(context.Background(), APIRoutersPath)
	require.ErrorContains(t, err, "decode router list")

	reply(jsonResponse(200, http.Header{"X-Next-Page": {"soon"}}, "[]"), nil)
	_, err = m.listRouters(context.Background(), APIRoutersPath)
	require.ErrorContains(t, err, `invalid X-Next-Page "soon"`)

	reply(nil, errors.New("connection refused"))
	_, err = m.listRouters(context.Background(), APIRoutersPath)
	require.ErrorContains(t, err, "proxy admin unreachable")

	// A header-less reply is a single page.
	reply(jsonResponse(200, nil, `[{"name":"only@rest","status":"enabled"}]`), nil)
	got, err = m.listRouters(context.Background(), APIRoutersPath)
	require.NoError(t, err)
	require.Len(t, got, 1)
}

func TestCheckRoutersReportsMissingAndDisabledRouters(t *testing.T) {
	m, _, tr := newTestManager(t, nil)
	require.NoError(t, m.Apply(oneRoute("a:80")))
	names := routerNamesInLastPut(tr.puts)
	require.NotEmpty(t, names)

	require.NoError(t, m.checkRouters(context.Background(), routerSet{http: names}))
	require.NoError(t, m.checkRouters(context.Background(), routerSet{}), "nothing expected, nothing to list")

	err := m.checkRouters(context.Background(), routerSet{http: []string{"ghost"}})
	require.EqualError(t, err, "router ghost not loaded yet")
	var rr *routerRejected
	require.False(t, errors.As(err, &rr), "a router that has not loaded yet is retried, not a rejection")

	// A broken listing is an error too (not "everything is fine").
	m.http = &http.Client{Transport: rtFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("connection refused")
	})}
	err = m.checkRouters(context.Background(), routerSet{tcp: []string{"x"}})
	require.ErrorContains(t, err, "proxy admin unreachable")
}

func TestWaitLiveStopsWhenTheContextEnds(t *testing.T) {
	m, _, _ := newTestManager(t, nil)
	require.NoError(t, m.Apply(oneRoute("a:80")))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// The marker of generation 1 is not live: the poll must give up when asked to.
	err := m.waitLive(ctx, 1, routerSet{})
	require.ErrorIs(t, err, context.Canceled)
}

func TestWaitLiveReturnsDefinitiveRouterRejection(t *testing.T) {
	m, _, tr := newTestManager(t, nil)
	require.NoError(t, m.Apply(oneRoute("a:80")))
	tr.mu.Lock()
	tr.disabled = true
	tr.mu.Unlock()
	names := routerNamesInLastPut(tr.puts)
	err := m.waitLive(context.Background(), m.Generation(), routerSet{http: names})
	var rr *routerRejected
	require.ErrorAs(t, err, &rr, "a disabled router is final, waiting longer cannot fix it")
}

func TestProbe(t *testing.T) {
	t.Run("inspect failure", func(t *testing.T) {
		m, _, _ := newTestManager(t, nil)
		_, err := m.Probe(context.Background(), "h", "/")
		require.ErrorContains(t, err, "inspect "+agentprotocol.ProxyContainer)
		require.ErrorIs(t, err, docker.ErrNotFound)
	})

	t.Run("falls back to the container name and normalizes the path", func(t *testing.T) {
		m, eng, tr := newTestManager(t, nil)
		require.NoError(t, m.Apply(oneRoute("a:80")))
		eng.mu.Lock()
		ip := eng.container.Networks[agentprotocol.EdgeNetwork].IP
		eng.mu.Unlock()

		var dialed []string
		m.http = &http.Client{Transport: rtFunc(func(r *http.Request) (*http.Response, error) {
			dialed = append(dialed, r.URL.Hostname()+r.URL.Path)
			if r.URL.Hostname() == ip {
				return nil, errors.New("no route to host") // Docker Desktop: the IP is not routable
			}
			return tr.RoundTrip(r)
		})}
		code, err := m.Probe(context.Background(), PingHost, "ping")
		require.NoError(t, err)
		require.Equal(t, 200, code)
		require.Equal(t, []string{ip + "/ping", agentprotocol.ProxyContainer + "/ping"}, dialed)
	})

	t.Run("all targets failing returns the last error", func(t *testing.T) {
		m, _, _ := newTestManager(t, nil)
		require.NoError(t, m.Apply(oneRoute("a:80")))
		m.http = &http.Client{Transport: rtFunc(func(r *http.Request) (*http.Response, error) {
			return nil, fmt.Errorf("dial %s: refused", r.URL.Hostname())
		})}
		_, err := m.Probe(context.Background(), "h", "/")
		require.ErrorContains(t, err, "dial "+agentprotocol.ProxyContainer+": refused")
	})

	t.Run("a cancelled context stops after the first target", func(t *testing.T) {
		m, _, _ := newTestManager(t, nil)
		require.NoError(t, m.Apply(oneRoute("a:80")))
		ctx, cancel := context.WithCancel(context.Background())
		var calls int
		m.http = &http.Client{Transport: rtFunc(func(r *http.Request) (*http.Response, error) {
			calls++
			cancel()
			return nil, errors.New("canceled")
		})}
		_, err := m.Probe(ctx, "h", "/")
		require.Error(t, err)
		require.Equal(t, 1, calls)
	})

	t.Run("an invalid path is rejected before dialing", func(t *testing.T) {
		m, _, _ := newTestManager(t, nil)
		require.NoError(t, m.Apply(oneRoute("a:80")))
		_, err := m.Probe(context.Background(), "h", "/\x7f")
		require.ErrorContains(t, err, "invalid control character in URL")
	})
}

func TestCheck(t *testing.T) {
	t.Run("container missing", func(t *testing.T) {
		m, _, _ := newTestManager(t, nil)
		err := m.check(context.Background())
		require.ErrorContains(t, err, "inspect proxy")
		require.ErrorIs(t, err, docker.ErrNotFound)
	})

	t.Run("container stopped", func(t *testing.T) {
		m, eng, _ := newTestManager(t, nil)
		require.NoError(t, m.Apply(oneRoute("a:80")))
		eng.mu.Lock()
		eng.container.Running, eng.container.State = false, "exited"
		eng.mu.Unlock()
		require.EqualError(t, m.check(context.Background()), "proxy container is exited")
	})

	t.Run("healthy and before the first push", func(t *testing.T) {
		m, _, _ := newTestManager(t, nil)
		require.NoError(t, m.Apply(oneRoute("a:80")))
		require.NoError(t, m.check(context.Background()))

		// A container that runs but whose proxy never took a table (generation 0).
		m.mu.Lock()
		m.gen = 0
		m.mu.Unlock()
		m.http = &http.Client{Transport: rtFunc(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("must not be probed")
		})}
		require.NoError(t, m.check(context.Background()), "no generation yet: nothing to probe")
	})

	t.Run("restarted traefik lost its table", func(t *testing.T) {
		m, _, tr := newTestManager(t, nil)
		require.NoError(t, m.Apply(oneRoute("a:80")))
		gen := m.Generation()
		tr.mu.Lock()
		tr.gen = "" // a fresh Traefik has no dynamic config
		tr.mu.Unlock()
		err := m.check(context.Background())
		require.EqualError(t, err, fmt.Sprintf("proxy lost its route table (generation %d answered 404)", gen))
	})

	t.Run("unreachable probe entrypoint", func(t *testing.T) {
		m, _, _ := newTestManager(t, nil)
		require.NoError(t, m.Apply(oneRoute("a:80")))
		m.http = &http.Client{Transport: rtFunc(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("connection refused")
		})}
		require.ErrorContains(t, m.check(context.Background()), "connection refused")
	})

	t.Run("an apply in flight owns the container", func(t *testing.T) {
		m, _, _ := newTestManager(t, nil)
		m.applyMu.Lock()
		defer m.applyMu.Unlock()
		// The container does not even exist, yet check must not touch it.
		require.NoError(t, m.check(context.Background()))
	})

	t.Run("pages server down", func(t *testing.T) {
		m, eng, _ := pagesManager(t)
		require.NoError(t, m.Apply(twoRoutes()))
		require.NoError(t, m.check(context.Background()))

		eng.mu.Lock()
		for _, name := range pagesNames {
			delete(eng.ctrs, name)
		}
		eng.mu.Unlock()
		require.EqualError(t, m.check(context.Background()), "pages server container is not running")
	})

	t.Run("pages inspect failure", func(t *testing.T) {
		m, eng, _ := pagesManager(t)
		require.NoError(t, m.Apply(twoRoutes()))
		fe := &faultEngine{fleetEngine: eng, fail: map[string]error{"inspect:" + agentprotocol.PagesContainer: errors.New("daemon gone")}}
		m.cfg.Engine = fe
		require.EqualError(t, m.check(context.Background()), "inspect pages server: daemon gone")
	})
}

// faultEngine injects failures into a fleetEngine by operation key.
type faultEngine struct {
	*fleetEngine
	mu   sync.Mutex
	fail map[string]error
	// logs is what ContainerLogs returns; logOpts records its options.
	logs    string
	logOpts []docker.LogOptions
}

func (f *faultEngine) failing(key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.fail[key]
}

func (f *faultEngine) set(key string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail == nil {
		f.fail = map[string]error{}
	}
	if err == nil {
		delete(f.fail, key)
		return
	}
	f.fail[key] = err
}

func (f *faultEngine) EnsureNetwork(ctx context.Context, n string, l map[string]string, o ...docker.NetworkOption) error {
	if err := f.failing("network"); err != nil {
		return err
	}
	return f.fleetEngine.EnsureNetwork(ctx, n, l, o...)
}

func (f *faultEngine) EnsureVolume(ctx context.Context, n string, l map[string]string) error {
	if err := f.failing("volume"); err != nil {
		return err
	}
	return f.fleetEngine.EnsureVolume(ctx, n, l)
}

func (f *faultEngine) ImageInspect(ctx context.Context, ref string) (docker.ImageInfo, error) {
	if err := f.failing("imageinspect"); err != nil {
		return docker.ImageInfo{}, err
	}
	return f.fleetEngine.ImageInspect(ctx, ref)
}

func (f *faultEngine) ImagePull(ctx context.Context, ref, auth string, fn func(string)) error {
	if err := f.failing("pull"); err != nil {
		return err
	}
	return f.fleetEngine.ImagePull(ctx, ref, auth, fn)
}

func (f *faultEngine) CopyToContainer(ctx context.Context, id, dir string, r io.Reader) error {
	if err := f.failing("copy"); err != nil {
		return err
	}
	return f.fleetEngine.CopyToContainer(ctx, id, dir, r)
}

func (f *faultEngine) ContainerRemove(ctx context.Context, id string, force, vols bool) error {
	if err := f.failing("remove"); err != nil {
		return err
	}
	return f.fleetEngine.ContainerRemove(ctx, id, force, vols)
}

func (f *faultEngine) ContainerInspect(ctx context.Context, nameOrID string) (docker.ContainerInfo, error) {
	if err := f.failing("inspect:" + nameOrID); err != nil {
		return docker.ContainerInfo{}, err
	}
	return f.fleetEngine.ContainerInspect(ctx, nameOrID)
}

func (f *faultEngine) ContainerLogs(_ context.Context, _ string, o docker.LogOptions) (io.ReadCloser, error) {
	if err := f.failing("logs"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.logOpts = append(f.logOpts, o)
	return io.NopCloser(strings.NewReader(f.logs)), nil
}

func (f *faultEngine) ContainerExec(ctx context.Context, id string, argv []string, spec docker.ExecSpec, so, se io.Writer) (int, error) {
	if err := f.failing("exec"); err != nil {
		return 0, err
	}
	return f.fleetEngine.ContainerExec(ctx, id, argv, spec, so, se)
}

func faultyManager(t *testing.T, mut func(*Config)) (*Manager, *faultEngine, *fakeTraefik) {
	t.Helper()
	fe := &faultEngine{fleetEngine: newFleetEngine()}
	m, _, tr, _ := newFleetManager(t, &memRouteStore{}, func(c *Config) {
		c.Engine = fe
		if mut != nil {
			mut(c)
		}
	})
	return m, fe, tr
}

func TestFirstContainerCreationFailures(t *testing.T) {
	px := agentprotocol.ProxyContainer
	cases := []struct {
		name  string
		key   string
		want  string
		start func(docker.ContainerSpec) error
	}{
		{name: "edge network", key: "network", want: "no network"},
		{name: "acme volume", key: "volume", want: "no volume"},
		{name: "image inspect", key: "imageinspect", want: "no image"},
		{name: "image pull", key: "pull", want: "pull denied"},
		{name: "bootstrap copy", key: "copy", want: "write proxy bootstrap config: no space"},
	}
	errs := map[string]error{
		"network": errors.New("no network"), "volume": errors.New("no volume"), "imageinspect": errors.New("no image"),
		"pull": errors.New("pull denied"), "copy": errors.New("no space"),
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m, fe, tr := faultyManager(t, nil)
			fe.set(c.key, errs[c.key])
			if c.key == "pull" {
				// traefik's image is "present" in the fleet fake; force the pull path.
				fe.set("imageinspect", fmt.Errorf("image: %w", docker.ErrNotFound))
			}
			err := m.Apply(twoRoutes())
			require.ErrorContains(t, err, c.want)
			require.Empty(t, tr.puts, "nothing is pushed to a proxy that never started")
			require.Nil(t, m.Current(), "the table is not remembered")
			if c.key == "copy" {
				_, _, exists := fe.container(px)
				require.False(t, exists, "a container without its bootstrap file is removed, not left to start")
			}
		})
	}
}

func TestContainerCreateFailureIsReported(t *testing.T) {
	m, fe, _ := faultyManager(t, nil)
	fe.createErr = func(docker.ContainerSpec) error { return errors.New("name in use") }
	require.ErrorContains(t, m.Apply(twoRoutes()), "create proxy container: name in use")
}

func TestInspectFailureOtherThanNotFoundStopsApply(t *testing.T) {
	m, fe, _ := faultyManager(t, nil)
	fe.set("inspect:"+agentprotocol.ProxyContainer, errors.New("daemon not responding"))
	err := m.Apply(twoRoutes())
	require.EqualError(t, err, "daemon not responding")
	require.Zero(t, fe.count("create:"+agentprotocol.ProxyContainer), "an unknown state must not trigger a create")
}

func TestStoppedProxyContainerIsStartedNotRecreated(t *testing.T) {
	m, fe, tr := faultyManager(t, nil)
	px := agentprotocol.ProxyContainer
	require.NoError(t, m.Apply(twoRoutes()))

	fe.fleetEngine.mu.Lock()
	fe.ctrs[px].info.Running, fe.ctrs[px].info.State = false, "exited"
	fe.fleetEngine.mu.Unlock()
	puts := len(tr.puts)

	require.NoError(t, m.Apply(twoRoutes()))
	require.Equal(t, 1, fe.count("create:"+px), "same spec: the existing container is reused")
	require.Equal(t, 2, fe.count("start:"+px))
	require.Greater(t, len(tr.puts), puts, "the table is pushed again to the restarted Traefik")

	// A start that fails is reported with the port hint and nothing is recreated.
	fe.fleetEngine.mu.Lock()
	fe.ctrs[px].info.Running, fe.ctrs[px].info.State = false, "exited"
	fe.fleetEngine.mu.Unlock()
	fe.startErr = func(docker.ContainerSpec) error {
		return errors.New("listen tcp4 0.0.0.0:8880: bind: address already in use")
	}
	err := m.Apply(twoRoutes())
	require.ErrorContains(t, err, "proxy failed to start")
	require.ErrorContains(t, err, "port 8880 or 8443 already in use")
	require.Equal(t, 1, fe.count("create:"+px))
}

func TestRecreateStopsWhenTheOldContainerCannotBeRemoved(t *testing.T) {
	m, fe, _ := faultyManager(t, nil)
	px := agentprotocol.ProxyContainer
	require.NoError(t, m.Apply(twoRoutes()))

	m.SetACMEEmail("ops@example.com") // static change: needs a recreate
	fe.set("remove", errors.New("container is paused"))
	err := m.Apply(twoRoutes())
	require.EqualError(t, err, "container is paused")
	require.Equal(t, 1, fe.count("create:"+px))
	require.Equal(t, twoRoutes(), m.Current())
}

func TestRecreateFailureWithFailingRestoreReportsBoth(t *testing.T) {
	m, fe, _ := faultyManager(t, nil)
	px := agentprotocol.ProxyContainer
	require.NoError(t, m.Apply(twoRoutes()))

	// The new container cannot be created, and neither can the previous one
	// (the daemon is out of space for the rest of this test).
	fe.createErr = func(docker.ContainerSpec) error { return errors.New("no space left on device") }
	m.SetACMEEmail("ops@example.com")
	err := m.Apply(twoRoutes())
	require.ErrorContains(t, err, "create proxy container: no space left on device")
	require.ErrorContains(t, err, "restoring the previous proxy container also failed")
	var restored *restoredError
	require.False(t, errors.As(err, &restored), "nothing was restored")
	_, _, exists := fe.container(px)
	require.False(t, exists)
}

func TestRecreateFailureWithoutPriorSpecChangeIsPlain(t *testing.T) {
	// The same spec cannot be "restored" onto itself: the error is just the failure.
	m, fe, _ := faultyManager(t, nil)
	fe.createErr = func(docker.ContainerSpec) error { return errors.New("denied") }
	err := m.Apply(twoRoutes())
	require.EqualError(t, err, "create proxy container: denied")
}

func TestRestoreContainerRemoveFailure(t *testing.T) {
	m, fe, tr := faultyManager(t, nil)
	require.NoError(t, m.Apply(twoRoutes()))
	m.SetACMEEmail("ops@example.com")
	tr.mu.Lock()
	tr.rejectPUT = http.StatusBadRequest
	tr.mu.Unlock()
	// The recreate itself removes the old container; fail the next removal,
	// which is the restore's.
	removes := 0
	fe.set("remove", nil)
	m.cfg.Engine = &removeCounter{faultEngine: fe, failFrom: 2, n: &removes}
	err := m.Apply(twoRoutes())
	require.ErrorContains(t, err, "rejected config (400)")
	require.ErrorContains(t, err, "restoring the previous proxy container also failed")
	require.ErrorContains(t, err, "container is paused")
}

// removeCounter fails ContainerRemove from the failFrom-th call on.
type removeCounter struct {
	*faultEngine
	failFrom int
	n        *int
}

func (r *removeCounter) ContainerRemove(ctx context.Context, id string, force, vols bool) error {
	*r.n++
	if *r.n >= r.failFrom {
		return errors.New("container is paused")
	}
	return r.faultEngine.ContainerRemove(ctx, id, force, vols)
}

func TestListenInUse(t *testing.T) {
	tcp, err := net.Listen("tcp", ":0")
	require.NoError(t, err)
	tcpPort := tcp.Addr().(*net.TCPAddr).Port
	require.True(t, listenInUse("tcp", tcpPort), "a held tcp port is in use")
	require.NoError(t, tcp.Close())
	require.False(t, listenInUse("tcp", tcpPort), "a released port is free")

	udp, err := net.ListenPacket("udp", ":0")
	require.NoError(t, err)
	udpPort := udp.LocalAddr().(*net.UDPAddr).Port
	require.True(t, listenInUse("udp", udpPort), "a held udp port is in use")
	require.NoError(t, udp.Close())
	require.False(t, listenInUse("udp", udpPort))
}

func TestPortHint(t *testing.T) {
	l4 := []L4Port{{Proto: "tcp", Port: 5432}, {Proto: "udp", Port: 5353}}
	inUse := func(msg string) error { return errors.New(msg) }

	// Not a bind failure: untouched.
	other := errors.New("no such image")
	require.Equal(t, other, portHint(other, 80, 443, l4))

	err := portHint(inUse("Bind for 0.0.0.0:5432 failed: port is already allocated"), 80, 443, l4)
	require.ErrorContains(t, err, "tcp port 5432 is already in use on this server (a tcp/udp route needs it)")

	err = portHint(inUse("listen udp4 0.0.0.0:5353: bind: address already in use"), 80, 443, l4)
	require.ErrorContains(t, err, "udp port 5353 is already in use")

	// A port that is none of the routes': name the candidates.
	err = portHint(inUse("Bind for 0.0.0.0:80 failed: port is already allocated"), 80, 443, l4)
	require.ErrorContains(t, err, "a port is already in use (web ports 80 and 443, tcp/udp route ports tcp/5432, udp/5353)")

	// Without tcp/udp routes only the web ports can be meant.
	err = portHint(inUse("Bind for 0.0.0.0:80 failed: port is already allocated"), 8880, 8443, nil)
	require.ErrorContains(t, err, "port 8880 or 8443 already in use (is another web server running?)")

	// The original error stays reachable.
	cause := inUse("address already in use")
	require.ErrorIs(t, portHint(cause, 80, 443, nil), cause)
}

func TestRestoredErrorUnwraps(t *testing.T) {
	cause := errors.New("bind failed")
	err := error(&restoredError{err: cause})
	require.Equal(t, "bind failed", err.Error())
	require.ErrorIs(t, err, cause)
}

func TestBootstrapStatuses(t *testing.T) {
	m, _, _ := newTestManager(t, nil)
	require.NoError(t, m.Bootstrap(context.Background()))
	require.Equal(t, agentprotocol.ProxyOK, m.Status().Status)
	require.Empty(t, m.Status().Error)
	require.NotZero(t, m.Generation(), "an empty table is still pushed so the marker exists")

	m, fe, _ := faultyManager(t, nil)
	fe.createErr = func(docker.ContainerSpec) error { return errors.New("boom") }
	err := m.Bootstrap(context.Background())
	require.ErrorContains(t, err, "restore routes: create proxy container: boom")
}

func TestRunBootstrapsThenStopsOnCancel(t *testing.T) {
	m, _, _ := newTestManager(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { m.Run(ctx); close(done) }()

	require.Eventually(t, func() bool { return m.Status().Status == agentprotocol.ProxyOK }, 5*time.Second, time.Millisecond)
	cancel()
	<-done
}

func TestRunReportsBootstrapFailureAndRetries(t *testing.T) {
	m, fe, _ := faultyManager(t, nil)
	fe.createErr = func(docker.ContainerSpec) error { return errors.New("port 80 busy") }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { m.Run(ctx); close(done) }()

	require.Eventually(t, func() bool {
		s := m.Status()
		return s.Status == agentprotocol.ProxyError && strings.Contains(s.Error, "port 80 busy")
	}, 5*time.Second, time.Millisecond)
	cancel()
	<-done
}

func TestRunReturnsQuietlyWhenCancelledDuringBootstrap(t *testing.T) {
	m, fe, _ := faultyManager(t, nil)
	fe.createErr = func(docker.ContainerSpec) error { return errors.New("interrupted") }
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	m.Run(ctx) // returns without waiting for the retry interval
	require.Equal(t, agentprotocol.ProxyStarting, m.Status().Status, "a shutdown is not reported as a proxy error")
}
