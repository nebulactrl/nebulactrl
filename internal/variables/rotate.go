package variables

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/deployments"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/internal/varexpr"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Rotation kinds.
const (
	KindAppSecret   = "app-secret"
	KindEngineBound = "engine-bound"
)

// RotationAffected is a service whose environment changes when a variable
// rotates.
type RotationAffected struct {
	ServiceID uuid.UUID `json:"serviceId"`
	Slug      string    `json:"slug"`
	Via       []string  `json:"via" doc:"Keys of this service whose resolved value changes"`
	Strategy  string    `json:"strategy" enum:"rolling,recreate"`
	Downtime  string    `json:"downtime" enum:"none,brief"`
}

// RotationBlocker stops a rotation.
type RotationBlocker struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// VariableRotationPlan is the plan (and result) of rotating a generated variable.
type VariableRotationPlan struct {
	Key         string             `json:"key"`
	Kind        string             `json:"kind" enum:"app-secret,engine-bound"`
	Affected    []RotationAffected `json:"affected"`
	DeployOrder []string           `json:"deployOrder" doc:"Service slugs, providers first"`
	Warnings    []string           `json:"warnings"`
	Blockers    []RotationBlocker  `json:"blockers"`
	// Staged is true when the rotation was applied (as a staged change).
	Staged bool `json:"staged"`
}

var errDryRun = errors.New("dry run")

// engineBoundNote explains why database credentials are not rotated in place.
const engineBoundNote = "The database image reads this value only when it initialises an empty data directory; " +
	"changing it would desynchronise the database and its clients. Create a second role/user, move the clients to it, then drop the old one."

type varsOf struct {
	slug string
	vars map[string]string
}

func (s *Service) captureVars(ctx context.Context, q *db.Queries, infos []services.EnvInfo) (map[uuid.UUID]varsOf, error) {
	out := map[uuid.UUID]varsOf{}
	for _, info := range infos {
		res, err := services.LoadResolver(ctx, q, s.box, info)
		if err != nil {
			return nil, err
		}
		for _, slug := range res.Slugs() {
			r, _, err := res.ResolveServiceLenient(slug)
			if err != nil {
				return nil, err
			}
			sd, _ := res.Service(slug)
			out[sd.ID] = varsOf{slug: slug, vars: r.Vars}
		}
	}
	return out, nil
}

// applyAndDiff runs mutate inside the transaction and returns the services
// whose resolved variables changed (with the keys), bumping the generation of
// every one except skip.
func (s *Service) applyAndDiff(ctx context.Context, q *db.Queries, infos []services.EnvInfo, skip uuid.UUID, mutate func() error) ([]RotationAffected, error) {
	before, err := s.captureVars(ctx, q, infos)
	if err != nil {
		return nil, err
	}
	if err := mutate(); err != nil {
		return nil, err
	}
	after, err := s.captureVars(ctx, q, infos)
	if err != nil {
		return nil, err
	}
	var out []RotationAffected
	for id, a := range after {
		b := before[id]
		var via []string
		for k, v := range a.vars {
			if ov, ok := b.vars[k]; !ok || ov != v {
				via = append(via, k)
			}
		}
		for k := range b.vars {
			if _, ok := a.vars[k]; !ok {
				via = append(via, k)
			}
		}
		if len(via) == 0 {
			continue
		}
		sort.Strings(via)
		if id != skip {
			if _, err := q.BumpServiceGeneration(ctx, id); err != nil {
				return nil, err
			}
		}
		out = append(out, RotationAffected{ServiceID: id, Slug: a.slug, Via: via})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Slug < out[j].Slug })
	return out, nil
}

// finishPlan fills strategy, downtime and the deploy order.
func (s *Service) finishPlan(ctx context.Context, q *db.Queries, infos []services.EnvInfo, affected []RotationAffected) ([]RotationAffected, []string, error) {
	for i := range affected {
		asc, err := services.LoadScopeInternal(ctx, q, affected[i].ServiceID)
		if err != nil {
			return nil, nil, err
		}
		vols, err := q.ListServiceVolumes(ctx, asc.Service.ID)
		if err != nil {
			return nil, nil, err
		}
		st, _ := deployments.ChooseStrategy(asc.Config(), len(vols))
		affected[i].Strategy, affected[i].Downtime = st, "none"
		if st == agentprotocol.StrategyRecreate {
			affected[i].Downtime = "brief"
		}
	}
	ids := make([]uuid.UUID, len(affected))
	slugOf := map[uuid.UUID]string{}
	for i, a := range affected {
		ids[i] = a.ServiceID
		slugOf[a.ServiceID] = a.Slug
	}
	var edges []services.RefEdge
	for _, info := range infos {
		e, err := services.ReferenceEdges(ctx, q, s.box, info.ID)
		if err != nil {
			return nil, nil, err
		}
		edges = append(edges, e...)
	}
	order := []string{}
	for _, id := range deployments.OrderByReferences(ids, edges) {
		order = append(order, slugOf[id])
	}
	return affected, order, nil
}

// RotateVariable re-materialises a generated service variable. dryRun returns
// the plan without changing anything; otherwise the new value is staged (never
// deployed). Preset database credentials and volume-backed passwords are
// blocked.
func (s *Service) RotateVariable(ctx context.Context, serviceID uuid.UUID, key string, dryRun bool) (VariableRotationPlan, error) {
	sc, err := services.LoadScope(ctx, s.q, policy.ScopeProjectsWrite, serviceID)
	if err != nil {
		return VariableRotationPlan{}, err
	}
	plan := VariableRotationPlan{Key: key, Kind: KindAppSecret, Affected: []RotationAffected{}, DeployOrder: []string{}, Warnings: []string{}, Blockers: []RotationBlocker{}}
	var changes []changeEntry
	infos := []services.EnvInfo{sc.EnvInfo()}
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		if _, err := q.LockServiceRow(ctx, serviceID); err != nil {
			return apierr.NotFound("service")
		}
		row, err := q.GetServiceVariable(ctx, db.GetServiceVariableParams{ServiceID: serviceID, Key: key})
		if errors.Is(err, pgx.ErrNoRows) {
			return apierr.NotFound("variable")
		}
		if err != nil {
			return err
		}
		if row.Source != services.SourceGenerated || len(row.TemplateEnc) == 0 {
			return apierr.Unprocessable("not_rotatable", "only generated variables can be rotated; set a new value instead")
		}
		tmpl, err := s.box.OpenString(secretbox.ServiceVariablesTemplate, secretbox.RowID(row.ServiceID.String(), row.Key), row.TemplateEnc)
		if err != nil {
			return err
		}
		vols, err := q.ListServiceVolumes(ctx, serviceID)
		if err != nil {
			return err
		}
		if b := engineBound(sc, row, len(vols)); b != nil {
			plan.Kind = KindEngineBound
			// A database that was never deployed has no data directory yet:
			// its credentials can still be regenerated freely.
			if sc.Service.CurrentDeploymentID != nil || sc.Service.AppliedGeneration > 0 {
				plan.Blockers = append(plan.Blockers, *b)
			}
		}
		if len(plan.Blockers) > 0 {
			return errDryRun
		}
		affected, err := s.applyAndDiff(ctx, q, infos, serviceID, func() error {
			pw, err := s.prepareWriteForced(row, tmpl)
			if err != nil {
				return err
			}
			pw.w.Op = services.OpRotate
			res, err := services.WriteServiceVariable(ctx, q, s.box, sc, pw.w)
			if err != nil {
				return err
			}
			changes = []changeEntry{s.changeOf(res, pw.w)}
			return nil
		})
		if err != nil {
			return err
		}
		// The rotated service itself is always affected.
		found := false
		for i := range affected {
			if affected[i].ServiceID == serviceID {
				found = true
				affected[i].Via = appendUnique(affected[i].Via, key)
			}
		}
		if !found {
			affected = append(affected, RotationAffected{ServiceID: serviceID, Slug: sc.Service.Slug, Via: []string{key}})
		}
		if plan.Affected, plan.DeployOrder, err = s.finishPlan(ctx, q, infos, affected); err != nil {
			return err
		}
		plan.Warnings = append(plan.Warnings, "Existing sessions, tokens or signatures made with the old value become invalid once the change is deployed.")
		if dryRun {
			return errDryRun
		}
		if _, err := q.BumpServiceGeneration(ctx, serviceID); err != nil {
			return err
		}
		restaged := make([]RestagedServiceRef, 0, len(plan.Affected))
		for _, a := range plan.Affected {
			if a.ServiceID != serviceID {
				restaged = append(restaged, RestagedServiceRef{ServiceID: a.ServiceID, Slug: a.Slug})
			}
		}
		plan.Staged = true
		return s.auditSet(ctx, tx, sc, "variable.rotate", changes, restaged)
	})
	if err != nil && !errors.Is(err, errDryRun) {
		if _, ok := apierr.As(err); ok {
			return VariableRotationPlan{}, err
		}
		return VariableRotationPlan{}, fmt.Errorf("rotate variable: %w", err)
	}
	if len(plan.Blockers) > 0 && !dryRun {
		return VariableRotationPlan{}, apierr.Unprocessable("rotation_unsupported", plan.Blockers[0].Message).WithDetails(map[string]any{"key": key, "kind": plan.Kind})
	}
	if plan.Staged {
		for _, a := range plan.Affected {
			services.Publish(s.bus, services.EventServiceUpdated, sc.ProjectID, sc.EnvironmentID, a.ServiceID)
		}
		services.Publish(s.bus, services.EventVariableUpdated, sc.ProjectID, sc.EnvironmentID, serviceID)
	}
	return plan, nil
}

func appendUnique(list []string, v string) []string {
	for _, x := range list {
		if x == v {
			return list
		}
	}
	list = append(list, v)
	sort.Strings(list)
	return list
}

// engineBound reports a blocker for credentials that the service's engine
// reads only when it initialises its data: database services and any
// volume-backed service with a *PASSWORD* key.
func engineBound(sc services.Scope, row db.ServiceVariable, volumes int) *RotationBlocker {
	isPassword := strings.Contains(strings.ToUpper(row.Key), "PASSWORD")
	switch {
	case sc.Service.Kind == services.KindDatabase && row.IsSecret, volumes > 0 && isPassword:
		return &RotationBlocker{Code: "rotation_unsupported", Message: engineBoundNote}
	}
	return nil
}

// prepareWriteForced re-materialises a stored template.
func (s *Service) prepareWriteForced(row db.ServiceVariable, tmpl string) (preparedWrite, error) {
	m, err := varexpr.Materialize(tmpl)
	if err != nil {
		return preparedWrite{}, templateError(row.Key, err)
	}
	pw := preparedWrite{w: services.VarWrite{
		Key: row.Key, Value: m.Stored, Secret: row.IsSecret || m.Secret, Scope: row.Scope, Source: services.SourceGenerated,
		Template: tmpl, GenCalls: m.Calls,
	}}
	for _, w := range m.Warnings {
		pw.warnings = append(pw.warnings, VariableWarning{Key: row.Key, Code: w.Code, Message: w.Message})
	}
	return pw, nil
}

// rotationAudit records that a variable's rotation reminder came due.
func rotationAudit(ctx context.Context, tx pgx.Tx, sc services.Scope, key string, days int) error {
	return audit.Record(ctx, tx, audit.Entry{
		Action: "variable.rotation_due", ResourceType: "service", ResourceID: sc.Service.ID.String(), ProjectID: &sc.ProjectID, WorkspaceID: sc.WorkspaceID,
		Metadata: map[string]any{"service": sc.Service.Slug, "key": key, "rotateEveryDays": days},
	})
}
