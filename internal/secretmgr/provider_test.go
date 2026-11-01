package secretmgr

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/varexpr"
)

func ref(t *testing.T, s string) varexpr.ExtRef {
	t.Helper()
	r, err := varexpr.ParseExtRef(s)
	require.NoError(t, err)
	return r
}

// fakeVault is a KV v2 server with token and AppRole auth.
type fakeVault struct {
	srv       *httptest.Server
	logins    atomic.Int32
	reads     atomic.Int32
	namespace string
}

func newFakeVault(t *testing.T) *fakeVault {
	t.Helper()
	fv := &fakeVault{}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/auth/approle/login", func(w http.ResponseWriter, r *http.Request) {
		fv.logins.Add(1)
		var body struct{ RoleID, SecretID string }
		var raw map[string]string
		_ = json.NewDecoder(r.Body).Decode(&raw)
		body.RoleID, body.SecretID = raw["role_id"], raw["secret_id"]
		if body.RoleID != "role-1" || body.SecretID != "sid-1" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"errors":["invalid role or secret ID"]}`))
			return
		}
		_, _ = w.Write([]byte(`{"auth":{"client_token":"s.approle-token"}}`))
	})
	mux.HandleFunc("/v1/auth/token/lookup-self", func(w http.ResponseWriter, r *http.Request) {
		if tok := r.Header.Get("X-Vault-Token"); tok != "s.root" && tok != "s.approle-token" {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"errors":["permission denied"]}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":{"id":"x"}}`))
	})
	mux.HandleFunc("/v1/secret/data/", func(w http.ResponseWriter, r *http.Request) {
		if tok := r.Header.Get("X-Vault-Token"); tok != "s.root" && tok != "s.approle-token" {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"errors":["permission denied"]}`))
			return
		}
		if fv.namespace != "" && r.Header.Get("X-Vault-Namespace") != fv.namespace {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"errors":["wrong namespace"]}`))
			return
		}
		fv.reads.Add(1)
		switch strings.TrimPrefix(r.URL.EscapedPath(), "/v1/secret/data/") {
		case "apps/shop/db":
			_, _ = w.Write([]byte(`{"data":{"data":{"password":"hunter2-vault","port":5432,"tls":true,"obj":{"a":1}},"metadata":{"version":3}}}`))
		case "apps/deleted":
			_, _ = w.Write([]byte(`{"data":{"data":null,"metadata":{"deletion_time":"2024-01-01T00:00:00Z"}}}`))
		case "apps/redirect":
			w.Header().Set("Location", "http://evil.invalid/steal")
			w.WriteHeader(http.StatusTemporaryRedirect)
		case "apps/boom":
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"errors":["storage backend unavailable"]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"errors":[]}`))
		}
	})
	fv.srv = httptest.NewServer(mux)
	t.Cleanup(fv.srv.Close)
	return fv
}

func vaultCfg(addr string) SecretManagerConfig {
	return SecretManagerConfig{Address: addr, Mount: "secret", AuthMethod: AuthToken}
}

func TestVaultTokenFetch(t *testing.T) {
	fv := newFakeVault(t)
	p, err := newProvider(KindVault, vaultCfg(fv.srv.URL), "s.root")
	require.NoError(t, err)
	ctx := context.Background()

	v, err := p.fetch(ctx, ref(t, "vault:bao/apps/shop/db#password"))
	require.NoError(t, err)
	require.Equal(t, "hunter2-vault", v)
	v, err = p.fetch(ctx, ref(t, "vault:bao/apps/shop/db#port"))
	require.NoError(t, err)
	require.Equal(t, "5432", v)
	v, err = p.fetch(ctx, ref(t, "vault:bao/apps/shop/db#tls"))
	require.NoError(t, err)
	require.Equal(t, "true", v)
	require.EqualValues(t, 1, fv.reads.Load(), "one read serves every field of the same secret")

	_, err = p.fetch(ctx, ref(t, "vault:bao/apps/shop/db#obj"))
	require.ErrorContains(t, err, "not a string")
	_, err = p.fetch(ctx, ref(t, "vault:bao/apps/shop/db#nope"))
	require.ErrorContains(t, err, `no field "nope"`)
	_, err = p.fetch(ctx, ref(t, "vault:bao/apps/missing#k"))
	require.ErrorContains(t, err, "no secret at apps/missing")
	_, err = p.fetch(ctx, ref(t, "vault:bao/apps/deleted#k"))
	require.ErrorContains(t, err, "deleted or destroyed")
	_, err = p.fetch(ctx, ref(t, "vault:bao/apps/boom#k"))
	require.ErrorContains(t, err, "HTTP 500")
	require.ErrorContains(t, err, "storage backend unavailable")
	_, err = p.fetch(ctx, ref(t, "vault:bao/apps/redirect#k"))
	require.ErrorContains(t, err, "redirected")
}

func TestEscapePathSegments(t *testing.T) {
	require.Equal(t, "a%20b/c/d%3Fe", escapePath("a b/c/d?e"))
}

func TestVaultBadToken(t *testing.T) {
	fv := newFakeVault(t)
	p, err := newProvider(KindVault, vaultCfg(fv.srv.URL), "s.wrong")
	require.NoError(t, err)
	_, err = p.fetch(context.Background(), ref(t, "vault:bao/apps/shop/db#password"))
	require.ErrorContains(t, err, "permission denied")
	require.Error(t, p.check(context.Background()))
}

func TestVaultAppRole(t *testing.T) {
	fv := newFakeVault(t)
	cfg := vaultCfg(fv.srv.URL)
	cfg.AuthMethod, cfg.RoleID, cfg.AppRoleMount = AuthAppRole, "role-1", "approle"
	p, err := newProvider(KindVault, cfg, "sid-1")
	require.NoError(t, err)
	ctx := context.Background()
	require.NoError(t, p.check(ctx))
	v, err := p.fetch(ctx, ref(t, "vault:bao/apps/shop/db#password"))
	require.NoError(t, err)
	require.Equal(t, "hunter2-vault", v)
	require.EqualValues(t, 1, fv.logins.Load(), "one login per session")

	bad, err := newProvider(KindVault, cfg, "wrong-secret-id")
	require.NoError(t, err)
	err = bad.check(ctx)
	require.ErrorContains(t, err, "AppRole login failed")
	require.ErrorContains(t, err, "invalid role or secret ID")
	require.NotContains(t, err.Error(), "wrong-secret-id", "errors never echo the credential")
}

func TestVaultNamespaceHeader(t *testing.T) {
	fv := newFakeVault(t)
	fv.namespace = "team-a"
	cfg := vaultCfg(fv.srv.URL)
	p, _ := newProvider(KindVault, cfg, "s.root")
	_, err := p.fetch(context.Background(), ref(t, "vault:bao/apps/shop/db#password"))
	require.ErrorContains(t, err, "wrong namespace")
	cfg.Namespace = "team-a"
	p, _ = newProvider(KindVault, cfg, "s.root")
	v, err := p.fetch(context.Background(), ref(t, "vault:bao/apps/shop/db#password"))
	require.NoError(t, err)
	require.Equal(t, "hunter2-vault", v)
}

func TestVaultUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	addr := srv.URL
	srv.Close()
	p, _ := newProvider(KindVault, vaultCfg(addr), "s.secret-token-value")
	_, err := p.fetch(context.Background(), ref(t, "vault:bao/a#k"))
	require.ErrorContains(t, err, "cannot reach")
	require.NotContains(t, err.Error(), "s.secret-token-value")
}

func TestVaultPrivateCA(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"data":{"k":"over-tls"},"metadata":{}}}`))
	}))
	defer srv.Close()
	cfg := vaultCfg(srv.URL)
	// Without the CA the connection fails.
	p, _ := newProvider(KindVault, cfg, "s.root")
	_, err := p.fetch(context.Background(), ref(t, "vault:bao/a#k"))
	require.ErrorContains(t, err, "cannot reach")
	// With the server's certificate as CA it works.
	cfg.CACert = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}))
	_, err = normalize(KindVault, cfg)
	require.NoError(t, err)
	p, err = newProvider(KindVault, cfg, "s.root")
	require.NoError(t, err)
	v, err := p.fetch(context.Background(), ref(t, "vault:bao/a#k"))
	require.NoError(t, err)
	require.Equal(t, "over-tls", v)
}

// fakeInfisical implements universal-auth login and the v4 secret read.
func newFakeInfisical(t *testing.T, logins *atomic.Int32) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/auth/universal-auth/login", func(w http.ResponseWriter, r *http.Request) {
		logins.Add(1)
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["clientId"] != "cid" || body["clientSecret"] != "csecret" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"statusCode":401,"message":"Invalid credentials","error":"UnauthorizedError"}`))
			return
		}
		_, _ = w.Write([]byte(`{"accessToken":"inf-token","expiresIn":7200,"accessTokenMaxTTL":7200,"tokenType":"Bearer"}`))
	})
	mux.HandleFunc("/api/v4/secrets/", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer inf-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		q := r.URL.Query()
		name := strings.TrimPrefix(r.URL.Path, "/api/v4/secrets/")
		if q.Get("projectId") != "proj-1" || q.Get("type") != "shared" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"message":"bad query"}`))
			return
		}
		switch q.Get("environment") + q.Get("secretPath") + "#" + name {
		case "prod/backend#DATABASE_URL":
			_, _ = w.Write([]byte(`{"secret":{"secretKey":"DATABASE_URL","secretValue":"postgres://inf:pw@db/app","secretValueHidden":false}}`))
		case "prod/#ROOT_KEY":
			_, _ = w.Write([]byte(`{"secret":{"secretKey":"ROOT_KEY","secretValue":"root-value","secretValueHidden":false}}`))
		case "prod/backend#HIDDEN":
			_, _ = w.Write([]byte(`{"secret":{"secretKey":"HIDDEN","secretValue":"","secretValueHidden":true}}`))
		case "prod/backend#FORBIDDEN":
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"statusCode":403,"message":"Permission denied: you are not allowed to read secrets","error":"Forbidden"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"statusCode":404,"message":"Secret not found","error":"NotFound"}`))
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestInfisicalFetch(t *testing.T) {
	var logins atomic.Int32
	srv := newFakeInfisical(t, &logins)
	cfg := SecretManagerConfig{SiteURL: srv.URL, ClientID: "cid", ProjectID: "proj-1"}
	p, err := newProvider(KindInfisical, cfg, "csecret")
	require.NoError(t, err)
	ctx := context.Background()
	require.NoError(t, p.check(ctx))

	v, err := p.fetch(ctx, ref(t, "vault:inf/prod/backend#DATABASE_URL"))
	require.NoError(t, err)
	require.Equal(t, "postgres://inf:pw@db/app", v)
	v, err = p.fetch(ctx, ref(t, "vault:inf/prod#ROOT_KEY"))
	require.NoError(t, err)
	require.Equal(t, "root-value", v, "no folder means the root folder")
	_, err = p.fetch(ctx, ref(t, "vault:inf/prod/backend#HIDDEN"))
	require.ErrorContains(t, err, "hidden")
	_, err = p.fetch(ctx, ref(t, "vault:inf/prod/backend#FORBIDDEN"))
	require.ErrorContains(t, err, "permission denied")
	_, err = p.fetch(ctx, ref(t, "vault:inf/prod/backend#NOPE"))
	require.ErrorContains(t, err, `no secret "NOPE" in environment "prod"`)
	require.EqualValues(t, 1, logins.Load())

	bad, _ := newProvider(KindInfisical, cfg, "wrong-client-secret")
	err = bad.check(ctx)
	require.ErrorContains(t, err, "machine identity login failed")
	require.ErrorContains(t, err, "Invalid credentials")
	require.NotContains(t, err.Error(), "wrong-client-secret")
}

func TestNormalize(t *testing.T) {
	c, err := normalize(KindVault, SecretManagerConfig{Address: "https://bao.example.com:8200/"})
	require.NoError(t, err)
	require.Equal(t, SecretManagerConfig{Address: "https://bao.example.com:8200", Mount: "secret", AuthMethod: AuthToken}, c)

	_, err = normalize(KindVault, SecretManagerConfig{Address: "bao.example.com"})
	require.Error(t, err)
	_, err = normalize(KindVault, SecretManagerConfig{Address: "https://u:p@bao.example.com"})
	require.Error(t, err)
	_, err = normalize(KindVault, SecretManagerConfig{Address: "https://bao.example.com", AuthMethod: AuthAppRole})
	require.ErrorContains(t, err, "roleId")
	_, err = normalize(KindVault, SecretManagerConfig{Address: "https://bao.example.com", Mount: "../etc"})
	require.Error(t, err)
	c, err = normalize(KindVault, SecretManagerConfig{Address: "http://bao:8200", AuthMethod: AuthAppRole, RoleID: "r", Mount: "/kv/team/", Namespace: "a/b"})
	require.NoError(t, err)
	require.Equal(t, "kv/team", c.Mount)
	require.Equal(t, "approle", c.AppRoleMount)

	c, err = normalize(KindInfisical, SecretManagerConfig{ClientID: "cid", ProjectID: "proj-1", Address: "ignored"})
	require.NoError(t, err)
	require.Equal(t, SecretManagerConfig{SiteURL: DefaultInfisicalURL, ClientID: "cid", ProjectID: "proj-1"}, c, "fields of other kinds are dropped")
	_, err = normalize(KindInfisical, SecretManagerConfig{ClientID: "cid"})
	require.Error(t, err)
	_, err = normalize("bitwarden", SecretManagerConfig{})
	require.ErrorContains(t, err, "vault, infisical or doppler")

	require.True(t, identityChanged(KindVault, SecretManagerConfig{Address: "https://a"}, SecretManagerConfig{Address: "https://b"}))
	require.False(t, identityChanged(KindVault, SecretManagerConfig{Address: "https://a", Mount: "x"}, SecretManagerConfig{Address: "https://a", Mount: "y"}))
	require.True(t, identityChanged(KindInfisical, SecretManagerConfig{ClientID: "a"}, SecretManagerConfig{ClientID: "b"}))
}
