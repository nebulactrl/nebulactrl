---
title: Migrate with an AI agent
description: "A playbook your coding agent follows to move Coolify or Dokploy projects to NebulaCtrl: inventory, mapping, staging, data copy and checks, with secret values kept out of its context."
section: Installing
order: 36
---

# Migrate with an AI agent

This page is a playbook for a coding agent (Claude Code, Codex, Cursor, …). It moves the projects of a Coolify v4 or Dokploy instance to NebulaCtrl through the `nebulactrl` CLI and `nebula.toml`: inventory, mapping, staging, data copy and checks. The person approves every checkpoint and runs the cutover. Variable values, passwords and tokens never enter the agent's context.

## For the person

Paste this prompt into your agent:

```text
Migrate my <Coolify|Dokploy> instance at <URL> to NebulaCtrl. Follow https://nebulactrl.com/docs/installing/migrate-with-an-ai-agent.md step by step and stop at every checkpoint for my approval.
```

Your control plane serves the same page at `<your control plane>/docs/installing/migrate-with-an-ai-agent.md`.

Set up before you paste it:

1. **NebulaCtrl installed**, side by side on the same host (the [installer](installer.md) with the flags from [Migrate from Coolify](migrate-from-coolify.md#1-install-nebulactrl-side-by-side)) or on a second machine. A Dokploy host runs Docker Swarm; if the installer or the proxy fails there, install NebulaCtrl on a second machine and move DNS at the cutover.
2. **`nebulactrl login`** done in the shell the agent uses.
3. **The source API token in a file only you can read.** Paste it on standard input, then press Ctrl-D:

   ```sh
   install -d -m 0700 ~/.nebula-migration
   install -m 0600 /dev/stdin ~/.nebula-migration/source.token
   ```

   Coolify: a token with `read` and `read:sensitive`. Dokploy: an API key of an owner or admin (Settings → Profile → API/CLI).
4. **SSH access** from the agent's shell to the source host(s), for the data copy.
5. **Private repositories and registries:** a git connection in Team settings → Integrations.

You keep for yourself: approving each checkpoint, entering registry passwords in the dashboard, and the cutover window.

## Rules

Every step obeys these rules.

- **Sealed values.** Variable values, passwords, tokens, compose files and file-mount contents are **sealed**: they travel only through files and pipes, never through your transcript.
  - Start every command that writes under `~/.nebula-migration` with `umask 077` (directories `0700`, files `0600`). Write sealed values to files under `~/.nebula-migration/<project>/` straight from the API response with `jq`.
  - Move them on only with `nebulactrl vars set <svc> KEY=- [--secret] < <file>`, or with `--file`/`--token-file`-style arguments.
  - Read JSON only through the [redaction filter](#reference-redaction-filter). List variables by key only.
  - When a value must be inspected (a reference or a literal?), run the [reference check](#reference-references): it prints key names, never values.
  - Tokens go in commands as `$(cat <file>)`, so they appear in no command text or output. Use `curl -fsS`, never `-v`.
- **Read-only source.** Send only GET requests to the source API. On the source host run only `docker ps`, `docker service ls`, `docker inspect` (through the redaction filter) and the dump commands of [Step 5](#step-5-data-checkpoint-2). The source stays untouched until the cutover, and the person runs the cutover.
- **Staged first.** Everything is created staged. Deploy only what the person approved at checkpoint 2 (services whose volumes receive data) or checkpoint 3 (everything else).
- **Schema-checked `nebula.toml`.** Follow the [authoring rules](../operations/project-config.md#writing-the-file-with-an-ai-assistant): start from `config export`, use only keys from `nebulactrl config schema`, put names in `secrets.keys` and values nowhere in the file, then `config validate` → `config plan` → `config apply`.
- **Checkpoints.** At a checkpoint, show the summary file and stop until the person answers.

`<project>` below is the source project's id (Dokploy `projectId`, Coolify project uuid), which is safe as a directory name. `<url>` is the source instance.

## Step 1: Preflight

1. `command -v nebulactrl curl jq python3 ssh` finds all five.
2. `nebulactrl --json teams ls` answers; note the logged-in team.
3. `nebulactrl --json servers ls` shows at least one online server. When there are several, ask the person which server receives the services.
4. Detect the source:
   - Coolify answers `curl -fsS -H "Authorization: Bearer $(cat ~/.nebula-migration/source.token)" <url>/api/v1/version` with its version.
   - Dokploy answers `curl -fsS -H "x-api-key: $(cat ~/.nebula-migration/source.token)" <url>/api/project.all`. Save that response sealed as `~/.nebula-migration/raw/project-all.json`. Record whether `.[0].environments` exists (v0.25 and later) or not (older: the service arrays sit on the project). Read the version from the live spec at `<url>/swagger` or the dashboard footer.

**Done when** `~/.nebula-migration/preflight.md` names the source kind and version, the target server and the logged-in team.

## Step 2: Inventory

### Coolify

1. Read the importer's plan; it carries no values:

   ```sh
   umask 077; nebulactrl import coolify --url <url> --token-file ~/.nebula-migration/source.token \
     --server <server> --plan --json > ~/.nebula-migration/coolify-plan.json
   ```

   It exits 1 when the plan has blockers; the file is still written. Summarise its projects, resources, `unsupported` entries and follow-ups.
2. Read what the importer leaves out, saving raw responses sealed under `~/.nebula-migration/raw/` (header `Authorization: Bearer $(cat ~/.nebula-migration/source.token)`, base `<url>/api/v1`):
   - one-click services: `GET /services` (with `docker_compose_raw`) as `services.json`, and `GET /services/{uuid}/envs` as `service-<uuid>-envs.json`;
   - scheduled tasks per application: `GET /applications/{uuid}/scheduled-tasks` (name, frequency, command);
   - file mounts: `GET /applications/{uuid}/storages`, list `file_storages`;
   - port mappings named in the plan's `unsupported` lists.

### Dokploy

1. List every service id, without values:

   ```sh
   cd ~/.nebula-migration && jq -r '
     .[] | .projectId as $p
     | (.environments // [. + {name: "production"}])[] | .name as $e
     | (["applications","application","applicationId"], ["compose","compose","composeId"],
        ["postgres","postgres","postgresId"], ["mysql","mysql","mysqlId"], ["mariadb","mariadb","mariadbId"],
        ["mongo","mongo","mongoId"], ["redis","redis","redisId"], ["libsql","libsql","libsqlId"]) as [$list, $proc, $idf]
     | (.[$list] // [])[] | [$p, $e, $proc, $idf, .[$idf], .name, (.appName // "")] | @tsv' raw/project-all.json > ids.tsv
   ```

2. Fetch every record and its schedules, sealed, with a redacted view next to it:

   ```sh
   umask 077; cd ~/.nebula-migration
   while IFS=$'\t' read -r p e proc idf id name app; do
     mkdir -p "$p/raw"; [ "$proc" = libsql ] && continue
     curl -fsS -H "x-api-key: $(cat source.token)" "<url>/api/$proc.one?$idf=$id" > "$p/raw/$proc-$id.json"
     case $proc in application|compose)
       curl -fsS -H "x-api-key: $(cat source.token)" "<url>/api/schedule.list?id=$id&scheduleType=$proc" > "$p/raw/schedule-$id.json";;
     esac
     jq -f redact.jq "$p/raw/$proc-$id.json" > "$p/raw/$proc-$id.redacted.json"
   done < ids.tsv
   ```

3. Field names come from canary (v0.31.0). Confirm the names you map against one redacted record of each kind before mapping; the published spec documents empty response bodies.
4. Split each `env` (applications, compose, the project and each environment) into one sealed file per key with the [splitter](#reference-redaction-filter). When `grep -l '^enc:' <dir>/env/*` prints a name, the instance stores ciphertext: stop and report to the person.

### Both

Write `~/.nebula-migration/inventory.md`, one row per resource:

- project / environment / name / kind;
- source: image, or git repo + branch + build type + Dockerfile path;
- port(s), domains, mounts, replicas, limits, health check, schedules;
- variable keys;
- database engine + image tag;
- the source server (Dokploy `serverId`, or the Coolify server).

**Done when** every id from the list calls has a row and the file holds no value. Check with this command, which prints a count (`0`) and nothing else:

```sh
grep -cF -f <(for f in ~/.nebula-migration/*/env/* ~/.nebula-migration/*/*/env/*; do [ -f "$f" ] && head -n1 "$f" && echo; done | awk 'length >= 8') ~/.nebula-migration/inventory.md
```

## Step 3: Map (checkpoint 1)

1. Dokploy: apply the [Dokploy mapping](#reference-dokploy-mapping). Coolify: take the importer's mapping and the [Coolify leftovers](#reference-coolify-leftovers).
2. Write `~/.nebula-migration/plan.md`, one row per resource:
   - the target: nebula.toml service, managed database, compose stack, or **manual** for the person, with the reason;
   - variables to rewrite to references ([reference check](#reference-references));
   - data to copy;
   - anything that needs an unrestricted server;
   - Redis and MongoDB targets, which need the person's acceptance of their source-available licence.
3. When a NebulaCtrl project of the same name exists (`nebulactrl --json projects ls`), propose `<name> (Dokploy)` or `<name> (Coolify)`.
4. **Checkpoint 1:** show `plan.md` and wait for approval or edits.

**Done when** the person approves `plan.md`.

## Step 4: Stage

Side by side on one host: HTTP-01 cannot validate while the source owns port 80, so give every domain `dns_resolver` or cover it with a wildcard certificate ([why](migrate-from-coolify.md#2-recreate-the-projects)).

### Coolify

1. Import with the flags the approved plan implies (`--rename-project`, `--exclude`, `--include`, `--git-connection`, `--set-env`):

   ```sh
   nebulactrl import coolify --url <url> --token-file ~/.nebula-migration/source.token --server <server> [flags]
   ```

2. Each one-click service:

   ```sh
   umask 077; d=~/.nebula-migration/<project>/<name>; mkdir -p "$d"
   jq -r --arg u <uuid> '.[] | select(.uuid == $u) | .docker_compose_raw' ~/.nebula-migration/raw/services.json > "$d/docker-compose.yml"
   jq -r '.[] | select(.is_preview | not) | "\(.key)=\(.real_value // .value | @json)"' ~/.nebula-migration/raw/service-<uuid>-envs.json > "$d/.env"
   nebulactrl import compose "$d/docker-compose.yml" --project <p> --env <e> --server <server> --as-stack --plan
   ```

   Then the same without `--plan`. Add `--secret <KEY>` for every key the [secret-name rule](#reference-secret-name-rule) marks.
3. Each scheduled task: `nebulactrl tasks create <svc> --name <name> --schedule '<frequency>' -- sh -c '<command>'`. Coolify's `daily`, `hourly`, `weekly`, `monthly`, `yearly` become `@daily` and so on.
4. Each file mount: a `files."/path"` entry in nebula.toml when it is at most 64 KiB and not a secret (write `content` from the sealed file with a script, never by hand); otherwise it goes to the person.

### Dokploy

1. `nebulactrl --json projects create <name>` (creates `production`; note the project id).
2. Every other environment, through the API:

   ```sh
   cfg=~/.config/nebulactrl/config.json
   curl -fsS -H "Authorization: Bearer $(jq -r .token $cfg)" -H 'Content-Type: application/json' \
     -d '{"name":"<env>","isProduction":false}' "$(jq -r .url $cfg)/api/v1/projects/<projectId>/environments"
   ```

3. Databases first: `nebulactrl --json databases create --project <p> --env <e> --engine <engine> --version <v> --name <name> --server <server>`. `<v>` is the closest version in `nebulactrl databases engines` that is not older than the source's major. Redis and MongoDB need `--accept-license`, and only when the person accepted it at checkpoint 1. Note each database's slug.
4. Application services: `nebulactrl config export --project <p> --env <e> -o ~/.nebula-migration/<project>/<env>.nebula.toml`, add the services from the mapping, then `config validate`, `config plan` and `config apply`, each with `--env <e> -f <that file>`.
5. Compose apps:

   ```sh
   umask 077; d=~/.nebula-migration/<project>/<name>; mkdir -p "$d"
   jq -r '.composeFile' ~/.nebula-migration/<project>/raw/compose-<id>.json > "$d/docker-compose.yml"
   jq -r '.env // ""' ~/.nebula-migration/<project>/raw/compose-<id>.json > "$d/.env"
   nebulactrl import compose "$d/docker-compose.yml" --project <p> --env <e> --server <server> --as-stack --plan
   ```

   Then the same without `--plan`, with `--domain <serviceName>=<host>` for each compose domain and `--secret <KEY>` for each key the secret-name rule marks. When the plan names a problem in the file (an external `dokploy-network`, a host bind mount), fix it with `sed` by line, without printing the file.
6. Variable values, per service and per key:

   ```sh
   nebulactrl vars set <svc> KEY=- [--secret] [--scope build] < ~/.nebula-migration/<project>/<name>/env/KEY
   ```

   `--secret` when the key matches the secret-name rule or Dokploy listed it under `buildSecrets`. `--scope build` for `buildArgs` and `buildSecrets` keys (split them like `env`); build scope exists only on git services.
7. Keys the reference check maps to a Dokploy database are set to the reference instead, `nebulactrl vars set <svc> 'KEY=${{<db-slug>.DATABASE_URL}}'`; their values are not copied, because the new database has new credentials.

**Done when** `config plan` reports no changes for every environment, every inventory row is staged or marked manual in `plan.md`, and `nebulactrl vars ls <svc>` shows every key that was not skipped.

## Step 5: Data (checkpoint 2)

**Checkpoint 2:** show the person the list of dumps and volume copies, and that the source keeps running. Wait for approval.

### Databases

Dumps run on the source host over SSH, into sealed files under `~/.nebula-migration/<project>/dumps/`. Find the container with `docker ps -q -f name=<appName or Coolify uuid>`; Dokploy Swarm task names start with `<appName>.`. Credentials come from the container's own environment, expanded inside `sh -c`. List the variable names with `docker exec <c> env | cut -d= -f1` and use the one present.

| Engine | Dump |
|---|---|
| Postgres | `docker exec <c> pg_dump -U <user> -Fc <db> > <file>.pgdump` (`user`, `db`: `databaseUser`, `databaseName`; not secret) |
| MySQL | `docker exec <c> sh -c 'exec mysqldump -uroot -p"$MYSQL_ROOT_PASSWORD" --single-transaction --routines --triggers <db>' > <file>.sql` |
| MariaDB | the same with `mariadb-dump` and `$MARIADB_ROOT_PASSWORD`, else `$MYSQL_ROOT_PASSWORD` |
| MongoDB | `docker exec <c> sh -c 'exec mongodump --archive --username "$MONGO_INITDB_ROOT_USERNAME" --password "$MONGO_INITDB_ROOT_PASSWORD" --authenticationDatabase admin' > <file>.archive` |
| Redis / Valkey | starts empty: `databases restore` refuses RDB files. Note it in the report |

Run each as `ssh <source host> '<dump command>' > <file>`. Restore with `nebulactrl databases restore <db> --file <file>`, which restores into a new sibling database; point the references at it as the command prints. When the person chose in-place at checkpoint 2: `--in-place --confirm <slug> --destination <d>`.

### Volumes

Named volumes (Dokploy `volume` mounts, Coolify storages):

1. Deploy the NebulaCtrl service once so its volume exists, and find it: `docker volume ls -q -f name=nc-vol-<service id>`.
2. Find the source volume in `docker inspect <c> --format '{{json .Mounts}}'`.
3. The person stops the source app's writers. Then copy:
   - same host: `docker run --rm -v <src>:/from:ro -v <dst>:/to alpine sh -c 'cp -a /from/. /to/'`;
   - another host: `docker run --rm -v <src>:/from:ro alpine tar -C /from -cf - . | ssh <target> 'docker run --rm -i -v <dst>:/to alpine tar -C /to -xf -'`.
4. `nebulactrl services restart <svc>`.

Bind mounts (host paths) go to the person.

**Done when** every database row has a restored database and every volume row is copied or handed over, each recorded in `~/.nebula-migration/data.md`.

## Step 6: Deploy and verify (checkpoint 3)

1. **Checkpoint 3:** ask before deploying.
2. `nebulactrl services deploy <svc> --wait`: databases first, then the apps in the depends-on order of the plan.
3. Test each domain and compare the status code and `<title>` with the same URL on the source:
   - side by side on one host: `curl -sk --resolve <host>:8443:127.0.0.1 https://<host>:8443/` (the proxy ports from the install);
   - second machine: `curl -sk --resolve <host>:443:<target ip> https://<host>/`.
4. On a failure: `nebulactrl services logs <svc> --tail 200`, fix nebula.toml or the variables, and deploy again, at most three attempts per service. Then report it to the person.

**Done when** every app answers with the same status as the source and `~/.nebula-migration/report.md` lists the result per service.

## Step 7: Hand over the cutover

Write the cutover steps for the person into `report.md`; the person runs them. Take them from [Migrate from Coolify](migrate-from-coolify.md#4-the-cutover-window): stop the writers, take the final dumps and restore, stop the source proxy, move NebulaCtrl's proxy to 80/443, switch DNS, smoke test. Add the [roll back](migrate-from-coolify.md#5-roll-back) steps.

Dokploy: name the containers and services that publish 80/443 and the dashboard (port 3000) as observed with `docker ps --format '{{.Names}} {{.Ports}}'` and `docker service ls`. A Swarm service stops with `docker service scale <name>=0` and comes back with its old replica count; a container with `docker stop` / `docker start`.

**Done when** `report.md` holds the cutover and rollback commands and every manual item left: registry passwords, bind-mount data, basic-auth users, redirects, Traefik middlewares, backup schedules to recreate, and the source token to revoke.

## Reference: redaction filter

Save this once as `~/.nebula-migration/redact.jq` and read every JSON response through `jq -f ~/.nebula-migration/redact.jq <file>`. It replaces every value field with `"<sealed>"` and keeps names, ids and structure:

```jq
walk(if type == "object" then with_entries(
  if (.key | test("(?i)^(env|previewenv|buildargs|buildsecrets|composefile|docker_compose(_raw)?|content|value|real_value|custom_docker_run_options)$|password|secret|token|privatekey|private_key|apikey|api_key|sshkey|_db_url$"))
  then .value = "<sealed>" else . end) else . end)
```

The splitter writes one sealed file per key of a dotenv string (`KEY=VALUE` lines; quoted values may span lines; `#` comments are skipped) and prints only the keys. It is also the key lister. Save it as `~/.nebula-migration/split-env.py`:

```python
import os, re, sys

LINE = re.compile(r"""^\s*(?:export\s+)?([\w.-]+)(?:\s*=\s*?|:\s+?)(\s*'(?:\\'|[^'])*'|\s*"(?:\\"|[^"])*"|\s*`(?:\\`|[^`])*`|[^#\r\n]+)?\s*(?:#.*)?$""", re.M)
NAME = re.compile(r"^[A-Za-z_][A-Za-z0-9_]*$")

src, out = sys.argv[1], sys.argv[2]
text = open(src, encoding="utf-8").read().replace("\r\n", "\n")
os.makedirs(out, mode=0o700, exist_ok=True)
for m in LINE.finditer(text):
    key, val = m.group(1), (m.group(2) or "").strip()
    if not NAME.match(key):
        print("skipped (invalid name):", key)
        continue
    if len(val) > 1 and val[0] in "'\"`" and val[-1] == val[0]:
        quote, val = val[0], val[1:-1]
        if quote == '"':
            val = val.replace("\\n", "\n").replace("\\r", "\r")
    fd = os.open(os.path.join(out, key), os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    os.fchmod(fd, 0o600)
    with os.fdopen(fd, "w", encoding="utf-8") as f:
        f.write(val)
    print(key)
```

```sh
umask 077; d=~/.nebula-migration/<project>/<name>
jq -r '.env // ""' ~/.nebula-migration/<project>/raw/application-<id>.json > "$d.env"
python3 ~/.nebula-migration/split-env.py "$d.env" "$d/env"
```

Coolify variables are a JSON list: `jq -r '.[].key'` lists them, and `jq -j --arg k KEY '.[] | select(.key == $k) | .real_value // .value' <file> > <dir>/env/KEY` seals one.

## Reference: references

Save the database hosts, one per line, in `~/.nebula-migration/<project>/hosts`: each database's `appName` (Dokploy) or uuid (Coolify). The database passwords go sealed into `<project>/db/<appName>.password` (`jq -j .databasePassword`). Then:

```sh
cd ~/.nebula-migration/<project>
while read -r h; do grep -lF "$h" */env/* | sed "s|\$| -> $h|"; done < hosts
for f in */env/*; do for p in db/*.password; do cmp -s "$f" "$p" && echo "$f -> $(basename "$p" .password) password"; done; done
grep -l '\${{' */env/*
```

The commands print file names (`<service>/env/<KEY>`), never values. Each hit of the first two becomes a reference to the new database:

| The value holds | Reference |
|---|---|
| a Postgres, MySQL or MariaDB URL | `${{<db-slug>.DATABASE_URL}}` |
| the host only | `${{<db-slug>.DATABASE_HOST}}` (`REDIS_HOST` for Redis) |
| a Redis URL | `${{<db-slug>.REDIS_URL}}` |
| a MongoDB URL | `${{<db-slug>.MONGO_URL}}` |
| the password | `${{<db-slug>.DATABASE_PASSWORD}}` (`REDIS_PASSWORD` for Redis) |

Hits of `grep -l '\${{'` hold Dokploy references:

- `${{project.X}}` and `${{environment.X}}` become shared variables. Seal `X` from the project's or the environment's `env`, then `nebulactrl vars shared set <project> X=- [--secret] < <file>` (or `<project>/<env>`). A key whose whole value is the reference: `nebulactrl vars shared share <scope> X <svc>`. Inside a longer value, rewrite it in the sealed file and set it as usual: `sed -E 's/\$\{\{(project|environment)\.([A-Za-z_][A-Za-z0-9_]*)\}\}/${{shared.\2}}/g' <file> > <file>.new && mv <file>.new <file>`.
- `${{X}}` (the service's own variable) means the same in NebulaCtrl; keep it.

## Reference: secret-name rule

A key is a secret when it matches `(?i)(pass|secret|token|key|salt|private|credential|auth|dsn|api_?key|_url$)`, or when the reference check found a database password in its value. All other keys are plain.

## Reference: Dokploy mapping

| Dokploy | NebulaCtrl |
|---|---|
| project / environment | project / environment (before v0.25: the project's services go to `production`) |
| application `docker` | `image` (tag kept; no tag → `latest` + warning). Registry credentials (`username`/`password`, `registry`) go to the person |
| application git (`github`/`gitlab`/`gitea`/`bitbucket`/`git`) + `dockerfile` | `git` + `build.type = "dockerfile"`, `build.dockerfile` = build path + `dockerfile` relative to the repository root, `build.context` = `dockerContextPath` or the build path |
| `nixpacks`/`railpack`/`static`/`heroku_buildpacks`/`paketo_buildpacks` | `build.type = "railpack"` + note: review `nebulactrl services build-plan <svc>` |
| `drop` | manual: commit the code to a repository |
| `command`/`args` | `command` argv |
| `memoryLimit` bytes | `memory_mb` = ceil(bytes / 1048576) |
| `cpuLimit` nano-CPUs | `cpus` = n / 1e9 |
| `replicas` | `scale.replicas` (only without volumes and with a port; at most 10) |
| `healthCheckSwarm` | `health_check`: `http` with `path`/`port` when `Test` is a curl or wget of `localhost:<port><path>`, else `exec` with `Test` as `command`; nanoseconds → seconds; `deadline_seconds` = start period + retries × interval |
| domains | `domains."host"` (`port` → `target_port`; `path` other than `/` → `"host/path"` key); `certificateType = "custom"` goes to the person |
| `ports` | TCP/UDP route: manual, listed in the plan |
| mounts `volume` / `bind` / `file` | `volumes` / manual / `files` (at most 64 KiB, not a secret) |
| `redirects`, `security` (basic auth) | manual, listed in the plan |
| `schedule.list` | `tasks` (a shell `script` → `["sh", "-c", script]`) |
| compose (`docker-compose`) | `import compose --as-stack` |
| compose (`stack`, Swarm) | the same, noting that Swarm `deploy:` keys are dropped |
| postgres / mysql / mariadb / mongo / redis | managed database, same engine, closest catalog version |
| libsql | manual |

Git source fields per `sourceType` (confirm in the live record): `github` `owner`/`repository`/`branch`/`buildPath`; `gitlab` `gitlabOwner`/`gitlabRepository`/`gitlabBranch`/`gitlabBuildPath`; `gitea` and `bitbucket` the same with their prefix; `git` `customGitUrl`/`customGitBranch`/`customGitBuildPath`. With a git connection for the host, write `git.connection` + `git.repo = "owner/name"`; a public repository takes `git.url`. A custom URL with an SSH key goes to the person (a deploy key in the service's Source settings).

## Reference: Coolify leftovers

What `nebulactrl import coolify` leaves out ([Import from Coolify](../operations/import-coolify.md#what-stays-manual)), and the agent's action:

| Left out | Action |
|---|---|
| one-click services | `import compose --as-stack` ([Step 4](#step-4-stage)) |
| scheduled tasks | `tasks create` |
| file mounts | `files` in nebula.toml, or the person |
| port mappings | manual, listed in the plan |
| registry credentials | the person, in the dashboard |
| data of databases and volumes | [Step 5](#step-5-data-checkpoint-2) |
