---
title: Upgrade and roll back
description: "Update the control plane to a new version, or go back to the previous one."
section: Installing
order: 50
---

# Upgrade and roll back

## Before you upgrade

1. Read the release notes for schema or protocol changes.
2. Confirm you hold `NEBULACTRL_SECRET_KEY` ([backup and restore](../operations/backup-restore.md)). The backup of the database is taken **for you**: the installer backs the control plane up before it changes anything, and the control plane does the same at startup when its version changed ([how it works](../operations/upgrades.md#backup-before-a-control-plane-upgrade)). **If that backup fails the upgrade stops**; fix the cause, or re-run with `--force-upgrade` to go on without one.
3. Note the version you run now (`docker compose -f /opt/nebulactrl/docker-compose.yml --env-file /opt/nebulactrl/.env images`).

## Upgrade the control plane (and the host's agent)

When a newer release exists the dashboard says so, and the **instance admin** can update from **Admin → Updates** with one click: the same installer, run for you on this host, with the backup first and an automatic return to the previous image if the new version is not healthy ([updating NebulaCtrl](../operations/updating.md)). The command below is the manual way, and the only one for a control plane installed with `--no-local-server`.

Re-run the installer with the new version. Nothing that exists is regenerated:

```sh
curl -fsSL https://nebula.example.com/install.sh | sudo bash -s -- --update-to 1.4.0
# or, with an image you built or saved yourself:
sudo bash install.sh --image nebulactrl:1.4.0 --image-archive nebulactrl-1.4.0.tar
```

`--update-to` installs that release for this run only, so a later plain re-run upgrades again. `--version 1.4.0` does the same but also **pins** the installation to 1.4.0; use it only when you want to stay there ([installer options](installer.md#options)).

The installer keeps `.env` and the database, loads or pulls the image, recreates only the containers whose definition changed, waits for `/readyz`, and upgrades the host's agent to the binary that ships with the new control plane. Your options are remembered in `/opt/nebulactrl/install.conf`.

- The server applies **forward-only** database migrations on startup, under a Postgres advisory lock. There is no automatic down migration.
- Startup order is safe: Postgres is kept, the server container is replaced, agents reconnect on their own (exponential backoff, up to 60 s) and the control plane **never stops remote containers** while it is down. Your services keep serving through a control-plane restart or upgrade, including those on the control-plane host (the proxy is the agent's, not the control plane's).
- Commands acknowledged before the restart keep their state; the agent redelivers unacknowledged results after reconnecting.
- The upgraded control plane serves new agent binaries immediately, and **agents upgrade themselves** when they reconnect (below).

## Agents upgrade themselves

An agent that is behind the control plane downloads the new binary from its control plane, verifies the SHA-256, replaces itself atomically, restarts when its running work is done and rolls back if the new binary does not start ([details](../operations/upgrades.md)). This is automatic by default (Servers page → **Upgrade automatically**); **Upgrade agents** does the same one server at a time on request, and each server's Updates tab shows the version skew. Your services keep running.

## Upgrade other servers by hand

You need this once for an agent that predates self-upgrade, for a server that is offline during the upgrade, and when you switched the automatic upgrade off. Re-run the agent installer on the host (no token needed; the identity is kept):

```sh
curl -fsSL https://nebula.example.com/install-agent.sh | sudo sh -s -- --url https://nebula.example.com
```

It downloads the new binary, verifies it against the checksum the control plane publishes, restarts the service, and verifies the service stays up. Managed containers and the Traefik proxy keep running during the agent restart. The wire protocol is versioned; a control plane refuses an unknown **major** protocol version with close code 4400, so upgrade the control plane first and agents second. The server card shows each agent's version.

## Built-in services are now instance services

**This release changes where backup storage, the image registry and logs come from. It is a breaking change for anyone who used the old per-team built-in S3.**

Before, **Set up for me** ran a separate SeaweedFS S3, Loki, Grafana and container registry for each team, as services in its hidden platform project, behind a public hostname with an IP allow list. Now one S3, one registry and one log store run on the control-plane host for every team, and each team gets an isolated slice ([built-in services](../operations/platform-services.md)). Headscale stays per team.

What the upgrade does:

- **The installer re-run (or the one-click update) adds the services.** `seaweedfs`, `registry` and `loki` start next to the control plane on an internal network with no published port, and the installer writes `NEBULACTRL_S3_ADMIN_KEY` and `NEBULACTRL_S3_ADMIN_SECRET` to `.env`. They need disk: the installer warns below 60 GB free. Pass `--instance-dir <path>` to keep their data on another disk ([installer](installer.md#options)).
- **Every team gets a slice** the first time the new control plane starts: a bucket and key, a registry prefix and a log tenant. Backups to the new **Built-in backup storage** work from then on, with a 50 GiB quota per team that the instance admin can change under **Admin → Built-in services**. Central logs are **off** until a team turns them on under **Log forwarding**.
- **Agents upgrade themselves** ([above](#agents-upgrade-themselves)); a server whose agent has not upgraded yet cannot use the built-in services.
- **Migration 0011 removes the old rows.** The platform services of kind S3, Loki, Grafana and registry are deleted from the database, and so are the *managed* registries (anything that referenced one falls back to the team's default registry). Nothing is adopted: NebulaCtrl does not move your data.

What you have to do if a team used the old built-in S3:

1. **The old per-team built-in S3 no longer has a public route after the upgrade.** NebulaCtrl no longer manages it. The backup destination it created, "NebulaCtrl S3", stays as an ordinary S3 destination row pointing at the old address, so backups scheduled to it fail.
2. **Move its backups, or run them again, on the built-in storage.** Edit each backup schedule (and each volume backup) to use **Built-in backup storage** on **Team settings → Backup storage**, then run a backup and check it under the schedule's history. Backups already stored in the old bucket are not copied for you: keep the old S3 container running until you have copied out what you still need, or let the new schedules build up their own history. Volume backups start a new repository in the new bucket.
3. **Clean up by hand.** **Migration 0016 stops the old S3, Loki, Grafana and registry containers** (their services in the team's hidden **NebulaCtrl platform** project are set to *stopped*; nothing is deleted, so their data volumes stay on disk and you can start one again from its service page to copy data out). Delete the stopped services and their volumes from that project when you no longer need them. The log drain "NebulaCtrl Loki" stays as an ordinary Loki drain to the old address; delete it, or turn on **Central logs**. Releases built through a managed registry cannot be rolled back until they are rebuilt (**Rebuild commit**).

The control-plane backup is not affected unless it wrote to the old S3: check **Admin → Control-plane backup** and, if you want its backups on the built-in storage, choose **Built-in storage** there (Admin says it is not off-site).

## DNS management in connected Cloudflare zones is always on

**Migration 0012 removes the "manage DNS" settings.** The per-domain *Manage DNS automatically* switch, the per-status-page *Manage* switch and the team-level defaults (*manage DNS automatically* and `defaultProxied`) are gone, and so is the `manage` key of `[domains."x".cloudflare]` in nebula.toml (a file that still has it is rejected with `unknown_key`; see [nebula.toml](../reference/nebula-toml.md)). In the API, `manage` on the domain and status page DNS endpoints is read-only, and the write endpoints (`PUT /domains/{id}/dns`, `PUT /status-pages/{id}/cloudflare`, `PUT /integrations/cloudflare`) answer `422` to a field they no longer accept.

What the upgrade does:

- **Domains and status pages in a connected zone are managed from the next sync** (the sweep runs every ten minutes), including those that had management switched off. A record somebody else created is never overwritten: it shows as a **Conflicting record** until a person presses **Replace with NebulaCtrl's record**, and a record that already points at the right target shows as `present` with **Adopt record**.
- **Existing domains keep their proxy setting.** A live DNS-only domain is not flipped to proxied. Only names that start being managed afterwards are created proxied by default; the team's old `defaultProxied` is not used any more.
- **Certificates are not replaced.** Names in a connected zone are issued through an automatic Cloudflare DNS-01 resolver after the next **Deploy proxy**, but only once NebulaCtrl's own DNS sync has written the name's record (status `managed`), and a name that already holds a valid certificate keeps it and renews it over HTTP-01 ([details](../operations/dns01-wildcard-certificates.md#names-in-a-connected-cloudflare-zone-automatic)).
- **Migration 0013 adds the certificate observation** (the table `domain_cert_stores`). Agents report which ACME store holds each host's certificate, so the domain's certificate line reads "Certificate via Cloudflare DNS (DNS-01)" only when that is true, and "Certificate via HTTP-01, renews over HTTP-01" for a certificate that was issued over HTTP-01. Nothing is re-issued by force.

If you want a name to stay out of NebulaCtrl's hands, keep its zone out of the token's **Zone Resources**.

## Roll back the control plane

Because migrations only go forward, an older server binary may not work against a newer schema. The supported rollback is:

1. `cd /opt/nebulactrl && docker compose down` (do **not** pass `-v`).
2. Restore the pre-upgrade backup (Admin → Control-plane backup → History, trigger *Before upgrade*; or `/var/backups/nebulactrl/pre-upgrade-*.dump` from the installer) into a clean database ([restore](../operations/backup-restore.md#restore)). Changes made after the dump (deployments, variable edits) are lost.
3. Run the installer with the previous `--version` (or image).

If the new version only changed code (no new migration, see `db/migrations` in the diff), re-running the installer with the previous version is enough.

## Roll back an agent

A failed self-upgrade rolls back by itself. After a good one the previous binary stays at `/usr/local/bin/nebulactrl-agent.prev`: move it over the binary and `systemctl restart nebulactrl-agent`. Or re-run the installer from an older control plane, or place an older binary at `/usr/local/bin/nebulactrl-agent`. The control plane never downgrades an agent, and an agent newer than the control plane is not upgraded. Use an agent whose protocol major version the control plane accepts.

## Roll back an application (not the platform)

That is a deployment rollback inside the UI/API/CLI: it re-deploys an earlier release's image and configuration. It does **not** restore data. See [operations: deployments](../operations/deployments.md).
