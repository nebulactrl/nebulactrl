package combine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/prodguard"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/internal/varexpr"
)

// jobMaxAttempts bounds River's retries of one step of the saga; the snoozes
// that wait for a deployment do not count.
const jobMaxAttempts = 12

// Start stages a combine and queues its deployment. It returns once the staged
// changes are committed: the sources are untouched and keep serving.
func (s *Service) Start(ctx context.Context, envID uuid.UUID, req CombineRequest) (CombineView, error) {
	env, err := services.LoadEnvironment(ctx, s.q, policy.ScopeProjectsWrite, envID)
	if err != nil {
		return CombineView{}, err
	}
	if err := requireWrite(ctx, &env.Env.ProjectID); err != nil {
		return CombineView{}, err
	}
	names, err := parseNames(req)
	if err != nil {
		return CombineView{}, err
	}
	// A first look outside the transaction: the production guard asks about the
	// reviewed plan, and a stale or blocked start fails before anything locks.
	a, err := s.analyze(ctx, s.q, env, req.ServiceIDs, &req.TargetServiceID, names, req.DefaultProcessName)
	if err != nil {
		return CombineView{}, err
	}
	if err := s.check(a, req, names); err != nil {
		return CombineView{}, err
	}
	if err := s.guardStart(ctx, env, a, req); err != nil {
		return CombineView{}, err
	}

	var (
		row    db.ServiceCombine
		afters []func(context.Context)
	)
	err = db.InTx(ctx, s.Pool, func(tx pgx.Tx) error {
		afters = nil
		q := db.New(tx)
		ids := slices.Clone(req.ServiceIDs)
		slices.SortFunc(ids, func(x, y uuid.UUID) int { return strings.Compare(x.String(), y.String()) })
		for _, id := range ids {
			if _, err := q.LockServiceRow(ctx, id); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return apierr.NotFound("service")
				}
				return fmt.Errorf("lock service: %w", err)
			}
		}
		// Everything is judged again under the locks.
		a, err := s.analyze(ctx, q, env, req.ServiceIDs, &req.TargetServiceID, names, req.DefaultProcessName)
		if err != nil {
			return err
		}
		if err := s.check(a, req, names); err != nil {
			return err
		}
		stored, err := s.stage(ctx, tx, q, a, req, names, &afters)
		if err != nil {
			return err
		}
		raw, err := json.Marshal(stored)
		if err != nil {
			return err
		}
		sources := make([]uuid.UUID, len(a.sources))
		for i, m := range a.sources {
			sources[i] = m.id()
		}
		row, err = q.InsertCombine(ctx, db.InsertCombineParams{
			EnvironmentID: env.Env.ID, TargetServiceID: a.target.id(), SourceServiceIds: sources, Plan: raw, CreatedBy: actorLabel(ctx),
		})
		if err != nil {
			return err
		}
		if err := s.Jobs.InsertTx(ctx, tx, Args{CombineID: row.ID}, &river.InsertOpts{MaxAttempts: jobMaxAttempts}); err != nil {
			return fmt.Errorf("enqueue combine job: %w", err)
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: auditAction, ResourceType: "service", ResourceID: a.target.id().String(), ProjectID: &env.Env.ProjectID, WorkspaceID: env.WorkspaceID,
			Metadata: map[string]any{"combine": row.ID.String(), "state": StateStaged, "target": a.target.sc.Service.Slug, "sources": stored.SourceSlugs},
		})
	})
	if err != nil {
		if services.IsUniqueViolation(err) {
			return CombineView{}, apierr.Conflict(CodeCombineInProgress, "the target is already part of a combine that is not finished")
		}
		return CombineView{}, err
	}
	for _, f := range afters {
		f(ctx)
	}
	s.publish(env, a.target.id(), req.ServiceIDs)
	s.stopPlanned(ctx, row, env.WorkspaceID, env.Env.ProjectID)
	return viewOf(row), nil
}

// auditAction is the audit action of every step of a combine.
const auditAction = "service.combine"

// actorLabel names the caller for service_combines.created_by.
func actorLabel(ctx context.Context) string {
	if p, ok := auth.PrincipalFrom(ctx); ok {
		return p.Label
	}
	return ""
}

// parseNames reads the process names of the request: one per source, valid and
// distinct.
func parseNames(req CombineRequest) (map[uuid.UUID]string, error) {
	out := map[uuid.UUID]string{}
	seen := map[string]bool{}
	def := req.DefaultProcessName
	if def != "" {
		if !services.ProcessNamePattern.MatchString(def) {
			return nil, apierr.Unprocessable(CodeInvalidProcessNames, fmt.Sprintf("defaultProcessName %q is not a valid process name", def))
		}
		seen[def] = true
	}
	for k, n := range req.ProcessNames {
		id, err := uuid.Parse(k)
		if err != nil {
			return nil, apierr.Unprocessable(CodeInvalidProcessNames, fmt.Sprintf("processNames key %q is not a service id", k))
		}
		if !services.ProcessNamePattern.MatchString(n) {
			return nil, apierr.Unprocessable(CodeInvalidProcessNames, fmt.Sprintf("%q is not a valid process name: lowercase letters, digits and '-', at most 31 characters", n))
		}
		if seen[n] {
			return nil, apierr.Unprocessable(CodeInvalidProcessNames, fmt.Sprintf("the process name %q is used twice", n))
		}
		seen[n] = true
		out[id] = n
	}
	return out, nil
}

// check refuses a start whose plan is stale or blocked, or whose request does
// not cover the plan (a process name per source, a choice per conflict).
func (s *Service) check(a *analysis, req CombineRequest, names map[uuid.UUID]string) error {
	if a.plan.Fingerprint != req.Fingerprint {
		return apierr.Conflict(CodePlanStale, "the services changed since the plan was made: review the new plan and start again")
	}
	for _, m := range a.sources {
		n, ok := names[m.id()]
		if !ok {
			return apierr.Unprocessable(CodeInvalidProcessNames, fmt.Sprintf("processNames has no entry for %s (%s)", m.sc.Service.Slug, m.id())).
				WithDetails(map[string]any{"serviceId": m.id()})
		}
		if n == a.target.cfg.DefaultProcessName() && req.DefaultProcessName == "" {
			return apierr.Unprocessable(CodeInvalidProcessNames, fmt.Sprintf("the process name %q is the name of the target's own process: choose another, or rename that process with defaultProcessName", n))
		}
	}
	for id := range names {
		if !slices.ContainsFunc(a.sources, func(m *member) bool { return m.id() == id }) {
			return apierr.Unprocessable(CodeInvalidProcessNames, fmt.Sprintf("processNames names %s, which is not a source of this combine", id))
		}
	}
	if len(a.plan.Blocking) > 0 {
		return blockerError(a.plan.Blocking)
	}
	var missing []string
	for _, c := range a.plan.Variables.Conflicts {
		choice, ok := req.VariableChoices[c.Key]
		if !ok || !validChoice(a, c.Key, choice) {
			missing = append(missing, c.Key)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return apierr.Unprocessable(CodeVariableChoiceRequired, "choose which service's value to keep for: "+strings.Join(missing, ", ")).
			WithDetails(map[string]any{"keys": missing})
	}
	return nil
}

// validChoice reports whether choice is a member that holds the key.
func validChoice(a *analysis, key, choice string) bool {
	id, err := uuid.Parse(choice)
	if err != nil {
		return false
	}
	ki := a.keys[key]
	return ki != nil && slices.ContainsFunc(ki.holders, func(h keyHolder) bool { return h.m.id() == id })
}

// blockerError is the error of the first blocker; every blocker is in details.
func blockerError(all []CombineBlocker) error {
	b := all[0]
	status := 422
	switch b.Code {
	case CodeDeploymentInProgress, CodeCombineInProgress:
		status = 409
	}
	e := apierr.New(status, b.Code, b.Message)
	d := map[string]any{"blocking": all}
	if len(b.Fields) > 0 {
		d["fields"] = b.Fields
	}
	if len(b.References) > 0 {
		d["references"] = b.References
	}
	return e.WithDetails(d)
}

// guardStart asks the production guard for the whole merge: it deletes services.
func (s *Service) guardStart(ctx context.Context, env services.EnvScope, a *analysis, req CombineRequest) error {
	if !prodguard.Applies(ctx, env.Env.IsProduction) {
		return nil
	}
	targets := make([]prodguard.ApprovalTarget, 0, len(a.all))
	for _, m := range a.all {
		targets = append(targets, prodguard.ApprovalTarget{Kind: "service", ID: m.id(), Name: m.sc.Service.Name, Generation: m.sc.Service.Generation})
	}
	srcNames := make([]string, len(a.sources))
	for i, m := range a.sources {
		srcNames[i] = m.sc.Service.Name
	}
	effects := []string{
		fmt.Sprintf("Stages one process per merged service on %s and deploys it; the merged services keep serving until the new release is up.", a.target.sc.Service.Name),
		"Then moves their domains and scheduled tasks to " + a.target.sc.Service.Name + " and deletes " + strings.Join(srcNames, ", ") + ".",
		"Their volumes are kept, but staged changes of the merged services are lost.",
	}
	pairs := make([]string, 0, len(req.ProcessNames)+len(req.VariableChoices))
	for k, v := range req.ProcessNames {
		pairs = append(pairs, "p:"+k+"="+v)
	}
	for k, v := range req.VariableChoices {
		pairs = append(pairs, "v:"+k+"="+v)
	}
	sort.Strings(pairs)
	summary := fmt.Sprintf("Combine %d services of production into %s", len(a.all), a.target.sc.Service.Name)
	return prodguard.Check(ctx, env.ProdScope(summary, true, targets, effects...), guardOp,
		prodguard.DigestIDs(guardOp, req.ServiceIDs, req.TargetServiceID, req.Fingerprint, req.DefaultProcessName, req.StopSourcesFirst, strings.Join(pairs, ",")))
}

// stage writes the combine onto the target (step S1). The sources are not
// modified: a failure rolls the whole transaction back and leaves no trace.
func (s *Service) stage(ctx context.Context, tx pgx.Tx, q *db.Queries, a *analysis, req CombineRequest, names map[uuid.UUID]string, afters *[]func(context.Context)) (storedPlan, error) {
	t := a.target
	defName := t.cfg.DefaultProcessName()
	if req.DefaultProcessName != "" {
		defName = req.DefaultProcessName
	}
	stored := storedPlan{
		Request: req, ProcessNames: map[string]string{}, DefaultProcessName: defName, TargetSlug: t.sc.Service.Slug,
		SourceSlugs: map[string]string{}, DomainMoves: a.plan.DomainMoves, TaskMoves: a.plan.TaskMoves,
	}
	for _, m := range a.sources {
		stored.ProcessNames[m.id().String()] = names[m.id()]
		stored.SourceSlugs[m.id().String()] = m.sc.Service.Slug
	}

	// a. The configuration: one process per source.
	cfg := mergedConfig(a, req.DefaultProcessName)
	tsc, err := services.LoadScope(ctx, q, policy.ScopeProjectsWrite, t.id())
	if err != nil {
		return stored, err
	}
	row, after, err := s.Services.UpdateTx(ctx, tx, tsc, tsc.Service.Generation, services.UpdateInput{Config: &cfg})
	if err != nil {
		return stored, err
	}
	*afters = append(*afters, after)
	tsc.Service = row
	for _, p := range cfg.Processes {
		stored.StagedFields = append(stored.StagedFields, "config.processes."+p.Name)
	}
	if req.DefaultProcessName != "" && req.DefaultProcessName != t.cfg.ProcessName {
		stored.StagedFields = append(stored.StagedFields, "config.processName")
	}

	// b. The variables: the chosen value of every key, references rewritten.
	fields, overwritten, backups, err := s.stageVariables(ctx, q, a, req, tsc)
	if err != nil {
		return stored, err
	}
	stored.StagedFields = append(stored.StagedFields, fields...)
	stored.VariableBackups = backups
	stored.OriginalProcessName = t.cfg.ProcessName
	if len(fields) > 0 {
		if _, err := q.BumpServiceGeneration(ctx, t.id()); err != nil {
			return stored, fmt.Errorf("stage target: %w", err)
		}
	}
	// A service that references a target variable whose value changed now has
	// a staged change of its own.
	for dep, keys := range a.dependents {
		if slices.ContainsFunc(keys, func(k string) bool { return overwritten[k] }) {
			if _, err := q.BumpServiceGeneration(ctx, dep); err != nil {
				return stored, fmt.Errorf("stage dependent: %w", err)
			}
		}
	}

	// c. The scale settings of the sources, for processes that are not deployed yet.
	for _, m := range a.sources {
		if err := stageScale(ctx, q, t.id(), names[m.id()], m.sc.Service); err != nil {
			return stored, err
		}
	}

	// d. Sources that stop first: only those that serve no domain, and that run.
	if req.StopSourcesFirst {
		for _, m := range a.sources {
			if len(m.domains) == 0 && m.sc.Service.CurrentDeploymentID != nil && m.sc.Service.DesiredState == services.DesiredRunning {
				stored.StoppedServiceIDs = append(stored.StoppedServiceIDs, m.id())
			}
		}
	}
	sort.Strings(stored.StagedFields)
	return stored, nil
}

// stageVariables writes, on the target, the winning value of every variable the
// target does not already hold in that form. fields are the pending-change
// fields it created; overwritten are the keys whose existing value changed.
func (s *Service) stageVariables(ctx context.Context, q *db.Queries, a *analysis, req CombineRequest, tsc services.Scope) (fields []string, overwritten map[string]bool, backups map[string]variableBackup, err error) {
	overwritten = map[string]bool{}
	backups = map[string]variableBackup{}
	keys := make([]string, 0, len(a.keys))
	for k := range a.keys {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		ki := a.keys[k]
		win := ki.holders[0]
		if len(ki.identities) > 1 {
			id, _ := uuid.Parse(req.VariableChoices[k])
			for _, h := range ki.holders {
				if h.m.id() == id {
					win = h
					break
				}
			}
		}
		w := services.VarWrite{Key: k, Value: win.value, Secret: win.row.row.IsSecret, Scope: win.row.row.Scope, Source: win.row.row.Source}
		comment := win.row.row.Comment
		w.Comment = &comment
		if r := win.row.row.RotateEveryDays; r != nil {
			n := int(*r)
			w.RotateEveryDays = &n
		}
		if win.row.template != "" {
			w.Template = win.tmpl
			if len(win.row.row.GenCalls) > 0 {
				var calls []varexpr.GeneratorCall
				if err := json.Unmarshal(win.row.row.GenCalls, &calls); err == nil {
					w.GenCalls = calls
				}
			}
		}
		backup, err := s.backupVariable(ctx, q, tsc, k)
		if err != nil {
			return nil, nil, nil, err
		}
		res, err := services.WriteServiceVariable(ctx, q, s.Box, tsc, w)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("stage variable %s: %w", k, err)
		}
		if !res.Changed {
			continue
		}
		backups[k] = backup
		fields = append(fields, "variables."+k)
		if res.HadOld && res.OldValue != w.Value {
			overwritten[k] = true
		}
	}
	return fields, overwritten, backups, nil
}

// variableBackup is how a target variable looked before the combine wrote it,
// so an abort of a target that was never deployed (no release to revert to)
// can put it back. Values are sealed: the stored plan holds no plaintext.
type variableBackup struct {
	Existed         bool            `json:"existed"`
	Secret          bool            `json:"secret,omitempty"`
	Scope           string          `json:"scope,omitempty"`
	Source          string          `json:"source,omitempty"`
	Comment         string          `json:"comment,omitempty"`
	Value           []byte          `json:"value,omitempty"`
	Template        []byte          `json:"template,omitempty"`
	GenCalls        json.RawMessage `json:"genCalls,omitempty"`
	RotateEveryDays *int32          `json:"rotateEveryDays,omitempty"`
}

func backupRowID(serviceID uuid.UUID, key string) string {
	return secretbox.RowID(serviceID.String(), key, "combine-backup")
}

// backupVariable records the current state of one target variable.
func (s *Service) backupVariable(ctx context.Context, q *db.Queries, tsc services.Scope, key string) (variableBackup, error) {
	id := tsc.Service.ID
	cur, err := q.GetServiceVariable(ctx, db.GetServiceVariableParams{ServiceID: id, Key: key})
	if errors.Is(err, pgx.ErrNoRows) {
		return variableBackup{}, nil
	}
	if err != nil {
		return variableBackup{}, fmt.Errorf("read variable %s: %w", key, err)
	}
	val, err := s.Box.OpenString(secretbox.ServiceVariablesValue, secretbox.RowID(id.String(), key), cur.ValueEnc)
	if err != nil {
		return variableBackup{}, fmt.Errorf("open variable %s: %w", key, err)
	}
	b := variableBackup{
		Existed: true, Secret: cur.IsSecret, Scope: cur.Scope, Source: cur.Source, Comment: cur.Comment,
		GenCalls: cur.GenCalls, RotateEveryDays: cur.RotateEveryDays,
	}
	if b.Value, err = s.Box.SealString(secretbox.ServiceVariablesValue, backupRowID(id, key), val); err != nil {
		return variableBackup{}, err
	}
	if len(cur.TemplateEnc) > 0 {
		tmpl, err := s.Box.OpenString(secretbox.ServiceVariablesTemplate, secretbox.RowID(id.String(), key), cur.TemplateEnc)
		if err != nil {
			return variableBackup{}, fmt.Errorf("open template of %s: %w", key, err)
		}
		if b.Template, err = s.Box.SealString(secretbox.ServiceVariablesTemplate, backupRowID(id, key), tmpl); err != nil {
			return variableBackup{}, err
		}
	}
	return b, nil
}

// stageScale gives the new process the replicas and autoscaling its source had.
// They are immediate settings that take effect once the process is deployed.
func stageScale(ctx context.Context, q *db.Queries, target uuid.UUID, process string, src db.Service) error {
	cur, err := q.EnsureProcessScale(ctx, db.EnsureProcessScaleParams{ServiceID: target, Process: process})
	if err != nil {
		return fmt.Errorf("stage scale of %s: %w", process, err)
	}
	if src.Replicas != cur.Replicas && src.Replicas >= 1 {
		if _, err := q.ApplyProcessScale(ctx, db.ApplyProcessScaleParams{ServiceID: target, Process: process, Replicas: src.Replicas, ExpectedReplicas: cur.Replicas}); err != nil {
			return fmt.Errorf("stage replicas of %s: %w", process, err)
		}
	}
	var as services.Autoscaling
	if len(src.Autoscaling) > 0 && json.Unmarshal(src.Autoscaling, &as) == nil && (as.Enabled || as != services.DefaultAutoscaling()) {
		raw, _ := json.Marshal(as)
		if _, err := q.SetProcessAutoscaling(ctx, db.SetProcessAutoscalingParams{ServiceID: target, Process: process, Autoscaling: raw}); err != nil {
			return fmt.Errorf("stage autoscaling of %s: %w", process, err)
		}
	}
	return nil
}

// stopPlanned stops the sources the plan stops first. It is idempotent: a
// source that is stopped already, or has nothing running, is skipped.
func (s *Service) stopPlanned(ctx context.Context, c db.ServiceCombine, workspaceID, projectID uuid.UUID) {
	var sp storedPlan
	if json.Unmarshal(c.Plan, &sp) != nil {
		return
	}
	ctx = prodguard.Covered(systemCtx(ctx, workspaceID, projectID))
	for _, id := range sp.StoppedServiceIDs {
		sc, err := services.LoadScopeInternal(ctx, s.q, id)
		if err != nil || sc.Service.DesiredState == services.DesiredStopped || sc.Service.CurrentDeploymentID == nil {
			continue
		}
		if _, err := s.Services.Stop(ctx, id); err != nil {
			s.Logger.WarnContext(ctx, "combine: stopping a source failed", "combine", c.ID, "service", id, "err", err)
		}
	}
}

// restartStopped starts again the sources the combine stopped.
func (s *Service) restartStopped(ctx context.Context, c db.ServiceCombine, workspaceID, projectID uuid.UUID) {
	var sp storedPlan
	if json.Unmarshal(c.Plan, &sp) != nil {
		return
	}
	ctx = prodguard.Covered(systemCtx(ctx, workspaceID, projectID))
	for _, id := range sp.StoppedServiceIDs {
		sc, err := services.LoadScopeInternal(ctx, s.q, id)
		if err != nil || sc.Service.DesiredState != services.DesiredStopped {
			continue
		}
		if _, err := s.Services.Restart(ctx, id); err != nil {
			s.Logger.WarnContext(ctx, "combine: starting a source again failed", "combine", c.ID, "service", id, "err", err)
		}
	}
}

// publish tells the live feed that the members changed.
func (s *Service) publish(env services.EnvScope, target uuid.UUID, members []uuid.UUID) {
	for _, id := range members {
		services.PublishIn(s.Bus, env.WorkspaceID, services.EventServiceUpdated, env.Env.ProjectID, env.Env.ID, id)
	}
	services.PublishIn(s.Bus, env.WorkspaceID, services.EventCanvasUpdated, env.Env.ProjectID, env.Env.ID, env.Env.ID)
}
