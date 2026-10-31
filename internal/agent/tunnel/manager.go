package tunnel

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

const (
	// statusEvery bounds how often a running connector is asked for /ready (the
	// heartbeat runs every 10 seconds).
	statusEvery      = 10 * time.Second
	readyTimeout     = 3 * time.Second
	logTailLines     = 12
	maxLogTailBytes  = 16 << 10
	maxReadyBody     = 64 << 10
	defaultWait      = 60 * time.Second
	defaultPoll      = 2 * time.Second
	restartLoopAfter = 2
	maxTokenLen      = 4096
)

var (
	tunnelIDRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	// tokenRe is the connector token alphabet: base64 (standard or URL-safe) of
	// a small JSON document.
	tokenRe = regexp.MustCompile(`^[A-Za-z0-9+/_=-]+$`)
)

// Manager creates, observes and removes the cloudflared container. Callers
// serialize Ensure and Remove (the engine runs them under one lock); Status may
// run concurrently with them.
type Manager struct {
	eng docker.Engine
	log *slog.Logger
	now func() time.Time

	// Ready fetches the connector's /ready endpoint (tests replace it).
	Ready func(ctx context.Context, url string) (status int, body []byte, err error)
	// WaitOnline bounds how long Ensure waits for the first connection and Poll
	// is the interval between reads (tests shorten both).
	WaitOnline time.Duration
	Poll       time.Duration

	mu    sync.Mutex
	cache cachedStatus
}

type cachedStatus struct {
	containerID string
	st          agentprotocol.TunnelStatus
	at          time.Time
}

// New returns a Manager.
func New(eng docker.Engine, log *slog.Logger) *Manager {
	if log == nil {
		log = slog.Default()
	}
	return &Manager{
		eng: eng, log: log.With("component", "tunnel"), now: time.Now,
		Ready: httpReady, WaitOnline: defaultWait, Poll: defaultPoll,
	}
}

func httpReady(ctx context.Context, url string) (int, []byte, error) {
	ctx, cancel := context.WithTimeout(ctx, readyTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxReadyBody))
	return resp.StatusCode, body, err
}

func (m *Manager) forget() {
	m.mu.Lock()
	m.cache = cachedStatus{}
	m.mu.Unlock()
}

func attention(id, msg string) agentprotocol.TunnelStatus {
	return agentprotocol.TunnelStatus{State: agentprotocol.TunnelStateNeedsAttn, TunnelID: id, Message: msg, Image: Image}
}

func redact(s, token string) string {
	if len(token) >= 6 {
		return strings.ReplaceAll(s, token, "••••••")
	}
	return s
}

// Validate checks a sync spec and its token before anything touches Docker.
func Validate(spec agentprotocol.TunnelSpec, token string) error {
	if !tunnelIDRe.MatchString(spec.TunnelID) {
		return errors.New("tunnelId must be a lowercase UUID")
	}
	if token == "" {
		return errors.New("the tunnel token was not sent")
	}
	if len(token) > maxTokenLen || !tokenRe.MatchString(token) {
		return errors.New("the tunnel token is malformed")
	}
	return nil
}

// Ensure makes the connector container match spec and waits for its first
// connection. It never returns an error: every problem becomes the reported
// state. The token is used only when the container is (re)created and is
// redacted from everything returned or logged.
func (m *Manager) Ensure(ctx context.Context, spec agentprotocol.TunnelSpec, token string, logf func(string)) agentprotocol.TunnelStatus {
	if logf == nil {
		logf = func(string) {}
	}
	inner := logf
	logf = func(s string) { inner(redact(s, token)) }
	st := m.ensure(ctx, spec, token, logf)
	st.Message = redact(st.Message, token)
	return st
}

func (m *Manager) ensure(ctx context.Context, spec agentprotocol.TunnelSpec, token string, logf func(string)) agentprotocol.TunnelStatus {
	if err := Validate(spec, token); err != nil {
		return attention(spec.TunnelID, "Invalid tunnel configuration: "+err.Error())
	}
	m.forget()
	if err := m.eng.EnsureNetwork(ctx, agentprotocol.EdgeNetwork, map[string]string{agentprotocol.LabelManaged: "true"}); err != nil {
		return attention(spec.TunnelID, "Could not create the edge network: "+err.Error())
	}
	want := ContainerSpec(spec)
	info, err := m.eng.ContainerInspect(ctx, agentprotocol.TunnelContainer)
	exists := err == nil
	if err != nil && !errors.Is(err, docker.ErrNotFound) {
		return attention(spec.TunnelID, "Could not inspect the cloudflared container: "+err.Error())
	}

	id := info.ID
	switch {
	case exists && info.Labels[agentprotocol.LabelTunnelSpec] == want.Labels[agentprotocol.LabelTunnelSpec] &&
		info.Labels[agentprotocol.LabelTunnel] == spec.TunnelID:
		if !info.Running && !info.Restarting {
			if err := m.eng.ContainerStart(ctx, id); err != nil {
				return attention(spec.TunnelID, "Could not start the cloudflared container: "+redact(err.Error(), token))
			}
		}
	default:
		if exists {
			logf("Tunnel: recreating the connector (its configuration or tunnel changed)")
			if err := m.removeContainer(ctx, info); err != nil {
				return attention(spec.TunnelID, err.Error())
			}
		}
		if err := m.ensureImage(ctx); err != nil {
			return attention(spec.TunnelID, "Could not pull the cloudflared image: "+err.Error())
		}
		id, err = m.eng.ContainerCreate(ctx, want)
		if err != nil {
			return attention(spec.TunnelID, redact("Could not create the cloudflared container: "+err.Error(), token))
		}
		// The token is in place before the first start and never in the
		// container's environment or arguments.
		tarball, err := TokenTar(token)
		if err == nil {
			err = m.eng.CopyToContainer(ctx, id, "/", tarball)
		}
		if err != nil {
			_ = m.eng.ContainerRemove(context.WithoutCancel(ctx), id, true, false)
			return attention(spec.TunnelID, redact("Could not write the tunnel token: "+err.Error(), token))
		}
		if err := m.eng.ContainerStart(ctx, id); err != nil {
			_ = m.eng.ContainerRemove(context.WithoutCancel(ctx), id, true, false)
			return attention(spec.TunnelID, redact("Could not start the cloudflared container: "+err.Error(), token))
		}
	}

	st := m.waitOnline(ctx, id, spec.TunnelID)
	m.mu.Lock()
	m.cache = cachedStatus{containerID: id, st: st, at: m.now()}
	m.mu.Unlock()
	if st.State == agentprotocol.TunnelStateOnline {
		logf(fmt.Sprintf("Tunnel: connected to Cloudflare (%d connections)", st.Connections))
	} else {
		logf("Tunnel: " + st.State + ": " + st.Message)
	}
	return st
}

func (m *Manager) ensureImage(ctx context.Context) error {
	if _, err := m.eng.ImageInspect(ctx, Image); err == nil {
		return nil
	} else if !errors.Is(err, docker.ErrNotFound) {
		return err
	}
	return m.eng.ImagePull(ctx, Image, "", nil)
}

func (m *Manager) removeContainer(ctx context.Context, info docker.ContainerInfo) error {
	grace := 10 * time.Second
	if info.Running || info.Restarting {
		if err := m.eng.ContainerStop(ctx, info.ID, &grace); err != nil && !errors.Is(err, docker.ErrNotFound) {
			return fmt.Errorf("stop the cloudflared container: %w", err)
		}
	}
	if err := m.eng.ContainerRemove(ctx, info.ID, true, false); err != nil && !errors.Is(err, docker.ErrNotFound) {
		return fmt.Errorf("remove the cloudflared container: %w", err)
	}
	return nil
}

// Remove deletes the connector. The tunnel itself is deleted by the control
// plane.
func (m *Manager) Remove(ctx context.Context, logf func(string)) agentprotocol.TunnelStatus {
	m.forget()
	info, err := m.eng.ContainerInspect(ctx, agentprotocol.TunnelContainer)
	switch {
	case errors.Is(err, docker.ErrNotFound):
	case err != nil:
		return attention("", "Could not inspect the cloudflared container: "+err.Error())
	default:
		if err := m.removeContainer(ctx, info); err != nil {
			return attention(info.Labels[agentprotocol.LabelTunnel], err.Error())
		}
	}
	if logf != nil {
		logf("Tunnel: removed the cloudflared container")
	}
	return agentprotocol.TunnelStatus{State: agentprotocol.TunnelStateRemoved}
}

// waitOnline polls the connector until it has a connection, the container dies
// or the wait ends, and returns the last state seen.
func (m *Manager) waitOnline(ctx context.Context, id, tunnelID string) agentprotocol.TunnelStatus {
	deadline := m.now().Add(m.WaitOnline)
	var last agentprotocol.TunnelStatus
	for {
		info, err := m.eng.ContainerInspect(ctx, id)
		if err != nil {
			return attention(tunnelID, "Cannot inspect the cloudflared container: "+err.Error())
		}
		last = m.observe(ctx, info)
		if last.State == agentprotocol.TunnelStateOnline || last.State == agentprotocol.TunnelStateNeedsAttn {
			return last
		}
		if !info.Running && !info.Restarting {
			return last
		}
		if ctx.Err() != nil {
			return attention(tunnelID, "The agent stopped while waiting for the tunnel.")
		}
		if !m.now().Before(deadline) {
			break
		}
		select {
		case <-ctx.Done():
			return attention(tunnelID, "The agent stopped while waiting for the tunnel.")
		case <-time.After(m.Poll):
		}
	}
	last.State = agentprotocol.TunnelStateProvisioning
	if last.Message == "" {
		last.Message = "The connector is still connecting to Cloudflare."
	}
	return last
}

// Status reports the connector for heartbeats; nil when the host has no
// cloudflared container. A running connector is asked for /ready at most every
// statusEvery.
func (m *Manager) Status(ctx context.Context) *agentprotocol.TunnelStatus {
	info, err := m.eng.ContainerInspect(ctx, agentprotocol.TunnelContainer)
	if err != nil {
		if !errors.Is(err, docker.ErrNotFound) {
			m.log.Debug("observe tunnel: inspect", "err", err)
		}
		return nil
	}
	m.mu.Lock()
	c := m.cache
	m.mu.Unlock()
	if c.containerID == info.ID && info.Running && m.now().Sub(c.at) < statusEvery {
		st := c.st
		return &st
	}
	st := m.observe(ctx, info)
	m.mu.Lock()
	m.cache = cachedStatus{containerID: info.ID, st: st, at: m.now()}
	m.mu.Unlock()
	return &st
}

// observe maps the container's state and its /ready answer onto TunnelStatus.
func (m *Manager) observe(ctx context.Context, info docker.ContainerInfo) agentprotocol.TunnelStatus {
	st := agentprotocol.TunnelStatus{TunnelID: info.Labels[agentprotocol.LabelTunnel], Image: info.Image}
	switch {
	case info.Restarting || (!info.Running && info.RestartCount >= restartLoopAfter && info.ExitCode != 0):
		st.State = agentprotocol.TunnelStateNeedsAttn
		st.Message = "The cloudflared container keeps restarting."
		if tail := m.logTail(ctx, info.ID); tail != "" {
			st.Message += " Last log lines: " + tail
		}
	case !info.Running:
		st.State = agentprotocol.TunnelStateOffline
		st.Message = "The cloudflared container is " + info.State + "."
	default:
		m.readReady(ctx, info, &st)
	}
	return st
}

// readReady asks the connector's /ready endpoint on the edge network. Without a
// reachable address (a Docker Desktop host cannot route to container IPs) the
// container's own health status decides, with the connection count unknown.
func (m *Manager) readReady(ctx context.Context, info docker.ContainerInfo, st *agentprotocol.TunnelStatus) {
	if n, ok := info.Networks[agentprotocol.EdgeNetwork]; ok && n.IP != "" {
		code, body, err := m.Ready(ctx, "http://"+n.IP+":"+strconv.Itoa(agentprotocol.TunnelMetricsPort)+"/ready")
		if err == nil && (code == http.StatusOK || code == http.StatusServiceUnavailable) {
			var rd struct {
				ReadyConnections int `json:"readyConnections"`
			}
			_ = json.Unmarshal(body, &rd)
			st.Connections = rd.ReadyConnections
			if code == http.StatusOK && rd.ReadyConnections > 0 {
				st.State = agentprotocol.TunnelStateOnline
				return
			}
			st.State = agentprotocol.TunnelStateProvisioning
			st.Message = "The connector has no connection to Cloudflare yet."
			return
		}
	}
	switch info.Health {
	case "healthy":
		st.State = agentprotocol.TunnelStateOnline
		st.Message = "Connection count unavailable."
	case "unhealthy":
		st.State = agentprotocol.TunnelStateProvisioning
		st.Message = "The connector is not connected to Cloudflare."
		if tail := m.logTail(ctx, info.ID); tail != "" {
			st.Message += " Last log lines: " + tail
		}
	default:
		st.State = agentprotocol.TunnelStateProvisioning
		st.Message = "The connector is starting."
	}
}

// logTail returns the last log lines of the connector on one line.
func (m *Manager) logTail(ctx context.Context, id string) string {
	cctx, cancel := context.WithTimeout(ctx, readyTimeout)
	defer cancel()
	rc, err := m.eng.ContainerLogs(cctx, id, docker.LogOptions{Tail: logTailLines})
	if err != nil {
		return ""
	}
	defer rc.Close()
	var lines []string
	sc := bufio.NewScanner(io.LimitReader(rc, maxLogTailBytes))
	for sc.Scan() {
		l := strings.TrimSpace(sc.Text())
		// Drop the RFC 3339 timestamp prefix the engine adds.
		if _, rest, ok := strings.Cut(l, " "); ok {
			l = rest
		}
		if l != "" {
			lines = append(lines, l)
		}
	}
	return strings.Join(lines, " | ")
}
