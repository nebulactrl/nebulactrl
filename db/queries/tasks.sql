-- Cron tasks -----------------------------------------------------------------

-- name: InsertServiceTask :one
INSERT INTO service_tasks (service_id, name, schedule, timezone, command, enabled, timeout_seconds, concurrency, next_run_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
RETURNING *;

-- name: GetServiceTask :one
SELECT * FROM service_tasks WHERE id = $1;

-- name: ListServiceTasks :many
SELECT * FROM service_tasks WHERE service_id = $1 ORDER BY lower(name), id;

-- name: UpdateServiceTask :one
UPDATE service_tasks SET
    name = sqlc.arg(name),
    schedule = sqlc.arg(schedule),
    timezone = sqlc.arg(timezone),
    command = sqlc.arg(command),
    enabled = sqlc.arg(enabled),
    timeout_seconds = sqlc.arg(timeout_seconds),
    concurrency = sqlc.arg(concurrency),
    next_run_at = sqlc.narg(next_run_at),
    updated_at = now()
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: DeleteServiceTask :exec
DELETE FROM service_tasks WHERE id = $1;

-- name: ListDueServiceTasks :many
-- Locked so two control-plane instances never start the same tick.
SELECT * FROM service_tasks
WHERE enabled AND next_run_at IS NOT NULL AND next_run_at <= $1
ORDER BY next_run_at, id
LIMIT 500
FOR UPDATE SKIP LOCKED;

-- name: AdvanceServiceTask :exec
UPDATE service_tasks SET last_run_at = $2, next_run_at = $3 WHERE id = $1;

-- Runs -----------------------------------------------------------------------

-- name: InsertTaskRun :one
INSERT INTO task_runs (
    workspace_id, service_id, task_id, name, source, command, status, error, timeout_seconds,
    server_id, actor_label, scheduled_for, finished_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
RETURNING *;

-- name: SetTaskRunCommand :exec
UPDATE task_runs SET command_id = $2 WHERE id = $1;

-- name: GetTaskRunInternal :one
SELECT * FROM task_runs WHERE id = $1;

-- name: GetTaskRunByCommand :one
SELECT * FROM task_runs WHERE command_id = $1;

-- name: GetTaskRunView :one
-- status is the effective status: a queued run whose command the agent has
-- acknowledged is running.
SELECT sqlc.embed(r),
       (CASE WHEN r.status = 'queued' AND c.status = 'acked' THEN 'running' ELSE r.status END)::text AS effective_status
FROM task_runs r
LEFT JOIN agent_commands c ON c.id = r.command_id
WHERE r.id = $1 AND r.workspace_id = $2;

-- name: GetTaskRunViewInternal :one
SELECT sqlc.embed(r),
       (CASE WHEN r.status = 'queued' AND c.status = 'acked' THEN 'running' ELSE r.status END)::text AS effective_status
FROM task_runs r
LEFT JOIN agent_commands c ON c.id = r.command_id
WHERE r.id = $1;

-- name: ListServiceTaskRuns :many
-- A service's runs (cron, manual and one-off), newest first.
SELECT sqlc.embed(r),
       (CASE WHEN r.status = 'queued' AND c.status = 'acked' THEN 'running' ELSE r.status END)::text AS effective_status
FROM task_runs r
LEFT JOIN agent_commands c ON c.id = r.command_id
WHERE r.service_id = sqlc.arg(service_id)
  AND (sqlc.narg(task_id)::uuid IS NULL OR r.task_id = sqlc.narg(task_id))
ORDER BY r.created_at DESC, r.id DESC
LIMIT sqlc.arg(row_limit);

-- name: ListLatestTaskRuns :many
-- The newest run of each task of a service.
SELECT DISTINCT ON (r.task_id) sqlc.embed(r),
       (CASE WHEN r.status = 'queued' AND c.status = 'acked' THEN 'running' ELSE r.status END)::text AS effective_status
FROM task_runs r
LEFT JOIN agent_commands c ON c.id = r.command_id
WHERE r.service_id = $1 AND r.task_id IS NOT NULL
ORDER BY r.task_id, r.created_at DESC, r.id DESC;

-- name: CountActiveTaskRuns :one
SELECT count(*)::bigint FROM task_runs WHERE task_id = $1 AND status IN ('queued','running');

-- name: FinishTaskRun :one
-- Records the outcome of a run that is still active; a finished run is left
-- untouched and not returned, which makes duplicate results idempotent.
UPDATE task_runs
SET status = sqlc.arg(status), exit_code = sqlc.narg(exit_code), error = sqlc.arg(error),
    started_at = COALESCE(sqlc.narg(started_at), started_at, created_at),
    finished_at = COALESCE(sqlc.narg(finished_at), now())
WHERE id = sqlc.arg(id) AND status IN ('queued','running')
RETURNING *;

-- name: FailStaleTaskRuns :many
-- Runs whose agent never reported back: their timeout plus a grace period has
-- long passed. (Acked commands never expire, so the run itself must.)
UPDATE task_runs
SET status = 'failed', finished_at = now(),
    error = 'The agent never reported a result for this run (it may have been restarted or lost). Check the task output and the server.'
WHERE status IN ('queued','running') AND created_at + make_interval(secs => timeout_seconds + 600) < now()
RETURNING *;

-- name: PruneTaskRuns :execrows
-- Keeps the newest row_limit finished runs of one task (or, with a null
-- task_id, of the service's one-off runs); active runs are never removed.
DELETE FROM task_runs
WHERE id IN (
    SELECT t.id FROM task_runs t
    WHERE t.service_id = sqlc.arg(service_id)
      AND t.task_id IS NOT DISTINCT FROM sqlc.narg(task_id)::uuid
      AND t.status NOT IN ('queued','running')
    ORDER BY t.created_at DESC, t.id DESC
    OFFSET sqlc.arg(keep)
);

-- name: ListTaskRunLogs :many
SELECT id, stream, line, created_at FROM task_run_logs
WHERE run_id = $1 AND id > $2
ORDER BY id
LIMIT $3;

-- name: CountTaskRunLogs :one
SELECT count(*)::bigint FROM task_run_logs WHERE run_id = $1;

-- name: SetTaskRunLogLines :exec
UPDATE task_runs SET log_lines = $2 WHERE id = $1;

-- Release lookup -------------------------------------------------------------

-- name: GetServiceCurrentRelease :one
-- The release the service's current deployment runs, with that deployment.
SELECT d.id AS deployment_id, d.server_id AS deployment_server_id, r.*
FROM services s
JOIN deployments d ON d.id = s.current_deployment_id
JOIN releases r ON r.id = d.release_id
WHERE s.id = $1;
