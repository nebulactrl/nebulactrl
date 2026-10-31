---
title: Domains and proxy routes
description: "Publish services and other upstreams on a domain; TCP and UDP routes; per-domain options."
section: Applications
order: 70
---

# Proxy routes: route services, domain options, TCP/UDP

Every server runs its own Traefik (managed by the agent). This page covers what you can publish through it beyond "one hostname → one service": route services (external upstreams and TCP/UDP exposure), per-domain options, and how all of it deploys. Server-level settings (uploaded certificates, the upstream policy, the tailnet) are in [proxy-tailnet-certificates.md](proxy-tailnet-certificates.md).

Everything here is **staged** like any other service change: it shows up as a pending change and takes effect on the next deploy. Writes need the `proxy:write` scope (session users hold it; MCP token presets never get it) in addition to `projects:write`. A server whose agent is too old to render these features answers `422 agent_too_old` instead of silently ignoring them.

## Route services

A route service (`kind: route`) is a canvas node without a container. It has a server, a protocol and one or more upstreams:

| Protocol | What it does |
|---|---|
| `http` | Publishes an upstream NebulaCtrl does not run (a NAS on the LAN, a port on the host, a tailnet device) under one or more hostnames, with the full set of domain options below. |
| `tcp` | Exposes `listenPort` of the server and forwards to the upstream, optionally restricted to client CIDRs and with the PROXY protocol. |
| `udp` | Same for UDP. There is no per-client restriction (see below). |

An upstream is either an external `host:port` or a managed service of the same environment (`serviceId` + `port`) on the **same server**; the latter resolves to that service's current containers, so "expose my Postgres on port 25432" keeps working across deploys of the database.

```
POST /environments/{environmentId}/routes     create (Idempotency-Key supported)
GET|PATCH|DELETE /routes/{routeId}            PATCH needs If-Match (service generation)
GET /servers/{serverId}/ports                 TCP/UDP ports held by route services
```

The protocol and the server of a route cannot change; delete and recreate it instead. `DELETE /services/{id}` does the same as `DELETE /routes/{id}`: the listen port is freed and, for a deployed route, the remaining table is pushed to the server in the same transaction, so the proxy stops publishing it. Restart, stop, logs, volumes and scaling do not apply to a route (`422 not_applicable`). Server drains skip route services.

Templates that publish a port (for example a game server) create the same kind of route service.

### Listen ports

TCP/UDP ports are static Traefik entrypoints on the host. A listen port is 1024-65535 and never `80`, `443` or the proxy's internal probe/admin ports (`422 invalid_listen_port`). A port is held per server **and protocol**; a second route asking for the same one gets `409 port_in_use` naming the holder, even before the first is deployed.

**Adding a new TCP/UDP port restarts the proxy on that server** (the agent recreates the Traefik container with the extra published port). Expect about a second of dropped connections, and HTTP is briefly unavailable too. The deploy plan says so (`New TCP port N restarts the proxy on <server> ...`). If the new container fails to start (for example the host port is taken by something else), the agent puts the previous container and routes back and the deployment fails with a message naming the port. Changing only upstreams or CIDRs does not restart anything.

### Security notes for TCP/UDP

- **Docker bypasses ufw/firewalld.** Published ports are opened by Docker's own iptables rules, so a host firewall rule such as `ufw deny 25432` does *not* protect the route. Use `allowCidrs` (and `denyCidrs`) on the route, or a firewall in front of the server (cloud firewall, VPN).
- A TCP route needs either `allowCidrs` or an explicit `openToInternet: true`; the plan warns when it is open to everyone. `denyCidrs` can be added to either (see [IP rules](#ip-rules-allow-and-deny)).
- **UDP has no IP rules.** Traefik has no UDP middleware, so `allowCidrs`, `denyCidrs` and `proxyProtocol` are rejected for UDP (`422 invalid_config`). Put UDP behind a firewall or VPN if it must not be public.
- **Public database ports** are TCP routes with a mandatory allow-list: see [Managed databases](databases.md#public-port).

### Upstream policy

External upstreams are checked against the server's upstream policy twice: by the control plane when you create or edit the route (`422 upstream_denied`) and by the agent when it applies the table (so a stale or hand-edited table cannot bypass it). By default:

- denied: unspecified addresses (`0.0.0.0`), loopback (`127.0.0.0/8`, `localhost`), link-local (`169.254.0.0/16`, which includes cloud metadata endpoints such as `169.254.169.254` and `metadata.google.internal`) and the other metadata addresses (`100.100.100.200`, `fd00:ec2::254`), the proxy's own containers (`nebulactrl-*`) and the proxy admin port;
- allowed: LAN addresses, CGNAT/tailnet addresses (`100.64.0.0/10`), public addresses, DNS names, and `host.docker.internal` (the Docker host, enabled by default per server).

Admins can allow loopback and link-local per server (Server → Proxy settings). The control plane validates against the policy the agent has *applied*, so a loosened policy takes effect for new routes after "Deploy proxy" on that server.

**DNS names are judged by what they resolve to.** The control plane resolves the name when you save the route and refuses one that points at a denied address; a name that resolves to a private address (RFC 1918, unique-local, CGNAT) can only be saved by a team admin. A name that only the server can resolve (a LAN name) is accepted there. The agent then resolves every name on each apply and each time it re-pushes the table (after a restart or a rollback), judges the addresses against the policy and gives Traefik the **resolved addresses**, not the name, so a name that answers differently later (DNS rebinding) never reaches a denied address. A change of a name's address reaches the proxy with the next apply. If a name does not resolve on the server, the apply is refused. The exceptions are the single-label and `*.ts.net` names of a server with managed-tailnet MagicDNS, which only the proxy container resolves. An upstream name that starts with `nc-` is refused: it is a managed container, choose the service as the target instead. The route's uptime probe is held to the same rule.

With `passHost: false`, or an https upstream without `transport.serverName`, all upstreams of the route must use one hostname: the agent sends that name as the `Host` header and the TLS server name of the pinned addresses.

## Domain options

Every domain of a service or an http route service has a routing rule. Edit it with `PATCH /domains/{domainId}` (`If-Match` = the owning service's generation; the response carries the new ETag). A plain hostname change needs only `projects:write`; everything else needs `proxy:write`.

| Field | Meaning |
|---|---|
| `pathPrefix` | Serve only this path and below, e.g. `/api`. The same hostname can carry several domains with different prefixes; `(hostname, pathPrefix)` is unique (`409 domain_conflict`). |
| `stripPrefix` / `addPrefix` | Remove the prefix before forwarding / prepend a path before forwarding. |
| `kind: redirect` | Answer with a redirect to `redirectTo` (a hostname, not a URL) with `redirectCode` 301/302/307/308 (default 308; Traefik only distinguishes permanent from temporary, so 301/308 render a permanent redirect, which answers 301 to a GET and 308 to other methods, and 302/307 a temporary one: 302 for a GET, 307 otherwise), keeping path and query when `preservePath` (default). A redirect domain takes no options, auth or prefix rewriting. The target must differ from the domain itself and must not close a cycle with the workspace's other whole-host redirects (`422 redirect_loop`). The classic use is `www.example.com` → `example.com`. |
| `wwwRedirect` | `true` on a whole-host `serve` domain (no path prefix) keeps a **www ↔ apex redirect** for it: the counterpart hostname (`www.<host>`, or the host without its `www.`) is routed as a whole-host `kind: redirect` domain with code **301** and `preservePath`, pointing at this domain. So `example.com` with the option on also answers `www.example.com` with a 301 to `https://example.com/…`; on `www.example.com` it redirects the apex to `www`. Both hostnames are routed, so the proxy requests a certificate for each (the redirect copies the domain's `tlsMode`, pinned certificate and DNS-01 provider; an uploaded certificate must cover both names, `422 certificate_mismatch`), and each needs its own DNS record: both rows show their DNS status and, outside a connected Cloudflare zone, the "Point an A record for … to <ip>" guidance (in a connected zone NebulaCtrl creates the record and the row shows the Cloudflare status, see [Cloudflare](cloudflare.md)). The redirect is an ordinary domain row (it shows in the list, in pending changes and in release snapshots, and is rolled back or discarded like any domain). It follows the domain's rename and TLS settings; `false` removes it and deleting the domain deletes it. Domain views carry `wwwPartner` (the counterpart name; empty for wildcards, redirects and path-prefixed domains), `wwwRedirect` (the redirect exists) and, on the redirect row, `wwwRedirectFor`. `422 invalid_www_redirect` for an ineligible domain, `409 domain_conflict` when the counterpart is already taken. Needs `proxy:write`, like every redirect. UI: the **Redirect www.… to this domain** switch in the domain editor, and **Also route … and redirect it here (301)** when adding an apex or `www.` domain. CLI: `nebulactrl domains add --www-redirect`, `nebulactrl domains www <service> <domain> on|off`. |
| `tlsMode` | `auto` (ACME when the server has an email and the name is public, otherwise the default or an uploaded certificate; for a name in a zone of the connected Cloudflare account the certificate comes through the `cloudflare` DNS-01 resolver once NebulaCtrl's own DNS sync has written the name's record and **Deploy proxy** has applied the resolver, see [DNS-01](dns01-wildcard-certificates.md#names-in-a-connected-cloudflare-zone-automatic)), `custom` (never ACME: an uploaded certificate selected by SNI, else the default one), `none` (plain HTTP only, no redirect). |
| `dnsResolver` | The DNS-01 provider that issues this domain's certificate (a resolver the service's server already has, [DNS-01](dns01-wildcard-certificates.md)); empty means HTTP-01. For a name in a zone of the connected Cloudflare account NebulaCtrl names the `cloudflare` resolver itself, whatever is set here, once its own DNS sync has written the name's record (status `managed`, no sync error) with the connection's token, and the server has an ACME email and a DNS-01 capable agent; before that, and after a refused DNS write, the name keeps this setting (HTTP-01 when empty) and the domain's certificate line says why. A name that already holds an HTTP-01 certificate renews it over HTTP-01. A `cloudflare` resolver staged by hand on the server takes the place of the automatic one. Tunnel domains are unaffected. |
| `certificateId` | Pin an uploaded certificate (only with `tlsMode: custom`). It must exist on the service's server and cover the hostname, wildcards matching exactly one label (`422 certificate_mismatch`). Traefik still selects by SNI; the pin is validation and documentation. |
| `options` | The typed middleware set, validated as a whole (`422 invalid_options`): `forceHttps` (default true), `compress`, `noindex`, `securityHeaders` (`basic` or `strict`, which adds HSTS), `requestHeaders`/`responseHeaders`, `cors`, `redirects` (regex), `rateLimit`, `access` (IP allow and deny lists with optional trusted-proxy depth for CDNs, see [IP rules](#ip-rules-allow-and-deny)) and `basicAuth`. |

Headers that must not be forged (`Host`, `X-Forwarded-For`, `Set-Cookie`, hop-by-hop headers, ...) are rejected. Path prefixes are `/segment[/segment]` without trailing slash.

### IP rules: allow and deny

A domain (`options.access`) and a TCP route (`allowCidrs`, `denyCidrs`) take two lists of CIDRs or single addresses (IPv4 and IPv6, at most 200 each). They are evaluated **deny first**:

1. a client that matches a `denyCidrs` entry is refused, even when the allow list would admit it;
2. otherwise a client is admitted when it matches an `allowCidrs` entry;
3. an **empty allow list means everyone not denied** (so a deny list alone blocks a bad range and leaves the rest open). On a TCP route that is only accepted together with `openToInternet: true`; on a database public port the allow list is never empty.

Traefik has no deny middleware, so the agent renders both lists as **one `ipAllowList`**: the allow list (or every address when it is empty) with the denied ranges subtracted (the allowed ranges are split around them into CIDRs). The decision is identical to the ordered rules above. A rule set that admits nobody (for example `allow 10.0.0.0/8, deny 10.0.0.0/7`) is rejected (`422 invalid_options` for a domain, `invalid_config` for a route), as is a deny list that splits the allowed space into more than 4096 networks (for example more than 32 single IPv6 hosts carved out of `::/0`, or about 128 single IPv4 hosts out of `0.0.0.0/0`); use broader entries. The web UI shows the ordered effective rules next to the lists. `trustedProxyDepth` (domains) picks the client address behind a CDN; rejected clients get `rejectStatus` (default 403) on a domain, and the connection is closed on a TCP route. The post-deploy verification probe skips access rules.

On a domain locked to Cloudflare (the origin lock, see [Cloudflare](cloudflare.md)) the rules are evaluated against the **real visitor address**: the entrypoints trust `X-Forwarded-For` only from Cloudflare's ranges, and `ipStrategy.depth: 1` picks the last entry, the address Cloudflare saw connect. Entries to its left are whatever the visitor sent and are never read, so `trustedProxyDepth` (access and rate limit) is ignored on such a domain. The lock itself is a separate `ipAllowList` on the direct peer address (Cloudflare's ranges, no `ipStrategy`), runs first and answers a peer outside the ranges with a plain `403 Forbidden`. Traefik's `ipStrategy` can only read the peer address or `X-Forwarded-For`, so `CF-Connecting-IP` is not used; Cloudflare puts the same address at the end of `X-Forwarded-For`. A request from a Cloudflare address without any `X-Forwarded-For` has no visitor address and is refused when a user rule is set.

**Clients on the host and the LAN.** An allow list that judges the connecting address (a TCP route's, and a domain's with no trusted-proxy depth) is applied as you wrote it. A client on the server itself (`127.0.0.1` through Docker's port publishing) or on the LAN behind the server's NAT arrives as the Docker bridge gateway, so a rule that allows the private ranges admits it. What stops the internet from arriving as a private address is not the rule but the listener: the public ports (80, 443 and TCP routes) are published on IPv4 only, so no IPv6 client is relayed as the network gateway; they are published on both families only when the `nebulactrl-edge` network has an IPv6 subnet (then Docker keeps the real source address). An IP allow list on a domain behind a Cloudflare Tunnel is refused, because the connector would be the peer ([Cloudflare Tunnel](cloudflare.md#cloudflare-tunnel-per-server)).

### Basic auth

```
PUT    /domains/{domainId}/auth-users             [{username, password?}]  replaces the user set
DELETE /domains/{domainId}/auth-users/{username}
```

Passwords are hashed with bcrypt (cost 10) and the hash is stored sealed; responses carry user names only. A user that already exists may omit the password to keep it. Enable the protection with `options.basicAuth: true` *after* adding users (`422 basic_auth_needs_users` otherwise); the last user cannot be removed while it is on. The hashes never appear in route tables, deploy payloads, audit entries or logs: they are attached to the command as `auth:<domainId>` secrets when it is sent and the agent keeps them only in its own route table. A rule with basic auth enabled and no resolvable users is a hard error on the agent: it never renders an unprotected route.

### Pending changes

Domain edits appear in the pending changes as `domains.<host><path>` (added/removed) and `domains.<host><path>.<field>` for `stripPrefix`, `addPrefix`, `kind`, `redirectTo`, `redirectCode`, `preservePath`, `tlsMode`, `certificateId`, `options` (compact JSON) and `authUsers` (names). Discarding restores the released value, except `authUsers` (passwords are not kept).

## How a route deploys

A **regular service** deploys normally; its payload also carries the per-domain rules and the server's applied proxy configuration. Domain edits therefore apply with the service's next deploy (there is no fast path that only reloads routes for a regular service: container names are tied to the current deployment).

A **route service** deploys *route-only*: the deployment has strategy `route_only`, nothing is built, pulled or started. The release snapshots the route config and its domains, and one `sync_routes` command carries the server's full route table (including this route, built from the new snapshot) and the proxy configuration. The agent applies it, reports `route` and `verify` events and a result like any deployment; for an http route it probes the hostname through the proxy and treats status < 500 as routed (set `verifySkip` for upstreams that legitimately answer 5xx). Verification checks routing, not access rules.

- Plan warnings: an external http route without basic auth or an IP allowlist ("this route has no access control" and "verification checks routing, not access rules"), TCP open to the internet, UDP without allowlist, Docker bypassing ufw, and the proxy restart for a new port.
- Plan blocking: `agent_too_old`, an http route without a domain (`route_needs_domain`), a managed upstream whose service is not deployed on the route's server (`upstream_not_deployed`).
- Rolling back a route re-sends the table of the old release. Deleting a deployed route pushes the remaining table.
- When a regular service is deployed, routes that target it are rewritten to its *new* container names in the table the agent applies after the health check, so exposed ports follow the deploy. *Deploy staged* orders routes after the services they point at.
- A reconnecting agent receives the full table (services, external routes, proxy configuration and the auth/certificate secrets) in its reconcile frame and re-applies it.

## Limitations

- Staged options apply on deploy; basic-auth users apply live with the next route push (any deploy, scale or route sync on that server) because they are resolved at send time from the current users.
- A new TCP/UDP port restarts the proxy briefly; there is no hot-add of listeners.
- A route can only target managed services on its own server; cross-server upstreams are external addresses.
- Deleting a managed service does not delete route services that target it: the route drops out of the table (no upstream) until the target is deployed again or the route is removed.
- Basic-auth passwords cannot be recovered or discarded back; set them again.
- The control plane cannot reach upstream addresses itself: reachability is verified from the server by the agent's post-deploy probe (http only).
