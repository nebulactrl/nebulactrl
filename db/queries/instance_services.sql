-- Instance services (S3, registry, Loki) and the per-team slices.

-- name: GetInstanceService :one
SELECT * FROM instance_services WHERE kind = $1;

-- name: ListInstanceServices :many
SELECT * FROM instance_services ORDER BY kind;

-- name: UpsertInstanceService :one
-- The control plane's view of one kind: status, message and endpoint. Options and
-- secrets are left alone when the row exists.
INSERT INTO instance_services (kind, status, message, endpoint)
VALUES ($1, $2, $3, $4)
ON CONFLICT (kind) DO UPDATE
SET status = EXCLUDED.status, message = EXCLUDED.message, endpoint = EXCLUDED.endpoint, updated_at = now()
RETURNING *;

-- name: SetInstanceServiceStatus :exec
UPDATE instance_services SET status = $2, message = $3, updated_at = now() WHERE kind = $1;

-- name: SetInstanceServiceOptions :exec
UPDATE instance_services SET options = $2, updated_at = now() WHERE kind = $1;

-- name: MergeInstanceServiceOptions :one
-- Merges a patch into options; a null in the patch clears a key.
UPDATE instance_services
SET options = jsonb_strip_nulls(COALESCE(options, '{}'::jsonb) || sqlc.arg(patch)::jsonb), updated_at = now()
WHERE kind = sqlc.arg(kind)
RETURNING *;

-- name: SetInstanceServiceSecrets :exec
UPDATE instance_services SET secrets_enc = $2, updated_at = now() WHERE kind = $1;

-- name: GetSlice :one
SELECT * FROM team_service_slices WHERE id = $1;

-- name: GetTeamSlice :one
SELECT * FROM team_service_slices WHERE team_id = $1 AND kind = $2;

-- name: ListTeamSlices :many
-- A team's live slices (not marked deleted).
SELECT * FROM team_service_slices WHERE team_id = $1 AND deleted_at IS NULL ORDER BY kind;

-- name: ListSlicesByKind :many
SELECT * FROM team_service_slices WHERE kind = $1 AND deleted_at IS NULL ORDER BY created_at, id;

-- name: ListSlicesForAdmin :many
-- Every slice, deleted teams' included (they wait for their purge).
SELECT * FROM team_service_slices ORDER BY team_name, team_id, kind;

-- name: ListSlicesDuePurge :many
SELECT * FROM team_service_slices
WHERE deleted_at IS NOT NULL AND purge_at IS NOT NULL AND purge_at <= $1
ORDER BY purge_at, id
LIMIT $2;

-- name: ListDeletedTeamSlices :many
SELECT * FROM team_service_slices WHERE team_id = $1 AND deleted_at IS NOT NULL ORDER BY kind;

-- name: InsertSlice :one
-- Idempotent: the (team, kind) row that already exists is left alone and nothing
-- is returned (pgx.ErrNoRows).
INSERT INTO team_service_slices (id, team_id, team_name, kind, ref, access_key, secret_enc)
VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (team_id, kind) DO NOTHING
RETURNING *;

-- name: SetSliceState :exec
UPDATE team_service_slices SET state = $2, message = $3, updated_at = now() WHERE id = $1;

-- name: SetSliceUsage :exec
UPDATE team_service_slices SET used_bytes = $2, used_at = now(), updated_at = now() WHERE id = $1;

-- name: SetSliceAlertLevel :exec
UPDATE team_service_slices SET alert_level = $2, updated_at = now() WHERE id = $1;

-- name: SetSliceQuota :one
-- NULL resets to the instance default.
UPDATE team_service_slices SET quota_bytes = sqlc.narg(quota_bytes), updated_at = now()
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: MarkTeamSlicesDeleted :many
-- Starts the 30-day purge clock. A slice already marked keeps its original dates.
UPDATE team_service_slices
SET deleted_at = now(), purge_at = sqlc.arg(purge_at)::timestamptz, updated_at = now()
WHERE team_id = sqlc.arg(team_id) AND deleted_at IS NULL
RETURNING *;

-- name: DeleteSlice :exec
DELETE FROM team_service_slices WHERE id = $1;

-- name: DeleteSlicesOfTeam :exec
DELETE FROM team_service_slices WHERE team_id = $1;

-- name: ListTeamsWithoutSlices :many
-- Teams that still need provisioning: not every wanted kind has a usable slice.
-- A failed slice counts as missing, so the periodic run retries it.
SELECT w.id, w.name
FROM workspaces w
WHERE w.deletion_state = ''
  AND (SELECT count(*) FROM team_service_slices s
       WHERE s.team_id = w.id AND s.deleted_at IS NULL AND s.state IN ('ready','full')
         AND s.kind = ANY(sqlc.arg(kinds)::text[])) < cardinality(sqlc.arg(kinds)::text[])
ORDER BY w.created_at, w.id;

-- name: SyncSliceTeamNames :exec
-- A renamed team keeps its slices; the admin table shows the current name.
UPDATE team_service_slices s SET team_name = w.name, updated_at = now()
FROM workspaces w
WHERE w.id = s.team_id AND s.team_name <> w.name;

-- name: GetS3QuotaState :one
-- What the quota gate needs for one team in a single read.
SELECT s.id, s.used_bytes, s.quota_bytes, s.state, (s.deleted_at IS NOT NULL)::boolean AS deleted,
       COALESCE((SELECT (i.options->>'defaultQuotaBytes')::bigint FROM instance_services i WHERE i.kind = 's3'), 0)::bigint AS default_quota_bytes
FROM team_service_slices s
WHERE s.team_id = $1 AND s.kind = 's3';

-- name: GetSliceByAccessKey :one
SELECT * FROM team_service_slices WHERE access_key = $1 AND kind = 's3' AND access_key <> '' AND deleted_at IS NULL;

-- name: GetControlPlaneWorkspace :one
-- The team that owns the control-plane host: the instance admin's alerts go there.
SELECT workspace_id FROM servers WHERE is_control_plane AND status <> 'revoked' LIMIT 1;

-- name: GetServerTeam :one
SELECT workspace_id FROM servers WHERE id = $1 AND status <> 'revoked';

-- name: GetBuiltinDestination :one
SELECT * FROM backup_destinations WHERE workspace_id = $1 AND kind = 'builtin';

-- name: InsertBuiltinDestination :one
-- Holds no credentials: the control plane resolves endpoint, bucket and key from the slice.
INSERT INTO backup_destinations (workspace_id, name, kind, endpoint, region, bucket, prefix, path_style, compat_mode)
VALUES ($1, $2, 'builtin', '', 'us-east-1', '', 'nebulactrl/', true, true)
ON CONFLICT (workspace_id) WHERE kind = 'builtin' DO NOTHING
RETURNING *;

-- name: GetBuiltinRegistry :one
SELECT * FROM registries WHERE workspace_id = $1 AND kind = 'builtin';

-- name: InsertBuiltinRegistry :one
INSERT INTO registries (workspace_id, name, kind, host, namespace, manage_retention)
VALUES ($1, $2, 'builtin', $3, $4, true)
ON CONFLICT (workspace_id) WHERE kind = 'builtin' DO NOTHING
RETURNING *;

-- name: GetBuiltinDrain :one
SELECT * FROM log_drains WHERE workspace_id = $1 AND builtin;

-- name: InsertBuiltinDrain :one
-- Created disabled: the team turns central logs on in Team settings.
INSERT INTO log_drains (workspace_id, name, kind, enabled, config, builtin)
VALUES ($1, $2, 'loki', false, $3, true)
ON CONFLICT (workspace_id) WHERE builtin DO NOTHING
RETURNING *;
