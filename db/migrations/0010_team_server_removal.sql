-- +goose Up
-- Removing a server and deleting a team both wait for agents (a decommission
-- empties the host before the row goes), so each is a background job with a
-- state the UI shows and a failure it can retry.

-- A server being removed: 'removing' while the job waits for the agent,
-- 'failed' (removal_error says why) until the person retries or forgets it.
ALTER TABLE servers
    ADD COLUMN removal_state      text        NOT NULL DEFAULT '' CHECK (removal_state IN ('', 'removing', 'failed')),
    ADD COLUMN removal_error      text        NOT NULL DEFAULT '',
    ADD COLUMN removal_started_at timestamptz;

-- A team being deleted: 'deleting' while the cascade job runs, 'failed'
-- (deletion_error says why) until it is retried. deletion_step is where the job
-- is (projects, servers, integrations, team); deletion_forgotten lists the
-- servers that were offline and so were removed without touching their hosts;
-- deletion_keep_dns is the choice to leave the DNS records in Cloudflare.
ALTER TABLE workspaces
    ADD COLUMN deletion_state      text        NOT NULL DEFAULT '' CHECK (deletion_state IN ('', 'deleting', 'failed')),
    ADD COLUMN deletion_error      text        NOT NULL DEFAULT '',
    ADD COLUMN deletion_step       text        NOT NULL DEFAULT '',
    ADD COLUMN deletion_started_at timestamptz,
    ADD COLUMN deletion_forgotten  text[]      NOT NULL DEFAULT '{}',
    ADD COLUMN deletion_keep_dns   boolean     NOT NULL DEFAULT false;

-- +goose Down
ALTER TABLE workspaces
    DROP COLUMN deletion_keep_dns,
    DROP COLUMN deletion_forgotten,
    DROP COLUMN deletion_started_at,
    DROP COLUMN deletion_step,
    DROP COLUMN deletion_error,
    DROP COLUMN deletion_state;
ALTER TABLE servers
    DROP COLUMN removal_started_at,
    DROP COLUMN removal_error,
    DROP COLUMN removal_state;
