-- name: GetDeployHook :one
SELECT * FROM deploy_hooks WHERE service_id = $1;

-- name: GetDeployHookForCall :one
-- The hook of a live service for an incoming call: the project and workspace
-- come with it so the call needs no second lookup.
SELECT h.*, s.environment_id, e.project_id, p.workspace_id
FROM deploy_hooks h
JOIN services s ON s.id = h.service_id AND s.deleted_at IS NULL
JOIN environments e ON e.id = s.environment_id
JOIN projects p ON p.id = e.project_id
WHERE h.service_id = $1;

-- name: EnableDeployHook :one
-- Turns the hook on with a fresh secret (the first time and after a disable).
INSERT INTO deploy_hooks (service_id, enabled, secret_hash, secret_set_at)
VALUES ($1, true, $2, now())
ON CONFLICT (service_id) DO UPDATE
SET enabled = true, secret_hash = EXCLUDED.secret_hash, secret_set_at = now(), pending = NULL, updated_at = now()
RETURNING *;

-- name: RotateDeployHook :one
UPDATE deploy_hooks SET secret_hash = $2, secret_set_at = now(), updated_at = now()
WHERE service_id = $1 AND enabled
RETURNING *;

-- name: DisableDeployHook :one
-- A disabled hook keeps no secret and no queued call.
UPDATE deploy_hooks SET enabled = false, secret_hash = NULL, secret_set_at = NULL, pending = NULL, updated_at = now()
WHERE service_id = $1
RETURNING *;

-- name: TouchDeployHook :exec
UPDATE deploy_hooks SET last_called_at = now(), last_outcome = $2 WHERE service_id = $1;

-- name: SetDeployHookPending :exec
UPDATE deploy_hooks SET pending = $2 WHERE service_id = $1 AND enabled;

-- name: ClaimDeployHookPending :one
-- Takes the queued call of a hook and returns it (no row when none).
UPDATE deploy_hooks AS h SET pending = NULL
FROM (SELECT d.service_id, d.pending FROM deploy_hooks d
      WHERE d.service_id = $1 AND d.pending IS NOT NULL FOR UPDATE) AS old
WHERE h.service_id = old.service_id
RETURNING old.pending::jsonb AS pending;

-- name: ListDeployHooksIdleWithPending :many
-- Hooks with a queued call whose service has no running deployment.
SELECT h.service_id FROM deploy_hooks h
JOIN services s ON s.id = h.service_id AND s.deleted_at IS NULL
WHERE h.enabled AND h.pending IS NOT NULL
  AND NOT EXISTS (SELECT 1 FROM deployments d WHERE d.service_id = h.service_id
                  AND d.status IN ('created','queued','preparing','building','releasing','verifying'))
LIMIT 100;
