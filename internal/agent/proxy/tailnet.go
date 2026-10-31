package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// TailscaleImage is the pinned tailscale image of the managed tailnet node
// (v1.104.2, multi-arch index digest).
const TailscaleImage = "tailscale/tailscale:v1.104.2@sha256:b11583ad2e93e4671128538c88cf11f460444a5d7e449311dd1933a400ab849d"

const (
	// LabelTailnetSpec fingerprints the tailscale container's configuration
	// (never the auth key); LabelTailnetNetns records the ID of the proxy
	// container whose network namespace it shares.
	LabelTailnetSpec  = "nebulactrl.tailnet.spec"
	LabelTailnetNetns = "nebulactrl.tailnet.netns"

	tailscaleStateDir    = "/var/lib/tailscale"
	tailscaleHealthAddr  = "127.0.0.1:9002"
	tailnetStatusEvery   = 30 * time.Second
	tailnetStatusTimeout = 8 * time.Second
	maxTailnetStatusJSON = 4 << 20
)

// tailnetState is the manager's cached view of the tailnet node.
type tailnetState struct {
	status    *agentprotocol.TailnetStatus
	checkedAt time.Time
}

// tailscaleSpec is the managed tailnet container: tailscaled in kernel mode
// inside the proxy container's network namespace, so Traefik's egress to
// 100.64.0.0/10 and (with MagicDNS) *.ts.net goes through the tailnet. No
// privileged mode, no docker socket; it needs the TUN device and
// NET_ADMIN/NET_RAW. authKey is set only while provisioning: the node's
// identity then lives on the state volume.
func tailscaleSpec(t agentprotocol.TailnetSpec, netnsID, authKey string) docker.ContainerSpec {
	host := t.Hostname
	if host == "" {
		host = "nebula-proxy"
	}
	env := []string{
		"TS_USERSPACE=false",
		"TS_STATE_DIR=" + tailscaleStateDir,
		"TS_AUTH_ONCE=true",
		"TS_ACCEPT_DNS=false",
		"TS_HOSTNAME=" + host,
		"TS_ENABLE_HEALTH_CHECK=true",
		"TS_LOCAL_ADDR_PORT=" + tailscaleHealthAddr,
	}
	hash := specHash(append([]string{TailscaleImage}, env...)...)
	if authKey != "" {
		env = append(env, "TS_AUTHKEY="+authKey)
	}
	return docker.ContainerSpec{
		Name:        agentprotocol.TailscaleContainer,
		Image:       TailscaleImage,
		Env:         env,
		Labels:      map[string]string{agentprotocol.LabelManaged: "true", LabelTailnetSpec: hash, LabelTailnetNetns: netnsID},
		NetworkMode: "container:" + agentprotocol.ProxyContainer,
		Devices:     []docker.DeviceMapping{{HostPath: "/dev/net/tun"}},
		CapAdd:      []string{"NET_ADMIN", "NET_RAW"},
		Mounts:      []docker.VolumeMount{{Volume: agentprotocol.TailscaleStateVolume, Target: tailscaleStateDir}},
		Healthcheck: &docker.HealthSpec{
			Test:     []string{"CMD", "wget", "-q", "-O", "/dev/null", "http://" + tailscaleHealthAddr + "/healthz"},
			Interval: 30 * time.Second, Timeout: 5 * time.Second, StartPeriod: 30 * time.Second, Retries: 3,
		},
		RestartPolicy: "unless-stopped",
		LogConfig:     &docker.LogConfig{Type: "json-file", Config: map[string]string{"max-size": LogMaxSize, "max-file": LogMaxFile}},
	}
}

// SetTailnetAuthKey hands the manager the one-time auth key of the next
// provisioning. It lives in memory only and is dropped after the apply that
// uses it.
func (m *Manager) SetTailnetAuthKey(key string) {
	m.mu.Lock()
	m.tailnetKey = key
	m.mu.Unlock()
}

// TailnetStatus is the observed tailnet state for heartbeats; nil when no
// tailnet is configured.
func (m *Manager) TailnetStatus() *agentprotocol.TailnetStatus {
	m.mu.RLock()
	spec := m.proxyCfg.Tailnet
	cached := m.tn.status
	m.mu.RUnlock()
	if spec == nil {
		return cloneTailnetStatus(cached)
	}
	switch spec.Mode {
	case agentprotocol.TailnetHost:
		return m.hostTailnetStatus()
	case agentprotocol.TailnetManaged:
		if cached == nil {
			return &agentprotocol.TailnetStatus{Mode: spec.Mode, State: agentprotocol.TailnetStateProvisioning}
		}
		return cloneTailnetStatus(cached)
	}
	return cloneTailnetStatus(cached)
}

func cloneTailnetStatus(s *agentprotocol.TailnetStatus) *agentprotocol.TailnetStatus {
	if s == nil {
		return nil
	}
	c := *s
	c.IPs = append([]string(nil), s.IPs...)
	return &c
}

func (m *Manager) hostTailnetStatus() *agentprotocol.TailnetStatus {
	st := &agentprotocol.TailnetStatus{Mode: agentprotocol.TailnetHost, State: agentprotocol.TailnetStateNeedsAuth,
		Message: "Tailscale was not detected on this host."}
	if m.cfg.HostTailscale == nil {
		return st
	}
	if h := m.cfg.HostTailscale(); h != nil && h.Detected {
		st.State, st.Message, st.IPs = agentprotocol.TailnetStateOnline, "", append([]string(nil), h.IPs...)
	}
	return st
}

func (m *Manager) setTailnetStatus(s *agentprotocol.TailnetStatus) {
	m.mu.Lock()
	m.tn = tailnetState{status: s, checkedAt: time.Now()}
	m.mu.Unlock()
}

// reconcileTailnet makes the tailnet container match spec. It is idempotent:
// an existing container with the right configuration, attached to the current
// proxy container, is left alone. Provision recreates the node (container and
// state volume) with authKey; Remove deletes container and volume. A nil spec
// or mode off without Remove leaves any existing container untouched (an agent
// that lost its state must never delete a node's identity).
func (m *Manager) reconcileTailnet(ctx context.Context, spec *agentprotocol.TailnetSpec, authKey string) (err error) {
	if spec == nil {
		return nil
	}
	if authKey != "" {
		defer func() {
			if err != nil {
				err = errors.New(strings.ReplaceAll(err.Error(), authKey, "[redacted]"))
			}
		}()
	}
	switch {
	case spec.Remove:
		if err := m.removeTailnet(ctx); err != nil {
			return err
		}
		m.setTailnetStatus(&agentprotocol.TailnetStatus{Mode: agentprotocol.TailnetOff, State: agentprotocol.TailnetStateOff})
		return nil
	case spec.Mode == agentprotocol.TailnetManaged:
		return m.ensureManagedTailnet(ctx, *spec, authKey)
	case spec.Mode == agentprotocol.TailnetHost:
		m.setTailnetStatus(m.hostTailnetStatus())
	default:
		m.setTailnetStatus(&agentprotocol.TailnetStatus{Mode: agentprotocol.TailnetOff, State: agentprotocol.TailnetStateOff})
	}
	return nil
}

func (m *Manager) removeTailnet(ctx context.Context) error {
	eng := m.cfg.Engine
	if err := eng.ContainerRemove(ctx, agentprotocol.TailscaleContainer, true, false); err != nil && !errors.Is(err, docker.ErrNotFound) {
		return fmt.Errorf("remove tailscale container: %w", err)
	}
	if err := eng.RemoveVolume(ctx, agentprotocol.TailscaleStateVolume, true); err != nil && !errors.Is(err, docker.ErrNotFound) {
		return fmt.Errorf("remove tailscale state volume: %w", err)
	}
	return nil
}

func (m *Manager) ensureManagedTailnet(ctx context.Context, spec agentprotocol.TailnetSpec, authKey string) error {
	eng := m.cfg.Engine
	px, err := eng.ContainerInspect(ctx, agentprotocol.ProxyContainer)
	if err != nil {
		return fmt.Errorf("tailnet needs the proxy container: %w", err)
	}
	info, err := eng.ContainerInspect(ctx, agentprotocol.TailscaleContainer)
	exists := err == nil
	if err != nil && !errors.Is(err, docker.ErrNotFound) {
		return fmt.Errorf("inspect tailscale container: %w", err)
	}

	if spec.Provision {
		if authKey == "" {
			return errors.New("provisioning the tailnet node needs an auth key and none was sent")
		}
		if err := m.removeTailnet(ctx); err != nil {
			return err
		}
		if err := m.createTailnet(ctx, tailscaleSpec(spec, px.ID, authKey)); err != nil {
			return err
		}
		m.setTailnetStatus(&agentprotocol.TailnetStatus{Mode: agentprotocol.TailnetManaged, State: agentprotocol.TailnetStateProvisioning})
		m.refreshTailnet(ctx)
		return nil
	}

	want := tailscaleSpec(spec, px.ID, "")
	switch {
	case exists && info.Labels[LabelTailnetSpec] == want.Labels[LabelTailnetSpec] && info.Labels[LabelTailnetNetns] == px.ID:
		if !info.Running {
			if err := eng.ContainerStart(ctx, info.ID); err != nil {
				return fmt.Errorf("start tailscale container: %w", err)
			}
		}
	default:
		// Missing, reconfigured (hostname) or attached to a proxy container that
		// no longer exists: recreate. The node's identity stays on the volume.
		if exists {
			if err := eng.ContainerRemove(ctx, info.ID, true, false); err != nil && !errors.Is(err, docker.ErrNotFound) {
				return fmt.Errorf("remove tailscale container: %w", err)
			}
		}
		if err := m.createTailnet(ctx, want); err != nil {
			return err
		}
	}
	m.refreshTailnet(ctx)
	return nil
}

func (m *Manager) createTailnet(ctx context.Context, spec docker.ContainerSpec) error {
	eng := m.cfg.Engine
	if err := eng.EnsureVolume(ctx, agentprotocol.TailscaleStateVolume, map[string]string{agentprotocol.LabelManaged: "true"}); err != nil {
		return fmt.Errorf("create tailscale state volume: %w", err)
	}
	if err := m.ensureImageRef(ctx, TailscaleImage); err != nil {
		return fmt.Errorf("pull tailscale image: %w", err)
	}
	id, err := eng.ContainerCreate(ctx, spec)
	if err != nil {
		return fmt.Errorf("create tailscale container: %w", err)
	}
	if err := eng.ContainerStart(ctx, id); err != nil {
		_ = eng.ContainerRemove(context.WithoutCancel(ctx), id, true, false)
		return fmt.Errorf("start tailscale container: %w", err)
	}
	return nil
}

// refreshTailnet reads `tailscale status --json` from the managed container
// and caches the result. It never fails: problems become the reported state.
func (m *Manager) refreshTailnet(ctx context.Context) {
	m.setTailnetStatus(m.readTailnetStatus(ctx))
}

func (m *Manager) readTailnetStatus(ctx context.Context) *agentprotocol.TailnetStatus {
	st := &agentprotocol.TailnetStatus{Mode: agentprotocol.TailnetManaged}
	cctx, cancel := context.WithTimeout(ctx, tailnetStatusTimeout)
	defer cancel()
	info, err := m.cfg.Engine.ContainerInspect(cctx, agentprotocol.TailscaleContainer)
	switch {
	case errors.Is(err, docker.ErrNotFound):
		st.State, st.Message = agentprotocol.TailnetStateOffline, "The tailscale container does not exist."
		return st
	case err != nil:
		st.State, st.Message = agentprotocol.TailnetStateOffline, "Cannot inspect the tailscale container: "+err.Error()
		return st
	case !info.Running:
		st.State, st.Message = agentprotocol.TailnetStateOffline, "The tailscale container is "+info.State+"."
		return st
	}
	var out, errb bytes.Buffer
	code, err := m.cfg.Engine.ContainerExec(cctx, info.ID, []string{"tailscale", "status", "--json", "--peers=false"},
		docker.ExecSpec{}, &limitWriter{w: &out, n: maxTailnetStatusJSON}, &limitWriter{w: &errb, n: 4096})
	if err != nil || (code != 0 && out.Len() == 0) {
		st.State = agentprotocol.TailnetStateProvisioning
		st.Message = "Waiting for tailscaled to answer."
		return st
	}
	return parseTailnetStatus(out.Bytes(), st)
}

// parseTailnetStatus maps `tailscale status --json` onto TailnetStatus.
func parseTailnetStatus(raw []byte, st *agentprotocol.TailnetStatus) *agentprotocol.TailnetStatus {
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
		st.State, st.Message = agentprotocol.TailnetStateProvisioning, "Waiting for tailscaled to answer."
		return st
	}
	if js.Self != nil {
		st.DeviceID = js.Self.ID
		st.FQDN = strings.TrimSuffix(js.Self.DNSName, ".")
		st.IPs = append([]string(nil), js.Self.TailscaleIPs...)
	}
	switch js.BackendState {
	case "Running":
		st.State = agentprotocol.TailnetStateOnline
	case "NeedsLogin", "NeedsMachineAuth":
		st.State = agentprotocol.TailnetStateNeedsAuth
		st.Message = "Device removed from the tailnet or key rejected. Re-provision."
	case "Stopped":
		st.State, st.Message = agentprotocol.TailnetStateOffline, "Tailscale is stopped."
	default: // Starting, NoState, ""
		st.State, st.Message = agentprotocol.TailnetStateProvisioning, "Tailscale is starting."
	}
	sort.Strings(st.IPs)
	return st
}

// limitWriter drops everything past n bytes so a hostile or huge exec output
// cannot grow the agent's memory.
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

// refreshTailnetIfDue re-reads the managed node's status at most every
// tailnetStatusEvery; the health loop calls it.
func (m *Manager) refreshTailnetIfDue(ctx context.Context) {
	m.mu.RLock()
	spec, at := m.appliedCfg.Tailnet, m.tn.checkedAt
	m.mu.RUnlock()
	if spec == nil || spec.Mode != agentprotocol.TailnetManaged || time.Since(at) < tailnetStatusEvery {
		return
	}
	m.refreshTailnet(ctx)
}
