-- name: GetTailnetIntegration :one
SELECT * FROM tailnet_integrations WHERE workspace_id = $1;

-- name: UpsertTailnetIntegration :one
-- An empty secret_enc keeps the stored secret (update without rotating it).
INSERT INTO tailnet_integrations (workspace_id, provider, tailnet, client_id, secret_enc, default_tags, allowed_tags, login_server, headscale_user, headscale_credential, updated_at)
VALUES (sqlc.arg(workspace_id), sqlc.arg(provider), sqlc.arg(tailnet), sqlc.arg(client_id), sqlc.arg(secret_enc), sqlc.arg(default_tags), sqlc.arg(allowed_tags),
        sqlc.arg(login_server), sqlc.arg(headscale_user), sqlc.arg(headscale_credential), now())
ON CONFLICT (workspace_id) DO UPDATE
SET provider     = EXCLUDED.provider,
    tailnet      = EXCLUDED.tailnet,
    client_id    = EXCLUDED.client_id,
    secret_enc   = CASE WHEN octet_length(EXCLUDED.secret_enc) = 0 THEN tailnet_integrations.secret_enc ELSE EXCLUDED.secret_enc END,
    default_tags = EXCLUDED.default_tags,
    allowed_tags = EXCLUDED.allowed_tags,
    login_server = EXCLUDED.login_server,
    headscale_user = EXCLUDED.headscale_user,
    headscale_credential = EXCLUDED.headscale_credential,
    -- A changed credential or control server invalidates the previous test result.
    last_test_at    = CASE WHEN octet_length(EXCLUDED.secret_enc) = 0 AND tailnet_integrations.client_id = EXCLUDED.client_id AND tailnet_integrations.provider = EXCLUDED.provider AND tailnet_integrations.login_server = EXCLUDED.login_server THEN tailnet_integrations.last_test_at ELSE NULL END,
    last_test_ok    = CASE WHEN octet_length(EXCLUDED.secret_enc) = 0 AND tailnet_integrations.client_id = EXCLUDED.client_id AND tailnet_integrations.provider = EXCLUDED.provider AND tailnet_integrations.login_server = EXCLUDED.login_server THEN tailnet_integrations.last_test_ok ELSE NULL END,
    last_test_error = CASE WHEN octet_length(EXCLUDED.secret_enc) = 0 AND tailnet_integrations.client_id = EXCLUDED.client_id AND tailnet_integrations.provider = EXCLUDED.provider AND tailnet_integrations.login_server = EXCLUDED.login_server THEN tailnet_integrations.last_test_error ELSE NULL END,
    updated_at   = now()
RETURNING *;

-- name: DeleteTailnetIntegration :execrows
DELETE FROM tailnet_integrations WHERE workspace_id = $1;

-- name: RecordTailnetIntegrationTest :exec
UPDATE tailnet_integrations
SET last_test_at = now(), last_test_ok = $2, last_test_error = $3, https_enabled = COALESCE(sqlc.narg(https_enabled)::boolean, https_enabled)
WHERE workspace_id = $1;

-- name: CountManagedTailnetServers :one
-- Servers of the workspace that use (or are staged to use) a managed tailnet node.
SELECT count(*) FROM servers WHERE workspace_id = $1 AND status <> 'revoked' AND (tailnet_mode = 'managed' OR tailnet_applied_mode = 'managed');

-- name: CountTailnetServices :one
-- Live services of the workspace that have a tailnet node or want one.
SELECT count(*) FROM services s
JOIN environments e ON e.id = s.environment_id
JOIN projects p ON p.id = e.project_id
WHERE p.workspace_id = $1 AND s.deleted_at IS NULL
  AND (s.config -> 'tailnet' ->> 'enabled' = 'true' OR EXISTS (SELECT 1 FROM service_tailnet st WHERE st.service_id = s.id));

-- name: GetServiceTailnet :one
SELECT * FROM service_tailnet WHERE service_id = $1;

-- name: ListServiceTailnetForServices :many
SELECT * FROM service_tailnet WHERE service_id = ANY(sqlc.arg(ids)::uuid[]);

-- name: ListServiceTailnetForWorkspace :many
SELECT * FROM service_tailnet WHERE workspace_id = $1 ORDER BY hostname;

-- name: UpsertServiceTailnetApplied :one
-- Records what a deploy/sync applied: the configuration, the identity it was
-- provisioned with and the node state the agent reported. device_id, fqdn and
-- ips are only replaced when the agent reported them (a node that is still
-- coming up keeps what is known).
INSERT INTO service_tailnet (service_id, workspace_id, server_id, provider, hostname, identity_hash, device_id, fqdn, ips, state, message,
                             applied, applied_hide_public, funnel, needs_reprovision, provisioned_at, observed_at, updated_at)
VALUES (sqlc.arg(service_id), sqlc.arg(workspace_id), sqlc.arg(server_id), sqlc.arg(provider), sqlc.arg(hostname), sqlc.arg(identity_hash),
        sqlc.arg(device_id), sqlc.arg(fqdn), sqlc.arg(ips), sqlc.arg(state), sqlc.arg(message),
        sqlc.arg(applied), sqlc.arg(applied_hide_public), sqlc.arg(funnel), sqlc.arg(needs_reprovision),
        CASE WHEN sqlc.arg(provisioned)::boolean THEN now() END, now(), now())
ON CONFLICT (service_id) DO UPDATE
SET server_id           = EXCLUDED.server_id,
    provider            = EXCLUDED.provider,
    hostname            = EXCLUDED.hostname,
    identity_hash       = EXCLUDED.identity_hash,
    device_id           = CASE WHEN EXCLUDED.device_id <> '' THEN EXCLUDED.device_id WHEN sqlc.arg(provisioned)::boolean THEN '' ELSE service_tailnet.device_id END,
    fqdn                = CASE WHEN EXCLUDED.fqdn <> '' THEN EXCLUDED.fqdn WHEN sqlc.arg(provisioned)::boolean THEN '' ELSE service_tailnet.fqdn END,
    ips                 = CASE WHEN cardinality(EXCLUDED.ips) > 0 THEN EXCLUDED.ips WHEN sqlc.arg(provisioned)::boolean THEN '{}' ELSE service_tailnet.ips END,
    state               = EXCLUDED.state,
    message             = EXCLUDED.message,
    applied             = EXCLUDED.applied,
    applied_hide_public = EXCLUDED.applied_hide_public,
    funnel              = EXCLUDED.funnel,
    needs_reprovision   = EXCLUDED.needs_reprovision,
    provisioned_at      = CASE WHEN sqlc.arg(provisioned)::boolean THEN now() ELSE service_tailnet.provisioned_at END,
    observed_at         = now(),
    updated_at          = now()
RETURNING *;

-- name: SetServiceTailnetObserved :one
-- Heartbeat of the node: the observed state of the sidecar. A node that needs
-- attention is flagged for re-provisioning so the next deployment fixes it.
UPDATE service_tailnet
SET state             = sqlc.arg(state),
    message           = sqlc.arg(message),
    fqdn              = CASE WHEN sqlc.arg(fqdn)::text <> '' THEN sqlc.arg(fqdn)::text ELSE fqdn END,
    ips               = CASE WHEN cardinality(sqlc.arg(ips)::text[]) > 0 THEN sqlc.arg(ips)::text[] ELSE ips END,
    device_id         = CASE WHEN sqlc.arg(device_id)::text <> '' THEN sqlc.arg(device_id)::text ELSE device_id END,
    needs_reprovision = needs_reprovision OR sqlc.arg(state)::text = 'needs_attention',
    observed_at       = now(),
    updated_at        = now()
WHERE service_id = sqlc.arg(service_id) AND server_id = sqlc.arg(server_id) AND state <> 'removing'
RETURNING *;

-- name: MarkServiceTailnetReprovision :one
UPDATE service_tailnet SET needs_reprovision = true, state = 'provisioning', message = 'Re-provisioning…', updated_at = now()
WHERE service_id = $1
RETURNING *;

-- name: MarkServiceTailnetRemoving :one
UPDATE service_tailnet SET state = 'removing', message = 'Removing from the tailnet…', updated_at = now()
WHERE service_id = $1
RETURNING *;

-- name: ClearServiceTailnetDevice :exec
-- The device was deleted on the control server (before re-provisioning).
UPDATE service_tailnet SET device_id = '', fqdn = '', ips = '{}', auth_key_id = '', needs_reprovision = true, updated_at = now() WHERE service_id = $1;

-- name: SetServiceTailnetAuthKeyID :exec
UPDATE service_tailnet SET auth_key_id = $2, updated_at = now() WHERE service_id = $1;

-- name: DeleteServiceTailnet :one
DELETE FROM service_tailnet WHERE service_id = $1 RETURNING *;

-- name: ListHiddenPublicServices :many
-- Services on a server whose applied tailnet configuration is "Tailnet only":
-- their public domains are never routed.
SELECT service_id FROM service_tailnet WHERE server_id = $1 AND applied_hide_public;

-- name: ListTailnetNodeRefs :many
-- Every tailnet device NebulaCtrl created for the workspace that its tables
-- still know: service sidecars, server mesh nodes, mesh links and the managed
-- tailnet node of a server proxy, with the auth key each was registered with.
SELECT st.device_id::text AS device_id, st.ips::text[] AS ips, st.hostname::text AS hostname, st.auth_key_id::text AS auth_key_id
FROM service_tailnet st WHERE st.workspace_id = $1 AND (st.device_id <> '' OR st.auth_key_id <> '')
UNION ALL
SELECT sm.device_id, sm.ips, sm.hostname, sm.auth_key_id
FROM server_mesh sm WHERE sm.workspace_id = $1 AND (sm.device_id <> '' OR sm.auth_key_id <> '')
UNION ALL
SELECT ml.device_id, ml.ips, ml.hostname, ml.auth_key_id
FROM mesh_links ml WHERE ml.workspace_id = $1 AND (ml.device_id <> '' OR ml.auth_key_id <> '')
UNION ALL
SELECT s.tailnet_device_id, '{}'::text[], coalesce(s.tailnet_hostname, ''), ''
FROM servers s WHERE s.workspace_id = $1 AND coalesce(s.tailnet_device_id, '') <> '';
