package store

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

func TestOpenFailsWhenTheStateDirCannotBeCreated(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "file")
	require.NoError(t, os.WriteFile(blocker, nil, 0o600))
	_, err := Open(filepath.Join(blocker, "state"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "create state dir")
}

func TestOpenFileFailsOnADirectoryPath(t *testing.T) {
	dir := t.TempDir()
	_, err := OpenFile(dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "open "+dir)
}

func TestOpenFileUsesTheGivenPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "custom.db")
	s, err := OpenFile(path)
	require.NoError(t, err)
	require.NoError(t, s.Close())
	_, err = os.Stat(path)
	require.NoError(t, err)
}

func TestIdentityEmptyThenReplaced(t *testing.T) {
	s, _ := openTemp(t)
	_, ok, err := s.Identity()
	require.NoError(t, err)
	assert.False(t, ok)

	require.NoError(t, s.PutIdentity(Identity{AgentID: "a1", ServerID: "s1", PrivateKey: []byte{1}}))
	require.NoError(t, s.PutIdentity(Identity{AgentID: "a2", ServerID: "s2", PrivateKey: []byte{2}, Revoked: true}))
	id, ok, err := s.Identity()
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "a2", id.AgentID)
	assert.Equal(t, "s2", id.ServerID)
	assert.Equal(t, []byte{2}, []byte(id.PrivateKey))
	assert.True(t, id.Revoked)
}

func TestMarkCommandIsANoOpForKnownIDsAndKeepsTheResult(t *testing.T) {
	s, _ := openTemp(t)
	require.NoError(t, s.PutResult(agentprotocol.ResultFrame{CommandID: "c", Outcome: agentprotocol.OutcomeSucceeded}))
	require.NoError(t, s.MarkCommand("c"))
	stored, seen := s.SeenCommand("c")
	require.True(t, seen)
	require.NotNil(t, stored, "re-marking must not erase the stored result")
	assert.Equal(t, agentprotocol.OutcomeSucceeded, stored.Outcome)
}

func TestSeenCommandForAnUnknownID(t *testing.T) {
	s, _ := openTemp(t)
	stored, seen := s.SeenCommand("never")
	assert.False(t, seen)
	assert.Nil(t, stored)
}

func TestPutResultNeedsACommandID(t *testing.T) {
	s, _ := openTemp(t)
	err := s.PutResult(agentprotocol.ResultFrame{Outcome: agentprotocol.OutcomeSucceeded})
	require.EqualError(t, err, "result has no command id")
	unacked, err := s.UnackedResults()
	require.NoError(t, err)
	assert.Empty(t, unacked)
}

func TestPutResultKeepsTheOriginalReceiptTimeAndReplacesTheResult(t *testing.T) {
	s, _ := openTemp(t)
	require.NoError(t, s.MarkCommand("c"))
	require.NoError(t, s.PutResult(agentprotocol.ResultFrame{CommandID: "c", Outcome: agentprotocol.OutcomeFailed, Error: "first"}))
	require.NoError(t, s.PutResult(agentprotocol.ResultFrame{CommandID: "c", Outcome: agentprotocol.OutcomeSucceeded}))
	stored, _ := s.SeenCommand("c")
	require.NotNil(t, stored)
	assert.Equal(t, agentprotocol.OutcomeSucceeded, stored.Outcome)
	assert.Empty(t, stored.Error)
	unacked, err := s.UnackedResults()
	require.NoError(t, err)
	require.Len(t, unacked, 1)
	assert.Equal(t, agentprotocol.OutcomeSucceeded, unacked[0].Outcome)
}

func TestAckResultOfAnUnknownCommandIsHarmless(t *testing.T) {
	s, _ := openTemp(t)
	require.NoError(t, s.AckResult("nope"))
}

func TestLoadRoutesEmptyThenSavedAndOverwritten(t *testing.T) {
	s, _ := openTemp(t)
	got, err := s.LoadRoutes()
	require.NoError(t, err)
	assert.Nil(t, got)

	require.NoError(t, s.SaveRoutes([]agentprotocol.Route{{ServiceID: "a"}}))
	require.NoError(t, s.SaveRoutes([]agentprotocol.Route{{ServiceID: "b"}, {ServiceID: "c"}}))
	got, err = s.LoadRoutes()
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, "b", got[0].ServiceID)
}

func TestTrafficAnalyticsSettingRoundTrips(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	require.NoError(t, err)
	_, ok, err := s.LoadTrafficAnalytics()
	require.NoError(t, err)
	assert.False(t, ok, "never saved is not the same as off")

	require.NoError(t, s.SaveTrafficAnalytics(true))
	require.NoError(t, s.Close())
	s, err = Open(dir)
	require.NoError(t, err)
	defer s.Close()
	enabled, ok, err := s.LoadTrafficAnalytics()
	require.NoError(t, err)
	assert.True(t, ok)
	assert.True(t, enabled)

	require.NoError(t, s.SaveTrafficAnalytics(false))
	enabled, ok, err = s.LoadTrafficAnalytics()
	require.NoError(t, err)
	assert.True(t, ok, "off was saved explicitly")
	assert.False(t, enabled)
}

func TestACMEEmailRoundTrips(t *testing.T) {
	s, _ := openTemp(t)
	got, err := s.LoadACMEEmail()
	require.NoError(t, err)
	assert.Equal(t, "", got)
	require.NoError(t, s.SaveACMEEmail("ops@example.com"))
	got, err = s.LoadACMEEmail()
	require.NoError(t, err)
	assert.Equal(t, "ops@example.com", got)
	require.NoError(t, s.SaveACMEEmail(""))
	got, err = s.LoadACMEEmail()
	require.NoError(t, err)
	assert.Equal(t, "", got, "clearing the email persists")
}

func TestProxyAdminSecretRoundTripsAndIsKeptApartFromTheACMEEmail(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	require.NoError(t, err)
	got, err := s.LoadProxyAdminSecret()
	require.NoError(t, err)
	assert.Equal(t, "", got)
	require.NoError(t, s.SaveProxyAdminSecret("s3cret"))
	require.NoError(t, s.SaveACMEEmail("a@b.c"))
	require.NoError(t, s.Close())

	s, err = Open(dir)
	require.NoError(t, err)
	defer s.Close()
	got, err = s.LoadProxyAdminSecret()
	require.NoError(t, err)
	assert.Equal(t, "s3cret", got)
	email, err := s.LoadACMEEmail()
	require.NoError(t, err)
	assert.Equal(t, "a@b.c", email)
}

func TestLoadProxyConfigWhenNoneSaved(t *testing.T) {
	s, _ := openTemp(t)
	cfg, ok, err := s.LoadProxyConfig()
	require.NoError(t, err)
	assert.False(t, ok)
	assert.Equal(t, agentprotocol.ProxyConfig{}, cfg)
}

func TestPutProbesWithNoResultsIsANoOp(t *testing.T) {
	s, _ := openTemp(t)
	require.NoError(t, s.PutProbes(nil))
	keys, _, err := s.PendingProbes(10)
	require.NoError(t, err)
	assert.Empty(t, keys)
}

var s0 = time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC)

func TestClosedStoreReportsErrors(t *testing.T) {
	s, err := Open(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, s.Close())
	assert.Error(t, s.PutIdentity(Identity{}))
	_, _, err = s.Identity()
	assert.Error(t, err)
	assert.Error(t, s.MarkCommand("x"))
	assert.Error(t, s.SaveRoutes(nil))
	_, err = s.LoadRoutes()
	assert.Error(t, err)
	assert.Error(t, s.SaveProxyAdminSecret("x"))
	_, err = s.LoadProxyAdminSecret()
	assert.Error(t, err)
	assert.Error(t, s.SaveProxyConfig(agentprotocol.ProxyConfig{}))
	_, _, err = s.LoadProxyConfig()
	assert.Error(t, err)
	assert.Error(t, s.SaveACMEEmail("x"))
	_, err = s.LoadACMEEmail()
	assert.Error(t, err)
	assert.Error(t, s.SaveTrafficAnalytics(true))
	_, _, err = s.LoadTrafficAnalytics()
	assert.Error(t, err)
	assert.Error(t, s.PutTraffic([]agentprotocol.TrafficBucket{{Host: "h"}}, s0))
	assert.Error(t, s.DeleteTraffic([][]byte{{1}}))
	_, err = s.PruneTraffic(s0)
	assert.Error(t, err)
	_, err = s.TrafficSalt("2026-10-29", func() ([]byte, error) { return []byte{1}, nil })
	assert.Error(t, err)
	assert.Error(t, s.PutProbes([]agentprotocol.ProbeResult{{ServiceID: "a"}}))
	assert.Error(t, s.DeleteProbes([][]byte{{1}}))
	_, err = s.PruneProbes(s0)
	assert.Error(t, err)
}
