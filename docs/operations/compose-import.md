---
title: Import a Compose project
description: "Turn a docker-compose.yml into staged NebulaCtrl services after a review."
section: Applications
order: 80
---

# Import a Docker Compose project

**Import Docker Compose** has two modes. **Split into services** (this page) reads a `docker-compose.yml` once and stages one ordinary service per Compose service in an environment: the same services you could have created by hand, with normal deploy, rollback, variables, domains and logs; it is a one-time import, not Compose at run time. **Run as stack** keeps the file as it is and runs it with `docker compose` as one service: see [Compose stacks](compose-stacks.md). Nothing deploys until you have reviewed the result and press **Deploy** (or choose "Stage and deploy").

Where: **Create → Import Docker Compose** on the canvas, the CLI `nebulactrl import compose`, or the API (`POST /environments/{id}/compose-imports/plan` and `/compose-imports`). It needs `projects:write` (`deployments:write` for deploy-right-away); reading the file from a git repository also needs a git connection or a public GitHub / GitLab.com repository.

## What the wizard does

1. **Source.** Paste the file, upload it (plus its `.env` and any files it references), or point at a file in a repository: a connected git provider (private repositories; not Bitbucket, which has no file API here) or the https URL of a public repository on github.com or gitlab.com. From a repository the importer also reads `.env`, the `env_file` entries and the bind-mounted files next to the Compose file (at most 40 reads, 1 MiB each).
2. **Review.** The plan shows, per Compose service, what it becomes; every row can be changed. Nothing is written yet. The plan is recomputed from the same input plus your choices, so what you review is what is imported.
3. **Stage.** One transaction creates the services, volumes, files, variables, domains, links, published ports and a canvas group named after the Compose project, and writes one audit event, `compose.import`. Replica counts, shared networks and the repository link follow once it is committed; a failure there becomes a warning, never a rollback of the import.

## How things map

| Compose | NebulaCtrl |
|---|---|
| `image` | image service (the tag is kept; a digest is resolved when it is deployed) |
| `build` (context, dockerfile) | git source built with the repository's Dockerfile. Context and Dockerfile are made relative to the repository root (the Dockerfile path is relative to the context in Compose and to the repository here). The repository is the one you imported from, the `git://`/`https://` context URL (`url#branch:subdir`), or a repository you give in the review. Build `args` and `target` are not applied |
| `command` | start command (shell form is split into words like Compose does) |
| `entrypoint` | the service's **entrypoint** Docker option (replaces the image's entrypoint and its default command, like Compose; `command` is passed to it). An empty entrypoint that would clear the image's is not applied and flagged |
| `environment`, `env_file`, `.env` | variables. Interpolation (`$VAR`, `${VAR:-default}`, `${VAR:?error}`, `$$`) is done by the importer against the `.env` contents; unset variables become empty strings and are listed |
| secrets | a key that looks like a credential (`PASSWORD`, `TOKEN`, `SECRET`, `*_KEY`, `DSN`, ...), a URL with a password or a long random token is flagged as a **secret** (encrypted, never shown again). You confirm or change every flag in the review |
| `ports` | the first HTTP-like container port becomes the service port, with a generated domain suggested (on when Compose published it); Traefik `Host()` labels give explicit hostnames. Other published ports can become **TCP/UDP routes**, off by default, with the Compose host port as the public port. Known database and broker ports are treated as plain TCP (no domain) |
| `PORT` variable | sets the service port; `PORT` and `NEBULA_*` are platform variables and are not imported |
| named `volumes`, anonymous volumes | service volumes (created empty: existing data in your Compose volumes is not copied) |
| bind-mounted **file** (≤ 64 KiB, at most 16 files and 256 KiB) | a config file written into the container before it starts. `*.sh` files get mode 0755. Never put secrets in them: they are stored in plain text |
| bind-mounted **directory** or host path | **rejected** with guidance. Tick "Use a named volume instead" to create a volume at that path; the files in the host directory are not copied |
| Docker socket, `/proc`, `ipc`, `network_mode`, `pid` of another container | never applied; listed per service as danger notes. (`privileged`, `devices`, `pid: host` and the other host-level options are applied or reported, see [Docker options and the target server](#docker-options-and-the-target-server)) |
| `depends_on`, `links` | deploy order (links on the canvas). `condition: service_completed_successfully` makes the dependency a **one-shot** service that must finish first |
| `healthcheck` | an exec health check running the Compose `test` inside the container (interval capped at 10 s, deadline from `start_period` and `retries`); `disable`/`NONE` turn it off. Without one, a service with a port gets a TCP check (not HTTP, so a 404 on `/` cannot block the deploy) |
| `deploy.resources.limits`, `mem_limit`, `cpus` | memory and CPU limits |
| `deploy.replicas`, `scale` | replicas, applied after the import and only for stateless services (no volumes, with a port; at most 10) |
| `stop_grace_period` | stop grace period |
| `cap_add`, `cap_drop`, `security_opt`, `read_only`, `pids_limit`, `hostname`, `dns`, `sysctls`, `user`, `group_add`, `devices`, `gpus` and `deploy.resources.reservations.devices`, `runtime`, `privileged`, `pid: host`, `ulimits`, `shm_size`, `extra_hosts`, `init`, `stop_signal`, `tmpfs` | [Docker options](docker-options.md), judged against the target server: see [below](#docker-options-and-the-target-server). Each value is checked on its own; a refused one is listed with the reason and never takes the others down |
| `networks` | the environment network; every service of the environment reaches the others by name. Several Compose networks are merged (a warning says so). An `external` network attaches to the team's [shared network](networking.md) of the same name when there is one |
| `labels` | ignored, except Traefik `Host()` rules and the load-balancer port |
| `profiles` | services behind a profile are not imported unless you switch them on |
| `extends` (same file), YAML anchors and merge keys | resolved. `extends` from another file and `include:` are not followed |
| `restart`, `container_name`, `logging`, `pull_policy`, `platform`, `tty`, ... | ignored (the platform decides) |
| anything else (`working_dir`, `secrets`, `configs`, `dns_search`, `userns_mode`, `uts`, ...) | listed per service as not applied |

## Docker options and the target server

Compose keys that set container options become the service's typed [Docker options](docker-options.md). Two tiers apply, the same ones the settings page and the stack allow-list use:

| Tier | Compose keys | On a restricted server | On an unrestricted server |
|---|---|---|---|
| every server | `cap_add` (safe list), `cap_drop`, `security_opt: no-new-privileges`, `read_only`, `pids_limit`, `hostname`, `dns` (at most 3 IPs), `sysctls` (curated list only), `user`, `group_add` (numeric IDs), `ulimits`, `shm_size`, `extra_hosts`, `init`, `stop_signal`, `tmpfs`, `entrypoint` | applied | applied |
| unrestricted servers only | `devices`, `gpus` and `deploy.resources.reservations.devices` with `capabilities: [gpu]` (the NVIDIA driver; `count`, extra capabilities such as `compute`), `runtime` (other than `runc`), `privileged`, `pid: host`, `security_opt: seccomp=unconfined` / `apparmor=unconfined`, `cap_add: NET_ADMIN` / `SYS_ADMIN` | **not applied**, listed as "Needs an unrestricted server" in the review | applied, with a warning note per option so you review it |

The target is the server chosen in the review. Without a server (or while the choice changes) the plan assumes a restricted one; switching the server recomputes it. An instance admin turns a server unrestricted under **Servers → the server → Settings → Danger zone → Unrestricted**; importing again then applies the options, or you can set them in the service settings afterwards. Nothing is dropped silently: whatever is not applied is listed per service with its reason.

Never applied, on any server, and listed as not applied:

- `devices` given as CDI names (`vendor.com/gpu=all`) or outside `/dev`; a GPU `device_ids` selection (use a count); any device reservation without the `gpu` capability; a GPU driver other than `nvidia`; a second GPU request (the first one is used)
- `security_opt` seccomp or AppArmor profiles, SELinux labels and `systempaths`
- `sysctls` outside the curated list (for example `vm.max_map_count`: it changes the host and cannot be set from a container; declare it as a host requirement in the service settings instead)
- `group_add` names (`video`), `hostname` values that are not one DNS label (`mail.example.com`), `dns` entries that are not IP addresses, `user` values that are not `uid[:gid]`
- `pid: service:...` and `pid: container:...`
- `read_only: true` when the service has config files that are not on a volume (a read-only root filesystem cannot take them): put the file on a volume or turn it off
- `cap_drop` of a capability that is also in `cap_add`

`runtime: runc` is Docker's default and needs nothing. A managed database does not take container options; the review says so.

## References between services

Variables that point at another Compose service by name are rewritten to references so they keep working and show up as edges on the canvas:

- `DB_HOST=postgres` → `DB_HOST=${{postgres.NEBULA_PRIVATE_HOST}}`
- `http://api:3000/v1` → `http://${{api.NEBULA_PRIVATE_HOST}}:3000/v1`
- `redis://cache:6379/1`, `host=db port=5432`, `db:5432`, and a whole-value host name in a host-like variable (`*_HOST`, `*_URL`, `*_ADDR`, `*_SERVER`, ...).

Only unambiguous positions are rewritten. A bare word is never treated as a host in a variable that is not host-like (`POSTGRES_DB=app` stays), an e-mail address is not a host, a longer name (`db.example.com`) is not the service `db`, and a service never references itself. Every rewrite is listed in the review with its before and after (passwords masked); in the variables table you can keep a value as it was ("Keep original host names"). A reference also becomes a deploy-order link, unless it would close a cycle. The services' slugs are what references use; if a slug already exists in the environment the new service gets a numbered one and the references follow it.

A literal `${{` in a Compose value is escaped so it can never become a reference or a generator.

## Managed databases

An **official** database image (postgres, mysql, mariadb, redis, valkey, mongo, clickhouse; plain version tags, optionally `-alpine`, `-bookworm`, ...) with a version the [managed catalog](databases.md) offers can become a managed database. It is **on by default** when eligible; switch it off per service to keep the image as it is. Redis and MongoDB (source-available licences) are off until you switch them on, which acknowledges the licence.

A service is not eligible when it overrides the command, mounts init scripts or other files, has volumes besides the data directory, sets variables the engine does not own, uses an unsupported version or image variant, or (MySQL/MariaDB) connects as `root`. The review says why.

When a service becomes managed:

- user and database name are kept (`POSTGRES_USER`, `POSTGRES_DB`, ...); the **password is generated** and the credential variables of the Compose service are not imported;
- the old password is replaced by `${{db.DATABASE_PASSWORD}}` (`REDIS_PASSWORD` for Valkey and Redis) in the places that use it as a password: the `user:password@` part of a URL, `password=` in a connection string, or the whole value of a password variable;
- a Redis URL without a password (`redis://cache:6379`) gains one: `redis://:${{cache.REDIS_PASSWORD}}@${{cache.NEBULA_PRIVATE_HOST}}:6379`; a variable that only holds the host needs `REDIS_PASSWORD` set by you (the review does not guess);
- the data volume is created empty. Restore a dump or a backup after the first deploy ([database backups](database-backups.md)).

## Secrets and privacy

The plan response never contains a secret value: secret variables come back as `value: ""` with `secret: true`. The pasted source (including `.env`) is processed in memory and not stored; only the resulting variables are, encrypted. The audit event records counts and host names, never values. Config files and non-secret variables are stored in plain text like everywhere else.

## Blockers

The import refuses to run (`422 import_blocked`, listing the reasons) while any of these holds, and the review shows them next to the cause:

- a required `${VAR:?message}` variable has no value;
- a service with a `build` has no repository to build from (give one, or switch it to an image);
- a referenced `env_file` or bind-mounted file was not supplied;
- a hostname is already used by another service, a TCP/UDP port is taken, or the server is revoked or draining.

## CLI

```sh
# review, change nothing
nebulactrl import compose ./docker-compose.yml --project shop --env production --plan

# stage on server box1 (reads .env and referenced files from next to the file)
nebulactrl import compose ./docker-compose.yml --project shop --env production --server box1 \
  --domain web=shop.example.com --no-managed cache --publish db=5432/tcp

# from a repository
nebulactrl import compose - --repo acme/shop --connection github-acme --branch main --path deploy/compose.yaml \
  --project shop --env production --server box1
```

Per-service decisions are `<service>=<value>` (`--include`, `--exclude`, `--managed`, `--no-managed`, `--git url#branch`, `--image`, `--domain host|auto|off`, `--port`, `--publish port/proto`, `--convert-bind /path`) and `<service>.<KEY>` for variables (`--secret`, `--not-secret`, `--skip-var`, `--no-rewrite`). `--file '<path as written>=<local file>'` supplies a referenced file explicitly. `--deploy` deploys right away. Everything supports `--json`.

`--as-stack` imports the file as one [Compose stack](compose-stacks.md) instead of splitting it (API: `mode: "stack"` on the plan and import requests; split stays the default). The plan lists the components, the suggested domains, the `.env` variables and the allow-list violations (a violation, or `build:`, blocks the import with `422 invalid_compose`; `--server` decides whether an unrestricted server lifts the restricted ones). In this mode `--secret`, `--not-secret` and `--skip-var` take the variable name (`<KEY>`), `--domain` and `--port` take the component name, and the flags that shape split services (`--include`, `--exclude`, `--managed`, `--no-managed`, `--publish`, `--git`, `--image`, `--convert-bind`, `--no-rewrite`) are refused.

```sh
nebulactrl import compose ./docker-compose.yml --project shop --env production --server box1 --as-stack --plan
```

## Limits

- It is an import, not a sync: changing the Compose file later does not update the services, and importing again creates new services (existing slugs get a numeric suffix).
- Volumes, bind-mounted directories and the data of existing containers are not migrated; shared named volumes become independent copies per service (a warning lists them).
- Services run on one server, on the environment network; Compose network separation is not kept.
- Build args and targets, `working_dir`, Compose secrets and configs and the other keys listed above are not applied.
- Only the first HTTP-like port gets a domain; other ports need an explicit TCP/UDP route.
- Reading from git uses the provider's file API (GitHub, GitLab, Gitea, Forgejo) or the public raw host (github.com, gitlab.com); there is no clone, so a file larger than 1 MiB or a repository path outside the Compose file's directory (`..`) cannot be read.
