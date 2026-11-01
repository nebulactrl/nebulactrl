-- ---- passkeys (WebAuthn) ----

-- name: ListUserPasskeys :many
SELECT * FROM user_passkeys WHERE user_id = $1 ORDER BY created_at, id;

-- name: GetUserPasskey :one
SELECT * FROM user_passkeys WHERE id = $1 AND user_id = $2;

-- name: GetPasskeyByCredentialID :one
SELECT * FROM user_passkeys WHERE credential_id = $1;

-- name: CountUserPasskeys :one
SELECT count(*) FROM user_passkeys WHERE user_id = $1;

-- name: CreateUserPasskey :one
INSERT INTO user_passkeys (user_id, name, credential_id, credential)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: RenameUserPasskey :execrows
UPDATE user_passkeys SET name = $3 WHERE id = $1 AND user_id = $2;

-- name: DeleteUserPasskey :execrows
DELETE FROM user_passkeys WHERE id = $1 AND user_id = $2;

-- name: TouchUserPasskey :exec
-- Stores the credential after a sign-in (sign counter, backup state) and stamps use.
UPDATE user_passkeys SET credential = $2, last_used_at = now() WHERE id = $1;

-- name: UserHasSecondFactor :one
SELECT (EXISTS (SELECT 1 FROM user_totp t WHERE t.user_id = $1 AND t.enabled_at IS NOT NULL)
        OR EXISTS (SELECT 1 FROM user_passkeys k WHERE k.user_id = $1))::boolean AS has_second_factor;
