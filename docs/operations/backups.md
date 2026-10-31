---
title: Volume backups
description: "File-level backups of a service's volumes, with hooks, retention and restore."
section: Databases and backups
order: 30
---

# Volume backups

NebulaCtrl backs up the **named volumes of an ordinary service** (anything that is not a managed database): uploads, SQLite files, a Git server's repositories, an app's `data` directory. Managed databases keep their own logical dumps ([database backups](database-backups.md)); the control plane's own backup is [backup-restore.md](backup-restore.md).

A volume backup is **a file-level backup**. Out of the box it is *crash-consistent*: the files are exactly as they were at one instant, as if the power had been cut. Apps that journal their writes (SQLite, PostgreSQL, Valkey with AOF, most key-value stores) start from such a copy without trouble. For *application-consistent* data add a **before-backup command** (below).

## How a backup runs

The point-in-time copy is taken fast; the slow part, moving the data, happens afterwards while the service runs normally.

1. **Before-backup command** (optional). Run inside the service's container with a timeout, for example `sqlite3 /data/app.db ".backup '/data/.nc-backup/app.db'"` or `redis-cli BGSAVE`. It runs **before** the freeze, because Docker refuses to exec into a paused container. If it fails, the backup fails with "Nothing was backed up." and the service is never frozen (unless you chose *continue anyway*).
2. **Freeze.** The agent takes the service's per-service lock (so no deployment can recreate the container in the middle), then runs `docker pause` on the container. This uses the kernel's cgroup freezer: every process stops, **TCP connections stay open and nothing is reset**; clients see a latency spike, not an outage.
3. **Local copy.** A helper container copies the volumes into a temporary *staging volume* with `cp -a --reflink=auto`. On filesystems with copy-on-write (btrfs, XFS with reflink) this takes well under a second regardless of size; elsewhere it is a plain copy, bounded by the freeze budget.
4. **Thaw.** `docker unpause`. The freeze lasted only as long as step 3.
5. **After-backup command** (optional) runs now (for `live` mode: after the upload).
6. **Upload.** The helper runs **restic** on the staging copy and uploads into the service's repository in the background. restic is incremental and deduplicating: unchanged data costs nothing, and the data is encrypted on the server before it leaves it. The staging copy is deleted afterwards.

The service is only ever paused (or stopped) for step 3, **never for the upload**, and never longer than the freeze budget.

### Consistency modes

| Mode | What happens | Use it for |
|---|---|---|
| **Brief freeze** (default) | pause → copy → unpause. Requests stall for about a second on copy-on-write hosts. The default budget is **5 s** (1–60). | most services |
| **Stop during backup** | stop → copy → start. Connections are reset and the service is unavailable until it is back. | apps that cannot be paused safely: leases or heartbeats shorter than the freeze (Consul/etcd-style, Kafka, RabbitMQ), apps with multi-file invariants and no crash recovery |
| **Keep running, no freeze** | no pause, no copy: restic reads the live volumes. The backup can hold files from different moments. | static uploads, or together with a before-backup command that produces a consistent file |

**The freeze budget is a hard limit.** If the copy does not finish within it, the agent thaws the service, discards the copy and fails the backup with *"Snapshot took longer than 5 s; use Stop mode or a pre-backup hook"*. There is no silent fallback to another mode, and the service is never left paused. A timer unfreezes independently of the copy, retries the unpause, and reports it loudly if it cannot.

A service that is **not running** is backed up without any freeze (nothing is writing), whatever mode is set. If a deployment (or another action on the service) is in progress for two minutes, the backup is `skipped` and the service is not touched.

Hosts without copy-on-write need free disk for the staging copy (about as much as the volumes use). The agent checks this **before** freezing and fails early with the numbers instead of filling the disk.

## Set it up

Service → **Backups** tab (shown for services that have at least one volume):

- **Destination**: the same destinations as database backups (Team settings → Backup storage): the team's **Built-in backup storage**, an S3-compatible store of your own, or a directory on the server. A directory is **not off-site**; it does not survive the loss of that server. The built-in storage is not off-site either: it lives on the control-plane host. See [built-in services](platform-services.md) for the quota and what happens when it is full.
- **Consistency**, **freeze budget**, before/after commands (program and arguments; no shell unless you write `sh -c '…'`; optional user and timeout, default 5 min, and *if it fails: stop the backup / continue anyway*), and **excludes** (restic patterns such as `*.tmp`).
- **Retention**: keep the last *N* daily, weekly and monthly backups (defaults **7 / 4 / 6**). `0` removes that limit; all zeros keeps everything. **Locked backups are never removed.**
- **Schedule** (optional): a cron expression and timezone, at most one run every 5 minutes. A schedule that finds the server offline records a `skipped` run with the reason.
- **Missed backups are reported.** Like database schedules, an enabled volume schedule that produced no successful backup within its window raises `backup.missed` once per streak. See [Service alerts](service-alerts.md#missed-backups).

*Back up now* uses the saved settings. All volumes of a service go into **one snapshot** (`/vol/<name>`), so they are consistent with each other.

### Where the data goes

- One restic repository **per service and destination**, at `<destination prefix>/volumes/<serviceId>`. Backups of different services never share a repository.
- The repository is encrypted (AES-256 with Poly1305) with a password NebulaCtrl generates on the first backup. It is stored sealed with `NEBULACTRL_SECRET_KEY` and reaches the agent only as a send-time secret in memory.
- **Download the recovery key** (Backups tab, admin scope, audited). Without the key, or without the secret key that seals it, the backups can not be read. Restoring the control plane without its secret key loses them, exactly as it loses destination credentials.

Restoring by hand without NebulaCtrl (restic ≥ 0.14):

```sh
export RESTIC_PASSWORD='<recovery key password>'
export RESTIC_REPOSITORY='<repository from the recovery key>'   # e.g. s3:https://s3.example.com/bucket/volumes/<serviceId>
# S3 also needs AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY
restic snapshots
restic restore <snapshot>:/vol/data --target /path/to/new/volume
```

The built-in storage has no address you can reach from outside, so this by-hand route applies to a bucket of your own. A backup on the built-in storage is restored from the Backups tab like any other.

## Restore

Choose a backup → **Restore**, pick the volume.

- **Restore into a new volume (default).** The data goes into a **new** Docker volume (`nc-vol-<id>-<name>-r<yymmddhhmmss>`). Nothing is overwritten. The volume row is then pointed at the new volume as a **staged change** ("`/data · restored from Jan 12, 03:30`", counted in the staged bar). **Nothing changes until you press *Deploy*.** The old volume is kept, unmounted, and listed under the service's Volumes as a retired volume you can delete when you are happy. *Discard* in the staged bar undoes the swap (the unused restored volume then also shows as retired).
- **Restore in place (guarded).** For when two copies do not fit on the disk. Type the service's slug. A **safety backup** of the current state is taken first (`pre_restore`); if it fails, nothing is changed. Then the agent stops the service, empties the volume, restores into it and starts the service again.

A rollback of a deployment **never** changes which volume a service uses.

### Browse files and download

A backup's **Browse files** shows the snapshot's directories. Download a single file, a folder as `.zip`, or a whole volume as `.zip`. Downloads are audited, stream through the control plane (nothing is stored there) and are limited to 1 GiB per file. Listings show up to 5,000 entries per directory.

## What runs on the server

Everything that touches volume bytes runs in a **short-lived helper container** from the official `restic/restic` image, **pinned by digest** (`agentprotocol.ResticImage`, restic 0.19.1). The agent may itself run in a container and cannot read `/var/lib/docker`.

- The image is pulled on first use only (~30 MB). Docker cleanup never removes it.
- Helpers carry the label `nebulactrl.helper=backup` and **never** `nebulactrl.managed`, so the orphan sweep and Docker cleanup leave them alone. They run with no capabilities beyond file ownership and read access, a read-only root filesystem, `no-new-privileges`, 512 MiB / 1 CPU, no published ports and (local destinations) no network at all.
- The repository password and S3 keys are passed **per exec in the environment**, never in the helper's create config (which `docker inspect` would show) and never in a command line. restic's cache lives in the volume `nebulactrl-backup-cache`.
- A **local destination** lives under the agent's backup directory. On the bare host the helper bind-mounts it. When the agent itself runs in a container (dev, compose), the helper mounts the volume or bind of the agent container that holds the backup directory; the directory must therefore be on a mounted volume.
- Leftovers are swept: every two minutes and on every reconnect the agent removes helpers and staging volumes no running backup owns, thaws a container a dead backup left paused and starts one a dead `stop`-mode backup left stopped. A paused container no command holds is always thawed.
- The agent reports a frozen service as running and healthy; the reconciler never restarts it.

## Limitations

- **Crash-consistent is not application-consistent.** An app with no journal or with short leases can restore into a state it cannot start from. Use a before-backup command or stop mode, and test a restore now and then.
- A freeze can cost a stalled request or a failed health probe if it outlasts the proxy's health check (about 3 s). Keep the budget small, or use stop mode.
- Hosts without copy-on-write copy the data while frozen; large volumes (over a few GB) will exceed a 5 s budget there. Use stop mode, a before-backup command that writes a small consistent file plus excludes, or `live` mode for static data.
- Applying a restored volume is a recreate deploy (services with volumes never get a rolling deploy).
- Retention runs after each successful backup and deletes with `restic forget --prune`; a server that is offline retries after the next backup. Rows of forgotten backups stay as history.
- Not offered: restoring selected files into the live volume, restoring as a new service, test restores, bandwidth limits.
- **The built-in storage has a quota** (50 GiB per team by default, set by the instance admin). When it is full, new backups fail with "Built-in backup storage is full"; existing backups are never deleted to make room. Free space, ask the instance admin for more, or use a destination of your own. The team and the instance admin are alerted at 80% and 100% ([notifications](notifications.md)).

## API

Operations are tagged `volume-backups` in the OpenAPI document: `GET/PUT/DELETE /services/{id}/volume-backup`, `POST/GET /services/{id}/volume-backups`, `GET/DELETE /volume-backups/{id}`, `PUT /volume-backups/{id}/lock`, `POST /volume-backups/{id}/restore`, `GET /volume-backups/{id}/files` and `…/files/content`, `GET /services/{id}/volume-restores`, `GET /services/{id}/retired-volumes`, `DELETE /retired-volumes/{id}`, `POST /services/{id}/volume-backup/recovery-key`.
