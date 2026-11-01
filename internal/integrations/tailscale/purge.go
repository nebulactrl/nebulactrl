package tailscale

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/policy"
)

// Disconnect removes everything NebulaCtrl created on the team's tailnet and
// then the integration: the devices of service sidecars, server mesh nodes,
// mesh links and managed server nodes (including those whose removal is still
// queued, which would find the integration gone), and the auth keys they were
// registered with that were never used. A device or key that is already gone
// is fine; a device that cannot be deleted stops the disconnect, so none is
// left behind with no way to delete it. It returns how many devices it removed.
// A team without an integration has nothing to do.
func (s *Service) Disconnect(ctx context.Context) (int, error) {
	if err := policy.Require(ctx, policy.ScopeAdmin, nil); err != nil {
		return 0, err
	}
	ws := policy.WorkspaceID(ctx)
	if _, err := s.q.GetTailnetIntegration(ctx, ws); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, nil
		}
		return 0, fmt.Errorf("get tailnet integration: %w", err)
	}

	refs, err := s.q.ListTailnetNodeRefs(ctx, ws)
	if err != nil {
		return 0, fmt.Errorf("list tailnet devices: %w", err)
	}
	var nodes []NodeRef
	var keys []string
	for _, r := range refs {
		nodes = append(nodes, NodeRef{DeviceID: r.DeviceID, IPs: r.Ips, Hostname: r.Hostname})
		keys = append(keys, r.AuthKeyID)
	}
	queued, err := s.queuedDeletions(ctx, ws)
	if err != nil {
		return 0, err
	}
	nodes = append(nodes, queued...)

	removed := 0
	var errs []error
	for _, n := range nodes {
		if n.empty() {
			continue
		}
		if err := s.DeleteNode(ctx, ws, n); err != nil {
			errs = append(errs, fmt.Errorf("delete tailnet device %s: %w", firstNonEmpty(n.DeviceID, n.Hostname), err))
			continue
		}
		removed++
	}
	if len(errs) > 0 {
		return removed, errors.Join(errs...)
	}
	for _, k := range keys {
		if k == "" {
			continue
		}
		// An unused key expires by itself; a failure here is not worth keeping
		// the team from being deleted.
		if err := s.RevokeNodeKey(ctx, ws, k); err != nil {
			s.log.WarnContext(ctx, "revoke tailnet auth key", "workspace_id", ws, "err", err)
		}
	}
	if err := s.Delete(ctx); err != nil {
		if ae, ok := apierr.As(err); ok && ae.Status == 404 {
			return removed, nil
		}
		return removed, err
	}
	return removed, nil
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}

// queuedDeletions lists the devices whose removal is queued and has not run:
// the revoke hooks of a server and the delete of a service queue the removal
// of a device and forget its row.
func (s *Service) queuedDeletions(ctx context.Context, ws uuid.UUID) ([]NodeRef, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT args FROM river_job
		  WHERE kind = $1 AND state IN ('available', 'pending', 'retryable', 'scheduled') AND args->>'workspaceId' = $2`,
		TailnetDeleteDeviceArgs{}.Kind(), ws.String())
	if err != nil {
		return nil, fmt.Errorf("list queued tailnet deletions: %w", err)
	}
	defer rows.Close()
	var out []NodeRef
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var a TailnetDeleteDeviceArgs
		if err := json.Unmarshal(raw, &a); err != nil {
			continue
		}
		out = append(out, NodeRef{DeviceID: a.DeviceID, IPs: a.IPs, Hostname: a.Hostname})
	}
	return out, rows.Err()
}
