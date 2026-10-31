package registries

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
)

func TestCreateSealsCredentialsAndNeverReturnsThem(t *testing.T) {
	f := newFixture(t)
	v, err := f.svc.Create(f.ctx, CreateInput{
		Name: "GHCR", Host: "GHCR.io", Namespace: "Acme/Platform", PushUsername: "deploy-bot", PushPassword: "ghp_pushsecret123",
		PullUsername: "reader", PullPassword: "ghp_pullsecret456",
	})
	require.NoError(t, err)
	require.Equal(t, KindExternal, v.Kind)
	require.Equal(t, "ghcr.io", v.Host, "hosts are lower-cased")
	require.Equal(t, "acme/platform", v.Namespace)
	require.True(t, v.HasPushAuth)
	require.True(t, v.HasPullAuth)
	require.Equal(t, "deploy-bot", v.PushUsername)
	require.Equal(t, "reader", v.PullUsername)
	require.False(t, v.Insecure)
	require.False(t, v.ManageRetention)

	// No read path carries a password: not the create response, the list, the get or the JSON.
	list, err := f.svc.List(f.ctx)
	require.NoError(t, err)
	got, err := f.svc.Get(f.ctx, v.ID)
	require.NoError(t, err)
	for _, x := range []any{v, list, got} {
		raw, err := json.Marshal(x)
		require.NoError(t, err)
		require.NotContains(t, string(raw), "ghp_pushsecret123")
		require.NotContains(t, string(raw), "ghp_pullsecret456")
		require.NotContains(t, string(raw), "assword\":\"g", "no password field")
	}

	// At rest they are sealed.
	row, err := f.q.GetRegistryInternal(context.Background(), v.ID)
	require.NoError(t, err)
	require.NotContains(t, string(row.PushAuthEnc), "ghp_pushsecret123")
	require.NotContains(t, string(row.PullAuthEnc), "ghp_pullsecret456")
	c, err := f.svc.open(secretbox.RegistriesPushAuth, v.ID, row.PushAuthEnc)
	require.NoError(t, err)
	require.Equal(t, credential{Username: "deploy-bot", Password: "ghp_pushsecret123"}, c)

	// Audited by name and fields, never by value.
	var meta string
	require.NoError(t, f.pool.QueryRow(context.Background(),
		`SELECT metadata::text FROM audit_events WHERE action = $1 AND resource_id = $2`, AuditCreate, v.ID.String()).Scan(&meta))
	require.Contains(t, meta, "ghcr.io")
	require.NotContains(t, meta, "ghp_")
}

func TestCreateValidation(t *testing.T) {
	f := newFixture(t)
	base := CreateInput{Name: "r", Host: "registry.example.com", PushUsername: "u", PushPassword: "p"}
	bad := func(name string, mod func(*CreateInput)) {
		t.Helper()
		in := base
		mod(&in)
		_, err := f.svc.Create(f.ctx, in)
		require.Equal(t, "validation_failed", code(err), name)
	}
	bad("scheme", func(in *CreateInput) { in.Host = "https://registry.example.com" })
	bad("path", func(in *CreateInput) { in.Host = "registry.example.com/v2" })
	bad("credentials in host", func(in *CreateInput) { in.Host = "user@registry.example.com" })
	bad("space", func(in *CreateInput) { in.Host = "registry example.com" })
	bad("empty host", func(in *CreateInput) { in.Host = "" })
	bad("port zero", func(in *CreateInput) { in.Host = "registry.example.com:0" })
	bad("port too big", func(in *CreateInput) { in.Host = "registry.example.com:70000" })
	bad("port text", func(in *CreateInput) { in.Host = "registry.example.com:abc" })
	bad("underscore label", func(in *CreateInput) { in.Host = "my_registry.example.com" })
	bad("empty label", func(in *CreateInput) { in.Host = "registry..example.com" })
	bad("ipv6 without brackets", func(in *CreateInput) { in.Host = "::1:5000" })
	bad("namespace empty part", func(in *CreateInput) { in.Namespace = "acme//platform" })
	bad("namespace char", func(in *CreateInput) { in.Namespace = "acme platform" })
	bad("namespace dot edge", func(in *CreateInput) { in.Namespace = ".acme" })
	bad("namespace double dash end", func(in *CreateInput) { in.Namespace = "acme-" })
	bad("no name", func(in *CreateInput) { in.Name = "  " })
	bad("long name", func(in *CreateInput) { in.Name = string(make([]byte, 81)) + "x" })
	bad("no push user", func(in *CreateInput) { in.PushUsername = "" })
	bad("no push password", func(in *CreateInput) { in.PushPassword = "" })
	bad("colon in user", func(in *CreateInput) { in.PushUsername = "a:b" })
	bad("pull user without password", func(in *CreateInput) { in.PullUsername = "reader" })
	bad("pull password without user", func(in *CreateInput) { in.PullPassword = "x" })

	for i, host := range []string{"registry.example.com", "registry.example.com:5000", "ghcr.io", "localhost:5000", "127.0.0.1:5000", "[::1]:5000", "single", "10.0.0.5"} {
		in := base
		in.Name, in.Host = fmt.Sprintf("ok-%d", i), host
		v, err := f.svc.Create(f.ctx, in)
		require.NoError(t, err, host)
		require.Equal(t, host, v.Host)
	}
	for _, ns := range []string{"", "acme", "my-org", "my_org", "a.b", "group/project", "group/sub-group/project", "a__b"} {
		in := base
		in.Name, in.Namespace = "ns-"+ns, ns
		_, err := f.svc.Create(f.ctx, in)
		require.NoError(t, err, "namespace %q", ns)
	}
	_, err := f.svc.Create(f.ctx, CreateInput{Name: "ok-0", Host: "x.io", PushUsername: "u", PushPassword: "p"})
	require.Equal(t, "registry_name_taken", code(err), "ok-0 exists from the table above")
}

func TestLoopbackHostsAreInsecure(t *testing.T) {
	for host, want := range map[string]bool{
		"localhost": true, "localhost:5000": true, "registry.localhost:8880": true, "127.0.0.1:5000": true, "127.1.2.3": true, "[::1]:5000": true,
		"ghcr.io": false, "registry.example.com:5000": false, "10.0.0.5:5000": false, "localhost.example.com": false, "notlocalhost": false,
	} {
		require.Equal(t, want, IsLoopbackHost(host), host)
	}
	f := newFixture(t)
	v, err := f.svc.Create(f.ctx, CreateInput{Name: "local", Host: "127.0.0.1:5055", PushUsername: "u", PushPassword: "p"})
	require.NoError(t, err)
	require.True(t, v.Insecure)
	info, err := f.svc.Resolve(f.ctx, v.ID)
	require.NoError(t, err)
	require.True(t, info.Insecure)
	require.True(t, info.Healthy, "an external registry has no control-plane-side health")
}

func TestUpdateKeepsPasswordsUnlessReplaced(t *testing.T) {
	f := newFixture(t)
	v := f.createExternal("GHCR")
	ctx := context.Background()
	creds := func(actions ...string) credential {
		t.Helper()
		auth, err := f.svc.Credentials(ctx, v.ID, Scope{Repository: "acme/app", Actions: actions}, 0)
		require.NoError(t, err)
		a, ok := DecodeAuth(auth)
		require.True(t, ok)
		return credential{Username: a.Username, Password: a.Password}
	}
	require.Equal(t, credential{Username: "deploy-bot", Password: "ghp_pushsecret123"}, creds(ActionPush, ActionPull))

	// Rename and change the namespace: the credentials stay.
	got, err := f.svc.Update(f.ctx, v.ID, UpdateInput{Name: new("GitHub"), Namespace: new("other")})
	require.NoError(t, err)
	require.Equal(t, "GitHub", got.Name)
	require.Equal(t, "other", got.Namespace)
	require.Equal(t, credential{Username: "deploy-bot", Password: "ghp_pushsecret123"}, creds(ActionPush))

	// Moving to another address would send the stored credentials there: the
	// password has to be entered again.
	_, err = f.svc.Update(f.ctx, v.ID, UpdateInput{Host: new("ghcr.io:443")})
	require.Equal(t, "secret_required", code(err))
	got, err = f.svc.Update(f.ctx, v.ID, UpdateInput{Host: new("ghcr.io:443"), PushPassword: new("ghp_pushsecret123")})
	require.NoError(t, err)
	require.Equal(t, "ghcr.io:443", got.Host)
	got, err = f.svc.Update(f.ctx, v.ID, UpdateInput{Host: new("GHCR.io:443")})
	require.NoError(t, err, "the same address needs no password")
	require.Equal(t, "ghcr.io:443", got.Host)

	// A new token for the same user.
	_, err = f.svc.Update(f.ctx, v.ID, UpdateInput{PushPassword: new("ghp_rotated999")})
	require.NoError(t, err)
	require.Equal(t, credential{Username: "deploy-bot", Password: "ghp_rotated999"}, creds(ActionPush))

	// A new username needs the password again: passwords are never shown.
	_, err = f.svc.Update(f.ctx, v.ID, UpdateInput{PushUsername: new("someone")})
	require.Equal(t, "validation_failed", code(err))
	got, err = f.svc.Update(f.ctx, v.ID, UpdateInput{PushUsername: new("someone"), PushPassword: new("pw")})
	require.NoError(t, err)
	require.Equal(t, "someone", got.PushUsername)

	// A read-only credential: pull-only scopes use it, everything else the push one.
	_, err = f.svc.Update(f.ctx, v.ID, UpdateInput{PullPassword: new("only-a-password")})
	require.Equal(t, "validation_failed", code(err), "a pull credential needs a username too")
	got, err = f.svc.Update(f.ctx, v.ID, UpdateInput{PullUsername: new("reader"), PullPassword: new("pullpw")})
	require.NoError(t, err)
	require.True(t, got.HasPullAuth)
	require.Equal(t, credential{Username: "reader", Password: "pullpw"}, creds(ActionPull))
	require.Equal(t, credential{Username: "someone", Password: "pw"}, creds(ActionPush, ActionPull), "builds push with the push credential")
	require.Equal(t, credential{Username: "someone", Password: "pw"}, creds(ActionPush))

	// The read-only credential would move too: it is re-entered or removed.
	_, err = f.svc.Update(f.ctx, v.ID, UpdateInput{Host: new("mirror.example.com"), PushPassword: new("pw")})
	require.Equal(t, "secret_required", code(err))
	got, err = f.svc.Update(f.ctx, v.ID, UpdateInput{Host: new("mirror.example.com"), PushPassword: new("pw"), PullPassword: new("pullpw")})
	require.NoError(t, err)
	require.Equal(t, "mirror.example.com", got.Host)
	got, err = f.svc.Update(f.ctx, v.ID, UpdateInput{Host: new("ghcr.io:443"), PushPassword: new("pw"), ClearPull: true})
	require.NoError(t, err)
	require.False(t, got.HasPullAuth)
	require.Equal(t, "ghcr.io:443", got.Host)

	got, err = f.svc.Update(f.ctx, v.ID, UpdateInput{ClearPull: true})
	require.NoError(t, err)
	require.False(t, got.HasPullAuth)
	require.Equal(t, credential{Username: "someone", Password: "pw"}, creds(ActionPull), "without a pull credential targets pull with the push one")

	_, err = f.svc.Update(f.ctx, v.ID, UpdateInput{ClearPull: true, PullUsername: new("x"), PullPassword: new("y")})
	require.Equal(t, "validation_failed", code(err))
	_, err = f.svc.Update(f.ctx, v.ID, UpdateInput{Host: new("https://ghcr.io")})
	require.Equal(t, "validation_failed", code(err))

	// Name clash.
	f.createExternal("Docker Hub")
	_, err = f.svc.Update(f.ctx, v.ID, UpdateInput{Name: new("Docker Hub")})
	require.Equal(t, "registry_name_taken", code(err))

	var n int
	require.NoError(t, f.pool.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE action = $1 AND metadata::text LIKE '%pullCredential%'`, AuditUpdate).Scan(&n))
	require.Positive(t, n)
	var all string
	require.NoError(t, f.pool.QueryRow(ctx, `SELECT string_agg(metadata::text, ' ') FROM audit_events WHERE action LIKE 'registry.%'`).Scan(&all))
	for _, secret := range []string{"ghp_pushsecret123", "ghp_rotated999", "pullpw", "pw\""} {
		require.NotContains(t, all, secret)
	}
}

func TestBuiltinRegistryIsReadOnly(t *testing.T) {
	f := newFixture(t)
	reg, _ := f.builtinRegistry("Built-in registry", "127.0.0.1:18500")
	for name, in := range map[string]UpdateInput{
		"host":      {Host: new("elsewhere.example.test")},
		"namespace": {Namespace: new("other")},
		"password":  {PushUsername: new("u"), PushPassword: new("p")},
		"name":      {Name: new("Images")},
		"retention": {ManageRetention: new(false)},
	} {
		_, err := f.svc.Update(f.ctx, reg.ID, in)
		require.Equal(t, "builtin_registry_readonly", code(err), name)
	}
	got, err := f.svc.Get(f.ctx, reg.ID)
	require.NoError(t, err)
	require.Equal(t, "Built-in registry", got.Name)
	require.True(t, got.ManageRetention)
	require.Equal(t, "builtin_registry", code(f.svc.Delete(f.ctx, reg.ID)))
}

func TestDeleteIsGuardedWhileSettingsReferenceTheRegistry(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	for name, step := range map[string][2]string{
		"team":    {"INSERT INTO team_build_settings (workspace_id, build_location, registry_id) VALUES ('%s', 'build_server', '%s')", "DELETE FROM team_build_settings"},
		"project": {"INSERT INTO projects (workspace_id, name, slug, registry_id) VALUES ('%s', 'p', 'p', '%s')", "DELETE FROM projects"},
		"service": {"", ""},
	} {
		v := f.createExternal("r-" + name)
		switch name {
		case "service":
			// A service override lives in services.config.registryId.
			var envID uuid.UUID
			var projID uuid.UUID
			require.NoError(t, f.pool.QueryRow(ctx, `INSERT INTO projects (workspace_id, name, slug) VALUES ($1, 'sp', 'sp') RETURNING id`, f.ws.ID).Scan(&projID))
			require.NoError(t, f.pool.QueryRow(ctx, `INSERT INTO environments (project_id, name, slug) VALUES ($1, 'e', 'e') RETURNING id`, projID).Scan(&envID))
			_, err := f.pool.Exec(ctx, `INSERT INTO services (environment_id, name, slug, source_type, config) VALUES ($1, 's', 's', 'image', jsonb_build_object('registryId', $2::text))`, envID, v.ID.String())
			require.NoError(t, err)
			step[1] = "UPDATE services SET deleted_at = now()"
		default:
			_, err := f.pool.Exec(ctx, fmt.Sprintf(step[0], f.ws.ID, v.ID))
			require.NoError(t, err, name)
		}

		err := f.svc.Delete(f.ctx, v.ID)
		require.Equal(t, "registry_in_use", code(err), name)
		got, err := f.svc.Get(f.ctx, v.ID)
		require.NoError(t, err)
		require.True(t, got.InUse, name)
		require.Equal(t, 1, map[string]int{"team": got.References.Teams, "project": got.References.Projects, "service": got.References.Services}[name], name)
		require.Equal(t, "registry_in_use", code(f.svc.CheckRemovable(ctx, v.ID)), name)

		_, err = f.pool.Exec(ctx, step[1])
		require.NoError(t, err, name)
		require.NoError(t, f.svc.CheckRemovable(ctx, v.ID))
		require.NoError(t, f.svc.Delete(f.ctx, v.ID), name)
		_, err = f.svc.Get(f.ctx, v.ID)
		require.Equal(t, "not_found", code(err))
	}
	var n int
	require.NoError(t, f.pool.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE action = $1`, AuditDelete).Scan(&n))
	require.Equal(t, 3, n)
}

func TestRegistriesAreScopedToTheirTeamAndAuthorized(t *testing.T) {
	f := newFixture(t)
	v := f.createExternal("mine")

	// Another team sees nothing and cannot touch it.
	other, err := f.q.CreateWorkspace(context.Background(), "other")
	require.NoError(t, err)
	ou, err := f.q.CreateUser(context.Background(), dbCreateUser(other.ID, "o@example.com"))
	require.NoError(t, err)
	octx := scopesCtx(other.ID, ou.ID, policy.AllScopes...)
	list, err := f.svc.List(octx)
	require.NoError(t, err)
	require.Empty(t, list)
	_, err = f.svc.Get(octx, v.ID)
	require.Equal(t, "not_found", code(err))
	_, err = f.svc.Update(octx, v.ID, UpdateInput{Name: new("x")})
	require.Equal(t, "not_found", code(err))
	require.Equal(t, "not_found", code(f.svc.Delete(octx, v.ID)))
	_, err = f.svc.ListImages(octx, v.ID, "", "", 0)
	require.Equal(t, "not_found", code(err))
	_, err = f.svc.Check(octx, v.ID, f.srv.ID)
	require.Equal(t, "not_found", code(err))

	// Reading needs projects:read; changing needs admin.
	ro := scopesCtx(f.ws.ID, f.user.ID, policy.ScopeProjectsRead)
	_, err = f.svc.List(ro)
	require.NoError(t, err)
	_, err = f.svc.Create(ro, CreateInput{Name: "x", Host: "x.io", PushUsername: "u", PushPassword: "p"})
	require.Equal(t, "forbidden", code(err))
	_, err = f.svc.Update(ro, v.ID, UpdateInput{Name: new("x")})
	require.Equal(t, "forbidden", code(err))
	require.Equal(t, "forbidden", code(f.svc.Delete(ro, v.ID)))
	_, err = f.svc.Check(ro, v.ID, f.srv.ID)
	require.Equal(t, "forbidden", code(err))
	none := scopesCtx(f.ws.ID, f.user.ID)
	_, err = f.svc.List(none)
	require.Equal(t, "forbidden", code(err))
}

func TestCredentialsValidateTheirScope(t *testing.T) {
	f := newFixture(t)
	v := f.createExternal("r")
	ctx := context.Background()
	_, err := f.svc.Credentials(ctx, v.ID, Scope{Repository: "acme/app"}, 0)
	require.Error(t, err, "no action")
	_, err = f.svc.Credentials(ctx, v.ID, Scope{Repository: "acme/app", Actions: []string{"admin"}}, 0)
	require.Error(t, err, "unknown action")
	_, err = f.svc.Credentials(ctx, uuid.New(), Scope{Repository: "acme/app", Actions: []string{ActionPull}}, 0)
	require.Equal(t, "not_found", code(err))

	auth, err := f.svc.Credentials(ctx, v.ID, Scope{Repository: "acme/app", Actions: []string{ActionPull, ActionPush, ActionPull}}, time.Minute)
	require.NoError(t, err)
	a, ok := DecodeAuth(auth)
	require.True(t, ok)
	require.Equal(t, "deploy-bot", a.Username)
	require.Equal(t, "ghcr.io", a.ServerAddress)

	// The built-in registry's credential is the placeholder: the server holds nothing that works.
	reg, _ := f.builtinRegistry("builtin", "127.0.0.1:18500")
	auth, err = f.svc.Credentials(ctx, reg.ID, Scope{Repository: f.ws.ID.String() + "/app", Actions: []string{ActionPush, ActionPull}}, 0)
	require.NoError(t, err)
	a, ok = DecodeAuth(auth)
	require.True(t, ok)
	require.Equal(t, BuiltinUsername, a.Username)
	require.Equal(t, BuiltinPassword, a.Password)
	require.Equal(t, "127.0.0.1:18500", a.ServerAddress)
}
