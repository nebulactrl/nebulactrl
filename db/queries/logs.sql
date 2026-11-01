-- name: GetServerLogSettings :one
SELECT * FROM server_log_settings WHERE server_id = $1;

-- name: UpsertServerLogSettings :one
INSERT INTO server_log_settings (server_id, capture, retention_days, max_service_bytes, max_server_bytes)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (server_id) DO UPDATE
SET capture = EXCLUDED.capture, retention_days = EXCLUDED.retention_days,
    max_service_bytes = EXCLUDED.max_service_bytes, max_server_bytes = EXCLUDED.max_server_bytes,
    updated_at = now()
RETURNING *;

-- name: SetServerLogUsage :exec
INSERT INTO server_log_settings (server_id, usage, usage_at) VALUES ($1, $2, now())
ON CONFLICT (server_id) DO UPDATE SET usage = EXCLUDED.usage, usage_at = EXCLUDED.usage_at;

-- name: LogServicesOnServer :many
-- Live services placed on, or currently deployed to, the server, with the names
-- agents label shipped lines with.
SELECT s.id AS service_id, s.name AS service_name, s.slug AS service_slug,
       e.id AS environment_id, e.name AS environment_name, p.id AS project_id, p.name AS project_name
FROM services s
JOIN environments e ON e.id = s.environment_id
JOIN projects p ON p.id = e.project_id
WHERE s.deleted_at IS NULL
  AND (s.server_id = $1
       OR EXISTS (SELECT 1 FROM deployments d WHERE d.id = s.current_deployment_id AND d.server_id = $1))
ORDER BY s.created_at, s.id;

-- name: ListLogDrains :many
SELECT * FROM log_drains WHERE workspace_id = $1 ORDER BY lower(name), id;

-- name: GetLogDrain :one
SELECT * FROM log_drains WHERE id = $1 AND workspace_id = $2;

-- name: InsertLogDrain :one
INSERT INTO log_drains (workspace_id, name, kind, enabled, config, secrets_enc, all_projects, project_ids, service_ids, query, buffer_max_bytes, id)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
RETURNING *;

-- name: UpdateLogDrain :one
UPDATE log_drains
SET name = $3, enabled = $4, config = $5, secrets_enc = $6, all_projects = $7, project_ids = $8,
    service_ids = $9, query = $10, buffer_max_bytes = $11, updated_at = now()
WHERE id = $1 AND workspace_id = $2
RETURNING *;

-- name: DeleteLogDrain :execrows
DELETE FROM log_drains WHERE id = $1 AND workspace_id = $2;

-- name: SetLogDrainTest :exec
UPDATE log_drains SET last_test_at = now(), last_test_ok = $2, last_test_message = $3 WHERE id = $1;

-- name: UpsertLogDrainStatus :exec
-- A server reports only on drains of its own team: drain IDs are not secrets.
INSERT INTO log_drain_status (drain_id, server_id, state, last_success_at, last_error, last_error_at, sent_lines, dropped_lines, backlog_bytes, reported_at)
SELECT $1, $2, $3, $4, $5, $6, $7, $8, $9, now()
WHERE EXISTS (SELECT 1 FROM log_drains WHERE id = $1 AND workspace_id = (SELECT workspace_id FROM servers WHERE id = $2))
ON CONFLICT (drain_id, server_id) DO UPDATE
SET state = EXCLUDED.state, last_success_at = EXCLUDED.last_success_at, last_error = EXCLUDED.last_error,
    last_error_at = EXCLUDED.last_error_at, sent_lines = EXCLUDED.sent_lines, dropped_lines = EXCLUDED.dropped_lines,
    backlog_bytes = EXCLUDED.backlog_bytes, reported_at = now();

-- name: DeleteLogDrainStatusExcept :exec
-- Forget the statuses of drains an agent no longer reports (removed from its configuration).
DELETE FROM log_drain_status WHERE server_id = $1 AND NOT (drain_id = ANY($2::uuid[]));

-- name: ListLogDrainStatuses :many
SELECT st.*, sv.name AS server_name
FROM log_drain_status st
JOIN servers sv ON sv.id = st.server_id
JOIN log_drains d ON d.id = st.drain_id
WHERE d.workspace_id = $1
ORDER BY sv.name, st.server_id;

-- name: ListLogCapableServers :many
SELECT id, name, status FROM servers WHERE workspace_id = $1 AND status <> 'revoked' ORDER BY name, id;

-- name: CountWorkspaceServices :one
SELECT count(*) FROM services s
JOIN environments e ON e.id = s.environment_id
JOIN projects p ON p.id = e.project_id
WHERE p.workspace_id = $1 AND s.id = ANY($2::uuid[]) AND s.deleted_at IS NULL;
