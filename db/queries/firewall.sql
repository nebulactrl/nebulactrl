-- name: SetServiceFirewall :one
-- The firewall is an operational setting: it does not touch generation. NULL removes it.
UPDATE services
SET firewall = sqlc.narg(firewall),
    updated_at = now()
WHERE id = sqlc.arg(id) AND deleted_at IS NULL
RETURNING *;
