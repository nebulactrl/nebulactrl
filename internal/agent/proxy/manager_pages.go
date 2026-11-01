package proxy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/internal/agent/pages"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// PagesImage is the pinned pages-server image: the official nginx stable
// branch on Alpine, slim variant (nginx 1.30.6, multi-arch index digest, about
// 5 MB per platform). It only serves static files the agent writes into it.
const PagesImage = "nginx:1.30.6-alpine-slim@sha256:dede8895648b461178c3bc604f7eed4423efe5220b2c4d839f5eb3d34ac3f8f7"

// LabelPagesSpec carries the fingerprint of the pages container's image,
// settings and rendered content.
const LabelPagesSpec = "nebulactrl.pages.spec"

const (
	pagesReadyTimeout = 20 * time.Second
	pagesMemoryBytes  = 64 << 20
)

// pagesState is the Manager's view of the pages server. All fields are guarded
// by applyMu.
type pagesState struct {
	// entries is the content of the running container (nil = unknown).
	entries []pages.Entry
	known   bool
}

func (m *Manager) pagesUp() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.pagesOK
}

func (m *Manager) setPagesUp(up bool) {
	m.mu.Lock()
	m.pagesOK = up
	m.mu.Unlock()
}

// pagesSpec is the container that serves b.
func (m *Manager) pagesSpec(b *pages.Bundle) docker.ContainerSpec {
	caps := []string{"CHOWN", "SETGID", "SETUID"}
	return docker.ContainerSpec{
		Name:  agentprotocol.PagesContainer,
		Image: PagesImage,
		Env:   []string{"NGINX_ENTRYPOINT_QUIET_LOGS=1"},
		Labels: map[string]string{
			agentprotocol.LabelManaged: "true",
			LabelPagesSpec:             specHash(PagesImage, b.Hash, fmt.Sprint(caps), fmt.Sprint(pagesMemoryBytes)),
		},
		Networks:      []docker.NetworkAttachment{{Network: agentprotocol.EdgeNetwork, Aliases: []string{agentprotocol.PagesContainer}, GwPriority: agentprotocol.EdgeGwPriority}},
		RestartPolicy: "unless-stopped",
		MemoryBytes:   pagesMemoryBytes,
		CapDrop:       []string{"ALL"},
		CapAdd:        caps,
		LogConfig:     &docker.LogConfig{Type: "json-file", Config: map[string]string{"max-size": "1m", "max-file": "2"}},
	}
}

func pagesMatches(info docker.ContainerInfo, want docker.ContainerSpec) bool {
	if info.Image != want.Image || info.RestartMode != want.RestartPolicy {
		return false
	}
	if info.Labels[LabelPagesSpec] == "" || info.Labels[LabelPagesSpec] != want.Labels[LabelPagesSpec] {
		return false
	}
	_, onEdge := info.Networks[agentprotocol.EdgeNetwork]
	return onEdge
}

// pagesCtr is one container of the pages server.
type pagesCtr struct {
	name string
	info docker.ContainerInfo
}

// pagesNames are the two names the pages server's container alternates
// between: a replacement takes the name the running one does not hold. Both
// carry the network alias agentprotocol.PagesContainer, which is what Traefik
// resolves.
var pagesNames = [...]string{agentprotocol.PagesContainer, agentprotocol.PagesContainerAlt}

func otherPagesName(name string) string {
	if name == pagesNames[0] {
		return pagesNames[1]
	}
	return pagesNames[0]
}

// inspectPages lists the pages containers that exist: one, or two while a
// replacement is in progress (or was interrupted).
func (m *Manager) inspectPages(ctx context.Context) ([]pagesCtr, error) {
	var out []pagesCtr
	for _, name := range pagesNames {
		info, err := m.cfg.Engine.ContainerInspect(ctx, name)
		switch {
		case err == nil:
			out = append(out, pagesCtr{name: name, info: info})
		case !errors.Is(err, docker.ErrNotFound):
			return nil, err
		}
	}
	return out, nil
}

// pagesServing reports whether a pages container runs.
func (m *Manager) pagesServing(ctx context.Context) (bool, error) {
	found, err := m.inspectPages(ctx)
	if err != nil {
		return false, err
	}
	for _, c := range found {
		if c.info.Running {
			return true, nil
		}
	}
	return false, nil
}

// bestPages picks the container to keep of those that exist: the one that runs
// and holds what is wanted, then one that runs, then one that holds it.
func bestPages(found []pagesCtr, want docker.ContainerSpec) pagesCtr {
	best, bestScore := found[0], -1
	for _, c := range found {
		score := 0
		if c.info.Running {
			score += 2
		}
		if pagesMatches(c.info, want) {
			score++
		}
		if score > bestScore {
			best, bestScore = c, score
		}
	}
	return best
}

// ensurePages makes the pages container exist, run and hold exactly b. A
// container created from other content is replaced: the content is placed
// before the first start, like the Traefik bootstrap file.
//
// The replacement never opens a window in which nothing answers to
// agentprotocol.PagesContainer (every error and maintenance page of every
// service is served from there): the new container starts next to the old one
// under the other of pagesNames with the same network alias, must be ready, and
// only then is the old one removed. Until then both answer, and the new one
// holds every page the old one does (preparePages renders the union). A
// replacement that fails leaves the old container as it was.
func (m *Manager) ensurePages(ctx context.Context, b *pages.Bundle) error {
	eng := m.cfg.Engine
	want := m.pagesSpec(b)
	found, err := m.inspectPages(ctx)
	if err != nil {
		return err
	}
	// Two containers exist only after an interrupted replacement: keep the
	// best, the other would answer with whatever content it was built from.
	var cur *pagesCtr
	if len(found) > 0 {
		best := bestPages(found, want)
		cur = &best
		for _, c := range found {
			if c.info.ID == best.info.ID {
				continue
			}
			if err := eng.ContainerRemove(ctx, c.info.ID, true, false); err != nil && !errors.Is(err, docker.ErrNotFound) {
				return err
			}
		}
	}

	if cur != nil && pagesMatches(cur.info, want) {
		if cur.info.Running {
			return nil
		}
		m.log.Info("starting existing pages container", "state", cur.info.State)
		if err := eng.ContainerStart(ctx, cur.info.ID); err != nil {
			return fmt.Errorf("pages server failed to start: %w", err)
		}
		return m.waitPagesReady(ctx, cur.info.ID)
	}
	want.Name = pagesNames[0]
	if cur != nil {
		want.Name = otherPagesName(cur.name)
		m.log.Info("replacing pages container: content or configuration changed")
	}

	labels := map[string]string{agentprotocol.LabelManaged: "true"}
	if err := eng.EnsureNetwork(ctx, agentprotocol.EdgeNetwork, labels); err != nil {
		return err
	}
	if err := m.ensureImageRef(ctx, PagesImage); err != nil {
		return fmt.Errorf("pull pages image: %w", err)
	}
	id, err := eng.ContainerCreate(ctx, want)
	if err != nil {
		return fmt.Errorf("create pages container: %w", err)
	}
	discard := func() { _ = eng.ContainerRemove(context.WithoutCancel(ctx), id, true, false) }
	archive, err := b.Archive()
	if err != nil {
		discard()
		return err
	}
	if err := eng.CopyToContainer(ctx, id, "/", archive); err != nil {
		discard()
		return fmt.Errorf("write pages content: %w", err)
	}
	if err := eng.ContainerStart(ctx, id); err != nil {
		discard()
		return fmt.Errorf("pages server failed to start: %w", err)
	}
	if err := m.waitPagesReady(ctx, id); err != nil {
		discard()
		return err
	}
	if cur == nil {
		return nil
	}
	if err := eng.ContainerRemove(ctx, cur.info.ID, true, false); err != nil && !errors.Is(err, docker.ErrNotFound) {
		discard() // two servers with different content must not answer together
		return fmt.Errorf("remove replaced pages container: %w", err)
	}
	return nil
}

// waitPagesReady polls the server's own /healthz from inside the container
// (the agent may not be able to reach the edge network).
func (m *Manager) waitPagesReady(ctx context.Context, id string) error {
	deadline := time.Now().Add(pagesReadyTimeout)
	var last error
	for time.Now().Before(deadline) {
		var out bytes.Buffer
		code, err := m.cfg.Engine.ContainerExec(ctx, id,
			[]string{"wget", "-q", "-O", "/dev/null", fmt.Sprintf("http://127.0.0.1:%d/healthz", agentprotocol.PagesPort)}, docker.ExecSpec{}, &out, &out)
		switch {
		case err == nil && code == 0:
			return nil
		case err != nil:
			last = err
		default:
			last = fmt.Errorf("health check exited %d: %s", code, bytes.TrimSpace(out.Bytes()))
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
	return fmt.Errorf("pages server did not become ready: %w", last)
}

// preparePages runs before a route push. It renders the pages of the routes
// being applied together with those already served (so a page never vanishes
// while a router still points at it) and brings the container up to date.
// Without maintenance routes a failure only disables the error pages; a route
// table that needs the pages server fails with it.
func (m *Manager) preparePages(ctx context.Context, routes []agentprotocol.Route) error {
	if !m.cfg.Pages {
		return nil
	}
	next := pages.Entries(routes)
	content := pages.Union(m.pages.entries, next)
	b, err := pages.Build(content, time.Now())
	if err == nil {
		err = m.ensurePages(ctx, b)
	}
	if err != nil {
		// A failed replacement leaves the old server running with its old
		// content. Routes that were live keep their pages: the previous table
		// is pushed again when this one is refused, and it must still find
		// them, or its maintenance windows would end with it.
		if serving, _ := m.pagesServing(ctx); serving && m.pages.known && m.pagesUp() {
			m.log.Warn("pages server not updated: the running one keeps serving", "err", err)
			if len(next) > 0 {
				return fmt.Errorf("maintenance pages unavailable: %w", err)
			}
			return nil
		}
		m.setPagesUp(false)
		m.pages = pagesState{}
		if len(next) > 0 {
			return fmt.Errorf("maintenance pages unavailable: %w", err)
		}
		m.log.Warn("pages server unavailable: Traefik's own error pages are used", "err", err)
		return nil
	}
	m.setPagesUp(true)
	m.pages = pagesState{entries: content, known: true}
	return nil
}

// finishPages runs after a successful route push: it drops the pages of
// services that left maintenance.
func (m *Manager) finishPages(ctx context.Context, routes []agentprotocol.Route) {
	if !m.cfg.Pages || !m.pages.known {
		return
	}
	next := pages.Entries(routes)
	if slices.EqualFunc(m.pages.entries, next, entryEqual) {
		return
	}
	b, err := pages.Build(next, time.Now())
	if err == nil {
		err = m.ensurePages(ctx, b)
	}
	if err != nil {
		// The old pages stay; they are unreachable and harmless.
		m.log.Warn("could not drop pages of services that left maintenance", "err", err)
		return
	}
	m.pages = pagesState{entries: next, known: true}
}

func entryEqual(a, b pages.Entry) bool {
	return a.ServiceID == b.ServiceID && a.Host == b.Host && a.Maintenance.Title == b.Maintenance.Title &&
		a.Maintenance.Message == b.Maintenance.Message && etaEqual(a.Maintenance.ETA, b.Maintenance.ETA)
}

func etaEqual(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Equal(*b)
}
