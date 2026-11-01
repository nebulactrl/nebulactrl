---
title: Deployments
description: "Rolling and recreate strategies, health checks, rollback, image retention and recovery."
section: Applications
order: 10
---

# Deployments: semantics you can rely on

Health checks run from the agent, outside the container, so images do not need `curl` or `wget`.

## Life of a deployment

```
created → queued → preparing → building → releasing → verifying → succeeded
                                                              ↘ failed | canceled | rolled_back | rollback_failed
```

Image sources skip `building`. Every transition is recorded with a timestamp (deployment → step timeline). Only **one deployment per service can be active**; a second request gets `409 deployment_in_progress`. Deploy and rollback return `202` with the deployment; they never report success before the agent has finished.

A release is immutable and written *before* dispatch: the resolved source (image + digest, or git URL + commit SHA), a config snapshot and the encrypted, resolved variables.

## Strategies

The plan (`POST /services/{id}/deployments/plan`, shown before you deploy) names the strategy and why:

| Strategy | When | What happens |
|---|---|---|
| **rolling** | the service has **no volumes** and an HTTP port | New container starts and is health checked while the old one keeps serving; traffic switches only after the route is loaded and verified. |
| **recreate** | anything else (volumes, no HTTP port) | The old container is stopped first, then the new one starts. **There is downtime** for the time it takes to start and become healthy. The plan lists the reasons (`has volumes`, `no http port`). |

### Rolling steps

1. Pull or build. If this fails the old container is untouched (`failed`).
2. Create and start the new container.
3. Health check it. Unhealthy = the deadline passes, the container exits, or it restarts. Then the new container is removed, the outcome is `failed` ("Previous release is still serving" plus the last 50 container log lines).
4. If the service has domains, push the full route table, now pointing at the new container, to Traefik. Traefik applies provider configuration asynchronously, so the agent waits for a per-generation marker router on the probe entrypoint to answer and reads the router statuses. If Traefik disabled any router (for example a bad upstream), the agent pushes the previous table again and the deployment is `failed`.
5. **Verify** through Traefik's internal probe entrypoint (port 8099, never published) using the service's first hostname and health path; any status below 500, 3 tries 2 s apart. If that fails, the previous routes are pushed again and the new container removed: `rolled_back`, or `rollback_failed` if the restore itself fails.
6. Retire: wait 2 s for drain, stop the old container with the **stop grace period**, remove it, prune old images.

### Recreate steps

Pull or build → stop the old container (kept) → start and health check the new one → on failure remove it and restart the old one (`rolled_back`, or `rollback_failed`) → on success load the route and remove the old container.

### Several processes

A service with [processes](processes.md) is one deployment: one build, one `pre_deploy`, then the processes with a port, then the others (a singleton stops before it starts), then the old containers are retired and `post_deploy` runs. A failing process fails or rolls back the whole deployment, and its message names the process. A rollback reverts every process to the target release.

## Canceling a deployment

A deployment can be canceled at **any step** until the new release is live: `POST /api/v1/deployments/{id}/cancel` (scope `deployments:write`), the **Cancel deployment** button in the Deployments tab and the canvas menu, `nebulactrl deployments cancel <id>`. The deployment ends as `canceled`, with the step it reached in `canceledAtStep` and "Canceled by <who>" in its message. The request and the outcome are audited (`deployment.cancel`, `deployment.canceled` with the step).

- **Before the agent acknowledged the command** (queued, preparing, building with the command still `pending` or `sent`): the command is canceled and the deployment ends as `canceled` at once.
- **Once the agent runs it:** the control plane records the request (`cancelRequestedAt`) and sends a `cancel_deploy` command to the agent. The deployment keeps its status until the agent has aborted and unwound; the UI shows "Canceling…". An offline server receives the request when it reconnects.

What the agent does at each step (the previous release keeps serving unless noted):

| Step reached | Abort |
|---|---|
| **pull** | the pull is stopped; nothing was changed |
| **build** | the build gets `SIGINT` (and `SIGKILL` after 10 s), so BuildKit stops it; the build work directory is removed; no image is produced |
| **pre-deploy** | the command's container is removed (never left for a later redelivery to adopt) |
| **create / start / health / route** | the containers of the new release are removed, including one whose creation was interrupted; if the new route table had already been applied it is withdrawn first |
| **verify** | the previous routes are restored **first**, then the new containers are removed |
| **stop** (recreate only, the old release is stopping) | the stop is not interrupted; right after it, the old release is started again |
| a **recreate** deployment past "stop old" | the previous release is started again, exactly like an automatic rollback |

The result is `canceled` only when everything was restored. If the previous routes or the stopped previous release **cannot** be restored, the result is `rollback_failed` with the manual recovery steps, never `canceled`. A canceled deployment never becomes the service's current deployment and consumes no generation: the staged changes stay staged.

**Too late:** once verification has passed the new release is live and the point of no return is behind it. The agent then reports "Too late to cancel" in the step timeline and the deployment finishes (retire, then `succeeded`). A **post-deploy command** that is still running can be stopped: the deployment stays `succeeded`, with a warning that the command was canceled. A deployment that already finished is `409 cannot_cancel`. A cancel is not an agent shutdown: if the agent stops mid-deploy the command still runs again after the restart.

## Health check semantics

Configured per service (**Settings → Deploy → Health check**); the agent runs the check against the container's address on its environment network (`nebulactrl-env-<id8>`, or the `-int` network when the service blocks outbound internet), not the shared edge network. See [networking](networking.md).

| Type | Passes when |
|---|---|
| `http` (default when a port is set) | `GET <path>` on the port returns status **200–399**. Default path `/`. |
| `tcp` | the TCP connection opens (default for managed databases) |
| `docker` | the image's own `HEALTHCHECK` reports healthy |
| `none` | the container has simply **run for 10 s without restarting**. The plan shows a warning: nothing proves it works. |

Timing defaults: interval 2 s, timeout 3 s, start period 0 s, **deadline 120 s**, success threshold **2 consecutive passes**. A container that exits or restarts during the deadline fails immediately. A health check failure during deployment aborts the new release; it does not by itself stop a running one: after a deploy, a container that later becomes unhealthy is shown as unhealthy, not killed. Docker's restart policy (`unless-stopped`) restarts crashed containers.

## Stop grace period

`stopGraceSeconds` (default 30, 1–3600): how long Docker waits after the stop signal before SIGKILL when a container is stopped for retire, restart or stop. The stop signal is the image's own `STOPSIGNAL`, SIGTERM when the image sets none (serversideup's `fpm-nginx` PHP image sets SIGQUIT). Raise it for workloads that drain slowly: a queue worker needs at least its job timeout.

## Variables and generations

Changing configuration, variables, domains or volumes does **not** redeploy. It increments the service's `generation`; the node shows a **Staged** chip and the environment's staged bar says "N changes to apply". **Review** lists the field-level changes (secret values masked). **Deploy** applies them: one deployment per changed service. Edits use optimistic concurrency: a stale `If-Match` returns `412 generation_conflict` and the UI reloads.

Secret variable values are never returned by any read endpoint, appear in logs only as `••••••`, and travel to the agent inside the command frame at send time, never in stored payloads.

## Rollback

*Roll back to this release* (deployment row, API `POST /deployments/{id}/rollback`, CLI `nebulactrl deployments rollback`) creates a **new deployment** from the old release's source and configuration snapshot. It is deployed with the normal strategy and checks.

> [!NOTE]
> **Application image and configuration only. Databases, volumes and external services are not rolled back.**

If the old release was a git build whose image has been pruned from the host (see below), the API returns `409 image_pruned`; use **Rebuild commit <sha7>**, which deploys that commit again. Image releases re-pull `repo@digest` when missing (registries may have deleted it).

**Variable values outside the rollback window.** A release keeps the sealed environment it was deployed with only while it is among the newest `imageRetention` succeeded releases (or running, or in flight); an hourly job clears it from older ones ([secrets at rest](../security/secrets-at-rest.md#release-secret-retention)). Rolling back to such a release deploys its old image and configuration with the service's **current** variables, says so in the timeline and in the rollback response, and fails (changing nothing) if the current variables do not resolve. Within the window, a rollback restores the old values.

Retention: after each successful deploy the agent keeps the newest `imageRetention` built images per service (default **5**) and removes the rest. The server's scheduled [Docker cleanup](docker-cleanup.md) (daily, and when the disk alert is active) never removes the current or any of these rollback images, and prunes build cache older than 7 days.

## Recovering from `rollback_failed`

`rollback_failed` means the deployment failed *and* the agent could not restore the previous release. The service may be down or serving the wrong release. On the host:

1. `docker ps -a --filter label=nebulactrl.service=<serviceId>`: find which containers exist (names `nc-<slug>-<deploymentId8>-<replica>`, replica 0…N-1).
2. `docker logs <container>` on the newest one; the deployment's log in the UI has the failure reason.
3. If an old container exists and is stopped (recreate): `docker start <name>`. If routes are wrong: in the UI, **Restart** the service or trigger a deploy of the last good release (rollback target) – the deploy re-applies the full route table.
4. If the host itself is the problem (disk full, Docker unhealthy, proxy error), fix it first; the server view shows the proxy status and disk gauge.
5. Redeploy and confirm `succeeded`. The reconciler also repairs a **missing** container automatically (at most 3 times per hour per service).

## Restart and stop

`restart` stops and starts the existing container (a brief interruption; not a rolling replace) with the configuration it was deployed with; staged changes are applied only by a deploy. `stop` stops it and keeps its configuration; the node shows stopped. Both honor the stop grace period.

## Moving a service to another server

Changing **Placement** is a staged change. Deploying creates a new deployment on the target; the old server's container is not auto-migrated and **volumes are not moved**. Plan data migration yourself.
