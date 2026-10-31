-- Build servers, OCI registries, build placement.

-- ============ server build role ============

-- name: SetServerBuildRole :one
-- NULL arguments keep the current value.
UPDATE servers
SET build_enabled         = COALESCE(sqlc.narg(build_enabled)::boolean, build_enabled),
    build_max_concurrent  = COALESCE(sqlc.narg(build_max_concurrent)::int, build_max_concurrent),
    build_allow_emulation = COALESCE(sqlc.narg(build_allow_emulation)::boolean, build_allow_emulation),
    build_allow_previews  = COALESCE(sqlc.narg(build_allow_previews)::boolean, build_allow_previews)
WHERE id = sqlc.arg(id) AND workspace_id = sqlc.arg(workspace_id)
RETURNING *;

-- name: ListBuildServers :many
-- Every build-enabled, not revoked server of a team (any status; the resolver
-- filters on liveness and capability).
SELECT * FROM servers
WHERE workspace_id = $1 AND build_enabled AND status <> 'revoked'
ORDER BY created_at, id;

-- name: CountServerActiveBuilds :one
-- Builds the server is running or has been asked to run: build_image commands
-- not yet finished whose deployment is still active.
SELECT count(*)::int
FROM agent_commands c
JOIN deployments d ON d.id = c.deployment_id
WHERE c.server_id = $1
  AND c.kind = 'build_image'
  AND c.status IN ('pending', 'sent', 'acked')
  AND d.status IN ('created','queued','preparing','building','releasing','verifying');

-- name: LockBuildServerSlots :exec
-- Serializes slot accounting of one build server for the rest of the transaction.
SELECT pg_advisory_xact_lock(hashtextextended('build-slots:' || sqlc.arg(server_id)::text, 0));

-- ============ deployments: build commands and ownership ============

-- name: SetDeploymentBuildServer :exec
UPDATE deployments SET build_server_id = sqlc.narg(build_server_id)::uuid WHERE id = sqlc.arg(id);

-- name: SetDeploymentNoCache :exec
-- "Deploy without cache": the build of this deployment ignores every cache.
UPDATE deployments SET no_cache = true WHERE id = $1;

-- name: GetBuildCommandByDeployment :one
SELECT * FROM agent_commands
WHERE deployment_id = $1 AND kind = 'build_image'
ORDER BY created_at DESC
LIMIT 1;

-- name: ListDeploymentCommands :many
-- Every command of a deployment (the build and the deploy; cancel_deploy rows included).
SELECT * FROM agent_commands WHERE deployment_id = $1 ORDER BY created_at, id;

-- name: GetDeploymentServiceForReporter :one
-- Verifies that the reporting server takes part in the deployment: it is the
-- deployment's target, or it owns one of the deployment's commands (the build
-- server of a build_image command). Returns the service.
SELECT d.service_id
FROM deployments d
WHERE d.id = sqlc.arg(id)
  AND (d.server_id = sqlc.arg(server_id)
       OR EXISTS (SELECT 1 FROM agent_commands c WHERE c.deployment_id = d.id AND c.server_id = sqlc.arg(server_id)));

-- ============ team build settings ============

-- name: GetTeamBuildSettings :one
SELECT * FROM team_build_settings WHERE workspace_id = $1;

-- name: UpsertTeamBuildSettings :one
INSERT INTO team_build_settings (workspace_id, build_location, registry_id)
VALUES ($1, $2, $3)
ON CONFLICT (workspace_id) DO UPDATE
SET build_location = EXCLUDED.build_location, registry_id = EXCLUDED.registry_id, updated_at = now()
RETURNING *;

-- ============ project placement ============

-- name: SetProjectBuildPlacement :one
UPDATE projects
SET build_location  = sqlc.arg(build_location),
    build_server_id = sqlc.narg(build_server_id)::uuid,
    registry_id     = sqlc.narg(registry_id)::uuid,
    updated_at      = now()
WHERE id = sqlc.arg(id) AND workspace_id = sqlc.arg(workspace_id)
RETURNING *;

-- ============ registry images ============

-- name: UpsertRegistryImage :one
-- A repeated push of the same digest revives the row.
INSERT INTO registry_images (registry_id, service_id, release_id, repository, digest, platform, build_hash, size_bytes)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (registry_id, repository, digest) DO UPDATE
SET service_id = EXCLUDED.service_id, release_id = EXCLUDED.release_id, platform = EXCLUDED.platform,
    build_hash = EXCLUDED.build_hash, size_bytes = COALESCE(EXCLUDED.size_bytes, registry_images.size_bytes),
    state = 'live', pushed_at = now()
RETURNING *;

-- name: FindReusableRegistryImage :one
-- A live image of the same service, build inputs and platform in this registry.
SELECT * FROM registry_images
WHERE service_id = sqlc.arg(service_id) AND build_hash = sqlc.arg(build_hash) AND platform = sqlc.arg(platform)
  AND registry_id = sqlc.arg(registry_id) AND state = 'live'
ORDER BY pushed_at DESC
LIMIT 1;

-- name: GetRegistryImageByRef :one
SELECT * FROM registry_images WHERE registry_id = $1 AND repository = $2 AND digest = $3;

-- name: SetRegistryImageState :exec
UPDATE registry_images SET state = sqlc.arg(state) WHERE id = sqlc.arg(id);

-- name: ExpireServiceRegistryImages :many
-- Retention bookkeeping: live images of the service that are not in the keep set
-- (newest succeeded releases plus the running one) become expired. Only registries
-- with manage_retention are touched: NebulaCtrl never deletes from an external
-- registry, so its images stay rollbackable. Managed registries delete expired
-- digests in their sweep.
UPDATE registry_images ri
SET state = 'expired'
WHERE ri.service_id = sqlc.arg(service_id)
  AND ri.state = 'live'
  AND (ri.release_id IS NULL OR NOT (ri.release_id = ANY(sqlc.arg(keep_release_ids)::uuid[])))
  AND ri.pushed_at < now() - interval '1 minute'
  AND ri.registry_id IN (SELECT r.id FROM registries r WHERE r.manage_retention)
RETURNING ri.*;

-- ============ placement ============

-- name: GetRegistryPlacementInfo :one
-- What the placement resolver needs to know about a registry: its team, kind and,
-- for a built-in registry, the status of the instance registry service.
SELECT r.id, r.workspace_id, r.kind, r.host, r.namespace, COALESCE(isv.status, '')::text AS platform_status
FROM registries r
LEFT JOIN instance_services isv ON isv.kind = 'registry' AND r.kind = 'builtin'
WHERE r.id = $1;

-- name: CountProjectBuildsOnServer :one
-- Deployments of the project's services that were built on the server (its
-- per-project BuildKit cache is warm when this is above zero).
SELECT count(*)::int
FROM deployments d
JOIN services s ON s.id = d.service_id
JOIN environments e ON e.id = s.environment_id
WHERE e.project_id = $1 AND d.build_server_id = $2;

-- name: ServerBuiltForService :one
-- True when the server ran a build_image command for a deployment of the service
-- in the last day (it may report that service's source scan).
SELECT EXISTS (
    SELECT 1 FROM agent_commands c
    JOIN deployments d ON d.id = c.deployment_id
    WHERE c.server_id = $1 AND d.service_id = $2 AND c.kind = 'build_image'
      AND c.created_at > now() - interval '1 day'
)::boolean;
