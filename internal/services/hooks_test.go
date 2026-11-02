package services_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/internal/tailnet/tailnetcfg"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// ---- hooks installed at wire time ----

type recHooks struct {
	mu       sync.Mutex
	dns      []uuid.UUID
	tailnet  []uuid.UUID
	veto     error
	guarded  []uuid.UUID
	source   []string
	edges    []string
	plan     services.MeshPlan
	planErr  error
	repaired int
}

func (h *recHooks) DomainsRemoved(_ context.Context, _ pgx.Tx, ws uuid.UUID) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.dns = append(h.dns, ws)
	return nil
}

func (h *recHooks) ValidateConfig(context.Context, services.Scope, tailnetcfg.TailnetConfig) error {
	return nil
}
func (h *recHooks) ConfigChanged(context.Context, pgx.Tx, services.Scope, *tailnetcfg.TailnetConfig, *tailnetcfg.TailnetConfig) error {
	return nil
}

func (h *recHooks) ServiceDeleted(_ context.Context, _ pgx.Tx, sc services.Scope) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.tailnet = append(h.tailnet, sc.Service.ID)
	return nil
}

func (h *recHooks) BeforeDelete(_ context.Context, _ pgx.Tx, sc services.Scope) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.guarded = append(h.guarded, sc.Service.ID)
	return h.veto
}

func (h *recHooks) BeforeMove(context.Context, *db.Queries, services.Scope, *uuid.UUID) error {
	return nil
}

type namedSource struct {
	name string
	h    *recHooks
}

func (n namedSource) GitSourceChanged(_ context.Context, sc services.Scope) {
	n.h.mu.Lock()
	defer n.h.mu.Unlock()
	n.h.source = append(n.h.source, n.name+":"+sc.Config().GitBranch)
}

func (h *recHooks) PlanEdge(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (services.MeshPlan, error) {
	return h.plan, h.planErr
}

func (h *recHooks) EdgeCreated(_ context.Context, _ pgx.Tx, _, from, to uuid.UUID) error {
	h.edges = append(h.edges, "created")
	return nil
}

func (h *recHooks) EdgeDeleted(_ context.Context, _ pgx.Tx, _, from, to uuid.UUID) error {
	h.edges = append(h.edges, "deleted")
	return nil
}

func (h *recHooks) Repair(context.Context, uuid.UUID) { h.repaired++ }

// meshHook adapts recHooks (whose ServiceDeleted is the tailnet one) to the mesh
// linker, which has the same method name with another signature.
type meshHook struct{ *recHooks }

func (m meshHook) ServiceDeleted(_ context.Context, _ pgx.Tx, _, id uuid.UUID) error {
	m.edges = append(m.edges, "service-deleted:"+id.String())
	return nil
}

func TestDeletingAServiceTellsTheWiredHooks(t *testing.T) {
	f := newOpsFixture(t)
	h := &recHooks{}
	f.svcs.SetDNSHook(h)
	f.svcs.SetTailnetHook(h)
	f.svcs.SetDatabaseGuard(h)
	sv := f.deployed(f.prod.ID, "web", services.Config{Image: "traefik/whoami:latest", Port: 80})

	// The database guard may veto: nothing is deleted, the others are not asked.
	h.veto = apierr.Conflict("database_has_replicas", "a primary with replicas cannot be deleted")
	rec := f.do(f.ctx, http.MethodDelete, "/services/"+sv.ID.String()+"?confirm=web", nil)
	require.Equal(t, http.StatusConflict, rec.Code)
	require.Equal(t, "database_has_replicas", f.code(rec))
	require.Nil(t, f.deletedAt(sv.ID))
	require.Empty(t, h.dns)
	require.Empty(t, f.disp.commands(agentprotocol.KindRemoveService))

	h.veto = nil
	rec = f.do(f.ctx, http.MethodDelete, "/services/"+sv.ID.String()+"?confirm=web", nil)
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	require.Equal(t, []uuid.UUID{sv.ID, sv.ID}, h.guarded)
	require.Equal(t, []uuid.UUID{f.ws.ID}, h.dns, "the DNS owner collects the deleted domains of the team")
	require.Equal(t, []uuid.UUID{sv.ID}, h.tailnet, "the tailnet node of the service is dropped")
}

func TestGitSourceHooksRunInOrderAndOnlyWhenTheSourceChanges(t *testing.T) {
	f := newOpsFixture(t)
	h := &recHooks{}
	f.svcs.AddSourceHook(namedSource{"first", h})
	f.svcs.AddSourceHook(namedSource{"second", h})
	f.svcs.AddSourceHook(namedSource{"third", h})

	cfg := services.Config{GitURL: "https://git.example.com/acme/shop.git", GitBranch: "main", Port: 8080}
	sv, err := f.svcs.Create(f.ctx, f.prod.ID, services.CreateInput{Name: "app", SourceType: services.SourceGit, ServerID: &f.server.ID, Config: cfg})
	require.NoError(t, err)
	require.Equal(t, []string{"first:main", "second:main", "third:main"}, h.source, "a new git service tells every hook, in registration order")

	h.source = nil
	name := "renamed"
	sv, err = f.svcs.Update(f.ctx, sv.ID, sv.Generation, services.UpdateInput{Name: &name})
	require.NoError(t, err)
	require.Empty(t, h.source, "a rename is not a source change")

	next := cfg
	next.GitBranch = "release"
	sv, err = f.svcs.Update(f.ctx, sv.ID, sv.Generation, services.UpdateInput{Config: &next})
	require.NoError(t, err)
	require.Equal(t, []string{"first:release", "second:release", "third:release"}, h.source)

	h.source = nil
	port := next
	port.Port = 9090
	_, err = f.svcs.Update(f.ctx, sv.ID, sv.Generation, services.UpdateInput{Config: &port})
	require.NoError(t, err)
	require.Empty(t, h.source, "a port change does not re-read the repository")

	// An image service never calls the hook.
	_, err = f.svcs.Create(f.ctx, f.prod.ID, services.CreateInput{Name: "img", SourceType: services.SourceImage, Config: services.Config{Image: "a:1", Port: 80}})
	require.NoError(t, err)
	require.Empty(t, h.source)
}

func TestMeshLinkerIsConsultedForCrossServerLinks(t *testing.T) {
	f := newOpsFixture(t)
	h := &recHooks{plan: services.EmptyMeshPlan()}
	f.svcs.SetMeshLinker(meshHook{h})
	api := f.imageService(f.prod.ID, "api")
	pg := f.imageService(f.prod.ID, "pg")
	env := "/environments/" + f.prod.ID.String()
	body := map[string]any{"fromServiceId": api.ID, "toServiceId": pg.ID}

	// The preview carries the mesh plan of the edge.
	h.plan = services.MeshPlan{CrossServer: true, Links: []services.MeshPlanLink{}, Blockers: []services.MeshBlocker{}, DevicesAdded: 1}
	rec := f.do(f.ctx, http.MethodGet, env+"/link-proposals?from="+api.ID.String()+"&to="+pg.ID.String(), nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.True(t, decodeJSONBody[services.LinkPreview](t, rec).Mesh.CrossServer)

	// Devices that would be added need an explicit confirmation; nothing is written without it.
	rec = f.do(f.ctx, http.MethodPost, env+"/links", body)
	require.GreaterOrEqual(t, rec.Code, 400, rec.Body.String())
	require.Equal(t, services.CodeMeshConfirmationRequired, f.code(rec))
	require.Zero(t, f.count("SELECT count(*) FROM service_links"))
	require.Empty(t, h.edges)

	body["acceptMeshChanges"] = true
	rec = f.do(f.ctx, http.MethodPost, env+"/links", body)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	link := decodeJSONBody[services.LinkResult](t, rec).Link
	require.Equal(t, []string{"created"}, h.edges, "the grants open in the link's transaction")

	rec = f.do(f.ctx, http.MethodDelete, "/links/"+link.ID, nil)
	require.Equal(t, http.StatusNoContent, rec.Code)
	require.Equal(t, []string{"created", "deleted"}, h.edges)

	// A planner failure fails the preview instead of hiding the mesh.
	h.planErr = apierr.Unprocessable("mesh_unavailable", "the mesh is not configured")
	rec = f.do(f.ctx, http.MethodGet, env+"/link-proposals?from="+api.ID.String()+"&to="+pg.ID.String(), nil)
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	require.Equal(t, "mesh_unavailable", f.code(rec))

	h.planErr = nil
	rec = f.do(f.ctx, http.MethodDelete, "/services/"+api.ID.String()+"?confirm=api", nil)
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	require.Contains(t, strings.Join(h.edges, ","), "service-deleted:"+api.ID.String(), "a deleted service drops its mesh grants")
}

// ---- placement ----

func TestServicesAreNotPlacedOnServersThatCannotTakeThem(t *testing.T) {
	f := newOpsFixture(t)
	create := func(serverID uuid.UUID) *httptest.ResponseRecorder {
		return f.do(f.ctx, http.MethodPost, "/environments/"+f.prod.ID.String()+"/services", map[string]any{
			"name": "app-" + uuid.NewString()[:6], "sourceType": "image", "serverId": serverID, "config": map[string]any{"image": "a:1", "port": 80},
		})
	}
	require.Equal(t, http.StatusCreated, create(f.server.ID).Code)

	_, err := f.pool.Exec(context.Background(), "UPDATE servers SET drain_state = 'draining' WHERE id = $1", f.server.ID)
	require.NoError(t, err)
	rec := create(f.server.ID)
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	require.Equal(t, "server_draining", f.code(rec))

	_, err = f.pool.Exec(context.Background(), "UPDATE servers SET drain_state = 'none', accept_workloads = false WHERE id = $1", f.server.ID)
	require.NoError(t, err)
	rec = create(f.server.ID)
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	require.Equal(t, "server_not_accepting_workloads", f.code(rec))
	require.Contains(t, rec.Body.String(), "Accept workloads")

	_, err = f.pool.Exec(context.Background(), "UPDATE servers SET accept_workloads = true, status = 'revoked' WHERE id = $1", f.server.ID)
	require.NoError(t, err)
	rec = create(f.server.ID)
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	require.Equal(t, "server_unavailable", f.code(rec))

	require.Equal(t, http.StatusUnprocessableEntity, create(uuid.New()).Code, "an unknown server")
}

// ---- stacks ----

const okCompose = "services:\n  web:\n    image: traefik/whoami:v1.10\n    ports: [\"80:80\"]\n  db:\n    image: postgres:16\n"
const privilegedCompose = "services:\n  web:\n    image: traefik/whoami:v1.10\n    privileged: true\n"

func TestCheckStackReportsComponentsViolationsAndServerCapabilities(t *testing.T) {
	f := newOpsFixture(t)
	path := "/environments/" + f.prod.ID.String() + "/stacks/check"

	rec := f.do(f.viewer, http.MethodPost, path, map[string]any{"compose": okCompose})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	res := decodeJSONBody[services.StackCheck](t, rec)
	require.Len(t, res.Components, 2)
	require.Equal(t, "db", res.Components[0].Name)
	require.Empty(t, res.Violations)
	require.False(t, res.Unrestricted)
	require.Nil(t, res.ComposeAvailable, "without a server nothing is known about Compose")

	rec = f.do(f.viewer, http.MethodPost, path, map[string]any{"compose": privilegedCompose})
	require.Equal(t, http.StatusOK, rec.Code)
	res = decodeJSONBody[services.StackCheck](t, rec)
	require.NotEmpty(t, res.Violations, "privileged mode is not allowed on a restricted server")

	// The same file passes on an unrestricted server; the agent's facts say whether Compose exists.
	_, err := f.pool.Exec(context.Background(), "UPDATE servers SET unrestricted = true WHERE id = $1", f.server.ID)
	require.NoError(t, err)
	rec = f.do(f.viewer, http.MethodPost, path, map[string]any{"compose": privilegedCompose, "serverId": f.server.ID})
	res = decodeJSONBody[services.StackCheck](t, rec)
	require.True(t, res.Unrestricted)
	require.Empty(t, res.Violations)
	require.Nil(t, res.ComposeAvailable, "the agent has not reported facts yet")

	for caps, want := range map[string]bool{`["` + agentprotocol.CapCompose + `"]`: true, `[]`: false} {
		_, err = f.pool.Exec(context.Background(), `UPDATE servers SET facts = $2 WHERE id = $1`, f.server.ID, `{"os":"linux","capabilities":`+caps+`}`)
		require.NoError(t, err)
		rec = f.do(f.viewer, http.MethodPost, path, map[string]any{"compose": okCompose, "serverId": f.server.ID})
		res = decodeJSONBody[services.StackCheck](t, rec)
		require.NotNil(t, res.ComposeAvailable, caps)
		require.Equal(t, want, *res.ComposeAvailable, caps)
	}

	rec = f.do(f.viewer, http.MethodPost, path, map[string]any{"compose": "services: [oops"})
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	require.Equal(t, "invalid_compose", f.code(rec))
	rec = f.do(f.viewer, http.MethodPost, path, map[string]any{"compose": okCompose, "serverId": uuid.New()})
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	require.Equal(t, "server_not_found", f.code(rec))
	require.Equal(t, http.StatusNotFound, f.do(f.viewer, http.MethodPost, "/environments/"+uuid.NewString()+"/stacks/check", map[string]any{"compose": okCompose}).Code)
}

func TestStacksAreCheckedWhenCreatedAndTheirComponentsDerived(t *testing.T) {
	f := newOpsFixture(t)
	path := "/environments/" + f.prod.ID.String() + "/services"

	rec := f.do(f.ctx, http.MethodPost, path, map[string]any{
		"name": "bad", "kind": "stack", "sourceType": "compose", "serverId": f.server.ID, "config": map[string]any{"stack": map[string]any{"compose": privilegedCompose}},
	})
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
	require.Equal(t, "invalid_compose", f.code(rec))
	require.Contains(t, rec.Body.String(), "violations")
	require.Contains(t, rec.Body.String(), "Unrestricted", "it says how an admin can allow it")

	rec = f.do(f.ctx, http.MethodPost, path, map[string]any{
		"name": "good", "kind": "stack", "sourceType": "compose", "serverId": f.server.ID, "config": map[string]any{"stack": map[string]any{"compose": okCompose}},
	})
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	v := decodeView(t, rec)
	require.Equal(t, services.KindStack, v.Kind)
	require.Len(t, v.Config.Stack.Components, 2, "the server derives the components from the compose file")

	// A domain on a component keeps the component: dropping it from the file is refused.
	_, err := f.pool.Exec(context.Background(), `INSERT INTO domains (service_id, hostname, target_port, component) VALUES ($1, 'db.example.com', 5432, 'db')`, v.ID)
	require.NoError(t, err)
	onlyWeb := "services:\n  web:\n    image: traefik/whoami:v1.10\n"
	rec = f.do(f.ctx, http.MethodPatch, "/services/"+v.ID.String(), map[string]any{"config": map[string]any{"stack": map[string]any{"compose": onlyWeb}}}, "If-Match", `"1"`)
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
	require.Equal(t, "component_in_use", f.code(rec))
	require.Contains(t, rec.Body.String(), "db.example.com")
}

func TestStackHelpers(t *testing.T) {
	f := newOpsFixture(t)

	p, err := services.CheckStackCompose(f.ctx, f.q, okCompose, false)
	require.NoError(t, err)
	require.Len(t, p.Components(), 2)
	_, err = services.CheckStackCompose(f.ctx, f.q, privilegedCompose, false)
	requireCode(t, err, "invalid_compose")
	_, err = services.CheckStackCompose(f.ctx, f.q, privilegedCompose, true)
	require.NoError(t, err, "unrestricted lifts the restricted rules")
	_, err = services.CheckStackCompose(f.ctx, f.q, "{", false)
	requireCode(t, err, "invalid_compose")

	ae := services.InvalidComposeError(nil, nil)
	require.Equal(t, "invalid_compose", ae.Code)
	require.Equal(t, []any{}, jsonRoundTrip(t, ae.Details["violations"]), "details always carry a list")
	require.Contains(t, services.InvalidComposeError(assertErr("boom"), nil).Message, "the compose file is not valid: boom")
	require.True(t, services.StackPolicy(true).Unrestricted)
	require.False(t, services.StackPolicy(false).Unrestricted)

	require.Equal(t, okCompose, services.DeriveStack(services.Config{Stack: &services.StackConfig{Compose: okCompose}}).Stack.Compose)
	require.Len(t, services.DeriveStack(services.Config{Stack: &services.StackConfig{Compose: okCompose}}).Stack.Components, 2)
	broken := services.Config{Stack: &services.StackConfig{Compose: "{"}}
	require.Empty(t, services.DeriveStack(broken).Stack.Components, "an unparsable file is left as it is")
	require.Nil(t, services.DeriveStack(services.Config{}).Stack)

	facts, known := services.ServerFacts(db.Server{})
	require.False(t, known)
	require.Empty(t, facts.OS)
	_, known = services.ServerFacts(db.Server{Facts: []byte(`{"os":""}`)})
	require.False(t, known, "facts without an OS are not facts")
	_, known = services.ServerFacts(db.Server{Facts: []byte(`nope`)})
	require.False(t, known)
	has, known := services.ServerHasCapability(db.Server{Facts: []byte(`{"os":"linux","capabilities":["x"]}`)}, "x")
	require.True(t, has)
	require.True(t, known)
	has, known = services.ServerHasCapability(db.Server{}, "x")
	require.False(t, has)
	require.False(t, known)
}

type assertErr string

func (e assertErr) Error() string { return string(e) }

func jsonRoundTrip(t *testing.T, v any) any {
	t.Helper()
	raw, err := json.Marshal(v)
	require.NoError(t, err)
	var out any
	require.NoError(t, json.Unmarshal(raw, &out))
	return out
}

// ---- applied release, tailnet view, networks ----

func TestAppliedReleaseSubstitutesTheRunningConfiguration(t *testing.T) {
	f := newOpsFixture(t)
	sv := f.deployed(f.prod.ID, "web", services.Config{Image: "traefik/whoami:v1", Port: 80})
	never := f.imageService(f.prod.ID, "idle")

	rel, ok, err := services.AppliedRelease(f.ctx, f.q, sv)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "traefik/whoami:v1", rel.ImageRef)
	_, ok, err = services.AppliedRelease(f.ctx, f.q, never)
	require.NoError(t, err)
	require.False(t, ok, "a service that never ran has no release")

	// Stage an edit: the live config moves on, the applied scope keeps the running one.
	cfg := services.DecodeConfig(sv.Config)
	cfg.Image = "traefik/whoami:v2"
	_, err = f.svcs.Update(f.ctx, sv.ID, sv.Generation, services.UpdateInput{Config: &cfg})
	require.NoError(t, err)
	sc, err := services.LoadScope(f.ctx, f.q, "projects:read", sv.ID)
	require.NoError(t, err)
	require.Equal(t, "traefik/whoami:v2", sc.Config().Image)
	applied, _, ok, err := services.AppliedScope(f.ctx, f.q, sc)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "traefik/whoami:v1", applied.Config().Image)
	require.Equal(t, sc.Service.ID, applied.Service.ID)

	idle, err := services.LoadScope(f.ctx, f.q, "projects:read", never.ID)
	require.NoError(t, err)
	same, _, ok, err := services.AppliedScope(f.ctx, f.q, idle)
	require.NoError(t, err)
	require.False(t, ok)
	require.Equal(t, idle.Service.ID, same.Service.ID, "without a release the scope is returned unchanged")

	prod := sc
	prod.IsProduction = true
	require.True(t, prod.ProductionHasStaged(), "a production service with staged edits must not ship them on a push")
	prod.Service.AppliedGeneration = prod.Service.Generation
	require.False(t, prod.ProductionHasStaged())
	prod.Service.AppliedGeneration = 0
	prod.IsProduction = false
	require.False(t, prod.ProductionHasStaged())
}

func TestTailnetViewAndDecoration(t *testing.T) {
	f := newOpsFixture(t)
	sv := f.deployed(f.prod.ID, "web", services.Config{Image: "traefik/whoami:v1", Port: 80})
	applied, err := json.Marshal(agentprotocol.ServiceTailnetSpec{Mode: agentprotocol.TailnetEnsure, ServiceID: sv.ID.String(), Web: true})
	require.NoError(t, err)
	_, err = f.pool.Exec(context.Background(), `INSERT INTO service_tailnet (service_id, workspace_id, provider, hostname, fqdn, ips, state, applied, funnel, applied_hide_public, observed_at)
		VALUES ($1, $2, 'tailscale', 'web', 'web.tail1234.ts.net', ARRAY['100.64.0.9'], 'online', $3, true, true, now())`, sv.ID, f.ws.ID, applied)
	require.NoError(t, err)

	rec := f.do(f.viewer, http.MethodGet, "/services/"+sv.ID.String(), nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	tv := decodeView(t, rec).Tailnet
	require.NotNil(t, tv)
	require.Equal(t, "online", tv.State)
	require.Equal(t, "web.tail1234.ts.net", tv.FQDN)
	require.Equal(t, []string{"100.64.0.9"}, tv.IPs)
	require.True(t, tv.Web)
	require.True(t, tv.Funnel)
	require.True(t, tv.HidePublic, "it follows the applied configuration")
	require.Equal(t, "https://web.tail1234.ts.net", tv.URL)

	// Headscale has no HTTPS by default; without a web exposure or a name there is no URL.
	_, err = f.pool.Exec(context.Background(), `UPDATE service_tailnet SET provider = 'headscale' WHERE service_id = $1`, sv.ID)
	require.NoError(t, err)
	require.Equal(t, "http://web.tail1234.ts.net", decodeView(t, f.do(f.viewer, http.MethodGet, "/services/"+sv.ID.String(), nil)).Tailnet.URL)
	_, err = f.pool.Exec(context.Background(), `UPDATE service_tailnet SET applied = '{}', ips = '{}' WHERE service_id = $1`, sv.ID)
	require.NoError(t, err)
	tv = decodeView(t, f.do(f.viewer, http.MethodGet, "/services/"+sv.ID.String(), nil)).Tailnet
	require.Empty(t, tv.URL)
	require.False(t, tv.Web)
	require.Equal(t, []string{}, tv.IPs, "no addresses is an empty list")
	require.Equal(t, []tailnetcfg.TCPForward{}, tv.TCP)

	other := f.imageService(f.prod.ID, "plain")
	require.Nil(t, decodeView(t, f.do(f.viewer, http.MethodGet, "/services/"+other.ID.String(), nil)).Tailnet, "a service off the tailnet has no tailnet view")
}

func TestNetworkErrorsAndEgressCounting(t *testing.T) {
	f := newOpsFixture(t)

	ae := services.NetworkConflict()
	require.Equal(t, "egress_blocked", ae.Code)
	require.Equal(t, http.StatusUnprocessableEntity, ae.Status)
	wrong := services.WrongServer("api", "backend")
	require.Equal(t, "network_wrong_server", wrong.Code)
	require.Contains(t, wrong.Message, `"backend"`)

	web := f.deployed(f.prod.ID, "web", services.Config{Image: "a:1", Port: 80})
	blocked := f.deployed(f.prod.ID, "blocked", services.Config{Image: "a:1", Port: 80, BlockEgress: true})

	has, err := services.EnvHasEgressBlocked(f.ctx, f.q, f.prod.ID, f.server.ID, web.ID, true)
	require.NoError(t, err)
	require.True(t, has, "the service's own setting counts without a query")
	has, err = services.EnvHasEgressBlocked(f.ctx, f.q, f.prod.ID, f.server.ID, web.ID, false)
	require.NoError(t, err)
	require.True(t, has, "another service of the environment on the server blocks egress")
	has, err = services.EnvHasEgressBlocked(f.ctx, f.q, f.prod.ID, f.server.ID, blocked.ID, false)
	require.NoError(t, err)
	require.False(t, has, "the blocking service itself is excluded")
	has, err = services.EnvHasEgressBlocked(f.ctx, f.q, f.staging.ID, f.server.ID, web.ID, false)
	require.NoError(t, err)
	require.False(t, has, "another environment is not affected")

	nets, err := services.ListNetworks(f.ctx, f.q, nil)
	require.NoError(t, err)
	require.Empty(t, nets)
	nets, err = services.ListNetworks(f.ctx, f.q, []uuid.UUID{web.ID})
	require.NoError(t, err)
	require.Empty(t, nets[web.ID])
}

// ---- views of running services ----

func (f *opsFixture) event(depID uuid.UUID, step, status string) {
	f.t.Helper()
	_, err := f.pool.Exec(context.Background(), `INSERT INTO deployment_events (deployment_id, step, status) VALUES ($1, $2, $3)`, depID, step, status)
	require.NoError(f.t, err)
}

func TestServiceListShowsVolumesDeploymentStepsAndStagedChanges(t *testing.T) {
	f := newOpsFixture(t)
	bg := context.Background()
	running := f.deployed(f.prod.ID, "running", services.Config{Image: "traefik/whoami:v1", Port: 80})
	_, err := f.pool.Exec(bg, `INSERT INTO volumes (service_id, name, mount_path) VALUES ($1, 'data', '/data')`, running.ID)
	require.NoError(t, err)

	// A service whose newest deployment failed names the step that failed.
	failed := f.imageService(f.prod.ID, "failing")
	fd, err := f.q.InsertDeployment(bg, db.InsertDeploymentParams{ServiceID: failed.ID, ServerID: &f.server.ID, Trigger: "manual", ActorType: "user", Strategy: "rolling"})
	require.NoError(t, err)
	f.event(fd.ID, "build", "started")
	f.event(fd.ID, "pull", "failed")
	f.event(fd.ID, "status", "failed")
	_, err = f.pool.Exec(bg, `UPDATE deployments SET status = 'failed', finished_at = now() WHERE id = $1`, fd.ID)
	require.NoError(t, err)

	// A service with a deployment in flight shows the step it is on.
	busy := f.imageService(f.prod.ID, "busy")
	bd, err := f.q.InsertDeployment(bg, db.InsertDeploymentParams{ServiceID: busy.ID, ServerID: &f.server.ID, Trigger: "manual", ActorType: "user", Strategy: "rolling"})
	require.NoError(t, err)
	f.event(bd.ID, "pull", "succeeded")
	f.event(bd.ID, "predeploy", "started")
	_, err = f.pool.Exec(bg, `UPDATE deployments SET status = 'releasing' WHERE id = $1`, bd.ID)
	require.NoError(t, err)

	// A deployed service with a staged edit counts it; a stopped one reports its desired state.
	cfg := services.DecodeConfig(running.Config)
	cfg.MemoryMB = 512
	_, err = f.svcs.Update(f.ctx, running.ID, running.Generation, services.UpdateInput{Config: &cfg})
	require.NoError(t, err)
	stopped := f.deployed(f.prod.ID, "stopped", services.Config{Image: "traefik/whoami:v1", Port: 80})
	_, err = f.svcs.Stop(f.ctx, stopped.ID)
	require.NoError(t, err)

	rec := f.do(f.viewer, http.MethodGet, "/environments/"+f.prod.ID.String()+"/services", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	byName := map[string]services.ServiceView{}
	for _, v := range decodeJSONBody[api2Page](t, rec).Items {
		byName[v.Name] = v
	}
	require.Len(t, byName, 4)

	r := byName["running"]
	require.Len(t, r.Volumes, 1)
	require.Equal(t, "/data", r.Volumes[0].MountPath)
	require.Equal(t, services.DesiredRunning, r.DesiredState)
	require.NotNil(t, r.StagedKind)
	require.Equal(t, services.StagedEdited, *r.StagedKind)
	require.Zero(t, r.PendingChangeCounts.Variables)
	require.GreaterOrEqual(t, r.PendingChangeCounts.Settings, 2, "the memory edit and the volume attached since the release are both staged")
	require.Empty(t, byName["failing"].Volumes, "no volumes is an empty list")

	require.NotNil(t, byName["failing"].LatestDeployment)
	require.NotNil(t, byName["failing"].LatestDeployment.FailedStep)
	require.Equal(t, "pull", *byName["failing"].LatestDeployment.FailedStep)
	require.NotNil(t, byName["busy"].LatestDeployment)
	require.NotNil(t, byName["busy"].LatestDeployment.CurrentStep)
	require.Equal(t, "predeploy", *byName["busy"].LatestDeployment.CurrentStep)
	require.Nil(t, byName["busy"].LatestDeployment.FailedStep)
	require.Equal(t, services.DesiredStopped, byName["stopped"].DesiredState)

	// A never-deployed service is staged as new.
	require.NotNil(t, byName["failing"].StagedKind)
	require.Equal(t, services.StagedNew, *byName["failing"].StagedKind)

	// Paging by cursor keeps the order.
	page := decodeJSONBody[api2Page](t, f.do(f.viewer, http.MethodGet, "/environments/"+f.prod.ID.String()+"/services?limit=3", nil))
	require.Len(t, page.Items, 3)
	require.NotEmpty(t, page.NextCursor)
	rest := decodeJSONBody[api2Page](t, f.do(f.viewer, http.MethodGet, "/environments/"+f.prod.ID.String()+"/services?limit=3&cursor="+page.NextCursor, nil))
	require.Len(t, rest.Items, 1)
	require.Empty(t, rest.NextCursor)
	require.Equal(t, http.StatusBadRequest, f.do(f.viewer, http.MethodGet, "/environments/"+f.prod.ID.String()+"/services?cursor=garbage", nil).Code)
}

type api2Page struct {
	Items      []services.ServiceView `json:"items"`
	NextCursor string                 `json:"nextCursor"`
}

// ---- log files ----

func TestLogDownloadWritesTextAndNDJSON(t *testing.T) {
	at := time.Date(2025, 3, 4, 5, 6, 7, 123, time.UTC)
	replica := 2

	var txt bytes.Buffer
	d := services.NewLogDownloadTo(&txt, services.DownloadText)
	require.NoError(t, d.Write(services.DownloadLine{TS: at, Line: "hello\r\n"}))
	require.NoError(t, d.Write(services.DownloadLine{TS: at, Stream: "build", Line: "compiling"}))
	require.NoError(t, d.Write(services.DownloadLine{TS: at, Stream: "runtime", Line: "running"}))
	d.Note("range cut at 3 lines")
	require.Equal(t, 3, d.Lines())
	require.Equal(t, strings.Join([]string{
		"2025-03-04T05:06:07.000000123Z hello",
		"2025-03-04T05:06:07.000000123Z [build] compiling",
		"2025-03-04T05:06:07.000000123Z running",
		"# range cut at 3 lines",
	}, "\n")+"\n", txt.String(), "the runtime stream is the default and is not labelled")
	require.False(t, d.Full())

	var nd bytes.Buffer
	d = services.NewLogDownloadTo(&nd, services.DownloadNDJSON)
	require.NoError(t, d.Write(services.DownloadLine{TS: at.In(time.FixedZone("x", 3600)), Stream: "runtime", Replica: &replica, DeploymentID: "dep-1", Line: "line\n"}))
	d.Note("cut")
	lines := strings.Split(strings.TrimSpace(nd.String()), "\n")
	require.Len(t, lines, 2)
	require.JSONEq(t, `{"ts":"2025-03-04T05:06:07.000000123Z","stream":"runtime","replica":2,"deploymentId":"dep-1","line":"line"}`, lines[0], "timestamps are UTC, lines are trimmed")
	require.JSONEq(t, `{"note":"cut","lines":1}`, lines[1])

	require.Equal(t, "web-logs-20250304T050607Z.txt", services.DownloadFilename("web", "logs", at, "txt"))
	for in, want := range map[string]string{"": services.DownloadText, "TXT": services.DownloadText, " ndjson ": services.DownloadNDJSON} {
		got, err := services.ParseDownloadFormat(in)
		require.NoError(t, err, in)
		require.Equal(t, want, got, in)
	}
	_, err := services.ParseDownloadFormat("csv")
	requireCode(t, err, "validation_failed")
	require.Contains(t, services.DownloadResponses()["200"].Content, "application/x-ndjson")
}

func TestLogDownloadResponseCarriesFileHeaders(t *testing.T) {
	r := chi.NewRouter()
	cfg := huma.DefaultConfig("t", "1")
	cfg.CreateHooks = nil
	a := humachi.New(r, cfg)
	huma.Register(a, huma.Operation{OperationID: "dl", Method: http.MethodGet, Path: "/dl", Responses: services.DownloadResponses()},
		func(context.Context, *struct{}) (*huma.StreamResponse, error) {
			return &huma.StreamResponse{Body: func(hctx huma.Context) {
				d := services.NewLogDownload(hctx, services.DownloadNDJSON, "web-logs.ndjson")
				_ = d.Write(services.DownloadLine{TS: time.Unix(0, 0), Line: "x"})
				d.Note("end")
			}}, nil
		})
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/dl", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "application/x-ndjson", rec.Header().Get("Content-Type"))
	require.Equal(t, `attachment; filename="web-logs.ndjson"`, rec.Header().Get("Content-Disposition"))
	require.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	require.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"))
	require.Contains(t, rec.Body.String(), `"line":"x"`)
}
