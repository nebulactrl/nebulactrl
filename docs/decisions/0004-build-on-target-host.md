---
title: 0004 Build on the target host
description: "Why images are built where they run by default, without a registry."
section: Internals
order: 4
---

# 0004. Build on the target host, no registry

- Status: accepted. Amended by [0008](0008-build-servers.md): building on the target host and no registry is the **default**, not the only path. Builders beyond the Dockerfile: [0006](0006-railpack-default-builder.md).
- Date: 2026-10-25

## Context

A build needs pinned commits, immutable image digests, a build timeout and cleanup, scoped encrypted registry credentials, and no mandatory proprietary registry. Where to build and how to distribute images were open questions. A service runs on a single explicit host and the platform must also work for homelab hosts that have no registry.

## Decision

**By default, images are built where they run, with no registry.** A server can optionally be a build server, and a registry is then used to ship the image ([ADR 0008](0008-build-servers.md)). Everything below describes the default path.

**Git builds.**
1. Before creating the release, the control plane resolves the branch to a commit SHA with go-git `remote.List` (ls-remote). The release stores `git URL + commit SHA`.
2. The agent clones with go-git into `<state-dir>/builds/<deploymentId>` and checks out that exact SHA. Private repositories use HTTPS BasicAuth with user `x-access-token` and the token from the frame's `secrets`.
3. The agent runs `docker build` through `exec.CommandContext` as an argument vector, never through a shell:
   `docker build --progress=plain -f <dockerfile> -t nc-build/<service-slug>:<deploymentId8> --label nebulactrl.managed=true --label nebulactrl.service=<id> --iidfile <file> <contextDir>`. `DOCKER_HOST` is inherited.
4. Every output line streams as a `log` frame with stream `build`. The timeout is 30 minutes; the build directory is removed afterwards. The scheduled Docker cleanup (`docker builder prune -f --filter until=168h` among its steps) bounds the cache; see `docs/operations/docker-cleanup.md`.
5. `dockerfilePath` and `buildContext` must be relative paths without `..` and are validated both in the control plane and again in the agent.

**Image sources.** The agent pulls with optional decrypted registry auth (from `secrets`, never persisted). The digest is taken from `RepoDigests`.

**Digest.** For an image built **on the target** the **local image ID is the digest**; it is recorded in `releases.source.digest` on success. For pulled images it is `repo@sha256:…`. A rollback of an image release pulls `repo@digest` if the image is missing. An image built on a **build server** is identified by its registry **manifest digest** instead and is deployed as `repo@sha256:…` (recorded as `registryRef` in the release source); the local-image-ID rule applies to target-built images only.

**Retention and rollback.** After a successful deploy the agent keeps the newest `imageRetention` (default 5) `nc-build/<slug>` images and removes older ones. Rolling back to a git release whose image has been pruned returns 409 `image_pruned`; the UI offers "Rebuild commit <sha7>" which creates a normal git deployment for that SHA. Rollback restores application image and configuration only; it does not touch volumes or databases, and the UI says so. With a registry the retention is two-tier: the target still keeps `imageRetention` images locally, and the control plane owns the registry's **live set** (current release, releases in flight, the newest `imageRetention` succeeded releases of each service). A registry release rolls back by pulling `repo@digest` on the target without any build server, and `image_pruned` is then decided from the control plane's registry inventory (built-in registry) or from a failed pull (BYO). Details in [ADR 0008](0008-build-servers.md).

**Labels.** Every managed container carries `nebulactrl.managed=true`, `nebulactrl.service`, `nebulactrl.deployment`, `nebulactrl.release`, `nebulactrl.environment`, which is also what the orphan sweep and image retention match on.

## Consequences

- No registry to run, secure or pay for; works on air-gapped or NAT'd homelab hosts; build secrets stay on the host that will run the code.
- Builds consume CPU, memory and disk on the production host and compete with the running service. Bounds are the 30 minute timeout, one build per service at a time, and cache pruning; there are no per-build resource limits.
- Untrusted repository content is built by the host Docker daemon (root-equivalent). This is documented in the threat model; a Dockerfile can run arbitrary commands at build time.
- A target-built image exists on one host only. Moving a service to another server means building or pulling again there; an image built on a build server and pushed to a registry can be pulled by any server.
- Build output is not reproducible bit-for-bit; the commit SHA plus image ID are what the release records.
- Single-platform builds for another architecture are in scope through build servers with emulation enabled ([ADR 0008](0008-build-servers.md)); multi-arch manifests remain out. On the default path, BuildKit features that need a builder outside the host Docker daemon (multi-platform, remote cache) are still out.

## Alternatives considered

- **Registry**: better for multi-host and build/run separation, but a mandatory registry conflicts with "no proprietary registry" and adds an operational burden. **Adopted as an optional distribution path in [0008](0008-build-servers.md)**, bring-your-own or the built-in registry.
- **Designated builder host**: needs an image transfer path and scheduling. **Adopted as optional in [0008](0008-build-servers.md)** as a build server that pushes to a registry; the `docker save | load` relay was not adopted.
- **Nixpacks/buildpacks**: not part of this decision, which covers Dockerfile builds; other builders are in [0006](0006-railpack-default-builder.md).
- **Building through the Docker API (`ImageBuild`)**: workable, but the CLI gives BuildKit behavior, `--iidfile` and readable plain-progress logs with fewer moving parts.
