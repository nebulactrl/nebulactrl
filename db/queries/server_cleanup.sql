-- name: GetServerCleanupSettings :one
SELECT * FROM server_cleanup_settings WHERE server_id = $1;

-- name: UpsertServerCleanupSettings :one
-- schedule_changed_at moves only when the cron expression does.
INSERT INTO server_cleanup_settings (server_id, workspace_id, enabled, remove_unmanaged_containers,
                                     schedule, unused_image_min_age_hours, build_cache_min_age_days, disk_trigger_percent)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (server_id) DO UPDATE
SET enabled = EXCLUDED.enabled,
    remove_unmanaged_containers = EXCLUDED.remove_unmanaged_containers,
    schedule_changed_at = CASE WHEN server_cleanup_settings.schedule = EXCLUDED.schedule
                               THEN server_cleanup_settings.schedule_changed_at ELSE now() END,
    schedule = EXCLUDED.schedule,
    unused_image_min_age_hours = EXCLUDED.unused_image_min_age_hours,
    build_cache_min_age_days = EXCLUDED.build_cache_min_age_days,
    disk_trigger_percent = EXCLUDED.disk_trigger_percent,
    updated_at = now()
RETURNING *;

-- name: InsertServerCleanupRun :one
INSERT INTO server_cleanup_runs (server_id, workspace_id, mode, trigger, requested_by)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: SetServerCleanupRunCommand :exec
UPDATE server_cleanup_runs SET command_id = $2 WHERE id = $1;

-- name: GetServerCleanupRun :one
SELECT * FROM server_cleanup_runs WHERE id = $1;

-- name: GetServerCleanupRunByCommand :one
SELECT * FROM server_cleanup_runs WHERE command_id = $1;

-- name: ListServerCleanupRuns :many
SELECT * FROM server_cleanup_runs WHERE server_id = $1 ORDER BY created_at DESC, id DESC LIMIT $2;

-- name: FinishServerCleanupRun :one
-- Records the outcome of a pending run once; a duplicate result changes nothing
-- and returns no row.
UPDATE server_cleanup_runs
SET status = sqlc.arg(status)::text,
    reclaimed_bytes = sqlc.arg(reclaimed_bytes)::bigint,
    items = sqlc.arg(items)::integer,
    report = sqlc.narg(report)::jsonb,
    error = sqlc.arg(error)::text,
    finished_at = now()
WHERE id = sqlc.arg(id) AND status = 'pending'
RETURNING *;

-- name: PruneServerCleanupRuns :exec
-- Keeps the newest rows of a server's history.
DELETE FROM server_cleanup_runs r
WHERE r.server_id = sqlc.arg(server_id)
  AND r.id NOT IN (
      SELECT k.id FROM server_cleanup_runs k WHERE k.server_id = sqlc.arg(server_id)
      ORDER BY k.created_at DESC, k.id DESC LIMIT sqlc.arg(keep)::int
  );

-- name: HasPendingServerCleanupRun :one
-- A run the agent has not answered yet. A pending run older than the command TTL
-- plus a day is not counted (its command was lost); normally HandleUndelivered
-- closes such runs.
SELECT EXISTS (
    SELECT 1 FROM server_cleanup_runs
    WHERE server_id = $1 AND status = 'pending' AND created_at > now() - interval '1 day'
);

-- name: LastServerCleanupRunAt :one
-- When the newest standard run of the given triggers started; no row: never.
SELECT created_at FROM server_cleanup_runs
WHERE server_id = sqlc.arg(server_id) AND mode = 'standard' AND trigger = ANY(sqlc.arg(triggers)::text[])
ORDER BY created_at DESC LIMIT 1;

-- name: ListServersDueForCleanup :many
-- Online servers that have cleanup on (no settings row means on) and an agent
-- recent enough to run it, with what the sweep needs to decide between the
-- scheduled and the emergency run: the schedule, and the disk usage of the
-- newest heartbeat sample (for a custom disk trigger).
SELECT s.id, s.workspace_id, s.name, s.created_at, s.facts, s.disk_alert_active, s.disk_alert_threshold_percent,
       COALESCE(c.remove_unmanaged_containers, false) AS remove_unmanaged_containers,
       COALESCE(c.schedule, '0 4 * * *')::text AS schedule,
       COALESCE(c.schedule_changed_at, s.created_at)::timestamptz AS schedule_changed_at,
       c.disk_trigger_percent,
       COALESCE((SELECT m.disk_used_bytes FROM metric_samples m WHERE m.server_id = s.id AND m.service_id IS NULL ORDER BY m.ts DESC LIMIT 1), 0)::bigint AS disk_used_bytes,
       COALESCE((SELECT m.disk_total_bytes FROM metric_samples m WHERE m.server_id = s.id AND m.service_id IS NULL ORDER BY m.ts DESC LIMIT 1), 0)::bigint AS disk_total_bytes
FROM servers s
LEFT JOIN server_cleanup_settings c ON c.server_id = s.id
WHERE s.status = 'online' AND COALESCE(c.enabled, true);

-- name: ListCleanupProtectedReleases :many
-- The releases whose images a cleanup of the server must keep: the current
-- release of every live service, the releases of deployments still in flight
-- and the newest imageRetention succeeded releases of each service (the ones a
-- rollback may pick), all as deployed on this server.
WITH ok AS (
    SELECT d.service_id, d.release_id, max(d.finished_at) AS last_ok
    FROM deployments d
    WHERE d.server_id = sqlc.arg(server_id) AND d.status = 'succeeded' AND d.release_id IS NOT NULL
    GROUP BY d.service_id, d.release_id
), ranked AS (
    SELECT ok.service_id, ok.release_id,
           row_number() OVER (PARTITION BY ok.service_id ORDER BY ok.last_ok DESC NULLS LAST) AS rn
    FROM ok
), wanted AS (
    SELECT rk.release_id
    FROM ranked rk
    JOIN services s ON s.id = rk.service_id AND s.deleted_at IS NULL
    WHERE rk.rn <= GREATEST(COALESCE(NULLIF((s.config->>'imageRetention')::int, 0), 5), 1)
    UNION
    SELECT d.release_id
    FROM deployments d
    JOIN services s ON s.id = d.service_id AND s.deleted_at IS NULL
    WHERE d.server_id = sqlc.arg(server_id) AND d.release_id IS NOT NULL
      AND (d.id = s.current_deployment_id
           OR d.status IN ('created','queued','preparing','building','releasing','verifying'))
)
SELECT r.id, r.service_id, r.source, r.image_ref
FROM releases r
WHERE r.id IN (SELECT release_id FROM wanted)
ORDER BY r.created_at, r.id;

-- name: ListCleanupProtectedVolumes :many
-- Every volume a service on the server uses, including those of deleted
-- services (deleting a service keeps its volumes unless asked otherwise), and
-- the retired volumes a restore swapped out (docker_name is set for those and
-- for volume rows that were swapped; empty means the name derived from the row).
SELECT v.service_id, v.name, v.docker_name
FROM volumes v
JOIN services s ON s.id = v.service_id
WHERE s.server_id = $1
UNION ALL
SELECT r.service_id, r.volume_name AS name, r.docker_name
FROM retired_volumes r
JOIN services s ON s.id = r.service_id
WHERE s.server_id = $1;
