package previews

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/deployments"
	"github.com/nebulactrl/nebulactrl/internal/services"
)

// waveTimeout bounds one deploy wave; a wave that cannot finish by then fails.
const waveTimeout = 3 * time.Hour

// advance moves the current deploy wave of a preview one step:
//
//  1. restart the stopped services of a resumed preview;
//  2. start the deployments of the wave in reference order (a service starts
//     once the services it references finished deploying successfully);
//  3. when every deployment succeeded, run the seed commands one after the
//     other;
//  4. mark the preview ready, or failed with the reason.
//
// It reports how long to wait before the next step, or done when the wave has
// ended (or the preview is paused or being deleted). Workers are serialized
// per preview with an advisory lock; all state lives in previews.deploy_state,
// so a restart of the control plane resumes where the wave stood.
func (s *Service) advance(ctx context.Context, id uuid.UUID) (wait time.Duration, done bool, err error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return 0, false, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if err := s.lock(ctx, tx, "wave:"+id.String()); err != nil {
		return 0, false, err
	}
	pv, err := s.q.GetPreview(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, true, nil
	}
	if err != nil {
		return 0, false, err
	}
	st := decodeState(pv.DeployState)
	if st.Done || pv.Status == StatusDeleting || pv.Status == StatusPaused {
		return 0, true, nil
	}
	sys := s.sysCtx(ctx, pv)
	rows, err := s.q.ListAllEnvironmentServices(ctx, pv.EnvironmentID)
	if err != nil {
		return 0, false, err
	}
	byID := make(map[uuid.UUID]db.Service, len(rows))
	for _, r := range rows {
		byID[r.ID] = r
	}

	if !st.Restarted {
		for _, sid := range st.Restart {
			if _, ok := byID[sid]; !ok {
				continue
			}
			if _, err := s.Services.Restart(sys, sid); err != nil {
				s.Logger.WarnContext(ctx, "previews: restart service of resumed preview", "preview", id.String(), "service", sid.String(), "err", err)
			}
		}
		st.Restarted = true
		ok, err := s.save(ctx, pv, st, nil, nil)
		if err != nil || !ok {
			return wavePoll, false, err
		}
	}

	if s.Now().Sub(st.Started) > waveTimeout {
		return s.finish(ctx, pv, st, StatusFailed, "The deploy did not finish within "+waveTimeout.String()+".")
	}

	// 2. Deployments.
	statuses, err := s.deploymentStatuses(ctx, st)
	if err != nil {
		return 0, false, err
	}
	var targets []uuid.UUID
	inWave := map[uuid.UUID]bool{}
	for _, t := range st.Targets {
		if _, ok := byID[t]; ok {
			targets = append(targets, t)
			inWave[t] = true
		}
	}
	edges, err := services.DeployEdges(ctx, s.q, s.Box, pv.EnvironmentID)
	if err != nil {
		return 0, false, err
	}
	var (
		failure string
		running int // deployments started and not finished
		waiting int // services not started yet
		changed bool
	)
	for _, t := range deployments.OrderByReferences(targets, edges) {
		key := t.String()
		if depID, ok := st.Deployments[key]; ok {
			d := statuses[depID]
			switch {
			case deployments.Status(d.Status) == deployments.Succeeded:
			case deployments.Status(d.Status).IsTerminal():
				if failure == "" {
					failure = fmt.Sprintf("%s: deployment %s. %s", byID[t].Name, d.Status, d.StatusMessage)
				}
			default:
				running++
			}
			continue
		}
		if failure != "" {
			continue
		}
		blocked := false
		for _, e := range edges {
			if e.From != t || !inWave[e.To] {
				continue
			}
			pd, started := st.Deployments[e.To.String()]
			switch {
			case !started:
				blocked = true
			case deployments.Status(statuses[pd].Status) == deployments.Succeeded:
			case deployments.Status(statuses[pd].Status).IsTerminal():
				failure = fmt.Sprintf("%s was not deployed because %s did not deploy.", byID[t].Name, byID[e.To].Name)
			default:
				blocked = true
			}
		}
		if failure != "" {
			continue
		}
		if blocked {
			waiting++
			continue
		}
		var commit Commit
		if sc := byID[t]; sc.SourceType == services.SourceGit && contains(pv.AffectedServiceIds, t) {
			commit = Commit{SHA: st.SHA, Message: pv.PrTitle, Author: pv.PrAuthor, Branch: pv.HeadBranch}
		}
		depID, err := s.Deployer.Deploy(sys, t, commit, "Preview of PR #"+strconv.Itoa(int(pv.PrNumber)))
		switch {
		case isCode(err, "deployment_in_progress"):
			// A deployment of an earlier wave still runs: supersede it if the
			// agent has not started it, otherwise wait for it.
			s.cancelActive(sys, t)
			waiting++
			continue
		case err != nil:
			failure = fmt.Sprintf("%s could not be deployed: %s", byID[t].Name, errMessage(err))
			continue
		}
		if st.Deployments == nil {
			st.Deployments = map[string]uuid.UUID{}
		}
		st.Deployments[key] = depID
		changed = true
		running++
	}
	if changed {
		ok, err := s.save(ctx, pv, st, new(StatusDeploying), nil)
		if err != nil || !ok {
			return wavePoll, false, err
		}
	}
	switch {
	case failure != "" && running == 0:
		return s.finish(ctx, pv, st, StatusFailed, failure)
	case failure != "" || running > 0 || waiting > 0:
		return wavePoll, false, nil
	}

	// 3. Seed commands.
	return s.seeds(ctx, pv, st, rows)
}

// deploymentStatuses loads the deployments of the wave by id.
func (s *Service) deploymentStatuses(ctx context.Context, st deployState) (map[uuid.UUID]db.Deployment, error) {
	out := map[uuid.UUID]db.Deployment{}
	if len(st.Deployments) == 0 {
		return out, nil
	}
	ids := make([]uuid.UUID, 0, len(st.Deployments))
	for _, id := range st.Deployments {
		ids = append(ids, id)
	}
	rows, err := s.q.ListDeploymentsByIDs(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("list deployments: %w", err)
	}
	for _, r := range rows {
		out[r.ID] = r
	}
	return out, nil
}

// cancelActive cancels the active deployment of a service (best effort): an
// unacknowledged one right away, a running one by asking its agent to abort.
func (s *Service) cancelActive(ctx context.Context, serviceID uuid.UUID) {
	d, err := s.q.GetActiveDeployment(ctx, serviceID)
	if err != nil {
		return
	}
	if err := s.Deployer.Cancel(ctx, d.ID); err != nil && !isCode(err, "cannot_cancel") {
		s.Logger.WarnContext(ctx, "previews: cancel superseded deployment", "deployment", d.ID.String(), "err", err)
	}
}

// seeds runs the project's seed commands in order, one at a time, once every
// deployment of the wave succeeded.
func (s *Service) seeds(ctx context.Context, pv db.Preview, st deployState, rows []db.Service) (time.Duration, bool, error) {
	set, err := s.q.GetPreviewSettings(ctx, pv.ProjectID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return 0, false, err
	}
	var all []PreviewSeedCommand
	if err == nil {
		_ = json.Unmarshal(set.SeedCommands, &all)
	}
	bySlug := map[string]uuid.UUID{}
	for _, r := range rows {
		bySlug[r.Slug] = r.ID
	}
	for i, sd := range all {
		if !seedApplies(st.Mode, sd.RunOn) {
			continue
		}
		svcID, ok := bySlug[sd.Service]
		if !ok {
			continue // excluded or deleted: nothing to run it in
		}
		key := strconv.Itoa(i)
		if runID, started := st.Seeds[key]; started {
			status, exit, msg, err := s.Seeder.RunStatus(ctx, runID)
			if err != nil {
				return 0, false, err
			}
			switch status {
			case "succeeded":
				continue
			case "failed", "timed_out":
				text := fmt.Sprintf("Seed command on %s %s", sd.Service, humanStatus(status))
				if exit != nil {
					text += " (exit " + strconv.Itoa(*exit) + ")"
				}
				if msg != "" {
					text += ": " + msg
				}
				return s.finish(ctx, pv, st, StatusFailed, text)
			}
			return wavePoll, false, nil // queued or running
		}
		// Start this seed; the next one starts after it succeeded.
		var started bool
		err := db.InTx(ctx, s.Pool, func(tx pgx.Tx) error {
			runID, err := s.Seeder.RunSeed(systemCtx(ctx, pv.WorkspaceID, pv.ProjectID), tx, svcID, []string{"sh", "-c", sd.Command})
			if err != nil {
				return err
			}
			if st.Seeds == nil {
				st.Seeds = map[string]uuid.UUID{}
			}
			st.Seeds[key] = runID
			n, err := db.New(tx).UpdateWave(ctx, db.UpdateWaveParams{
				ID: pv.ID, Seq: int32(st.Seq), State: encodeState(st), Status: new(StatusSeeding), Message: new("Running seed command on " + sd.Service),
			})
			if err != nil {
				return err
			}
			if n == 0 {
				return errSuperseded
			}
			started = true
			return nil
		})
		switch {
		case errors.Is(err, errSuperseded):
			return wavePoll, false, nil
		case err != nil:
			var ae *apierr.Error
			if errors.As(err, &ae) {
				return s.finish(ctx, pv, st, StatusFailed, fmt.Sprintf("Seed command on %s could not start: %s", sd.Service, ae.Message))
			}
			return 0, false, err
		}
		if started {
			s.publish(pv)
		}
		return time.Second, false, nil
	}
	return s.finish(ctx, pv, st, StatusReady, "")
}

var errSuperseded = errors.New("deploy wave superseded")

func humanStatus(s string) string {
	if s == "timed_out" {
		return "timed out"
	}
	return s
}

// save persists the wave state (and optionally the status); ok is false when a
// newer wave, a pause or a delete took over.
func (s *Service) save(ctx context.Context, pv db.Preview, st deployState, status, message *string) (bool, error) {
	n, err := s.q.UpdateWave(ctx, db.UpdateWaveParams{ID: pv.ID, Seq: int32(st.Seq), State: encodeState(st), Status: status, Message: message})
	if err != nil {
		return false, fmt.Errorf("save wave: %w", err)
	}
	return n > 0, nil
}

// finish ends the wave with the given terminal status.
func (s *Service) finish(ctx context.Context, pv db.Preview, st deployState, status, message string) (time.Duration, bool, error) {
	st.Done = true
	ok, err := s.save(ctx, pv, st, &status, &message)
	if err != nil {
		return 0, false, err
	}
	if !ok {
		return wavePoll, false, nil
	}
	fresh, err := s.q.GetPreview(ctx, pv.ID)
	if err == nil {
		s.publish(fresh)
	}
	if err := s.enqueuePublish(ctx, pv.ID); err != nil {
		s.Logger.ErrorContext(ctx, "previews: enqueue publish", "preview", pv.ID.String(), "err", err)
	}
	return 0, true, nil
}

func contains(ids []uuid.UUID, id uuid.UUID) bool {
	for _, x := range ids {
		if x == id {
			return true
		}
	}
	return false
}

func errMessage(err error) string {
	var ae *apierr.Error
	if errors.As(err, &ae) {
		return ae.Message
	}
	return err.Error()
}

// seedApplies reports whether a seed command with runOn runs in a wave of the
// given mode: the first deployment (create) runs every seed command, later
// pushes and resumes only the ones set to run on every push.
func seedApplies(mode, runOn string) bool {
	return mode == "create" || runOn == RunOnPush
}
