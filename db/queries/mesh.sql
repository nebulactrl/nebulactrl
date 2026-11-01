-- Mesh settings, links, grants and exposures.

-- name: GetMeshSettings :one
SELECT * FROM mesh_settings WHERE workspace_id = $1;

-- name: UpsertMeshSettings :one
INSERT INTO mesh_settings (workspace_id, enabled, auto_join, mesh_tag, acl_mode, updated_at)
VALUES (sqlc.arg(workspace_id), sqlc.arg(enabled), sqlc.arg(auto_join), sqlc.arg(mesh_tag), sqlc.arg(acl_mode), now())
ON CONFLICT (workspace_id) DO UPDATE
SET enabled    = EXCLUDED.enabled,
    auto_join  = EXCLUDED.auto_join,
    mesh_tag   = EXCLUDED.mesh_tag,
    acl_mode   = EXCLUDED.acl_mode,
    updated_at = now()
RETURNING *;

-- name: SetMeshPaused :exec
UPDATE mesh_settings SET paused = $2, paused_reason = $3, updated_at = now() WHERE workspace_id = $1;

-- name: SetMeshACLState :exec
UPDATE mesh_settings
SET acl_state = $2, acl_message = $3, acl_hash = $4, acl_version = $5,
    acl_applied_at = CASE WHEN $2 = 'applied' THEN now() ELSE acl_applied_at END,
    updated_at = now()
WHERE workspace_id = $1;

-- name: SetMeshCoordinator :exec
UPDATE mesh_settings SET coordinator_ok = $2, coordinator_checked_at = now() WHERE workspace_id = $1;

-- name: GetServerMesh :one
SELECT * FROM server_mesh WHERE server_id = $1;

-- name: ListServerMesh :many
SELECT * FROM server_mesh WHERE workspace_id = $1 ORDER BY hostname;

-- name: InsertServerMesh :one
INSERT INTO server_mesh (server_id, workspace_id, hostname, identity_hash)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: DeleteServerMesh :execrows
DELETE FROM server_mesh WHERE server_id = $1;

-- name: ListMeshLinks :many
SELECT * FROM mesh_links WHERE workspace_id = $1 ORDER BY created_at, id;

-- name: ListMeshLinksOnServer :many
SELECT * FROM mesh_links WHERE server_id = $1 ORDER BY created_at, id;

-- name: GetMeshLink :one
SELECT * FROM mesh_links WHERE id = $1 AND workspace_id = $2;

-- name: GetMeshLinkByKey :one
SELECT * FROM mesh_links
WHERE environment_id = $1 AND server_id = $2 AND remote_server_id = $3 AND purpose = $4;

-- name: InsertMeshLink :one
INSERT INTO mesh_links (workspace_id, environment_id, server_id, remote_server_id, purpose, hostname, identity_hash)
VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (environment_id, server_id, remote_server_id, purpose) DO UPDATE SET hostname = mesh_links.hostname
RETURNING *;

-- name: DeleteMeshLink :execrows
DELETE FROM mesh_links WHERE id = $1;

-- name: ListMeshExposures :many
SELECT * FROM mesh_exposures WHERE workspace_id = $1 ORDER BY server_id, mesh_port;

-- name: ListMeshExposuresOnServer :many
SELECT * FROM mesh_exposures WHERE server_id = $1 ORDER BY mesh_port;

-- name: ListMeshExposuresOfEnvironment :many
SELECT * FROM mesh_exposures WHERE environment_id = $1 ORDER BY purpose, mesh_port;

-- name: GetMeshExposureByService :one
SELECT * FROM mesh_exposures WHERE service_id = $1 AND purpose = $2;

-- name: ListMeshExposuresUsedPorts :many
SELECT mesh_port FROM mesh_exposures WHERE server_id = $1 ORDER BY mesh_port;

-- name: InsertMeshExposure :one
INSERT INTO mesh_exposures (workspace_id, server_id, service_id, environment_id, purpose, container_port, mesh_port)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING *;

-- name: DeleteMeshExposure :execrows
DELETE FROM mesh_exposures WHERE id = $1;

-- name: ListMeshGrants :many
SELECT g.* FROM mesh_grants g JOIN mesh_links l ON l.id = g.link_id WHERE l.workspace_id = $1;

-- name: ListMeshGrantsOfLink :many
SELECT * FROM mesh_grants WHERE link_id = $1;

-- name: ListMeshGrantsOfRef :many
SELECT * FROM mesh_grants WHERE ref_id = $1;

-- name: InsertMeshGrant :exec
INSERT INTO mesh_grants (link_id, exposure_id, ref_id, source)
VALUES ($1, $2, $3, $4)
ON CONFLICT DO NOTHING;

-- name: DeleteMeshGrant :execrows
DELETE FROM mesh_grants WHERE link_id = $1 AND exposure_id = $2 AND ref_id = $3 AND source = $4;

-- name: EnsureMeshSettings :exec
INSERT INTO mesh_settings (workspace_id) VALUES ($1) ON CONFLICT (workspace_id) DO NOTHING;

-- name: UpdateMeshSettings :one
UPDATE mesh_settings
SET enabled    = COALESCE(sqlc.narg(enabled)::boolean, enabled),
    auto_join  = COALESCE(sqlc.narg(auto_join)::boolean, auto_join),
    mesh_tag   = COALESCE(sqlc.narg(mesh_tag)::text, mesh_tag),
    acl_mode   = COALESCE(sqlc.narg(acl_mode)::text, acl_mode),
    updated_at = now()
WHERE workspace_id = sqlc.arg(workspace_id)
RETURNING *;

-- name: ForceMeshACLMode :exec
UPDATE mesh_settings SET acl_mode = $2, updated_at = now() WHERE workspace_id = $1;

-- name: CountMeshInUse :one
SELECT ((SELECT count(*) FROM server_mesh sm WHERE sm.workspace_id = $1)
      + (SELECT count(*) FROM mesh_links ml WHERE ml.workspace_id = $1)
      + (SELECT count(*) FROM mesh_settings ms WHERE ms.workspace_id = $1 AND ms.enabled))::int;

-- name: CountOtherWorkspacesOnTailnet :one
SELECT count(*)::int FROM tailnet_integrations
WHERE workspace_id <> sqlc.arg(workspace_id)
  AND provider = sqlc.arg(provider)
  AND tailnet = sqlc.arg(tailnet)
  AND login_server = sqlc.arg(login_server);

-- name: ListMeshWorkspaces :many
SELECT workspace_id FROM mesh_settings
WHERE enabled
   OR EXISTS (SELECT 1 FROM server_mesh sm WHERE sm.workspace_id = mesh_settings.workspace_id)
   OR EXISTS (SELECT 1 FROM mesh_links ml WHERE ml.workspace_id = mesh_settings.workspace_id);

-- name: ListServerMeshAll :many
SELECT * FROM server_mesh ORDER BY server_id;

-- name: UpsertServerMeshRow :one
INSERT INTO server_mesh (server_id, workspace_id, hostname, identity_hash, state, message, needs_reprovision)
VALUES ($1, $2, $3, $4, 'provisioning', '', true)
ON CONFLICT (server_id) DO UPDATE SET hostname = EXCLUDED.hostname, identity_hash = EXCLUDED.identity_hash, updated_at = now()
RETURNING *;

-- name: SetServerMeshProvisioning :exec
UPDATE server_mesh
SET state = 'provisioning', message = '', needs_reprovision = true,
    peers = '[]', applied = '{}', observed_at = NULL, updated_at = now()
WHERE server_id = $1;

-- name: SetServerMeshKey :exec
UPDATE server_mesh SET auth_key_id = $2, provisioned_at = now(), updated_at = now() WHERE server_id = $1;

-- name: ClearServerMeshKey :exec
UPDATE server_mesh SET auth_key_id = '', updated_at = now() WHERE server_id = $1;

-- name: ClearServerMeshDevice :exec
UPDATE server_mesh SET device_id = '', fqdn = '', ips = '{}', updated_at = now() WHERE server_id = $1;

-- name: SetServerMeshApplied :exec
UPDATE server_mesh
SET applied = $2, identity_hash = $3, needs_reprovision = false, updated_at = now()
WHERE server_id = $1;

-- name: SetServerMeshState :exec
UPDATE server_mesh SET state = $2, message = $3, updated_at = now() WHERE server_id = $1;

-- name: SetServerMeshObserved :one
UPDATE server_mesh
SET state = sqlc.arg(state),
    message = sqlc.arg(message),
    fqdn = CASE WHEN sqlc.arg(fqdn)::text <> '' THEN sqlc.arg(fqdn)::text ELSE fqdn END,
    ips = CASE WHEN cardinality(sqlc.arg(ips)::text[]) > 0 THEN sqlc.arg(ips)::text[] ELSE ips END,
    device_id = CASE WHEN sqlc.arg(device_id)::text <> '' THEN sqlc.arg(device_id)::text ELSE device_id END,
    peers = sqlc.arg(peers)::jsonb,
    auth_key_id = CASE WHEN sqlc.arg(state) = 'online' THEN '' ELSE auth_key_id END,
    observed_at = now(),
    updated_at = now()
WHERE server_id = sqlc.arg(server_id)
RETURNING *;

-- name: GetMeshLinkByID :one
SELECT * FROM mesh_links WHERE id = $1;

-- name: ListMeshLinksTargeting :many
SELECT * FROM mesh_links WHERE remote_server_id = $1 ORDER BY created_at, id;

-- name: SetMeshLinkProvisioning :exec
UPDATE mesh_links
SET state = 'provisioning', message = '', needs_reprovision = true,
    applied = '{}', observed_at = NULL
WHERE id = $1;

-- name: SetMeshLinkKey :exec
UPDATE mesh_links SET auth_key_id = $2 WHERE id = $1;

-- name: ClearMeshLinkKey :exec
UPDATE mesh_links SET auth_key_id = '' WHERE id = $1;

-- name: SetMeshLinkApplied :exec
UPDATE mesh_links
SET applied = $2, identity_hash = $3, target_ip = $4, needs_reprovision = false
WHERE id = $1;

-- name: SetMeshLinkState :exec
UPDATE mesh_links SET state = $2, message = $3 WHERE id = $1;

-- name: SetMeshLinkObserved :one
UPDATE mesh_links
SET state = sqlc.arg(state),
    message = sqlc.arg(message),
    ips = CASE WHEN cardinality(sqlc.arg(ips)::text[]) > 0 THEN sqlc.arg(ips)::text[] ELSE ips END,
    device_id = CASE WHEN sqlc.arg(device_id)::text <> '' THEN sqlc.arg(device_id)::text ELSE device_id END,
    auth_key_id = CASE WHEN sqlc.arg(state) = 'online' THEN '' ELSE auth_key_id END,
    observed_at = now()
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: GetMeshExposureByID :one
SELECT * FROM mesh_exposures WHERE id = $1;

-- name: ListMeshExposuresOfServers :many
SELECT * FROM mesh_exposures WHERE server_id = ANY($1::uuid[]) ORDER BY server_id, mesh_port;

-- name: ListMeshGrantsOfExposure :many
SELECT * FROM mesh_grants WHERE exposure_id = $1;

-- name: ListMeshGrantsOfExposures :many
SELECT * FROM mesh_grants WHERE exposure_id = ANY($1::uuid[]);

-- name: CountMeshGrantsOfLink :one
SELECT count(*)::int FROM mesh_grants WHERE link_id = $1;

-- name: CountMeshGrantsOfExposure :one
SELECT count(*)::int FROM mesh_grants WHERE exposure_id = $1;

-- name: DeleteMeshGrantsOfRef :many
DELETE FROM mesh_grants WHERE ref_id = $1 RETURNING link_id, exposure_id;

-- name: DeleteMeshGrantsOfLink :exec
DELETE FROM mesh_grants WHERE link_id = $1;

-- name: DeleteMeshGrantsOfExposure :exec
DELETE FROM mesh_grants WHERE exposure_id = $1;

-- name: ListMeshServicesBrief :many
SELECT sv.id, sv.slug, sv.name, sv.environment_id, sv.server_id, sv.kind, sv.source_type, sv.config
FROM services sv WHERE sv.id = ANY($1::uuid[]);

-- name: GetMeshServiceScope :one
SELECT sv.id, sv.slug, sv.name, sv.kind, sv.environment_id, sv.server_id, sv.config, sv.deleted_at,
       e.name AS env_name, e.slug AS env_slug, p.id AS project_id, p.name AS project_name, p.workspace_id
FROM services sv
JOIN environments e ON e.id = sv.environment_id
JOIN projects p ON p.id = e.project_id
WHERE sv.id = $1;

-- name: GetMeshEnvironment :one
SELECT e.id, e.name, e.slug, p.id AS project_id, p.name AS project_name, p.workspace_id
FROM environments e JOIN projects p ON p.id = e.project_id
WHERE e.id = $1;

-- name: ListMeshEnvironmentsByIDs :many
SELECT e.id, e.name, e.slug, p.id AS project_id, p.name AS project_name, p.workspace_id
FROM environments e JOIN projects p ON p.id = e.project_id
WHERE e.id = ANY($1::uuid[]);

-- name: ListMeshOpenCommands :many
SELECT id, kind, status, created_at, acked_at FROM agent_commands
WHERE server_id = $1 AND kind IN ('sync_mesh', 'remove_mesh') AND status IN ('pending', 'sent', 'acked')
ORDER BY created_at;

-- name: ListMeshServerRowsOnline :many
SELECT s.id, s.name, s.status, s.facts, s.workspace_id FROM servers s
WHERE s.workspace_id = $1 AND s.status <> 'revoked'
ORDER BY s.name, s.id;

-- name: CountMeshServicesOnServerBlockedEgress :one
SELECT count(*)::int FROM services
WHERE environment_id = $1 AND server_id = $2 AND deleted_at IS NULL AND (config->>'blockEgress')::boolean IS TRUE;

-- name: ClearMeshLinkDevice :exec
UPDATE mesh_links SET device_id = '', ips = '{}' WHERE id = $1;

-- name: ListReadyPlatformServicesOfKind :many
SELECT * FROM platform_services WHERE kind = $1 AND status = 'ready' ORDER BY created_at, id;
