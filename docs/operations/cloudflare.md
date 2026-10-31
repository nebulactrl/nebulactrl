---
title: Cloudflare DNS
description: "Connect Cloudflare, keep DNS records in sync, see live DNS status and lock proxied origins to Cloudflare."
section: Servers
order: 60
---

# Cloudflare: DNS records, proxy toggle, live DNS status and the origin lock

NebulaCtrl can keep the DNS records of your service domains in Cloudflare in step with where the services run. One **team-level connection** (an API token) covers every zone the token can see. This page covers the connection, the records NebulaCtrl manages, the per-domain proxy toggle, the DNS status and the [origin lock](#origin-lock) that keeps proxied domains reachable only through Cloudflare. Cloudflare Tunnel is a separate part of the integration (see the *Tunnel* section at the end).

Nothing here is required: without a connection, domains work and you point DNS yourself. Once Cloudflare is connected, there is no per-domain or per-page switch for DNS management: every hostname whose zone is in the connected account is managed.

The hostnames of [status pages](status-pages.md#cloudflare-dns) get the same treatment: records for the control-plane server's public IPs (`A` and `AAAA`), a proxy toggle, the live status, and the origin lock on the page's status route. Ownership, conflicts, the SSL/TLS warnings and the lock below apply to them unchanged; a page's records are marked `nebulactrl:<pageId>` and its changes are audited as `status_page.dns.update`.

## Connect Cloudflare

**Team settings → Integrations → Cloudflare** (owners and admins only).

1. In Cloudflare, **My Profile → API Tokens → Create Token → Custom token** (or an account-owned token under **Manage Account → Account API tokens**) with:

   | Permission | Needed for |
   |---|---|
   | Zone → Zone → **Read** | listing zones, the zone picker (required) |
   | Zone → DNS → **Edit** | creating, updating and deleting records (required) |
   | Zone → Zone Settings → **Read** | the SSL-mode warning for proxied records, and the SSL-mode check of the origin lock (optional) |
   | Zone → Zone Settings → **Edit** | turning on Authenticated Origin Pulls for the [origin lock](#origin-lock) (optional; without it the lock is IP-only) |
   | Account → Cloudflare Tunnel → **Edit** | tunnels only (optional) |

   Limit **Zone Resources** to the zones NebulaCtrl should manage.
2. Paste the token. NebulaCtrl verifies it with Cloudflare (it must be able to list zones) before it stores it. The token is encrypted with `NEBULACTRL_SECRET_KEY`, is write-only and never appears in API responses, logs, audit entries or notifications.

If you already created a Cloudflare DNS-01 credential (Servers → Proxy → DNS-01 certificates), choose it under **Or reuse a DNS-01 credential** instead of typing the token again. It must have the scopes above. The token is copied; the two features then rotate independently.

The zone list is cached and refreshed on **Test connection**, when the token changes and every six hours. Domains are matched to the zone with the longest matching name (`app.eu.example.com` goes to `eu.example.com` if you have that zone, else `example.com`). Connecting needs no further choice: every domain (and status page hostname) in the token's zones is managed from then on, on connect and by the next sync, and a name added later is managed from the start. A record somebody else created is never overwritten (see [Ownership](#ownership-only-records-nebulactrl-created-are-touched)). **Disconnect** stops all management; tick *Also delete the DNS records NebulaCtrl created* to remove them from Cloudflare first.

**A connected zone belongs to the team that connected it.** Every name inside an **active** zone of a team's connection (the zone itself and all names below it) can be used for a service domain or a status page only by that team; another team that tries gets `409` "the hostname … is not available". The refusal has the same wording as for any taken name, so it never reveals whether, or which team, uses a name. When two teams connected the same zone the older connection wins, and the longest zone name wins when zones are nested. A zone that is still `pending` at Cloudflare (its name servers not delegated) claims nothing, because Cloudflare accepts any domain into an account before it is proven. Names outside every connected zone stay first come, first served. A domain created before a zone was claimed keeps working and can be edited, but cannot be renamed into a name the claim covers. The hostname the instance generates for a service (`<service>-<env>-<project>.<base>`) is tied to the team: when the plain candidates are taken, the last candidate carries the team's own short id, so another team that took the plain names cannot leave a service without a hostname.

## What NebulaCtrl manages

Every domain that falls into a connected zone gets records created for its hostname:

| Server | Record |
|---|---|
| public IPv4 set | `A` to the server's public IP |
| public IPv6 set | `AAAA` to the server's public IP |
| behind a Cloudflare Tunnel | proxied `CNAME` to `<tunnel-id>.cfargotunnel.com` |

Records follow the domain through its life:

* **domain added** — the record is created right away (before the deployment), so DNS can propagate. Domains that appear through a template, a pull-request preview or a route service are picked up by their first DNS check, seconds after creation;
* **domain removed, service deleted** — the record is deleted; if two domains share a hostname (different paths) the record stays until both are gone;
* **hostname renamed** — the new name gets a record, the old one is removed;
* **the server's public IP changes** — every record of the services placed on that server is updated;
* **a service moves to another server** (including a [drain](draining-servers.md)) — the record is retargeted when the deployment on the new server succeeds, not before, so traffic keeps reaching the old server until the new one is live;
* **a tunnel is created or removed on the server** — address records and the tunnel CNAME replace each other.
* **the integration is connected later** — domains that had no zone yet are managed on connect and by the next sync. The same holds for domains and pages that existed before management became unconditional: they are managed from the next sync (the sweep below runs every ten minutes).

Syncs run as background jobs with retries. A sweep every ten minutes repairs drift (for example a record deleted in the Cloudflare dashboard). Every change is audited as `dns.record.create`, `dns.record.update` or `dns.record.delete` (hostname, zone, type, target, never the token). If a sync cannot succeed (Cloudflare errors, missing permission, rejected token) the domain shows the error and NebulaCtrl sends a **DNS sync failed** [notification](notifications.md) (once per failure, at most every six hours).

### Ownership: only records NebulaCtrl created are touched

Every record NebulaCtrl creates carries the comment `nebulactrl:<domainId>` and, where your Cloudflare plan supports tags, the tag `nebulactrl:managed`. NebulaCtrl modifies or deletes a record only if **it carries that marker and NebulaCtrl recorded creating it**. In particular:

* A record you created is **never modified or deleted** by a sync, a sweep or a status read, not even when it points to the wrong place. If it already points at the right target it is reported as `present`; if it differs from the wanted one or cannot sit next to it, it is reported as a `conflict`. Either way it stays as it is until you act on it.
* If you edit the comment of a managed record, it stops being managed: NebulaCtrl will not touch it again (and forgets it when the domain is removed).
* Records of other types (TXT, MX, …) are never read or changed.

Two actions hand a record over, and only these change an unmarked record (team admin only, see below):

* **Adopt record** (a `present` record): the record already points at the right target. NebulaCtrl marks it as managed and applies the proxy setting; where it points does not change.
* **Replace with NebulaCtrl's record** (a `conflict`): NebulaCtrl first deletes the unmarked records of the conflicting types that block or exceed the wanted ones (for example a `CNAME` where an `A` is wanted, or a second `A` record), then marks or points one record per target. The dialog lists what is replaced before you confirm. Each change is audited: `dns.record.update` with `adopted: true`, `dns.record.delete` with `replaced: true` and `dns.record.create`. Through the API this is `POST /api/v1/domains/{id}/dns/fix` with `{"adopt": true}`; without `adopt` the call answers `409 adoption_required`.

Taking over a record you did not create changes DNS the whole organisation may rely on (`mail.example.com`, `vpn.example.com`), so **both actions need a team admin** (the `admin` scope, and a token not restricted to projects), for service domains as for status pages. A developer who calls the fix with `adopt: true` gets `403` and nothing is written. Creating and correcting NebulaCtrl's own records needs only the ordinary `projects:write`.

## Per-domain settings and status

In the domain dialog of a service, a **Cloudflare DNS** section appears for hostnames in a connected zone. It reads "NebulaCtrl manages this record in your Cloudflare zone *zone*." and shows:

* **Proxy** — *DNS only* (grey cloud) or *Proxied* (orange cloud). Tunnel domains are always proxied. See [Proxied is the default](#proxied-is-the-default).
* **Status** (live from Cloudflare):

  | Status | Meaning |
  |---|---|
  | `managed` | a record created by NebulaCtrl points to the server or tunnel |
  | `present` | a record you created already points to the right place; untouched |
  | `conflict` | shown as **Conflicting record**, with what exists and where it points: a record NebulaCtrl did not create differs from the wanted one or cannot sit next to it (for example a `CNAME` where an `A` is wanted, or two `A` records). It is never touched by syncs, sweeps or status reads |
  | `missing` | no record exists yet |
  | `wrong_target` | NebulaCtrl's own record drifted; the next sync corrects it |
  | `not_in_zone` | the hostname is in none of the connected zones; NebulaCtrl does not manage it |

* **Actions** — **Create record** (`missing`), **Fix DNS** (`wrong_target`, instead of waiting for the next sync), **Retry sync** (a `managed` record whose last sync failed), **Adopt record** (`present`) and **Replace with NebulaCtrl's record** (`conflict`, the only action for a conflict). Adopt and Replace ask first.

The domain row's DNS chip follows this status for a hostname in a connected zone: `managed` and `present` read **DNS ok**, `conflict` reads **Conflicting record** with what exists, `missing` and `wrong_target` read **DNS pending**. The "Point an A record …" guidance only appears for hostnames outside a connected zone, and the background DNS check does no address lookup for a domain in a connected zone.

The same data is available as `GET /api/v1/domains/{id}/dns` (`manage` is read-only: `true` in a connected zone), `PUT /api/v1/domains/{id}/dns` (`proxied`, `originLock`; any other field, such as `manage`, is rejected with `422`) and `POST /api/v1/domains/{id}/dns/fix` (`adopt`); `nebulactrl domains ls <service>` shows the status column.

### Proxied is the default

A name that **starts being managed** is created proxied (orange cloud): a new domain or page in a connected zone, or an existing one when the integration is connected later and it had no zone yet. Exceptions:

* **wildcard names** start DNS only (Cloudflare proxies wildcards on the Enterprise plan only); the status shows a one-line note;
* **tunnel names** are always proxied `CNAME`s;
* a **private (non-public) server IP** cannot be proxied; the existing warning appears.

The proxy setting stays a choice: switch a domain to *DNS only* at any time. **Existing domains keep their current proxy setting.** A live DNS-only domain is never flipped to proxied. In the API, the CLI, MCP and [nebula.toml](../reference/nebula-toml.md), an omitted `proxied` for a domain in a connected zone means `true` for a name that starts being managed and never changes an existing name. New proxied names get the [origin lock](#origin-lock) by default.

### Proxied records and the SSL/TLS mode

NebulaCtrl serves HTTPS and redirects plain HTTP to it. With a proxied record, Cloudflare connects to your server using the zone's **SSL/TLS mode**:

* **Flexible** or **Off** — Cloudflare talks plain HTTP to your server, which redirects to HTTPS: visitors get a **redirect loop**. NebulaCtrl shows a warning on every proxied domain of such a zone (when the token may read zone settings). Set the zone to **Full (strict)** in Cloudflare → SSL/TLS, or switch the domain to *DNS only*.
* **Full** and **Full (strict)** work. *Strict* needs a certificate Cloudflare trusts at the origin; NebulaCtrl's Let's Encrypt certificates are fine.

Certificate issuance keeps working behind the proxy. For a name in a connected zone NebulaCtrl uses [DNS-01 through the connection](#certificates), which does not involve your server at all. Names outside a connected zone use HTTP-01, which passes through Cloudflare when the zone lets the challenge reach port 80. The *DNS check* of a domain reports "proxied through Cloudflare" instead of comparing addresses, because a proxied name resolves to Cloudflare, not to your server.

A proxied record also needs a publicly routable address: Cloudflare cannot reach a private (LAN) IP, which is flagged as a warning.

### Certificates

For a name in a zone of the connected account (proxied or DNS only, status page hostnames and wildcard hostnames included) NebulaCtrl obtains the certificate with the `cloudflare` DNS-01 resolver automatically, using the connection's token. NebulaCtrl cannot ask a token for its permissions, so it goes by evidence: a name qualifies only after NebulaCtrl's own DNS sync wrote its record with that token (DNS status `managed`, no sync error). Before the first successful sync, for a `present`, `conflict` or `missing` name, and after a DNS write was refused (HTTP 401 or 403), the name stays on HTTP-01 and its certificate line says why; the next successful sync brings DNS-01 back. The resolver is applied with **Deploy proxy** (static proxy configuration; the plan shows a restart), and until then the name keeps HTTP-01. A server needs an ACME email and a DNS-01 capable agent. The agent reports which ACME store holds each host's certificate (no certificate or key leaves it), and the certificate line reads **Certificate via Cloudflare DNS (DNS-01)** with its status only when that is true. A name that already holds an HTTP-01 certificate reads **Certificate via HTTP-01, renews over HTTP-01**, with a line saying that new names use DNS-01: Traefik does not request a new certificate for a name that already has one, the old certificate keeps serving with no gap, and nothing is re-issued by force. See [Names in a connected Cloudflare zone](dns01-wildcard-certificates.md#names-in-a-connected-cloudflare-zone-automatic) for the details. Tunnel domains keep their behaviour (no ACME at the origin).

## Origin lock

A proxied (orange-cloud) domain is meant to be reached **through** Cloudflare: its WAF, rate limits and bot rules only protect the site if visitors cannot skip them by connecting to the server's IP. The origin lock enforces that. It is **on by default for every proxied domain** and needs nothing but the Cloudflare connection above.

### When a domain is locked

| Domain | Locked? |
|---|---|
| Proxied, lock switch on (default) | **yes**, once the DNS record is actually proxied |
| Proxied, but you switched the lock off | no. The domain shows a warning: *Anyone who finds this server's IP can bypass Cloudflare.* |
| DNS only (grey cloud) | no. It must stay reachable directly (a server can host proxied and DNS-only domains side by side) |
| Served through a [Cloudflare Tunnel](#cloudflare-tunnel-per-server) | no. It has no public origin to protect; cloudflared connects from inside |
| Wildcard hostnames, redirect rules | no (the proxy cannot attach a per-host TLS option to a wildcard, and a redirect exposes nothing) |

The lock is decided **per hostname**: when one hostname has several rules (different paths) they all lock, or none does, and your opt-out wins.

The lock follows the DNS record, not just the toggle. NebulaCtrl locks a domain only after its record is proxied (so visitors are never locked out while DNS still points straight at the server) and unlocks it **before** the record is switched back to DNS only.

### The three layers

The domain's DNS panel (Service → Domains → the domain) shows **Origin locked to Cloudflare** with each layer and its state, and the opt-out switch.

1. **IP lock.** The router of the domain answers only connections whose **TCP peer** is in Cloudflare's published IPv4 and IPv6 ranges; every other peer gets `403 Forbidden` with Traefik's plain body (nothing about the server). It is applied per route in Traefik, not with `ufw` or other host firewall rules: Docker-published ports bypass `ufw`, and DNS-only domains on the same server must stay reachable. On port 80 a locked domain only redirects to HTTPS, behind the same lock; Let's Encrypt HTTP-01 validation is answered by Traefik's own ACME router and is never behind the lock.
2. **Authenticated Origin Pulls (AOP).** The TLS option of the domain requires a client certificate (`RequireAndVerifyClientCert`) signed by Cloudflare's origin-pull CA, so a connection that did not come from Cloudflare cannot even finish the handshake. NebulaCtrl turns AOP on for the zone and only then makes the proxy require the certificate. States:
   * **On**: AOP is on in the zone and the proxy requires the certificate.
   * **Unavailable**: AOP cannot be used; the domain stays **locked by IP only** and the panel says why:
     * *the token cannot change zone settings*: add **Zone → Zone Settings → Edit** to the token (the panel links to the token page). The next sync, within ten minutes, turns AOP on;
     * *the zone's SSL/TLS mode is Flexible or Off*: Cloudflare does not connect to the origin over HTTPS, so there is no client certificate to present. Set the mode to **Full (strict)** (the same change also fixes the redirect loop above). The panel shows the live mode, so a fix shows at once and the next sync enables AOP.
   * **Off**: not applied (the domain is not locked, or the zone has not been checked yet).

   *How it is turned on.* Authenticated Origin Pulls with Cloudflare's own certificate is the zone setting `tls_client_auth` ("global" AOP): NebulaCtrl reads `GET /zones/{zone_id}/settings/tls_client_auth` and, when it is off, sets `PATCH /zones/{zone_id}/settings/tls_client_auth` with `{"value":"on"}`: the [Edit zone setting](https://developers.cloudflare.com/api/resources/zones/subresources/settings/methods/edit/) endpoint, as described in Cloudflare's [global AOP guide](https://developers.cloudflare.com/ssl/origin-configuration/authenticated-origin-pull/set-up/global/). The API reference lists **Zone Settings Write** as the permission for that endpoint (the dashboard names it *Zone → Zone Settings → Edit*). It is distinct from *zone-level* AOP with your own certificate (`origin_tls_client_auth/settings`) and from per-hostname AOP; both are out of scope. The setting is zone-wide: it makes Cloudflare present its certificate to every origin of the zone, and origins that do not ask for it ignore it. Turning it on is audited as `cloudflare.zone.aop.enable`; **NebulaCtrl never turns it off.** The state is stored per zone and re-checked at every DNS sync of a locked domain (trusted for at most five minutes; the ten-minute sweep runs one), so switching AOP off in the dashboard is repaired within about ten minutes.

   *The pinned CA.* The proxy trusts exactly one CA for this, Cloudflare's origin-pull CA, embedded in the control plane binary:

   | | |
   |---|---|
   | Source | `https://developers.cloudflare.com/ssl/static/authenticated_origin_pull_ca.pem` (downloaded 2026-10-27) |
   | Subject | `CN=origin-pull.cloudflare.net, OU=Origin Pull, O=CloudFlare, Inc.` |
   | SHA-256 fingerprint (DER) | `9A:1A:C2:B4:BE:15:F9:F2:7E:EE:20:A7:34:CB:A4:E9:89:8F:61:00:1B:3B:D7:C8:4B:69:B5:6A:3E:25:A2:B9` |
   | Valid until | 2029-11-01 |

   To refresh it (a release task), download the file again, replace `internal/integrations/cloudflare/data/authenticated_origin_pull_ca.pem`, read the new fingerprint with `openssl x509 -in <file> -noout -fingerprint -sha256` and update `OriginPullCAFingerprint` in `originlock.go`. A unit test fails when the file and the constant differ, and when the certificate is within 180 days of expiry.
3. **Real client IP.** The entrypoints trust `X-Forwarded-For` **only from Cloudflare's ranges**; from any other peer Traefik discards it before anything reads it. On a locked domain the visitor address is the **last** entry Cloudflare appended (Cloudflare adds the address that connected to it, which is the `CF-Connecting-IP` value; everything to its left is supplied by the visitor and is never read). Your IP allow and deny rules, the rate limit, the access log, [traffic analytics](traffic-analytics.md) and the request-origin map all use that address, while the Cloudflare IP lock checks the TCP peer: two separate checks that look at two different addresses. A `trustedProxyDepth` set on a locked domain is ignored (a larger depth would read a forgeable entry).

   Traefik's IP strategy can read only the TCP peer or `X-Forwarded-For`, so `CF-Connecting-IP` itself is not consulted by the proxy rules; the value is the same by construction. The control plane stores no visitor addresses: analytics keep per-minute counts per country and city computed on the agent from the access log. The control plane's **own** rate limits (sign-in, reset, invites, the public status pages) and audit actor addresses key on the visitor: behind the host's proxy the last `X-Forwarded-For` entry, and when that entry is one of Cloudflare's ranges, `CF-Connecting-IP` ([client addresses](../security/login-methods.md#client-addresses-and-rate-limits)).

### The IP ranges

The ranges are Cloudflare's, kept current without a release:

* A job fetches `https://api.cloudflare.com/client/v4/ips` (no authentication) **every three hours**. If the API fails or its answer is rejected, it falls back to `https://www.cloudflare.com/ips-v4` and `ips-v6`.
* An answer is accepted only when it is plausible: valid CIDRs of the right family, no range wider than `/8` (IPv4) or `/16` (IPv6), at least 10 IPv4 and 5 IPv6 ranges, and **not shrinking either family by more than half** compared with the list in use. Anything else is refused and the **last good list stays in use**.
* A list built into the binary (fetched 2026-10-27) is used on first start and on installs without internet access.
* The list is stored with its hash, source and `fetched_at`; the last error and the first failure of the current streak are recorded. **Team settings → Integrations → Cloudflare** shows the source (Cloudflare API, published text files, built-in copy), the number of ranges, when it was fetched and last changed, any fetch problem, and **Refresh now** (administrators; `POST /api/v1/cloudflare/ips/refresh`). `GET /api/v1/cloudflare/ips` returns the same.
* **When the content changes** the new ranges are pushed to every server that serves a locked domain as a route update, in the same transaction as the change (no redeploy), the change is audited as `cloudflare.ips.updated` (what was added and removed) and teams with a Cloudflare connection get a **Cloudflare IP ranges changed** [notification](notifications.md). Because Traefik reads the trusted forwarded-header ranges from its static configuration, a server whose locked set of ranges changed has its proxy container recreated (about a second of dropped connections, like adding a TCP port); a server without locked domains is not touched.
* When fetching has failed for **24 hours**, teams get a **Cloudflare IP ranges stale** notification (once per outage). Locked domains keep using the last good list; if Cloudflare adds ranges in the meantime, visitors served from them are blocked until a fetch works.

### Turning the lock off for a domain

The **Lock the origin to Cloudflare** switch in the domain's DNS panel (or `PUT /api/v1/domains/{id}/dns` with `{"originLock": false}`) opts one domain out. It asks for confirmation, is applied to the proxy at once, and is audited (`domain.dns.update` with `originLock: false`). While a proxied domain is opted out the panel keeps showing: *Anyone who finds this server's IP can bypass Cloudflare.*

### What the lock does not do

* **It proves "from Cloudflare", not "from your Cloudflare account".** Cloudflare's ranges are shared by every Cloudflare customer, and so is the origin-pull certificate the lock checks: it is the **global Authenticated Origin Pulls CA**, and Cloudflare presents a certificate signed by it for every zone on every account. Someone who learns your server's IP can point a zone of their own at it (with their own plan's settings, for example a different `Host` header) and pass both layers: the lock keeps out the open internet, not another Cloudflare customer. This limit is inherent to the shared CA; it has not been confirmed against a live account for every plan. Where you can, use **per-zone Authenticated Origin Pulls** with your own client certificate, which Cloudflare presents only for your zone: upload a certificate and key in the zone's **SSL/TLS → Origin Server → Authenticated Origin Pulls** (custom certificate) and have your proxy trust that CA instead of the global one. NebulaCtrl does not manage per-zone certificates yet: the lock it renders pins the global CA. Also hide the IP (the Cloudflare Tunnel needs no open port at all) or add a secret header check at the application, if that matters.
* Non-HTTP routes (TCP/UDP) are not covered; restrict them with their own allowed CIDRs.
* DNS-only domains on the same server stay reachable directly by design.
* The lock does not change certificate issuance: for a name outside a connected zone HTTP-01 passes through Cloudflare only if the zone lets the challenge reach port 80 (an *Always Use HTTPS* rule or an SSL mode that contacts the origin on :443 can prevent it); use a [DNS-01 resolver](dns01-wildcard-certificates.md) for such domains. Names in a connected zone use [DNS-01 automatically](#certificates).

## Failure modes

| Symptom | Cause and fix |
|---|---|
| Connect fails with *Cloudflare rejected the token* | wrong token, expired, or it lacks Zone:Read. Create a new token with the permissions above |
| Status *not in zone* | the token cannot see the zone (add it to the token's Zone Resources, then **Test connection**) |
| Domain error mentioning a permission | the token has Zone:Read but not DNS:Edit on that zone |
| Certificate line says *Waiting for the first successful DNS sync in this zone* | DNS-01 starts after NebulaCtrl's own sync wrote the record (status `managed`). A record someone else created (`present`), a `conflict` or a `missing` record does not count: adopt or replace it, or wait for the sync |
| Certificate line says *Cloudflare refused the last DNS change* | the write was refused (401/403): the token lacks **Zone → DNS → Edit** on that zone. The name uses HTTP-01 until a sync succeeds again |
| Certificate line says *Certificate via HTTP-01, renews over HTTP-01* | the name already had an HTTP-01 certificate; it keeps it and renews it over HTTP-01. DNS-01 issues for names without a certificate |
| Connection card shows an error | Cloudflare returned 401 for the stored token; paste a new token. Records are not changed while the token is rejected |
| `conflict` | a record NebulaCtrl did not create differs from the wanted one or blocks it. It is left alone until you press **Replace with NebulaCtrl's record**, or remove it in Cloudflare yourself |
| `wrong_target` | NebulaCtrl's own record drifted; the next sync corrects it (or press **Fix DNS**) |
| Redirect loop on a proxied domain | zone SSL mode is Flexible/Off, see above |
| A record is still in the zone after deleting a domain | the cleanup runs within seconds, or at the next sweep; it never deletes a record whose marker was edited away |
| Origin lock shows *Authenticated Origin Pulls unavailable* | the token lacks **Zone → Zone Settings → Edit**, or the zone's SSL mode is Flexible/Off; the domain is locked by IP only until fixed |
| A proxied domain answers `403 Forbidden` from a browser that goes through Cloudflare | the visitor was served from a Cloudflare range the stored list lacks: check **Cloudflare IP ranges** (age, last error) and press **Refresh now** |
| `525` (SSL handshake failed) from Cloudflare on a locked domain | AOP is off in Cloudflare while the proxy requires the certificate; the next sync (within ten minutes) turns it on again, or switch the domain's lock off while you investigate |
| *Pending* next to the lock | the DNS record is not proxied yet; the lock follows once it is |

## Tokens and rotation

Replace the token in Team settings → Integrations at any time (paste the new one; the old one stops being used immediately). Revoke the old one in Cloudflare afterwards. NebulaCtrl never needs a Global API Key.

## Cloudflare Tunnel per server

A tunnel lets Cloudflare reach a server without any inbound port: **ports 80 and 443 may stay closed**. Enable it on **Servers → (server) → Proxy → Cloudflare Tunnel**. It acts at once and is not staged for *Deploy proxy*.

**Needs:** the Cloudflare connection with an account selected, a token that also has **Account → Cloudflare Tunnel → Edit** (a token without it fails with `cloudflare_tunnel_forbidden`), an agent that advertises the `tunnel.cloudflared` capability, and the `servers:write` + `proxy:write` scopes (administrators).

**IP rules do not work behind a tunnel.** The connector is a container on the `nebulactrl-edge` network, so the proxy sees its private Docker address, never the visitor's. An IP **allow** list on a domain served through the tunnel therefore cannot tell visitors apart, and it is not enforced loosely: such a domain answers `403` to everyone until the rule is removed, and saving an IP rule (allow or deny) on an already-tunnelled domain is refused with `422 ip_rules_on_tunnel`. A platform service such as Headscale is never reachable through a tunnel. Protect a tunnelled hostname with Cloudflare Access in front of it, or serve it without the tunnel and use the [origin lock](#origin-lock) with IP rules.

**What happens when you enable it**

1. NebulaCtrl creates a remotely-managed tunnel in your account (`nebulactrl-<server>-<id>`) and pushes its ingress rules: every hostname of the server's services goes to `http://nebulactrl-traefik:80` with that hostname as the Host header, then a final `http_status:404` catch-all. The rules follow the server's domains (checked every minute and after the connector connects).
2. The agent runs `cloudflare/cloudflared` (pinned by digest) as `nebulactrl-cloudflared` on the `nebulactrl-edge` network, restart `unless-stopped`, no published ports, all capabilities dropped. Its token is fetched from the Cloudflare API when the command is sent and written to a file in the container; it is never stored in the database, in a command payload or in an environment variable.
3. When the connector reports its first connection the tunnel is **active**: domains of this server in connected zones get a proxied CNAME `<tunnelId>.cfargotunnel.com` instead of A/AAAA records. Until then DNS is unchanged.
4. For those domains the proxy skips HTTP-01 (Cloudflare terminates the public HTTPS; the origin keeps Traefik's default certificate, or the domain's DNS-01 resolver or a covering wildcard) and serves port 80 directly instead of redirecting to HTTPS, which would loop.

**Status** shows in the server's proxy tab and on the server card (connected, connecting, offline, needs attention) with the number of connections, taken from the connector's `/ready` endpoint and reported with every heartbeat. A missing or stopped connector on an online server is re-created automatically (at most every five minutes); *needs attention* (for example a rejected token) waits for **Re-sync**.

**Disabling** points the domains back at the server's public IP, removes the connector and deletes the tunnel at Cloudflare about a minute later. Open ports 80 and 443 before the DNS change propagates. If Cloudflare refuses the delete (token lost the permission), the state stays *Removing* with the reason; fix the token and retry, or **Force remove** to drop the record (delete the tunnel in the Cloudflare dashboard yourself). Revoking a server tears its tunnel down the same way. A Cloudflare connection cannot be removed while a server still has a tunnel.

**Notes:** the connector's metrics port (20241, `/ready` and counters) is reachable from containers on `nebulactrl-edge`, not from outside. Domains outside connected zones keep their own DNS and certificates even when the tunnel is active. Hosts that cannot route to container IPs (Docker Desktop) report the connection count as unknown and use the container's health instead.
