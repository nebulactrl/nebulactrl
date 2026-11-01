---
title: 0001 Go modular monolith
description: "Why the control plane is a single Go binary split by domain."
section: Internals
order: 1
---

# 0001. Go modular monolith for the control plane

- Status: accepted
- Date: 2026-10-25

## Context

NebulaCtrl is meant to be a lightweight self-hosted platform: one control-plane process (API, static web, optional MCP, background workers) plus one small agent per Docker host, backed by PostgreSQL. UI, CLI and MCP must call the same authorized domain services. Operating it should not require a fleet of services.

## Decision

- The control plane is a **single Go binary** (`nebulactrl-server`), split into internal packages by domain (`projects`, `environments`, `services`, `variables`, `domains`, `deployments`, `servers`, `agents`, `reconciler`, `jobs`, `mcp`, …). Packages communicate through Go interfaces, not network calls. Domain service methods call `policy.Require` and `audit.Record`; HTTP handlers and MCP tools are thin adapters over them.
- The agent (`nebulactrl-agent`) and the CLI (`nebulactrl`) are separate static Go binaries in the same module and share `pkg/agentprotocol`.
- Libraries instead of frameworks: chi + huma (OpenAPI 3.1 and RFC 9457 errors generated from Go types), pgx + sqlc + goose, River for jobs in the same Postgres, scs sessions with argon2id, `coder/websocket`, the moby client, go-git, the official Go MCP SDK, cobra.
- Postgres is the only stateful dependency. Jobs, sessions, idempotency keys and the command queue live there; no Redis, no message broker.
- Live UI updates use SSE from an in-process event bus. A single control-plane instance is assumed.
- The web app is built separately (React, Vite) and embedded into the server binary for release; in development Vite proxies `/api`, `/agent` and `/mcp` to the server.

## Consequences

- One artifact to deploy, back up and upgrade; transactional consistency between domain writes, audit rows and job inserts (River inserts in the same transaction).
- Module boundaries are enforced by convention and review only; keep packages free of import cycles and keep handlers free of business rules.
- The monolith is not horizontally scalable as written: the websocket hub and event bus are in-process. Splitting workers out later is possible because River and Postgres already mediate all work, but it is not designed for.
- Go on both sides gives one toolchain and a shared protocol package, at the cost of the web stack being a second ecosystem.

## Alternatives considered

- **Laravel/PHP control plane**: a second language next to the Go agent, with no shared protocol package.
- **Rust/Axum**: strong, but more complexity across the whole system for no product advantage.
- **A TypeScript full stack**: a separate language from the agent and no shared protocol package.
- **Microservices** (API, scheduler, hub as separate processes): rejected because the goal is a lightweight system, not a heavy microservices one.
