package composites_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agents"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/databases"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/deployments"
	"github.com/nebulactrl/nebulactrl/internal/domains"
	"github.com/nebulactrl/nebulactrl/internal/environments"
	"github.com/nebulactrl/nebulactrl/internal/events"
	"github.com/nebulactrl/nebulactrl/internal/jobs"
	"github.com/nebulactrl/nebulactrl/internal/mcp/composites"
	"github.com/nebulactrl/nebulactrl/internal/mcp/mcpkit"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/projects"
	"github.com/nebulactrl/nebulactrl/internal/proxyroutes"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/servers"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/internal/testutil"
	"github.com/nebulactrl/nebulactrl/internal/variables"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// fakeAgents records enqueued commands and serves canned runtime log lines.
// Setting logErr makes the log subscription fail (a partial-failure case).
type fakeAgents struct {
	mu     sync.Mutex
	cmds   []agents.NewCommand
	subs   []agentprotocol.LogsSubscribe
	lines  map[string][]string // serviceID → runtime lines
	logErr error
}

// logRequests returns the runtime log subscriptions the control plane opened.
func (f *fakeAgents) logRequests() []agentprotocol.LogsSubscribe {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]agentprotocol.LogsSubscribe(nil), f.subs...)
}

func (f *fakeAgents) Enqueue(_ context.Context, _ pgx.Tx, c agents.NewCommand) (uuid.UUID, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cmds = append(f.cmds, c)
	return uuid.New(), nil
}

func (f *fakeAgents) SubscribeLogs(_ context.Context, _ uuid.UUID, req agentprotocol.LogsSubscribe) (<-chan agentprotocol.LogFrame, func(), error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.subs = append(f.subs, req)
	if f.logErr != nil {
		return nil, nil, f.logErr
	}
	lines := f.lines[req.ServiceID]
	if req.Tail > 0 && len(lines) > req.Tail {
		lines = lines[len(lines)-req.Tail:]
	}
	ch := make(chan agentprotocol.LogFrame, 1)
	fr := agentprotocol.LogFrame{StreamID: req.StreamID, ServiceID: req.ServiceID, Stream: agentprotocol.StreamRuntime}
	base := time.Now().Add(-time.Duration(len(lines)) * time.Second)
	for i, l := range lines {
		fr.Lines = append(fr.Lines, agentprotocol.LogLine{TS: base.Add(time.Duration(i) * time.Second), Text: l})
	}
	ch <- fr
	close(ch)
	return ch, func() {}, nil
}

func (f *fakeAgents) commands(kind string) []agents.NewCommand {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []agents.NewCommand
	for _, c := range f.cmds {
		if c.Kind == kind {
			out = append(out, c)
		}
	}
	return out
}

// fx is a control plane's service graph over a fresh schema with one team,
// one project (production and staging environments), an online server and a
// full-scope MCP principal.
type fx struct {
	t       *testing.T
	pool    *pgxpool.Pool
	q       *db.Queries
	ag      *fakeAgents
	deps    composites.Deps
	box     *secretbox.Box
	tools   map[string]mcpkit.Tool
	ws      db.Workspace
	user    db.CreateUserRow
	project db.Project
	prod    db.Environment
	staging db.Environment
	server  db.Server
	// admin is the principal tests call with unless they build another.
	admin *auth.Principal
}

func newFx(t *testing.T) *fx {
	t.Helper()
	pool := testutil.NewPool(t)
	q := db.New(pool)
	bg := context.Background()
	ws, err := q.CreateWorkspace(bg, "w")
	require.NoError(t, err)
	u, err := q.CreateUser(bg, db.CreateUserParams{WorkspaceID: ws.ID, Email: "a@example.com", Name: "A", PasswordHash: "x"})
	require.NoError(t, err)
	prj, err := q.CreateProject(bg, db.CreateProjectParams{WorkspaceID: ws.ID, Name: "Shop", Slug: "shop"})
	require.NoError(t, err)
	prod, err := q.CreateEnvironment(bg, db.CreateEnvironmentParams{ProjectID: prj.ID, Name: "production", Slug: "production", IsProduction: true})
	require.NoError(t, err)
	stg, err := q.CreateEnvironment(bg, db.CreateEnvironmentParams{ProjectID: prj.ID, Name: "staging", Slug: "staging"})
	require.NoError(t, err)
	base := "apps.example.com"
	ip := "203.0.113.7"
	srv, err := q.CreateServer(bg, db.CreateServerParams{WorkspaceID: ws.ID, Name: "agent1", PublicIp: &ip, BaseDomain: &base})
	require.NoError(t, err)
	facts, _ := json.Marshal(agentprotocol.Facts{OS: "linux", Capabilities: []string{agentprotocol.CapProxyV2}})
	_, err = pool.Exec(bg, "UPDATE servers SET status = 'online', facts = $2, last_seen_at = now() WHERE id = $1", srv.ID, facts)
	require.NoError(t, err)
	srv, err = q.LoadServerRow(bg, db.LoadServerRowParams{ID: srv.ID})
	require.NoError(t, err)

	box, err := secretbox.New(bytes.Repeat([]byte{7}, 32))
	require.NoError(t, err)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	bus := events.NewBus()
	jm := jobs.New(pool, logger)
	ag := &fakeAgents{lines: map[string][]string{}}
	svcs := services.NewService(pool, bus, box, ag, logger)
	vars := variables.NewService(pool, bus, box)
	doms := domains.NewService(pool, bus, jm, box)
	domains.RegisterJobs(jm, doms, logger)
	routes := proxyroutes.NewService(pool, bus, jm, ag, svcs, logger)
	svcs.SetRouteHook(routes)
	dep := deployments.NewService(deployments.Deps{Pool: pool, Bus: bus, Jobs: jm, Box: box, Dispatcher: ag, Services: svcs, Logger: logger})
	deployments.RegisterJobs(jm, dep)
	dbs := databases.NewService(pool, bus, box, ag, svcs, logger)

	f := &fx{t: t, pool: pool, q: q, ag: ag, box: box, ws: ws, user: u, project: prj, prod: prod, staging: stg, server: srv}
	f.deps = composites.Deps{
		Pool: pool, Logger: logger,
		Projects: projects.NewService(pool, bus), Environments: environments.NewService(pool, bus),
		Servers:  servers.NewService(pool, bus, nil, nil, logger),
		Services: svcs, Deployments: dep, Variables: vars, Domains: doms, Databases: dbs, Audit: audit.NewService(pool),
	}
	f.admin = f.principal(policy.AllScopes...)
	f.tools = map[string]mcpkit.Tool{}
	for _, tl := range composites.Tools(f.deps) {
		f.tools[tl.Name] = tl
	}
	return f
}

// principal is an MCP principal of the team holding scopes.
func (f *fx) principal(scopes ...string) *auth.Principal {
	return &auth.Principal{Type: auth.TypeMCP, UserID: f.user.ID, WorkspaceID: f.ws.ID, Role: auth.RoleOwner, Label: "mcp-test", Scopes: scopes}
}

// ctx carries p the way the MCP layer does.
func (f *fx) ctx(p *auth.Principal) context.Context {
	return audit.WithActorType(auth.WithPrincipal(context.Background(), p), audit.ActorMCP)
}

// call invokes a composite as p and returns its structured result, or the
// error the tool returned.
func (f *fx) call(p *auth.Principal, tool string, args any) (map[string]any, error) {
	f.t.Helper()
	tl, ok := f.tools[tool]
	require.True(f.t, ok, "no composite %q", tool)
	for _, sc := range tl.Scopes {
		require.True(f.t, policy.HasScope(p, sc), "principal lacks %s required to list %s", sc, tool)
	}
	raw, err := json.Marshal(args)
	require.NoError(f.t, err)
	out, err := tl.Call(f.ctx(p), p, raw)
	if err != nil {
		return nil, err
	}
	b, err := json.Marshal(out)
	require.NoError(f.t, err)
	var m map[string]any
	require.NoError(f.t, json.Unmarshal(b, &m), string(b))
	return m, nil
}

// must is call that fails the test on a tool error.
func (f *fx) must(tool string, args any) map[string]any {
	f.t.Helper()
	m, err := f.call(f.admin, tool, args)
	require.NoError(f.t, err)
	return m
}

// sectionErrors returns the section names listed in a result's errors[].
func sectionErrors(m map[string]any) []string {
	var out []string
	errs, _ := m["errors"].([]any)
	for _, e := range errs {
		out = append(out, e.(map[string]any)["section"].(string))
	}
	return out
}

// errCode returns the code of a tool error.
func errCode(err error) string {
	var te *mcpkit.Error
	if err != nil && errors.As(err, &te) {
		return te.Code
	}
	return ""
}

// ---- seeding ----

type svcOpts struct {
	env    db.Environment
	slug   string
	kind   string
	image  string
	port   int
	server *db.Server
	// processes are the additional named processes of the service.
	processes []services.ProcessConfig
	// undeployed leaves the service without a deployment.
	undeployed bool
}

// service inserts a service. Unless undeployed, it also has a release and a
// succeeded deployment, applied_generation == generation, and a healthy
// observed state.
func (f *fx) service(o svcOpts) db.Service {
	f.t.Helper()
	bg := context.Background()
	if o.kind == "" {
		o.kind = services.KindApp
	}
	if o.image == "" {
		o.image = "traefik/whoami:latest"
	}
	if o.port == 0 && o.kind != services.KindDatabase {
		o.port = 80
	}
	srv := o.server
	if srv == nil {
		srv = &f.server
	}
	raw, _ := json.Marshal(services.Config{Image: o.image, Port: o.port, Processes: o.processes}.WithDefaults())
	sv, err := f.q.InsertService(bg, db.InsertServiceParams{
		ID: uuid.New(), EnvironmentID: o.env.ID, Name: o.slug, Slug: o.slug, Kind: o.kind,
		SourceType: services.SourceImage, Config: raw, ServerID: &srv.ID,
	})
	require.NoError(f.t, err)
	if o.undeployed {
		return sv
	}
	dep := f.release(sv, o.image, "succeeded", "")
	require.NoError(f.t, f.q.MarkServiceDeployed(bg, db.MarkServiceDeployedParams{ID: sv.ID, CurrentDeploymentID: &dep.ID, AppliedGeneration: sv.Generation}))
	obs, _ := json.Marshal(map[string]any{"state": "running", "health": "healthy", "containerId": "abc", "deploymentId": dep.ID.String()})
	_, err = f.pool.Exec(bg, "UPDATE services SET observed = $2, observed_at = now() WHERE id = $1", sv.ID, obs)
	require.NoError(f.t, err)
	sv, err = f.reload(sv.ID)
	require.NoError(f.t, err)
	return sv
}

// release inserts a release of image plus a deployment with the given status
// (and status message). The deployment's service is sv.
func (f *fx) release(sv db.Service, image, status, message string) db.Deployment {
	f.t.Helper()
	bg := context.Background()
	snap, _ := json.Marshal(services.Snapshot{Config: services.DecodeConfig(sv.Config), ServerID: sv.ServerID})
	src, _ := json.Marshal(services.ReleaseSource{Type: services.SourceImage, Image: image})
	rel, err := f.q.InsertRelease(bg, db.InsertReleaseParams{ID: uuid.New(), ServiceID: sv.ID, Source: src, Config: snap, ImageRef: image})
	require.NoError(f.t, err)
	dep, err := f.q.InsertDeployment(bg, db.InsertDeploymentParams{
		ServiceID: sv.ID, ReleaseID: &rel.ID, ServerID: sv.ServerID, Trigger: "manual", ActorType: "user", Strategy: "rolling", Generation: sv.Generation,
	})
	require.NoError(f.t, err)
	_, err = f.pool.Exec(bg, "UPDATE deployments SET status = $2, status_message = $3, finished_at = CASE WHEN $2 IN ('succeeded','failed','canceled','rolled_back','rollback_failed') THEN now() ELSE NULL END WHERE id = $1", dep.ID, status, message)
	require.NoError(f.t, err)
	return dep
}

// event appends a deployment timeline event.
func (f *fx) event(dep uuid.UUID, step, status, msg string) {
	f.t.Helper()
	require.NoError(f.t, f.q.InsertDeploymentEvent(context.Background(), db.InsertDeploymentEventParams{DeploymentID: dep, Step: step, Status: status, Message: msg}))
}

// logLine appends a persisted build/deploy/runtime/debug log line.
func (f *fx) logLine(dep uuid.UUID, stream, line string) {
	f.t.Helper()
	_, err := f.pool.Exec(context.Background(), "INSERT INTO deployment_logs (deployment_id, stream, line) VALUES ($1, $2, $3)", dep, stream, line)
	require.NoError(f.t, err)
}

// stage stages a real edit (one variable) so the service differs from its
// release: a bare generation bump is not a staged change (a diff-free service
// is not reported as staged).
func (f *fx) stage(id uuid.UUID) {
	f.t.Helper()
	chgPut(f, id, "STAGED_"+strings.ToUpper(strings.ReplaceAll(uuid.NewString()[:8], "-", "")), "1", false)
}

// setObserved sets the observed runtime state.
func (f *fx) setObserved(id uuid.UUID, state, health string) {
	f.t.Helper()
	obs, _ := json.Marshal(map[string]any{"state": state, "health": health, "containerId": "abc"})
	_, err := f.pool.Exec(context.Background(), "UPDATE services SET observed = $2, observed_at = now() WHERE id = $1", id, obs)
	require.NoError(f.t, err)
}

// offlineServer inserts a second server that is offline.
func (f *fx) offlineServer(name string) db.Server {
	f.t.Helper()
	bg := context.Background()
	srv, err := f.q.CreateServer(bg, db.CreateServerParams{WorkspaceID: f.ws.ID, Name: name})
	require.NoError(f.t, err)
	_, err = f.pool.Exec(bg, "UPDATE servers SET status = 'offline', last_seen_at = now() - interval '2 hours' WHERE id = $1", srv.ID)
	require.NoError(f.t, err)
	srv, err = f.q.LoadServerRow(bg, db.LoadServerRowParams{ID: srv.ID})
	require.NoError(f.t, err)
	return srv
}

func (f *fx) selector(env db.Environment, slug string) string {
	return f.project.Slug + "/" + env.Slug + "/" + slug
}

// reload reads a service row.
func (f *fx) reload(id uuid.UUID) (db.Service, error) {
	sc, err := services.LoadScopeInternal(context.Background(), f.q, id)
	if err != nil {
		return db.Service{}, err
	}
	return sc.Service, nil
}
