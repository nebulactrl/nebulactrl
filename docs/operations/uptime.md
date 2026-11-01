---
title: Uptime monitoring
description: "Probes that run from each server every 30 seconds, the six states, availability and its math, failure and recovery thresholds, maintenance, uptime.down and uptime.up alerts, retention and the uptime API."
section: Observability
order: 42
---

# Uptime monitoring

NebulaCtrl checks every service it runs, from the inside, every 30 seconds, and tells you whether it is up. Each service has a **state**, an **availability** over the last 24 hours, 7, 30 and 90 days, a list of **outages**, and an alert when one starts and when it ends. Nothing needs to be set up: a service is monitored from its first deployment.

This is different from [service alerts](service-alerts.md), which read the container's state (crashed, restart loop). Uptime asks the service itself: does it answer?

## How checks work

The agent on each server probes the services it runs. The probe uses the service's own health check, so it measures what your health check says:

| Service | A check passes when |
|---|---|
| App or worker, health check `http` | `GET` of the path returns 200 to 399 within the check's timeout (redirects are not followed) |
| `tcp` | the port accepts a connection |
| `docker` or `exec` | Docker reports the container healthy (`starting` counts as passing during the start period) |
| `none` | the container is running and not restarting |
| Route to an external upstream | a TCP connect to each upstream, or a `GET` of the route's health path when it has one |
| Compose stack | every member container runs and is healthy |

The probe goes over the environment's Docker network, so a service with no public domain is checked too, and the check does not depend on DNS, the proxy or the certificate. Use a route's health path or a domain check elsewhere if you want to verify the public side.

A **check** is one probe cycle of the service. With several replicas, every replica is probed:

- all replicas pass: **up**;
- some pass: **degraded**;
- none pass, or none runs although some should: **down**;
- no replica is wanted (the service is stopped or scaled to 0): **paused**. A paused result is not a check and counts nowhere.

The latency of a check is the slowest passing HTTP or TCP probe; checks without a network probe (Docker health, `none`) have none.

Agents keep results on disk until the control plane has stored them, and send them again after a reconnect, so a short outage of the link loses nothing. The control plane recognises results it already has (by time per service) and counts each check once. Results older than 6 hours are dropped by the agent. An agent that is offline sends nothing; those periods have no data (see [unknown](#states)).

Agents older than the uptime feature do not report; their services stay `unknown`.

## States

| State | Meaning |
|---|---|
| `up` | The last checks passed |
| `degraded` | Some replicas fail and at least one passes. Shown at once, never alerts |
| `down` | An outage is open: the failure threshold was reached and the recovery threshold is not yet |
| `maintenance` | Maintenance mode or a [maintenance window](#maintenance) covers the service |
| `paused` | The service is stopped on purpose, or monitoring is switched off |
| `unknown` | No check yet, or no result for more than 2 minutes (three missed cycles and a margin): the agent is not reporting |

`unknown` is never `down`: a server that stopped reporting has its own [`server.offline`](notifications.md) alert, and one lost link must not look like an outage of every service on the server. An outage that was open stays open, silently, until checks return and show the service healthy.

## Thresholds

One failed check is often a blip. Per service you set, with the [API below](#api):

| Setting | Default | Meaning |
|---|---|---|
| `failureThreshold` | 3 | consecutive **down** checks that open an outage (90 seconds) |
| `recoveryThreshold` | 2 | consecutive **up or degraded** checks that end it (60 seconds) |
| `alerts` | on | send `uptime.down` / `uptime.up` |
| `reminderMinutes` | 0 (off) | while an outage lasts, send `uptime.down` again every this many minutes, 0 to 1440 ([reminders](#reminders)) |
| `enabled` | on | probe the service at all |

Both thresholds go from 1 to 20. A passing check resets the failure streak, and a failing check resets the recovery streak. Degraded checks count as passing.

Until the threshold is reached nothing is an outage: the service keeps its state, and the API shows the streak as `consecutiveFailures` next to the effective `failureThreshold` ("failing 2/3").

**Deployments.** While a deployment of the service is in flight, failing checks are still recorded (availability tells the truth) but cannot open an outage. When the deployment is over, the threshold starts from zero: a service that is still down after its deployment becomes an outage after `failureThreshold` more checks. An outage that is already open ends normally, even during a deployment.

## Outages and alerts

When the failure threshold is reached the service becomes `down`, an **outage** (incident) opens at that check with the error the probe reported (`HTTP 503`, `connection refused`, `unhealthy: ...`), and one `uptime.down` notification (severity error) is sent. When the recovery threshold is reached the outage ends and one `uptime.up` (shown green, sent with the severity of the alert it closes, so a channel that got one gets the other) tells how long it lasted. A service never has two open outages, and a flapping service sends one pair per outage.

Both are ordinary [notification events](notifications.md) in the **Services** group and on by default. On upgrade they are added to every channel that already receives `service.unhealthy`. The message names the service, environment and project, the cause and, on recovery, the duration.

With `alerts` off, outages are still recorded and shown but nothing is sent. Switching `alerts` off during an open outage still sends its `uptime.up`, so the `uptime.down` that went out is never left dangling.

Stopping a service, or switching monitoring off, closes an open outage **without** a recovery message: the service did not come back, it was stopped.

### Reminders

A long outage is easy to forget. With `reminderMinutes` above 0 and `alerts` on, `uptime.down` is sent again every that many minutes while the outage stays open, counted from the last alert or reminder. The reminder says so ("Reminder: ... has been down for 2 h") and how long the outage has lasted. Reminders are checked once a minute, so one can be up to a minute late. There are none while the service is in maintenance, while its state is `unknown` (the agent is not reporting) or `paused`, for an outage whose first alert was never sent (alerts were off when it opened), or after `alerts` is switched off. A reminder is never sent twice. Each is the same `uptime.down` event, so channels need no new setting.

## Maintenance

A service is in maintenance while

- its **maintenance mode** is on (see [Maintenance mode](maintenance-and-error-pages.md)), or
- an active, not canceled **maintenance window** that lists it covers the check.

Checks taken in maintenance are counted as `maintenance`, whatever the probe found. The state is `maintenance`, no outage can open and no alert is sent; failures do not count towards the threshold. An outage that was already open stays open and ends when the recovery threshold is met. Maintenance is judged by the time of the check: a window covers the checks between its start (inclusive) and end (exclusive). Maintenance mode covers checks from the moment it was switched on.

## Availability

```
availability = (up + degraded) / (checks - maintenance)
```

Maintenance leaves both sides of the fraction, so planned work never lowers it. Periods without checks (an offline agent, a service that was not yet monitored) have no row and are **no data**, not 100 %: a window with nothing to judge reports `null`. Paused results are not checks.

The API reports the availability of

- `24h`: the last 24 hours, in hour buckets;
- `7d`, `30d`, `90d`: the last 7, 30 or 90 UTC days, today so far included.

## Storage and retention

Checks are summed per service and minute as they arrive (`checks`, `up`, `degraded`, `down`, `maintenance`, and the latency sum and maximum), then rolled into hours (every 10 minutes, recomputing the last 3 hours) and UTC days (hourly, the last 2 days; the same job repairs hours up to a day old after a late upload). Rollups are recomputed from the tier below, so they are idempotent. Individual probe results are not stored on the control plane.

| Tier | Kept |
|---|---|
| Minutes | 48 hours |
| Hours | 35 days |
| Days | 400 days |

Outages are kept until the service is deleted.

## In the dashboard

- **Service Overview, Uptime card.** The state with its icon and since when; "Failing 2/3" in amber while failed checks are counting towards an outage and the state is still `up` (the dashboard shows it, alerts and status pages never do); the last error; availability for 24 hours, 7, 30 and 90 days (a dash when there is nothing to judge, never 0%); one strip of checks with a **1h / 24h / 7d / 90d** toggle (a cell per minute, hour or UTC day; hover or use the arrow keys for its time, checks, latency and error); average and slowest latency of the strip's range; and the last five outages with their duration. A service without a check yet says so. A Compose stack shows the card once it has checks.
- **Service Settings → Operations → Uptime monitoring.** Monitoring on or off, alerts, failed checks before an outage and passing checks to recover (1 to 20 each), and the reminder interval (off, 15, 30, 60, 120 minutes or your own). Apply takes effect at once; no deploy runs.
- **Environment Observability → Uptime.** One row per service: state, 30-day availability, a compact 30-day bar and the last check, down and degraded services first. A row opens the service's Overview. A long list shows its first five rows plus every service that is down, degraded, without data or failing; *Show more* reveals the rest and says how many there are and in which state.

All three refresh by themselves when a service changes state, and every 30 seconds otherwise.

## API

Every endpoint is under `/api/v1`. Reads need `projects:read` on the service's project; changing a monitor needs the `admin` scope (workspace owner or admin).

| Operation | Endpoint | What it does |
|---|---|---|
| `getServiceUptime` | `GET /services/{serviceId}/uptime?range=1h\|24h\|7d\|30d\|90d` | State, since when, last check, last error, latency; `consecutiveFailures` and `failureThreshold`; availability for 24h, 7d, 30d, 90d; the series of the range (60 minute cells for `1h`, hourly points for 24h and 7d, daily for 30d and 90d; every bucket present, with `checks` 0 where there was no data, so a "last hour" strip can be drawn as it is; per bucket: checks, up, degraded, down, maintenance, average and maximum latency) and the 20 most recent outages. Average latency is the latency sum over the **passing** checks (`up + degraded`) only, since failing and maintenance checks measure none |
| `getEnvironmentUptime` | `GET /environments/{environmentId}/uptime` | Every service of the environment with its state, 30-day availability and 30 daily points |
| `getUptimeMonitor` | `GET /services/{serviceId}/uptime-monitor` | `enabled`, `alerts`, `failureThreshold`, `recoveryThreshold`, `reminderMinutes` (the defaults when never set) |
| `updateUptimeMonitor` | `PUT /services/{serviceId}/uptime-monitor` | Replaces them (all five fields). No deployment needed: the server's agent stops or resumes probing within seconds. Audited as `uptime_monitor.update` |

The dashboard refreshes on the live-update event `uptime.state.changed`.

Over MCP the three reads are available (observe toolset); `updateUptimeMonitor` is not, because it is workspace administration.

## Limits

- Probes check what the health check checks. A service whose health check is `none` is "up" while its container runs.
- Checks run every 30 seconds: shorter outages can be missed, and an outage is detected after `failureThreshold` checks, 90 seconds by default.
- The state shown after a control-plane restart is the stored one; no check is lost, because agents resend what was not acknowledged.
- Maintenance mode is judged by its current setting: a check taken in maintenance mode that was switched off before the agent delivered it counts as a normal check.
