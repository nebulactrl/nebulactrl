-- +goose Up
-- Uptime monitoring (internal/uptime) and status pages (internal/statuspages).
--
-- Every agent probes the services it runs from inside, over the Docker
-- network, every 30 s (the service's health check: an HTTP or TCP probe, the
-- container's Docker health for docker/exec checks, the container running for
-- none; a route's upstream and health path). Results come in minute buckets
-- and drive one state per service.

-- uptime_monitors: per-service settings that apply without a deploy. A service
-- without a row uses the defaults (monitored, alerting, 3 failed checks to go
-- down, 2 good checks to recover, no reminders). reminder_minutes > 0 repeats
-- uptime.down every that many minutes while the outage lasts.
CREATE TABLE uptime_monitors (
    service_id         uuid PRIMARY KEY REFERENCES services(id) ON DELETE CASCADE,
    workspace_id       uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    enabled            boolean NOT NULL DEFAULT true,
    alerts             boolean NOT NULL DEFAULT true,
    failure_threshold  integer NOT NULL DEFAULT 3 CHECK (failure_threshold BETWEEN 1 AND 20),
    recovery_threshold integer NOT NULL DEFAULT 2 CHECK (recovery_threshold BETWEEN 1 AND 20),
    reminder_minutes   integer NOT NULL DEFAULT 0 CHECK (reminder_minutes BETWEEN 0 AND 1440),
    updated_at         timestamptz NOT NULL DEFAULT now()
);

-- uptime_state: the current state of each monitored service.
--   up           the last checks passed
--   degraded     some replicas failed and at least one passed
--   down         failure_threshold checks in a row failed
--   maintenance  maintenance mode or a maintenance window covers the service
--   paused       stopped on purpose (no replicas wanted), or monitoring is off
--   unknown      no checks yet, or the agent stopped reporting (never "down":
--                an offline server is its own alert)
CREATE TABLE uptime_state (
    service_id            uuid PRIMARY KEY REFERENCES services(id) ON DELETE CASCADE,
    workspace_id          uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    state                 text NOT NULL DEFAULT 'unknown'
                          CHECK (state IN ('up','degraded','down','maintenance','paused','unknown')),
    since                 timestamptz NOT NULL DEFAULT now(),
    consecutive_failures  integer NOT NULL DEFAULT 0,
    consecutive_successes integer NOT NULL DEFAULT 0,
    last_check_at         timestamptz,
    last_ok_at            timestamptz,
    last_error            text NOT NULL DEFAULT '',
    last_latency_ms       integer,
    updated_at            timestamptz NOT NULL DEFAULT now()
);

-- uptime_incidents: outages found by the probes (state down), at most one open
-- per service. notified_at / recovery_notified_at record the uptime.down and
-- uptime.up notifications, reminded_at the last reminder.
CREATE TABLE uptime_incidents (
    id                   uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id         uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    service_id           uuid NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    started_at           timestamptz NOT NULL,
    resolved_at          timestamptz,
    cause                text NOT NULL DEFAULT '',
    notified_at          timestamptz,
    reminded_at          timestamptz,
    recovery_notified_at timestamptz
);
CREATE UNIQUE INDEX uptime_incidents_open_idx ON uptime_incidents (service_id) WHERE resolved_at IS NULL;
CREATE INDEX uptime_incidents_service_idx ON uptime_incidents (service_id, started_at DESC);

-- Check counts per service and minute, hour and UTC day. A check is one
-- probe cycle of the service (all its replicas): up, degraded, down, or taken
-- during maintenance. Availability = (up + degraded) / (checks - maintenance);
-- periods without checks (agent offline) have no row and count as no data.
-- Retention: minutes 48 h, hours 35 days, days 400 days.
CREATE TABLE uptime_1m (
    service_id     uuid NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    bucket         timestamptz NOT NULL,
    checks         integer NOT NULL DEFAULT 0,
    up             integer NOT NULL DEFAULT 0,
    degraded       integer NOT NULL DEFAULT 0,
    down           integer NOT NULL DEFAULT 0,
    maintenance    integer NOT NULL DEFAULT 0,
    latency_sum_ms bigint NOT NULL DEFAULT 0,
    latency_max_ms integer NOT NULL DEFAULT 0,
    PRIMARY KEY (service_id, bucket)
);
CREATE TABLE uptime_1h (LIKE uptime_1m INCLUDING ALL);
ALTER TABLE uptime_1h ADD FOREIGN KEY (service_id) REFERENCES services(id) ON DELETE CASCADE;
CREATE TABLE uptime_1d (LIKE uptime_1m INCLUDING ALL);
ALTER TABLE uptime_1d ADD FOREIGN KEY (service_id) REFERENCES services(id) ON DELETE CASCADE;

-- Public status pages. A published page with a hostname is served read-only
-- on that hostname by the control plane's status listener; the dashboard is
-- never reachable through it. Components are the services shown, under a
-- public display name, optionally grouped and linked (url: an https link the
-- component name points to). footer is plain text shown under the page.
CREATE TABLE status_pages (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    title        text NOT NULL,
    slug         text NOT NULL,
    description  text NOT NULL DEFAULT '',
    footer       text NOT NULL DEFAULT '',
    hostname     text UNIQUE,
    published    boolean NOT NULL DEFAULT false,
    show_uptime  boolean NOT NULL DEFAULT true,
    theme        text NOT NULL DEFAULT 'auto' CHECK (theme IN ('auto','light','dark')),
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    UNIQUE (workspace_id, slug)
);
CREATE TABLE status_page_components (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    page_id      uuid NOT NULL REFERENCES status_pages(id) ON DELETE CASCADE,
    service_id   uuid NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    display_name text NOT NULL,
    group_name   text NOT NULL DEFAULT '',
    url          text NOT NULL DEFAULT '',
    position     integer NOT NULL,
    UNIQUE (page_id, service_id)
);

-- Incidents written by people and posted to status pages, with their
-- timeline of updates and the components they affect.
CREATE TABLE status_incidents (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    title        text NOT NULL,
    impact       text NOT NULL CHECK (impact IN ('none','minor','major','critical')),
    status       text NOT NULL CHECK (status IN ('investigating','identified','monitoring','resolved')),
    created_by   uuid REFERENCES users(id) ON DELETE SET NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    resolved_at  timestamptz
);
CREATE INDEX status_incidents_workspace_idx ON status_incidents (workspace_id, created_at DESC);
CREATE TABLE status_incident_updates (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    incident_id uuid NOT NULL REFERENCES status_incidents(id) ON DELETE CASCADE,
    status      text NOT NULL CHECK (status IN ('investigating','identified','monitoring','resolved')),
    body        text NOT NULL,
    created_by  uuid REFERENCES users(id) ON DELETE SET NULL,
    created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX status_incident_updates_incident_idx ON status_incident_updates (incident_id, created_at);
CREATE TABLE status_incident_pages (
    incident_id uuid NOT NULL REFERENCES status_incidents(id) ON DELETE CASCADE,
    page_id     uuid NOT NULL REFERENCES status_pages(id) ON DELETE CASCADE,
    PRIMARY KEY (incident_id, page_id)
);
CREATE TABLE status_incident_services (
    incident_id uuid NOT NULL REFERENCES status_incidents(id) ON DELETE CASCADE,
    service_id  uuid NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    PRIMARY KEY (incident_id, service_id)
);

-- Scheduled maintenance: while a window is active its services are in
-- maintenance (no uptime.down alerts, excluded from availability), and the
-- pages it is posted to announce it ahead of time.
CREATE TABLE maintenance_windows (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    title        text NOT NULL,
    description  text NOT NULL DEFAULT '',
    starts_at    timestamptz NOT NULL,
    ends_at      timestamptz NOT NULL,
    canceled_at  timestamptz,
    created_by   uuid REFERENCES users(id) ON DELETE SET NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    CHECK (ends_at > starts_at)
);
CREATE INDEX maintenance_windows_active_idx ON maintenance_windows (workspace_id, ends_at) WHERE canceled_at IS NULL;
CREATE TABLE maintenance_window_services (
    window_id  uuid NOT NULL REFERENCES maintenance_windows(id) ON DELETE CASCADE,
    service_id uuid NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    PRIMARY KEY (window_id, service_id)
);
CREATE TABLE maintenance_window_pages (
    window_id uuid NOT NULL REFERENCES maintenance_windows(id) ON DELETE CASCADE,
    page_id   uuid NOT NULL REFERENCES status_pages(id) ON DELETE CASCADE,
    PRIMARY KEY (window_id, page_id)
);

-- Channels that already follow service health also get the new uptime events.
UPDATE notification_channels
   SET event_types = event_types || ARRAY['uptime.down', 'uptime.up']
 WHERE 'service.unhealthy' = ANY (event_types)
   AND NOT ('uptime.down' = ANY (event_types));

-- +goose Down
UPDATE notification_channels
   SET event_types = array_remove(array_remove(event_types, 'uptime.down'), 'uptime.up');
DROP TABLE maintenance_window_pages;
DROP TABLE maintenance_window_services;
DROP TABLE maintenance_windows;
DROP TABLE status_incident_services;
DROP TABLE status_incident_pages;
DROP TABLE status_incident_updates;
DROP TABLE status_incidents;
DROP TABLE status_page_components;
DROP TABLE status_pages;
DROP TABLE uptime_1d;
DROP TABLE uptime_1h;
DROP TABLE uptime_1m;
DROP TABLE uptime_incidents;
DROP TABLE uptime_state;
DROP TABLE uptime_monitors;
