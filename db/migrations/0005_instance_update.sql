-- +goose Up
-- The control plane's check for a newer release and the one-click update
-- (internal/sysupdate). One row of instance-wide state.
--   latest_*, checked_at   the newest release the feed announced and when it
--                          was fetched (a restart does not refetch before it is due)
--   check_error            why the last fetch failed ('' after a success)
--   state                  '' (never updated), running, succeeded or failed: the
--                          update the instance admin started. running blocks a
--                          second one; the control plane that starts after it
--                          settles it (version == target_version: succeeded,
--                          anything else: failed, the installer rolled back).
--   from_version, target_version, started_at, finished_at, error  that update
--   command_id             the update_control_plane command sent to the local agent
--   requested_by           the instance admin who started it
CREATE TABLE instance_update (
    id                  boolean PRIMARY KEY DEFAULT true CHECK (id),
    latest_version      text NOT NULL DEFAULT '',
    latest_published_at timestamptz,
    latest_notes        text NOT NULL DEFAULT '',
    latest_notes_url    text NOT NULL DEFAULT '',
    latest_min_agent    text NOT NULL DEFAULT '',
    checked_at          timestamptz,
    check_error         text NOT NULL DEFAULT '',
    state               text NOT NULL DEFAULT '' CHECK (state IN ('', 'running', 'succeeded', 'failed')),
    from_version        text NOT NULL DEFAULT '',
    target_version      text NOT NULL DEFAULT '',
    started_at          timestamptz,
    finished_at         timestamptz,
    error               text NOT NULL DEFAULT '',
    command_id          uuid,
    requested_by        uuid REFERENCES users (id) ON DELETE SET NULL,
    updated_at          timestamptz NOT NULL DEFAULT now()
);
INSERT INTO instance_update (id) VALUES (true);

-- +goose Down
DROP TABLE instance_update;
