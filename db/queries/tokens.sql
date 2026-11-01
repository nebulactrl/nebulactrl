-- name: CreateAPIToken :one
INSERT INTO api_tokens (workspace_id, user_id, name, token_hash, prefix, scopes, project_ids, expires_at,
                        allow_production_without_approval, kind)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
RETURNING *;

-- name: ListAPITokens :many
-- The team's tokens; user_id limits the list to one member's own tokens.
SELECT * FROM api_tokens
WHERE workspace_id = sqlc.arg(workspace_id)
  AND (sqlc.narg(user_id)::uuid IS NULL OR user_id = sqlc.narg(user_id)::uuid)
ORDER BY created_at DESC, id DESC;

-- name: GetAPITokenByHash :one
SELECT t.id, t.workspace_id, t.user_id, t.name, t.scopes, t.project_ids,
       t.expires_at, t.revoked_at, t.last_used_at,
       t.allow_production_without_approval, t.kind
FROM api_tokens t
WHERE t.token_hash = $1;

-- name: RevokeAPIToken :one
-- user_id limits the revocation to one member's own tokens.
UPDATE api_tokens
SET revoked_at = COALESCE(revoked_at, now())
WHERE id = sqlc.arg(id) AND workspace_id = sqlc.arg(workspace_id)
  AND (sqlc.narg(user_id)::uuid IS NULL OR user_id = sqlc.narg(user_id)::uuid)
RETURNING *;

-- name: TouchAPIToken :exec
UPDATE api_tokens
SET last_used_at = now()
WHERE id = $1 AND (last_used_at IS NULL OR last_used_at < now() - interval '1 minute');
