-- ===== Processes of a service: replicas and autoscaling of the named processes ===========
-- The default process keeps services.replicas / autoscaling / last_scale_*; these rows are the named
-- processes only. A missing row means 1 replica and the default autoscaling.

-- name: ListProcessScales :many
SELECT * FROM service_process_scale
WHERE service_id = $1
ORDER BY process;

-- name: ListProcessScalesForServices :many
SELECT * FROM service_process_scale
WHERE service_id = ANY(sqlc.arg(service_ids)::uuid[])
ORDER BY service_id, process;

-- name: EnsureProcessScale :one
-- Inserts the default row of a process when it has none and returns the row either way, so a
-- compare-and-swap (ApplyProcessScale) always has something to swap.
INSERT INTO service_process_scale (service_id, process)
VALUES (sqlc.arg(service_id), sqlc.arg(process))
ON CONFLICT (service_id, process) DO UPDATE SET process = service_process_scale.process
RETURNING *;

-- name: ApplyProcessScale :one
-- Compare-and-swap on the replica count: no row means a concurrent scale.
-- Operational settings only: services.generation is NOT bumped (never a staged change).
UPDATE service_process_scale
SET replicas = sqlc.arg(replicas),
    last_scale_at = now(),
    last_scale_event = sqlc.arg(last_scale_event),
    scale_blocked_reason = NULL,
    updated_at = now()
WHERE service_id = sqlc.arg(service_id) AND process = sqlc.arg(process) AND replicas = sqlc.arg(expected_replicas)
RETURNING *;

-- name: SetProcessAutoscaling :one
-- Upsert: a process that was never scaled gets its row here.
INSERT INTO service_process_scale (service_id, process, autoscaling)
VALUES (sqlc.arg(service_id), sqlc.arg(process), sqlc.arg(autoscaling))
ON CONFLICT (service_id, process) DO UPDATE
SET autoscaling = EXCLUDED.autoscaling, scale_blocked_reason = NULL, updated_at = now()
RETURNING *;

-- name: SetProcessScaleBlocked :one
-- Returns a row only when the stored reason changed, so callers publish once.
UPDATE service_process_scale
SET scale_blocked_reason = sqlc.narg(reason)::text
WHERE service_id = sqlc.arg(service_id) AND process = sqlc.arg(process)
  AND scale_blocked_reason IS DISTINCT FROM sqlc.narg(reason)::text
RETURNING *;

-- name: PruneProcessScales :exec
-- Deletes the rows of processes that are not in `keep` (the named processes of the deployed config).
DELETE FROM service_process_scale
WHERE service_id = sqlc.arg(service_id) AND NOT (process = ANY(sqlc.arg(keep)::text[]));

-- name: DeleteProcessScale :exec
DELETE FROM service_process_scale
WHERE service_id = sqlc.arg(service_id) AND process = sqlc.arg(process);

-- name: CopyProcessScale :exec
-- Gives to_process a copy of the settings of from_process (nothing when it has no row). The target
-- must have no row: delete it first (a leftover of a removed process would hit the primary key).
INSERT INTO service_process_scale (service_id, process, replicas, autoscaling, last_scale_at, last_scale_event, scale_blocked_reason)
SELECT s.service_id, sqlc.arg(to_process)::text, s.replicas, s.autoscaling, s.last_scale_at, s.last_scale_event, s.scale_blocked_reason
FROM service_process_scale s
WHERE s.service_id = sqlc.arg(service_id) AND s.process = sqlc.arg(from_process);

-- name: ListProcessRenames :many
-- Staged renames of named processes: the running release still has from_process, the staged config to_process.
SELECT * FROM service_process_rename
WHERE service_id = $1
ORDER BY to_process;

-- name: UpsertProcessRename :exec
INSERT INTO service_process_rename (service_id, from_process, to_process)
VALUES (sqlc.arg(service_id), sqlc.arg(from_process), sqlc.arg(to_process))
ON CONFLICT (service_id, to_process) DO UPDATE SET from_process = EXCLUDED.from_process;

-- name: DeleteProcessRename :exec
DELETE FROM service_process_rename
WHERE service_id = sqlc.arg(service_id) AND to_process = sqlc.arg(to_process);

-- name: DeleteStaleProcessRenames :exec
-- Drops the renames the staged config no longer carries: the new name is gone from `keep` (the named
-- processes of the config) or the old name is back in it.
DELETE FROM service_process_rename
WHERE service_id = sqlc.arg(service_id)
  AND (NOT (to_process = ANY(sqlc.arg(keep)::text[])) OR from_process = ANY(sqlc.arg(keep)::text[]));

-- name: SetProcessDomainsPort :exec
-- Follows a change of the port of a named process: the domains that route to it (component = its name).
UPDATE domains
SET target_port = sqlc.arg(target_port)
WHERE service_id = sqlc.arg(service_id) AND component = sqlc.arg(component) AND target_port <> sqlc.arg(target_port);

-- name: ListAutoscalingProcesses :many
-- Named processes with autoscaling enabled, with their service and the server of its current deployment.
SELECT sqlc.embed(s), sqlc.embed(p), d.server_id AS deployment_server_id
FROM service_process_scale p
JOIN services s ON s.id = p.service_id
JOIN deployments d ON d.id = s.current_deployment_id
WHERE s.deleted_at IS NULL
  AND d.server_id IS NOT NULL
  AND (p.autoscaling ->> 'enabled')::boolean
ORDER BY s.created_at, s.id, p.process;

-- name: CountActiveCombinesForService :one
-- Active combines (staged, deploying or in cutover) that have the service as target or as a source.
SELECT count(*)::int FROM service_combines
WHERE state IN ('staged', 'deploying', 'cutover')
  AND (target_service_id = sqlc.arg(service_id) OR sqlc.arg(service_id) = ANY(source_service_ids));
