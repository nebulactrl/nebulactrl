-- +goose Up
-- Wait for CI: a push to a linked service deploys only after the commit's checks pass.
-- ci_wait is the newest push still waiting for them: {sha, message, author, branch, since, checkedAt}.
ALTER TABLE service_git
    ADD COLUMN wait_for_ci boolean NOT NULL DEFAULT false,
    ADD COLUMN ci_wait     jsonb;

-- +goose Down
ALTER TABLE service_git
    DROP COLUMN ci_wait,
    DROP COLUMN wait_for_ci;
