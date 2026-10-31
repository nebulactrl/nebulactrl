-- ===== Request-origin geolocation ===========================================

-- name: ListServersGeo :many
-- Geolocation setting of the servers a report reads (workspace-scoped by the caller).
SELECT id, traffic_analytics_enabled, geo_enabled, geo_source, geo_level
FROM servers
WHERE id = ANY(sqlc.arg(server_ids)::uuid[]) AND workspace_id = sqlc.arg(workspace_id);

-- name: GetGeoCredentials :one
SELECT * FROM geo_credentials WHERE workspace_id = $1;

-- name: UpsertGeoCredentials :one
INSERT INTO geo_credentials (workspace_id, maxmind_account_id, maxmind_key_enc)
VALUES ($1, $2, $3)
ON CONFLICT (workspace_id) DO UPDATE
SET maxmind_account_id = EXCLUDED.maxmind_account_id, maxmind_key_enc = EXCLUDED.maxmind_key_enc, updated_at = now()
RETURNING *;

-- name: DeleteGeoCredentials :execrows
DELETE FROM geo_credentials WHERE workspace_id = $1;

-- name: ListMaxMindGeoServerIDs :many
-- Online-or-not servers of a team that use the MaxMind source (resynced when the key changes).
SELECT id FROM servers
WHERE workspace_id = $1 AND geo_enabled AND geo_source = 'maxmind' AND status <> 'revoked';
