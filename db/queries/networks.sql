-- name: InsertSharedNetwork :one
INSERT INTO shared_networks (workspace_id, server_id, name, created_by)
VALUES (sqlc.arg(workspace_id), sqlc.arg(server_id), sqlc.arg(name), sqlc.narg(created_by))
RETURNING *;

-- name: GetSharedNetwork :one
-- A network of the workspace; another team's network is indistinguishable from a missing one.
SELECT * FROM shared_networks WHERE id = sqlc.arg(id) AND workspace_id = sqlc.arg(workspace_id);

-- name: ListSharedNetworks :many
SELECT * FROM shared_networks
WHERE workspace_id = sqlc.arg(workspace_id)
  AND (sqlc.narg(server_id)::uuid IS NULL OR server_id = sqlc.narg(server_id)::uuid)
ORDER BY lower(name), id;

-- name: DeleteSharedNetwork :execrows
DELETE FROM shared_networks WHERE id = sqlc.arg(id) AND workspace_id = sqlc.arg(workspace_id);

-- name: ListNetworkAttachments :many
-- Live services attached to the networks of a workspace (optionally one network),
-- with the project context the alias is built from.
SELECT sn.network_id, sn.service_id, s.name AS service_name, s.slug AS service_slug, s.kind AS service_kind,
       s.environment_id, e.name AS env_name, e.slug AS env_slug, p.id AS project_id, p.name AS project_name, p.slug AS project_slug,
       s.server_id AS service_server_id,
       (s.slug || '.' || e.slug || '.' || p.slug)::text AS alias
FROM service_networks sn
JOIN shared_networks n ON n.id = sn.network_id
JOIN services s ON s.id = sn.service_id AND s.deleted_at IS NULL
JOIN environments e ON e.id = s.environment_id
JOIN projects p ON p.id = e.project_id
WHERE n.workspace_id = sqlc.arg(workspace_id)
  AND (sqlc.narg(network_id)::uuid IS NULL OR sn.network_id = sqlc.narg(network_id)::uuid)
ORDER BY p.slug, e.slug, s.slug;

-- name: CountNetworkAttachments :one
-- Live services attached to a network.
SELECT count(*) FROM service_networks sn
JOIN services s ON s.id = sn.service_id AND s.deleted_at IS NULL
WHERE sn.network_id = $1;

-- name: ListServicesNetworks :many
-- The shared networks of services (staged state), with each service's alias.
SELECT sn.service_id, n.id AS network_id, n.name AS network_name, n.server_id AS network_server_id,
       (s.slug || '.' || e.slug || '.' || p.slug)::text AS alias
FROM service_networks sn
JOIN shared_networks n ON n.id = sn.network_id
JOIN services s ON s.id = sn.service_id
JOIN environments e ON e.id = s.environment_id
JOIN projects p ON p.id = e.project_id
WHERE sn.service_id = ANY(sqlc.arg(service_ids)::uuid[])
ORDER BY lower(n.name), n.id;

-- name: AttachServiceNetwork :execrows
INSERT INTO service_networks (service_id, network_id)
VALUES ($1, $2)
ON CONFLICT (service_id, network_id) DO NOTHING;

-- name: DetachServiceNetwork :execrows
DELETE FROM service_networks WHERE service_id = $1 AND network_id = $2;

-- name: ListNetworksByIDs :many
SELECT * FROM shared_networks WHERE id = ANY(sqlc.arg(ids)::uuid[]);

-- name: CountEnvEgressBlockedServices :one
-- Live services of the environment, other than the excluded one, that block
-- outbound internet on the server: in their staged config (so the services of
-- one deploy batch get consistent payloads whatever the order) or in their
-- current release. Every other service of the environment joins the internal
-- network to reach them.
SELECT count(*) FROM services s
LEFT JOIN deployments d ON d.id = s.current_deployment_id
LEFT JOIN releases r ON r.id = d.release_id
WHERE s.environment_id = sqlc.arg(environment_id) AND s.deleted_at IS NULL AND s.kind <> 'route'
  AND s.id <> sqlc.arg(exclude_service_id)
  AND (
    (s.server_id = sqlc.arg(server_id) AND COALESCE((s.config ->> 'blockEgress')::boolean, false))
    OR (d.server_id = sqlc.arg(server_id) AND COALESCE((r.config -> 'config' ->> 'blockEgress')::boolean, false))
  );

-- name: PurgeDeletedNetworkAttachments :exec
-- Attachments of soft-deleted services never block deleting a network.
DELETE FROM service_networks sn USING services s
WHERE s.id = sn.service_id AND s.deleted_at IS NOT NULL AND sn.network_id = $1;

-- name: CountEnvDeployedServices :one
-- Live services of the environment, other than the excluded one, that run on the server.
SELECT count(*) FROM services s
JOIN deployments d ON d.id = s.current_deployment_id
WHERE s.environment_id = sqlc.arg(environment_id) AND s.deleted_at IS NULL AND s.kind <> 'route'
  AND d.server_id = sqlc.arg(server_id) AND s.id <> sqlc.arg(exclude_service_id);
