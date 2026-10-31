package proxyserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/dns01"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// HandleResult applies the result of a proxy_configure command: on success the
// certificates the command carried become applied (removed ones are deleted),
// the upstream policy and tailnet settings are copied into their applied
// columns, the tailnet device id and status are stored and a replaced or
// removed device is queued for deletion. A failure changes nothing but the
// observed tailnet status. Commands of other kinds are ignored. It is
// idempotent: the agent may redeliver a result.
func (s *Service) HandleResult(ctx context.Context, serverID, commandID uuid.UUID, res agentprotocol.ResultFrame) error {
	cmd, err := s.q.GetAgentCommand(ctx, commandID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("load command: %w", err)
	}
	if cmd.Kind != agentprotocol.KindProxyConfigure || cmd.ServerID != serverID {
		return nil
	}
	var payload agentprotocol.ProxyConfigurePayload
	if err := json.Unmarshal(cmd.Payload, &payload); err != nil {
		s.log.WarnContext(ctx, "proxy_configure payload unreadable", "command_id", commandID, "err", err)
		return nil
	}
	var result agentprotocol.ProxyConfigureResult
	if len(res.Data) > 0 {
		_ = json.Unmarshal(res.Data, &result)
	}

	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		srv, err := q.LoadServerRow(ctx, db.LoadServerRowParams{ID: serverID})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if _, err := q.LockServerForProxy(ctx, db.LockServerForProxyParams{ID: serverID, WorkspaceID: srv.WorkspaceID}); err != nil {
			return err
		}

		if res.Outcome != agentprotocol.OutcomeSucceeded {
			s.log.WarnContext(ctx, "proxy_configure failed", "server_id", serverID, "command_id", commandID, "err", firstNonEmpty(res.Error, result.Message))
			if result.Tailnet != nil {
				return s.storeTailnetStatus(ctx, q, serverID, result.Tailnet)
			}
			return nil
		}

		// A redelivered result of an older command must not undo a newer success.
		if last, err := q.LastProxyConfigureCommand(ctx, serverID); err == nil && last.ID != commandID && last.Status == "succeeded" {
			return nil
		}

		ids := make([]uuid.UUID, 0, len(payload.Proxy.Certificates))
		for _, c := range payload.Proxy.Certificates {
			if id, err := uuid.Parse(c.ID); err == nil {
				ids = append(ids, id)
			}
		}
		if err := q.MarkCertificatesApplied(ctx, db.MarkCertificatesAppliedParams{ServerID: serverID, Ids: ids}); err != nil {
			return err
		}
		if err := q.PurgeRemovedCertificates(ctx, db.PurgeRemovedCertificatesParams{ServerID: serverID, KeepIds: ids}); err != nil {
			return err
		}
		for _, cs := range result.Certificates {
			id, err := uuid.Parse(cs.ID)
			if err != nil || !containsUUID(ids, id) {
				continue
			}
			status, msg := "active", ""
			if !cs.Loaded {
				status, msg = "error", firstNonEmpty(cs.Error, "the proxy could not load this certificate")
			}
			if err := q.SetCertificateStatus(ctx, db.SetCertificateStatusParams{ID: id, Status: status, StatusMessage: msg}); err != nil {
				return err
			}
		}

		spec := payload.Proxy.Tailnet
		mode, hostname, magic := agentprotocol.TailnetOff, "", false
		provisioned := false
		if spec != nil && spec.Mode != "" {
			mode, hostname, magic, provisioned = spec.Mode, spec.Hostname, spec.MagicDNS, spec.Provision
		}
		newDevice := ""
		if result.Tailnet != nil && mode == agentprotocol.TailnetManaged {
			newDevice = result.Tailnet.DeviceID
		}
		clearDevice := mode != agentprotocol.TailnetManaged || (provisioned && newDevice == "")
		oldDevice := derefStr(srv.TailnetDeviceID)
		var hp, dp *string
		if mode == agentprotocol.TailnetManaged && hostname != "" {
			hp = &hostname
		}
		if newDevice != "" {
			dp = &newDevice
		}
		gen, ok := generationOfKey(cmd.IdempotencyKey)
		if !ok {
			gen = srv.ProxyGeneration
		}
		pol, err := json.Marshal(payload.Proxy.UpstreamPolicy)
		if err != nil {
			return err
		}
		if err := q.ApplyServerProxyResult(ctx, db.ApplyServerProxyResultParams{
			ID: serverID, Generation: gen, UpstreamPolicy: pol, TailnetMode: mode, TailnetHostname: hp, TailnetMagicDns: magic,
			ClearDevice: clearDevice, DeviceID: dp,
			HttpPort: int32Of(&payload.Proxy.HTTPPort), HttpsPort: int32Of(&payload.Proxy.HTTPSPort),
		}); err != nil {
			return err
		}
		// What this deploy applied of DNS-01 (names only, never a value).
		if err := q.SetServerAppliedDNS01(ctx, db.SetServerAppliedDNS01Params{ID: serverID, AppliedDns01: dns01.SnapshotOf(payload.Proxy)}); err != nil {
			return err
		}
		switch {
		case result.Tailnet != nil:
			if err := s.storeTailnetStatus(ctx, q, serverID, result.Tailnet); err != nil {
				return err
			}
		case mode == agentprotocol.TailnetOff:
			if err := q.SetServerTailnetStatus(ctx, db.SetServerTailnetStatusParams{ID: serverID}); err != nil {
				return err
			}
		}
		if oldDevice != "" && oldDevice != newDevice && (mode != agentprotocol.TailnetManaged || provisioned) && s.ts != nil {
			if err := s.ts.EnqueueDeleteDevice(ctx, tx, srv.WorkspaceID, oldDevice); err != nil {
				return fmt.Errorf("queue tailnet device removal: %w", err)
			}
		}
		return audit.Record(ctx, tx, audit.Entry{
			WorkspaceID: srv.WorkspaceID, Action: "server.proxy.applied", ResourceType: "server", ResourceID: serverID.String(),
			Metadata: map[string]any{"generation": gen, "restarted": result.Restarted, "certificates": len(ids), "tailnetMode": mode},
		})
	})
	if err != nil {
		return fmt.Errorf("apply proxy_configure result: %w", err)
	}
	s.publish(serverID)
	return nil
}

func (s *Service) storeTailnetStatus(ctx context.Context, q *db.Queries, serverID uuid.UUID, ts *agentprotocol.TailnetStatus) error {
	raw, err := json.Marshal(ts)
	if err != nil {
		return err
	}
	return q.SetServerTailnetStatus(ctx, db.SetServerTailnetStatusParams{ID: serverID, Status: raw})
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}

func containsUUID(ids []uuid.UUID, id uuid.UUID) bool {
	for _, x := range ids {
		if x == id {
			return true
		}
	}
	return false
}
