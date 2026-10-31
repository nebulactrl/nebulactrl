-- name: InsertAgentCommand :one
INSERT INTO agent_commands (id, server_id, deployment_id, kind, payload, idempotency_key, expires_at)
VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (idempotency_key) DO UPDATE SET idempotency_key = EXCLUDED.idempotency_key
RETURNING *;

-- name: GetAgentCommand :one
SELECT * FROM agent_commands WHERE id = $1;

-- name: ListDeliverableAgentCommands :many
-- Commands to (re)send. Normally only never-sent commands before their expiry.
-- include_sent (used on hello) also returns rows already written to a previous
-- connection, and acked commands that have no result yet: the agent may have
-- restarted mid-command. It dedupes by command ID, ignores a command it is still
-- running, replays a stored result, and otherwise resumes the work idempotently.
-- Acked commands never expire (an acked command is the agent's to finish).
SELECT * FROM agent_commands
WHERE server_id = sqlc.arg(server_id)
  AND (
        (status = 'pending' AND expires_at > now())
     OR (sqlc.arg(include_sent)::boolean AND status = 'sent' AND expires_at > now())
     OR (sqlc.arg(include_sent)::boolean AND status = 'acked')
  )
ORDER BY created_at, id;

-- name: ListServersWithPendingCommands :many
SELECT DISTINCT server_id FROM agent_commands
WHERE status = 'pending' AND expires_at > now();

-- name: MarkAgentCommandSent :exec
UPDATE agent_commands
SET status = 'sent', sent_at = COALESCE(sent_at, now())
WHERE id = $1 AND status IN ('pending', 'sent');

-- name: MarkAgentCommandAcked :exec
-- Only the server the command belongs to can acknowledge it.
UPDATE agent_commands
SET status = 'acked', acked_at = COALESCE(acked_at, now())
WHERE id = sqlc.arg(id) AND server_id = sqlc.arg(server_id) AND status IN ('pending', 'sent');

-- name: FinishAgentCommand :one
-- Records a result. A row that already succeeded/failed is left untouched and
-- not returned, which makes duplicate results idempotent.
UPDATE agent_commands
SET status      = sqlc.arg(status),
    result      = sqlc.arg(result),
    error       = sqlc.arg(error),
    acked_at    = COALESCE(acked_at, now()),
    finished_at = now()
WHERE id = sqlc.arg(id) AND server_id = sqlc.arg(server_id) AND status NOT IN ('succeeded', 'failed')
RETURNING *;

-- name: FailUndeliveredAgentCommand :one
-- Used when a command cannot be sent (e.g. its secrets cannot be resolved).
UPDATE agent_commands
SET status = 'failed', error = $2, finished_at = now()
WHERE id = $1 AND status IN ('pending', 'sent')
RETURNING *;

-- name: ExpireAgentCommands :many
-- Only never-acknowledged commands expire. Acked commands wait for the agent.
UPDATE agent_commands
SET status = 'expired', error = 'not delivered before expiry', finished_at = now()
WHERE status IN ('pending', 'sent') AND expires_at < now()
RETURNING id, server_id, deployment_id;

-- name: CancelServerAgentCommands :many
UPDATE agent_commands
SET status = 'canceled', error = 'server revoked', finished_at = now()
WHERE server_id = $1 AND status IN ('pending', 'sent')
RETURNING id, server_id, deployment_id;

-- name: CountDeploymentLogs :one
-- Ordinary lines (build, deploy, runtime); the debug stream has its own cap.
SELECT count(*)::bigint FROM deployment_logs WHERE deployment_id = $1 AND stream <> 'debug';

-- name: CountDeploymentDebugLogs :one
SELECT count(*)::bigint FROM deployment_logs WHERE deployment_id = $1 AND stream = 'debug';
