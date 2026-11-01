---
title: Replicas and autoscaling
description: "Run a stateless service on several replicas and scale on CPU."
section: Applications
order: 100
---

# Replicas and autoscaling

A stateless service can run on 1–10 replicas on its own server. Replicas are
containers of the same release; the server's Traefik load-balances across them
and health-checks every upstream.

**Eligible services:** no volumes, an HTTP port, and a kind other than
`database`. Anything else is fixed at one replica (`422 replicas_require_stateless`).
A deploy of a service that stopped being eligible (a volume was added, the port
removed) runs one replica, resets the count to 1 and turns autoscaling off.

## Manual scaling

`PUT /api/v1/services/{id}/scaling {"replicas": 3}` or
`nebulactrl services scale <service> 3`. It applies immediately and is never a
staged change: a deployed service gets a `scale_service` command that reuses
the running release (no rebuild, no new deployment); an undeployed one stores
the setting for its first deploy. New replicas must pass the health check
before they receive traffic; removed replicas leave the upstream list first and
drain for 2 seconds. Scaling is refused while a deployment is running
(`deployment_in_progress`) or the service is stopped (`service_stopped`).

The control plane also states the count in every reconcile frame, which the
agent uses to catch up on a `scale_service` command that expired while it was
offline. Both carry the time the count was stored, so a frame built before a
scale that already ran on the agent (it still names the old count) is ignored
for that service: a scale is final until the control plane stores a newer one.

Deploys keep the current replica count. A rolling deploy starts N new
containers, waits until all are healthy, switches routing to the new set and
retires the old one; if any new replica fails, the old set keeps serving.

### Per process

In a service with [processes](processes.md) every process has its own replicas and autoscaling (`services scale --process`, `PUT /services/{id}/processes/{process}/scaling`, `processes.<name>.scale` in `nebula.toml`). A process does not need an HTTP port to scale, so queue workers scale. A singleton process, and every process of a service with volumes or files, runs one instance (`422 replicas_require_stateless`).

## Autoscaling

`nebulactrl services scale <service> --autoscale 1:5:70` (min:max:target%), or
`PUT .../scaling {"autoscaling": {"enabled": true, "min": 1, "max": 5, "targetCpuPercent": 70}}`.
While enabled, the replica count is managed by the autoscaler and a manual
`replicas` value returns `409 autoscaling_enabled`.

A periodic job (every 30 s) judges each deployed service from the per-replica
CPU samples (percent of the replica's CPU limit, or of one core without a limit):

| Rule | Value |
|---|---|
| Scale out +1 | average above the target for 2 minutes |
| Scale in −1 | average below 35 % (never more than half the target) for 10 minutes |
| Cooldown | 3 minutes after scaling out, 10 minutes after scaling in |
| Bounds | min and max, within 1–10 |

Every sample bucket of the window must contain data; a gap restarts the clock.

**Capacity guard.** A scale-out is blocked when the server's free memory is
below one replica's memory (the configured memory limit, else the observed
average per replica) or the server has an active disk alert. The service then
shows `scaling.blockedReason`, e.g. "Scale-out blocked: agent1 has 0.4 GiB
free", until the condition clears. A failed `scale_service` command is shown
the same way ("Scale failed: …").

Each autoscale action is audited as `service.autoscale` (system actor, with the
reason, e.g. "CPU 84% > 70% for 2m"); manual changes as `service.scale`.

## Observability

`GET /services/{id}` reports `scaling.replicas` (desired), `scaling.ready`
(running and healthy) and `observed.replicas[]` per container. The metrics
endpoint sums CPU and memory over the replicas, adds `cpuPerReplica`, and the
newest point carries `replicas: [{index, cpuPercent, memBytes}]`. Runtime logs
merge all replicas, prefixed `[r<N>]` when there is more than one.

Replicas share one server. Spreading replicas across servers is not supported.
