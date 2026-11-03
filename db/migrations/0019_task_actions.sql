-- +goose Up
-- A task's action: run a command in a throwaway container (command), restart
-- the service's running container (restart), or the hidden schedule of a cron
-- service that runs the service's own command (service; never listed).
ALTER TABLE service_tasks
    ADD COLUMN action text NOT NULL DEFAULT 'command' CHECK (action IN ('command', 'restart', 'service'));

-- +goose Down
ALTER TABLE service_tasks DROP COLUMN action;
