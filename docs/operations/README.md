---
title: Operations index
description: "Every operations page, with when to read it."
section: Reference
order: 5
---

# Operating NebulaCtrl

## Start here

| Page | Read it when |
|---|---|
| [Backup and restore](backup-restore.md) | **Before anything else.** The scheduled control-plane backup (daily 03:30 UTC, 14 kept, S3, the built-in storage or a local directory; the last two are not off-site), `NEBULACTRL_SECRET_KEY` (never in a backup), `nebulactrl-server restore --from <dest> --at <timestamp>`, restoring from the built-in storage, what is *not* backed up |

## Applications and deployments

| Page | Read it when |
|---|---|
| [Deployments](deployments.md) | rolling vs recreate, health check semantics, rollback, image retention, `rollback_failed` recovery |
| [Builds: Dockerfile, Railpack, starters](builds.md) | a repository without a Dockerfile, the build-plan preview, changing a service's source between an image and Git, pinned Railpack and how to upgrade it, buildx fallback, disk use |
| [SSH deploy keys](ssh-deploy-keys.md) | cloning over `ssh://` or `git@host:org/repo.git`, generated or uploaded keys, rotation, known_hosts pinning (trust on first use), "Accept new host key", limits |
| [Import a Docker Compose project](compose-import.md) | turning a `docker-compose.yml` into staged services: the review, what maps and what does not, rewritten references, managed-database opt-in, secrets, blockers, the CLI |
| [Import from Coolify](import-coolify.md) | migrating a Coolify v4 instance: the token it needs (`read` + `read:sensitive`), what maps (applications, Dockerfile/Nixpacks builds, domains, health checks, custom Docker run options, managed databases with the same engine and version, variables and rewritten references), what is never copied (data) and stays manual, the CLI `nebulactrl import coolify` |
| [Compose stacks](compose-stacks.md) | running a `docker-compose.yml` as one service with `docker compose`: the generated override, routing through domains, deploy and rollback, the allow-list (no privileged mode, host network, devices, Docker socket or host bind mounts), the per-server Unrestricted switch, limits |
| [Docker options](docker-options.md) | letting a service add a capability, a ulimit, a bigger `/dev/shm`, extra hosts, labels, an init process, a stop signal or tmpfs mounts; what is never allowed; the per-server Unrestricted switch for `NET_ADMIN` / `SYS_ADMIN`, devices and GPUs |
| [Processes](processes.md) | one service running a web server, queue workers and a scheduler from one build and release |
| [Replicas and autoscaling](scaling.md) | running a stateless service on several replicas, CPU autoscaling rules, capacity guard |
| [Pull-request previews](previews.md) | a preview environment per open PR: what is cloned, fresh databases and seed commands, domains, the 5-running / 7-idle-day limits, pausing and resuming, fork PRs and why they get no secrets |
| [Clone and move](clone-and-move.md) | a staging copy of production: cloning a service or a whole environment (secrets copied sealed, generated values regenerated, new empty volumes and databases, domains dropped or suffixed), moving a service to another environment with its references and links checked, the dry run, the CLI, what is not copied |
| [Web terminal and file browser](terminal.md) | opening a shell or browsing files in a replica container, roles and the production confirmation, what is audited (never the I/O), limits, the CLI `services shell`, proxy settings for WebSockets |
| [Maintenance mode and error pages](maintenance-and-error-pages.md) | putting a service into maintenance (UI, CLI, API), what visitors see (503 + Retry-After), the automatic 404/502/503 pages, the `nebulactrl-pages` container and its pinned nginx image |
| [Deploy hooks](deploy-hooks.md) | a URL per service that deploys it from CI or `curl` (`POST /hooks/deploy/<id>/<secret>`): the show-once secret and rotation, `?tag=` / `?digest=` as a one-off release, Docker Hub and GHCR push payloads, queueing behind a running deployment, the 6/min rate limit, audit, curl / GitHub Actions / GitLab CI snippets |
| [Projects](projects.md) | deleting a project: empty, or with everything in it (`cascade` and `confirm`), what goes and what stays, the deletion plan, the production approval, the audit entry |

## Domains, networking and certificates

| Page | Read it when |
|---|---|
| [Networking](networking.md) | what can reach what on a server, team shared networks (same server, hostnames `<service>.<env>.<project>`), **Block outbound internet** (default on for databases), deploying the siblings, what is and is not isolated |
| [Proxy routes: route services, domain options, TCP/UDP](proxy-routes.md) | publishing a LAN/host/tailnet upstream, exposing a TCP/UDP port, redirects, path prefixes, basic auth and other per-domain options, the proxy restart on a new port, why ufw does not protect Docker ports |
| [Custom certificates, upstream policy, tailnet](proxy-tailnet-certificates.md) | uploading a certificate, the SSRF upstream policy, routing to tailnet devices (managed or host Tailscale), the Tailscale OAuth client and ACL setup |
| [Per-service tailnet (Tailscale / Headscale)](service-tailnet.md) | making one service reachable on your tailnet (HTTPS, raw TCP, optional Funnel), "Tailnet only", the sidecar and how its key is minted, re-provisioning, device removal, Headscale limits |
| [Server mesh and cross-server links](mesh.md) | joining servers to your tailnet (server and link nodes), letting services of one environment on different servers reach each other, the generated ACL (managed on Headscale, advisory or opt-in managed on Tailscale), the device count, public DERP relays, the paused migration window, failure modes, the connectivity check |
| [DNS-01 and wildcard certificates](dns01-wildcard-certificates.md) | Cloudflare, Route 53, Hetzner and DigitalOcean resolvers, credentials per team or server, wildcard domains, generated-domain coverage, certificate status and expiry |
| [Cloudflare: DNS records, proxy toggle, live DNS status](cloudflare.md) | connecting a scoped API token, the records NebulaCtrl always manages for hostnames in a connected zone and how it recognises them (ownership marker, adopting a matching record, replacing a conflicting one), proxied by default for new names, staying in sync on IP change / move / drain, the SSL-mode redirect-loop warning, the per-domain status, automatic DNS-01 certificates, the origin lock, tunnels |

## Databases and backups

| Page | Read it when |
|---|---|
| [Managed databases](databases.md) | engines and versions, `DATABASE_URL` exports, the audited reveal action, credential rotation, version upgrades (pg_upgrade) |
| [Postgres standby replicas and manual Promote](database-replicas.md) | advanced, Postgres only: when to use a standby (another server, read offload) and when not, adding a replica on another or the same server, `DATABASE_WRITE_*` / `DATABASE_READ_*`, lag, Promote (switchover or forced, data loss, fencing, consumers rewritten), rebuild, backups, restore and upgrades, limits |
| [Database backups](database-backups.md) | scheduled and on-demand dumps streamed to the built-in backup storage, S3-compatible storage or a server directory, the built-in storage's quota and why it is not off-site, retention, restore into a new database or in place |
| [Volume backups](backups.md) | file-level backups of a service's volumes: pre-hook, freeze (`docker pause`), staged copy, background restic upload, stop and live modes, retention, restore into a new volume with a staged swap, file browser and single-file download |

## Servers

| Page | Read it when |
|---|---|
| [Servers and agents](servers.md) | offline servers, revoke and re-enroll, in-flight deployments, reconciler |
| [Build servers and registries](build-servers.md) | building on a dedicated server instead of the target: roles (Deployments, Builds, Both), where builds run (team, project, service), the built-in registry every team has (private through the agent link, no credentials), bring-your-own registries (GHCR, Docker Hub, GitLab, Harbor), cross-architecture builds and emulation, per-project builders and preview isolation, fallback to the target, rollback and what to keep in a registry, the failure table, limits |
| [Draining servers and the fleet overview](draining-servers.md) | taking a server out for maintenance or retirement, capacity and overcommit, drain failure and cancel semantics, required scopes |
| [Docker cleanup](docker-cleanup.md) | the daily and disk-alert cleanup of images, build cache and networks per server, what is never removed (volumes, rollback images, infrastructure images), the optional stopped-container step, run history and the explicit unused-volume action |
| [Upgrades: agents and the control plane](upgrades.md) | agents replacing their own binary (checksum, atomic swap, rollback when the new one does not start), automatic and staged upgrades, version skew per server, the control-plane backup taken before an upgrade and when it blocks one, `--force-upgrade` |
| [Updating NebulaCtrl](updating.md) | the update notice and the daily check (what it sends: an anonymous `GET` of the feed, nothing else), the instance admin's one click update (installer run by the host's agent as a systemd unit, backup first, about a minute of dashboard downtime, rollback to the previous image, outcome shown for an hour), when one click is unavailable, the manual command and what `--version` pins, `NEBULACTRL_UPDATE_CHECK`, `NEBULACTRL_UPDATE_FEED_URL` and mirrors |
| [Server patching](server-patching.md) | pending OS updates and reboot-required per server (apt and dnf, checked every 6 hours), "N updates (M security)" on the server card, the instance admin's Apply updates (plan, typed confirmation, production acknowledgement, log) and Reboot (waits for the agent to reconnect), the `server.reboot_required` notification, limits |
| [Host shell](../security/host-shell.md) | the opt-in root shell on a server: who may use it (Owners and Admins with two-factor authentication), the production confirm, what is audited (never the I/O), how the agent runs it |

## Observability

| Page | Read it when |
|---|---|
| [Monitoring, logs and retention](monitoring.md) | health endpoints, JSON logs, `correlation_id`, metrics and traffic analytics retention, disk alerts |
| [Log history and log drains](logs.md) | runtime logs stored on each server (7 days, size caps), History view and query language, drains to Loki, OTLP, syslog and HTTP JSON (Datadog, Better Stack, Axiom), retries, backlog bound, health, and the optional built-in central logs (off by default, 7 days on the control-plane host, the Central logs tab and API) |
| [Traffic analytics and request origins](traffic-analytics.md) | what the proxy access log is turned into, the "Where requests come from" map, DB-IP Lite vs MaxMind GeoLite2, country vs city (~8 MB vs ~130 MB), attribution, privacy |
| [Notifications](notifications.md) | email, Slack, Discord, Telegram and signed-webhook channels, event rules, retries and the delivery log, verifying `X-NebulaCtrl-Signature` |
| [Service alerts](service-alerts.md) | crash, unhealthy, restart-loop and missed-backup alerts: once per incident, recovery messages, flapping, thresholds, what is not watched |
| [Uptime monitoring](uptime.md) | probes that run from each server every 30 seconds, the six states, availability and its math, failure and recovery thresholds, maintenance, `uptime.down` and `uptime.up` alerts, retention, the uptime API |
| [Status pages](status-pages.md) | public status pages on your own domain: components and display names, hostname and DNS check, publishing, incidents and maintenance windows, the overall-state rules, what is public and what is never shown |

## Secrets and built-in services

| Page | Read it when |
|---|---|
| [Secret managers and team variables](secret-managers.md) | `${{vault:<name>/<path>#<key>}}` references to HashiCorp Vault / OpenBao (KV v2, token or AppRole) and Infisical (machine identity), fetched at deploy time and never stored, the policy to create, errors that name the reference, redaction, the team variable layer `${{team.KEY}}` |
| [Built-in services](platform-services.md) | the S3 backup storage, image registry and central logs that run beside the control plane for every team (one of each, a slice per team, private through the agent link, quotas, retention, Admin → Built-in services, deleting a team's slice, licences), and the per-team Headscale ("Private network (Headscale)", Set up for me, internal-only access, removal) |

## When something is wrong

| Page | Read it when |
|---|---|
| [Troubleshooting](troubleshooting.md) | proxy/port 80, certificates, DNS, agent will not connect, deployment stuck |
| [Known limitations](limitations.md) | what this release does **not** do |
| [Advanced settings](configuration.md) | you were told to change a control-plane timer (`NEBULACTRL_MESH_LINK_GRACE`, `NEBULACTRL_REPLICA_BROKEN_AFTER`); the defaults and what shortening them risks |

Installation and upgrade live in [`docs/installing`](../installing/README.md); the security model in [`docs/security`](../security/README.md); design decisions in [`docs/decisions`](../decisions/).
