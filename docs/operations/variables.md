---
title: Variables and secrets
description: "Encrypted variables, references between services and write-only secrets."
section: Applications
order: 50
---

# Variables and secrets

Variables are encrypted at rest (AES-256-GCM). A variable marked secret is write-only: no API, MCP tool or UI returns its value. Changes are staged and reach a container on the next deploy.

## Syntax

A value is plain text with `${{ … }}` expressions:

| Form | Meaning |
|---|---|
| `${{slug.KEY}}` | A variable, export or platform variable of another service in the same environment. |
| `${{KEY}}` | A variable (or platform variable) of the same service. |
| `${{shared.KEY}}` | A shared variable: the environment's value, else the project's. |
| `${{team.KEY}}` | A team variable: one value for every project of the team. |
| `${{vault:<name>/<path>#<key>}}` | A secret in an external secret manager (Vault, OpenBao, Infisical), fetched when a deploy starts and never stored. See [secret managers](secret-managers.md). |
| `$${{` | A literal `${{`. |
| `${{urlencode(x)}}`, `${{base64(x)}}` | Transforms of a reference or a quoted string (resolve time). |
| `${{secret(len, alphabet)}}` | A random string. `len` 1..1024 (default 32). `alphabet`: `alnum` (default), `hex`, `base64`, `base64url`, `digits`, `lower`, or a quoted literal of 2..128 distinct printable ASCII characters without `$ { }`. |
| `${{randomBytes(n, enc)}}` | `n` random bytes (1..1024) as `base64` (default), `hex` or `base64url`. |
| `${{randomInt(min, max)}}`, `${{uuid()}}` | A random integer (inclusive), a UUIDv4. |

Limits: 64 expressions per value, functions nest at most 3 deep, 64 KiB per value. Syntax errors return `422 invalid_template` with the offset. `shared`, `team` and `vault` are reserved: no service can use them as a slug. `vault.KEY` is a syntax error that points at the `vault:<name>/<path>#<key>` form.

## Generators run once

Generators run in the write path only, with `crypto/rand`. The generated text is stored (encrypted) together with the template; a deploy never regenerates it. Writing the same template text again keeps the value and the version, so `nebulactrl vars set TOKEN='${{secret(32)}}'` is safe in CI. `secret`/`randomBytes` force the variable to be secret (an explicit `isSecret:false` is rejected with `secret_required`). Values shorter than 8 characters warn (`weak_secret`); values shorter than 6 characters are not masked in logs.

Rotation is explicit: `POST /services/{id}/variables/{key}/rotate {"dryRun":true}` returns the plan (consumers, deploy order, downtime, warnings, blockers); `dryRun:false` stages the new value and restages consumers. Nothing deploys. Database credentials and `*PASSWORD*` variables of volume-backed services cannot be rotated once deployed (`rotation_unsupported`): the image reads them only when it initialises its data directory. Use a second role or the database panel. `rotateEveryDays` sets a reminder only.

## Layers and restaging

Service variables, environment-shared variables, project-shared variables and team variables. A shared variable reaches a service only through an explicit `KEY=${{shared.KEY}}` row ("Share to services" stages it); a team variable the same way with `KEY=${{team.KEY}}`. Shared values may reference only other shared variables, team variables, external secrets, generators and transforms; team values only other team variables, external secrets, generators and transforms. Cycles are rejected at write time (`reference_cycle`); a reference to something that does not exist yet is a warning, and blocks a deploy (`unresolved_reference`).

When a write changes the resolved environment of other services (a provider's password, a shared value, a deleted environment override), those services are restaged: their generation advances and the staged changes show the row with `origin: inherited` and `via`.

### Team variables

`${{team.KEY}}` is the third shared layer, next to the project and the environment, and applies to every project of the team. Edit them in the UI under Shared variables → Whole team, or through `GET|PUT /team/shared-variables`, `DELETE /team/shared-variables/{key}`, `POST /team/shared-variables/{key}/rotate|share` and `GET …/history`. Same behaviour as the other layers: secrets are write-only, a change restages every service that resolves to the value (in any project), cycles are rejected, versions and audit entries (`shared_variable.set|delete|rotate` with `"layer":"team"`) keep fingerprints only. Only owners and admins may change them; a member or token restricted to some projects has no access, because the values reach every project.

## Platform variables

User keys named `PORT` or starting with `NEBULA_` are reserved. Release-scoped (referenceable): `PORT`, `NEBULA_SERVICE_NAME`, `NEBULA_ENVIRONMENT`, `NEBULA_PRIVATE_HOST`, `NEBULA_PUBLIC_DOMAIN`, `NEBULA_PUBLIC_URL` (always `https://`), `NEBULA_PROJECT_NAME`, `NEBULA_SERVICE_ID`, `NEBULA_ENVIRONMENT_NAME`. Known once the source is pinned (own service only): `NEBULA_GIT_COMMIT_SHA`, `NEBULA_GIT_BRANCH`, `NEBULA_IMAGE`. Per deployment: `NEBULA_DEPLOYMENT_ID`, `NEBULA_RELEASE_ID` (a rollback carries the new deployment's id). Per container (set by the agent): `NEBULA_REPLICA_INDEX`, `NEBULA_REPLICA_COUNT` (the count at the time the container was created), and, in a service with [processes](processes.md), `NEBULA_PROCESS` (the container's process name).

Exports (for example a database's `DATABASE_URL`) are computed by the resolver, not stored, and never injected into the exporting service. A user variable with the same key wins.

**Ports depend on who asks when the provider is on another server.** `${{api.PORT}}` and every port-bearing export (`DATABASE_URL`, `DATABASE_PORT`, `PGPORT`, `REDIS_URL`, the `DATABASE_WRITE_*` and `DATABASE_READ_*` family of a [replica group](database-replicas.md#writer-and-reader-variables)) resolve **per consumer**: to the provider's mesh port when the provider is exposed over the [mesh](mesh.md#cross-server-service-links) and runs on a different server than the consumer, and to its container port otherwise. The host (`NEBULA_PRIVATE_HOST`, the slug) never changes. The mesh port equals the container port whenever that port is free on the provider's server, so most references keep their value. Editing a variable that references a service on another server never adds a mesh link by itself: the staged bar says "Mesh: adds link A → B (1 device)" and the deploy of the consumer adds it.

## Build-time variables (git services)

Scope `runtime` (default), `build` or `both`. Plain build/both values become `--build-arg`; secret ones (including values that derive from a secret) become BuildKit secrets (`--secret id=KEY,env=NCBS_KEY`, the value in the docker process environment, never argv): use `RUN --mount=type=secret,id=KEY,env=KEY …`. A secret is never offered as a build arg. Build scope needs a git source (`build_scope_requires_git`) and, for secrets, Docker 23+ on the host (`buildkit_required`; there is no silent fallback). Git SHA/branch variables are runtime only, to keep the layer cache.

## Suggestions

Suggestions come from two places: the template env files of the repository and the databases of the environment.

### Keys from `.env.example` (prefill)

The files `.env.example`, `.env.sample`, `.env.template` and `.env.dist` are read from the repository root and the build context (64 KiB and 500 keys at most). `.env`, `.env.local` and `.env.production` are never read. They are read **before the first deploy**:

- through the git provider's API when the service is linked to a GitHub, GitLab, Gitea or Forgejo connection, or the URL is a public repository on github.com or gitlab.com;
- otherwise (Bitbucket, other hosts, SSH, a private repository behind a token, a provider that fails) by a scan-only command on the service's server: a depth-1 fetch of the branch head into memory, nothing checked out, nothing built. A pasted token is sent at send time only and is never stored in the command or the database. An SSH repository can only be read once the service exists and has a deploy key.

Smart defaults per key: a database export of the environment becomes a reference (`DATABASE_URL=${{postgres.DATABASE_URL}}`); a secret-looking key with a placeholder becomes a generator (`${{secret(32)}}`); a safe example value is kept; a value pointing at localhost is cleared; placeholder text such as `changeme` or `<your-key>` is never proposed as a value; platform keys (`PORT`, `NEBULA_*`) are left out and reported. The comment above a key is its hint.

- **Create dialog:** the Git repository dialog reads the repository as it is filled in and, when keys are found, offers a Variables step. Every key is checked; a key that needs your own value is a field. Creating the service stages the checked keys with it (`source: "suggested"`).
- **Variables tab:** a banner "N keys from .env.example aren't set" opens the same review sheet for bulk add. "Read again" fetches the files now.
- **Kept current:** the files are read again when a git service is created or its repository, branch, build context, server or token changes (a background job), and on every push to the tracked branch. A push that lists its changed files and touches no template file skips the read.
- **Deploy plan:** variables added from the example that still have no value produce the warning "3 variables from .env.example have no value" and list them. It never blocks a deploy; setting a value (any normal write) clears it.

API: `POST /services/{id}/env-template-scan` and `POST /environments/{id}/env-template-preview` (nothing is created or stored by the preview). A repository that cannot be read answers `status: unavailable` with the reason instead of an error.

### Databases

`GET /services/{id}/suggested-variables` also lists databases of the environment that nothing references yet. Accepting a suggestion is a normal staged write.

## History and audit

Every change appends a version (kept: 50 per key, 180 days). Non-secret versions keep their value (restorable as a new version); secrets keep only an HMAC fingerprint derived from the instance secret key (matched against every key of the [key ring](../security/secrets-at-rest.md) after a rotation). Audit entries (`variable.set|delete|rotate|restore|share`, `shared_variable.*`) list keys, versions, non-secret values (truncated) and fingerprints, plus the affected services; never a secret value.

## Redaction

Logs are masked for every secret value, every secret substituted into a derived variable, and their URL-escaped and base64 forms (values of 6+ characters), on the server and the agent. Values fetched from a secret manager are masked the same way: the control plane keeps them in memory only (the last three deploys of a service, for at most 30 minutes) and re-fetches them on demand, once a minute at most.

CLI: `nebulactrl vars ls|set|rm|rotate|history`, `nebulactrl vars shared ls|set|rm|rotate|share <project[/env]>`.

## In nebula.toml

A [nebula.toml](project-config.md) file holds variable names, references and generators, never secret values. Declare a secret by name with `secrets.keys = ["NAME"]` and set its value in the dashboard or with `nebulactrl vars set`; a literal value under a credential-looking name such as `PASSWORD` or `API_KEY` is rejected (`literal_secret`). Applying the file never reads or rewrites a stored secret. See the [reference](../reference/nebula-toml.md#variables-and-secrets).
