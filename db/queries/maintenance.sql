-- name: SetServiceMaintenance :one
-- Maintenance is an operational setting: it does not touch generation.
UPDATE services
SET maintenance_enabled = sqlc.arg(enabled),
    maintenance_title = sqlc.arg(title),
    maintenance_message = sqlc.arg(message),
    maintenance_eta = sqlc.narg(eta),
    maintenance_since = sqlc.narg(since),
    updated_at = now()
WHERE id = sqlc.arg(id) AND deleted_at IS NULL
RETURNING *;
