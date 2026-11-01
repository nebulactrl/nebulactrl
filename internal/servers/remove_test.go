package servers_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/servers"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// fakeAgent stands in for the hub: which servers are online and what a
// decommission request answers.
type fakeAgent struct {
	mu       sync.Mutex
	online   map[uuid.UUID]bool
	requests []agentRequest
	// answer is the result of a request (default: succeeded with counts).
	answer func(id uuid.UUID) (agentprotocol.ResultFrame, error)
}

type agentRequest struct {
	Server  uuid.UUID
	Kind    string
	Payload agentprotocol.DecommissionPayload
}

func (a *fakeAgent) Online(id uuid.UUID) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.online[id]
}

func (a *fakeAgent) Request(_ context.Context, id uuid.UUID, kind string, payload any, _ map[string]string, _ time.Duration) (agentprotocol.ResultFrame, error) {
	a.mu.Lock()
	p, _ := payload.(agentprotocol.DecommissionPayload)
	a.requests = append(a.requests, agentRequest{Server: id, Kind: kind, Payload: p})
	answer := a.answer
	a.mu.Unlock()
	if answer != nil {
		return answer(id)
	}
	data, _ := json.Marshal(agentprotocol.DecommissionResult{Containers: 3, Networks: 2, Volumes: 1})
	return agentprotocol.ResultFrame{Outcome: agentprotocol.OutcomeSucceeded, Data: data}, nil
}

func (a *fakeAgent) sent() []agentRequest {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]agentRequest(nil), a.requests...)
}

type removeFixture struct {
	*unrestrictedFixture
	agent  *fakeAgent
	queued []servers.RemoveServerArgs
	ws     uuid.UUID
}

func newRemoveFixture(t *testing.T) *removeFixture {
	t.Helper()
	u, _ := newUnrestrictedFixture(t)
	f := &removeFixture{unrestrictedFixture: u, agent: &fakeAgent{online: map[uuid.UUID]bool{}}, ws: u.server.WorkspaceID}
	f.svc.SetAgent(f.agent)
	f.svc.SetRemovalQueue(func(_ context.Context, _ pgx.Tx, args servers.RemoveServerArgs) error {
		f.queued = append(f.queued, args)
		return nil
	})
	return f
}

// goOnline makes the server online with the agent able to decommission.
func (f *removeFixture) goOnline(t *testing.T, id uuid.UUID, capable bool) {
	t.Helper()
	facts := agentprotocol.Facts{Capabilities: []string{agentprotocol.CapDockerCleanup}}
	if capable {
		facts.Capabilities = append(facts.Capabilities, agentprotocol.CapDecommission)
	}
	raw, err := json.Marshal(facts)
	require.NoError(t, err)
	_, err = f.pool.Exec(context.Background(), "UPDATE servers SET status = 'online', facts = $2, last_seen_at = now() WHERE id = $1", id, raw)
	require.NoError(t, err)
	f.agent.mu.Lock()
	f.agent.online[id] = true
	f.agent.mu.Unlock()
}

func (f *removeFixture) exists(t *testing.T, id uuid.UUID) bool {
	t.Helper()
	var n int
	require.NoError(t, f.pool.QueryRow(context.Background(), "SELECT count(*) FROM servers WHERE id = $1", id).Scan(&n))
	return n == 1
}

func requireAPIError(t *testing.T, err error, status int, code string) *apierr.Error {
	t.Helper()
	ae, ok := apierr.As(err)
	require.True(t, ok, "not an API error: %v", err)
	require.Equal(t, status, ae.Status, ae.Error())
	require.Equal(t, code, ae.Code)
	return ae
}

func TestRemoveIsRefusedWhileServicesArePlaced(t *testing.T) {
	f := newRemoveFixture(t)
	f.goOnline(t, f.server.ID, true)
	f.addService(t, "app", true) // a deleted service does not count
	f.addService(t, "app", false)

	for _, mode := range []string{servers.ModeClean, servers.ModeForget} {
		_, err := f.svc.Remove(f.admin, f.server.ID, servers.RemoveInput{Confirm: "edge", Mode: mode})
		ae := requireAPIError(t, err, http.StatusConflict, "server_has_services")
		ps, ok := ae.Details["projects"].([]servers.BlockingProject)
		require.True(t, ok, "details list the projects: %#v", ae.Details)
		require.Len(t, ps, 1)
		require.Equal(t, "p", ps[0].Name)
		require.Equal(t, 1, ps[0].Services)
	}
	require.True(t, f.exists(t, f.server.ID))
	require.Empty(t, f.agent.sent(), "the host was never asked anything")
	require.Empty(t, f.queued)
}

func TestRemoveRefusesTheServerTheControlPlaneRunsOn(t *testing.T) {
	f := newRemoveFixture(t)
	f.goOnline(t, f.server.ID, true)
	_, err := f.pool.Exec(context.Background(), "UPDATE servers SET is_control_plane = true WHERE id = $1", f.server.ID)
	require.NoError(t, err)

	for _, mode := range []string{servers.ModeClean, servers.ModeForget} {
		_, err = f.svc.Remove(f.admin, f.server.ID, servers.RemoveInput{Confirm: "edge", Mode: mode})
		requireAPIError(t, err, http.StatusConflict, "server_is_control_plane")
	}
	require.True(t, f.exists(t, f.server.ID))
}

func TestRemoveChecksTheNameTheModeAndTheScope(t *testing.T) {
	f := newRemoveFixture(t)
	f.goOnline(t, f.server.ID, true)

	_, err := f.svc.Remove(f.admin, f.server.ID, servers.RemoveInput{Confirm: "Edge"})
	requireAPIError(t, err, http.StatusUnprocessableEntity, "confirmation_mismatch")
	_, err = f.svc.Remove(f.admin, f.server.ID, servers.RemoveInput{Confirm: "edge", Mode: "wipe"})
	requireAPIError(t, err, http.StatusUnprocessableEntity, "invalid_mode")

	writer := auth.WithPrincipal(context.Background(), &auth.Principal{
		Type: auth.TypeUser, UserID: uuid.New(), WorkspaceID: f.ws, Scopes: []string{policy.ScopeServersWrite},
	})
	_, err = f.svc.Remove(writer, f.server.ID, servers.RemoveInput{Confirm: "edge"})
	requireAPIError(t, err, http.StatusForbidden, "forbidden")
	reader := auth.WithPrincipal(context.Background(), &auth.Principal{
		Type: auth.TypeUser, UserID: uuid.New(), WorkspaceID: f.ws, Scopes: []string{policy.ScopeServersRead},
	})
	_, err = f.svc.Remove(reader, f.server.ID, servers.RemoveInput{Confirm: "edge"})
	requireAPIError(t, err, http.StatusForbidden, "forbidden")
	require.True(t, f.exists(t, f.server.ID))
}

func TestRemoveCleanAsksTheAgentAndDeletesTheServerWhenItSucceeds(t *testing.T) {
	f := newRemoveFixture(t)
	f.goOnline(t, f.server.ID, true)

	res, err := f.svc.Remove(f.admin, f.server.ID, servers.RemoveInput{Confirm: "edge"}) // the default mode
	require.NoError(t, err)
	require.Equal(t, servers.ModeClean, res.Mode)
	require.False(t, res.Removed, "the agent still has to empty the host")
	require.False(t, res.HostUntouched)
	require.NotNil(t, res.Server)
	require.NotNil(t, res.Server.Removal)
	require.Equal(t, servers.RemovalRemoving, res.Server.Removal.State)
	require.Len(t, f.queued, 1)
	require.Equal(t, f.server.ID, f.queued[0].ServerID)
	require.Empty(t, f.agent.sent(), "the job asks the agent, not the request")

	// Removing it twice at once is refused.
	_, err = f.svc.Remove(f.admin, f.server.ID, servers.RemoveInput{Confirm: "edge"})
	requireAPIError(t, err, http.StatusConflict, "removal_in_progress")

	require.NoError(t, f.svc.RunRemoval(context.Background(), f.queued[0]))
	sent := f.agent.sent()
	require.Len(t, sent, 1)
	require.Equal(t, agentprotocol.KindDecommission, sent[0].Kind)
	require.Equal(t, f.server.ID.String(), sent[0].Payload.ServerID)
	require.False(t, f.exists(t, f.server.ID), "the row goes once the agent reported success")

	rows := f.audit(t, servers.ActionRemoved)
	require.Len(t, rows, 1)
	require.Equal(t, "clean", rows[0]["mode"])
	require.Equal(t, false, rows[0]["hostUntouched"])
	require.EqualValues(t, 3, rows[0]["containers"])
	require.EqualValues(t, 2, rows[0]["networks"])
	require.EqualValues(t, 1, rows[0]["volumes"])
}

func TestRemoveCleanFailureKeepsTheServerAndCanBeRetried(t *testing.T) {
	f := newRemoveFixture(t)
	f.goOnline(t, f.server.ID, true)
	f.agent.answer = func(uuid.UUID) (agentprotocol.ResultFrame, error) {
		return agentprotocol.ResultFrame{Outcome: agentprotocol.OutcomeFailed, Error: "volume nc-vol-1234-data is in use"}, nil
	}

	_, err := f.svc.Remove(f.admin, f.server.ID, servers.RemoveInput{Confirm: "edge"})
	require.NoError(t, err)
	require.NoError(t, f.svc.RunRemoval(context.Background(), f.queued[0]), "a failed removal is recorded, not retried by the queue")
	require.True(t, f.exists(t, f.server.ID))
	sv, err := f.svc.Get(f.admin, f.server.ID)
	require.NoError(t, err)
	require.NotNil(t, sv.Removal)
	require.Equal(t, servers.RemovalFailed, sv.Removal.State)
	require.Contains(t, sv.Removal.Error, "nc-vol-1234-data is in use")
	require.Empty(t, f.audit(t, servers.ActionRemoved))

	// The person retries; the agent is fine now.
	f.agent.answer = nil
	_, err = f.svc.Remove(f.admin, f.server.ID, servers.RemoveInput{Confirm: "edge"})
	require.NoError(t, err)
	require.Len(t, f.queued, 2)
	require.NoError(t, f.svc.RunRemoval(context.Background(), f.queued[1]))
	require.False(t, f.exists(t, f.server.ID))
}

func TestRemoveCleanFailsWhenTheAgentIsGoneMidway(t *testing.T) {
	f := newRemoveFixture(t)
	f.goOnline(t, f.server.ID, true)
	f.agent.answer = func(uuid.UUID) (agentprotocol.ResultFrame, error) {
		return agentprotocol.ResultFrame{}, errors.New("the server disconnected before it answered")
	}
	_, err := f.svc.Remove(f.admin, f.server.ID, servers.RemoveInput{Confirm: "edge"})
	require.NoError(t, err)
	require.NoError(t, f.svc.RunRemoval(context.Background(), f.queued[0]))
	sv, err := f.svc.Get(f.admin, f.server.ID)
	require.NoError(t, err)
	require.Equal(t, servers.RemovalFailed, sv.Removal.State)
	require.Contains(t, sv.Removal.Error, "disconnected")
}

func TestRemoveJobStopsWhenAServiceWasPlacedMeanwhile(t *testing.T) {
	f := newRemoveFixture(t)
	f.goOnline(t, f.server.ID, true)
	_, err := f.svc.Remove(f.admin, f.server.ID, servers.RemoveInput{Confirm: "edge"})
	require.NoError(t, err)
	f.addService(t, "app", false)

	require.NoError(t, f.svc.RunRemoval(context.Background(), f.queued[0]))
	require.Empty(t, f.agent.sent(), "nothing is removed from a host that got a service")
	sv, err := f.svc.Get(f.admin, f.server.ID)
	require.NoError(t, err)
	require.Equal(t, servers.RemovalFailed, sv.Removal.State)
	require.Contains(t, sv.Removal.Error, "are placed on this server")
}

func TestRemoveOfflineServerIsForgottenWithoutTouchingTheHost(t *testing.T) {
	f := newRemoveFixture(t)
	// Offline: the row says so and no agent is connected.
	_, err := f.pool.Exec(context.Background(), "UPDATE servers SET status = 'offline' WHERE id = $1", f.server.ID)
	require.NoError(t, err)

	res, err := f.svc.Remove(f.admin, f.server.ID, servers.RemoveInput{Confirm: "edge", Mode: servers.ModeClean})
	require.NoError(t, err)
	require.Equal(t, servers.ModeForget, res.Mode, "an offline host cannot be cleaned")
	require.True(t, res.Removed)
	require.True(t, res.HostUntouched)
	require.Empty(t, f.agent.sent())
	require.Empty(t, f.queued)
	require.False(t, f.exists(t, f.server.ID))

	rows := f.audit(t, servers.ActionRemoved)
	require.Len(t, rows, 1)
	require.Equal(t, "forget", rows[0]["mode"])
	require.Equal(t, true, rows[0]["hostUntouched"])
}

func TestRemoveForgetSkipsTheAgentOfAnOnlineServer(t *testing.T) {
	f := newRemoveFixture(t)
	f.goOnline(t, f.server.ID, true)
	res, err := f.svc.Remove(f.admin, f.server.ID, servers.RemoveInput{Confirm: "edge", Mode: servers.ModeForget})
	require.NoError(t, err)
	require.True(t, res.Removed)
	require.True(t, res.HostUntouched)
	require.Empty(t, f.agent.sent())
	require.False(t, f.exists(t, f.server.ID))
}

func TestRemoveCleanNeedsAnAgentThatCanDecommission(t *testing.T) {
	f := newRemoveFixture(t)
	f.goOnline(t, f.server.ID, false)
	_, err := f.svc.Remove(f.admin, f.server.ID, servers.RemoveInput{Confirm: "edge"})
	requireAPIError(t, err, http.StatusConflict, "agent_cannot_decommission")
	require.True(t, f.exists(t, f.server.ID))

	// Forgetting it works.
	_, err = f.svc.Remove(f.admin, f.server.ID, servers.RemoveInput{Confirm: "edge", Mode: servers.ModeForget})
	require.NoError(t, err)
	require.False(t, f.exists(t, f.server.ID))
}

func TestRemoveForTeamCleansOnlineServersAndForgetsTheRest(t *testing.T) {
	f := newRemoveFixture(t)
	online := f.server.ID
	f.goOnline(t, online, true)
	offline, err := f.q.CreateServer(context.Background(), db.CreateServerParams{WorkspaceID: f.ws, Name: "far"})
	require.NoError(t, err)
	old, err := f.q.CreateServer(context.Background(), db.CreateServerParams{WorkspaceID: f.ws, Name: "old"})
	require.NoError(t, err)
	f.goOnline(t, old.ID, false)

	out, err := f.svc.RemoveForTeam(context.Background(), online)
	require.NoError(t, err)
	require.Equal(t, servers.OutcomeCleaned, out)
	out, err = f.svc.RemoveForTeam(context.Background(), offline.ID)
	require.NoError(t, err)
	require.Equal(t, servers.OutcomeForgotten, out)
	out, err = f.svc.RemoveForTeam(context.Background(), old.ID)
	require.NoError(t, err)
	require.Equal(t, servers.OutcomeForgotten, out, "an agent that cannot decommission is forgotten")

	require.Len(t, f.agent.sent(), 1)
	require.Equal(t, online, f.agent.sent()[0].Server)
	for _, id := range []uuid.UUID{online, offline.ID, old.ID} {
		require.False(t, f.exists(t, id))
	}
	// Gone already counts as done: the job can be repeated.
	out, err = f.svc.RemoveForTeam(context.Background(), online)
	require.NoError(t, err)
	require.Equal(t, servers.OutcomeForgotten, out)
}

func TestRemoveForTeamFailsWhenTheAgentCannotEmptyTheHost(t *testing.T) {
	f := newRemoveFixture(t)
	f.goOnline(t, f.server.ID, true)
	f.agent.answer = func(uuid.UUID) (agentprotocol.ResultFrame, error) {
		return agentprotocol.ResultFrame{Outcome: agentprotocol.OutcomeFailed, Error: "volume in use"}, nil
	}
	_, err := f.svc.RemoveForTeam(context.Background(), f.server.ID)
	require.ErrorContains(t, err, "volume in use")
	require.True(t, f.exists(t, f.server.ID), "a host that was not emptied keeps its server")
}

func TestRemoveForTeamRefusesTheControlPlaneServer(t *testing.T) {
	f := newRemoveFixture(t)
	_, err := f.pool.Exec(context.Background(), "UPDATE servers SET is_control_plane = true WHERE id = $1", f.server.ID)
	require.NoError(t, err)
	_, err = f.svc.RemoveForTeam(context.Background(), f.server.ID)
	requireAPIError(t, err, http.StatusConflict, "server_is_control_plane")
}
