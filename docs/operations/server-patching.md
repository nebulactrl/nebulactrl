---
title: Server patching
description: "Pending OS updates, reboot-required, and the instance admin's Apply updates and Reboot."
section: Servers
order: 95
---

# Server patching

A Docker host is also an operating system that needs its security updates. NebulaCtrl shows, for every server, how many package updates are waiting (and how many of them are security updates), whether the host asks for a reboot, and gives the **instance admin** two actions: **Apply updates** and **Reboot**.

Nothing is installed and nothing reboots without a person asking. The installer's one-time setup of unattended security updates (`deploy/install/install.sh`) is separate and keeps working; this page is about seeing the state and acting on it from the dashboard.

## What you see

| Where | What |
|---|---|
| Servers list, every card | `3 updates (1 security)` with a status icon, or `Up to date`; a **Reboot required** badge when the host asks for one. A server that has not been checked yet says nothing. |
| Server page → **Overview** | A **Reboot required** warning with a link to the Updates tab, while the host asks for one. |
| Server page → **Updates** tab | The counts, the time of the last check, the package manager, the pending packages (security first, risky ones with a warning), **Check for updates**, **Apply updates**, **Reboot**, and the history of runs with the upgrade's log. The tab's badge reads `Reboot` or `N security` when something needs you. |
| Notifications | `server.reboot_required` (see below). |

The numbers are the ones the host's package manager reports after refreshing its package lists. They are only as fresh as the last check: the tab shows when it ran and marks a check older than 12 hours as out of date.

## How it is checked

The agent runs a read-only script on the host and reports the result. Everything runs on the host the way [host hardening](../security/host-hardening.md) does: an agent installed on the host runs the script directly as root; an agent that runs in a container (the dev stack) starts a short-lived privileged helper container from its own image whose command enters PID 1's namespaces with `nsenter` (the host shell's mechanism). The agent advertises the capability `host.patching`; a server whose agent is older gets `422 agent_too_old`.

| Package manager | Pending updates | Security updates | Reboot required |
|---|---|---|---|
| **apt** (Debian, Ubuntu) | `apt-get update`, then the `Inst` lines of `apt-get -s upgrade` (what `apt-get upgrade` would do; packages that would need new dependencies are *kept back* and not counted) | An update whose origin is a `-security` suite (`jammy-security`, `stable-security`, ESM `…-infra-security`) | `/run/reboot-required` exists; the packages that asked are in `/run/reboot-required.pkgs` |
| **dnf** (RHEL family) | `dnf check-update --refresh` | The packages in `dnf check-update --security` | `needs-restarting -r` says so (exit 1); without it, a newer installed `kernel-core` than the running kernel |

A host with neither manager is reported as *not managed here*. The check also reads the kernel's boot id (`/proc/sys/kernel/random/boot_id`), which is how a reboot is proven (below). If the package lists cannot be refreshed (no network), the numbers are the last ones the host downloaded and the tab says so.

**When it runs.** A control-plane sweep looks every 30 minutes and checks each **online** server that has had no run (of any kind, with any outcome) in the last **6 hours**, so a server is checked at most every 6 hours and a server whose checks fail is retried at the same pace, not on every sweep. **Check for updates** starts one by hand (it needs the Owner or Admin role and the `servers:write` scope; it changes no package). Only one run per server is in flight at a time (`409 patching_running`); an offline server gets `409 server_offline`.

## Apply updates

**Apply updates** upgrades the packages on the host as root: `apt-get upgrade` (never `dist-upgrade`, so nothing is removed and no new package is pulled in) or `dnf upgrade`, with `Dpkg::Options::=--force-confold` so configuration files you changed are kept. **Security only** upgrades just the packages that have a security update (`apt-get install --only-upgrade <those packages>`, `dnf upgrade --security`). It never reboots by itself.

Who and how:

* **Instance admin only** (the first user, or an `admin`-scoped token acting for them), with the `servers:write` scope. Owners who are not the instance admin get `403`. Upgrading packages and rebooting are root operations on the host; this is the same rule as the *Unrestricted* switch.
* **A check first.** The plan comes from the last check; before one has completed the request returns `409 no_check_yet`. Nothing to upgrade is `422 no_updates`.
* **A plan.** `GET /api/v1/servers/{id}/patches/plan?securityOnly=` returns what would be upgraded, the packages that deserve a warning and why (a new kernel, glibc, systemd and NVIDIA drivers need a reboot; Docker/containerd/runc restart the Docker daemon, which stops containers unless Docker's live-restore is on; openssh-server, the bootloader, a database server installed on the host, Tailscale/cloudflared), whether a reboot will probably be needed, how many services run on the server, and the production services among them. The dashboard shows it in the confirmation.
* **A confirmation.** The request must carry `confirm`, the server's name (`422 confirmation_mismatch`). The dashboard asks you to type it.
* **A production guard.** When the server runs services of a **production environment**, or is the host of the control plane itself, the request must also set `acknowledgeProduction` (`422 production_ack_required` otherwise). A token or MCP client that must obtain approvals for production changes (see [production approval](../reference/mcp.md#production-approval)) additionally needs the human approval for each production environment on the server, and the call is refused when approvals are unavailable. The audit row names the production services affected.
* **Audited.** `server.patches.apply` records who, which server, whether security only, how many updates and the production services; `server.patches.run` records the outcome (system action).

The upgrade runs **detached on the host** (`systemd-run`, falling back to `nohup setsid`) and writes its output and exit code under `/var/lib/nebulactrl-patching/` (files older than 7 days are removed on the next run). The agent polls for the result. This matters because upgrading Docker restarts the daemon, which restarts the helper container and, for an agent that runs in a container, the agent itself: the upgrade finishes anyway, and the agent attaches to it again when it is back (a start request for a run id that already started attaches instead of starting another). Host contact lost for up to 15 minutes is tolerated; a run whose process vanished without an exit code (the host rebooted mid-upgrade) is reported as failed, and running it again resumes where apt or dnf stopped.

When it ends, the agent checks again, so the new counts and the reboot flag are stored with the run. The run keeps the **last 48 KiB of the package manager's output** as its log (shown in the history; `GET /api/v1/servers/{id}/patches/runs/{runId}`). The log is delivered when the upgrade ends, not streamed while it runs. A non-zero exit status marks the run `failed` with the log and the new state.

## Reboot

**Reboot** restarts the whole server. Every service on it goes down until the host boots and Docker starts them again (containers with the `unless-stopped` policy that NebulaCtrl creates come back by themselves). It has the same gates as Apply updates: instance admin, the server's name typed as `confirm`, `acknowledgeProduction` when production or the control plane runs there, the production approval for guarded principals, and an audit row (`server.patches.reboot`, then `server.patches.run`). The dashboard's confirmation warns that services go down and lists the production services.

The agent schedules the reboot a few seconds ahead (`systemd-run --on-active=5s systemctl reboot`, else `shutdown -r +1`) so it can still answer, and reports the boot id it will replace. The run is then **`rebooting`**: NebulaCtrl waits for the agent to reconnect. Once the server is online again the control plane asks it for a check and compares the boot id; **a different boot id finishes the run as `succeeded`**. If the agent reconnects before the host went down, the boot id is the same and the wait continues. If the server does not come back within **20 minutes**, the run fails with that message (check the console; services stay stopped until the host boots). A server revoked meanwhile fails the run too.

While the host is down the usual `server.offline` and `server.online` notifications fire; they are not suppressed for a planned reboot. On the control-plane host, the dashboard is unavailable too until it is back.

## Notification: `server.reboot_required`

Sent once each time a server **starts needing a reboot**: the first check that sees the flag, or an upgrade that sets it. It is not repeated while the flag stays set, and a new streak after a reboot is announced again. Severity warning, group Servers, on by default for a new channel. Server events belong to no project, so only workspace-wide channels receive them (see [Notifications](notifications.md)). There is no notification for pending updates themselves.

## API

All under `/api/v1/servers/{serverId}`; the reads need `servers:read`.

| Operation | Endpoint | Notes |
|---|---|---|
| `getServerPatches` | `GET /patches` | Counts, packages, reboot-required, last check, the run in flight, production services on the server, recent runs. The server list and `GET /servers/{id}` carry a `patches` summary. |
| `planServerPatches` | `GET /patches/plan?securityOnly=` | What an upgrade would do; changes nothing. |
| `getServerPatchRun` | `GET /patches/runs/{runId}` | One run with its log. |
| `checkServerPatches` | `POST /patches/check` | `202`; Owner or Admin. |
| `applyServerPatches` | `POST /patches/apply` | `202`; instance admin; body `{confirm, securityOnly?, acknowledgeProduction?}`. |
| `rebootServer` | `POST /patches/reboot` | `202`; instance admin; body `{confirm, acknowledgeProduction?}`. |

Errors: `409 server_offline`, `409 patching_running`, `409 no_check_yet`, `422 agent_too_old`, `422 server_revoked`, `422 patching_unsupported`, `422 no_updates`, `422 confirmation_mismatch`, `422 production_ack_required`, `409 approval_required`. The three writes accept an `Idempotency-Key`.

**MCP.** The reads and *check* are available as operations (`call_operation`). `applyServerPatches` and `rebootServer` are **not exposed over MCP**: they change the host as root (`host-root`, see [MCP](../reference/mcp.md)).

## Limits

* **apt and dnf only.** zypper, pacman, apk and the like report *not managed here*.
* **`apt-get upgrade` semantics.** Packages held back because they need new dependencies or removals are neither counted nor upgraded; do those by hand. Third-party repositories are included as the host has them configured.
* **No automatic upgrades or reboots.** Nothing is installed or rebooted without a person asking. (Use the installer's unattended security updates for that; this page then tells you when a reboot is pending.)
* **A reboot is not drained.** The server's services go down; move them first with [draining](draining-servers.md) if that matters.
* **No live log.** The upgrade's output is stored when it ends (the last 48 KiB).
* **Checks need an online agent** that can reach the host (a helper container or the host itself) and, for the refresh, the distribution's mirrors.
