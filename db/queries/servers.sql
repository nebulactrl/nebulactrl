-- name: CreateServer :one
INSERT INTO servers (workspace_id, name, public_ip, base_domain, acme_email)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: GetServerByID :one
SELECT * FROM servers WHERE id = $1;

-- name: GetServerScoped :one
SELECT s.*,
       (SELECT count(*) FROM services sv WHERE sv.server_id = s.id AND sv.deleted_at IS NULL)::int AS service_count
FROM servers s
WHERE s.id = $1 AND s.workspace_id = $2;

-- name: ListServersPage :many
SELECT s.*,
       (SELECT count(*) FROM services sv WHERE sv.server_id = s.id AND sv.deleted_at IS NULL)::int AS service_count
FROM servers s
WHERE s.workspace_id = $1
  AND (sqlc.narg(cursor_created_at)::timestamptz IS NULL
       OR (s.created_at, s.id) < (sqlc.narg(cursor_created_at)::timestamptz, sqlc.narg(cursor_id)::uuid))
ORDER BY s.created_at DESC, s.id DESC
LIMIT $2;

-- name: UpdateServerSettings :one
UPDATE servers
SET name        = COALESCE(sqlc.narg(name)::text, name),
    public_ip   = CASE WHEN sqlc.arg(set_public_ip)::boolean THEN sqlc.narg(public_ip)::text ELSE public_ip END,
    base_domain = CASE WHEN sqlc.arg(set_base_domain)::boolean THEN sqlc.narg(base_domain)::text ELSE base_domain END,
    acme_email  = CASE WHEN sqlc.arg(set_acme_email)::boolean THEN sqlc.narg(acme_email)::text ELSE acme_email END,
    metrics_retention_days        = COALESCE(sqlc.narg(metrics_retention_days)::int, metrics_retention_days),
    disk_alert_threshold_percent  = COALESCE(sqlc.narg(disk_alert_threshold_percent)::int, disk_alert_threshold_percent),
    traffic_analytics_enabled     = COALESCE(sqlc.narg(traffic_analytics_enabled)::boolean, traffic_analytics_enabled),
    traffic_hourly_retention_days = COALESCE(sqlc.narg(traffic_hourly_retention_days)::int, traffic_hourly_retention_days),
    traffic_daily_retention_days  = COALESCE(sqlc.narg(traffic_daily_retention_days)::int, traffic_daily_retention_days),
    geo_enabled      = COALESCE(sqlc.narg(geo_enabled)::boolean, geo_enabled),
    geo_source       = COALESCE(sqlc.narg(geo_source)::text, geo_source),
    geo_level        = COALESCE(sqlc.narg(geo_level)::text, geo_level),
    geo_refresh_days = COALESCE(sqlc.narg(geo_refresh_days)::int, geo_refresh_days)
WHERE id = sqlc.arg(id) AND workspace_id = sqlc.arg(workspace_id)
RETURNING *;

-- name: UpdateServerEnrolled :one
UPDATE servers
SET name  = CASE WHEN name = '' THEN sqlc.arg(hostname)::text ELSE name END,
    facts = sqlc.arg(facts)::jsonb
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: UpdateServerHello :one
UPDATE servers
SET status           = 'online',
    agent_version    = $2,
    protocol_version = $3,
    facts            = $4,
    last_seen_at     = now()
WHERE id = $1 AND status <> 'revoked'
RETURNING *;

-- name: RecordServerHeartbeat :one
-- Returns the values from before the update so the caller can tell whether
-- anything the UI shows has changed.
WITH prev AS (
    SELECT p.id, p.status, p.proxy_status, p.proxy_error FROM servers p WHERE p.id = sqlc.arg(server_id)::uuid FOR UPDATE OF p
)
UPDATE servers s
SET status       = 'online',
    last_seen_at = now(),
    proxy_status = sqlc.arg(proxy_status)::text,
    proxy_error  = sqlc.arg(proxy_error)::text,
    facts        = CASE WHEN sqlc.arg(docker_version)::text = '' THEN s.facts
                        ELSE jsonb_set(COALESCE(s.facts, '{}'::jsonb), '{dockerVersion}', to_jsonb(sqlc.arg(docker_version)::text)) END
FROM prev
WHERE s.id = prev.id AND s.status <> 'revoked'
RETURNING prev.status AS prev_status, prev.proxy_status AS prev_proxy_status, prev.proxy_error AS prev_proxy_error,
          s.workspace_id, s.name, s.disk_alert_threshold_percent, s.disk_alert_active;

-- name: MarkStaleServersOffline :many
UPDATE servers
SET status = 'offline'
WHERE status = 'online' AND (last_seen_at IS NULL OR last_seen_at < sqlc.arg(cutoff)::timestamptz)
RETURNING id;

-- name: MarkServerRevoked :one
UPDATE servers
SET status = 'revoked'
WHERE id = $1 AND workspace_id = $2
RETURNING *;

-- name: SetServerUnrestricted :one
UPDATE servers
SET unrestricted = sqlc.arg(unrestricted)::boolean
WHERE id = sqlc.arg(id) AND workspace_id = sqlc.arg(workspace_id)
RETURNING *;

-- name: CountServerUnrestrictedDockerServices :one
-- Live services placed on the server whose Docker options need an unrestricted
-- server (devices, gpus, runtime, privileged, pidHost, unconfined seccomp or
-- AppArmor, NET_ADMIN, SYS_ADMIN; capAdd names are stored normalised).
SELECT count(*)::int FROM services
WHERE server_id = $1 AND kind <> 'stack' AND deleted_at IS NULL AND (
    jsonb_array_length(CASE WHEN jsonb_typeof(config #> '{docker,devices}') = 'array' THEN config #> '{docker,devices}' ELSE '[]'::jsonb END) > 0
    OR jsonb_typeof(config #> '{docker,gpus}') = 'object'
    OR coalesce(config #>> '{docker,runtime}', '') <> ''
    OR coalesce(config #>> '{docker,privileged}', 'false') = 'true'
    OR coalesce(config #>> '{docker,pidHost}', 'false') = 'true'
    OR coalesce(config #>> '{docker,seccompUnconfined}', 'false') = 'true'
    OR coalesce(config #>> '{docker,apparmorUnconfined}', 'false') = 'true'
    OR (jsonb_typeof(config #> '{docker,capAdd}') = 'array'
        AND (config #> '{docker,capAdd}' @> '["NET_ADMIN"]'::jsonb OR config #> '{docker,capAdd}' @> '["SYS_ADMIN"]'::jsonb))
);

-- name: CountServerStacks :one
-- Live Compose stacks placed on the server.
SELECT count(*)::int FROM services
WHERE server_id = $1 AND kind = 'stack' AND deleted_at IS NULL;

-- name: ListServerBlockingProjects :many
-- The projects that have live services placed on the server, with how many.
-- The platform services of the team live in its hidden system project.
SELECT p.id, p.name, p.slug, p.system, count(*)::int AS services
FROM services s
JOIN environments e ON e.id = s.environment_id
JOIN projects p ON p.id = e.project_id
WHERE s.server_id = $1 AND s.deleted_at IS NULL
GROUP BY p.id, p.name, p.slug, p.system
ORDER BY p.system, p.name, p.id;

-- name: SetServerRemoval :one
-- The state of a server's removal job. started_at is set when a removal starts
-- and kept while it fails.
UPDATE servers
SET removal_state      = sqlc.arg(state)::text,
    removal_error      = sqlc.arg(error)::text,
    removal_started_at = CASE WHEN sqlc.arg(state)::text = 'removing' THEN now() WHEN sqlc.arg(state)::text = '' THEN NULL ELSE removal_started_at END
WHERE id = sqlc.arg(id) AND workspace_id = sqlc.arg(workspace_id)
RETURNING *;

-- name: DeleteServer :execrows
DELETE FROM servers WHERE id = $1 AND workspace_id = $2;

-- name: ListTeamServers :many
SELECT * FROM servers WHERE workspace_id = $1 ORDER BY created_at, id;
