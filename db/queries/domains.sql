-- name: ListServiceDomains :many
SELECT * FROM domains WHERE service_id = $1 ORDER BY created_at, id;

-- name: ListDomainsForServices :many
SELECT * FROM domains WHERE service_id = ANY(sqlc.arg(service_ids)::uuid[]) ORDER BY created_at, id;

-- name: InsertDomain :one
-- Zero values of the rule fields take the column defaults (serve, 308, auto, no options).
INSERT INTO domains (service_id, hostname, target_port, generated, status_message,
                     kind, path_prefix, strip_prefix, add_prefix, redirect_to, redirect_code, preserve_path,
                     tls_mode, certificate_id, options, dns_resolver, component)
VALUES ($1, $2, $3, $4, $5,
        COALESCE(NULLIF(sqlc.arg(kind)::text, ''), 'serve'), sqlc.arg(path_prefix), sqlc.arg(strip_prefix), sqlc.arg(add_prefix),
        sqlc.narg(redirect_to), CASE WHEN sqlc.arg(redirect_code)::int = 0 THEN 308 ELSE sqlc.arg(redirect_code)::int END,
        sqlc.arg(preserve_path), COALESCE(NULLIF(sqlc.arg(tls_mode)::text, ''), 'auto'), sqlc.narg(certificate_id),
        COALESCE(sqlc.narg(options)::jsonb, '{}'::jsonb), sqlc.arg(dns_resolver)::text, sqlc.arg(component)::text)
RETURNING *;

-- name: UpdateDomain :one
UPDATE domains
SET hostname = sqlc.arg(hostname), path_prefix = sqlc.arg(path_prefix), strip_prefix = sqlc.arg(strip_prefix),
    add_prefix = sqlc.arg(add_prefix), kind = sqlc.arg(kind), redirect_to = sqlc.narg(redirect_to),
    redirect_code = sqlc.arg(redirect_code), preserve_path = sqlc.arg(preserve_path), tls_mode = sqlc.arg(tls_mode),
    certificate_id = sqlc.narg(certificate_id), options = sqlc.arg(options),
    dns_resolver = sqlc.arg(dns_resolver)::text,
    -- A changed host or resolver forgets the observed certificate.
    cert_status     = CASE WHEN sqlc.arg(reset_dns)::bool OR dns_resolver <> sqlc.arg(dns_resolver)::text THEN '' ELSE cert_status END,
    cert_message    = CASE WHEN sqlc.arg(reset_dns)::bool OR dns_resolver <> sqlc.arg(dns_resolver)::text THEN '' ELSE cert_message END,
    cert_issuer     = CASE WHEN sqlc.arg(reset_dns)::bool OR dns_resolver <> sqlc.arg(dns_resolver)::text THEN '' ELSE cert_issuer END,
    cert_not_after  = CASE WHEN sqlc.arg(reset_dns)::bool OR dns_resolver <> sqlc.arg(dns_resolver)::text THEN NULL ELSE cert_not_after END,
    status_message = CASE WHEN sqlc.arg(reset_dns)::bool THEN sqlc.arg(status_message)::text ELSE status_message END,
    dns_ok = CASE WHEN sqlc.arg(reset_dns)::bool THEN NULL ELSE dns_ok END,
    dns_checked_at = CASE WHEN sqlc.arg(reset_dns)::bool THEN NULL ELSE dns_checked_at END
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: ListDomainAuthUsernames :many
SELECT username FROM domain_auth_users WHERE domain_id = $1 ORDER BY username;

-- name: ListAuthUsernamesForDomains :many
SELECT domain_id, username FROM domain_auth_users
WHERE domain_id = ANY(sqlc.arg(domain_ids)::uuid[]) ORDER BY domain_id, username;

-- name: ListDomainAuthHashes :many
SELECT domain_id, username, hash_enc FROM domain_auth_users
WHERE domain_id = ANY(sqlc.arg(domain_ids)::uuid[]) ORDER BY domain_id, username;

-- name: UpsertDomainAuthUser :exec
INSERT INTO domain_auth_users (domain_id, username, hash_enc) VALUES ($1, $2, $3)
ON CONFLICT (domain_id, username) DO UPDATE SET hash_enc = EXCLUDED.hash_enc;

-- name: DeleteDomainAuthUser :execrows
DELETE FROM domain_auth_users WHERE domain_id = $1 AND username = $2;

-- name: DeleteDomainAuthUsersExcept :exec
DELETE FROM domain_auth_users WHERE domain_id = $1 AND NOT (username = ANY(sqlc.arg(keep)::text[]));

-- name: CountDomainAuthUsers :one
SELECT count(*) FROM domain_auth_users WHERE domain_id = $1;

-- name: GetDomainCertificate :one
-- A certificate of the server that is not staged for deletion.
SELECT id, domains FROM certificates WHERE id = $1 AND server_id = $2 AND deleted_at IS NULL;

-- name: ListWorkspaceRedirectDomains :many
-- Whole-host redirects of a workspace (loop detection).
SELECT dm.id, dm.hostname, dm.redirect_to
FROM domains dm
JOIN services s ON s.id = dm.service_id AND s.deleted_at IS NULL
JOIN environments e ON e.id = s.environment_id
JOIN projects p ON p.id = e.project_id
WHERE p.workspace_id = $1 AND dm.kind = 'redirect' AND dm.path_prefix = '' AND dm.redirect_to IS NOT NULL;

-- name: GetDomainByID :one
SELECT * FROM domains WHERE id = $1;

-- name: DeleteDomain :execrows
DELETE FROM domains WHERE id = $1;

-- name: SetDomainDNS :exec
UPDATE domains
SET dns_ok = sqlc.narg(dns_ok), dns_checked_at = now(), status_message = sqlc.arg(status_message)
WHERE id = sqlc.arg(id);

-- name: SetServiceDomainsActive :exec
UPDATE domains SET status = 'active' WHERE service_id = $1 AND status <> 'active';

-- name: DeleteServiceDomains :exec
DELETE FROM domains WHERE service_id = $1;

-- name: GetDomainDNSInfo :one
SELECT sqlc.embed(d), srv.public_ip
FROM domains d
JOIN services s ON s.id = d.service_id
LEFT JOIN servers srv ON srv.id = s.server_id
WHERE d.id = $1 AND s.deleted_at IS NULL;

-- name: ListAllDomainIDs :many
SELECT d.id FROM domains d JOIN services s ON s.id = d.service_id WHERE s.deleted_at IS NULL;

-- name: DeleteServiceDomainByKey :execrows
DELETE FROM domains WHERE service_id = $1 AND hostname = $2 AND path_prefix = $3;

-- name: GetServiceDomainByKey :one
SELECT * FROM domains WHERE service_id = $1 AND hostname = $2 AND path_prefix = $3;
