package buildplacement

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/testutil"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

type fixture struct {
	t    *testing.T
	pool *pgxpool.Pool
	q    *db.Queries
	ws   db.Workspace
	prj  db.Project
	// target is an online amd64 server that deploys the service.
	target db.Server
	svc    uuid.UUID
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	pool := testutil.NewPool(t)
	q := db.New(pool)
	bg := context.Background()
	ws, err := q.CreateWorkspace(bg, "w")
	require.NoError(t, err)
	prj, err := q.CreateProject(bg, db.CreateProjectParams{WorkspaceID: ws.ID, Name: "Shop", Slug: "shop"})
	require.NoError(t, err)
	f := &fixture{t: t, pool: pool, q: q, ws: ws, prj: prj, svc: uuid.New()}
	f.target = f.server("target", "amd64", false)
	return f
}

// server adds an online server; builders also get the build role and capability.
func (f *fixture) server(name, arch string, builder bool) db.Server {
	f.t.Helper()
	bg := context.Background()
	srv, err := f.q.CreateServer(bg, db.CreateServerParams{WorkspaceID: f.ws.ID, Name: name})
	require.NoError(f.t, err)
	caps := []string{agentprotocol.CapProxyV2, agentprotocol.CapInstanceTunnel}
	if builder {
		caps = append(caps, agentprotocol.CapBuildPush)
	}
	facts, _ := json.Marshal(agentprotocol.Facts{OS: "linux", Arch: arch, Capabilities: caps})
	_, err = f.pool.Exec(bg, "UPDATE servers SET status = 'online', last_seen_at = now(), facts = $2, build_enabled = $3 WHERE id = $1", srv.ID, facts, builder)
	require.NoError(f.t, err)
	srv, err = f.q.LoadServerRow(bg, db.LoadServerRowParams{ID: srv.ID})
	require.NoError(f.t, err)
	return srv
}

func (f *fixture) exec(sql string, args ...any) {
	f.t.Helper()
	_, err := f.pool.Exec(context.Background(), sql, args...)
	require.NoError(f.t, err)
}

// registry inserts an external registry of the team and returns its id.
func (f *fixture) registry(name string) uuid.UUID {
	f.t.Helper()
	var id uuid.UUID
	require.NoError(f.t, f.pool.QueryRow(context.Background(),
		`INSERT INTO registries (workspace_id, name, kind, host) VALUES ($1, $2, 'external', 'registry.example.com') RETURNING id`, f.ws.ID, name).Scan(&id))
	return id
}

// builtin inserts the team's built-in registry and sets the status of the
// instance registry service.
func (f *fixture) builtin(name, serviceStatus string) uuid.UUID {
	f.t.Helper()
	bg := context.Background()
	f.exec(`INSERT INTO instance_services (kind, status) VALUES ('registry', $1) ON CONFLICT (kind) DO UPDATE SET status = $1`, serviceStatus)
	var id uuid.UUID
	require.NoError(f.t, f.pool.QueryRow(bg,
		`INSERT INTO registries (workspace_id, name, kind, host, manage_retention) VALUES ($1, $2, 'builtin', '127.0.0.1:18500', true) RETURNING id`,
		f.ws.ID, name).Scan(&id))
	return id
}

func (f *fixture) team(registry *uuid.UUID) {
	f.t.Helper()
	_, err := f.q.UpsertTeamBuildSettings(context.Background(), db.UpsertTeamBuildSettingsParams{WorkspaceID: f.ws.ID, BuildLocation: LocationBuildServer, RegistryID: registry})
	require.NoError(f.t, err)
}

func (f *fixture) scope() Scope {
	return Scope{WorkspaceID: f.ws.ID, ProjectID: f.prj.ID, ServiceID: f.svc, Target: f.target}
}

func (f *fixture) resolve(sc Scope, preview bool) (Placement, []Warning) {
	f.t.Helper()
	p, w, err := Resolve(context.Background(), f.q, sc, preview)
	require.NoError(f.t, err)
	return p, w
}

func codes(ws []Warning) []string {
	out := []string{}
	for _, w := range ws {
		out = append(out, w.Code)
	}
	return out
}

func TestDefaultsToTargetWithoutWarnings(t *testing.T) {
	f := newFixture(t)
	p, w := f.resolve(f.scope(), false)
	require.Equal(t, LocationTarget, p.Location)
	require.Nil(t, p.BuildServerID)
	require.Equal(t, "linux/amd64", p.Platform)
	require.False(t, p.Fallback)
	require.Empty(t, w)
}

func TestBuildServerChosenAndSticky(t *testing.T) {
	f := newFixture(t)
	reg := f.registry("r")
	f.team(&reg)
	a, b, c := f.server("a", "amd64", true), f.server("b", "amd64", true), f.server("c", "amd64", true)

	p, _ := f.resolve(f.scope(), false)
	require.Equal(t, LocationBuildServer, p.Location)
	require.NotNil(t, p.BuildServerID)
	require.True(t, p.Sticky)
	require.Equal(t, reg, *p.RegistryID)
	require.Equal(t, "registry.example.com", p.RegistryHost)
	first := *p.BuildServerID

	// The same service keeps its build server.
	for range 5 {
		again, _ := f.resolve(f.scope(), false)
		require.Equal(t, first, *again.BuildServerID)
	}
	// Many services spread over the servers (rendezvous hashing).
	seen := map[uuid.UUID]bool{}
	for range 60 {
		sc := f.scope()
		sc.ServiceID = uuid.New()
		got, _ := f.resolve(sc, false)
		seen[*got.BuildServerID] = true
	}
	require.Len(t, seen, 3, "services spread over all eligible build servers")
	require.True(t, seen[a.ID] && seen[b.ID] && seen[c.ID])

	// When the chosen server goes offline only its services move.
	f.exec("UPDATE servers SET status = 'offline' WHERE id = $1", first)
	moved, _ := f.resolve(f.scope(), false)
	require.NotEqual(t, first, *moved.BuildServerID)
	f.exec("UPDATE servers SET status = 'online', last_seen_at = now() WHERE id = $1", first)
	back, _ := f.resolve(f.scope(), false)
	require.Equal(t, first, *back.BuildServerID, "it returns when the server is back")
}

func TestPinWinsAndUnusablePinFallsBack(t *testing.T) {
	f := newFixture(t)
	reg := f.registry("r")
	f.team(&reg)
	a := f.server("a", "amd64", true)
	b := f.server("b", "amd64", true)
	sc := f.scope()
	sc.BuildServerID = &b.ID
	p, _ := f.resolve(sc, false)
	require.Equal(t, b.ID, *p.BuildServerID)
	require.False(t, p.Sticky)

	// The project's pin applies when the service sets none; the service's wins.
	sc.BuildServerID = nil
	f.exec("UPDATE projects SET build_server_id = $2 WHERE id = $1", f.prj.ID, a.ID)
	p, _ = f.resolve(sc, false)
	require.Equal(t, a.ID, *p.BuildServerID)
	sc.BuildServerID = &b.ID
	p, _ = f.resolve(sc, false)
	require.Equal(t, b.ID, *p.BuildServerID)

	// An unusable pin does not move to another build server: it falls back to the target.
	f.exec("UPDATE servers SET status = 'offline' WHERE id = $1", b.ID)
	p, w := f.resolve(sc, false)
	require.Equal(t, LocationTarget, p.Location)
	require.True(t, p.Fallback)
	require.Equal(t, []string{WarnFallback}, codes(w))
	require.Contains(t, w[0].Message, "No build server available; building on target")
	require.Contains(t, p.Reason, "pinned build server b is unusable")
}

func TestFallbackReasons(t *testing.T) {
	f := newFixture(t)
	reg := f.registry("r")
	f.team(&reg)

	// No build server at all.
	p, w := f.resolve(f.scope(), false)
	require.Equal(t, LocationTarget, p.Location)
	require.True(t, p.Fallback)
	require.Contains(t, p.Reason, "no build server is set up")
	require.Equal(t, []string{WarnFallback}, codes(w))

	// A build-enabled server that is offline / lacks the capability / has the role off.
	off := f.server("off", "amd64", true)
	f.exec("UPDATE servers SET status = 'offline' WHERE id = $1", off.ID)
	nocap := f.server("nocap", "amd64", false)
	f.exec("UPDATE servers SET build_enabled = true WHERE id = $1", nocap.ID)
	p, _ = f.resolve(f.scope(), false)
	require.True(t, p.Fallback)
	require.Contains(t, p.Reason, "off: offline")
	require.Contains(t, p.Reason, "nocap: agent cannot build and push")

	// An eligible one fixes it; then no registry makes it fall back again.
	f.server("ok", "amd64", true)
	p, _ = f.resolve(f.scope(), false)
	require.Equal(t, LocationBuildServer, p.Location)
	f.team(nil)
	p, w = f.resolve(f.scope(), false)
	require.True(t, p.Fallback)
	require.Contains(t, p.Reason, "no registry is configured")
	require.Equal(t, []string{WarnFallback}, codes(w))
}

func TestRegistryFromAnotherTeamOrUnhealthyFallsBack(t *testing.T) {
	f := newFixture(t)
	f.server("b", "amd64", true)

	other, err := f.q.CreateWorkspace(context.Background(), "other")
	require.NoError(t, err)
	var foreign uuid.UUID
	require.NoError(t, f.pool.QueryRow(context.Background(),
		`INSERT INTO registries (workspace_id, name, kind, host) VALUES ($1, 'x', 'external', 'evil.example.com') RETURNING id`, other.ID).Scan(&foreign))
	f.team(&foreign)
	p, _ := f.resolve(f.scope(), false)
	require.True(t, p.Fallback)
	require.Contains(t, p.Reason, "no longer exists", "another team's registry is as good as missing")

	down := f.builtin("built-in", "provisioning")
	f.team(&down)
	p, _ = f.resolve(f.scope(), false)
	require.True(t, p.Fallback)
	require.Contains(t, p.Reason, "not healthy")

	f.exec("UPDATE instance_services SET status = 'ready' WHERE kind = 'registry'")
	p, _ = f.resolve(f.scope(), false)
	require.Equal(t, LocationBuildServer, p.Location)
	require.Equal(t, "127.0.0.1:18500", p.RegistryHost)
}

// The built-in registry is reached through the agent's tunnel endpoint on both
// the build server and the target.
func TestBuiltinRegistryNeedsTheTunnelOnTargetAndBuilder(t *testing.T) {
	f := newFixture(t)
	reg := f.builtin("built-in", "ready")
	f.team(&reg)
	b := f.server("b", "amd64", true)

	p, _ := f.resolve(f.scope(), false)
	require.Equal(t, LocationBuildServer, p.Location)

	// An agent without the capability cannot use it.
	noTunnel := func(id uuid.UUID) {
		facts, _ := json.Marshal(agentprotocol.Facts{OS: "linux", Arch: "amd64", Capabilities: []string{agentprotocol.CapProxyV2, agentprotocol.CapBuildPush}})
		f.exec("UPDATE servers SET facts = $2 WHERE id = $1", id, facts)
	}
	noTunnel(b.ID)
	sc := f.scope()
	p, _ = f.resolve(sc, false)
	require.True(t, p.Fallback)
	require.Contains(t, p.Reason, "cannot reach the built-in registry")

	f.exec("UPDATE servers SET facts = $2 WHERE id = $1", b.ID, mustFacts(true))
	target, err := f.q.LoadServerRow(context.Background(), db.LoadServerRowParams{ID: f.target.ID})
	require.NoError(t, err)
	noTunnel(target.ID)
	sc.Target, err = f.q.LoadServerRow(context.Background(), db.LoadServerRowParams{ID: f.target.ID})
	require.NoError(t, err)
	p, _ = f.resolve(sc, false)
	require.True(t, p.Fallback)
	require.Contains(t, p.Reason, "cannot reach the built-in registry")
}

func mustFacts(builder bool) []byte {
	caps := []string{agentprotocol.CapProxyV2, agentprotocol.CapInstanceTunnel}
	if builder {
		caps = append(caps, agentprotocol.CapBuildPush)
	}
	facts, _ := json.Marshal(agentprotocol.Facts{OS: "linux", Arch: "amd64", Capabilities: caps})
	return facts
}

func TestOverrideOrderServiceProjectTeam(t *testing.T) {
	f := newFixture(t)
	teamReg, projReg, svcReg := f.registry("team"), f.registry("project"), f.registry("service")
	f.server("b", "amd64", true)
	f.team(&teamReg)

	p, _ := f.resolve(f.scope(), false)
	require.Equal(t, teamReg, *p.RegistryID)

	// The project overrides the team: here back to the target.
	f.exec("UPDATE projects SET build_location = 'target' WHERE id = $1", f.prj.ID)
	p, w := f.resolve(f.scope(), false)
	require.Equal(t, LocationTarget, p.Location)
	require.False(t, p.Fallback)
	require.Empty(t, w)

	// The service overrides the project; its registry beats the project's and the team's.
	f.exec("UPDATE projects SET build_location = 'build_server', registry_id = $2 WHERE id = $1", f.prj.ID, projReg)
	p, _ = f.resolve(f.scope(), false)
	require.Equal(t, projReg, *p.RegistryID)
	sc := f.scope()
	sc.Location, sc.RegistryID = LocationBuildServer, &svcReg
	p, _ = f.resolve(sc, false)
	require.Equal(t, svcReg, *p.RegistryID)
	sc.Location = LocationTarget
	p, _ = f.resolve(sc, false)
	require.Equal(t, LocationTarget, p.Location, "a service can opt out")
	sc.Location = "inherit"
	p, _ = f.resolve(sc, false)
	require.Equal(t, LocationBuildServer, p.Location)
}

func TestPreviewsNeedAnOptedInBuildServer(t *testing.T) {
	f := newFixture(t)
	reg := f.registry("r")
	f.team(&reg)
	b := f.server("b", "amd64", true)

	p, _ := f.resolve(f.scope(), false)
	require.Equal(t, LocationBuildServer, p.Location)

	p, w := f.resolve(f.scope(), true)
	require.Equal(t, LocationTarget, p.Location, "previews never build on a shared build server")
	require.True(t, p.Fallback)
	require.Contains(t, p.Reason, "previews do not build on this server")
	require.Equal(t, []string{WarnFallback}, codes(w))

	f.exec("UPDATE servers SET build_allow_previews = true WHERE id = $1", b.ID)
	p, _ = f.resolve(f.scope(), true)
	require.Equal(t, LocationBuildServer, p.Location)
	require.Equal(t, b.ID, *p.BuildServerID)
}

func TestEmulation(t *testing.T) {
	f := newFixture(t)
	reg := f.registry("r")
	f.team(&reg)
	arm := f.server("arm", "arm64", true)

	// Emulation is off by default: an arm64 builder cannot build for the amd64 target.
	p, _ := f.resolve(f.scope(), false)
	require.True(t, p.Fallback)
	require.Contains(t, p.Reason, "emulation is off")

	// Allowed but not reported by the agent.
	f.exec("UPDATE servers SET build_allow_emulation = true WHERE id = $1", arm.ID)
	p, _ = f.resolve(f.scope(), false)
	require.True(t, p.Fallback)
	require.Contains(t, p.Reason, "does not report emulation for linux/amd64")

	// Allowed and reported: it builds the target's platform under emulation, with a warning.
	facts, _ := json.Marshal(agentprotocol.Facts{OS: "linux", Arch: "arm64", Capabilities: []string{agentprotocol.CapBuildPush}, Emulation: []string{"linux/amd64"}})
	f.exec("UPDATE servers SET facts = $2 WHERE id = $1", arm.ID, facts)
	p, w := f.resolve(f.scope(), false)
	require.Equal(t, LocationBuildServer, p.Location)
	require.True(t, p.Emulated)
	require.Equal(t, "linux/amd64", p.Platform, "the image is built for the target")
	require.Contains(t, codes(w), WarnEmulation)

	// A native builder needs none of it.
	native := f.server("native", "amd64", true)
	sc := f.scope()
	sc.BuildServerID = &native.ID
	p, w = f.resolve(sc, false)
	require.False(t, p.Emulated)
	require.NotContains(t, codes(w), WarnEmulation)
}

func TestColdCacheWarningUntilTheProjectBuiltThere(t *testing.T) {
	f := newFixture(t)
	reg := f.registry("r")
	f.team(&reg)
	b := f.server("b", "amd64", true)
	_, w := f.resolve(f.scope(), false)
	require.Contains(t, codes(w), WarnColdCache)

	var envID, svcID, depID uuid.UUID
	require.NoError(t, f.pool.QueryRow(context.Background(), `INSERT INTO environments (project_id, name, slug, is_production) VALUES ($1, 'production', 'production', true) RETURNING id`, f.prj.ID).Scan(&envID))
	require.NoError(t, f.pool.QueryRow(context.Background(), `INSERT INTO services (environment_id, name, slug, kind, source_type, config) VALUES ($1, 's', 's', 'app', 'git', '{}') RETURNING id`, envID).Scan(&svcID))
	require.NoError(t, f.pool.QueryRow(context.Background(), `INSERT INTO deployments (service_id, trigger, actor_type, status, build_server_id) VALUES ($1, 'manual', 'user', 'succeeded', $2) RETURNING id`, svcID, b.ID).Scan(&depID))
	_, w = f.resolve(f.scope(), false)
	require.NotContains(t, codes(w), WarnColdCache)
}

func TestValidateTargets(t *testing.T) {
	f := newFixture(t)
	reg := f.registry("r")
	plain := f.server("plain", "amd64", false)
	b := f.server("b", "amd64", true)
	bg := context.Background()
	require.NoError(t, ValidateTargets(bg, f.q, f.ws.ID, &b.ID, &reg))
	require.Error(t, ValidateTargets(bg, f.q, f.ws.ID, &plain.ID, nil), "not a build server")
	missing := uuid.New()
	require.Error(t, ValidateTargets(bg, f.q, f.ws.ID, &missing, nil))
	require.Error(t, ValidateTargets(bg, f.q, f.ws.ID, nil, &missing))
	other, err := f.q.CreateWorkspace(bg, "other")
	require.NoError(t, err)
	require.Error(t, ValidateTargets(bg, f.q, other.ID, &b.ID, nil), "another team's server is missing")
}

func TestRepositoryAndTagNames(t *testing.T) {
	require.Equal(t, "shop-api", RepositoryName("", "shop", "api"))
	require.Equal(t, "acme/shop-api", RepositoryName("/acme/", "shop", "api"))
	id := uuid.MustParse("12345678-aaaa-bbbb-cccc-000000000000")
	require.Equal(t, "r-12345678", Tag(id))
	require.Equal(t, "nebulactrl-buildkit-12345678", BuilderName(id))
}

// The slot accounting: a build server runs at most build_max_concurrent builds;
// the next ones queue until a build finishes.
func TestSlotsCapConcurrentBuilds(t *testing.T) {
	f := newFixture(t)
	b := f.server("b", "amd64", true)
	f.exec("UPDATE servers SET build_max_concurrent = 2 WHERE id = $1", b.ID)
	bg := context.Background()

	var envID, svcID uuid.UUID
	require.NoError(t, f.pool.QueryRow(bg, `INSERT INTO environments (project_id, name, slug, is_production) VALUES ($1, 'production', 'production', true) RETURNING id`, f.prj.ID).Scan(&envID))
	require.NoError(t, f.pool.QueryRow(bg, `INSERT INTO services (environment_id, name, slug, kind, source_type, config) VALUES ($1, 's', 's', 'app', 'git', '{}') RETURNING id`, envID).Scan(&svcID))
	startBuild := func(i int) uuid.UUID {
		// One active deployment per service: use one service row per build.
		var sid, did uuid.UUID
		require.NoError(t, f.pool.QueryRow(bg, `INSERT INTO services (environment_id, name, slug, kind, source_type, config) VALUES ($1, $2, $2, 'app', 'git', '{}') RETURNING id`, envID, "svc"+string(rune('a'+i))).Scan(&sid))
		require.NoError(t, f.pool.QueryRow(bg, `INSERT INTO deployments (service_id, trigger, actor_type, status) VALUES ($1, 'manual', 'user', 'preparing') RETURNING id`, sid).Scan(&did))
		tx, err := f.pool.Begin(bg)
		require.NoError(t, err)
		ok, _, err := Slots{}.Acquire(bg, tx, b.ID, 2)
		require.NoError(t, err)
		require.True(t, ok)
		_, err = tx.Exec(bg, `INSERT INTO agent_commands (server_id, deployment_id, kind, payload, idempotency_key, expires_at) VALUES ($1, $2, 'build_image', '{}', $3, now() + interval '15 minutes')`, b.ID, did, uuid.NewString())
		require.NoError(t, err)
		require.NoError(t, tx.Commit(bg))
		return did
	}
	d1 := startBuild(0)
	startBuild(1)

	tx, err := f.pool.Begin(bg)
	require.NoError(t, err)
	ok, running, err := Slots{}.Acquire(bg, tx, b.ID, 2)
	require.NoError(t, err)
	require.False(t, ok, "the third build waits")
	require.Equal(t, 2, running)
	require.NoError(t, tx.Rollback(bg))

	// Finishing a build (its command succeeded) frees a slot...
	f.exec("UPDATE agent_commands SET status = 'succeeded' WHERE deployment_id = $1", d1)
	assertFree := func(want bool) {
		tx, err := f.pool.Begin(bg)
		require.NoError(t, err)
		defer func() { _ = tx.Rollback(bg) }()
		ok, _, err := Slots{}.Acquire(bg, tx, b.ID, 2)
		require.NoError(t, err)
		require.Equal(t, want, ok)
	}
	assertFree(true)
	// A third build takes the freed slot; the cap holds again until its deployment ends.
	d3 := startBuild(2)
	assertFree(false)
	f.exec("UPDATE deployments SET status = 'canceled' WHERE id = $1", d3)
	assertFree(true)
}
