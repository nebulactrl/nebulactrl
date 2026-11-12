-- +goose Up
-- Service firewall: the ordered rules of agentprotocol.Firewall ({"rules": [...]}),
-- evaluated by the proxy against every request to the service's domains.
-- Operational like maintenance: it never bumps the generation, is not part of
-- a release and is never rolled back; a change reaches the server at once on
-- the route table. NULL: no firewall.
ALTER TABLE services ADD COLUMN firewall jsonb;

-- +goose Down
ALTER TABLE services DROP COLUMN firewall;
