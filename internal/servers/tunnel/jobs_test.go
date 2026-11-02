package tunnel

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/integrations/cloudflare"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// brokenCF is a Cloudflare connection whose client lookup fails with err.
type brokenCF struct{ err error }

func (b brokenCF) Client(context.Context, uuid.UUID) (API, string, error) { return nil, "", b.err }
func (brokenCF) EnqueueSyncServer(context.Context, pgx.Tx, uuid.UUID, uuid.UUID) error {
	return nil
}

func TestJobKindsAndUniquenessAreStable(t *testing.T) {
	// Kinds are stored in queued jobs: renaming one would strand them.
	require.Equal(t, "server_tunnel_delete", DeleteArgs{}.Kind())
	require.Equal(t, "server_tunnel_ingress", IngressArgs{}.Kind())
	require.Equal(t, "server_tunnel_sweep", SweepArgs{}.Kind())

	del, ing := DeleteArgs{}.InsertOpts(), IngressArgs{}.InsertOpts()
	require.Equal(t, 8, del.MaxAttempts, "the delete is retried for a long time")
	require.Equal(t, 5, ing.MaxAttempts)
	require.True(t, del.UniqueOpts.ByArgs, "one pending delete per server")
	require.True(t, ing.UniqueOpts.ByArgs, "one pending ingress push per server")

	require.Greater(t, (&sweepWorker{}).Timeout(&river.Job[SweepArgs]{}), (&ingressWorker{}).Timeout(&river.Job[IngressArgs]{}),
		"the sweep walks every tunnel and gets more time than one push")
	require.Equal(t, (&deleteWorker{}).Timeout(&river.Job[DeleteArgs]{}), (&ingressWorker{}).Timeout(&river.Job[IngressArgs]{}))
}

func TestDeleteWorkerDeletesTheTunnelAtCloudflare(t *testing.T) {
	f := newFixture(t)
	f.enable()
	_, err := f.svc.Disable(f.admin, f.server.ID, false)
	require.NoError(t, err)
	w := &deleteWorker{svc: f.svc}
	job := &river.Job[DeleteArgs]{Args: DeleteArgs{ServerID: f.server.ID}}

	require.NoError(t, w.Work(bg, job))
	require.True(t, f.api.has("DELETE /accounts/"+account+"/cfd_tunnel/"+tunnelID))
	_, err = f.q.GetServerTunnel(bg, f.server.ID)
	require.ErrorIs(t, err, pgx.ErrNoRows, "the row goes with the remote tunnel")

	require.NoError(t, w.Work(bg, job), "a job that runs twice is a no-op")
}

func TestDeleteWorkerRetriesTransientFailuresButNotPermanentOnes(t *testing.T) {
	f := newFixture(t)
	f.enable()
	_, err := f.svc.Disable(f.admin, f.server.ID, false)
	require.NoError(t, err)
	w := &deleteWorker{svc: f.svc}
	job := &river.Job[DeleteArgs]{Args: DeleteArgs{ServerID: f.server.ID}}

	f.svc.cf = brokenCF{err: errors.New("dial tcp: connection refused")}
	require.Error(t, w.Work(bg, job), "a network failure is retried by the queue")
	require.Contains(t, f.row().Message, "connection refused")
	require.Equal(t, "removing", f.row().State)

	f.svc.cf = brokenCF{err: cloudflare.ErrNotConfigured}
	require.NoError(t, w.Work(bg, job), "without a Cloudflare connection retrying cannot help")
	require.Contains(t, f.row().Message, "Cloudflare is not connected.")
}

func TestIngressWorkerPushesChangedHostnamesAndSwallowsPermanentFailures(t *testing.T) {
	f := newFixture(t)
	f.enable()
	w := &ingressWorker{svc: f.svc}
	job := &river.Job[IngressArgs]{Args: IngressArgs{ServerID: f.server.ID}}

	f.deployed("web", "new.example.com", true)
	require.NoError(t, w.Work(bg, job))
	hosts, _ := f.api.ingressHosts(t)
	require.Equal(t, []string{"new.example.com"}, hosts)

	// A rejected token is permanent: the job ends, the reason stays on the tunnel for the sweep.
	f.deployed("api", "other.example.com", true)
	f.svc.cf = brokenCF{err: cloudflare.ErrAuth}
	require.NoError(t, w.Work(bg, job))
	require.Contains(t, f.row().IngressError, "lacks the Cloudflare Tunnel permission")

	// A network failure is returned so the queue retries it.
	f.svc.cf = brokenCF{err: errors.New("i/o timeout")}
	require.Error(t, w.Work(bg, job))
	require.Contains(t, f.row().IngressError, "i/o timeout")
}

func TestSweepWorkerKeepsGoingWhenOneTunnelFails(t *testing.T) {
	f := newFixture(t)
	f.enable()
	f.activate()
	f.deployed("web", "new.example.com", true)
	f.svc.cf = brokenCF{err: cloudflare.ErrAuth}

	require.NoError(t, (&sweepWorker{svc: f.svc}).Work(bg, &river.Job[SweepArgs]{}), "a failing tunnel never fails the sweep")
	require.Contains(t, f.row().IngressError, "lacks the Cloudflare Tunnel permission")
}

func TestSweepResendsConnectorOfAStaleObservationButNotForOfflineServers(t *testing.T) {
	f := newFixture(t)
	f.enable()
	f.activate()
	syncs := func() int { return len(f.disp.of(agentprotocol.KindSyncTunnel)) }
	require.Equal(t, 1, syncs())

	// Online and fresh: nothing to do, even long after the last command.
	_, err := f.pool.Exec(bg, `UPDATE server_tunnels SET observed_at = now(), connector_synced_at = now() - interval '10 minutes' WHERE server_id = $1`, f.server.ID)
	require.NoError(t, err)
	require.NoError(t, f.svc.Sweep(bg))
	require.Equal(t, 1, syncs())

	// The agent has not reported for a while: the command is sent again.
	_, err = f.pool.Exec(bg, `UPDATE server_tunnels SET observed_at = now() - interval '10 minutes' WHERE server_id = $1`, f.server.ID)
	require.NoError(t, err)
	require.NoError(t, f.svc.Sweep(bg))
	require.Equal(t, 2, syncs(), "an unobserved connector is re-synced")

	// A server that is not online cannot be told anything.
	_, err = f.pool.Exec(bg, `UPDATE server_tunnels SET observed_at = NULL, connector_synced_at = now() - interval '10 minutes' WHERE server_id = $1`, f.server.ID)
	require.NoError(t, err)
	_, err = f.pool.Exec(bg, `UPDATE servers SET status = 'offline' WHERE id = $1`, f.server.ID)
	require.NoError(t, err)
	require.NoError(t, f.svc.Sweep(bg))
	require.Equal(t, 2, syncs())

	// A connector never observed since enabling is retried once the server is back.
	_, err = f.pool.Exec(bg, `UPDATE servers SET status = 'online' WHERE id = $1`, f.server.ID)
	require.NoError(t, err)
	require.NoError(t, f.svc.Sweep(bg))
	require.Equal(t, 3, syncs())

	// An agent too old to run the connector is left alone.
	_, err = f.pool.Exec(bg, `UPDATE server_tunnels SET connector_synced_at = now() - interval '10 minutes' WHERE server_id = $1`, f.server.ID)
	require.NoError(t, err)
	_, err = f.pool.Exec(bg, `UPDATE servers SET facts = '{"os":"linux","capabilities":[]}' WHERE id = $1`, f.server.ID)
	require.NoError(t, err)
	require.NoError(t, f.svc.Sweep(bg))
	require.Equal(t, 3, syncs())
}

func TestSecretsForCommandFetchesTheTokenOnlyForSyncCommands(t *testing.T) {
	f := newFixture(t)
	f.enable()

	sec, err := f.svc.SecretsForCommand(bg, agentprotocol.KindRemoveTunnel, nil)
	require.NoError(t, err)
	require.Empty(t, sec, "removing the connector needs no token")

	_, err = f.svc.SecretsForCommand(bg, agentprotocol.KindSyncTunnel, []byte(`{not json`))
	require.ErrorContains(t, err, "decode sync_tunnel payload")

	_, err = f.svc.SecretsForCommand(bg, agentprotocol.KindSyncTunnel, []byte(`{"tunnel":{"tunnelId":"someone-elses"}}`))
	require.ErrorContains(t, err, "no longer exists")

	payload := []byte(`{"tunnel":{"tunnelId":"` + tunnelID + `"}}`)
	f.svc.cf = brokenCF{err: cloudflare.ErrNotConfigured}
	_, err = f.svc.SecretsForCommand(bg, agentprotocol.KindSyncTunnel, payload)
	require.EqualError(t, err, "Cloudflare is not connected.")
	f.svc.cf = brokenCF{err: cloudflare.ErrAuth}
	_, err = f.svc.SecretsForCommand(bg, agentprotocol.KindSyncTunnel, payload)
	require.ErrorContains(t, err, "lacks the Cloudflare Tunnel permission")

	f.svc.cf = f.cf
	_, err = f.svc.Disable(f.admin, f.server.ID, false)
	require.NoError(t, err)
	_, err = f.svc.SecretsForCommand(bg, agentprotocol.KindSyncTunnel, payload)
	require.ErrorContains(t, err, "being removed", "no token is minted for a tunnel on its way out")
}

func TestHandlesOnlyTunnelCommands(t *testing.T) {
	require.True(t, Handles(agentprotocol.KindSyncTunnel))
	require.True(t, Handles(agentprotocol.KindRemoveTunnel))
	require.False(t, Handles(agentprotocol.KindSyncRoutes))
	require.False(t, Handles(""))
}

func TestAgentResultsForOtherCommandsAndMalformedPayloadsAreIgnored(t *testing.T) {
	f := newFixture(t)
	f.enable()
	store := func(kind string, payload []byte, key string) uuid.UUID {
		t.Helper()
		c, err := f.q.InsertAgentCommand(bg, db.InsertAgentCommandParams{
			ID: uuid.New(), ServerID: f.server.ID, Kind: kind, Payload: payload, IdempotencyKey: key, ExpiresAt: time.Now().Add(time.Hour),
		})
		require.NoError(t, err)
		return c.ID
	}
	ok := agentprotocol.ResultFrame{Outcome: agentprotocol.OutcomeFailed, Error: "boom"}

	// An unknown command id and a command of another kind are not ours.
	require.NoError(t, f.svc.HandleResult(bg, f.server.ID, uuid.New(), ok))
	require.NoError(t, f.svc.HandleResult(bg, f.server.ID, store(agentprotocol.KindRemoveTunnel, []byte(`{}`), "k-rm"), ok))
	require.NoError(t, f.svc.HandleUndelivered(bg, uuid.New(), "gone"))
	require.NoError(t, f.svc.HandleUndelivered(bg, store(agentprotocol.KindSyncRoutes, []byte(`{}`), "k-rt"), "undelivered"))
	require.Equal(t, "provisioning", f.row().State, "none of those touched the tunnel")
	require.Empty(t, f.row().Message)

	// A stored payload that cannot be parsed is logged and dropped, never an error that blocks the hub.
	bad := store(agentprotocol.KindSyncTunnel, []byte(`"not an object"`), "k-bad")
	require.NoError(t, f.svc.HandleResult(bg, f.server.ID, bad, ok))
	require.NoError(t, f.svc.HandleUndelivered(bg, bad, "undelivered"))
	require.Equal(t, "provisioning", f.row().State)

	// A successful result without a known state, or with an unreadable one, needs attention with a generic reason.
	good := store(agentprotocol.KindSyncTunnel, f.disp.of(agentprotocol.KindSyncTunnel)[0].Payload, "k-good")
	require.NoError(t, f.svc.HandleResult(bg, f.server.ID, good, agentprotocol.ResultFrame{Outcome: agentprotocol.OutcomeSucceeded, Data: []byte(`{"state":"weird"}`)}))
	require.Equal(t, "needs_attention", f.row().State)
	require.Equal(t, "The Cloudflare connector could not be started.", f.row().Message)
}

func TestTunnelActivatedHookMovesDomainsOntoTheTunnel(t *testing.T) {
	f := newFixture(t)
	f.deployed("web", "app.example.com", true)
	f.enable()

	f.svc.TunnelActivated(bg, f.server.ID)
	require.Equal(t, []uuid.UUID{f.server.ID}, f.cf.syncs, "the domains' DNS is re-synced")
	require.Len(t, f.disp.of(agentprotocol.KindSyncRoutes), 1)
	var kind string
	require.NoError(t, f.pool.QueryRow(bg, `SELECT kind FROM river_job WHERE kind = 'server_tunnel_ingress'`).Scan(&kind), "the ingress rules are verified in the background")

	// Without a tunnel (or while removing) there is nothing to activate.
	other := newFixture(t)
	other.svc.TunnelActivated(bg, other.server.ID)
	require.Zero(t, other.cf.syncCount())
}

func TestServerRevokedWithoutATunnelDoesNothing(t *testing.T) {
	f := newFixture(t)
	f.svc.ServerRevoked(bg, f.ws, f.server.ID)
	require.Empty(t, f.deleteJobArgs())

	f.enable()
	_, err := f.svc.Disable(f.admin, f.server.ID, false)
	require.NoError(t, err)
	f.svc.ServerRevoked(bg, f.ws, f.server.ID)
	require.Len(t, f.deleteJobArgs(), 1, "a tunnel that is already being removed is not removed twice")
}

func TestEnableNeedsTheCloudflareAccount(t *testing.T) {
	f := newFixture(t)
	f.svc.cf = accountlessCF{f.cf}

	_, err := f.svc.Enable(f.admin, f.server.ID)
	require.Equal(t, "cloudflare_account_unknown", apiCode(t, err))
	require.Zero(t, f.api.count("POST "), "no tunnel is created without an account")

	v, err := f.svc.Get(f.admin, f.server.ID)
	require.NoError(t, err)
	require.True(t, v.CloudflareConnected)
	require.False(t, v.AccountReady, "the UI asks the user to pick an account")
}

// accountlessCF is a connected Cloudflare whose account is not chosen yet.
type accountlessCF struct{ *fakeCF }

func (a accountlessCF) Client(ctx context.Context, ws uuid.UUID) (API, string, error) {
	c, _, err := a.fakeCF.Client(ctx, ws)
	return c, "", err
}

func TestTunnelNameIsADNSFriendlySlugOfTheServerName(t *testing.T) {
	id := uuid.MustParse("5a1b2c3d-0000-4000-8000-123456789abc")
	short := agentprotocol.Short(id.String())
	require.Equal(t, "nebulactrl-agent-1-"+short, tunnelName(db.Server{ID: id, Name: "Agent 1"}))
	require.Equal(t, "nebulactrl-server-"+short, tunnelName(db.Server{ID: id, Name: "   !!! "}), "a name with no usable characters falls back")
	long := tunnelName(db.Server{ID: id, Name: strings.Repeat("a", 60) + " tail"})
	require.Equal(t, "nebulactrl-"+strings.Repeat("a", 40)+"-"+short, long, "the slug is capped")
}

func TestSupportsTunnelAssumesNewAgentsUntilFactsArrive(t *testing.T) {
	require.True(t, SupportsTunnel(db.Server{}), "no facts yet: a freshly enrolled agent")
	require.True(t, SupportsTunnel(db.Server{Facts: []byte(`{`)}), "unreadable facts do not block")
	require.True(t, SupportsTunnel(db.Server{Facts: []byte(`{"capabilities":[]}`)}), "facts without an OS are not facts")
	require.False(t, SupportsTunnel(db.Server{Facts: []byte(`{"os":"linux","capabilities":["proxy_v2"]}`)}))
	require.True(t, SupportsTunnel(db.Server{Facts: []byte(`{"os":"linux","capabilities":["` + agentprotocol.CapTunnel + `"]}`)}))
}

func TestAPIErrorMapping(t *testing.T) {
	require.Equal(t, "cloudflare_account_unknown", apiCode(t, apiError(cloudflare.ErrNoAccount, "x")))
	require.Equal(t, "cloudflare_not_connected", apiCode(t, apiError(cloudflare.ErrNotConfigured, "x")))
	require.Equal(t, "cloudflare_tunnel_forbidden", apiCode(t, apiError(cloudflare.ErrAuth, "x")))
	plain := errors.New("socket closed")
	require.ErrorIs(t, apiError(plain, "push rules"), plain, "anything else keeps its cause and gets context")
	require.ErrorContains(t, apiError(plain, "push rules"), "push rules: socket closed")
}

func TestAdapterReadsTheTeamsCloudflareConnection(t *testing.T) {
	f := newFixture(t)
	box, err := secretbox.New(bytes.Repeat([]byte{5}, 32))
	require.NoError(t, err)
	cf := cloudflare.NewService(f.pool, box, nil, nil)
	ad := Adapter{S: cf}

	_, _, err = ad.Client(bg, f.ws)
	require.ErrorIs(t, err, cloudflare.ErrNotConfigured, "a team without a connection")

	tok, err := box.SealString(secretbox.CloudflareConnectionsToken, f.ws.String(), "cf-token-value")
	require.NoError(t, err)
	_, err = f.pool.Exec(bg, `INSERT INTO cloudflare_connections (workspace_id, token_enc, account_id, account_name) VALUES ($1, $2, $3, 'Acme')`, f.ws, tok, account)
	require.NoError(t, err)
	client, acct, err := ad.Client(bg, f.ws)
	require.NoError(t, err)
	require.NotNil(t, client)
	require.Equal(t, account, acct)

	// A token sealed under another key cannot be opened: the error says so.
	other, err := secretbox.New(bytes.Repeat([]byte{6}, 32))
	require.NoError(t, err)
	_, _, err = Adapter{S: cloudflare.NewService(f.pool, other, nil, nil)}.Client(bg, f.ws)
	require.ErrorContains(t, err, "decrypt cloudflare token")

	// Nothing to re-sync for a server without domains: the call succeeds inside a transaction.
	require.NoError(t, db.InTx(bg, f.pool, func(tx pgx.Tx) error { return ad.EnqueueSyncServer(bg, tx, f.ws, f.server.ID) }))
}

// racyCF creates the server's tunnel row while Enable is talking to Cloudflare:
// two admins enabling the same server at once.
type racyCF struct {
	*fakeCF
	f *fixture
}

func (r racyCF) Client(ctx context.Context, ws uuid.UUID) (API, string, error) {
	_, err := r.f.pool.Exec(ctx, `INSERT INTO server_tunnels (server_id, workspace_id, account_id, tunnel_id, tunnel_name) VALUES ($1, $2, $3, 'other-tunnel', 'other')
		ON CONFLICT DO NOTHING`, r.f.server.ID, ws, account)
	if err != nil {
		return nil, "", err
	}
	return r.fakeCF.Client(ctx, ws)
}

func TestConcurrentEnableLosesCleanlyAndDeletesItsTunnel(t *testing.T) {
	f := newFixture(t)
	f.svc.cf = racyCF{fakeCF: f.cf, f: f}

	_, err := f.svc.Enable(f.admin, f.server.ID)
	require.Equal(t, "tunnel_exists", apiCode(t, err), "the other enable won")
	require.True(t, f.api.has("DELETE /accounts/"+account+"/cfd_tunnel/"+tunnelID), "the tunnel this call created is not left at Cloudflare")
	require.Equal(t, "other-tunnel", f.row().TunnelID, "the winner's tunnel row is untouched")
	require.Empty(t, f.disp.of(agentprotocol.KindSyncTunnel), "no connector was queued for the loser")
}
