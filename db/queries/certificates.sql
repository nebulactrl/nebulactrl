-- name: ListAppliedCertificates :many
SELECT * FROM certificates
WHERE server_id = $1 AND applied_at IS NOT NULL AND deleted_at IS NULL
ORDER BY name, id;

-- name: GetCertificateKey :one
SELECT key_enc FROM certificates WHERE id = $1 AND deleted_at IS NULL;

-- name: InsertCertificate :one
INSERT INTO certificates (workspace_id, server_id, name, domains, cert_pem, key_enc, fingerprint, issuer, not_before, not_after, id)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
RETURNING *;

-- name: ListServerCertificates :many
-- Every certificate row of a server, including staged removals (deleted_at set).
SELECT * FROM certificates WHERE server_id = $1 ORDER BY name, id;

-- name: ListDesiredCertificates :many
-- The DESIRED certificate set of a server: staged uploads included, staged removals excluded.
SELECT * FROM certificates WHERE server_id = $1 AND deleted_at IS NULL ORDER BY name, id;

-- name: GetCertificateScoped :one
SELECT * FROM certificates WHERE id = $1 AND workspace_id = $2;

-- name: CountDomainsPinningCertificate :one
SELECT count(*) FROM domains WHERE certificate_id = $1;

-- name: SoftDeleteCertificate :exec
UPDATE certificates SET deleted_at = now() WHERE id = $1 AND deleted_at IS NULL;

-- name: HardDeleteCertificate :exec
DELETE FROM certificates WHERE id = $1;

-- name: MarkCertificatesApplied :exec
-- Marks the given staged uploads applied (status active unless a later result reports an error).
UPDATE certificates SET applied_at = now(), status = 'active', status_message = ''
WHERE server_id = $1 AND id = ANY(sqlc.arg(ids)::uuid[]) AND applied_at IS NULL AND deleted_at IS NULL;

-- name: PurgeRemovedCertificates :exec
-- Hard-deletes staged removals that were not part of the applied set.
DELETE FROM certificates WHERE server_id = $1 AND deleted_at IS NOT NULL AND NOT (id = ANY(sqlc.arg(keep_ids)::uuid[]));

-- name: SetCertificateStatus :exec
UPDATE certificates SET status = $2, status_message = $3 WHERE id = $1;

-- name: ListCertificatesForExpiry :many
-- Live (not staged-for-removal) certificates with their workspace, for CertExpiryJob.
SELECT id, workspace_id, server_id, name, not_after, status, status_message, applied_at
FROM certificates WHERE deleted_at IS NULL ORDER BY not_after;

-- name: CertificateExpiryAuditExists :one
SELECT EXISTS (
    SELECT 1 FROM audit_events
    WHERE action = 'certificate.expiring' AND resource_id = sqlc.arg(resource_id)::text
      AND metadata->>'thresholdDays' = sqlc.arg(threshold_days)::text
);
