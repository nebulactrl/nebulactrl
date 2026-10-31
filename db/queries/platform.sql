-- name: InsertPlatformService :one
INSERT INTO platform_services (workspace_id, kind, environment_id, service_ids, hostname, public_url, options, secrets_enc, created_by, id)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
RETURNING *;

-- name: GetPlatformService :one
SELECT * FROM platform_services WHERE id = $1 AND workspace_id = $2;

-- name: GetPlatformServiceInternal :one
SELECT * FROM platform_services WHERE id = $1;

-- name: GetPlatformServiceByKind :one
SELECT * FROM platform_services WHERE workspace_id = $1 AND kind = $2;

-- name: ListPlatformServices :many
SELECT * FROM platform_services WHERE workspace_id = $1 ORDER BY created_at, id;

-- name: SetPlatformProgress :one
UPDATE platform_services
SET status = $2, step = $3, status_message = $4, deployment_ids = $5, updated_at = now()
WHERE id = $1
RETURNING *;

-- name: SetPlatformWiring :one
UPDATE platform_services SET wiring = $2, updated_at = now() WHERE id = $1 RETURNING *;

-- name: SetPlatformOptions :one
UPDATE platform_services SET options = $2, updated_at = now() WHERE id = $1 RETURNING *;

-- name: SetPlatformSecrets :one
UPDATE platform_services SET secrets_enc = $2, updated_at = now() WHERE id = $1 RETURNING *;

-- name: SetPlatformServiceIDs :one
UPDATE platform_services SET service_ids = $2, updated_at = now() WHERE id = $1 RETURNING *;

-- name: DeletePlatformService :execrows
DELETE FROM platform_services WHERE id = $1 AND workspace_id = $2;

-- name: ListServicesIncludingDeleted :many
SELECT * FROM services WHERE id = ANY(sqlc.arg(ids)::uuid[]) ORDER BY created_at, id;

-- name: GetServiceForUpdateByID :one
SELECT * FROM services WHERE id = $1 AND deleted_at IS NULL FOR UPDATE;
