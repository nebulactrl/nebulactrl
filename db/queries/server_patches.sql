-- name: GetServerPatchState :one
SELECT * FROM server_patch_state WHERE server_id = $1;

-- name: UpsertServerPatchState :one
-- The result of a check (or of the check that follows an upgrade).
INSERT INTO server_patch_state (server_id, workspace_id, manager, supported, updates, security_updates, packages,
                                reboot_required, reboot_packages, boot_id, message, checked_at)
VALUES (sqlc.arg(server_id), sqlc.arg(workspace_id), sqlc.arg(manager), sqlc.arg(supported), sqlc.arg(updates),
        sqlc.arg(security_updates), sqlc.narg(packages)::jsonb, sqlc.arg(reboot_required), sqlc.arg(reboot_packages)::text[],
        sqlc.arg(boot_id), sqlc.arg(message), now())
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
INSERT INTO server_patch_runs (server_id, workspace_id, action, trigger, requested_by, security_only, updates_before)
VALUES ($1, $2, $3, $4, $5, $6, $7)
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
       ''::text AS log, message, error, created_at, finished_at
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
