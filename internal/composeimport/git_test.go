package composeimport_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/composeimport"
	"github.com/nebulactrl/nebulactrl/internal/gitproviders"
)

type repoFiles struct {
	mu    sync.Mutex
	files map[string]string
	paths []string
}

func (r *repoFiles) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.paths = append(r.paths, req.URL.Path)
	parts := strings.SplitN(strings.TrimPrefix(req.URL.Path, "/"), "/", 4)
	if len(parts) == 4 {
		if body, ok := r.files[parts[3]]; ok {
			_, _ = io.WriteString(w, body)
			return
		}
	}
	http.NotFound(w, req)
}

type rewrite struct{ to *url.URL }

func (rt rewrite) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.URL.Scheme, r.URL.Host = rt.to.Scheme, rt.to.Host
	return http.DefaultTransport.RoundTrip(r)
}

// publicGit serves public-repository reads from a local repoFiles server.
type publicGit struct{ pub *gitproviders.Service }

func (g publicGit) OpenRepo(ctx context.Context, _ uuid.UUID, repo, branch string) (*gitproviders.RepoReader, error) {
	return g.pub.OpenPublicRepo(ctx, "https://github.com/"+repo, branch)
}

func (g publicGit) OpenPublicRepo(ctx context.Context, u, branch string) (*gitproviders.RepoReader, error) {
	return g.pub.OpenPublicRepo(ctx, u, branch)
}

func (g publicGit) CloneURLFor(_ context.Context, _ uuid.UUID, repo string) (string, string, error) {
	return "https://github.com/" + repo + ".git", "main", nil
}

func (g publicGit) Link(context.Context, uuid.UUID, gitproviders.LinkInput) (gitproviders.GitLinkView, error) {
	return gitproviders.GitLinkView{}, nil
}

const repoCompose = `services:
  web:
    image: traefik/whoami:v1.10
    ports:
      - "8080:80"
    env_file: ./web.env
`

func TestPlanReadsTheComposeFileAndItsFilesFromAPublicRepository(t *testing.T) {
	f := newImportFixture(t)
	repo := &repoFiles{files: map[string]string{
		"deploy/compose.yaml": repoCompose,
		"deploy/web.env":      "GREETING=hello\n",
	}}
	srv := httptest.NewServer(repo)
	t.Cleanup(srv.Close)
	to, err := url.Parse(srv.URL)
	require.NoError(t, err)
	f.deps.Git = publicGit{pub: gitproviders.NewService(gitproviders.Deps{
		HTTP: &http.Client{Transport: rewrite{to: to}}, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})}
	svc := composeimport.NewService(f.deps)
	ref := func(r composeimport.ComposeRepoRef) composeimport.ComposePlanRequest {
		return composeimport.ComposePlanRequest{Source: composeimport.ComposeSource{Repo: &r}, ServerID: &f.srv.ID}
	}

	plan, err := svc.Plan(f.ctx, f.env.ID, ref(composeimport.ComposeRepoRef{URL: "https://github.com/acme/shop", Path: "deploy/compose.yaml"}))
	require.NoError(t, err)
	require.True(t, plan.Installable, "%v", plan.Blockers)
	require.Len(t, plan.Services, 1)
	require.Equal(t, "web", plan.Services[0].Name)
	require.Contains(t, repo.paths, "/acme/shop/main/deploy/web.env", "referenced files are read next to the compose file")
	require.Empty(t, f.services(), "a plan writes nothing")

	// Without a path the usual names are tried in order.
	repo.files["docker-compose.yml"] = repoCompose
	repo.files["web.env"] = "GREETING=hi\n"
	plan, err = svc.Plan(f.ctx, f.env.ID, ref(composeimport.ComposeRepoRef{URL: "https://github.com/acme/shop"}))
	require.NoError(t, err)
	require.Len(t, plan.Services, 1)

	// A connection works the same way, and needs its repository.
	conn := uuid.New()
	_, err = svc.Plan(f.ctx, f.env.ID, ref(composeimport.ComposeRepoRef{ConnectionID: &conn, Repo: "acme/shop"}))
	require.NoError(t, err)
	require.Equal(t, "validation_failed", apiCode(t, planErr(svc, f, ref(composeimport.ComposeRepoRef{ConnectionID: &conn}))))
	require.Equal(t, "validation_failed", apiCode(t, planErr(svc, f, ref(composeimport.ComposeRepoRef{}))))

	// Refusals: a path that leaves the repository, a missing file.
	require.Equal(t, "validation_failed", apiCode(t, planErr(svc, f, ref(composeimport.ComposeRepoRef{URL: "https://github.com/acme/shop", Path: "../secrets.yml"}))))
	require.Equal(t, "compose_not_found", apiCode(t, planErr(svc, f, ref(composeimport.ComposeRepoRef{URL: "https://github.com/acme/shop", Path: "nope.yml"}))))
	// A server build without git readers says so.
	require.Equal(t, "git_unavailable", apiCode(t, planErr(f.svc, f, ref(composeimport.ComposeRepoRef{URL: "https://github.com/acme/shop"}))))
}

func planErr(svc *composeimport.Service, f *importFixture, req composeimport.ComposePlanRequest) error {
	_, err := svc.Plan(f.ctx, f.env.ID, req)
	return err
}
