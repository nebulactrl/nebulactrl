---
title: Clone and move
description: "Copy a service or a whole environment, and move a service to another environment."
section: Applications
order: 95
---

# Clone and move

Three operations rearrange what you already run:

| | What it does | Where |
|---|---|---|
| **Clone a service** | A staged copy of one service, in the same or another environment. | Service menu → **Clone**, `nebulactrl services clone`, `POST /services/{id}/clone` |
| **Clone an environment** | A new, non-production environment with a staged copy of every service. The usual "staging from production". | Environment switcher → **Clone environment**, `nebulactrl envs clone`, `POST /environments/{id}/clone` |
| **Move a service** | Re-homes a service in another environment, staged until its next deploy. | Service menu → **Move to environment**, `nebulactrl services move`, `POST /services/{id}/move` |

Nothing runs because of a clone: the new services are staged like any new service
and start when you deploy them. Both clone operations and the move accept
`dryRun` (`--dry-run`): they report what they would do, including conflicts, and change
nothing.

You need write access (`projects:write`) to the source **and** to the target
environment, because a clone carries the source's secrets. A token restricted to
one project cannot clone out of it into another.

## What a clone copies

| | |
|---|---|
| Configuration | Image or repository, build settings, port, health check, start command, resource limits, [Docker options](docker-options.md), pre- and post-deploy commands, files. A Tailnet setting is **not** copied (one tagged device per service). |
| Server | The source's server, or the one you pick (`serverId`). A clone on another server is checked like any new service (online, accepting workloads, Docker options allowed there). |
| Variables | All of them. **Secrets are copied sealed**: each value is opened and sealed again for its new row inside one transaction; no value appears in a response, an audit entry or a log, and the clone's ciphertext is not a copy of the source's. Variables with a generator (`${{secret(32)}}`, `uuid()`, …) are **generated again**, so a staging copy never shares a secret with production. |
| References | See [references](#references-and-slugs). |
| Credentials | The Git token, the registry login and the SSH deploy key (with its pinned host keys) are sealed again for the clone. A git clone is linked to the same repository with **auto-deploy off**; switch it on in the service's Git settings. |
| Volumes | The **definitions** (name, mount path) are copied; the volumes are **new and empty**. Data is not copied (see [volume data](#volume-data)). |
| Databases | A managed database is created **new and empty**: same engine, version and tuning, **new credentials**, an empty data volume. Nothing from the source database (data, credentials, extra variables) is copied. The result carries a note: [restore a dump](database-backups.md#restore-from-a-dump-file) into it. A read replica is not cloned; add a replica to the cloned primary instead. |
| Domains | Never duplicated, because hostnames are unique. See [domains](#domains). |
| Links | A service clone in the **same** environment gets the links the source draws to other services (not the links other services draw to the source). An environment clone copies every link between the cloned services. A clone into another environment copies no link. |
| Environment extras | An environment clone also copies the environment's shared variables (sealed again, generated ones regenerated) and its canvas layout. |

Not copied: backup schedules, cron tasks, deploy hooks, shared-network
attachments (a staging copy must not join production's network), maintenance
mode, replica counts and autoscaling, release and deployment history. Route
services and Compose stacks are not cloned: an environment clone lists them as
skipped; cloning one on its own is refused with `not_applicable`.

### References and slugs

A **service clone** in the same environment gets a new slug (`api` becomes
`api-copy`). References *to the clone itself* in its own variables follow it
(`${{api.PORT}}` becomes `${{api-copy.PORT}}`); references to other services
stay, so the clone talks to the same siblings the source does.

An **environment clone** keeps every slug, so a reference such as
`${{postgres.DATABASE_URL}}` in the cloned `api` resolves to the **cloned**
`postgres`, with the clone's own credentials. The clone is self-contained: no
cloned service points back at production.

When a clone goes to **another environment**, the references of its variables are
checked there. The result lists every reference that does not resolve (a service
that is not in the target environment, a shared variable that does not exist),
so you can add the missing service or change the variable before deploying.

### Domains

A hostname can belong to one domain rule only, so a clone never gets its source's
domains. Choose:

| Mode | Result |
|---|---|
| `drop` (default) | The clone has no domain. Attach new ones in the Networking tab. |
| `suffix` | Each public domain is cloned with a suffix on its first label: `app.example.com` becomes `app-copy.example.com` (same environment), or `app-<environment slug>.example.com` (another environment, or an environment clone). Set `domainSuffix` to choose another. Path rules, port and the other options come along, a custom certificate does not (the clone asks for its own), and basic-auth users are copied (hashes sealed again), so a protected site stays protected. |

Generated domains (`<service>-<env>-<project>.<base>`) are regenerated for the
clone's own name and environment. Redirects, apex domains (they cannot be
suffixed) and Compose component domains are skipped and listed.

A hostname that is already taken is a **conflict**: the whole clone fails with
`409 domain_conflict` (`details.hostnames`) and nothing is created. Choose another
suffix, or drop the domains. A dry run reports the conflicts without failing.

### Volume data

Cloning does not copy volume data. NebulaCtrl's backup and restore machinery is
per service: a volume backup goes to that service's own repository and restores
only into the same service, and database restore creates its sibling from a
backup of the source. Copying data would mean taking a backup, restoring it into
the clone and waiting for both, which is not a cheap side effect of a clone. To
bring data over:

- **Databases:** restore a dump into the cloned database (the database's
  **Backups** tab → **Restore from a file**, or `nebulactrl databases restore`;
  [restore from a dump file](database-backups.md#restore-from-a-dump-file)), or restore a database backup **as a new
  database** ([Database backups](database-backups.md)) instead of cloning.
- **Other volumes:** back the source volume up and restore it into the clone with a
  [volume backup](backups.md) once the clone has its own destination, or copy the
  files with the web terminal.

## Moving a service

A move changes the environment of a service. It is **staged**: the service keeps
running in its old environment until you deploy it. That deployment joins the new
environment's network and resolves its variables there. Domains, volumes, data,
variables and the release history stay with the service. The service's
**Pending changes** list the move as `environment: Project / old → Project / new`.

Before anything is written the move is checked:

| Check | Result |
|---|---|
| Variables of the service that reference services staying behind (`${{postgres.DATABASE_URL}}`), or project/environment shared variables that do not exist in the target | Listed as `outgoing`. They **break**: the next deploy fails with `unresolved_reference` until you fix them. |
| A variable that references a service the target environment also has (a `postgres` in both) | Listed as `rebound`: it would silently point at the other environment's service. |
| Other services of the old environment that reference the moved one | Listed as `incoming`. They break, and are marked as having staged changes so a deploy of the environment shows it. |
| Canvas links of the service | Removed (listed); draw them again in the new environment. |
| The target already has a service with the same slug | The moved service gets a free one (`api-2`); references to itself are rewritten. |
| A deployment of the service is running | `409 deployment_in_progress`. |
| The service is in a replica group or has a cross-server link | `409 move_blocked`: remove those first. |
| Route services, Compose stacks, the platform project, pull-request previews | Refused (`not_applicable`, `preview_environment`). |

A move that breaks references is refused with `409 move_blocked` (the plan is in
`details.plan`) unless you pass `force` (`--force`; the web app asks you to
confirm). Moving across **projects** of the team is allowed; the result warns that
project-level shared variables, git connections and team settings follow the new
project. Moving a service out of or into a **production** environment needs
approval for guarded clients (AI tokens), like other production changes.

Undo a move by moving the service back.

## In the web app

- **Clone a service:** open the service, then the **⋯ menu → Clone**. Pick the
  environment (default: this one), a name, the server and the domain mode; **Check**
  runs the dry run and shows conflicts, unresolved references and notes; **Clone**
  creates the staged copy and opens it.
- **Move a service:** **⋯ menu → Move to environment**. The dialog shows the plan
  (broken references, removed links, a new slug) before you confirm; the service
  then shows a *Staged* change you apply with **Deploy**.
- **Clone an environment:** the environment switcher's **Clone environment**
  asks for the new name, the domain mode and optionally one server for every
  clone, shows the services and notes, and opens the new environment.

## CLI

```bash
# A staging copy of production; domains get -staging
nebulactrl envs clone production --project shop --name Staging --domains suffix

# Clone one service (staged); --dry-run shows what would happen
nebulactrl services clone shop/production/api --env staging --dry-run
nebulactrl services clone shop/production/api --env staging --domains suffix

# Move: check, then do it
nebulactrl services move shop/production/api-copy --env staging --dry-run
nebulactrl services move shop/production/api-copy --env staging --force
```

All three print JSON with `--json`.

## API and MCP

`POST /services/{serviceId}/clone`, `POST /environments/{environmentId}/clone` and
`POST /services/{serviceId}/move` (see the [API reference](../reference/api.md)).
Both clones honour `Idempotency-Key`. Over MCP they are generic operations
(`find_operation` / `call_operation`): cloning is a creation like `createService`,
and `moveService` is production-guarded when the source or target environment is
production. Secrets never appear in any of the results.

## Audit

`service.clone`, `environment.clone` and `service.move` are recorded with the
source, the target and counts (services, forced, broken references), never with
values.

## Limits

- No data is copied (see [volume data](#volume-data)); databases start empty.
- Clones stay inside one team. A clone is never production, and never joins
  shared networks, the tailnet, or the source's schedules.
- Route services, Compose stacks and read replicas are not cloned; routes,
  stacks, replica-group members and services with cross-server links cannot be moved.
- Cloning a service whose source is on a server that is offline works (the clone
  is only staged), but deploying the clone needs its own server online.
