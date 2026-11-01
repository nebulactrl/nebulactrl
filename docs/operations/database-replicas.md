---
title: Postgres standby replicas and manual Promote
description: "Advanced, Postgres only: a standby replica on another server, read offload, manual promote and rebuild. Not a backup, and nothing switches over by itself."
section: Databases and backups
order: 15
---

# Postgres standby replicas and manual Promote

> **Advanced feature, PostgreSQL only.** Most databases do not need it. Start with [scheduled backups](database-backups.md); add a standby replica only if one of the cases below is yours.

A **standby replica** is a second Postgres service in the same environment that follows the [managed database](databases.md) with streaming replication and serves read-only queries. In the database panel it sits in a collapsed **Advanced** section, which opens by itself when the database already has replicas, is one, or is fenced. If the primary is lost, you can **Promote** a replica by hand and the platform repoints your apps.

## When to use it

- **A standby on another server.** A copy of the database streams to a second server over the [mesh](mesh.md). If the first server is lost, you promote the copy by hand and the apps follow after you deploy the staged rewrite. This is the case the feature is made for.
- **Read offload.** Reports, exports or read-heavy queries run against `DATABASE_READ_URL` and leave the primary's CPU for writes. A replica on another server also spares the primary's disk and network.

## When not to use it

- **As a backup.** A replica is **not a backup**: a `DROP TABLE` or a bad `UPDATE` replicates within seconds. Keep [scheduled backups](database-backups.md); there is no point-in-time recovery.
- **On the same server, for safety.** A replica on the primary's own server shares its disk and host, so it does not survive losing that server. It only helps with read load. The add dialog says so and still lets you do it.
- **As an automatic switch-over.** NebulaCtrl does not watch the primary and does not switch anything by itself: there is no leader election and no witness. Promote is a decision you make and confirm.

There is no synchronous replication and no cascading replicas. See [limits](#limits).

Scope: **PostgreSQL only** (16, 17 and 18, plain and PostGIS flavor). MySQL, MariaDB, Valkey, Redis, MongoDB and ClickHouse have no replicas, and their panels do not show this section.

## How it works

- A replica is an ordinary `kind=database` service of the primary's environment, with its own data volume and its own deployments, logs and cancel. Its engine, version, flavor, database name and user match the primary.
- Replication is **asynchronous** and physical (`pg_basebackup`, then a WAL stream with a replication slot).
- A primary has **at most 5 replicas** (`replicas_limit`). A replica cannot have replicas of its own (`replica_not_primary`).
- The primary needs **no restart and no configuration change**: Postgres' defaults already allow replication. Adding a replica creates a dedicated replication role `nebula_r_<id>` on the primary, adds a managed block to `pg_hba.conf` for it, reloads the configuration and caps the WAL the primary keeps for a lost replica (see [Disk use on the primary](#disk-use-on-the-primary)). The role, the block and the slot are removed again when the replica is removed.
- The replica keeps the primary's setting of outbound traffic: **Block outbound internet stays on**. Its only route to the primary is the environment network (same server) or the mesh link.

## Add a replica

Primary's database panel → **Advanced → Standby replica → Add standby replica**, or:

```
POST /services/{primaryId}/database/replicas
{"name": "pg-replica", "serverId": "…", "acceptMeshChanges": false}
```

`"dryRun": true` returns the plan (`200`) without changing anything. Needs `projects:write` and `deployments:write`; the CLI is `nebulactrl databases replicas add`. The real request answers `202` with a **saga**, not a staged change:

1. **prepare**: the role, the `pg_hba.conf` block and the slot cap on the primary;
2. **link**: nothing on the same server; across servers, the exposure, link and grant on the [mesh](mesh.md) and a connectivity check;
3. **clone**: one agent command runs `pg_basebackup` into the replica's new volume (large databases take a while: the default time limit is 6 hours; **cancel** deletes the half-written volume);
4. **deploy**: the replica service is deployed through the normal deploy path;
5. **streaming**.

The saga is re-entrant: a restart of the control plane or a disconnected agent resumes it. Replica states are `pending`, `preparing`, `cloning`, `deploying`, `streaming`, `lagging`, `broken`, `needs_rebuild`, `promoting`, `removing` and `failed`.

The replica copies the primary's memory and CPU limits, Docker options and stop grace period; edit them afterwards like any service. It is placed on the server you choose and **cannot be moved to another server afterwards** (`replica_pinned`: a move does not move volumes anyway); use [Rebuild](#rebuild) instead.

The replica's start command refuses to start with an **empty data directory** (it exits with code 3 and says "rebuild the replica"), because the stock entrypoint would otherwise initialise a fresh, empty primary that looks healthy.

### Same server and cross-server

| | Same server | Across servers |
|---|---|---|
| Path | the replica joins the environment network and connects to the primary's slug on port 5432 | the replica connects to a **link node** alias on the replica's server, which forwards over the mesh to the primary's server node and then to the primary |
| Needs | nothing extra | [mesh](mesh.md) **enabled on both servers** (`mesh_server_required` blocks otherwise, with a one-click plan to enable it), `/dev/net/tun` on the replica's server (`mesh_no_tun`), an agent that supports it (`mesh_agent_too_old`), the mesh not paused, the coordinator reachable (`mesh_coordinator_unreachable` for a new replica) |
| Devices | none | adds the link node (and the server nodes if enabling the mesh), the plan says "adds N devices" and asks for `acceptMeshChanges` (`mesh_confirmation_required` without it) |
| Encryption | local | WireGuard between the link node and the server node; the last hop on each host is local |
| Removing the last replica | releases the role, slot and block | also releases the grant, then the link and the exposure |

The replication link and an app's [service link](mesh.md#cross-server-service-links) are separate nodes with separate rules: a replica's link cannot reach an app port.

### Disk use on the primary

A replica that stops reading makes the primary keep WAL for its slot. NebulaCtrl sets `max_slot_wal_keep_size` to the smaller of **4 GiB** and **10 % of the server's disk**, so a dead replica cannot fill the primary's disk: past the cap the slot is invalidated (`wal_status = lost`), the replica becomes `needs_rebuild` and the primary carries on. The replica row shows the WAL retained by its slot.

## Writer and reader variables

Every member of a replica group (the primary and each replica) exports the same set of **derived variables**, in addition to the [usual ones](databases.md#connecting-other-services). They are computed when a reference is resolved, are not stored and never produce a staged change on their own:

| Export | Value |
|---|---|
| `DATABASE_WRITE_URL`, `DATABASE_WRITE_HOST`, `DATABASE_WRITE_PORT` | the group's **current primary** |
| `DATABASE_READ_URL`, `DATABASE_READ_HOST`, `DATABASE_READ_PORT`, `DATABASE_READ_HOSTS` | the replicas that are `streaming` or `lagging` and **reachable by the consumer** |

Use them as `${{pg.DATABASE_WRITE_URL}}` and `${{pg.DATABASE_READ_URL}}`, where `pg` is the slug of any member. The URL and password exports are masked and redacted like `DATABASE_URL`.

Which replicas a consumer gets:

1. the replicas **on the consumer's server**, if any;
2. otherwise the replicas on other servers, reached through the mesh (the port is the mesh port, resolved per consumer);
3. otherwise **the writer**: with no usable replica, `DATABASE_READ_*` equals `DATABASE_WRITE_*`.

One replica gives a plain URL; several give a libpq multi-host URL, `postgres://user:pass@h1:5432,h2:5432/db` (a driver without multi-host support can use `DATABASE_READ_HOST`). `DATABASE_URL`, `PGHOST` and the others keep describing the service they are read from: on a replica that is the read-only server.

When a replica joins, leaves or changes **health class** (`streaming` or `lagging` against `broken`, `needs_rebuild` and the like; **not** its lag numbers), the services that reference any member are **staged**, never deployed. There is no reader proxy or pooler: the application picks the URL.

## Lag and health

The agent polls every 30 seconds, on the local socket, without any command traffic and without secrets. Lag comes from the **primary's** `pg_stat_replication` (bytes behind, and the replay lag in milliseconds); if that is unavailable it falls back to the replica's own received-versus-replayed difference and marks it stale. Time-based lag measured on the replica alone is not used, because it reads as unknown while the database is idle.

| State | Meaning |
|---|---|
| `streaming` | connected and replaying |
| `lagging` | more than 64 MiB or 60 seconds behind for 5 minutes |
| `broken` | the replica has not been connected to the primary for more than 2 minutes (the receiver stopped, the link is down or the primary is gone) |
| `needs_rebuild` | the slot was lost (`wal_status = lost`), or the replica follows a primary that was promoted over it |

Cards show the lag, the primary shows a `n replicas` chip and the replica `Replica of <slug>`. An observation older than 90 seconds is shown as "unknown". Notifications (see [notifications](notifications.md)) fire on a **transition**: `database.replica.lagging` and `database.replica.broken`, de-duplicated.

Two Postgres behaviours to know: `hot_standby_feedback` is **off**, so long queries on a replica never bloat the primary, and `max_standby_streaming_delay` is 30 seconds, so **a long query on a replica can be cancelled** when it conflicts with replay. Retry it or use a dedicated longer-running copy.

`GET /services/{id}/database/replication` returns the status; `GET …/database` includes `replication` (`role`: `standalone`, `primary`, `replica` or `fenced`, the primary, the replicas with state and lag, and whether a rebuild is possible).

## Promote

**Promote** turns a replica into the new primary. It is **manual**. The dialog says so: "Manual. NebulaCtrl does not detect failures or switch automatically." Use it for planned maintenance or after you have decided the primary is gone.

`POST /services/{replicaId}/database/promote`, with `"dryRun": true` first for the plan, then the real request with `{"confirm": "<replica slug>", "acceptDataLoss": false}`. It needs `projects:write` and `deployments:write`, and a production environment needs the production approval like a deploy ([production guard](../reference/mcp.md#production-approval)). Only one promotion per replica runs at a time (`promote_in_progress`). There is no MCP tool to promote.

### The plan

The dry run reports:

- the **mode**: `switchover` when the primary's server is online and its container is running, otherwise `forced`;
- the last observed lag and how old that observation is;
- the **consumers** whose variables reference the old primary, listed **per server**, with the links (and devices) the new primary needs for each consumer server;
- a warning `mesh_server_required` for a consumer on a server without the mesh: it cannot reach the new primary until the mesh is enabled there;
- the other replicas that will need a rebuild, and the backup schedules that will move;
- blockers: the replica is not `streaming`, `lagging` or `broken` (`replica_not_streaming`), its agent is offline (`replica_agent_offline`), or another promotion is running (`promote_in_progress`).

### Switchover (the old primary is reachable)

No committed data is lost.

1. **Freeze**: the old primary is set read-only and other sessions are ended.
2. **Stop and fence**: it is stopped cleanly, a fence is recorded and its restart policy is set to `no`.
3. **Final position**: the platform reads the last checkpoint from the **stopped** data directory and waits (5 minutes) until the replica has replayed it. If that times out, the old primary is restarted, unfrozen and unfenced, the promotion fails and **nothing was lost**.
4. **Promote** the replica (`pg_promote`) and clear its replica settings.
5. **Repoint** (below).
6. The old primary stays **fenced and stopped, with its data kept**.

### Forced (the old primary is unreachable or revoked)

The fence is recorded **first**, so the old primary cannot run as a primary again, then the replica is promoted. Nothing is read from the lost server. It needs `"acceptDataLoss": true` (`data_loss_not_accepted`, 422). **Everything the replica had not yet received is lost**: with asynchronous replication that is up to the replication lag, shown in the plan ("up to the replication lag, which is `<n> MiB` as of `<t>`, or unknown, will be lost").

### Fencing

A fenced service is carried in every reconcile: the agent stops its container, sets its restart policy to `no` and refuses to start it on a deploy. There is **no "unfence"**, because running two writers is how data diverges. A fenced old primary can be [rebuilt as a replica](#rebuild) of the new primary or deleted. **Honest limit:** on a forced Promote, if the old primary's host comes back and Docker restarts the container (`unless-stopped`) before the agent reconnects and fences it, writes accepted in that window are lost; there is no merge, and rebuild is the only way back.

### Repoint: consumers on any server

In one transaction the platform:

- turns the replica into a standalone database, keeps its credentials (references to the old primary become literal copies of the same values) and clears its replica settings;
- marks the **other replicas `needs_rebuild`**, now following the new primary;
- **moves the backup schedules** to the new primary (the backup history stays with the old service);
- **stages a rewrite of the consumers**: in every consumer on **any server**, every `${{<old>.KEY}}` reference to the old primary becomes `${{<new>.KEY}}`. Nothing is deployed: the rewrites show in the staged bar, can be reviewed and discarded, and apply with **Deploy staged**;
- derives the grants the new primary needs for those consumers, adding the links the plan listed; the old primary's grants are dropped after the grace period.

A consumer that copied a connection string as plain text is not found; update it by hand. A consumer on a server **without the mesh** cannot reach a primary that moved to another server until the mesh is enabled there. A forced Promote while the old primary's server is dead depends on nothing from that server. It is audited as `database.promote` and `database.fence`.

## Rebuild

**Rebuild** (`POST /services/{id}/database/rebuild`, on a replica or a fenced primary; the CLI is `nebulactrl databases rebuild`) clones the database again from its primary into a **new data volume**; the old volume stays until you delete it. Use it for `needs_rebuild`, `broken` replicas whose slot is gone, a replica that followed a primary that was promoted over it, and the **old primary after a Promote**: its body may name the primary to follow (`followServiceId`), by default the replica that was promoted over it. There is no `pg_rewind`: rebuild is simpler and safe, at the cost of copying the whole database. Audited as `database.replica.rebuild`.

## Interactions

**Backups.** [Database backups](database-backups.md) run on the **current primary only**. A schedule or a manual backup on a replica is refused (`backup_replica_unsupported`): a `pg_dump` on a standby can be cancelled by replay conflicts. A Promote moves the schedules with the role.

**Restore.** Restoring into a **new** database creates an ordinary standalone database, without replica settings. Restoring **in place** is refused on a replica (`replica_read_only`) and allowed on a primary with a warning: the restore replicates to every replica as ordinary writes.

**Upgrades.** A major version upgrade of a primary or a replica is **blocked while the group has replicas** (`replicas_exist`): `pg_upgrade` carries neither the `pg_hba.conf` block nor the replication slots. Remove the replicas, upgrade, add the replicas again. The minor version can drift when a floating major tag is redeployed: the plan warns, and you should redeploy the replicas first.

**Deleting** a primary that has replicas is refused (`replicas_exist`), and a replica cannot be deleted as a plain service (`replica_remove_required`): use **Remove replica** so its slot, role and `pg_hba.conf` block on the primary are dropped. **Credentials**: rotation on a replica is blocked (`replica_read_only`); rotate on the primary, the new password replicates and the replica's references stay consistent. A [public port](databases.md#public-port) on a replica is allowed (it is a read-only server) and the plan says so. **Rollback** of a replica restores its configuration; the empty-data guard keeps a restored replica setting from doing harm on a promoted service.

**Removing a replica** stops it, drops its slot, role and block on the primary, deletes its service and then waits for the agent to remove its container and **data volume** before the replica is gone: the replica stays in `removing` until the agent reports the removal done, and a removal the agent reports as incomplete (a volume still in use, say) is sent again up to three times in all. A replica whose deploy is still running is stopped once that deploy is over, so it cannot stay connected to the primary; a drop the primary refuses (a slot still in use) is likewise sent again up to three times in all, because a slot left behind keeps the primary's WAL. An agent that is offline, or that does not answer within 10 minutes, does not hold the replica forever: the removal is given up with a warning in the control plane log, and a queued removal still runs when the agent returns. A drop that keeps failing or a primary that is offline is given up the same way: the warning names the slot, which you can drop on the primary with `SELECT pg_drop_replication_slot('<slot>')`.

**Servers.** A revoked server's replicas become `failed`; replicas whose primary was on it become `broken` and Promote is offered. [Draining](draining-servers.md) a server moves services but not volumes, so a replica cannot be moved (`replica_pinned`).

## API, CLI and audit

| | |
|---|---|
| Add | `POST /services/{id}/database/replicas` |
| Remove | `DELETE /services/{id}/database/replica` (on the replica) |
| Promote | `POST /services/{id}/database/promote` |
| Rebuild | `POST /services/{id}/database/rebuild` |
| Status | `GET /services/{id}/database/replication` |
| CLI | `nebulactrl databases replicas add\|ls\|rm`, `nebulactrl databases promote <replica> [--yes] [--accept-data-loss]`, `nebulactrl databases rebuild <service> [--follow]` |
| Audit | `database.replica.add`, `database.replica.remove`, `database.replica.rebuild`, `database.promote`, `database.fence` |

Errors: `replica_wrong_engine`, `replicas_limit`, `replica_not_primary`, `replicas_exist`, `replica_remove_required`, `replica_pinned`, `replica_read_only`, `replica_not_rebuildable`, `replica_not_streaming`, `replica_agent_offline`, `database_fenced`, `backup_replica_unsupported`, `mesh_server_required`, `mesh_confirmation_required`, `mesh_link_not_ready`, `primary_not_running`, `promote_in_progress`, `promote_confirmation_required`, `data_loss_not_accepted`, `agent_too_old`. The replication password is generated, sealed, delivered at send time only, and never appears in a payload, the audit log, the API or the logs. MCP has a read-only view (`get_database` shows replication); there is no tool to add, promote or rebuild.

## Limits

- **Manual only.** NebulaCtrl never promotes by itself, never detects that a primary is gone and has no witness or fencing device: it fences by stopping the container through the agent, which needs the agent to be reachable (or the host to stay down).
- **Asynchronous.** A forced Promote loses what the replica had not received. There is no synchronous option, no delayed replica, no cascading replica, no logical replication and **no point-in-time recovery**. Keep [backups](database-backups.md).
- **Postgres only.** No other engine, no reader proxy or pooler, and no replica outside the primary's environment.
- At most **5 replicas** per primary; a replica is pinned to its server.
- Replicas across servers depend on the [mesh](mesh.md): see its [failure modes](mesh.md#failure-modes) and what was **not verified** there (Tailscale SaaS, NAT and relay fallback across real networks, hosts with a firewall, large databases).
- Split-brain after a real power loss, large-database clone times and clones over a real WAN were not exercised.
