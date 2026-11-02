package mcpkit_test

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agents"
	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/environments"
	"github.com/nebulactrl/nebulactrl/internal/events"
	"github.com/nebulactrl/nebulactrl/internal/mcp/mcpkit"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/projects"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/internal/testutil"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

type nopDispatcher struct{}

func (nopDispatcher) Enqueue(context.Context, pgx.Tx, agents.NewCommand) (uuid.UUID, error) {
	return uuid.New(), nil
}

func (nopDispatcher) SubscribeLogs(context.Context, uuid.UUID, agentprotocol.LogsSubscribe) (<-chan agentprotocol.LogFrame, func(), error) {
	return nil, nil, io.EOF
}

// selFx is a team with project "shop" (production + staging), a server and a
// resolver over the real domain services.
type selFx struct {
	t      *testing.T
	pool   *pgxpool.Pool
	q      *db.Queries
	r      *mcpkit.Resolver
	ws     db.Workspace
	user   db.CreateUserRow
	shop   db.Project
	prod   db.Environment
	stg    db.Environment
	svcs   *services.Service
	server db.Server
	ctx    context.Context
}

func newSelFx(t *testing.T) *selFx {
	t.Helper()
	pool := testutil.NewPool(t)
	q := db.New(pool)
	bg := context.Background()
	ws, err := q.CreateWorkspace(bg, "w")
	require.NoError(t, err)
	u, err := q.CreateUser(bg, db.CreateUserParams{WorkspaceID: ws.ID, Email: "a@example.com", Name: "A", PasswordHash: "x"})
	require.NoError(t, err)
	f := &selFx{t: t, pool: pool, q: q, ws: ws, user: u}
	f.shop = f.project("Shop", "shop")
	f.prod = f.env(f.shop, "production", "production", true)
	f.stg = f.env(f.shop, "staging", "staging", false)
	srv, err := q.CreateServer(bg, db.CreateServerParams{WorkspaceID: ws.ID, Name: "agent1"})
	require.NoError(t, err)
	_, err = pool.Exec(bg, "UPDATE servers SET status = 'online' WHERE id = $1", srv.ID)
	require.NoError(t, err)
	f.server = srv

	box, err := secretbox.New(bytes.Repeat([]byte{7}, 32))
	require.NoError(t, err)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	bus := events.NewBus()
	f.svcs = services.NewService(pool, bus, box, nopDispatcher{}, logger)
	f.r = mcpkit.NewResolver(projects.NewService(pool, bus), environments.NewService(pool, bus), f.svcs, pool)
	f.ctx = f.as()
	return f
}

func (f *selFx) project(name, slug string) db.Project {
	f.t.Helper()
	p, err := f.q.CreateProject(context.Background(), db.CreateProjectParams{WorkspaceID: f.ws.ID, Name: name, Slug: slug})
	require.NoError(f.t, err)
	return p
}

func (f *selFx) env(p db.Project, name, slug string, prod bool) db.Environment {
	f.t.Helper()
	e, err := f.q.CreateEnvironment(context.Background(), db.CreateEnvironmentParams{ProjectID: p.ID, Name: name, Slug: slug, IsProduction: prod})
	require.NoError(f.t, err)
	return e
}

// as is a full-scope MCP caller, restricted to projectIDs when given.
func (f *selFx) as(projectIDs ...uuid.UUID) context.Context {
	return auth.WithPrincipal(context.Background(), &auth.Principal{
		Type: auth.TypeMCP, UserID: f.user.ID, WorkspaceID: f.ws.ID, Role: auth.RoleOwner,
		Scopes: policy.AllScopes, ProjectIDs: projectIDs,
	})
}

func (f *selFx) addService(env db.Environment, name string) services.ServiceView {
	f.t.Helper()
	v, err := f.svcs.Create(f.ctx, env.ID, services.CreateInput{
		Name: name, SourceType: services.SourceImage, ServerID: &f.server.ID,
		Config: services.Config{Image: "traefik/whoami:latest", Port: 80},
	})
	require.NoError(f.t, err)
	return v
}

func (f *selFx) domain(svc uuid.UUID, host string) {
	f.t.Helper()
	_, err := f.pool.Exec(context.Background(), `INSERT INTO domains (service_id, hostname, target_port) VALUES ($1, $2, 80)`, svc, host)
	require.NoError(f.t, err)
}

func selErr(t *testing.T, err error, code string) *mcpkit.Error {
	t.Helper()
	var me *mcpkit.Error
	require.ErrorAs(t, err, &me, "want a tool error, got %v", err)
	require.Equal(t, code, me.Code, me.Message)
	return me
}

func choicePaths(t *testing.T, e *mcpkit.Error) []string {
	t.Helper()
	data, ok := e.Data.(map[string]any)
	require.True(t, ok, "the error carries choices")
	cs, ok := data["choices"].([]mcpkit.Choice)
	require.True(t, ok)
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.Path
	}
	return out
}

func TestErrorStringIsCodeAndMessage(t *testing.T) {
	require.Equal(t, "not_found: no such thing", mcpkit.Errf(mcpkit.CodeNotFound, "no such %s", "thing").Error())
}

func TestProjectSelectorForms(t *testing.T) {
	f := newSelFx(t)
	other := f.project("Billing Platform", "billing")

	for _, sel := range []string{f.shop.ID.String(), "shop", "SHOP", "  shop  ", "Shop"} {
		p, err := f.r.Project(f.ctx, sel)
		require.NoError(t, err, sel)
		require.Equal(t, f.shop.ID, p.ID, sel)
	}
	p, err := f.r.Project(f.ctx, "billing platform")
	require.NoError(t, err)
	require.Equal(t, other.ID, p.ID, "the name is the fallback when no slug matches")

	_, err = f.r.Project(f.ctx, "")
	selErr(t, err, mcpkit.CodeInvalidArgument)
	_, err = f.r.Project(f.ctx, "   ")
	selErr(t, err, mcpkit.CodeInvalidArgument)

	_, err = f.r.Project(f.ctx, uuid.NewString())
	ae, ok := apierr.As(err)
	require.True(t, ok, "an unknown id is the service's own not-found: %v", err)
	require.Equal(t, 404, ae.Status)
}

func TestProjectMissSuggestsNearProjectsAndFallsBackToAll(t *testing.T) {
	f := newSelFx(t)
	f.project("Billing Platform", "billing")

	e := selErr(t, mustErr(f.r.Project(f.ctx, "shop-eu")), mcpkit.CodeNotFound)
	require.Contains(t, e.Message, `"shop-eu"`)
	require.Equal(t, []string{"shop"}, choicePaths(t, e), "a project whose slug is contained in the selector is near")

	e = selErr(t, mustErr(f.r.Project(f.ctx, "bill")), mcpkit.CodeNotFound)
	require.Equal(t, []string{"billing"}, choicePaths(t, e), "a selector contained in the slug or name is near")

	e = selErr(t, mustErr(f.r.Project(f.ctx, "zzz")), mcpkit.CodeNotFound)
	require.Equal(t, []string{"billing", "shop"}, choicePaths(t, e), "nothing near lists every project, sorted by slug")
}

func mustErr[T any](_ T, err error) error { return err }

func TestProjectAmbiguousNameListsEveryMatch(t *testing.T) {
	f := newSelFx(t)
	f.project("Twin", "twin-a")
	f.project("Twin", "twin-b")

	e := selErr(t, mustErr(f.r.Project(f.ctx, "twin")), mcpkit.CodeAmbiguous)
	require.Contains(t, e.Message, "use an id")
	require.ElementsMatch(t, []string{"twin-a", "twin-b"}, choicePaths(t, e))

	e = selErr(t, mustErr(f.r.Project(f.ctx, "Twin")), mcpkit.CodeAmbiguous)
	require.Len(t, choicePaths(t, e), 2)
}

func TestProjectSelectorNeverRevealsProjectsOutsideTheTokenRestriction(t *testing.T) {
	f := newSelFx(t)
	other := f.project("Secret", "secret")

	ctx := f.as(f.shop.ID)
	p, err := f.r.Project(ctx, "shop")
	require.NoError(t, err)
	require.Equal(t, f.shop.ID, p.ID)

	e := selErr(t, mustErr(f.r.Project(ctx, "secret")), mcpkit.CodeNotFound)
	for _, c := range choicePaths(t, e) {
		require.NotEqual(t, "secret", c, "a project outside the restriction is not suggested")
	}
	_, err = f.r.Project(ctx, other.ID.String())
	ae, ok := apierr.As(err)
	require.True(t, ok)
	require.Contains(t, []int{403, 404}, ae.Status)
}

func TestEnvironmentSelectorForms(t *testing.T) {
	f := newSelFx(t)

	got, err := f.r.Environment(f.ctx, f.stg.ID.String())
	require.NoError(t, err)
	require.Equal(t, f.stg.ID, got.ID)

	for sel, want := range map[string]uuid.UUID{
		"shop":              f.prod.ID, // the single production environment
		"shop/":             f.prod.ID,
		"shop/staging":      f.stg.ID,
		"SHOP/Staging":      f.stg.ID,
		" shop / staging  ": f.stg.ID,
		"shop/production":   f.prod.ID,
	} {
		got, err := f.r.Environment(f.ctx, sel)
		require.NoError(t, err, sel)
		require.Equal(t, want, got.ID, sel)
	}

	_, err = f.r.Environment(f.ctx, "")
	selErr(t, err, mcpkit.CodeInvalidArgument)
	_, err = f.r.Environment(f.ctx, "nope/staging")
	selErr(t, err, mcpkit.CodeNotFound)
}

func TestEnvironmentNameIsTheFallbackWhenNoSlugMatches(t *testing.T) {
	f := newSelFx(t)
	qa := f.env(f.shop, "Quality Assurance", "qa", false)

	got, err := f.r.Environment(f.ctx, "shop/quality assurance")
	require.NoError(t, err)
	require.Equal(t, qa.ID, got.ID)
}

func TestEnvironmentMissAndAmbiguityListTheProjectsEnvironments(t *testing.T) {
	f := newSelFx(t)

	e := selErr(t, mustErr(f.r.Environment(f.ctx, "shop/prod")), mcpkit.CodeNotFound)
	require.Contains(t, e.Message, `"prod"`)
	require.Contains(t, e.Message, `"shop"`)
	require.ElementsMatch(t, []string{"shop/production", "shop/staging"}, choicePaths(t, e))

	f.env(f.shop, "Dup", "dup-a", false)
	f.env(f.shop, "Dup", "dup-b", false)
	e = selErr(t, mustErr(f.r.Environment(f.ctx, "shop/dup")), mcpkit.CodeAmbiguous)
	require.ElementsMatch(t, []string{"shop/dup-a", "shop/dup-b"}, choicePaths(t, e), "only the matching environments are offered")
}

func TestBareProjectNeedsASingleProductionEnvironment(t *testing.T) {
	f := newSelFx(t)

	// No production flag but exactly one standard environment: that one.
	solo := f.project("Solo", "solo")
	only := f.env(solo, "main", "main", false)
	got, err := f.r.Environment(f.ctx, "solo")
	require.NoError(t, err)
	require.Equal(t, only.ID, got.ID)

	// Preview environments never count.
	f.env(solo, "pr-1", "pr-1", false)
	_, err = f.pool.Exec(context.Background(), `UPDATE environments SET kind = 'preview' WHERE project_id = $1 AND slug = 'pr-1'`, solo.ID)
	require.NoError(t, err)
	got, err = f.r.Environment(f.ctx, "solo")
	require.NoError(t, err)
	require.Equal(t, only.ID, got.ID, "a preview environment is not a candidate")

	// Two standard environments and no production: ambiguous, all listed.
	f.env(solo, "dev", "dev", false)
	e := selErr(t, mustErr(f.r.Environment(f.ctx, "solo")), mcpkit.CodeAmbiguous)
	require.Contains(t, e.Message, "no single production environment")
	require.ElementsMatch(t, []string{"solo/main", "solo/dev", "solo/pr-1"}, choicePaths(t, e))

	// Two production environments: ambiguous as well.
	twin := f.project("Twin", "twin")
	f.env(twin, "eu", "eu", true)
	f.env(twin, "us", "us", true)
	selErr(t, mustErr(f.r.Environment(f.ctx, "twin")), mcpkit.CodeAmbiguous)

	// No environment at all.
	f.project("Empty", "empty")
	selErr(t, mustErr(f.r.Environment(f.ctx, "empty")), mcpkit.CodeAmbiguous)
}

func TestServiceSelectorForms(t *testing.T) {
	f := newSelFx(t)
	api := f.addService(f.prod, "api")
	web := f.addService(f.stg, "Web Frontend")

	for sel, want := range map[string]uuid.UUID{
		api.ID.String():                 api.ID,
		"shop/production/api":           api.ID,
		"SHOP/Production/API":           api.ID,
		"shop/api":                      api.ID, // the project's single production environment
		"api":                           api.ID, // unique across the estate
		"shop/staging/web-frontend":     web.ID,
		"shop/staging/web frontend":     web.ID, // the name is the fallback
		"web frontend":                  web.ID,
		"shop/staging/ Web Frontend  ":  web.ID,
		" shop/production/api ":         api.ID,
		"shop/production/  api":         api.ID,
		"shop/production/api/extra/bad": uuid.Nil,
	} {
		got, err := f.r.Service(f.ctx, sel)
		if want == uuid.Nil {
			selErr(t, err, mcpkit.CodeInvalidArgument)
			continue
		}
		require.NoError(t, err, sel)
		require.Equal(t, want, got.ID, sel)
	}

	_, err := f.r.Service(f.ctx, "")
	selErr(t, err, mcpkit.CodeInvalidArgument)
}

func TestServiceSelectorMissesListWhatExists(t *testing.T) {
	f := newSelFx(t)
	f.addService(f.prod, "api")
	f.addService(f.prod, "worker")

	e := selErr(t, mustErr(f.r.Service(f.ctx, "shop/production/ap")), mcpkit.CodeNotFound)
	require.Contains(t, e.Message, `"ap"`)
	require.Contains(t, e.Message, "shop/production")
	require.ElementsMatch(t, []string{"shop/production/api", "shop/production/worker"}, choicePaths(t, e))

	e = selErr(t, mustErr(f.r.Service(f.ctx, "nosuch")), mcpkit.CodeNotFound)
	require.Contains(t, e.Message, "search_resources")
	require.Nil(t, e.Data, "a bare-name miss has no candidates to offer")

	selErr(t, mustErr(f.r.Service(f.ctx, "nope/production/api")), mcpkit.CodeNotFound)
	selErr(t, mustErr(f.r.Service(f.ctx, "shop/nope/api")), mcpkit.CodeNotFound)
	selErr(t, mustErr(f.r.Service(f.ctx, "nope/api")), mcpkit.CodeNotFound)
}

func TestServiceNameDuplicatesInOneEnvironmentAreAmbiguous(t *testing.T) {
	f := newSelFx(t)
	a := f.addService(f.prod, "Job Runner")
	b := f.addService(f.prod, "Job Runner")
	require.NotEqual(t, a.Slug, b.Slug)

	// No slug equals the selector, so the name decides, and two names match.
	e := selErr(t, mustErr(f.r.Service(f.ctx, "shop/production/job runner")), mcpkit.CodeAmbiguous)
	require.ElementsMatch(t, []string{"shop/production/" + a.Slug, "shop/production/" + b.Slug}, choicePaths(t, e))
	got, err := f.r.Service(f.ctx, "shop/production/"+b.Slug)
	require.NoError(t, err, "a slug disambiguates")
	require.Equal(t, b.ID, got.ID)
}

func TestBareSlugAcrossEnvironmentsIsAmbiguousWithPaths(t *testing.T) {
	f := newSelFx(t)
	f.addService(f.prod, "api")
	f.addService(f.stg, "api")
	other := f.project("Billing", "billing")
	otherProd := f.env(other, "production", "production", true)
	f.addService(otherProd, "api")

	e := selErr(t, mustErr(f.r.Service(f.ctx, "api")), mcpkit.CodeAmbiguous)
	require.Contains(t, e.Message, "matches several services")
	require.ElementsMatch(t, []string{"shop/production/api", "shop/staging/api", "billing/production/api"}, choicePaths(t, e))

	// A project restriction removes the other estate from the scan.
	restricted := f.as(other.ID)
	got, err := f.r.Service(restricted, "api")
	require.NoError(t, err)
	require.Equal(t, other.ID, got.ProjectID)
}

func TestHostnameSelector(t *testing.T) {
	f := newSelFx(t)
	api := f.addService(f.prod, "api")
	f.domain(api.ID, "api.example.com")

	got, err := f.r.Service(f.ctx, "api.example.com")
	require.NoError(t, err)
	require.Equal(t, api.ID, got.ID)
	got, err = f.r.Service(f.ctx, "API.Example.COM")
	require.NoError(t, err)
	require.Equal(t, api.ID, got.ID, "hostnames compare case-insensitively")

	// An unknown hostname is a miss, not a database error.
	selErr(t, mustErr(f.r.Service(f.ctx, "other.example.com")), mcpkit.CodeNotFound)

	// A service called like a dotted name still resolves through the name.
	dotted := f.addService(f.stg, "v1.2")
	got, err = f.r.Service(f.ctx, "v1.2")
	require.NoError(t, err)
	require.Equal(t, dotted.ID, got.ID)
}

func TestHostnameOfAServiceTheCallerCannotReadIsAMiss(t *testing.T) {
	f := newSelFx(t)
	other := f.project("Billing", "billing")
	otherProd := f.env(other, "production", "production", true)
	hidden := f.addService(otherProd, "ledger")
	f.domain(hidden.ID, "ledger.example.com")

	got, err := f.r.Service(f.ctx, "ledger.example.com")
	require.NoError(t, err)
	require.Equal(t, hidden.ID, got.ID, "visible to the unrestricted caller")

	e := selErr(t, mustErr(f.r.Service(f.as(f.shop.ID), "ledger.example.com")), mcpkit.CodeNotFound)
	require.NotContains(t, e.Message, hidden.ID.String(), "no trace of the hidden service")
	require.Nil(t, e.Data)
}

func TestHostnameSelectorsAreDisabledWithoutAPool(t *testing.T) {
	f := newSelFx(t)
	api := f.addService(f.prod, "api")
	f.domain(api.ID, "api.example.com")

	r := mcpkit.NewResolver(f.r.Projects, f.r.Environments, f.r.Services, nil)
	selErr(t, mustErr(r.Service(f.ctx, "api.example.com")), mcpkit.CodeNotFound)
	got, err := r.Service(f.ctx, "shop/production/api")
	require.NoError(t, err, "path selectors do not need the pool")
	require.Equal(t, api.ID, got.ID)
}

func TestNewResolverWiresItsServices(t *testing.T) {
	f := newSelFx(t)
	require.NotNil(t, f.r.Projects)
	require.NotNil(t, f.r.Environments)
	require.Same(t, f.svcs, f.r.Services)
	require.Same(t, f.pool, f.r.Pool)
}
