package secretmgr

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/db"
)

const (
	dopplerServiceToken  = "dp.st.backend-prd-token-0123456789"
	dopplerPersonalToken = "dp.pt.personal-token-0123456789"
)

// fakeDoppler implements the two endpoints the provider uses, with Doppler's
// token rules: a service token is bound to backend/prd and needs no project or
// config; a personal token needs both. Errors use Doppler's `messages` shape.
type fakeDoppler struct {
	srv       *httptest.Server
	downloads atomic.Int32

	mu    sync.Mutex
	query []url.Values // the query of every download, in order
}

func (f *fakeDoppler) queries() []url.Values {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]url.Values(nil), f.query...)
}

func newFakeDoppler(t *testing.T) *fakeDoppler {
	t.Helper()
	f := &fakeDoppler{}
	secrets := map[string]string{
		"backend/prd": `{"DATABASE_URL":"postgres://doppler:pw-from-doppler@db/app","API_KEY":"key-0123456789","NOT_TEXT":null}`,
		"web/dev":     `{"WEB_TOKEN":"web-token-0123456789"}`,
	}
	mux := http.NewServeMux()
	auth := func(w http.ResponseWriter, r *http.Request) (token string, ok bool) {
		token = r.Header.Get("Authorization")
		switch token {
		case "Bearer " + dopplerServiceToken, "Bearer " + dopplerPersonalToken:
			return token, true
		}
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"messages":["Invalid Auth token"],"success":false}`))
		return "", false
	}
	mux.HandleFunc("/v3/me", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := auth(w, r); !ok {
			return
		}
		_, _ = w.Write([]byte(`{"type":"service","name":"nebulactrl"}`))
	})
	mux.HandleFunc("/v3/configs/config/secrets/download", func(w http.ResponseWriter, r *http.Request) {
		token, ok := auth(w, r)
		if !ok {
			return
		}
		f.downloads.Add(1)
		q := r.URL.Query()
		f.mu.Lock()
		f.query = append(f.query, q)
		f.mu.Unlock()
		if q.Get("format") != "json" || q.Get("include_dynamic_secrets") != "false" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"messages":["unexpected query"],"success":false}`))
			return
		}
		project, config := q.Get("project"), q.Get("config")
		if token == "Bearer "+dopplerServiceToken {
			if project == "" && config == "" {
				project, config = "backend", "prd" // bound to one config
			}
		} else if project == "" || config == "" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"messages":["Must provide project and config"],"success":false}`))
			return
		}
		body, found := secrets[project+"/"+config]
		if !found || (token == "Bearer "+dopplerServiceToken && project+"/"+config != "backend/prd") {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"messages":["Could not find requested config"],"success":false}`))
			return
		}
		_, _ = w.Write([]byte(body))
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func dopplerCfg(site string) SecretManagerConfig { return SecretManagerConfig{SiteURL: site} }

func TestDopplerServiceTokenReadsItsConfigOncePerSession(t *testing.T) {
	fd := newFakeDoppler(t)
	p, err := newProvider(KindDoppler, dopplerCfg(fd.srv.URL), dopplerServiceToken, nil)
	require.NoError(t, err)
	ctx := context.Background()
	require.NoError(t, p.check(ctx))

	v, err := p.fetch(ctx, ref(t, "doppler:dp#DATABASE_URL"))
	require.NoError(t, err)
	require.Equal(t, "postgres://doppler:pw-from-doppler@db/app", v)
	v, err = p.fetch(ctx, ref(t, "doppler:dp#API_KEY"))
	require.NoError(t, err)
	require.Equal(t, "key-0123456789", v)
	require.EqualValues(t, 1, fd.downloads.Load(), "one download serves every secret of the config")

	// A service token is bound to its config: nothing names a project or config.
	q := fd.queries()[0]
	require.Equal(t, "json", q.Get("format"))
	require.Equal(t, "false", q.Get("include_dynamic_secrets"), "dynamic secrets are never leased")
	require.False(t, q.Has("project"))
	require.False(t, q.Has("config"))

	// Naming the config the token is bound to also works; another one is refused by Doppler.
	v, err = p.fetch(ctx, ref(t, "doppler:dp/backend/prd#API_KEY"))
	require.NoError(t, err)
	require.Equal(t, "key-0123456789", v)
	_, err = p.fetch(ctx, ref(t, "doppler:dp/web/dev#WEB_TOKEN"))
	require.ErrorContains(t, err, `project "web", config "dev" was not found`)
}

func TestDopplerPersonalTokenNeedsProjectAndConfig(t *testing.T) {
	fd := newFakeDoppler(t)
	ctx := context.Background()

	// The reference names them.
	p, err := newProvider(KindDoppler, dopplerCfg(fd.srv.URL), dopplerPersonalToken, nil)
	require.NoError(t, err)
	v, err := p.fetch(ctx, ref(t, "doppler:dp/web/dev#WEB_TOKEN"))
	require.NoError(t, err)
	require.Equal(t, "web-token-0123456789", v)
	q := fd.queries()[0]
	require.Equal(t, "web", q.Get("project"))
	require.Equal(t, "dev", q.Get("config"))

	// Without them the integration has to carry defaults; otherwise the error says how to fix it.
	_, err = p.fetch(ctx, ref(t, "doppler:dp#API_KEY"))
	require.ErrorContains(t, err, "Must provide project and config")
	require.ErrorContains(t, err, "name the project and config in the reference")

	cfg := dopplerCfg(fd.srv.URL)
	cfg.DefaultProject, cfg.DefaultConfig = "backend", "prd"
	p, err = newProvider(KindDoppler, cfg, dopplerPersonalToken, nil)
	require.NoError(t, err)
	v, err = p.fetch(ctx, ref(t, "doppler:dp#API_KEY"))
	require.NoError(t, err)
	require.Equal(t, "key-0123456789", v)
	// A reference's own project and config win over the defaults; each
	// (project, config) is downloaded once per session.
	before := fd.downloads.Load()
	for range 2 {
		v, err = p.fetch(ctx, ref(t, "doppler:dp/web/dev#WEB_TOKEN"))
		require.NoError(t, err)
		require.Equal(t, "web-token-0123456789", v)
	}
	require.EqualValues(t, 1, fd.downloads.Load()-before)
	require.Equal(t, "web", fd.queries()[len(fd.queries())-1].Get("project"))
}

func TestDopplerErrorsNameTheProblemNotTheToken(t *testing.T) {
	fd := newFakeDoppler(t)
	ctx := context.Background()

	bad, err := newProvider(KindDoppler, dopplerCfg(fd.srv.URL), "dp.st.wrong-token-value", nil)
	require.NoError(t, err)
	err = bad.check(ctx)
	require.ErrorContains(t, err, "token check failed")
	require.ErrorContains(t, err, "authentication failed")
	require.ErrorContains(t, err, "Invalid Auth token")
	require.NotContains(t, err.Error(), "wrong-token-value")
	_, err = bad.fetch(ctx, ref(t, "doppler:dp#API_KEY"))
	require.ErrorContains(t, err, "authentication failed")
	require.NotContains(t, err.Error(), "wrong-token-value")

	p, _ := newProvider(KindDoppler, dopplerCfg(fd.srv.URL), dopplerServiceToken, nil)
	_, err = p.fetch(ctx, ref(t, "doppler:dp#NOPE"))
	require.ErrorContains(t, err, `no secret "NOPE" in the config the service token is bound to`)
	_, err = p.fetch(ctx, ref(t, "doppler:dp#NOT_TEXT"))
	require.ErrorContains(t, err, `no secret "NOT_TEXT"`, "a secret that is not text is not readable")

	pt, _ := newProvider(KindDoppler, dopplerCfg(fd.srv.URL), dopplerPersonalToken, nil)
	_, err = pt.fetch(ctx, ref(t, "doppler:dp/backend/stg#K"))
	require.ErrorContains(t, err, `project "backend", config "stg" was not found`)
	_, err = pt.fetch(ctx, ref(t, "doppler:dp/backend/prd#NOPE"))
	require.ErrorContains(t, err, `no secret "NOPE" in Doppler project "backend", config "prd"`)

	// Unreachable and redirecting servers never leak the token either.
	srv := httptest.NewServer(http.NotFoundHandler())
	addr := srv.URL
	srv.Close()
	down, _ := newProvider(KindDoppler, dopplerCfg(addr), dopplerServiceToken, nil)
	_, err = down.fetch(ctx, ref(t, "doppler:dp#K"))
	require.ErrorContains(t, err, "cannot reach")
	require.NotContains(t, err.Error(), dopplerServiceToken)

	redir := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "http://evil.invalid/steal")
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer redir.Close()
	rp, _ := newProvider(KindDoppler, dopplerCfg(redir.URL), dopplerServiceToken, nil)
	_, err = rp.fetch(ctx, ref(t, "doppler:dp#K"))
	require.ErrorContains(t, err, "redirected")
}

func TestNormalizeDoppler(t *testing.T) {
	c, err := normalize(KindDoppler, SecretManagerConfig{})
	require.NoError(t, err)
	require.Equal(t, SecretManagerConfig{SiteURL: DefaultDopplerURL}, c)

	c, err = normalize(KindDoppler, SecretManagerConfig{SiteURL: "https://doppler.example.com/", DefaultProject: " backend ", DefaultConfig: "prd_us"})
	require.NoError(t, err)
	require.Equal(t, SecretManagerConfig{SiteURL: "https://doppler.example.com", DefaultProject: "backend", DefaultConfig: "prd_us"}, c)

	for _, bad := range []SecretManagerConfig{
		{DefaultProject: "backend"},
		{DefaultConfig: "prd"},
		{DefaultProject: "back end", DefaultConfig: "prd"},
		{DefaultProject: "backend", DefaultConfig: ".."},
		{SiteURL: "doppler.example.com"},
		{SiteURL: "https://u:p@doppler.example.com"},
	} {
		_, err := normalize(KindDoppler, bad)
		require.Error(t, err, "%+v", bad)
	}
	// Vault fields do not carry over.
	c, err = normalize(KindDoppler, SecretManagerConfig{Address: "https://bao", Mount: "kv", ClientID: "x"})
	require.NoError(t, err)
	require.Equal(t, SecretManagerConfig{SiteURL: DefaultDopplerURL}, c)

	// Only the API URL points the token somewhere else.
	a := SecretManagerConfig{SiteURL: "https://api.doppler.com"}
	require.False(t, identityChanged(KindDoppler, a, SecretManagerConfig{SiteURL: "https://api.doppler.com", DefaultProject: "p", DefaultConfig: "c"}))
	require.True(t, identityChanged(KindDoppler, a, SecretManagerConfig{SiteURL: "https://doppler.internal"}))
}

func TestDopplerExampleAndKindMatch(t *testing.T) {
	require.Equal(t, "${{doppler:dp/backend/prd#DATABASE_URL}}", example("dp", KindDoppler, SecretManagerConfig{}))
	require.Equal(t, "${{doppler:dp#DATABASE_URL}}", example("dp", KindDoppler, SecretManagerConfig{DefaultProject: "p", DefaultConfig: "c"}))
	require.Equal(t, "${{vault:bao/apps/shop/db#password}}", example("bao", KindVault, SecretManagerConfig{}))
}

func TestDopplerTestRefUsesTheIntegrationsSyntax(t *testing.T) {
	row := dbIntegration("dp", KindDoppler)
	r, err := testRef(row, TestInput{Path: "backend/prd", Key: "API_KEY"})
	require.NoError(t, err)
	require.Equal(t, "doppler:dp/backend/prd#API_KEY", r.String())
	r, err = testRef(row, TestInput{Key: "API_KEY"})
	require.NoError(t, err)
	require.Equal(t, "doppler:dp#API_KEY", r.String())
	r, err = testRef(row, TestInput{})
	require.NoError(t, err)
	require.Nil(t, r)
	_, err = testRef(row, TestInput{Path: "just-a-project", Key: "K"})
	require.Error(t, err)

	r, err = testRef(dbIntegration("bao", KindVault), TestInput{Path: "apps/shop/db", Key: "password"})
	require.NoError(t, err)
	require.Equal(t, "vault:bao/apps/shop/db#password", r.String())
}

func dbIntegration(name, kind string) db.SecretIntegration {
	return db.SecretIntegration{Name: name, Kind: kind}
}
