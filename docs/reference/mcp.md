---
title: MCP server
description: "Let an AI client run NebulaCtrl the way you would by hand: look, stage a change, apply it, with production changes approved by a person."
section: Reference
order: 30
---

# MCP server

NebulaCtrl can expose a Model Context Protocol (MCP) server so an AI client, such as a coding agent, can do most of what you do in the dashboard: find out what is wrong, change a service, add a database, set up a domain, deploy, roll back. It is **off by default**.

The MCP server is a layer over the same services the dashboard, CLI and REST API use. Scopes, project restrictions, roles, audit entries and the deployment rules apply unchanged. An agent can do nothing a token with the same scopes could not do through the [API](api.md), and a few things you can do by hand it can never do at all (see [what it cannot do](#what-it-cannot-do)).

## What an AI can do

| You want to… | The client uses |
|---|---|
| Know the state of everything, or what is broken | `get_overview`, `find_issues`, `get_recent_changes`, `search_resources` |
| Find out why a service or a deployment failed | `diagnose_service`, `explain_failed_deployment`, `diagnose_server`, `tail_logs` |
| Change a service (image, port, health check, variables, domains, volumes) | `stage_changes`, then `review_changes` and `apply_changes`. With the `config` toolset on, single edits such as `service_update`, `variable_set`, `domain_add` and `volume_add` are listed too. |
| Add a database or a domain in one step | `add_database`, `setup_domain` |
| Check that a deployment really works | `verify_service` |
| Describe a project as a `nebula.toml` file, check it, see what it would change and stage it | `sync_config` (`config` toolset) with `action` `export`, `plan`, then `apply` (stages only), and `review_changes` and `apply_changes` |
| Go back to the previous release | `rollback_service` |
| Back up and restore, run a one-off command, install a template | `create_backup`, `restore_backup`, `run_command`, `plan_template`, `install_template` |
| Reach anything else the API offers | `find_operation`, then `call_operation` |

The tools come in three layers:

1. **Workflow tools (16).** One call does what takes several clicks: `get_overview`, `find_issues`, `diagnose_service`, `explain_failed_deployment`, `diagnose_server`, `tail_logs`, `search_resources`, `review_changes`, `stage_changes`, `apply_changes`, `rollback_service`, `add_database`, `setup_domain`, `verify_service`, `get_recent_changes`, and for the [project file](../operations/project-config.md) `sync_config`, one tool whose `action` is `export`, `plan` (which includes the validation) or `apply`. They fetch in parallel, and when one part fails they return the rest with an `errors` list instead of failing everything.
2. **Single-purpose tools (about 60).** Named `noun_verb` (`service_list`, `variable_set`, `server_get`, `database_create`), with a short description and the four MCP hints (`readOnlyHint`, `destructiveHint`, `idempotentHint`, `openWorldHint`) set per tool, so a client can tell reading from deleting. Only the `observe` ones are listed by default; the rest are [opt-in toolsets](#toolsets). Large inputs, such as a service's `config`, point to `find_operation("updateService")` for the full field reference.
3. **The long tail through two tools.** `find_operation` (`query`, optional `tag`, `limit` up to 15) lists the operations the client may call, with name, summary, input schema and scopes. `call_operation` runs one by `operationId`: path, query and body arguments are one flat object validated against the operation's schema, `ifMatch` is an integer, and names such as `projectId` or `serviceId` also accept a selector. Operations on the [deny list](#what-it-cannot-do) are refused with `operation_denied` and their reason; missing scopes answer `forbidden` and name them; the production approval applies to everything.

Every result has structured content and a short text version, and lists are paged by cursor (`{items, nextCursor}`). `detail: "concise"` (the default) or `"full"` controls how much a read returns. Results are capped at 64 KB and say `truncated` when cut. A client can name things instead of passing ids: a project by slug or name, an environment as `project/env`, a service as `project/env/service`, a hostname, or a bare slug when it is unique. An ambiguous name fails with `ambiguous_selector` and the `choices` instead of guessing. Selectors need `projects:read`, so a deploy-only token must pass ids.

The server also publishes five prompts (`troubleshoot_service`, `explain_failed_deployment`, `estate_health`, `ship_a_change`, `author_config`) and four resources (`nebula://overview`, `nebula://service/{id}`, `nebula://deployment/{id}`, `nebula://schema/nebula.toml`, the JSON Schema of the project file), and its `instructions` tell the client where to start, how the staged workflow goes and the production rule.

## Turn it on

The instance admin (the first account created here) turns it on in **Admin → MCP** with the switch there. It takes effect at once, needs no restart, survives restarts and is recorded in the audit log (`instance.mcp.enable`, `instance.mcp.disable`). While it is off, `/mcp` answers `404`. Once it is on, **Account settings → MCP clients** shows the endpoint URL and a client configuration you can copy. Other people see whether it is on but cannot change it.

You can also decide it with the environment: when `NEBULACTRL_MCP_ENABLED` is set on the control plane (`true` or `false`), it decides, the switch in Admin → MCP is locked with a note saying so, and `PUT /api/v1/system/mcp` answers `409 mcp_locked`. Remove the variable and restart to hand the decision back to the switch.

## Connect a client

The endpoint is `/mcp` on your control plane, using the Streamable HTTP transport. It accepts an API token only, as `Authorization: Bearer nct_…`. Sessions and tokens in a query string are refused.

1. In **Account settings → API tokens**, create a token. Pick a preset (see [scopes](#scopes-and-token-presets)) and restrict it to the projects the agent should see.
2. Put the token in an environment variable on your machine, so it is not written into a config file you may commit:

   ```sh
   export NEBULACTRL_TOKEN=nct_…
   ```

3. Add the server to your client. Replace `nebula.example.com` with your control plane's address.

### Claude Code

```sh
claude mcp add --transport http nebulactrl https://nebula.example.com/mcp \
  --header "Authorization: Bearer $NEBULACTRL_TOKEN"
```

Or commit a project-scoped `.mcp.json` that reads the token from the environment:

```json
{
  "mcpServers": {
    "nebulactrl": {
      "type": "http",
      "url": "https://nebula.example.com/mcp",
      "headers": { "Authorization": "Bearer ${NEBULACTRL_TOKEN}" }
    }
  }
}
```

### Cursor

In `.cursor/mcp.json` (project) or `~/.cursor/mcp.json` (all projects):

```json
{
  "mcpServers": {
    "nebulactrl": {
      "url": "https://nebula.example.com/mcp",
      "headers": { "Authorization": "Bearer ${env:NEBULACTRL_TOKEN}" }
    }
  }
}
```

### Codex

In `~/.codex/config.toml`:

```toml
[mcp_servers.nebulactrl]
url = "https://nebula.example.com/mcp"
bearer_token_env_var = "NEBULACTRL_TOKEN"
```

### Any other client

Clients that read an `mcpServers` block with a URL and headers take the same shape as the Cursor example, with the token pasted in place of the variable. **Account settings → MCP clients** generates this snippet with your real endpoint.

### Offering more tools

A client is offered the `core` and `observe` tools by default, which is enough to look, diagnose, stage a change, apply it and roll back (the editing happens through `stage_changes`). For an agent that edits configuration, add `?toolsets=core,config` to the URL, for example in Claude Code:

```sh
claude mcp add --transport http nebulactrl "https://nebula.example.com/mcp?toolsets=core,config,observe" \
  --header "Authorization: Bearer $NEBULACTRL_TOKEN"
```

and the same URL in the `url` field of the Cursor and Codex configs. List every toolset you want: an explicit list is exactly those (see [toolsets](#toolsets)). Add `data` for databases and backups, `servers` for server management.

## Toolsets

A long tool list costs the model attention and context, so tools are grouped into toolsets. By default a client is offered **`core` and `observe` only** (about 32 KB of tool descriptions for an admin token); `config`, `data` and `servers` are opt-in:

| Toolset | Contains |
|---|---|
| `core` | The 15 workflow tools, `find_operation`, `call_operation` and `whoami`. Always the best place to start. |
| `config` | The project file tool `sync_config`, projects, environments, services, variables, domains, volumes, links, previews, tasks, `run_command`, templates, Compose import, and clone and move (`cloneService`, `cloneEnvironment`, `moveService` through `call_operation`; a move into or out of production needs approval). |
| `data` | Databases, backups and restores (including volume backups and schedules). |
| `servers` | Servers, the fleet overview, and draining a server. |
| `observe` | Deployments, service and server metrics and analytics, the audit log and the notification channel list (the last two need `admin`). |

Three things decide what a client sees:

- **Scopes.** A tool is listed only when the token holds every scope it needs. A read-only token never sees `apply_changes`.
- **Toolsets, set in the URL.** There is no per-token setting. Without `?toolsets=` the client lists `core` and `observe`. To opt in to more, name all you want: `https://nebula.example.com/mcp?toolsets=core,config,data,servers,observe`. An explicit list is exactly those toolsets (`core`, `config`, `data`, `servers`, `observe`; an unknown name answers `400 invalid_toolsets`), so `?toolsets=core` lists about 25 KB of core tools only. The URL cannot add anything the token's scopes do not allow.
- **The long tail.** Tools that are not listed stay reachable. With no `?toolsets=`, `find_operation` and `call_operation` reach every operation the token may use that is not denied, with the same scope and approval checks. With an explicit `?toolsets=`, they are restricted to those toolsets too, so the URL doubles as a fence.

## The staged workflow

Changes to a service, its variables, domains, volumes and links **only stage**. Nothing reaches a running container until the changes are applied, the same as in the dashboard, where staged changes wait behind the "N changes to apply" bar.

1. **Stage.** `stage_changes` takes the `service`, the `ifMatch` generation the client last saw, and any of `config`, `variables`, `domains` and `volumes`. A variable can be generated (`generate: {length, charset}`) or deleted. Each edit returns what changed: `{staged, generation, appliedGeneration, change, pendingCount, next}`. A service with `generation` above `appliedGeneration` has staged changes. If someone else edited the service in between, the `ifMatch` no longer matches and nothing is staged.
2. **Review.** `review_changes` (a `service` or a whole `environment`) shows every pending change field by field, with secrets masked.
3. **Apply.** `apply_changes` is the only way from staged to running. It takes `services` (each a `service` selector plus the `expectedGeneration` the client reviewed) or a whole `environment` (`project/env`) together with `confirmPending`, the number of staged services the client reviewed (what `review_changes` lists for that environment). `waitSeconds` (at most 50) waits for the result. `noCache: true` builds the git services of that call without cache (`--no-cache --pull`, no reuse of a build server's earlier image) for these deployments only ([deploying without cache](../operations/builds.md#deploying-without-cache)); it applies to services with staged changes, like the rest of the call. If someone changed a service after the review, the generation no longer matches; if the number of staged services differs, the call fails with `plan_changed`. Either way nothing deploys and the client reviews again.
4. **Verify.** `verify_service` requests the service's own hostnames through the proxy and reports whether it answers; for a service with processes it also reports each process that is short of ready replicas. It refuses loopback, private and link-local addresses (unless it is the service's own server), follows no cross-host redirects and times out after 8 seconds. `rollback_service` (`service`, `to`: `previous`, a release id, a deployment id or a commit's first 7 or more characters; `dryRun` previews it) returns to an earlier release. A rollback restores the application image and configuration only (every process goes back with the release); data is not rolled back.

A deployment that outlasts `waitSeconds` is still running. The result says so and names the next tool to call; it never reports success it has not seen. `outcome_uncertain` means a command was accepted but its server went offline before reporting, so check before retrying.

Secrets: `variable_set` can generate a value (`generate: {length, charset}`; `length` 1 to 1024, default 32; `charset` one of `alnum`, `hex`, `base64`, `base64url`, `digits`, `lower`), or take a value you typed. A secret value is never returned by any tool, never logged and masked in audit entries and in log output. `service_update` merges the `config` fields you give into the current config, so you send only what changes, with one exception: a `config.processes` you send replaces the whole list of [processes](../operations/processes.md), so read it with `service_get` first and send every process you want to keep. `stage_changes` takes the same `processes` list (and `processName`, `singleton`) under `config`, and `domains.add` and `setup_domain` take a `process` to route a domain to one process.

A service with several processes counts as up only when every process has all its replicas ready. `service_get` lists the `processes` with their state, `verify_service` and `diagnose_service` name the process that is short of replicas, `explain_failed_deployment` names the process a deployment failed in, and `tail_logs` takes `process` to read one process' output (without it the lines of every process are merged, each prefixed with the process name). `setProcessScaling` (through `call_operation`) scales one process; like `setServiceScaling` it needs approval in production.

## Production approval

An AI client that changes a **production** environment needs a person to approve it. The check cannot be routed around by a model: the domain services ask for it, and the MCP dispatch itself asks for every guarded operation whose service does not (scheduled tasks, git links, clones with secrets, DNS replacement and origin-lock changes, deleting retired volumes and backups, unlocking a backup, backup schedules, public database ports, links and mesh changes, and revoking or cutting off a server that hosts production services). It fails closed: if the approval store cannot be reached, the action is refused. A test fails when an operation that changes a production resource is neither guarded, denied nor listed with the reason it needs no approval.

What is guarded, when the environment is production: deploy and apply, deploy staged changes, rollback, restart, stop, deleting a service, restoring a backup, an in-place volume restore, running a task or one-off command, creating, changing or deleting a task, linking or unlinking git, cloning a service or environment from production, DNS adopt and origin-lock or proxy changes, deleting a volume, retired volume, backup or volume backup, unlocking a backup, backup and volume-backup settings, opening or closing a database's public port, deleting a domain, environment or project (a cascading project delete is bound to the plan the approver saw), un-flagging an environment as production, upgrading a database, rotating credentials, enabling maintenance mode, changing scaling, deleting a template instance, importing a Compose file with deploy, cancelling an unfinished deployment, and draining, revoking or turning off the tunnel of a server that runs production services. Non-production environments need no approval.

When a guarded action is requested:

1. **The approval page.** The tool returns the error `confirmation_required` with the plan and a link, `https://nebula.example.com/approvals/{id}`. A person opens it **signed in to the dashboard**: the page shows what will happen with **Approve** and **Decline**. After they approve, the client repeats the same call and it goes through. Owners, Admins and Developers with deploy rights on the project can decide; Viewers can only read. Approving happens only there. The client is never asked to approve (there is no in-client form), and nothing a client sends can approve a request: the approval operations are not available over MCP.
2. **Nothing a model can type.** The model cannot pass a `confirm: true` for production. An approval is single-use and bound to the token, the tool, the exact arguments and the generations that were reviewed. It lives at most 10 minutes from the request. If the arguments differ on the retry, or the service's generation moved in between, it is void and the client has to ask again. A request you decline keeps refusing the same call for one minute.

Over plain HTTP the same refusals appear as `409 approval_required`, `403 approval_declined` and `503 approval_unavailable`. Every outcome is written to the [audit log](../operations/teams.md) as `approval.requested`, `approved`, `declined`, `expired`, `refused`, `error` or `used`.

### Token kinds and settings

A token is made for one use. An **MCP token** works only on `/mcp`; sent to `/api/v1` it is refused with `403 token_kind_mismatch`. A **REST token** (REST API, CLI, CI) works only on `/api/v1`; sent to `/mcp` it is refused. So the production approval and the deny list cannot be sidestepped by sending an agent's token to the REST API. Create an MCP token from the MCP presets in **Account settings → API tokens**; existing tokens became REST tokens, so an agent needs a new MCP token.

Every token, of either kind, needs approval on production by default. The **Production changes** section has one setting:

- **Skip approval for production changes** (`allowProductionWithoutApproval`) is for an unattended job such as a nightly deploy. It is **off by default**; only the instance admin or a team Owner or Admin signed in on the dashboard can turn it on, and turning it on is audited. A token can never create a token that skips approvals, or one that outlives it. The exemption is honoured only while the creator is still the instance admin or an Owner or Admin. Keep it off for any token a model uses interactively.

Signed-in browser sessions are never guarded by this.

## Scopes and token presets

Each tool needs the scopes of the REST operations it calls, and the service re-checks them on every call. `logs:read` gates reading runtime, build and task logs, both in `tail_logs` and in the diagnostic tools that include log excerpts, because logs can hold data a person would not paste into a chat. Existing tokens that could read projects received it when you upgraded, so nothing stops working.

| Scope | Lets the client |
|---|---|
| `projects:read` | Read projects, environments, services, deployments, variables (never secret values) and templates. |
| `servers:read` | Read servers, their metrics and proxy state. |
| `logs:read` | Read logs (`tail_logs`), a service's [central logs](../operations/logs.md#central-logs) (`getServiceCentralLogs`), and log-bearing diagnoses. `get_task_run` needs it together with `projects:read`. |
| `projects:write` | Stage changes to projects, environments, services, variables, domains and volumes; install templates (`deploy: true` also needs `deployments:write`); restoring a backup needs it together with `deployments:write`. |
| `deployments:write` | Apply changes, restart, stop, back up, run commands (`run_command`; a selector in it needs `projects:read`, so a deploy-only token passes ids). |
| `deployments:rollback` | Roll back. |
| `servers:write` | Change server settings. |
| `proxy:write` | Change proxy routes, certificates and ports. Never in an MCP preset. |
| `admin` | Everything the token's creator's role holds. |

The presets in **Account settings → API tokens**:

- **MCP read-only:** `projects:read`, `servers:read`, `logs:read`. Look and diagnose; change nothing. Start here.
- **MCP deploy:** adds `deployments:write`: apply changes that are already staged (say, from the dashboard), restart, run commands. It cannot stage edits itself.
- **MCP configure:** adds `projects:write` as well, so the client can stage edits to services, variables, domains and volumes and then apply them. Production changes still need approval.

No preset includes `deployments:rollback` or `proxy:write`; add `deployments:rollback` under Custom if you want the client to roll back.

A token is also limited to its projects and by its creator's role, which is checked again on every use. See [API tokens](../operations/teams.md#api-tokens).

## What it cannot do

Some operations are never available over MCP, whatever the token holds, and `find_operation` does not list them. They would let a model (or whoever steers it through a prompt injection) take over the account, read a secret, or reach the host itself. You can still do all of them in the dashboard. Each one is denied for one of these reasons:

| Not available | Why |
|---|---|
| Sign-in, two-factor, passkeys, password reset, OIDC, first-run setup (`auth-plumbing`) | They exist for people signing in. A token never authenticates a person. |
| Your own account: profile, password, two-factor and passkey management, switching or listing teams, accepting an invite, approving a production approval (`session-only`) | Browser account management. An agent must not change how you sign in, and a principal must never approve its own production change: only a person in the web UI can. |
| Create API tokens, enrollment tokens, team invites and member password reset links; enable or rotate deploy hooks and webhook secrets; start a GitHub App manifest (`mints-credential`) | They return a credential, link or secret URL once. An agent holding one could escalate beyond its own scopes or add a machine to your fleet. |
| Reveal a database export or public connection string or a volume backup recovery key (`reveals-secret`) | Secret values stay write-only. Copy them in the dashboard, which audits it. |
| Write Cloudflare, Tailscale, DNS, registry, git, secret manager, backup destination, log drain, notification channel or MaxMind credentials; upload a TLS key; instance backup and instance settings; SSO provider setup; teams, member roles and removal, team security; the per-team Headscale platform service; the [built-in services](../operations/platform-services.md): their settings and checks, a team's quota, and purging a deleted team's slice (`getInstanceServices`, `updateInstanceServices`, `checkInstanceServices`, `updateTeamSliceQuota`, `purgeTeamSlice`) (`instance-admin`) | A credential would pass through the model's context, and these settings change the platform for everyone or decide who may do what. Reading most of them stays available. A team's own slice (`getTeamSlice`: backup use against the quota, registry size, log retention) and a service's central logs are reads and stay available; none of them returns a key, bucket or address. |
| The server `unrestricted` switch, the host shell switch, applying or reverting host hardening, upgrading a server's packages and rebooting it, updating the control plane itself (`host-root`) | They give or change root-level access to the host or widen what containers may do on it. The web terminal and file upload are not exposed over MCP at all. |
| Following a log stream (`streaming`) | MCP calls return one result. `tail_logs` returns a bounded, redacted tail instead. |
| An HTML page, such as a maintenance-page preview (`binary`) | A tool result is JSON or text. |
| Raw deploy, deploy staged, rollback, log history endpoints (`superseded-by-composite`) | `apply_changes`, `rollback_service` and `tail_logs` do the same job with the review, the production approval and the result handling the raw endpoint lacks. |
| The raw validate, plan, apply and export endpoints of the project file (`superseded-by-composite`) | `sync_config` does all four in one tool: `action` `plan` returns the validation and the plan with the plan id, `apply` adds the stage-only rule and the production approval, `export` writes the file. `apply` never deploys and never prunes. |
| Merging services into one service with several processes: `combineServices`, `abortCombine`, `retryCombine` (`person-only`) | A merge deletes the original services once the new release serves. A person reviews the plan (`planCombineServices` and `getCombine` are available) and confirms it in the web UI or with `nebulactrl services combine`. |
| The repository binding of an environment: read, change, clear, check, and its auto-apply switch (`instance-admin`) | Auto-apply stages the file on every push. That automation is a decision for a person; an agent neither reads nor sets it. |

One exec path remains on purpose: `run_command` runs a command you name in a throwaway container of the service's image, with the service's environment. That is **shell-equivalent** for that service: it can read the service's secrets and change its data. It needs `deployments:write`, is subject to the production approval and is audited with the command line. Leave `deployments:write` off any token an agent uses on untrusted input.

Everything else is available, through a tool or through `call_operation`, behind its scopes and the production approval.

## Limits and safety

- **Rate limit.** 60 requests per minute per token. Beyond that the call fails with a rate-limit error; wait and retry.
- **Output.** A result is capped at 64 KB. `tail_logs` returns at most 500 lines, redacted.
- **Logs are untrusted text.** A container can print anything, including text that looks like an instruction. Log and command output comes back as `{untrusted: true, nonce, source, notice, text, lines}`, with the text between `<untrusted-log nonce=…>` markers and a fresh random `nonce` on every call, so the client can tell it from tool output. It stays redacted.
- **Audit.** Every call is written to the audit log as `mcp.tool_call` with the arguments redacted (anything that may hold a secret, a value or a command is replaced) and the client's name. The service's own audit rows carry the actor type `mcp` and the label "token via client". A network client cannot claim the MCP actor: it is set in-process, never by a header.
- **Retries.** Mutating calls are idempotent: a repeated call returns the first result rather than deploying twice.
- **Errors** carry a `code` and a message.

## Renamed tools

If a client or prompt still uses an older tool name, these are the replacements. The old names are gone, with no aliases.

| Old name | Now |
|---|---|
| `list_projects`, `list_environments`, `list_services` | `project_list`, `environment_list`, `service_list` |
| `get_service`, `get_deployments`, `get_deployment_status` | `service_get`, `deployment_list`, `deployment_get` (adds `terminal` and `outcome_uncertain`) |
| `list_servers`, `get_server_status` | `server_list`, `server_get` |
| `get_service_traffic`, `restart_service` | `service_analytics`, `service_restart` |
| `get_service_logs` | `tail_logs` (needs `logs:read`) |
| `plan_deployment` | `review_changes` |
| `deploy_service` | `apply_changes` |
| `rollback_deployment` (with `confirmReleaseId`) | `rollback_service` |

`run_command`, `get_task_run`, the four backup tools and the three template tools keep their names.

## Machine-readable docs

These docs are also available to agents as plain text: see [llms.txt](/llms.txt) for an index, [llms-full.txt](/llms-full.txt) for everything in one file, and the OpenAPI document at `/api/v1/openapi.json`.
