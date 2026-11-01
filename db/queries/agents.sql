-- name: CreateEnrollmentToken :one
INSERT INTO enrollment_tokens (workspace_id, server_id, token_hash, expires_at, created_by)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: GetEnrollmentTokenForUpdate :one
SELECT * FROM enrollment_tokens WHERE token_hash = $1 FOR UPDATE;

-- name: MarkEnrollmentTokenUsed :exec
UPDATE enrollment_tokens SET used_at = now() WHERE id = $1;

-- name: RevokeServerEnrollmentTokens :exec
UPDATE enrollment_tokens
SET revoked_at = COALESCE(revoked_at, now())
WHERE server_id = $1 AND used_at IS NULL;

-- name: CreateAgentIdentity :one
INSERT INTO agent_identities (server_id, public_key)
VALUES ($1, $2)
RETURNING *;

-- name: GetAgentIdentity :one
SELECT * FROM agent_identities WHERE id = $1;

-- name: RevokeServerAgentIdentity :exec
UPDATE agent_identities
SET revoked_at = COALESCE(revoked_at, now())
WHERE server_id = $1;
