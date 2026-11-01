package coolify

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/databases"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/deployments"
	"github.com/nebulactrl/nebulactrl/internal/domains"
	"github.com/nebulactrl/nebulactrl/internal/events"
	"github.com/nebulactrl/nebulactrl/internal/gitproviders"
	"github.com/nebulactrl/nebulactrl/internal/jobs"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/internal/testutil"
)

type fakeDeployer struct {
	mu    sync.Mutex
	calls []deployCall
}

type deployCall struct {
	env uuid.UUID
	ids []uuid.UUID
	msg string
}

func (d *fakeDeployer) DeployStaged(_ context.Context, envID uuid.UUID, ids []uuid.UUID, msg string) (deployments.StagedResult, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls = append(d.calls, deployCall{envID, ids, msg})
	return deployments.StagedResult{}, nil
}

type linkCall struct {
	service uuid.UUID
	in      gitproviders.LinkInput
}

type fakeGit struct{ calls []linkCall }

func (g *fakeGit) Link(_ context.Context, id uuid.UUID, in gitproviders.LinkInput) (gitproviders.GitLinkView, error) {
	g.calls = append(g.calls, linkCall{id, in})
	return gitproviders.GitLinkView{}, nil
}

type fixture struct {
	t        *testing.T
	pool     *pgxpool.Pool
	q        *db.Queries
	ctx      context.Context
	box      *secretbox.Box
	svc      *Service
	srv      db.Server
	ws       db.Workspace
	coolify  *fakeCoolify
	deployer *fakeDeployer
	git      *fakeGit
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
	srv, err := q.CreateServer(bg, db.CreateServerParams{WorkspaceID: ws.ID, Name: "homelab-1"})
	require.NoError(t, err)
	_, err = pool.Exec(bg, "UPDATE servers SET status = 'online', public_ip = '203.0.113.7' WHERE id = $1", srv.ID)
	require.NoError(t, err)
	srv, err = q.LoadServerRow(bg, db.LoadServerRowParams{ID: srv.ID, WorkspaceID: &ws.ID})
	require.NoError(t, err)

	box, err := secretbox.New(bytes.Repeat([]byte{7}, 32))
	require.NoError(t, err)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	bus := events.NewBus()
	svcs := services.NewService(pool, bus, box, nil, logger)
	jm := jobs.New(pool, logger)
	domains.RegisterJobs(jm, domains.NewService(pool, bus, jm, box), logger)
	ctx := auth.WithPrincipal(bg, &auth.Principal{Type: auth.TypeUser, UserID: u.ID, WorkspaceID: ws.ID, Label: "a@example.com", Scopes: policy.AllScopes})
	f := &fixture{t: t, pool: pool, q: q, ctx: ctx, box: box, srv: srv, ws: ws, coolify: newFakeCoolify(t), deployer: &fakeDeployer{}, git: &fakeGit{}}
	f.svc = NewService(Deps{
		Pool: pool, Bus: bus, Jobs: jm, Services: svcs, Databases: databases.NewService(pool, bus, box, nil, svcs, logger),
		Deploy: f.deployer, Git: f.git, Box: box, Logger: logger,
	})
	return f
}

func (f *fixture) source() CoolifySource {
	return CoolifySource{URL: f.coolify.url(), Token: fakeToken}
}

func (f *fixture) apply(ch CoolifyChoices) (CoolifyApplyResult, error) {
	return f.svc.Apply(f.ctx, CoolifyApplyRequest{Source: f.source(), Choices: ch, ServerID: f.srv.ID})
}

func (f *fixture) project(slug string) db.Project {
	f.t.Helper()
	rows, err := f.q.ListProjects(context.Background(), db.ListProjectsParams{WorkspaceID: f.ws.ID, Limit: 100})
	require.NoError(f.t, err)
	for _, p := range rows {
		if p.Slug == slug {
			return p
		}
	}
	f.t.Fatalf("project %q not found", slug)
	return db.Project{}
}

func (f *fixture) envServices(projectSlug string) map[string]db.Service {
	f.t.Helper()
	p := f.project(projectSlug)
	envs, err := f.q.ListEnvironments(context.Background(), p.ID)
	require.NoError(f.t, err)
	out := map[string]db.Service{}
	for _, e := range envs {
		rows, err := f.q.ListAllEnvironmentServices(context.Background(), e.ID)
		require.NoError(f.t, err)
		for _, r := range rows {
			out[r.Slug] = r
		}
	}
	return out
}

func (f *fixture) vars(id uuid.UUID) map[string]services.RawVar {
	f.t.Helper()
	v, err := services.LoadRawVars(context.Background(), f.q, f.box, id)
	require.NoError(f.t, err)
	return v
}

func (f *fixture) count(query string, args ...any) int {
	f.t.Helper()
	var n int
	require.NoError(f.t, f.pool.QueryRow(context.Background(), query, args...).Scan(&n))
	return n
}

func TestApplyStagesEverything(t *testing.T) {
	f := newFixture(t)
	conn := uuid.New()
	res, err := f.apply(CoolifyChoices{
		Registries:      map[string]CoolifyRegistryAuth{"ghcr.io": {Username: "acme-bot", Password: ghcrCanaryToken}},
		GitConnectionID: &conn,
	})
	require.NoError(t, err)
	require.Len(t, res.Projects, 2)
	require.Len(t, res.Services, 7)
	require.Empty(t, res.Deployments)
	require.Empty(t, f.deployer.calls, "nothing is deployed unless asked")

	// projects and environments
	dash, exp := f.project("dashboard"), f.project("shop")
	require.Equal(t, "Dashboard", dash.Name)
	for _, p := range []db.Project{dash, exp} {
		envs, err := f.q.ListEnvironments(context.Background(), p.ID)
		require.NoError(t, err)
		require.Len(t, envs, 1)
		require.Equal(t, "production", envs[0].Slug)
		require.True(t, envs[0].IsProduction)
	}

	svcs := f.envServices("shop")
	require.Len(t, svcs, 5)
	web, ssr, worker := svcs["shop-web"], svcs["shop-ssr"], svcs["shop-worker"]
	pg, valkey := svcs["shop-postgres"], svcs["shop-valkey"]

	// everything is staged
	for slug, s := range svcs {
		require.Greater(t, s.Generation, s.AppliedGeneration, slug)
		require.Nil(t, s.CurrentDeploymentID, slug)
		require.Equal(t, f.srv.ID, *s.ServerID, slug)
	}
	require.Equal(t, 0, f.count("SELECT count(*) FROM deployments"))

	// managed databases: same engine, flavor and version; the data is not copied
	pgCfg := services.DecodeConfig(pg.Config)
	require.Equal(t, services.KindDatabase, pg.Kind)
	require.Equal(t, "postgres", pgCfg.Database.Engine)
	require.Equal(t, "postgis", pgCfg.Database.Flavor)
	require.Equal(t, "18", pgCfg.Database.Version)
	require.Equal(t, "postgis/postgis:18-3.6", pgCfg.Image)
	require.Equal(t, "shop", pgCfg.Database.Username)
	require.Equal(t, "shop", pgCfg.Database.DatabaseName)
	vcfg := services.DecodeConfig(valkey.Config)
	require.Equal(t, "valkey", vcfg.Database.Engine)
	require.Equal(t, "8", vcfg.Database.Version)
	pgVars := f.vars(pg.ID)
	require.NotEqual(t, pgPassword, pgVars["POSTGRES_PASSWORD"].Value, "a new password is generated")
	require.NotEqual(t, valkeyPassword, f.vars(valkey.ID)["VALKEY_PASSWORD"].Value)

	// image services
	webCfg := services.DecodeConfig(web.Config)
	require.Equal(t, services.SourceImage, web.SourceType)
	require.Equal(t, "ghcr.io/acme/shop:v0.6.0", webCfg.Image)
	require.Equal(t, 8080, webCfg.Port)
	require.Equal(t, "exec", webCfg.Healthcheck.Type)
	require.Contains(t, webCfg.Healthcheck.Command[1], "'http://localhost:8080/up'")
	require.Equal(t, []string{"php", "artisan", "migrate", "--force"}, webCfg.PreDeployCommand)
	require.NotEmpty(t, web.RegistryAuthEnc, "the registry credential is sealed")
	for _, s := range []db.Service{ssr, worker} {
		require.NotEmpty(t, s.RegistryAuthEnc, s.Slug)
	}
	ssrCfg := services.DecodeConfig(ssr.Config)
	require.Equal(t, 13714, ssrCfg.Port)
	require.Equal(t, []string{"docker-php-serversideup-entrypoint", "bun", "/var/www/html/bootstrap/ssr/ssr.js"}, ssrCfg.Docker.Entrypoint)
	require.Contains(t, ssrCfg.Healthcheck.Command[1], "'http://localhost:13714/health'")
	require.Equal(t, services.KindWorker, worker.Kind)
	wCfg := services.DecodeConfig(worker.Config)
	require.Equal(t, 0, wCfg.Port)
	require.Equal(t, "none", wCfg.Healthcheck.Type)
	require.Equal(t, "queue:work", wCfg.Docker.Entrypoint[3])

	// git service
	dsvc := f.envServices("dashboard")
	d := dsvc["dashboard"]
	dCfg := services.DecodeConfig(d.Config)
	require.Equal(t, services.SourceGit, d.SourceType)
	require.Equal(t, "https://github.com/acme/tracker", dCfg.GitURL)
	require.Equal(t, "main", dCfg.GitBranch)
	require.Equal(t, "dockerfile", dCfg.BuildType)
	require.Equal(t, "Dockerfile", dCfg.DockerfilePath)
	require.Empty(t, d.RegistryAuthEnc)
	require.Len(t, f.git.calls, 1, "linked to the chosen git connection")
	require.Equal(t, "acme/tracker", f.git.calls[0].in.Repo)
	require.Equal(t, conn, f.git.calls[0].in.ConnectionID)
	require.Equal(t, d.ID, f.git.calls[0].service)

	// variables: rewritten, marked, scoped
	v := f.vars(web.ID)
	require.Equal(t, "${{shop-postgres.NEBULA_PRIVATE_HOST}}", v["DB_HOST"].Value)
	require.Equal(t, "${{shop-postgres.DATABASE_PASSWORD}}", v["DB_PASSWORD"].Value)
	require.True(t, v["DB_PASSWORD"].Secret)
	require.Equal(t, "${{shop-valkey.REDIS_PASSWORD}}", v["REDIS_PASSWORD"].Value)
	require.Equal(t, "http://${{shop-ssr.NEBULA_PRIVATE_HOST}}:13714", v["INERTIA_SSR_URL"].Value)
	require.Equal(t, appKeyValue, v["APP_KEY"].Value)
	require.True(t, v["APP_KEY"].Secret)
	require.False(t, v["APP_NAME"].Secret)
	require.Equal(t, "price $${{ 5 }} usd", v["LITERAL_BRACES"].Value)
	for _, k := range []string{"PORT", "COOLIFY_FQDN", "SERVICE_FQDN_WEB", "PREVIEW_ONLY", "LOCKED_SECRET"} {
		require.NotContains(t, v, k)
	}
	require.Len(t, f.vars(ssr.ID), 3)
	dv := f.vars(d.ID)
	require.Equal(t, "${{tracker-postgres.DATABASE_URL}}", dv["DATABASE_URL"].Value)
	require.Equal(t, "both", dv["DATABASE_URL"].Scope, "build-time flag kept for git services")

	// the references resolve against the real services
	envInfo, err := services.LoadEnvInfo(context.Background(), f.q, web.EnvironmentID)
	require.NoError(t, err)
	resolver, err := services.LoadResolver(context.Background(), f.q, f.box, envInfo)
	require.NoError(t, err)
	resolved, err := resolver.ResolveService("shop-web")
	require.NoError(t, err)
	require.Equal(t, "shop-postgres", resolved.Vars["DB_HOST"])
	require.Equal(t, pgVars["POSTGRES_PASSWORD"].Value, resolved.Vars["DB_PASSWORD"])
	require.Equal(t, "shop", resolved.Vars["DB_USERNAME"])
	require.Equal(t, "5432", resolved.Vars["DB_PORT"])
	require.Equal(t, "http://shop-ssr:13714", resolved.Vars["INERTIA_SSR_URL"])
	require.Regexp(t, `^postgres://shop:[A-Za-z0-9]{32}@shop-postgres:5432/shop$`, resolved.Vars["DATABASE_URL"])
	require.NotContains(t, resolved.Vars["REDIS_URL"], "${{")

	// volumes, domains, links
	vols, err := f.q.ListServiceVolumes(context.Background(), web.ID)
	require.NoError(t, err)
	var mounts []string
	for _, vol := range vols {
		mounts = append(mounts, vol.Name+":"+vol.MountPath)
	}
	require.Contains(t, mounts, "storage:/var/www/html/storage/app")
	doms, err := f.q.ListServiceDomains(context.Background(), d.ID)
	require.NoError(t, err)
	require.Len(t, doms, 2)
	byHost := map[string]db.Domain{}
	for _, dm := range doms {
		byHost[dm.Hostname] = dm
		require.EqualValues(t, 8080, dm.TargetPort)
	}
	require.JSONEq(t, `{"noindex":true,"compress":true}`, string(byHost["share.example.com"].Options))
	require.JSONEq(t, `{"compress":true}`, string(byHost["home.example.com"].Options))
	webDoms, err := f.q.ListServiceDomains(context.Background(), web.ID)
	require.NoError(t, err)
	require.Len(t, webDoms, 1)
	require.Equal(t, "shop.example.com", webDoms[0].Hostname)
	ssrDoms, err := f.q.ListServiceDomains(context.Background(), ssr.ID)
	require.NoError(t, err)
	require.Empty(t, ssrDoms)
	require.Equal(t, 1, f.count("SELECT count(*) FROM service_links WHERE from_service_id = $1 AND to_service_id = $2", web.ID, pg.ID))
	require.Equal(t, 1, f.count("SELECT count(*) FROM service_links WHERE from_service_id = $1 AND to_service_id = $2", web.ID, ssr.ID))
	require.Equal(t, 1, f.count("SELECT count(*) FROM service_links WHERE from_service_id = $1 AND to_service_id = $2", worker.ID, valkey.ID))

	// the result carries follow-ups with the real slugs, and no secrets
	require.Contains(t, strings.Join(res.Followups, "\n"), "nebulactrl databases restore shop-postgres --file")
	raw, err := json.Marshal(res)
	require.NoError(t, err)
	for _, canary := range []string{fakeToken, pgPassword, valkeyPassword, appKeyValue, ghcrCanaryToken, mailPassword} {
		require.NotContains(t, string(raw), canary)
	}

	// audit: counts only, no values
	var auditText string
	require.NoError(t, f.pool.QueryRow(context.Background(), "SELECT string_agg(action || ' ' || metadata::text, E'\\n') FROM audit_events").Scan(&auditText))
	require.Contains(t, auditText, "coolify.import")
	require.Contains(t, auditText, `"services": 4`)
	require.Contains(t, auditText, `"databases": 3`)
	for _, canary := range []string{fakeToken, pgPassword, pgPassword2, valkeyPassword, appKeyValue, mailPassword, s3Secret, ghcrCanaryToken, "mk-canary-1"} {
		require.NotContains(t, auditText, canary)
	}
}

func TestApplyDeploysOnlyWhenAsked(t *testing.T) {
	f := newFixture(t)
	_, err := f.svc.Apply(f.ctx, CoolifyApplyRequest{Source: f.source(), ServerID: f.srv.ID, Deploy: true})
	require.NoError(t, err)
	require.Len(t, f.deployer.calls, 2, "one DeployStaged per environment")
	total := 0
	for _, c := range f.deployer.calls {
		require.Equal(t, "Imported from Coolify", c.msg)
		total += len(c.ids)
	}
	require.Equal(t, 7, total)
}

func TestDeployNeedsTheDeployScope(t *testing.T) {
	f := newFixture(t)
	ctx := auth.WithPrincipal(context.Background(), &auth.Principal{Type: auth.TypeToken, WorkspaceID: f.ws.ID, Scopes: []string{policy.ScopeProjectsWrite}})
	_, err := f.svc.Apply(ctx, CoolifyApplyRequest{Source: f.source(), ServerID: f.srv.ID, Deploy: true})
	require.Equal(t, 403, statusOf(t, err))
	require.Equal(t, 0, f.count("SELECT count(*) FROM projects"))
}

func statusOf(t *testing.T, err error) int {
	t.Helper()
	e, ok := apierr.As(err)
	require.True(t, ok, "%v", err)
	return e.Status
}

func TestPlanWritesNothing(t *testing.T) {
	f := newFixture(t)
	p, err := f.svc.Plan(f.ctx, CoolifyPlanRequest{Source: f.source(), ServerID: &f.srv.ID})
	require.NoError(t, err)
	require.True(t, p.Installable, "%v", p.Blockers)
	require.Equal(t, 0, f.count("SELECT count(*) FROM projects"))
	require.Equal(t, 0, f.count("SELECT count(*) FROM services"))
	require.Equal(t, 0, f.count("SELECT count(*) FROM audit_events WHERE action LIKE 'coolify%'"))
	raw, _ := json.Marshal(p)
	for _, canary := range []string{fakeToken, pgPassword, valkeyPassword, appKeyValue} {
		require.NotContains(t, string(raw), canary)
	}
}

func TestSecondImportNeverOverwritesTheFirst(t *testing.T) {
	f := newFixture(t)
	_, err := f.apply(CoolifyChoices{})
	require.NoError(t, err)
	before := f.envServices("shop")

	res, err := f.apply(CoolifyChoices{})
	require.NoError(t, err)
	require.Len(t, res.Projects, 2)
	second := f.project("shop-coolify")
	require.Equal(t, "shop (Coolify)", second.Name)
	require.Equal(t, before["shop-web"].ID, f.envServices("shop")["shop-web"].ID)
	require.Len(t, f.envServices("shop-coolify"), 5)
	// hostnames are unique: the second copy leaves them to the first, with a warning
	require.Equal(t, 0, len(mustDomains(t, f, f.envServices("shop-coolify")["shop-web"].ID)))
	require.Contains(t, strings.Join(res.Warnings, "\n"), "already")
}

func mustDomains(t *testing.T, f *fixture, id uuid.UUID) []db.Domain {
	t.Helper()
	d, err := f.q.ListServiceDomains(context.Background(), id)
	require.NoError(t, err)
	return d
}

func TestImportIntoAnExistingProjectKeepsItsServices(t *testing.T) {
	f := newFixture(t)
	bg := context.Background()
	prj, err := f.q.CreateProject(bg, db.CreateProjectParams{WorkspaceID: f.ws.ID, Name: "Prod", Slug: "prod"})
	require.NoError(t, err)
	env, err := f.q.CreateEnvironment(bg, db.CreateEnvironmentParams{ProjectID: prj.ID, Name: "Production", Slug: "production", IsProduction: true})
	require.NoError(t, err)
	_, err = f.q.InsertService(bg, db.InsertServiceParams{ID: uuid.New(), EnvironmentID: env.ID, Name: "shop-web", Slug: "shop-web", Kind: "app", SourceType: "image", Config: []byte(`{"image":"x/y:1","stopGraceSeconds":30,"imageRetention":5,"healthcheck":{"type":"none"}}`)})
	require.NoError(t, err)

	res, err := f.apply(CoolifyChoices{Projects: map[string]CoolifyProjectChoice{uuidProjExp: {ExistingProjectID: &prj.ID}}})
	require.NoError(t, err)
	var exp CoolifyAppliedProject
	for _, p := range res.Projects {
		if p.ID == prj.ID {
			exp = p
		}
	}
	require.False(t, exp.Created)
	require.False(t, exp.Environments[0].Created)
	svcs := f.envServices("prod")
	require.Contains(t, svcs, "shop-web", "the existing service stays")
	require.Contains(t, svcs, "shop-web-2", "the imported one gets the next free slug")
	// references point at the real slugs
	v := f.vars(svcs["shop-web-2"].ID)
	require.Equal(t, "${{shop-postgres.NEBULA_PRIVATE_HOST}}", v["DB_HOST"].Value)
	require.Empty(t, f.vars(svcs["shop-web"].ID), "the existing service is untouched")
}

func TestApplyIsBlockedWithoutReadSensitive(t *testing.T) {
	f := newFixture(t)
	f.coolify.sensitive = false
	_, err := f.apply(CoolifyChoices{})
	e, ok := apierr.As(err)
	require.True(t, ok, "%v", err)
	require.Equal(t, "import_blocked", e.Code)
	require.Contains(t, e.Error(), "read:sensitive")
	require.Equal(t, 0, f.count("SELECT count(*) FROM projects"))
}

func TestLockedVariableValueIsStoredWhenSupplied(t *testing.T) {
	f := newFixture(t)
	v := explicitCanary
	_, err := f.apply(CoolifyChoices{Resources: map[string]CoolifyResourceChoice{uuidWeb: {Env: map[string]CoolifyEnvChoice{"LOCKED_SECRET": {Value: &v}}}}})
	require.NoError(t, err)
	web := f.envServices("shop")["shop-web"]
	got := f.vars(web.ID)["LOCKED_SECRET"]
	require.Equal(t, explicitCanary, got.Value)
	require.True(t, got.Secret)
}

func TestImportNeedsWriteScopeAndAnUnrestrictedPrincipal(t *testing.T) {
	f := newFixture(t)
	read := auth.WithPrincipal(context.Background(), &auth.Principal{Type: auth.TypeToken, WorkspaceID: f.ws.ID, Scopes: []string{policy.ScopeProjectsRead}})
	_, err := f.svc.Plan(read, CoolifyPlanRequest{Source: f.source()})
	require.Equal(t, 403, statusOf(t, err))
	restricted := auth.WithPrincipal(context.Background(), &auth.Principal{Type: auth.TypeToken, WorkspaceID: f.ws.ID, Scopes: policy.AllScopes, ProjectIDs: []uuid.UUID{uuid.New()}})
	_, err = f.svc.Apply(restricted, CoolifyApplyRequest{Source: f.source(), ServerID: f.srv.ID})
	require.Equal(t, 403, statusOf(t, err))
	require.Equal(t, 0, f.count("SELECT count(*) FROM projects"))
}

func TestCoolifyErrorsNeverEchoTheTokenOrTheBody(t *testing.T) {
	f := newFixture(t)
	bad := CoolifySource{URL: f.coolify.url(), Token: "wrong-token-canary-123"}
	_, err := f.svc.Plan(f.ctx, CoolifyPlanRequest{Source: bad})
	e, ok := apierr.As(err)
	require.True(t, ok, "%v", err)
	require.Equal(t, "coolify_unauthorized", e.Code)
	require.NotContains(t, e.Error(), "wrong-token-canary-123")
	require.NotContains(t, e.Error(), "Unauthenticated", "the response body is never repeated")

	_, err = f.svc.Plan(f.ctx, CoolifyPlanRequest{Source: CoolifySource{URL: "http://127.0.0.1:1", Token: fakeToken}})
	e, ok = apierr.As(err)
	require.True(t, ok, "%v", err)
	require.Equal(t, "coolify_unreachable", e.Code)
	require.NotContains(t, e.Error(), fakeToken)

	for _, url := range []string{"", "ftp://x", "https://user:pw@host", "http://169.254.169.254"} {
		_, err = f.svc.Plan(f.ctx, CoolifyPlanRequest{Source: CoolifySource{URL: url, Token: fakeToken}})
		require.Error(t, err, url)
		require.NotContains(t, err.Error(), fakeToken)
	}
}

func TestPlanDoesNotFollowRedirects(t *testing.T) {
	f := newFixture(t)
	other := newFakeCoolify(t)
	redirect := newRedirector(t, other.url())
	_, err := f.svc.Plan(f.ctx, CoolifyPlanRequest{Source: CoolifySource{URL: redirect, Token: fakeToken}})
	require.Error(t, err)
	require.Empty(t, other.requests, "the token must not reach another host")
}
