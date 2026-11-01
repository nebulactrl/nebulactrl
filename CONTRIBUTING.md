# Contributing to NebulaCtrl

Read [`README.md`](README.md) first, then the ADRs in [`docs/decisions/`](docs/decisions/). NebulaCtrl is free, self-hosted and open source; it is licensed under **AGPL-3.0-only** ([`LICENSE`](LICENSE), [ADR 0005](docs/decisions/0005-license.md)). Contributions are accepted under the same licence (inbound = outbound), and there is no CLA. New dependencies must have an AGPL-compatible licence (see [Third-party licences](docs/security/third-party-licences.md)).

## Toolchain

| Tool | Version | Used for |
|---|---|---|
| Go | 1.27 (CI and dev use 1.27.3) | control plane, agent, CLI |
| bun | 1.4 | web package manager, dev server, unit tests |
| TypeScript | 7.0.2 (native `tsc`; pinned exactly in all four packages) | type checking |
| sqlc | 1.30 | typed Go from `db/queries` |
| Docker | 29 (Engine + Compose v2) | dev Postgres, dev agents, integration tests |
| openssl | any | `make setup` key generation |

`make setup` checks all of these, creates `.env` from `.env.example` with a fresh `NEBULACTRL_SECRET_KEY`, and runs `bun install`.

TypeScript 7 ships no JavaScript compiler API, only the native `tsc`. `@hey-api/openapi-ts` builds its output with that API (`ts.factory`, `ts.createPrinter`) and has no stable release without it (only a `0.0.0-next` snapshot), so `apps/web` aliases `typescript` to `@typescript/typescript6` (TS 6.0, the compatibility package Microsoft publishes for this) and gets `tsc` from the `@typescript/native` alias of `typescript@7.0.2`. `tsc6` is the TS 6 binary. Nothing else imports `typescript`. Drop the alias once `@hey-api/openapi-ts` stops importing it, then pin `typescript` to 7.x here as in the other packages.

## Repository layout

```text
apps/web/            React + Vite web app (React Flow canvas, TanStack Router/Query)
apps/site/           Public marketing site, docs and installer (static, prerendered; see its README)
packages/docs/       Docs pipeline shared by apps/web and apps/site (bun workspace: `bun install` at the root)
cmd/server/          control plane  -> bin/nebulactrl-server (also: keygen, openapi)
cmd/agent/           host agent     -> bin/nebulactrl-agent
cmd/cli/             CLI            -> bin/nebulactrl
internal/            control-plane domains: api, auth, policy, audit, events, secretbox,
                     projects, environments, services, variables, domains, deployments,
                     servers, agents, reconciler, jobs, mcp, webui, config, db
internal/agent/      agent internals: conn, store, docker, build, health, proxy, engine, logs, stats
pkg/agentprotocol/   wire frames shared by server and agent
db/migrations/       goose SQL migrations (embedded)
db/queries/          sqlc query files, one per area
openapi/             generated openapi.json (committed)
deploy/              dev compose, agent systemd/installer, production compose
docs/                architecture, ADRs, install/operate/security guides
tests/               integration tests and fixtures
```

## Workflow

1. `make dev-deps` then `make dev` for the control plane and web app. `make dev-agent TOKEN=…` for a local agent.
2. Domain rules live in `internal/<domain>` service methods. Each method calls `policy.Require` and writes `audit.Record` in the same transaction as the mutation. HTTP handlers and MCP tools only adapt; do not put authorization or business rules in them.
3. Secrets: variable values, git tokens and registry credentials are never put in `agent_commands.payload`, logs or API responses. They travel as `secrets` on the outgoing frame only.
4. Schema changes: add a new goose migration in `db/migrations`, add or edit queries in `db/queries/<area>.sql`, run `make generate`. Do not edit an applied migration.
5. Run `make lint test` before pushing (tier 1: unit and database tests, about a minute). Run `make test-smoke` (tier 2: the core Docker scenarios, a few minutes) when you touch the agent, deploy engine, reconciler or proxy code. `make test-full` (tier 3, everything) is the thorough run; run it yourself for larger changes to those areas or to templates, backups or builds. CI runs the tier 1 and 2 checks on every pull request (see [CI](#ci)), so these local runs catch problems before the push. See [`tests/README.md`](tests/README.md).
   `make test-full` is heavy: run it at low priority (`nice -n 19 make test-full`) so the machine stays usable.
6. Keep deployment claims honest: do not describe behavior as zero-downtime, rolled back or HA unless a test proves it.

## Generated files

Commit generated output; never edit it by hand. `make generate` rebuilds all of it.

| Generated | Source | Tool |
|---|---|---|
| `internal/db/*.go` | `db/migrations`, `db/queries` | `sqlc generate` |
| `openapi/openapi.json` | huma handlers and types | `go run ./cmd/server openapi` |
| `apps/web/src/api/gen/**` | `openapi/openapi.json` | `@hey-api/openapi-ts` (`bun run gen:api`) |
| `internal/apiclient/**` | `openapi/openapi.json` | `oapi-codegen` (config `internal/apiclient/oapi-codegen.yaml`) |
| `apps/web/src/routeTree.gen.ts` | `apps/web/src/routes` | TanStack Router plugin (on `vite`/`typecheck`) |

## Conventions

- Go: standard `gofmt`, errors wrapped with `%w`, `log/slog` JSON logging, `context.Context` first. Deployment log lines carry `correlation_id` = deployment ID.
- Go lint: `make lint-go` runs a pinned golangci-lint v2 (config in `.golangci.yml`: the `standard` linters plus `gosec`, `errorlint`, `bodyclose`, `nilerr`, `unparam`, the diagnostic checks of `gocritic`, and `gofmt`) over every package, including the `integration`-tagged tests. Fix findings rather than silencing them; when a finding is a false positive, add `//nolint:<linter> // <reason>` on that line, and keep new config exclusions narrow, with a reason.
- Dead code: `make lint-dead` runs `scripts/ci/deadcode.sh` (Go functions no `main` package reaches; production code that only tests call counts as dead) and `scripts/ci/knip.sh` (unused files, exports and dependencies of the Bun workspace, configured in `knip.json`). Delete what they report; generated code is excluded in the scripts.
- TypeScript: biome (tabs, double quotes), strict `tsc`, the `#/*` import alias.
- Coverage: CI uploads the coverage of the Go tests (cross-package, `-coverpkg=./...`), the smoke scenarios and the web unit tests to [Codecov](https://app.codecov.io/gh/nebulactrl/nebulactrl). Every web module is loaded by `apps/web/src/modules.test.ts`, so an untested file counts as uncovered instead of missing. New code must be at least **90%** covered (`codecov/patch` is a required check); the project's target is 90% as well, shown on every pull request and required once the total reaches it. Generated code, `tools/` and test scaffolding do not count (`codecov.yml`). Measure Go locally with `go test -cover -coverpkg=./... ./... -args -test.gocoverdir=/tmp/cov` and `go tool covdata percent -i=/tmp/cov`, web with `bun test src --coverage` in `apps/web`.
- Web motion and surfaces follow the tokens in `apps/web/src/styles.css`; animate named properties only (never `transition-all`), and gate motion with `MotionConfig reducedMotion="user"`.
- Decisions that change the architecture get an ADR in `docs/decisions/` (Context, Decision, Consequences, Alternatives).
- Commits follow [Conventional Commits](https://www.conventionalcommits.org/) (see [Commits and versions](#commits-and-versions)); one logical change per commit.

## CI

GitHub Actions runs on standard GitHub-hosted runners (`ubuntu-24.04`; `ubuntu-24.04-arm` for the native arm64 image build).

| Workflow | When | What |
|---|---|---|
| `ci` | every pull request to `main`, every push to `main`, on demand, and as a release gate | tier 1 (`go vet`, `go test` in 3 shards, web typecheck/lint/knip/test/build, with coverage uploaded to Codecov), tier 2 (the smoke Docker scenarios), and the static checks: workflow rules + actionlint + zizmor, govulncheck, golangci-lint + deadcode and, on pull requests, dependency review |
| `docs` | every pull request to `main`, every push to `main`, on demand, and as a release gate | docs links, search index, web and site builds |
| `full` | nightly (03:17 UTC, on `main`), on demand (`templates: sample` or `all`; `run` for a focused repeat of chosen tests), and as a release gate | tier 3, every Docker scenario, in 4 shards |
| `security` | nightly (04:41 UTC, on `main`), on demand, and on a push to `main` that changes `bun.lock`, `go.mod` or `go.sum` | govulncheck, osv-scanner over `bun.lock` and `go.mod`, and a Grype scan of `ghcr.io/nebulactrl/nebulactrl:latest` whose findings go to code scanning (skipped while no release exists) |
| `scorecard` | every push to `main`, weekly (Saturday 05:29 UTC), and when a branch protection rule changes | [OpenSSF Scorecard](https://scorecard.dev/viewer/?uri=github.com/nebulactrl/nebulactrl); the result is published for the README badge and uploaded to code scanning |
| `release` | on demand only, from `main` | the gates above, then the release itself (below) |

**Pull requests.** A pull request to `main` produces these checks, which are the required status checks of the `main` ruleset:

- `go (shard 1/3)`, `go (shard 2/3)`, `go (shard 3/3)`
- `web typecheck, lint, test, build`
- `smoke (docker scenarios)`
- `docs links, search index, web and site builds`
- `workflows (rules, actionlint, zizmor)`
- `govulncheck`
- `golangci-lint`
- `dependency review`

A new run on the same pull request cancels the superseded one. Pushes to `main` run the same checks and are not cancelled by a manual run. The nightly `full` run is not a required check: when it fails, fix `main` or the test.

**Pull requests from forks** do not run until a maintainer approves the run (Settings → Actions → Fork pull request workflows: require approval for all external contributors). Read what the pull request changes in `.github/`, `Makefile`, `tests/`, `scripts/` and the package scripts before approving: a workflow run executes the pull request's code. Runs from forks get a read-only token and no secrets; the release secrets live in the `release-signing` and `release-site` environments, which only `main` can enter.

Workflow rules, enforced by `make lint-workflows` (the `workflows` check runs the same script; it also runs `actionlint` and [`zizmor`](https://docs.zizmor.sh/) at the versions pinned in `scripts/release/check-workflows.sh`, so it needs `go` and `uvx` or `pipx`; `zizmor` findings of medium severity or higher fail it):

- triggers are `workflow_dispatch`, `workflow_call`, `push`, `pull_request` and `schedule` only (plus `branch_protection_rule` in `scorecard.yml`, which Scorecard needs); never `pull_request_target` or `workflow_run`, which hand a fork's code the base repository's token and secrets;
- top-level `permissions: contents: read`; a job asks for more only for what it does (`contents: write` to tag or upload release assets, `packages: write` to push the image, `id-token: write` and `attestations: write` to attest a build);
- `actions/checkout` uses `persist-credentials: false` unless the job pushes with the checkout's credentials (only the release `version` job does);
- only standard GitHub-hosted runners (`ubuntu-24.04`, `ubuntu-24.04-arm`), no self-hosted or third-party labels;
- every action is pinned to a full commit SHA with its tag as a trailing comment (`uses: actions/checkout@<sha> # v7.0.1`; Dependabot proposes the updates monthly), and the repository only allows GitHub-owned actions plus `oven-sh/setup-bun`, `docker/setup-buildx-action`, `docker/login-action`, `docker/build-push-action` and `ossf/scorecard-action`;
- every job has `timeout-minutes`, so a hang cannot run for hours;
- the release workflow restores no cache in the jobs that hold secrets or publish (`site`, the image builds), so a cache entry written by another run cannot reach a release; `ci`, `full` and `docs` keep theirs;
- the base images of the `Dockerfile` are pinned as `tag@sha256:<digest>`; Dependabot bumps the tag and the digest together;
- security tools that are not actions run as `go run <module>@<version>`, `pipx run <package>==<version>` or a digest-pinned `docker run`, never as a floating version (govulncheck is pinned in `ci.yml` and `security.yml`, the Grype image in `release.yml` and `security.yml`: bump both together).

Tests run with `-count=1`, so a result is never replayed from a cache. The release image build uses no layer cache.

**Release provenance.** The `binaries` job attests the build provenance of the CLI and agent archives, and `image-manifest` attests the multi-architecture image (stored in the registry next to it); verification is in [`SECURITY.md`](SECURITY.md#verifying-a-release). The `images` job scans each architecture's image with Grype (`--only-fixed --fail-on critical`) before `image-manifest` creates any tag.

**Sharding.** `ci` runs the Go job as 3 shards of packages and `full` runs the Docker tier as 4 shards of tests, each shard on its own runner (`fail-fast: false`, a timeout per shard). The split is deterministic and balanced by recorded durations: `scripts/ci/go-durations.json` (per package, `go run ./tools/shardtests -packages -update-from <ci logs>`) and `tests/integration/durations.json` (per test, `-update-from full.log`); a new package or test without an entry gets a default weight, and `go test ./tools/shardtests` proves that every package and test runs in exactly one shard. Locally: `./tests/run.sh full --shard 1/4`.

## Commits and versions

Versions come from commit messages. Use `type(scope): subject` (scope optional) with the types below; `!` after the type, or a `BREAKING CHANGE:` footer, marks a breaking change.

| Commit | Bump while on 0.x |
|---|---|
| `feat:` | minor (0.1.0 → 0.2.0) |
| `fix:`, `perf:`, `revert:` | patch (0.1.0 → 0.1.1) |
| `feat!:`, `fix!:`, `BREAKING CHANGE:` footer | **minor** (0.1.0 → 0.2.0): see below |
| `docs:`, `chore:`, `ci:`, `build:`, `test:`, `refactor:`, `style:` | no release |

The highest bump among the commits since the last tag wins. If none of them needs a release, the release workflow says so and creates nothing.

**NebulaCtrl is on 0.x**, where Semantic Versioning allows a minor version to break things. `release.config.mjs` maps a breaking change to a **minor** bump on purpose: semantic-release's default would cut `1.0.0` the first time anyone writes `feat!:`. `make test-release-rules` checks this against the real semantic-release in a scratch repository under `/tmp` (a `feat!:` after `v0.1.0` yields `0.2.0`); run it after touching `release.config.mjs` or the pinned versions in `scripts/release/semantic-release.sh`.

## Releasing

1. Make sure `main` has the commits you want, pushed, with Conventional Commit messages.
2. **Actions → release → Run workflow**, branch `main`, **`dry_run` ticked**. It prints the version the release would have (job summary of `version`). It skips the gates and publishes nothing.
3. Run it again with `dry_run` unticked. The jobs run in this order, and nothing is published until the gates pass:
   - `gate-ci`, `gate-full` and `gate-docs` (the full tier contains the smoke scenarios, so `ci` runs without them);
   - `version`: creates the tag `vX.Y.Z` and a **draft** GitHub Release with generated notes;
   - `binaries`: CLI and agent archives for linux and macOS (amd64, arm64) and `SHA256SUMS`, attached to the draft (`make release-binaries VERSION=X.Y.Z` builds the same files locally into `dist/`);
   - `images`: the control-plane image `ghcr.io/nebulactrl/nebulactrl` (which also carries the CLI for every platform), one native build per architecture, then `image-manifest` tags `X.Y.Z`, `X.Y` and `latest` (built with `SITE_URL` and `GITHUB_URL` build args, which turn on the in-app docs' "Latest at" note and "Edit this page");
   - `sign`: signs `install.sh` (as the site serves it), `SHA256SUMS`, the two agent binaries from the image and `release-manifest.txt` (image digest, agent SHA-256, SHA-256 of every installer file) with minisign, and attaches the `.minisig` files to the draft. The installer, the control plane's one-click update and `install-agent.sh` verify them ([Verifying a release](docs/installing/installer.md#verifying-a-release)). It fails, before the site goes out, when the secret is missing or is not the key whose public half is in `internal/releasesign`;
   - `publish`: checks that the draft holds every asset of `binaries` and `sign` (7 archives and checksums, 9 signature and signed files: 16 in all), then publishes it and marks it latest;
   - `site`: after `publish`, builds `apps/site` with `SITE_URL=https://nebulactrl.com` and `GITHUB_URL=https://github.com/nebulactrl/nebulactrl` (the site shows its GitHub links, the release feed links each release page; unset, both are left out), stamps `install.sh` with the release version, serves `install.sh.minisig` and the signed files of every release at `releases/<version>/` (the installer looks there; GitHub's release assets carry the same files), writes the release feed (below) and deploys it with wrangler (needs the [owner actions](#owner-actions) below).
4. If `binaries`, `images`, `sign`, `publish` or `site` fail after `version` succeeded, use **Re-run failed jobs** on that run. Starting a new run would report "no release" because the tag already exists.

This repository has GitHub's immutable releases enabled: once a release is published its assets and tag are locked and nothing can be uploaded to it. So the release is created as a draft, every asset is attached to the draft, and only `publish` makes it public. Until then every job can be re-run against the draft, and `site` (the release feed) never announces a release that is not published.

The version is stamped into the binaries (`main.version`), the web app (`VITE_APP_VERSION`) and the image. The installer a control plane serves at `/install.sh` installs that control plane's own version; fetched raw from the repository it installs `latest`.

**The release feed.** The `site` job also publishes `https://nebulactrl.com/releases/latest.json` and `https://nebulactrl.com/releases/<version>.json`: `{version, publishedAt, notesUrl, notes}`, where `notes` is the body of the GitHub Release of that version (trimmed, cut at 20,000 characters with a link to the release page). Every control plane fetches `latest.json` daily to show its update notice and to offer the one-click update ([Updating NebulaCtrl](docs/operations/updating.md)). It is made by `apps/site/scripts/release-feed.ts` (tested in `release-feed.test.ts`) after the site build, so a normal build or a dry run has none. The job's last step warns when the live file does not show the new version. Check it by hand with `curl -fsSL https://nebulactrl.com/releases/latest.json`. Each deploy replaces the whole site, so only the newest `<version>.json` exists next to `latest.json`.

Only one release runs at a time and a running release is never cancelled. Release from `main` only. Nothing is committed back to the repository and nothing is published to npm: semantic-release runs through `npx` with exact versions pinned in `scripts/release/semantic-release.sh`, not in the bun lockfile.

### The first release

There is no tag yet, and semantic-release cannot make a first release on 0.x (it always starts at `1.0.0`). So when no `v*` tag exists the `version` job makes **`v0.1.0`** itself: an annotated tag at the released commit and a draft GitHub Release with generated notes, without semantic-release. A dry run says so. Every later release goes through semantic-release from that tag.

### Distribution

Installs and updates need no GitHub access and no login:

- **Images.** `ghcr.io/nebulactrl/nebulactrl` is a **public** package (package visibility is set on the package, not the repository). Servers and the installer pull it anonymously; nothing in `install.sh`, the agent installer or the compose files runs `docker login`, and none may be added.
- **Website.** `apps/site` is deployed to **nebulactrl.com** as Cloudflare Workers static assets (`apps/site/wrangler.jsonc`; headers and caching in `apps/site/public/_headers`) by the `site` job of the release workflow, and only by it. It serves `/install.sh` (stamped with the release version before the build, the same stamp the image gets), the docs and `llms.txt`. The job runs after `image-manifest`, because the stamped installer pulls that image tag, and is skipped on a dry run. To check the site locally without deploying: `cd apps/site && SITE_URL=https://nebulactrl.com bun run build && npx --yes wrangler@4.154.0 deploy --dry-run` (the number is `WRANGLER_VERSION` in `release.yml`; keep them equal).
- **CLI.** The control plane serves it: `curl -fsSL https://<control plane>/cli.sh | sh` detects the OS and CPU, downloads `/download/cli/{os}/{arch}`, checks it against `/download/cli/SHA256SUMS` and installs it. The image carries linux, darwin and windows builds for amd64 and arm64 under `/usr/share/nebulactrl/cli` (about 54 MB, 21 MB compressed). A development server has no binaries and answers 404 with a message (`NEBULACTRL_CLI_BINARIES_DIR` is unset). The GitHub Release archives of the `binaries` job are public too.

### Owner actions

Needed once, by the repository owner; the workflow cannot do them.

1. **Environment secrets** (Settings → Environments). Each environment has a deployment branch policy that allows only `main`, so the secrets reach only the one job that names the environment, and only on a release from `main`:
   - `release-site`, for the `site` job:
     - `CLOUDFLARE_API_TOKEN`: a custom API token with **Account → Workers Scripts → Edit** and **Zone → Workers Routes → Edit**, zone resources limited to `nebulactrl.com`. If the first deploy names another missing permission, add exactly that one.
     - `CLOUDFLARE_ACCOUNT_ID`: the ID of the Cloudflare account that owns the `nebulactrl.com` zone (Workers & Pages → Account ID). It is not a secret, so an environment **variable** of the same name works too.
   - `release-signing`, for the `sign` job:
     - `NEBULACTRL_MINISIGN_KEY`: the release signing key, an **unencrypted** minisign secret key (`minisign -G -W`, then paste the whole secret key file). Its public half is `internal/releasesign.PublicKey` and `NC_RELEASE_PUBKEY` in `deploy/install/install.sh`; replacing the key means changing both and shipping a release that the old key signs.
2. **After the first release, make the image public.** GitHub → the `nebulactrl` organisation → Packages → `nebulactrl` → Package settings → Danger zone → Change visibility → **Public**. A package a workflow creates is private until you do this, and until then every `docker pull` from a server fails with "denied". Later tags inherit it. Check with `docker logout ghcr.io; docker pull ghcr.io/nebulactrl/nebulactrl:latest`.
3. **Redirect www to the apex.** `wrangler.jsonc` attaches both `nebulactrl.com` and `www.nebulactrl.com` to the Worker (Cloudflare creates the DNS records), but a Worker custom domain cannot redirect. In the Cloudflare dashboard, zone `nebulactrl.com` → Rules → Redirect Rules → **Redirect from WWW to root** (a single redirect): when `http.host eq "www.nebulactrl.com"`, redirect with status 301 to `concat("https://nebulactrl.com", http.request.uri.path)` and preserve the query string.
4. **After the first deploy**, open `https://nebulactrl.com/install.sh` (it shows the release version in `NC_DEFAULT_VERSION=`), `https://nebulactrl.com/releases/latest.json` (the release feed, with the same version) and `https://www.nebulactrl.com/` (it redirects to the apex).

### Going to 1.0

An explicit, manual decision; nothing reaches 1.0 by accident.

1. In `release.config.mjs`, change the first release rule from `{ breaking: true, release: "minor" }` to `{ breaking: true, release: "major" }` (do not just delete it: a `fix!:` would become a patch) and update `scripts/release/test-release-rules.sh` to match.
2. Commit it, then add a breaking commit that says so, for example `git commit --allow-empty -m "feat!: first stable release"`.
3. Run the release workflow (dry run first). The next version is `1.0.0`; from then on a breaking change is a major bump.

(Alternatively create the `v1.0.0` tag by hand on the commit you choose; semantic-release continues from it.)
