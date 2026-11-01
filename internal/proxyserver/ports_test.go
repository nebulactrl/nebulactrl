package proxyserver

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// fakeAdmins makes one user the instance administrator.
type fakeAdmins struct{ admin uuid.UUID }

func (a fakeAdmins) IsInstanceAdmin(_ context.Context, id uuid.UUID) (bool, error) {
	return id == a.admin, nil
}

// portsFixture is a fixture whose agent has proxy.ports and whose fixture user
// is the instance administrator.
func portsFixture(t *testing.T) *fixture {
	t.Helper()
	f := newFixture(t)
	p, ok := auth.PrincipalFrom(f.admin)
	require.True(t, ok)
	f.svc.admins = fakeAdmins{admin: p.UserID}
	f.setFacts(agentprotocol.Facts{Capabilities: []string{agentprotocol.CapProxyV2, agentprotocol.CapProxyPorts}})
	return f
}

func (f *fixture) setFacts(facts agentprotocol.Facts) {
	f.t.Helper()
	raw, err := json.Marshal(facts)
	require.NoError(f.t, err)
	_, err = f.pool.Exec(context.Background(), `UPDATE servers SET facts=$2 WHERE id=$1`, f.srv.ID, raw)
	require.NoError(f.t, err)
}

// report stores the ports the agent reported in a heartbeat.
func (f *fixture) report(p agentprotocol.ProxyPorts) {
	f.t.Helper()
	p.Normalize()
	raw, err := json.Marshal(p)
	require.NoError(f.t, err)
	n, err := f.q.RecordServerProxyPorts(context.Background(), dbRecordPorts(f.srv.ID, raw))
	require.NoError(f.t, err)
	require.EqualValues(f.t, 1, n)
}

func TestValidatePortsMatrix(t *testing.T) {
	reserved := []l4Reservation{{Protocol: "tcp", Port: 5432, Service: "pg"}, {Protocol: "udp", Port: 8443, Service: "dns"}, {Protocol: "tcp", Port: 8081, Service: "web2"}}
	for _, c := range []struct {
		name        string
		http, https int
		want        string
	}{
		{"defaults", 80, 443, ""},
		{"alternate", 8080, 8443 + 1, ""},
		{"zero", 0, 443, "invalid_proxy_ports"},
		{"too big", 80, 65536, "invalid_proxy_ports"},
		{"equal", 8080, 8080, "invalid_proxy_ports"},
		{"admin port http", agentprotocol.ProxyAdminPort, 443, "invalid_proxy_ports"},
		{"admin port https", 80, agentprotocol.ProxyAdminPort, "invalid_proxy_ports"},
		{"tcp route on http", 8081, 443, "proxy_port_clash"},
		{"tcp route on https", 80, 5432, "proxy_port_clash"},
		{"udp route on https", 80, 8443, "proxy_port_clash"},
		{"udp route on http is fine", 8443, 443, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			err := ValidatePorts(c.http, c.https, reserved)
			if c.want == "" {
				require.NoError(t, err)
				return
			}
			require.Equal(t, c.want, code(t, err))
		})
	}
}

func TestUpdatePortsIsInstanceAdminOnlyAndStaged(t *testing.T) {
	f := portsFixture(t)
	bg := context.Background()

	// A token with proxy:write (no admin scope) and a team admin who is not the instance admin are refused.
	_, err := f.svc.UpdateSettings(f.token, f.srv.ID, SettingsInput{HTTPPort: ptr(8080), HTTPSPort: ptr(8443)})
	require.Equal(t, "forbidden", code(t, err))
	teamAdmin := principal(f.ws, uuid.New(), []string{"admin", "proxy:write", "servers:read", "servers:write"}, false)
	_, err = f.svc.UpdateSettings(teamAdmin, f.srv.ID, SettingsInput{HTTPPort: ptr(8080), HTTPSPort: ptr(8443)})
	require.Equal(t, "instance_admin_required", code(t, err))
	require.Nil(t, f.reload().ProxyHttpPort, "nothing was staged")

	// The instance admin stages them; the generation moves and the view says so.
	gen := f.reload().ProxyGeneration
	v, err := f.svc.UpdateSettings(f.admin, f.srv.ID, SettingsInput{HTTPPort: ptr(8080), HTTPSPort: ptr(8443)})
	require.NoError(t, err)
	require.Greater(t, f.reload().ProxyGeneration, gen)
	require.True(t, v.Ports.Staged)
	require.Equal(t, 8080, v.Ports.HTTP)
	require.Equal(t, 8443, v.Ports.HTTPS)
	require.Equal(t, 80, v.Ports.AppliedHTTP, "nothing applied yet: the default ports")
	require.Equal(t, 443, v.Ports.AppliedHTTPS)
	require.True(t, v.Ports.Supported)
	require.Equal(t, 8080, *v.Ports.SetHTTP)
	require.Equal(t, 8443, *v.Ports.SetHTTPS)

	// The audit event names the changed keys and their values, nothing else.
	var meta string
	require.NoError(t, f.pool.QueryRow(bg, `SELECT metadata::text FROM audit_events WHERE action='server.proxy.update' AND resource_id=$1 ORDER BY created_at DESC LIMIT 1`, f.srv.ID.String()).Scan(&meta))
	var m map[string]any
	require.NoError(t, json.Unmarshal([]byte(meta), &m))
	require.EqualValues(t, 8080, m["httpPort"])
	require.EqualValues(t, 8443, m["httpsPort"])

	// Unchanged input is a no-op; 0 resets to the agent's own ports.
	gen = f.reload().ProxyGeneration
	_, err = f.svc.UpdateSettings(f.admin, f.srv.ID, SettingsInput{HTTPPort: ptr(8080)})
	require.NoError(t, err)
	require.Equal(t, gen, f.reload().ProxyGeneration)
	v, err = f.svc.UpdateSettings(f.admin, f.srv.ID, SettingsInput{HTTPPort: ptr(0), HTTPSPort: ptr(0)})
	require.NoError(t, err)
	require.Nil(t, v.Ports.SetHTTP)
	require.Nil(t, v.Ports.SetHTTPS)
	require.False(t, v.Ports.Staged, "reset to what is applied (nothing)")
	require.NoError(t, f.pool.QueryRow(bg, `SELECT metadata::text FROM audit_events WHERE action='server.proxy.update' AND resource_id=$1 ORDER BY created_at DESC LIMIT 1`, f.srv.ID.String()).Scan(&meta))
	require.Contains(t, meta, `"httpPort": null`)
}

func TestUpdatePortsValidation(t *testing.T) {
	f := portsFixture(t)
	_, err := f.svc.UpdateSettings(f.admin, f.srv.ID, SettingsInput{HTTPPort: ptr(70000)})
	require.Equal(t, "invalid_proxy_ports", code(t, err))
	_, err = f.svc.UpdateSettings(f.admin, f.srv.ID, SettingsInput{HTTPPort: ptr(-1)})
	require.Equal(t, "invalid_proxy_ports", code(t, err))
	_, err = f.svc.UpdateSettings(f.admin, f.srv.ID, SettingsInput{HTTPPort: ptr(8080), HTTPSPort: ptr(8080)})
	require.Equal(t, "invalid_proxy_ports", code(t, err))
	_, err = f.svc.UpdateSettings(f.admin, f.srv.ID, SettingsInput{HTTPPort: ptr(agentprotocol.ProxyAdminPort)})
	require.Equal(t, "invalid_proxy_ports", code(t, err))
	// Only one port set: the pair is checked with the agent's own other port.
	f.report(agentprotocol.ProxyPorts{HTTP: 8080, HTTPS: 8443, Source: agentprotocol.ProxyPortsFlag, FlagHTTP: 8080, FlagHTTPS: 8443})
	_, err = f.svc.UpdateSettings(f.admin, f.srv.ID, SettingsInput{HTTPPort: ptr(8443)})
	require.Equal(t, "invalid_proxy_ports", code(t, err), "equals the agent's own HTTPS port")
	require.Nil(t, f.reload().ProxyHttpPort)

	// An agent without proxy.ports would drop the fields.
	f.setFacts(agentprotocol.Facts{Capabilities: []string{agentprotocol.CapProxyV2}})
	_, err = f.svc.UpdateSettings(f.admin, f.srv.ID, SettingsInput{HTTPPort: ptr(8080), HTTPSPort: ptr(8443)})
	require.Equal(t, "agent_too_old", code(t, err))
	// Other settings still work on it.
	_, err = f.svc.UpdateSettings(f.admin, f.srv.ID, SettingsInput{AllowHostGateway: ptr(false)})
	require.NoError(t, err)
}

// finish marks a command done (what the agent hub does with its result), so the
// next deploy is not refused as in progress.
func (f *fixture) finish(cmd db.AgentCommand, status string) {
	f.t.Helper()
	_, err := f.pool.Exec(context.Background(), `UPDATE agent_commands SET status=$2, finished_at=now() WHERE id=$1`, cmd.ID, status)
	require.NoError(f.t, err)
}

func dbRecordPorts(id uuid.UUID, raw []byte) db.RecordServerProxyPortsParams {
	return db.RecordServerProxyPortsParams{ID: id, Ports: raw}
}

func TestPlanPortsDiffReasonAndWarnings(t *testing.T) {
	f := portsFixture(t)
	f.report(agentprotocol.ProxyPorts{HTTP: 8080, HTTPS: 8443, Source: agentprotocol.ProxyPortsFlag, FlagHTTP: 8080, FlagHTTPS: 8443, BusyOther: []int{443, 80}})
	_, err := f.pool.Exec(context.Background(), `UPDATE servers SET acme_email='ops@example.com' WHERE id=$1`, f.srv.ID)
	require.NoError(t, err)

	p, err := f.svc.PlanDeploy(f.admin, f.srv.ID, false, nil)
	require.NoError(t, err)
	require.Empty(t, p.Diff, "nothing staged")

	// Moving onto the ports another process (Coolify's proxy) holds.
	_, err = f.svc.UpdateSettings(f.admin, f.srv.ID, SettingsInput{HTTPPort: ptr(80), HTTPSPort: ptr(443)})
	require.NoError(t, err)
	p, err = f.svc.PlanDeploy(f.admin, f.srv.ID, false, nil)
	require.NoError(t, err)
	require.True(t, p.RestartRequired)
	require.Equal(t, []ProxyPlanChange{
		{Field: "proxy.httpPort", From: "8080", To: "80"},
		{Field: "proxy.httpsPort", From: "8443", To: "443"},
	}, p.Diff)
	require.Contains(t, strings.Join(p.Reasons, " "), "Changing a published port recreates the proxy container (about 1 second of dropped connections).")
	joined := strings.Join(p.Warnings, "\n")
	require.Contains(t, joined, "Port 80 is in use by another process")
	require.Contains(t, joined, "Port 443 is in use by another process")
	require.NotContains(t, joined, "dashboard", "not the control-plane server")

	// The dashboard host's server.
	_, err = f.pool.Exec(context.Background(), `UPDATE servers SET is_control_plane=true WHERE id=$1`, f.srv.ID)
	require.NoError(t, err)
	p, err = f.svc.PlanDeploy(f.admin, f.srv.ID, false, nil)
	require.NoError(t, err)
	require.Contains(t, strings.Join(p.Warnings, "\n"), "NEBULACTRL_PUBLIC_URL and the agent's own URL are not changed")

	// A move that frees nothing busy raises no busy-port warning.
	f.report(agentprotocol.ProxyPorts{HTTP: 8080, HTTPS: 8443, Source: agentprotocol.ProxyPortsFlag, FlagHTTP: 8080, FlagHTTPS: 8443})
	p, err = f.svc.PlanDeploy(f.admin, f.srv.ID, false, nil)
	require.NoError(t, err)
	require.NotContains(t, strings.Join(p.Warnings, "\n"), "in use by another process")
}

func TestPlanPreviewDoesNotStage(t *testing.T) {
	f := portsFixture(t)
	p, err := f.svc.PlanDeploy(f.token, f.srv.ID, false, &PortsInput{HTTP: ptr(8080), HTTPS: ptr(8443)})
	require.NoError(t, err)
	require.True(t, p.RestartRequired)
	require.Len(t, p.Diff, 2)
	require.Nil(t, f.reload().ProxyHttpPort, "a preview stores nothing")
	require.EqualValues(t, 0, f.reload().ProxyGeneration)

	_, err = f.svc.PlanDeploy(f.token, f.srv.ID, false, &PortsInput{HTTP: ptr(8080), HTTPS: ptr(8080)})
	require.Equal(t, "invalid_proxy_ports", code(t, err))
}

func TestDeployCarriesPortsAndResultAppliesThem(t *testing.T) {
	f := portsFixture(t)
	bg := context.Background()
	_, err := f.svc.UpdateSettings(f.admin, f.srv.ID, SettingsInput{HTTPPort: ptr(8080), HTTPSPort: ptr(8443)})
	require.NoError(t, err)

	res, err := f.svc.Deploy(f.token, f.srv.ID, false)
	require.NoError(t, err)
	require.True(t, res.Plan.RestartRequired)
	cmd := f.lastCommand(agentprotocol.KindProxyConfigure)
	var payload agentprotocol.ProxyConfigurePayload
	require.NoError(t, json.Unmarshal(cmd.Payload, &payload))
	require.Equal(t, 8080, payload.Proxy.HTTPPort)
	require.Equal(t, 8443, payload.Proxy.HTTPSPort)

	var meta string
	require.NoError(t, f.pool.QueryRow(bg, `SELECT metadata::text FROM audit_events WHERE action='server.proxy.deploy' AND resource_id=$1`, f.srv.ID.String()).Scan(&meta))
	require.Contains(t, meta, `"httpPort": 8080`)

	// Until the agent confirms, route pushes still carry the applied (default) ports.
	cfg, err := BuildConfig(bg, f.q, f.srv.ID)
	require.NoError(t, err)
	require.Zero(t, cfg.HTTPPort)
	require.Zero(t, cfg.HTTPSPort)

	// A failed deploy applies nothing.
	require.NoError(t, f.svc.HandleResult(bg, f.srv.ID, cmd.ID, agentprotocol.ResultFrame{
		CommandID: cmd.ID.String(), Outcome: agentprotocol.OutcomeFailed, Error: "port 8080 is already in use",
		Data: json.RawMessage(`{"outcome":"failed","message":"port 8080 is already in use"}`),
	}))
	f.finish(cmd, "failed")
	require.Nil(t, f.reload().ProxyAppliedHttpPort)

	// A second deploy succeeds and flips the applied ports.
	res, err = f.svc.Deploy(f.token, f.srv.ID, false)
	require.NoError(t, err)
	cmd = f.lastCommand(agentprotocol.KindProxyConfigure)
	require.Equal(t, res.CommandID, cmd.ID)
	f.finish(cmd, "succeeded")
	require.NoError(t, f.svc.HandleResult(bg, f.srv.ID, cmd.ID, result(t, agentprotocol.OutcomeSucceeded, agentprotocol.ProxyConfigureResult{Restarted: true}, cmd)))
	srv := f.reload()
	require.EqualValues(t, 8080, *srv.ProxyAppliedHttpPort)
	require.EqualValues(t, 8443, *srv.ProxyAppliedHttpsPort)
	cfg, err = BuildConfig(bg, f.q, f.srv.ID)
	require.NoError(t, err)
	require.Equal(t, 8080, cfg.HTTPPort)
	require.Equal(t, 8443, cfg.HTTPSPort)

	v, err := f.svc.GetProxy(f.reader, f.srv.ID)
	require.NoError(t, err)
	require.False(t, v.Ports.Staged)

	// Reset: the next deploy carries 0/0 and the applied columns clear.
	_, err = f.svc.UpdateSettings(f.admin, f.srv.ID, SettingsInput{HTTPPort: ptr(0), HTTPSPort: ptr(0)})
	require.NoError(t, err)
	_, err = f.svc.Deploy(f.token, f.srv.ID, false)
	require.NoError(t, err)
	cmd = f.lastCommand(agentprotocol.KindProxyConfigure)
	var reset agentprotocol.ProxyConfigurePayload
	require.NoError(t, json.Unmarshal(cmd.Payload, &reset))
	require.Zero(t, reset.Proxy.HTTPPort)
	require.Zero(t, reset.Proxy.HTTPSPort)
	require.NoError(t, f.svc.HandleResult(bg, f.srv.ID, cmd.ID, result(t, agentprotocol.OutcomeSucceeded, agentprotocol.ProxyConfigureResult{Restarted: true}, cmd)))
	require.Nil(t, f.reload().ProxyAppliedHttpPort)
}

func TestHeartbeatPortsViewAndSource(t *testing.T) {
	f := portsFixture(t)
	v, err := f.svc.GetProxy(f.reader, f.srv.ID)
	require.NoError(t, err)
	require.False(t, v.Ports.Reported)
	require.Equal(t, "", v.Ports.Source)

	f.report(agentprotocol.ProxyPorts{HTTP: 8080, HTTPS: 8443, Source: agentprotocol.ProxyPortsServer, FlagHTTP: 80, FlagHTTPS: 443, BusyOther: []int{80}})
	v, err = f.svc.GetProxy(f.reader, f.srv.ID)
	require.NoError(t, err)
	require.True(t, v.Ports.Reported)
	require.Equal(t, "server", v.Ports.Source)
	require.Equal(t, 8080, v.Ports.AppliedHTTP)
	require.Equal(t, 443, v.Ports.AgentHTTPS)
	require.Equal(t, []int{80}, v.Ports.BusyOther)
	require.Equal(t, 80, v.Ports.HTTP, "nothing staged: the desired ports are the agent's own")

	// The same report again writes nothing.
	raw, _ := json.Marshal(agentprotocol.ProxyPorts{HTTP: 8080, HTTPS: 8443, Source: agentprotocol.ProxyPortsServer, FlagHTTP: 80, FlagHTTPS: 443, BusyOther: []int{80}})
	n, err := f.q.RecordServerProxyPorts(context.Background(), dbRecordPorts(f.srv.ID, raw))
	require.NoError(t, err)
	require.EqualValues(t, 0, n)
}
