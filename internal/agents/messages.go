package agents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// onHello persists the agent's facts, applies results the agent still held,
// sends the desired state and schedules redelivery of unacknowledged commands.
func (h *Hub) onHello(ctx context.Context, c *agentConn, hello agentprotocol.Hello) {
	facts, _ := json.Marshal(hello.Facts)
	ver := hello.AgentVersion
	pv := int32(hello.ProtocolVersion)
	// The hello flips the server online: remember what it was, for the "back online" alert.
	var before *db.Server
	if s, err := h.q.GetServerByID(ctx, c.serverID); err == nil {
		before = &s
	}
	_, err := h.q.UpdateServerHello(ctx, db.UpdateServerHelloParams{
		ID: c.serverID, AgentVersion: &ver, ProtocolVersion: &pv, Facts: facts,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		c.closeWith(agentprotocol.CloseRevoked, "agent revoked")
		return
	}
	if err != nil {
		c.log.ErrorContext(ctx, "persist hello", "err", err)
	}
	h.publishServer(c.serverID)
	if err == nil && before != nil {
		h.notifyOnline(ctx, c.serverID, before.Name, before.Status)
	}

	// Results first: the desired state below must already reflect them.
	for _, res := range hello.UnackedResults {
		h.processResult(ctx, c, res)
	}

	if rh := h.resultHandler(); rh != nil {
		rec, err := rh.DesiredState(ctx, c.serverID)
		if err != nil {
			c.log.ErrorContext(ctx, "compute desired state", "err", err)
		} else {
			rec.Hello = true // the agent takes the frame's TableSeq as its record, lower or higher
			if err := c.sendFrame(agentprotocol.TypeReconcile, "", rec); err != nil {
				c.log.WarnContext(ctx, "send reconcile", "err", err)
			}
		}
	}
	h.runHelloHooks(ctx, c.serverID, hello)
	c.kickDelivery(true)
}

// dispatch handles one frame from the agent. It runs on the reader goroutine,
// so frames are applied in the order the agent sent them.
func (h *Hub) dispatch(ctx context.Context, c *agentConn, f agentprotocol.Frame) {
	switch f.Type {
	case agentprotocol.TypeHeartbeat:
		var hb agentprotocol.Heartbeat
		if f.Into(&hb) == nil {
			h.onHeartbeat(ctx, c, hb)
		}
	case agentprotocol.TypeAck:
		var a agentprotocol.Ack
		if f.Into(&a) != nil {
			return
		}
		if a.CommandID == "" {
			a.CommandID = f.ID
		}
		if id, err := uuid.Parse(a.CommandID); err == nil {
			if err := h.q.MarkAgentCommandAcked(ctx, db.MarkAgentCommandAckedParams{ID: id, ServerID: c.serverID}); err != nil {
				c.log.ErrorContext(ctx, "mark command acked", "command_id", id, "err", err)
			}
		}
	case agentprotocol.TypeResult:
		var res agentprotocol.ResultFrame
		if f.Into(&res) == nil {
			h.processResult(ctx, c, res)
		}
	case agentprotocol.TypeEvent:
		var ev agentprotocol.EventFrame
		if f.Into(&ev) != nil {
			return
		}
		if rh := h.resultHandler(); rh != nil {
			if err := rh.HandleEvent(ctx, c.serverID, ev); err != nil {
				c.log.ErrorContext(ctx, "handle deploy event", "deployment_id", ev.DeploymentID, "step", ev.Step, "err", err)
			}
		}
	case agentprotocol.TypeObserved:
		var obs agentprotocol.Observed
		if f.Into(&obs) != nil {
			return
		}
		if rh := h.resultHandler(); rh != nil {
			if err := rh.HandleObserved(ctx, c.serverID, obs); err != nil {
				c.log.ErrorContext(ctx, "handle observed", "err", err)
			}
		}
	case agentprotocol.TypeStats:
		var st agentprotocol.Stats
		if f.Into(&st) == nil {
			h.onStats(ctx, c, st)
		}
	case agentprotocol.TypeTraffic:
		var tf agentprotocol.Traffic
		if f.Into(&tf) == nil {
			h.onTraffic(ctx, c, tf)
		}
	case agentprotocol.TypeProbes:
		var pf agentprotocol.Probes
		if f.Into(&pf) == nil {
			h.onProbes(ctx, c, pf)
		}
	case agentprotocol.TypeSourceScan:
		var sc agentprotocol.SourceScan
		if f.Into(&sc) == nil {
			h.onSourceScan(ctx, c, sc)
		}
	case agentprotocol.TypeLogStatus:
		var ls agentprotocol.LogStatus
		if f.Into(&ls) == nil {
			h.onLogStatus(ctx, c, ls)
		}
	case agentprotocol.TypeLog:
		var lf agentprotocol.LogFrame
		if f.Into(&lf) == nil {
			h.onLog(ctx, c, lf)
		}
	case agentprotocol.TypeLogEnd:
		var le agentprotocol.LogEnd
		if f.Into(&le) == nil {
			h.onLogEnd(ctx, c, le)
		}
	case agentprotocol.TypeTunnelOpen:
		var o agentprotocol.TunnelOpen
		if f.Into(&o) == nil {
			h.onTunnelOpen(c, o)
		}
	case agentprotocol.TypeTunnelAck, agentprotocol.TypeTunnelClose:
		h.tunnelControl(c, f)
	default:
		if agentprotocol.IsStreamType(f.Type) {
			h.routeStream(c, f)
			return
		}
		c.log.DebugContext(ctx, "ignoring frame from agent", "type", f.Type)
	}
}

func clampInt64(v uint64) int64 {
	if v > 1<<62 {
		return 1 << 62
	}
	return int64(v)
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func (h *Hub) onHeartbeat(ctx context.Context, c *agentConn, hb agentprotocol.Heartbeat) {
	eps := hb.Endpoints
	c.endpoints.Store(&eps)
	prev, err := h.q.RecordServerHeartbeat(ctx, db.RecordServerHeartbeatParams{
		ServerID: c.serverID, ProxyStatus: hb.Proxy.Status, ProxyError: hb.Proxy.Error, DockerVersion: hb.DockerVersion,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		c.closeWith(agentprotocol.CloseRevoked, "agent revoked")
		return
	}
	if err != nil {
		c.log.ErrorContext(ctx, "record heartbeat", "err", err)
		return
	}
	if err := h.q.InsertServerMetricSample(ctx, db.InsertServerMetricSampleParams{
		ServerID: c.serverID, CpuPercent: hb.CPUPercent,
		MemBytes: clampInt64(hb.MemUsed), MemLimitBytes: clampInt64(hb.MemTotal),
		DiskUsedBytes: clampInt64(hb.DiskUsed), DiskTotalBytes: clampInt64(hb.DiskTotal),
	}); err != nil {
		c.log.ErrorContext(ctx, "insert server metric sample", "err", err)
	}
	h.evalDiskAlert(ctx, c, prev, hb)
	h.recordTailnet(ctx, c, hb)
	h.recordTunnel(ctx, c, hb)
	h.recordAcme(ctx, c, hb)
	h.recordProxyPorts(ctx, c, hb)
	h.recordGeo(c, hb)
	h.recordMesh(ctx, c, hb)
	if prev.PrevStatus != "online" || deref(prev.PrevProxyStatus) != hb.Proxy.Status || deref(prev.PrevProxyError) != hb.Proxy.Error {
		h.publishServer(c.serverID)
		h.notifyOnline(ctx, c.serverID, prev.Name, prev.PrevStatus)
	}
}

func (h *Hub) onStats(ctx context.Context, c *agentConn, st agentprotocol.Stats) {
	// Every replica of the frame shares one sample instant so replicas can be
	// summed per instant when the series is read.
	ts := h.now()
	for _, cs := range st.Containers {
		sid, err := uuid.Parse(cs.ServiceID)
		if err != nil {
			continue
		}
		if err := h.q.InsertServiceMetricSample(ctx, db.InsertServiceMetricSampleParams{
			ServerID: &c.serverID, ServiceID: sid, Ts: ts, Replica: int32(max(cs.Replica, 0)), Process: cs.Process, CpuPercent: cs.CPUPercent,
			MemBytes: clampInt64(cs.MemBytes), MemLimitBytes: clampInt64(cs.MemLimit),
		}); err != nil {
			c.log.ErrorContext(ctx, "insert service metric sample", "service_id", sid, "process", cs.Process, "replica", cs.Replica, "err", err)
			return
		}
	}
}

// processResult records a command result and applies it to the domain. The
// agent is told to forget the result (result_ack) only after both succeeded; a
// failure leaves the agent holding it, and it is redelivered on reconnect.
// Duplicates are expected (the agent resends unacked results), so the domain's
// HandleResult must be idempotent.
func (h *Hub) processResult(ctx context.Context, c *agentConn, res agentprotocol.ResultFrame) {
	id, err := uuid.Parse(res.CommandID)
	if err != nil {
		return
	}
	cmd, err := h.q.GetAgentCommand(ctx, id)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		// Ephemeral commands (log subscriptions) are not persisted.
		h.onEphemeralResult(c, id, res)
		_ = c.sendFrame(agentprotocol.TypeResultAck, "", agentprotocol.ResultAck{CommandID: res.CommandID})
		return
	case err != nil:
		c.log.ErrorContext(ctx, "load command for result", "command_id", id, "err", err)
		return
	case cmd.ServerID != c.serverID:
		c.log.WarnContext(ctx, "result for a command of another server ignored", "command_id", id)
		return
	}

	if cmd.Status != "succeeded" && cmd.Status != "failed" {
		status := "failed"
		if res.Outcome == agentprotocol.OutcomeSucceeded {
			status = "succeeded"
		}
		stored, _ := json.Marshal(res)
		if _, err := h.q.FinishAgentCommand(ctx, db.FinishAgentCommandParams{
			ID: id, ServerID: c.serverID, Status: status, Result: stored, Error: res.Error,
		}); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			c.log.ErrorContext(ctx, "finish command", "command_id", id, "err", err)
			return
		}
	}
	if rh := h.resultHandler(); rh != nil {
		if err := rh.HandleResult(ctx, c.serverID, id, cmd.DeploymentID, res); err != nil {
			c.log.ErrorContext(ctx, "handle command result", "command_id", id, "kind", cmd.Kind, "err", err)
			return
		}
	}
	_ = c.sendFrame(agentprotocol.TypeResultAck, "", agentprotocol.ResultAck{CommandID: res.CommandID})
}

// deliver sends the server's undelivered commands, oldest first.
func (h *Hub) deliver(ctx context.Context, c *agentConn, includeSent bool) {
	rows, err := h.q.ListDeliverableAgentCommands(ctx, db.ListDeliverableAgentCommandsParams{ServerID: c.serverID, IncludeSent: includeSent})
	if err != nil {
		if ctx.Err() == nil {
			c.log.Error("list deliverable commands", "err", err)
		}
		return
	}
	for _, row := range rows {
		if c.closed() {
			return
		}
		if err := h.sendCommand(ctx, c, row); err != nil {
			if errors.Is(err, errConnClosed) {
				return
			}
			c.log.ErrorContext(ctx, "send command", "command_id", row.ID, "kind", row.Kind, "err", err)
		}
	}
}

// sendCommand attaches the send-time secrets (never persisted) and queues the
// command frame. A command whose secrets cannot be resolved is failed instead
// of being sent without them.
func (h *Hub) sendCommand(ctx context.Context, c *agentConn, row db.AgentCommand) error {
	var secrets map[string]string
	if src := h.secretSource(); src != nil {
		var err error
		secrets, err = src.SecretsForCommand(ctx, row.ID, row.Kind, row.Payload)
		if err != nil {
			msg := fmt.Sprintf("Not delivered: could not resolve the command's secrets: %v. No changes were made.", err)
			failed, ferr := h.q.FailUndeliveredAgentCommand(ctx, db.FailUndeliveredAgentCommandParams{ID: row.ID, Error: msg})
			if ferr == nil {
				h.reportUndelivered(ctx, []UndeliveredCommand{{ID: failed.ID, ServerID: failed.ServerID, DeploymentID: failed.DeploymentID}}, msg)
			}
			return fmt.Errorf("resolve secrets: %w", err)
		}
	}
	frame := agentprotocol.Command{
		ID: row.ID.String(), IdempotencyKey: row.IdempotencyKey, Kind: row.Kind,
		Payload: row.Payload, Secrets: secrets, ExpiresAt: row.ExpiresAt,
	}
	if err := c.sendFrame(agentprotocol.TypeCommand, row.ID.String(), frame); err != nil {
		return err
	}
	if err := h.q.MarkAgentCommandSent(ctx, row.ID); err != nil {
		return fmt.Errorf("mark sent: %w", err)
	}
	return nil
}

// onTraffic hands a traffic frame to the analytics sink and acknowledges it
// once the buckets are stored. A sink failure withholds the ack so the agent
// resends.
func (h *Hub) onTraffic(ctx context.Context, c *agentConn, t agentprotocol.Traffic) {
	if sink := h.trafficSink(); sink != nil {
		if err := sink.IngestTraffic(ctx, c.serverID, t); err != nil {
			c.log.ErrorContext(ctx, "ingest traffic", "seq", t.Seq, "buckets", len(t.Buckets), "err", err)
			return
		}
	}
	_ = c.sendFrame(agentprotocol.TypeTrafficAck, "", agentprotocol.TrafficAck{Seq: t.Seq})
}

// onProbes hands a probes frame to the uptime sink and acknowledges it once
// the results are stored. A sink failure withholds the ack so the agent
// resends.
func (h *Hub) onProbes(ctx context.Context, c *agentConn, p agentprotocol.Probes) {
	if sink := h.probeSink(); sink != nil {
		if err := sink.IngestProbes(ctx, c.serverID, p); err != nil {
			c.log.ErrorContext(ctx, "ingest probes", "seq", p.Seq, "results", len(p.Results), "err", err)
			return
		}
	}
	_ = c.sendFrame(agentprotocol.TypeProbesAck, "", agentprotocol.ProbesAck{Seq: p.Seq})
}

// diskAlertClearMargin is the hysteresis below the threshold before an active
// disk alert clears, so a disk hovering at the threshold does not flap.
const diskAlertClearMargin = 2

// DiskAlertAction is the audit action recorded once per threshold crossing.
const DiskAlertAction = "server.disk_threshold_exceeded"

// evalDiskAlert compares the root filesystem usage of a heartbeat with the
// server's threshold. Crossing it upward sets disk_alert_active, writes one
// system audit event and publishes server.updated; falling back clears the
// flag (and publishes) without an audit entry.
func (h *Hub) evalDiskAlert(ctx context.Context, c *agentConn, prev db.RecordServerHeartbeatRow, hb agentprotocol.Heartbeat) {
	if hb.DiskTotal == 0 {
		return
	}
	pct := float64(hb.DiskUsed) / float64(hb.DiskTotal) * 100
	threshold := float64(prev.DiskAlertThresholdPercent)
	want := prev.DiskAlertActive
	switch {
	case pct >= threshold:
		want = true
	case pct < threshold-diskAlertClearMargin:
		want = false
	}
	if want == prev.DiskAlertActive {
		return
	}
	var crossed bool
	err := db.InTx(ctx, h.pool, func(tx pgx.Tx) error {
		row, err := db.New(tx).SetServerDiskAlert(ctx, db.SetServerDiskAlertParams{ID: c.serverID, Active: want})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil // another heartbeat already flipped it
		}
		if err != nil {
			return err
		}
		crossed = want
		if !want {
			return nil
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: DiskAlertAction, ResourceType: "server", ResourceID: c.serverID.String(), WorkspaceID: row.WorkspaceID,
			Metadata: map[string]any{
				"name": row.Name, "thresholdPercent": row.DiskAlertThresholdPercent,
				"usedPercent": int(pct + 0.5), "diskUsedBytes": hb.DiskUsed, "diskTotalBytes": hb.DiskTotal,
			},
		})
	})
	if err != nil {
		c.log.ErrorContext(ctx, "evaluate disk alert", "err", err)
		return
	}
	h.publishServer(c.serverID)
	if crossed {
		h.notifyDiskAlert(ctx, c.serverID, int(pct+0.5), prev.DiskAlertThresholdPercent, hb.DiskUsed, hb.DiskTotal)
	}
}
