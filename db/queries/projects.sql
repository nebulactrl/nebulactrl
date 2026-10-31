-- name: ListProjects :many
SELECT * FROM projects
WHERE workspace_id = $1
  AND NOT system
  AND (sqlc.narg(project_ids)::uuid[] IS NULL OR id = ANY(sqlc.narg(project_ids)::uuid[]))
  AND (sqlc.narg(cursor_created_at)::timestamptz IS NULL
       OR (created_at, id) < (sqlc.narg(cursor_created_at)::timestamptz, sqlc.narg(cursor_id)::uuid))
ORDER BY created_at DESC, id DESC
LIMIT $2;

-- name: GetProject :one
SELECT * FROM projects WHERE id = $1 AND workspace_id = $2;

-- name: CreateProject :one
INSERT INTO projects (workspace_id, name, slug, description)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: UpdateProject :one
UPDATE projects
SET name = $3, description = $4, updated_at = now()
WHERE id = $1 AND workspace_id = $2
RETURNING *;

-- name: DeleteProject :execrows
DELETE FROM projects WHERE id = $1 AND workspace_id = $2;

-- name: ListProjectSlugsLike :many
SELECT slug FROM projects
WHERE workspace_id = sqlc.arg(workspace_id) AND (slug = sqlc.arg(base)::text OR slug LIKE sqlc.arg(base)::text || '-%');

-- name: CountProjectLiveServices :one
SELECT count(*) FROM services s
JOIN environments e ON e.id = s.environment_id
WHERE e.project_id = $1 AND s.deleted_at IS NULL;

-- name: GetSystemProject :one
-- The team's "NebulaCtrl platform" project (platform services live in it).
SELECT * FROM projects WHERE workspace_id = $1 AND system;

-- name: CreateSystemProject :one
INSERT INTO projects (workspace_id, name, slug, description, system)
VALUES ($1, $2, $3, $4, true)
RETURNING *;

-- name: GetProjectDeletionCounts :one
-- What deleting the project removes, across all its environments (previews included).
SELECT
  (SELECT count(*) FROM services s JOIN environments e ON e.id = s.environment_id
     WHERE e.project_id = sqlc.arg(project_id) AND s.deleted_at IS NULL AND s.kind <> 'database')::int AS services,
  (SELECT count(*) FROM services s JOIN environments e ON e.id = s.environment_id
     WHERE e.project_id = sqlc.arg(project_id) AND s.deleted_at IS NULL AND s.kind = 'database')::int AS databases,
  (SELECT count(*) FROM volumes v JOIN services s ON s.id = v.service_id JOIN environments e ON e.id = s.environment_id
     WHERE e.project_id = sqlc.arg(project_id) AND s.deleted_at IS NULL)::int AS volumes,
  (SELECT count(*) FROM domains d JOIN services s ON s.id = d.service_id JOIN environments e ON e.id = s.environment_id
     WHERE e.project_id = sqlc.arg(project_id) AND s.deleted_at IS NULL)::int AS domains,
  (SELECT count(*) FROM previews WHERE project_id = sqlc.arg(project_id))::int AS previews,
  (SELECT count(*) FROM status_page_components c JOIN services s ON s.id = c.service_id JOIN environments e ON e.id = s.environment_id
     WHERE e.project_id = sqlc.arg(project_id) AND s.deleted_at IS NULL)::int AS status_page_components,
  (SELECT count(*) FROM cloudflare_records r
     WHERE r.owner_kind = 'domain'
       AND r.owner_id IN (SELECT d.id FROM domains d JOIN services s ON s.id = d.service_id JOIN environments e ON e.id = s.environment_id
                          WHERE e.project_id = sqlc.arg(project_id) AND s.deleted_at IS NULL))::int AS managed_dns_records;

-- name: ListProjectStandardServices :many
-- Live services of the project's own (non-preview) environments, oldest first.
SELECT s.* FROM services s
JOIN environments e ON e.id = s.environment_id
WHERE e.project_id = $1 AND e.kind <> 'preview' AND s.deleted_at IS NULL
ORDER BY s.created_at, s.id;

-- name: CountProjectActiveDeployments :one
-- Deployments still running for services of the project's own environments.
SELECT count(*)::int FROM deployments d
JOIN services s ON s.id = d.service_id
JOIN environments e ON e.id = s.environment_id
WHERE e.project_id = $1 AND e.kind <> 'preview' AND s.deleted_at IS NULL
  AND d.status IN ('created','queued','preparing','building','releasing','verifying');

-- name: CountProjectDatabaseReplicas :one
SELECT count(*)::int FROM database_replicas r
JOIN environments e ON e.id = r.environment_id
JOIN services s ON s.id = r.service_id AND s.deleted_at IS NULL
WHERE e.project_id = $1;

-- name: DeleteProjectSharedVariables :execrows
-- The project's shared variables and those of its environments (they carry the project id).
DELETE FROM shared_variables WHERE project_id = sqlc.arg(project_id)::uuid;
