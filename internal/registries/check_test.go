package registries

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// answer waits for the enqueued registry_check, then delivers the agent's result.
func (f *fixture) answer(res agentprotocol.RegistryCheckResult) {
	f.t.Helper()
	go func() {
		for range 100 {
			// The command is visible once Check's transaction committed.
			if _, id, ok := f.disp.last(); ok && f.commandVisible(id) {
				outcome := agentprotocol.OutcomeSucceeded
				if res.Error != "" {
					outcome = agentprotocol.OutcomeFailed
				}
				data, _ := json.Marshal(res)
				if err := f.svc.HandleResult(context.Background(), id, agentprotocol.ResultFrame{CommandID: id.String(), Outcome: outcome, Data: data, Error: res.Error}); err != nil {
					f.t.Errorf("handle result: %v", err)
				}
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
	}()
}

func TestCheckDispatchesRegistryCheckAndStoresTheResult(t *testing.T) {
	f := newFixture(t)
	v := f.createExternal("GHCR")
	f.svc.checkWait = 10 * time.Second

	f.answer(agentprotocol.RegistryCheckResult{Push: true, Pull: true})
	res, err := f.svc.Check(f.ctx, v.ID, f.srv.ID)
	require.NoError(t, err)
	require.True(t, res.Ok)
	require.True(t, res.Push)
	require.True(t, res.Pull)
	require.False(t, res.Pending)
	require.Equal(t, f.srv.ID, *res.ServerID)

	// The command: kind registry_check, no credential anywhere in the payload.
	cmd, id, ok := f.disp.last()
	require.True(t, ok)
	require.Equal(t, agentprotocol.KindRegistryCheck, cmd.Kind)
	require.Equal(t, f.srv.ID, cmd.ServerID)
	var p agentprotocol.RegistryCheckPayload
	require.NoError(t, json.Unmarshal(cmd.Payload, &p))
	require.Equal(t, "ghcr.io", p.Host)
	require.False(t, p.Insecure)
	require.Equal(t, "acme/nebulactrl-check", p.Repository)
	require.NotContains(t, string(cmd.Payload), "ghp_pushsecret123")
	require.NotContains(t, string(cmd.Payload), "password")
	var stored string
	require.NoError(t, f.pool.QueryRow(context.Background(), `SELECT payload::text FROM agent_commands WHERE id = $1`, id).Scan(&stored))
	require.NotContains(t, stored, "ghp_pushsecret123")

	// Send-time secrets: the push credential, and the pull one only when it is separate.
	secrets, err := f.svc.SecretsForCommand(context.Background(), cmd.Kind, cmd.Payload)
	require.NoError(t, err)
	require.Len(t, secrets, 1)
	a, ok := DecodeAuth(secrets[agentprotocol.SecretRegistryPushAuth])
	require.True(t, ok)
	require.Equal(t, agentprotocol.RegistryAuth{Username: "deploy-bot", Password: "ghp_pushsecret123", ServerAddress: "ghcr.io"}, a)
	_, err = f.svc.Update(f.ctx, v.ID, UpdateInput{PullUsername: new("reader"), PullPassword: new("pullpw")})
	require.NoError(t, err)
	secrets, err = f.svc.SecretsForCommand(context.Background(), cmd.Kind, cmd.Payload)
	require.NoError(t, err)
	a, _ = DecodeAuth(secrets[agentprotocol.SecretRegistryAuth])
	require.Equal(t, "pullpw", a.Password)
	other, err := f.svc.SecretsForCommand(context.Background(), agentprotocol.KindBuildImage, cmd.Payload)
	require.NoError(t, err)
	require.Nil(t, other, "other command kinds carry no registry secrets from here")

	// lastCheck is stored per server and shown by the view.
	got, err := f.svc.Get(f.ctx, v.ID)
	require.NoError(t, err)
	lc := got.LastCheck[f.srv.ID.String()]
	require.True(t, lc.Ok)
	require.WithinDuration(t, time.Now(), lc.At, time.Minute)

	// Audited.
	var n int
	require.NoError(t, f.pool.QueryRow(context.Background(), `SELECT count(*) FROM audit_events WHERE action = $1 AND resource_id = $2`, AuditCheck, v.ID.String()).Scan(&n))
	require.Equal(t, 1, n)
}

func TestCheckReportsFailuresWithoutLeakingCredentials(t *testing.T) {
	f := newFixture(t)
	v := f.createExternal("GHCR")
	f.svc.checkWait = 10 * time.Second

	f.answer(agentprotocol.RegistryCheckResult{Push: false, Pull: true, Error: "push to acme/x denied for ghp_pushsecret123"})
	res, err := f.svc.Check(f.ctx, v.ID, f.srv.ID)
	require.NoError(t, err)
	require.False(t, res.Ok)
	require.False(t, res.Push)
	require.True(t, res.Pull)
	require.Contains(t, res.Error, "denied")
	require.NotContains(t, res.Error, "ghp_pushsecret123", "the agent's message is scrubbed before it is stored or shown")
	got, err := f.svc.Get(f.ctx, v.ID)
	require.NoError(t, err)
	require.NotContains(t, got.LastCheck[f.srv.ID.String()].Error, "ghp_pushsecret123")
}

func TestCheckResultInterpretation(t *testing.T) {
	f := newFixture(t)
	v := f.createExternal("GHCR")
	f.svc.checkWait = 10 * time.Second
	for name, c := range map[string]struct {
		res  agentprotocol.RegistryCheckResult
		ok   bool
		want string
	}{
		"fine":       {agentprotocol.RegistryCheckResult{Push: true, Pull: true}, true, ""},
		"push only":  {agentprotocol.RegistryCheckResult{Push: true, Pull: false}, false, "pull credential"},
		"pull only":  {agentprotocol.RegistryCheckResult{Push: false, Pull: true}, false, "push credential"},
		"with error": {agentprotocol.RegistryCheckResult{Error: "registry unreachable: dial tcp: i/o timeout"}, false, "unreachable"},
	} {
		f.disp.mu.Lock()
		f.disp.cmds, f.disp.ids = nil, nil
		f.disp.mu.Unlock()
		f.answer(c.res)
		res, err := f.svc.Check(f.ctx, v.ID, f.srv.ID)
		require.NoError(t, err, name)
		require.Equal(t, c.ok, res.Ok, name)
		if c.want != "" {
			require.Contains(t, res.Error, c.want, name)
		}
	}
}

func TestCheckNeedsAnOnlineServerAndReportsPending(t *testing.T) {
	f := newFixture(t)
	v := f.createExternal("GHCR")

	_, err := f.svc.Check(f.ctx, v.ID, uuid.New())
	require.Equal(t, "server_not_found", code(err))

	_, err = f.pool.Exec(context.Background(), `UPDATE servers SET status = 'offline' WHERE id = $1`, f.srv.ID)
	require.NoError(t, err)
	_, err = f.svc.Check(f.ctx, v.ID, f.srv.ID)
	require.Equal(t, "server_offline", code(err))
	_, _, queued := f.disp.last()
	require.False(t, queued, "nothing is dispatched to an offline server")

	_, err = f.pool.Exec(context.Background(), `UPDATE servers SET status = 'online' WHERE id = $1`, f.srv.ID)
	require.NoError(t, err)
	f.svc.checkWait = 300 * time.Millisecond
	res, err := f.svc.Check(f.ctx, v.ID, f.srv.ID)
	require.NoError(t, err)
	require.True(t, res.Pending, "the agent has not answered: poll the registry for lastCheck")
	require.False(t, res.Ok)

	// The command never reached the agent: the result is recorded as a failure.
	_, id, _ := f.disp.last()
	require.NoError(t, f.svc.HandleUndelivered(context.Background(), id, "Not delivered: server offline. No changes were made."))
	got, err := f.svc.Get(f.ctx, v.ID)
	require.NoError(t, err)
	lc := got.LastCheck[f.srv.ID.String()]
	require.False(t, lc.Ok)
	require.Contains(t, lc.Error, "Not delivered")
}

func TestCheckOfTheBuiltinRegistryNeedsItReady(t *testing.T) {
	f := newFixture(t)
	reg, _ := f.builtinRegistry("Built-in registry", "127.0.0.1:18500")
	f.svc.checkWait = 10 * time.Second

	// The probe repository lies under the team's prefix; the command carries the
	// placeholder credential (the control plane adds the real token when the
	// request passes through its forwarder), never a secret.
	f.answer(agentprotocol.RegistryCheckResult{Push: true, Pull: true})
	res, err := f.svc.Check(f.ctx, reg.ID, f.srv.ID)
	require.NoError(t, err)
	require.True(t, res.Ok)
	cmd, _, _ := f.disp.last()
	var p agentprotocol.RegistryCheckPayload
	require.NoError(t, json.Unmarshal(cmd.Payload, &p))
	require.Equal(t, f.ws.ID.String()+"/"+checkRepository, p.Repository)
	require.True(t, p.Insecure, "127.0.0.1 is the loopback registry Docker accepts over plain http")
	secrets, err := f.svc.SecretsForCommand(context.Background(), cmd.Kind, cmd.Payload)
	require.NoError(t, err)
	a, ok := DecodeAuth(secrets[agentprotocol.SecretRegistryPushAuth])
	require.True(t, ok, "a pushing client")
	require.Equal(t, BuiltinUsername, a.Username)
	require.Equal(t, BuiltinPassword, a.Password)

	// A registry that is not ready cannot be tested.
	f.setBuiltinStatus("failed")
	_, err = f.svc.Check(f.ctx, reg.ID, f.srv.ID)
	require.Equal(t, "registry_unavailable", code(err))
}
