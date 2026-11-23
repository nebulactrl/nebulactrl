-- +goose Up
-- Service auto-ban policy: agentprotocol.AutoBan ({"enabled", "threshold",
-- "windowSeconds", "durationSeconds", "allow"}). Operational like the
-- firewall: it never bumps the generation, is not part of a release and is
-- never rolled back; a change reaches the server at once on the route table.
-- NULL: no policy.
ALTER TABLE services ADD COLUMN autoban jsonb;

-- Bans the agent's auto-ban engine decided, mirrored from its ban frames. The
-- row id is the agent's ban id, so a redelivered frame changes nothing. The
-- visitor's address is kept only while the ban is active (enforcement and
-- unban need it): it is NULL once the ban ended, and ended rows keep the
-- prefix only (pruned after 90 days).
CREATE TABLE service_bans (
    id           uuid PRIMARY KEY,
    server_id    uuid NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
    service_id   uuid NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    route_id     text NOT NULL,
    prefix       text NOT NULL,
    address      text,
    started_at   timestamptz NOT NULL,
    expires_at   timestamptz NOT NULL,
    ended_at     timestamptz,
    end_reason   text CHECK (end_reason IN ('expired', 'unbanned', 'lifted', 'resync')),
    strikes      int NOT NULL DEFAULT 0,
    rules        jsonb NOT NULL DEFAULT '{}'::jsonb,
    source       text NOT NULL DEFAULT 'auto',
    -- When the ban was counted into a service.autoban notification digest.
    notified_at  timestamptz,
    created_at   timestamptz NOT NULL DEFAULT now(),
    CHECK ((ended_at IS NULL) = (end_reason IS NULL))
);
CREATE INDEX service_bans_active_idx ON service_bans (service_id) WHERE ended_at IS NULL;
CREATE INDEX service_bans_server_active_idx ON service_bans (server_id) WHERE ended_at IS NULL;
CREATE INDEX service_bans_ended_idx ON service_bans (ended_at) WHERE ended_at IS NOT NULL;
CREATE INDEX service_bans_unnotified_idx ON service_bans (created_at) WHERE notified_at IS NULL;

-- +goose Down
DROP TABLE service_bans;
ALTER TABLE services DROP COLUMN autoban;
