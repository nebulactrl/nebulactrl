package proxy

import (
	"archive/tar"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/internal/agent/guard"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

type guardCopy struct {
	container, dir, name string
	mode                 int64
	content              []byte
}

// guardEngine models the two containers the guard logic looks at: the proxy
// (id set by the test) and the guard container the manager creates.
type guardEngine struct {
	docker.Engine
	mu      sync.Mutex
	proxyID string
	started time.Time
	guard   *docker.ContainerInfo
	next    int

	created []docker.ContainerSpec
	removed []string
	copies  []guardCopy
	starts  []string
	execs   [][]string
	// execCodes are the exit codes of the next execs; empty: 0.
	execCodes []int
}

func (g *guardEngine) ImageInspect(context.Context, string) (docker.ImageInfo, error) {
	return docker.ImageInfo{}, nil
}

func (g *guardEngine) ContainerInspect(_ context.Context, name string) (docker.ContainerInfo, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	switch name {
	case agentprotocol.ProxyContainer:
		if g.proxyID == "" {
			return docker.ContainerInfo{}, docker.ErrNotFound
		}
		return docker.ContainerInfo{ID: g.proxyID, Name: name, Running: true, StartedAt: g.started}, nil
	case agentprotocol.GuardContainer:
		if g.guard == nil {
			return docker.ContainerInfo{}, docker.ErrNotFound
		}
		return *g.guard, nil
	}
	return docker.ContainerInfo{}, docker.ErrNotFound
}

func (g *guardEngine) ContainerCreate(_ context.Context, spec docker.ContainerSpec) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.created = append(g.created, spec)
	g.next++
	id := "guard" + string(rune('0'+g.next))
	g.guard = &docker.ContainerInfo{ID: id, Name: spec.Name, Labels: spec.Labels, NetworkMode: spec.NetworkMode}
	return id, nil
}

func (g *guardEngine) CopyToContainer(_ context.Context, id, dir string, r io.Reader) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	tr := tar.NewReader(r)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		body, err := io.ReadAll(tr)
		if err != nil {
			return err
		}
		g.copies = append(g.copies, guardCopy{container: id, dir: dir, name: h.Name, mode: h.Mode, content: body})
	}
}

func (g *guardEngine) ContainerStart(_ context.Context, id string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.starts = append(g.starts, id)
	g.guard.Running, g.guard.StartedAt = true, time.Now()
	return nil
}

func (g *guardEngine) ContainerRemove(_ context.Context, id string, _, _ bool) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.guard == nil || (id != g.guard.ID && id != agentprotocol.GuardContainer) {
		return docker.ErrNotFound
	}
	g.removed = append(g.removed, id)
	g.guard = nil
	return nil
}

func (g *guardEngine) ContainerExec(_ context.Context, _ string, argv []string, _ docker.ExecSpec, _, _ io.Writer) (int, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.execs = append(g.execs, argv)
	if len(g.execCodes) > 0 {
		code := g.execCodes[0]
		g.execCodes = g.execCodes[1:]
		return code, nil
	}
	return 0, nil
}

func (g *guardEngine) copiesOf(name string) []guardCopy {
	g.mu.Lock()
	defer g.mu.Unlock()
	var out []guardCopy
	for _, c := range g.copies {
		if c.name == name {
			out = append(out, c)
		}
	}
	return out
}

func newGuardManager(t *testing.T, geo func() string) (*Manager, *guardEngine, string) {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "agent")
	require.NoError(t, os.WriteFile(bin, []byte("\x7fELF-fake-agent"), 0o755))
	eng := &guardEngine{proxyID: "px1", started: time.Now().Add(-time.Hour)}
	m, err := NewManager(Config{
		Engine: eng, HTTPPort: 8880, HTTPSPort: 8443, HTTPClient: &http.Client{},
		GuardBinary: bin, GuardBinaryID: "bin-v1", GeoDatabase: geo,
	})
	require.NoError(t, err)
	return m, eng, bin
}

func countryRoute(id string) agentprotocol.Route {
	return agentprotocol.Route{
		ServiceID: id, Hosts: []string{id + ".example.com"}, Upstreams: []string{"nc-app-aaaaaaaa-0:8080"},
		RouteExt: agentprotocol.RouteExt{Firewall: &agentprotocol.Firewall{Rules: []agentprotocol.FirewallRule{
			fwr("geo", blockA, notc(fwc("country", "in", "DE"))),
			func() agentprotocol.FirewallRule {
				r := fwr("off", blockA, fwc("method", "in", "GET"))
				r.Disabled = true
				return r
			}(),
		}}},
	}
}

func TestGuardFirewallsSelectsTheRoutesTheGuardEvaluates(t *testing.T) {
	country := countryRoute("11111111-1111-1111-1111-111111111111")
	pathOnly := countryRoute("22222222-2222-2222-2222-222222222222")
	pathOnly.Firewall = &agentprotocol.Firewall{Rules: []agentprotocol.FirewallRule{fwr("p", blockA, fwc("path", "prefix", "/x"))}}
	ipOnly := countryRoute("33333333-3333-3333-3333-333333333333")
	ipOnly.Firewall = &agentprotocol.Firewall{Rules: []agentprotocol.FirewallRule{fwr("i", blockA, fwc("ip", "in", "10.0.0.0/8"))}}
	ipTunnel := ipOnly
	ipTunnel.ServiceID = "44444444-4444-4444-4444-444444444444"
	ipTunnel.Domains = []agentprotocol.DomainRule{{ID: domA, Host: ipTunnel.Hosts[0], Tunnel: true}}
	disabledGeo := countryRoute("55555555-5555-5555-5555-555555555555")
	for i := range disabledGeo.Firewall.Rules {
		disabledGeo.Firewall.Rules[i].Disabled = true
	}
	none := agentprotocol.Route{ServiceID: "66666666-6666-6666-6666-666666666666", Hosts: []string{"x.example.com"}, Upstreams: []string{"a:1"}}

	got := GuardFirewalls([]agentprotocol.Route{country, pathOnly, ipOnly, ipTunnel, disabledGeo, none})
	require.Len(t, got, 2)
	require.Contains(t, got, country.ServiceID)
	require.Contains(t, got, ipTunnel.ServiceID)
	require.Equal(t, []string{"geo"}, []string{got[country.ServiceID].Rules[0].ID}, "the guard gets the active rules only")
	require.Len(t, got[country.ServiceID].Rules, 1)

	require.Empty(t, GuardFirewalls(nil))
}

func TestPrepareGuardNeedsTheAgentBinary(t *testing.T) {
	m, eng, _ := newGuardManager(t, nil)
	m.cfg.GuardBinary = ""
	err := m.prepareGuard(context.Background(), []agentprotocol.Route{countryRoute(svcApp)})
	require.ErrorContains(t, err, "edge guard")
	require.Empty(t, eng.created)

	// Without guarded routes it does nothing, binary or not.
	require.NoError(t, m.prepareGuard(context.Background(), oneRoute("a:80")))
	require.Empty(t, eng.created)
}

func TestEnsureGuardCreatesStartsAndWaitsForTheRules(t *testing.T) {
	m, eng, bin := newGuardManager(t, nil)
	fws := GuardFirewalls([]agentprotocol.Route{countryRoute(svcApp)})
	require.NoError(t, m.ensureGuard(context.Background(), fws))

	require.Len(t, eng.created, 1)
	spec := eng.created[0]
	require.Equal(t, agentprotocol.GuardContainer, spec.Name)
	require.Equal(t, PagesImage, spec.Image)
	require.Equal(t, "container:"+agentprotocol.ProxyContainer, spec.NetworkMode, "the guard shares the proxy's network namespace")
	require.Equal(t, "px1", spec.Labels[LabelGuardNetns])
	require.Equal(t, []string{guard.Binary, "guard", "--dir", guard.Dir, "--listen", "127.0.0.1:83"}, spec.Cmd)
	require.Equal(t, []string{"ALL"}, spec.CapDrop)
	require.Equal(t, []string{"NET_BIND_SERVICE"}, spec.CapAdd)
	require.Equal(t, "unless-stopped", spec.RestartPolicy)
	require.Equal(t, "true", spec.Labels[agentprotocol.LabelManaged])

	binary := eng.copiesOf("nebulactrl-agent")
	require.Len(t, binary, 1)
	want, err := os.ReadFile(bin)
	require.NoError(t, err)
	require.Equal(t, want, binary[0].content)
	require.EqualValues(t, 0o755, binary[0].mode)
	require.Equal(t, guard.Dir, binary[0].dir)
	require.Equal(t, "guard1", binary[0].container)

	rules := eng.copiesOf(guard.RulesFile)
	require.Len(t, rules, 1)
	var doc guard.Rules
	require.NoError(t, json.Unmarshal(rules[0].content, &doc))
	require.Equal(t, guard.Rules{Routes: fws}, doc)
	require.Equal(t, 1, len(eng.starts))

	require.Len(t, eng.execs, 1)
	ready := eng.execs[0]
	require.Equal(t, "wget", ready[0])
	require.Equal(t, "http://127.0.0.1:83/v1/ready?rules="+guard.Hash(rules[0].content), ready[len(ready)-1], "it waits for exactly the document it wrote")
}

func TestEnsureGuardIsIdempotentAndRewritesOnlyChangedRules(t *testing.T) {
	m, eng, _ := newGuardManager(t, nil)
	a := countryRoute(svcApp)
	fws := GuardFirewalls([]agentprotocol.Route{a})
	require.NoError(t, m.ensureGuard(context.Background(), fws))
	require.NoError(t, m.ensureGuard(context.Background(), fws))
	require.Len(t, eng.created, 1)
	require.Len(t, eng.copiesOf(guard.RulesFile), 1, "same rules: nothing is written")
	require.Len(t, eng.execs, 1, "and nothing waited for")
	require.Len(t, eng.starts, 1)

	// More firewalls: the rules file is rewritten and awaited, the container stays.
	b := countryRoute(svcWhoami)
	fws2 := GuardFirewalls([]agentprotocol.Route{a, b})
	require.NoError(t, m.ensureGuard(context.Background(), fws2))
	require.Len(t, eng.created, 1)
	rules := eng.copiesOf(guard.RulesFile)
	require.Len(t, rules, 2)
	require.Len(t, eng.copiesOf("nebulactrl-agent"), 1, "the binary is copied once")
	require.Len(t, eng.execs, 2)
	require.Equal(t, "http://127.0.0.1:83/v1/ready?rules="+guard.Hash(rules[1].content), eng.execs[1][len(eng.execs[1])-1])
	require.Len(t, eng.starts, 1)
}

func TestEnsureGuardRecreatesAGuardOutsideTheProxyNamespace(t *testing.T) {
	m, eng, _ := newGuardManager(t, nil)
	fws := GuardFirewalls([]agentprotocol.Route{countryRoute(svcApp)})
	require.NoError(t, m.ensureGuard(context.Background(), fws))
	require.Len(t, eng.created, 1)

	// The proxy container was recreated: the guard sits in the old namespace.
	eng.proxyID = "px2"
	require.NoError(t, m.ensureGuard(context.Background(), fws))
	require.Len(t, eng.created, 2)
	require.Equal(t, []string{"guard1"}, eng.removed)
	require.Equal(t, "px2", eng.created[1].Labels[LabelGuardNetns])
	require.Len(t, eng.copiesOf("nebulactrl-agent"), 2)
	require.Len(t, eng.copiesOf(guard.RulesFile), 2, "a new container gets its rules again")

	// A new agent binary makes a new guard.
	m.cfg.GuardBinaryID = "bin-v2"
	require.NoError(t, m.ensureGuard(context.Background(), fws))
	require.Len(t, eng.created, 3)
	require.Equal(t, []string{"guard1", "guard2"}, eng.removed)

	// A proxy that restarted after the guard started also lost the namespace.
	eng.started = time.Now().Add(time.Minute)
	require.NoError(t, m.ensureGuard(context.Background(), fws))
	require.Len(t, eng.created, 4)
}

func TestEnsureGuardAdoptsARunningGuardAfterAnAgentRestart(t *testing.T) {
	m, eng, _ := newGuardManager(t, nil)
	fws := GuardFirewalls([]agentprotocol.Route{countryRoute(svcApp)})
	require.NoError(t, m.ensureGuard(context.Background(), fws))

	// A new manager (the agent restarted) finds the guard and keeps it, but
	// writes the rules again.
	m2, err := NewManager(Config{Engine: eng, HTTPPort: 8880, HTTPSPort: 8443, HTTPClient: &http.Client{}, GuardBinary: m.cfg.GuardBinary, GuardBinaryID: "bin-v1"})
	require.NoError(t, err)
	require.NoError(t, m2.ensureGuard(context.Background(), fws))
	require.Len(t, eng.created, 1, "the running guard is adopted")
	require.Empty(t, eng.removed)
	require.Len(t, eng.copiesOf(guard.RulesFile), 2)
	require.Len(t, eng.starts, 1)
}

func TestEnsureGuardStartsAStoppedGuard(t *testing.T) {
	m, eng, _ := newGuardManager(t, nil)
	fws := GuardFirewalls([]agentprotocol.Route{countryRoute(svcApp)})
	require.NoError(t, m.ensureGuard(context.Background(), fws))
	eng.guard.Running = false // it crashed

	require.NoError(t, m.ensureGuard(context.Background(), fws))
	require.Len(t, eng.created, 1)
	require.Len(t, eng.starts, 2, "put back")
	require.Len(t, eng.execs, 2, "and waited for")
}

func TestEnsureGuardRetriesNotReadyUntilItServes(t *testing.T) {
	m, eng, _ := newGuardManager(t, nil)
	eng.execCodes = []int{1, 1, 0}
	require.NoError(t, m.ensureGuard(context.Background(), GuardFirewalls([]agentprotocol.Route{countryRoute(svcApp)})))
	require.Len(t, eng.execs, 3)
}

func TestEnsureGuardFailsWithoutTheProxyContainer(t *testing.T) {
	m, eng, _ := newGuardManager(t, nil)
	eng.proxyID = ""
	err := m.ensureGuard(context.Background(), GuardFirewalls([]agentprotocol.Route{countryRoute(svcApp)}))
	require.ErrorContains(t, err, "needs the proxy container")
	require.Empty(t, eng.created)
}

func TestEnsureGuardCopiesTheGeoIPDatabaseOnce(t *testing.T) {
	db := filepath.Join(t.TempDir(), "dbip-country-2026-10.mmdb")
	require.NoError(t, os.WriteFile(db, []byte("mmdb-1"), 0o644))
	m, eng, _ := newGuardManager(t, func() string { return db })
	fws := GuardFirewalls([]agentprotocol.Route{countryRoute(svcApp)})
	require.NoError(t, m.ensureGuard(context.Background(), fws))

	var geoCopies []guardCopy
	for _, c := range eng.copies {
		if strings.HasPrefix(c.name, "geo-") {
			geoCopies = append(geoCopies, c)
		}
	}
	require.Len(t, geoCopies, 1)
	require.Regexp(t, `^geo-[0-9a-f]{12}\.mmdb$`, geoCopies[0].name)
	require.Equal(t, []byte("mmdb-1"), geoCopies[0].content)
	var doc guard.Rules
	require.NoError(t, json.Unmarshal(eng.copiesOf(guard.RulesFile)[0].content, &doc))
	require.Equal(t, geoCopies[0].name, doc.Geo, "the rules name the copied database")

	// Unchanged database: not copied again, rules unchanged.
	require.NoError(t, m.ensureGuard(context.Background(), fws))
	require.Len(t, eng.copies, 3)

	// A newer database is copied under a new name and the rules switch to it.
	require.NoError(t, os.WriteFile(db, []byte("mmdb-22"), 0o644))
	require.NoError(t, m.ensureGuard(context.Background(), fws))
	require.Len(t, eng.copies, 5)
	require.NoError(t, json.Unmarshal(eng.copiesOf(guard.RulesFile)[1].content, &doc))
	require.NotEqual(t, geoCopies[0].name, doc.Geo)
	require.Regexp(t, `^geo-[0-9a-f]{12}\.mmdb$`, doc.Geo)
}

func TestEnsureGuardWithAMissingDatabaseFileWritesRulesWithoutGeo(t *testing.T) {
	m, eng, _ := newGuardManager(t, func() string { return filepath.Join(t.TempDir(), "gone.mmdb") })
	require.NoError(t, m.ensureGuard(context.Background(), GuardFirewalls([]agentprotocol.Route{countryRoute(svcApp)})))
	var doc guard.Rules
	require.NoError(t, json.Unmarshal(eng.copiesOf(guard.RulesFile)[0].content, &doc))
	require.Empty(t, doc.Geo, "country rules wait for a database (the guard drops them meanwhile)")
	require.Len(t, doc.Routes, 1)
}

func TestFinishGuardRemovesAGuardNothingNeeds(t *testing.T) {
	m, eng, _ := newGuardManager(t, nil)
	fws := GuardFirewalls([]agentprotocol.Route{countryRoute(svcApp)})
	require.NoError(t, m.ensureGuard(context.Background(), fws))

	// The table still needs it: kept, with exactly the table's firewalls.
	m.finishGuard(context.Background(), []agentprotocol.Route{countryRoute(svcApp)})
	require.Empty(t, eng.removed)

	m.finishGuard(context.Background(), oneRoute("a:80"))
	require.Equal(t, []string{agentprotocol.GuardContainer}, eng.removed)
	require.Nil(t, eng.guard)
	require.Equal(t, guardState{checked: true}, m.guard)

	// Nothing to remove the second time: no engine call.
	m.finishGuard(context.Background(), oneRoute("a:80"))
	require.Len(t, eng.removed, 1)
}

func TestFinishGuardLooksForALeftoverGuardOnceAfterStart(t *testing.T) {
	m, eng, _ := newGuardManager(t, nil)
	// A guard an earlier agent left behind.
	eng.guard = &docker.ContainerInfo{ID: "old", Name: agentprotocol.GuardContainer, Running: true}
	m.finishGuard(context.Background(), oneRoute("a:80"))
	require.Equal(t, []string{agentprotocol.GuardContainer}, eng.removed)
	require.True(t, m.guard.checked)
}

func TestGuardFirewallsFollowTheTableToTheGuard(t *testing.T) {
	// A table with the guarded firewall's route and an unguarded one: the
	// guard holds only the former, under its route id.
	m, eng, _ := newGuardManager(t, nil)
	guarded := countryRoute(svcApp)
	plain := agentprotocol.Route{ServiceID: svcWhoami, Hosts: []string{"w.example.com"}, Upstreams: []string{"a:1"}, RouteExt: agentprotocol.RouteExt{Firewall: &agentprotocol.Firewall{Rules: []agentprotocol.FirewallRule{fwr("p", blockA, fwc("path", "prefix", "/x"))}}}}
	require.NoError(t, m.prepareGuard(context.Background(), []agentprotocol.Route{guarded, plain}))
	var doc guard.Rules
	require.NoError(t, json.Unmarshal(eng.copiesOf(guard.RulesFile)[0].content, &doc))
	require.Len(t, doc.Routes, 1)
	require.Contains(t, doc.Routes, svcApp)
	require.Equal(t, "geo", doc.Routes[svcApp].Rules[0].ID)
}
