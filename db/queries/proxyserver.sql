-- name: LockServerForProxy :one
-- Serialises proxy settings edits and "Deploy proxy" per server.
SELECT * FROM servers WHERE id = $1 AND workspace_id = $2 FOR UPDATE;

-- name: UpdateServerUpstreamPolicy :exec
UPDATE servers SET upstream_policy = $2, proxy_generation = proxy_generation + 1 WHERE id = $1;

-- name: UpdateServerTailnetSettings :exec
UPDATE servers
SET tailnet_mode = $2, tailnet_hostname = $3, tailnet_magic_dns = $4, proxy_generation = proxy_generation + 1
WHERE id = $1;

-- name: UpdateServerProxyPorts :exec
-- Stages the proxy port override (NULL = the agent's own ports).
UPDATE servers
SET proxy_http_port = sqlc.narg(http_port)::int, proxy_https_port = sqlc.narg(https_port)::int, proxy_generation = proxy_generation + 1
WHERE id = sqlc.arg(id)::uuid;

-- name: BumpServerProxyGeneration :exec
UPDATE servers SET proxy_generation = proxy_generation + 1 WHERE id = $1;

-- name: ApplyServerProxyResult :exec
-- Copies the settings a successful proxy_configure carried into the applied
-- columns. applied_generation only advances (a newer deploy may have finished first).
UPDATE servers
SET proxy_applied_generation = GREATEST(proxy_applied_generation, sqlc.arg(generation)::bigint),
    applied_upstream_policy  = sqlc.arg(upstream_policy)::jsonb,
    tailnet_applied_mode     = sqlc.arg(tailnet_mode)::text,
    tailnet_applied_hostname = sqlc.narg(tailnet_hostname)::text,
    tailnet_applied_magic_dns = sqlc.arg(tailnet_magic_dns)::boolean,
    proxy_applied_http_port  = sqlc.narg(http_port)::int,
    proxy_applied_https_port = sqlc.narg(https_port)::int,
    tailnet_device_id        = CASE WHEN sqlc.arg(clear_device)::boolean THEN NULL
                                    ELSE COALESCE(sqlc.narg(device_id)::text, tailnet_device_id) END
WHERE id = sqlc.arg(id)::uuid;

-- name: SetServerTailnetStatus :exec
-- Unconditional write (proxy_configure result). A NULL status clears it.
UPDATE servers SET tailnet_status = sqlc.narg(status)::jsonb, tailnet_status_at = now() WHERE id = sqlc.arg(id)::uuid;

-- name: RecordServerTailnetHeartbeat :one
-- Heartbeat path: writes only when the observed status changed or is older than
-- five minutes, so a steady tailnet costs one UPDATE per five minutes instead of
-- one per heartbeat. Returns the previous status; no row = nothing written.
WITH prev AS (SELECT id, tailnet_status, tailnet_status_at FROM servers WHERE id = sqlc.arg(id)::uuid FOR UPDATE)
UPDATE servers s
SET tailnet_status    = sqlc.narg(status)::jsonb,
    tailnet_status_at = now(),
    tailnet_device_id = CASE WHEN s.tailnet_applied_mode = 'managed' AND sqlc.arg(device_id)::text <> ''
                             THEN sqlc.arg(device_id)::text ELSE s.tailnet_device_id END
FROM prev
WHERE s.id = prev.id
  AND (prev.tailnet_status IS DISTINCT FROM sqlc.narg(status)::jsonb
       OR prev.tailnet_status_at IS NULL OR prev.tailnet_status_at < now() - interval '5 minutes')
RETURNING prev.tailnet_status AS prev_status;

-- name: ListServerL4Ports :many
-- TCP/UDP route ports reserved on a server; deployed = the route service has a current deployment.
SELECT rp.service_id, rp.protocol, rp.listen_port, sv.name AS service_name,
       (sv.current_deployment_id IS NOT NULL)::boolean AS deployed
FROM route_ports rp
JOIN services sv ON sv.id = rp.service_id AND sv.deleted_at IS NULL
WHERE rp.server_id = $1
ORDER BY rp.protocol, rp.listen_port;

-- name: LastProxyConfigureCommand :one
SELECT id, status, error, created_at, finished_at, result FROM agent_commands
WHERE server_id = $1 AND kind = 'proxy_configure' ORDER BY created_at DESC, id DESC LIMIT 1;

-- name: ProxyConfigureInFlight :one
SELECT EXISTS (
    SELECT 1 FROM agent_commands
    WHERE server_id = $1 AND kind = 'proxy_configure'
      AND (status IN ('pending','sent','acked') AND (status = 'acked' OR expires_at > now()))
);

-- name: ListServiceNamesByIDs :many
SELECT id, name FROM services WHERE id = ANY(sqlc.arg(ids)::uuid[]);

-- name: RecordServerProxyPorts :execrows
-- Heartbeat path: stores the ports the proxy reports (facts.proxyPorts). Writes
-- only when they changed; 0 rows = nothing written.
UPDATE servers
SET facts = jsonb_set(COALESCE(facts, '{}'::jsonb), '{proxyPorts}', sqlc.arg(ports)::jsonb)
WHERE id = sqlc.arg(id)::uuid AND status <> 'revoked'
  AND COALESCE(facts, '{}'::jsonb) -> 'proxyPorts' IS DISTINCT FROM sqlc.arg(ports)::jsonb;

-- name: ListServerHTTP01Context :many
-- What decides whether HTTP-01 can work on a server: its ACME contact, the
-- applied HTTP port override and the HTTP port the proxy reports.
SELECT id, acme_email, proxy_applied_http_port,
       COALESCE(NULLIF(facts #>> '{proxyPorts,http}', '')::int, 0)::int AS reported_http_port
FROM servers WHERE id = ANY(sqlc.arg(ids)::uuid[]);

-- name: ListServerPlacedDomains :many
-- Domains of the live services placed on a server (deployed or not): what its proxy will serve.
SELECT dm.* FROM domains dm
JOIN services s ON s.id = dm.service_id AND s.deleted_at IS NULL
WHERE s.server_id = $1
ORDER BY dm.hostname, dm.id;
