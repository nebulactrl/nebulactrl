package engine

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agent/autoban"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

type fakeBans struct {
	service, ban string
	err          error
}

func (f *fakeBans) Unban(service, ban string) error {
	f.service, f.ban = service, ban
	return f.err
}

func unbanCmd(t *testing.T, service, ban string) agentprotocol.Command {
	t.Helper()
	p, err := json.Marshal(agentprotocol.AutoBanUnbanPayload{ServiceID: service, BanID: ban})
	require.NoError(t, err)
	return agentprotocol.Command{ID: "c1", Kind: agentprotocol.KindAutoBanUnban, Payload: p}
}

func unbanMessage(t *testing.T, res agentprotocol.ResultFrame) string {
	t.Helper()
	var dr agentprotocol.DeployResult
	require.NoError(t, json.Unmarshal(res.Data, &dr))
	return dr.Message
}

func TestAutoBanUnbanCommand(t *testing.T) {
	fb := &fakeBans{}
	e := &Engine{cfg: Config{AutoBan: fb}}

	res := e.HandleCommand(t.Context(), unbanCmd(t, "svc", "ban1"))
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome)
	require.Equal(t, AutoBanUnbanned, unbanMessage(t, res))
	require.Equal(t, "svc", fb.service)
	require.Equal(t, "ban1", fb.ban)

	fb.err = autoban.ErrNotFound
	res = e.HandleCommand(t.Context(), unbanCmd(t, "svc", "gone"))
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, "a ban that is not active is not a failure")
	require.Equal(t, AutoBanNotFound, unbanMessage(t, res))

	fb.err = errors.New("disk full")
	res = e.HandleCommand(t.Context(), unbanCmd(t, "svc", "ban2"))
	require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome)
	require.Contains(t, res.Error, "disk full")
}

func TestAutoBanUnbanCommandRejectsBadInput(t *testing.T) {
	e := &Engine{cfg: Config{AutoBan: &fakeBans{}}}
	res := e.HandleCommand(t.Context(), agentprotocol.Command{ID: "c", Kind: agentprotocol.KindAutoBanUnban, Payload: []byte("{")})
	require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome)
	res = e.HandleCommand(t.Context(), unbanCmd(t, "", "ban"))
	require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome)

	e = &Engine{}
	res = e.HandleCommand(t.Context(), unbanCmd(t, "svc", "ban"))
	require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome, "an agent without the engine refuses")
}
