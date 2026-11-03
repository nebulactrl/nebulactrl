---
title: Tasks and cron jobs
description: "Scheduled jobs, one-off commands, and commands before and after a deploy."
section: Applications
order: 110
---

# Tasks: cron jobs, one-off commands and the pre- and post-deploy commands

Four ways to run a command next to a service. All of them run in a **throwaway
container of the service's own image**, on the service's server, with the
service's environment variables, on its environment network (so
`postgres:5432` resolves) and the edge network. They publish no ports, never
restart, use the service's memory and CPU limits, have a timeout, and the
container is removed when the command ends, whatever its outcome.

The command is an **argv array without a shell**. Use `["sh", "-c", "…"]` for
shell syntax. It replaces the image's entrypoint (like `docker exec`, and
unlike `docker run image args`), so an image with `ENTRYPOINT ["/app/server"]`
runs `["/app/server", "migrate"]` as written.

## Cron tasks

Per service, up to 20 tasks: `name`, `action`, `schedule`, `command`, `enabled`,
`timeoutSeconds` (default 1800), `concurrency` (`forbid` or `allow`).

- **Action**: `command` (default) runs `command` in a throwaway container as
  described above. `restart` **restarts the service's running container** on the
  schedule instead, for services that leak memory or need a periodic refresh; it
  takes no command or timeout (the runs record a fixed 300 seconds). Every
  replica restarts, so a service with one replica has a brief outage. A restart
  task is refused on a one-shot service (`422 restart_not_applicable`), is
  skipped like any other tick while the service is stopped, and never changes the
  service's desired state. Its runs appear in the run history with an empty
  command, finished by the restart's result. In production the guard and the
  `deployments:write` scope apply as for a command task. Create one with
  `action: "restart"` on `POST /services/{id}/tasks`,
  `nebulactrl tasks create --action restart`, `action = "restart"` in
  `nebula.toml`, or **Action → Restart service** in the task dialog (the card
  menu's **Schedule task…** opens it for a service).

- **Schedule**: five fields (`minute hour day-of-month month day-of-week`) or
  `@hourly`, `@daily`, `@weekly`, `@monthly`, `@yearly`. Parsed by the in-house
  `internal/cronexpr`. The finest resolution is one minute.
- **Time zone**: each task has an IANA `timezone` (`Europe/Berlin`,
  `America/New_York`, …; default `UTC`, and every task created before the field
  existed stays `UTC`). The schedule is read as wall-clock time in that zone, so
  "every day at 03:00" stays at 03:00 across daylight-saving changes (the UTC
  instant moves by an hour). A wall time that does not exist (the spring-forward
  gap) is skipped for that day; one that happens twice (the autumn hour) runs
  once, the first time. An unknown zone is `422 invalid_timezone`. Set it with
  `timezone` on `POST /services/{id}/tasks` and `PATCH /tasks/{id}` (changing it
  recomputes the next run), `nebulactrl tasks create --timezone` /
  `tasks update --timezone`, or the **Time zone** field of the task dialog, which
  previews the next runs in the task's zone, in UTC and in your own zone. The
  list shows the zone next to the schedule. `POST /task-schedules/preview` takes
  `timezone` too. The control plane binary embeds the zone database, so zones
  work on minimal images.
- **Enabled**: a disabled task keeps its settings and history but the schedule
  starts no runs, and it has no next run. Switch it with `enabled` on
  `PATCH /tasks/{id}` or `nebulactrl tasks update --enable` / `--disable`.
- A River periodic job evaluates due tasks every minute. A task that is due runs
  **once**, however long the control plane was away: missed ticks are not
  replayed.
- `forbid` (default): while the previous run of the task is still active the tick
  is recorded as **skipped** ("the previous run is still running"). `allow`
  starts another run. A streak of identical skips is recorded once, so it never
  pushes real history out.
- A tick is also skipped, with the reason, when the service is stopped, has never
  been deployed or its server is offline.
- **History**: the newest 50 finished runs of each task (and 50 one-off runs per
  service) are kept with their output; older ones are deleted. Output is
  redacted with the service's secret values and capped at 10,000 lines.
- **Run now** starts a run immediately, ignoring the schedule and `concurrency`.
- An acked run whose agent never answers is failed after `timeout + 10 minutes`
  ("the agent never reported a result").
- **Notifications**: a failed or timed-out **cron** run (and runs started by
  features such as preview seeding) emits `task.failed`; runs a person started
  from the UI, CLI or MCP do not, you are looking at them.

## One-off commands

`POST /api/v1/services/{id}/run {command, timeoutSeconds?}` (scope
`deployments:write`, audited as `service.run_command` with the command line).
Returns `202` with a run. Follow it with
`GET /task-runs/{runId}/logs?follow=true` (server-sent events; `event: end` carries
`{status, exitCode}`), or `GET /task-runs/{runId}` for the status and exit code.
`409 not_deployed` / `server_offline` when it cannot start.

- UI: the dock's **More** menu and ⌘K ("Run command in …"); the Tasks section of
  the service settings also has the button, plus the run history drawer.
- CLI: `nebulactrl services run <service> -- <command…>` streams the output and
  **exits with the command's exit code**. `--detach` returns the run id.
- MCP: `run_command` (needs `deployments:write`; waits up to `waitSeconds`,
  default 20, returns at most 500 redacted lines, 64 KB) and `get_task_run`.

Docker exec level access is root-equivalent on that service's data, so the scope
is the same one that can deploy. Output is untrusted application text.

## Pre-deploy command

Service settings → Deploy → **Pre-deploy command** (`config.preDeployCommand`,
`preDeployTimeoutSeconds`, default 600, at most 3600).

Order of a deploy on the agent: pull or build the image → **pre-deploy** → stop
the old container (recreate only) → create and start the new one → health check →
route → verify. The pre-deploy container uses the **new image** and the deploy's
exact environment. Until it succeeds nothing of the previous release has been
touched, so for rolling **and** recreate deploys the old release keeps serving
while migrations run.

A non-zero exit (or a timeout) fails the deployment at the step `predeploy`; the
message carries the exit code and the last 20 output lines, and says that the
previous release is still serving. The output is in the deployment logs. The LED
strip keeps its three LEDs (Build, Release, Verify): `predeploy` lights Release
and the card shows a small "pre-deploy" label under it while it runs.

It runs for every deployment that creates a **new release** (manual, staged,
git push, preview). It does **not** run for rollbacks, reconcile redeploys or
server drains: those re-run an existing release and must not touch data.
Beware that migrations are not undone by a rollback.

## Post-deploy command

Service settings → Deploy → **Post-deploy command** (`config.postDeployCommand`,
`postDeployTimeoutSeconds`, default 600, at most 3600). It mirrors the pre-deploy
command, but runs **after** the swap, for work that needs the new release to be
live: cache warm-up, a search reindex, a smoke call.

Order of a deploy on the agent: pull or build → pre-deploy → create and start →
health check → route → verify → **retire the previous release** → **post-deploy**.
The post-deploy container is a one-shot container of the **new image** with the
deploy's exact environment, on the environment network (it reaches the live
service by its name, without an alias of its own). It uses the same memory and
CPU limits as the service and is removed when it ends.

**A failure only warns.** The new release is already serving, so a non-zero exit,
a timeout or a command that cannot start never fails or rolls back the
deployment. Instead:

- the deployment is `succeeded`, and its message ends with
  `Warning: post-deploy command failed with exit code N`;
- the timeline shows the step **Post-deploy** with the status `warning` (a
  triangle) and the sentence "The new release is live; the deployment was not
  rolled back." The command's output is in the deployment logs;
- a notification goes out as `task.failed` with severity **warning** (title
  "Post-deploy command failed on <service>"). Channels that subscribe to task
  failures at the `warning` level or lower receive it; the deployment itself is
  still announced as `deploy.succeeded`.

Like the pre-deploy command it runs for every deployment that creates a **new
release** (manual, staged, git push, preview), not for rollbacks, reconcile
redeploys or drains, and a service that is itself a one-shot has none. The
deployment stays in `verifying` until the command ends (up to its timeout), so a
new deploy of the same service is refused as in progress until then.

## Operational notes

- The agent finds the image from the service's running container on the host,
  else from the release (pulling it with the service's registry credentials).
- Containers are named `nc-task-<service>-<run8>` / `nc-predeploy-<service>-<dep8>` / `nc-postdeploy-<service>-<dep8>`
  and carry `nebulactrl.task-run`, `nebulactrl.task-service`,
  `nebulactrl.task-kind` labels, but **not** `nebulactrl.service`: they are never
  replicas. The agent sweeps leftovers (exited ones older than 10 minutes, running
  ones far past their deadline) on every reconcile.
- A redelivered `run_task` command (agent restarted mid-run) **adopts** the
  existing container instead of running the command a second time. If the agent
  shuts down while a task runs, the container is left running and adopted after
  restart; the logs written during the gap are not replayed.
- Command payloads persisted in `agent_commands` hold the command line and the
  variable **keys** only; values are attached at send time. Do not put secrets
  into the command line itself (they would be stored), use variables.
- `GET /services/{id}/task-runs`, `/task-runs/{id}` and the output need
  `projects:read`; creating, editing and deleting tasks needs `projects:write`;
  running needs `deployments:write`.

## Cron services

A **cron service** is a service whose whole job is to run on a schedule, for
example a nightly report or a cleanup. It is a one-shot service
(`config.oneshot = true`) with a schedule (`config.cron`):

```json
{ "oneshot": true, "startCommand": ["sh", "-c", "echo tick"],
  "cron": { "schedule": "0 3 * * *", "timezone": "Europe/Berlin", "timeoutSeconds": 900, "concurrency": "forbid" } }
```

`timezone` defaults to UTC, `timeoutSeconds` to 1800 (1-86400) and `concurrency`
to `forbid` (skip a run while the previous one still runs; `allow` starts it
anyway). Only app and worker services can have one, and it needs `oneshot`
(`422 cron_requires_oneshot`, `cron_not_applicable`, `invalid_cron`,
`cron_never_fires`, `invalid_timezone`, `invalid_timeout`).

- **A deploy starts nothing.** It pulls or builds the image, records the release
  and ends with "Image ready; runs on schedule". The service has no container
  between runs, so it is never reconciled, probed or reported missing. Services
  that depend on it are released as soon as its deploy succeeds.
- **Runs.** Each run is a [task run](#cron-tasks) of the service's own image
  with its start command (or the image's `CMD`), variables, volumes, files and
  Docker options, named after the service. The schedule is a staged setting: it
  ships with the release and the service's hidden schedule (not listed under
  tasks) follows the deployed release, rollbacks included. **Run now** and the
  run history are on the service card (`nebulactrl tasks run <service>`,
  `nebulactrl tasks runs <service>`).
- **Stop and Start pause and resume the schedule.** `POST /services/{id}/stop`
  and `/restart` send no agent command (the response has no `commandId`); ticks of
  a paused schedule are recorded as skipped ("the service is stopped").
- **Create one** with the **Cron job** entry of the canvas menu,
  `nebulactrl services create --image <ref> --cron "0 3 * * *" [--cron-timezone <tz>]`
  (implies `--one-shot`), or a `[services.<name>.cron]` table in `nebula.toml`.
  The service view's `cron` carries the hidden task's id, the next run and the
  last run.

## Cron task or singleton process

For a scheduler that only needs to start something every minute (`php artisan schedule:run`) a cron task is the better fit: no always-on container, no scheduler state, `concurrency = "forbid"`. When the scheduler must stay resident (`schedule:work`, `celery beat`, a clock process) or needs sub-minute resolution, run it as a `singleton` [process](processes.md#schedulers-a-singleton-process-or-a-cron-task).
