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
| `cron` | yes | no | no | no |
| `stop_grace_seconds`, `block_egress` | yes | yes | no | yes |
| `memory_mb`, `cpus`, `image_retention`, `docker` | yes | yes | no | no |
| `tailnet` | yes | yes | no | no |
| `restart_policy`, `restart_max_retries` | yes | no | no | no |
| `pre_deploy`, `post_deploy`, `files`, `volumes`, `scale` | yes | no | no | no |
| `tasks` | yes | yes | no | no |
| `process_name`, `singleton`, `processes` | yes | no | no | no |
| `variables`, `secrets` | yes | yes | no | yes |
| `domains` | yes (needs a port) | no | yes (http routes) | yes (`component`) |
| `backups` | no | yes | no | no |
| `uptime` | yes | yes | yes | yes |
| `firewall`, `maintenance` | yes (not `one_shot`) | no | yes (http routes) | no |

A key not allowed for the kind is `not_allowed_for_kind`.

### Runtime keys

| Key | Type | Default | Bounds |
|---|---|---|---|
| `image` | string | required for `image` | `repo[:tag][@digest]`, no registry credentials |
| `port` | integer | `0` | 0-65535; `0` means no HTTP |
| `command` | argv | image default | `[]` clears |
| `one_shot` | bool | `false` | Runs to completion on every deploy; excludes `pre_deploy` and `post_deploy` |
| `stop_grace_seconds` | integer | `30` | 1-3600; a stack applies it to stopping and restarting its containers |
| `restart_policy` | string | `always` | `always`, `on-failure` or `never`; not with `one_shot`; not for databases |
| `restart_max_retries` | integer | `10` with `on-failure` | 1-100; only with `restart_policy = "on-failure"` |
| `memory_mb` | integer | `0` (no limit) | `0` or at least 6; no upper bound in the file or the API (the dashboard form stops at 1048576) |
| `cpus` | float | `0` (no limit) | at least 0, fractions allowed; no upper bound; the only float in the format |
| `image_retention` | integer | `5` | 1-50 |
| `block_egress` | bool | `false` (`true` for a new database) | No outbound internet; also on a stack |
| `generate_domain` | bool | `false` | Attach the platform-generated hostname when the service has none |

`cron` (staged, each key owned on its own): turns the service into a cron service. `schedule` (required: a 5-field cron expression or `@daily` and friends), `timezone` (`UTC`; an IANA zone), `timeout_seconds` (1800, 1-86400), `concurrency` (`forbid`, `allow`; `forbid` skips a run while the previous one still runs). `one_shot = true` is implied (`one_shot = false` with `[cron]` is an error); a cron service has no `port`, `pre_deploy`, `post_deploy`, `processes` or `tasks`. A deploy pulls or builds the image and starts nothing; the image's own command (or `command`) runs on the schedule. See [cron services](../operations/tasks.md#cron-services).

**Removing `[cron]`** releases the schedule, and the live schedule keeps running. An apply with `--prune` turns it off, exactly like switching **Run on a schedule** off in the dashboard (staged): the service stays a one-shot job that runs once per deploy. `one_shot` is a key of its own: the file only owned it through `[cron]`, so to make the service a normal long-running one again, write `one_shot = false` (an explicit `one_shot = true` keeps it one-shot). Removing only `timezone`, `timeout_seconds` or `concurrency` while `schedule` stays releases that key and changes nothing.

`health_check`: `type` (`http`, `tcp`, `docker`, `exec`, `none`), `path` (http, starts with `/`), `port`, `interval_seconds` (2), `timeout_seconds` (3), `start_period_seconds` (0), `deadline_seconds` (120, 1-3600), `success_threshold` (2), `command` (exec; first element `CMD` or `CMD-SHELL`). Each key is owned on its own. Bounds, the same in the file and the API: `interval_seconds`, `timeout_seconds` and `success_threshold` at least 1, `start_period_seconds` at least 0, `deadline_seconds` 1-3600, `port` 0-65535; there is no other upper bound (the dashboard form limits the three timings and the start period to 3600 and the threshold to 100).

`pre_deploy` and `post_deploy`: `command` (argv, required) and `timeout_seconds` (600, 1-3600).

### `git` and `build`

| Key | Type | Notes |
|---|---|---|
| `git.url` | string | Public repository, or one whose token or deploy key is already stored. An `https://`, `http://`, `git://` or `ssh://` URL, or the scp form `git@host:org/repo.git`; an SSH URL clones with the service's stored SSH deploy key ([SSH deploy keys](../operations/ssh-deploy-keys.md)), which is not a file key. Userinfo in the URL is rejected. Exclusive with `connection` and `repo`. |
| `git.connection` | string (name) | Git connection; credentials stay in the connection. immediate |
| `git.repo` | string | `owner/name`. immediate |
| `git.branch` | string | Required. |
| `git.submodules` | `auto`, `on`, `off` | Default `auto`. |
| `git.auto_deploy` | bool | Default `true` on a new link; needs `connection`. immediate |
| `git.wait_for_ci` | bool | Default `false`; needs `connection`, and a connection that can read CI (not Bitbucket or webhook-only). A push deploys only after its checks pass. immediate |
| `git.poll` | `off`, `1m`, `5m`, `15m`, `60m` | Default `off`. "Check for new commits": reads the branch head from the remote that often, for a service without a working webhook. Works with a plain `url` and with a `connection`; `watch_paths` do not apply. A commit found this way deploys with trigger `git_push`. immediate |
| `git.poll_deploy` | bool | Default `true`. Whether a new commit found by `git.poll` deploys; `false` only shows it as an available update. Only used while `git.poll` is on. immediate |
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

`target_port`, `component` and `process` are **create-only**: the platform fixes them when the domain is created and an update ignores them, so the plan refuses a different value on an existing hostname with `immutable_field` (on an app, a `target_port` other than the port of the process the domain routes to is refused as `invalid_target_port`). To point a hostname somewhere else, remove the domain and add it again, or use another hostname. Leaving the keys out of an existing domain's table changes nothing.

`cloudflare`: NebulaCtrl always manages the DNS record of a hostname whose zone is in the team's connected Cloudflare account; there is no key to turn that off. The old `manage` key was removed: a file that still has it is rejected with `unknown_key` ("key \"manage\" was removed"), so delete the line. `proxied` is the orange cloud. Leave it out and the name keeps what it has: an existing DNS-only name stays DNS only, an existing proxied name stays proxied, and `plan` shows no change for it. A name NebulaCtrl starts managing, for example a new domain, is proxied, except a wildcard name or a name on a non-HTTP(S) port, which are DNS only because the Cloudflare proxy cannot serve them. A name behind a Cloudflare Tunnel is always proxied, whatever the file says. Write `proxied = false` to keep a name DNS only, or `proxied = true` to change a DNS-only name. `origin_lock` is the [origin lock](../operations/cloudflare.md#origin-lock) of a proxied name and follows the same rule: left out means unchanged. `nebulactrl config export` writes `proxied = false` for a DNS-only name in a connected zone, since proxied is the default there.

`options`: `force_https`, `compress`, `noindex`, `security_headers` (`basic`, `strict`), `request_headers`, `response_headers`, `cors` (`origins`, `methods`, `headers`, `expose_headers`, `credentials`, `max_age_seconds`), `redirects` (array of `regex`, `replacement`, `permanent`), `rate_limit` (`average`, `period_seconds`, `burst`, `trusted_proxy_depth`), `access` (`allow_cidrs`, `deny_cidrs`, `trusted_proxy_depth`, `reject_status`), `basic_auth` (enables only; users and passwords are never in the file), `large_uploads`.

### `database` and `backups`

`database` is create-only: `engine` (required), `version`, `flavor`, `name`, `username`, `accept_license` (required `true` for source-available engines). A difference on an existing database is `immutable_field`.

`backups.<key>` (key matches `^[a-z0-9][a-z0-9_-]{0,62}$`; immediate): `destination` (name, required), `cron` (required; at most one run per 5 minutes), `timezone` (`UTC`), `enabled` (`true`), `retention_count` (7, 0-1000), `retention_days` (0, 0-3650). Schedules created in the dashboard have no key and are never touched.

### `route`

`protocol` (`http`, `tcp`, `udp`; create-only), `upstreams` (1-20 inline tables, each `{ address = "host:port" }` or `{ service = "<slug>", port = 1234 }`), `scheme`, `insecure_skip_verify`, `server_name`, `pass_host`, `health_path`, `verify_skip`, `listen_port` (1024-65535, tcp and udp), `allow_cidrs`, `deny_cidrs`, `open_to_internet` (required when `allow_cidrs` is empty on tcp and udp), `proxy_protocol` (0, 1, 2). Needs `server`. The public port of a database is not a file key.

### `stack`

Exactly one of `compose` (up to 256 KiB) and `compose_file` (relative path, sent as an attachment); `wait_seconds` (300, 1-3600); `resources.<component>` with `memory_mb` and `cpus`. `stop_grace_seconds` and `block_egress` apply to the stack's containers (see [Runtime keys](#runtime-keys)). `generate_domain` is refused on a stack: the generated hostname is a dashboard and API action (Networking → Generate domain, with a `component`); in the file write an explicit `domains."<host>"` table with `component`.

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

A process is owned as a whole: a key you leave out takes its default. `domains."<host>".process` names the process a domain routes to (apps only; stacks use `component`); without it a domain routes to the own process, or to the only process with a port. The process name `web` (or `process_name`) is taken by the own process. A service with volumes cannot scale a process above one replica. **The file has no rename:** changing `processes.old` to `processes.new` is a remove and an add (a new process with the defaults; `old` is only released, and `--prune` removes it); see [processes](../operations/processes.md) for what that means for replicas and domains.

### `volumes`, `files`, `scale`, `uptime`, `tasks`, `docker`, `tailnet`

- `volumes`: `name = "/mount/path"`, or the table form `name = { path = "/mount/path", owner = "10001:10001" }`; names match `^[a-z0-9][a-z0-9_.-]{0,62}$`, paths are absolute. The string form means no owner. `owner` is `"uid"` or `"uid:gid"`, numbers only (0-2147483647): the agent sets the owner of the volume's root directory (not recursively), for an image that runs as a non-root user and mounts a path the image does not contain ([volumes and their owner](../operations/deployments.md#volumes-and-their-owner)). Export writes the table form only for a volume that has an owner. An environment override replaces a volume entry as a unit. Staged. Removing a volume from the file only **releases** it: it stays attached to the service. An apply with `--prune` detaches it (a staged change); the Docker volume and its data are never deleted. A service with volumes cannot have `scale.replicas` above 1.
- `files."/path"`: `mode` (octal string, `0644`) and exactly one of `content` or `source` (an attachment). At most 16 files, 64 KiB each, 256 KiB in all. The whole set is one unit.
- `scale` (immediate): `replicas` (1-10), `autoscaling.enabled`, `autoscaling.min` (1-10), `autoscaling.max` (1-10, at least `min`), `autoscaling.target_cpu_percent` (20-95). More than one replica, or autoscaling on, is refused with `replicas_require_stateless` for a service with volumes, with `files`, or without an HTTP `port` (a service with `processes` may scale a process without a port), the same rule as the dashboard. The plan checks it against the live service, so a file that leaves out `port` or `volumes` is judged by what the service has.
- `uptime` (immediate, admin scope): `enabled`, `alerts`, `failure_threshold` (3, 1-20), `recovery_threshold` (2, 1-20), `reminder_minutes` (0, 0-1440).
- `tasks.<name>` (immediate, at most 20; image, git and database services): `schedule` (required cron), `timezone` (`UTC`), `action` (`command`, `restart`; `command` by default), `command` (argv, required unless `action = "restart"`, where it must be absent), `enabled`, `timeout_seconds` (1800, 1-86400; not with `restart`), `concurrency` (`forbid`, `allow`). A `restart` task restarts the service's running container on the schedule and is refused on a `one_shot` service.
- `docker`: staged, each key owned individually. Allowed everywhere: `cap_add`, `cap_drop`, `no_new_privileges`, `read_only_rootfs`, `pids_limit`, `hostname`, `dns`, `sysctls`, `host_sysctls`, `user`, `group_add`, `ulimits`, `shm_size_mb`, `extra_hosts`, `labels`, `init`, `stop_signal`, `tmpfs` (`size_mb` 0-65536, `0` or absent means 64), `entrypoint` (1-32 elements). Only on unrestricted servers: `devices`, `gpus`, `runtime`, `privileged`, `pid_host`, `seccomp_unconfined`, `apparmor_unconfined` and the capabilities `NET_ADMIN` and `SYS_ADMIN`. The rules, limits and the API-to-file names are in [Docker options](../operations/docker-options.md#in-nebulatoml).
- `tailnet`: staged, **owned as one unit** (every key you leave out takes its default, like a process; the live Funnel flag is never touched). Puts the service on your Tailscale or Headscale tailnet; see [per-service tailnet](../operations/service-tailnet.md). Image, git and database services. A cron or `one_shot` service cannot be on a tailnet.

  | Key | Type | Default | Notes |
  |---|---|---|---|
  | `enabled` | bool | required | `false` switches the node off; the other keys are then ignored. |
  | `hostname` | string | `<service>-<environment>` | An RFC 1123 label, at most 63 characters; lower-cased. |
  | `extra_tags` | array of strings | none | `tag:name`, at most 10. Each must be in the integration's allowed tags. |
  | `web` | bool | `false` | Serves the service's `port` on the tailnet (HTTPS on 443 with Tailscale, HTTP on 80 with Headscale). Needs `port` in the file. |
  | `tcp` | array of `{ listen, target, tls }` | none | At most 16 forwards: `listen` (1-65535, unique, required), `target` (the service port; defaults to `listen`), `tls` (terminate TLS with the node certificate; Tailscale only). With `web = true`, 80 and 443 are taken. |
  | `hide_public` | bool | `false` | "Tailnet only": the service's public domain routers are removed. |

  At least one of `web = true` or a `tcp` entry is needed. The values go through the same rules as `PATCH /services/{id}` (`tailnetcfg` validation), so the file and the API agree. What depends on the team's integration (`tailnet_not_configured`, `tailnet_tag_not_allowed`, the Tailscale-only features) is checked when the change is applied, not in the plan. `funnel` is **not a file key** (`unknown_key`): it publishes the service on the public internet and needs a typed confirmation in the dashboard. A file apply keeps whatever Funnel value is live, and refuses a table without `web = true` while Funnel is on. Removing the table releases it and leaves the node as it is; write `enabled = false` to switch it off. In an environment block the table merges by key like any other, and `tcp` replaces as an array; `unset = ["tailnet"]` releases it there.

  ```toml
  [services.web.tailnet]          # admin UI reachable only from the tailnet
  enabled     = true
  hostname    = "openheroes-admin"
  tcp         = [{ listen = 443, target = 8641, tls = true }]
  hide_public = true
  ```

### `firewall` and `maintenance`

Both are immediate and operational, like their dashboard controls: they change the proxy at once, without a deployment, and are not staged or rolled back. They apply to HTTP traffic only: image and git services and routes with `protocol = "http"`. Either table on a database, a stack or a tcp or udp route is `not_allowed_for_kind`, and on a `one_shot` or `cron` job (which serves no traffic) `invalid_value`.

**`firewall`** (immediate, scope `proxy:write`; it also needs `projects:write`, which every apply holds) is the service's own rule list, see [Service firewall](../operations/firewall.md). The first enabled rule whose conditions all match decides, so **the list is owned as one unit, in order**: a plan or apply replaces the whole list, and moving a rule is a change. Each rule is a `[[services.<s>.firewall.rules]]` table, each condition a `[[services.<s>.firewall.rules.conditions]]` table below it.

| Key | Type | Default | Notes |
|---|---|---|---|
| `rules` | array of tables | none | At most 50. An empty `[services.<s>.firewall]` table owns the firewall with no rules. |
| `rules.id` | string | kept by position | 1-16 lowercase letters or digits, unique in the list. Optional: a rule without one keeps the id of the live rule at the same position (a new rule gets a generated one), so a file without ids plans as unchanged. Ids starting with `std` are reserved for the standard protection (`invalid_value`). |
| `rules.name` | string | required | 1-80 characters; shown in the dashboard and the block log. |
| `rules.action` | string | required | `block` answers HTTP 403; `allow` lets the request through and skips the rules below it. |
| `rules.disabled` | bool | `false` | Keep the rule but never evaluate it. |
| `rules.conditions` | array of tables | required | 1-10 per rule; every condition must match. |
| `conditions.field` | string | required | `ip`, `country`, `path`, `method`, `header`, `userAgent`, `query`. |
| `conditions.op` | string | required | Depends on the field: `ip`, `country` and `method` take `in`; `path` takes `prefix`, `exact`, `contains`, `regex`; `header` and `query` take `equals`, `contains`, `regex`, `present`; `userAgent` takes `contains`, `regex`. |
| `conditions.name` | string | none | Header or query parameter name; required for `header` and `query`, not allowed for the other fields. |
| `conditions.values` | array of strings | none | 1-50 per condition (an `ip` condition takes up to 200 addresses; none for `present`); the condition matches when any value does. Trimmed and de-duplicated; countries and methods are upper-cased. |
| `conditions.not` | bool | `false` | Invert the condition. |

The rules go through the same validation as `PUT /services/{id}/firewall` (`invalid_value`, naming the rule and condition at fault). The plan also checks what only the target server decides: `agent_too_old` when its agent predates firewalls, and `geo_required` when an active rule matches `country` and the server has no geolocation. Either blocks the service in the plan instead of failing the apply. The **standard protection** (secret-file probes) is not in the file: it is always on, after your rules, and never exported. Removing `[services.<s>.firewall]` from the file only **releases** the list, which stays live; an apply with `--prune` removes the service's own rules, like **Apply firewall** with an empty list (the standard protection stays). Export writes the live rules, ids included, so a round trip keeps them; a service with no rules gets no table unless the file already owns the firewall. In an environment block the rule list replaces the lower one as a whole.

```toml
[[services.web.firewall.rules]]
id     = "wp"
name   = "Block WordPress probes"
action = "block"

[[services.web.firewall.rules.conditions]]
field  = "path"
op     = "contains"
values = ["wp-login.php", "xmlrpc.php", "/wp-admin", "phpmyadmin"]
```

**`maintenance`** (immediate, scope `deployments:write`) puts the service into [maintenance mode](../operations/maintenance-and-error-pages.md#maintenance-mode): every domain answers `503` with the maintenance page. The table is owned as one unit.

| Key | Type | Default | Notes |
|---|---|---|---|
| `enabled` | bool | required | `true` switches maintenance on, `false` off. |
| `title` | string | `Down for maintenance` | At most 80 characters. Only used while `enabled = true`. |
| `message` | string | the default copy | At most 600 characters, plain text, newlines kept. Only used while `enabled = true`. |
| `eta` | string | none | An RFC 3339 time **in the future**, written as a string (`"2026-12-01T06:00:00Z"`); stored in UTC, truncated to seconds. Shown on the page and sent as `Retry-After`. Only used while `enabled = true`. |

`title`, `message` and `eta` are only compared while `enabled = true`: switching maintenance off keeps the stored text for next time, which a file with `enabled = false` neither sets nor compares. A plan blocks an `eta` that has passed (`invalid_value`) when it would change the setting; a file that repeats the live window is unchanged even after the time has passed. Removing `[services.<s>.maintenance]` only **releases** the setting and leaves the service in whatever state it is in; an apply with `--prune` **switches maintenance off**. Export writes the table while maintenance is on, and `enabled = false` where the file already owns it; a service that is not in maintenance and whose file says nothing gets no table, so an export never writes an outage that is not live. A file that says `enabled = true` puts the service back into maintenance on every apply that finds it off: end the window by writing `enabled = false`, not by deleting the table.

```toml
[services.web.maintenance]
enabled = true
title   = "Back soon"
message = "We are upgrading the database."
eta     = "2026-12-01T06:00:00Z"
```

### Not in the file

These settings are deliberately not keys: they are secrets, or an action that needs a person's confirmation. An apply never reads or changes them; set each where listed.

| Setting | Why it is not a key | Where to set it |
|---|---|---|
| Registry credentials of an image service | A secret | Service → Settings → Source (`registryAuth` in the API); see [change a service's source](../operations/builds.md#change-a-services-source). Moving to another source clears them. |
| Git access token | A secret | Service → Settings → Source (`gitToken` in the API), or link a git connection with `git.connection` and `git.repo` ([Git providers](../operations/git-providers.md)). |
| SSH deploy keys | A private key | Service → Settings → Source → **Use SSH**; a `git.url` in SSH form then uses it ([SSH deploy keys](../operations/ssh-deploy-keys.md)). |
| Deploy hook URL | The URL is the secret and is shown once | Service → Settings → Deploy → **Deploy hook** ([deploy hooks](../operations/deploy-hooks.md#turn-it-on)). |
| Basic-auth users and passwords | `options.basic_auth` only switches it on | The domain's rules in the dashboard, or the API ([basic auth](../operations/proxy-routes.md#basic-auth)). The plan warns `basic_auth_no_users` until a user exists. |
| Tailnet Funnel | Publishes the service on the public internet; needs a typed confirmation | The service's tailnet settings ([Funnel](../operations/service-tailnet.md#funnel)). |
| The public port of a managed database | A separate TCP route with an allow-list | The database's Networking settings or the API ([public port](../operations/databases.md#public-port)). |
| The generated hostname of a stack | An action, not a setting | Networking → Generate domain; in the file write an explicit `domains."<host>"` table with `component`. |
| The standard protection of a service's firewall | It is always on, after the service's own rules | Not a file key; to let one of its paths through, add an `allow` rule for it ([standard protection](../operations/firewall.md#standard-protection)). |

## Environments

`[environments.<slug>]` overrides one environment: `server`, `variables` and `secrets` (the environment layer of shared variables), and `services.<slug>` with the same keys as a service plus `unset`. The file never creates, renames or deletes environments.

### The merge rule

For service `S` in environment `E` the resolved definition is `merge(services.S, environments.E.services.S)`:

1. Tables merge by key, recursively.
2. Scalars and arrays replace the lower value as a whole. An override restates a full list.
3. Leaves replace as a unit: a variable entry, a volume entry, `domains.<host>.options`, `files.<path>`, a `route.upstreams` item, `resources.<component>`. A process is not a leaf: `processes.<name>` merges key by key like `tasks.<name>`.
4. `unset` (environment blocks only) removes TOML key paths after the merge, for example `["variables.FEATURE_X", 'domains."shop.acme.com"', "tasks.digest", "processes.clock", "scale"]`. A removed key is not owned in that environment; nothing live is deleted.
5. `only_in` filters services before merging. A service defined only in an environment block must be complete.
6. Error positions refer to the line that supplied the winning value.

## Limits

File 256 KiB; attachments at most 8, 256 KiB each, 1 MiB in all; 64 services; 500 variables per service and per shared layer; 50 domains per service; 16 files per service; 20 tasks per service; 9 processes per service besides its own; 50 firewall rules per service, 10 conditions per rule, 50 values per condition (200 for `ip`); 50 watch paths; 20 upstreams; 32 environment blocks; table nesting depth 10; any string 64 KiB.

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
