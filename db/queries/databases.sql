-- name: InsertDatabaseRotation :one
INSERT INTO database_rotations (service_id, mode, status, password_key, new_password_enc, command_id, actor_label, id)
VALUES ($1, $2, 'pending', $3, $4, $5, $6, $7)
RETURNING *;

-- name: GetDatabaseRotation :one
SELECT * FROM database_rotations WHERE id = $1;

-- name: GetPendingDatabaseRotation :one
SELECT * FROM database_rotations WHERE service_id = $1 AND status = 'pending';

-- name: FinishDatabaseRotation :one
-- Idempotent: only a pending rotation can finish.
UPDATE database_rotations
SET status = sqlc.arg(status), error = sqlc.arg(error), finished_at = now()
WHERE id = sqlc.arg(id) AND status = 'pending'
RETURNING *;

-- name: ListDatabaseRotations :many
SELECT * FROM database_rotations WHERE service_id = $1 ORDER BY created_at DESC LIMIT 20;

-- name: SetDatabaseRotationCommand :exec
UPDATE database_rotations SET command_id = $2 WHERE id = $1;

-- name: UpsertDatabaseInfo :exec
INSERT INTO database_info (service_id, info, error, command_id, updated_at)
VALUES ($1, $2, $3, $4, now())
ON CONFLICT (service_id) DO UPDATE
SET info = EXCLUDED.info, error = EXCLUDED.error, command_id = EXCLUDED.command_id, updated_at = now();

-- name: GetDatabaseInfo :one
SELECT * FROM database_info WHERE service_id = $1;

-- name: InsertDatabaseUpgrade :one
INSERT INTO database_upgrades (service_id, method, from_version, to_version, old_volume, new_volume, status, command_id, actor_label)
VALUES ($1, $2, $3, $4, $5, $6, 'pending', $7, $8)
RETURNING *;

-- name: GetDatabaseUpgrade :one
SELECT * FROM database_upgrades WHERE id = $1;

-- name: GetPendingDatabaseUpgrade :one
SELECT * FROM database_upgrades WHERE service_id = $1 AND status = 'pending';

-- name: GetLatestDatabaseUpgrade :one
SELECT * FROM database_upgrades WHERE service_id = $1 ORDER BY created_at DESC LIMIT 1;

-- name: FinishDatabaseUpgrade :one
UPDATE database_upgrades
SET status = sqlc.arg(status), error = sqlc.arg(error), finished_at = now()
WHERE id = sqlc.arg(id) AND status = 'pending'
RETURNING *;

-- name: ListEnvironmentVariableTemplates :many
-- Raw (decrypted by the caller) variable rows of the live services of an
-- environment, used to find the services whose variables reference a provider.
SELECT v.service_id, v.key, v.value_enc
FROM service_variables v
JOIN services s ON s.id = v.service_id
WHERE s.environment_id = $1 AND s.deleted_at IS NULL;

-- name: SetServiceVolumeMount :exec
UPDATE volumes SET name = $3, mount_path = $4 WHERE service_id = $1 AND name = $2;

-- name: GetServiceVolumeByName :one
SELECT * FROM volumes WHERE service_id = $1 AND name = $2;

-- name: ListEnvironmentDatabases :many
SELECT * FROM services
WHERE environment_id = $1 AND kind = 'database' AND deleted_at IS NULL
ORDER BY created_at, id;
