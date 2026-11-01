---
title: Add a server
description: "Enroll a Linux host with Docker so NebulaCtrl can deploy to it."
section: Servers
order: 10
---

# Servers (install agents)

## The first server

The [installer](installer.md) enrolls the control-plane host itself: it installs the agent there and creates the server (flagged **control plane**), which is online by the time you finish setup. Its proxy serves the dashboard and your apps. Its **Accept workloads** setting decides whether services can be scheduled on it (on by default; see [the installer](installer.md#accept-workloads)). The rest of this page is for *additional* servers.

## Additional servers

Each other Docker host that runs services needs the NebulaCtrl agent. The agent is a single static binary; the control plane serves it at `/agent/v1/download/{amd64|arm64}` and serves the installer at `/install-agent.sh`. Check the [requirements](requirements.md) first: Debian 12/13, Ubuntu 22.04, 24.04 or 26.04, or RHEL/Rocky/AlmaLinux 9, Docker 24+ with the Compose plugin 2.24+, ports 80/443 free. The agent installer needs Docker to be installed already; it does not install it.


## 1. Create an enrollment token

In the UI: **Servers → Add server**. Enter a name and, optionally:

| Field | Used for |
|---|---|
| Public IP | DNS checks (A records are compared against it) and generated `*.sslip.io` domains |
| Base domain | Generated domains become `<service>-<env>-<project>.<base-domain>` |
| ACME email | Contact for Let's Encrypt certificates issued by this host's Traefik |

The dialog shows the install command. The token (`nct_enroll_…`) is **single use, expires after one hour and can be revoked**; only its hash is stored.

## 2. Run the installer on the target host

```sh
curl -fsSL https://nebula.example.com/install-agent.sh | sudo sh -s -- \
  --url https://nebula.example.com --token nct_enroll_XXXXXXXX
```

The script:

1. checks that it runs as root, on Linux with systemd, with `curl`/`wget`, and that Docker is installed, reachable and **24 or newer**; it warns if port 80/443 is already in use;
2. downloads the right binary for `uname -m` to `/usr/local/bin/nebulactrl-agent`;
3. writes `/etc/systemd/system/nebulactrl-agent.service` (reference copy: [`deploy/agent/nebulactrl-agent.service`](../../deploy/agent/nebulactrl-agent.service)) and, on first install, `/etc/nebulactrl-agent.env` (mode 0600) containing the one-time token;
4. enables and starts the service and fails if it does not stay running.

Options: `--proxy-http-port N` and `--proxy-https-port N` move Traefik's public listeners; `--proxy-admin-port N` moves its admin port (default 9000, bound to 127.0.0.1 only); `--network-pool CIDR` and `--network-prefix-len N` set the address range environment networks are allocated from (default `10.213.0.0/16` in /24s; see [Networking](../operations/networking.md#address-pool)); `--insecure-dev` allows a plain-`http://` control plane (development only). Re-running the installer upgrades the binary and keeps the identity (no token needed). The agent binary itself also reads `--uptime-interval` / `NEBULACTRL_UPTIME_INTERVAL` (default `30s`), a test and diagnostic knob for how often it probes services; leave it unset (see [advanced settings](../operations/configuration.md)).

On first start the agent generates an Ed25519 keypair, redeems the token, stores its identity in `/var/lib/nebulactrl-agent/agent.db` (mode 0600), starts its **Traefik proxy container** (`nebulactrl-traefik`, network `nebulactrl-edge`) and connects. The server turns **online** within seconds, with its Docker and agent versions, CPU/memory/disk and proxy status.

## 3. Verify

- UI: the server card shows *online*, a recent "last seen", and **Proxy: running**. A **Proxy error** (typically port 80 in use) is shown with its reason; fix it and restart the agent (`systemctl restart nebulactrl-agent`).
- Host: `systemctl status nebulactrl-agent`, `journalctl -u nebulactrl-agent -f`, `docker ps --filter name=nebulactrl-traefik`.
- Firewall: allow inbound **80/tcp, 443/tcp, 443/udp** from the internet to this host for public services. **No inbound port is needed for the control plane**; the agent only needs outbound HTTPS to it.

## What lives on the host

| Item | Where |
|---|---|
| Agent binary, unit, token file | `/usr/local/bin/nebulactrl-agent`, `/etc/systemd/system/nebulactrl-agent.service`, `/etc/nebulactrl-agent.env` |
| Identity and in-flight command state | `/var/lib/nebulactrl-agent/agent.db` |
| Git build workspaces | `/var/lib/nebulactrl-agent/builds/<deploymentId>` (removed after each build) |
| Proxy | container `nebulactrl-traefik`, network `nebulactrl-edge`, volume `nebulactrl-traefik-acme` (ACME certificates in `acme.json`: keep it) |
| Your services | containers named `nc-<service>-<deploymentId8>-<replica>`, labelled `nebulactrl.managed=true`; volumes `nc-vol-<serviceId8>-<name>`; networks `nebulactrl-env-<environmentId8>`, `nebulactrl-env-<environmentId8>-int` and `nebulactrl-shared-<id8>` |

## Revoking and re-enrolling

See [operations: servers](../operations/servers.md). Short version: **Servers → server → Settings → Danger zone → Revoke** closes the agent's connection immediately and the agent exits with "agent revoked; re-enroll". To re-enroll that host, run `install-agent.sh --uninstall --purge`, create a new enrollment token and run the installer again.

## Removing a server

**Servers → server → Settings → Danger zone → Remove server…** deletes the server from NebulaCtrl. With **clean** (the default for an online server) the agent first removes every container, network and volume NebulaCtrl manages on the host, including the proxy and its data, and then removes itself: `/usr/local/bin/nebulactrl-agent`, `/etc/systemd/system/nebulactrl-agent.service`, `/etc/nebulactrl-agent.env` and `/var/lib/nebulactrl-agent`. Docker and everything that is not NebulaCtrl's stay. With **forget** (and always for an offline server) the host is not touched: it keeps its containers and the agent's files; clean it by hand with `curl -fsSL https://<control-plane>/install-agent.sh | sudo sh -s -- --uninstall --purge` plus `docker ps -aq --filter label=nebulactrl.managed | xargs -r docker rm -f`. Services must be moved or deleted first, and the server NebulaCtrl runs on cannot be removed. Details: [operations: servers](../operations/servers.md#remove-a-server).

## Several agents

Add one server per Docker host. Each service is placed on one explicitly chosen server. Hosts do **not** share a private network: services on different hosts cannot reach each other by service name ([limitations](../operations/limitations.md)).
