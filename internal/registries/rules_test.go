package registries

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/riverqueue/river"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/jobs"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

func (f *fixture) exec(sql string, args ...any) {
	f.t.Helper()
	_, err := f.pool.Exec(context.Background(), sql, args...)
	require.NoError(f.t, err)
}

func TestNormalizeHostRefusals(t *testing.T) {
	for raw, want := range map[string]string{
		"":                              "host is required",
		"   ":                           "host is required",
		"https://ghcr.io":               "must not include a scheme",
		strings.Repeat("a", 260):        "too long",
		"ghcr.io/acme":                  "bare hostname",
		"user@ghcr.io":                  "bare hostname",
		"ghcr io":                       "bare hostname",
		"[::1":                          "invalid IPv6 host",
		"[::1]x":                        "invalid host",
		"[127.0.0.1]:5000":              "invalid IPv6 host",
		"[nope]:5000":                   "invalid IPv6 host",
		"::1":                           "needs brackets",
		"ghcr.io:99999":                 "",
		"-bad-.example.com":             "",
		"under_score.example.com":       "",
		"a..b.example.com":              "",
		"ghcr.io:notaport":              "",
		strings.Repeat("a", 64) + ".io": "",
	} {
		_, err := NormalizeHost(raw)
		require.Error(t, err, raw)
		require.Equal(t, "validation_failed", code(err), raw)
		if want != "" {
			require.Contains(t, err.Error(), want, raw)
		}
	}
	for _, c := range []struct{ raw, want string }{
		{" GHCR.io ", "ghcr.io"},
		{"registry:5000", "registry:5000"},
		{"[::1]:5000", "[::1]:5000"},
		{"[2001:db8::1]", "[2001:db8::1]"},
		{"127.0.0.1:5000", "127.0.0.1:5000"},
		{"a.b-c.example.io", "a.b-c.example.io"},
	} {
		got, err := NormalizeHost(c.raw)
		require.NoError(t, err, c.raw)
		require.Equal(t, c.want, got)
	}
}

func TestNormalizeNamespaceAndCredentialLimits(t *testing.T) {
	_, err := NormalizeNamespace(strings.Repeat("a", maxNamespaceLen+1))
	require.Equal(t, "validation_failed", code(err))
	got, err := NormalizeNamespace("  /Acme/Web-Apps/ ")
	require.NoError(t, err)
	require.Equal(t, "acme/web-apps", got)
	_, err = NormalizeNamespace("acme//apps")
	require.Error(t, err)
	empty, err := NormalizeNamespace("  ")
	require.NoError(t, err)
	require.Empty(t, empty)

	require.Equal(t, "validation_failed", code(validateCredential("The push credential", "u", strings.Repeat("p", maxCredLen+1))))
	require.Equal(t, "validation_failed", code(validateCredential("The push credential", strings.Repeat("u", maxCredLen+1), "p")))
	require.Equal(t, "validation_failed", code(validateCredential("The push credential", "", "p")))
	require.NoError(t, validateCredential("The push credential", "u", "p"))
}

func TestImageCursorsRoundTripAndRejectGarbage(t *testing.T) {
	at := time.Date(2026, 2, 3, 4, 5, 6, 789000000, time.UTC)
	id := uuid.New()
	gotAt, gotID, err := decodeImageCursor(encodeImageCursor(at, id))
	require.NoError(t, err)
	require.True(t, gotAt.Equal(at))
	require.Equal(t, id, gotID)

	enc := func(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }
	for name, c := range map[string]string{
		"not base64":      "%%%",
		"no separator":    enc("2026-01-01T00:00:00Z"),
		"bad time":        enc("yesterday|" + id.String()),
		"bad id":          enc("2026-01-01T00:00:00Z|nope"),
		"empty":           enc(""),
		"swapped halves":  enc(id.String() + "|2026-01-01T00:00:00Z"),
		"extra separator": enc("2026-01-01T00:00:00Z|" + id.String() + "|x"),
	} {
		_, _, err := decodeImageCursor(c)
		require.Error(t, err, name)
	}
}

func TestListImagesRefusals(t *testing.T) {
	f := newFixture(t)
	v := f.createExternal("GHCR")

	_, err := f.svc.ListImages(f.ctx, v.ID, "archived", "", 10)
	require.Equal(t, "validation_failed", code(err))
	_, err = f.svc.ListImages(f.ctx, v.ID, "", "garbage", 10)
	require.Equal(t, "invalid_cursor", code(err))
	_, err = f.svc.ListImages(f.ctx, uuid.New(), "", "", 10)
	require.Equal(t, "not_found", code(err))
	other, _ := f.otherTeam()
	_, err = f.svc.ListImages(other, v.ID, "", "", 10)
	require.Equal(t, "not_found", code(err), "another team's registry does not exist")
	_, err = f.svc.ListImages(auth.WithPrincipal(context.Background(), &auth.Principal{Type: auth.TypeToken, WorkspaceID: f.ws.ID, TokenID: new(uuid.New()), Scopes: []string{policy.ScopeServersRead}}), v.ID, "", "", 10)
	require.Equal(t, "forbidden", code(err))
}

// ---- results of checks ----

func (f *fixture) command(kind string, payload any) db.AgentCommand {
	f.t.Helper()
	raw, err := json.Marshal(payload)
	require.NoError(f.t, err)
	id := uuid.New()
	cmd, err := f.q.InsertAgentCommand(context.Background(), db.InsertAgentCommandParams{
		ID: id, ServerID: f.srv.ID, Kind: kind, Payload: raw, IdempotencyKey: id.String(), ExpiresAt: time.Now().Add(time.Hour),
	})
	require.NoError(f.t, err)
	return cmd
}

func TestHandleResultIgnoresCommandsItCannotAttribute(t *testing.T) {
	f := newFixture(t)
	v := f.createExternal("GHCR")
	bg := context.Background()
	failed := agentprotocol.ResultFrame{Outcome: agentprotocol.OutcomeFailed, Error: "boom"}
	lastCheck := func() int {
		got, err := f.svc.Get(f.ctx, v.ID)
		require.NoError(t, err)
		return len(got.LastCheck)
	}

	require.NoError(t, f.svc.HandleResult(bg, uuid.New(), failed), "unknown command")
	require.NoError(t, f.svc.HandleResult(bg, f.command(agentprotocol.KindSyncRoutes, map[string]any{}).ID, failed), "another kind of command")
	require.NoError(t, f.svc.HandleResult(bg, f.command(agentprotocol.KindRegistryCheck, "garbage").ID, failed), "a payload that cannot be read")
	require.NoError(t, f.svc.HandleResult(bg, f.command(agentprotocol.KindRegistryCheck, map[string]any{"registryId": "nope"}).ID, failed), "a payload without a registry")
	require.Zero(t, lastCheck(), "none of those recorded a result")

	// A failure without words still tells the person why the check failed.
	ok := f.command(agentprotocol.KindRegistryCheck, map[string]any{"registryId": v.ID.String(), "host": "ghcr.io"})
	require.NoError(t, f.svc.HandleResult(bg, ok.ID, agentprotocol.ResultFrame{Outcome: agentprotocol.OutcomeFailed}))
	got, err := f.svc.Get(f.ctx, v.ID)
	require.NoError(t, err)
	res := got.LastCheck[f.srv.ID.String()]
	require.False(t, res.Ok)
	require.Equal(t, "the agent could not run the check", res.Error)
}

func TestHandleUndeliveredIgnoresWhatItCannotAttribute(t *testing.T) {
	f := newFixture(t)
	v := f.createExternal("GHCR")
	bg := context.Background()

	require.NoError(t, f.svc.HandleUndelivered(bg, uuid.New(), "expired"))
	require.NoError(t, f.svc.HandleUndelivered(bg, f.command(agentprotocol.KindSyncRoutes, map[string]any{}).ID, "expired"))
	require.NoError(t, f.svc.HandleUndelivered(bg, f.command(agentprotocol.KindRegistryCheck, "garbage").ID, "expired"))
	got, err := f.svc.Get(f.ctx, v.ID)
	require.NoError(t, err)
	require.Empty(t, got.LastCheck)

	ok := f.command(agentprotocol.KindRegistryCheck, map[string]any{"registryId": v.ID.String(), "host": "ghcr.io"})
	require.NoError(t, f.svc.HandleUndelivered(bg, ok.ID, "the server was offline for 15 minutes"))
	got, err = f.svc.Get(f.ctx, v.ID)
	require.NoError(t, err)
	res := got.LastCheck[f.srv.ID.String()]
	require.False(t, res.Ok)
	require.Contains(t, res.Error, "offline for 15 minutes")
}

func TestSecretsForACheckCommandRejectUnreadablePayloads(t *testing.T) {
	f := newFixture(t)
	bg := context.Background()
	_, err := f.svc.SecretsForCommand(bg, agentprotocol.KindRegistryCheck, json.RawMessage(`"garbage"`))
	require.ErrorContains(t, err, "decode registry_check payload")
	_, err = f.svc.SecretsForCommand(bg, agentprotocol.KindRegistryCheck, json.RawMessage(`{"registryId":"nope"}`))
	require.ErrorContains(t, err, "registry id")
	_, err = f.svc.SecretsForCommand(bg, agentprotocol.KindRegistryCheck, json.RawMessage(`{"registryId":"`+uuid.NewString()+`"}`))
	require.ErrorContains(t, err, "load registry")
}

func TestClipKeepsMessagesShort(t *testing.T) {
	require.Equal(t, "unauthorized", clip("  unauthorized \n"))
	long := clip(strings.Repeat("é", maxCheckMessage*2))
	require.True(t, strings.HasSuffix(long, "…"))
	require.LessOrEqual(t, len(long), maxCheckMessage+len("…")+2)
}

func TestCheckRefusals(t *testing.T) {
	f := newFixture(t)
	v := f.createExternal("GHCR")
	_, err := f.svc.Check(f.ctx, v.ID, uuid.New())
	require.Equal(t, "server_not_found", code(err))
	other, _ := f.otherTeam()
	_, err = f.svc.Check(other, v.ID, f.srv.ID)
	require.Equal(t, "not_found", code(err))
	_, err = f.svc.Check(f.ctx, uuid.New(), f.srv.ID)
	require.Equal(t, "not_found", code(err))
}

// ---- credentials ----

func TestResolveAndCredentialsOfUnknownOrEmptyRegistries(t *testing.T) {
	f := newFixture(t)
	bg := context.Background()

	_, err := f.svc.Resolve(bg, uuid.New())
	require.Equal(t, "not_found", code(err))
	_, err = f.svc.Credentials(bg, uuid.New(), Scope{Repository: "acme/web", Actions: []string{ActionPush}}, time.Minute)
	require.Equal(t, "not_found", code(err))

	v := f.createExternal("GHCR")
	info, err := f.svc.Resolve(bg, v.ID)
	require.NoError(t, err)
	require.Equal(t, "ghcr.io", info.Host)

	// A registry whose credential was emptied cannot be used, and says which one.
	f.exec(`UPDATE registries SET push_auth_enc = '\x'::bytea WHERE id = $1`, v.ID)
	_, err = f.svc.Credentials(bg, v.ID, Scope{Repository: "acme/web", Actions: []string{ActionPush}}, time.Minute)
	require.ErrorContains(t, err, "registry GHCR has no credential")
}

func TestEncodeAuthRoundTrips(t *testing.T) {
	enc, err := EncodeAuth("bot", "pw", "ghcr.io")
	require.NoError(t, err)
	got, ok := DecodeAuth(enc)
	require.True(t, ok)
	require.Equal(t, agentprotocol.RegistryAuth{Username: "bot", Password: "pw", ServerAddress: "ghcr.io"}, got)
}

// ---- updating ----

func TestUpdateRefusals(t *testing.T) {
	f := newFixture(t)
	v := f.createExternal("GHCR")
	u := func(in UpdateInput) error { _, err := f.svc.Update(f.ctx, v.ID, in); return err }

	require.Equal(t, "validation_failed", code(u(UpdateInput{Name: new("  ")})))
	require.Equal(t, "validation_failed", code(u(UpdateInput{Namespace: new(strings.Repeat("a", maxNamespaceLen+1))})))
	require.Equal(t, "validation_failed", code(u(UpdateInput{PushUsername: new("other-bot")})), "a new username needs the password again")
	require.Equal(t, "validation_failed", code(u(UpdateInput{PullPassword: new("only-a-password")})), "there is no read-only credential to keep the user of")
	require.Equal(t, "validation_failed", code(u(UpdateInput{PullUsername: new("reader")})))
	require.Equal(t, "validation_failed", code(u(UpdateInput{ClearPull: true, PullUsername: new("reader")})))
	require.Equal(t, "secret_required", code(u(UpdateInput{Host: new("registry.example.com")})), "moving the registry sends the credential elsewhere")
	require.Equal(t, "not_found", code(func() error { _, err := f.svc.Update(f.ctx, uuid.New(), UpdateInput{}); return err }()))

	// Nothing refused changed the registry.
	got, err := f.svc.Get(f.ctx, v.ID)
	require.NoError(t, err)
	require.Equal(t, "GHCR", got.Name)
	require.Equal(t, "ghcr.io", got.Host)
}

func TestUpdateMovesTheRegistryOnlyWithBothCredentialsAgain(t *testing.T) {
	f := newFixture(t)
	v := f.createExternal("GHCR")
	_, err := f.svc.Update(f.ctx, v.ID, UpdateInput{PullUsername: new("reader"), PullPassword: new("pullpw")})
	require.NoError(t, err)

	err = func() error {
		_, err := f.svc.Update(f.ctx, v.ID, UpdateInput{Host: new("registry.example.com"), PushPassword: new("newpush")})
		return err
	}()
	require.Equal(t, "secret_required", code(err), "the read-only password goes to the new address too")

	moved, err := f.svc.Update(f.ctx, v.ID, UpdateInput{Host: new("registry.example.com"), PushPassword: new("newpush"), ClearPull: true, ManageRetention: new(true)})
	require.NoError(t, err)
	require.Equal(t, "registry.example.com", moved.Host)
	require.True(t, moved.ManageRetention)
	require.False(t, moved.HasPullAuth)
	require.Equal(t, 1, f.count(`SELECT count(*) FROM audit_events WHERE action = 'registry.update' AND metadata @> '{"changed":["manageRetention","host","pullCredential"]}'`))

	// Changing the read-only username needs the password too.
	_, err = f.svc.Update(f.ctx, v.ID, UpdateInput{PullUsername: new("reader"), PullPassword: new("pw2")})
	require.NoError(t, err)
	_, err = f.svc.Update(f.ctx, v.ID, UpdateInput{PullUsername: new("reader2")})
	require.Equal(t, "validation_failed", code(err))
	info, err := f.svc.Resolve(context.Background(), v.ID)
	require.NoError(t, err)
	require.Equal(t, "registry.example.com", info.Host)
}

func (f *fixture) count(sql string, args ...any) int {
	f.t.Helper()
	var n int
	require.NoError(f.t, f.pool.QueryRow(context.Background(), sql, args...).Scan(&n))
	return n
}

func TestTheBuiltinRegistryViewSaysWhenItIsNotSetUpOrBusy(t *testing.T) {
	f := newFixture(t)
	reg, _ := f.builtinRegistry("Built-in", "registry.test:5000")

	got, err := f.svc.Get(f.ctx, reg.ID)
	require.NoError(t, err)
	require.Equal(t, "ready", got.Builtin.Status)
	require.Equal(t, "idle", got.Builtin.GCState)

	f.setBuiltinStatus("failed")
	got, err = f.svc.Get(f.ctx, reg.ID)
	require.NoError(t, err)
	require.Equal(t, "unavailable", got.Builtin.Status)

	require.NoError(t, f.svc.PatchManagedState(context.Background(), map[string]any{"gc": map[string]any{"readOnly": true}}))
	got, err = f.svc.Get(f.ctx, reg.ID)
	require.NoError(t, err)
	require.NotEmpty(t, got.Builtin.GCState)
}

func TestManagedSecretsRefuseUnusableKeys(t *testing.T) {
	require.ErrorContains(t, ManagedSecrets{}.signingKeyErr(), "signing key is missing")
	_, err := ManagedSecrets{RegistryKeyPEM: string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte("junk")}))}.JWKS()
	require.ErrorContains(t, err, "parse registry signing key")

	other, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	require.NoError(t, err)
	der, err := x509.MarshalPKCS8PrivateKey(other)
	require.NoError(t, err)
	_, err = ManagedSecrets{RegistryKeyPEM: string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))}.JWKS()
	require.ErrorContains(t, err, "not an ECDSA P-256 key")

	good, err := GenerateManagedSecrets()
	require.NoError(t, err)
	require.NotEmpty(t, good.RegistryKeyID)
	jwks, err := good.JWKS()
	require.NoError(t, err)
	require.Contains(t, jwks, good.RegistryKeyID)
	require.NotContains(t, jwks, "PRIVATE")
}

func (m ManagedSecrets) signingKeyErr() error {
	_, err := m.signingKey()
	return err
}

func TestLoadManagedSecretsWithoutOrWithBrokenSecrets(t *testing.T) {
	f := newFixture(t)
	bg := context.Background()
	_, ok, err := LoadManagedSecrets(bg, f.q, f.box)
	require.NoError(t, err)
	require.False(t, ok, "no registry service yet")

	_, err = f.q.UpsertInstanceService(bg, db.UpsertInstanceServiceParams{Kind: instanceKind, Status: "ready"})
	require.NoError(t, err)
	_, ok, err = LoadManagedSecrets(bg, f.q, f.box)
	require.NoError(t, err)
	require.False(t, ok, "a registry service without a signing key")

	require.NoError(t, f.q.SetInstanceServiceSecrets(bg, db.SetInstanceServiceSecretsParams{Kind: instanceKind, SecretsEnc: []byte("not sealed by this key")}))
	_, _, err = LoadManagedSecrets(bg, f.q, f.box)
	require.ErrorContains(t, err, "decrypt registry secrets")

	sealed, err := f.box.Seal(secretbox.InstanceServicesSecrets, instanceKind, []byte("not json"))
	require.NoError(t, err)
	require.NoError(t, f.q.SetInstanceServiceSecrets(bg, db.SetInstanceServiceSecretsParams{Kind: instanceKind, SecretsEnc: sealed}))
	_, _, err = LoadManagedSecrets(bg, f.q, f.box)
	require.ErrorContains(t, err, "decode registry secrets")
}

func TestSigningADeleteTokenNeedsTheBuiltinKey(t *testing.T) {
	f := newFixture(t)
	_, err := f.svc.signToken(context.Background(), time.Minute, RepoAccess{Name: "acme/web", Actions: []string{ActionDelete}})
	require.ErrorContains(t, err, "the built-in registry has no signing key", "without the registry's key nothing can be signed")
}

// ---- sweep jobs ----

func TestSweepJobRunsTheRetentionSweep(t *testing.T) {
	f := newFixture(t)
	bg := context.Background()
	reg, ms := f.builtinRegistry("Built-in", "registry.test:5000")
	img := f.image(reg.ID, "acme/web", digest(1), ImageExpired)
	sr := &sweepRegistry{t: t, ms: ms}
	f.httpFn = sr.round

	jm := jobs.New(f.pool, nil)
	RegisterJobs(jm, f.svc)
	require.NoError(t, jm.Migrate(bg))
	require.NoError(t, jm.Start(bg))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		require.NoError(t, jm.Stop(ctx))
	})
	require.Equal(t, "registry_sweep", SweepArgs{}.Kind())
	require.NoError(t, jm.Insert(bg, SweepArgs{}, &river.InsertOpts{MaxAttempts: 1}))

	require.Eventually(t, func() bool { return f.imageState(img.ID) == ImageDeleted }, 15*time.Second, 50*time.Millisecond)
}

func TestSweepWithoutABuiltinURLSkipsAndNamesWhy(t *testing.T) {
	f := newFixture(t)
	reg, _ := f.builtinRegistry("Built-in", "registry.test:5000")
	f.svc.BuiltinURL = ""
	res, err := f.svc.SweepRegistry(context.Background(), reg.ID)
	require.NoError(t, err)
	require.Equal(t, "the built-in registry is not configured", res.Skipped)
	require.Zero(t, res.Deleted)

	_, err = f.svc.SweepRegistry(context.Background(), uuid.New())
	require.Error(t, err)
}

// ---- HTTP ----

func TestHTTPRegistryLifecycle(t *testing.T) {
	f := newFixture(t)
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

	status, body := do(http.MethodPost, "/registries", map[string]any{"name": "GHCR", "host": "GHCR.io", "namespace": "acme", "pushUsername": "bot", "pushPassword": "ghp_secret_value"})
	require.Equal(t, http.StatusCreated, status, body)
	id := body["id"].(string)
	require.Equal(t, "ghcr.io", body["host"])
	raw, _ := json.Marshal(body)
	require.NotContains(t, string(raw), "ghp_secret_value")

	status, body = do(http.MethodPost, "/registries", map[string]any{"name": "GHCR", "host": "ghcr.io", "pushUsername": "bot", "pushPassword": "x"})
	require.Equal(t, http.StatusConflict, status)
	require.Equal(t, "registry_name_taken", body["code"])
	status, body = do(http.MethodPost, "/registries", map[string]any{"name": "Bad", "host": "https://ghcr.io", "pushUsername": "bot", "pushPassword": "x"})
	require.Equal(t, http.StatusUnprocessableEntity, status)
	require.Equal(t, "validation_failed", body["code"])

	status, body = do(http.MethodGet, "/registries", nil)
	require.Equal(t, http.StatusOK, status)
	require.Len(t, body["items"], 1)
	status, body = do(http.MethodGet, "/registries/"+id, nil)
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, "GHCR", body["name"])
	status, _ = do(http.MethodGet, "/registries/"+uuid.NewString(), nil)
	require.Equal(t, http.StatusNotFound, status)

	status, body = do(http.MethodPatch, "/registries/"+id, map[string]any{"name": "GitHub", "manageRetention": true})
	require.Equal(t, http.StatusOK, status, body)
	require.Equal(t, "GitHub", body["name"])

	status, body = do(http.MethodGet, "/registries/"+id+"/images?state=live&limit=5", nil)
	require.Equal(t, http.StatusOK, status, body)
	require.Empty(t, body["items"])
	status, body = do(http.MethodGet, "/registries/"+id+"/images?cursor=garbage", nil)
	require.Equal(t, http.StatusBadRequest, status)
	require.Equal(t, "invalid_cursor", body["code"])

	status, body = do(http.MethodPost, "/registries/"+id+"/check", map[string]any{"serverId": uuid.NewString()})
	require.Equal(t, http.StatusUnprocessableEntity, status)
	require.Equal(t, "server_not_found", body["code"])

	status, _ = do(http.MethodDelete, "/registries/"+id, nil)
	require.Equal(t, http.StatusNoContent, status)
	status, _ = do(http.MethodGet, "/registries/"+id, nil)
	require.Equal(t, http.StatusNotFound, status)
}
