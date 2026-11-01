-- name: InsertRoutePort :exec
INSERT INTO route_ports (service_id, server_id, protocol, listen_port)
VALUES ($1, $2, $3, $4);

-- name: DeleteRoutePort :exec
DELETE FROM route_ports WHERE service_id = $1;

-- name: GetRoutePort :one
SELECT * FROM route_ports WHERE service_id = $1;

-- name: GetRoutePortHolder :one
-- The route service that holds a listen port of a server.
SELECT rp.service_id, s.name AS service_name, s.slug AS service_slug, e.project_id
FROM route_ports rp
JOIN services s ON s.id = rp.service_id AND s.deleted_at IS NULL
JOIN environments e ON e.id = s.environment_id
WHERE rp.server_id = $1 AND rp.protocol = $2 AND rp.listen_port = $3;

-- name: ListServerRoutePorts :many
SELECT rp.service_id, rp.server_id, rp.protocol, rp.listen_port,
       s.name AS service_name, s.slug AS service_slug, s.current_deployment_id, e.project_id
FROM route_ports rp
JOIN services s ON s.id = rp.service_id AND s.deleted_at IS NULL
JOIN environments e ON e.id = s.environment_id
WHERE rp.server_id = $1
ORDER BY rp.protocol, rp.listen_port;

-- name: ListServerAppliedRoutePorts :many
-- Listen ports of route services whose current deployment runs on the server
-- (the L4 entrypoints the proxy has now).
SELECT rp.protocol, rp.listen_port
FROM route_ports rp
JOIN services s ON s.id = rp.service_id AND s.deleted_at IS NULL
JOIN deployments d ON d.id = s.current_deployment_id
WHERE d.server_id = $1 AND rp.server_id = $1
ORDER BY rp.protocol, rp.listen_port;

-- name: ListServerRouteServices :many
-- Live route services whose current deployment runs on the server, with the
-- snapshot config of that deployment's release.
SELECT s.id AS service_id, s.slug AS service_slug, s.name AS service_name, s.config AS service_config,
       s.maintenance_enabled, s.maintenance_title, s.maintenance_message, s.maintenance_eta,
       d.id AS deployment_id, rel.config AS release_config
FROM deployments d
JOIN services s ON s.id = d.service_id AND s.current_deployment_id = d.id
LEFT JOIN releases rel ON rel.id = d.release_id
WHERE d.server_id = $1 AND s.kind = 'route' AND s.deleted_at IS NULL
ORDER BY s.created_at, s.id;

-- name: ListManagedTargets :many
-- Current deployments of the services a route service points at.
SELECT s.id AS service_id, s.slug AS service_slug, s.environment_id, s.replicas, d.id AS deployment_id, d.server_id, rel.config AS release_config
FROM services s
JOIN deployments d ON d.id = s.current_deployment_id
LEFT JOIN releases rel ON rel.id = d.release_id
WHERE s.id = ANY(sqlc.arg(service_ids)::uuid[]) AND s.deleted_at IS NULL;

-- name: ListEnvironmentRouteServices :many
SELECT * FROM services
WHERE environment_id = $1 AND kind = 'route' AND deleted_at IS NULL
ORDER BY created_at, id;

-- name: ListRouteServicesTargeting :many
-- Live route services of a workspace whose upstreams reference the given service.
SELECT s.* FROM services s
JOIN environments e ON e.id = s.environment_id
JOIN projects p ON p.id = e.project_id
WHERE p.workspace_id = sqlc.arg(workspace_id) AND s.kind = 'route' AND s.deleted_at IS NULL
  AND s.config->'route'->'upstreams' @> jsonb_build_array(jsonb_build_object('serviceId', sqlc.arg(target_id)::text))
ORDER BY s.created_at, s.id;

-- name: ListServerRouteServiceIDs :many
-- Route services assigned to a server (drain skips them).
SELECT id FROM services WHERE server_id = $1 AND kind = 'route' AND deleted_at IS NULL;

-- name: LoadServiceNameByID :one
SELECT name FROM services WHERE id = $1;
