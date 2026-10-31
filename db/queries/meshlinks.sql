-- Cross-server service links (internal/meshlinks).

-- name: ListMeshLinksOfEnvironment :many
SELECT * FROM mesh_links WHERE environment_id = $1 ORDER BY created_at, id;

-- name: ListMeshGrantsOfEnvironment :many
SELECT g.* FROM mesh_grants g
JOIN mesh_links l ON l.id = g.link_id
WHERE l.environment_id = $1
ORDER BY g.created_at, g.ref_id;

-- Environments the periodic repair has to look at: every environment that
-- still has a link or an exposure.
-- name: ListMeshEnvironments :many
SELECT e.environment_id FROM (
    SELECT environment_id FROM mesh_links
    UNION
    SELECT environment_id FROM mesh_exposures
) e
ORDER BY e.environment_id;

-- name: ListMeshExposuresOfService :many
SELECT * FROM mesh_exposures WHERE service_id = $1 ORDER BY purpose;

-- Replicas of an environment: the owners of source='replica' grants.
-- name: ListMeshReplicaRefs :many
SELECT service_id, primary_service_id, state FROM database_replicas WHERE environment_id = $1;
