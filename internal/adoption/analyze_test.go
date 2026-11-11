package adoption

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

var (
	srvID  = uuid.MustParse("00000000-0000-4000-8000-0000000000aa")
	envA   = uuid.MustParse("e0000000-0000-4000-8000-00000000000a")
	envB   = uuid.MustParse("e0000000-0000-4000-8000-00000000000b")
	webID  = uuid.MustParse("a1111111-1111-4111-8111-111111111111")
	dbID   = uuid.MustParse("b2222222-2222-4222-8222-222222222222")
	jobID  = uuid.MustParse("c3333333-3333-4333-8333-333333333333")
	relID  = uuid.MustParse("d4444444-4444-4444-8444-444444444444")
	depA   = uuid.MustParse("f5555555-5555-4555-8555-555555555555")
	depOld = uuid.MustParse("f6666666-6666-4666-8666-666666666666")
	relDB  = uuid.MustParse("d7777777-7777-4777-8777-777777777777")
	depDB  = uuid.MustParse("f8888888-8888-4888-8888-888888888888")
	relJob = uuid.MustParse("d9999999-9999-4999-8999-999999999999")
	depJob = uuid.MustParse("fa999999-9999-4999-8999-999999999999")
	t0     = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
)

type ctr struct {
	svc, env, dep, rel uuid.UUID
	slug               string
	idx                int
	image              string
	tags, digests      []string
	env_               []string
	imageEnv           []string
	cmd, imageCmd      []string
	port               int
	running            bool
	created            time.Time
	mounts             []agentprotocol.InventoryMount
	process            string
	procDefault        bool
	labels             map[string]string
	name               string
	specImage          string
}

func (o ctr) build(t *testing.T) agentprotocol.InventoryContainer {
	t.Helper()
	spec := agentprotocol.SpecLabel{
		Slug: o.slug, Port: o.port, StopGraceSeconds: 30,
		Healthcheck: agentprotocol.HealthcheckSpec{Type: "http", Path: "/", Port: o.port, IntervalSeconds: 2, TimeoutSeconds: 3, DeadlineSeconds: 120, SuccessThreshold: 2},
		Process:     o.process, ProcessDefault: o.procDefault, Image: o.specImage,
	}
	if o.port == 0 {
		spec.Healthcheck = agentprotocol.HealthcheckSpec{Type: "none", IntervalSeconds: 2, TimeoutSeconds: 3, DeadlineSeconds: 120, SuccessThreshold: 2}
	}
	raw, err := json.Marshal(spec)
	require.NoError(t, err)
	name := agentprotocol.ContainerName(o.slug, o.dep.String(), o.idx)
	if o.process != "" {
		name = agentprotocol.ProcessContainerName(o.slug, o.dep.String(), o.process, o.procDefault, o.idx)
	}
	if o.name != "" {
		name = o.name
	}
	labels := map[string]string{
		agentprotocol.LabelManaged: "true", agentprotocol.LabelService: o.svc.String(), agentprotocol.LabelDeployment: o.dep.String(),
		agentprotocol.LabelRelease: o.release().String(), agentprotocol.LabelEnvironment: o.env.String(), agentprotocol.LabelReplica: itoa(o.idx),
		agentprotocol.LabelSpec: string(raw),
	}
	if o.process != "" {
		labels[agentprotocol.LabelProcess] = o.process
	}
	for k, v := range o.labels {
		labels[k] = v
	}
	created := o.created
	if created.IsZero() {
		created = t0
	}
	return agentprotocol.InventoryContainer{
		ID: "id-" + name, Name: "/" + name, Image: o.image, ImageID: "sha256:" + o.slug, State: "running", Running: o.running, Created: created, Labels: labels,
		Env: o.env_, Cmd: o.cmd, Mounts: o.mounts, ImageRepoTags: o.tags, ImageRepoDigests: o.digests, ImageEnv: o.imageEnv, ImageCmd: o.imageCmd,
	}
}

func (o ctr) release() uuid.UUID {
	if o.rel == uuid.Nil {
		return relID
	}
	return o.rel
}

func itoa(i int) string { return string(rune('0' + i)) }

func newContext() Context {
	return Context{
		Server:       ContextServer{ID: srvID, Name: "homelab", Held: true, Unrestricted: false, Current: map[uuid.UUID]bool{}, Unmanaged: map[uuid.UUID]bool{}},
		Environments: map[uuid.UUID]ContextEnv{}, Services: map[uuid.UUID]ContextService{}, Domains: map[string]bool{},
	}
}

func webContainers(t *testing.T, n int, volume ...bool) []agentprotocol.InventoryContainer {
	mounts := []agentprotocol.InventoryMount{{Type: "bind", Source: "/etc/x", Destination: "/x"}}
	if len(volume) > 0 && volume[0] {
		mounts = append(mounts, agentprotocol.InventoryMount{Type: "volume", Name: agentprotocol.VolumeName(webID.String(), "uploads"), Destination: "/data"})
	}
	var out []agentprotocol.InventoryContainer
	for i := range n {
		out = append(out, ctr{
			svc: webID, env: envA, dep: depA, slug: "web", idx: i, image: "nginx@sha256:abc", tags: []string{"nginx:1.27"}, digests: []string{"nginx@sha256:abc"},
			port: 80, imageEnv: []string{"PATH=/usr/bin", "NGINX_VERSION=1.27"},
			env_: []string{"PATH=/usr/bin", "NGINX_VERSION=1.27", "PORT=80", "NEBULA_SERVICE_ID=" + webID.String(), "NEBULA_DEPLOYMENT_ID=" + depA.String(),
				"NEBULA_REPLICA_INDEX=" + itoa(i), "APP_MODE=prod", "DB_PASSWORD=hunter2", "DATABASE_URL=postgres://u:pw@db/x", "GREETING=${{x}}"},
			running: true,
			mounts:  mounts,
		}.build(t))
	}
	return out
}

func webRoute() agentprotocol.Route {
	return agentprotocol.Route{
		ServiceID: webID.String(), Hosts: []string{"app.example.com"}, Upstreams: []string{"nc-web-f5555555-0:80", "nc-web-f5555555-1:80"},
		RouteExt: agentprotocol.RouteExt{Domains: []agentprotocol.DomainRule{{ID: uuid.NewString(), Host: "app.example.com", Options: agentprotocol.RouteOptions{Compress: true}}}},
	}
}

func findService(t *testing.T, p *AdoptionPlan, id uuid.UUID) AdoptionService {
	t.Helper()
	for _, e := range p.Environments {
		for _, s := range e.Services {
			if s.ID == id {
				return s
			}
		}
	}
	t.Fatalf("service %s not in plan", id)
	return AdoptionService{}
}

func TestAnalyzeImageAppWithDomain(t *testing.T) {
	inv := agentprotocol.WorkloadInventory{Containers: webContainers(t, 2), Routes: []agentprotocol.Route{webRoute()}}
	p := Analyze(inv, newContext(), AdoptionChoices{})
	require.Empty(t, p.Blockers)
	require.Len(t, p.Environments, 1)
	e := p.Environments[0]
	require.Equal(t, envA, e.ID)
	require.Equal(t, "create", e.Action)
	require.Equal(t, "production", e.Name)
	require.True(t, e.Production)
	require.Equal(t, "Adopted from homelab", e.ProjectName)
	require.Equal(t, "adopted-from-homelab", e.ProjectSlug)

	s := findService(t, p, webID)
	require.Equal(t, "adopt", s.Action)
	require.True(t, s.Adoptable, s.Reason)
	require.Equal(t, "web", s.Slug)
	require.Equal(t, "app", s.Kind)
	require.Equal(t, 2, s.Replicas)
	require.Equal(t, "nginx:1.27", s.Image)
	require.Equal(t, depA, *s.DeploymentID)
	require.Equal(t, relID, *s.ReleaseID)
	require.Len(t, s.Containers, 2)
	require.Len(t, s.Domains, 1)
	require.True(t, s.Domains[0].Include, s.Domains[0].Note)
	require.Equal(t, 80, s.Domains[0].TargetPort)
	require.Empty(t, s.Volumes)
	require.Contains(t, s.Notes[len(s.Notes)-1], "bind mount")
	require.Equal(t, 80, s.rec.cfg.Port)
	require.Equal(t, "nginx@sha256:abc", s.rec.source.Digest)

	keys := map[string]AdoptionVariable{}
	for _, v := range s.Env {
		keys[v.Key] = v
	}
	require.Contains(t, keys, "APP_MODE")
	require.NotContains(t, keys, "PORT")
	require.NotContains(t, keys, "NEBULA_SERVICE_ID")
	require.NotContains(t, keys, "PATH")
	require.NotContains(t, keys, "NGINX_VERSION")
	require.True(t, keys["DB_PASSWORD"].Secret)
	require.Equal(t, "name", keys["DB_PASSWORD"].SecretReason)
	require.True(t, keys["DATABASE_URL"].Secret)
	require.Equal(t, "url credentials", keys["DATABASE_URL"].SecretReason)
	require.False(t, keys["APP_MODE"].Secret)
	// the release keeps the platform variables but not the per-container ones or the image's own
	require.Equal(t, "80", s.rec.releaseVar["PORT"])
	require.NotContains(t, s.rec.releaseVar, "NEBULA_DEPLOYMENT_ID")
	require.NotContains(t, s.rec.releaseVar, "NEBULA_REPLICA_INDEX")
	require.NotContains(t, s.rec.releaseVar, "PATH")
	require.Equal(t, AdoptionTotals{Adopt: 1, Containers: 2}, p.Totals)

	// the plan never carries a value
	raw, err := json.Marshal(p)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "hunter2")
	require.NotContains(t, string(raw), "pw@db")
}

func TestAnalyzeChoices(t *testing.T) {
	inv := agentprotocol.WorkloadInventory{Containers: webContainers(t, 1), Routes: []agentprotocol.Route{webRoute()}}
	off := false
	p := Analyze(inv, newContext(), AdoptionChoices{
		Environments: map[uuid.UUID]AdoptionEnvironmentChoice{envA: {ProjectName: "Legacy", Name: "live", Production: &off}},
		Services:     map[uuid.UUID]AdoptionServiceChoice{webID: {Name: "Web app", Secret: map[string]bool{"APP_MODE": true, "DB_PASSWORD": false}, SkipHosts: []string{"APP.example.com"}}},
	})
	require.Empty(t, p.Blockers)
	e := p.Environments[0]
	require.Equal(t, "Legacy", e.ProjectName)
	require.Equal(t, "live", e.Name)
	require.False(t, e.Production)
	s := e.Services[0]
	require.Equal(t, "Web app", s.Name)
	require.False(t, s.Domains[0].Include)
	for _, v := range s.Env {
		switch v.Key {
		case "APP_MODE":
			require.True(t, v.Secret)
			require.Equal(t, "choice", v.SecretReason)
		case "DB_PASSWORD":
			require.False(t, v.Secret)
		}
	}
	// leave
	p = Analyze(inv, newContext(), AdoptionChoices{Services: map[uuid.UUID]AdoptionServiceChoice{webID: {Action: "leave"}}})
	require.Equal(t, AdoptionTotals{Leave: 1, Containers: 1}, p.Totals)
	require.Equal(t, "none", p.Environments[0].Action, "an environment without an adopted service is not created")
}

func TestAnalyzePostgresDatabase(t *testing.T) {
	c := ctr{
		svc: dbID, env: envA, dep: depA, slug: "db", image: "postgres@sha256:def", tags: []string{"postgres:17"}, digests: []string{"postgres@sha256:def"},
		port: 5432, running: true, imageEnv: []string{"PG_MAJOR=17", "PGDATA=/var/lib/postgresql/data"},
		env_:   []string{"PG_MAJOR=17", "PGDATA=/var/lib/postgresql/data", "POSTGRES_USER=app", "POSTGRES_PASSWORD=pw", "POSTGRES_DB=shop"},
		mounts: []agentprotocol.InventoryMount{{Type: "volume", Name: agentprotocol.VolumeName(dbID.String(), "data"), Destination: "/var/lib/postgresql/data"}},
	}.build(t)
	p := Analyze(agentprotocol.WorkloadInventory{Containers: []agentprotocol.InventoryContainer{c}}, newContext(), AdoptionChoices{})
	require.Empty(t, p.Blockers)
	s := findService(t, p, dbID)
	require.True(t, s.Adoptable, s.Reason)
	require.Equal(t, "database", s.Kind)
	require.Equal(t, &AdoptionDatabase{Engine: "postgres", Version: "17", DatabaseName: "shop", Username: "app"}, s.Database)
	require.Equal(t, "postgres:17", s.Image)
	require.Equal(t, "adopt", s.Action)
	for _, v := range s.Env {
		if v.Key == "POSTGRES_PASSWORD" {
			require.True(t, v.Secret)
			require.Equal(t, "database password", v.SecretReason)
		}
	}
	require.Equal(t, "17", s.rec.cfg.Database.Version)

	// an engine image with a tag the catalog does not offer stays an image service
	c = ctr{
		svc: dbID, env: envA, dep: depA, slug: "db", image: "postgres:9.6", tags: []string{"postgres:9.6"}, port: 5432, running: true,
		env_: []string{"POSTGRES_PASSWORD=pw"},
	}.build(t)
	p = Analyze(agentprotocol.WorkloadInventory{Containers: []agentprotocol.InventoryContainer{c}}, newContext(), AdoptionChoices{})
	s = findService(t, p, dbID)
	require.True(t, s.Adoptable, s.Reason)
	require.Equal(t, "app", s.Kind)
	require.Nil(t, s.Database)
	require.Contains(t, s.Notes[0], "not a managed catalog version")
}

func TestAnalyzeStandbyIsNotAdopted(t *testing.T) {
	c := ctr{svc: dbID, env: envA, dep: depA, slug: "db-r", image: "postgres:17", tags: []string{"postgres:17"}, port: 5432, running: true,
		env_: []string{"POSTGRES_REPLICATION_PASSWORD=x"}}.build(t)
	p := Analyze(agentprotocol.WorkloadInventory{Containers: []agentprotocol.InventoryContainer{c}}, newContext(), AdoptionChoices{})
	s := findService(t, p, dbID)
	require.False(t, s.Adoptable)
	require.Equal(t, "database", s.Kind)
	require.Equal(t, "leave", s.Action)
	require.Empty(t, p.Blockers)
}

func TestAnalyzeBuiltImageNote(t *testing.T) {
	c := ctr{svc: webID, env: envA, dep: depA, slug: "api", image: "nc-build/api:f5555555", tags: []string{"nc-build/api:f5555555"}, port: 8080, running: true}.build(t)
	p := Analyze(agentprotocol.WorkloadInventory{Containers: []agentprotocol.InventoryContainer{c}}, newContext(), AdoptionChoices{})
	s := findService(t, p, webID)
	require.Equal(t, "adopt", s.Action)
	require.Contains(t, s.Notes[0], "Built from git by an earlier NebulaCtrl")
}

// An image keeps the tag of every registry address it was pushed to: the
// reference the containers' spec label records wins, and the pinned digest is
// the one of that repository. Without a record the pick is said in a note.
func TestAnalyzeImageRefAmongSeveralTags(t *testing.T) {
	const stale, current = "127.0.0.1:49452/app:dev", "127.0.0.1:61443/app:dev"
	base := ctr{svc: webID, env: envA, dep: depA, slug: "web", image: "sha256:web", port: 80, running: true,
		tags: []string{current, stale}, digests: []string{"127.0.0.1:49452/app@sha256:aaa", "127.0.0.1:61443/app@sha256:bbb"}}

	recorded := base
	recorded.specImage = current
	p := Analyze(agentprotocol.WorkloadInventory{Containers: []agentprotocol.InventoryContainer{recorded.build(t)}}, newContext(), AdoptionChoices{})
	s := findService(t, p, webID)
	require.Equal(t, current, s.Image)
	require.Equal(t, "127.0.0.1:61443/app@sha256:bbb", s.rec.source.Digest)
	for _, n := range s.Notes {
		require.NotContains(t, n, "several tags")
	}

	p = Analyze(agentprotocol.WorkloadInventory{Containers: []agentprotocol.InventoryContainer{base.build(t)}}, newContext(), AdoptionChoices{})
	s = findService(t, p, webID)
	require.Equal(t, stale, s.Image, "without a record the first tag is taken")
	require.Equal(t, "127.0.0.1:49452/app@sha256:aaa", s.rec.source.Digest, "the digest of the taken tag's repository")
	require.True(t, slices.ContainsFunc(s.Notes, func(n string) bool { return strings.Contains(n, "several tags") }), "notes: %v", s.Notes)

	gone := base
	gone.specImage = "registry.example.com/app:old" // no longer a tag of the image
	p = Analyze(agentprotocol.WorkloadInventory{Containers: []agentprotocol.InventoryContainer{gone.build(t)}}, newContext(), AdoptionChoices{})
	require.Equal(t, stale, findService(t, p, webID).Image)
}

func TestAnalyzeStackIsLeft(t *testing.T) {
	c := ctr{svc: webID, env: envA, dep: depA, slug: "stk", image: "redis", port: 0, running: true, labels: map[string]string{agentprotocol.LabelComponent: "cache"}}.build(t)
	p := Analyze(agentprotocol.WorkloadInventory{Containers: []agentprotocol.InventoryContainer{c}}, newContext(), AdoptionChoices{})
	s := findService(t, p, webID)
	require.False(t, s.Adoptable)
	require.Equal(t, "stack", s.Kind)
	require.Equal(t, "leave", s.Action)
	require.Contains(t, s.Reason, "Compose stacks are not adopted")
	require.Empty(t, p.Blockers)
	require.Equal(t, AdoptionTotals{Leave: 1, Containers: 1}, p.Totals)

	// adopting it anyway blocks
	p = Analyze(agentprotocol.WorkloadInventory{Containers: []agentprotocol.InventoryContainer{c}}, newContext(),
		AdoptionChoices{Services: map[uuid.UUID]AdoptionServiceChoice{webID: {Action: "adopt"}}})
	require.Len(t, p.Blockers, 1)
}

func TestAnalyzeRenamedContainerIsNotAdopted(t *testing.T) {
	c := ctr{svc: webID, env: envA, dep: depA, slug: "web", image: "nginx:1.27", tags: []string{"nginx:1.27"}, port: 80, running: true, name: "evil"}.build(t)
	p := Analyze(agentprotocol.WorkloadInventory{Containers: []agentprotocol.InventoryContainer{c}}, newContext(), AdoptionChoices{})
	s := findService(t, p, webID)
	require.False(t, s.Adoptable)
	require.Equal(t, "container name does not match its labels", s.Reason)
	require.Equal(t, "leave", s.Action)
}

func TestAnalyzeMissingLabels(t *testing.T) {
	c := ctr{svc: webID, env: envA, dep: depA, slug: "web", image: "nginx", tags: []string{"nginx:1"}, port: 80, running: true}.build(t)
	delete(c.Labels, agentprotocol.LabelRelease)
	p := Analyze(agentprotocol.WorkloadInventory{Containers: []agentprotocol.InventoryContainer{c}}, newContext(), AdoptionChoices{})
	s := findService(t, p, webID)
	require.False(t, s.Adoptable)
	require.Equal(t, "missing NebulaCtrl labels", s.Reason)
	require.Equal(t, uuid.Nil, p.Environments[0].ID)
}

func TestAnalyzeRestrictedDomainsAreLeftOut(t *testing.T) {
	r := webRoute()
	r.Hosts = []string{"app.example.com", "admin.example.com", "tls.example.com", "taken.example.com"}
	r.Domains = []agentprotocol.DomainRule{
		{ID: "1", Host: "app.example.com"},
		{ID: "2", Host: "admin.example.com", AuthRef: "auth:2", Options: agentprotocol.RouteOptions{BasicAuth: true}},
		{ID: "3", Host: "tls.example.com", TLSMode: agentprotocol.TLSCustom},
		{ID: "4", Host: "taken.example.com"},
	}
	cx := newContext()
	cx.Domains["taken.example.com"] = true
	p := Analyze(agentprotocol.WorkloadInventory{Containers: webContainers(t, 1), Routes: []agentprotocol.Route{r}}, cx, AdoptionChoices{})
	s := findService(t, p, webID)
	got := map[string]AdoptionDomain{}
	for _, d := range s.Domains {
		got[d.Host] = d
	}
	require.True(t, got["app.example.com"].Include)
	require.False(t, got["admin.example.com"].Include)
	require.Contains(t, got["admin.example.com"].Note, "access rules cannot be read back")
	require.False(t, got["tls.example.com"].Include)
	require.Contains(t, got["tls.example.com"].Note, "custom certificate")
	require.False(t, got["taken.example.com"].Include)
	require.Contains(t, got["taken.example.com"].Note, "already used")
}

func TestAnalyzeOtherTeamBlocks(t *testing.T) {
	cx := newContext()
	cx.Services[webID] = ContextService{ID: webID, SameTeam: false}
	p := Analyze(agentprotocol.WorkloadInventory{Containers: webContainers(t, 1)}, cx, AdoptionChoices{})
	require.Len(t, p.Blockers, 1)
	require.Contains(t, p.Blockers[0], "belongs to another team")
	// leaving it resolves the blocker
	p = Analyze(agentprotocol.WorkloadInventory{Containers: webContainers(t, 1)}, cx, AdoptionChoices{Services: map[uuid.UUID]AdoptionServiceChoice{webID: {Action: "leave"}}})
	require.Empty(t, p.Blockers)

	// an environment of another team blocks its services too
	cx = newContext()
	cx.Environments[envA] = ContextEnv{ID: envA, SameTeam: false}
	p = Analyze(agentprotocol.WorkloadInventory{Containers: webContainers(t, 1)}, cx, AdoptionChoices{})
	require.Len(t, p.Blockers, 1)
	require.Equal(t, "none", p.Environments[0].Action)
}

func TestAnalyzeExistingServiceStates(t *testing.T) {
	cx := newContext()
	other := uuid.New()
	cx.Services[webID] = ContextService{ID: webID, SameTeam: true, EnvironmentID: envA, CurrentDeploymentID: &depA, HostGone: true}
	cx.Environments[envA] = ContextEnv{ID: envA, ProjectID: uuid.New(), ProjectName: "P", Name: "prod", Slug: "prod", SameTeam: true, ServiceSlugs: map[string]uuid.UUID{"web": webID}}
	p := Analyze(agentprotocol.WorkloadInventory{Containers: webContainers(t, 1)}, cx, AdoptionChoices{})
	require.Empty(t, p.Blockers)
	require.Equal(t, "reattach", findService(t, p, webID).Action)
	require.Equal(t, "existing", p.Environments[0].Action)

	cx.Services[webID] = ContextService{ID: webID, SameTeam: true, EnvironmentID: envA, CurrentDeploymentID: &other, HostGone: true}
	p = Analyze(agentprotocol.WorkloadInventory{Containers: webContainers(t, 1)}, cx, AdoptionChoices{})
	s := findService(t, p, webID)
	require.False(t, s.Adoptable)
	require.Equal(t, "leave", s.Action)
	require.Equal(t, "this instance already has this service with another release", s.Reason)

	// a slug taken by another service of an existing environment
	cx.Services = map[uuid.UUID]ContextService{}
	cx.Environments[envA] = ContextEnv{ID: envA, ProjectID: uuid.New(), Name: "prod", Slug: "prod", SameTeam: true, ServiceSlugs: map[string]uuid.UUID{"web": other}}
	p = Analyze(agentprotocol.WorkloadInventory{Containers: webContainers(t, 1)}, cx, AdoptionChoices{})
	require.Len(t, p.Blockers, 1)
}

func TestAnalyzeLeftoverAndNotCandidates(t *testing.T) {
	old := ctr{svc: webID, env: envA, dep: depOld, slug: "web", image: "nginx:1", tags: []string{"nginx:1"}, port: 80, running: true, created: t0.Add(-time.Hour)}.build(t)
	cs := append(webContainers(t, 1), old)
	cx := newContext()
	// a managed service of this instance is not a candidate
	cx.Server.Current[jobID] = true
	cs = append(cs, ctr{svc: jobID, env: envA, dep: depA, slug: "job", image: "x", tags: []string{"x:1"}, running: true}.build(t))
	p := Analyze(agentprotocol.WorkloadInventory{Containers: cs}, cx, AdoptionChoices{})
	s := findService(t, p, webID)
	require.Equal(t, depA, *s.DeploymentID)
	leftovers := 0
	for _, c := range s.Containers {
		if c.Leftover {
			leftovers++
		}
	}
	require.Equal(t, 1, leftovers)
	require.Len(t, p.Environments[0].Services, 1)

	// without a hold or an unmanaged mark nothing is a candidate
	cx.Server.Held = false
	p = Analyze(agentprotocol.WorkloadInventory{Containers: cs}, cx, AdoptionChoices{})
	require.Empty(t, p.Environments)
	cx.Server.Unmanaged[webID] = true
	p = Analyze(agentprotocol.WorkloadInventory{Containers: cs}, cx, AdoptionChoices{})
	require.Len(t, p.Environments, 1)
}

func TestAnalyzeProcessesAndReplicaLimit(t *testing.T) {
	var cs []agentprotocol.InventoryContainer
	for i := range 2 {
		cs = append(cs, ctr{svc: webID, env: envA, dep: depA, slug: "app", idx: i, image: "app:1", tags: []string{"app:1"}, port: 80, running: true,
			process: "web", procDefault: true}.build(t))
	}
	cs = append(cs, ctr{svc: webID, env: envA, dep: depA, slug: "app", idx: 0, image: "app:1", tags: []string{"app:1"}, running: true, process: "queue",
		cmd: []string{"run", "queue"}}.build(t))
	p := Analyze(agentprotocol.WorkloadInventory{Containers: cs}, newContext(), AdoptionChoices{})
	s := findService(t, p, webID)
	require.True(t, s.Adoptable, s.Reason)
	require.Equal(t, []string{"web", "queue"}, s.Processes)
	require.Equal(t, 2, s.Replicas)
	require.Equal(t, map[string]int{"queue": 1}, s.rec.procScale)
	require.Len(t, s.rec.cfg.Processes, 1)
	require.Equal(t, []string{"run", "queue"}, s.rec.cfg.Processes[0].StartCommand)

	var many []agentprotocol.InventoryContainer
	for i := range 11 {
		many = append(many, ctr{svc: webID, env: envA, dep: depA, slug: "web", idx: i % 10, image: "n:1", tags: []string{"n:1"}, running: true, name: agentprotocol.ContainerName("web", depA.String(), i%10)}.build(t))
	}
	// the 11th shares an index with the first: the labels are the guard, so count by containers
	p = Analyze(agentprotocol.WorkloadInventory{Containers: many}, newContext(), AdoptionChoices{})
	s = findService(t, p, webID)
	require.False(t, s.Adoptable)
	require.Contains(t, s.Reason, "at most 10")
}

func TestAnalyzeRouteOnlyAndMultipleEnvironments(t *testing.T) {
	route := agentprotocol.Route{ServiceID: jobID.String(), Hosts: []string{"ext.example.com"}, Upstreams: []string{"10.0.0.5:80"}, RouteExt: agentprotocol.RouteExt{Kind: agentprotocol.RouteKindExternal}}
	cs := append(webContainers(t, 2), ctr{svc: dbID, env: envB, dep: depA, slug: "cache", image: "x", tags: []string{"x:1"}, running: true}.build(t))
	p := Analyze(agentprotocol.WorkloadInventory{Containers: cs, Routes: []agentprotocol.Route{route}}, newContext(), AdoptionChoices{})
	require.Len(t, p.Environments, 3)
	require.Equal(t, envA, p.Environments[0].ID, "the largest environment first")
	require.Equal(t, "production", p.Environments[0].Name)
	require.Equal(t, "environment-2", p.Environments[1].Name)
	require.False(t, p.Environments[1].Production)
	require.Equal(t, p.Environments[0].ProjectSlug, p.Environments[1].ProjectSlug, "one project for all")
	route1 := findService(t, p, jobID)
	require.Equal(t, "route", route1.Kind)
	require.False(t, route1.Adoptable)
	require.Equal(t, uuid.Nil, p.Environments[2].ID)
}

func TestAnalyzeVolumes(t *testing.T) {
	odd := ctr{
		svc: webID, env: envA, dep: depA, slug: "web", image: "nginx:1", tags: []string{"nginx:1"}, port: 80, running: true,
		mounts: []agentprotocol.InventoryMount{
			{Type: "volume", Name: agentprotocol.VolumeName(webID.String(), "uploads"), Destination: "/data"},
			{Type: "volume", Name: "legacy_files", Destination: "/srv/Public Files"},
			{Type: "volume", Name: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", Destination: "/tmp/anon"},
		},
	}.build(t)
	p := Analyze(agentprotocol.WorkloadInventory{Containers: []agentprotocol.InventoryContainer{odd}}, newContext(), AdoptionChoices{})
	s := findService(t, p, webID)
	require.True(t, s.Adoptable, s.Reason)
	require.Equal(t, []AdoptionVolume{
		{Name: "public-files", MountPath: "/srv/Public Files", DockerName: "legacy_files"},
		{Name: "uploads", MountPath: "/data"},
	}, s.Volumes)
	require.Contains(t, s.Notes[len(s.Notes)-1], "anonymous volume")

	// a volume pins a service to one replica: the agent would remove the others
	cs := webContainers(t, 2, true)
	p = Analyze(agentprotocol.WorkloadInventory{Containers: cs}, newContext(), AdoptionChoices{})
	s = findService(t, p, webID)
	require.False(t, s.Adoptable)
	require.Contains(t, s.Reason, "2 replicas")
}

func TestAnalyzeDomainsThatFailTheCreateChecksAreLeftOut(t *testing.T) {
	cx := newContext()
	cx.CheckDomain = func(r agentprotocol.DomainRule) error {
		if r.Host == "claimed.example.com" {
			return errors.New("the hostname is claimed by another team")
		}
		return nil
	}
	r := webRoute()
	r.Hosts = []string{"app.example.com", "claimed.example.com"}
	r.Domains = []agentprotocol.DomainRule{{ID: "1", Host: "app.example.com", Generated: true}, {ID: "2", Host: "claimed.example.com"}}
	p := Analyze(agentprotocol.WorkloadInventory{Containers: webContainers(t, 1), Routes: []agentprotocol.Route{r}}, cx, AdoptionChoices{})
	s := findService(t, p, webID)
	got := map[string]AdoptionDomain{}
	for _, d := range s.Domains {
		got[d.Host] = d
	}
	require.True(t, got["app.example.com"].Include)
	require.False(t, got["claimed.example.com"].Include)
	require.Equal(t, "this host name is not available here: the hostname is claimed by another team", got["claimed.example.com"].Note)

	// a host routed to a port the service does not have cannot be created either
	r = webRoute()
	r.Upstreams = []string{"nc-web-f5555555-0:9999"}
	p = Analyze(agentprotocol.WorkloadInventory{Containers: webContainers(t, 1), Routes: []agentprotocol.Route{r}}, newContext(), AdoptionChoices{})
	d := findService(t, p, webID).Domains[0]
	require.False(t, d.Include)
	require.Contains(t, d.Note, "the service cannot route this host name")
}
