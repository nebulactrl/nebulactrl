---
title: Compose stacks
description: "Run a docker-compose.yml as one service with the safe allow-list."
section: Applications
order: 85
---

# Compose stacks

A **stack** runs a Docker Compose file as it is. It is one service on the canvas: you paste or upload a `docker-compose.yml`, and the agent runs `docker compose up` for it on the server you choose. The services in the file are the stack's **components**.

Use a stack when you have a Compose file you do not want to take apart (init containers, `depends_on` conditions, sidecars that share a network namespace, a vendor's reference file). Use [Import a Docker Compose project](compose-import.md) with **Split into services** when you want each part to be its own service with its own domains, variables, replicas, backups and rollbacks.

Where: **Create → Compose stack** on the canvas, **Create → Import Docker Compose → Run as stack**, or the API (`kind: "stack"`, `sourceType: "compose"`, `config.stack.compose`). Decision record: [ADR 0007](../decisions/0007-compose-stacks.md).

## What a stack is

- One service: **one deploy, one release, one rollback, one set of staged changes.** Editing the compose file, the variables, the domains or the limits stages a change; **Deploy** applies it to the whole stack.
- Components are not services. They have no variables, domains or settings of their own; the stack's variables are shared by the whole file, and a domain points at one component and one container port.
- The compose project is named `nc-<first 8 characters of the service ID>`. Containers are named `nc-<id8>-<component>` and carry the usual `nebulactrl.*` labels plus `nebulactrl.component`. Named volumes are `nc-<id8>_<volume>`.
- **Recreate only.** `docker compose up` replaces changed containers in place, so a deploy is not zero-downtime. Components restart on every deploy (the release's deployment ID is part of their labels).
- Requires the **Docker Compose plugin 2.24 or newer** on the server. The server page shows the version it found (Settings → Danger zone); without it a stack cannot be deployed there (`compose_unavailable`). The installer installs the plugin; on an existing host: `apt install docker-compose-plugin` (Debian/Ubuntu) or `dnf install docker-compose-plugin` (RHEL family), then restart the agent.

A stack runs images only. For a git app with workers or a scheduler use [processes](processes.md) instead.

## Variables

The stack's variables (including project and environment shared variables, references and secrets) are used for **Compose interpolation**: write `${DB_PASSWORD}` in the file and set `DB_PASSWORD` in the Variables tab. A variable is only given to a component when the file passes it on (`environment: [DB_PASSWORD]` or `DB_PASSWORD: ${DB_PASSWORD}`), exactly as with `docker compose --env-file`.

- Values are sent to the agent with the deploy command and handed to `docker compose` in a **clean environment**: the agent's own environment (its enrollment token, its Docker settings) never reaches the file. Variables that would change how the root `docker compose` client runs are dropped, so `${...}` placeholders with these names stay empty: `DOCKER_*`, `COMPOSE_*` (except `COMPOSE_PROFILES`), `BUILDKIT_*`, `BUILDX_*`, loader and interpreter injection (`LD_*`, `DYLD_*`, `NODE_OPTIONS`, `PYTHONPATH`, …), `PATH` and `HOME`.
- Nothing is written to disk by NebulaCtrl except the compose file itself (with the `${...}` placeholders) and the generated override (no values). Docker stores what a container was created with, as for every container.
- Secret values are masked in deployment logs.
- `env_file:` is not supported (a stack has no files next to the compose file): put the values in the stack's variables.

## What the agent changes: the override

The agent never edits your file. It generates a second file, the **override**, and runs `docker compose -f compose.yaml -f override.yaml`. The override:

| Does | Why |
|---|---|
| adds the managed labels (`nebulactrl.managed`, `.service`, `.deployment`, `.release`, `.environment`, `.component`, `.replica`) | status, logs, metrics, the orphan sweep and routing find the containers |
| sets `container_name: nc-<id8>-<component>` | stable names; also DNS names for Traefik |
| joins every component to the environment network with the component name as alias (and to the environment's internal network or shared networks like any service) | other services of the environment reach `web` by name; [network isolation](networking.md) is unchanged. The project's own default network stays, so components still reach each other |
| removes `ports:` (host publishing) | nothing is bound on the host; HTTP is routed by Traefik through a domain |
| applies the per-component memory and CPU limits you set in the stack's settings (they replace `mem_limit`, `cpus` and `deploy.resources.limits`) | resource control from the UI |
| sets `restart: unless-stopped` for components that set no `restart` | like every service |
| pins images to the release's digests for a rollback or a reconcile redeploy | a rollback runs the images that were running, even for `:latest` tags |

If **Block outbound internet** is on for the stack, the components join only the environment's internal network (the compose file's own networks are replaced).

## Routing

A stack has no `Port`. Add domains like for any service, but pick the **component** and the **container port**; any port from 1 to 65535 is accepted, and the ports the file declares (`ports:` targets and `expose:`) are offered. Traefik routes to `nc-<id8>-<component>:<port>` over the environment network, with all the usual domain options (redirects, path prefixes, basic auth, IP rules, TLS).

- `ports:` entries are **never published on the host**, whatever the file says. A published TCP/UDP port (a database, a game server) is therefore not reachable from outside; keep such components internal, or use the [public database port](databases.md) of a managed database. HTTP-like ports become domains.
- After the containers are healthy the agent switches the routes and **verifies through the proxy** (`GET /` through the first domain of the stack must answer below 500). A failed verification restores the previous routes and the previous stack.
- Maintenance mode and traffic analytics are not available for stacks.

## Health, deploy and rollback

- Health is Compose's: the agent runs `docker compose up -d --wait` and waits for every component with a `healthcheck` to be healthy and every other component to be running (`depends_on` conditions work). The wait is bounded by **Wait timeout** (default 300 seconds, at most 3600). A component that exits with code 0 (an init or migration container) counts as healthy ("completed").
- Steps in the deployment view: **pull** (validate the file against the allow-list, normalise it, pull images), **create/start** (`up`), **health**, **route**, **verify**.
- **A failed `up`, health check or verification restores the previous release** (the previous compose file and override are re-applied) and the deployment ends `rolled_back`; if that fails too it is `rollback_failed` and the message says how to recover. The first deployment has nothing to restore: its containers are removed, volumes are kept, and the deployment is `failed`. The last 50 log lines of unhealthy components are attached.
- **Rollback** to an earlier release re-applies that release's compose file with the image digests recorded for it. It never touches data: volumes, databases and external services are not rolled back (the dialog says so).
- **Cancel** works at any step (see [Deployments](deployments.md)).
- **Restart** runs `docker compose restart` for the whole stack, **Stop** stops every component (containers are kept), **Delete** runs `docker compose down --remove-orphans`; **Delete volumes** adds `--volumes`.

## What is checked: the allow-list

Docker is root on the host, so a compose file can ask for the host itself. By default NebulaCtrl refuses a file that does. The agent normalises the file with `docker compose config --format json` (anchors, `extends`, short syntaxes and variables resolved) and checks that model; the control plane checks the file as you wrote it, so mistakes show up as you edit and on save. A violation blocks the deploy (`422 invalid_compose`, with the list).

Refused unless the server is **unrestricted** (below):

- `privileged: true`, and privileged lifecycle hooks
- `network_mode` other than `none` or `service:<another service of the stack>` (so no `host`, `bridge`, `container:`); `pid`, `ipc`, `uts`, `userns_mode`, `cgroup` that name the host or a container outside the stack
- `devices`, `device_cgroup_rules`, `gpus`, GPU reservations, `runtime` other than `runc`, `cgroup_parent`, `storage_opt`, `isolation`, block-device limits
- `cap_add` beyond the [safe list](docker-options.md#capabilities), including `NET_ADMIN` and `SYS_ADMIN` (they need an unrestricted server like everything else here; there is no instance-wide switch for them)
- `security_opt` other than `no-new-privileges`: `seccomp=unconfined` and `apparmor=unconfined` need an unrestricted server like the typed [Docker options](docker-options.md); profiles and labels are refused too
- `sysctls` outside the curated list of [namespaced container sysctls](docker-options.md) (the same list plain services may set; host-wide parameters such as `vm.max_map_count` are refused)
- **host bind mounts**, in short or long syntax, relative or absolute: this includes the Docker socket (`/var/run/docker.sock`). Named volumes (declared in the file), tmpfs and anonymous volumes are fine; `volumes_from: container:...` is not
- external volumes, custom volume or network `name:`s, volume `driver_opts` (`device`/`type: none`/`o: bind` mount host paths), non-local volume drivers: they could reach another project's data
- external networks, other network drivers (`host`, `macvlan`, `overlay`), network `driver_opts`: they would bypass the environment isolation
- `file:` secrets and configs, external secrets and configs (use `content:` or `environment:`)
- labels starting with `com.docker.`, `nebulactrl.` or `traefik.`

These work on every server, as they do for plain services: `cap_drop`, `no-new-privileges`, `read_only`, `pids_limit`, `hostname`, `dns`, curated `sysctls`, `user` and `group_add`. Everything marked above as needing an unrestricted server (`privileged`, `devices`, `gpus` and GPU reservations, `runtime` other than `runc`, `pid: host`, unconfined `seccomp` and `apparmor`, `cap_add` of `NET_ADMIN` or `SYS_ADMIN`) is the same set the typed Docker options gate behind the server's Unrestricted switch.

Not supported even on an unrestricted server (the stack model cannot carry them):

- `build:` (the agent runs images; build elsewhere, or use [Split into services](compose-import.md) with a git source)
- `include`, `extends` with a `file:`, `env_file`, top-level `models`, `provider:` services (they need other files or run host-side plugins)
- `scale` or `deploy.replicas` above 1 (container names are fixed)
- service names that are not lower-case letters, digits, `-` or `_`, up to 63 characters (they become container names and DNS aliases)

A component whose name equals the slug of another service in the same environment (or of a component of another stack there) is refused (`stack_alias_conflict`): both would answer to the same name on the environment network.

## Unrestricted (per server)

An **instance admin** can switch the allow-list off for one server: **Servers → the server → Settings → Danger zone → Unrestricted**. It is the same switch that lets plain services use [devices, GPUs, a runtime, privileged mode and the other host-level Docker options](docker-options.md#the-unrestricted-switch-per-server), because the two give identical power: anyone who can deploy a one-service stack on an unrestricted server can already ask for `privileged: true` and a host bind mount. The dialog warns that anyone who can deploy on that server can take it over (privileged mode, host networking, devices, the Docker socket, host bind mounts) and asks you to type the server's name. It is recorded in the audit log (`server.unrestricted.enable` / `.disable`, with the number of stacks and of services that use unrestricted-only options). The API is `PUT /servers/{id}/unrestricted`, the CLI `nebulactrl servers unrestricted`.

- The flag is read at **every deploy**, including rollbacks and reconcile redeploys. A rollback to a release that needed it fails on a server that is restricted again; so does the next deploy of such a stack. Switching it off does not stop running stacks.
- Only the allow-list is lifted. Ports are still never published on the host, the unsupported list above still applies, and containers still carry the managed labels and join the environment network (unless the file asks for `network_mode`).
- Keep unrestricted servers for stacks you wrote yourself. See the [threat model](../security/threat-model.md).

## Logs, metrics, terminal

- **Logs:** all components interleaved, each line prefixed `component | `; filter by component in the UI (`?component=` in the API). History and drains keep the prefix.
- **Metrics:** the charts show the whole stack (CPU and memory summed over its containers); the last sample lists each component. The component list in the Overview shows the same numbers per component.
- **Terminal and files:** the shell and the file browser open in one component; the component's position in the sorted component list is the `replica` index.

## Import

**Create → Import Docker Compose** has two modes: **Split into services** (the one-time import described in [compose-import.md](compose-import.md)) and **Run as stack**. Stack mode keeps the file as written (nothing is rewritten, `${VAR}` stays), turns the `.env` entries into the stack's variables (credential-looking keys are flagged secret), suggests a domain for components that publish an HTTP-like port (or carry a Traefik `Host()` label), shows the components and any allow-list violations, and stages one stack. Nothing deploys until you deploy. From the CLI: `nebulactrl import compose --as-stack`.

Directory and file bind mounts that the split importer can convert into config files or volumes are **not converted** in stack mode; they are violations: replace them with named volumes or `configs:` with `content:`.

## Known limits

- No rolling updates, replicas, autoscaling, maintenance mode, one-off tasks or cron tasks, volume backups (file-level) or preview environments for stacks.
- `build:` is not supported; images are pulled by tag at deploy time (`pull` happens on every deploy) and a rollback pins the recorded digests.
- Traffic analytics and request origins do not cover stacks.
- Components are not services: no per-component staging, deployments or variables.
- Only HTTP is routed. Published TCP/UDP ports are removed.
- A stack lives on one server; moving it creates a new deployment there and does not move volumes.
- Compose project directories are not backed up beyond the compose file kept in the release; named volumes are your data.
