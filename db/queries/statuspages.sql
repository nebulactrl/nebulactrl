-- Status pages, incidents and maintenance windows (internal/statuspages).

-- Pages ----------------------------------------------------------------------

-- name: InsertStatusPage :one
INSERT INTO status_pages (workspace_id, title, slug, description, footer, hostname, published, show_uptime, theme)
VALUES (sqlc.arg(workspace_id), sqlc.arg(title), sqlc.arg(slug), sqlc.arg(description), sqlc.arg(footer), sqlc.narg(hostname),
        sqlc.arg(published), sqlc.arg(show_uptime), sqlc.arg(theme))
RETURNING *;

-- name: GetStatusPage :one
SELECT * FROM status_pages WHERE id = sqlc.arg(id) AND workspace_id = sqlc.arg(workspace_id);

-- name: GetStatusPageForUpdate :one
-- Serializes changes of one page (publish preconditions against component edits).
SELECT * FROM status_pages WHERE id = sqlc.arg(id) AND workspace_id = sqlc.arg(workspace_id) FOR UPDATE;

-- name: ListStatusPages :many
SELECT * FROM status_pages WHERE workspace_id = sqlc.arg(workspace_id) ORDER BY lower(title), id;

-- name: UpdateStatusPage :one
UPDATE status_pages SET
    title = sqlc.arg(title),
    slug = sqlc.arg(slug),
    description = sqlc.arg(description),
    footer = sqlc.arg(footer),
    hostname = sqlc.narg(hostname),
    published = sqlc.arg(published),
    show_uptime = sqlc.arg(show_uptime),
    theme = sqlc.arg(theme),
    updated_at = now()
WHERE id = sqlc.arg(id) AND workspace_id = sqlc.arg(workspace_id)
RETURNING *;

-- name: DeleteStatusPage :execrows
DELETE FROM status_pages WHERE id = sqlc.arg(id) AND workspace_id = sqlc.arg(workspace_id);

-- name: StatusHostnameTaken :one
-- True when a status page already uses the (lowercase) hostname. Service domains must not take it.
SELECT EXISTS (SELECT 1 FROM status_pages WHERE hostname = sqlc.arg(hostname)::text);

-- name: StatusPageHostnameOwner :one
-- The team and title of the status page that uses the (lowercase) hostname: a team told its
-- own page holds the name can be specific, another team is told nothing.
SELECT workspace_id, title FROM status_pages WHERE hostname = sqlc.arg(hostname)::text LIMIT 1;

-- name: DomainHostnameTaken :one
-- True when a service domain uses the hostname. Status pages must not take it.
SELECT EXISTS (SELECT 1 FROM domains WHERE lower(hostname) = sqlc.arg(hostname)::text);

-- name: ListWorkspaceStatusPageIDs :many
-- The pages among ids that belong to the workspace.
SELECT id FROM status_pages WHERE id = ANY(sqlc.arg(ids)::uuid[]) AND workspace_id = sqlc.arg(workspace_id);

-- Components -----------------------------------------------------------------

-- name: ListStatusPageComponents :many
-- Components of one page (page_id set) or of every page of the workspace, in page order.
-- Services deleted meanwhile are skipped.
SELECT c.page_id, c.service_id, c.display_name, c.group_name, c.url, c.position,
       s.name AS service_name, e.name AS environment_name, p.id AS project_id, p.name AS project_name
FROM status_page_components c
JOIN status_pages sp ON sp.id = c.page_id
JOIN services s ON s.id = c.service_id AND s.deleted_at IS NULL
JOIN environments e ON e.id = s.environment_id
JOIN projects p ON p.id = e.project_id
WHERE sp.workspace_id = sqlc.arg(workspace_id)
  AND (sqlc.narg(page_id)::uuid IS NULL OR c.page_id = sqlc.narg(page_id)::uuid)
ORDER BY c.page_id, c.position;

-- name: DeleteStatusPageComponents :exec
DELETE FROM status_page_components WHERE page_id = sqlc.arg(page_id);

-- name: InsertStatusPageComponent :exec
INSERT INTO status_page_components (page_id, service_id, display_name, group_name, url, position)
VALUES (sqlc.arg(page_id), sqlc.arg(service_id), sqlc.arg(display_name), sqlc.arg(group_name), sqlc.arg(url), sqlc.arg(position));

-- name: CountLiveStatusPageComponents :one
-- Components whose service still exists.
SELECT count(*)::int
FROM status_page_components c
JOIN services s ON s.id = c.service_id AND s.deleted_at IS NULL
WHERE c.page_id = sqlc.arg(page_id);

-- name: ListWorkspaceServicesByIDs :many
-- The live services among ids that belong to the workspace, with their project.
SELECT s.id, s.name, e.project_id
FROM services s
JOIN environments e ON e.id = s.environment_id
JOIN projects p ON p.id = e.project_id
WHERE s.id = ANY(sqlc.arg(ids)::uuid[]) AND s.deleted_at IS NULL AND p.workspace_id = sqlc.arg(workspace_id);

-- Incidents ------------------------------------------------------------------

-- name: InsertStatusIncident :one
INSERT INTO status_incidents (workspace_id, title, impact, status, created_by, created_at, updated_at, resolved_at)
VALUES (sqlc.arg(workspace_id), sqlc.arg(title), sqlc.arg(impact), sqlc.arg(status), sqlc.narg(created_by),
        sqlc.arg(at), sqlc.arg(at), sqlc.narg(resolved_at))
RETURNING *;

-- name: InsertStatusIncidentUpdate :one
INSERT INTO status_incident_updates (incident_id, status, body, created_by, created_at)
VALUES (sqlc.arg(incident_id), sqlc.arg(status), sqlc.arg(body), sqlc.narg(created_by), sqlc.arg(at))
RETURNING *;

-- name: GetStatusIncident :one
SELECT * FROM status_incidents WHERE id = sqlc.arg(id) AND workspace_id = sqlc.arg(workspace_id);

-- name: GetStatusIncidentForUpdate :one
SELECT * FROM status_incidents WHERE id = sqlc.arg(id) AND workspace_id = sqlc.arg(workspace_id) FOR UPDATE;

-- name: ListStatusIncidents :many
-- Newest first. status filters one status, active = not resolved yet, page_id = posted to that page.
SELECT i.* FROM status_incidents i
WHERE i.workspace_id = sqlc.arg(workspace_id)
  AND (sqlc.narg(status)::text IS NULL OR i.status = sqlc.narg(status)::text)
  AND (sqlc.narg(active)::boolean IS NULL OR (i.resolved_at IS NULL) = sqlc.narg(active)::boolean)
  AND (sqlc.narg(page_id)::uuid IS NULL
       OR EXISTS (SELECT 1 FROM status_incident_pages ip WHERE ip.incident_id = i.id AND ip.page_id = sqlc.narg(page_id)::uuid))
  AND (sqlc.narg(cursor_created_at)::timestamptz IS NULL
       OR (i.created_at, i.id) < (sqlc.narg(cursor_created_at)::timestamptz, sqlc.narg(cursor_id)::uuid))
ORDER BY i.created_at DESC, i.id DESC
LIMIT sqlc.arg(row_limit);

-- name: UpdateStatusIncident :one
UPDATE status_incidents SET
    title = sqlc.arg(title),
    impact = sqlc.arg(impact),
    status = sqlc.arg(status),
    resolved_at = sqlc.narg(resolved_at),
    updated_at = sqlc.arg(at)
WHERE id = sqlc.arg(id) AND workspace_id = sqlc.arg(workspace_id)
RETURNING *;

-- name: DeleteStatusIncident :execrows
DELETE FROM status_incidents WHERE id = sqlc.arg(id) AND workspace_id = sqlc.arg(workspace_id);

-- name: ListStatusIncidentUpdates :many
-- Timelines of the incidents, newest entry first.
SELECT * FROM status_incident_updates
WHERE incident_id = ANY(sqlc.arg(incident_ids)::uuid[])
ORDER BY created_at DESC, id DESC;

-- name: ListStatusIncidentPageIDs :many
SELECT incident_id, page_id FROM status_incident_pages WHERE incident_id = ANY(sqlc.arg(incident_ids)::uuid[]);

-- name: ListStatusIncidentServiceIDs :many
SELECT incident_id, service_id FROM status_incident_services WHERE incident_id = ANY(sqlc.arg(incident_ids)::uuid[]);

-- name: DeleteStatusIncidentPages :exec
DELETE FROM status_incident_pages WHERE incident_id = sqlc.arg(incident_id);

-- name: InsertStatusIncidentPage :exec
INSERT INTO status_incident_pages (incident_id, page_id) VALUES (sqlc.arg(incident_id), sqlc.arg(page_id))
ON CONFLICT DO NOTHING;

-- name: DeleteStatusIncidentServices :exec
DELETE FROM status_incident_services WHERE incident_id = sqlc.arg(incident_id);

-- name: InsertStatusIncidentService :exec
INSERT INTO status_incident_services (incident_id, service_id) VALUES (sqlc.arg(incident_id), sqlc.arg(service_id))
ON CONFLICT DO NOTHING;

-- Maintenance windows --------------------------------------------------------

-- name: InsertMaintenanceWindow :one
INSERT INTO maintenance_windows (workspace_id, title, description, starts_at, ends_at, created_by, created_at, updated_at)
VALUES (sqlc.arg(workspace_id), sqlc.arg(title), sqlc.arg(description), sqlc.arg(starts_at), sqlc.arg(ends_at),
        sqlc.narg(created_by), sqlc.arg(at), sqlc.arg(at))
RETURNING *;

-- name: GetMaintenanceWindow :one
SELECT * FROM maintenance_windows WHERE id = sqlc.arg(id) AND workspace_id = sqlc.arg(workspace_id);

-- name: GetMaintenanceWindowForUpdate :one
SELECT * FROM maintenance_windows WHERE id = sqlc.arg(id) AND workspace_id = sqlc.arg(workspace_id) FOR UPDATE;

-- name: ListMaintenanceWindows :many
-- Latest start first. phase: upcoming (not started, not canceled), active (running, not canceled),
-- past (ended or canceled); page_id = posted to that page.
SELECT w.* FROM maintenance_windows w
WHERE w.workspace_id = sqlc.arg(workspace_id)
  AND (sqlc.narg(phase)::text IS NULL
       OR (sqlc.narg(phase)::text = 'upcoming' AND w.canceled_at IS NULL AND w.starts_at > sqlc.arg(as_of)::timestamptz)
       OR (sqlc.narg(phase)::text = 'active' AND w.canceled_at IS NULL AND w.starts_at <= sqlc.arg(as_of)::timestamptz
           AND w.ends_at > sqlc.arg(as_of)::timestamptz)
       OR (sqlc.narg(phase)::text = 'past' AND (w.canceled_at IS NOT NULL OR w.ends_at <= sqlc.arg(as_of)::timestamptz)))
  AND (sqlc.narg(page_id)::uuid IS NULL
       OR EXISTS (SELECT 1 FROM maintenance_window_pages wp WHERE wp.window_id = w.id AND wp.page_id = sqlc.narg(page_id)::uuid))
  AND (sqlc.narg(cursor_starts_at)::timestamptz IS NULL
       OR (w.starts_at, w.id) < (sqlc.narg(cursor_starts_at)::timestamptz, sqlc.narg(cursor_id)::uuid))
ORDER BY w.starts_at DESC, w.id DESC
LIMIT sqlc.arg(row_limit);

-- name: UpdateMaintenanceWindow :one
UPDATE maintenance_windows SET
    title = sqlc.arg(title),
    description = sqlc.arg(description),
    starts_at = sqlc.arg(starts_at),
    ends_at = sqlc.arg(ends_at),
    updated_at = sqlc.arg(at)
WHERE id = sqlc.arg(id) AND workspace_id = sqlc.arg(workspace_id)
RETURNING *;

-- name: CancelMaintenanceWindow :one
UPDATE maintenance_windows SET canceled_at = sqlc.arg(at)::timestamptz, updated_at = sqlc.arg(at)::timestamptz
WHERE id = sqlc.arg(id) AND workspace_id = sqlc.arg(workspace_id)
RETURNING *;

-- name: DeleteMaintenanceWindow :execrows
DELETE FROM maintenance_windows WHERE id = sqlc.arg(id) AND workspace_id = sqlc.arg(workspace_id);

-- name: ListMaintenanceWindowPageIDs :many
SELECT window_id, page_id FROM maintenance_window_pages WHERE window_id = ANY(sqlc.arg(window_ids)::uuid[]);

-- name: ListMaintenanceWindowServiceIDs :many
SELECT window_id, service_id FROM maintenance_window_services WHERE window_id = ANY(sqlc.arg(window_ids)::uuid[]);

-- name: DeleteMaintenanceWindowPages :exec
DELETE FROM maintenance_window_pages WHERE window_id = sqlc.arg(window_id);

-- name: InsertMaintenanceWindowPage :exec
INSERT INTO maintenance_window_pages (window_id, page_id) VALUES (sqlc.arg(window_id), sqlc.arg(page_id))
ON CONFLICT DO NOTHING;

-- name: DeleteMaintenanceWindowServices :exec
DELETE FROM maintenance_window_services WHERE window_id = sqlc.arg(window_id);

-- name: InsertMaintenanceWindowService :exec
INSERT INTO maintenance_window_services (window_id, service_id) VALUES (sqlc.arg(window_id), sqlc.arg(service_id))
ON CONFLICT DO NOTHING;

-- Public view (Service.Public: a fixed number of queries per page) -----------

-- name: GetPublishedStatusPageByHostname :one
SELECT * FROM status_pages WHERE hostname = sqlc.arg(hostname)::text AND published;

-- name: ListPublicComponents :many
-- The components of a page in page order with the current state of their service
-- ('unknown' while it has none), the last heartbeat of the server the service runs
-- on (the server of its current deployment, else its own; NULL when it has none, or
-- the server is pending or revoked) and whether maintenance covers it at now_ts
-- (maintenance mode, or a window that is not canceled). Services deleted meanwhile are skipped.
SELECT c.service_id, c.display_name, c.group_name, c.url, COALESCE(u.state, 'unknown')::text AS state,
       srv.last_seen_at AS server_last_seen_at,
       (s.maintenance_enabled OR EXISTS (
           SELECT 1 FROM maintenance_window_services ws
           JOIN maintenance_windows w ON w.id = ws.window_id
           WHERE ws.service_id = c.service_id AND w.canceled_at IS NULL
             AND w.starts_at <= sqlc.arg(now_ts)::timestamptz AND w.ends_at > sqlc.arg(now_ts)::timestamptz
       ))::boolean AS in_maintenance
FROM status_page_components c
JOIN services s ON s.id = c.service_id AND s.deleted_at IS NULL
LEFT JOIN uptime_state u ON u.service_id = c.service_id
LEFT JOIN deployments d ON d.id = s.current_deployment_id
LEFT JOIN servers srv ON srv.id = COALESCE(d.server_id, s.server_id) AND srv.status IN ('online', 'offline')
WHERE c.page_id = sqlc.arg(page_id)
ORDER BY c.position;

-- name: ListPublicDailyUptime :many
-- Day rollups of the page's services in [from_ts, to_ts).
SELECT d.service_id, d.bucket, d.checks, d.up, d.degraded, d.down, d.maintenance
FROM uptime_1d d
WHERE d.service_id IN (SELECT c.service_id FROM status_page_components c WHERE c.page_id = sqlc.arg(page_id))
  AND d.bucket >= sqlc.arg(from_ts)::timestamptz AND d.bucket < sqlc.arg(to_ts)::timestamptz;

-- name: ListPublicPartialDayUptime :many
-- Minute rollups since since_ts summed per service: today's partial day, fresher than the hourly day rollup.
SELECT m.service_id,
       COALESCE(SUM(m.checks), 0)::int AS checks,
       COALESCE(SUM(m.up), 0)::int AS up,
       COALESCE(SUM(m.degraded), 0)::int AS degraded,
       COALESCE(SUM(m.down), 0)::int AS down,
       COALESCE(SUM(m.maintenance), 0)::int AS maintenance
FROM uptime_1m m
WHERE m.service_id IN (SELECT c.service_id FROM status_page_components c WHERE c.page_id = sqlc.arg(page_id))
  AND m.bucket >= sqlc.arg(since_ts)::timestamptz
GROUP BY m.service_id;

-- name: ListPublicIncidents :many
-- Incidents posted to the page that are not resolved or were resolved since since_ts, newest first.
SELECT i.id, i.title, i.impact, i.status, i.created_at, i.resolved_at
FROM status_incidents i
JOIN status_incident_pages ip ON ip.incident_id = i.id
WHERE ip.page_id = sqlc.arg(page_id) AND (i.resolved_at IS NULL OR i.resolved_at >= sqlc.arg(since_ts)::timestamptz)
ORDER BY i.created_at DESC, i.id DESC
LIMIT 300;

-- name: ListPublicIncidentUpdates :many
SELECT incident_id, status, body, created_at
FROM status_incident_updates
WHERE incident_id = ANY(sqlc.arg(incident_ids)::uuid[])
ORDER BY created_at DESC, id DESC;

-- name: ListPublicMaintenance :many
-- Windows posted to the page that are not canceled, end after as_of and start by horizon, soonest first.
SELECT w.id, w.title, w.description, w.starts_at, w.ends_at, w.created_at, w.updated_at
FROM maintenance_windows w
JOIN maintenance_window_pages wp ON wp.window_id = w.id
WHERE wp.page_id = sqlc.arg(page_id) AND w.canceled_at IS NULL
  AND w.ends_at > sqlc.arg(as_of)::timestamptz AND w.starts_at <= sqlc.arg(horizon)::timestamptz
ORDER BY w.starts_at, w.id
LIMIT 100;

-- Cloudflare DNS of a page's hostname (internal/integrations/cloudflare) -------

-- name: GetStatusPageByID :one
-- A page without the workspace check, for the system-level DNS sync.
SELECT * FROM status_pages WHERE id = sqlc.arg(id);

-- name: ListCloudflareWorkspaceStatusPages :many
-- Every status page of a workspace that has a hostname.
SELECT * FROM status_pages WHERE workspace_id = sqlc.arg(workspace_id) AND hostname IS NOT NULL AND hostname <> '' ORDER BY created_at, id;

-- name: SetStatusPageCloudflareZone :exec
-- First zone match of a page's hostname: its records are managed from now on, and it starts proxied.
UPDATE status_pages
SET cf_zone_id = sqlc.arg(zone_id), cf_zone_name = sqlc.arg(zone_name),
    cf_proxied = sqlc.arg(proxied),
    cf_status = CASE WHEN sqlc.arg(zone_id)::text = '' THEN '' ELSE cf_status END,
    cf_message = '', cf_error = ''
WHERE id = sqlc.arg(id);

-- name: SetStatusPageCloudflareMatch :exec
-- Re-match an existing page (hostname changed, zones refreshed) without touching its settings.
UPDATE status_pages
SET cf_zone_id = sqlc.arg(zone_id), cf_zone_name = sqlc.arg(zone_name),
    cf_status = CASE WHEN sqlc.arg(zone_id)::text = '' THEN '' WHEN cf_zone_id <> sqlc.arg(zone_id)::text THEN '' ELSE cf_status END,
    cf_message = CASE WHEN cf_zone_id <> sqlc.arg(zone_id)::text THEN '' ELSE cf_message END,
    cf_error = CASE WHEN cf_zone_id <> sqlc.arg(zone_id)::text THEN '' ELSE cf_error END
WHERE id = sqlc.arg(id);

-- name: SetStatusPageCloudflareSettings :one
UPDATE status_pages
SET cf_proxied = COALESCE(sqlc.narg(proxied)::boolean, cf_proxied)
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: SetStatusPageOriginLock :one
UPDATE status_pages SET cf_origin_lock = sqlc.arg(enabled) WHERE id = sqlc.arg(id) RETURNING *;

-- name: SetStatusPageCloudflareState :exec
UPDATE status_pages
SET cf_status = sqlc.arg(status), cf_message = sqlc.arg(message), cf_error = sqlc.arg(error), cf_checked_at = now()
WHERE id = sqlc.arg(id);

-- name: SetStatusPageCloudflareError :exec
UPDATE status_pages SET cf_error = sqlc.arg(error), cf_checked_at = now() WHERE id = sqlc.arg(id);

-- name: ClearWorkspaceStatusPagesCloudflare :exec
-- The connection was removed: forget zone matches and states of the team's pages.
UPDATE status_pages
SET cf_zone_id = '', cf_zone_name = '', cf_status = '', cf_message = '', cf_error = '', cf_checked_at = NULL
WHERE workspace_id = sqlc.arg(workspace_id) AND cf_zone_id <> '';

-- name: ListOriginLockStatusPages :many
-- Of the pages served on these hostnames, those eligible for the Cloudflare origin
-- lock: published, opted in, proxied, in a connected zone with DNS managed by
-- NebulaCtrl, and with a proxied A/AAAA record of ours. The row carries the zone's
-- Authenticated Origin Pulls state.
SELECT sp.id AS page_id, COALESCE(sp.hostname, '')::text AS hostname, sp.cf_zone_id AS zone_id, sp.workspace_id, COALESCE(aop.state, '')::text AS aop_state
FROM status_pages sp
LEFT JOIN cloudflare_zone_aop aop ON aop.workspace_id = sp.workspace_id AND aop.zone_id = sp.cf_zone_id
WHERE sp.hostname = ANY(sqlc.arg(hostnames)::text[])
  AND sp.published AND sp.cf_origin_lock AND sp.cf_proxied AND sp.cf_zone_id <> ''
  AND EXISTS (
      SELECT 1 FROM cloudflare_records r
      WHERE r.zone_id = sp.cf_zone_id AND r.hostname = sp.hostname AND r.proxied AND r.type IN ('A','AAAA'));

-- name: ListStatusOriginLockServerIDs :many
-- The control-plane server when a published page may be origin-locked (opted in,
-- proxied record of ours). zone_id restricts to one zone; empty means every zone.
SELECT srv.id
FROM servers srv
WHERE srv.is_control_plane
  AND EXISTS (
      SELECT 1 FROM status_pages sp
      WHERE sp.published AND sp.cf_origin_lock AND sp.cf_proxied AND sp.cf_zone_id <> ''
        AND (sqlc.arg(zone_id)::text = '' OR sp.cf_zone_id = sqlc.arg(zone_id)::text)
        AND EXISTS (
            SELECT 1 FROM cloudflare_records r
            WHERE r.zone_id = sp.cf_zone_id AND r.hostname = sp.hostname AND r.proxied AND r.type IN ('A','AAAA')))
ORDER BY srv.id;

-- Start from project ---------------------------------------------------------

-- name: ProjectInWorkspace :one
SELECT EXISTS (SELECT 1 FROM projects WHERE id = sqlc.arg(project_id) AND workspace_id = sqlc.arg(workspace_id));

-- name: ListPrefillServices :many
-- The services a page starts with for a project: those of its production
-- environment with at least one public domain (a served domain without an IP
-- allow list, on a service that is not tailnet-only), by name.
SELECT s.id, s.name
FROM services s
JOIN environments e ON e.id = s.environment_id AND e.is_production AND e.kind = 'standard'
JOIN projects p ON p.id = e.project_id
WHERE p.id = sqlc.arg(project_id) AND p.workspace_id = sqlc.arg(workspace_id) AND s.deleted_at IS NULL
  AND EXISTS (
      SELECT 1 FROM domains d
      WHERE d.service_id = s.id AND d.kind = 'serve'
        AND COALESCE(jsonb_array_length(d.options -> 'access' -> 'allowCidrs'), 0) = 0)
  AND NOT EXISTS (SELECT 1 FROM service_tailnet t WHERE t.service_id = s.id AND t.applied_hide_public)
ORDER BY lower(s.name), s.id
LIMIT sqlc.arg(row_limit);

-- name: ListCloudflareStatusPageIDs :many
-- Pages whose hostname is in a connected zone, in every workspace: the control-plane server's public IPs changed.
SELECT id FROM status_pages WHERE cf_zone_id <> '' ORDER BY created_at, id;

-- name: DeleteProjectStatusPageComponents :many
-- Detaches the services of a project from every status page; returns the pages that changed.
DELETE FROM status_page_components c
USING services s, environments e
WHERE c.service_id = s.id AND s.environment_id = e.id AND e.project_id = $1
RETURNING c.page_id;
