-- name: InsertDNSCredential :one
INSERT INTO dns_credentials (workspace_id, server_id, name, provider, secrets_enc, env_names, id)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING *;

-- name: GetDNSCredential :one
SELECT * FROM dns_credentials WHERE id = $1 AND workspace_id = $2;

-- name: GetDNSCredentialForServer :one
-- A credential the server may use: of the server's team, team-wide or scoped to this server.
SELECT c.* FROM dns_credentials c
JOIN servers sv ON sv.id = sqlc.arg(server_id)::uuid AND sv.workspace_id = c.workspace_id
WHERE c.id = sqlc.arg(id)::uuid AND (c.server_id IS NULL OR c.server_id = sv.id);

-- name: ListDNSCredentials :many
-- The team's credentials: team-wide ones plus, when server_id is given, the ones scoped to that server;
-- without server_id every credential of the team.
SELECT * FROM dns_credentials
WHERE workspace_id = sqlc.arg(workspace_id)
  AND (sqlc.narg(server_id)::uuid IS NULL OR server_id IS NULL OR server_id = sqlc.narg(server_id)::uuid)
ORDER BY name, id;

-- name: UpdateDNSCredential :one
-- A NULL secrets_enc keeps the values (rename only); changed values advance rev.
UPDATE dns_credentials
SET name        = sqlc.arg(name),
    secrets_enc = COALESCE(sqlc.narg(secrets_enc)::bytea, secrets_enc),
    env_names   = COALESCE(sqlc.narg(env_names)::text[], env_names),
    rev         = CASE WHEN sqlc.narg(secrets_enc)::bytea IS NULL THEN rev ELSE rev + 1 END,
    updated_at  = now()
WHERE id = sqlc.arg(id) AND workspace_id = sqlc.arg(workspace_id)
RETURNING *;

-- name: DeleteDNSCredential :execrows
DELETE FROM dns_credentials WHERE id = $1 AND workspace_id = $2;

-- name: DNSCredentialUsage :many
-- Servers whose desired resolvers use a credential of the team.
SELECT r.credential_id, r.server_id, s.name AS server_name
FROM server_dns_resolvers r
JOIN servers s ON s.id = r.server_id
WHERE s.workspace_id = $1
ORDER BY s.name;

-- name: DNSCredentialAppliedOn :many
-- Servers whose applied configuration still references a credential.
SELECT id, name FROM servers
WHERE workspace_id = sqlc.arg(workspace_id)
  AND applied_dns01 -> 'resolvers' @> jsonb_build_array(jsonb_build_object('id', sqlc.arg(credential_id)::text));

-- name: ListServerDNSResolvers :many
SELECT r.server_id, r.provider, r.credential_id, r.delay_seconds, r.staging,
       c.name AS credential_name, c.server_id AS credential_server_id, c.env_names, c.rev
FROM server_dns_resolvers r
JOIN dns_credentials c ON c.id = r.credential_id
WHERE r.server_id = $1
ORDER BY r.provider;

-- name: UpsertServerDNSResolver :exec
INSERT INTO server_dns_resolvers (server_id, provider, credential_id, delay_seconds, staging)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (server_id, provider) DO UPDATE
SET credential_id = EXCLUDED.credential_id, delay_seconds = EXCLUDED.delay_seconds, staging = EXCLUDED.staging;

-- name: DeleteServerDNSResolver :execrows
DELETE FROM server_dns_resolvers WHERE server_id = $1 AND provider = $2;

-- name: CountServerWildcardsForProvider :one
SELECT count(*) FROM wildcard_certs WHERE server_id = $1 AND provider = $2;

-- name: CountServerDomainsForProvider :one
-- Live domains of services placed on the server that name the provider.
SELECT count(*) FROM domains d
JOIN services s ON s.id = d.service_id AND s.deleted_at IS NULL
WHERE s.server_id = sqlc.arg(server_id)::uuid AND d.dns_resolver = sqlc.arg(dns_resolver)::text;

-- name: InsertWildcardCert :one
INSERT INTO wildcard_certs (workspace_id, server_id, domain, include_apex, provider, cover_generated, is_default)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING *;

-- name: GetWildcardCert :one
SELECT * FROM wildcard_certs WHERE id = $1 AND workspace_id = $2;

-- name: ListServerWildcardCerts :many
SELECT * FROM wildcard_certs WHERE server_id = $1 ORDER BY domain, id;

-- name: ListWildcardCertsForServers :many
SELECT * FROM wildcard_certs WHERE server_id = ANY(sqlc.arg(server_ids)::uuid[]) ORDER BY domain, id;

-- name: UpdateWildcardCert :one
-- A change of domain or provider forgets the old observation.
UPDATE wildcard_certs
SET include_apex    = sqlc.arg(include_apex),
    provider        = sqlc.arg(provider),
    cover_generated = sqlc.arg(cover_generated),
    is_default      = sqlc.arg(is_default),
    status          = CASE WHEN provider = sqlc.arg(provider) THEN status ELSE 'pending' END,
    status_message  = CASE WHEN provider = sqlc.arg(provider) THEN status_message ELSE '' END,
    issuer          = CASE WHEN provider = sqlc.arg(provider) THEN issuer ELSE '' END,
    not_after       = CASE WHEN provider = sqlc.arg(provider) THEN not_after ELSE NULL END
WHERE id = sqlc.arg(id) AND workspace_id = sqlc.arg(workspace_id)
RETURNING *;

-- name: ClearDefaultWildcardCert :exec
UPDATE wildcard_certs SET is_default = false WHERE server_id = $1 AND id <> $2 AND is_default;

-- name: DeleteWildcardCert :execrows
DELETE FROM wildcard_certs WHERE id = $1 AND workspace_id = $2;

-- name: ObserveWildcardCert :one
-- The agent's observation (heartbeat). Writes only on a change or when the stored
-- one is older than five minutes; no row = nothing written.
UPDATE wildcard_certs
SET status = sqlc.arg(status), status_message = sqlc.arg(status_message), issuer = sqlc.arg(issuer),
    not_after = sqlc.narg(not_after)::timestamptz, checked_at = now()
WHERE id = sqlc.arg(id)::uuid AND server_id = sqlc.arg(server_id)::uuid
  AND (status IS DISTINCT FROM sqlc.arg(status) OR status_message IS DISTINCT FROM sqlc.arg(status_message)
       OR issuer IS DISTINCT FROM sqlc.arg(issuer) OR not_after IS DISTINCT FROM sqlc.narg(not_after)::timestamptz
       OR checked_at IS NULL OR checked_at < now() - interval '5 minutes')
RETURNING id;

-- name: ObserveDomainCert :one
UPDATE domains d
SET cert_status = sqlc.arg(status), cert_message = sqlc.arg(status_message), cert_issuer = sqlc.arg(issuer),
    cert_not_after = sqlc.narg(not_after)::timestamptz, cert_checked_at = now()
FROM services s
WHERE d.id = sqlc.arg(id)::uuid AND s.id = d.service_id AND s.server_id = sqlc.arg(server_id)::uuid
  AND (d.cert_status IS DISTINCT FROM sqlc.arg(status) OR d.cert_message IS DISTINCT FROM sqlc.arg(status_message)
       OR d.cert_issuer IS DISTINCT FROM sqlc.arg(issuer) OR d.cert_not_after IS DISTINCT FROM sqlc.narg(not_after)::timestamptz
       OR d.cert_checked_at IS NULL OR d.cert_checked_at < now() - interval '5 minutes')
RETURNING d.id;

-- name: ListDomainServers :many
-- The server each of the given domains' services is placed on (NULL: none).
SELECT d.id AS domain_id, s.server_id
FROM domains d JOIN services s ON s.id = d.service_id
WHERE d.id = ANY(sqlc.arg(domain_ids)::uuid[]);

-- name: SetServerAppliedDNS01 :exec
UPDATE servers SET applied_dns01 = $2 WHERE id = $1;
