---
title: 0007 Compose stacks
description: "Why Compose files run natively with docker compose."
section: Internals
order: 7
---

# 0007. Native Compose stacks

- Status: accepted
- Date: 2026-10-27

## Context

An ordinary service is one container, and a Docker Compose file can be imported once into ordinary services ([Compose import](../operations/compose-import.md)). That import drops what Compose cannot express here (build args, directory bind mounts, Compose secrets and configs, devices) and is never kept in sync. For many self-hosters "my `docker-compose.yml` runs as it is" is the first question. A Compose runtime contradicts "a service is one container" and the staged-change model, so it needed a decision.

## Decision

**A stack is one service whose runtime is `docker compose`.** Stacks are native, with the safe allow-list on by default and an explicit per-server escape hatch.

- **Model.** `kind = stack`, `sourceType = compose`. The compose file is part of the service configuration (`config.stack.compose`), so everything that already works on a service keeps working on the whole stack as one unit: staged changes and the generation counter, one deployment, one release, rollback, audit, variables, domains. The compose services are *components*; they have no rows of their own and appear in the observed state, the metrics, the logs and the canvas card.
- **Runtime.** The agent writes the compose file and a generated **override** to `<state-dir>/stacks/<serviceId8>/` and runs `docker compose -p nc-<serviceId8> -f compose.yaml -f override.yaml up -d --remove-orphans --wait`. The override (pure code in `pkg/composestack`, shared by the control plane and the agent) adds the labels every managed container carries, fixed container names (`nc-<serviceId8>-<component>`), joins each component to its environment network with the component name as DNS alias (the same isolation model as ordinary services, including egress blocking and shared networks), removes host port publishing, applies per-component resource limits and, for rollbacks, pins images to the digests the release recorded.
- **Routing.** Compose `ports:` are never published on the host. HTTP traffic reaches a component through a domain, which names the component and a container port; Traefik routes to `nc-<serviceId8>-<component>:<port>` on the environment network, so the zero-exposure model of ordinary services carries over. Published TCP/UDP ports are not supported.
- **Strategy.** Always `recreate`: `docker compose up` replaces changed containers in place. Stacks do not get the rolling strategy; the plan says why. After the containers are healthy the agent applies the routes and verifies through the proxy like for ordinary services; a failed `up`, health check or verification re-applies the previous compose file and override (`rolled_back`), a failed restore is `rollback_failed`.
- **Safety.** Docker is root on the host, and a compose file can ask for almost anything. By default the agent normalises the file with `docker compose config --format json` and refuses it unless it passes the allow-list: no `privileged`, no host/container network, PID, IPC, UTS or user namespaces, no devices or GPUs, no capabilities beyond the safe list (`NET_ADMIN` and `SYS_ADMIN` need an unrestricted server), no security options other than `no-new-privileges`, no host bind mounts (which includes the Docker socket), no external volumes or networks or custom volume/network names (they could reach another project's data or break environment isolation), no driver options that mount host paths, no `file:` secrets and configs, no reserved labels. The control plane runs the same check on the file as written, so mistakes surface when you save. An **instance admin** can switch a single server to **Unrestricted** (see [Unrestricted](#unrestricted) below); the allow-list is then not applied to the stacks of that server. A few things stay unsupported either way because the stack model cannot carry them (`build:`, `include`, `env_file`, `extends` with a file, `provider:` services, replicas above one).
- **Secrets.** A stack's variables are used for compose interpolation (`${VAR}`). They are sent as send-time secrets, never written to disk, and handed to compose in a clean environment (the agent's own environment, including its enrollment token, never reaches compose; user variables that would change how the root `docker compose` client runs are dropped: `DOCKER_*`, `COMPOSE_*` except `COMPOSE_PROFILES`, `BUILDKIT_*`, `BUILDX_*`, loader and interpreter injection (`LD_*`, `DYLD_*`, `NODE_OPTIONS`, `PYTHONPATH`, …), `PATH` and `HOME`).
- **Requirement.** The Docker Compose plugin 2.24 or newer (the override uses `!reset` and `!override`). The agent reports its version in the facts and advertises the `compose` capability; the control plane refuses stack deploys on a server without it, and the installer installs the plugin.

## Consequences

- Compose files run with their own semantics (healthchecks, `depends_on`, `restart`, named volumes, `command`, `entrypoint`, `user`, anything the allow-list lets through) instead of an approximation.
- A stack is coarser than services: one deploy button, one rollback, no per-component staging, no rolling update, no replicas, no per-component volume backups or one-off tasks, no maintenance mode. Users who need those use "Split into services".
- Images are pulled by tag at deploy time; a rollback runs the digests recorded for the release. Data is never rolled back, as everywhere else.
- Named volumes live under the compose project name (`nc-<serviceId8>_<name>`); deleting a stack keeps them unless the user ticks "delete volumes".
- The trust boundary documented in the threat model does not move for restricted servers (Docker is still root, but the allow-list keeps a compose file from asking for the host); on an Unrestricted server any deployer can take over the host, and the UI says so.
- Compose evolves; the agent checks the normalised model rather than the YAML text so new syntaxes cannot slip past, but a new Compose key that grants host access would be allowed until the allow-list learns it. The check is a deny-by-key list for risky keys plus structural rules for volumes, networks and namespaces, not a sandbox.

## Alternatives considered

- **Keep import only.** Cheap, but "my `docker-compose.yml` runs as it is" stays unmet.
- **One service row per compose service with a stack parent.** Gives per-component nodes, domains and variables, but every deployment-level feature (generation, rollback, staged changes, cancel) would have to be coordinated across rows, and a stack could end up half deployed. Components as observed state of one service keeps the unit of change atomic.
- **Translate the compose file into our own container creation** (what import does). That is the import path; it cannot keep Compose behaviour such as `depends_on` health conditions and init containers exact.
- **Free-form compose without an allow-list.** Rejected: the default must be safe.
- **Swarm or `docker stack`.** Out of scope (no cluster mode, ADR 0001).

## Unrestricted

Stacks and plain-service [Docker options](../operations/docker-options.md) share one per-server switch, **Unrestricted** (`servers.unrestricted`, `PUT /servers/{id}/unrestricted`), because the two give identical power. It is instance admin only, with the server's name typed to confirm, and audited as `server.unrestricted.enable`/`.disable`.
