---
title: Builds
description: "Dockerfile, Railpack and starter Dockerfiles: how a repository becomes an image."
section: Applications
order: 20
---

# Builds: Dockerfile, Railpack and starter Dockerfiles

By default, git services are built on the server that runs them ([ADR 0004](../decisions/0004-build-on-target-host.md), [ADR 0006](../decisions/0006-railpack-default-builder.md)). A team, project or service can instead build on a dedicated **build server** that pushes to a registry, and the server that runs the service pulls the image by digest: see [Build servers and registries](build-servers.md). The build methods below are the same either way. **Settings → Source → Build method** chooses how.

| Build method | What happens |
|---|---|
| **Automatic** (default) | The repository's `Dockerfile` if there is one, otherwise Railpack. |
| **Dockerfile** | Always the Dockerfile (path configurable); the deployment fails if it is missing. |
| **Railpack** | Railpack, even if the repository has a Dockerfile (which is then ignored). |
| **Starter: Node.js, Python, Go, Ruby, PHP, static site** | A Dockerfile generated from the files in the build context. It is printed at the top of the build log so you can copy it into your repository. |

A Dockerfile always wins in Automatic mode. If you changed *Dockerfile path* away from `Dockerfile` and that file does not exist, the build fails with a clear message instead of falling back to Railpack.

## Preview the build plan

**Settings → Source → Preview build plan** (API: `POST /api/v1/services/{id}/build-plan`, needs `deployments:write`) asks the server that **will build** the service (its target, or the build server it resolves to) to clone the branch head and report:

- the method that applies and why ("No Dockerfile in the repository, so Railpack detects how to build it"),
- for Railpack: the detected providers (`node`, `python`, `staticfile`, …), the toolchain versions it would install, the build steps and the **start command**,
- Railpack's warnings and suggestions (for example "Add a `package-lock.json` for more deterministic installs"),
- whether a Dockerfile was found and, if not, which starter matches the project.

Nothing is built or deployed. That server must be online. The first plan on a host takes longer while Railpack's tooling downloads. The preview uses the **saved** configuration and the current build variables.

## Railpack

Pinned version: **Railpack 0.43.0**, frontend image `ghcr.io/railwayapp/railpack-frontend:v0.43.0@sha256:355ef61f…` (full digest in `pkg/agentprotocol/build.go`).

What Railpack detects: Node (npm, yarn, pnpm, bun), Python, Go, PHP, Ruby, Java, .NET, Deno, Rust, Elixir, static sites (a `Staticfile` or an `index.html`) and more; see the [Railpack docs](https://railpack.com/getting-started). Configure it with a `railpack.json` in the repository or with `RAILPACK_*` build variables, for example `RAILPACK_START_CMD`, `RAILPACK_NODE_VERSION`, `RAILPACK_PACKAGES`. Set build variables under **Variables** with scope *Build*; plain and secret values are both handed to Railpack as BuildKit secrets.

**Node and Bun versions.** For a Node project (a `package.json` in the build context) NebulaCtrl defaults `RAILPACK_NODE_VERSION` to `26.13.0` (Node 26 LTS), and `RAILPACK_BUN_VERSION` to `1.4.4` for a Bun project, instead of Railpack's own default. Your own pin always wins: set the build variable, `engines` in `package.json`, `packageManager` (Bun), `.nvmrc`, `.node-version`, `.bun-version`, a `mise.toml` / `.tool-versions` entry, or `packages` in `railpack.json`. The build log lists a default when one is applied.

**The port.** Railpack does not know your port. Set the service's *Port*; the platform passes it as `PORT`, which Railpack's Node/Python/static start commands honour (the static site server listens on `$PORT`).

**Start command.** The default comes from Railpack (shown in the preview). To change it, prefer `RAILPACK_START_CMD`. A service-level *Start command* also works: Railpack images start with `bash -c`, so a multi-word command is passed as one quoted script.

**Requirements on the server.** Outbound access to `ghcr.io` (the frontend and Railpack's builder images), `registry-1.docker.io` and the usual package registries. Railpack's build graph needs BuildKit `mergeop`:

- Docker with the **containerd image store** (the default on fresh Docker 29 installs): builds run in the daemon.
- Otherwise the agent creates a `docker buildx` builder named `nebulactrl-buildkit` (a privileged BuildKit container, image pinned by digest) the first time a Railpack build needs it. This needs the `docker-buildx-plugin` package (included with Docker's own packages; Debian's `docker.io` lacks it). Without it Railpack builds fail with that explanation; Dockerfile and starter builds still work.

Check which applies: `docker info --format '{{json .DriverStatus}}'` mentions `io.containerd.snapshotter.v1` with the containerd store.

**Disk.** A first Railpack build pulls ~100–500 MB of builder images and leaves BuildKit cache. The server's [Docker cleanup](docker-cleanup.md) prunes build cache older than 7 days daily (`docker builder prune --filter until=168h`, plus `docker buildx prune --builder nebulactrl-buildkit` when that builder exists) and keeps the pinned BuildKit and Railpack images. To reclaim everything Railpack brought: `docker buildx rm -f nebulactrl-buildkit` (removes the builder container and its cache), `docker image rm moby/buildkit:v0.34.1 ghcr.io/railwayapp/railpack-frontend:v0.43.0`. On a [build server](build-servers.md) every project has its own builder, `nebulactrl-buildkit-<project8>`, and builds always go through it (`buildx build --push`), whatever the daemon's image store.

### Vite, Vite+ and Void apps

Checked against the pinned planner (Railpack 0.43.0), plan only:

| Project | What happens |
|---|---|
| Vite SPA (`vite` in `devDependencies`, `build: "vite build"`) | Detected as a static site, output `dist`, served by Caddy. No settings needed. |
| Vite+ project after `vp migrate` (adds the `vite` alias) | Same as a Vite SPA. |
| Vite+ project with only `vite-plus` (as `vp create` can produce) | **Not detected**: the plan has no start command and the container would not start. Add the build variable `RAILPACK_SPA_OUTPUT_DIR=dist`, or run `vp migrate`. |
| Void app | Only its `target: "node"` mode runs outside Cloudflare. Add a `start` script (`node dist/ssr/index.js`) or set `RAILPACK_START_CMD`; the app listens on `PORT`. Void's `db`, `kv`, `storage`, `auth`, `ai`, sandbox, WebSocket and email modules need Cloudflare and are unavailable; use a [managed database](databases.md) instead. Not verified with a real build. |

## Starter Dockerfiles

Generated from the build context (the *Build context* path):

| Starter | Base image | Install | Start |
|---|---|---|---|
| `node` | `node:26.13.0-slim` (pinned by digest) | `npm ci` / `pnpm` / `yarn` by lockfile (else `npm install`), `build` script if present | `npm start` (or `main`, `server.js`, `index.js`) |
| `python` | `python:3.13-slim` | `requirements.txt`, `Pipfile` or `pyproject.toml` | `python main.py` / `app.py` / `server.py` |
| `go` | `golang:<go.mod version>-alpine` → `alpine:3.22` | `go build` of the root package or the single `cmd/<name>` | the binary |
| `ruby` | `ruby:3.4-slim` | `bundle install` | `bin/rails server` or `rackup` |
| `php` | `php:8.4-apache` (+ Composer) | `composer install --no-dev` if `composer.json` | Apache on `$PORT` (document root `public/` when it has an `index.php`) |
| `static` | `nginx:1.29-alpine` | none: serves prebuilt files from `index.html`'s directory (root, `public/`, `dist/` or `build/`) | nginx on `$PORT` |

`.git` is never copied into a starter image; your `.dockerignore` is honoured too. Set the *Start command* when the heuristics do not find one. Starters run no build for `static`: run your site generator in CI and commit the output, or use Railpack.

## Change a service's source

**General → Source → Change source…** switches a service between a Docker image and a Git repository. It works for image and Git services only, not for Compose stacks, databases or routes (`422 not_applicable`).

It is a **staged change**, like any other edit: nothing runs until the next deploy. **Review changes** lists it as one line, `Source: image → Git (acme/shop, main, Railpack)` (the other way, `Source: Git → image (nginx:1.27)`), and the next deploy applies it. A Git source builds first, so that deploy starts with a build.

| | |
|---|---|
| **Kept** | name, domains, variables, volumes, replicas, health check and the deployment history |
| **Cleared** | the old source's own settings: image credentials when moving to Git; the repository link, the access token and the build settings when moving to an image |

- **History.** Older releases keep the source they were deployed with. Rolling back to one runs the image it ran.
- **Build-scoped variables** apply to a Git source only. Moving a service to an image with build-scoped variables blocks the deploy plan until their scope is *Runtime*.
- **Discarding** the staged change restores the previous type and its settings. It does not bring back what was cleared: enter the image credentials, the access token or the repository link again.
- **Production.** Deploying the change needs the usual approval; an MCP agent needs a person to approve it ([production approval](../reference/mcp.md#production-approval)).
- **Audit.** The change is recorded as `service.source_changed`.

**API:** `PUT /api/v1/services/{serviceId}/source` with an `If-Match` header (a stale one is `412 generation_conflict`) and the target in `sourceType`: `image` with `image` (and optional `registryAuth`), or `git` with `gitUrl`, `gitBranch` and optionally `gitToken`, `buildType`, `dockerfilePath`, `buildContext` and `submodules`. A missing or invalid field is `422 invalid_config`; a service that already has that source type is `422 source_unchanged`. The repository **connection** is not part of the call: link a connected account afterwards with the git link endpoint ([Git providers](git-providers.md)). `DELETE /api/v1/services/{serviceId}/pending-changes/source` discards the change. With a `nebula.toml` file the same switch is a [staged `source` change](project-config.md#switching-between-an-image-and-a-git-repository).

## Deploying without cache

Stale layers are the usual cause of "works on my machine, fails here". **Redeploy without cache** (inspector ⋯ menu, the Deployments tab and the canvas menu of a git service; API `POST /api/v1/services/{id}/deployments` with `{"noCache": true}`; `nebulactrl services deploy <service> --no-cache`) builds **that one deployment** from scratch:

- Dockerfile, starter and Railpack builds run with `--no-cache --pull`: every layer is rebuilt and the base images are pulled again. This is the same on the target and on a [build server](build-servers.md), where the build also ignores the registry cache (`--cache-from`).
- An image a build server already pushed for the same commit and settings is **not reused** (a normal deploy of an unchanged commit would reuse it). The fresh image becomes the reusable one for later deployments.
- The deployment records it: the timeline shows "Deploying without build cache", the deployment has `noCache: true` and the audit event `deployment.create` carries `noCache`. The next deployment builds normally; there is no setting that makes it permanent.
- `POST /environments/{id}/deploy-staged` and MCP `apply_changes` accept `noCache` too and apply it to the git services of that batch. Both only deploy services with staged changes, and the raw deploy operation is not exposed over MCP, so an AI client cannot rebuild an unchanged service without cache; use the web app, the CLI or REST for that.
- Image services have nothing to build: `noCache` on one is `422 validation_failed`. Rollbacks and reconcile redeploys never build. A server whose agent predates this option answers `422 agent_too_old` instead of quietly building with the cache; update the agent.

Limits: `--no-cache` ignores cached *layers*; BuildKit **cache mounts** (the package-manager caches Railpack mounts, such as the npm or pip download cache) are kept, because BuildKit does not treat them as layers. They speed up downloads but cannot make a build wrong. To drop them as well, run `docker builder prune -af` on the server (the agent also prunes build cache older than a week daily). `--pull` needs the registries of your base images to be reachable, and a `FROM` that names a local-only image fails with it.

## Git submodules

**Settings → Source → Git submodules** (config `submodules`: `auto`, `on` or `off`; `nebulactrl services update <service> --submodules on`):

| Mode | Behaviour |
|---|---|
| **Automatic** (default) | Submodules are checked out recursively when the commit has a `.gitmodules` file. A submodule that cannot be fetched is a **warning** in the build log and the build continues without it. |
| **Always** | Same checkout; a submodule that cannot be fetched **fails the build**. Use it once you rely on the submodule. |
| **Never** | The submodule folders stay empty. |

Automatic is the default because a repository that has submodules does not build without them; making it opt-in would keep that failure for everyone who does not find the setting. Because a warning never fails a build, a repository whose submodules cannot be fetched keeps building. The checkout follows the commit's recorded submodule commits, nests up to five levels deep, and is part of the build-reuse key on build servers (changing the mode rebuilds).

**Credentials.** A `.gitmodules` file is untrusted input, so the repository's credentials only go to the repository's own host:

- An HTTPS repository with an access token: the token is used for submodules on the **same host** (and port) over https, and for relative URLs such as `../lib.git`. A submodule written as `git@github.com:org/lib.git` or `ssh://git@github.com/org/lib.git` on that same host is fetched over HTTPS with the token instead, so one token covers a repository and its private submodules on the same provider as long as the token can read them (a GitHub fine-grained token must list the submodule repositories).
- An SSH repository with a [deploy key](ssh-deploy-keys.md): the key and its pinned host key are used for ssh submodules on the **same host and port**. Providers such as GitHub accept a deploy key for one repository only, so a private submodule needs the key added to that repository too, or a user/machine key that can read both.
- Submodules on **another host** are fetched anonymously over `https://`, `http://` or `git://`; a private one fails (a warning in Automatic, a failed build in Always). An `ssh://` submodule on another host is refused, since there is no key or pinned host key for it; change its URL to https on the parent's host or mirror it.
- Passwords in a submodule URL, `file://`, local paths and other schemes are refused.

Not supported: Git LFS (pointer files are checked out as text), shallow clones and per-submodule credentials. A server whose agent predates submodule support ignores the setting in Automatic, and a service set to *Always* is blocked with `agent_too_old` until the agent is updated.

## Upgrading the pinned Railpack

1. Check the release notes of the new version for breaking changes (provider behaviour changes are the usual ones).
2. Resolve the frontend digest without pulling: `docker buildx imagetools inspect ghcr.io/railwayapp/railpack-frontend:vX.Y.Z` and take the top-level `Digest`.
3. Update `RailpackVersion` and `RailpackFrontendImage` together in `pkg/agentprotocol/build.go` (and `BuildKitImage` if you also bump BuildKit; get its digest the same way).
4. Refresh `internal/agent/build/testdata/*-plan.json` / `*-info.json` by running `railpack prepare` from the new frontend image against a Node and a static example, run `go test ./internal/agent/build/`, then `./tests/run.sh -run TestRailpackBuilds -v` (it cleans up the images and cache it created).
5. Ship the new agent. Hosts pick up the new frontend image on their next Railpack build; images already built keep running.

## Troubleshooting

| Message | Cause and fix |
|---|---|
| `railpack could not plan this source` | Railpack recognised no language. Add a Dockerfile, choose a starter, or add `railpack.json`. The preview shows the reason. |
| `requested experimental feature mergeop has been disabled` | Should not surface: the agent retries through the managed builder. If it does, the buildx plugin is missing (`docker buildx version`). |
| `create the BuildKit builder (is the docker buildx plugin installed?)` | Install `docker-buildx-plugin`, or enable the containerd image store, or use a Dockerfile. |
| `dockerfilePath: "…" not found in repository` | Build method Dockerfile (or Automatic with a custom path) and the file is not in the repository at that commit. |
| `starter:static needs an index.html` | Nothing to serve at the root, `public/`, `dist/` or `build/`. |
| Preview: `409 server_offline` | The plan is made on the server that will build; bring it online (or switch the service to build on its target). |
| Preview: `504 build_plan_timeout` | The first plan on a host downloads tooling; try again. |
