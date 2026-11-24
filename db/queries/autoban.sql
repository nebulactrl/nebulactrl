-- name: SetServiceAutoBan :one
-- The auto-ban policy is an operational setting: it does not touch generation. NULL removes it.
UPDATE services
SET autoban = sqlc.narg(autoban),
    updated_at = now()
WHERE id = sqlc.arg(id) AND deleted_at IS NULL
RETURNING *;

-- name: InsertServiceBan :exec
-- Mirrors a ban the agent decided. The id is the agent's, so a redelivered
-- frame (and a ban that already ended) changes nothing. A ban of a service
-- that is gone, or that does not run on the reporting server, is dropped.
INSERT INTO service_bans (id, server_id, service_id, route_id, prefix, address, started_at, expires_at, strikes, rules)
SELECT sqlc.arg(id), sqlc.arg(server_id), s.id, sqlc.arg(route_id), sqlc.arg(prefix), sqlc.arg(address), sqlc.arg(started_at), sqlc.arg(expires_at), sqlc.arg(strikes), sqlc.arg(rules)
FROM services s
WHERE s.id = sqlc.arg(service_id) AND s.deleted_at IS NULL
  AND (s.server_id = sqlc.arg(server_id)
       OR EXISTS (SELECT 1 FROM deployments d WHERE d.id = s.current_deployment_id AND d.server_id = sqlc.arg(server_id)))
ON CONFLICT (id) DO NOTHING;

-- name: EndServiceBan :execrows
-- Ends an active ban of the server and forgets its address. An ended or unknown ban is left alone.
UPDATE service_bans
SET ended_at = sqlc.arg(ended_at), end_reason = sqlc.arg(end_reason), address = NULL
WHERE id = sqlc.arg(id) AND server_id = sqlc.arg(server_id) AND ended_at IS NULL;

-- name: EndServiceBansNotIn :execrows
-- Snapshot reconcile: ends the server's active bans of these services that the snapshot does not list.
UPDATE service_bans
SET ended_at = sqlc.arg(ended_at), end_reason = 'resync', address = NULL
WHERE server_id = sqlc.arg(server_id)
  AND service_id = ANY(sqlc.arg(service_ids)::uuid[])
  AND ended_at IS NULL
  AND NOT (id = ANY(sqlc.arg(keep_ids)::uuid[]));

-- name: ExpireServiceBans :execrows
-- Ends active bans that ran out more than a grace period ago without the agent
-- reporting it (an agent that never came back); the grace period covers a
-- slow end frame.
UPDATE service_bans
SET ended_at = expires_at, end_reason = 'expired', address = NULL
WHERE ended_at IS NULL AND expires_at < sqlc.arg(before);

-- name: PruneServiceBans :execrows
DELETE FROM service_bans WHERE ended_at IS NOT NULL AND ended_at < sqlc.arg(before);

-- name: GetServiceBan :one
SELECT * FROM service_bans WHERE id = sqlc.arg(id) AND service_id = sqlc.arg(service_id);

-- name: CountActiveServiceBans :one
SELECT count(*) FROM service_bans WHERE service_id = sqlc.arg(service_id) AND ended_at IS NULL;

-- name: ListServiceBans :many
-- One page of a service's bans, newest first. Active: no ended_at. Ended: has one.
SELECT * FROM service_bans
WHERE service_id = sqlc.arg(service_id)
  AND (ended_at IS NULL) = sqlc.arg(active)::bool
  AND (NOT sqlc.arg(after)::bool OR (started_at, id) < (sqlc.arg(after_started_at)::timestamptz, sqlc.arg(after_id)::uuid))
ORDER BY started_at DESC, id DESC
LIMIT sqlc.arg(page_limit);

-- name: ListUnnotifiedServiceBans :many
-- Bans not yet told to anyone, with their service and project for the digest.
SELECT b.id, b.service_id, b.rules, b.created_at, s.name AS service_name, s.firewall, e.project_id
FROM service_bans b
JOIN services s ON s.id = b.service_id AND s.deleted_at IS NULL
JOIN environments e ON e.id = s.environment_id
WHERE b.notified_at IS NULL AND b.created_at < sqlc.arg(before)
ORDER BY b.created_at, b.id;

-- name: MarkServiceBansNotified :exec
UPDATE service_bans SET notified_at = now() WHERE id = ANY(sqlc.arg(ids)::uuid[]);

-- name: ClearNotifiedServiceBans :execrows
-- Bans of a service that is gone or that were never worth telling: nothing to
-- notify, so they stop showing up as pending.
UPDATE service_bans SET notified_at = now()
WHERE service_bans.notified_at IS NULL AND service_bans.created_at < sqlc.arg(before)
  AND NOT EXISTS (SELECT 1 FROM services s WHERE s.id = service_bans.service_id AND s.deleted_at IS NULL);
