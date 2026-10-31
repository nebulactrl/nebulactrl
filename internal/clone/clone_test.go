package clone_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/canvas"
	"github.com/nebulactrl/nebulactrl/internal/clone"
	"github.com/nebulactrl/nebulactrl/internal/databases"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/domains"
	"github.com/nebulactrl/nebulactrl/internal/events"
	"github.com/nebulactrl/nebulactrl/internal/jobs"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/internal/testutil"
	"github.com/nebulactrl/nebulactrl/internal/varexpr"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

const canary = "sekret-canary-12345"

var testKey = bytes.Repeat([]byte{7}, 32)

// noopDNSWorker lets clones enqueue DNS checks (River validates job kinds).
type noopDNSWorker struct {
	river.WorkerDefaults[domains.DNSCheckArgs]
}

func (*noopDNSWorker) Work(context.Context, *river.Job[domains.DNSCheckArgs]) error { return nil }

type fixture struct {
	t       *testing.T
	pool    *pgxpool.Pool
	q       *db.Queries
	ctx     context.Context
	box     *secretbox.Box
	svcs    *services.Service
	dbs     *databases.Service
	cl      *clone.Service
	ws      db.Workspace
	userID  uuid.UUID
	project db.Project
	prod    db.Environment
	staging db.Environment
	server  db.Server

	postgres db.Service
	api      db.Service
	web      db.Service
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	databases.RegisterExports()
	t.Cleanup(func() { services.RegisterExportProvider(nil) })
	pool := testutil.NewPool(t)
	q := db.New(pool)
	bg := context.Background()
	ws, err := q.CreateWorkspace(bg, "w")
	require.NoError(t, err)
	u, err := q.CreateUser(bg, db.CreateUserParams{WorkspaceID: ws.ID, Email: "a@example.com", Name: "A", PasswordHash: "x"})
	require.NoError(t, err)
	prj, err := q.CreateProject(bg, db.CreateProjectParams{WorkspaceID: ws.ID, Name: "Shop", Slug: "shop"})
	require.NoError(t, err)
	prod, err := q.CreateEnvironment(bg, db.CreateEnvironmentParams{ProjectID: prj.ID, Name: "production", Slug: "production", IsProduction: true})
	require.NoError(t, err)
	staging, err := q.CreateEnvironment(bg, db.CreateEnvironmentParams{ProjectID: prj.ID, Name: "staging", Slug: "staging"})
	require.NoError(t, err)
	srv, err := q.CreateServer(bg, db.CreateServerParams{WorkspaceID: ws.ID, Name: "agent1"})
	require.NoError(t, err)
	_, err = pool.Exec(bg, "UPDATE servers SET status = 'online', base_domain = 'example.com' WHERE id = $1", srv.ID)
	require.NoError(t, err)
	srv, err = q.LoadServerRow(bg, db.LoadServerRowParams{ID: srv.ID})
	require.NoError(t, err)

	box, err := secretbox.New(testKey)
	require.NoError(t, err)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	bus := events.NewBus()
	svcs := services.NewService(pool, bus, box, nil, logger)
	dbs := databases.NewService(pool, bus, box, nil, svcs, logger)
	jm := jobs.New(pool, logger)
	jm.AddWorker(func(w *river.Workers) { river.AddWorker(w, &noopDNSWorker{}) })
	ctx := auth.WithPrincipal(bg, &auth.Principal{Type: auth.TypeUser, UserID: u.ID, WorkspaceID: ws.ID, Label: "ada@example.com", Scopes: policy.AllScopes})

	f := &fixture{
		t: t, pool: pool, q: q, ctx: ctx, box: box, svcs: svcs, dbs: dbs, ws: ws, userID: u.ID, project: prj, prod: prod, staging: staging, server: srv,
		cl: clone.NewService(clone.Deps{Pool: pool, Bus: bus, Box: box, Jobs: jm, Services: svcs, Databases: dbs, Logger: logger}),
	}

	// Production: postgres, api (git, secrets, generated secret, self and sibling
	// references, a volume, two domains), web (image, registry auth).
	pg, err := dbs.Create(ctx, databases.CreateInput{EnvironmentID: prod.ID, Name: "postgres", Engine: "postgres", Version: "17", ServerID: &srv.ID, AcceptLicense: true})
	require.NoError(t, err)
	f.postgres = f.row(pg.ID)
	f.api = f.insert(prod.ID, "api", services.KindApp, services.SourceGit, services.Config{
		GitURL: "https://github.com/acme/shop.git", GitBranch: "main", Port: 8080, MemoryMB: 512,
		Docker: &agentprotocol.DockerOptions{NoNewPrivileges: true, CapDrop: []string{"ALL"}},
	})
	f.web = f.insert(prod.ID, "web", services.KindApp, services.SourceImage, services.Config{Image: "traefik/whoami:latest", Port: 80})
	f.seal("services", "git_token_enc", f.api.ID, secretbox.ServicesGitToken, f.api.ID.String(), "ghp_basetoken")
	f.seal("services", "registry_auth_enc", f.web.ID, secretbox.ServicesRegistryAuth, f.web.ID.String(), `{"username":"u","password":"`+canary+`"}`)

	f.setVar(f.api.ID, "API_KEY", canary, true)
	f.setVar(f.api.ID, "LOG_LEVEL", "debug", false)
	f.setVar(f.api.ID, "DATABASE_URL", "${{postgres.DATABASE_URL}}", false)
	f.setVar(f.api.ID, "SELF_PORT", "${{api.PORT}}", false)
	f.setVar(f.api.ID, "TEAM_TOKEN", "${{shared.PROJECT_FLAG}}", false)
	tmpl := "${{secret(24)}}"
	m, err := varexpr.Materialize(tmpl)
	require.NoError(t, err)
	sc, err := services.LoadScopeInternal(bg, q, f.api.ID)
	require.NoError(t, err)
	_, err = services.WriteServiceVariable(bg, q, box, sc, services.VarWrite{
		Key: "SESSION_SECRET", Value: m.Stored, Secret: true, Source: services.SourceGenerated, Template: tmpl, GenCalls: m.Calls,
	})
	require.NoError(t, err)
	f.setVar(f.web.ID, "API_URL", "http://${{api.NEBULA_PRIVATE_HOST}}:${{api.PORT}}", false)

	_, err = q.InsertVolume(bg, db.InsertVolumeParams{ServiceID: f.api.ID, Name: "uploads", MountPath: "/uploads"})
	require.NoError(t, err)
	_, err = q.InsertLink(bg, db.InsertLinkParams{EnvironmentID: prod.ID, FromServiceID: f.api.ID, ToServiceID: f.postgres.ID})
	require.NoError(t, err)
	_, err = q.InsertLink(bg, db.InsertLinkParams{EnvironmentID: prod.ID, FromServiceID: f.web.ID, ToServiceID: f.api.ID})
	require.NoError(t, err)
	return f
}

func (f *fixture) row(id uuid.UUID) db.Service {
	f.t.Helper()
	sc, err := services.LoadScopeInternal(context.Background(), f.q, id)
	require.NoError(f.t, err)
	return sc.Service
}

func (f *fixture) insert(envID uuid.UUID, slug, kind, source string, cfg services.Config) db.Service {
	f.t.Helper()
	raw, _ := json.Marshal(cfg.WithDefaults())
	sv, err := f.q.InsertService(context.Background(), db.InsertServiceParams{
		ID: uuid.New(), EnvironmentID: envID, Name: slug, Slug: slug, Kind: kind, SourceType: source, Config: raw, ServerID: &f.server.ID,
	})
	require.NoError(f.t, err)
	return sv
}

// seal stores value sealed under col in table.column of row id.
func (f *fixture) seal(table, column string, id uuid.UUID, col secretbox.Column, rowKey, value string) {
	f.t.Helper()
	enc, err := f.box.SealString(col, rowKey, value)
	require.NoError(f.t, err)
	_, err = f.pool.Exec(context.Background(), "UPDATE "+table+" SET "+column+" = $2 WHERE id = $1", id, enc)
	require.NoError(f.t, err)
}

func (f *fixture) setVar(serviceID uuid.UUID, key, value string, secret bool) {
	f.t.Helper()
	sc, err := services.LoadScopeInternal(context.Background(), f.q, serviceID)
	require.NoError(f.t, err)
	_, err = services.WriteServiceVariable(context.Background(), f.q, f.box, sc, services.VarWrite{Key: key, Value: value, Secret: secret})
	require.NoError(f.t, err)
}

func (f *fixture) vars(serviceID uuid.UUID) map[string]services.RawVar {
	f.t.Helper()
	raw, err := services.LoadRawVars(context.Background(), f.q, f.box, serviceID)
	require.NoError(f.t, err)
	return raw
}

func (f *fixture) domain(serviceID uuid.UUID, host string, opts string) db.Domain {
	f.t.Helper()
	p := db.InsertDomainParams{ServiceID: serviceID, Hostname: host, TargetPort: 8080}
	if opts != "" {
		p.Options = json.RawMessage(opts)
	}
	d, err := f.q.InsertDomain(context.Background(), p)
	require.NoError(f.t, err)
	return d
}

func (f *fixture) envServices(envID uuid.UUID) map[string]db.Service {
	f.t.Helper()
	rows, err := f.q.ListAllEnvironmentServices(context.Background(), envID)
	require.NoError(f.t, err)
	out := map[string]db.Service{}
	for _, r := range rows {
		out[r.Slug] = r
	}
	return out
}

func (f *fixture) count(query string, args ...any) int {
	f.t.Helper()
	var n int
	require.NoError(f.t, f.pool.QueryRow(context.Background(), query, args...).Scan(&n))
	return n
}

// asToken returns ctx for a token holding scopes on the given projects.
func (f *fixture) asToken(projectIDs []uuid.UUID, scopes ...string) context.Context {
	id := uuid.New()
	return auth.WithPrincipal(context.Background(), &auth.Principal{
		Type: auth.TypeToken, UserID: f.userID, TokenID: &id, WorkspaceID: f.ws.ID, Label: "token", Scopes: scopes, ProjectIDs: projectIDs,
	})
}

func requireNoCanary(t *testing.T, v any) {
	t.Helper()
	raw, err := json.Marshal(v)
	require.NoError(t, err)
	require.NotContains(t, string(raw), canary)
}

// ── clone a service ─────────────────────────────────────────────────────────

func TestCloneServiceSameEnvironment(t *testing.T) {
	f := newFixture(t)
	f.domain(f.api.ID, "api.shop.example.com", "")

	res, err := f.cl.CloneService(f.ctx, f.api.ID, clone.ServiceInput{})
	require.NoError(t, err)
	require.Len(t, res.Services, 1)
	require.NotNil(t, res.Service)
	cl := f.row(res.Service.ID)
	require.Equal(t, "api copy", cl.Name)
	require.Equal(t, "api-copy", cl.Slug)
	require.Equal(t, f.prod.ID, cl.EnvironmentID)
	require.Equal(t, f.server.ID, *cl.ServerID)
	require.Equal(t, int64(0), cl.AppliedGeneration, "a clone is staged, never deployed")

	// Configuration, Docker options included.
	cfg, srcCfg := services.DecodeConfig(cl.Config), services.DecodeConfig(f.api.Config)
	require.Equal(t, srcCfg.GitURL, cfg.GitURL)
	require.Equal(t, 512, cfg.MemoryMB)
	require.NotNil(t, cfg.Docker)
	require.True(t, cfg.Docker.NoNewPrivileges)
	require.Equal(t, []string{"ALL"}, cfg.Docker.CapDrop)

	// Variables: copied, secrets still secret, references rewritten only for the clone itself.
	vars, src := f.vars(cl.ID), f.vars(f.api.ID)
	require.Equal(t, canary, vars["API_KEY"].Value)
	require.True(t, vars["API_KEY"].Secret)
	require.Equal(t, "debug", vars["LOG_LEVEL"].Value)
	require.Equal(t, "${{postgres.DATABASE_URL}}", vars["DATABASE_URL"].Value, "references to siblings stay")
	require.Equal(t, "${{api-copy.PORT}}", vars["SELF_PORT"].Value, "a reference to itself follows the clone")
	require.Equal(t, "${{shared.PROJECT_FLAG}}", vars["TEAM_TOKEN"].Value)
	require.NotEqual(t, src["SESSION_SECRET"].Value, vars["SESSION_SECRET"].Value, "generated secrets are generated again")
	require.True(t, vars["SESSION_SECRET"].Secret)
	require.Len(t, vars["SESSION_SECRET"].Value, 24)

	// The sealed bytes are bound to the new row: not a byte copy.
	srcRows, err := f.q.ListServiceVariables(context.Background(), f.api.ID)
	require.NoError(t, err)
	newRows, err := f.q.ListServiceVariables(context.Background(), cl.ID)
	require.NoError(t, err)
	srcEnc := map[string][]byte{}
	for _, v := range srcRows {
		srcEnc[v.Key] = v.ValueEnc
	}
	for _, v := range newRows {
		require.NotEqual(t, srcEnc[v.Key], v.ValueEnc, v.Key)
		require.NotContains(t, string(v.ValueEnc), canary)
	}
	_, err = f.box.OpenString(secretbox.ServiceVariablesValue, secretbox.RowID(f.api.ID.String(), "API_KEY"), newEncOf(newRows, "API_KEY"))
	require.Error(t, err, "the clone's value does not open as the source's row")

	// Credentials: sealed again for the clone.
	tok, err := f.box.OpenString(secretbox.ServicesGitToken, cl.ID.String(), cl.GitTokenEnc)
	require.NoError(t, err)
	require.Equal(t, "ghp_basetoken", tok)
	require.NotEqual(t, f.row(f.api.ID).GitTokenEnc, cl.GitTokenEnc)

	// Volumes: new and empty (a different Docker volume), same definition.
	vols, err := f.q.ListServiceVolumes(context.Background(), cl.ID)
	require.NoError(t, err)
	require.Len(t, vols, 1)
	require.Equal(t, "/uploads", vols[0].MountPath)
	srcVols, err := f.q.ListServiceVolumes(context.Background(), f.api.ID)
	require.NoError(t, err)
	require.NotEqual(t, services.DockerVolumeName(srcVols[0]), services.DockerVolumeName(vols[0]))

	// Domains are dropped, never duplicated; the source keeps its own.
	require.Len(t, res.Domains, 1)
	require.Equal(t, clone.ActionDropped, res.Domains[0].Action)
	require.Equal(t, 0, f.count("SELECT count(*) FROM domains WHERE service_id = $1", cl.ID))
	require.Equal(t, 1, f.count("SELECT count(*) FROM domains WHERE service_id = $1", f.api.ID))

	// Links: the clone consumes what the source consumes; nothing consumes the clone.
	require.Equal(t, 1, res.Links)
	require.Equal(t, 1, f.count("SELECT count(*) FROM service_links WHERE from_service_id = $1 AND to_service_id = $2", cl.ID, f.postgres.ID))
	require.Equal(t, 0, f.count("SELECT count(*) FROM service_links WHERE to_service_id = $1", cl.ID))

	// Nothing leaks: result, audit and the sealed columns.
	requireNoCanary(t, res)
	require.Equal(t, 1, f.count("SELECT count(*) FROM audit_events WHERE action = 'service.clone' AND resource_id = $1", cl.ID.String()))
	require.Equal(t, 0, f.count("SELECT count(*) FROM audit_events WHERE metadata::text LIKE '%' || $1 || '%'", canary))
	testutil.RequireSealedValuesOpen(t, f.pool, testKey)

	// The source is untouched.
	require.Equal(t, canary, f.vars(f.api.ID)["API_KEY"].Value)
	require.Equal(t, "${{api.PORT}}", f.vars(f.api.ID)["SELF_PORT"].Value)
}

func newEncOf(rows []db.ServiceVariable, key string) []byte {
	for _, r := range rows {
		if r.Key == key {
			return r.ValueEnc
		}
	}
	return nil
}

func TestCloneServiceDomainSuffixAndConflicts(t *testing.T) {
	f := newFixture(t)
	d := f.domain(f.api.ID, "api.shop.example.com", `{"basicAuth":true}`)
	hash, err := f.box.SealString(secretbox.DomainAuthUsersHash, secretbox.RowID(d.ID.String(), "admin"), "$2a$10$hashcanary")
	require.NoError(t, err)
	require.NoError(t, f.q.UpsertDomainAuthUser(context.Background(), db.UpsertDomainAuthUserParams{DomainID: d.ID, Username: "admin", HashEnc: hash}))
	f.domain(f.api.ID, "example.org", "") // an apex domain cannot be suffixed

	// A taken hostname fails the whole clone and creates nothing.
	f.domain(f.web.ID, "api-copy.shop.example.com", "")
	before := f.count("SELECT count(*) FROM services")
	_, err = f.cl.CloneService(f.ctx, f.api.ID, clone.ServiceInput{Domains: clone.DomainsSuffix})
	var ae *apierr.Error
	require.ErrorAs(t, err, &ae)
	require.Equal(t, "domain_conflict", ae.Code)
	require.Equal(t, before, f.count("SELECT count(*) FROM services"), "a conflict rolls the clone back")

	// A dry run reports the conflict and changes nothing.
	plan, err := f.cl.CloneService(f.ctx, f.api.ID, clone.ServiceInput{Domains: clone.DomainsSuffix, DryRun: true})
	require.NoError(t, err)
	require.True(t, plan.DryRun)
	require.Nil(t, plan.Service)
	actions := map[string]string{}
	for _, o := range plan.Domains {
		actions[o.Hostname] = o.Action
	}
	require.Equal(t, clone.ActionConflict, actions["api.shop.example.com"])
	require.Equal(t, clone.ActionSkipped, actions["example.org"])
	require.Equal(t, before, f.count("SELECT count(*) FROM services"))

	// Another suffix works; the basic-auth user comes along, sealed again.
	res, err := f.cl.CloneService(f.ctx, f.api.ID, clone.ServiceInput{Domains: clone.DomainsSuffix, DomainSuffix: "staging"})
	require.NoError(t, err)
	cl := f.row(res.Service.ID)
	doms, err := f.q.ListServiceDomains(context.Background(), cl.ID)
	require.NoError(t, err)
	require.Len(t, doms, 1, "the apex domain is skipped")
	require.Equal(t, "api-staging.shop.example.com", doms[0].Hostname)
	require.False(t, doms[0].Generated)
	require.Equal(t, 8080, int(doms[0].TargetPort))
	require.JSONEq(t, `{"basicAuth":true}`, string(doms[0].Options))
	hashes, err := f.q.ListDomainAuthHashes(context.Background(), []uuid.UUID{doms[0].ID})
	require.NoError(t, err)
	require.Len(t, hashes, 1)
	got, err := f.box.OpenString(secretbox.DomainAuthUsersHash, secretbox.RowID(doms[0].ID.String(), "admin"), hashes[0].HashEnc)
	require.NoError(t, err)
	require.Equal(t, "$2a$10$hashcanary", got)
	require.NotEqual(t, hash, hashes[0].HashEnc)
	// The source still owns its hostnames.
	require.Equal(t, 1, f.count("SELECT count(*) FROM domains WHERE hostname = 'api.shop.example.com'"))
	testutil.RequireSealedValuesOpen(t, f.pool, testKey)
}

func TestCloneServiceDatabaseIsNewAndEmpty(t *testing.T) {
	f := newFixture(t)
	srcVars := f.vars(f.postgres.ID)
	require.NotEmpty(t, srcVars["POSTGRES_PASSWORD"].Value)

	res, err := f.cl.CloneService(f.ctx, f.postgres.ID, clone.ServiceInput{})
	require.NoError(t, err)
	require.True(t, res.Services[0].Fresh)
	require.Contains(t, strings.Join(res.Warnings, " "), "Restore a dump")
	cl := f.row(res.Service.ID)
	require.Equal(t, services.KindDatabase, cl.Kind)
	require.NotEqual(t, f.postgres.ID, cl.ID)

	vars := f.vars(cl.ID)
	require.NotEmpty(t, vars["POSTGRES_PASSWORD"].Value)
	require.NotEqual(t, srcVars["POSTGRES_PASSWORD"].Value, vars["POSTGRES_PASSWORD"].Value, "a database gets new credentials")
	vols, err := f.q.ListServiceVolumes(context.Background(), cl.ID)
	require.NoError(t, err)
	srcVols, err := f.q.ListServiceVolumes(context.Background(), f.postgres.ID)
	require.NoError(t, err)
	require.Len(t, vols, len(srcVols))
	require.NotEqual(t, services.DockerVolumeName(srcVols[0]), services.DockerVolumeName(vols[0]), "an empty data volume of its own")
	requireNoCanary(t, res)
}

func TestCloneServiceToAnotherEnvironmentChecksReferences(t *testing.T) {
	f := newFixture(t)
	f.domain(f.api.ID, "api.shop.example.com", "")
	res, err := f.cl.CloneService(f.ctx, f.api.ID, clone.ServiceInput{EnvironmentID: &f.staging.ID, Domains: clone.DomainsSuffix})
	require.NoError(t, err)
	cl := f.row(res.Service.ID)
	require.Equal(t, f.staging.ID, cl.EnvironmentID)
	require.Equal(t, "api", cl.Slug, "the name is kept in another environment")
	require.Equal(t, "${{api.PORT}}", f.vars(cl.ID)["SELF_PORT"].Value)

	// postgres and the shared flag do not exist in staging: reported, not silent.
	refs := map[string]string{}
	for _, r := range res.References {
		refs[r.Key] = r.Ref
		require.Equal(t, clone.RefUnresolved, r.Status)
	}
	require.Equal(t, "postgres.DATABASE_URL", refs["DATABASE_URL"])
	require.Equal(t, "shared.PROJECT_FLAG", refs["TEAM_TOKEN"])
	require.NotContains(t, refs, "SELF_PORT")
	// Links belong to the source environment.
	require.Equal(t, 0, res.Links)
	require.NotEmpty(t, res.Warnings)
	doms, err := f.q.ListServiceDomains(context.Background(), cl.ID)
	require.NoError(t, err)
	require.Len(t, doms, 1)
	require.Equal(t, "api-staging.shop.example.com", doms[0].Hostname, "the default suffix is the target environment's slug")
}

func TestCloneNeedsWriteAccessToBothEnds(t *testing.T) {
	f := newFixture(t)
	other, err := f.q.CreateProject(context.Background(), db.CreateProjectParams{WorkspaceID: f.ws.ID, Name: "Other", Slug: "other"})
	require.NoError(t, err)
	otherEnv, err := f.q.CreateEnvironment(context.Background(), db.CreateEnvironmentParams{ProjectID: other.ID, Name: "production", Slug: "production"})
	require.NoError(t, err)

	// Read-only: cloning copies secrets, so it takes write access.
	_, err = f.cl.CloneService(f.asToken(nil, policy.ScopeProjectsRead), f.api.ID, clone.ServiceInput{})
	var ae *apierr.Error
	require.ErrorAs(t, err, &ae)
	require.Equal(t, 403, ae.Status)

	// A token for the other project cannot see the source (404), and one for the
	// source's project cannot write into the other project (404).
	_, err = f.cl.CloneService(f.asToken([]uuid.UUID{other.ID}, policy.ScopeProjectsWrite), f.api.ID, clone.ServiceInput{})
	require.ErrorAs(t, err, &ae)
	require.Equal(t, 404, ae.Status)
	_, err = f.cl.CloneService(f.asToken([]uuid.UUID{f.project.ID}, policy.ScopeProjectsWrite), f.api.ID, clone.ServiceInput{EnvironmentID: &otherEnv.ID})
	require.ErrorAs(t, err, &ae)
	require.Equal(t, 404, ae.Status)
	require.Equal(t, 0, len(f.envServices(otherEnv.ID)))

	// With write access to both projects a clone across projects works.
	res, err := f.cl.CloneService(f.asToken([]uuid.UUID{f.project.ID, other.ID}, policy.ScopeProjectsWrite, policy.ScopeProjectsRead), f.api.ID, clone.ServiceInput{EnvironmentID: &otherEnv.ID})
	require.NoError(t, err)
	require.Equal(t, otherEnv.ID, f.row(res.Service.ID).EnvironmentID)
}

func TestCloneRejectsRoutesStacksAndPreviews(t *testing.T) {
	f := newFixture(t)
	route := f.insert(f.prod.ID, "edge", services.KindRoute, services.SourceExternal, services.Config{})
	var ae *apierr.Error
	_, err := f.cl.CloneService(f.ctx, route.ID, clone.ServiceInput{})
	require.ErrorAs(t, err, &ae)
	require.Equal(t, "not_applicable", ae.Code)

	_, err = f.pool.Exec(context.Background(), "UPDATE environments SET kind = 'preview' WHERE id = $1", f.staging.ID)
	require.NoError(t, err)
	_, err = f.cl.CloneService(f.ctx, f.api.ID, clone.ServiceInput{EnvironmentID: &f.staging.ID})
	require.ErrorAs(t, err, &ae)
	require.Equal(t, "preview_environment", ae.Code)
}

// ── clone an environment ────────────────────────────────────────────────────

func TestCloneEnvironment(t *testing.T) {
	f := newFixture(t)
	bg := context.Background()
	f.domain(f.api.ID, "api.shop.example.com", "")
	gen, err := f.q.InsertDomain(bg, db.InsertDomainParams{ServiceID: f.web.ID, Hostname: "web-production-shop.example.com", TargetPort: 80, Generated: true})
	require.NoError(t, err)
	require.True(t, gen.Generated)
	f.insert(f.prod.ID, "edge", services.KindRoute, services.SourceExternal, services.Config{})

	// An environment-level shared variable (secret), and a canvas layout.
	sharedID := uuid.New()
	enc, err := f.box.SealString(secretbox.SharedVariablesValue, sharedID.String(), canary)
	require.NoError(t, err)
	require.NoError(t, f.q.CopySharedVariable(bg, db.CopySharedVariableParams{
		ID: sharedID, WorkspaceID: f.ws.ID, ProjectID: &f.project.ID, EnvironmentID: &f.prod.ID, Key: "PROJECT_FLAG", ValueEnc: enc, IsSecret: true, Source: services.SourceManual,
	}))
	layout, _ := json.Marshal(canvas.Layout{
		Nodes:  map[string]canvas.Pos{f.api.ID.String(): {X: 10, Y: 20}, f.postgres.ID.String(): {X: 300, Y: 20}},
		Groups: []canvas.Group{{ID: "g1", Name: "Backend", ServiceIDs: []string{f.api.ID.String(), f.postgres.ID.String()}}},
	})
	_, err = f.q.InsertCanvasLayout(bg, db.InsertCanvasLayoutParams{EnvironmentID: f.prod.ID, Layout: layout})
	require.NoError(t, err)

	// A dry run changes nothing.
	plan, err := f.cl.CloneEnvironment(f.ctx, f.prod.ID, clone.EnvironmentInput{Name: "QA", Domains: clone.DomainsSuffix, DryRun: true})
	require.NoError(t, err)
	require.True(t, plan.DryRun)
	require.Len(t, plan.Services, 3)
	require.Equal(t, 0, f.count("SELECT count(*) FROM environments WHERE name = 'QA'"))
	require.Equal(t, 4, f.count("SELECT count(*) FROM services"), "postgres, api, web and the route")

	res, err := f.cl.CloneEnvironment(f.ctx, f.prod.ID, clone.EnvironmentInput{Name: "QA", Domains: clone.DomainsSuffix})
	require.NoError(t, err)
	require.NotNil(t, res.Environment)
	require.False(t, res.Environment.IsProduction, "a clone of production is not production")
	require.Equal(t, "qa", res.Environment.Slug)
	qa := res.Environment.ID

	require.Len(t, res.Skipped, 1)
	require.Equal(t, "edge", res.Skipped[0].Slug)
	cloned := f.envServices(qa)
	require.Len(t, cloned, 3)
	for slug := range cloned {
		require.NotEqual(t, f.envServices(f.prod.ID)[slug].ID, cloned[slug].ID)
	}
	require.Empty(t, res.References, "slugs are kept: references point at the cloned siblings")

	// The database is new and empty.
	require.NotEqual(t, f.vars(f.postgres.ID)["POSTGRES_PASSWORD"].Value, f.vars(cloned["postgres"].ID)["POSTGRES_PASSWORD"].Value)
	require.Contains(t, strings.Join(res.Warnings, " "), "Restore a dump")

	// The cloned api resolves ${{postgres.DATABASE_URL}} to the cloned postgres.
	apiVars := f.vars(cloned["api"].ID)
	require.Equal(t, "${{postgres.DATABASE_URL}}", apiVars["DATABASE_URL"].Value)
	require.Equal(t, canary, apiVars["API_KEY"].Value)
	require.True(t, apiVars["API_KEY"].Secret)
	cur, err := services.LoadCurrentState(bg, f.q, f.box, cloned["api"])
	require.NoError(t, err)
	require.NotNil(t, cur.Resolved, "every reference resolves in the cloned environment")
	dbURL := cur.Resolved.Vars["DATABASE_URL"]
	require.Contains(t, dbURL, "@postgres", "the host is the clone's private name")
	require.NotContains(t, dbURL, f.vars(f.postgres.ID)["POSTGRES_PASSWORD"].Value, "and its password is the clone's, not production's")
	require.Contains(t, dbURL, f.vars(cloned["postgres"].ID)["POSTGRES_PASSWORD"].Value)

	// Links among cloned services, the environment's shared variable (sealed again) and the layout.
	require.Equal(t, 2, res.Links)
	require.Equal(t, 2, f.count("SELECT count(*) FROM service_links WHERE environment_id = $1", qa))
	require.Equal(t, 1, res.SharedVariables)
	var sharedEnc []byte
	var newSharedID uuid.UUID
	require.NoError(t, f.pool.QueryRow(bg, "SELECT id, value_enc FROM shared_variables WHERE environment_id = $1 AND key = 'PROJECT_FLAG'", qa).Scan(&newSharedID, &sharedEnc))
	require.NotEqual(t, sharedID, newSharedID)
	got, err := f.box.OpenString(secretbox.SharedVariablesValue, newSharedID.String(), sharedEnc)
	require.NoError(t, err)
	require.Equal(t, canary, got)
	row, err := f.q.GetCanvasLayout(bg, qa)
	require.NoError(t, err)
	var l canvas.Layout
	require.NoError(t, json.Unmarshal(row.Layout, &l))
	require.Contains(t, l.Nodes, cloned["api"].ID.String())
	require.NotContains(t, l.Nodes, f.api.ID.String())
	require.Len(t, l.Groups, 1)
	require.ElementsMatch(t, []string{cloned["api"].ID.String(), cloned["postgres"].ID.String()}, l.Groups[0].ServiceIDs)

	// Domains: the custom one is suffixed, the generated one regenerated for the new environment.
	byHost := map[string]string{}
	for _, o := range res.Domains {
		byHost[o.Hostname] = o.To
	}
	require.Equal(t, "api-qa.shop.example.com", byHost["api.shop.example.com"])
	require.Equal(t, "web-qa-shop.example.com", byHost["web-production-shop.example.com"])
	require.Equal(t, 1, f.count("SELECT count(*) FROM domains WHERE service_id = $1", f.api.ID))
	require.Equal(t, 1, f.count("SELECT count(*) FROM domains WHERE hostname = 'api-qa.shop.example.com' AND service_id = $1", cloned["api"].ID))

	// Registry auth of the image service was sealed again for the clone.
	reg, err := f.box.OpenString(secretbox.ServicesRegistryAuth, cloned["web"].ID.String(), cloned["web"].RegistryAuthEnc)
	require.NoError(t, err)
	require.Contains(t, reg, canary)

	requireNoCanary(t, res)
	require.Equal(t, 0, f.count("SELECT count(*) FROM audit_events WHERE metadata::text LIKE '%' || $1 || '%'", canary))
	testutil.RequireSealedValuesOpen(t, f.pool, testKey)

	// Cloning again with the same suffix is a conflict (and rolls back).
	envsBefore := f.count("SELECT count(*) FROM environments")
	_, err = f.cl.CloneEnvironment(f.ctx, f.prod.ID, clone.EnvironmentInput{Name: "QA", Domains: clone.DomainsSuffix, DomainSuffix: "qa"})
	var ae *apierr.Error
	require.ErrorAs(t, err, &ae)
	require.Equal(t, "domain_conflict", ae.Code)
	require.Equal(t, envsBefore, f.count("SELECT count(*) FROM environments"))
	again, err := f.cl.CloneEnvironment(f.ctx, f.prod.ID, clone.EnvironmentInput{Name: "QA"})
	require.NoError(t, err)
	require.Equal(t, "qa-2", again.Environment.Slug)
}

// ── move a service ──────────────────────────────────────────────────────────

func TestMoveServiceChecksReferencesAndIsStaged(t *testing.T) {
	f := newFixture(t)
	bg := context.Background()
	layout, _ := json.Marshal(canvas.Layout{
		Nodes:  map[string]canvas.Pos{f.api.ID.String(): {X: 1, Y: 2}, f.web.ID.String(): {X: 3, Y: 4}},
		Groups: []canvas.Group{{ID: "g1", Name: "Front", ServiceIDs: []string{f.api.ID.String()}}},
	})
	_, err := f.q.InsertCanvasLayout(bg, db.InsertCanvasLayoutParams{EnvironmentID: f.prod.ID, Layout: layout})
	require.NoError(t, err)
	// A deployed release of api, so the move shows as a staged change.
	snap, _ := json.Marshal(services.Snapshot{Config: services.DecodeConfig(f.api.Config), ServerID: &f.server.ID, EnvironmentID: &f.prod.ID})
	src, _ := json.Marshal(services.ReleaseSource{Type: services.SourceGit, GitURL: "https://github.com/acme/shop.git", Branch: "main"})
	rel, err := f.q.InsertRelease(bg, db.InsertReleaseParams{ID: uuid.New(), ServiceID: f.api.ID, Source: src, Config: snap})
	require.NoError(t, err)

	flag := uuid.New()
	flagEnc, err := f.box.SealString(secretbox.SharedVariablesValue, flag.String(), "on")
	require.NoError(t, err)
	require.NoError(t, f.q.CopySharedVariable(bg, db.CopySharedVariableParams{
		ID: flag, WorkspaceID: f.ws.ID, ProjectID: &f.project.ID, Key: "PROJECT_FLAG", ValueEnc: flagEnc, Source: services.SourceManual,
	}))
	// The plan: references break in both directions.
	plan, err := f.cl.MoveService(f.ctx, f.api.ID, clone.MoveInput{EnvironmentID: f.staging.ID, DryRun: true})
	require.NoError(t, err)
	require.True(t, plan.DryRun)
	require.False(t, plan.Applied)
	require.True(t, plan.Blocked)
	out := map[string]string{}
	for _, o := range plan.Outgoing {
		out[o.Key] = o.Status
	}
	require.Equal(t, map[string]string{"DATABASE_URL": clone.RefUnresolved}, out, "the project-level shared variable still resolves in staging")
	require.Len(t, plan.Incoming, 1)
	require.Equal(t, "web", plan.Incoming[0].Service)
	require.Equal(t, []string{"API_URL"}, plan.Incoming[0].Keys)
	require.ElementsMatch(t, []string{"postgres", "web"}, plan.Links)
	require.Equal(t, f.api.EnvironmentID, f.row(f.api.ID).EnvironmentID, "a dry run moves nothing")

	// Without force a blocked move is a 409 carrying the plan.
	_, err = f.cl.MoveService(f.ctx, f.api.ID, clone.MoveInput{EnvironmentID: f.staging.ID})
	var ae *apierr.Error
	require.ErrorAs(t, err, &ae)
	require.Equal(t, "move_blocked", ae.Code)
	require.Equal(t, f.prod.ID, f.row(f.api.ID).EnvironmentID)

	webGen := f.row(f.web.ID).Generation
	res, err := f.cl.MoveService(f.ctx, f.api.ID, clone.MoveInput{EnvironmentID: f.staging.ID, Force: true})
	require.NoError(t, err)
	require.True(t, res.Applied)
	require.NotNil(t, res.Service)
	moved := f.row(f.api.ID)
	require.Equal(t, f.staging.ID, moved.EnvironmentID)
	require.Equal(t, "api", moved.Slug)
	require.Greater(t, moved.Generation, moved.AppliedGeneration, "the move is staged")
	require.True(t, res.Service.HasStagedChanges)
	// Variables, volumes and the sealed secret went with it, untouched.
	require.Equal(t, canary, f.vars(f.api.ID)["API_KEY"].Value)
	require.Equal(t, 1, f.count("SELECT count(*) FROM volumes WHERE service_id = $1", f.api.ID))
	// Links are removed, the canvas node is dropped, and the dependent is staged.
	require.Equal(t, 0, f.count("SELECT count(*) FROM service_links WHERE from_service_id = $1 OR to_service_id = $1", f.api.ID))
	row, err := f.q.GetCanvasLayout(bg, f.prod.ID)
	require.NoError(t, err)
	var l canvas.Layout
	require.NoError(t, json.Unmarshal(row.Layout, &l))
	require.NotContains(t, l.Nodes, f.api.ID.String())
	require.Contains(t, l.Nodes, f.web.ID.String())
	require.Empty(t, l.Groups, "a group left empty is removed")
	require.Greater(t, f.row(f.web.ID).Generation, webGen)
	require.Equal(t, 1, f.count("SELECT count(*) FROM audit_events WHERE action = 'service.move' AND resource_id = $1", f.api.ID.String()))
	requireNoCanary(t, res)

	// The staged change names the environments.
	cur, err := services.LoadCurrentState(bg, f.q, f.box, moved)
	require.NoError(t, err)
	changes, err := services.DiffRelease(bg, f.q, f.box, rel, cur)
	require.NoError(t, err)
	var env *services.FieldChange
	for i := range changes {
		if changes[i].Field == "environment" {
			env = &changes[i]
		}
	}
	require.NotNil(t, env, "pending changes list the environment")
	require.Equal(t, "Shop / production", env.From)
	require.Equal(t, "Shop / staging", env.To)
	testutil.RequireSealedValuesOpen(t, f.pool, testKey)

	// Moving back to where it is already is refused.
	_, err = f.cl.MoveService(f.ctx, f.api.ID, clone.MoveInput{EnvironmentID: f.staging.ID})
	require.ErrorAs(t, err, &ae)
	require.Equal(t, "same_environment", ae.Code)
}

func TestMoveServiceReboundSlugAndRename(t *testing.T) {
	f := newFixture(t)
	// Staging already has a postgres and an api of its own.
	pg, err := f.dbs.Create(f.ctx, databases.CreateInput{EnvironmentID: f.staging.ID, Name: "postgres", Engine: "postgres", Version: "17", ServerID: &f.server.ID, AcceptLicense: true})
	require.NoError(t, err)
	f.insert(f.staging.ID, "api", services.KindApp, services.SourceImage, services.Config{Image: "traefik/whoami:latest", Port: 80})

	plan, err := f.cl.MoveService(f.ctx, f.api.ID, clone.MoveInput{EnvironmentID: f.staging.ID, DryRun: true})
	require.NoError(t, err)
	byKey := map[string]clone.ReferenceFinding{}
	for _, o := range plan.Outgoing {
		byKey[o.Key] = o
	}
	require.Equal(t, clone.RefRebound, byKey["DATABASE_URL"].Status, "the reference would silently point at staging's postgres")
	require.True(t, plan.SlugChanged)
	require.Equal(t, "api-2", plan.Slug)

	res, err := f.cl.MoveService(f.ctx, f.api.ID, clone.MoveInput{EnvironmentID: f.staging.ID, Force: true})
	require.NoError(t, err)
	moved := f.row(f.api.ID)
	require.Equal(t, "api-2", moved.Slug)
	require.Equal(t, "api-2", res.Service.Slug)
	require.Equal(t, "${{api-2.PORT}}", f.vars(f.api.ID)["SELF_PORT"].Value, "references to itself follow the new slug")
	require.Equal(t, pg.ID, f.envServices(f.staging.ID)["postgres"].ID)
}

func TestMoveServiceRefusals(t *testing.T) {
	f := newFixture(t)
	bg := context.Background()
	var ae *apierr.Error

	// A running deployment blocks the move.
	_, err := f.q.InsertDeployment(bg, db.InsertDeploymentParams{ServiceID: f.web.ID, ServerID: &f.server.ID, Trigger: "manual", ActorType: "user", Strategy: "recreate"})
	require.NoError(t, err)
	_, err = f.cl.MoveService(f.ctx, f.web.ID, clone.MoveInput{EnvironmentID: f.staging.ID})
	require.ErrorAs(t, err, &ae)
	require.Equal(t, "deployment_in_progress", ae.Code)

	// Cross-server state (a mesh exposure) cannot follow a move.
	_, err = f.pool.Exec(bg, `INSERT INTO mesh_exposures (workspace_id, server_id, service_id, environment_id, purpose, container_port, mesh_port)
		VALUES ($1, $2, $3, $4, 'service', 5432, 15001)`, f.ws.ID, f.server.ID, f.postgres.ID, f.prod.ID)
	require.NoError(t, err)
	_, err = f.cl.MoveService(f.ctx, f.postgres.ID, clone.MoveInput{EnvironmentID: f.staging.ID, Force: true})
	require.ErrorAs(t, err, &ae)
	require.Equal(t, "move_blocked", ae.Code)

	// Routes and stacks are not movable; previews are not valid sources or targets.
	route := f.insert(f.prod.ID, "edge", services.KindRoute, services.SourceExternal, services.Config{})
	_, err = f.cl.MoveService(f.ctx, route.ID, clone.MoveInput{EnvironmentID: f.staging.ID})
	require.ErrorAs(t, err, &ae)
	require.Equal(t, "not_applicable", ae.Code)
	_, err = f.pool.Exec(bg, "UPDATE environments SET kind = 'preview' WHERE id = $1", f.staging.ID)
	require.NoError(t, err)
	_, err = f.cl.MoveService(f.ctx, f.api.ID, clone.MoveInput{EnvironmentID: f.staging.ID, Force: true})
	require.ErrorAs(t, err, &ae)
	require.Equal(t, "preview_environment", ae.Code)

	// A token without write access cannot move; one restricted to another project sees nothing.
	_, err = f.cl.MoveService(f.asToken(nil, policy.ScopeProjectsRead), f.api.ID, clone.MoveInput{EnvironmentID: f.prod.ID})
	require.ErrorAs(t, err, &ae)
	require.Equal(t, 403, ae.Status)
	other, err := f.q.CreateProject(bg, db.CreateProjectParams{WorkspaceID: f.ws.ID, Name: "Other", Slug: "other"})
	require.NoError(t, err)
	_, err = f.cl.MoveService(f.asToken([]uuid.UUID{other.ID}, policy.ScopeProjectsWrite), f.api.ID, clone.MoveInput{EnvironmentID: f.prod.ID})
	require.ErrorAs(t, err, &ae)
	require.Equal(t, 404, ae.Status)
}

// A move without references or links needs no force, and a service can move
// into another project of the team.
func TestMoveServiceCleanAcrossProjects(t *testing.T) {
	f := newFixture(t)
	other, err := f.q.CreateProject(context.Background(), db.CreateProjectParams{WorkspaceID: f.ws.ID, Name: "Other", Slug: "other"})
	require.NoError(t, err)
	otherEnv, err := f.q.CreateEnvironment(context.Background(), db.CreateEnvironmentParams{ProjectID: other.ID, Name: "production", Slug: "production"})
	require.NoError(t, err)
	lone := f.insert(f.prod.ID, "lone", services.KindApp, services.SourceImage, services.Config{Image: "traefik/whoami:latest", Port: 80})

	res, err := f.cl.MoveService(f.ctx, lone.ID, clone.MoveInput{EnvironmentID: otherEnv.ID})
	require.NoError(t, err)
	require.True(t, res.Applied)
	require.False(t, res.Blocked)
	require.Equal(t, otherEnv.ID, f.row(lone.ID).EnvironmentID)
	require.Contains(t, strings.Join(res.Warnings, " "), "another project")
}

// configSource records that a nebula.toml apply manages the service.
func (f *fixture) configSource(svc db.Service) {
	f.t.Helper()
	_, err := f.pool.Exec(context.Background(), `INSERT INTO config_sources (service_id, environment_id, workspace_id, origin, file_sha256, applied_generation, applied_by_type)
		VALUES ($1, $2, $3, 'api', 'abc', $4, 'user')`, svc.ID, svc.EnvironmentID, f.ws.ID, svc.Generation)
	require.NoError(f.t, err)
}

func (f *fixture) hasConfigSource(id uuid.UUID) bool {
	f.t.Helper()
	return f.count("SELECT count(*) FROM config_sources WHERE service_id = $1", id) == 1
}

// ── config_sources follows the service only where it is still managed ───────

func TestMoveServiceDropsConfigSource(t *testing.T) {
	f := newFixture(t)
	lone := f.insert(f.prod.ID, "lone", services.KindApp, services.SourceImage, services.Config{Image: "traefik/whoami:latest", Port: 80})
	f.configSource(lone)
	f.configSource(f.web)
	require.True(t, f.hasConfigSource(lone.ID))

	_, err := f.cl.MoveService(f.ctx, lone.ID, clone.MoveInput{EnvironmentID: f.staging.ID})
	require.NoError(t, err)
	require.Equal(t, f.staging.ID, f.row(lone.ID).EnvironmentID)
	require.False(t, f.hasConfigSource(lone.ID), "the file of the old environment no longer manages the moved service")
	require.True(t, f.hasConfigSource(f.web.ID), "other services keep their ownership")

	// A refused move leaves the row.
	_, err = f.q.InsertDeployment(context.Background(), db.InsertDeploymentParams{ServiceID: f.web.ID, ServerID: &f.server.ID, Trigger: "manual", ActorType: "user", Strategy: "recreate"})
	require.NoError(t, err)
	_, err = f.cl.MoveService(f.ctx, f.web.ID, clone.MoveInput{EnvironmentID: f.staging.ID})
	require.Error(t, err)
	require.True(t, f.hasConfigSource(f.web.ID), "a move that fails leaves the row")
}

func TestCloneServiceDoesNotCopyConfigSource(t *testing.T) {
	f := newFixture(t)
	f.configSource(f.api)
	res, err := f.cl.CloneService(f.ctx, f.api.ID, clone.ServiceInput{})
	require.NoError(t, err)
	require.NotNil(t, res.Service)
	require.False(t, f.hasConfigSource(res.Service.ID), "a clone is not managed by the file")
	require.True(t, f.hasConfigSource(f.api.ID))
	require.Equal(t, 1, f.count("SELECT count(*) FROM config_sources"))
}

func TestDeleteServiceDropsConfigSource(t *testing.T) {
	f := newFixture(t)
	lone := f.insert(f.prod.ID, "lone", services.KindApp, services.SourceImage, services.Config{Image: "traefik/whoami:latest", Port: 80})
	f.configSource(lone)
	f.configSource(f.web)

	require.NoError(t, f.svcs.Delete(f.ctx, lone.ID, services.DeleteInput{Confirm: "lone"}))
	require.False(t, f.hasConfigSource(lone.ID))
	require.True(t, f.hasConfigSource(f.web.ID))
}
