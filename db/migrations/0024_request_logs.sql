-- +goose Up
-- Request logs: the agent stores one line per routed HTTP request in a store of
-- its own on the host (never drained, never sent to a central log). They are on
-- by default for servers with traffic analytics; query strings are dropped
-- unless the server opts in. Requests for assets (scripts, styles, images,
-- fonts) are only stored when they failed, unless the server keeps them all.
-- A server without a row uses these defaults too.
ALTER TABLE server_log_settings
    ADD COLUMN request_logs             boolean NOT NULL DEFAULT true,
    ADD COLUMN request_logs_keep_query  boolean NOT NULL DEFAULT false,
    ADD COLUMN request_logs_keep_assets boolean NOT NULL DEFAULT false;

-- +goose Down
ALTER TABLE server_log_settings
    DROP COLUMN request_logs_keep_assets,
    DROP COLUMN request_logs_keep_query,
    DROP COLUMN request_logs;
