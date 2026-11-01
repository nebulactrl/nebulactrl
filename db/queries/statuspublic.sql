-- name: ListPublishedStatusPageHosts :many
-- The published status pages that have a hostname, for the control-plane
-- server's route table (domains.withStatusRoutes). Hostnames are stored
-- lower-case by the status pages service; lower() is a backstop.
SELECT id, COALESCE(lower(hostname), '')::text AS hostname
FROM status_pages
WHERE published AND hostname IS NOT NULL AND hostname <> ''
ORDER BY id;
