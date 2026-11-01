package services_test

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

	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/events"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/prodguard"
	"github.com/nebulactrl/nebulactrl/internal/prodguard/prodguardtest"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/internal/testutil"
)

type linkGuardFixture struct {
	t       *testing.T
	pool    *pgxpool.Pool
	q       *db.Queries
	box     *secretbox.Box
	svcs    *services.Service
	ws      db.Workspace
	userID  uuid.UUID
	prod    db.Environment
	staging db.Environment
}

func newLinkGuardFixture(t *testing.T) *linkGuardFixture {
	t.Helper()
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
	box, err := secretbox.New(bytes.Repeat([]byte{5}, 32))
	require.NoError(t, err)
	svcs := services.NewService(pool, events.NewBus(), box, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	return &linkGuardFixture{t: t, pool: pool, q: q, box: box, svcs: svcs, ws: ws, userID: u.ID, prod: prod, staging: staging}
}

func (f *linkGuardFixture) service(envID uuid.UUID, slug string) db.Service {
	f.t.Helper()
	raw, err := json.Marshal(services.Config{Image: "traefik/whoami:latest", Port: 80}.WithDefaults())
	require.NoError(f.t, err)
	sv, err := f.q.InsertService(context.Background(), db.InsertServiceParams{
		ID: uuid.New(), EnvironmentID: envID, Name: slug, Slug: slug, Kind: services.KindApp, SourceType: services.SourceImage, Config: raw,
	})
	require.NoError(f.t, err)
	return sv
}

func (f *linkGuardFixture) link(envID, from, to uuid.UUID) db.ServiceLink {
	f.t.Helper()
	l, err := f.q.InsertLink(context.Background(), db.InsertLinkParams{EnvironmentID: envID, FromServiceID: from, ToServiceID: to})
	require.NoError(f.t, err)
	return l
}

func (f *linkGuardFixture) links(envID uuid.UUID) int {
	f.t.Helper()
	var n int
	require.NoError(f.t, f.pool.QueryRow(context.Background(), "SELECT count(*) FROM service_links WHERE environment_id = $1", envID).Scan(&n))
	return n
}

// restToken is a REST token as authentication builds it: guarded unless exempt.
func (f *linkGuardFixture) restToken() context.Context {
	id := uuid.New()
	return auth.WithPrincipal(context.Background(), &auth.Principal{
		Type: auth.TypeToken, UserID: f.userID, TokenID: &id, WorkspaceID: f.ws.ID, Role: auth.RoleOwner,
		Label: "ci", Scopes: policy.AllScopes, RequireProductionApproval: true,
	})
}

// person is a browser session (also the approver).
func (f *linkGuardFixture) person() context.Context {
	return auth.WithPrincipal(context.Background(), &auth.Principal{
		Type: auth.TypeUser, UserID: f.userID, WorkspaceID: f.ws.ID, Role: auth.RoleOwner, Label: "Ada", Scopes: policy.AllScopes,
	})
}

func (f *linkGuardFixture) installGuard() *prodguard.Guard {
	f.t.Helper()
	g := prodguard.New(prodguard.Deps{Pool: f.pool, Box: f.box})
	g.Install()
	f.t.Cleanup(prodguardtest.Uninstall)
	return g
}

func (f *linkGuardFixture) approve(g *prodguard.Guard, err error) *prodguard.ErrApprovalRequired {
	f.t.Helper()
	ar, ok := prodguard.AsRequired(err)
	require.True(f.t, ok, "want approval_required, got %v", err)
	_, aerr := g.Approve(f.person(), ar.ApprovalID)
	require.NoError(f.t, aerr)
	return ar
}

// A REST token cannot link two services of a production environment without a
// person's approval; the browser session is not asked.
func TestCreateLinkInProductionNeedsApprovalForRESTTokens(t *testing.T) {
	f := newLinkGuardFixture(t)
	g := f.installGuard()
	rest := f.restToken()
	a, b := f.service(f.prod.ID, "a"), f.service(f.prod.ID, "b")

	_, err := f.svcs.CreateLink(rest, f.prod.ID, a.ID, b.ID)
	ar, ok := prodguard.AsRequired(err)
	require.True(t, ok, "got %v", err)
	require.Contains(t, ar.Plan.Summary, "Link two services of the production environment")
	require.Equal(t, 0, f.links(f.prod.ID), "nothing is linked before the approval")

	f.approve(g, err)
	res, err := f.svcs.CreateLink(rest, f.prod.ID, a.ID, b.ID)
	require.NoError(t, err)
	require.Equal(t, a.ID, res.Link.FromServiceID)
	require.Equal(t, b.ID, res.Link.ToServiceID)
	require.Equal(t, 1, f.links(f.prod.ID))

	// The approval was used up (the guard asks before the duplicate is noticed).
	_, err = f.svcs.CreateLink(rest, f.prod.ID, a.ID, b.ID)
	_, ok = prodguard.AsRequired(err)
	require.True(t, ok, "got %v", err)

	// An approval is for these two services: the reverse edge is another call.
	_, err = f.svcs.CreateLink(rest, f.prod.ID, b.ID, a.ID)
	_, ok = prodguard.AsRequired(err)
	require.True(t, ok, "got %v", err)
	require.Equal(t, 1, f.links(f.prod.ID))

	// A person in the browser is never asked.
	_, err = f.svcs.CreateLink(f.person(), f.prod.ID, b.ID, a.ID)
	require.NoError(t, err)
	require.Equal(t, 2, f.links(f.prod.ID))
}

// A REST token cannot remove a link of a production environment without a
// person's approval; the browser session is not asked.
func TestDeleteLinkInProductionNeedsApprovalForRESTTokens(t *testing.T) {
	f := newLinkGuardFixture(t)
	g := f.installGuard()
	rest := f.restToken()
	a, b, c := f.service(f.prod.ID, "a"), f.service(f.prod.ID, "b"), f.service(f.prod.ID, "c")
	l := f.link(f.prod.ID, a.ID, b.ID)
	other := f.link(f.prod.ID, b.ID, c.ID)

	err := f.svcs.DeleteLink(rest, l.ID.String())
	ar, ok := prodguard.AsRequired(err)
	require.True(t, ok, "got %v", err)
	require.Contains(t, ar.Plan.Summary, "Remove a link between services of the production environment")
	require.Equal(t, 2, f.links(f.prod.ID), "the link still exists before the approval")

	f.approve(g, err)
	require.NoError(t, f.svcs.DeleteLink(rest, l.ID.String()))
	require.Equal(t, 1, f.links(f.prod.ID))

	// An approval names one link: another one is another call.
	err = f.svcs.DeleteLink(rest, other.ID.String())
	_, ok = prodguard.AsRequired(err)
	require.True(t, ok, "got %v", err)
	require.Equal(t, 1, f.links(f.prod.ID))

	// A person in the browser is never asked.
	require.NoError(t, f.svcs.DeleteLink(f.person(), other.ID.String()))
	require.Equal(t, 0, f.links(f.prod.ID))
}

// A non-production environment is not guarded.
func TestLinksInNonProductionAreNotGuarded(t *testing.T) {
	f := newLinkGuardFixture(t)
	f.installGuard()
	rest := f.restToken()
	a, b := f.service(f.staging.ID, "a"), f.service(f.staging.ID, "b")

	res, err := f.svcs.CreateLink(rest, f.staging.ID, a.ID, b.ID)
	require.NoError(t, err)
	require.Equal(t, 1, f.links(f.staging.ID))

	require.NoError(t, f.svcs.DeleteLink(rest, res.Link.ID))
	require.Equal(t, 0, f.links(f.staging.ID))
}
