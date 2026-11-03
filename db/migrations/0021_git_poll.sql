-- +goose Up
-- Check for new commits: a service without a working webhook asks the remote for its branch head on a
-- schedule. No row means off. next_check_at is when the poller looks next (claimed atomically, with
-- jitter, by the sweep); head_* is what the last successful check saw; last_error is why the last one failed.
CREATE TABLE service_git_poll (
    service_id       uuid PRIMARY KEY REFERENCES services(id) ON DELETE CASCADE,
    interval_minutes integer     NOT NULL CHECK (interval_minutes IN (1, 5, 15, 60)),
    deploy           boolean     NOT NULL DEFAULT true,
    next_check_at    timestamptz NOT NULL,
    checked_at       timestamptz,
    head_sha         text,
    head_branch      text,
    last_error       text        NOT NULL DEFAULT '',
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX service_git_poll_due ON service_git_poll (next_check_at);

-- +goose Down
DROP TABLE service_git_poll;
