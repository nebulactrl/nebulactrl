package mesh

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/netip"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

const (
	// statusEvery bounds how often a running node is asked for its status (the
	// heartbeat runs every 10 seconds).
	statusEvery      = 10 * time.Second
	statusTimeout    = 8 * time.Second
	pingTimeout      = 3 * time.Second
	maxStatusJSON    = 4 << 20
	maxPeers         = 64
	logTailLines     = 15
	maxLogTailBytes  = 16 << 10
	restartLoopAfter = 2
	msgNeedsLogin    = "Device removed from the tailnet or key rejected. Re-provision."
)

type cachedStatus struct {
	containerID string
	st          agentprotocol.MeshNodeStatus
	at          time.Time
}

func attention(spec agentprotocol.MeshNodeSpec, msg string) agentprotocol.MeshNodeStatus {
	return agentprotocol.MeshNodeStatus{NodeID: spec.NodeID, Kind: spec.Kind, State: agentprotocol.MeshStateNeedsAttn, Message: msg}
}

func redactKey(s, key string) string {
	if len(key) >= 6 {
		return strings.ReplaceAll(s, key, "••••••")
	}
	return s
}

// tsStatus is the part of `tailscale status --json` the agent reads.
type tsStatus struct {
	BackendState string
	AuthURL      string
	Self         *struct {
		ID           string
		DNSName      string
		TailscaleIPs []string
	}
	Peer map[string]struct {
		HostName     string
		DNSName      string
		TailscaleIPs []string
		Tags         []string
		Online       bool
		CurAddr      string
		Relay        string
	}
}

func firstIPv4(ips []string) string {
	for _, s := range ips {
		if a, err := netip.ParseAddr(s); err == nil && a.Is4() {
			return s
		}
	}
	return ""
}

func hasAnyTag(have, want []string) bool {
	for _, h := range have {
		for _, w := range want {
			if h == w {
				return true
			}
		}
	}
	return false
}

// ParseStatus maps `tailscale status --json` onto the reported state and
// fills identity and peers. Peers are limited to devices carrying one of tags
// (the team's mesh tag) when tags is non-empty, at most maxPeers, sorted by IP.
// NodeID and Kind are the caller's.
func ParseStatus(raw []byte, tags []string) agentprotocol.MeshNodeStatus {
	return parseStatus(raw, tags, maxPeers)
}

// parseStatus is ParseStatus with an explicit peer cap (0 = no cap).
func parseStatus(raw []byte, tags []string, limit int) agentprotocol.MeshNodeStatus {
	provisioning := agentprotocol.MeshNodeStatus{State: agentprotocol.MeshStateProvisioning, Message: "Waiting for tailscaled to answer."}
	var js tsStatus
	if err := json.Unmarshal(raw, &js); err != nil {
		return provisioning
	}
	var st agentprotocol.MeshNodeStatus
	if js.Self != nil {
		st.DeviceID = js.Self.ID
		st.FQDN = strings.TrimSuffix(js.Self.DNSName, ".")
		st.IPs = append([]string(nil), js.Self.TailscaleIPs...)
		sort.Strings(st.IPs)
	}
	for _, p := range js.Peer {
		ip := firstIPv4(p.TailscaleIPs)
		if ip == "" || (len(tags) > 0 && !hasAnyTag(p.Tags, tags)) {
			continue
		}
		host := strings.TrimSuffix(p.DNSName, ".")
		if host == "" {
			host = p.HostName
		}
		st.Peers = append(st.Peers, agentprotocol.MeshPeer{
			IP: ip, Host: host, Online: p.Online, Direct: p.Online && p.CurAddr != "", Relay: p.Relay,
		})
	}
	sort.Slice(st.Peers, func(i, j int) bool { return st.Peers[i].IP < st.Peers[j].IP })
	if limit > 0 && len(st.Peers) > limit {
		st.Peers = st.Peers[:limit]
	}
	switch {
	case js.BackendState == "Running":
		st.State = agentprotocol.MeshStateOnline
	case js.BackendState == "NeedsLogin" || js.BackendState == "NeedsMachineAuth" || js.AuthURL != "":
		st.State, st.Message = agentprotocol.MeshStateNeedsAttn, msgNeedsLogin
	case js.BackendState == "Stopped":
		st.State, st.Message = agentprotocol.MeshStateOffline, "Tailscale is stopped."
	default: // Starting, NoState, ""
		st.State, st.Message = agentprotocol.MeshStateProvisioning, "Tailscale is starting."
	}
	return st
}

// readStatus execs `tailscale status --json` in the node (a fixed argv, no
// shell) and maps it.
func (m *Manager) readStatus(ctx context.Context, id string, peers bool, tags []string) agentprotocol.MeshNodeStatus {
	return m.readStatusLimit(ctx, id, peers, tags, maxPeers)
}

func (m *Manager) readStatusLimit(ctx context.Context, id string, peers bool, tags []string, limit int) agentprotocol.MeshNodeStatus {
	cctx, cancel := context.WithTimeout(ctx, statusTimeout)
	defer cancel()
	argv := []string{"tailscale", "status", "--json"}
	if !peers {
		argv = append(argv, "--peers=false")
	}
	var out, errb bytes.Buffer
	code, err := m.eng.ContainerExec(cctx, id, argv, docker.ExecSpec{}, &limitWriter{w: &out, n: maxStatusJSON}, &limitWriter{w: &errb, n: 4096})
	if err != nil || (code != 0 && out.Len() == 0) {
		return agentprotocol.MeshNodeStatus{State: agentprotocol.MeshStateProvisioning, Message: "Waiting for tailscaled to answer."}
	}
	return parseStatus(out.Bytes(), tags, limit)
}

// pingTarget asks the node to ping ip once over the tailnet (a disco ping: it
// proves the peer is reachable, not that the ACL lets a port through; the
// connectivity check dials the real path). nil = could not run.
func (m *Manager) pingTarget(ctx context.Context, id, ip string) (reachable *bool, latency time.Duration, direct bool) {
	cctx, cancel := context.WithTimeout(ctx, pingTimeout+2*time.Second)
	defer cancel()
	var out, errb bytes.Buffer
	code, err := m.eng.ContainerExec(cctx, id, []string{"tailscale", "ping", "--c=1", "--timeout=" + pingTimeout.String(), ip},
		docker.ExecSpec{}, &limitWriter{w: &out, n: 4096}, &limitWriter{w: &errb, n: 4096})
	if err != nil {
		return nil, 0, false
	}
	ok := code == 0 && strings.Contains(out.String(), "pong")
	if ok {
		latency, direct = parsePong(out.String())
	}
	return &ok, latency, direct
}

// parsePong reads "pong from host (ip) via 1.2.3.4:41641 in 3ms" or
// "... via DERP(nyc) in 41ms".
func parsePong(s string) (time.Duration, bool) {
	line := ""
	for _, l := range strings.Split(s, "\n") {
		if strings.Contains(l, "pong") {
			line = l
			break
		}
	}
	if line == "" {
		return 0, false
	}
	direct := !strings.Contains(line, "DERP(") && !strings.Contains(line, "peer-relay")
	if i := strings.LastIndex(line, " in "); i >= 0 {
		if d, err := time.ParseDuration(strings.TrimSpace(line[i+4:])); err == nil {
			return d, direct
		}
	}
	return 0, direct
}

// targetOf reads the TS_TAILNET_TARGET_IP of a running link node.
func targetOf(env []string) string {
	for _, e := range env {
		if v, ok := strings.CutPrefix(e, "TS_TAILNET_TARGET_IP="); ok {
			return v
		}
	}
	return ""
}

// authKeyOf reads the provisioning key a node container still carries (it is
// only present until the node is recreated without one): heartbeat statuses
// are redacted with it.
func authKeyOf(env []string) string {
	for _, e := range env {
		if v, ok := strings.CutPrefix(e, "TS_AUTHKEY="); ok {
			return v
		}
	}
	return ""
}

// mapExited maps a stopped node to the reported state: a restart loop (what a
// deleted device or a rejected key looks like) needs re-provisioning.
func (m *Manager) mapExited(ctx context.Context, id string, nodeID, kind string, info docker.ContainerInfo) agentprotocol.MeshNodeStatus {
	msg := fmt.Sprintf("The mesh node exited with code %d.", info.ExitCode)
	if tail := m.logTail(ctx, id); tail != "" {
		msg += " Last log lines: " + tail
	}
	st := agentprotocol.MeshNodeStatus{NodeID: nodeID, Kind: kind}
	if info.ExitCode != 0 {
		st.State, st.Message = agentprotocol.MeshStateNeedsAttn, msg+" "+msgNeedsLogin
		return st
	}
	st.State, st.Message = agentprotocol.MeshStateOffline, msg
	return st
}

// logTail returns the last log lines of the node on one line.
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

// nodeStatus reads the state of a running or stopped node container. spec is
// the desired spec when known (tags for the peer filter); the kind, target
// and environment otherwise come from the container itself.
func (m *Manager) nodeStatus(ctx context.Context, info docker.ContainerInfo, spec *agentprotocol.MeshNodeSpec) agentprotocol.MeshNodeStatus {
	nodeID, kind := info.Labels[agentprotocol.LabelMesh], info.Labels[agentprotocol.LabelMeshKind]
	var tags []string
	if spec != nil {
		tags = spec.Tags
	}
	var st agentprotocol.MeshNodeStatus
	switch {
	case info.Restarting || (!info.Running && info.RestartCount >= restartLoopAfter && info.ExitCode != 0):
		st = agentprotocol.MeshNodeStatus{NodeID: nodeID, Kind: kind, State: agentprotocol.MeshStateNeedsAttn,
			Message: "The mesh node keeps restarting. " + msgNeedsLogin}
	case !info.Running:
		st = m.mapExited(ctx, info.ID, nodeID, kind, info)
	default:
		m.mu.Lock()
		cached, ok := m.cache[nodeID]
		m.mu.Unlock()
		if ok && cached.containerID == info.ID && m.now().Sub(cached.at) < statusEvery {
			return cached.st
		}
		st = m.readStatus(ctx, info.ID, kind == agentprotocol.MeshKindServer, tags)
		st.NodeID, st.Kind = nodeID, kind
		if kind == agentprotocol.MeshKindLink && st.State == agentprotocol.MeshStateOnline {
			if target := targetOf(info.Env); target != "" {
				reach, _, _ := m.pingTarget(ctx, info.ID, target)
				st.TargetReachable = reach
				if reach != nil && !*reach {
					st.State = agentprotocol.MeshStateDegraded
					st.Message = "The remote server node " + target + " does not answer over the tailnet."
				}
			}
		}
		st.CheckedAt = m.now()
		m.mu.Lock()
		m.cache[nodeID] = cachedStatus{containerID: info.ID, st: st, at: m.now()}
		m.mu.Unlock()
		return st
	}
	st.CheckedAt = m.now()
	return st
}

// Status reports every mesh node of the host for the heartbeat; nil when the
// agent manages none. A running node is asked for its status at most every
// statusEvery.
func (m *Manager) Status(ctx context.Context) *agentprotocol.MeshStatus {
	cs, err := m.eng.ContainerList(ctx, docker.ListFilter{All: true, Labels: map[string]string{
		agentprotocol.LabelManaged: "true", agentprotocol.LabelMesh: "",
	}})
	if err != nil || len(cs) == 0 {
		if err != nil {
			m.log.Debug("observe mesh: list containers", "err", err)
		}
		return nil
	}
	sort.Slice(cs, func(i, j int) bool { return cs[i].Name < cs[j].Name })
	out := &agentprotocol.MeshStatus{TunDevice: m.tunDevice(), Nodes: make([]agentprotocol.MeshNodeStatus, len(cs))}
	var wg sync.WaitGroup
	for i, c := range cs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			st := m.nodeStatus(ctx, c, m.desiredSpec(c.Labels[agentprotocol.LabelMesh]))
			st.Message = redactKey(st.Message, authKeyOf(c.Env))
			out.Nodes[i] = st
		}()
	}
	wg.Wait()
	return out
}
