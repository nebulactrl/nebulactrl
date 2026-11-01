-- name: GetServiceSSHKey :one
SELECT * FROM service_ssh_keys WHERE service_id = $1;

-- name: UpsertServiceSSHKey :one
-- Sets the service's deploy key. rotated_at is set when a key is replaced.
INSERT INTO service_ssh_keys (service_id, private_key_enc, public_key, fingerprint, source)
VALUES (sqlc.arg(service_id), sqlc.arg(private_key_enc), sqlc.arg(public_key), sqlc.arg(fingerprint), sqlc.arg(source))
ON CONFLICT (service_id) DO UPDATE SET
    private_key_enc = EXCLUDED.private_key_enc, public_key = EXCLUDED.public_key,
    fingerprint = EXCLUDED.fingerprint, source = EXCLUDED.source, rotated_at = now()
RETURNING *;

-- name: DeleteServiceSSHKey :execrows
DELETE FROM service_ssh_keys WHERE service_id = $1;

-- name: ListServiceSSHHosts :many
SELECT * FROM service_ssh_hosts WHERE service_id = $1 ORDER BY host;

-- name: GetServiceSSHHost :one
SELECT * FROM service_ssh_hosts WHERE service_id = $1 AND host = $2;

-- name: PinServiceSSHHost :one
-- Records a host key. Pinning an existing host replaces its key and clears the
-- recorded mismatch (the "Accept new host key" action).
INSERT INTO service_ssh_hosts (service_id, host, key_type, public_key, fingerprint, accepted_by)
VALUES (sqlc.arg(service_id), sqlc.arg(host), sqlc.arg(key_type), sqlc.arg(public_key), sqlc.arg(fingerprint), sqlc.narg(accepted_by))
ON CONFLICT (service_id, host) DO UPDATE SET
    key_type = EXCLUDED.key_type, public_key = EXCLUDED.public_key, fingerprint = EXCLUDED.fingerprint,
    first_seen_at = now(), accepted_by = EXCLUDED.accepted_by,
    mismatch_fingerprint = NULL, mismatch_at = NULL
RETURNING *;

-- name: PinServiceSSHHostIfAbsent :execrows
-- Trust on first use; never replaces an existing pin.
INSERT INTO service_ssh_hosts (service_id, host, key_type, public_key, fingerprint)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (service_id, host) DO NOTHING;

-- name: RecordServiceSSHHostMismatch :exec
UPDATE service_ssh_hosts SET mismatch_fingerprint = $3, mismatch_at = now()
WHERE service_id = $1 AND host = $2;

-- name: DeleteServiceSSHHosts :exec
DELETE FROM service_ssh_hosts WHERE service_id = $1;

-- name: InsertServiceSSHKeyIfAbsent :exec
-- Gives a preview service its own copy of the deploy key of the service it was
-- cloned from. The caller re-seals the key for the new service.
INSERT INTO service_ssh_keys (service_id, private_key_enc, public_key, fingerprint, source)
VALUES (sqlc.arg(service_id), sqlc.arg(private_key_enc), sqlc.arg(public_key), sqlc.arg(fingerprint), sqlc.arg(source))
ON CONFLICT (service_id) DO NOTHING;

-- name: CopyServiceSSHHosts :exec
INSERT INTO service_ssh_hosts (service_id, host, key_type, public_key, fingerprint)
SELECT sqlc.arg(to_service_id)::uuid, h.host, h.key_type, h.public_key, h.fingerprint
FROM service_ssh_hosts h WHERE h.service_id = sqlc.arg(from_service_id)::uuid
ON CONFLICT (service_id, host) DO NOTHING;
