package mesh

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/internal/agent/proxy"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

const (
	defaultWait  = 90 * time.Second
	defaultPoll  = 2 * time.Second
	syncParallel = 4
	removeGrace  = 5 * time.Second
	msgNoTun     = "/dev/net/tun is not available on this server: a link node needs the TUN device."
)

// Manager creates, updates, observes and removes the mesh nodes of a host. It
// mirrors internal/agent/tailnet.Manager: it never returns an error for a node
// problem, every problem becomes the reported state, and an auth key is
// redacted from everything returned or logged. Whole-set operations (Sync,
// Remove) are serialised; per-node work holds the node's own lock, so Observe
// and Refresh may run concurrently with them.
type Manager struct {
	eng docker.Engine
	log *slog.Logger
	now func() time.Time

	// WaitRunning bounds how long a newly created node is waited for and Poll
	// is the interval between status reads (tests shorten both).
	WaitRunning time.Duration
	Poll        time.Duration

	// EnsureNetwork creates a managed network when it does not exist. The
	// engine sets it to allocate subnets from the agent's address pool; nil
	// lets the daemon pick.
	EnsureNetwork func(ctx context.Context, name string, labels map[string]string, internal bool) error
	// TunDevice reports whether /dev/net/tun exists (nil: false).
	TunDevice func() bool
	// Access makes the container networks reachable from the agent for the
	// duration of a probe and returns the release function (nil: no-op). The
	// engine's probeAccess attaches an agent that runs in a container.
	Access func(ctx context.Context, networks ...string) (release func())
	// Dial opens the connection of mesh_check (nil: net.Dialer).
	Dial func(ctx context.Context, network, addr string) (net.Conn, error)

	syncMu  sync.Mutex // whole-set operations
	imageMu sync.Mutex // one image pull at a time

	mu      sync.Mutex
	locks   map[string]*sync.Mutex
	cache   map[string]cachedStatus
	serve   map[string]string                     // nodeID -> hash of the serve config last written
	desired map[string]agentprotocol.MeshNodeSpec // the last applied set (memory only: it rides every hello)
}

// New returns a Manager.
func New(eng docker.Engine, log *slog.Logger) *Manager {
	if log == nil {
		log = slog.Default()
	}
	return &Manager{
		eng: eng, log: log.With("component", "mesh"), now: time.Now,
		WaitRunning: defaultWait, Poll: defaultPoll,
		locks: map[string]*sync.Mutex{}, cache: map[string]cachedStatus{},
		serve: map[string]string{}, desired: map[string]agentprotocol.MeshNodeSpec{},
	}
}

func (m *Manager) tunDevice() bool { return m.TunDevice != nil && m.TunDevice() }

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

func (m *Manager) lockNode(id string) (unlock func()) {
	m.mu.Lock()
	l, ok := m.locks[id]
	if !ok {
		l = &sync.Mutex{}
		m.locks[id] = l
	}
	m.mu.Unlock()
	l.Lock()
	return l.Unlock
}

// forget drops what is cached about a node: its status and the serve config it
// was last given.
func (m *Manager) forget(id string) {
	m.mu.Lock()
	delete(m.cache, id)
	delete(m.serve, id)
	m.mu.Unlock()
}

// forgetStatus drops only the cached status of a node: a sync re-reads it, but
// the serve config the container already has stays known (it is keyed by the
// container id, so a recreated node is written again).
func (m *Manager) forgetStatus(id string) {
	m.mu.Lock()
	delete(m.cache, id)
	m.mu.Unlock()
}

// desiredSpec returns the last applied spec of a node, or nil.
func (m *Manager) desiredSpec(id string) *agentprotocol.MeshNodeSpec {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.desired[id]; ok {
		return &s
	}
	return nil
}

// desiredSpecs returns the last applied set sorted by node id.
func (m *Manager) desiredSpecs() []agentprotocol.MeshNodeSpec {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]agentprotocol.MeshNodeSpec, 0, len(m.desired))
	for _, s := range m.desired {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].NodeID < out[j].NodeID })
	return out
}

func redacting(logf func(string), key string) func(string) {
	if logf == nil {
		return func(string) {}
	}
	return func(s string) { logf(redactKey(s, key)) }
}

func (m *Manager) ensureImage(ctx context.Context) error {
	m.imageMu.Lock()
	defer m.imageMu.Unlock()
	if _, err := m.eng.ImageInspect(ctx, proxy.TailscaleImage); err == nil {
		return nil
	} else if !errors.Is(err, docker.ErrNotFound) {
		return err
	}
	return m.eng.ImagePull(ctx, proxy.TailscaleImage, "", nil)
}

// Sync applies the COMPLETE desired mesh set of the server: nodes whose spec
// fingerprint changed are recreated on their state volume, exposures and
// networks change live, nodes that are no longer desired are removed with their
// volumes. keys holds the single-use auth keys by node id; a key is used only
// for a node whose spec asks for provisioning.
func (m *Manager) Sync(ctx context.Context, p agentprotocol.SyncMeshPayload, keys map[string]string, logf func(string)) agentprotocol.MeshStatus {
	if logf == nil {
		logf = func(string) {}
	}
	m.syncMu.Lock()
	defer m.syncMu.Unlock()

	statuses := make([]agentprotocol.MeshNodeStatus, len(p.Nodes))
	todo := make([]int, 0, len(p.Nodes))
	keep := map[string]bool{} // never removed as strays, valid or not
	desired := map[string]agentprotocol.MeshNodeSpec{}
	for i, n := range p.Nodes {
		keep[n.NodeID] = true
		switch err := Validate(n); {
		case err != nil:
			statuses[i] = attention(n, "Invalid mesh node: "+err.Error())
		case desired[n.NodeID].NodeID != "":
			statuses[i] = attention(n, "The node is listed twice.")
		default:
			desired[n.NodeID] = n
			todo = append(todo, i)
		}
	}
	m.mu.Lock()
	m.desired = desired
	m.mu.Unlock()

	removed := m.removeStrays(ctx, keep, logf)

	sem := make(chan struct{}, syncParallel)
	var wg sync.WaitGroup
	for _, i := range todo {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			n := p.Nodes[i]
			statuses[i] = m.ensureNode(ctx, n, keys[agentprotocol.SecretMeshKeyPrefix+n.NodeID], logf)
		}()
	}
	wg.Wait()
	return agentprotocol.MeshStatus{Nodes: append(statuses, removed...), TunDevice: m.tunDevice()}
}

// removeStrays removes every mesh container whose node is not in keep.
func (m *Manager) removeStrays(ctx context.Context, keep map[string]bool, logf func(string)) []agentprotocol.MeshNodeStatus {
	cs, err := m.eng.ContainerList(ctx, docker.ListFilter{All: true, Labels: map[string]string{
		agentprotocol.LabelManaged: "true", agentprotocol.LabelMesh: "",
	}})
	if err != nil {
		m.log.Warn("sync mesh: list containers", "err", err)
		return nil
	}
	var out []agentprotocol.MeshNodeStatus
	for _, c := range cs {
		id := c.Labels[agentprotocol.LabelMesh]
		if keep[id] {
			continue
		}
		out = append(out, m.removeNode(ctx, id, c.Labels[agentprotocol.LabelMeshKind], logf))
	}
	return out
}

// Remove removes the named nodes (containers and volumes). The devices
// themselves are deleted by the control plane.
func (m *Manager) Remove(ctx context.Context, ids []string, logf func(string)) agentprotocol.MeshStatus {
	m.syncMu.Lock()
	defer m.syncMu.Unlock()
	out := agentprotocol.MeshStatus{TunDevice: m.tunDevice()}
	for _, id := range ids {
		kind := ""
		if cs, err := m.eng.ContainerList(ctx, docker.ListFilter{All: true, Labels: map[string]string{
			agentprotocol.LabelManaged: "true", agentprotocol.LabelMesh: id,
		}}); err == nil && len(cs) > 0 {
			kind = cs[0].Labels[agentprotocol.LabelMeshKind]
		}
		m.mu.Lock()
		delete(m.desired, id)
		m.mu.Unlock()
		out.Nodes = append(out.Nodes, m.removeNode(ctx, id, kind, logf))
	}
	return out
}

// removeNode removes a node's container(s) and both volumes.
func (m *Manager) removeNode(ctx context.Context, id, kind string, logf func(string)) agentprotocol.MeshNodeStatus {
	unlock := m.lockNode(id)
	defer unlock()
	m.forget(id)
	st := agentprotocol.MeshNodeStatus{NodeID: id, Kind: kind, State: agentprotocol.MeshStateRemoved}
	if !idRe.MatchString(id) {
		st.State, st.Message = agentprotocol.MeshStateNeedsAttn, "Invalid node id."
		return st
	}
	if err := m.removeContainers(ctx, id); err != nil {
		st.State, st.Message = agentprotocol.MeshStateNeedsAttn, "Could not remove the mesh node: "+err.Error()
		return st
	}
	for _, v := range []string{agentprotocol.MeshStateVolume(id), agentprotocol.MeshConfigVolume(id)} {
		if err := m.eng.RemoveVolume(ctx, v, true); err != nil && !errors.Is(err, docker.ErrNotFound) {
			st.State, st.Message = agentprotocol.MeshStateNeedsAttn, "Could not remove the mesh node volume: "+err.Error()
			return st
		}
	}
	if logf != nil {
		logf("Mesh: removed node " + id)
	}
	return st
}

// removeContainers stops and removes every container of the node.
func (m *Manager) removeContainers(ctx context.Context, id string) error {
	cs, err := m.eng.ContainerList(ctx, docker.ListFilter{All: true, Labels: map[string]string{
		agentprotocol.LabelManaged: "true", agentprotocol.LabelMesh: id,
	}})
	if err != nil {
		return fmt.Errorf("list: %w", err)
	}
	for _, c := range cs {
		if err := m.removeContainer(ctx, c); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) removeContainer(ctx context.Context, c docker.ContainerInfo) error {
	grace := removeGrace
	if c.Running || c.Restarting {
		if err := m.eng.ContainerStop(ctx, c.ID, &grace); err != nil && !errors.Is(err, docker.ErrNotFound) {
			return fmt.Errorf("stop %s: %w", c.Name, err)
		}
	}
	if err := m.eng.ContainerRemove(ctx, c.ID, true, false); err != nil && !errors.Is(err, docker.ErrNotFound) {
		return fmt.Errorf("remove %s: %w", c.Name, err)
	}
	return nil
}

// ensureNode makes one node match spec and, when it was just created, waits
// for it to come up. It never returns an error.
func (m *Manager) ensureNode(ctx context.Context, spec agentprotocol.MeshNodeSpec, key string, logf func(string)) (st agentprotocol.MeshNodeStatus) {
	unlock := m.lockNode(spec.NodeID)
	defer unlock()
	logf = redacting(logf, key)
	defer func() {
		st.Message = redactKey(st.Message, key)
		st.NodeID, st.Kind = spec.NodeID, spec.Kind
	}()
	m.forgetStatus(spec.NodeID)

	if spec.Provision && key == "" && m.nodeMissing(ctx, spec) {
		return attention(spec, "Provisioning the mesh node needs an auth key and none was sent.")
	}
	if err := m.ensureNodeNetworks(ctx, spec); err != nil {
		return attention(spec, err.Error())
	}
	name := ContainerName(spec)
	want := ContainerSpec(spec, "")
	info, err := m.eng.ContainerInspect(ctx, name)
	exists := err == nil
	if err != nil && !errors.Is(err, docker.ErrNotFound) {
		return attention(spec, "Could not inspect the mesh node: "+err.Error())
	}

	provision, create, carryFP := spec.Provision, !exists, ""
	if provision && exists && (key == "" || info.Labels[labelKeyFP] == KeyFingerprint(key)) {
		// A hello repeats the sync (the very key already enrolled the node, or
		// no key rides the replay): wiping the identity again would burn another
		// single-use key and change the node's tailnet address.
		provision = false
	}
	switch {
	case provision:
		logf("Mesh: provisioning " + spec.Hostname)
		if err := m.removeContainers(ctx, spec.NodeID); err != nil {
			return attention(spec, err.Error())
		}
		for _, v := range []string{agentprotocol.MeshStateVolume(spec.NodeID), agentprotocol.MeshConfigVolume(spec.NodeID)} {
			if err := m.eng.RemoveVolume(ctx, v, true); err != nil && !errors.Is(err, docker.ErrNotFound) {
				return attention(spec, "Could not remove the old node state: "+err.Error())
			}
		}
		create = true
	case exists && info.Labels[agentprotocol.LabelMeshSpec] != want.Labels[agentprotocol.LabelMeshSpec]:
		// Reconfigured (image, login server, target, hostname): recreate on the
		// same state volume, the identity survives.
		logf("Mesh: recreating " + spec.Hostname + " (its configuration changed)")
		carryFP = info.Labels[labelKeyFP]
		if err := m.removeContainer(ctx, info); err != nil {
			return attention(spec, err.Error())
		}
		create = true
	}

	id := info.ID
	if create {
		useKey := ""
		if provision {
			useKey = key
		}
		var err error
		if id, err = m.createNode(ctx, spec, useKey, carryFP); err != nil {
			return attention(spec, redactKey(err.Error(), key))
		}
	} else if !info.Running && !info.Restarting {
		if err := m.eng.ContainerStart(ctx, id); err != nil {
			return attention(spec, "Could not start the mesh node: "+err.Error())
		}
	}

	if err := m.syncNetworks(ctx, spec, id); err != nil {
		return attention(spec, err.Error())
	}
	if spec.Kind == agentprotocol.MeshKindServer {
		if err := m.writeServe(ctx, id, spec); err != nil {
			return attention(spec, "Could not update the serve config: "+err.Error())
		}
	}

	if create {
		st = m.waitOnline(ctx, id, spec)
	} else if cur, err := m.eng.ContainerInspect(ctx, id); err != nil {
		st = attention(spec, "Cannot inspect the mesh node: "+err.Error())
	} else {
		st = m.nodeStatus(ctx, cur, &spec)
	}
	if st.State == agentprotocol.MeshStateOnline {
		logf("Mesh: " + spec.Hostname + " is online as " + st.FQDN)
	} else {
		logf("Mesh: " + spec.Hostname + ": " + st.State + ": " + st.Message)
	}
	return st
}

// ensureNodeNetworks creates the networks a node starts on: the mesh bridge for
// a server node, the environment networks for a link node.
func (m *Manager) ensureNodeNetworks(ctx context.Context, spec agentprotocol.MeshNodeSpec) error {
	if spec.Kind == agentprotocol.MeshKindServer {
		if err := m.ensureNetwork(ctx, agentprotocol.MeshNetwork, map[string]string{agentprotocol.LabelManaged: "true"}, false); err != nil {
			return fmt.Errorf("Could not create the mesh network: %w", err)
		}
		return nil
	}
	for _, a := range LinkNetworks(spec) {
		if err := m.ensureEnvNetwork(ctx, a.Network, spec.EnvironmentID); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) ensureEnvNetwork(ctx context.Context, name, envID string) error {
	labels := map[string]string{agentprotocol.LabelManaged: "true", agentprotocol.LabelEnvironment: envID}
	if err := m.ensureNetwork(ctx, name, labels, isInternalNetwork(name)); err != nil {
		return fmt.Errorf("Could not create the network %s: %w", name, err)
	}
	return nil
}

// createNode creates, and starts, a node container; the serve config of a
// server node is in place before the first start.
func (m *Manager) createNode(ctx context.Context, spec agentprotocol.MeshNodeSpec, key, carryFP string) (string, error) {
	if err := m.ensureImage(ctx); err != nil {
		return "", fmt.Errorf("Could not pull the tailscale image: %w", err)
	}
	vols := []string{agentprotocol.MeshStateVolume(spec.NodeID)}
	if spec.Kind == agentprotocol.MeshKindServer {
		vols = append(vols, agentprotocol.MeshConfigVolume(spec.NodeID))
	}
	for _, v := range vols {
		if err := m.eng.EnsureVolume(ctx, v, map[string]string{agentprotocol.LabelManaged: "true", agentprotocol.LabelMesh: spec.NodeID}); err != nil {
			return "", fmt.Errorf("Could not create the mesh node volume: %w", err)
		}
	}
	cs := ContainerSpec(spec, key)
	if key == "" && carryFP != "" {
		cs.Labels[labelKeyFP] = carryFP
	}
	id, err := m.eng.ContainerCreate(ctx, cs)
	if err != nil {
		if spec.Kind == agentprotocol.MeshKindLink && strings.Contains(err.Error(), "/dev/net/tun") {
			return "", errors.New(msgNoTun)
		}
		return "", fmt.Errorf("Could not create the mesh node: %w", err)
	}
	cleanup := func() { _ = m.eng.ContainerRemove(context.WithoutCancel(ctx), id, true, false) }
	if spec.Kind == agentprotocol.MeshKindServer {
		if err := m.writeServe(ctx, id, spec); err != nil {
			cleanup()
			return "", fmt.Errorf("Could not write the serve config: %w", err)
		}
	}
	if err := m.eng.ContainerStart(ctx, id); err != nil {
		cleanup()
		if spec.Kind == agentprotocol.MeshKindLink && strings.Contains(err.Error(), "/dev/net/tun") {
			return "", errors.New(msgNoTun)
		}
		return "", fmt.Errorf("Could not start the mesh node: %w", err)
	}
	return id, nil
}

// wantNet is a network a node should be on.
type wantNet struct {
	env      string
	internal bool
	aliases  []string
}

func wantedNetworks(spec agentprotocol.MeshNodeSpec) map[string]wantNet {
	out := map[string]wantNet{}
	if spec.Kind == agentprotocol.MeshKindLink {
		for _, a := range LinkNetworks(spec) {
			out[a.Network] = wantNet{env: spec.EnvironmentID, internal: isInternalNetwork(a.Network), aliases: a.Aliases}
		}
		return out
	}
	for _, e := range spec.Exposures {
		out[exposureNetwork(e)] = wantNet{env: e.EnvironmentID, internal: e.BlockEgress}
	}
	return out
}

// sameAliases compares the explicit aliases an endpoint reports with the
// desired set. The daemon adds the container's short ID (and may add its name)
// to what it reports; those are not ours.
func sameAliases(observed, desired []string, info docker.ContainerInfo) bool {
	want := map[string]bool{}
	for _, a := range desired {
		want[a] = true
	}
	got := map[string]bool{}
	for _, a := range observed {
		if a == info.Name || (len(a) >= 12 && strings.HasPrefix(info.ID, a)) || a == info.ID {
			continue
		}
		got[a] = true
	}
	if len(got) != len(want) {
		return false
	}
	for a := range want {
		if !got[a] {
			return false
		}
	}
	return true
}

// syncNetworks makes the node's environment networks match the spec without
// recreating it: a server node joins the networks of its exposures, a link
// node the environment's networks under its aliases (re-attached only when the
// alias set changed; established connections keep flowing through the DNAT).
func (m *Manager) syncNetworks(ctx context.Context, spec agentprotocol.MeshNodeSpec, id string) error {
	info, err := m.eng.ContainerInspect(ctx, id)
	if err != nil {
		return fmt.Errorf("Cannot inspect the mesh node: %w", err)
	}
	want := wantedNetworks(spec)
	names := make([]string, 0, len(want))
	for n := range want {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		w := want[n]
		cur, on := info.Networks[n]
		switch {
		case !on:
			if err := m.ensureEnvNetwork(ctx, n, w.env); err != nil {
				return err
			}
			if err := m.eng.NetworkConnect(ctx, n, id, w.aliases); err != nil {
				return fmt.Errorf("Could not connect the mesh node to %s: %w", n, err)
			}
			m.log.Info("mesh node joined a network", "node", spec.NodeID, "network", n)
		case spec.Kind == agentprotocol.MeshKindLink && !sameAliases(cur.Aliases, w.aliases, info):
			if err := m.eng.NetworkDisconnect(ctx, n, id); err != nil {
				return fmt.Errorf("Could not re-attach the mesh node to %s: %w", n, err)
			}
			if err := m.eng.NetworkConnect(ctx, n, id, w.aliases); err != nil {
				return fmt.Errorf("Could not re-attach the mesh node to %s: %w", n, err)
			}
			m.log.Info("mesh link aliases changed", "node", spec.NodeID, "network", n, "aliases", w.aliases)
		}
	}
	for n := range info.Networks {
		if _, keep := want[n]; !keep && isEnvNetwork(n) {
			if err := m.eng.NetworkDisconnect(ctx, n, id); err != nil {
				return fmt.Errorf("Could not disconnect the mesh node from %s: %w", n, err)
			}
			m.log.Info("mesh node left a network", "node", spec.NodeID, "network", n)
		}
	}
	return nil
}

// writeServe renders the serve config of a server node and copies it into the
// watched /config when it changed (no restart).
func (m *Manager) writeServe(ctx context.Context, id string, spec agentprotocol.MeshNodeSpec) error {
	cfg, err := ServeConfig(m.serveTargets(ctx, spec.Exposures))
	if err != nil {
		return err
	}
	h := hashBytes(cfg)
	m.mu.Lock()
	same := m.serve[spec.NodeID] == id+":"+h
	m.mu.Unlock()
	if same {
		return nil
	}
	tarball, err := serveTar(cfg)
	if err != nil {
		return err
	}
	if err := m.eng.CopyToContainer(ctx, id, configDir, tarball); err != nil {
		return err
	}
	m.mu.Lock()
	m.serve[spec.NodeID] = id + ":" + h
	m.mu.Unlock()
	return nil
}

// Refresh re-evaluates the serve targets of every desired server node (a
// deployment may have added or dropped the nc-svc alias of an exposed
// service) and rewrites the serve config of the ones that changed.
func (m *Manager) Refresh(ctx context.Context) {
	for _, spec := range m.desiredSpecs() {
		if spec.Kind != agentprotocol.MeshKindServer || len(spec.Exposures) == 0 {
			continue
		}
		func() {
			unlock := m.lockNode(spec.NodeID)
			defer unlock()
			info, err := m.eng.ContainerInspect(ctx, ContainerName(spec))
			if err != nil || !info.Running {
				return
			}
			if err := m.writeServe(ctx, info.ID, spec); err != nil {
				m.log.Warn("mesh: refresh serve targets", "node", spec.NodeID, "err", err)
			}
		}()
	}
}

// waitOnline polls a new node until it is online, the container dies or the
// wait ends, and returns its status.
func (m *Manager) waitOnline(ctx context.Context, id string, spec agentprotocol.MeshNodeSpec) agentprotocol.MeshNodeStatus {
	deadline := m.now().Add(m.WaitRunning)
	server := spec.Kind == agentprotocol.MeshKindServer
	var last agentprotocol.MeshNodeStatus
	for {
		info, err := m.eng.ContainerInspect(ctx, id)
		switch {
		case err != nil:
			return attention(spec, "Cannot inspect the mesh node: "+err.Error())
		case !info.Running && !info.Restarting:
			return m.mapExited(ctx, id, spec.NodeID, spec.Kind, info)
		case info.Running:
			if last = m.readStatus(ctx, id, server, spec.Tags); last.State == agentprotocol.MeshStateOnline {
				m.forgetStatus(spec.NodeID)
				return m.nodeStatus(ctx, info, &spec)
			}
		}
		if ctx.Err() != nil {
			return attention(spec, "The agent stopped while waiting for the mesh node.")
		}
		if !m.now().Before(deadline) {
			break
		}
		select {
		case <-ctx.Done():
			return attention(spec, "The agent stopped while waiting for the mesh node.")
		case <-time.After(m.Poll):
		}
	}
	switch last.State {
	case agentprotocol.MeshStateNeedsAttn:
		if tail := m.logTail(ctx, id); tail != "" {
			last.Message += " Last log lines: " + tail
		}
		return last
	case "":
		return attention(spec, "The mesh node did not answer in time.")
	}
	last.State = agentprotocol.MeshStateProvisioning
	last.Message = "The node is still coming up."
	return last
}

// nodeMissing reports whether the node's container does not exist (an
// inspect error other than not-found counts as present: the caller inspects
// again and reports it).
func (m *Manager) nodeMissing(ctx context.Context, spec agentprotocol.MeshNodeSpec) bool {
	_, err := m.eng.ContainerInspect(ctx, ContainerName(spec))
	return errors.Is(err, docker.ErrNotFound)
}
