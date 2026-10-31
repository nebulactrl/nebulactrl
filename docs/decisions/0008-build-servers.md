---
title: 0008 Build servers and registries
description: "Why builds can optionally run on a dedicated build server and ship through an OCI registry."
section: Internals
order: 8
---

# 0008. Optional build servers and OCI registry distribution

- Status: accepted
- Date: 2026-10-27
- Amends: [0004](0004-build-on-target-host.md) (building on the target stays the default; this adds an optional second path), [0006](0006-railpack-default-builder.md) (the managed builder is reused on build servers)

## Context

[ADR 0004](0004-build-on-target-host.md) builds every git service on the server that runs it and has no registry. That is the right zero-configuration path, and it stays. It has three costs for larger fleets:

- a build competes for CPU, memory and disk with the running service on the production host;
- untrusted Dockerfiles (and fork pull-request previews) execute next to production data and runtime secrets;
- a build exists on one host only, so a rollback outside the host's `imageRetention` window, a move to another server and a reconcile redeploy all need a rebuild.

A common design is a build server that pushes to a registry and deployment servers that pull; it works by SSHing into every host, so "build here, run there" needs only credentials on both ends. NebulaCtrl agents only **dial out** ([ADR 0002](0002-agent-transport.md)): there is no agent-to-agent path, and the control plane is the only rendezvous. Registry handling is also the usual bug source of that design: an ambient `~/.docker/config.json` that differs between users, credentials that "log in" but cannot push, a mutable tag that the target does not re-pull.

The decisions:

- support **both** a bring-your-own (BYO) registry and a NebulaCtrl-run registry (the built-in registry, one registry on the control-plane host with a repository prefix per team: private, reached through the agent link and managed by the instance admin, see [built-in services](../operations/platform-services.md));
- the built-in registry is CNCF Distribution `registry:3.1.2`, pinned by index digest, as an explicit exception to the "stable for two years" library rule (the project is 12 years old, the v3 line was 19 months old when it was pinned);
- **always fall back to the target** when no build server or no usable registry exists, with a visible event, never a blocked deploy;
- **one BuildKit builder per project** on a build server, and previews never build on a shared build server unless its operator opts in.

## Decision

**A build server builds and pushes to an OCI registry; the target pulls by digest.**

1. **Where a build runs is a placement decision**, resolved **service → project → team**, with `target` as the default at every level. A registry is needed only when something resolves to `build_server`. Compose stacks and image services never build, so they are unaffected.
2. **Server roles are two orthogonal flags.** `accept_workloads` decides placement of workloads; `build_enabled` decides build eligibility. *Deployments* (the default) is `accept_workloads` on and `build_enabled` off, *Builds* is the reverse, *Both* sets both. Build eligibility never reads `accept_workloads`. Each build server has `build_max_concurrent` (default 2), `build_allow_emulation` and `build_allow_previews` (both off).
3. **Two durable commands per deployment.** The control plane writes the release first, sends a `build_image` command to the build server (clone the pinned commit, `docker buildx build --push`, return the manifest digest), records the pushed image, and then sends the ordinary `deploy` command to the target with `Image{Ref: repo@sha256:…}`. The target engine already pulls images by digest with send-time registry credentials, so everything after the pull (architecture guard, create, health, route, verify, retire) is the existing engine. The step `push` maps to the `building` status; there is no separate deployment status for it.
4. **Registry.** `registries` are team objects of two kinds: `external` (any OCI registry with a basic-auth push credential and an optional read-only pull credential: GHCR, Docker Hub, GitLab, Harbor, self-run) and `builtin` (the instance's image registry, a [built-in service](../operations/platform-services.md); every team has its slice). ECR, Google Artifact Registry and Azure ACR need token refresh and are not supported.
5. **Image identity.** The repository is `<host>/<namespace>/<project-slug>-<service-slug>` (an empty namespace is allowed), the tag is `r-<release8>`, and the release records the registry **manifest digest** and `registryRef = repo@sha256:…`. Targets deploy the digest, never the tag. Each pushed image is recorded in `registry_images` with its own repository, so renaming a service never breaks a rollback. Provenance and SBOM attestations are off (`--provenance=false --sbom=false`), so the pushed result is a single manifest rather than an index.
6. **One platform per build.** A build produces one single-platform image for the **target's** architecture. A build server of another architecture is eligible only with `build_allow_emulation` on and an agent that reports the platform in `facts.emulation`. Multi-arch manifests are not built.
7. **Sticky selection with a concurrency cap.** An explicit pin wins; otherwise rendezvous hashing over eligible servers with the service id as the key keeps the BuildKit cache warm. Each build server has its own cap; further builds queue on the control plane and the deployment stays `preparing` with "Waiting for a build slot on <server>".
8. **Isolation.** The agent creates one BuildKit builder per project, `nebulactrl-buildkit-<project8>`, with its own cache and garbage-collection limits. Preview builds go to the target unless a build server sets `build_allow_previews`. Runtime environment variables **never reach a build server**; only the git credential, build variables and the registry push credential do.
9. **Credentials are send-time only and never ambient.** The push credential of an external registry reaches the build agent in the command frame's `secrets` and is written to a mode-0700 temporary `DOCKER_CONFIG` that exists for one build and is always removed; pulls pass the credential per request. `agent_commands.payload` carries only booleans. For the built-in registry the control plane is the token server (Docker token authentication): requests ride the agent's connection to the control plane, the control plane decides the team from the agent's identity, restricts the request to that team's repository prefix and adds a short-lived registry token itself. No registry credential rests on any agent host.
10. **Rollback needs no build server.** Rolling back a registry release is an ordinary deploy of `repo@digest` on the **current** target. `image_pruned` is decided from `registry_images.state` before dispatch; for a BYO registry, where NebulaCtrl deletes nothing, an image removed out of band shows up as a failed pull. "Rebuild commit" uses the current effective build location.
11. **Retention.** Targets keep `imageRetention` images locally (fast rollback, works while the registry is down). The control plane owns the **live set** (current release, releases of deployments in flight, the newest `imageRetention` succeeded releases of each service). For a registry with **managed retention** (the built-in registry) it marks other `registry_images` rows `expired`, deletes those explicit digests through the registry API and runs `registry garbage-collect` (without `--delete-untagged`) inside a read-only window. For BYO registries NebulaCtrl records images but never marks them expired and never deletes.
12. **Failure handling** is by situation, in [build servers: troubleshooting](../operations/build-servers.md#troubleshooting): a build server offline at dispatch falls back to the target; one that goes offline after acknowledging the command never times out into a terminal state (the rule for every acknowledged command, [servers](../operations/servers.md#what-happens-when-a-server-goes-offline)); a registry that cannot be reached fails fast in a pre-build check; a pull failure on the target leaves the old release serving.

Every frame that belongs to a deployment (events, results, logs, source scans, cancel) is accepted from **any server that owns a command of that deployment**, looked up through `agent_commands`, not only from `deployments.server_id`. Cancel targets whichever command is active.

## Consequences

- A build server never runs production workloads (when set to *Builds*), so a build cannot starve the service it is building, and an untrusted Dockerfile does not execute on the host that holds runtime secrets. The cost is a second privileged host (the builder is privileged BuildKit) that holds the git credential, build variables and a push credential for **all projects** of the team; it is inside the documented trusted-team boundary ([threat model](../security/threat-model.md)). Per-project builders isolate caches, not the kernel.
- A rollback of a registry release works with the build server offline or deleted and does not depend on the target's local image cache. It depends on the registry being up and on the digest still existing there.
- The registry is an availability dependency for build-server deployments only: a registry outage blocks the push and the pull, but a fallback keeps deploys working when none is configured.
- Two WAN hops per deploy replace zero (build server → registry, registry → target), mitigated by layer deduplication in the OCI protocol.
- The built-in registry holds every team's images on the control-plane host, so that host is a high-value target and its loss loses the images (they can be rebuilt from their commits). The registry has no public hostname, certificate or open port: Docker daemons accept a loopback registry without TLS, and each agent listens on `127.0.0.1` and carries the traffic over its link to the control plane.
- BYO registries cannot be garbage-collected by NebulaCtrl. A lifecycle rule that deletes untagged images, or only keeps a few tags, can destroy rollback targets; the docs say what to keep.
- Releases built on the target carry a local `imageId` and keep the target-built semantics; a release with a `registryRef` takes precedence. Both representations coexist.
- Agents that do not report the `build.push` capability are never chosen as build servers; the deployment falls back to the target.
- Multi-arch manifests, ECR/GAR/ACR token refresh, registry cache export and a registry-free transport are explicitly not included.

## Alternatives considered

- **Relay transport: `docker save | docker load` through the control-plane WebSocket.** The only registry-free option that works with outbound-only agents, and its one real merit. Rejected: the control plane sits in the image data path (it would see every layer in clear); there is **no layer deduplication** (`docker save` ships every parent layer, so a 300 MB image with 30 MB of changed layers moves ~600 MB through the control plane instead of ~60 MB with a registry); both agents must be online at once and a drop restarts the whole transfer; the bulk bytes would share the one socket that carries heartbeats, logs and commands; and rollback would still need each target's local image. It stays a possible later fallback for installs that cannot run a registry host.
- **Remote BuildKit driven by the target (`docker buildx create --driver remote tcp://…`).** Needs an inbound listener on the build server; agents never accept inbound connections. It still would need the source to flow to the builder, so it has no advantage over the builder the build agent drives locally (which is what this ADR does).
- **zot instead of `registry:3`.** Inline garbage collection and a retention policy remove the read-only window, and it has dedupe and scrub. Its default garbage collection deletes **untagged manifests** after a delay, which is a footgun for digest-pinned rollback targets unless every retained release keeps a tag, and it is a larger surface. `registry:3` is the reference implementation, smaller, and its token authentication fits a control plane that every agent can already reach. Both speak the OCI API, so swapping is a catalog change; the unique `r-<release8>` tag already makes zot safe.
- **Native multi-arch.** One buildx invocation cannot span two hosts without an inbound path, so native multi-arch would be two builds on two build servers plus `docker buildx imagetools create`. Valuable only for fleets that move one release between architectures; recorded as the extension path.
- **A registry credential stored in `~/.docker/config.json` on each server.** Rejected: a global `docker login` leaves long-lived credentials on a shared builder and fails when the build and the pull run under different Docker configs.
- **Always random build-server choice.** Fragments the cache (1/N hit chance); replaced by sticky selection.
- **Hosted builders (Docker Build Cloud and similar).** Contradicts the self-hosted, no-SaaS goal.
