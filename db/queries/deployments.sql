-- name: InsertDeployment :one
INSERT INTO deployments (service_id, release_id, server_id, trigger, actor_type, actor_id, status, strategy, rolled_back_from, generation, message)
VALUES ($1, $2, $3, $4, $5, $6, 'created', $7, $8, $9, $10)
RETURNING *;

-- name: GetDeploymentScope :one
-- Loads a deployment through its service, project and the caller's workspace;
-- a deployment of another workspace is a no-rows result.
SELECT sqlc.embed(d),
       s.environment_id,
       s.slug AS service_slug,
       s.name AS service_name,
       e.project_id,
       p.workspace_id
FROM deployments d
JOIN services s ON s.id = d.service_id
JOIN environments e ON e.id = s.environment_id
JOIN projects p ON p.id = e.project_id
WHERE d.id = sqlc.arg(id)
  AND p.workspace_id = sqlc.arg(workspace_id);

-- name: GetDeploymentScopeInternal :one
-- GetDeploymentScope without the workspace check. For jobs, agent callbacks and
-- paths that already authorized the deployment; never for an id taken from a request.
SELECT sqlc.embed(d),
       s.environment_id,
       s.slug AS service_slug,
       s.name AS service_name,
       e.project_id,
       p.workspace_id
FROM deployments d
JOIN services s ON s.id = d.service_id
JOIN environments e ON e.id = s.environment_id
JOIN projects p ON p.id = e.project_id
WHERE d.id = sqlc.arg(id);

-- name: LockDeployment :one
SELECT * FROM deployments WHERE id = $1 FOR UPDATE;

-- name: ListServiceDeploymentsPage :many
SELECT * FROM deployments
WHERE service_id = sqlc.arg(service_id)
  AND (sqlc.narg(cursor_created_at)::timestamptz IS NULL
       OR (created_at, id) < (sqlc.narg(cursor_created_at)::timestamptz, sqlc.narg(cursor_id)::uuid))
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg(row_limit);

-- name: SetDeploymentStatus :one
UPDATE deployments
SET status = sqlc.arg(status)::text,
    status_message = CASE WHEN sqlc.arg(message)::text = '' THEN status_message ELSE sqlc.arg(message)::text END,
    started_at = CASE WHEN sqlc.arg(mark_started)::bool THEN COALESCE(started_at, now()) ELSE started_at END,
    finished_at = CASE WHEN sqlc.arg(mark_finished)::bool THEN now() ELSE finished_at END
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: SetDeploymentPrepared :exec
UPDATE deployments
SET release_id = $2, strategy = $3, generation = $4, server_id = $5
WHERE id = $1;

-- name: InsertDeploymentEvent :exec
INSERT INTO deployment_events (deployment_id, step, status, message) VALUES ($1, $2, $3, $4);

-- name: ListDeploymentEvents :many
SELECT * FROM deployment_events WHERE deployment_id = $1 ORDER BY id;

-- name: ListDeploymentLogLines :many
SELECT * FROM deployment_logs
WHERE deployment_id = sqlc.arg(deployment_id) AND id > sqlc.arg(after_id)
  AND stream = ANY(sqlc.arg(streams)::text[])
ORDER BY id
LIMIT sqlc.arg(row_limit);

-- name: ListDeploymentCommandViews :many
-- Every agent command of a deployment (deploy, build_image, ...) with the
-- name of the server it went to, oldest first.
SELECT c.*, s.name AS server_name
FROM agent_commands c
JOIN servers s ON s.id = c.server_id
WHERE c.deployment_id = $1
ORDER BY c.created_at, c.id;

-- name: GetDeployCommandByDeployment :one
SELECT * FROM agent_commands
WHERE deployment_id = $1 AND kind = 'deploy'
ORDER BY created_at DESC
LIMIT 1;

-- name: CountReconcileDeploymentsSince :one
SELECT count(*) FROM deployments
WHERE service_id = $1 AND trigger = 'reconcile' AND created_at > $2;

-- name: CountRecentReconcileLimitEvents :one
SELECT count(*) FROM audit_events
WHERE action = 'reconcile.limit_reached' AND resource_id = $1 AND created_at > $2;

-- name: CountActiveDeployments :one
SELECT count(*) FROM deployments
WHERE service_id = $1 AND status IN ('created','queued','preparing','building','releasing','verifying');

-- name: GetActiveDeployment :one
SELECT * FROM deployments
WHERE service_id = $1 AND status IN ('created','queued','preparing','building','releasing','verifying')
LIMIT 1;

-- name: ListEnvironmentDeploymentsPage :many
-- Environment-wide deployments, newest first. statuses / service_id NULL = no filter.
SELECT sqlc.embed(d), s.name AS service_name, s.slug AS service_slug
FROM deployments d
JOIN services s ON s.id = d.service_id AND s.deleted_at IS NULL
WHERE s.environment_id = sqlc.arg(environment_id)
  AND (sqlc.narg(statuses)::text[] IS NULL OR d.status = ANY(sqlc.narg(statuses)::text[]))
  AND (sqlc.narg(service_id)::uuid IS NULL OR d.service_id = sqlc.narg(service_id)::uuid)
  AND (sqlc.narg(cursor_created_at)::timestamptz IS NULL
       OR (d.created_at, d.id) < (sqlc.narg(cursor_created_at)::timestamptz, sqlc.narg(cursor_id)::uuid))
ORDER BY d.created_at DESC, d.id DESC
LIMIT sqlc.arg(row_limit);

-- name: CancelUnackedAgentCommand :one
-- Cancels a command the agent has not acknowledged yet.
UPDATE agent_commands
SET status = 'canceled', error = sqlc.arg(error), finished_at = now()
WHERE id = sqlc.arg(id) AND status IN ('pending', 'sent')
RETURNING *;

-- name: ListFailedDeploymentSteps :many
-- The step of the newest failed step event of each deployment (status rows
-- use the step "status" and are ignored).
SELECT DISTINCT ON (deployment_id) deployment_id, step
FROM deployment_events
WHERE deployment_id = ANY(sqlc.arg(deployment_ids)::uuid[]) AND status = 'failed' AND step <> 'status'
ORDER BY deployment_id, id DESC;

-- name: ListDeploymentActorLabels :many
-- Display names of the users and API tokens behind deployments.actor_id
-- (text, so ids are compared as text). A token deleted since the deployment
-- simply has no row.
SELECT u.id::text AS id, 'user'::text AS kind, COALESCE(NULLIF(u.name, ''), u.email)::text AS label
FROM users u
WHERE u.id::text = ANY(sqlc.arg(ids)::text[])
UNION ALL
SELECT t.id::text, 'token'::text, t.name::text
FROM api_tokens t
WHERE t.id::text = ANY(sqlc.arg(ids)::text[]);

-- name: SetDeploymentBuildInfo :exec
UPDATE deployments SET build_info = sqlc.arg(build_info)::jsonb WHERE id = sqlc.arg(id);

-- name: ListCurrentDeploymentSteps :many
-- The newest step event of each deployment with the status it reported (status
-- rows use the step "status" and are ignored). A step whose newest event is
-- "started" is the one the agent is on right now.
SELECT DISTINCT ON (deployment_id) deployment_id, step, status
FROM deployment_events
WHERE deployment_id = ANY(sqlc.arg(deployment_ids)::uuid[]) AND step <> 'status'
ORDER BY deployment_id, id DESC;

-- name: DeploymentOutcomesPerDay :many
-- Deployments of an environment per UTC day (by creation time) since the given
-- instant, split into succeeded, failed (failed or rollback_failed) and
-- everything else (canceled, rolled_back and still in flight). Days with no
-- deployment have no row.
SELECT (d.created_at AT TIME ZONE 'UTC')::date AS day,
       count(*) FILTER (WHERE d.status = 'succeeded')::int AS succeeded,
       count(*) FILTER (WHERE d.status IN ('failed','rollback_failed'))::int AS failed,
       count(*) FILTER (WHERE d.status NOT IN ('succeeded','failed','rollback_failed'))::int AS other
FROM deployments d
JOIN services s ON s.id = d.service_id AND s.deleted_at IS NULL
WHERE s.environment_id = sqlc.arg(environment_id)
  AND d.created_at >= sqlc.arg(since)::timestamptz
GROUP BY 1
ORDER BY 1;

-- name: RequestDeploymentCancel :one
-- Records that a person asked to cancel a deployment the agent already runs.
-- The status changes only when the agent reports the abort.
UPDATE deployments
SET cancel_requested_at = now(), cancel_requested_by = sqlc.arg(requested_by)::text
WHERE id = sqlc.arg(id) AND status IN ('created', 'queued', 'preparing', 'building', 'releasing', 'verifying')
RETURNING *;

-- name: SetDeploymentCanceledStep :exec
UPDATE deployments SET canceled_at_step = sqlc.arg(step)::text WHERE id = sqlc.arg(id);
