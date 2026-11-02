package proxy

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

func faultyPagesManager(t *testing.T) (*Manager, *faultEngine, *fakeTraefik) {
	t.Helper()
	return faultyManager(t, func(c *Config) { c.Pages = true })
}

func maintenanceTable() []agentprotocol.Route {
	eta := time.Now().Add(time.Hour)
	return withMaintenance(twoRoutes(), &agentprotocol.Maintenance{Title: "Upgrade", ETA: &eta})
}

func TestStoppedPagesContainerIsStartedNotRecreated(t *testing.T) {
	m, fe, _ := faultyPagesManager(t)
	pages := agentprotocol.PagesContainer
	require.NoError(t, m.Apply(twoRoutes()))
	require.Equal(t, 1, fe.count("create:"+pages))

	fe.fleetEngine.mu.Lock()
	fe.ctrs[pages].info.Running, fe.ctrs[pages].info.State = false, "exited"
	fe.fleetEngine.mu.Unlock()

	require.NoError(t, m.Apply(twoRoutes()))
	require.Equal(t, 1, fe.count("create:"+pages), "the container holds the right content: it is only started")
	require.Equal(t, 2, fe.count("start:"+pages))
	info, _, ok := fe.container(pages)
	require.True(t, ok)
	require.True(t, info.Running)
}

func TestPagesInterruptedReplacementKeepsTheBestContainer(t *testing.T) {
	m, fe, _ := faultyPagesManager(t)
	pages, alt := agentprotocol.PagesContainer, agentprotocol.PagesContainerAlt
	require.NoError(t, m.Apply(twoRoutes()))

	// A replacement was interrupted: a second, stopped container with other
	// content exists next to the running one.
	fe.fleetEngine.mu.Lock()
	orig := fe.ctrs[pages]
	stale := &fleetCtr{spec: orig.spec, info: orig.info}
	stale.spec.Name, stale.info.Name, stale.info.ID = alt, alt, "stale-id"
	stale.info.Running, stale.info.State = false, "created"
	stale.info.Labels = map[string]string{agentprotocol.LabelManaged: "true", LabelPagesSpec: "other-content"}
	fe.ctrs[alt] = stale
	fe.fleetEngine.mu.Unlock()

	require.NoError(t, m.Apply(twoRoutes()))
	_, _, staleLeft := fe.container(alt)
	require.False(t, staleLeft, "two servers with different content must not both answer")
	info, _, ok := fe.container(pages)
	require.True(t, ok)
	require.True(t, info.Running, "the running container that holds the content is kept")
	require.Equal(t, 1, fe.count("create:"+pages), "nothing is rebuilt")
}

func TestPagesInterruptedReplacementRemovalFailure(t *testing.T) {
	m, fe, _ := faultyPagesManager(t)
	pages, alt := agentprotocol.PagesContainer, agentprotocol.PagesContainerAlt
	require.NoError(t, m.Apply(twoRoutes()))
	fe.fleetEngine.mu.Lock()
	orig := fe.ctrs[pages]
	stale := &fleetCtr{spec: orig.spec, info: orig.info}
	stale.spec.Name, stale.info.Name, stale.info.ID = alt, alt, "stale-id"
	stale.info.Running = false
	fe.ctrs[alt] = stale
	fe.fleetEngine.mu.Unlock()

	// Applying a table with no maintenance degrades: the failure to clean up is not fatal.
	fe.set("remove", errors.New("container is paused"))
	require.NoError(t, m.Apply(twoRoutes()))
	// With maintenance the pages server is needed, so the failure surfaces.
	err := m.Apply(maintenanceTable())
	require.ErrorContains(t, err, "maintenance pages unavailable")
	require.ErrorContains(t, err, "container is paused")
	require.Equal(t, twoRoutes(), m.Current(), "the live table stays")
}

func TestPagesInspectFailureFailsMaintenanceApply(t *testing.T) {
	m, fe, _ := faultyPagesManager(t)
	require.NoError(t, m.Apply(twoRoutes()))
	fe.set("inspect:"+agentprotocol.PagesContainer, errors.New("daemon not responding"))

	err := m.Apply(maintenanceTable())
	require.ErrorContains(t, err, "maintenance pages unavailable: daemon not responding")
	require.Equal(t, twoRoutes(), m.Current())
}

func TestPagesReplacementContentCopyFailureLeavesTheOldServerRunning(t *testing.T) {
	m, fe, _ := faultyPagesManager(t)
	pages, alt := agentprotocol.PagesContainer, agentprotocol.PagesContainerAlt
	require.NoError(t, m.Apply(twoRoutes()))

	fe.set("copy", errors.New("no space left"))
	err := m.Apply(maintenanceTable())
	require.ErrorContains(t, err, "maintenance pages unavailable")
	require.ErrorContains(t, err, "write pages content: no space left")

	info, _, ok := fe.container(pages)
	require.True(t, ok)
	require.True(t, info.Running, "the old pages server keeps serving")
	_, _, altLeft := fe.container(alt)
	require.False(t, altLeft, "the half-built replacement is removed")
	require.Equal(t, twoRoutes(), m.Current())
}

func TestPagesReplacementStartFailureRemovesTheNewContainer(t *testing.T) {
	m, fe, _ := faultyPagesManager(t)
	pages, alt := agentprotocol.PagesContainer, agentprotocol.PagesContainerAlt
	require.NoError(t, m.Apply(twoRoutes()))

	fe.startErr = func(spec docker.ContainerSpec) error {
		if spec.Name == alt {
			return errors.New("cannot start")
		}
		return nil
	}
	err := m.Apply(maintenanceTable())
	require.ErrorContains(t, err, "pages server failed to start: cannot start")
	_, _, altLeft := fe.container(alt)
	require.False(t, altLeft)
	info, _, ok := fe.container(pages)
	require.True(t, ok)
	require.True(t, info.Running)
}

func TestPagesStoppedContainerStartFailure(t *testing.T) {
	m, fe, _ := faultyPagesManager(t)
	pages := agentprotocol.PagesContainer
	require.NoError(t, m.Apply(twoRoutes()))
	fe.fleetEngine.mu.Lock()
	fe.ctrs[pages].info.Running = false
	fe.fleetEngine.mu.Unlock()
	fe.startErr = func(spec docker.ContainerSpec) error {
		if spec.Name == pages || spec.Name == agentprotocol.PagesContainerAlt {
			return errors.New("cannot start")
		}
		return nil
	}
	// No maintenance: pages are optional, the table still applies without them.
	require.NoError(t, m.Apply(twoRoutes()))
	require.False(t, m.pagesUp(), "error pages fall back to Traefik's own")
	// A table that needs them is refused.
	err := m.Apply(maintenanceTable())
	require.ErrorContains(t, err, "pages server failed to start: cannot start")
}

// failFirstRemove fails the first ContainerRemove only.
type failFirstRemove struct {
	*faultEngine
	done bool
}

func (f *failFirstRemove) ContainerRemove(ctx context.Context, id string, force, vols bool) error {
	if !f.done {
		f.done = true
		return errors.New("container is paused")
	}
	return f.faultEngine.ContainerRemove(ctx, id, force, vols)
}

func TestPagesReplacedContainerThatCannotBeRemovedDiscardsTheNewOne(t *testing.T) {
	m, fe, _ := faultyPagesManager(t)
	pages, alt := agentprotocol.PagesContainer, agentprotocol.PagesContainerAlt
	require.NoError(t, m.Apply(twoRoutes()))

	m.cfg.Engine = &failFirstRemove{faultEngine: fe}
	err := m.Apply(maintenanceTable())
	require.ErrorContains(t, err, "remove replaced pages container: container is paused")
	_, _, altLeft := fe.container(alt)
	require.False(t, altLeft, "two servers with different content must not answer together")
	_, _, origLeft := fe.container(pages)
	require.True(t, origLeft)
}

func TestPagesImagePullFailure(t *testing.T) {
	m, fe, _ := faultyPagesManager(t)
	fe.set("pull", errors.New("pull denied"))
	// Without maintenance the table applies with Traefik's own error pages.
	require.NoError(t, m.Apply(twoRoutes()))
	require.False(t, m.pagesUp())

	err := m.Apply(maintenanceTable())
	require.ErrorContains(t, err, "maintenance pages unavailable: pull pages image: pull denied")
	require.Equal(t, twoRoutes(), m.Current())
}
