---
title: The control plane
description: "How the control-plane containers are laid out, hardened and run by hand."
section: Installing
order: 40
---

# The control plane

The control plane is one container (`nebulactrl-server`, with the web UI embedded) and Postgres 17, started by Docker Compose from [`deploy/install/docker-compose.yml`](../../deploy/install/docker-compose.yml). **The [installer](installer.md) sets this up; you do not write the files yourself.** This page explains what it made and how to run the same thing by hand.

## Layout

```text
/opt/nebulactrl/
  docker-compose.yml     written by the installer; edit the repository's copy, not this one
  .env                   every secret and setting, mode 0600
  install.conf           the installer's remembered options
  uninstall.sh
  state/                 markers (which steps ran)
/var/backups/nebulactrl/control-plane/   local control-plane backups
```

```sh
cd /opt/nebulactrl
docker compose ps
docker compose logs -f nebulactrl         # JSON logs; migrations are applied on startup
curl -fsS http://127.0.0.1:8080/readyz    # domain mode; http://127.0.0.1:8000 without a domain
```

`.env` holds:

| Variable | Meaning |
|---|---|
| `POSTGRES_PASSWORD` | Database password (hex, because it is embedded in the connection URL) |
| `NEBULACTRL_SECRET_KEY` | Base64 of 32 random bytes. Encrypts every sealed value: variables, git tokens, registry credentials, SSH keys, integration tokens, TOTP seeds, backup passwords (AES-256-GCM). **Exists nowhere else: back it up apart from the database backups.** Rotate it with `nebulactrl-server rekey`, never by replacing it ([secrets at rest](../security/secrets-at-rest.md)). Alternatives: `NEBULACTRL_SECRET_KEY_FILE` (path to a file holding the key, keeps it out of `docker inspect`) and `NEBULACTRL_SECRET_KEY_PREVIOUS[_FILE]` (earlier keys, during a rotation) |
| `NEBULACTRL_PUBLIC_URL` | The URL users and agents use. An `https://` value turns on the `Secure` session-cookie attribute and is baked into agent install commands |
| `NEBULACTRL_BIND`, `NEBULACTRL_PORT` | Host interface and port the container publishes (`127.0.0.1:8080` with a domain, `0.0.0.0:8000` without) |
| `NEBULACTRL_SETUP_TOKEN` | Required by the setup page until the admin exists. The installer sets it. **If you leave it empty** (a hand-made compose install), the server generates a one-time token at start while no user exists and writes it to its log (`docker compose logs nebulactrl`, the line "first-run setup", with a ready `setup_url`); a restart before setup is complete logs a new one. Nobody who can only reach the dashboard can claim the admin account |
| `NEBULACTRL_DASHBOARD_UPSTREAM` | `nebulactrl:8080` in domain mode: the dashboard's system route target |
| `NEBULACTRL_STATUS_UPSTREAM` | `nebulactrl:8081` (default; `host:port`, a leading `http://` is dropped): where the control-plane host's proxy sends the hostnames of published [status pages](../operations/status-pages.md#how-public-pages-are-served), the status listener, never the dashboard. The installer writes it in domain mode |
| `NEBULACTRL_DASHBOARD_ALLOW_CIDRS` | Comma separated CIDRs or addresses (IPv4, IPv6, at most 200) that may reach the dashboard's hostname. A malformed value stops the start. Seeds the setting; see [Dashboard access](#dashboard-access) |
| `NEBULACTRL_DASHBOARD_DNS_RESOLVER`, `NEBULACTRL_DASHBOARD_DNS_TOKEN` (or `_FILE`) | DNS-01 provider (`cloudflare`) and its API token for the dashboard's certificate. Both or neither. The token is sealed into the DNS credential store, never logged |
| `NEBULACTRL_LOCAL_ENROLL_TOKEN`, `NEBULACTRL_LOCAL_SERVER_NAME`, `NEBULACTRL_LOCAL_SERVER_IP`, `NEBULACTRL_ACME_EMAIL` | Enrollment of the host's own agent as the first server |
| `NEBULACTRL_BACKUP_HOST_DIR` | Host directory behind the local backup destination |
| `NEBULACTRL_MCP_ENABLED`, `NEBULACTRL_LOG_LEVEL` | Optional: `NEBULACTRL_MCP_ENABLED` decides `/mcp` and locks the switch in Admin → MCP (unset: off until the instance admin turns it on there), `debug`/`info`/`warn`/`error` |
| `NEBULACTRL_UPDATE_CHECK`, `NEBULACTRL_UPDATE_FEED_URL` | Optional: `false` turns off the daily check for a newer release and the update notice (default `true`); the feed URL to read instead of `https://nebulactrl.com/releases/latest.json`, for a mirror. A value that is not `true`/`false`, or a feed URL that is not `https`, stops the start (a plain `http` feed is refused: the update it announces runs as root). See [updating NebulaCtrl](../operations/updating.md#settings) |
| `NEBULACTRL_GIT_ALLOW_PRIVATE` | Optional: git remotes and git provider base URLs on a private network. The control plane refuses loopback, private (10/8, 172.16/12, 192.168/16), carrier-grade NAT (100.64/10) and link-local or cloud-metadata addresses when it reads a git repository or calls a git provider, also after a redirect. List what you host yourself, comma separated: host names (`git.corp.example`), IP addresses or CIDR ranges (`10.0.0.0/24`). Link-local and metadata addresses cannot be allowed |
| `PG_*` | Postgres tuning computed from the RAM |

The server also reads `NEBULACTRL_LISTEN`, `NEBULACTRL_STATUS_LISTEN` (default `:8081`: the listener that serves published status pages by `Host` and nothing else; keep it off the host's interfaces, the compose file never publishes it), `NEBULACTRL_AGENT_BINARIES_DIR` and `NEBULACTRL_BACKUP_DIR`; the compose file sets them. `NEBULACTRL_MAX_DUMP_UPLOAD_BYTES` (default 5 GiB) limits the dump files [restored into a database](../operations/database-backups.md#restore-from-a-dump-file). It reads `NEBULACTRL_FORCE_UPGRADE=true` (default off) to start after a version change although the backup it takes first failed ([upgrades](../operations/upgrades.md#backup-before-a-control-plane-upgrade)). A few advanced timers (`NEBULACTRL_MESH_LINK_GRACE`, `NEBULACTRL_REPLICA_BROKEN_AFTER`) are listed under [advanced settings](../operations/configuration.md); leave them unset.

> [!WARNING]
> **Back up `NEBULACTRL_SECRET_KEY` now.** `sudo grep NEBULACTRL_SECRET_KEY /opt/nebulactrl/.env`, into a password manager or vault, apart from the database dumps. If you lose it every stored secret is unrecoverable and every variable must be re-entered; a key that is not in the key ring stops the values sealed with it from decrypting. See [backup and restore](../operations/backup-restore.md) and [secrets at rest](../security/secrets-at-rest.md). Admin → Control-plane backup keeps a reminder card until an administrator confirms it.

## Hardening of the containers

- Postgres: internal network only (no route out, no published port), SCRAM-SHA-256, data checksums, capabilities dropped, `no-new-privileges`.
- Control plane: read-only root filesystem, all capabilities dropped, `no-new-privileges`, no Docker socket; its only writable path is the backup directory.
- There is no reverse proxy in the compose file: the host's agent's Traefik serves the dashboard ([single proxy on the control-plane host](installer.md#single-proxy-on-the-control-plane-host)).

## Dashboard access

In domain mode the dashboard is a system route on the control-plane host's proxy. Who may reach it and how its certificate is issued is an instance setting: **Admin → Dashboard access** (instance admin only), `GET`/`PUT /api/v1/instance/dashboard-access`, or `nebulactrl instance dashboard-access get|set`.

- **Allow list.** CIDRs or addresses, IPv4 and IPv6 (at most 200). It becomes an `ipAllowList` on the route and applies as soon as it is saved: no proxy restart. Anybody else gets `403` from the proxy. Agents on other servers and webhooks use the same hostname, so keep their addresses in the list. *Add my current IP* and the *Tailnet only* preset (`100.64.0.0/10`, `fd7a:115c:a1e0::/48`) fill it in.
- **Lock-out guard.** A list that leaves out the address you are connecting from is refused with `409 dashboard_lockout` unless you confirm (`confirmLockout`; `--confirm-lockout` in the CLI). The address is the one the proxy saw: the last `X-Forwarded-For` entry, trusted only from a private or loopback peer. A caller who reaches the control plane directly (an SSH tunnel to `127.0.0.1:8080`) never meets the list and is never warned; that is also the way back in after a lock-out.
- **Certificate.** Automatic HTTP-01, or DNS-01 with a provider that already has a resolver on the control-plane server (Servers → Proxy). `422 resolver_not_configured` otherwise. A resolver that is staged but not deployed yet sets `proxyDeployRequired`: the dashboard keeps its HTTP-01 certificate until **Deploy proxy** applies the resolver.
- **Where the values come from.** `NEBULACTRL_DASHBOARD_ALLOW_CIDRS` and `NEBULACTRL_DASHBOARD_DNS_RESOLVER`/`_TOKEN` only **seed** the setting. While nobody has edited it the installer's values are written on every start; after the first save the database row wins and the environment is ignored, so edits survive restarts and installer re-runs. When the environment names a DNS-01 provider and token, the control plane, once the host's agent is online, creates the DNS credential `dashboard-<provider>` (sealed, scoped to that server), sets the server's resolver, deploys the proxy and only then makes the route use it, retrying with backoff. It never changes an existing credential, so rotate the token in the DNS credential.
- **Audit.** `dashboard.access.update` (an admin's change: networks, provider, whether the lock-out was confirmed) and `dashboard.access.seed` (the installer's values). Tokens are never recorded.

## Running it by hand

If you cannot use the installer (another distribution, a managed Docker host):

1. Install Docker Engine 24+ and the Compose plugin 2.24+, and create the edge network once: `docker network create --label nebulactrl.managed=true nebulactrl-edge`.
2. Build or load the image (`docker build -t nebulactrl:local .`).
3. Copy [`deploy/install/docker-compose.yml`](../../deploy/install/docker-compose.yml) next to a `.env` made from [`deploy/install/env.example`](../../deploy/install/env.example) (mode 0600) and run `docker compose up -d`.
4. Put HTTPS in front of `127.0.0.1:8080` yourself (any reverse proxy; it must pass WebSocket upgrades on `/agent/v1/connect` and must not buffer `text/event-stream` responses on `/api/v1/events` and the log streams), or install an agent on the host as a server (below) and add the system route by setting `NEBULACTRL_DASHBOARD_UPSTREAM=nebulactrl:8080` and the `NEBULACTRL_LOCAL_*` variables.
5. Open `NEBULACTRL_PUBLIC_URL`, create the admin on the setup page, then [add servers](servers.md).

On start the server applies its database migrations (and the job queue's) and waits up to 30 seconds for Postgres.

## First run

While no user exists the **setup page** asks for an email, name, password, team name and (with the installer) the setup token, and creates the admin and the team. Afterwards setup is closed (`POST /api/v1/setup` returns 409). Then:

1. The first server is already there when you used the installer: the control-plane host, **online** within a minute, with **Accept workloads** on.
2. Create a project, open its canvas, add a service (placed on that server, or on one you [add](servers.md)).
3. **Admin → Control-plane backup** (instance admin only): add an off-site backup destination and confirm you stored the secret key. **Account settings → API tokens** for the CLI, CI or MCP.

The login endpoint is rate limited to 10 attempts per minute per visitor address and to 10 wrong passwords per 10 minutes per account; setup with the token is limited the same way. The address is the visitor's, not the proxy's ([client addresses](../security/login-methods.md#client-addresses-and-rate-limits)).

## Production notes

- Pin the image version you run; do not run `latest` of anything you cannot roll back ([upgrade](upgrade.md)).
- Keep the control plane's time in sync (NTP).
- Agents keep your apps running through any control-plane restart or upgrade: the control plane never stops remote containers while it is down.
