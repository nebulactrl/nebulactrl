package mesh

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

const dialTimeout = 5 * time.Second

// Check is the connectivity check (mesh_check): the ground truth for "does the
// whole path work", independent of any ACL API.
//
//   - LinkID + Port: the agent process dials <link node address on its
//     environment network>:<port>. The link node DNATs the connection through
//     the tailnet to the remote server node, whose serve listener answers on
//     the mesh port: connected | refused | timeout (an ACL that does not allow
//     the port, a missing path or a dead remote node all look like a timeout).
//   - PeerNodeID: the tailnet reachability of a peer server node, read from this
//     server's own node (`tailscale status --json` and a ping). PeerNodeID is
//     the remote server id when a link node here targets it, the peer's
//     tailnet IPv4, or the peer's machine name.
//
// A payload that names neither returns an error.
func (m *Manager) Check(ctx context.Context, p agentprotocol.MeshCheckPayload) (agentprotocol.MeshCheckResult, error) {
	switch {
	case p.PeerNodeID != "":
		return m.checkPeer(ctx, p.PeerNodeID), nil
	case p.LinkID != "" && p.Port >= 1 && p.Port <= 65535:
		if !idRe.MatchString(p.LinkID) {
			return agentprotocol.MeshCheckResult{}, errors.New("invalid link id")
		}
		return m.checkLink(ctx, p.LinkID, p.Port), nil
	}
	return agentprotocol.MeshCheckResult{}, errors.New("mesh_check needs a linkId and a port, or a peerNodeId")
}

func noPath(msg string) agentprotocol.MeshCheckResult {
	return agentprotocol.MeshCheckResult{Result: agentprotocol.MeshCheckNoPath, Message: msg}
}

func (m *Manager) checkLink(ctx context.Context, linkID string, port int) agentprotocol.MeshCheckResult {
	info, err := m.eng.ContainerInspect(ctx, agentprotocol.MeshLinkContainer(linkID))
	switch {
	case errors.Is(err, docker.ErrNotFound):
		return noPath("The link node does not exist on this server.")
	case err != nil:
		return noPath("Cannot inspect the link node: " + err.Error())
	case !info.Running:
		return noPath("The link node is not running.")
	}
	env := info.Labels[agentprotocol.LabelEnvironment]
	network, ip := "", ""
	for _, n := range []string{agentprotocol.EnvNetwork(env), agentprotocol.EnvInternalNetwork(env)} {
		if ep, ok := info.Networks[n]; ok && ep.IP != "" {
			network, ip = n, ep.IP
			break
		}
	}
	if ip == "" {
		return noPath("The link node has no address on the environment network.")
	}
	if m.Access != nil {
		defer m.Access(ctx, network)()
	}
	dial := m.Dial
	if dial == nil {
		dial = (&net.Dialer{}).DialContext
	}
	dctx, cancel := context.WithTimeout(ctx, dialTimeout)
	defer cancel()
	start := m.now()
	c, err := dial(dctx, "tcp", net.JoinHostPort(ip, strconv.Itoa(port)))
	if err == nil {
		_ = c.Close()
		return agentprotocol.MeshCheckResult{Result: agentprotocol.MeshCheckConnected, LatencyMs: int(m.now().Sub(start).Milliseconds())}
	}
	var ne net.Error
	switch {
	case errors.Is(err, syscall.ECONNREFUSED) || strings.Contains(err.Error(), "connection refused"):
		return agentprotocol.MeshCheckResult{Result: agentprotocol.MeshCheckRefused,
			Message: "The remote server node refused the connection: nothing is exposed on that mesh port."}
	case errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout()):
		return agentprotocol.MeshCheckResult{Result: agentprotocol.MeshCheckTimeout,
			Message: "No answer within " + dialTimeout.String() + ": the tunnel is down, the ACL does not allow the port, or the remote server node is not running."}
	case ctx.Err() != nil:
		return noPath("The check was canceled.")
	}
	return noPath("Cannot reach the link node: " + err.Error())
}

// localNode picks the running node the peer check reads from: the server node
// first, any link node otherwise.
func (m *Manager) localNode(ctx context.Context) (docker.ContainerInfo, bool) {
	cs, err := m.eng.ContainerList(ctx, docker.ListFilter{Labels: map[string]string{
		agentprotocol.LabelManaged: "true", agentprotocol.LabelMesh: "",
	}})
	if err != nil {
		return docker.ContainerInfo{}, false
	}
	var link *docker.ContainerInfo
	for i, c := range cs {
		if !c.Running {
			continue
		}
		if c.Labels[agentprotocol.LabelMeshKind] == agentprotocol.MeshKindServer {
			return c, true
		}
		link = &cs[i]
	}
	if link != nil {
		return *link, true
	}
	return docker.ContainerInfo{}, false
}

func (m *Manager) checkPeer(ctx context.Context, peer string) agentprotocol.MeshCheckResult {
	node, ok := m.localNode(ctx)
	if !ok {
		return noPath("No mesh node is running on this server.")
	}
	st := m.readStatusLimit(ctx, node.ID, true, nil, 0)
	if st.State != agentprotocol.MeshStateOnline {
		return noPath("This server's mesh node is " + st.State + ".")
	}
	ip := m.resolvePeer(peer, st.Peers)
	if ip == "" {
		return noPath("The peer is not known to this server's mesh node.")
	}
	var found *agentprotocol.MeshPeer
	for i := range st.Peers {
		if st.Peers[i].IP == ip {
			found = &st.Peers[i]
		}
	}
	if found == nil {
		return noPath("The peer is not in this server's tailnet map: the ACL or the coordinator does not show it.")
	}
	if !found.Online {
		return noPath("The peer is offline in the tailnet.")
	}
	reach, latency, direct := m.pingTarget(ctx, node.ID, ip)
	switch {
	case reach == nil:
		return noPath("Could not run the ping.")
	case !*reach:
		return agentprotocol.MeshCheckResult{Result: agentprotocol.MeshCheckTimeout, Message: "The peer does not answer a tailnet ping."}
	}
	msg := "Relayed"
	if direct {
		msg = "Direct"
	}
	return agentprotocol.MeshCheckResult{Result: agentprotocol.MeshCheckConnected, Message: msg, LatencyMs: int(latency.Milliseconds())}
}

// resolvePeer maps a PeerNodeID to a tailnet IPv4.
func (m *Manager) resolvePeer(peer string, peers []agentprotocol.MeshPeer) string {
	if a, err := netip.ParseAddr(peer); err == nil && a.Is4() {
		return peer
	}
	for _, s := range m.desiredSpecs() {
		if s.Kind == agentprotocol.MeshKindLink && s.RemoteServerID == peer && s.TargetIP != "" {
			return s.TargetIP
		}
	}
	for _, p := range peers {
		if label, _, _ := strings.Cut(p.Host, "."); label == peer {
			return p.IP
		}
	}
	return ""
}
