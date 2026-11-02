package servers_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/config"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/jobs"
	"github.com/nebulactrl/nebulactrl/internal/servers"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

func (f *unrestrictedFixture) jobs() *jobs.Manager {
	return jobs.New(f.pool, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func TestJobKindsAreStable(t *testing.T) {
	// Kinds are stored in queued jobs: renaming one strands them.
	require.Equal(t, "prune_metrics", servers.PruneMetricsArgs{}.Kind())
	require.Equal(t, "downsample_metrics_1m", servers.DownsampleMetrics1mArgs{}.Kind())
	require.Equal(t, "downsample_metrics_15m", servers.DownsampleMetrics15mArgs{}.Kind())
	require.Equal(t, "remove_server", servers.RemoveServerArgs{}.Kind())
	require.Equal(t, 1, servers.RemoveServerArgs{}.InsertOpts().MaxAttempts, "a failed removal is shown to the person, not retried by the queue")
}

func TestMetricsJobsFoldPruneAndRegister(t *testing.T) {
	f, _ := newUnrestrictedFixture(t)
	bg := context.Background()
	jm := f.jobs()

	servers.RegisterJobs(jm)
	client, err := jm.Client()
	require.NoError(t, err, "the registered workers and periodic jobs form a valid queue")
	require.NotNil(t, client)

	_, err = f.pool.Exec(bg, `
		INSERT INTO metric_samples (server_id, ts, cpu_percent, mem_bytes, mem_limit_bytes, disk_used_bytes, disk_total_bytes) VALUES
		($1, date_trunc('minute', now() - interval '10 minutes') + interval '5 seconds',  10, 100, 1000, 50, 100),
		($1, date_trunc('minute', now() - interval '10 minutes') + interval '20 seconds', 30, 300, 1000, 60, 100),
		($1, now() - interval '60 hours', 5, 100, 1000, 50, 100)`, f.server.ID)
	require.NoError(t, err)

	require.NoError(t, servers.RunDownsample1mJobForTest(bg, jm))
	var cpu float64
	var samples int
	require.NoError(t, f.pool.QueryRow(bg, `SELECT cpu_avg, samples FROM metric_samples_1m WHERE server_id = $1`, f.server.ID).Scan(&cpu, &samples))
	require.InDelta(t, 20, cpu, 0.001)
	require.Equal(t, 2, samples)

	require.NoError(t, servers.RunDownsample15mJobForTest(bg, jm))
	require.NoError(t, f.pool.QueryRow(bg, `SELECT cpu_avg, samples FROM metric_samples_15m WHERE server_id = $1`, f.server.ID).Scan(&cpu, &samples))
	require.InDelta(t, 20, cpu, 0.001)
	require.Equal(t, 2, samples)

	// The prune job drops the raw sample older than the raw retention and keeps the recent ones.
	require.NoError(t, servers.RunPruneJobForTest(bg, jm))
	var raw int
	require.NoError(t, f.pool.QueryRow(bg, `SELECT count(*) FROM metric_samples WHERE server_id = $1`, f.server.ID).Scan(&raw))
	require.Equal(t, 2, raw)
	require.NoError(t, servers.RunPruneJobForTest(bg, jm), "pruning twice is harmless")
}

func TestRemovalJobRunsThroughTheQueue(t *testing.T) {
	f := newRemoveFixture(t)
	f.goOnline(t, f.server.ID, true)
	f.svc.SetRemovalQueue(nil) // the real queue: a river job inserted in the request's transaction
	jm := f.jobs()
	servers.RegisterRemovalJob(jm, f.svc)
	f.svc.SetJobs(jm)

	res, err := f.svc.Remove(f.admin, f.server.ID, servers.RemoveInput{Confirm: "edge"})
	require.NoError(t, err)
	require.False(t, res.Removed)

	var raw []byte
	require.NoError(t, f.pool.QueryRow(context.Background(), `SELECT args FROM river_job WHERE kind = 'remove_server'`).Scan(&raw))
	var args servers.RemoveServerArgs
	require.NoError(t, json.Unmarshal(raw, &args))
	require.Equal(t, f.server.ID, args.ServerID)
	require.True(t, args.Guard, "the job re-checks for placed services")
	require.Equal(t, "user", args.ActorType, "the audit row names who asked")

	require.Greater(t, servers.RemovalJobTimeoutForTest(f.svc), servers.DecommissionTimeout, "the runner waits longer than the agent may take")
	require.NoError(t, servers.RunRemovalJobForTest(context.Background(), f.svc, args))
	require.False(t, f.exists(t, f.server.ID))
	require.Len(t, f.audit(t, servers.ActionRemoved), 1)
}

func TestRemovalNeedsAJobManager(t *testing.T) {
	f := newRemoveFixture(t)
	f.goOnline(t, f.server.ID, true)
	f.svc.SetRemovalQueue(nil)

	_, err := f.svc.Remove(f.admin, f.server.ID, servers.RemoveInput{Confirm: "edge"})
	require.ErrorContains(t, err, "no job manager to run the removal")
	sv, err := f.svc.Get(f.admin, f.server.ID)
	require.NoError(t, err)
	require.Nil(t, sv.Removal, "a removal that could not be queued leaves no removing state behind")
}

// ---- hooks ----

type recordingDNS struct{ calls []uuid.UUID }

func (d *recordingDNS) EnqueueSyncServer(_ context.Context, _ pgx.Tx, _, serverID uuid.UUID) error {
	d.calls = append(d.calls, serverID)
	return nil
}

func TestPublicAddressChangeResyncsDNSOnlyWhenItChanged(t *testing.T) {
	f, _ := newUnrestrictedFixture(t)
	dns := &recordingDNS{}
	f.svc.SetDNS(dns)
	set := func(ip string) {
		t.Helper()
		_, err := f.svc.Update(f.admin, f.server.ID, servers.UpdateInput{PublicIP: &ip})
		require.NoError(t, err)
	}

	set("203.0.113.5")
	require.Equal(t, []uuid.UUID{f.server.ID}, dns.calls, "the domains follow the new address")
	set("203.0.113.5")
	require.Len(t, dns.calls, 1, "the same address again changes nothing")
	set("203.0.113.6")
	require.Len(t, dns.calls, 2)
	set("")
	require.Len(t, dns.calls, 3, "clearing the address moves the domains too")
	set("")
	require.Len(t, dns.calls, 3)

	name := "renamed"
	_, err := f.svc.Update(f.admin, f.server.ID, servers.UpdateInput{Name: &name})
	require.NoError(t, err)
	require.Len(t, dns.calls, 3, "a rename does not touch DNS")
}

func TestRevokeRunsTheHooksInRegistrationOrder(t *testing.T) {
	f, _ := newUnrestrictedFixture(t)
	var order []string
	var seenWS, seenServer uuid.UUID
	f.svc.AddRevokeHook(func(_ context.Context, ws, id uuid.UUID) { order = append(order, "first"); seenWS, seenServer = ws, id })
	f.svc.AddRevokeHook(func(context.Context, uuid.UUID, uuid.UUID) { order = append(order, "second") })

	_, err := f.svc.Revoke(f.admin, f.server.ID)
	require.NoError(t, err)
	require.Equal(t, []string{"first", "second"}, order)
	require.Equal(t, f.server.ID, seenServer)
	require.Equal(t, f.server.WorkspaceID, seenWS)
}

// ---- the server view ----

func TestServerViewCarriesWhatTheAgentAndTheProxyReported(t *testing.T) {
	f, _ := newUnrestrictedFixture(t)
	bg := context.Background()
	facts, err := json.Marshal(map[string]any{"os": "linux", "composeVersion": "2.29.1", "capabilities": []string{agentprotocol.CapProxyV2, agentprotocol.CapDecommission}})
	require.NoError(t, err)
	tailnet, err := json.Marshal(map[string]any{"mode": "managed", "state": "online", "fqdn": "edge.tail1234.ts.net", "ips": []string{"100.64.0.7"}})
	require.NoError(t, err)
	_, err = f.pool.Exec(bg, `UPDATE servers SET status = 'online', facts = $2, proxy_status = 'error', proxy_error = 'port 80 busy',
		proxy_generation = 5, proxy_applied_generation = 3, tailnet_hostname = 'edge', tailnet_status = $3 WHERE id = $1`, f.server.ID, facts, tailnet)
	require.NoError(t, err)
	_, err = f.pool.Exec(bg, `INSERT INTO metric_samples (server_id, ts, cpu_percent, mem_bytes, mem_limit_bytes, disk_used_bytes, disk_total_bytes)
		VALUES ($1, now() - interval '1 minute', 12.5, 400, 1000, 70, 100)`, f.server.ID)
	require.NoError(t, err)
	_, err = f.pool.Exec(bg, `INSERT INTO server_tunnels (server_id, workspace_id, account_id, tunnel_id, tunnel_name, state, connections, activated_at)
		VALUES ($1, $2, 'acct', 'tun-1', 'nebulactrl-edge', 'online', 4, now())`, f.server.ID, f.server.WorkspaceID)
	require.NoError(t, err)

	sv, err := f.svc.Get(f.admin, f.server.ID)
	require.NoError(t, err)
	require.NotNil(t, sv.Facts)
	require.Equal(t, "2.29.1", sv.ComposeVersion)
	require.True(t, sv.CanDecommission)
	require.True(t, sv.Proxy.V2)
	require.Equal(t, "error", sv.Proxy.Status)
	require.Equal(t, "port 80 busy", sv.Proxy.Error)
	require.EqualValues(t, 5, sv.Proxy.Generation)
	require.EqualValues(t, 3, sv.Proxy.AppliedGeneration, "behind the generation means staged proxy changes")
	require.Equal(t, "edge", sv.Proxy.Tailnet.Hostname)
	require.Equal(t, "online", sv.Proxy.Tailnet.State)
	require.Equal(t, "edge.tail1234.ts.net", sv.Proxy.Tailnet.FQDN)
	require.Equal(t, []string{"100.64.0.7"}, sv.Proxy.Tailnet.IPs)
	require.NotNil(t, sv.Latest)
	require.InDelta(t, 12.5, sv.Latest.CPUPercent, 0.001)
	require.EqualValues(t, 400, sv.Latest.MemUsed)
	require.EqualValues(t, 1000, sv.Latest.MemTotal)
	require.True(t, sv.Tunnel.Enabled)
	require.Equal(t, "online", sv.Tunnel.State)
	require.True(t, sv.Tunnel.Active)
	require.Equal(t, 4, sv.Tunnel.Connections)
	require.Equal(t, "tun-1.cfargotunnel.com", sv.Tunnel.CNAME)

	// The list shows the same summary, and a server without a tunnel says off.
	other, err := f.q.CreateServer(bg, db.CreateServerParams{WorkspaceID: f.server.WorkspaceID, Name: "second"})
	require.NoError(t, err)
	items, _, err := f.svc.List(f.admin, "", 50)
	require.NoError(t, err)
	require.Len(t, items, 2)
	byID := map[uuid.UUID]servers.Server{}
	for _, it := range items {
		byID[it.ID] = it
	}
	require.True(t, byID[f.server.ID].Tunnel.Enabled)
	require.Equal(t, "online", byID[f.server.ID].Tunnel.State)
	require.False(t, byID[other.ID].Tunnel.Enabled)
	require.Equal(t, "off", byID[other.ID].Tunnel.State)
	require.Nil(t, byID[other.ID].Latest)
	require.Equal(t, []string{}, byID[other.ID].Proxy.Tailnet.IPs, "no tailnet yet is an empty list, not null")

	// A tunnel being removed is not active.
	_, err = f.pool.Exec(bg, `UPDATE server_tunnels SET state = 'removing' WHERE server_id = $1`, f.server.ID)
	require.NoError(t, err)
	sv, err = f.svc.Get(f.admin, f.server.ID)
	require.NoError(t, err)
	require.False(t, sv.Tunnel.Active)
	require.Equal(t, "removing", sv.Tunnel.State)
}

func TestPublicBaseAndInstallCommandFollowTheControlPlaneAddress(t *testing.T) {
	https := &config.Config{PublicURL: "https://nebula.example.com/"}
	require.Equal(t, "https://nebula.example.com", servers.PublicBase(https))
	cmd := servers.InstallCommand(https, "tok_1")
	require.Equal(t, "curl -fsSL https://nebula.example.com/install-agent.sh | sudo sh -s -- --url https://nebula.example.com --token tok_1", cmd)

	dev := &config.Config{Listen: "127.0.0.1:9090"}
	require.Equal(t, "http://localhost:9090", servers.PublicBase(dev), "without a public URL the listen port is used")
	require.Contains(t, servers.InstallCommand(dev, "t"), "--insecure-dev", "plain http needs the dev flag")
	require.Equal(t, "http://localhost:8080", servers.PublicBase(&config.Config{Listen: "garbage"}), "an unparsable listen address falls back to 8080")
}
