---
title: What is NebulaCtrl?
description: "A self-hosted platform for deploying apps on your own Docker servers, with a canvas, a CLI and an API."
section: Getting started
order: 10
---

# What is NebulaCtrl?

NebulaCtrl is a self-hosted platform for running your apps on servers you own. You connect a Linux machine with Docker, point NebulaCtrl at a Git repository or a container image, and it builds, starts, routes and watches the result. There is no Kubernetes, no Swarm and no hosted service in the middle: one control plane, one small agent per server, and plain Docker underneath.

Everything runs on machines you control, and the interface is a canvas where you see your services and the lines between them.

NebulaCtrl is free to use and runs on your own servers. It is open source under the AGPL-3.0, and the installer, the container images and the CLI are all downloadable without an account: the installer from `nebulactrl.com`, the images from the public registry `ghcr.io/nebulactrl/nebulactrl`, the CLI from your own control plane.

## What you get

- **Deploys from Git or an image.** Push to a branch and deploy, or pull `ghcr.io/you/app:1.4`. A repository without a Dockerfile is built with Railpack. See [builds](../operations/builds.md).
- **A deploy you can trust.** A new version is started and health-checked *before* traffic moves to it, and a failed deploy leaves the old one serving. See [deployments](../operations/deployments.md).
- **Domains and HTTPS.** Every server runs a proxy that gets Let's Encrypt certificates for you. See [domains and proxy routes](../operations/proxy-routes.md).
- **Variables and secrets** that are encrypted at rest, can refer to each other (`${{db.DATABASE_URL}}`), and are never shown again once saved. See [variables and secrets](../operations/variables.md).
- **Databases and backups**: PostgreSQL, MySQL, MariaDB, Valkey and more, with scheduled backups. See [managed databases](../operations/databases.md).
- **Previews, scaling, scheduled tasks, a web terminal**, and a catalogue of one-click [templates](../operations/templates.md).
- **Several servers** in one place, including machines behind a home router: agents dial out, so no inbound port is needed for the control plane.
- **A CLI and an API** that do everything the dashboard does. See the [CLI reference](../reference/cli.md) and the [API reference](../reference/api.md).

## How the pieces fit

```
   you ──► dashboard / CLI / API ──► control plane ──► Postgres
                                          ▲
                           outbound WebSocket from each agent
                                          │
             server A: agent ─ proxy ─ your containers
             server B: agent ─ proxy ─ your containers
```

- The **control plane** is one Go program with a Postgres database. It holds your projects, settings and history, and tells agents what to run.
- An **agent** runs on each server. It builds images, starts containers, runs health checks and reports back. It connects *out* to the control plane, so servers behind NAT work.
- A **proxy** (Traefik) on each server terminates HTTPS and routes each domain to the right container. The agent manages it; you never edit its config.

The control plane can go down without stopping your apps. Containers keep running; you only lose the ability to change things until it is back.

## What it is not

NebulaCtrl is for a trusted team running its own infrastructure. A NebulaCtrl administrator is a root administrator of every connected server, because Docker access is root. It is not a way to host strangers' code safely. Read the [threat model](../security/threat-model.md) and the [known limitations](../operations/limitations.md) before you rely on it.

## Where next

1. [Learn the vocabulary](concepts.md): projects, environments, services, servers and staged changes.
2. [Follow the quickstart](quickstart.md): from a bare machine to a running app with a domain and a database.
