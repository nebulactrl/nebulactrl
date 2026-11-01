---
title: nebula.toml reference
description: "Every key of the project file (services, variables, domains, tasks, backups, environments), the merge rule and error codes."
section: Reference
order: 40
---

# nebula.toml reference

Every key of `nebula.toml`. How the file is used (validate, plan, apply, git, drift) is in the [nebula.toml guide](../operations/project-config.md); the machine-readable form is the JSON Schema at `/schema/nebula.toml.json`, which carries the same descriptions and examples.

## Conventions

- TOML, UTF-8, `schema = 1`. Unknown keys, wrong types and TOML datetimes are errors.
- `snake_case` everywhere. Units live in the key name: `_seconds`, `_mb`, `_days`, `_minutes`, `_percent`. No `"30s"` or `"512mb"` strings.
- Services are a map keyed by slug (`[services.web]`). Collections are maps keyed by their identity: variable name, hostname, volume name, task name, backup key, file path. Arrays are for ordered or atomic lists only (argv, upstreams, CIDRs, watch paths, redirects).
- *name* means a name resolved in the workspace (server, git connection, registry, backup destination, network, certificate). An unknown or ambiguous name is a problem, never a guess.
- **staged** keys take effect on the next deploy and show in the staged bar. **immediate** keys take effect at once, like the dashboard control they map to.
- An absent key is not owned and not changed on an existing service, whatever its default says. The default applies when the service is created.

## Root

| Key | Type | Notes |
|---|---|---|
| `schema` | integer | Required. Only `1`. |
| `"$schema"` | string | Ignored; for editors. |
| `project` | string | Optional project slug; must match the target project. |
| `server` | string (name) | Default server. Precedence: service > environment > root. |
| `variables` | table | Project-layer shared variables, read as `${{shared.KEY}}`. |
| `secrets` | table `{ keys, scope }` | Shared secret names, no values. |
| `services` | table | One table per service, keyed by slug. |
| `environments` | table | Optional overrides per environment slug. |

`auto_apply`, `is_production`, tokens and credentials are not keys.

## Services

Exactly one source key decides the kind:

| Source key | Kind |
|---|---|
| `image` | An image service (`app`, or `worker` without a `port`). |
| `git` | A service built from a repository. |
| `database` | A managed database. |
| `route` | An external proxy route; no container. |
| `stack` | A compose stack. |

An app can move between `image` and `git` on apply: the plan shows a staged `source` change and the next deploy applies it. Moving to or from `database`, `route` or `stack` is `source_type_change`. See [switching between an image and a Git repository](../operations/project-config.md#switching-between-an-image-and-a-git-repository).

Common keys: `name` (immediate), `server` (staged; required for `route`), `only_in` (environments the service exists in), `depends_on` (deploy order, staged), `networks` (attach existing shared networks, staged), `unset` (environment blocks only).

| Section | image, git | database | route | stack |
|---|---|---|---|---|
| `port`, `command`, `health_check`, `one_shot` | yes | derived | no | no |
| `stop_grace_seconds`, `memory_mb`, `cpus`, `image_retention`, `block_egress`, `docker` | yes | yes | no | no |
| `pre_deploy`, `post_deploy`, `files`, `volumes`, `scale`, `tasks` | yes | no | no | no |
| `process_name`, `singleton`, `processes` | yes | no | no | no |
| `variables`, `secrets` | yes | yes | no | yes |
| `domains` | yes (needs a port) | no | yes (http routes) | yes (`component`) |
| `backups` | no | yes | no | no |
| `uptime` | yes | yes | yes | yes |

A key not allowed for the kind is `not_allowed_for_kind`.

### Runtime keys

| Key | Type | Default | Bounds |
|---|---|---|---|
| `image` | string | required for `image` | `repo[:tag][@digest]`, no registry credentials |
| `port` | integer | `0` | 0-65535; `0` means no HTTP |
| `command` | argv | image default | `[]` clears |
| `one_shot` | bool | `false` | Runs to completion on every deploy; excludes `pre_deploy` and `post_deploy` |
| `stop_grace_seconds` | integer | `30` | 1-3600 |
| `memory_mb` | integer | `0` (no limit) | `0` or at least 6 |
| `cpus` | float | `0` (no limit) | at least 0; the only float in the format |
| `image_retention` | integer | `5` | 1-50 |
| `block_egress` | bool | `false` (`true` for a new database) | No outbound internet |
| `generate_domain` | bool | `false` | Attach the platform-generated hostname when the service has none |

`health_check`: `type` (`http`, `tcp`, `docker`, `exec`, `none`), `path` (http, starts with `/`), `port`, `interval_seconds` (2), `timeout_seconds` (3), `start_period_seconds` (0), `deadline_seconds` (120, 1-3600), `success_threshold` (2), `command` (exec; first element `CMD` or `CMD-SHELL`). Each key is owned on its own.

`pre_deploy` and `post_deploy`: `command` (argv, required) and `timeout_seconds` (600, 1-3600).

### `git` and `build`

| Key | Type | Notes |
|---|---|---|
| `git.url` | string | Public repository, or one whose token or deploy key is already stored. Userinfo in the URL is rejected. Exclusive with `connection` and `repo`. |
| `git.connection` | string (name) | Git connection; credentials stay in the connection. immediate |
| `git.repo` | string | `owner/name`. immediate |
| `git.branch` | string | Required. |
| `git.submodules` | `auto`, `on`, `off` | Default `auto`. |
| `git.auto_deploy` | bool | Default `true` on a new link; needs `connection`. immediate |
| `git.watch_paths` | array, at most 50 | Replaces the list; globs and `!` exclusions. immediate |
| `build.type` | `auto`, `dockerfile`, `railpack`, `starter:<node\|python\|go\|ruby\|php\|static>` | Default `auto`. |
| `build.dockerfile`, `build.context` | relative path | Defaults `Dockerfile` and `.`. No absolute paths, no `..`. |
| `build.location` | `inherit`, `target`, `build_server` | Default `inherit`. |
| `build.server`, `build.registry` | string (name) | Optional. |

### Variables and secrets

A variable value is a string in the variable grammar: a plain value, `"${{PORT}}"`, `"${{db.DATABASE_URL}}"`, `"${{shared.KEY}}"`, `"${{team.KEY}}"`, `"${{vault:integration/path#key}}"`, a generator (`"${{secret(32)}}"`, `randomInt`, `uuid`, `randomBytes`), or a transform (`"${{urlencode(db.PASSWORD)}}"`). `"$${{"` writes a literal `${{`. Non-string values are `wrong_type`. Names match `^[A-Za-z_][A-Za-z0-9_]{0,254}$`; `PORT` and `NEBULA_*` are reserved.

The table form carries metadata: `KEY = { value = "...", scope = "build", comment = "...", rotate_every_days = 90, public = false }`. `scope` is `runtime` (default), `build` or `both`; `public = true` asserts a literal is not a credential. `secrets = { keys = ["NAME"], scope = "runtime" }` declares names that are secret, with no value in the file. A variable containing a generator is a secret; `secret = false` with one is rejected.

### `domains`

`[services.<s>.domains."<host>[/<path-prefix>]"]`; a table may be empty. Keys (all staged unless noted): `target_port`, `component` (stack, required there), `process` (app with processes), `strip_prefix`, `add_prefix`, `kind` (`serve`, `redirect`), `redirect_to`, `redirect_code` (301, 302, 307, 308; default 308), `preserve_path`, `tls` (`auto`, `custom`, `none`), `certificate` (name), `dns_resolver`, `www_redirect`, `options` (the whole table replaces the live option set), and `cloudflare` (`proxied`, `origin_lock`; immediate, needs a Cloudflare connection).

`cloudflare`: NebulaCtrl always manages the DNS record of a hostname whose zone is in the team's connected Cloudflare account; there is no key to turn that off. The old `manage` key was removed: a file that still has it is rejected with `unknown_key` ("key \"manage\" was removed"), so delete the line. `proxied` is the orange cloud. Leave it out and the name keeps what it has: an existing DNS-only name stays DNS only, an existing proxied name stays proxied, and `plan` shows no change for it. A name NebulaCtrl starts managing, for example a new domain, is proxied, except a wildcard name or a name on a non-HTTP(S) port, which are DNS only because the Cloudflare proxy cannot serve them. A name behind a Cloudflare Tunnel is always proxied, whatever the file says. Write `proxied = false` to keep a name DNS only, or `proxied = true` to change a DNS-only name. `origin_lock` is the [origin lock](../operations/cloudflare.md#origin-lock) of a proxied name and follows the same rule: left out means unchanged. `nebulactrl config export` writes `proxied = false` for a DNS-only name in a connected zone, since proxied is the default there.

`options`: `force_https`, `compress`, `noindex`, `security_headers` (`basic`, `strict`), `request_headers`, `response_headers`, `cors` (`origins`, `methods`, `headers`, `expose_headers`, `credentials`, `max_age_seconds`), `redirects` (array of `regex`, `replacement`, `permanent`), `rate_limit` (`average`, `period_seconds`, `burst`, `trusted_proxy_depth`), `access` (`allow_cidrs`, `deny_cidrs`, `trusted_proxy_depth`, `reject_status`), `basic_auth` (enables only; users and passwords are never in the file), `large_uploads`.

### `database` and `backups`

`database` is create-only: `engine` (required), `version`, `flavor`, `name`, `username`, `accept_license` (required `true` for source-available engines). A difference on an existing database is `immutable_field`.

`backups.<key>` (key matches `^[a-z0-9][a-z0-9_-]{0,62}$`; immediate): `destination` (name, required), `cron` (required; at most one run per 5 minutes), `timezone` (`UTC`), `enabled` (`true`), `retention_count` (7, 0-1000), `retention_days` (0, 0-3650). Schedules created in the dashboard have no key and are never touched.

### `route`

`protocol` (`http`, `tcp`, `udp`; create-only), `upstreams` (1-20 inline tables, each `{ address = "host:port" }` or `{ service = "<slug>", port = 1234 }`), `scheme`, `insecure_skip_verify`, `server_name`, `pass_host`, `health_path`, `verify_skip`, `listen_port` (1024-65535, tcp and udp), `allow_cidrs`, `deny_cidrs`, `open_to_internet` (required when `allow_cidrs` is empty on tcp and udp), `proxy_protocol` (0, 1, 2). Needs `server`. The public port of a database is not a file key.

### `stack`

Exactly one of `compose` (up to 256 KiB) and `compose_file` (relative path, sent as an attachment); `wait_seconds` (300, 1-3600); `resources.<component>` with `memory_mb` and `cpus`.

### `processes`

A service can run more than one command from the same build. The service's own `port`, `command`, `health_check`, `stop_grace_seconds`, `memory_mb`, `cpus` and `singleton` are its **default process**, named `web` unless `process_name` says otherwise. `[services.<s>.processes.<name>]` adds the others (staged, at most 9; names match `^[a-z0-9]([a-z0-9-]{0,29}[a-z0-9])?$`). See [processes](../operations/processes.md).

| Key | Type | Default | Bounds |
|---|---|---|---|
| `process_name` (service) | string | `web` | Name of the service's own process |
| `singleton` (service) | bool | `false` | The own process runs one instance; stop before start |
| `processes.<name>.command` | argv | required | 1-64 elements; no shell |
| `processes.<name>.port` | integer | `0` | 0-65535, unique across the service's processes |
| `processes.<name>.stop_grace_seconds` | integer | the service's | 1-3600 |
| `processes.<name>.singleton` | bool | `false` | Excludes `scale.replicas` above 1 and autoscaling |
| `processes.<name>.memory_mb`, `cpus` | integer, float | the service's | as for the service |
| `processes.<name>.health_check` | table | follows the process' port | the keys of the service's `health_check` |
| `processes.<name>.scale` | table (immediate) | 1 replica | `replicas`, `autoscaling.*` as for `scale` |

A process is owned as a whole: a key you leave out takes its default. `domains."<host>".process` names the process a domain routes to (apps only; stacks use `component`); without it a domain routes to the own process, or to the only process with a port. The process name `web` (or `process_name`) is taken by the own process. A service with volumes cannot scale a process above one replica.

### `volumes`, `files`, `scale`, `uptime`, `tasks`, `docker`

- `volumes`: `name = "/mount/path"`; names match `^[a-z0-9][a-z0-9_.-]{0,62}$`, paths are absolute. Staged. Detaching never deletes data. A service with volumes cannot have `scale.replicas` above 1.
- `files."/path"`: `mode` (octal string, `0644`) and exactly one of `content` or `source` (an attachment). At most 16 files, 64 KiB each, 256 KiB in all. The whole set is one unit.
- `scale` (immediate): `replicas` (1-10), `autoscaling.enabled`, `autoscaling.min`, `autoscaling.max`, `autoscaling.target_cpu_percent` (20-95).
- `uptime` (immediate, admin scope): `enabled`, `alerts`, `failure_threshold` (3, 1-20), `recovery_threshold` (2, 1-20), `reminder_minutes` (0, 0-1440).
- `tasks.<name>` (immediate, at most 20): `schedule` (required cron), `timezone` (`UTC`), `command` (argv, required), `enabled`, `timeout_seconds` (1800, 1-86400), `concurrency` (`forbid`, `allow`).
- `docker`: staged, each key owned individually. Allowed everywhere: `cap_add`, `cap_drop`, `no_new_privileges`, `read_only_rootfs`, `pids_limit`, `hostname`, `dns`, `sysctls`, `host_sysctls`, `user`, `group_add`, `ulimits`, `shm_size_mb`, `extra_hosts`, `labels`, `init`, `stop_signal`, `tmpfs`, `entrypoint`. Only on unrestricted servers: `devices`, `gpus`, `runtime`, `privileged`, `pid_host`, `seccomp_unconfined`, `apparmor_unconfined` and the capabilities `NET_ADMIN` and `SYS_ADMIN`.

## Environments

`[environments.<slug>]` overrides one environment: `server`, `variables` and `secrets` (the environment layer of shared variables), and `services.<slug>` with the same keys as a service plus `unset`. The file never creates, renames or deletes environments.

### The merge rule

For service `S` in environment `E` the resolved definition is `merge(services.S, environments.E.services.S)`:

1. Tables merge by key, recursively.
2. Scalars and arrays replace the lower value as a whole. An override restates a full list.
3. Leaves replace as a unit: a variable entry, `domains.<host>.options`, `files.<path>`, a `route.upstreams` item, `resources.<component>`. A process is not a leaf: `processes.<name>` merges key by key like `tasks.<name>`.
4. `unset` (environment blocks only) removes TOML key paths after the merge, for example `["variables.FEATURE_X", 'domains."shop.acme.com"', "tasks.digest", "processes.clock", "scale"]`. A removed key is not owned in that environment; nothing live is deleted.
5. `only_in` filters services before merging. A service defined only in an environment block must be complete.
6. Error positions refer to the line that supplied the winning value.

## Limits

File 256 KiB; attachments at most 8, 256 KiB each, 1 MiB in all; 64 services; 500 variables per service and per shared layer; 50 domains per service; 16 files per service; 20 tasks per service; 9 processes per service besides its own; 50 watch paths; 20 upstreams; 32 environment blocks; table nesting depth 10; any string 64 KiB.

## Problems

Each problem is `{ severity, code, message, path, line, column, hint, suggestions, environment }`, shown as `nebula.toml:19:1: error[unknown_key] services.web.healthcheck: unknown key "healthcheck"; did you mean "health_check"?`. The codes and what to do about each are in the [troubleshooting table of the guide](../operations/project-config.md#troubleshooting-by-code).

## Examples

A web app from git, a worker, Postgres with nightly backups, a cron task, a proxied domain, an external route, an uptime monitor and staging overrides:

<!-- example: docs-full -->
```toml
#:schema https://nebula.example.com/schema/nebula.toml.json
schema  = 1
project = "acme-shop"          # must equal the target project's slug when --project / the binding names one
server  = "fra-1"              # default server (by name) for every service; override per service or environment

# Project-wide shared variables. Services read them as ${{shared.APP_NAME}}.
[variables]
APP_NAME = "Acme Shop"

# ───────────────────────── web: built from git ─────────────────────────
[services.web]
name               = "Web"
port               = 3000                 # no port => the service is a worker (no HTTP health check, no domain)
stop_grace_seconds = 30
depends_on         = ["db"]               # deploy order; references in variables add edges automatically

[services.web.git]
connection  = "github-acme"               # git connection NAME (Team settings → Integrations); never a token
repo        = "acme/shop"
branch      = "main"
auto_deploy = true                        # immediate
watch_paths = ["apps/web/**", "packages/**", "!**/*.md"]   # monorepo filter; replaces the whole list

[services.web.build]
type       = "dockerfile"                 # auto | dockerfile | railpack | starter:<node|python|go|ruby|php|static>
dockerfile = "apps/web/Dockerfile"
context    = "."

[services.web.health_check]
type             = "http"
path             = "/healthz"
deadline_seconds = 90

[services.web.pre_deploy]                 # runs before the new release starts; non-zero exit fails the deploy
command         = ["bun", "run", "db:migrate"]
timeout_seconds = 300

[services.web.variables]
NODE_ENV     = "production"
APP_URL      = "https://shop.acme.com"
DATABASE_URL = "${{db.DATABASE_URL}}"     # reference to another service's exported variable
SESSION_SECRET = "${{secret(48)}}"        # generator: runs once, same text again keeps the value
SITE_NAME    = "${{shared.APP_NAME}}"
BUILD_FLAG   = { value = "1", scope = "build", comment = "read by the bundler" }   # table form for metadata

[services.web.secrets]                    # names only; the values are set in the dashboard / CLI / secret manager
keys = ["STRIPE_SECRET_KEY"]

[services.web.domains."shop.acme.com"]    # key = hostname, or hostname/path-prefix
www_redirect = true

[services.web.domains."shop.acme.com".cloudflare]   # immediate; needs a Cloudflare connection
proxied = true

[services.web.domains."shop.acme.com".options]      # replaces the domain's whole option set
compress         = true
security_headers = "strict"

[services.web.scale]                      # immediate
replicas = 2

[services.web.uptime]                     # immediate
failure_threshold = 3
reminder_minutes  = 60

[services.web.tasks.digest]               # cron task, immediate; runs in the service's image
schedule        = "0 7 * * 1-5"
timezone        = "Europe/Berlin"
command         = ["bun", "run", "jobs:digest"]
timeout_seconds = 900

# ───────────────────────── worker: same repo, no port ─────────────────────────
[services.worker]
command    = ["bun", "run", "worker"]
memory_mb  = 512
depends_on = ["db"]

[services.worker.git]
connection  = "github-acme"
repo        = "acme/shop"
branch      = "main"
watch_paths = ["apps/worker/**", "packages/**"]

[services.worker.build]
type       = "dockerfile"
dockerfile = "apps/worker/Dockerfile"
context    = "."

[services.worker.variables]
DATABASE_URL      = "${{db.DATABASE_URL}}"
QUEUE_CONCURRENCY = "8"

# ───────────────────────── db: managed Postgres with nightly backups ─────────────────────────
[services.db.database]                    # create-only keys: engine, version, name, username
engine   = "postgres"
version  = "17"
name     = "shop"
username = "shop"

[services.db.backups.nightly]             # immediate; key = stable identity of the schedule
destination     = "offsite-s3"            # backup destination NAME
cron            = "0 2 * * *"
timezone        = "Europe/Berlin"
retention_count = 14
retention_days  = 30

[services.db.uptime]
reminder_minutes = 30

# ───────────────────────── printer: external route (no container) ─────────────────────────
[services.printer]
name    = "Office printer"
only_in = ["production"]                  # this service exists in the file only for these environments

[services.printer.route]
protocol    = "http"
upstreams   = [{ address = "192.168.1.20:631" }]     # or { service = "web", port = 3000 } for a managed service
health_path = "/"

[services.printer.domains."print.acme.com".options.access]
allow_cidrs = ["203.0.113.0/24"]

# ───────────────────────── staging overrides ─────────────────────────
[environments.staging]
server = "fra-staging"

[environments.staging.variables]          # environment-layer shared variables (override the project layer)
APP_NAME = "Acme Shop (staging)"

[environments.staging.services.web]
unset = ['domains."shop.acme.com"', "scale", "uptime"]   # TOML key paths; only allowed in environment blocks

[environments.staging.services.web.git]
branch = "develop"

[environments.staging.services.web.variables]
NODE_ENV = "staging"
APP_URL  = "https://shop.staging.acme.com"

[environments.staging.services.web.domains."shop.staging.acme.com".cloudflare]
proxied = false

[environments.staging.services.worker.git]
branch = "develop"

[environments.staging.services.db]
unset = ["backups.nightly"]
```

A single image:

<!-- example: docs-single-image -->
```toml
#:schema https://nebula.example.com/schema/nebula.toml.json
schema = 1
server = "fra-1"

[services.whoami]
image = "traefik/whoami:v1.10"
port  = 80

[services.whoami.health_check]
type = "http"
path = "/health"

[services.whoami.variables]
GREETING = "hello"

[services.whoami.domains."whoami.acme.com"]

[services.whoami.scale]
replicas = 2
```

A service with processes, a per-process scale and a staging override that drops one:

<!-- example: processes -->
```toml
#:schema https://nebula.example.com/schema/nebula.toml.json
schema = 1
server = "fra-1"

[services.web]
port         = 3000
command      = ["bin/rails", "server", "-b", "0.0.0.0"]   # the service's own process
process_name = "web"                                      # optional; "web" when absent
singleton    = false                                      # optional; applies to the service's own process

[services.web.git]
url    = "https://github.com/acme/shop.git"
branch = "main"

[services.web.pre_deploy]                                 # runs once per deployment, not once per process
command = ["bin/rails", "db:migrate"]

[services.web.domains."shop.acme.com"]                    # routes to the service's own process

[services.web.processes.sidekiq]
command            = ["bundle", "exec", "sidekiq"]
stop_grace_seconds = 120

[services.web.processes.sidekiq.scale]                    # immediate, per process
replicas = 2

[services.web.processes.clock]
command   = ["bundle", "exec", "clockwork", "clock.rb"]
singleton = true                                          # one instance; the old one stops before the new one starts

# staging runs one sidekiq and no clock
[environments.staging.services.web]
unset = ["processes.clock"]

[environments.staging.services.web.processes.sidekiq.scale]
replicas = 1
```

A compose stack:

<!-- example: docs-stack -->
```toml
#:schema https://nebula.example.com/schema/nebula.toml.json
schema = 1
server = "fra-1"

[services.shop]
name = "Shop stack"

[services.shop.stack]
wait_seconds = 300
compose = '''
services:
  web:
    image: nginx:1.27-alpine
    environment:
      GREETING: ${GREETING}
  cache:
    image: valkey/valkey:8
'''

[services.shop.stack.resources.cache]
memory_mb = 256

[services.shop.variables]
GREETING = "hello"

[services.shop.domains."shop.acme.com"]
component   = "web"
target_port = 80
```
