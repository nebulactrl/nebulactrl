---
title: Quickstart
description: "From a bare machine to a running app with a domain and a database."
section: Getting started
order: 30
---

# Quickstart

This walks you from nothing to an app on the internet with its own domain and a PostgreSQL database. Allow about twenty minutes, most of it waiting for downloads.

You need:

- A Linux machine you can SSH into as root or with `sudo`: Debian 12/13, Ubuntu 22.04, 24.04 or 26.04, or RHEL, Rocky or AlmaLinux 9, on amd64 or arm64. A small VPS with 2 GB of RAM is plenty. See [requirements](../installing/requirements.md).
- Ports 80 and 443 free on it.
- Optional but recommended: a domain you control, with an A record pointing at the machine.

## 1. Install

On the machine, run the installer. With a domain for the dashboard:

```sh
curl -fsSL https://nebulactrl.com/install.sh | sudo bash -s -- --domain nebula.example.com --email you@example.com
```

Without a domain, run it plain and the dashboard is served over HTTP on port 8000, which is fine for trying things out:

```sh
curl -fsSL https://nebulactrl.com/install.sh | sudo bash
```

The installer is published at `https://nebulactrl.com/install.sh`, and every control plane serves the same script at its own `/install.sh`; see [install NebulaCtrl](../installing/README.md). The installer sets up Docker if it is missing, generates every secret, starts the database and the control plane, and prints two things at the end: the **dashboard URL** and a **setup URL** with a one-time token. The control-plane image is public (`ghcr.io/nebulactrl/nebulactrl`), so nothing on your machine needs a GitHub or registry login.

> [!WARNING]
> Store `NEBULACTRL_SECRET_KEY` somewhere other than the machine. Without it, a restored backup cannot decrypt your variables. The installer prints the command to read it: `sudo grep NEBULACTRL_SECRET_KEY /opt/nebulactrl/.env`.

Open the setup URL, choose a team name, and create your account. You land on the projects page, and the machine you installed on is already listed under **Servers** as your first server.

## 2. Add another server (optional)

Skip this if one machine is enough: the first server can run your apps. To add a second host:

1. Open **Servers** and choose **Add server**.
2. Give it a name. If you know them, add its public IP, a base domain and an email for Let's Encrypt. The base domain is what generated domains hang off.
3. Choose **Create install command**, copy it, and run it on the new host. The host needs Docker 24 or newer already installed.

The dialog waits for the agent. When it connects, the server shows as online with its CPU, memory and disk. Details and troubleshooting are in [add a server](../installing/servers.md).

## 3. Create a project

On the projects page choose **New project**, name it, and open it. A project starts with a `production` environment and an empty canvas that says "Create your first service".

## 4. Deploy your first app

We will use `traefik/whoami`, a tiny web server that prints information about the request it received. It is a good first app because it has nothing to configure.

1. On the canvas choose **Create**, then **Docker image**.
2. Enter `traefik/whoami:latest` as the image. The name fills in as `whoami`.
3. Pick the server to run it on, and set the port to `80`.
4. Choose **Create service**.

The card appears with a **Staged** chip, and the toast says nothing runs until you deploy. In production the toast offers **Review & deploy**; in other environments it offers **Deploy now**. Either way, the **Deploy · 1** button in the top bar applies your staged changes.

Watch the card: it goes through *Preparing*, *Releasing* and *Verifying*, and settles on a deployed state when the health check passes. Open the service and its **Deployments** tab to see each step and the log.

To deploy from Git instead, choose **Create → Git repository**. A repository with a Dockerfile uses it; one without gets a build plan from Railpack. See [builds](../operations/builds.md) and [Git providers](../operations/git-providers.md).

## 5. Give it a domain

1. Open the service, then **Settings → Networking**.
2. If you set a base domain or public IP on the server, choose **Generate domain**. It creates a hostname like `whoami-production-myproject.<base domain>`, or one ending in `.sslip.io` when you have only an IP.
3. Or type your own hostname, for example `whoami.example.com`, and choose **Add domain**. Point an A record for it at the server's public IP; in a zone of a [connected Cloudflare account](../operations/cloudflare.md) no record is needed, NebulaCtrl creates it.

A domain is a staged change too. Choose **Deploy** to apply it. The proxy requests a certificate and routes the hostname to your container. Open the domain in a browser and you should see the whoami output over HTTPS.

If the page does not load, the domain list shows the DNS status for each hostname, and [troubleshooting](../operations/troubleshooting.md) covers the usual causes.

## 6. Add a database

1. On the canvas choose **Create → Database** and pick PostgreSQL and a version.
2. Choose **Create service**, then deploy it. A database is created with generated credentials and a data volume.
3. In your app's **Variables** tab, add `DATABASE_URL` with the value `${{pg.DATABASE_URL}}`, where `pg` is the database's name. Or drag a line from your app's card to the database and accept the variable NebulaCtrl proposes.
4. The **N changes to apply** bar appears. Choose **Deploy** to restart the app with the new variable.

The password in `DATABASE_URL` is a secret: it is masked in the dashboard and redacted from logs. The app and the database reach each other by private hostname because they are on the same server and environment. See [variables and secrets](../operations/variables.md) and [managed databases](../operations/databases.md).

## Where next

- Turn on a [daily backup of your databases](../operations/database-backups.md), and an off-site destination for the [control-plane backup](../operations/backup-restore.md).
- Deploy on every push with [Git providers](../operations/git-providers.md) or a deploy hook.
- Invite your team: [teams, roles and invites](../operations/teams.md).
- Install something ready-made from [templates](../operations/templates.md).
- Use the [CLI](../reference/cli.md): `nebulactrl services deploy <service> --wait` fits in a CI job. Your control plane serves it: `curl -fsSL https://<your control plane>/cli.sh | sh` installs the build that matches it (also under **Account settings → API tokens**), then `nebulactrl login --url https://<your control plane> --token <API token>`.
