-- +goose Up
-- Automatic operating system updates of a server: apply the pending updates
-- every night inside a maintenance window. A server without a row has them off.
--
-- scope 'security' upgrades only the packages with a security update, 'all'
-- every package. The window starts at window_start (HH:MM) in timezone and lasts
-- window_minutes; the control plane adds a stable per-server jitter. reboot
-- 'whenRequired' lets a successful run reboot a server that asks for it (with
-- guards); 'never' leaves that to a person. hold_packages are extra to the
-- built-in holds (Docker and containerd packages), which are always excluded.
--
-- consecutive_failures counts failed scheduled runs in a row; three disable the
-- schedule (enabled = false, disabled_reason says why). settings_changed_at moves
-- when a person saves the settings, so a window already under way waits for the
-- next night instead of starting at once. native_handoff is true while the host's
-- own automatic updates (unattended-upgrades, dnf-automatic) are switched off in
-- favour of this schedule; the agent confirms each change.
CREATE TABLE server_patch_auto_settings (
    server_id            uuid PRIMARY KEY REFERENCES servers(id) ON DELETE CASCADE,
    workspace_id         uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    enabled              boolean NOT NULL DEFAULT false,
    scope                text NOT NULL DEFAULT 'security' CHECK (scope IN ('security', 'all')),
    window_start         text NOT NULL DEFAULT '03:00' CHECK (window_start ~ '^([01][0-9]|2[0-3]):[0-5][0-9]$'),
    window_minutes       integer NOT NULL DEFAULT 120 CHECK (window_minutes BETWEEN 60 AND 240),
    timezone             text NOT NULL DEFAULT 'UTC',
    reboot               text NOT NULL DEFAULT 'never' CHECK (reboot IN ('never', 'whenRequired')),
    hold_packages        text[] NOT NULL DEFAULT '{}',
    consecutive_failures integer NOT NULL DEFAULT 0,
    disabled_reason      text NOT NULL DEFAULT '',
    native_handoff       boolean NOT NULL DEFAULT false,
    settings_changed_at  timestamptz NOT NULL DEFAULT now(),
    updated_by           uuid REFERENCES users(id) ON DELETE SET NULL,
    updated_at           timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX server_patch_auto_enabled_idx ON server_patch_auto_settings (server_id) WHERE enabled OR native_handoff;

-- slot_start is the start of the maintenance window a scheduled upgrade (and the
-- reboot that follows it) belongs to. A window starts at most one upgrade.
ALTER TABLE server_patch_runs ADD COLUMN slot_start timestamptz;
CREATE UNIQUE INDEX server_patch_runs_slot_idx ON server_patch_runs (server_id, slot_start)
    WHERE action = 'apply' AND trigger = 'scheduled' AND slot_start IS NOT NULL;
-- Scheduled work in flight anywhere (the one-at-a-time rule) and recent scheduled reboots.
CREATE INDEX server_patch_runs_scheduled_idx ON server_patch_runs (created_at DESC)
    WHERE trigger = 'scheduled' AND action IN ('apply', 'reboot');

-- The host's own automatic update mechanism the last check found active:
-- 'unattended-upgrades', 'dnf-automatic' or empty.
ALTER TABLE server_patch_state ADD COLUMN native_auto_updates text NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE server_patch_state DROP COLUMN native_auto_updates;
DROP INDEX server_patch_runs_scheduled_idx;
DROP INDEX server_patch_runs_slot_idx;
ALTER TABLE server_patch_runs DROP COLUMN slot_start;
DROP TABLE server_patch_auto_settings;
