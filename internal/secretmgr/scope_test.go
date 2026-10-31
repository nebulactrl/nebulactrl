package secretmgr

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/internal/testutil"
)

// scopeFixture is a team with two projects (A has production and staging, B has
// production), an admin, a Doppler integration server and the secretmgr Service.
type scopeFixture struct {
	t    *testing.T
	pool *pgxpool.Pool
	q    *db.Queries
	svc  *Service
	fd   *fakeDoppler
	ctx  context.Context
	ws   db.Workspace

	projA, projB            db.Project
	envAProd, envAStg, envB db.Environment
}

func newScopeFixture(t *testing.T) *scopeFixture {
	t.Helper()
	pool := testutil.NewPool(t)
	q := db.New(pool)
	bg := context.Background()
	ws, err := q.CreateWorkspace(bg, "w")
	require.NoError(t, err)
	u, err := q.CreateUser(bg, db.CreateUserParams{WorkspaceID: ws.ID, Email: "a@example.com", Name: "A", PasswordHash: "x"})
	require.NoError(t, err)
	mk := func(name, slug string) db.Project {
		p, err := q.CreateProject(bg, db.CreateProjectParams{WorkspaceID: ws.ID, Name: name, Slug: slug})
		require.NoError(t, err)
		return p
	}
	env := func(p db.Project, slug string, prod bool) db.Environment {
		e, err := q.CreateEnvironment(bg, db.CreateEnvironmentParams{ProjectID: p.ID, Name: slug, Slug: slug, IsProduction: prod})
		require.NoError(t, err)
		return e
	}
	f := &scopeFixture{t: t, pool: pool, q: q, ws: ws, fd: newFakeDoppler(t)}
	f.projA, f.projB = mk("Shop", "shop"), mk("Blog", "blog")
	f.envAProd, f.envAStg, f.envB = env(f.projA, "production", true), env(f.projA, "staging", false), env(f.projB, "production", true)
	box, err := secretbox.New(bytes.Repeat([]byte{7}, 32))
	require.NoError(t, err)
	f.svc = NewService(pool, box, slog.New(slog.NewTextHandler(io.Discard, nil)))
	f.ctx = auth.WithPrincipal(bg, &auth.Principal{Type: auth.TypeUser, UserID: u.ID, WorkspaceID: ws.ID, Label: "dev@example.com", Scopes: policy.AllScopes})
	return f
}

// create adds the Doppler integration "dp" (service token bound to backend/prd).
func (f *scopeFixture) create(scope string, grants ...ScopeGrant) SecretManager {
	f.t.Helper()
	m, err := f.svc.Create(f.ctx, Input{
		Name: "dp", Kind: KindDoppler, Config: SecretManagerConfig{SiteURL: f.fd.srv.URL}, Secret: dopplerServiceToken,
		Scope: scope, Grants: grants,
	})
	require.NoError(f.t, err)
	return m
}

func (f *scopeFixture) scope(p db.Project, e db.Environment, purpose string) services.ExtScope {
	return services.ExtScope{
		WorkspaceID: f.ws.ID, ProjectID: p.ID, EnvironmentID: e.ID, ServiceID: uuid.New(),
		Service: "api", Project: p.Slug, Environment: e.Slug, Purpose: purpose,
	}
}

func (f *scopeFixture) code(err error) string {
	f.t.Helper()
	ae, ok := apierr.As(err)
	require.True(f.t, ok, "%v", err)
	return ae.Code
}

func grantOf(p db.Project, e *db.Environment) ScopeGrant {
	g := ScopeGrant{ProjectID: p.ID}
	if e != nil {
		g.EnvironmentID = &e.ID
	}
	return g
}

func TestIntegrationScopeSelectedRefusesOutOfScopeBeforeAnyRequest(t *testing.T) {
	f := newScopeFixture(t)
	bg := context.Background()
	m := f.create(ScopeSelected, grantOf(f.projA, &f.envAStg))
	require.Equal(t, ScopeSelected, m.Scope)
	require.Len(t, m.Grants, 1)
	require.Equal(t, "Shop", m.Grants[0].ProjectName)
	require.Equal(t, "staging", m.Grants[0].EnvironmentName)

	r := ref(t, "doppler:dp#API_KEY")

	// The granted environment reads the secret.
	v, err := f.svc.Fetcher(f.scope(f.projA, f.envAStg, services.ExtPurposeDeploy)).Fetch(bg, r)
	require.NoError(t, err)
	require.Equal(t, "key-0123456789", v)
	require.EqualValues(t, 1, f.fd.downloads.Load())

	// Another environment of the same project, and another project, are refused
	// with an error that names the reference; no request leaves.
	for _, c := range []struct {
		p db.Project
		e db.Environment
	}{{f.projA, f.envAProd}, {f.projB, f.envB}} {
		_, err = f.svc.Fetcher(f.scope(c.p, c.e, services.ExtPurposeDeploy)).Fetch(bg, r)
		require.Equal(t, "integration_not_allowed", f.code(err))
		require.ErrorContains(t, err, "${{doppler:dp#API_KEY}}")
		require.ErrorContains(t, err, `integration "dp" is not available to project "`+c.p.Slug+`" environment "`+c.e.Slug+`"`)
	}
	require.EqualValues(t, 1, f.fd.downloads.Load(), "a refused reference never reaches the manager")

	// A scope without a project (a caller that did not say) fails closed.
	_, err = f.svc.Fetcher(services.ExtScope{WorkspaceID: f.ws.ID}).Fetch(bg, r)
	require.Equal(t, "integration_not_allowed", f.code(err))
}

func TestIntegrationScopeProjectGrantCoversItsEnvironmentsAndTeamScopeEveryone(t *testing.T) {
	f := newScopeFixture(t)
	bg := context.Background()
	m := f.create(ScopeSelected, grantOf(f.projA, nil))
	r := ref(t, "doppler:dp#API_KEY")
	fetch := func(p db.Project, e db.Environment) error {
		_, err := f.svc.Fetcher(f.scope(p, e, services.ExtPurposeDeploy)).Fetch(bg, r)
		return err
	}
	require.NoError(t, fetch(f.projA, f.envAProd))
	require.NoError(t, fetch(f.projA, f.envAStg))
	require.Equal(t, "integration_not_allowed", f.code(fetch(f.projB, f.envB)))

	// An environment created later is covered by the project grant.
	later, err := f.q.CreateEnvironment(bg, db.CreateEnvironmentParams{ProjectID: f.projA.ID, Name: "pr-7", Slug: "pr-7"})
	require.NoError(t, err)
	require.NoError(t, fetch(f.projA, later))

	// Omitting the scope on update keeps scope and grants.
	kept, err := f.svc.Update(f.ctx, m.ID, Input{Config: SecretManagerConfig{SiteURL: f.fd.srv.URL}})
	require.NoError(t, err)
	require.Equal(t, ScopeSelected, kept.Scope)
	require.Len(t, kept.Grants, 1)

	// Back to the whole team: every project, and the grants are gone.
	team, err := f.svc.Update(f.ctx, m.ID, Input{Config: SecretManagerConfig{SiteURL: f.fd.srv.URL}, Scope: ScopeTeam, Grants: []ScopeGrant{grantOf(f.projB, nil)}})
	require.NoError(t, err)
	require.Equal(t, ScopeTeam, team.Scope)
	require.Empty(t, team.Grants)
	require.NoError(t, fetch(f.projB, f.envB))
	var n int
	require.NoError(t, f.pool.QueryRow(bg, "SELECT count(*) FROM secret_integration_scopes").Scan(&n))
	require.Zero(t, n)

	// Narrow again to one project; the list shows it.
	_, err = f.svc.Update(f.ctx, m.ID, Input{Config: SecretManagerConfig{SiteURL: f.fd.srv.URL}, Scope: ScopeSelected, Grants: []ScopeGrant{grantOf(f.projB, &f.envB)}})
	require.NoError(t, err)
	list, err := f.svc.List(f.ctx)
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.Equal(t, ScopeSelected, list[0].Scope)
	require.Equal(t, "Blog", list[0].Grants[0].ProjectName)
	require.NoError(t, fetch(f.projB, f.envB))
	require.Equal(t, "integration_not_allowed", f.code(fetch(f.projA, f.envAStg)))
}

func TestIntegrationScopeValidation(t *testing.T) {
	f := newScopeFixture(t)
	bg := context.Background()
	create := func(scope string, grants ...ScopeGrant) error {
		_, err := f.svc.Create(f.ctx, Input{Name: "x" + uuid.NewString()[:6], Kind: KindDoppler, Config: SecretManagerConfig{SiteURL: f.fd.srv.URL}, Secret: dopplerServiceToken, Scope: scope, Grants: grants})
		return err
	}
	require.Equal(t, "grants_required", f.code(create(ScopeSelected)))
	require.Equal(t, "invalid_scope", f.code(create("some")))

	// A project outside the team, an environment of another project.
	otherWS, err := f.q.CreateWorkspace(bg, "other")
	require.NoError(t, err)
	foreign, err := f.q.CreateProject(bg, db.CreateProjectParams{WorkspaceID: otherWS.ID, Name: "Foreign", Slug: "foreign"})
	require.NoError(t, err)
	require.Equal(t, "invalid_grant", f.code(create(ScopeSelected, ScopeGrant{ProjectID: foreign.ID})))
	require.Equal(t, "invalid_grant", f.code(create(ScopeSelected, grantOf(f.projA, &f.envB))))

	// Duplicates collapse; environment grants of a fully allowed project fold into it.
	m, err := f.svc.Create(f.ctx, Input{
		Name: "folded", Kind: KindDoppler, Config: SecretManagerConfig{SiteURL: f.fd.srv.URL}, Secret: dopplerServiceToken, Scope: ScopeSelected,
		Grants: []ScopeGrant{grantOf(f.projA, &f.envAStg), grantOf(f.projA, nil), grantOf(f.projA, nil), grantOf(f.projB, &f.envB), grantOf(f.projB, &f.envB)},
	})
	require.NoError(t, err)
	require.Len(t, m.Grants, 2)

	// A failed create leaves nothing behind.
	var n int
	require.NoError(t, f.pool.QueryRow(bg, "SELECT count(*) FROM secret_integrations").Scan(&n))
	require.Equal(t, 1, n)

	// Only admins change scopes.
	ro := auth.WithPrincipal(bg, &auth.Principal{Type: auth.TypeToken, WorkspaceID: f.ws.ID, TokenID: new(uuid.New()), Scopes: []string{policy.ScopeProjectsRead}})
	_, err = f.svc.Update(ro, m.ID, Input{Scope: ScopeTeam})
	require.Equal(t, "forbidden", f.code(err))
}

func TestAccessLogRecordsReferencesAndServiceNeverValues(t *testing.T) {
	f := newScopeFixture(t)
	bg := context.Background()
	f.create(ScopeTeam)

	sc := f.scope(f.projA, f.envAProd, services.ExtPurposeDeploy)
	sc.DeploymentID, sc.StartedBy = uuid.NewString(), "dev@example.com"
	fetcher := f.svc.Fetcher(sc)
	_, err := fetcher.Fetch(bg, ref(t, "doppler:dp#DATABASE_URL"))
	require.NoError(t, err)
	_, err = fetcher.Fetch(bg, ref(t, "doppler:dp#API_KEY"))
	require.NoError(t, err)
	_, err = fetcher.Fetch(bg, ref(t, "doppler:dp#API_KEY"))
	require.NoError(t, err, "a repeated reference is served from the session")
	_, err = fetcher.Fetch(bg, ref(t, "doppler:dp#MISSING"))
	require.Error(t, err)
	_, err = fetcher.Fetch(bg, ref(t, "doppler:nope#K"))
	require.Error(t, err)
	_, err = fetcher.Fetch(bg, ref(t, "vault:dp/a#k"))
	require.ErrorContains(t, err, "is a doppler integration, which does not take vault: references")

	// Nothing is recorded before the operation ends.
	var n int
	require.NoError(t, f.pool.QueryRow(bg, "SELECT count(*) FROM audit_events WHERE action LIKE 'secret.fetch%'").Scan(&n))
	require.Zero(t, n)

	services.FlushFetcher(bg, fetcher)
	services.FlushFetcher(bg, fetcher) // idempotent: nothing new to record

	rows, err := f.pool.Query(bg, "SELECT action, actor_type, actor_label, resource_type, resource_id, project_id, metadata FROM audit_events WHERE action LIKE 'secret.%' ORDER BY id")
	require.NoError(t, err)
	defer rows.Close()
	var got int
	for rows.Next() {
		got++
		var action, actorType, label, rtype, rid string
		var project *uuid.UUID
		var raw []byte
		require.NoError(t, rows.Scan(&action, &actorType, &label, &rtype, &rid, &project, &raw))
		require.Equal(t, ActionFetch, action)
		require.Equal(t, "system", actorType, "a send-time fetch has no principal; startedBy names who started the deployment")
		require.Equal(t, "service", rtype)
		require.Equal(t, sc.ServiceID.String(), rid)
		require.Equal(t, f.projA.ID, *project)
		var meta map[string]any
		require.NoError(t, json.Unmarshal(raw, &meta))
		require.Equal(t, "api", meta["service"])
		require.Equal(t, "shop", meta["project"])
		require.Equal(t, "production", meta["environment"])
		require.Equal(t, services.ExtPurposeDeploy, meta["purpose"])
		require.Equal(t, sc.DeploymentID, meta["deployment"])
		require.Equal(t, "dev@example.com", meta["startedBy"])
		require.Equal(t, []any{"doppler:dp#API_KEY", "doppler:dp#DATABASE_URL"}, meta["references"])
		require.EqualValues(t, 2, meta["count"])
		failed, _ := meta["failed"].([]any)
		require.Len(t, failed, 3)
		for _, secret := range []string{"key-0123456789", "pw-from-doppler", dopplerServiceToken} {
			require.NotContains(t, string(raw), secret, "the access log never holds a value or the credential")
		}
	}
	require.Equal(t, 1, got, "one event per operation")
}

func TestAccessLogOfADeniedFetchAndOfAUserOperation(t *testing.T) {
	f := newScopeFixture(t)
	bg := context.Background()
	f.create(ScopeSelected, grantOf(f.projA, nil))

	// An operation that runs for a signed-in user records that user as the actor.
	fetcher := f.svc.Fetcher(f.scope(f.projB, f.envB, services.ExtPurposeDatabase))
	_, err := fetcher.Fetch(f.ctx, ref(t, "doppler:dp#API_KEY"))
	require.Equal(t, "integration_not_allowed", f.code(err))
	services.FlushFetcher(f.ctx, fetcher)

	var action, actorType, label string
	var raw []byte
	require.NoError(t, f.pool.QueryRow(bg, "SELECT action, actor_type, actor_label, metadata FROM audit_events WHERE action LIKE 'secret.fetch%'").Scan(&action, &actorType, &label, &raw))
	require.Equal(t, ActionFetchDenied, action)
	require.Equal(t, "user", actorType)
	require.Equal(t, "dev@example.com", label)
	var meta map[string]any
	require.NoError(t, json.Unmarshal(raw, &meta))
	require.Equal(t, []any{"doppler:dp#API_KEY"}, meta["denied"])
	require.Equal(t, services.ExtPurposeDatabase, meta["purpose"])
	require.NotContains(t, meta, "references")

	// A fetcher that read nothing records nothing.
	services.FlushFetcher(bg, f.svc.Fetcher(f.scope(f.projA, f.envAProd, services.ExtPurposeTask)))
	var n int
	require.NoError(t, f.pool.QueryRow(bg, "SELECT count(*) FROM audit_events WHERE action LIKE 'secret.fetch%'").Scan(&n))
	require.Equal(t, 1, n)
}

func TestFlushSurvivesACancelledOperation(t *testing.T) {
	f := newScopeFixture(t)
	f.create(ScopeTeam)
	ctx, cancel := context.WithCancel(f.ctx)
	fetcher := f.svc.Fetcher(f.scope(f.projA, f.envAProd, services.ExtPurposeMask))
	_, err := fetcher.Fetch(ctx, ref(t, "doppler:dp#API_KEY"))
	require.NoError(t, err)
	cancel() // the operation ended (a timeout) before the access log was written
	services.FlushFetcher(ctx, fetcher)
	var n int
	require.NoError(t, f.pool.QueryRow(context.Background(), "SELECT count(*) FROM audit_events WHERE action = 'secret.fetch'").Scan(&n))
	require.Equal(t, 1, n)
}

func TestTestEndpointAuditsTheReferenceButNeverTheValue(t *testing.T) {
	f := newScopeFixture(t)
	m := f.create(ScopeTeam)
	res, err := f.svc.Test(f.ctx, m.ID, TestInput{Key: "API_KEY"})
	require.NoError(t, err)
	require.True(t, res.OK, res.Message)
	require.True(t, res.Read)
	require.NotContains(t, res.Message, "key-0123456789")

	var raw []byte
	require.NoError(t, f.pool.QueryRow(context.Background(), "SELECT metadata FROM audit_events WHERE action = 'secret_manager.test'").Scan(&raw))
	require.Contains(t, string(raw), "doppler:dp#API_KEY")
	require.NotContains(t, string(raw), "key-0123456789")

	// Admin tests are not subject to scoping: they check the credential itself.
	scoped := f.create2("scoped", ScopeSelected, grantOf(f.projB, nil))
	res, err = f.svc.Test(f.ctx, scoped.ID, TestInput{Key: "API_KEY"})
	require.NoError(t, err)
	require.True(t, res.OK, res.Message)
}

func (f *scopeFixture) create2(name, scope string, grants ...ScopeGrant) SecretManager {
	f.t.Helper()
	m, err := f.svc.Create(f.ctx, Input{
		Name: name, Kind: KindDoppler, Config: SecretManagerConfig{SiteURL: f.fd.srv.URL}, Secret: dopplerServiceToken, Scope: scope, Grants: grants,
	})
	require.NoError(f.t, err)
	return m
}

func TestAvailabilityQueryMatchesTheFetchPath(t *testing.T) {
	f := newScopeFixture(t)
	bg := context.Background()
	f.create(ScopeSelected, grantOf(f.projA, &f.envAStg))
	f.create2("everyone", ScopeTeam)
	access := func(p db.Project, e db.Environment) map[string]bool {
		rows, err := f.q.ListSecretIntegrationAccess(bg, db.ListSecretIntegrationAccessParams{WorkspaceID: f.ws.ID, ProjectID: p.ID, EnvironmentID: e.ID})
		require.NoError(t, err)
		out := map[string]bool{}
		for _, r := range rows {
			require.Equal(t, KindDoppler, r.Kind)
			out[r.Name] = r.Allowed
		}
		return out
	}
	require.Equal(t, map[string]bool{"dp": true, "everyone": true}, access(f.projA, f.envAStg))
	require.Equal(t, map[string]bool{"dp": false, "everyone": true}, access(f.projA, f.envAProd))
	require.Equal(t, map[string]bool{"dp": false, "everyone": true}, access(f.projB, f.envB))
}
