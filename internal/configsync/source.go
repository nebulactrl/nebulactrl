package configsync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/projectconfig"
	"github.com/nebulactrl/nebulactrl/internal/services"
)

func (s *Service) loadScopeInternal(ctx context.Context, id uuid.UUID) (services.Scope, error) {
	return services.LoadScopeInternal(ctx, s.q, id)
}

// appliedByLabel is how a source row names who applied it.
func appliedByLabel(row db.ConfigSource) string {
	if row.Origin == OriginGit && row.CommitSha != "" {
		sha := row.CommitSha
		if len(sha) > 7 {
			sha = sha[:7]
		}
		return "nebula.toml @" + sha
	}
	return row.AppliedByLabel
}

// ServiceSource reports whether a service is managed by a file and how far it drifted. It reads no file:
// the drift of each owned field is the stored base against the live value, and a newer file is the
// desired fingerprint of the last plan stored with the environment.
func (s *Service) ServiceSource(ctx context.Context, serviceID uuid.UUID) (ServiceConfigSource, error) {
	sc, err := services.LoadScope(ctx, s.q, policy.ScopeProjectsRead, serviceID)
	if err != nil {
		return ServiceConfigSource{}, err
	}
	return s.serviceSource(ctx, s.q, sc)
}

func (s *Service) serviceSource(ctx context.Context, q *db.Queries, sc services.Scope) (ServiceConfigSource, error) {
	out := ServiceConfigSource{Fields: []FieldState{}, Pending: []string{}}
	row, err := q.GetConfigSource(ctx, sc.Service.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, nil
	}
	if err != nil {
		return out, fmt.Errorf("get config source: %w", err)
	}
	owned := map[string]string{}
	_ = json.Unmarshal(row.Owned, &owned)
	pending := map[string]json.RawMessage{}
	_ = json.Unmarshal(row.Pending, &pending)
	at := row.AppliedAt
	out.Managed, out.Origin, out.Repo, out.Branch, out.Path, out.Commit = true, row.Origin, row.RepoFullName, row.Branch, row.Path, row.CommitSha
	out.FileSHA256, out.AppliedAt, out.AppliedBy = row.FileSha256, &at, appliedByLabel(row)
	for id := range pending {
		out.Pending = append(out.Pending, id)
	}
	sort.Strings(out.Pending)

	env, err := services.LoadEnvironment(ctx, q, policy.ScopeProjectsRead, sc.EnvironmentID)
	if err != nil {
		return out, err
	}
	live, err := loadLive(ctx, q, s.Box, env)
	if err != nil {
		return out, err
	}
	ls := live.Services[sc.Service.Slug]
	if ls == nil {
		return out, nil
	}
	var lastDesired map[string]string
	if set := live.Settings; set != nil && len(set.LastPlan) > 0 {
		var lp LastPlan
		if json.Unmarshal(set.LastPlan, &lp) == nil {
			lastDesired = lp.Services[sc.Service.Slug].Fields
		}
	}
	pc := &planCtx{ctx: ctx, q: q, box: s.Box, live: live, env: env}
	states := []string{}
	for _, id := range sortedKeys(owned) {
		if isAux(id) {
			continue
		}
		ops := opsFor(id)
		if ops == nil {
			continue
		}
		lv, present := ops.live(pc, ls, id, owned[id+auxSuffix])
		file := owned[id]
		if d, ok := lastDesired[id]; ok {
			file = d
		}
		st := classify(owned[id], true, lv.FP, present, file)
		if st == StateNew {
			st = StateDrift // a member removed in the dashboard
		}
		out.Fields = append(out.Fields, FieldState{Field: id, State: st})
		states = append(states, st)
	}
	out.State = serviceStateOf(states)
	if len(states) == 0 {
		out.State = StateInSync
	}
	return out, nil
}

// ReleaseFields stops the file owning the given fields of a service; nothing else changes.
func (s *Service) ReleaseFields(ctx context.Context, serviceID uuid.UUID, fieldIDs []string) (ServiceConfigSource, error) {
	ctx = audit.WithVia(ctx, viaFile)
	sc, err := services.LoadScope(ctx, s.q, policy.ScopeProjectsWrite, serviceID)
	if err != nil {
		return ServiceConfigSource{}, err
	}
	err = db.InTx(ctx, s.Pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		row, err := q.GetConfigSourceForUpdate(ctx, serviceID)
		if errors.Is(err, pgx.ErrNoRows) {
			return apierr.Unprocessable(CodeFieldNotOwned, "this service is not managed by a nebula.toml")
		}
		if err != nil {
			return err
		}
		owned := map[string]string{}
		_ = json.Unmarshal(row.Owned, &owned)
		pending := map[string]json.RawMessage{}
		_ = json.Unmarshal(row.Pending, &pending)
		var unknown []string
		for _, id := range fieldIDs {
			if _, ok := owned[id]; !ok || isAux(id) {
				unknown = append(unknown, id)
			}
		}
		if len(unknown) > 0 {
			return apierr.Unprocessable(CodeFieldNotOwned, "the file does not own: "+strings.Join(unknown, ", ")).
				WithDetails(map[string]any{"fields": unknown})
		}
		for _, id := range fieldIDs {
			dropOwned(owned, id)
			delete(pending, id)
		}
		pj, _ := json.Marshal(pending)
		if err := q.SetConfigSourceOwned(ctx, db.SetConfigSourceOwnedParams{ServiceID: serviceID, Owned: ownedJSON(owned), Pending: pj, AppliedGeneration: row.AppliedGeneration}); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: "config.release", ResourceType: "service", ResourceID: serviceID.String(), ProjectID: &sc.ProjectID,
			Metadata: map[string]any{"service": sc.Service.Slug, "fields": fieldIDs},
		})
	})
	if err != nil {
		return ServiceConfigSource{}, err
	}
	services.Publish(s.Bus, evConfigUpdated, sc.ProjectID, sc.EnvironmentID, sc.EnvironmentID)
	return s.serviceSource(ctx, s.q, sc)
}

// RunPending runs the immediate steps of a service that could not run yet (they are stored with their
// desired values, which carry no secrets). A step that succeeds becomes owned and leaves the record.
func (s *Service) RunPending(ctx context.Context, serviceID uuid.UUID) error {
	sc, err := services.LoadScopeInternal(ctx, s.q, serviceID)
	if err != nil {
		return err
	}
	row, err := s.q.GetConfigSource(ctx, serviceID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	pending := map[string]json.RawMessage{}
	_ = json.Unmarshal(row.Pending, &pending)
	if len(pending) == 0 {
		return nil
	}
	ctx = audit.WithVia(auth.WithPrincipal(ctx, auth.System(sc.WorkspaceID, sc.ProjectID, "nebula.toml (deferred)",
		policy.ScopeProjectsRead, policy.ScopeProjectsWrite, policy.ScopeDeploymentsWrite)), viaFile)
	env, err := services.LoadEnvironment(ctx, s.q, policy.ScopeProjectsRead, sc.EnvironmentID)
	if err != nil {
		return err
	}
	live, err := loadLive(ctx, s.q, s.Box, env)
	if err != nil {
		return err
	}
	ls := live.Services[sc.Service.Slug]
	if ls == nil {
		return nil
	}
	pc := &planCtx{ctx: ctx, q: s.q, box: s.Box, live: live, env: env}
	var errs []error
	for _, id := range sortedKeys(pending) {
		switch {
		case strings.HasPrefix(id, "backups."):
			var v projectconfig.BackupValue
			if err := json.Unmarshal(pending[id], &v); err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", id, err))
				continue
			}
			fv := projectconfig.FieldValue{ID: id, Value: v, Applies: AppliesImmediate}
			d, err := opsFor(id).desired(pc, ls, fv)
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", id, err))
				continue
			}
			if err := s.applyBackup(ctx, ls, serviceID, v, live.Refs); err != nil {
				if !isDeferrable(err) {
					errs = append(errs, fmt.Errorf("%s: %w", id, err))
				}
				continue
			}
			if err := s.markOwned(ctx, serviceID, []*fieldPlan{{fv: fv, desired: d}}, nil, nil, []string{id}); err != nil {
				errs = append(errs, err)
			}
		default:
			errs = append(errs, fmt.Errorf("%s: no replay for this field", id))
		}
	}
	return errors.Join(errs...)
}

// RunPendingLoop replays pending steps whenever a deployment succeeds. It returns when ctx is done.
func (s *Service) RunPendingLoop(ctx context.Context) {
	if s.Bus == nil || s.q == nil {
		return
	}
	for ev := range s.Bus.Subscribe(ctx) {
		if ev.Type != services.EventDeploymentUpdated {
			continue
		}
		depID, err := uuid.Parse(ev.ResourceID)
		if err != nil {
			continue
		}
		serviceID, err := s.q.GetSucceededDeploymentService(ctx, depID)
		if err != nil {
			continue
		}
		if err := s.RunPending(ctx, serviceID); err != nil {
			s.Logger.Warn("nebula.toml: pending steps failed", "service", serviceID, "error", err)
		}
	}
}
