package prodguard

import (
	"context"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
)

// Querier is the part of a pgx pool CheckServer needs.
type Querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// hostedProductionSQL lists the live services of production environments that
// run on a server, limited to what an approval page shows.
const hostedProductionSQL = `
SELECT s.id, s.name, e.id, e.name, p.id, p.name
FROM services s
JOIN servers sv ON sv.id = s.server_id
JOIN environments e ON e.id = s.environment_id
JOIN projects p ON p.id = e.project_id
WHERE s.server_id = $1 AND sv.workspace_id = $2 AND s.deleted_at IS NULL AND e.is_production
ORDER BY p.name, e.name, s.name
LIMIT 20`

// CheckServer asks the guard before a change that cuts a server off. It does
// nothing unless the principal is guarded and the server hosts at least one
// service of a production environment. The approver reads the production
// services that depend on the server; digestArgs bind the approval to the
// call's arguments in addition to the server id. summary is the whole sentence
// the approver reads.
func CheckServer(ctx context.Context, q Querier, workspaceID, serverID uuid.UUID, op, summary string, irreversible bool, effects []string, digestArgs ...any) error {
	if !Applies(ctx, true) {
		return nil
	}
	rows, err := q.Query(ctx, hostedProductionSQL, serverID, workspaceID)
	if err != nil {
		return apierr.New(503, CodeApprovalUnavailable, "the production check failed, so the change was refused")
	}
	type hosted struct {
		serviceID, envID, projectID uuid.UUID
		service, env, project       string
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (hosted, error) {
		var h hosted
		err := r.Scan(&h.serviceID, &h.service, &h.envID, &h.env, &h.projectID, &h.project)
		return h, err
	})
	if err != nil {
		return apierr.New(503, CodeApprovalUnavailable, "the production check failed, so the change was refused")
	}
	if len(list) == 0 {
		return nil
	}
	targets := make([]ApprovalTarget, len(list))
	names := make([]string, len(list))
	for i, h := range list {
		targets[i] = ApprovalTarget{Kind: "service", ID: h.serviceID, Name: h.service}
		names[i] = h.project + "/" + h.env + "/" + h.service
	}
	if len(list) > 1 {
		effects = append(effects, "Production services on this server: "+strings.Join(names, ", ")+".")
	}
	first := list[0]
	scope := ProdScope{
		WorkspaceID: workspaceID, ProjectID: first.projectID, EnvironmentID: first.envID,
		Project: first.project, Environment: first.env, IsProduction: true,
		Summary: summary, Effects: effects, Irreversible: irreversible, Targets: targets,
	}
	args := append([]any{serverID}, digestArgs...)
	return Check(ctx, scope, op, Digest(op, args...))
}
