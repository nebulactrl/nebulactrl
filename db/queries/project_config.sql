-- nebula.toml (internal/configsync): ownership rows, the per-environment binding/status and
-- the identity of file-managed backup schedules.

-- name: LockConfigApply :exec
-- Serialises applies to one environment (released at commit).
SELECT pg_advisory_xact_lock(hashtext('config-apply'), hashtext(sqlc.arg(environment_id)::text));

-- name: LockEnvironmentServices :many
-- The live services of an environment, locked for the apply that re-plans them.
SELECT * FROM services WHERE environment_id = $1 AND deleted_at IS NULL ORDER BY created_at, id FOR UPDATE;

-- name: ListEnvironmentServicesAll :many
SELECT * FROM services WHERE environment_id = $1 AND deleted_at IS NULL ORDER BY created_at, id;

-- name: GetConfigSource :one
SELECT * FROM config_sources WHERE service_id = $1;

-- name: GetConfigSourceForUpdate :one
SELECT * FROM config_sources WHERE service_id = $1 FOR UPDATE;

-- name: ListConfigSourcesByEnvironment :many
SELECT cs.* FROM config_sources cs
JOIN services s ON s.id = cs.service_id AND s.deleted_at IS NULL
WHERE cs.environment_id = $1;

-- name: UpsertConfigSource :one
INSERT INTO config_sources (
    service_id, environment_id, workspace_id, origin, repo_full_name, branch, path, commit_sha, file_sha256,
    schema_version, owned, pending, applied_generation, applied_by_type, applied_by_id, applied_by_label
) VALUES (
    sqlc.arg(service_id), sqlc.arg(environment_id), sqlc.arg(workspace_id), sqlc.arg(origin), sqlc.arg(repo_full_name),
    sqlc.arg(branch), sqlc.arg(path), sqlc.arg(commit_sha), sqlc.arg(file_sha256), sqlc.arg(schema_version),
    sqlc.arg(owned), sqlc.arg(pending), sqlc.arg(applied_generation), sqlc.arg(applied_by_type), sqlc.arg(applied_by_id),
    sqlc.arg(applied_by_label)
)
ON CONFLICT (service_id) DO UPDATE SET
    environment_id = EXCLUDED.environment_id, workspace_id = EXCLUDED.workspace_id, origin = EXCLUDED.origin,
    repo_full_name = EXCLUDED.repo_full_name, branch = EXCLUDED.branch, path = EXCLUDED.path, commit_sha = EXCLUDED.commit_sha,
    file_sha256 = EXCLUDED.file_sha256, schema_version = EXCLUDED.schema_version, owned = EXCLUDED.owned,
    pending = EXCLUDED.pending, applied_generation = EXCLUDED.applied_generation, applied_at = now(),
    applied_by_type = EXCLUDED.applied_by_type, applied_by_id = EXCLUDED.applied_by_id,
    applied_by_label = EXCLUDED.applied_by_label
RETURNING *;

-- name: SetConfigSourceOwned :exec
-- A later step of an apply (immediate settings) or a release: replaces owned and pending.
UPDATE config_sources SET owned = sqlc.arg(owned), pending = sqlc.arg(pending), applied_generation = sqlc.arg(applied_generation)
WHERE service_id = sqlc.arg(service_id);

-- name: DeleteConfigSource :exec
DELETE FROM config_sources WHERE service_id = $1;

-- name: ListConfigSourcesWithPending :many
-- Services of an environment with deferred immediate steps.
SELECT cs.* FROM config_sources cs
JOIN services s ON s.id = cs.service_id AND s.deleted_at IS NULL
WHERE cs.environment_id = $1 AND cs.pending <> '{}'::jsonb;

-- name: SetEnvironmentConfigOwnedShared :exec
INSERT INTO environment_config_settings (environment_id, workspace_id, owned_shared)
VALUES (sqlc.arg(environment_id), sqlc.arg(workspace_id), sqlc.arg(owned_shared))
ON CONFLICT (environment_id) DO UPDATE SET owned_shared = EXCLUDED.owned_shared, updated_at = now();

-- name: SetBackupScheduleConfigKey :exec
UPDATE backup_schedules SET config_key = sqlc.narg(config_key) WHERE id = sqlc.arg(id);

-- name: SetServiceGeneration :exec
-- Lowers a generation that one apply bumped several times to its single advance (never raises it).
UPDATE services SET generation = sqlc.arg(generation) WHERE id = sqlc.arg(id) AND generation > sqlc.arg(generation);

-- name: ListServiceTasksForServices :many
SELECT * FROM service_tasks WHERE service_id = ANY(sqlc.arg(ids)::uuid[]) ORDER BY service_id, name;

-- name: ListBackupSchedulesForServices :many
SELECT * FROM backup_schedules WHERE service_id = ANY(sqlc.arg(ids)::uuid[]) ORDER BY service_id, created_at, id;

-- name: ListUptimeMonitorsForServices :many
SELECT * FROM uptime_monitors WHERE service_id = ANY(sqlc.arg(ids)::uuid[]);

-- name: ListDomainsByHostnames :many
-- Domains holding any of these hostnames (across the instance), to find hostname_in_use.
SELECT d.*, s.slug AS service_slug, s.environment_id AS service_environment_id
FROM domains d JOIN services s ON s.id = d.service_id AND s.deleted_at IS NULL
WHERE d.hostname = ANY(sqlc.arg(hostnames)::text[]);

-- name: ListWorkspaceServers :many
-- Every server of the workspace, to resolve the names a file uses.
SELECT * FROM servers WHERE workspace_id = $1 ORDER BY lower(name), id;

-- name: ListWorkspaceCertificates :many
-- Uploaded certificates of the workspace (staged removals excluded), to resolve the names a file uses.
SELECT * FROM certificates WHERE workspace_id = $1 AND deleted_at IS NULL ORDER BY name, id;

-- name: GetSucceededDeploymentService :one
-- The service of a deployment that succeeded (no row while it is still running or failed).
SELECT service_id FROM deployments WHERE id = $1 AND status = 'succeeded';

-- name: GetEnvironmentInternal :one
-- An environment with its project's workspace, for background jobs (no authorization).
SELECT sqlc.embed(e), p.workspace_id AS workspace_id, p.slug AS project_slug, p.system AS project_system
FROM environments e JOIN projects p ON p.id = e.project_id
WHERE e.id = $1;

-- name: GetEnvironmentConfigSettings :one
SELECT * FROM environment_config_settings WHERE environment_id = $1;

-- name: UpsertEnvironmentConfigBinding :exec
-- Sets the explicit binding; the previous check result no longer applies.
INSERT INTO environment_config_settings (environment_id, workspace_id, connection_id, repo_full_name, branch, path)
VALUES (sqlc.arg(environment_id), sqlc.arg(workspace_id), sqlc.narg(connection_id), sqlc.arg(repo_full_name), sqlc.arg(branch), sqlc.arg(path))
ON CONFLICT (environment_id) DO UPDATE SET
    connection_id = EXCLUDED.connection_id, repo_full_name = EXCLUDED.repo_full_name, branch = EXCLUDED.branch, path = EXCLUDED.path,
    status = 'unknown', status_detail = '', last_commit_sha = '', last_file_sha256 = '', last_path = '', last_plan = NULL, checked_at = NULL,
    updated_at = now();

-- name: SetEnvironmentConfigAutoApply :exec
INSERT INTO environment_config_settings (environment_id, workspace_id, auto_apply)
VALUES (sqlc.arg(environment_id), sqlc.arg(workspace_id), sqlc.arg(auto_apply))
ON CONFLICT (environment_id) DO UPDATE SET auto_apply = EXCLUDED.auto_apply, updated_at = now();

-- name: ClearEnvironmentConfigAutoApply :exec
-- Run in the same transaction that marks an environment production.
UPDATE environment_config_settings SET auto_apply = false, updated_at = now() WHERE environment_id = $1 AND auto_apply;

-- name: ClearEnvironmentConfigBinding :exec
-- Drops the explicit binding and auto_apply; owned_shared stays.
UPDATE environment_config_settings SET
    connection_id = NULL, repo_full_name = '', branch = '', path = '', auto_apply = false,
    status = 'unknown', status_detail = '', last_commit_sha = '', last_file_sha256 = '', last_path = '', last_plan = NULL, checked_at = NULL,
    updated_at = now()
WHERE environment_id = $1;

-- name: SetEnvironmentConfigStatus :exec
INSERT INTO environment_config_settings (environment_id, workspace_id, status, status_detail, last_commit_sha, last_file_sha256, last_path, last_plan, checked_at)
VALUES (sqlc.arg(environment_id), sqlc.arg(workspace_id), sqlc.arg(status), sqlc.arg(status_detail), sqlc.arg(last_commit_sha), sqlc.arg(last_file_sha256),
        sqlc.arg(last_path), sqlc.narg(last_plan), sqlc.arg(checked_at)::timestamptz)
ON CONFLICT (environment_id) DO UPDATE SET
    status = EXCLUDED.status, status_detail = EXCLUDED.status_detail, last_commit_sha = EXCLUDED.last_commit_sha,
    last_file_sha256 = EXCLUDED.last_file_sha256, last_path = EXCLUDED.last_path, last_plan = EXCLUDED.last_plan,
    checked_at = EXCLUDED.checked_at, updated_at = now();

-- name: TouchEnvironmentConfigChecked :exec
-- Same file as the last check: only the time and the commit move.
UPDATE environment_config_settings SET checked_at = sqlc.arg(checked_at)::timestamptz, last_commit_sha = sqlc.arg(last_commit_sha), updated_at = now()
WHERE environment_id = sqlc.arg(environment_id);

-- name: ListExplicitConfigEnvironmentsForRepo :many
-- Standard environments with an explicit binding to this repository and branch.
SELECT sqlc.embed(s) FROM environment_config_settings s
JOIN environments e ON e.id = s.environment_id AND e.kind = 'standard'
WHERE s.connection_id = sqlc.arg(connection_id)::uuid AND lower(s.repo_full_name) = lower(sqlc.arg(repo)::text) AND s.branch = sqlc.arg(branch)::text;

-- name: ListGitEnvironmentsForRepo :many
-- Standard environments with a live service linked to this repository (candidates of a derived binding).
SELECT DISTINCT s.environment_id FROM service_git sg
JOIN services s ON s.id = sg.service_id AND s.deleted_at IS NULL
JOIN environments e ON e.id = s.environment_id AND e.kind = 'standard'
WHERE sg.connection_id = sqlc.arg(connection_id)::uuid AND lower(sg.repo_full_name) = lower(sqlc.arg(repo)::text);

-- name: ListEnvironmentGitLinks :many
-- Git links of an environment's live services (derived binding).
SELECT sg.service_id, sg.connection_id, sg.repo_full_name, s.slug AS service_slug, s.config AS service_config
FROM service_git sg JOIN services s ON s.id = sg.service_id AND s.deleted_at IS NULL
WHERE s.environment_id = $1
ORDER BY s.created_at, s.id;

-- name: ListEnvironmentServiceGenerations :many
SELECT id, generation FROM services WHERE environment_id = $1 AND deleted_at IS NULL ORDER BY id;
