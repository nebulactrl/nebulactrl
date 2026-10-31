-- name: ListCloudflareZoneClaims :many
-- Every team's cached Cloudflare zone list, oldest connection first. A zone in
-- the list that is active at Cloudflare is a claim on every name inside it
-- (hostclaim.ClaimedByOther).
SELECT workspace_id, zones, created_at
FROM cloudflare_connections
ORDER BY created_at, workspace_id;
