---
title: Migrate from Coolify
description: "Move a host from Coolify to NebulaCtrl in place: install side by side, copy the data, cut over in minutes, roll back in about one."
section: Installing
order: 35
---

# Migrate from Coolify, in place

This page moves **one host** from Coolify 4.x to NebulaCtrl without a second server. Both run side by side on the host while you rebuild and rehearse; Coolify keeps serving the whole time. A short **cutover window** (minutes) then moves ports 80/443 to NebulaCtrl, and Coolify stays stopped but intact for a soak period, which is also your rollback.

If you can afford a second machine, installing NebulaCtrl there, copying the data and switching DNS is simpler and needs no downtime. This page is for the single-host case.

## What changes hands

| Coolify | NebulaCtrl |
|---|---|
| `coolify-proxy` (Traefik) on 80/443, dashboard on `:8000` | NebulaCtrl's own Traefik (run by its agent), first on spare ports and then on 80/443; dashboard on a port you pick (`--port 8100` below) or behind its own domain |
| Applications (Dockerfile, image, Git) | Services; see the [Coolify importer](#2-recreate-the-projects) |
| Databases | Managed databases (Postgres, PostGIS, Valkey and others); see [restoring a dump](#3-rehearse-copy-the-data-and-test-on-the-spare-ports) |
| Per-domain rules (IP allow lists, compression) | Per-domain access and compress options |
| Daily database backup | Scheduled backups to S3, off the host's disk |
| Dynamic Traefik files (an external upstream such as Plex on a tailnet node) | An external-service route; by hand |

## 0. Before you start

- A way to **log in over the network that survives the cutover**: SSH is untouched by NebulaCtrl's installer, and Tailscale (if you use it) is separate software.
- List what holds the ports you care about: `ss -ltnp | grep -E ':(80|443|8000|8100|8080|8443)\b'`. Coolify uses 80/443 (proxy), 8000 (dashboard) and 6001/6002 (realtime). The commands below use **8100** for NebulaCtrl's dashboard and **8080/8443** for its proxy.
- Cloudflare: while HTTP-01 cannot work (see below), every certificate is issued with **DNS-01**. Create an API token with **Zone:Read** and **DNS:Edit** on the zones of your domains, and keep it in a file readable by root only. If your DNS is Cloudflare-proxied, also connect Cloudflare in NebulaCtrl (Team settings → Integrations) so it owns the records and can lock the origin; see [Cloudflare](../operations/cloudflare.md).
- Keep the Coolify host's **Coolify API token** (read, and read-sensitive if you want the secrets imported) for the importer.
- Know your cutover window: the apps are down for the time between stopping Coolify and the first successful request through NebulaCtrl (plan 5 to 10 minutes).

## 1. Install NebulaCtrl side by side

Run this on the Coolify host. It does not touch ufw, fail2ban or the hardening settings, and it leaves ports 80, 443 and 8000 to Coolify:

```sh
echo -n '<cloudflare-api-token>' | sudo install -m 0600 /dev/stdin /root/cf-token

curl -fsSL https://nebulactrl.com/install.sh | sudo bash -s -- \
  --no-firewall --no-fail2ban --no-hardening \
  --port 8100 --proxy-http-port 8080 --proxy-https-port 8443 \
  --domain nebula.example.com --email you@example.com \
  --dashboard-allow 100.64.0.0/10,fd7a:115c:a1e0::/48 \
  --dashboard-dns01 cloudflare --dns-token-file /root/cf-token
```

(`/install.sh` of any running NebulaCtrl control plane serves the same script. Add `--no-auto-updates` too if you do not want unattended upgrades on this host.)

What each part does:

| Option | Why |
|---|---|
| `--no-firewall --no-fail2ban --no-hardening` | Keep the host as it is. Without `--no-firewall` the installer would switch on ufw, which also closes ports Coolify needs (6001/6002, 8000) because it has no tailnet rules |
| `--port 8100` | With a domain this is the control plane's **loopback** port that the host's agent uses (it is 8080 by default, which is now the proxy's). Without a domain it is the dashboard's port; Coolify's dashboard holds 8000 |
| `--proxy-http-port 8080 --proxy-https-port 8443` | NebulaCtrl's Traefik runs next to Coolify's. Apps answer on `https://<host>:8443` until you move it |
| `--domain`, `--email` | The dashboard's own HTTPS name. Its URL carries the port: `https://nebula.example.com:8443` |
| `--dashboard-allow <cidrs>` | Only these addresses may open the dashboard. `100.64.0.0/10` and `fd7a:115c:a1e0::/48` are the tailnet ranges |
| `--dashboard-dns01 cloudflare --dns-token-file …` | The dashboard certificate comes from DNS-01. It works while the name resolves to a tailnet address and while another proxy holds port 80. The token is read from the file, never printed, and sealed into NebulaCtrl's credential store |

**Without a domain**, tailnet only: replace the domain, allow list and DNS-01 options with `--bind <tailscale-ip> --port 8100`. The dashboard is then `http://<tailscale-ip>:8100` (plain HTTP over the tailnet), published on that address only and remembered by later runs. See [the installer](installer.md#binding-the-dashboard-to-one-address).

Point `nebula.example.com` at the host's **tailnet** address (A record). Open the setup link the installer prints from a tailnet device; the allow list also blocks the setup page from everywhere else. The installer warns about HTTP-01 where it could not work, and the first server (this host) is already enrolled: Servers, the host, **Proxy** shows its ports as 8080/8443.

Check that nothing of Coolify's moved: `curl -sI http://127.0.0.1:80/`, your sites, and Coolify's dashboard on `:8000` all answer as before.

## 2. Recreate the projects

Pre-issue certificates **before** any DNS change: for each domain set the DNS-01 resolver (`cloudflare`) or add a wildcard certificate under Servers, Proxy, DNS-01. HTTP-01 cannot validate while Coolify owns port 80; NebulaCtrl warns on every domain that would use it ("HTTP-01 cannot validate"). See [DNS-01 and wildcard certificates](../operations/dns01-wildcard-certificates.md).

### Importer

Import projects, applications, variables, domains and databases from the Coolify API. The **control plane** (not your laptop) must reach the Coolify URL; a tailnet address is fine. The token needs the Coolify abilities `read` and `read:sensitive` (the second one is what lets secrets come across). Review first, then stage; nothing is deployed unless you pass `--deploy`:

```sh
export COOLIFY_TOKEN=<coolify api token>
nebulactrl import coolify --url https://coolify.example.com --plan        # review (alias --dry-run; exit 1 on blockers; --json)
nebulactrl import coolify --url https://coolify.example.com --server <server> \
  [--registry-user ghcr.io=<user> --registry-token-env ghcr.io=<ENVVAR>] [--git-connection <id|name>] \
  [--set-env <app>.<KEY>=<ENVVAR>]
```

It stages projects and environments, image and Git services, managed databases of the same engine and version, **every** domain of each application, health checks, limits, Docker options, pre- and post-deploy commands, volumes (empty) and variables with secrets marked and database references rewritten to `${{db.*}}`. **Stays manual:** the data of databases and volumes (the importer prints the `databases restore` command to use), registry credentials unless you pass them, private-repository access (a GitHub App or deploy key cannot be exported), locked variable values unless you supply them with `--set-env`, DNS, Traefik labels and dynamic files (an external Plex route, per-domain IP allow lists, redirects), scheduled tasks, one-click services and host port mappings. More flags (`--exclude`, `--rename`, `--domain`, `--secret`, `--skip-var`, ...) and the full mapping are on the importer's own page: [import from Coolify](../operations/import-coolify.md).

Then: review every staged service in the dashboard (variables marked secret, health checks, the `entrypoint` and pre-deploy command overrides your Coolify apps used), create the managed databases **empty**, and add an S3 backup destination and schedule.

## 3. Rehearse: copy the data and test on the spare ports

Copy each database with `pg_dump` from Coolify's container into the new managed database, then deploy the apps on the spare ports and test with `curl --resolve` so no DNS has to change:

```sh
curl -sk --resolve app.example.com:8443:127.0.0.1 https://app.example.com:8443/
```

### Restoring a dump

Dump from Coolify's database container (the apps' writers stopped for the final one) and restore into a managed database, by default as a **new sibling database** so the original stays untouched:

```sh
docker exec <coolify-pg> pg_dump -U <user> -Fc <db> > db.pgdump
nebulactrl databases restore <db> --file db.pgdump
```

The upload streams through the control plane to the agent, with no temporary copy; `--no-owner` and `--no-acl` are applied for you. In the dashboard: the database, Backups, "Restore from a dump file". Details, limits and `--in-place` are in [restore from a dump file](../operations/database-backups.md#restore-from-a-dump-file) (that page is the reference; this section is short on purpose). Do the rehearsal restore as often as you like; only the final one matters.

Cache and queue stores such as Valkey/Redis usually start **empty** after the queue drains; copy them only when you know they hold state you cannot rebuild.

Rehearse everything: logins, background jobs, scheduled tasks, uploads, third-party webhooks that would hit a different host name, the external routes.

## 4. The cutover window

Pick the window when traffic is lowest and tell people. Have a second SSH session open.

1. **Stop the writers.** Stop the queue workers and let the queues drain, then stop the Coolify applications (Coolify, or `docker stop <app containers>`).
2. **Final data copy.** Take a last `pg_dump` and restore it into the managed database (as above, with the apps stopped). Point the new services at the new databases if they are not already.
3. **Stop Coolify's proxy.** Stop Coolify itself first, because it can start its proxy again, then the proxy: `docker stop coolify coolify-realtime`, then `docker stop coolify-proxy`. Confirm 80 and 443 are free: `ss -ltn | grep -E ':(80|443)\b'` prints nothing.
4. **Move NebulaCtrl's proxy to 80/443.** Either re-run the installer with the default ports,

   ```sh
   curl -fsSL http://127.0.0.1:8100/install.sh | sudo bash -s -- --proxy-http-port 80 --proxy-https-port 443
   ```

   or, as the instance admin, set the ports in **Servers, the host, Proxy, Proxy ports** (review the plan, confirm, then **Deploy proxy**; Reset hands control back to the agent's flags; see [proxy ports](../operations/proxy-ports.md)). Traefik is recreated in about a second. The installer refuses while something still holds 80 or 443 and changes nothing in that case. A port set there overrides the installer's flags. See [moving the proxy back to 80/443](installer.md#side-by-side-with-another-proxy).
5. **Switch DNS** where it has to change. A Cloudflare-proxied domain needs no change (the origin is the same host); a name that pointed at the tailnet address stays as it is.
6. **Smoke-test every host name and the external routes**, then start the writers (workers) on NebulaCtrl. Check the dashboard on `https://nebula.example.com` (the installer rewrote its URL without the port).

## 5. Roll back

If something is wrong, you can be back on Coolify in about a minute, because Coolify's data and containers are untouched:

```sh
sudo systemctl stop nebulactrl-agent            # stops NebulaCtrl's management
sudo docker stop nebulactrl-traefik             # releases 80/443
sudo docker start coolify coolify-realtime coolify-proxy   # and your Coolify apps
```

Data written to NebulaCtrl's databases since the cutover is not in Coolify's: if that matters, `pg_dump` the new database and restore it into Coolify's before starting the apps. To retry NebulaCtrl later, re-run the installer with `--proxy-http-port 8080 --proxy-https-port 8443` (it restarts the agent and puts the proxy on the spare ports).

## 6. Soak, then clean up

Run for about **7 days** with Coolify stopped but intact. Then take a final backup of the NebulaCtrl instance (Admin → Control-plane backup; the secret key apart from it) and a last Coolify backup, and remove Coolify: its containers, images and volumes (`docker system prune` after listing what it removes) and, if you want it, turn on the firewall and the hardening that you skipped (`--no-firewall` and `--no-hardening` are only install-time options: re-run the installer without them).

## What to expect

- Coolify's custom Traefik files (`/data/coolify/proxy/dynamic/*.yaml`) are not in its API: recreate them as external routes and per-domain access rules.
- Imported HTTP health checks run Coolify's own request **inside the container** (`curl`, else `wget`, against `http://localhost:<port><path>`), as Coolify did: frameworks with a host allow list (Laravel trusted hosts, Django `ALLOWED_HOSTS`) refuse a probe from outside, whose Host is the container address. Apps without a port deploy as recreate with a short gap.
- Compression, IP allow lists and redirects are options per domain, not per proxy.
- Plan the secrets: variables the importer cannot read stay empty until you fill them in, and `APP_KEY`-style secrets must keep their exact values to keep existing sessions and encrypted data valid.
