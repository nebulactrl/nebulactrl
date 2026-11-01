package databases

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/databases/engines"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/deployments"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// replicationSecrets attaches the send-time secrets of the replication
// commands: the replication role's password for repl_prepare (the new password)
// and for the clone. The password is the replica's POSTGRES_REPLICATION_PASSWORD
// variable; it never rides in a payload.
func (s *Service) replicationSecrets(ctx context.Context, kind string, payload json.RawMessage, out map[string]string) error {
	switch kind {
	case agentprotocol.KindDBReplicaClone:
		var p agentprotocol.ReplicaClonePayload
		if err := json.Unmarshal(payload, &p); err != nil {
			return fmt.Errorf("decode clone payload: %w", err)
		}
		id, err := uuid.Parse(p.ServiceID)
		if err != nil {
			return fmt.Errorf("clone service id: %w", err)
		}
		pw, err := s.replicationPassword(ctx, id)
		if err != nil {
			return err
		}
		out[agentprotocol.SecretReplPassword] = pw
	case agentprotocol.KindDBAdmin:
		var p agentprotocol.DBAdminPayload
		if err := json.Unmarshal(payload, &p); err != nil {
			return fmt.Errorf("decode db_admin payload: %w", err)
		}
		if p.Action != agentprotocol.DBActionReplPrepare || p.Repl == nil {
			return nil
		}
		primary, err := uuid.Parse(p.ServiceID)
		if err != nil {
			return fmt.Errorf("db_admin service id: %w", err)
		}
		rows, err := s.q.ListDatabaseReplicasOfPrimary(ctx, primary)
		if err != nil {
			return err
		}
		for _, r := range rows {
			if r.ReplUser != p.Repl.Role {
				continue
			}
			pw, err := s.replicationPassword(ctx, r.ServiceID)
			if err != nil {
				return err
			}
			out[agentprotocol.SecretDBAdminNewPassword] = pw
			return nil
		}
		return fmt.Errorf("no replica uses the role %q", p.Repl.Role)
	}
	return nil
}

// replicationPassword reads a replica's replication password.
func (s *Service) replicationPassword(ctx context.Context, replicaID uuid.UUID) (string, error) {
	sc, err := services.LoadScopeInternal(ctx, s.q, replicaID)
	if err != nil {
		return "", fmt.Errorf("load replica: %w", err)
	}
	vars, err := s.resolved(ctx, s.q, sc)
	if err != nil {
		return "", fmt.Errorf("resolve replica variables: %w", err)
	}
	pw := vars[engines.ReplicationPasswordKey]
	if pw == "" {
		return "", fmt.Errorf("the replica has no %s", engines.ReplicationPasswordKey)
	}
	return pw, nil
}

// kickForCommand nudges the saga a finished command belongs to, so the next step
// does not wait for the job's snooze. The idempotency key names the saga:
// "<action>:<replicaId>:<cycle>" or "promo:<promotionId>:<step>".
func (s *Service) kickForCommand(ctx context.Context, cmd db.AgentCommand) {
	parts := strings.Split(cmd.IdempotencyKey, ":")
	if len(parts) < 3 {
		return
	}
	id, err := uuid.Parse(parts[1])
	if err != nil {
		return
	}
	switch {
	case parts[0] == "promo":
		if s.rep.Jobs != nil {
			if err := s.rep.Jobs.Insert(ctx, PromotionAdvanceArgs{PromotionID: id}, uniqueOpts()); err != nil {
				s.logger.WarnContext(ctx, "kick promotion", "promotion_id", id, "err", err)
			}
		}
	case strings.HasPrefix(parts[0], "repl_"):
		if s.rep.Jobs != nil {
			if err := s.rep.Jobs.Insert(ctx, ReplicaAdvanceArgs{ServiceID: id}, uniqueOpts()); err != nil {
				s.logger.WarnContext(ctx, "kick replica", "service_id", id, "err", err)
			}
		}
	}
}

// MaxObservedSkew is how far an agent's CheckedAt may be from the control
// plane's clock before the report is stamped with the control plane's time.
// The promotion wait decides from these stamps, so an agent cannot choose them.
const MaxObservedSkew = time.Minute

// HandleObserved ingests the replication status that rides in the observed
// frame of a server. It accepts a status only for a database the control plane
// placed on that server (the ones AnnotateDesired asked the agent to poll): a
// server cannot report the replication state of another server's databases,
// whose IDs are not secrets.
func (s *Service) HandleObserved(ctx context.Context, serverID uuid.UUID, obs agentprotocol.Observed) error {
	var firstErr error
	var replicas, primaries map[uuid.UUID]bool // loaded on the first replication report
	for _, o := range obs.Services {
		if o.Replication == nil {
			continue
		}
		id, err := uuid.Parse(o.ServiceID)
		if err != nil {
			continue
		}
		if replicas == nil {
			if replicas, primaries, err = s.placedOn(ctx, serverID); err != nil {
				return err
			}
		}
		if !replicas[id] {
			if _, isReplica, err := s.replicaRow(ctx, s.q, id); err != nil {
				if firstErr == nil {
					firstErr = err
				}
				continue
			} else if isReplica || !primaries[id] {
				s.logger.WarnContext(ctx, "ignoring replication status for a database that does not run on the reporting server",
					"service_id", o.ServiceID, "server_id", serverID.String())
				continue
			}
		}
		st := *o.Replication
		if now := s.now(); st.CheckedAt.Before(now.Add(-MaxObservedSkew)) || st.CheckedAt.After(now.Add(MaxObservedSkew)) {
			st.CheckedAt = time.Time{} // ObserveReplication stamps it with the control plane's time
		}
		if err := s.ObserveReplication(ctx, id, st); err != nil {
			s.logger.ErrorContext(ctx, "observe replication", "service_id", o.ServiceID, "server_id", serverID.String(), "err", err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

// placedOn returns the replica services placed on a server and the primaries
// of replicas that are placed on it.
func (s *Service) placedOn(ctx context.Context, serverID uuid.UUID) (replicas, primaries map[uuid.UUID]bool, err error) {
	reps, err := s.q.ListDatabaseReplicasOnServer(ctx, &serverID)
	if err != nil {
		return nil, nil, fmt.Errorf("list replicas on server: %w", err)
	}
	prims, err := s.q.ListDatabaseReplicasOfPrimariesOnServer(ctx, &serverID)
	if err != nil {
		return nil, nil, fmt.Errorf("list primaries on server: %w", err)
	}
	replicas, primaries = make(map[uuid.UUID]bool, len(reps)), make(map[uuid.UUID]bool, len(prims))
	for _, r := range reps {
		replicas[r.ServiceID] = true
	}
	for _, r := range prims {
		primaries[r.PrimaryServiceID] = true
	}
	return replicas, primaries, nil
}

// side is one observation's contribution to a replica's row.
type side struct {
	receiveLSN, replayLSN *string
	lagBytes              *int64
	replayLagMs           *int64
	walStatus             *string
	connected             bool
	// replicaSide marks an observation by the replica itself: its byte lag is a
	// fallback for the primary's.
	replicaSide bool
	checkedAt   time.Time
}

// ObserveReplication applies the status a database container reported: a replica
// reports its walreceiver, a primary its slots and standbys. Either side moves
// the replica between streaming, lagging, broken and needs_rebuild; lag numbers
// alone never restage anything.
func (s *Service) ObserveReplication(ctx context.Context, serviceID uuid.UUID, st agentprotocol.ReplicationStatus) error {
	if st.Error != "" {
		return nil // the agent could not read the database; the last state stands
	}
	at := st.CheckedAt
	if at.IsZero() {
		at = s.now()
	}
	if r, ok, err := s.replicaRow(ctx, s.q, serviceID); err != nil {
		return err
	} else if ok {
		sd := side{
			connected:   st.ReceiverStatus == "streaming",
			replicaSide: true,
			checkedAt:   at,
		}
		if st.ReceiveLSN != "" {
			sd.receiveLSN = &st.ReceiveLSN
		}
		if st.ReplayLSN != "" {
			sd.replayLSN = &st.ReplayLSN
		}
		lag := st.ReplayLagBytes
		sd.lagBytes = &lag
		return s.applyObservation(ctx, r.ServiceID, sd)
	}
	rows, err := s.q.ListDatabaseReplicasOfPrimary(ctx, serviceID)
	if err != nil {
		return err
	}
	for _, r := range rows {
		sd := side{checkedAt: at}
		for _, sl := range st.Slots {
			if sl.Name == r.SlotName && sl.WALStatus != "" {
				ws := sl.WALStatus
				sd.walStatus = &ws
			}
		}
		for _, sb := range st.Standbys {
			if sb.ApplicationName != r.SlotName {
				continue
			}
			sd.connected = sb.State == "streaming"
			lag := sb.LagBytes
			sd.lagBytes = &lag
			sd.replayLagMs = sb.ReplayLagMs
			if lag == 0 && sd.replayLagMs == nil {
				zero := int64(0) // an idle standby reports no replay lag
				sd.replayLagMs = &zero
			}
		}
		if err := s.applyObservation(ctx, r.ServiceID, sd); err != nil {
			return err
		}
	}
	return nil
}

// applyObservation merges one side into a replica's row and derives its state,
// in one transaction.
func (s *Service) applyObservation(ctx context.Context, replicaID uuid.UUID, sd side) error {
	var before, after db.DatabaseReplica
	var staged []db.Service
	err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		r, err := q.LockDatabaseReplica(ctx, replicaID)
		if err != nil {
			return err
		}
		before = r
		switch r.State {
		case ReplicaPromoting:
			// The promotion saga owns the state, but it decides when the replica has
			// replayed the old primary's final position from the replica's own numbers:
			// record those (and nothing else) so the wait can finish. A primary-side
			// observation must not refresh observed_at over a stale replay position.
			if !sd.replicaSide || sd.replayLSN == nil {
				return nil
			}
			return q.ObserveDatabaseReplica(ctx, db.ObserveDatabaseReplicaParams{
				ReceiveLsn: sd.receiveLSN, ReplayLsn: sd.replayLSN, ObservedAt: &sd.checkedAt, ServiceID: replicaID,
			})
		case ReplicaDeploying, ReplicaStreaming, ReplicaLagging, ReplicaBroken, ReplicaNeedsRebuild:
		default:
			return nil // the saga owns the replica
		}
		lag, lagMs := r.LagBytes, r.ReplayLagMs
		if sd.lagBytes != nil && (!sd.replicaSide || r.LagBytes == nil || r.ObservedAt == nil || sd.checkedAt.Sub(*r.ObservedAt) > StaleAfter) {
			lag = sd.lagBytes
		}
		if sd.replayLagMs != nil {
			lagMs = sd.replayLagMs
		}
		over := (lag != nil && *lag > LagBytesThreshold) || (lagMs != nil && time.Duration(*lagMs)*time.Millisecond > LagTimeThreshold)
		lost := sd.walStatus != nil && *sd.walStatus == "lost"
		healthy := sd.connected && !over && !lost
		p := db.ObserveDatabaseReplicaParams{
			ReceiveLsn: sd.receiveLSN, ReplayLsn: sd.replayLSN, LagBytes: sd.lagBytes, ReplayLagMs: sd.replayLagMs, WalStatus: sd.walStatus,
			ObservedAt: &sd.checkedAt, Healthy: healthy, ServiceID: replicaID,
		}
		if sd.replicaSide && sd.lagBytes != nil && lag != sd.lagBytes {
			p.LagBytes = nil // the primary's number stands
		}
		if err := q.ObserveDatabaseReplica(ctx, p); err != nil {
			return err
		}
		lastGood := r.CreatedAt
		if r.LastStreamingAt != nil {
			lastGood = *r.LastStreamingAt
		}
		if healthy {
			lastGood = sd.checkedAt
		}
		gap := sd.checkedAt.Sub(lastGood)
		state, msg := r.State, r.Message
		switch {
		case lost:
			state, msg = ReplicaNeedsRebuild, "The primary dropped the replica's replication slot (WAL was removed): rebuild the replica."
		case r.State == ReplicaNeedsRebuild:
			// Only a rebuild leaves this state.
		case sd.connected && !over:
			state, msg = ReplicaStreaming, ""
		case sd.connected && over && gap > LaggingAfter:
			state, msg = ReplicaLagging, fmt.Sprintf("The replica is %s behind the primary.", humanBytes(valueI64(lag)))
		case sd.connected && over:
			if r.State == ReplicaDeploying || r.State == ReplicaBroken {
				state, msg = ReplicaStreaming, ""
			}
		case !sd.connected && r.State != ReplicaDeploying && gap > s.brokenAfter:
			state, msg = ReplicaBroken, "The replica is not streaming from the primary."
		}
		_, cycle := parseStep(r.Step)
		step := r.Step
		if state != r.State && state == ReplicaStreaming && r.State == ReplicaDeploying {
			step = mkStep(stepDone, cycle)
		}
		if state != r.State || msg != r.Message {
			if err := q.SetDatabaseReplicaState(ctx, db.SetDatabaseReplicaStateParams{ServiceID: replicaID, State: state, Message: msg, Step: step}); err != nil {
				return err
			}
		}
		after = r
		after.State, after.Message, after.Step = state, msg, step
		if healthClass(r.State) != healthClass(state) {
			members, err := s.groupServices(ctx, q, r.PrimaryServiceID)
			if err != nil {
				return err
			}
			if staged, err = s.stageGroupReferrers(ctx, q, r.EnvironmentID, members); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("apply replica observation: %w", err)
	}
	if after.ServiceID != uuid.Nil && (after.State != before.State) {
		s.afterTransition(ctx, before, after.State, after.Message, staged)
	}
	return nil
}

func valueI64(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

// AnnotateDesired adds the replication part of a server's desired state to the
// reconcile frame: which databases the agent polls, and which services are
// fenced (must never run as a primary again). It runs on every hello and resync.
func (s *Service) AnnotateDesired(ctx context.Context, serverID uuid.UUID, rec *agentprotocol.Reconcile) error {
	idx := make(map[string]int, len(rec.Services))
	for i, ds := range rec.Services {
		idx[ds.ServiceID] = i
	}
	superuser := func(id uuid.UUID) string {
		sc, err := services.LoadScopeInternal(ctx, s.q, id)
		if err != nil {
			return ""
		}
		if sp := sc.Config().Database; sp != nil {
			return sp.Normalize().Username
		}
		return ""
	}
	reps, err := s.q.ListDatabaseReplicasOnServer(ctx, &serverID)
	if err != nil {
		return err
	}
	for _, r := range reps {
		i, ok := idx[r.ServiceID.String()]
		if !ok || r.State == ReplicaRemoving {
			continue
		}
		rec.Services[i].Replication = &agentprotocol.ReplicationDesired{
			Role: agentprotocol.ReplRoleReplica, User: superuser(r.ServiceID), Slot: r.SlotName,
		}
	}
	prims, err := s.q.ListDatabaseReplicasOfPrimariesOnServer(ctx, &serverID)
	if err != nil {
		return err
	}
	seen := map[uuid.UUID]bool{}
	for _, r := range prims {
		if seen[r.PrimaryServiceID] {
			continue
		}
		seen[r.PrimaryServiceID] = true
		if i, ok := idx[r.PrimaryServiceID.String()]; ok {
			rec.Services[i].Replication = &agentprotocol.ReplicationDesired{
				Role: agentprotocol.ReplRolePrimary, User: superuser(r.PrimaryServiceID),
			}
		}
	}
	fences, err := s.q.ListDatabaseFencesOnServer(ctx, &serverID)
	if err != nil {
		return err
	}
	for _, f := range fences {
		if i, ok := idx[f.ServiceID.String()]; ok {
			rec.Services[i].Fenced = true
			rec.Services[i].Replication = nil
		}
	}
	return nil
}

// DatabaseRuntime implements deployments.DatabaseHook: the replication part of a
// database service's deploy payload (nil for a standalone database).
func (s *Service) DatabaseRuntime(ctx context.Context, q *db.Queries, sc services.Scope) (*agentprotocol.DatabaseRuntime, error) {
	spec := sc.Config().Database
	if sc.Service.Kind != services.KindDatabase || spec == nil || spec.Engine != engines.Postgres {
		return nil, nil
	}
	user := spec.Normalize().Username
	if r, ok, err := s.replicaRow(ctx, q, sc.Service.ID); err != nil {
		return nil, err
	} else if ok {
		return &agentprotocol.DatabaseRuntime{Replication: &agentprotocol.ReplicationDesired{
			Role: agentprotocol.ReplRoleReplica, User: user, Slot: r.SlotName,
		}}, nil
	}
	n, err := q.CountDatabaseReplicasOfPrimary(ctx, sc.Service.ID)
	if err != nil {
		return nil, err
	}
	if n > 0 {
		return &agentprotocol.DatabaseRuntime{Replication: &agentprotocol.ReplicationDesired{
			Role: agentprotocol.ReplRolePrimary, User: user,
		}}, nil
	}
	return nil, nil
}

// PlanCheck implements deployments.DatabaseHook: a fenced old primary cannot be
// deployed (it would run as a second writer), and a replica's deploy is
// explained.
func (s *Service) PlanCheck(ctx context.Context, q *db.Queries, sc services.Scope, cfg services.Config) (warnings []string, blocking []deployments.PlanBlocking, err error) {
	if sc.Service.Kind != services.KindDatabase || cfg.Database == nil {
		return nil, nil, nil
	}
	if _, err := q.GetDatabaseFence(ctx, sc.Service.ID); err == nil {
		blocking = append(blocking, deployments.PlanBlocking{
			Code: CodeDatabaseFenced, Message: "This database is a fenced old primary and must never run as a primary again. Rebuild it as a replica of the new primary, or delete it.",
		})
	} else if !isNoRows(err) {
		return nil, nil, err
	}
	if r, ok, err := s.replicaRow(ctx, q, sc.Service.ID); err != nil {
		return nil, nil, err
	} else if ok && cfg.Database.Replica != nil {
		if r.State == ReplicaNeedsRebuild || r.State == ReplicaFailed {
			warnings = append(warnings, "This replica is "+strings.ReplaceAll(r.State, "_", " ")+"; deploying it will not repair it. Rebuild it from the database panel.")
		}
		if psc, err := services.LoadScopeInternal(ctx, q, r.PrimaryServiceID); err == nil {
			if pspec := psc.Config().Database; pspec != nil && pspec.Version != cfg.Database.Version {
				warnings = append(warnings, fmt.Sprintf("The replica runs PostgreSQL %s and its primary %s: keep them on the same version (redeploy replicas first when upgrading).", cfg.Database.Version, pspec.Version))
			}
		}
	} else if n, err := q.CountDatabaseReplicasOfPrimary(ctx, sc.Service.ID); err != nil {
		return nil, nil, err
	} else if n > 0 {
		warnings = append(warnings, fmt.Sprintf("This database has %d read replica(s). Redeploying restarts the primary: replicas reconnect on their own.", n))
	}
	return warnings, blocking, nil
}

// ServerRevoked is the revoke hook (servers.AddRevokeHook): the replicas that ran
// on the server are failed, and the replicas whose primary ran there are broken
// with the hint that Promote is available. Nothing is promoted automatically.
func (s *Service) ServerRevoked(ctx context.Context, _ uuid.UUID, serverID uuid.UUID) {
	onServer, err := s.q.ListDatabaseReplicasOnServer(ctx, &serverID)
	if err != nil {
		s.logger.ErrorContext(ctx, "list replicas of revoked server", "server_id", serverID, "err", err)
		return
	}
	for _, r := range onServer {
		if r.State == ReplicaRemoving || r.State == ReplicaFailed {
			continue
		}
		_, cycle := parseStep(r.Step)
		if err := s.transition(ctx, r, ReplicaFailed, "The replica's server was revoked.", mkStep(stepDone, cycle)); err != nil {
			s.logger.ErrorContext(ctx, "fail replica of revoked server", "service_id", r.ServiceID, "err", err)
		}
	}
	ofPrimaries, err := s.q.ListDatabaseReplicasOfPrimariesOnServer(ctx, &serverID)
	if err != nil {
		s.logger.ErrorContext(ctx, "list replicas of revoked primary", "server_id", serverID, "err", err)
		return
	}
	for _, r := range ofPrimaries {
		switch r.State {
		case ReplicaStreaming, ReplicaLagging, ReplicaDeploying, ReplicaPreparing, ReplicaCloning, ReplicaPending:
			if err := s.transition(ctx, r, ReplicaBroken, "Primary lost: its server was revoked. Promote is available.", r.Step); err != nil {
				s.logger.ErrorContext(ctx, "break replica of revoked primary", "service_id", r.ServiceID, "err", err)
			}
		}
	}
}
