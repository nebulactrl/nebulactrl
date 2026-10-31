---
title: The installer
description: "What install.sh does, its options, and how to re-run, upgrade or recover with it."
section: Installing
order: 30
---

# The installer

`deploy/install/install.sh` installs the control plane on one machine and makes that machine the first server. It is a single bash script. The control plane serves a self-contained copy (libraries and files inlined) at `/install.sh`, so this works from any running instance too:

```sh
curl -fsSL https://<host>/install.sh | sudo bash -s -- [options]
sudo bash deploy/install/install.sh [options]        # from a checkout
```

Supported: **Debian 12/13, Ubuntu 22.04, 24.04 and 26.04, RHEL/Rocky/AlmaLinux 9**; amd64 and arm64; systemd; root. Anything else stops with a message (`--force-unsupported` tries a Debian or RHEL family system anyway).

`--check` runs the system and port checks, prints the plan and changes nothing.

## Two ways to serve the dashboard

| | With `--domain nebula.example.com --email you@example.com` | Without a domain |
|---|---|---|
| URL | `https://nebula.example.com`, Let's Encrypt certificate | `http://<ip>:8000` (plain HTTP) |
| Served by | the host's own proxy (see below) | the control-plane container, published on `0.0.0.0:8000` |
| Control-plane container binds | `127.0.0.1:8080` (only the host's agent uses it) | `0.0.0.0:8000` |
| Before you start | the domain's **A record must point at this machine** and 80/443 must be reachable from the internet (Let's Encrypt HTTP-01) | nothing |

Without a domain the installer ends with a notice: passwords and sessions travel unencrypted, and servers you add from other machines need `--insecure-dev`. Use it on a trusted network and switch soon. Switching is a re-run with `--domain`: secrets, data and the agent identity are kept, the dashboard moves behind the proxy, and port 8000 stops being published.

The installer warns when the domain does not resolve yet, or resolves to another address than this machine's. The certificate is issued as soon as DNS is right. `--public-ip` overrides the detected address (it is also what **Generate domain** uses, `<service>-<env>-<project>.<ip>.sslip.io`).

### Single proxy on the control-plane host

The control-plane host always runs an agent, and **that agent's Traefik is the only proxy on the machine**. The compose file has no Traefik of its own. In domain mode the control plane adds one *system route* to the route table of that server: the dashboard's hostname, automatic Let's Encrypt (the server's ACME email is the one you gave), to `nebulactrl:8080` on the `nebulactrl-edge` network. It is part of every route push, deploy and reconcile of that server, so no deployment can displace it, and no service can take the dashboard's hostname (`409 domain_conflict`). It appears in the server's proxy route table as "NebulaCtrl dashboard".

Because the host's agent enrolls **before** the admin account exists (the installer creates the first team and hands the agent its token; **Setup** then adopts that team, and you become its owner), the route and the certificate are in place before you open the setup link, so the setup page already loads over HTTPS.

If the dashboard route breaks, the dashboard is still reachable on `http://127.0.0.1:8080` from the machine itself (for example through `ssh -L 8080:127.0.0.1:8080`).

With `--no-local-server` there is no agent and no proxy: in domain mode the container stays on `127.0.0.1:8080` and you provide the HTTPS proxy (it must pass WebSocket upgrades on `/agent/v1/connect` and must not buffer `text/event-stream`).

### Accept workloads

The first server has a setting **Accept workloads** (Servers → the server → Settings → Scheduling; `acceptWorkloads` in the API), **on by default**: you can deploy apps next to NebulaCtrl. Turn it off to keep the machine for NebulaCtrl alone. Off refuses new services and moves onto it (`422 server_not_accepting_workloads`, also in the drain planner, template and Compose-import checks and the placement pickers) and leaves what already runs untouched. Every server has the setting; the installer's host is the one marked "control plane".

### Dashboard access: allow list and DNS-01

In domain mode the dashboard is one route on the host's proxy, and two options shape it:

```sh
... --domain nebula.example.com --email you@example.com \
    --dashboard-allow 100.64.0.0/10,fd7a:115c:a1e0::/48 \
    --dashboard-dns01 cloudflare --dns-token-file /root/cf-token
```

- `--dashboard-allow <cidr,...>` puts an **IP allow list** on the dashboard route (Traefik `ipAllowList`, matched against the address of the TCP peer, which is the visitor when nothing sits in front of the proxy). The example above is "tailnet only". Addresses and CIDRs, IPv4 and IPv6, at most 200 entries; `off` removes the list. Everything else gets `403` from the proxy, including the setup link, so open the setup URL from an allowed address.
- `--dashboard-dns01 cloudflare` issues the dashboard's certificate with **DNS-01**, which needs no inbound connection: it works when the name resolves to a tailnet or LAN address, or when another proxy holds port 80 (see [side by side](#side-by-side-with-another-proxy)). `--dns-token-file <path>` names a file that holds the Cloudflare API token (Zone:Read and DNS:Edit on the zone). The token is **read from the file, never from the command line, and never printed**; the installer keeps it in `.env` (0600) and the control plane seals it into the DNS-01 credential store (the same one as Servers → Proxy → DNS-01) the first time it starts. Delete the file afterwards; rotate the token later in the DNS credential, not by re-running the installer.
- Both options need `--domain` (without a domain there is no HTTPS route; use `--bind` below) and the local server.
- They **seed** the settings once. Afterwards an instance admin edits them in **Admin → Dashboard access**: the allow list (the dashboard refuses to save a list that excludes your current address unless you confirm) and the certificate method. Changes are audited. Re-running the installer with different values does not overwrite what you changed there.

`--dashboard-dns01` supports Cloudflare in the installer. For another provider, add its credential and resolver under Servers → Proxy → DNS-01 after the install, then choose it in Dashboard access.

### Binding the dashboard to one address

Without a domain, `--bind <ip>` publishes the dashboard port on that local address only, for example the Tailscale address: `--bind 100.64.0.7 --port 8100`. The dashboard URL becomes `http://<ip>:<port>`. The host's own agent still connects over `127.0.0.1`: the installer adds `docker-compose.loopback.yml` (merged by `COMPOSE_FILE` in `.env`) that publishes the same port on loopback. The address is remembered in `install.conf`; an address you put into `.env` by hand is adopted on the next run instead of being reset to `0.0.0.0`. `--bind 0.0.0.0` goes back to every address. `--bind` must be an address of this machine; with `--domain` it is ignored (the container listens on `127.0.0.1` behind the proxy).

Docker-published ports bypass ufw/firewalld, so the bind address is the protection, not the firewall.

### Side by side with another proxy

NebulaCtrl's Traefik normally owns ports 80 and 443. To run it next to another proxy (the typical case: the proxy of the panel you are migrating away from, see [migrate from Coolify](migrate-from-coolify.md)), give it other ports:

```sh
... --port 8100 --proxy-http-port 8080 --proxy-https-port 8443 [--proxy-admin-port 9100]
```

- `--proxy-http-port`, `--proxy-https-port` and `--proxy-admin-port` (loopback only, default 9000) go to the local agent's installer, which writes them into the agent's systemd unit. Apps answer on `https://<host>:8443` until the proxy moves.
- With a domain the dashboard URL carries the HTTPS port (`https://nebula.example.com:8443`). `--port` is then the control plane's loopback port (default 8080; the installer picks 8081, and so on, when the proxy sits on 8080), and `--port` is the dashboard port in IP mode. If another dashboard on the host already holds a port (8000 is common), pass a free one, for example `--port 8100`.
- **Certificates: use DNS-01.** Let's Encrypt HTTP-01 needs port 80 on the internet, which the other proxy holds. The installer warns when the dashboard would use HTTP-01 with the proxy off port 80; use `--dashboard-dns01`. The same is checked for app domains (the plan and the domain list warn "HTTP-01 cannot validate"): give them a DNS-01 resolver or a wildcard certificate under Servers → Proxy.
- The firewall step opens the configured ports. On a host that runs another proxy, pass `--no-firewall` (a first ufw activation would close the ports the other proxy needs).

**Moving the proxy back to 80/443.** Stop the other proxy first (`docker stop <its container>`), then either

1. re-run the installer with the default ports: `curl -fsSL http://127.0.0.1:8100/install.sh | sudo bash -s -- --proxy-http-port 80 --proxy-https-port 443`. The agent's unit is rewritten, the agent restarts, and Traefik is recreated on the new ports (about a second of dropped connections). The installer checks that 80/443 are free first and refuses otherwise, then waits for the proxy to publish them; or
2. as an instance admin, in **Servers → the server → Proxy → Proxy ports**: review the plan, confirm to stage it, then **Deploy proxy** applies it by recreating Traefik ([proxy ports](../operations/proxy-ports.md)). Use Reset to hand control back to the agent's own flags.

A port set in Proxy ports **overrides** the installer's flags, so after using it, change ports there (or press Reset) rather than by re-running the installer. In domain mode the dashboard URL (`NEBULACTRL_PUBLIC_URL`) includes the HTTPS port, so after moving back re-run the installer (step 1) to rewrite it, or use the old URL until you do: the route is the same.

## Options

| Option | Meaning |
|---|---|
| `--domain <fqdn>` | Serve over HTTPS with Let's Encrypt |
| `--email <addr>` | Let's Encrypt contact; required with `--domain` |
| `--public-ip <ip>` | This machine's public IPv4 address (detected when omitted) |
| `--port <n>` | Dashboard port without a domain (default 8000). With a domain: the control plane's loopback port for the host's agent (default 8080) |
| `--bind <ip>` | Without a domain: publish the dashboard on this local address only (for example the Tailscale IP). Remembered |
| `--proxy-http-port <n>`, `--proxy-https-port <n>`, `--proxy-admin-port <n>` | Host ports of the local server's proxy (default 80, 443, 9000), to run beside another proxy. Remembered |
| `--dashboard-allow <cidr,...>` | Allow list on the dashboard route (needs `--domain`); `off` removes it |
| `--dashboard-dns01 cloudflare`, `--dns-token-file <path>` | DNS-01 certificate for the dashboard; the token is read from the file and never printed |
| `--version <tag>`, `--image <ref>` | Image to run. The default is the version of the control plane that served the script (`curl <your control plane>/install.sh`), or the release the site was last published with when the script was fetched from nebulactrl.com (`latest` from a raw checkout). A release is **verified** and pulled by digest: `ghcr.io/nebulactrl/nebulactrl@sha256:…`, the digest in the release's signed manifest (a public image; nothing needs `docker login`; see [Verifying a release](#verifying-a-release)). `--version` takes a release version (`0.1.2`) or `latest`; anything else needs `--image`. Given explicitly, the version is **pinned** in `install.conf`: a later plain re-run keeps it, and `--version latest` drops the pin. `--image` is an image of your own and is **not** verified |
| `--update-to <version>` | Install that release for this run only (what Admin → Updates runs, as `bash install.sh --update-to <version>`). Unlike `--version` it is not remembered, so a later plain re-run still upgrades. An installation pinned with `--version` moves its pin to the new version; one pinned with `--image` refuses and tells you to re-run with `--image` |
| `--image-archive <file>` | `docker load` the image from a `docker save` archive instead of pulling it. Not verified (the installer says so) |
| `--no-docker-install` | Fail instead of installing Docker |
| `--no-daemon-defaults` | Leave `/etc/docker/daemon.json` alone |
| `--no-local-server` | No agent on this machine |
| `--no-backup` | No daily control-plane backup |
| `--no-firewall` / `--no-fail2ban` / `--no-auto-updates` / `--no-hardening` | Skip that step |
| `--restart-docker` | Restart Docker to apply `daemon.json` even when containers would stop |
| `--dir`, `--backup-dir` | `/opt/nebulactrl`, `/var/backups/nebulactrl` |
| `--instance-dir <path>` | Where the [built-in services](../operations/platform-services.md) (S3 backup storage, image registry, log store) keep their data, as `s3/`, `registry/` and `loki/` below it. Default: Docker named volumes. Remembered. The directory is created root-only (mode 0700; the service directories below it 0750, owned by the uid their container runs as): it holds every team's backups and images. The installer warns below 60 GB free on that disk |
| `--force-unsupported`, `--check` | see above |
| `--force-upgrade` | go on with an upgrade although the control-plane backup taken first failed (without it a failed backup stops the installer before anything is changed) |
| `--uninstall …` | run the uninstaller ([uninstall](uninstall.md)) |

Options are remembered in `/opt/nebulactrl/install.conf`, so a plain re-run keeps your choices; flags override them.

> [!NOTE]
> Every release publishes the control-plane image to `ghcr.io/nebulactrl/nebulactrl` (public) for linux/amd64 and linux/arm64, tagged `X.Y.Z`, `X.Y` and `latest`. To run an image you built yourself, build it from a checkout (`docker build -t nebulactrl:local .`) and use `--image nebulactrl:local` (same machine) or `docker save nebulactrl:local -o nebulactrl.tar` and `--image nebulactrl:local --image-archive nebulactrl.tar` (another machine). The installer's own tests do exactly this.

## Verifying a release

Releases are signed with [minisign](https://jedisct1.github.io/minisign/) in CI. The public key is in `install.sh` (`NC_RELEASE_PUBKEY`), compiled into the control plane and the agent, and printed here:

```text
RWSfYAoo0hKcaKeFYcOsofTSgfZdb2GVo/jjE4pfxaDYSi4SSsC6inQP
```

Each release carries `install.sh`, `SHA256SUMS`, the two agent binaries (`nebulactrl-agent-linux-amd64`, `-arm64`, the ones inside the control-plane image) and `release-manifest.txt`, each with a `.minisig` signature. They are published on the release's GitHub page and at `https://nebulactrl.com/releases/<version>/` (the installer looks there; `releases/latest/` is the newest release). The manifest records the image digest, the agent binaries' SHA-256 and the SHA-256 of every file under `deploy/install`.

What is checked, and when:

- **The installer** verifies the manifest, then pulls `image@sha256:<digest>` (never a tag). With `openssl` 3 or `minisign` installed (every supported distribution has openssl; the installer adds it when both are missing) nothing else is needed. When the script is neither the one the site or a control plane serves nor a checkout, its libraries and files are fetched from the release's **tag**, never a branch, and compared with the manifest; a file that differs is not used.
- **The one-click update** (Admin → Updates): the control plane checks `install.sh` against `install.sh.minisig` before it sends anything, and the agent checks it again with the key compiled into it before the installer runs as root. An unsigned installer, a changed one or a signature by another key stops the update and says why. An `http://` feed or installer URL is refused (`NEBULACTRL_UPDATE_FEED_URL` must be https); credentials in the feed URL are used to read the feed and are never passed on to the installer, so a mirror's `install.sh` and `install.sh.minisig` must be readable without them.
- **The agent binary** (`install-agent.sh`, also what the installer runs for the control-plane host): the script downloads the binary from the control plane and its signature from the release (`https://nebulactrl.com/releases/<version>/nebulactrl-agent-linux-<arch>.minisig`, the version being the control plane's), and installs it only when the signature verifies. A development control plane has no release; pass `--no-verify` for a build you made yourself.

By hand: `minisign -Vm install.sh -P RWSfYAoo0hKcaKeFYcOsofTSgfZdb2GVo/jjE4pfxaDYSi4SSsC6inQP` (with `install.sh.minisig` next to it). A network that cannot reach nebulactrl.com mirrors the release's directory (`releases/<version>/`) and sets `NC_RELEASE_BASE` (https only) for the installer and for `install-agent.sh`; the signatures still have to verify, so a mirror cannot change what is installed. The libraries and files of a script that is not assembled are fetched from the repository at the release's tag (`NC_SOURCE_BASE` points at a mirror of it).

The signing key pair is made once with `minisign -G -W`; the secret half is the repository secret `NEBULACTRL_MINISIGN_KEY`, and `internal/releasesign.PublicKey` and `NC_RELEASE_PUBKEY` in `install.sh` hold the public half. The release workflow refuses to publish when the secret does not match the compiled-in key.

## What each step does

Every step is idempotent and safe to run again. The installer prints `ok`, `warn` and what it did; warnings are repeated at the end. A failure stops the run and says in which step; fix the cause and run it again.

1. **System checks.** Root, Linux, supported distribution, systemd, architecture, RAM (warns below 900 MB), free disk (warns below 3 GB), and that the proxy's ports (80/443 unless `--proxy-http-port`/`--proxy-https-port` say otherwise) and the dashboard port are free (unless NebulaCtrl itself holds them; on a re-run only ports the proxy does not publish yet are checked). Every option is validated here: CIDR lists, ports, `--bind` (an address of this machine), the token file. Missing `curl`, `ss`, `base32`, CA certificates and `gpg` are installed.
2. **Docker.** If `docker` is missing, Docker Engine, the Compose plugin and buildx are installed from Docker's official repository (`download.docker.com`, signed key in `/etc/apt/keyrings` or the `.repo` file for dnf; Rocky and Alma use Docker's CentOS repository). Distribution packages that conflict (`docker.io`, `podman-docker`, …) stop the run with an instruction rather than being removed. An existing Docker is kept: it must be **24 or newer** and have the **Compose plugin** (installed from Docker's repository when only that is missing).
3. **Daemon defaults** (`/etc/docker/daemon.json`): `log-driver json-file` with `max-size 10m` and `max-file 3`; `live-restore: true`; `userland-proxy: false`; `default-address-pools` `10.212.0.0/16` in /24s (the agent allocates its environment networks from `10.213.0.0/16`, so the two never collide). **Existing keys always win**: a key you already set is kept and reported, only missing keys are added, and the original is saved as `daemon.json.nebulactrl-bak`. `userland-proxy` and the address pool need a Docker restart: on a fresh Docker it just happens; when containers already run **without** live-restore it is **not** done (they would stop) and the installer tells you to restart Docker in a maintenance window, or pass `--restart-docker`.
4. **Edge network.** `nebulactrl-edge` is created with the label the agent uses (the agent would create it, but the control plane joins it).
5. **Files and secrets** in `/opt/nebulactrl`: `docker-compose.yml`, `.env` (0600), `install.conf`, `uninstall.sh`, `state/`. Generated once, never regenerated: the Postgres password (48 hex), `NEBULACTRL_SECRET_KEY` (32 random bytes, base64), the setup token, and the local agent's enrollment token. If a database volume from an earlier install exists but `.env` is gone, the installer **refuses** to invent a new key and says so. Postgres settings (`PG_*`) are recomputed from the RAM on every run; unchanged RAM means unchanged file means no restart.
6. **Control plane and Postgres.** Postgres 17 runs on an **internal Docker network** (no route out, no published port, reachable only by the control plane), initialised with SCRAM-SHA-256 for local and host connections and data checksums, capabilities dropped. Tuning from RAM: `shared_buffers` 15 % (128 MB–2 GB, the host also runs apps), `effective_cache_size` 40 %, `work_mem` and `maintenance_work_mem` scaled, `random_page_cost` 1.1 on SSD/NVMe, WAL compression, an idle-in-transaction timeout. The control plane runs read-only, with all capabilities dropped, `no-new-privileges` and without the Docker socket. The installer waits for both to be healthy and for `/readyz`.
   The installer also starts the **built-in services**: `seaweedfs` (S3), `registry` and `loki`, each on an internal network of its own (`svc-s3`, `svc-registry`, `svc-loki`, shared only with the control plane) with no published port, and writes their generated keys (`NEBULACTRL_S3_ADMIN_KEY`, `NEBULACTRL_S3_ADMIN_SECRET`, `NEBULACTRL_REGISTRY_HTTP_SECRET`) to `.env`. Servers reach them only through the agent's outbound link. There is nothing to set up: every team gets its slice when it is created.
7. **Daily control-plane backup.** The instance backup under Admin → Control-plane backup is switched on: daily 03:30 UTC, 14 kept, to the **local directory** `/var/backups/nebulactrl/control-plane` (owner uid 65532, mode 0700), with a first run right away. It is only seeded while nobody configured it. The secret key is never in a backup. A local copy is not off-site: the installer and the UI both say so, add an S3-compatible bucket.
8. **Local agent.** The agent binary comes from the control plane (`/install-agent.sh`, the same installer other servers use) and runs as the systemd unit `nebulactrl-agent`, dialing `http://127.0.0.1:<port>`. (The agent accepts plain HTTP only for loopback addresses.) A drop-in sets `RestartSec=12` so that early restarts, before the control plane accepts the token, stay under the enrollment rate limit. Re-runs upgrade the binary and keep the identity.
9. **Firewall.** SSH first, then 80/tcp, 443/tcp, 443/udp, and the dashboard port in IP mode. ufw on Debian/Ubuntu (default deny incoming, allow outgoing, only set when ufw was inactive), firewalld on RHEL family. The SSH ports are the ones `sshd -T` reports, the one your session came in on (`$SSH_CONNECTION`) and anything sshd listens on; rules are added **before** the firewall is switched on. Where the kernel has no firewall support (some containers) the step warns and continues. **Docker-published ports bypass ufw/firewalld rules**; NebulaCtrl never publishes service ports on its own, and TCP/UDP routes you create carry mandatory allowlists.
10. **fail2ban** for sshd (`/etc/fail2ban/jail.d/nebulactrl-sshd.local`: 5 failures in 10 minutes, 1 hour ban, systemd journal backend; EPEL on RHEL family).
11. **Automatic security updates**: `unattended-upgrades` (`20auto-upgrades`) or `dnf-automatic` set to `upgrade_type = security`, `apply_updates = yes`. No automatic reboot. Docker's packages are not security-origin, so Docker is not upgraded behind your back.
12. **Lynis hardening.** `lib/hardening.sh` runs a Lynis audit, applies a curated set of **safe** remediations, re-audits, and prints the hardening index with what remains; the agent repeats it weekly and the server's **Security** tab shows the result. It never touches SSH authentication, root login or firewall rules. It only warns when it fails. See [host hardening](../security/host-hardening.md).
13. **Summary:** dashboard URL, the setup URL with the one-time token, where the files and backups are, the secret-key reminder (`sudo grep NEBULACTRL_SECRET_KEY /opt/nebulactrl/.env`: store it apart from the backups), the off-site backup reminder and every warning.

**SSH is never modified.** The installer reads sshd's configuration to find the ports to allow and changes nothing in it; the test suite checks that `sshd_config*` is byte-identical after an install, a re-run and an uninstall.

## First run

Open the **setup URL** the installer printed (`<dashboard>/setup?token=…`). The page needs the token because the installer sets `NEBULACTRL_SETUP_TOKEN`: without it nobody who finds the dashboard first can claim the admin account (`403 setup_token_invalid`; attempts are rate limited). The token is removed from the address bar after the page reads it and stops mattering when setup is done. It is stored in `/opt/nebulactrl/.env` and `INSTALL-SUMMARY.txt`. Choose your team name, create the account, and the first server (this machine) is already online.

If you start the control plane yourself (a manual Compose install) and set no `NEBULACTRL_SETUP_TOKEN`, it does not leave setup open: while no account exists it generates a one-time token at boot and logs it once (the `setup_token` and `setup_url` fields of a `first-run setup` warning). Find it with `docker compose logs nebulactrl` (in `/opt/nebulactrl`, or wherever your Compose file lives), open the logged `setup_url` or paste the token on the setup page. The token lives in memory only: a restart before setup is finished logs a new one, and once an account exists none is generated.

## Files, ports and containers

| | |
|---|---|
| `/opt/nebulactrl/` | `docker-compose.yml`, `.env` (every secret, 0600), `install.conf`, `uninstall.sh`, `state/`, `INSTALL-SUMMARY.txt` |
| `/var/backups/nebulactrl/control-plane/` | local control-plane backups |
| `/usr/local/bin/nebulactrl-agent`, `nebulactrl-agent.service` (+ `.service.d/10-installer.conf`), `/etc/nebulactrl-agent.env` | the local agent |
| `/var/lib/nebulactrl-agent/` | agent identity, build workspaces, log history |
| `/etc/docker/daemon.json` (+ `.nebulactrl-bak`), `/etc/apt/keyrings/docker.asc`, `docker.list` / `docker-ce.repo` | Docker |
| containers `nebulactrl-nebulactrl-1`, `nebulactrl-postgres-1`; networks `nebulactrl_backend` (internal), `nebulactrl-edge`; volume `nebulactrl_postgres-data` | the control plane |
| containers `nebulactrl-seaweedfs-1`, `nebulactrl-registry-1`, `nebulactrl-loki-1`; networks `nebulactrl_svc-s3`, `nebulactrl_svc-registry`, `nebulactrl_svc-loki` (internal, one per service); volumes `nebulactrl_s3-data`, `nebulactrl_registry-data`, `nebulactrl_loki-data` (or the `--instance-dir` directories) | the [built-in services](../operations/platform-services.md), on the control-plane host |
| containers `nebulactrl-traefik`, `nebulactrl-pages`, volume `nebulactrl-traefik-acme` | the agent's proxy |
| Listening on the host | 80, 443 (tcp, udp) the proxy, or the `--proxy-http-port`/`--proxy-https-port` you chose; `127.0.0.1:9000` the proxy's admin; `127.0.0.1:8080` (domain mode, `--port` changes it) or `0.0.0.0:8000` (IP mode, `--bind` narrows it) the dashboard; SSH. **Nothing else: Postgres is not published, and neither is the status listener (`nebulactrl:8081` on the edge network, the only way [status pages](../operations/status-pages.md#how-public-pages-are-served) are served).** |

## Re-running, upgrading, recovering

- **On an existing installation the installer backs the control plane up first** (`nebulactrl-server pre-upgrade-backup` in the running container; a `pg_dump` to `/var/backups/nebulactrl/pre-upgrade-<time>.dump` when that control plane is too old to have the command) and stops before changing anything if the backup failed, unless `--force-upgrade`. See [upgrades](../operations/upgrades.md#backup-before-a-control-plane-upgrade).
- **From the dashboard.** When a release newer than yours exists, the instance admin can run the upgrade with one click (Admin → Updates): the agent on this host starts this same installer as a systemd unit with `--update-to <version>`. See [updating NebulaCtrl](../operations/updating.md).
- **If the new version does not become healthy** (`docker compose up --wait` for up to 240 seconds, then `/readyz` for about two minutes), an upgrade restores the previous image (kept by ID; a moving tag is pointed back at it) and the previous `docker-compose.yml` and `.env` (temporary copies, removed at exit), starts the previous version, says it rolled back and exits with status **4**. If that does not come up either, it exits with status 1 and says the control plane is down. Migrations that already ran stay applied; the pre-upgrade backup is the way back for data ([updating](../operations/updating.md#if-the-new-version-does-not-start)).
- `curl … | sudo bash` again, or `sudo bash /opt/nebulactrl/…` with the same options: nothing that exists is regenerated; changed settings are applied; containers are only recreated when their definition changed (a new image tag, new RAM tuning); the agent binary is upgraded to the control plane's version. See [upgrade](upgrade.md).
- A step failed: fix the cause and run again. A power loss or a killed run leaves nothing a re-run does not repair.
- `.env` lost but the database volume remains: restore `.env` from your backup (the installer will not invent a key), or [uninstall with `--purge`](uninstall.md) and restore the database from a backup.
- **Locked out** (lost password or lost 2FA of the only Owner): `cd /opt/nebulactrl && docker compose exec nebulactrl nebulactrl-server reset-password you@example.com` prints a one-time reset link (30 minutes); `reset-2fa you@example.com` removes the authenticator app, passkeys and recovery codes after you type the email. Both run on the host against the database, end the account's sessions and are audited as system actions. If setup was never completed there is no account to reset: use the setup URL instead. See [Troubleshooting → Locked out](../operations/troubleshooting.md#locked-out).
- The host's agent identity from an earlier installation is moved aside (`/var/lib/nebulactrl-agent.old-<time>`) when a fresh control plane is installed over it.

## Tested

`tests/install/run.sh` runs the real installer in disposable systemd machines (OrbStack) for Debian 12, Ubuntu 22.04, 24.04 and 26.04, and Rocky 9: install through the piped, self-contained script, secrets absent from its output, Postgres private and SCRAM-only, the daily backup written, firewall, fail2ban and updates configured; a re-run that changes nothing; setup with and without the token and enrollment of the host's agent before the admin exists; the switch to a domain with the dashboard served through the host proxy (HTTP→HTTPS redirect, WebSocket upgrade, login through the proxy); an app deployed on the control-plane host and **Accept workloads** off refusing the next one while the first keeps serving; uninstall (data kept), reinstall over the kept data, and `--purge`. See [`tests/install/README.md`](../../tests/install/README.md). The audit/hardening step is exercised when `NC_TEST_HARDENING=1`.
