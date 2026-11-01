package mesh

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// serverState maps an agent-reported node state onto server_mesh.state (which
// has no degraded or removed).
func serverState(st string) string {
	switch st {
	case agentprotocol.MeshStateOnline, agentprotocol.MeshStateDegraded:
		return "online"
	case agentprotocol.MeshStateOffline:
		return "offline"
	case agentprotocol.MeshStateNeedsAttn:
		return "needs_attention"
	}
	return "provisioning"
}

// linkState maps an agent-reported node state onto mesh_links.state.
func linkState(st string) string {
	switch st {
	case agentprotocol.MeshStateOnline, agentprotocol.MeshStateDegraded, agentprotocol.MeshStateOffline, agentprotocol.MeshStateNeedsAttn:
		return st
	}
	return "provisioning"
}

// HandleResult applies the result of a sync_mesh command. remove_mesh and
// mesh_check need nothing: removal already forgot the rows, and checks are read
// by the caller that waits for them. Duplicate delivery is expected, so
// everything here is idempotent.
func (s *Service) HandleResult(ctx context.Context, serverID, commandID uuid.UUID, res agentprotocol.ResultFrame) error {
	cmd, err := s.q.GetAgentCommand(ctx, commandID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("get command: %w", err)
	}
	if cmd.Kind != agentprotocol.KindSyncMesh {
		return nil
	}
	var p agentprotocol.SyncMeshPayload
	if json.Unmarshal(cmd.Payload, &p) != nil {
		return nil
	}
	var st agentprotocol.MeshStatus
	if len(res.Data) > 0 {
		_ = json.Unmarshal(res.Data, &st)
	}
	byID := map[string]agentprotocol.MeshNodeStatus{}
	for _, n := range st.Nodes {
		byID[n.NodeID] = n
	}
	srv, err := s.q.GetServerByID(ctx, serverID)
	if err != nil {
		return fmt.Errorf("get server: %w", err)
	}
	ipsChanged := false
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		for _, sp := range p.Nodes {
			id, err := uuid.Parse(sp.NodeID)
			if err != nil {
				continue
			}
			ns, have := byID[sp.NodeID]
			if res.Outcome != agentprotocol.OutcomeSucceeded {
				ns = agentprotocol.MeshNodeStatus{NodeID: sp.NodeID, Kind: sp.Kind, State: agentprotocol.MeshStateNeedsAttn, Message: res.Error}
				if ns.Message == "" {
					ns.Message = "The agent could not apply the mesh configuration."
				}
				have = true
			}
			if !have {
				ns = agentprotocol.MeshNodeStatus{NodeID: sp.NodeID, Kind: sp.Kind, State: agentprotocol.MeshStateProvisioning}
			}
			changed, err := s.recordNode(ctx, q, id, sp, ns, true)
			if err != nil {
				return err
			}
			ipsChanged = ipsChanged || changed
		}
		return nil
	})
	if err != nil {
		return err
	}
	if ipsChanged {
		s.nudgeACL(ctx, nil, srv.WorkspaceID)
	}
	s.publish(srv.WorkspaceID, serverID)
	return nil
}

// recordNode stores what the agent reported for one node. applied is true for a
// command result (the spec was processed: remember it). It reports whether the
// node's tailnet addresses changed.
func (s *Service) recordNode(ctx context.Context, q *db.Queries, id uuid.UUID, sp agentprotocol.MeshNodeSpec, ns agentprotocol.MeshNodeStatus, applied bool) (bool, error) {
	ips := ns.IPs
	if ips == nil {
		ips = []string{}
	}
	attention := ns.State == agentprotocol.MeshStateNeedsAttn
	switch sp.Kind {
	case agentprotocol.MeshKindServer:
		prev, err := q.GetServerMesh(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		peers, _ := json.Marshal(nonNilPeers(ns.Peers))
		row, err := q.SetServerMeshObserved(ctx, db.SetServerMeshObservedParams{
			ServerID: id, State: serverState(ns.State), Message: ns.Message, Fqdn: ns.FQDN, Ips: ips, DeviceID: ns.DeviceID, Peers: peers,
		})
		if err != nil {
			return false, err
		}
		if applied && !attention {
			sp.Provision = false
			raw, _ := json.Marshal(sp)
			if err := q.SetServerMeshApplied(ctx, db.SetServerMeshAppliedParams{ServerID: id, Applied: raw, IdentityHash: sp.IdentityHash}); err != nil {
				return false, err
			}
		}
		return !slices.Equal(prev.Ips, row.Ips), nil
	default:
		prev, err := q.GetMeshLinkByID(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		row, err := q.SetMeshLinkObserved(ctx, db.SetMeshLinkObservedParams{ID: id, State: linkState(ns.State), Message: ns.Message, Ips: ips, DeviceID: ns.DeviceID})
		if err != nil {
			return false, err
		}
		if applied && !attention {
			sp.Provision = false
			raw, _ := json.Marshal(sp)
			if err := q.SetMeshLinkApplied(ctx, db.SetMeshLinkAppliedParams{ID: id, Applied: raw, IdentityHash: sp.IdentityHash, TargetIp: sp.TargetIP}); err != nil {
				return false, err
			}
		}
		return !slices.Equal(prev.Ips, row.Ips), nil
	}
}

func nonNilPeers(p []agentprotocol.MeshPeer) []agentprotocol.MeshPeer {
	if p == nil {
		return []agentprotocol.MeshPeer{}
	}
	return p
}

// HandleUndelivered handles a sync_mesh command that never reached its agent
// (expired, canceled, or its secrets could not be resolved, for example while
// the mesh is paused). The key it may have minted is revoked; the nodes stay
// "provisioning" so the next reconcile sends the set again.
func (s *Service) HandleUndelivered(ctx context.Context, commandID uuid.UUID, msg string) error {
	cmd, err := s.q.GetAgentCommand(ctx, commandID)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && cmd.Kind != agentprotocol.KindSyncMesh) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("get command: %w", err)
	}
	var p agentprotocol.SyncMeshPayload
	if json.Unmarshal(cmd.Payload, &p) != nil {
		return nil
	}
	for _, sp := range p.Nodes {
		if !sp.Provision {
			continue
		}
		id, err := uuid.Parse(sp.NodeID)
		if err != nil {
			continue
		}
		var ws uuid.UUID
		var keyID string
		switch sp.Kind {
		case agentprotocol.MeshKindServer:
			row, err := s.q.GetServerMesh(ctx, id)
			if err != nil {
				continue
			}
			ws, keyID = row.WorkspaceID, row.AuthKeyID
			_ = s.q.ClearServerMeshKey(ctx, id)
			_ = s.q.SetServerMeshState(ctx, db.SetServerMeshStateParams{ServerID: id, State: "provisioning", Message: msg})
		default:
			row, err := s.q.GetMeshLinkByID(ctx, id)
			if err != nil {
				continue
			}
			ws, keyID = row.WorkspaceID, row.AuthKeyID
			_ = s.q.ClearMeshLinkKey(ctx, id)
			_ = s.q.SetMeshLinkState(ctx, db.SetMeshLinkStateParams{ID: id, State: "provisioning", Message: msg})
		}
		if keyID != "" {
			if err := s.ts.RevokeNodeKey(ctx, ws, keyID); err != nil {
				s.log.WarnContext(ctx, "could not revoke the key of an undelivered command", "node_id", sp.NodeID, "err", err)
			}
		}
	}
	return nil
}

// ObserveHeartbeat records the mesh status an agent reports every heartbeat
// (nil when it runs no mesh container). Reported nodes the control plane does
// not desire (strays, a disabled mesh, a lost removal) trigger a forced sync.
func (s *Service) ObserveHeartbeat(ctx context.Context, serverID uuid.UUID, ms *agentprotocol.MeshStatus) error {
	srv, err := s.q.GetServerByID(ctx, serverID)
	if err != nil {
		return fmt.Errorf("get server: %w", err)
	}
	sn, err := s.load(ctx, s.q, srv.WorkspaceID)
	if err != nil {
		return err
	}
	want := map[string]agentprotocol.MeshNodeSpec{}
	for _, sp := range sn.desiredNodes(serverID, false) {
		want[sp.NodeID] = sp
	}
	reported := map[string]bool{}
	ipsChanged, changed, stray := false, false, false
	if ms != nil {
		for _, ns := range ms.Nodes {
			if ns.State == agentprotocol.MeshStateRemoved {
				continue
			}
			sp, ok := want[ns.NodeID]
			if !ok {
				stray = true
				continue
			}
			reported[ns.NodeID] = true
			id, _ := uuid.Parse(ns.NodeID)
			if sp.Kind == agentprotocol.MeshKindServer && sn.nodes[id].ServerID == uuid.Nil {
				continue
			}
			var prevState, prevMsg string
			if sp.Kind == agentprotocol.MeshKindServer {
				n := sn.nodes[serverID]
				prevState, prevMsg = n.State, n.Message
			} else {
				for _, l := range sn.links {
					if l.ID == id {
						prevState, prevMsg = l.State, l.Message
					}
				}
			}
			ipc, err := s.recordNode(ctx, db.New(s.pool), id, sp, ns, false)
			if err != nil {
				return err
			}
			stateChanged := sp.Kind == agentprotocol.MeshKindServer && serverState(ns.State) != prevState || sp.Kind == agentprotocol.MeshKindLink && linkState(ns.State) != prevState
			changed = changed || stateChanged || ns.Message != prevMsg
			ipsChanged = ipsChanged || ipc
		}
	}
	// A desired node that the agent no longer reports is offline, after the
	// grace that a just-written node needs to appear.
	for id, sp := range want {
		if reported[id] {
			continue
		}
		nid, _ := uuid.Parse(id)
		switch sp.Kind {
		case agentprotocol.MeshKindServer:
			n := sn.nodes[serverID]
			if n.State == "provisioning" || n.State == "needs_attention" || s.now().Sub(n.UpdatedAt) < deployGrace {
				continue
			}
			if n.State != "offline" {
				_ = s.q.SetServerMeshState(ctx, db.SetServerMeshStateParams{ServerID: serverID, State: "offline", Message: "The mesh node is not running on this server."})
				changed = true
			}
		default:
			for _, l := range sn.links {
				if l.ID != nid || l.State == "provisioning" || l.State == "needs_attention" || l.State == "offline" || s.now().Sub(l.CreatedAt) < deployGrace {
					continue
				}
				_ = s.q.SetMeshLinkState(ctx, db.SetMeshLinkStateParams{ID: nid, State: "offline", Message: "The link node is not running on this server."})
				changed = true
			}
		}
	}
	if ipsChanged {
		s.nudgeACL(ctx, nil, srv.WorkspaceID)
		// Link nodes target the IP of the remote server node: re-send when it moved.
		for _, l := range sn.links {
			if l.RemoteServerID == serverID {
				s.nudgeSync(ctx, nil, l.ServerID, true)
			}
		}
	}
	if stray {
		s.nudgeSync(ctx, nil, serverID, true)
	}
	if changed || ipsChanged {
		s.publish(srv.WorkspaceID, serverID)
	}
	return nil
}
