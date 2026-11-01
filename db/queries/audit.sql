-- name: InsertAuditEvent :exec
INSERT INTO audit_events (workspace_id, actor_type, actor_id, actor_label, action, resource_type, resource_id, project_id, metadata)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9);

-- name: ListAuditEvents :many
SELECT * FROM audit_events
WHERE workspace_id = $1
  AND (sqlc.narg(project_ids)::uuid[] IS NULL OR project_id = ANY(sqlc.narg(project_ids)::uuid[]))
  AND (sqlc.narg(project_id)::uuid IS NULL OR project_id = sqlc.narg(project_id)::uuid)
  AND (sqlc.narg(resource_id)::text IS NULL OR resource_id = sqlc.narg(resource_id)::text)
  AND (sqlc.narg(action_prefix)::text IS NULL OR starts_with(action, sqlc.narg(action_prefix)::text))
  AND (sqlc.narg(cursor_id)::bigint IS NULL OR id < sqlc.narg(cursor_id)::bigint)
ORDER BY id DESC
LIMIT $2;
