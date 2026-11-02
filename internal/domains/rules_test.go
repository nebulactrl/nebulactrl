package domains

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// dnsRecorder is a DNSHook that records what the service told it.
type dnsRecorder struct {
	created, renamed []uuid.UUID
	removed          []uuid.UUID // team ids
	failCreate       error
	failRename       error
	failRemove       error
}

func (h *dnsRecorder) DomainCreated(_ context.Context, _ pgx.Tx, id uuid.UUID) error {
	h.created = append(h.created, id)
	return h.failCreate
}

func (h *dnsRecorder) DomainRenamed(_ context.Context, _ pgx.Tx, id uuid.UUID) error {
	h.renamed = append(h.renamed, id)
	return h.failRename
}

func (h *dnsRecorder) DomainsRemoved(_ context.Context, _ pgx.Tx, ws uuid.UUID) error {
	h.removed = append(h.removed, ws)
	return h.failRemove
}

func (f *wwwFixture) principal() *auth.Principal {
	p, _ := auth.PrincipalFrom(f.ctx)
	return p
}

// as returns the fixture's user restricted to scopes, signed in as an API token.
func (f *wwwFixture) as(scopes ...string) context.Context {
	p := *f.principal()
	tok := uuid.New()
	p.Type, p.TokenID, p.Scopes = auth.TypeToken, &tok, scopes
	return auth.WithPrincipal(context.Background(), &p)
}

func (f *wwwFixture) exec(sql string, args ...any) {
	f.t.Helper()
	_, err := f.pool.Exec(context.Background(), sql, args...)
	require.NoError(f.t, err)
}

func (f *wwwFixture) mustCreate(host string, in RuleInput) services.DomainView {
	f.t.Helper()
	v, err := f.svc.Create(f.ctx, f.svcA.ID, host, 0, in)
	require.NoError(f.t, err)
	return v
}

// otherService inserts another image service on the fixture's server.
func (f *wwwFixture) otherService(name string) db.Service {
	f.t.Helper()
	raw, _ := json.Marshal(services.Config{Image: "traefik/whoami:latest", Port: 80}.WithDefaults())
	sv, err := f.q.InsertService(context.Background(), db.InsertServiceParams{
		ID: uuid.New(), EnvironmentID: f.env.ID, Name: name, Slug: name, Kind: services.KindApp, SourceType: services.SourceImage, Config: raw, ServerID: &f.server.ID,
	})
	require.NoError(f.t, err)
	return sv
}

func TestGenerateWalksThroughTheCandidatesThenTheTeamsOwn(t *testing.T) {
	f := newWWWFixture(t)
	base := "site-production-shop.203.0.113.9.sslip.io"
	v, err := f.svc.Generate(f.ctx, f.svcA.ID, "")
	require.NoError(t, err)
	require.Equal(t, base, v.Hostname)
	require.True(t, v.Generated)
	require.Equal(t, 80, v.TargetPort)

	// Taken names (by any service of the instance) push the next one along.
	taken := []string{"site-production-shop-2.203.0.113.9.sslip.io", "site-production-shop-3.203.0.113.9.sslip.io"}
	other := f.otherService("other")
	for _, h := range taken {
		_, err := f.svc.Create(f.ctx, other.ID, h, 0, RuleInput{})
		require.NoError(t, err)
	}
	v, err = f.svc.Generate(f.ctx, f.svcA.ID, "")
	require.NoError(t, err)
	require.Equal(t, "site-production-shop-4.203.0.113.9.sslip.io", v.Hostname)

	// After five plain names the team's own candidate follows; then it is over.
	_, err = f.svc.Create(f.ctx, other.ID, "site-production-shop-5.203.0.113.9.sslip.io", 0, RuleInput{})
	require.NoError(t, err)
	ws := f.principal().WorkspaceID
	v, err = f.svc.Generate(f.ctx, f.svcA.ID, "")
	require.NoError(t, err)
	require.Equal(t, teamCandidate(base, ws), v.Hostname)
	_, err = f.svc.Generate(f.ctx, f.svcA.ID, "")
	requireCode(t, err, "domain_conflict")
}

func TestGenerateNeedsAServerAndABaseDomain(t *testing.T) {
	f := newWWWFixture(t)

	f.exec(`UPDATE servers SET public_ip = NULL WHERE id = $1`, f.server.ID)
	_, err := f.svc.Generate(f.ctx, f.svcA.ID, "")
	requireCode(t, err, "no_base_domain")
	// A base domain on the server is enough.
	f.exec(`UPDATE servers SET base_domain = 'Apps.Example.com' WHERE id = $1`, f.server.ID)
	v, err := f.svc.Generate(f.ctx, f.svcA.ID, "")
	require.NoError(t, err)
	require.Equal(t, "site-production-shop.apps.example.com", v.Hostname)

	f.exec(`UPDATE services SET server_id = NULL WHERE id = $1`, f.svcA.ID)
	_, err = f.svc.Generate(f.ctx, f.svcA.ID, "")
	requireCode(t, err, "no_server")
}

func TestListAndDeleteDomains(t *testing.T) {
	f := newWWWFixture(t)
	a := f.mustCreate("a.example.com", RuleInput{})
	b := f.mustCreate("b.example.com", RuleInput{})

	list, err := f.svc.List(f.ctx, f.svcA.ID)
	require.NoError(t, err)
	require.Len(t, list, 2)
	_, err = f.svc.List(f.ctx, uuid.New())
	requireCode(t, err, "not_found")
	_, err = f.svc.List(f.as(policy.ScopeServersRead), f.svcA.ID)
	requireCode(t, err, "forbidden")

	gen := f.gen()
	requireCode(t, f.svc.Delete(f.ctx, uuid.New()), "not_found")
	requireCode(t, f.svc.Delete(f.as(policy.ScopeProjectsRead), a.ID), "forbidden")
	require.NoError(t, f.svc.Delete(f.ctx, a.ID))
	require.Equal(t, gen+1, f.gen(), "removing a domain stages a change")
	require.NotContains(t, f.rows(), "a.example.com")
	require.Contains(t, f.rows(), "b.example.com")
	require.Equal(t, 1, f.auditCount("domain.remove", a.ID))
	requireCode(t, f.svc.Delete(f.ctx, a.ID), "not_found")
	_ = b
}

func TestDNSHookFollowsCreateRenameAndDelete(t *testing.T) {
	f := newWWWFixture(t)
	hook := &dnsRecorder{}
	f.svc.SetDNS(hook)

	v := f.mustCreate("hooked.example.com", RuleInput{})
	require.Equal(t, []uuid.UUID{v.ID}, hook.created)
	gen := f.gen()
	host := "renamed.example.com"
	_, _, err := f.svc.Update(f.ctx, v.ID, gen, UpdateInput{Hostname: &host})
	require.NoError(t, err)
	require.Equal(t, []uuid.UUID{v.ID}, hook.renamed)
	require.NoError(t, f.svc.Delete(f.ctx, v.ID))
	require.Equal(t, []uuid.UUID{f.principal().WorkspaceID}, hook.removed)

	// A hook that fails undoes the whole change.
	hook.failCreate = errors.New("zone sync down")
	_, err = f.svc.Create(f.ctx, f.svcA.ID, "never.example.com", 0, RuleInput{})
	require.ErrorContains(t, err, "dns hook")
	require.NotContains(t, f.rows(), "never.example.com")
	hook.failCreate = nil
	keep := f.mustCreate("keep.example.com", RuleInput{})
	hook.failRename = errors.New("boom")
	host = "other.example.com"
	_, _, err = f.svc.Update(f.ctx, keep.ID, f.gen(), UpdateInput{Hostname: &host})
	require.ErrorContains(t, err, "dns hook")
	require.Contains(t, f.rows(), "keep.example.com", "a failed rename leaves the domain as it was")
	hook.failRemove = errors.New("boom")
	require.ErrorContains(t, f.svc.Delete(f.ctx, keep.ID), "dns hook")
	require.Contains(t, f.rows(), "keep.example.com")
}

func TestRuleValidationOnCreate(t *testing.T) {
	f := newWWWFixture(t)
	str := func(s string) *string { return &s }
	flag := func(b bool) *bool { return &b }
	num := func(n int) *int { return &n }
	redirect := agentprotocol.DomainRedirect

	f.mustCreate("target.example.com", RuleInput{})
	cases := []struct {
		name string
		host string
		in   RuleInput
		code string
	}{
		{"path prefix without slash", "a.example.com", RuleInput{PathPrefix: str("api")}, "invalid_path_prefix"},
		{"add prefix invalid", "a.example.com", RuleInput{AddPrefix: str("no-slash")}, "invalid_path_prefix"},
		{"strip without prefix", "a.example.com", RuleInput{StripPrefix: flag(true)}, "invalid_path_prefix"},
		{"serve with a redirect target", "a.example.com", RuleInput{RedirectTo: str("elsewhere.example.com")}, "invalid_redirect"},
		{"redirect without a target", "a.example.com", RuleInput{Kind: &redirect}, "invalid_redirect"},
		{"redirect to itself", "a.example.com", RuleInput{Kind: &redirect, RedirectTo: str("A.Example.com.")}, "invalid_redirect"},
		{"redirect code", "a.example.com", RuleInput{Kind: &redirect, RedirectTo: str("b.example.com"), RedirectCode: num(303)}, "invalid_redirect"},
		{"redirect with a strip prefix", "a.example.com", RuleInput{Kind: &redirect, RedirectTo: str("b.example.com"), PathPrefix: str("/x"), StripPrefix: flag(true)}, "invalid_redirect"},
		{"redirect with options", "a.example.com", RuleInput{Kind: &redirect, RedirectTo: str("b.example.com"), Options: &agentprotocol.RouteOptions{Compress: true}}, "invalid_redirect"},
		{"unknown kind", "a.example.com", RuleInput{Kind: str("proxy")}, "invalid_kind"},
		{"unknown tls mode", "a.example.com", RuleInput{TLSMode: str("maybe")}, "invalid_tls_mode"},
		{"certificate pin without custom", "a.example.com", RuleInput{CertificateID: str(uuid.NewString())}, "invalid_tls_mode"},
		{"certificate that is not a uuid", "a.example.com", RuleInput{CertificateID: str("nope")}, "invalid_certificate"},
		{"certificate that does not exist", "a.example.com", RuleInput{TLSMode: str("custom"), CertificateID: str(uuid.NewString())}, "certificate_mismatch"},
		{"unknown dns resolver", "a.example.com", RuleInput{DNSResolver: str("not-a-provider")}, "invalid_dns_resolver"},
		{"dns resolver with tls none", "a.example.com", RuleInput{TLSMode: str("none"), DNSResolver: str("cloudflare")}, "invalid_tls_mode"},
		{"invalid options", "a.example.com", RuleInput{Options: &agentprotocol.RouteOptions{SecurityHeaders: "paranoid"}}, "invalid_options"},
		{"basic auth before users", "a.example.com", RuleInput{Options: &agentprotocol.RouteOptions{BasicAuth: true}}, "basic_auth_needs_users"},
		{"duplicate host and path", "target.example.com", RuleInput{}, "domain_conflict"},
	}
	for _, c := range cases {
		_, err := f.svc.Create(f.ctx, f.svcA.ID, c.host, 0, c.in)
		require.Error(t, err, c.name)
		ae, ok := apierr.As(err)
		require.True(t, ok, "%s: %v", c.name, err)
		require.Equal(t, c.code, ae.Code, c.name)
	}
	require.Len(t, f.rows(), 1, "no refused rule left a row behind")

	// Valid redirect and path rules are stored as given.
	v := f.mustCreate("go.example.com", RuleInput{Kind: &redirect, RedirectTo: str(" Target.Example.com. "), RedirectCode: num(301), PreservePath: flag(false)})
	row := f.rows()["go.example.com"]
	require.Equal(t, agentprotocol.DomainRedirect, row.Kind)
	require.Equal(t, "target.example.com", *row.RedirectTo)
	require.EqualValues(t, 301, row.RedirectCode)
	require.False(t, row.PreservePath)
	require.Equal(t, v.ID, row.ID)
	f.mustCreate("api.example.com", RuleInput{PathPrefix: str("/api"), StripPrefix: flag(true), AddPrefix: str("/v2")})
	require.Equal(t, "/api", f.rows()["api.example.com"].PathPrefix)
	// The same host may be used again under another path.
	_, err := f.svc.Create(f.ctx, f.svcA.ID, "api.example.com", 0, RuleInput{PathPrefix: str("/other")})
	require.NoError(t, err)
	_, err = f.svc.Create(f.ctx, f.svcA.ID, "api.example.com", 0, RuleInput{PathPrefix: str("/api")})
	requireCode(t, err, "domain_conflict")
}

func TestPinnedCertificatesMustCoverTheHostname(t *testing.T) {
	f := newWWWFixture(t)
	cert := func(name string, names ...string) uuid.UUID {
		id := uuid.New()
		f.exec(`INSERT INTO certificates (id, workspace_id, server_id, name, domains, cert_pem, key_enc, fingerprint, not_before, not_after)
			VALUES ($1, $2, $3, $4, $5, 'pem', '\x00', 'fp', now(), now() + interval '1 day')`,
			id, f.principal().WorkspaceID, f.server.ID, name, names)
		return id
	}
	exact, wild := cert("exact", "app.example.com"), cert("wild", "*.example.com")
	custom, str := "custom", func(s string) *string { return &s }

	v, err := f.svc.Create(f.ctx, f.svcA.ID, "app.example.com", 0, RuleInput{TLSMode: &custom, CertificateID: str(exact.String())})
	require.NoError(t, err)
	require.Equal(t, exact, *v.CertificateID)
	_, err = f.svc.Create(f.ctx, f.svcA.ID, "www.example.com", 0, RuleInput{TLSMode: &custom, CertificateID: str(wild.String())})
	require.NoError(t, err, "a wildcard covers one label")
	_, err = f.svc.Create(f.ctx, f.svcA.ID, "deep.app.example.com", 0, RuleInput{TLSMode: &custom, CertificateID: str(wild.String())})
	requireCode(t, err, "certificate_mismatch")
	_, err = f.svc.Create(f.ctx, f.svcA.ID, "other.example.org", 0, RuleInput{TLSMode: &custom, CertificateID: str(exact.String())})
	requireCode(t, err, "certificate_mismatch")

	// A certificate staged for removal cannot be pinned.
	f.exec(`UPDATE certificates SET deleted_at = now() WHERE id = $1`, wild)
	_, err = f.svc.Create(f.ctx, f.svcA.ID, "ok.example.com", 0, RuleInput{TLSMode: &custom, CertificateID: str(wild.String())})
	requireCode(t, err, "certificate_mismatch")
	// A pin moves with an update; clearing it needs a mode that takes none.
	auto := "auto"
	_, _, err = f.svc.Update(f.ctx, v.ID, f.gen(), UpdateInput{Rule: RuleInput{TLSMode: &auto, CertificateID: str("")}})
	require.NoError(t, err)
	require.Nil(t, f.rows()["app.example.com"].CertificateID)
}

func TestCertCovers(t *testing.T) {
	for _, c := range []struct {
		names []string
		host  string
		want  bool
	}{
		{[]string{"app.example.com"}, "app.example.com", true},
		{[]string{"APP.example.com"}, "app.EXAMPLE.com", true},
		{[]string{"*.example.com"}, "app.example.com", true},
		{[]string{"*.example.com"}, "example.com", false},
		{[]string{"*.example.com"}, "a.b.example.com", false},
		{[]string{"*.example.com"}, ".example.com", false},
		{[]string{"*.example.com"}, "app.example.org", false},
		{[]string{"a.example.com", "*.example.org"}, "x.example.org", true},
		{nil, "app.example.com", false},
	} {
		require.Equal(t, c.want, CertCovers(c.names, c.host), "%v covers %s", c.names, c.host)
	}
}

func TestRedirectLoopsAreRefused(t *testing.T) {
	f := newWWWFixture(t)
	str := func(s string) *string { return &s }
	redirect := agentprotocol.DomainRedirect

	f.mustCreate("a.example.com", RuleInput{Kind: &redirect, RedirectTo: str("b.example.com")})
	f.mustCreate("b.example.com", RuleInput{Kind: &redirect, RedirectTo: str("c.example.com")})
	_, err := f.svc.Create(f.ctx, f.svcA.ID, "c.example.com", 0, RuleInput{Kind: &redirect, RedirectTo: str("a.example.com")})
	requireCode(t, err, "redirect_loop")
	// The same chain ending somewhere else is fine, and a path redirect is never part of a loop.
	_, err = f.svc.Create(f.ctx, f.svcA.ID, "c.example.com", 0, RuleInput{Kind: &redirect, RedirectTo: str("z.example.com")})
	require.NoError(t, err)
	_, err = f.svc.Create(f.ctx, f.svcA.ID, "z.example.com", 0, RuleInput{Kind: &redirect, RedirectTo: str("a.example.com"), PathPrefix: str("/old")})
	require.NoError(t, err)

	// Changing an existing redirect to close a loop is refused too, and the domain keeps its target.
	c := f.view("c.example.com")
	_, _, err = f.svc.Update(f.ctx, c.ID, f.gen(), UpdateInput{Rule: RuleInput{RedirectTo: str("a.example.com")}})
	requireCode(t, err, "redirect_loop")
	require.Equal(t, "z.example.com", *f.rows()["c.example.com"].RedirectTo)
	// A chain of 26 redirects is too long to follow.
	for i := range 26 {
		from := fmt.Sprintf("h%02d.example.com", i)
		to := fmt.Sprintf("h%02d.example.com", i+1)
		f.mustCreate(from, RuleInput{Kind: &redirect, RedirectTo: str(to)})
	}
	_, err = f.svc.Create(f.ctx, f.svcA.ID, "start.example.com", 0, RuleInput{Kind: &redirect, RedirectTo: str("h00.example.com")})
	requireCode(t, err, "redirect_loop")
}

func TestRulesNeedProxyWriteAndAnAgentThatKnowsThem(t *testing.T) {
	f := newWWWFixture(t)
	compress := &agentprotocol.RouteOptions{Compress: true}

	// A plain hostname needs only projects:write; any proxy field needs proxy:write too.
	_, err := f.svc.Create(f.as(policy.ScopeProjectsWrite), f.svcA.ID, "plain.example.com", 0, RuleInput{})
	require.NoError(t, err)
	_, err = f.svc.Create(f.as(policy.ScopeProjectsWrite), f.svcA.ID, "opts.example.com", 0, RuleInput{Options: compress})
	requireCode(t, err, "forbidden")
	_, err = f.svc.Create(f.as(policy.ScopeProjectsWrite, policy.ScopeProxyWrite), f.svcA.ID, "opts.example.com", 0, RuleInput{Options: compress})
	require.NoError(t, err)
	// A wildcard is a proxy feature.
	_, err = f.svc.Create(f.as(policy.ScopeProjectsWrite), f.svcA.ID, "*.wild.example.com", 0, RuleInput{})
	requireCode(t, err, "forbidden")
	_, err = f.svc.Create(f.ctx, f.svcA.ID, "*.wild.example.com", 0, RuleInput{TLSMode: new("none")})
	require.NoError(t, err)
	_, err = f.svc.Create(f.ctx, f.svcA.ID, "*.bad", 0, RuleInput{})
	requireCode(t, err, "invalid_hostname")

	// An agent that does not advertise proxy v2 would drop the fields.
	old, _ := json.Marshal(agentprotocol.Facts{OS: "linux"})
	f.exec(`UPDATE servers SET facts = $2 WHERE id = $1`, f.server.ID, old)
	_, err = f.svc.Create(f.ctx, f.svcA.ID, "old.example.com", 0, RuleInput{Options: compress})
	requireCode(t, err, "agent_too_old")
	_, err = f.svc.Create(f.ctx, f.svcA.ID, "*.old.example.com", 0, RuleInput{})
	requireCode(t, err, "agent_too_old")
	_, err = f.svc.Create(f.ctx, f.svcA.ID, "old.example.com", 0, RuleInput{})
	require.NoError(t, err, "a plain hostname still works")

	// A proxy that is not running refuses new hostnames.
	f.exec(`UPDATE servers SET proxy_status = 'error', proxy_error = 'port 80 is in use' WHERE id = $1`, f.server.ID)
	_, err = f.svc.Create(f.ctx, f.svcA.ID, "down.example.com", 0, RuleInput{})
	ae := requireCodeErr(t, err, "proxy_unavailable")
	require.Contains(t, ae.Message, "port 80 is in use")
	f.exec(`UPDATE servers SET proxy_error = NULL WHERE id = $1`, f.server.ID)
	_, err = f.svc.Create(f.ctx, f.svcA.ID, "down.example.com", 0, RuleInput{})
	ae = requireCodeErr(t, err, "proxy_unavailable")
	require.NotContains(t, ae.Message, ":")
}

func requireCodeErr(t *testing.T, err error, code string) *apierr.Error {
	t.Helper()
	ae, ok := apierr.As(err)
	require.True(t, ok, "%v", err)
	require.Equal(t, code, ae.Code)
	return ae
}

func TestSupportsProxyV2(t *testing.T) {
	facts := func(f agentprotocol.Facts) db.Server {
		raw, _ := json.Marshal(f)
		return db.Server{Name: "s", Facts: raw}
	}
	require.True(t, SupportsProxyV2(db.Server{}), "a server that never connected is assumed current")
	require.True(t, SupportsProxyV2(db.Server{Facts: []byte(`not json`)}))
	require.True(t, SupportsProxyV2(facts(agentprotocol.Facts{})), "facts without an OS are not facts yet")
	require.False(t, SupportsProxyV2(facts(agentprotocol.Facts{OS: "linux"})))
	require.True(t, SupportsProxyV2(facts(agentprotocol.Facts{OS: "linux", Capabilities: []string{agentprotocol.CapProxyV2}})))
	require.NoError(t, RequireProxyV2(nil))
	srv := facts(agentprotocol.Facts{OS: "linux"})
	requireCode(t, RequireProxyV2(&srv), "agent_too_old")
}

func TestUpdateChangesRulesAndGuardsTheGeneration(t *testing.T) {
	f := newWWWFixture(t)
	str := func(s string) *string { return &s }
	redirect, serve := agentprotocol.DomainRedirect, agentprotocol.DomainServe
	v := f.mustCreate("app.example.com", RuleInput{})

	// Stale generations are refused with the current one.
	_, _, err := f.svc.Update(f.ctx, v.ID, f.gen()-1, UpdateInput{Rule: RuleInput{PathPrefix: str("/x")}})
	ae := requireCodeErr(t, err, "generation_conflict")
	require.Equal(t, 412, ae.Status)
	require.EqualValues(t, f.gen(), ae.Details["currentGeneration"])

	// A real change bumps the generation and is audited with the fields.
	gen := f.gen()
	got, newGen, err := f.svc.Update(f.ctx, v.ID, gen, UpdateInput{Rule: RuleInput{PathPrefix: str("/api"), TLSMode: str("none")}})
	require.NoError(t, err)
	require.Equal(t, gen+1, newGen)
	require.Equal(t, f.gen(), newGen)
	require.Equal(t, "/api", got.PathPrefix)
	require.Equal(t, 1, f.count(`SELECT count(*) FROM audit_events WHERE action='domain.update' AND resource_id=$1 AND metadata->'fields' @> '["pathPrefix","tlsMode"]'::jsonb`, v.ID.String()))

	// Saying nothing new is a no-op: no bump, no audit.
	_, sameGen, err := f.svc.Update(f.ctx, v.ID, newGen, UpdateInput{Rule: RuleInput{PathPrefix: str("/api")}})
	require.NoError(t, err)
	require.Equal(t, newGen, sameGen)
	require.Equal(t, 1, f.auditCount("domain.update", v.ID))

	// Turning it into a redirect and back to serving clears the target.
	_, _, err = f.svc.Update(f.ctx, v.ID, newGen, UpdateInput{Rule: RuleInput{Kind: &redirect, RedirectTo: str("to.example.com"), PathPrefix: str("")}})
	require.NoError(t, err)
	require.Equal(t, "to.example.com", *f.rows()["app.example.com"].RedirectTo)
	_, _, err = f.svc.Update(f.ctx, v.ID, f.gen(), UpdateInput{Rule: RuleInput{Kind: &serve}})
	require.NoError(t, err)
	require.Nil(t, f.rows()["app.example.com"].RedirectTo)
	require.Equal(t, agentprotocol.DomainServe, f.rows()["app.example.com"].Kind)

	// A rename resets the DNS result, queues a check and keeps the rest.
	f.exec(`UPDATE domains SET dns_ok = true WHERE id = $1`, v.ID)
	jobs := f.dnsJobs()
	host := "Renamed.Example.com"
	renamed, _, err := f.svc.Update(f.ctx, v.ID, f.gen(), UpdateInput{Hostname: &host})
	require.NoError(t, err)
	require.Equal(t, "renamed.example.com", renamed.Hostname)
	require.Nil(t, renamed.DNSOk)
	require.Contains(t, renamed.StatusMessage, "renamed.example.com")
	require.Equal(t, jobs+1, f.dnsJobs())
	badHost := "not a host"
	_, _, err = f.svc.Update(f.ctx, v.ID, f.gen(), UpdateInput{Hostname: &badHost})
	requireCode(t, err, "invalid_hostname")
	taken := f.mustCreate("taken.example.com", RuleInput{})
	takenHost := "taken.example.com"
	_, _, err = f.svc.Update(f.ctx, v.ID, f.gen(), UpdateInput{Hostname: &takenHost})
	requireCode(t, err, "domain_conflict")
	_ = taken

	// Unknown domain, no scope, and proxy fields without proxy:write.
	_, _, err = f.svc.Update(f.ctx, uuid.New(), 1, UpdateInput{})
	requireCode(t, err, "not_found")
	_, _, err = f.svc.Update(f.as(policy.ScopeProjectsRead), v.ID, f.gen(), UpdateInput{Hostname: &host})
	requireCode(t, err, "forbidden")
	_, _, err = f.svc.Update(f.as(policy.ScopeProjectsWrite), v.ID, f.gen(), UpdateInput{Rule: RuleInput{PathPrefix: str("/z")}})
	requireCode(t, err, "forbidden")
	// A hostname change alone needs projects:write only, but not a stopped proxy.
	f.exec(`UPDATE servers SET proxy_status = 'error' WHERE id = $1`, f.server.ID)
	other := "again.example.com"
	_, _, err = f.svc.Update(f.ctx, v.ID, f.gen(), UpdateInput{Hostname: &other})
	requireCode(t, err, "proxy_unavailable")
	_, _, err = f.svc.Update(f.ctx, v.ID, f.gen(), UpdateInput{Rule: RuleInput{PathPrefix: str("/still-ok")}})
	require.NoError(t, err, "a rule change does not need the proxy to be up")
}

func TestUpdateProxyFeaturesNeedAnUpToDateAgent(t *testing.T) {
	f := newWWWFixture(t)
	v := f.mustCreate("app.example.com", RuleInput{})
	old, _ := json.Marshal(agentprotocol.Facts{OS: "linux"})
	f.exec(`UPDATE servers SET facts = $2 WHERE id = $1`, f.server.ID, old)
	_, _, err := f.svc.Update(f.ctx, v.ID, f.gen(), UpdateInput{Rule: RuleInput{Options: &agentprotocol.RouteOptions{Compress: true}}})
	requireCode(t, err, "agent_too_old")
	_, err = f.svc.SetAuthUsers(f.ctx, v.ID, []AuthUserInput{{Username: "a", Password: "longenough"}})
	requireCode(t, err, "agent_too_old")
}

func TestBasicAuthUsers(t *testing.T) {
	f := newWWWFixture(t)
	v := f.mustCreate("secret.example.com", RuleInput{})
	users := func(us ...AuthUserInput) ([]string, error) { return f.svc.SetAuthUsers(f.ctx, v.ID, us) }

	// Validation first; nothing is stored by a refused request.
	gen := f.gen()
	for name, tc := range map[string][]AuthUserInput{
		"bad name":           {{Username: "has space", Password: "longenough"}},
		"empty name":         {{Username: "", Password: "longenough"}},
		"duplicate":          {{Username: "ann", Password: "longenough"}, {Username: "ann", Password: "longenough2"}},
		"password missing":   {{Username: "ann"}},
		"password too short": {{Username: "ann", Password: "short"}},
		"password too long":  {{Username: "ann", Password: strings.Repeat("x", 73)}},
	} {
		_, err := users(tc...)
		requireCode(t, err, "invalid_auth_users")
		_ = name
	}
	many := make([]AuthUserInput, maxAuthUsers+1)
	for i := range many {
		many[i] = AuthUserInput{Username: "u" + string(rune('a'+i/26)) + string(rune('a'+i%26)), Password: "longenough"}
	}
	_, err := users(many...)
	requireCode(t, err, "invalid_auth_users")
	require.Equal(t, gen, f.gen())

	// Users are stored sealed; the names come back sorted.
	names, err := users(AuthUserInput{Username: "zed", Password: "zed-password"}, AuthUserInput{Username: "ann", Password: "ann-password"})
	require.NoError(t, err)
	require.Equal(t, []string{"ann", "zed"}, names)
	require.Equal(t, gen+1, f.gen())
	require.Equal(t, 1, f.auditCount("domain.auth.set", v.ID))
	require.Equal(t, []string{"ann", "zed"}, f.view("secret.example.com").AuthUsers)
	hashes, err := f.q.ListDomainAuthHashes(context.Background(), []uuid.UUID{v.ID})
	require.NoError(t, err)
	require.Len(t, hashes, 2)
	for _, h := range hashes {
		require.NotContains(t, string(h.HashEnc), "password", "only sealed hashes are stored")
	}

	// A user without a password keeps the stored hash; users not listed go.
	_, err = users(AuthUserInput{Username: "ann"}, AuthUserInput{Username: "new", Password: "new-password"})
	require.NoError(t, err)
	require.Equal(t, []string{"ann", "new"}, f.view("secret.example.com").AuthUsers)

	// Enabling basic auth needs users (the options validator checks), and the rule then carries a reference.
	_, err = users()
	require.NoError(t, err, "with basic auth off the last user may go")
	require.Empty(t, f.view("secret.example.com").AuthUsers)
	_, _, err = f.svc.Update(f.ctx, v.ID, f.gen(), UpdateInput{Rule: RuleInput{Options: &agentprotocol.RouteOptions{BasicAuth: true}}})
	requireCode(t, err, "basic_auth_needs_users")
	_, err = users(AuthUserInput{Username: "ann", Password: "ann-password"}, AuthUserInput{Username: "bob", Password: "bob-password"})
	require.NoError(t, err)
	_, _, err = f.svc.Update(f.ctx, v.ID, f.gen(), UpdateInput{Rule: RuleInput{Options: &agentprotocol.RouteOptions{BasicAuth: true}}})
	require.NoError(t, err)

	// With basic auth on, the last user cannot be removed.
	_, err = users()
	requireCode(t, err, "basic_auth_needs_users")
	require.Equal(t, []string{"ann", "bob"}, f.view("secret.example.com").AuthUsers)
	requireCode(t, f.svc.DeleteAuthUser(f.ctx, v.ID, "nobody"), "not_found")
	require.NoError(t, f.svc.DeleteAuthUser(f.ctx, v.ID, "bob"))
	require.Equal(t, []string{"ann"}, f.view("secret.example.com").AuthUsers)
	requireCode(t, f.svc.DeleteAuthUser(f.ctx, v.ID, "ann"), "basic_auth_needs_users")
	requireCode(t, f.svc.DeleteAuthUser(f.ctx, uuid.New(), "ann"), "not_found")

	// Permissions: proxy:write is needed on top of projects:write.
	_, err = f.svc.SetAuthUsers(f.as(policy.ScopeProjectsWrite), v.ID, []AuthUserInput{{Username: "ann"}})
	requireCode(t, err, "forbidden")
	requireCode(t, f.svc.DeleteAuthUser(f.as(policy.ScopeProjectsWrite), v.ID, "ann"), "forbidden")
	_, err = f.svc.SetAuthUsers(f.ctx, uuid.New(), nil)
	requireCode(t, err, "not_found")

	// A redirect takes no users.
	redirect := agentprotocol.DomainRedirect
	to := "secret.example.com"
	rd := f.mustCreate("moved.example.com", RuleInput{Kind: &redirect, RedirectTo: &to})
	_, err = f.svc.SetAuthUsers(f.ctx, rd.ID, []AuthUserInput{{Username: "ann", Password: "ann-password"}})
	requireCode(t, err, "invalid_redirect")
}

func TestBasicAuthSecretsReachTheAgentAsBcryptLines(t *testing.T) {
	f := newWWWFixture(t)
	v := f.mustCreate("secret.example.com", RuleInput{})
	_, err := f.svc.SetAuthUsers(f.ctx, v.ID, []AuthUserInput{{Username: "ann", Password: "ann-password"}, {Username: "bob", Password: "bob-password"}})
	require.NoError(t, err)
	_, _, err = f.svc.Update(f.ctx, v.ID, f.gen(), UpdateInput{Rule: RuleInput{Options: &agentprotocol.RouteOptions{BasicAuth: true}}})
	require.NoError(t, err)
	plain := f.mustCreate("plain.example.com", RuleInput{})
	f.deployService(f.svcA.ID, services.Config{Image: "traefik/whoami:latest", Port: 80}.WithDefaults())

	routes, err := ServerRoutes(context.Background(), f.q, f.server.ID, uuid.Nil)
	require.NoError(t, err)
	var rule agentprotocol.DomainRule
	for _, rt := range routes {
		for _, d := range rt.Domains {
			if d.Host == "secret.example.com" {
				rule = d
			}
			if d.Host == "plain.example.com" {
				require.Empty(t, d.AuthRef)
			}
		}
	}
	require.Equal(t, agentprotocol.SecretAuthPrefix+v.ID.String(), rule.AuthRef, "the rule names the secret, never carries it")
	_ = plain

	box, err := secretbox.New(bytesOf(7))
	require.NoError(t, err)
	secrets, err := AuthSecrets(context.Background(), f.q, box, routes, routes)
	require.NoError(t, err)
	require.Len(t, secrets, 1)
	lines := strings.Split(secrets[rule.AuthRef], "\n")
	require.Len(t, lines, 2)
	byUser := map[string]string{}
	for _, l := range lines {
		u, h, ok := strings.Cut(l, ":")
		require.True(t, ok)
		byUser[u] = h
	}
	require.NoError(t, bcrypt.CompareHashAndPassword([]byte(byUser["ann"]), []byte("ann-password")))
	require.NoError(t, bcrypt.CompareHashAndPassword([]byte(byUser["bob"]), []byte("bob-password")))
	require.Error(t, bcrypt.CompareHashAndPassword([]byte(byUser["ann"]), []byte("bob-password")))

	// The same through loose rules (a deploy payload's DomainRules).
	loose, err := RuleSecrets(context.Background(), f.q, box, []agentprotocol.DomainRule{rule, {ID: uuid.NewString(), Host: "x"}})
	require.NoError(t, err)
	require.Equal(t, secrets, loose)
	none, err := RuleSecrets(context.Background(), f.q, box, nil)
	require.NoError(t, err)
	require.Empty(t, none)

	// A rule with a broken id is a bug in the caller, reported rather than skipped.
	_, err = RuleSecrets(context.Background(), f.q, box, []agentprotocol.DomainRule{{ID: "nope", AuthRef: "secret:auth:nope"}})
	require.ErrorContains(t, err, "domain id")
	_, err = AuthSecrets(context.Background(), f.q, box, []agentprotocol.Route{{Domains: []agentprotocol.DomainRule{{ID: "nope", AuthRef: "x"}}}})
	require.ErrorContains(t, err, "domain id")

	// A box with another key cannot open the hashes: the send fails instead of sending garbage.
	wrong, err := secretbox.New(bytesOf(9))
	require.NoError(t, err)
	_, err = AuthSecrets(context.Background(), f.q, wrong, routes)
	require.ErrorContains(t, err, "decrypt auth hash")
}

func bytesOf(b byte) []byte {
	out := make([]byte, secretbox.KeySize)
	for i := range out {
		out[i] = b
	}
	return out
}

func TestDNSCheckRecordsTheResultAndAnnouncesChanges(t *testing.T) {
	f := newWWWFixture(t)
	v := f.mustCreate("app.example.com", RuleInput{})
	var changed []uuid.UUID
	c := &DNSChecker{q: f.q, resolver: fakeResolver{addrs: []string{"203.0.113.9"}}, logger: nopLogger(),
		onChange: func(_ context.Context, id uuid.UUID) { changed = append(changed, id) }}

	require.NoError(t, c.Check(context.Background(), v.ID))
	row, err := f.q.GetDomainByID(context.Background(), v.ID)
	require.NoError(t, err)
	require.NotNil(t, row.DnsOk)
	require.True(t, *row.DnsOk)
	require.Equal(t, "DNS points to 203.0.113.9.", row.StatusMessage)
	require.Equal(t, []uuid.UUID{v.ID}, changed)

	// The same answer again announces nothing.
	require.NoError(t, c.Check(context.Background(), v.ID))
	require.Len(t, changed, 1)
	// A different answer does.
	c.resolver = fakeResolver{addrs: []string{"198.51.100.1"}}
	require.NoError(t, c.Check(context.Background(), v.ID))
	row, _ = f.q.GetDomainByID(context.Background(), v.ID)
	require.False(t, *row.DnsOk)
	require.Contains(t, row.StatusMessage, "currently resolves to 198.51.100.1")
	require.Len(t, changed, 2)

	// A deleted domain is nothing to check.
	require.NoError(t, c.Check(context.Background(), uuid.New()))

	// A domain in a connected Cloudflare zone is judged by its record's status, not by a lookup.
	f.exec(`UPDATE domains SET cf_zone_id='z1', cf_zone_name='example.com', cf_status='managed', cf_proxied=true WHERE id=$1`, v.ID)
	c.resolver = fakeResolver{err: errors.New("must not be asked")}
	require.NoError(t, c.Check(context.Background(), v.ID))
	row, _ = f.q.GetDomainByID(context.Background(), v.ID)
	require.True(t, *row.DnsOk)
	require.Equal(t, "DNS is proxied through Cloudflare.", row.StatusMessage)
}

func TestCloudflareDNSStatuses(t *testing.T) {
	for _, c := range []struct {
		status  string
		proxied bool
		message string
		want    *bool
		text    string
	}{
		{"managed", false, "", new(true), "DNS points to this server through Cloudflare."},
		{"present", true, "", new(true), "DNS is proxied through Cloudflare."},
		{"conflict", false, "", new(false), "A record that NebulaCtrl did not create is in the way"},
		{"conflict", false, "mine", new(false), "mine"},
		{"missing", false, "", new(false), "setting up this record in your Cloudflare zone example.com"},
		{"wrong_target", false, "", new(false), "setting up this record"},
		{"", false, "", nil, "checks this record in your Cloudflare zone example.com"},
	} {
		ok, msg := cloudflareDNS(db.Domain{CfStatus: c.status, CfProxied: c.proxied, CfMessage: c.message, CfZoneName: "example.com"})
		if c.want == nil {
			require.Nil(t, ok, c.status)
		} else {
			require.NotNil(t, ok, c.status)
			require.Equal(t, *c.want, *ok, c.status)
		}
		require.Contains(t, msg, c.text, c.status)
	}
}

func TestDNSCheckMatchesNewDomainsAgainstTheTeamsZones(t *testing.T) {
	f := newWWWFixture(t)
	v := f.mustCreate("app.example.com", RuleInput{})
	var matched []uuid.UUID
	c := &DNSChecker{q: f.q, resolver: fakeResolver{addrs: []string{"203.0.113.9"}}, logger: nopLogger(),
		match: func(_ context.Context, id uuid.UUID) error {
			matched = append(matched, id)
			f.exec(`UPDATE domains SET cf_zone_id='z1', cf_status='managed' WHERE id=$1`, id)
			return nil
		}}
	require.NoError(t, c.Check(context.Background(), v.ID))
	require.Equal(t, []uuid.UUID{v.ID}, matched)
	row, _ := f.q.GetDomainByID(context.Background(), v.ID)
	require.Equal(t, "DNS points to this server through Cloudflare.", row.StatusMessage, "the refreshed row was used")
	// Once matched it is not matched again.
	require.NoError(t, c.Check(context.Background(), v.ID))
	require.Len(t, matched, 1)

	// A matcher that fails is logged, and the check still happens.
	f.exec(`UPDATE domains SET cf_zone_id='', cf_status='' WHERE id=$1`, v.ID)
	c.match = func(context.Context, uuid.UUID) error { return errors.New("zone sync down") }
	require.NoError(t, c.Check(context.Background(), v.ID))
	row, _ = f.q.GetDomainByID(context.Background(), v.ID)
	require.Equal(t, "DNS points to 203.0.113.9.", row.StatusMessage)
}

func TestDNSSweepChecksEveryDomainAndToleratesFailures(t *testing.T) {
	f := newWWWFixture(t)
	a := f.mustCreate("a.example.com", RuleInput{})
	b := f.mustCreate("b.example.com", RuleInput{})
	c := &DNSChecker{q: f.q, resolver: fakeResolver{addrs: []string{"203.0.113.9"}}, logger: nopLogger()}
	w := &dnsWorker{c: c}

	// A job without a domain sweeps all of them.
	require.NoError(t, w.Work(context.Background(), jobOf(DNSCheckArgs{})))
	for _, id := range []uuid.UUID{a.ID, b.ID} {
		row, _ := f.q.GetDomainByID(context.Background(), id)
		require.NotNil(t, row.DnsOk, id.String())
		require.True(t, *row.DnsOk)
	}
	// A job for one domain checks only that one (and bypasses cached lookups).
	f.exec(`UPDATE domains SET dns_ok = NULL WHERE true`)
	require.NoError(t, w.Work(context.Background(), jobOf(DNSCheckArgs{DomainID: &a.ID})))
	rowA, _ := f.q.GetDomainByID(context.Background(), a.ID)
	rowB, _ := f.q.GetDomainByID(context.Background(), b.ID)
	require.NotNil(t, rowA.DnsOk)
	require.Nil(t, rowB.DnsOk)

	// publishDomain is quiet for a domain that is gone and tells the bus about one that is not.
	f.svc.publishDomain(context.Background(), uuid.New())
	f.svc.publishDomain(context.Background(), a.ID)
	require.Equal(t, "domain_dns_check", DNSCheckArgs{}.Kind())
	require.Equal(t, 10*time.Minute, DNSCheckInterval)
}

func nopLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func jobOf(args DNSCheckArgs) *river.Job[DNSCheckArgs] { return &river.Job[DNSCheckArgs]{Args: args} }
