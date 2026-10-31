-- +goose Up
-- nebula.toml (internal/configsync): ownership of file-managed fields, the per-environment
-- file binding/status/auto-apply, and the identity of file-managed backup schedules.

-- One row per service that has been applied from a file. owned maps a field id to the
-- fingerprint of the value the last apply wrote (the "base" of the three-way drift check);
-- pending holds immediate-kind changes that could not run yet (service not deployed).
CREATE TABLE config_sources (
    service_id         uuid PRIMARY KEY REFERENCES services(id) ON DELETE CASCADE,
    environment_id     uuid NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
    workspace_id       uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    origin             text NOT NULL CHECK (origin IN ('git','cli','api','mcp','ui')),
    repo_full_name     text NOT NULL DEFAULT '',
    branch             text NOT NULL DEFAULT '',
    path               text NOT NULL DEFAULT '',
    commit_sha         text NOT NULL DEFAULT '',
    file_sha256        text NOT NULL,
    schema_version     integer NOT NULL DEFAULT 1,
    owned              jsonb NOT NULL DEFAULT '{}'::jsonb,
    pending            jsonb NOT NULL DEFAULT '{}'::jsonb,
    applied_generation bigint NOT NULL,
    applied_at         timestamptz NOT NULL DEFAULT now(),
    applied_by_type    text NOT NULL CHECK (applied_by_type IN ('user','token','mcp','system')),
    applied_by_id      text NOT NULL DEFAULT '',
    applied_by_label   text NOT NULL DEFAULT ''
);
CREATE INDEX config_sources_env_idx ON config_sources (environment_id);

-- One row per environment that has a file binding, auto_apply, a check status or owned shared
-- variables. An empty repo_full_name with a NULL connection_id is a derived binding.
-- auto_apply is never true for a production environment (enforced in the service layer and
-- cleared when the environment is marked production).
CREATE TABLE environment_config_settings (
    environment_id   uuid PRIMARY KEY REFERENCES environments(id) ON DELETE CASCADE,
    workspace_id     uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    connection_id    uuid REFERENCES git_connections(id) ON DELETE SET NULL,
    repo_full_name   text NOT NULL DEFAULT '',
    branch           text NOT NULL DEFAULT '',
    path             text NOT NULL DEFAULT '',
    auto_apply       boolean NOT NULL DEFAULT false,
    owned_shared     jsonb NOT NULL DEFAULT '{}'::jsonb,
    status           text NOT NULL DEFAULT 'unknown'
                     CHECK (status IN ('unknown','in_sync','differs','invalid','not_found','unreachable','error')),
    status_detail    text NOT NULL DEFAULT '',
    last_commit_sha  text NOT NULL DEFAULT '',
    last_file_sha256 text NOT NULL DEFAULT '',
    last_plan        jsonb,
    checked_at       timestamptz,
    last_path        text NOT NULL DEFAULT '',
    updated_at       timestamptz NOT NULL DEFAULT now(),
    CHECK ((repo_full_name = '') = (branch = ''))
);
CREATE INDEX environment_config_settings_repo_idx
    ON environment_config_settings (connection_id, repo_full_name, branch) WHERE repo_full_name <> '';

-- Identity of a file-managed backup schedule (backups.<key>). NULL = created elsewhere, never
-- touched by the file.
ALTER TABLE backup_schedules ADD COLUMN config_key text;
CREATE UNIQUE INDEX backup_schedules_config_key_uniq
    ON backup_schedules (service_id, config_key) WHERE config_key IS NOT NULL;

-- +goose Down
DROP INDEX backup_schedules_config_key_uniq;
ALTER TABLE backup_schedules DROP COLUMN config_key;
DROP TABLE environment_config_settings;
DROP TABLE config_sources;
