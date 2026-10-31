package databases

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/databases/engines"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/internal/varexpr"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Manual promote. Promote makes a read replica the new primary of
// its group. It is manual: NebulaCtrl does not detect failures or switch by
// itself. Two modes:
//
//   - switchover: the old primary is reachable. It is frozen (read-only), stopped
//     and fenced, its final WAL position is read from its stopped data directory,
//     the replica replays up to it, then is promoted. No data is lost.
//   - forced: the old primary is unreachable. It is fenced first (persisted, and
//     carried in every reconcile), the replica is promoted as it is. What the
//     replica had not received (the lag) is lost.
//
// The old primary is never unfenced: it can only be rebuilt as a replica of the
// new primary, or deleted.

// Promotion modes.
const (
	ModeSwitchover = "switchover"
	ModeForced     = "forced"
)

// Promotion steps (database_promotions.step).
const (
	pstepFreeze   = "freeze"
	pstepStop     = "stop"
	pstepFinalLSN = "final_lsn"
	pstepWait     = "wait"
	pstepPromote  = "promote"
	pstepRepoint  = "repoint"
	pstepLinks    = "links"
	// Abort path of a switchover that could not complete.
	pstepAbortUnfence  = "abort_unfence"
	pstepAbortRestart  = "abort_restart"
	pstepAbortUnfreeze = "abort_unfreeze"
)

const (
	// catchUpTimeout bounds the wait for the replica to replay the old primary's
	// final position.
	catchUpTimeout = 5 * time.Minute
	// promoteAttempts is how often repl_promote is tried.
	promoteAttempts = 3
)

// ServiceRef names a service in a plan.
type ServiceRef struct {
	ID   uuid.UUID `json:"id"`
	Slug string    `json:"slug"`
}

// PromoteOldPrimary is the primary a promotion replaces.
type PromoteOldPrimary struct {
	ID         uuid.UUID `json:"id"`
	Slug       string    `json:"slug"`
	ServerName string    `json:"serverName,omitempty"`
}

// PromoteConsumer is a service whose variables reference the old primary.
type PromoteConsumer struct {
	ServiceID    uuid.UUID  `json:"serviceId"`
	Slug         string     `json:"slug"`
	ServerID     *uuid.UUID `json:"serverId,omitempty"`
	ServerName   string     `json:"serverName,omitempty"`
	Keys         []string   `json:"keys" doc:"Variables that reference the old primary; they are rewritten as staged changes"`
	DevicesAdded int        `json:"devicesAdded"`
	Warnings     []Problem  `json:"warnings"`
}

// PromotePlan is what promoting a replica does.
type PromotePlan struct {
	Mode            string            `json:"mode" enum:"switchover,forced"`
	LagBytes        *int64            `json:"lagBytes,omitempty"`
	LagObservedAt   *time.Time        `json:"lagObservedAt,omitempty"`
	Replica         ServiceRef        `json:"replica"`
	OldPrimary      PromoteOldPrimary `json:"oldPrimary"`
	Consumers       []PromoteConsumer `json:"consumers"`
	RebuildReplicas []ServiceRef      `json:"rebuildReplicas"`
	SchedulesMoving int               `json:"schedulesMoving"`
	Blockers        []Problem         `json:"blockers"`
	Warnings        []Problem         `json:"warnings"`
	DataLossWarning string            `json:"dataLossWarning,omitempty"`
}

// PromoteInput is a promote request.
type PromoteInput struct {
	DryRun bool
	// Confirm must equal the replica's slug.
	Confirm string
	// AcceptDataLoss is required in forced mode.
	AcceptDataLoss bool
}

// PromoteResult is the response of the promote endpoint.
type PromoteResult struct {
	DryRun      bool        `json:"dryRun"`
	Plan        PromotePlan `json:"plan"`
	PromotionID *uuid.UUID  `json:"promotionId,omitempty"`
	Status      string      `json:"status,omitempty"`
}

// promoteState is a plan with the rows it was computed from.
type promoteState struct {
	plan     PromotePlan
	rsc, psc services.Scope
	row      db.DatabaseReplica
	others   []db.DatabaseReplica
	consumer []db.Service
	keys     map[uuid.UUID][]string
}

func (s *Service) planPromote(ctx context.Context, q *db.Queries, rsc services.Scope) (promoteState, error) {
	st := promoteState{plan: PromotePlan{
		Consumers: []PromoteConsumer{}, RebuildReplicas: []ServiceRef{}, Blockers: []Problem{}, Warnings: []Problem{},
		Replica: ServiceRef{ID: rsc.Service.ID, Slug: rsc.Service.Slug},
	}, rsc: rsc, keys: map[uuid.UUID][]string{}}
	block := func(code, msg string) { st.plan.Blockers = append(st.plan.Blockers, Problem{Code: code, Message: msg}) }
	warn := func(code, msg string) { st.plan.Warnings = append(st.plan.Warnings, Problem{Code: code, Message: msg}) }

	r, ok, err := s.replicaRow(ctx, q, rsc.Service.ID)
	if err != nil {
		return st, err
	}
	if !ok {
		return st, apierr.Conflict("not_a_replica", "only a read replica can be promoted")
	}
	st.row = r
	if st.psc, err = services.LoadScopeInternal(ctx, q, r.PrimaryServiceID); err != nil {
		return st, err
	}
	psc := st.psc
	st.plan.OldPrimary = PromoteOldPrimary{ID: psc.Service.ID, Slug: psc.Service.Slug}
	st.plan.LagBytes, st.plan.LagObservedAt = r.LagBytes, r.ObservedAt

	if busy, err := s.promotionRunningIn(ctx, q, psc.Service.ID); err != nil {
		return st, err
	} else if busy {
		block(CodePromoteInProgress, "a promotion is already running in this replica group")
	}
	switch r.State {
	case ReplicaStreaming, ReplicaLagging, ReplicaBroken:
	default:
		block("replica_not_streaming", fmt.Sprintf("the replica is %s; only a streaming, lagging or broken (primary lost) replica can be promoted", strings.ReplaceAll(r.State, "_", " ")))
	}
	if ok, why := s.serviceRunning(rsc.Service); !ok {
		block("replica_not_running", "the replica must be running: "+why)
	}
	if rsc.Service.ServerID == nil {
		block("replica_agent_offline", "the replica has no server")
	} else if srv, err := s.loadServer(ctx, q, rsc.WorkspaceID, *rsc.Service.ServerID); err != nil {
		return st, fmt.Errorf("load replica server: %w", err)
	} else {
		if srv.Status != "online" {
			block("replica_agent_offline", "the replica's server "+srv.Name+" is "+srv.Status+"; it must be online to promote")
		}
		if !hasCap(srv, agentprotocol.CapReplication) {
			block(CodeAgentTooOld, "the agent on "+srv.Name+" is too old for read replicas; update the agent first")
		}
	}

	// Mode: switchover while the old primary's server is online and its database runs.
	st.plan.Mode = ModeForced
	if psrvID, err := s.currentServer(ctx, q, psc); err == nil {
		if srv, err := s.loadServer(ctx, q, psc.WorkspaceID, psrvID); err == nil {
			st.plan.OldPrimary.ServerName = srv.Name
			if ok, _ := s.serviceRunning(psc.Service); ok && srv.Status == "online" {
				st.plan.Mode = ModeSwitchover
			}
		}
	} else if ae, ok := apierr.As(err); !ok {
		return st, err
	} else if ae.Code != "not_deployed" && ae.Code != "server_unavailable" {
		return st, err
	}

	siblings, err := q.ListDatabaseReplicasOfPrimary(ctx, psc.Service.ID)
	if err != nil {
		return st, err
	}
	exclude := map[uuid.UUID]bool{psc.Service.ID: true}
	for _, o := range siblings {
		exclude[o.ServiceID] = true
		if o.ServiceID == rsc.Service.ID {
			continue
		}
		st.others = append(st.others, o)
		if sc, err := services.LoadScopeInternal(ctx, q, o.ServiceID); err == nil {
			st.plan.RebuildReplicas = append(st.plan.RebuildReplicas, ServiceRef{ID: o.ServiceID, Slug: sc.Service.Slug})
		}
	}
	if st.plan.SchedulesMoving, err = s.schedulesOf(ctx, q, psc.Service.ID); err != nil {
		return st, err
	}

	refs, keys, err := s.referrersOfAny(ctx, q, rsc.EnvironmentID, map[string]bool{psc.Service.Slug: true}, exclude)
	if err != nil {
		return st, err
	}
	st.consumer, st.keys = refs, keys
	for _, c := range refs {
		pc := PromoteConsumer{ServiceID: c.ID, Slug: c.Slug, ServerID: c.ServerID, Keys: keys[c.ID], Warnings: []Problem{}}
		if c.ServerID != nil {
			if srv, err := s.loadServer(ctx, q, rsc.WorkspaceID, *c.ServerID); err == nil {
				pc.ServerName = srv.Name
			}
			if rsc.Service.ServerID != nil && *c.ServerID != *rsc.Service.ServerID {
				if s.rep.Mesh == nil {
					pc.Warnings = append(pc.Warnings, Problem{Code: CodeMeshServerRequired, Message: "the server mesh is not available: " + c.Slug + " cannot reach the new primary"})
				} else {
					lp, err := s.rep.Mesh.PlanLink(ctx, q, rsc.EnvironmentID, *c.ServerID, *rsc.Service.ServerID, agentprotocol.MeshPurposeService)
					if err != nil {
						return st, fmt.Errorf("plan consumer link: %w", err)
					}
					pc.DevicesAdded = lp.DevicesAdded
					for _, b := range lp.Blockers {
						code := b.Code
						if code == "" {
							code = CodeMeshServerRequired
						}
						pc.Warnings = append(pc.Warnings, Problem{Code: code, Message: b.Message + " (" + c.Slug + " cannot reach the new primary until this is fixed)"})
					}
				}
			}
		}
		st.plan.Consumers = append(st.plan.Consumers, pc)
	}

	warn("manual", "Manual. NebulaCtrl does not detect failures or switch automatically.")
	if len(st.plan.RebuildReplicas) > 0 {
		warn("rebuild_others", fmt.Sprintf("%d other replica(s) follow the old primary and must be rebuilt as replicas of the new one.", len(st.plan.RebuildReplicas)))
	}
	if st.plan.Mode == ModeSwitchover {
		warn("old_primary_stopped", "The old primary is made read-only, stopped and fenced. Its data is kept; it can be rebuilt as a replica of the new primary or deleted. There is no unfence.")
	} else {
		lag := "unknown"
		if r.LagBytes != nil {
			lag = humanBytes(*r.LagBytes)
		}
		as := "an unknown time"
		if r.ObservedAt != nil {
			as = r.ObservedAt.UTC().Format(time.RFC3339)
		}
		st.plan.DataLossWarning = fmt.Sprintf("Forced promote: up to the replication lag, which is %s as of %s, will be lost.", lag, as)
		warn("forced", st.plan.DataLossWarning+" If the old primary's host comes back, writes it accepts before the agent fences it are lost; the old primary can only be rebuilt.")
	}
	if r.State == ReplicaBroken && st.plan.Mode == ModeSwitchover {
		warn("replica_broken", "The replica is not streaming. The switchover waits up to 5 minutes for it to replay the old primary's final position and aborts (nothing lost) if it cannot.")
	}
	if len(refs) > 0 {
		warn("consumers_rewritten", fmt.Sprintf("%d service(s) reference the old primary: their variables are rewritten to the new primary as staged changes (review and deploy them).", len(refs)))
	}
	return st, nil
}

func (s *Service) schedulesOf(ctx context.Context, q *db.Queries, id uuid.UUID) (int, error) {
	n, err := q.CountBackupSchedulesOfService(ctx, id)
	return int(n), err
}

// Promote promotes a read replica. With DryRun it only returns the plan.
func (s *Service) Promote(ctx context.Context, replicaID uuid.UUID, in PromoteInput) (PromoteResult, error) {
	rsc, _, _, err := s.managed(ctx, policy.ScopeProjectsWrite, replicaID)
	if err != nil {
		return PromoteResult{}, err
	}
	if err := policy.Require(ctx, policy.ScopeDeploymentsWrite, &rsc.ProjectID); err != nil {
		return PromoteResult{}, err
	}
	st, err := s.planPromote(ctx, s.q, rsc)
	if err != nil {
		return PromoteResult{}, err
	}
	res := PromoteResult{DryRun: in.DryRun, Plan: st.plan}
	if in.DryRun {
		return res, nil
	}
	if err := firstProblem(st.plan.Blockers); err != nil {
		return PromoteResult{}, err
	}
	if in.Confirm != rsc.Service.Slug {
		return PromoteResult{}, apierr.Unprocessable(CodePromoteConfirmation, "confirm must equal the replica's slug").
			WithDetails(map[string]any{"slug": rsc.Service.Slug})
	}
	if st.plan.Mode == ModeForced && !in.AcceptDataLoss {
		return PromoteResult{}, apierr.Unprocessable(CodeDataLossNotAccepted, st.plan.DataLossWarning+" Send acceptDataLoss=true to proceed.")
	}
	effects := []string{
		fmt.Sprintf("Promotes %s to the primary of its replica group (%s).", rsc.Service.Name, st.plan.Mode),
		fmt.Sprintf("Fences and stops %s: it never runs as a primary again.", st.psc.Service.Name),
		"Rewrites the variables that reference the old primary as staged changes.",
	}
	if st.plan.Mode == ModeForced {
		effects = append(effects, st.plan.DataLossWarning)
	}
	if err := rsc.Guard(ctx, "promote_database",
		fmt.Sprintf("Promote the production replica %s to primary (%s / %s)", rsc.Service.Name, rsc.ProjectName, rsc.EnvName),
		true, effects, st.plan.Mode, in.AcceptDataLoss); err != nil {
		return PromoteResult{}, err
	}

	var promo db.DatabasePromotion
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		var err error
		promo, err = q.InsertDatabasePromotion(ctx, db.InsertDatabasePromotionParams{
			WorkspaceID: rsc.WorkspaceID, EnvironmentID: rsc.EnvironmentID, ReplicaServiceID: rsc.Service.ID, OldPrimaryServiceID: st.psc.Service.ID,
			Mode: st.plan.Mode, AcceptDataLoss: in.AcceptDataLoss, ActorLabel: actorLabel(ctx),
		})
		if err != nil {
			return err
		}
		step := pstepFreeze
		if st.plan.Mode == ModeForced {
			// The fence is persisted before anything else (and carried in every reconcile).
			if err := q.InsertDatabaseFence(ctx, db.InsertDatabaseFenceParams{
				ServiceID: st.psc.Service.ID, PromotionID: &promo.ID, Reason: "forced promote of " + rsc.Service.Slug,
			}); err != nil {
				return err
			}
			step = pstepPromote
		}
		if err := q.UpdateDatabasePromotionStep(ctx, db.UpdateDatabasePromotionStepParams{ID: promo.ID, Step: step, Message: "Started."}); err != nil {
			return err
		}
		if err := q.SetDatabaseReplicaState(ctx, db.SetDatabaseReplicaStateParams{
			ServiceID: rsc.Service.ID, State: ReplicaPromoting, Message: "Promotion in progress.", Step: st.row.Step,
		}); err != nil {
			return err
		}
		if s.rep.Jobs != nil {
			if err := s.rep.Jobs.InsertTx(ctx, tx, PromotionAdvanceArgs{PromotionID: promo.ID}, nil); err != nil {
				return fmt.Errorf("enqueue promotion: %w", err)
			}
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: ActionPromote, ResourceType: "service", ResourceID: rsc.Service.ID.String(), ProjectID: &rsc.ProjectID,
			Metadata: map[string]any{
				"replica": rsc.Service.Slug, "oldPrimary": st.psc.Service.Slug, "mode": st.plan.Mode,
				"acceptDataLoss": in.AcceptDataLoss, "promotionId": promo.ID.String(),
			},
		})
	})
	if err != nil {
		if services.IsUniqueViolation(err) {
			return PromoteResult{}, apierr.Conflict(CodePromoteInProgress, "a promotion of this replica is already running")
		}
		return PromoteResult{}, fmt.Errorf("start promotion: %w", err)
	}
	s.afterTransition(ctx, st.row, ReplicaPromoting, "Promotion in progress.", nil)
	if st.plan.Mode == ModeForced {
		if srv := valueOr(st.psc.Service.ServerID); srv != uuid.Nil {
			s.resync(ctx, srv)
		}
	}
	res.PromotionID, res.Status = &promo.ID, "running"
	return res, nil
}

// setPromoStep records the promotion's progress.
func (s *Service) setPromoStep(ctx context.Context, p db.DatabasePromotion, step, msg string, lsn *string) error {
	return s.q.UpdateDatabasePromotionStep(ctx, db.UpdateDatabasePromotionStepParams{ID: p.ID, Step: step, Message: msg, FreezeLsn: lsn})
}

// AdvancePromotion moves a promotion forward; it reports done once the
// promotion finished (succeeded or failed). Re-entrant.
func (s *Service) AdvancePromotion(ctx context.Context, id uuid.UUID) (bool, error) {
	for range 12 {
		p, err := s.q.GetDatabasePromotion(ctx, id)
		if isNoRows(err) {
			return true, nil
		}
		if err != nil {
			return false, err
		}
		if p.Status != "running" {
			return true, nil
		}
		moved, done, err := s.advancePromotionOnce(ctx, p)
		if err != nil {
			return false, err
		}
		if done {
			return true, nil
		}
		if !moved {
			return false, nil
		}
	}
	return false, nil
}

func (s *Service) advancePromotionOnce(ctx context.Context, p db.DatabasePromotion) (moved, done bool, err error) {
	rsc, err := services.LoadScopeInternal(ctx, s.q, p.ReplicaServiceID)
	if err != nil {
		return false, true, s.finishPromotion(ctx, p, "failed", "the replica no longer exists")
	}
	psc, err := services.LoadScopeInternal(ctx, s.q, p.OldPrimaryServiceID)
	if err != nil {
		return false, true, s.finishPromotion(ctx, p, "failed", "the old primary no longer exists")
	}
	pspec, rspec := psc.Config().Database, rsc.Config().Database
	if pspec == nil || rspec == nil {
		return false, true, s.finishPromotion(ctx, p, "failed", "the group lost its database configuration")
	}
	key := func(step string) string { return "promo:" + p.ID.String() + ":" + step }
	step := func(next, msg string) (bool, bool, error) { return true, false, s.setPromoStep(ctx, p, next, msg, nil) }
	abort := func(msg string) (bool, bool, error) {
		return true, false, s.setPromoStep(ctx, p, pstepAbortUnfence, "Aborting: "+msg, nil)
	}
	// The abort steps keep the reason the switchover was abandoned for.
	keep := func(next string) (bool, bool, error) {
		return true, false, s.setPromoStep(ctx, p, next, p.Message, nil)
	}
	oldServer := valueOr(psc.Service.ServerID)
	if sv, err := s.currentServer(ctx, s.q, psc); err == nil {
		oldServer = sv
	}
	newServer := valueOr(rsc.Service.ServerID)
	vol, _ := pspec.DataMount()
	if v, err := s.dataVolumeName(ctx, s.q, psc.Service.ID, *pspec); err == nil {
		vol = v
	}

	switch p.Step {
	case pstepFreeze:
		cmd, err := s.replCommand(ctx, key("freeze"), oldServer, psc, *pspec, agentprotocol.DBActionReplFreeze, &agentprotocol.ReplAdmin{})
		if err != nil {
			return false, false, err
		}
		switch st, msg, _ := outcome(cmd); st {
		case cmdFailed:
			return abort("freezing the old primary failed: " + msg)
		case cmdOK:
			return step(pstepStop, "The old primary is read-only; stopping and fencing it.")
		}
		return false, false, nil

	case pstepStop:
		if err := s.q.InsertDatabaseFence(ctx, db.InsertDatabaseFenceParams{
			ServiceID: psc.Service.ID, PromotionID: &p.ID, Reason: "switchover to " + rsc.Service.Slug,
		}); err != nil {
			return false, false, err
		}
		if err := s.q.SetServiceDesiredState(ctx, db.SetServiceDesiredStateParams{ID: psc.Service.ID, DesiredState: services.DesiredStopped}); err != nil {
			return false, false, err
		}
		cmd, err := s.command(ctx, key("stop"), oldServer, agentprotocol.KindStopService, agentprotocol.ServiceActionPayload{
			ServiceID: psc.Service.ID.String(), StopGraceSeconds: max(psc.Config().StopGraceSeconds, 60),
		})
		if err != nil {
			return false, false, err
		}
		switch st, msg, _ := outcome(cmd); st {
		case cmdFailed:
			return abort("stopping the old primary failed: " + msg)
		case cmdOK:
			s.resync(ctx, oldServer)
			return step(pstepFinalLSN, "The old primary is stopped and fenced; reading its final position.")
		}
		return false, false, nil

	case pstepFinalLSN:
		cmd, err := s.replCommand(ctx, key("lsn"), oldServer, psc, *pspec, agentprotocol.DBActionReplFinalLSN, &agentprotocol.ReplAdmin{DataVolume: vol})
		if err != nil {
			return false, false, err
		}
		switch st, msg, data := outcome(cmd); st {
		case cmdFailed:
			return abort("reading the final position failed: " + msg)
		case cmdOK:
			var res agentprotocol.DBAdminResult
			if !mustUnmarshal(data, &res) || !engines.ValidLSN(res.FinalLSN) {
				return abort("the agent returned no valid final position")
			}
			lsn := res.FinalLSN
			return true, false, s.setPromoStep(ctx, p, pstepWait, "Waiting for the replica to replay up to "+lsn+".", &lsn)
		}
		return false, false, nil

	case pstepWait:
		if p.FreezeLsn == nil {
			return abort("no final position was recorded")
		}
		if caught, err := s.replicaCaughtUp(ctx, p, rsc, *p.FreezeLsn); err != nil {
			return false, false, err
		} else if caught {
			return step(pstepPromote, "The replica replayed everything; promoting it.")
		}
		since := p.StartedAt
		if c, err := s.q.GetAgentCommandByKey(ctx, key("lsn")); err == nil && c.FinishedAt != nil {
			since = *c.FinishedAt
		}
		if s.now().Sub(since) > catchUpTimeout {
			return abort("the replica did not replay the old primary's final position within 5 minutes; nothing was lost")
		}
		return false, false, nil

	case pstepPromote:
		lsn := ""
		if p.Mode == ModeSwitchover && p.FreezeLsn != nil {
			lsn = *p.FreezeLsn
		}
		for n := 1; n <= promoteAttempts; n++ {
			cmd, err := s.replCommand(ctx, key("promote:"+strconv.Itoa(n)), newServer, rsc, *rspec, agentprotocol.DBActionReplPromote, &agentprotocol.ReplAdmin{LSN: lsn})
			if err != nil {
				return false, false, err
			}
			switch st, msg, _ := outcome(cmd); st {
			case cmdRunning:
				return false, false, nil
			case cmdOK:
				return step(pstepRepoint, "The replica is the new primary; repointing the group.")
			case cmdFailed:
				if n == promoteAttempts {
					return false, true, s.failPromotion(ctx, p, rsc, "promoting the replica failed: "+msg+
						". The old primary is fenced; promote the replica again (forced) once it is reachable.")
				}
			}
		}
		return false, false, nil

	case pstepRepoint:
		if err := s.repoint(ctx, p, rsc, psc); err != nil {
			return false, false, err
		}
		return true, false, s.setPromoStep(ctx, p, pstepLinks, "Connecting consumers on other servers.", nil)

	case pstepLinks:
		msg := "The replica is the new primary."
		if s.rep.Links != nil {
			// The consumers' references name the new primary now.
			group := map[uuid.UUID]bool{psc.Service.ID: true, rsc.Service.ID: true}
			others, err := s.q.ListDatabaseReplicasOfPrimary(ctx, rsc.Service.ID)
			for _, o := range others {
				group[o.ServiceID] = true // replicas of the new primary reference it too
			}
			var refs []db.Service
			if err == nil {
				refs, _, err = s.referrersOfAny(ctx, s.q, rsc.EnvironmentID, map[string]bool{rsc.Service.Slug: true}, group)
			}
			if err == nil {
				ids := make([]uuid.UUID, len(refs))
				for i, c := range refs {
					ids[i] = c.ID
				}
				err = s.rep.Links.ReconcileAdd(ctx, rsc.EnvironmentID, ids)
			}
			if err != nil {
				s.logger.WarnContext(ctx, "reconcile mesh links after promote", "env_id", rsc.EnvironmentID, "err", err)
				msg += " Mesh links for consumers on other servers are pending: deploy them to retry."
			}
		}
		s.resync(ctx, oldServer)
		s.resync(ctx, newServer)
		return false, true, s.finishPromotion(ctx, p, "succeeded", msg)

	// Abort path: a switchover that cannot complete puts the old primary back.
	case pstepAbortUnfence:
		if _, err := s.q.DeleteDatabaseFence(ctx, psc.Service.ID); err != nil {
			return false, false, err
		}
		if err := s.q.SetServiceDesiredState(ctx, db.SetServiceDesiredStateParams{ID: psc.Service.ID, DesiredState: services.DesiredRunning}); err != nil {
			return false, false, err
		}
		s.resync(ctx, oldServer)
		return keep(pstepAbortRestart)
	case pstepAbortRestart:
		cmd, err := s.command(ctx, key("restart"), oldServer, agentprotocol.KindRestartService, agentprotocol.ServiceActionPayload{
			ServiceID: psc.Service.ID.String(), StopGraceSeconds: psc.Config().StopGraceSeconds,
		})
		if err != nil {
			return false, false, err
		}
		if st, _, _ := outcome(cmd); st == cmdRunning {
			return false, false, nil
		}
		return keep(pstepAbortUnfreeze)
	case pstepAbortUnfreeze:
		cmd, err := s.replCommand(ctx, key("unfreeze"), oldServer, psc, *pspec, agentprotocol.DBActionReplUnfreeze, &agentprotocol.ReplAdmin{})
		if err != nil {
			return false, false, err
		}
		if st, _, _ := outcome(cmd); st == cmdRunning {
			return false, false, nil
		}
		return false, true, s.failPromotion(ctx, p, rsc, strings.TrimSuffix(strings.TrimPrefix(p.Message, "Aborting: "), ".")+
			". The switchover was rolled back: the old primary is running and writable again, nothing was lost.")
	}
	return false, true, s.finishPromotion(ctx, p, "failed", "unknown step "+p.Step)
}

// replicaCaughtUp reports whether the replica has replayed up to lsn, from its
// last observation.
func (s *Service) replicaCaughtUp(ctx context.Context, p db.DatabasePromotion, rsc services.Scope, lsn string) (bool, error) {
	r, ok, err := s.replicaRow(ctx, s.q, rsc.Service.ID)
	if err != nil || !ok || r.ReplayLsn == nil || r.ObservedAt == nil {
		return false, err
	}
	if r.ObservedAt.Before(p.StartedAt) {
		return false, nil
	}
	have, ok1 := parseLSN(*r.ReplayLsn)
	want, ok2 := parseLSN(lsn)
	return ok1 && ok2 && have >= want, nil
}

// parseLSN reads a PostgreSQL pg_lsn (<hi>/<lo>, hex) as a number.
func parseLSN(s string) (uint64, bool) {
	hi, lo, ok := strings.Cut(s, "/")
	if !ok {
		return 0, false
	}
	h, err1 := strconv.ParseUint(hi, 16, 32)
	l, err2 := strconv.ParseUint(lo, 16, 32)
	if err1 != nil || err2 != nil {
		return 0, false
	}
	return h<<32 | l, true
}

// failPromotion ends a promotion as failed and puts the replica back to
// streaming (the next observation corrects it).
func (s *Service) failPromotion(ctx context.Context, p db.DatabasePromotion, rsc services.Scope, msg string) error {
	if r, ok, err := s.replicaRow(ctx, s.q, rsc.Service.ID); err == nil && ok && r.State == ReplicaPromoting {
		if err := s.q.SetDatabaseReplicaState(ctx, db.SetDatabaseReplicaStateParams{
			ServiceID: r.ServiceID, State: ReplicaStreaming, Message: "", Step: r.Step,
		}); err != nil {
			return err
		}
	}
	return s.finishPromotion(ctx, p, "failed", msg)
}

func (s *Service) finishPromotion(ctx context.Context, p db.DatabasePromotion, status, msg string) error {
	if _, err := s.q.FinishDatabasePromotion(ctx, db.FinishDatabasePromotionParams{ID: p.ID, Status: status, Message: msg}); err != nil && !isNoRows(err) {
		return err
	}
	if rsc, err := services.LoadScopeInternal(ctx, s.q, p.ReplicaServiceID); err == nil {
		services.Publish(s.bus, EventDatabaseUpdated, rsc.ProjectID, rsc.EnvironmentID, p.ReplicaServiceID)
		services.Publish(s.bus, EventDatabaseUpdated, rsc.ProjectID, rsc.EnvironmentID, p.OldPrimaryServiceID)
		services.PublishService(s.bus, services.EventServiceUpdated, rsc)
	}
	return nil
}

// repoint is the one transaction that turns the group around: the replica row
// goes, the service becomes a plain primary, its credentials are literal
// copies, the other replicas follow the new primary (and need a rebuild), the
// backup schedules move and every consumer's references are rewritten, all as
// staged changes.
func (s *Service) repoint(ctx context.Context, p db.DatabasePromotion, rsc, psc services.Scope) error {
	var staged []db.Service
	var rewritten int
	err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		_, err := q.LockDatabaseReplica(ctx, rsc.Service.ID)
		if isNoRows(err) {
			return nil // already repointed
		}
		if err != nil {
			return err
		}
		eng := engines.Lookup(engines.Postgres)
		// The credentials the replica resolved through its references are copied
		// before the references go away.
		vars, err := s.resolved(ctx, q, rsc)
		if err != nil {
			return fmt.Errorf("resolve replica credentials: %w", err)
		}
		cur := rsc.Config()
		spec := *cur.Database
		spec.Replica = nil
		cur.Database = &spec
		if _, err := q.SetServiceConfigBumped(ctx, db.SetServiceConfigBumpedParams{ID: rsc.Service.ID, Config: mustJSON(cur.WithDefaults())}); err != nil {
			return err
		}
		for _, v := range []struct {
			key    string
			secret bool
		}{{eng.UserKey, false}, {eng.PasswordKey, true}, {eng.DatabaseKey, false}} {
			if val, ok := vars[v.key]; ok && v.key != "" {
				if _, err := services.WriteServiceVariable(ctx, q, s.box, rsc, services.VarWrite{Key: v.key, Value: val, Secret: v.secret}); err != nil {
					return fmt.Errorf("materialise %s: %w", v.key, err)
				}
			}
		}
		if _, _, err := services.DeleteServiceVariableVersioned(ctx, q, s.box, rsc, engines.ReplicationPasswordKey); err != nil {
			return fmt.Errorf("remove %s: %w", engines.ReplicationPasswordKey, err)
		}
		if _, err := q.DeleteDatabaseReplica(ctx, rsc.Service.ID); err != nil {
			return err
		}
		if s.rep.Mesh != nil {
			if err := s.rep.Mesh.Release(ctx, tx, rsc.Service.ID); err != nil {
				return fmt.Errorf("release replication grants: %w", err)
			}
		}
		if err := q.InsertDatabaseFence(ctx, db.InsertDatabaseFenceParams{ServiceID: psc.Service.ID, PromotionID: &p.ID, Reason: "promoted " + rsc.Service.Slug}); err != nil {
			return err
		}

		// Other replicas follow the new primary and need a rebuild.
		others, err := q.ListDatabaseReplicasOfPrimary(ctx, psc.Service.ID)
		if err != nil {
			return err
		}
		for _, o := range others {
			if err := s.repointReplica(ctx, tx, o, rsc, psc); err != nil {
				return err
			}
		}
		if _, err := q.MoveBackupSchedules(ctx, db.MoveBackupSchedulesParams{ServiceID: psc.Service.ID, ServiceID_2: rsc.Service.ID}); err != nil {
			return err
		}

		// Consumers: every reference to the old primary now names the new one.
		group := map[uuid.UUID]bool{psc.Service.ID: true, rsc.Service.ID: true}
		for _, o := range others {
			group[o.ServiceID] = true
		}
		refs, _, err := s.referrersOfAny(ctx, q, rsc.EnvironmentID, map[string]bool{psc.Service.Slug: true}, group)
		if err != nil {
			return err
		}
		for _, c := range refs {
			csc, err := services.LoadScopeInternal(ctx, q, c.ID)
			if err != nil {
				continue
			}
			n, err := s.rewriteReferences(ctx, q, csc, psc.Service.Slug, rsc.Service.Slug)
			if err != nil {
				return err
			}
			rewritten += n
			staged = append(staged, c)
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: ActionFence, ResourceType: "service", ResourceID: psc.Service.ID.String(), ProjectID: &psc.ProjectID,
			Metadata: map[string]any{"service": psc.Service.Slug, "promotion": p.ID.String(), "newPrimary": rsc.Service.Slug, "rewrittenVariables": rewritten},
		})
	})
	if err != nil {
		return fmt.Errorf("repoint group: %w", err)
	}
	services.Publish(s.bus, EventDatabaseUpdated, rsc.ProjectID, rsc.EnvironmentID, rsc.Service.ID)
	services.Publish(s.bus, EventDatabaseUpdated, psc.ProjectID, psc.EnvironmentID, psc.Service.ID)
	services.PublishService(s.bus, services.EventServiceUpdated, rsc)
	services.PublishService(s.bus, services.EventServiceUpdated, psc)
	s.publishReferrers(rsc, staged)
	return nil
}

// repointReplica makes replica o follow the promoted service: its credential
// references are rewritten, its mesh grants released (they led to the old
// primary) and its state is needs_rebuild.
func (s *Service) repointReplica(ctx context.Context, tx pgx.Tx, o db.DatabaseReplica, newPrimary, oldPrimary services.Scope) error {
	q := db.New(tx)
	osc, err := services.LoadScopeInternal(ctx, q, o.ServiceID)
	if err != nil {
		return nil
	}
	if _, err := s.rewriteReferences(ctx, q, osc, oldPrimary.Service.Slug, newPrimary.Service.Slug); err != nil {
		return err
	}
	if s.rep.Mesh != nil {
		if err := s.rep.Mesh.Release(ctx, tx, o.ServiceID); err != nil {
			return fmt.Errorf("release grants of %s: %w", osc.Service.Slug, err)
		}
	}
	_, cycle := parseStep(o.Step)
	return q.SetDatabaseReplicaPrimary(ctx, db.SetDatabaseReplicaPrimaryParams{
		ServiceID: o.ServiceID, PrimaryServiceID: newPrimary.Service.ID, State: ReplicaNeedsRebuild,
		Message: "The primary was promoted: rebuild this replica to follow " + newPrimary.Service.Slug + ".", Step: mkStep(stepDone, cycle),
	})
}

// rewriteReferences rewrites every variable of sc that references the service
// from so it references to, as staged changes. Generated variables hold random
// values, not references, and are skipped. It returns the number of variables
// changed.
func (s *Service) rewriteReferences(ctx context.Context, q *db.Queries, sc services.Scope, from, to string) (int, error) {
	rows, err := q.ListServiceVariables(ctx, sc.Service.ID)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, row := range rows {
		if row.Source == services.SourceGenerated {
			continue
		}
		val, err := s.box.OpenString(secretbox.ServiceVariablesValue, secretbox.RowID(sc.Service.ID.String(), row.Key), row.ValueEnc)
		if err != nil {
			return n, fmt.Errorf("decrypt variable %s: %w", row.Key, err)
		}
		nv, changed := varexpr.RewriteRefSlug(val, from, to)
		if !changed {
			continue
		}
		if _, err := services.WriteServiceVariable(ctx, q, s.box, sc, services.VarWrite{Key: row.Key, Value: nv, Secret: row.IsSecret}); err != nil {
			return n, fmt.Errorf("rewrite %s of %s: %w", row.Key, sc.Service.Slug, err)
		}
		n++
	}
	if n > 0 {
		// Variable writes do not stage by themselves: the bump puts the service in the staged bar.
		if _, err := q.BumpServiceGeneration(ctx, sc.Service.ID); err != nil {
			return n, fmt.Errorf("stage %s: %w", sc.Service.Slug, err)
		}
	}
	return n, nil
}
