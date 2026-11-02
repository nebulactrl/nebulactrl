package terminal

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/testutil"
)

// dbFx is a Service on the real stores against a fresh schema: one team with an
// instance admin (the earliest user) and a second member, a project with a
// production and a development environment and an online server.
type dbFx struct {
	t       *testing.T
	pool    *pgxpool.Pool
	q       *db.Queries
	svc     *Service
	ws      db.Workspace
	admin   db.CreateUserRow // earliest user: the instance admin
	member  db.CreateUserRow
	project db.Project
	prod    db.Environment
	dev     db.Environment
	server  db.Server
}

func newDBFx(t *testing.T) *dbFx {
	t.Helper()
	pool := testutil.NewPool(t)
	q := db.New(pool)
	bg := context.Background()
	ws, err := q.CreateWorkspace(bg, "team")
	require.NoError(t, err)
	admin, err := q.CreateUser(bg, db.CreateUserParams{WorkspaceID: ws.ID, Email: "root@example.com", Name: "Root", PasswordHash: "x"})
	require.NoError(t, err)
	_, err = pool.Exec(bg, "UPDATE users SET created_at = now() - interval '1 day' WHERE id = $1", admin.ID)
	require.NoError(t, err)
	member, err := q.CreateUser(bg, db.CreateUserParams{WorkspaceID: ws.ID, Email: "dev@example.com", Name: "Dev", PasswordHash: "x"})
	require.NoError(t, err)
	prj, err := q.CreateProject(bg, db.CreateProjectParams{WorkspaceID: ws.ID, Name: "Shop", Slug: "shop"})
	require.NoError(t, err)
	prod, err := q.CreateEnvironment(bg, db.CreateEnvironmentParams{ProjectID: prj.ID, Name: "production", Slug: "production", IsProduction: true})
	require.NoError(t, err)
	dev, err := q.CreateEnvironment(bg, db.CreateEnvironmentParams{ProjectID: prj.ID, Name: "dev", Slug: "dev"})
	require.NoError(t, err)
	srv, err := q.CreateServer(bg, db.CreateServerParams{WorkspaceID: ws.ID, Name: "hetzner-01"})
	require.NoError(t, err)
	_, err = pool.Exec(bg, "UPDATE servers SET status = 'online' WHERE id = $1", srv.ID)
	require.NoError(t, err)
	svc := NewService(Deps{Pool: pool, Agents: &fakeAgents{}, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	return &dbFx{t: t, pool: pool, q: q, svc: svc, ws: ws, admin: admin, member: member, project: prj, prod: prod, dev: dev, server: srv}
}

func (f *dbFx) ctxFor(userID uuid.UUID, role string, scopes []string, projects ...uuid.UUID) context.Context {
	p := &auth.Principal{Type: auth.TypeUser, UserID: userID, WorkspaceID: f.ws.ID, Label: "u@example.com", Role: role, Scopes: scopes}
	if len(projects) > 0 {
		p.ProjectIDs = projects
	}
	return auth.WithPrincipal(context.Background(), p)
}

func (f *dbFx) adminCtx() context.Context {
	return f.ctxFor(f.admin.ID, auth.RoleOwner, policy.AllScopes)
}

func (f *dbFx) memberAdminCtx() context.Context {
	return f.ctxFor(f.member.ID, auth.RoleAdmin, policy.AllScopes)
}

type svcOpts struct {
	kind     string
	config   string
	observed string
	deployed bool
	server   bool
}

func (f *dbFx) insertService(env db.Environment, name string, o svcOpts) db.Service {
	f.t.Helper()
	bg := context.Background()
	if o.kind == "" {
		o.kind = "app"
	}
	if o.config == "" {
		o.config = `{}`
	}
	p := db.InsertServiceParams{
		EnvironmentID: env.ID, Name: name, Slug: name, Kind: o.kind, SourceType: "image",
		Config: json.RawMessage(o.config), ID: uuid.New(),
	}
	if o.server {
		p.ServerID = &f.server.ID
	}
	s, err := f.q.InsertService(bg, p)
	require.NoError(f.t, err)
	if o.observed != "" {
		_, err = f.pool.Exec(bg, "UPDATE services SET observed = $2::jsonb WHERE id = $1", s.ID, o.observed)
		require.NoError(f.t, err)
	}
	if o.deployed {
		var dep uuid.UUID
		require.NoError(f.t, f.pool.QueryRow(bg,
			"INSERT INTO deployments (service_id, server_id, trigger, actor_type) VALUES ($1, $2, 'manual', 'user') RETURNING id", s.ID, f.server.ID).Scan(&dep))
		_, err = f.pool.Exec(bg, "UPDATE services SET current_deployment_id = $2 WHERE id = $1", s.ID, dep)
		require.NoError(f.t, err)
	}
	return s
}

func (f *dbFx) auditRows(action string) []map[string]any {
	f.t.Helper()
	rows, err := f.pool.Query(context.Background(),
		"SELECT resource_type, resource_id, actor_type, actor_label, project_id::text, metadata FROM audit_events WHERE action = $1 ORDER BY id", action)
	require.NoError(f.t, err)
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var rt, rid, at, al string
		var pid *string
		var md map[string]any
		require.NoError(f.t, rows.Scan(&rt, &rid, &at, &al, &pid, &md))
		row := map[string]any{"resourceType": rt, "resourceId": rid, "actorType": at, "actorLabel": al, "metadata": md}
		if pid != nil {
			row["projectId"] = *pid
		}
		out = append(out, row)
	}
	require.NoError(f.t, rows.Err())
	return out
}

func statusOf(t *testing.T, err error) (int, string) {
	t.Helper()
	var ae *apierr.Error
	require.True(t, errors.As(err, &ae), "want an apierr.Error, got %v", err)
	return ae.Status, ae.Code
}

const runningObserved = `{"state":"running","replicas":[{"index":0,"state":"running","health":"healthy"},{"index":1,"state":"exited"}]}`

func TestInfoReadsServiceStateFromTheDatabase(t *testing.T) {
	f := newDBFx(t)
	ctx := f.adminCtx()

	t.Run("deployed and running in development", func(t *testing.T) {
		s := f.insertService(f.dev, "web", svcOpts{deployed: true, server: true, observed: runningObserved})
		v, err := f.svc.Info(ctx, s.ID)
		require.NoError(t, err)
		require.True(t, v.Available)
		require.Empty(t, v.Reason)
		require.False(t, v.Production)
		require.Equal(t, "hetzner-01", v.ServerName)
		require.Equal(t, []ReplicaView{
			{Index: 0, State: "running", Health: "healthy", Running: true},
			{Index: 1, State: "exited"},
		}, v.Replicas)
		require.True(t, v.FileWrites.Allowed)
		require.Equal(t, MaxSessionsPerUser, v.MaxSessions)
		require.Zero(t, v.ActiveSessions)
	})

	t.Run("production is read-only until an admin allows writes", func(t *testing.T) {
		s := f.insertService(f.prod, "api", svcOpts{deployed: true, server: true, observed: runningObserved})
		v, err := f.svc.Info(ctx, s.ID)
		require.NoError(t, err)
		require.True(t, v.Production)
		require.True(t, v.Available)
		require.False(t, v.FileWrites.Allowed)
		require.Contains(t, v.FileWrites.Reason, "read-only in production")

		_, err = f.svc.SetSettings(ctx, f.project.ID, SettingsView{AllowProductionFileWrites: true})
		require.NoError(t, err)
		v, err = f.svc.Info(ctx, s.ID)
		require.NoError(t, err)
		require.True(t, v.FileWrites.Allowed, "the stored setting lifts the production restriction")
		require.Empty(t, v.FileWrites.Reason)
	})

	t.Run("not deployed", func(t *testing.T) {
		s := f.insertService(f.dev, "undeployed", svcOpts{server: true, observed: runningObserved})
		v, err := f.svc.Info(ctx, s.ID)
		require.NoError(t, err)
		require.False(t, v.Available)
		require.Equal(t, "The service is not deployed yet.", v.Reason)
	})

	t.Run("deployed without a server", func(t *testing.T) {
		s := f.insertService(f.dev, "serverless", svcOpts{deployed: true})
		v, err := f.svc.Info(ctx, s.ID)
		require.NoError(t, err)
		require.False(t, v.Available)
		require.Empty(t, v.ServerName)
		require.NotNil(t, v.Replicas, "replicas are an empty list, not null")
		require.Empty(t, v.Replicas)
	})

	t.Run("route services have no container", func(t *testing.T) {
		s := f.insertService(f.dev, "edge", svcOpts{kind: "route", deployed: true, server: true, observed: runningObserved})
		v, err := f.svc.Info(ctx, s.ID)
		require.NoError(t, err)
		require.False(t, v.Available)
		require.Equal(t, "This service has no container: it only routes traffic.", v.Reason)
	})

	t.Run("no replica running", func(t *testing.T) {
		s := f.insertService(f.dev, "stopped", svcOpts{deployed: true, server: true, observed: `{"state":"exited"}`})
		v, err := f.svc.Info(ctx, s.ID)
		require.NoError(t, err)
		require.False(t, v.Available)
		require.Equal(t, "No replica is running.", v.Reason)
		require.Equal(t, []ReplicaView{{Index: 0, State: "exited"}}, v.Replicas, "an observed state without replica detail is replica 0")
	})

	t.Run("running without per-replica detail", func(t *testing.T) {
		s := f.insertService(f.dev, "plain", svcOpts{deployed: true, server: true, observed: `{"state":"running","health":"healthy"}`})
		v, err := f.svc.Info(ctx, s.ID)
		require.NoError(t, err)
		require.True(t, v.Available)
		require.Equal(t, []ReplicaView{{Index: 0, State: "running", Health: "healthy", Running: true}}, v.Replicas)
	})
}

func TestInfoRefusesCallersOutsideTheirReach(t *testing.T) {
	f := newDBFx(t)
	s := f.insertService(f.dev, "web", svcOpts{deployed: true, server: true, observed: runningObserved})
	other := uuid.New()

	t.Run("another team sees no service", func(t *testing.T) {
		ctx := auth.WithPrincipal(context.Background(), &auth.Principal{
			Type: auth.TypeUser, UserID: f.admin.ID, WorkspaceID: uuid.New(), Role: auth.RoleOwner, Scopes: policy.AllScopes,
		})
		_, err := f.svc.Info(ctx, s.ID)
		st, _ := statusOf(t, err)
		require.Equal(t, http.StatusNotFound, st)
	})
	t.Run("unknown service", func(t *testing.T) {
		_, err := f.svc.Info(f.adminCtx(), uuid.New())
		st, _ := statusOf(t, err)
		require.Equal(t, http.StatusNotFound, st)
	})
	t.Run("viewer role", func(t *testing.T) {
		_, err := f.svc.Info(f.ctxFor(f.member.ID, auth.RoleViewer, policy.AllScopes), s.ID)
		st, _ := statusOf(t, err)
		require.Equal(t, http.StatusForbidden, st)
	})
	t.Run("missing deployments:write", func(t *testing.T) {
		_, err := f.svc.Info(f.ctxFor(f.member.ID, auth.RoleDeveloper, []string{policy.ScopeProjectsRead}), s.ID)
		st, _ := statusOf(t, err)
		require.Equal(t, http.StatusForbidden, st)
	})
	t.Run("missing projects:read", func(t *testing.T) {
		_, err := f.svc.Info(f.ctxFor(f.member.ID, auth.RoleDeveloper, []string{policy.ScopeDeploymentsWrite}), s.ID)
		st, _ := statusOf(t, err)
		require.Equal(t, http.StatusForbidden, st)
	})
	t.Run("restricted to another project", func(t *testing.T) {
		_, err := f.svc.Info(f.ctxFor(f.member.ID, auth.RoleDeveloper, policy.AllScopes, other), s.ID)
		st, _ := statusOf(t, err)
		require.Equal(t, http.StatusNotFound, st)
	})
	t.Run("anonymous", func(t *testing.T) {
		_, err := f.svc.Info(context.Background(), s.ID)
		st, _ := statusOf(t, err)
		require.Equal(t, http.StatusUnauthorized, st)
	})
	t.Run("platform services are for administrators", func(t *testing.T) {
		_, err := f.pool.Exec(context.Background(), "UPDATE projects SET system = true WHERE id = $1", f.project.ID)
		require.NoError(t, err)
		scopes := []string{policy.ScopeProjectsRead, policy.ScopeDeploymentsWrite}
		_, err = f.svc.Info(f.ctxFor(f.member.ID, auth.RoleDeveloper, scopes), s.ID)
		st, _ := statusOf(t, err)
		require.Equal(t, http.StatusNotFound, st)
		_, err = f.svc.Info(f.adminCtx(), s.ID)
		require.NoError(t, err, "an administrator still reaches it")
	})
}

func TestLoadReadsProcessesOfTheServiceConfig(t *testing.T) {
	f := newDBFx(t)
	s := f.insertService(f.dev, "multi", svcOpts{
		deployed: true, server: true, observed: runningObserved,
		config: `{"image":"nginx","processes":[{"name":"worker","command":"run worker"}]}`,
	})
	plain := f.insertService(f.dev, "plain", svcOpts{deployed: true, server: true, config: `{"image":"nginx"}`})
	st := &dbStore{pool: f.pool, q: f.q}

	tg, err := st.load(f.adminCtx(), s.ID)
	require.NoError(t, err)
	require.NotEmpty(t, tg.Processes)
	require.Contains(t, tg.Processes, "worker")
	require.NotEmpty(t, tg.DefaultProcess)
	require.True(t, tg.knowsProcess("worker"))
	require.Equal(t, f.project.ID, tg.ProjectID)
	require.Equal(t, f.dev.ID, tg.EnvironmentID)
	require.False(t, tg.Production)
	require.True(t, tg.Deployed)
	require.Equal(t, "multi", tg.Name)

	tg, err = st.load(f.adminCtx(), plain.ID)
	require.NoError(t, err)
	require.Empty(t, tg.Processes)
	require.Empty(t, tg.DefaultProcess)
}

func TestReplicasOfDecodesObservedState(t *testing.T) {
	require.Nil(t, replicasOf(nil))
	require.Nil(t, replicasOf(json.RawMessage(`not json`)))
	require.Empty(t, replicasOf(json.RawMessage(`{}`)), "no state and no replicas: nothing to show")
	require.Equal(t,
		[]ReplicaView{{Index: 2, Process: "worker", State: "running", Health: "healthy", Running: true}},
		replicasOf(json.RawMessage(`{"state":"exited","replicas":[{"index":2,"process":"worker","state":"running","health":"healthy"}]}`)),
		"per-replica detail wins over the service state")
}

func TestTerminalSettingsRoundTripAndAudit(t *testing.T) {
	f := newDBFx(t)
	ctx := f.adminCtx()

	v, err := f.svc.Settings(ctx, f.project.ID)
	require.NoError(t, err)
	require.False(t, v.AllowProductionFileWrites, "off by default")

	out, err := f.svc.SetSettings(ctx, f.project.ID, SettingsView{AllowProductionFileWrites: true})
	require.NoError(t, err)
	require.True(t, out.AllowProductionFileWrites)
	v, err = f.svc.Settings(ctx, f.project.ID)
	require.NoError(t, err)
	require.True(t, v.AllowProductionFileWrites, "the setting is stored")

	rows := f.auditRows(ActionSettingsUpdate)
	require.Len(t, rows, 1)
	require.Equal(t, "project", rows[0]["resourceType"])
	require.Equal(t, f.project.ID.String(), rows[0]["resourceId"])
	require.Equal(t, f.project.ID.String(), rows[0]["projectId"])
	require.Equal(t, "user", rows[0]["actorType"])
	require.Equal(t, true, rows[0]["metadata"].(map[string]any)["allowProductionFileWrites"])

	_, err = f.svc.SetSettings(ctx, f.project.ID, SettingsView{})
	require.NoError(t, err)
	v, err = f.svc.Settings(ctx, f.project.ID)
	require.NoError(t, err)
	require.False(t, v.AllowProductionFileWrites, "switching it off overwrites the row")
	require.Len(t, f.auditRows(ActionSettingsUpdate), 2)
}

func TestTerminalSettingsRefusals(t *testing.T) {
	f := newDBFx(t)
	set := SettingsView{AllowProductionFileWrites: true}

	t.Run("developers cannot change the setting", func(t *testing.T) {
		_, err := f.svc.SetSettings(f.ctxFor(f.member.ID, auth.RoleDeveloper, policy.AllScopes), f.project.ID, set)
		st, _ := statusOf(t, err)
		require.Equal(t, http.StatusForbidden, st)
		v, err := f.svc.Settings(f.adminCtx(), f.project.ID)
		require.NoError(t, err)
		require.False(t, v.AllowProductionFileWrites)
		require.Empty(t, f.auditRows(ActionSettingsUpdate))
	})
	t.Run("a token without the admin scope", func(t *testing.T) {
		_, err := f.svc.SetSettings(f.ctxFor(f.member.ID, auth.RoleAdmin, []string{policy.ScopeProjectsRead}), f.project.ID, set)
		st, _ := statusOf(t, err)
		require.Equal(t, http.StatusForbidden, st)
	})
	t.Run("unknown project", func(t *testing.T) {
		_, err := f.svc.SetSettings(f.adminCtx(), uuid.New(), set)
		st, _ := statusOf(t, err)
		require.Equal(t, http.StatusNotFound, st)
		_, err = f.svc.Settings(f.adminCtx(), uuid.New())
		st, _ = statusOf(t, err)
		require.Equal(t, http.StatusNotFound, st)
	})
	t.Run("project of another team", func(t *testing.T) {
		ctx := auth.WithPrincipal(context.Background(), &auth.Principal{
			Type: auth.TypeUser, UserID: f.admin.ID, WorkspaceID: uuid.New(), Role: auth.RoleOwner, Scopes: policy.AllScopes,
		})
		_, err := f.svc.Settings(ctx, f.project.ID)
		st, _ := statusOf(t, err)
		require.Equal(t, http.StatusNotFound, st)
		_, err = f.svc.SetSettings(ctx, f.project.ID, set)
		st, _ = statusOf(t, err)
		require.Equal(t, http.StatusNotFound, st)
	})
	t.Run("restricted principal outside the project", func(t *testing.T) {
		ctx := f.ctxFor(f.member.ID, auth.RoleAdmin, policy.AllScopes, uuid.New())
		_, err := f.svc.Settings(ctx, f.project.ID)
		st, _ := statusOf(t, err)
		require.Equal(t, http.StatusNotFound, st)
	})
	t.Run("missing projects:read", func(t *testing.T) {
		// A token with write scopes but neither projects:read nor admin (which implies every scope).
		scopes := []string{policy.ScopeProjectsWrite, policy.ScopeDeploymentsWrite}
		_, err := f.svc.Settings(f.ctxFor(f.member.ID, auth.RoleAdmin, scopes), f.project.ID)
		st, _ := statusOf(t, err)
		require.Equal(t, http.StatusForbidden, st)
	})
}

func TestHostShellStoreAgainstTheDatabase(t *testing.T) {
	f := newDBFx(t)
	ctx := f.adminCtx()

	t.Run("off by default, offline server and no two-factor", func(t *testing.T) {
		v, err := f.svc.HostShellInfo(ctx, f.server.ID)
		require.NoError(t, err)
		require.Equal(t, f.server.ID, v.ServerID)
		require.Equal(t, "hetzner-01", v.ServerName)
		require.False(t, v.Enabled)
		require.False(t, v.Available)
		require.False(t, v.Production)
		require.False(t, v.ControlPlane)
		require.False(t, v.TwoFactorEnabled)
		require.Contains(t, v.Reason, "off")
	})

	t.Run("production is derived from the services on the server", func(t *testing.T) {
		f.insertService(f.dev, "dev-svc", svcOpts{server: true})
		v, err := f.svc.HostShellInfo(ctx, f.server.ID)
		require.NoError(t, err)
		require.False(t, v.Production, "a development service does not make the server production")
		f.insertService(f.prod, "prod-svc", svcOpts{server: true})
		v, err = f.svc.HostShellInfo(ctx, f.server.ID)
		require.NoError(t, err)
		require.True(t, v.Production)
	})

	t.Run("enabling is stored and audited; two-factor makes it available", func(t *testing.T) {
		out, err := f.svc.SetHostShell(ctx, f.server.ID, HostShellSettingsView{Enabled: true})
		require.NoError(t, err)
		require.True(t, out.Enabled)
		v, err := f.svc.HostShellInfo(ctx, f.server.ID)
		require.NoError(t, err)
		require.True(t, v.Enabled)
		require.False(t, v.Available)
		require.Contains(t, v.Reason, "two-factor")

		// A TOTP secret that was never confirmed is not a second factor.
		_, err = f.pool.Exec(context.Background(), "INSERT INTO user_totp (user_id, secret_enc) VALUES ($1, '\\x00')", f.admin.ID)
		require.NoError(t, err)
		v, err = f.svc.HostShellInfo(ctx, f.server.ID)
		require.NoError(t, err)
		require.False(t, v.TwoFactorEnabled)

		_, err = f.pool.Exec(context.Background(), "UPDATE user_totp SET enabled_at = now() WHERE user_id = $1", f.admin.ID)
		require.NoError(t, err)
		v, err = f.svc.HostShellInfo(ctx, f.server.ID)
		require.NoError(t, err)
		require.True(t, v.TwoFactorEnabled)
		require.True(t, v.Available)
		require.Empty(t, v.Reason)

		rows := f.auditRows(ActionHostShellEnable)
		require.Len(t, rows, 1)
		require.Equal(t, "server", rows[0]["resourceType"])
		require.Equal(t, f.server.ID.String(), rows[0]["resourceId"])
		require.Equal(t, true, rows[0]["metadata"].(map[string]any)["enabled"])
		var by *uuid.UUID
		require.NoError(t, f.pool.QueryRow(context.Background(), "SELECT updated_by FROM server_host_shell WHERE server_id = $1", f.server.ID).Scan(&by))
		require.NotNil(t, by)
		require.Equal(t, f.admin.ID, *by, "the setting records who changed it")
	})

	t.Run("a passkey counts as a second factor", func(t *testing.T) {
		v, err := f.svc.HostShellInfo(f.memberAdminCtx(), f.server.ID)
		require.NoError(t, err)
		require.False(t, v.TwoFactorEnabled)
		_, err = f.pool.Exec(context.Background(),
			"INSERT INTO user_passkeys (user_id, name, credential_id, credential) VALUES ($1, 'key', '\\x01', '{}')", f.member.ID)
		require.NoError(t, err)
		v, err = f.svc.HostShellInfo(f.memberAdminCtx(), f.server.ID)
		require.NoError(t, err)
		require.True(t, v.TwoFactorEnabled)
	})

	t.Run("an offline server is not available", func(t *testing.T) {
		_, err := f.pool.Exec(context.Background(), "UPDATE servers SET status = 'offline' WHERE id = $1", f.server.ID)
		require.NoError(t, err)
		v, err := f.svc.HostShellInfo(ctx, f.server.ID)
		require.NoError(t, err)
		require.False(t, v.Available)
		require.Equal(t, "The server is offline.", v.Reason)
	})

	t.Run("disabling is stored and audited", func(t *testing.T) {
		_, err := f.svc.SetHostShell(ctx, f.server.ID, HostShellSettingsView{Enabled: false})
		require.NoError(t, err)
		v, err := f.svc.HostShellInfo(ctx, f.server.ID)
		require.NoError(t, err)
		require.False(t, v.Enabled)
		rows := f.auditRows(ActionHostShellDisable)
		require.Len(t, rows, 1)
		require.Equal(t, false, rows[0]["metadata"].(map[string]any)["enabled"])
	})

	t.Run("servers of another team and unknown servers are 404", func(t *testing.T) {
		other, err := f.q.CreateWorkspace(context.Background(), "other")
		require.NoError(t, err)
		foreign, err := f.q.CreateServer(context.Background(), db.CreateServerParams{WorkspaceID: other.ID, Name: "foreign"})
		require.NoError(t, err)
		for _, id := range []uuid.UUID{foreign.ID, uuid.New()} {
			_, err = f.svc.HostShellInfo(ctx, id)
			st, _ := statusOf(t, err)
			require.Equal(t, http.StatusNotFound, st)
			_, err = f.svc.SetHostShell(ctx, id, HostShellSettingsView{Enabled: true})
			st, _ = statusOf(t, err)
			require.Equal(t, http.StatusNotFound, st)
		}
		var n int
		require.NoError(t, f.pool.QueryRow(context.Background(), "SELECT count(*) FROM server_host_shell WHERE server_id = $1", foreign.ID).Scan(&n))
		require.Zero(t, n, "nothing was stored for the foreign server")
	})
}

func TestHostShellControlPlaneRulesAgainstTheDatabase(t *testing.T) {
	f := newDBFx(t)
	_, err := f.pool.Exec(context.Background(), "UPDATE servers SET is_control_plane = true WHERE id = $1", f.server.ID)
	require.NoError(t, err)

	// The earliest user is the instance admin and may use the control-plane host.
	v, err := f.svc.HostShellInfo(f.adminCtx(), f.server.ID)
	require.NoError(t, err)
	require.True(t, v.ControlPlane)
	require.False(t, v.Restricted)

	// Another team Admin is restricted from it.
	v, err = f.svc.HostShellInfo(f.memberAdminCtx(), f.server.ID)
	require.NoError(t, err)
	require.True(t, v.Restricted)
	require.Contains(t, v.Reason, "instance admin")
	_, err = f.svc.SetHostShell(f.memberAdminCtx(), f.server.ID, HostShellSettingsView{Enabled: true})
	st, code := statusOf(t, err)
	require.Equal(t, http.StatusForbidden, st)
	require.Equal(t, CodeInstanceAdminRequired, code)
	require.Empty(t, f.auditRows(ActionHostShellEnable))

	_, err = f.svc.SetHostShell(f.adminCtx(), f.server.ID, HostShellSettingsView{Enabled: true})
	require.NoError(t, err)
	require.Len(t, f.auditRows(ActionHostShellEnable), 1)
}

func TestIsInstanceAdminWithoutUsers(t *testing.T) {
	pool := testutil.NewPool(t)
	hs := &dbHostStore{pool: pool, q: db.New(pool)}
	ok, err := hs.isInstanceAdmin(context.Background(), uuid.New())
	require.NoError(t, err)
	require.False(t, ok, "with no users nobody is the instance admin")
}

func TestHostShellRefusesTokensAndNonAdmins(t *testing.T) {
	f := newDBFx(t)
	tokenCtx := auth.WithPrincipal(context.Background(), &auth.Principal{
		Type: auth.TypeToken, UserID: f.admin.ID, WorkspaceID: f.ws.ID, Role: auth.RoleOwner, Scopes: policy.AllScopes,
	})
	_, err := f.svc.HostShellInfo(tokenCtx, f.server.ID)
	st, code := statusOf(t, err)
	require.Equal(t, http.StatusForbidden, st)
	require.Equal(t, CodeSessionRequired, code)

	_, err = f.svc.SetHostShell(f.ctxFor(f.member.ID, auth.RoleDeveloper, policy.AllScopes), f.server.ID, HostShellSettingsView{Enabled: true})
	st, _ = statusOf(t, err)
	require.Equal(t, http.StatusForbidden, st)
	require.Empty(t, f.auditRows(ActionHostShellEnable))

	_, err = f.svc.HostShellInfo(context.Background(), f.server.ID)
	st, _ = statusOf(t, err)
	require.Equal(t, http.StatusUnauthorized, st)

	_, err = f.svc.HostShellInfo(f.ctxFor(f.member.ID, auth.RoleAdmin, []string{policy.ScopeProjectsRead}), f.server.ID)
	st, _ = statusOf(t, err)
	require.Equal(t, http.StatusForbidden, st, "servers:read is required")
}
