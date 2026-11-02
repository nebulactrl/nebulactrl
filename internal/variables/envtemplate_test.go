package variables_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/deployments"
	"github.com/nebulactrl/nebulactrl/internal/gitproviders"
	"github.com/nebulactrl/nebulactrl/internal/jobs"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/internal/variables"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

const exampleEnv = `# Session signing secret
SECRET_KEY=changeme
DATABASE_URL=postgres://postgres:pw@localhost:5432/shop
PORT=3000
DEBUG=true
`

// repoServer answers the raw-file requests of the public repository reader.
type repoServer struct {
	mu    sync.Mutex
	files map[string]string // path below /acme/shop/<branch>/ -> content
	fail  map[string]int    // path -> status
	paths []string
}

func (r *repoServer) set(files map[string]string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.files = files
}

func (r *repoServer) requests() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.paths...)
}

func (r *repoServer) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.paths = append(r.paths, req.URL.Path)
	parts := strings.SplitN(strings.TrimPrefix(req.URL.Path, "/"), "/", 4)
	rel := ""
	if len(parts) == 4 {
		rel = parts[3]
	}
	if code, ok := r.fail[rel]; ok {
		http.Error(w, "boom", code)
		return
	}
	if body, ok := r.files[rel]; ok {
		_, _ = io.WriteString(w, body)
		return
	}
	http.NotFound(w, req)
}

type rewriteTransport struct{ to *url.URL }

func (rt rewriteTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.URL.Scheme, r.URL.Host = rt.to.Scheme, rt.to.Host
	return http.DefaultTransport.RoundTrip(r)
}

type gitLink struct {
	conn uuid.UUID
	repo string
}

// fakeGit is a variables.GitReaders whose readers come from a real
// gitproviders service talking to the repoServer.
type fakeGit struct {
	mu      sync.Mutex
	pub     *gitproviders.Service
	calls   []string
	repoErr error
	links   map[uuid.UUID]gitLink
}

func (g *fakeGit) note(s string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.calls = append(g.calls, s)
}

func (g *fakeGit) callList() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.calls...)
}

func (g *fakeGit) OpenRepo(ctx context.Context, _ uuid.UUID, repo, branch string) (*gitproviders.RepoReader, error) {
	g.note("OpenRepo")
	if g.repoErr != nil {
		return nil, g.repoErr
	}
	return g.pub.OpenPublicRepo(ctx, "https://github.com/"+repo, branch)
}

func (g *fakeGit) OpenRepoInternal(ctx context.Context, _ uuid.UUID, repo, branch string) (*gitproviders.RepoReader, error) {
	g.note("OpenRepoInternal")
	if g.repoErr != nil {
		return nil, g.repoErr
	}
	return g.pub.OpenPublicRepo(ctx, "https://github.com/"+repo, branch)
}

func (g *fakeGit) OpenPublicRepo(ctx context.Context, repoURL, branch string) (*gitproviders.RepoReader, error) {
	g.note("OpenPublicRepo " + repoURL)
	return g.pub.OpenPublicRepo(ctx, repoURL, branch)
}

func (g *fakeGit) ServiceRepo(_ context.Context, id uuid.UUID) (uuid.UUID, string, bool, error) {
	l, ok := g.links[id]
	return l.conn, l.repo, ok, nil
}

type fakeAgent struct {
	mu         sync.Mutex
	scans      []string // slugs
	previews   []deployments.EnvPreviewInput
	scanRes    agentprotocol.EnvScanResult
	scanErr    error
	previewRes agentprotocol.EnvScanResult
	previewErr error
}

func (a *fakeAgent) ScanEnvTemplates(_ context.Context, sc services.Scope) (agentprotocol.EnvScanResult, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.scans = append(a.scans, sc.Service.Slug)
	return a.scanRes, a.scanErr
}

func (a *fakeAgent) PreviewEnvTemplates(_ context.Context, in deployments.EnvPreviewInput) (agentprotocol.EnvScanResult, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.previews = append(a.previews, in)
	return a.previewRes, a.previewErr
}

type tplFx struct {
	*fx
	repo  *repoServer
	git   *fakeGit
	agent *fakeAgent
}

// newTplFx wires the template reads to a repository server, a fake git
// provider layer and (when withAgent) a fake agent scanner.
func newTplFx(t *testing.T, withAgent bool) *tplFx {
	t.Helper()
	f := newFx(t)
	repo := &repoServer{files: map[string]string{".env.example": exampleEnv}, fail: map[string]int{}}
	srv := httptest.NewServer(repo)
	t.Cleanup(srv.Close)
	to, err := url.Parse(srv.URL)
	require.NoError(t, err)
	pub := gitproviders.NewService(gitproviders.Deps{
		HTTP:   &http.Client{Transport: rewriteTransport{to: to}},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	git := &fakeGit{pub: pub, links: map[uuid.UUID]gitLink{}}
	tf := &tplFx{fx: f, repo: repo, git: git}
	deps := variables.EnvTemplateDeps{Git: git, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if withAgent {
		tf.agent = &fakeAgent{}
		deps.Agent = tf.agent
	}
	f.svc.SetEnvTemplates(deps)
	return tf
}

// withDatabaseExport makes service "pg" export DATABASE_URL for the test.
func withDatabaseExport(t *testing.T) {
	t.Helper()
	prev := services.RegisterExportProvider(func(in services.ExportInput) []services.Export {
		if in.Slug == "pg" {
			return []services.Export{{Key: "DATABASE_URL", Value: "postgres://pg:5432/db", Secret: true}}
		}
		return nil
	})
	t.Cleanup(func() { services.RegisterExportProvider(prev) })
}

func (f *fx) addGitSvc(env db.Environment, slug, gitURL, buildContext string, token bool) db.Service {
	f.t.Helper()
	cfg, err := json.Marshal(map[string]any{"gitUrl": gitURL, "gitBranch": "main", "buildContext": buildContext, "port": 8080})
	require.NoError(f.t, err)
	p := db.InsertServiceParams{
		ID: uuid.New(), EnvironmentID: env.ID, Name: slug, Slug: slug, Kind: "app", SourceType: "git", Config: cfg,
	}
	if token {
		p.GitTokenEnc = []byte("sealed")
	}
	sv, err := f.q.InsertService(context.Background(), p)
	require.NoError(f.t, err)
	return sv
}

func (f *fx) storedEnv(id uuid.UUID) string {
	f.t.Helper()
	var s string
	require.NoError(f.t, f.pool.QueryRow(context.Background(), "SELECT COALESCE(suggested_env::text, '') FROM services WHERE id = $1", id).Scan(&s))
	return s
}

func (f *fx) storedKeys(id uuid.UUID) []string {
	f.t.Helper()
	raw := f.storedEnv(id)
	if raw == "" {
		return nil
	}
	var se struct {
		Files []struct {
			Vars []struct{ Key string } `json:"vars"`
		} `json:"files"`
	}
	require.NoError(f.t, json.Unmarshal([]byte(raw), &se))
	var keys []string
	for _, fl := range se.Files {
		for _, v := range fl.Vars {
			keys = append(keys, v.Key)
		}
	}
	return keys
}

func itemKeys(items []variables.SuggestedVariable) []string {
	out := []string{}
	for _, it := range items {
		out = append(out, it.Key)
	}
	return out
}

func itemByKey(t *testing.T, items []variables.SuggestedVariable, key string) variables.SuggestedVariable {
	t.Helper()
	for _, it := range items {
		if it.Key == key {
			return it
		}
	}
	require.Failf(t, "suggestion missing", "%s not in %v", key, itemKeys(items))
	return variables.SuggestedVariable{}
}

func TestScanTemplatesReadsPublicRepositoryThroughProvider(t *testing.T) {
	withDatabaseExport(t)
	f := newTplFx(t, false)
	f.addSvc(f.env, "pg")
	app := f.addGitSvc(f.env, "app", "https://github.com/acme/shop.git", "", false)
	_, err := f.svc.Put(f.ctx, app.ID, []variables.VariableInput{in("DEBUG", "1")})
	require.NoError(t, err)

	scan, err := f.svc.ScanTemplates(f.ctx, app.ID)
	require.NoError(t, err)
	require.Equal(t, variables.TemplatesFound, scan.Status)
	require.Equal(t, variables.TemplateViaProvider, scan.Method)
	require.Equal(t, "main", scan.Branch)
	require.Empty(t, scan.Commit, "provider reads carry no commit")
	require.Equal(t, "4 keys in 1 file.", scan.Message)
	require.Equal(t, []variables.EnvTemplateFile{{Path: ".env.example", Keys: 4}}, scan.Files)
	require.Equal(t, []string{"SECRET_KEY", "DATABASE_URL"}, itemKeys(scan.Items), "DEBUG is set already and PORT is a platform key")
	secret := itemByKey(t, scan.Items, "SECRET_KEY")
	require.Equal(t, "${{secret(32)}}", secret.Value)
	require.True(t, secret.ProposedSecret)
	require.Equal(t, variables.SuggestEnvExample, secret.Source)
	require.Contains(t, secret.Comment, "signing secret")
	require.Equal(t, "${{pg.DATABASE_URL}}", itemByKey(t, scan.Items, "DATABASE_URL").Value, "a database of the environment fills a localhost URL")
	require.Len(t, scan.Skipped, 1)
	require.Equal(t, "PORT", scan.Skipped[0].Key)
	require.Equal(t, []string{"OpenPublicRepo https://github.com/acme/shop.git"}, f.git.callList())
	require.Equal(t, []string{"/acme/shop/main/.env.example", "/acme/shop/main/.env.sample", "/acme/shop/main/.env.template", "/acme/shop/main/.env.dist"}, f.repo.requests())

	// What was read is the stored suggestion source of the service.
	require.Equal(t, []string{"SECRET_KEY", "DATABASE_URL", "PORT", "DEBUG"}, f.storedKeys(app.ID))
	sug, err := f.svc.Suggestions(f.ctx, app.ID)
	require.NoError(t, err)
	require.Equal(t, "main", sug.Branch)
	require.Equal(t, []string{"SECRET_KEY", "DATABASE_URL"}, itemKeys(sug.Items))
}

func TestScanTemplatesRejectsNonGitAndUnauthorized(t *testing.T) {
	f := newTplFx(t, false)
	img := f.addSvc(f.env, "img")
	_, err := f.svc.ScanTemplates(f.ctx, img.ID)
	require.Equal(t, "not_git_source", apiCode(t, err))

	app := f.addGitSvc(f.env, "app", "https://github.com/acme/shop.git", "", false)
	viewer := f.as(auth.RoleViewer, nil, policy.ScopeProjectsRead)
	_, err = f.svc.ScanTemplates(viewer, app.ID)
	ae, ok := apierr.As(err)
	require.True(t, ok)
	require.Equal(t, http.StatusForbidden, ae.Status)
	_, err = f.svc.ScanTemplates(f.ctx, uuid.New())
	require.Equal(t, "not_found", apiCode(t, err))
	require.Empty(t, f.git.callList(), "nothing is read for a refused request")
}

func TestScanTemplatesWithoutFilesClearsStoredSuggestions(t *testing.T) {
	f := newTplFx(t, false)
	app := f.addGitSvc(f.env, "app", "https://github.com/acme/shop.git", "", false)
	_, err := f.svc.ScanTemplates(f.ctx, app.ID)
	require.NoError(t, err)
	require.NotEmpty(t, f.storedKeys(app.ID))

	f.repo.set(map[string]string{"README.md": "hello"})
	scan, err := f.svc.ScanTemplates(f.ctx, app.ID)
	require.NoError(t, err)
	require.Equal(t, variables.TemplatesNone, scan.Status)
	require.Equal(t, variables.TemplateViaNone, scan.Method)
	require.Contains(t, scan.Message, ".env.example, .env.sample, .env.template, .env.dist")
	require.Empty(t, scan.Files)
	require.Empty(t, scan.Items)
	require.NotNil(t, scan.Items)
	require.Empty(t, f.storedKeys(app.ID), "keys removed from the example stop being suggested")
	sug, err := f.svc.Suggestions(f.ctx, app.ID)
	require.NoError(t, err)
	require.Empty(t, sug.Items)
}

func TestScanTemplatesReadsBuildContextToo(t *testing.T) {
	f := newTplFx(t, false)
	f.repo.set(map[string]string{".env.example": "ROOT_KEY=1\n", "web/.env.example": "WEB_KEY=2\n"})
	app := f.addGitSvc(f.env, "app", "https://github.com/acme/shop.git", "web", false)
	scan, err := f.svc.ScanTemplates(f.ctx, app.ID)
	require.NoError(t, err)
	require.Equal(t, "2 keys in 2 files.", scan.Message)
	require.Equal(t, []variables.EnvTemplateFile{{Path: ".env.example", Keys: 1}, {Path: "web/.env.example", Keys: 1}}, scan.Files)
	require.Equal(t, []string{"ROOT_KEY", "WEB_KEY"}, itemKeys(scan.Items))
}

func TestScanTemplatesFallsBackToAgentForOtherHosts(t *testing.T) {
	f := newTplFx(t, true)
	f.agent.scanRes = agentprotocol.EnvScanResult{Commit: "abc123", Files: []agentprotocol.EnvFileScan{
		{Path: ".env.example", Vars: []agentprotocol.EnvVarHint{{Key: "FROM_AGENT", Value: "yes"}}},
	}}
	app := f.addGitSvc(f.env, "app", "https://bitbucket.org/acme/shop.git", "", false)

	scan, err := f.svc.ScanTemplates(f.ctx, app.ID)
	require.NoError(t, err)
	require.Equal(t, variables.TemplateViaAgent, scan.Method)
	require.Equal(t, "abc123", scan.Commit)
	require.Equal(t, "main", scan.Branch)
	require.Equal(t, []string{"FROM_AGENT"}, itemKeys(scan.Items))
	require.Equal(t, "yes", scan.Items[0].Value)
	require.Equal(t, []string{"app"}, f.agent.scans)
	require.Empty(t, f.repo.requests(), "an unsupported host is never fetched by the control plane")
	require.Contains(t, f.storedEnv(app.ID), "abc123")
	sug, err := f.svc.Suggestions(f.ctx, app.ID)
	require.NoError(t, err)
	require.Equal(t, "abc123", sug.Commit)
}

func TestScanTemplatesWithStoredTokenSkipsPublicRoute(t *testing.T) {
	f := newTplFx(t, true)
	f.agent.scanRes = agentprotocol.EnvScanResult{Files: []agentprotocol.EnvFileScan{{Path: ".env.sample", Vars: []agentprotocol.EnvVarHint{{Key: "PRIVATE_ONE", Value: "x"}}}}}
	app := f.addGitSvc(f.env, "app", "https://github.com/acme/private.git", "", true)
	scan, err := f.svc.ScanTemplates(f.ctx, app.ID)
	require.NoError(t, err)
	require.Equal(t, variables.TemplateViaAgent, scan.Method)
	require.Equal(t, []string{"PRIVATE_ONE"}, itemKeys(scan.Items))
	require.Empty(t, f.git.callList(), "a repository behind a token is not fetched anonymously")
	require.Empty(t, f.repo.requests())
}

func TestScanTemplatesUnavailableLeavesStoredFilesAlone(t *testing.T) {
	f := newTplFx(t, false)
	app := f.addGitSvc(f.env, "app", "https://github.com/acme/shop.git", "", false)
	_, err := f.svc.ScanTemplates(f.ctx, app.ID)
	require.NoError(t, err)
	before := f.storedEnv(app.ID)
	require.NotEmpty(t, before)

	// The provider answers an error for a template file and no agent can step in.
	f.repo.fail[".env.sample"] = http.StatusInternalServerError
	scan, err := f.svc.ScanTemplates(f.ctx, app.ID)
	require.NoError(t, err, "an unreadable repository is a result, not an error")
	require.Equal(t, variables.TemplatesUnavailable, scan.Status)
	require.Equal(t, variables.TemplateViaNone, scan.Method)
	require.True(t, strings.HasPrefix(scan.Message, "The git provider could not be read: "), scan.Message)
	require.NotNil(t, scan.Files)
	require.NotNil(t, scan.Items)
	require.NotNil(t, scan.Skipped)
	require.Equal(t, before, f.storedEnv(app.ID))
}

func TestScanTemplatesProviderErrorFallsBackToAgent(t *testing.T) {
	f := newTplFx(t, true)
	f.repo.fail[".env.sample"] = http.StatusInternalServerError
	f.agent.scanRes = agentprotocol.EnvScanResult{Commit: "c1", Files: []agentprotocol.EnvFileScan{{Path: ".env.example", Vars: []agentprotocol.EnvVarHint{{Key: "VIA_AGENT"}}}}}
	app := f.addGitSvc(f.env, "app", "https://github.com/acme/shop.git", "", false)
	scan, err := f.svc.ScanTemplates(f.ctx, app.ID)
	require.NoError(t, err)
	require.Equal(t, variables.TemplateViaAgent, scan.Method)
	require.Equal(t, []string{"VIA_AGENT"}, itemKeys(scan.Items))
	require.True(t, itemByKey(t, scan.Items, "VIA_AGENT").NeedsInput, "an empty example value needs input")
}

func TestScanTemplatesConnectionWithoutAPI(t *testing.T) {
	f := newTplFx(t, false)
	f.git.repoErr = apierr.Unprocessable("no_repo_api", "raw provider text")
	app := f.addGitSvc(f.env, "app", "https://bitbucket.org/acme/shop.git", "", false)
	f.git.links[app.ID] = gitLink{conn: uuid.New(), repo: "acme/shop"}

	scan, err := f.svc.ScanTemplates(f.ctx, app.ID)
	require.NoError(t, err)
	require.Equal(t, variables.TemplatesUnavailable, scan.Status)
	require.Equal(t, "This git connection has no API access, so the repository is read on the server.", scan.Message)
	require.Equal(t, []string{"OpenRepo"}, f.git.callList(), "the caller's access to the connection is checked")

	// Another provider failure shows its own message.
	f.git.repoErr = apierr.Unprocessable("repo_not_found", "The connection cannot access acme/shop")
	scan, err = f.svc.ScanTemplates(f.ctx, app.ID)
	require.NoError(t, err)
	require.Equal(t, "The connection cannot access acme/shop", scan.Message)
}

func TestScanTemplatesAgentFailuresAreReportedNotRaised(t *testing.T) {
	f := newTplFx(t, true)
	app := f.addGitSvc(f.env, "app", "https://bitbucket.org/acme/shop.git", "", false)
	f.agent.scanErr = apierr.Conflict("server_offline", "The server is offline.")
	scan, err := f.svc.ScanTemplates(f.ctx, app.ID)
	require.NoError(t, err)
	require.Equal(t, variables.TemplatesUnavailable, scan.Status)
	require.Equal(t, "The server is offline.", scan.Message)

	f.agent.scanErr = errors.New("ssh exploded")
	scan, err = f.svc.ScanTemplates(f.ctx, app.ID)
	require.NoError(t, err)
	require.Equal(t, "ssh exploded", scan.Message)

	f.agent.scanErr = errors.New("")
	scan, err = f.svc.ScanTemplates(f.ctx, app.ID)
	require.NoError(t, err)
	require.Equal(t, "The repository could not be read.", scan.Message, "an empty reason gets a generic one")
}

func TestScanTemplatesWithoutConfiguredReadersIsUnavailable(t *testing.T) {
	f := newFx(t)
	app := f.addGitSvc(f.env, "app", "https://github.com/acme/shop.git", "", false)
	scan, err := f.svc.ScanTemplates(f.ctx, app.ID)
	require.NoError(t, err)
	require.Equal(t, variables.TemplatesUnavailable, scan.Status)
	require.Equal(t, "The repository could not be read.", scan.Message)
	require.Empty(t, f.storedEnv(app.ID))
}

func TestPreviewTemplatesValidatesInput(t *testing.T) {
	f := newTplFx(t, true)
	pv := func(in variables.EnvTemplatePreviewInput) error {
		_, err := f.svc.PreviewTemplates(f.ctx, f.env.ID, in)
		return err
	}
	require.Equal(t, "validation_failed", apiCode(t, pv(variables.EnvTemplatePreviewInput{GitURL: "https://github.com/acme/shop", BuildContext: "../etc"})))
	require.Equal(t, "validation_failed", apiCode(t, pv(variables.EnvTemplatePreviewInput{GitURL: "https://github.com/acme/shop", BuildContext: "/abs"})))
	require.Equal(t, "validation_failed", apiCode(t, pv(variables.EnvTemplatePreviewInput{})))
	conn := uuid.New()
	require.Equal(t, "validation_failed", apiCode(t, pv(variables.EnvTemplatePreviewInput{ConnectionID: &conn})), "a connection needs a repository")
	require.Equal(t, "validation_failed", apiCode(t, pv(variables.EnvTemplatePreviewInput{Repo: "acme/shop"})), "a repository needs a connection")

	_, err := f.svc.PreviewTemplates(f.ctx, uuid.New(), variables.EnvTemplatePreviewInput{GitURL: "https://github.com/acme/shop"})
	require.Equal(t, "not_found", apiCode(t, err))
	viewer := f.as(auth.RoleViewer, nil, policy.ScopeProjectsRead)
	_, err = f.svc.PreviewTemplates(viewer, f.env.ID, variables.EnvTemplatePreviewInput{GitURL: "https://github.com/acme/shop"})
	ae, ok := apierr.As(err)
	require.True(t, ok)
	require.Equal(t, http.StatusForbidden, ae.Status)
	require.Empty(t, f.git.callList())
}

func TestPreviewTemplatesPublicRepositoryWithDatabaseReferences(t *testing.T) {
	withDatabaseExport(t)
	f := newTplFx(t, false)
	f.addSvc(f.env, "pg")
	f.repo.set(map[string]string{"web/.env.example": exampleEnv})

	scan, err := f.svc.PreviewTemplates(f.ctx, f.env.ID, variables.EnvTemplatePreviewInput{
		GitURL: " https://github.com/acme/shop.git ", BuildContext: "web",
	})
	require.NoError(t, err)
	require.Equal(t, variables.TemplatesFound, scan.Status)
	require.Equal(t, variables.TemplateViaProvider, scan.Method)
	require.Equal(t, "main", scan.Branch, "the default branch is main for a public repository")
	require.Equal(t, []variables.EnvTemplateFile{{Path: "web/.env.example", Keys: 4}}, scan.Files)
	require.Equal(t, []string{"SECRET_KEY", "DATABASE_URL", "DEBUG"}, itemKeys(scan.Items))
	require.Equal(t, "${{pg.DATABASE_URL}}", itemByKey(t, scan.Items, "DATABASE_URL").Value)
	require.Equal(t, "PORT", scan.Skipped[0].Key)
	require.Equal(t, []string{"OpenPublicRepo https://github.com/acme/shop.git"}, f.git.callList())
}

func TestPreviewTemplatesThroughConnection(t *testing.T) {
	f := newTplFx(t, true)
	conn := uuid.New()
	scan, err := f.svc.PreviewTemplates(f.ctx, f.env.ID, variables.EnvTemplatePreviewInput{ConnectionID: &conn, Repo: " acme/shop ", Branch: "main"})
	require.NoError(t, err)
	require.Equal(t, variables.TemplateViaProvider, scan.Method)
	require.Equal(t, []string{"OpenRepo"}, f.git.callList(), "the previewing user's access to the connection is checked")

	// A connection that cannot read files cannot be previewed on a server either.
	f.git.repoErr = apierr.Unprocessable("no_repo_api", "x")
	scan, err = f.svc.PreviewTemplates(f.ctx, f.env.ID, variables.EnvTemplatePreviewInput{ConnectionID: &conn, Repo: "acme/shop"})
	require.NoError(t, err)
	require.Equal(t, variables.TemplatesUnavailable, scan.Status)
	require.Equal(t, "This repository is read once the service exists and is linked to the connection.", scan.Message)
	require.Empty(t, f.agent.previews)
}

func TestPreviewTemplatesOnTheAgent(t *testing.T) {
	f := newTplFx(t, true)
	f.agent.previewRes = agentprotocol.EnvScanResult{Commit: "deadbeef", Files: []agentprotocol.EnvFileScan{
		{Path: ".env.example", Vars: []agentprotocol.EnvVarHint{{Key: "A", Value: "1"}, {Key: "B", Value: "2"}}},
	}}
	srv := uuid.New()

	// No server picked yet.
	scan, err := f.svc.PreviewTemplates(f.ctx, f.env.ID, variables.EnvTemplatePreviewInput{GitURL: "https://git.example.com/acme/shop.git"})
	require.NoError(t, err)
	require.Equal(t, variables.TemplatesUnavailable, scan.Status)
	require.Equal(t, "Pick a server: this repository is read on the server that builds it.", scan.Message)

	// SSH repositories need the per-service deploy key.
	scan, err = f.svc.PreviewTemplates(f.ctx, f.env.ID, variables.EnvTemplatePreviewInput{GitURL: "git@github.com:acme/shop.git", ServerID: &srv})
	require.NoError(t, err)
	require.Equal(t, "An SSH repository is read once the service exists and has a deploy key.", scan.Message)
	require.Empty(t, f.agent.previews)

	// A pasted token keeps a GitHub URL off the anonymous route.
	scan, err = f.svc.PreviewTemplates(f.ctx, f.env.ID, variables.EnvTemplatePreviewInput{
		GitURL: "https://github.com/acme/shop.git", Branch: " dev ", BuildContext: "api", GitToken: "tok", ServerID: &srv,
	})
	require.NoError(t, err)
	require.Equal(t, variables.TemplateViaAgent, scan.Method)
	require.Equal(t, "deadbeef", scan.Commit)
	require.Equal(t, "dev", scan.Branch)
	require.Equal(t, "2 keys in 1 file.", scan.Message)
	require.Equal(t, []string{"A", "B"}, itemKeys(scan.Items))
	require.Equal(t, []deployments.EnvPreviewInput{{ServerID: srv, GitURL: "https://github.com/acme/shop.git", Branch: "dev", BuildContext: "api", Token: "tok"}}, f.agent.previews)

	// A server that is offline is reported, and nothing is stored anywhere.
	f.agent.previewErr = apierr.Conflict("server_offline", "The server is offline.")
	scan, err = f.svc.PreviewTemplates(f.ctx, f.env.ID, variables.EnvTemplatePreviewInput{GitURL: "https://git.example.com/acme/shop.git", ServerID: &srv})
	require.NoError(t, err)
	require.Equal(t, variables.TemplatesUnavailable, scan.Status)
	require.Equal(t, "The server is offline.", scan.Message)
}

func (f *fx) jobCount() int {
	f.t.Helper()
	return f.count("SELECT count(*) FROM river_job WHERE kind = 'scan_env_templates'")
}

func TestGitSourceChangedSchedulesOneDelayedScan(t *testing.T) {
	f := newTplFx(t, false)
	jm := jobs.New(f.pool, slog.New(slog.NewTextHandler(io.Discard, nil)))
	variables.RegisterJobs(jm, f.svc)
	f.svc.SetEnvTemplates(variables.EnvTemplateDeps{Git: f.git, Jobs: jm})
	app := f.addGitSvc(f.env, "app", "https://github.com/acme/shop.git", "", false)
	other := f.addGitSvc(f.env, "other", "https://github.com/acme/other.git", "", false)
	scopeOf := func(id uuid.UUID) services.Scope {
		sc, err := services.LoadScopeInternal(context.Background(), f.q, id)
		require.NoError(t, err)
		return sc
	}

	before := time.Now()
	f.svc.GitSourceChanged(f.ctx, scopeOf(app.ID))
	require.Equal(t, 1, f.jobCount())
	// A second trigger while the first is pending is folded into it.
	f.svc.GitSourceChanged(f.ctx, scopeOf(app.ID))
	require.Equal(t, 1, f.jobCount())
	// Another service has its own job.
	f.svc.GitSourceChanged(f.ctx, scopeOf(other.ID))
	require.Equal(t, 2, f.jobCount())

	var (
		args     []byte
		attempts int
		at       time.Time
	)
	require.NoError(t, f.pool.QueryRow(context.Background(),
		"SELECT args, max_attempts, scheduled_at FROM river_job WHERE kind = 'scan_env_templates' AND args->>'serviceId' = $1", app.ID.String()).Scan(&args, &attempts, &at))
	require.Equal(t, 4, attempts, "a few retries for an offline server")
	require.True(t, at.After(before.Add(2*time.Second)), "the read waits for the create dialog to link the repository: %s", at.Sub(before))

	// Without collaborators nothing is scheduled.
	g := newFx(t)
	sc, err := services.LoadScopeInternal(context.Background(), g.q, g.addGitSvc(g.env, "app", "https://github.com/acme/shop.git", "", false).ID)
	require.NoError(t, err)
	g.svc.GitSourceChanged(g.ctx, sc)
	require.Zero(t, g.jobCount())
}

func TestTemplateScanJob(t *testing.T) {
	f := newTplFx(t, true)
	run := func(id uuid.UUID) error { return variables.RunTemplateScanJob(f.ctx, f.svc, id) }

	// A service that is gone or no longer built from git has nothing to read.
	require.NoError(t, run(uuid.New()))
	img := f.addSvc(f.env, "img")
	require.NoError(t, run(img.ID))
	require.Empty(t, f.agent.scans)

	app := f.addGitSvc(f.env, "app", "https://github.com/acme/shop.git", "", true) // a token: read on the agent

	// An offline server is retried by River, any other failure is not.
	f.agent.scanErr = apierr.Conflict("server_offline", "The server is offline.")
	require.EqualError(t, run(app.ID), "The server is offline.")
	f.agent.scanErr = apierr.Conflict("env_scan_timeout", "The scan took too long.")
	require.EqualError(t, run(app.ID), "The scan took too long.")
	f.agent.scanErr = apierr.Unprocessable("env_scan_failed", "bad repository")
	require.NoError(t, run(app.ID))
	require.Empty(t, f.storedEnv(app.ID))

	f.agent.scanErr = nil
	f.agent.scanRes = agentprotocol.EnvScanResult{Commit: "c0ffee", Files: []agentprotocol.EnvFileScan{{Path: ".env.example", Vars: []agentprotocol.EnvVarHint{{Key: "JOB_KEY", Value: "1"}}}}}
	require.NoError(t, run(app.ID))
	require.Equal(t, []string{"JOB_KEY"}, f.storedKeys(app.ID))
	require.Contains(t, f.storedEnv(app.ID), "c0ffee")
}

func TestStoredTemplatesAreCappedAndSkipLongPaths(t *testing.T) {
	f := newTplFx(t, true)
	files := []agentprotocol.EnvFileScan{}
	for i := range 10 {
		files = append(files, agentprotocol.EnvFileScan{Path: strings.Repeat("d", i) + "/.env.example", Vars: []agentprotocol.EnvVarHint{{Key: "K" + strings.Repeat("A", i)}}})
	}
	f.agent.scanRes = agentprotocol.EnvScanResult{Files: files}
	app := f.addGitSvc(f.env, "app", "https://bitbucket.org/acme/shop.git", "", false)
	_, err := f.svc.ScanTemplates(f.ctx, app.ID)
	require.NoError(t, err)
	require.Len(t, f.storedKeys(app.ID), 8, "at most 8 files are kept")

	long := strings.Repeat("p", 201) + "/.env.example"
	f.agent.scanRes = agentprotocol.EnvScanResult{Files: []agentprotocol.EnvFileScan{
		{Path: ".env.example", Vars: []agentprotocol.EnvVarHint{{Key: "OK"}}},
		{Path: long, Vars: []agentprotocol.EnvVarHint{{Key: "TOO_LONG"}}},
		{Path: "after/.env.example", Vars: []agentprotocol.EnvVarHint{{Key: "AFTER"}}},
	}}
	_, err = f.svc.ScanTemplates(f.ctx, app.ID)
	require.NoError(t, err)
	require.Equal(t, []string{"OK"}, f.storedKeys(app.ID), "a path over 200 characters ends the stored list")
}

func TestOnPushRereadsOnlyWhatTheChangeTouches(t *testing.T) {
	f := newTplFx(t, false)
	ctx := context.Background()
	conn, err := f.q.InsertGitConnection(ctx, db.InsertGitConnectionParams{
		ID: uuid.New(), WorkspaceID: f.ws.ID, Provider: "github", Name: "gh", BaseUrl: "https://github.com", AuthType: "token",
		Status: "active", WebhookSecretEnc: []byte("x"), Meta: json.RawMessage(`{}`),
	})
	require.NoError(t, err)
	link := func(id uuid.UUID) {
		_, err := f.q.UpsertServiceGit(ctx, db.UpsertServiceGitParams{ServiceID: id, ConnectionID: conn.ID, RepoFullName: "acme/shop", AutoDeploy: true, WatchPaths: []string{}})
		require.NoError(t, err)
		f.git.links[id] = gitLink{conn: conn.ID, repo: "acme/shop"}
	}
	read := f.addGitSvc(f.env, "read", "https://github.com/acme/shop.git", "", false)
	fresh := f.addGitSvc(f.env, "fresh", "https://github.com/acme/shop.git", "", false)
	other := f.addGitSvc(f.env, "otherbranch", "https://github.com/acme/shop.git", "", false)
	img := f.addSvc(f.env, "img")
	link(read.ID)
	link(fresh.ID)
	link(other.ID)
	link(img.ID)
	_, err = f.pool.Exec(ctx, `UPDATE services SET config = jsonb_set(config, '{gitBranch}', '"dev"') WHERE id = $1`, other.ID)
	require.NoError(t, err)

	f.repo.set(map[string]string{".env.example": "OLD_KEY=1\n"})
	_, err = f.svc.ScanTemplates(f.ctx, read.ID)
	require.NoError(t, err)
	require.Equal(t, []string{"OLD_KEY"}, f.storedKeys(read.ID))
	f.repo.set(map[string]string{".env.example": "OLD_KEY=1\nNEW_KEY=2\n"})
	f.git.calls = nil

	push := func(branch string, paths []string, complete bool) {
		f.svc.OnPush(ctx, gitproviders.PushEvent{
			ConnectionID: conn.ID, Provider: gitproviders.GitHub, RepoFullName: "Acme/Shop", Branch: branch, ChangedPaths: paths, PathsComplete: complete,
		})
	}

	// A complete push without a template file skips services read before and
	// reads the one that was never read.
	push("main", []string{"README.md", "src/app.go"}, true)
	require.Equal(t, []string{"OLD_KEY"}, f.storedKeys(read.ID), "already read and untouched")
	require.Equal(t, []string{"OLD_KEY", "NEW_KEY"}, f.storedKeys(fresh.ID), "never read, so read now")
	require.Empty(t, f.storedKeys(other.ID), "it tracks another branch")
	require.Empty(t, f.storedEnv(img.ID), "not a git service")
	require.Equal(t, []string{"OpenRepoInternal"}, f.git.callList(), "a push has no principal: the internal opener, once")

	// A template file anywhere in the push (even in a subdirectory) re-reads.
	push("main", []string{"docs/readme.md", "web/.env.dist"}, true)
	require.Equal(t, []string{"OLD_KEY", "NEW_KEY"}, f.storedKeys(read.ID))

	// A push that does not list its files might have touched one.
	f.repo.set(map[string]string{".env.example": "ONLY_KEY=1\n"})
	push("main", nil, false)
	require.Equal(t, []string{"ONLY_KEY"}, f.storedKeys(read.ID))
	require.Equal(t, []string{"ONLY_KEY"}, f.storedKeys(fresh.ID))

	// .env itself is never a template.
	f.repo.set(map[string]string{".env.example": "LAST=1\n"})
	push("main", []string{".env", ".env.local", ".env.production"}, true)
	require.Equal(t, []string{"ONLY_KEY"}, f.storedKeys(read.ID))

	// A push of another repository finds no service.
	n := len(f.repo.requests())
	f.svc.OnPush(ctx, gitproviders.PushEvent{ConnectionID: conn.ID, RepoFullName: "acme/elsewhere", Branch: "main", PathsComplete: false})
	require.Len(t, f.repo.requests(), n)

	// Without git readers a push is ignored.
	g := newFx(t)
	g.svc.OnPush(ctx, gitproviders.PushEvent{ConnectionID: conn.ID, RepoFullName: "acme/shop", Branch: "main"})
}

func TestOnPushKeepsStoredFilesWhenTheRepositoryCannotBeRead(t *testing.T) {
	f := newTplFx(t, false)
	ctx := context.Background()
	conn, err := f.q.InsertGitConnection(ctx, db.InsertGitConnectionParams{
		ID: uuid.New(), WorkspaceID: f.ws.ID, Provider: "github", Name: "gh", BaseUrl: "https://github.com", AuthType: "token",
		Status: "active", WebhookSecretEnc: []byte("x"), Meta: json.RawMessage(`{}`),
	})
	require.NoError(t, err)
	app := f.addGitSvc(f.env, "app", "https://github.com/acme/shop.git", "", false)
	_, err = f.q.UpsertServiceGit(ctx, db.UpsertServiceGitParams{ServiceID: app.ID, ConnectionID: conn.ID, RepoFullName: "acme/shop", WatchPaths: []string{}})
	require.NoError(t, err)
	f.git.links[app.ID] = gitLink{conn: conn.ID, repo: "acme/shop"}
	_, err = f.svc.ScanTemplates(f.ctx, app.ID)
	require.NoError(t, err)
	before := f.storedEnv(app.ID)

	f.git.repoErr = apierr.Unprocessable("repo_not_found", "gone")
	f.repo.set(map[string]string{".env.example": "CHANGED=1\n"})
	f.svc.OnPush(ctx, gitproviders.PushEvent{ConnectionID: conn.ID, RepoFullName: "acme/shop", Branch: "main", ChangedPaths: []string{".env.example"}, PathsComplete: true})
	require.Equal(t, before, f.storedEnv(app.ID))
}
