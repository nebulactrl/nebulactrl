---
title: Teams, roles and invites
description: "Who can do what: roles, project scoping and invitations."
section: Teams and security
order: 10
---

# Teams, roles and invites

Glossary: **Team (UI) = workspace (code)**. The `workspaces` table, `workspace_id` columns and `policy.WorkspaceID(ctx)` keep the internal name.

## Model

- A user belongs to any number of teams through `memberships(user_id, workspace_id, role, project_ids)`. Each team has its own projects, servers, integrations, API tokens, notification channels, backup destinations and audit log. Servers are never shared.
- Setup creates the first team (optional `teamName`, default "NebulaCtrl") and its owner, who is also the **instance admin** (the first user created).
- The browser session stores the current team (`teamId`). `POST /teams/{id}/switch` re-checks membership (404 for non-members). If the stored team is gone the session falls back to the user's oldest team.
- Any user can create a team and becomes its owner, unless the instance admin turns off `allowTeamCreation` (Admin → Sign-in and teams, or `PATCH /instance`); the instance admin may always.

## Roles

| Role | Scopes | Notes |
|---|---|---|
| Owner | all | Deletes the team, manages owners (there must always be one) |
| Admin | all | Everything except managing owners |
| Developer | projects:read/write, deployments:write/rollback, servers:read | Deploys, variables, terminal/files, tasks. No members, integrations, servers or other people's tokens |
| Viewer | projects:read, servers:read | Read-only; never sees secrets, reveal, terminal or files |

**Host access is an instance-admin decision.** Only the instance admin can make a server *unrestricted* (devices, GPUs, a custom runtime, privileged mode and the other options that hand a container the host, and unrestricted Compose stacks: [Docker options](docker-options.md#the-unrestricted-switch-per-server)). A team owner, an admin or a developer cannot, whatever their scopes, so nobody below the instance admin can gain root on a host unless the instance admin allowed it for that server.

`policy.ScopesForRole` maps roles to scopes, and `policy.Require` checks a scope. `policy.RequireRole(ctx, projectID, roles...)` covers role-only checks. Owners and admins always see every project; developers and viewers can be restricted to a project list (outside it: 404).

## API tokens

A token belongs to one team (`api_tokens.workspace_id`). Its scopes are intersected with the creator's role at creation and **again on every use**: demoting the creator demotes the token, removing them from the team makes it answer 401. `admin` on a token means "everything my role holds". Project lists are intersected with the member's restriction. Admins see all tokens of the team; other members manage only their own (browser session). MCP follows tokens.

## Invites

`POST /teams/{id}/invites`: with `email` the link is mailed (the team's enabled email notification channel, else the instance SMTP relay from `PATCH /instance`, else link-only; the response's `delivery` says which) and works once for that address. Without `email` it is a link invite with `maxUses` (default 1) and an expiry (default 7 days, max 90). The token (`nci_…`) is stored hashed and shown once. `GET /invites/{token}` previews it publicly (404 invalid, 410 expired/used); `POST /invites/{token}/accept` joins a signed-in user or creates an account (name + password) and signs it in. Single use is enforced atomically. Emailed links need `NEBULACTRL_PUBLIC_URL`.

## Password reset links

`POST /teams/{teamId}/members/{userId}/password-reset` (Owners and Admins, signed-in users only; admins cannot reset an Owner and nobody can reset themselves) returns a single-use link, valid for 30 minutes, shown once. In the web app: **Team settings → Members → Reset password**. It needs no SMTP setup, so it is also the way to help someone when the instance cannot send email. Using the link signs the member out of every device (their API tokens keep working unless they choose to revoke them). It works only for a member whose whole account is in this team: a member who belongs to **any other team** gets `409 member_of_other_teams` (the instance admin can still issue the link), and the instance admin's password is reset on the server (`403 instance_admin_account`). Members who only sign in through an identity provider have no password: `409 no_password`. Audited as `member.password_reset_link`. Details: [forgot password](../security/login-methods.md#forgot-password).

## CLI

`nebulactrl teams ls|create|switch`, `nebulactrl members ls|invite`. A token belongs to one team, so `login` saves each token under its team slug and `teams switch <slug>` selects the saved one.

## Delete a team

**Team settings → Delete this team** or `DELETE /api/v1/teams/{teamId}?confirm=<slug>` (owners, signed in as a person; operation `deleteTeam`; denied to MCP agents). The danger zone first shows the plan (`GET /api/v1/teams/{teamId}/deletion-plan`): counts of projects, services, databases, volumes, servers (online and offline), domains, status pages, DNS records NebulaCtrl manages in Cloudflare and tailnet devices it created.

Everything goes, in this order, as a background job (the API answers 202; the team shows **Deleting…** in the switcher until it is gone):

1. every project with everything in it (services and their volumes, domains, previews, shared variables), then the team's platform service (its Headscale tailnet server, if it set one up);
2. every server. An online server is removed with the [clean mode](servers.md#remove-a-server): its agent empties the host and removes itself. An offline server is removed without touching its host; those are listed in the team's `deletion.forgottenServers` so you can clean them by hand;
3. the status pages and their DNS, the Cloudflare connection (the DNS records NebulaCtrl created are deleted from the zone unless you tick **Keep the DNS records**, `keepDnsRecords=true`), and the Tailscale integration with the devices and keys NebulaCtrl created;
4. the team itself, with its notification channels, registries, secrets, tokens and audit log (database rows).

Backups already written to your own S3 or local destinations stay where they are. The team's **slice of the [built-in services](platform-services.md#when-a-team-is-deleted)** (its backup bucket and key, its registry images and its central logs) is **kept for 30 days** after the deletion and then deleted automatically; the instance admin can delete it at once (Admin → Built-in services, **Delete now**, with the team's name typed). If a step fails (a deployment still running, a project with read replicas, an agent that cannot empty its host, a platform service that instance settings still use), the job stops, the team stays with `deletion.state` `failed` and the reason shown in the danger zone, and deleting again retries from where it stopped. A team that owns the server NebulaCtrl itself runs on cannot be deleted (409 `team_hosts_control_plane`).
