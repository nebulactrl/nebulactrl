-- +goose Up
-- Requests the service firewall blocked, per rule: {"<ruleId>": n}. The rule id is
-- a user rule's id or a standard protection's (std…). Counted in the agent next
-- to status_codes and merged the same way by the hourly and daily rollups; only
-- these counts leave the host, never the request lines. '{}' for rows recorded
-- before the agent reported blocks (nothing is backfilled).
ALTER TABLE traffic_1m ADD COLUMN blocked jsonb NOT NULL DEFAULT '{}'::jsonb;
ALTER TABLE traffic_1h ADD COLUMN blocked jsonb NOT NULL DEFAULT '{}'::jsonb;
ALTER TABLE traffic_1d ADD COLUMN blocked jsonb NOT NULL DEFAULT '{}'::jsonb;

-- +goose Down
ALTER TABLE traffic_1d DROP COLUMN blocked;
ALTER TABLE traffic_1h DROP COLUMN blocked;
ALTER TABLE traffic_1m DROP COLUMN blocked;
