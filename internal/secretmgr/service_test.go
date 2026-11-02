package secretmgr

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/services"
)

func asAPIError(err error) (*apierr.Error, bool) { return apierr.As(err) }

func (f *scopeFixture) exec(sql string, args ...any) {
	f.t.Helper()
	_, err := f.pool.Exec(context.Background(), sql, args...)
	require.NoError(f.t, err)
}

func (f *scopeFixture) count(sql string, args ...any) int {
	f.t.Helper()
	var n int
	require.NoError(f.t, f.pool.QueryRow(context.Background(), sql, args...).Scan(&n))
	return n
}

// asMember is a token with every scope except admin.
func (f *scopeFixture) asMember() context.Context {
	var scopes []string
	for _, s := range policy.AllScopes {
		if s != policy.ScopeAdmin {
			scopes = append(scopes, s)
		}
	}
	return auth.WithPrincipal(context.Background(), &auth.Principal{Type: auth.TypeToken, UserID: uuid.New(), TokenID: new(uuid.New()), WorkspaceID: f.ws.ID, Scopes: scopes})
}

func (f *scopeFixture) input() Input {
	return Input{Name: "dp", Kind: KindDoppler, Config: SecretManagerConfig{SiteURL: f.fd.srv.URL}, Secret: dopplerServiceToken}
}

func TestCreateRefusals(t *testing.T) {
	f := newScopeFixture(t)
	long := strings.Repeat("a", 64)
	for name, tc := range map[string]struct {
		mut  func(*Input)
		code string
	}{
		"uppercase name":      {func(in *Input) { in.Name = "Doppler" }, "invalid_name"},
		"name with a space":   {func(in *Input) { in.Name = "my dp" }, "invalid_name"},
		"empty name":          {func(in *Input) { in.Name = "  " }, "invalid_name"},
		"name over 63":        {func(in *Input) { in.Name = long }, "invalid_name"},
		"unknown kind":        {func(in *Input) { in.Kind = "ldap" }, "invalid_kind"},
		"bad address":         {func(in *Input) { in.Config.SiteURL = "ftp://x" }, "invalid_address"},
		"no credential":       {func(in *Input) { in.Secret = "" }, "secret_required"},
		"credential too long": {func(in *Input) { in.Secret = strings.Repeat("x", 4097) }, "invalid_secret"},
		"two-line credential": {func(in *Input) { in.Secret = "a\nb" }, "invalid_secret"},
		"selected, no grants": {func(in *Input) { in.Scope = ScopeSelected }, "grants_required"},
		"grant on no project": {func(in *Input) {
			in.Scope, in.Grants = ScopeSelected, []ScopeGrant{{ProjectID: uuid.New()}}
		}, "invalid_grant"},
	} {
		in := f.input()
		tc.mut(&in)
		_, err := f.svc.Create(f.ctx, in)
		require.Equal(t, tc.code, f.code(err), name)
	}
	require.Zero(t, f.count(`SELECT count(*) FROM secret_integrations`), "nothing refused was stored")

	_, err := f.svc.Create(f.asMember(), f.input())
	require.Equal(t, "forbidden", f.code(err), "credentials are the admin's")

	// An environment that is not in the granted project.
	_, err = f.svc.Create(f.ctx, func() Input {
		in := f.input()
		in.Scope, in.Grants = ScopeSelected, []ScopeGrant{{ProjectID: f.projA.ID, EnvironmentID: &f.envB.ID}}
		return in
	}())
	require.Equal(t, "invalid_grant", f.code(err))

	// More grants than the allow-list takes.
	many := make([]ScopeGrant, maxGrants+1)
	for i := range many {
		many[i] = ScopeGrant{ProjectID: f.projA.ID}
	}
	in := f.input()
	in.Scope, in.Grants = ScopeSelected, many
	_, err = f.svc.Create(f.ctx, in)
	require.Equal(t, "invalid_grants", f.code(err))
}

func TestCreateStoresTheCredentialSealedAndAuditsNeverItsValue(t *testing.T) {
	f := newScopeFixture(t)
	m, err := f.svc.Create(f.ctx, f.input())
	require.NoError(t, err)
	require.Equal(t, ScopeTeam, m.Scope, "a new integration is for the whole team unless limited")
	require.True(t, m.SecretSet)
	require.Empty(t, m.Grants)
	require.Equal(t, "${{doppler:dp/backend/prd#DATABASE_URL}}", m.Example)
	raw, err := json.Marshal(m)
	require.NoError(t, err)
	require.NotContains(t, string(raw), dopplerServiceToken)

	var enc []byte
	require.NoError(t, f.pool.QueryRow(context.Background(), `SELECT credentials_enc FROM secret_integrations WHERE id = $1`, m.ID).Scan(&enc))
	require.NotContains(t, string(enc), dopplerServiceToken)
	got, err := f.svc.box.OpenString(secretbox.SecretIntegrationsCredentials, m.ID.String(), enc)
	require.NoError(t, err)
	require.Equal(t, dopplerServiceToken, got)
	require.Equal(t, 1, f.count(`SELECT count(*) FROM audit_events WHERE action = 'secret_manager.create' AND metadata::text NOT LIKE '%' || $1 || '%'`, dopplerServiceToken))

	_, err = f.svc.Create(f.ctx, f.input())
	require.Equal(t, "name_taken", f.code(err))
}

func TestListShowsGrantsWithNamesAndNeedsOnlyReadAccess(t *testing.T) {
	f := newScopeFixture(t)
	require.Empty(t, mustList(t, f))

	f.create(ScopeSelected, grantOf(f.projA, &f.envAStg), grantOf(f.projB, nil))
	f.create2("everyone", ScopeTeam)
	items := mustList(t, f)
	require.Len(t, items, 2)
	by := map[string]SecretManager{}
	for _, it := range items {
		by[it.Name] = it
	}
	require.Empty(t, by["everyone"].Grants)
	grants := by["dp"].Grants
	require.Len(t, grants, 2)
	names := map[string]string{}
	for _, g := range grants {
		names[g.ProjectName] = g.EnvironmentName
	}
	require.Equal(t, map[string]string{"Shop": "staging", "Blog": ""}, names)

	// A member without admin sees the list (to pick a reference) but changes nothing.
	_, err := f.svc.List(f.asMember())
	require.NoError(t, err)
	_, err = f.svc.List(auth.WithPrincipal(context.Background(), &auth.Principal{Type: auth.TypeToken, WorkspaceID: f.ws.ID, TokenID: new(uuid.New()), Scopes: []string{policy.ScopeServersRead}}))
	require.Equal(t, "forbidden", f.code(err))
}

func mustList(t *testing.T, f *scopeFixture) []SecretManager {
	t.Helper()
	items, err := f.svc.List(f.ctx)
	require.NoError(t, err)
	return items
}

func TestUpdateRules(t *testing.T) {
	f := newScopeFixture(t)
	m := f.create(ScopeTeam)
	update := func(in Input) (SecretManager, error) { return f.svc.Update(f.ctx, m.ID, in) }

	_, err := f.svc.Update(f.asMember(), m.ID, Input{Config: m.Config})
	require.Equal(t, "forbidden", f.code(err))
	_, err = f.svc.Update(f.ctx, uuid.New(), Input{Config: m.Config})
	require.Equal(t, "not_found", f.code(err))
	_, err = update(Input{Kind: KindVault, Config: SecretManagerConfig{Address: "https://vault.example.com"}})
	require.Equal(t, "kind_fixed", f.code(err))
	_, err = update(Input{Config: SecretManagerConfig{SiteURL: "ftp://bad"}})
	require.Equal(t, "invalid_address", f.code(err))
	_, err = update(Input{Config: m.Config, Secret: "two\nlines"})
	require.Equal(t, "invalid_secret", f.code(err))

	// Pointing the stored credential at another server needs the credential again.
	moved := SecretManagerConfig{SiteURL: "https://doppler.example.com"}
	_, err = update(Input{Config: moved})
	require.Equal(t, "secret_required", f.code(err))

	// Changing only what is not an identity keeps the credential.
	got, err := update(Input{Config: SecretManagerConfig{SiteURL: f.fd.srv.URL, DefaultProject: "backend", DefaultConfig: "prd"}})
	require.NoError(t, err)
	require.Equal(t, "backend", got.Config.DefaultProject)
	require.True(t, got.SecretSet)
	require.Equal(t, "${{doppler:dp#DATABASE_URL}}", got.Example)
	require.Equal(t, 1, f.count(`SELECT count(*) FROM audit_events WHERE action = 'secret_manager.update' AND metadata @> '{"changed":["config"]}'`))

	// A new credential is sealed under the same id.
	rotated, err := update(Input{Config: moved, Secret: "dp.st.new-token-0123456789"})
	require.NoError(t, err)
	require.Equal(t, "https://doppler.example.com", rotated.Config.SiteURL)
	var enc []byte
	require.NoError(t, f.pool.QueryRow(context.Background(), `SELECT credentials_enc FROM secret_integrations WHERE id = $1`, m.ID).Scan(&enc))
	plain, err := f.svc.box.OpenString(secretbox.SecretIntegrationsCredentials, m.ID.String(), enc)
	require.NoError(t, err)
	require.Equal(t, "dp.st.new-token-0123456789", plain)
	require.Equal(t, 1, f.count(`SELECT count(*) FROM audit_events WHERE action = 'secret_manager.update' AND metadata::text LIKE '%secret%' AND metadata::text NOT LIKE '%new-token%'`))
}

func TestUpdateScopeAndGrants(t *testing.T) {
	f := newScopeFixture(t)
	m := f.create(ScopeTeam)
	update := func(scope string, grants ...ScopeGrant) (SecretManager, error) {
		return f.svc.Update(f.ctx, m.ID, Input{Config: m.Config, Scope: scope, Grants: grants})
	}
	changedScope := func() int {
		return f.count(`SELECT count(*) FROM audit_events WHERE action = 'secret_manager.update' AND metadata @> '{"changed":["scope"]}'`)
	}

	// Omitting the scope keeps it.
	same, err := f.svc.Update(f.ctx, m.ID, Input{Config: m.Config})
	require.NoError(t, err)
	require.Equal(t, ScopeTeam, same.Scope)
	require.Zero(t, changedScope())

	// Limiting needs grants.
	_, err = update(ScopeSelected)
	require.Equal(t, "grants_required", f.code(err))
	limited, err := update(ScopeSelected, grantOf(f.projA, &f.envAProd), grantOf(f.projB, nil))
	require.NoError(t, err)
	require.Equal(t, ScopeSelected, limited.Scope)
	require.Len(t, limited.Grants, 2)
	require.Equal(t, 1, changedScope())
	require.Equal(t, 2, f.count(`SELECT count(*) FROM secret_integration_scopes WHERE integration_id = $1`, m.ID))

	// The same grants in another order change nothing and are not audited as a change.
	again, err := update(ScopeSelected, grantOf(f.projB, nil), grantOf(f.projA, &f.envAProd))
	require.NoError(t, err)
	require.Len(t, again.Grants, 2)
	require.Equal(t, 1, changedScope())

	// A different set is a change.
	_, err = update(ScopeSelected, grantOf(f.projB, nil))
	require.NoError(t, err)
	require.Equal(t, 2, changedScope())
	require.Equal(t, 1, f.count(`SELECT count(*) FROM secret_integration_scopes WHERE integration_id = $1`, m.ID))

	// Back to the whole team drops the allow-list.
	team, err := update(ScopeTeam)
	require.NoError(t, err)
	require.Empty(t, team.Grants)
	require.Zero(t, f.count(`SELECT count(*) FROM secret_integration_scopes WHERE integration_id = $1`, m.ID))
}

func TestSameGrants(t *testing.T) {
	p1, p2, e1 := uuid.New(), uuid.New(), uuid.New()
	g := func(p uuid.UUID, e *uuid.UUID) ScopeGrant { return ScopeGrant{ProjectID: p, EnvironmentID: e} }

	require.True(t, sameGrants(nil, nil))
	require.True(t, sameGrants([]ScopeGrant{g(p1, nil), g(p2, &e1)}, []ScopeGrant{g(p2, &e1), g(p1, nil)}))
	require.False(t, sameGrants([]ScopeGrant{g(p1, nil)}, []ScopeGrant{g(p1, &e1)}), "a project and one of its environments are different grants")
	require.False(t, sameGrants([]ScopeGrant{g(p1, nil)}, []ScopeGrant{g(p1, nil), g(p2, nil)}))
	require.False(t, sameGrants([]ScopeGrant{g(p1, nil), g(p2, nil)}, []ScopeGrant{g(p1, nil), g(uuid.New(), nil)}))
}

func TestDeleteIsRefusedWhileAVariableReferencesTheIntegration(t *testing.T) {
	f := newScopeFixture(t)
	bg := context.Background()
	m := f.create(ScopeSelected, grantOf(f.projA, nil))
	svc, err := f.q.InsertService(bg, db.InsertServiceParams{
		ID: uuid.New(), EnvironmentID: f.envAProd.ID, Name: "api", Slug: "api", Kind: services.KindApp, SourceType: services.SourceImage, Config: []byte(`{}`),
	})
	require.NoError(t, err)
	sc, err := services.LoadScopeInternal(bg, f.q, svc.ID)
	require.NoError(t, err)
	_, err = services.WriteServiceVariable(bg, f.q, f.svc.box, sc, services.VarWrite{Key: "DB", Value: "${{doppler:dp/backend/prd#DATABASE_URL}}"})
	require.NoError(t, err)
	_, err = services.WriteServiceVariable(bg, f.q, f.svc.box, sc, services.VarWrite{Key: "PLAIN", Value: "not a reference"})
	require.NoError(t, err)
	shared := func(project, env *uuid.UUID, key string) {
		id := uuid.New()
		enc, err := f.svc.box.SealString(secretbox.SharedVariablesValue, id.String(), "${{doppler:dp/backend/prd#"+key+"}}")
		require.NoError(t, err)
		require.NoError(t, f.q.CopySharedVariable(bg, db.CopySharedVariableParams{
			ID: id, WorkspaceID: f.ws.ID, ProjectID: project, EnvironmentID: env, Key: key, ValueEnc: enc, Source: services.SourceManual,
		}))
	}
	shared(nil, nil, "TEAM_KEY")
	shared(&f.projA.ID, nil, "PROJECT_KEY")
	shared(&f.projA.ID, &f.envAProd.ID, "ENV_KEY")

	require.Equal(t, "forbidden", f.code(f.svc.Delete(f.asMember(), m.ID)))
	require.Equal(t, "not_found", f.code(f.svc.Delete(f.ctx, uuid.New())))

	err = f.svc.Delete(f.ctx, m.ID)
	require.Equal(t, "integration_in_use", f.code(err))
	ae, _ := asAPIError(err)
	require.Equal(t, []string{
		"Shop/production/api DB", "environment shared ENV_KEY", "project shared PROJECT_KEY", "team TEAM_KEY",
	}, ae.Details["variables"], "every layer is listed, sorted, and a reference to another integration is not")
	require.Equal(t, 1, f.count(`SELECT count(*) FROM secret_integrations WHERE id = $1`, m.ID))

	// With the references gone, the integration and its grants go too.
	f.exec(`DELETE FROM service_variables`)
	f.exec(`DELETE FROM shared_variables`)
	require.NoError(t, f.svc.Delete(f.ctx, m.ID))
	require.Zero(t, f.count(`SELECT count(*) FROM secret_integrations WHERE id = $1`, m.ID))
	require.Zero(t, f.count(`SELECT count(*) FROM secret_integration_scopes WHERE integration_id = $1`, m.ID))
	require.Equal(t, 1, f.count(`SELECT count(*) FROM audit_events WHERE action = 'secret_manager.delete' AND resource_id = $1`, m.ID.String()))
}

func TestDeleteListsAtMostFiveReferencesInItsMessage(t *testing.T) {
	f := newScopeFixture(t)
	bg := context.Background()
	m := f.create(ScopeTeam)
	for i := range 7 {
		id := uuid.New()
		enc, err := f.svc.box.SealString(secretbox.SharedVariablesValue, id.String(), "${{doppler:dp/backend/prd#K}}")
		require.NoError(t, err)
		require.NoError(t, f.q.CopySharedVariable(bg, db.CopySharedVariableParams{
			ID: id, WorkspaceID: f.ws.ID, Key: fmt.Sprintf("KEY_%d", i), ValueEnc: enc, Source: services.SourceManual,
		}))
	}
	err := f.svc.Delete(f.ctx, m.ID)
	ae, _ := asAPIError(err)
	require.Contains(t, ae.Message, "7 variable(s)")
	require.Equal(t, 5, strings.Count(ae.Message, "team KEY_"), "the message names five, the details carry all")
	require.Len(t, ae.Details["variables"], 7)
}

func TestTestReportsTheOutcomeAndStoresIt(t *testing.T) {
	f := newScopeFixture(t)
	good := f.create(ScopeTeam)

	// Signing in alone.
	res, err := f.svc.Test(f.ctx, good.ID, TestInput{})
	require.NoError(t, err)
	require.True(t, res.OK)
	require.False(t, res.Read)
	require.Contains(t, res.Message, "Signed in")
	items := mustList(t, f)
	require.NotNil(t, items[0].LastTest.OK)
	require.True(t, *items[0].LastTest.OK)
	require.Empty(t, items[0].LastTest.Error)

	// Signed in, but the secret cannot be read: the message says which part failed.
	res, err = f.svc.Test(f.ctx, good.ID, TestInput{Path: "web/prd", Key: "WEB_TOKEN"})
	require.NoError(t, err)
	require.False(t, res.OK)
	require.Contains(t, res.Message, "signed in, but reading ${{doppler:dp/web/prd#WEB_TOKEN}} failed")
	items = mustList(t, f)
	require.False(t, *items[0].LastTest.OK)
	require.Contains(t, items[0].LastTest.Error, "signed in")

	// A credential the manager rejects.
	bad, err := f.svc.Create(f.ctx, Input{Name: "bad", Kind: KindDoppler, Config: SecretManagerConfig{SiteURL: f.fd.srv.URL}, Secret: "dp.st.wrong-token-0123456789"})
	require.NoError(t, err)
	res, err = f.svc.Test(f.ctx, bad.ID, TestInput{Key: "API_KEY"})
	require.NoError(t, err)
	require.False(t, res.OK)
	require.False(t, res.Read)
	require.Contains(t, res.Message, "authentication failed")
	require.NotContains(t, res.Message, "wrong-token", "the credential never appears in what is shown")
	require.Equal(t, 0, f.count(`SELECT count(*) FROM audit_events WHERE action = 'secret_manager.test' AND metadata::text LIKE '%wrong-token%'`))
}

func TestTestRefusals(t *testing.T) {
	f := newScopeFixture(t)
	m := f.create(ScopeTeam)
	_, err := f.svc.Test(f.asMember(), m.ID, TestInput{})
	require.Equal(t, "forbidden", f.code(err))
	_, err = f.svc.Test(f.ctx, uuid.New(), TestInput{})
	require.Equal(t, "not_found", f.code(err))
	// A path without a key is not a reference.
	_, err = f.svc.Test(f.ctx, m.ID, TestInput{Path: "backend/prd"})
	require.Equal(t, "invalid_reference", f.code(err))
}

func TestProviderForRejectsAStoredRowItCannotUse(t *testing.T) {
	f := newScopeFixture(t)
	m := f.create(ScopeTeam)
	row, err := f.q.GetSecretIntegration(context.Background(), db.GetSecretIntegrationParams{ID: m.ID, WorkspaceID: f.ws.ID})
	require.NoError(t, err)

	torn := row
	torn.Config = []byte(`"not an object"`)
	_, err = f.svc.providerFor(torn)
	require.ErrorContains(t, err, "decode integration dp")

	wrongKey := row
	wrongKey.ID = uuid.New() // the credential is sealed under the id of its row
	_, err = f.svc.providerFor(wrongKey)
	require.ErrorContains(t, err, "decrypt credential of dp")

	unknown := row
	unknown.Kind = "ldap"
	_, err = f.svc.providerFor(unknown)
	require.ErrorContains(t, err, `unknown integration kind "ldap"`)
}

// ---- normalize ----

func TestNormalizeRefusesWhatCannotWork(t *testing.T) {
	const notACert = "-----BEGIN NOT A CERT-----\nAAAA\n-----END NOT A CERT-----"
	vault := func(mut func(*SecretManagerConfig)) SecretManagerConfig {
		c := SecretManagerConfig{Address: "https://vault.example.com"}
		mut(&c)
		return c
	}
	infisical := func(mut func(*SecretManagerConfig)) SecretManagerConfig {
		c := SecretManagerConfig{ClientID: "client-1", ProjectID: "proj-123"}
		mut(&c)
		return c
	}
	for name, tc := range map[string]struct {
		kind string
		cfg  SecretManagerConfig
		code string
	}{
		"vault: namespace with spaces":    {KindVault, vault(func(c *SecretManagerConfig) { c.Namespace = "a b" }), "invalid_namespace"},
		"vault: namespace going up":       {KindVault, vault(func(c *SecretManagerConfig) { c.Namespace = "a/../b" }), "invalid_namespace"},
		"vault: mount going up":           {KindVault, vault(func(c *SecretManagerConfig) { c.Mount = "kv/../x" }), "invalid_mount"},
		"vault: mount with a space":       {KindVault, vault(func(c *SecretManagerConfig) { c.Mount = "my kv" }), "invalid_mount"},
		"vault: unknown auth method":      {KindVault, vault(func(c *SecretManagerConfig) { c.AuthMethod = "ldap" }), "invalid_auth_method"},
		"vault: approle without role id":  {KindVault, vault(func(c *SecretManagerConfig) { c.AuthMethod = AuthAppRole }), "invalid_role_id"},
		"vault: approle mount going up":   {KindVault, vault(func(c *SecretManagerConfig) { c.AuthMethod, c.RoleID, c.AppRoleMount = AuthAppRole, "r1", "../x" }), "invalid_mount"},
		"vault: CA that is not a cert":    {KindVault, vault(func(c *SecretManagerConfig) { c.CACert = notACert }), "invalid_ca_cert"},
		"vault: CA too large":             {KindVault, vault(func(c *SecretManagerConfig) { c.CACert = strings.Repeat("x", 64*1024+1) }), "invalid_ca_cert"},
		"vault: address with credentials": {KindVault, vault(func(c *SecretManagerConfig) { c.Address = "https://u:p@vault.example.com" }), "invalid_address"},
		"vault: address with a query":     {KindVault, vault(func(c *SecretManagerConfig) { c.Address = "https://vault.example.com/?x=1" }), "invalid_address"},
		"infisical: bad site":             {KindInfisical, infisical(func(c *SecretManagerConfig) { c.SiteURL = "infisical" }), "invalid_address"},
		"infisical: no client id":         {KindInfisical, infisical(func(c *SecretManagerConfig) { c.ClientID = "" }), "invalid_client_id"},
		"infisical: bad client id":        {KindInfisical, infisical(func(c *SecretManagerConfig) { c.ClientID = "has space" }), "invalid_client_id"},
		"infisical: no project id":        {KindInfisical, infisical(func(c *SecretManagerConfig) { c.ProjectID = "" }), "invalid_project_id"},
		"infisical: bad organization":     {KindInfisical, infisical(func(c *SecretManagerConfig) { c.OrganizationSlug = "a b" }), "invalid_organization"},
	} {
		_, err := normalize(tc.kind, tc.cfg)
		ae, ok := asAPIError(err)
		require.True(t, ok, name)
		require.Equal(t, tc.code, ae.Code, name)
	}

	got, err := normalize(KindInfisical, infisical(func(c *SecretManagerConfig) { c.OrganizationSlug = " acme-co " }))
	require.NoError(t, err)
	require.Equal(t, DefaultInfisicalURL, got.SiteURL)
	require.Equal(t, "acme-co", got.OrganizationSlug)
	require.Equal(t, "${{vault:x/prod/backend#DATABASE_URL}}", example("x", KindInfisical, got), "the example is written the way references are resolved")
}

func TestIdentityChangedPerKind(t *testing.T) {
	v := SecretManagerConfig{Address: "https://v", AuthMethod: AuthAppRole, RoleID: "r", AppRoleMount: "approle", Namespace: "ns"}
	for name, change := range map[string]func(*SecretManagerConfig){
		"address": func(c *SecretManagerConfig) { c.Address = "https://other" }, "auth method": func(c *SecretManagerConfig) { c.AuthMethod = AuthToken },
		"role": func(c *SecretManagerConfig) { c.RoleID = "r2" }, "approle mount": func(c *SecretManagerConfig) { c.AppRoleMount = "x" },
		"namespace": func(c *SecretManagerConfig) { c.Namespace = "n2" },
	} {
		cur := v
		change(&cur)
		require.True(t, identityChanged(KindVault, v, cur), name)
	}
	mount := v
	mount.Mount = "kv"
	require.False(t, identityChanged(KindVault, v, mount), "the KV mount is not part of the identity")

	i := SecretManagerConfig{SiteURL: "https://i", ClientID: "c", ProjectID: "p"}
	require.True(t, identityChanged(KindInfisical, i, SecretManagerConfig{SiteURL: "https://j", ClientID: "c"}))
	require.True(t, identityChanged(KindInfisical, i, SecretManagerConfig{SiteURL: "https://i", ClientID: "d"}))
	require.False(t, identityChanged(KindInfisical, i, SecretManagerConfig{SiteURL: "https://i", ClientID: "c", ProjectID: "other"}))

	d := SecretManagerConfig{SiteURL: "https://d"}
	require.True(t, identityChanged(KindDoppler, d, SecretManagerConfig{SiteURL: "https://e"}))
	require.False(t, identityChanged(KindDoppler, d, SecretManagerConfig{SiteURL: "https://d", DefaultProject: "p", DefaultConfig: "c"}))
	require.False(t, identityChanged("unknown", d, SecretManagerConfig{SiteURL: "https://e"}))
}

// ---- HTTP ----

func TestHTTPSecretManagerLifecycle(t *testing.T) {
	f := newScopeFixture(t)
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			p, _ := auth.PrincipalFrom(f.ctx)
			next.ServeHTTP(w, req.WithContext(auth.WithPrincipal(req.Context(), p)))
		})
	})
	cfg := huma.DefaultConfig("test", "1")
	cfg.CreateHooks = nil
	Register(humachi.New(r, cfg), f.svc)
	do := func(method, path string, body any) (int, map[string]any) {
		var rd *bytes.Reader
		if body != nil {
			raw, err := json.Marshal(body)
			require.NoError(t, err)
			rd = bytes.NewReader(raw)
		} else {
			rd = bytes.NewReader(nil)
		}
		req := httptest.NewRequest(method, path, rd)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		var m map[string]any
		if rec.Body.Len() > 0 {
			_ = json.Unmarshal(rec.Body.Bytes(), &m)
		}
		return rec.Code, m
	}

	status, body := do(http.MethodPost, "/secret-managers", map[string]any{
		"name": "dp", "kind": "doppler", "siteUrl": f.fd.srv.URL, "secret": dopplerServiceToken,
	})
	require.Equal(t, http.StatusCreated, status, body)
	id := body["id"].(string)
	require.Equal(t, true, body["secretSet"])
	require.NotContains(t, body, "secret")
	status, body = do(http.MethodPost, "/secret-managers", map[string]any{"name": "dp", "kind": "doppler", "siteUrl": f.fd.srv.URL, "secret": dopplerServiceToken})
	require.Equal(t, http.StatusConflict, status)
	require.Equal(t, "name_taken", body["code"])

	status, body = do(http.MethodGet, "/secret-managers", nil)
	require.Equal(t, http.StatusOK, status)
	require.Len(t, body["items"], 1)

	status, body = do(http.MethodPut, "/secret-managers/"+id, map[string]any{"siteUrl": f.fd.srv.URL, "defaultProject": "backend", "defaultConfig": "prd"})
	require.Equal(t, http.StatusOK, status, body)
	require.Equal(t, "backend", body["config"].(map[string]any)["defaultProject"])

	status, body = do(http.MethodPost, "/secret-managers/"+id+"/test", map[string]any{"key": "API_KEY"})
	require.Equal(t, http.StatusOK, status, body)
	require.Equal(t, true, body["ok"])
	require.Equal(t, true, body["read"])
	status, _ = do(http.MethodPost, "/secret-managers/"+uuid.NewString()+"/test", map[string]any{})
	require.Equal(t, http.StatusNotFound, status)

	status, _ = do(http.MethodDelete, "/secret-managers/"+id, nil)
	require.Equal(t, http.StatusNoContent, status)
	status, body = do(http.MethodDelete, "/secret-managers/"+id, nil)
	require.Equal(t, http.StatusNotFound, status)
	require.Equal(t, "not_found", body["code"])
}
