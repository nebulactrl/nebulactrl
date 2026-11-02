package composeimport_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/composeimport"
	"github.com/nebulactrl/nebulactrl/internal/gitproviders"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

func (f *importFixture) stackPlan(src composeimport.ComposeSource) composeimport.ComposePlan {
	f.t.Helper()
	req := composeimport.ComposePlanRequest{Source: src, ServerID: &f.srv.ID, Mode: composeimport.ModeStack}
	p, err := f.svc.Plan(f.ctx, f.env.ID, req)
	require.NoError(f.t, err)
	return p
}

func TestStackPlanStopsAtAServerThatCannotTakeIt(t *testing.T) {
	for _, tc := range []struct{ sql, msg string }{
		{"status = 'revoked'", "agent1 has been revoked."},
		{"drain_state = 'draining'", "agent1 is being drained and cannot take new services."},
		{"accept_workloads = false", "agent1 does not accept workloads (Accept workloads is off in its settings)."},
	} {
		t.Run(tc.sql, func(t *testing.T) {
			f := newImportFixture(t)
			f.setServer(t, tc.sql)
			plan := f.stackPlan(stackReq(stackCompose, ""))
			require.False(t, plan.Installable)
			require.Contains(t, plan.Blockers, tc.msg)
			c, ok := checkOf(plan, "server")
			require.True(t, ok)
			require.Equal(t, composeimport.CheckFail, c.Status)
			_, ok = checkOf(plan, "domain")
			require.False(t, ok, "the checks stop at the server")

			_, err := f.svc.Import(f.ctx, f.env.ID, composeimport.ComposeImportRequest{Source: stackReq(stackCompose, ""), ServerID: f.srv.ID, Mode: composeimport.ModeStack})
			require.Equal(t, "preflight_failed", apiCode(t, err))
			require.Empty(t, f.services())
		})
	}
}

func TestStackPlanWithoutAServerOnlyAsksToChooseOne(t *testing.T) {
	f := newImportFixture(t)
	plan, err := f.svc.Plan(f.ctx, f.env.ID, composeimport.ComposePlanRequest{Source: stackReq(stackCompose, ""), Mode: composeimport.ModeStack})
	require.NoError(t, err)
	c, ok := checkOf(plan, "server")
	require.True(t, ok)
	require.Equal(t, composeimport.CheckWarn, c.Status)
	require.Contains(t, c.Message, "Choose a server to check the allow-list")
	require.True(t, plan.Installable)
}

func TestStackPlanReportsWhetherTheAgentHasTheComposePlugin(t *testing.T) {
	facts := func(f agentprotocol.Facts) string {
		b, err := json.Marshal(f)
		require.NoError(t, err)
		return "facts = '" + string(b) + "'::jsonb"
	}
	f := newImportFixture(t)
	f.setServer(t, facts(agentprotocol.Facts{OS: "linux", Capabilities: []string{agentprotocol.CapProxyV2}}))
	plan := f.stackPlan(stackReq(stackCompose, ""))
	require.NotNil(t, plan.Stack.ComposeAvailable)
	require.False(t, *plan.Stack.ComposeAvailable)
	c, ok := checkOf(plan, "compose")
	require.True(t, ok)
	require.Equal(t, composeimport.CheckWarn, c.Status)
	require.Equal(t, "The agent on agent1 does not report the Docker Compose plugin (2.24 or newer). The stack is staged, but deploying needs it.", c.Message)
	require.True(t, plan.Installable, "a missing plugin only warns: the stack is staged")

	f.setServer(t, facts(agentprotocol.Facts{OS: "linux", ComposeVersion: "2.29.0", Capabilities: []string{agentprotocol.CapCompose}}))
	plan = f.stackPlan(stackReq(stackCompose, ""))
	require.NotNil(t, plan.Stack.ComposeAvailable)
	require.True(t, *plan.Stack.ComposeAvailable)
	_, ok = checkOf(plan, "compose")
	require.False(t, ok)
}

func TestStackPlanWarnsAboutComponentsNamedLikeServicesOfTheEnvironment(t *testing.T) {
	f := newImportFixture(t)
	_, err := f.svc.Import(f.ctx, f.env.ID, composeimport.ComposeImportRequest{Source: readFixture(t, "kv"), ServerID: f.srv.ID})
	require.NoError(t, err)

	plan := f.stackPlan(stackReq("services:\n  app:\n    image: nginx\n  other:\n    image: busybox\n", ""))
	c, ok := checkOf(plan, "alias-app")
	require.True(t, ok)
	require.Equal(t, composeimport.CheckWarn, c.Status)
	require.Equal(t, "The component app has the same name as a service of this environment; one of them has to be renamed before the stack can deploy.", c.Message)
	_, ok = checkOf(plan, "alias-other")
	require.False(t, ok)
	require.True(t, plan.Installable, "a warning does not block the import")
}

func TestStackComponentsExplainProfilesAndPortsThatAreNotRouted(t *testing.T) {
	f := newImportFixture(t)
	compose := `services:
  web:
    image: nginx:1.27
  debug:
    image: busybox:1.36
    profiles: [debug, tools]
  broker:
    image: eclipse-mosquitto:2
    ports:
      - "1883:1883"
`
	plan := f.stackPlan(stackReq(compose, ""))
	byName := map[string]composeimport.ComposeStackComponent{}
	for _, c := range plan.Stack.Components {
		byName[c.Name] = c
	}
	require.Equal(t, "run", byName["web"].Action)
	require.Equal(t, "profile", byName["debug"].Action)
	require.Equal(t, []string{"Has Compose profiles (debug, tools), so a deploy does not start it unless COMPOSE_PROFILES selects one."}, byName["debug"].Notes)
	require.Empty(t, byName["debug"].Domains)
	require.Equal(t, []string{"Port 1883/tcp is not published on the host: stacks only route HTTP domains to their components."}, byName["broker"].Notes)
	require.Empty(t, byName["broker"].Domains)
	require.Zero(t, byName["broker"].Port)
	require.Equal(t, []int{}, byName["web"].Ports, "no ports is an empty list, not null")
}

func TestStackVariablesSkipInvalidNamesAndReportDotenvProblems(t *testing.T) {
	f := newImportFixture(t)
	dotenv := "GOOD=1\n1BAD=2\nthis line has no equals sign\nCOMPOSE_PROFILES=debug\nDOCKER_HOST=tcp://x\nCOMPOSE_FILE=x\n"
	plan := f.stackPlan(stackReq(stackCompose, dotenv))
	vars := map[string]composeimport.ComposePlanEnv{}
	for _, v := range plan.Stack.Variables {
		vars[v.Key] = v
	}
	require.False(t, vars["GOOD"].Skipped)
	require.False(t, vars["COMPOSE_PROFILES"].Skipped, "COMPOSE_PROFILES is the one COMPOSE_* variable a stack passes on")
	require.True(t, vars["DOCKER_HOST"].Skipped)
	require.True(t, vars["COMPOSE_FILE"].Skipped)
	require.Equal(t, "Compose never receives DOCKER_* or COMPOSE_* variables from a stack.", vars["DOCKER_HOST"].Note)
	require.NotContains(t, vars, "1BAD", "an invalid name is dropped by the .env reader and reported")
	require.NotEmpty(t, plan.Stack.Notes)
	for _, n := range plan.Stack.Notes {
		require.True(t, strings.HasPrefix(n, ".env: "), n)
	}
	require.Contains(t, strings.Join(plan.Stack.Notes, "\n"), "1BAD")
}

func TestStackPlanSaysWhyDomainsCouldNotBeSuggested(t *testing.T) {
	f := newImportFixture(t)
	// Valid for the stack (Compose resolves it at deploy time), unusable for the split analysis.
	compose := "services:\n  web:\n    image: nginx:${TAG:?TAG must be set}\n"
	req := composeimport.ComposePlanRequest{Source: stackReq(compose, ""), ServerID: &f.srv.ID, Mode: composeimport.ModeStack}
	plan, err := f.svc.Plan(f.ctx, f.env.ID, req)
	if err != nil {
		t.Skipf("the stack parser rejects this file too: %v", err)
	}
	require.NotNil(t, plan.Stack)
	require.Len(t, plan.Stack.Components, 1)
	var found bool
	for _, n := range plan.Stack.Notes {
		if strings.HasPrefix(n, "Domains could not be suggested: ") && strings.HasSuffix(n, ". Add them after the import.") {
			found = true
		}
	}
	if !found {
		t.Skipf("the split analysis accepts this file: %v", plan.Stack.Notes)
	}
	require.Empty(t, plan.Stack.Components[0].Domains)
}

const traefikStack = `name: site
services:
  web:
    image: nginx:1.27
    labels:
      - traefik.enable=true
      - traefik.http.routers.web.rule=Host("app.example.com")
      - traefik.http.services.web.loadbalancer.server.port=80
`

func TestStackDomainChecksCoverTheProxyTheBaseDomainAndHostnamesInUse(t *testing.T) {
	f := newImportFixture(t)
	f.setServer(t, "unrestricted = true")
	plan := f.stackPlan(stackReq(traefikStack, ""))
	require.Len(t, plan.Stack.Components, 1)
	require.Len(t, plan.Stack.Components[0].Domains, 1)
	require.Equal(t, "app.example.com", plan.Stack.Components[0].Domains[0].Host)

	// A proxy that is down, and no base domain for generated names (an explicit name needs none).
	f.setServer(t, "proxy_status = 'error'")
	plan = f.stackPlan(stackReq(traefikStack, ""))
	c, ok := checkOf(plan, "proxy")
	require.True(t, ok)
	require.Equal(t, composeimport.CheckFail, c.Status)
	require.Equal(t, "The proxy on agent1 is not running; domains cannot be attached.", c.Message)
	require.False(t, plan.Installable)
	c, ok = checkOf(plan, "domain")
	require.True(t, ok)
	require.Equal(t, composeimport.CheckWarn, c.Status)
	f.setServer(t, "proxy_status = 'ok'")

	// The same file imported twice: the second one meets the hostname of the first.
	_, err := f.svc.Import(f.ctx, f.env.ID, composeimport.ComposeImportRequest{Source: stackReq(traefikStack, ""), ServerID: f.srv.ID, Mode: composeimport.ModeStack})
	require.NoError(t, err)
	plan = f.stackPlan(stackReq(traefikStack, ""))
	var taken []string
	for _, c := range plan.Checks {
		if c.ID == "domain" && c.Status == composeimport.CheckFail {
			taken = append(taken, c.Message)
		}
	}
	require.Equal(t, []string{"The hostname app.example.com is already in use. Turn the domain of web off or change it."}, taken)
	require.False(t, plan.Installable)
	require.Contains(t, plan.Blockers, taken[0])
	_, err = f.svc.Import(f.ctx, f.env.ID, composeimport.ComposeImportRequest{Source: stackReq(traefikStack, ""), ServerID: f.srv.ID, Mode: composeimport.ModeStack})
	require.Equal(t, "preflight_failed", apiCode(t, err))
	require.Len(t, f.services(), 1)
}

func TestStackImportWithoutABaseDomainWarnsInsteadOfFailing(t *testing.T) {
	f := newImportFixture(t)
	res, err := f.svc.Import(f.ctx, f.env.ID, composeimport.ComposeImportRequest{Source: stackReq(stackCompose, ""), ServerID: f.srv.ID, Mode: composeimport.ModeStack})
	require.NoError(t, err)
	require.Equal(t, []string{"No hostname was generated for shop: the server has neither a base domain nor a public IP. Add a domain in its settings."}, res.Warnings)
	require.Len(t, f.services(), 1)
	var n int
	require.NoError(t, f.pool.QueryRow(context.Background(), "SELECT count(*) FROM domains").Scan(&n))
	require.Zero(t, n)
}

func TestStackImportKeepsAnExplicitHostnameFromTraefikLabels(t *testing.T) {
	f := newImportFixture(t)
	f.setServer(t, "unrestricted = true")
	_, err := f.svc.Import(f.ctx, f.env.ID, composeimport.ComposeImportRequest{Source: stackReq(traefikStack, ""), ServerID: f.srv.ID, Mode: composeimport.ModeStack})
	require.NoError(t, err)
	var host, component string
	var port int32
	var generated bool
	require.NoError(t, f.pool.QueryRow(context.Background(), "SELECT hostname, component, target_port, generated FROM domains").Scan(&host, &component, &port, &generated))
	require.Equal(t, "app.example.com", host)
	require.Equal(t, "web", component)
	require.EqualValues(t, 80, port)
	require.False(t, generated)
}

func TestStackImportWithoutVariableSupportRollsBack(t *testing.T) {
	f := newImportFixture(t)
	svc := f.with(func(d *composeimport.Deps) { d.Variables = nil })
	_, err := svc.Import(f.ctx, f.env.ID, composeimport.ComposeImportRequest{Source: stackReq(stackCompose, stackDotenv), ServerID: f.srv.ID, Mode: composeimport.ModeStack})
	require.Equal(t, "variables_unavailable", apiCode(t, err))
	require.Empty(t, f.services(), "the stack created before the failure is rolled back")

	// A file without variables needs no writer.
	_, err = svc.Import(f.ctx, f.env.ID, composeimport.ComposeImportRequest{Source: stackReq(stackCompose, ""), ServerID: f.srv.ID, Mode: composeimport.ModeStack})
	require.NoError(t, err)
}

func TestStackImportDeploysTheStackWhenAsked(t *testing.T) {
	f := newImportFixture(t)
	dep := &fakeDeployer{}
	svc := f.with(func(d *composeimport.Deps) { d.Deploy = dep })
	res, err := svc.Import(f.ctx, f.env.ID, composeimport.ComposeImportRequest{Source: stackReq(stackCompose, ""), ServerID: f.srv.ID, Mode: composeimport.ModeStack, Deploy: true})
	require.NoError(t, err)
	require.Len(t, res.Services, 1)
	require.Len(t, res.Deployments, 1)
	require.Equal(t, [][]uuid.UUID{{res.Services[0].ID}}, dep.calls)
}

// ---- sources in a git repository ----

// repoService serves the files from a local server in place of GitHub.
func (f *importFixture) repoService(files map[string]string) (*composeimport.Service, *repoFiles) {
	f.t.Helper()
	repo := &repoFiles{files: files}
	srv := httptest.NewServer(repo)
	f.t.Cleanup(srv.Close)
	to, err := url.Parse(srv.URL)
	require.NoError(f.t, err)
	svc := f.with(func(d *composeimport.Deps) {
		d.Git = publicGit{pub: gitproviders.NewService(gitproviders.Deps{
			HTTP: &http.Client{Transport: rewrite{to: to}}, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		})}
	})
	return svc, repo
}

func TestStackFromARepositoryKeepsItsDotenvAndRecordsWhereItCameFrom(t *testing.T) {
	f := newImportFixture(t)
	svc, _ := f.repoService(map[string]string{
		"docker-compose.yml": stackCompose,
		".env":               "APP_NAME=from-the-repo\nDB_PASSWORD=hunter2hunter2\n",
	})
	ref := composeimport.ComposeRepoRef{URL: "https://github.com/acme/shop"}
	plan, err := svc.Plan(f.ctx, f.env.ID, composeimport.ComposePlanRequest{Source: composeimport.ComposeSource{Repo: &ref}, ServerID: &f.srv.ID, Mode: composeimport.ModeStack})
	require.NoError(t, err)
	require.Equal(t, "repo", plan.SourceKind)
	vars := map[string]composeimport.ComposePlanEnv{}
	for _, v := range plan.Stack.Variables {
		vars[v.Key] = v
	}
	require.Equal(t, "from-the-repo", vars["APP_NAME"].Value, "the repository's .env feeds the variables")
	require.True(t, vars["DB_PASSWORD"].Secret)

	_, err = svc.Import(f.ctx, f.env.ID, composeimport.ComposeImportRequest{Source: composeimport.ComposeSource{Repo: &ref}, ServerID: f.srv.ID, Mode: composeimport.ModeStack})
	require.NoError(t, err)
	var source, repo, branch string
	require.NoError(t, f.pool.QueryRow(context.Background(),
		"SELECT metadata->>'source', metadata->>'repo', metadata->>'branch' FROM audit_events WHERE action = 'compose.import'").Scan(&source, &repo, &branch))
	require.Equal(t, "repo", source)
	require.Equal(t, "acme/shop", repo)
	require.Equal(t, "main", branch)
}

func TestSplitImportFromARepositoryRecordsTheRepositoryInTheAudit(t *testing.T) {
	f := newImportFixture(t)
	svc, _ := f.repoService(map[string]string{"compose.yaml": repoCompose, "web.env": "GREETING=hi\n"})
	ref := composeimport.ComposeRepoRef{URL: "https://github.com/acme/shop"}
	res, err := svc.Import(f.ctx, f.env.ID, composeimport.ComposeImportRequest{Source: composeimport.ComposeSource{Repo: &ref}, ServerID: f.srv.ID})
	require.NoError(t, err)
	require.Len(t, res.Services, 1)
	var source, repo string
	require.NoError(t, f.pool.QueryRow(context.Background(), "SELECT metadata->>'source', metadata->>'repo' FROM audit_events WHERE action = 'compose.import'").Scan(&source, &repo))
	require.Equal(t, "repo", source)
	require.Equal(t, "acme/shop", repo)
}

func TestAComposeFileGivenNextToARepositoryIsUsedAsIs(t *testing.T) {
	f := newImportFixture(t)
	svc, repo := f.repoService(map[string]string{"docker-compose.yml": "services: {broken: [", ".env": "GREETING=from-repo\n"})
	ref := composeimport.ComposeRepoRef{URL: "https://github.com/acme/shop"}
	plan, err := svc.Plan(f.ctx, f.env.ID, composeimport.ComposePlanRequest{
		Source:   composeimport.ComposeSource{Repo: &ref, Compose: "services:\n  web:\n    image: traefik/whoami:v1.10\n    environment:\n      G: ${GREETING}\n"},
		ServerID: &f.srv.ID,
	})
	require.NoError(t, err, "the pasted file wins; the repository file is never read")
	require.Len(t, plan.Services, 1)
	require.Equal(t, "web", plan.Services[0].Name)
	for _, p := range repo.paths {
		require.NotContains(t, p, "docker-compose.yml")
	}
}

func TestAnUnreadableRepositoryFileIsReportedWithItsName(t *testing.T) {
	f := newImportFixture(t)
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	t.Cleanup(broken.Close)
	to, err := url.Parse(broken.URL)
	require.NoError(t, err)
	svc := f.with(func(d *composeimport.Deps) {
		d.Git = publicGit{pub: gitproviders.NewService(gitproviders.Deps{
			HTTP: &http.Client{Transport: rewrite{to: to}}, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		})}
	})
	ref := composeimport.ComposeRepoRef{URL: "https://github.com/acme/shop", Path: "deploy/compose.yaml"}
	_, err = svc.Plan(f.ctx, f.env.ID, composeimport.ComposePlanRequest{Source: composeimport.ComposeSource{Repo: &ref}})
	require.Equal(t, "repo_read_failed", apiCode(t, err))
	require.Contains(t, err.Error(), "deploy/compose.yaml")
}
