---
title: Server patching
description: "Pending OS updates, reboot-required, the instance admin's Apply updates and Reboot, and optional nightly automatic updates."
section: Servers
order: 95
---

# Server patching

A Docker host is also an operating system that needs its security updates. NebulaCtrl shows, for every server, how many package updates are waiting (and how many of them are security updates), whether the host asks for a reboot, and gives the **instance admin** two actions: **Apply updates** and **Reboot**.

By default nothing is installed and nothing reboots without a person asking. Per server you can switch on [automatic updates](#automatic-updates): a nightly run in a maintenance window that installs the updates for you (Docker's own packages excluded), and optionally reboots when that is safe. The installer's unattended security updates (`deploy/install/install.sh`) keep patching a server until you switch NebulaCtrl's schedule on; it then takes over from them (see [Automatic updates](#automatic-updates)).

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

## Automatic updates

Per server, **off by default**: a server without settings behaves as above and nothing happens without a person. The instance admin switches them on in the server's **Updates** tab (or with `updateServerPatchAuto`, below).

**What runs.** Every night, inside the maintenance window, the control plane starts an upgrade on the server: the same *apply* run as **Apply updates** (`trigger` `scheduled`, shown in the history like any run, with its log). The agent first checks again, so it upgrades what is pending *now*; the stored check only decides whether to start at all: when the last check is under 6 hours old and shows nothing to upgrade for the scope, nothing is started and nothing is recorded.

| Setting | Default | Notes |
|---|---|---|
| `enabled` | off | |
| `scope` | `security` | `security` upgrades only the packages with a security update (apt and dnf today); `all` every package. `422 scope_unsupported` when the host's package manager cannot do security-only. |
| `windowStart` | `03:00` | `HH:MM` in `timezone`. Avoid 01:00-02:59, where clocks change. |
| `windowMinutes` | `120` | 60-240. |
| `timezone` | `UTC` | An IANA zone, for example `Europe/Berlin`. |
| `reboot` | `never` | `never` or `whenRequired` (see below). |
| `holdPackages` | none | Up to 20 package names, extra to the built-in holds. |

**Window and jitter.** A window is one per local calendar day: it starts at `windowStart` in `timezone` and lasts `windowMinutes`; on a day where daylight saving time moves the clock there is still exactly one window and one upgrade. Each server waits a stable offset into its window (a hash of its ID, between 0 and `min(30, windowMinutes/4)` minutes) so servers do not all start together. Nothing starts in the **last 15 minutes** of the window, and a run that has started is never killed when the window ends. There is **no catch-up**: a window that passed, or that began before you saved the settings, is not made up; the next night's runs. The control plane looks every 5 minutes.

**One at a time, and not while the server is busy.** A server starts its upgrade only when it is online, its agent supports it, no check, upgrade or reboot of it is in flight, no deployment, build, backup or restore is running on it, and **no other scheduled upgrade is running anywhere on the instance** (one at a time: servers that are due wait for the next sweep, still inside their window). A window starts at most one upgrade per server: if it failed, the next try is the next night.

**What is never upgraded.** Docker's and containerd's packages restart the daemon, which stops every container that has no live-restore, so scheduled runs always leave them out: `docker-ce`, `docker-ce-cli`, `docker-ce-rootless-extras`, `docker-buildx-plugin`, `docker-compose-plugin`, `containerd.io`, `docker.io`, `containerd`, `runc` and `moby-*` (matched exactly, so `docker-ce` does not hold back `docker-ce-cli` by accident; they are all listed). Upgrade them with **Apply updates**, where you see the plan. `holdPackages` adds your own (`422 invalid_package` for a bad name). On apt the held packages are left out of the list the agent installs (nothing is marked with `apt-mark hold`, so a failed run cannot leave a stray hold on the host); if upgrading the others would pull a held package along, the run fails with a message saying so. On dnf they are passed to `--exclude`. A manual **Apply updates** is not affected by any of this.

**How the agent runs it.** Like a manual upgrade (detached, configuration files you changed kept, `NEEDRESTART_SUSPEND=1`), plus: it waits up to 10 minutes for the dpkg lock (`DPkg::Lock::Timeout=600`, so a distribution timer holding it is not a failure) and retries downloads (`Acquire::Retries=3`); on apt a scheduled `all` or `security` run also installs the new dependencies kept-back packages need, such as kernel meta packages (`--with-new-pkgs` semantics, never removing anything); and it **skips the run with the reason** when `/var` has less than 1 GiB free, or a separate `/boot` has less than 300 MiB free. A skip counts as a failed run (below), so you hear about a full disk. Manual runs keep the exact behaviour described under *Apply updates*.

**Reboot.** `never` only installs: the existing `server.reboot_required` notification tells you when a reboot is pending. `whenRequired` lets the run reboot the server, through the same flow as **Reboot**, after a successful upgrade that left a reboot required, and only when **all** of these hold; otherwise it does not reboot and the upgrade's message says why:

* at least 10 minutes are left in the window,
* the server did not reboot automatically in the last 24 hours,
* the server is **not** the host of the control plane,
* the agent saw that **every running container** has the restart policy `always` or `unless-stopped` (the agent reads them with `docker ps`/`docker inspect` after the upgrade; if it cannot, there is no reboot).

**Production servers.** Turning automatic updates on for a server that runs services of a production environment, or the control plane itself, needs `acknowledgeProduction` (`422 production_ack_required`), like **Apply updates**, and for a token that must obtain approvals for production changes the same approval. The acknowledgement is the opt-in for the nightly runs.

**Failures.** A failed scheduled run (the package manager failed, the disk preflight skipped it, the command could not be delivered, a scheduled reboot did not come back) sends `server.auto_update_failed`. **Three failed runs in a row** switch automatic updates off for that server (`enabled` false, `disabledReason` says why, one more notification); a successful upgrade starts the count over. Saving the settings again clears the reason and the count. There is no automatic rollback of packages.

**The host's own automatic updates.** Debian and Ubuntu hosts usually run `unattended-upgrades`, RHEL-family hosts `dnf-automatic` (the installer sets one up). Two updaters at once fight over the package lock, so when you switch NebulaCtrl's schedule on, the agent hands over, on its next check (immediately when the server is online): on apt it writes `/etc/apt/apt.conf.d/99nebulactrl-auto-updates` with `APT::Periodic::Unattended-Upgrade "0";`; on dnf it disables `dnf-automatic.timer`, `dnf-automatic-install.timer` or `dnf5-automatic.timer` if enabled and remembers which in `/var/lib/nebulactrl-patching/native-timers`. When you switch it off (or after three failures), the drop-in is removed and exactly those timers are enabled again, so the host goes back to what it did before. The check reports `nativeAutoUpdates` (`unattended-upgrades`, `dnf-automatic` or empty), the mechanism that is active on the host right now; it reads empty while NebulaCtrl has taken over.

**Quiet-hour suggestion.** The Updates tab shows `suggestedStart`: the start of the quietest contiguous window (`windowMinutes` rounded up to whole hours) in the last 14 days of the server's traffic, in `timezone`, never starting between 01:00 and 02:59. It is a suggestion; nothing moves by itself. It is `null` with fewer than 3 days of traffic data (the server's hourly traffic retention applies, 7 days by default). A server that serves no traffic through NebulaCtrl's proxy has no data.

**Audit.** Changing the settings is audited as `server.patches.auto`; a scheduled run is audited when it finishes (`server.patches.run`, trigger `scheduled`), like a manual one.

## Notification: `server.reboot_required`

Sent once each time a server **starts needing a reboot**: the first check that sees the flag, or an upgrade that sets it. It is not repeated while the flag stays set, and a new streak after a reboot is announced again. Severity warning, group Servers, on by default for a new channel. Server events belong to no project, so only workspace-wide channels receive them (see [Notifications](notifications.md)). There is no notification for pending updates themselves.

## Notification: `server.auto_update_failed`

Sent each time a scheduled automatic update (or the reboot after it) of a server fails; the third failure in a row says automatic updates were switched off. Severity warning, group Servers, on by default for a new channel. A successful night sends nothing.

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
| `updateServerPatchAuto` | `PUT /patches/auto` | `200` with the `auto` object; instance admin; body `{enabled, scope, windowStart, windowMinutes, timezone, reboot, holdPackages, acknowledgeProduction?}` replaces the settings. |

`getServerPatches` carries `auto`: the settings plus `builtinHolds`, `securityOnlySupported`, `nativeAutoUpdates`, `nextWindowStart`, `lastScheduledRun`, `consecutiveFailures`, `disabledReason`, `suggestedStart` and `requiresAcknowledgement`.

Errors: `409 server_offline`, `409 patching_running`, `409 no_check_yet`, `422 agent_too_old`, `422 server_revoked`, `422 patching_unsupported`, `422 no_updates`, `422 confirmation_mismatch`, `422 production_ack_required`, `409 approval_required`; `updateServerPatchAuto` also `422 invalid_timezone`, `422 invalid_window`, `422 scope_unsupported`, `422 invalid_package`. The three POST writes accept an `Idempotency-Key`.

**MCP.** The reads and *check* are available as operations (`call_operation`). `applyServerPatches` and `rebootServer` are **not exposed over MCP**: they change the host as root (`host-root`, see [MCP](../reference/mcp.md)). `updateServerPatchAuto` schedules root upgrades and is held to the same rule.

## Limits

* **apt and dnf only.** zypper, pacman, apk and the like report *not managed here*.
* **`apt-get upgrade` semantics.** Packages held back because they need new dependencies or removals are neither counted nor upgraded; do those by hand. Third-party repositories are included as the host has them configured.
* **Automatic updates are opt-in and conservative.** Nothing is installed or rebooted on its own unless you switch [automatic updates](#automatic-updates) on for the server, and then only inside its window, never Docker's or containerd's packages, one server at a time on the instance, and a reboot only when the guards hold (it is off by default). There is no automatic rollback and no canary or staged rollout across servers.
* **A reboot is not drained.** The server's services go down; move them first with [draining](draining-servers.md) if that matters.
* **No live log.** The upgrade's output is stored when it ends (the last 48 KiB).
* **Checks need an online agent** that can reach the host (a helper container or the host itself) and, for the refresh, the distribution's mirrors.
