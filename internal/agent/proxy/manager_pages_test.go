package proxy

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

func pagesManager(t *testing.T) (*Manager, *fleetEngine, *fakeTraefik) {
	m, eng, tr, _ := newFleetManager(t, &memRouteStore{}, func(c *Config) { c.Pages = true })
	return m, eng, tr
}

func withMaintenance(routes []agentprotocol.Route, m *agentprotocol.Maintenance) []agentprotocol.Route {
	out := append([]agentprotocol.Route(nil), routes...)
	out[0].Maintenance = m
	return out
}

func TestPagesContainerStartsNextToTraefik(t *testing.T) {
	m, eng, tr := pagesManager(t)
	require.NoError(t, m.Apply(twoRoutes()))

	info, spec, ok := eng.container(agentprotocol.PagesContainer)
	require.True(t, ok)
	require.True(t, info.Running)
	require.Equal(t, PagesImage, spec.Image)
	require.Contains(t, PagesImage, "@sha256:", "pinned by digest")
	require.Equal(t, []docker.NetworkAttachment{{Network: agentprotocol.EdgeNetwork, Aliases: []string{agentprotocol.PagesContainer}, GwPriority: agentprotocol.EdgeGwPriority}}, spec.Networks)
	require.Empty(t, spec.Ports, "the pages server publishes nothing")
	require.Equal(t, []string{"ALL"}, spec.CapDrop)
	require.Equal(t, "true", spec.Labels[agentprotocol.LabelManaged])
	require.Equal(t, "unless-stopped", spec.RestartPolicy)
	require.Contains(t, eng.events(), "pull:"+PagesImage)

	// Its content is placed before the first start.
	ev := eng.events()
	require.Less(t, indexOf(ev, "create:"+agentprotocol.PagesContainer), indexOf(ev, "start:"+agentprotocol.PagesContainer))
	tarball := string(eng.copies[agentprotocol.PagesContainer])
	require.Contains(t, tarball, "etc/nginx/nginx.conf")
	require.Contains(t, tarball, "usr/share/nebulactrl-pages/p/503.html")

	// The Traefik config carries the errors middleware and the catch-all.
	put := tr.puts[len(tr.puts)-1]
	require.Contains(t, put, `"nc-errors"`)
	require.Contains(t, put, `"nc-pages-notfound-https"`)

	// Applying the same table again recreates nothing.
	creates := eng.count("create:" + agentprotocol.PagesContainer)
	require.NoError(t, m.Apply(twoRoutes()))
	require.Equal(t, creates, eng.count("create:"+agentprotocol.PagesContainer))
}

func indexOf(s []string, v string) int {
	for i, x := range s {
		if x == v {
			return i
		}
	}
	return -1
}

func TestMaintenanceRecreatesPagesBeforeAndAfterThePush(t *testing.T) {
	m, eng, tr := pagesManager(t)
	pages := agentprotocol.PagesContainer
	base := twoRoutes()
	require.NoError(t, m.Apply(base))
	require.Equal(t, 1, eng.count("create:"+pages))

	// Putting a service into maintenance: the page exists before the routers
	// point at it.
	var putsAtCreate []int
	eng.createErr = func(s docker.ContainerSpec) error {
		if s.Name == pages || s.Name == agentprotocol.PagesContainerAlt {
			putsAtCreate = append(putsAtCreate, len(tr.puts))
		}
		return nil
	}
	eta := time.Now().Add(time.Hour)
	on := withMaintenance(base, &agentprotocol.Maintenance{Title: "Upgrade", ETA: &eta})
	puts := len(tr.puts)
	require.NoError(t, m.Apply(on))
	require.Equal(t, []int{puts}, putsAtCreate, "pages recreated once, before the route push")
	require.Contains(t, tr.puts[len(tr.puts)-1], "/m/"+base[0].ServiceID)
	conf := pagesContent(eng)
	require.Contains(t, conf, "m-"+base[0].ServiceID+".html")

	// Same table: nothing is recreated.
	require.NoError(t, m.Apply(on))
	require.Len(t, putsAtCreate, 1)

	// Leaving maintenance: the routers move back first, then the page is dropped.
	puts = len(tr.puts)
	require.NoError(t, m.Apply(base))
	require.Len(t, putsAtCreate, 2)
	require.Equal(t, puts+1, putsAtCreate[1], "pages recreated after the route push")
	require.NotContains(t, tr.puts[len(tr.puts)-1], "/m/"+base[0].ServiceID)
	require.NotContains(t, pagesContent(eng), "m-"+base[0].ServiceID+".html")
}

func TestPagesFailureDegradesOrFails(t *testing.T) {
	m, eng, tr := pagesManager(t)
	eng.createErr = func(s docker.ContainerSpec) error {
		if s.Name == agentprotocol.PagesContainer {
			return errors.New("no space left on device")
		}
		return nil
	}
	// Without maintenance the table still applies, with Traefik's own error pages.
	require.NoError(t, m.Apply(twoRoutes()))
	require.NotContains(t, tr.puts[len(tr.puts)-1], `"nc-errors"`)
	require.NotContains(t, tr.puts[len(tr.puts)-1], "nc-pages")

	// A table that needs the pages server does not apply, and the live one stays.
	prev := m.Current()
	err := m.Apply(withMaintenance(twoRoutes(), &agentprotocol.Maintenance{}))
	require.Error(t, err)
	require.True(t, strings.Contains(err.Error(), "maintenance pages unavailable"), err.Error())
	require.Equal(t, prev, m.Current())
	require.NotContains(t, tr.puts[len(tr.puts)-1], "/m/", "the previous table is live again")

	// The server comes back on the next apply.
	eng.createErr = nil
	require.NoError(t, m.Apply(twoRoutes()))
	require.Contains(t, tr.puts[len(tr.puts)-1], `"nc-errors"`)
}

func TestPagesDisabledByDefault(t *testing.T) {
	m, eng, tr, _ := newFleetManager(t, nil, nil)
	require.NoError(t, m.Apply(twoRoutes()))
	_, _, ok := eng.container(agentprotocol.PagesContainer)
	require.False(t, ok)
	require.NotContains(t, tr.puts[len(tr.puts)-1], "nc-pages")
}

// pagesContent is the archive last copied into the pages container that exists.
func pagesContent(eng *fleetEngine) string {
	for _, name := range pagesNames {
		if _, _, ok := eng.container(name); ok {
			eng.mu.Lock()
			content := string(eng.copies[name])
			eng.mu.Unlock()
			return content
		}
	}
	return ""
}

// pagesContainers is how many pages containers exist.
func pagesContainers(eng *fleetEngine) int {
	n := 0
	for _, name := range pagesNames {
		if _, _, ok := eng.container(name); ok {
			n++
		}
	}
	return n
}

// servingPages replays the engine's events and fails when, once the pages
// server first ran, a moment came in which no container answered to its alias.
// Every router of a service in maintenance (and every error page) points
// there: such a moment is a 502 for them.
func servingPages(t *testing.T, events []string) {
	t.Helper()
	running := map[string]bool{}
	started := false
	for i, e := range events {
		kind, arg, _ := strings.Cut(e, ":")
		switch kind {
		case "start":
			if arg == agentprotocol.PagesContainer || arg == agentprotocol.PagesContainerAlt {
				running[arg], started = true, true
			}
		case "remove":
			delete(running, arg)
		}
		require.False(t, started && len(running) == 0, "no pages server runs after event %d (%s): %v", i, e, events[:i+1])
	}
}

// reachingService lists the serving routers of a service in a pushed dynamic
// configuration that send requests to the service itself (the probe routers
// always do, and the redirect routers reach nothing).
func reachingService(t *testing.T, put, serviceID string) []string {
	t.Helper()
	var cfg struct {
		HTTP struct {
			Routers map[string]struct {
				Service string `json:"service"`
			} `json:"routers"`
		} `json:"http"`
	}
	require.NoError(t, json.Unmarshal([]byte(put), &cfg))
	svc := ServiceName(serviceID)
	var out []string
	for name, r := range cfg.HTTP.Routers {
		if strings.HasPrefix(name, svc+"-") && !strings.HasSuffix(name, "-probe") && r.Service == svc {
			out = append(out, name)
		}
	}
	return out
}

// The pages container is replaced whenever a service enters or leaves
// maintenance, or its text changes, in the middle of other services' traffic
// (a deployment, another service's toggle). The old one answers until the new
// one is ready: replacing it must never leave the alias without a server.
func TestPagesReplacementNeverLeavesAWindow(t *testing.T) {
	m, eng, _ := pagesManager(t)
	base := twoRoutes()
	eta := time.Now().Add(time.Hour)
	on := withMaintenance(base, &agentprotocol.Maintenance{Title: "Upgrade", ETA: &eta})
	edited := withMaintenance(base, &agentprotocol.Maintenance{Title: "Upgrade, again", ETA: &eta})

	require.NoError(t, m.Apply(base))
	require.NoError(t, m.Apply(on))     // enters maintenance: replaced before the push
	require.NoError(t, m.Apply(edited)) // text changed: replaced before the push
	require.NoError(t, m.Apply(base))   // leaves maintenance: replaced after the push

	servingPages(t, eng.events())
	require.Equal(t, 2, eng.count("create:"+agentprotocol.PagesContainer), "the first server and the second replacement")
	require.Equal(t, 2, eng.count("create:"+agentprotocol.PagesContainerAlt), "the first and the third replacement: the names alternate")
	require.Equal(t, 1, pagesContainers(eng), "the replaced servers are gone")
	info, _, ok := eng.container(agentprotocol.PagesContainerAlt)
	require.True(t, ok)
	require.True(t, info.Running)
	require.Contains(t, pagesContent(eng), "usr/share/nebulactrl-pages/p/503.html")
	require.NotContains(t, pagesContent(eng), "m-"+base[0].ServiceID+".html")
}

// A replacement that does not start never replaces the running server.
func TestPagesReplacementThatFailsKeepsTheOldServer(t *testing.T) {
	m, eng, _ := pagesManager(t)
	pages := agentprotocol.PagesContainer
	base := twoRoutes()
	require.NoError(t, m.Apply(base))
	old, _, _ := eng.container(pages)

	eng.startErr = func(s docker.ContainerSpec) error {
		if s.Name == agentprotocol.PagesContainerAlt {
			return errors.New("cannot start")
		}
		return nil
	}
	eta := time.Now().Add(time.Hour)
	err := m.Apply(withMaintenance(base, &agentprotocol.Maintenance{Title: "Upgrade", ETA: &eta}))
	require.Error(t, err)
	servingPages(t, eng.events())

	now, _, ok := eng.container(pages)
	require.True(t, ok)
	require.Equal(t, old.ID, now.ID, "the old server still answers")
	require.Equal(t, 1, pagesContainers(eng))
}

// A refused table never ends a maintenance window. The replacement of the
// pages server failed, so the table is refused and the previous one is pushed
// again: it must find the pages of the server that still runs. Pushed without
// pages it routed every domain of the service in maintenance to the service.
func TestFailedPagesReplacementKeepsTheMaintenanceWindow(t *testing.T) {
	m, eng, tr := pagesManager(t)
	base := twoRoutes()
	eta := time.Now().Add(time.Hour)
	on := withMaintenance(base, &agentprotocol.Maintenance{Title: "Upgrade", ETA: &eta})
	edited := withMaintenance(base, &agentprotocol.Maintenance{Title: "Upgrade, again", ETA: &eta})
	require.NoError(t, m.Apply(on))
	require.Empty(t, reachingService(t, tr.puts[len(tr.puts)-1], base[0].ServiceID))

	eng.createErr = func(docker.ContainerSpec) error { return errors.New("no space left on device") }
	require.Error(t, m.Apply(edited))
	last := tr.puts[len(tr.puts)-1]
	require.Contains(t, last, "/m/"+base[0].ServiceID, "the maintenance page is still served")
	require.Empty(t, reachingService(t, last, base[0].ServiceID), "no router reaches the service")

	// The next apply with a healthy engine brings the new text.
	eng.createErr = nil
	require.NoError(t, m.Apply(edited))
	require.Contains(t, pagesContent(eng), "m-"+base[0].ServiceID+".html")
	require.Empty(t, reachingService(t, tr.puts[len(tr.puts)-1], base[0].ServiceID))
}

// Pages that are really gone (nothing runs, none can start) never expose the
// service either: a service in maintenance gets a bare 503 from the proxy.
func TestMaintenanceWithoutAPagesServerNeverReachesTheService(t *testing.T) {
	m, eng, tr := pagesManager(t)
	base := twoRoutes()
	on := withMaintenance(base, &agentprotocol.Maintenance{Title: "Upgrade"})
	require.NoError(t, m.Apply(on))
	require.NoError(t, eng.ContainerRemove(t.Context(), agentprotocol.PagesContainer, true, false))

	eng.createErr = func(docker.ContainerSpec) error { return errors.New("no space left on device") }
	require.Error(t, m.Apply(on), "the table needs pages that cannot be started")
	last := tr.puts[len(tr.puts)-1]
	require.Contains(t, last, maintenanceDownService)
	require.Empty(t, reachingService(t, last, base[0].ServiceID), "no router reaches the service")
}

// An interrupted replacement leaves both containers. One of them goes before
// anything else happens: the other would answer with whatever it was built from.
func TestPagesInterruptedReplacementIsCleanedUp(t *testing.T) {
	m, eng, _ := pagesManager(t)
	base := twoRoutes()
	require.NoError(t, m.Apply(base))

	stale, err := eng.ContainerCreate(t.Context(), docker.ContainerSpec{Name: agentprotocol.PagesContainerAlt, Image: PagesImage})
	require.NoError(t, err)
	require.NoError(t, eng.ContainerStart(t.Context(), stale))
	require.Equal(t, 2, pagesContainers(eng))

	require.NoError(t, m.Apply(withMaintenance(base, &agentprotocol.Maintenance{Title: "Upgrade"})))
	require.Equal(t, 1, pagesContainers(eng))
	servingPages(t, eng.events())
	require.Contains(t, pagesContent(eng), "m-"+base[0].ServiceID+".html")
}
