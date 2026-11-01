//go:build integration

package integration

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/http/httptrace"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agent/decommission"
	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/internal/agent/engine"
	"github.com/nebulactrl/nebulactrl/internal/app"
	"github.com/nebulactrl/nebulactrl/internal/config"
	"github.com/nebulactrl/nebulactrl/internal/testutil"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

const (
	adminEmail    = "admin@example.com"
	adminPassword = "correct horse battery"
	canary        = "sekret-canary-12345"
)

func newCmd(ctx context.Context, dir, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	return cmd
}

func waitTCP(ctx context.Context, addr string) error {
	for {
		c, err := net.DialTimeout("tcp", addr, time.Second)
		if err == nil {
			_ = c.Close()
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("waiting for %s: %w", addr, ctx.Err())
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// StackOptions tune ExclusiveStack.
type StackOptions struct {
	// NoAgent skips starting the in-process agent (the server stays pending).
	NoAgent bool
	// ListenAll binds the control plane to 0.0.0.0 so containers can reach it.
	ListenAll bool
	// Partitionable routes the agent through a NetProxy (Stack.Net) so a test
	// can cut and heal the agent <-> control plane link.
	Partitionable bool
	// CleanupTune scopes and times the agent's Docker cleanup.
	CleanupTune engine.CleanupTuner
	// DecommissionTune scopes the agent's decommission to the test's resources.
	DecommissionTune func(*decommission.Config)
}

// Stack is a test's view of a control plane, a server and one project.
//
// SharedStack returns a view onto the one control plane, database and agent
// that TestMain shares between parallel tests; the view owns only its own
// project and environment. ExclusiveStack builds a private control plane
// (fresh schema) plus a private agent for tests that kill or restart the
// agent, recreate Traefik or change server-wide settings.
type Stack struct {
	t    testing.TB
	Pool *pgxpool.Pool
	App  *app.App
	Srv  *httptest.Server
	Opts StackOptions

	client *http.Client

	Agent     *Agent
	StateDir  string
	Fault     *ProbeFault
	Net       *NetProxy
	ServerID  string
	ProjectID string
	EnvID     string
	EnrollTok string

	shared     bool
	netSlots   int // network slots the test holds (SharedStackN)
	hostBase   string
	servicesMu sync.Mutex
	serviceIDs []string
	extraEnvs  []string // environments of further projects the test created
}

// controlPlane is an in-process control plane behind an httptest server with
// the admin user logged in on Client.
type controlPlane struct {
	App    *app.App
	Srv    *httptest.Server
	Client *http.Client
	stop   func()
}

// startControlPlane builds and starts the control plane on pool and completes
// the first-run setup (the admin session lives in Client's cookie jar).
func startControlPlane(pool *pgxpool.Pool, listenAll bool) (*controlPlane, error) {
	cfg := &config.Config{SecretKey: bytes.Repeat([]byte{9}, 32), Listen: ":0", MCPEnabled: true, Timers: harnessTimers()}
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	if os.Getenv("NEBULACTRL_TEST_CP_LOGS") != "" {
		logger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	}
	a, err := app.Build(cfg, pool, logger)
	if err != nil {
		return nil, fmt.Errorf("build control plane: %w", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	if err := a.Start(ctx); err != nil {
		cancel()
		a.Close()
		return nil, fmt.Errorf("start control plane: %w", err)
	}
	srv := httptest.NewUnstartedServer(a.Handler)
	if listenAll {
		l, err := net.Listen("tcp", "0.0.0.0:0")
		if err != nil {
			cancel()
			a.Close()
			return nil, err
		}
		srv.Listener.Close()
		srv.Listener = l
	}
	srv.Start()
	cfg.PublicURL = srv.URL

	jar, err := cookiejar.New(nil)
	if err != nil {
		srv.Close()
		cancel()
		a.Close()
		return nil, err
	}
	cp := &controlPlane{App: a, Srv: srv, Client: &http.Client{Jar: jar}}
	cp.stop = func() {
		srv.Close()
		sctx, scancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer scancel()
		_ = a.Jobs.Stop(sctx)
		cancel()
		a.Close()
	}
	status, raw, err := cp.do("POST", "/setup", map[string]any{"email": adminEmail, "password": adminPassword, "name": "Admin"})
	if err != nil || status != http.StatusCreated {
		cp.stop()
		return nil, fmt.Errorf("setup: HTTP %d err=%v: %s", status, err, raw)
	}
	return cp, nil
}

// do issues an admin-session request under /api/v1 without a *testing.T.
func (cp *controlPlane) do(method, path string, body any) (int, []byte, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, cp.Srv.URL+"/api/v1"+path, rd)
	if err != nil {
		return 0, nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := cp.Client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(res.Body)
	return res.StatusCode, raw, err
}

// ExclusiveStack builds a private control plane (fresh schema), creates a
// project and an enrollment token, and (unless NoAgent) starts and waits for a
// private agent. Use it only from tests that do not call t.Parallel: they run
// serially before the parallel group, and the shared agent is not running then.
func ExclusiveStack(t *testing.T, opts ...StackOptions) *Stack {
	t.Helper()
	requireDocker(t)
	stopSharedAgent() // a leftover shared agent would fight over Traefik
	var o StackOptions
	if len(opts) > 0 {
		o = opts[0]
	}
	pool := testutil.NewPool(t)
	cp, err := startControlPlane(pool, o.ListenAll)
	require.NoError(t, err)
	t.Cleanup(cp.stop)

	s := &Stack{
		t: t, Pool: pool, App: cp.App, Srv: cp.Srv, Opts: o, client: cp.Client, Fault: &ProbeFault{},
		hostBase: hostBase(t.Name()),
	}
	s.createProject("Itest")
	if o.Partitionable {
		s.Net, err = NewNetProxy(cp.Srv.Listener.Addr().String())
		require.NoError(t, err)
		t.Cleanup(s.Net.Close)
	}
	s.ServerID, s.EnrollTok = s.EnrollmentToken("itest-host")
	s.StateDir = t.TempDir()
	// Cleanup order (LIFO): this runs before the control plane stops.
	t.Cleanup(s.cleanupContainers)
	if !o.NoAgent {
		s.StartAgent()
		s.WaitServerOnline(60 * time.Second)
	}
	return s
}

// createProject creates a project (with its production environment) and makes
// it the stack's own.
func (s *Stack) createProject(name string) {
	s.t.Helper()
	r := s.Do("POST", "/projects", map[string]any{"name": name})
	require.Equal(s.t, http.StatusCreated, r.Status, string(r.Raw))
	s.ProjectID = r.JSON().Str("id")
	r = s.Do("GET", "/projects/"+s.ProjectID+"/environments", nil)
	require.Equal(s.t, http.StatusOK, r.Status, string(r.Raw))
	s.EnvID = r.JSON().Items()[0].Str("id")
	trackEnv(s.EnvID)
}

// URL is the control plane's base URL.
func (s *Stack) URL() string { return s.Srv.URL }

// Host returns a hostname unique to this test, `<test>-<rand>.localhost`,
// optionally prefixed with extra labels (Host("ext") is `ext.<test>-<rand>.localhost`).
// The hostname constraint is global, so parallel tests must never share one.
func (s *Stack) Host(labels ...string) string {
	return strings.Join(append(append([]string{}, labels...), s.hostBase+".localhost"), ".")
}

// StartAgent starts the in-process agent with the stack's state dir (it
// enrolls on first start, then reuses the stored identity).
func (s *Stack) StartAgent() *Agent {
	s.t.Helper()
	url := s.Srv.URL
	if s.Net != nil {
		url = s.Net.URL()
	}
	s.Agent = MustStartAgent(s.t, AgentOptions{StateDir: s.StateDir, URL: url, Token: s.EnrollTok, Fault: s.Fault, CleanupTune: s.Opts.CleanupTune, DecommissionTune: s.Opts.DecommissionTune})
	return s.Agent
}

// EnrollmentToken creates a pending server and returns (serverID, token).
func (s *Stack) EnrollmentToken(name string) (string, string) {
	s.t.Helper()
	r := s.Do("POST", "/servers/enrollment-tokens", map[string]any{"name": name})
	require.Equal(s.t, http.StatusCreated, r.Status, string(r.Raw))
	j := r.JSON()
	return j.Obj("server").Str("id"), j.Str("token")
}

// ---- REST ----------------------------------------------------------------

// J is a decoded JSON object with typed accessors.
type J map[string]any

func (j J) Str(k string) string {
	v, _ := j[k].(string)
	return v
}

func (j J) Obj(k string) J {
	v, _ := j[k].(map[string]any)
	return J(v)
}

func (j J) Int(k string) int {
	v, _ := j[k].(float64)
	return int(v)
}

func (j J) Bool(k string) bool {
	v, _ := j[k].(bool)
	return v
}

func (j J) Items() []J {
	arr, _ := j["items"].([]any)
	out := make([]J, 0, len(arr))
	for _, v := range arr {
		m, _ := v.(map[string]any)
		out = append(out, J(m))
	}
	return out
}

// Resp is an HTTP response.
type Resp struct {
	Status int
	Header http.Header
	Raw    []byte
}

func (r Resp) JSON() J {
	var m map[string]any
	if err := json.Unmarshal(r.Raw, &m); err != nil {
		panic(fmt.Sprintf("not a JSON object (HTTP %d): %s", r.Status, r.Raw))
	}
	return J(m)
}

// Do issues a session-authenticated request under /api/v1.
func (s *Stack) Do(method, path string, body any, headers ...string) Resp {
	s.t.Helper()
	return s.do(s.client, "", method, path, body, headers...)
}

// DoToken issues a request authenticated by a bearer API token only.
func (s *Stack) DoToken(token, method, path string, body any, headers ...string) Resp {
	s.t.Helper()
	return s.do(&http.Client{}, token, method, path, body, headers...)
}

func (s *Stack) do(c *http.Client, token, method, path string, body any, headers ...string) Resp {
	s.t.Helper()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		require.NoError(s.t, err)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, s.Srv.URL+"/api/v1"+path, rd)
	require.NoError(s.t, err)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	res, err := c.Do(req)
	require.NoError(s.t, err)
	defer res.Body.Close()
	raw, err := io.ReadAll(res.Body)
	require.NoError(s.t, err)
	return Resp{Status: res.StatusCode, Header: res.Header, Raw: raw}
}

// Token creates a REST API token (session-authenticated) and returns it. The
// stack's tests deploy to production environments, so it skips the approval a
// REST token otherwise needs there (the session user is the instance admin).
func (s *Stack) Token(scopes []string, projectIDs []string) string {
	return s.newToken("rest", scopes, projectIDs)
}

// MCPToken creates an MCP token: it works only on /mcp, with the production
// approval flow on.
func (s *Stack) MCPToken(scopes []string, projectIDs []string) string {
	return s.newToken("mcp", scopes, projectIDs)
}

func (s *Stack) newToken(kind string, scopes []string, projectIDs []string) string {
	s.t.Helper()
	body := map[string]any{"name": "itest", "kind": kind, "scopes": scopes}
	if kind == "rest" {
		body["allowProductionWithoutApproval"] = true
	}
	if projectIDs != nil {
		body["projectIds"] = projectIDs
	}
	r := s.Do("POST", "/tokens", body)
	require.Equal(s.t, http.StatusCreated, r.Status, string(r.Raw))
	return r.JSON().Str("token")
}

// ---- servers -------------------------------------------------------------

// WaitServerOnline waits until the server is online with the proxy ready.
func (s *Stack) WaitServerOnline(timeout time.Duration) J {
	return s.WaitServer(s.ServerID, timeout)
}

// WaitServer waits until serverID is online and its proxy status is ok.
func (s *Stack) WaitServer(serverID string, timeout time.Duration) J {
	s.t.Helper()
	var last J
	ok := eventually(timeout, 500*time.Millisecond, func() bool {
		r := s.Do("GET", "/servers/"+serverID, nil)
		if r.Status != http.StatusOK {
			return false
		}
		last = r.JSON()
		return last.Str("status") == "online" && last.Obj("proxy").Str("status") == agentprotocol.ProxyOK
	})
	require.True(s.t, ok, "server %s did not become online with a healthy proxy: %v", serverID, last)
	return last
}

func eventually(timeout, every time.Duration, fn func() bool) bool {
	deadline := time.Now().Add(timeout)
	for {
		if fn() {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(every)
	}
}

// ---- services & deployments ---------------------------------------------

// SvcSpec describes a service to create.
type SvcSpec struct {
	Name      string
	ServerID  string // default: the stack's server
	Image     string // image source when Git is empty
	GitURL    string
	GitBranch string
	Port      int
	Health    map[string]any // healthcheck overrides
	Vars      map[string]string
	Secrets   map[string]string
	Domain    string
	Env       string // environment id (default: stack env)
	Config    map[string]any
}

// Service is a created service.
type Service struct {
	ID, Slug string
}

// NewService creates a service (plus variables and an optional domain).
func (s *Stack) NewService(sp SvcSpec) Service {
	s.t.Helper()
	if sp.ServerID == "" {
		sp.ServerID = s.ServerID
	}
	if sp.Env == "" {
		sp.Env = s.EnvID
	}
	if sp.Port == 0 {
		sp.Port = 8080
	}
	// 1 s probes with a single success keep deploys fast; this lives in the test
	// service config, not in a product default.
	hc := map[string]any{"type": "http", "path": "/health", "intervalSeconds": 1, "deadlineSeconds": 30, "successThreshold": 1}
	for k, v := range sp.Health {
		hc[k] = v
	}
	cfg := map[string]any{"port": sp.Port, "healthcheck": hc, "stopGraceSeconds": 5}
	body := map[string]any{"name": sp.Name, "serverId": sp.ServerID}
	if sp.GitURL != "" {
		body["sourceType"] = "git"
		cfg["gitUrl"] = sp.GitURL
		cfg["gitBranch"] = firstNonEmpty(sp.GitBranch, "main")
	} else {
		body["sourceType"] = "image"
		cfg["image"] = sp.Image
	}
	for k, v := range sp.Config {
		cfg[k] = v
	}
	body["config"] = cfg
	r := s.Do("POST", "/environments/"+sp.Env+"/services", body)
	require.Equal(s.t, http.StatusCreated, r.Status, string(r.Raw))
	svc := Service{ID: r.JSON().Str("id"), Slug: r.JSON().Str("slug")}
	s.servicesMu.Lock()
	s.serviceIDs = append(s.serviceIDs, svc.ID)
	s.servicesMu.Unlock()

	if len(sp.Vars)+len(sp.Secrets) > 0 {
		s.SetVars(svc.ID, sp.Vars, sp.Secrets)
	}
	if sp.Domain != "" {
		s.AddDomain(svc.ID, sp.Domain)
	}
	return svc
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// SetVars upserts plain and secret variables.
func (s *Stack) SetVars(serviceID string, plain, secrets map[string]string) {
	s.t.Helper()
	var vars []map[string]any
	for k, v := range plain {
		vars = append(vars, map[string]any{"key": k, "value": v, "isSecret": false})
	}
	for k, v := range secrets {
		vars = append(vars, map[string]any{"key": k, "value": v, "isSecret": true})
	}
	r := s.Do("PUT", "/services/"+serviceID+"/variables", map[string]any{"variables": vars})
	require.Equal(s.t, http.StatusOK, r.Status, string(r.Raw))
}

// AddDomain attaches hostname to the service (targets the service port).
func (s *Stack) AddDomain(serviceID, hostname string) {
	s.t.Helper()
	r := s.Do("POST", "/services/"+serviceID+"/domains", map[string]any{"hostname": hostname})
	require.Equal(s.t, http.StatusCreated, r.Status, string(r.Raw))
}

// Deploy requests a deployment and returns its ID.
func (s *Stack) Deploy(serviceID string) string {
	s.t.Helper()
	r := s.Do("POST", "/services/"+serviceID+"/deployments", map[string]any{})
	require.Equal(s.t, http.StatusAccepted, r.Status, string(r.Raw))
	return r.JSON().Str("id")
}

// Terminal deployment statuses.
func terminal(status string) bool {
	switch status {
	case "succeeded", "failed", "canceled", "rolled_back", "rollback_failed":
		return true
	}
	return false
}

// Deployment fetches a deployment.
func (s *Stack) Deployment(id string) J {
	s.t.Helper()
	r := s.Do("GET", "/deployments/"+id, nil)
	require.Equal(s.t, http.StatusOK, r.Status, string(r.Raw))
	return r.JSON()
}

// WaitDeployment waits for a terminal status and returns the deployment.
func (s *Stack) WaitDeployment(id string, timeout time.Duration) J {
	s.t.Helper()
	var d J
	ok := eventually(timeout, 500*time.Millisecond, func() bool {
		r := s.Do("GET", "/deployments/"+id, nil)
		if r.Status != http.StatusOK {
			return false
		}
		d = r.JSON()
		return terminal(d.Str("status"))
	})
	require.True(s.t, ok, "deployment %s did not finish within %s: %v", id, timeout, d)
	return d
}

// DeployOK deploys and requires success.
func (s *Stack) DeployOK(serviceID string) J {
	s.t.Helper()
	d := s.WaitDeployment(s.Deploy(serviceID), 3*time.Minute)
	require.Equal(s.t, "succeeded", d.Str("status"), "deployment: %v\nlogs:\n%s", d, s.DeploymentLogs(d.Str("id")))
	return d
}

// DeploymentLogs returns the persisted log lines of a deployment (all streams).
func (s *Stack) DeploymentLogs(id string) string {
	s.t.Helper()
	r := s.Do("GET", "/deployments/"+id+"/logs", nil)
	if r.Status != http.StatusOK {
		return fmt.Sprintf("(logs HTTP %d: %s)", r.Status, r.Raw)
	}
	var sb strings.Builder
	for _, it := range r.JSON().Items() {
		fmt.Fprintf(&sb, "[%s] %s\n", it.Str("stream"), it.Str("line"))
	}
	return sb.String()
}

// Service fetches a service.
func (s *Stack) Service(id string) J {
	s.t.Helper()
	r := s.Do("GET", "/services/"+id, nil)
	require.Equal(s.t, http.StatusOK, r.Status, string(r.Raw))
	return r.JSON()
}

// ---- docker --------------------------------------------------------------

// Containers lists every container (any state) of a service.
func (s *Stack) Containers(serviceID string) []docker.ContainerInfo {
	s.t.Helper()
	cs, err := dockerEng.ContainerList(context.Background(), docker.ListFilter{
		All: true, Labels: map[string]string{agentprotocol.LabelService: serviceID},
	})
	require.NoError(s.t, err)
	return cs
}

// RunningContainers lists the running containers of a service.
func (s *Stack) RunningContainers(serviceID string) []docker.ContainerInfo {
	var out []docker.ContainerInfo
	for _, c := range s.Containers(serviceID) {
		if c.Running {
			out = append(out, c)
		}
	}
	return out
}

func (s *Stack) cleanupContainers() {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	s.servicesMu.Lock()
	ids := append([]string(nil), s.serviceIDs...)
	extra := append([]string(nil), s.extraEnvs...)
	s.servicesMu.Unlock()
	// Services created through other paths (e.g. reconcile redeploys) carry the
	// same service ID, so listing by service ID is complete. The services of an
	// environment the test tracked (a preview's, created by the control plane
	// itself) are found by the environment label.
	envs := map[string]bool{s.EnvID: true}
	for _, e := range extra {
		envs[e] = true
		cs, err := dockerEng.ContainerList(ctx, docker.ListFilter{All: true, Labels: map[string]string{agentprotocol.LabelEnvironment: e}})
		if err != nil {
			continue
		}
		for _, c := range cs {
			if id := c.Labels[agentprotocol.LabelService]; id != "" && !slices.Contains(ids, id) {
				ids = append(ids, id)
			}
		}
	}
	for _, id := range ids {
		cs, err := dockerEng.ContainerList(ctx, docker.ListFilter{All: true, Labels: map[string]string{agentprotocol.LabelService: id}})
		if err != nil {
			continue
		}
		for _, c := range cs {
			if e := c.Labels[agentprotocol.LabelEnvironment]; e != "" {
				envs[e] = true
			}
			_ = dockerEng.ContainerRemove(ctx, c.ID, true, false)
		}
		// Volumes outlive their containers; database services leave data behind.
		if vols, err := sh(ctx, "", "docker", "volume", "ls", "-q", "--filter", "name=^"+agentprotocol.VolumePrefix+agentprotocol.Short(id)+"-"); err == nil {
			for _, v := range strings.Fields(vols) {
				_, _ = sh(ctx, "", "docker", "volume", "rm", "-f", v)
			}
		}
	}
	// Every environment gets its own bridge network; Docker's address pools are
	// finite, so a test run must not leave them behind. Networks that are still
	// in use fail to remove, which is fine.
	for e := range envs {
		removeEnvNetworks(ctx, e)
	}
}

// ---- traffic through Traefik --------------------------------------------

// ProxyClient talks to the harness Traefik on 127.0.0.1 with SNI/Host = host.
type ProxyClient struct {
	c     *http.Client
	host  string
	https string
	UA    string
}

// NewProxyClient returns a client for https://<host>/ served by the proxy.
func NewProxyClient(host string) *ProxyClient {
	addr := net.JoinHostPort("127.0.0.1", fmt.Sprint(proxyPorts.HTTPS))
	tr := &http.Transport{
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, network, addr)
		},
		TLSClientConfig:   &tls.Config{InsecureSkipVerify: true, ServerName: host}, //nolint:gosec // test-only self-signed default cert
		DisableKeepAlives: true,
	}
	return &ProxyClient{c: &http.Client{Transport: tr, Timeout: 5 * time.Second}, host: host, https: addr}
}

// Get requests path and returns (status, body).
func (p *ProxyClient) Get(path string) (int, string, error) {
	code, body, _, err := p.GetTimed(path)
	return code, body, err
}

// Wire is when one request crossed the wire: Wrote is when its last byte was
// handed to the socket, First when the first byte of the answer came back. The
// proxy routes a request somewhere in between, so a request written after
// another one answered was routed after that answer was produced.
type Wire struct {
	Wrote, First time.Time
}

// GetTimed is Get that also reports when the request was written and answered.
func (p *ProxyClient) GetTimed(path string) (int, string, Wire, error) {
	var (
		mu sync.Mutex // the trace hooks run on the transport's goroutines
		w  Wire
	)
	snapshot := func() Wire {
		mu.Lock()
		defer mu.Unlock()
		return w
	}
	req, err := http.NewRequest(http.MethodGet, "https://"+p.host+path, nil)
	if err != nil {
		return 0, "", Wire{}, err
	}
	req = req.WithContext(httptrace.WithClientTrace(req.Context(), &httptrace.ClientTrace{
		WroteRequest: func(httptrace.WroteRequestInfo) {
			mu.Lock()
			w.Wrote = time.Now()
			mu.Unlock()
		},
		GotFirstResponseByte: func() {
			mu.Lock()
			w.First = time.Now()
			mu.Unlock()
		},
	}))
	if p.UA != "" {
		req.Header.Set("User-Agent", p.UA)
	}
	res, err := p.c.Do(req)
	if err != nil {
		return 0, "", snapshot(), err
	}
	defer res.Body.Close()
	b, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	return res.StatusCode, strings.TrimSpace(string(b)), snapshot(), err
}

// GetRetry is Get that retries a failed connection a few times. Traefik's
// published ports blink for a moment whenever Docker attaches or detaches a
// network (any parallel test deploying or cleaning up), which is no concern of a
// test that only needs an answer. Tests that assert on every request use Get.
func (p *ProxyClient) GetRetry(path string) (int, string, error) {
	var (
		code int
		body string
		err  error
	)
	for range 5 {
		if code, body, err = p.Get(path); err == nil {
			return code, body, nil
		}
		time.Sleep(150 * time.Millisecond)
	}
	return code, body, err
}

// WaitBody waits until GET path answers 200 with exactly want.
func (p *ProxyClient) WaitBody(t testing.TB, path, want string, timeout time.Duration) {
	t.Helper()
	var code int
	var body string
	var err error
	ok := eventually(timeout, 300*time.Millisecond, func() bool {
		code, body, err = p.Get(path)
		return err == nil && code == 200 && body == want
	})
	require.True(t, ok, "GET %s: want 200 %q, last: %d %q err=%v", path, want, code, body, err)
}

// LoadResult summarizes a background request load.
type LoadResult struct {
	Total    int
	Non2xx   int
	Errors   int
	Statuses map[int]int  // HTTP status → count, every answered request
	Bodies   []string     // body of every successful request, in order
	Answers  []LoadAnswer // the same requests with their wire timing, in order
	Failures []string     // first few failures, for diagnostics
}

// LoadAnswer is one successful request of a Load: its position in the tick
// schedule, the body and when it crossed the wire.
type LoadAnswer struct {
	Index int
	Body  string
	Wire
}

// StaleAfterSwitch checks the promise of a traffic switch from old to next: once
// next has answered, the proxy sends no new request to old. Requests of a Load
// are concurrent, each on its own connection, so their tick order is not their
// arrival order (a slow TLS handshake lets a later tick overtake an earlier
// one). What is ordered is wall time: the proxy had switched when the first byte
// of the first next answer was received, so any request whose last byte was
// written after that arrived after the switch and must not be answered by old.
// It returns that moment and the old answers that violate this.
func StaleAfterSwitch(answers []LoadAnswer, old, next string) (time.Time, []LoadAnswer) {
	var switched time.Time
	for _, a := range answers {
		if a.Body == next && (switched.IsZero() || a.First.Before(switched)) {
			switched = a.First
		}
	}
	var stale []LoadAnswer
	if switched.IsZero() {
		return switched, nil
	}
	for _, a := range answers {
		if a.Body == old && a.Wrote.After(switched) {
			stale = append(stale, a)
		}
	}
	return switched, stale
}

// Summary is the failure detail for assertion messages: the status histogram
// and the first failures (send offset, status, start of the body).
func (r LoadResult) Summary() string {
	return fmt.Sprintf("statuses=%v errors=%d first failures=%q", r.Statuses, r.Errors, r.Failures)
}

// Load sends GET path every interval until Stop, then reports.
type Load struct {
	stop      chan struct{}
	done      chan struct{}
	res       LoadResult
	completed atomic.Int64
}

// WaitMore blocks until n more requests have completed (answered or failed),
// so a test waits for traffic to flow instead of sleeping for a fixed time.
func (l *Load) WaitMore(t testing.TB, n int, timeout time.Duration) {
	t.Helper()
	target := l.completed.Load() + int64(n)
	require.True(t, eventually(timeout, 20*time.Millisecond, func() bool { return l.completed.Load() >= target }),
		"the load generator completed only %d of %d further requests", l.completed.Load()-(target-int64(n)), n)
}

type loadSample struct {
	code int
	body string
	err  error
	at   time.Duration // tick time since the load started
	wire Wire
}

// StartLoad begins a request load against path. Requests are issued on a fixed
// schedule (one new request per tick, concurrently), so a slow response never
// hides later failures.
func StartLoad(p *ProxyClient, path string, interval time.Duration) *Load {
	l := &Load{stop: make(chan struct{}), done: make(chan struct{})}
	started := time.Now()
	go func() {
		defer close(l.done)
		var (
			wg      sync.WaitGroup
			mu      sync.Mutex
			samples []loadSample
		)
		tick := time.NewTicker(interval)
		defer tick.Stop()
		for stopped := false; !stopped; {
			select {
			case <-l.stop:
				stopped = true
			case <-tick.C:
				mu.Lock()
				idx := len(samples)
				at := time.Since(started)
				samples = append(samples, loadSample{at: at})
				mu.Unlock()
				wg.Add(1)
				go func() {
					defer wg.Done()
					code, body, wire, err := p.GetTimed(path)
					mu.Lock()
					samples[idx] = loadSample{code: code, body: body, err: err, at: at, wire: wire}
					mu.Unlock()
					l.completed.Add(1)
				}()
			}
		}
		wg.Wait()
		l.res.Statuses = map[int]int{}
		for _, sm := range samples { // send order
			l.res.Total++
			if sm.err == nil {
				l.res.Statuses[sm.code]++
			}
			switch {
			case sm.err != nil:
				l.res.Errors++
				if len(l.res.Failures) < 5 {
					l.res.Failures = append(l.res.Failures, fmt.Sprintf("+%s: %s", sm.at.Round(time.Millisecond), sm.err))
				}
			case sm.code < 200 || sm.code > 299:
				l.res.Non2xx++
				if len(l.res.Failures) < 5 {
					// Error pages are large single-line HTML; keep the start only.
					b := sm.body
					if len(b) > 160 {
						b = b[:160] + "…"
					}
					l.res.Failures = append(l.res.Failures, fmt.Sprintf("+%s: HTTP %d %q", sm.at.Round(time.Millisecond), sm.code, b))
				}
			default:
				l.res.Bodies = append(l.res.Bodies, sm.body)
				l.res.Answers = append(l.res.Answers, LoadAnswer{Index: l.res.Total - 1, Body: sm.body, Wire: sm.wire})
			}
		}
	}()
	return l
}

// Stop ends the load and returns the result.
func (l *Load) Stop() LoadResult {
	close(l.stop)
	<-l.done
	return l.res
}

// ---- helpers for assertions ----------------------------------------------

// dumpAllTables returns the text of every row of every table in the test schema
// (bytea as hex), so secrets can be searched for anywhere.
func (s *Stack) dumpAllTables(skip ...string) string {
	return s.dumpTables(nil, skip...)
}

// dumpTables is dumpAllTables restricted to only (when non-empty).
func (s *Stack) dumpTables(only []string, skip ...string) string {
	s.t.Helper()
	ctx := context.Background()
	rows, err := s.Pool.Query(ctx, `SELECT tablename FROM pg_tables WHERE schemaname = current_schema()`)
	require.NoError(s.t, err)
	var tables []string
	for rows.Next() {
		var n string
		require.NoError(s.t, rows.Scan(&n))
		tables = append(tables, n)
	}
	rows.Close()
	require.NoError(s.t, rows.Err())
	var sb strings.Builder
outer:
	for _, tb := range tables {
		if len(only) > 0 && !contains(only, tb) {
			continue
		}
		for _, sk := range skip {
			if tb == sk {
				continue outer
			}
		}
		var dump *string
		q := fmt.Sprintf(`SELECT string_agg(t::text, E'\n') FROM %q t`, tb)
		require.NoError(s.t, s.Pool.QueryRow(ctx, q).Scan(&dump), tb)
		if dump != nil {
			fmt.Fprintf(&sb, "-- %s\n%s\n", tb, *dump)
		}
	}
	return sb.String()
}

func contains(xs []string, v string) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}
