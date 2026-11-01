-- Database replica queries.

-- name: GetDatabaseReplica :one
SELECT * FROM database_replicas WHERE service_id = $1;

-- name: ListDatabaseReplicasOfPrimary :many
SELECT * FROM database_replicas WHERE primary_service_id = $1 ORDER BY created_at, service_id;

-- name: ListDatabaseReplicasOfEnvironment :many
SELECT * FROM database_replicas WHERE environment_id = $1 ORDER BY created_at, service_id;

-- name: InsertDatabaseReplica :one
INSERT INTO database_replicas (service_id, workspace_id, environment_id, primary_service_id, slot_name, repl_user, link_id, exposure_id, created_by)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
RETURNING *;

-- name: SetDatabaseReplicaState :exec
UPDATE database_replicas SET state = $2, message = $3, step = $4, updated_at = now() WHERE service_id = $1;

-- name: DeleteDatabaseReplica :execrows
DELETE FROM database_replicas WHERE service_id = $1;

-- name: GetRunningDatabasePromotion :one
SELECT * FROM database_promotions WHERE replica_service_id = $1 AND status = 'running';

-- name: GetDatabasePromotion :one
SELECT * FROM database_promotions WHERE id = $1;

-- name: InsertDatabasePromotion :one
INSERT INTO database_promotions (workspace_id, environment_id, replica_service_id, old_primary_service_id, mode, accept_data_loss, actor_label)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING *;

-- name: GetDatabaseFence :one
SELECT * FROM database_fences WHERE service_id = $1;

-- name: ListDatabaseFencesOnServer :many
SELECT f.* FROM database_fences f JOIN services s ON s.id = f.service_id WHERE s.server_id = $1;

-- name: InsertDatabaseFence :exec
INSERT INTO database_fences (service_id, promotion_id, reason) VALUES ($1, $2, $3)
ON CONFLICT (service_id) DO NOTHING;

-- name: DeleteDatabaseFence :execrows
DELETE FROM database_fences WHERE service_id = $1;

-- name: LockDatabaseReplica :one
SELECT * FROM database_replicas WHERE service_id = $1 FOR UPDATE;

-- name: SetDatabaseReplicaLinks :exec
UPDATE database_replicas SET link_id = $2, exposure_id = $3, updated_at = now() WHERE service_id = $1;

-- name: SetDatabaseReplicaClone :exec
UPDATE database_replicas SET clone_command_id = $2, updated_at = now() WHERE service_id = $1;

-- name: SetDatabaseReplicaPrimary :exec
UPDATE database_replicas
SET primary_service_id = $2, state = $3, message = $4, step = $5, clone_command_id = NULL, link_id = NULL, exposure_id = NULL, updated_at = now()
WHERE service_id = $1;

-- name: ObserveDatabaseReplica :exec
-- Merges one side's observation (primary or replica): a side that does not know a
-- value leaves it. updated_at is the saga clock and is not touched.
UPDATE database_replicas
SET receive_lsn = COALESCE(sqlc.narg(receive_lsn), receive_lsn), replay_lsn = COALESCE(sqlc.narg(replay_lsn), replay_lsn),
    lag_bytes = COALESCE(sqlc.narg(lag_bytes), lag_bytes), replay_lag_ms = COALESCE(sqlc.narg(replay_lag_ms), replay_lag_ms),
    wal_status = COALESCE(sqlc.narg(wal_status), wal_status), retained_bytes = COALESCE(sqlc.narg(retained_bytes), retained_bytes),
    observed_at = sqlc.arg(observed_at),
    last_streaming_at = CASE WHEN sqlc.arg(healthy)::boolean THEN sqlc.arg(observed_at) ELSE last_streaming_at END
WHERE service_id = sqlc.arg(service_id);

-- name: ListDatabaseReplicasObservable :many
-- Replicas whose state follows the observations.
SELECT * FROM database_replicas WHERE state IN ('deploying', 'streaming', 'lagging', 'broken');

-- name: ListDatabaseReplicasInSaga :many
SELECT * FROM database_replicas WHERE state IN ('pending', 'preparing', 'cloning', 'deploying', 'removing');

-- name: ListDatabaseReplicasOnServer :many
SELECT r.* FROM database_replicas r JOIN services s ON s.id = r.service_id WHERE s.server_id = $1 AND s.deleted_at IS NULL;

-- name: ListDatabaseReplicasOfPrimariesOnServer :many
SELECT r.* FROM database_replicas r JOIN services p ON p.id = r.primary_service_id WHERE p.server_id = $1 AND p.deleted_at IS NULL;

-- name: CountDatabaseReplicasOfPrimary :one
SELECT count(*)::int FROM database_replicas WHERE primary_service_id = $1;

-- name: ListDatabaseReplicasWithServers :many
-- A primary's replicas with their service rows, for views and group data.
SELECT sqlc.embed(r), s.slug AS slug, s.name AS name, s.server_id AS server_id, s.desired_state AS desired_state
FROM database_replicas r JOIN services s ON s.id = r.service_id
WHERE r.primary_service_id = $1 AND s.deleted_at IS NULL
ORDER BY r.created_at, r.service_id;

-- name: ListEnvironmentDatabaseFences :many
SELECT f.* FROM database_fences f JOIN services s ON s.id = f.service_id WHERE s.environment_id = $1 AND s.deleted_at IS NULL;

-- name: ListRunningDatabasePromotions :many
SELECT * FROM database_promotions WHERE status = 'running' ORDER BY started_at;

-- name: GetLatestDatabasePromotionOfService :one
SELECT * FROM database_promotions
WHERE replica_service_id = $1 OR old_primary_service_id = $1
ORDER BY started_at DESC LIMIT 1;

-- name: UpdateDatabasePromotionStep :exec
UPDATE database_promotions
SET step = sqlc.arg(step), message = sqlc.arg(message), freeze_lsn = COALESCE(sqlc.narg(freeze_lsn), freeze_lsn)
WHERE id = sqlc.arg(id) AND status = 'running';

-- name: FinishDatabasePromotion :one
UPDATE database_promotions SET status = $2, message = $3, finished_at = now()
WHERE id = $1 AND status = 'running'
RETURNING *;

-- name: MoveBackupSchedules :execrows
UPDATE backup_schedules SET service_id = $2, updated_at = now() WHERE service_id = $1;

-- name: CountBackupSchedulesOfService :one
SELECT count(*)::int FROM backup_schedules WHERE service_id = $1;

-- name: GetAgentCommandByKey :one
-- Saga steps look their command up by the key they enqueued it with.
SELECT * FROM agent_commands WHERE idempotency_key = $1;

-- name: GetLatestServiceDeployment :one
SELECT * FROM deployments WHERE service_id = $1 ORDER BY created_at DESC, id DESC LIMIT 1;

-- name: ListServiceVariableKeys :many
SELECT key FROM service_variables WHERE service_id = $1 ORDER BY key;
