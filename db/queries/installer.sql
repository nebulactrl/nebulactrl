-- name: GetControlPlaneServer :one
-- The server the installer put the control plane on (at most one, not revoked).
SELECT * FROM servers WHERE is_control_plane AND status <> 'revoked' LIMIT 1;

-- name: ControlPlaneServerExists :one
-- True once the installer's server row was created, revoked or not: a revoked one is never recreated.
SELECT EXISTS (SELECT 1 FROM servers WHERE is_control_plane);

-- name: CreateControlPlaneServer :one
INSERT INTO servers (workspace_id, name, public_ip, acme_email, is_control_plane)
VALUES ($1, $2, $3, $4, true)
RETURNING *;

-- name: SetServerAcceptWorkloads :one
UPDATE servers
SET accept_workloads = sqlc.arg(accept_workloads)::boolean
WHERE id = sqlc.arg(id) AND workspace_id = sqlc.arg(workspace_id)
RETURNING *;

-- name: RefreshUnusedEnrollmentToken :execrows
-- Extends a not yet redeemed, not revoked token (the installer's local-agent token).
UPDATE enrollment_tokens
SET expires_at = sqlc.arg(expires_at)
WHERE token_hash = sqlc.arg(token_hash) AND used_at IS NULL AND revoked_at IS NULL;

-- name: EnrollmentTokenExists :one
SELECT EXISTS (SELECT 1 FROM enrollment_tokens WHERE token_hash = $1);

-- name: CountWorkspaceMemberships :one
SELECT count(*)::int FROM memberships WHERE workspace_id = $1;
