-- Check for new commits (service_git_poll). No row means off.

-- name: UpsertGitPoll :one
-- Turns polling on or changes it; the first check runs at next_check_at.
INSERT INTO service_git_poll (service_id, interval_minutes, deploy, next_check_at)
VALUES (sqlc.arg(service_id), sqlc.arg(interval_minutes), sqlc.arg(deploy), sqlc.arg(next_check_at))
ON CONFLICT (service_id) DO UPDATE SET
    interval_minutes = EXCLUDED.interval_minutes,
    deploy = EXCLUDED.deploy,
    next_check_at = EXCLUDED.next_check_at,
    updated_at = now()
RETURNING *;

-- name: DeleteGitPoll :exec
DELETE FROM service_git_poll WHERE service_id = $1;

-- name: GetGitPoll :one
SELECT * FROM service_git_poll WHERE service_id = $1;

-- name: ListGitPollsForServices :many
SELECT * FROM service_git_poll WHERE service_id = ANY(sqlc.arg(ids)::uuid[]);

-- name: ClaimDueGitPolls :many
-- Takes the polls that are due and schedules their next check one interval (10 % jitter) after now,
-- so two sweeps never check the same service. unit_seconds is the length of one interval minute (60).
UPDATE service_git_poll AS p
SET next_check_at = sqlc.arg(now)::timestamptz
                    + make_interval(secs => p.interval_minutes * sqlc.arg(unit_seconds)::float8 * (0.9 + random() * 0.2))
WHERE p.service_id IN (
    SELECT g.service_id FROM service_git_poll g
    JOIN services s ON s.id = g.service_id AND s.deleted_at IS NULL
    WHERE g.next_check_at <= sqlc.arg(now)::timestamptz
    ORDER BY g.next_check_at
    LIMIT sqlc.arg(row_limit)
    FOR UPDATE OF g SKIP LOCKED
)
RETURNING p.*;

-- name: SetGitPollResult :exec
-- Records a check. A failed check passes no head and keeps the last one seen.
UPDATE service_git_poll SET
    checked_at = sqlc.arg(checked_at),
    head_sha = COALESCE(sqlc.narg(head_sha)::text, head_sha),
    head_branch = COALESCE(sqlc.narg(head_branch)::text, head_branch),
    last_error = sqlc.arg(last_error),
    updated_at = now()
WHERE service_id = sqlc.arg(service_id);

-- name: GitPollCommitHandled :one
-- Whether the commit already has a deployment after the one the service runs (active, queued, failed or
-- succeeded: a failed commit is never retried, and a rollback is not undone), is waiting to deploy, or was
-- refused by wait-for-CI (while the service still waits for CI).
SELECT (
    EXISTS (
        SELECT 1 FROM deployments d
        WHERE d.service_id = sqlc.arg(service_id) AND d.commit_sha = sqlc.arg(sha)::text
          AND d.created_at >= COALESCE(
              (SELECT r.created_at FROM services s
               JOIN deployments cd ON cd.id = s.current_deployment_id
               JOIN releases r ON r.id = cd.release_id
               WHERE s.id = sqlc.arg(service_id)),
              '-infinity'::timestamptz)
    )
    OR EXISTS (
        SELECT 1 FROM service_git g
        WHERE g.service_id = sqlc.arg(service_id)
          AND (g.pending_commit->>'sha' = sqlc.arg(sha)::text OR g.ci_wait->>'sha' = sqlc.arg(sha)::text
               OR (g.wait_for_ci AND g.ci_blocked->>'sha' = sqlc.arg(sha)::text))
    )
)::boolean AS handled;

-- name: RetryGitPollSoon :exec
-- Brings the next check forward (a commit found while a deployment was running is looked at again soon).
UPDATE service_git_poll SET next_check_at = LEAST(next_check_at, sqlc.arg(at)::timestamptz)
WHERE service_id = sqlc.arg(service_id);
