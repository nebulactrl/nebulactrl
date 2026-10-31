package mesh

import (
	"context"
	"errors"
	"net"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

type timeoutErr struct{}

func (timeoutErr) Error() string   { return "i/o timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

var _ net.Error = timeoutErr{}

func linkRig(t *testing.T) (*fakeEngine, *Manager) {
	t.Helper()
	f := newFake(statusOnline)
	m := newManager(f)
	st := sync1(t, m, keysFor(linkID), nil, provision(linkSpec()))
	require.Equal(t, agentprotocol.MeshStateOnline, st.Nodes[0].State, st.Nodes[0].Message)
	return f, m
}

func TestCheckLinkDialsTheLinkAddressOnTheEnvironmentNetwork(t *testing.T) {
	_, m := linkRig(t)
	var dialed string
	var released bool
	m.Dial = func(_ context.Context, network, addr string) (net.Conn, error) {
		dialed = network + " " + addr
		return fakeDial(nil)(context.Background(), network, addr)
	}
	m.Access = func(_ context.Context, networks ...string) func() {
		require.Equal(t, []string{"nebulactrl-env-aaaaaaaa"}, networks)
		return func() { released = true }
	}
	res, err := m.Check(context.Background(), agentprotocol.MeshCheckPayload{LinkID: linkID, Port: 15001})
	require.NoError(t, err)
	require.Equal(t, agentprotocol.MeshCheckConnected, res.Result)
	require.Regexp(t, `^tcp 10\.0\.0\.\d+:15001$`, dialed)
	require.True(t, released, "the agent's access to the network is released after the probe")
}

func TestCheckLinkOutcomes(t *testing.T) {
	_, m := linkRig(t)
	for name, c := range map[string]struct {
		err  error
		want string
	}{
		"refused":      {&net.OpError{Op: "dial", Err: syscall.ECONNREFUSED}, agentprotocol.MeshCheckRefused},
		"refused text": {errors.New("dial tcp: connection refused"), agentprotocol.MeshCheckRefused},
		"timeout":      {timeoutErr{}, agentprotocol.MeshCheckTimeout},
		"deadline":     {context.DeadlineExceeded, agentprotocol.MeshCheckTimeout},
		"unreachable":  {errors.New("network is unreachable"), agentprotocol.MeshCheckNoPath},
	} {
		m.Dial = fakeDial(c.err)
		res, err := m.Check(context.Background(), agentprotocol.MeshCheckPayload{LinkID: linkID, Port: 15001})
		require.NoError(t, err, name)
		require.Equal(t, c.want, res.Result, name)
		require.NotEmpty(t, res.Message, name)
	}
}

func TestCheckLinkNoPath(t *testing.T) {
	f := newFake(statusOnline)
	m := newManager(f)
	m.Dial = fakeDial(nil)
	res, _ := m.Check(context.Background(), agentprotocol.MeshCheckPayload{LinkID: linkID, Port: 15001})
	require.Equal(t, agentprotocol.MeshCheckNoPath, res.Result)
	require.Contains(t, res.Message, "does not exist")

	f.seed("nc-meshl-1a1a1a1a", false, map[string]string{agentprotocol.LabelManaged: "true", agentprotocol.LabelMesh: linkID})
	res, _ = m.Check(context.Background(), agentprotocol.MeshCheckPayload{LinkID: linkID, Port: 15001})
	require.Equal(t, agentprotocol.MeshCheckNoPath, res.Result)
	require.Contains(t, res.Message, "not running")

	f.mu.Lock()
	f.find("nc-meshl-1a1a1a1a").Running = true
	f.mu.Unlock()
	res, _ = m.Check(context.Background(), agentprotocol.MeshCheckPayload{LinkID: linkID, Port: 15001})
	require.Contains(t, res.Message, "no address")
}

func TestCheckRejectsBadPayloads(t *testing.T) {
	m := newManager(newFake(statusOnline))
	for name, p := range map[string]agentprotocol.MeshCheckPayload{
		"empty":    {},
		"no port":  {LinkID: linkID},
		"bad port": {LinkID: linkID, Port: 70000},
		"bad id":   {LinkID: "../x", Port: 1},
	} {
		_, err := m.Check(context.Background(), p)
		require.Error(t, err, name)
	}
}

func TestCheckPeerReadsTheTailnetFromTheServerNode(t *testing.T) {
	f := newFake(statusOnline)
	m := newManager(f)
	sync1(t, m, keysFor(srvID, linkID), nil, provision(serverSpec()), provision(linkSpec()))

	// By the peer's tailnet address, by the remote server id a link targets,
	// and by machine name.
	for _, peer := range []string{"100.64.0.9", remote, "nc-other"} {
		res, err := m.Check(context.Background(), agentprotocol.MeshCheckPayload{PeerNodeID: peer})
		require.NoError(t, err)
		require.Equal(t, agentprotocol.MeshCheckConnected, res.Result, peer+": "+res.Message)
		require.Equal(t, "Direct", res.Message)
		require.EqualValues(t, 3, res.LatencyMs)
	}

	f.pingOut = "pong from nc-other (100.64.0.9) via DERP(nyc) in 41ms\n"
	res, _ := m.Check(context.Background(), agentprotocol.MeshCheckPayload{PeerNodeID: "100.64.0.9"})
	require.Equal(t, "Relayed", res.Message)
	require.EqualValues(t, 41, res.LatencyMs)

	f.pingOut, f.pingCode = "ping timed out", 1
	res, _ = m.Check(context.Background(), agentprotocol.MeshCheckPayload{PeerNodeID: "100.64.0.9"})
	require.Equal(t, agentprotocol.MeshCheckTimeout, res.Result)

	// A peer the ACL does not show, an unknown name, and no node at all.
	res, _ = m.Check(context.Background(), agentprotocol.MeshCheckPayload{PeerNodeID: "100.64.0.77"})
	require.Equal(t, agentprotocol.MeshCheckNoPath, res.Result)
	res, _ = m.Check(context.Background(), agentprotocol.MeshCheckPayload{PeerNodeID: "nope"})
	require.Equal(t, agentprotocol.MeshCheckNoPath, res.Result)
	res, _ = newManager(newFake(statusOnline)).Check(context.Background(), agentprotocol.MeshCheckPayload{PeerNodeID: "100.64.0.9"})
	require.Equal(t, agentprotocol.MeshCheckNoPath, res.Result)
}

func TestParsePong(t *testing.T) {
	d, direct := parsePong("pong from x (100.64.0.9) via 1.2.3.4:41641 in 3ms")
	require.Equal(t, 3*time.Millisecond, d)
	require.True(t, direct)
	d, direct = parsePong("pong from x (100.64.0.9) via DERP(fra) in 41ms")
	require.Equal(t, 41*time.Millisecond, d)
	require.False(t, direct)
	_, direct = parsePong("timeout")
	require.False(t, direct)
}

// A link node whose remote node stops answering is degraded, not online.
func TestLinkNodeIsDegradedWhenTheTargetDoesNotAnswer(t *testing.T) {
	f, m := linkRig(t)
	f.mu.Lock()
	f.pingOut, f.pingCode = "no reply", 1
	f.mu.Unlock()
	m.forgetStatus(linkID)
	st := m.Status(context.Background())
	require.Len(t, st.Nodes, 1)
	require.Equal(t, agentprotocol.MeshStateDegraded, st.Nodes[0].State)
	require.NotNil(t, st.Nodes[0].TargetReachable)
	require.False(t, *st.Nodes[0].TargetReachable)
}
