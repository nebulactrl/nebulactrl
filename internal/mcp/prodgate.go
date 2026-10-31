package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/mcp/catalog"
	"github.com/nebulactrl/nebulactrl/internal/prodguard"
)

// The central production gate.
//
// Every guarded operation is also guarded by its domain service
// (prodguard.Check), so a REST token on /api/v1 is held to the same rule as an
// MCP principal. The gate runs first for MCP calls: it describes the call to
// the approver from the catalog (catalog.CentralGuard says which operations and
// how to find their environment) and, once its approval is used, hands the
// domain service a prodguard.Covered context so the call asks only once. The
// catalog test fails for an operation that is neither guarded, denied nor
// listed as needing no approval.

// gateServiceFrom and gateEnvironmentFrom map a target to the SQL expression
// that yields the service id or environment id its id leads to. They are
// constants keyed by target; the id is always a bind parameter.
var gateServiceFrom = map[catalog.Target]string{
	catalog.TargetService:        `$1::uuid`,
	catalog.TargetTask:           `(SELECT service_id FROM service_tasks WHERE id = $1)`,
	catalog.TargetBackup:         `(SELECT service_id FROM backups WHERE id = $1)`,
	catalog.TargetVolumeBackup:   `(SELECT service_id FROM volume_backups WHERE id = $1)`,
	catalog.TargetBackupSchedule: `(SELECT service_id FROM backup_schedules WHERE id = $1)`,
	catalog.TargetRetiredVolume:  `(SELECT service_id FROM retired_volumes WHERE id = $1)`,
	catalog.TargetDomain:         `(SELECT service_id FROM domains WHERE id = $1)`,
}

var gateEnvironmentFrom = map[catalog.Target]string{
	catalog.TargetEnvironment: `$1::uuid`,
	catalog.TargetLink:        `(SELECT environment_id FROM service_links WHERE id = $1)`,
	catalog.TargetMeshLink:    `(SELECT environment_id FROM mesh_links WHERE id = $1)`,
}

const (
	gateServiceSQL = `
SELECT s.id, s.name, e.id, e.name, p.id, p.name
FROM services s
JOIN environments e ON e.id = s.environment_id
JOIN projects p ON p.id = e.project_id
WHERE s.id = %s AND p.workspace_id = $2 AND s.deleted_at IS NULL AND e.is_production`
	gateEnvironmentSQL = `
SELECT e.id, e.name, p.id, p.name
FROM environments e
JOIN projects p ON p.id = e.project_id
WHERE e.id = %s AND p.workspace_id = $2 AND e.is_production`
	gateServerSQL = `
SELECT sv.name, s.id, s.name, e.id, e.name, p.id, p.name
FROM services s
JOIN servers sv ON sv.id = s.server_id
JOIN environments e ON e.id = s.environment_id
JOIN projects p ON p.id = e.project_id
WHERE s.server_id = $1 AND sv.workspace_id = $2 AND s.deleted_at IS NULL AND e.is_production
ORDER BY p.name, e.name, s.name
LIMIT 20`
)

// gateRow is one production service (or the environment) a call touches.
type gateRow struct {
	serviceID, serviceName         string
	envID, projectID               uuid.UUID
	envName, projectName, serverNm string
}

// gate asks for approval when m is centrally guarded, the principal is
// guarded, the call matches the operation's conditions and its target lives in
// a production environment. It returns *prodguard.ErrApprovalRequired (or the
// guard's other refusals); a nil error means the call may be dispatched.
//
// The domain services guard these operations themselves (so REST tokens are
// guarded too). When the gate's own Check passed, the person's approval has
// been consumed for exactly this call, so the returned context is
// prodguard.Covered and the domain service does not ask a second time. The
// context never leaves the process; a request cannot carry it.
func (e *env) gate(ctx context.Context, m *opModel, args map[string]any) (context.Context, error) {
	c, ok := catalog.CentralGuard(m.id)
	if !ok || !prodguard.Guarded(e.p) || !gateApplies(c, args) {
		return ctx, nil
	}
	id, ok := e.gateTargetID(ctx, m, c, args)
	if !ok {
		return ctx, nil // the call names no usable id: the API answers 4xx
	}
	pool := e.h.deps.Pool
	if pool == nil {
		return ctx, apierr.New(503, prodguard.CodeApprovalUnavailable, "production approvals are not available on this control plane; the change was refused")
	}
	rows, err := loadGateRows(ctx, pool, c.Target, id, e.p.WorkspaceID)
	if err != nil {
		e.h.log.ErrorContext(ctx, "production gate lookup", "operation", m.id, "error", err)
		return ctx, apierr.New(503, prodguard.CodeApprovalUnavailable, "the production check failed, so the change was refused")
	}
	var visible []gateRow
	for _, r := range rows {
		// A project the token cannot see is not this layer's to describe: the
		// API refuses the call itself.
		if e.p.CanSeeProject(r.projectID) {
			visible = append(visible, r)
		}
	}
	if len(visible) == 0 {
		return ctx, nil
	}
	scope := gateScope(c, visible, args, e.p.WorkspaceID)
	if err := prodguard.Check(ctx, scope, m.id, gateDigest(m.id, id, args)); err != nil {
		return ctx, err
	}
	return prodguard.Covered(ctx), nil
}

// gateApplies reports whether the call is one the operation guards.
func gateApplies(c catalog.Central, args map[string]any) bool {
	if len(c.When) == 0 {
		return true
	}
	for _, w := range c.When {
		v, present := args[w.Arg]
		switch {
		case !present:
		case w.Equals == nil:
			return true
		case v == w.Equals:
			return true
		}
	}
	return false
}

// gateTargetID finds the id of the call's target resource among its arguments.
func (e *env) gateTargetID(ctx context.Context, m *opModel, c catalog.Central, args map[string]any) (uuid.UUID, bool) {
	param := c.Target.Param()
	for _, p := range m.params {
		if p.name != param {
			continue
		}
		s, _ := args[p.name].(string)
		if p.selector != "" {
			resolved, err := resolveSelector(ctx, e.h.resolver, p.selector, s)
			if err != nil {
				return uuid.Nil, false
			}
			s = resolved
		}
		id, err := uuid.Parse(strings.TrimSpace(s))
		return id, err == nil
	}
	return uuid.Nil, false
}

func loadGateRows(ctx context.Context, pool *pgxpool.Pool, t catalog.Target, id, workspace uuid.UUID) ([]gateRow, error) {
	switch {
	case t == catalog.TargetServer:
		rs, err := pool.Query(ctx, gateServerSQL, id, workspace)
		if err != nil {
			return nil, err
		}
		return pgx.CollectRows(rs, func(r pgx.CollectableRow) (gateRow, error) {
			var g gateRow
			err := r.Scan(&g.serverNm, &g.serviceID, &g.serviceName, &g.envID, &g.envName, &g.projectID, &g.projectName)
			return g, err
		})
	case gateServiceFrom[t] != "":
		rs, err := pool.Query(ctx, fmt.Sprintf(gateServiceSQL, gateServiceFrom[t]), id, workspace)
		if err != nil {
			return nil, err
		}
		return pgx.CollectRows(rs, func(r pgx.CollectableRow) (gateRow, error) {
			var g gateRow
			var sid uuid.UUID
			err := r.Scan(&sid, &g.serviceName, &g.envID, &g.envName, &g.projectID, &g.projectName)
			g.serviceID = sid.String()
			return g, err
		})
	case gateEnvironmentFrom[t] != "":
		rs, err := pool.Query(ctx, fmt.Sprintf(gateEnvironmentSQL, gateEnvironmentFrom[t]), id, workspace)
		if err != nil {
			return nil, err
		}
		return pgx.CollectRows(rs, func(r pgx.CollectableRow) (gateRow, error) {
			var g gateRow
			err := r.Scan(&g.envID, &g.envName, &g.projectID, &g.projectName)
			return g, err
		})
	}
	return nil, fmt.Errorf("no production lookup for target %q", t)
}

// gateScope builds what the approver reads from the operation's plan, the
// production rows it touches and the arguments it lists.
func gateScope(c catalog.Central, rows []gateRow, args map[string]any, workspace uuid.UUID) prodguard.ProdScope {
	first := rows[0]
	name := first.serviceName
	switch c.Target {
	case catalog.TargetEnvironment, catalog.TargetLink, catalog.TargetMeshLink:
		name = first.envName
	case catalog.TargetServer:
		name = first.serverNm
	}
	effects := append([]string{}, c.Effects...)
	for _, k := range c.Show {
		if v, ok := args[k]; ok {
			effects = append(effects, k+": "+clipText(compactJSON(v), 300))
		}
	}
	var targets []prodguard.ApprovalTarget
	switch c.Target {
	case catalog.TargetEnvironment, catalog.TargetLink, catalog.TargetMeshLink:
		targets = append(targets, prodguard.ApprovalTarget{Kind: "environment", ID: first.envID, Name: first.envName})
	default:
		for _, r := range rows {
			if sid, err := uuid.Parse(r.serviceID); err == nil {
				targets = append(targets, prodguard.ApprovalTarget{Kind: "service", ID: sid, Name: r.serviceName})
			}
		}
	}
	if c.Target == catalog.TargetServer && len(rows) > 1 {
		names := make([]string, len(rows))
		for i, r := range rows {
			names[i] = r.projectName + "/" + r.envName + "/" + r.serviceName
		}
		effects = append(effects, "Production services on this server: "+strings.Join(names, ", ")+".")
	}
	return prodguard.ProdScope{
		WorkspaceID: workspace, ProjectID: first.projectID, EnvironmentID: first.envID,
		Project: first.projectName, Environment: first.envName, IsProduction: true,
		Summary: strings.ReplaceAll(c.Summary, "{name}", name), Effects: effects, Irreversible: c.Irreversible,
		Targets: targets,
	}
}

// gateDigest binds an approval to the operation, its target and every
// argument, so a retry with anything else asks again. The Idempotency-Key
// header differs per call and is not part of what the approver saw.
func gateDigest(op string, target uuid.UUID, args map[string]any) []byte {
	rest := make(map[string]any, len(args))
	for k, v := range args {
		if k != "Idempotency-Key" {
			rest[k] = v
		}
	}
	b, _ := json.Marshal(rest) // map keys are sorted: the same call gives the same bytes
	return prodguard.Digest(op, target, string(b))
}

func compactJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}
