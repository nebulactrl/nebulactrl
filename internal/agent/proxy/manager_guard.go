package proxy

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/internal/agent/guard"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// The edge guard sidecar (internal/agent/guard): the agent's own binary, run as
// `nebulactrl-agent guard` in a container that shares the proxy container's
// network namespace (like the tailnet node), so Traefik's forwardAuth reaches
// it on 127.0.0.1 and nothing outside the proxy can. The container uses the
// pages image (already on the host, a shell-less entrypoint passthrough); the
// binary, the rules and the GeoIP database are copied into it.
const (
	// LabelGuardSpec fingerprints the guard container (image, binary);
	// LabelGuardNetns records the proxy container whose namespace it shares.
	LabelGuardSpec  = "nebulactrl.guard.spec"
	LabelGuardNetns = "nebulactrl.guard.netns"

	guardMemoryBytes  = 192 << 20
	guardReadyTimeout = 10 * time.Second
)

// guardState is what the manager last put into the guard container (applyMu).
type guardState struct {
	// container is the guard container's ID; rules the hash of its rules
	// file and bans the hash of its bans file; geo the GeoIP file it holds
	// ("" none) and geoStamp the source file's size and modification time.
	container string
	rules     string
	bans      string
	geo       string
	geoStamp  string
	// checked is set once the manager knows whether a guard container exists.
	checked bool
}

// GuardRoute is what the edge guard evaluates for one route.
type GuardRoute struct {
	// Firewall holds the active rules the guard evaluates (none for a route
	// that is guarded for its auto-ban only).
	Firewall *agentprotocol.Firewall
	// AutoBan is the route's enabled auto-ban policy; nil without one.
	AutoBan *agentprotocol.AutoBan
}

// autoBanOn reports whether r has an enabled auto-ban policy. Such a route
// always goes through the guard, which decides allow list, bans and firewall
// rules in that order, so none of its firewall is compiled into routers.
func autoBanOn(r agentprotocol.Route) bool {
	return !r.IsL4() && r.AutoBan != nil && r.AutoBan.Enabled
}

// needsGuard reports whether the edge guard serves r: it has an enabled
// auto-ban policy, or a firewall the routers cannot express (see
// agentprotocol.Firewall.NeedsGuard).
func needsGuard(r agentprotocol.Route) bool {
	if r.IsL4() {
		return false
	}
	return autoBanOn(r) || (len(r.Firewall.Active()) > 0 && r.Firewall.NeedsGuard(headerClientIP(r)))
}

// GuardRoutes returns what the edge guard evaluates, by route id: the routes
// that need it for either reason (needsGuard).
func GuardRoutes(routes []agentprotocol.Route) map[string]GuardRoute {
	out := map[string]GuardRoute{}
	for _, r := range routes {
		if !needsGuard(r) {
			continue
		}
		gr := GuardRoute{Firewall: &agentprotocol.Firewall{Rules: r.Firewall.Active()}}
		if autoBanOn(r) {
			ab := *r.AutoBan
			gr.AutoBan = &ab
		}
		out[r.ServiceID] = gr
	}
	return out
}

// guardFirewalls is the rules document's content: the firewall of every route.
func guardFirewalls(routes map[string]GuardRoute) map[string]*agentprotocol.Firewall {
	out := make(map[string]*agentprotocol.Firewall, len(routes))
	for id, gr := range routes {
		out[id] = gr.Firewall
	}
	return out
}

// guardSpec is the guard container attached to the proxy container netnsID.
func (m *Manager) guardSpec(netnsID string) docker.ContainerSpec {
	return docker.ContainerSpec{
		Name:  agentprotocol.GuardContainer,
		Image: PagesImage,
		Cmd:   []string{guard.Binary, "guard", "--dir", guard.Dir, "--listen", "127.0.0.1:" + strconv.Itoa(agentprotocol.GuardPort)},
		Labels: map[string]string{
			agentprotocol.LabelManaged: "true",
			LabelGuardSpec:             specHash(PagesImage, m.cfg.GuardBinaryID, fmt.Sprint(guardMemoryBytes)),
			LabelGuardNetns:            netnsID,
		},
		NetworkMode:   "container:" + agentprotocol.ProxyContainer,
		RestartPolicy: "unless-stopped",
		MemoryBytes:   guardMemoryBytes,
		CapDrop:       []string{"ALL"},
		// The guard binds a port below 1024 (agentprotocol.GuardPort).
		CapAdd:    []string{"NET_BIND_SERVICE"},
		LogConfig: &docker.LogConfig{Type: "json-file", Config: map[string]string{"max-size": "1m", "max-file": "2"}},
	}
}

// prepareGuard runs before a route table is pushed: the guard must know every
// firewall the new routers send to it, and keep those of the live table until
// the push is done. Without guarded routes it does nothing (finishGuard
// removes a guard nothing needs).
func (m *Manager) prepareGuard(ctx context.Context, routes []agentprotocol.Route) error {
	want := GuardRoutes(routes)
	if len(want) == 0 {
		return nil
	}
	if m.cfg.GuardBinary == "" {
		return errors.New("a firewall that matches countries (or visitor addresses behind Cloudflare), or auto-ban, needs the edge guard, which this agent cannot run: it is not a Linux binary")
	}
	union := GuardRoutes(m.Current())
	for id, gr := range want {
		union[id] = gr
	}
	return m.ensureGuard(ctx, union)
}

// finishGuard runs after a route table went live: the guard keeps exactly the
// table's firewalls, or is removed when no route needs it. Failures are
// logged: the table is live, and the guard still holds a superset.
func (m *Manager) finishGuard(ctx context.Context, routes []agentprotocol.Route) {
	want := GuardRoutes(routes)
	if len(want) == 0 {
		if m.guard.container != "" || !m.guard.checked {
			// Once after a start (a guard left by an earlier agent), then only
			// when this manager ran one.
			if err := m.cfg.Engine.ContainerRemove(ctx, agentprotocol.GuardContainer, true, false); err != nil && !errors.Is(err, docker.ErrNotFound) {
				m.log.Warn("remove edge guard", "err", err)
				return
			}
		}
		m.guard = guardState{checked: true}
		return
	}
	if err := m.ensureGuard(ctx, want); err != nil {
		m.log.Warn("edge guard", "err", err)
	}
}

// checkGuard is the health check's upkeep of the guard: put back a guard that
// stopped or lost the proxy's network namespace, and hand it a GeoIP database
// that was downloaded since the last route push.
func (m *Manager) checkGuard(ctx context.Context) {
	if want := GuardRoutes(m.Current()); len(want) > 0 && m.cfg.GuardBinary != "" {
		if err := m.ensureGuard(ctx, want); err != nil {
			m.log.Warn("edge guard", "err", err)
		}
	}
}

// BanSource is the auto-ban engine as the manager sees it
// (internal/agent/autoban).
type BanSource interface {
	// SetRoutes hands the engine the route table that went live.
	SetRoutes(routes []agentprotocol.Route)
	// Bans returns the active bans by route id; only the bans are read (the
	// allow lists come from the routes).
	Bans() agentprotocol.GuardBans
	// OnChange registers the function the engine calls, debounced, when
	// Bans() may have changed.
	OnChange(fn func())
}

// pushBansTimeout bounds the guard update a ban change triggers.
const pushBansTimeout = 30 * time.Second

// SetBanSource attaches the auto-ban engine: its bans are written to the
// guard next to the rules, a change of them is pushed (PushBans), and every
// route table that goes live is handed to it. Call it before the first Apply.
func (m *Manager) SetBanSource(src BanSource) {
	m.mu.Lock()
	m.banSource = src
	m.mu.Unlock()
	src.OnChange(func() {
		ctx, cancel := context.WithTimeout(context.Background(), pushBansTimeout)
		defer cancel()
		if err := m.PushBans(ctx); err != nil {
			m.log.Warn("edge guard: push bans", "err", err)
		}
	})
}

// guardBans is the bans document for routes: the allow list of each auto-ban
// route and the engine's active bans of it.
func (m *Manager) guardBans(routes map[string]GuardRoute) agentprotocol.GuardBans {
	m.mu.RLock()
	src := m.banSource
	m.mu.RUnlock()
	var have agentprotocol.GuardBans
	if src != nil {
		have = src.Bans()
	}
	out := agentprotocol.GuardBans{Services: map[string]agentprotocol.GuardRouteBans{}}
	for id, gr := range routes {
		if gr.AutoBan != nil {
			out.Services[id] = agentprotocol.GuardRouteBans{Allow: gr.AutoBan.Allow, Bans: have.Services[id].Bans}
		}
	}
	return out
}

// PushBans writes the current bans to the running guard, and only them: the
// rules, the GeoIP database and the container stay as they are. The auto-ban
// engine calls it when a ban started or ended. It does nothing while no guard
// runs (ensureGuard writes the bans with the next route table).
func (m *Manager) PushBans(ctx context.Context) error {
	m.applyMu.Lock()
	defer m.applyMu.Unlock()
	if m.guard.container == "" {
		return nil
	}
	routes := GuardRoutes(m.Current())
	if len(routes) == 0 {
		return nil
	}
	raw, err := guard.EncodeBans(m.guardBans(routes))
	if err != nil {
		return err
	}
	hash := guard.Hash(raw)
	if hash == m.guard.bans {
		return nil
	}
	if err := m.cfg.Engine.CopyToContainer(ctx, m.guard.container, guard.Dir, tarOf(guard.BansFile, 0o644, raw)); err != nil {
		return fmt.Errorf("write edge guard bans: %w", err)
	}
	if err := m.waitGuard(ctx, m.guard.rules, hash); err != nil {
		return err
	}
	m.guard.bans = hash
	return nil
}

// ensureGuard makes the guard container run, attached to the current proxy
// container, holding the routes' firewalls and bans and the server's GeoIP
// database; after a change it waits until the guard serves them.
func (m *Manager) ensureGuard(ctx context.Context, routes map[string]GuardRoute) error {
	eng := m.cfg.Engine
	px, err := eng.ContainerInspect(ctx, agentprotocol.ProxyContainer)
	if err != nil {
		return fmt.Errorf("edge guard needs the proxy container: %w", err)
	}
	want := m.guardSpec(px.ID)
	info, err := eng.ContainerInspect(ctx, agentprotocol.GuardContainer)
	exists := err == nil
	if err != nil && !errors.Is(err, docker.ErrNotFound) {
		return fmt.Errorf("inspect edge guard: %w", err)
	}
	// A proxy container that restarted after the guard started has a new
	// network namespace; the guard still sits in the old one.
	fresh := !exists || info.Labels[LabelGuardSpec] != want.Labels[LabelGuardSpec] || info.Labels[LabelGuardNetns] != px.ID ||
		(info.Running && px.StartedAt.After(info.StartedAt))
	if !fresh && info.ID != m.guard.container {
		// A guard this manager did not start (the agent restarted): keep it
		// running and write its files again.
		m.guard = guardState{container: info.ID}
	}
	if fresh {
		// Missing, from another binary, or outside the proxy's namespace: recreate.
		if exists {
			if err := eng.ContainerRemove(ctx, info.ID, true, false); err != nil && !errors.Is(err, docker.ErrNotFound) {
				return fmt.Errorf("remove edge guard: %w", err)
			}
		}
		m.guard = guardState{}
		if err := m.ensureImageRef(ctx, PagesImage); err != nil {
			return fmt.Errorf("pull edge guard image: %w", err)
		}
		id, err := eng.ContainerCreate(ctx, want)
		if err != nil {
			return fmt.Errorf("create edge guard: %w", err)
		}
		if err := m.copyGuardBinary(ctx, id); err != nil {
			_ = eng.ContainerRemove(context.WithoutCancel(ctx), id, true, false)
			return err
		}
		m.guard.container = id
	}
	m.guard.checked = true

	// The database first: the rules name it.
	geo, err := m.syncGuardGeo(ctx, m.guard.container)
	if err != nil {
		m.log.Warn("edge guard: copy geoip database; country rules wait for it", "err", err)
	}
	raw, err := guard.Encode(guard.Rules{Routes: guardFirewalls(routes), Geo: geo})
	if err != nil {
		return err
	}
	hash := guard.Hash(raw)
	bansRaw, err := guard.EncodeBans(m.guardBans(routes))
	if err != nil {
		return err
	}
	bansHash := guard.Hash(bansRaw)
	changed := fresh || !info.Running || hash != m.guard.rules || bansHash != m.guard.bans
	if fresh || hash != m.guard.rules {
		if err := eng.CopyToContainer(ctx, m.guard.container, guard.Dir, tarOf(guard.RulesFile, 0o644, raw)); err != nil {
			return fmt.Errorf("write edge guard rules: %w", err)
		}
	}
	if fresh || bansHash != m.guard.bans {
		if err := eng.CopyToContainer(ctx, m.guard.container, guard.Dir, tarOf(guard.BansFile, 0o644, bansRaw)); err != nil {
			return fmt.Errorf("write edge guard bans: %w", err)
		}
	}
	if fresh || !info.Running {
		if err := eng.ContainerStart(ctx, m.guard.container); err != nil {
			return fmt.Errorf("start edge guard: %w", err)
		}
	}
	if changed {
		if err := m.waitGuard(ctx, hash, bansHash); err != nil {
			return err
		}
	}
	m.guard.rules, m.guard.bans = hash, bansHash
	return nil
}

// syncGuardGeo copies the server's GeoIP database into the guard when it
// changed and returns its name there ("" without a database).
func (m *Manager) syncGuardGeo(ctx context.Context, id string) (string, error) {
	src := ""
	if m.cfg.GeoDatabase != nil {
		src = m.cfg.GeoDatabase()
	}
	if src == "" {
		return "", nil
	}
	fi, err := os.Stat(src)
	if err != nil {
		return "", err
	}
	stamp := fmt.Sprintf("%s/%d/%d", path.Base(src), fi.Size(), fi.ModTime().UnixNano())
	if stamp == m.guard.geoStamp && m.guard.geo != "" {
		return m.guard.geo, nil
	}
	sum := sha256.Sum256([]byte(stamp))
	name := "geo-" + hex.EncodeToString(sum[:6]) + ".mmdb"
	f, err := os.Open(src)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if err := m.cfg.Engine.CopyToContainer(ctx, id, guard.Dir, streamTar(name, 0o644, fi.Size(), f)); err != nil {
		return "", fmt.Errorf("copy geoip database: %w", err)
	}
	m.guard.geo, m.guard.geoStamp = name, stamp
	return name, nil
}

// copyGuardBinary places the agent's own binary in a created guard container.
// The container's image does not have the guard directory, and Docker copies
// only into a directory that exists: it is created first (the rules and the
// GeoIP database go into it after the binary).
func (m *Manager) copyGuardBinary(ctx context.Context, id string) error {
	f, err := os.Open(m.cfg.GuardBinary)
	if err != nil {
		return fmt.Errorf("open agent binary for the edge guard: %w", err)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	if err := m.cfg.Engine.CopyToContainer(ctx, id, "/", dirTar(strings.TrimPrefix(guard.Dir, "/"), 0o755)); err != nil {
		return fmt.Errorf("create the edge guard directory: %w", err)
	}
	if err := m.cfg.Engine.CopyToContainer(ctx, id, guard.Dir, streamTar(path.Base(guard.Binary), 0o755, fi.Size(), f)); err != nil {
		return fmt.Errorf("copy agent binary into the edge guard: %w", err)
	}
	return nil
}

// waitGuard polls the guard until it serves the rules with hash and the bans
// with bansHash.
func (m *Manager) waitGuard(ctx context.Context, hash, bansHash string) error {
	deadline := time.Now().Add(guardReadyTimeout)
	argv := []string{"wget", "-q", "-O", "/dev/null", "http://127.0.0.1:" + strconv.Itoa(agentprotocol.GuardPort) + "/v1/ready?rules=" + hash + "&bans=" + bansHash}
	for {
		code, err := m.cfg.Engine.ContainerExec(ctx, m.guard.container, argv, docker.ExecSpec{}, nil, nil)
		if err == nil && code == 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("edge guard did not load its rules and bans within %s (exit %d, %v)", guardReadyTimeout, code, err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// tarOf is a tar archive holding one file.
func tarOf(name string, mode int64, content []byte) io.Reader {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	_ = tw.WriteHeader(&tar.Header{Name: name, Mode: mode, Size: int64(len(content)), ModTime: time.Now()})
	_, _ = tw.Write(content)
	_ = tw.Close()
	return &buf
}

// dirTar is a tar archive holding one empty directory.
func dirTar(dir string, mode int64) io.Reader {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	_ = tw.WriteHeader(&tar.Header{Name: dir + "/", Typeflag: tar.TypeDir, Mode: mode, ModTime: time.Now()})
	_ = tw.Close()
	return &buf
}

// streamTar is a tar archive holding one file read from r, built while it is
// read (the agent binary and a city database are tens of megabytes).
func streamTar(name string, mode, size int64, r io.Reader) io.Reader {
	pr, pw := io.Pipe()
	go func() {
		tw := tar.NewWriter(pw)
		err := tw.WriteHeader(&tar.Header{Name: name, Mode: mode, Size: size, ModTime: time.Now()})
		if err == nil {
			_, err = io.CopyN(tw, r, size)
		}
		if err == nil {
			err = tw.Close()
		}
		pw.CloseWithError(err)
	}()
	return pr
}
