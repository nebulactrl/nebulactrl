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
| GitHub (and GitHub Enterprise Server) | One click creates a **GitHub App** (manifest flow) owned by you or an organization: NebulaCtrl sends you to GitHub to confirm it. Permissions: contents read, metadata read, pull requests write, commit statuses write. Events: push, pull request. Install it on the repositories you want. If you leave GitHub before confirming, the connection stays "Waiting to finish": its **Finish setup** button sends you back to GitHub. | The App's own webhook delivers for every installed repository. |
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

## Wait for CI

With **Wait for CI to pass** on (`waitForCi` on the link, `git.wait_for_ci` in `nebula.toml`), a push does not deploy at once: NebulaCtrl parks it and reads the commit's checks every 30 seconds. The newest push replaces an older parked one, and the link shows `waitingForCi` (sha, message, author, branch, since) meanwhile.

- All checks pass: the push deploys (trigger `git_push`, same busy-service queueing as above). A commit with no checks at all deploys after 2 minutes.
- A check fails: the push is not deployed and a `deployment.ci_blocked` notification names the failing check. Push again (or deploy manually) to continue.
- Checks still pending after 2 hours: the push is dropped with a `deployment.ci_blocked` notification.
- Turning auto-deploy or Wait for CI off, or changing the branch, drops the parked push without deploying it.

What counts as a check: GitHub check runs (a run that is not `completed` is pending; `success`, `neutral` and `skipped` pass) and commit statuses; GitLab's newest pipeline of the commit; Gitea and Forgejo commit statuses. Statuses named `nebulactrl/…` (the deployment status NebulaCtrl reports itself) are ignored. Bitbucket and webhook-only connections cannot read CI results (`422 ci_not_supported`).

GitHub Apps created by NebulaCtrl ask for the **Checks: read** permission. An App created earlier does not gain it automatically: grant it in the App's settings and accept the new permission on the installation. Until then a waiting push is dropped with a notification that says so.

## Without a webhook: check for new commits

A push only reaches NebulaCtrl through a webhook. A service created from a plain git URL, a private repository with an SSH deploy key, a Bitbucket or webhook-only connection, or a GitLab or Gitea link whose webhook could not be registered never hears about new commits. **Check for new commits** closes that gap: NebulaCtrl asks the remote for the head of the service's branch on a schedule, the way a manual deploy resolves it, and compares it with the commit the service runs.

Turn it on in the service's Source settings → **Check for new commits**: choose Every minute, 5 minutes, 15 minutes or hour (Off is the default). It works for any app or worker built from git, with or without a connection, and uses the credentials a deployment uses: the service's git token, its connection, or its SSH deploy key. It is an operational setting: it applies at once, is never staged and is not rolled back.

- **Deploy new commits automatically** (on by default): a commit that is not running is deployed like a push (trigger `git_push`); the deployment's note says it was found by checking, for example "New commit found by checking main every 5 min". Turn it off to only be told: the card shows "New commit abc1234" and the menu offers **Deploy latest commit**, but nothing deploys by itself.
- **Status**: the settings show when the branch was last checked, "main is at abc1234 (running)" or "New commit abc1234 on main" with a **Deploy latest** button, or the error of the last check (an unreachable remote, a rejected token). A failed check is not announced; it stays on the service and the next check tries again. **Check now** reads the head right away and never deploys.
- **Each commit once**: a commit that already has a deployment after the running release (active, queued, failed or succeeded) is not deployed again, so a failed commit is not retried and a rollback is not undone. Push or deploy by hand to continue.
- **Busy services**: a commit found during a running deployment waits and deploys when it ends. A linked service queues it like a push; others look again within a minute.
- **Wait for CI** applies: on a linked service that waits for CI, a commit found by checking is parked until its checks pass.
- Stopped services, cron services and pull-request previews are checked but never deployed, and a service that was never deployed has no running commit to compare with. Watch paths do not filter commits found by checking: every new commit on the branch counts.

The same setting in `nebula.toml`: `[services.api.git] poll = "5m"` (`"off"`, `"1m"`, `"5m"`, `"15m"` or `"60m"`) and `poll_deploy = false` for notify-only. From the CLI: `nebulactrl services git-poll api --every 5m`, `--notify-only`, `--off`, `--check`. A production service asks for approval when a token turns it on (see [production approval](../reference/mcp.md#production-approval)).

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

`/git/connections*` (connections, repos, branches, deliveries, OAuth/manifest flows), `/services/{id}/git` and `/services/{id}/git-poll` (check for new commits; `POST …/git-poll/check` reads the head now). `GET /services/{id}` carries `git` (provider, repo, auto-deploy, watch paths) and `gitPoll` (null while off) next to `config.gitUrl` / `config.gitBranch`; MCP `service_get` returns the same. Deployments carry `commit` and trigger `git_push`.

## Removing a connection

Refused while services use it unless forced, which unlinks them. Webhooks NebulaCtrl registered are deleted; a GitHub App stays installed at GitHub. Webhooks of repositories no service uses any more are removed hourly.
