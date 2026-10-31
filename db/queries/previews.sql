-- name: GetPreviewSettings :one
SELECT * FROM preview_settings WHERE project_id = $1;

-- name: UpsertPreviewSettings :one
INSERT INTO preview_settings (project_id, workspace_id, enabled, base_environment_id, fork_prs, max_concurrent, idle_days, seed_commands, excluded_services)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
ON CONFLICT (project_id) DO UPDATE
SET enabled = EXCLUDED.enabled, base_environment_id = EXCLUDED.base_environment_id, fork_prs = EXCLUDED.fork_prs,
    max_concurrent = EXCLUDED.max_concurrent, idle_days = EXCLUDED.idle_days, seed_commands = EXCLUDED.seed_commands,
    excluded_services = EXCLUDED.excluded_services, updated_at = now()
RETURNING *;

-- name: ListPreviewTargets :many
-- Projects whose enabled previews apply to a PR: the base environment holds at
-- least one of the services connected to the PR's repository.
SELECT DISTINCT ps.*, p.slug AS project_slug, p.name AS project_name
FROM preview_settings ps
JOIN projects p ON p.id = ps.project_id
JOIN services s ON s.environment_id = ps.base_environment_id AND s.deleted_at IS NULL
WHERE ps.enabled AND s.id = ANY(sqlc.arg(service_ids)::uuid[]);

-- name: InsertPreviewEnvironment :one
INSERT INTO environments (project_id, name, slug, is_production, kind, parent_environment_id, pr_number, pr_title, connection_id)
VALUES ($1, $2, $3, false, 'preview', $4, $5, $6, $7)
RETURNING *;

-- name: UpdatePreviewEnvironmentTitle :exec
UPDATE environments SET name = $2, pr_title = $3 WHERE id = $1;

-- name: CountEnvironmentPreviews :one
SELECT count(*) FROM environments WHERE parent_environment_id = $1;

-- name: InsertPreview :one
INSERT INTO previews (workspace_id, project_id, environment_id, base_environment_id, connection_id, provider, repo, pr_number,
                      pr_title, pr_author, pr_url, head_branch, head_sha, base_branch, from_fork, affected_service_ids, status)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, 'creating')
RETURNING *;

-- name: GetPreview :one
SELECT * FROM previews WHERE id = $1;

-- name: LockPreview :one
SELECT * FROM previews WHERE id = $1 FOR UPDATE;

-- name: GetPreviewByPR :one
SELECT * FROM previews WHERE project_id = $1 AND connection_id = $2 AND repo = $3 AND pr_number = $4;

-- name: ListPreviewsForPR :many
SELECT * FROM previews WHERE connection_id = $1 AND lower(repo) = lower(sqlc.arg(repo)::text) AND pr_number = $2;

-- name: ListProjectPreviews :many
SELECT * FROM previews WHERE project_id = $1 ORDER BY created_at DESC, id;

-- name: UpdatePreviewStatus :one
UPDATE previews SET status = $2, status_message = $3, updated_at = now(),
    paused_at = CASE WHEN $2 = 'paused' THEN now() ELSE NULL END
WHERE id = $1
RETURNING *;

-- name: UpdatePreviewPush :one
UPDATE previews
SET head_sha = $2, head_branch = $3, pr_title = $4, last_push_at = now(), updated_at = now()
WHERE id = $1
RETURNING *;

-- name: SetPreviewDeployState :exec
UPDATE previews SET deploy_state = $2, updated_at = now() WHERE id = $1;

-- name: ListPausablePreviews :many
-- Running previews of a project that can be paused, least recently pushed first.
SELECT * FROM previews
WHERE project_id = $1 AND status IN ('ready','failed') AND id <> sqlc.arg(except_id)::uuid
ORDER BY last_push_at, id;

-- name: CountRunningPreviews :one
SELECT count(*) FROM previews WHERE project_id = $1 AND status NOT IN ('paused','deleting');

-- name: ListIdlePreviews :many
SELECT pr.* FROM previews pr
JOIN preview_settings ps ON ps.project_id = pr.project_id
WHERE pr.status <> 'deleting' AND pr.last_push_at < now() - make_interval(days => ps.idle_days);

-- name: ListProjectsWithPreviews :many
SELECT DISTINCT project_id FROM previews WHERE status NOT IN ('paused','deleting');

-- name: DeletePreviewEnvironment :execrows
DELETE FROM environments WHERE id = $1 AND kind = 'preview';

-- name: ListEnvironmentLiveServiceIDs :many
SELECT id, slug FROM services WHERE environment_id = $1 AND deleted_at IS NULL ORDER BY created_at, id;

-- name: UpdateServiceSlug :exec
UPDATE services SET slug = $2 WHERE id = $1;

-- name: CloneServiceVolumes :exec
INSERT INTO volumes (service_id, name, mount_path)
SELECT sqlc.arg(new_service_id)::uuid, name, mount_path FROM volumes WHERE service_id = sqlc.arg(old_service_id)::uuid
ON CONFLICT (service_id, name) DO NOTHING;

-- name: ListEnvironmentSharedVariablesRaw :many
SELECT * FROM shared_variables WHERE environment_id = $1 ORDER BY key;

-- name: CopySharedVariable :exec
INSERT INTO shared_variables (workspace_id, project_id, environment_id, key, value_enc, is_secret, source, template_enc, gen_calls, comment, id)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
ON CONFLICT DO NOTHING;



-- name: LatestPreviewDeployments :many
-- The newest deployment of each service, for rendering the PR comment.
SELECT DISTINCT ON (service_id) * FROM deployments
WHERE service_id = ANY(sqlc.arg(service_ids)::uuid[])
ORDER BY service_id, created_at DESC, id DESC;


-- name: ListPreviewServiceDomains :many
SELECT d.* FROM domains d
JOIN services s ON s.id = d.service_id
WHERE s.environment_id = $1 AND s.deleted_at IS NULL AND d.kind = 'serve'
ORDER BY d.service_id, d.created_at, d.id;

-- name: UpdateWave :execrows
-- Writes the deploy wave state (and optionally the status) of a preview, but
-- only while the wave is still the current one and the preview is not paused or
-- being deleted. Zero rows means a newer wave (or a pause/delete) took over.
UPDATE previews
SET deploy_state = sqlc.arg(state),
    status = COALESCE(sqlc.narg(status)::text, status),
    status_message = COALESCE(sqlc.narg(message)::text, status_message),
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND COALESCE((deploy_state->>'seq')::int, 0) = sqlc.arg(seq)::int
  AND status NOT IN ('paused','deleting');

-- name: StartWave :one
-- Begins a new deploy wave: the new state, status deploying and the new head.
UPDATE previews
SET deploy_state = sqlc.arg(state), status = 'deploying', status_message = '', paused_at = NULL,
    head_sha = sqlc.arg(head_sha), head_branch = sqlc.arg(head_branch), pr_title = sqlc.arg(pr_title),
    last_push_at = now(), updated_at = now()
WHERE id = sqlc.arg(id)
RETURNING *;
