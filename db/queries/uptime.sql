-- Uptime monitoring (internal/uptime): probe ingest, state, incidents, rollups
-- and the uptime API reads. Tables: db/migrations/0006_uptime.sql.

-- ===== Ingest ================================================================

-- name: ListUptimeServicesOnServer :many
-- The live services of the server's workspace that run on the server (their
-- server, or the server of their current deployment), with their project
-- context. Services outside that set are not the server's to report on.
SELECT sqlc.embed(sv),
       p.id AS project_id, p.name AS project_name, e.name AS environment_name
FROM services sv
JOIN environments e ON e.id = sv.environment_id
JOIN projects p ON p.id = e.project_id
WHERE sv.id = ANY(sqlc.arg(service_ids)::uuid[])
  AND sv.deleted_at IS NULL
  AND p.workspace_id = sqlc.arg(workspace_id)::uuid
  AND (sv.server_id = sqlc.arg(server_id)::uuid
       OR EXISTS (SELECT 1 FROM deployments d WHERE d.id = sv.current_deployment_id AND d.server_id = sqlc.arg(server_id)::uuid));

-- name: ListUptimeMonitors :many
SELECT * FROM uptime_monitors WHERE service_id = ANY(sqlc.arg(service_ids)::uuid[]);

-- name: ListUptimeMaintenanceWindows :many
-- Windows of the services that overlap [from_ts, to_ts]. Canceled windows never apply.
SELECT ws.service_id, w.starts_at, w.ends_at
FROM maintenance_window_services ws
JOIN maintenance_windows w ON w.id = ws.window_id
WHERE ws.service_id = ANY(sqlc.arg(service_ids)::uuid[])
  AND w.canceled_at IS NULL
  AND w.starts_at <= sqlc.arg(to_ts)::timestamptz
  AND w.ends_at > sqlc.arg(from_ts)::timestamptz;

-- name: ListUptimeDeployingServices :many
-- Services with a deployment in flight (the same statuses the health alerts treat as active).
SELECT DISTINCT service_id FROM deployments
WHERE service_id = ANY(sqlc.arg(service_ids)::uuid[])
  AND status IN ('created','queued','preparing','building','releasing','verifying');

-- name: EnsureUptimeState :exec
-- Creates the state row of a service seen for the first time (unknown).
INSERT INTO uptime_state (service_id, workspace_id, state, since)
VALUES (sqlc.arg(service_id), sqlc.arg(workspace_id), 'unknown', sqlc.arg(since))
ON CONFLICT (service_id) DO NOTHING;

-- name: LockUptimeStates :many
-- Locks the states in a fixed order, so concurrent frames cannot deadlock.
SELECT * FROM uptime_state
WHERE service_id = ANY(sqlc.arg(service_ids)::uuid[])
ORDER BY service_id
FOR UPDATE;

-- name: ListOpenUptimeIncidents :many
SELECT * FROM uptime_incidents
WHERE service_id = ANY(sqlc.arg(service_ids)::uuid[]) AND resolved_at IS NULL;

-- name: SaveUptimeState :exec
UPDATE uptime_state SET
    state                 = sqlc.arg(state)::text,
    since                 = sqlc.arg(since)::timestamptz,
    consecutive_failures  = sqlc.arg(consecutive_failures)::integer,
    consecutive_successes = sqlc.arg(consecutive_successes)::integer,
    last_check_at         = sqlc.narg(last_check_at)::timestamptz,
    last_ok_at            = sqlc.narg(last_ok_at)::timestamptz,
    last_error            = sqlc.arg(last_error)::text,
    last_latency_ms       = sqlc.narg(last_latency_ms)::integer,
    updated_at            = now()
WHERE service_id = sqlc.arg(service_id)::uuid;

-- name: OpenUptimeIncident :one
INSERT INTO uptime_incidents (workspace_id, service_id, started_at, cause, notified_at)
VALUES (sqlc.arg(workspace_id), sqlc.arg(service_id), sqlc.arg(started_at), sqlc.arg(cause), sqlc.narg(notified_at))
RETURNING *;

-- name: ResolveUptimeIncident :exec
UPDATE uptime_incidents
SET resolved_at = sqlc.arg(resolved_at)::timestamptz,
    recovery_notified_at = sqlc.narg(recovery_notified_at)::timestamptz
WHERE id = sqlc.arg(id)::uuid AND resolved_at IS NULL;

-- name: AddUptime1m :exec
-- Adds the counts of newly stored checks to their minute. Results are
-- deduplicated by the state's last_check_at in the same transaction, so a
-- resent frame never reaches this query twice.
INSERT INTO uptime_1m (service_id, bucket, checks, up, degraded, down, maintenance, latency_sum_ms, latency_max_ms)
VALUES (sqlc.arg(service_id), sqlc.arg(bucket), sqlc.arg(checks), sqlc.arg(up), sqlc.arg(degraded), sqlc.arg(down),
        sqlc.arg(maintenance), sqlc.arg(latency_sum_ms), sqlc.arg(latency_max_ms))
ON CONFLICT (service_id, bucket) DO UPDATE SET
    checks         = uptime_1m.checks + EXCLUDED.checks,
    up             = uptime_1m.up + EXCLUDED.up,
    degraded       = uptime_1m.degraded + EXCLUDED.degraded,
    down           = uptime_1m.down + EXCLUDED.down,
    maintenance    = uptime_1m.maintenance + EXCLUDED.maintenance,
    latency_sum_ms = uptime_1m.latency_sum_ms + EXCLUDED.latency_sum_ms,
    latency_max_ms = GREATEST(uptime_1m.latency_max_ms, EXCLUDED.latency_max_ms);

-- name: LockStaleUptimeStates :many
-- States of live services whose last check is older than the cutoff: the
-- agent stopped reporting. Locked so the pure Expire step and its save are atomic
-- with concurrent ingest.
SELECT st.* FROM uptime_state st
JOIN services sv ON sv.id = st.service_id
WHERE sv.deleted_at IS NULL
  AND st.state IN ('up','degraded','down','maintenance')
  AND st.last_check_at < sqlc.arg(cutoff)::timestamptz
ORDER BY st.service_id
FOR UPDATE OF st;

-- name: ListUptimeServiceRefs :many
SELECT sv.id AS service_id, sv.name, e.id AS environment_id, p.id AS project_id, p.workspace_id
FROM services sv
JOIN environments e ON e.id = sv.environment_id
JOIN projects p ON p.id = e.project_id
WHERE sv.id = ANY(sqlc.arg(service_ids)::uuid[]);

-- ===== Rollups and retention =================================================

-- name: RollupUptime1h :execrows
-- Recomputes every hour that has minute rows in [from_ts, to_ts); idempotent.
INSERT INTO uptime_1h (service_id, bucket, checks, up, degraded, down, maintenance, latency_sum_ms, latency_max_ms)
SELECT service_id,
       (date_trunc('hour', bucket AT TIME ZONE 'UTC') AT TIME ZONE 'UTC')::timestamptz AS hour_bucket,
       sum(checks)::integer, sum(up)::integer, sum(degraded)::integer, sum(down)::integer, sum(maintenance)::integer,
       sum(latency_sum_ms)::bigint, max(latency_max_ms)::integer
FROM uptime_1m
WHERE bucket >= sqlc.arg(from_ts)::timestamptz AND bucket < sqlc.arg(to_ts)::timestamptz
GROUP BY service_id, hour_bucket
ON CONFLICT (service_id, bucket) DO UPDATE SET
    checks = EXCLUDED.checks, up = EXCLUDED.up, degraded = EXCLUDED.degraded, down = EXCLUDED.down,
    maintenance = EXCLUDED.maintenance, latency_sum_ms = EXCLUDED.latency_sum_ms, latency_max_ms = EXCLUDED.latency_max_ms;

-- name: RollupUptime1d :execrows
-- Recomputes every UTC day that has hour rows in [from_ts, to_ts); idempotent.
INSERT INTO uptime_1d (service_id, bucket, checks, up, degraded, down, maintenance, latency_sum_ms, latency_max_ms)
SELECT service_id,
       (date_trunc('day', bucket AT TIME ZONE 'UTC') AT TIME ZONE 'UTC')::timestamptz AS day_bucket,
       sum(checks)::integer, sum(up)::integer, sum(degraded)::integer, sum(down)::integer, sum(maintenance)::integer,
       sum(latency_sum_ms)::bigint, max(latency_max_ms)::integer
FROM uptime_1h
WHERE bucket >= sqlc.arg(from_ts)::timestamptz AND bucket < sqlc.arg(to_ts)::timestamptz
GROUP BY service_id, day_bucket
ON CONFLICT (service_id, bucket) DO UPDATE SET
    checks = EXCLUDED.checks, up = EXCLUDED.up, degraded = EXCLUDED.degraded, down = EXCLUDED.down,
    maintenance = EXCLUDED.maintenance, latency_sum_ms = EXCLUDED.latency_sum_ms, latency_max_ms = EXCLUDED.latency_max_ms;

-- name: PruneUptime1m :execrows
DELETE FROM uptime_1m WHERE bucket < sqlc.arg(before)::timestamptz;

-- name: PruneUptime1h :execrows
DELETE FROM uptime_1h WHERE bucket < sqlc.arg(before)::timestamptz;

-- name: PruneUptime1d :execrows
DELETE FROM uptime_1d WHERE bucket < sqlc.arg(before)::timestamptz;

-- ===== Reads =================================================================

-- name: GetUptimeState :one
SELECT * FROM uptime_state WHERE service_id = $1;

-- name: GetUptimeMonitor :one
SELECT * FROM uptime_monitors WHERE service_id = $1;

-- name: UpsertUptimeMonitor :one
INSERT INTO uptime_monitors (service_id, workspace_id, enabled, alerts, failure_threshold, recovery_threshold, reminder_minutes)
VALUES (sqlc.arg(service_id), sqlc.arg(workspace_id), sqlc.arg(enabled), sqlc.arg(alerts), sqlc.arg(failure_threshold), sqlc.arg(recovery_threshold), sqlc.arg(reminder_minutes))
ON CONFLICT (service_id) DO UPDATE SET
    enabled = EXCLUDED.enabled, alerts = EXCLUDED.alerts,
    failure_threshold = EXCLUDED.failure_threshold, recovery_threshold = EXCLUDED.recovery_threshold,
    reminder_minutes = EXCLUDED.reminder_minutes,
    updated_at = now()
RETURNING *;

-- name: ListUptimeReminders :many
-- Open outages whose uptime.down was sent and whose next reminder is due: the
-- monitor alerts, reminds every reminder_minutes (counted from the last
-- reminder, else the alert), and the service is down right now (not in
-- maintenance, not unknown or paused).
SELECT sqlc.embed(i),
       m.reminder_minutes,
       sv.name AS service_name, e.id AS environment_id, e.name AS environment_name,
       p.id AS project_id, p.name AS project_name
FROM uptime_incidents i
JOIN uptime_monitors m ON m.service_id = i.service_id
JOIN uptime_state st ON st.service_id = i.service_id
JOIN services sv ON sv.id = i.service_id
JOIN environments e ON e.id = sv.environment_id
JOIN projects p ON p.id = e.project_id
WHERE i.resolved_at IS NULL AND i.notified_at IS NOT NULL
  AND m.enabled AND m.alerts AND m.reminder_minutes > 0
  AND st.state = 'down' AND sv.deleted_at IS NULL
  AND COALESCE(i.reminded_at, i.notified_at) + m.reminder_minutes * interval '1 minute' <= sqlc.arg(due_at)::timestamptz
ORDER BY i.started_at;

-- name: ClaimUptimeReminder :execrows
-- Records a reminder before it is sent: only one caller wins a given reminder.
UPDATE uptime_incidents SET reminded_at = sqlc.arg(reminded_at)::timestamptz
WHERE id = sqlc.arg(id)::uuid AND resolved_at IS NULL
  AND reminded_at IS NOT DISTINCT FROM sqlc.narg(previous)::timestamptz;

-- name: ListDisabledUptimeServices :many
-- Services of a server whose monitoring is switched off (Reconcile.Uptime.Disabled).
SELECT m.service_id
FROM uptime_monitors m
JOIN services sv ON sv.id = m.service_id
WHERE NOT m.enabled AND sv.deleted_at IS NULL
  AND (sv.server_id = sqlc.arg(server_id)::uuid
       OR EXISTS (SELECT 1 FROM deployments d WHERE d.id = sv.current_deployment_id AND d.server_id = sqlc.arg(server_id)::uuid))
ORDER BY m.service_id;

-- name: ResolveOpenUptimeIncident :exec
-- Closes a service's open incident without a recovery notice (monitoring
-- switched off, or the service stopped on purpose).
UPDATE uptime_incidents SET resolved_at = sqlc.arg(resolved_at)::timestamptz
WHERE service_id = sqlc.arg(service_id)::uuid AND resolved_at IS NULL;

-- name: ListUptimeIncidents :many
SELECT * FROM uptime_incidents WHERE service_id = sqlc.arg(service_id)::uuid
ORDER BY started_at DESC LIMIT sqlc.arg(row_limit)::integer;

-- name: ListUptime1mRange :many
SELECT * FROM uptime_1m
WHERE service_id = ANY(sqlc.arg(service_ids)::uuid[]) AND bucket >= sqlc.arg(from_ts)::timestamptz AND bucket <= sqlc.arg(to_ts)::timestamptz
ORDER BY service_id, bucket;

-- name: ListUptime1hRange :many
SELECT * FROM uptime_1h
WHERE service_id = ANY(sqlc.arg(service_ids)::uuid[]) AND bucket >= sqlc.arg(from_ts)::timestamptz AND bucket < sqlc.arg(to_ts)::timestamptz
ORDER BY service_id, bucket;

-- name: ListUptime1dRange :many
SELECT * FROM uptime_1d
WHERE service_id = ANY(sqlc.arg(service_ids)::uuid[]) AND bucket >= sqlc.arg(from_ts)::timestamptz AND bucket < sqlc.arg(to_ts)::timestamptz
ORDER BY service_id, bucket;

-- name: ListEnvironmentUptime :many
-- Every live service of an environment with its state and monitor switch (no
-- row: not checked yet / monitored by default).
SELECT sv.id AS service_id, sv.name, sv.slug, sv.kind, sv.desired_state,
       st.state, st.since, st.last_check_at, st.last_error, st.last_latency_ms,
       m.enabled AS monitor_enabled
FROM services sv
LEFT JOIN uptime_state st ON st.service_id = sv.id
LEFT JOIN uptime_monitors m ON m.service_id = sv.id
WHERE sv.environment_id = sqlc.arg(environment_id)::uuid AND sv.deleted_at IS NULL
ORDER BY sv.created_at, sv.id;
