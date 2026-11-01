-- name: SetServiceEnvironmentBumped :one
-- Moves a service to another environment (and gives it the slug that is free
-- there) and advances the generation: the move is staged until the next deploy.
UPDATE services SET environment_id = $2, slug = $3, generation = generation + 1, updated_at = now()
WHERE id = $1 AND deleted_at IS NULL
RETURNING *;

-- name: DeleteServiceLinks :execrows
-- Removes the manual canvas links a service takes part in (either end).
DELETE FROM service_links WHERE from_service_id = $1 OR to_service_id = $1;

-- name: CountServiceMeshState :one
-- Cross-server exposures, grants and replica groups of a service: state tied to
-- its environment that a move cannot carry.
SELECT (SELECT count(*) FROM mesh_exposures WHERE service_id = sqlc.arg(sid)::uuid)
     + (SELECT count(*) FROM mesh_grants WHERE ref_id = sqlc.arg(sid)::uuid)
     + (SELECT count(*) FROM database_replicas WHERE service_id = sqlc.arg(sid)::uuid OR primary_service_id = sqlc.arg(sid)::uuid) AS n;

-- name: GetEnvironmentLabel :one
-- "<project> / <environment>" for staged-change text.
SELECT (p.name || ' / ' || e.name)::text AS label
FROM environments e JOIN projects p ON p.id = e.project_id
WHERE e.id = $1;

-- name: SetServiceCredentials :exec
-- Stores the re-sealed git token and registry auth of a cloned service.
UPDATE services SET git_token_enc = $2, registry_auth_enc = $3 WHERE id = $1;
