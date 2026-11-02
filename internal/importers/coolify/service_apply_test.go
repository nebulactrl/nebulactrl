package coolify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/deployments"
	"github.com/nebulactrl/nebulactrl/internal/domains"
	"github.com/nebulactrl/nebulactrl/internal/gitproviders"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/prodguard"
	"github.com/nebulactrl/nebulactrl/internal/prodguard/prodguardtest"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

const (
	uuidGen    = "genapp00000000000000001a"
	uuidPort   = "portapp0000000000000002b"
	uuidQueue  = "queueapp000000000000003c"
	uuidDupA   = "dupapp000000000000000a1d"
	uuidDupB   = "dupapp000000000000000b2e"
	uuidLong   = "longapp00000000000000004f"
	longAppLen = 58
)

// extraApp is an image application of the "shop" project.
func extraApp(uuid, name, fqdn, port string) map[string]any {
	return map[string]any{
		"id": len(name), "uuid": uuid, "name": name, "environment_id": 2, "fqdn": fqdn, "build_pack": "dockerimage", "ports_exposes": port,
		"docker_registry_image_name": "docker.io/acme/" + strings.ToLower(name[:1]), "docker_registry_image_tag": "1.0",
		"health_check_enabled": false, "health_check_path": "/", "health_check_port": nil, "health_check_return_code": 200, "health_check_interval": 5,
		"health_check_timeout": 5, "health_check_retries": 10, "health_check_start_period": 5, "health_check_type": "http",
		"limits_memory": "0", "limits_memory_swap": "0", "limits_memory_reservation": "0", "limits_cpus": "0", "limits_cpuset": nil, "limits_cpu_shares": 1024,
		"base_directory": "/", "dockerfile_location": "/Dockerfile", "custom_docker_run_options": nil, "pre_deployment_command": nil, "post_deployment_command": nil,
		"is_http_basic_auth_enabled": false, "custom_labels": nil,
	}
}

// planned returns the slugs the plan gives the resource with the Coolify uuid.
func (f *fixture) planned(coolifyUUID string) (project, env, service string) {
	f.t.Helper()
	p, err := f.svc.Plan(f.ctx, CoolifyPlanRequest{Source: f.source(), ServerID: &f.srv.ID})
	require.NoError(f.t, err)
	for _, pp := range p.Projects {
		for _, pe := range pp.Environments {
			for _, r := range pe.Resources {
				if r.UUID == coolifyUUID {
					return pp.Slug, pe.Slug, r.Slug
				}
			}
		}
	}
	f.t.Fatalf("resource %s is not in the plan", coolifyUUID)
	return "", "", ""
}

// firstGenerated is the first hostname the import would generate for the resource.
func (f *fixture) firstGenerated(coolifyUUID string) (project, host string) {
	f.t.Helper()
	proj, env, slug := f.planned(coolifyUUID)
	base, ok := domains.BaseDomain(f.srv)
	require.True(f.t, ok)
	return proj, domains.GenerateHostname(slug, env, proj, base)
}

// takeHosts gives the hostnames to a service of another project, as if they were in use.
func (f *fixture) takeHosts(hosts ...string) {
	f.t.Helper()
	bg := context.Background()
	prj, err := f.q.CreateProject(bg, db.CreateProjectParams{WorkspaceID: f.ws.ID, Name: "Other", Slug: "other"})
	require.NoError(f.t, err)
	env, err := f.q.CreateEnvironment(bg, db.CreateEnvironmentParams{ProjectID: prj.ID, Name: "Dev", Slug: "dev"})
	require.NoError(f.t, err)
	svc, err := f.q.InsertService(bg, db.InsertServiceParams{ID: uuid.New(), EnvironmentID: env.ID, Name: "holder", Slug: "holder", Kind: "app", SourceType: "image",
		Config: []byte(`{"image":"x/y:1","stopGraceSeconds":30,"imageRetention":5,"healthcheck":{"type":"none"}}`)})
	require.NoError(f.t, err)
	for _, h := range hosts {
		_, err := f.pool.Exec(bg, "INSERT INTO domains (service_id, hostname, target_port) VALUES ($1, $2, 80)", svc.ID, h)
		require.NoError(f.t, err)
	}
}

func (f *fixture) domainsOf(project, slug string) []db.Domain {
	f.t.Helper()
	svc, ok := f.envServices(project)[slug]
	require.True(f.t, ok, "service %s/%s", project, slug)
	return mustDomains(f.t, f, svc.ID)
}

func (f *fixture) person() context.Context {
	p, _ := auth.PrincipalFrom(f.ctx)
	return auth.WithPrincipal(context.Background(), &auth.Principal{
		Type: auth.TypeUser, UserID: p.UserID, WorkspaceID: f.ws.ID, Role: auth.RoleOwner, Label: "Ada", Scopes: policy.AllScopes,
	})
}

// guardedToken is a REST token as authentication builds it: asked for approvals.
func (f *fixture) guardedToken() context.Context {
	p, _ := auth.PrincipalFrom(f.ctx)
	id := uuid.New()
	return auth.WithPrincipal(context.Background(), &auth.Principal{
		Type: auth.TypeToken, UserID: p.UserID, TokenID: &id, WorkspaceID: f.ws.ID, Role: auth.RoleOwner,
		Label: "ci", Scopes: policy.AllScopes, RequireProductionApproval: true,
	})
}

func (f *fixture) applyAs(ctx context.Context, req CoolifyApplyRequest) (CoolifyApplyResult, error) {
	req.Source, req.ServerID = f.source(), f.srv.ID
	return f.svc.Apply(ctx, req)
}

func codeOf(t *testing.T, err error) (string, int) {
	t.Helper()
	e, ok := apierr.As(err)
	require.True(t, ok, "%v", err)
	return e.Code, e.Status
}

// ---- production guard on existing environments ----

func (f *fixture) existingProduction() (prj db.Project, env db.Environment) {
	f.t.Helper()
	bg := context.Background()
	prj, err := f.q.CreateProject(bg, db.CreateProjectParams{WorkspaceID: f.ws.ID, Name: "Prod", Slug: "prod"})
	require.NoError(f.t, err)
	env, err = f.q.CreateEnvironment(bg, db.CreateEnvironmentParams{ProjectID: prj.ID, Name: "Production", Slug: "production", IsProduction: true})
	require.NoError(f.t, err)
	return prj, env
}

func TestImportIntoAnExistingProductionEnvironmentNeedsApprovalForGuardedTokens(t *testing.T) {
	f := newFixture(t)
	prj, env := f.existingProduction()
	tok := f.guardedToken()
	choices := CoolifyChoices{Projects: map[string]CoolifyProjectChoice{uuidProjExp: {ExistingProjectID: &prj.ID}}}
	servicesBefore := f.count("SELECT count(*) FROM services")
	projectsBefore := f.count("SELECT count(*) FROM projects")

	// No guard installed on the control plane: the import fails closed.
	_, err := f.applyAs(tok, CoolifyApplyRequest{Choices: choices})
	code, status := codeOf(t, err)
	require.Equal(t, prodguard.CodeApprovalUnavailable, code)
	require.Equal(t, 503, status)
	require.Equal(t, servicesBefore, f.count("SELECT count(*) FROM services"))

	g := prodguard.New(prodguard.Deps{Pool: f.pool, Box: f.box})
	g.Install()
	t.Cleanup(prodguardtest.Uninstall)

	_, err = f.applyAs(tok, CoolifyApplyRequest{Choices: choices})
	staged, ok := prodguard.AsRequired(err)
	require.True(t, ok, "want approval_required, got %v", err)
	require.Contains(t, staged.Plan.Summary, "Import from Coolify into production (Production)")
	require.Equal(t, "Production", staged.Plan.Environment)
	require.Len(t, staged.Plan.Effects, 2)
	require.Contains(t, staged.Plan.Effects[0], "Creates 5 service(s): shop-postgres, shop-ssr, shop-valkey, shop-web, shop-worker")
	require.Equal(t, "They stay staged.", staged.Plan.Effects[1])
	require.Equal(t, servicesBefore, f.count("SELECT count(*) FROM services"), "nothing is created before the approval")
	require.Equal(t, projectsBefore, f.count("SELECT count(*) FROM projects"), "not even the other project")

	// The approval is for these choices: deploying right away is another call.
	_, err = f.applyAs(tok, CoolifyApplyRequest{Choices: choices, Deploy: true})
	deploying, ok := prodguard.AsRequired(err)
	require.True(t, ok, "got %v", err)
	require.NotEqual(t, staged.ApprovalID, deploying.ApprovalID)
	require.Equal(t, "Deploys them right away.", deploying.Plan.Effects[1])

	// A person approves the staged import; the token may then run exactly that.
	_, err = g.Approve(f.person(), staged.ApprovalID)
	require.NoError(t, err)
	_, err = f.applyAs(tok, CoolifyApplyRequest{Choices: choices, Deploy: true})
	_, stillAsked := prodguard.AsRequired(err)
	require.True(t, stillAsked, "the approval of the staged import does not cover a deploying one")
	res, err := f.applyAs(tok, CoolifyApplyRequest{Choices: choices})
	require.NoError(t, err)
	require.Len(t, res.Services, 7)
	require.Empty(t, f.deployer.calls)
	require.Equal(t, 5, f.count("SELECT count(*) FROM services WHERE environment_id = $1", env.ID))

	// A person in the browser is never asked.
	res, err = f.applyAs(f.person(), CoolifyApplyRequest{Choices: choices})
	require.NoError(t, err)
	require.NotEmpty(t, res.Services)
}

func TestImportIntoNewEnvironmentsIsNotGuarded(t *testing.T) {
	f := newFixture(t)
	g := prodguard.New(prodguard.Deps{Pool: f.pool, Box: f.box})
	g.Install()
	t.Cleanup(prodguardtest.Uninstall)

	res, err := f.applyAs(f.guardedToken(), CoolifyApplyRequest{})
	require.NoError(t, err, "a new environment has nothing to protect")
	require.Len(t, res.Services, 7)
}

// ---- the target server ----

func TestApplyNeedsAnExistingServerOfTheTeamThatIsNotRevoked(t *testing.T) {
	f := newFixture(t)
	_, err := f.svc.Apply(f.ctx, CoolifyApplyRequest{Source: f.source(), ServerID: uuid.New()})
	code, status := codeOf(t, err)
	require.Equal(t, "server_not_found", code)
	require.Equal(t, 422, status)

	_, err = f.pool.Exec(context.Background(), "UPDATE servers SET status = 'revoked' WHERE id = $1", f.srv.ID)
	require.NoError(t, err)
	_, err = f.apply(CoolifyChoices{})
	code, status = codeOf(t, err)
	require.Equal(t, "server_revoked", code)
	require.Equal(t, 422, status)
	require.Contains(t, err.Error(), "homelab-1 has been revoked")
	require.Zero(t, f.count("SELECT count(*) FROM projects"))
}

func TestApplyWithoutManagedDatabasesRollsEverythingBack(t *testing.T) {
	f := newFixture(t)
	f.svc.Databases = nil
	_, err := f.apply(CoolifyChoices{})
	code, status := codeOf(t, err)
	require.Equal(t, "databases_unavailable", code)
	require.Equal(t, 422, status)
	require.Zero(t, f.count("SELECT count(*) FROM projects"), "the projects created before the failure are rolled back")
	require.Zero(t, f.count("SELECT count(*) FROM services"))
}

// ---- generated hostnames ----

func TestGeneratedHostnameReplacesTheAutoGeneratedCoolifyAddress(t *testing.T) {
	f := newFixture(t)
	f.coolify.extraApps = []map[string]any{extraApp(uuidGen, "gen-app", "http://gen-app.203.0.113.10.sslip.io", "3000")}
	proj, first := f.firstGenerated(uuidGen)

	res, err := f.apply(CoolifyChoices{})
	require.NoError(t, err)
	doms := f.domainsOf(proj, "gen-app")
	require.Len(t, doms, 1)
	d := doms[0]
	require.Equal(t, first, d.Hostname, "the first free generated hostname")
	require.True(t, d.Generated)
	require.EqualValues(t, 3000, d.TargetPort)
	require.Equal(t, domains.DNSGuidance(first, f.srv.PublicIp), d.StatusMessage)
	var opts agentprotocol.RouteOptions
	require.NoError(t, json.Unmarshal(d.Options, &opts))
	require.NotNil(t, opts.ForceHTTPS)
	require.False(t, *opts.ForceHTTPS, "an http:// address in Coolify keeps plain http allowed")
	require.NotContains(t, strings.Join(res.Warnings, "\n"), "gen-app")
	require.Equal(t, 1, f.count("SELECT count(*) FROM river_job WHERE kind = 'domain_dns_check' AND args->>'domainId' = $1", d.ID.String()),
		"the DNS check of the new domain is queued")
}

func TestGeneratedHostnameTakesTheNextFreeSuffix(t *testing.T) {
	f := newFixture(t)
	f.coolify.extraApps = []map[string]any{extraApp(uuidGen, "gen-app", "http://gen-app.203.0.113.10.sslip.io", "3000")}
	proj, first := f.firstGenerated(uuidGen)
	label, rest, _ := strings.Cut(first, ".")
	f.takeHosts(first, label+"-2."+rest)

	_, err := f.apply(CoolifyChoices{})
	require.NoError(t, err)
	doms := f.domainsOf(proj, "gen-app")
	require.Len(t, doms, 1)
	require.Equal(t, label+"-3."+rest, doms[0].Hostname)
	require.True(t, doms[0].Generated)
}

func TestGeneratedHostnameSuffixKeepsTheLabelWithin63Characters(t *testing.T) {
	f := newFixture(t)
	name := strings.Repeat("x", longAppLen)
	f.coolify.extraApps = []map[string]any{extraApp(uuidLong, name, "http://long.203.0.113.10.sslip.io", "3000")}
	proj, first := f.firstGenerated(uuidLong)
	label, rest, _ := strings.Cut(first, ".")
	require.LessOrEqual(t, len(label), 63)
	require.GreaterOrEqual(t, len(label), 62, "the planned label is as long as a label gets, so the suffix needs room")
	f.takeHosts(first)

	_, err := f.apply(CoolifyChoices{})
	require.NoError(t, err)
	_, _, slug := f.planned2(uuidLong, proj)
	doms := f.domainsOf(proj, slug)
	require.Len(t, doms, 1)
	got, _, _ := strings.Cut(doms[0].Hostname, ".")
	require.LessOrEqual(t, len(got), 63)
	require.True(t, strings.HasSuffix(got, "-2"), got)
	require.Equal(t, strings.Trim(label[:61], "-")+"-2", got)
	require.True(t, strings.HasSuffix(doms[0].Hostname, "."+rest))
}

// planned2 finds the slug a created service got (the plan is not repeatable
// after the import: its slug is the next free one).
func (f *fixture) planned2(coolifyUUID, project string) (string, string, string) {
	f.t.Helper()
	for slug, s := range f.envServices(project) {
		if strings.HasPrefix(slug, "xxx") {
			return project, "", s.Slug
		}
	}
	f.t.Fatalf("service of %s not found", coolifyUUID)
	return "", "", ""
}

func TestImportFailsWhenNoGeneratedHostnameIsFree(t *testing.T) {
	f := newFixture(t)
	f.coolify.extraApps = []map[string]any{extraApp(uuidGen, "gen-app", "http://gen-app.203.0.113.10.sslip.io", "3000")}
	_, first := f.firstGenerated(uuidGen)
	label, rest, _ := strings.Cut(first, ".")
	taken := []string{first}
	for i := 2; i <= 9; i++ {
		taken = append(taken, fmt.Sprintf("%s-%d.%s", label, i, rest))
	}
	f.takeHosts(taken...)
	projectsBefore := f.count("SELECT count(*) FROM projects")

	_, err := f.apply(CoolifyChoices{})
	code, status := codeOf(t, err)
	require.Equal(t, "domain_conflict", code)
	require.Equal(t, 409, status)
	require.Contains(t, err.Error(), "gen-app")
	require.Equal(t, projectsBefore, f.count("SELECT count(*) FROM projects"), "the import is rolled back as a whole")
}

func TestNoHostnameIsGeneratedForAServerWithoutBaseDomainOrPublicIP(t *testing.T) {
	f := newFixture(t)
	f.coolify.extraApps = []map[string]any{extraApp(uuidGen, "gen-app", "http://gen-app.203.0.113.10.sslip.io", "3000")}
	_, err := f.pool.Exec(context.Background(), "UPDATE servers SET public_ip = NULL, base_domain = NULL WHERE id = $1", f.srv.ID)
	require.NoError(t, err)

	res, err := f.apply(CoolifyChoices{})
	require.NoError(t, err)
	require.Contains(t, strings.Join(res.Warnings, "\n"),
		"No hostname was generated for gen-app: the server has neither a base domain nor a public IP. Add a domain in its settings.")
	require.Empty(t, f.domainsOf("shop", "gen-app"))
	require.NotEmpty(t, f.domainsOf("shop", "shop-web"), "explicit hostnames are still created")
}

func TestApplyRefusesToAddDomainsWhileTheServersProxyIsDown(t *testing.T) {
	f := newFixture(t)
	_, err := f.pool.Exec(context.Background(), "UPDATE servers SET proxy_status = $2 WHERE id = $1", f.srv.ID, agentprotocol.ProxyError)
	require.NoError(t, err)

	_, err = f.apply(CoolifyChoices{})
	code, status := codeOf(t, err)
	require.Equal(t, "proxy_unavailable", code)
	require.Equal(t, 422, status)
	require.Contains(t, err.Error(), "the proxy on homelab-1 is not running")
	require.Zero(t, f.count("SELECT count(*) FROM projects"), "nothing is imported half-way")
}

// ---- explicit hostnames ----

func TestDomainPortPathAndSchemeFollowTheCoolifyAddress(t *testing.T) {
	f := newFixture(t)
	f.coolify.extraApps = []map[string]any{
		extraApp(uuidPort, "port-app", "https://port.example.com:9000/api,http://plain.example.com", "3000"),
		extraApp(uuidQueue, "queue-app", "https://queue.example.com", "3000"),
	}
	worker := "worker"
	res, err := f.apply(CoolifyChoices{Resources: map[string]CoolifyResourceChoice{uuidQueue: {Kind: &worker}}})
	require.NoError(t, err)

	byHost := map[string]db.Domain{}
	for _, d := range f.domainsOf("shop", "port-app") {
		byHost[d.Hostname] = d
	}
	require.Len(t, byHost, 2)
	withPort := byHost["port.example.com"]
	require.EqualValues(t, 9000, withPort.TargetPort, "the port of the address wins over the service port")
	require.Equal(t, "/api", withPort.PathPrefix)
	plain := byHost["plain.example.com"]
	require.EqualValues(t, 3000, plain.TargetPort, "no port in the address: the service port")
	require.Empty(t, plain.PathPrefix)
	var opts agentprotocol.RouteOptions
	require.NoError(t, json.Unmarshal(plain.Options, &opts))
	require.NotNil(t, opts.ForceHTTPS)
	require.False(t, *opts.ForceHTTPS)

	// A worker has no port: its domain cannot be routed and is reported, not dropped silently.
	require.Empty(t, f.domainsOf("shop", "queue-app"))
	require.Contains(t, strings.Join(res.Warnings, "\n"), "queue-app: the domain queue.example.com was skipped (the service has no port).")
}

func TestTwoApplicationsWithTheSameHostnameKeepTheFirstAndWarnAboutTheSecond(t *testing.T) {
	f := newFixture(t)
	f.coolify.extraApps = []map[string]any{
		extraApp(uuidDupA, "dup-a", "https://dup.example.com", "3000"),
		extraApp(uuidDupB, "dup-b", "https://dup.example.com", "3000"),
	}
	res, err := f.apply(CoolifyChoices{})
	require.NoError(t, err, "the unique violation must not abort the import transaction")

	first := f.domainsOf("shop", "dup-a")
	require.Len(t, first, 1)
	require.Equal(t, "dup.example.com", first[0].Hostname)
	require.Empty(t, f.domainsOf("shop", "dup-b"))
	require.Contains(t, strings.Join(res.Warnings, "\n"), "dup-b: the hostname dup.example.com is already in use and was skipped.")
	require.NotContains(t, strings.Join(res.Warnings, "\n"), "dup-a: the hostname")
	require.Len(t, res.Services, 9, "the rest of the import was committed")
}

func TestTwoVolumesWithTheSameNameKeepTheFirstAndWarnAboutTheSecond(t *testing.T) {
	f := newFixture(t)
	f.coolify.extraApps = []map[string]any{extraApp(uuidDupA, "dup-a", "https://dup.example.com", "3000")}
	f.coolify.storages = map[string][]any{uuidDupA: {
		map[string]any{"name": uuidDupA + "-data", "mount_path": "/data"},
		map[string]any{"name": uuidDupA + "-Data", "mount_path": "/other"},
		map[string]any{"name": uuidDupA + "-cache", "mount_path": "/cache"},
	}}
	res, err := f.apply(CoolifyChoices{})
	require.NoError(t, err, "the unique violation must not abort the import transaction")

	svc := f.envServices("shop")["dup-a"]
	vols, err := f.q.ListServiceVolumes(context.Background(), svc.ID)
	require.NoError(t, err)
	got := map[string]string{}
	for _, v := range vols {
		got[v.Name] = v.MountPath
	}
	require.Equal(t, map[string]string{"data": "/data", "cache": "/cache"}, got)
	require.Contains(t, strings.Join(res.Warnings, "\n"), "dup-a: volume data was skipped (name or mount path already used).")
	require.Len(t, f.domainsOf("shop", "dup-a"), 1, "what follows the skipped volume is still imported")
}

// ---- after the commit ----

type itemDeployer struct {
	err     error
	created map[uuid.UUID]uuid.UUID // service -> deployment
}

func (d *itemDeployer) DeployStaged(_ context.Context, _ uuid.UUID, ids []uuid.UUID, _ string) (deployments.StagedResult, error) {
	if d.err != nil {
		return deployments.StagedResult{}, d.err
	}
	var out deployments.StagedResult
	for _, id := range ids {
		dep := uuid.New()
		d.created[id] = dep
		out.Items = append(out.Items, deployments.DeploymentView{ID: dep, ServiceID: id})
	}
	return out, nil
}

func TestDeployingImportReportsTheDeploymentsItStarted(t *testing.T) {
	f := newFixture(t)
	dep := &itemDeployer{created: map[uuid.UUID]uuid.UUID{}}
	f.svc.Deploy = dep
	res, err := f.svc.Apply(f.ctx, CoolifyApplyRequest{Source: f.source(), ServerID: f.srv.ID, Deploy: true})
	require.NoError(t, err)
	require.Len(t, res.Services, 7)
	require.Len(t, dep.created, 7, "every created service was handed to the deployer")
	var want []uuid.UUID
	for _, s := range res.Services {
		d, ok := dep.created[s.ID]
		require.True(t, ok, s.Slug)
		want = append(want, d)
	}
	require.ElementsMatch(t, want, res.Deployments)
}

func TestAFailingDeployLeavesTheServicesStagedAndSaysSo(t *testing.T) {
	f := newFixture(t)
	f.svc.Deploy = &itemDeployer{err: errors.New("no capacity")}
	res, err := f.svc.Apply(f.ctx, CoolifyApplyRequest{Source: f.source(), ServerID: f.srv.ID, Deploy: true})
	require.NoError(t, err, "the import itself is committed")
	require.Len(t, res.Services, 7)
	require.Empty(t, res.Deployments)
	require.Contains(t, strings.Join(res.Warnings, "\n"), "The services were added but deploying failed: no capacity")
	require.Equal(t, 5, len(f.envServices("shop")))
}

type refusingGit struct{}

func (refusingGit) Link(context.Context, uuid.UUID, gitproviders.LinkInput) (gitproviders.GitLinkView, error) {
	return gitproviders.GitLinkView{}, errors.New("repository not found")
}

func TestAFailedGitLinkIsAWarningNotAFailure(t *testing.T) {
	f := newFixture(t)
	f.svc.Git = refusingGit{}
	conn := uuid.New()
	res, err := f.apply(CoolifyChoices{GitConnectionID: &conn})
	require.NoError(t, err)
	require.Contains(t, strings.Join(res.Warnings, "\n"),
		"Could not link Dashboard to acme/tracker: repository not found. Link it under Settings → Source so builds can clone it.")
	require.Contains(t, f.envServices("dashboard"), "dashboard")
}

func TestGitServicesAreNotLinkedWithoutAConnectionChoice(t *testing.T) {
	f := newFixture(t)
	res, err := f.apply(CoolifyChoices{})
	require.NoError(t, err)
	require.Empty(t, f.git.calls)
	require.NotContains(t, strings.Join(res.Warnings, "\n"), "Could not link")
}
