-- +goose Up
-- Replicas and autoscaling of the NAMED processes of a service. The default process keeps services.replicas /
-- services.autoscaling / last_scale_at / last_scale_event / scale_blocked_reason. A missing row = 1 replica,
-- default autoscaling. Immediate settings: never bump services.generation, never part of a release.
CREATE TABLE service_process_scale (
    service_id           uuid        NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    process              text        NOT NULL CHECK (process ~ '^[a-z0-9]([a-z0-9-]{0,29}[a-z0-9])?$'),
    replicas             integer     NOT NULL DEFAULT 1 CHECK (replicas BETWEEN 1 AND 10),
    autoscaling          jsonb       NOT NULL DEFAULT '{"enabled":false,"min":1,"max":3,"targetCpuPercent":70}'::jsonb,
    last_scale_at        timestamptz,
    last_scale_event     jsonb,
    scale_blocked_reason text,
    updated_at           timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (service_id, process)
);

-- Metrics get a process dimension. '' = legacy single-process samples.
ALTER TABLE metric_samples     ADD COLUMN process text NOT NULL DEFAULT '';
ALTER TABLE metric_samples_1m  ADD COLUMN process text NOT NULL DEFAULT '';
ALTER TABLE metric_samples_15m ADD COLUMN process text NOT NULL DEFAULT '';
-- Rollup rows with process = '' are the service total (today's rows, now also for multi-process services).
-- Rows with a process name are that process' own sums (written only for samples that carry a process).
DROP INDEX metric_samples_1m_key;
CREATE UNIQUE INDEX metric_samples_1m_key  ON metric_samples_1m  (server_id, (COALESCE(service_id, '00000000-0000-0000-0000-000000000000'::uuid)), process, bucket);
DROP INDEX metric_samples_15m_key;
CREATE UNIQUE INDEX metric_samples_15m_key ON metric_samples_15m (server_id, (COALESCE(service_id, '00000000-0000-0000-0000-000000000000'::uuid)), process, bucket);
CREATE INDEX metric_samples_process_idx ON metric_samples (service_id, process, ts) WHERE process <> '';

-- "Combine into one service" saga.
CREATE TABLE service_combines (
    id                 uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    environment_id     uuid        NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
    target_service_id  uuid        NOT NULL REFERENCES services(id),
    source_service_ids uuid[]      NOT NULL,
    -- the accepted CombineRequest plus the resolved moves (domains, tasks, scale rows, stopped services)
    plan               jsonb       NOT NULL,
    state              text        NOT NULL CHECK (state IN ('staged', 'deploying', 'cutover', 'done', 'failed', 'aborted')),
    deployment_id      uuid        REFERENCES deployments(id),
    error              text        NOT NULL DEFAULT '',
    created_by         text        NOT NULL DEFAULT '', -- actor label
    created_at         timestamptz NOT NULL DEFAULT now(),
    updated_at         timestamptz NOT NULL DEFAULT now(),
    finished_at        timestamptz
);
CREATE UNIQUE INDEX service_combines_one_active ON service_combines (target_service_id) WHERE state IN ('staged', 'deploying', 'cutover');
CREATE INDEX service_combines_env_idx ON service_combines (environment_id, created_at DESC);

-- +goose Down
DROP TABLE service_combines;
DROP INDEX metric_samples_process_idx;
-- Per-process rollup rows would break the old unique indexes; the service totals (process = '') stay.
DELETE FROM metric_samples_15m WHERE process <> '';
DELETE FROM metric_samples_1m WHERE process <> '';
DROP INDEX metric_samples_15m_key;
CREATE UNIQUE INDEX metric_samples_15m_key ON metric_samples_15m (server_id, (COALESCE(service_id, '00000000-0000-0000-0000-000000000000'::uuid)), bucket);
DROP INDEX metric_samples_1m_key;
CREATE UNIQUE INDEX metric_samples_1m_key ON metric_samples_1m (server_id, (COALESCE(service_id, '00000000-0000-0000-0000-000000000000'::uuid)), bucket);
ALTER TABLE metric_samples_15m DROP COLUMN process;
ALTER TABLE metric_samples_1m DROP COLUMN process;
ALTER TABLE metric_samples DROP COLUMN process;
DROP TABLE service_process_scale;
