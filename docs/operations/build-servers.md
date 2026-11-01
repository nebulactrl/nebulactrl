---
title: Build servers and registries
description: "Build on a dedicated server, push to an OCI registry, deploy by digest: roles, placement, registries, rollback and troubleshooting."
section: Servers
order: 25
---

# Build servers and registries

By default a git service is built on the server that runs it ([builds](builds.md), [ADR 0004](../decisions/0004-build-on-target-host.md)): nothing to set up, no registry. A **build server** is an optional second path: one enrolled server builds the image, **pushes it to an OCI registry**, and the server that runs the service **pulls it by digest**. Use it when builds should not compete with production for CPU and disk, when untrusted Dockerfiles should not run next to production data, or when a release must be deployable on any server without a rebuild. The design and the alternatives are in [ADR 0008](../decisions/0008-build-servers.md).

Nothing changes until something resolves to **Build server** (below). Services that use an image, Compose stacks and databases never build, so they are unaffected.

## How a deployment works

1. The control plane resolves the commit, picks a build server and a registry, and writes the release.
2. The build server clones the pinned commit, builds one image for the **target's architecture** with BuildKit, pushes it as `<registry>/<namespace>/<project>-<service>:r-<release8>`, and returns the manifest digest.
3. The control plane records the image (`registryRef` on the release, a row in the registry's image list) and sends the normal deploy command to the target, which pulls `repo@sha256:…` and runs the usual steps (health check, routing, retire the old container).

The deployment's timeline shows both servers (**Built on X, deployed on Y**) and a **push** step. The build log is the build server's output; the pull and deploy lines come from the target. Build, push and pull all count as the **building** status.

What goes where:

| Data | Build server | Target |
|---|---|---|
| Git token or SSH deploy key, build variables | yes | no (it never clones) |
| Registry **push** credential | yes | no |
| Registry **pull** credential | no | yes |
| Runtime variables and secrets | **never** | yes |

Secrets are attached to the command when it is sent and are never stored in the command row ([threat model](../security/threat-model.md)).

## Server roles

Open the server's page, **Settings → Scheduling → Role**:

| Role | Runs workloads | Builds | Flags |
|---|---|---|---|
| **Deployments** (default) | yes | no | `accept_workloads` on, `build_enabled` off |
| **Builds** | no | yes | `accept_workloads` off, `build_enabled` on |
| **Both** | yes | yes | both on |

`accept_workloads` keeps its meaning (placement of services, drains, templates and the Headscale set-up); build eligibility never reads it. A **Builds** server therefore cannot be chosen for a service, a drain or a template, and a **Both** server builds on the same Docker daemon that runs its workloads.

More settings on a build server:

| Setting | Default | Meaning |
|---|---|---|
| **Max concurrent builds** | 2 | 1 to 16. The cap is per build server; a build over the cap **queues on the control plane** and the deployment stays *preparing* with "Waiting for a build slot on <server> (N builds running)". |
| **Allow emulation** | off | Allow builds for another CPU architecture ([below](#cross-architecture-builds-and-emulation)). |
| **Allow previews** | off | Allow [pull-request previews](previews.md) to build here ([below](#per-project-builders-and-preview-isolation)). |

Changing the role is audited (`server.build_role`). Use a dedicated host for **Builds**: the BuildKit builder is privileged, so a compromised build host exposes the git credentials, build variables and push credentials of every project that builds there. Give it the agent and Docker only, and revoke it like any server if it is suspect ([servers](servers.md#revoke)).

A server needs a current agent to be a build server (the `build.push` capability). An older agent is never chosen; the deployment falls back to the target.

## Where builds run

Placement is resolved **service → project → team**. The first level that is not *inherit* wins; the team default is **Target**.

| Level | Where to set it | Choices |
|---|---|---|
| Team | **Team settings → Build location** (`GET/PUT /team/build-settings`) | Target or Build server, plus the default registry |
| Project | **Project settings → Build location** | Inherit, Target or Build server; an optional pinned build server; an optional registry |
| Service | **Service → Settings → General → Build** | Inherit, Target or Build server; an optional pinned build server; an optional registry |

The pinned server and the registry follow the same order (service, then project). The CLI sets the service override with `--build-location` and `--build-server` on `nebulactrl services create` and `services update`.

The deploy **plan** shows the result before you deploy: the build server, the registry, the platform and every warning (fallback, emulation, a cold cache).

**Choosing the server.** A pin always wins. Otherwise NebulaCtrl hashes the service over the **eligible** servers, so the same service keeps landing on the same build server and its BuildKit cache stays warm. A server is eligible when it is `build_enabled`, online and not revoked, has the `build.push` capability, matches the target's architecture (or may emulate it), is allowed to build previews if this is a preview, and the registry exists and is healthy. A server at its concurrency cap is still eligible: the build queues.

**Reusing a build.** If a live image already exists for the same commit, build configuration, build variables (compared by a keyed hash, never stored), builder version and platform, the build is skipped: the deployment's timeline says "Reusing the image already built for commit <sha7> (no build needed)" and it deploys that digest. This is also why a redeploy that only changes a runtime variable is fast.

**Rebuild commit** (offered when a rollback target is gone) uses the **current** effective placement, not the one the old release was built with.

## Registries

A registry is a team object (**Team settings → Image registries**). A build server needs one. Every team already has the **built-in registry**; you can add your own on top.

### Bring your own registry

Any OCI/Docker v2 registry with **basic authentication**. Fields:

| Field | Meaning |
|---|---|
| **Host** | `ghcr.io`, `registry.example.com:5000`. No scheme. |
| **Namespace** | The path under the host, possibly empty. The repository is `<host>/<namespace>/<project>-<service>`. |
| **Push credential** (required) | Username and password or token that may create repositories and push. Write-only: never returned, sealed with the instance key. |
| **Pull credential** (optional) | A read-only credential for the **target** servers. Without one, the push credential is used for pulls too. Use it. |

**Test registry** (per server, `POST /registries/{id}/check?serverId=`) asks that server to reach the registry and try a push and a pull with the stored credentials, and shows the registry's own error. Run it for the build server **and** for each target before the first deploy. The same check runs automatically at the start of every build, before minutes are spent building.

Recipes (credentials and names are the provider's; check its current console):

**GitHub Container Registry (GHCR)**
- Host `ghcr.io`, namespace your user or organisation **in lowercase**.
- GHCR accepts **classic personal access tokens only**. Push: a token with `write:packages`. Pull: a second token with `read:packages`. The username is your GitHub login.
- A classic token is account-wide, not limited to one repository; create a dedicated bot account for it if that matters. New packages are private by default and linked to the account, not to a repository, so set the package access for the organisation if other members need it.

**Docker Hub**
- Host `docker.io`, namespace your Docker Hub user or organisation.
- Create an **access token** (push: Read & Write; pull: Read-only) and use your Docker Hub username with it.
- NebulaCtrl creates one repository per service (`<project>-<service>`); check your plan's private-repository limit and the pull rate limits.

**GitLab**
- Host `registry.gitlab.com` (or your instance's registry host), namespace `<group>/<project>`: the images live under that GitLab project.
- Push: a **deploy token** with `write_registry` (or a personal or project access token with `write_registry`). Pull: a deploy token with `read_registry`. The username is the deploy token's username.

**Harbor**
- Host your Harbor hostname, namespace the **Harbor project name**. The Harbor project must exist first: Harbor does not create projects on push.
- Create a **robot account** for the project: push and pull for the push credential, pull only for the pull credential. Use the robot's full name (it contains a `$`) as the username.

Other self-run registries (`registry:3`, Gitea, Forgejo, a cloud provider's registry with a static credential) work the same way. A registry on plain HTTP is refused unless it is on a loopback address, because Docker daemons only accept plain HTTP for loopback (the integration test uses one).

**Not supported:** AWS ECR, Google Artifact Registry and Azure ACR hand out tokens that expire within hours; a stored password does not work with them and NebulaCtrl does not refresh tokens ([limits](#limits)).

### The built-in registry

Every team has one from the minute it is created: nothing to set up, no credential to enter, nothing to pay for. It is the team's slice of the **image registry that runs beside the control plane** ([built-in services](platform-services.md): CNCF Distribution `registry:3.1.2`, pinned by digest and run unmodified, registry kind `builtin`). **Team settings → Image registries** lists it first, with the team's size and the last cleanup. It cannot be deleted and its address and credentials cannot be changed, and there is no "Set up for me" for it. Builds use it unless a team, project or service picks another registry.

- **Private, through the agent link.** There is no public hostname, certificate or open port. A server's agent listens on `127.0.0.1:18500`; the build server pushes and the target pulls `127.0.0.1:18500/<team prefix>/<project>-<service>@sha256:…`, and the traffic rides the agent's existing outbound connection to the control plane. Docker accepts a loopback registry without TLS. It works behind NAT, and there is nothing to configure on the build server or the target.
- **A repository prefix per team.** The control plane decides the team from the agent's identity and lets a request touch only that team's prefix; it adds the registry token itself. No server, and no registry password, ever exists on a host. A team cannot see another team's images.
- **Retention is managed** ([below](#rollback-and-retention)): an hourly sweep of unused images and a weekly garbage-collection window. A team may keep about 50 GiB of images; a push over that is refused with `403 denied` ([built-in services](platform-services.md)).
- **Needs an up-to-date agent.** A server whose agent is too old to carry the traffic cannot use it; the build then falls back to the target and the plan says why ([fallback](#fallback-to-the-target)).
- **Back it up?** The images can be rebuilt from their commits, so there is nothing to back up. It lives on the control-plane host with the other instance services, and the instance admin sees its health under Admin → Built-in services.

## Cross-architecture builds and emulation

A deployment has exactly one target server and so one CPU architecture. NebulaCtrl builds **one single-platform image** for that architecture (for example `linux/arm64`). There are no multi-arch manifests.

- A build server of the **same architecture** is always eligible.
- A build server of **another architecture** is eligible only if **Allow emulation** is on for it **and** it reports that platform in its facts (`facts.emulation`, detected from the kernel's `binfmt_misc` QEMU handlers). Otherwise it is skipped, and with no eligible server the deployment [falls back to the target](#fallback-to-the-target).
- Emulation is **much slower** than native builds, especially for compilation. Railpack builds one platform per build and has no cross-compile shortcut, so every install and build step runs emulated.
- To enable it on an amd64 build host: `docker run --privileged --rm tonistiigi/binfmt --install all`, then restart the agent so it reports the platform.
- A faster alternative: build natively on a server of the target's architecture, or cross-compile in your Dockerfile with `FROM --platform=$BUILDPLATFORM …` and `TARGETPLATFORM`/`TARGETARCH`, so the slow part runs natively even on the amd64 builder.
- Placement only considers build servers whose architecture matches the target (or that may emulate it), so an image for the wrong architecture is not built in the first place. A release records the platform it was built for (`platform` in its source); do not move a release's service to a server of the other architecture without rebuilding the commit, or the container fails with `exec format error`.

## Per-project builders and preview isolation

A build server runs **one BuildKit builder per project**, named `nebulactrl-buildkit-<project8>`, each with its own cache. Cache mounts and layer caches are therefore not shared between projects, so one project's build cannot read or poison another's cache. It costs disk: the same base layers are cached once per project. Garbage-collection limits are set on each builder, and an agent-level daily prune stays as a backstop ([Docker cleanup](docker-cleanup.md#build-servers)). Remove a builder and its cache with `docker buildx rm -f nebulactrl-buildkit-<project8>`; the next build recreates it.

A per-project builder isolates **caches**, not the kernel: the builder is privileged and a container escape on the build host reaches every project that builds there. That is inside the [trusted-team boundary](../security/threat-model.md); do not put mutually untrusting teams' projects on one build server.

**Pull-request previews never build on a shared build server.** They build on their target, as without a build server, unless a build server has **Allow previews** on. Fork pull-request code is untrusted ([previews](previews.md#fork-pull-requests)); keep **Allow previews** off on any build server that holds credentials you care about, or dedicate one build server to previews.

## Fallback to the target

A deployment never blocks because there is no build server. When nothing usable exists, the build runs on the **target**, exactly as without this feature, the timeline shows **"No build server available; building on <target> (<reason>)"** (a *placement* event), and the deploy plan carries the same warning. This happens when:

- no server is `build_enabled`, online, and capable (`build.push`);
- the architecture does not match and emulation is off or not reported;
- the build server was **offline when the deployment was dispatched** (nothing had been sent yet);
- no registry is configured, or the registry is not healthy (the built-in registry reports it);
- the deployment is a preview and no build server allows previews.

Fallback is decided **before** the build command is sent. A build server that goes offline **after** it acknowledged the command is a different case: the true outcome is unknown, so the deployment waits ("Waiting for build server <name>", with the time it went offline) and never times out into a terminal state ([servers](servers.md#what-happens-when-a-server-goes-offline)). When it reconnects it replays the result. Cancel the deployment, or deploy again after switching the service to **Target**, if the host is gone.

## Rollback and retention

**A rollback of a registry release needs no build server.** It is an ordinary deploy of `repo@digest` on the **current** target: the build server can be offline, removed or revoked, and the target does not need the image locally. It does need the **registry up** and the digest **still present** in it. The target also keeps its newest `imageRetention` images locally (default 5), so a rollback inside that window works without the registry too.

If the digest is gone, the rollback does not start the new release and the running one keeps serving: for the **built-in** registry NebulaCtrl knows beforehand from its image list and refuses with 409 `image_pruned`; for a **BYO** registry it finds out when the target's pull fails (the deployment fails at the pull step, "Previous release is still serving", with the registry's own message). Either way, **Rebuild commit <sha7>** deploys the commit again.

**What is kept.** For every service the **live set** is the current release, the releases of deployments in flight, and the newest `imageRetention` succeeded releases (the ones **Roll back to this release** can pick). Each image NebulaCtrl pushes is recorded with its own repository and digest in the registry's image list (**Team settings → Image registries → Images**, `GET /registries/{id}/images`), whose state is `live`, `expired` or `deleted`.

- **Built-in registry:** an **hourly sweep** deletes the digests of images already marked `expired` (those outside the live set) through the registry API, one explicit digest at a time; it never touches a `live` image or a digest it did not record, so a push whose result was not yet stored is safe. Blobs nothing references are then reclaimed by `registry garbage-collect` (never with `--delete-untagged`) in a **weekly window** that starts between 02:00 and 05:00 UTC: builds for the registry queue, running pushes get up to 2 hours to finish, the registry is set **read-only** for a few minutes, the collection runs, and writes come back. Pulls work during the window; pushes get **503 with `Retry-After`**. The collection runs on the control-plane host. There is no setting for any of it.
- **BYO registry:** NebulaCtrl **never deletes** anything and does not mark anything expired: its images stay `live` in the list for good, and the registry keeps growing until you set a lifecycle rule.

**What to keep in a BYO registry**

- **Do not enable "delete untagged" rules.** A digest-pinned rollback target can look untagged to a registry that only counts tags.
- Keep **at least `imageRetention` recent tags per repository, plus a margin**: every release has a unique, never-moved tag `r-<release8>`, so rules that keep "the newest N tags" keep the newest releases. Ten is a comfortable number for the default of 5.
- Never delete the digest of the **current** release: a reconcile redeploy, a drain or a move to another server pulls it again.
- Delete by age only if the age exceeds how long you want rollbacks to work.

Lifecycle rules that fit:

| Registry | Rule |
|---|---|
| GitLab | **Cleanup policy**: keep the most recent **10** tags per image name, remove tags older than 90 days, name regex `r-.*`, and leave **untagged** removal off. |
| Harbor | **Tag retention**: retain the most recent **10** pushed artifacts per repository, for the project. Do not add an untagged-artifact purge. |
| GHCR | Keep the newest 10 versions of each package, with a scheduled workflow that deletes older **tagged** versions only (for example the `actions/delete-package-versions` action with `min-versions-to-keep: 10`), and **never** `delete-only-untagged-versions`. |
| Docker Hub | No tag-count rule for tagged images; prune by hand if needed. |

**Local images.** A build server pushes with `--push` and nothing is loaded into its Docker image store, so there is no image keep-list on a build-only server; only BuildKit cache, the pinned BuildKit and Railpack images and clone workdirs remain ([Docker cleanup](docker-cleanup.md#build-servers)). A target keeps the images of registry releases in its keep list ([Docker cleanup](docker-cleanup.md#what-it-never-removes)).

## Troubleshooting

| Situation | What happens | What to do |
|---|---|---|
| Build server **offline at dispatch** | Nothing is sent; the deployment [falls back to the target](#fallback-to-the-target) and says so. If the server drops off right after dispatch and never received the build command, it expires after 15 minutes and the deployment then builds on the target ("Build server <name> did not take the build; building on <target>"). | Bring it back, or set the service to **Target**. |
| Build server **offline after it acknowledged** | The deployment waits; it never times out ("Waiting for build server <name>"). When it reconnects it replays the result (a repeated build is cheap thanks to the cache). | Wait, or cancel and deploy again; to move on, switch to **Target**. |
| **Registry unreachable** from the build server | Fails in the pre-build check, before the build, with `cannot reach <host>: …` and the underlying error. During a push BuildKit retries; the final failure is `failed` at the **push** step. The target is untouched. | Run **Test registry** for that server; check DNS, the firewall, the certificate and the registry's own status. |
| **Push authorization failed** (`401`, `403`, `denied`, `unauthorized`) | The build fails with the registry's own message; no retry. | Replace the push credential; GHCR needs `write:packages`; Harbor needs the project to exist; a deploy token needs `write_registry`. Then **Test registry**. |
| **Pull from the target fails** (auth, unreachable) | The pull happens before the old release is touched: the deployment is `failed` with "Previous release is still serving". | Fix the pull credential or the network, then **Redeploy**: the digest exists, so nothing is rebuilt. |
| **Digest missing** from the registry | Built-in: rollback refused with 409 `image_pruned`. BYO: the pull fails and the running release keeps serving. | **Rebuild commit <sha7>**; review your lifecycle rule. |
| **Wrong architecture** | Placement skips build servers of another architecture unless emulation is on and reported; with none left the build [falls back to the target](#fallback-to-the-target). | Use a build server of the target's architecture, or enable emulation, or build on the target. |
| **Built-in registry read-only** (garbage-collection window) | Builds for the registry queue until the window ends; pushes get `503` with `Retry-After` meanwhile. | Retry in a few minutes. |
| **Build server disk full** | A preflight refuses to start with too little free space, runs an emergency cache prune and retries once; otherwise `failed` with BuildKit's error. | Free space or raise the build server's disk; see [Docker cleanup](docker-cleanup.md). |
| **Build timeout** | Fails after 30 minutes, as for target builds. | Fix the slow step; for emulated builds consider a native builder. |
| **Too many builds** | Each build server runs at most **Max concurrent builds**; the rest queue, per server, not globally ("Waiting for a build slot on <server> (N builds running)"). | Raise the cap if the host can take it, or add a build server. |
| **Control plane restarted mid-build** | Commands are durable. The build continues on the agent; its result is delivered when the agent reconnects. | Nothing. |
| Same tag pushed again, target runs old code | Cannot happen: targets deploy the **digest**, never the tag. | |
| `blob unknown to registry` / `denied` on push to a third-party registry | The registry's own message is shown verbatim in the build log with the server's name. | Run **Test registry**; check quotas and the repository's visibility settings with the provider. |

The build log of a registry deployment lists both servers' steps. Use **Deployments → the deployment → Logs** and read the `build` and `push` lines for the build server and the `pull` and `deploy` lines for the target.

## Limits

- **One platform per build, no multi-arch manifests.** A release runs on servers of one architecture; moving it across architectures means a rebuild. The extension path is native builds on two build servers joined with `docker buildx imagetools create`.
- **No ECR, Google Artifact Registry or Azure ACR token refresh.** Only registries with a static basic credential work. A long-lived access key through a registry that supports static credentials works; tokens that expire in hours do not.
- **No registry-free transport.** There is no `docker save | load` relay through the control plane; a build server needs a registry that both it and the targets can reach. Without a registry, build on the target.
- **BYO registries are never cleaned up** by NebulaCtrl, and a lifecycle rule can delete a rollback target.
- **GHCR credentials are account-wide** (classic tokens only); other registries scope as finely as their credentials allow.
- **The build server is a shared privileged host** for every project of the team; per-project builders isolate caches only ([threat model](../security/threat-model.md)).
- **The built-in registry is one registry on the control-plane host**, shared by every team's prefix: a size cap of about 50 GiB per team plus retention, and its loss loses the images (they can be rebuilt from their commits). It is not reachable from outside the host, so it cannot serve pulls to machines that are not NebulaCtrl servers ([built-in services](platform-services.md#limits)).
- Provenance and SBOM attestations are off, so the registry holds a single plain manifest per release.
- Stacks, image services, databases and platform services do not use build servers.
- Plain-HTTP registries are accepted on loopback addresses only.
- Registry cache export (`--cache-to type=registry`) is not offered; the persistent per-project builder is what keeps builds fast.

## API and audit

| | |
|---|---|
| `GET/PUT /team/build-settings` | team default location and registry |
| `GET /registries`, `GET /registries/{id}`, `GET /registries/{id}/images` | registries and their pushed images (`projects:read`); credentials are never returned |
| `POST /registries`, `PATCH /registries/{id}`, `DELETE /registries/{id}` | external registries (`admin`): `name`, `host`, `namespace`, `pushUsername`, `pushPassword`, optional `pullUsername`, `pullPassword`. Deleting is refused with `409 registry_in_use` while the team's build settings, a project or a service references it. The built-in registry (kind `builtin`) is read-only: `PATCH` answers `409 builtin_registry_readonly` and `DELETE` `409 builtin_registry`. |
| `POST /registries/{id}/check?serverId=` | test a registry from a server (`admin`); waits up to 25 s for the agent and answers `{ok, push, pull, error}`; `409 server_offline` if the agent is not connected |
| `PATCH /servers/{id}` | `build_enabled`, `build_max_concurrent`, `build_allow_emulation`, `build_allow_previews`, `accept_workloads` |

Audit actions: `registry.create`, `registry.update`, `registry.delete`, `registry.check`, `server.build_role`, and `deployment.build_server` (which build server built a release). The deployment resource carries the build server's id next to the target's.
