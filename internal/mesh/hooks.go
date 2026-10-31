package mesh

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/integrations/tailscale"
)

// audit records a workspace-level mesh action.
func (s *Service) audit(ctx context.Context, action string, ws uuid.UUID) error {
	return db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		return audit.Record(ctx, tx, audit.Entry{Action: action, ResourceType: "mesh", ResourceID: ws.String(), WorkspaceID: ws, Metadata: map[string]any{}})
	})
}

// ServerRevoked is the revoke hook (servers.AddRevokeHook): the server's agent
// is gone, so its node, the link nodes that run on it and every exposure and
// grant that involved it are forgotten, their devices are deleted from the
// tailnet (retried in the background) and the ACL regenerates (the addresses
// vanish). Link nodes on other servers that targeted it are dropped too; their
// agents remove them with the next forced sync. Failures are logged, never
// raised: revoking a server must not fail because the tailnet is unreachable.
func (s *Service) ServerRevoked(ctx context.Context, ws, serverID uuid.UUID) {
	err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		var remoteSync []uuid.UUID
		links, err := q.ListMeshLinksOnServer(ctx, serverID)
		if err != nil {
			return err
		}
		targeting, err := q.ListMeshLinksTargeting(ctx, serverID)
		if err != nil {
			return err
		}
		for _, l := range append(links, targeting...) {
			if _, err := q.DeleteMeshLink(ctx, l.ID); err != nil {
				return err
			}
			if err := s.ts.EnqueueDeleteNode(ctx, tx, ws, tailscale.NodeRef{DeviceID: l.DeviceID, IPs: l.Ips, Hostname: l.Hostname}); err != nil {
				return err
			}
			if l.ServerID != serverID {
				remoteSync = append(remoteSync, l.ServerID)
			}
		}
		exps, err := q.ListMeshExposuresOnServer(ctx, serverID)
		if err != nil {
			return err
		}
		for _, e := range exps {
			if _, err := q.DeleteMeshExposure(ctx, e.ID); err != nil {
				return err
			}
		}
		node, err := q.GetServerMesh(ctx, serverID)
		have := err == nil
		if err != nil && err != pgx.ErrNoRows {
			return err
		}
		if have {
			if _, err := q.DeleteServerMesh(ctx, serverID); err != nil {
				return err
			}
			if err := s.ts.EnqueueDeleteNode(ctx, tx, ws, tailscale.NodeRef{DeviceID: node.DeviceID, IPs: node.Ips, Hostname: node.Hostname}); err != nil {
				return err
			}
		}
		if !have && len(links) == 0 && len(targeting) == 0 && len(exps) == 0 {
			return nil
		}
		for _, id := range remoteSync {
			s.nudgeSync(ctx, tx, id, true)
		}
		s.nudgeACL(ctx, tx, ws)
		return audit.Record(ctx, tx, audit.Entry{
			Action: "mesh.node.remove", ResourceType: "server", ResourceID: serverID.String(), WorkspaceID: ws,
			Metadata: map[string]any{"reason": "server revoked", "links": len(links) + len(targeting), "exposures": len(exps)},
		})
	})
	if err != nil {
		s.log.ErrorContext(ctx, "clean up the mesh of a revoked server", "server_id", serverID.String(), "err", err)
		return
	}
	s.publish(ws, serverID)
}
