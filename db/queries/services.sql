-- name: GetServiceScope :one
-- Loads a live service with its environment/project context inside the caller's
-- workspace; a service of another workspace is a no-rows result.
SELECT sqlc.embed(s),
       e.project_id,
       e.slug AS env_slug,
       e.name AS env_name,
       e.is_production,
       p.slug AS project_slug,
       p.name AS project_name,
       p.system AS project_system,
       p.workspace_id
FROM services s
JOIN environments e ON e.id = s.environment_id
JOIN projects p ON p.id = e.project_id
WHERE s.id = sqlc.arg(id)
  AND s.deleted_at IS NULL
  AND p.workspace_id = sqlc.arg(workspace_id);

-- name: GetServiceScopeInternal :one
-- GetServiceScope without the workspace check. For jobs and agent callbacks only;
-- never for an id taken from a request.
SELECT sqlc.embed(s),
       e.project_id,
       e.slug AS env_slug,
       e.name AS env_name,
       e.is_production,
       p.slug AS project_slug,
       p.name AS project_name,
       p.system AS project_system,
       p.workspace_id
FROM services s
JOIN environments e ON e.id = s.environment_id
JOIN projects p ON p.id = e.project_id
WHERE s.id = sqlc.arg(id)
  AND s.deleted_at IS NULL;

-- name: ListEnvironmentServices :many
SELECT * FROM services
WHERE environment_id = sqlc.arg(environment_id)
  AND deleted_at IS NULL
  AND (sqlc.narg(cursor_created_at)::timestamptz IS NULL
       OR (created_at, id) > (sqlc.narg(cursor_created_at)::timestamptz, sqlc.narg(cursor_id)::uuid))
ORDER BY created_at, id
LIMIT sqlc.arg(row_limit);

-- name: ListAllEnvironmentServices :many
SELECT * FROM services
WHERE environment_id = $1 AND deleted_at IS NULL
ORDER BY created_at, id;

-- name: InsertService :one
INSERT INTO services (environment_id, name, slug, kind, source_type, config, git_token_enc, registry_auth_enc, server_id, id)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
RETURNING *;

-- name: ListServiceSlugsLike :many
SELECT slug FROM services
WHERE environment_id = sqlc.arg(environment_id) AND deleted_at IS NULL
  AND (slug = sqlc.arg(base)::text OR slug LIKE sqlc.arg(base)::text || '-%');

-- name: UpdateServiceCAS :one
-- Compare-and-swap on generation: no row means a concurrent change.
UPDATE services
SET name = sqlc.arg(name),
    config = sqlc.arg(config),
    server_id = sqlc.arg(server_id),
    git_token_enc = sqlc.arg(git_token_enc),
    registry_auth_enc = sqlc.arg(registry_auth_enc),
    generation = generation + 1,
    updated_at = now()
WHERE id = sqlc.arg(id) AND generation = sqlc.arg(expected_generation) AND deleted_at IS NULL
RETURNING *;

-- name: SetServiceSourceType :exec
-- Moves a service between image and git sources. Only a caller that also writes
-- the matching config through UpdateServiceCAS (same transaction) may use it.
UPDATE services SET source_type = $2 WHERE id = $1 AND deleted_at IS NULL;

-- name: BumpServiceGeneration :one
UPDATE services SET generation = generation + 1, updated_at = now()
WHERE id = $1 AND deleted_at IS NULL
RETURNING generation;

-- name: SoftDeleteService :exec
UPDATE services SET deleted_at = now(), updated_at = now() WHERE id = $1 AND deleted_at IS NULL;

-- name: SetServiceObserved :exec
UPDATE services SET observed = $2, observed_at = $3 WHERE id = $1;

-- name: MarkServiceDeployed :exec
UPDATE services
SET current_deployment_id = $2, applied_generation = $3, updated_at = now()
WHERE id = $1;

-- name: SetServiceCurrentPortDomains :exec
-- Follows a change of the service port: default-process domains only (component ''); the domains of a
-- named process (or of a stack component) keep the port of their own process.
UPDATE domains SET target_port = $2 WHERE service_id = $1 AND component = '';

-- name: ListServerCurrentServices :many
-- Live services whose current deployment runs on the given server. process_replicas maps each named
-- process to its desired replicas, process_scaled_at to the time of its last scale (processes that were
-- never scaled are absent from the second object).
SELECT sqlc.embed(s), d.release_id AS current_release_id,
       (SELECT COALESCE(jsonb_object_agg(p.process, p.replicas), '{}'::jsonb)
          FROM service_process_scale p WHERE p.service_id = s.id)::jsonb AS process_replicas,
       (SELECT COALESCE(jsonb_object_agg(p.process, p.last_scale_at) FILTER (WHERE p.last_scale_at IS NOT NULL), '{}'::jsonb)
          FROM service_process_scale p WHERE p.service_id = s.id)::jsonb AS process_scaled_at
FROM deployments d
JOIN services s ON s.id = d.service_id AND s.current_deployment_id = d.id
WHERE d.server_id = $1 AND s.deleted_at IS NULL
ORDER BY s.created_at, s.id;

-- name: ListServerRouteDomains :many
-- Domains of every live non-route service whose current deployment runs on the server. process_replicas
-- maps each named process of the service to its desired replicas.
SELECT s.id AS service_id, s.slug AS service_slug, s.kind AS service_kind, s.environment_id, d.id AS deployment_id, sqlc.embed(dm),
       s.replicas, rel.config AS release_config,
       s.maintenance_enabled, s.maintenance_title, s.maintenance_message, s.maintenance_eta,
       (SELECT COALESCE(jsonb_object_agg(p.process, p.replicas), '{}'::jsonb)
          FROM service_process_scale p WHERE p.service_id = s.id)::jsonb AS process_replicas
FROM deployments d
JOIN services s ON s.id = d.service_id AND s.current_deployment_id = d.id
JOIN domains dm ON dm.service_id = s.id
LEFT JOIN releases rel ON rel.id = d.release_id
WHERE d.server_id = $1 AND s.deleted_at IS NULL AND s.kind <> 'route'
ORDER BY s.id, dm.target_port, dm.created_at, dm.id;

-- name: LoadServerRow :one
SELECT * FROM servers
WHERE id = sqlc.arg(id)
  AND (sqlc.narg(workspace_id)::uuid IS NULL OR workspace_id = sqlc.narg(workspace_id)::uuid);

-- name: LoadServerRows :many
SELECT * FROM servers WHERE id = ANY(sqlc.arg(ids)::uuid[]);

-- name: ListLatestDeploymentsForServices :many
SELECT DISTINCT ON (service_id) * FROM deployments
WHERE service_id = ANY(sqlc.arg(service_ids)::uuid[])
ORDER BY service_id, created_at DESC, id DESC;

-- name: ListReleasesByIDs :many
SELECT * FROM releases WHERE id = ANY(sqlc.arg(ids)::uuid[]);

-- name: ListDeploymentsByIDs :many
SELECT * FROM deployments WHERE id = ANY(sqlc.arg(ids)::uuid[]);

-- name: ListVolumesForServices :many
SELECT * FROM volumes WHERE service_id = ANY(sqlc.arg(service_ids)::uuid[]) ORDER BY created_at, id;

-- name: SetServiceDesiredState :exec
UPDATE services SET desired_state = $2, updated_at = now() WHERE id = $1 AND deleted_at IS NULL;

-- name: SetServiceConfigBumped :one
-- Replaces the config and advances the generation (per-field discard).
UPDATE services
SET config = $2, generation = generation + 1, updated_at = now()
WHERE id = $1 AND deleted_at IS NULL
RETURNING *;

-- name: DeleteServiceDomainByHostname :execrows
DELETE FROM domains WHERE service_id = $1 AND hostname = $2;

-- name: DeleteServiceVolumeByName :execrows
DELETE FROM volumes WHERE service_id = $1 AND name = $2;

-- name: LockServiceRow :one
SELECT * FROM services WHERE id = $1 AND deleted_at IS NULL FOR UPDATE;

-- name: SetServiceServerBumped :one
-- Moves the service's staged placement and advances the generation.
UPDATE services SET server_id = $2, generation = generation + 1, updated_at = now()
WHERE id = $1 AND deleted_at IS NULL
RETURNING *;
