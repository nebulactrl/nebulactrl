-- ===== Combine N services into one service with several processes =========================

-- name: InsertCombine :one
INSERT INTO service_combines (environment_id, target_service_id, source_service_ids, plan, state, created_by)
VALUES (sqlc.arg(environment_id), sqlc.arg(target_service_id), sqlc.arg(source_service_ids)::uuid[], sqlc.arg(plan), 'staged', sqlc.arg(created_by))
RETURNING *;

-- name: GetCombine :one
SELECT * FROM service_combines WHERE id = $1;

-- name: ListCombinesForEnvironment :many
SELECT * FROM service_combines
WHERE environment_id = $1
ORDER BY created_at DESC, id DESC;

-- name: AdvanceCombine :one
-- Compare-and-swap on the state: no row means another actor moved the combine. A new deployment_id
-- replaces the stored one; an empty error clears it. finished_at is set on done, failed and aborted
-- and cleared again by a retry.
UPDATE service_combines
SET state = sqlc.arg(to_state)::text,
    deployment_id = COALESCE(sqlc.narg(deployment_id)::uuid, deployment_id),
    error = sqlc.arg(error)::text,
    finished_at = CASE WHEN sqlc.arg(to_state)::text IN ('done', 'failed', 'aborted') THEN now() ELSE NULL END,
    updated_at = now()
WHERE id = sqlc.arg(id) AND state = sqlc.arg(from_state)::text
RETURNING *;

-- name: LockCombine :one
SELECT * FROM service_combines WHERE id = $1 FOR UPDATE;

-- name: UpdateReleaseConfig :exec
-- Replaces the config snapshot of a release. Used once, by the combine cutover, to add the moved
-- domains to the target's current release.
UPDATE releases SET config = sqlc.arg(config) WHERE id = sqlc.arg(id);

-- name: MoveDomainsToService :many
-- Re-homes domains onto another service; `component` is the process name there ('' = default process).
UPDATE domains
SET service_id = sqlc.arg(service_id), component = sqlc.arg(component)
WHERE id = ANY(sqlc.arg(domain_ids)::uuid[])
RETURNING *;

-- name: MoveServiceTasks :exec
UPDATE service_tasks
SET service_id = sqlc.arg(to_service_id), updated_at = now()
WHERE service_id = sqlc.arg(from_service_id);
