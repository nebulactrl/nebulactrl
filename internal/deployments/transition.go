package deployments

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// StatusStep is the deployment_events step used for status-change rows.
const StatusStep = "status"

// advance moves deployment id to status `to`, walking intermediate statuses so
// every hop appends a deployment_events row. message becomes status_message
// (when non-empty) and is attached to the final hop. A deployment that already
// reached `to` or a terminal status is left untouched (changed=false), which
// makes redelivered agent frames harmless. A transition outside the table
// returns *TransitionError.
func (s *Service) advance(ctx context.Context, tx pgx.Tx, id uuid.UUID, to Status, message string) (dep db.Deployment, changed bool, err error) {
	q := db.New(tx)
	dep, err = q.LockDeployment(ctx, id)
	if err != nil {
		return db.Deployment{}, false, fmt.Errorf("lock deployment: %w", err)
	}
	cur := Status(dep.Status)
	if cur == to || cur.IsTerminal() {
		return dep, false, nil
	}
	path := Path(cur, to)
	if path == nil {
		return dep, false, &TransitionError{From: cur, To: to}
	}
	for i, hop := range path {
		msg := ""
		if i == len(path)-1 {
			msg = message
		}
		dep, err = q.SetDeploymentStatus(ctx, db.SetDeploymentStatusParams{
			ID: id, Status: string(hop), Message: msg,
			MarkStarted:  hop != Queued && hop != Created,
			MarkFinished: hop.IsTerminal(),
		})
		if err != nil {
			return db.Deployment{}, false, fmt.Errorf("set deployment status: %w", err)
		}
		if err := q.InsertDeploymentEvent(ctx, db.InsertDeploymentEventParams{
			DeploymentID: id, Step: StatusStep, Status: string(hop), Message: msg,
		}); err != nil {
			return db.Deployment{}, false, fmt.Errorf("insert deployment event: %w", err)
		}
	}
	return dep, true, nil
}

// failDeployment ends a non-terminal deployment as failed in its own transaction.
func (s *Service) failDeployment(ctx context.Context, id uuid.UUID, msg string) error {
	var changed bool
	err := db.InTx(ctx, s.Pool, func(tx pgx.Tx) error {
		var err error
		_, changed, err = s.advance(ctx, tx, id, Failed, msg)
		return err
	})
	if err != nil {
		return err
	}
	if changed {
		if dc, err := s.loadDeploymentInternal(ctx, id); err == nil {
			s.publish(services.EventDeploymentUpdated, dc)
			s.publishService(dc)
			s.notifyFinished(ctx, dc, Failed, msg)
		}
	}
	return nil
}

// Views renders deployments for the API, in order.
func (s *Service) Views(ctx context.Context, deps []db.Deployment) ([]DeploymentView, error) {
	out := make([]DeploymentView, 0, len(deps))
	if len(deps) == 0 {
		return out, nil
	}
	var relIDs, srvIDs []uuid.UUID
	for _, d := range deps {
		if d.ReleaseID != nil {
			relIDs = append(relIDs, *d.ReleaseID)
		}
		if d.ServerID != nil {
			srvIDs = append(srvIDs, *d.ServerID)
		}
		if d.BuildServerID != nil {
			srvIDs = append(srvIDs, *d.BuildServerID)
		}
	}
	rels := map[uuid.UUID]db.Release{}
	if len(relIDs) > 0 {
		rows, err := s.q.ListReleasesByIDs(ctx, relIDs)
		if err != nil {
			return nil, fmt.Errorf("list releases: %w", err)
		}
		for _, r := range rows {
			rels[r.ID] = r
		}
	}
	srvs := map[uuid.UUID]db.Server{}
	if len(srvIDs) > 0 {
		rows, err := s.q.LoadServerRows(ctx, srvIDs)
		if err != nil {
			return nil, fmt.Errorf("load servers: %w", err)
		}
		for _, r := range rows {
			srvs[r.ID] = r
		}
	}
	actors, err := s.actorLabels(ctx, deps)
	if err != nil {
		return nil, err
	}
	for _, d := range deps {
		v := DeploymentView{
			ID: d.ID, ServiceID: d.ServiceID, ReleaseID: d.ReleaseID, ServerID: d.ServerID, Trigger: d.Trigger,
			ActorType: d.ActorType, ActorLabel: actorDisplay(d, actors),
			Status: d.Status, Strategy: d.Strategy, StatusMessage: d.StatusMessage, RolledBackFrom: d.RolledBackFrom, Generation: d.Generation,
			CreatedAt: d.CreatedAt, StartedAt: d.StartedAt, FinishedAt: d.FinishedAt, Message: messagePtr(d.Message),
			CancelRequestedAt: d.CancelRequestedAt, CancelRequestedBy: d.CancelRequestedBy, CanceledAtStep: d.CanceledAtStep, NoCache: d.NoCache,
		}
		if d.BuildServerID != nil {
			v.BuildServerID = d.BuildServerID
			if b, ok := srvs[*d.BuildServerID]; ok {
				v.BuildServerName = b.Name
			}
		}
		if len(d.BuildInfo) > 0 {
			var bi agentprotocol.BuildInfo
			if json.Unmarshal(d.BuildInfo, &bi) == nil {
				v.Build = &bi
			}
		}
		if d.CommitSha != "" {
			v.Commit = &CommitView{SHA: d.CommitSha, Message: d.CommitMessage, Author: d.CommitAuthor, Branch: d.CommitBranch}
		}
		if d.ReleaseID != nil {
			if r, ok := rels[*d.ReleaseID]; ok {
				v.Release = &ReleaseView{ID: r.ID, ImageRef: r.ImageRef, Source: services.DecodeSource(r.Source), CreatedAt: r.CreatedAt}
			}
		}
		if Status(d.Status).IsActive() && d.BuildServerID != nil && d.ReleaseID != nil {
			// The build is still running (or waiting) on the build server: the deployment waits for it.
			depID := d.ID
			if bc, err := s.q.GetBuildCommandByDeployment(ctx, &depID); err == nil && (bc.Status == "pending" || bc.Status == "sent" || bc.Status == "acked") {
				if b, ok := srvs[*d.BuildServerID]; ok && b.Status != "online" {
					v.WaitingFor = &WaitingFor{ServerID: b.ID, ServerName: b.Name, OfflineSince: b.LastSeenAt}
				}
			}
		}
		if Status(d.Status).IsActive() && d.ServerID != nil && v.WaitingFor == nil {
			if srv, ok := srvs[*d.ServerID]; ok && srv.Status != "online" {
				depID := d.ID
				if cmd, err := s.q.GetDeployCommandByDeployment(ctx, &depID); err == nil && (cmd.Status == "pending" || cmd.Status == "sent" || cmd.Status == "acked") {
					v.WaitingFor = &WaitingFor{ServerID: srv.ID, ServerName: srv.Name, OfflineSince: srv.LastSeenAt}
				}
			}
		}
		out = append(out, v)
	}
	return out, nil
}

// stepStatus maps an agent deploy step to the deployment status it implies.
// ok is false for steps that do not change the status (image pulls stay in
// `preparing`; `retire` stays in `verifying`).
func stepStatus(step string) (Status, bool) {
	switch step {
	case agentprotocol.StepBuild, agentprotocol.StepPush:
		return Building, true
	case agentprotocol.StepPredeploy, agentprotocol.StepCreate, agentprotocol.StepStart:
		return Releasing, true
	case agentprotocol.StepHealth, agentprotocol.StepRoute, agentprotocol.StepVerify:
		return Verifying, true
	}
	return "", false
}

// outcomeStatus maps a result outcome to a terminal deployment status.
func outcomeStatus(outcome string) Status {
	switch outcome {
	case agentprotocol.OutcomeSucceeded:
		return Succeeded
	case agentprotocol.OutcomeRolledBack:
		return RolledBack
	case agentprotocol.OutcomeRollbackFailed:
		return RollbackFailed
	case agentprotocol.OutcomeCanceled:
		return Canceled
	default:
		return Failed
	}
}
