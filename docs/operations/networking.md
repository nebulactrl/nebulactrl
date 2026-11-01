---
title: Networking
description: "Environment networks, shared networks and blocking outbound internet."
section: Applications
order: 60
---

# Networking: environment networks, shared networks, blocking outbound internet

What can talk to what on an enrolled server, and the three controls you have: the private network every environment gets, opt-in **shared networks** that connect chosen services of different projects, and a per-service **Block outbound internet** switch.

For the threat model behind this (what is isolated and what is not) read the [network section of the threat model](../security/threat-model.md).

## The layout on a server

| Network | Created for | Who is on it |
|---|---|---|
| `nebulactrl-edge` | the server | **Traefik**, the error-pages server and `cloudflared` (when a tunnel is on). No app, task or tailnet container is on it. |
| `nebulactrl-env-<id8>` | each environment, on the servers it runs services on | the environment's services (alias = the service slug), and Traefik while a route needs it |
| `nebulactrl-env-<id8>-int` | an environment with a service that blocks outbound internet; Docker `internal`: no gateway | the blocked services, and every other service of the environment (same alias), and Traefik while a route needs it |
| `nebulactrl-shared-<id8>` | a team shared network, on one server | the services attached to it (alias `<service>.<env>.<project>`) |
| `nebulactrl-mesh` | a mesh-enabled server ([mesh](mesh.md)) | the server's mesh node `nc-mesh-<server>`, which also joins the environment networks of the services it exposes to other servers |

A mesh **link node** (`nc-meshl-<link>`) joins the environment's network, and `-int` when the environment has one, under the **slugs of the services it reaches on another server**. Nothing else changes for the services themselves: see [Across servers](#across-servers).

All managed networks are named `nebulactrl-…` so they sort **after** `nebulactrl-edge`. Docker picks a container's default gateway network by name order, and when Traefik joined an environment network that sorted before the edge network, Docker moved its gateway and re-bound its published ports (a brief connection blip). On Docker Engine 28 and newer (API 1.48) the agent also gives the edge network the highest gateway priority on Traefik, the error-pages server and `cloudflared`.

Consequences:

- A container of project A cannot reach a container of project B on the same host: they share no network. Teams on one installation never share one either.
- Services never publish host ports; only Traefik does. Traefik joins the network of each environment (or `-int` network) that holds a routed service when the route is applied, and leaves it again when the last route is gone.
- The agent probes a new container's health on its address on its environment network (a containerised agent attaches itself to the networks it probes).
- Networks are created on demand by the first deployment that needs them, and the agent removes the ones that are empty (see [Address pool](#address-pool)). You never create a Docker network by hand.

Inside an environment a service reaches another by its slug (`postgres:5432`). `NEBULA_PRIVATE_HOST` is the slug.

In a service with [processes](processes.md) the service slug is the alias of the default process and `<process>.<slug>` the alias of a named process, on every network the container joins.

## Across servers

Each server has its own networks, so a service on server A cannot reach one on server B by its slug. Once the [mesh](mesh.md) is enabled on both servers, **services of the same environment** can: a consumer dials `<provider slug>:<port>` as usual and a link node carries the connection over the tailnet. The environment boundary still holds: a link belongs to one environment, its access rules are generated for that environment's services only, and containers of another environment on the same server have no alias for it and no route to it. A service that blocks outbound internet still reaches the providers it is linked to, because the link node is on the `-int` network. The providers' ports are the container ports when free on their server, otherwise a mesh port in 15000-15999 (variable references follow).

## Shared networks

Use one when services of **different projects** (or environments) on the **same server** must reach each other: a shared Postgres for several projects, an internal API used by two products.

- A shared network belongs to the **team** and to **one server**. It never spans servers (the mesh does not carry shared networks) and never crosses teams: another team's network does not exist as far as you are concerned (404).
- **Team settings → Private networks → New network**: a name (unique in the team) and the server. Needs the `servers:write` scope (owners and admins). The page lists every network with its server and the services attached, each with the hostname to copy.
- **Service → Settings → Networking → Shared networks**: tick the networks to join. The list shows the networks on the service's server. The hostname the service gets on each is shown for copying: `<service>.<environment>.<project>`, for example `api.production.shop`. Needs `deployments:write` on the service's project.
- Attaching and detaching are **staged** like every setting: the service shows *Staged* and **N changes to apply**, the review sheet lists `networks.<name>`, discarding it puts the attachment back as it was, and the next deploy joins or leaves the network. Rolling back restores the networks the old release had (a network that was deleted since is simply left out).
- A service on a shared network is on its environment network too; it keeps reaching its environment by slug and other teams' services only through the shared network.
- A canvas card on a shared network shows a small network chip; hover it for the hostnames.

What the control plane refuses:

| Code | Meaning |
|---|---|
| `network_wrong_server` (422) | the service runs on another server than the network |
| `service_no_server` (422) | the service is not placed on a server yet |
| `egress_blocked` (422) | the service blocks outbound internet (see below): shared networks and outbound blocking are exclusive, because a shared bridge would give the service its internet back |
| `network_in_use` (409) | deleting a network that services are still attached to |
| `network_name_taken` (409) | a network with that name exists in the team |

Moving a service to another server **detaches** its shared networks (they cannot follow it); the change is staged and shows in the review sheet. Deleting a service frees its attachments.

Audit actions: `network.create`, `network.delete`, `network.attach`, `network.detach` (the service and network names, never anything secret).

## Block outbound internet

Service → Settings → **Networking** → *Block outbound internet*. It is part of the service's configuration, so it is **staged and released** like the port or the health check: it takes effect on the next deploy, and a rollback restores the value the old release had.

A blocked service joins only the environment's `-int` network. A Docker `internal` network has no gateway, so the container has **no route to the internet, the LAN or the host**. Every other service of the environment joins `-int` too under the same alias, so it still reaches the blocked service; the proxy reaches it over `-int` as well.

Defaults:

- **Managed databases: on.** A database needs no internet, and backups do not need one either: they stream through the agent. You can switch it off for one database (for example to install an extension on start-up) in its settings.
- **Apps and workers: off.** Most apps call something: package registries, payment or mail APIs, webhooks.

Things to know:

- **Calls out fail.** Anything that resolves or connects to the internet from that container fails: updates, webhooks, third-party APIs, `npm install` at start. Cron tasks and one-off commands run with the same network membership as the service. Builds run on the host and are not affected.
- **Deploy the others too.** A service deployed *before* a sibling started blocking outbound is not yet on the `-int` network; it joins it the next time it is deployed. The deploy plan of the service you changed says so ("Other services in this environment join the internal network the next time they are deployed"). Redeploy the services that need to reach it, or use **Deploy staged** on the environment.
- **The plan warns about outbound-looking variables.** If a service that blocks outbound has variables that look like they call the internet (a public URL, `SMTP_*`, `STRIPE_*`, `AWS_*`, `S3_*`, `SENTRY_*`, …), the plan lists their names. It is a hint from names and values, not a guarantee in either direction.
- **A blocked service cannot join shared networks** (`egress_blocked`). To share a database across projects, allow outbound for it, attach it, and rely on the shared network's membership for access control. A service that may reach the internet is not filtered at all.
- **A tailnet sidecar still works.** The sidecar keeps its own outbound access (it needs it to reach the tailnet's control server) and also joins `-int`, so it reaches the blocked service; only the service container is cut off.
- **A mesh link still works.** The link node joins `-int`, so a blocked consumer reaches the providers it is granted on other servers (and a blocked replica reaches its primary) and nothing else ([mesh](mesh.md#what-runs-on-a-server)).
- Route services (external upstreams and TCP/UDP exposures) have no container, so the setting does not apply to them.

## Address pool

Docker hands out subnets of new networks from its *default address pools* (`172.16.0.0/12` split into /16s and `192.168.0.0/16` split into /20s): about 30 networks per host. Every environment needs one network, or two with an outbound block, so a host with many environments or previews failed with `all predefined address pools have been fully subnetted`.

The agent therefore gives every `nebulactrl-env-*`, `nebulactrl-env-*-int` and `nebulactrl-shared-*` network an explicit subnet out of its own pool, **`10.213.0.0/16` split into /24s: 256 networks** of up to 253 containers each. The edge network `nebulactrl-edge` is created with Docker's defaults and does not use the pool.

| Flag | Environment | Default |
|---|---|---|
| `--network-pool` | `NEBULACTRL_NETWORK_POOL` | `10.213.0.0/16` |
| `--network-prefix-len` | `NEBULACTRL_NETWORK_PREFIX_LEN` | `24` |

How a subnet is picked (one network at a time, under a lock):

- The **first free** subnet of the pool, lowest address first. A subnet freed by a removed network is used again.
- A subnet is free when it overlaps neither the IPAM subnet of **any** Docker network on the host (not only NebulaCtrl's) nor an address range of a network interface of the agent. Inside a container that means the daemon's networks are what counts.
- If Docker still refuses the pick as overlapping (a host route the agent cannot see, for example a VPN), the agent tries the next subnet. After 64 refusals it stops and reports it.
- A network that **already exists keeps its subnet**, whatever it is: upgrading the agent never recreates a network, so networks created before the pool existed stay where they are (and are swept when they empty, then come back from the pool).
- Only IPv4 pools are supported; the networks are IPv4.

When the pool is full the deployment fails with a message such as `network address pool exhausted: no free /24 subnet left in 10.213.0.0/16 for network nebulactrl-env-… (256 subnets …); widen the pool with --network-pool …`. Fix it by, in this order:

1. Removing unused networks: `docker network prune` removes the ones nothing uses; the agent's sweep removes empty `nebulactrl-env-*` and `nebulactrl-shared-*` networks (and leftovers of the old `nc-env-*` / `nc-shared-*` names) by itself after 60 seconds.
2. Using smaller subnets: `--network-prefix-len 26` gives a `/16` pool 1024 networks of 61 containers each.
3. Widening the pool: `--network-pool 10.208.0.0/12` is 4096 networks of /24.

Change the pool if `10.213.0.0/16` collides with something you reach from the host (a LAN, VPN or office network): pick a range none of them uses. Changing it affects only networks created afterwards.

Set them as environment variables in `/etc/nebulactrl-agent.env` (the systemd unit loads it) and restart the agent, for example `NEBULACTRL_NETWORK_POOL=10.208.0.0/12`. An invalid pool or prefix length stops the agent at start.

## Operating notes

- **See what is where:** `docker network ls --filter label=nebulactrl.managed=true` on the server; `docker network inspect nebulactrl-env-<id8>` lists the members. The environment id's first eight characters are in the URL of the environment's services (API: `environmentId`).
- **A service cannot reach a sibling after a change:** check that both were deployed since the change (see *Deploy the others too*). A service that only has a shared-network hostname must be on the network *and* the target must be too.
- **Traefik returns 502 for a service that works:** the proxy joins a service's network when its route is applied (every route carries the networks it needs). Redeploy the service; if the proxy itself was recreated, an agent reconnect re-applies every route.
- **A leftover network** after deleting everything: it is removed by the agent's next sweep (it runs every minute and on every reconcile), once nothing is connected to it and it is at least 60 seconds old. Networks with a container still connected are never removed.
- **API:** `GET/POST /networks`, `GET/DELETE /networks/{id}`, `PUT/DELETE /services/{id}/networks/{networkId}`; the setting is `config.blockEgress` of `PATCH /services/{id}`. Omitting `blockEgress` from a config PATCH keeps the current value.

## Limits

- No per-destination egress rules: a service is either allowed out entirely or not at all.
- Networks never span servers and there is no overlay network. Services of one environment on different servers can reach each other through the opt-in [mesh](mesh.md) (TCP only, tagged tailnet devices that count against your plan); across environments and for anything else use a tunnel, a tailnet or a public domain.
- Docker networks are not a sandbox for hostile tenants: everything on a host shares its kernel and Docker daemon. See the [threat model](../security/threat-model.md).
