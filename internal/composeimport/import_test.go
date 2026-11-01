package composeimport_test

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
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/composeimport"
	"github.com/nebulactrl/nebulactrl/internal/databases"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/domains"
	"github.com/nebulactrl/nebulactrl/internal/events"
	"github.com/nebulactrl/nebulactrl/internal/jobs"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/internal/templates"
	"github.com/nebulactrl/nebulactrl/internal/testutil"
	"github.com/nebulactrl/nebulactrl/internal/variables"
)

type importFixture struct {
	t    *testing.T
	pool *pgxpool.Pool
	q    *db.Queries
	ctx  context.Context
	box  *secretbox.Box
	svc  *composeimport.Service
	env  db.Environment
	srv  db.Server
}

func newImportFixture(t *testing.T) *importFixture {
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
	env, err := q.CreateEnvironment(bg, db.CreateEnvironmentParams{ProjectID: prj.ID, Name: "production", Slug: "production", IsProduction: true})
	require.NoError(t, err)
	srv, err := q.CreateServer(bg, db.CreateServerParams{WorkspaceID: ws.ID, Name: "agent1"})
	require.NoError(t, err)
	_, err = pool.Exec(bg, "UPDATE servers SET status = 'online' WHERE id = $1", srv.ID)
	require.NoError(t, err)

	box, err := secretbox.New(bytes.Repeat([]byte{7}, 32))
	require.NoError(t, err)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	bus := events.NewBus()
	svcs := services.NewService(pool, bus, box, nil, logger)
	vars := variables.NewService(pool, bus, box)
	jm := jobs.New(pool, logger)
	domains.RegisterJobs(jm, domains.NewService(pool, bus, jm, box), logger)
	ctx := auth.WithPrincipal(bg, &auth.Principal{Type: auth.TypeUser, UserID: u.ID, WorkspaceID: ws.ID, Label: "a@example.com", Scopes: policy.AllScopes})
	return &importFixture{
		t: t, pool: pool, q: q, ctx: ctx, box: box, env: env, srv: srv,
		svc: composeimport.NewService(composeimport.Deps{
			Pool: pool, Bus: bus, Jobs: jm, Services: svcs, Variables: templates.VariablesAdapter{S: vars},
			Databases: databases.NewService(pool, bus, box, nil, svcs, logger), Logger: logger,
		}),
	}
}

func (f *importFixture) services() map[string]db.Service {
	f.t.Helper()
	rows, err := f.q.ListAllEnvironmentServices(context.Background(), f.env.ID)
	require.NoError(f.t, err)
	out := map[string]db.Service{}
	for _, r := range rows {
		out[r.Slug] = r
	}
	return out
}

func (f *importFixture) vars(id uuid.UUID) map[string]services.RawVar {
	f.t.Helper()
	v, err := services.LoadRawVars(context.Background(), f.q, f.box, id)
	require.NoError(f.t, err)
	return v
}

func apiCode(t *testing.T, err error) string {
	t.Helper()
	e, ok := apierr.As(err)
	require.True(t, ok, "%v", err)
	return e.Code
}

func readFixture(t *testing.T, name string) composeimport.ComposeSource {
	t.Helper()
	return composeimport.LoadSourceForTest(t, name)
}

func TestImportStagesAComposeProject(t *testing.T) {
	f := newImportFixture(t)
	src := readFixture(t, "kv")

	plan, err := f.svc.Plan(f.ctx, f.env.ID, composeimport.ComposePlanRequest{Source: src, ServerID: &f.srv.ID})
	require.NoError(t, err)
	require.True(t, plan.Installable, "%v", plan.Blockers)
	require.Len(t, plan.Services, 2)

	res, err := f.svc.Import(f.ctx, f.env.ID, composeimport.ComposeImportRequest{Source: src, ServerID: f.srv.ID})
	require.NoError(t, err)
	require.Len(t, res.Services, 2)
	require.NotEmpty(t, res.GroupID)

	got := f.services()
	require.Len(t, got, 2)
	app, kv := got["app"], got["valkey"]
	require.Equal(t, services.KindApp, app.Kind)
	require.Equal(t, services.KindDatabase, kv.Kind)
	require.Equal(t, f.srv.ID, *app.ServerID)
	require.Greater(t, app.Generation, app.AppliedGeneration, "everything is staged, nothing deployed")

	cfg := services.DecodeConfig(app.Config)
	require.Equal(t, "nebulactrl-test-app:dev", cfg.Image)
	require.Equal(t, 8080, cfg.Port)
	require.Equal(t, "tcp", cfg.Healthcheck.Type)
	require.NotNil(t, services.DecodeConfig(kv.Config).Database)
	require.Equal(t, "valkey", services.DecodeConfig(kv.Config).Database.Engine)

	// The Compose URL points at the managed database, password and all.
	raw := f.vars(app.ID)
	require.Equal(t, "redis://:${{valkey.REDIS_PASSWORD}}@${{valkey.NEBULA_PRIVATE_HOST}}:6379/0", raw["KV_URL"].Value)
	require.True(t, raw["KV_URL"].Secret)
	require.Equal(t, "compose-v1", raw["VERSION"].Value)
	envInfo, err := services.LoadEnvInfo(context.Background(), f.q, f.env.ID)
	require.NoError(t, err)
	resolver, err := services.LoadResolver(context.Background(), f.q, f.box, envInfo)
	require.NoError(t, err)
	resolved, err := resolver.ResolveService("app")
	require.NoError(t, err)
	require.Regexp(t, `^redis://:[A-Za-z0-9]{32}@[^:]+:6379/0$`, resolved.Vars["KV_URL"])
	require.NotContains(t, resolved.Vars["KV_URL"], "${{")

	// Deploy order: the app waits for valkey.
	var links int
	require.NoError(t, f.pool.QueryRow(context.Background(), "SELECT count(*) FROM service_links WHERE from_service_id = $1 AND to_service_id = $2", app.ID, kv.ID).Scan(&links))
	require.Equal(t, 1, links)

	// A canvas group holds both services.
	var layout []byte
	require.NoError(t, f.pool.QueryRow(context.Background(), "SELECT layout FROM canvas_layouts WHERE environment_id = $1", f.env.ID).Scan(&layout))
	var l struct {
		Groups []struct {
			ID         string   `json:"id"`
			Name       string   `json:"name"`
			ServiceIDs []string `json:"serviceIds"`
		} `json:"groups"`
	}
	require.NoError(t, json.Unmarshal(layout, &l))
	require.Len(t, l.Groups, 1)
	require.Equal(t, res.GroupID, l.Groups[0].ID)
	require.Equal(t, "Compose project", l.Groups[0].Name)
	require.ElementsMatch(t, []string{app.ID.String(), kv.ID.String()}, l.Groups[0].ServiceIDs)

	var audits int
	require.NoError(t, f.pool.QueryRow(context.Background(), "SELECT count(*) FROM audit_events WHERE action = 'compose.import'").Scan(&audits))
	require.Equal(t, 1, audits)
}

func TestImportKeepsSecretsOutOfThePlanAndEncrypted(t *testing.T) {
	f := newImportFixture(t)
	src := readFixture(t, "n8n")

	plan, err := f.svc.Plan(f.ctx, f.env.ID, composeimport.ComposePlanRequest{Source: src, ServerID: &f.srv.ID})
	require.NoError(t, err)
	body, err := json.Marshal(plan)
	require.NoError(t, err)
	for _, secret := range []string{"changePassword", "0123456789abcdef0123456789abcdef"} {
		require.NotContains(t, string(body), secret, "the plan never carries secret values")
	}

	_, err = f.svc.Import(f.ctx, f.env.ID, composeimport.ComposeImportRequest{Source: src, ServerID: f.srv.ID})
	require.NoError(t, err)
	got := f.services()
	n8n, pg := got["n8n"], got["postgres"]

	vars := f.vars(n8n.ID)
	require.True(t, vars["DB_POSTGRESDB_PASSWORD"].Secret)
	require.Equal(t, "changePassword", vars["DB_POSTGRESDB_PASSWORD"].Value)
	require.True(t, vars["N8N_ENCRYPTION_KEY"].Secret)
	require.False(t, vars["GENERIC_TIMEZONE"].Secret)
	require.Equal(t, "${{postgres.NEBULA_PRIVATE_HOST}}", vars["DB_POSTGRESDB_HOST"].Value)
	var plain int
	require.NoError(t, f.pool.QueryRow(context.Background(), "SELECT count(*) FROM service_variables WHERE service_id = $1 AND encode(value_enc, 'escape') LIKE '%changePassword%'", n8n.ID).Scan(&plain))
	require.Zero(t, plain, "stored encrypted")

	// Volumes and the init script file.
	vols, err := f.q.ListServiceVolumes(context.Background(), pg.ID)
	require.NoError(t, err)
	require.Len(t, vols, 1)
	require.Equal(t, "/var/lib/postgresql/data", vols[0].MountPath)
	cfg := services.DecodeConfig(pg.Config)
	require.Len(t, cfg.Files, 1)
	require.Equal(t, "0755", cfg.Files[0].Mode)
	require.Contains(t, cfg.Files[0].Content, "CREATE USER")
	require.Equal(t, "exec", cfg.Healthcheck.Type)
}

func TestImportRefusesABlockedPlan(t *testing.T) {
	f := newImportFixture(t)
	src := composeimport.ComposeSource{Compose: "services:\n  a:\n    build: ./a\n"}
	plan, err := f.svc.Plan(f.ctx, f.env.ID, composeimport.ComposePlanRequest{Source: src, ServerID: &f.srv.ID})
	require.NoError(t, err)
	require.False(t, plan.Installable)
	_, err = f.svc.Import(f.ctx, f.env.ID, composeimport.ComposeImportRequest{Source: src, ServerID: f.srv.ID})
	require.Equal(t, "import_blocked", apiCode(t, err))
	require.Empty(t, f.services())

	_, err = f.svc.Plan(f.ctx, f.env.ID, composeimport.ComposePlanRequest{Source: composeimport.ComposeSource{Compose: "services: ["}})
	require.Equal(t, "invalid_compose", apiCode(t, err))
	_, err = f.svc.Plan(f.ctx, f.env.ID, composeimport.ComposePlanRequest{})
	require.Equal(t, "validation_failed", apiCode(t, err))
}

func TestImportRespectsScopesAndWorkspace(t *testing.T) {
	f := newImportFixture(t)
	src := readFixture(t, "kv")
	readOnly := auth.WithPrincipal(context.Background(), &auth.Principal{Type: auth.TypeToken, WorkspaceID: policyWS(f), Scopes: []string{policy.ScopeProjectsRead}, Label: "ro", TokenID: new(uuid.New())})
	_, err := f.svc.Import(readOnly, f.env.ID, composeimport.ComposeImportRequest{Source: src, ServerID: f.srv.ID})
	require.Error(t, err)
	require.Empty(t, f.services())
}

func policyWS(f *importFixture) uuid.UUID { return policy.WorkspaceID(f.ctx) }

func TestImportPlausibleKeepsOptionsFilesAndManagedDatabase(t *testing.T) {
	f := newImportFixture(t)
	src := readFixture(t, "plausible")
	res, err := f.svc.Import(f.ctx, f.env.ID, composeimport.ComposeImportRequest{Source: src, ServerID: f.srv.ID})
	require.NoError(t, err)
	require.Len(t, res.Services, 4)
	got := f.services()

	ch := services.DecodeConfig(got["plausible-events-db"].Config)
	require.NotNil(t, ch.Docker)
	require.Equal(t, "nofile", ch.Docker.Ulimits[0].Name)
	require.Len(t, ch.Files, 2)

	db := services.DecodeConfig(got["plausible-db"].Config)
	require.NotNil(t, db.Database)
	require.Equal(t, "postgres", db.Database.Username)

	// The app's DATABASE_URL references the managed database's generated password; the old one is gone.
	raw := f.vars(got["plausible"].ID)
	require.Equal(t, "postgres://postgres:${{plausible-db.DATABASE_PASSWORD}}@${{plausible-db.NEBULA_PRIVATE_HOST}}:5432/plausible_db", raw["DATABASE_URL"].Value)
	require.True(t, raw["DATABASE_URL"].Secret)
	require.Equal(t, "https://plausible.example.com", raw["BASE_URL"].Value)
	require.NotContains(t, raw, "POSTGRES_PASSWORD")
	require.NotEqual(t, "postgres", f.vars(got["plausible-db"].ID)["POSTGRES_PASSWORD"].Value, "the managed database generated its own password")
}

const dockerOptionsCompose = `services:
  app:
    image: busybox
    user: '1000:1000'
    cap_drop: [ALL]
    read_only: true
    devices: ['/dev/dri:/dev/dri']
    privileged: true
`

// The options every server accepts are imported; the ones that need an
// unrestricted server are reported on a restricted target and imported on an
// unrestricted one.
func TestImportDockerOptionsFollowTheServerPolicy(t *testing.T) {
	for _, unrestricted := range []bool{false, true} {
		f := newImportFixture(t)
		if unrestricted {
			f.setServer(t, "unrestricted = true")
		}
		src := composeimport.ComposeSource{Compose: dockerOptionsCompose}
		plan, err := f.svc.Plan(f.ctx, f.env.ID, composeimport.ComposePlanRequest{Source: src, ServerID: &f.srv.ID})
		require.NoError(t, err)
		var needs []string
		for _, n := range plan.Services[0].Notes {
			if strings.Contains(n.Message, "Needs an unrestricted server") {
				needs = append(needs, n.Key)
			}
		}
		if unrestricted {
			require.Empty(t, needs)
		} else {
			require.ElementsMatch(t, []string{"app.devices", "app.privileged"}, needs)
		}

		_, err = f.svc.Import(f.ctx, f.env.ID, composeimport.ComposeImportRequest{Source: src, ServerID: f.srv.ID})
		require.NoError(t, err)
		cfg := services.DecodeConfig(f.services()["app"].Config)
		require.NotNil(t, cfg.Docker)
		require.Equal(t, "1000:1000", cfg.Docker.User)
		require.Equal(t, []string{"ALL"}, cfg.Docker.CapDrop)
		require.True(t, cfg.Docker.ReadOnlyRootfs)
		require.Equal(t, unrestricted, cfg.Docker.Privileged)
		require.Equal(t, unrestricted, len(cfg.Docker.Devices) == 1)
		require.Equal(t, unrestricted, cfg.Docker.NeedsUnrestricted())
	}
}
