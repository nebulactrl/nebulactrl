---
title: Install NebulaCtrl
description: "One command installs the control plane on a fresh machine and makes it your first server."
section: Installing
order: 10
---

# Installing NebulaCtrl

One command installs the control plane on a fresh machine, and the machine is the first server your apps can run on:

```sh
curl -fsSL https://<host>/install.sh | sudo bash -s -- --domain nebula.example.com --email you@example.com
```

`<host>` is where the installer is published: `https://nebulactrl.com/install.sh`, or `/install.sh` of any running control plane.

Without `--domain` the dashboard is served on `http://<ip>:8000` (plain HTTP, with a warning) so you can try NebulaCtrl on a machine that has no domain yet:

```sh
curl -fsSL https://<host>/install.sh | sudo bash
```

When it finishes it prints the dashboard URL and a **setup URL** with a one-time token. Open that, create the admin account, and deploy something.

Supported hosts: Debian 12/13, Ubuntu 22.04, 24.04 and 26.04, RHEL/Rocky/AlmaLinux 9; amd64 and arm64.

## What the installer does

It always does the following, in this order:

1. Checks the system (distribution, systemd, CPU, RAM, free ports) and installs the base packages it needs.
2. Installs **Docker Engine and the Compose plugin from Docker's official repository** if Docker is missing.
3. Applies **safe Docker daemon defaults**: log rotation, `live-restore`, `userland-proxy: false` and a default address pool that does not overlap the agent's.
4. **Generates every secret** (database password, the instance secret key, the setup token, the local agent's enrollment token) and keeps them in `/opt/nebulactrl/.env`, mode 0600.
5. Starts **Postgres 17 on a private, internal network** (SCRAM authentication, tuned to the machine's RAM, no published port) and the control plane in a hardened container.
6. **Serves the dashboard**: over HTTPS with a Let's Encrypt certificate when you give a domain, otherwise over plain HTTP on port 8000.
7. Schedules a **daily control-plane backup** to a local directory and reminds you to add an off-site destination.
8. Prints the setup URL.

These steps are also on by default; each has a flag to turn it off:

| Step | Default | Off with |
|---|---|---|
| Enroll this machine as the **first server** (an agent on the host, "deploy apps here") | on | `--no-local-server` |
| Firewall: SSH (detected port), 80, 443/tcp and 443/udp only (ufw, firewalld on RHEL) | on | `--no-firewall` |
| Automatic security updates (unattended-upgrades, dnf-automatic) | on | `--no-auto-updates` |
| fail2ban for SSH | on | `--no-fail2ban` |
| [Lynis](../security/host-hardening.md) audit and a curated set of safe fixes | on | `--no-hardening` |
| Daily control-plane backup | on | `--no-backup` |

Every step is **idempotent**: re-run the installer to upgrade or repair. It never regenerates a secret that exists, and it **never changes your SSH configuration** in a way that could lock you out.

## Pages

1. [The installer](installer.md): every option, every step, the files and ports it touches, HTTPS and the dashboard route, re-running and recovering
2. [Requirements and supported hosts](requirements.md)
3. [The control plane](control-plane.md): how it runs, the layout under `/opt/nebulactrl`, manual Compose install, first run
4. [Add more servers](servers.md): the first server, enrollment tokens, the agent installer, "Accept workloads"
5. [Upgrade and roll back](upgrade.md), and [Updating NebulaCtrl](../operations/updating.md): the update notice and the one click update
6. [Uninstall](uninstall.md)
7. [Migrate from Coolify](migrate-from-coolify.md): install beside Coolify on the same host, copy the data, cut over, roll back

Next, read [`docs/operations`](../operations/README.md) (backups first) and [`docs/security`](../security/README.md).

For a development setup (hot reload, dev agent, test database) see [`CONTRIBUTING.md`](../../CONTRIBUTING.md) instead.
