// Package forward is the control plane's end of the instance-service tunnel.
//
// An agent offers 127.0.0.1:18333 (S3), :18500 (registry) and :18100 (Loki) on
// its server and relays every accepted TCP connection to the control plane as a
// tunnel stream (pkg/agentprotocol/localtunnel.go). The hub knows which server
// the stream belongs to from the authenticated handshake; this package resolves
// that server to its team and speaks HTTP on the stream, per service:
//
//   - S3: a signature-passthrough proxy. The client signed for 127.0.0.1:18333
//     with its team's slice key; the request is forwarded unchanged, Host
//     preserved, after checking that the key belongs to the stream's team, that
//     the bucket is the team's, that the operation is one a backup needs, and
//     that a write fits the team's quota.
//   - Registry: the request is checked against the team's repository prefix
//     and forwarded with a short-lived bearer token for exactly that name,
//     which the control plane signs. Clients hold no registry credential.
//   - Loki: only the push endpoint, with X-Scope-OrgID set to the team.
//
// The agent is a dumb pipe; nothing a client sends decides the team.
package forward

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/agents"
	"github.com/nebulactrl/nebulactrl/internal/instsvc"
	"github.com/nebulactrl/nebulactrl/internal/registries"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// RegistryAuth is the registry backend as the forwarder needs it.
type RegistryAuth interface {
	// SignRepoToken signs a short-lived registry token for exactly the given
	// repositories and actions.
	SignRepoToken(ctx context.Context, access ...registries.RepoAccess) (string, error)
	// ReadOnly reports whether the registry is in its garbage-collection window.
	ReadOnly(ctx context.Context) bool
}

// Deps are the collaborators of a Forwarder.
type Deps struct {
	Slices instsvc.Slices
	Gate   instsvc.QuotaGate
	// S3URL, RegistryURL and LokiURL are the services' base URLs on the
	// internal network. An empty URL turns that service off: its streams are
	// refused.
	S3URL, RegistryURL, LokiURL string
	Registry                    RegistryAuth
	// Ledger bounds a team's use of the registry disk (quota check, push
	// record). Nil: a Slices that implements RegistryLedger is used, else none.
	Ledger RegistryLedger
	Logger *slog.Logger
}

// Forwarder implements agents.TunnelAcceptor.
type Forwarder struct {
	d      Deps
	log    *slog.Logger
	s3     http.Handler
	reg    http.Handler
	loki   http.Handler
	teams  ttlCache[uuid.UUID, instsvc.TeamSlices]
	owners ttlCache[string, uuid.UUID]
	// cacheTTL is a field so tests can turn the cache off.
	cacheTTL time.Duration
	// bodyIdle is the longest a request body may stay silent (tests shorten it).
	bodyIdle time.Duration

	// registryWrites counts the registry write requests in flight; the
	// garbage-collection window waits for it to reach zero.
	registryWrites atomic.Int64
}

// cacheTTL bounds how stale a slice lookup can be: a removed key or a deleted
// team is refused within this time.
const defaultCacheTTL = 5 * time.Second

// New returns a Forwarder.
func New(d Deps) (*Forwarder, error) {
	f := &Forwarder{d: d, log: d.Logger, cacheTTL: defaultCacheTTL, bodyIdle: defaultBodyIdle}
	if f.log == nil {
		f.log = slog.Default()
	}
	if f.d.Ledger == nil {
		if l, ok := d.Slices.(RegistryLedger); ok {
			f.d.Ledger = l
		}
	}
	var err error
	if d.S3URL != "" {
		if f.s3, err = newS3(f, d.S3URL); err != nil {
			return nil, fmt.Errorf("s3 url: %w", err)
		}
	}
	if d.RegistryURL != "" {
		if f.reg, err = newRegistry(f, d.RegistryURL); err != nil {
			return nil, fmt.Errorf("registry url: %w", err)
		}
	}
	if d.LokiURL != "" {
		if f.loki, err = newLoki(f, d.LokiURL); err != nil {
			return nil, fmt.Errorf("loki url: %w", err)
		}
	}
	return f, nil
}

// RegistryWrites is the number of registry write requests in flight.
func (f *Forwarder) RegistryWrites() int { return int(f.registryWrites.Load()) }

// peer is who is on the other end of a stream; set by the hub's handshake, never by the client.
type peer struct {
	team   uuid.UUID
	server uuid.UUID
}

type peerKey struct{}

func peerOf(ctx context.Context) peer {
	p, _ := ctx.Value(peerKey{}).(peer)
	return p
}

// AcceptTunnel implements agents.TunnelAcceptor: it serves HTTP on the stream
// until the client closes it. The hub closes c after the return.
func (f *Forwarder) AcceptTunnel(ctx context.Context, p agents.TunnelPeer, c net.Conn) error {
	var h http.Handler
	switch p.Service {
	case agentprotocol.TunnelS3:
		h = f.s3
	case agentprotocol.TunnelRegistry:
		h = f.reg
	case agentprotocol.TunnelLoki:
		h = f.loki
	}
	if h == nil {
		return fmt.Errorf("the %s service is not available on this control plane", p.Service)
	}
	team, err := f.d.Slices.TeamOfServer(ctx, p.ServerID)
	if err != nil {
		return errors.New("server is not known to a team")
	}
	pe := peer{team: team, server: p.ServerID}
	srv := &http.Server{
		Handler:           withBodyIdleTimeout(h, f.bodyIdle),
		ReadHeaderTimeout: 30 * time.Second,
		IdleTimeout:       5 * time.Minute,
		ErrorLog:          slog.NewLogLogger(slog.NewTextHandler(io.Discard, nil), slog.LevelError),
		BaseContext:       func(net.Listener) context.Context { return context.WithValue(ctx, peerKey{}, pe) },
	}
	ln := newConnListener(c)
	stop := context.AfterFunc(ctx, func() { _ = c.Close() })
	defer stop()
	if err := srv.Serve(ln); err != nil && !errors.Is(err, net.ErrClosed) && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// connListener hands one connection to an http.Server and reports the end of
// that connection by failing the next Accept, so Serve returns when the stream does.
type connListener struct {
	mu     sync.Mutex
	conn   net.Conn
	closed chan struct{}
	once   sync.Once
}

func newConnListener(c net.Conn) *connListener {
	l := &connListener{closed: make(chan struct{})}
	l.conn = &notifyConn{Conn: c, onClose: func() { l.once.Do(func() { close(l.closed) }) }}
	return l
}

func (l *connListener) Accept() (net.Conn, error) {
	l.mu.Lock()
	c := l.conn
	l.conn = nil
	l.mu.Unlock()
	if c != nil {
		return c, nil
	}
	<-l.closed
	return nil, net.ErrClosed
}

func (l *connListener) Close() error {
	l.once.Do(func() { close(l.closed) })
	return nil
}

func (l *connListener) Addr() net.Addr { return tunnelAddr{} }

type tunnelAddr struct{}

func (tunnelAddr) Network() string { return "tunnel" }
func (tunnelAddr) String() string  { return "tunnel" }

// notifyConn calls onClose when the connection is closed.
type notifyConn struct {
	net.Conn
	onClose func()
}

func (c *notifyConn) Close() error {
	err := c.Conn.Close()
	c.onClose()
	return err
}

// ---- shared upstream plumbing ---------------------------------------------------------

// newTransport is the transport to one instance service on the internal network.
func newTransport() *http.Transport {
	return &http.Transport{
		DialContext:           (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		MaxIdleConns:          64,
		MaxIdleConnsPerHost:   32,
		IdleConnTimeout:       90 * time.Second,
		ResponseHeaderTimeout: 0, // a multipart complete or a big listing can take minutes
		// Bytes pass through exactly as the client signed them.
		DisableCompression: true,
	}
}

func parseBase(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("%q is not an http(s) URL", raw)
	}
	return u, nil
}

// newProxy returns a streaming reverse proxy to target that keeps the client's
// Host (SigV4 covers it, and the registry builds Location URLs from it).
// rewrite may adjust the outbound request further.
func newProxy(target *url.URL, log *slog.Logger, rewrite func(*httputil.ProxyRequest), modify func(*http.Response) error, onError func(http.ResponseWriter, *http.Request, error)) *httputil.ReverseProxy {
	return &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.Out.Host = pr.In.Host
			if rewrite != nil {
				rewrite(pr)
			}
		},
		Transport:      newTransport(),
		FlushInterval:  -1,
		ModifyResponse: modify,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			if !errors.Is(err, context.Canceled) {
				log.Warn("instance service request failed", "path", r.URL.Path, "err", err)
			}
			onError(w, r, err)
		},
		ErrorLog: slog.NewLogLogger(slog.NewTextHandler(io.Discard, nil), slog.LevelError),
	}
}

// ---- tiny TTL cache ----------------------------------------------------------------------

type cacheEntry[V any] struct {
	v   V
	exp time.Time
}

type ttlCache[K comparable, V any] struct {
	mu sync.Mutex
	m  map[K]cacheEntry[V]
}

func (c *ttlCache[K, V]) get(ttl time.Duration, k K, load func() (V, error)) (V, error) {
	now := time.Now()
	c.mu.Lock()
	if e, ok := c.m[k]; ok && now.Before(e.exp) {
		c.mu.Unlock()
		return e.v, nil
	}
	c.mu.Unlock()
	v, err := load()
	if err != nil {
		var zero V
		return zero, err
	}
	c.mu.Lock()
	if c.m == nil || len(c.m) > 4096 {
		c.m = map[K]cacheEntry[V]{}
	}
	c.m[k] = cacheEntry[V]{v: v, exp: now.Add(ttl)}
	c.mu.Unlock()
	return v, nil
}

func (f *Forwarder) teamSlices(ctx context.Context, team uuid.UUID) (instsvc.TeamSlices, error) {
	return f.teams.get(f.cacheTTL, team, func() (instsvc.TeamSlices, error) { return f.d.Slices.ForTeam(ctx, team) })
}

func (f *Forwarder) keyOwner(ctx context.Context, accessKey string) (uuid.UUID, error) {
	return f.owners.get(f.cacheTTL, accessKey, func() (uuid.UUID, error) { return f.d.Slices.AccessKeyOwner(ctx, accessKey) })
}
