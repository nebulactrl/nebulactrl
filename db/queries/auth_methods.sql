-- ---- instance sign-in settings ----

-- name: GetAuthSettings :one
SELECT * FROM auth_settings WHERE id = true;

-- name: SetAuthSettings :one
UPDATE auth_settings SET allow_open_signup = $1, updated_at = now() WHERE id = true RETURNING *;

-- ---- identity providers ----

-- name: ListAuthProviders :many
SELECT * FROM auth_providers ORDER BY created_at, id;

-- name: ListEnabledAuthProviders :many
SELECT * FROM auth_providers WHERE enabled ORDER BY created_at, id;

-- name: GetAuthProvider :one
SELECT * FROM auth_providers WHERE id = $1;

-- name: CreateAuthProvider :one
INSERT INTO auth_providers (id, kind, name, issuer_url, client_id, client_secret_enc, scopes, allowed_domains, trust_mfa, enabled)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
RETURNING *;

-- name: UpdateAuthProvider :one
UPDATE auth_providers SET
    name = $2,
    issuer_url = $3,
    client_id = $4,
    client_secret_enc = COALESCE(sqlc.narg('client_secret_enc'), client_secret_enc),
    scopes = $5,
    allowed_domains = $6,
    trust_mfa = $7,
    enabled = $8,
    updated_at = now()
WHERE id = $1
RETURNING *;

-- name: DeleteAuthProvider :execrows
DELETE FROM auth_providers WHERE id = $1;

-- ---- identities ----

-- name: GetUserIdentity :one
SELECT * FROM user_identities WHERE provider_id = $1 AND subject = $2;

-- name: ListUserIdentities :many
SELECT i.*, p.name AS provider_name, p.kind AS provider_kind
FROM user_identities i
JOIN auth_providers p ON p.id = i.provider_id
WHERE i.user_id = $1
ORDER BY i.created_at;

-- name: CreateUserIdentity :one
INSERT INTO user_identities (user_id, provider_id, subject, email)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: TouchUserIdentity :exec
UPDATE user_identities SET last_login_at = now() WHERE id = $1;

-- name: DeleteUserIdentity :execrows
DELETE FROM user_identities WHERE id = $1 AND user_id = $2;

-- name: CreateFederatedUser :one
INSERT INTO users (email, name, password_hash)
VALUES ($1, $2, '')
RETURNING *;

-- ---- TOTP ----

-- name: GetUserTOTP :one
SELECT * FROM user_totp WHERE user_id = $1;

-- name: UpsertPendingTOTP :exec
INSERT INTO user_totp (user_id, secret_enc) VALUES ($1, $2)
ON CONFLICT (user_id) DO UPDATE
SET secret_enc = EXCLUDED.secret_enc, last_step = 0, failures = 0, locked_until = NULL, created_at = now()
WHERE user_totp.enabled_at IS NULL;

-- name: EnableTOTP :execrows
UPDATE user_totp SET enabled_at = now(), last_step = $2, failures = 0, locked_until = NULL
WHERE user_id = $1 AND enabled_at IS NULL;

-- name: DeleteUserTOTP :exec
DELETE FROM user_totp WHERE user_id = $1;

-- name: UserHasTOTP :one
SELECT EXISTS (SELECT 1 FROM user_totp WHERE user_id = $1 AND enabled_at IS NOT NULL);

-- name: AcceptTOTPStep :execrows
-- Atomically records a verified step; fails when the step (or a later one) was
-- already used, which blocks replay of an observed code.
UPDATE user_totp SET last_step = $2, failures = 0, locked_until = NULL
WHERE user_id = $1 AND enabled_at IS NOT NULL AND last_step < $2;

-- name: RecordTOTPFailure :one
UPDATE user_totp SET
    failures = CASE WHEN failures + 1 >= sqlc.arg(max_failures)::int THEN 0 ELSE failures + 1 END,
    locked_until = CASE WHEN failures + 1 >= sqlc.arg(max_failures)::int
                        THEN now() + make_interval(secs => sqlc.arg(lock_seconds)::int) ELSE locked_until END
WHERE user_id = sqlc.arg(user_id)
RETURNING locked_until;

-- ---- recovery codes ----

-- name: DeleteRecoveryCodes :exec
DELETE FROM user_recovery_codes WHERE user_id = $1;

-- name: InsertRecoveryCode :exec
INSERT INTO user_recovery_codes (user_id, code_hash) VALUES ($1, $2);

-- name: UseRecoveryCode :execrows
UPDATE user_recovery_codes SET used_at = now()
WHERE user_id = $1 AND code_hash = $2 AND used_at IS NULL;

-- name: CountRecoveryCodesLeft :one
SELECT count(*) FROM user_recovery_codes WHERE user_id = $1 AND used_at IS NULL;

-- ---- team requirement ----

-- name: GetTeamTwoFactorState :one
SELECT w.require_two_factor,
       (EXISTS (SELECT 1 FROM user_totp t WHERE t.user_id = $2 AND t.enabled_at IS NOT NULL)
        OR EXISTS (SELECT 1 FROM user_passkeys k WHERE k.user_id = $2))::boolean AS user_has_second_factor
FROM workspaces w
WHERE w.id = $1;

-- name: SetTeamRequireTwoFactor :execrows
UPDATE workspaces SET require_two_factor = $2 WHERE id = $1;

-- name: ListUserRequiredTwoFactorTeams :many
SELECT w.id, w.name
FROM workspaces w
JOIN memberships m ON m.workspace_id = w.id
WHERE m.user_id = $1 AND w.require_two_factor
ORDER BY w.name;

-- name: CountTeamTwoFactor :one
SELECT count(*)::bigint AS members,
       (count(*) FILTER (WHERE EXISTS (SELECT 1 FROM user_totp t WHERE t.user_id = m.user_id AND t.enabled_at IS NOT NULL)
                         OR EXISTS (SELECT 1 FROM user_passkeys k WHERE k.user_id = m.user_id)))::bigint AS enrolled
FROM memberships m
WHERE m.workspace_id = $1;
