---
title: 0003 Traefik proxy
description: "Why every host runs Traefik v3, managed by the agent."
section: Internals
order: 3
---

# 0003. Reverse proxy: Traefik v3, managed by the agent (REST provider)

- Status: accepted (Caddy was the alternative considered first, see Alternatives)
- Date: 2026-10-26

## Context

Domain routing needs automatic HTTPS, failure-safe configuration updates, preserved old routes when a new target is unhealthy, WebSocket compatibility and hostname conflict detection. The choice was between Caddy and Traefik. Each host has its own proxy; the proxy is managed infrastructure, not a user service.

## Decision

Every enrolled host runs **Traefik v3**, created and owned by the agent as a Docker container. Services never publish host ports; only Traefik does. Routes are pushed as one complete dynamic configuration through Traefik's **REST provider**.

**Bootstrap (at agent start, idempotent).**
- network `nebulactrl-edge`; volume `nebulactrl-traefik-acme` (mounted at `/data`, holds `acme.json`);
- container `nebulactrl-traefik` from `traefik:v3.7.16@sha256:c8a121e4bb1347e8fd52657fab12f2cb70b68f8a018895e2068f92c9e519b9c2` (v3.6.17 / v3.7.1 and later carry the fix for GHSA-96qj-4jj5-wcjc, the `rest@internal` exposure), restart `unless-stopped`, json-file logs rotated at 10 MB × 3;
- static configuration as command flags:
  - entrypoints `web` `:80`, `websecure` `:443` (HTTP/3 on 443/udp), `probe` `:8099` (never published) and `ncadmin` `:9000`;
  - `--providers.rest=true`, `--providers.file.filename=/etc/traefik/bootstrap.yml` (static bootstrap only), `--ping` with manual routing, the API without dashboard;
  - `gracetimeout=10s` on the public entrypoints;
  - the ACME resolver `le` (HTTP-01 on `web`, storage `/data/acme.json`) only when the server has an ACME email;
  - the JSON access log on stdout (read by the agent's traffic collector) unless the server's traffic analytics setting is off; Traefik's own log is JSON on stderr;
- ports `<--proxy-http-port>:80`, `<--proxy-https-port>:443` and `…:443/udp`. The admin entrypoint is published as `127.0.0.1:<--proxy-admin-port>:9000` for a host-installed agent; an agent running in a container on the edge network reaches `http://nebulactrl-traefik:9000` and nothing is published;
- the bootstrap file is written into the container with `CopyToContainer` **before its first start**, so there is no window without the admin router. It defines the router `nc-admin` (entrypoint `ncadmin`, `rest@internal`, `basicAuth` user `nebulactrl-agent`), a read-only `nc-api` router (`api@internal`, same auth) and a liveness router on `probe`. The password is a random 32-byte secret the agent generates once and keeps in its bbolt store; only its bcrypt hash is in the container. `providers.rest.insecure` is never set: it would expose an unauthenticated write endpoint to every container on the edge network. Other containers get 401.
- If Traefik cannot start (for example port 80 is taken) the heartbeat reports `proxy.status="error"` with the reason, the server view shows it, and creating a domain on that server returns 422 `proxy_unavailable`.

**Configuration model.** The agent holds the whole route table and pushes the **full** dynamic configuration with `PUT /api/providers/rest`. No incremental patching. For each service it emits:
- router `nc-<serviceId>-https` on `websecure`, rule `Host(…) || Host(…)` for all hostnames, with TLS. Names that can be issued a public certificate use `certResolver: le` (only when an ACME email exists); `*.localhost`, `*.local`, bare names and IPs use Traefik's default certificate. A service that mixes both kinds gets a second router `…-https-local`;
- router `nc-<serviceId>-http` on `web`, a `redirectScheme` middleware to https (with the public HTTPS port when it is not 443). Traefik answers ACME HTTP-01 challenges before routers;
- router `nc-<serviceId>-probe` on `probe` with the same rule, no TLS and `observability.accessLogs=false`;
- service `nc-<serviceId>` as a load balancer with the single server `http://<containerName>:<port>` over the edge network and `passHostHeader`. The container name is unique per deployment, so old and new releases coexist;
- one generation marker router `nc-gen` on `probe`, rule ``Host(`gen-<n>.nc.invalid`)``, to `ping@internal`.

Hosts that match no router get Traefik's default `404`, so an unrouted host cannot pass verification.

**Synchronous apply on an asynchronous provider.** Traefik applies a pushed provider configuration asynchronously and a `200` from the REST endpoint only means "decoded". `Apply` therefore returns only when the new table is live or has failed:
1. `PUT` the configuration (basic auth). A non-2xx answer is an error.
2. Poll the probe entrypoint with `Host: gen-<n>.nc.invalid` until it answers 200 (at most 15 s). The generation number grows monotonically (millisecond clock).
3. Read all routers from the authenticated admin API. Traefik loads a config it can parse even when it cannot build part of it (a bad upstream URL, an invalid rule); such routers are `disabled` with an error. Any disabled router of this generation fails the apply.
4. On any failure the agent pushes the previous table again (a fresh generation) and returns the error. The in-memory and bbolt copies of the last-applied table change only after success.

The agent validates hostnames, upstreams and ports before building the configuration, so step 3 is a safety net rather than a normal path. Because the REST provider replaces the whole configuration, the routes of a rejected generation are absent until the restore completes (measured at roughly 0.3 s).

**Static versus dynamic settings.** Route changes never restart Traefik. The ACME email and the access-log toggle are static flags: when either changes, the agent recreates the container (and then pushes the table again). The container carries a spec label (a hash of image, flags, ports, log options and the admin secret) so an unchanged configuration is never recreated.

**Liveness.** The REST provider keeps its state in Traefik's memory only. The agent checks every 5 s that the marker of the live generation still answers; if Traefik restarted, the marker is gone and the agent bootstraps again and pushes the table.

**Verification before commitment.** After a new container is healthy and the table is live, the agent sends `GET http://<traefik-ip>:8099<healthPath>` with `Host: <first hostname>` and expects a status below 500 (3 tries, 2 s apart; the container IP on the edge network, falling back to the `nebulactrl-traefik` DNS name). The probe routers have the same rules and services as the public ones, so this tests the exact table production traffic uses without DNS or public ports. On failure the agent pushes the previous table and removes the new container.

**Conflicts.** Hostnames are globally unique in the control-plane database (409 `domain_conflict`); because services never bind host ports, a hostname clash is the only routing conflict.

## Consequences

- One whole-document apply is easy to reason about, makes the route table reproducible from the control plane after a restart, and lets the reconciler re-apply it on reconnect.
- The proxy is a Docker container the agent must be allowed to create; it needs ports 80/443 free and the admin port (default 9000 on 127.0.0.1). It restarts with the host through Docker's restart policy; the agent re-pushes the table.
- Config reloads never close existing connections (Traefik hot reload), so rolling swaps keep serving (measured: 0 errors over 2,281 requests with the old upstream stopped after each swap). WebSocket connections to the old container end when that container is stopped.
- The probe entrypoint shares Traefik's process, so a verify pass shows routing is correct but not that DNS or the public network path is.
- Unlike Caddy's `/load`, a bad configuration is not refused up front; the agent detects it through router status and restores the previous table, with a short gap for the affected routes.
- A change of ACME email or of the traffic-analytics setting restarts the proxy container briefly.
- Local names use Traefik's default self-signed certificate (browsers warn), there is no internal CA.
- Cross-host traffic is explicitly out of scope: each host's Traefik only routes to local containers.

## Proxy v2 (agent side)

The agent renders more than host routers. The wire contract is `pkg/agentprotocol/proxy.go`; the agent code is `internal/agent/proxy`.

- **Per-domain routers.** A route carries `DomainRule`s. Each rule renders `nc-<serviceId>-d<domainId8>-https|http|probe` (redirect domains: `-redir` and `-http`) with the rule ``Host(`h`) [&& PathPrefix(`/p`)]``. Hosts without a rule use the plain per-service names (`nc-<serviceId>-https|-http|-probe`). The middleware chain is ordered cheapest rejection first: `ipAllowList → rateLimit → basicAuth → redirectRegex → stripPrefix → addPrefix → headers → compress`. The probe router keeps only the prefix rewrites, so verification checks routing and the upstream, not access rules. A basic-auth rule without resolved users is a hard error: the table is never applied with an unprotected domain.
- **Deny lists are subtraction.** Traefik has no deny middleware (only `ipAllowList`), so `Access.DenyCIDRs` and `Route.DenyCIDRs` are rendered into the same one `ipAllowList` as (allow list, or every address when it is empty) minus the denied ranges (`agentprotocol.EffectiveAllow`, CIDR arithmetic on `netip`). That gives exactly "deny first, then allow, default deny when an allow list exists". Rule sets that admit nobody or expand beyond 4096 networks are refused by both the control plane and the agent. Without a deny list the allow list is rendered as written.
- **Secrets stay out of payloads.** bcrypt lines (`auth:<domainId>`) and certificate keys (`cert:<id>`) arrive in `Command.Secrets`; `ResolveSecrets` fills the agent-side fields. The resolved table and the certificates (with keys) live in the agent's bbolt file (mode 0600), so a restart converges offline. A missing secret fails the command before anything is applied.
- **Custom certificates** go into the same REST document as inline PEM (`tls.certificates[].certFile/keyFile`): no files, no restart (verified on Traefik v3.7.16). A pair that does not parse is excluded and reported in `ProxyConfigureResult.Certificates`. A name covered by an uploaded certificate is served from the certificate store even on an `auto` domain; ACME stays for names no certificate covers.
- **TCP/UDP routes** need static entrypoints (`tcp-<port>`, `udp-<port>`) and published host ports, so the container is recreated when the set of listen ports changes (about one second of dropped connections; HTTP routes are rebuilt immediately after). A container that cannot start (a port already in use) is removed and the previous container is created again with the previous table and configuration; the error names the port. The host-gateway entry (`host.docker.internal:host-gateway`, upstream policy) and the tailnet resolver (`100.100.100.100`, MagicDNS) are static settings of the same kind. `Apply` waits for tcp and udp routers through `/api/tcp/routers` and `/api/udp/routers` as for http.
- **Upstream policy** is enforced by the agent as well as the control plane, for external routes and every tcp/udp route: loopback, link-local (cloud metadata) and the proxy's own containers are denied unless an administrator allows them per server. Names are also resolved best effort; a name that resolves to a denied address is rejected, a resolution failure is allowed. UDP routes cannot carry an allowlist (Traefik has no UDP middleware), and the agent refuses a udp route that has one.
- **Tailnet egress** runs as a second container, `nebulactrl-tailscale`, in kernel mode inside the proxy container's network namespace (`network_mode: container:nebulactrl-traefik`, `/dev/net/tun`, `NET_ADMIN`/`NET_RAW`, no privileged mode, no docker socket, identity on the `nebulactrl-tailscale-state` volume). It is recreated whenever the proxy container is, because the namespace belongs to the proxy; the identity survives on the volume, so no new auth key is needed. A one-time auth key is held in memory only and dropped after the apply that used it. Disabling removes the container and volume; an agent that merely lost its state never deletes a node. Status comes from `tailscale status --json` and travels in heartbeats.

Not verified against a real tailnet.

## Maintenance and error pages

Traefik cannot serve files, so a second managed container, `nebulactrl-pages`, serves the maintenance page and the automatic 404/502/503/504 pages. See [operations/maintenance-and-error-pages.md](../operations/maintenance-and-error-pages.md).

- **Image.** The official `nginx` on Alpine, `slim` variant, stable branch (`1.30.6-alpine-slim`), pinned by index digest. Alternatives weighed: Caddy (~60 MB, a second ACME-capable proxy we would have to keep from touching certificates), `busybox httpd` (cannot set a status code with a file body or per-file headers), `traefik/whoami` or a purpose-built Go image (an image of our own to build, sign and publish). nginx answers a status with an `error_page` file and per-location `add_header ... always`, which is exactly the need, at about 5 MB.
- **Wiring.** An `errors` middleware (`502-504`, query `/err/{status}`) leads the chain of every service router; a `priority: 1` catch-all on `web`/`websecure` (`tls: {}`) rewrites unknown hosts to `/err/404`. Maintenance repoints a service's *serving* routers to the pages service behind a `replacePath` to `/m/<serviceId>`; probe routers stay on the real service, so verification keeps checking the app. The flag is `Route.Maintenance` and travels with the ordinary route table (`sync_routes`, reconcile).
- **Ordering.** Maintenance content is written before a push that needs it and removed after a push that no longer does.
- **Deploys inherit.** A deploy payload's route table is older than any maintenance toggle since dispatch, so the engine takes the flag from the proxy's live table.

## Who owns a service's upstreams

A route table from the control plane (`sync_routes`, a replacing `proxy_configure`) is built from its database when the command is queued. The database learns of a new deployment or replica count only when the agent's result arrives, so a table queued for any other reason (a domain, a maintenance toggle, another service) in that window is older than what the agent has done. Applied as sent it either names the *previous* containers of a service that a rolling deploy or scale has just switched (they go back into the upstreams and the retire step stops them right after: a sustained 502), or lacks the route of a service whose first deployment has just been routed (a 404 page, and the proxy leaves the service's network: 502 until the next push).

The agent therefore keeps the live upstreams, networks and health path (one unit with the replica set) of every managed route whose service has a command running, or was deployed or scaled here to a different replica set than the table names, and keeps such a service's live route when the table lacks it. Hosts, domains and maintenance in a route the table does carry are applied as sent. A deployed service's routes change through its own deploy, scale and remove commands, never by absence from a table; this is the rule Reconcile already applied to its own frames. External upstreams and services the agent knows nothing about stay the control plane's. See `keepLiveRoutes` in `internal/agent/engine/proxyconfigure.go`.

## Whole tables are applied in the order they were built

The agent runs commands concurrently and a command waits for the route lock behind a deploy's verification, which takes seconds; the lock is not a queue. A whole table the control plane built earlier (`sync_routes`, a replacing `proxy_configure`, the reconcile frame) can therefore be applied after a newer one and undo what the newer one changed: a maintenance window ends and visitors reach the application, a domain disappears. `keepLiveRoutes` protects the upstreams a table names, not the state only the control plane owns (maintenance, domains, access rules).

So every such table carries `tableSeq`: the builder bumps a per-server counter (`server_route_tables`, migration 0009) in the transaction that reads the routes, before reading them. The bump locks the counter until the transaction ends, so tables of one server are numbered in the order of the states they read. The agent records the highest number it applied (persisted with the proxy state) and applies no table below it: the command succeeds with "Superseded by a newer route table"; a `proxy_configure` still installs its configuration over the agent's own table; a reconcile frame brings neither table nor configuration. Equal numbers apply (redelivery); 0 (an older control plane, a route-only deployment's own table, the tables the agent builds for deploy, scale and remove) applies and moves nothing. The frame the control plane sends on connect is marked `hello` and replaces the record on arrival, lower or higher, so a control plane restored from a backup, whose numbers went back, converges. See `internal/agent/engine/tableseq.go`.

The counter's lock lasts to the end of the transaction, so it has one rule: it is the **last lock** a numbering transaction takes. Every row it writes or locks comes before `db.Queries.NumberRouteTable` (which first takes the server row shared, since the command carries a foreign key to it), nothing that locks follows it (audit entries, job inserts and the like are written first, the numbered table and its command last), and a transaction that numbers several servers does so in ascending server id (`db.SortedServerIDs`). With one global order, rows then counters, two transactions never wait for each other. `TestNumberingTransactionsNeverDeadlock` runs the real shapes against each other.

## Alternatives considered

- **Caddy 2**: `POST /load` validates and applies a whole JSON document atomically and keeps the old config if it rejects the new one, with a built-in internal CA for `*.localhost`. The cost of choosing Traefik instead is the asynchronous apply described above and the default (self-signed) certificate for local names.
- **Traefik with the Docker provider (labels)**: needs the Docker socket inside the proxy container and gives no single whole-table apply or acknowledgement.
- **Traefik with the file provider**: needs a file write into the container and relies on filesystem watching with no apply acknowledgement; the REST provider gives a single authenticated write endpoint and the same marker technique works.
- **Traefik with the HTTP provider**: the agent would have to serve the configuration to Traefik and Traefik polls it, adding latency and a second moving part.
- **nginx or HAProxy with templated config files**: no built-in ACME, reload semantics are manual, and config validation means shelling out.
- **A proxy built into the agent**: violates the rule of not writing a custom proxy.
