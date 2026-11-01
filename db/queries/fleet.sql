-- name: ListFleetServices :many
-- Live services placed on a server, with project/environment context, volume
-- and domain counts and the status of their newest deployment. server_id NULL
-- lists every placed service of the workspace.
SELECT sv.id, sv.name, sv.slug, sv.kind, sv.config, sv.generation, sv.applied_generation,
       sv.server_id, sv.current_deployment_id, sv.observed,
       p.id AS project_id, p.name AS project_name,
       e.id AS environment_id, e.name AS environment_name, e.slug AS environment_slug,
       (SELECT count(*) FROM volumes v WHERE v.service_id = sv.id)::int AS volume_count,
       (SELECT count(*) FROM domains dm WHERE dm.service_id = sv.id)::int AS domain_count,
       COALESCE((SELECT d.status FROM deployments d WHERE d.service_id = sv.id ORDER BY d.created_at DESC, d.id DESC LIMIT 1), '')::text AS latest_status
FROM services sv
JOIN environments e ON e.id = sv.environment_id
JOIN projects p ON p.id = e.project_id
WHERE p.workspace_id = sqlc.arg(workspace_id)
  AND sv.deleted_at IS NULL
  AND sv.kind <> 'route' -- route services have no container: fleet overview and drains skip them
  AND sv.server_id IS NOT NULL
  AND (sqlc.narg(server_id)::uuid IS NULL OR sv.server_id = sqlc.narg(server_id)::uuid)
ORDER BY p.name, e.name, sv.name, sv.id;

-- name: ListServersForFleet :many
SELECT * FROM servers WHERE workspace_id = $1 ORDER BY created_at, id;

-- name: LatestServiceMetricSamples :many
-- Newest sample instant per service within the window; replicas are summed.
SELECT m.service_id, sum(m.cpu_percent)::float8 AS cpu_percent, sum(m.mem_bytes)::bigint AS mem_bytes, sum(m.mem_limit_bytes)::bigint AS mem_limit_bytes
FROM metric_samples m
JOIN (
    SELECT service_id, max(ts) AS ts
    FROM metric_samples
    WHERE service_id = ANY(sqlc.arg(service_ids)::uuid[]) AND ts > sqlc.arg(since)::timestamptz
    GROUP BY service_id
) l ON l.service_id = m.service_id AND l.ts = m.ts
GROUP BY m.service_id;

-- name: InsertServerDrain :one
INSERT INTO server_drains (server_id, workspace_id, moves, actor_type, actor_id, actor_label)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: GetServerDrain :one
-- The running drain of a server, else its newest finished one.
SELECT * FROM server_drains
WHERE server_id = $1
ORDER BY (state = 'running') DESC, created_at DESC
LIMIT 1;

-- name: GetServerDrainByID :one
SELECT * FROM server_drains WHERE id = $1;

-- name: UpdateServerDrainMoves :exec
UPDATE server_drains SET moves = $2 WHERE id = $1;

-- name: FinishServerDrain :exec
UPDATE server_drains SET state = $2, message = $3, moves = $4, finished_at = now() WHERE id = $1;

-- name: RequestServerDrainCancel :one
UPDATE server_drains SET cancel_requested = true WHERE server_id = $1 AND state = 'running' RETURNING *;

-- name: SetServerDrainState :one
UPDATE servers
SET drain_state = sqlc.arg(drain_state),
    drain_message = sqlc.arg(drain_message),
    drain_started_at = CASE WHEN sqlc.arg(drain_state)::text = 'draining' THEN now()
                            WHEN sqlc.arg(drain_state)::text = 'none' THEN NULL
                            ELSE drain_started_at END
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: RevertServicePlacement :execrows
-- Undoes a placement change of a move whose deployment failed, but only when
-- nothing else touched the service since.
UPDATE services
SET server_id = sqlc.arg(server_id), generation = sqlc.arg(generation), updated_at = now()
WHERE id = sqlc.arg(id) AND generation = sqlc.arg(expected_generation) AND server_id = sqlc.arg(current_server_id) AND deleted_at IS NULL;

-- name: GetDeploymentStatus :one
SELECT id, status, status_message, trigger FROM deployments WHERE id = $1;

-- name: GetServiceCurrentCommit :one
-- Release source of the service's current deployment.
SELECT r.source
FROM services sv
JOIN deployments d ON d.id = sv.current_deployment_id
JOIN releases r ON r.id = d.release_id
WHERE sv.id = $1;
