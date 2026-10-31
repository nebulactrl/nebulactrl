---
title: 0006 Railpack builder
description: "Why Railpack is the zero-config builder and a Dockerfile always wins."
section: Internals
order: 6
---

# 0006. Railpack is the zero-config builder; Dockerfile always wins

- Status: accepted
- Date: 2026-10-26
- Amends: [0004](0004-build-on-target-host.md) (builds run on the target host **or on a build server** ([0008](0008-build-servers.md)); this adds how)

## Context

ADR 0004 builds a git source with the repository's Dockerfile only, which leaves a repository without one with nothing to build. The decisions: a repository without a Dockerfile builds with Railpack by default, generated starter Dockerfiles remain as an alternative, a Dockerfile always wins, and never Nixpacks (in maintenance mode).

Railpack is MIT and written in Go. When it was chosen it was 0.x and about 20 months old, so it is an explicit exception to the library rule (dependencies must be stable for two years). The exception is contained by pinning and by being replaceable: the build method is one value in the service config.

## Decision

**Build types.** `services.config.buildType` is one of:

| Value | Behaviour |
|---|---|
| `auto` (default) | The repository's Dockerfile if it exists at `dockerfilePath`, otherwise Railpack. A `dockerfilePath` the user changed from the default and that is missing is an error, never a silent switch to Railpack. |
| `dockerfile` | The Dockerfile; its absence is an error. |
| `railpack` | Railpack, even when a Dockerfile exists. |
| `starter:<lang>` | A generated Dockerfile for `node`, `python`, `go`, `ruby`, `php` or `static`. It is written next to the clone (never into it), printed to the build log, and built like any Dockerfile. |

The agent decides after the clone, because only it can see the files. The control plane only carries the setting (`GitSource.BuildType`).

**Pinned Railpack: 0.43.0** (released 2026-10-19). Three constants in `pkg/agentprotocol/build.go` move together:

- `RailpackVersion = "0.43.0"`
- `RailpackFrontendImage = ghcr.io/railwayapp/railpack-frontend:v0.43.0@sha256:355ef61f…c676cc`, the digest of the multi-arch index (verified with `docker buildx imagetools inspect`)
- `BuildKitImage = moby/buildkit:v0.34.1@sha256:99368f3e…ec1da5c` (only for the buildx fallback below)

**How it runs: the BuildKit frontend, not the `railpack` CLI.** Railpack documents two ways to build: `railpack build` (the CLI talks to a BuildKit daemon, which the docs start as `docker run --privileged moby/buildkit`) and the BuildKit frontend through `docker build`. The frontend is what Railpack recommends for production and needs no extra daemon:

1. `railpack prepare` plans the build. It runs from the **same frontend image** (so one Railpack version plans and builds) in a throwaway container created with `docker create`. The source goes in and `railpack-plan.json` / `railpack-info.json` come out with `docker cp`, so it works when the agent itself runs in a container (no host bind mounts), and the container has no privileges, no mounts and no docker socket.
2. `docker build --build-arg BUILDKIT_SYNTAX=<frontend image> -f railpack-plan.json -t nc-build/<slug>:<dep8> --label … --iidfile …`. The result is a local image with the usual labels and image ID as digest, so deployment, retention and rollback are unchanged.

**The mergeop caveat (found by testing).** Railpack's build graph uses BuildKit `mergeop`, which the Docker daemon only enables with the **containerd image store**. Docker 29 enables it on fresh installs; upgraded installs and OrbStack keep the classic store and fail with `requested experimental feature mergeop has been disabled`. The agent therefore reads `docker info`:

- containerd image store → plain `docker build` (the daemon's own BuildKit and cache);
- otherwise → a managed `docker buildx` builder `nebulactrl-buildkit` (driver `docker-container`, image pinned above), created on first use, and `docker buildx build --load`.

If the daemon claims the containerd store and still refuses `mergeop`, the agent retries once through the managed builder. The managed builder's container is privileged (that is how the buildx `docker-container` driver works). It only exists on hosts that need it, and the agent already holds the Docker socket (root-equivalent), so it adds no capability a Dockerfile build lacked. A host with neither the containerd store nor the buildx plugin fails Railpack builds with a message that says so; Dockerfile and starter builds are unaffected.

**On a build server** ([ADR 0008](0008-build-servers.md)) the same managed builder is reused, and it is always used: a build there is `docker buildx build --push` into a registry, never `--load`, so the classic-store question above does not arise. The builder is one container **per project**, `nebulactrl-buildkit-<project8>`, each with its own cache, instead of the single `nebulactrl-buildkit` of a target host. The Dockerfile, starter and Railpack paths all go through it, and the planner, variables and secrets behave exactly as described here. Railpack builds one platform per build, so a cross-architecture Railpack build runs every install and build step under emulation.

**Variables and secrets.** Railpack treats every variable as a BuildKit secret. Plain and secret build variables from the variables system are therefore both passed as `--secret id=KEY,env=NCBS_KEY` (values in the docker CLI's process environment, never in argv) plus a `secrets-hash` build arg so layers rebuild when a value changes. For planning, the values reach `railpack prepare` through a mode-0600 shell script copied into the planner container (bare `--env KEY` flags name them), which supports multi-line values and keeps them out of the container's argument vector. `RAILPACK_*` variables (for example `RAILPACK_START_CMD`) are ordinary build variables.

**Build-plan preview.** `POST /services/{id}/build-plan` queues a `build_plan` command; the agent clones the branch head, decides the method and, for Railpack, runs the planner and returns providers, resolved packages, steps and the start command. Nothing is built. The request waits for the agent (409 `server_offline` instead of queueing for an offline server, 504 after 150 s).

**Recorded per deployment.** The agent returns a `BuildInfo` with the deploy result; the control plane stores it in `deployments.build_info` and exposes it as `build` on the deployment.

**Start command.** Railpack images have the entrypoint `["/bin/bash","-c"]` and the start command as `CMD`. A service `startCommand` with several words is joined into one quoted script for such images, so `["node","server.js"]` works as expected.

## Consequences

- A repository without a Dockerfile deploys with no configuration; the plan preview says why Railpack chose what it chose.
- A Railpack build downloads its toolchain (mise) and pulls `ghcr.io/railwayapp/railpack-builder:*` images (hundreds of MB) on the build host on first use; those builder images are tagged by Railpack, not pinned by us. Hosts need outbound access to `ghcr.io`.
- Railpack's output depends on its version; bumping the pin is a deliberate change (see `docs/operations/builds.md`).
- Railpack is 0.x. If it stops being maintained, set affected services to `dockerfile` or a starter and remove three constants and `internal/agent/build/railpack.go`.
- Untrusted repository code still builds on the host Docker daemon; Railpack runs the project's install and build commands (`npm install`, `pip install`, …) just as a Dockerfile would.

## Alternatives considered

- **`railpack build` CLI.** Needs the CLI binary on every host and a privileged BuildKit container always; the frontend path needs neither except in the mergeop fallback.
- **Cloud Native Buildpacks (pack + Paketo).** Compliant with the library rule, but `pack` exposes the Docker socket to the build container for trusted builders; ruled out in favour of Railpack.
- **Nixpacks.** In maintenance mode; never.
