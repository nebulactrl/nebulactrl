-- name: ListEnvironments :many
SELECT * FROM environments WHERE project_id = $1 ORDER BY is_production DESC, created_at, id;

-- name: GetEnvironmentScoped :one
SELECT e.* FROM environments e
JOIN projects p ON p.id = e.project_id
WHERE e.id = $1 AND p.workspace_id = $2;

-- name: CreateEnvironment :one
INSERT INTO environments (project_id, name, slug, is_production)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: UpdateEnvironment :one
UPDATE environments SET name = $2, is_production = $3 WHERE id = $1 RETURNING *;

-- name: DeleteEnvironment :execrows
DELETE FROM environments WHERE id = $1;

-- name: ListEnvironmentSlugsLike :many
SELECT slug FROM environments
WHERE project_id = sqlc.arg(project_id) AND (slug = sqlc.arg(base)::text OR slug LIKE sqlc.arg(base)::text || '-%');

-- name: CountEnvironmentLiveServices :one
SELECT count(*) FROM services WHERE environment_id = $1 AND deleted_at IS NULL;
