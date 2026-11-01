-- Teams (workspaces), memberships, invites, instance settings.

-- name: CreateTeam :one
INSERT INTO workspaces (name, slug, created_by) VALUES ($1, $2, $3) RETURNING *;

-- name: ListTeamSlugsLike :many
SELECT slug FROM workspaces WHERE slug = sqlc.arg(base)::text OR slug LIKE sqlc.arg(base)::text || '-%';

-- name: UpdateTeamName :one
UPDATE workspaces SET name = $2 WHERE id = $1 RETURNING *;

-- name: DeleteTeam :execrows
DELETE FROM workspaces WHERE id = $1;

-- name: TeamHostsControlPlane :one
-- True when the team owns the server the control plane itself runs on.
SELECT EXISTS (SELECT 1 FROM servers WHERE workspace_id = $1 AND is_control_plane AND status <> 'revoked');

-- name: StartTeamDeletion :one
-- Marks the team as being deleted and clears the last failure. keep_dns is the
-- choice made with the request; forgotten starts over with every run.
UPDATE workspaces
SET deletion_state = 'deleting', deletion_error = '', deletion_step = 'projects', deletion_started_at = now(),
    deletion_forgotten = '{}', deletion_keep_dns = sqlc.arg(keep_dns)::boolean
WHERE id = sqlc.arg(id) AND deletion_state <> 'deleting'
RETURNING *;

-- name: SetTeamDeletionStep :exec
UPDATE workspaces SET deletion_step = sqlc.arg(step)::text WHERE id = sqlc.arg(id);

-- name: AddTeamDeletionForgotten :exec
-- Records a server that was removed without touching its host.
UPDATE workspaces SET deletion_forgotten = array_append(deletion_forgotten, sqlc.arg(server)::text) WHERE id = sqlc.arg(id);

-- name: FailTeamDeletion :exec
UPDATE workspaces SET deletion_state = 'failed', deletion_error = sqlc.arg(error)::text WHERE id = sqlc.arg(id);

-- name: ListTeamProjectIDs :many
-- The team's projects, the hidden system project last.
SELECT id, name, system FROM projects WHERE workspace_id = $1 ORDER BY system, created_at, id;

-- name: GetTeamDeletionCounts :one
-- What deleting the team removes. Services and databases are live ones of every
-- project, the hidden system project included.
SELECT
  (SELECT count(*) FROM projects pr WHERE pr.workspace_id = sqlc.arg(id) AND NOT pr.system)::int AS projects,
  (SELECT count(*) FROM services s JOIN environments e ON e.id = s.environment_id JOIN projects p ON p.id = e.project_id
     WHERE p.workspace_id = sqlc.arg(id) AND s.deleted_at IS NULL AND s.kind <> 'database')::int AS services,
  (SELECT count(*) FROM services s JOIN environments e ON e.id = s.environment_id JOIN projects p ON p.id = e.project_id
     WHERE p.workspace_id = sqlc.arg(id) AND s.deleted_at IS NULL AND s.kind = 'database')::int AS databases,
  (SELECT count(*) FROM volumes v JOIN services s ON s.id = v.service_id JOIN environments e ON e.id = s.environment_id JOIN projects p ON p.id = e.project_id
     WHERE p.workspace_id = sqlc.arg(id) AND s.deleted_at IS NULL)::int AS volumes,
  (SELECT count(*) FROM servers WHERE workspace_id = sqlc.arg(id) AND status = 'online')::int AS servers_online,
  (SELECT count(*) FROM servers WHERE workspace_id = sqlc.arg(id) AND status <> 'online')::int AS servers_offline,
  (SELECT count(*) FROM domains d JOIN services s ON s.id = d.service_id JOIN environments e ON e.id = s.environment_id JOIN projects p ON p.id = e.project_id
     WHERE p.workspace_id = sqlc.arg(id) AND s.deleted_at IS NULL)::int AS domains,
  (SELECT count(*) FROM status_pages WHERE workspace_id = sqlc.arg(id))::int AS status_pages,
  (SELECT count(*) FROM cloudflare_records WHERE workspace_id = sqlc.arg(id))::int AS managed_dns_records,
  ((SELECT count(*) FROM service_tailnet WHERE workspace_id = sqlc.arg(id) AND device_id <> '')
   + (SELECT count(*) FROM server_mesh WHERE workspace_id = sqlc.arg(id) AND device_id <> '')
   + (SELECT count(*) FROM mesh_links WHERE workspace_id = sqlc.arg(id) AND device_id <> '')
   + (SELECT count(*) FROM servers WHERE workspace_id = sqlc.arg(id) AND coalesce(tailnet_device_id, '') <> ''))::int AS tailnet_devices;

-- name: InsertMembership :one
INSERT INTO memberships (user_id, workspace_id, role, project_ids)
VALUES ($1, $2, $3, $4)
ON CONFLICT (user_id, workspace_id) DO NOTHING
RETURNING *;

-- name: GetMembership :one
SELECT * FROM memberships WHERE user_id = $1 AND workspace_id = $2;

-- name: GetMembershipForUpdate :one
SELECT * FROM memberships WHERE user_id = $1 AND workspace_id = $2 FOR UPDATE;

-- name: FirstMembership :one
-- The user's oldest team: the default after login and when the stored team
-- is gone.
SELECT * FROM memberships WHERE user_id = $1 ORDER BY created_at, workspace_id LIMIT 1;

-- name: ListMyTeams :many
SELECT w.id, w.name, w.slug, w.created_by, w.created_at,
       w.deletion_state, w.deletion_error, w.deletion_step, w.deletion_started_at, w.deletion_forgotten, w.deletion_keep_dns,
       m.role, m.project_ids,
       (SELECT count(*) FROM memberships mm WHERE mm.workspace_id = w.id)::int AS member_count
FROM memberships m
JOIN workspaces w ON w.id = m.workspace_id
WHERE m.user_id = $1
ORDER BY m.created_at, w.id;

-- name: GetTeamForUser :one
SELECT w.id, w.name, w.slug, w.created_by, w.created_at,
       w.deletion_state, w.deletion_error, w.deletion_step, w.deletion_started_at, w.deletion_forgotten, w.deletion_keep_dns,
       m.role, m.project_ids,
       (SELECT count(*) FROM memberships mm WHERE mm.workspace_id = w.id)::int AS member_count
FROM memberships m
JOIN workspaces w ON w.id = m.workspace_id
WHERE m.user_id = $1 AND w.id = $2;

-- name: ListMembers :many
SELECT m.user_id, m.workspace_id, m.role, m.project_ids, m.created_at AS joined_at,
       u.email, u.name
FROM memberships m
JOIN users u ON u.id = m.user_id
WHERE m.workspace_id = $1
ORDER BY CASE m.role WHEN 'owner' THEN 0 WHEN 'admin' THEN 1 WHEN 'developer' THEN 2 ELSE 3 END,
         lower(u.name), u.id;

-- name: GetMember :one
SELECT m.user_id, m.workspace_id, m.role, m.project_ids, m.created_at AS joined_at,
       u.email, u.name
FROM memberships m
JOIN users u ON u.id = m.user_id
WHERE m.workspace_id = $1 AND m.user_id = $2;

-- name: UpdateMembership :one
UPDATE memberships SET role = $3, project_ids = $4
WHERE workspace_id = $1 AND user_id = $2
RETURNING *;

-- name: DeleteMembership :execrows
DELETE FROM memberships WHERE workspace_id = $1 AND user_id = $2;

-- name: LockOwners :many
-- Locks the owner rows of a team so concurrent demotions cannot both succeed.
SELECT user_id FROM memberships WHERE workspace_id = $1 AND role = 'owner' FOR UPDATE;

-- name: CountTeamProjects :one
SELECT count(*) FROM projects WHERE workspace_id = sqlc.arg(workspace_id) AND id = ANY(sqlc.arg(ids)::uuid[]);

-- name: RevokeUserTokensInTeam :exec
-- Tokens of a removed member stop working at once.
UPDATE api_tokens SET revoked_at = COALESCE(revoked_at, now())
WHERE workspace_id = $1 AND user_id = $2;

-- Invites ---------------------------------------------------------------------

-- name: InsertInvite :one
INSERT INTO team_invites (workspace_id, token_hash, prefix, email, role, project_ids, max_uses, expires_at, created_by)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
RETURNING *;

-- name: ListInvites :many
SELECT i.*, COALESCE(NULLIF(u.name, ''), u.email, '')::text AS created_by_label
FROM team_invites i
LEFT JOIN users u ON u.id = i.created_by
WHERE i.workspace_id = $1 AND i.revoked_at IS NULL
ORDER BY i.created_at DESC, i.id;

-- name: GetInviteByHash :one
SELECT i.*, w.name AS team_name, w.slug AS team_slug
FROM team_invites i
JOIN workspaces w ON w.id = i.workspace_id
WHERE i.token_hash = $1;

-- name: GetInvite :one
SELECT * FROM team_invites WHERE id = $1 AND workspace_id = $2;

-- name: DeleteInvite :one
DELETE FROM team_invites WHERE id = $1 AND workspace_id = $2 RETURNING *;

-- name: ConsumeInvite :one
-- Atomically takes one use of a live invite; no row = expired, revoked or
-- used up.
UPDATE team_invites
SET use_count = use_count + 1
WHERE id = $1 AND expires_at > now() AND revoked_at IS NULL AND use_count < max_uses
RETURNING *;

-- name: InsertInviteUse :execrows
INSERT INTO team_invite_uses (invite_id, user_id) VALUES ($1, $2)
ON CONFLICT DO NOTHING;

-- name: MarkInviteEmailSent :exec
UPDATE team_invites SET email_sent_at = now() WHERE id = $1;

-- Instance settings -------------------------------------------------------------

-- name: GetInstanceSettings :one
SELECT * FROM instance_settings WHERE id;

-- name: SetInstanceMCPEnabled :exec
UPDATE instance_settings SET mcp_enabled = sqlc.arg(mcp_enabled), updated_at = now() WHERE id;

-- name: UpdateInstanceSettings :one
UPDATE instance_settings SET
    allow_team_creation = sqlc.arg(allow_team_creation),
    smtp_host = sqlc.arg(smtp_host),
    smtp_port = sqlc.arg(smtp_port),
    smtp_tls = sqlc.arg(smtp_tls),
    smtp_username = sqlc.arg(smtp_username),
    smtp_password_enc = COALESCE(sqlc.narg(smtp_password_enc), smtp_password_enc),
    smtp_from = sqlc.arg(smtp_from),
    smtp_insecure_skip_verify = sqlc.arg(smtp_insecure_skip_verify),
    updated_at = now()
WHERE id
RETURNING *;

-- name: ClearInstanceSMTPPassword :exec
UPDATE instance_settings SET smtp_password_enc = NULL, updated_at = now() WHERE id;

-- SSE scoping -------------------------------------------------------------------

-- name: EventWorkspace :one
-- The team that owns a project, environment, service, deployment, domain, server, API token, registry,
-- shared network, git connection, notification channel, backup destination, status page, platform service or log drain id
-- (used to keep the live feed within one team and to attribute system audit rows). No row = unknown or deleted.
SELECT workspace_id FROM projects WHERE projects.id = $1
UNION ALL SELECT p.workspace_id FROM environments e JOIN projects p ON p.id = e.project_id WHERE e.id = $1
UNION ALL SELECT p.workspace_id FROM services s JOIN environments e ON e.id = s.environment_id JOIN projects p ON p.id = e.project_id WHERE s.id = $1
UNION ALL SELECT p.workspace_id FROM deployments d JOIN services s ON s.id = d.service_id JOIN environments e ON e.id = s.environment_id JOIN projects p ON p.id = e.project_id WHERE d.id = $1
UNION ALL SELECT p.workspace_id FROM domains dm JOIN services s ON s.id = dm.service_id JOIN environments e ON e.id = s.environment_id JOIN projects p ON p.id = e.project_id WHERE dm.id = $1
UNION ALL SELECT servers.workspace_id FROM servers WHERE servers.id = $1
UNION ALL SELECT api_tokens.workspace_id FROM api_tokens WHERE api_tokens.id = $1
UNION ALL SELECT registries.workspace_id FROM registries WHERE registries.id = $1
UNION ALL SELECT shared_networks.workspace_id FROM shared_networks WHERE shared_networks.id = $1
UNION ALL SELECT git_connections.workspace_id FROM git_connections WHERE git_connections.id = $1
UNION ALL SELECT notification_channels.workspace_id FROM notification_channels WHERE notification_channels.id = $1
UNION ALL SELECT backup_destinations.workspace_id FROM backup_destinations WHERE backup_destinations.id = $1
UNION ALL SELECT status_pages.workspace_id FROM status_pages WHERE status_pages.id = $1
UNION ALL SELECT platform_services.workspace_id FROM platform_services WHERE platform_services.id = $1
UNION ALL SELECT log_drains.workspace_id FROM log_drains WHERE log_drains.id = $1
UNION ALL SELECT workspaces.id FROM workspaces WHERE workspaces.id = $1
LIMIT 1;
