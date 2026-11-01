package mesh

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"github.com/nebulactrl/nebulactrl/internal/agents"
	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/integrations/tailscale"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Handles reports whether kind is a command of this domain.
func Handles(kind string) bool {
	return kind == agentprotocol.KindSyncMesh || kind == agentprotocol.KindRemoveMesh || kind == agentprotocol.KindMeshCheck
}

// sameSpec compares two node specs ignoring Provision (which only says how the
// node gets created, not what it is).
func sameSpec(a, b agentprotocol.MeshNodeSpec) bool {
	a.Provision, b.Provision = false, false
	ra, _ := json.Marshal(a)
	rb, _ := json.Marshal(b)
	return string(ra) == string(rb)
}

func decodeApplied(raw json.RawMessage) (agentprotocol.MeshNodeSpec, bool) {
	var sp agentprotocol.MeshNodeSpec
	if len(raw) == 0 || json.Unmarshal(raw, &sp) != nil || sp.NodeID == "" {
		return sp, false
	}
	return sp, true
}

// converged reports whether every desired node equals what the agent last
// applied and no node waits for provisioning.
func (sn *snapshot) converged(serverID uuid.UUID, want []agentprotocol.MeshNodeSpec) bool {
	for _, sp := range want {
		var raw json.RawMessage
		var reprov bool
		switch sp.Kind {
		case agentprotocol.MeshKindServer:
			n := sn.nodes[serverID]
			raw, reprov = n.Applied, n.NeedsReprovision && n.State != "needs_attention"
		default:
			for _, l := range sn.links {
				if l.ID.String() == sp.NodeID {
					raw, reprov = l.Applied, l.NeedsReprovision && l.State != "needs_attention"
				}
			}
		}
		if reprov {
			return false
		}
		got, ok := decodeApplied(raw)
		if !ok || !sameSpec(got, sp) {
			return false
		}
	}
	return true
}

// SyncServer sends the complete desired mesh set of a server when it differs
// from what the agent last applied (or when force is set, which the mutations
// use so removals reach the agent). It waits while the mesh is paused and while
// an earlier mesh command is still open: the set is complete and idempotent, so
// the next reconcile sends it again.
func (s *Service) SyncServer(ctx context.Context, serverID uuid.UUID, force bool) error {
	srv, err := s.q.GetServerByID(ctx, serverID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("get server: %w", err)
	}
	if srv.Status == "revoked" || !SupportsMesh(srv) {
		return nil
	}
	sn, err := s.load(ctx, s.q, srv.WorkspaceID)
	if err != nil {
		return err
	}
	if sn.set.Paused {
		return nil
	}
	if !sn.haveInteg {
		return nil
	}
	want := sn.desiredNodes(serverID, true)
	if len(want) == 0 && !force {
		return nil
	}
	if !force && sn.converged(serverID, want) {
		return nil
	}
	open, err := s.q.ListMeshOpenCommands(ctx, serverID)
	if err != nil {
		return fmt.Errorf("list open mesh commands: %w", err)
	}
	for _, c := range open {
		if c.Status != "acked" || s.now().Sub(c.CreatedAt) < openCommandGrace {
			return nil
		}
	}
	// A node that needs attention is never re-provisioned automatically: every
	// attempt mints a key. Re-provision is an explicit action.
	for i := range want {
		if !want[i].Provision {
			continue
		}
		switch want[i].Kind {
		case agentprotocol.MeshKindServer:
			if sn.nodes[serverID].State == "needs_attention" {
				want[i].Provision = false
			}
		default:
			for _, l := range sn.links {
				if l.ID.String() == want[i].NodeID && l.State == "needs_attention" {
					want[i].Provision = false
				}
			}
		}
	}
	return db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		return s.enqueueSync(ctx, tx, serverID, want)
	})
}

// enqueueSync stores a sync_mesh command with the complete set. The payload
// holds no key: it is minted when the command is sent.
func (s *Service) enqueueSync(ctx context.Context, tx pgx.Tx, serverID uuid.UUID, nodes []agentprotocol.MeshNodeSpec) error {
	if nodes == nil {
		nodes = []agentprotocol.MeshNodeSpec{}
	}
	raw, err := json.Marshal(agentprotocol.SyncMeshPayload{Nodes: nodes})
	if err != nil {
		return err
	}
	if _, err := s.disp.Enqueue(ctx, tx, agents.NewCommand{ServerID: serverID, Kind: agentprotocol.KindSyncMesh, Payload: raw}); err != nil {
		return fmt.Errorf("enqueue sync_mesh: %w", err)
	}
	return nil
}

// SecretsForCommand attaches the send-time node keys of a sync_mesh command:
// one single-use, pre-authorized, non-ephemeral, tagged key per node that is
// being provisioned. The previous device of a re-provisioned node is deleted
// first so the new one keeps its name. Keys are returned for the outgoing frame
// only.
func (s *Service) SecretsForCommand(ctx context.Context, kind string, payload json.RawMessage) (map[string]string, error) {
	if kind != agentprotocol.KindSyncMesh {
		return nil, nil
	}
	var p agentprotocol.SyncMeshPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return nil, fmt.Errorf("decode sync_mesh payload: %w", err)
	}
	out := map[string]string{}
	for _, sp := range p.Nodes {
		if !sp.Provision {
			continue
		}
		key, err := s.mintFor(ctx, sp)
		if err != nil {
			return nil, err
		}
		if key != "" {
			out[agentprotocol.SecretMeshKeyPrefix+sp.NodeID] = key
		}
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// mintFor mints the key of one node being provisioned and records its id.
func (s *Service) mintFor(ctx context.Context, sp agentprotocol.MeshNodeSpec) (string, error) {
	id, err := uuid.Parse(sp.NodeID)
	if err != nil {
		return "", fmt.Errorf("mesh node id: %w", err)
	}
	var ws uuid.UUID
	var ref tailscale.NodeRef
	var oldKey string
	switch sp.Kind {
	case agentprotocol.MeshKindServer:
		row, err := s.q.GetServerMesh(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return "", nil // disabled meanwhile
		}
		if err != nil {
			return "", err
		}
		ws, oldKey, ref = row.WorkspaceID, row.AuthKeyID, tailscale.NodeRef{DeviceID: row.DeviceID, IPs: row.Ips, Hostname: row.Hostname}
	default:
		row, err := s.q.GetMeshLinkByID(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return "", nil
		}
		if err != nil {
			return "", err
		}
		ws, oldKey, ref = row.WorkspaceID, row.AuthKeyID, tailscale.NodeRef{DeviceID: row.DeviceID, IPs: row.Ips, Hostname: row.Hostname}
	}
	set, err := s.settings(ctx, s.q, ws)
	if err != nil {
		return "", err
	}
	if set.Paused {
		return "", apierr.MeshPaused(set.PausedReason)
	}
	if ref.DeviceID != "" || len(ref.IPs) > 0 {
		if err := s.ts.DeleteNode(ctx, ws, ref); err != nil {
			return "", fmt.Errorf("remove the previous tailnet device: %w", err)
		}
		if sp.Kind == agentprotocol.MeshKindServer {
			_ = s.q.ClearServerMeshDevice(ctx, id)
		} else {
			_ = s.q.ClearMeshLinkDevice(ctx, id)
		}
	}
	if oldKey != "" {
		if err := s.ts.RevokeNodeKey(ctx, ws, oldKey); err != nil {
			s.log.WarnContext(ctx, "could not revoke the previous node key", "node_id", sp.NodeID, "err", err)
		}
	}
	tag := set.MeshTag
	if tag == "" {
		tag = DefaultMeshTag
	}
	key, err := s.ts.MintNodeKey(ctx, ws, tailscale.MintRequest{Tags: []string{tag}, Expiry: keyExpiry, Description: "nebulactrl mesh " + sp.Hostname})
	if errors.Is(err, tailscale.ErrNotConfigured) {
		return "", apierr.MeshNotConfigured()
	}
	if err != nil {
		return "", fmt.Errorf("mint a mesh node key: %w", err)
	}
	switch sp.Kind {
	case agentprotocol.MeshKindServer:
		err = s.q.SetServerMeshKey(ctx, db.SetServerMeshKeyParams{ServerID: id, AuthKeyID: key.ID})
	default:
		err = s.q.SetMeshLinkKey(ctx, db.SetMeshLinkKeyParams{ID: id, AuthKeyID: key.ID})
	}
	if err != nil {
		s.log.WarnContext(ctx, "could not remember the node key id", "node_id", sp.NodeID, "err", err)
	}
	if key.ID != "" && s.jobs != nil {
		// An unused key is revoked after it expired, and the node flagged.
		_ = s.jobs.Insert(ctx, KeySweepArgs{NodeID: id, NodeKind: sp.Kind, KeyID: key.ID},
			&river.InsertOpts{ScheduledAt: s.now().Add(keyExpiry + 5*time.Minute)})
	}
	return key.Key, nil
}

// SweepKey revokes a node key that is still recorded (the node never came online
// with it) and flags the node.
func (s *Service) SweepKey(ctx context.Context, a KeySweepArgs) error {
	var ws uuid.UUID
	switch a.NodeKind {
	case agentprotocol.MeshKindServer:
		row, err := s.q.GetServerMesh(ctx, a.NodeID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if row.AuthKeyID != a.KeyID {
			return nil // consumed, or replaced by a newer key
		}
		ws = row.WorkspaceID
		if err := s.q.ClearServerMeshKey(ctx, a.NodeID); err != nil {
			return err
		}
		if row.State != "online" {
			_ = s.q.SetServerMeshState(ctx, db.SetServerMeshStateParams{ServerID: a.NodeID, State: "needs_attention", Message: "The node never joined the tailnet with its key. Re-provision it."})
		}
	default:
		row, err := s.q.GetMeshLinkByID(ctx, a.NodeID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if row.AuthKeyID != a.KeyID {
			return nil
		}
		ws = row.WorkspaceID
		if err := s.q.ClearMeshLinkKey(ctx, a.NodeID); err != nil {
			return err
		}
		if row.State != "online" {
			_ = s.q.SetMeshLinkState(ctx, db.SetMeshLinkStateParams{ID: a.NodeID, State: "needs_attention", Message: "The node never joined the tailnet with its key. Re-provision it."})
		}
	}
	if err := s.ts.RevokeNodeKey(ctx, ws, a.KeyID); err != nil {
		s.log.WarnContext(ctx, "could not revoke an unused mesh node key", "node_id", a.NodeID.String(), "err", err)
	}
	s.publish(ws)
	return nil
}

// AddReconcile fills Reconcile.Mesh of a server's hello with the complete
// desired set, so an expired sync_mesh command never leaves the server
// unconverged. It never provisions (no key rides in a reconcile): nodes that need
// a key are provisioned by the sync_mesh command the reconcile job sends.
func (s *Service) AddReconcile(ctx context.Context, serverID uuid.UUID, rec *agentprotocol.Reconcile) error {
	srv, err := s.q.GetServerByID(ctx, serverID)
	if err != nil {
		return fmt.Errorf("get server: %w", err)
	}
	if !SupportsMesh(srv) {
		return nil
	}
	sn, err := s.load(ctx, s.q, srv.WorkspaceID)
	if err != nil {
		return err
	}
	if sn.set.Paused || !sn.haveInteg {
		return nil
	}
	nodes := sn.desiredNodes(serverID, false)
	if len(nodes) == 0 && len(sn.nodes) == 0 && !sn.set.Enabled {
		return nil // the team never used the mesh: leave the agent alone
	}
	rec.Mesh = &agentprotocol.SyncMeshPayload{Nodes: nodes}
	return nil
}
