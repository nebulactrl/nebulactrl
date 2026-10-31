---
title: Built-in services
description: "The S3 backup storage, image registry and central logs that run beside the control plane for every team, their quotas, and the per-team Headscale."
section: Platform services
order: 10
---

# Built-in services

NebulaCtrl runs a few services for you so that backups, build images and logs work from the first minute, with nothing to set up and nothing to pay for:

| Service | What it holds | Product | Per team |
|---|---|---|---|
| **Backup storage** (S3) | NebulaCtrl's own data: your teams' backups. A project that needs S3 runs its own as a project service. | [SeaweedFS](https://github.com/seaweedfs/seaweedfs) 4.51, Apache-2.0 | a bucket and a key that can only touch it |
| **Image registry** | the images built for each team (see [build servers](build-servers.md)) | [CNCF Distribution](https://github.com/distribution/distribution) `registry:3.1.2`, Apache-2.0 | a repository prefix |
| **Central logs** | 7 days of the logs a team chooses to send ([logs](logs.md#central-logs)) | [Loki](https://github.com/grafana/loki) 3.7.9, **AGPL-3.0** | a log tenant |
| **Headscale** | a team's own tailnet control server ([below](#private-network-headscale)) | [Headscale](https://github.com/juanfont/headscale) v0.29.6, BSD-3-Clause | one per team, set up by the team |

The first three are **instance services**: one of each runs for the whole installation, **managed by the instance admin only**. Each team gets an isolated **slice** of them. Teams can be real tenants who do not trust each other, so a team cannot create, move or delete the services, cannot see another team's slice, and never sees a key. Headscale is different: a team is its own tailnet, so it stays a per-team service that Owners and Admins set up.

Everything else (an external S3, your own registry, a Loki or syslog drain) stays per team: the built-in services are a default, not a requirement. Single sign-on is not one of them: you connect the OpenID Connect provider you already run (see [single sign-on setup](../security/sso-setup.md)).

## Where they run

On the **control-plane host**, as containers of the control plane's own Compose project, next to it. There are no instance servers: nothing to place and nothing to choose. The installer sets them up (`seaweedfs`, `registry`, `loki`), pinned by digest like every other image, each on **an internal Docker network of its own** (`svc-s3`, `svc-registry`, `svc-loki`) that it shares only with the control plane: no route out, **no published port**, and no route from one service to another, so a compromised registry or Loki container cannot reach the S3 store. SeaweedFS's master, volume and filer ports (which have no sign-in of their own) are on the S3 network only, and its volume servers accept writes only with a token the master signed (a fresh random key is generated at every start). The control plane reaches the services at `http://seaweedfs:8333`, `http://registry:5000` and `http://loki:3100`.

The data lives in Docker volumes, or in host directories if you pass `--instance-dir <path>` to the [installer](../installing/installer.md); put it on the disk with room. The installer warns when less than **60 GB** are free there, because each team gets 50 GiB of backup storage by default (the quota is a limit, not a reservation: a team that stores 2 GiB takes 2 GiB).

One disk serves every team and the control plane's own database, so the services also guard the **whole disk**: while the filesystem of the control plane's container (the Docker data disk, where the default volumes live) has less than 2 GiB or 5% free, new writes to the built-in S3 and registry are refused (`507` for S3, `503` with `Retry-After` for the registry); reads, deletes and the control plane itself keep working. S3 uploads are counted by the bytes actually sent: a declared size that does not match the body is refused, and the parts of multipart uploads that were never completed count toward the quota and are aborted after 24 hours. Images are bounded too: a team may keep about **50 GiB** of images (the sum of the sizes its pushed manifests list; a layer shared by several images is counted for each), a push over it is refused with `403 denied` (deletes still work), and an image that NebulaCtrl did not build (one pushed by hand to `<team-id>/...`) is removed **14 days** after its last push; pushing the same digest again starts the 14 days over. Data in `--instance-dir` on a different filesystem is not seen by the disk guard.

## How servers reach them: private, through the agent link

There is **no public hostname and no open port** for any of the three. A server's agent already keeps an outbound connection to the control plane, and the built-in services ride on it:

1. The agent listens on three fixed addresses of **127.0.0.1** on its own server: `18333` (S3), `18500` (registry) and `18100` (logs). Nothing else on the network can reach them.
2. What connects there (the backup helper, the Docker daemon pulling or pushing an image, the log shipper) is carried **inside the agent's existing WebSocket** to the control plane, as streams. It works behind NAT and a firewall that only allows outbound traffic.
3. The control plane hands each stream to the service. **The agent's identity decides the team**, never anything the client sends: the control plane looks up the server of the connection, then its team, and injects the team's identity before the request reaches the service. The S3 request is accepted only for that team's bucket and key, the registry only under that team's prefix, and logs are written to that team's tenant.

It is always one hop. The dashboard never links to the storage: a download streams through the control plane. Because the agent is only a pipe, a server holds no credential that works outside its own team's slice.

A server whose agent cannot listen on one of the three ports (something else uses it) reports that in its heartbeat, and that service stays unavailable on that server until the port is free. The Docker daemon treats `127.0.0.1` as a trusted local registry, which is why the registry works without a certificate.

## Admin → Built-in services

For the instance admin (Admin, then **Built-in services**; scope `admin`). It shows:

- **Services**: whether the backup storage, the image registry and the central log store answer, how many teams have a slice of each and how much the backups use in total. **Check now** runs the same check the control plane runs every minute: the health of each service, the system bucket, and a slice for every team that lacks one. A failed service says why.
- **Default quota**: the backup storage every team gets unless you set its own. **50 GiB** until you change it; the minimum is **1 GiB**.
- **Teams**: one row per team with used space against its quota (a bar, amber from 80%), the state of its slices, **Edit quota** (a team's own quota, or back to the default) and, for a **deleted team**, the date its slice will be purged and **Delete now** (below).

### Quotas

The quota applies to a team's **backup storage** only. When it is reached, **new backups fail with a plain error** ("Built-in backup storage is full") and the team can free space or ask you for more; **existing backups are never deleted to make room**. Reads and deletes always work.

- **Alerts.** The team and the instance admin are notified at **80%** and again at **100%**, once per crossing; the alert re-arms when use falls below 75%. See [notifications](notifications.md) (`storage.quota_warning`, `storage.quota_full`). The instance admin's copy goes to the control-plane host's team.
- **Enforcement.** The control plane checks every write against the quota before it reaches the store (the authority), and the store's own bucket quota is a second fence. Use is measured every five minutes and after each backup, so the figure can trail a very recent backup by a moment.
- **Lowering a quota below use** only blocks new backups; nothing is deleted.
- **The registry is capped, the logs are not.** A team may keep about 50 GiB of images (see [where they run](#where-they-run)); logs follow their 7 days of retention, and images follow the registry's retention (below). Images NebulaCtrl did not build are removed 14 days after their last push.

### The system slice and the control-plane backup

The control-plane backup ([Admin → Control-plane backup](backup-restore.md)) can write to the built-in storage, in its own system bucket, which no team can reach. Admin says plainly that this is **not off-site**: if the host is lost, the database backup is lost with it. It suggests an external destination, and the backup works with both.

### When a team is deleted

Its slice (bucket, key, logs, registry images) is **kept for 30 days**, then deleted by a job that runs every hour. The row shows the purge date. **Delete now** removes it at once: it asks you to type the team's name, deletes the bucket and its key, the team's images and its logs, and is audited (`team_slice.purge`). It is refused while the team still exists.

## What a team sees

Teams read their slice and nothing else; there is no key, bucket name, address or setting to copy.

- **Team settings → Backup storage** shows a read-only **Built-in backup storage** card: used space against the quota, and the state (ready, full, being set up, not working). Every team has it from the minute it is created, so [database backups](database-backups.md) and [volume backups](backups.md) can be scheduled right away. The same page also adds an external S3 or a directory.
- **Team settings → Image registries** shows the built-in registry, with this team's size and the last cleanup, next to the registries you add. It cannot be edited or deleted. [Builds](build-servers.md) use it by default.
- **Team settings → Log forwarding** has **Central logs**, a switch that is **off by default**: turn it on and the servers start sending the team's service logs to its tenant. A service's **Logs** tab then has **Central logs (7 days)**; while forwarding is off it says how to turn it on ([logs](logs.md#central-logs)).

The API for the team is `GET /team-slice` (read scope; used and quota of the backups, the registry's size, the log retention). The admin's are `GET` and `PUT /admin/instance-services`, `POST /admin/instance-services/check`, `PUT /admin/team-slices/{sliceId}/quota` and `DELETE /admin/team-slices/{teamId}?confirm=<team name>`. None of them returns a key, a bucket or an address. AI clients (MCP) can read `getTeamSlice` and the central logs; the admin operations are denied there ([MCP](../reference/mcp.md)).

## The registry's retention

Images are pruned, not capped. An **hourly sweep** deletes the digests of images outside every service's live set through the registry API, one explicit digest at a time. A **weekly window** (starting between 02:00 and 05:00 UTC) collects the freed blobs with `registry garbage-collect`, never with `--delete-untagged`: builds queue, running pushes get up to two hours to finish, the registry is read-only for a few minutes (pushes get `503` with `Retry-After`, pulls keep working), the collection runs and writes come back. The collection runs on the control-plane host through an agent command that only that host accepts. There is no setting for any of it; the details are in [build servers](build-servers.md).

## Credentials

Nothing here asks you for a password. The control plane generates the admin key of the storage and the registry's token-signing key at install time (the installer writes the first to `.env`; the second is sealed in the database), and a key for each team's bucket when the slice is created. They are sealed with the instance key like every stored secret, and never appear in a view, the audit log or a command row. **Back up the instance key** (`NEBULACTRL_SECRET_KEY`): without it the sealed slice keys cannot be read, which would strand every team's backups.

## Private network (Headscale)

Headscale is the one platform service that stays per team: **Team settings → Private network (Headscale)**, Owners and Admins only (scope `admin`). **Set up for me** runs it as an ordinary service in the team's hidden **NebulaCtrl platform** project (one environment, `platform`; visible to administrators only, not in the projects list) on a server you choose, and connects the **Tailscale integration** to it. You can still use your own Tailscale or Headscale; nothing here is required.

### What a set-up does

`POST /platform-services` stages everything in one transaction (the service pinned by digest, its volume, its variables, its public hostname, an audit entry) and starts a job. The job **deploys** the staged service through the normal deploy path (so cancel, logs and the deployment history work), **waits** for it, then **wires** it: it waits until the public URL answers, creates the integration with the generated credentials and tests it (the test mints and expires a key). A test that fails is retried for up to 15 minutes (certificates and DNS take a while) and then the service is **failed** with the reason. A failed service keeps what it created; **Retry** resumes at the step that failed and **Reconnect** mints a new API key on a ready one.

### Hostname, TLS and the address

Tailscale clients and your people's browsers reach Headscale through the server's proxy at a hostname. It is generated like any generated domain (`<name>-platform-nebulactrl-platform.<base domain>`, else `<public ip>.sslip.io`); enter your own to override it. Certificates are Let's Encrypt, so the server needs an **ACME email** (422 `acme_email_required` otherwise). `.localhost` names are exempt, for local use. An **address override** that is `http://`, or names a non-standard port, is accepted as typed (`publicUrl` in the API); use `http://` only on a trusted network.

### Internal only

**Headscale is reachable from your servers and tailnet only, never from the public internet.** Every route of it carries an IP allow list, and a request from anywhere else gets `403` from the proxy before it reaches the service. There is no setting to open it up, and editing or removing its domain does not lift it.

| Allowed | Why |
|---|---|
| `10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16` | private networks, including the Docker networks of the server itself |
| `100.64.0.0/10`, `fd7a:115c:a1e0::/48` | your tailnet (Tailscale and Headscale addresses) |
| `fc00::/7`, `127.0.0.0/8`, `::1/128` | IPv6 unique-local addresses and loopback |
| the public IPv4 or IPv6 address of **every server of the team** that is not revoked | the other servers' tailscale clients reach the control URL from their public addresses |

An allow list you add to the domain (its access rules) is **intersected** with this one: it can narrow the list, never widen it. A client-IP depth is ignored unless the domain is behind Cloudflare's origin lock, so a forged `X-Forwarded-For` cannot pass. **The public ports are published on IPv4 only** (unless the edge network has IPv6), so an IPv6 client of an IPv4-only network cannot arrive as the network gateway and pass the private ranges above. For the same reason **a platform service cannot be served through a Cloudflare Tunnel**: its connector would be the peer, so its domain answers `403` to everyone while a tunnel serves it. The list follows your servers: adding a server with a public address, changing it or revoking a server re-sends the route tables (a `sync_routes` command, no redeploy). A server **without a recorded public address** cannot be allowed: set it under **Servers, Settings**. The list holds at most 200 entries.

### Configuration

Headscale runs with SQLite on its volume, no embedded DERP, MagicDNS under `tailnet.internal`, and a **database-backed policy** (`policy.mode: database`) that NebulaCtrl owns: the user `nebulactrl` owns `tag:nebula-svc` and `tag:nebula-mesh`, members reach tagged services, and the [server mesh](mesh.md#access-rules-acl) adds the rules it generates. Anything you change in that policy by hand is overwritten. The wiring creates the user, an **API key valid for 365 days** (`headscale apikeys create`, run in the container), writes the base policy before any node can join, and creates the Tailscale integration with provider Headscale, credential "API key" and `tag:nebula-svc`. The page shows the key's expiry; **Reconnect** mints a new one before it runs out.

**Relays are Tailscale's public DERP servers.** Headscale has no relay of its own here; it hands out Tailscale's public DERP map as the fallback for peers that cannot connect directly. Traffic stays end-to-end encrypted, but it does leave your infrastructure, and Headscale needs outbound internet to fetch the map, so it is the one service whose outbound access is not blocked. The set-up dialog says so ([DERP](mesh.md#derp-public-relays-are-the-fallback)). Tailscale clients need HTTPS on the control URL. Joining your own devices is not automated: create a pre-auth key with `headscale preauthkeys create --user nebulactrl` from the service's terminal.

**Existing installs are migrated once, with the mesh paused.** A Headscale set up before the mesh existed used a policy file; a background job pauses the mesh, redeploys Headscale with the database policy mode, writes the base policy and the mesh rules, reads them back and un-pauses ([mesh](mesh.md)). Established tunnels and existing nodes keep working meanwhile.

### Backups of Headscale's data

Headscale holds state. The page does not schedule a backup for it: back up its volume to one of the team's destinations from the service's **Backups** tab ([volume backups](backups.md)); the built-in backup storage is a valid destination.

### Removing

**Remove** needs the kind (`headscale`) typed as confirmation and asks whether to **also delete the data** (the volumes; off by default). The Tailscale integration is removed **first**; if servers still use the managed tailnet, or a [mesh](mesh.md) is enabled or has server nodes, nothing changes and you get the reason (`integration_in_use`). Then the service is deleted through the normal path and you can set it up again.

### API

| Method and path | |
|---|---|
| `GET /platform-services` | the catalog (image, licence, footprint, notice) with the team's Headscale |
| `POST /platform-services` | set it up (202): `kind` (`headscale`), `serverId`, optional `hostname`, `publicUrl`, `replace` |
| `GET /platform-services/{id}` | status, step, message, wiring, warnings, details |
| `POST /platform-services/{id}/retry` · `/reconnect` | resume a failed one; mint a new API key |
| `DELETE /platform-services/{id}?confirm=headscale&deleteData=` | unwire and remove |

Audit actions: `platform.provision`, `platform.remove`, `platform.rewire`, plus the usual entries of what the job creates.

## Licences

SeaweedFS, the registry and Headscale are Apache-2.0 or BSD-3-Clause. **Loki is AGPL-3.0.** NebulaCtrl runs the **unmodified upstream images** as separate containers and talks to them over HTTP; it does not link, bundle or modify them, and the licence applies to those programs, not to NebulaCtrl. If you modify and offer Loki to others over a network, the AGPL's source-offer terms apply to your modified version. See [third-party licences](../security/third-party-licences.md).

## Upgrading the images

The versions are pinned by tag and digest of the multi-platform index: the instance services in `deploy/install/docker-compose.yml` and Headscale in `internal/platform/catalog.go` (a test refuses an unpinned Headscale image). To bump one, read the product's release notes, `docker buildx imagetools inspect <image>:<tag>` for the digest and change the pin; the installer re-run (or the one-click update) applies it. Headscale services already set up keep their pinned version until you deploy a newer one on the service in the platform project, and Headscale reads its configuration at start, so check the release notes for a configuration change first.

## Upgrading from per-team built-in services

Earlier versions ran a separate S3, log store, Grafana and registry for each team as services in its platform project, behind a public hostname with an IP allow list. They are replaced by the instance services above, with **no automatic adoption**. See the [upgrade notes](../installing/upgrade.md#built-in-services-are-now-instance-services).

## Limits

- **One host.** The instance services run on the control-plane host only: its disk holds every team's backups, images and logs, and its loss loses them. Add an external destination for backups you cannot lose; the control-plane backup says the same.
- **Images are capped at about 50 GiB per team, logs are not.** Logs have retention only, and the 50 GiB image cap is fixed (it cannot be changed per team). Many teams together can still fill the disk before the weekly collection runs; watch it.
- **Backup storage is for NebulaCtrl's data.** It does not serve a project's own S3 needs and there is no way to reach it from a project.
- **The agent link carries the traffic.** A backup or an image pull through the built-in services is as fast as the server's connection to the control plane, and stops while that link is down; backups fail cleanly and the next one runs.
- **Docker's loopback rule.** The registry relies on Docker accepting `127.0.0.1` registries without TLS. That is documented Docker behaviour (`dockerd`: "Local registries, whose IP address falls in the 127.0.0.0/8 range, are automatically marked as insecure"), but upstream notes it may change. Pulls through the containerd image store were not verified.
- **Headscale:** one per team; the public URL is fixed at set-up (changing the hostname means removing and setting up again); it was not exercised with real Tailscale clients across real networks ([mesh limits](mesh.md#limits)).
