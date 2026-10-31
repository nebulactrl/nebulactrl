-- name: GetIdempotencyKey :one
SELECT * FROM idempotency_keys WHERE principal_id = $1 AND key = $2;

-- name: InsertIdempotencyKey :exec
INSERT INTO idempotency_keys (principal_id, key, method, path, status, body)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (principal_id, key) DO NOTHING;

-- name: DeleteIdempotencyKeysBefore :execrows
DELETE FROM idempotency_keys WHERE created_at < $1;
