---
title: Upgrades: agents and the control plane
description: "Agents upgrade themselves to the control plane's version; the control plane backs itself up before it upgrades."
section: Servers
order: 95
---

# Upgrades: agents and the control plane

Two things happen when NebulaCtrl upgrades:

- **Every agent catches up on its own.** An agent that is behind the control plane downloads the new binary from the control plane it is connected to, checks it, replaces itself and restarts. If the new binary does not start, it puts the old one back.
- **The control plane backs up its database first.** Before it migrates its schema, it takes a control-plane backup and refuses to go on when that backup failed.

Upgrading the control plane itself is a separate step: [update it from the dashboard](updating.md) (one click for the instance admin, which runs the installer on the control plane's host), or re-run the installer by hand ([upgrade guide](../installing/upgrade.md)). The Docker socket stays out of the control plane either way: the installer is started by the agent on its host.

## Agent upgrades

### What happens

The control plane runs version `V` and serves the matching agent binary at `/agent/v1/download/<amd64|arm64>`. For an agent on an older version it sends one `agent_upgrade` command that carries `V` and the **SHA-256** of that binary. The command travels over the agent's authenticated connection and holds no secret.

The agent then:

1. downloads the binary from the control plane URL it was started with (over TLS; plain HTTP only where the agent already accepts it) into a temporary file next to its own binary, hashing while it reads;
2. compares the hash with the one in the command; a mismatch deletes the file and fails the upgrade, nothing changed;
3. runs `<new> --version` and requires `V` (catches a wrong architecture or a corrupt file);
4. keeps the running binary as `<binary>.prev` (a hard link, or a copy) and writes `upgrade.json` in its state directory;
5. replaces its binary with one atomic `rename`: the path holds a whole binary at every moment;
6. reports success, waits until its **other running commands are done** (a build, a backup, a deployment; at most 45 minutes), then exits so that systemd (`Restart=always`) or the container's restart policy starts the new binary.

Your services and the proxy keep running; only the agent restarts. The control plane marks the upgrade **succeeded** when the agent reconnects with the target version.

### If the new binary does not work

The new binary starts "on probation". It is kept once it has stayed connected to the control plane for 20 seconds. It is rolled back (`<binary>.prev` is renamed over it and the agent exits to restart) when:

- it started 3 times without staying connected (crash loop), or
- it was not connected within 5 minutes of its start.

The agent remembers the rolled-back version and does not install it again by itself for 24 hours; the control plane does not retry it automatically either. The server's Updates tab shows the reason (**Last upgrade** → *The agent rolled the upgrade back: …*). **Retry anyway** there (or `force` in the API) installs the version again.

After a successful upgrade `<binary>.prev` stays as one generation for a manual rollback: `mv /usr/local/bin/nebulactrl-agent.prev /usr/local/bin/nebulactrl-agent && systemctl restart nebulactrl-agent`.

### Automatic and staged

- **Automatic upgrade** (Servers page, on by default; instance administrator only). When an agent that is **provably behind** connects (both versions are release versions and the agent's is lower), it is upgraded right away. Each server upgrades independently when it connects, which is also how a control-plane upgrade spreads: agents reconnect over a few minutes. A development build (`dev`, `1.4.0-3-gabc1234`) or an agent that is newer than the control plane is never upgraded automatically.
- **Upgrade agents** (button on the Servers page; Owners and Admins). Upgrades the servers that can be upgraded now **one at a time**, in name order: the next server starts when the previous agent has reconnected with the new version, and the **first failure stops the rollout**, leaving the remaining servers untouched. Needs the `servers:write` scope. A single server has its own **Upgrade agent** button on its Updates tab.

Both paths are audited: `server.agent.upgrade` (a person), `server.agent.upgrade.auto`, `server.agent.upgraded`, `server.agent.upgrade.failed`, `server.agent.upgrade.rollout`, `server.agent.upgrade.halted` and `instance.agent_auto_upgrade.update`.

### Version skew

Every server card shows how its agent compares with the control plane: *Behind 1.5.0*, *Newer than the control plane*, *Version not comparable* (a development build), or nothing when they match. The same data is `GET /api/v1/instance/upgrade`.

| Skew | Meaning | Upgrade |
|---|---|---|
| `current` | same version | none |
| `behind` | the agent is older | automatic, or on request |
| `ahead` | the agent is newer | never; upgrade the control plane first |
| `unknown` | a version is not a release version | on request only |

### When an agent cannot upgrade itself

The server's Updates tab says why. The common reasons:

| Reason | What to do |
|---|---|
| The agent predates self-upgrade | Re-run the agent installer on the host once ([upgrade guide](../installing/upgrade.md#upgrade-other-servers-by-hand)); later upgrades are automatic. |
| The directory of the agent binary is not writable (a read-only container image) | Run the agent with a writable binary directory, or upgrade by re-creating the container from the new image. |
| The server is offline | Upgrades only run on a connected agent. |
| This control plane ships no binary for the server's architecture | Only `amd64` and `arm64` Linux are built. Development control planes have no binaries directory. |

An agent that runs in a container upgrades the binary inside the container's writable layer; the container restart policy restarts it. A container that is recreated from its image starts the image's version again and is upgraded again at the next connect, so for containers also bump the image tag.

### Files

| File | Holds |
|---|---|
| `<binary>.prev` | the previous binary |
| `<state-dir>/upgrade.json` | the last upgrade: states `swapped`, `committed`, `rolled_back`, `abandoned`, the start counter, the version not to retry |

The agent's last upgrade also appears in the host facts (`facts.agentUpgrade`) so the control plane learns about a rollback it was not connected for.

## Backup before a control-plane upgrade

The control plane takes a **control-plane backup** (the [instance backup](backup-restore.md): the same destination, dump format and manifest, trigger *Before upgrade*) before it migrates its database in two situations:

- **The installer is re-run.** Before it changes anything, the installer runs `nebulactrl-server pre-upgrade-backup` inside the running control plane. If that command does not exist (a control plane older than this feature), it dumps the database with `pg_dump` to `/var/backups/nebulactrl/pre-upgrade-<time>.dump` (mode 0600) instead.
- **The server starts with another version than the one that last ran on this database.** The check runs at startup, **before** the migrations: it compares its version with the one stored in the database. A development build (a version that is not a release such as `1.5.0`), a new database and an unchanged version skip it. If the installer already backed up for exactly this version in the last 24 hours, the server does not back up a second time.

What counts as failure:

| Outcome | What happens |
|---|---|
| Backed up | The upgrade goes on. The run shows in Admin → Control-plane backup → History as *Before upgrade*. |
| **The backup failed** (a destination is configured, but pg_dump or the upload failed) | **The upgrade stops.** The installer exits before it changes anything; the server exits at startup without migrating. The old control plane keeps running. |
| No destination is configured (no S3 bucket, no `NEBULACTRL_BACKUP_DIR`) | The upgrade goes on with a warning, and the dashboard says *No backup before the upgrade*. The installer always configures a local destination. |
| Forced | You asked to go on without it: `sudo bash install.sh --force-upgrade`, or `NEBULACTRL_FORCE_UPGRADE=true` for a server start. The outcome is recorded as *forced*. |

You can take it yourself at any time: `cd /opt/nebulactrl && docker compose exec nebulactrl nebulactrl-server pre-upgrade-backup` (exit status 0 backed up or nothing configured, 3 the backup failed, `--force` turns a failure into a warning).

The outcome of the last pre-upgrade backup is shown in Admin → Control-plane backup, and returned by `GET /api/v1/instance/upgrade` (`preUpgradeBackup`). Restoring it is the same as any control-plane backup: [restore](backup-restore.md#restore).

The backup does not contain `NEBULACTRL_SECRET_KEY`. Keep it apart from the dumps.

A large database makes the startup wait for the dump (limit 30 minutes). If the container's health check gives up earlier, take the backup first with the installer, which has no such limit: its result is reused.

## API

| Operation | Endpoint | Needs |
|---|---|---|
| `getInstanceUpgrade` | `GET /api/v1/instance/upgrade` | `servers:read` |
| `updateInstanceUpgrade` | `PATCH /api/v1/instance/upgrade` `{agentAutoUpgrade}` | `servers:write`, instance admin |
| `upgradeServerAgent` | `POST /api/v1/servers/{serverId}/agent-upgrade` `{force?}` | `servers:write`, Owner or Admin |
| `upgradeServerAgents` | `POST /api/v1/servers/agent-upgrades` `{serverIds?, force?}` | `servers:write`, Owner or Admin |

The upgrade endpoints answer `202`. Errors: `409 server_offline`, `agent_current`, `agent_newer`, `upgrade_in_progress`, `nothing_to_upgrade`; `422 agent_predates_upgrade`, `no_agent_binary`. They are not available over MCP: replacing the binary of a root process is for people.

## Settings

| Setting | Where | Default |
|---|---|---|
| Upgrade agents automatically | Servers page | on |
| `NEBULACTRL_FORCE_UPGRADE=true` | control-plane environment | off: a failed pre-upgrade backup stops the start |
| `--force-upgrade` | installer | off |

The control-plane update (the notice, the one click, `NEBULACTRL_UPDATE_CHECK`) is on [Updating NebulaCtrl](updating.md).
