package gitproviders_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/gitproviders"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// text answers a provider's raw-file route with plain text.
func (f *fakeProvider) text(route, body string) {
	f.on(route, func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, body)
	})
}

const exampleFile = "# Postgres connection\nDATABASE_URL=\nSECRET_KEY=changeme\nAPP_NAME=demo\n"

func keysByFile(files []agentprotocol.EnvFileScan) map[string][]string {
	out := map[string][]string{}
	for _, f := range files {
		for _, v := range f.Vars {
			out[f.Path] = append(out[f.Path], v.Key)
		}
	}
	return out
}

func TestReadEnvTemplatesGitea(t *testing.T) {
	f := newFixture(t)
	conn := f.giteaConnection()
	f.giteaRepo("main")
	f.provider.text("GET /api/v1/repos/acme/shop/raw/.env.example", exampleFile)
	f.provider.text("GET /api/v1/repos/acme/shop/raw/api/.env.sample", "REDIS_URL=redis://localhost:6379\n")

	r, err := f.svc.OpenRepo(f.ctx, conn.Connection.ID, "acme/shop", "")
	require.NoError(t, err)
	require.Equal(t, "main", r.Branch, "the default branch when none is given")

	files, err := gitproviders.ReadEnvTemplates(f.ctx, r, "api")
	require.NoError(t, err)
	require.Len(t, files, 2)
	require.Equal(t, ".env.example", files[0].Path)
	require.Equal(t, "api/.env.sample", files[1].Path)
	require.Equal(t, "DATABASE_URL", files[0].Vars[0].Key)
	require.Equal(t, "Postgres connection", files[0].Vars[0].Comment, "the comment above a key is its hint")
	require.Equal(t, "changeme", files[0].Vars[1].Value)

	// Only the four template names were requested, in the root and in the
	// build context; real env files are never read.
	var asked []string
	for _, c := range f.provider.calls {
		if strings.Contains(c.Path, "/raw/") {
			asked = append(asked, strings.TrimPrefix(c.Path, "/api/v1/repos/acme/shop/raw/"))
			require.Equal(t, "main", urlQuery(t, c.Query).Get("ref"))
		}
	}
	require.ElementsMatch(t, []string{
		".env.example", ".env.sample", ".env.template", ".env.dist",
		"api/.env.example", "api/.env.sample", "api/.env.template", "api/.env.dist",
	}, asked)
	for _, p := range asked {
		require.NotContains(t, []string{".env", ".env.local", ".env.production", "api/.env"}, p)
	}
}

func urlQuery(t *testing.T, raw string) url.Values {
	t.Helper()
	q, err := url.ParseQuery(raw)
	require.NoError(t, err)
	return q
}

func TestReadEnvTemplatesNothingFoundAndBadContext(t *testing.T) {
	f := newFixture(t)
	conn := f.giteaConnection()
	f.giteaRepo("main")
	r, err := f.svc.OpenRepo(f.ctx, conn.Connection.ID, "acme/shop", "main")
	require.NoError(t, err)

	files, err := gitproviders.ReadEnvTemplates(f.ctx, r, "")
	require.NoError(t, err)
	require.Empty(t, files, "a repository without template files is not an error")

	_, err = gitproviders.ReadEnvTemplates(f.ctx, r, "../outside")
	require.Error(t, err, "a build context outside the repository is refused")
	require.Empty(t, f.provider.callsTo("GET", "/api/v1/repos/acme/shop/raw/../outside/.env.example"))
}

func TestReadEnvTemplatesProviderErrorIsNotSwallowed(t *testing.T) {
	f := newFixture(t)
	conn := f.giteaConnection()
	f.giteaRepo("main")
	// The provider announces its rate limit the way GitHub does, in headers.
	f.provider.on("GET /api/v1/repos/acme/shop/raw/.env.example", func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, "internal-service-secret")
	})
	r, err := f.svc.OpenRepo(f.ctx, conn.Connection.ID, "acme/shop", "main")
	require.NoError(t, err)
	_, err = gitproviders.ReadEnvTemplates(f.ctx, r, "")
	require.Error(t, err, "a failing provider is reported, not read as 'no template file'")
	require.Contains(t, err.Error(), "rate limited")
	require.NotContains(t, err.Error(), "internal-service-secret", "the provider's body is never echoed")
}

func TestReadEnvTemplatesForbiddenWithoutRateLimitHeadersIsNotCalledRateLimited(t *testing.T) {
	f := newFixture(t)
	conn := f.giteaConnection()
	f.giteaRepo("main")
	f.provider.json("GET /api/v1/repos/acme/shop/raw/.env.example", 403, map[string]string{"message": "rate limited"})
	r, err := f.svc.OpenRepo(f.ctx, conn.Connection.ID, "acme/shop", "main")
	require.NoError(t, err)
	_, err = gitproviders.ReadEnvTemplates(f.ctx, r, "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "Forbidden", "classified from status and headers, not from the body")
	require.NotContains(t, err.Error(), "rate limited")
}

func TestReadEnvTemplatesSkipsOversizedFile(t *testing.T) {
	f := newFixture(t)
	conn := f.giteaConnection()
	f.giteaRepo("main")
	f.provider.text("GET /api/v1/repos/acme/shop/raw/.env.example", strings.Repeat("A=1\n", 20000)) // 80 KB
	f.provider.text("GET /api/v1/repos/acme/shop/raw/.env.dist", "B=2\n")
	r, err := f.svc.OpenRepo(f.ctx, conn.Connection.ID, "acme/shop", "main")
	require.NoError(t, err)
	files, err := gitproviders.ReadEnvTemplates(f.ctx, r, "")
	require.NoError(t, err)
	require.Equal(t, map[string][]string{".env.dist": {"B"}}, keysByFile(files))
}

func TestReadEnvTemplatesGitLab(t *testing.T) {
	f := newFixture(t)
	f.provider.json("GET /api/v4/user", 200, map[string]string{"username": "alice"})
	res, err := f.svc.CreateConnection(f.ctx, gitproviders.CreateConnectionInput{
		Provider: gitproviders.GitLab, Name: "GL", BaseURL: f.provider.srv.URL, AuthType: gitproviders.AuthToken, Token: "glpat-secret-token-1234",
	})
	require.NoError(t, err)
	f.provider.json("GET /api/v4/projects/acme/shop", 200, map[string]any{"path_with_namespace": "acme/shop", "default_branch": "develop", "http_url_to_repo": "https://gitlab.example.com/acme/shop.git"})
	f.provider.text("GET /api/v4/projects/acme/shop/repository/files/.env.example/raw", "TOKEN_X=\n")

	r, err := f.svc.OpenRepo(f.ctx, res.Connection.ID, "acme/shop", "")
	require.NoError(t, err)
	require.Equal(t, "develop", r.Branch)
	files, err := gitproviders.ReadEnvTemplates(f.ctx, r, "")
	require.NoError(t, err)
	require.Equal(t, map[string][]string{".env.example": {"TOKEN_X"}}, keysByFile(files))
	calls := f.provider.callsTo("GET", "/api/v4/projects/acme/shop/repository/files/.env.example/raw")
	require.Len(t, calls, 1)
	require.Equal(t, "develop", urlQuery(t, calls[0].Query).Get("ref"))
	require.Equal(t, "Bearer glpat-secret-token-1234", calls[0].Auth, "the connection's credentials authenticate the read")
}

func TestOpenRepoRefusesWithoutAPIAndWithoutScope(t *testing.T) {
	f := newFixture(t)
	bb, err := f.svc.CreateConnection(f.ctx, gitproviders.CreateConnectionInput{Provider: gitproviders.Bitbucket, Name: "Bitbucket"})
	require.NoError(t, err)
	var ae *apierr.Error
	_, err = f.svc.OpenRepo(f.ctx, bb.Connection.ID, "acme/shop", "main")
	require.ErrorAs(t, err, &ae)
	require.Equal(t, "no_repo_api", ae.Code, "Bitbucket has no file API: the agent reads it instead")

	conn := f.giteaConnection()
	f.giteaRepo("main")
	_, err = f.svc.OpenRepo(f.reader, conn.Connection.ID, "acme/shop", "main")
	require.ErrorAs(t, err, &ae)
	require.Equal(t, http.StatusForbidden, ae.Status, "reading a connection's repositories needs projects:write")
}

func TestOpenRepoInternalNeedsNoPrincipalAndServiceRepo(t *testing.T) {
	f := newFixture(t)
	conn := f.giteaConnection()
	f.giteaRepo("main")
	f.provider.text("GET /api/v1/repos/acme/shop/raw/.env.example", "A=1\n")
	svc := f.gitService("api")
	f.link(svc.ID, conn.Connection.ID, gitproviders.LinkInput{Branch: "main"})

	bg := context.Background()
	id, repo, ok, err := f.svc.ServiceRepo(bg, svc.ID)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, conn.Connection.ID, id)
	require.Equal(t, "acme/shop", repo)

	// A push subscriber or job has no principal.
	r, err := f.svc.OpenRepoInternal(bg, id, repo, "main")
	require.NoError(t, err)
	files, err := gitproviders.ReadEnvTemplates(bg, r, "")
	require.NoError(t, err)
	require.Len(t, files, 1)
	_, err = f.svc.OpenRepo(bg, id, repo, "main")
	require.Error(t, err, "the authorized opener still refuses a caller without a principal")

	_, _, ok, err = f.svc.ServiceRepo(bg, f.gitService("other").ID)
	require.NoError(t, err)
	require.False(t, ok, "a service with a pasted URL has no link")
}

// rewriteTransport sends every request to the test server, whatever the host.
type rewriteTransport struct{ to *url.URL }

func (rt rewriteTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("X-Original-Host", r.URL.Host)
	r.URL.Scheme, r.URL.Host = rt.to.Scheme, rt.to.Host
	return http.DefaultTransport.RoundTrip(r)
}

func TestReadEnvTemplatesPublicRepositories(t *testing.T) {
	var hosts, paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hosts = append(hosts, r.Header.Get("X-Original-Host"))
		paths = append(paths, r.URL.Path)
		if strings.HasSuffix(r.URL.Path, "/.env.example") {
			_, _ = io.WriteString(w, "PUBLIC_KEY_X=1\n")
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	to, _ := url.Parse(srv.URL)
	f := newFixture(t)
	svc := gitproviders.NewService(gitproviders.Deps{
		Pool: f.pool, Box: f.box, Services: f.svcs, Deployer: f.dep,
		HTTP:   &http.Client{Transport: rewriteTransport{to: to}},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})

	r, err := svc.OpenPublicRepo(f.ctx, "https://github.com/acme/shop.git", "main")
	require.NoError(t, err)
	files, err := gitproviders.ReadEnvTemplates(f.ctx, r, "")
	require.NoError(t, err)
	require.Equal(t, map[string][]string{".env.example": {"PUBLIC_KEY_X"}}, keysByFile(files))
	require.Equal(t, "raw.githubusercontent.com", hosts[0])
	require.Equal(t, "/acme/shop/main/.env.example", paths[0])

	hosts, paths = nil, nil
	r, err = svc.OpenPublicRepo(f.ctx, "https://gitlab.com/acme/shop", "")
	require.NoError(t, err)
	_, err = gitproviders.ReadEnvTemplates(f.ctx, r, "")
	require.NoError(t, err)
	require.Equal(t, "gitlab.com", hosts[0])
	require.Equal(t, "/acme/shop/-/raw/main/.env.example", paths[0])

	var ae *apierr.Error
	_, err = svc.OpenPublicRepo(f.ctx, "https://bitbucket.org/acme/shop.git", "main")
	require.ErrorAs(t, err, &ae)
	require.Equal(t, "unsupported_host", ae.Code, "other hosts are read by the agent")
}
