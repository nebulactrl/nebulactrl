-- +goose Up
-- The newest commit wait-for-CI refused to deploy (failed checks, checks that
-- cannot be read, or checks pending too long): {sha, branch, reason, at}. A
-- check for new commits does not park that commit again while the service
-- waits for CI, and the service's link shows why it was not deployed.
ALTER TABLE service_git ADD COLUMN ci_blocked jsonb;

-- +goose Down
ALTER TABLE service_git DROP COLUMN ci_blocked;
