---
title: Servers and agents
description: "Offline servers, revoking and re-enrolling, and how the reconciler behaves."
section: Servers
order: 20
---

# Servers and agents

## The server page

Each server has one page, `/servers/<id>`, with a tab for each job. Every tab has its own address, so it can be bookmarked and shared.

| Tab | Address | What it holds |
|---|---|---|
| Overview | `/servers/<id>` | Status, address and proxy under the name; what the host reports (OS, CPUs, memory, disk, Docker, agent version); CPU, memory and disk charts; the last 24 hours of traffic in four numbers; the services placed here; warnings (agent not reporting, disk over its threshold, reboot required, Unrestricted) only while they are true. |
| Traffic | `/servers/<id>/traffic` | The full [traffic analytics](traffic-analytics.md) of this server's proxy. |
| Proxy | `/servers/<id>/proxy` | Ports, certificates, the Cloudflare tunnel, DNS-01, upstream policy, the tailnet and the routes. |
| Security | `/servers/<id>/security` | The [host hardening](../security/host-hardening.md) audit. |
| Updates | `/servers/<id>/updates` | [OS updates](server-patching.md), the agent's version and upgrade, and the host's kernel, cgroup and protocol versions. |
| Settings | `/servers/<id>/settings/<section>` | Owners and admins. General, Scheduling, Monitoring, Logs and disk, Mesh, Host shell and Danger zone (Unrestricted, drain, revoke, remove), one page each. |

The addresses `/servers/<id>#proxy`, `#security`, `#updates`, `#traffic`, `#settings`, `#mesh` and `?tab=<name>` open the tab that holds that topic; the hash wins over `?tab=`, and `?drain=1` travels along.

## Server states

| State | Meaning |
|---|---|
| `pending` | An enrollment token exists; no agent has redeemed it yet |
| `online` | A heartbeat arrived within the last 30 s |
| `offline` | No heartbeat for 30 s (agent down, host down, or network cut). Marked by a sweep that runs every 10 s. |
| `revoked` | The identity was revoked; the agent cannot reconnect |

Every enrolled agent holds its own Ed25519 identity. A connection is authenticated by signing a fresh server nonce; nonces older than 30 s are rejected.

## What happens when a server goes offline

- **Services keep running.** The agent and Traefik are independent of the control plane connection; the proxy continues to serve.
- **New commands** (deploys, restarts, …) are stored first, then sent. If the agent never received a command, it expires after **15 minutes**: the deployment becomes `failed` with "Not delivered: server offline. No changes were made."
- **Commands the agent already acknowledged never time out into a terminal state**, because the true outcome is unknown while it is offline. The deployment stays active and the canvas shows "Waiting for <server>" with the offline time. When the agent reconnects it replays the result and the deployment settles. If you know the host is gone, revoke the server to cancel what is pending.
- Reads (logs, current status) show stale data marked as stale.

## On reconnect: reconcile

When an agent connects, the control plane sends it the desired set of active deployments. The agent re-applies its route table, reports what is actually running (`observed`), and removes managed containers that belong to no desired deployment and no in-flight command **and are older than 10 minutes** (manually started containers without the `nebulactrl.managed=true` label are never touched).

If an active deployment's container is **missing** (for example someone ran `docker rm`), the control plane redeploys the same release (`trigger=reconcile`), **at most 3 times per service per hour**. A fourth miss records the audit event `reconcile.limit_reached` and the node shows an error; fix the cause (out-of-memory kills, disk full, a manual removal) and deploy again.

## Build servers

A server can be a **build server**: **Settings → Scheduling → Role** chooses *Deployments* (the default), *Builds* (no workloads, builds only) or *Both*, with a cap on concurrent builds and switches for emulation and pull-request previews. A build server builds images, pushes them to a registry and never needs the images afterwards; the servers that run services pull them by digest. Everything about roles, where builds run, registries, rollback and troubleshooting is in [Build servers and registries](build-servers.md).

What changes for the rest of this page:

- **Offline build server.** If it is offline when a deployment is dispatched, the build falls back to the target and the timeline says so; a build command it never received expires after 15 minutes and the deployment then builds on the target. If it goes offline **after** it acknowledged a build, the deployment waits (the canvas shows "Waiting for <name>") and never times out, exactly like any acknowledged command above; it settles when the agent reconnects and replays the result.
- **Revoking a build server** cancels its pending commands. If a deployment is waiting on a build there, cancel it and deploy again (placement then picks another build server or builds on the target). **Already built releases are unaffected**: their images live in the registry and roll back without it.
- **A server whose role is *Builds*** does not accept workloads, so it is not offered for services, drains or templates ([draining servers](draining-servers.md)).
- **Agents** must be current to build (`build.push` capability); older agents are never chosen.

## Revoke

**Servers → server → Settings → Danger zone → Revoke** (hold to confirm) or `POST /api/v1/servers/{serverId}/revoke` (scope `servers:write`; CLI: `nebulactrl servers revoke <id>`).

- The agent's socket is closed with status 4401 and the agent exits with "agent revoked; re-enroll".
- Pending commands for that server are cancelled.
- The containers on the host **keep running** until you remove them (see [uninstall](../installing/uninstall.md)).
- Revocation is recorded in the audit log.

Revoke when a host is retired, lost, or suspected compromised. A compromised agent host is a compromised root account ([threat model](../security/threat-model.md)); revoking only cuts its access to the control plane, so also rotate every secret that was deployed to it.

## Remove a server

**Servers → server → Remove server…** (also on every card of the servers list) or `DELETE /api/v1/servers/{serverId}?confirm=<name>&mode=clean|forget` (scope `servers:write` and `admin`; operation `removeServer`; denied to MCP agents). You type the server's name. Revoke keeps the server row; removal deletes it.

| Mode | What happens |
|---|---|
| `clean` (default) | The agent stops and removes every container, network and volume NebulaCtrl manages on the host (found by their `nebulactrl.*` labels, plus the proxy and its sibling containers and the `nc-vol-*` volumes; **volume data is deleted**), reports the result, and then deletes itself: its identity, systemd unit, environment file, binary and state directory, as `install-agent.sh --uninstall --purge` does. Docker, images and every container, network and volume that is not NebulaCtrl's stay, and so do **local backups**: the default backup directory (`/var/lib/nebulactrl-agent/backups`) is moved to `/var/lib/nebulactrl-backups` (into a timestamped subdirectory when that exists already) and a `--backup-dir` outside the state directory is left where it is; the agent logs where they went. The server row is deleted after the agent reported success. It runs in the background: the API answers 202 and the server shows **Removing…**; if the agent cannot finish, the server stays with `removal.state` `failed` and the reason, and removing again retries. |
| `forget` | The agent is revoked and the server deleted at once, **without touching the host**: containers keep running and the agent's files stay. The dialog shows the commands to clean the host by hand. An **offline** server is always forgotten, whatever mode was asked for (`mode` in the answer says which). |

Both are refused while services are placed on the server (409 `server_has_services`; `details.projects` lists the projects, the hidden platform project included) and for the server the control plane itself runs on (409 `server_is_control_plane`; uninstall NebulaCtrl from that host instead). `clean` needs an agent that advertises `canDecommission` (409 `agent_cannot_decommission` otherwise: upgrade the agent, or forget the server). The audit log records `server.removed` with the mode and the counts the agent removed. Deleting a [team](teams.md#delete-a-team) removes its servers the same way.

## Agent versions

Each server card shows when its agent is behind the control plane. Agents upgrade themselves (automatically, or with **Upgrade agents**), verify the binary and roll back if the new one does not start: [Upgrades](upgrades.md).

## Re-enroll a host

1. On the host: `curl -fsSL <url>/install-agent.sh | sudo sh -s -- --uninstall --purge` (deletes the old identity).
2. In the UI create a **new** server (**Add server**) or token; tokens are single use.
3. Run the new install command on the host.

The new server gets a new ID. Services are placed on a specific server, so move them by changing **Placement** in each service's settings and deploying; this creates a new deployment on the target, and **volumes are not migrated**. To empty a whole server in one operation, see [Draining servers](draining-servers.md).

If an agent was merely moved to new hardware, copy `/var/lib/nebulactrl-agent/agent.db` (mode 0600) to the same path before starting it and it keeps its identity.

## Lost the agent database

If `agent.db` is lost on a still-valid server, the agent has no identity and cannot connect. Revoke the server in the UI, then re-enroll as above.

## Moving the control plane

Agents dial `NEBULACTRL_PUBLIC_URL`. To change it, update `.env`, then re-run the installer on each host with the new `--url` (it rewrites the unit and restarts the agent; the identity is kept).
