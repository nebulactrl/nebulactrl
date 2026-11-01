package platform_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
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
	"github.com/nebulactrl/nebulactrl/internal/backups"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/deployments"
	"github.com/nebulactrl/nebulactrl/internal/domains"
	"github.com/nebulactrl/nebulactrl/internal/events"
	"github.com/nebulactrl/nebulactrl/internal/integrations/tailscale"
	"github.com/nebulactrl/nebulactrl/internal/jobs"
	"github.com/nebulactrl/nebulactrl/internal/platform"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/projects"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/internal/terminal"
	"github.com/nebulactrl/nebulactrl/internal/testutil"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// ---- fakes ---------------------------------------------------------------------------

type rowDispatcher struct{}

func (rowDispatcher) Enqueue(ctx context.Context, tx pgx.Tx, c agents.NewCommand) (uuid.UUID, error) {
	id := uuid.New()
	_, err := db.New(tx).InsertAgentCommand(ctx, db.InsertAgentCommandParams{
		ID: id, ServerID: c.ServerID, DeploymentID: c.DeploymentID, Kind: c.Kind, Payload: c.Payload,
		IdempotencyKey: id.String(), ExpiresAt: time.Now().Add(time.Hour),
	})
	return id, err
}

func (rowDispatcher) SubscribeLogs(context.Context, uuid.UUID, agentprotocol.LogsSubscribe) (<-chan agentprotocol.LogFrame, func(), error) {
	return nil, nil, io.EOF
}

type fakeBackups struct {
	mu        sync.Mutex
	existing  []backups.DestinationView
	volumeCfg map[uuid.UUID]backups.VolumeBackupConfigInput
}

func (f *fakeBackups) ListDestinations(context.Context) ([]backups.DestinationView, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]backups.DestinationView{}, f.existing...), nil
}

func (f *fakeBackups) PutVolumeBackupConfig(_ context.Context, id uuid.UUID, in backups.VolumeBackupConfigInput) (backups.VolumeBackupConfigView, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.volumeCfg == nil {
		f.volumeCfg = map[uuid.UUID]backups.VolumeBackupConfigInput{}
	}
	f.volumeCfg[id] = in
	return backups.VolumeBackupConfigView{}, nil
}

type fakeTailnet struct {
	cur      tailscale.TailscaleIntegration
	put      []tailscale.PutInput
	deleted  bool
	deleteEr error
}

func (f *fakeTailnet) Get(context.Context) (tailscale.TailscaleIntegration, error) { return f.cur, nil }
func (f *fakeTailnet) Put(_ context.Context, in tailscale.PutInput) (tailscale.TailscaleIntegration, error) {
	f.put = append(f.put, in)
	f.cur = tailscale.TailscaleIntegration{Configured: true, Provider: in.Provider, LoginServer: in.LoginServer}
	return f.cur, nil
}
func (f *fakeTailnet) Delete(context.Context) error {
	if f.deleteEr != nil {
		return f.deleteEr
	}
	f.deleted = true
	f.cur = tailscale.TailscaleIntegration{}
	return nil
}
func (f *fakeTailnet) Test(context.Context) (tailscale.TailscaleTestResult, error) {
	return tailscale.TailscaleTestResult{OK: true}, nil
}

// fakeDeployer records the deployments it "starts" as real rows.
type fakeDeployer struct {
	pool   *pgxpool.Pool
	status string
	calls  int
}

func (f *fakeDeployer) DeployStaged(ctx context.Context, envID uuid.UUID, ids []uuid.UUID, _ string) (deployments.StagedResult, error) {
	f.calls++
	var out deployments.StagedResult
	for _, id := range ids {
		var depID uuid.UUID
		if err := f.pool.QueryRow(ctx, `INSERT INTO deployments (service_id, trigger, actor_type, status, strategy) VALUES ($1, 'staged', 'system', $2, 'recreate') RETURNING id`, id, f.status).Scan(&depID); err != nil {
			return out, err
		}
		out.Items = append(out.Items, deployments.DeploymentView{ID: depID, ServiceID: id})
	}
	return out, nil
}

// execAgents answers exec_open with canned output per command.
type execAgents struct {
	mu   sync.Mutex
	ran  [][]string
	out  map[string]string // by joined argv prefix
	code map[string]int
}

func (a *execAgents) OpenStream(_ uuid.UUID, id string) (terminal.Stream, error) {
	return &execStream{a: a, id: id, frames: make(chan agentprotocol.Frame, 16), done: make(chan struct{})}, nil
}

type execStream struct {
	a      *execAgents
	id     string
	frames chan agentprotocol.Frame
	done   chan struct{}
}

func (s *execStream) Frames() <-chan agentprotocol.Frame { return s.frames }
func (s *execStream) Done() <-chan struct{}              { return s.done }
func (s *execStream) Lost() bool                         { return false }
func (s *execStream) Close()                             {}
func (s *execStream) Send(typ string, body any) error {
	if typ != agentprotocol.TypeExecOpen {
		return nil
	}
	open := body.(agentprotocol.ExecOpen)
	s.a.mu.Lock()
	s.a.ran = append(s.a.ran, open.Command)
	key := strings.Join(open.Command, " ")
	out, code := "", 0
	for k, v := range s.a.out {
		if strings.HasPrefix(key, k) {
			out, code = v, s.a.code[k]
		}
	}
	s.a.mu.Unlock()
	push := func(typ string, v any) {
		raw, _ := json.Marshal(v)
		s.frames <- agentprotocol.Frame{Envelope: agentprotocol.Envelope{Type: typ, ID: s.id}, Raw: raw}
	}
	push(agentprotocol.TypeExecOpened, agentprotocol.ExecOpened{SessionID: s.id, Container: "c"})
	push(agentprotocol.TypeExecOutput, agentprotocol.ExecOutput{SessionID: s.id, Data: []byte(strings.ReplaceAll(out, "\n", "\r\n"))})
	push(agentprotocol.TypeExecExit, agentprotocol.ExecExit{SessionID: s.id, ExitCode: code})
	return nil
}

// ---- fixture ---------------------------------------------------------------------------

type fixture struct {
	t        *testing.T
	pool     *pgxpool.Pool
	q        *db.Queries
	ws       db.Workspace
	user     db.CreateUserRow
	server   db.Server
	ctx      context.Context
	box      *secretbox.Box
	svc      *platform.Service
	projects *projects.Service
	svcs     *services.Service

	backups  *fakeBackups
	tailnet  *fakeTailnet
	deployer *fakeDeployer
	agents   *execAgents
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	pool := testutil.NewPool(t)
	q := db.New(pool)
	bg := context.Background()
	ws, err := q.CreateWorkspace(bg, "w")
	require.NoError(t, err)
	u, err := q.CreateUser(bg, db.CreateUserParams{WorkspaceID: ws.ID, Email: "dev@example.com", Name: "Dev", PasswordHash: "x"})
	require.NoError(t, err)
	srv, err := q.CreateServer(bg, db.CreateServerParams{WorkspaceID: ws.ID, Name: "agent1"})
	require.NoError(t, err)
	_, err = pool.Exec(bg, "UPDATE servers SET status = 'online', base_domain = 'example.test', acme_email = 'ops@example.test' WHERE id = $1", srv.ID)
	require.NoError(t, err)
	srv, err = q.LoadServerRow(bg, db.LoadServerRowParams{ID: srv.ID})
	require.NoError(t, err)

	box, err := secretbox.New(bytes.Repeat([]byte{7}, 32))
	require.NoError(t, err)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	bus := events.NewBus()
	jm := jobs.New(pool, logger)
	svcs := services.NewService(pool, bus, box, rowDispatcher{}, logger)
	domainSvc := domains.NewService(pool, bus, jm, box)
	domains.RegisterJobs(jm, domainSvc, logger)

	f := &fixture{
		t: t, pool: pool, q: q, ws: ws, user: u, server: srv, box: box, svcs: svcs, projects: projects.NewService(pool, bus),
		backups: &fakeBackups{}, tailnet: &fakeTailnet{}, deployer: &fakeDeployer{pool: pool, status: "succeeded"},
		agents: &execAgents{out: map[string]string{}, code: map[string]int{}},
	}
	f.svc = platform.NewService(platform.Deps{
		Pool: pool, Bus: bus, Jobs: jm, Box: box, Services: svcs, Deploy: f.deployer, Backups: f.backups,
		Tailnet: f.tailnet, Agents: f.agents, Logger: logger,
	})
	platform.RegisterJobs(jm, f.svc)
	f.ctx = auth.WithPrincipal(bg, &auth.Principal{Type: auth.TypeUser, UserID: u.ID, WorkspaceID: ws.ID, Role: auth.RoleOwner, Label: u.Email, Scopes: policy.AllScopes})
	return f
}

func (f *fixture) provision(mod ...func(*platform.ProvisionInput)) platform.ProvisionResult {
	f.t.Helper()
	in := platform.ProvisionInput{Kind: platform.KindHeadscale, ServerID: f.server.ID}
	for _, m := range mod {
		m(&in)
	}
	r, err := f.svc.Provision(f.ctx, in)
	require.NoError(f.t, err)
	return r
}

// step runs the job body until it reports done or waits, like River would.
func (f *fixture) run(id uuid.UUID, max int) (time.Duration, bool) {
	f.t.Helper()
	for range max {
		wait, done, err := f.svc.Advance(context.Background(), id)
		require.NoError(f.t, err)
		if done || wait > 0 {
			return wait, done
		}
	}
	f.t.Fatalf("still advancing after %d steps", max)
	return 0, false
}

func (f *fixture) row(id uuid.UUID) db.PlatformService {
	r, err := f.q.GetPlatformServiceInternal(context.Background(), id)
	require.NoError(f.t, err)
	return r
}

func (f *fixture) platformService(r platform.ProvisionResult) db.Service {
	rows, err := f.q.ListServicesIncludingDeleted(context.Background(), f.row(r.View.ID).ServiceIds)
	require.NoError(f.t, err)
	require.Len(f.t, rows, 1)
	return rows[0]
}

// ---- tests -----------------------------------------------------------------------------

func TestProvisionStagesTheServiceInAHiddenSystemProject(t *testing.T) {
	f := newFixture(t)
	r := f.provision()
	require.Equal(t, platform.StatusProvisioning, r.View.Status)
	require.Equal(t, platform.StepConfigure, r.View.Step)
	require.Regexp(t, `^headscale-platform-nebulactrl-platform\.example\.test$`, r.View.Hostname)
	require.Equal(t, "https://"+r.View.Hostname, r.View.PublicURL)

	// The project is hidden from the list but exists, flagged.
	list, _, err := f.projects.List(f.ctx, "", 50)
	require.NoError(t, err)
	require.Empty(t, list, "the system project is not in the projects list")
	sys, err := f.q.GetSystemProject(context.Background(), f.ws.ID)
	require.NoError(t, err)
	require.True(t, sys.System)
	require.Equal(t, platform.SystemProjectName, sys.Name)
	got, err := f.projects.Get(f.ctx, sys.ID)
	require.NoError(t, err)
	require.True(t, got.System)
	_, err = f.projects.Update(f.ctx, sys.ID, projects.UpdateInput{Name: new("Renamed")})
	require.Equal(t, "system_project", apiCode(err))
	require.Equal(t, "system_project", apiCode(f.projects.Delete(f.ctx, sys.ID, projects.DeleteInput{})))

	// The service is a staged, pinned image service with a volume and the domain.
	svc := f.platformService(r)
	cfg := services.DecodeConfig(svc.Config)
	require.Contains(t, cfg.Image, "headscale/headscale:")
	require.False(t, cfg.BlockEgress, "Headscale fetches the DERP map")
	require.Equal(t, 8080, cfg.Port)
	require.Greater(t, svc.Generation, svc.AppliedGeneration, "staged until deployed")
	vols, err := f.q.ListServiceVolumes(context.Background(), svc.ID)
	require.NoError(t, err)
	require.Len(t, vols, 1)
	require.Equal(t, "/var/lib/headscale", vols[0].MountPath)
	doms, err := f.q.ListServiceDomains(context.Background(), svc.ID)
	require.NoError(t, err)
	require.Len(t, doms, 1)
	require.Equal(t, r.View.Hostname, doms[0].Hostname)
	require.Equal(t, int32(8080), doms[0].TargetPort)

	// No credential is generated at set-up: Headscale's key is minted when it is wired.
	row := f.row(r.View.ID)
	require.Empty(t, row.SecretsEnc)

	// A River job and an audit entry were written in the same transaction.
	var jobs int
	require.NoError(t, f.pool.QueryRow(context.Background(), `SELECT count(*) FROM river_job WHERE kind = 'platform_provision'`).Scan(&jobs))
	require.Equal(t, 1, jobs)
	var audits int
	require.NoError(t, f.pool.QueryRow(context.Background(), `SELECT count(*) FROM audit_events WHERE action = 'platform.provision'`).Scan(&audits))
	require.Equal(t, 1, audits)

	// One per team.
	_, err = f.svc.Provision(f.ctx, platform.ProvisionInput{Kind: platform.KindHeadscale, ServerID: f.server.ID})
	require.Equal(t, "platform_service_exists", apiCode(err))
}

func TestProvisionAuthorizationAndValidation(t *testing.T) {
	f := newFixture(t)
	dev := auth.WithPrincipal(context.Background(), &auth.Principal{Type: auth.TypeToken, UserID: f.user.ID, TokenID: new(uuid.New()), WorkspaceID: f.ws.ID, Scopes: []string{policy.ScopeProjectsWrite, policy.ScopeDeploymentsWrite}})
	_, err := f.svc.Provision(dev, platform.ProvisionInput{Kind: platform.KindHeadscale, ServerID: f.server.ID})
	require.Equal(t, http.StatusForbidden, apiStatus(err))
	_, err = f.svc.List(dev)
	require.Equal(t, http.StatusForbidden, apiStatus(err))

	_, err = f.svc.Provision(f.ctx, platform.ProvisionInput{Kind: "nope", ServerID: f.server.ID})
	require.Equal(t, "invalid_kind", apiCode(err))
	for _, kind := range []string{"s3", "loki", "grafana", "registry"} {
		_, err = f.svc.Provision(f.ctx, platform.ProvisionInput{Kind: kind, ServerID: f.server.ID})
		require.Equal(t, "invalid_kind", apiCode(err), kind)
	}
	_, err = f.svc.Provision(f.ctx, platform.ProvisionInput{Kind: platform.KindHeadscale, ServerID: uuid.New()})
	require.Equal(t, "server_not_found", apiCode(err))
	_, err = f.svc.Provision(f.ctx, platform.ProvisionInput{Kind: platform.KindHeadscale, ServerID: f.server.ID, Hostname: "bad_host"})
	require.Error(t, err)

	// Without ACME the proxy serves a self-signed certificate: https needs it.
	_, err = f.pool.Exec(context.Background(), "UPDATE servers SET acme_email = NULL WHERE id = $1", f.server.ID)
	require.NoError(t, err)
	_, err = f.svc.Provision(f.ctx, platform.ProvisionInput{Kind: platform.KindHeadscale, ServerID: f.server.ID})
	require.Equal(t, "acme_email_required", apiCode(err))
	// ... but a plain-http URL or a .localhost name is allowed (local testing).
	r := f.provision(func(in *platform.ProvisionInput) { in.Hostname = "hs.localhost" })
	require.Equal(t, "https://hs.localhost", r.View.PublicURL)
	// Nothing was written by the refused attempts.
	var n int
	require.NoError(t, f.pool.QueryRow(context.Background(), `SELECT count(*) FROM platform_services`).Scan(&n))
	require.Equal(t, 1, n)
}

func (f *fixture) makeReady(id uuid.UUID) {
	_, err := f.pool.Exec(context.Background(), `UPDATE platform_services SET status = 'ready', step = 'done' WHERE id = $1`, id)
	require.NoError(f.t, err)
}

func TestJobWaitsForTheDeploymentBeforeWiring(t *testing.T) {
	f := newFixture(t)
	f.deployer.status = "building"
	r := f.provision()
	id := r.View.ID

	// configure and deploy, then wait for the deployment.
	wait, done := f.run(id, 5)
	require.False(t, done)
	require.Positive(t, wait)
	require.Equal(t, 1, f.deployer.calls)
	require.Equal(t, platform.StepAwait, f.row(id).Step)
	require.Empty(t, f.tailnet.put, "nothing is wired before the deployment succeeded")
}

func TestFailedDeploymentFailsAndRetryRedeploys(t *testing.T) {
	f := newFixture(t)
	f.deployer.status = "failed"
	r := f.provision()
	_, done := f.run(r.View.ID, 6)
	require.True(t, done)
	row := f.row(r.View.ID)
	require.Equal(t, platform.StatusFailed, row.Status)
	require.Contains(t, row.StatusMessage, "failed")
	require.Empty(t, f.tailnet.put)

	_, err := f.svc.Rewire(f.ctx, r.View.ID)
	require.Equal(t, "not_ready", apiCode(err))
	f.deployer.status = "succeeded"
	_, err = f.pool.Exec(context.Background(), `DELETE FROM deployments`)
	require.NoError(t, err)
	v, err := f.svc.Retry(f.ctx, r.View.ID)
	require.NoError(t, err)
	require.Equal(t, platform.StatusProvisioning, v.Status)
	require.Equal(t, platform.StepDeploy, v.Step, "a failed deployment is deployed again")
	_, err = f.svc.Retry(f.ctx, r.View.ID)
	require.Equal(t, "not_failed", apiCode(err))
}

func TestHeadscaleWiringMintsAKeyThroughExecAndConnectsTheIntegration(t *testing.T) {
	f := newFixture(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{"status":"pass"}`)) }))
	defer srv.Close()
	f.agents.out["headscale users create"] = "User created"
	f.agents.out["headscale apikeys create"] = "\x1b[0mhskey-api-abc_def-ghi\n"
	r := f.provision()
	_, err := f.pool.Exec(context.Background(), `UPDATE platform_services SET public_url = $2 WHERE id = $1`, r.View.ID, srv.URL)
	require.NoError(t, err)

	f.run(r.View.ID, 8)
	row := f.row(r.View.ID)
	require.Equal(t, platform.StatusReady, row.Status, row.StatusMessage)
	require.Len(t, f.tailnet.put, 1)
	p := f.tailnet.put[0]
	require.Equal(t, tailscale.ProviderHeadscale, p.Provider)
	require.Equal(t, srv.URL, p.LoginServer)
	require.Equal(t, "hskey-api-abc_def-ghi", p.ClientSecret)
	require.Equal(t, "nebulactrl", p.HeadscaleUser)
	require.Equal(t, tailscale.HeadscaleAPIKey, p.HeadscaleCredential)
	require.Equal(t, [][]string{
		{"headscale", "users", "create", "nebulactrl"},
		{"headscale", "apikeys", "create", "--expiration", "365d"},
	}, f.agents.ran)
	v, err := f.svc.Get(f.ctx, r.View.ID)
	require.NoError(t, err)
	require.NotEmpty(t, v.Details["apiKeyExpires"])
	raw, _ := json.Marshal(v)
	require.NotContains(t, string(raw), "hskey-api")

	// "Reconnect" mints a new key.
	f.agents.out["headscale apikeys create"] = "hskey-api-second\n"
	_, err = f.svc.Rewire(f.ctx, r.View.ID)
	require.NoError(t, err)
	f.run(r.View.ID, 4)
	require.Len(t, f.tailnet.put, 2)
	require.Equal(t, "hskey-api-second", f.tailnet.put[1].ClientSecret)
}

func TestHeadscaleRefusesToReplaceAnIntegrationUnlessAsked(t *testing.T) {
	f := newFixture(t)
	f.tailnet.cur = tailscale.TailscaleIntegration{Configured: true, Provider: "tailscale_oauth"}
	_, err := f.svc.Provision(f.ctx, platform.ProvisionInput{Kind: platform.KindHeadscale, ServerID: f.server.ID})
	require.Equal(t, "integration_exists", apiCode(err))
	r := f.provision(func(in *platform.ProvisionInput) { in.Replace = true })
	require.Equal(t, platform.StatusProvisioning, r.View.Status)
}

func TestRemoveUnwiresFirstAndRefusesWhileInUse(t *testing.T) {
	f := newFixture(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("{}")) }))
	defer srv.Close()
	f.agents.out["headscale users create"] = "User created"
	f.agents.out["headscale apikeys create"] = "hskey-api-x\n"
	r := f.provision()
	_, err := f.pool.Exec(context.Background(), `UPDATE platform_services SET public_url = $2 WHERE id = $1`, r.View.ID, srv.URL)
	require.NoError(t, err)
	f.run(r.View.ID, 8)
	require.Equal(t, platform.StatusReady, f.row(r.View.ID).Status)

	err = f.svc.Remove(f.ctx, r.View.ID, platform.RemoveInput{Confirm: "wrong"})
	require.Equal(t, "confirm_mismatch", apiCode(err))

	f.tailnet.deleteEr = apierr.Conflict("integration_in_use", "the tailnet is in use")
	err = f.svc.Remove(f.ctx, r.View.ID, platform.RemoveInput{Confirm: platform.KindHeadscale})
	require.Equal(t, "integration_in_use", apiCode(err))
	require.Equal(t, platform.StatusReady, f.row(r.View.ID).Status, "nothing changed")
	svc := f.platformService(r)
	require.Nil(t, svc.DeletedAt, "the service is untouched")

	f.tailnet.deleteEr = nil
	require.NoError(t, f.svc.Remove(f.ctx, r.View.ID, platform.RemoveInput{Confirm: platform.KindHeadscale, DeleteData: true}))
	require.True(t, f.tailnet.deleted)
	_, err = f.q.GetPlatformServiceInternal(context.Background(), r.View.ID)
	require.ErrorIs(t, err, pgx.ErrNoRows)
	gone := f.platformService2(svc.ID)
	require.NotNil(t, gone.DeletedAt)

	// It can be set up again.
	_ = f.provision(func(in *platform.ProvisionInput) { in.Hostname = "hs-b.example.test"; in.Replace = true })
}

func (f *fixture) platformService2(id uuid.UUID) db.Service {
	rows, err := f.q.ListServicesIncludingDeleted(context.Background(), []uuid.UUID{id})
	require.NoError(f.t, err)
	require.Len(f.t, rows, 1)
	return rows[0]
}

func TestRemoveHeadscaleKeepsAForeignIntegration(t *testing.T) {
	f := newFixture(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("{}")) }))
	defer srv.Close()
	f.agents.out["headscale users create"] = "User created"
	f.agents.out["headscale apikeys create"] = "hskey-api-x\n"
	r := f.provision()
	_, err := f.pool.Exec(context.Background(), `UPDATE platform_services SET public_url = $2 WHERE id = $1`, r.View.ID, srv.URL)
	require.NoError(t, err)
	f.run(r.View.ID, 8)
	require.Equal(t, platform.StatusReady, f.row(r.View.ID).Status)

	// The owner pointed the integration somewhere else meanwhile: it stays.
	f.tailnet.cur = tailscale.TailscaleIntegration{Configured: true, Provider: "headscale", LoginServer: "https://other.example.com"}
	require.NoError(t, f.svc.Remove(f.ctx, r.View.ID, platform.RemoveInput{Confirm: platform.KindHeadscale}))
	require.False(t, f.tailnet.deleted)

	// Ours is removed.
	r2 := f.provision(func(in *platform.ProvisionInput) { in.Replace = true })
	_, err = f.pool.Exec(context.Background(), `UPDATE platform_services SET public_url = $2 WHERE id = $1`, r2.View.ID, srv.URL)
	require.NoError(t, err)
	f.tailnet.cur = tailscale.TailscaleIntegration{}
	f.run(r2.View.ID, 8)
	require.NoError(t, f.svc.Remove(f.ctx, r2.View.ID, platform.RemoveInput{Confirm: platform.KindHeadscale}))
	require.True(t, f.tailnet.deleted)
}

func TestBackupOfDataIsScheduledOnlyToAnExistingExternalDestination(t *testing.T) {
	f := newFixture(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("{}")) }))
	defer srv.Close()
	f.agents.out["headscale users create"] = "User created"
	f.agents.out["headscale apikeys create"] = "hskey-api-x\n"

	// No destination, or only the built-in one (it lives on the same host): ready,
	// not backed up, with a hint.
	f.backups.existing = []backups.DestinationView{{ID: uuid.New(), Name: "Built-in backup storage", Kind: "builtin"}}
	r := f.provision()
	_, err := f.pool.Exec(context.Background(), `UPDATE platform_services SET public_url = $2 WHERE id = $1`, r.View.ID, srv.URL)
	require.NoError(t, err)
	f.run(r.View.ID, 8)
	v, err := f.svc.Get(f.ctx, r.View.ID)
	require.NoError(t, err)
	require.False(t, v.Backup.Configured)
	require.Contains(t, v.Backup.Hint, "Not backed up")
	require.Empty(t, f.backups.volumeCfg)

	// With an external destination, reconnecting schedules the backup.
	dest := uuid.New()
	f.backups.existing = append(f.backups.existing, backups.DestinationView{ID: dest, Name: "Offsite", Kind: agentprotocol.DestS3})
	_, err = f.svc.Rewire(f.ctx, r.View.ID)
	require.NoError(t, err)
	f.run(r.View.ID, 4)
	v, err = f.svc.Get(f.ctx, r.View.ID)
	require.NoError(t, err)
	require.True(t, v.Backup.Configured)
	cfg := f.backups.volumeCfg[f.row(r.View.ID).ServiceIds[0]]
	require.Equal(t, dest, cfg.DestinationID)
	require.Equal(t, "17 3 * * *", cfg.Cron)
	require.True(t, cfg.Enabled)
}

func TestASystemServiceDeletedElsewhereIsReported(t *testing.T) {
	f := newFixture(t)
	r := f.provision()
	f.makeReady(r.View.ID)
	_, err := f.pool.Exec(context.Background(), `UPDATE services SET deleted_at = now()`)
	require.NoError(t, err)
	v, err := f.svc.Get(f.ctx, r.View.ID)
	require.NoError(t, err)
	require.Equal(t, "missing", v.Services[0].State)
	require.Contains(t, strings.Join(v.Warnings, " "), "removed outside this page")
	// It can still be removed.
	require.NoError(t, f.svc.Remove(f.ctx, r.View.ID, platform.RemoveInput{Confirm: platform.KindHeadscale}))
}

func TestListShowsTheCatalogWithTheTeamsInstance(t *testing.T) {
	f := newFixture(t)
	l, err := f.svc.List(f.ctx)
	require.NoError(t, err)
	require.Len(t, l.Kinds, 1)
	require.Equal(t, platform.KindHeadscale, l.Kinds[0].Kind)
	require.Nil(t, l.ProjectID)
	require.Nil(t, l.Kinds[0].Instance)
	f.provision()
	l, err = f.svc.List(f.ctx)
	require.NoError(t, err)
	require.NotNil(t, l.ProjectID)
	require.NotNil(t, l.Kinds[0].Instance)

	// Another team sees nothing of it.
	ws2, err := f.q.CreateWorkspace(context.Background(), "w2")
	require.NoError(t, err)
	other := auth.WithPrincipal(context.Background(), &auth.Principal{Type: auth.TypeUser, UserID: f.user.ID, WorkspaceID: ws2.ID, Role: auth.RoleOwner, Scopes: policy.AllScopes})
	l2, err := f.svc.List(other)
	require.NoError(t, err)
	require.Nil(t, l2.Kinds[0].Instance)
	_, err = f.svc.Get(other, f.row(l.Kinds[0].Instance.ID).ID)
	require.Equal(t, http.StatusNotFound, apiStatus(err), "no peeking across teams")
}

func apiCode(err error) string {
	if ae, ok := apierr.As(err); ok {
		return ae.Code
	}
	return ""
}

func apiStatus(err error) int {
	if ae, ok := apierr.As(err); ok {
		return ae.Status
	}
	return 0
}

func TestSystemProjectIsInvisibleToNonAdministrators(t *testing.T) {
	f := newFixture(t)
	r := f.provision()
	sys, err := f.q.GetSystemProject(context.Background(), f.ws.ID)
	require.NoError(t, err)
	svc := f.platformService(r)
	dev := auth.WithPrincipal(context.Background(), &auth.Principal{
		Type: auth.TypeUser, UserID: f.user.ID, WorkspaceID: f.ws.ID, Role: auth.RoleDeveloper,
		Scopes: policy.ScopesForRole(auth.RoleDeveloper),
	})

	_, err = f.projects.Get(dev, sys.ID)
	require.Equal(t, http.StatusNotFound, apiStatus(err), "the project")
	_, err = f.svcs.Get(dev, svc.ID)
	require.Equal(t, http.StatusNotFound, apiStatus(err), "the service")
	_, _, err = f.svcs.List(dev, *f.row(r.View.ID).EnvironmentID, "", 50)
	require.Equal(t, http.StatusNotFound, apiStatus(err), "the environment's services")
	_, err = f.svcs.Restart(dev, svc.ID)
	require.Equal(t, http.StatusNotFound, apiStatus(err), "actions on it")

	// Administrators still see it.
	_, err = f.projects.Get(f.ctx, sys.ID)
	require.NoError(t, err)
	_, err = f.svcs.Get(f.ctx, svc.ID)
	require.NoError(t, err)
}
