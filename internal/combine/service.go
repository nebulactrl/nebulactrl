package combine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/prodguard"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/services"
)

// load reads a combine and authorizes the caller for its environment (404
// outside the caller's team or project restriction).
func (s *Service) load(ctx context.Context, id uuid.UUID, scope string) (db.ServiceCombine, services.EnvScope, error) {
	c, err := s.q.GetCombine(ctx, id)
	if errNoRows(err) {
		return db.ServiceCombine{}, services.EnvScope{}, apierr.NotFound("combine")
	}
	if err != nil {
		return db.ServiceCombine{}, services.EnvScope{}, fmt.Errorf("get combine: %w", err)
	}
	env, err := services.LoadEnvironment(ctx, s.q, scope, c.EnvironmentID)
	if err != nil {
		if apiNotFound(err) {
			return db.ServiceCombine{}, services.EnvScope{}, apierr.NotFound("combine")
		}
		return db.ServiceCombine{}, services.EnvScope{}, err
	}
	return c, env, nil
}

// Get returns a combine.
func (s *Service) Get(ctx context.Context, id uuid.UUID) (CombineView, error) {
	c, _, err := s.load(ctx, id, policy.ScopeProjectsRead)
	if err != nil {
		return CombineView{}, err
	}
	return viewOf(c), nil
}

func invalidState(c db.ServiceCombine, what string) error {
	return apierr.Conflict(CodeInvalidState, fmt.Sprintf("a combine that is %s cannot be %s", c.State, what)).
		WithDetails(map[string]any{"state": c.State})
}

// Abort takes a staged or failed combine back (step S5): the sources that were
// stopped start again, the changes the combine staged on the target are
// discarded and the scale settings it created are removed. It is refused while
// the deployment or the cutover runs, and once the sources are gone.
func (s *Service) Abort(ctx context.Context, id uuid.UUID) (CombineView, error) {
	_, env, err := s.load(ctx, id, policy.ScopeProjectsWrite)
	if err != nil {
		return CombineView{}, err
	}
	if err := requireWrite(ctx, &env.Env.ProjectID); err != nil {
		return CombineView{}, err
	}
	var row db.ServiceCombine
	var afters []func(context.Context)
	err = db.InTx(ctx, s.Pool, func(tx pgx.Tx) error {
		afters = nil
		q := db.New(tx)
		// The combine row is locked for the whole abort: the job's deployment step
		// waits for it and then finds the combine aborted.
		cur, err := q.LockCombine(ctx, id)
		if err != nil {
			return err
		}
		if cur.State != StateStaged && cur.State != StateFailed {
			return invalidState(cur, "aborted")
		}
		var sp storedPlan
		if err := json.Unmarshal(cur.Plan, &sp); err != nil {
			return fmt.Errorf("read the stored plan: %w", err)
		}
		tsc, err := services.LoadScopeInternal(ctx, q, cur.TargetServiceID)
		if err != nil {
			return err
		}
		if ok, err := s.releaseRunsProcesses(ctx, q, tsc.Service, sp); err != nil {
			return err
		} else if ok {
			return apierr.Conflict(CodeInvalidState, "the target's release already runs every process: retry the combine to finish it, or roll the target back").
				WithDetails(map[string]any{"state": cur.State})
		}
		if _, deployed, err := services.AppliedRelease(ctx, q, tsc.Service); err != nil {
			return err
		} else if deployed {
			if err := s.Deployments.DiscardFields(ctx, cur.TargetServiceID, sp.StagedFields); err != nil {
				return err
			}
		} else {
			// Never deployed: discarding reverts to a release, and there is none.
			after, err := s.unstage(ctx, tx, q, cur.TargetServiceID, sp)
			if err != nil {
				return err
			}
			afters = append(afters, after)
		}
		// What is left of the target's config names the processes whose scale rows stay.
		fresh, err := services.LoadScopeInternal(ctx, q, cur.TargetServiceID)
		if err != nil {
			return err
		}
		keep := []string{}
		for _, p := range fresh.Config().Processes {
			keep = append(keep, p.Name)
		}
		if err := q.PruneProcessScales(ctx, db.PruneProcessScalesParams{ServiceID: cur.TargetServiceID, Keep: keep}); err != nil {
			return fmt.Errorf("remove the scale settings of the combine: %w", err)
		}
		s.restartStopped(ctx, cur, tsc.WorkspaceID, tsc.ProjectID)
		row, err = q.AdvanceCombine(ctx, db.AdvanceCombineParams{ID: id, FromState: cur.State, ToState: StateAborted})
		if err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: auditAction, ResourceType: "service", ResourceID: cur.TargetServiceID.String(), ProjectID: &env.Env.ProjectID, WorkspaceID: env.WorkspaceID,
			Metadata: map[string]any{"combine": id.String(), "state": StateAborted, "target": sp.TargetSlug, "discarded": sp.StagedFields},
		})
	})
	if err != nil {
		return CombineView{}, err
	}
	for _, f := range afters {
		f(ctx)
	}
	s.publish(env, append([]uuid.UUID{row.TargetServiceID}, row.SourceServiceIds...))
	return viewOf(row), nil
}

// unstage takes exactly what the start staged off a target that has no release:
// the combine's processes, the default process name and the variables it wrote
// (put back as they were, or deleted when they did not exist), through the
// service update path so generation and diffs stay consistent.
func (s *Service) unstage(ctx context.Context, tx pgx.Tx, q *db.Queries, targetID uuid.UUID, sp storedPlan) (func(context.Context), error) {
	noop := func(context.Context) {}
	sc, err := services.LoadScope(ctx, q, policy.ScopeProjectsWrite, targetID)
	if err != nil {
		return noop, err
	}
	cfg := sc.Config()
	added := map[string]bool{}
	for _, n := range sp.ProcessNames {
		added[n] = true
	}
	kept := cfg.Processes[:0:0]
	for _, p := range cfg.Processes {
		if !added[p.Name] {
			kept = append(kept, p)
		}
	}
	changed := len(kept) != len(cfg.Processes)
	cfg.Processes = kept
	if cfg.ProcessName == sp.DefaultProcessName && sp.DefaultProcessName != sp.OriginalProcessName {
		cfg.ProcessName, changed = sp.OriginalProcessName, true
	}
	after := noop
	if changed {
		row, a, err := s.Services.UpdateTx(ctx, tx, sc, sc.Service.Generation, services.UpdateInput{Config: &cfg})
		if err != nil {
			return noop, err
		}
		after, sc.Service = a, row
	}
	restored := false
	for key, b := range sp.VariableBackups {
		if !b.Existed {
			if _, _, err := services.DeleteServiceVariableVersioned(ctx, q, s.Box, sc, key); err != nil {
				return noop, fmt.Errorf("remove variable %s: %w", key, err)
			}
			restored = true
			continue
		}
		val, err := s.Box.OpenString(secretbox.ServiceVariablesValue, backupRowID(targetID, key), b.Value)
		if err != nil {
			return noop, fmt.Errorf("open the saved value of %s: %w", key, err)
		}
		w := services.VarWrite{Key: key, Value: val, Secret: b.Secret, Scope: b.Scope, Source: b.Source, Comment: &b.Comment, Op: services.OpRestore}
		if b.RotateEveryDays != nil {
			n := int(*b.RotateEveryDays)
			w.RotateEveryDays = &n
		}
		if len(b.Template) > 0 {
			if w.Template, err = s.Box.OpenString(secretbox.ServiceVariablesTemplate, backupRowID(targetID, key), b.Template); err != nil {
				return noop, fmt.Errorf("open the saved template of %s: %w", key, err)
			}
			if len(b.GenCalls) > 0 {
				_ = json.Unmarshal(b.GenCalls, &w.GenCalls)
			}
		}
		if res, err := services.WriteServiceVariable(ctx, q, s.Box, sc, w); err != nil {
			return noop, fmt.Errorf("restore variable %s: %w", key, err)
		} else if res.Changed {
			restored = true
		}
	}
	if restored {
		if _, err := q.BumpServiceGeneration(ctx, targetID); err != nil {
			return noop, fmt.Errorf("restage target: %w", err)
		}
	}
	return after, nil
}

// Retry runs the deployment of a failed combine again for the same staged
// configuration. When the target's release already runs every process (the
// cutover failed), the job skips the deployment and finishes the cutover.
func (s *Service) Retry(ctx context.Context, id uuid.UUID) (CombineView, error) {
	c, env, err := s.load(ctx, id, policy.ScopeProjectsWrite)
	if err != nil {
		return CombineView{}, err
	}
	if err := requireWrite(ctx, &env.Env.ProjectID); err != nil {
		return CombineView{}, err
	}
	if c.State != StateFailed {
		return CombineView{}, invalidState(c, "retried")
	}
	if prodguard.Applies(ctx, env.Env.IsProduction) {
		names := []string{}
		targets := []prodguard.ApprovalTarget{}
		ids := append([]uuid.UUID{c.TargetServiceID}, c.SourceServiceIds...)
		rows, err := s.q.ListAllEnvironmentServices(ctx, c.EnvironmentID)
		if err != nil {
			return CombineView{}, fmt.Errorf("list services: %w", err)
		}
		for _, r := range rows {
			for _, id := range ids {
				if r.ID == id {
					names = append(names, r.Name)
					targets = append(targets, prodguard.ApprovalTarget{Kind: "service", ID: r.ID, Name: r.Name, Generation: r.Generation})
				}
			}
		}
		effects := []string{"Deploys the combined service again; the merged services are deleted once it runs every process."}
		if err := prodguard.Check(ctx, env.ProdScope("Retry combining "+strings.Join(names, ", ")+" in production", true, targets, effects...), guardOp,
			prodguard.Digest(guardOp, "retry", c.ID)); err != nil {
			return CombineView{}, err
		}
	}
	var row db.ServiceCombine
	err = db.InTx(ctx, s.Pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		cur, err := q.LockCombine(ctx, id)
		if err != nil {
			return err
		}
		if cur.State != StateFailed {
			return invalidState(cur, "retried")
		}
		if row, err = q.AdvanceCombine(ctx, db.AdvanceCombineParams{ID: id, FromState: StateFailed, ToState: StateStaged}); err != nil {
			return err
		}
		if err := s.Jobs.InsertTx(ctx, tx, Args{CombineID: id}, &river.InsertOpts{MaxAttempts: jobMaxAttempts}); err != nil {
			return fmt.Errorf("enqueue combine job: %w", err)
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: auditAction, ResourceType: "service", ResourceID: cur.TargetServiceID.String(), ProjectID: &env.Env.ProjectID, WorkspaceID: env.WorkspaceID,
			Metadata: map[string]any{"combine": id.String(), "state": StateStaged, "retry": true},
		})
	})
	if err != nil {
		return CombineView{}, err
	}
	s.publish(env, []uuid.UUID{row.TargetServiceID})
	return viewOf(row), nil
}
