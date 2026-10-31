-- The control plane's check for a newer release and the one-click update
-- (internal/sysupdate).

-- name: GetInstanceUpdate :one
SELECT * FROM instance_update WHERE id;

-- name: RecordUpdateFeed :one
-- A successful fetch of the release feed.
UPDATE instance_update
SET latest_version = sqlc.arg(latest_version)::text,
    latest_published_at = sqlc.narg(published_at)::timestamptz,
    latest_notes = sqlc.arg(notes)::text,
    latest_notes_url = sqlc.arg(notes_url)::text,
    latest_min_agent = sqlc.arg(min_agent)::text,
    checked_at = now(),
    check_error = '',
    updated_at = now()
WHERE id
RETURNING *;

-- name: RecordUpdateFeedError :exec
-- A failed fetch: the last known release stays.
UPDATE instance_update SET check_error = $1, updated_at = now() WHERE id;

-- name: BeginInstanceUpdate :one
-- Claims the single update slot. No row: an update is running (one that has run
-- since before stale_before is treated as lost).
UPDATE instance_update
SET state = 'running',
    from_version = sqlc.arg(from_version)::text,
    target_version = sqlc.arg(target_version)::text,
    started_at = now(),
    finished_at = NULL,
    error = '',
    command_id = NULL,
    requested_by = sqlc.narg(requested_by)::uuid,
    updated_at = now()
WHERE id AND (state <> 'running' OR started_at < sqlc.arg(stale_before)::timestamptz)
RETURNING *;

-- name: SetInstanceUpdateCommand :exec
UPDATE instance_update SET command_id = $1, updated_at = now() WHERE id AND state = 'running';

-- name: FinishInstanceUpdate :one
-- Moves the update out of one of from_states, once.
UPDATE instance_update
SET state = sqlc.arg(state)::text,
    error = sqlc.arg(error)::text,
    finished_at = now(),
    updated_at = now()
WHERE id AND state = ANY(sqlc.arg(from_states)::text[])
RETURNING *;
