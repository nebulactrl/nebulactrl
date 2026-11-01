---
title: One-click templates
description: "Deploy a self-hosted app from the template catalogue."
section: Templates
order: 10
---

# One-click templates

NebulaCtrl ships a hand-reviewed catalog of self-hostable apps (`templates/catalog/*.yaml`, over 300 of them, embedded in the server binary). **Add to canvas** turns a template into ordinary, staged services in an environment: nothing is deployed until you press Deploy (or choose "Add and deploy"). A template is **not** a Docker Compose project (to turn one into services, see [Compose import](compose-import.md)); every template service becomes an independent service on the canvas, on one server, with normal deploy, rollback, variables, domains and logs.

## What an install creates

For each template service, in one transaction:

- a service (image pinned as `repo:tag@sha256:…`), or a managed database through the databases package (`database:` block);
- volumes, config files, variables (secrets generated once and stored encrypted), at most one domain, and published TCP/UDP ports (proxy routes);
- manual links for `dependsOn` (deploy order, canvas edges), plus the edges derived from `${{key.VAR}}` references;
- a canvas group named after the template, and a `template_instances` row (the stack, deletable as a unit);
- one audit event `template.install`.

Deploying a stack uses *deploy staged* for exactly those services: providers deploy first (references and `dependsOn`); a one-shot provider must finish before its dependants start.

## Format (schema 1)

Strict YAML: an unknown field is an error. One file per template, `templates/catalog/<id>.yaml`, file name equals `id`.

```yaml
schema: 1
id: umami                      # [a-z0-9-], equals the file name
revision: 1                    # bump when an install of the new text would differ (image, config, env, files, volumes...)
name: Umami
summary: One line (<= 200 chars).
description: Optional longer text.
category: web                  # homelab | media | storage | games | devtools | ai | data | web | analytics | automation | productivity | security
tags: [analytics, privacy]
icon: umami                    # simple-icons slug (optional)
links: {website: https://umami.is, docs: https://umami.is/docs, source: https://github.com/umami-software/umami}
upstream:
  license: MIT                 # SPDX id or name of the UPSTREAM APP (required)
  licenseUrl: https://…        # required for source-available and proprietary
  class: permissive            # permissive | copyleft | source-available | proprietary
origin: {type: original}       # or coolify|dokploy with path, commit, license (attribution; required)
requires: {memoryMb: 512, diskGb: 2, arch: [amd64, arm64]}   # arch defaults to both; list only what the images support
heavy: false                   # true: several GB / lots of RAM / slow first start (see "Heavy templates")
requiresUnrestricted: false    # true: a service uses devices, GPUs or another unrestricted-only option; installs only on an unrestricted server
inputs:                        # install form; used as ${input:KEY}
  - {key: ADMIN_EMAIL, label: Admin email, format: email, required: true}
  - {key: ADMIN_PASSWORD, label: Admin password, secret: true, default: "${{secret(20)}}"}
services:                      # 1..8, ordered
  - key: app                   # [a-z0-9-]; `${{app.VAR}}` references use it
    name: Umami
    kind: app                  # app (default) | worker | database
    image: ghcr.io/umami-software/umami:3.4.1   # explicit, non-floating tag
    digest: sha256:…           # multi-arch index digest; set by tools/template-pin
    port: 3000                 # http port (0 = none)
    command: [..]              # optional start command (argv)
    healthcheck: {type: http, path: /api/heartbeat, startPeriodSeconds: 15, deadlineSeconds: 180}
    env:
      DATABASE_URL: ${{db.DATABASE_URL}}
      APP_SECRET: ${{secret(64)}}
      TITLE: ${input:TITLE}
    secrets: [OTHER_KEY]       # env keys that are secret although they have no generator
    volumes: [{name: data, path: /app/data}]
    files: [{path: /etc/app/config.toml, mode: "0444", content: "…"}]
    ports: [{name: game, port: 25565, protocol: tcp, publish: true}]
    domains: [{generate: true}]          # or {host: "${input:DOMAIN}"}; max one
    dependsOn: [db]
    oneshot: false             # run-to-completion job (migrations): exit 0 = success, never restarted
    resources: {memoryMb: 512, cpus: 1}
    docker:                    # container options, typed, same names as a service's Docker options (see "Docker options")
      entrypoint: [/bin/sh, -c]
      shmSizeMb: 512
      ulimits: [{name: nofile, soft: 65536, hard: 65536}]
    stopGraceSeconds: 60
  - key: db
    name: Umami DB
    kind: database
    database: {engine: postgres, version: "17", databaseName: umami}   # managed database; version must be one the engine catalog offers
group: Umami                   # canvas group title (default: name)
notes: ["Default login admin / umami: change it."]
```

### Categories and licence classes

The gallery has twelve categories (ids in the `category` field; the order below is the display order): `homelab` (dashboards, networking), `media`, `storage` (files, sync, backups), `games`, `devtools`, `ai`, `data` (databases, search, brokers, backend as a service), `web` (CMS, wikis, shops, forms), `analytics` (web analytics, uptime, observability), `automation`, `productivity` (notes, tasks, documents, chat, mail, ERP) and `security` (identity, SSO, password managers). A template has exactly one.

`upstream.class` says what the upstream licence allows, and the gallery filters on it:

| Class | Meaning | Examples of licences |
|---|---|---|
| `permissive` | Permissively licensed, without copyleft | MIT, Apache-2.0, BSD, ISC |
| `copyleft` | Copyleft: derivative works must stay open | GPL, AGPL, LGPL, MPL, EUPL |
| `source-available` | The source is public but the licence is not OSI-approved, or open-core code is restricted (needs `licenseUrl`; the UI warns) | BUSL, SSPL, Elastic, FSL, Sustainable Use |
| `proprietary` | Closed source that is free to run (needs `licenseUrl`; the UI warns) | Plex |

The gallery's first licence chip covers `permissive` plus `copyleft`. The class describes the app the image runs, not the image's base layers and not the repository's docs.

### Inputs and formats

An input is `text` by default, a password field when `secret: true`, and a select when it has `options`. `format:` makes it a typed field: `email`, `url` (http or https), `hostname` (DNS name or IP, no scheme or path) or `number` (whole number). The form renders the matching browser field, and the install and the plan refuse a non-empty value that does not fit with 422 `invalid_input`. Rules the validator enforces: the format is one of those five, a default must fit it, secret and select inputs stay plain text, and a **required** input without a default whose key ends in `_EMAIL`, `_URL` or `_URI` must declare the matching format.

### Managed database versions

`database.version` must be a version the engine catalog (`internal/databases/engines`) offers: for example postgres 16-18, mysql 8.4 and 9.7, mariadb 11.8 and 12.3, valkey 8 and 9, redis 8, mongodb 7 and 8. Anything else fails an install with `invalid_database`, so the validator (and with it every tier-1 test) rejects it. A template that needs another version or a database user other than `postgres` uses an image service with `kind: database` instead.

### Variables

Env values use the variable grammar:

| Syntax | Meaning |
|---|---|
| `${input:KEY}` | install-time input (expanded when installing; also allowed in `files[].content`, `command`, `domains[].host`) |
| `${{key.VAR}}` | variable of another template service (rewritten to its real slug at install) |
| `${{VAR}}` | variable of the same service (including platform variables such as `${{NEBULA_PUBLIC_URL}}`, `${{NEBULA_PRIVATE_HOST}}`, `${{PORT}}`) |
| `${{secret(32)}}`, `${{randomInt(1000,9999)}}`, `${{uuid()}}`, `${{randomBytes(32,"hex")}}` | generated once at install |
| `$${{…}}` | a literal `${{` |

Managed databases export `DATABASE_URL`, `DATABASE_HOST`, `DATABASE_PORT`, `DATABASE_NAME`, `DATABASE_USER`, `DATABASE_PASSWORD` (SQL engines) or `REDIS_URL`, `REDIS_HOST`, `REDIS_PORT`, `REDIS_PASSWORD` (valkey/redis; valkey also `VALKEY_URL`). Reference them as `${{db.DATABASE_URL}}`. Engines: `postgres`, `mysql`, `mariadb`, `valkey`, `redis`, `mongodb`, `clickhouse` (versions are majors as strings, e.g. postgres `"17"`, valkey `"9"`). Use a managed database whenever the app can use one of those engines; use an image service with `kind: database` for anything else (for example a vector-enabled Postgres build).

Environment keys named like credentials (`*PASSWORD*`, `*SECRET*`, `*TOKEN*`, `*API_KEY*`, `*SALT*`…) must not have a literal value: use a generator, an input or a reference.

### Docker options

A service may carry a `docker:` block. It is the same typed set a service's own Docker options take (`docs/operations/docker-options.md`), with the same JSON names (`entrypoint`, `shmSizeMb`, `ulimits`, `capAdd`, `capDrop`, `sysctls`, `hostSysctls`, `user`, `groupAdd`…), strictly decoded (an unknown key is an error) and validated by the very same validator at catalog load, so a template can never carry an option a service could not. An install writes the block into the staged service's configuration; it is an ordinary, editable setting afterwards.

- Free text values (an `entrypoint` script, `user`, `groupAdd`) may use `${input:KEY}`; a **secret** input is refused there, because the block is stored in clear text like `command` and `files`.
- An `entrypoint` replaces the image's ENTRYPOINT and drops the image's default command; `command` becomes its arguments. The way to say compose's `entrypoint: []` is to put the command's program into `entrypoint` and keep the rest in `command`.
- A managed database (`database:`) takes no docker block.
- `readOnlyRootfs` needs every `files` entry on a volume.
- **Unrestricted-only options** (`devices`, `gpus`, `runtime`, `privileged`, `pidHost`, `seccompUnconfined`, `apparmorUnconfined`, and the capabilities `NET_ADMIN` and `SYS_ADMIN`) hand a container the host's hardware or root. A template using one must say `requiresUnrestricted: true` at the top level, and the validator checks the flag is set exactly when one is used. The gallery marks such a template, the plan shows a failed `unrestricted` check on any other server, and the install is refused with 422 `requires_unrestricted_server` (an instance admin makes a server unrestricted in its settings). The catalog ships hardware variants as separate templates for that reason: `jellyfin-hwaccel` (the render device) and `ollama-with-open-webui-gpu` (NVIDIA GPUs).
- `hostSysctls` (for example `vm.max_map_count: 262144` for Elasticsearch) is a host minimum that nothing applies: the deploy plan warns, with the command to run, when the server's value is lower.

### Health checks

Every non-one-shot service needs one (`none` is not allowed in the catalog).

- `http` / `tcp`: probed by the agent from outside the container (no curl in the image needed).
- `exec`: `command: [CMD, pg_isready, -U, app]` or `[CMD-SHELL, "…"]`, executed inside the container by Docker; the agent waits for it to report healthy.
- `docker`: use the image's own `HEALTHCHECK`.

### Files

`files` are written into the container before it first starts (tar copy). Content is stored in plain text in the service config; never put secrets in files. Paths are absolute without `..`; at most 16 files, 64 KiB each. A service with files is not scaled beyond one replica.

### Ports and game servers

`ports` declares non-HTTP container ports. With `publish: true`, the install creates a TCP or UDP proxy route on the same host port (open to the internet; the proxy restarts briefly the first time a new port is added on a server). Install fails with `port_in_use` when another route on that server already owns the port; the plan reports it first. Ports below 1024 cannot be published.

### One-shot services

`oneshot: true` services run once per deploy (database migrations, seed jobs). They have no port, domain or health check; exit code 0 is success, anything else fails the deployment. Services that depend on them (via `dependsOn`) wait for it to finish.

## Curation rules

A template ships only when every item holds:

1. The image is published by the project itself (or is a Docker Official Image) on Docker Hub, GHCR or Quay. No community forks of dead projects.
2. Pinned `tag` **and** `digest` of the multi-arch index, verified with `go run ./tools/template-pin` (a registry manifest check; it never pulls). Floating tags (`latest`, `stable`, `main`…) are rejected by the validator.
3. `upstream.license` is present and classified. **Source-available** apps (n8n, Directus, Outline, …) are shipped with `class: source-available` and a `licenseUrl`; the UI shows a badge and the licence link.
4. A real health check and sensible ports, volumes and resource hints.
5. Static validation passes (`go test ./internal/templates/`), and a sample of templates is deploy-tested in `tests/integration` (images are pulled, tested and removed again). The digests of the whole catalog are checked against the registries with manifest requests only (`NEBULACTRL_TEMPLATE_REGISTRY_CHECK=1`); no image is pulled to pin or verify one.

Translated templates carry an `origin` block (`type`, `path` into the upstream repository, `commit`, `license`) and are covered by `templates/NOTICE`, which carries the upstream licences and states what NebulaCtrl changed. They are reviewed by hand; the translator never runs at runtime.

Reasons a draft is **rejected** and never ships (the rejected ids and their reasons are kept in `REJECTED.txt` in the translate tool's local, uncommitted working directory `templates/drafts`): the image is not published by the project (community repackaging, wrapper images) unless the project's own docs recommend it; the upstream is archived or idle for 12 months; only floating tags exist; it needs host bind mounts, the Docker socket, privileged mode, devices, host networking, wildcard DNS or a second public domain; it needs an out-of-band bootstrap that cannot be expressed as inputs and files; it needs more than 8 services; or there is no health check that proves the app started. Self-hosted edge stacks (a reverse proxy with its own Traefik and gateway) are rejected for a structural reason, described in the next section.

### Edge stacks (why a template cannot be its own edge)

A template turns into ordinary services on the environment network behind the agent's Traefik, which owns 80/443. A stack that **is** its own edge, a tunnelling reverse proxy that ships its own Traefik and WireGuard gateway, cannot be expressed that way, and the blockers are concrete:

- Such a stack generates Traefik configuration through an HTTP provider and authenticates every resource with a plugin that is declared in Traefik's *static* configuration. The agent's Traefik is configured only through its REST provider and cannot load either, so the stack's dashboard could sit behind our proxy but the resources it exists to publish could not.
- Its own Traefik must therefore bind 80/443. Templates cannot publish ports below 1024, and a service never publishes host ports at all: only TCP/UDP routes (1024-65535) through the agent's Traefik exist.
- In the upstream compose file Traefik runs with `network_mode: service:<gateway>`, because it reaches the sites' WireGuard addresses on `wg0` inside the gateway's network namespace. Service containers only join the environment network, so that topology cannot be created from a template (a [Compose stack](compose-stacks.md) may use `network_mode: service:<other service>`, but stacks never publish host ports and templates cannot create stacks).
- The gateway needs `NET_ADMIN` (unrestricted servers only; `SYS_MODULE`, in the upstream compose file, is never allowed) and two UDP ports on the host. A UDP route through the agent's proxy makes the upstream see the proxy's own socket instead of the peer's address, and the relay and NAT hole punching work from the peer's source address.

Product changes that would make it possible, in order of size: a service option to share another service's network namespace (unrestricted servers only); a per-server **external proxy** mode (the agent does not start Traefik, or moves it off 80/443, and a service may publish host ports directly, unrestricted servers only); and template support for a Compose stack. Until then, run such a stack on its own host with its own installer and connect the sites that need it with its connector from NebulaCtrl-managed or any other hosts. A connector template is not shipped either: it needs a site ID and secret created in a running instance of the stack (the same out-of-band bootstrap that keeps `tailscale-exitnode` out), and the connector image keeps changing.

### Heavy templates

`heavy: true` marks a template whose install downloads several GB (large images, models, game files), asks for 4 GB of RAM or more (the catalog test enforces this) or takes many minutes to start: azuracast, gitlab-ce, the AI stacks, the Steam game servers. The gallery card shows a "Heavy" badge and the plan warns. **Heavy templates are never part of the integration sample and need a manual check**: deploy them yourself on a server with the room, or name them in `NEBULACTRL_TEST_TEMPLATES`.

Three static rules guard every template in `go test ./internal/templates/` (tier 1, no network): the catalog has at least 300 valid templates and uses every category; every one has a licence class (with `licenseUrl` for the restricted classes), an origin that points into the upstream catalog at a commit, a pinned `tag@digest` (the all-zero placeholder is rejected) and no leftover draft marker; and every template survives the snapshot encoding the update flow depends on (below).

## Versions and updates

A template has two version numbers. The **revision** (`revision:`) counts the template's own changes: bump it whenever installing the new text would produce something different (a new image tag, another health check, a changed variable, file or volume); metadata-only edits (summary, tags, category, icon, links) do not need one. The **app version** is not written by hand: it is the tag of the template's main image (the first image service that is not a one-shot job or a database), for example `3.4.1`, shown on the card.

An install records both (`template_instances.template_revision`, `template_version`) and a **snapshot** of the template as installed (`template_snapshot`, the template text with its `${input:KEY}` placeholders and generators, never an input value, so no secret). The catalog is embedded in the server binary, so after an upgrade the older revision exists nowhere else; the snapshot is what an update diffs against.

**Update available.** When the catalog's template differs from a stack's snapshot, the stack's group header on the canvas and the service's Settings tab show an update marker. It opens a diff (also `GET /template-instances/{id}/update` and `nebulactrl templates update <instance>`), with every change in one of four states:

| State | Meaning |
|---|---|
| `apply` | Will be staged: the service still has what the old revision installed |
| `kept` | Left alone because you changed the field on the service (image, a health check setting, a file you edited, a variable you changed or removed); a secret that exists is never overwritten |
| `manual` | Never done by an update: new or removed services, a database engine or version (use the database's Upgrade action), published ports and domains, removed variables, a changed volume mount path or a removed volume, and values built from an input whose value is stored only as a secret |
| `applied` | The service already has the new value (a retry after a partial update) |

**Stage update** (`POST /template-instances/{id}/update`, `--apply` in the CLI) writes the `apply` changes as ordinary staged edits through the normal service paths (service update with its validation, variables with generated secrets, volumes), so the staged bar shows them and **Deploy** applies them in the usual way; nothing deploys by itself, and rollback works as always. Inputs the new revision added are asked for in the dialog (required ones must be filled; generated ones are generated). The request carries the revision you reviewed: a newer one in the catalog fails with 409 `template_changed`. A failure part-way is safe to retry, because anything already applied shows as `applied`. The instance then records the new revision, version and snapshot (kept and manual items do not come back as updates), and an audit event `template.update` lists what was staged.

Installs made before updates existed have no snapshot: they show "newer revision available (reinstall)" and cannot be updated in place.

## What the tests deploy

Static validation covers the whole catalog on every run. `TestTemplatesDeploySample` (full tier) installs templates through the API, deploys them, probes the generated domain and removes everything again, and a run is bounded: a **rotation of 3 templates per day** (non-heavy, deterministic, every one is reached in a few months) plus **at most 8 of the templates changed against origin/main**, picked deterministically and rotating by day so a big change set is covered over several runs. Heavy templates are left out. The test logs which templates it deployed and which it skipped. Required inputs are filled with realistic values of their format (`ops@nebulactrl.dev`, `https://example.nebulactrl.test`, `host.nebulactrl.test`, `587`, a select's first option, a generated-style password). `NEBULACTRL_TEST_TEMPLATES` overrides the choice: a comma separated list of ids, or `all`.

## Tools

```
go run ./tools/template-pin templates/catalog                  # verify digests (no pulls)
go run ./tools/template-pin -update templates/catalog/x.yaml   # resolve tag → digest, rewrite the file
go test ./internal/templates/                                  # schema + pinning + licence for the whole catalog
NEBULACTRL_TEMPLATE_REGISTRY_CHECK=1 go test -run Registry ./internal/templates/
./tests/run.sh full -run '^TestTemplatesDeploySample$'              # deploy a bounded sample (below)
NEBULACTRL_TEST_TEMPLATES=umami,ghost ./tests/run.sh full -run '^TestTemplatesDeploySample$'   # exactly these (heavy ones too)
NEBULACTRL_TEST_TEMPLATES=all ./tests/run.sh full -run '^TestTemplatesDeploySample$'           # exhaustive sweep, hours
go run ./tools/template-translate …                            # one-time draft generator, see tools/template-translate/README
go run ./tools/template-translate -enrich <drafts> -enrich-out <dir>   # tags, digests, licences, rejections (no pulls)
go run ./tools/template-translate -check templates/catalog/x.yaml      # static validation of one file
go run ./tools/template-translate -resolve redis:7.4                   # digest and platforms of an image (HEAD request)
```

## In the web app

- **Create → Templates** (or ⌘K → "New from template…", or the "Browse templates" button on an empty canvas) opens the gallery: search (every word must match the name, summary, tags, licence or database engine; names rank first), twelve category chips and three licence chips (permissive and copyleft, source-available, proprietary), each with a count that follows the search and the other filter, and a card per template (services, memory, database engines, licence, a TCP/UDP marker for game servers). A link `…/p/<project>/<env>?template=<id>` opens the gallery on that template.
- A card opens the plan step. The left side describes the template and lists the services it creates; the right side is the install form (secret inputs are password fields and say when they are generated if left empty), the server picker with its CPU, memory and disk headroom, and a live preflight from the plan endpoint that re-runs when the server or an input changes. **Add to canvas** stages the stack; **Add and deploy** also deploys it. Both stay disabled until a fresh preflight has no failed check. **Source-available** and **proprietary** templates carry a warning badge with the licence name and link.
- After the install the canvas takes the group and the card positions the control plane saved, and fits the new group into view.
- A group that is a template stack has **Update** while the catalog has a newer revision (it opens the diff above), **Deploy stack** (deploys what is staged in it; in production it opens the review sheet) and **Delete stack** (type the template id, optionally delete the volumes, hold to confirm) in its header. A service's Settings tab says which template it came from.
- Service Settings can edit what templates configure: an exec health check (`CMD` or `CMD-SHELL` first), **Files** (at most 16 of 64 KiB, stored in plain text: never put secrets there) and the **one-shot job** switch.

## CLI and MCP

```
nebulactrl templates ls [--category games] [--license source-available] [--search minecraft]
nebulactrl templates show n8n
nebulactrl templates plan umami --project acme --env production --server box1 [--input KEY=VALUE]
nebulactrl templates install umami --project acme --env production --server box1 [--deploy]
nebulactrl templates installed --project acme --env production      # shows version, revision and UPDATE
nebulactrl templates update <instance-id> [--apply] [--input KEY=VALUE]   # diff, or stage it
nebulactrl templates rm <instance-id> --confirm umami [--delete-volumes]
```

MCP (when it is [switched on](../reference/mcp.md#turn-it-on)): `list_templates` and `plan_template` (scope `projects:read`), `install_template` (`projects:write`; `deploy: true` also needs `deployments:write`, published ports need `proxy:write`). They call the same service methods as REST and are audited as `mcp`.

## Install semantics worth knowing

- **Everything is staged.** Installing never deploys unless `deploy: true`. The staged bar then shows N staged services; Deploy applies them in dependency order.
- **Secrets are generated once**, at install time, and stored encrypted. Inputs with generator defaults (for example an admin password) are generated once and reused wherever the template references them. The plan lists secret names only.
- **The stack is not special afterwards.** Editing, redeploying, rolling back or deleting one service uses the normal service paths. `Delete stack` removes every service of the install, its published ports and its canvas group.
- **Different servers are not supported**: all services of a template land on one server (private networking is per host).
- **Image digests** are pinned in the installed service (`repo:tag@sha256:…`), so a template revision never changes what an existing install runs; only staging an [update](#versions-and-updates) does, and only as a staged change you deploy.
- **Docker Hub rate limits** apply to the pulls of a stack (anonymous pulls are limited per IP); give the service registry credentials if a pull fails with `toomanyrequests`.

## API

`GET /api/v1/catalog/templates?q=&category=&license=`, `GET /api/v1/catalog/templates/{id}`, `POST /api/v1/environments/{id}/templates/{templateId}/plan`, `POST /api/v1/environments/{id}/templates/{templateId}/instances`, `GET /api/v1/environments/{id}/template-instances`, `GET /api/v1/template-instances/{id}/update` (diff), `POST /api/v1/template-instances/{id}/update` (`{revision, inputs}`, stages it; needs `projects:write`), `DELETE /api/v1/template-instances/{id}?confirm=<templateId>&deleteVolumes=<bool>`. Installing needs `projects:write` (plus `proxy:write` for templates that publish ports); `deploy: true` additionally needs `deployments:write`.
