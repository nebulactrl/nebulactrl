-- name: GetProjectTerminalSettings :one
SELECT allow_production_file_writes
FROM project_terminal_settings
WHERE project_id = $1;

-- name: UpsertProjectTerminalSettings :exec
INSERT INTO project_terminal_settings (project_id, allow_production_file_writes)
VALUES (sqlc.arg(project_id), sqlc.arg(allow_production_file_writes))
ON CONFLICT (project_id) DO UPDATE
SET allow_production_file_writes = EXCLUDED.allow_production_file_writes,
    updated_at = now();
