-- name: InsertGitConnection :one
INSERT INTO git_connections (id, workspace_id, provider, name, base_url, auth_type, status, status_message, account_login, credentials_enc, webhook_secret_enc, meta, created_by)
VALUES (sqlc.arg(id), sqlc.arg(workspace_id), sqlc.arg(provider), sqlc.arg(name), sqlc.arg(base_url), sqlc.arg(auth_type), sqlc.arg(status), sqlc.arg(status_message),
        sqlc.arg(account_login), sqlc.arg(credentials_enc), sqlc.arg(webhook_secret_enc), sqlc.arg(meta), sqlc.narg(created_by))
RETURNING *;

-- name: GetGitConnection :one
-- Loads a connection of the caller's workspace; another workspace's is a no-rows result.
SELECT * FROM git_connections
WHERE id = sqlc.arg(id) AND workspace_id = sqlc.arg(workspace_id);

-- name: GetGitConnectionInternal :one
-- GetGitConnection without the workspace check. For jobs and webhooks that
-- authenticate through the connection's own secret; never for an id taken from an
-- authenticated user request.
SELECT * FROM git_connections WHERE id = sqlc.arg(id);

-- name: ListGitConnections :many
SELECT * FROM git_connections WHERE workspace_id = $1 ORDER BY created_at, id;

-- name: UpdateGitConnection :one
UPDATE git_connections
SET name = sqlc.arg(name),
    base_url = sqlc.arg(base_url),
    status = sqlc.arg(status),
    status_message = sqlc.arg(status_message),
    account_login = sqlc.arg(account_login),
    credentials_enc = sqlc.arg(credentials_enc),
    meta = sqlc.arg(meta),
    updated_at = now()
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: SetGitConnectionAuthType :exec
UPDATE git_connections SET auth_type = $2, updated_at = now() WHERE id = $1;

-- name: SetGitConnectionStatus :exec
UPDATE git_connections SET status = $2, status_message = $3, updated_at = now() WHERE id = $1;

-- name: SetGitConnectionCredentials :exec
UPDATE git_connections SET credentials_enc = $2, updated_at = now() WHERE id = $1;

-- name: SetGitConnectionWebhookSecret :exec
UPDATE git_connections SET webhook_secret_enc = $2, updated_at = now() WHERE id = $1;

-- name: SetGitConnectionPending :exec
UPDATE git_connections SET pending_state_hash = $2, pending_expires_at = $3, updated_at = now() WHERE id = $1;

-- name: GetGitConnectionByState :one
SELECT * FROM git_connections
WHERE pending_state_hash = $1 AND pending_expires_at > now();

-- name: ClearGitConnectionPending :exec
UPDATE git_connections SET pending_state_hash = NULL, pending_expires_at = NULL, updated_at = now() WHERE id = $1;

-- name: DeleteGitConnection :exec
DELETE FROM git_connections WHERE id = $1;

-- name: DeleteExpiredPendingGitConnections :execrows
-- Connections whose manifest / OAuth flow never finished.
DELETE FROM git_connections
WHERE status = 'pending' AND pending_expires_at IS NOT NULL AND pending_expires_at < now() - interval '1 day'
  AND NOT EXISTS (SELECT 1 FROM service_git sg WHERE sg.connection_id = git_connections.id);

-- name: CountServiceGitForConnection :one
SELECT count(*) FROM service_git sg
JOIN services s ON s.id = sg.service_id AND s.deleted_at IS NULL
WHERE sg.connection_id = $1;

-- name: UpsertGitWebhook :exec
INSERT INTO git_webhooks (connection_id, repo_full_name, external_id, status, status_message)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (connection_id, repo_full_name)
DO UPDATE SET external_id = EXCLUDED.external_id, status = EXCLUDED.status, status_message = EXCLUDED.status_message, updated_at = now();

-- name: GetGitWebhook :one
SELECT * FROM git_webhooks WHERE connection_id = $1 AND lower(repo_full_name) = lower($2);

-- name: ListGitWebhooks :many
SELECT * FROM git_webhooks WHERE connection_id = $1 ORDER BY repo_full_name;

-- name: DeleteGitWebhook :exec
DELETE FROM git_webhooks WHERE connection_id = $1 AND lower(repo_full_name) = lower($2);

-- name: ListOrphanGitWebhooks :many
-- Hooks no live service refers to any more (unlinked or deleted services).
SELECT w.* FROM git_webhooks w
WHERE w.updated_at < now() - interval '1 hour'
  AND NOT EXISTS (
      SELECT 1 FROM service_git sg JOIN services s ON s.id = sg.service_id AND s.deleted_at IS NULL
      WHERE sg.connection_id = w.connection_id AND lower(sg.repo_full_name) = lower(w.repo_full_name))
LIMIT 100;

-- name: UpsertServiceGit :one
INSERT INTO service_git (service_id, connection_id, repo_full_name, auto_deploy, watch_paths)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (service_id)
DO UPDATE SET connection_id = EXCLUDED.connection_id, repo_full_name = EXCLUDED.repo_full_name,
              auto_deploy = EXCLUDED.auto_deploy, watch_paths = EXCLUDED.watch_paths, updated_at = now()
RETURNING *;

-- name: GetServiceGit :one
SELECT * FROM service_git WHERE service_id = $1;

-- name: ListServiceGitForServices :many
SELECT * FROM service_git WHERE service_id = ANY(sqlc.arg(ids)::uuid[]);

-- name: ListServiceGitRefs :many
-- The git link of each service with its connection's name and provider (service views).
SELECT sg.service_id, sg.connection_id, sg.repo_full_name, sg.auto_deploy, sg.watch_paths,
       c.provider, c.name AS connection_name, c.base_url
FROM service_git sg
JOIN git_connections c ON c.id = sg.connection_id
WHERE sg.service_id = ANY(sqlc.arg(ids)::uuid[]);

-- name: CopyServiceGit :exec
-- Links a service to the repository of another (previews): auto-deploy off, no watch paths.
INSERT INTO service_git (service_id, connection_id, repo_full_name, auto_deploy, watch_paths)
SELECT sqlc.arg(to_service_id)::uuid, sg.connection_id, sg.repo_full_name, false, '{}'
FROM service_git sg WHERE sg.service_id = sqlc.arg(from_service_id)::uuid
ON CONFLICT (service_id) DO NOTHING;

-- name: DeleteServiceGit :exec
DELETE FROM service_git WHERE service_id = $1;

-- name: ListServiceGitForRepo :many
-- Live services linked to a repository (case-insensitive), with their config.
SELECT sqlc.embed(sg), s.config AS service_config
FROM service_git sg
JOIN services s ON s.id = sg.service_id AND s.deleted_at IS NULL
WHERE sg.connection_id = $1 AND lower(sg.repo_full_name) = lower($2)
ORDER BY s.created_at, s.id;

-- name: SetServiceGitPending :exec
UPDATE service_git SET pending_commit = $2 WHERE service_id = $1;

-- name: ClaimServiceGitPending :one
-- Takes the queued push of a service and returns it (no row when none).
UPDATE service_git AS sg SET pending_commit = NULL
FROM (SELECT g.service_id, g.pending_commit FROM service_git g
      WHERE g.service_id = $1 AND g.pending_commit IS NOT NULL FOR UPDATE) AS old
WHERE sg.service_id = old.service_id
RETURNING old.pending_commit::jsonb AS pending_commit;

-- name: ListServiceGitIdleWithPending :many
-- Services with a queued push whose deployments have all finished.
SELECT sg.service_id FROM service_git sg
JOIN services s ON s.id = sg.service_id AND s.deleted_at IS NULL
WHERE sg.pending_commit IS NOT NULL
  AND NOT EXISTS (SELECT 1 FROM deployments d WHERE d.service_id = sg.service_id
                  AND d.status IN ('created','queued','preparing','building','releasing','verifying'))
LIMIT 100;

-- name: InsertGitDeliveryID :execrows
INSERT INTO git_delivery_ids (connection_id, delivery_id) VALUES ($1, $2) ON CONFLICT DO NOTHING;

-- name: DeleteGitDeliveryID :exec
DELETE FROM git_delivery_ids WHERE connection_id = $1 AND delivery_id = $2;

-- name: PruneGitDeliveryIDs :execrows
DELETE FROM git_delivery_ids WHERE seen_at < $1;

-- name: InsertGitDelivery :one
INSERT INTO git_deliveries (connection_id, delivery_id, event, outcome, http_status, summary, repo_full_name, branch, commit_sha, headers, payload_bytes, deployments)
VALUES (sqlc.arg(connection_id), sqlc.arg(delivery_id), sqlc.arg(event), sqlc.arg(outcome), sqlc.arg(http_status), sqlc.arg(summary),
        sqlc.arg(repo_full_name), sqlc.arg(branch), sqlc.arg(commit_sha), sqlc.arg(headers), sqlc.arg(payload_bytes), sqlc.arg(deployments)::uuid[])
RETURNING *;

-- name: ListGitDeliveries :many
SELECT * FROM git_deliveries
WHERE connection_id = sqlc.arg(connection_id)
  AND (sqlc.narg(before_id)::bigint IS NULL OR id < sqlc.narg(before_id)::bigint)
ORDER BY id DESC
LIMIT sqlc.arg(row_limit);

-- name: PruneGitDeliveries :execrows
-- Keeps the newest 200 per connection, none older than 14 days.
DELETE FROM git_deliveries WHERE id IN (
    SELECT x.id FROM (
        SELECT id, received_at, row_number() OVER (PARTITION BY connection_id ORDER BY id DESC) AS rn FROM git_deliveries
    ) x WHERE x.rn > 200 OR x.received_at < now() - interval '14 days');

-- name: SetDeploymentCommit :exec
UPDATE deployments SET commit_sha = $2, commit_message = $3, commit_author = $4, commit_branch = $5 WHERE id = $1;

-- name: SetDeploymentCommitSHA :exec
-- Fills in the commit a git deployment built when the trigger did not know it.
UPDATE deployments SET commit_sha = $2, commit_branch = CASE WHEN commit_branch = '' THEN $3 ELSE commit_branch END
WHERE id = $1 AND commit_sha = '';

-- name: ListDeploymentsNeedingGitStatus :many
-- Deployments of linked git services whose provider commit status is behind.
SELECT d.id FROM deployments d
JOIN service_git sg ON sg.service_id = d.service_id
LEFT JOIN git_status_reports r ON r.deployment_id = d.id
WHERE d.commit_sha <> '' AND d.created_at > now() - interval '3 days' AND d.trigger NOT IN ('reconcile','drain')
  AND r.state IS DISTINCT FROM (CASE d.status
        WHEN 'succeeded' THEN 'success'
        WHEN 'failed' THEN 'failure' WHEN 'rolled_back' THEN 'failure' WHEN 'rollback_failed' THEN 'failure'
        WHEN 'canceled' THEN 'error'
        ELSE 'pending' END)
  AND (r.deployment_id IS NULL OR r.error = '' OR (r.attempts < 8 AND r.reported_at < now() - interval '1 minute'))
ORDER BY d.created_at
LIMIT 100;

-- name: GetGitStatusSubject :one
-- Everything needed to report a deployment's commit status.
SELECT sqlc.embed(d),
       s.slug AS service_slug,
       s.name AS service_name,
       s.environment_id,
       e.slug AS environment_slug,
       e.project_id,
       p.workspace_id,
       sg.connection_id,
       sg.repo_full_name,
       COALESCE(r.state, '') AS reported_state
FROM deployments d
JOIN services s ON s.id = d.service_id
JOIN environments e ON e.id = s.environment_id
JOIN projects p ON p.id = e.project_id
JOIN service_git sg ON sg.service_id = d.service_id
LEFT JOIN git_status_reports r ON r.deployment_id = d.id
WHERE d.id = $1;

-- name: MarkGitStatusReported :exec
INSERT INTO git_status_reports (deployment_id, state, error, attempts) VALUES ($1, $2, '', 0)
ON CONFLICT (deployment_id) DO UPDATE SET state = EXCLUDED.state, error = '', attempts = 0, reported_at = now();

-- name: MarkGitStatusFailed :exec
INSERT INTO git_status_reports (deployment_id, state, error, attempts) VALUES ($1, '', $2, 1)
ON CONFLICT (deployment_id) DO UPDATE SET error = EXCLUDED.error, attempts = git_status_reports.attempts + 1, reported_at = now();
