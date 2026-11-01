---
title: Pull-request previews
description: "A throwaway environment for every open pull request."
section: Applications
order: 90
---

# Pull-request previews

A preview is a real environment that NebulaCtrl creates for every open pull
request of a repository connected to the project, deploys with the pull
request's head commit, and deletes again when the pull request is closed or
merged. It needs a [git connection](git-providers.md) (GitHub, GitLab, Gitea or
Forgejo) with webhooks, and at least one service of the project's base
environment linked to the repository.

Open it from **Project → Previews**. The page lists the open previews (status,
age, last push, URLs, delete) above the settings. The environment switcher
groups previews under the environment they were cloned from, labelled
`PR #12 · title`; a preview's canvas carries a banner with its pull request,
status and a delete button.

## What a preview is

| | |
|---|---|
| Environment | `environments.kind = 'preview'`, slug `pr-<n>`, name `PR #<n> · <title>`, `parent_environment_id` = the base environment. Standard environment APIs work on it. |
| Services | A copy of the base environment's services. Services connected to the pull request's repository build the head commit on the PR branch; every other service is cloned as it is (image services run the same image, git services of other repositories build their own branch). Route services (external upstreams) are not cloned. |
| Databases | Created **fresh**: the same engine and version, new generated credentials, an empty data volume. Nothing from the base database (data, credentials, extra variables) is copied. References such as `${{postgres.DATABASE_URL}}` resolve to the preview's database. |
| Variables | Copied from the base services, including secret variables, so the preview runs like the base. Variables that contain generators (`${{secret(32)}}`) are generated again. The environment-level shared variables of the base environment are copied; project-level ones apply to the preview too. |
| Volumes | Declared again, **empty**. |
| Domains | Each service that has a public domain in the base environment gets a generated `<service>-pr-<n>.<base domain>` (the server's base domain, else `<ip>.sslip.io`). If the name is taken, the project slug and then a counter are added. Custom domains are never copied (hostnames are unique). |
| Canvas | The base environment's layout, so the preview looks the same. |

The first deployment starts the databases and other providers first; a service
whose variables reference another service waits until that one succeeded. If a
deployment fails, services that depend on it are not started and the preview is
marked `failed` with the reason.

## Lifecycle

| Event | What happens |
|---|---|
| PR opened / reopened | The environment is cloned and deployed. A comment with the status and URLs is posted on the pull request and a commit status `nebulactrl/preview` (pending, then success or failure) is set on the head commit. |
| New push | The services built from the repository redeploy at the new head commit (pinned to the SHA). Other services keep running. A duplicate webhook delivery does nothing. |
| PR closed / merged | The preview is deleted: every service with its container, **volumes** and domains (`remove_service` with `deleteVolumes`), then the environment. The comment is updated to say so. |
| Over the limit | The least recently pushed running preview is **paused**: its services are stopped, volumes stay. The next push to its pull request resumes it (stopped services start again, the pushed commit is deployed). |
| Idle | A preview with no push for the configured idle days is deleted (checked every 10 minutes). Paused previews count as idle too. |

If a push arrives for a pull request without a preview (for example after an
idle deletion) one is created. The sticky comment is found by a marker
(`<!-- nebulactrl-preview:<project id> -->`), so every project that previews a
repository gets one comment per pull request.

## Settings

`GET/PUT /api/v1/projects/{id}/previews/settings` (`projects:read` / `projects:write`):

| Setting | Default | Notes |
|---|---|---|
| `enabled` | off | |
| `baseEnvironmentId` | the production environment | A standard environment of the project. A base environment cannot be deleted while previews of it exist (`409 environment_has_previews`). |
| `forkPrs` | off | See [Fork pull requests](#fork-pull-requests). |
| `maxConcurrent` | 5 | 1–50 previews running at once. Past it the oldest is paused. |
| `idleDays` | 7 | 1–365. |
| `excludedServices` | none | Service slugs of the base environment that previews do not get. |
| `seedCommands` | none | `[{service, command, runOn}]`, below. |

### Seed commands

A preview's database starts empty. A seed command runs a one-off job (the same
mechanism as one-off commands in service settings) in a service of the preview
once **all deployments of the wave succeeded**, for example
`npm run db:migrate && npm run db:seed` in the app that owns the schema.
`command` is a shell command line run with `sh -c` in the service's current
image with its environment and networks. `runOn` is `create` (default: the first
deployment only) or `push` (also after every later deployment). Seed commands
run one after the other; a failing one (non-zero exit or timeout) marks the
preview `failed` and is shown in the comment, the commit status and the list.
Output is in the service's run history.

## Fork pull requests

Off by default: a fork's code is not yours, and a preview runs it. When an admin
turns `forkPrs` on, fork previews **never receive secrets**:

- no secret variable, and no variable whose value references a secret shared
  variable or the vault (`${{shared.KEY}}`, `${{vault.…}}`);
- no secret environment-level shared variables;
- no git token, and no repository link: the clone uses the fork's public HTTPS
  URL (the services that build the repository get `gitUrl` = the fork and the
  PR branch). Private services of other repositories in the project cannot be
  cloned for a fork preview;
- no registry credentials for private images.

Credentials of the preview's own databases are generated for the preview. Pull
requests from forks also never get commit statuses or comments through the
service's repository link; the comment and aggregate status of the preview
itself are still posted.

Pull-request previews of same-repository branches are trusted like a push to
that repository: whoever can push a branch can run code with the base
environment's secrets. Restrict who can push, or keep previews off for projects
whose secrets must not reach every contributor.

## Where previews build

Previews follow the [build placement](build-servers.md#where-builds-run) of their services, with one difference: **a preview never builds on a shared build server** unless that server has **Allow previews** on. A preview's code is the pull request's (and for a fork, untrusted), so by default it is built on the preview's own target server, even when its base services build on a build server. When a build server does allow previews:

- the preview builds there like any deployment, with its own per-project BuildKit builder and cache, and its image is pushed to the registry under the usual repository and a unique `r-<release8>` tag (the repository name has the project and service, not the environment, so a preview's repository can be the base service's repository; deployments always pull the **digest**, never a tag);
- fork previews are still built from untrusted code on that server, with the registry push credential and the other projects' builders on the same host. Keep **Allow previews** off for any build server that matters, or dedicate a build server to previews and give it a registry credential that cannot touch your production repositories.

If a preview's services resolve to a build server but none allows previews, the build runs on the target and the preview's timeline says so ("No build server available; building on <target>"); this never blocks the preview.

## Limits and costs

Each preview runs every service of the base environment (minus exclusions): size
`maxConcurrent` by memory, and exclude heavy services (a search cluster, a
worker) that previews do not need. A paused preview keeps its volumes (disk) but
no memory. Deleting a preview removes its volumes, including the preview
database.

## API

| | |
|---|---|
| `GET /projects/{id}/previews` | the project's previews: status (`creating`, `deploying`, `seeding`, `ready`, `failed`, `paused`, `deleting`), PR, head commit, URLs, `createdAt`, `lastPushAt` |
| `DELETE /previews/{id}` | start deleting a preview (202); needs `projects:write` |
| `GET/PUT /projects/{id}/previews/settings` | above |

Audit actions: `preview.settings.update`, `preview.create`, `preview.deploy`,
`preview.pause`, `preview.delete` (system actor for the automatic ones).
Notifications: `preview.created` and `preview.deleted`.

## Troubleshooting

- **No preview appears.** The base environment must contain a service linked to
  the repository the pull request belongs to (Service → Source), previews must
  be enabled, and the connection's webhook must deliver (Team settings →
  Integrations → Deliveries). Fork pull requests are ignored while `forkPrs` is off (logged as
  "fork pull request ignored").
- **`failed` with "could not be deployed".** The message is the deployment's:
  a missing server, an unresolved reference to an excluded service, a failed
  build. Fix it and push again, or redeploy the service from the preview's
  canvas.
- **Preview stuck deleting.** A service of the preview still has a running
  deployment; the teardown retries every few seconds until it finishes.
- **Containers of a deleted preview remain.** `remove_service` is delivered like
  any command; if the server was offline for longer than 15 minutes the command
  expired and the reconciler removes the containers later (volumes may need a
  manual `docker volume rm nc-vol-<service id>-…`).
