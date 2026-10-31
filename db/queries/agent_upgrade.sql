-- Agent self-upgrade and the control plane's pre-upgrade backup.

-- name: GetInstanceUpgrade :one
SELECT * FROM instance_upgrade WHERE id;

-- name: SetAgentAutoUpgrade :one
UPDATE instance_upgrade SET agent_auto_upgrade = $1, updated_at = now() WHERE id RETURNING *;

-- name: SetInstanceLastVersion :exec
UPDATE instance_upgrade SET last_version = $1, updated_at = now() WHERE id;

-- name: RecordPreUpgradeBackup :exec
UPDATE instance_upgrade
SET pre_upgrade_at = now(),
    pre_upgrade_status = $1,
    pre_upgrade_message = $2,
    pre_upgrade_from_version = $3,
    pre_upgrade_to_version = $4,
    updated_at = now()
WHERE id;

-- name: ListServerAgentUpgrades :many
SELECT * FROM server_agent_upgrades WHERE workspace_id = $1;

-- name: GetServerAgentUpgrade :one
SELECT * FROM server_agent_upgrades WHERE server_id = $1;

-- name: GetServerAgentUpgradeByCommand :one
SELECT * FROM server_agent_upgrades WHERE command_id = $1;

-- name: UpsertServerAgentUpgrade :one
INSERT INTO server_agent_upgrades (server_id, workspace_id, state, trigger, from_version, target_version, message, command_id, requested_by)
VALUES ($1, $2, 'queued', $3, $4, $5, '', $6, $7)
ON CONFLICT (server_id) DO UPDATE
SET state = 'queued', trigger = EXCLUDED.trigger, from_version = EXCLUDED.from_version, target_version = EXCLUDED.target_version,
    message = '', command_id = EXCLUDED.command_id, requested_by = EXCLUDED.requested_by,
    requested_at = now(), finished_at = NULL, updated_at = now()
RETURNING *;

-- name: SetServerAgentUpgradeState :one
UPDATE server_agent_upgrades
SET state = sqlc.arg(state)::text,
    message = sqlc.arg(message)::text,
    finished_at = CASE WHEN sqlc.arg(state)::text IN ('succeeded','failed') THEN now() ELSE NULL END,
    updated_at = now()
WHERE server_id = sqlc.arg(server_id) AND state = ANY(sqlc.arg(from_states)::text[])
RETURNING *;
