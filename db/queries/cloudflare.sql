-- name: GetCloudflareConnection :one
SELECT * FROM cloudflare_connections WHERE workspace_id = $1;

-- name: UpsertCloudflareConnection :one
-- Stores a freshly verified token together with the zone list it can see.
INSERT INTO cloudflare_connections (workspace_id, token_enc, account_id, account_name,
                                    status, status_message, token_expires_at, verified_at, zones, zones_synced_at)
VALUES (sqlc.arg(workspace_id), sqlc.arg(token_enc), sqlc.arg(account_id), sqlc.arg(account_name),
        'ok', '', sqlc.narg(token_expires_at), now(), sqlc.arg(zones), now())
ON CONFLICT (workspace_id) DO UPDATE
SET token_enc = EXCLUDED.token_enc, account_id = EXCLUDED.account_id, account_name = EXCLUDED.account_name,
    status = 'ok', status_message = '', token_expires_at = EXCLUDED.token_expires_at, verified_at = now(),
    zones = EXCLUDED.zones, zones_synced_at = now(), updated_at = now()
RETURNING *;

-- name: UpdateCloudflareSettings :one
UPDATE cloudflare_connections
SET account_id = sqlc.arg(account_id), account_name = sqlc.arg(account_name), updated_at = now()
WHERE workspace_id = sqlc.arg(workspace_id)
RETURNING *;

-- name: SetCloudflareVerified :one
-- A successful verification: the token is valid and the zone list is fresh.
UPDATE cloudflare_connections
SET status = 'ok', status_message = '', token_expires_at = sqlc.narg(token_expires_at), verified_at = now(),
    zones = sqlc.arg(zones), zones_synced_at = now(),
    account_id = sqlc.arg(account_id), account_name = sqlc.arg(account_name), updated_at = now()
WHERE workspace_id = sqlc.arg(workspace_id)
RETURNING *;

-- name: SetCloudflareFailed :exec
UPDATE cloudflare_connections
SET status = 'error', status_message = $2, updated_at = now()
WHERE workspace_id = $1;

-- name: DeleteCloudflareConnection :execrows
DELETE FROM cloudflare_connections WHERE workspace_id = $1;

-- name: ListCloudflareWorkspaces :many
SELECT workspace_id FROM cloudflare_connections ORDER BY workspace_id;

-- name: ListCloudflareRecordsForHost :many
SELECT * FROM cloudflare_records WHERE workspace_id = $1 AND zone_id = $2 AND hostname = $3 ORDER BY record_id;

-- name: ListCloudflareRecords :many
SELECT * FROM cloudflare_records WHERE workspace_id = $1 ORDER BY hostname, record_id;

-- name: UpsertCloudflareRecord :exec
INSERT INTO cloudflare_records (zone_id, record_id, workspace_id, owner_kind, owner_id, hostname, type, content, proxied)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
ON CONFLICT (zone_id, record_id) DO UPDATE
SET owner_kind = EXCLUDED.owner_kind, owner_id = EXCLUDED.owner_id, hostname = EXCLUDED.hostname, type = EXCLUDED.type,
    content = EXCLUDED.content, proxied = EXCLUDED.proxied, updated_at = now();

-- name: LatestCloudflareRecordChange :many
-- When NebulaCtrl last created or changed a record of this owner's hostname: one row, none when it never has.
SELECT updated_at FROM cloudflare_records WHERE owner_kind = $1 AND owner_id = $2 ORDER BY updated_at DESC LIMIT 1;

-- name: DeleteCloudflareRecord :exec
DELETE FROM cloudflare_records WHERE zone_id = $1 AND record_id = $2;

-- name: DeleteCloudflareRecordsOfWorkspace :exec
DELETE FROM cloudflare_records WHERE workspace_id = $1;

-- name: ListOrphanCloudflareRecords :many
-- Tracked records whose hostname no live domain or status page of the workspace uses any more.
SELECT r.* FROM cloudflare_records r
WHERE r.workspace_id = $1
  AND NOT EXISTS (
    SELECT 1 FROM domains d
    JOIN services s ON s.id = d.service_id AND s.deleted_at IS NULL
    JOIN environments e ON e.id = s.environment_id
    JOIN projects p ON p.id = e.project_id
    WHERE p.workspace_id = r.workspace_id AND d.hostname = r.hostname AND d.cf_zone_id = r.zone_id)
  AND NOT EXISTS (
    SELECT 1 FROM status_pages sp
    WHERE sp.workspace_id = r.workspace_id AND sp.hostname = r.hostname AND sp.cf_zone_id = r.zone_id)
ORDER BY r.hostname, r.record_id;

-- name: ListCloudflareWorkspaceDomains :many
-- Every live domain of a workspace.
SELECT d.* FROM domains d
JOIN services s ON s.id = d.service_id AND s.deleted_at IS NULL
JOIN environments e ON e.id = s.environment_id
JOIN projects p ON p.id = e.project_id
WHERE p.workspace_id = $1
ORDER BY d.created_at, d.id;

-- name: ListCloudflareServerDomainIDs :many
-- Domains of the services placed on a server that live in a connected zone.
SELECT d.id FROM domains d
JOIN services s ON s.id = d.service_id AND s.deleted_at IS NULL
WHERE s.server_id = $1 AND d.cf_zone_id <> ''
ORDER BY d.created_at, d.id;

-- name: ListCloudflareServiceDomainIDs :many
SELECT id FROM domains WHERE service_id = $1 AND cf_zone_id <> '' ORDER BY created_at, id;

-- name: SetDomainCloudflareZone :exec
-- First zone match of a domain: its records are managed from now on, and it starts proxied unless the name cannot be.
UPDATE domains
SET cf_zone_id = sqlc.arg(zone_id), cf_zone_name = sqlc.arg(zone_name),
    cf_proxied = sqlc.arg(proxied),
    cf_status = CASE WHEN sqlc.arg(zone_id)::text = '' THEN '' ELSE cf_status END,
    cf_message = '', cf_error = ''
WHERE id = sqlc.arg(id);

-- name: SetDomainCloudflareMatch :exec
-- Re-match an existing domain (renamed, zones refreshed) without touching its settings.
UPDATE domains
SET cf_zone_id = sqlc.arg(zone_id), cf_zone_name = sqlc.arg(zone_name),
    cf_status = CASE WHEN sqlc.arg(zone_id)::text = '' THEN '' WHEN cf_zone_id <> sqlc.arg(zone_id)::text THEN '' ELSE cf_status END,
    cf_message = CASE WHEN cf_zone_id <> sqlc.arg(zone_id)::text THEN '' ELSE cf_message END,
    cf_error = CASE WHEN cf_zone_id <> sqlc.arg(zone_id)::text THEN '' ELSE cf_error END
WHERE id = sqlc.arg(id);

-- name: SetDomainCloudflareSettings :one
UPDATE domains
SET cf_proxied = COALESCE(sqlc.narg(proxied)::boolean, cf_proxied)
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: SetDomainCloudflareState :exec
UPDATE domains
SET cf_status = sqlc.arg(status), cf_message = sqlc.arg(message), cf_error = sqlc.arg(error), cf_checked_at = now()
WHERE id = sqlc.arg(id);

-- name: SetDomainCloudflareError :exec
UPDATE domains SET cf_error = sqlc.arg(error), cf_checked_at = now() WHERE id = sqlc.arg(id);

-- name: ClearWorkspaceDomainsCloudflare :exec
-- The connection was removed: forget zone matches and states of the team's domains.
UPDATE domains d
SET cf_zone_id = '', cf_zone_name = '', cf_status = '', cf_message = '', cf_error = '', cf_checked_at = NULL
FROM services s, environments e, projects p
WHERE s.id = d.service_id AND e.id = s.environment_id AND p.id = e.project_id AND p.workspace_id = $1 AND d.cf_zone_id <> '';

-- name: GetCloudflareIPList :one
-- The last validated IP list (no row: the built-in list is in use).
SELECT * FROM cloudflare_ip_lists WHERE id = 1;

-- name: UpsertCloudflareIPList :one
-- Stores a validated list. changed_at moves only when the hash differs.
INSERT INTO cloudflare_ip_lists (id, v4, v6, hash, source, fetched_at, changed_at, last_error, failing_since, stale_notified_at)
VALUES (1, sqlc.arg(v4), sqlc.arg(v6), sqlc.arg(hash), sqlc.arg(source), now(), now(), '', NULL, NULL)
ON CONFLICT (id) DO UPDATE
SET v4 = EXCLUDED.v4, v6 = EXCLUDED.v6, source = EXCLUDED.source, fetched_at = now(),
    changed_at = CASE WHEN cloudflare_ip_lists.hash <> EXCLUDED.hash THEN now() ELSE cloudflare_ip_lists.changed_at END,
    hash = EXCLUDED.hash, last_error = '', failing_since = NULL, stale_notified_at = NULL
RETURNING *;

-- name: SetCloudflareIPListFailure :exec
-- A fetch failed: the stored list stays. failing_since keeps the first failure of the streak.
UPDATE cloudflare_ip_lists
SET last_error = sqlc.arg(last_error), failing_since = COALESCE(failing_since, now())
WHERE id = 1;

-- name: SeedCloudflareIPList :exec
-- Stores the built-in list when nothing is stored yet, so failures have a row to live on.
INSERT INTO cloudflare_ip_lists (id, v4, v6, hash, source, fetched_at, changed_at)
VALUES (1, sqlc.arg(v4), sqlc.arg(v6), sqlc.arg(hash), 'builtin', now(), now())
ON CONFLICT (id) DO NOTHING;

-- name: MarkCloudflareIPListStaleNotified :exec
UPDATE cloudflare_ip_lists SET stale_notified_at = now() WHERE id = 1;

-- name: SetDomainOriginLock :one
UPDATE domains SET cf_origin_lock = sqlc.arg(enabled) WHERE id = sqlc.arg(id) RETURNING *;

-- name: ListOriginLockDomains :many
-- Of the given domains, those eligible for the Cloudflare origin lock: opted in,
-- proxied, in a connected zone with DNS managed by NebulaCtrl, and with a
-- proxied A/AAAA record of ours. (Tunnelled domains are excluded by the caller.) The
-- row carries the zone's Authenticated Origin Pulls state.
SELECT dm.id AS domain_id, dm.hostname, dm.cf_zone_id AS zone_id, p.workspace_id, COALESCE(aop.state, '')::text AS aop_state
FROM domains dm
JOIN services s ON s.id = dm.service_id
JOIN environments e ON e.id = s.environment_id
JOIN projects p ON p.id = e.project_id
LEFT JOIN cloudflare_zone_aop aop ON aop.workspace_id = p.workspace_id AND aop.zone_id = dm.cf_zone_id
WHERE dm.id = ANY(sqlc.arg(domain_ids)::uuid[])
  AND dm.cf_origin_lock AND dm.cf_proxied AND dm.cf_zone_id <> ''
  AND EXISTS (
      SELECT 1 FROM cloudflare_records r
      WHERE r.zone_id = dm.cf_zone_id AND r.hostname = dm.hostname AND r.proxied AND r.type IN ('A','AAAA'));

-- name: GetZoneAOP :one
SELECT * FROM cloudflare_zone_aop WHERE workspace_id = $1 AND zone_id = $2;

-- name: UpsertZoneAOP :exec
INSERT INTO cloudflare_zone_aop (workspace_id, zone_id, state, reason_code, reason, checked_at)
VALUES ($1, $2, $3, $4, $5, now())
ON CONFLICT (workspace_id, zone_id) DO UPDATE
SET state = EXCLUDED.state, reason_code = EXCLUDED.reason_code, reason = EXCLUDED.reason, checked_at = now();

-- name: ListOriginLockServerIDs :many
-- Servers whose current deployments serve at least one domain that may be
-- origin-locked (opted in, proxied record of ours). zone_id restricts to one
-- zone of a workspace (AOP state change); empty means every server.
SELECT DISTINCT d.server_id
FROM domains dm
JOIN services s ON s.id = dm.service_id AND s.deleted_at IS NULL
JOIN deployments d ON d.id = s.current_deployment_id AND d.server_id IS NOT NULL
WHERE dm.cf_origin_lock AND dm.cf_proxied AND dm.cf_zone_id <> ''
  AND (sqlc.arg(zone_id)::text = '' OR dm.cf_zone_id = sqlc.arg(zone_id)::text)
  AND EXISTS (
      SELECT 1 FROM cloudflare_records r
      WHERE r.zone_id = dm.cf_zone_id AND r.hostname = dm.hostname AND r.proxied AND r.type IN ('A','AAAA'))
ORDER BY d.server_id;

-- name: GetDomainRouteServerID :one
-- The server whose current deployment of the domain's service serves it.
SELECT d.server_id
FROM domains dm
JOIN services s ON s.id = dm.service_id AND s.deleted_at IS NULL
JOIN deployments d ON d.id = s.current_deployment_id
WHERE dm.id = $1 AND d.server_id IS NOT NULL;
