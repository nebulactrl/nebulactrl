---
title: Draining servers and the fleet
description: "Take a server out for maintenance or retirement; capacity and overcommit."
section: Servers
order: 30
---

# Draining servers and the fleet overview

## When to drain

Drain a server to empty it before maintenance (kernel update, reboot, disk work) or before retiring a VPS. A drain moves **every** service placed on the server to other servers, one at a time, and closes the server to new placements.

Draining is not required to revoke a server, but revoking leaves the containers running and the services still placed on it ([Servers and agents](servers.md#revoke)). Drain first, then revoke.

**Volumes are not copied.** A service with volumes, and any service of kind `database`, starts with empty volumes on its target. Move the data yourself before you drain (see [Stateful services](#stateful-services)).

In the UI: open the server's detail page and use the drain sheet (the final button is hold-to-confirm: "Hold to drain <server>"). The same flow is available over the API.

## Plan (dry run)

`POST /api/v1/servers/{serverId}/drain/plan` changes nothing. It returns one move per service on the server, plus a capacity verdict. The body is optional: `{"targetServerId": "<id>"}` sends every service to that server; omitting it picks a target per service.

Move order: stateless services first, services with volumes or of kind `database` last. A failure stops the drain, so data-bearing services are touched last.

### Automatic target

Without `targetServerId`, each service goes to the eligible server with the most free memory. A server is eligible when it is online, not the source, not revoked, and its drain state is neither `draining` nor `drained`.

Free memory (headroom) of a candidate:

```
total memory − max(memory used, committed limits) − memory already assigned by this plan
```

- *Committed limits* is the sum of the memory limits (`memoryMb`) of the services already placed on the candidate.
- *Assigned* is what earlier moves of the same plan already booked on it, so one big server does not receive everything unless it still has the most room.
- A service's demand is its memory limit; with no limit, its latest usage sample (at most 2 minutes old); with neither, nothing is booked.
- Ties are broken by server name, then ID.
- A server that has not reported its memory ranks below every server that has. If it is the only candidate, the plan still picks it and warns that capacity is unknown.

With an explicit target the server must exist, differ from the source, not be revoked, and not be draining or drained (`422 server_draining`).

### What the plan reports

Per move: `targetServerId`/`targetServerName`, `stateful`, `warnings[]`, `blocking[]`. Overall: `capacity.targetFits` and `capacity.reasons[]`.

Blocking problems (the drain cannot start):

| Code | Cause |
|---|---|
| `no_target` | No online, schedulable server can take the service |
| `target_offline` | The chosen target is offline |

Capacity: `targetFits` is `false` when a target would exceed its memory after the assigned moves; `reasons` say by how much. If a target has not reported its memory the check is skipped for it and a reason says so. The start endpoint does not re-check `targetFits`; read it before you start.

Warnings the plan can emit:

- the target was chosen automatically (and how much memory remains free, or that capacity is unknown);
- volumes are not copied / a database starts empty on the target;
- the service has never been deployed: only its placement changes, nothing is started;
- the service has staged changes: they are deployed together with the move;
- the service has no memory limit (and possibly no usage data): the capacity check approximates or cannot account for it;
- a deployment of the service is in progress: the drain waits for it to finish;
- the service has domains and the target has a different public IP: point the A records to the target after the move;
- the source server is not online: the old container cannot be stopped until its agent reconnects.

## Stateful services

A service is *stateful* when it has at least one volume or its kind is `database`. On the target it starts with empty volumes.

1. Back up or copy the data to the target host yourself.
2. Start the drain with `"confirmStateful": true`. Without it the API answers `422 confirmation_required` naming the stateful services. In the UI this is the checkbox "I have moved the data, or I accept losing it."

Nothing in the drain code copies or deletes volume data.

## Starting the drain

`POST /api/v1/servers/{serverId}/drain` with:

```json
{
  "moves": [{"serviceId": "…", "targetServerId": "…"}],
  "confirmStateful": true
}
```

Rules enforced on start:

- Every service on the server appears in `moves` exactly once (`422 incomplete_moves` lists the missing ones; `422 invalid_moves` for duplicates or services not on this server).
- Every target passes the checks above and is **online** (`422 target_offline` otherwise). Plan blockers (`no_target`) are returned as errors.
- `409 drain_in_progress` if the server is already draining.
- `Idempotency-Key` is honored.

The call returns `202`. In one transaction the server's drain state becomes `draining`, a drain run is stored, a background job is enqueued, and the audit event `server.drain.start` is written (including the plan and `confirmStateful`).

## How the moves run

Moves run **sequentially**, in plan order. Per service:

1. If a deployment of the service is active, wait (3 s polls) until it ends.
2. Change the service's placement to the target. This is a normal service update, so it increments the service generation and is audited under the initiator.
3. If the service was never deployed, it is marked done ("Not deployed yet: only the placement changed.").
4. Otherwise create a deployment with trigger **`drain`** for the release content that is running now (a git service is pinned to its current commit SHA; a drain never ships new code). Staged config and variable changes are deployed with it.
5. Wait until that deployment ends. `succeeded` marks the move done. Per the endpoint description, the old container is removed once the new one is live.

A service that was deleted during the drain is skipped as done. A drain job interrupted by a control-plane restart resumes from the stored progress and adopts an already running `drain` deployment instead of starting a second one.

The job acts as the initiator with `projects:read`, `projects:write` and `deployments:write`, so deployments and audit entries are attributed to them.

When every move is done, the run is `completed`, the server's drain state becomes `drained`, and `server.drain.complete` is audited.

Follow progress with `GET /api/v1/servers/{serverId}/drain` (the running drain, else the newest finished one, else `{"drain": null}`). Each move has the status `pending`, `moving`, `deploying`, `done` or `failed`, and `deploymentStatus` live from its deployment.

## Failure semantics

The first move that fails **stops the drain**:

- Failure means the placement update was rejected, or the `drain` deployment ended in anything but `succeeded` (`failed`, `rolled_back`, `rollback_failed`, `canceled`, or the deployment disappeared).
- That service's placement is **reverted** to the source server, so the placement matches what is actually running. If its placement or generation was changed meanwhile, the revert is skipped and the move's message says "the service's placement was changed meanwhile and was not reverted".
- Services not yet started are **untouched** and keep running on the source. Services already moved stay on their targets; nothing is moved back.
- The run is `failed`; the server's drain state is `failed`, with `drainMessage` = `Moving <service> to <server> failed: <reason>`. `server.drain.fail` is audited.

In state `failed` the server accepts new placements again (the scheduling guard applies only to `draining` and `drained`). Fix the cause, then either **Retry (re-plan)** in the UI, which plans the services that are left, or undrain.

## Cancel

`POST /api/v1/servers/{serverId}/drain/cancel` asks the drain to stop **after the move in flight**. A move that is moving or deploying is finished first; no further move starts. Nothing is rolled back.

| Moves done at that point | Result |
|---|---|
| 0 | Run `canceled`; server drain state returns to `none` |
| N ≥ 1 of M | Run `canceled`; server drain state `failed` with message **"Canceled after N of M moves"** |

`409 no_drain_in_progress` when no drain is running. `server.drain.cancel` is audited.

## Undrain

`POST /api/v1/servers/{serverId}/undrain` sets a `drained` or `failed` server back to `none` so it can receive services again. Services that were moved **stay where they are**.

- While a drain is running: `409 drain_in_progress` (cancel first).
- On a server whose state is already `none`: no change, the server is returned.

## Scheduling guard

A server in drain state `draining` or `drained` refuses new placements: creating a service on it, or changing a service's placement to it, returns `422 server_draining` ("<name> is being drained"). Editing a service that stays on the server is not affected. The same guard rejects such a server as an explicit drain target. The UI greys these servers out in the service placement select and in the drain target list.

Drain states: `none`, `draining`, `drained`, `failed`. `drainState` and `drainMessage` appear on the fleet overview; the drain run itself carries `createdAt`/`finishedAt`.

## Required scopes

The drain touches every project placed on the server, so **tokens restricted to specific projects are refused** (`403`) on every drain endpoint. Session users hold every scope.

| Endpoint | Scopes |
|---|---|
| `GET /fleet` | `servers:read` |
| `POST /servers/{serverId}/drain/plan` | `servers:read`, `projects:read` |
| `GET /servers/{serverId}/drain` | `servers:read`, `projects:read` |
| `POST /servers/{serverId}/drain` | `servers:write`, `deployments:write`, `projects:write` |
| `POST /servers/{serverId}/drain/cancel` | `servers:write`, `deployments:write` |
| `POST /servers/{serverId}/undrain` | `servers:write` |

## API endpoints

All under `/api/v1`, tag `fleet`:

| Method and path | Purpose |
|---|---|
| `GET /fleet` | Capacity and placement of every server |
| `POST /servers/{serverId}/drain/plan` | Dry run |
| `POST /servers/{serverId}/drain` | Start (`202`) |
| `GET /servers/{serverId}/drain` | Progress |
| `POST /servers/{serverId}/drain/cancel` | Stop after the move in flight |
| `POST /servers/{serverId}/undrain` | Make schedulable again |

Deployments created by a drain have trigger `drain`.

## Fleet overview

`GET /api/v1/fleet` (UI: the Fleet view on the Servers page) lists every server of the workspace with:

- status, `drainState`, `drainMessage`;
- `cpus`, `memTotalBytes`, `diskTotalBytes`, and the latest `cpuPercent`, `memUsedBytes`, `diskUsedBytes` from heartbeats;
- `committedMemBytes`: the sum of the memory limits of the services placed on the server;
- `unlimitedServices`: how many placed services have no memory limit (they are not in the committed sum);
- `overcommitted`: `true` when `committedMemBytes` is greater than `memTotalBytes` (never when the total is unknown; equal is not overcommit);
- `services[]`: name, project, environment, kind, `memLimitBytes` (0 = unlimited), latest usage, `stateful`, and a `status` (`undeployed`, `deploying`, `running`, `unhealthy`, `stopped`, `missing`, `failed`, `unknown`).

`totals` sums servers, services, memory total, used and committed.

Server numbers always cover all services. The `services[]` rows are limited to the projects the caller may see.

Overcommit is a warning, not a limit: the code only flags it. The drain planner uses `max(used, committed)` in its headroom formula, so it steers moves away from overcommitted servers.
