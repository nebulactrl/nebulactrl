# Tests

Three tiers, from cheap to thorough:

| Tier | Command | What | When |
|---|---|---|---|
| 1 | `make test` | Go unit and database tests (`go test ./...`), web unit tests. Static validation of every catalog template. No Docker. | every change; the `ci` workflow, run by hand |
| 2 | `make test-smoke` | 8 core Docker scenarios on one shared control plane and agent | when you touch the agent, deploy engine, reconciler, proxy or network code; the `ci` workflow, run by hand |
| 3 | `make test-full` | everything: templates sample, Railpack, backups, reconnect/partition, proxy routes, previews, Gitea, terminal, tasks, replicas, ... | before larger changes in those areas; the `full` workflow, run by hand and as a release gate |

`go test -tags integration ./tests/integration/...` directly runs the full tier
(`NEBULACTRL_TEST_TIER` unset means `full`), but use `tests/run.sh`, which also
takes care of the dev agent (see below).

```sh
make test-smoke                                          # tier 2
make test-full                                           # tier 3
./tests/run.sh smoke -run 'TestRollback|TestIDOR' -v     # a subset (extra args go to go test)
./tests/run.sh full -run TestTasks -v
NEBULACTRL_TEST_MULTI=1 make test-full                   # + the multi-server test
NEBULACTRL_TEST_TEMPLATES=all make test-full             # deploy-test the whole template catalog
```

## Tier 2: the smoke set

| Test | Asserts |
|---|---|
| `TestEnroll` | the agent's server is online with facts; token reuse → 401; a revoked server's agent exits `ErrRevoked` and a new deploy is rejected with "server revoked" |
| `TestImageDeploy` | `succeeded`, labeled container, observed running + healthy |
| `TestZeroDowntimeRolling` | 50 req/s through Traefik during v1→v2: 0 non-2xx, 0 connection errors, v1→v2 switch |
| `TestFailedHealthKeepsOldServing` | `failed` "Previous release is still serving", v1 untouched |
| `TestRollback` | rollback to v1's release → `succeeded`, `/` is v1 |
| `TestSecretsNeverLeak` | `sekret-canary-12345` absent from deployment logs, `agent_commands`, every table, REST and MCP |
| `TestIDOR` | project-A token gets 404 on B's service/deployment/logs (REST + MCP) |
| `TestNetworkIsolation` | a service sees its environment's siblings by slug (and its own environment's `web`, not another's) but can reach another environment's services neither by name nor by IP; a service that blocks outbound internet cannot reach a listener outside Docker that a normal service reaches, and its siblings still reach it |

Every test starts with `requireTier(t, tierSmoke)` or `requireTier(t, tierFull)`
(`grep requireTier tests/integration` is the tier list). A full-tier test is
skipped when `NEBULACTRL_TEST_TIER=smoke`.

## Tier 3: everything else

| Test | Asserts |
|---|---|
| `TestBadImage` | `failed`, pull error in persisted logs |
| `TestVerifyFailureRollsBack` | probe fault injected → `rolled_back`, v1 serving, new container removed |
| `TestGitDeploy` | release pins commit SHA + image ID, build logs persisted |
| `TestReconcileRestoresMissingContainer` | `docker rm -f` → `reconcile` deployment restores it |
| `TestTrafficAnalytics` | 50 requests → analytics within 90s, no raw IP/User-Agent stored |
| `TestReplicasLoadBalance`, `TestScaleService` | three replicas behind one route, rolling and live scaling under load |
| `TestProcessesDeployRollRollback` | one service, three processes from one image (a web process, a two-replica worker without a port, a singleton clock): containers carry `nebulactrl.process` and the per-process names, pre- and post-deploy run once; per-process logs (filter and `<process> \| ` prefix) and scaling, a singleton refuses replicas (`replicas_require_stateless`); a changed singleton never runs two instances while web is served without a failed request; a failing web process fails the deployment before any other process starts, a failing worker after web switched rolls everything back; rollback reverts every process in one deployment; terminal targets a process; an agent without the capability gets `agent_too_old` and nothing is persisted |
| `TestMaintenanceAndErrorPages` | maintenance mode and the automatic 404/502/503 pages |
| `TestUptimeDetectsOutageAndRecovery`, `TestUptimeMaintenanceSuppressesAlerts` | the agent probes every 2 s (`NEBULACTRL_UPTIME_INTERVAL`, harness default): a service with an http health check is `up` with checks counted; `docker pause` freezes it (still "running", every probe times out) → state `down`, one open `uptime_incidents` row, one signed `uptime.down` webhook (a local receiver, a project-scoped channel); unpaused → `up`, the outage resolved, one `uptime.up`, nothing repeated, 24 h availability strictly between 0 and 100 %. With an active maintenance window and a failure threshold of 1 the failing checks give state `maintenance`, no incident, no alert, and are counted as maintenance checks that availability leaves out |
| `TestStatusPagePublicHostServesOnlyThePage` | exclusive: the control-plane server flag and `statusroute` point the proxy at a status listener on a host port (`host.docker.internal`, like the external-route tests); a published page answers on its own hostname through Traefik (HTML with title and display name, `status.json` without UUIDs or the service name, an SVG badge) while `/api/v1/*`, `/login`, `/mcp` and `/agent/v1/connect` are 404 there; unpublishing withdraws the route; a service domain cannot take the hostname (409 `domain_conflict`) |
| `TestTasks` | one-off, cron and pre-deploy commands |
| `TestLogHistoryAndDrain` | the agent stores runtime logs without a viewer; history API (time range, query language, paging, redaction); an HTTP JSON drain delivers the lines (retrying 503s, header credential, masked secrets, no backfill), health and test-send |
| `TestDockerCleanupKeepsRollbackImages` | Docker cleanup (exclusive, agent clock +100 h and its view of the daemon scoped to the test's own images): a dangling and an old unused image are removed, the current and the rollback image stay and the rollback finds its image locally, a volume is never touched, the run report counts and the volume action needs the typed name |
| `TestTerminalAndFiles` | web terminal and file browser, audit, session limits |
| `TestHostShell` | the opt-in host shell (exclusive): off by default (403 `host_shell_disabled`), 403 `two_factor_required` and no token access, then `uname -n` in a real shell on the host, 2 sessions per person, audit without I/O |
| `TestManagedDatabasesConnectAndRotate` | managed Postgres/Valkey, reveal, credential rotation |
| `TestDatabaseBackupsPostgres` | backups to a local directory and S3 (SeaweedFS), restore, schedule |
| `TestVolumeBackupFreezeRestoreAndDownload` | freeze-mode volume backup (restic) to a local directory, file browser + single-file download, restore into a new volume with a staged swap, deploy brings the old content back, no helper/staging left (exclusive) |
| `TestOpenBaoSecretReachesContainerAndIsNeverStored` | a real OpenBao (dev mode, pinned image, removed with its image afterwards): a `${{vault:…}}` variable and a URL-encoded derivative reach the container env, the app's echo shows the mask, and no table, decrypted release, command payload, API, MCP output or log holds the value; a new value in OpenBao reaches the next deploy; a deleted secret fails the deploy ("Not delivered", naming the reference) and leaves the running container alone |
| `TestComposeImportDeploysAndConnects` | a Compose project is planned and imported (references rewritten, managed Valkey with its password, a plain Valkey), the staged result deploys and the apps reach their databases through the rewritten references; the import is idempotent |
| `TestFilesExecHealthAndOneShot` | template prerequisites on the agent |
| `TestTemplatesDeploySample` | deploys a rotating sample of catalog templates (below) |
| `TestRailpackBuilds` | git repos without a Dockerfile build with Railpack |
| `TestProxyExternalHTTPRoute`, `TestProxyTCPRoute` | routes to external upstreams, TCP entrypoints (recreates Traefik) |
| `TestPublicDatabasePortAndDockerOptions` | a managed Valkey's public port: an allow-list that excludes the client is closed by the proxy, one that includes it connects, a deny list beats a confirmed `0.0.0.0/0`, the audited external connection string works, closing frees the port; a service with `shmSize`, `capAdd NET_BIND_SERVICE`, init, stop signal, ulimit, tmpfs and a label shows them in `docker inspect`, and forbidden options are refused |
| `TestGiteaPushDeploysAndReportsStatus`, `TestPreviewLifecycleWithFreshDatabase` | git provider webhooks, PR previews |
| `TestReconnect…` | partition after ack → result replayed; agent killed after ack → deployment settles consistently |
| `TestMultiServer` | needs `NEBULACTRL_TEST_MULTI=1`; runs on host2's daemon only |
| `TestReplicaSameServer…` | Postgres read replicas on one server, no mesh (`pgreplicas_test.go`): create/clone/stream, lag, writer/reader exports through a consumer, restart keeps streaming, an emptied volume exits 3 (no initdb), rebuild, delete/upgrade/backup guards, manual switchover Promote (old primary fenced, consumers and backup schedule follow, rebuild as replica), remove drops slot/role/hba line |
| `TestMeshCrossServer` | needs `NEBULACTRL_TEST_MULTI=1` **and** `NEBULACTRL_TEST_MESH=1` (`mesh_test.go`, `meshlinks_test.go`, `meshfixture_test.go`): a real Headscale container, agent1 and host2, mesh nodes and managed ACL, cross-server service links, a replica on the other server, grant removal, switchover and forced Promote across servers, server revoke. See "The mesh tests" below |
| `TestPGUpgradeConvertsVolume` | needs `NEBULACTRL_TEST_PGUPGRADE=1` (pulls ~550 MB) |

### Templates

Tier 1 validates **every** template in `templates/catalog` statically
(`internal/templates`, `TestCatalogStatic`): schema, a pinned `tag@digest`, a
licence, health checks, ports, volumes and references. It pulls no images and
runs in a second.

`TestTemplatesDeploySample` (tier 3 only) deploys templates for real, probes
their generated domain through Traefik, and removes stack, volumes and images
again after each one. `NEBULACTRL_TEST_TEMPLATES` selects them:

| Value | Templates |
|---|---|
| unset or `sample` | the rotation of the day **plus at most 8 changed templates**, never a heavy one |
| `all` | the whole catalog, heavy templates included |
| `id1,id2` | exactly these, heavy ones included |

- **Rotation**: the catalog ids, sorted; a window of 3 starting at index
  `(days since 1970-01-01 UTC × 3) mod N`, wrapping around. The same templates run
  on the same day everywhere, and because the window advances by 3 a day, every
  template is covered within ⌈N/3⌉ days (about 100 days for the 312 non-heavy
  templates).
- **Changed**: templates whose `templates/catalog/<id>.yaml` differs from
  `origin/main` (`git diff origin/main...HEAD`, plus uncommitted and untracked
  files); `HEAD~1` if there is no `origin/main`; none if git is not available.
  Deleted templates are ignored. A run deploys **at most 8** of them (a window
  of 8 over the sorted ids, advancing by 8 per day, so a large change set is
  covered over several days). The rest are logged as skipped and still
  validated statically.
- **Heavy** templates (`heavy: true`: multi-GB images, 4 GB of RAM or more, slow
  first start) are never sampled; they need a manual check, or an explicit
  `NEBULACTRL_TEST_TEMPLATES=id`.
- **Inputs**: required inputs are filled from their `format` (`email`, `url`,
  `hostname`, `number`, text, a select's first option), see
  `internal/templates/testsample`.

The `full` workflow has a `templates` input (`sample` or `all`) for manual runs.

## How the Docker tiers work

`tests/integration` (build tag `integration`) runs the real control plane
(`internal/app.Build`, migrated schema, real HTTP listener, River) and the real
agent wiring of `cmd/agent` — in the same process — against the local Docker
daemon, a managed Traefik and Postgres. Nothing is mocked except where a test
injects a fault on purpose (see `ProbeFault` below).

`TestMain` does the slow work once, in parallel:

- pre-pulls the images of the selected tier (those not on the host yet),
- builds the fixture app image and a throwaway registry (and, in the full tier, the git fixture),
- migrates one schema and starts **one control plane**.

The **agent with Traefik** starts on first use (it needs the exclusive tests to be
finished, see below) and is shared by all parallel tests.

### Shared and exclusive tests

- **Parallel tests** call `t.Parallel()` and take `SharedStack(t)`. Each gets its
  own project and environment (so its own Docker network) and its own hostnames:
  `s.Host()` is `<test>-<rand>.localhost` and `s.Host("api")` is
  `api.<test>-<rand>.localhost`. Slugs, domains and networks never collide. At
  cleanup the test deletes its services through the API, so the control plane
  stops supervising them and the agent removes containers and volumes.
  Assertions about rows must be scoped to the test's own project: the schema is shared.
- **Exclusive tests** never call `t.Parallel()` and take `ExclusiveStack(t)`, a
  private control plane (fresh schema) and agent. They are the ones that kill or
  restart the agent (reconnect, partition), recreate Traefik (TCP routes), change
  server-wide settings (base domain: templates, previews), use a second server
  (multi-server) or need the control plane on all interfaces (Gitea), plus
  Railpack, which owns the shared BuildKit builder. `go test` runs non-parallel
  tests first, one at a time, and releases the parallel group afterwards, so
  exclusive tests never overlap with the shared agent.

### Speed

- Test services use a 1 s health interval and a single success (`SvcSpec` defaults in
  `stack_test.go`; a test config, not a product default).
- The fixture app (`fixtures/app`, `FROM scratch`) is used wherever the app
  itself is not under test.
- Fixed sleeps are replaced by polling (`eventually`, `Load.WaitMore`).
- Product timers a test would otherwise sit out are shortened through the control
  plane's own settings, not by sleeping less: the harness starts every control plane with
  `NEBULACTRL_MESH_LINK_GRACE=30s` (production: 10 m), `NEBULACTRL_REPLICA_BROKEN_AFTER=60s`
  (production: 2 m) and `NEBULACTRL_UPTIME_INTERVAL=2s` (production: 30 s; the harness agent probes at the same period); see `harness_timers_test.go` and
  [advanced settings](../docs/operations/configuration.md). The same variable in the
  environment of `tests/run.sh` overrides the short value.

### Sharding

`tests/run.sh full --shard I/N` runs the I-th of N slices of the top-level tests; without
`--shard` everything runs. CI uses `--shard 1/4` … `4/4`, one runner each
(`full.yml`). The selection (`tools/shardtests`) is deterministic: the test list comes from
`go test -tags integration -list '.*'` (no database or Docker needed), the weights from
[`tests/integration/durations.json`](integration/durations.json) (approximate seconds per
test from a `-v` run; tests missing from it get `defaultSeconds`), and tests are assigned
heaviest first to the least loaded shard. Parallel tests share one control plane and run
concurrently, so they count `parallelFactor` of their duration. `go test ./tools/shardtests`
(tier 1) proves that the four shards cover every test exactly once and fails when
`durations.json` names a test that no longer exists. After adding or slowing down a test,
refresh the file from a local `./tests/run.sh full -v` run (`go run ./tools/shardtests -h`).

### macOS: how many networks can be alive

On OrbStack and Docker Desktop for Mac every Docker network is a bridge on the
Mac, and macOS has only a few: once they are used up, new networks have no route
from the host and the in-process agent's health checks time out. Parallel tests
therefore reserve network slots (`SharedStackN(t, n)`: one per environment plus one
for an environment with a service that blocks egress) and wait for a free
one. The limit is `NEBULACTRL_TEST_MAX_NETWORKS` (default 3 on macOS, unbounded
elsewhere; `0` = unbounded). Stray networks (yours, or the dev stack's) reduce
what is left: if deploys fail with health-check timeouts to `10.213.x.x`,
remove what you do not need or lower the limit.

## Requirements

- Docker (tested on OrbStack, Docker 29). Loopback registries count as insecure, so no daemon setup is needed.
- Postgres reachable through `NEBULACTRL_TEST_DATABASE_URL` (`make test-db` creates `nebulactrl_test` inside the dev Postgres; `make test-smoke` and `make test-full` do it). The harness creates fresh migrated schemas and drops them afterwards.
- `go`, `git`, `docker` on `PATH`. Network access for images that are not on the host yet.

## It pauses the dev agent1

The agent manages a container named **`nebulactrl-traefik`** on the network
**`nebulactrl-edge`**; both names are fixed. The dev stack's `agent1`
(`make dev-agent`) uses the same names on the same Docker daemon, so two agents
would fight over one Traefik. `tests/run.sh` therefore

1. takes a lock (two runs share one Docker daemon, so a second one waits),
2. stops `agent1` and removes its `nebulactrl-traefik` (the dev services keep
   running; only the proxy is down meanwhile),
3. runs the tests with `NEBULACTRL_TEST_PROXY_TAKEOVER=1`,
4. restarts `agent1` afterwards (its proxy manager recreates Traefik with the dev ports).

Running `go test -tags integration ./tests/...` directly **skips everything** with
an explanation while a `nebulactrl-traefik` is running, so a stray run can never
disturb a dev agent. The harness removes its own `nebulactrl-traefik`, the
networks of the environments it created and the fixture containers when the run
ends. The harness agent sets a 24h orphan-sweep age, so it never removes dev
containers it does not own. Images the harness pre-pulled stay (they make the next
run fast); `docker image prune -a` removes them.

The dev control plane (`:18090`) and its database are never touched.

## Ports

Traefik is published on `18880` (http), `18443` (https) and admin `127.0.0.1:19000`;
a port that is taken (for example 18443) is replaced by a free one. Override with
`NEBULACTRL_TEST_HTTP_PORT`, `NEBULACTRL_TEST_HTTPS_PORT`, `NEBULACTRL_TEST_ADMIN_PORT`.
The fixture registry and git server bind free loopback ports.
The harness sets `NEBULACTRL_GIT_ALLOW_PRIVATE=127.0.0.1` for its in-process control planes (`main_test.go`): every git fixture (git://, ssh, Gitea) listens on loopback, which the control plane's SSRF guard refuses otherwise. The agent has no such guard.

## Fixtures

- `fixtures/app`: HTTP server on `:8080`. `/` returns `$VERSION`, `/health` answers 500 when `FAIL_HEALTH=1`,
  the process exits 1 at start when `CRASH=1`, `SECRET_ECHO=<NAME>` prints `NAME=<value>` at start (proves log redaction),
  `/log?msg=…` prints lines to stdout (log history and drain tests), `/fetch?url=` requests a URL from inside the container (network tests), `/db` and `/kv` talk to managed databases.
  The harness compiles it for the daemon's architecture and builds `nebulactrl-test-app:dev` (`FROM scratch`, no pulls).
  Because the engine always pulls image sources, the image is pushed to a throwaway `registry:2` on `127.0.0.1:<port>`.
- `fixtures/git-server`: alpine + `git daemon`, serving a one-commit bare repo (`app.git`: Dockerfile + the prebuilt
  binary) on `git://127.0.0.1:<port>/app.git`. The port is published on loopback because the in-process agent runs
  on the host and cannot resolve Docker DNS names.
- `fixtures/railpack`: two repositories without a Dockerfile (Node app, static site).

`TestVerifyFailureRollsBack` injects its failure through `ProbeFault`, a wrapper
around the agent's proxy manager that exists only in this package (`agent_test.go`);
faults are per hostname, so they do not touch other tests on the shared agent. The
production agent has no such flag. `TestMultiServer` starts the dev `multi` compose
profile (`host2` docker-in-docker + `agent2`) pointed at the test's control plane and
removes it again.

Debugging: `NEBULACTRL_TEST_CP_LOGS=1` and `NEBULACTRL_TEST_AGENT_LOGS=1` print control-plane and agent logs.

### The mesh tests

`TestMeshCrossServer` builds one world (control plane, agent1 on the local daemon, agent2 in
host2, a pinned Headscale from `deploy/dev/headscale/config.yaml` with `policy.mode: database`
and Tailscale's public DERP map, the mesh enabled on both servers) and runs the phases as subtests,
cheapest first. It pulls the Headscale and Tailscale images and starts docker-in-docker, so it
is opt-in:

```sh
NEBULACTRL_TEST_MULTI=1 NEBULACTRL_TEST_MESH=1 ./tests/run.sh full -run '^(TestMesh|TestReplica)' -timeout 90m
```

It never skips a part that cannot run: when link nodes cannot start (no `/dev/net/tun` in
host2, a Docker that refuses `NET_ADMIN`), it **fails** with the node's log. The login server
must be an IP every container and the test process can reach (`host.docker.internal` does not
resolve inside host2's containers): the fixture probes the Docker bridge gateway and this
machine's addresses, or take `NEBULACTRL_TEST_MESH_HOST_IP`. The phase that removes a variable
reference waits for the reference grace of `meshlinks` (ten minutes in production); the harness
sets `NEBULACTRL_MESH_LINK_GRACE=30s`, and a replica that cannot reconnect turns `broken` after
60 s instead of 2 minutes (`NEBULACTRL_REPLICA_BROKEN_AFTER`). Set either variable in the
environment of `tests/run.sh` to run with the production timing.

On macOS the world needs several Docker networks at once (two environments, each with an
internal network for its databases, plus the edge network); OrbStack routes only eight bridges to
the host (see "macOS: how many networks can be alive"). With the dev stack's own environment
network and other projects' networks alive, the database phases can fail with
`dial tcp 10.213.x.x:5432: i/o timeout` health checks: remove the networks you do not need
first. The exclusive mesh world does not reserve network slots.

Not verifiable on one machine: Tailscale SaaS (OAuth, ACL API, tag ownership, device quota),
NAT traversal across real networks (the two "servers" share one Docker VM and host2 sits behind
its own NAT, so no direct path forms and the tunnel always runs over a Tailscale public DERP
relay: this needs outbound internet and is not hermetic; direct paths are never exercised),
WAN latency/MTU, hosts with ufw/firewalld, Headscale behind TLS, the clone
time of a large database, split-brain on a real power loss.

## CI

`ci.yml` and `docs.yml` run on every pull request to `main` and every push to `main`;
`full.yml` runs nightly on `main`; all three also run on demand (Actions → workflow →
Run workflow) and as gates of `release.yml`, which is on demand only. The rules, the
required checks and the fork pull request approval are in
[`CONTRIBUTING.md`](../CONTRIBUTING.md#ci), and `make lint-workflows` enforces the
workflow rules. Tests run with `-count=1`.

- `.github/workflows/ci.yml`: `go vet` + `go test -count=1 ./...` as 3 parallel shards of
  packages (`go vet` in shard 1; the split is `tools/shardtests -packages` over
  `scripts/ci/go-durations.json`), each with a Postgres service container, web
  typecheck/biome/tests/build with bun, and tier 2 on the runner's Docker (the `smoke`
  input turns it off on demand; pushes and pull requests always run it, and the release
  passes `false` because tier 3 contains it).
- `.github/workflows/full.yml`: tier 3 (the `templates` input as above) as 4 parallel shards
  (`tests/run.sh full --shard I/4`, see "Sharding"), each on its own runner.
- `.github/workflows/docs.yml`: the docs and site builds.
- `.github/workflows/security.yml`, `.github/workflows/scorecard.yml`: nightly vulnerability
  scans (govulncheck, osv-scanner, the published image) and OpenSSF Scorecard; neither runs tests.
- `.github/workflows/release.yml`: runs the three above as gates, then releases (see
  [`CONTRIBUTING.md`](../CONTRIBUTING.md#releasing)).
