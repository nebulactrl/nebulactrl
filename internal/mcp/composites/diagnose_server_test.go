package composites_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/mcp/mcpkit"
	"github.com/nebulactrl/nebulactrl/internal/policy"
)

// diagServerSample inserts a host-level resource sample minutesAgo old.
func (f *fx) diagServerSample(minutesAgo int, cpu float64, memUsed, memTotal, diskUsed, diskTotal int64) {
	f.t.Helper()
	_, err := f.pool.Exec(context.Background(),
		`INSERT INTO metric_samples (server_id, service_id, ts, cpu_percent, mem_bytes, mem_limit_bytes, disk_used_bytes, disk_total_bytes)
		 VALUES ($1, NULL, now() - make_interval(mins => $2), $3, $4, $5, $6, $7)`,
		f.server.ID, minutesAgo, cpu, memUsed, memTotal, diskUsed, diskTotal)
	require.NoError(f.t, err)
}

// diagServerCommand inserts an unfinished agent command of a server.
func (f *fx) diagServerCommand(serverID any, kind, status string, minutesAgo int) {
	f.t.Helper()
	_, err := f.pool.Exec(context.Background(),
		`INSERT INTO agent_commands (server_id, kind, payload, idempotency_key, status, expires_at, created_at, acked_at)
		 VALUES ($1, $2, '{}', gen_random_uuid()::text, $3, now() + interval '1 hour', now() - make_interval(mins => $4), CASE WHEN $3 = 'acked' THEN now() - make_interval(mins => $4) END)`,
		serverID, kind, status, minutesAgo)
	require.NoError(f.t, err)
}

func TestDiagnoseServerHappyPath(t *testing.T) {
	f := newFx(t)
	f.service(svcOpts{env: f.staging, slug: "web"})
	bad := f.service(svcOpts{env: f.prod, slug: "api"})
	f.setObserved(bad.ID, "exited", "unknown")

	// Disk climbs 50% -> 80% in 50 minutes; memory is fine.
	const gb = int64(1 << 30)
	f.diagServerSample(50, 20, 2*gb, 8*gb, 50*gb, 100*gb)
	f.diagServerSample(25, 35, 2*gb, 8*gb, 65*gb, 100*gb)
	f.diagServerSample(1, 30, 2*gb, 8*gb, 80*gb, 100*gb)
	f.diagServerCommand(f.server.ID, "deploy", "acked", 10)
	f.diagServerCommand(f.server.ID, "restart", "pending", 5)

	m := f.must("diagnose_server", map[string]any{"server": "AGENT1"})

	require.Equal(t, "degraded", m["verdict"], "online but with warnings")
	require.Empty(t, sectionErrors(m))
	srv := m["server"].(map[string]any)
	require.Equal(t, f.server.ID.String(), srv["id"])
	require.Equal(t, "online", srv["status"])
	require.Equal(t, "linux", srv["os"])
	require.Equal(t, true, srv["proxyV2"])
	require.EqualValues(t, 1, srv["controlPlaneProtocol"])

	usage := m["usage"].(map[string]any)
	require.EqualValues(t, 80, usage["diskUsedPercent"])
	require.EqualValues(t, 25, usage["memUsedPercent"])

	metrics := m["metrics"].(map[string]any)
	require.EqualValues(t, 3, metrics["samples"])
	require.EqualValues(t, 50, metrics["diskStartPercent"])
	require.EqualValues(t, 80, metrics["diskNowPercent"])
	require.Greater(t, metrics["diskPointsPerHour"], 0.0)

	placed := m["services"].(map[string]any)
	require.EqualValues(t, 2, placed["total"])
	items := placed["items"].([]any)
	require.Len(t, items, 2)
	require.Equal(t, "shop/production/api", items[0].(map[string]any)["selector"], "problem services come first")
	require.Equal(t, "exited", items[0].(map[string]any)["state"])
	require.Equal(t, true, items[0].(map[string]any)["production"])

	cmds := m["commands"].([]any)
	require.Len(t, cmds, 2)
	codes := diagFindingCodes(m)
	require.Contains(t, codes, "acked_without_result")
	require.Contains(t, codes, "commands_queued")
	require.Contains(t, codes, "services_unhealthy")
	require.Contains(t, codes, "disk_filling")
	require.Equal(t, "critical", m["findings"].([]any)[0].(map[string]any)["severity"])

	require.Equal(t, "none", m["drain"].(map[string]any)["state"])
	require.Contains(t, diagNextTools(m), "diagnose_service")
	require.Contains(t, m["summary"], "agent1")
}

func TestDiagnoseServerOffline(t *testing.T) {
	f := newFx(t)
	off := f.offlineServer("lost")
	f.diagServerCommand(off.ID, "deploy", "acked", 30)

	m := f.must("diagnose_server", map[string]any{"server": off.ID.String(), "detail": "full"})
	require.Equal(t, "down", m["verdict"])
	require.Contains(t, diagFindingCodes(m), "server_offline")
	require.Contains(t, diagFindingCodes(m), "acked_without_result")
	srv := m["server"].(map[string]any)
	require.Equal(t, "offline", srv["status"])
	require.Greater(t, srv["offlineSeconds"], float64(3600), "silent for two hours")
	require.Contains(t, srv["lastSeen"], "h ago")
	require.Empty(t, m["metrics"], "no samples, no metrics block")
}

func TestDiagnoseServerPartialFailure(t *testing.T) {
	f := newFx(t)
	f.service(svcOpts{env: f.staging, slug: "web"})

	// Only servers:read: the placed services (projects:read) degrade to an error entry.
	m, err := f.call(f.principal(policy.ScopeServersRead), "diagnose_server", map[string]any{"server": f.server.ID.String()})
	require.NoError(t, err)
	require.Equal(t, []string{"services"}, sectionErrors(m))
	require.Equal(t, "forbidden", m["errors"].([]any)[0].(map[string]any)["code"])
	require.Equal(t, "healthy", m["verdict"])
	require.Equal(t, "online", m["server"].(map[string]any)["status"])
	require.Empty(t, m["services"].(map[string]any)["items"])
}

func TestDiagnoseServerArguments(t *testing.T) {
	f := newFx(t)
	f.offlineServer("lost")

	_, err := f.call(f.admin, "diagnose_server", map[string]any{"server": "agent1", "detail": "x"})
	require.Equal(t, "invalid_argument", errCode(err))
	_, err = f.call(f.admin, "diagnose_server", map[string]any{})
	require.Equal(t, "invalid_argument", errCode(err))
	_, err = f.call(f.admin, "diagnose_server", map[string]any{"server": "agent"})
	require.Equal(t, "not_found", errCode(err))
	var te *mcpkit.Error
	require.ErrorAs(t, err, &te)
	require.NotEmpty(t, te.Data.(map[string]any)["choices"], "a near miss lists candidates")
	_, err = f.call(f.admin, "diagnose_server", map[string]any{"server": "00000000-0000-0000-0000-000000000001"})
	ae, ok := apierr.As(err)
	require.True(t, ok, "an unknown id is the service's not_found, mapped by the registry")
	require.Equal(t, 404, ae.Status)

	// Two servers sharing a name are ambiguous: the choices carry both.
	_, err = f.q.CreateServer(context.Background(), db.CreateServerParams{WorkspaceID: f.ws.ID, Name: "agent1"})
	require.NoError(t, err)
	_, err = f.call(f.admin, "diagnose_server", map[string]any{"server": "agent1"})
	require.Equal(t, "ambiguous_selector", errCode(err))
	require.ErrorAs(t, err, &te)
	require.Len(t, te.Data.(map[string]any)["choices"], 2)
}
