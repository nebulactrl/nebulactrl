-- name: GetServerHostShell :one
-- No row means off. The workspace check keeps the lookup inside the caller's team.
SELECT s.id AS server_id, s.name, s.status,
       COALESCE(h.enabled, false)::boolean AS enabled,
       EXISTS (
           SELECT 1 FROM services sv
           JOIN environments e ON e.id = sv.environment_id
           WHERE sv.server_id = s.id AND sv.deleted_at IS NULL AND e.is_production
       ) AS production
FROM servers s
LEFT JOIN server_host_shell h ON h.server_id = s.id
WHERE s.id = $1 AND s.workspace_id = $2;

-- name: UpsertServerHostShell :exec
INSERT INTO server_host_shell (server_id, enabled, updated_by) VALUES ($1, $2, $3)
ON CONFLICT (server_id) DO UPDATE SET enabled = EXCLUDED.enabled, updated_by = EXCLUDED.updated_by, updated_at = now();
