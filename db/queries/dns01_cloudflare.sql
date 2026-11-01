-- name: ListServerTLSDomains :many
-- Live domains of services placed on the server that ask for an automatic
-- certificate, with the evidence of their Cloudflare DNS sync.
SELECT d.id, d.hostname, d.generated, d.cf_zone_id, d.cf_status, d.cf_error
FROM domains d
JOIN services s ON s.id = d.service_id AND s.deleted_at IS NULL
WHERE s.server_id = sqlc.arg(server_id)::uuid AND d.tls_mode = 'auto'
ORDER BY d.hostname, d.id;

-- name: ListDomainDNSEvidence :many
-- The Cloudflare DNS sync state of the given domains.
SELECT id, hostname, cf_zone_id, cf_status, cf_error
FROM domains
WHERE id = ANY(sqlc.arg(domain_ids)::uuid[]);

-- name: ListWorkspacePublishedStatusPages :many
-- The published status pages of a team that have a hostname, with the evidence of their Cloudflare DNS sync.
SELECT id, COALESCE(lower(hostname), '')::text AS hostname, cf_zone_id, cf_status, cf_error
FROM status_pages
WHERE workspace_id = $1 AND published AND hostname IS NOT NULL AND hostname <> ''
ORDER BY id;
