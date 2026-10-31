-- Volume backup configs ------------------------------------------------------

-- name: GetVolumeBackupConfig :one
SELECT * FROM volume_backup_configs WHERE service_id = $1;

-- name: UpsertVolumeBackupConfig :one
INSERT INTO volume_backup_configs (
    service_id, workspace_id, destination_id, mode, freeze_budget_seconds, pre_hook, post_hook, excludes,
    keep_daily, keep_weekly, keep_monthly, cron, timezone, enabled, next_run_at
) VALUES (
    sqlc.arg(service_id), sqlc.arg(workspace_id), sqlc.arg(destination_id), sqlc.arg(mode), sqlc.arg(freeze_budget_seconds),
    sqlc.narg(pre_hook), sqlc.narg(post_hook), sqlc.arg(excludes)::text[],
    sqlc.arg(keep_daily), sqlc.arg(keep_weekly), sqlc.arg(keep_monthly), sqlc.arg(cron), sqlc.arg(timezone), sqlc.arg(enabled),
    sqlc.narg(next_run_at)
)
ON CONFLICT (service_id) DO UPDATE SET
    destination_id = EXCLUDED.destination_id,
    mode = EXCLUDED.mode,
    freeze_budget_seconds = EXCLUDED.freeze_budget_seconds,
    pre_hook = EXCLUDED.pre_hook,
    post_hook = EXCLUDED.post_hook,
    excludes = EXCLUDED.excludes,
    keep_daily = EXCLUDED.keep_daily,
    keep_weekly = EXCLUDED.keep_weekly,
    keep_monthly = EXCLUDED.keep_monthly,
    cron = EXCLUDED.cron,
    timezone = EXCLUDED.timezone,
    enabled = EXCLUDED.enabled,
    next_run_at = EXCLUDED.next_run_at,
    updated_at = now()
RETURNING *;

-- name: DeleteVolumeBackupConfig :execrows
DELETE FROM volume_backup_configs WHERE service_id = $1;

-- name: ListDueVolumeBackupConfigs :many
-- Locks the due rows so concurrent ticks never run a config twice.
SELECT c.* FROM volume_backup_configs c
JOIN services sv ON sv.id = c.service_id AND sv.deleted_at IS NULL
WHERE c.enabled AND c.next_run_at IS NOT NULL AND c.next_run_at <= $1
ORDER BY c.next_run_at
LIMIT 100
FOR UPDATE OF c SKIP LOCKED;

-- name: AdvanceVolumeBackupConfig :exec
UPDATE volume_backup_configs
SET last_run_at = $2, next_run_at = $3, updated_at = now()
WHERE service_id = $1;

-- Repositories ---------------------------------------------------------------

-- name: GetVolumeRepo :one
SELECT * FROM volume_repos WHERE service_id = $1 AND destination_id = $2;

-- name: InsertVolumeRepo :exec
-- A concurrent first backup may race: the first password wins.
INSERT INTO volume_repos (service_id, destination_id, repo_path, password_enc)
VALUES ($1, $2, $3, $4)
ON CONFLICT (service_id, destination_id) DO NOTHING;

-- Backups --------------------------------------------------------------------

-- name: InsertVolumeBackup :one
INSERT INTO volume_backups (
    workspace_id, service_id, destination_id, server_id, trigger, status, mode, volumes, error, actor_label, finished_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, sqlc.arg(volumes)::text[], $8, $9, $10)
RETURNING *;

-- name: SetVolumeBackupCommand :exec
UPDATE volume_backups SET command_id = $2 WHERE id = $1;

-- name: GetVolumeBackup :one
SELECT * FROM volume_backups WHERE id = $1 AND workspace_id = $2;

-- name: GetVolumeBackupInternal :one
SELECT * FROM volume_backups WHERE id = $1;

-- name: GetVolumeBackupByCommand :one
SELECT * FROM volume_backups WHERE command_id = $1;

-- name: ListServiceVolumeBackups :many
-- status is the effective status: a queued backup whose command the agent has
-- acknowledged is running.
SELECT sqlc.embed(b),
       (CASE WHEN b.status = 'queued' AND c.status = 'acked' THEN 'running' ELSE b.status END)::text AS effective_status,
       d.name AS destination_name, d.kind AS destination_kind
FROM volume_backups b
JOIN backup_destinations d ON d.id = b.destination_id
LEFT JOIN agent_commands c ON c.id = b.command_id
WHERE b.service_id = $1
ORDER BY b.created_at DESC, b.id DESC
LIMIT $2;

-- name: GetVolumeBackupView :one
SELECT sqlc.embed(b),
       (CASE WHEN b.status = 'queued' AND c.status = 'acked' THEN 'running' ELSE b.status END)::text AS effective_status,
       d.name AS destination_name, d.kind AS destination_kind
FROM volume_backups b
JOIN backup_destinations d ON d.id = b.destination_id
LEFT JOIN agent_commands c ON c.id = b.command_id
WHERE b.id = $1 AND b.workspace_id = $2;

-- name: CompleteVolumeBackup :one
UPDATE volume_backups
SET status = 'succeeded', snapshot_id = $2, consistency = $3, size_bytes = $4, bytes_added = $5, files = $6,
    freeze_ms = $7, stage_ms = $8, stage_method = $9, hook_outcome = $10, warnings = sqlc.arg(warnings)::text[], error = '',
    started_at = COALESCE(sqlc.narg(started_at), started_at), finished_at = now()
WHERE id = $1 AND status = 'queued'
RETURNING *;

-- name: FailVolumeBackup :one
UPDATE volume_backups
SET status = $2, error = $3, finished_at = now()
WHERE id = $1 AND status = 'queued'
RETURNING *;

-- name: SetVolumeBackupLocked :one
UPDATE volume_backups SET locked = $3 WHERE id = $1 AND workspace_id = $2 RETURNING *;

-- name: MarkVolumeBackupsObjectDeleted :exec
UPDATE volume_backups SET object_deleted_at = now() WHERE id = ANY(sqlc.arg(ids)::uuid[]) AND object_deleted_at IS NULL;

-- name: MarkVolumeSnapshotsForgotten :many
-- The agent reported these snapshots of a service's repository as removed.
UPDATE volume_backups SET object_deleted_at = now()
WHERE service_id = $1 AND destination_id = $2 AND snapshot_id = ANY(sqlc.arg(snapshot_ids)::text[])
  AND snapshot_id <> '' AND object_deleted_at IS NULL
RETURNING *;

-- name: ClearVolumeBackupObjectDeleted :exec
UPDATE volume_backups SET object_deleted_at = NULL, error = sqlc.arg(error) WHERE id = ANY(sqlc.arg(ids)::uuid[]);

-- name: DeleteVolumeBackupsByIDs :exec
DELETE FROM volume_backups WHERE id = ANY(sqlc.arg(ids)::uuid[]);

-- name: ListVolumeBackupsByIDs :many
SELECT * FROM volume_backups WHERE id = ANY(sqlc.arg(ids)::uuid[]);

-- name: ListStoredVolumeBackups :many
-- Successful, still stored backups of a service in one destination's repository.
SELECT * FROM volume_backups
WHERE service_id = $1 AND destination_id = $2 AND status = 'succeeded' AND object_deleted_at IS NULL AND snapshot_id <> ''
ORDER BY created_at DESC, id DESC;

-- name: FailStaleVolumeBackups :many
-- A backup whose agent never reported back (agent lost for good) must not stay
-- "running" forever.
UPDATE volume_backups
SET status = 'failed', error = 'No result was received from the agent within the maximum backup duration.', finished_at = now()
WHERE status = 'queued' AND created_at < $1
RETURNING *;

-- Restores -------------------------------------------------------------------

-- name: InsertVolumeRestore :one
INSERT INTO volume_restores (
    workspace_id, backup_id, service_id, volume_name, mode, status, docker_name, pre_backup_id, actor_label
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
RETURNING *;

-- name: GetVolumeRestoreInternal :one
SELECT * FROM volume_restores WHERE id = $1;

-- name: GetVolumeRestoreByCommand :one
SELECT * FROM volume_restores WHERE command_id = $1;

-- name: GetVolumeRestoreByPreBackup :one
SELECT * FROM volume_restores WHERE pre_backup_id = $1 AND status = 'pre_backup';

-- name: ListServiceVolumeRestores :many
SELECT * FROM volume_restores WHERE service_id = $1 ORDER BY created_at DESC, id DESC LIMIT $2;

-- name: SetVolumeRestoreProgress :one
UPDATE volume_restores
SET status = $2, command_id = COALESCE(sqlc.narg(command_id), command_id), error = $3,
    finished_at = CASE WHEN $2 IN ('succeeded','failed') THEN now() ELSE finished_at END
WHERE id = $1 AND status NOT IN ('succeeded','failed')
RETURNING *;

-- name: HasActiveVolumeRestore :one
-- An in-place restore (safety backup or restore running) of the service.
SELECT EXISTS (
    SELECT 1 FROM volume_restores WHERE service_id = $1 AND status IN ('pre_backup','restoring')
)::boolean;

-- Retired volumes ------------------------------------------------------------

-- name: InsertRetiredVolume :exec
INSERT INTO retired_volumes (service_id, volume_name, docker_name, reason)
VALUES ($1, $2, $3, $4)
ON CONFLICT (service_id, docker_name) DO NOTHING;

-- name: GetRetiredVolume :one
SELECT * FROM retired_volumes WHERE id = $1;

-- name: ListRetiredVolumes :many
SELECT * FROM retired_volumes WHERE service_id = $1 ORDER BY created_at DESC, id DESC;

-- name: ListRetiredVolumesByIDs :many
SELECT * FROM retired_volumes WHERE id = ANY(sqlc.arg(ids)::uuid[]);

-- name: DeleteRetiredVolumesByIDs :exec
DELETE FROM retired_volumes WHERE id = ANY(sqlc.arg(ids)::uuid[]);

-- name: DeleteRetiredVolumeByDockerName :exec
DELETE FROM retired_volumes WHERE service_id = $1 AND docker_name = $2;
