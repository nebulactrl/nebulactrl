-- name: GetServerPatchState :one
SELECT * FROM server_patch_state WHERE server_id = $1;

-- name: UpsertServerPatchState :one
-- The result of a check (or of the check that follows an upgrade).
INSERT INTO server_patch_state (server_id, workspace_id, manager, supported, updates, security_updates, packages,
                                reboot_required, reboot_packages, boot_id, message, native_auto_updates, checked_at)
VALUES (sqlc.arg(server_id), sqlc.arg(workspace_id), sqlc.arg(manager), sqlc.arg(supported), sqlc.arg(updates),
        sqlc.arg(security_updates), sqlc.narg(packages)::jsonb, sqlc.arg(reboot_required), sqlc.arg(reboot_packages)::text[],
        sqlc.arg(boot_id), sqlc.arg(message), sqlc.arg(native_auto_updates), now())
ON CONFLICT (server_id) DO UPDATE
SET manager = EXCLUDED.manager,
    supported = EXCLUDED.supported,
    updates = EXCLUDED.updates,
    security_updates = EXCLUDED.security_updates,
    packages = EXCLUDED.packages,
    reboot_required = EXCLUDED.reboot_required,
    reboot_packages = EXCLUDED.reboot_packages,
    boot_id = EXCLUDED.boot_id,
    message = EXCLUDED.message,
    native_auto_updates = EXCLUDED.native_auto_updates,
    checked_at = now(),
    updated_at = now()
RETURNING *;

-- name: ListServerPatchSummaries :many
-- What the server list shows: the newest check, and the run in flight (if any).
SELECT p.server_id, p.supported, p.updates, p.security_updates, p.reboot_required, p.checked_at,
       COALESCE((
           SELECT r.action FROM server_patch_runs r
           WHERE r.server_id = p.server_id AND r.status IN ('pending', 'rebooting') AND r.created_at > now() - interval '1 day'
           ORDER BY r.created_at DESC LIMIT 1
       ), '')::text AS busy
FROM server_patch_state p
WHERE p.server_id = ANY(sqlc.arg(ids)::uuid[]);

-- name: ListBusyServerPatchRuns :many
-- Servers with a run in flight, for servers without a state row yet.
SELECT DISTINCT ON (server_id) server_id, action FROM server_patch_runs
WHERE server_id = ANY(sqlc.arg(ids)::uuid[]) AND status IN ('pending', 'rebooting') AND created_at > now() - interval '1 day'
ORDER BY server_id, created_at DESC;

-- name: InsertServerPatchRun :one
INSERT INTO server_patch_runs (server_id, workspace_id, action, trigger, requested_by, security_only, updates_before, slot_start)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING *;

-- name: SetServerPatchRunCommand :exec
UPDATE server_patch_runs SET command_id = $2 WHERE id = $1;

-- name: GetServerPatchRun :one
SELECT * FROM server_patch_runs WHERE id = $1 AND server_id = $2;

-- name: GetServerPatchRunByCommand :one
-- The run a command belongs to: its own command, or the check that verifies its reboot.
SELECT * FROM server_patch_runs WHERE command_id = $1 OR verify_command_id = $1
ORDER BY created_at DESC LIMIT 1;

-- name: ListServerPatchRuns :many
-- Newest first, without the log (it can be large; GetServerPatchRun has it).
SELECT id, server_id, workspace_id, action, trigger, status, command_id, verify_command_id, verify_sent_at, requested_by,
       security_only, boot_id_before, rebooting_since, updates_before, updates_after, exit_code,
       ''::text AS log, message, error, created_at, finished_at, slot_start
FROM server_patch_runs WHERE server_id = $1 ORDER BY created_at DESC, id DESC LIMIT $2;

-- name: FinishServerPatchRun :one
-- Records the outcome of a run that is still pending or rebooting, once; a
-- duplicate result changes nothing and returns no row.
UPDATE server_patch_runs
SET status = sqlc.arg(status)::text,
    updates_after = sqlc.narg(updates_after)::integer,
    exit_code = sqlc.narg(exit_code)::integer,
    log = sqlc.arg(log)::text,
    message = sqlc.arg(message)::text,
    error = sqlc.arg(error)::text,
    finished_at = now()
WHERE id = sqlc.arg(id) AND status IN ('pending', 'rebooting')
RETURNING *;

-- name: MarkServerPatchRunRebooting :one
-- The host accepted the reboot: the run now waits for the agent to come back on another boot.
UPDATE server_patch_runs
SET status = 'rebooting', boot_id_before = sqlc.arg(boot_id_before)::text, rebooting_since = now(), message = sqlc.arg(message)::text
WHERE id = sqlc.arg(id) AND status = 'pending'
RETURNING *;

-- name: SetServerPatchRunVerify :exec
UPDATE server_patch_runs SET verify_command_id = $2, verify_sent_at = now() WHERE id = $1;

-- name: ListRebootingServerPatchRuns :many
-- Runs waiting for their server to come back, with what the watcher needs.
SELECT sqlc.embed(r), s.status AS server_status, s.last_seen_at AS server_last_seen_at
FROM server_patch_runs r JOIN servers s ON s.id = r.server_id
WHERE r.status = 'rebooting'
ORDER BY r.created_at;

-- name: HasActiveServerPatchRun :one
-- A run the agent has not finished. A run older than a day is not counted (its
-- command was lost); normally HandleUndelivered closes such runs.
SELECT EXISTS (
    SELECT 1 FROM server_patch_runs
    WHERE server_id = $1 AND status IN ('pending', 'rebooting') AND created_at > now() - interval '1 day'
);

-- name: PruneServerPatchRuns :exec
DELETE FROM server_patch_runs r
WHERE r.server_id = sqlc.arg(server_id)
  AND r.id NOT IN (
      SELECT k.id FROM server_patch_runs k WHERE k.server_id = sqlc.arg(server_id)
      ORDER BY k.created_at DESC, k.id DESC LIMIT sqlc.arg(keep)::int
  );

-- name: ListServersDueForPatchCheck :many
-- Online servers that have had no run (check, upgrade or reboot, any outcome)
-- for the interval: a server whose checks fail is retried at the same pace, not
-- every sweep.
SELECT s.id, s.workspace_id, s.name, s.facts
FROM servers s
WHERE s.status = 'online'
  AND NOT EXISTS (
      SELECT 1 FROM server_patch_runs r
      WHERE r.server_id = s.id AND r.created_at > now() - make_interval(secs => sqlc.arg(every_seconds)::float8)
  );

-- name: ListServerProductionServices :many
-- Live services placed on the server that belong to a production environment.
SELECT sv.id AS service_id, sv.name AS service_name, e.id AS environment_id, e.name AS environment_name,
       p.id AS project_id, p.name AS project_name
FROM services sv
JOIN environments e ON e.id = sv.environment_id
JOIN projects p ON p.id = e.project_id
WHERE sv.server_id = $1 AND sv.deleted_at IS NULL AND e.is_production
ORDER BY p.name, sv.name;

-- name: CountServerLiveServices :one
SELECT count(*)::int FROM services WHERE server_id = $1 AND deleted_at IS NULL;

-- ---- automatic updates ----

-- name: GetServerPatchAutoSettings :one
SELECT * FROM server_patch_auto_settings WHERE server_id = $1;

-- name: UpsertServerPatchAutoSettings :one
-- Saved by a person: a new save starts the failure count over, clears the reason
-- an earlier run of failures switched the schedule off, and makes the window
-- already under way wait for the next night (settings_changed_at).
INSERT INTO server_patch_auto_settings (server_id, workspace_id, enabled, scope, window_start, window_minutes, timezone, reboot,
                                        hold_packages, updated_by)
VALUES (sqlc.arg(server_id), sqlc.arg(workspace_id), sqlc.arg(enabled), sqlc.arg(scope), sqlc.arg(window_start),
        sqlc.arg(window_minutes), sqlc.arg(timezone), sqlc.arg(reboot), sqlc.arg(hold_packages)::text[], sqlc.narg(updated_by))
ON CONFLICT (server_id) DO UPDATE
SET enabled = EXCLUDED.enabled,
    scope = EXCLUDED.scope,
    window_start = EXCLUDED.window_start,
    window_minutes = EXCLUDED.window_minutes,
    timezone = EXCLUDED.timezone,
    reboot = EXCLUDED.reboot,
    hold_packages = EXCLUDED.hold_packages,
    consecutive_failures = 0,
    disabled_reason = '',
    settings_changed_at = now(),
    updated_by = EXCLUDED.updated_by,
    updated_at = now()
RETURNING *;

-- name: ListOnlineServerPatchAuto :many
-- The schedules the sweep looks at: enabled, on an online server, with what the
-- due decision needs from the server and its last check.
SELECT sqlc.embed(a), s.name AS server_name, s.facts AS server_facts, s.is_control_plane AS server_is_control_plane,
       p.checked_at AS state_checked_at, COALESCE(p.updates, 0)::int AS state_updates,
       COALESCE(p.security_updates, 0)::int AS state_security_updates
FROM server_patch_auto_settings a
JOIN servers s ON s.id = a.server_id
LEFT JOIN server_patch_state p ON p.server_id = a.server_id
WHERE a.enabled AND s.status = 'online'
ORDER BY a.server_id;

-- name: ListServersNeedingNativeHandoff :many
-- Online servers whose host automatic updates do not yet match the schedule
-- (enabled but not handed over, or disabled but still handed over) and that had
-- no run for the interval, so an agent that cannot switch them is not asked in a loop.
SELECT s.id, s.facts
FROM server_patch_auto_settings a
JOIN servers s ON s.id = a.server_id
WHERE a.enabled <> a.native_handoff AND s.status = 'online'
  AND NOT EXISTS (
      SELECT 1 FROM server_patch_runs r
      WHERE r.server_id = s.id AND r.created_at > now() - make_interval(secs => sqlc.arg(every_seconds)::float8)
  )
ORDER BY s.id;

-- name: SetServerPatchAutoNativeHandoff :exec
UPDATE server_patch_auto_settings SET native_handoff = $2 WHERE server_id = $1 AND native_handoff <> $2;

-- name: RecordServerPatchAutoFailure :one
-- A scheduled run failed: count it, and switch the schedule off at max_failures in a row.
UPDATE server_patch_auto_settings
SET consecutive_failures = consecutive_failures + 1,
    enabled = enabled AND consecutive_failures + 1 < sqlc.arg(max_failures)::int,
    disabled_reason = CASE WHEN enabled AND consecutive_failures + 1 >= sqlc.arg(max_failures)::int
                           THEN sqlc.arg(reason)::text ELSE disabled_reason END,
    updated_at = now()
WHERE server_id = sqlc.arg(server_id)
RETURNING *;

-- name: ResetServerPatchAutoFailures :exec
UPDATE server_patch_auto_settings SET consecutive_failures = 0 WHERE server_id = $1 AND consecutive_failures <> 0;

-- name: HasScheduledPatchApplyForSlot :one
SELECT EXISTS (
    SELECT 1 FROM server_patch_runs
    WHERE server_id = $1 AND slot_start = $2 AND action = 'apply' AND trigger = 'scheduled'
);

-- name: HasScheduledPatchRunInFlight :one
-- A scheduled upgrade or reboot is in flight on any server: only one runs at a
-- time on the instance. A run older than 6 hours is not counted (its command was lost).
SELECT EXISTS (
    SELECT 1 FROM server_patch_runs
    WHERE trigger = 'scheduled' AND action IN ('apply', 'reboot') AND status IN ('pending', 'rebooting')
      AND created_at > now() - interval '6 hours'
);

-- name: HasRecentScheduledPatchReboot :one
SELECT EXISTS (
    SELECT 1 FROM server_patch_runs
    WHERE server_id = $1 AND trigger = 'scheduled' AND action = 'reboot' AND created_at > now() - interval '24 hours'
);

-- name: HasServerBusyWork :one
-- A deployment or build (here as the target or the build server), a backup or a
-- restore is running on the server. Work older than 12 hours is not counted.
SELECT (
    EXISTS (
        SELECT 1 FROM deployments d
        WHERE (d.server_id = sqlc.arg(server_id) OR d.build_server_id = sqlc.arg(server_id))
          AND d.status IN ('created', 'queued', 'preparing', 'building', 'releasing', 'verifying')
          AND d.created_at > now() - interval '12 hours'
    ) OR EXISTS (
        SELECT 1 FROM backups b
        WHERE b.server_id = sqlc.arg(server_id) AND b.status IN ('queued', 'running') AND b.created_at > now() - interval '12 hours'
    ) OR EXISTS (
        SELECT 1 FROM backup_restores r
        JOIN services sv ON sv.id = r.source_service_id OR sv.id = r.target_service_id
        WHERE sv.server_id = sqlc.arg(server_id) AND r.status IN ('provisioning', 'pre_backup', 'restoring')
          AND r.created_at > now() - interval '12 hours'
    )
)::boolean AS busy;

-- name: LastScheduledPatchApply :one
SELECT id, status, created_at, finished_at FROM server_patch_runs
WHERE server_id = $1 AND action = 'apply' AND trigger = 'scheduled'
ORDER BY created_at DESC, id DESC LIMIT 1;

-- name: ListServerHourlyRequests :many
-- Requests per hour the server served in the last 14 days, for the quiet-hour suggestion.
SELECT bucket, sum(requests)::bigint AS requests
FROM traffic_1h
WHERE server_id = $1 AND bucket >= now() - interval '14 days'
GROUP BY bucket
ORDER BY bucket;
