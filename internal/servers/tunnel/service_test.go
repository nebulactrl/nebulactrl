package tunnel

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agents"
	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/events"
	"github.com/nebulactrl/nebulactrl/internal/integrations/cloudflare"
	"github.com/nebulactrl/nebulactrl/internal/jobs"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/internal/testutil"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

const (
	account  = "0123456789abcdef0123456789abcdef"
	tunnelID = "5a1b2c3d-0000-4000-8000-123456789abc"
	// connectorToken is what the fake Cloudflare API answers to a token request;
	// it must never be stored anywhere.
	connectorToken = "eyJhIjoidGVzdCIsInQiOiJ0dW4iLCJzIjoiU0VDUkVULUNBTkFSWS0xMjM0NSJ9"
)

var bg = context.Background()

// fakeAPI is the part of the Cloudflare API the tunnel service uses.
type fakeAPI struct {
	srv *httptest.Server

	mu       sync.Mutex
	calls    []string // "METHOD path"
	names    []string // names of created tunnels
	config   json.RawMessage
	failPut  bool
	deny     bool
	notFound bool // answer 404 to every tunnel call (already deleted)
}

func newFakeAPI(t *testing.T) *fakeAPI {
	f := &fakeAPI{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		defer f.mu.Unlock()
		f.calls = append(f.calls, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		write := func(status int, result any, msg string) {
			w.WriteHeader(status)
			env := map[string]any{"success": msg == "", "errors": []any{}, "result": result}
			if msg != "" {
				env["errors"] = []any{map[string]any{"code": 1000, "message": msg}}
			}
			_ = json.NewEncoder(w).Encode(env)
		}
		if f.deny {
			write(http.StatusForbidden, nil, "Authentication error")
			return
		}
		if f.notFound {
			write(http.StatusNotFound, nil, "Tunnel not found")
			return
		}
		base := "/accounts/" + account + "/cfd_tunnel"
		rest, ok := strings.CutPrefix(r.URL.Path, base)
		switch {
		case !ok:
			write(http.StatusNotFound, nil, "no route")
		case rest == "" && r.Method == http.MethodPost:
			var in struct {
				Name      string `json:"name"`
				ConfigSrc string `json:"config_src"`
			}
			_ = json.Unmarshal(body, &in)
			f.names = append(f.names, in.Name)
			write(http.StatusOK, map[string]any{"id": tunnelID, "name": in.Name, "status": "inactive", "config_src": in.ConfigSrc}, "")
		case rest == "/"+tunnelID+"/configurations" && r.Method == http.MethodPut:
			if f.failPut {
				write(http.StatusBadRequest, nil, "ingress invalid")
				return
			}
			f.config = body
			write(http.StatusOK, map[string]any{"tunnel_id": tunnelID}, "")
		case rest == "/"+tunnelID+"/token" && r.Method == http.MethodGet:
			write(http.StatusOK, connectorToken, "")
		case strings.HasPrefix(rest, "/"+tunnelID) && r.Method == http.MethodDelete:
			write(http.StatusOK, map[string]any{"id": tunnelID}, "")
		default:
			write(http.StatusNotFound, nil, "no route")
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeAPI) has(call string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if c == call {
			return true
		}
	}
	return false
}

func (f *fakeAPI) count(prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

// ingressHosts returns the hostnames of the last pushed configuration and the
// service of its last rule.
func (f *fakeAPI) ingressHosts(t *testing.T) (hosts []string, last string) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	var cfg struct {
		Config struct {
			Ingress []struct {
				Hostname string `json:"hostname"`
				Service  string `json:"service"`
			} `json:"ingress"`
		} `json:"config"`
	}
	require.NoError(t, json.Unmarshal(f.config, &cfg))
	for _, r := range cfg.Config.Ingress {
		if r.Hostname != "" {
			hosts = append(hosts, r.Hostname)
			require.Equal(t, "http://nebulactrl-traefik:80", r.Service)
		}
	}
	return hosts, cfg.Config.Ingress[len(cfg.Config.Ingress)-1].Service
}

// fakeCF is the team connection: a real client against the fake API.
type fakeCF struct {
	client        *cloudflare.Client
	notConfigured bool
	mu            sync.Mutex
	syncs         []uuid.UUID
}

func (f *fakeCF) Client(context.Context, uuid.UUID) (API, string, error) {
	if f.notConfigured {
		return nil, "", cloudflare.ErrNotConfigured
	}
	return f.client, account, nil
}

func (f *fakeCF) EnqueueSyncServer(_ context.Context, _ pgx.Tx, _, serverID uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.syncs = append(f.syncs, serverID)
	return nil
}

func (f *fakeCF) syncCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.syncs)
}

type fakeDisp struct {
	mu   sync.Mutex
	cmds []agents.NewCommand
}

func (d *fakeDisp) Enqueue(_ context.Context, _ pgx.Tx, c agents.NewCommand) (uuid.UUID, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.cmds = append(d.cmds, c)
	return uuid.New(), nil
}

func (d *fakeDisp) SubscribeLogs(context.Context, uuid.UUID, agentprotocol.LogsSubscribe) (<-chan agentprotocol.LogFrame, func(), error) {
	return nil, nil, io.EOF
}

func (d *fakeDisp) of(kind string) []agents.NewCommand {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []agents.NewCommand
	for _, c := range d.cmds {
		if c.Kind == kind {
			out = append(out, c)
		}
	}
	return out
}

type fixture struct {
	t      *testing.T
	pool   *pgxpool.Pool
	q      *db.Queries
	ws     uuid.UUID
	env    db.Environment
	server db.Server
	other  db.Server // a server of another team
	svc    *Service
	api    *fakeAPI
	cf     *fakeCF
	disp   *fakeDisp
	admin  context.Context
	viewer context.Context
}

func principal(ws, user uuid.UUID, scopes []string) context.Context {
	return auth.WithPrincipal(bg, &auth.Principal{Type: auth.TypeUser, UserID: user, WorkspaceID: ws, Label: "t", Scopes: scopes})
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	pool := testutil.NewPool(t)
	q := db.New(pool)
	ws, err := q.CreateWorkspace(bg, "w")
	require.NoError(t, err)
	ws2, err := q.CreateWorkspace(bg, "w2")
	require.NoError(t, err)
	u, err := q.CreateUser(bg, db.CreateUserParams{WorkspaceID: ws.ID, Email: "a@example.com", Name: "A", PasswordHash: "x"})
	require.NoError(t, err)
	prj, err := q.CreateProject(bg, db.CreateProjectParams{WorkspaceID: ws.ID, Name: "Shop", Slug: "shop"})
	require.NoError(t, err)
	env, err := q.CreateEnvironment(bg, db.CreateEnvironmentParams{ProjectID: prj.ID, Name: "production", Slug: "production", IsProduction: true})
	require.NoError(t, err)
	srv, err := q.CreateServer(bg, db.CreateServerParams{WorkspaceID: ws.ID, Name: "Agent 1"})
	require.NoError(t, err)
	other, err := q.CreateServer(bg, db.CreateServerParams{WorkspaceID: ws2.ID, Name: "foreign"})
	require.NoError(t, err)
	_, err = pool.Exec(bg, "UPDATE servers SET status = 'online'")
	require.NoError(t, err)

	api := newFakeAPI(t)
	cf := &fakeCF{client: cloudflare.New(cloudflare.Config{BaseURL: api.srv.URL, Token: "cf-token", HTTPClient: api.srv.Client()})}
	disp := &fakeDisp{}
	jm := jobs.New(pool, nil)
	svc := NewService(Deps{Pool: pool, Bus: events.NewBus(), Dispatcher: disp, Jobs: jm, Cloudflare: cf})
	RegisterJobs(jm, svc)
	return &fixture{
		t: t, pool: pool, q: q, ws: ws.ID, env: env, server: srv, other: other, svc: svc, api: api, cf: cf, disp: disp,
		admin:  principal(ws.ID, u.ID, policy.AllScopes),
		viewer: principal(ws.ID, u.ID, []string{policy.ScopeProjectsRead, policy.ScopeServersRead}),
	}
}

// deployed inserts an image service with one domain and a succeeded current
// deployment on f.server; zone marks the domain as managed in a Cloudflare zone.
func (f *fixture) deployed(slug, host string, zone bool) {
	f.t.Helper()
	cfg := services.Config{Image: "traefik/whoami:latest", Port: 80}.WithDefaults()
	raw, err := json.Marshal(cfg)
	require.NoError(f.t, err)
	sv, err := f.q.InsertService(bg, db.InsertServiceParams{
		ID: uuid.New(), EnvironmentID: f.env.ID, Name: slug, Slug: slug, Kind: services.KindApp, SourceType: services.SourceImage, Config: raw, ServerID: &f.server.ID,
	})
	require.NoError(f.t, err)
	dom, err := f.q.InsertDomain(bg, db.InsertDomainParams{ServiceID: sv.ID, Hostname: host, TargetPort: 80})
	require.NoError(f.t, err)
	snap, _ := json.Marshal(services.Snapshot{Config: cfg})
	src, _ := json.Marshal(services.ReleaseSource{Type: services.SourceImage, Image: cfg.Image})
	rel, err := f.q.InsertRelease(bg, db.InsertReleaseParams{ID: uuid.New(), ServiceID: sv.ID, Source: src, Config: snap, ImageRef: cfg.Image})
	require.NoError(f.t, err)
	dep, err := f.q.InsertDeployment(bg, db.InsertDeploymentParams{
		ServiceID: sv.ID, ReleaseID: &rel.ID, ServerID: &f.server.ID, Trigger: "manual", ActorType: "user", Strategy: "rolling",
	})
	require.NoError(f.t, err)
	_, err = f.pool.Exec(bg, "UPDATE deployments SET status = 'succeeded' WHERE id = $1", dep.ID)
	require.NoError(f.t, err)
	require.NoError(f.t, f.q.MarkServiceDeployed(bg, db.MarkServiceDeployedParams{ID: sv.ID, CurrentDeploymentID: &dep.ID}))
	if zone {
		_, err = f.pool.Exec(bg, "UPDATE domains SET cf_zone_id = 'zone1', cf_zone_name = 'example.com' WHERE id = $1", dom.ID)
		require.NoError(f.t, err)
	}
}

func (f *fixture) row() db.ServerTunnel {
	f.t.Helper()
	r, err := f.q.GetServerTunnel(bg, f.server.ID)
	require.NoError(f.t, err)
	return r
}

func (f *fixture) enable() TunnelView {
	f.t.Helper()
	v, err := f.svc.Enable(f.admin, f.server.ID)
	require.NoError(f.t, err)
	return v
}

// activate reports the connector online, as a result or heartbeat would.
func (f *fixture) activate() {
	f.t.Helper()
	require.NoError(f.t, f.svc.observe(bg, f.server.ID, tunnelID, agentprotocol.TunnelStatus{State: agentprotocol.TunnelStateOnline, Connections: 4}))
}

func apiCode(t *testing.T, err error) string {
	t.Helper()
	ae, ok := apierr.As(err)
	require.True(t, ok, "not an API error: %v", err)
	return ae.Code
}

// deleteJobArgs lists the args of the queued server_tunnel_delete jobs.
func (f *fixture) deleteJobArgs() []string {
	f.t.Helper()
	rows, err := f.pool.Query(bg, `SELECT args::text FROM river_job WHERE kind = 'server_tunnel_delete' ORDER BY id`)
	require.NoError(f.t, err)
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		require.NoError(f.t, rows.Scan(&s))
		out = append(out, s)
	}
	return out
}

func TestEnableCreatesTunnelPushesIngressAndQueuesConnector(t *testing.T) {
	f := newFixture(t)
	f.deployed("web", "app.example.com", true)
	f.deployed("api", "api.example.com", false)

	v := f.enable()
	require.True(t, v.Enabled)
	require.Equal(t, "provisioning", v.State)
	require.False(t, v.Active)
	require.Equal(t, tunnelID, v.TunnelID)
	require.Equal(t, tunnelID+".cfargotunnel.com", v.CNAME)
	require.Equal(t, 2, v.IngressHosts)

	// A remotely-managed tunnel, named after the server.
	require.Len(t, f.api.names, 1)
	require.True(t, strings.HasPrefix(f.api.names[0], "nebulactrl-agent-1-"), f.api.names[0])

	// Every hostname goes to the proxy, then the 404 catch-all.
	hosts, last := f.api.ingressHosts(t)
	require.Equal(t, []string{"api.example.com", "app.example.com"}, hosts)
	require.Equal(t, "http_status:404", last)

	// One connector command naming the tunnel; no token anywhere in it.
	cmds := f.disp.of(agentprotocol.KindSyncTunnel)
	require.Len(t, cmds, 1)
	var p agentprotocol.SyncTunnelPayload
	require.NoError(t, json.Unmarshal(cmds[0].Payload, &p))
	require.Equal(t, tunnelID, p.Tunnel.TunnelID)

	// The ingress hash is recorded, and the action is audited.
	require.NotEmpty(t, f.row().IngressHash)
	var n int
	require.NoError(t, f.pool.QueryRow(bg, `SELECT count(*) FROM audit_events WHERE action = 'tunnel.enable' AND resource_id = $1`, f.server.ID.String()).Scan(&n))
	require.Equal(t, 1, n)

	// DNS still points at the server: nothing is active before the first connection.
	_, ok, err := f.svc.TunnelTarget(bg, f.server.ID)
	require.NoError(t, err)
	require.False(t, ok)

	// A second enable is refused.
	_, err = f.svc.Enable(f.admin, f.server.ID)
	require.Equal(t, "tunnel_exists", apiCode(t, err))
}

func TestTokenIsASendTimeSecretAndNeverPersisted(t *testing.T) {
	f := newFixture(t)
	f.deployed("web", "app.example.com", true)
	f.enable()
	f.activate()
	_, err := f.svc.Disable(f.admin, f.server.ID, false)
	require.NoError(t, err)

	// The send-time secret is fetched from the API on demand.
	f.api.mu.Lock()
	f.api.calls = nil
	f.api.mu.Unlock()
	cmds := f.disp.of(agentprotocol.KindSyncTunnel)
	require.Len(t, cmds, 1)
	// (the tunnel is being removed now: no token is handed out)
	_, err = f.svc.SecretsForCommand(bg, agentprotocol.KindSyncTunnel, cmds[0].Payload)
	require.Error(t, err)
	require.Zero(t, f.api.count("GET"), "no token request for a removing tunnel")

	// A live tunnel gets its token, and only in the returned map.
	f2 := newFixture(t)
	f2.deployed("web", "app.example.com", true)
	f2.enable()
	cmd := f2.disp.of(agentprotocol.KindSyncTunnel)[0]
	secrets, err := f2.svc.SecretsForCommand(bg, agentprotocol.KindSyncTunnel, cmd.Payload)
	require.NoError(t, err)
	require.Equal(t, map[string]string{agentprotocol.SecretTunnelToken: connectorToken}, secrets)
	require.True(t, f2.api.has("GET /accounts/"+account+"/cfd_tunnel/"+tunnelID+"/token"))

	// Other commands carry none.
	none, err := f2.svc.SecretsForCommand(bg, agentprotocol.KindRemoveTunnel, []byte(`{}`))
	require.NoError(t, err)
	require.Empty(t, none)

	// The token is in no persisted payload, row or audit record.
	for _, c := range f2.disp.cmds {
		require.NotContains(t, string(c.Payload), connectorToken)
	}
	var dump string
	require.NoError(t, f2.pool.QueryRow(bg, `SELECT
		coalesce((SELECT string_agg(row_to_json(t)::text, ' ') FROM server_tunnels t), '') || ' ' ||
		coalesce((SELECT string_agg(metadata::text, ' ') FROM audit_events), '') || ' ' ||
		coalesce((SELECT string_agg(payload::text, ' ') FROM agent_commands), '') || ' ' ||
		coalesce((SELECT string_agg(args::text, ' ') FROM river_job), '')`).Scan(&dump))
	require.NotContains(t, dump, connectorToken)
	require.NotContains(t, dump, "SECRET-CANARY")
}

func TestFirstConnectionActivatesTheTunnel(t *testing.T) {
	f := newFixture(t)
	f.deployed("web", "app.example.com", true)
	f.deployed("api", "api.example.com", false)
	f.enable()
	require.Zero(t, f.cf.syncCount(), "enabling alone does not touch DNS")

	// Still connecting: not active.
	require.NoError(t, f.svc.observe(bg, f.server.ID, tunnelID, agentprotocol.TunnelStatus{State: agentprotocol.TunnelStateProvisioning, Message: "connecting"}))
	require.Nil(t, f.row().ActivatedAt)
	require.Zero(t, f.cf.syncCount())

	f.activate()
	row := f.row()
	require.NotNil(t, row.ActivatedAt)
	require.Equal(t, "online", row.State)
	require.Equal(t, int32(4), row.Connections)

	// DNS: the Cloudflare integration is asked to re-sync the server's domains and
	// from now on gets the tunnel CNAME.
	require.Equal(t, []uuid.UUID{f.server.ID}, f.cf.syncs)
	cname, ok, err := f.svc.TunnelTarget(bg, f.server.ID)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, tunnelID+".cfargotunnel.com", cname)

	// Routes: the managed domain is flagged as tunnelled, the other is not.
	routes := f.disp.of(agentprotocol.KindSyncRoutes)
	require.Len(t, routes, 1)
	var pl agentprotocol.SyncRoutesPayload
	require.NoError(t, json.Unmarshal(routes[0].Payload, &pl))
	flags := map[string]bool{}
	for _, rt := range pl.Routes {
		for _, d := range rt.Domains {
			flags[d.Host] = d.Tunnel
		}
	}
	require.Equal(t, map[string]bool{"app.example.com": true, "api.example.com": false}, flags)

	// A later online report does not activate again.
	f.activate()
	require.Equal(t, 1, f.cf.syncCount())
	require.Len(t, f.disp.of(agentprotocol.KindSyncRoutes), 1)

	// A report for another tunnel is ignored.
	require.NoError(t, f.svc.observe(bg, f.server.ID, "6b1b2c3d-0000-4000-8000-123456789abc", agentprotocol.TunnelStatus{State: agentprotocol.TunnelStateOffline}))
	require.Equal(t, "online", f.row().State)
}

func TestResultAndUndeliveredUpdateTheState(t *testing.T) {
	f := newFixture(t)
	f.enable()
	cmd := f.disp.of(agentprotocol.KindSyncTunnel)[0]
	// The command row the hub would have stored.
	stored, err := f.q.InsertAgentCommand(bg, db.InsertAgentCommandParams{
		ID: uuid.New(), ServerID: f.server.ID, Kind: cmd.Kind, Payload: cmd.Payload, IdempotencyKey: "k1", ExpiresAt: time.Now().Add(time.Hour),
	})
	require.NoError(t, err)

	data, _ := json.Marshal(agentprotocol.TunnelStatus{State: agentprotocol.TunnelStateOnline, TunnelID: tunnelID, Connections: 4})
	require.NoError(t, f.svc.HandleResult(bg, f.server.ID, stored.ID, agentprotocol.ResultFrame{CommandID: stored.ID.String(), Outcome: agentprotocol.OutcomeSucceeded, Data: data}))
	require.Equal(t, "online", f.row().State)
	require.NotNil(t, f.row().ActivatedAt)

	// A failed result needs attention and keeps the message.
	require.NoError(t, f.svc.HandleResult(bg, f.server.ID, stored.ID, agentprotocol.ResultFrame{CommandID: stored.ID.String(), Outcome: agentprotocol.OutcomeFailed, Error: "docker unreachable"}))
	require.Equal(t, "needs_attention", f.row().State)
	require.Equal(t, "docker unreachable", f.row().Message)

	// A command that never reached the agent says why.
	require.NoError(t, f.svc.HandleUndelivered(bg, stored.ID, "Not delivered: server offline. No changes were made."))
	require.Contains(t, f.row().Message, "Not delivered")
}

func TestDisableFlipsDNSBackAndDeletesTheTunnel(t *testing.T) {
	f := newFixture(t)
	f.deployed("web", "app.example.com", true)
	f.enable()
	f.activate()
	syncsBefore := f.cf.syncCount()

	v, err := f.svc.Disable(f.admin, f.server.ID, false)
	require.NoError(t, err)
	require.Equal(t, "removing", v.State)
	require.False(t, v.Active)

	// The DNS is re-synced without a tunnel target; the connector is removed; the
	// routes lose their tunnel flag.
	_, ok, err := f.svc.TunnelTarget(bg, f.server.ID)
	require.NoError(t, err)
	require.False(t, ok)
	require.Equal(t, syncsBefore+1, f.cf.syncCount())
	require.Len(t, f.disp.of(agentprotocol.KindRemoveTunnel), 1)
	routes := f.disp.of(agentprotocol.KindSyncRoutes)
	require.Len(t, routes, 2)
	var pl agentprotocol.SyncRoutesPayload
	require.NoError(t, json.Unmarshal(routes[1].Payload, &pl))
	require.False(t, pl.Routes[0].Domains[0].Tunnel)

	// The tunnel is deleted at Cloudflare later, by a job.
	require.Len(t, f.deleteJobArgs(), 1)
	require.False(t, f.api.has("DELETE /accounts/"+account+"/cfd_tunnel/"+tunnelID))
	require.NoError(t, f.svc.DeleteRemote(bg, f.server.ID))
	require.True(t, f.api.has("DELETE /accounts/"+account+"/cfd_tunnel/"+tunnelID+"/connections"))
	require.True(t, f.api.has("DELETE /accounts/"+account+"/cfd_tunnel/"+tunnelID))
	_, err = f.q.GetServerTunnel(bg, f.server.ID)
	require.ErrorIs(t, err, pgx.ErrNoRows)

	// A server without a tunnel has nothing to disable.
	_, err = f.svc.Disable(f.admin, f.server.ID, false)
	require.Equal(t, "not_found", apiCode(t, err))
}

func TestDisableForceDropsTheRecordWithoutAskingCloudflare(t *testing.T) {
	f := newFixture(t)
	f.enable()
	_, err := f.svc.Disable(f.admin, f.server.ID, true)
	require.NoError(t, err)
	_, err = f.q.GetServerTunnel(bg, f.server.ID)
	require.ErrorIs(t, err, pgx.ErrNoRows)
	require.Empty(t, f.deleteJobArgs())
	require.False(t, f.api.has("DELETE /accounts/"+account+"/cfd_tunnel/"+tunnelID))
}

func TestDeleteFailureIsKeptAndRetriable(t *testing.T) {
	f := newFixture(t)
	f.enable()
	_, err := f.svc.Disable(f.admin, f.server.ID, false)
	require.NoError(t, err)

	f.api.mu.Lock()
	f.api.deny = true
	f.api.mu.Unlock()
	// A refused delete (the token lost its permission) is permanent: no retry storm,
	// the reason is on the row.
	require.NoError(t, f.svc.DeleteRemote(bg, f.server.ID))
	r := f.row()
	require.Equal(t, "removing", r.State)
	require.Contains(t, r.Message, "Could not delete the tunnel at Cloudflare")

	// The token works again: disabling again retries.
	f.api.mu.Lock()
	f.api.deny = false
	f.api.mu.Unlock()
	require.NoError(t, f.svc.DeleteRemote(bg, f.server.ID))
	_, err = f.q.GetServerTunnel(bg, f.server.ID)
	require.ErrorIs(t, err, pgx.ErrNoRows)
}

func TestServerRevokedTearsTheTunnelDownWithoutAgentCommands(t *testing.T) {
	f := newFixture(t)
	f.enable()
	f.activate()
	before := len(f.disp.cmds)

	f.svc.ServerRevoked(bg, f.ws, f.server.ID)
	require.Equal(t, "removing", f.row().State)
	require.Len(t, f.disp.cmds, before, "a revoked agent is not asked to remove the connector")
	require.Len(t, f.deleteJobArgs(), 1)
	_, ok, _ := f.svc.TunnelTarget(bg, f.server.ID)
	require.False(t, ok)
}

func TestEnableRefusals(t *testing.T) {
	f := newFixture(t)

	// A viewer cannot enable; a server of another team is a 404.
	_, err := f.svc.Enable(f.viewer, f.server.ID)
	require.Equal(t, "forbidden", apiCode(t, err))
	require.Empty(t, f.api.calls)
	_, err = f.svc.Enable(f.admin, f.other.ID)
	require.Equal(t, "not_found", apiCode(t, err))
	_, err = f.svc.Get(f.admin, f.other.ID)
	require.Equal(t, "not_found", apiCode(t, err))

	// An agent that does not advertise the capability.
	facts, _ := json.Marshal(agentprotocol.Facts{OS: "linux", Capabilities: []string{agentprotocol.CapProxyV2}})
	_, err = f.pool.Exec(bg, "UPDATE servers SET facts = $2 WHERE id = $1", f.server.ID, facts)
	require.NoError(t, err)
	_, err = f.svc.Enable(f.admin, f.server.ID)
	require.Equal(t, "agent_too_old", apiCode(t, err))
	facts, _ = json.Marshal(agentprotocol.Facts{OS: "linux", Capabilities: []string{agentprotocol.CapTunnel}})
	_, err = f.pool.Exec(bg, "UPDATE servers SET facts = $2 WHERE id = $1", f.server.ID, facts)
	require.NoError(t, err)

	// No Cloudflare connection.
	f.cf.notConfigured = true
	_, err = f.svc.Enable(f.admin, f.server.ID)
	require.Equal(t, "cloudflare_not_connected", apiCode(t, err))
	f.cf.notConfigured = false

	// A token without the Tunnel permission.
	f.api.mu.Lock()
	f.api.deny = true
	f.api.mu.Unlock()
	_, err = f.svc.Enable(f.admin, f.server.ID)
	require.Equal(t, "cloudflare_tunnel_forbidden", apiCode(t, err))
	f.api.mu.Lock()
	f.api.deny = false
	f.api.mu.Unlock()
	require.Empty(t, f.disp.cmds)

	// A revoked server.
	_, err = f.pool.Exec(bg, "UPDATE servers SET status = 'revoked' WHERE id = $1", f.server.ID)
	require.NoError(t, err)
	_, err = f.svc.Enable(f.admin, f.server.ID)
	require.Equal(t, "server_revoked", apiCode(t, err))
}

func TestFailedIngressPushDeletesTheNewTunnel(t *testing.T) {
	f := newFixture(t)
	f.api.mu.Lock()
	f.api.failPut = true
	f.api.mu.Unlock()

	_, err := f.svc.Enable(f.admin, f.server.ID)
	require.Equal(t, "cloudflare_error", apiCode(t, err))
	require.True(t, f.api.has("DELETE /accounts/"+account+"/cfd_tunnel/"+tunnelID), "no orphaned tunnel at Cloudflare")
	_, err = f.q.GetServerTunnel(bg, f.server.ID)
	require.ErrorIs(t, err, pgx.ErrNoRows)
	require.Empty(t, f.disp.cmds)
}

func TestIngressFollowsTheServersHostnames(t *testing.T) {
	f := newFixture(t)
	f.enable()
	puts := func() int { return f.api.count("PUT ") }
	require.Equal(t, 1, puts())

	// Nothing changed: no push.
	require.NoError(t, f.svc.SyncIngress(bg, f.server.ID, false))
	require.Equal(t, 1, puts())

	// A new domain on the server is pushed by the next sync.
	f.deployed("web", "new.example.com", true)
	require.NoError(t, f.svc.SyncIngress(bg, f.server.ID, false))
	require.Equal(t, 2, puts())
	hosts, last := f.api.ingressHosts(t)
	require.Equal(t, []string{"new.example.com"}, hosts)
	require.Equal(t, "http_status:404", last)
	require.Equal(t, int32(1), f.row().IngressHosts)

	// A failed push is recorded on the row and retried by the next sync.
	f.deployed("api", "other.example.com", true)
	f.api.mu.Lock()
	f.api.failPut = true
	f.api.mu.Unlock()
	require.Error(t, f.svc.SyncIngress(bg, f.server.ID, false))
	require.Contains(t, f.row().IngressError, "ingress invalid")
	f.api.mu.Lock()
	f.api.failPut = false
	f.api.mu.Unlock()
	require.NoError(t, f.svc.SyncIngress(bg, f.server.ID, false))
	require.Empty(t, f.row().IngressError)
	hosts, _ = f.api.ingressHosts(t)
	require.Equal(t, []string{"new.example.com", "other.example.com"}, hosts)
}

func TestSweepResendsAMissingConnectorOnce(t *testing.T) {
	f := newFixture(t)
	f.enable()
	f.activate()
	require.Len(t, f.disp.of(agentprotocol.KindSyncTunnel), 1)

	// The container vanished: the heartbeat said so.
	_, err := f.pool.Exec(bg, `UPDATE server_tunnels SET state = 'offline', observed_at = now() WHERE server_id = $1`, f.server.ID)
	require.NoError(t, err)

	// Too soon after the last command: nothing.
	require.NoError(t, f.svc.Sweep(bg))
	require.Len(t, f.disp.of(agentprotocol.KindSyncTunnel), 1)

	_, err = f.pool.Exec(bg, `UPDATE server_tunnels SET connector_synced_at = now() - interval '10 minutes' WHERE server_id = $1`, f.server.ID)
	require.NoError(t, err)
	require.NoError(t, f.svc.Sweep(bg))
	require.Len(t, f.disp.of(agentprotocol.KindSyncTunnel), 2)

	// A connector that needs attention is left to a person.
	_, err = f.pool.Exec(bg, `UPDATE server_tunnels SET state = 'needs_attention', connector_synced_at = now() - interval '10 minutes' WHERE server_id = $1`, f.server.ID)
	require.NoError(t, err)
	require.NoError(t, f.svc.Sweep(bg))
	require.Len(t, f.disp.of(agentprotocol.KindSyncTunnel), 2)
}

func TestHeartbeatRecording(t *testing.T) {
	f := newFixture(t)
	f.enable()
	rec := func(state string, conns int32, missing bool) (db.RecordServerTunnelHeartbeatRow, error) {
		return f.q.RecordServerTunnelHeartbeat(bg, db.RecordServerTunnelHeartbeatParams{
			ServerID: f.server.ID, State: state, Connections: conns, Missing: missing,
		})
	}

	// A missing connector right after enabling is not news (it is still being created).
	_, err := rec("offline", 0, true)
	require.ErrorIs(t, err, pgx.ErrNoRows)

	// The first online heartbeat activates; the same one again writes nothing.
	r, err := rec("online", 4, false)
	require.NoError(t, err)
	require.Equal(t, "provisioning", r.PrevState)
	require.Nil(t, r.PrevActivatedAt)
	require.NotNil(t, r.ActivatedAt)
	_, err = rec("online", 4, false)
	require.ErrorIs(t, err, pgx.ErrNoRows)

	// A change is written; the activation stays.
	r, err = rec("online", 2, false)
	require.NoError(t, err)
	require.NotNil(t, r.PrevActivatedAt)

	// Once observed, a missing connector is recorded at once.
	r, err = rec("offline", 0, true)
	require.NoError(t, err)
	require.Equal(t, "online", r.PrevState)
	require.Equal(t, "offline", f.row().State)

	// A tunnel being removed ignores heartbeats.
	_, err = f.svc.Disable(f.admin, f.server.ID, false)
	require.NoError(t, err)
	_, err = rec("online", 4, false)
	require.ErrorIs(t, err, pgx.ErrNoRows)
	require.Equal(t, "removing", f.row().State)
}

func TestViewForAServerWithoutATunnel(t *testing.T) {
	f := newFixture(t)
	v, err := f.svc.Get(f.admin, f.server.ID)
	require.NoError(t, err)
	require.False(t, v.Enabled)
	require.Equal(t, StateOff, v.State)
	require.True(t, v.CloudflareConnected)
	require.True(t, v.AccountReady)
	require.True(t, v.AgentSupported)
	require.Equal(t, "http://nebulactrl-traefik:80", v.Upstream)

	f.cf.notConfigured = true
	v, err = f.svc.Get(f.viewer, f.server.ID)
	require.NoError(t, err)
	require.False(t, v.CloudflareConnected)
}

func TestDeleteGateBlocksWhileATunnelExists(t *testing.T) {
	f := newFixture(t)
	require.NoError(t, f.svc.DeleteGate(bg, f.ws))
	f.enable()
	require.Equal(t, "cloudflare_tunnels_exist", apiCode(t, f.svc.DeleteGate(bg, f.ws)))
}
