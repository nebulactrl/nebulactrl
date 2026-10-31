-- Channels -------------------------------------------------------------------

-- name: InsertNotificationChannel :one
INSERT INTO notification_channels (
    workspace_id, name, kind, enabled, config, secrets_enc, event_types, min_severity, all_projects, project_ids, id
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
RETURNING *;

-- name: GetNotificationChannel :one
SELECT * FROM notification_channels WHERE id = $1 AND workspace_id = $2;

-- name: GetNotificationChannelInternal :one
SELECT * FROM notification_channels WHERE id = $1;

-- name: ListNotificationChannels :many
SELECT * FROM notification_channels WHERE workspace_id = $1 ORDER BY lower(name), id;

-- name: ListEnabledNotificationChannels :many
SELECT * FROM notification_channels WHERE workspace_id = $1 AND enabled ORDER BY created_at, id;

-- name: UpdateNotificationChannel :one
UPDATE notification_channels SET
    name = sqlc.arg(name),
    enabled = sqlc.arg(enabled),
    config = sqlc.arg(config),
    secrets_enc = COALESCE(sqlc.narg(secrets_enc), secrets_enc),
    event_types = sqlc.arg(event_types),
    min_severity = sqlc.arg(min_severity),
    all_projects = sqlc.arg(all_projects),
    project_ids = sqlc.arg(project_ids),
    updated_at = now()
WHERE id = sqlc.arg(id) AND workspace_id = sqlc.arg(workspace_id)
RETURNING *;

-- name: SetNotificationChannelTest :exec
UPDATE notification_channels
SET last_test_at = now(), last_test_ok = $2, last_test_message = $3
WHERE id = $1;

-- name: DeleteNotificationChannel :execrows
DELETE FROM notification_channels WHERE id = $1 AND workspace_id = $2;

-- name: NotificationChannelStats :many
-- Delivery counts of the last 24 hours per channel (test sends excluded).
SELECT channel_id,
       count(*) FILTER (WHERE status = 'delivered')::int AS delivered,
       count(*) FILTER (WHERE status = 'failed')::int AS failed,
       max(created_at)::timestamptz AS last_at
FROM notification_deliveries
WHERE workspace_id = $1 AND NOT is_test AND created_at > now() - interval '24 hours'
GROUP BY channel_id;

-- Deliveries -----------------------------------------------------------------

-- name: InsertNotificationDelivery :one
INSERT INTO notification_deliveries (
    workspace_id, channel_id, event_type, severity, title, project_id, payload, dedupe_key, is_test, status, attempts
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
RETURNING *;

-- name: NotificationDedupeExists :one
-- True when an identical event was already queued for the channel inside the window.
SELECT EXISTS (
    SELECT 1 FROM notification_deliveries
    WHERE channel_id = $1 AND dedupe_key = $2 AND NOT is_test AND created_at > $3
);

-- name: GetNotificationDelivery :one
SELECT * FROM notification_deliveries WHERE id = $1;

-- name: ListNotificationDeliveries :many
SELECT * FROM notification_deliveries
WHERE channel_id = sqlc.arg(channel_id)
  AND workspace_id = sqlc.arg(workspace_id)
  AND (sqlc.arg(status)::text = '' OR status = sqlc.arg(status)::text)
  AND (sqlc.narg(before_created)::timestamptz IS NULL
       OR (created_at, id) < (sqlc.narg(before_created)::timestamptz, sqlc.narg(before_id)::uuid))
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg(row_limit);

-- name: RecordNotificationAttempt :exec
-- Stores the outcome of an attempt. status is retrying, delivered or failed.
UPDATE notification_deliveries SET
    status = sqlc.arg(status),
    attempts = sqlc.arg(attempts),
    last_error = sqlc.arg(last_error),
    response_code = sqlc.narg(response_code),
    next_attempt_at = sqlc.narg(next_attempt_at),
    delivered_at = CASE WHEN sqlc.arg(status) = 'delivered' THEN now() ELSE delivered_at END,
    updated_at = now()
WHERE id = sqlc.arg(id);

-- name: PruneNotificationDeliveries :execrows
DELETE FROM notification_deliveries WHERE created_at < $1;

-- Event context --------------------------------------------------------------

-- name: NotifProjectBrief :one
SELECT id, workspace_id, name FROM projects WHERE id = $1;

-- name: NotifServerBrief :one
SELECT id, workspace_id, name FROM servers WHERE id = $1;

-- name: NotifServiceBrief :one
SELECT s.id AS service_id, s.name AS service_name, e.id AS environment_id, e.slug AS env_slug, e.name AS env_name,
       p.id AS project_id, p.name AS project_name, p.workspace_id
FROM services s
JOIN environments e ON e.id = s.environment_id
JOIN projects p ON p.id = e.project_id
WHERE s.id = $1;

-- name: NotifCountProjects :one
SELECT count(*) FROM projects WHERE workspace_id = sqlc.arg(workspace_id) AND id = ANY(sqlc.arg(ids)::uuid[]);
