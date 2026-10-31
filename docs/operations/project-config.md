---
title: nebula.toml guide
description: "Validate, plan, apply; git integration; drift; AI authoring rules."
section: Applications
order: 150
---

# nebula.toml: your project as a file

`nebula.toml` describes a project's services, domains, variables (names and references, never secret values), tasks, uptime monitors, backup schedules, scaling, volumes, files and per-environment overrides in one file you keep in your repository. Everything it can say, the dashboard can say too: applying the file **stages** the same changes a dashboard edit would, and applies the settings that take effect at once. It never deploys on its own.

Every key is listed in the [nebula.toml reference](../reference/nebula-toml.md). Editors that read JSON Schema complete and check the file from `/schema/nebula.toml.json`; put `#:schema <your-address>/schema/nebula.toml.json` on the first line.

`railpack.json` is a different file: the builder reads it from your repository to decide how to build the image. `nebula.toml` decides what runs, where, and with which settings.

## A small file

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

Exactly one source key per service (`image`, `git`, `database`, `route` or `stack`) decides its kind. Scalars go before sub-tables in every TOML table.

### Switching between an image and a Git repository

A file declares exactly one of `image` or `[services.<name>.git]` for an app. Change which one and the next apply **changes the service's source**: the plan lists it as one staged change `source` (for example `image → git`), it applies on the next deploy, and applying never deploys. Domains, variables and volumes stay.

- The old source's own settings are cleared. Registry credentials and a git token are never in the file, so they cannot carry over: the old ones are dropped, and a private repository or registry needs them entered again in the dashboard.
- Link a connected account with `git.connection` and `git.repo`. The link is an immediate field, not part of the staged switch; a file with only `git.url` leaves the service unlinked.
- Discarding the staged change restores the previous source settings, not the cleared credentials or link. See [change a service's source](builds.md#change-a-services-source).
- Compose stacks, databases and routes are refused with `source_type_change`: the file cannot turn an app into one of them, or one of them into an app.

## The lifecycle: validate, plan, apply, deploy

| Step | Command | What happens |
|---|---|---|
| Validate | `nebulactrl config validate` | Reads the file and reports every problem with `file:line:col`, a stable code and "did you mean" suggestions. `--offline` checks the file alone. Exit code 1 if anything is wrong. |
| Plan | `nebulactrl config plan --env production` | Compares the file with the environment and lists every change, marked **staged** or **applies at once**, and why it is there. Read-only. `--detailed-exit-code` returns 3 when changes are pending. |
| Apply | `nebulactrl config apply --env production` | Re-plans and stages the changes in one transaction, then applies the immediate settings. Pass `--plan-id` to fail if anything changed since the plan. |
| Deploy | `nebulactrl config apply --deploy`, or the Deploy key | Staged changes take effect on the next deploy, like any dashboard edit. |

Staged changes are visible in the staged bar, with `set by nebula.toml` beside the lines the file owns, and you can discard them. Settings that act on a running service (scaling of the service and of each process, tasks, backup schedules, uptime, a git link, DNS records) apply at once, like their dashboard controls.

The same calls exist in the API (`validateProjectConfig`, `planProjectConfig`, `applyProjectConfig`, `exportProjectConfig`) and, for agents, as one MCP tool, `sync_config`, in the `config` toolset (`?toolsets=core,config`): its `action` is `export`, `plan` or `apply`, and `plan` includes the validation.

## Ownership and drift

- A key present in the file (after the environment's overrides) is **owned** by the file. A key you leave out is not touched.
- Removing a key **releases** it: the live value stays, the file stops managing it. Removing a service from the file never deletes it; the plan lists it as *not in file*.
- `--prune` additionally deletes only the variables, domains, volumes, networks, links, tasks, processes and backup schedules the file owned before and no longer names. It never deletes services, databases or stored backups, and it needs `--yes`. Agents cannot prune.
- Editing an owned field in the dashboard is allowed. It shows as **edited here** (drift) with a file icon beside the control, and the next apply overwrites it. From the icon you can **Revert to file** or **Stop managing** that field.
- The comparison is three-way: what the last apply wrote, the file, and what is live. The plan says whether a change is *file changed*, *edited in dashboard* or *both changed*.

## Git integration

An environment reads its file from a repository: either set under **Environment → Config from git**, or derived when all its git services share one repository and branch. On every push the control plane reads the file at the pushed commit and marks the environment:

- **nebula.toml differs**: a banner above the canvas, with **Review**. The review dialog is the plan; **Stage changes** applies it without deploying.
- **invalid**: the banner shows the first error with its line and a link to the file at that commit.

In a monorepo keep one root file declaring all services; each service picks its code with `git.watch_paths`, `build.context` and `build.dockerfile`.

**Auto-apply** (per environment, off by default, needs the admin scope) stages a plan on push when it has no blockers, no prune and nothing privileged (Docker options that need an unrestricted server, `NET_ADMIN`, `SYS_ADMIN`). It never deploys, and it is never available in production. `auto_apply` is a setting of the control plane, not a key of the file: a repository cannot switch on its own automation.

## Production

Production never auto-applies: it gets the banner and the review. A person applies the plan. An agent working through MCP may stage changes like it does with `stage_changes`; if the plan has changes that apply at once, the production guard asks a human to approve that exact plan first.

## Start from what you have

`nebulactrl config export --env production > nebula.toml` (or **Export nebula.toml** in the environment menu) writes your running project as a file: defaults are left out, secrets appear as names only, and what the format cannot say is listed as a warning. `--all` factors every environment into a base plus `[environments.<slug>]` overrides. A fresh export plans to no changes. `nebulactrl config init` writes a starter file from the repository you are in.

## Secrets

Never write a secret value. Use `secrets.keys = ["NAME"]` (names only; set the value in the dashboard or with `nebulactrl vars set`), a generator such as `"${{secret(32)}}"`, or a reference such as `"${{db.DATABASE_URL}}"` or `"${{vault:integration/path#key}}"`. There are no keys for tokens, passwords, deploy keys or registry credentials. See [variables and secrets](variables.md).

## Writing the file with an AI assistant

<!-- ai-authoring-guide -->
1. **Start from an export of the live project** (`sync_config` with `action: "export"`, or `nebulactrl config export`), or `config init` for a new repo. Never write the file from memory.
2. **Always plan before you apply.** `sync_config` with `action: "plan"` checks the file and shows what it would change (CLI: `config validate`, then `config plan`). Read every diagnostic; fix errors by their `path`, `line` and `suggestions`. Apply only a plan with `valid: true`; pass its `planId`.
3. **Never write secret values.** Use `secrets.keys = ["NAME"]`, a generator (`"${{secret(32)}}"`) or a reference (`"${{db.DATABASE_URL}}"`, `"${{vault:…}}"`). Never put tokens, passwords, private keys or credentials in URLs. Never add keys for credentials: there are none.
4. **Use only keys from the schema.** Unknown keys are errors. Keys are `snake_case`; units are in the key name (`memory_mb`, `timeout_seconds`); all numbers are plain integers; variable values are always strings.
5. **One source key per service** (`image`, `git`, `database`, `route` or `stack`); the key decides the kind. Do not set `port`, `command` or `image` on a `database`.
6. **Scalars before sub-tables** in every TOML table; services and collections are tables keyed by name (`[services.web]`, `[services.web.domains."app.example.com"]`), not arrays.
7. **Tables merge by key; scalars and arrays replace.** An environment block restates whole arrays. To drop something in one environment use `unset`.
8. **Put hostnames, servers and per-environment values inside `[environments.<slug>]`** when the project has more than one environment.
9. **A key you write is owned by the file and will overwrite dashboard edits; a key you omit is left alone.** Do not rewrite fields you were not asked to change. Removing a service from the file does not delete it.
10. `sync_config` with `action: "apply"` **stages**; it never deploys. Review with `review_changes`, then deploy with `apply_changes`. In production, immediate changes need human approval; do not retry around it.
11. Do not set `auto_apply`; it is not a file key and agents cannot enable it.
<!-- /ai-authoring-guide -->

## Troubleshooting by code

Each problem carries a stable `code`. Fix the key at `path` (line and column are in the report).

| Code | Meaning and fix |
|---|---|
| `toml_syntax` | The file is not valid TOML. Look at the reported line; a scalar written after a `[table]` header belongs to that table. |
| `file_too_large`, `limit_exceeded` | A limit was passed (file 256 KiB, 64 services, 500 variables per service, 50 domains, 20 tasks, 9 processes besides the service's own). Split the project or remove entries. |
| `invalid_encoding` | The file must be UTF-8 without a BOM or NUL bytes. |
| `schema_unsupported`, `missing_required` | `schema = 1` is required; so are the keys named in the message. |
| `unknown_key`, `wrong_type`, `invalid_value` | A key not in the schema, a value of the wrong TOML type (variable values are strings), or a value out of bounds. Use the suggestion. |
| `invalid_slug`, `reserved_slug` | Service slugs are lower-case letters, digits and `-`; `shared`, `team`, `vault` and `doppler` are reserved. |
| `missing_source`, `conflicting_source` | A service needs exactly one of `image`, `git`, `database`, `route`, `stack`. |
| `not_allowed_for_kind`, `derived_field` | The key does not exist for this kind of service, or the platform derives it (a database's image and port). |
| `unset_in_base`, `invalid_unset_path` | `unset` works in environment blocks only, and its paths must exist in the base definition. |
| `credential_key`, `credential_in_url`, `literal_secret`, `secret_in_file`, `secret_has_value` | A credential was written into the file. Use `secrets.keys`, a generator or a reference. |
| `invalid_reference`, `unknown_service_reference`, `unresolved_reference`, `reference_cycle`, `invalid_template`, `reserved_variable` | A `${{…}}` expression is malformed, names a missing service, loops, or sets `PORT` or `NEBULA_*`. |
| `path_escape`, `attachment_missing` | A referenced file (`compose_file`, `files.*.source`, a Dockerfile path) leaves the repository or was not sent. |
| `unknown_environment`, `project_mismatch` | The environment or project named does not exist, or the file's `project` differs from the target. |
| `unknown_server`, `unknown_connection`, `unknown_registry`, `unknown_destination`, `unknown_network`, `unknown_certificate`, `ambiguous_reference` | A name in the file does not match exactly one object in the workspace. |
| `immutable_field`, `source_type_change` | `immutable_field`: a create-only key (database engine, route protocol) cannot change through the file. `source_type_change`: the file would turn a service into a different **kind** (a database, route or stack, or an app into one of those). Moving an app between `image = …` and `[services.<name>.git]` is allowed: it is a staged `source` change. |
| `hostname_in_use`, `invalid_upstream`, `upstream_denied` | Another service owns the hostname, or the route's upstream is not allowed on that server. |
| `process_command_required`, `process_name_reserved`, `process_port_conflict`, `process_singleton_scale` | A process needs its own `command`; its name cannot be the own process' name (`web`, or `process_name`); ports are unique across the service's processes; a `singleton` process runs one instance, so drop its `scale.replicas` above 1 or its autoscaling. See [processes](processes.md). |
| `license_not_accepted` | Set `accept_license = true` for a source-available database engine. |
| `docker_unrestricted`, `auto_apply_privileged` | The option needs an unrestricted server; auto-apply never applies it, a person must. |
| `cloudflare_not_connected`, `replicas_require_stateless`, `egress_blocked` | The matching dashboard prerequisite is missing. |
| `basic_auth_no_users`, `secret_missing_value`, `secret_flag_differs` | Warnings: set the missing value or users in the dashboard; the variable will be marked secret on the next apply. |
| `plan_stale`, `generation_conflict` | The file or environment changed since the plan. Plan again. |
| `insufficient_scope`, `approval_required` | The token lacks a scope, or production needs a person's approval of this plan. |
| `production_auto_apply_unsupported` | Auto-apply cannot be turned on in production. |
| `deferred` | Information: a setting will run once the service is deployed. |
