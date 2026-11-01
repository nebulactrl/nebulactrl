-- Restores from an uploaded dump file ------------------------------------------

-- name: InsertDumpRestore :one
INSERT INTO dump_restores (
    workspace_id, mode, source_service_id, target_service_id, status, declared_size, file_name, pre_backup_id, actor_label
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
RETURNING *;

-- name: GetDumpRestore :one
SELECT * FROM dump_restores WHERE id = $1 AND workspace_id = $2;

-- name: GetDumpRestoreInternal :one
SELECT * FROM dump_restores WHERE id = $1;

-- name: GetDumpRestoreByPreBackup :one
SELECT * FROM dump_restores WHERE pre_backup_id = $1 AND status = 'pre_backup';

-- name: ListServiceDumpRestores :many
SELECT * FROM dump_restores
WHERE source_service_id = $1 OR target_service_id = $1
ORDER BY created_at DESC LIMIT $2;

-- name: SetDumpRestoreDeployment :exec
UPDATE dump_restores SET deployment_id = $2, updated_at = now() WHERE id = $1;

-- name: SetDumpRestoreStatus :one
-- Moves an unfinished restore to status; a finished restore never changes.
UPDATE dump_restores
SET status = $2, error = $3, updated_at = now(),
    finished_at = CASE WHEN $2 IN ('succeeded','failed') THEN now() ELSE finished_at END
WHERE id = $1 AND status NOT IN ('succeeded','failed')
RETURNING *;

-- name: ClaimDumpRestoreUpload :one
-- Starts the upload: only one request wins a restore that awaits its file.
UPDATE dump_restores
SET status = 'uploading', file_name = $2, format = $3, gzip = $4, size_bytes = $5, bytes_received = 0,
    started_at = now(), updated_at = now(), error = ''
WHERE id = $1 AND status = 'awaiting_upload'
RETURNING *;

-- name: ReleaseDumpRestoreUpload :exec
-- An upload that failed before the agent consumed any byte leaves the target
-- as it was: the same restore can take another file. The error says why.
UPDATE dump_restores
SET status = 'awaiting_upload', bytes_received = 0, error = $2, updated_at = now()
WHERE id = $1 AND status = 'uploading';

-- name: SetDumpRestoreProgress :exec
UPDATE dump_restores SET bytes_received = $2, updated_at = now() WHERE id = $1 AND status = 'uploading';

-- name: SetDumpRestoreRestoring :exec
UPDATE dump_restores
SET status = 'restoring', bytes_received = $2, sha256 = $3, updated_at = now()
WHERE id = $1 AND status = 'uploading';

-- name: AppendDumpRestoreLog :exec
-- Keeps the newest 64 KiB of the log.
UPDATE dump_restores SET log = right(log || $2, 65536), updated_at = now() WHERE id = $1;

-- name: ListStaleDumpRestores :many
-- Restores nobody progressed since before: awaiting a file for too long, or
-- running with no live owner (the control plane restarted).
SELECT * FROM dump_restores
WHERE status IN ('awaiting_upload','uploading','restoring') AND updated_at < $1;
