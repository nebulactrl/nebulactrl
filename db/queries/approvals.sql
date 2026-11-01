-- name: LatestApproval :one
-- The newest open approval (pending, approved or declined) of one call.
SELECT * FROM approvals
WHERE workspace_id = $1 AND principal_id = $2 AND tool = $3 AND args_digest = $4
  AND status IN ('pending', 'approved', 'declined')
ORDER BY created_at DESC, id DESC
LIMIT 1;

-- name: InsertApproval :one
INSERT INTO approvals (workspace_id, project_id, environment_id, principal_id, principal_type, principal_label,
                       client_name, tool, args_digest, plan, generations, expires_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
RETURNING *;

-- name: GetApproval :one
SELECT * FROM approvals WHERE id = $1 AND workspace_id = $2;

-- name: SetApprovalStatus :one
-- Closes an open approval without a decision (expired, stale, or a declined one
-- that was reported to the caller). Only moves out of the given status.
UPDATE approvals SET status = sqlc.arg(status)
WHERE id = sqlc.arg(id) AND status = sqlc.arg(from_status)
RETURNING *;

-- name: DecideApproval :one
-- Approves or declines a pending, unexpired approval exactly once. A declined
-- request keeps refusing the same call for one more minute, not for ten.
UPDATE approvals
SET status = sqlc.arg(status)::text, decided_by = sqlc.arg(decided_by), decided_via = sqlc.arg(decided_via), decided_at = now(),
    expires_at = CASE WHEN sqlc.arg(status)::text = 'declined' THEN LEAST(expires_at, now() + interval '1 minute') ELSE expires_at END
WHERE id = sqlc.arg(id) AND workspace_id = sqlc.arg(workspace_id) AND status = 'pending' AND expires_at > now()
RETURNING *;

-- name: UseApproval :one
-- Consumes an approved, unexpired approval exactly once.
UPDATE approvals SET status = 'used', used_at = now()
WHERE id = $1 AND status = 'approved' AND expires_at > now()
RETURNING *;

-- name: PruneApprovals :execrows
DELETE FROM approvals WHERE created_at < now() - interval '30 days';

-- name: ApprovalDecider :one
SELECT name FROM users WHERE id = $1;

-- name: ApprovalProjectNames :one
SELECT p.name AS project_name, e.name AS environment_name
FROM environments e JOIN projects p ON p.id = e.project_id
WHERE e.id = $1;

-- name: ListProductionEnvironmentIDs :many
-- The production environments among ids.
SELECT id FROM environments WHERE id = ANY(sqlc.arg(ids)::uuid[]) AND is_production;
