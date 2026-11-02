package composeimport_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/composeimport"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/deployments"
	"github.com/nebulactrl/nebulactrl/internal/domains"
	"github.com/nebulactrl/nebulactrl/internal/gitproviders"
	"github.com/nebulactrl/nebulactrl/internal/networks"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/prodguard"
	"github.com/nebulactrl/nebulactrl/internal/prodguard/prodguardtest"
	"github.com/nebulactrl/nebulactrl/internal/scaling"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/internal/templates"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// ---- fakes of the collaborators that act after the commit ----

type linkRec struct {
	service uuid.UUID
	in      gitproviders.LinkInput
}

type fakeGitSource struct {
	mu      sync.Mutex
	links   []linkRec
	linkErr error
}

func (g *fakeGitSource) OpenRepo(context.Context, uuid.UUID, string, string) (*gitproviders.RepoReader, error) {
	return nil, errors.New("no reader in this fake")
}

func (g *fakeGitSource) OpenPublicRepo(context.Context, string, string) (*gitproviders.RepoReader, error) {
	return nil, errors.New("no reader in this fake")
}

func (g *fakeGitSource) CloneURLFor(_ context.Context, _ uuid.UUID, repo string) (string, string, error) {
	return "https://github.com/" + repo + ".git", "trunk", nil
}

func (g *fakeGitSource) Link(_ context.Context, id uuid.UUID, in gitproviders.LinkInput) (gitproviders.GitLinkView, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.links = append(g.links, linkRec{id, in})
	return gitproviders.GitLinkView{}, g.linkErr
}

type setRec struct {
	service  uuid.UUID
	replicas int
}

type fakeScaler struct {
	calls []setRec
	err   error
}

func (s *fakeScaler) Set(_ context.Context, id uuid.UUID, in scaling.SetInput) (services.ServiceView, error) {
	n := 0
	if in.Replicas != nil {
		n = *in.Replicas
	}
	s.calls = append(s.calls, setRec{id, n})
	return services.ServiceView{}, s.err
}

type attachRec struct{ service, network uuid.UUID }

type fakeNetworks struct {
	nets    []networks.NetworkView
	attach  []attachRec
	err     error
	listErr error
}

func (n *fakeNetworks) List(context.Context, *uuid.UUID) ([]networks.NetworkView, error) {
	return n.nets, n.listErr
}

func (n *fakeNetworks) Attach(_ context.Context, service, network uuid.UUID) (services.NetworkRef, error) {
	n.attach = append(n.attach, attachRec{service, network})
	return services.NetworkRef{}, n.err
}

type fakeDeployer struct {
	err   error
	calls [][]uuid.UUID
}

func (d *fakeDeployer) DeployStaged(_ context.Context, _ uuid.UUID, ids []uuid.UUID, _ string) (deployments.StagedResult, error) {
	d.calls = append(d.calls, ids)
	if d.err != nil {
		return deployments.StagedResult{}, d.err
	}
	var out deployments.StagedResult
	for _, id := range ids {
		out.Items = append(out.Items, deployments.DeploymentView{ID: uuid.New(), ServiceID: id})
	}
	return out, nil
}

func (f *importFixture) with(mod func(*composeimport.Deps)) *composeimport.Service {
	mod(&f.deps)
	return composeimport.NewService(f.deps)
}

func checkOf(plan composeimport.ComposePlan, id string) (composeimport.ComposeCheck, bool) {
	for _, c := range plan.Checks {
		if c.ID == id {
			return c, true
		}
	}
	return composeimport.ComposeCheck{}, false
}

func domainChoice(d composeimport.ComposeDomainChoice) composeimport.ComposeChoices {
	return composeimport.ComposeChoices{Services: map[string]composeimport.ComposeServiceChoice{"app": {Domain: &d}}}
}

// ---- the server the services would run on ----

func TestPlanChecksTheChosenServerFirst(t *testing.T) {
	cases := []struct {
		name, sql, status, msg string
		installable            bool
	}{
		{"online", "", composeimport.CheckOK, "agent1 is online.", true},
		{"offline", "status = 'offline'", composeimport.CheckWarn, "agent1 is offline: the services are staged, but deploying waits until the agent connects.", true},
		{"revoked", "status = 'revoked'", composeimport.CheckFail, "agent1 has been revoked.", false},
		{"draining", "drain_state = 'draining'", composeimport.CheckFail, "agent1 is being drained and cannot take new services.", false},
		{"drained", "drain_state = 'drained'", composeimport.CheckFail, "agent1 is being drained and cannot take new services.", false},
		{"no workloads", "accept_workloads = false", composeimport.CheckFail, "agent1 does not accept workloads (Accept workloads is off in its settings).", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newImportFixture(t)
			if tc.sql != "" {
				f.setServer(t, tc.sql)
			}
			src := readFixture(t, "kv")
			plan, err := f.svc.Plan(f.ctx, f.env.ID, composeimport.ComposePlanRequest{Source: src, ServerID: &f.srv.ID})
			require.NoError(t, err)
			c, ok := checkOf(plan, "server")
			require.True(t, ok)
			require.Equal(t, tc.status, c.Status)
			require.Equal(t, tc.msg, c.Message)
			require.Equal(t, tc.installable, plan.Installable)
			if !tc.installable {
				require.Contains(t, plan.Blockers, tc.msg)
				_, err := f.svc.Import(f.ctx, f.env.ID, composeimport.ComposeImportRequest{Source: src, ServerID: f.srv.ID})
				require.Equal(t, "preflight_failed", apiCode(t, err))
				require.Empty(t, f.services(), "nothing is staged on a server that cannot take it")
			}
		})
	}
}

func TestPlanWithoutAServerOnlyAsksToChooseOne(t *testing.T) {
	f := newImportFixture(t)
	plan, err := f.svc.Plan(f.ctx, f.env.ID, composeimport.ComposePlanRequest{Source: readFixture(t, "kv")})
	require.NoError(t, err)
	c, ok := checkOf(plan, "server")
	require.True(t, ok)
	require.Equal(t, composeimport.CheckWarn, c.Status)
	require.Equal(t, "Choose a server to check capacity, domains and published ports.", c.Message)
	require.True(t, plan.Installable)
}

func TestPlanForAServerOfAnotherTeamIsNotFound(t *testing.T) {
	f := newImportFixture(t)
	other, err := f.q.CreateWorkspace(context.Background(), "other")
	require.NoError(t, err)
	srv, err := f.q.CreateServer(context.Background(), db.CreateServerParams{WorkspaceID: other.ID, Name: "theirs"})
	require.NoError(t, err)
	_, err = f.svc.Plan(f.ctx, f.env.ID, composeimport.ComposePlanRequest{Source: readFixture(t, "kv"), ServerID: &srv.ID})
	require.Equal(t, "server_not_found", apiCode(t, err))
	_, err = f.svc.Import(f.ctx, f.env.ID, composeimport.ComposeImportRequest{Source: readFixture(t, "kv"), ServerID: uuid.New()})
	require.Equal(t, "server_not_found", apiCode(t, err))
}

// ---- domains ----

func TestPlanChecksDomainsAgainstTheProxyAndTheHostnamesInUse(t *testing.T) {
	f := newImportFixture(t)
	src := readFixture(t, "kv")
	plan := func(c composeimport.ComposeChoices) composeimport.ComposePlan {
		p, err := f.svc.Plan(f.ctx, f.env.ID, composeimport.ComposePlanRequest{Source: src, ServerID: &f.srv.ID, Choices: c})
		require.NoError(t, err)
		return p
	}

	// A generated hostname needs a base domain or a public IP: the server has neither.
	p := plan(domainChoice(composeimport.ComposeDomainChoice{Enabled: true}))
	c, ok := checkOf(p, "domain")
	require.True(t, ok)
	require.Equal(t, composeimport.CheckWarn, c.Status)
	require.Equal(t, "The server has neither a base domain nor a public IP, so generated hostnames are skipped. Add domains after the import.", c.Message)
	require.True(t, p.Installable, "a missing base domain only warns")

	// A hostname that is not valid.
	p = plan(domainChoice(composeimport.ComposeDomainChoice{Enabled: true, Host: "not a host!"}))
	c, ok = checkOf(p, "domain")
	require.True(t, ok)
	require.Equal(t, composeimport.CheckFail, c.Status)
	require.True(t, strings.HasPrefix(c.Message, "app: "), c.Message)
	require.False(t, p.Installable)
	_, err := f.svc.Import(f.ctx, f.env.ID, composeimport.ComposeImportRequest{Source: src, ServerID: f.srv.ID, Choices: domainChoice(composeimport.ComposeDomainChoice{Enabled: true, Host: "not a host!"})})
	require.Equal(t, "preflight_failed", apiCode(t, err))

	// The proxy of the server is down.
	f.setServer(t, "proxy_status = 'error'")
	named := domainChoice(composeimport.ComposeDomainChoice{Enabled: true, Host: "app.example.com"})
	p = plan(named)
	c, ok = checkOf(p, "proxy")
	require.True(t, ok)
	require.Equal(t, composeimport.CheckFail, c.Status)
	require.Equal(t, "The proxy on agent1 is not running; domains cannot be attached.", c.Message)
	require.False(t, p.Installable)
	_, err = f.svc.Import(f.ctx, f.env.ID, composeimport.ComposeImportRequest{Source: src, ServerID: f.srv.ID, Choices: named})
	require.Equal(t, "preflight_failed", apiCode(t, err))
	require.Empty(t, f.services())

	// A hostname that an earlier import already holds.
	f.setServer(t, "proxy_status = 'ok'")
	_, err = f.svc.Import(f.ctx, f.env.ID, composeimport.ComposeImportRequest{Source: src, ServerID: f.srv.ID, Choices: named})
	require.NoError(t, err)
	p = plan(named)
	c, ok = checkOf(p, "domain")
	require.True(t, ok)
	require.Equal(t, composeimport.CheckFail, c.Status)
	require.Equal(t, "The hostname app.example.com is already in use. Turn the domain of app off or change it.", c.Message)
	require.False(t, p.Installable)
	_, err = f.svc.Import(f.ctx, f.env.ID, composeimport.ComposeImportRequest{Source: src, ServerID: f.srv.ID, Choices: named})
	require.Equal(t, "preflight_failed", apiCode(t, err))
	require.Len(t, f.services(), 2, "the refused import added nothing")
}

func (f *importFixture) domainsOf(serviceID uuid.UUID) []db.Domain {
	f.t.Helper()
	d, err := f.q.ListServiceDomains(context.Background(), serviceID)
	require.NoError(f.t, err)
	return d
}

func TestImportAttachesTheChosenHostnameToTheServicePort(t *testing.T) {
	f := newImportFixture(t)
	src := readFixture(t, "kv")
	res, err := f.svc.Import(f.ctx, f.env.ID, composeimport.ComposeImportRequest{
		Source: src, ServerID: f.srv.ID, Choices: domainChoice(composeimport.ComposeDomainChoice{Enabled: true, Host: "App.Example.COM"}),
	})
	require.NoError(t, err)
	require.Empty(t, res.Warnings)
	app := f.services()["app"]
	doms := f.domainsOf(app.ID)
	require.Len(t, doms, 1)
	require.Equal(t, "app.example.com", doms[0].Hostname, "hostnames are stored lowercase")
	require.EqualValues(t, 8080, doms[0].TargetPort)
	require.False(t, doms[0].Generated)
	require.Equal(t, domains.DNSGuidance("app.example.com", nil), doms[0].StatusMessage)
	require.Equal(t, 1, f.countRows("SELECT count(*) FROM river_job WHERE kind = 'domain_dns_check' AND args->>'domainId' = $1", doms[0].ID.String()),
		"the DNS check of the new domain is queued")
	require.Empty(t, f.domainsOf(f.services()["valkey"].ID), "a database gets no domain")

	var meta []byte
	require.NoError(t, f.pool.QueryRow(context.Background(), "SELECT metadata FROM audit_events WHERE action = 'compose.import'").Scan(&meta))
	var m struct {
		Hostnames []string `json:"hostnames"`
		Mode      string   `json:"mode"`
	}
	require.NoError(t, json.Unmarshal(meta, &m))
	require.Equal(t, []string{"app.example.com"}, m.Hostnames)
	require.Equal(t, "split", m.Mode)
}

func (f *importFixture) countRows(query string, args ...any) (n int) {
	f.t.Helper()
	require.NoError(f.t, f.pool.QueryRow(context.Background(), query, args...).Scan(&n))
	return n
}

func (f *importFixture) holdHosts(hosts ...string) {
	f.t.Helper()
	bg := context.Background()
	p, err := f.q.CreateProject(bg, db.CreateProjectParams{WorkspaceID: policy.WorkspaceID(f.ctx), Name: "Other", Slug: "other"})
	require.NoError(f.t, err)
	e, err := f.q.CreateEnvironment(bg, db.CreateEnvironmentParams{ProjectID: p.ID, Name: "dev", Slug: "dev"})
	require.NoError(f.t, err)
	s, err := f.q.InsertService(bg, db.InsertServiceParams{ID: uuid.New(), EnvironmentID: e.ID, Name: "holder", Slug: "holder", Kind: "app", SourceType: "image",
		Config: []byte(`{"image":"x/y:1","stopGraceSeconds":30,"imageRetention":5,"healthcheck":{"type":"none"}}`)})
	require.NoError(f.t, err)
	for _, h := range hosts {
		_, err := f.pool.Exec(bg, "INSERT INTO domains (service_id, hostname, target_port) VALUES ($1, $2, 80)", s.ID, h)
		require.NoError(f.t, err)
	}
}

func TestGeneratedHostnameIsServiceEnvironmentProjectUnderTheBaseDomain(t *testing.T) {
	f := newImportFixture(t)
	f.setServer(t, "base_domain = 'example.test'")
	choice := domainChoice(composeimport.ComposeDomainChoice{Enabled: true})
	res, err := f.svc.Import(f.ctx, f.env.ID, composeimport.ComposeImportRequest{Source: readFixture(t, "kv"), ServerID: f.srv.ID, Choices: choice})
	require.NoError(t, err)
	require.Empty(t, res.Warnings)
	doms := f.domainsOf(f.services()["app"].ID)
	require.Len(t, doms, 1)
	require.Equal(t, "app-production-shop.example.test", doms[0].Hostname)
	require.True(t, doms[0].Generated)
	require.EqualValues(t, 8080, doms[0].TargetPort)
}

func TestGeneratedHostnameTakesTheNextFreeSuffix(t *testing.T) {
	f := newImportFixture(t)
	f.setServer(t, "base_domain = 'example.test'")
	f.holdHosts("app-production-shop.example.test", "app-production-shop-2.example.test")
	choice := domainChoice(composeimport.ComposeDomainChoice{Enabled: true})
	_, err := f.svc.Import(f.ctx, f.env.ID, composeimport.ComposeImportRequest{Source: readFixture(t, "kv"), ServerID: f.srv.ID, Choices: choice})
	require.NoError(t, err)
	doms := f.domainsOf(f.services()["app"].ID)
	require.Len(t, doms, 1)
	require.Equal(t, "app-production-shop-3.example.test", doms[0].Hostname)
}

func TestImportFailsAsAWholeWhenNoGeneratedHostnameIsFree(t *testing.T) {
	f := newImportFixture(t)
	f.setServer(t, "base_domain = 'example.test'")
	hosts := []string{"app-production-shop.example.test"}
	for i := 2; i <= 9; i++ {
		hosts = append(hosts, fmt.Sprintf("app-production-shop-%d.example.test", i))
	}
	f.holdHosts(hosts...)
	choice := domainChoice(composeimport.ComposeDomainChoice{Enabled: true})
	_, err := f.svc.Import(f.ctx, f.env.ID, composeimport.ComposeImportRequest{Source: readFixture(t, "kv"), ServerID: f.srv.ID, Choices: choice})
	require.Equal(t, "domain_conflict", apiCode(t, err))
	e, _ := apierr.As(err)
	require.Equal(t, 409, e.Status)
	require.Contains(t, e.Message, "app")
	require.Empty(t, f.services(), "the services created before the failure are rolled back")
}

func TestNoHostnameIsGeneratedWithoutABaseDomainOrPublicIP(t *testing.T) {
	f := newImportFixture(t)
	choice := domainChoice(composeimport.ComposeDomainChoice{Enabled: true})
	res, err := f.svc.Import(f.ctx, f.env.ID, composeimport.ComposeImportRequest{Source: readFixture(t, "kv"), ServerID: f.srv.ID, Choices: choice})
	require.NoError(t, err)
	require.Equal(t, []string{"No hostname was generated for app: the server has neither a base domain nor a public IP. Add a domain in its settings."}, res.Warnings)
	require.Len(t, f.services(), 2)
	require.Empty(t, f.domainsOf(f.services()["app"].ID))
}

func TestGeneratedHostnameUsesThePublicIPWhenThereIsNoBaseDomain(t *testing.T) {
	f := newImportFixture(t)
	f.setServer(t, "public_ip = '203.0.113.9'")
	choice := domainChoice(composeimport.ComposeDomainChoice{Enabled: true})
	_, err := f.svc.Import(f.ctx, f.env.ID, composeimport.ComposeImportRequest{Source: readFixture(t, "kv"), ServerID: f.srv.ID, Choices: choice})
	require.NoError(t, err)
	doms := f.domainsOf(f.services()["app"].ID)
	require.Len(t, doms, 1)
	require.Equal(t, "app-production-shop.203.0.113.9.sslip.io", doms[0].Hostname)
	require.Equal(t, "Point an A record for app-production-shop.203.0.113.9.sslip.io to 203.0.113.9", doms[0].StatusMessage)
}

// ---- what happens after the commit ----

const gitReplicasCompose = `services:
  web:
    build: .
    ports:
      - "8080:80"
    deploy:
      replicas: 3
    networks: [shared]
  worker:
    image: busybox:1.36
    command: ["sleep", "3600"]
networks:
  shared:
    external: true
    name: team-net
`

func gitChoices(conn uuid.UUID) composeimport.ComposeChoices {
	return composeimport.ComposeChoices{Services: map[string]composeimport.ComposeServiceChoice{
		"web": {Source: "git", ConnectionID: &conn, Repo: "acme/shop"},
	}}
}

func TestImportLinksReplicatesAndAttachesAfterTheCommit(t *testing.T) {
	f := newImportFixture(t)
	f.setServer(t, "base_domain = 'example.test'")
	netID, conn := uuid.New(), uuid.New()
	git, scaler := &fakeGitSource{}, &fakeScaler{}
	nets := &fakeNetworks{nets: []networks.NetworkView{{ID: netID, Name: "team-net"}}}
	svc := f.with(func(d *composeimport.Deps) { d.Git, d.Scaling, d.Networks = git, scaler, nets })
	src := composeimport.ComposeSource{Compose: gitReplicasCompose}

	plan, err := svc.Plan(f.ctx, f.env.ID, composeimport.ComposePlanRequest{Source: src, ServerID: &f.srv.ID, Choices: gitChoices(conn)})
	require.NoError(t, err)
	require.True(t, plan.Installable, "%v", plan.Blockers)
	require.Len(t, plan.Networks, 1)
	require.NotNil(t, plan.Networks[0].SharedNetworkID)
	require.Equal(t, netID, *plan.Networks[0].SharedNetworkID)

	res, err := svc.Import(f.ctx, f.env.ID, composeimport.ComposeImportRequest{Source: src, ServerID: f.srv.ID, Choices: gitChoices(conn)})
	require.NoError(t, err)
	require.Empty(t, res.Warnings)
	got := f.services()
	web := got["web"]
	cfg := services.DecodeConfig(web.Config)
	require.Equal(t, services.SourceGit, web.SourceType)
	require.Equal(t, "https://github.com/acme/shop.git", cfg.GitURL)
	require.Equal(t, "trunk", cfg.GitBranch)
	require.Equal(t, agentprotocol.BuildDockerfile, cfg.BuildType)

	require.Len(t, git.links, 1, "only the service built from git is linked")
	require.Equal(t, web.ID, git.links[0].service)
	require.Equal(t, conn, git.links[0].in.ConnectionID)
	require.Equal(t, "acme/shop", git.links[0].in.Repo)
	require.Equal(t, "trunk", git.links[0].in.Branch)
	require.NotNil(t, git.links[0].in.AutoDeploy)
	require.False(t, *git.links[0].in.AutoDeploy, "a link made by an import never deploys by itself")
	require.Equal(t, []setRec{{web.ID, 3}}, scaler.calls)
	require.Equal(t, []attachRec{{web.ID, netID}}, nets.attach)
}

func TestFailuresAfterTheCommitAreWarningsNotErrors(t *testing.T) {
	f := newImportFixture(t)
	f.setServer(t, "base_domain = 'example.test'")
	netID, conn := uuid.New(), uuid.New()
	git, scaler := &fakeGitSource{linkErr: errors.New("repository not found")}, &fakeScaler{err: errors.New("no capacity")}
	nets := &fakeNetworks{nets: []networks.NetworkView{{ID: netID, Name: "team-net"}}, err: errors.New("network gone")}
	svc := f.with(func(d *composeimport.Deps) { d.Git, d.Scaling, d.Networks = git, scaler, nets })

	res, err := svc.Import(f.ctx, f.env.ID, composeimport.ComposeImportRequest{
		Source: composeimport.ComposeSource{Compose: gitReplicasCompose}, ServerID: f.srv.ID, Choices: gitChoices(conn),
	})
	require.NoError(t, err, "the services are committed; the follow-ups only warn")
	require.Len(t, f.services(), 2)
	require.Equal(t, []string{
		"Could not link web to acme/shop: repository not found. Link it under Settings → Source so builds can clone it.",
		"Could not set 3 replicas on web: no capacity",
		"Could not attach web to the shared network: network gone",
	}, res.Warnings)
}

func TestReplicasAndNetworksAreLeftAloneWithoutTheServicesThatApplyThem(t *testing.T) {
	f := newImportFixture(t) // no scaler, no networks, no git
	f.setServer(t, "base_domain = 'example.test'")
	compose := "services:\n  web:\n    image: traefik/whoami:v1.10\n    ports: ['8080:80']\n    deploy:\n      replicas: 2\n"
	res, err := f.svc.Import(f.ctx, f.env.ID, composeimport.ComposeImportRequest{Source: composeimport.ComposeSource{Compose: compose}, ServerID: f.srv.ID})
	require.NoError(t, err)
	require.Empty(t, res.Warnings)
	require.Len(t, f.services(), 1)
}

func TestSharedNetworksAreOnlyMatchedWhenTheTeamHasThem(t *testing.T) {
	f := newImportFixture(t)
	nets := &fakeNetworks{listErr: errors.New("networks unavailable")}
	svc := f.with(func(d *composeimport.Deps) { d.Networks = nets })
	plan, err := svc.Plan(f.ctx, f.env.ID, composeimport.ComposePlanRequest{
		Source: composeimport.ComposeSource{Compose: gitReplicasCompose}, ServerID: &f.srv.ID,
		Choices: composeimport.ComposeChoices{Services: map[string]composeimport.ComposeServiceChoice{"web": {Source: "image", Image: "traefik/whoami:v1.10"}}},
	})
	require.NoError(t, err)
	require.Len(t, plan.Networks, 1)
	require.Nil(t, plan.Networks[0].SharedNetworkID, "a failing list is not fatal: nothing is attached")
	require.Contains(t, plan.Networks[0].Note, "No shared network named team-net exists")
}

// ---- deploying right away ----

func (f *importFixture) token(scopes []string, guarded bool) context.Context {
	p, _ := auth.PrincipalFrom(f.ctx)
	id := uuid.New()
	return auth.WithPrincipal(context.Background(), &auth.Principal{
		Type: auth.TypeToken, UserID: p.UserID, TokenID: &id, WorkspaceID: p.WorkspaceID, Role: auth.RoleOwner, Label: "ci",
		Scopes: scopes, RequireProductionApproval: guarded,
	})
}

func TestImportDeploysTheStagedServicesWhenAsked(t *testing.T) {
	f := newImportFixture(t)
	dep := &fakeDeployer{}
	svc := f.with(func(d *composeimport.Deps) { d.Deploy = dep })
	res, err := svc.Import(f.ctx, f.env.ID, composeimport.ComposeImportRequest{Source: readFixture(t, "kv"), ServerID: f.srv.ID, Deploy: true})
	require.NoError(t, err)
	require.Len(t, dep.calls, 1)
	require.Len(t, dep.calls[0], 2)
	require.Len(t, res.Deployments, 2)
	for _, d := range res.Deployments {
		require.Contains(t, dep.calls[0], d.ServiceID)
	}

	// A failing deploy leaves the services staged and says so.
	dep = &fakeDeployer{err: errors.New("no capacity")}
	svc = f.with(func(d *composeimport.Deps) { d.Deploy = dep })
	res, err = svc.Import(f.ctx, f.env.ID, composeimport.ComposeImportRequest{
		Source: composeimport.ComposeSource{Compose: "services:\n  solo:\n    image: busybox:1.36\n    command: ['sleep', '1']\n"}, ServerID: f.srv.ID, Deploy: true,
	})
	require.NoError(t, err)
	require.Empty(t, res.Deployments)
	require.Equal(t, []string{"The services were added but deploying failed: no capacity"}, res.Warnings)
}

func TestDeployingImportNeedsTheDeployScope(t *testing.T) {
	f := newImportFixture(t)
	tok := f.token([]string{policy.ScopeProjectsWrite}, false)
	_, err := f.svc.Import(tok, f.env.ID, composeimport.ComposeImportRequest{Source: readFixture(t, "kv"), ServerID: f.srv.ID, Deploy: true})
	e, ok := apierr.As(err)
	require.True(t, ok, "%v", err)
	require.Equal(t, 403, e.Status)
	require.Empty(t, f.services())
}

func TestDeployingIntoProductionNeedsApprovalForGuardedTokens(t *testing.T) {
	f := newImportFixture(t) // the fixture's environment is production
	dep := &fakeDeployer{}
	svc := f.with(func(d *composeimport.Deps) { d.Deploy = dep })
	tok := f.token(policy.AllScopes, true)
	req := composeimport.ComposeImportRequest{Source: readFixture(t, "kv"), ServerID: f.srv.ID, Deploy: true}

	// No guard on the control plane: refused.
	_, err := svc.Import(tok, f.env.ID, req)
	e, ok := apierr.As(err)
	require.True(t, ok, "%v", err)
	require.Equal(t, prodguard.CodeApprovalUnavailable, e.Code)
	require.Empty(t, f.services())

	g := prodguard.New(prodguard.Deps{Pool: f.pool, Box: f.box})
	g.Install()
	t.Cleanup(prodguardtest.Uninstall)

	_, err = svc.Import(tok, f.env.ID, req)
	ar, ok := prodguard.AsRequired(err)
	require.True(t, ok, "want approval_required, got %v", err)
	require.Contains(t, ar.Plan.Summary, "Import a Compose project into production and deploy it (production)")
	require.Equal(t, []string{"Creates 2 service(s): app, valkey.", "Starts them right away on the chosen server, databases first."}, ar.Plan.Effects)
	require.Empty(t, f.services(), "nothing is created before the approval")

	// Staging only is not guarded.
	staged := req
	staged.Deploy = false
	_, err = svc.Import(tok, f.env.ID, staged)
	require.NoError(t, err)
	require.Empty(t, dep.calls)
	require.Len(t, f.services(), 2)

	// A person approves a deploying import (of a different project: the approval is bound to the request).
	other := req
	other.Source = composeimport.ComposeSource{Compose: "services:\n  solo:\n    image: busybox:1.36\n    command: ['sleep', '1']\n"}
	_, err = svc.Import(tok, f.env.ID, other)
	ar2, ok := prodguard.AsRequired(err)
	require.True(t, ok, "got %v", err)
	_, err = g.Approve(f.person(), ar.ApprovalID)
	require.NoError(t, err)
	_, err = svc.Import(tok, f.env.ID, other)
	_, still := prodguard.AsRequired(err)
	require.True(t, still, "the approval for the first request does not cover the second")
	_, err = g.Approve(f.person(), ar2.ApprovalID)
	require.NoError(t, err)
	res, err := svc.Import(tok, f.env.ID, other)
	require.NoError(t, err)
	require.Len(t, res.Deployments, 1)
	require.Len(t, dep.calls, 1)
}

func (f *importFixture) person() context.Context {
	p, _ := auth.PrincipalFrom(f.ctx)
	return auth.WithPrincipal(context.Background(), &auth.Principal{
		Type: auth.TypeUser, UserID: p.UserID, WorkspaceID: p.WorkspaceID, Role: auth.RoleOwner, Label: "Ada", Scopes: policy.AllScopes,
	})
}

// ---- published ports ----

const mqttCompose = `services:
  broker:
    image: eclipse-mosquitto:2
    ports:
      - "1883:1883"
`

func publish(key string, ports ...string) composeimport.ComposeChoices {
	return composeimport.ComposeChoices{Services: map[string]composeimport.ComposeServiceChoice{key: {PublishPorts: ports}}}
}

func TestPublishedPortsNeedAnAgentThatCanRouteThem(t *testing.T) {
	f := newImportFixture(t) // no port router in this build
	plan, err := f.svc.Plan(f.ctx, f.env.ID, composeimport.ComposePlanRequest{
		Source: composeimport.ComposeSource{Compose: mqttCompose}, ServerID: &f.srv.ID, Choices: publish("broker", "1883/tcp"),
	})
	require.NoError(t, err)
	c, ok := checkOf(plan, "agent")
	require.True(t, ok)
	require.Equal(t, composeimport.CheckFail, c.Status)
	require.Equal(t, "The agent on agent1 cannot publish TCP/UDP ports; update the agent first or turn the ports off.", c.Message)
	require.False(t, plan.Installable)

	// An agent that announced its facts without the proxy v2 capability cannot either, router or not.
	svc := f.with(func(d *composeimport.Deps) { d.Ports = templates.PortsAdapter{} })
	f.setServer(t, `facts = '{"os":"linux"}'::jsonb`)
	plan, err = svc.Plan(f.ctx, f.env.ID, composeimport.ComposePlanRequest{
		Source: composeimport.ComposeSource{Compose: mqttCompose}, ServerID: &f.srv.ID, Choices: publish("broker", "1883/tcp"),
	})
	require.NoError(t, err)
	c, ok = checkOf(plan, "agent")
	require.True(t, ok)
	require.Equal(t, composeimport.CheckFail, c.Status)
}

func TestImportPublishesTheChosenPortsAndRefusesAPortThatIsTaken(t *testing.T) {
	f := newImportFixture(t)
	svc := f.with(func(d *composeimport.Deps) { d.Ports = templates.PortsAdapter{} })
	req := composeimport.ComposePlanRequest{Source: composeimport.ComposeSource{Compose: mqttCompose}, ServerID: &f.srv.ID, Choices: publish("broker", "1883/tcp")}

	plan, err := svc.Plan(f.ctx, f.env.ID, req)
	require.NoError(t, err)
	require.True(t, plan.Installable, "%v", plan.Blockers)
	c, ok := checkOf(plan, "port-1883")
	require.True(t, ok)
	require.Equal(t, composeimport.CheckOK, c.Status)
	require.Equal(t, "tcp/1883 (broker) will be published on agent1.", c.Message)
	c, ok = checkOf(plan, "proxy-restart")
	require.True(t, ok)
	require.Equal(t, composeimport.CheckWarn, c.Status)

	res, err := svc.Import(f.ctx, f.env.ID, composeimport.ComposeImportRequest{Source: req.Source, ServerID: f.srv.ID, Choices: req.Choices})
	require.NoError(t, err)
	require.Len(t, res.Services, 2, "the broker and the TCP route that publishes it")
	require.Equal(t, "broker", res.Services[0].Slug)
	require.Contains(t, res.Services[1].Slug, "1883")
	require.Len(t, f.services(), 2)

	// The port is now taken on that server: the next plan says so and the import is refused.
	plan, err = svc.Plan(f.ctx, f.env.ID, req)
	require.NoError(t, err)
	c, ok = checkOf(plan, "port-1883")
	require.True(t, ok)
	require.Equal(t, composeimport.CheckFail, c.Status)
	require.Equal(t, "tcp/1883 is already published by another route on agent1.", c.Message)
	require.False(t, plan.Installable)
	_, err = svc.Import(f.ctx, f.env.ID, composeimport.ComposeImportRequest{Source: req.Source, ServerID: f.srv.ID, Choices: req.Choices})
	require.Equal(t, "preflight_failed", apiCode(t, err))
	require.Len(t, f.services(), 2)
}
