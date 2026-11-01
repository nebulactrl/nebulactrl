-- name: ListEnvironmentLinks :many
SELECT l.* FROM service_links l
JOIN services f ON f.id = l.from_service_id AND f.deleted_at IS NULL
JOIN services t ON t.id = l.to_service_id AND t.deleted_at IS NULL
WHERE l.environment_id = $1
ORDER BY l.created_at, l.id;

-- name: InsertLink :one
INSERT INTO service_links (environment_id, from_service_id, to_service_id)
VALUES ($1, $2, $3)
RETURNING *;

-- name: GetLinkByID :one
SELECT * FROM service_links WHERE id = $1;

-- name: DeleteLink :execrows
DELETE FROM service_links WHERE id = $1;
