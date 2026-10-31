package conn

import (
	"context"
	"errors"
	"fmt"
	"net"

	"github.com/nebulactrl/nebulactrl/internal/tunnelmux"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Instance service tunnels (pkg/agentprotocol/localtunnel.go). The agent
// initiates every stream: localproxy accepts a TCP connection on 127.0.0.1 and
// opens a stream for it. Control frames travel through the outbox like every
// other frame; data frames are binary messages from the tunnel lane, which the
// writer serves only when the outbox is empty, so heartbeats, acks and results
// never wait behind bulk data for more than one chunk.

// Errors OpenTunnel returns before the control plane is asked.
var (
	ErrNotConnected = errors.New("not connected to the control plane")
	ErrTunnelLimit  = errors.New("too many open instance service connections")
)

// OpenTunnel opens a stream to an instance service (agentprotocol.TunnelS3,
// TunnelRegistry or TunnelLoki) through the control plane and waits until it
// accepted. The returned connection also has CloseWrite (half-close). When the
// control-plane connection drops, every stream fails.
func (c *Client) OpenTunnel(ctx context.Context, service string) (net.Conn, error) {
	limit := agentprotocol.TunnelServiceLimit(service)
	if limit == 0 {
		return nil, fmt.Errorf("unknown instance service %q", service)
	}
	if !c.connected.Load() {
		return nil, ErrNotConnected
	}
	if c.tun.Len() >= agentprotocol.MaxTunnelStreams || c.tun.Count(service) >= limit {
		return nil, ErrTunnelLimit
	}
	s, err := c.tun.Dial(ctx, service)
	if err != nil {
		if errors.Is(err, tunnelmux.ErrDown) {
			return nil, ErrNotConnected
		}
		return nil, err
	}
	return s, nil
}

// tunnelControl sends a tunnel control frame; it fails while disconnected.
func (c *Client) tunnelControl(typ string, body any) error {
	if !c.Stream(typ, "", body) {
		return ErrNotConnected
	}
	return nil
}
