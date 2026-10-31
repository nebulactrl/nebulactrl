-- name: ObserveDomainCertStore :one
-- The agent's observation of which ACME storage holds a domain's certificate
-- (heartbeat). Writes only on a change or when the stored one is older than five
-- minutes; no row = nothing written. The domain must belong to a service on the server.
INSERT INTO domain_cert_stores (domain_id, issued_via)
SELECT d.id, sqlc.arg(issued_via)::text
FROM domains d
JOIN services s ON s.id = d.service_id
WHERE d.id = sqlc.arg(id)::uuid AND s.server_id = sqlc.arg(server_id)::uuid
ON CONFLICT (domain_id) DO UPDATE
SET issued_via = EXCLUDED.issued_via, observed_at = now()
WHERE domain_cert_stores.issued_via IS DISTINCT FROM EXCLUDED.issued_via
   OR domain_cert_stores.observed_at < now() - interval '5 minutes'
RETURNING domain_id;

-- name: ClearDomainCertStore :execrows
-- The agent sees no certificate for the domain's host (any more).
DELETE FROM domain_cert_stores cs
USING domains d, services s
WHERE cs.domain_id = d.id AND s.id = d.service_id
  AND d.id = sqlc.arg(id)::uuid AND s.server_id = sqlc.arg(server_id)::uuid;

-- name: ListDomainCertStores :many
SELECT domain_id, issued_via FROM domain_cert_stores WHERE domain_id = ANY(sqlc.arg(domain_ids)::uuid[]);
