---
title: Troubleshooting
description: "Proxy and port 80, certificates, DNS, agents that will not connect, stuck deployments."
section: Troubleshooting
order: 10
---

# Troubleshooting

Start with: which part is failing, the control plane (`docker compose logs nebulactrl`), the agent (`journalctl -u nebulactrl-agent`), or the proxy (`docker logs nebulactrl-traefik` on the agent host)?

## The agent does not connect

| Symptom | Cause / fix |
|---|---|
| Installer: "refusing plain http:// control plane URL" | The control plane URL is `http://`. Use an `https://` URL (see [serving the dashboard](../installing/installer.md#two-ways-to-serve-the-dashboard)) or `--insecure-dev` on a trusted dev network. |
| Installer: "Docker Engine … too old" / "daemon is not reachable" | Install or start Docker 24+ (`systemctl start docker`). |
| Journal: enrollment `401` / "invalid or expired token" | Tokens are single use and live 1 hour. Create a new one. |
| Journal: TLS / certificate errors | The control plane's certificate is not valid for the hostname in `--url`, or the host's clock is wrong (`timedatectl`). |
| Journal: "agent revoked; re-enroll" | The server was revoked. [Re-enroll](servers.md#re-enroll-a-host). |
| Connects, drops, reconnects | A reverse proxy is closing idle WebSockets. Raise its read/idle timeout for `/agent/v1/connect`; the agent heartbeats every 10 s. |
| Server shows `offline` but the host is fine | Outbound HTTPS blocked, DNS broken, or the control plane URL changed. `curl -v <url>/healthz` from the host. |

## Proxy: port 80 or 443 in use

The server card shows **Proxy error** and adding a domain fails with `422 proxy_unavailable`. Find the occupant:

```sh
sudo ss -ltnp 'sport = :80 or sport = :443'
docker logs nebulactrl-traefik 2>&1 | tail -20
```

Typical culprits: a host nginx/Apache, or another reverse-proxy container. (The control plane's own compose file has no proxy: the host's agent's Traefik serves the dashboard.) Stop it, **or** move NebulaCtrl's proxy: `sudo systemctl edit nebulactrl-agent`, add `--proxy-http-port` / `--proxy-https-port` to `ExecStart` (or put `NEBULACTRL_PROXY_HTTP_PORT=8080` into `/etc/nebulactrl-agent.env`) and restart. Public HTTPS certificate issuance needs the real 80/443 reachable from the internet.

The proxy also needs **127.0.0.1:9000** for its admin entrypoint. If another program on the host uses that port, the proxy fails to start the same way; move it with `--proxy-admin-port` (or `NEBULACTRL_PROXY_ADMIN_PORT`).

The agent creates `nebulactrl-traefik` when it starts, so after freeing the port run `sudo systemctl restart nebulactrl-agent`; the heartbeat then reports the proxy as running.

## A domain does not work

1. **DNS**: the domain row shows DNS status. For a hostname in a zone of your connected Cloudflare account NebulaCtrl creates the record itself and the row follows the Cloudflare status: **DNS ok** (`managed`, `present`), **DNS pending** (`missing`, `wrong_target`) or **Conflicting record**. A conflict is a record NebulaCtrl did not create that differs from the wanted one or cannot sit next to it (for example a CNAME where an A is wanted); it is never touched until you press **Replace with NebulaCtrl's record** ([Cloudflare](cloudflare.md#ownership-only-records-nebulactrl-created-are-touched)). For other hostnames, A records must point at the server's public IP; a job rechecks every 10 minutes (`dns_ok` is empty when the server has no public IP configured), and wildcard CNAMEs to a proxy such as Cloudflare in orange-cloud mode make the check say "mismatch" although it may work.
2. **Certificate**: Traefik needs port 80 reachable from the internet for the ACME HTTP-01 challenge and an ACME email on the server; only non-local names get Let's Encrypt certificates. Names in a zone of your connected Cloudflare account use DNS-01 through Cloudflare instead and do not need port 80, once **Deploy proxy** has applied the resolver and NebulaCtrl's own DNS sync has written the name's record (DNS status `managed`): the domain's certificate line reads "Certificate via Cloudflare DNS (DNS-01)" only when the agent reports the certificate in the DNS-01 store. Before the first successful sync, for a `present`, `conflict` or `missing` record, and after a refused DNS write, the name stays on HTTP-01 and the line says why. A name that already holds an HTTP-01 certificate reads "Certificate via HTTP-01, renews over HTTP-01" and keeps it (see [DNS-01 and wildcard certificates](dns01-wildcard-certificates.md#names-in-a-connected-cloudflare-zone-automatic)). Look at `docker logs nebulactrl-traefik 2>&1 | grep -i acme`. Let's Encrypt rate limits apply to repeated failures; wait or use another hostname.
3. **502 / "no upstream"**: the service is not healthy or not on the edge network. `docker ps --filter name=nc-<slug>`; check the deployment succeeded.
4. **Hostname conflict**: `409 domain_conflict`: a hostname belongs to exactly one service in the whole installation.
5. **Local testing**: `*.localhost`, `*.local`, bare names and IPs get Traefik's default self-signed certificate: use `curl -k`. Unknown hosts get Traefik's default 404.

## A deployment fails

Open the deployment: the step timeline says *where*, the log says *why*.

| Message | Meaning |
|---|---|
| "Not delivered: server offline. No changes were made." | The agent never received the command within 15 min. Safe to retry once the server is online. |
| "Waiting for <server> (offline since …)" | The command was **acknowledged**; its outcome is unknown until the agent returns. Do not retry blindly; check the host or revoke the server. |
| pull error (unauthorized, not found) | Image name/tag or registry credentials wrong. |
| "Previous release is still serving" + container logs | The new container failed its health check; fix the app or the health check settings (path, port, start period, deadline). The old release is untouched. |
| `rolled_back` | The route verification failed; the old release was restored. Check the route/health path and that the app answers `<500` for the first hostname. |
| `rollback_failed` | See [recovering from rollback_failed](deployments.md#recovering-from-rollback_failed). |
| `unresolved_reference` / `reference_cycle` | A `${{service.KEY}}` variable points at an unknown service/key in the same environment, or variables reference each other in a loop. |
| `409 deployment_in_progress` | Another deployment of this service is still active; wait for it to reach a terminal state. |
| build fails | The full `docker build` output is in the deployment log. Builds run on the target host with its Docker daemon (timeout 30 min). |

## Disk full on an agent host

Images, the build cache and volumes share Docker's data root. Check the disk gauge (warns at the per-server threshold, default 80 %). The server's [Docker cleanup](docker-cleanup.md) runs daily and starts by itself when the alert is active; press *Run cleanup now* on the server page (Settings → Logs and disk) to reclaim at once, or lower a service's *image retention*. Never `docker volume prune` blindly: it removes service data (the cleanup's *Remove unused volumes* keeps every volume of your services).

## The UI looks stale

If the pill "Live updates paused — retrying" is visible, the event stream (`/api/v1/events`, Server-Sent Events) is blocked, usually by a proxy that buffers or has a short timeout. Data still refreshes every 15 s. Disable buffering for `text/event-stream` in your reverse proxy.

## Control plane will not start

| Log message | Fix |
|---|---|
| `NEBULACTRL_SECRET_KEY is required` / invalid | The key must be base64 of exactly 32 bytes (set `NEBULACTRL_SECRET_KEY` or `NEBULACTRL_SECRET_KEY_FILE`, not both). |
| `sealed values are not on the current key or format; run nebulactrl-server rekey` (warning) | Values are still on an older key or in the v1 format. Run `nebulactrl-server rekey` ([secrets at rest](../security/secrets-at-rest.md#rotating-the-key)). Everything keeps working while the old key is in `NEBULACTRL_SECRET_KEY_PREVIOUS`. |
| `decryption failed` for a stored credential, `rekey` reports `cannot decrypt` | The value was sealed with a key that is not in the ring (a previous key was removed too early), or its row was tampered with. Put the old key back into `NEBULACTRL_SECRET_KEY_PREVIOUS`, or re-enter the secret. |
| `database unreachable` | Postgres is not healthy or the password in `NEBULACTRL_DATABASE_URL` does not match `POSTGRES_PASSWORD` (the password is only applied when the database volume is first created). |
| `goose up` errors | A migration failed; the database may be on a newer schema than the binary (downgrade). Restore a backup or run the newer version. |
| Exits immediately under Compose | `docker compose logs nebulactrl`; missing required `.env` values are reported by Compose itself. |
| The setup page asks for a token and the install has none | With no `NEBULACTRL_SETUP_TOKEN` set, the server generates a one-time token at first boot while no account exists and logs it once: `docker compose logs nebulactrl` and look for the `first-run setup` warning (`setup_token`, `setup_url`). A restart before setup is finished logs a new token and invalidates the old one. Setting `NEBULACTRL_SETUP_TOKEN` in `.env` pins it instead. |

## Locked out

Lost the password of the only Owner, or the phone with the authenticator app? Both are fixed from the control-plane host; nothing needs SMTP or another admin. Run the commands with the server's environment, in the standard install (`/opt/nebulactrl`; the Compose service is `nebulactrl`, or `control-plane` in `deploy/compose`):

```sh
cd /opt/nebulactrl
docker compose exec nebulactrl nebulactrl-server reset-password you@example.com
docker compose exec nebulactrl nebulactrl-server reset-2fa you@example.com
```

- **`reset-password <email>`** prints a one-time link (`<dashboard>/reset-password?token=ncr_…`, works once, valid 30 minutes). Open it and choose a new password. If `NEBULACTRL_PUBLIC_URL` is not set the output shows the path only: put the address you open the dashboard at in front of it. `--generate` sets a random password instead and prints it once; use it for an account that has no password (passkey or single sign-on only). It asks you to type the account email first (`--confirm <email>` answers in advance).
- **`reset-2fa <email>`** removes the authenticator app (also a half-finished enrolment and its lockout), every passkey and every recovery code, after you type the account email (`--confirm <email>`). Afterwards the account signs in with its password alone until it enrols again in **Account settings → Security**; a team that requires 2FA asks for that at the next sign-in. It refuses for an account with no password and no linked provider whose only way in is its passkeys: run `reset-password --generate` first.
- A lost password **and** a lost second factor: run both. A reset link never bypasses 2FA.
- Both end every browser session of the account and are audited as **system** actions (`operator.password_reset_link`, `operator.password_set`, `operator.2fa_reset`, in each of the account's teams, with the OS user that ran them; never the link or the password). API tokens are not revoked: revoke them in **Account settings → API tokens** if you suspect a leak.
- No accounts yet (`error: this instance has no accounts yet…`): first-run setup is still pending. Open the setup URL the installer printed (`<dashboard>/setup?token=…`, also in `/opt/nebulactrl/INSTALL-SUMMARY.txt`). Without an installer-set token, read the generated one from `docker compose logs nebulactrl`.
- Whoever can run these has the database and the secret key already; see [login methods](../security/login-methods.md#forgot-password) for the by-email flow and the team-admin reset.

## Reporting a problem

Include the NebulaCtrl version, the deployment ID, the relevant `correlation_id` log lines, and `journalctl -u nebulactrl-agent` around the time. Remove secrets first. Security problems: see [`SECURITY.md`](../../SECURITY.md).
