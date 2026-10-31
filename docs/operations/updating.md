---
title: Updating NebulaCtrl
description: "The update notice, the one click update of the control plane, what happens during it, how it rolls back, the manual command and how to turn the check off or point it at a mirror."
section: Installing
order: 45
---

# Updating NebulaCtrl

Your control plane checks once a day whether NebulaCtrl has a newer release. When it has, the dashboard says so, shows the release notes and lets the **instance admin** update with one click. The update runs the installer on the control plane's own host, backs the database up first, and puts the previous version back if the new one does not become healthy.

Updating the control plane is separate from updating the servers' agents, which upgrade themselves afterwards ([agent upgrades](upgrades.md)). Your running apps are not touched by either.

## The update notice

When a newer release exists, everyone who is signed in sees a chip in the sidebar. It points to **Admin → Updates**, which shows:

- the version you run and the newest release, with its date and release notes (and a link to the release page);
- the **Update** button, when you may use it, or the reason why you cannot ([below](#when-one-click-is-not-available));
- the command to update by hand, always;
- when the last check ran, and a short, soft message when it failed. A failed check never shows a warning banner: the next daily check tries again. The instance admin also gets a **Check now** button next to it, which reads the release feed immediately (at most once every 30 seconds).

A development build (`dev`) never shows an update.

## What the daily check sends

The control plane fetches `https://nebulactrl.com/releases/latest.json` one minute after it starts and then once a day. It is a plain anonymous `GET`:

- The `User-Agent` is `nebulactrl/<version>`, for example `nebulactrl/0.1.0`. That is all the request says about you.
- No instance id, no email, no hostname, no list of servers and no cookies. There is no telemetry anywhere in NebulaCtrl, and the feed is a static file that answers every caller the same.
- The answer is a small JSON file ([the feed](#the-feed)). Nothing is downloaded or installed because of it. An update only starts when an instance admin clicks **Update**.

To stop the check and the notice altogether, set `NEBULACTRL_UPDATE_CHECK=false` ([settings](#settings)).

## Update from the dashboard

**Admin → Updates → Update** installs the release the card announces. Only the **instance admin** (the first account created on this control plane) can start it. It is audited as `instance.update.start`.

### What happens

1. The control plane checks that the version you asked for is the announced one and newer than the one you run, that no update is already running, and that the agent on its own host is online and new enough to run the installer. If not, it says why and starts nothing.
2. It sends one command to **the agent on the control plane's own host**, the machine the installer set up.
3. The agent downloads the installer to `/var/lib/nebulactrl-update/install-<timestamp>.sh` (mode 0700, deleted when the unit ends), checks that it is the NebulaCtrl installer, and starts it as a **transient systemd unit** named `nebulactrl-update-<unix timestamp>` (`systemd-run --collect`). It refuses when another `nebulactrl-update-*` unit is active. The unit belongs to neither the agent nor the control plane's container, so it keeps running while both restart. Its log: `journalctl -u nebulactrl-update-<timestamp>`.
4. The installer backs the control plane up first ([backup before an upgrade](upgrades.md#backup-before-a-control-plane-upgrade)). **If the backup fails, it stops** before it changes anything: the old version keeps running, and the card shows why (see [the outcome](#the-outcome)).
5. It pulls `ghcr.io/nebulactrl/nebulactrl:<version>`, recreates the control plane container (the database container stays) and upgrades the host's agent to the binary that ships with the new version.
6. It waits until the container reports healthy (up to 240 seconds) and `/readyz` answers (about two minutes at most).

**The dashboard is down for about a minute** while the new container starts. Sign-in, the API and the CLI answer again once it is up. **Running apps are not touched:** the proxy is the agent's, not the control plane's, and other servers' agents reconnect on their own ([upgrade](../installing/upgrade.md)).

The agent runs `bash install.sh --update-to <version>` ([the option](../installing/installer.md#options)) and the options in `/opt/nebulactrl/install.conf` still apply.

### The outcome

The installer reports its own failure: when it stops (backup failed, pin refused, health failed and rolled back), it writes the reason, including the rollback text, to the instance, and the card shows it. A success cannot be reported by the old control plane, so the **new one marks it** when it starts with the version that was asked for. An update that is marked running and gets no result for 30 minutes is marked **failed** (look at `journalctl -u 'nebulactrl-update-*'` on the host).

Each outcome is audited as `instance.update.succeeded` or `instance.update.failed`. A result stays in the card for **one hour**.

### If the new version does not start

When the new version is not healthy, the installer **puts the previous version back**. Before it changed anything it kept the previous image by its ID (a moving tag such as `latest` is pointed back at it), and copies of the previous `docker-compose.yml` and `.env` (removed again when the installer exits, because `.env` holds secrets). The health wait is `docker compose up --wait` for up to 240 seconds, then `/readyz` for about two minutes. If it fails, the installer restores those files and that image, starts the previous version, prints that it rolled back and exits with status **4**. You then see an update that failed with a message like *The new version did not become healthy; the installer rolled back to 0.1.0.*, and the old version is running again.

If the rollback does not come up either, the installer exits with status **1** and says the control plane is down. Restore the pre-upgrade backup (below).

Two things to know:

- **Migrations that already ran stay applied.** They only go forward. If the new version migrated the database before it failed, the previous version may refuse the newer schema and not start either. Then restore the **pre-upgrade backup** the installer took (Admin → Control-plane backup → History, trigger *Before upgrade*, or `/var/backups/nebulactrl/control-plane` on the host) into a clean database, following [restore](backup-restore.md#restore) and [roll back the control plane](../installing/upgrade.md#roll-back-the-control-plane). Changes made after that backup are lost.
- The release that failed is still announced. The notice stays until a newer release is out; read the release notes and the unit's log before you try again.

### When one click is not available

The card then shows the reason in plain words and keeps the manual command. The usual reasons:

| Reason | What to do |
|---|---|
| You are not the instance admin | Ask the instance admin, or use the manual command. Everyone still sees the notice. |
| The control plane was installed with `--no-local-server`, so its host has no agent of NebulaCtrl | Use the manual command on the host. |
| The agent on the control plane's host is offline | Bring the agent back ([servers](servers.md)), or use the manual command. |
| That agent is too old, or the host does not run systemd | Only Linux hosts with systemd can run the update (the agent offers it as the capability `controlplane.update`). An old agent upgrades itself when it next connects to a current control plane; until then, and on hosts without systemd, use the manual command. |
| The installation lives in another directory (`--dir`), or `curl` is missing on the host | The agent refuses, and says so. Use the manual command, with `--dir` if you set it. |
| An update is already running (this one, or another `nebulactrl-update-*` unit on the host) | Wait for its outcome. |
| Checks are off (`NEBULACTRL_UPDATE_CHECK=false`) | Nothing is announced; use the manual command. |
| The installation was pinned with `--image` | The update stops with a message in the card: re-run the installer with `--image <the new image>`. |

MCP clients and AI agents **cannot** start an update: the operation is denied over MCP, like the other operations that replace software on a host ([MCP](../reference/mcp.md)).

## Update by hand

Run this on the control plane's host. The card shows it with the version filled in (and from your own feed origin when you use a mirror):

```sh
curl -fsSL https://nebulactrl.com/install.sh | sudo bash -s -- --update-to 0.1.2
```

It is the same installer, and the same command the dashboard's update runs: the same backup first and the same rollback to the previous image if the new version is not healthy ([installer](../installing/installer.md#re-running-upgrading-recovering)). Know how it picks the version:

- **`--update-to X.Y.Z` installs that release for this run only.** It is not remembered, so a later plain re-run still upgrades. An installation that was pinned with `--version` moves its pin to the new version. Without a version, the installer installs the default of the script you ran.
- **`--version X.Y.Z` is for pinning on purpose.** It is remembered in `/opt/nebulactrl/install.conf`, so a later plain re-run installs the same version again, not a newer one. `--version latest` drops the pin. Use it when you want to stay on a version.
- **A plain re-run installs the default of the script you ran.** The script from `nebulactrl.com` installs the release the site was last published with. A script fetched from your own control plane (`https://<your host>/install.sh`) installs **that control plane's own version**, so using it does not upgrade anything.
- Options such as `--domain` are remembered either way, so you do not repeat them.

A host with no route to `ghcr.io` loads the image from an archive instead: `--image nebulactrl:X.Y.Z --image-archive <file>` ([upgrade](../installing/upgrade.md#upgrade-the-control-plane-and-the-hosts-agent)).

## Settings

Set these in `/opt/nebulactrl/.env` and run `cd /opt/nebulactrl && docker compose -p nebulactrl up -d` (or re-run the installer, which keeps both) to apply them. A value that is not `true` or `false`, or a feed URL that is not `https`, stops the start: a plain `http` feed can be changed on the way, and the update it announces runs as root.

| Variable | Default | Meaning |
|---|---|---|
| `NEBULACTRL_UPDATE_CHECK` | `true` | `false` stops the daily check, the notice and the Update button. The control plane then makes no request to the feed at all. |
| `NEBULACTRL_UPDATE_FEED_URL` | `https://nebulactrl.com/releases/latest.json` | Where the check reads the feed, for a mirror or an air gapped network. See [the feed](#the-feed) for where the update gets the installer. |

## The feed

`https://nebulactrl.com/releases/latest.json` is written by the release workflow, next to `releases/<version>.json` (the same object for that release). It is a static file:

```json
{
  "version": "0.1.2",
  "publishedAt": "2026-10-29T12:34:56Z",
  "notesUrl": "",
  "notes": "## 0.1.2\n\n### Bug fixes\n\n* ..."
}
```

| Field | Meaning |
|---|---|
| `version` | The release, without the `v`. |
| `publishedAt` | ISO 8601, UTC. |
| `notesUrl` | The release page. |
| `notes` | The release notes as Markdown, trimmed. Longer than 20,000 characters they are cut and end with a link to `notesUrl`. |
| `minAgent` | Optional. The official feed does not set it. |

To **mirror** it (a network that cannot reach nebulactrl.com), copy the file to a host you control, serve it as `application/json` over **https**, and set `NEBULACTRL_UPDATE_FEED_URL` to its address. The one click update downloads the installer from `<feed origin>/install.sh` when the feed URL ends in `/releases/latest.json` (so serve `/install.sh` from the same origin), else from `https://nebulactrl.com/install.sh`. The installer is **signed**: serve `install.sh.minisig` next to it (from the release, unchanged). The control plane checks the signature before it sends anything, and the agent checks it again with the key compiled into it before the installer runs as root, so a mirror cannot make an update run anything the release did not sign ([verifying a release](../installing/installer.md#verifying-a-release)). Credentials in the feed URL (`https://user:token@mirror/...`) are used to read the feed only: they are never put in the manual command or on the agent's command line, so `install.sh` and `install.sh.minisig` must be readable without them. A mirror that cannot serve them means updating by hand. With the check off or no feed, you update by hand whenever you choose.

## API

| Operation | Endpoint | Needs |
|---|---|---|
| `getSystemUpdate` | `GET /api/v1/system/update` | any signed-in user |
| `startSystemUpdate` | `POST /api/v1/system/update` `{version}` | instance admin, `Idempotency-Key` |
| `checkSystemUpdate` | `POST /api/v1/system/update/check` | instance admin |

`GET` returns the running version (`current`), the announced one (`latest`), whether it is newer (`available`), the notes (`notes`, `notesUrl`, `publishedAt`), when the last check ran and how it ended (`checkedAt`, `checkError`), whether checks are on (`checkEnabled`), whether **you** are the instance admin (`isAdmin`), whether **you** can start an update now (`canUpdate`, with `cannotUpdateReason` when you cannot), the manual command (`manualCommand`, empty unless you are the instance admin) and, while an update runs and for an hour after it, `updating` (`state` `running`, `succeeded` or `failed`, `fromVersion`, `targetVersion`, `startedAt`, `finishedAt`, `error`).

`POST /api/v1/system/update` answers `202` with the same body. `version` must be the announced `latest`. Errors: `403` when you are not the instance admin, `409 up_to_date` (nothing newer), `409 update_in_progress`, `422 update_unavailable` (no local server, agent offline, too old or without systemd; the message says which). This operation is not available over MCP.

`422 update_unavailable` is also what an installer that does not verify against the release signature gets (no `install.sh.minisig`, a changed file, another key): nothing is sent to the agent.

Audit log: `instance.update.start` (the instance admin who started it, from which version to which), then `instance.update.succeeded` or `instance.update.failed` (recorded by the control plane itself, actor system, when the outcome is known).

`POST /api/v1/system/update/check` is the **Check now** button: it reads the feed at once and answers with the same body as `GET`. Errors: `409 update_check_disabled` (checks are off), `502 update_check_failed` (the feed could not be read; the last known release stays), `429` with `Retry-After` when the feed was checked less than 30 seconds ago. Over MCP it is available as a probe.
