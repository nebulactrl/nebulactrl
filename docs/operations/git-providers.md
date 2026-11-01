---
title: Git providers and deploy on push
description: "Connect GitHub, GitLab and others, and deploy when you push."
section: Applications
order: 30
---

# Git providers, webhooks and deploy on push

NebulaCtrl connects to GitHub, GitLab, Gitea, Forgejo and Bitbucket. A connection lets you pick repositories instead of pasting URLs, deploys a service when you push to its branch, shows each deployment as a status on its commit and clones private repositories with short-lived credentials.

Set `NEBULACTRL_PUBLIC_URL` first: it is the address providers deliver webhooks to and redirect OAuth flows back to. Team settings → Integrations → Git providers is where connections live (admin scope).

## Connect a provider

| Provider | How | Webhooks |
|---|---|---|
| GitHub (and GitHub Enterprise Server) | One click creates a **GitHub App** (manifest flow) owned by you or an organization. Permissions: contents read, metadata read, pull requests write, commit statuses write. Events: push, pull request. Install it on the repositories you want. | The App's own webhook delivers for every installed repository. |
| GitLab (gitlab.com or self-managed) | A personal/group/project access token with the `api` scope, or an OAuth application (scopes `api read_repository`; redirect URI `<public url>/api/v1/git/oauth/callback`). | Registered per repository through the API, with a secret token header. |
| Gitea, Forgejo | Always give the instance URL. An access token, or an OAuth2 application (same redirect URI). | Registered per repository through the API; signed with HMAC-SHA256. Gitea blocks private webhook targets by default: allow the control plane in `[webhook] ALLOWED_HOST_LIST`. |
| Bitbucket (Cloud and Data Center) | A signed push webhook only (optionally a repository access token to clone private repositories). No repository picker, no statuses, no previews. | You create it by hand with the URL and secret NebulaCtrl shows once; the secret can be rotated. |

Credentials, OAuth tokens (refreshed automatically), the GitHub App private key and webhook secrets are encrypted at rest and never returned by the API.

## Deploy on push

Link a git service to a repository in its Source settings (or pick the repository when you create the service). The link and its auto-deploy settings are operational: they apply immediately, never bump the generation and are never rolled back. The clone URL and branch stay in the service config (picking another repository stages that change like any edit).

- **Branch**: a push to `config.gitBranch` deploys. The build is pinned to the pushed commit; the deployment has trigger `git_push` and shows the commit sha, message and author in the Deployments lists.
- **Watch paths** (monorepos): gitignore-style globs relative to the repository root, e.g. `apps/api/**`, `packages/*/src`, `Dockerfile`. `**` crosses directories, a plain directory name watches everything below it, a leading `!` excludes (`!**/*.md`). A push deploys when a changed file matches (and is not excluded). Providers that cap or omit the file list (a GitHub push of over 2,048 commits, a new branch without commits, GitLab pushes with more than 20 commits, every Bitbucket push) cannot be filtered: they deploy.
- **Skip**: `[skip ci]`, `[ci skip]`, `[skip deploy]`, `[nebula skip]` in the commit subject.
- **Busy services**: a push during a running deployment is queued (the newest wins) and deploys when that deployment ends.
- Auto-deploy ships the service's current configuration, including staged changes, exactly like a manual deploy.

## Commit statuses

Every deployment of a linked service reports `nebulactrl/<service>` on its commit: pending while it queues, builds and verifies, then success, or failure (failed, rolled back) / error (canceled), with a link to the deployment. Each state is sent once; failures are retried for several minutes. Reconcile and drain redeployments are not reported. Bitbucket has no status API.

## Webhook security

`POST /hooks/git/{provider}/{connectionId}` is unauthenticated but verified: GitHub, Gitea and Forgejo by HMAC-SHA256 of the body, GitLab by its secret token header, Bitbucket by `X-Hub-Signature: sha256=…`. A delivery that fails verification triggers nothing (HTTP 401). Providers sign only the body, so the delivery id header alone proves nothing: both the delivery id and the hash of the payload are remembered for 7 days, and a repeat of either (a captured payload sent again under a new id) is rejected as a replay (HTTP 409), so an old push cannot roll a service back. Every delivery is logged with redacted headers (Team settings → Integrations → Deliveries; last 200, 14 days): processed, ignored, rejected, replayed or error, and what it started.

## Clone credentials

A linked service clones with credentials minted when the deploy command is sent: a GitHub App installation token restricted to that repository with read access to its contents (valid one hour), or the connection's access token. They travel as send-time secrets only; they are never stored in commands, releases or logs. A service without a link keeps using its own stored git token.

The connection's credentials go only to the repository the link points at. Changing a linked service's `gitUrl` to another repository or host ends the link (the URL stays, auto-deploy stops); if a URL ever differs from the link, the deploy is refused with `409 git_link_mismatch` instead of sending the token anywhere else. A token is never sent over plain `http://` (use `https://`), and a `gitUrl` with credentials in it (`https://user:token@host/…`) is refused: connect the repository with a git connection or put the token in the service's token field.

## Private-network git servers

The control plane reads git remotes (to resolve a branch) and calls the provider API of a connection. It refuses loopback, private, carrier-grade NAT, link-local and cloud-metadata addresses, also after a redirect, and never shows what a remote answered beyond its status. A Gitea, Forgejo or GitLab that you host on a private network needs an entry in `NEBULACTRL_GIT_ALLOW_PRIVATE` on the control plane (host names, IPs or CIDR ranges, comma separated; see [control plane variables](../installing/control-plane.md)). Link-local and metadata addresses cannot be allowed.

## API, CLI and MCP

`/git/connections*` (connections, repos, branches, deliveries, OAuth/manifest flows) and `/services/{id}/git`. `GET /services/{id}` carries `git` (provider, repo, auto-deploy, watch paths) next to `config.gitUrl` / `config.gitBranch`; MCP `service_get` returns the same. Deployments carry `commit` and trigger `git_push`.

## Removing a connection

Refused while services use it unless forced, which unlinks them. Webhooks NebulaCtrl registered are deleted; a GitHub App stays installed at GitHub. Webhooks of repositories no service uses any more are removed hourly.
