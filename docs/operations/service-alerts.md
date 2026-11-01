---
title: Service alerts
description: "Crash, unhealthy, restart-loop and missed-backup alerts: how incidents are detected, announced once and closed, and the thresholds."
section: Observability
order: 41
---

# Service alerts

NebulaCtrl tells you when a running service goes wrong, once, and tells you when it is well again. Four events carry it, plus one for backups:

| Event | Severity | Sent when |
|---|---|---|
| `service.crashed` | error | A container exited and stayed down, or Docker restarted it after a crash |
| `service.unhealthy` | warning | The container runs but its health check keeps failing |
| `service.restart_loop` | error | Docker restarted the container 3 times in 10 minutes, it sits in the `restarting` state, or the service keeps failing again and again (see [Flapping](#flapping)) |
| `service.recovered` | the severity of the alert it closes | The incident ended: the service has been healthy for 2 minutes |
| `backup.missed` | error | A scheduled database or volume backup did not succeed within its window ([below](#missed-backups)) |

They are ordinary notification events. A [channel](notifications.md) receives the ones it selects in its event matrix (the **Services** and **Backups** groups), at or above its minimum severity, for the projects it covers. All five are on by default for new channels. On an existing install the upgrade adds them to every channel that already receives `deploy.failed`; other channels stay as you configured them.

## What is watched

The agent reports every service's state every 15 seconds: per replica its state, health and Docker's restart counter. The control plane feeds each report to a small state machine per service (`internal/svcalerts`). Nothing extra runs on your servers.

A service is **not** watched while

- a deployment of it is active (the container is being replaced on purpose);
- you stopped it (`desiredState: stopped`). Stopping a service with an open incident closes the incident without a recovery message;
- its server is offline (no reports arrive: [`server.offline`](notifications.md) covers that). When reports resume after more than 2 minutes of silence, the evidence gathered before (how long it was down, restart counters) is discarded.

A new release starts with a clean slate: its containers are new, so their restart counters begin at 0.

A compose stack component that finished cleanly (a one-shot job that exited with code 0) is healthy, not crashed.

## One incident, announced once

A stretch of trouble is one **incident**. It has a kind, escalates but never de-escalates, and produces:

1. **One alert** when it opens, with the cause and, for a crash or restart loop, the last runtime log lines.
2. **One more alert** only if it gets worse: unhealthy, then crashed, then restart loop (each kind at most once per incident).
3. **One recovery** (`service.recovered`) after the service has been healthy for the *healthy-for* time. A blip during that time restarts the clock, so a service that bounces never recovers early.

Nothing repeats while the incident stays open, however long it lasts. A restart of the control plane does not re-announce an open incident: incidents are stored (`service_incidents`) and resumed.

Every message carries an `Incident` field (the first 8 characters of the incident id), so two incidents of the same service are never merged by the 5-minute duplicate filter.

### Debounce

A short blip is not an incident:

- **Down:** a container must stay down (exited, or missing) for the *down-for* time (30 s) before it is announced. A restart by Docker after a crash is the exception: the counter proves the crash happened, so it is announced at once.
- **Unhealthy:** the health check must fail for the whole *unhealthy-for* time (2 min). `starting` is not unhealthy, so a slow start never alerts.
- **Restart loop:** *restart-loop-at* restarts (3) inside the *restart window* (10 min).

### Flapping

A service that fails, recovers and fails again would otherwise send an alert and a recovery every few minutes. When the *flapping-at* th incident (3) opens inside the *flap window* (1 hour), that incident is announced once as `service.restart_loop` ("keeps failing") and **marked flapping**: from then on it is silent, absorbing every further failure and recovery. It closes, with one `service.recovered`, only after the service has been stable for a whole flap window. The next failure after that is a normal incident again. So the most a flapping service can send in an hour is two alert/recovery pairs and one flapping notice.

## Thresholds

**Team settings → Notifications → Service alerts** (admin scope; `GET/PUT /api/v1/notifications/alert-settings`) sets them for the workspace. They apply to every channel; which channel hears about which event is each channel's own event selection.

| Setting | Default | Range | Meaning |
|---|---|---|---|
| Down for | 30 s | 0 to 3600 | How long a container stays down before `service.crashed` |
| Unhealthy for | 120 s | 0 to 3600 | How long the health check fails before `service.unhealthy` |
| Restart loop at | 3 | 2 to 50 | Docker restarts inside the restart window |
| Restart window | 10 min | 1 to 1440 | How far back restarts are counted |
| Healthy for | 120 s | 10 to 3600 | Stable time before an incident closes |
| Flapping at | 3 | 2 to 20 | Incidents inside the flap window |
| Flap window | 60 min | 5 to 1440 | How far back incidents are counted, and the stable time a flapping service needs |
| Backup grace | 0 (automatic) | 0 to 10080 min | Extra time a backup gets after its due time |
| Log lines | 5 | 0 to 20 | Runtime log lines attached to a crash or restart-loop message |

A change takes effect within a minute and is audited (`notifications.alert_settings_update`).

### Log lines

A crash or restart-loop alert attaches the last runtime lines of the service so the cause is in the message. The lines are masked for the service's own secrets (the same redaction as the log viewer), control characters are removed and each line is cut at 200 characters. Notifications go to third parties (Slack, Discord, email), and application output can still hold data you would not want there, so set *Log lines* to 0 if your logs are sensitive. An unhealthy alert never carries logs. If the log tail cannot be read (the agent is busy or the container is gone) the alert is sent without it.

## Where it shows

- **Notifications:** the five events above, in the channel's event matrix and the delivery log.
- **Audit log:** `service.incident_opened`, `service.incident_escalated` and `service.incident_resolved` (actor `system`; the metadata has the kind, the detail, the incident id and, when closed, the resolution `recovered` or `stopped`).
- **Service Overview:** a *Health incidents* card with the last 5 incidents, newest first (shown once the service had one).
- **API:** `GET /api/v1/services/{id}/incidents` (`projects:read`). The MCP tool catalogue exposes it and the settings read through `call_operation`; changing the thresholds is not available over MCP.

## Missed backups

`backup.missed` closes the gap left by `backup.failed`: a schedule that silently stops (the control plane or the server was down, the destination is unreachable, every run is skipped) would otherwise raise nothing.

An enabled schedule, database or volume, is **missed** when no backup succeeded by its *deadline*:

> deadline = the next due time after the last success + a grace

The grace is the *backup grace* setting; with 0 it is automatic, a quarter of the interval to the following run, at least 15 minutes and at most 6 hours (so 15 min for an hourly schedule, 6 h for a daily or weekly one). The next due time comes from the cron expression, so irregular schedules (`0 3 * * 1-5`) are handled.

- The last success is the newest succeeded backup of that schedule (for a database also a manual backup of the database; for volumes any non-restore backup). A failed or skipped run does not count.
- A schedule is tracked from the first check (every 15 minutes) that sees it enabled, so a new or re-enabled schedule gets a whole interval before it can be missed.
- A run that is still queued or running is not a miss.
- A database you stopped is not checked (its scheduled runs are *skipped* and already raise `backup.failed` as warnings).
- It is sent **once per streak**. The next successful backup ends the streak; missing the next window announces again. The success itself is the recovery: it raises `backup.succeeded` if a channel selected it.

The message names the service, the destination and the schedule, says when the last backup succeeded (or that none has since the schedule was enabled) and when one was due.

## Not included

- **No restart cap.** NebulaCtrl only announces a restart loop; a crash-looping container keeps restarting under Docker's `unless-stopped` policy until you stop or fix it.
- Detection runs on the 15-second observed reports, so the shortest crash alert is about 15 s after the crash (plus the down-for time).
- Thresholds are per workspace, not per service or per channel.
