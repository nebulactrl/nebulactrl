-- Control-plane (instance) backup ------------------------------------------------

-- name: GetInstanceBackupSettings :one
SELECT * FROM instance_backup_settings WHERE id;

-- name: UpdateInstanceBackupSettings :one
UPDATE instance_backup_settings SET
    enabled = sqlc.arg(enabled),
    dest_kind = sqlc.arg(dest_kind),
    endpoint = sqlc.arg(endpoint),
    region = sqlc.arg(region),
    bucket = sqlc.arg(bucket),
    prefix = sqlc.arg(prefix),
    path_style = sqlc.arg(path_style),
    compat_mode = sqlc.arg(compat_mode),
    access_key_enc = CASE WHEN sqlc.arg(clear_creds)::boolean THEN sqlc.narg(access_key_enc) ELSE COALESCE(sqlc.narg(access_key_enc), access_key_enc) END,
    secret_key_enc = CASE WHEN sqlc.arg(clear_creds)::boolean THEN sqlc.narg(secret_key_enc) ELSE COALESCE(sqlc.narg(secret_key_enc), secret_key_enc) END,
    local_dir = sqlc.arg(local_dir),
    schedule = sqlc.arg(schedule),
    retention_count = sqlc.arg(retention_count),
    next_run_at = sqlc.narg(next_run_at),
    last_test_at = CASE WHEN sqlc.arg(clear_test)::boolean THEN NULL ELSE last_test_at END,
    last_test_ok = CASE WHEN sqlc.arg(clear_test)::boolean THEN NULL ELSE last_test_ok END,
    last_test_message = CASE WHEN sqlc.arg(clear_test)::boolean THEN '' ELSE last_test_message END,
    updated_at = now()
WHERE id
RETURNING *;

-- name: SetInstanceBackupTest :exec
UPDATE instance_backup_settings
SET last_test_at = now(), last_test_ok = $1, last_test_message = $2
WHERE id;

-- name: AckInstanceBackupKey :one
UPDATE instance_backup_settings SET key_ack_at = now(), key_ack_by = $1, updated_at = now()
WHERE id
RETURNING *;

-- name: ClaimDueInstanceBackup :one
-- Moves next_run_at forward and returns true-ish row only when the schedule was
-- due and still held the observed next_run_at (one claimant wins).
UPDATE instance_backup_settings
SET next_run_at = sqlc.narg(new_next_run_at)
WHERE id AND enabled AND next_run_at IS NOT NULL AND next_run_at <= sqlc.arg(now)::timestamptz
RETURNING *;

-- name: InsertInstanceBackup :one
INSERT INTO instance_backups (trigger, dest_kind, dest_label, dest_fingerprint, actor_label)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: GetInstanceBackup :one
SELECT * FROM instance_backups WHERE id = $1;

-- name: ListInstanceBackups :many
SELECT * FROM instance_backups ORDER BY started_at DESC, id LIMIT $1;

-- name: ListInstanceBackupCandidates :many
-- Successful backups of one destination whose objects still exist.
SELECT * FROM instance_backups
WHERE status = 'succeeded' AND object_deleted_at IS NULL AND dest_fingerprint = $1
ORDER BY started_at DESC, id;

-- name: FinishInstanceBackup :one
UPDATE instance_backups SET
    status = sqlc.arg(status),
    object_key = sqlc.arg(object_key),
    size_bytes = sqlc.arg(size_bytes),
    sha256 = sqlc.arg(sha256),
    pg_version = sqlc.arg(pg_version),
    error = sqlc.arg(error),
    finished_at = now()
WHERE id = sqlc.arg(id) AND status = 'running'
RETURNING *;

-- name: MarkInstanceBackupObjectDeleted :exec
UPDATE instance_backups SET object_deleted_at = now() WHERE id = $1;

-- name: FailStaleInstanceBackups :many
UPDATE instance_backups
SET status = 'failed', error = 'The run was interrupted before it finished (the control plane stopped or restarted).', finished_at = now()
WHERE status = 'running' AND started_at < $1
RETURNING *;

-- name: PruneFailedInstanceBackups :exec
-- Keeps the newest 30 failed rows; failures carry no stored object.
DELETE FROM instance_backups
WHERE status = 'failed' AND id NOT IN (
    SELECT i.id FROM instance_backups i WHERE i.status = 'failed' ORDER BY i.started_at DESC LIMIT 30
);

-- name: GetLastSucceededInstanceBackup :one
SELECT * FROM instance_backups WHERE status = 'succeeded' ORDER BY started_at DESC, id LIMIT 1;
