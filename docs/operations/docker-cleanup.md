---
title: Docker cleanup
description: "Daily and disk-alert cleanup of images, build cache and networks."
section: Servers
order: 90
---

# Docker cleanup

Images, build cache and stopped containers pile up on a Docker host. NebulaCtrl cleans each server on a schedule, so a disk does not fill up with last month's builds, and never at the cost of a rollback.

Each server has a **Docker cleanup** card under *Settings* on its page. The schedule is **on by default**.

## When it runs

| Trigger | When |
|---|---|
| Scheduled | By the server's **schedule**, by default daily at **04:00 UTC**, for every online server with cleanup on. A server that enrolled after a slot, or whose schedule was just changed, waits for the next one: a new schedule never catches up on earlier slots. A server that was offline at a slot is cleaned as soon as it is back online. |
| Disk trigger | When the root disk reaches the server's *disk trigger* (by default its *disk alert threshold*, 80 %) and no cleanup started on it in the last 6 hours. |
| By hand | *Run cleanup now*, `POST /api/v1/servers/{id}/cleanup/runs`. Works with the schedule off. |

Only one cleanup runs on a server at a time. An offline server is never sent one: the command would be stale by the time it connected.

## What it removes

| Step | Removed | Notes |
|---|---|---|
| Stopped containers | Containers that are **not** managed by NebulaCtrl and have been stopped for at least an hour | **Only when the switch is on. Off by default.** Never removes a container's volumes. Helper containers NebulaCtrl starts itself (`nebulactrl.helper` label, `nebulactrl-hostshell-*`) are never touched. |
| Dangling images | Images with no tag that no container uses | No age limit. |
| Unused images | Tagged images that no container uses, older than the **unused image age** (default 72 hours) | Age is the later of the image's build date and the time it was last pulled or tagged, so an old image you pulled an hour ago is not old. |
| Build cache | Cache older than the **build cache age** (default 7 days) | The daemon's and, when it exists, the managed BuildKit builder's. |
| Networks | Empty NebulaCtrl environment and shared networks | The same networks the agent already sweeps every minute. |

## What it never removes

- **Volumes.** A scheduled, emergency or manual cleanup never removes a volume, and never removes a container together with its volumes. Removing unused volumes is a separate action (below).
- **Rollback images.** For every service on the server the control plane sends the agent the list of images to keep: the **current release**, the releases of any **deployment in flight**, and the **newest `imageRetention` succeeded releases** (the ones *Roll back to this release* can pick; default 5). The agent never deletes an image in that list, whatever its age or whether a container uses it. The list matches git builds by local image ID and image releases by reference and digest (a release built on a [build server](build-servers.md) by its registry `repo@digest`, plus the release it was built from), so a tag that moved on does not orphan an older release.
- **Pinned infrastructure images.** Traefik, the pages server, cloudflared, Tailscale, BuildKit and the Railpack frontend, and any helper image registered with `agentprotocol.PinImage`. The agent knows these itself and keeps them even when nothing uses them right now. On a build server this includes the per-project BuildKit builders' image.
- **Images in use** by any container, running or stopped.

A cleanup command without the keep list is refused by the agent. If the list could not be computed the command is not sent.

## Build servers

A [build server](build-servers.md) pushes what it builds to a registry (`docker buildx build --push`) and nothing is loaded into its Docker image store, so there are no release images on it and no keep list to compute: a **Builds** server's cleanup has nothing to protect. What it accumulates is:

- **BuildKit cache**, one builder per project (`nebulactrl-buildkit-<project8>`). Each builder is created with its own garbage-collection limits (a reserved size and a maximum size), so BuildKit evicts the oldest records by itself when a project's cache grows. The daily age-based prune above stays as a backstop and prunes every builder.
- The pinned BuildKit and Railpack frontend images, and any Railpack builder images pulled for a build.
- Clone workdirs, which are removed when the build ends.

Two guards protect running builds:

- **No prune while building.** The agent holds a lock for the length of every build and never prunes build cache underneath a running build. (Pruning mid-build kills it at "importing to docker".)
- **Disk preflight.** A build refuses to start with too little free space, runs an emergency cache prune and retries once; otherwise the build fails with BuildKit's error. The disk alert threshold and the disk trigger above apply to a build server like any server.

On a **Both** server the usual rules apply for the workloads, and the cache of the build side is pruned as above.

The images of the **built-in registry** are not cleaned up here: the control plane deletes expired digests through the registry's API and collects blobs in a weekly window ([build servers: rollback and retention](build-servers.md#rollback-and-retention)).

## Results

Every run is recorded (the last 30 per server) with the trigger, the space it freed, the number of items removed and what each step did, including how many images it kept for releases. Open a run on the card to see the steps and the first removed names.

- Freed space is the difference in Docker's image storage before and after (shared layers count once) plus the build cache the prune reported. Containers and networks free little and are counted as items only.
- A step that fails (the daemon refuses an image, buildx is gone) is reported on its row; the other steps still run, and the run is marked *Failed* while still showing what it freed.
- An image that became used between listing and removal is not a failure.
- Each finished run is written to the audit log as a system action (`server.cleanup`); starting one by hand, changing the settings and the volume action are audited with the person (`server.cleanup.start`, `server.cleanup.update`, `server.cleanup.volumes`).
- A run that **freed more than 1 GB, or failed**, sends a `server.cleanup` notification (see [Notifications](notifications.md)): info for a large reclaim, error for a failure. Smaller successful runs are silent.

## Removing unused volumes

*Remove unused volumes…* (`POST /api/v1/servers/{id}/cleanup/volumes`) is the only way a cleanup deletes a volume. It asks you to type the server's name and deletes, with their data, the volumes that **no container (running or stopped) mounts** and that **NebulaCtrl does not manage**: anonymous volumes and volumes created outside NebulaCtrl. Volumes of your services (`nc-vol-*`, anything labeled as managed, anything the control plane lists as a service volume, including the volumes of deleted services) are kept. It runs nothing else and the schedule never starts it. It cannot be undone.

## Settings and API

| Setting | Field | Default | |
|---|---|---|---|
| Scheduled cleanup | `enabled` | on | Turns the scheduled and the disk-trigger runs on or off. *Run cleanup now* always works. |
| Schedule | `cron` | `0 4 * * *` | A 5-field cron expression (or `@daily`, `@weekly`, ...) evaluated in **UTC**. Runs must be at least 15 minutes apart (`422 validation_failed` otherwise). The page offers *Daily at 04:00*, *Every 6 hours* and *Sundays at 03:00* as shortcuts. |
| Unused image age | `unusedImageMinAgeHours` | 72 | 1 to 8760 hours. Rollback images and pinned infrastructure images are kept whatever the age. |
| Build cache age | `buildCacheMinAgeDays` | 7 | 1 to 365 days. |
| Disk trigger | `diskTriggerPercent` | follows the disk alert threshold | 50 to 99 % of the root disk. With a value set, the heartbeat's disk usage decides (not the alert flag). At most one emergency run every 6 hours. |
| Stopped containers | `removeStoppedUnmanaged` | off | Also remove stopped containers NebulaCtrl does not manage. |

`GET` and `PUT /api/v1/servers/{id}/cleanup` read and change them (the `servers:write` scope for `PUT` and the runs). `PUT` replaces every setting: `0`, an empty string or an omitted field restores that default. The change is audited (`server.cleanup.update`, with the values). The server's own *disk alert threshold* is a separate setting.

## Limits

- Build-cache pruning is a step of the cleanup. With the schedule off, build cache is only pruned by a manual run.
- The keep list is computed when the command is queued. A deployment that starts afterwards is protected by its age (an unused image is only removed after its age limit, 72 hours by default; a dangling one is removed at once, so avoid running a cleanup by hand while you redeploy the same tag).
- A server whose agent is older than this feature (`docker.cleanup` capability) shows *This agent is too old* and is skipped.
