-- ===== Server adoption: unmanaged services and rows rebuilt with the IDs of the running containers =====

-- name: ListServerUnmanagedServices :many
SELECT service_id FROM server_unmanaged_services WHERE server_id = $1 ORDER BY service_id;

-- name: InsertServerUnmanagedService :exec
INSERT INTO server_unmanaged_services (server_id, service_id) VALUES ($1, $2)
ON CONFLICT DO NOTHING;

-- name: DeleteServerUnmanagedService :exec
DELETE FROM server_unmanaged_services WHERE server_id = $1 AND service_id = $2;

-- name: CreateEnvironmentWithID :one
INSERT INTO environments (id, project_id, name, slug, is_production)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: InsertAdoptedDeployment :one
-- The deployment of containers that already run: it is recorded as succeeded,
-- no deploy runs.
INSERT INTO deployments (id, service_id, release_id, server_id, trigger, actor_type, actor_id, status, strategy, generation, message, started_at, finished_at)
VALUES ($1, $2, $3, $4, 'adopt', $5, $6, 'succeeded', 'rolling', $7, $8, now(), now())
RETURNING *;

-- name: InsertVolumeWithDockerName :one
INSERT INTO volumes (service_id, name, mount_path, owner, docker_name) VALUES ($1, $2, $3, $4, $5) RETURNING *;

-- name: SetServiceReplicas :exec
-- Operational setting only: generation is NOT bumped.
UPDATE services SET replicas = $2, updated_at = now() WHERE id = $1;

-- name: ReattachDeploymentServer :exec
UPDATE deployments SET server_id = $2 WHERE id = $1;

-- name: ReattachServiceServer :exec
UPDATE services SET server_id = $2, updated_at = now() WHERE id = $1;

-- name: ListServicesForAdoption :many
-- The services with the given IDs in any team, deleted ones included: the IDs come from the labels of
-- containers on a host, so they may belong to another team or to a service this instance deleted.
-- dep_server_status is the status of the server the current deployment ran on ('' when it has none).
SELECT s.id, s.environment_id, s.slug, s.deleted_at, s.current_deployment_id, p.workspace_id,
       d.server_id AS dep_server_id,
       COALESCE(srv.status, '')::text AS dep_server_status
FROM services s
JOIN environments e ON e.id = s.environment_id
JOIN projects p ON p.id = e.project_id
LEFT JOIN deployments d ON d.id = s.current_deployment_id
LEFT JOIN servers srv ON srv.id = d.server_id
WHERE s.id = ANY(sqlc.arg(ids)::uuid[]);

-- name: ListDomainKeysByHostnames :many
SELECT hostname, path_prefix FROM domains WHERE hostname = ANY(sqlc.arg(hostnames)::text[]);

-- name: UpsertProcessReplicas :exec
-- The replicas of a named process of an adopted service: operational setting, no scale event.
INSERT INTO service_process_scale (service_id, process, replicas)
VALUES (sqlc.arg(service_id), sqlc.arg(process), sqlc.arg(replicas))
ON CONFLICT (service_id, process) DO UPDATE SET replicas = EXCLUDED.replicas, updated_at = now();

-- name: ReattachReleaseServer :exec
-- The release a reattached deployment runs records the server it ran on; it now runs on this one, so the
-- service does not show a staged server change.
UPDATE releases SET config = jsonb_set(config, '{serverId}', to_jsonb(sqlc.arg(server_id)::text))
WHERE id = (SELECT d.release_id FROM deployments d WHERE d.id = sqlc.arg(deployment_id));
