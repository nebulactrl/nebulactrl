package proxy

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// fakeEngine is the slice of docker.Engine the proxy manager uses; embedding
// the interface makes any other call panic.
type fakeEngine struct {
	docker.Engine
	mu        sync.Mutex
	container *docker.ContainerInfo
	created   []docker.ContainerSpec
	removed   int
	started   int
	copied    []byte
	copyDir   string
	// networks are the Docker networks NetworkList reports (the edge network's
	// subnets, the default bridge): nil = none.
	networks []docker.NetworkInfo
}

func (f *fakeEngine) EnsureNetwork(context.Context, string, map[string]string, ...docker.NetworkOption) error {
	return nil
}

func (f *fakeEngine) NetworkList(_ context.Context, prefix string, _ map[string]string) ([]docker.NetworkInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []docker.NetworkInfo
	for _, n := range f.networks {
		if strings.HasPrefix(n.Name, prefix) {
			out = append(out, n)
		}
	}
	return out, nil
}
func (f *fakeEngine) EnsureVolume(context.Context, string, map[string]string) error { return nil }
func (f *fakeEngine) ImageInspect(context.Context, string) (docker.ImageInfo, error) {
	return docker.ImageInfo{}, nil
}

func (f *fakeEngine) ContainerInspect(_ context.Context, _ string) (docker.ContainerInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.container == nil {
		return docker.ContainerInfo{}, docker.ErrNotFound
	}
	return *f.container, nil
}

func (f *fakeEngine) ContainerCreate(_ context.Context, spec docker.ContainerSpec) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.created = append(f.created, spec)
	f.container = &docker.ContainerInfo{
		ID: "px1", Name: spec.Name, Image: spec.Image, Labels: spec.Labels, RestartMode: spec.RestartPolicy, State: "created",
		Networks: map[string]docker.ContainerNetwork{agentprotocol.EdgeNetwork: {IP: "10.0.0.2"}},
	}
	return "px1", nil
}

func (f *fakeEngine) CopyToContainer(_ context.Context, _ string, dir string, r io.Reader) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.copyDir = dir
	f.copied, _ = io.ReadAll(r)
	return nil
}

func (f *fakeEngine) ContainerExec(context.Context, string, []string, docker.ExecSpec, io.Writer, io.Writer) (int, error) {
	return 0, nil
}

func (f *fakeEngine) ContainerStart(context.Context, string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.started++
	f.container.State, f.container.Running = "running", true
	return nil
}

func (f *fakeEngine) ContainerRemove(context.Context, string, bool, bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removed++
	f.container = nil
	return nil
}

// fakeTraefik simulates the admin, API and probe entrypoints in-process.
type fakeTraefik struct {
	mu       sync.Mutex
	user     string
	password string

	puts       []string // PUT bodies in order
	gen        string   // marker host of the live config
	upstream   string   // upstream URL of the live config's first service
	rejectPUT  int      // when non-zero the next PUT answers this status
	brokenURL  string   // PUT bodies containing this make routers disabled
	disabled   bool     // live config is broken: routers disabled
	disabledL4 bool     // tcp/udp routers report disabled
	unauthPUTs int
}

var genRe = regexp.MustCompile("Host\\(`(gen-\\d+\\.nc\\.invalid)`\\)")

func (f *fakeTraefik) RoundTrip(r *http.Request) (*http.Response, error) {
	rec := httptest.NewRecorder()
	f.ServeHTTP(rec, r)
	return rec.Result(), nil
}

func (f *fakeTraefik) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	port := r.URL.Port()
	switch {
	case port == strconv.Itoa(agentprotocol.ProxyProbePort):
		host := r.Host
		switch {
		case host == PingHost, host == f.gen && f.gen != "":
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
		return
	case port == "9000":
		u, p, ok := r.BasicAuth()
		if !ok || u != f.user || p != f.password {
			f.unauthPUTs++
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch {
		case r.Method == http.MethodPut && r.URL.Path == RESTPath:
			body, _ := io.ReadAll(r.Body)
			f.puts = append(f.puts, string(body))
			if f.rejectPUT != 0 {
				code := f.rejectPUT
				f.rejectPUT = 0
				http.Error(w, "bad json", code)
				return
			}
			f.gen = genRe.FindStringSubmatch(string(body))[1]
			f.disabled = f.brokenURL != "" && strings.Contains(string(body), f.brokenURL)
			var cfg struct {
				HTTP struct {
					Services map[string]struct {
						LoadBalancer struct {
							Servers []struct{ URL string } `json:"servers"`
						} `json:"loadBalancer"`
					} `json:"services"`
				} `json:"http"`
			}
			_ = json.Unmarshal(body, &cfg)
			f.upstream = ""
			for _, s := range cfg.HTTP.Services {
				if len(s.LoadBalancer.Servers) > 0 {
					f.upstream = s.LoadBalancer.Servers[0].URL
				}
			}
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodGet && r.URL.Path == APIRoutersPath:
			var out []apiRouter
			for _, name := range routerNamesInLastPut(f.puts) {
				ar := apiRouter{Name: name + "@rest", Status: "enabled"}
				if f.disabled && name != genRouter {
					ar.Status, ar.Error = "disabled", []string{"error parsing server URL"}
				}
				out = append(out, ar)
			}
			w.Header().Set("X-Next-Page", "1")
			_ = json.NewEncoder(w).Encode(out)
		case r.Method == http.MethodGet && (r.URL.Path == APITCPRoutersPath || r.URL.Path == APIUDPRoutersPath):
			proto := "tcp"
			if r.URL.Path == APIUDPRoutersPath {
				proto = "udp"
			}
			var out []apiRouter
			for _, name := range l4RouterNamesInLastPut(f.puts, proto) {
				ar := apiRouter{Name: name + "@rest", Status: "enabled"}
				if f.disabledL4 {
					ar.Status, ar.Error = "disabled", []string{"entrypoint does not exist"}
				}
				out = append(out, ar)
			}
			w.Header().Set("X-Next-Page", "1")
			_ = json.NewEncoder(w).Encode(out)
		default:
			http.NotFound(w, r)
		}
		return
	}
	http.NotFound(w, r)
}

func routerNamesInLastPut(puts []string) []string {
	if len(puts) == 0 {
		return nil
	}
	var cfg struct {
		HTTP struct {
			Routers map[string]json.RawMessage `json:"routers"`
		} `json:"http"`
	}
	_ = json.Unmarshal([]byte(puts[len(puts)-1]), &cfg)
	var names []string
	for n := range cfg.HTTP.Routers {
		names = append(names, n)
	}
	return names
}

// l4RouterNamesInLastPut lists the tcp or udp routers of the last PUT body.
func l4RouterNamesInLastPut(puts []string, proto string) []string {
	if len(puts) == 0 {
		return nil
	}
	var cfg map[string]struct {
		Routers map[string]json.RawMessage `json:"routers"`
	}
	_ = json.Unmarshal([]byte(puts[len(puts)-1]), &cfg)
	var names []string
	for n := range cfg[proto].Routers {
		names = append(names, n)
	}
	return names
}

type memRouteStore struct {
	routes  []agentprotocol.Route
	email   string
	traffic *bool
	secret  string
	cfg     *agentprotocol.ProxyConfig
	cfgOK   bool
}

func (s *memRouteStore) SaveRoutes(r []agentprotocol.Route) error { s.routes = r; return nil }
func (s *memRouteStore) LoadRoutes() ([]agentprotocol.Route, error) {
	return s.routes, nil
}
func (s *memRouteStore) SaveACMEEmail(e string) error      { s.email = e; return nil }
func (s *memRouteStore) LoadACMEEmail() (string, error)    { return s.email, nil }
func (s *memRouteStore) SaveTrafficAnalytics(v bool) error { s.traffic = &v; return nil }
func (s *memRouteStore) LoadTrafficAnalytics() (bool, bool, error) {
	if s.traffic == nil {
		return false, false, nil
	}
	return *s.traffic, true, nil
}
func (s *memRouteStore) SaveProxyAdminSecret(v string) error   { s.secret = v; return nil }
func (s *memRouteStore) LoadProxyAdminSecret() (string, error) { return s.secret, nil }
func (s *memRouteStore) SaveProxyConfig(c agentprotocol.ProxyConfig) error {
	s.cfg, s.cfgOK = &c, true
	return nil
}
func (s *memRouteStore) LoadProxyConfig() (agentprotocol.ProxyConfig, bool, error) {
	if s.cfg == nil {
		return agentprotocol.ProxyConfig{}, false, nil
	}
	return *s.cfg, true, nil
}

func newTestManager(t *testing.T, st RouteStore) (*Manager, *fakeEngine, *fakeTraefik) {
	t.Helper()
	eng := &fakeEngine{}
	m, err := NewManager(Config{
		Engine: eng, Store: st, HTTPPort: 8880, HTTPSPort: 8443,
		HTTPClient: &http.Client{},
	})
	require.NoError(t, err)
	tr := &fakeTraefik{user: AdminUser, password: m.adminSecret}
	m.http = &http.Client{Transport: tr}
	return m, eng, tr
}

func oneRoute(upstream string) []agentprotocol.Route {
	return []agentprotocol.Route{{ServiceID: svcApp, Hosts: []string{"app.localhost"}, Upstreams: []string{upstream}}}
}

func TestApplyWaitsForGenerationAndAuthenticates(t *testing.T) {
	st := &memRouteStore{}
	m, eng, tr := newTestManager(t, st)

	require.NoError(t, m.Apply(oneRoute("nc-app-1:80")))

	require.Len(t, tr.puts, 1)
	require.Equal(t, 0, tr.unauthPUTs, "agent authenticates with the admin secret")
	require.Equal(t, "http://nc-app-1:80", tr.upstream)
	require.Equal(t, GenerationHost(m.generation()), tr.gen, "the live marker is the manager's generation")
	require.Equal(t, oneRoute("nc-app-1:80"), m.Current())
	require.Equal(t, oneRoute("nc-app-1:80"), st.routes, "table is persisted")
	require.Equal(t, 1, eng.started)

	// Probe answers through the probe entrypoint: routed host vs unknown.
	code, err := m.Probe(context.Background(), GenerationHost(m.generation()), "/")
	require.NoError(t, err)
	require.Equal(t, 200, code)
	code, err = m.Probe(context.Background(), "unknown.localhost", "/x")
	require.NoError(t, err)
	require.Equal(t, 404, code, "unknown hosts must 404 so verify cannot pass falsely")
}

func TestApplyGenerationsStrictlyIncrease(t *testing.T) {
	m, _, _ := newTestManager(t, nil)
	require.NoError(t, m.Apply(oneRoute("a:80")))
	g1 := m.generation()
	require.NoError(t, m.Apply(oneRoute("b:80")))
	require.Greater(t, m.generation(), g1)
}

func TestApplyRejectedPUTKeepsPreviousTable(t *testing.T) {
	st := &memRouteStore{}
	m, _, tr := newTestManager(t, st)
	require.NoError(t, m.Apply(oneRoute("nc-app-1:80")))

	tr.rejectPUT = http.StatusBadRequest
	err := m.Apply(oneRoute("nc-app-2:80"))
	require.ErrorContains(t, err, "rejected config (400)")

	require.Equal(t, oneRoute("nc-app-1:80"), m.Current(), "in-memory table unchanged")
	require.Equal(t, oneRoute("nc-app-1:80"), st.routes, "persisted table unchanged")
	// Rejected PUT, then the previous table pushed again.
	require.Len(t, tr.puts, 3)
	require.Equal(t, "http://nc-app-1:80", tr.upstream, "previous route is live again")
}

func TestApplyDetectsDisabledRoutersAndRestoresPrevious(t *testing.T) {
	st := &memRouteStore{}
	m, _, tr := newTestManager(t, st)
	require.NoError(t, m.Apply(oneRoute("nc-app-1:80")))

	// Traefik accepts the PUT (200) but cannot build the service.
	tr.brokenURL = "nc-app-2"
	err := m.Apply(oneRoute("nc-app-2:80"))
	require.ErrorContains(t, err, "disabled routers")
	require.ErrorContains(t, err, "error parsing server URL")

	require.Equal(t, oneRoute("nc-app-1:80"), m.Current())
	require.Equal(t, oneRoute("nc-app-1:80"), st.routes)
	require.False(t, tr.disabled, "the restored table is healthy")
	require.Equal(t, "http://nc-app-1:80", tr.upstream)
}

func TestApplyReportsFailedRestore(t *testing.T) {
	m, _, tr := newTestManager(t, nil)
	require.NoError(t, m.Apply(oneRoute("nc-app-1:80")))
	tr.brokenURL = "nc-app" // both the new and the restored table are rejected
	err := m.Apply(oneRoute("nc-app-2:80"))
	require.ErrorContains(t, err, "restoring the previous route table also failed")
}

func TestApplyValidatesBeforeTouchingTheProxy(t *testing.T) {
	m, _, tr := newTestManager(t, nil)
	err := m.Apply([]agentprotocol.Route{{ServiceID: svcApp, Hosts: []string{"a`b"}, Upstreams: []string{"x:80"}}})
	require.Error(t, err)
	require.Empty(t, tr.puts)
}

// Routes persisted before replicas (no upstreams) cannot be rendered; the
// manager drops them instead of failing every later Apply.
func TestLegacyPersistedRoutesAreDropped(t *testing.T) {
	st := &memRouteStore{routes: []agentprotocol.Route{
		{ServiceID: "old", Hosts: []string{"old.localhost"}},
		{ServiceID: svcApp, Hosts: []string{"app.localhost"}, Upstreams: []string{"x:80"}},
	}}
	m, _, _ := newTestManager(t, st)
	require.Equal(t, []agentprotocol.Route{{ServiceID: svcApp, Hosts: []string{"app.localhost"}, Upstreams: []string{"x:80"}}}, m.Current())
}

func TestStaticSettingChangeRecreatesContainer(t *testing.T) {
	m, eng, _ := newTestManager(t, nil)
	require.NoError(t, m.Apply(oneRoute("a:80")))
	require.Len(t, eng.created, 1)

	// Same static config: no recreate.
	require.NoError(t, m.Apply(oneRoute("b:80")))
	require.Len(t, eng.created, 1)
	require.Equal(t, 0, eng.removed)

	// ACME email is static configuration.
	m.SetACMEEmail("ops@example.com")
	require.NoError(t, m.Apply(oneRoute("b:80")))
	require.Len(t, eng.created, 2)
	require.Equal(t, 1, eng.removed)
	require.Contains(t, eng.created[1].Cmd, "--certificatesresolvers.le.acme.email=ops@example.com")

	// So is the access log.
	m.SetTrafficAnalytics(false)
	require.NoError(t, m.Apply(oneRoute("b:80")))
	require.Len(t, eng.created, 3)
	require.NotContains(t, strings.Join(eng.created[2].Cmd, " "), "--accesslog")

	// Re-applying the same settings keeps the container.
	require.NoError(t, m.Apply(oneRoute("c:80")))
	require.Len(t, eng.created, 3)
}

func TestContainerSpec(t *testing.T) {
	m, _, _ := newTestManager(t, nil)
	spec := m.desiredSpec()

	require.Equal(t, agentprotocol.ProxyContainer, spec.Name)
	require.Equal(t, TraefikImage, spec.Image)
	require.Contains(t, TraefikImage, "@sha256:", "image is pinned by digest")
	require.Equal(t, "unless-stopped", spec.RestartPolicy)
	require.Equal(t, []docker.VolumeMount{{Volume: agentprotocol.ProxyACMEVolume, Target: "/data"}}, spec.Mounts)
	require.NotNil(t, spec.LogConfig)
	require.Equal(t, "json-file", spec.LogConfig.Type)
	require.Equal(t, "10m", spec.LogConfig.Config["max-size"])
	require.Equal(t, "3", spec.LogConfig.Config["max-file"])

	// Public ports, admin on loopback, probe never published.
	require.ElementsMatch(t, []docker.PortPublish{
		{HostIP: "0.0.0.0", HostPort: 8880, ContainerPort: 80, Proto: "tcp"},
		{HostIP: "0.0.0.0", HostPort: 8443, ContainerPort: 443, Proto: "tcp"},
		{HostIP: "0.0.0.0", HostPort: 8443, ContainerPort: 443, Proto: "udp"},
		{HostIP: "127.0.0.1", HostPort: 9000, ContainerPort: 9000, Proto: "tcp"},
	}, spec.Ports)
	for _, p := range spec.Ports {
		require.NotEqual(t, agentprotocol.ProxyProbePort, p.ContainerPort)
	}
}

func TestAdminPublishRules(t *testing.T) {
	cases := []struct {
		url     string
		ip      string
		publish bool
	}{
		{"", "127.0.0.1", true},
		{"http://127.0.0.1:9000", "127.0.0.1", true},
		{"http://localhost:9000", "127.0.0.1", true},
		{"http://nebulactrl-traefik:9000", "", false},
		{"http://host2:9000", "", true},
	}
	for _, c := range cases {
		m, err := NewManager(Config{Engine: &fakeEngine{}, AdminURL: c.url})
		require.NoError(t, err)
		ip, pub := m.adminPublish()
		require.Equal(t, c.publish, pub, c.url)
		require.Equal(t, c.ip, ip, c.url)
	}
}

func TestSpecChangesWithAdminSecretAndAdminPublishing(t *testing.T) {
	a, _, _ := newTestManager(t, &memRouteStore{})
	b, _, _ := newTestManager(t, &memRouteStore{})
	require.NotEqual(t, a.adminSecret, b.adminSecret)
	require.NotEqual(t, a.desiredSpec().Labels[LabelSpec], b.desiredSpec().Labels[LabelSpec], "a new secret needs a new bootstrap file, hence a new container")

	c, err := NewManager(Config{Engine: &fakeEngine{}, HTTPPort: 8880, HTTPSPort: 8443, AdminURL: "http://nebulactrl-traefik:9000"})
	require.NoError(t, err)
	c.adminSecret = a.adminSecret
	require.NotEqual(t, a.desiredSpec().Labels[LabelSpec], c.desiredSpec().Labels[LabelSpec])
}

func TestMatches(t *testing.T) {
	m, _, _ := newTestManager(t, nil)
	spec := m.desiredSpec()
	info := docker.ContainerInfo{
		Image: spec.Image, RestartMode: spec.RestartPolicy, Labels: spec.Labels,
		Networks: map[string]docker.ContainerNetwork{agentprotocol.EdgeNetwork: {}},
	}
	require.True(t, matches(info, spec))

	old := info
	old.Labels = map[string]string{agentprotocol.LabelManaged: "true"}
	require.False(t, matches(old, spec), "a container without the spec label is recreated")

	stale := info
	stale.Image = "traefik:v3.0.0"
	require.False(t, matches(stale, spec))

	off := info
	off.Networks = nil
	require.False(t, matches(off, spec), "must be on the edge network")
}

func TestAdminSecretIsGeneratedOnceAndPersisted(t *testing.T) {
	st := &memRouteStore{}
	m1, err := NewManager(Config{Engine: &fakeEngine{}, Store: st})
	require.NoError(t, err)
	require.NotEmpty(t, m1.adminSecret)
	require.GreaterOrEqual(t, len(m1.adminSecret), 43, "32 random bytes, base64url")
	require.Equal(t, m1.adminSecret, st.secret)

	m2, err := NewManager(Config{Engine: &fakeEngine{}, Store: st})
	require.NoError(t, err)
	require.Equal(t, m1.adminSecret, m2.adminSecret)
}

func TestBootstrapFileIsWrittenBeforeFirstStart(t *testing.T) {
	m, eng, _ := newTestManager(t, nil)
	require.NoError(t, m.Apply(oneRoute("a:80")))

	require.Equal(t, "/", eng.copyDir)
	files := untar(t, eng.copied)
	body, ok := files["etc/traefik/bootstrap.yml"]
	require.True(t, ok, "bootstrap.yml at /etc/traefik/bootstrap.yml")

	re := regexp.MustCompile(`"nebulactrl-agent:([^"]+)"`)
	hash := re.FindStringSubmatch(body)
	require.Len(t, hash, 2)
	require.NoError(t, bcrypt.CompareHashAndPassword([]byte(hash[1]), []byte(m.adminSecret)), "file carries a bcrypt hash of the stored secret")
	require.NotContains(t, body, m.adminSecret, "the secret itself never lands in the container")
}

func untar(t *testing.T, b []byte) map[string]string {
	t.Helper()
	out := map[string]string{}
	tr := tar.NewReader(bytes.NewReader(b))
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return out
		}
		require.NoError(t, err)
		if h.Typeflag != tar.TypeReg {
			continue
		}
		data, err := io.ReadAll(tr)
		require.NoError(t, err)
		out[h.Name] = string(data)
	}
}
