package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

func openTemp(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	s, err := Open(dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	return s, dir
}

func TestOpenCreatesFileMode0600(t *testing.T) {
	_, dir := openTemp(t)
	fi, err := os.Stat(filepath.Join(dir, FileName))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), fi.Mode().Perm())
}

func TestCommandDedupeReturnsStoredResult(t *testing.T) {
	s, _ := openTemp(t)

	_, seen := s.SeenCommand("cmd-1")
	require.False(t, seen)

	require.NoError(t, s.MarkCommand("cmd-1"))
	stored, seen := s.SeenCommand("cmd-1")
	require.True(t, seen)
	require.Nil(t, stored, "received but unfinished command has no stored result")

	res := agentprotocol.ResultFrame{
		CommandID: "cmd-1",
		Outcome:   agentprotocol.OutcomeSucceeded,
		Data:      json.RawMessage(`{"containerId":"abc"}`),
	}
	require.NoError(t, s.PutResult(res))

	stored, seen = s.SeenCommand("cmd-1")
	require.True(t, seen)
	require.NotNil(t, stored)
	require.Equal(t, res.Outcome, stored.Outcome)
	require.JSONEq(t, string(res.Data), string(stored.Data))

	// Acknowledging drops the unacked copy but keeps dedupe.
	unacked, err := s.UnackedResults()
	require.NoError(t, err)
	require.Len(t, unacked, 1)
	require.NoError(t, s.AckResult("cmd-1"))
	unacked, err = s.UnackedResults()
	require.NoError(t, err)
	require.Empty(t, unacked)
	stored, seen = s.SeenCommand("cmd-1")
	require.True(t, seen)
	require.NotNil(t, stored)
}

func TestStatePersistsAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	require.NoError(t, err)
	require.NoError(t, s.PutIdentity(Identity{AgentID: "a", ServerID: "s", PrivateKey: []byte{1, 2, 3}}))
	require.NoError(t, s.PutResult(agentprotocol.ResultFrame{CommandID: "c", Outcome: agentprotocol.OutcomeFailed, Error: "boom"}))
	require.NoError(t, s.SaveRoutes([]agentprotocol.Route{{ServiceID: "svc", Hosts: []string{"a.localhost"}, Upstreams: []string{"x:80", "y:80"}}}))
	require.NoError(t, s.Close())

	s, err = Open(dir)
	require.NoError(t, err)
	defer s.Close()

	id, ok, err := s.Identity()
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "a", id.AgentID)

	unacked, err := s.UnackedResults()
	require.NoError(t, err)
	require.Len(t, unacked, 1)
	require.Equal(t, "boom", unacked[0].Error)

	routes, err := s.LoadRoutes()
	require.NoError(t, err)
	require.Len(t, routes, 1)
	require.Equal(t, []string{"x:80", "y:80"}, routes[0].Upstreams)
}

func TestPruneKeepsUnackedAndRecent(t *testing.T) {
	s, _ := openTemp(t)
	require.NoError(t, s.MarkCommand("old-acked"))
	require.NoError(t, s.PutResult(agentprotocol.ResultFrame{CommandID: "old-unacked", Outcome: agentprotocol.OutcomeSucceeded}))
	require.NoError(t, s.AckResult("old-unacked"))
	require.NoError(t, s.PutResult(agentprotocol.ResultFrame{CommandID: "pending", Outcome: agentprotocol.OutcomeSucceeded}))

	n, err := s.PruneCommands(time.Now().Add(time.Hour))
	require.NoError(t, err)
	require.Equal(t, 2, n)
	_, seen := s.SeenCommand("pending")
	require.True(t, seen, "commands with an unacked result are never pruned")
	_, seen = s.SeenCommand("old-acked")
	require.False(t, seen)
}
