package templates_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/canvas"
	"github.com/nebulactrl/nebulactrl/internal/databases"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/deployments"
	"github.com/nebulactrl/nebulactrl/internal/domains"
	"github.com/nebulactrl/nebulactrl/internal/events"
	"github.com/nebulactrl/nebulactrl/internal/jobs"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/prodguard"
	"github.com/nebulactrl/nebulactrl/internal/prodguard/prodguardtest"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/internal/templates"
	"github.com/nebulactrl/nebulactrl/internal/testutil"
	"github.com/nebulactrl/nebulactrl/internal/variables"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// ---- templates under test ----

type demoSpec struct {
	Rev     int
	Version string
	Health  string
	Log     string
	Conf    string
	Region  bool
	Cache   bool
	Notes   bool
}

func demoV1() demoSpec {
	return demoSpec{Rev: 1, Version: "1.0.0", Health: "/health", Log: "info", Conf: `listen = 8080\n`}
}

func ifThen(ok bool, s string) string {
	if ok {
		return s
	}
	return ""
}

// demoYAML renders the "demo" template: an app with a managed database, a
// generated hostname, a volume, a file, a published UDP port and generated and
// input-fed secrets.
func demoYAML(s demoSpec) string {
	r := strings.NewReplacer(
		"@REV@", strconv.Itoa(s.Rev), "@VERSION@", s.Version, "@HEALTH@", s.Health, "@LOG@", s.Log, "@CONF@", s.Conf,
		"@REGIONINPUT@", ifThen(s.Region, "  - {key: REGION, label: Region, default: eu}\n"),
		"@REGIONENV@", ifThen(s.Region, "      REGION: ${input:REGION}\n"),
		"@CACHE@", ifThen(s.Cache, "      - {name: cache, path: /cache}\n"),
		"@NOTES@", ifThen(s.Notes, "notes:\n  - Sign in with the admin password.\n"),
	)
	return r.Replace(`schema: 1
id: demo
revision: @REV@
name: Demo
summary: A demo application with a database.
description: Demo application.
category: devtools
tags: [demo]
links:
  website: https://example.org
upstream:
  license: MIT
  class: permissive
requires:
  memoryMb: 256
  diskGb: 1
inputs:
  - {key: TITLE, label: Title, default: Demo}
  - {key: ADMIN_PASSWORD, label: Admin password, secret: true, default: "${{secret(20)}}"}
@REGIONINPUT@services:
  - key: app
    name: Demo
    kind: app
    image: ghcr.io/acme/demo:@VERSION@
    digest: sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
    port: 8080
    healthcheck:
      type: http
      path: @HEALTH@
      startPeriodSeconds: 5
      deadlineSeconds: 60
    dependsOn: [db]
    domains:
      - generate: true
    env:
      DATABASE_URL: ${{db.DATABASE_URL}}
      TITLE: ${input:TITLE}
      ADMIN_PASSWORD: ${input:ADMIN_PASSWORD}
      SESSION_SECRET: ${{secret(32)}}
      LOG_LEVEL: @LOG@
@REGIONENV@    volumes:
      - {name: data, path: /data}
@CACHE@    files:
      - path: /etc/demo/app.conf
        content: "@CONF@"
    ports:
      - {name: udp, port: 1700, protocol: udp, publish: true}
    resources:
      memoryMb: 256
  - key: db
    name: Demo DB
    kind: database
    database: {engine: postgres, version: "17", databaseName: demo}
group: Demo
@NOTES@`)
}

// hostedYAML has an explicit hostname taken from an input.
const hostedYAML = `schema: 1
id: hosted
revision: 1
name: Hosted
summary: An app on a hostname the installer chooses.
category: web
links:
  website: https://example.org
upstream:
  license: MIT
  class: permissive
inputs:
  - {key: HOSTNAME, label: Hostname, format: hostname, default: app.example.org}
services:
  - key: web
    name: Hosted
    image: ghcr.io/acme/hosted:2.0.0
    digest: sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
    port: 3000
    healthcheck: {type: tcp}
    domains:
      - host: ${input:HOSTNAME}
`

// gpuYAML needs a server an instance admin made unrestricted.
const gpuYAML = `schema: 1
id: gpu
revision: 1
name: GPU
summary: Uses a privileged container.
category: devtools
links:
  website: https://example.org
upstream:
  license: Proprietary
  licenseUrl: https://example.org/license
  class: proprietary
heavy: true
requiresUnrestricted: true
services:
  - key: app
    name: GPU App
    image: ghcr.io/acme/gpu:3.0.0
    digest: sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc
    port: 9000
    healthcheck: {type: tcp}
    docker:
      privileged: true
`

// jobYAML has a one-shot migration the app waits for.
const jobYAML = `schema: 1
id: job
revision: 1
name: Job
summary: An app with a one-shot migration.
category: devtools
links:
  website: https://example.org
upstream:
  license: GPL-3.0
  class: copyleft
services:
  - key: app
    name: Job App
    image: ghcr.io/acme/job:4.0.0
    digest: sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd
    port: 8000
    healthcheck: {type: tcp}
    dependsOn: [migrate]
  - key: migrate
    name: Job Migrate
    image: ghcr.io/acme/job:4.0.0
    digest: sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd
    command: [migrate]
    oneshot: true
`

func catalogOf(t *testing.T, files map[string]string) *templates.Catalog {
	t.Helper()
	m := fstest.MapFS{}
	for id, body := range files {
		m["catalog/"+id+".yaml"] = &fstest.MapFile{Data: []byte(body)}
	}
	cat, errs := templates.LoadFS(m, "catalog")
	for _, err := range errs {
		t.Error(err)
	}
	require.Empty(t, errs)
	return cat
}

// ---- fixture ----

type noopDNSWorker struct {
	river.WorkerDefaults[domains.DNSCheckArgs]
}

func (*noopDNSWorker) Work(context.Context, *river.Job[domains.DNSCheckArgs]) error { return nil }

type deployCall struct {
	env uuid.UUID
	ids []uuid.UUID
	msg string
}

type fakeDeploy struct {
	calls []deployCall
	err   error
}

func (d *fakeDeploy) DeployStaged(_ context.Context, env uuid.UUID, ids []uuid.UUID, msg string) (deployments.StagedResult, error) {
	d.calls = append(d.calls, deployCall{env: env, ids: ids, msg: msg})
	if d.err != nil {
		return deployments.StagedResult{}, d.err
	}
	items := make([]deployments.DeploymentView, len(ids))
	for i, id := range ids {
		items[i] = deployments.DeploymentView{ID: uuid.New(), ServiceID: id}
	}
	return deployments.StagedResult{Items: items, Order: ids}, nil
}

type tfx struct {
	t      *testing.T
	pool   *pgxpool.Pool
	q      *db.Queries
	ctx    context.Context
	box    *secretbox.Box
	bus    *events.Bus
	svcs   *services.Service
	vars   *variables.Service
	dbs    *databases.Service
	jm     *jobs.Manager
	deploy *fakeDeploy
	ws     db.Workspace
	userID uuid.UUID
	prj    db.Project
	env    db.Environment
	prod   db.Environment
	srv    db.Server
}

func newTfx(t *testing.T) *tfx {
	t.Helper()
	databases.RegisterExports()
	t.Cleanup(func() { services.RegisterExportProvider(nil) })
	pool := testutil.NewPool(t)
	q := db.New(pool)
	bg := context.Background()
	ws, err := q.CreateWorkspace(bg, "w")
	require.NoError(t, err)
	u, err := q.CreateUser(bg, db.CreateUserParams{WorkspaceID: ws.ID, Email: "a@example.com", Name: "A", PasswordHash: "x"})
	require.NoError(t, err)
	prj, err := q.CreateProject(bg, db.CreateProjectParams{WorkspaceID: ws.ID, Name: "Shop", Slug: "shop"})
	require.NoError(t, err)
	env, err := q.CreateEnvironment(bg, db.CreateEnvironmentParams{ProjectID: prj.ID, Name: "staging", Slug: "staging"})
	require.NoError(t, err)
	prod, err := q.CreateEnvironment(bg, db.CreateEnvironmentParams{ProjectID: prj.ID, Name: "production", Slug: "production", IsProduction: true})
	require.NoError(t, err)
	srv, err := q.CreateServer(bg, db.CreateServerParams{WorkspaceID: ws.ID, Name: "agent1"})
	require.NoError(t, err)
	_, err = pool.Exec(bg, "UPDATE servers SET status = 'online', base_domain = 'example.com' WHERE id = $1", srv.ID)
	require.NoError(t, err)
	srv, err = q.LoadServerRow(bg, db.LoadServerRowParams{ID: srv.ID})
	require.NoError(t, err)

	box, err := secretbox.New(bytes.Repeat([]byte{9}, 32))
	require.NoError(t, err)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	bus := events.NewBus()
	svcs := services.NewService(pool, bus, box, nil, logger)
	jm := jobs.New(pool, logger)
	jm.AddWorker(func(w *river.Workers) { river.AddWorker(w, &noopDNSWorker{}) })
	return &tfx{
		t: t, pool: pool, q: q, box: box, bus: bus, svcs: svcs, jm: jm, deploy: &fakeDeploy{}, ws: ws, userID: u.ID, prj: prj, env: env, prod: prod, srv: srv,
		vars: variables.NewService(pool, bus, box),
		dbs:  databases.NewService(pool, bus, box, nil, svcs, logger),
		ctx:  auth.WithPrincipal(bg, &auth.Principal{Type: auth.TypeUser, UserID: u.ID, WorkspaceID: ws.ID, Role: auth.RoleOwner, Label: "ada@example.com", Scopes: policy.AllScopes}),
	}
}

// service builds a template service over cat with every collaborator wired.
func (f *tfx) service(cat *templates.Catalog) *templates.Service {
	return templates.NewService(templates.Deps{
		Pool: f.pool, Bus: f.bus, Jobs: f.jm, Catalog: cat, Services: f.svcs,
		Variables: templates.VariablesAdapter{S: f.vars}, Databases: templates.DatabasesAdapter{S: f.dbs},
		Ports: templates.PortsAdapter{}, Deploy: f.deploy, Materialize: templates.MaterializeWith,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
}

func (f *tfx) exec(query string, args ...any) {
	f.t.Helper()
	_, err := f.pool.Exec(context.Background(), query, args...)
	require.NoError(f.t, err)
}

func (f *tfx) count(query string, args ...any) int {
	f.t.Helper()
	var n int
	require.NoError(f.t, f.pool.QueryRow(context.Background(), query, args...).Scan(&n))
	return n
}

func (f *tfx) setServer(set string) {
	f.t.Helper()
	f.exec("UPDATE servers SET "+set+" WHERE id = $1", f.srv.ID)
}

func (f *tfx) setFacts(facts agentprotocol.Facts) {
	f.t.Helper()
	raw, err := json.Marshal(facts)
	require.NoError(f.t, err)
	f.exec("UPDATE servers SET facts = $2 WHERE id = $1", f.srv.ID, raw)
}

func (f *tfx) row(id uuid.UUID) db.Service {
	f.t.Helper()
	sc, err := services.LoadScopeInternal(context.Background(), f.q, id)
	require.NoError(f.t, err)
	return sc.Service
}

func (f *tfx) cfg(id uuid.UUID) services.Config {
	f.t.Helper()
	return services.DecodeConfig(f.row(id).Config)
}

func (f *tfx) varValue(id uuid.UUID, key string) (value string, secret bool) {
	f.t.Helper()
	row, err := f.q.GetServiceVariable(context.Background(), db.GetServiceVariableParams{ServiceID: id, Key: key})
	require.NoError(f.t, err, key)
	v, err := f.box.OpenString(secretbox.ServiceVariablesValue, secretbox.RowID(id.String(), key), row.ValueEnc)
	require.NoError(f.t, err)
	return v, row.IsSecret
}

func (f *tfx) install(svc *templates.Service, id string, req templates.TemplateInstallRequest) templates.TemplateInstallResult {
	f.t.Helper()
	if req.ServerID == uuid.Nil {
		req.ServerID = f.srv.ID
	}
	res, err := svc.Install(f.ctx, f.env.ID, id, req)
	require.NoError(f.t, err)
	return res
}

func (f *tfx) serviceByKey(res templates.TemplateInstallResult, key string) db.Service {
	f.t.Helper()
	for _, s := range res.Instance.Services {
		if s.Key == key {
			return f.row(s.ID)
		}
	}
	require.Failf(f.t, "instance service missing", "%s in %+v", key, res.Instance.Services)
	return db.Service{}
}

func apiErr(t *testing.T, err error) *apierr.Error {
	t.Helper()
	ae, ok := apierr.As(err)
	require.True(t, ok, "not an API error: %v", err)
	return ae
}

func checkOf(t *testing.T, p templates.TemplatePlan, id string) templates.TemplatePlanCheck {
	t.Helper()
	for _, c := range p.Checks {
		if c.ID == id {
			return c
		}
	}
	require.Failf(t, "check missing", "%s in %+v", id, p.Checks)
	return templates.TemplatePlanCheck{}
}

func hasCheck(p templates.TemplatePlan, id string) bool {
	for _, c := range p.Checks {
		if c.ID == id {
			return true
		}
	}
	return false
}

func planService(t *testing.T, p templates.TemplatePlan, key string) templates.TemplatePlanService {
	t.Helper()
	for _, s := range p.Services {
		if s.Key == key {
			return s
		}
	}
	require.Failf(t, "plan service missing", "%s", key)
	return templates.TemplatePlanService{}
}

// ---- plan ----

func TestPlanDescribesTheInstall(t *testing.T) {
	f := newTfx(t)
	svc := f.service(catalogOf(t, map[string]string{"demo": demoYAML(demoV1())}))

	p, err := svc.Plan(f.ctx, f.env.ID, "demo", templates.TemplatePlanRequest{ServerID: &f.srv.ID, Inputs: map[string]string{"TITLE": "Mine"}})
	require.NoError(t, err)
	require.True(t, p.Installable)
	require.True(t, p.ProxyRestart, "a published port restarts the proxy")
	require.Equal(t, "demo", p.Template.ID)

	app := planService(t, p, "app")
	require.Equal(t, "demo", app.Slug)
	require.Equal(t, services.KindApp, app.Kind)
	require.Equal(t, "ghcr.io/acme/demo:1.0.0", app.Image)
	require.Equal(t, 8080, app.Port)
	require.Equal(t, []string{"/data"}, app.Volumes)
	require.Equal(t, 1, app.Files)
	require.Equal(t, "generated", app.Domain)
	require.Equal(t, []string{"db"}, app.DependsOn)
	require.Equal(t, 256, app.MemoryMB)
	require.Equal(t, []templates.TemplatePlanPort{{Name: "udp", Port: 1700, Protocol: "udp", Publish: true}}, app.Ports)
	dbSvc := planService(t, p, "db")
	require.Equal(t, "demo-db", dbSvc.Slug)
	require.Equal(t, services.KindDatabase, dbSvc.Kind)
	require.Equal(t, "postgres 17", dbSvc.Database)

	require.Len(t, p.Inputs, 2)
	require.Equal(t, templates.TemplatePlanInput{Key: "TITLE", Label: "Title", Value: "Mine"}, p.Inputs[0])
	pw := p.Inputs[1]
	require.True(t, pw.Secret)
	require.True(t, pw.Generated)
	require.Empty(t, pw.Value, "a secret or generated value is never shown")
	require.Equal(t, []string{"app.ADMIN_PASSWORD", "app.SESSION_SECRET"}, p.Secrets)

	require.Equal(t, templates.CheckOK, checkOf(t, p, "license").Status)
	require.Equal(t, "Demo is licensed MIT.", checkOf(t, p, "license").Message)
	require.Equal(t, templates.CheckOK, checkOf(t, p, "server").Status)
	require.Equal(t, "agent1 is online.", checkOf(t, p, "server").Message)
	require.Contains(t, checkOf(t, p, "secrets").Message, "2 secret variable(s)")
	require.Equal(t, "A hostname under example.com will be generated.", checkOf(t, p, "domain").Message)
	require.Equal(t, templates.CheckWarn, checkOf(t, p, "proxy-restart").Status)
	require.Equal(t, templates.CheckOK, checkOf(t, p, "port-1700").Status)
	require.Equal(t, "udp/1700 (udp) will be published on agent1.", checkOf(t, p, "port-1700").Message)
	require.Zero(t, f.count("SELECT count(*) FROM services WHERE environment_id = $1", f.env.ID), "a plan writes nothing")
	require.Zero(t, f.count("SELECT count(*) FROM template_instances"))

	// Without a server only the server-free checks run.
	p, err = svc.Plan(f.ctx, f.env.ID, "demo", templates.TemplatePlanRequest{})
	require.NoError(t, err)
	require.True(t, p.Installable)
	require.Equal(t, templates.CheckWarn, checkOf(t, p, "server").Status)
	require.Equal(t, "Choose a server to check capacity, architecture and port conflicts.", checkOf(t, p, "server").Message)
	require.False(t, hasCheck(p, "memory"))
	require.False(t, hasCheck(p, "domain"))
}

func TestPlanRejectsBadRequests(t *testing.T) {
	f := newTfx(t)
	svc := f.service(catalogOf(t, map[string]string{"demo": demoYAML(demoV1())}))
	_, err := svc.Plan(f.ctx, f.env.ID, "nope", templates.TemplatePlanRequest{})
	require.Equal(t, "not_found", apiErr(t, err).Code)
	_, err = svc.Plan(f.ctx, uuid.New(), "demo", templates.TemplatePlanRequest{})
	require.Equal(t, "not_found", apiErr(t, err).Code)
	_, err = svc.Plan(f.ctx, f.env.ID, "demo", templates.TemplatePlanRequest{Inputs: map[string]string{"NOPE": "x"}})
	require.Equal(t, "unknown_input", apiErr(t, err).Code)
	missing := uuid.New()
	_, err = svc.Plan(f.ctx, f.env.ID, "demo", templates.TemplatePlanRequest{ServerID: &missing})
	require.Equal(t, "server_not_found", apiErr(t, err).Code)

	// A principal limited to other projects cannot see the environment.
	other := auth.WithPrincipal(context.Background(), &auth.Principal{
		Type: auth.TypeUser, UserID: f.userID, WorkspaceID: f.ws.ID, Role: auth.RoleDeveloper, Scopes: policy.AllScopes, ProjectIDs: []uuid.UUID{uuid.New()},
	})
	_, err = svc.Plan(other, f.env.ID, "demo", templates.TemplatePlanRequest{})
	require.Equal(t, http.StatusNotFound, apiErr(t, err).Status)
}

func TestPlanServerChecks(t *testing.T) {
	f := newTfx(t)
	svc := f.service(catalogOf(t, map[string]string{"demo": demoYAML(demoV1())}))
	plan := func() templates.TemplatePlan {
		p, err := svc.Plan(f.ctx, f.env.ID, "demo", templates.TemplatePlanRequest{ServerID: &f.srv.ID})
		require.NoError(t, err)
		return p
	}

	f.setServer("status = 'revoked'")
	p := plan()
	require.False(t, p.Installable)
	require.Equal(t, templates.CheckFail, checkOf(t, p, "server").Status)
	require.Equal(t, "agent1 has been revoked.", checkOf(t, p, "server").Message)
	require.False(t, hasCheck(p, "memory"), "no capacity checks for a server that cannot be used")

	f.setServer("status = 'online', drain_state = 'draining'")
	p = plan()
	require.False(t, p.Installable)
	require.Equal(t, "agent1 is being drained and cannot take new services.", checkOf(t, p, "server").Message)
	f.setServer("drain_state = 'drained'")
	require.False(t, plan().Installable)

	f.setServer("drain_state = 'none', accept_workloads = false")
	p = plan()
	require.False(t, p.Installable)
	require.Equal(t, "agent1 does not accept workloads (Accept workloads is off in its settings).", checkOf(t, p, "server").Message)

	f.setServer("accept_workloads = true, status = 'offline'")
	p = plan()
	require.True(t, p.Installable, "an offline server only delays the deploy")
	require.Equal(t, templates.CheckWarn, checkOf(t, p, "server").Status)
	require.Equal(t, "agent1 is offline: the stack is staged, but deploying waits until the agent connects.", checkOf(t, p, "server").Message)
	f.setServer("status = 'online'")

	// Facts: architecture, memory and disk.
	f.setFacts(agentprotocol.Facts{OS: "linux", Arch: "s390x", Capabilities: []string{agentprotocol.CapProxyV2}})
	p = plan()
	require.False(t, p.Installable)
	require.Equal(t, "agent1 runs s390x, but this template only supports amd64, arm64.", checkOf(t, p, "arch").Message)

	f.setFacts(agentprotocol.Facts{OS: "linux", Arch: "arm64", MemTotal: 128 << 20, DiskTotal: 512 << 20, Capabilities: []string{agentprotocol.CapProxyV2}})
	p = plan()
	require.False(t, p.Installable)
	require.Equal(t, templates.CheckOK, checkOf(t, p, "arch").Status)
	require.Equal(t, templates.CheckFail, checkOf(t, p, "memory").Status)
	require.Equal(t, "Needs about 256 MB of RAM; agent1 has 128 MB in total.", checkOf(t, p, "memory").Message)
	require.Equal(t, templates.CheckWarn, checkOf(t, p, "disk").Status)
	require.Equal(t, "Needs about 1 GB of disk; agent1 has 0 GB in total.", checkOf(t, p, "disk").Message)

	f.setFacts(agentprotocol.Facts{OS: "linux", Arch: "amd64", MemTotal: 400 << 20, DiskTotal: 50 << 30, Capabilities: []string{agentprotocol.CapProxyV2}})
	p = plan()
	require.True(t, p.Installable)
	require.Equal(t, templates.CheckWarn, checkOf(t, p, "memory").Status)
	require.Contains(t, checkOf(t, p, "memory").Message, "so it will be tight")
	require.False(t, hasCheck(p, "disk"))

	f.setFacts(agentprotocol.Facts{OS: "linux", Arch: "amd64", MemTotal: 4096 << 20, Capabilities: []string{agentprotocol.CapProxyV2}})
	p = plan()
	require.Equal(t, templates.CheckOK, checkOf(t, p, "memory").Status)
	require.Equal(t, "Needs about 256 MB of RAM; agent1 has 4096 MB.", checkOf(t, p, "memory").Message)

	// An agent that predates proxy v2 cannot publish ports.
	f.setFacts(agentprotocol.Facts{OS: "linux", Arch: "amd64", MemTotal: 4096 << 20})
	p = plan()
	require.False(t, p.Installable)
	require.Equal(t, "The agent on agent1 is too old to publish TCP/UDP ports; update the agent first.", checkOf(t, p, "agent").Message)
	f.setFacts(agentprotocol.Facts{OS: "linux", Arch: "amd64", Capabilities: []string{agentprotocol.CapProxyV2}})

	// Domains need a working proxy and a base domain.
	f.setServer("proxy_status = 'error'")
	p = plan()
	require.False(t, p.Installable)
	require.Equal(t, "The proxy on agent1 is not running; domains cannot be attached.", checkOf(t, p, "proxy").Message)
	f.setServer("proxy_status = NULL, base_domain = NULL")
	p = plan()
	require.True(t, p.Installable)
	require.Equal(t, templates.CheckWarn, checkOf(t, p, "domain").Status)
	require.Contains(t, checkOf(t, p, "domain").Message, "neither a base domain nor a public IP")
	f.setServer("public_ip = '203.0.113.7'")
	p = plan()
	require.Equal(t, "A hostname under 203.0.113.7.sslip.io will be generated.", checkOf(t, p, "domain").Message)
}

func TestPlanLicenceHeavyAndUnrestrictedChecks(t *testing.T) {
	f := newTfx(t)
	svc := f.service(catalogOf(t, map[string]string{"gpu": gpuYAML, "demo": demoYAML(demoV1())}))
	p, err := svc.Plan(f.ctx, f.env.ID, "gpu", templates.TemplatePlanRequest{})
	require.NoError(t, err)
	require.Equal(t, templates.CheckWarn, checkOf(t, p, "license").Status)
	require.Contains(t, checkOf(t, p, "license").Message, "proprietary software (Proprietary)")
	require.Equal(t, templates.CheckWarn, checkOf(t, p, "heavy").Status)
	require.Equal(t, templates.CheckWarn, checkOf(t, p, "unrestricted").Status, "without a server it only warns")

	p, err = svc.Plan(f.ctx, f.env.ID, "gpu", templates.TemplatePlanRequest{ServerID: &f.srv.ID})
	require.NoError(t, err)
	require.False(t, p.Installable)
	require.Equal(t, templates.CheckFail, checkOf(t, p, "unrestricted").Status)
	require.Contains(t, checkOf(t, p, "unrestricted").Message, "agent1 is a restricted server")

	f.setServer("unrestricted = true")
	p, err = svc.Plan(f.ctx, f.env.ID, "gpu", templates.TemplatePlanRequest{ServerID: &f.srv.ID})
	require.NoError(t, err)
	require.True(t, p.Installable)
	require.Equal(t, templates.CheckOK, checkOf(t, p, "unrestricted").Status)
}

// ---- install ----

func TestInstallCreatesTheWholeStack(t *testing.T) {
	f := newTfx(t)
	svc := f.service(catalogOf(t, map[string]string{"demo": demoYAML(demoV1())}))

	res, err := svc.Install(f.ctx, f.env.ID, "demo", templates.TemplateInstallRequest{
		ServerID: f.srv.ID, Inputs: map[string]string{"TITLE": "My Demo"}, Deploy: true,
	})
	require.NoError(t, err)
	require.Empty(t, res.Warnings)

	inst := res.Instance
	require.Equal(t, "demo", inst.TemplateID)
	require.Equal(t, 1, inst.TemplateRevision)
	require.Equal(t, "1.0.0", inst.TemplateVersion)
	require.Equal(t, "Demo", inst.Name, "the group title is the canvas title")
	require.Equal(t, map[string]string{"TITLE": "My Demo"}, inst.Inputs, "secret and generated inputs are not recorded")
	keys := []string{}
	for _, s := range inst.Services {
		keys = append(keys, s.Key)
	}
	require.Equal(t, []string{"app", "db", "port:app:udp"}, keys)
	require.Len(t, res.Services, 3)

	app := f.serviceByKey(res, "app")
	dbRow := f.serviceByKey(res, "db")
	route := f.serviceByKey(res, "port:app:udp")
	require.Equal(t, "demo", app.Slug)
	require.Equal(t, "demo-db", dbRow.Slug)
	require.Equal(t, services.KindDatabase, dbRow.Kind)
	require.Equal(t, "route", route.Kind)
	require.NotNil(t, app.ServerID)
	require.Equal(t, f.srv.ID, *app.ServerID)
	require.Equal(t, inst.ID, *app.TemplateInstanceID)
	require.Equal(t, "app", *app.TemplateKey)

	cfg := f.cfg(app.ID)
	require.Equal(t, "ghcr.io/acme/demo:1.0.0@sha256:"+strings.Repeat("a", 64), cfg.Image)
	require.Equal(t, 8080, cfg.Port)
	require.Equal(t, 256, cfg.MemoryMB)
	require.Equal(t, "http", cfg.Healthcheck.Type)
	require.Equal(t, "/health", cfg.Healthcheck.Path)
	require.Equal(t, []services.FileMount{{Path: "/etc/demo/app.conf", Content: "listen = 8080\n"}}, cfg.Files)

	vols, err := f.q.ListServiceVolumes(context.Background(), app.ID)
	require.NoError(t, err)
	require.Len(t, vols, 1)
	require.Equal(t, "data", vols[0].Name)
	require.Equal(t, "/data", vols[0].MountPath)

	// Variables: inputs expanded, references rewritten to the real slug,
	// generators materialised once, credentials stored as secrets.
	title, secret := f.varValue(app.ID, "TITLE")
	require.Equal(t, "My Demo", title)
	require.False(t, secret)
	dburl, _ := f.varValue(app.ID, "DATABASE_URL")
	require.Equal(t, "${{demo-db.DATABASE_URL}}", dburl)
	pw, secret := f.varValue(app.ID, "ADMIN_PASSWORD")
	require.True(t, secret)
	require.Len(t, pw, 20, "the input's generated default is the stored value")
	session, secret := f.varValue(app.ID, "SESSION_SECRET")
	require.True(t, secret)
	require.Len(t, session, 32)
	require.NotContains(t, session, "$")
	level, secret := f.varValue(app.ID, "LOG_LEVEL")
	require.Equal(t, "info", level)
	require.False(t, secret)

	require.Equal(t, 1, f.count("SELECT count(*) FROM service_links WHERE from_service_id = $1 AND to_service_id = $2", app.ID, dbRow.ID))
	var host string
	var generated bool
	require.NoError(t, f.pool.QueryRow(context.Background(), "SELECT hostname, generated FROM domains WHERE service_id = $1", app.ID).Scan(&host, &generated))
	require.Equal(t, "demo-staging-shop.example.com", host)
	require.True(t, generated)
	require.Equal(t, 1, f.count("SELECT count(*) FROM river_job WHERE kind = 'domain_dns_check'"), "the hostname is checked")

	// The group of the template's own services is on the canvas; the route service stays outside.
	cv, err := canvas.NewService(f.pool, f.bus).Get(f.ctx, f.prj.ID, f.env.ID)
	require.NoError(t, err)
	require.Len(t, cv.Layout.Groups, 1)
	require.Equal(t, "Demo", cv.Layout.Groups[0].Name)
	require.Equal(t, inst.GroupID, cv.Layout.Groups[0].ID)
	require.ElementsMatch(t, []string{app.ID.String(), dbRow.ID.String()}, cv.Layout.Groups[0].ServiceIDs)

	// Audited with what matters and nothing secret.
	var meta []byte
	require.NoError(t, f.pool.QueryRow(context.Background(), "SELECT metadata FROM audit_events WHERE action = 'template.install' AND resource_id = $1", inst.ID.String()).Scan(&meta))
	m := auditMeta(t, meta)
	require.Equal(t, "demo", m["template"])
	require.Equal(t, []any{"demo-staging-shop.example.com"}, m["hostnames"])
	require.EqualValues(t, 3, m["services"])
	require.NotContains(t, string(meta), pw, "an input's generated secret is never audited")

	// Deploy: everything created, in one call, with a message.
	require.Len(t, f.deploy.calls, 1)
	require.Equal(t, f.env.ID, f.deploy.calls[0].env)
	require.Equal(t, "Installed from template Demo", f.deploy.calls[0].msg)
	require.ElementsMatch(t, []uuid.UUID{app.ID, dbRow.ID, route.ID}, f.deploy.calls[0].ids)
	require.Len(t, res.Deployments, 3)

	// The stack is listed with its services.
	list, err := svc.ListInstances(f.ctx, f.env.ID)
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.Equal(t, inst.ID, list[0].ID)
	require.Len(t, list[0].Services, 3)
	require.Equal(t, map[string]string{"TITLE": "My Demo"}, list[0].Inputs)
	require.False(t, list[0].UpdateAvailable)
	require.Equal(t, 1, list[0].CurrentRevision)
	other, err := svc.ListInstances(f.ctx, f.prod.ID)
	require.NoError(t, err)
	require.Empty(t, other)
	_, err = svc.ListInstances(f.ctx, uuid.New())
	require.Equal(t, "not_found", apiErr(t, err).Code)
}

func TestInstallNameOverrideAndDeployFailure(t *testing.T) {
	f := newTfx(t)
	svc := f.service(catalogOf(t, map[string]string{"job": jobYAML}))
	f.deploy.err = errors.New("no agent")
	res, err := svc.Install(f.ctx, f.env.ID, "job", templates.TemplateInstallRequest{ServerID: f.srv.ID, Name: "  Nightly job  ", Deploy: true})
	require.NoError(t, err, "a failed deploy does not undo the install")
	require.Equal(t, "Nightly job", res.Instance.Name)
	require.Equal(t, []string{"The stack was added but deploying failed: no agent"}, res.Warnings)
	require.Empty(t, res.Deployments)
	require.Len(t, res.Services, 2)
	require.Equal(t, 2, f.count("SELECT count(*) FROM services WHERE environment_id = $1 AND template_instance_id = $2", f.env.ID, res.Instance.ID))

	// The one-shot service is configured to run once and not be health checked.
	mig := f.serviceByKey(res, "migrate")
	cfg := f.cfg(mig.ID)
	require.True(t, cfg.OneShot)
	require.Equal(t, services.HealthNone, cfg.Healthcheck.Type)
	require.Equal(t, []string{"migrate"}, cfg.StartCommand)
	require.Equal(t, 1, f.count("SELECT count(*) FROM service_links WHERE from_service_id = $1 AND to_service_id = $2", f.serviceByKey(res, "app").ID, mig.ID), "the app waits for the migration")

	// A second install of the same template gets its own slugs.
	res2 := f.install(svc, "job", templates.TemplateInstallRequest{})
	require.Equal(t, "Job", res2.Instance.Name, "no title given: the template's group or name")
	require.Equal(t, "job-app-2", f.serviceByKey(res2, "app").Slug)
}

func TestInstallRefusals(t *testing.T) {
	f := newTfx(t)
	svc := f.service(catalogOf(t, map[string]string{"demo": demoYAML(demoV1()), "gpu": gpuYAML}))
	install := func(ctx context.Context, id string, req templates.TemplateInstallRequest) error {
		_, err := svc.Install(ctx, f.env.ID, id, req)
		return err
	}
	noStack := func() {
		t.Helper()
		require.Zero(t, f.count("SELECT count(*) FROM services WHERE environment_id = $1", f.env.ID), "a refused install leaves nothing behind")
		require.Zero(t, f.count("SELECT count(*) FROM template_instances"))
	}

	require.Equal(t, "server_not_found", apiErr(t, install(f.ctx, "demo", templates.TemplateInstallRequest{})).Code, "no server given")
	require.Equal(t, "server_not_found", apiErr(t, install(f.ctx, "demo", templates.TemplateInstallRequest{ServerID: uuid.New()})).Code)
	require.Equal(t, "not_found", apiErr(t, install(f.ctx, "nope", templates.TemplateInstallRequest{ServerID: f.srv.ID})).Code)
	require.Equal(t, "unknown_input", apiErr(t, install(f.ctx, "demo", templates.TemplateInstallRequest{ServerID: f.srv.ID, Inputs: map[string]string{"X": "1"}})).Code)

	viewer := auth.WithPrincipal(context.Background(), &auth.Principal{Type: auth.TypeUser, UserID: f.userID, WorkspaceID: f.ws.ID, Role: auth.RoleViewer, Scopes: []string{policy.ScopeProjectsRead}})
	require.Equal(t, http.StatusForbidden, apiErr(t, install(viewer, "demo", templates.TemplateInstallRequest{ServerID: f.srv.ID})).Status)
	noDeploy := auth.WithPrincipal(context.Background(), &auth.Principal{Type: auth.TypeUser, UserID: f.userID, WorkspaceID: f.ws.ID, Role: auth.RoleDeveloper, Scopes: []string{policy.ScopeProjectsRead, policy.ScopeProjectsWrite}})
	require.Equal(t, http.StatusForbidden, apiErr(t, install(noDeploy, "demo", templates.TemplateInstallRequest{ServerID: f.srv.ID, Deploy: true})).Status, "deploying needs deployments:write")
	require.Empty(t, f.deploy.calls)

	// A failed preflight check refuses the install and carries the checks.
	f.setServer("status = 'revoked'")
	err := install(f.ctx, "demo", templates.TemplateInstallRequest{ServerID: f.srv.ID})
	ae := apiErr(t, err)
	require.Equal(t, "preflight_failed", ae.Code)
	require.Equal(t, "agent1 has been revoked.", ae.Message)
	require.NotNil(t, ae.Details["checks"])
	f.setServer("status = 'online'")

	err = install(f.ctx, "gpu", templates.TemplateInstallRequest{ServerID: f.srv.ID})
	require.Equal(t, "requires_unrestricted_server", apiErr(t, err).Code)
	noStack()

	f.setServer("unrestricted = true")
	res := f.install(svc, "gpu", templates.TemplateInstallRequest{})
	require.Equal(t, "privileged", f.cfgDockerFlag(f.serviceByKey(res, "app").ID))
}

// cfgDockerFlag reports which unrestricted option a service uses.
func (f *tfx) cfgDockerFlag(id uuid.UUID) string {
	f.t.Helper()
	if d := f.cfg(id).Docker; d != nil && d.Privileged {
		return "privileged"
	}
	return ""
}

func TestInstallRollsBackWhenACollaboratorIsMissing(t *testing.T) {
	f := newTfx(t)
	cat := catalogOf(t, map[string]string{"demo": demoYAML(demoV1())})
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	base := templates.Deps{
		Pool: f.pool, Bus: f.bus, Jobs: f.jm, Catalog: cat, Services: f.svcs, Materialize: templates.MaterializeWith, Logger: logger,
		Variables: templates.VariablesAdapter{S: f.vars}, Databases: templates.DatabasesAdapter{S: f.dbs}, Ports: templates.PortsAdapter{}, Deploy: f.deploy,
	}
	cases := map[string]struct {
		mutate func(*templates.Deps)
		code   string
	}{
		"no databases": {func(d *templates.Deps) { d.Databases = nil }, "databases_unavailable"},
		"no variables": {func(d *templates.Deps) { d.Variables = nil }, "variables_unavailable"},
		"no ports":     {func(d *templates.Deps) { d.Ports = nil }, "ports_unavailable"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			d := base
			c.mutate(&d)
			_, err := templates.NewService(d).Install(f.ctx, f.env.ID, "demo", templates.TemplateInstallRequest{ServerID: f.srv.ID})
			require.Equal(t, c.code, apiErr(t, err).Code)
			require.Zero(t, f.count("SELECT count(*) FROM services WHERE environment_id = $1", f.env.ID), "services created before the failure are rolled back")
			require.Zero(t, f.count("SELECT count(*) FROM template_instances"))
			require.Zero(t, f.count("SELECT count(*) FROM canvas_layouts"))
			require.Zero(t, f.count("SELECT count(*) FROM domains"))
		})
	}
}

func TestInstallChecksHostnamesAndPorts(t *testing.T) {
	f := newTfx(t)
	svc := f.service(catalogOf(t, map[string]string{"hosted": hostedYAML, "demo": demoYAML(demoV1())}))

	_, err := svc.Plan(f.ctx, f.env.ID, "hosted", templates.TemplatePlanRequest{ServerID: &f.srv.ID, Inputs: map[string]string{"HOSTNAME": "Not A Host"}})
	require.Equal(t, "invalid_input", apiErr(t, err).Code, "the input's hostname format is enforced")

	res := f.install(svc, "hosted", templates.TemplateInstallRequest{Inputs: map[string]string{"HOSTNAME": "app.example.org"}})
	var host string
	var generated bool
	require.NoError(t, f.pool.QueryRow(context.Background(), "SELECT hostname, generated FROM domains WHERE service_id = $1", f.serviceByKey(res, "web").ID).Scan(&host, &generated))
	require.Equal(t, "app.example.org", host)
	require.False(t, generated)

	// The hostname is taken now: the plan says so and the install is refused.
	p, err := svc.Plan(f.ctx, f.env.ID, "hosted", templates.TemplatePlanRequest{ServerID: &f.srv.ID})
	require.NoError(t, err)
	require.False(t, p.Installable)
	require.Equal(t, "The hostname app.example.org is already in use.", checkOf(t, p, "domain").Message)
	_, err = svc.Install(f.ctx, f.env.ID, "hosted", templates.TemplateInstallRequest{ServerID: f.srv.ID})
	require.Equal(t, "preflight_failed", apiErr(t, err).Code)
	require.Equal(t, 1, f.count("SELECT count(*) FROM template_instances"))

	// The same for a published port once a route owns it.
	f.install(svc, "demo", templates.TemplateInstallRequest{})
	p, err = svc.Plan(f.ctx, f.env.ID, "demo", templates.TemplatePlanRequest{ServerID: &f.srv.ID})
	require.NoError(t, err)
	require.False(t, p.Installable)
	require.Equal(t, templates.CheckFail, checkOf(t, p, "port-1700").Status)
	require.Equal(t, "udp/1700 is already published by another route on agent1.", checkOf(t, p, "port-1700").Message)
}

func TestInstallWithoutAProxyOrBaseDomainSkipsTheHostname(t *testing.T) {
	f := newTfx(t)
	svc := f.service(catalogOf(t, map[string]string{"demo": demoYAML(demoV1())}))
	f.setServer("base_domain = NULL")
	res := f.install(svc, "demo", templates.TemplateInstallRequest{})
	require.Len(t, res.Warnings, 1)
	require.Contains(t, res.Warnings[0], "No hostname was generated for Demo")
	require.Zero(t, f.count("SELECT count(*) FROM domains"))

	f.setServer("base_domain = 'example.com', proxy_status = 'error'")
	_, err := svc.Install(f.ctx, f.env.ID, "demo", templates.TemplateInstallRequest{ServerID: f.srv.ID})
	require.Equal(t, "preflight_failed", apiErr(t, err).Code, "the plan already refuses a server whose proxy is down")
}

// ---- deleting an instance ----

func TestDeleteInstanceRemovesTheStack(t *testing.T) {
	f := newTfx(t)
	svc := f.service(catalogOf(t, map[string]string{"demo": demoYAML(demoV1())}))
	res := f.install(svc, "demo", templates.TemplateInstallRequest{})
	id := res.Instance.ID

	require.Equal(t, "not_found", apiErr(t, svc.DeleteInstance(f.ctx, uuid.New(), "demo", false)).Code)
	err := svc.DeleteInstance(f.ctx, id, "something-else", false)
	ae := apiErr(t, err)
	require.Equal(t, "confirm_mismatch", ae.Code)
	require.Equal(t, "demo", ae.Details["templateId"])
	viewer := auth.WithPrincipal(context.Background(), &auth.Principal{Type: auth.TypeUser, UserID: f.userID, WorkspaceID: f.ws.ID, Role: auth.RoleViewer, Scopes: []string{policy.ScopeProjectsRead}})
	require.Equal(t, http.StatusForbidden, apiErr(t, svc.DeleteInstance(viewer, id, "demo", false)).Status)
	list, err := svc.ListInstances(f.ctx, f.env.ID)
	require.NoError(t, err)
	require.Len(t, list, 1, "refused deletes keep the stack")

	require.NoError(t, svc.DeleteInstance(f.ctx, id, "demo", true))
	list, err = svc.ListInstances(f.ctx, f.env.ID)
	require.NoError(t, err)
	require.Empty(t, list)
	require.Zero(t, f.count("SELECT count(*) FROM services WHERE template_instance_id = $1 AND deleted_at IS NULL", id), "every service of the stack is gone")
	cv, err := canvas.NewService(f.pool, f.bus).Get(f.ctx, f.prj.ID, f.env.ID)
	require.NoError(t, err)
	require.Empty(t, cv.Layout.Groups, "the canvas group is removed")
	var meta []byte
	require.NoError(t, f.pool.QueryRow(context.Background(), "SELECT metadata FROM audit_events WHERE action = 'template.delete' AND resource_id = $1", id.String()).Scan(&meta))
	dm := auditMeta(t, meta)
	require.Equal(t, true, dm["deleteVolumes"])
	require.EqualValues(t, 3, dm["services"])

	// Once deleted, the instance is gone for good.
	require.Equal(t, "not_found", apiErr(t, svc.DeleteInstance(f.ctx, id, "demo", false)).Code)
}

func TestDeleteInstanceInProductionNeedsApprovalForRESTTokens(t *testing.T) {
	f := newTfx(t)
	g := prodguard.New(prodguard.Deps{Pool: f.pool, Box: f.box})
	g.Install()
	t.Cleanup(prodguardtest.Uninstall)
	svc := f.service(catalogOf(t, map[string]string{"job": jobYAML}))
	res, err := svc.Install(f.ctx, f.prod.ID, "job", templates.TemplateInstallRequest{ServerID: f.srv.ID})
	require.NoError(t, err, "a person in the browser is never asked")
	tokenID := uuid.New()
	rest := auth.WithPrincipal(context.Background(), &auth.Principal{
		Type: auth.TypeToken, UserID: f.userID, TokenID: &tokenID, WorkspaceID: f.ws.ID, Role: auth.RoleOwner,
		Label: "ci", Scopes: policy.AllScopes, RequireProductionApproval: true,
	})

	err = svc.DeleteInstance(rest, res.Instance.ID, "job", false)
	ar, ok := prodguard.AsRequired(err)
	require.True(t, ok, "got %v", err)
	require.Contains(t, ar.Plan.Summary, "Delete the template installation job from production")
	require.Equal(t, 2, f.count("SELECT count(*) FROM services WHERE template_instance_id = $1 AND deleted_at IS NULL", res.Instance.ID), "nothing is deleted before the approval")

	_, err = g.Approve(f.ctx, ar.ApprovalID)
	require.NoError(t, err)
	require.NoError(t, svc.DeleteInstance(rest, res.Instance.ID, "job", false))
	require.Zero(t, f.count("SELECT count(*) FROM services WHERE template_instance_id = $1 AND deleted_at IS NULL", res.Instance.ID))
}

// ---- updates ----

func v2Spec() demoSpec {
	return demoSpec{Rev: 2, Version: "1.1.0", Health: "/healthz", Log: "warn", Conf: `listen = 9090\n`, Region: true, Cache: true, Notes: true}
}

func changeOf(u templates.TemplateUpdate, field string) templates.TemplateUpdateChange {
	for _, c := range u.Changes {
		if c.Field == field {
			return c
		}
	}
	return templates.TemplateUpdateChange{}
}

func TestTemplateUpdateLifecycle(t *testing.T) {
	f := newTfx(t)
	v1 := f.service(catalogOf(t, map[string]string{"demo": demoYAML(demoV1())}))
	v2 := f.service(catalogOf(t, map[string]string{"demo": demoYAML(v2Spec())}))
	res := f.install(v1, "demo", templates.TemplateInstallRequest{Inputs: map[string]string{"TITLE": "Mine"}})
	app := f.serviceByKey(res, "app")
	id := res.Instance.ID

	// The old catalog has nothing newer to offer.
	u, err := v1.GetUpdate(f.ctx, id)
	require.NoError(t, err)
	require.False(t, u.Available)
	require.Empty(t, u.Changes)
	require.Empty(t, u.Blocked)

	// The user edits one of the template's variables on the service: the update must leave it.
	_, err = f.vars.Put(f.ctx, app.ID, []variables.VariableInput{{Key: "LOG_LEVEL", Value: new("debug")}})
	require.NoError(t, err)

	inst, err := v2.ListInstances(f.ctx, f.env.ID)
	require.NoError(t, err)
	require.Len(t, inst, 1)
	require.True(t, inst[0].UpdateAvailable)
	require.Equal(t, 2, inst[0].CurrentRevision)
	require.Equal(t, "1.1.0", inst[0].CurrentVersion)
	require.Equal(t, 1, inst[0].TemplateRevision)

	u, err = v2.GetUpdate(f.ctx, id)
	require.NoError(t, err)
	require.True(t, u.Available)
	require.Equal(t, 1, u.InstalledRevision)
	require.Equal(t, "1.0.0", u.InstalledVersion)
	require.Equal(t, 2, u.CurrentRevision)
	require.Equal(t, "1.1.0", u.CurrentVersion)
	require.Equal(t, []string{"Sign in with the admin password."}, u.Notes)
	require.Equal(t, templates.ChangeApply, changeOf(u, "image").Status)
	require.Equal(t, templates.ChangeApply, changeOf(u, "healthcheck.path").Status)
	require.Equal(t, templates.ChangeApply, changeOf(u, "env.REGION").Status)
	require.Equal(t, templates.ChangeApply, changeOf(u, "volume:cache").Status)
	require.Equal(t, templates.ChangeApply, changeOf(u, "file:/etc/demo/app.conf").Status)
	require.Equal(t, templates.ChangeApply, changeOf(u, "input:REGION").Status)
	logChange := changeOf(u, "env.LOG_LEVEL")
	require.Equal(t, templates.ChangeKept, logChange.Status)
	require.Contains(t, logChange.Reason, "You changed this variable")
	require.Equal(t, u.Apply+u.Kept+u.Manual+u.Applied, len(u.Changes))
	require.Positive(t, u.Apply)
	require.Equal(t, 1, u.Kept)
	require.Len(t, u.Inputs, 1)
	require.Equal(t, "REGION", u.Inputs[0].Key)
	require.Equal(t, "eu", u.Inputs[0].Value)
	require.Equal(t, "ghcr.io/acme/demo:1.0.0", f.cfgImageRef(app.ID), "GetUpdate writes nothing")

	// Refused applications.
	_, err = v2.ApplyUpdate(f.ctx, id, templates.TemplateUpdateRequest{Revision: 1})
	require.Equal(t, "template_changed", apiErr(t, err).Code)
	require.Equal(t, http.StatusConflict, apiErr(t, err).Status)
	_, err = v2.ApplyUpdate(f.ctx, id, templates.TemplateUpdateRequest{Revision: 2, Inputs: map[string]string{"TITLE": "x"}})
	require.Equal(t, "unknown_input", apiErr(t, err).Code, "only inputs the new revision added can be given")
	_, err = v2.ApplyUpdate(f.ctx, uuid.New(), templates.TemplateUpdateRequest{Revision: 2})
	require.Equal(t, "not_found", apiErr(t, err).Code)
	viewer := auth.WithPrincipal(context.Background(), &auth.Principal{Type: auth.TypeUser, UserID: f.userID, WorkspaceID: f.ws.ID, Role: auth.RoleViewer, Scopes: []string{policy.ScopeProjectsRead}})
	_, err = v2.ApplyUpdate(viewer, id, templates.TemplateUpdateRequest{Revision: 2})
	require.Equal(t, http.StatusForbidden, apiErr(t, err).Status)
	_, err = v2.GetUpdate(viewer, id)
	require.NoError(t, err, "reading an update needs only projects:read")
	require.Equal(t, "ghcr.io/acme/demo:1.0.0", f.cfgImageRef(app.ID))

	// Applying stages the changes and records the new revision.
	genBefore := f.row(app.ID).Generation
	out, err := v2.ApplyUpdate(f.ctx, id, templates.TemplateUpdateRequest{Revision: 2, Inputs: map[string]string{"REGION": "us"}})
	require.NoError(t, err)
	staged := 0
	for _, c := range u.Changes {
		if c.Status == templates.ChangeApply && c.Service != "" {
			staged++
		}
	}
	require.Equal(t, staged, out.Staged, "input changes are recorded, not staged")
	require.Equal(t, 5, out.Staged)
	require.Equal(t, []string{"demo"}, out.Services)
	require.Equal(t, 2, out.Instance.TemplateRevision)
	require.Equal(t, "1.1.0", out.Instance.TemplateVersion)
	require.Equal(t, map[string]string{"TITLE": "Mine", "REGION": "us"}, out.Instance.Inputs)
	require.False(t, out.Instance.UpdateAvailable)
	require.Empty(t, out.Warnings, "nothing needs manual work in this revision")

	cfg := f.cfg(app.ID)
	require.Equal(t, "ghcr.io/acme/demo:1.1.0@sha256:"+strings.Repeat("a", 64), cfg.Image)
	require.Equal(t, "/healthz", cfg.Healthcheck.Path)
	require.Equal(t, []services.FileMount{{Path: "/etc/demo/app.conf", Content: "listen = 9090\n"}}, cfg.Files)
	require.Greater(t, f.row(app.ID).Generation, genBefore, "the changes are staged on the service")
	region, _ := f.varValue(app.ID, "REGION")
	require.Equal(t, "us", region, "the new input's value feeds the new variable")
	level, _ := f.varValue(app.ID, "LOG_LEVEL")
	require.Equal(t, "debug", level, "a value the user changed is kept")
	title, _ := f.varValue(app.ID, "TITLE")
	require.Equal(t, "Mine", title)
	vols, err := f.q.ListServiceVolumes(context.Background(), app.ID)
	require.NoError(t, err)
	names := []string{}
	for _, v := range vols {
		names = append(names, v.Name+":"+v.MountPath)
	}
	require.ElementsMatch(t, []string{"data:/data", "cache:/cache"}, names)
	require.Equal(t, 1, f.count("SELECT count(*) FROM audit_events WHERE action = 'template.update' AND resource_id = $1", id.String()))

	// Nothing is left to do, and a retry stages nothing again.
	u, err = v2.GetUpdate(f.ctx, id)
	require.NoError(t, err)
	require.False(t, u.Available)
	require.Equal(t, 2, u.InstalledRevision)
	out, err = v2.ApplyUpdate(f.ctx, id, templates.TemplateUpdateRequest{Revision: 2})
	require.NoError(t, err)
	require.Zero(t, out.Staged)
	require.Empty(t, out.Services)
	require.Equal(t, "ghcr.io/acme/demo:1.1.0@sha256:"+strings.Repeat("a", 64), f.cfg(app.ID).Image)
}

func (f *tfx) cfgImageRef(id uuid.UUID) string {
	f.t.Helper()
	repo, _, _ := strings.Cut(f.cfg(id).Image, "@")
	return repo
}

func TestTemplateUpdateBlockedStates(t *testing.T) {
	f := newTfx(t)
	v1 := f.service(catalogOf(t, map[string]string{"demo": demoYAML(demoV1())}))
	res := f.install(v1, "demo", templates.TemplateInstallRequest{})
	id := res.Instance.ID

	// The template left the catalog.
	gone := f.service(catalogOf(t, map[string]string{"job": jobYAML}))
	u, err := gone.GetUpdate(f.ctx, id)
	require.NoError(t, err)
	require.Equal(t, "This template is no longer in the catalog.", u.Blocked)
	require.False(t, u.Available)
	require.Zero(t, u.CurrentRevision)
	_, err = gone.ApplyUpdate(f.ctx, id, templates.TemplateUpdateRequest{Revision: 1})
	require.Equal(t, "update_unavailable", apiErr(t, err).Code)
	list, err := gone.ListInstances(f.ctx, f.env.ID)
	require.NoError(t, err)
	require.Equal(t, "This template is no longer in the catalog.", list[0].UpdateBlocked)

	// An instance from before snapshots existed: blocked only when there is something newer.
	f.exec("UPDATE template_instances SET template_snapshot = NULL WHERE id = $1", id)
	u, err = v1.GetUpdate(f.ctx, id)
	require.NoError(t, err)
	require.Empty(t, u.Blocked, "the catalog has nothing newer")
	v2 := f.service(catalogOf(t, map[string]string{"demo": demoYAML(v2Spec())}))
	u, err = v2.GetUpdate(f.ctx, id)
	require.NoError(t, err)
	require.Contains(t, u.Blocked, "installed before template updates existed")
	require.Empty(t, u.Changes)
	_, err = v2.ApplyUpdate(f.ctx, id, templates.TemplateUpdateRequest{Revision: 2})
	require.Equal(t, "update_unavailable", apiErr(t, err).Code)
	list, err = v2.ListInstances(f.ctx, f.env.ID)
	require.NoError(t, err)
	require.Contains(t, list[0].UpdateBlocked, "installed before template updates existed")
	require.False(t, list[0].UpdateAvailable)
}

func TestTemplateUpdateReportsWhatItDoesNotDo(t *testing.T) {
	f := newTfx(t)
	v1 := f.service(catalogOf(t, map[string]string{"demo": demoYAML(demoV1())}))
	res := f.install(v1, "demo", templates.TemplateInstallRequest{})

	// Revision 2 raises the database version, which an update never does in place.
	spec := v2Spec()
	yaml2 := strings.Replace(demoYAML(spec), `version: "17"`, `version: "18"`, 1)
	cat := catalogOf(t, map[string]string{"demo": yaml2})
	v2 := f.service(cat)
	u, err := v2.GetUpdate(f.ctx, res.Instance.ID)
	require.NoError(t, err)
	c := changeOf(u, "database.version")
	require.Equal(t, templates.ChangeManual, c.Status)
	require.Equal(t, "17", c.From)
	require.Equal(t, "18", c.To)
	out, err := v2.ApplyUpdate(f.ctx, res.Instance.ID, templates.TemplateUpdateRequest{Revision: 2})
	require.NoError(t, err)
	require.Contains(t, out.Warnings[0], "change(s) are not applied by an update")
	require.Equal(t, "17", f.dbVersion(f.serviceByKey(res, "db").ID), "the database is untouched")
}

func (f *tfx) dbVersion(id uuid.UUID) string {
	f.t.Helper()
	cfg := f.cfg(id)
	require.NotNil(f.t, cfg.Database)
	return cfg.Database.Version
}

func auditMeta(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	m := map[string]any{}
	require.NoError(t, json.Unmarshal(raw, &m))
	return m
}
