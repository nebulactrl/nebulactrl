-- +goose Up
-- +goose StatementBegin
CREATE EXTENSION IF NOT EXISTS pgcrypto;

-- ============================================================================
-- Identity and access
-- ============================================================================

-- A user belongs to teams through memberships (the "team" of the product is
-- the workspace of the code).
CREATE TABLE users (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    email          text NOT NULL UNIQUE,
    name           text NOT NULL,
    password_hash  text NOT NULL,
    created_at     timestamptz NOT NULL DEFAULT now()
);

-- A team is a workspace: every top-level table carries workspace_id.
-- require_two_factor: members of a team that requires two-factor authentication
-- must enrol TOTP (or sign in through an identity provider that asserts MFA and
-- is trusted) before they can act in that team.
CREATE TABLE workspaces (
    id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name               text NOT NULL,
    created_at         timestamptz NOT NULL DEFAULT now(),
    slug               text NOT NULL DEFAULT ('team-' || substr(replace(gen_random_uuid()::text, '-', ''), 1, 12)),
    created_by         uuid REFERENCES users(id) ON DELETE SET NULL,
    require_two_factor boolean NOT NULL DEFAULT false
);
CREATE UNIQUE INDEX workspaces_slug_key ON workspaces (slug);

-- One row per (user, team): the user's role in that team and an optional
-- per-project restriction (NULL = every project of the team).
CREATE TABLE memberships (
    user_id       uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    workspace_id  uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    role          text NOT NULL CHECK (role IN ('owner','admin','developer','viewer')),
    project_ids   uuid[],
    created_at    timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, workspace_id)
);
CREATE INDEX memberships_workspace_idx ON memberships (workspace_id, created_at);

-- scs pgxstore schema
CREATE TABLE sessions (
    token   text PRIMARY KEY,
    data    bytea NOT NULL,
    expiry  timestamptz NOT NULL
);
CREATE INDEX sessions_expiry_idx ON sessions (expiry);

-- API tokens.
-- allow_production_without_approval: an Owner/Admin may switch the production
-- guard off for one MCP token (off by default). require_production_approval: any
-- token creator may switch the guard on for one REST token (REST tokens are not
-- guarded by default so CI pipelines and deploy scripts keep working).
CREATE TABLE api_tokens (
    id                                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id                      uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    user_id                           uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name                              text NOT NULL,
    token_hash                        bytea NOT NULL UNIQUE,
    prefix                            text NOT NULL,
    scopes                            text[] NOT NULL,
    project_ids                       uuid[],
    expires_at                        timestamptz,
    last_used_at                      timestamptz,
    revoked_at                        timestamptz,
    created_at                        timestamptz NOT NULL DEFAULT now(),
    allow_production_without_approval boolean NOT NULL DEFAULT false,
    require_production_approval       boolean NOT NULL DEFAULT false
);

-- Invites by email or link. The raw token (nci_…) is shown once at creation;
-- only its SHA-256 is stored. email NULL = link invite. A link works until it
-- expires, is revoked or has been used max_uses times; one user can use an
-- invite once (team_invite_uses).
CREATE TABLE team_invites (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id  uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    token_hash    bytea NOT NULL UNIQUE,
    prefix        text NOT NULL,
    email         text,
    role          text NOT NULL CHECK (role IN ('owner','admin','developer','viewer')),
    project_ids   uuid[],
    max_uses      integer NOT NULL DEFAULT 1 CHECK (max_uses >= 1),
    use_count     integer NOT NULL DEFAULT 0,
    expires_at    timestamptz NOT NULL,
    email_sent_at timestamptz,
    created_by    uuid REFERENCES users(id) ON DELETE SET NULL,
    revoked_at    timestamptz,
    created_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX team_invites_workspace_idx ON team_invites (workspace_id, created_at DESC);

CREATE TABLE team_invite_uses (
    invite_id  uuid NOT NULL REFERENCES team_invites(id) ON DELETE CASCADE,
    user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    used_at    timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (invite_id, user_id)
);

-- Password reset links. The raw token (30 minutes, single use) is only ever in
-- the email or the admin's screen; the table keeps its SHA-256. created_by is
-- NULL for a link the account holder asked for by email, and the admin's user
-- id for a link an admin generated for a member.
CREATE TABLE password_reset_tokens (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash bytea NOT NULL UNIQUE,
    created_by uuid REFERENCES users(id) ON DELETE SET NULL,
    expires_at timestamptz NOT NULL,
    used_at    timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX password_reset_tokens_user_idx ON password_reset_tokens (user_id, created_at DESC);

-- TOTP secret per user. enabled_at is NULL while enrolment is pending
-- confirmation. last_step is the last accepted 30 s time step (replay guard);
-- failures/locked_until throttle guessing across sessions.
CREATE TABLE user_totp (
    user_id      uuid PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    secret_enc   bytea NOT NULL,
    enabled_at   timestamptz,
    last_step    bigint NOT NULL DEFAULT 0,
    failures     integer NOT NULL DEFAULT 0,
    locked_until timestamptz,
    created_at   timestamptz NOT NULL DEFAULT now()
);

-- Single-use recovery codes, stored as SHA-256 of the normalised code.
CREATE TABLE user_recovery_codes (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    code_hash  text NOT NULL,
    used_at    timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (user_id, code_hash)
);

-- WebAuthn passkeys. A user can register several, name them, and use any of them
-- to sign in without a password or as the second step after a password. The
-- credential column is the go-webauthn Credential (public key, sign counter,
-- flags, transports) without the attestation blob. Nothing here is secret: the
-- private key never leaves the authenticator.
CREATE TABLE user_passkeys (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id       uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name          text NOT NULL,
    credential_id bytea NOT NULL UNIQUE,
    credential    jsonb NOT NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    last_used_at  timestamptz
);
CREATE INDEX user_passkeys_user_idx ON user_passkeys (user_id, created_at);

-- Instance-wide sign-in settings (one row). Open sign-up lets a new person create
-- an account through an identity provider without an invite; it defaults to off.
CREATE TABLE auth_settings (
    id                boolean PRIMARY KEY DEFAULT true CHECK (id),
    allow_open_signup boolean NOT NULL DEFAULT false,
    updated_at        timestamptz NOT NULL DEFAULT now()
);
INSERT INTO auth_settings (id) VALUES (true);

-- Identity providers. kind 'oidc' is any OpenID Connect issuer with discovery;
-- kind 'github' is GitHub OAuth (issuer_url is then an optional GitHub Enterprise
-- Server base URL, empty for github.com). The client secret is sealed with
-- secretbox and never returned.
CREATE TABLE auth_providers (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    kind              text NOT NULL CHECK (kind IN ('oidc','github')),
    name              text NOT NULL,
    issuer_url        text NOT NULL DEFAULT '',
    client_id         text NOT NULL,
    client_secret_enc bytea NOT NULL,
    scopes            text[] NOT NULL DEFAULT '{}',
    allowed_domains   text[] NOT NULL DEFAULT '{}',
    trust_mfa         boolean NOT NULL DEFAULT false,
    enabled           boolean NOT NULL DEFAULT true,
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now()
);

-- A user's link to an identity-provider account. subject is the issuer's stable
-- "sub" (the numeric id for GitHub).
CREATE TABLE user_identities (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id     uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    provider_id uuid NOT NULL REFERENCES auth_providers(id) ON DELETE CASCADE,
    subject     text NOT NULL,
    email       text NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    last_login_at timestamptz,
    UNIQUE (provider_id, subject),
    UNIQUE (user_id, provider_id)
);
CREATE INDEX user_identities_user_idx ON user_identities (user_id);

-- ============================================================================
-- Instance settings (single-row tables, edited by the instance admin = the
-- first user)
-- ============================================================================

-- smtp_* is the fallback mail relay for invites when a team has no email
-- notification channel.
CREATE TABLE instance_settings (
    id                    boolean PRIMARY KEY DEFAULT true CHECK (id),
    allow_team_creation   boolean NOT NULL DEFAULT true,
    smtp_host             text NOT NULL DEFAULT '',
    smtp_port             integer NOT NULL DEFAULT 587,
    smtp_tls              text NOT NULL DEFAULT 'starttls' CHECK (smtp_tls IN ('implicit','starttls','none')),
    smtp_username         text NOT NULL DEFAULT '',
    smtp_password_enc     bytea,
    smtp_from             text NOT NULL DEFAULT '',
    smtp_insecure_skip_verify boolean NOT NULL DEFAULT false,
    updated_at            timestamptz NOT NULL DEFAULT now()
);
INSERT INTO instance_settings (id) VALUES (true);

-- Control-plane backup: a scheduled pg_dump of NebulaCtrl's own database to ONE
-- instance-wide destination (an S3-compatible bucket or a directory on the
-- control-plane host). It belongs to no team. The instance secret key
-- (NEBULACTRL_SECRET_KEY) is never part of a dump and is not stored here; only
-- the admin's acknowledgement that it is stored elsewhere is (key_ack_*).
-- Destination credentials are sealed with secretbox and write-only.
CREATE TABLE instance_backup_settings (
    id                boolean PRIMARY KEY DEFAULT true CHECK (id),
    enabled           boolean NOT NULL DEFAULT false,
    -- Destination. local_dir is a relative sub-directory of NEBULACTRL_BACKUP_DIR.
    dest_kind         text NOT NULL DEFAULT 'local' CHECK (dest_kind IN ('s3','local')),
    endpoint          text NOT NULL DEFAULT '',
    region            text NOT NULL DEFAULT '',
    bucket            text NOT NULL DEFAULT '',
    prefix            text NOT NULL DEFAULT '',
    path_style        boolean NOT NULL DEFAULT false,
    compat_mode       boolean NOT NULL DEFAULT true,
    access_key_enc    bytea,
    secret_key_enc    bytea,
    local_dir         text NOT NULL DEFAULT '',
    -- Schedule: 5-field cron in UTC, default daily 03:30. Retention keeps the
    -- newest retention_count successful backups.
    schedule          text NOT NULL DEFAULT '30 3 * * *',
    retention_count   integer NOT NULL DEFAULT 14 CHECK (retention_count BETWEEN 1 AND 365),
    next_run_at       timestamptz,
    last_test_at      timestamptz,
    last_test_ok      boolean,
    last_test_message text NOT NULL DEFAULT '',
    -- The reminder card stays until an admin confirms the secret key is kept
    -- somewhere other than next to the backups.
    key_ack_at        timestamptz,
    key_ack_by        uuid REFERENCES users(id) ON DELETE SET NULL,
    updated_at        timestamptz NOT NULL DEFAULT now()
);
INSERT INTO instance_backup_settings (id) VALUES (true);

-- One control-plane backup run. The destination is copied onto the row (dest_*)
-- so history survives a destination change; retention only prunes rows whose
-- dest_fingerprint equals the current destination's. object_deleted_at is set
-- when retention removed the stored objects; the row stays as history. A
-- pre-upgrade backup is an instance backup with its own trigger.
CREATE TABLE instance_backups (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    trigger          text NOT NULL CHECK (trigger IN ('schedule','manual','pre_upgrade')),
    status           text NOT NULL DEFAULT 'running' CHECK (status IN ('running','succeeded','failed')),
    dest_kind        text NOT NULL,
    dest_label       text NOT NULL,
    dest_fingerprint text NOT NULL,
    object_key       text NOT NULL DEFAULT '',
    size_bytes       bigint NOT NULL DEFAULT 0,
    sha256           text NOT NULL DEFAULT '',
    pg_version       text NOT NULL DEFAULT '',
    error            text NOT NULL DEFAULT '',
    actor_label      text NOT NULL DEFAULT '',
    started_at       timestamptz NOT NULL DEFAULT now(),
    finished_at      timestamptz,
    object_deleted_at timestamptz
);
CREATE INDEX instance_backups_started_idx ON instance_backups (started_at DESC);
-- At most one run at a time.
CREATE UNIQUE INDEX instance_backups_one_running ON instance_backups ((true)) WHERE status = 'running';

-- Agent self-upgrade and the control-plane pre-upgrade backup. One row of
-- instance-wide upgrade state.
--   last_version         the version of the control plane that last finished its
--                        startup (migrations applied). A start with another
--                        version is an upgrade (or a downgrade): the control
--                        plane takes a backup first (internal/backups).
--   agent_auto_upgrade   upgrade an agent behind the control plane when it
--                        connects. Default on.
--   pre_upgrade_*        outcome of the last pre-upgrade backup, for the UI.
CREATE TABLE instance_upgrade (
    id                       boolean PRIMARY KEY DEFAULT true CHECK (id),
    last_version             text NOT NULL DEFAULT '',
    agent_auto_upgrade       boolean NOT NULL DEFAULT true,
    pre_upgrade_at           timestamptz,
    pre_upgrade_status       text NOT NULL DEFAULT '' CHECK (pre_upgrade_status IN ('','succeeded','failed','skipped','forced')),
    pre_upgrade_message      text NOT NULL DEFAULT '',
    pre_upgrade_from_version text NOT NULL DEFAULT '',
    pre_upgrade_to_version   text NOT NULL DEFAULT '',
    updated_at               timestamptz NOT NULL DEFAULT now()
);
INSERT INTO instance_upgrade (id) VALUES (true);

-- Access control of the dashboard's system route (internal/systemroute): who
-- may reach the dashboard's hostname on the control-plane host's proxy and how
-- its certificate is issued. It belongs to no team.
--
-- One row. It is created the first time the installer's settings
-- (NEBULACTRL_DASHBOARD_ALLOW_CIDRS / _DNS_RESOLVER) are seen, and the
-- installer keeps updating it on every start until an admin edits it
-- (edited_at IS NOT NULL); from then on the row is the source of truth, so the
-- admin's changes survive restarts and installer re-runs. No row means "the
-- defaults": no allow list, automatic HTTP-01.
CREATE TABLE dashboard_access (
    id                    boolean PRIMARY KEY DEFAULT true CHECK (id),
    -- CIDRs or addresses allowed to reach the dashboard; empty = everybody.
    allow_cidrs           text[] NOT NULL DEFAULT '{}',
    -- DNS-01 provider code of the dashboard's certificate; '' = HTTP-01. The
    -- installer sets it only once the provider's resolver is live on the
    -- proxy.
    dns_resolver          text NOT NULL DEFAULT '',
    seeded_from_installer boolean NOT NULL DEFAULT false,
    edited_at             timestamptz,
    edited_by             uuid REFERENCES users(id) ON DELETE SET NULL,
    updated_at            timestamptz NOT NULL DEFAULT now()
);

-- ============================================================================
-- Servers and agents
-- ============================================================================

-- Observability settings: metrics retention and a disk threshold, plus traffic
-- analytics retention.
-- Draining (evacuation): draining/drained servers are not schedulable.
-- Proxy settings: proxy_generation is bumped by every server-level staged change
-- (certificate upload/delete, upstream policy, tailnet mode, proxy ports);
-- proxy_applied_generation is set when "Deploy proxy" succeeds.
--   upstream_policy {allowLoopback, allowLinkLocal, allowHostGateway}
--   tailnet_status  observed {state, fqdn, ips[], deviceId, message, source}
-- upstream_policy / tailnet_hostname / tailnet_magic_dns / proxy_http_port /
-- proxy_https_port are DESIRED; the applied_* / *_applied_* columns are what the
-- last successful proxy_configure applied (applied_dns01 is
-- {"resolvers":[DNSResolver...],"wildcards":[WildcardCert...]}, no values).
-- proxy_http_port / proxy_https_port NULL = whatever the agent was started with
-- (--proxy-http-port, --proxy-https-port: 80 and 443 by default). What the proxy
-- actually listens on is observed (facts.proxyPorts, from heartbeats), not
-- stored here.
-- Geolocation of request origins (the agent looks client IPs up in a local GeoIP
-- database and ships per-minute counts per country and city; IPs are never sent
-- or stored):
--   geo_source  dbip     DB-IP Lite (CC BY 4.0, no key)
--               maxmind  MaxMind GeoLite2 (account ID + licence key of the team)
--   geo_level   country  ~8 MB database, country counts only
--               city     ~130 MB database, adds city counts
-- unrestricted: one per-server switch (instance admin only) for Compose stacks
-- and the Docker options that hand a container host devices, GPUs, a runtime or
-- host-root equivalents (privileged, host PID, unconfined seccomp/AppArmor,
-- NET_ADMIN, SYS_ADMIN).
-- is_control_plane: the host the installer put the control plane on. Its agent's
-- proxy serves the dashboard (a system route to the control-plane container) and
-- there is at most one such server.
-- accept_workloads: whether services may be scheduled on the server. Turning it
-- off keeps what already runs there and refuses new placements and moves.
-- Build role (orthogonal to accept_workloads): "Deployments" is the default,
-- "Builds" is accept_workloads=false + build_enabled=true, "Both" sets both.
-- Build eligibility never reads accept_workloads.
CREATE TABLE servers (
    id                            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id                  uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    name                          text NOT NULL,
    public_ip                     text,
    base_domain                   text,
    acme_email                    text,
    status                        text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','online','offline','revoked')),
    agent_version                 text,
    protocol_version              integer,
    facts                         jsonb,
    proxy_status                  text,
    proxy_error                   text,
    last_seen_at                  timestamptz,
    created_at                    timestamptz NOT NULL DEFAULT now(),
    metrics_retention_days        integer NOT NULL DEFAULT 7  CHECK (metrics_retention_days BETWEEN 1 AND 90),
    disk_alert_threshold_percent  integer NOT NULL DEFAULT 80 CHECK (disk_alert_threshold_percent BETWEEN 50 AND 99),
    disk_alert_active             boolean NOT NULL DEFAULT false,
    traffic_analytics_enabled     boolean NOT NULL DEFAULT true,
    traffic_hourly_retention_days integer NOT NULL DEFAULT 7  CHECK (traffic_hourly_retention_days BETWEEN 2 AND 90),
    traffic_daily_retention_days  integer NOT NULL DEFAULT 90 CHECK (traffic_daily_retention_days BETWEEN 7 AND 365),
    drain_state                   text NOT NULL DEFAULT 'none' CHECK (drain_state IN ('none','draining','drained','failed')),
    drain_message                 text NOT NULL DEFAULT '',
    drain_started_at              timestamptz,
    proxy_generation              bigint  NOT NULL DEFAULT 0,
    proxy_applied_generation      bigint  NOT NULL DEFAULT 0,
    upstream_policy               jsonb   NOT NULL DEFAULT '{"allowLoopback":false,"allowLinkLocal":false,"allowHostGateway":true}'::jsonb,
    tailnet_mode                  text    NOT NULL DEFAULT 'off' CHECK (tailnet_mode IN ('off','managed','host')),
    tailnet_hostname              text,
    tailnet_magic_dns             boolean NOT NULL DEFAULT false,
    tailnet_applied_mode          text    NOT NULL DEFAULT 'off',
    tailnet_device_id             text,
    tailnet_status                jsonb,
    tailnet_status_at             timestamptz,
    applied_upstream_policy       jsonb   NOT NULL DEFAULT '{"allowLoopback":false,"allowLinkLocal":false,"allowHostGateway":true}'::jsonb,
    tailnet_applied_hostname      text,
    tailnet_applied_magic_dns     boolean NOT NULL DEFAULT false,
    applied_dns01                 jsonb   NOT NULL DEFAULT '{}'::jsonb,
    geo_enabled                   boolean NOT NULL DEFAULT true,
    geo_source                    text    NOT NULL DEFAULT 'dbip'    CHECK (geo_source IN ('dbip', 'maxmind')),
    geo_level                     text    NOT NULL DEFAULT 'country' CHECK (geo_level IN ('country', 'city')),
    geo_refresh_days              integer NOT NULL DEFAULT 7         CHECK (geo_refresh_days BETWEEN 1 AND 90),
    unrestricted                  boolean NOT NULL DEFAULT false,
    accept_workloads              boolean NOT NULL DEFAULT true,
    is_control_plane              boolean NOT NULL DEFAULT false,
    build_enabled                 boolean NOT NULL DEFAULT false,
    build_max_concurrent          integer NOT NULL DEFAULT 2 CHECK (build_max_concurrent BETWEEN 1 AND 16),
    build_allow_emulation         boolean NOT NULL DEFAULT false,
    build_allow_previews          boolean NOT NULL DEFAULT false,
    proxy_http_port               integer CHECK (proxy_http_port BETWEEN 1 AND 65535),
    proxy_https_port              integer CHECK (proxy_https_port BETWEEN 1 AND 65535),
    proxy_applied_http_port       integer CHECK (proxy_applied_http_port BETWEEN 1 AND 65535),
    proxy_applied_https_port      integer CHECK (proxy_applied_https_port BETWEEN 1 AND 65535)
);
CREATE UNIQUE INDEX servers_one_control_plane ON servers ((true)) WHERE is_control_plane AND status <> 'revoked';

CREATE TABLE agent_identities (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    server_id   uuid NOT NULL UNIQUE REFERENCES servers(id) ON DELETE CASCADE,
    public_key  bytea NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    revoked_at  timestamptz
);

CREATE TABLE enrollment_tokens (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id  uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    server_id     uuid NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
    token_hash    bytea NOT NULL UNIQUE,
    expires_at    timestamptz NOT NULL,
    used_at       timestamptz,
    revoked_at    timestamptz,
    created_by    uuid REFERENCES users(id) ON DELETE SET NULL,
    created_at    timestamptz NOT NULL DEFAULT now()
);

-- One row per drain run. moves is the ordered plan with per-move progress:
-- [{serviceId, serviceName, projectName, environmentName, sourceServerId,
--   targetServerId, targetServerName, stateful, status, deploymentId, message,
--   generationBefore, generationAfter}]. A server has at most one running drain.
CREATE TABLE server_drains (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    server_id        uuid NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
    workspace_id     uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    state            text NOT NULL DEFAULT 'running' CHECK (state IN ('running','completed','failed','canceled')),
    moves            jsonb NOT NULL,
    cancel_requested boolean NOT NULL DEFAULT false,
    message          text NOT NULL DEFAULT '',
    actor_type       text NOT NULL,
    actor_id         text NOT NULL DEFAULT '',
    actor_label      text NOT NULL DEFAULT '',
    created_at       timestamptz NOT NULL DEFAULT now(),
    finished_at      timestamptz
);
CREATE UNIQUE INDEX server_drains_one_running ON server_drains (server_id) WHERE state = 'running';
CREATE INDEX server_drains_server_idx ON server_drains (server_id, created_at DESC);

-- The host shell: an interactive shell on a server itself, opt-in per server
-- (off by default; no row means off). An Owner or Admin enables it; each session
-- is audited as server.shell.open / server.shell.close in audit_events, and its
-- I/O is never stored.
CREATE TABLE server_host_shell (
    server_id  uuid PRIMARY KEY REFERENCES servers(id) ON DELETE CASCADE,
    enabled    boolean NOT NULL DEFAULT false,
    updated_by uuid REFERENCES users(id) ON DELETE SET NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- Scheduled Docker cleanup of a server. A server without a settings row has the
-- defaults: cleanup on, stopped containers NebulaCtrl does not manage kept.
-- Volumes are never part of the schedule (see server_cleanup_runs.mode).
-- The schedule is editable: by default daily at 04:00 UTC, unused images after
-- 72 hours, build cache after 7 days, and the emergency run at the server's disk
-- alert threshold (a NULL disk_trigger_percent follows
-- servers.disk_alert_threshold_percent). schedule_changed_at lets a new schedule
-- take effect from the moment it is saved instead of catching up on slots of the
-- old one.
CREATE TABLE server_cleanup_settings (
    server_id                   uuid PRIMARY KEY REFERENCES servers(id) ON DELETE CASCADE,
    workspace_id                uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    enabled                     boolean NOT NULL DEFAULT true,
    remove_unmanaged_containers boolean NOT NULL DEFAULT false,
    updated_at                  timestamptz NOT NULL DEFAULT now(),
    schedule                    text        NOT NULL DEFAULT '0 4 * * *',
    schedule_changed_at         timestamptz NOT NULL DEFAULT now(),
    unused_image_min_age_hours  integer     NOT NULL DEFAULT 72  CHECK (unused_image_min_age_hours BETWEEN 1 AND 8760),
    build_cache_min_age_days    integer     NOT NULL DEFAULT 7   CHECK (build_cache_min_age_days BETWEEN 1 AND 365),
    disk_trigger_percent        integer     CHECK (disk_trigger_percent BETWEEN 50 AND 99)
);

-- One row per cleanup run: scheduled (daily 04:00 UTC), emergency (disk above the
-- server's alert threshold), manual ("Run cleanup now") or an explicit volume
-- cleanup (mode = 'volumes'). The report is the agent's CleanupResult.
CREATE TABLE server_cleanup_runs (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    server_id       uuid NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
    workspace_id    uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    mode            text NOT NULL DEFAULT 'standard' CHECK (mode IN ('standard','volumes')),
    trigger         text NOT NULL CHECK (trigger IN ('scheduled','emergency','manual')),
    status          text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','succeeded','failed')),
    command_id      uuid,
    requested_by    uuid REFERENCES users(id) ON DELETE SET NULL,
    reclaimed_bytes bigint NOT NULL DEFAULT 0,
    items           integer NOT NULL DEFAULT 0,
    report          jsonb,
    error           text NOT NULL DEFAULT '',
    created_at      timestamptz NOT NULL DEFAULT now(),
    finished_at     timestamptz
);
CREATE INDEX server_cleanup_runs_server_idx ON server_cleanup_runs (server_id, created_at DESC);
CREATE INDEX server_cleanup_runs_command_idx ON server_cleanup_runs (command_id) WHERE command_id IS NOT NULL;

-- Host hardening: a Lynis audit of each server and a curated set of safe fixes.
-- The weekly re-audit is on by default (no row means on); a server whose agent
-- has no Lynis simply records a skipped run.
CREATE TABLE server_hardening_settings (
    server_id    uuid PRIMARY KEY REFERENCES servers(id) ON DELETE CASCADE,
    workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    weekly       boolean NOT NULL DEFAULT true,
    updated_at   timestamptz NOT NULL DEFAULT now()
);

-- What the newest audit found on a server. report is the agent's parsed Lynis
-- report (HardeningReport), fixes the state of every catalog fix
-- (HardeningFixState list). first_index is the hardening index of the first audit,
-- kept so the page can show where the server started.
CREATE TABLE server_hardening_state (
    server_id        uuid PRIMARY KEY REFERENCES servers(id) ON DELETE CASCADE,
    workspace_id     uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    lynis_installed  boolean NOT NULL DEFAULT false,
    lynis_version    text NOT NULL DEFAULT '',
    mode             text NOT NULL DEFAULT '',
    first_index      integer,
    first_audited_at timestamptz,
    hardening_index  integer,
    audited_at       timestamptz,
    report           jsonb,
    fixes            jsonb,
    reboot_required  boolean NOT NULL DEFAULT false,
    updated_at       timestamptz NOT NULL DEFAULT now()
);

-- One row per audit, apply or rollback: scheduled (weekly) or manual. outcomes
-- is what an apply or rollback did per fix (HardeningFixOutcome list).
CREATE TABLE server_hardening_runs (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    server_id       uuid NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
    workspace_id    uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    action          text NOT NULL CHECK (action IN ('audit','apply','rollback')),
    trigger         text NOT NULL CHECK (trigger IN ('scheduled','manual')),
    status          text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','succeeded','failed','skipped')),
    command_id      uuid,
    requested_by    uuid REFERENCES users(id) ON DELETE SET NULL,
    fix_ids         text[] NOT NULL DEFAULT '{}',
    index_before    integer,
    hardening_index integer,
    lynis_version   text NOT NULL DEFAULT '',
    outcomes        jsonb,
    message         text NOT NULL DEFAULT '',
    error           text NOT NULL DEFAULT '',
    created_at      timestamptz NOT NULL DEFAULT now(),
    finished_at     timestamptz
);
CREATE INDEX server_hardening_runs_server_idx ON server_hardening_runs (server_id, created_at DESC);
CREATE INDEX server_hardening_runs_command_idx ON server_hardening_runs (command_id) WHERE command_id IS NOT NULL;

-- Server patching: what the host's package manager says is pending, and the
-- history of checks, upgrades and reboots NebulaCtrl ran on it.
--
-- What the newest check found. packages is the agent's list of pending upgrades
-- (PatchPackage list, capped), reboot_packages the packages that asked for the
-- reboot. boot_id is the kernel's boot id when the check ran; a reboot is proven
-- by it changing.
CREATE TABLE server_patch_state (
    server_id        uuid PRIMARY KEY REFERENCES servers(id) ON DELETE CASCADE,
    workspace_id     uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    manager          text NOT NULL DEFAULT '',
    supported        boolean NOT NULL DEFAULT false,
    updates          integer NOT NULL DEFAULT 0,
    security_updates integer NOT NULL DEFAULT 0,
    packages         jsonb,
    reboot_required  boolean NOT NULL DEFAULT false,
    reboot_packages  text[] NOT NULL DEFAULT '{}',
    boot_id          text NOT NULL DEFAULT '',
    message          text NOT NULL DEFAULT '',
    checked_at       timestamptz,
    updated_at       timestamptz NOT NULL DEFAULT now()
);

-- One row per check, upgrade or reboot, scheduled or manual. A reboot run is
-- 'rebooting' from the moment the host accepted it until the agent is back on a
-- different boot id (verify_command_id is the check that proves it).
CREATE TABLE server_patch_runs (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    server_id         uuid NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
    workspace_id      uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    action            text NOT NULL CHECK (action IN ('check','apply','reboot')),
    trigger           text NOT NULL CHECK (trigger IN ('scheduled','manual')),
    status            text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','rebooting','succeeded','failed')),
    command_id        uuid,
    verify_command_id uuid,
    verify_sent_at    timestamptz,
    requested_by      uuid REFERENCES users(id) ON DELETE SET NULL,
    security_only     boolean NOT NULL DEFAULT false,
    boot_id_before    text NOT NULL DEFAULT '',
    rebooting_since   timestamptz,
    updates_before    integer,
    updates_after     integer,
    exit_code         integer,
    log               text NOT NULL DEFAULT '',
    message           text NOT NULL DEFAULT '',
    error             text NOT NULL DEFAULT '',
    created_at        timestamptz NOT NULL DEFAULT now(),
    finished_at       timestamptz
);
CREATE INDEX server_patch_runs_server_idx ON server_patch_runs (server_id, created_at DESC);
CREATE INDEX server_patch_runs_command_idx ON server_patch_runs (command_id) WHERE command_id IS NOT NULL;
CREATE INDEX server_patch_runs_verify_idx ON server_patch_runs (verify_command_id) WHERE verify_command_id IS NOT NULL;

-- The latest agent upgrade of each server. state:
--   queued      the agent_upgrade command is on its way or running
--   restarting  the new binary is installed; the agent restarts when idle
--   succeeded   the agent reconnected with the target version
--   failed      refused, rolled back, expired or the agent came back unchanged
-- A row is replaced by the next upgrade; the history is the audit log.
CREATE TABLE server_agent_upgrades (
    server_id      uuid PRIMARY KEY REFERENCES servers(id) ON DELETE CASCADE,
    workspace_id   uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    state          text NOT NULL CHECK (state IN ('queued','restarting','succeeded','failed')),
    trigger        text NOT NULL CHECK (trigger IN ('auto','manual')),
    from_version   text NOT NULL DEFAULT '',
    target_version text NOT NULL,
    message        text NOT NULL DEFAULT '',
    command_id     uuid,
    requested_by   uuid REFERENCES users(id) ON DELETE SET NULL,
    requested_at   timestamptz NOT NULL DEFAULT now(),
    finished_at    timestamptz,
    updated_at     timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX server_agent_upgrades_command_idx ON server_agent_upgrades (command_id) WHERE command_id IS NOT NULL;

-- ============================================================================
-- Projects and environments
-- ============================================================================

-- system: the hidden "NebulaCtrl platform" project of a team (platform services).
-- build_location / build_server_id / registry_id override the team's build
-- settings (team_build_settings). registry_id gets its foreign key once
-- registries exist (below).
CREATE TABLE projects (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id     uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    name             text NOT NULL,
    slug             text NOT NULL,
    description      text NOT NULL DEFAULT '',
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    system           boolean NOT NULL DEFAULT false,
    build_location   text NOT NULL DEFAULT 'inherit' CHECK (build_location IN ('inherit','target','build_server')),
    build_server_id  uuid REFERENCES servers(id) ON DELETE SET NULL,
    registry_id      uuid,
    UNIQUE (workspace_id, slug)
);
CREATE UNIQUE INDEX projects_one_system_per_workspace ON projects (workspace_id) WHERE system;

-- A pull-request preview is a real environment (kind = 'preview') cloned from
-- the project's base environment; the PR it belongs to lives in `previews`.
-- connection_id points at the git connection without a foreign key: removing a
-- connection must not delete running previews, the idle sweep and the PR-closed
-- event clean them up.
CREATE TABLE environments (
    id                    uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id            uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    name                  text NOT NULL,
    slug                  text NOT NULL,
    is_production         boolean NOT NULL DEFAULT false,
    created_at            timestamptz NOT NULL DEFAULT now(),
    kind                  text NOT NULL DEFAULT 'standard' CHECK (kind IN ('standard','preview')),
    parent_environment_id uuid REFERENCES environments(id),
    pr_number             integer,
    pr_title              text NOT NULL DEFAULT '',
    connection_id         uuid,
    UNIQUE (project_id, slug)
);
CREATE INDEX environments_parent_idx ON environments (parent_environment_id) WHERE parent_environment_id IS NOT NULL;

CREATE TABLE canvas_layouts (
    environment_id  uuid PRIMARY KEY REFERENCES environments(id) ON DELETE CASCADE,
    layout          jsonb NOT NULL DEFAULT '{}'::jsonb,
    version         bigint NOT NULL DEFAULT 1,
    updated_at      timestamptz NOT NULL DEFAULT now()
);

-- Web terminal and file browser settings, per project. The terminal itself
-- stores nothing (sessions are ephemeral and their I/O is never recorded); its
-- audit trail is terminal.open / terminal.close rows in audit_events.
--
-- allow_production_file_writes: the file browser is read-only in production
-- environments unless an admin turns this on for the project.
CREATE TABLE project_terminal_settings (
    project_id                   uuid PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE,
    allow_production_file_writes boolean NOT NULL DEFAULT false,
    updated_at                   timestamptz NOT NULL DEFAULT now()
);

-- Platform services: managed services NebulaCtrl runs for its own use ("Set up for
-- me"): S3 storage for backup destinations, Loki (and Grafana) for log drains,
-- Headscale for the tailnet integration, an OpenID Connect provider for SSO and
-- an OCI registry. They live in one system project per team, hidden from the
-- projects list.
--
-- One row per platform service of a team. The row is the state machine of its
-- provisioning job: configure -> deploy -> await -> wire -> done. The services
-- themselves are ordinary services of the system project (service_ids).
--   secrets_enc  sealed JSON of the credentials NebulaCtrl generated (never returned)
--   options      non-secret choices (bucket, retention, users of the identity provider, ...)
--   wiring       ids of what the service was wired into (backup destination, log drain,
--                tailnet integration, sign-in provider) so removal can unwire it
CREATE TABLE platform_services (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id   uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    kind           text NOT NULL CHECK (kind IN ('s3','loki','grafana','headscale','idp','registry')),
    status         text NOT NULL DEFAULT 'provisioning' CHECK (status IN ('provisioning','ready','failed','removing')),
    step           text NOT NULL DEFAULT 'configure' CHECK (step IN ('configure','deploy','await','wire','done')),
    status_message text NOT NULL DEFAULT '',
    environment_id uuid REFERENCES environments(id) ON DELETE SET NULL,
    service_ids    uuid[] NOT NULL DEFAULT '{}',
    deployment_ids uuid[] NOT NULL DEFAULT '{}',
    hostname       text NOT NULL DEFAULT '',
    public_url     text NOT NULL DEFAULT '',
    options        jsonb NOT NULL DEFAULT '{}',
    secrets_enc    bytea,
    wiring         jsonb NOT NULL DEFAULT '{}',
    created_by     uuid REFERENCES users(id) ON DELETE SET NULL,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now(),
    UNIQUE (workspace_id, kind)
);

-- OCI registries: bring-your-own (external, sealed basic-auth credentials) or the
-- managed "Set up for me" registry (a platform service; credentials are minted
-- per command).
CREATE TABLE registries (
    id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id        uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    name                text NOT NULL,
    kind                text NOT NULL CHECK (kind IN ('external','managed')),
    host                text NOT NULL,              -- registry.example.com[:port], no scheme
    namespace           text NOT NULL DEFAULT '',
    push_auth_enc       bytea,                      -- sealed {username,password}; NULL for managed
    pull_auth_enc       bytea,                      -- optional; NULL => push credential
    platform_service_id uuid REFERENCES platform_services(id) ON DELETE SET NULL,
    manage_retention    boolean NOT NULL DEFAULT false,  -- managed: true
    last_check          jsonb,                      -- {serverId: {ok, push, pull, error, at}}
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now(),
    UNIQUE (workspace_id, name)
);

ALTER TABLE projects
    ADD CONSTRAINT projects_registry_id_fkey FOREIGN KEY (registry_id) REFERENCES registries(id) ON DELETE SET NULL;

-- Build settings: the team default (one row per workspace, created lazily).
-- Projects override it.
CREATE TABLE team_build_settings (
    workspace_id   uuid PRIMARY KEY REFERENCES workspaces(id) ON DELETE CASCADE,
    build_location text NOT NULL DEFAULT 'target' CHECK (build_location IN ('target','build_server')),
    registry_id    uuid REFERENCES registries(id) ON DELETE SET NULL,
    updated_at     timestamptz NOT NULL DEFAULT now()
);

-- ============================================================================
-- Services
-- ============================================================================

-- A template install: the stack of services created from one catalog entry.
-- Every created service is an ordinary service; this row only remembers where
-- it came from so the stack can be shown as one canvas group and deleted as a
-- unit. inputs holds the NON-secret install inputs.
--
-- Template versioning: an install records the template revision, the version
-- of the app it pinned (the tag of the main image, shown as "v3.4.1") and a
-- snapshot of the template as installed. The snapshot is what an update diffs
-- against: the catalog is embedded in the server binary, so the older revision
-- is no longer available after an upgrade. It holds the template text with its
-- ${input:KEY} placeholders, never an input value, so it carries no secret.
-- Installs without a snapshot (NULL) cannot be updated in place.
CREATE TABLE template_instances (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id      uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    environment_id    uuid NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
    template_id       text NOT NULL,
    template_revision integer NOT NULL,
    name              text NOT NULL,
    group_id          text NOT NULL,
    inputs            jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_by_type   text NOT NULL DEFAULT 'user',
    created_by        text NOT NULL DEFAULT '',
    created_at        timestamptz NOT NULL DEFAULT now(),
    deleted_at        timestamptz,
    template_version  text        NOT NULL DEFAULT '',
    template_snapshot jsonb,
    updated_at        timestamptz,
    updated_by_type   text        NOT NULL DEFAULT '',
    updated_by        text        NOT NULL DEFAULT ''
);
CREATE INDEX template_instances_env_idx ON template_instances (environment_id) WHERE deleted_at IS NULL;

-- A service. kind 'route' services (source_type 'external') are proxy routes; a
-- 'stack' (source_type 'compose') is a native Compose stack whose compose file
-- lives in services.config.stack and which the agent runs through
-- `docker compose`.
--
-- desired_state: running | stopped; set by stop, restored by restart and by any
-- deployment.
--
-- Replicas, autoscaling and maintenance mode are operational settings: they are
-- not part of the release snapshot and never produce staged changes.
--   autoscaling      {enabled, min, max, targetCpuPercent}
--   last_scale_event {at, from, to, reason, actor}
--   scale_blocked_reason  why the autoscaler (or a scale command) did not scale
-- Maintenance mode: while enabled, the proxy answers 503 with a maintenance page
-- on every domain of the service. It never bumps services.generation, is not
-- part of a release and is never rolled back. The text is kept while disabled so
-- the next activation can start from it.
--
-- suggested_env: keys found in .env.example-style files of a git source (reported
-- by the agent after checkout): {commit, files:[{path, vars:[{key,value,comment}]}]}.
--
-- current_deployment_id gets its foreign key once deployments exist (below).
CREATE TABLE services (
    id                     uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    environment_id         uuid NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
    name                   text NOT NULL,
    slug                   text NOT NULL,
    kind                   text NOT NULL DEFAULT 'app' CHECK (kind IN ('app','worker','database','route','stack')),
    source_type            text NOT NULL CHECK (source_type IN ('image','git','external','compose')),
    config                 jsonb NOT NULL DEFAULT '{}'::jsonb,
    git_token_enc          bytea,
    registry_auth_enc      bytea,
    generation             bigint NOT NULL DEFAULT 1,
    applied_generation     bigint NOT NULL DEFAULT 0,
    server_id              uuid REFERENCES servers(id) ON DELETE SET NULL,
    current_deployment_id  uuid,
    observed               jsonb,
    observed_at            timestamptz,
    created_at             timestamptz NOT NULL DEFAULT now(),
    updated_at             timestamptz NOT NULL DEFAULT now(),
    deleted_at             timestamptz,
    desired_state          text NOT NULL DEFAULT 'running' CHECK (desired_state IN ('running','stopped')),
    replicas               integer NOT NULL DEFAULT 1 CHECK (replicas BETWEEN 1 AND 10),
    autoscaling            jsonb   NOT NULL DEFAULT '{"enabled":false,"min":1,"max":3,"targetCpuPercent":70}'::jsonb,
    last_scale_at          timestamptz,
    last_scale_event       jsonb,
    scale_blocked_reason   text,
    suggested_env          jsonb,
    template_instance_id   uuid REFERENCES template_instances(id) ON DELETE SET NULL,
    template_key           text,
    maintenance_enabled    boolean NOT NULL DEFAULT false,
    maintenance_title      text NOT NULL DEFAULT '',
    maintenance_message    text NOT NULL DEFAULT '',
    maintenance_eta        timestamptz,
    maintenance_since      timestamptz
);
CREATE UNIQUE INDEX services_env_slug_uniq ON services (environment_id, slug) WHERE deleted_at IS NULL;
CREATE INDEX services_server_idx ON services (server_id) WHERE deleted_at IS NULL;
CREATE INDEX services_template_instance_idx ON services (template_instance_id) WHERE template_instance_id IS NOT NULL;

-- Service variables: scope, provenance, generated-secret templates and versions.
--   source        manual | generated | imported | suggested | template
--   template_enc  authored template when it contained generator calls
--   gen_calls     [{fn,args}] descriptors of those calls (no literals)
CREATE TABLE service_variables (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    service_id        uuid NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    key               text NOT NULL,
    value_enc         bytea NOT NULL,
    is_secret         boolean NOT NULL DEFAULT false,
    updated_at        timestamptz NOT NULL DEFAULT now(),
    scope             text    NOT NULL DEFAULT 'runtime' CHECK (scope IN ('runtime','build','both')),
    source            text    NOT NULL DEFAULT 'manual',
    template_enc      bytea,
    gen_calls         jsonb,
    version           integer NOT NULL DEFAULT 1,
    generated_at      timestamptz,
    rotate_every_days integer CHECK (rotate_every_days IS NULL OR rotate_every_days > 0),
    comment           text    NOT NULL DEFAULT '',
    created_at        timestamptz NOT NULL DEFAULT now(),
    UNIQUE (service_id, key)
);

-- Shared variables in three layers. A team row has project_id and environment_id
-- NULL and is referenced as ${{team.KEY}}; a project row has environment_id NULL;
-- a row with an environment overrides the project row for that environment.
CREATE TABLE shared_variables (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id   uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    project_id     uuid REFERENCES projects(id) ON DELETE CASCADE,
    environment_id uuid REFERENCES environments(id) ON DELETE CASCADE,
    key            text NOT NULL,
    value_enc      bytea NOT NULL,
    is_secret      boolean NOT NULL DEFAULT false,
    source         text NOT NULL DEFAULT 'manual',
    template_enc   bytea,
    gen_calls      jsonb,
    version        integer NOT NULL DEFAULT 1,
    generated_at   timestamptz,
    comment        text NOT NULL DEFAULT '',
    updated_at     timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT shared_variables_layer CHECK (environment_id IS NULL OR project_id IS NOT NULL)
);
CREATE UNIQUE INDEX shared_variables_project_key ON shared_variables (project_id, key) WHERE environment_id IS NULL;
CREATE UNIQUE INDEX shared_variables_env_key ON shared_variables (environment_id, key) WHERE environment_id IS NOT NULL;
CREATE UNIQUE INDEX shared_variables_team_key ON shared_variables (workspace_id, key) WHERE project_id IS NULL;

-- Version history. Secrets keep no value, only an HMAC fingerprint.
CREATE TABLE variable_versions (
    id           bigserial PRIMARY KEY,
    workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    project_id   uuid REFERENCES projects(id) ON DELETE CASCADE,
    owner_kind   text NOT NULL CHECK (owner_kind IN ('service','environment','project','team')),
    owner_id     uuid NOT NULL,
    key          text NOT NULL,
    version      integer NOT NULL,
    op           text NOT NULL CHECK (op IN ('set','delete','rotate','restore')),
    is_secret    boolean NOT NULL,
    source       text NOT NULL,
    scope        text NOT NULL DEFAULT 'runtime',
    value_enc    bytea,                 -- non-secret values only
    fingerprint  text NOT NULL,
    gen_calls    jsonb,
    actor_type   text NOT NULL,
    actor_id     text,
    release_id   uuid,
    created_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX variable_versions_owner_key ON variable_versions (owner_kind, owner_id, key, id DESC);
CREATE INDEX variable_versions_created ON variable_versions (created_at);

-- External secret managers. A reference ${{vault:<name>/<path>#<key>}} names one
-- row; the value it points to is fetched at deploy time and never stored.
-- config holds the non-secret connection settings (address, mount, namespace,
-- auth method, project and environment ...); credentials_enc is the sealed JSON
-- of the credentials (token, AppRole secret id, client secret) and is never
-- returned by the API.
--
-- Scoping: an integration is available to the whole team ('team', the default)
-- or only to the projects and environments listed in secret_integration_scopes
-- ('selected').
CREATE TABLE secret_integrations (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id    uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    name            text NOT NULL CHECK (name ~ '^[a-z0-9][a-z0-9-]{0,62}$'),
    kind            text NOT NULL CHECK (kind IN ('vault','infisical','doppler')),
    config          jsonb NOT NULL DEFAULT '{}',
    credentials_enc bytea NOT NULL,
    last_test_at    timestamptz,
    last_test_ok    boolean,
    last_test_error text NOT NULL DEFAULT '',
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    scope           text NOT NULL DEFAULT 'team' CHECK (scope IN ('team','selected')),
    UNIQUE (workspace_id, name)
);

-- A grant with a NULL environment_id covers every environment of the project,
-- including environments created later (previews); a grant with an environment
-- covers that environment only. 'selected' with no grants is available nowhere.
CREATE TABLE secret_integration_scopes (
    integration_id uuid NOT NULL REFERENCES secret_integrations(id) ON DELETE CASCADE,
    project_id     uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    environment_id uuid REFERENCES environments(id) ON DELETE CASCADE,
    created_at     timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX secret_integration_scopes_unique
    ON secret_integration_scopes (integration_id, project_id, COALESCE(environment_id, '00000000-0000-0000-0000-000000000000'::uuid));
CREATE INDEX secret_integration_scopes_project ON secret_integration_scopes (project_id);

CREATE TABLE service_links (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    environment_id   uuid NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
    from_service_id  uuid NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    to_service_id    uuid NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    created_at       timestamptz NOT NULL DEFAULT now(),
    UNIQUE (from_service_id, to_service_id)
);

-- A volume row can point at a Docker volume other than the one derived from
-- its name (nc-vol-<serviceId8>-<name>): a restore into a NEW volume swaps the
-- row to the restored volume (staged until the next deploy). restore_label
-- says why ("restored from Jan 12, 03:30") and is shown as the staged change.
CREATE TABLE volumes (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    service_id    uuid NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    name          text NOT NULL,
    mount_path    text NOT NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    docker_name   text NOT NULL DEFAULT '',
    restore_label text NOT NULL DEFAULT '',
    UNIQUE (service_id, name)
);

-- Docker volumes a restore swapped out. They are unmounted but kept (their data
-- is the user's rollback path) until the user deletes them.
CREATE TABLE retired_volumes (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    service_id  uuid NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    volume_name text NOT NULL,
    docker_name text NOT NULL,
    reason      text NOT NULL DEFAULT '',
    created_at  timestamptz NOT NULL DEFAULT now(),
    UNIQUE (service_id, docker_name)
);

-- SSH deploy keys for git-source services. A service clones over SSH with one
-- key pair: generated by the control plane (Ed25519) or uploaded. The private
-- key is stored encrypted (secretbox), is write-only and travels to the agent as
-- a send-time secret; only the public key and its fingerprint are ever returned.
CREATE TABLE service_ssh_keys (
    service_id      uuid PRIMARY KEY REFERENCES services(id) ON DELETE CASCADE,
    private_key_enc bytea NOT NULL,
    public_key      text NOT NULL,
    fingerprint     text NOT NULL,
    source          text NOT NULL CHECK (source IN ('generated', 'uploaded')),
    created_at      timestamptz NOT NULL DEFAULT now(),
    rotated_at      timestamptz
);

-- Pinned SSH host keys (known_hosts), per service and "host:port". The first
-- fetch records the key (trust on first use); afterwards a different key fails
-- the clone until a user accepts the new one. mismatch_* remembers the last
-- rejected key so the UI can show it next to the "Accept new host key" action.
CREATE TABLE service_ssh_hosts (
    service_id           uuid NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    host                 text NOT NULL,
    key_type             text NOT NULL,
    public_key           text NOT NULL,
    fingerprint          text NOT NULL,
    first_seen_at        timestamptz NOT NULL DEFAULT now(),
    accepted_by          uuid REFERENCES users(id) ON DELETE SET NULL,
    mismatch_fingerprint text,
    mismatch_at          timestamptz,
    PRIMARY KEY (service_id, host)
);

-- A per-service deploy hook URL (POST /hooks/deploy/<serviceId>/<secret>) for CI
-- pipelines and container registries. The secret is shown once and only its
-- SHA-256 lives here; a hook with enabled = false has no secret at all.
CREATE TABLE deploy_hooks (
    service_id     uuid PRIMARY KEY REFERENCES services(id) ON DELETE CASCADE,
    enabled        boolean NOT NULL DEFAULT false,
    secret_hash    bytea CHECK (secret_hash IS NULL OR octet_length(secret_hash) = 32),
    -- When the current secret was created (enable or rotate).
    secret_set_at  timestamptz,
    last_called_at timestamptz,
    -- deploying | queued | ignored (what the last authenticated call did).
    last_outcome   text NOT NULL DEFAULT '',
    -- The newest call that arrived while a deployment of the service was
    -- running; the sweep deploys it when that deployment ends:
    -- {"imageRef": "repo:tag" | "", "source": "...", "queuedAt": "..."}.
    pending        jsonb,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now(),
    CHECK (NOT enabled OR secret_hash IS NOT NULL)
);

-- ============================================================================
-- Deployments and agent commands
-- ============================================================================

CREATE TABLE releases (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    service_id  uuid NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    source      jsonb NOT NULL,
    config      jsonb NOT NULL,
    env_enc     bytea,
    image_ref   text NOT NULL DEFAULT '',
    created_at  timestamptz NOT NULL DEFAULT now()
);

-- message: optional free-text note given when a deployment is requested (max 200
-- chars). build_info: how a git deployment was built, as reported by the agent
-- when it finished:
--   {method: dockerfile|railpack|starter, reason, starter?, dockerfile?,
--    railpackVersion?, providers?, packages?, steps?, startCommand?, builder?}
-- NULL for image deployments and for builds that never started.
-- commit_*: shown on deployments (git_push deployments know it up front; other
-- git deployments get it when the release pins the commit).
-- Cancel at any step: the person who asked and when. The deployment keeps its
-- status until the agent reports `canceled` (or the deploy finishes first, in
-- which case the request is moot). canceled_at_step is the agent's step when the
-- abort took effect.
-- build_server_id: the server that built the release for this deployment (NULL:
-- built on the target, or no build).
-- no_cache: "Deploy without cache": this one deployment builds with --no-cache
-- --pull and never reuses an image a build server already pushed.
-- The 'deploy_hook' trigger marks deployments started by a deploy hook.
CREATE TABLE deployments (
    id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    service_id          uuid NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    release_id          uuid REFERENCES releases(id) ON DELETE SET NULL,
    server_id           uuid REFERENCES servers(id) ON DELETE SET NULL,
    trigger             text NOT NULL CHECK (trigger IN ('manual','redeploy','rollback','staged','reconcile','drain','git_push','preview','cron','deploy_hook')),
    actor_type          text NOT NULL,
    actor_id            text NOT NULL DEFAULT '',
    status              text NOT NULL DEFAULT 'created',
    strategy            text NOT NULL DEFAULT 'recreate',
    status_message      text NOT NULL DEFAULT '',
    rolled_back_from    uuid REFERENCES deployments(id) ON DELETE SET NULL,
    generation          bigint NOT NULL DEFAULT 0,
    created_at          timestamptz NOT NULL DEFAULT now(),
    started_at          timestamptz,
    finished_at         timestamptz,
    message             text NOT NULL DEFAULT '',
    build_info          jsonb,
    commit_sha          text NOT NULL DEFAULT '',
    commit_message      text NOT NULL DEFAULT '',
    commit_author       text NOT NULL DEFAULT '',
    commit_branch       text NOT NULL DEFAULT '',
    cancel_requested_at timestamptz,
    cancel_requested_by text NOT NULL DEFAULT '',
    canceled_at_step    text NOT NULL DEFAULT '',
    build_server_id     uuid REFERENCES servers(id) ON DELETE SET NULL,
    no_cache            boolean NOT NULL DEFAULT false
);
CREATE UNIQUE INDEX deployments_one_active ON deployments (service_id)
    WHERE status IN ('created','queued','preparing','building','releasing','verifying');
CREATE INDEX deployments_service_created_idx ON deployments (service_id, created_at DESC);
-- Environment-wide deployment list (newest first).
CREATE INDEX deployments_created_idx ON deployments (created_at DESC, id DESC);

ALTER TABLE services ADD CONSTRAINT services_current_deployment_fk
    FOREIGN KEY (current_deployment_id) REFERENCES deployments(id) ON DELETE SET NULL;

CREATE TABLE deployment_events (
    id             bigserial PRIMARY KEY,
    deployment_id  uuid NOT NULL REFERENCES deployments(id) ON DELETE CASCADE,
    step           text NOT NULL,
    status         text NOT NULL,
    message        text NOT NULL DEFAULT '',
    created_at     timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX deployment_events_dep_idx ON deployment_events (deployment_id, id);
-- Failed-step lookups.
CREATE INDEX deployment_events_failed_idx ON deployment_events (deployment_id, id DESC) WHERE status = 'failed';

CREATE TABLE deployment_logs (
    id             bigserial PRIMARY KEY,
    deployment_id  uuid NOT NULL REFERENCES deployments(id) ON DELETE CASCADE,
    stream         text NOT NULL,
    line           text NOT NULL,
    created_at     timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX deployment_logs_dep_idx ON deployment_logs (deployment_id, id);

CREATE TABLE agent_commands (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    server_id        uuid NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
    deployment_id    uuid REFERENCES deployments(id) ON DELETE SET NULL,
    kind             text NOT NULL,
    payload          jsonb NOT NULL,
    idempotency_key  text NOT NULL UNIQUE,
    status           text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','sent','acked','succeeded','failed','expired','canceled')),
    result           jsonb,
    error            text NOT NULL DEFAULT '',
    expires_at       timestamptz NOT NULL,
    sent_at          timestamptz,
    acked_at         timestamptz,
    finished_at      timestamptz,
    created_at       timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX agent_commands_server_status_idx ON agent_commands (server_id, status);

-- One row per image pushed to a registry. The repository is recorded per image
-- so renames never break rollbacks.
CREATE TABLE registry_images (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    registry_id uuid NOT NULL REFERENCES registries(id) ON DELETE CASCADE,
    service_id  uuid REFERENCES services(id) ON DELETE SET NULL,
    release_id  uuid REFERENCES releases(id) ON DELETE SET NULL,
    repository  text NOT NULL,
    digest      text NOT NULL,
    platform    text NOT NULL,
    build_hash  text NOT NULL,
    size_bytes  bigint,
    state       text NOT NULL DEFAULT 'live' CHECK (state IN ('live','expired','deleted')),
    pushed_at   timestamptz NOT NULL DEFAULT now(),
    UNIQUE (registry_id, repository, digest)
);
CREATE INDEX registry_images_reuse ON registry_images (service_id, build_hash, platform) WHERE state = 'live';

-- ============================================================================
-- Tasks
-- ============================================================================

-- Tasks: scheduled (cron) commands, one-off commands and their run history.
-- A task run is an agent command (run_task): a throwaway container of the
-- service's current image with the service's environment. Output is stored
-- redacted; the control plane never persists variable values in the command.
-- The pre-deploy command is a service config field (services.config), not a
-- row here: it is a step of the deploy command.

-- A recurring command of a service. schedule is a 5-field cron expression or
-- @macro evaluated in timezone (an IANA name, validated by the application via
-- cronexpr.LoadLocation); next_run_at is the next due instant (absolute).
-- concurrency "forbid" skips a tick while the previous run of the task is still
-- active.
CREATE TABLE service_tasks (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    service_id      uuid NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    name            text NOT NULL,
    schedule        text NOT NULL,
    command         jsonb NOT NULL,
    enabled         boolean NOT NULL DEFAULT true,
    timeout_seconds integer NOT NULL DEFAULT 1800 CHECK (timeout_seconds BETWEEN 1 AND 86400),
    concurrency     text NOT NULL DEFAULT 'forbid' CHECK (concurrency IN ('forbid','allow')),
    next_run_at     timestamptz,
    last_run_at     timestamptz,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    timezone        text NOT NULL DEFAULT 'UTC',
    UNIQUE (service_id, name)
);
CREATE INDEX service_tasks_due_idx ON service_tasks (next_run_at) WHERE enabled;

-- One execution. task_id is null for one-off commands. source says what
-- started the run: cron, manual (Run now), one_off (run command) or a feature
-- name such as preview_seed. status: queued (sent, not yet running), running
-- (acked), succeeded, failed (non-zero exit, could not start, not delivered,
-- lost) or timed_out; skipped is a cron tick that was not run (overlap,
-- server offline, service not deployed) and says why in error.
CREATE TABLE task_runs (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id    uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    service_id      uuid NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    task_id         uuid REFERENCES service_tasks(id) ON DELETE CASCADE,
    name            text NOT NULL,
    source          text NOT NULL,
    command         jsonb NOT NULL,
    status          text NOT NULL DEFAULT 'queued' CHECK (status IN ('queued','running','succeeded','failed','timed_out','skipped')),
    exit_code       integer,
    error           text NOT NULL DEFAULT '',
    timeout_seconds integer NOT NULL DEFAULT 1800,
    server_id       uuid REFERENCES servers(id) ON DELETE SET NULL,
    command_id      uuid REFERENCES agent_commands(id) ON DELETE SET NULL,
    actor_label     text NOT NULL DEFAULT '',
    scheduled_for   timestamptz,
    log_lines       integer NOT NULL DEFAULT 0,
    created_at      timestamptz NOT NULL DEFAULT now(),
    started_at      timestamptz,
    finished_at     timestamptz
);
CREATE INDEX task_runs_service_idx ON task_runs (service_id, created_at DESC);
CREATE INDEX task_runs_task_idx ON task_runs (task_id, created_at DESC) WHERE task_id IS NOT NULL;
CREATE INDEX task_runs_active_idx ON task_runs (created_at) WHERE status IN ('queued','running');
CREATE INDEX task_runs_command_idx ON task_runs (command_id) WHERE command_id IS NOT NULL;

-- Output lines of a run, already redacted. Capped per run by the control plane.
CREATE TABLE task_run_logs (
    id         bigserial PRIMARY KEY,
    run_id     uuid NOT NULL REFERENCES task_runs(id) ON DELETE CASCADE,
    stream     text NOT NULL DEFAULT 'task',
    line       text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX task_run_logs_run_idx ON task_run_logs (run_id, id);

-- ============================================================================
-- Git providers, auto-deploy and pull-request previews
-- ============================================================================

-- Git provider connections: one set of credentials per provider account or
-- instance. Secrets (access tokens, OAuth client secrets and tokens, the GitHub
-- App private key) live sealed in credentials_enc; webhook_secret_enc signs and
-- verifies the connection's webhooks.
CREATE TABLE git_connections (
    id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id       uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    provider           text NOT NULL CHECK (provider IN ('github','gitlab','gitea','forgejo','bitbucket')),
    name               text NOT NULL,
    -- Web base URL without a trailing slash (https://github.com, https://gitlab.example.com).
    base_url           text NOT NULL,
    auth_type          text NOT NULL CHECK (auth_type IN ('github_app','oauth','token','webhook_only')),
    -- pending: waiting for the OAuth consent / GitHub App installation.
    status             text NOT NULL DEFAULT 'active' CHECK (status IN ('pending','active','error')),
    status_message     text NOT NULL DEFAULT '',
    -- The account the credentials act as (user or organisation login).
    account_login      text NOT NULL DEFAULT '',
    credentials_enc    bytea,
    webhook_secret_enc bytea NOT NULL,
    -- Non-secret provider details: appId, appSlug, installationId, clientId, htmlUrl, …
    meta               jsonb NOT NULL DEFAULT '{}',
    -- sha256 of the one-time state of a running OAuth / manifest flow.
    pending_state_hash text,
    pending_expires_at timestamptz,
    created_by         uuid REFERENCES users(id) ON DELETE SET NULL,
    created_at         timestamptz NOT NULL DEFAULT now(),
    updated_at         timestamptz NOT NULL DEFAULT now(),
    UNIQUE (workspace_id, name)
);
CREATE INDEX git_connections_state_idx ON git_connections (pending_state_hash) WHERE pending_state_hash IS NOT NULL;

-- Webhooks NebulaCtrl registered through a provider API (GitLab, Gitea,
-- Forgejo). GitHub Apps and Bitbucket have none: the App-level hook and the
-- manually created hook deliver for every repository.
CREATE TABLE git_webhooks (
    connection_id  uuid NOT NULL REFERENCES git_connections(id) ON DELETE CASCADE,
    repo_full_name text NOT NULL,
    external_id    text NOT NULL DEFAULT '',
    status         text NOT NULL DEFAULT 'active' CHECK (status IN ('active','error')),
    status_message text NOT NULL DEFAULT '',
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (connection_id, repo_full_name)
);

-- The link between a git service and its repository, plus the auto-deploy
-- settings. These are operational settings: they never bump
-- services.generation and are not part of a release. The clone URL and the
-- branch stay in services.config.
CREATE TABLE service_git (
    service_id     uuid PRIMARY KEY REFERENCES services(id) ON DELETE CASCADE,
    connection_id  uuid NOT NULL REFERENCES git_connections(id) ON DELETE CASCADE,
    repo_full_name text NOT NULL,
    auto_deploy    boolean NOT NULL DEFAULT true,
    -- Monorepo watch paths (globs; a leading ! excludes). Empty = every push.
    watch_paths    text[] NOT NULL DEFAULT '{}',
    -- The newest push that arrived while a deployment was running; deployed
    -- when that deployment finishes ({sha, message, author, branch}).
    pending_commit jsonb,
    updated_at     timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX service_git_repo_idx ON service_git (connection_id, lower(repo_full_name));

-- Delivery IDs seen in the last 24 hours: a repeat is a replay and is rejected.
CREATE TABLE git_delivery_ids (
    connection_id uuid NOT NULL REFERENCES git_connections(id) ON DELETE CASCADE,
    delivery_id   text NOT NULL,
    seen_at       timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (connection_id, delivery_id)
);
CREATE INDEX git_delivery_ids_seen_idx ON git_delivery_ids (seen_at);

-- Delivery log (headers redacted, payload not stored).
CREATE TABLE git_deliveries (
    id             bigserial PRIMARY KEY,
    connection_id  uuid NOT NULL REFERENCES git_connections(id) ON DELETE CASCADE,
    delivery_id    text NOT NULL DEFAULT '',
    event          text NOT NULL DEFAULT '',
    outcome        text NOT NULL CHECK (outcome IN ('processed','ignored','rejected','replayed','error')),
    http_status    integer NOT NULL,
    summary        text NOT NULL DEFAULT '',
    repo_full_name text NOT NULL DEFAULT '',
    branch         text NOT NULL DEFAULT '',
    commit_sha     text NOT NULL DEFAULT '',
    headers        jsonb NOT NULL DEFAULT '{}',
    payload_bytes  integer NOT NULL DEFAULT 0,
    deployments    uuid[] NOT NULL DEFAULT '{}',
    received_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX git_deliveries_conn_idx ON git_deliveries (connection_id, id DESC);

-- The commit status last reported to the provider for a deployment, so every
-- state is sent once and a restart never repeats or reorders statuses.
CREATE TABLE git_status_reports (
    deployment_id uuid PRIMARY KEY REFERENCES deployments(id) ON DELETE CASCADE,
    -- The last state successfully reported ('' = none yet).
    state         text NOT NULL DEFAULT '',
    -- The last failed attempt (cleared by a success) and how many in a row.
    error         text NOT NULL DEFAULT '',
    attempts      integer NOT NULL DEFAULT 0,
    reported_at   timestamptz NOT NULL DEFAULT now()
);

-- Per-project preview settings (one row once the project was configured).
--   seed_commands    [{service, command, runOn: "create"|"push"}]: one-off commands run in a
--                    database consumer (an app service) after its deploy succeeded.
--   excluded_services service slugs of the base environment that previews do not get.
CREATE TABLE preview_settings (
    project_id          uuid PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE,
    workspace_id        uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    enabled             boolean NOT NULL DEFAULT false,
    base_environment_id uuid REFERENCES environments(id) ON DELETE SET NULL,
    fork_prs            boolean NOT NULL DEFAULT false,
    max_concurrent      integer NOT NULL DEFAULT 5 CHECK (max_concurrent BETWEEN 1 AND 50),
    idle_days           integer NOT NULL DEFAULT 7 CHECK (idle_days BETWEEN 1 AND 365),
    seed_commands       jsonb NOT NULL DEFAULT '[]'::jsonb,
    excluded_services   text[] NOT NULL DEFAULT '{}',
    updated_at          timestamptz NOT NULL DEFAULT now()
);

-- One row per open (or recently closed, until cleanup finished) PR preview.
--   deploy_state  the current deploy wave: {sha, startedAt, targets[], deployments{}, seeds{}, ...}
CREATE TABLE previews (
    id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id        uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    project_id          uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    environment_id      uuid NOT NULL UNIQUE REFERENCES environments(id) ON DELETE CASCADE,
    base_environment_id uuid NOT NULL REFERENCES environments(id),
    connection_id       uuid NOT NULL,
    provider            text NOT NULL,
    repo                text NOT NULL,
    pr_number           integer NOT NULL,
    pr_title            text NOT NULL DEFAULT '',
    pr_author           text NOT NULL DEFAULT '',
    pr_url              text NOT NULL DEFAULT '',
    head_branch         text NOT NULL DEFAULT '',
    head_sha            text NOT NULL DEFAULT '',
    base_branch         text NOT NULL DEFAULT '',
    from_fork           boolean NOT NULL DEFAULT false,
    -- Services of the preview that build the PR's repository (redeployed on every push).
    affected_service_ids uuid[] NOT NULL DEFAULT '{}',
    status              text NOT NULL DEFAULT 'creating'
                        CHECK (status IN ('creating','deploying','seeding','ready','failed','paused','deleting')),
    status_message      text NOT NULL DEFAULT '',
    deploy_state        jsonb NOT NULL DEFAULT '{}'::jsonb,
    last_push_at        timestamptz NOT NULL DEFAULT now(),
    paused_at           timestamptz,
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now(),
    UNIQUE (project_id, connection_id, repo, pr_number)
);
CREATE INDEX previews_project_idx ON previews (project_id, created_at DESC);
CREATE INDEX previews_pr_idx ON previews (connection_id, repo, pr_number);

-- ============================================================================
-- Domains, proxy, certificates and DNS
-- ============================================================================

-- Custom certificates (upload). Scoped to a server because each server runs
-- its own proxy. A certificate is staged until "Deploy proxy": applied_at is
-- null while staged and deleted_at marks a staged removal; the proxy config only
-- ever carries rows with applied_at set and deleted_at null.
CREATE TABLE certificates (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id   uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    server_id      uuid NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
    name           text NOT NULL,
    domains        text[] NOT NULL,
    cert_pem       text NOT NULL,
    key_enc        bytea NOT NULL,
    fingerprint    text NOT NULL,
    issuer         text NOT NULL DEFAULT '',
    not_before     timestamptz NOT NULL,
    not_after      timestamptz NOT NULL,
    status         text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','active','error')),
    status_message text NOT NULL DEFAULT '',
    applied_at     timestamptz,
    deleted_at     timestamptz,
    created_at     timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX certificates_server_name_key ON certificates (server_id, name) WHERE deleted_at IS NULL;
CREATE INDEX certificates_server_idx ON certificates (server_id);

-- Domains carry routing and policy fields. Path routing means the hostname alone
-- is not unique; (hostname, path_prefix) is.
--   target_port is 0 for domains of route services (their upstream is external).
--   options is the JSON of agentprotocol.RouteOptions (no secrets).
--   component is the compose service a domain routes to; '' for every other service.
-- dns_resolver names the DNS-01 provider code that obtains the domain's
-- certificate ('' = HTTP-01 or a covering wildcard). The code is validated
-- against the lego catalog (pkg/agentprotocol, generated from the release
-- Traefik vendors) where it enters the API; the columns only keep the shape of
-- a code, because the catalog moves with the pinned Traefik. cert_* is the
-- observed certificate state.
-- Cloudflare DNS: cf_zone_id is set when the hostname falls into a zone of the
-- team's connection ('' = not in a connected zone) and is matched against the
-- cached zone list, so it is known as soon as the domain exists. cf_manage lets
-- one domain opt out of record management. cf_status is the last evaluated DNS
-- status ('' = never evaluated); cf_error is the last sync failure.
-- cf_origin_lock: a domain is locked (accepts only traffic that comes through
-- Cloudflare) when it is proxied, not tunnelled and this is true; the default
-- keeps every proxied domain locked.
CREATE TABLE domains (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    service_id      uuid NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    hostname        text NOT NULL,
    target_port     integer NOT NULL,
    generated       boolean NOT NULL DEFAULT false,
    status          text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','active','error')),
    status_message  text NOT NULL DEFAULT '',
    dns_ok          boolean,
    dns_checked_at  timestamptz,
    created_at      timestamptz NOT NULL DEFAULT now(),
    kind            text    NOT NULL DEFAULT 'serve' CHECK (kind IN ('serve','redirect')),
    path_prefix     text    NOT NULL DEFAULT '',
    strip_prefix    boolean NOT NULL DEFAULT false,
    add_prefix      text    NOT NULL DEFAULT '',
    redirect_to     text,
    redirect_code   integer NOT NULL DEFAULT 308 CHECK (redirect_code IN (301,302,307,308)),
    preserve_path   boolean NOT NULL DEFAULT true,
    tls_mode        text    NOT NULL DEFAULT 'auto' CHECK (tls_mode IN ('auto','custom','none')),
    certificate_id  uuid    REFERENCES certificates(id) ON DELETE SET NULL,
    options         jsonb   NOT NULL DEFAULT '{}'::jsonb,
    dns_resolver    text    NOT NULL DEFAULT '' CHECK (dns_resolver = '' OR dns_resolver ~ '^[a-z0-9][a-z0-9-]{0,39}$'),
    cert_status     text    NOT NULL DEFAULT '' CHECK (cert_status IN ('','pending','active','error')),
    cert_message    text    NOT NULL DEFAULT '',
    cert_issuer     text    NOT NULL DEFAULT '',
    cert_not_after  timestamptz,
    cert_checked_at timestamptz,
    cf_zone_id      text    NOT NULL DEFAULT '',
    cf_zone_name    text    NOT NULL DEFAULT '',
    cf_manage       boolean NOT NULL DEFAULT true,
    cf_proxied      boolean NOT NULL DEFAULT false,
    cf_status       text    NOT NULL DEFAULT '' CHECK (cf_status IN ('','managed','present','wrong_target','missing','not_in_zone')),
    cf_message      text    NOT NULL DEFAULT '',
    cf_error        text    NOT NULL DEFAULT '',
    cf_checked_at   timestamptz,
    component       text    NOT NULL DEFAULT '',
    cf_origin_lock  boolean NOT NULL DEFAULT true
);
CREATE UNIQUE INDEX domains_host_path_key ON domains (hostname, path_prefix);
CREATE INDEX domains_cf_zone_idx ON domains (cf_zone_id) WHERE cf_zone_id <> '';

-- Basic-auth users of a domain. The bcrypt hash is treated as a secret
-- (offline-crackable) and sealed with secretbox; plaintext is never stored.
CREATE TABLE domain_auth_users (
    domain_id  uuid NOT NULL REFERENCES domains(id) ON DELETE CASCADE,
    username   text NOT NULL,
    hash_enc   bytea NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (domain_id, username)
);

-- TCP/UDP listen ports of route services, unique per server and protocol.
-- Maintained by internal/proxyroutes together with the services row.
CREATE TABLE route_ports (
    service_id  uuid PRIMARY KEY REFERENCES services(id) ON DELETE CASCADE,
    server_id   uuid NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
    protocol    text NOT NULL CHECK (protocol IN ('tcp','udp')),
    listen_port integer NOT NULL CHECK (listen_port BETWEEN 1 AND 65535),
    UNIQUE (server_id, protocol, listen_port)
);

-- DNS provider credentials for DNS-01. Scope team (server_id NULL: usable on
-- every server of the team) or one server. The variables are sealed with
-- secretbox as one JSON object {ENV_NAME: value}; env_names lists the names (not
-- secret) so command payloads can name what the send-time secrets carry. rev
-- advances whenever the values change: a resolver that applied an older rev is
-- "rotated". provider is any lego provider code; only its shape is checked here.
CREATE TABLE dns_credentials (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    server_id    uuid REFERENCES servers(id) ON DELETE CASCADE,
    name         text NOT NULL,
    provider     text NOT NULL CHECK (provider ~ '^[a-z0-9][a-z0-9-]{0,39}$'),
    secrets_enc  bytea NOT NULL,
    env_names    text[] NOT NULL,
    rev          integer NOT NULL DEFAULT 1,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX dns_credentials_name_key ON dns_credentials (workspace_id, name);
CREATE INDEX dns_credentials_server_idx ON dns_credentials (server_id) WHERE server_id IS NOT NULL;

-- The resolver of a provider on a server (at most one: the credentials are
-- the proxy container's environment, a process-wide namespace). Desired state;
-- servers.applied_dns01 is what the last successful "Deploy proxy" applied.
CREATE TABLE server_dns_resolvers (
    server_id     uuid NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
    provider      text NOT NULL CHECK (provider ~ '^[a-z0-9][a-z0-9-]{0,39}$'),
    credential_id uuid NOT NULL REFERENCES dns_credentials(id) ON DELETE RESTRICT,
    delay_seconds integer NOT NULL DEFAULT 0 CHECK (delay_seconds BETWEEN 0 AND 600),
    staging       boolean NOT NULL DEFAULT false,
    PRIMARY KEY (server_id, provider)
);
CREATE INDEX server_dns_resolvers_credential_idx ON server_dns_resolvers (credential_id);

-- Wildcard certificates: *.<domain> (and the apex) through a server's
-- resolver. Desired state like the resolvers; status/issuer/not_after are the
-- agent's observation (heartbeat). cover_generated lets generated domains use
-- it; is_default makes it the proxy's default certificate
-- (tls.stores.default.defaultGeneratedCert).
CREATE TABLE wildcard_certs (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id    uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    server_id       uuid NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
    domain          text NOT NULL,
    include_apex    boolean NOT NULL DEFAULT true,
    provider        text NOT NULL CHECK (provider ~ '^[a-z0-9][a-z0-9-]{0,39}$'),
    cover_generated boolean NOT NULL DEFAULT false,
    is_default      boolean NOT NULL DEFAULT false,
    status          text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','active','error')),
    status_message  text NOT NULL DEFAULT '',
    issuer          text NOT NULL DEFAULT '',
    not_after       timestamptz,
    checked_at      timestamptz,
    created_at      timestamptz NOT NULL DEFAULT now(),
    UNIQUE (server_id, domain)
);
CREATE UNIQUE INDEX wildcard_certs_default_key ON wildcard_certs (server_id) WHERE is_default;

-- A team-level Cloudflare connection that keeps the DNS records of service
-- domains in sync (A/AAAA to the server, or the tunnel CNAME). One per team. The
-- API token is sealed with secretbox and never returned. zones is the cached
-- zone list ([{id,name,accountId,status}]); account_id is what Cloudflare Tunnel
-- calls need (detected from the zones, or chosen by the user when the token sees
-- several accounts).
CREATE TABLE cloudflare_connections (
    workspace_id     uuid PRIMARY KEY REFERENCES workspaces(id) ON DELETE CASCADE,
    token_enc        bytea NOT NULL,
    account_id       text NOT NULL DEFAULT '',
    account_name     text NOT NULL DEFAULT '',
    auto_manage      boolean NOT NULL DEFAULT true,
    default_proxied  boolean NOT NULL DEFAULT false,
    status           text NOT NULL DEFAULT 'ok' CHECK (status IN ('ok','error')),
    status_message   text NOT NULL DEFAULT '',
    token_expires_at timestamptz,
    verified_at      timestamptz,
    zones            jsonb NOT NULL DEFAULT '[]'::jsonb,
    zones_synced_at  timestamptz,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now()
);

-- Records NebulaCtrl created (or adopted). domain_id has no foreign key: a
-- record outlives its domain until the collector has deleted it from Cloudflare.
-- Only rows listed here, and carrying the ownership comment, are ever modified.
CREATE TABLE cloudflare_records (
    zone_id      text NOT NULL,
    record_id    text NOT NULL,
    workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    domain_id    uuid NOT NULL,
    hostname     text NOT NULL,
    type         text NOT NULL CHECK (type IN ('A','AAAA','CNAME')),
    content      text NOT NULL,
    proxied      boolean NOT NULL DEFAULT false,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (zone_id, record_id)
);
CREATE INDEX cloudflare_records_domain_idx ON cloudflare_records (domain_id);
CREATE INDEX cloudflare_records_workspace_idx ON cloudflare_records (workspace_id);

-- Cloudflare origin lock: a proxied (orange-cloud) domain only accepts traffic
-- that comes through Cloudflare. Three layers, rendered by the agent per route:
-- an IP allow list of Cloudflare's ranges, Authenticated Origin Pulls (a client
-- certificate Cloudflare presents) and the real visitor IP from CF-Connecting-IP.
--
-- Cloudflare's published IP ranges. One row (id = 1): the last list that
-- passed validation. No row means the list built into the binary is in use
-- (the refresh job seeds one with source 'builtin' to record a failing fetch).
-- source: 'api' is /client/v4/ips, 'text' the ips-v4/ips-v6 files. fetched_at
-- is the last successful fetch (also when the list did not change), changed_at
-- the last time its content changed. last_error and failing_since describe
-- fetches that failed since then.
CREATE TABLE cloudflare_ip_lists (
    id            smallint PRIMARY KEY CHECK (id = 1),
    v4            text[] NOT NULL,
    v6            text[] NOT NULL,
    hash          text NOT NULL,
    source        text NOT NULL CHECK (source IN ('builtin','api','text')),
    fetched_at    timestamptz NOT NULL,
    changed_at    timestamptz NOT NULL,
    last_error    text NOT NULL DEFAULT '',
    failing_since timestamptz,
    stale_notified_at timestamptz
);

-- Authenticated Origin Pulls state per zone. 'on': the zone setting is on and
-- the zone's SSL mode accepts it. 'unavailable': it cannot be used (reason_code
-- says why: permission, ssl_mode, error) and the zone's domains are locked by
-- IP only. Rows are written whenever a locked domain of the zone is evaluated.
CREATE TABLE cloudflare_zone_aop (
    workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    zone_id      text NOT NULL,
    state        text NOT NULL CHECK (state IN ('on','unavailable')),
    reason_code  text NOT NULL DEFAULT '' CHECK (reason_code IN ('','permission','ssl_mode','error')),
    reason       text NOT NULL DEFAULT '',
    checked_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (workspace_id, zone_id)
);

-- One remotely-managed Cloudflare Tunnel per server. The tunnel lives at
-- Cloudflare (created, configured and deleted through the API with the team's
-- Cloudflare connection); the agent runs the cloudflared connector next to the
-- proxy. The connector token is fetched from the API at command send time and
-- is never stored.
--
-- A row exists while the server has (or is getting rid of) a tunnel. state is
-- the observed connector state from the agent (heartbeat or command result);
-- 'removing' marks a tunnel being torn down: the row is deleted once the tunnel
-- is gone at Cloudflare. activated_at is set when the connector first reported a
-- connection: from then on the server's domains in connected zones point at the
-- tunnel CNAME and are served as tunnelled (no HTTP-01, no HTTPS redirect).
CREATE TABLE server_tunnels (
    server_id         uuid PRIMARY KEY REFERENCES servers(id) ON DELETE CASCADE,
    workspace_id      uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    account_id        text NOT NULL,
    tunnel_id         text NOT NULL,
    tunnel_name       text NOT NULL,
    state             text NOT NULL DEFAULT 'provisioning'
                      CHECK (state IN ('provisioning','online','offline','needs_attention','removing')),
    message           text NOT NULL DEFAULT '',
    connections       integer NOT NULL DEFAULT 0,
    image             text NOT NULL DEFAULT '',
    -- The ingress hostnames last pushed to Cloudflare (fingerprint) and the
    -- outcome of the last push.
    ingress_hash      text NOT NULL DEFAULT '',
    ingress_hosts     integer NOT NULL DEFAULT 0,
    ingress_synced_at timestamptz,
    ingress_error     text NOT NULL DEFAULT '',
    -- When a sync_tunnel command was last queued (the sweep re-sends it, at most
    -- every five minutes, when the connector is missing).
    connector_synced_at timestamptz,
    activated_at      timestamptz,
    observed_at       timestamptz,
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX server_tunnels_tunnel_key ON server_tunnels (tunnel_id);
CREATE INDEX server_tunnels_workspace_idx ON server_tunnels (workspace_id);

-- Tailscale / Headscale workspace integration (one row per workspace). The OAuth
-- client secret is sealed with secretbox and never returned. For headscale the
-- secret is an API key (secret_enc) used to create single-use pre-auth keys and
-- delete nodes, or a reusable tagged pre-auth key that is handed to every
-- sidecar as is (no device removal through the API then).
CREATE TABLE tailnet_integrations (
    workspace_id    uuid PRIMARY KEY REFERENCES workspaces(id) ON DELETE CASCADE,
    provider        text NOT NULL DEFAULT 'tailscale_oauth' CHECK (provider IN ('tailscale_oauth', 'headscale')),
    tailnet         text NOT NULL DEFAULT '-',
    client_id       text NOT NULL DEFAULT '',
    secret_enc      bytea NOT NULL,
    default_tags    text[] NOT NULL DEFAULT '{tag:nebula-svc}',
    allowed_tags    text[] NOT NULL DEFAULT '{tag:nebula-svc}',
    https_enabled   boolean,
    last_test_at    timestamptz,
    last_test_ok    boolean,
    last_test_error text,
    updated_at      timestamptz NOT NULL DEFAULT now(),
    login_server         text NOT NULL DEFAULT '',
    headscale_user       text NOT NULL DEFAULT '',
    headscale_credential text NOT NULL DEFAULT 'apikey' CHECK (headscale_credential IN ('apikey', 'preauth'))
);

-- Per-service Tailscale / Headscale. Desired state lives in
-- services.config.tailnet (staged by the generation counter like every
-- setting). service_tailnet is the OBSERVED / APPLIED side: the device, its
-- address, the configuration the agent last applied and the node's health. It
-- never touches the generation.
--
-- The node of a service. One row per service that has (or is getting) a
-- sidecar. `applied` is the tailnetcfg.Config the agent last applied and
-- `identity_hash` the fingerprint of the device identity (provider, login
-- server, hostname, tags) it was provisioned with. A change of the identity,
-- of the server or `needs_reprovision` makes the next deployment re-create the
-- device with a new key.
CREATE TABLE service_tailnet (
    service_id          uuid PRIMARY KEY REFERENCES services(id) ON DELETE CASCADE,
    workspace_id        uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    server_id           uuid REFERENCES servers(id) ON DELETE SET NULL,
    provider            text NOT NULL CHECK (provider IN ('tailscale', 'headscale')),
    hostname            text NOT NULL,
    identity_hash       text NOT NULL DEFAULT '',
    device_id           text NOT NULL DEFAULT '',
    fqdn                text NOT NULL DEFAULT '',
    ips                 text[] NOT NULL DEFAULT '{}',
    state               text NOT NULL DEFAULT 'provisioning' CHECK (state IN ('provisioning', 'online', 'offline', 'needs_attention', 'removing')),
    message             text NOT NULL DEFAULT '',
    auth_key_id         text NOT NULL DEFAULT '',
    applied             jsonb NOT NULL DEFAULT '{}'::jsonb,
    applied_hide_public boolean NOT NULL DEFAULT false,
    funnel              boolean NOT NULL DEFAULT false,
    needs_reprovision   boolean NOT NULL DEFAULT false,
    provisioned_at      timestamptz,
    observed_at         timestamptz,
    updated_at          timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX service_tailnet_server_idx ON service_tailnet (server_id) WHERE applied_hide_public;
CREATE INDEX service_tailnet_workspace_idx ON service_tailnet (workspace_id);

-- ============================================================================
-- Networks and the server mesh
-- ============================================================================

-- Team shared networks. A shared network is a named Docker network on ONE
-- server (nebulactrl-shared-<id8>); services of any project of the team that run on
-- that server attach to it explicitly and get the alias
-- <service-slug>.<env-slug>.<project-slug>. Attachments are staged (they bump
-- the service generation) and frozen into the release snapshot when deployed.
-- Nothing is ever shared across teams: every row belongs to one workspace.
CREATE TABLE shared_networks (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    server_id    uuid NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
    name         text NOT NULL CHECK (length(name) BETWEEN 1 AND 63),
    created_by   uuid REFERENCES users(id) ON DELETE SET NULL,
    created_at   timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX shared_networks_name_key ON shared_networks (workspace_id, lower(name));
CREATE INDEX shared_networks_server_idx ON shared_networks (server_id);

CREATE TABLE service_networks (
    service_id uuid NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    network_id uuid NOT NULL REFERENCES shared_networks(id) ON DELETE RESTRICT,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (service_id, network_id)
);
CREATE INDEX service_networks_network_idx ON service_networks (network_id);

-- Managed tailnet server mesh and cross-server service links.
--
-- A team (workspace) has at most one tailnet (its tailnet_integrations row).
-- Every mesh-enabled server runs one *server node* (userspace, ingress via
-- `tailscale serve`); an environment on one server that must dial a service on
-- another gets a *link node* (kernel mode, egress). A link node belongs to ONE
-- environment, so its tailnet IP is the ACL source for exactly that environment.
--
-- mesh_exposures: a service port reachable on the server node of the server it
--   runs on. purpose 'replication' = a primary's port for its replicas (mesh_port
--   allocated in 15000-15999); 'service' = any TCP service port for consumers on
--   other servers (mesh_port prefers the container port when free on that node).
-- mesh_links: the link node of (environment, local server -> remote server,
--   purpose). Its aliases on the environment networks are derived from its grants:
--   the slugs of the granted 'service' providers, mesh-<remote server id8> for
--   'replication'.
-- mesh_grants: what a link may reach. The ACL generator turns grants into rules
--   (src = link node IPv4, dst = remote server node IPv4:mesh_port). ref_id is the
--   consumer service (source reference|edge) or the replica service (source replica).

CREATE TABLE mesh_settings (
    workspace_id           uuid PRIMARY KEY REFERENCES workspaces(id) ON DELETE CASCADE,
    enabled                boolean NOT NULL DEFAULT false,
    auto_join              boolean NOT NULL DEFAULT false,
    -- Mesh changes (key mints, node sync, ACL writes, grant reconciliation, replica
    -- sagas) wait while paused; established tunnels keep working. Used while the
    -- platform Headscale is redeployed with policy.mode: database.
    paused                 boolean NOT NULL DEFAULT false,
    paused_reason          text    NOT NULL DEFAULT '',
    mesh_tag               text    NOT NULL DEFAULT 'tag:nebula-mesh',
    acl_mode               text    NOT NULL DEFAULT 'advisory' CHECK (acl_mode IN ('managed', 'advisory', 'off')),
    acl_state              text    NOT NULL DEFAULT 'unknown'  CHECK (acl_state IN ('unknown', 'applied', 'pending', 'drift', 'advisory', 'error')),
    acl_message            text    NOT NULL DEFAULT '',
    acl_hash               text    NOT NULL DEFAULT '',
    -- Tailscale ETag / Headscale updatedAt of the policy NebulaCtrl last wrote.
    acl_version            text    NOT NULL DEFAULT '',
    acl_applied_at         timestamptz,
    coordinator_ok         boolean,
    coordinator_checked_at timestamptz,
    updated_at             timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE server_mesh (
    server_id         uuid PRIMARY KEY REFERENCES servers(id) ON DELETE CASCADE,
    workspace_id      uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    state             text NOT NULL DEFAULT 'provisioning' CHECK (state IN ('provisioning', 'online', 'offline', 'needs_attention', 'removing')),
    message           text NOT NULL DEFAULT '',
    hostname          text NOT NULL,
    identity_hash     text NOT NULL DEFAULT '',
    device_id         text NOT NULL DEFAULT '',
    fqdn              text NOT NULL DEFAULT '',
    ips               text[] NOT NULL DEFAULT '{}',
    auth_key_id       text NOT NULL DEFAULT '',
    needs_reprovision boolean NOT NULL DEFAULT false,
    peers             jsonb NOT NULL DEFAULT '[]',
    -- The MeshNodeSpec the agent last applied.
    applied           jsonb NOT NULL DEFAULT '{}',
    provisioned_at    timestamptz,
    observed_at       timestamptz,
    updated_at        timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX server_mesh_workspace_idx ON server_mesh (workspace_id);

CREATE TABLE mesh_links (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id      uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    environment_id    uuid NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
    -- Where the link node runs (the consumer side) and whose server node it targets.
    server_id         uuid NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
    remote_server_id  uuid NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
    purpose           text NOT NULL DEFAULT 'replication' CHECK (purpose IN ('replication', 'service')),
    state             text NOT NULL DEFAULT 'provisioning' CHECK (state IN ('provisioning', 'online', 'degraded', 'offline', 'needs_attention', 'removing')),
    message           text NOT NULL DEFAULT '',
    hostname          text NOT NULL,
    identity_hash     text NOT NULL DEFAULT '',
    device_id         text NOT NULL DEFAULT '',
    ips               text[] NOT NULL DEFAULT '{}',
    target_ip         text NOT NULL DEFAULT '',
    auth_key_id       text NOT NULL DEFAULT '',
    needs_reprovision boolean NOT NULL DEFAULT false,
    applied           jsonb NOT NULL DEFAULT '{}',
    observed_at       timestamptz,
    created_at        timestamptz NOT NULL DEFAULT now(),
    CHECK (server_id <> remote_server_id),
    UNIQUE (environment_id, server_id, remote_server_id, purpose)
);
CREATE INDEX mesh_links_server_idx ON mesh_links (server_id);
CREATE INDEX mesh_links_remote_server_idx ON mesh_links (remote_server_id);
CREATE INDEX mesh_links_workspace_idx ON mesh_links (workspace_id);

CREATE TABLE mesh_exposures (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id   uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    server_id      uuid NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
    service_id     uuid NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    environment_id uuid NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
    purpose        text NOT NULL DEFAULT 'replication' CHECK (purpose IN ('replication', 'service')),
    container_port integer NOT NULL CHECK (container_port BETWEEN 1 AND 65535),
    -- The port on the server node (tailnet side). Replication exposures are
    -- allocated from 15000-15999; service exposures keep the container port when
    -- it is free on that node, else take the first free port of 15000-15999.
    mesh_port      integer NOT NULL CHECK (mesh_port BETWEEN 1 AND 65535),
    created_at     timestamptz NOT NULL DEFAULT now(),
    UNIQUE (server_id, mesh_port),
    UNIQUE (service_id, purpose)
);
CREATE INDEX mesh_exposures_workspace_idx ON mesh_exposures (workspace_id);
CREATE INDEX mesh_exposures_environment_idx ON mesh_exposures (environment_id);

CREATE TABLE mesh_grants (
    link_id     uuid NOT NULL REFERENCES mesh_links(id) ON DELETE CASCADE,
    exposure_id uuid NOT NULL REFERENCES mesh_exposures(id) ON DELETE CASCADE,
    -- The consumer service (reference, edge) or the replica service (replica).
    ref_id      uuid NOT NULL,
    -- Why the grant exists: a variable reference in the consumer, a canvas link
    -- (service_links) or a replica. A grant is removed when no source remains.
    source      text NOT NULL CHECK (source IN ('reference', 'edge', 'replica')),
    created_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (link_id, exposure_id, ref_id, source)
);
CREATE INDEX mesh_grants_exposure_idx ON mesh_grants (exposure_id);
CREATE INDEX mesh_grants_ref_idx ON mesh_grants (ref_id);

-- ============================================================================
-- Managed databases
-- ============================================================================

-- The engine, version and credentials of a database live in
-- services.config.database and the service's variables; these tables hold the
-- operational state around them.

-- Credential rotations. new_password_enc is the password to apply; it is
-- attached to the db_admin command at send time (never stored in the command
-- payload) and moves into the service's variable when the agent reports success.
CREATE TABLE database_rotations (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    service_id       uuid NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    mode             text NOT NULL CHECK (mode IN ('exec','redeploy','variable')),
    status           text NOT NULL CHECK (status IN ('pending','applied','failed')),
    password_key     text NOT NULL,
    new_password_enc bytea NOT NULL,
    command_id       uuid,
    error            text NOT NULL DEFAULT '',
    actor_label      text NOT NULL DEFAULT '',
    created_at       timestamptz NOT NULL DEFAULT now(),
    finished_at      timestamptz
);
CREATE UNIQUE INDEX database_rotations_one_pending ON database_rotations (service_id) WHERE status = 'pending';
CREATE INDEX database_rotations_service ON database_rotations (service_id, created_at DESC);

-- Last version/size/connection snapshot the agent reported for a database.
CREATE TABLE database_info (
    service_id  uuid PRIMARY KEY REFERENCES services(id) ON DELETE CASCADE,
    info        jsonb,
    error       text NOT NULL DEFAULT '',
    command_id  uuid,
    updated_at  timestamptz NOT NULL DEFAULT now()
);

-- In-place major upgrades that copy the data into a new volume (pg_upgrade).
-- On success the service's volume row is switched to new_volume and the image to
-- the target version as a staged change; the old volume is kept.
CREATE TABLE database_upgrades (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    service_id   uuid NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    method       text NOT NULL,
    from_version text NOT NULL,
    to_version   text NOT NULL,
    old_volume   text NOT NULL,
    new_volume   text NOT NULL,
    status       text NOT NULL CHECK (status IN ('pending','ready','failed')),
    command_id   uuid,
    error        text NOT NULL DEFAULT '',
    actor_label  text NOT NULL DEFAULT '',
    created_at   timestamptz NOT NULL DEFAULT now(),
    finished_at  timestamptz
);
CREATE UNIQUE INDEX database_upgrades_one_pending ON database_upgrades (service_id) WHERE status = 'pending';
CREATE INDEX database_upgrades_service ON database_upgrades (service_id, created_at DESC);

-- Postgres read replicas. A replica is an ordinary kind=database service of the
-- same environment as its primary (config.database.replica drives its start
-- command); this table is its lifecycle and observed state.
CREATE TABLE database_replicas (
    service_id         uuid PRIMARY KEY REFERENCES services(id) ON DELETE CASCADE,
    workspace_id       uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    environment_id     uuid NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
    primary_service_id uuid NOT NULL REFERENCES services(id),
    slot_name          text NOT NULL,
    repl_user          text NOT NULL,
    link_id            uuid REFERENCES mesh_links(id) ON DELETE SET NULL,
    exposure_id        uuid REFERENCES mesh_exposures(id) ON DELETE SET NULL,
    state              text NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'preparing', 'cloning', 'deploying', 'streaming', 'lagging', 'broken', 'needs_rebuild', 'promoting', 'removing', 'failed')),
    message            text NOT NULL DEFAULT '',
    -- Saga cursor (prepare, link, clone, deploy, ...).
    step               text NOT NULL DEFAULT 'prepare',
    clone_command_id   uuid,
    receive_lsn        text,
    replay_lsn         text,
    lag_bytes          bigint,
    replay_lag_ms      bigint,
    wal_status         text,
    retained_bytes     bigint,
    observed_at        timestamptz,
    last_streaming_at  timestamptz,
    created_by         uuid REFERENCES users(id) ON DELETE SET NULL,
    created_at         timestamptz NOT NULL DEFAULT now(),
    updated_at         timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX database_replicas_primary_idx ON database_replicas (primary_service_id);
CREATE INDEX database_replicas_environment_idx ON database_replicas (environment_id);

CREATE TABLE database_promotions (
    id                     uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id           uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    environment_id         uuid NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
    replica_service_id     uuid NOT NULL REFERENCES services(id),
    old_primary_service_id uuid NOT NULL REFERENCES services(id),
    mode                   text NOT NULL CHECK (mode IN ('switchover', 'forced')),
    status                 text NOT NULL DEFAULT 'running' CHECK (status IN ('running', 'succeeded', 'failed')),
    step                   text NOT NULL DEFAULT 'freeze',
    message                text NOT NULL DEFAULT '',
    freeze_lsn             text,
    accept_data_loss       boolean NOT NULL DEFAULT false,
    actor_label            text NOT NULL DEFAULT '',
    started_at             timestamptz NOT NULL DEFAULT now(),
    finished_at            timestamptz
);
CREATE UNIQUE INDEX database_promotions_one_running ON database_promotions (replica_service_id) WHERE status = 'running';

-- A fenced service must never run as a primary again; sticky across agent
-- reconnects (carried in every reconcile).
CREATE TABLE database_fences (
    service_id   uuid PRIMARY KEY REFERENCES services(id) ON DELETE CASCADE,
    promotion_id uuid REFERENCES database_promotions(id) ON DELETE SET NULL,
    reason       text NOT NULL DEFAULT '',
    fenced_at    timestamptz NOT NULL DEFAULT now()
);

-- ============================================================================
-- Backups and restores
-- ============================================================================

-- Database backups: destinations, schedules, backup runs and restores.
-- Dumps are streamed by the agent straight from the database container to a
-- destination; the control plane only holds metadata. Destination credentials
-- are sealed with secretbox and attached to agent commands at send time.

-- A place backups are written: an S3-compatible bucket or a directory on the
-- host running the database ("local", not off-site). local_dir is a relative
-- sub-directory of the agent's backup directory.
CREATE TABLE backup_destinations (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id      uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    name              text NOT NULL,
    kind              text NOT NULL CHECK (kind IN ('s3','local')),
    endpoint          text NOT NULL DEFAULT '',
    region            text NOT NULL DEFAULT '',
    bucket            text NOT NULL DEFAULT '',
    prefix            text NOT NULL DEFAULT '',
    path_style        boolean NOT NULL DEFAULT false,
    compat_mode       boolean NOT NULL DEFAULT true,
    access_key_enc    bytea,
    secret_key_enc    bytea,
    local_dir         text NOT NULL DEFAULT '',
    last_test_at      timestamptz,
    last_test_ok      boolean,
    last_test_message text NOT NULL DEFAULT '',
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX backup_destinations_name_uniq ON backup_destinations (workspace_id, lower(name));

-- A recurring backup of one database. cron is a 5-field expression or @macro
-- evaluated in timezone; next_run_at is the next due instant (UTC). Retention
-- keeps the newest retention_count successful backups of the schedule and drops
-- older ones past retention_days; 0 disables that limit. Locked backups are
-- never pruned.
CREATE TABLE backup_schedules (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id    uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    service_id      uuid NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    destination_id  uuid NOT NULL REFERENCES backup_destinations(id) ON DELETE RESTRICT,
    cron            text NOT NULL,
    timezone        text NOT NULL DEFAULT 'UTC',
    enabled         boolean NOT NULL DEFAULT true,
    retention_count integer NOT NULL DEFAULT 7 CHECK (retention_count >= 0),
    retention_days  integer NOT NULL DEFAULT 0 CHECK (retention_days >= 0),
    next_run_at     timestamptz,
    last_run_at     timestamptz,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX backup_schedules_due_idx ON backup_schedules (next_run_at) WHERE enabled;
CREATE INDEX backup_schedules_service_idx ON backup_schedules (service_id);

-- One backup run. status: queued (command not yet running), running (acked),
-- succeeded, failed, skipped (not attempted: database or server not running)
-- or expired (never delivered, no changes made). object_deleted_at is set when
-- retention or a user removed the stored object; the row stays as history.
CREATE TABLE backups (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id      uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    service_id        uuid NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    schedule_id       uuid REFERENCES backup_schedules(id) ON DELETE SET NULL,
    destination_id    uuid NOT NULL REFERENCES backup_destinations(id) ON DELETE RESTRICT,
    server_id         uuid REFERENCES servers(id) ON DELETE SET NULL,
    trigger           text NOT NULL CHECK (trigger IN ('manual','schedule','pre_restore')),
    status            text NOT NULL DEFAULT 'queued' CHECK (status IN ('queued','running','succeeded','failed','skipped','expired')),
    engine            text NOT NULL DEFAULT '',
    engine_version    text NOT NULL DEFAULT '',
    database_name     text NOT NULL DEFAULT '',
    format            text NOT NULL DEFAULT '',
    object_key        text NOT NULL DEFAULT '',
    size_bytes        bigint NOT NULL DEFAULT 0,
    sha256            text NOT NULL DEFAULT '',
    error             text NOT NULL DEFAULT '',
    command_id        uuid,
    locked            boolean NOT NULL DEFAULT false,
    actor_label       text NOT NULL DEFAULT '',
    object_deleted_at timestamptz,
    started_at        timestamptz,
    finished_at       timestamptz,
    created_at        timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX backups_service_idx ON backups (service_id, created_at DESC);
CREATE INDEX backups_schedule_idx ON backups (schedule_id, created_at DESC);
CREATE INDEX backups_destination_idx ON backups (destination_id);
CREATE UNIQUE INDEX backups_command_idx ON backups (command_id) WHERE command_id IS NOT NULL;

-- A restore of a backup. mode 'new' restores into a freshly provisioned sibling
-- database (target_service_id); 'in_place' restores into the source database
-- after a pre-restore backup. status: provisioning (waiting for the new
-- database to run), pre_backup, restoring, succeeded or failed.
CREATE TABLE backup_restores (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id      uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    backup_id         uuid NOT NULL REFERENCES backups(id) ON DELETE CASCADE,
    mode              text NOT NULL CHECK (mode IN ('new','in_place')),
    source_service_id uuid NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    target_service_id uuid REFERENCES services(id) ON DELETE CASCADE,
    status            text NOT NULL CHECK (status IN ('provisioning','pre_backup','restoring','succeeded','failed')),
    pre_backup_id     uuid REFERENCES backups(id) ON DELETE SET NULL,
    deployment_id     uuid,
    command_id        uuid,
    error             text NOT NULL DEFAULT '',
    actor_label       text NOT NULL DEFAULT '',
    created_at        timestamptz NOT NULL DEFAULT now(),
    finished_at       timestamptz
);
CREATE INDEX backup_restores_backup_idx ON backup_restores (backup_id);
CREATE INDEX backup_restores_source_idx ON backup_restores (source_service_id, created_at DESC);
CREATE UNIQUE INDEX backup_restores_command_idx ON backup_restores (command_id) WHERE command_id IS NOT NULL;

-- Restore of a managed database from a dump file the user uploads (pg_dump,
-- mysqldump, mongodump). The file is relayed to the agent as it arrives and is
-- never stored by the control plane, so a row holds only metadata and a short
-- log. mode 'new' restores into a freshly provisioned sibling database
-- (target_service_id), 'in_place' into the source database after a
-- pre-restore backup. status: provisioning (waiting for the new database to
-- run), pre_backup, awaiting_upload (the target is ready for the file),
-- uploading, restoring (the whole file is with the agent), succeeded, failed.
CREATE TABLE dump_restores (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id      uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    mode              text NOT NULL CHECK (mode IN ('new','in_place')),
    source_service_id uuid NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    target_service_id uuid REFERENCES services(id) ON DELETE CASCADE,
    status            text NOT NULL CHECK (status IN ('provisioning','pre_backup','awaiting_upload','uploading','restoring','succeeded','failed')),
    file_name         text NOT NULL DEFAULT '',
    format            text NOT NULL DEFAULT '',
    gzip              boolean NOT NULL DEFAULT false,
    declared_size     bigint NOT NULL DEFAULT 0,
    size_bytes        bigint NOT NULL DEFAULT 0,
    bytes_received    bigint NOT NULL DEFAULT 0,
    sha256            text NOT NULL DEFAULT '',
    pre_backup_id     uuid REFERENCES backups(id) ON DELETE SET NULL,
    deployment_id     uuid,
    log               text NOT NULL DEFAULT '',
    error             text NOT NULL DEFAULT '',
    actor_label       text NOT NULL DEFAULT '',
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now(),
    started_at        timestamptz,
    finished_at       timestamptz
);
CREATE INDEX dump_restores_source_idx ON dump_restores (source_service_id, created_at DESC);
CREATE INDEX dump_restores_target_idx ON dump_restores (target_service_id) WHERE target_service_id IS NOT NULL;
CREATE INDEX dump_restores_pre_backup_idx ON dump_restores (pre_backup_id) WHERE pre_backup_id IS NOT NULL;
CREATE INDEX dump_restores_open_idx ON dump_restores (updated_at) WHERE status NOT IN ('succeeded','failed');

-- Volume backups: file-level backups of the named volumes of an ordinary
-- (non-database) service. The agent takes a point-in-time copy (pre-hook ->
-- freeze -> copy to a staging volume -> thaw) and uploads it in the
-- background with restic, one encrypted repository per service and
-- destination. The control plane holds metadata, the sealed repository
-- password and the retention policy; credentials and the password are attached
-- to agent commands at send time and never stored in a payload.

-- The restic repository of a service in one destination. password_enc is the
-- repository password sealed with secretbox (losing NEBULACTRL_SECRET_KEY loses
-- it: the UI offers a recovery key download). repo_path is the path below the
-- destination's prefix: volumes/<serviceId>.
CREATE TABLE volume_repos (
    service_id     uuid NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    destination_id uuid NOT NULL REFERENCES backup_destinations(id) ON DELETE RESTRICT,
    repo_path      text NOT NULL,
    password_enc   bytea NOT NULL,
    created_at     timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (service_id, destination_id)
);

-- How a service's volumes are backed up and when. One per service.
-- mode: freeze (docker pause while the local copy is taken; default), stop
-- (stop, copy, start) or live (no pause; the copy may be inconsistent).
-- freeze_budget_seconds bounds the pause: over budget the agent thaws and fails.
-- pre_hook / post_hook are {command[], user, timeoutSeconds, continueOnError}
-- run inside the service's container (pre: before the freeze).
-- keep_*: restic forget --keep-daily/-weekly/-monthly (0 = no limit of that
-- kind; all 0 keeps everything). Locked backups are never forgotten.
-- cron/timezone/enabled: the optional schedule (cron '' = manual only).
CREATE TABLE volume_backup_configs (
    service_id            uuid PRIMARY KEY REFERENCES services(id) ON DELETE CASCADE,
    workspace_id          uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    destination_id        uuid NOT NULL REFERENCES backup_destinations(id) ON DELETE RESTRICT,
    mode                  text NOT NULL DEFAULT 'freeze' CHECK (mode IN ('freeze','stop','live')),
    freeze_budget_seconds integer NOT NULL DEFAULT 5 CHECK (freeze_budget_seconds BETWEEN 1 AND 60),
    pre_hook              jsonb,
    post_hook             jsonb,
    excludes              text[] NOT NULL DEFAULT '{}',
    keep_daily            integer NOT NULL DEFAULT 7 CHECK (keep_daily BETWEEN 0 AND 3650),
    keep_weekly           integer NOT NULL DEFAULT 4 CHECK (keep_weekly BETWEEN 0 AND 520),
    keep_monthly          integer NOT NULL DEFAULT 6 CHECK (keep_monthly BETWEEN 0 AND 120),
    cron                  text NOT NULL DEFAULT '',
    timezone              text NOT NULL DEFAULT 'UTC',
    enabled               boolean NOT NULL DEFAULT false,
    next_run_at           timestamptz,
    last_run_at           timestamptz,
    created_at            timestamptz NOT NULL DEFAULT now(),
    updated_at            timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX volume_backup_configs_due_idx ON volume_backup_configs (next_run_at) WHERE enabled;

-- One volume backup run (one restic snapshot covering all of the service's
-- volumes, at /vol/<name>). status: queued (command not yet running), running
-- (acked), succeeded, failed, skipped (not attempted: server offline, service
-- busy) or expired (never delivered, no changes made). object_deleted_at is set
-- once the snapshot was forgotten; the row stays as history.
-- mode is what was requested, consistency what the agent actually used.
CREATE TABLE volume_backups (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id      uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    service_id        uuid NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    destination_id    uuid NOT NULL REFERENCES backup_destinations(id) ON DELETE RESTRICT,
    server_id         uuid REFERENCES servers(id) ON DELETE SET NULL,
    trigger           text NOT NULL CHECK (trigger IN ('manual','schedule','pre_restore')),
    status            text NOT NULL DEFAULT 'queued' CHECK (status IN ('queued','running','succeeded','failed','skipped','expired')),
    mode              text NOT NULL DEFAULT 'freeze',
    consistency       text NOT NULL DEFAULT '',
    volumes           text[] NOT NULL DEFAULT '{}',
    snapshot_id       text NOT NULL DEFAULT '',
    size_bytes        bigint NOT NULL DEFAULT 0,
    bytes_added       bigint NOT NULL DEFAULT 0,
    files             bigint NOT NULL DEFAULT 0,
    freeze_ms         integer NOT NULL DEFAULT 0,
    stage_ms          integer NOT NULL DEFAULT 0,
    stage_method      text NOT NULL DEFAULT '',
    hook_outcome      text NOT NULL DEFAULT '',
    warnings          text[] NOT NULL DEFAULT '{}',
    error             text NOT NULL DEFAULT '',
    command_id        uuid,
    locked            boolean NOT NULL DEFAULT false,
    actor_label       text NOT NULL DEFAULT '',
    object_deleted_at timestamptz,
    started_at        timestamptz,
    finished_at       timestamptz,
    created_at        timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX volume_backups_service_idx ON volume_backups (service_id, created_at DESC);
CREATE INDEX volume_backups_destination_idx ON volume_backups (destination_id);
CREATE UNIQUE INDEX volume_backups_command_idx ON volume_backups (command_id) WHERE command_id IS NOT NULL;

-- A restore of one volume of a backup. mode new_volume restores into a NEW
-- Docker volume (docker_name) and stages the swap on the service's volume row
-- (applied by the next deploy); in_place stops the service, overwrites the
-- volume and starts it again, after a safety backup (pre_backup_id).
-- status: pre_backup, restoring, succeeded or failed.
CREATE TABLE volume_restores (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id  uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    backup_id     uuid NOT NULL REFERENCES volume_backups(id) ON DELETE CASCADE,
    service_id    uuid NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    volume_name   text NOT NULL,
    mode          text NOT NULL CHECK (mode IN ('new_volume','in_place')),
    status        text NOT NULL CHECK (status IN ('pre_backup','restoring','succeeded','failed')),
    docker_name   text NOT NULL DEFAULT '',
    pre_backup_id uuid REFERENCES volume_backups(id) ON DELETE SET NULL,
    command_id    uuid,
    error         text NOT NULL DEFAULT '',
    actor_label   text NOT NULL DEFAULT '',
    created_at    timestamptz NOT NULL DEFAULT now(),
    finished_at   timestamptz
);
CREATE INDEX volume_restores_backup_idx ON volume_restores (backup_id);
CREATE INDEX volume_restores_service_idx ON volume_restores (service_id, created_at DESC);
CREATE UNIQUE INDEX volume_restores_command_idx ON volume_restores (command_id) WHERE command_id IS NOT NULL;

-- ============================================================================
-- Metrics, traffic analytics and logs
-- ============================================================================

-- Raw resource samples (≈10 s), kept for 48 hours. service_id is NULL for host
-- samples. replica is the replica index (0-based); host samples and
-- single-replica services use 0.
CREATE TABLE metric_samples (
    server_id         uuid NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
    service_id        uuid REFERENCES services(id) ON DELETE CASCADE,
    ts                timestamptz NOT NULL DEFAULT now(),
    cpu_percent       double precision NOT NULL DEFAULT 0,
    mem_bytes         bigint NOT NULL DEFAULT 0,
    mem_limit_bytes   bigint NOT NULL DEFAULT 0,
    disk_used_bytes   bigint NOT NULL DEFAULT 0,
    disk_total_bytes  bigint NOT NULL DEFAULT 0,
    replica           integer NOT NULL DEFAULT 0
);
CREATE INDEX metric_samples_server_ts_idx ON metric_samples (server_id, ts);
CREATE INDEX metric_samples_service_ts_idx ON metric_samples (service_id, ts);

-- Resource metric downsampling tiers: avg and max per bucket, kept for
-- servers.metrics_retention_days. service_id is NULL for host samples. The
-- rollup tiers hold one row per service: cpu_* / mem_* / mem_limit are the SUM
-- over the replicas of a sample instant, and replicas is the number of replica
-- containers seen in the bucket (maximum), so the per-replica average is
-- cpu_avg / replicas.
CREATE TABLE metric_samples_1m (
    server_id   uuid NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
    service_id  uuid REFERENCES services(id) ON DELETE CASCADE,
    bucket      timestamptz NOT NULL,
    cpu_avg     double precision NOT NULL DEFAULT 0,
    cpu_max     double precision NOT NULL DEFAULT 0,
    mem_avg     bigint NOT NULL DEFAULT 0,
    mem_max     bigint NOT NULL DEFAULT 0,
    mem_limit   bigint NOT NULL DEFAULT 0,
    disk_used   bigint NOT NULL DEFAULT 0,
    disk_total  bigint NOT NULL DEFAULT 0,
    samples     integer NOT NULL DEFAULT 1,
    replicas    integer NOT NULL DEFAULT 1
);
CREATE UNIQUE INDEX metric_samples_1m_key ON metric_samples_1m (server_id, (COALESCE(service_id, '00000000-0000-0000-0000-000000000000'::uuid)), bucket);
CREATE INDEX metric_samples_1m_service_idx ON metric_samples_1m (service_id, bucket);

CREATE TABLE metric_samples_15m (
    server_id   uuid NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
    service_id  uuid REFERENCES services(id) ON DELETE CASCADE,
    bucket      timestamptz NOT NULL,
    cpu_avg     double precision NOT NULL DEFAULT 0,
    cpu_max     double precision NOT NULL DEFAULT 0,
    mem_avg     bigint NOT NULL DEFAULT 0,
    mem_max     bigint NOT NULL DEFAULT 0,
    mem_limit   bigint NOT NULL DEFAULT 0,
    disk_used   bigint NOT NULL DEFAULT 0,
    disk_total  bigint NOT NULL DEFAULT 0,
    samples     integer NOT NULL DEFAULT 1,
    replicas    integer NOT NULL DEFAULT 1
);
CREATE UNIQUE INDEX metric_samples_15m_key ON metric_samples_15m (server_id, (COALESCE(service_id, '00000000-0000-0000-0000-000000000000'::uuid)), bucket);
CREATE INDEX metric_samples_15m_service_idx ON metric_samples_15m (service_id, bucket);

-- Traffic analytics tiers (aggregates only; raw access logs, client IPs, user
-- agents, referrer URLs and query strings are never stored). The agent ships
-- minute buckets; hourly and daily rows are rolled up from them.
--   lat_hist      28 latency counters (bounds in pkg/agentprotocol), merged by summing
--   hll           HyperLogLog p=12 of visitor hashes (bots excluded), merged by union
--   status_codes  {"200":N,"404":N,...}
--   top           {"paths":[{"k","n","e","b","ms"}],"pages":[...],"referrers":[...],"browsers":[...],"os":[...],"devices":[...],"notFound":[...]}
--   geo           request origins, rolled up under the same retention (rows are shared):
--                 {"countries":{"US":12,"DE":3},
--                  "cities":[{"name","country","lat","lon","n"}],   -- top cities, rounded to 0.1°
--                  "otherCities":N,                                 -- located requests beyond the city cap / without a city
--                  "unknown":N}                                     -- requests the database does not locate
--                 '{}' for rows recorded while geolocation was off.
-- service_id is NULL for hosts that route to no service ("unrouted").
CREATE TABLE traffic_1m (
    server_id     uuid NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
    service_id    uuid REFERENCES services(id) ON DELETE SET NULL,
    host          text NOT NULL,
    bucket        timestamptz NOT NULL,
    requests      bigint NOT NULL DEFAULT 0,
    documents     bigint NOT NULL DEFAULT 0,
    bot_requests  bigint NOT NULL DEFAULT 0,
    bytes_in      bigint NOT NULL DEFAULT 0,
    bytes_out     bigint NOT NULL DEFAULT 0,
    s1xx          bigint NOT NULL DEFAULT 0,
    s2xx          bigint NOT NULL DEFAULT 0,
    s3xx          bigint NOT NULL DEFAULT 0,
    s4xx          bigint NOT NULL DEFAULT 0,
    s5xx          bigint NOT NULL DEFAULT 0,
    s_other       bigint NOT NULL DEFAULT 0,
    lat_hist      bigint[] NOT NULL,
    lat_sum_ms    double precision NOT NULL DEFAULT 0,
    visitors      bigint NOT NULL DEFAULT 0,
    hll           bytea NOT NULL DEFAULT ''::bytea,
    status_codes  jsonb NOT NULL DEFAULT '{}'::jsonb,
    top           jsonb NOT NULL DEFAULT '{}'::jsonb,
    geo           jsonb NOT NULL DEFAULT '{}'::jsonb,
    PRIMARY KEY (server_id, host, bucket)
);
CREATE INDEX traffic_1m_service_idx ON traffic_1m (service_id, bucket);
CREATE INDEX traffic_1m_bucket_idx ON traffic_1m (bucket);

CREATE TABLE traffic_1h (
    server_id     uuid NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
    service_id    uuid REFERENCES services(id) ON DELETE SET NULL,
    host          text NOT NULL,
    bucket        timestamptz NOT NULL,
    requests      bigint NOT NULL DEFAULT 0,
    documents     bigint NOT NULL DEFAULT 0,
    bot_requests  bigint NOT NULL DEFAULT 0,
    bytes_in      bigint NOT NULL DEFAULT 0,
    bytes_out     bigint NOT NULL DEFAULT 0,
    s1xx          bigint NOT NULL DEFAULT 0,
    s2xx          bigint NOT NULL DEFAULT 0,
    s3xx          bigint NOT NULL DEFAULT 0,
    s4xx          bigint NOT NULL DEFAULT 0,
    s5xx          bigint NOT NULL DEFAULT 0,
    s_other       bigint NOT NULL DEFAULT 0,
    lat_hist      bigint[] NOT NULL,
    lat_sum_ms    double precision NOT NULL DEFAULT 0,
    visitors      bigint NOT NULL DEFAULT 0,
    hll           bytea NOT NULL DEFAULT ''::bytea,
    status_codes  jsonb NOT NULL DEFAULT '{}'::jsonb,
    top           jsonb NOT NULL DEFAULT '{}'::jsonb,
    geo           jsonb NOT NULL DEFAULT '{}'::jsonb,
    PRIMARY KEY (server_id, host, bucket)
);
CREATE INDEX traffic_1h_service_idx ON traffic_1h (service_id, bucket);
CREATE INDEX traffic_1h_bucket_idx ON traffic_1h (bucket);

CREATE TABLE traffic_1d (
    server_id     uuid NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
    service_id    uuid REFERENCES services(id) ON DELETE SET NULL,
    host          text NOT NULL,
    bucket        timestamptz NOT NULL,
    requests      bigint NOT NULL DEFAULT 0,
    documents     bigint NOT NULL DEFAULT 0,
    bot_requests  bigint NOT NULL DEFAULT 0,
    bytes_in      bigint NOT NULL DEFAULT 0,
    bytes_out     bigint NOT NULL DEFAULT 0,
    s1xx          bigint NOT NULL DEFAULT 0,
    s2xx          bigint NOT NULL DEFAULT 0,
    s3xx          bigint NOT NULL DEFAULT 0,
    s4xx          bigint NOT NULL DEFAULT 0,
    s5xx          bigint NOT NULL DEFAULT 0,
    s_other       bigint NOT NULL DEFAULT 0,
    lat_hist      bigint[] NOT NULL,
    lat_sum_ms    double precision NOT NULL DEFAULT 0,
    visitors      bigint NOT NULL DEFAULT 0,
    hll           bytea NOT NULL DEFAULT ''::bytea,
    status_codes  jsonb NOT NULL DEFAULT '{}'::jsonb,
    top           jsonb NOT NULL DEFAULT '{}'::jsonb,
    geo           jsonb NOT NULL DEFAULT '{}'::jsonb,
    PRIMARY KEY (server_id, host, bucket)
);
CREATE INDEX traffic_1d_service_idx ON traffic_1d (service_id, bucket);
CREATE INDEX traffic_1d_bucket_idx ON traffic_1d (bucket);

-- The MaxMind GeoLite2 account of a team, shared by every server of the team
-- that selects the MaxMind source. The licence key is sealed with secretbox.
CREATE TABLE geo_credentials (
    workspace_id        uuid PRIMARY KEY REFERENCES workspaces(id) ON DELETE CASCADE,
    maxmind_account_id  text  NOT NULL,
    maxmind_key_enc     bytea NOT NULL,
    updated_at          timestamptz NOT NULL DEFAULT now()
);

-- Log history and log drains.
--
-- The runtime log lives on the server that produced it (the agent stores it,
-- compressed, under <state-dir>/logs); the control plane only keeps how long and
-- how much each server retains, a usage snapshot the agent reports, and the
-- drains the agents ship to.
--
-- server_log_settings: one optional row per server; a server without a row uses
-- the defaults (capture on, 7 days, 500 MB per service, 5 GB per server).
-- usage is the agent's last report ({totalBytes, services:[...]}).
CREATE TABLE server_log_settings (
    server_id         uuid PRIMARY KEY REFERENCES servers(id) ON DELETE CASCADE,
    capture           boolean NOT NULL DEFAULT true,
    retention_days    integer NOT NULL DEFAULT 7 CHECK (retention_days BETWEEN 1 AND 90),
    max_service_bytes bigint NOT NULL DEFAULT 524288000 CHECK (max_service_bytes >= 33554432),
    max_server_bytes  bigint NOT NULL DEFAULT 5368709120 CHECK (max_server_bytes >= 33554432),
    usage             jsonb,
    usage_at          timestamptz,
    updated_at        timestamptz NOT NULL DEFAULT now()
);

-- A destination agents ship log lines to. Credentials (tokens, passwords, header
-- values) live in secrets_enc, a secretbox-sealed JSON object that is never
-- returned; config holds the kind's non-secret settings (agentprotocol.*DrainConfig).
-- Scope: all_projects, or only project_ids; service_ids narrows further; query is
-- the log search language, evaluated on the agent.
CREATE TABLE log_drains (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id     uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    name             text NOT NULL,
    kind             text NOT NULL CHECK (kind IN ('loki','otlp','syslog','http')),
    enabled          boolean NOT NULL DEFAULT true,
    config           jsonb NOT NULL DEFAULT '{}'::jsonb,
    secrets_enc      bytea,
    all_projects     boolean NOT NULL DEFAULT true,
    project_ids      uuid[] NOT NULL DEFAULT '{}',
    service_ids      uuid[] NOT NULL DEFAULT '{}',
    query            text NOT NULL DEFAULT '',
    buffer_max_bytes bigint NOT NULL DEFAULT 268435456 CHECK (buffer_max_bytes >= 8388608),
    last_test_at      timestamptz,
    last_test_ok      boolean,
    last_test_message text NOT NULL DEFAULT '',
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX log_drains_name_uniq ON log_drains (workspace_id, lower(name));

-- The health of a drain on one server, as the agent reports it.
CREATE TABLE log_drain_status (
    drain_id        uuid NOT NULL REFERENCES log_drains(id) ON DELETE CASCADE,
    server_id       uuid NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
    state           text NOT NULL CHECK (state IN ('ok','retrying','failing','disabled')),
    last_success_at timestamptz,
    last_error      text NOT NULL DEFAULT '',
    last_error_at   timestamptz,
    sent_lines      bigint NOT NULL DEFAULT 0,
    dropped_lines   bigint NOT NULL DEFAULT 0,
    backlog_bytes   bigint NOT NULL DEFAULT 0,
    reported_at     timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (drain_id, server_id)
);

-- ============================================================================
-- Notifications and service alerts
-- ============================================================================

-- Notifications: channels (email, Slack, Discord, Telegram, signed webhook), one
-- subscription rule per channel and a delivery log.
--
-- Everything that can authenticate (webhook URLs, bot tokens, SMTP passwords,
-- the webhook signing secret) lives in secrets_enc, a secretbox-sealed JSON
-- object that is never returned by the API. config holds the non-secret
-- settings (SMTP host, port, TLS mode, user name, sender, recipients, chat id).
--
-- The rule: event_types lists the subscribed event types; min_severity drops
-- events below it; all_projects = true means the whole workspace (including
-- events that belong to no project, such as server alerts), otherwise only
-- events of project_ids are sent.
CREATE TABLE notification_channels (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id      uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    name              text NOT NULL,
    kind              text NOT NULL CHECK (kind IN ('email','slack','discord','telegram','webhook')),
    enabled           boolean NOT NULL DEFAULT true,
    config            jsonb NOT NULL DEFAULT '{}'::jsonb,
    secrets_enc       bytea,
    event_types       text[] NOT NULL DEFAULT '{}',
    min_severity      text NOT NULL DEFAULT 'info' CHECK (min_severity IN ('info','warning','error')),
    all_projects      boolean NOT NULL DEFAULT true,
    project_ids       uuid[] NOT NULL DEFAULT '{}',
    last_test_at      timestamptz,
    last_test_ok      boolean,
    last_test_message text NOT NULL DEFAULT '',
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX notification_channels_name_uniq ON notification_channels (workspace_id, lower(name));

-- One delivery of one event to one channel. payload is the event as it is
-- rendered (title, body, link, fields), so a retry sends exactly what the first
-- attempt did. last_error never contains a secret: senders scrub URLs and the
-- stored secrets from errors before they get here. status: pending (queued),
-- retrying (an attempt failed, another is scheduled), delivered, failed (gave up).
-- dedupe_key identifies identical events so a repeat within five minutes is
-- not sent twice; test sends (is_test) are exempt.
CREATE TABLE notification_deliveries (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id    uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    channel_id      uuid NOT NULL REFERENCES notification_channels(id) ON DELETE CASCADE,
    event_type      text NOT NULL,
    severity        text NOT NULL CHECK (severity IN ('info','warning','error')),
    title           text NOT NULL,
    project_id      uuid REFERENCES projects(id) ON DELETE SET NULL,
    payload         jsonb NOT NULL,
    dedupe_key      text NOT NULL DEFAULT '',
    is_test         boolean NOT NULL DEFAULT false,
    status          text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','retrying','delivered','failed')),
    attempts        integer NOT NULL DEFAULT 0,
    last_error      text NOT NULL DEFAULT '',
    response_code   integer,
    next_attempt_at timestamptz,
    delivered_at    timestamptz,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX notification_deliveries_channel_idx ON notification_deliveries (channel_id, created_at DESC, id DESC);
CREATE INDEX notification_deliveries_dedupe_idx ON notification_deliveries (channel_id, dedupe_key, created_at) WHERE NOT is_test;
CREATE INDEX notification_deliveries_created_idx ON notification_deliveries (created_at);

-- Service health alerts (service.crashed, service.unhealthy,
-- service.restart_loop, service.recovered) and the "backup missed" alert.

-- Thresholds of the incident detector, one row per workspace (no row = the
-- defaults in internal/svcalerts). Every duration is how long a condition must
-- hold before it is announced, so a deploy blip or a slow start is not.
CREATE TABLE service_alert_settings (
    workspace_id            uuid PRIMARY KEY REFERENCES workspaces(id) ON DELETE CASCADE,
    crash_grace_seconds     integer NOT NULL DEFAULT 30  CHECK (crash_grace_seconds BETWEEN 0 AND 3600),
    unhealthy_grace_seconds integer NOT NULL DEFAULT 120 CHECK (unhealthy_grace_seconds BETWEEN 0 AND 3600),
    loop_restarts           integer NOT NULL DEFAULT 3   CHECK (loop_restarts BETWEEN 2 AND 50),
    loop_window_minutes     integer NOT NULL DEFAULT 10  CHECK (loop_window_minutes BETWEEN 1 AND 1440),
    recover_after_seconds   integer NOT NULL DEFAULT 120 CHECK (recover_after_seconds BETWEEN 10 AND 3600),
    flap_incidents          integer NOT NULL DEFAULT 3   CHECK (flap_incidents BETWEEN 2 AND 20),
    flap_window_minutes     integer NOT NULL DEFAULT 60  CHECK (flap_window_minutes BETWEEN 5 AND 1440),
    -- Grace after a backup schedule's next due time before backup.missed fires;
    -- 0 = automatic (a quarter of the schedule's interval, 15 minutes to 6 hours).
    backup_grace_minutes    integer NOT NULL DEFAULT 0   CHECK (backup_grace_minutes BETWEEN 0 AND 10080),
    -- Runtime log lines attached to a crash or restart-loop message; 0 = none.
    log_lines               integer NOT NULL DEFAULT 5   CHECK (log_lines BETWEEN 0 AND 20),
    updated_at              timestamptz NOT NULL DEFAULT now()
);

-- One row per incident: a stretch of time in which a running service was down,
-- unhealthy or restart-looping. kind is the worst kind reached. At most one
-- incident per service is open (resolved_at IS NULL). flapping marks an
-- incident that absorbed repeated failures (further alerts are suppressed
-- until the service is stable again). resolution: recovered, stopped, deleted.
CREATE TABLE service_incidents (
    id                    uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id          uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    service_id            uuid NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    kind                  text NOT NULL CHECK (kind IN ('unhealthy','crashed','restart_loop')),
    flapping              boolean NOT NULL DEFAULT false,
    detail                text NOT NULL DEFAULT '',
    restarts              integer NOT NULL DEFAULT 0,
    deployment_id         uuid,
    opened_at             timestamptz NOT NULL,
    notified_at           timestamptz,
    resolved_at           timestamptz,
    resolution            text NOT NULL DEFAULT '',
    recovery_notified_at  timestamptz
);
CREATE UNIQUE INDEX service_incidents_open_idx ON service_incidents (service_id) WHERE resolved_at IS NULL;
CREATE INDEX service_incidents_service_idx ON service_incidents (service_id, opened_at DESC);

-- Missed-backup tracking: since is when the schedule was first seen enabled
-- (the reference time until its first success), notified_at when backup.missed
-- was last sent. subject_id is the backup schedule (database) or the service
-- (volume). The row is dropped while the schedule is disabled or gone.
CREATE TABLE backup_missed_alerts (
    kind        text NOT NULL CHECK (kind IN ('database','volume')),
    subject_id  uuid NOT NULL,
    since       timestamptz NOT NULL,
    notified_at timestamptz,
    PRIMARY KEY (kind, subject_id)
);

-- ============================================================================
-- Production guard, audit and idempotency
-- ============================================================================

-- Production guard. An MCP principal (or an API token that opts in) cannot act
-- on a production environment without a human approval. The approval of one
-- call is bound to the principal, the operation, a digest of its arguments and
-- the generation of every target, and expires after at most ten minutes.
CREATE TABLE approvals (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id    uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    project_id      uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    environment_id  uuid NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
    -- The credential that asked: the API token id.
    principal_id    uuid NOT NULL,
    principal_type  text NOT NULL CHECK (principal_type IN ('mcp', 'token')),
    -- The token's name and the MCP clientInfo name, for the approval page.
    principal_label text NOT NULL DEFAULT '',
    client_name     text NOT NULL DEFAULT '',
    -- The domain operation ("deploy", "rollback", ...) and the SHA-256 of its
    -- arguments.
    tool            text NOT NULL,
    args_digest     bytea NOT NULL CHECK (octet_length(args_digest) = 32),
    -- What will happen, shown to the approver: prodguard.Plan.
    plan            jsonb NOT NULL,
    -- target id → generation at request time. The approval is void when any
    -- target moved.
    generations     jsonb NOT NULL DEFAULT '{}'::jsonb,
    -- pending → approved | declined | expired | stale; approved → used.
    status          text NOT NULL DEFAULT 'pending'
                    CHECK (status IN ('pending', 'approved', 'declined', 'used', 'expired', 'stale')),
    decided_by      uuid REFERENCES users(id) ON DELETE SET NULL,
    decided_via     text CHECK (decided_via IN ('web', 'elicitation')),
    decided_at      timestamptz,
    used_at         timestamptz,
    expires_at      timestamptz NOT NULL,
    created_at      timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX approvals_lookup ON approvals (workspace_id, principal_id, tool, args_digest, created_at DESC);
CREATE INDEX approvals_created ON approvals (created_at);

CREATE TABLE audit_events (
    id             bigserial PRIMARY KEY,
    workspace_id   uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    actor_type     text NOT NULL CHECK (actor_type IN ('user','token','mcp','system')),
    actor_id       text NOT NULL DEFAULT '',
    actor_label    text NOT NULL DEFAULT '',
    action         text NOT NULL,
    resource_type  text NOT NULL,
    resource_id    text NOT NULL DEFAULT '',
    project_id     uuid,
    metadata       jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at     timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX audit_events_ws_id_idx ON audit_events (workspace_id, id DESC);
-- Audit filters: by project and by resource.
CREATE INDEX audit_events_project_idx ON audit_events (workspace_id, project_id, id DESC);
CREATE INDEX audit_events_resource_idx ON audit_events (workspace_id, resource_id, id DESC);

CREATE TABLE idempotency_keys (
    principal_id  text NOT NULL,
    key           text NOT NULL,
    method        text NOT NULL,
    path          text NOT NULL,
    status        integer NOT NULL,
    body          jsonb,
    created_at    timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (principal_id, key)
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE services DROP CONSTRAINT services_current_deployment_fk;
ALTER TABLE projects DROP CONSTRAINT projects_registry_id_fkey;

DROP TABLE
    idempotency_keys, audit_events, approvals, backup_missed_alerts,
    service_incidents, service_alert_settings, notification_deliveries, notification_channels,
    log_drain_status, log_drains, server_log_settings, geo_credentials,
    traffic_1d, traffic_1h, traffic_1m, metric_samples_15m,
    metric_samples_1m, metric_samples, volume_restores, volume_backups,
    volume_backup_configs, volume_repos, dump_restores, backup_restores,
    backups, backup_schedules, backup_destinations, database_fences,
    database_promotions, database_replicas, database_upgrades, database_info,
    database_rotations, mesh_grants, mesh_exposures, mesh_links,
    server_mesh, mesh_settings, service_networks, shared_networks,
    service_tailnet, tailnet_integrations, server_tunnels, cloudflare_zone_aop,
    cloudflare_ip_lists, cloudflare_records, cloudflare_connections, wildcard_certs,
    server_dns_resolvers, dns_credentials, route_ports, domain_auth_users,
    domains, certificates, previews, preview_settings,
    git_status_reports, git_deliveries, git_delivery_ids, service_git,
    git_webhooks, git_connections, task_run_logs, task_runs,
    service_tasks, registry_images, agent_commands, deployment_logs,
    deployment_events, deployments, releases, deploy_hooks,
    service_ssh_hosts, service_ssh_keys, retired_volumes, volumes,
    service_links, secret_integration_scopes, secret_integrations, variable_versions,
    shared_variables, service_variables, services, template_instances,
    team_build_settings, registries, platform_services, project_terminal_settings,
    canvas_layouts,
    environments, projects, server_agent_upgrades, server_patch_runs,
    server_patch_state, server_hardening_runs, server_hardening_state, server_hardening_settings,
    server_cleanup_runs, server_cleanup_settings, server_host_shell, server_drains,
    enrollment_tokens, agent_identities, servers, dashboard_access,
    instance_upgrade, instance_backups, instance_backup_settings, instance_settings,
    user_identities, auth_providers, auth_settings, user_passkeys,
    user_recovery_codes, user_totp, password_reset_tokens, team_invite_uses,
    team_invites, api_tokens, sessions, memberships,
    workspaces, users;
-- +goose StatementEnd
