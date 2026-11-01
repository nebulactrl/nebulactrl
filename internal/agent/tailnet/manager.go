package tailnet

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/internal/agent/proxy"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

const (
	// statusEvery bounds how often a running sidecar is asked for its status
	// (the observed report runs every 15 seconds).
	statusEvery      = 45 * time.Second
	statusTimeout    = 8 * time.Second
	maxStatusJSON    = 4 << 20
	logTailLines     = 15
	maxLogTailBytes  = 16 << 10
	defaultWait      = 90 * time.Second
	defaultPoll      = 2 * time.Second
	restartLoopAfter = 2
	msgNeedsLogin    = "Device removed from the tailnet or key rejected. Re-provision."
)

// Manager creates, updates, observes and removes the tailnet sidecars of a
// host. Callers serialize Ensure and Remove per service (the engine's service
// lock); Observe may run concurrently with them.
type Manager struct {
	eng docker.Engine
	log *slog.Logger
	now func() time.Time

	// WaitRunning bounds how long Ensure waits for the node to come up and Poll
	// is the interval between status reads (tests shorten both).
	WaitRunning time.Duration
	Poll        time.Duration

	// EnsureNetwork creates the managed environment network name (internal:
	// the egress-blocked sibling) when it does not exist. The engine sets it to
	// allocate subnets from the agent's address pool; nil lets the daemon pick.
	EnsureNetwork func(ctx context.Context, name string, labels map[string]string, internal bool) error

	mu    sync.Mutex
	cache map[string]cachedStatus // serviceID -> last status read from the node
}

type cachedStatus struct {
	containerID string
	st          agentprotocol.ServiceTailnetStatus
	at          time.Time
}

// New returns a Manager.
func New(eng docker.Engine, log *slog.Logger) *Manager {
	if log == nil {
		log = slog.Default()
	}
	return &Manager{eng: eng, log: log.With("component", "tailnet"), now: time.Now, WaitRunning: defaultWait, Poll: defaultPoll, cache: map[string]cachedStatus{}}
}

func (m *Manager) ensureNetwork(ctx context.Context, name string, labels map[string]string, internal bool) error {
	switch {
	case m.EnsureNetwork != nil:
		return m.EnsureNetwork(ctx, name, labels, internal)
	case internal:
		return m.eng.EnsureInternalNetwork(ctx, name, labels)
	default:
		return m.eng.EnsureNetwork(ctx, name, labels)
	}
}

func (m *Manager) forget(serviceID string) {
	m.mu.Lock()
	delete(m.cache, serviceID)
	m.mu.Unlock()
}

func attention(msg string) agentprotocol.ServiceTailnetStatus {
	return agentprotocol.ServiceTailnetStatus{State: agentprotocol.ServiceTailnetNeedsAttn, Message: msg}
}

func redactKey(s, key string) string {
	if len(key) >= 6 {
		return strings.ReplaceAll(s, key, "••••••")
	}
	return s
}

// Ensure makes the sidecar match spec and waits for the node to come up. It
// never returns an error: every problem becomes the reported state, so a
// tailnet failure can never fail the app deployment. authKey is needed only
// when spec.Provision is set and is redacted from everything returned or
// logged.
func (m *Manager) Ensure(ctx context.Context, spec agentprotocol.ServiceTailnetSpec, authKey string, logf func(string)) agentprotocol.ServiceTailnetStatus {
	if logf == nil {
		logf = func(string) {}
	}
	logf = redacting(logf, authKey)
	st := m.ensure(ctx, spec, authKey, logf)
	st.Message = redactKey(st.Message, authKey)
	st.IdentityHash = spec.IdentityHash
	return st
}

func redacting(logf func(string), key string) func(string) {
	return func(s string) { logf(redactKey(s, key)) }
}

func (m *Manager) ensure(ctx context.Context, spec agentprotocol.ServiceTailnetSpec, authKey string, logf func(string)) agentprotocol.ServiceTailnetStatus {
	if err := Validate(spec); err != nil {
		return attention("Invalid tailnet configuration: " + err.Error())
	}
	if spec.Provision && authKey == "" {
		return attention("Provisioning the tailnet node needs an auth key and none was sent.")
	}
	m.forget(spec.ServiceID)
	name := agentprotocol.TailnetContainerName(spec.ServiceID)
	want := ContainerSpec(spec, "")

	if err := m.ensureNetwork(ctx, agentprotocol.EnvNetwork(spec.EnvironmentID), map[string]string{
		agentprotocol.LabelManaged: "true", agentprotocol.LabelEnvironment: spec.EnvironmentID,
	}, false); err != nil {
		return attention("Could not create the environment network: " + err.Error())
	}
	if spec.BlockEgress {
		if err := m.ensureNetwork(ctx, agentprotocol.EnvInternalNetwork(spec.EnvironmentID), map[string]string{
			agentprotocol.LabelManaged: "true", agentprotocol.LabelEnvironment: spec.EnvironmentID,
		}, true); err != nil {
			return attention("Could not create the internal environment network: " + err.Error())
		}
	}
	info, err := m.eng.ContainerInspect(ctx, name)
	exists := err == nil
	if err != nil && !errors.Is(err, docker.ErrNotFound) {
		return attention("Could not inspect the tailnet sidecar: " + err.Error())
	}

	provisioned := false
	create := !exists
	switch {
	case spec.Provision:
		logf("Tailnet: provisioning " + spec.Hostname)
		if err := m.removeAll(ctx, spec.ServiceID, true); err != nil {
			return attention(err.Error())
		}
		create, provisioned = true, true
	case exists && info.Labels[agentprotocol.LabelTailnetSpec] != want.Labels[agentprotocol.LabelTailnetSpec]:
		// Reconfigured (image, login server, network): recreate on the same
		// state volumes, the identity survives.
		logf("Tailnet: recreating the sidecar (its configuration changed)")
		if err := m.eng.ContainerRemove(ctx, info.ID, true, false); err != nil && !errors.Is(err, docker.ErrNotFound) {
			return attention("Could not remove the old sidecar: " + err.Error())
		}
		create = true
	}

	id := info.ID
	if create {
		if err := m.ensureImage(ctx); err != nil {
			return attention("Could not pull the tailscale image: " + err.Error())
		}
		for _, v := range []string{agentprotocol.TailnetStateVolumeName(spec.ServiceID), agentprotocol.TailnetConfigVolumeName(spec.ServiceID)} {
			if err := m.eng.EnsureVolume(ctx, v, map[string]string{agentprotocol.LabelManaged: "true", agentprotocol.LabelTailnet: spec.ServiceID}); err != nil {
				return attention("Could not create the sidecar volume: " + err.Error())
			}
		}
		cs := ContainerSpec(spec, authKey)
		id, err = m.eng.ContainerCreate(ctx, cs)
		if err != nil {
			return attention(redactKey("Could not create the tailnet sidecar: "+err.Error(), authKey))
		}
		// The serve config is in place before the first start.
		if err := m.writeServe(ctx, id, spec, ""); err != nil {
			_ = m.eng.ContainerRemove(context.WithoutCancel(ctx), id, true, false)
			return attention("Could not write the serve config: " + err.Error())
		}
		if err := m.eng.ContainerStart(ctx, id); err != nil {
			_ = m.eng.ContainerRemove(context.WithoutCancel(ctx), id, true, false)
			return attention(redactKey("Could not start the tailnet sidecar: "+err.Error(), authKey))
		}
	} else {
		if !info.Running {
			if err := m.eng.ContainerStart(ctx, id); err != nil {
				return attention("Could not start the tailnet sidecar: " + err.Error())
			}
		}
		// Exposures change live: the watched file is rewritten, no restart. A
		// running Headscale node already has its DNS name, so the web exposure is
		// never interrupted.
		fqdn := ""
		if spec.Provider == agentprotocol.TailnetProviderHeadscale && info.Running {
			fqdn = m.readStatus(ctx, id).FQDN
		}
		if err := m.writeServe(ctx, id, spec, fqdn); err != nil {
			return attention("Could not update the serve config: " + err.Error())
		}
	}

	st := m.waitOnline(ctx, id, spec)
	st.Provisioned = provisioned
	if st.State == agentprotocol.ServiceTailnetOnline && spec.Provider == agentprotocol.TailnetProviderHeadscale && spec.Web && st.FQDN != "" {
		// Headscale: the Web key must be the node's real DNS name.
		if err := m.writeServe(ctx, id, spec, st.FQDN); err != nil {
			st.State, st.Message = agentprotocol.ServiceTailnetNeedsAttn, "Could not write the serve config: "+err.Error()
		}
	}
	m.mu.Lock()
	m.cache[spec.ServiceID] = cachedStatus{containerID: id, st: st, at: m.now()}
	m.mu.Unlock()
	if st.State == agentprotocol.ServiceTailnetOnline {
		logf("Tailnet: " + spec.Hostname + " is online as " + st.FQDN)
	} else {
		logf("Tailnet: " + st.State + ": " + st.Message)
	}
	return st
}

// writeServe renders and copies the serve config into the sidecar.
func (m *Manager) writeServe(ctx context.Context, id string, spec agentprotocol.ServiceTailnetSpec, fqdn string) error {
	cfg, err := ServeConfig(spec, fqdn)
	if err != nil {
		return err
	}
	tarball, err := serveTar(cfg)
	if err != nil {
		return err
	}
	return m.eng.CopyToContainer(ctx, id, configDir, tarball)
}

func (m *Manager) ensureImage(ctx context.Context) error {
	if _, err := m.eng.ImageInspect(ctx, proxy.TailscaleImage); err == nil {
		return nil
	} else if !errors.Is(err, docker.ErrNotFound) {
		return err
	}
	return m.eng.ImagePull(ctx, proxy.TailscaleImage, "", nil)
}

// waitOnline polls the node until it is online, the container dies or the wait
// ends, and returns the last state seen.
func (m *Manager) waitOnline(ctx context.Context, id string, spec agentprotocol.ServiceTailnetSpec) agentprotocol.ServiceTailnetStatus {
	deadline := m.now().Add(m.WaitRunning)
	var last agentprotocol.ServiceTailnetStatus
	for {
		info, err := m.eng.ContainerInspect(ctx, id)
		switch {
		case err != nil:
			return attention("Cannot inspect the tailnet sidecar: " + err.Error())
		case !info.Running && !info.Restarting:
			return m.exited(ctx, id, info)
		case info.Running:
			last = m.readStatus(ctx, id)
			if last.State == agentprotocol.ServiceTailnetOnline {
				return last
			}
		}
		if ctx.Err() != nil {
			return attention("The agent stopped while waiting for the tailnet node.")
		}
		if !m.now().Before(deadline) {
			break
		}
		select {
		case <-ctx.Done():
			return attention("The agent stopped while waiting for the tailnet node.")
		case <-time.After(m.Poll):
		}
	}
	switch last.State {
	case agentprotocol.ServiceTailnetNeedsAttn:
		if tail := m.logTail(ctx, id); tail != "" {
			last.Message += " Last log lines: " + tail
		}
		return last
	case "":
		return attention("The tailnet node did not answer in time.")
	}
	last.State = agentprotocol.ServiceTailnetProvisioning
	last.Message = "The node is still coming up."
	return last
}

// exited maps a stopped sidecar to the reported state: a restart loop (what a
// deleted device or a rejected key looks like) needs re-provisioning.
func (m *Manager) exited(ctx context.Context, id string, info docker.ContainerInfo) agentprotocol.ServiceTailnetStatus {
	msg := fmt.Sprintf("The tailnet sidecar exited with code %d.", info.ExitCode)
	if tail := m.logTail(ctx, id); tail != "" {
		msg += " Last log lines: " + tail
	}
	if info.ExitCode != 0 {
		return attention(msg + " " + msgNeedsLogin)
	}
	return agentprotocol.ServiceTailnetStatus{State: agentprotocol.ServiceTailnetOffline, Message: msg}
}

// logTail returns the last log lines of the sidecar on one line.
func (m *Manager) logTail(ctx context.Context, id string) string {
	cctx, cancel := context.WithTimeout(ctx, statusTimeout)
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

// readStatus execs `tailscale status --json` in the sidecar (a fixed argv, no
// shell) and maps it.
func (m *Manager) readStatus(ctx context.Context, id string) agentprotocol.ServiceTailnetStatus {
	cctx, cancel := context.WithTimeout(ctx, statusTimeout)
	defer cancel()
	var out, errb bytes.Buffer
	code, err := m.eng.ContainerExec(cctx, id, []string{"tailscale", "status", "--json", "--peers=false"},
		docker.ExecSpec{}, &limitWriter{w: &out, n: maxStatusJSON}, &limitWriter{w: &errb, n: 4096})
	if err != nil || (code != 0 && out.Len() == 0) {
		return agentprotocol.ServiceTailnetStatus{State: agentprotocol.ServiceTailnetProvisioning, Message: "Waiting for tailscaled to answer."}
	}
	return ParseStatus(out.Bytes())
}

// ParseStatus maps `tailscale status --json` onto the reported state.
func ParseStatus(raw []byte) agentprotocol.ServiceTailnetStatus {
	var js struct {
		BackendState string
		AuthURL      string
		Self         *struct {
			ID           string
			DNSName      string
			TailscaleIPs []string
		}
	}
	if err := json.Unmarshal(raw, &js); err != nil {
		return agentprotocol.ServiceTailnetStatus{State: agentprotocol.ServiceTailnetProvisioning, Message: "Waiting for tailscaled to answer."}
	}
	var st agentprotocol.ServiceTailnetStatus
	if js.Self != nil {
		st.DeviceID = js.Self.ID
		st.FQDN = strings.TrimSuffix(js.Self.DNSName, ".")
		st.IPs = append([]string(nil), js.Self.TailscaleIPs...)
		sort.Strings(st.IPs)
	}
	switch {
	case js.BackendState == "Running":
		st.State = agentprotocol.ServiceTailnetOnline
	case js.BackendState == "NeedsLogin" || js.BackendState == "NeedsMachineAuth" || js.AuthURL != "":
		st.State, st.Message = agentprotocol.ServiceTailnetNeedsAttn, msgNeedsLogin
	case js.BackendState == "Stopped":
		st.State, st.Message = agentprotocol.ServiceTailnetOffline, "Tailscale is stopped."
	default: // Starting, NoState, ""
		st.State, st.Message = agentprotocol.ServiceTailnetProvisioning, "Tailscale is starting."
	}
	return st
}

// limitWriter drops everything past n bytes so a huge exec output cannot grow
// the agent's memory.
type limitWriter struct {
	w io.Writer
	n int
}

func (l *limitWriter) Write(p []byte) (int, error) {
	if l.n <= 0 {
		return len(p), nil
	}
	if len(p) > l.n {
		_, _ = l.w.Write(p[:l.n])
		l.n = 0
		return len(p), nil
	}
	l.n -= len(p)
	return l.w.Write(p)
}

// removeAll removes the sidecar container and, with volumes, its state and
// config volumes.
func (m *Manager) removeAll(ctx context.Context, serviceID string, volumes bool) error {
	name := agentprotocol.TailnetContainerName(serviceID)
	grace := 5 * time.Second
	if info, err := m.eng.ContainerInspect(ctx, name); err == nil {
		if info.Running || info.Restarting {
			if err := m.eng.ContainerStop(ctx, info.ID, &grace); err != nil && !errors.Is(err, docker.ErrNotFound) {
				return fmt.Errorf("stop the tailnet sidecar: %w", err)
			}
		}
		if err := m.eng.ContainerRemove(ctx, info.ID, true, false); err != nil && !errors.Is(err, docker.ErrNotFound) {
			return fmt.Errorf("remove the tailnet sidecar: %w", err)
		}
	} else if !errors.Is(err, docker.ErrNotFound) {
		return fmt.Errorf("inspect the tailnet sidecar: %w", err)
	}
	if volumes {
		for _, v := range []string{agentprotocol.TailnetStateVolumeName(serviceID), agentprotocol.TailnetConfigVolumeName(serviceID)} {
			if err := m.eng.RemoveVolume(ctx, v, true); err != nil && !errors.Is(err, docker.ErrNotFound) {
				return fmt.Errorf("remove volume %s: %w", v, err)
			}
		}
	}
	return nil
}

// Remove deletes the sidecar and its volumes. The device itself is deleted by
// the control plane.
func (m *Manager) Remove(ctx context.Context, serviceID string, logf func(string)) agentprotocol.ServiceTailnetStatus {
	m.forget(serviceID)
	if err := m.removeAll(ctx, serviceID, true); err != nil {
		return attention("Could not remove the tailnet sidecar: " + err.Error())
	}
	if logf != nil {
		logf("Tailnet: removed the sidecar and its volumes")
	}
	return agentprotocol.ServiceTailnetStatus{State: agentprotocol.ServiceTailnetRemoved, Removed: true}
}

// Observe reports the sidecar of a service; nil when it has none. A running
// sidecar is asked for its status at most every statusEvery.
func (m *Manager) Observe(ctx context.Context, serviceID string) *agentprotocol.ServiceTailnetStatus {
	cs, err := m.eng.ContainerList(ctx, docker.ListFilter{All: true, Labels: map[string]string{
		agentprotocol.LabelManaged: "true", agentprotocol.LabelTailnet: serviceID,
	}})
	if err != nil || len(cs) == 0 {
		if err != nil {
			m.log.Debug("observe tailnet: list containers", "service_id", serviceID, "err", err)
		}
		return nil
	}
	c := cs[0]
	var st agentprotocol.ServiceTailnetStatus
	switch {
	case c.Restarting || (!c.Running && c.RestartCount >= restartLoopAfter && c.ExitCode != 0):
		st = attention("The tailnet sidecar keeps restarting. " + msgNeedsLogin)
	case !c.Running:
		st = agentprotocol.ServiceTailnetStatus{State: agentprotocol.ServiceTailnetOffline, Message: "The tailnet sidecar is " + c.State + "."}
	default:
		m.mu.Lock()
		cached, ok := m.cache[serviceID]
		m.mu.Unlock()
		if ok && cached.containerID == c.ID && m.now().Sub(cached.at) < statusEvery {
			st = cached.st
		} else {
			st = m.readStatus(ctx, c.ID)
			m.mu.Lock()
			m.cache[serviceID] = cachedStatus{containerID: c.ID, st: st, at: m.now()}
			m.mu.Unlock()
		}
	}
	return &st
}
