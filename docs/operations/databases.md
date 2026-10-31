---
title: Managed databases
description: "Engines, connection variables, credential rotation and version upgrades."
section: Databases and backups
order: 10
---

# Managed databases

A managed database is an ordinary service (`kind=database`) with a typed `database` section in its configuration. NebulaCtrl derives everything engine-specific from that section: image, port, start command, TCP health check, the data volume and its mount path, and the credential variables. You pick an engine and a version; nothing else needs to be configured to get a working database.

Databases are **single-container** (one instance, no connection pooling; PostgreSQL alone has an [advanced standby replica](database-replicas.md) option that you promote by hand) and always use the `recreate` strategy: a configuration change restarts the database and causes a short outage, which the deployment plan says up front.

## Engines

| Engine | Versions | Image | Licence | Notes |
|---|---|---|---|---|
| PostgreSQL | 18, 17, 16 | `postgres` | PostgreSQL | `postgis` flavor: `postgis/postgis` (GPL-2.0 image). PostgreSQL 18 uses a new data-directory layout, so its volume mounts at `/var/lib/postgresql`. |
| MySQL | 8.4 LTS, 9.7 | `mysql` | GPL-2.0 | |
| MariaDB | 11.8 LTS, 12.3 | `mariadb` | GPL-2.0 | |
| Valkey | 9, 8 | `valkey/valkey` | BSD-3-Clause | **Default key-value engine.** |
| Redis | 8 | `redis` | RSALv2 / SSPLv1 / AGPLv3 | Selectable, with a licence label; creation needs `acceptLicense`. |
| MongoDB | 8.0, 7.0 | `mongo` | SSPL-1.0 | Opt-in, with a licence label; creation needs `acceptLicense`. |
| ClickHouse | 26.8 LTS, 25.8 LTS | `clickhouse/clickhouse-server` | Apache-2.0 | Native-protocol port 9000. Backups are not supported. |

Image tags are major-version tags; every release records the resolved digest, so a deployment is reproducible even though the tag floats between patch releases. Redeploying a database picks up the newest patch release of its major version.

The catalog is data (`internal/databases/engines`): a unit test checks that every engine has exactly one default version, a licence, and a licence label when the licence is source-available.

## Creating a database

Canvas → **Create → Database** → pick an engine and version (and flavor, server and database name). Via the API: `POST /environments/{environmentId}/databases`; via the CLI: `nebulactrl databases create`. Creation is **staged** like every other change: it creates the service, the data volume and the generated credentials, and nothing runs until you deploy.

Credentials are ordinary secret variables on the database service, generated once with `crypto/rand` (32 alphanumeric characters) using the engine's own names:

| Engine | Variables |
|---|---|
| PostgreSQL | `POSTGRES_USER`, `POSTGRES_PASSWORD`, `POSTGRES_DB` |
| MySQL / MariaDB | `MYSQL_*` / `MARIADB_*`: `ROOT_PASSWORD`, `USER`, `PASSWORD`, `DATABASE` |
| Valkey / Redis | `VALKEY_PASSWORD` / `REDIS_PASSWORD` (the server is started with `--requirepass`) |
| MongoDB | `MONGO_INITDB_ROOT_USERNAME`, `MONGO_INITDB_ROOT_PASSWORD`, `MONGO_INITDB_DATABASE` |
| ClickHouse | `CLICKHOUSE_USER`, `CLICKHOUSE_PASSWORD`, `CLICKHOUSE_DB` |

Secret values are never returned by any read. See *Reveal* below.

## Connecting other services

Databases export **derived connection variables**. They are computed from the engine and the credentials when a reference is resolved, are not stored, never injected into the database container and never produce a staged change:

| Engine | Exports |
|---|---|
| PostgreSQL | `DATABASE_URL`, `DATABASE_HOST`, `DATABASE_PORT`, `DATABASE_NAME`, `DATABASE_USER`, `DATABASE_PASSWORD`, `PGHOST`, `PGPORT`, `PGUSER`, `PGPASSWORD`, `PGDATABASE` |
| MySQL / MariaDB | `DATABASE_URL`, `MYSQL_URL`, `DATABASE_HOST`, … `DATABASE_PASSWORD` |
| Valkey | `REDIS_URL`, `VALKEY_URL`, `REDIS_HOST`, `REDIS_PORT`, `REDIS_PASSWORD` |
| Redis | `REDIS_URL`, `REDIS_HOST`, `REDIS_PORT`, `REDIS_PASSWORD` |
| MongoDB | `MONGO_URL`, `DATABASE_URL`, `DATABASE_HOST`, … |
| ClickHouse | `CLICKHOUSE_URL`, `DATABASE_URL`, `DATABASE_HOST`, … |

In a consumer, set `DATABASE_URL=${{pg.DATABASE_URL}}` (where `pg` is the database's slug). Exports that contain the password are derived secrets: masked everywhere and redacted from logs. Connecting an edge on the canvas proposes exactly this one reference. A service on the **same server** reaches the database by its private hostname. A service on **another server** of the same environment reaches it through a [mesh link](mesh.md#cross-server-service-links): the host is still the slug, and `DATABASE_PORT`, `PGPORT`, `DATABASE_URL` and the other port-bearing exports resolve to the mesh port **for that consumer** (the container port when that port is free on the database's server). The mesh must be enabled on both servers, and the link is added by deploying the consumer, which the plan states with the devices it adds. A PostgreSQL group with replicas also exports `DATABASE_WRITE_*` and `DATABASE_READ_*` ([writer and reader variables](database-replicas.md#writer-and-reader-variables)).

## Reveal a connection string

Secrets stay masked in the UI, API and MCP. To connect from your laptop (`psql`) there is one explicit action: **Copy connection string**, `POST /services/{id}/database/reveal {"key":"DATABASE_URL"}`.

- Requires the `deployments:write` scope (admin included); browser sessions hold every scope. There is no MCP tool for it.
- Audited in the same transaction as `secret.reveal` (service and key, never the value).
- The response is `Cache-Control: no-store` and carries a 60-second `expiresAt`; the UI shows it briefly and does not keep it.
- The reveal response is not an idempotent operation, so the value never lands in stored idempotency bodies.

The internal hostname is only resolvable on the server's private network (and through a mesh link for consumers of the same environment on other servers). To connect from outside, use a tunnel or the TCP routes of the proxy.

## Rotating credentials

The official database images read their password variable **only when they initialise an empty data directory**. Editing `POSTGRES_PASSWORD` on a running database changes the variable and nothing else, so the database and its clients would silently disagree. NebulaCtrl therefore refuses edits of credential variables on a deployed database (`credential_managed`) and offers **Rotate credentials** (`POST /services/{id}/database/rotate-credentials`, `dryRun` for the plan):

- **PostgreSQL, MySQL, MariaDB, MongoDB, ClickHouse (`exec`):** the agent runs a typed command inside the container (`ALTER ROLE` / `ALTER USER` / `changeUserPassword`). The new password reaches the command through its stdin and environment, never argv, never the command payload in the database, and is masked in results. When the agent reports success the variable is updated. **The old password stops working immediately**, so every service using it fails to connect until it is redeployed.
- **Valkey, Redis (`redeploy`):** the server takes its password from the environment on every start. The variable is changed and the database restaged; it restarts on the next deploy.
- **Never deployed:** the new password simply becomes the initial one.

Every service in the environment whose variables reference `${{<slug>.…}}` is **staged** (its generation advances), so the next *Deploy staged changes* redeploys the consumers, databases first. Nothing is deployed automatically. Services that copied the password as plain text are not found: update them by hand. Rotation requires `projects:write` and `deployments:write`, runs one at a time and is blocked while the database is deploying or upgrading.

If the agent is offline the command waits; if it is never delivered the rotation fails with *Not delivered: server offline. No changes were made.*

## Version upgrades

`POST /services/{id}/database/upgrade/plan {"targetVersion":"18"}` returns a plan: method, steps, warnings, blockers and expected downtime. **Take a backup first** ([database backups](database-backups.md)). Downgrades are blocked.

| Engine | Method | What happens |
|---|---|---|
| Valkey, Redis, MySQL, MariaDB, ClickHouse | `redeploy` | The image changes as a staged change; the next deploy upgrades the data files on start. Cannot be reverted without a backup. |
| PostgreSQL | `pg_upgrade` | The database is **stopped**. The agent runs [`tianon/postgres-upgrade`](https://github.com/tianon/docker-postgres-upgrade) (pinned per version pair, e.g. `17-to-18`) with the old volume as the source and a **new volume** as the target, then the service switches to the new volume and image as a staged change. **The old volume is kept untouched** on the server (delete it yourself when you are satisfied). Deploy the staged change to start the new version, then run `vacuumdb --all --analyze-in-stages`. If the conversion fails the old database is started again and nothing changed. |
| PostgreSQL PostGIS, MongoDB | `dump_restore` | Not automatic: restore a backup into a new database at the target version, switch the references, delete the old database. |

A never-deployed database just changes image. `pg_upgrade` copies the data (hard links are not used), so the host needs free disk space for a second copy. The helper needs the checksum setting of the old cluster; the agent retries once with the opposite `initdb` flag when `pg_upgrade` reports a mismatch (PostgreSQL 18 enables data checksums by default, earlier versions do not).

A PostgreSQL primary or replica **cannot be upgraded while the group has [read replicas](database-replicas.md#interactions)** (`replicas_exist`): `pg_upgrade` carries neither the replication block of `pg_hba.conf` nor the replication slots. Remove the replicas, upgrade, and add them again. Scheduled and manual [backups](database-backups.md) run on the primary only.

`tianon/postgres-upgrade` is a third-party image from a personal Docker Hub namespace; its README calls itself a proof of concept. The upgrade path is covered by unit tests and was exercised by hand against a PostgreSQL 17 volume converted to 18.

## Observing a database

The database inspector's **Overview** shows the engine and version, its connection exports (names only) and, on **Refresh**, the version, database size and open connections that the agent reads with a read-only query inside the container. These are reported asynchronously (`POST /services/{id}/database/refresh`, then `GET /services/{id}/database`).

## Public port

A database can be published on a public port of its server, for a client that is not on the server (a laptop, a BI tool, a hosted job). It is off by default and always restricted by an IP allow-list.

| | |
|---|---|
| Where | the database's **Networking** settings: *Expose on a public port*; API `PUT /services/{id}/database/public-port` (`GET` reads it, `DELETE` closes it) |
| What it creates | one TCP route service (see [proxy routes](proxy-routes.md)) on the database's server, marked as the database's public port. It shows on the canvas like other route services and is **staged**: deploy it to apply, like any change |
| Allow-list | **mandatory** (`422 allowlist_required`): CIDRs or single addresses. `0.0.0.0/0` or `::/0` is accepted only with `confirmOpen` set to the database slug (the UI asks you to type it, `422 open_confirmation_required`) |
| Deny list | optional, evaluated first, so it wins even over a confirmed `0.0.0.0/0` ([IP rules](proxy-routes.md#ip-rules-allow-and-deny)) |
| Port | 1024-65535, never `80`, `443` or a proxy port; the UI suggests the engine's own port (5432 …) and moves to another one when it is taken (`409 port_in_use`) |
| Scopes | `projects:write` and `proxy:write` to change it, `projects:read` to read it |

**A new public port restarts the proxy on that server.** The proxy publishes each TCP port as a static entrypoint, so the agent recreates the Traefik container when the set of listening ports changes: about a second of dropped connections, and HTTP is briefly unavailable. The UI and the deployment plan warn about it, and `newEntrypoint` in the API says whether it applies. Changing only the allow or deny list of a port that is already published restarts nothing.

**TLS.** The proxy forwards raw TCP and never terminates TLS on this port, so a database that terminates TLS itself keeps doing so end to end. The managed databases do **not** enable TLS on their own, so by default the connection, password included, is not encrypted. Keep the allow-list to networks you trust, or tunnel the connection through a VPN or a [per-service tailnet](service-tailnet.md).

**Docker bypasses host firewalls** (ufw, firewalld): the allow-list is the control, not a host firewall rule ([proxy routes](proxy-routes.md#security-notes-for-tcpudp)). The proxy sees the client address Docker hands it, so a server behind another NAT or proxy sees that device's address, not the real client's.

**The external connection string** uses the server's **public IP** (set it under the server's Settings → General; `422 no_public_address` otherwise) and the public port. Like the private exports it is only revealed on request: `POST /services/{id}/database/public-port/connection` (`deployments:write`), uncached, shown for 60 seconds and recorded in the audit log as `secret.reveal` with `public: true` (never the value).

## Advanced

- **[Standby replicas](database-replicas.md)** (PostgreSQL only, advanced). A copy of the database that streams to another server and can answer read-only queries, so you can promote it by hand if the first server is lost. It is not a backup, and nothing switches over by itself, so keep your scheduled backups. On the same server it only helps with read load. In the database panel it sits in a collapsed **Advanced** section.

## Limits

- One container per database and no connection pooling. PostgreSQL can have up to 5 asynchronous [standby replicas](database-replicas.md) that you promote by hand; the other engines have none.
- A public port is a TCP route with an IP allow-list, not a managed TLS endpoint; there is no per-engine TLS setup (see [Public port](#public-port)). Closing it (`DELETE`, or removing the route) closes the listener on the server; the proxy is recreated only when its set of ports changes.
- Settings (`postgresql.conf`, `my.cnf`), init scripts, TLS and extension management are not exposed. Choose the PostGIS flavor for geospatial data.
- ClickHouse and MongoDB are lightly exercised; the integration tests cover PostgreSQL and Valkey.
