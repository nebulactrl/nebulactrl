---
title: Concepts
description: "Projects, environments, services, servers and staged changes: the few ideas the rest of NebulaCtrl is built on."
section: Getting started
order: 20
---

# Concepts

Six ideas explain almost everything you see in NebulaCtrl.

## Team

A **team** is the top level: it owns the projects, servers, API tokens, integrations and audit log. When you set up NebulaCtrl you create the first team and its owner. People join a team by invitation and get a role, which decides what they can change. See [teams, roles and invites](../operations/teams.md).

Settings live in three places. **Account settings** (your profile, security, API tokens and MCP clients) open from the user menu. **Team settings** (members, audit log, integrations, backup storage and the rest) are in the sidebar's TEAM group. **Admin** (updates, control-plane backup, sign-in and teams, dashboard access, email relay and MCP) is at the bottom of the sidebar and shows only for the instance admin.

## Project

A **project** groups the services of one product, for example "Shop" with its web app, worker and database. Projects are what you switch between in the top bar.

## Environment

Every project has **environments** such as `production` and `staging`. An environment is a separate copy of the project's services, with its own variables, domains and data. Each one has its own private network, so `staging` cannot reach `production` by accident. The environment's name is in the top bar, and production is marked so you notice where you are. See [networking](../operations/networking.md).

## Service

A **service** is one thing that runs: a web app, a worker, a database or a scheduled job. Its **source** is a Git repository (built into an image) or a ready-made Docker image. Services appear as cards on the **canvas**, and clicking one opens the **inspector** with tabs for Overview, Deployments, Variables, Backups, Metrics, Analytics, Logs and Settings.

The lines between cards are connections: they show which service talks to which, and connecting two cards proposes the right variable (for example `DATABASE_URL=${{pg.DATABASE_URL}}`) for you to confirm.

A database is a service too, created from **Create → Database**, with its credentials generated and its data kept on a volume. See [managed databases](../operations/databases.md).

## Server

A **server** is a Linux machine with Docker and the NebulaCtrl agent. Each service is placed on one server. A server's card shows whether it is online, its CPU, memory and disk, and the state of its proxy. The machine you install NebulaCtrl on is your first server. See [servers and agents](../operations/servers.md).

## Staged changes

Changing a service's settings, variables, domains or volumes does **not** restart it. The change is *staged*: the card shows a **Staged** chip, and a bar says "N changes to apply". Nothing about the running app changes until you deploy.

From that bar you can **Review** the exact differences (secret values stay hidden), **Discard** them, or **Deploy** to apply them together. Staging is what lets you change five things and restart once.

## Project file

A project can also be described in a file, `nebula.toml`, kept in your repository. Applying it **stages** changes exactly like editing in the dashboard; nothing is deployed until you deploy. A field the file sets is marked "set by nebula.toml" in the dashboard, and editing it here is allowed and shows as drift. See the [nebula.toml guide](../operations/project-config.md).

## Deployments and releases

A **deployment** is one attempt to get a service to its configured state. It moves through *preparing, building, releasing and verifying* and ends as `succeeded`, `failed`, `canceled`, `rolled_back` or `rollback_failed`. The words say what happened, and a failed one says at which step.

Each deployment is made from a **release**: a frozen record of the source (image digest or Git commit), the settings and the variables. You can **roll back** to any earlier release.

> [!NOTE]
> A rollback restores the application image and its configuration only. Databases, volumes and external services are not rolled back.
