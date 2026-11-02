package projects_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/events"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/prodguard"
	"github.com/nebulactrl/nebulactrl/internal/prodguard/prodguardtest"
	"github.com/nebulactrl/nebulactrl/internal/projects"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/testutil"
)

// crud is a small rig for the project use cases: one team, one owner.
type crud struct {
	t      *testing.T
	pool   *pgxpool.Pool
	q      *db.Queries
	bus    *events.Bus
	svc    *projects.Service
	ws     db.Workspace
	user   db.CreateUserRow
	person context.Context
	mcp    context.Context
}

func (c *crud) as(typ string, scopes []string, projectIDs []uuid.UUID) context.Context {
	p := &auth.Principal{Type: typ, UserID: c.user.ID, WorkspaceID: c.ws.ID, Role: auth.RoleOwner, Label: "tester", Scopes: scopes, ProjectIDs: projectIDs}
	if typ != auth.TypeUser {
		tok := uuid.New()
		p.TokenID = &tok
	}
	return auth.WithPrincipal(context.Background(), p)
}

func newCrud(t *testing.T) *crud {
	t.Helper()
	pool := testutil.NewPool(t)
	q := db.New(pool)
	bg := context.Background()
	ws, err := q.CreateWorkspace(bg, "w")
	require.NoError(t, err)
	u, err := q.CreateUser(bg, db.CreateUserParams{WorkspaceID: ws.ID, Email: "a@example.com", Name: "Ada", PasswordHash: "x"})
	require.NoError(t, err)
	box, err := secretbox.New(bytes.Repeat([]byte{2}, 32))
	require.NoError(t, err)
	prodguard.New(prodguard.Deps{Pool: pool, Box: box}).Install()
	t.Cleanup(prodguardtest.Uninstall)

	bus := events.NewBus()
	c := &crud{t: t, pool: pool, q: q, bus: bus, svc: projects.NewService(pool, bus), ws: ws, user: u}
	c.person = c.as(auth.TypeUser, policy.AllScopes, nil)
	c.mcp = c.as(auth.TypeMCP, policy.AllScopes, nil)
	return c
}

func (c *crud) project(name string) db.Project {
	c.t.Helper()
	p, err := c.svc.Create(c.person, name, "")
	require.NoError(c.t, err)
	row, err := c.q.GetProject(context.Background(), db.GetProjectParams{ID: p.ID, WorkspaceID: c.ws.ID})
	require.NoError(c.t, err)
	return row
}

func (c *crud) count(sql string, args ...any) int {
	c.t.Helper()
	var n int
	require.NoError(c.t, c.pool.QueryRow(context.Background(), sql, args...).Scan(&n))
	return n
}

func (c *crud) exec(sql string, args ...any) {
	c.t.Helper()
	_, err := c.pool.Exec(context.Background(), sql, args...)
	require.NoError(c.t, err)
}

// service puts a live service into the project's production environment.
func (c *crud) service(p db.Project, slug string) db.Service {
	c.t.Helper()
	envs, err := c.q.ListEnvironments(context.Background(), p.ID)
	require.NoError(c.t, err)
	sv, err := c.q.InsertService(context.Background(), db.InsertServiceParams{
		ID: uuid.New(), EnvironmentID: envs[0].ID, Name: slug, Slug: slug, Kind: "app", SourceType: "image", Config: json.RawMessage(`{"image":"nginx:1"}`),
	})
	require.NoError(c.t, err)
	return sv
}

func drain(feed <-chan events.Event) []events.Event {
	var out []events.Event
	for {
		select {
		case e := <-feed:
			out = append(out, e)
		default:
			return out
		}
	}
}

func (c *crud) feed() <-chan events.Event {
	ctx, cancel := context.WithCancel(context.Background())
	c.t.Cleanup(cancel)
	return c.bus.Subscribe(ctx)
}

func requireCode(t *testing.T, err error, status int, code string) {
	t.Helper()
	ae, ok := apierr.As(err)
	require.True(t, ok, "not an API error: %v", err)
	require.Equal(t, status, ae.Status, ae.Message)
	require.Equal(t, code, ae.Code)
}

func TestCreateMakesTheProjectAndItsProductionEnvironment(t *testing.T) {
	c := newCrud(t)
	feed := c.feed()

	p, err := c.svc.Create(c.person, "  My Shop!  ", "sells things")
	require.NoError(t, err)
	require.Equal(t, "My Shop!", p.Name, "the name is trimmed")
	require.Equal(t, "my-shop", p.Slug)
	require.Equal(t, "sells things", p.Description)
	require.False(t, p.System)

	envs, err := c.q.ListEnvironments(context.Background(), p.ID)
	require.NoError(t, err)
	require.Len(t, envs, 1)
	require.Equal(t, "production", envs[0].Slug)
	require.True(t, envs[0].IsProduction)

	require.Equal(t, 1, c.count(`SELECT count(*) FROM audit_events WHERE action = 'project.create' AND project_id = $1 AND metadata @> '{"name":"My Shop!","slug":"my-shop"}'`, p.ID))
	require.Equal(t, 1, c.count(`SELECT count(*) FROM audit_events WHERE action = 'environment.create' AND project_id = $1 AND resource_id = $2`, p.ID, envs[0].ID.String()))

	require.Equal(t, []events.Event{{Type: events.TypeProjectCreated, ProjectID: p.ID.String(), ResourceID: p.ID.String(), WorkspaceID: c.ws.ID.String()}}, drain(feed))
}

func TestCreateGivesEachProjectOfTheSameNameItsOwnSlug(t *testing.T) {
	c := newCrud(t)
	slugs := map[string]bool{}
	for range 3 {
		p, err := c.svc.Create(c.person, "Shop", "")
		require.NoError(t, err)
		slugs[p.Slug] = true
	}
	require.Equal(t, map[string]bool{"shop": true, "shop-2": true, "shop-3": true}, slugs)

	p, err := c.svc.Create(c.person, "日本語", "")
	require.NoError(t, err)
	require.Equal(t, "project", p.Slug, "a name without letters or digits falls back to 'project'")
}

func TestCreateRefusals(t *testing.T) {
	c := newCrud(t)
	feed := c.feed()

	_, err := c.svc.Create(c.person, "   ", "")
	requireCode(t, err, http.StatusUnprocessableEntity, "validation_failed")

	_, err = c.svc.Create(c.as(auth.TypeToken, []string{policy.ScopeProjectsRead}, nil), "X", "")
	requireCode(t, err, http.StatusForbidden, "forbidden")

	_, err = c.svc.Create(c.as(auth.TypeToken, policy.AllScopes, []uuid.UUID{uuid.New()}), "X", "")
	requireCode(t, err, http.StatusForbidden, "forbidden")

	_, err = c.svc.Create(context.Background(), "X", "")
	requireCode(t, err, http.StatusUnauthorized, "unauthorized")

	require.Zero(t, c.count(`SELECT count(*) FROM projects`))
	require.Empty(t, drain(feed))
}

func TestListShowsTheTeamsProjectsNewestFirstInPages(t *testing.T) {
	c := newCrud(t)
	var created []string
	for _, n := range []string{"One", "Two", "Three"} {
		created = append(created, c.project(n).ID.String())
	}
	// Another team's project and the platform project never show up.
	other, err := c.q.CreateWorkspace(context.Background(), "other")
	require.NoError(t, err)
	_, err = c.q.CreateProject(context.Background(), db.CreateProjectParams{WorkspaceID: other.ID, Name: "Foreign", Slug: "foreign"})
	require.NoError(t, err)
	platform, err := c.q.CreateProject(context.Background(), db.CreateProjectParams{WorkspaceID: c.ws.ID, Name: "NebulaCtrl platform", Slug: "platform"})
	require.NoError(t, err)
	c.exec(`UPDATE projects SET system = true WHERE id = $1`, platform.ID)

	first, next, err := c.svc.List(c.person, "", 2)
	require.NoError(t, err)
	require.Len(t, first, 2)
	require.Equal(t, created[2], first[0].ID.String(), "newest first")
	require.Equal(t, created[1], first[1].ID.String())
	require.NotEmpty(t, next)

	rest, next, err := c.svc.List(c.person, next, 2)
	require.NoError(t, err)
	require.Len(t, rest, 1)
	require.Equal(t, created[0], rest[0].ID.String())
	require.Empty(t, next, "the last page has no cursor")

	all, next, err := c.svc.List(c.person, "", 3)
	require.NoError(t, err)
	require.Len(t, all, 3)
	require.Empty(t, next, "exactly a full page is not followed by an empty one")
}

func TestListHonoursProjectRestrictionScopesAndCursors(t *testing.T) {
	c := newCrud(t)
	a := c.project("A")
	c.project("B")

	only := c.as(auth.TypeToken, policy.AllScopes, []uuid.UUID{a.ID})
	got, _, err := c.svc.List(only, "", 10)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, a.ID, got[0].ID)

	none := c.as(auth.TypeToken, policy.AllScopes, []uuid.UUID{})
	got, _, err = c.svc.List(none, "", 10)
	require.NoError(t, err)
	require.Empty(t, got, "an empty restriction sees nothing, not everything")

	_, _, err = c.svc.List(c.as(auth.TypeToken, []string{policy.ScopeProjectsWrite}, nil), "", 10)
	requireCode(t, err, http.StatusForbidden, "forbidden")

	_, _, err = c.svc.List(c.person, "not-a-cursor", 10)
	requireCode(t, err, http.StatusBadRequest, "invalid_cursor")
}

func TestGetIsScopedToTheTeamAndTheCallersProjects(t *testing.T) {
	c := newCrud(t)
	p := c.project("Shop")
	got, err := c.svc.Get(c.person, p.ID)
	require.NoError(t, err)
	require.Equal(t, p.ID, got.ID)
	require.Equal(t, "Shop", got.Name)

	_, err = c.svc.Get(c.person, uuid.New())
	requireCode(t, err, http.StatusNotFound, "not_found")

	foreign, err := c.q.CreateWorkspace(context.Background(), "foreign")
	require.NoError(t, err)
	p2 := &auth.Principal{Type: auth.TypeUser, UserID: uuid.New(), WorkspaceID: foreign.ID, Role: auth.RoleOwner, Scopes: policy.AllScopes}
	_, err = c.svc.Get(auth.WithPrincipal(context.Background(), p2), p.ID)
	requireCode(t, err, http.StatusNotFound, "not_found")

	_, err = c.svc.Get(c.as(auth.TypeToken, policy.AllScopes, []uuid.UUID{uuid.New()}), p.ID)
	requireCode(t, err, http.StatusNotFound, "not_found")

	_, err = c.svc.Get(c.as(auth.TypeToken, []string{policy.ScopeProjectsWrite}, nil), p.ID)
	requireCode(t, err, http.StatusForbidden, "forbidden")
}

func TestThePlatformProjectIsForAdministrators(t *testing.T) {
	c := newCrud(t)
	p := c.project("NebulaCtrl platform")
	c.exec(`UPDATE projects SET system = true WHERE id = $1`, p.ID)

	member := c.as(auth.TypeToken, []string{policy.ScopeProjectsRead, policy.ScopeProjectsWrite}, nil)
	_, err := c.svc.Get(member, p.ID)
	requireCode(t, err, http.StatusNotFound, "not_found")

	got, err := c.svc.Get(c.person, p.ID)
	require.NoError(t, err)
	require.True(t, got.System)

	name := "renamed"
	_, err = c.svc.Update(c.person, p.ID, projects.UpdateInput{Name: &name})
	requireCode(t, err, http.StatusConflict, "system_project")
	requireCode(t, c.svc.Delete(c.person, p.ID, projects.DeleteInput{}), http.StatusConflict, "system_project")
	requireCode(t, c.svc.Delete(c.person, p.ID, projects.DeleteInput{Cascade: true, Confirm: p.Name}), http.StatusConflict, "system_project")
	_, err = c.svc.DeleteCascade(c.person, p.ID)
	requireCode(t, err, http.StatusConflict, "system_project")
	_, err = c.svc.Plan(member, p.ID)
	requireCode(t, err, http.StatusNotFound, "not_found")
	require.Equal(t, 1, c.count(`SELECT count(*) FROM projects WHERE id = $1`, p.ID))
}

func TestUpdateRenamesWithoutChangingTheSlug(t *testing.T) {
	c := newCrud(t)
	p := c.project("Shop")
	feed := c.feed()
	name, desc := "  Store  ", "all of it"

	got, err := c.svc.Update(c.person, p.ID, projects.UpdateInput{Name: &name, Description: &desc})
	require.NoError(t, err)
	require.Equal(t, "Store", got.Name)
	require.Equal(t, "all of it", got.Description)
	require.Equal(t, "shop", got.Slug, "the slug is stable")
	require.Equal(t, 1, c.count(`SELECT count(*) FROM audit_events WHERE action = 'project.update' AND project_id = $1 AND metadata = '{"name":"Store"}'`, p.ID))
	require.Equal(t, []events.Event{{Type: events.TypeProjectUpdated, ProjectID: p.ID.String(), ResourceID: p.ID.String(), WorkspaceID: c.ws.ID.String()}}, drain(feed))

	// Leaving a field out keeps it.
	desc2 := ""
	got, err = c.svc.Update(c.person, p.ID, projects.UpdateInput{Description: &desc2})
	require.NoError(t, err)
	require.Equal(t, "Store", got.Name)
	require.Empty(t, got.Description, "an explicit empty description clears it")
}

func TestUpdateRefusals(t *testing.T) {
	c := newCrud(t)
	p := c.project("Shop")
	empty, sp := "", "   "
	bad, target := "everywhere", "target"

	for _, in := range map[string]projects.UpdateInput{
		"empty name":                    {Name: &empty},
		"blank name":                    {Name: &sp},
		"unknown build location":        {BuildLocation: &bad},
		"build server without location": {BuildServerID: new(uuid.New())},
		"registry without location":     {RegistryID: new(uuid.New())},
	} {
		_, err := c.svc.Update(c.person, p.ID, in)
		requireCode(t, err, http.StatusUnprocessableEntity, "validation_failed")
	}
	_, err := c.svc.Update(c.person, p.ID, projects.UpdateInput{BuildLocation: &target, BuildServerID: new(uuid.New())})
	requireCode(t, err, http.StatusUnprocessableEntity, "build_server_not_found")
	_, err = c.svc.Update(c.person, p.ID, projects.UpdateInput{BuildLocation: &target, RegistryID: new(uuid.New())})
	requireCode(t, err, http.StatusUnprocessableEntity, "registry_not_found")

	srv, err := c.q.CreateServer(context.Background(), db.CreateServerParams{WorkspaceID: c.ws.ID, Name: "plain"})
	require.NoError(t, err)
	bs := "build_server"
	_, err = c.svc.Update(c.person, p.ID, projects.UpdateInput{BuildLocation: &bs, BuildServerID: &srv.ID})
	requireCode(t, err, http.StatusUnprocessableEntity, "not_a_build_server")

	_, err = c.svc.Update(c.person, uuid.New(), projects.UpdateInput{Name: &sp})
	requireCode(t, err, http.StatusNotFound, "not_found")
	_, err = c.svc.Update(c.as(auth.TypeToken, []string{policy.ScopeProjectsRead}, nil), p.ID, projects.UpdateInput{Name: &target})
	requireCode(t, err, http.StatusForbidden, "forbidden")

	got, err := c.svc.Get(c.person, p.ID)
	require.NoError(t, err)
	require.Equal(t, "Shop", got.Name, "no refused update changed anything")
	require.Equal(t, "inherit", got.BuildLocation)
}

func TestUpdateReplacesTheBuildPlacementAsAUnit(t *testing.T) {
	c := newCrud(t)
	p := c.project("Shop")
	srv, err := c.q.CreateServer(context.Background(), db.CreateServerParams{WorkspaceID: c.ws.ID, Name: "builder"})
	require.NoError(t, err)
	c.exec(`UPDATE servers SET build_enabled = true WHERE id = $1`, srv.ID)

	bs := "build_server"
	got, err := c.svc.Update(c.person, p.ID, projects.UpdateInput{BuildLocation: &bs, BuildServerID: &srv.ID})
	require.NoError(t, err)
	require.Equal(t, "build_server", got.BuildLocation)
	require.Equal(t, &srv.ID, got.BuildServerID)
	require.Nil(t, got.RegistryID)
	require.Equal(t, 1, c.count(`SELECT count(*) FROM audit_events WHERE action = 'project.update' AND metadata @> $1::jsonb`,
		fmt.Sprintf(`{"buildLocation":"build_server","buildServerId":%q}`, srv.ID)))

	// Setting only the location clears the pinned server: the placement is a unit.
	target := "target"
	got, err = c.svc.Update(c.person, p.ID, projects.UpdateInput{BuildLocation: &target})
	require.NoError(t, err)
	require.Equal(t, "target", got.BuildLocation)
	require.Nil(t, got.BuildServerID)
	require.Equal(t, "Shop", got.Name)

	inherit := "inherit"
	got, err = c.svc.Update(c.person, p.ID, projects.UpdateInput{BuildLocation: &inherit})
	require.NoError(t, err)
	require.Equal(t, "inherit", got.BuildLocation)
}

func TestDeleteRemovesAnEmptyProjectWithItsEnvironments(t *testing.T) {
	c := newCrud(t)
	p := c.project("Shop")
	feed := c.feed()

	require.NoError(t, c.svc.Delete(c.person, p.ID, projects.DeleteInput{}))
	require.Zero(t, c.count(`SELECT count(*) FROM projects WHERE id = $1`, p.ID))
	require.Zero(t, c.count(`SELECT count(*) FROM environments WHERE project_id = $1`, p.ID))
	require.Equal(t, 1, c.count(`SELECT count(*) FROM audit_events WHERE action = 'project.delete' AND resource_id = $1 AND metadata @> '{"name":"Shop","slug":"shop"}'`, p.ID.String()))
	require.Equal(t, []events.Event{{Type: events.TypeProjectDeleted, ProjectID: p.ID.String(), ResourceID: p.ID.String(), WorkspaceID: c.ws.ID.String()}}, drain(feed))

	requireCode(t, c.svc.Delete(c.person, p.ID, projects.DeleteInput{}), http.StatusNotFound, "not_found")
}

func TestDeleteRefusesAProjectThatStillHasServices(t *testing.T) {
	c := newCrud(t)
	p := c.project("Shop")
	sv := c.service(p, "web")

	requireCode(t, c.svc.Delete(c.person, p.ID, projects.DeleteInput{}), http.StatusConflict, "project_not_empty")
	require.Equal(t, 1, c.count(`SELECT count(*) FROM projects WHERE id = $1`, p.ID))

	// A soft-deleted service no longer blocks it.
	require.NoError(t, c.q.SoftDeleteService(context.Background(), sv.ID))
	require.NoError(t, c.svc.Delete(c.person, p.ID, projects.DeleteInput{}))
}

func TestDeleteChecksScopeAndRestriction(t *testing.T) {
	c := newCrud(t)
	p := c.project("Shop")
	requireCode(t, c.svc.Delete(c.as(auth.TypeToken, []string{policy.ScopeProjectsRead}, nil), p.ID, projects.DeleteInput{}), http.StatusForbidden, "forbidden")
	requireCode(t, c.svc.Delete(c.as(auth.TypeToken, policy.AllScopes, []uuid.UUID{uuid.New()}), p.ID, projects.DeleteInput{}), http.StatusNotFound, "not_found")
	require.Equal(t, 1, c.count(`SELECT count(*) FROM projects WHERE id = $1`, p.ID))
}

func TestDeletingAnEmptyProductionProjectAsAnAgentNeedsAnApproval(t *testing.T) {
	c := newCrud(t)
	p := c.project("Shop")

	err := c.svc.Delete(c.mcp, p.ID, projects.DeleteInput{})
	ar, ok := prodguard.AsRequired(err)
	require.True(t, ok, "got %v", err)
	require.Contains(t, ar.Plan.Summary, "Delete the project Shop and its production environment")
	require.True(t, ar.Plan.Irreversible)
	require.Equal(t, 1, c.count(`SELECT count(*) FROM projects WHERE id = $1`, p.ID), "nothing is deleted before the approval")

	// A project with services is refused before anybody is asked to approve.
	c.service(p, "web")
	err = c.svc.Delete(c.mcp, p.ID, projects.DeleteInput{})
	requireCode(t, err, http.StatusConflict, "project_not_empty")
}

func TestDeletingAnApprovedEmptyProjectGoesThrough(t *testing.T) {
	c := newCrud(t)
	p := c.project("Shop")

	err := c.svc.Delete(c.mcp, p.ID, projects.DeleteInput{})
	ar, ok := prodguard.AsRequired(err)
	require.True(t, ok, "got %v", err)
	_, err = prodguard.Installed().Approve(c.person, ar.ApprovalID)
	require.NoError(t, err)

	require.NoError(t, c.svc.Delete(c.mcp, p.ID, projects.DeleteInput{}))
	require.Zero(t, c.count(`SELECT count(*) FROM projects WHERE id = $1`, p.ID))
}

func TestDeletingAProjectWithoutAProductionEnvironmentNeedsNoApproval(t *testing.T) {
	c := newCrud(t)
	p := c.project("Shop")
	c.exec(`UPDATE environments SET is_production = false WHERE project_id = $1`, p.ID)
	require.NoError(t, c.svc.Delete(c.mcp, p.ID, projects.DeleteInput{}))
}

func TestCascadeDeleteWithoutItsWiringIsRefused(t *testing.T) {
	c := newCrud(t)
	p := c.project("Shop")
	_, err := c.svc.DeleteCascade(c.person, p.ID)
	require.ErrorContains(t, err, "not configured")
	err = c.svc.Delete(c.person, p.ID, projects.DeleteInput{Cascade: true, Confirm: "Shop"})
	require.ErrorContains(t, err, "not configured")
	require.Equal(t, 1, c.count(`SELECT count(*) FROM projects WHERE id = $1`, p.ID))
}

// cascadeFakes plugs recording stand-ins into the cascade.
type cascadeFakes struct {
	deletedServices []string
	tornDown        []uuid.UUID
	announced       int
	deleteService   func(context.Context, uuid.UUID, string) error
	teardown        func(context.Context, uuid.UUID) error
	detach          func(context.Context, pgx.Tx, uuid.UUID) (int, func(), error)
}

func (c *crud) wireCascade(f *cascadeFakes) {
	c.svc.SetCascade(projects.CascadeDeps{
		DeleteService: func(ctx context.Context, id uuid.UUID, slug string) error {
			f.deletedServices = append(f.deletedServices, slug)
			if f.deleteService != nil {
				return f.deleteService(ctx, id, slug)
			}
			return c.q.SoftDeleteService(ctx, id)
		},
		TeardownPreview: func(ctx context.Context, id uuid.UUID) error {
			f.tornDown = append(f.tornDown, id)
			if f.teardown != nil {
				return f.teardown(ctx, id)
			}
			return nil
		},
		DetachStatusPages: func(ctx context.Context, tx pgx.Tx, id uuid.UUID) (int, func(), error) {
			if f.detach != nil {
				return f.detach(ctx, tx, id)
			}
			return 0, func() { f.announced++ }, nil
		},
	})
}

func TestDeleteCascadeRemovesTheProjectAndAnnouncesEveryEnvironment(t *testing.T) {
	c := newCrud(t)
	p := c.project("Shop")
	staging, err := c.q.CreateEnvironment(context.Background(), db.CreateEnvironmentParams{ProjectID: p.ID, Name: "staging", Slug: "staging"})
	require.NoError(t, err)
	c.service(p, "web")
	c.service(p, "worker")
	f := &cascadeFakes{}
	c.wireCascade(f)
	feed := c.feed()

	plan, err := c.svc.DeleteCascade(c.person, p.ID)
	require.NoError(t, err)
	require.Equal(t, 2, plan.Services, "the plan is what the project held before")
	require.Len(t, plan.Environments, 2)
	require.ElementsMatch(t, []string{"web", "worker"}, f.deletedServices)
	require.Equal(t, 1, f.announced, "the status page announcement runs after the commit")
	require.Zero(t, c.count(`SELECT count(*) FROM projects WHERE id = $1`, p.ID))
	require.Zero(t, c.count(`SELECT count(*) FROM environments WHERE project_id = $1`, p.ID))

	var types []string
	var envIDs []string
	for _, e := range drain(feed) {
		types = append(types, e.Type)
		if e.Type == events.TypeEnvironmentDeleted {
			envIDs = append(envIDs, e.EnvironmentID)
			require.Equal(t, p.ID.String(), e.ProjectID)
			require.Equal(t, c.ws.ID.String(), e.WorkspaceID)
		}
	}
	require.ElementsMatch(t, []string{events.TypeEnvironmentDeleted, events.TypeEnvironmentDeleted, events.TypeProjectDeleted}, types)
	require.Len(t, envIDs, 2)
	require.Contains(t, envIDs, staging.ID.String())
}

func TestDeleteCascadeToleratesAServiceThatIsAlreadyGone(t *testing.T) {
	c := newCrud(t)
	p := c.project("Shop")
	c.service(p, "web")
	f := &cascadeFakes{deleteService: func(ctx context.Context, id uuid.UUID, _ string) error {
		require.NoError(t, c.q.SoftDeleteService(ctx, id))
		return apierr.NotFound("service") // somebody else deleted it a moment ago
	}}
	c.wireCascade(f)
	_, err := c.svc.DeleteCascade(c.person, p.ID)
	require.NoError(t, err)
	require.Zero(t, c.count(`SELECT count(*) FROM projects WHERE id = $1`, p.ID))
}

func TestDeleteCascadeStopsAtTheFirstFailureAndCanBeRepeated(t *testing.T) {
	c := newCrud(t)
	p := c.project("Shop")
	c.service(p, "web")
	boom := errors.New("agent unreachable")

	f := &cascadeFakes{deleteService: func(context.Context, uuid.UUID, string) error { return boom }}
	c.wireCascade(f)
	_, err := c.svc.DeleteCascade(c.person, p.ID)
	require.ErrorIs(t, err, boom)
	require.ErrorContains(t, err, "delete service web")
	require.Equal(t, 1, c.count(`SELECT count(*) FROM projects WHERE id = $1`, p.ID))

	// An API error passes through untouched.
	f.deleteService = func(context.Context, uuid.UUID, string) error { return apierr.Conflict("busy", "service is busy") }
	_, err = c.svc.DeleteCascade(c.person, p.ID)
	requireCode(t, err, http.StatusConflict, "busy")

	// The next attempt succeeds.
	f.deleteService = nil
	_, err = c.svc.DeleteCascade(c.person, p.ID)
	require.NoError(t, err)
}

func TestDeleteCascadeReportsAServiceThatWasNotRemoved(t *testing.T) {
	c := newCrud(t)
	p := c.project("Shop")
	c.service(p, "web")
	f := &cascadeFakes{deleteService: func(context.Context, uuid.UUID, string) error { return nil }} // claims success, deletes nothing
	c.wireCascade(f)

	_, err := c.svc.DeleteCascade(c.person, p.ID)
	requireCode(t, err, http.StatusConflict, "project_not_empty")
	require.Equal(t, 1, c.count(`SELECT count(*) FROM projects WHERE id = $1`, p.ID), "nothing of the project is removed while a service is left")
	require.Equal(t, 1, c.count(`SELECT count(*) FROM environments WHERE project_id = $1`, p.ID))
}

func TestDeleteCascadeTearsDownPreviewsFirstAndReportsFailures(t *testing.T) {
	c := newCrud(t)
	p := c.project("Shop")
	envs, err := c.q.ListEnvironments(context.Background(), p.ID)
	require.NoError(t, err)
	pr := int32(3)
	conn := uuid.New()
	pvEnv, err := c.q.InsertPreviewEnvironment(context.Background(), db.InsertPreviewEnvironmentParams{
		ProjectID: p.ID, Name: "PR #3", Slug: "pr-3", ParentEnvironmentID: &envs[0].ID, PrNumber: &pr, PrTitle: "Fix", ConnectionID: &conn,
	})
	require.NoError(t, err)
	pv, err := c.q.InsertPreview(context.Background(), db.InsertPreviewParams{
		WorkspaceID: c.ws.ID, ProjectID: p.ID, EnvironmentID: pvEnv.ID, BaseEnvironmentID: envs[0].ID, ConnectionID: conn,
		Provider: "github", Repo: "acme/shop", PrNumber: pr, AffectedServiceIds: []uuid.UUID{},
	})
	require.NoError(t, err)

	boom := errors.New("preview cleanup failed")
	f := &cascadeFakes{teardown: func(context.Context, uuid.UUID) error { return boom }}
	c.wireCascade(f)
	_, err = c.svc.DeleteCascade(c.person, p.ID)
	require.ErrorIs(t, err, boom)
	require.ErrorContains(t, err, "delete preview")
	require.Equal(t, []uuid.UUID{pv.ID}, f.tornDown)
	require.Empty(t, f.deletedServices, "no service is touched when a preview could not go")

	f.teardown = func(ctx context.Context, id uuid.UUID) error {
		if _, err := c.pool.Exec(ctx, `DELETE FROM previews WHERE id = $1`, id); err != nil {
			return err
		}
		_, err := c.pool.Exec(ctx, `DELETE FROM environments WHERE id = $1`, pvEnv.ID)
		return err
	}
	_, err = c.svc.DeleteCascade(c.person, p.ID)
	require.NoError(t, err)
}

func TestDeleteCascadeRollsBackWhenDetachingStatusPagesFails(t *testing.T) {
	c := newCrud(t)
	p := c.project("Shop")
	boom := errors.New("status page broke")
	f := &cascadeFakes{detach: func(context.Context, pgx.Tx, uuid.UUID) (int, func(), error) { return 0, nil, boom }}
	c.wireCascade(f)

	_, err := c.svc.DeleteCascade(c.person, p.ID)
	require.ErrorIs(t, err, boom)
	require.ErrorContains(t, err, "delete project")
	require.Equal(t, 1, c.count(`SELECT count(*) FROM projects WHERE id = $1`, p.ID))
	require.Equal(t, 1, c.count(`SELECT count(*) FROM environments WHERE project_id = $1`, p.ID))
}

func TestCascadeIsRefusedWhileAReplicaExists(t *testing.T) {
	c := newCrud(t)
	p := c.project("Shop")
	primary := c.service(p, "db")
	replica := c.service(p, "db-replica")
	envs, err := c.q.ListEnvironments(context.Background(), p.ID)
	require.NoError(t, err)
	c.exec(`INSERT INTO database_replicas (service_id, workspace_id, environment_id, primary_service_id, slot_name, repl_user) VALUES ($1, $2, $3, $4, 'slot', 'repl')`,
		replica.ID, c.ws.ID, envs[0].ID, primary.ID)
	f := &cascadeFakes{}
	c.wireCascade(f)

	_, err = c.svc.DeleteCascade(c.person, p.ID)
	requireCode(t, err, http.StatusConflict, "project_has_replicas")
	require.Empty(t, f.deletedServices, "refused before anything is deleted")
	require.Equal(t, 1, c.count(`SELECT count(*) FROM projects WHERE id = $1`, p.ID))
}

// ---- cursors and helpers ----

func TestCursorRoundTrips(t *testing.T) {
	at := time.Date(2026, 5, 6, 7, 8, 9, 123456000, time.UTC)
	id := uuid.New()
	cur := projects.EncodeCursor(at, id)
	gotAt, gotID, ok, err := projects.DecodeCursor(cur)
	require.NoError(t, err)
	require.True(t, ok)
	require.True(t, gotAt.Equal(at))
	require.Equal(t, id, gotID)

	_, _, ok, err = projects.DecodeCursor("")
	require.NoError(t, err)
	require.False(t, ok, "an empty cursor means the first page")
	_, _, _, err = projects.DecodeCursor("%%%")
	requireCode(t, err, http.StatusBadRequest, "invalid_cursor")
}

func TestIsUniqueViolation(t *testing.T) {
	c := newCrud(t)
	p := c.project("Shop")
	_, err := c.pool.Exec(context.Background(), `INSERT INTO projects (workspace_id, name, slug) VALUES ($1, 'Dup', $2)`, c.ws.ID, p.Slug)
	require.Error(t, err)
	require.True(t, projects.IsUniqueViolation(err))
	require.True(t, projects.IsUniqueViolation(fmt.Errorf("wrapped: %w", err)))

	_, err = c.pool.Exec(context.Background(), `INSERT INTO projects (workspace_id, name, slug) VALUES ($1, NULL, 'x')`, c.ws.ID)
	require.Error(t, err)
	require.False(t, projects.IsUniqueViolation(err), "a not-null violation is not a unique violation")
	require.False(t, projects.IsUniqueViolation(errors.New("nope")))
	require.False(t, projects.IsUniqueViolation(nil))
}

// ---- HTTP ----

type httpCrud struct {
	*crud
	router chi.Router
}

func newHTTPCrud(t *testing.T, scopes ...string) *httpCrud {
	t.Helper()
	c := newCrud(t)
	if len(scopes) == 0 {
		scopes = policy.AllScopes
	}
	router := chi.NewRouter()
	router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req.WithContext(auth.WithPrincipal(req.Context(), &auth.Principal{
				Type: auth.TypeUser, UserID: c.user.ID, WorkspaceID: c.ws.ID, Role: auth.RoleOwner, Scopes: scopes,
			})))
		})
	})
	cfg := huma.DefaultConfig("test", "1")
	cfg.CreateHooks = nil
	projects.Register(humachi.New(router, cfg), c.svc)
	return &httpCrud{crud: c, router: router}
}

func (h *httpCrud) do(method, path string, body any) (int, map[string]any) {
	h.t.Helper()
	var rd *bytes.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		require.NoError(h.t, err)
		rd = bytes.NewReader(raw)
	} else {
		rd = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	h.router.ServeHTTP(rec, req)
	var m map[string]any
	if rec.Body.Len() > 0 {
		require.NoError(h.t, json.Unmarshal(rec.Body.Bytes(), &m), rec.Body.String())
	}
	return rec.Code, m
}

func TestHTTPCreateGetUpdateAndList(t *testing.T) {
	h := newHTTPCrud(t)

	status, body := h.do(http.MethodPost, "/projects", map[string]any{"name": "Shop", "description": "d"})
	require.Equal(t, http.StatusCreated, status, body)
	id := body["id"].(string)
	require.Equal(t, "shop", body["slug"])
	require.Equal(t, "inherit", body["buildLocation"])

	status, body = h.do(http.MethodGet, "/projects/"+id, nil)
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, "Shop", body["name"])

	status, body = h.do(http.MethodPatch, "/projects/"+id, map[string]any{"name": "Store", "buildLocation": "target"})
	require.Equal(t, http.StatusOK, status, body)
	require.Equal(t, "Store", body["name"])
	require.Equal(t, "target", body["buildLocation"])

	status, body = h.do(http.MethodPatch, "/projects/"+id, map[string]any{"buildServerId": uuid.NewString()})
	require.Equal(t, http.StatusUnprocessableEntity, status, body)
	require.Equal(t, "validation_failed", body["code"])

	h.project("Second")
	status, body = h.do(http.MethodGet, "/projects?limit=1", nil)
	require.Equal(t, http.StatusOK, status)
	require.Len(t, body["items"], 1)
	next, _ := body["nextCursor"].(string)
	require.NotEmpty(t, next)
	status, body = h.do(http.MethodGet, "/projects?limit=1&cursor="+next, nil)
	require.Equal(t, http.StatusOK, status)
	require.Len(t, body["items"], 1)
	require.Equal(t, "Store", body["items"].([]any)[0].(map[string]any)["name"])
	require.Nil(t, body["nextCursor"])

	status, body = h.do(http.MethodGet, "/projects?cursor=zzz", nil)
	require.Equal(t, http.StatusBadRequest, status)
	require.Equal(t, "invalid_cursor", body["code"])
}

func TestHTTPCreateValidatesTheBody(t *testing.T) {
	h := newHTTPCrud(t)
	status, _ := h.do(http.MethodPost, "/projects", map[string]any{"name": ""})
	require.Equal(t, http.StatusUnprocessableEntity, status)
	status, _ = h.do(http.MethodPost, "/projects", map[string]any{"name": strings.Repeat("x", 101)})
	require.Equal(t, http.StatusUnprocessableEntity, status)
	status, _ = h.do(http.MethodPost, "/projects", map[string]any{"name": "ok", "description": strings.Repeat("x", 501)})
	require.Equal(t, http.StatusUnprocessableEntity, status)
	require.Zero(t, h.count(`SELECT count(*) FROM projects`))
}

func TestHTTPDeletionPlanAndDelete(t *testing.T) {
	h := newHTTPCrud(t)
	p := h.project("Shop")
	sv := h.service(p, "web")

	status, body := h.do(http.MethodGet, "/projects/"+p.ID.String()+"/deletion-plan", nil)
	require.Equal(t, http.StatusOK, status, body)
	require.EqualValues(t, 1, body["services"])
	require.Equal(t, true, body["backupDestinationsKept"])
	require.Equal(t, "Shop", body["project"].(map[string]any)["name"])

	status, body = h.do(http.MethodDelete, "/projects/"+p.ID.String(), nil)
	require.Equal(t, http.StatusConflict, status)
	require.Equal(t, "project_not_empty", body["code"])

	status, body = h.do(http.MethodDelete, "/projects/"+p.ID.String()+"?cascade=true&confirm=Nope", nil)
	require.Equal(t, http.StatusUnprocessableEntity, status)
	require.Equal(t, "confirm_mismatch", body["code"])
	require.Equal(t, map[string]any{"name": "Shop"}, body["details"])

	require.NoError(t, h.q.SoftDeleteService(context.Background(), sv.ID))
	status, _ = h.do(http.MethodDelete, "/projects/"+p.ID.String(), nil)
	require.Equal(t, http.StatusNoContent, status)
	status, _ = h.do(http.MethodGet, "/projects/"+p.ID.String(), nil)
	require.Equal(t, http.StatusNotFound, status)
}

func TestHTTPReadOnlyScopeCannotWrite(t *testing.T) {
	h := newHTTPCrud(t, policy.ScopeProjectsRead)
	p := h.project("Shop")

	status, _ := h.do(http.MethodGet, "/projects/"+p.ID.String(), nil)
	require.Equal(t, http.StatusOK, status)
	status, _ = h.do(http.MethodPost, "/projects", map[string]any{"name": "X"})
	require.Equal(t, http.StatusForbidden, status)
	status, _ = h.do(http.MethodPatch, "/projects/"+p.ID.String(), map[string]any{"name": "X"})
	require.Equal(t, http.StatusForbidden, status)
	status, _ = h.do(http.MethodDelete, "/projects/"+p.ID.String(), nil)
	require.Equal(t, http.StatusForbidden, status)
}
