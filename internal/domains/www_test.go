package domains

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
	"github.com/nebulactrl/nebulactrl/internal/events"
	"github.com/nebulactrl/nebulactrl/internal/jobs"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/internal/testutil"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

type wwwFixture struct {
	t      *testing.T
	pool   *pgxpool.Pool
	q      *db.Queries
	ctx    context.Context
	svc    *Service
	svcA   db.Service
	env    db.Environment
	server db.Server
}

func newWWWFixture(t *testing.T) *wwwFixture {
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
	env, err := q.CreateEnvironment(bg, db.CreateEnvironmentParams{ProjectID: prj.ID, Name: "production", Slug: "production"})
	require.NoError(t, err)
	ip := "203.0.113.9"
	srv, err := q.CreateServer(bg, db.CreateServerParams{WorkspaceID: ws.ID, Name: "agent1", PublicIp: &ip})
	require.NoError(t, err)
	facts, _ := json.Marshal(agentprotocol.Facts{OS: "linux", Capabilities: []string{agentprotocol.CapProxyV2}})
	_, err = pool.Exec(bg, "UPDATE servers SET status = 'online', facts = $2 WHERE id = $1", srv.ID, facts)
	require.NoError(t, err)
	raw, _ := json.Marshal(services.Config{Image: "traefik/whoami:latest", Port: 80}.WithDefaults())
	sv, err := q.InsertService(bg, db.InsertServiceParams{
		ID: uuid.New(), EnvironmentID: env.ID, Name: "site", Slug: "site", Kind: services.KindApp, SourceType: services.SourceImage, Config: raw, ServerID: &srv.ID,
	})
	require.NoError(t, err)

	box, err := secretbox.New(bytes.Repeat([]byte{7}, 32))
	require.NoError(t, err)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	jm := jobs.New(pool, logger)
	svc := NewService(pool, events.NewBus(), jm, box)
	RegisterJobs(jm, svc, logger)
	ctx := auth.WithPrincipal(bg, &auth.Principal{Type: auth.TypeUser, UserID: u.ID, WorkspaceID: ws.ID, Label: "t", Scopes: policy.AllScopes})
	return &wwwFixture{t: t, pool: pool, q: q, ctx: ctx, svc: svc, svcA: sv, env: env, server: srv}
}

func (f *wwwFixture) gen() int64 {
	f.t.Helper()
	row, err := f.q.GetServiceScopeInternal(context.Background(), f.svcA.ID)
	require.NoError(f.t, err)
	return row.Service.Generation
}

func (f *wwwFixture) rows() map[string]db.Domain {
	f.t.Helper()
	rows, err := f.q.ListServiceDomains(context.Background(), f.svcA.ID)
	require.NoError(f.t, err)
	out := map[string]db.Domain{}
	for _, r := range rows {
		out[r.Hostname] = r
	}
	return out
}

func (f *wwwFixture) view(host string) services.DomainView {
	f.t.Helper()
	vs, err := f.svc.List(f.ctx, f.svcA.ID)
	require.NoError(f.t, err)
	for _, v := range vs {
		if v.Hostname == host {
			return v
		}
	}
	f.t.Fatalf("no domain %s", host)
	return services.DomainView{}
}

func requireCode(t *testing.T, err error, code string) {
	t.Helper()
	ae, ok := apierr.As(err)
	require.True(t, ok, "%v", err)
	require.Equal(t, code, ae.Code)
}

func TestWWWRedirectApexCanonicalCreatesBothHosts(t *testing.T) {
	f := newWWWFixture(t)
	on := true
	v, err := f.svc.Create(f.ctx, f.svcA.ID, "Example.com", 0, RuleInput{WWWRedirect: &on})
	require.NoError(t, err)
	require.Equal(t, "www.example.com", v.WWWPartner)
	require.True(t, v.WWWRedirect)

	rows := f.rows()
	require.Len(t, rows, 2, "the domain and its www redirect")
	www := rows["www.example.com"]
	require.Equal(t, agentprotocol.DomainRedirect, www.Kind)
	require.NotNil(t, www.RedirectTo)
	require.Equal(t, "example.com", *www.RedirectTo)
	require.EqualValues(t, 301, www.RedirectCode)
	require.True(t, www.PreservePath)
	require.Equal(t, agentprotocol.TLSAuto, www.TlsMode, "automatic certificates for both hosts")
	require.Equal(t, rows["example.com"].TargetPort, www.TargetPort)
	require.Contains(t, www.StatusMessage, "Point an A record for www.example.com to 203.0.113.9", "its own DNS guidance")
	require.Contains(t, rows["example.com"].StatusMessage, "Point an A record for example.com to 203.0.113.9")

	rv := f.view("www.example.com")
	require.Equal(t, "example.com", rv.WWWRedirectFor)
	require.Empty(t, rv.WWWPartner, "a redirect has no partner of its own")

	// The agent gets both hosts: a serve rule and a redirect rule with the 301.
	rule := RuleFromRow(www)
	require.Equal(t, agentprotocol.DomainRedirect, rule.Kind)
	require.Equal(t, "example.com", rule.RedirectTo)
	require.Equal(t, 301, rule.RedirectCode)
	require.Equal(t, "www.example.com", rule.Host)
}

func TestWWWRedirectWWWCanonicalRedirectsApexToWWW(t *testing.T) {
	f := newWWWFixture(t)
	on := true
	_, err := f.svc.Create(f.ctx, f.svcA.ID, "www.example.com", 0, RuleInput{WWWRedirect: &on})
	require.NoError(t, err)
	rows := f.rows()
	require.Len(t, rows, 2)
	apex := rows["example.com"]
	require.Equal(t, agentprotocol.DomainRedirect, apex.Kind)
	require.Equal(t, "www.example.com", *apex.RedirectTo)
	require.Equal(t, agentprotocol.DomainServe, rows["www.example.com"].Kind)
	require.Equal(t, "example.com", f.view("www.example.com").WWWPartner)
}

func TestWWWRedirectToggleRenameAndDelete(t *testing.T) {
	f := newWWWFixture(t)
	d, err := f.svc.Create(f.ctx, f.svcA.ID, "example.com", 0, RuleInput{})
	require.NoError(t, err)
	require.False(t, d.WWWRedirect)
	require.Equal(t, "www.example.com", d.WWWPartner, "eligible, but off")
	require.Len(t, f.rows(), 1)

	on, off := true, false
	// Turn on through PATCH.
	d, gen, err := f.svc.Update(f.ctx, d.ID, f.gen(), UpdateInput{Rule: RuleInput{WWWRedirect: &on}})
	require.NoError(t, err)
	require.True(t, d.WWWRedirect)
	require.Equal(t, f.gen(), gen)
	require.Len(t, f.rows(), 2)

	// Turning it on again changes nothing and does not advance the generation.
	before := f.gen()
	_, gen, err = f.svc.Update(f.ctx, d.ID, before, UpdateInput{Rule: RuleInput{WWWRedirect: &on}})
	require.NoError(t, err)
	require.Equal(t, before, gen)

	// Renaming the domain moves the redirect with it.
	host := "example.org"
	d, _, err = f.svc.Update(f.ctx, d.ID, f.gen(), UpdateInput{Hostname: &host})
	require.NoError(t, err)
	rows := f.rows()
	require.Len(t, rows, 2)
	require.NotContains(t, rows, "www.example.com")
	require.Equal(t, "example.org", *rows["www.example.org"].RedirectTo)
	require.True(t, d.WWWRedirect)

	// The redirect follows the certificate settings of the domain.
	none := agentprotocol.TLSNone
	_, _, err = f.svc.Update(f.ctx, d.ID, f.gen(), UpdateInput{Rule: RuleInput{TLSMode: &none}})
	require.NoError(t, err)
	require.Equal(t, agentprotocol.TLSNone, f.rows()["www.example.org"].TlsMode)

	// Turn off.
	d, _, err = f.svc.Update(f.ctx, d.ID, f.gen(), UpdateInput{Rule: RuleInput{WWWRedirect: &off}})
	require.NoError(t, err)
	require.False(t, d.WWWRedirect)
	require.Len(t, f.rows(), 1)

	// Deleting the domain deletes its redirect.
	_, _, err = f.svc.Update(f.ctx, d.ID, f.gen(), UpdateInput{Rule: RuleInput{WWWRedirect: &on}})
	require.NoError(t, err)
	require.Len(t, f.rows(), 2)
	require.NoError(t, f.svc.Delete(f.ctx, d.ID))
	require.Empty(t, f.rows())
}

func TestWWWRedirectRejections(t *testing.T) {
	f := newWWWFixture(t)
	on := true

	_, err := f.svc.Create(f.ctx, f.svcA.ID, "example.com", 0, RuleInput{WWWRedirect: &on, PathPrefix: new("/api")})
	requireCode(t, err, "invalid_www_redirect")
	_, err = f.svc.Create(f.ctx, f.svcA.ID, "*.example.com", 0, RuleInput{WWWRedirect: &on, TLSMode: new(agentprotocol.TLSNone)})
	requireCode(t, err, "invalid_www_redirect")
	_, err = f.svc.Create(f.ctx, f.svcA.ID, "app.example.com", 0, RuleInput{WWWRedirect: &on, Kind: new("redirect"), RedirectTo: new("example.com")})
	requireCode(t, err, "invalid_www_redirect")
	require.Empty(t, f.rows(), "a rejected create leaves nothing behind")

	// The counterpart hostname is already taken: 409 and nothing is created.
	_, err = f.svc.Create(f.ctx, f.svcA.ID, "www.example.com", 0, RuleInput{})
	require.NoError(t, err)
	_, err = f.svc.Create(f.ctx, f.svcA.ID, "example.com", 0, RuleInput{WWWRedirect: &on})
	requireCode(t, err, "domain_conflict")
	require.Len(t, f.rows(), 1, "the canonical domain was rolled back with the failed redirect")
}
