-- +goose Up
-- Adoption of a server that already runs NebulaCtrl workloads (a lost or
-- reinstalled control plane, or a move from another instance).
--   adoption_state  '' (normal) | 'held': the first hello of the server found
--                   managed containers this instance does not know; the agent
--                   then sweeps nothing until the adoption is reviewed.
ALTER TABLE servers ADD COLUMN adoption_state text NOT NULL DEFAULT ''
    CHECK (adoption_state IN ('', 'held'));

-- Services left running unmanaged on a server by an adoption: the agent never
-- sweeps their containers, networks or live routes. service_id names a service
-- of the earlier instance (no row here), so it has no foreign key.
CREATE TABLE server_unmanaged_services (
    server_id  uuid NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
    service_id uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (server_id, service_id)
);

-- The 'adopt' trigger marks the deployment an adoption records for the
-- containers already running (no deploy ran).
ALTER TABLE deployments DROP CONSTRAINT deployments_trigger_check;
ALTER TABLE deployments ADD CONSTRAINT deployments_trigger_check
    CHECK (trigger IN ('manual','redeploy','rollback','staged','reconcile','drain','git_push','preview','cron','deploy_hook','adopt'));

-- +goose Down
UPDATE deployments SET trigger = 'reconcile' WHERE trigger = 'adopt';
ALTER TABLE deployments DROP CONSTRAINT deployments_trigger_check;
ALTER TABLE deployments ADD CONSTRAINT deployments_trigger_check
    CHECK (trigger IN ('manual','redeploy','rollback','staged','reconcile','drain','git_push','preview','cron','deploy_hook'));
DROP TABLE server_unmanaged_services;
ALTER TABLE servers DROP COLUMN adoption_state;
