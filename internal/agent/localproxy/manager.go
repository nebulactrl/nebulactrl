// Package localproxy offers the instance services (S3, image registry, Loki)
// to this server on 127.0.0.1 and carries every accepted TCP connection to the
// control plane as a tunnel stream (pkg/agentprotocol/localtunnel.go). The
// agent is a dumb pipe: it parses nothing, holds no credential and the control
// plane decides the team from the agent's authenticated connection.
package localproxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sort"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Dialer opens a tunnel stream to an instance service. conn.Client implements it.
type Dialer interface {
	OpenTunnel(ctx context.Context, service string) (net.Conn, error)
}

// DialerFunc adapts a function to Dialer.
type DialerFunc func(ctx context.Context, service string) (net.Conn, error)

func (f DialerFunc) OpenTunnel(ctx context.Context, service string) (net.Conn, error) {
	return f(ctx, service)
}

// Config configures a Manager.
type Config struct {
	Dialer Dialer
	Logger *slog.Logger
	// Port returns the only port an endpoint of service may use. Nil means the
	// protocol's fixed ports (agentprotocol.LocalPort). Tests use 0 for any.
	Port func(service string) int
	// RetryEvery is how often Run retries endpoints that could not bind
	// (default 30s).
	RetryEvery time.Duration
}

// Manager owns the local listeners.
type Manager struct {
	cfg Config
	log *slog.Logger

	mu        sync.Mutex
	desired   map[string]agentprotocol.Endpoint
	listeners map[string]*listener
	refused   map[string]string // endpoints the control plane may not ask for
	bindErr   map[string]string // endpoints whose port could not be bound (retried)
	closed    bool
}

type listener struct {
	service string
	ln      net.Listener
	cancel  context.CancelFunc
	conns   sync.WaitGroup
}

// New returns a Manager with no listeners.
func New(cfg Config) *Manager {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Port == nil {
		cfg.Port = agentprotocol.LocalPort
	}
	if cfg.RetryEvery <= 0 {
		cfg.RetryEvery = 30 * time.Second
	}
	return &Manager{
		cfg: cfg, log: cfg.Logger.With("component", "localproxy"),
		desired: map[string]agentprotocol.Endpoint{}, listeners: map[string]*listener{}, refused: map[string]string{}, bindErr: map[string]string{},
	}
}

// Apply makes the listeners match eps: it stops listeners that are no longer
// wanted and starts the missing ones. Anything but a known service on its fixed
// port is refused and reported in Status. It is idempotent.
func (m *Manager) Apply(eps agentprotocol.InstanceEndpoints) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return
	}
	want := map[string]agentprotocol.Endpoint{}
	errs := map[string]string{}
	for _, ep := range eps.Listen {
		switch {
		case agentprotocol.TunnelServiceLimit(ep.Service) == 0:
			errs[ep.Service] = "unknown service"
			want[ep.Service] = ep
		case ep.Port != m.cfg.Port(ep.Service):
			errs[ep.Service] = fmt.Sprintf("port %d is not allowed for %s", ep.Port, ep.Service)
			want[ep.Service] = ep
		default:
			want[ep.Service] = ep
		}
	}
	for svc, l := range m.listeners {
		if ep, ok := want[svc]; !ok || errs[svc] != "" || ep.Port != m.desired[svc].Port {
			m.stopLocked(svc, l)
		}
	}
	m.desired, m.refused = want, errs
	for svc := range m.bindErr {
		if _, ok := want[svc]; !ok {
			delete(m.bindErr, svc)
		}
	}
	m.startMissingLocked()
}

func (m *Manager) startMissingLocked() {
	for svc, ep := range m.desired {
		if _, running := m.listeners[svc]; running || m.refused[svc] != "" {
			continue
		}
		ln, err := net.Listen("tcp", net.JoinHostPort(agentprotocol.LocalHost, strconv.Itoa(ep.Port)))
		if err != nil {
			if m.bindErr[svc] == "" {
				m.log.Warn("instance service endpoint not listening", "service", svc, "port", ep.Port, "err", err)
			}
			m.bindErr[svc] = bindError(ep.Port, err)
			continue
		}
		ctx, cancel := context.WithCancel(context.Background())
		l := &listener{service: svc, ln: ln, cancel: cancel}
		m.listeners[svc] = l
		delete(m.bindErr, svc)
		go m.accept(ctx, l)
		m.log.Info("instance service endpoint listening", "service", svc, "addr", ln.Addr().String())
	}
}

func bindError(port int, err error) string {
	if errors.Is(err, syscall.EADDRINUSE) {
		return fmt.Sprintf("port %d is already in use on this server", port)
	}
	return fmt.Sprintf("cannot listen on port %d: %v", port, err)
}

func (m *Manager) stopLocked(svc string, l *listener) {
	delete(m.listeners, svc)
	l.cancel()
	_ = l.ln.Close()
}

// Status reports the requested endpoints for Heartbeat.Endpoints, by service.
func (m *Manager) Status() []agentprotocol.EndpointStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]agentprotocol.EndpointStatus, 0, len(m.desired))
	for svc, ep := range m.desired {
		st := agentprotocol.EndpointStatus{Service: svc, Port: ep.Port, Error: m.refused[svc]}
		if st.Error == "" {
			st.Error = m.bindErr[svc]
		}
		if l := m.listeners[svc]; l != nil {
			st.Listening = true
			st.Port = l.ln.Addr().(*net.TCPAddr).Port
		}
		out = append(out, st)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Service < out[j].Service })
	return out
}

// Addr is the local address service listens on ("" when it does not).
func (m *Manager) Addr(service string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if l := m.listeners[service]; l != nil {
		return l.ln.Addr().String()
	}
	return ""
}

// Run retries endpoints that could not bind until ctx ends, then stops every
// listener.
func (m *Manager) Run(ctx context.Context) {
	t := time.NewTicker(m.cfg.RetryEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			m.Close()
			return
		case <-t.C:
			m.mu.Lock()
			if !m.closed {
				m.startMissingLocked()
			}
			m.mu.Unlock()
		}
	}
}

// Close stops every listener and ends their connections.
func (m *Manager) Close() {
	m.mu.Lock()
	m.closed = true
	ls := make([]*listener, 0, len(m.listeners))
	for svc, l := range m.listeners {
		ls = append(ls, l)
		m.stopLocked(svc, l)
	}
	m.mu.Unlock()
	for _, l := range ls {
		l.conns.Wait()
	}
}

func (m *Manager) accept(ctx context.Context, l *listener) {
	for {
		c, err := l.ln.Accept()
		if err != nil {
			if ctx.Err() == nil {
				m.log.Warn("instance service listener stopped", "service", l.service, "err", err)
			}
			return
		}
		l.conns.Add(1)
		go func() {
			defer l.conns.Done()
			m.handle(ctx, l.service, c)
		}()
	}
}

// handle carries one local connection. While the control plane is unreachable
// or refuses the stream the local connection is closed at once, which the
// client sees as a connection error and retries.
func (m *Manager) handle(ctx context.Context, service string, local net.Conn) {
	defer local.Close()
	tun, err := m.cfg.Dialer.OpenTunnel(ctx, service)
	if err != nil {
		m.log.Debug("instance service stream refused", "service", service, "err", err)
		return
	}
	defer tun.Close()
	Pipe(ctx, local, tun)
}

type aborter interface{ Abort(code, reason string) }

type closeWriter interface{ CloseWrite() error }

var bufPool = sync.Pool{New: func() any { b := make([]byte, agentprotocol.TunnelChunk); return &b }}

// Pipe copies both directions between local and tun until both ended, passing a
// half-close (EOF) on as a FIN. A failure in either direction, or ctx ending,
// aborts both at once.
func Pipe(ctx context.Context, local, tun net.Conn) {
	stop := context.AfterFunc(ctx, func() { abort(local, tun) })
	defer stop()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		if err := copyHalf(tun, local); err != nil {
			abort(local, tun)
		}
	}()
	go func() {
		defer wg.Done()
		if err := copyHalf(local, tun); err != nil {
			abort(local, tun)
		}
	}()
	wg.Wait()
}

func abort(local, tun net.Conn) {
	if a, ok := tun.(aborter); ok {
		a.Abort(agentprotocol.TunnelCodeClosed, "local connection ended")
	}
	_ = tun.Close()
	_ = local.Close()
}

// copyHalf copies src to dst until EOF and half-closes dst.
func copyHalf(dst, src net.Conn) error {
	bp := bufPool.Get().(*[]byte)
	defer bufPool.Put(bp)
	_, err := io.CopyBuffer(struct{ io.Writer }{dst}, struct{ io.Reader }{src}, *bp)
	if err != nil {
		return err
	}
	if cw, ok := dst.(closeWriter); ok {
		return cw.CloseWrite()
	}
	return nil
}
