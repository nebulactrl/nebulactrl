-- OCI registries (external and the built-in per-team slice).
-- The build-placement and retention-bookkeeping queries live in build_servers.sql.

-- name: InsertRegistry :one
-- id is optional.
INSERT INTO registries (id, workspace_id, name, kind, host, namespace, push_auth_enc, pull_auth_enc, manage_retention)
VALUES (COALESCE(sqlc.narg(id)::uuid, gen_random_uuid()), sqlc.arg(workspace_id), sqlc.arg(name), sqlc.arg(kind), sqlc.arg(host), sqlc.arg(namespace),
        sqlc.narg(push_auth_enc), sqlc.narg(pull_auth_enc), sqlc.arg(manage_retention))
RETURNING *;

-- name: GetRegistry :one
SELECT * FROM registries WHERE id = $1 AND workspace_id = $2;

-- name: GetRegistryInternal :one
SELECT * FROM registries WHERE id = $1;

-- name: ListRegistries :many
SELECT * FROM registries WHERE workspace_id = $1 ORDER BY created_at, id;

-- name: ListBuiltinRetentionRegistries :many
-- Built-in registries whose expired digests the sweep deletes.
SELECT * FROM registries WHERE kind = 'builtin' AND manage_retention ORDER BY created_at, id;

-- name: UpdateRegistry :one
UPDATE registries
SET name = sqlc.arg(name), host = sqlc.arg(host), namespace = sqlc.arg(namespace),
    push_auth_enc = sqlc.narg(push_auth_enc), pull_auth_enc = sqlc.narg(pull_auth_enc),
    manage_retention = sqlc.arg(manage_retention), updated_at = now()
WHERE id = sqlc.arg(id) AND workspace_id = sqlc.arg(workspace_id)
RETURNING *;

-- name: DeleteRegistry :execrows
DELETE FROM registries WHERE id = $1 AND workspace_id = $2;

-- name: SetRegistryLastCheck :exec
-- Merges one server's check result into last_check ({serverId: result}).
UPDATE registries
SET last_check = COALESCE(last_check, '{}'::jsonb) || jsonb_build_object(sqlc.arg(server_id)::text, sqlc.arg(result)::jsonb),
    updated_at = now()
WHERE id = sqlc.arg(id);

-- name: CountRegistryReferences :one
-- What still points at a registry: the team default, project settings and
-- service overrides (services.config.registryId).
SELECT
    (SELECT count(*) FROM team_build_settings t WHERE t.registry_id = $1)::int AS teams,
    (SELECT count(*) FROM projects p WHERE p.registry_id = $1)::int AS projects,
    (SELECT count(*) FROM services s WHERE s.deleted_at IS NULL AND s.config->>'registryId' = $1::text)::int AS services;

-- name: ListRegistryImages :many
-- Newest first; cursor is (pushed_at, id) of the last row of the previous page.
SELECT * FROM registry_images
WHERE registry_id = sqlc.arg(registry_id)
  AND (sqlc.narg(state)::text IS NULL OR state = sqlc.narg(state)::text)
  AND (sqlc.narg(cursor_at)::timestamptz IS NULL OR (pushed_at, id) < (sqlc.narg(cursor_at)::timestamptz, sqlc.narg(cursor_id)::uuid))
ORDER BY pushed_at DESC, id DESC
LIMIT sqlc.arg(row_limit);

-- name: RegistryImageStats :one
-- Live images of a registry: how many and how much space they take.
SELECT count(*)::int AS image_count, COALESCE(sum(size_bytes), 0)::bigint AS size_bytes
FROM registry_images WHERE registry_id = $1 AND state = 'live';

-- name: ListExpiredRegistryImages :many
SELECT * FROM registry_images WHERE registry_id = $1 AND state = 'expired' ORDER BY pushed_at, id LIMIT $2;

-- name: ClaimExpiredRegistryImage :one
-- Marks an expired image deleted before its digest is removed from the registry.
-- A live image (revived by a new push of the same digest) is never claimed.
UPDATE registry_images SET state = 'deleted' WHERE id = $1 AND state = 'expired' RETURNING *;

-- name: CountActiveRegistryBuilds :one
-- Builds that push to the registry and have not finished (the GC window waits
-- for them). host_prefix is "<host>/" escaped for LIKE.
SELECT count(*)::int FROM agent_commands c
WHERE c.kind = 'build_image' AND c.status IN ('pending', 'sent', 'acked')
  AND c.payload->>'repository' LIKE sqlc.arg(host_prefix)::text || '%' ESCAPE '\';

-- name: RecordPushedRegistryImage :exec
-- A manifest a team pushed through the forwarder, into the built-in registry of
-- its workspace (no row when it has none). It records images NebulaCtrl did not
-- build (no service, no build hash) so the sweep can age them out and the quota
-- can count them. A built image (service set) is left alone; a repeated push
-- of the same hand-pushed digest refreshes its age and revives a deleted row.
INSERT INTO registry_images (registry_id, repository, digest, platform, build_hash, size_bytes)
SELECT r.id, r.host || '/' || sqlc.arg(repository)::text, sqlc.arg(digest)::text, '', '', sqlc.arg(size_bytes)::bigint
FROM registries r WHERE r.workspace_id = sqlc.arg(workspace_id) AND r.kind = 'builtin'
ON CONFLICT (registry_id, repository, digest) DO UPDATE
SET pushed_at = now(), size_bytes = EXCLUDED.size_bytes,
    state = CASE WHEN registry_images.state = 'deleted' THEN 'live' ELSE registry_images.state END
WHERE registry_images.service_id IS NULL AND registry_images.build_hash = '';

-- name: BuiltinRegistryBytes :one
-- The bytes a workspace's built-in registry holds on disk as far as the control
-- plane knows: images not yet deleted (expired ones stay until the sweep runs).
SELECT COALESCE(sum(ri.size_bytes), 0)::bigint
FROM registry_images ri JOIN registries r ON r.id = ri.registry_id
WHERE r.workspace_id = $1 AND r.kind = 'builtin' AND ri.state IN ('live', 'expired');

-- name: ExpireUnrecordedRegistryImages :execrows
-- Retention for images NebulaCtrl did not build: those pushed more than
-- older_than_days days ago (and not pushed again since) become expired, and the
-- sweep deletes them like any expired image.
UPDATE registry_images
SET state = 'expired'
WHERE registry_id = sqlc.arg(registry_id) AND state = 'live'
  AND service_id IS NULL AND build_hash = ''
  AND pushed_at < now() - make_interval(days => sqlc.arg(older_than_days)::int);
