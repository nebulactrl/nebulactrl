package firewall_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/api"
	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/domains"
	"github.com/nebulactrl/nebulactrl/internal/events"
	"github.com/nebulactrl/nebulactrl/internal/firewall"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/internal/testutil"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// fakePusher records the servers PushRoutes was called for, and the firewall
// column of serviceID as the pushing transaction sees it.
type fakePusher struct {
	servers   []uuid.UUID
	serviceID uuid.UUID
	seen      [][]byte
	err       error
}

func (f *fakePusher) PushRoutes(ctx context.Context, tx pgx.Tx, serverID uuid.UUID) error {
	f.servers = append(f.servers, serverID)
	var raw []byte
	if err := tx.QueryRow(ctx, "SELECT firewall FROM services WHERE id = $1", f.serviceID).Scan(&raw); err != nil {
		return err
	}
	f.seen = append(f.seen, raw)
	return f.err
}

type fixture struct {
	t       *testing.T
	pool    *pgxpool.Pool
	q       *db.Queries
	ctx     context.Context
	reader  context.Context
	noProxy context.Context
	svc     *firewall.Service
	push    *fakePusher
	env     db.Environment
	server  db.Server
	bus     *events.Bus
	ws      db.Workspace
	userID  uuid.UUID
}

func newFixture(t *testing.T) *fixture {
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
	env, err := q.CreateEnvironment(bg, db.CreateEnvironmentParams{ProjectID: prj.ID, Name: "production", Slug: "production", IsProduction: true})
	require.NoError(t, err)
	srv, err := q.CreateServer(bg, db.CreateServerParams{WorkspaceID: ws.ID, Name: "agent1"})
	require.NoError(t, err)
	_, err = pool.Exec(bg, "UPDATE servers SET status = 'online' WHERE id = $1", srv.ID)
	require.NoError(t, err)

	bus := events.NewBus()
	push := &fakePusher{}
	p := func(scopes []string) context.Context {
		return auth.WithPrincipal(bg, &auth.Principal{Type: auth.TypeUser, UserID: u.ID, WorkspaceID: ws.ID, Label: "alice@example.com", Scopes: scopes})
	}
	return &fixture{
		t: t, pool: pool, q: q, env: env, server: srv, bus: bus, push: push, ws: ws, userID: u.ID,
		ctx:     p(policy.AllScopes),
		reader:  p([]string{policy.ScopeProjectsRead}),
		noProxy: p([]string{policy.ScopeProjectsRead, policy.ScopeProjectsWrite}),
		svc:     firewall.NewService(pool, bus, push, slog.New(slog.NewTextHandler(io.Discard, nil))),
	}
}

func (f *fixture) exec(sql string, args ...any) {
	f.t.Helper()
	_, err := f.pool.Exec(context.Background(), sql, args...)
	require.NoError(f.t, err)
}

// web inserts a web service on the fixture's server with a domain. With
// deployed it also has a succeeded current deployment (the server's route
// table then carries it).
func (f *fixture) web(slug, host string, deployed bool) db.Service {
	f.t.Helper()
	bg := context.Background()
	cfg := services.Config{Image: "traefik/whoami:latest", Port: 80}.WithDefaults()
	raw, err := json.Marshal(cfg)
	require.NoError(f.t, err)
	sv, err := f.q.InsertService(bg, db.InsertServiceParams{
		ID:            uuid.New(),
		EnvironmentID: f.env.ID, Name: slug, Slug: slug, Kind: services.KindApp, SourceType: services.SourceImage, Config: raw, ServerID: &f.server.ID,
	})
	require.NoError(f.t, err)
	_, err = f.q.InsertDomain(bg, db.InsertDomainParams{ServiceID: sv.ID, Hostname: host, TargetPort: 80})
	require.NoError(f.t, err)
	f.push.serviceID = sv.ID
	if !deployed {
		return sv
	}
	snap, _ := json.Marshal(services.Snapshot{Config: cfg})
	src, _ := json.Marshal(services.ReleaseSource{Type: services.SourceImage, Image: cfg.Image})
	rel, err := f.q.InsertRelease(bg, db.InsertReleaseParams{ID: uuid.New(), ServiceID: sv.ID, Source: src, Config: snap, ImageRef: cfg.Image})
	require.NoError(f.t, err)
	dep, err := f.q.InsertDeployment(bg, db.InsertDeploymentParams{
		ServiceID: sv.ID, ReleaseID: &rel.ID, ServerID: &f.server.ID, Trigger: "manual", ActorType: "user", Strategy: "rolling",
	})
	require.NoError(f.t, err)
	f.exec("UPDATE deployments SET status = 'succeeded' WHERE id = $1", dep.ID)
	require.NoError(f.t, f.q.MarkServiceDeployed(bg, db.MarkServiceDeployedParams{ID: sv.ID, CurrentDeploymentID: &dep.ID}))
	f.exec("UPDATE services SET applied_generation = generation WHERE id = $1", sv.ID)
	return sv
}

func (f *fixture) storedFirewall(id uuid.UUID) []byte {
	f.t.Helper()
	var raw []byte
	require.NoError(f.t, f.pool.QueryRow(context.Background(), "SELECT firewall FROM services WHERE id = $1", id).Scan(&raw))
	return raw
}

func (f *fixture) auditCount() int {
	f.t.Helper()
	var n int
	require.NoError(f.t, f.pool.QueryRow(context.Background(), "SELECT count(*) FROM audit_events WHERE action = $1", firewall.ActionUpdate).Scan(&n))
	return n
}

func (f *fixture) setFacts(caps ...string) {
	f.t.Helper()
	raw, err := json.Marshal(agentprotocol.Facts{OS: "linux", Arch: "amd64", Capabilities: caps})
	require.NoError(f.t, err)
	f.exec("UPDATE servers SET facts = $2 WHERE id = $1", f.server.ID, raw)
}

func (f *fixture) disableGeo() {
	f.t.Helper()
	f.exec("UPDATE servers SET geo_enabled = false, traffic_analytics_enabled = false WHERE id = $1", f.server.ID)
}

func (f *fixture) enableGeo() {
	f.t.Helper()
	f.exec("UPDATE servers SET geo_enabled = true, traffic_analytics_enabled = true WHERE id = $1", f.server.ID)
}

func requireAPIError(t *testing.T, err error, status int, code string) {
	t.Helper()
	ae, ok := apierr.As(err)
	require.True(t, ok, "want an API error, got %v", err)
	require.Equal(t, status, ae.Status)
	require.Equal(t, code, ae.Code)
}

func rule(id, action string, conds ...agentprotocol.FirewallCondition) agentprotocol.FirewallRule {
	return agentprotocol.FirewallRule{ID: id, Name: "rule " + id, Action: action, Conditions: conds}
}

func cond(field, op string, values ...string) agentprotocol.FirewallCondition {
	return agentprotocol.FirewallCondition{Field: field, Op: op, Values: values}
}

var (
	blockAdmin = rule("admin", agentprotocol.FirewallBlock, cond("path", "prefix", "/admin"))
	blockDE    = rule("notde", agentprotocol.FirewallBlock, agentprotocol.FirewallCondition{Field: "country", Op: "in", Values: []string{"DE"}, Not: true})
)

func TestPutStoresRulesAndGetReturnsThem(t *testing.T) {
	f := newFixture(t)
	sv := f.web("web", "app.example.com", true)

	v, err := f.svc.Put(f.ctx, sv.ID, firewall.PutInput{Rules: []agentprotocol.FirewallRule{
		rule("office", agentprotocol.FirewallAllow, cond("ip", "in", "203.0.113.0/24")),
		blockAdmin,
	}})
	require.NoError(t, err)
	require.Len(t, v.Rules, 2)
	require.Equal(t, "office", v.Rules[0].ID)
	require.Equal(t, firewall.EnforcementRoutes, v.Enforcement)
	require.Equal(t, []string{"app.example.com"}, v.Hosts)
	require.True(t, v.Deployed)
	require.Equal(t, "agent1", v.Server)
	require.True(t, v.GeoAvailable, "servers geolocate visitors by default")
	require.True(t, v.Supported)

	got, err := f.svc.Get(f.reader, sv.ID)
	require.NoError(t, err)
	require.Equal(t, v, got, "a read-only principal sees what Put returned")
	require.JSONEq(t, `{"rules":[
		{"id":"office","name":"rule office","action":"allow","conditions":[{"field":"ip","op":"in","values":["203.0.113.0/24"]}]},
		{"id":"admin","name":"rule admin","action":"block","conditions":[{"field":"path","op":"prefix","values":["/admin"]}]}]}`, string(f.storedFirewall(sv.ID)))

	// Replacing is a full replacement; an empty list removes the firewall.
	v, err = f.svc.Put(f.ctx, sv.ID, firewall.PutInput{Rules: []agentprotocol.FirewallRule{blockAdmin}})
	require.NoError(t, err)
	require.Len(t, v.Rules, 1)
	v, err = f.svc.Put(f.ctx, sv.ID, firewall.PutInput{})
	require.NoError(t, err)
	require.Empty(t, v.Rules)
	require.NotNil(t, v.Rules, "the view lists no rules as [] not null")
	require.Equal(t, firewall.EnforcementNone, v.Enforcement)
	require.Nil(t, f.storedFirewall(sv.ID), "no rules is stored as NULL")

	got, err = f.svc.Get(f.reader, sv.ID)
	require.NoError(t, err)
	require.Equal(t, v, got)
}

func TestPutNormalizesRules(t *testing.T) {
	f := newFixture(t)
	sv := f.web("web", "app.example.com", true)
	f.enableGeo()
	v, err := f.svc.Put(f.ctx, sv.ID, firewall.PutInput{Rules: []agentprotocol.FirewallRule{{
		Name: "  Block bad  ", Action: agentprotocol.FirewallBlock,
		Conditions: []agentprotocol.FirewallCondition{
			{Field: "country", Op: "in", Values: []string{" cn ", "ru", "CN", ""}},
			{Field: "method", Op: "in", Values: []string{"delete", "Post"}},
			{Field: "header", Op: "equals", Name: "  X-Key ", Values: []string{" k1 "}},
		},
	}}})
	require.NoError(t, err)
	r := v.Rules[0]
	require.Regexp(t, `^[0-9a-f]{10}$`, r.ID, "a rule without an id gets one")
	require.Equal(t, "Block bad", r.Name)
	require.Equal(t, []string{"CN", "RU"}, r.Conditions[0].Values, "countries are upper-cased, trimmed and de-duplicated")
	require.Equal(t, []string{"DELETE", "POST"}, r.Conditions[1].Values)
	require.Equal(t, "X-Key", r.Conditions[2].Name)
	require.Equal(t, []string{"k1"}, r.Conditions[2].Values)

	// The generated id is stable: sending the stored rules back changes nothing.
	pushes := len(f.push.servers)
	_, err = f.svc.Put(f.ctx, sv.ID, firewall.PutInput{Rules: v.Rules})
	require.NoError(t, err)
	require.Equal(t, pushes, len(f.push.servers), "an unchanged firewall pushes nothing")
	require.Equal(t, 1, f.auditCount())
}

func TestPutRejectsInvalidRules(t *testing.T) {
	f := newFixture(t)
	sv := f.web("web", "app.example.com", true)
	for name, rules := range map[string][]agentprotocol.FirewallRule{
		"bad regex":                       {rule("a", agentprotocol.FirewallBlock, cond("path", "regex", "(unclosed"))},
		"bad op":                          {rule("a", agentprotocol.FirewallBlock, cond("ip", "prefix", "10.0.0.0/8"))},
		"bad cidr":                        {rule("a", agentprotocol.FirewallBlock, cond("ip", "in", "10.0.0.0/40"))},
		"backtick":                        {rule("a", agentprotocol.FirewallBlock, cond("path", "contains", "a`b"))},
		"no condition":                    {rule("a", agentprotocol.FirewallBlock)},
		"bad action":                      {rule("a", "deny", cond("method", "in", "GET"))},
		"duplicate id":                    {blockAdmin, blockAdmin},
		"path no slash":                   {rule("a", agentprotocol.FirewallBlock, cond("path", "prefix", "admin"))},
		"values emptied by normalization": {rule("a", agentprotocol.FirewallBlock, cond("path", "contains", "  "))},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := f.svc.Put(f.ctx, sv.ID, firewall.PutInput{Rules: rules})
			requireAPIError(t, err, 422, "invalid_firewall")
			require.Nil(t, f.storedFirewall(sv.ID))
		})
	}
	ae, _ := apierr.As(func() error {
		_, err := f.svc.Put(f.ctx, sv.ID, firewall.PutInput{Rules: []agentprotocol.FirewallRule{
			blockAdmin, rule("b", agentprotocol.FirewallBlock, cond("path", "regex", "(")),
		}})
		return err
	}())
	require.Contains(t, ae.Message, "rule 2", "the message names the rule at fault")
	require.Empty(t, f.push.servers)
	require.Zero(t, f.auditCount())
}

func TestPutCountryRulesNeedGeolocationOnTheServer(t *testing.T) {
	f := newFixture(t)
	sv := f.web("web", "app.example.com", true)
	in := firewall.PutInput{Rules: []agentprotocol.FirewallRule{blockDE}}

	// Servers geolocate by default; this one has it switched off.
	f.disableGeo()
	_, err := f.svc.Put(f.ctx, sv.ID, in)
	requireAPIError(t, err, 422, "geo_required")
	require.Nil(t, f.storedFirewall(sv.ID))
	require.Empty(t, f.push.servers)

	// Geolocation without traffic analytics is not enough either.
	f.exec("UPDATE servers SET geo_enabled = true, traffic_analytics_enabled = false WHERE id = $1", f.server.ID)
	_, err = f.svc.Put(f.ctx, sv.ID, in)
	requireAPIError(t, err, 422, "geo_required")
	f.exec("UPDATE servers SET geo_enabled = false, traffic_analytics_enabled = true WHERE id = $1", f.server.ID)
	_, err = f.svc.Put(f.ctx, sv.ID, in)
	requireAPIError(t, err, 422, "geo_required")

	// A disabled country rule needs nothing, and shows no guard.
	off := blockDE
	off.Disabled = true
	v, err := f.svc.Put(f.ctx, sv.ID, firewall.PutInput{Rules: []agentprotocol.FirewallRule{off, blockAdmin}})
	require.NoError(t, err)
	require.Equal(t, firewall.EnforcementRoutes, v.Enforcement)
	require.False(t, v.GeoAvailable)

	f.enableGeo()
	v, err = f.svc.Put(f.ctx, sv.ID, in)
	require.NoError(t, err)
	require.Equal(t, firewall.EnforcementGuard, v.Enforcement, "country rules are evaluated by the edge guard")
	require.True(t, v.GeoAvailable)

	// Removing the firewall never needs geolocation.
	f.exec("UPDATE servers SET geo_enabled = false WHERE id = $1", f.server.ID)
	v, err = f.svc.Put(f.ctx, sv.ID, firewall.PutInput{})
	require.NoError(t, err)
	require.Equal(t, firewall.EnforcementNone, v.Enforcement)
}

func TestPutAgentWithoutFirewallCapabilityIsTooOld(t *testing.T) {
	f := newFixture(t)
	sv := f.web("web", "app.example.com", true)
	in := firewall.PutInput{Rules: []agentprotocol.FirewallRule{blockAdmin}}

	// A server that never reported facts is assumed current.
	v, err := f.svc.Put(f.ctx, sv.ID, in)
	require.NoError(t, err)
	require.True(t, v.Supported)
	_, err = f.svc.Put(f.ctx, sv.ID, firewall.PutInput{})
	require.NoError(t, err)

	f.setFacts(agentprotocol.CapProxyV2)
	pushes := len(f.push.servers)
	_, err = f.svc.Put(f.ctx, sv.ID, in)
	requireAPIError(t, err, 422, "agent_too_old")
	require.Equal(t, pushes, len(f.push.servers))
	require.Nil(t, f.storedFirewall(sv.ID))
	got, err := f.svc.Get(f.reader, sv.ID)
	require.NoError(t, err)
	require.False(t, got.Supported)

	f.setFacts(agentprotocol.CapProxyV2, agentprotocol.CapFirewall)
	v, err = f.svc.Put(f.ctx, sv.ID, in)
	require.NoError(t, err)
	require.True(t, v.Supported)

	// An agent that lost the capability can still have its firewall removed.
	f.setFacts()
	v, err = f.svc.Put(f.ctx, sv.ID, firewall.PutInput{})
	require.NoError(t, err)
	require.Empty(t, v.Rules)
	require.False(t, v.Supported)
}

func TestPutPushesRoutesOnlyForDeployedServices(t *testing.T) {
	f := newFixture(t)
	deployed := f.web("web", "app.example.com", true)
	in := firewall.PutInput{Rules: []agentprotocol.FirewallRule{blockAdmin}}

	_, err := f.svc.Put(f.ctx, deployed.ID, in)
	require.NoError(t, err)
	require.Equal(t, []uuid.UUID{f.server.ID}, f.push.servers, "the server running the service gets its table")
	require.Len(t, f.push.seen, 1)
	require.JSONEq(t, string(f.storedFirewall(deployed.ID)), string(f.push.seen[0]), "the push runs in the transaction that stored the rules")
	require.NotEmpty(t, f.push.seen[0])
	require.Equal(t, 1, f.auditCount())

	// The same rules again: nothing to do.
	_, err = f.svc.Put(f.ctx, deployed.ID, in)
	require.NoError(t, err)
	require.Len(t, f.push.servers, 1)
	require.Equal(t, 1, f.auditCount())

	// Removing them pushes again.
	_, err = f.svc.Put(f.ctx, deployed.ID, firewall.PutInput{})
	require.NoError(t, err)
	require.Len(t, f.push.servers, 2)
	require.Equal(t, 2, f.auditCount())

	// An undeployed service stores the rules and waits for its first deployment.
	idle := f.web("idle", "idle.example.com", false)
	before := len(f.push.servers)
	v, err := f.svc.Put(f.ctx, idle.ID, in)
	require.NoError(t, err)
	require.Equal(t, before, len(f.push.servers), "no push for a service that never ran")
	require.False(t, v.Deployed)
	require.Equal(t, "agent1", v.Server, "the server the service is assigned to")
	require.Equal(t, firewall.EnforcementRoutes, v.Enforcement)
	require.NotNil(t, f.storedFirewall(idle.ID))
}

func TestPutRollsBackWhenThePushFails(t *testing.T) {
	f := newFixture(t)
	sv := f.web("web", "app.example.com", true)
	f.push.err = errors.New("server unreachable")

	_, err := f.svc.Put(f.ctx, sv.ID, firewall.PutInput{Rules: []agentprotocol.FirewallRule{blockAdmin}})
	require.ErrorContains(t, err, "server unreachable")
	require.Len(t, f.push.servers, 1)
	require.Nil(t, f.storedFirewall(sv.ID), "the rules are not stored when the table could not be pushed")
	require.Zero(t, f.auditCount())
}

func TestPutAuditsTheChange(t *testing.T) {
	f := newFixture(t)
	sv := f.web("web", "app.example.com", true)
	f.enableGeo()
	off := blockAdmin
	off.ID, off.Disabled = "off", true
	_, err := f.svc.Put(f.ctx, sv.ID, firewall.PutInput{Rules: []agentprotocol.FirewallRule{blockDE, off}})
	require.NoError(t, err)

	var resource, resourceID string
	var meta []byte
	require.NoError(t, f.pool.QueryRow(context.Background(),
		"SELECT resource_type, resource_id, metadata FROM audit_events WHERE action = $1", firewall.ActionUpdate).Scan(&resource, &resourceID, &meta))
	require.Equal(t, "service", resource)
	require.Equal(t, sv.ID.String(), resourceID)
	var m map[string]any
	require.NoError(t, json.Unmarshal(meta, &m))
	require.Equal(t, "web", m["slug"])
	require.EqualValues(t, 2, m["rules"])
	require.EqualValues(t, 1, m["active"])
	require.Equal(t, true, m["countries"])
}

func TestPutScopesAndMissingServices(t *testing.T) {
	f := newFixture(t)
	sv := f.web("web", "app.example.com", true)
	in := firewall.PutInput{Rules: []agentprotocol.FirewallRule{blockAdmin}}

	_, err := f.svc.Put(f.reader, sv.ID, in)
	requireAPIError(t, err, 403, "forbidden")
	_, err = f.svc.Put(f.noProxy, sv.ID, in)
	requireAPIError(t, err, 403, "forbidden")
	_, err = f.svc.Get(f.reader, sv.ID)
	require.NoError(t, err)
	_, err = f.svc.Put(f.ctx, uuid.New(), in)
	requireAPIError(t, err, 404, "not_found")
	_, err = f.svc.Get(f.ctx, uuid.New())
	requireAPIError(t, err, 404, "not_found")
	require.Nil(t, f.storedFirewall(sv.ID))
	require.Empty(t, f.push.servers)
}

func TestPutNeedsApprovalFromAGuardedProductionPrincipal(t *testing.T) {
	f := newFixture(t)
	sv := f.web("web", "app.example.com", true)
	guarded := auth.WithPrincipal(context.Background(), &auth.Principal{
		Type: auth.TypeToken, UserID: f.userID, WorkspaceID: f.ws.ID, Label: "ci", Scopes: policy.AllScopes, RequireProductionApproval: true,
	})
	_, err := f.svc.Put(guarded, sv.ID, firewall.PutInput{Rules: []agentprotocol.FirewallRule{blockAdmin}})
	require.Error(t, err, "a production change by a guarded token asks for approval first")
	require.Nil(t, f.storedFirewall(sv.ID))
	require.Empty(t, f.push.servers)
	require.Zero(t, f.auditCount())

	// Reading is never guarded.
	_, err = f.svc.Get(guarded, sv.ID)
	require.NoError(t, err)
}

func TestPutRejectsTCPRoutes(t *testing.T) {
	f := newFixture(t)
	cfg, _ := json.Marshal(services.Config{Route: &services.RouteConfig{Protocol: agentprotocol.ProtoTCP, ListenPort: 25565}})
	sv, err := f.q.InsertService(context.Background(), db.InsertServiceParams{
		ID:            uuid.New(),
		EnvironmentID: f.env.ID, Name: "mc", Slug: "mc", Kind: services.KindRoute, SourceType: services.SourceExternal, Config: cfg,
	})
	require.NoError(t, err)
	_, err = f.svc.Put(f.ctx, sv.ID, firewall.PutInput{Rules: []agentprotocol.FirewallRule{blockAdmin}})
	requireAPIError(t, err, 422, "firewall_unsupported")
}

func TestViewEnforcementOfAddressRulesBehindCloudflare(t *testing.T) {
	f := newFixture(t)
	sv := f.web("web", "app.example.com", true)
	ipRule := rule("ip", agentprotocol.FirewallBlock, cond("ip", "in", "198.51.100.0/24"))

	v, err := f.svc.Put(f.ctx, sv.ID, firewall.PutInput{Rules: []agentprotocol.FirewallRule{ipRule}})
	require.NoError(t, err)
	require.Equal(t, firewall.EnforcementRoutes, v.Enforcement, "a direct domain: ClientIP routers see the peer")

	// The domain's proxied Cloudflare record points at this server: the origin
	// is locked to Cloudflare, so the visitor's address arrives in a header.
	ip := "203.0.113.10"
	f.exec("UPDATE servers SET public_ip = $2 WHERE id = $1", f.server.ID, ip)
	var domainID uuid.UUID
	require.NoError(t, f.pool.QueryRow(context.Background(), "SELECT id FROM domains WHERE service_id = $1", sv.ID).Scan(&domainID))
	f.exec(`UPDATE domains SET cf_zone_id = 'z1', cf_zone_name = 'x', cf_proxied = true, cf_origin_lock = true WHERE id = $1`, domainID)
	require.NoError(t, f.q.UpsertCloudflareRecord(context.Background(), db.UpsertCloudflareRecordParams{
		ZoneID: "z1", RecordID: "rec-1", WorkspaceID: f.ws.ID, OwnerKind: "domain", OwnerID: domainID, Hostname: "app.example.com", Type: "A", Content: ip, Proxied: true,
	}))
	got, err := f.svc.Get(f.reader, sv.ID)
	require.NoError(t, err)
	require.Equal(t, firewall.EnforcementGuard, got.Enforcement, "address rules behind Cloudflare go to the guard")

	// A firewall without address rules stays compiled into routers.
	v, err = f.svc.Put(f.ctx, sv.ID, firewall.PutInput{Rules: []agentprotocol.FirewallRule{blockAdmin}})
	require.NoError(t, err)
	require.Equal(t, firewall.EnforcementRoutes, v.Enforcement)
}

func TestServerRoutesCarryTheActiveRulesOnly(t *testing.T) {
	f := newFixture(t)
	sv := f.web("web", "app.example.com", true)
	other := f.web("other", "other.example.com", true)
	f.push.serviceID = sv.ID

	off := rule("off", agentprotocol.FirewallBlock, cond("method", "in", "GET"))
	off.Disabled = true
	_, err := f.svc.Put(f.ctx, sv.ID, firewall.PutInput{Rules: []agentprotocol.FirewallRule{
		blockAdmin, off, rule("office", agentprotocol.FirewallAllow, cond("ip", "in", "203.0.113.0/24")),
	}})
	require.NoError(t, err)

	table := func() map[string]agentprotocol.Route {
		routes, err := domains.ServerRoutes(context.Background(), f.q, f.server.ID, uuid.Nil)
		require.NoError(t, err)
		out := map[string]agentprotocol.Route{}
		for _, r := range routes {
			out[r.ServiceID] = r
		}
		return out
	}
	routes := table()
	require.Len(t, routes, 2)
	fw := routes[sv.ID.String()].Firewall
	require.NotNil(t, fw)
	require.Len(t, fw.Rules, 2, "the disabled rule is not sent to the agent")
	require.Equal(t, "admin", fw.Rules[0].ID)
	require.Equal(t, "office", fw.Rules[1].ID)
	require.Nil(t, routes[other.ID.String()].Firewall, "other services carry no firewall")
	require.Equal(t, agentprotocol.Firewall{Rules: fw.Rules}, *fw)
	require.NoError(t, fw.Validate())

	// Only disabled rules: the route is exactly the route of a service
	// without a firewall.
	_, err = f.svc.Put(f.ctx, sv.ID, firewall.PutInput{Rules: []agentprotocol.FirewallRule{off}})
	require.NoError(t, err)
	require.Nil(t, table()[sv.ID.String()].Firewall)
	v, err := f.svc.Get(f.ctx, sv.ID)
	require.NoError(t, err)
	require.Len(t, v.Rules, 1, "the disabled rule is kept in the service's firewall")
	require.Equal(t, firewall.EnforcementNone, v.Enforcement)

	_, err = f.svc.Put(f.ctx, sv.ID, firewall.PutInput{Rules: []agentprotocol.FirewallRule{blockAdmin}})
	require.NoError(t, err)
	require.NotNil(t, table()[sv.ID.String()].Firewall)
	_, err = f.svc.Put(f.ctx, sv.ID, firewall.PutInput{})
	require.NoError(t, err)
	require.Nil(t, table()[sv.ID.String()].Firewall)
}

func TestFirewallOfServiceRowAndDecode(t *testing.T) {
	require.Nil(t, domains.DecodeFirewall(nil))
	require.Nil(t, domains.DecodeFirewall(json.RawMessage(`null`)))
	require.Nil(t, domains.DecodeFirewall(json.RawMessage(`{not json`)))
	fw := domains.DecodeFirewall(json.RawMessage(`{"rules":[{"id":"a","name":"a","action":"block","disabled":true,"conditions":[]}]}`))
	require.NotNil(t, fw)
	require.Len(t, fw.Rules, 1)

	require.Nil(t, domains.FirewallOf(db.Service{}))
	require.Nil(t, domains.FirewallOf(db.Service{Firewall: json.RawMessage(`{"rules":[{"id":"a","name":"a","action":"block","disabled":true,"conditions":[]}]}`)}))
	of := domains.FirewallOf(db.Service{Firewall: json.RawMessage(`{"rules":[{"id":"a","name":"a","action":"block","disabled":true,"conditions":[]},{"id":"b","name":"b","action":"allow","conditions":[]}]}`)})
	require.NotNil(t, of)
	require.Len(t, of.Rules, 1)
	require.Equal(t, "b", of.Rules[0].ID)
}

// ---- HTTP ----

func httpCall(t *testing.T, ctx context.Context, svc *firewall.Service, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	p, _ := auth.PrincipalFrom(ctx)
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			if p != nil {
				req = req.WithContext(auth.WithPrincipal(req.Context(), p))
			}
			next.ServeHTTP(w, req)
		})
	})
	cfg := huma.DefaultConfig("test", "1")
	cfg.CreateHooks = nil
	cfg.Components.Schemas = api.NewSchemaRegistry()
	firewall.Register(humachi.New(r, cfg), svc)

	rd := bytes.NewReader(nil)
	if body != nil {
		raw, err := json.Marshal(body)
		require.NoError(t, err)
		rd = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func TestHTTPPutAndGet(t *testing.T) {
	f := newFixture(t)
	sv := f.web("web", "app.example.com", true)
	path := "/services/" + sv.ID.String() + "/firewall"

	rec := httpCall(t, f.ctx, f.svc, http.MethodPut, path, map[string]any{"rules": []any{
		map[string]any{"id": "noadmin", "name": "No admin", "action": "block", "conditions": []any{
			map[string]any{"field": "path", "op": "prefix", "values": []string{"/admin"}, "not": false},
		}},
	}})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var view firewall.FirewallView
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &view))
	require.Len(t, view.Rules, 1)
	require.Equal(t, "noadmin", view.Rules[0].ID)
	require.Equal(t, firewall.EnforcementRoutes, view.Enforcement)

	rec = httpCall(t, f.reader, f.svc, http.MethodGet, path, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var got firewall.FirewallView
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Equal(t, view, got)

	rec = httpCall(t, f.ctx, f.svc, http.MethodPut, path, map[string]any{"rules": []any{
		map[string]any{"id": "bad", "name": "bad", "action": "block", "conditions": []any{
			map[string]any{"field": "path", "op": "regex", "values": []string{"("}},
		}},
	}})
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
	var problem map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &problem))
	require.Equal(t, "invalid_firewall", problem["code"])

	rec = httpCall(t, f.ctx, f.svc, http.MethodPut, path, map[string]any{"rules": []any{
		map[string]any{"id": "bad", "name": "bad", "action": "maybe", "conditions": []any{}},
	}})
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code, "an unknown action is refused at the edge")

	rec = httpCall(t, f.reader, f.svc, http.MethodPut, path, map[string]any{"rules": []any{}})
	require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	rec = httpCall(t, context.Background(), f.svc, http.MethodGet, path, nil)
	require.Equal(t, http.StatusUnauthorized, rec.Code, rec.Body.String())
	rec = httpCall(t, f.ctx, f.svc, http.MethodGet, "/services/"+uuid.NewString()+"/firewall", nil)
	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
}
