-- Destinations ---------------------------------------------------------------

-- name: InsertBackupDestination :one
INSERT INTO backup_destinations (
    workspace_id, name, kind, endpoint, region, bucket, prefix, path_style, compat_mode,
    access_key_enc, secret_key_enc, local_dir, id
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
RETURNING *;

-- name: GetBackupDestination :one
SELECT * FROM backup_destinations WHERE id = $1 AND workspace_id = $2;

-- name: GetBackupDestinationInternal :one
SELECT * FROM backup_destinations WHERE id = $1;

-- name: ListBackupDestinations :many
SELECT * FROM backup_destinations WHERE workspace_id = $1 ORDER BY lower(name), id;

-- name: UpdateBackupDestination :one
UPDATE backup_destinations SET
    name = sqlc.arg(name),
    endpoint = sqlc.arg(endpoint),
    region = sqlc.arg(region),
    bucket = sqlc.arg(bucket),
    prefix = sqlc.arg(prefix),
    path_style = sqlc.arg(path_style),
    compat_mode = sqlc.arg(compat_mode),
    access_key_enc = COALESCE(sqlc.narg(access_key_enc), access_key_enc),
    secret_key_enc = COALESCE(sqlc.narg(secret_key_enc), secret_key_enc),
    local_dir = sqlc.arg(local_dir),
    last_test_at = NULL, last_test_ok = NULL, last_test_message = '',
    updated_at = now()
WHERE id = sqlc.arg(id) AND workspace_id = sqlc.arg(workspace_id)
RETURNING *;

-- name: SetBackupDestinationTest :exec
UPDATE backup_destinations
SET last_test_at = now(), last_test_ok = $2, last_test_message = $3
WHERE id = $1;

-- name: DeleteBackupDestination :execrows
DELETE FROM backup_destinations WHERE id = $1 AND workspace_id = $2;

-- name: CountBackupDestinationUsage :one
SELECT
    (SELECT count(*) FROM backup_schedules s WHERE s.destination_id = $1)::bigint AS schedules,
    (SELECT count(*) FROM backups b WHERE b.destination_id = $1 AND b.object_deleted_at IS NULL AND b.status = 'succeeded')::bigint AS backups,
    (SELECT count(*) FROM volume_backup_configs c WHERE c.destination_id = $1)::bigint AS volume_configs,
    (SELECT count(*) FROM volume_backups vb WHERE vb.destination_id = $1 AND vb.object_deleted_at IS NULL AND vb.status = 'succeeded')::bigint AS volume_backups,
    (SELECT count(*) FROM volume_repos r WHERE r.destination_id = $1)::bigint AS volume_repos;

-- Schedules ------------------------------------------------------------------

-- name: InsertBackupSchedule :one
INSERT INTO backup_schedules (
    workspace_id, service_id, destination_id, cron, timezone, enabled, retention_count, retention_days, next_run_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
RETURNING *;

-- name: GetBackupSchedule :one
SELECT * FROM backup_schedules WHERE id = $1;

-- name: ListBackupSchedulesByService :many
SELECT * FROM backup_schedules WHERE service_id = $1 ORDER BY created_at, id;

-- name: UpdateBackupSchedule :one
UPDATE backup_schedules SET
    destination_id = sqlc.arg(destination_id),
    cron = sqlc.arg(cron),
    timezone = sqlc.arg(timezone),
    enabled = sqlc.arg(enabled),
    retention_count = sqlc.arg(retention_count),
    retention_days = sqlc.arg(retention_days),
    next_run_at = sqlc.narg(next_run_at),
    updated_at = now()
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: DeleteBackupSchedule :execrows
DELETE FROM backup_schedules WHERE id = $1;

-- name: ListDueBackupSchedules :many
-- Locks the due rows so concurrent ticks never run a schedule twice.
SELECT s.* FROM backup_schedules s
JOIN services sv ON sv.id = s.service_id AND sv.deleted_at IS NULL
WHERE s.enabled AND s.next_run_at IS NOT NULL AND s.next_run_at <= $1
ORDER BY s.next_run_at
LIMIT 100
FOR UPDATE OF s SKIP LOCKED;

-- name: AdvanceBackupSchedule :exec
UPDATE backup_schedules
SET last_run_at = $2, next_run_at = $3, updated_at = now()
WHERE id = $1;

-- Backups --------------------------------------------------------------------

-- name: InsertBackup :one
INSERT INTO backups (
    workspace_id, service_id, schedule_id, destination_id, server_id, trigger, status,
    engine, engine_version, database_name, object_key, error, actor_label, finished_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
RETURNING *;

-- name: SetBackupCommand :exec
UPDATE backups SET command_id = $2 WHERE id = $1;

-- name: GetBackup :one
SELECT * FROM backups WHERE id = $1 AND workspace_id = $2;

-- name: GetBackupInternal :one
SELECT * FROM backups WHERE id = $1;

-- name: GetBackupByCommand :one
SELECT * FROM backups WHERE command_id = $1;

-- name: ListServiceBackups :many
-- status is the effective status: a queued backup whose command the agent has
-- acknowledged is running.
SELECT sqlc.embed(b),
       (CASE WHEN b.status = 'queued' AND c.status = 'acked' THEN 'running' ELSE b.status END)::text AS effective_status,
       d.name AS destination_name, d.kind AS destination_kind
FROM backups b
JOIN backup_destinations d ON d.id = b.destination_id
LEFT JOIN agent_commands c ON c.id = b.command_id
WHERE b.service_id = $1
ORDER BY b.created_at DESC, b.id DESC
LIMIT $2;

-- name: GetBackupView :one
SELECT sqlc.embed(b),
       (CASE WHEN b.status = 'queued' AND c.status = 'acked' THEN 'running' ELSE b.status END)::text AS effective_status,
       d.name AS destination_name, d.kind AS destination_kind
FROM backups b
JOIN backup_destinations d ON d.id = b.destination_id
LEFT JOIN agent_commands c ON c.id = b.command_id
WHERE b.id = $1 AND b.workspace_id = $2;

-- name: CompleteBackup :one
UPDATE backups
SET status = 'succeeded', size_bytes = $2, sha256 = $3, format = $4, object_key = $5, error = '',
    started_at = COALESCE($6, started_at), finished_at = now()
WHERE id = $1 AND status = 'queued'
RETURNING *;

-- name: FailBackup :one
UPDATE backups
SET status = $2, error = $3, finished_at = now()
WHERE id = $1 AND status = 'queued'
RETURNING *;

-- name: SetBackupLocked :one
UPDATE backups SET locked = $3 WHERE id = $1 AND workspace_id = $2 RETURNING *;

-- name: MarkBackupObjectDeleted :exec
UPDATE backups SET object_deleted_at = now() WHERE id = ANY(sqlc.arg(ids)::uuid[]);

-- name: ClearBackupObjectDeleted :exec
UPDATE backups SET object_deleted_at = NULL, error = sqlc.arg(error) WHERE id = ANY(sqlc.arg(ids)::uuid[]);

-- name: DeleteBackupsByIDs :exec
DELETE FROM backups WHERE id = ANY(sqlc.arg(ids)::uuid[]);

-- name: ListBackupsByIDs :many
SELECT * FROM backups WHERE id = ANY(sqlc.arg(ids)::uuid[]);

-- name: ListScheduleRetentionCandidates :many
-- Successful, still stored backups of a schedule, newest first.
SELECT * FROM backups
WHERE schedule_id = $1 AND status = 'succeeded' AND object_deleted_at IS NULL
ORDER BY created_at DESC, id DESC;

-- name: ListSucceededBackupsWithObject :many
SELECT * FROM backups WHERE service_id = $1 AND status = 'succeeded' AND object_deleted_at IS NULL
ORDER BY created_at DESC;

-- name: LatestServiceBackup :one
SELECT * FROM backups WHERE service_id = $1 AND status = 'succeeded' AND object_deleted_at IS NULL
ORDER BY created_at DESC LIMIT 1;

-- Restores -------------------------------------------------------------------

-- name: InsertBackupRestore :one
INSERT INTO backup_restores (
    workspace_id, backup_id, mode, source_service_id, target_service_id, status, pre_backup_id, deployment_id, actor_label
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
RETURNING *;

-- name: GetBackupRestore :one
SELECT * FROM backup_restores WHERE id = $1 AND workspace_id = $2;

-- name: GetBackupRestoreInternal :one
SELECT * FROM backup_restores WHERE id = $1;

-- name: GetBackupRestoreByCommand :one
SELECT * FROM backup_restores WHERE command_id = $1;

-- name: GetBackupRestoreByPreBackup :one
SELECT * FROM backup_restores WHERE pre_backup_id = $1 AND status = 'pre_backup';

-- name: ListServiceRestores :many
SELECT * FROM backup_restores
WHERE source_service_id = $1 OR target_service_id = $1
ORDER BY created_at DESC LIMIT $2;

-- name: SetRestoreProgress :one
UPDATE backup_restores
SET status = $2, command_id = COALESCE(sqlc.narg(command_id), command_id), error = $3,
    finished_at = CASE WHEN $2 IN ('succeeded','failed') THEN now() ELSE finished_at END
WHERE id = $1 AND status NOT IN ('succeeded','failed')
RETURNING *;

-- name: SetRestoreDeployment :exec
UPDATE backup_restores SET deployment_id = $2 WHERE id = $1;

-- name: SetRestorePreBackup :exec
UPDATE backup_restores SET pre_backup_id = $2 WHERE id = $1;

-- name: FailStaleBackups :many
-- A backup whose agent never reported back (agent lost for good) must not stay
-- "running" forever.
UPDATE backups
SET status = 'failed', error = 'No result was received from the agent within 12 hours.', finished_at = now()
WHERE status = 'queued' AND created_at < now() - interval '12 hours'
RETURNING *;
