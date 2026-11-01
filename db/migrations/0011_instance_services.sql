-- +goose Up
-- Built-in services are instance infrastructure. S3 (backups), the image
-- registry and Loki run once, beside the control plane; every team gets an
-- isolated slice: a bucket and a key that can only touch it, a repository
-- prefix, a log tenant. Headscale stays a per-team platform service (a team is
-- its own tailnet); Grafana is gone.

-- One row per instance service kind: its health and its instance-wide options
-- (s3 {defaultQuotaBytes}; registry {readOnly, phase, lastGcAt}). secrets_enc
-- is sealed with secretbox (s3: the system bucket key; registry: the token
-- signing key) and never returned.
CREATE TABLE instance_services (
    kind        text PRIMARY KEY CHECK (kind IN ('s3','registry','loki')),
    status      text NOT NULL DEFAULT 'provisioning' CHECK (status IN ('provisioning','ready','failed')),
    message     text NOT NULL DEFAULT '',
    endpoint    text NOT NULL DEFAULT '',
    options     jsonb NOT NULL DEFAULT '{}',
    secrets_enc bytea,
    updated_at  timestamptz NOT NULL DEFAULT now()
);

-- A team's slice of one instance service. team_id has no foreign key: the slice
-- outlives the team by 30 days (deleted_at/purge_at), then it is purged.
--   ref         s3: the bucket; registry: "<team-id>/"; loki: the tenant
--   secret_enc  s3 only: the secret of the bucket-scoped key (sealed, never returned)
--   quota_bytes NULL = the instance default; s3 only
--   alert_level the highest storage alert already sent (0, 80 or 100)
CREATE TABLE team_service_slices (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    team_id     uuid NOT NULL,
    team_name   text NOT NULL,
    kind        text NOT NULL CHECK (kind IN ('s3','registry','loki')),
    ref         text NOT NULL,
    access_key  text NOT NULL DEFAULT '',
    secret_enc  bytea,
    quota_bytes bigint CHECK (quota_bytes > 0),
    used_bytes  bigint NOT NULL DEFAULT 0,
    used_at     timestamptz,
    state       text NOT NULL DEFAULT 'provisioning' CHECK (state IN ('provisioning','ready','full','failed')),
    message     text NOT NULL DEFAULT '',
    alert_level integer NOT NULL DEFAULT 0 CHECK (alert_level IN (0,80,100)),
    deleted_at  timestamptz,
    purge_at    timestamptz,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    UNIQUE (team_id, kind)
);
CREATE INDEX team_service_slices_purge ON team_service_slices (purge_at) WHERE deleted_at IS NOT NULL;
CREATE UNIQUE INDEX team_service_slices_access_key ON team_service_slices (access_key) WHERE access_key <> '';

-- Per-team S3, registry, Loki and Grafana are gone. Their host containers stay
-- running (the owner moves the data by hand); only the rows go. Registries that
-- pointed at the managed registry fall back (ON DELETE SET NULL).
--
-- Their public routes go first. Only the Headscale route is IP-restricted now,
-- so a leftover domain of an old platform service would be served to the whole
-- internet. The services keep running without a domain; the route tables drop
-- the hostnames the next time they are built. The Headscale domain is untouched.
DELETE FROM domains WHERE service_id IN (
    SELECT unnest(service_ids) FROM platform_services WHERE kind <> 'headscale'
);
DELETE FROM platform_services WHERE kind <> 'headscale';
ALTER TABLE platform_services DROP CONSTRAINT platform_services_kind_check;
ALTER TABLE platform_services ADD CONSTRAINT platform_services_kind_check CHECK (kind = 'headscale');

DELETE FROM registries WHERE kind = 'managed';
ALTER TABLE registries DROP COLUMN platform_service_id;
ALTER TABLE registries DROP CONSTRAINT registries_kind_check;
ALTER TABLE registries ADD CONSTRAINT registries_kind_check CHECK (kind IN ('external','builtin'));
CREATE UNIQUE INDEX registries_one_builtin ON registries (workspace_id) WHERE kind = 'builtin';

-- The built-in rows hold no credentials: the control plane resolves endpoint,
-- bucket and key from the team's slice when it builds a command.
ALTER TABLE backup_destinations DROP CONSTRAINT backup_destinations_kind_check;
ALTER TABLE backup_destinations ADD CONSTRAINT backup_destinations_kind_check CHECK (kind IN ('s3','local','builtin'));
CREATE UNIQUE INDEX backup_destinations_one_builtin ON backup_destinations (workspace_id) WHERE kind = 'builtin';

ALTER TABLE instance_backup_settings DROP CONSTRAINT instance_backup_settings_dest_kind_check;
ALTER TABLE instance_backup_settings ADD CONSTRAINT instance_backup_settings_dest_kind_check CHECK (dest_kind IN ('s3','local','builtin'));

ALTER TABLE log_drains ADD COLUMN builtin boolean NOT NULL DEFAULT false;
CREATE UNIQUE INDEX log_drains_one_builtin ON log_drains (workspace_id) WHERE builtin;

-- +goose Down
-- Rows that only exist in the new model are dropped; the per-team services they
-- replaced are not recreated.
DROP INDEX log_drains_one_builtin;
ALTER TABLE log_drains DROP COLUMN builtin;

UPDATE instance_backup_settings SET dest_kind = 'local', enabled = false WHERE dest_kind = 'builtin';
ALTER TABLE instance_backup_settings DROP CONSTRAINT instance_backup_settings_dest_kind_check;
ALTER TABLE instance_backup_settings ADD CONSTRAINT instance_backup_settings_dest_kind_check CHECK (dest_kind IN ('s3','local'));

DROP INDEX backup_destinations_one_builtin;
DELETE FROM backup_destinations WHERE kind = 'builtin';
ALTER TABLE backup_destinations DROP CONSTRAINT backup_destinations_kind_check;
ALTER TABLE backup_destinations ADD CONSTRAINT backup_destinations_kind_check CHECK (kind IN ('s3','local'));

DROP INDEX registries_one_builtin;
DELETE FROM registries WHERE kind = 'builtin';
ALTER TABLE registries DROP CONSTRAINT registries_kind_check;
ALTER TABLE registries ADD CONSTRAINT registries_kind_check CHECK (kind IN ('external','managed'));
ALTER TABLE registries ADD COLUMN platform_service_id uuid REFERENCES platform_services(id) ON DELETE SET NULL;

ALTER TABLE platform_services DROP CONSTRAINT platform_services_kind_check;
ALTER TABLE platform_services ADD CONSTRAINT platform_services_kind_check
    CHECK (kind IN ('s3','loki','grafana','headscale','registry'));

DROP TABLE team_service_slices;
DROP TABLE instance_services;
