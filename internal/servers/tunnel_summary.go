package servers

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// ServerTunnelSummary is the Cloudflare Tunnel state of a server as the server
// views show it (internal/servers/tunnel has the full view and the actions).
type ServerTunnelSummary struct {
	Enabled     bool       `json:"enabled" doc:"The server has a Cloudflare Tunnel (or is removing one)"`
	State       string     `json:"state" enum:"off,provisioning,online,offline,needs_attention,removing" doc:"Observed connector state; off without a tunnel"`
	Message     string     `json:"message,omitempty"`
	Active      bool       `json:"active" doc:"The connector connected: the server's domains in connected zones point at the tunnel"`
	Connections int        `json:"connections" doc:"Ready connections to Cloudflare's edge"`
	CNAME       string     `json:"cname,omitempty" doc:"<tunnelId>.cfargotunnel.com"`
	ObservedAt  *time.Time `json:"observedAt"`
}

// revokeHook runs after a server was revoked (the tunnel service tears the
// server's tunnel down: the connector cannot be removed any more; the mesh
// removes the server's nodes, exposures and links; replicas and grants clean up).
type revokeHook func(ctx context.Context, workspaceID, serverID uuid.UUID)

// AddRevokeHook registers a hook that runs, in registration order, after a
// server was revoked. Hooks must not fail the revoke: they log and retry on
// their own.
func (s *Service) AddRevokeHook(h func(ctx context.Context, workspaceID, serverID uuid.UUID)) {
	s.hooks = append(s.hooks, h)
}

func (s *Service) tunnelsByServer(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]*db.ServerTunnel, error) {
	out := map[uuid.UUID]*db.ServerTunnel{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := s.q.ListServerTunnels(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("list server tunnels: %w", err)
	}
	for i := range rows {
		out[rows[i].ServerID] = &rows[i]
	}
	return out, nil
}

// tunnelSummary renders a tunnel row; nil is a server without a tunnel.
func tunnelSummary(r *db.ServerTunnel) ServerTunnelSummary {
	if r == nil {
		return ServerTunnelSummary{State: "off"}
	}
	return ServerTunnelSummary{
		Enabled: true, State: r.State, Message: r.Message, Active: r.ActivatedAt != nil && r.State != "removing",
		Connections: int(r.Connections), CNAME: r.TunnelID + agentprotocol.TunnelCNAMESuffix, ObservedAt: r.ObservedAt,
	}
}
