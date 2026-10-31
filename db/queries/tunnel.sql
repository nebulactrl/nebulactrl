-- name: GetServerTunnel :one
SELECT * FROM server_tunnels WHERE server_id = $1;

-- name: ListServerTunnels :many
SELECT * FROM server_tunnels WHERE server_id = ANY(sqlc.arg(ids)::uuid[]);

-- name: ListActiveServerTunnels :many
-- Tunnels that should be running (not being removed), for the sweep.
SELECT * FROM server_tunnels WHERE state <> 'removing' ORDER BY created_at;

-- name: InsertServerTunnel :one
INSERT INTO server_tunnels (server_id, workspace_id, account_id, tunnel_id, tunnel_name)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: MarkServerTunnelRemoving :one
UPDATE server_tunnels SET state = 'removing', message = '', connections = 0, updated_at = now()
WHERE server_id = $1
RETURNING *;

-- name: SetServerTunnelRemoveError :exec
UPDATE server_tunnels SET message = $2, updated_at = now() WHERE server_id = $1 AND state = 'removing';

-- name: DeleteServerTunnel :exec
DELETE FROM server_tunnels WHERE server_id = $1;

-- name: SetServerTunnelIngress :exec
UPDATE server_tunnels
SET ingress_hash = $2, ingress_hosts = $3, ingress_synced_at = now(), ingress_error = '', updated_at = now()
WHERE server_id = $1;

-- name: SetServerTunnelIngressError :exec
UPDATE server_tunnels SET ingress_error = $2, updated_at = now() WHERE server_id = $1;

-- name: SetServerTunnelObserved :one
-- Result of a sync_tunnel command (or an undelivered one): the observed state
-- replaces the stored one; the first online observation activates the tunnel.
UPDATE server_tunnels
SET state = sqlc.arg(state)::text,
    message = sqlc.arg(message)::text,
    connections = sqlc.arg(connections)::integer,
    image = CASE WHEN sqlc.arg(image)::text <> '' THEN sqlc.arg(image)::text ELSE image END,
    observed_at = now(),
    activated_at = CASE WHEN activated_at IS NULL AND sqlc.arg(state)::text = 'online' THEN now() ELSE activated_at END,
    updated_at = now()
WHERE server_id = sqlc.arg(server_id)::uuid AND state <> 'removing'
RETURNING *;

-- name: RecordServerTunnelHeartbeat :one
-- Heartbeat path: writes only when the observed state changed or the stored one
-- is older than a minute. A missing connector (missing = true) is recorded at
-- once when it was observed before, and after three minutes otherwise (a fresh
-- enable has no container yet). Returns the previous state and activation; no
-- row = nothing written.
WITH prev AS (
    SELECT server_id, state, message, connections, activated_at, observed_at, created_at
    FROM server_tunnels WHERE server_id = sqlc.arg(server_id)::uuid AND state <> 'removing' FOR UPDATE
)
UPDATE server_tunnels t
SET state = sqlc.arg(state)::text,
    message = sqlc.arg(message)::text,
    connections = sqlc.arg(connections)::integer,
    image = CASE WHEN sqlc.arg(image)::text <> '' THEN sqlc.arg(image)::text ELSE t.image END,
    observed_at = now(),
    activated_at = CASE WHEN t.activated_at IS NULL AND sqlc.arg(state)::text = 'online' THEN now() ELSE t.activated_at END,
    updated_at = now()
FROM prev
WHERE t.server_id = prev.server_id
  AND (NOT sqlc.arg(missing)::boolean OR prev.observed_at IS NOT NULL OR prev.created_at < now() - interval '3 minutes')
  AND (prev.state IS DISTINCT FROM sqlc.arg(state)::text
       OR prev.message IS DISTINCT FROM sqlc.arg(message)::text
       OR prev.connections IS DISTINCT FROM sqlc.arg(connections)::integer
       OR prev.observed_at IS NULL OR prev.observed_at < now() - interval '1 minute')
RETURNING prev.state AS prev_state, prev.activated_at AS prev_activated_at, t.activated_at AS activated_at;

-- name: ListServerIngressHostnames :many
-- Every hostname of the live services on a server: what the tunnel's ingress rules must cover.
SELECT DISTINCT dm.hostname
FROM domains dm
JOIN services s ON s.id = dm.service_id AND s.deleted_at IS NULL
JOIN deployments d ON d.id = s.current_deployment_id
WHERE d.server_id = $1
ORDER BY dm.hostname;

-- name: ListTunnelledDomainIDs :many
-- Of the given domains, those served through the server's tunnel: the tunnel is
-- active and the domain's DNS record is one we manage in a connected zone.
SELECT dm.id
FROM domains dm
WHERE dm.id = ANY(sqlc.arg(domain_ids)::uuid[])
  AND dm.cf_zone_id <> ''
  AND EXISTS (
      SELECT 1 FROM server_tunnels st
      WHERE st.server_id = sqlc.arg(server_id)::uuid AND st.activated_at IS NOT NULL AND st.state <> 'removing'
  );

-- name: GetServerTunnelByTunnelID :one
SELECT * FROM server_tunnels WHERE tunnel_id = $1;

-- name: HasOpenTunnelCommand :one
-- A sync_tunnel or remove_tunnel command of the server that is still in flight
-- (acked commands included; one older than ten minutes no longer counts).
SELECT EXISTS (
    SELECT 1 FROM agent_commands
    WHERE server_id = $1 AND kind IN ('sync_tunnel', 'remove_tunnel')
      AND status IN ('pending', 'sent', 'acked') AND created_at > now() - interval '10 minutes'
)::boolean AS open;

-- name: SetServerTunnelSynced :exec
UPDATE server_tunnels SET connector_synced_at = now(), updated_at = now() WHERE server_id = $1;

-- name: CountWorkspaceTunnels :one
SELECT count(*) FROM server_tunnels WHERE workspace_id = $1;
