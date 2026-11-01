-- name: ListUserLiveAPITokens :many
-- The API tokens of a user that still work, across every team: what a password
-- change or reset leaves alive, listed so the person can see them.
SELECT t.id, t.workspace_id, w.name AS workspace_name, t.name, t.prefix,
       t.created_at, t.last_used_at, t.expires_at
FROM api_tokens t
JOIN workspaces w ON w.id = t.workspace_id
WHERE t.user_id = $1 AND t.revoked_at IS NULL AND (t.expires_at IS NULL OR t.expires_at > now())
ORDER BY t.created_at DESC, t.id DESC;

-- name: RevokeUserAPITokens :execrows
-- Every live API token of the user, in every team (the person's own choice
-- after a password change or reset).
UPDATE api_tokens SET revoked_at = now()
WHERE user_id = $1 AND revoked_at IS NULL;

-- name: PeekPasswordResetToken :one
-- Whether a reset link still works, without spending it (the reset page checks
-- its link when it opens).
SELECT user_id FROM password_reset_tokens
WHERE token_hash = $1 AND used_at IS NULL AND expires_at > $2;
