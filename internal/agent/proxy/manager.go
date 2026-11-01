// Package proxy manages the per-host Traefik reverse proxy (ADR 0003): it
// bootstraps the nebulactrl-traefik container on the edge network and pushes
// complete route tables through Traefik's REST provider.
package proxy

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// TraefikImage is the pinned Traefik v3 image (v3.7.16, multi-arch index
// digest). v3.6.17 / v3.7.1 and later carry the fix for GHSA-96qj-4jj5-wcjc
// (rest@internal exposure).
const TraefikImage = "traefik:v3.7.16@sha256:c8a121e4bb1347e8fd52657fab12f2cb70b68f8a018895e2068f92c9e519b9c2"

// LabelSpec carries the fingerprint of the container's static configuration.
const LabelSpec = "nebulactrl.proxy.spec"

// Proxy container log rotation (json-file driver).
const (
	LogMaxSize = "10m"
	LogMaxFile = "3"
)

const (
	checkInterval     = 5 * time.Second
	retryInterval     = 15 * time.Second
	readyTimeout      = 45 * time.Second
	applyLiveTimeout  = 15 * time.Second
	applyPollInterval = 50 * time.Millisecond

	// adminRetryWindow bounds how long a push waits for an admin API that is
	// down (Traefik restarting after a crash, a daemon restart or a recreate).
	adminRetryWindow  = 30 * time.Second
	adminRetryInitial = 200 * time.Millisecond
	adminRetryMax     = 2 * time.Second
)

// RouteStore persists the last-applied route table and proxy settings.
// *store.Store implements it.
type RouteStore interface {
	SaveRoutes([]agentprotocol.Route) error
	LoadRoutes() ([]agentprotocol.Route, error)
	SaveACMEEmail(string) error
	LoadACMEEmail() (string, error)
	SaveTrafficAnalytics(bool) error
	// LoadTrafficAnalytics returns the persisted setting; ok is false when none was saved.
	LoadTrafficAnalytics() (enabled, ok bool, err error)
	SaveProxyAdminSecret(string) error
	LoadProxyAdminSecret() (string, error)
	SaveProxyConfig(agentprotocol.ProxyConfig) error
	// LoadProxyConfig returns the persisted proxy configuration; ok is false
	// when none was saved.
	LoadProxyConfig() (cfg agentprotocol.ProxyConfig, ok bool, err error)
}

// Config configures a Manager.
type Config struct {
	Engine docker.Engine
	Store  RouteStore // optional; nil keeps state in memory only
	// AdminURL is the admin entrypoint as seen from the agent. The default is
	// http://127.0.0.1:<AdminPort>, served by the port the container publishes
	// on the host loopback. A URL whose host is the proxy container itself
	// (http://nebulactrl-traefik:9000, for an agent running in a container on
	// the edge network) needs no published port; any other non-loopback host
	// publishes the port on all interfaces of the Docker host.
	AdminURL  string
	AdminPort int // host port of the published admin entrypoint; default 9000
	HTTPPort  int // host port mapped to :80
	HTTPSPort int // host port mapped to :443 (tcp+udp)
	Logger    *slog.Logger
	// HTTPClient is optional, for tests.
	HTTPClient *http.Client
	// HostTailscale reports a Tailscale instance running on the host itself
	// (tailnet mode host). Optional.
	HostTailscale func() *agentprotocol.HostTailscale
	// Pages runs the pages server (maintenance and error pages) next to Traefik
	// and adds the errors middleware and catch-all 404 to the route table.
	Pages bool
	// PortInUse reports whether a process of the host holds a port (proto tcp
	// or udp). Optional: the default tries to bind it. Tests replace it.
	PortInUse func(proto string, port int) bool
}

// Manager owns the Traefik container and its dynamic config.
type Manager struct {
	cfg  Config
	log  *slog.Logger
	http *http.Client

	adminURL    string
	adminSecret string

	// applyMu serializes everything that touches the proxy container or its
	// dynamic config: Apply, Bootstrap, the health check, and with them every
	// create, recreate, restore, network join and route push. An Apply never
	// runs while the container is being replaced.
	applyMu sync.Mutex

	// Admin retry timings (adminRetry* constants; shortened by tests).
	adminRetryWindow, adminRetryInitial, adminRetryMax time.Duration

	mu        sync.RWMutex
	routes    []agentprotocol.Route
	acmeEmail string
	// trafficAnalytics enables Traefik's access log; on by default (the control
	// plane's server setting overrides it). Changing it recreates the container.
	trafficAnalytics bool
	status           agentprotocol.ProxyHealth
	gen              int64 // generation of the live dynamic config; 0 = none pushed yet

	// proxyCfg is the server-level configuration (certificates with resolved
	// keys, upstream policy, tailnet spec) the next Apply uses; appliedCfg is
	// what the last successful Apply ran with and is restored when an Apply fails.
	proxyCfg   agentprotocol.ProxyConfig
	appliedCfg agentprotocol.ProxyConfig
	// applied is the container-defining inputs of the last successful Apply
	// (nil until known); a failed recreate restores the container it describes.
	applied *specInputs
	// last is the report of the most recent Apply (LastApply).
	last agentprotocol.ProxyConfigureResult
	// tailnetKey is the one-time auth key of the pending provisioning; memory only.
	tailnetKey string
	tn         tailnetState
	// pagesOK is true while the pages container runs the content of the live
	// table; pages is its content (applyMu).
	pagesOK bool
	pages   pagesState
	// acme is the observed DNS-01 certificate state (mu); acmeMu serializes
	// its background refresh.
	acme   acmeState
	acmeMu sync.Mutex
	// portsMu guards the cached probe of the well-known web ports (Ports).
	portsMu sync.Mutex
	busy    []int
	busyAt  time.Time
	busyFor [2]int
	// edge is what the last apply saw of the edge network (edge.go).
	edge edgeState
}

// NewManager builds a Manager and loads any persisted route table, settings
// and admin secret (generating the secret on first use).
func NewManager(cfg Config) (*Manager, error) {
	if cfg.Engine == nil {
		return nil, errors.New("proxy: engine is required")
	}
	if cfg.AdminPort == 0 {
		cfg.AdminPort = agentprotocol.ProxyAdminPort
	}
	if cfg.AdminURL == "" {
		cfg.AdminURL = "http://127.0.0.1:" + strconv.Itoa(cfg.AdminPort)
	}
	if _, err := url.Parse(cfg.AdminURL); err != nil {
		return nil, fmt.Errorf("proxy: invalid admin url %q: %w", cfg.AdminURL, err)
	}
	if cfg.HTTPPort == 0 {
		cfg.HTTPPort = 80
	}
	if cfg.HTTPSPort == 0 {
		cfg.HTTPSPort = 443
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{
			Timeout: 30 * time.Second,
			// A probe must see the upstream's own status, never a followed redirect.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}
	}
	m := &Manager{
		cfg:      cfg,
		log:      cfg.Logger.With("component", "proxy"),
		http:     hc,
		adminURL: strings.TrimRight(cfg.AdminURL, "/"),
		status:   agentprotocol.ProxyHealth{Status: agentprotocol.ProxyStarting},

		trafficAnalytics: true,
		proxyCfg:         agentprotocol.ProxyConfig{UpstreamPolicy: agentprotocol.DefaultUpstreamPolicy()},

		adminRetryWindow: adminRetryWindow, adminRetryInitial: adminRetryInitial, adminRetryMax: adminRetryMax,
	}
	if cfg.Store != nil {
		routes, err := cfg.Store.LoadRoutes()
		if err != nil {
			return nil, fmt.Errorf("load routes: %w", err)
		}
		m.routes = dropLegacyRoutes(routes, m.log)
		if m.acmeEmail, err = cfg.Store.LoadACMEEmail(); err != nil {
			return nil, fmt.Errorf("load acme email: %w", err)
		}
		enabled, ok, err := cfg.Store.LoadTrafficAnalytics()
		if err != nil {
			return nil, fmt.Errorf("load traffic analytics setting: %w", err)
		}
		if ok {
			m.trafficAnalytics = enabled
		}
		if m.adminSecret, err = cfg.Store.LoadProxyAdminSecret(); err != nil {
			return nil, fmt.Errorf("load proxy admin secret: %w", err)
		}
		pc, ok, err := cfg.Store.LoadProxyConfig()
		if err != nil {
			return nil, fmt.Errorf("load proxy config: %w", err)
		}
		if ok {
			m.proxyCfg = cloneProxyConfig(pc)
			if t := m.proxyCfg.Tailnet; t != nil {
				t.Provision, t.Remove = false, false
			}
		}
		if ok || len(m.routes) > 0 {
			in := m.inputsFor(m.routes, m.proxyCfg)
			m.applied = &in
		}
	}
	m.appliedCfg = cloneProxyConfig(m.proxyCfg)
	if m.adminSecret == "" {
		raw := make([]byte, 32)
		if _, err := rand.Read(raw); err != nil {
			return nil, fmt.Errorf("generate proxy admin secret: %w", err)
		}
		m.adminSecret = base64.RawURLEncoding.EncodeToString(raw)
		if cfg.Store != nil {
			if err := cfg.Store.SaveProxyAdminSecret(m.adminSecret); err != nil {
				return nil, fmt.Errorf("persist proxy admin secret: %w", err)
			}
		}
	}
	return m, nil
}

// Status is the proxy health reported in heartbeats, with the ports the proxy
// runs on.
func (m *Manager) Status() agentprotocol.ProxyHealth {
	m.mu.RLock()
	s := m.status
	m.mu.RUnlock()
	ports := m.Ports()
	s.Ports = &ports
	return s
}

func (m *Manager) setStatus(status, reason string) {
	m.mu.Lock()
	m.status = agentprotocol.ProxyHealth{Status: status, Error: reason}
	m.mu.Unlock()
}

// Current returns a copy of the last-applied route table.
func (m *Manager) Current() []agentprotocol.Route {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return cloneRoutes(m.routes)
}

// SetACMEEmail sets the ACME contact email. It is static configuration: a
// change recreates the container on the next Apply.
func (m *Manager) SetACMEEmail(email string) {
	m.mu.Lock()
	m.acmeEmail = email
	m.mu.Unlock()
}

// SetTrafficAnalytics turns Traefik's access log on or off. It is static
// configuration: a change recreates the container on the next Apply.
func (m *Manager) SetTrafficAnalytics(enabled bool) {
	m.mu.Lock()
	m.trafficAnalytics = enabled
	m.mu.Unlock()
}

// TrafficAnalytics reports whether access logging is enabled.
func (m *Manager) TrafficAnalytics() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.trafficAnalytics
}

// ACMEEmail returns the configured ACME contact email.
func (m *Manager) ACMEEmail() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.acmeEmail
}

// dropLegacyRoutes discards persisted routes written before replicas existed
// (single "upstream" field, no upstreams): they cannot be rendered, and the
// control plane's reconcile on hello restores the real table.
func dropLegacyRoutes(in []agentprotocol.Route, log *slog.Logger) []agentprotocol.Route {
	out := in[:0:0]
	for _, r := range in {
		if len(r.Upstreams) == 0 {
			log.Warn("dropping persisted route without upstreams (pre-replica format)", "service_id", r.ServiceID)
			continue
		}
		out = append(out, r)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func cloneRoutes(in []agentprotocol.Route) []agentprotocol.Route {
	if in == nil {
		return nil
	}
	out := make([]agentprotocol.Route, len(in))
	for i, r := range in {
		out[i] = r
		out[i].Hosts = append([]string(nil), r.Hosts...)
		out[i].Upstreams = append([]string(nil), r.Upstreams...)
		out[i].Networks = append([]string(nil), r.Networks...)
		out[i].AllowCIDRs = append([]string(nil), r.AllowCIDRs...)
		out[i].DenyCIDRs = append([]string(nil), r.DenyCIDRs...)
		out[i].Domains = append([]agentprotocol.DomainRule(nil), r.Domains...)
	}
	return out
}

// nextGeneration returns a generation number greater than every earlier one
// (millisecond clock, bumped when two pushes share a millisecond).
func (m *Manager) nextGeneration() int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := time.Now().UnixMilli()
	if n <= m.gen {
		n = m.gen + 1
	}
	return n
}

// Apply pushes the complete route table (PUT /api/providers/rest) and returns
// once Traefik serves it, or fails. Traefik applies provider configs
// asynchronously, so Apply waits for the generation marker router to answer on
// the probe entrypoint and then reads router statuses: a config Traefik
// accepted but could not build (for example a bad upstream URL) is rejected
// and the previous table is pushed again. On success the table is remembered
// in memory and persisted.
func (m *Manager) Apply(routes []agentprotocol.Route) error {
	m.applyMu.Lock()
	defer m.applyMu.Unlock()
	return m.applyLocked(context.Background(), routes)
}

func (m *Manager) applyLocked(ctx context.Context, routes []agentprotocol.Route) error {
	cfg := m.ProxyConfig()
	// Whether the edge network has IPv6 decides how the public ports are bound
	// (edge.go).
	if err := m.refreshEdge(ctx); err != nil {
		m.revertConfig()
		return err
	}
	if err := m.checkRoutes(ctx, routes, cfg); err != nil {
		m.revertConfig()
		return err
	}
	email := m.ACMEEmail()
	traffic := m.TrafficAnalytics()
	usable, certStatus := CertStatuses(cfg.Certificates)
	in := m.inputsFor(routes, cfg)

	// Static settings changed (ACME email, access log, L4 ports, host gateway,
	// tailnet DNS, the trusted proxy ranges of an origin lock) or the container
	// is gone: recreate it first. Its dynamic
	// config starts empty either way. A recreate that fails to start puts the
	// previous container back (ensureContainer).
	recreated, err := m.ensureContainer(ctx, in)
	if err != nil {
		var restored *restoredError
		if errors.As(err, &restored) {
			// The previous container runs again with an empty dynamic config.
			if perr := m.pushApplied(ctx, m.Current(), m.applied.ACMEEmail); perr != nil {
				err = fmt.Errorf("%w (re-applying the previous route table also failed: %v)", err, perr)
			}
		}
		m.revertConfig()
		return err
	}

	prev := m.Current()
	err = m.syncNetworks(ctx, routes, false)
	if err == nil {
		err = m.preparePages(ctx, routes)
	}
	if err == nil {
		err = m.push(ctx, routes, email, usable, cfg)
	}
	if err != nil {
		m.log.Warn("route table rejected, restoring previous", "err", err)
		prevEmail := email
		if recreated {
			// The new container may lack entrypoints or resolvers the previous
			// table needs: bring the previous container back first.
			var rerr error
			if prevEmail, rerr = m.restoreContainer(ctx, in); rerr != nil {
				err = fmt.Errorf("%w (restoring the previous proxy container also failed: %v)", err, rerr)
			}
		}
		if rerr := m.pushApplied(ctx, prev, prevEmail); rerr != nil {
			err = fmt.Errorf("%w (restoring the previous route table also failed: %v)", err, rerr)
		}
		m.revertConfig()
		// The proxy may have joined networks only the rejected table needs.
		if nerr := m.syncNetworks(ctx, prev, true); nerr != nil {
			m.log.Warn("restore proxy networks", "err", nerr)
		}
		return err
	}
	m.finishPages(ctx, routes)
	// Leave the networks no route needs any more (the new table is live).
	if nerr := m.syncNetworks(ctx, routes, true); nerr != nil {
		m.log.Warn("prune proxy networks", "err", nerr)
	}

	m.mu.Lock()
	m.routes = cloneRoutes(routes)
	m.appliedCfg = cloneProxyConfig(cfg)
	applied := in
	m.applied = &applied
	m.last = agentprotocol.ProxyConfigureResult{Restarted: recreated, Certificates: certStatus}
	m.acme.checkedAt = time.Time{} // observe the DNS-01 certificates again soon
	key := m.tailnetKey
	m.mu.Unlock()
	if m.cfg.Store != nil {
		if err := m.cfg.Store.SaveRoutes(routes); err != nil {
			m.log.Error("persist routes", "err", err)
		}
		if err := m.cfg.Store.SaveACMEEmail(email); err != nil {
			m.log.Error("persist acme email", "err", err)
		}
		if err := m.cfg.Store.SaveTrafficAnalytics(traffic); err != nil {
			m.log.Error("persist traffic analytics setting", "err", err)
		}
	}
	m.log.Info("applied routes", "routes", len(routes), "generation", m.generation(), "recreated", recreated)

	// The tailnet node last: it shares the (possibly new) proxy container's
	// network namespace, and a tailnet failure must not undo a good route table.
	terr := m.reconcileTailnet(ctx, cfg.Tailnet, key)
	m.finishTailnet()
	st := m.TailnetStatus()
	m.mu.Lock()
	m.last.Tailnet = st
	m.mu.Unlock()
	if terr != nil {
		m.log.Error("tailnet", "err", terr)
		return fmt.Errorf("tailnet: %w", terr)
	}
	return nil
}

// Generation is the generation of the live dynamic config (0 until the first
// push). It is only reported in a deployment's debug log.
func (m *Manager) Generation() int64 { return m.generation() }

func (m *Manager) generation() int64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.gen
}

// push renders routes as a new generation, PUTs it and waits until it is live.
// Upstream names are resolved, judged against the upstream policy and rendered
// as the resolved addresses (PinUpstreams) on every push, so a name is never
// resolved by Traefik itself and a re-push after a restart or a rollback is
// judged again.
func (m *Manager) push(ctx context.Context, routes []agentprotocol.Route, email string, certs []agentprotocol.CertRef, cfg agentprotocol.ProxyConfig) error {
	routes, err := PinUpstreams(ctx, routes, cfg.UpstreamPolicy, magicDNSOn(cfg))
	if err != nil {
		return err
	}
	// The edge network may have been created since the apply began.
	if err := m.refreshEdge(ctx); err != nil {
		return err
	}
	gen := m.nextGeneration()
	_, httpsPort := m.effectivePorts(cfg)
	opts := Options{
		ACMEEmail: email, PublicHTTPSPort: httpsPort, Generation: gen, Certificates: certs, Policy: cfg.UpstreamPolicy, Pages: m.pagesUp(),
		DNSResolvers: cfg.DNSResolvers, Wildcards: cfg.WildcardCerts,
	}
	body, err := BuildConfig(routes, opts)
	if err != nil {
		return err
	}
	if err := m.put(ctx, body); err != nil {
		return err
	}
	// The generation is recorded even if the config is then judged broken: the
	// marker router is live either way and later generations must exceed it.
	m.mu.Lock()
	m.gen = gen
	m.mu.Unlock()
	set := routerSet{http: RouterNames(routes, opts)}
	set.tcp, set.udp = L4RouterNames(routes, opts)
	return m.waitLive(ctx, gen, set)
}

func (m *Manager) adminRequest(ctx context.Context, method, path string, body []byte) (*http.Response, error) {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, m.adminURL+path, rd)
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(AdminUser, m.adminSecret)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return m.http.Do(req)
}

// put pushes one dynamic config. The published admin port is briefly closed
// while the proxy is reconfigured under Apply's own feet: Docker re-binds a
// container's published ports when its gateway endpoint changes, which
// can still happen on an old Docker that picks the gateway by network name
// (managed networks are named nebulactrl-env-* so they sort after
// nebulactrl-edge), and while Traefik restarts (crash, daemon restart,
// recreate). So every attempt first waits for the admin to
// answer, and a transient transport error (connection refused or reset, EOF,
// DNS miss, timeout) is retried with backoff for up to adminRetryWindow. A
// rejection by Traefik (any HTTP status) is definitive and never retried.
func (m *Manager) put(ctx context.Context, body []byte) error {
	return m.retryAdmin(ctx, func() error {
		if err := m.adminUp(ctx); err != nil && isTransientAdmin(err) {
			return err
		}
		// Any other readiness failure (bad credentials, an unexpected
		// status) is left for the PUT to report with its own message.
		return m.putOnce(ctx, body)
	})
}

func (m *Manager) putOnce(ctx context.Context, body []byte) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	resp, err := m.adminRequest(ctx, http.MethodPut, RESTPath, body)
	if err != nil {
		return fmt.Errorf("proxy admin unreachable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("proxy rejected config (%d): %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}

// retryAdmin runs fn until it succeeds, fails with a definitive error, ctx
// ends or adminRetryWindow has passed, backing off between transient failures.
func (m *Manager) retryAdmin(ctx context.Context, fn func() error) error {
	deadline := time.Now().Add(m.adminRetryWindow)
	backoff := m.adminRetryInitial
	for attempt := 1; ; attempt++ {
		err := fn()
		if err == nil || !isTransientAdmin(err) || ctx.Err() != nil {
			return err
		}
		if !time.Now().Add(backoff).Before(deadline) {
			return fmt.Errorf("%w (gave up after %d attempts over %s)", err, attempt, m.adminRetryWindow)
		}
		m.log.Warn("proxy admin not reachable, retrying", "attempt", attempt, "in", backoff, "err", err)
		select {
		case <-ctx.Done():
			return err
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, m.adminRetryMax)
	}
}

// isTransientAdmin reports whether err means the admin API is (briefly)
// unreachable rather than that Traefik answered and said no.
func isTransientAdmin(err error) bool {
	if err == nil {
		return false
	}
	switch {
	case errors.Is(err, syscall.ECONNREFUSED), errors.Is(err, syscall.ECONNRESET), errors.Is(err, syscall.ECONNABORTED),
		errors.Is(err, syscall.EPIPE), errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		return true
	}
	var dns *net.DNSError
	if errors.As(err, &dns) {
		return true
	}
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

// routerRejected is a definitive failure: Traefik loaded the config but
// disabled a router.
type routerRejected struct{ msg string }

func (e *routerRejected) Error() string { return e.msg }

// waitLive polls until the generation marker answers and every router of the
// config is enabled.
func (m *Manager) waitLive(ctx context.Context, gen int64, routers routerSet) error {
	deadline := time.Now().Add(applyLiveTimeout)
	var last error
	for {
		code, err := m.Probe(ctx, GenerationHost(gen), "/")
		switch {
		case err != nil:
			last = fmt.Errorf("probe: %w", err)
		case code != http.StatusOK:
			last = fmt.Errorf("generation %d not live yet (probe answered %d)", gen, code)
		default:
			last = m.checkRouters(ctx, routers)
			var rr *routerRejected
			if last == nil {
				return nil
			}
			if errors.As(last, &rr) {
				return last
			}
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("proxy did not apply the new config within %s: %w", applyLiveTimeout, last)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(applyPollInterval):
		}
	}
}

type apiRouter struct {
	Name   string   `json:"name"`
	Status string   `json:"status"`
	Error  []string `json:"error"`
}

// listRouters reads every router (all pages) from one of the admin API's
// router endpoints (APIRoutersPath, APITCPRoutersPath, APIUDPRoutersPath).
func (m *Manager) listRouters(ctx context.Context, path string) (map[string]apiRouter, error) {
	out := map[string]apiRouter{}
	page := 1
	for {
		cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		resp, err := m.adminRequest(cctx, http.MethodGet, path+"?per_page=500&page="+strconv.Itoa(page), nil)
		if err != nil {
			cancel()
			return nil, fmt.Errorf("proxy admin unreachable: %w", err)
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
		next := resp.Header.Get("X-Next-Page")
		_ = resp.Body.Close()
		cancel()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("proxy admin returned %d for router list", resp.StatusCode)
		}
		var rs []apiRouter
		if err := json.Unmarshal(body, &rs); err != nil {
			return nil, fmt.Errorf("decode router list: %w", err)
		}
		for _, r := range rs {
			out[r.Name] = r
		}
		if n, err := strconv.Atoi(next); err != nil || n <= page {
			return out, nil
		}
		page++
	}
}

// routerSet names the REST-provider routers a config must have enabled.
type routerSet struct{ http, tcp, udp []string }

// checkRouters verifies that every expected REST-provider router is enabled.
func (m *Manager) checkRouters(ctx context.Context, expected routerSet) error {
	var bad []string
	for _, g := range []struct {
		path  string
		names []string
	}{{APIRoutersPath, expected.http}, {APITCPRoutersPath, expected.tcp}, {APIUDPRoutersPath, expected.udp}} {
		if len(g.names) == 0 {
			continue
		}
		have, err := m.listRouters(ctx, g.path)
		if err != nil {
			return err
		}
		for _, name := range g.names {
			r, ok := have[name+"@rest"]
			switch {
			case !ok:
				return fmt.Errorf("router %s not loaded yet", name)
			case r.Status != "enabled":
				bad = append(bad, fmt.Sprintf("%s: %s", name, strings.Join(r.Error, "; ")))
			}
		}
	}
	if len(bad) > 0 {
		return &routerRejected{msg: "proxy disabled routers: " + strings.Join(bad, " | ")}
	}
	return nil
}

// Probe sends GET <proxy>:8099<path> with the given Host header through the
// unpublished probe entrypoint and returns the status code. It reaches the
// proxy via its IP on the edge network and falls back to the container's DNS
// name when that IP is not routable from the agent (e.g. Docker Desktop).
// A Host that matches no router answers 404.
func (m *Manager) Probe(ctx context.Context, host, path string) (int, error) {
	if path == "" || path[0] != '/' {
		path = "/" + path
	}
	var targets []string
	ictx, cancel := context.WithTimeout(ctx, 5*time.Second)
	info, err := m.cfg.Engine.ContainerInspect(ictx, agentprotocol.ProxyContainer)
	cancel()
	if err != nil {
		return 0, fmt.Errorf("inspect %s: %w", agentprotocol.ProxyContainer, err)
	}
	if ep, ok := info.Networks[agentprotocol.EdgeNetwork]; ok && ep.IP != "" {
		targets = append(targets, ep.IP)
	}
	targets = append(targets, agentprotocol.ProxyContainer)

	var lastErr error
	for _, t := range targets {
		code, err := m.probeOnce(ctx, net.JoinHostPort(t, strconv.Itoa(agentprotocol.ProxyProbePort)), host, path)
		if err == nil {
			return code, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			break
		}
	}
	return 0, lastErr
}

func (m *Manager) probeOnce(ctx context.Context, addr, host, path string) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+path, nil)
	if err != nil {
		return 0, err
	}
	req.Host = host
	resp, err := m.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	return resp.StatusCode, nil
}

// Run keeps the proxy healthy until ctx ends: it bootstraps Traefik (retrying
// while it cannot start, e.g. port 80 in use), then re-checks it periodically
// and repairs it when it stops or loses its dynamic config (the REST provider
// keeps no state across restarts). Status is reported through Status().
func (m *Manager) Run(ctx context.Context) {
	healthy := false
	for {
		wait := checkInterval
		if !healthy {
			if err := m.Bootstrap(ctx); err != nil {
				if ctx.Err() != nil {
					return
				}
				m.log.Error("proxy bootstrap failed", "err", err)
				m.setStatus(agentprotocol.ProxyError, err.Error())
				wait = retryInterval
			} else {
				healthy = true
			}
		} else if err := m.check(ctx); err != nil {
			if ctx.Err() != nil {
				return
			}
			m.log.Warn("proxy unhealthy, repairing", "err", err)
			m.setStatus(agentprotocol.ProxyError, err.Error())
			healthy = false
			wait = 0
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

// check verifies the container runs and still serves the live generation. A
// restarted Traefik (crash, daemon restart) answers 404 for the marker: the
// bootstrap that follows pushes the route table again.
func (m *Manager) check(ctx context.Context) error {
	// An Apply or Bootstrap in flight owns the container and the generation
	// marker; probing mid-push would misread the not-yet-live generation.
	if !m.applyMu.TryLock() {
		return nil
	}
	defer m.applyMu.Unlock()
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	info, err := m.cfg.Engine.ContainerInspect(cctx, agentprotocol.ProxyContainer)
	if err != nil {
		return fmt.Errorf("inspect proxy: %w", err)
	}
	if !info.Running {
		return fmt.Errorf("proxy container is %s", info.State)
	}
	if m.cfg.Pages && m.pagesUp() {
		serving, err := m.pagesServing(cctx)
		if err != nil {
			return fmt.Errorf("inspect pages server: %w", err)
		}
		if !serving {
			return errors.New("pages server container is not running")
		}
	}
	gen := m.generation()
	if gen == 0 {
		return nil
	}
	code, err := m.Probe(cctx, GenerationHost(gen), "/")
	if err != nil {
		return err
	}
	if code != http.StatusOK {
		return fmt.Errorf("proxy lost its route table (generation %d answered %d)", gen, code)
	}
	m.refreshTailnetIfDue(cctx)
	// The TLS handshakes can take seconds; never hold the health loop for them.
	go m.refreshAcmeIfDue(ctx)
	return nil
}

// Bootstrap ensures the edge network, the ACME volume and the nebulactrl-traefik
// container exist and run the pinned configuration, waits until it answers, and
// pushes the last-applied routes.
func (m *Manager) Bootstrap(ctx context.Context) error {
	m.applyMu.Lock()
	defer m.applyMu.Unlock()
	m.setStatus(agentprotocol.ProxyStarting, "")

	routes := m.Current()
	if routes == nil {
		routes = []agentprotocol.Route{}
	}
	if err := m.applyLocked(ctx, routes); err != nil {
		return fmt.Errorf("restore routes: %w", err)
	}
	m.setStatus(agentprotocol.ProxyOK, "")
	m.log.Info("proxy ready", "image", TraefikImage)
	return nil
}

func (m *Manager) ensureImage(ctx context.Context) error { return m.ensureImageRef(ctx, TraefikImage) }

func (m *Manager) ensureImageRef(ctx context.Context, ref string) error {
	if _, err := m.cfg.Engine.ImageInspect(ctx, ref); err == nil {
		return nil
	} else if !errors.Is(err, docker.ErrNotFound) {
		return err
	}
	m.log.Info("pulling image", "image", ref)
	return m.cfg.Engine.ImagePull(ctx, ref, "", nil)
}

// adminPublish decides how the admin entrypoint is published on the host.
func (m *Manager) adminPublish() (hostIP string, publish bool) {
	u, err := url.Parse(m.adminURL)
	if err != nil {
		return "", false
	}
	switch h := u.Hostname(); {
	case h == agentprotocol.ProxyContainer:
		return "", false
	case h == "localhost", h == "127.0.0.1", h == "::1":
		return "127.0.0.1", true
	default:
		return "", true
	}
}

// specInputs is everything that varies the Traefik container's definition
// besides the fixed image, admin secret and published admin/web ports.
type specInputs struct {
	ACMEEmail   string
	Traffic     bool
	L4          []L4Port
	HostGateway bool // host.docker.internal:host-gateway in /etc/hosts
	TailnetDNS  bool // 100.100.100.100 (tailnet MagicDNS) in the container's DNS list
	// EdgeIPv6: the edge network has IPv6, so docker publishes the public ports
	// on both families and keeps the visitor's address. Without it they are
	// bound to IPv4 only: an IPv6 visitor would otherwise arrive through
	// docker-proxy with the network gateway as its address (edge.go).
	EdgeIPv6 bool
	// LargeUploads: a domain of the table takes long request bodies (a registry);
	// the public entrypoints get the long read timeout.
	LargeUploads bool
	// TrustedProxyCIDRs are the ranges the web and websecure entrypoints trust
	// X-Forwarded-* from (TrustedProxyCIDRs of the table: Cloudflare's ranges
	// while a domain is origin-locked). A static entrypoint option: the
	// container is recreated only when this sorted set changes.
	TrustedProxyCIDRs []string
	// DNS are the DNS-01 resolvers (static flags) and their credentials (the
	// container's environment). Empty without an ACME email.
	DNS []agentprotocol.DNSResolver
	// HTTPPort and HTTPSPort are the host ports of the web entrypoints (the
	// server setting, else the agent's flags; 0 = the agent's flags).
	HTTPPort, HTTPSPort int
}

// tailnetDNS is the MagicDNS resolver of a managed tailnet node.
const tailnetDNS = "100.100.100.100"

func (m *Manager) inputsFor(routes []agentprotocol.Route, cfg agentprotocol.ProxyConfig) specInputs {
	httpPort, httpsPort := m.effectivePorts(cfg)
	in := specInputs{
		ACMEEmail: m.ACMEEmail(), Traffic: m.TrafficAnalytics(), L4: L4Ports(routes), LargeUploads: HasLargeUploads(routes),
		TrustedProxyCIDRs: TrustedProxyCIDRs(routes),
		HostGateway:       cfg.UpstreamPolicy.AllowHostGateway,
		TailnetDNS:        cfg.Tailnet != nil && cfg.Tailnet.Mode == agentprotocol.TailnetManaged && cfg.Tailnet.MagicDNS,
		EdgeIPv6:          m.edgeIPv6(),
		HTTPPort:          httpPort,
		HTTPSPort:         httpsPort,
	}
	if in.ACMEEmail != "" {
		in.DNS = sortedResolvers(cloneDNS(cfg.DNSResolvers))
	}
	return in
}

// desiredSpec is the container the agent wants for the current table and
// configuration.
func (m *Manager) desiredSpec() docker.ContainerSpec {
	return m.specFor(m.inputsFor(m.Current(), m.ProxyConfig()))
}

// specFor is the container for in. The bootstrap file is not part of the spec;
// the spec label fingerprints everything that defines it, including the admin
// secret the file's password hash derives from.
func (m *Manager) specFor(in specInputs) docker.ContainerSpec {
	httpPort, httpsPort := m.webPorts(in)
	flags := StaticFlags(StaticOptions{
		ACMEEmail: in.ACMEEmail, PublicHTTPSPort: httpsPort, TrafficAnalytics: in.Traffic, L4Ports: in.L4,
		DNSResolvers: in.DNS, LargeUploads: in.LargeUploads, TrustedProxyCIDRs: in.TrustedProxyCIDRs,
	})
	env := dnsEnv(in.DNS)
	// All interfaces ("") only while the edge network can carry IPv6 sources.
	bind := "0.0.0.0"
	if in.EdgeIPv6 {
		bind = ""
	}
	ports := []docker.PortPublish{
		{HostIP: bind, HostPort: httpPort, ContainerPort: 80, Proto: "tcp"},
		{HostIP: bind, HostPort: httpsPort, ContainerPort: 443, Proto: "tcp"},
		{HostIP: bind, HostPort: httpsPort, ContainerPort: 443, Proto: "udp"},
	}
	if ip, ok := m.adminPublish(); ok {
		ports = append(ports, docker.PortPublish{HostIP: ip, HostPort: m.cfg.AdminPort, ContainerPort: agentprotocol.ProxyAdminPort, Proto: "tcp"})
	}
	for _, p := range in.L4 {
		ports = append(ports, docker.PortPublish{HostIP: bind, HostPort: p.Port, ContainerPort: p.Port, Proto: p.Proto})
	}
	portParts := make([]string, len(ports))
	for i, p := range ports {
		portParts[i] = fmt.Sprintf("%s:%d->%d/%s", p.HostIP, p.HostPort, p.ContainerPort, p.Proto)
	}
	var extraHosts, dns []string
	if in.HostGateway {
		extraHosts = []string{agentprotocol.HostGatewayName + ":host-gateway"}
	}
	if in.TailnetDNS {
		dns = []string{tailnetDNS}
	}
	parts := []string{
		TraefikImage, m.adminSecret, LogMaxSize, LogMaxFile, string(BootstrapYAML("")), strings.Join(portParts, ","),
		"extrahosts=" + strings.Join(extraHosts, ","), "dns=" + strings.Join(dns, ","),
	}
	if len(env) > 0 {
		// The credentials' digest, never the values: rotating a token recreates the container.
		parts = append(parts, "dnsenv="+dnsFingerprint(env))
	}
	hash := specHash(append(parts, flags...)...)
	return docker.ContainerSpec{
		Name:   agentprotocol.ProxyContainer,
		Image:  TraefikImage,
		Cmd:    flags,
		Env:    env,
		Labels: map[string]string{agentprotocol.LabelManaged: "true", LabelSpec: hash},
		Networks: []docker.NetworkAttachment{
			{Network: agentprotocol.EdgeNetwork, Aliases: []string{agentprotocol.ProxyContainer}, GwPriority: agentprotocol.EdgeGwPriority},
		},
		Ports:         ports,
		Mounts:        []docker.VolumeMount{{Volume: agentprotocol.ProxyACMEVolume, Target: "/data"}},
		RestartPolicy: "unless-stopped",
		ExtraHosts:    extraHosts,
		DNS:           dns,
		// Access logs go to stdout; keep them bounded so a traffic spike
		// cannot fill the disk.
		LogConfig: &docker.LogConfig{Type: "json-file", Config: map[string]string{"max-size": LogMaxSize, "max-file": LogMaxFile}},
	}
}

// matches reports whether an existing container was created from want.
func matches(info docker.ContainerInfo, want docker.ContainerSpec) bool {
	if info.Image != want.Image || info.RestartMode != want.RestartPolicy {
		return false
	}
	if info.Labels[LabelSpec] == "" || info.Labels[LabelSpec] != want.Labels[LabelSpec] {
		return false
	}
	_, onEdge := info.Networks[agentprotocol.EdgeNetwork]
	return onEdge
}

// restoredError reports a failed recreate after which the previous container
// was put back: the caller must push the previous route table again.
type restoredError struct{ err error }

func (e *restoredError) Error() string { return e.err.Error() }
func (e *restoredError) Unwrap() error { return e.err }

// ensureContainer makes sure the container exists with the desired spec and
// runs, and waits until it answers on the admin and probe entrypoints. It
// recreates the container when its static configuration changed (recreated is
// true then). When the new container cannot start (a tcp/udp port already in
// use, ...) the previous container is created again so the proxy never stays
// down, and the error is a *restoredError naming the cause.
func (m *Manager) ensureContainer(ctx context.Context, in specInputs) (recreated bool, err error) {
	eng := m.cfg.Engine
	want := m.specFor(in)

	info, err := eng.ContainerInspect(ctx, agentprotocol.ProxyContainer)
	removedOld := false
	switch {
	case err == nil && matches(info, want):
		if info.Running {
			return false, nil
		}
		m.log.Info("starting existing proxy container", "state", info.State)
		if err := m.startContainer(ctx, info.ID, in); err != nil {
			return false, err
		}
		return false, m.waitReady(ctx)
	case err == nil:
		if err := m.checkNewWebPorts(ctx, info, want); err != nil {
			return false, err // nothing was touched: the running proxy keeps serving
		}
		m.log.Info("recreating proxy container: configuration changed", "image", info.Image)
		m.dropTailnetContainer(ctx) // it shares the old container's network namespace
		if err := eng.ContainerRemove(ctx, info.ID, true, false); err != nil && !errors.Is(err, docker.ErrNotFound) {
			return false, err
		}
		removedOld = true
	case !errors.Is(err, docker.ErrNotFound):
		return false, err
	}

	if err := m.createAndStart(ctx, want, in, removedOld); err != nil {
		if removedOld && m.applied != nil && m.specFor(*m.applied).Labels[LabelSpec] != want.Labels[LabelSpec] {
			prev := *m.applied
			if rerr := m.createAndStart(ctx, m.specFor(prev), prev, false); rerr != nil {
				return false, fmt.Errorf("%w (restoring the previous proxy container also failed: %v)", err, rerr)
			}
			m.reconcileAppliedTailnet(ctx)
			return false, &restoredError{err: fmt.Errorf("%w; the previous proxy configuration was restored", err)}
		}
		return false, err
	}
	return true, nil
}

// createAndStart creates the proxy container from spec, places the bootstrap
// file, starts it and waits until it answers. With cleanup a container that
// fails to start or become ready is removed so a previous spec can take its name.
func (m *Manager) createAndStart(ctx context.Context, spec docker.ContainerSpec, in specInputs, cleanup bool) error {
	eng := m.cfg.Engine
	labels := map[string]string{agentprotocol.LabelManaged: "true"}
	if err := eng.EnsureNetwork(ctx, agentprotocol.EdgeNetwork, labels); err != nil {
		return err
	}
	if err := eng.EnsureVolume(ctx, agentprotocol.ProxyACMEVolume, labels); err != nil {
		return err
	}
	if err := m.ensureImage(ctx); err != nil {
		return err
	}
	id, err := eng.ContainerCreate(ctx, spec)
	if err != nil {
		return fmt.Errorf("create proxy container: %w", err)
	}
	discard := func() {
		if cleanup {
			_ = eng.ContainerRemove(context.WithoutCancel(ctx), id, true, false)
		}
	}
	// The bootstrap file goes in before the first start, so Traefik never runs
	// without its admin router.
	tarball, err := bootstrapArchive(m.adminSecret)
	if err != nil {
		_ = eng.ContainerRemove(ctx, id, true, false)
		return err
	}
	if err := eng.CopyToContainer(ctx, id, "/", tarball); err != nil {
		_ = eng.ContainerRemove(ctx, id, true, false)
		return fmt.Errorf("write proxy bootstrap config: %w", err)
	}
	if err := m.startContainer(ctx, id, in); err != nil {
		discard()
		return err
	}
	// The dynamic config lives in Traefik's memory: a new container has none.
	m.mu.Lock()
	m.gen = 0
	m.mu.Unlock()
	if err := m.waitReady(ctx); err != nil {
		discard()
		return err
	}
	return nil
}

// restoreContainer recreates the container of the last applied inputs after a
// route table was rejected on the container built for failed. It returns the
// ACME email the restored container runs with.
func (m *Manager) restoreContainer(ctx context.Context, failed specInputs) (string, error) {
	if m.applied == nil || m.specFor(*m.applied).Labels[LabelSpec] == m.specFor(failed).Labels[LabelSpec] {
		return failed.ACMEEmail, nil
	}
	prev := *m.applied
	m.log.Warn("restoring the previous proxy container")
	m.dropTailnetContainer(ctx)
	if err := m.cfg.Engine.ContainerRemove(ctx, agentprotocol.ProxyContainer, true, false); err != nil && !errors.Is(err, docker.ErrNotFound) {
		return failed.ACMEEmail, err
	}
	if err := m.createAndStart(ctx, m.specFor(prev), prev, false); err != nil {
		return failed.ACMEEmail, err
	}
	m.reconcileAppliedTailnet(ctx)
	return prev.ACMEEmail, nil
}

// pushApplied pushes routes with the last applied certificates and policy.
func (m *Manager) pushApplied(ctx context.Context, routes []agentprotocol.Route, email string) error {
	cfg := m.appliedConfig()
	usable, _ := CertStatuses(cfg.Certificates)
	return m.push(ctx, routes, email, usable, cfg)
}

// bootstrapArchive returns the tar stream that places BootstrapFile in a new container.
func bootstrapArchive(secret string) (io.Reader, error) {
	// The secret is 32 random bytes, so a low bcrypt cost loses nothing and
	// keeps every authenticated admin request cheap.
	hash, err := bcrypt.GenerateFromPassword([]byte(secret), bcrypt.MinCost)
	if err != nil {
		return nil, fmt.Errorf("hash proxy admin secret: %w", err)
	}
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, dir := range []string{"etc/", "etc/traefik/"} {
		if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeDir, Name: dir, Mode: 0o755}); err != nil {
			return nil, err
		}
	}
	content := BootstrapYAML(string(hash))
	if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: strings.TrimPrefix(BootstrapFile, "/"), Mode: 0o600, Size: int64(len(content))}); err != nil {
		return nil, err
	}
	if _, err := tw.Write(content); err != nil {
		return nil, err
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	return &buf, nil
}

func (m *Manager) startContainer(ctx context.Context, id string, in specInputs) error {
	if err := m.cfg.Engine.ContainerStart(ctx, id); err != nil {
		httpPort, httpsPort := m.webPorts(in)
		return fmt.Errorf("proxy failed to start: %w", portHint(err, httpPort, httpsPort, in.L4))
	}
	return nil
}

var bindPortRe = regexp.MustCompile(`(?:Bind for|listen (?:tcp|udp)[46]?|bind:)[^\n]*?:(\d{1,5})\b`)

// portHint prefixes bind failures with the port involved so the reason is
// recognizable in the UI: a tcp/udp route's port when the failing bind is one
// of l4, the proxy's web ports otherwise.
func portHint(err error, httpPort, httpsPort int, l4 []L4Port) error {
	msg := err.Error()
	if !strings.Contains(msg, "address already in use") && !strings.Contains(msg, "port is already allocated") {
		return err
	}
	if mm := bindPortRe.FindStringSubmatch(msg); mm != nil {
		port, _ := strconv.Atoi(mm[1])
		for _, p := range l4 {
			if p.Port == port {
				return fmt.Errorf("%s port %d is already in use on this server (a tcp/udp route needs it): %w", p.Proto, p.Port, err)
			}
		}
	}
	if len(l4) > 0 {
		ports := make([]string, len(l4))
		for i, p := range l4 {
			ports[i] = p.Proto + "/" + strconv.Itoa(p.Port)
		}
		return fmt.Errorf("a port is already in use (web ports %d and %d, tcp/udp route ports %s): %w", httpPort, httpsPort, strings.Join(ports, ", "), err)
	}
	return fmt.Errorf("port %d or %d already in use (is another web server running?): %w", httpPort, httpsPort, err)
}

// waitReady waits until the admin API answers authenticated requests and the
// liveness router answers on the probe entrypoint.
func (m *Manager) waitReady(ctx context.Context) error {
	deadline := time.Now().Add(readyTimeout)
	var last error
	for time.Now().Before(deadline) {
		last = m.ready(ctx)
		if last == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	return fmt.Errorf("proxy not ready after %s: %w", readyTimeout, last)
}

// adminUp reports whether the admin API answers an authenticated request.
func (m *Manager) adminUp(ctx context.Context) error {
	cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	resp, err := m.adminRequest(cctx, http.MethodGet, APIRoutersPath+"?per_page=1", nil)
	if err != nil {
		return fmt.Errorf("proxy admin unreachable: %w", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized:
		return errors.New("proxy admin rejected the agent's credentials")
	default:
		return fmt.Errorf("proxy admin returned %d", resp.StatusCode)
	}
	return nil
}

func (m *Manager) ready(ctx context.Context) error {
	if err := m.adminUp(ctx); err != nil {
		return err
	}
	cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	code, err := m.Probe(cctx, PingHost, "/ping")
	if err != nil {
		return fmt.Errorf("proxy probe entrypoint: %w", err)
	}
	if code != http.StatusOK {
		return fmt.Errorf("proxy probe entrypoint answered %d", code)
	}
	return nil
}
