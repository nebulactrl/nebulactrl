package dns01_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/api"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/dns01"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/testutil"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

const token = "cf-http-token-424242"

type fx struct {
	t     *testing.T
	q     *db.Queries
	svc   *dns01.Service
	ws    db.Workspace
	srv   db.Server
	admin context.Context
	dev   context.Context
}

func newFx(t *testing.T) *fx {
	t.Helper()
	pool := testutil.NewPool(t)
	q := db.New(pool)
	bg := context.Background()
	ws, err := q.CreateWorkspace(bg, "w")
	require.NoError(t, err)
	email := "ops@example.com"
	srv, err := q.CreateServer(bg, db.CreateServerParams{WorkspaceID: ws.ID, Name: "edge", AcmeEmail: &email})
	require.NoError(t, err)
	_, err = pool.Exec(bg, "UPDATE servers SET base_domain = 'example.net' WHERE id = $1", srv.ID)
	require.NoError(t, err)
	box, err := secretbox.New(bytes.Repeat([]byte{5}, 32))
	require.NoError(t, err)
	as := func(role string, scopes []string) context.Context {
		return auth.WithPrincipal(context.Background(), &auth.Principal{Type: auth.TypeUser, UserID: uuid.New(), WorkspaceID: ws.ID, Role: role, Label: "t", Scopes: scopes})
	}
	return &fx{
		t: t, q: q, svc: dns01.NewService(pool, nil, box), ws: ws, srv: srv,
		admin: as(auth.RoleAdmin, policy.AllScopes),
		dev:   as(auth.RoleDeveloper, []string{policy.ScopeProxyWrite, policy.ScopeServersRead}),
	}
}

func (f *fx) call(ctx context.Context, method, path string, body any) *httptest.ResponseRecorder {
	f.t.Helper()
	p, err := policy.Authenticated(ctx)
	require.NoError(f.t, err)
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req.WithContext(auth.WithPrincipal(req.Context(), p)))
		})
	})
	cfg := huma.DefaultConfig("test", "1")
	cfg.CreateHooks = nil
	cfg.Components.Schemas = api.NewSchemaRegistry()
	dns01.Register(humachi.New(r, cfg), f.svc)
	rd := bytes.NewReader(nil)
	if body != nil {
		raw, err := json.Marshal(body)
		require.NoError(f.t, err)
		rd = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func (f *fx) expect(rec *httptest.ResponseRecorder, status int) *httptest.ResponseRecorder {
	f.t.Helper()
	require.Equal(f.t, status, rec.Code, rec.Body.String())
	return rec
}

func (f *fx) errCode(rec *httptest.ResponseRecorder) string {
	f.t.Helper()
	var e struct {
		Code string `json:"code"`
	}
	require.NoError(f.t, json.Unmarshal(rec.Body.Bytes(), &e), rec.Body.String())
	return e.Code
}

func (f *fx) state(rec *httptest.ResponseRecorder) dns01.ServerDNS01 {
	f.t.Helper()
	var st dns01.ServerDNS01
	require.NoError(f.t, json.Unmarshal(rec.Body.Bytes(), &st), rec.Body.String())
	return st
}

func (f *fx) cred(rec *httptest.ResponseRecorder) dns01.DNSCredential {
	f.t.Helper()
	var c dns01.DNSCredential
	require.NoError(f.t, json.Unmarshal(rec.Body.Bytes(), &c), rec.Body.String())
	return c
}

func (f *fx) createCred(name, value string) dns01.DNSCredential {
	f.t.Helper()
	return f.cred(f.expect(f.call(f.admin, http.MethodPost, "/dns/credentials", map[string]any{
		"name": name, "provider": "cloudflare", "values": map[string]string{"CF_DNS_API_TOKEN": value},
	}), http.StatusCreated))
}

func (f *fx) server() db.Server {
	f.t.Helper()
	row, err := f.q.LoadServerRow(context.Background(), db.LoadServerRowParams{ID: f.srv.ID})
	require.NoError(f.t, err)
	return row
}

// deployProxy records the desired DNS-01 configuration as applied, the way a
// successful proxy_configure does.
func (f *fx) deployProxy() {
	f.t.Helper()
	snap, err := dns01.Desired(context.Background(), f.q, f.srv.ID)
	require.NoError(f.t, err)
	var pc agentprotocol.ProxyConfig
	snap.Apply(&pc)
	require.NoError(f.t, f.q.SetServerAppliedDNS01(context.Background(), db.SetServerAppliedDNS01Params{ID: f.srv.ID, AppliedDns01: dns01.SnapshotOf(pc)}))
}

func (f *fx) plan() dns01.Plan {
	f.t.Helper()
	p, err := dns01.PlanFor(context.Background(), f.q, f.server())
	require.NoError(f.t, err)
	return p
}

func diffOf(p dns01.Plan, field string) (dns01.PlanChange, bool) {
	for _, c := range p.Diff {
		if c.Field == field {
			return c, true
		}
	}
	return dns01.PlanChange{}, false
}

func hasWarning(p dns01.Plan, part string) bool {
	for _, w := range p.Warnings {
		if strings.Contains(w, part) {
			return true
		}
	}
	return false
}

func TestHTTPProviderCatalogs(t *testing.T) {
	f := newFx(t)
	codes := func(path string) map[string]bool {
		rec := f.expect(f.call(f.dev, http.MethodGet, path, nil), http.StatusOK)
		var out struct {
			Items []agentprotocol.DNSProviderSpec `json:"items"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
		require.NotEmpty(t, out.Items)
		m := map[string]bool{}
		for _, it := range out.Items {
			m[it.Code] = true
		}
		return m
	}
	curated, lego := codes("/dns/providers"), codes("/dns/lego-providers")
	require.True(t, curated["cloudflare"] && curated["route53"], "the curated list has the common providers")
	for code := range lego {
		require.False(t, curated[code], "%s is curated and must not be in the generic list", code)
	}

	none := auth.WithPrincipal(context.Background(), &auth.Principal{Type: auth.TypeUser, UserID: uuid.New(), WorkspaceID: f.ws.ID, Scopes: []string{policy.ScopeLogsRead}})
	f.expect(f.call(none, http.MethodGet, "/dns/providers", nil), http.StatusForbidden)
	f.expect(f.call(none, http.MethodGet, "/dns/lego-providers", nil), http.StatusForbidden)
}

func TestHTTPCredentialLifecycle(t *testing.T) {
	f := newFx(t)
	body := map[string]any{"name": "cf", "provider": "cloudflare", "values": map[string]string{"CF_DNS_API_TOKEN": token}}

	// Only administrators write credentials.
	f.expect(f.call(f.dev, http.MethodPost, "/dns/credentials", body), http.StatusForbidden)
	rec := f.expect(f.call(f.admin, http.MethodPost, "/dns/credentials", map[string]any{
		"name": "bad", "provider": "no-such-provider", "values": map[string]string{"X": "1"},
	}), http.StatusUnprocessableEntity)
	require.Equal(t, "invalid_provider", f.errCode(rec))
	rec = f.expect(f.call(f.admin, http.MethodPost, "/dns/credentials", map[string]any{
		"name": "bad", "provider": "cloudflare", "values": map[string]string{"PATH": "/tmp"},
	}), http.StatusUnprocessableEntity)
	require.NotEmpty(t, f.errCode(rec))

	rec = f.expect(f.call(f.admin, http.MethodPost, "/dns/credentials", body), http.StatusCreated)
	cred := f.cred(rec)
	require.NotContains(t, rec.Body.String(), token, "a secret value never leaves the API")
	require.Equal(t, "cf", cred.Name)
	require.Equal(t, "team", cred.Scope)
	require.Equal(t, 1, cred.Rev)
	require.True(t, cred.Fields[0].Set)
	require.Nil(t, cred.Fields[0].Value)

	rec = f.expect(f.call(f.admin, http.MethodPost, "/dns/credentials", body), http.StatusConflict)
	require.Equal(t, "credential_name_taken", f.errCode(rec))

	// A credential scoped to the server.
	rec = f.expect(f.call(f.admin, http.MethodPost, "/dns/credentials", map[string]any{
		"name": "edge-only", "provider": "cloudflare", "serverId": f.srv.ID, "values": map[string]string{"CF_DNS_API_TOKEN": "edge-token-1"},
	}), http.StatusCreated)
	scoped := f.cred(rec)
	require.Equal(t, "server", scoped.Scope)
	require.Equal(t, f.srv.ID, *scoped.ServerID)

	list := func(ctx context.Context, query string) []dns01.DNSCredential {
		rec := f.expect(f.call(ctx, http.MethodGet, "/dns/credentials"+query, nil), http.StatusOK)
		var out struct {
			Items []dns01.DNSCredential `json:"items"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
		require.NotContains(t, rec.Body.String(), token)
		return out.Items
	}
	require.Len(t, list(f.dev, ""), 2)
	require.Len(t, list(f.dev, "?serverId="+f.srv.ID.String()), 2, "team-wide and the server's own")
	other, err := f.q.CreateServer(context.Background(), db.CreateServerParams{WorkspaceID: f.ws.ID, Name: "other"})
	require.NoError(t, err)
	only := list(f.dev, "?serverId="+other.ID.String())
	require.Len(t, only, 1, "another server's credentials are not listed: only the team-wide one")
	require.Equal(t, "cf", only[0].Name)
	f.expect(f.call(f.dev, http.MethodGet, "/dns/credentials?serverId="+uuid.NewString(), nil), http.StatusNotFound)
	f.expect(f.call(f.dev, http.MethodGet, "/dns/credentials?serverId=not-a-uuid", nil), http.StatusUnprocessableEntity)

	// Rename and rotate; only administrators may.
	f.expect(f.call(f.dev, http.MethodPatch, "/dns/credentials/"+cred.ID.String(), map[string]any{"name": "renamed"}), http.StatusForbidden)
	rec = f.expect(f.call(f.admin, http.MethodPatch, "/dns/credentials/"+cred.ID.String(), map[string]any{"name": "renamed"}), http.StatusOK)
	require.Equal(t, "renamed", f.cred(rec).Name)
	require.Equal(t, 1, f.cred(rec).Rev, "a rename does not rotate")
	rec = f.expect(f.call(f.admin, http.MethodPatch, "/dns/credentials/"+cred.ID.String(), map[string]any{"values": map[string]string{"CF_DNS_API_TOKEN": "rotated-token-2"}}), http.StatusOK)
	require.Equal(t, 2, f.cred(rec).Rev)
	f.expect(f.call(f.admin, http.MethodPatch, "/dns/credentials/"+uuid.NewString(), map[string]any{"name": "x"}), http.StatusNotFound)
	rec = f.expect(f.call(f.admin, http.MethodPatch, "/dns/credentials/"+cred.ID.String(), map[string]any{"name": "edge-only"}), http.StatusConflict)
	require.NotEmpty(t, f.errCode(rec), "names are unique per team")

	// A credential a resolver uses cannot be deleted.
	f.expect(f.call(f.dev, http.MethodPut, "/servers/"+f.srv.ID.String()+"/dns01/resolvers/cloudflare", map[string]any{"credentialId": cred.ID}), http.StatusOK)
	rec = f.expect(f.call(f.admin, http.MethodDelete, "/dns/credentials/"+cred.ID.String(), nil), http.StatusConflict)
	require.Equal(t, "credential_in_use", f.errCode(rec))
	for _, c := range list(f.admin, "") {
		if c.ID == cred.ID {
			require.Len(t, c.UsedBy, 1)
			require.Equal(t, "edge", c.UsedBy[0].ServerName)
		} else {
			require.Empty(t, c.UsedBy)
		}
	}

	f.expect(f.call(f.dev, http.MethodDelete, "/dns/credentials/"+scoped.ID.String(), nil), http.StatusForbidden)
	f.expect(f.call(f.admin, http.MethodDelete, "/dns/credentials/"+scoped.ID.String(), nil), http.StatusNoContent)
	f.expect(f.call(f.admin, http.MethodDelete, "/dns/credentials/"+scoped.ID.String(), nil), http.StatusNotFound)
	require.Len(t, list(f.admin, ""), 1)
}

func TestHTTPResolversAndWildcardsStageUntilTheProxyIsDeployed(t *testing.T) {
	f := newFx(t)
	cred := f.createCred("cf", token)
	base := "/servers/" + f.srv.ID.String() + "/dns01"

	st := f.state(f.expect(f.call(f.dev, http.MethodGet, base, nil), http.StatusOK))
	require.Empty(t, st.Resolvers)
	require.Empty(t, st.Wildcards)
	require.False(t, st.Staged)
	require.Equal(t, "ops@example.com", st.ACMEEmail)
	require.Equal(t, "example.net", st.BaseDomain)

	// Resolver input checks.
	rec := f.expect(f.call(f.dev, http.MethodPut, base+"/resolvers/cloudflare", map[string]any{"credentialId": uuid.New()}), http.StatusUnprocessableEntity)
	require.Equal(t, "invalid_credential", f.errCode(rec))
	rec = f.expect(f.call(f.dev, http.MethodPut, base+"/resolvers/route53", map[string]any{"credentialId": cred.ID}), http.StatusUnprocessableEntity)
	require.Equal(t, "invalid_credential", f.errCode(rec), "the credential is for cloudflare")
	f.expect(f.call(f.dev, http.MethodPut, base+"/resolvers/cloudflare", map[string]any{"credentialId": cred.ID, "delaySeconds": 601}), http.StatusUnprocessableEntity)
	rec = f.expect(f.call(f.dev, http.MethodPut, base+"/resolvers/cloudflare", map[string]any{"credentialId": cred.ID, "delaySeconds": 0}), http.StatusOK)
	st = f.state(rec)
	require.True(t, st.Staged)
	require.Len(t, st.Resolvers, 1)
	require.Equal(t, dns01.ChangeAdd, st.Resolvers[0].Change)
	require.Equal(t, "cf", st.Resolvers[0].CredentialName)
	require.Equal(t, cred.ID, st.Resolvers[0].CredentialID)
	require.NotContains(t, rec.Body.String(), token)
	f.expect(f.call(f.dev, http.MethodPut, "/servers/"+uuid.NewString()+"/dns01/resolvers/cloudflare", map[string]any{"credentialId": cred.ID}), http.StatusNotFound)

	// Wildcards: they need a resolver of their provider and a base domain.
	wild := map[string]any{"domain": "Example.NET", "provider": "cloudflare", "includeApex": true, "default": true}
	rec = f.expect(f.call(f.dev, http.MethodPost, base+"/wildcards", map[string]any{"domain": "example.org", "provider": "route53", "includeApex": false}), http.StatusUnprocessableEntity)
	require.Equal(t, "resolver_not_configured", f.errCode(rec))
	rec = f.expect(f.call(f.dev, http.MethodPost, base+"/wildcards", map[string]any{"domain": "localhost", "provider": "cloudflare", "includeApex": false}), http.StatusUnprocessableEntity)
	require.Equal(t, "invalid_hostname", f.errCode(rec))
	rec = f.expect(f.call(f.dev, http.MethodPost, base+"/wildcards", wild), http.StatusCreated)
	st = f.state(rec)
	require.Len(t, st.Wildcards, 1)
	w1 := st.Wildcards[0]
	require.Equal(t, "example.net", w1.Domain, "the domain is normalised")
	require.Equal(t, []string{"*.example.net", "example.net"}, w1.Names)
	require.True(t, w1.Default)
	require.Equal(t, dns01.ChangeAdd, w1.Change)
	rec = f.expect(f.call(f.dev, http.MethodPost, base+"/wildcards", wild), http.StatusConflict)
	require.Equal(t, "wildcard_exists", f.errCode(rec))

	// Helpers the proxy and domain code use.
	ok, err := dns01.HasResolver(context.Background(), f.q, f.srv.ID, "cloudflare")
	require.NoError(t, err)
	require.True(t, ok)
	ok, err = dns01.HasResolver(context.Background(), f.q, f.srv.ID, "route53")
	require.NoError(t, err)
	require.False(t, ok)
	covered := func(host string, generated bool) bool {
		ok, err := dns01.WildcardCovered(context.Background(), f.q, f.srv.ID, host, generated)
		require.NoError(t, err)
		return ok
	}
	require.True(t, covered("app.example.net", false))
	require.True(t, covered("example.net", false), "the apex is included")
	require.True(t, covered("*.example.net", false))
	require.False(t, covered("a.b.example.net", false), "a wildcard covers one label")
	require.False(t, covered("app.example.org", false))
	require.False(t, covered("app.example.net", true), "generated names need the certificate to opt in")

	// Deploy the proxy: nothing is staged any more and the plan is empty.
	f.deployProxy()
	st = f.state(f.expect(f.call(f.dev, http.MethodGet, base, nil), http.StatusOK))
	require.False(t, st.Staged)
	require.Equal(t, dns01.ChangeNone, st.Resolvers[0].Change)
	require.Equal(t, dns01.ChangeNone, st.Wildcards[0].Change)
	p := f.plan()
	require.Empty(t, p.Diff)
	require.False(t, p.Restart)
	require.Empty(t, p.Warnings)

	// Changing the wildcard's options is a staged update, and covering generated
	// names of another base domain is flagged.
	rec = f.expect(f.call(f.dev, http.MethodPatch, "/dns01/wildcards/"+w1.ID.String(), map[string]any{"includeApex": false, "coverGenerated": true}), http.StatusOK)
	st = f.state(rec)
	require.Equal(t, dns01.ChangeUpdate, st.Wildcards[0].Change)
	require.Equal(t, []string{"*.example.net"}, st.Wildcards[0].Names)
	require.True(t, st.Wildcards[0].CoverGenerated)
	require.True(t, st.Wildcards[0].Default, "unset options keep their value")
	p = f.plan()
	c, found := diffOf(p, "dns01.wildcard.example.net")
	require.True(t, found)
	require.Equal(t, "*.example.net, example.net; default certificate", c.From)
	require.Equal(t, "*.example.net; covers generated domains; default certificate", c.To)
	require.False(t, p.Restart, "wildcards do not restart the proxy")
	f.expect(f.call(f.dev, http.MethodPatch, "/dns01/wildcards/"+uuid.NewString(), map[string]any{"includeApex": true}), http.StatusNotFound)

	// The second default takes over from the first.
	rec = f.expect(f.call(f.dev, http.MethodPost, base+"/wildcards", map[string]any{"domain": "example.org", "provider": "cloudflare", "includeApex": false, "default": true, "coverGenerated": true}), http.StatusCreated)
	st = f.state(rec)
	require.Len(t, st.Wildcards, 2)
	defaults := 0
	var w2 dns01.WildcardCertView
	for _, w := range st.Wildcards {
		if w.Default {
			defaults++
			w2 = w
		}
	}
	require.Equal(t, 1, defaults, "one default certificate per server")
	require.Equal(t, "example.org", w2.Domain)
	p = f.plan()
	require.True(t, hasWarning(p, "*.example.org covers generated domains only when it matches the server's base domain (example.net)"))
	require.True(t, hasWarning(p, "Requesting *.example.org needs cloudflare to accept TXT records"))
	c, found = diffOf(p, "dns01.wildcard.example.org")
	require.True(t, found)
	require.Equal(t, "-", c.From)
	require.Equal(t, "*.example.org via cloudflare", c.To)

	// A resolver with wildcards cannot go.
	rec = f.expect(f.call(f.dev, http.MethodDelete, base+"/resolvers/cloudflare", nil), http.StatusConflict)
	require.Equal(t, "resolver_in_use", f.errCode(rec))

	// Removing an applied wildcard shows as a removal until the proxy is deployed;
	// removing a staged one just drops it.
	f.expect(f.call(f.dev, http.MethodDelete, "/dns01/wildcards/"+w2.ID.String(), nil), http.StatusOK)
	rec = f.expect(f.call(f.dev, http.MethodDelete, "/dns01/wildcards/"+w1.ID.String(), nil), http.StatusOK)
	st = f.state(rec)
	require.Len(t, st.Wildcards, 1)
	require.Equal(t, dns01.ChangeRemove, st.Wildcards[0].Change)
	require.Equal(t, w1.ID, st.Wildcards[0].ID)
	require.True(t, st.Staged)
	p = f.plan()
	c, found = diffOf(p, "dns01.wildcard.example.net")
	require.True(t, found)
	require.Equal(t, "*.example.net", c.From)
	require.Equal(t, "removed", c.To)
	f.expect(f.call(f.dev, http.MethodDelete, "/dns01/wildcards/"+w1.ID.String(), nil), http.StatusNotFound)

	// Rotating the credential and tuning the resolver recreate the proxy container.
	f.expect(f.call(f.admin, http.MethodPatch, "/dns/credentials/"+cred.ID.String(), map[string]any{"values": map[string]string{"CF_DNS_API_TOKEN": "rotated-token-3"}}), http.StatusOK)
	rec = f.expect(f.call(f.dev, http.MethodGet, base, nil), http.StatusOK)
	st = f.state(rec)
	require.Equal(t, dns01.ChangeUpdate, st.Resolvers[0].Change)
	require.True(t, st.Resolvers[0].Rotated)
	p = f.plan()
	require.True(t, p.Restart)
	c, _ = diffOf(p, "dns01.resolver.cloudflare.credential")
	require.Equal(t, "previous values", c.From)
	require.Equal(t, "rotated values of cf", c.To)
	require.Len(t, p.Reasons, 1, "one reason however many static changes")

	cred2 := f.createCred("cf2", "second-token-1")
	rec = f.expect(f.call(f.dev, http.MethodPut, base+"/resolvers/cloudflare", map[string]any{"credentialId": cred2.ID, "delaySeconds": 30, "staging": true}), http.StatusOK)
	st = f.state(rec)
	require.Equal(t, dns01.ChangeUpdate, st.Resolvers[0].Change)
	require.True(t, st.Resolvers[0].Staging)
	require.Equal(t, 30, st.Resolvers[0].DelaySeconds)
	p = f.plan()
	c, _ = diffOf(p, "dns01.resolver.cloudflare.credential")
	require.Equal(t, cred.ID.String(), c.From)
	require.Equal(t, "cf2", c.To)
	c, _ = diffOf(p, "dns01.resolver.cloudflare.delaySeconds")
	require.Equal(t, "0", c.From)
	require.Equal(t, "30", c.To)
	c, _ = diffOf(p, "dns01.resolver.cloudflare.staging")
	require.Equal(t, "off", c.From)
	require.Equal(t, "on", c.To)
	require.True(t, hasWarning(p, "uses the Let's Encrypt staging CA"))

	// Removing an applied resolver is a staged removal; removing it again is a 404.
	rec = f.expect(f.call(f.dev, http.MethodDelete, base+"/resolvers/cloudflare", nil), http.StatusOK)
	st = f.state(rec)
	require.Len(t, st.Resolvers, 1)
	require.Equal(t, dns01.ChangeRemove, st.Resolvers[0].Change)
	c, _ = diffOf(f.plan(), "dns01.resolver.cloudflare")
	require.Equal(t, "configured", c.From)
	require.Equal(t, "removed", c.To)
	f.deployProxy()
	f.expect(f.call(f.dev, http.MethodDelete, base+"/resolvers/cloudflare", nil), http.StatusNotFound)

	// Without write access to the proxy nothing can be staged.
	ro := auth.WithPrincipal(context.Background(), &auth.Principal{Type: auth.TypeUser, UserID: uuid.New(), WorkspaceID: f.ws.ID, Scopes: []string{policy.ScopeServersRead}})
	f.expect(f.call(ro, http.MethodGet, base, nil), http.StatusOK)
	f.expect(f.call(ro, http.MethodPut, base+"/resolvers/cloudflare", map[string]any{"credentialId": cred.ID}), http.StatusForbidden)
	f.expect(f.call(ro, http.MethodDelete, base+"/resolvers/cloudflare", nil), http.StatusForbidden)
	f.expect(f.call(ro, http.MethodPost, base+"/wildcards", wild), http.StatusForbidden)
}

func TestSetResolverNeedsAnACMEEmailAndAUsableCredential(t *testing.T) {
	f := newFx(t)
	cred := f.createCred("cf", token)
	noEmail, err := f.q.CreateServer(context.Background(), db.CreateServerParams{WorkspaceID: f.ws.ID, Name: "noemail"})
	require.NoError(t, err)
	rec := f.call(f.dev, http.MethodPut, "/servers/"+noEmail.ID.String()+"/dns01/resolvers/cloudflare", map[string]any{"credentialId": cred.ID})
	f.expect(rec, http.StatusUnprocessableEntity)
	require.Equal(t, "acme_email_required", f.errCode(rec))

	// A credential scoped to another server is refused.
	other, err := f.q.CreateServer(context.Background(), db.CreateServerParams{WorkspaceID: f.ws.ID, Name: "other"})
	require.NoError(t, err)
	scoped := f.cred(f.expect(f.call(f.admin, http.MethodPost, "/dns/credentials", map[string]any{
		"name": "other-only", "provider": "cloudflare", "serverId": other.ID, "values": map[string]string{"CF_DNS_API_TOKEN": "t-other-1"},
	}), http.StatusCreated))
	rec = f.call(f.dev, http.MethodPut, "/servers/"+f.srv.ID.String()+"/dns01/resolvers/cloudflare", map[string]any{"credentialId": scoped.ID})
	f.expect(rec, http.StatusUnprocessableEntity)
	require.Equal(t, "invalid_credential", f.errCode(rec))
	require.Contains(t, rec.Body.String(), "scoped to another server")
}
