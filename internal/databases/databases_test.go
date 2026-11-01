package databases_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agents"
	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/databases"
	"github.com/nebulactrl/nebulactrl/internal/databases/engines"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/events"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/internal/testutil"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// rowDispatcher stores commands as real agent_commands rows, like the hub does:
// a command enqueued under an existing idempotency key is that command.
type rowDispatcher struct{ cmds []agents.NewCommand }

func (d *rowDispatcher) Enqueue(ctx context.Context, tx pgx.Tx, c agents.NewCommand) (uuid.UUID, error) {
	d.cmds = append(d.cmds, c)
	key := c.IdempotencyKey
	if key == "" {
		key = uuid.NewString()
	}
	row, err := db.New(tx).InsertAgentCommand(ctx, db.InsertAgentCommandParams{
		ID: uuid.New(), ServerID: c.ServerID, DeploymentID: c.DeploymentID, Kind: c.Kind, Payload: c.Payload,
		IdempotencyKey: key, ExpiresAt: time.Now().Add(time.Hour),
	})
	return row.ID, err
}

func (d *rowDispatcher) SubscribeLogs(context.Context, uuid.UUID, agentprotocol.LogsSubscribe) (<-chan agentprotocol.LogFrame, func(), error) {
	return nil, nil, io.EOF
}

type fixture struct {
	t       *testing.T
	pool    *pgxpool.Pool
	q       *db.Queries
	ctx     context.Context
	box     *secretbox.Box
	disp    *rowDispatcher
	svcs    *services.Service
	dbs     *databases.Service
	env     db.Environment
	project db.Project
	server  db.Server
	ws      db.Workspace
	userID  uuid.UUID
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
	env, err := q.CreateEnvironment(bg, db.CreateEnvironmentParams{ProjectID: prj.ID, Name: "production", Slug: "production", IsProduction: true})
	require.NoError(t, err)
	srv, err := q.CreateServer(bg, db.CreateServerParams{WorkspaceID: ws.ID, Name: "agent1"})
	require.NoError(t, err)
	_, err = pool.Exec(bg, "UPDATE servers SET status = 'online' WHERE id = $1", srv.ID)
	require.NoError(t, err)

	box, err := secretbox.New(bytes.Repeat([]byte{7}, 32))
	require.NoError(t, err)
	disp := &rowDispatcher{}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	bus := events.NewBus()
	svcs := services.NewService(pool, bus, box, disp, logger)
	ctx := auth.WithPrincipal(bg, &auth.Principal{Type: auth.TypeUser, UserID: u.ID, WorkspaceID: ws.ID, Label: "alice@example.com", Scopes: policy.AllScopes})
	return &fixture{
		t: t, pool: pool, q: q, ctx: ctx, box: box, disp: disp, svcs: svcs, env: env, project: prj, server: srv, ws: ws, userID: u.ID,
		dbs: databases.NewService(pool, bus, box, disp, svcs, logger),
	}
}

func (f *fixture) principal(scopes []string, projects []uuid.UUID) context.Context {
	return auth.WithPrincipal(context.Background(), &auth.Principal{
		Type: auth.TypeToken, UserID: f.userID, TokenID: new(uuid.New()), WorkspaceID: f.ws.ID, Label: "ci", Scopes: scopes, ProjectIDs: projects,
	})
}

func (f *fixture) createDB(name, engine, version string) services.ServiceView {
	f.t.Helper()
	v, err := f.dbs.Create(f.ctx, databases.CreateInput{
		EnvironmentID: f.env.ID, Name: name, Engine: engine, Version: version, ServerID: &f.server.ID, AcceptLicense: true,
	})
	require.NoError(f.t, err)
	return v
}

// deploy gives a service a succeeded deployment on the fixture server.
func (f *fixture) deploy(id uuid.UUID) {
	f.t.Helper()
	bg := context.Background()
	sc, err := services.LoadScopeInternal(bg, f.q, id)
	require.NoError(f.t, err)
	cfg := sc.Config()
	snap, _ := json.Marshal(services.Snapshot{Config: cfg})
	src, _ := json.Marshal(services.ReleaseSource{Type: services.SourceImage, Image: cfg.Image})
	rel, err := f.q.InsertRelease(bg, db.InsertReleaseParams{ID: uuid.New(), ServiceID: id, Source: src, Config: snap, ImageRef: cfg.Image})
	require.NoError(f.t, err)
	dep, err := f.q.InsertDeployment(bg, db.InsertDeploymentParams{
		ServiceID: id, ReleaseID: &rel.ID, ServerID: &f.server.ID, Trigger: "manual", ActorType: "user", Strategy: "recreate",
	})
	require.NoError(f.t, err)
	_, err = f.pool.Exec(bg, "UPDATE deployments SET status = 'succeeded' WHERE id = $1", dep.ID)
	require.NoError(f.t, err)
	require.NoError(f.t, f.q.MarkServiceDeployed(bg, db.MarkServiceDeployedParams{ID: id, CurrentDeploymentID: &dep.ID, AppliedGeneration: sc.Service.Generation}))
}

func (f *fixture) reload(id uuid.UUID) db.Service {
	f.t.Helper()
	sc, err := services.LoadScopeInternal(context.Background(), f.q, id)
	require.NoError(f.t, err)
	return sc.Service
}

func (f *fixture) setVar(serviceID uuid.UUID, key, value string) {
	f.t.Helper()
	sc, err := services.LoadScopeInternal(context.Background(), f.q, serviceID)
	require.NoError(f.t, err)
	_, err = services.WriteServiceVariable(context.Background(), f.q, f.box, sc, services.VarWrite{Key: key, Value: value})
	require.NoError(f.t, err)
}

func (f *fixture) getVar(serviceID uuid.UUID, key string) string {
	f.t.Helper()
	raw, err := services.LoadRawVars(context.Background(), f.q, f.box, serviceID)
	require.NoError(f.t, err)
	return raw[key].Value
}

func (f *fixture) resolve(slug string) services.Resolved {
	f.t.Helper()
	env, err := services.LoadEnvInfo(context.Background(), f.q, f.env.ID)
	require.NoError(f.t, err)
	res, err := services.LoadResolver(context.Background(), f.q, f.box, env)
	require.NoError(f.t, err)
	out, err := res.ResolveService(slug)
	require.NoError(f.t, err)
	return out
}

func (f *fixture) app(slug string) db.Service {
	f.t.Helper()
	cfg := services.Config{Image: "traefik/whoami:latest", Port: 80}.WithDefaults()
	raw, _ := json.Marshal(cfg)
	sv, err := f.q.InsertService(context.Background(), db.InsertServiceParams{
		ID: uuid.New(), EnvironmentID: f.env.ID, Name: slug, Slug: slug, Kind: services.KindApp, SourceType: services.SourceImage, Config: raw, ServerID: &f.server.ID,
	})
	require.NoError(f.t, err)
	return sv
}

func (f *fixture) lastCommand(kind string) (db.AgentCommand, bool) {
	f.t.Helper()
	var cmd db.AgentCommand
	rows, err := f.pool.Query(context.Background(), "SELECT id FROM agent_commands WHERE kind = $1 ORDER BY created_at DESC LIMIT 1", kind)
	require.NoError(f.t, err)
	defer rows.Close()
	if !rows.Next() {
		return cmd, false
	}
	var id uuid.UUID
	require.NoError(f.t, rows.Scan(&id))
	rows.Close()
	cmd, err = f.q.GetAgentCommand(context.Background(), id)
	require.NoError(f.t, err)
	return cmd, true
}

func (f *fixture) result(cmd db.AgentCommand, outcome, errMsg string, data any) {
	f.t.Helper()
	raw, _ := json.Marshal(data)
	require.NoError(f.t, f.dbs.HandleResult(context.Background(), cmd.ID, agentprotocol.ResultFrame{
		CommandID: cmd.ID.String(), Outcome: outcome, Data: raw, Error: errMsg,
	}))
}

func apiCode(t *testing.T, err error) string {
	t.Helper()
	require.Error(t, err)
	e, ok := apierr.As(err)
	require.True(t, ok, "not an apierr: %v", err)
	return e.Code
}

func TestCreateBuildsStagedDatabaseFromCatalog(t *testing.T) {
	f := newFixture(t)
	v := f.createDB("Main DB", "postgres", "17")
	require.Equal(t, services.KindDatabase, v.Kind)
	require.Equal(t, "postgres:17", v.Config.Image)
	require.Equal(t, 5432, v.Config.Port)
	require.Equal(t, services.HealthTCP, v.Config.Healthcheck.Type)
	require.NotNil(t, v.Config.Database)
	require.Equal(t, "17", v.Config.Database.Version)
	require.True(t, v.HasStagedChanges, "creating stages, never deploys")

	vols, err := f.q.ListServiceVolumes(context.Background(), v.ID)
	require.NoError(t, err)
	require.Len(t, vols, 1)
	require.Equal(t, "/var/lib/postgresql/data", vols[0].MountPath)

	raw, err := services.LoadRawVars(context.Background(), f.q, f.box, v.ID)
	require.NoError(t, err)
	require.Len(t, raw["POSTGRES_PASSWORD"].Value, 32)
	require.True(t, raw["POSTGRES_PASSWORD"].Secret)
	require.Equal(t, "postgres", raw["POSTGRES_USER"].Value)
	require.Equal(t, "app", raw["POSTGRES_DB"].Value)
	require.False(t, raw["POSTGRES_USER"].Secret)

	// Postgres 18 mounts one level higher.
	v18 := f.createDB("Newer DB", "postgres", "18")
	vols, err = f.q.ListServiceVolumes(context.Background(), v18.ID)
	require.NoError(t, err)
	require.Equal(t, "/var/lib/postgresql", vols[0].MountPath)

	// Valkey is the default key-value engine; its start command expands the password.
	kv := f.createDB("Cache", "valkey", "")
	require.Equal(t, "valkey/valkey:9", kv.Config.Image)
	require.Contains(t, strings.Join(kv.Config.StartCommand, " "), `--requirepass "$VALKEY_PASSWORD"`)
	require.Equal(t, 6379, kv.Config.Port)

	// The create is audited.
	var n int
	require.NoError(t, f.pool.QueryRow(context.Background(), "SELECT count(*) FROM audit_events WHERE action = 'database.create'").Scan(&n))
	require.Equal(t, 3, n)
}

func TestCreateValidatesAgainstTheCatalog(t *testing.T) {
	f := newFixture(t)
	create := func(in databases.CreateInput) error {
		in.EnvironmentID = f.env.ID
		_, err := f.dbs.Create(f.ctx, in)
		return err
	}
	require.Equal(t, "invalid_database", apiCode(t, create(databases.CreateInput{Engine: "oracle"})))
	require.Equal(t, "invalid_database", apiCode(t, create(databases.CreateInput{Engine: "postgres", Version: "9.6"})))
	require.Equal(t, "invalid_database", apiCode(t, create(databases.CreateInput{Engine: "mysql", Version: "8.4", Flavor: "postgis"})))
	require.Equal(t, "invalid_database", apiCode(t, create(databases.CreateInput{Engine: "postgres", DatabaseName: "x; drop table y"})))
	// Source-available engines need an acknowledgement of the licence.
	require.Equal(t, "license_not_accepted", apiCode(t, create(databases.CreateInput{Engine: "mongodb"})))
	require.Equal(t, "license_not_accepted", apiCode(t, create(databases.CreateInput{Engine: "redis"})))
	require.NoError(t, create(databases.CreateInput{Engine: "mongodb", AcceptLicense: true}))
	require.NoError(t, create(databases.CreateInput{Engine: "postgres", Version: "17", Flavor: "postgis", Name: "geo"}))
	// No write scope, no database.
	ro := f.principal([]string{policy.ScopeProjectsRead}, nil)
	_, err := f.dbs.Create(ro, databases.CreateInput{EnvironmentID: f.env.ID, Engine: "valkey"})
	require.Equal(t, "forbidden", apiCode(t, err))
}

func TestCreateTxLeavesAuditAndCommitToTheCaller(t *testing.T) {
	f := newFixture(t)
	var row db.Service
	err := db.InTx(f.ctx, f.pool, func(tx pgx.Tx) error {
		var err error
		row, err = f.dbs.CreateForTemplate(f.ctx, tx, databases.CreateInput{EnvironmentID: f.env.ID, Name: "tpl-db", Engine: "postgres", Version: "17", DatabaseName: "umami"})
		return err
	})
	require.NoError(t, err)
	require.Equal(t, "tpl-db", row.Slug)
	var n int
	require.NoError(t, f.pool.QueryRow(context.Background(), "SELECT count(*) FROM audit_events WHERE action = 'database.create'").Scan(&n))
	require.Zero(t, n, "the caller owns the audit entry")
	require.Equal(t, "umami", f.getVar(row.ID, "POSTGRES_DB"))

	// A rolled-back transaction leaves nothing behind.
	sentinel := io.ErrUnexpectedEOF
	err = db.InTx(f.ctx, f.pool, func(tx pgx.Tx) error {
		if _, err := f.dbs.CreateTx(f.ctx, tx, databases.CreateInput{EnvironmentID: f.env.ID, Name: "gone", Engine: "valkey"}); err != nil {
			return err
		}
		return sentinel
	})
	require.ErrorIs(t, err, sentinel)
	all, err := f.q.ListAllEnvironmentServices(context.Background(), f.env.ID)
	require.NoError(t, err)
	require.Len(t, all, 1)
}

func TestDerivedExportsResolveForConsumers(t *testing.T) {
	f := newFixture(t)
	pg := f.createDB("pg", "postgres", "17")
	kv := f.createDB("cache", "valkey", "9")
	api := f.app("api")
	f.setVar(api.ID, "DATABASE_URL", "${{pg.DATABASE_URL}}")
	f.setVar(api.ID, "REDIS_URL", "${{cache.REDIS_URL}}")
	f.setVar(api.ID, "DB_HOST", "${{pg.DATABASE_HOST}}")

	pw := f.getVar(pg.ID, "POSTGRES_PASSWORD")
	kpw := f.getVar(kv.ID, "VALKEY_PASSWORD")
	res := f.resolve("api")
	require.Equal(t, "postgres://postgres:"+pw+"@pg:5432/app", res.Vars["DATABASE_URL"])
	require.Equal(t, "redis://:"+kpw+"@cache:6379", res.Vars["REDIS_URL"])
	require.Equal(t, "pg", res.Vars["DB_HOST"])
	require.True(t, res.Secret["DATABASE_URL"], "an export containing the password is a derived secret")
	require.True(t, res.Secret["REDIS_URL"])
	require.False(t, res.Secret["DB_HOST"])

	// Exports are virtual: nothing was stored on the database, no staging by reading.
	raw, err := services.LoadRawVars(context.Background(), f.q, f.box, pg.ID)
	require.NoError(t, err)
	require.NotContains(t, raw, "DATABASE_URL")

	// A reference to an export the engine does not have fails.
	f.setVar(api.ID, "BAD", "${{cache.DATABASE_URL}}")
	env, _ := services.LoadEnvInfo(context.Background(), f.q, f.env.ID)
	r, err := services.LoadResolver(context.Background(), f.q, f.box, env)
	require.NoError(t, err)
	_, err = r.ResolveService("api")
	require.Error(t, err)
}

func TestLinkProposalIsOneExportReference(t *testing.T) {
	f := newFixture(t)
	pg := f.createDB("pg", "postgres", "17")
	props := services.ProposeLinkVariables(pg.Slug, pg.Kind, pg.Config)
	byKey := map[string]string{}
	for _, p := range props {
		byKey[p.Key] = p.Value
	}
	require.Equal(t, "${{pg.DATABASE_URL}}", byKey["DATABASE_URL"])
	require.Equal(t, "${{pg.NEBULA_PRIVATE_HOST}}", byKey["DATABASE_HOST"])
	kv := f.createDB("cache", "valkey", "")
	byKey = map[string]string{}
	for _, p := range services.ProposeLinkVariables(kv.Slug, kv.Kind, kv.Config) {
		byKey[p.Key] = p.Value
	}
	require.Equal(t, "${{cache.REDIS_URL}}", byKey["REDIS_URL"])
}

func TestRevealIsScopedAndAudited(t *testing.T) {
	f := newFixture(t)
	pg := f.createDB("pg", "postgres", "17")
	pw := f.getVar(pg.ID, "POSTGRES_PASSWORD")

	r, err := f.dbs.Reveal(f.ctx, pg.ID, "DATABASE_URL")
	require.NoError(t, err)
	require.Equal(t, "postgres://postgres:"+pw+"@pg:5432/app", r.Value)
	require.WithinDuration(t, r.RevealedAt.Add(databases.RevealTTL), r.ExpiresAt, time.Second)

	// Audited as secret.reveal; the audit row names the key, never the value.
	var meta []byte
	var actor string
	require.NoError(t, f.pool.QueryRow(context.Background(),
		"SELECT metadata, actor_label FROM audit_events WHERE action = 'secret.reveal'").Scan(&meta, &actor))
	require.Contains(t, string(meta), "DATABASE_URL")
	require.NotContains(t, string(meta), pw)
	require.Equal(t, "alice@example.com", actor)

	// Scope checks: read-only tokens are refused (and nothing is audited).
	ro := f.principal([]string{policy.ScopeProjectsRead, policy.ScopeServersRead}, nil)
	_, err = f.dbs.Reveal(ro, pg.ID, "DATABASE_URL")
	require.Equal(t, "forbidden", apiCode(t, err))
	// deployments:write is enough; so is admin.
	dw := f.principal([]string{policy.ScopeDeploymentsWrite}, nil)
	_, err = f.dbs.Reveal(dw, pg.ID, "DATABASE_URL")
	require.NoError(t, err)
	adm := f.principal([]string{policy.ScopeAdmin}, nil)
	_, err = f.dbs.Reveal(adm, pg.ID, "DATABASE_URL")
	require.NoError(t, err)
	// A token restricted to another project cannot see the service at all.
	other := f.principal([]string{policy.ScopeDeploymentsWrite}, []uuid.UUID{uuid.New()})
	_, err = f.dbs.Reveal(other, pg.ID, "DATABASE_URL")
	require.Equal(t, "not_found", apiCode(t, err))
	// Unknown export.
	_, err = f.dbs.Reveal(f.ctx, pg.ID, "POSTGRES_PASSWORD")
	require.Equal(t, "not_found", apiCode(t, err))

	var n int
	require.NoError(t, f.pool.QueryRow(context.Background(), "SELECT count(*) FROM audit_events WHERE action = 'secret.reveal'").Scan(&n))
	require.Equal(t, 3, n)

	// The masked reads keep hiding it: the variable list has no value for a secret.
	view, err := f.dbs.Get(f.ctx, pg.ID)
	require.NoError(t, err)
	raw, _ := json.Marshal(view)
	require.NotContains(t, string(raw), pw)
}

func TestRotateExecChangesPasswordThroughAgentAndStagesReferrers(t *testing.T) {
	f := newFixture(t)
	pg := f.createDB("pg", "postgres", "17")
	f.deploy(pg.ID)
	api := f.app("api")
	f.setVar(api.ID, "DATABASE_URL", "${{pg.DATABASE_URL}}")
	worker := f.app("worker") // unrelated: never staged
	oldPW := f.getVar(pg.ID, "POSTGRES_PASSWORD")
	f.deploy(api.ID)
	f.deploy(worker.ID)
	dbGen := f.reload(pg.ID).Generation

	// A dry run changes nothing and names the consumers.
	plan, err := f.dbs.RotateCredentials(f.ctx, pg.ID, true)
	require.NoError(t, err)
	require.Equal(t, "planned", plan.Status)
	require.Equal(t, databases.ModeExec, plan.Plan.Mode)
	require.Len(t, plan.Plan.Consumers, 1)
	require.Equal(t, "api", plan.Plan.Consumers[0].Slug)
	require.Equal(t, oldPW, f.getVar(pg.ID, "POSTGRES_PASSWORD"))
	require.Empty(t, f.disp.cmds)

	res, err := f.dbs.RotateCredentials(f.ctx, pg.ID, false)
	require.NoError(t, err)
	require.Equal(t, "pending", res.Status)
	require.Len(t, f.disp.cmds, 1)
	cmd, ok := f.lastCommand(agentprotocol.KindDBAdmin)
	require.True(t, ok)
	require.Equal(t, f.server.ID, cmd.ServerID)

	// The persisted payload names the action, never a password.
	var p agentprotocol.DBAdminPayload
	require.NoError(t, json.Unmarshal(cmd.Payload, &p))
	require.Equal(t, agentprotocol.DBActionRotatePassword, p.Action)
	require.Equal(t, "postgres", p.Engine)
	require.NotContains(t, string(cmd.Payload), oldPW)
	// Nothing changes until the agent reports success.
	require.Equal(t, oldPW, f.getVar(pg.ID, "POSTGRES_PASSWORD"))
	require.Equal(t, int64(0), f.reload(api.ID).Generation-f.reload(api.ID).AppliedGeneration)

	// At send time the secrets carry the current and the new password.
	sec, err := f.dbs.SecretsForCommand(f.ctx, cmd.ID, cmd.Kind, cmd.Payload)
	require.NoError(t, err)
	require.Equal(t, oldPW, sec[agentprotocol.SecretDBAdminPassword])
	newPW := sec[agentprotocol.SecretDBAdminNewPassword]
	require.Len(t, newPW, 32)
	require.NotEqual(t, oldPW, newPW)
	require.NotContains(t, string(cmd.Payload), newPW)

	// A second rotation while one is pending is refused.
	_, err = f.dbs.RotateCredentials(f.ctx, pg.ID, false)
	require.Equal(t, "rotation_blocked", apiCode(t, err))

	// The agent reports success: the variable follows and the consumer is staged.
	f.result(cmd, agentprotocol.OutcomeSucceeded, "", agentprotocol.DBAdminResult{Action: agentprotocol.DBActionRotatePassword})
	require.Equal(t, newPW, f.getVar(pg.ID, "POSTGRES_PASSWORD"))
	apiRow := f.reload(api.ID)
	require.Greater(t, apiRow.Generation, apiRow.AppliedGeneration, "the referrer shows as staged")
	workerRow := f.reload(worker.ID)
	require.Equal(t, workerRow.Generation, workerRow.AppliedGeneration, "an unrelated service is untouched")
	require.Equal(t, dbGen, f.reload(pg.ID).Generation, "the database is not restaged: its password lives in the data directory")
	require.Equal(t, "postgres://postgres:"+newPW+"@pg:5432/app", f.resolve("api").Vars["DATABASE_URL"])

	// A replayed result is a no-op.
	gen := f.reload(api.ID).Generation
	f.result(cmd, agentprotocol.OutcomeSucceeded, "", agentprotocol.DBAdminResult{})
	require.Equal(t, gen, f.reload(api.ID).Generation)

	// Audit: request and application, keys and counts only.
	var audits []string
	rows, err := f.pool.Query(context.Background(), "SELECT action || ' ' || metadata::text FROM audit_events WHERE action LIKE 'database.rotate_credentials%' ORDER BY id")
	require.NoError(t, err)
	for rows.Next() {
		var s string
		require.NoError(t, rows.Scan(&s))
		audits = append(audits, s)
	}
	require.Len(t, audits, 2)
	for _, a := range audits {
		require.NotContains(t, a, newPW)
		require.NotContains(t, a, oldPW)
	}

	view, err := f.dbs.Get(f.ctx, pg.ID)
	require.NoError(t, err)
	require.Equal(t, "applied", view.Rotation.Status)
}

func TestRotateExecFailureKeepsTheOldCredentials(t *testing.T) {
	f := newFixture(t)
	pg := f.createDB("pg", "postgres", "17")
	f.deploy(pg.ID)
	api := f.app("api")
	f.setVar(api.ID, "DATABASE_URL", "${{pg.DATABASE_URL}}")
	f.deploy(api.ID)
	oldPW := f.getVar(pg.ID, "POSTGRES_PASSWORD")

	_, err := f.dbs.RotateCredentials(f.ctx, pg.ID, false)
	require.NoError(t, err)
	cmd, _ := f.lastCommand(agentprotocol.KindDBAdmin)
	f.result(cmd, agentprotocol.OutcomeFailed, "psql exited with code 2: connection refused", agentprotocol.DBAdminResult{})

	require.Equal(t, oldPW, f.getVar(pg.ID, "POSTGRES_PASSWORD"))
	apiRow := f.reload(api.ID)
	require.Equal(t, apiRow.Generation, apiRow.AppliedGeneration)
	view, err := f.dbs.Get(f.ctx, pg.ID)
	require.NoError(t, err)
	require.Equal(t, "failed", view.Rotation.Status)
	require.Contains(t, view.Rotation.Error, "connection refused")

	// And a new rotation can be requested again.
	_, err = f.dbs.RotateCredentials(f.ctx, pg.ID, false)
	require.NoError(t, err)
}

func TestRotateNeedsBothScopesAndAUsableDatabase(t *testing.T) {
	f := newFixture(t)
	pg := f.createDB("pg", "postgres", "17")
	f.deploy(pg.ID)
	pwOnly := f.principal([]string{policy.ScopeProjectsWrite}, nil)
	_, err := f.dbs.RotateCredentials(pwOnly, pg.ID, false)
	require.Equal(t, "forbidden", apiCode(t, err))
	dwOnly := f.principal([]string{policy.ScopeDeploymentsWrite}, nil)
	_, err = f.dbs.RotateCredentials(dwOnly, pg.ID, false)
	require.Equal(t, "forbidden", apiCode(t, err))

	// An active deployment blocks it.
	_, err = f.pool.Exec(context.Background(), `INSERT INTO deployments (service_id, server_id, trigger, actor_type, status, strategy) VALUES ($1, $2, 'manual', 'user', 'building', 'recreate')`, pg.ID, f.server.ID)
	require.NoError(t, err)
	_, err = f.dbs.RotateCredentials(f.ctx, pg.ID, false)
	require.Equal(t, "rotation_blocked", apiCode(t, err))

	// A plain service is not a managed database.
	app := f.app("plain")
	_, err = f.dbs.RotateCredentials(f.ctx, app.ID, false)
	require.Equal(t, "not_a_managed_database", apiCode(t, err))
}

func TestRotateRedeployEngineRestagesDatabaseAndConsumers(t *testing.T) {
	f := newFixture(t)
	kv := f.createDB("cache", "valkey", "9")
	f.deploy(kv.ID)
	api := f.app("api")
	f.setVar(api.ID, "REDIS_URL", "${{cache.REDIS_URL}}")
	f.deploy(api.ID)
	oldPW := f.getVar(kv.ID, "VALKEY_PASSWORD")

	res, err := f.dbs.RotateCredentials(f.ctx, kv.ID, false)
	require.NoError(t, err)
	require.Equal(t, "applied", res.Status)
	require.Equal(t, databases.ModeRedeploy, res.Plan.Mode)
	require.Empty(t, f.disp.cmds, "no exec: the engine reads its password at start")
	require.NotEqual(t, oldPW, f.getVar(kv.ID, "VALKEY_PASSWORD"))
	kvRow, apiRow := f.reload(kv.ID), f.reload(api.ID)
	require.Greater(t, kvRow.Generation, kvRow.AppliedGeneration, "the database restages")
	require.Greater(t, apiRow.Generation, apiRow.AppliedGeneration, "so does its consumer")
}

func TestRotateBeforeFirstDeployJustReplacesTheInitialPassword(t *testing.T) {
	f := newFixture(t)
	pg := f.createDB("pg", "postgres", "17")
	oldPW := f.getVar(pg.ID, "POSTGRES_PASSWORD")
	res, err := f.dbs.RotateCredentials(f.ctx, pg.ID, false)
	require.NoError(t, err)
	require.Equal(t, databases.ModeVariable, res.Plan.Mode)
	require.Equal(t, "applied", res.Status)
	require.Empty(t, f.disp.cmds)
	require.NotEqual(t, oldPW, f.getVar(pg.ID, "POSTGRES_PASSWORD"))
}

func TestUndeliveredRotationFailsWithoutChangingAnything(t *testing.T) {
	f := newFixture(t)
	pg := f.createDB("pg", "postgres", "17")
	f.deploy(pg.ID)
	oldPW := f.getVar(pg.ID, "POSTGRES_PASSWORD")
	_, err := f.dbs.RotateCredentials(f.ctx, pg.ID, false)
	require.NoError(t, err)
	cmd, _ := f.lastCommand(agentprotocol.KindDBAdmin)
	require.NoError(t, f.dbs.HandleUndelivered(context.Background(), cmd.ID, ""))
	view, err := f.dbs.Get(f.ctx, pg.ID)
	require.NoError(t, err)
	require.Equal(t, "failed", view.Rotation.Status)
	require.Contains(t, view.Rotation.Error, "Not delivered")
	require.Equal(t, oldPW, f.getVar(pg.ID, "POSTGRES_PASSWORD"))
}

func TestInfoRefreshStoresTheAgentSnapshot(t *testing.T) {
	f := newFixture(t)
	pg := f.createDB("pg", "postgres", "17")
	_, err := f.dbs.Refresh(f.ctx, pg.ID)
	require.Equal(t, "not_deployed", apiCode(t, err))
	f.deploy(pg.ID)
	_, err = f.dbs.Refresh(f.ctx, pg.ID)
	require.NoError(t, err)
	cmd, ok := f.lastCommand(agentprotocol.KindDBAdmin)
	require.True(t, ok)
	sec, err := f.dbs.SecretsForCommand(f.ctx, cmd.ID, cmd.Kind, cmd.Payload)
	require.NoError(t, err)
	require.Equal(t, f.getVar(pg.ID, "POSTGRES_PASSWORD"), sec[agentprotocol.SecretDBAdminPassword])
	require.NotContains(t, sec, agentprotocol.SecretDBAdminNewPassword)

	f.result(cmd, agentprotocol.OutcomeSucceeded, "", agentprotocol.DBAdminResult{
		Action: agentprotocol.DBActionInfo, Info: &agentprotocol.DBInfo{Version: "17.6", SizeBytes: 8 << 20, Connections: 4},
	})
	view, err := f.dbs.Get(f.ctx, pg.ID)
	require.NoError(t, err)
	require.NotNil(t, view.Info)
	require.Equal(t, "17.6", view.Info.Version)
	require.Equal(t, int64(8<<20), view.Info.SizeBytes)
	require.Equal(t, 4, view.Info.Connections)
	// Exports are listed without values.
	var primary string
	for _, e := range view.Exports {
		if e.Primary {
			primary = e.Key
		}
	}
	require.Equal(t, "DATABASE_URL", primary)
}

func TestUpgradePlans(t *testing.T) {
	f := newFixture(t)
	pg := f.createDB("pg", "postgres", "17")

	p, err := f.dbs.PlanUpgrade(f.ctx, pg.ID, "18")
	require.NoError(t, err)
	require.Equal(t, engines.MethodRedeploy, p.Method, "never deployed: nothing to convert")
	f.deploy(pg.ID)
	p, err = f.dbs.PlanUpgrade(f.ctx, pg.ID, "18")
	require.NoError(t, err)
	require.Equal(t, engines.MethodPGUpgrade, p.Method)
	require.Equal(t, "tianon/postgres-upgrade:17-to-18", p.HelperImage)
	require.True(t, p.Executable)
	require.Empty(t, p.Blockers)

	p, err = f.dbs.PlanUpgrade(f.ctx, pg.ID, "16")
	require.NoError(t, err)
	require.Equal(t, "downgrade", p.Kind)
	require.NotEmpty(t, p.Blockers)

	mg := f.createDB("docs", "mongodb", "7.0")
	f.deploy(mg.ID)
	p, err = f.dbs.PlanUpgrade(f.ctx, mg.ID, "8.0")
	require.NoError(t, err)
	require.Equal(t, engines.MethodDumpRestore, p.Method)
	_, err = f.dbs.Upgrade(f.ctx, mg.ID, "8.0", mg.Slug)
	require.Equal(t, "upgrade_not_automatic", apiCode(t, err))
}

func TestUpgradePGUpgradeConvertsIntoANewVolumeAndStages(t *testing.T) {
	f := newFixture(t)
	pg := f.createDB("pg", "postgres", "17")
	f.deploy(pg.ID)

	_, err := f.dbs.Upgrade(f.ctx, pg.ID, "18", "wrong")
	require.Equal(t, "confirm_mismatch", apiCode(t, err))
	res, err := f.dbs.Upgrade(f.ctx, pg.ID, "18", pg.Slug)
	require.NoError(t, err)
	require.Equal(t, "pending", res.Status)

	cmd, ok := f.lastCommand(agentprotocol.KindDBUpgrade)
	require.True(t, ok)
	var p agentprotocol.DBUpgradePayload
	require.NoError(t, json.Unmarshal(cmd.Payload, &p))
	require.Equal(t, "tianon/postgres-upgrade:17-to-18", p.HelperImage)
	require.Equal(t, "data", p.OldVolume)
	require.Equal(t, "data-pg18", p.NewVolume)
	require.Equal(t, "stopped", f.reload(pg.ID).DesiredState, "the agent stops the database")

	// A second upgrade while one runs is refused.
	_, err = f.dbs.Upgrade(f.ctx, pg.ID, "18", pg.Slug)
	require.Equal(t, "upgrade_blocked", apiCode(t, err))

	f.result(cmd, agentprotocol.OutcomeSucceeded, "", agentprotocol.DBUpgradeResult{Message: "ok"})
	row := f.reload(pg.ID)
	cfg := services.DecodeConfig(row.Config)
	require.Equal(t, "18", cfg.Database.Version)
	require.Equal(t, "postgres:18", cfg.Image)
	require.Greater(t, row.Generation, row.AppliedGeneration, "the new version is a staged change")
	vols, err := f.q.ListServiceVolumes(context.Background(), pg.ID)
	require.NoError(t, err)
	require.Len(t, vols, 1)
	require.Equal(t, "data-pg18", vols[0].Name)
	require.Equal(t, "/var/lib/postgresql", vols[0].MountPath)

	view, err := f.dbs.Get(f.ctx, pg.ID)
	require.NoError(t, err)
	require.Equal(t, "ready", view.Upgrade.Status)
	require.Equal(t, "data", view.Upgrade.OldVolume)
}

func TestUpgradeFailureLeavesTheDatabaseOnItsOldVersion(t *testing.T) {
	f := newFixture(t)
	pg := f.createDB("pg", "postgres", "17")
	f.deploy(pg.ID)
	_, err := f.dbs.Upgrade(f.ctx, pg.ID, "18", pg.Slug)
	require.NoError(t, err)
	cmd, _ := f.lastCommand(agentprotocol.KindDBUpgrade)
	f.result(cmd, agentprotocol.OutcomeFailed, "pg_upgrade failed; the database was not changed", agentprotocol.DBUpgradeResult{})
	row := f.reload(pg.ID)
	require.Equal(t, "running", row.DesiredState)
	require.Equal(t, "17", services.DecodeConfig(row.Config).Database.Version)
	vols, _ := f.q.ListServiceVolumes(context.Background(), pg.ID)
	require.Equal(t, "data", vols[0].Name)
	view, _ := f.dbs.Get(f.ctx, pg.ID)
	require.Equal(t, "failed", view.Upgrade.Status)
}

func TestUpgradeInPlaceEnginesStageTheNewImage(t *testing.T) {
	f := newFixture(t)
	kv := f.createDB("cache", "valkey", "8")
	f.deploy(kv.ID)
	res, err := f.dbs.Upgrade(f.ctx, kv.ID, "9", kv.Slug)
	require.NoError(t, err)
	require.Equal(t, "staged", res.Status)
	require.Empty(t, f.disp.cmds)
	row := f.reload(kv.ID)
	require.Equal(t, "valkey/valkey:9", services.DecodeConfig(row.Config).Image)
	require.Greater(t, row.Generation, row.AppliedGeneration)
}

func TestPatchKeepsTheEngineAndVersion(t *testing.T) {
	f := newFixture(t)
	pg := f.createDB("pg", "postgres", "17")
	// A PATCH that replaces the config cannot change the engine, version or derived fields.
	cfg := services.Config{Image: "evil/postgres:1", Port: 9999, MemoryMB: 512}
	v, err := f.svcs.Update(f.ctx, pg.ID, pg.Generation, services.UpdateInput{Config: &cfg})
	require.NoError(t, err)
	require.Equal(t, "17", v.Config.Database.Version)
	require.Equal(t, "postgres:17", v.Config.Image)
	require.Equal(t, 5432, v.Config.Port)
	require.Equal(t, 512, v.Config.MemoryMB)
}

// A database-kind service can also be created through the generic service API
// from a plain image config.
func TestServiceCreateAcceptsDatabaseKindFromPlainImage(t *testing.T) {
	f := newFixture(t)
	_, err := f.svcs.Create(f.ctx, f.env.ID, services.CreateInput{Name: "x", Kind: services.KindDatabase, SourceType: services.SourceImage, Config: services.Config{Image: "postgres:17", Port: 5432}})
	require.NoError(t, err)
}

// Managed databases never need the internet (backups stream through the
// agent), so outbound internet starts blocked. A config update that does not
// mention it keeps it blocked; only an explicit false allows it.
func TestDatabasesBlockEgressByDefault(t *testing.T) {
	f := newFixture(t)
	v := f.createDB("pg", "postgres", "17")
	require.True(t, v.Config.BlockEgress)
	require.True(t, services.DecodeConfig(f.reload(v.ID).Config).BlockEgress)

	cfg := v.Config
	cfg.MemoryMB = 512
	updated, err := f.svcs.Update(f.ctx, v.ID, v.Generation, services.UpdateInput{Config: &cfg})
	require.NoError(t, err)
	require.True(t, updated.Config.BlockEgress, "a PATCH that omits blockEgress must not unblock a database")

	allow := false
	updated, err = f.svcs.Update(f.ctx, v.ID, updated.Generation, services.UpdateInput{Config: &cfg, BlockEgress: &allow})
	require.NoError(t, err)
	require.False(t, updated.Config.BlockEgress)
}
