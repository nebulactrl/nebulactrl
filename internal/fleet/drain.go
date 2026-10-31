package fleet

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/deployments"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/servers"
	"github.com/nebulactrl/nebulactrl/internal/services"
)

// Waits between drain steps while a deployment is running.
const (
	pollInterval  = 2 * time.Second
	retryInterval = 500 * time.Millisecond
	busyInterval  = 3 * time.Second
)

// actorCtx rebuilds the initiator of a drain for the job's service calls, so
// audit entries and deployments are attributed to them. The principal holds
// exactly the scopes a move needs.
func actorCtx(ctx context.Context, d db.ServerDrain) context.Context {
	p := &auth.Principal{
		Type: d.ActorType, WorkspaceID: d.WorkspaceID, Label: d.ActorLabel,
		Scopes: []string{policy.ScopeProjectsRead, policy.ScopeProjectsWrite, policy.ScopeDeploymentsWrite},
	}
	if id, err := uuid.Parse(d.ActorID); err == nil {
		if d.ActorType == auth.TypeUser {
			p.UserID = id
		} else {
			p.TokenID = &id
		}
	}
	return auth.WithPrincipal(ctx, p)
}

func decodeMoves(raw json.RawMessage) ([]storedMove, error) {
	var m []storedMove
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("decode drain moves: %w", err)
	}
	return m, nil
}

// view renders a drain with the live status of its deployments.
func (s *Service) view(ctx context.Context, d db.ServerDrain) (DrainView, error) {
	stored, err := decodeMoves(d.Moves)
	if err != nil {
		return DrainView{}, err
	}
	var depIDs []uuid.UUID
	for _, m := range stored {
		if m.DeploymentID != nil {
			depIDs = append(depIDs, *m.DeploymentID)
		}
	}
	status := map[uuid.UUID]string{}
	if len(depIDs) > 0 {
		deps, err := s.q.ListDeploymentsByIDs(ctx, depIDs)
		if err != nil {
			return DrainView{}, fmt.Errorf("list drain deployments: %w", err)
		}
		for _, dep := range deps {
			status[dep.ID] = dep.Status
		}
	}
	v := DrainView{
		ID: d.ID, ServerID: d.ServerID, State: d.State, Message: d.Message, CancelRequested: d.CancelRequested,
		ActorLabel: d.ActorLabel, CreatedAt: d.CreatedAt, FinishedAt: d.FinishedAt, Moves: make([]Move, 0, len(stored)),
	}
	for _, m := range stored {
		if m.DeploymentID != nil {
			m.DeploymentStatus = status[*m.DeploymentID]
		}
		v.Moves = append(v.Moves, m.Move)
	}
	return v, nil
}

// Get returns the running drain of a server, else the newest finished one.
func (s *Service) Get(ctx context.Context, serverID uuid.UUID) (*DrainView, error) {
	if _, err := requireFleetWide(ctx, policy.ScopeServersRead, policy.ScopeProjectsRead); err != nil {
		return nil, err
	}
	if _, err := s.loadServer(ctx, policy.ScopeServersRead, serverID); err != nil {
		return nil, err
	}
	d, err := s.q.GetServerDrain(ctx, serverID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get drain: %w", err)
	}
	v, err := s.view(ctx, d)
	return &v, err
}

// Start begins draining a server: it validates the moves, marks the server as
// draining (so nothing new is placed on it) and schedules the DrainJob.
func (s *Service) Start(ctx context.Context, serverID uuid.UUID, in StartInput) (DrainView, error) {
	p, err := requireFleetWide(ctx, policy.ScopeServersWrite, policy.ScopeDeploymentsWrite, policy.ScopeProjectsWrite)
	if err != nil {
		return DrainView{}, err
	}
	src, err := s.loadServer(ctx, policy.ScopeServersWrite, serverID)
	if err != nil {
		return DrainView{}, err
	}
	if src.DrainState == services.DrainStateDraining {
		return DrainView{}, apierr.Conflict("drain_in_progress", fmt.Sprintf("%s is already being drained", src.Name))
	}
	sn, err := s.load(ctx, src.WorkspaceID)
	if err != nil {
		return DrainView{}, err
	}
	rows := sn.sourceRows(serverID)
	byID := make(map[uuid.UUID]db.ListFleetServicesRow, len(rows))
	for _, r := range rows {
		byID[r.ID] = r
	}

	targets := make(map[uuid.UUID]uuid.UUID, len(in.Moves))
	for _, m := range in.Moves {
		if _, dup := targets[m.ServiceID]; dup {
			return DrainView{}, apierr.Unprocessable("invalid_moves", "a service appears in more than one move")
		}
		if _, ok := byID[m.ServiceID]; !ok {
			return DrainView{}, apierr.Unprocessable("invalid_moves", fmt.Sprintf("service %s is not placed on %s", m.ServiceID, src.Name))
		}
		targets[m.ServiceID] = m.TargetServerID
	}
	var missing []string
	for _, r := range rows {
		if _, ok := targets[r.ID]; !ok {
			missing = append(missing, r.Name)
		}
	}
	if len(missing) > 0 {
		return DrainView{}, apierr.Unprocessable("incomplete_moves", "every service on the server must be moved; missing: "+joinNames(missing))
	}
	for _, t := range targets {
		h, err := s.checkTarget(sn, serverID, t)
		if err != nil {
			return DrainView{}, err
		}
		if h.status != "online" {
			return DrainView{}, apierr.Unprocessable(BlockTargetOffline, fmt.Sprintf("%s is %s; a deployment to it could not start", h.srv.Name, h.status))
		}
	}
	plan := s.plan(sn, serverID, nil, targets)
	var stateful []string
	for _, m := range plan.Moves {
		if m.Stateful {
			stateful = append(stateful, m.ServiceName)
		}
	}
	if len(stateful) > 0 && !in.ConfirmStateful {
		return DrainView{}, apierr.Unprocessable("confirmation_required",
			"volumes are not copied: confirmStateful is required to move "+joinNames(stateful))
	}
	if err := s.guardDrainStart(ctx, serverID, src.WorkspaceID, src.Name, rows, plan.Moves, in.ConfirmStateful); err != nil {
		return DrainView{}, err
	}

	moves := make([]storedMove, 0, len(plan.Moves))
	for _, pm := range plan.Moves {
		if len(pm.Blocking) > 0 || pm.TargetServerID == nil {
			return DrainView{}, apierr.Unprocessable(pm.Blocking[0].Code, pm.Blocking[0].Message)
		}
		moves = append(moves, storedMove{Move: Move{
			ServiceID: pm.ServiceID, ServiceName: pm.ServiceName, ProjectName: pm.ProjectName, EnvironmentName: pm.EnvironmentName,
			SourceServerID: serverID, TargetServerID: *pm.TargetServerID, TargetServerName: pm.TargetServerName,
			Stateful: pm.Stateful, Status: MovePending,
		}})
	}
	raw, err := json.Marshal(moves)
	if err != nil {
		return DrainView{}, err
	}

	var drain db.ServerDrain
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		var err error
		drain, err = q.InsertServerDrain(ctx, db.InsertServerDrainParams{
			ServerID: serverID, WorkspaceID: src.WorkspaceID, Moves: raw,
			ActorType: p.Type, ActorID: p.ID().String(), ActorLabel: p.Label,
		})
		if err != nil {
			return err
		}
		if _, err := q.SetServerDrainState(ctx, db.SetServerDrainStateParams{ID: serverID, DrainState: services.DrainStateDraining}); err != nil {
			return err
		}
		if err := s.jobs.InsertTx(ctx, tx, DrainArgs{DrainID: drain.ID}, nil); err != nil {
			return fmt.Errorf("enqueue drain job: %w", err)
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: "server.drain.start", ResourceType: "server", ResourceID: serverID.String(),
			Metadata: map[string]any{"drainId": drain.ID.String(), "server": src.Name, "moves": len(moves), "confirmStateful": in.ConfirmStateful, "plan": movePairs(moves)},
		})
	})
	if services.IsUniqueViolation(err) {
		return DrainView{}, apierr.Conflict("drain_in_progress", fmt.Sprintf("%s is already being drained", src.Name))
	}
	if err != nil {
		return DrainView{}, fmt.Errorf("start drain: %w", err)
	}
	s.publish(serverID)
	return s.view(ctx, drain)
}

func movePairs(moves []storedMove) []map[string]string {
	out := make([]map[string]string, len(moves))
	for i, m := range moves {
		out[i] = map[string]string{"serviceId": m.ServiceID.String(), "targetServerId": m.TargetServerID.String()}
	}
	return out
}

// Cancel asks the running drain to stop after the move in flight.
func (s *Service) Cancel(ctx context.Context, serverID uuid.UUID) (DrainView, error) {
	if _, err := requireFleetWide(ctx, policy.ScopeServersWrite, policy.ScopeDeploymentsWrite); err != nil {
		return DrainView{}, err
	}
	if _, err := s.loadServer(ctx, policy.ScopeServersWrite, serverID); err != nil {
		return DrainView{}, err
	}
	d, err := s.q.RequestServerDrainCancel(ctx, serverID)
	if errors.Is(err, pgx.ErrNoRows) {
		return DrainView{}, apierr.Conflict("no_drain_in_progress", "this server is not being drained")
	}
	if err != nil {
		return DrainView{}, fmt.Errorf("cancel drain: %w", err)
	}
	s.publish(serverID)
	return s.view(ctx, d)
}

// Undrain makes a drained or failed-drain server schedulable again.
func (s *Service) Undrain(ctx context.Context, serverID uuid.UUID) (servers.Server, error) {
	if _, err := requireFleetWide(ctx, policy.ScopeServersWrite); err != nil {
		return servers.Server{}, err
	}
	src, err := s.loadServer(ctx, policy.ScopeServersWrite, serverID)
	if err != nil {
		return servers.Server{}, err
	}
	switch src.DrainState {
	case services.DrainStateDraining:
		return servers.Server{}, apierr.Conflict("drain_in_progress", "cancel the drain before undraining the server")
	case services.DrainStateNone:
		return s.servers.Get(ctx, serverID)
	}
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := db.New(tx).SetServerDrainState(ctx, db.SetServerDrainStateParams{ID: serverID, DrainState: services.DrainStateNone}); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: "server.undrain", ResourceType: "server", ResourceID: serverID.String(),
			Metadata: map[string]any{"server": src.Name, "from": src.DrainState},
		})
	})
	if err != nil {
		return servers.Server{}, fmt.Errorf("undrain: %w", err)
	}
	s.publish(serverID)
	return s.servers.Get(ctx, serverID)
}

// StepResult tells the job what to do next.
type StepResult struct {
	// Done is true once the drain reached a final state.
	Done bool
	// Wait is how long to wait before the next step; zero means "step again now".
	Wait time.Duration
}

// Step advances a drain by one action. It is restart-safe: all progress lives
// in server_drains.moves, so any step can run again after a crash.
func (s *Service) Step(ctx context.Context, drainID uuid.UUID) (StepResult, error) {
	d, err := s.q.GetServerDrainByID(ctx, drainID)
	if errors.Is(err, pgx.ErrNoRows) {
		return StepResult{Done: true}, nil
	}
	if err != nil {
		return StepResult{}, fmt.Errorf("get drain: %w", err)
	}
	if d.State != RunRunning {
		return StepResult{Done: true}, nil
	}
	moves, err := decodeMoves(d.Moves)
	if err != nil {
		return StepResult{}, err
	}
	actx := actorCtx(ctx, d)

	next := -1
	for i, m := range moves {
		if m.Status == MoveMoving || m.Status == MoveDeploying {
			return s.driveMove(actx, d, moves, i)
		}
		if m.Status == MovePending && next < 0 {
			next = i
		}
	}
	if d.CancelRequested {
		return s.finishCanceled(actx, d, moves)
	}
	if next < 0 {
		return s.finishCompleted(actx, d, moves)
	}
	return s.driveMove(actx, d, moves, next)
}

func (s *Service) saveMoves(ctx context.Context, d db.ServerDrain, moves []storedMove) error {
	raw, err := json.Marshal(moves)
	if err != nil {
		return err
	}
	if err := s.q.UpdateServerDrainMoves(ctx, db.UpdateServerDrainMovesParams{ID: d.ID, Moves: raw}); err != nil {
		return fmt.Errorf("save drain moves: %w", err)
	}
	s.publish(d.ServerID)
	return nil
}

// driveMove advances move i: change the placement, deploy, then wait for the
// deployment to end.
func (s *Service) driveMove(ctx context.Context, d db.ServerDrain, moves []storedMove, i int) (StepResult, error) {
	m := &moves[i]
	sc, err := services.LoadScopeInternal(ctx, s.q, m.ServiceID)
	if ae, ok := apierr.As(err); ok && ae.Status == 404 {
		m.Status, m.Message = MoveDone, "The service no longer exists."
		return StepResult{}, s.saveMoves(ctx, d, moves)
	}
	if err != nil {
		return StepResult{}, err
	}

	if m.Status == MoveDeploying && m.DeploymentID != nil {
		return s.pollMove(ctx, d, moves, i)
	}

	placed := sc.Service.ServerID != nil && *sc.Service.ServerID == m.TargetServerID
	if !placed {
		if n, err := s.q.CountActiveDeployments(ctx, m.ServiceID); err != nil {
			return StepResult{}, err
		} else if n > 0 {
			return StepResult{Wait: busyInterval}, nil // let the running deployment finish first
		}
		m.Status = MoveMoving
		m.GenerationBefore, m.GenerationAfter = sc.Service.Generation, sc.Service.Generation+1
		if err := s.saveMoves(ctx, d, moves); err != nil {
			return StepResult{}, err
		}
		target := m.TargetServerID
		_, err := s.svcs.Update(ctx, m.ServiceID, sc.Service.Generation, services.UpdateInput{ServerID: &target})
		if ae, ok := apierr.As(err); ok {
			if ae.Code == "generation_conflict" {
				return StepResult{Wait: retryInterval}, nil
			}
			return s.failMove(ctx, d, moves, i, fmt.Sprintf("Moving %s to %s failed: %s", m.ServiceName, m.TargetServerName, ae.Message))
		}
		if err != nil {
			return StepResult{}, err
		}
		if sc, err = services.LoadScopeInternal(ctx, s.q, m.ServiceID); err != nil {
			return StepResult{}, err
		}
	}

	// Placement is on the target. A service that never ran has nothing to start.
	if sc.Service.CurrentDeploymentID == nil {
		m.Status, m.Message = MoveDone, "Not deployed yet: only the placement changed."
		return StepResult{}, s.saveMoves(ctx, d, moves)
	}

	// A deployment of ours that survived a crash is adopted instead of repeated.
	if act, err := s.q.GetActiveDeployment(ctx, m.ServiceID); err == nil {
		if act.Trigger == deployments.TriggerDrain {
			m.Status, m.DeploymentID = MoveDeploying, &act.ID
			return StepResult{Wait: pollInterval}, s.saveMoves(ctx, d, moves)
		}
		return StepResult{Wait: busyInterval}, nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return StepResult{}, err
	}

	sha := ""
	if raw, err := s.q.GetServiceCurrentCommit(ctx, m.ServiceID); err == nil {
		sha = services.DecodeSource(raw).CommitSHA
	}
	dv, err := s.deps.DeployDrain(ctx, m.ServiceID, sha)
	if ae, ok := apierr.As(err); ok {
		if ae.Code == "deployment_in_progress" {
			return StepResult{Wait: busyInterval}, nil
		}
		return s.failMove(ctx, d, moves, i, fmt.Sprintf("Moving %s to %s failed: %s", m.ServiceName, m.TargetServerName, ae.Message))
	}
	if err != nil {
		return StepResult{}, err
	}
	m.Status, m.DeploymentID = MoveDeploying, &dv.ID
	return StepResult{Wait: pollInterval}, s.saveMoves(ctx, d, moves)
}

func (s *Service) pollMove(ctx context.Context, d db.ServerDrain, moves []storedMove, i int) (StepResult, error) {
	m := &moves[i]
	dep, err := s.q.GetDeploymentStatus(ctx, *m.DeploymentID)
	if errors.Is(err, pgx.ErrNoRows) {
		return s.failMove(ctx, d, moves, i, fmt.Sprintf("Moving %s to %s failed: its deployment disappeared.", m.ServiceName, m.TargetServerName))
	}
	if err != nil {
		return StepResult{}, err
	}
	switch st := deployments.Status(dep.Status); {
	case st.IsActive():
		return StepResult{Wait: pollInterval}, nil
	case st == deployments.Succeeded:
		m.Status, m.Message = MoveDone, ""
		return StepResult{}, s.saveMoves(ctx, d, moves)
	default:
		reason := dep.StatusMessage
		if reason == "" {
			reason = "the deployment ended as " + dep.Status
		}
		return s.failMove(ctx, d, moves, i, fmt.Sprintf("Moving %s to %s failed: %s", m.ServiceName, m.TargetServerName, reason))
	}
}

// failMove ends the drain as failed at move i. The service goes back to the
// server it was on (when nothing else changed it meanwhile), so the placement
// agrees with what is actually running; the remaining services are untouched.
func (s *Service) failMove(ctx context.Context, d db.ServerDrain, moves []storedMove, i int, msg string) (StepResult, error) {
	m := &moves[i]
	m.Status, m.Message = MoveFailed, msg
	raw, err := json.Marshal(moves)
	if err != nil {
		return StepResult{}, err
	}
	var sc *services.Scope
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		if m.GenerationAfter > 0 {
			n, err := q.RevertServicePlacement(ctx, db.RevertServicePlacementParams{
				ID: m.ServiceID, ServerID: &m.SourceServerID, Generation: m.GenerationBefore,
				ExpectedGeneration: m.GenerationAfter, CurrentServerID: &m.TargetServerID,
			})
			if err != nil {
				return err
			}
			if n == 0 {
				m.Message += " (the service's placement was changed meanwhile and was not reverted)"
				if raw, err = json.Marshal(moves); err != nil {
					return err
				}
			}
		}
		if err := q.FinishServerDrain(ctx, db.FinishServerDrainParams{ID: d.ID, State: RunFailed, Message: m.Message, Moves: raw}); err != nil {
			return err
		}
		if _, err := q.SetServerDrainState(ctx, db.SetServerDrainStateParams{ID: d.ServerID, DrainState: services.DrainStateFailed, DrainMessage: m.Message}); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: "server.drain.fail", ResourceType: "server", ResourceID: d.ServerID.String(),
			Metadata: map[string]any{"drainId": d.ID.String(), "service": m.ServiceName, "message": m.Message},
		})
	})
	if err != nil {
		return StepResult{}, fmt.Errorf("fail drain: %w", err)
	}
	if got, err := services.LoadScopeInternal(ctx, s.q, m.ServiceID); err == nil {
		sc = &got
		services.PublishService(s.bus, services.EventServiceUpdated, *sc)
	}
	s.log.WarnContext(ctx, "server drain failed", "server_id", d.ServerID.String(), "drain_id", d.ID.String(), "service", m.ServiceName, "message", m.Message)
	s.publish(d.ServerID)
	return StepResult{Done: true}, nil
}

func (s *Service) finishCompleted(ctx context.Context, d db.ServerDrain, moves []storedMove) (StepResult, error) {
	raw, err := json.Marshal(moves)
	if err != nil {
		return StepResult{}, err
	}
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		if err := q.FinishServerDrain(ctx, db.FinishServerDrainParams{ID: d.ID, State: RunCompleted, Moves: raw}); err != nil {
			return err
		}
		if _, err := q.SetServerDrainState(ctx, db.SetServerDrainStateParams{ID: d.ServerID, DrainState: services.DrainStateDrained}); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: "server.drain.complete", ResourceType: "server", ResourceID: d.ServerID.String(),
			Metadata: map[string]any{"drainId": d.ID.String(), "moves": len(moves)},
		})
	})
	if err != nil {
		return StepResult{}, fmt.Errorf("complete drain: %w", err)
	}
	s.publish(d.ServerID)
	return StepResult{Done: true}, nil
}

func (s *Service) finishCanceled(ctx context.Context, d db.ServerDrain, moves []storedMove) (StepResult, error) {
	done := 0
	for _, m := range moves {
		if m.Status == MoveDone {
			done++
		}
	}
	state, msg := services.DrainStateNone, ""
	if done > 0 {
		state, msg = services.DrainStateFailed, fmt.Sprintf("Canceled after %d of %d moves", done, len(moves))
	}
	raw, err := json.Marshal(moves)
	if err != nil {
		return StepResult{}, err
	}
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		if err := q.FinishServerDrain(ctx, db.FinishServerDrainParams{ID: d.ID, State: RunCanceled, Message: strings.TrimSpace(msg), Moves: raw}); err != nil {
			return err
		}
		if _, err := q.SetServerDrainState(ctx, db.SetServerDrainStateParams{ID: d.ServerID, DrainState: state, DrainMessage: msg}); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: "server.drain.cancel", ResourceType: "server", ResourceID: d.ServerID.String(),
			Metadata: map[string]any{"drainId": d.ID.String(), "done": done, "total": len(moves)},
		})
	})
	if err != nil {
		return StepResult{}, fmt.Errorf("cancel drain: %w", err)
	}
	s.publish(d.ServerID)
	return StepResult{Done: true}, nil
}
