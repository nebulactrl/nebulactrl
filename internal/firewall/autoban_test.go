package firewall_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agents"
	"github.com/nebulactrl/nebulactrl/internal/api"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/domains"
	"github.com/nebulactrl/nebulactrl/internal/firewall"
	"github.com/nebulactrl/nebulactrl/internal/notifications"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// fakeDisp stores the commands it is given, as agent_commands rows.
type fakeDisp struct {
	mu   sync.Mutex
	cmds []agents.NewCommand
	ids  []uuid.UUID
}

func (d *fakeDisp) Enqueue(ctx context.Context, tx pgx.Tx, c agents.NewCommand) (uuid.UUID, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	id := uuid.New()
	d.cmds, d.ids = append(d.cmds, c), append(d.ids, id)
	_, err := tx.Exec(ctx, `INSERT INTO agent_commands (id, server_id, kind, payload, idempotency_key, expires_at) VALUES ($1,$2,$3,$4,$5, now() + interval '15 minutes')`,
		id, c.ServerID, c.Kind, c.Payload, id.String())
	return id, err
}

func (d *fakeDisp) SubscribeLogs(context.Context, uuid.UUID, agentprotocol.LogsSubscribe) (<-chan agentprotocol.LogFrame, func(), error) {
	return nil, nil, io.EOF
}

type fakeEmitter struct {
	mu     sync.Mutex
	events []notifications.Event
}

func (e *fakeEmitter) Emit(_ context.Context, ev notifications.Event) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.events = append(e.events, ev)
}

func policyOn() agentprotocol.AutoBan {
	return agentprotocol.AutoBan{Enabled: true, Threshold: 5, WindowSeconds: 120, DurationSeconds: 900, Allow: []string{"203.0.113.7", "198.51.100.0/24"}}
}

func (f *fixture) storedAutoBan(id uuid.UUID) []byte {
	f.t.Helper()
	var raw []byte
	require.NoError(f.t, f.pool.QueryRow(context.Background(), "SELECT autoban FROM services WHERE id = $1", id).Scan(&raw))
	return raw
}

func (f *fixture) auditCountOf(action string) int {
	f.t.Helper()
	var n int
	require.NoError(f.t, f.pool.QueryRow(context.Background(), "SELECT count(*) FROM audit_events WHERE action = $1", action).Scan(&n))
	return n
}

func TestPutAutoBanStoresPolicyAndPushesRoutes(t *testing.T) {
	f := newFixture(t)
	sv := f.web("web", "app.example.com", true)
	f.enableGeo()

	v, err := f.svc.GetAutoBan(f.reader, sv.ID)
	require.NoError(t, err)
	require.Equal(t, agentprotocol.AutoBan{Threshold: 10, WindowSeconds: 600, DurationSeconds: 3600, Allow: []string{}}, v.Policy, "a service without a policy shows the defaults, off")
	require.True(t, v.Supported)
	require.True(t, v.Analytics)
	require.Equal(t, "agent1", v.Server)
	require.Equal(t, []string{"app.example.com"}, v.Hosts)
	require.Empty(t, v.Warnings)
	require.NotNil(t, v.Warnings)

	pushes := len(f.push.servers)
	v, err = f.svc.PutAutoBan(f.ctx, sv.ID, policyOn())
	require.NoError(t, err)
	require.Equal(t, agentprotocol.AutoBan{Enabled: true, Threshold: 5, WindowSeconds: 120, DurationSeconds: 900, Allow: []string{"203.0.113.7/32", "198.51.100.0/24"}}, v.Policy,
		"the allow list is stored as masked prefixes")
	require.Len(t, f.push.servers, pushes+1, "a change re-pushes the server's routes at once")
	require.JSONEq(t, `{"enabled":true,"threshold":5,"windowSeconds":120,"durationSeconds":900,"allow":["203.0.113.7/32","198.51.100.0/24"]}`, string(f.storedAutoBan(sv.ID)))
	require.Equal(t, 1, f.auditCountOf(firewall.ActionAutoBanUpdate))

	table := func() map[string]agentprotocol.Route {
		routes, err := domains.ServerRoutes(context.Background(), f.q, f.server.ID, uuid.Nil)
		require.NoError(t, err)
		out := map[string]agentprotocol.Route{}
		for _, r := range routes {
			out[r.ServiceID] = r
		}
		return out
	}
	rt := table()[sv.ID.String()]
	require.NotNil(t, rt.AutoBan, "the route table carries an enabled policy")
	require.Equal(t, v.Policy, *rt.AutoBan)

	// The same policy again is a no-op: no push, no audit.
	pushes = len(f.push.servers)
	_, err = f.svc.PutAutoBan(f.ctx, sv.ID, policyOn())
	require.NoError(t, err)
	require.Len(t, f.push.servers, pushes)
	require.Equal(t, 1, f.auditCountOf(firewall.ActionAutoBanUpdate))

	// A disabled policy keeps its limits but the route carries none.
	off := policyOn()
	off.Enabled = false
	v, err = f.svc.PutAutoBan(f.ctx, sv.ID, off)
	require.NoError(t, err)
	require.False(t, v.Policy.Enabled)
	require.Equal(t, 5, v.Policy.Threshold)
	require.NotNil(t, f.storedAutoBan(sv.ID))
	require.Nil(t, table()[sv.ID.String()].AutoBan)

	// The default policy removes the column value.
	v, err = f.svc.PutAutoBan(f.ctx, sv.ID, agentprotocol.AutoBan{Threshold: 10, WindowSeconds: 600, DurationSeconds: 3600})
	require.NoError(t, err)
	require.Nil(t, f.storedAutoBan(sv.ID))
	require.Equal(t, 10, v.Policy.Threshold)
}

func TestPutAutoBanValidation(t *testing.T) {
	f := newFixture(t)
	sv := f.web("web", "app.example.com", true)
	f.enableGeo()

	bad := map[string]func(*agentprotocol.AutoBan){
		"threshold low":  func(a *agentprotocol.AutoBan) { a.Threshold = 2 },
		"threshold high": func(a *agentprotocol.AutoBan) { a.Threshold = 1001 },
		"window short":   func(a *agentprotocol.AutoBan) { a.WindowSeconds = 59 },
		"window long":    func(a *agentprotocol.AutoBan) { a.WindowSeconds = 86401 },
		"duration short": func(a *agentprotocol.AutoBan) { a.DurationSeconds = 299 },
		"duration long":  func(a *agentprotocol.AutoBan) { a.DurationSeconds = 604801 },
		"bad allow":      func(a *agentprotocol.AutoBan) { a.Allow = []string{"not-an-address"} },
	}
	for name, mutate := range bad {
		a := policyOn()
		mutate(&a)
		_, err := f.svc.PutAutoBan(f.ctx, sv.ID, a)
		requireAPIError(t, err, http.StatusUnprocessableEntity, "invalid_autoban")
		require.Nil(t, f.storedAutoBan(sv.ID), name)
	}
	many := policyOn()
	for range agentprotocol.MaxAccessCIDRs + 1 {
		many.Allow = append(many.Allow, "203.0.113.9")
	}
	_, err := f.svc.PutAutoBan(f.ctx, sv.ID, many.Normalize())
	require.NoError(t, err, "duplicates collapse before the limit applies")
	many.Allow = nil
	for i := range agentprotocol.MaxAccessCIDRs + 1 {
		many.Allow = append(many.Allow, "10.0."+itoa(i/250)+"."+itoa(i%250+1))
	}
	_, err = f.svc.PutAutoBan(f.ctx, sv.ID, many)
	requireAPIError(t, err, http.StatusUnprocessableEntity, "invalid_autoban")

	// A disabled policy is checked against the same limits.
	a := policyOn()
	a.Enabled, a.Threshold = false, 1
	_, err = f.svc.PutAutoBan(f.ctx, sv.ID, a)
	requireAPIError(t, err, http.StatusUnprocessableEntity, "invalid_autoban")
}

func itoa(i int) string {
	b, _ := json.Marshal(i)
	return string(b)
}

func TestPutAutoBanNeedsAnalyticsAndACurrentAgent(t *testing.T) {
	f := newFixture(t)
	sv := f.web("web", "app.example.com", true)

	f.disableGeo()
	pushes := len(f.push.servers)
	_, err := f.svc.PutAutoBan(f.ctx, sv.ID, policyOn())
	requireAPIError(t, err, http.StatusUnprocessableEntity, "analytics_required")
	require.Len(t, f.push.servers, pushes)
	require.Nil(t, f.storedAutoBan(sv.ID))
	v, err := f.svc.GetAutoBan(f.reader, sv.ID)
	require.NoError(t, err)
	require.False(t, v.Analytics)

	// Switching it off needs neither.
	off := policyOn()
	off.Enabled = false
	_, err = f.svc.PutAutoBan(f.ctx, sv.ID, off)
	require.NoError(t, err)

	f.enableGeo()
	f.setFacts(agentprotocol.CapProxyV2, agentprotocol.CapFirewall)
	_, err = f.svc.PutAutoBan(f.ctx, sv.ID, policyOn())
	requireAPIError(t, err, http.StatusUnprocessableEntity, "agent_too_old")
	v, err = f.svc.GetAutoBan(f.reader, sv.ID)
	require.NoError(t, err)
	require.False(t, v.Supported)

	f.setFacts(agentprotocol.CapProxyV2, agentprotocol.CapAutoBan)
	v, err = f.svc.PutAutoBan(f.ctx, sv.ID, policyOn())
	require.NoError(t, err)
	require.True(t, v.Policy.Enabled)
	require.True(t, v.Supported)
}

func TestPutAutoBanScopesAndTCPRoutes(t *testing.T) {
	f := newFixture(t)
	sv := f.web("web", "app.example.com", true)
	f.enableGeo()
	_, err := f.svc.PutAutoBan(f.noProxy, sv.ID, policyOn())
	require.Error(t, err, "proxy:write is needed")
	_, err = f.svc.PutAutoBan(f.reader, sv.ID, policyOn())
	require.Error(t, err)

	cfg, _ := json.Marshal(services.Config{Route: &services.RouteConfig{Protocol: agentprotocol.ProtoTCP, ListenPort: 25565}})
	mc, err := f.q.InsertService(context.Background(), db.InsertServiceParams{
		ID: uuid.New(), EnvironmentID: f.env.ID, Name: "mc", Slug: "mc", Kind: services.KindRoute, SourceType: services.SourceExternal, Config: cfg,
	})
	require.NoError(t, err)
	_, err = f.svc.PutAutoBan(f.ctx, mc.ID, policyOn())
	requireAPIError(t, err, http.StatusUnprocessableEntity, "autoban_unsupported")
}

func TestPutAutoBanOfAnUndeployedServiceIsStoredWithoutAPush(t *testing.T) {
	f := newFixture(t)
	sv := f.web("web", "app.example.com", false)
	pushes := len(f.push.servers)
	_, err := f.svc.PutAutoBan(f.ctx, sv.ID, policyOn())
	require.NoError(t, err)
	require.Len(t, f.push.servers, pushes)
	require.NotNil(t, f.storedAutoBan(sv.ID))
	// A deploy of the service builds its route from the row.
	require.NotNil(t, domains.AutoBanOf(mustService(t, f, sv.ID)))
}

func mustService(t *testing.T, f *fixture, id uuid.UUID) db.Service {
	t.Helper()
	var raw []byte
	require.NoError(t, f.pool.QueryRow(context.Background(), "SELECT autoban FROM services WHERE id = $1", id).Scan(&raw))
	return db.Service{ID: id, Autoban: raw}
}

func TestViewWarnsAboutCloudflareProxiedDomainsThatAreNotLocked(t *testing.T) {
	f := newFixture(t)
	sv := f.web("web", "app.example.com", true)
	f.exec("UPDATE domains SET cf_proxied = true WHERE service_id = $1", sv.ID)
	v, err := f.svc.GetAutoBan(f.reader, sv.ID)
	require.NoError(t, err)
	require.Equal(t, []string{firewall.WarnCloudflareProxiedNotLocked}, v.Warnings)
}

// ---- bans from the agent ----

func (f *fixture) banEvent(svc uuid.UUID, prefix string, rules map[string]int) (agentprotocol.BanEvent, uuid.UUID) {
	id := uuid.New()
	now := time.Now().UTC().Truncate(time.Second)
	return agentprotocol.BanEvent{
		EventID: uuid.NewString(), Kind: agentprotocol.BanEventBan, At: now,
		Ban: agentprotocol.Ban{
			ID: id.String(), ServiceID: svc.String(), RouteID: svc.String(), Prefix: prefix,
			StartedAt: now, ExpiresAt: now.Add(time.Hour), Strikes: 10, Rules: rules,
		},
	}, id
}

func endEvent(ban agentprotocol.Ban, reason string) agentprotocol.BanEvent {
	return agentprotocol.BanEvent{EventID: uuid.NewString(), Kind: agentprotocol.BanEventEnd, At: time.Now().UTC(), Reason: reason, Ban: ban}
}

func (f *fixture) ban(id uuid.UUID) db.ServiceBan {
	f.t.Helper()
	var b db.ServiceBan
	err := f.pool.QueryRow(context.Background(), `SELECT id, server_id, service_id, route_id, prefix, address, started_at, expires_at, ended_at, end_reason, strikes, rules, source, notified_at, created_at
		FROM service_bans WHERE id = $1`, id).Scan(&b.ID, &b.ServerID, &b.ServiceID, &b.RouteID, &b.Prefix, &b.Address, &b.StartedAt, &b.ExpiresAt, &b.EndedAt, &b.EndReason, &b.Strikes, &b.Rules, &b.Source, &b.NotifiedAt, &b.CreatedAt)
	require.NoError(f.t, err)
	return b
}

func (f *fixture) banCount() int {
	f.t.Helper()
	var n int
	require.NoError(f.t, f.pool.QueryRow(context.Background(), "SELECT count(*) FROM service_bans").Scan(&n))
	return n
}

func TestIngestBansIsIdempotentAndKeepsTheAddressOnlyWhileActive(t *testing.T) {
	f := newFixture(t)
	sv := f.web("web", "app.example.com", true)
	ev, id := f.banEvent(sv.ID, "203.0.113.7/32", map[string]int{"stdsecrets": 10})
	frame := agentprotocol.BanFrame{Seq: 1, Events: []agentprotocol.BanEvent{ev}}

	require.NoError(t, f.svc.IngestBans(f.ctx, f.server.ID, frame))
	require.NoError(t, f.svc.IngestBans(f.ctx, f.server.ID, frame), "a redelivered frame changes nothing")
	require.Equal(t, 1, f.banCount())
	b := f.ban(id)
	require.Equal(t, "203.0.113.7/32", b.Prefix)
	require.NotNil(t, b.Address)
	require.Equal(t, "203.0.113.7", *b.Address)
	require.Equal(t, f.server.ID, b.ServerID)
	require.Equal(t, sv.ID, b.ServiceID)
	require.Equal(t, int32(10), b.Strikes)
	require.JSONEq(t, `{"stdsecrets":10}`, string(b.Rules))
	require.Nil(t, b.EndedAt)
	require.Equal(t, "auto", b.Source)

	// An IPv6 ban shows the network address of its /64.
	ev6, id6 := f.banEvent(sv.ID, "2001:db8:1:2::/64", nil)
	require.NoError(t, f.svc.IngestBans(f.ctx, f.server.ID, agentprotocol.BanFrame{Seq: 2, Events: []agentprotocol.BanEvent{ev6}}))
	require.Equal(t, "2001:db8:1:2::", *f.ban(id6).Address)
	require.JSONEq(t, `{}`, string(f.ban(id6).Rules))

	// The end event ends it and forgets the address; a replay or a late second end is a no-op.
	end := endEvent(ev.Ban, agentprotocol.BanEndExpired)
	require.NoError(t, f.svc.IngestBans(f.ctx, f.server.ID, agentprotocol.BanFrame{Seq: 3, Events: []agentprotocol.BanEvent{end}}))
	require.NoError(t, f.svc.IngestBans(f.ctx, f.server.ID, agentprotocol.BanFrame{Seq: 3, Events: []agentprotocol.BanEvent{end}}))
	require.NoError(t, f.svc.IngestBans(f.ctx, f.server.ID, agentprotocol.BanFrame{Seq: 4, Events: []agentprotocol.BanEvent{endEvent(ev.Ban, agentprotocol.BanEndLifted)}}))
	b = f.ban(id)
	require.NotNil(t, b.EndedAt)
	require.Nil(t, b.Address, "the address is forgotten when the ban ends")
	require.Equal(t, "expired", *b.EndReason, "the first end wins")
	require.Equal(t, "203.0.113.7/32", b.Prefix)

	// A ban event replayed after its end does not bring the ban back.
	require.NoError(t, f.svc.IngestBans(f.ctx, f.server.ID, frame))
	require.NotNil(t, f.ban(id).EndedAt)
	require.Nil(t, f.ban(id).Address)

	// An unknown end reason is stored as lifted instead of failing the frame.
	weird := endEvent(ev6.Ban, "mystery")
	require.NoError(t, f.svc.IngestBans(f.ctx, f.server.ID, agentprotocol.BanFrame{Seq: 5, Events: []agentprotocol.BanEvent{weird}}))
	require.Equal(t, "lifted", *f.ban(id6).EndReason)
}

func TestIngestBansDropsWhatTheServerDoesNotOwn(t *testing.T) {
	f := newFixture(t)
	sv := f.web("web", "app.example.com", true)
	other, err := f.q.CreateServer(context.Background(), db.CreateServerParams{WorkspaceID: f.ws.ID, Name: "agent2"})
	require.NoError(t, err)

	ev, _ := f.banEvent(sv.ID, "203.0.113.7/32", nil)
	// The other server's agent cannot ban a service that does not run on it.
	require.NoError(t, f.svc.IngestBans(f.ctx, other.ID, agentprotocol.BanFrame{Seq: 1, Events: []agentprotocol.BanEvent{ev}}))
	require.Zero(t, f.banCount())

	gone, _ := f.banEvent(uuid.New(), "203.0.113.8/32", nil)
	notUUID, _ := f.banEvent(sv.ID, "203.0.113.9/32", nil)
	notUUID.Ban.ServiceID = "nope"
	badPrefix, _ := f.banEvent(sv.ID, "garbage", nil)
	require.NoError(t, f.svc.IngestBans(f.ctx, f.server.ID, agentprotocol.BanFrame{Seq: 2, Events: []agentprotocol.BanEvent{gone, notUUID, badPrefix}}))
	require.Zero(t, f.banCount(), "bans of unknown services and unreadable events are dropped, the frame is still stored")

	// A snapshot of another server never ends this server's bans.
	require.NoError(t, f.svc.IngestBans(f.ctx, f.server.ID, agentprotocol.BanFrame{Seq: 3, Events: []agentprotocol.BanEvent{ev}}))
	require.NoError(t, f.svc.IngestBans(f.ctx, other.ID, agentprotocol.BanFrame{Seq: 1, Snapshot: true, Services: []string{sv.ID.String()}}))
	require.Equal(t, 1, f.banCount())
	require.Nil(t, f.ban(uuid.MustParse(ev.Ban.ID)).EndedAt)
}

func TestIngestBansSnapshotEndsTheBansItDoesNotList(t *testing.T) {
	f := newFixture(t)
	a := f.web("a", "a.example.com", true)
	b := f.web("b", "b.example.com", true)
	keepEv, keep := f.banEvent(a.ID, "203.0.113.1/32", nil)
	dropEv, drop := f.banEvent(a.ID, "203.0.113.2/32", nil)
	otherEv, other := f.banEvent(b.ID, "203.0.113.3/32", nil)
	require.NoError(t, f.svc.IngestBans(f.ctx, f.server.ID, agentprotocol.BanFrame{Seq: 1, Events: []agentprotocol.BanEvent{keepEv, dropEv, otherEv}}))

	// The snapshot lists the full active set of service a: one of its bans, and a new one.
	newEv, fresh := f.banEvent(a.ID, "203.0.113.4/32", nil)
	require.NoError(t, f.svc.IngestBans(f.ctx, f.server.ID, agentprotocol.BanFrame{
		Seq: 2, Snapshot: true, Services: []string{a.ID.String()}, Events: []agentprotocol.BanEvent{keepEv, newEv},
	}))
	require.Nil(t, f.ban(keep).EndedAt)
	require.Nil(t, f.ban(fresh).EndedAt)
	d := f.ban(drop)
	require.NotNil(t, d.EndedAt)
	require.Equal(t, "resync", *d.EndReason)
	require.Nil(t, d.Address)
	require.Nil(t, f.ban(other).EndedAt, "services the snapshot does not name are left alone")

	// An empty snapshot of a service ends all of its bans.
	require.NoError(t, f.svc.IngestBans(f.ctx, f.server.ID, agentprotocol.BanFrame{Seq: 3, Snapshot: true, Services: []string{a.ID.String(), b.ID.String()}}))
	for _, id := range []uuid.UUID{keep, fresh, other} {
		require.Equal(t, "resync", *f.ban(id).EndReason)
	}
}

// ---- listing and unban ----

func TestListBansPagesActiveAndEnded(t *testing.T) {
	f := newFixture(t)
	sv := f.web("web", "app.example.com", true)
	var evs []agentprotocol.BanEvent
	for i := range 3 {
		ev, _ := f.banEvent(sv.ID, "203.0.113."+itoa(i+1)+"/32", map[string]int{"stdsecrets": 10})
		ev.Ban.StartedAt = ev.Ban.StartedAt.Add(time.Duration(i) * time.Minute)
		evs = append(evs, ev)
	}
	require.NoError(t, f.svc.IngestBans(f.ctx, f.server.ID, agentprotocol.BanFrame{Seq: 1, Events: evs}))
	require.NoError(t, f.svc.IngestBans(f.ctx, f.server.ID, agentprotocol.BanFrame{Seq: 2, Events: []agentprotocol.BanEvent{endEvent(evs[0].Ban, agentprotocol.BanEndUnbanned)}}))

	page, err := f.svc.ListBans(f.reader, sv.ID, firewall.BanStateActive, api.PageQuery{Limit: 1})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	require.NotEmpty(t, page.NextCursor)
	require.Equal(t, "203.0.113.3/32", page.Items[0].Prefix, "newest first")
	require.Equal(t, "203.0.113.3", *page.Items[0].Address)
	require.Equal(t, map[string]int{"stdsecrets": 10}, page.Items[0].Rules)
	require.Nil(t, page.Items[0].EndedAt)
	next, err := f.svc.ListBans(f.reader, sv.ID, firewall.BanStateActive, api.PageQuery{Limit: 1, Cursor: page.NextCursor})
	require.NoError(t, err)
	require.Len(t, next.Items, 1)
	require.Equal(t, "203.0.113.2/32", next.Items[0].Prefix)
	require.Empty(t, next.NextCursor)

	ended, err := f.svc.ListBans(f.reader, sv.ID, firewall.BanStateEnded, api.PageQuery{})
	require.NoError(t, err)
	require.Len(t, ended.Items, 1)
	require.Equal(t, "203.0.113.1/32", ended.Items[0].Prefix)
	require.Nil(t, ended.Items[0].Address, "an ended ban shows the prefix only")
	require.Equal(t, "unbanned", ended.Items[0].EndReason)
	require.NotNil(t, ended.Items[0].EndedAt)

	v, err := f.svc.GetAutoBan(f.reader, sv.ID)
	require.NoError(t, err)
	require.Equal(t, 2, v.ActiveBans)

	_, err = f.svc.ListBans(f.reader, sv.ID, firewall.BanStateActive, api.PageQuery{Cursor: "garbage"})
	requireAPIError(t, err, http.StatusBadRequest, "invalid_cursor")
}

func TestUnbanQueuesACommandAndTheResultEndsTheBan(t *testing.T) {
	f := newFixture(t)
	disp := &fakeDisp{}
	f.svc.SetDispatcher(disp)
	sv := f.web("web", "app.example.com", true)
	ev, id := f.banEvent(sv.ID, "203.0.113.7/32", nil)
	require.NoError(t, f.svc.IngestBans(f.ctx, f.server.ID, agentprotocol.BanFrame{Seq: 1, Events: []agentprotocol.BanEvent{ev}}))

	out, err := f.svc.Unban(f.ctx, sv.ID, id)
	require.NoError(t, err)
	require.Equal(t, "queued", out.Status)
	require.Len(t, disp.cmds, 1)
	require.Equal(t, agentprotocol.KindAutoBanUnban, disp.cmds[0].Kind)
	require.Equal(t, f.server.ID, disp.cmds[0].ServerID)
	var p agentprotocol.AutoBanUnbanPayload
	require.NoError(t, json.Unmarshal(disp.cmds[0].Payload, &p))
	require.Equal(t, agentprotocol.AutoBanUnbanPayload{ServiceID: sv.ID.String(), BanID: id.String()}, p)
	require.Equal(t, 1, f.auditCountOf(firewall.ActionAutoBanUnban))
	require.Nil(t, f.ban(id).EndedAt, "the ban ends when the agent confirms")

	// A failed result leaves it active; a result of another kind is ignored.
	cmd := disp.ids[0]
	require.NoError(t, f.svc.HandleResult(f.ctx, f.server.ID, cmd, agentprotocol.ResultFrame{CommandID: cmd.String(), Outcome: agentprotocol.OutcomeFailed}))
	require.Nil(t, f.ban(id).EndedAt)
	require.NoError(t, f.svc.HandleResult(f.ctx, f.server.ID, uuid.New(), agentprotocol.ResultFrame{Outcome: agentprotocol.OutcomeSucceeded}))
	require.Nil(t, f.ban(id).EndedAt)
	// Another server's result for this command is ignored.
	require.NoError(t, f.svc.HandleResult(f.ctx, uuid.New(), cmd, agentprotocol.ResultFrame{CommandID: cmd.String(), Outcome: agentprotocol.OutcomeSucceeded}))
	require.Nil(t, f.ban(id).EndedAt)

	ok := agentprotocol.ResultFrame{CommandID: cmd.String(), Outcome: agentprotocol.OutcomeSucceeded}
	require.NoError(t, f.svc.HandleResult(f.ctx, f.server.ID, cmd, ok))
	require.NoError(t, f.svc.HandleResult(f.ctx, f.server.ID, cmd, ok), "a redelivered result is a no-op")
	b := f.ban(id)
	require.NotNil(t, b.EndedAt)
	require.Equal(t, "unbanned", *b.EndReason)
	require.Nil(t, b.Address)

	// Unbanning an ended ban, an unknown ban or a ban of another service.
	_, err = f.svc.Unban(f.ctx, sv.ID, id)
	requireAPIError(t, err, http.StatusConflict, "ban_not_active")
	_, err = f.svc.Unban(f.ctx, sv.ID, uuid.New())
	requireAPIError(t, err, http.StatusNotFound, "not_found")
	other := f.web("other", "other.example.com", true)
	ev2, id2 := f.banEvent(sv.ID, "203.0.113.8/32", nil)
	require.NoError(t, f.svc.IngestBans(f.ctx, f.server.ID, agentprotocol.BanFrame{Seq: 2, Events: []agentprotocol.BanEvent{ev2}}))
	_, err = f.svc.Unban(f.ctx, other.ID, id2)
	requireAPIError(t, err, http.StatusNotFound, "not_found")
	require.Len(t, disp.cmds, 1, "nothing was queued for the refused unbans")

	// proxy:write is needed.
	_, err = f.svc.Unban(f.noProxy, sv.ID, id2)
	require.Error(t, err)
	_, err = f.svc.Unban(f.reader, sv.ID, id2)
	require.Error(t, err)
}

// ---- HTTP ----

func TestHTTPAutoBanEndpoints(t *testing.T) {
	f := newFixture(t)
	f.svc.SetDispatcher(&fakeDisp{})
	sv := f.web("web", "app.example.com", true)
	f.enableGeo()
	base := "/services/" + sv.ID.String()

	rec := httpCall(t, f.reader, f.svc, http.MethodGet, base+"/autoban", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var view firewall.AutoBanView
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &view))
	require.False(t, view.Policy.Enabled)
	require.Equal(t, 10, view.Policy.Threshold)

	body := map[string]any{"enabled": true, "threshold": 5, "windowSeconds": 120, "durationSeconds": 900, "allow": []string{"203.0.113.0/24"}}
	rec = httpCall(t, f.ctx, f.svc, http.MethodPut, base+"/autoban", body)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &view))
	require.True(t, view.Policy.Enabled)
	require.Equal(t, []string{"203.0.113.0/24"}, view.Policy.Allow)

	body["threshold"] = 1
	rec = httpCall(t, f.ctx, f.svc, http.MethodPut, base+"/autoban", body)
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
	var problem map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &problem))
	require.Equal(t, "invalid_autoban", problem["code"])

	rec = httpCall(t, f.reader, f.svc, http.MethodPut, base+"/autoban", body)
	require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	rec = httpCall(t, context.Background(), f.svc, http.MethodGet, base+"/autoban", nil)
	require.Equal(t, http.StatusUnauthorized, rec.Code)
	rec = httpCall(t, f.ctx, f.svc, http.MethodGet, "/services/"+uuid.NewString()+"/autoban", nil)
	require.Equal(t, http.StatusNotFound, rec.Code)

	ev, id := f.banEvent(sv.ID, "203.0.113.7/32", map[string]int{"stdsecrets": 10})
	require.NoError(t, f.svc.IngestBans(f.ctx, f.server.ID, agentprotocol.BanFrame{Seq: 1, Events: []agentprotocol.BanEvent{ev}}))

	rec = httpCall(t, f.reader, f.svc, http.MethodGet, base+"/bans", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var page firewall.BansPage
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &page))
	require.Len(t, page.Items, 1)
	require.Equal(t, id, page.Items[0].ID)
	require.Equal(t, "203.0.113.7", *page.Items[0].Address)
	rec = httpCall(t, f.reader, f.svc, http.MethodGet, base+"/bans?state=ended", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &page))
	require.Empty(t, page.Items)
	rec = httpCall(t, f.reader, f.svc, http.MethodGet, base+"/bans?state=bogus", nil)
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code, "state is an enum")

	rec = httpCall(t, f.reader, f.svc, http.MethodDelete, base+"/bans/"+id.String(), nil)
	require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	rec = httpCall(t, f.ctx, f.svc, http.MethodDelete, base+"/bans/"+id.String(), nil)
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	var out firewall.UnbanView
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	require.Equal(t, "queued", out.Status)

	require.NoError(t, f.svc.IngestBans(f.ctx, f.server.ID, agentprotocol.BanFrame{Seq: 2, Events: []agentprotocol.BanEvent{endEvent(ev.Ban, agentprotocol.BanEndUnbanned)}}))
	rec = httpCall(t, f.ctx, f.svc, http.MethodDelete, base+"/bans/"+id.String(), nil)
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &problem))
	require.Equal(t, "ban_not_active", problem["code"])
}

// ---- jobs ----

func TestDigestAnnouncesBansOncePerServiceWithoutAddresses(t *testing.T) {
	f := newFixture(t)
	em := &fakeEmitter{}
	f.svc.SetNotifier(em)
	a := f.web("shop", "shop.example.com", true)
	b := f.web("blog", "blog.example.com", true)
	_, err := f.svc.Put(f.ctx, a.ID, firewall.PutInput{Rules: []agentprotocol.FirewallRule{blockAdmin}})
	require.NoError(t, err)

	var evs []agentprotocol.BanEvent
	for i, rules := range []map[string]int{{"stdsecrets": 10, "admin": 3}, {"stdsecrets": 6}, {"admin": 20}} {
		ev, _ := f.banEvent(a.ID, "203.0.113."+itoa(i+1)+"/32", rules)
		evs = append(evs, ev)
	}
	evB, _ := f.banEvent(b.ID, "198.51.100.1/32", map[string]int{"gone": 4})
	evs = append(evs, evB)
	require.NoError(t, f.svc.IngestBans(f.ctx, f.server.ID, agentprotocol.BanFrame{Seq: 1, Events: evs}))

	n, err := f.svc.Digest(context.Background(), time.Now().Add(time.Minute))
	require.NoError(t, err)
	require.Equal(t, 2, n)
	require.Len(t, em.events, 2, "one message per service")
	byService := map[uuid.UUID]notifications.Event{}
	for _, ev := range em.events {
		require.Equal(t, notifications.TypeServiceAutoBan, ev.Type)
		require.NotNil(t, ev.ServiceID)
		byService[*ev.ServiceID] = ev
		for _, text := range append([]string{ev.Title, ev.Body}, mapValues(ev.Fields)...) {
			require.NotContains(t, text, "203.0.113", "no address in a notification")
			require.NotContains(t, text, "198.51.100")
		}
	}
	require.Equal(t, "3 addresses banned on shop in the last 10 minutes", byService[a.ID].Title)
	require.Equal(t, "3", byService[a.ID].Fields["Banned"])
	require.Equal(t, "rule admin", byService[a.ID].Fields["Top rule"], "the rule with the most strikes (23 against 16), named by the service's own rule name")
	require.Equal(t, "1 address banned on blog in the last 10 minutes", byService[b.ID].Title)
	require.Equal(t, "a removed rule", byService[b.ID].Fields["Top rule"])

	// Announced bans are not announced again; a new one is.
	n, err = f.svc.Digest(context.Background(), time.Now().Add(time.Minute))
	require.NoError(t, err)
	require.Zero(t, n)
	ev, _ := f.banEvent(a.ID, "203.0.113.50/32", map[string]int{"stdsecrets": 10})
	require.NoError(t, f.svc.IngestBans(f.ctx, f.server.ID, agentprotocol.BanFrame{Seq: 2, Events: []agentprotocol.BanEvent{ev}}))
	n, err = f.svc.Digest(context.Background(), time.Now().Add(time.Minute))
	require.NoError(t, err)
	require.Equal(t, 1, n)
	require.Equal(t, "1 address banned on shop in the last 10 minutes", em.events[2].Title)
	require.Equal(t, "Secret files", em.events[2].Fields["Top rule"])
}

func mapValues(m map[string]string) []string {
	var out []string
	for _, v := range m {
		out = append(out, v)
	}
	return out
}

func TestPruneBansEndsLapsedBansAndDropsOldEndedOnes(t *testing.T) {
	f := newFixture(t)
	sv := f.web("web", "app.example.com", true)
	now := time.Now().UTC()

	lapsed, lapsedID := f.banEvent(sv.ID, "203.0.113.1/32", nil)
	lapsed.Ban.StartedAt, lapsed.Ban.ExpiresAt = now.Add(-3*time.Hour), now.Add(-2*time.Hour)
	recent, recentID := f.banEvent(sv.ID, "203.0.113.2/32", nil)
	recent.Ban.StartedAt, recent.Ban.ExpiresAt = now.Add(-time.Hour), now.Add(-time.Minute)
	oldEnded, oldID := f.banEvent(sv.ID, "203.0.113.3/32", nil)
	newEnded, newID := f.banEvent(sv.ID, "203.0.113.4/32", nil)
	require.NoError(t, f.svc.IngestBans(f.ctx, f.server.ID, agentprotocol.BanFrame{Seq: 1, Events: []agentprotocol.BanEvent{lapsed, recent, oldEnded, newEnded}}))
	old := endEvent(oldEnded.Ban, agentprotocol.BanEndExpired)
	old.At = now.Add(-91 * 24 * time.Hour)
	recentEnd := endEvent(newEnded.Ban, agentprotocol.BanEndExpired)
	recentEnd.At = now.Add(-89 * 24 * time.Hour)
	require.NoError(t, f.svc.IngestBans(f.ctx, f.server.ID, agentprotocol.BanFrame{Seq: 2, Events: []agentprotocol.BanEvent{old, recentEnd}}))

	ended, pruned, err := f.svc.PruneBans(context.Background(), now)
	require.NoError(t, err)
	require.EqualValues(t, 1, ended, "only the ban that lapsed well past its expiry")
	require.EqualValues(t, 1, pruned)

	b := f.ban(lapsedID)
	require.NotNil(t, b.EndedAt)
	require.Equal(t, "expired", *b.EndReason)
	require.Nil(t, b.Address)
	require.Nil(t, f.ban(recentID).EndedAt, "a ban just past its expiry waits for the agent's end frame")
	require.Equal(t, 3, f.banCount())
	_ = newID
	var n int
	require.NoError(t, f.pool.QueryRow(context.Background(), "SELECT count(*) FROM service_bans WHERE id = $1", oldID).Scan(&n))
	require.Zero(t, n)
}
