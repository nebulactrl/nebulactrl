---
title: Requirements
description: "Supported operating systems, architectures, ports and Docker versions."
section: Installing
order: 20
---

# Requirements and supported hosts

## Every host (control plane and servers)

| | Supported |
|---|---|
| OS | **Debian 12 and 13, Ubuntu 22.04, 24.04 and 26.04, RHEL, Rocky Linux and AlmaLinux 9** (systemd required) |
| CPU | **amd64** and **arm64** (`uname -m` = `x86_64` or `aarch64`) |
| Docker | **Docker Engine 24 or newer** from Docker's official repository; the control-plane installer installs it |
| Docker Compose plugin | **`docker compose` 2.24 or newer** (`docker-compose-plugin` from Docker's repository). The installer installs it with Docker, and installs or upgrades it when Docker is already there without it. Compose stacks need 2.24; servers without it refuse stack deploys |
| Access | root (the agent runs as root because the Docker socket is root-equivalent) |
| Network | Outbound HTTPS (Docker's repository, registries including `ghcr.io` for the NebulaCtrl image, git hosts, Let's Encrypt, `nebulactrl.com` for the control plane's daily [release check](../operations/updating.md#what-the-daily-check-sends), and for servers the control plane) |
| Ports | **80/tcp, 443/tcp and 443/udp free** for the host's Traefik proxy, plus **9000 on 127.0.0.1** for its admin entrypoint (never exposed). If 80/443 are taken, the server shows a proxy error and adding domains fails with `proxy_unavailable`. `--proxy-http-port`, `--proxy-https-port` and `--proxy-admin-port` move them (agent installer) |
| Disk | Images, build cache and volumes live in Docker's data root. A build needs space for the clone, cache and the image. The server card warns when the root filesystem crosses its threshold (default 80 %) and the scheduled Docker cleanup keeps images and cache in check |

The installer refuses an unsupported distribution (`--force-unsupported` tries a Debian or RHEL family system anyway). macOS and Windows hosts are not supported; Docker Desktop is for development only.

Nothing needs to listen on a *server* for the control plane: the agent connects outbound, so hosts behind NAT or a home router work. Only the proxy's 80/443 must be reachable by your visitors (and by Let's Encrypt).

## The control-plane host

It is also a server (its agent runs the proxy that serves the dashboard), so everything above applies, plus:

| | Needed |
|---|---|
| CPU / RAM | 1 vCPU and **1 GB** for the control plane alone; plan for the apps you will deploy there too (turn **Accept workloads** off to keep the host for NebulaCtrl). Postgres is tuned to the RAM it finds |
| Disk | Postgres data and the local backups (`/var/backups/nebulactrl`); deployment logs are capped at 10,000 lines each and metrics are pruned ([monitoring](../operations/monitoring.md)) |
| Network | Reachable by every agent and by the browsers that use the UI |
| DNS | For HTTPS: a hostname whose **A record points at this machine** before you run the installer with `--domain`. Without a domain the dashboard is plain HTTP on port 8000 |
| Time | NTP: enrollment tokens, sessions and the 30 second handshake window are time based |

The control plane is a **single instance**: no clustering, and nothing takes over when it is down ([limitations](../operations/limitations.md)).

## Domains for your services

- A wildcard or per-service DNS A record pointing at the *server's* public IP (the UI shows exactly what to create).
- Or no DNS at all: set the server's public IP and use **Generate domain**, which creates `<service>-<env>-<project>.<public-ip>.sslip.io`.
- `*.localhost`, `*.local`, bare names and IP addresses get Traefik's default self-signed certificate (browsers warn; use `curl -k`).
- Certificates for other names come from Let's Encrypt via Traefik (HTTP-01 on port 80), or DNS-01 with a provider; set an ACME email on the server (the installer does for the first one).
