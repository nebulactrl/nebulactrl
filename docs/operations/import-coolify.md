---
title: Import from Coolify
description: "Stage the projects, applications, databases, domains and variables of a Coolify v4 instance as NebulaCtrl services."
section: Applications
order: 81
---

# Import from Coolify

**Import from Coolify** reads a Coolify v4 instance through its REST API and stages the same projects, environments, applications and databases here. It is a **one-time migration helper**: everything lands as ordinary, *staged* services you review and deploy yourself, and nothing is deployed unless you ask. **Data is never copied** (databases, volumes); the plan prints the follow-up steps, and [Restore from a dump file](database-backups.md#restore-from-a-dump-file) copies a Postgres or MySQL database.

Where: **Create → Import from Coolify** on the canvas, the CLI `nebulactrl import coolify`, or the API (`POST /imports/coolify/plan` and `/imports/coolify/apply`). It needs `projects:write` on an unrestricted token or session (the import creates projects), plus `deployments:write` for `--deploy`. It is **not** available over MCP: a Coolify token and every variable value of another instance must not pass through a model.

## What you need in Coolify

1. **API access enabled** on the instance (Settings → Advanced → API access), reachable from the NebulaCtrl **control plane** (the control plane makes the requests, not your browser).
2. An **API token with the `read` and `read:sensitive` abilities** (Keys & Tokens → API tokens). `read` lists resources; `read:sensitive` is what makes Coolify return **variable values, custom Docker run options, pre/post-deployment commands and database passwords**. Without it the plan is blocked: Coolify answers with those fields hidden, and an import would silently drop them. The token owner must be a team admin or owner. ([Coolify: API permissions](https://coolify.io/docs/api/permissions), [authorization](https://coolify.io/docs/api/authorization).)

Use a separate token and revoke it after the migration. The token is sent in the request body to your NebulaCtrl server, used for that single request, and **never stored, logged, audited or returned**; the plan carries no variable values.

The control plane follows no redirects (so the token cannot be forwarded to another host) and refuses link-local addresses (cloud metadata); private LAN and tailnet addresses are allowed, because that is where Coolify usually lives. Errors never repeat Coolify's response body.

## The flow

1. **Connect / plan.** `nebulactrl import coolify --url https://coolify.example.com --plan` (the token comes from `$COOLIFY_TOKEN`, `--token-file`, or `--token`). The plan lists every resource, its variables **by key** (never values), the references that were rewritten, and everything that is not imported. Nothing is written.
2. **Decide.** Flags (or the wizard's controls) exclude resources, rename projects and services, choose an existing project, override ports and domains, flip secret flags, supply locked values and registry credentials. The plan is recomputed from Coolify on every call, so what you review is what is imported.
3. **Stage.** `nebulactrl import coolify --url … --server <server>` creates, in one transaction: the projects and environments, services, managed databases, volumes, variables, domains, deploy-order links, and one audit event `coolify.import` (counts only). Git links follow once it is committed; a failure there is a warning, never a rollback.
4. **Review and deploy** from the canvas (staged bar) or `nebulactrl services deploy <service> --wait`. `--deploy` deploys everything right away, one environment at a time.

```sh
export COOLIFY_TOKEN=…               # read + read:sensitive
export GHCR_TOKEN=…                  # a pull credential for ghcr.io (Coolify's own login cannot be exported)

# 1. review
nebulactrl import coolify --url https://coolify.example.com --plan

# 2. stage (nothing runs yet)
nebulactrl import coolify --url https://coolify.example.com --server server-1 \
  --registry-user ghcr.io=ci-bot --registry-token-env ghcr.io=GHCR_TOKEN \
  --git-connection github
```

Re-running is safe: a project whose name exists gets a ` (Coolify)` suffix (or use `--rename-project` / `--into`), a service slug that is taken becomes `name-2`, and a hostname already used by a NebulaCtrl service is left out with a warning. Existing services are never changed.

## How things map

### Projects and environments

A Coolify project becomes a NebulaCtrl project and each of its environments an environment (`production` / `prod` is flagged **production**). If a project of that name exists, the new one is named `<name> (Coolify)`; override with `--rename-project <coolify>=<name>`, merge with `--into <coolify project>=<project>` (environments with the same name are reused, services are only added), or leave it out with `--skip-project`.

### Applications

| Coolify | NebulaCtrl |
|---|---|
| Docker image (`dockerimage`, or an image name without a repository) | **image service** with the image and tag. A missing tag becomes `latest` with a warning; `sha256:` tags become a digest. Registry credentials are not readable from Coolify: the plan lists every registry host that needs one and the services that use it; give `--registry-user host=user` and `--registry-token-env host=VAR` (stored per service, write-only, sent at deploy time like any registry credential) |
| Dockerfile (`dockerfile`) with a git repository | **git service**, build method *Dockerfile*. Coolify's *Dockerfile location* is relative to its *base directory*; here the path is relative to the repository root, so `base /apps/web` + `/Dockerfile` becomes Dockerfile path `apps/web/Dockerfile`, build context `apps/web` |
| Nixpacks, Railpack, static | **git service**, build method **Railpack** (Nixpacks' successor) with a warning. Coolify's install, build and start commands (and the publish directory of a static site) become the build variables `RAILPACK_INSTALL_CMD`, `RAILPACK_BUILD_CMD`, `RAILPACK_START_CMD` (`RAILPACK_SPA_OUTPUT_DIR`): check the build preview before you deploy. `NIXPACKS_*` variables are not read by Railpack |
| Docker Compose application, or a Dockerfile stored inside Coolify | **not imported**, listed with the reason (import the Compose file with `nebulactrl import compose --as-stack`; commit an inline Dockerfile to a repository) |
| Git repository | the clone URL comes from Coolify (`owner/name` becomes `https://github.com/owner/name`). The branch is kept; a commit Coolify pinned is reported (NebulaCtrl builds the head of the branch). Private repositories: Coolify's GitHub App and deploy keys **cannot be exported**; select a git connection (`--git-connection`) so GitHub/GitLab repositories are linked, or add an SSH deploy key in the service's Source settings |
| `ports_exposes` | the first port is the service **port** (the rest are noted). An application with **no domain, no health check, no published port and no other service pointing at it** becomes a **worker** without a port, with a note; override with `--port <app>=<n>` / `--kind <app>=app` |
| `ports_mappings` (published host ports) | **not imported**: add a TCP/UDP route for the port |
| `fqdn` (every domain) | one **domain** per entry. `http://` entries keep plain HTTP allowed; an explicit `:port` is the target port; a path becomes a path prefix; `noindex_domains` become the *noindex* option; `gzip`/`compress` in the Traefik labels turns on *compression*; auto-generated `sslip.io`/`nip.io`/`traefik.me` hosts become a generated NebulaCtrl hostname. Invalid or already-used hosts are left out with a note. Redirects (www), basic auth, IP allow lists and other Traefik **labels are not imported**: they are listed, and set per domain under Domains → Rules |
| health check | enabled HTTP check → an **exec** check that runs Coolify's own request inside the container: `curl` (else `wget`) against `<scheme>://<health_check_host, default localhost>:<port><path>`, with the interval, timeout and start period; deadline = start period + retries × interval. Coolify ran its checks inside the container too, so the image has `curl` or `wget`, and frameworks with a host allow list (Laravel trusted hosts, Django `ALLOWED_HOSTS`, Rails host authorization) accept `localhost` where they refuse a probe from outside addressed to the container IP. A command check → an exec check. Disabled → **none** (the release counts as healthy once it ran 10 s without restarting), or the image's own `HEALTHCHECK` when Coolify found one. Methods other than GET and response-text checks are not supported and are noted; like Coolify's own check, any non-error status passes |
| `limits_memory`, `limits_cpus` | memory (MiB) and CPU limits. Swap, reservation, CPU set and CPU shares are not supported and listed |
| pre-/post-deployment command | the service's **pre-deploy** / **post-deploy** command. Coolify ran them with `sh -c`; a command with shell syntax keeps it by running under `sh -c`, a plain command becomes an argv. The pre-deploy command runs once per deployment in a one-shot container of the new image before the old release is touched |
| persistent storages | **named volumes** (the app-uuid prefix is removed). The data is **not** copied; host bind mounts and file storages are listed as not imported |
| scheduled tasks | listed as not imported (add them under Tasks → Cron) |
| preview-only variables | not imported |

### Custom Docker run options (`custom_docker_run_options`)

The option string is split with a real shell-word parser (quotes, escapes, line continuations; nothing is expanded or executed) and mapped to the typed [Docker options](docker-options.md). A quoted `--entrypoint "docker-entrypoint.sh node server.js"` becomes the exec-form argv `["docker-entrypoint.sh","node","server.js"]` (Coolify splits it the same way); an entrypoint replaces the image's default command, so the start command stays empty.

| Applied | Needs an **unrestricted server** (flagged; applied only when the target server is unrestricted) | Listed as not imported |
|---|---|---|
| `--entrypoint`, `--cap-add`, `--cap-drop`, `--security-opt no-new-privileges`, `--shm-size`, `--ulimit nofile/memlock`, `--add-host`, `--init`, `--tmpfs`, `--hostname`, `--dns`, `--sysctl` (curated list), `--user`, `--group-add`, `--pids-limit`, `--read-only`, `--stop-signal`; `--memory` / `--cpus` when Coolify has no limit of its own | `--privileged`, `--device`, `--gpus`, `--runtime`, `--pid host`, `--security-opt seccomp=unconfined` / `apparmor=unconfined`, `--cap-add NET_ADMIN` / `SYS_ADMIN` | `--network`, `--publish`, `--volume`, `--env`, `--label`, `--restart`, `--ip`, `--cpuset-cpus`, health flags, other ulimits, `-it` and everything the parser does not recognise |

Each option is validated on its own, so one refusal never drops the others. Whatever is not applied is listed per service with the reason; options that need an unrestricted server say so ("An instance admin has to switch the target server to Unrestricted… import again, or set it in the service settings"). Pick the target server first so the plan shows what it accepts.

### Databases

Each database becomes a [managed database](databases.md) with the **same engine** and, where the catalog offers it, the same version and flavor. Coolify's image decides the engine (a `redis` database running `valkey/valkey:8` is Valkey):

| Coolify image | Managed |
|---|---|
| `postgres:<v>` | PostgreSQL `<v>` (Alpine variants become the Debian image, with a note) |
| `postgis/postgis:<v>-<x>` | PostgreSQL with the **PostGIS** flavor (for example `postgis/postgis:18-3.6` stays 18 / PostGIS 3.6; a different PostGIS minor is noted) |
| `mysql:<v>`, `mariadb:<v>` | MySQL / MariaDB: the same version, else the same major, else the next offered (MySQL 8.0 → 8.4 LTS) |
| `valkey/valkey`, `redis`, `eqalpha/keydb`, `dragonflydb/dragonfly` | Valkey (Redis → Redis 8; KeyDB and Dragonfly → Valkey, Redis-compatible) |
| `mongo`, `clickhouse/clickhouse-server` | MongoDB, ClickHouse |
| SQLite, `timescale/…`, `supabase/postgres`, `bitnami/…`, any other image | **not imported**, with the reason; run them as an image service with the same image |

The user and database name are kept; **a new password is generated** and the old one is not imported. A version older than the catalog's oldest is moved to the next offered one (a logical dump restores into a newer version); newer than offered is not imported. Redis and MongoDB have source-available licences and are **off by default**: `--include <database>` is the acceptance. Public ports, custom config files, init scripts, SSL mode, Docker options and resource limits are listed as not carried over. **The data is not copied**; the plan prints, per database, the follow-up, for example:

```sh
# on the Coolify host
docker exec <coolify database uuid> pg_dump -U shop -Fc shop > shop-postgres.dump
# against NebulaCtrl
nebulactrl databases restore shop-postgres --file shop-postgres.dump
```

Key-value databases start empty (cache, queue and session data can usually be rebuilt; drain the queues first). MongoDB and ClickHouse need `mongodump`/`mongorestore` or the engine's own tools.

### Variables

Every variable is imported with its value, except those NebulaCtrl owns or Coolify injects: `PORT` and `NEBULA_*` (platform variables), `COOLIFY_*`, `SERVICE_FQDN_*`, `SERVICE_URL_*`, `SOURCE_COMMIT`, and preview-only variables. Shared-variable references (`{{environment.X}}`) are inlined with the value Coolify resolves, with a note. A literal `${{` in a value is escaped so it stays literal.

- **Secrets.** A variable is a secret when Coolify locked it (*shown once*), when its name looks like a credential (`*_KEY`, `*SECRET*`, `*PASSWORD*`, `*TOKEN*`, `*_DSN`, `API_KEY`, `*_KEK*`, …; public keys and `*_KEY_ID` are exempt), when its value is a URL with a password, or when it became a reference to a database password. The plan says why (`secretReason`) and you can flip any flag (`--secret`, `--not-secret`).
- **Locked variables.** Coolify never returns the value of a locked variable. They are listed with `?` and **not imported** until you supply the value with `--set-env <app>.<KEY>=<ENV_VAR>` (or `--set`, or in the wizard), or set them on the service afterwards. A supplied value is stored as typed, literally: a `${{…}}` in it stays text. To point a variable at another service (`${{db.DATABASE_URL}}`), set it after the import with `nebulactrl vars set`.
- **Build-time flags.** *Available at build time* maps to the variable scope (`build`, or `both` together with runtime). Build scope exists for **git** services; on image services the variable stays a runtime variable, with a note. Secret build values reach the build as BuildKit secrets (Docker 23+ on the server).
- **References.** A value that points at a Coolify database or application **in the same environment** is rewritten to a NebulaCtrl reference, so it keeps working after the services get new names and passwords:

| In the Coolify value | Becomes |
|---|---|
| the database's internal hostname (its UUID), or an application's UUID / network alias | `${{db.NEBULA_PRIVATE_HOST}}` |
| the database password (whole value, in a URL's user info, or a long password inside text) | `${{db.DATABASE_PASSWORD}}` (`REDIS_PASSWORD` for Redis/Valkey) |
| the exact connection URL Coolify shows for the database | `${{db.DATABASE_URL}}` (`REDIS_URL`, `MONGO_URL`, `CLICKHOUSE_URL`) |
| user, database name or port as the whole value of a fitting key (`*_USER(NAME)`, `*_DB`/`*_DATABASE`/`*_NAME`, `*_PORT`), only for a database the service demonstrably uses | `${{db.DATABASE_USER}}`, `${{db.DATABASE_NAME}}`, `${{db.DATABASE_PORT}}` (`REDIS_PORT` for Redis/Valkey) |
| `http://<ssr-uuid>:3000` | `http://${{ssr.NEBULA_PRIVATE_HOST}}:3000` |

Other text around the match is kept (`redis://default:${{cache.REDIS_PASSWORD}}@${{cache.NEBULA_PRIVATE_HOST}}:6379/1`). A reference to a resource that is **not imported**, or lives in **another environment**, is left as it is and flagged. `--no-rewrite <app>.<KEY>` keeps one value verbatim. Services that reference each other get a **deploy-order link**, so databases deploy first. The rewritten references are listed in the plan (key and new reference; never the old value).

### Coolify one-click services

A Coolify "service" is a Docker Compose stack from a template. It is **not imported automatically**; the plan lists each one (with its project) and the next step: save its Compose file from Coolify and run `nebulactrl import compose <file> --as-stack`, or install the matching [template](templates.md).

## What stays manual

Registry credentials (unless you pass them), private-repository access, the **data** of databases and volumes, DNS (still pointing at Coolify until you move it), Cloudflare/Traefik middlewares and dynamic route files (per-domain access rules, redirects, the external routes such as a Plex upstream: use a route service), scheduled tasks, port mappings, file mounts, locked variable values, backups and notifications.

## CLI reference

```
nebulactrl import coolify --url <coolify url> [--server <server>] [flags]

--url, --token-env (default COOLIFY_TOKEN), --token-file, --token    connection (token is never printed)
--plan | --dry-run                       review only; exit code 1 when the plan has blockers
--deploy                                 deploy right away (default: staged)
--server                                 required to import
--git-connection <id|name>               link GitHub/GitLab repositories for builds
--skip-project, --rename-project, --into project choices
--exclude, --include, --rename, --port, --kind, --engine-version   per application or database
--domain <app>/<host>=<new host>|off     change or drop a domain
--secret, --not-secret, --skip-var, --no-rewrite <app>.<KEY>       per variable
--set-env <app>.<KEY>=<ENV_VAR>, --set <app>.<KEY>=<value>         values of locked variables
--registry-user <host>=<user>, --registry-token-env <host>=<ENV_VAR>
```

Resources are named by their Coolify name or UUID; ambiguous names are rejected with the candidates listed. `--json` prints the plan or the result (no values either way).

## Limits

- Coolify v4 only (API `/api/v1`, read from the `v4.x` OpenAPI); fields that are missing or typed differently in other versions are read leniently and degrade to a note.
- The importer reads, per resource, the application, its variables, storages and scheduled tasks, databases, one-click services and servers: a large instance takes a few seconds per resource.
- Coolify teams: a token sees one team; run the import once per team.
- Variables over 64 KiB and more than 500 variables per service are rejected by the variables API like any other write.

## Troubleshooting

| Message | Meaning |
|---|---|
| `Coolify refused the token` (`coolify_unauthorized`) | wrong or expired token, API access disabled on the instance, or Coolify's **Allowed IPs** (Settings → Advanced) exclude the control plane. On the same host the request arrives from the gateway of Coolify's Docker network (`docker network inspect coolify`, for example `10.0.1.1`): add that address for the import and remove it afterwards |
| `does not look like a Coolify v4 instance` | the URL is not the instance (use the address of the dashboard, not a proxied app) |
| `cannot reach Coolify at …` | the control plane cannot connect (firewall, tailnet, DNS); remember the control plane makes the request |
| `The Coolify token cannot read variable values` | the token lacks `read:sensitive` |
| `import_blocked` | the plan has blockers: run with `--plan` and read the `[blocked]` lines |
| a domain was "left out" | the hostname is already used by a NebulaCtrl service (a previous import) |
