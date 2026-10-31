---
title: Database backups
description: "Scheduled and on-demand dumps to S3 or a server directory, and how to restore them."
section: Databases and backups
order: 20
---

# Database backups

NebulaCtrl backs up **managed databases** (the database panel's engines) on a schedule or on demand, keeps them in a destination you choose, prunes old ones, and restores them. This page is about your databases. Backing up the control plane itself (Postgres and `NEBULACTRL_SECRET_KEY`) is [backup-restore.md](backup-restore.md).

## How a backup runs

The dump never touches the control plane's disk or memory on its way to a destination of your own. To the built-in storage it is carried as a stream over the agent's link and lands in the S3 service on the control-plane host, which is where the team's slice lives.

1. The control plane records a backup run and queues a `db_backup` command for the agent of the database's server. The command names the destination and the object key; it carries **no credentials**. The destination's keys and the database password are attached to the frame at send time and live only in agent memory for the run.
2. The agent runs the engine's own dump tool **inside the database container** (an argv vector, no shell; credentials go through the exec environment, not the command line, except MongoDB whose tools only take them in the URI).
3. The dump is streamed: `dump stdout → (gzip) → SHA-256 → destination upload`. There is **no temp file**, so a 50 GB dump does not need 50 GB of spare disk. S3 uploads are multipart with 16 MiB parts, at most two in flight.
4. The exit status is checked **before** the stream may end. A dump that fails halfway makes the upload fail: the multipart upload is aborted (or the local temp file removed), no object and no manifest are left behind, and the run shows `failed` with the dump tool's own error.
5. After the upload the agent writes `<object>.manifest.json` (engine, version, database, format, size, SHA-256, project/service names) and verifies the stored size. A restore into a fresh control plane can understand backups from the bucket alone.

Runs have a real state: `queued` → `running` (the agent acknowledged) → `succeeded` | `failed`. A run is `skipped` when it was not attempted (database stopped, not deployed, or the server offline) and `expired` when the command was never delivered. Nothing is ever reported as a success that did not happen.

| Engine | Tool (in the container) | Stored as | Restore |
|---|---|---|---|
| PostgreSQL | `pg_dump --format=custom --no-owner --no-acl` | `.dump` | `pg_restore --single-transaction --exit-on-error` |
| MySQL | `mysqldump --single-transaction --quick --routines --events --triggers` (as `root`) | `.sql.gz` | `mysql` |
| MariaDB | `mariadb-dump …` (as `root`) | `.sql.gz` | `mariadb` |
| MongoDB | `mongodump --archive --gzip` | `.archive.gz` | `mongorestore --archive --gzip` |
| Valkey / Redis | `valkey-cli --rdb -` / `redis-cli --rdb -` | `.rdb` | **not automated** (see below) |
| ClickHouse | not supported | | |

Key-value dumps are RDB snapshots. They are real backups, but loading an RDB means replacing the data file of a stopped server, which NebulaCtrl does not do: download the object and place it as `dump.rdb` in the data volume yourself.

## Destinations

Team settings → **Backup storage** (admin scope). Three kinds:

- **Built-in backup storage** (kind `builtin`): every team has it from the minute it is created, with nothing to set up. It is the team's slice of the S3 that runs beside the control plane ([built-in services](platform-services.md)): a bucket and a key that only touches it, which the team never sees. The card shows used space against the quota and the state; it cannot be edited or deleted. **It is not off-site**: the data is on the control-plane host, so if that host is lost, so are the backups; add an external destination for what you cannot lose. When the quota (50 GiB per team by default) is reached, **new backups fail with "Built-in backup storage is full"**; existing backups are never deleted to make room. The team and the instance admin are alerted at 80% and 100% ([notifications](notifications.md)). It holds NebulaCtrl's backups only; it is not an S3 for your projects.
- **S3-compatible** (AWS S3, Cloudflare R2, Backblaze B2, Wasabi, Hetzner Object Storage, SeaweedFS, Garage, …). Built on `aws-sdk-go-v2`. For a custom endpoint choose *path-style* if the store needs it and keep **compat mode** on (it is the default for custom endpoints): since 2025 the AWS SDK adds CRC32 checksums to every request, which several S3 implementations reject. Compat mode limits them to operations that require them. Use a **bucket-scoped key** with `s3:PutObject`, `s3:GetObject`, `s3:DeleteObject`, `s3:ListBucket` and `s3:AbortMultipartUpload`.
- **Local directory on the server** — flagged **not off-site**. Files are written under the agent's backup directory (`--backup-dir`, default `<state-dir>/backups`, `/var/lib/nebulactrl-agent/backups`), in the sub-directory you name. A destination can not point outside that directory; to use another disk mount it there or set `--backup-dir`. A backup on the database's own host does not survive the loss of that host, so use it as a first copy, not the only one. A local backup lives on one server and can only be restored onto a database on that same server.

Keys are sealed with `NEBULACTRL_SECRET_KEY` and are write-only in the API and UI (`accessKeySet: true`, never the value). Restoring the control plane without its secret key loses them.

**Test** writes, reads back and deletes a probe object from the agent of a server you pick (the agent is the one that talks to the destination). The result and any error stay on the destination.

A destination that schedules or stored backups use can not be deleted, and its endpoint, bucket, prefix and directory can not change while backups live in it.

## Schedules and retention

A database panel → **Backups** → *Add schedule*: a destination, a cron expression, a timezone and retention.

- Cron is the standard 5 fields (`minute hour day-of-month month day-of-week`) or `@hourly`, `@daily`, `@weekly`, `@monthly`, `@yearly`. Ranges, lists, steps (`*/15`, `10-50/10`) and month/weekday names work. When both day fields are restricted a day matches if either does (classic cron). The parser is in-house (`internal/cronexpr`), with its next-run calculation tested across daylight-saving changes: a wall time that does not exist (spring forward) is skipped that day, a repeated one (fall back) runs once.
- At most one backup every **5 minutes**.
- A tick runs every minute. Missed runs are not replayed; after downtime a schedule runs once and moves on.
- **Missed backups are reported.** A schedule whose backup did not succeed within its window (the next due time after the last success plus a grace of a quarter of the interval, 15 minutes to 6 hours) raises `backup.missed` once, until the next success. It covers the case where nothing ran at all (control plane or server down, destination unreachable). See [Service alerts](service-alerts.md#missed-backups).
- **Retention**: keep the newest *N* successful backups of the schedule (default 7) and/or delete those older than *D* days. `0` disables a limit. The newest backup is never pruned, and **locked** backups are never pruned and do not count towards *N*. Backups made on demand are never pruned automatically. Pruning deletes the object through the agent of the server that made it (an offline server retries after the next successful backup).

## Restore

Pick a backup → **Restore**.

- **To a new database (default).** NebulaCtrl creates a sibling database service next to the original (same engine, version and settings, its own volume, **fresh credentials**), deploys it empty, waits until it runs, then streams the dump into it. The original is untouched. Point your apps at the new database when you are satisfied, then delete the old one. For restores onto the same server this needs disk for both copies.
- **In place (advanced, destructive).** Type the database's slug to confirm. NebulaCtrl first takes a safety backup (marked *pre-restore*) to the same destination; only if that succeeds it restores over the database (MongoDB with `--drop`; PostgreSQL as described below). If the safety backup fails nothing is changed. NebulaCtrl can not stop your applications' writes: stop dependants first if consistency matters.

**PostgreSQL** is restored into a *scratch database* created from `template0` (no extensions, no schemas) and swapped in under the database's name only when it restored completely and the checksum matched, in both modes. A dump creates its own extensions and schemas, and some images fill the database they provision with them (the PostGIS image creates `postgis`, `postgis_topology`, `fuzzystrmatch` and `postgis_tiger_geocoder` with their `tiger` and `topology` schemas), so a restore straight into that database would fail with `schema "tiger" already exists`. A failed restore drops the scratch database and leaves the target as it was; a successful in-place restore ends the connections to the database at the swap, renames the old one away and drops it. The managed user needs the right to create databases (it does: it is the superuser of the managed server).

The agent verifies the object's SHA-256 against the recorded value while restoring; a mismatch fails the restore and reports it, and PostgreSQL's swap does not happen. Restore to a new database only happens after the new database's deployment succeeded; a failed deployment fails the restore and leaves the sibling for you to inspect or delete.

A backup is not proven until it restores. Restore the latest one into a new database now and then and look at the data.

## Restore from a dump file

Moving a database in from somewhere else (another server, another panel, a laptop): **Database → Backups → Restore from a file**, or

```sh
nebulactrl databases restore shop/production/postgres --file shop.pgdump
nebulactrl databases restore shop/production/postgres --file shop.sql.gz --in-place --confirm postgres --destination s3
```

| Engine | Accepted files |
|---|---|
| PostgreSQL | `pg_dump -Fc` (custom), `-Ft` (tar) or plain SQL, each optionally `.gz` |
| MySQL, MariaDB | plain SQL from `mysqldump` / `mariadb-dump`, optionally `.gz` |
| MongoDB | `mongodump --archive`, optionally `.gz` |

The format is detected from the file's first bytes; the wrong engine, a `pg_dumpall` cluster dump, zstd/xz/zip, UTF-16 text and Redis/Valkey RDB files are refused with the reason, before anything is changed. Make a Postgres dump with `pg_dump -Fc -U <user> <db> > shop.pgdump`.

**How the file travels.** The upload goes through the control plane to the server's agent as a stream and is piped into the engine's own client (`pg_restore`, `psql`, `mysql`, `mongorestore`) inside the database container. Nothing is stored on the control plane or written to the agent's disk, and at most one 256 KiB window is in memory, so a 5 GB file works. The default limit is **5 GiB** (`NEBULACTRL_MAX_DUMP_UPLOAD_BYTES`); a larger file is refused at the start. Throughput is bounded by the window over the round trip to the agent. If you front the control plane with a proxy that caps request bodies (a Cloudflare-proxied hostname does at 100–500 MB), upload through a direct or DNS-only hostname.

**Targets.** As for backups, **a new database is the default**: a sibling is created, deployed empty, and the file is restored into it; the original is untouched. **In place** needs the database's slug and a backup destination for the safety backup (default: the one of its schedule); only after that backup succeeded can the file be sent. The restore waits in `awaiting_upload` until the target runs, then the client sends the file (the CLI and web app do this for you; the raw endpoint is below).

**What PostgreSQL does.** The file is restored into a *scratch database* created from `template0`, and swapped in only when it restored completely: the live database is renamed away, the scratch database takes its name, the old one is dropped (connections are ended at that moment). A broken or abandoned file therefore leaves the data exactly as it was, and PostGIS dumps work because their extensions and schemas are created by the dump itself in an empty database. `pg_restore` runs with `--no-owner --no-acl --role=<managed user>`: every object ends up owned by the managed user and grants are dropped. For plain SQL the same is done on the stream: ownership, `GRANT`/`REVOKE`, role, tablespace, `CREATE/DROP/ALTER DATABASE` and `\connect` lines are skipped, and so is every other psql meta-command (`\!`, `\i`, `\o`, `\copy`, …): a dump file can never make the client run a shell command or touch a file. A stream that hides a meta-command in a way the filter cannot follow is refused rather than altered. The log counts what was skipped. In-place needs room for both copies while it runs.

**MySQL, MariaDB, MongoDB** are restored directly into the target database; they have no atomic swap. `CREATE/DROP DATABASE` and `USE` lines and `DEFINER=` clauses of a SQL dump are removed so the data lands in the managed database, and client commands (`system`, `source`, `tee`, `pager`, `\!`, …) are dropped; `mongorestore` restores the databases the archive contains (`--drop` for in-place). A failed in-place restore can leave part of the dump there: restore the safety backup, or prefer a new database.

Progress and errors (the client's own messages, with credentials masked) are kept as the restore's log, shown in the dialog and in `GET /dump-restores/{id}`. The control plane records the SHA-256 of what it relayed. A restore whose transfer is lost (control plane restart, agent offline) is failed with "nothing was switched over"; start it again.

API: `POST /services/{id}/dump-restores` (202; `mode`, `name`, `serverId`, `confirmSlug`, `destinationId`, `fileName`, `sizeBytes`), `GET /dump-restores/{id}`, `GET /services/{id}/dump-restores`, `POST /dump-restores/{id}/cancel`, and the raw upload `PUT /api/v1/dump-restores/{id}/upload?fileName=` (body = the file, `Content-Length` required; 202 once the agent has the whole file; 409 `dump_restore_not_ready` until the status is `awaiting_upload`). Statuses: `provisioning`, `pre_backup`, `awaiting_upload`, `uploading`, `restoring`, `succeeded`, `failed`. Needs `deployments:write` (and `projects:write` for a new database). Audited as `backup.dump_restore.create|upload|succeeded|failed|cancel`. The create operation is not available over MCP (it needs a binary upload); reads are.

Limits: PostgreSQL 16+ images, agents of this release or newer (an older agent never answers and the upload is refused after 60 s), no `pg_dumpall` cluster dumps, no URL or object-storage import, no resumable uploads.

## API, audit, permissions

- Destinations: `GET/POST /backup-destinations`, `PATCH/DELETE /backup-destinations/{id}`, `POST …/test`. Admin scope to write.
- Per database: `GET/POST /services/{id}/backup-schedules`, `GET/POST /services/{id}/backups` (202), `GET /services/{id}/backup-restores`.
- `PATCH/DELETE /backup-schedules/{id}`, `GET/DELETE /backups/{id}`, `PUT /backups/{id}/lock`, `POST /backups/{id}/restore` (202), `GET /backup-restores/{id}`.
- Creating, restoring and deleting need `deployments:write` on the project; reading needs `projects:read`. Creates honour `Idempotency-Key`.
- Every change is audited (`backup.create`, `backup.restore`, `backup.delete`, `backup.lock`, `backup.schedule.*`, `backup.destination.*`); credential changes record only that they happened.
- Finished backups and restores raise [notifications](notifications.md): `backup.failed` and `backup.missed` by default, `backup.succeeded` if you turn it on.

## Limits

- Logical dumps only: no point-in-time recovery, no volume snapshots.
- No client-side encryption: protect the bucket and its key. A planned opt-in is `age` encryption; a lost key makes backups unreadable, so it will be explicit.
- Backups of one database are not coordinated with another's.
- Deleting a project or database removes its backup history rows but leaves the stored objects: delete them from the destination if you do not need them.
