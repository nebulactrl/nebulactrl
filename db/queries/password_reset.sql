-- name: InsertPasswordResetToken :one
INSERT INTO password_reset_tokens (user_id, token_hash, created_by, expires_at)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: CountSelfServiceResetTokensSince :one
-- How many links the account holder asked for since the given time (admin
-- generated links do not count against the per-account email limit).
SELECT count(*) FROM password_reset_tokens
WHERE user_id = $1 AND created_by IS NULL AND created_at > $2;

-- name: ConsumePasswordResetToken :one
-- Single use: the first caller to find the live link marks it used; every
-- later or concurrent caller (and an expired link) finds no row.
UPDATE password_reset_tokens SET used_at = $2
WHERE token_hash = $1 AND used_at IS NULL AND expires_at > $2
RETURNING user_id;

-- name: SpendPasswordResetTokens :exec
-- After a reset every other live link of the account dies with it.
UPDATE password_reset_tokens SET used_at = $2 WHERE user_id = $1 AND used_at IS NULL;

-- name: ListUserTeamIDs :many
SELECT workspace_id FROM memberships WHERE user_id = $1 ORDER BY created_at, workspace_id;

-- name: AnyEmailChannelEnabled :one
SELECT EXISTS (SELECT 1 FROM notification_channels WHERE kind = 'email' AND enabled);

-- name: PruneExpiredPasswordResetTokens :exec
DELETE FROM password_reset_tokens WHERE expires_at < $1 - interval '1 day';
