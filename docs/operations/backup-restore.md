---
title: Control-plane backup and restore
description: "Back up NebulaCtrl itself: the database and the secret key, and how to restore."
section: Databases and backups
order: 40
---

# Backup and restore

NebulaCtrl's state is **one Postgres database plus one secret key**. They must be backed up together, and they must be stored in different places:

| Item | Contains | Without it |
|---|---|---|
| Postgres database `nebulactrl` | projects, environments, services, config, releases, deployments, audit log, API token hashes, agent identities (public keys), encrypted secrets, canvas layouts, job queue | everything is gone |
| `NEBULACTRL_SECRET_KEY` (and `NEBULACTRL_SECRET_KEY_PREVIOUS`, if you rotated) | the AES-256-GCM key ring for every sealed value: variables, git and registry credentials, SSH keys, integration tokens, TOTP seeds, backup passwords | the database restores but **every secret sealed with a missing key is unreadable** and must be re-entered |

The key never lives in the database, and **a backup never contains it** (a dump next to the key would defeat the encryption). Store it in a password manager or secrets vault, apart from the backups but retrievable on the day of a disaster. The key is a 32-byte value; `nebulactrl-server keygen` makes a new one. **Rotate with the procedure in [secrets at rest](../security/secrets-at-rest.md#rotating-the-key)** (new key as `NEBULACTRL_SECRET_KEY`, the old one as `NEBULACTRL_SECRET_KEY_PREVIOUS`, then `nebulactrl-server rekey`), never by just replacing the key: values sealed with a key that is not in the ring cannot be read. **Keep every old key for as long as backups taken before the rotation might be restored.**

Admin → Control-plane backup shows a **fingerprint** of the key (12 hex characters; it identifies the key without revealing it) and writes the same fingerprint into every backup. After a restore, the command prints it again, so you can check that the key you are about to start with is the one the backed-up instance used.

## What is *not* in the backup

- **Your services' data.** Docker volumes (`nc-vol-…`) and anything databases or apps keep on the agent hosts are not copied by the control-plane backup. *A Docker volume is not a backup.* Managed databases have their own [scheduled backups](database-backups.md); for everything else use the application's own dump tools or snapshots of the host.
- **Proxy certificates** (volume `nebulactrl-traefik-acme` on each agent host, holding `acme.json`). They are re-issued automatically; back the volume up only if you want to avoid rate limits during a rebuild.
- **Built images.** They are rebuilt from git (the release stores the commit) or re-pulled.
- **Browser sessions.** Session rows are left out on purpose: after a restore everyone signs in again.
- **Agent identities** live on each host in `/var/lib/nebulactrl-agent/agent.db` *and* as public keys in the database. After a restore from a backup, agents reconnect with the same identity as long as it is in the backup.

## Scheduled control-plane backup

**Admin → Control-plane backup** (instance admin only: the first account). It runs `pg_dump` against the control plane's own database and streams the result to one **instance backup destination**, which belongs to no team. It is independent of the per-team destinations used by [database backups](database-backups.md), though it uses the same kinds of storage.

| | Default | Notes |
|---|---|---|
| Schedule | daily at **03:30 UTC** | a 24-hour time of day in the UI; the API takes any 5-field cron (UTC), at most one run every 5 minutes |
| Retention | newest **14** successful backups | 1 to 365; the newest backup is never removed |
| State | **on** after the [installer](../installing/installer.md): a daily backup to the local directory below. Otherwise **off** until a destination is set up and you switch it on | |

What a run does:

1. `pg_dump --format=custom --no-owner --no-acl --exclude-table-data=sessions` streams into the destination. There is no temporary file; a dump that fails (or an upload that fails) leaves no object behind.
2. The upload is verified (size at the destination equals the bytes dumped).
3. A manifest `…dump.manifest.json` is written last, with the SHA-256, the PostgreSQL version, the key fingerprint and the schema version. A dump without a manifest is not a restore point.
4. Retention deletes the oldest backups of this destination beyond the count (dump and manifest). Backups made for an earlier destination stay where they are: changing the destination never deletes anything.

Objects are stored as `<prefix>/nebulactrl-instance/<yyyy>/<mm>/nebulactrl-<yyyymmddThhmmssZ>-<id8>.dump`.

Details worth knowing:

- **One run at a time.** A scheduled slot that comes due while a run is still going is skipped, not queued. A run has five hours.
- **Missed slots.** If the control plane is down at 03:30, one backup runs as soon as it is back up; the next slot is computed from then.
- **Failures are loud.** A failed run is recorded with the reason (credentials and the database password are masked), written to the audit log (`instance.backup.failed`) and sent as a **Backup or restore failed** notification to the instance administrator's team (the oldest team). Turn on **Backup or restore succeeded** to also hear about good runs. Set up an email or chat channel under [notifications](notifications.md), otherwise nobody is told.
- **Test the destination** with the **Test** button: the control plane writes, reads back and deletes a small probe object.
- **Back up now** starts a run immediately, whether or not the schedule is on.
- **Before an upgrade** the control plane runs a backup by itself (trigger *Before upgrade*), whether or not the schedule is on, and refuses to upgrade when it failed: [upgrades](upgrades.md#backup-before-a-control-plane-upgrade).
- Every change and every run is audited (`instance.backup.update`, `.run`, `.test`, `.succeeded`, `.failed`, `.key_acknowledged`). Credentials are recorded as "changed", never as values.

### Destinations

| Kind | Where | Off-site? |
|---|---|---|
| **S3-compatible bucket** | AWS S3, Cloudflare R2, Backblaze B2, Wasabi, MinIO, SeaweedFS, Garage … | yes |
| **Built-in storage** | the system bucket of the [built-in S3](platform-services.md) that runs beside the control plane; needs no endpoint, bucket or keys, and no team can reach it | **no**, labelled "not off-site" |
| **Directory on the control-plane host** | a sub-directory of `NEBULACTRL_BACKUP_DIR` | **no**, labelled "not off-site" |

A directory and the built-in storage are only a first copy: both live on the control-plane host, so if the host is lost, so are the backups. Admin says so when you choose **Built-in storage** and suggests an external destination; use a bucket, or sync the directory elsewhere. In the installer's Compose setup `NEBULACTRL_BACKUP_DIR` is `/var/lib/nebulactrl/backups` inside the container, bind-mounted from `/var/backups/nebulactrl` on the host (`--backup-dir` or `NEBULACTRL_BACKUP_HOST_DIR` changes it). Outside Docker set the variable yourself; without it the local option is unavailable.

S3 permissions for a bucket-scoped key: `s3:PutObject`, `s3:GetObject`, `s3:DeleteObject`, `s3:ListBucket`, `s3:AbortMultipartUpload`. Credentials are stored encrypted and never shown again. Turn on bucket encryption and keep the key's scope to this bucket: the dump contains password hashes, API token hashes and the *encrypted* secrets, so treat the bucket as sensitive.

### Requirements

- **`pg_dump` on the control-plane host.** The container image ships the PostgreSQL 17 client tools. A host install needs `postgresql-client` of at least the database server's major version. Without them the page says so and backups do not start.
- A reachable database through `NEBULACTRL_DATABASE_URL` (password and TLS settings are passed to `pg_dump` through its environment, never on the command line).

### The reminder card

Until an administrator confirms that the instance secret key is stored separately, a **Store your instance secret key separately** card shows on Admin → Control-plane backup. **I stored the key** dismisses it for everyone and is audited. It does not check anything: it is a promise, so keep it.

## Restore

Restoring replaces the control plane's knowledge with the backup's. Anything that happened after the backup (deployments, edits) is forgotten, and **running containers on agent hosts are not touched**: the reconciler reconciles them against the restored desired state when agents reconnect, and removes managed containers that belong to no known deployment after a 10 minute grace period. The teams' backups, images and logs in the [built-in services](platform-services.md) are not in the control-plane backup either: they stay on the host's disk.

A restore is a **command-line operation into an empty database**, run with the control plane stopped:

```
nebulactrl-server restore --from <source> [--at <timestamp>] [--database-url <url>]
```

| Flag | Meaning |
|---|---|
| `--from` | where the backups are: `s3://bucket/prefix` or a directory (the destination's directory, or any parent of it) |
| `--at` | restore the newest backup created **at or before** a time: RFC 3339 (`2026-10-27T03:30:00Z`), `"2026-10-27 03:30"` (UTC), a date (the end of that UTC day) or `latest` (the default) |
| `--backup-id` | restore exactly this backup (id or its first 8 characters); overrides `--at` |
| `--list` | list the backups at the source (id, time, size, PostgreSQL version, key fingerprint) and exit |
| `--database-url` | the **empty** target database; default `$NEBULACTRL_DATABASE_URL` |
| `--tmp-dir` | where the dump is downloaded and verified before restoring; needs room for one dump (default: the system temp directory) |
| `--endpoint`, `--region`, `--path-style` | S3-compatible stores (`s3://` sources only) |

For an S3 source the credentials come from the environment, never from the command line: `NEBULACTRL_RESTORE_ACCESS_KEY` and `NEBULACTRL_RESTORE_SECRET_KEY` (or the standard `AWS_ACCESS_KEY_ID` and `AWS_SECRET_ACCESS_KEY`). They are not read from the control plane's database, because that database is exactly what you are restoring. The command does not need `NEBULACTRL_SECRET_KEY`.

What the command does: it finds the backup, **downloads it and verifies its SHA-256 and size** against the manifest, refuses a target that already holds any table, and then runs `pg_restore --single-transaction --exit-on-error`. A damaged backup is rejected before a byte reaches the database, and a restore that fails half-way is rolled back, leaving the database empty so you can try another backup. On success it prints the key fingerprint the original instance used.

### Step by step with Docker Compose

```sh
cd /opt/nebulactrl    # where the installer put docker-compose.yml and .env
# 1. stop the control plane, keep Postgres running
docker compose stop nebulactrl
# 2. recreate an empty database
docker compose exec -T postgres psql -U nebulactrl -d postgres \
  -c 'DROP DATABASE nebulactrl WITH (FORCE)' -c 'CREATE DATABASE nebulactrl OWNER nebulactrl'
# 3a. restore from the local backup volume ...
docker compose run --rm --no-deps nebulactrl restore \
  --from /var/lib/nebulactrl/backups --tmp-dir /var/lib/nebulactrl/backups --at latest
# 3b. ... or from a bucket
docker compose run --rm --no-deps \
  -e NEBULACTRL_RESTORE_ACCESS_KEY=... -e NEBULACTRL_RESTORE_SECRET_KEY=... \
  nebulactrl restore --from s3://my-bucket/nebulactrl --endpoint https://s3.example.com --path-style \
  --tmp-dir /var/lib/nebulactrl/backups --at 2026-10-27T03:30:00Z
# 3c. ... or from the built-in storage (a corrupt database on a live host; see below)
docker compose run --rm --no-deps \
  -e NEBULACTRL_RESTORE_ACCESS_KEY=... -e NEBULACTRL_RESTORE_SECRET_KEY=... \
  nebulactrl restore --from s3://nebulactrl-system --endpoint http://seaweedfs:8333 --path-style --region us-east-1 \
  --tmp-dir /var/lib/nebulactrl/backups --at latest
# 4. start the control plane with the ORIGINAL NEBULACTRL_SECRET_KEY in .env
docker compose up -d nebulactrl
```

(`--tmp-dir` points at the volume because the container's `/tmp` is a RAM-backed tmpfs.) `docker compose run … restore --list --from …` shows what is available first.

**Restoring from the built-in storage.** The built-in S3 has no public address and no published port, so nothing outside the host can read it: only a container on the Compose network `svc-s3` can (the control plane is on it), which is why 3c runs the restore through `docker compose run` and reaches the store as `seaweedfs:8333`. The bucket is `nebulactrl-system` (add your prefix if you set one in the destination); the key is the store's admin key, `NEBULACTRL_S3_ADMIN_KEY` and `NEBULACTRL_S3_ADMIN_SECRET` from `.env`, because the control plane's database is the thing being restored. This is for a **corrupt database on a host that is still alive**. It does not survive the loss of the host, because the backups are on the same disk: after a lost host you need an **external bucket** (or a copy of the directory) from before it was lost.

Without Compose, run the same `nebulactrl-server restore` against your database, with `pg_restore` of at least the dump's PostgreSQL major version on the `PATH`.

Verify: `curl -fsS "$NEBULACTRL_PUBLIC_URL/readyz"`, sign in (everyone has to: sessions are not backed up), open a service and confirm a secret variable shows as *set*; deploy a harmless change to prove secrets decrypt. If a deploy fails to resolve variables with a decryption error, the key does not match the backup: compare it with the fingerprint the restore printed.

Restore with the **same or a newer** `nebulactrl-server` than the one that wrote the backup. A newer one migrates the schema forward on start; an older one warns that the backup has a newer schema.

To restore onto a **new machine**: install the same version (see [upgrade](../installing/upgrade.md)), put the original `NEBULACTRL_SECRET_KEY` in `.env`, restore as above, and keep (or re-point DNS for) `NEBULACTRL_PUBLIC_URL`, because agents dial that URL. If the URL must change, re-run the installer on each host with the new `--url`.

### When the restore says no

| Message | Cause and fix |
|---|---|
| `the target database is not empty` | restore only into a new, empty database (`CREATE DATABASE nebulactrl`), never over a live one |
| `SHA-256 … it is damaged` / `incomplete or damaged` | the object at the source is not the one that was written; pick another with `--at` or `--backup-id` |
| `no control-plane backups were found at the source` | wrong `--from` or prefix; run `--list`; a dump without a manifest is not offered |
| `no backup at or before …; the oldest one is from …` | `--at` is older than every backup; retention removed it |
| `pg_restore was not found` | install the PostgreSQL client tools (the image has them) |
| `pg_restore: error: … unsupported version` | the client tools are older than the dump's PostgreSQL; install a newer `postgresql-client` |
| `S3 credentials are missing` | set `NEBULACTRL_RESTORE_ACCESS_KEY` and `NEBULACTRL_RESTORE_SECRET_KEY` |

## Manual backup (alternative)

If you cannot use the scheduled backup, a plain dump works and restores with the same tools:

```sh
cd /opt/nebulactrl
mkdir -p backups
docker compose exec -T postgres pg_dump -U nebulactrl -d nebulactrl --format=custom \
  > "backups/nebulactrl-$(date -u +%Y%m%dT%H%M%SZ).dump"
```

Restore it into an empty database (steps 1 and 2 above), then load it with `docker compose exec -T postgres pg_restore -U nebulactrl -d nebulactrl --no-owner < backups/<file>.dump` and start the control plane (step 4). (`nebulactrl-server restore` only reads backups written by the scheduled backup, because it relies on the manifest.) Also record: the `.env` file (it holds the key and the database password), the image tag or commit you run, and your DNS records.

Restoring **a managed database** from a dump file you made yourself (`pg_dump`, `mysqldump`, `mongodump`) is a different operation: see [restore from a dump file](database-backups.md#restore-from-a-dump-file).

## Test it

A backup you have never restored is a hope. Quarterly: restore the latest backup into a scratch Compose project (`docker compose -p nebulactrl-restore-test …` with a different `NEBULACTRL_PORT`), start it **without agents enrolled to it** (it has the same agent identities, so do not point real agents at it), and check that you can sign in and see your projects.
