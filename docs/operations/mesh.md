---
title: Server mesh and cross-server links
description: "Join your servers to your tailnet, link services across servers, and keep the access rules NebulaCtrl generates for it."
section: Servers
order: 75
---

# Server mesh and cross-server links

The **mesh** joins the servers of a team to the team's tailnet (your Tailscale or the managed Headscale) so that a service on one server can reach a service on another server **inside the same environment**. Two features use it:

- **Cross-server service links**: an app on server A reaches a database, an API or a [read replica](database-replicas.md) on server B by its service name, for example `postgres:5432`.
- **Read replicas across servers**: the replication stream of a [Postgres read replica](database-replicas.md#same-server-and-cross-server) travels over the mesh.

It is a handful of small, ordinary containers that NebulaCtrl starts on your servers, plus an access policy it generates. This page explains what they are, what they cost, what can go wrong and how to test it.

The mesh is **off** until you enable it per server, and it needs a Tailscale or Headscale integration first.

## What runs on a server

| Container | One per | What it does | Privileges |
|---|---|---|---|
| **Server node** `nc-mesh-<server>` | mesh-enabled server | The server's identity on the tailnet. It accepts connections from peers and forwards them (`tailscale serve`, TCP only) to the services you expose. | None to speak of: userspace networking, `cap_drop: ALL`, `no-new-privileges`, no `/dev/net/tun`, no host network, no Docker socket. |
| **Link node** `nc-meshl-<link>` | environment, local server, remote server and purpose, **only while a link needs it** | The way out: containers of the environment dial `<service>:<port>` and the link node carries the connection to the remote server node. | `NET_ADMIN`, `NET_RAW` and `/dev/net/tun` **in its own network namespace only**, the same shape as the proxy's [managed tailnet container](proxy-tailnet-certificates.md#managed-mode-details). Not privileged, no Docker socket, no host network. |

Both use the same pinned `tailscale/tailscale` image as the other tailnet features. **Neither runs in the host's network namespace**, so NebulaCtrl changes no firewall rule, installs nothing on the host and does not interact with ufw or firewalld. Tunnel traffic is outbound UDP and HTTPS from containers. If the host already runs Tailscale, it keeps working next to the mesh (NebulaCtrl never adopts or manages it).

A link node joins the environment's network (and its `-int` network when the environment has one, see [networking](networking.md#the-layout-on-a-server)) under the **aliases of the services it reaches**. That is why a consumer that **blocks outbound internet** still reaches its providers: the link is on the internal network, and nothing else is.

A link node needs `/dev/net/tun` on its host (the agent reports it). A server without it can still join the mesh and *be* a provider; it cannot be the consuming side of a link: the plan says `mesh_no_tun`. On most distributions `modprobe tun` is enough.

## Providers

The mesh uses the integration you already have, in *Team settings → Integrations → Tailscale* (see [Setting up the Tailscale integration](proxy-tailnet-certificates.md#setting-up-the-tailscale-integration) and [Headscale](service-tailnet.md#headscale)):

| Provider | Joining | Access rules (ACL) |
|---|---|---|
| **Managed Headscale** ("Set up for me", [Private network (Headscale)](platform-services.md#private-network-headscale)) | single-use tagged pre-auth keys through its API key | **Managed**: NebulaCtrl owns the whole policy |
| **Your own Headscale** with an API key | same | **Managed** when its policy mode is `database`; **advisory** when it is `file` (the server refuses writes) |
| Headscale with a **pre-auth key** only | joining only; nodes cannot be deleted through the API, so removal leaves devices behind | **Advisory** |
| **Tailscale** (OAuth client) | single-use tagged auth keys | **Advisory** by default; **managed** only if you opt in |

Every mesh node carries **one tag** for the whole team, `tag:nebula-mesh` by default. It must be in the integration's allowed tags, and on Tailscale it needs a `tagOwners` line (see [the policy snippet](proxy-tailnet-certificates.md#setting-up-the-tailscale-integration)). Auth keys are **single-use, pre-authorized, non-ephemeral, tagged and valid for 1 hour**; they are minted when the command is sent, travel in the outgoing frame only, and are never in command payloads, the audit log, the API or logs. A node keeps its identity in a volume, so steady state needs no key.

## Turning it on

1. Connect the integration.
2. Server → **Settings → Mesh** → **Enable**, or `PUT /servers/{id}/mesh {"enabled": true, "acceptMeshChanges": true}` (`servers:write`; the change is accepted with `202`). Add `?dryRun=true` to see the plan: the devices it adds, whether `/dev/net/tun` is present, blockers and warnings. Without `acceptMeshChanges` an enabling that adds devices answers `mesh_confirmation_required`. It is audited as `mesh.enable` (`mesh.disable` for the opposite).
3. The server node appears under *Team settings → Server mesh* with its tailnet IP, name, peers (direct or relayed, latency), its exposures and its state: `provisioning`, `online`, `offline` or `needs_attention` (`unknown` when the last observation is older than 60 seconds). A link node can also be `degraded` (no direct path) or `removing`.

Link nodes are never created by hand. They appear when a deployment, a canvas edge, a replica or a Promote needs one ([below](#who-creates-links)).

## Access rules (ACL)

NebulaCtrl **generates** the rules; you do not write them. They come from three tables only: the server nodes, the links and the *grants* (which link may reach which exposed service). The result is always an explicit allow list, so the default is deny:

```jsonc
// one rule per link; src is the link node's tailnet IP, dst the remote server node and the granted ports
{"action": "accept", "src": ["100.64.0.12"], "dst": ["100.64.0.7:15432"]}
```

- A link belongs to **one environment**, so its IP is the source of rules for that environment's services only. A grant can only join a link of environment E to a service of environment E; that is enforced when the grant is created, so one environment can never open another environment's port. This is how access is limited **per team** (a team is a tailnet) **and per environment** without a tag per environment.
- Replication links and service links are **different nodes** with different source IPs, so a consumer's link can never reach a replication port and the other way round.
- Rules are IP and port based, sorted, and deterministic. IPv6 is left unmatched, which means denied.
- Rules are re-generated when nodes, IPs, links or grants change (debounced, a few seconds). A re-provisioned node gets a new IP and its rules follow.

**Team settings → Server mesh → Preview rules** (`POST /mesh/acl/preview`) shows the rules and, for Tailscale, the snippet. The page also lists each rule with why it exists: `link → server:port because replica <slug> of <primary>`.

### The ACL mode

`PUT /mesh/settings {"aclMode": "managed" | "advisory" | "off"}` needs the `admin` scope.

| Mode | What NebulaCtrl does |
|---|---|
| **managed** (Headscale) | Writes the **whole** policy document (`PUT /api/v1/policy`, after `POST /api/v1/policy/check`): the tag owners, the rule that lets members reach service tags, and the generated mesh rules. **Anything you edit by hand in that policy is overwritten.** Needs the API key credential and Headscale's `policy.mode: database`. |
| **advisory** (default on Tailscale) | Shows the generated `tagOwners` and `acls` snippet to paste into your tailnet policy, and writes nothing. With the optional `policy_file:read` scope the **ACL check** (`POST /mesh/acl/check`, `servers:read`) compares the stored policy with the generated rules: it lists the generated rules that are missing and the outdated NebulaCtrl rules still present, and it cannot prove that a broader rule of yours does not already allow the traffic. **The connectivity check ([Test](#test)) is the ground truth.** |
| **managed** (Tailscale, opt-in) | Patches **only** the mesh rules in your policy and keeps your comments (HuJSON), through the Tailscale policy API. A rule is treated as ours only when every source and destination is a mesh-node IP; only those are replaced, and `tagOwners` for the mesh tag is added. It validates before it writes and refuses to write over a policy that changed in between (`If-Match`). Enabling is audited as `mesh.acl.managed.enable`. |
| **off** | No rules are shown or applied; your tailnet's default policy decides. |

> **Read this before you opt in on Tailscale.** The managed push needs the OAuth client to carry the **`policy_file`** scope (write access to the policy). **A token with that scope can rewrite your entire tailnet policy**, not just the mesh rules, and NebulaCtrl's control plane holds that credential: a compromised control plane could open every port on the tailnet. NebulaCtrl asks for it only if you switch the mode on (the default scopes stay `auth_keys` and `devices:core`, with optional `policy_file:read` for the drift check), and it refuses the mode when another team uses the same tailnet (`mesh_acl_shared_tailnet`) and when the scope is missing (`mesh_policy_scope_missing`). If you do not want that trust relationship, stay advisory and paste the snippet.

A **replica or link deploy waits** until its link node is online and the policy state is `applied`, `advisory` or `off`; in advisory mode the wait ends with a successful connectivity check, never with an assumption. The state is shown on the Server mesh page: `unknown`, `applied`, `pending`, `drift`, `advisory` or `error`.

With an advisory policy and a tailnet whose default is allow-all, a link node can reach **any** exposure port of the remote server node. The Server mesh page warns about it. Tighten the tailnet policy (a new tailnet starts allow-all) or use a managed mode.

## Devices and plan limits

Every mesh node is a **tagged device**:

```
devices added = server nodes (one per enabled server) + link nodes (one per environment × server pair × purpose in use)
```

and they come on top of the devices of [per-service tailnets](service-tailnet.md) and the proxy's managed node. Every plan or dialog that adds a link states "adds N devices", and the page shows the number the mesh uses.

- **Tailscale**: Personal includes **50 tagged resources** and charges **$1 per month for each one beyond that**; it allows up to 6 users and 3 ACL groups. These are the published numbers at the time of writing: check [tailscale.com/pricing](https://tailscale.com/pricing) for your plan, because NebulaCtrl cannot read your quota and does not stop at it.
- **Headscale**: one instance with SQLite and no per-device limit. It is the single place where *changes* happen (see [failure modes](#failure-modes)).

A link exists only while something needs it; removing the last reference releases the grant, then the link, then the exposure, and deletes the device through the API (not possible with a pre-auth key credential: the UI says so).

## Cross-server service links

A **consumer** `C` on server A reaches a **provider** `P` on server B, both in the **same environment**. `P` is any service with a TCP port: an app, a worker, a database or a replica. UDP, Compose stacks and route services cannot be exposed (`mesh_not_exposable`), and **cross-environment links are not built**: shared networks stay on one server.

How it fits together:

1. **Exposure.** On B's server node a forward `meshPort → tcp://<service>:<port>` is created. When `P`'s running container carries the mesh alias it uses that, otherwise its container name; the agent decides by inspecting, so exposing a running service **needs no restart**.
2. **Link.** On A, a link node for (environment, A → B). Its aliases are the **slugs of the providers it reaches**.
3. **Grant.** The link may reach this exposure, because of a reference, a canvas edge or a replica.

**What the consumer dials is `<provider slug>:<port>`**, exactly like on one server. The port is the container port **when that port is free** on server B's node; otherwise the first free port in 15000-15999. It is allocated once and stays. In the common case `http://api:8080` works unchanged; the port differs only when two services on server B use the same one.

Variable references follow without you doing anything. `${{api.NEBULA_PRIVATE_HOST}}` is the slug, which is the alias. `${{api.PORT}}`, the database exports (`DATABASE_URL`, `DATABASE_PORT`, `PGPORT`, `REDIS_URL`, the `DATABASE_WRITE_*` and `DATABASE_READ_*` exports) and each group member's port become **consumer-aware**: they resolve to the mesh port when the provider runs on another server than the consumer, and to the container port otherwise. See [variables](variables.md#platform-variables).

### Who creates links

Links cost devices, so they are **never created by editing a variable**. They are added only by an explicit action:

- **Deploying or deploy-staged on the consumer.** The plan lists the links it adds ("adds N devices") and any blocker; deploying is the confirmation.
- **Creating a canvas edge** between services on different servers. The dialog states the devices and asks for confirmation (`acceptMeshChanges`); without it the API answers `mesh_confirmation_required`.
- **Adding a replica or promoting** one ([database replicas](database-replicas.md)).
- Deploying a provider that **moved server**.

Editing a variable that references a service on another server only stages: the staged bar shows "Mesh: adds link A → B (1 device)". A background **repair** pass every 5 minutes only **removes** what nothing refers to any more (after 10 minutes of grace; an edge's grant goes at once) and restores rows lost by a crash for pairs you already confirmed. It never adds a device.

### What a deploy checks

For a consumer that needs a link the plan blocks, or warns, with:

| Code | Meaning |
|---|---|
| `mesh_server_required` | mesh is not enabled on a server involved (with "Enable mesh on `<server>`") |
| `mesh_no_tun` | the consuming server has no `/dev/net/tun` |
| `mesh_agent_too_old` | the server's agent does not manage mesh nodes: update it |
| `mesh_paused` | the mesh is [paused](#the-paused-migration-window) |
| `mesh_not_exposable` | the provider is UDP, a stack, a route service or has no port |
| `mesh_route_pending` | warning: a reference to a provider on another server has no link yet |

After the link is added the deployment **waits up to 5 minutes** for every needed link to be online and the policy applied. On timeout it fails with "Not deployed: mesh link not ready. No changes were made." and nothing of the consumer changed.

### Security of a link

A consumer reaches exactly the provider ports it is granted, through its environment's link only. Other environments on the same server have no alias for it and no route to the link, and the policy source is the link's own IP. **Other services of the same environment on the consuming server can use the link too**: that is the same trust boundary as everything else in an environment. Traffic is encrypted by WireGuard between the link node and the server node; **it is not encrypted between the server node and the provider container on the same host** (that hop is local), and it adds no authentication of its own: the provider's own credentials still apply.

## DERP: public relays are the fallback

Peers connect directly when they can. When two servers have no direct UDP path, WireGuard traffic falls back to **DERP relays**. The **managed Headscale** is configured with **Tailscale's public DERP map** and has no relay of its own, so the fallback is Tailscale's public relays. Traffic stays end-to-end encrypted: a relay sees ciphertext, the endpoint addresses and the volume, not the content. This is disclosed on the Server mesh page and in the "Set up for me" dialog. It also means the managed Headscale server needs outbound internet access to fetch the map (it is the one service of the team's platform project that does not block outbound). An embedded DERP server is **not built**. Allowing inbound UDP 41641 on the servers only improves the chance of a direct path; it is optional and NebulaCtrl never opens it for you.

If you use your own Tailscale or Headscale, its DERP configuration applies.

## The paused migration window

Managed Headscale used to run with a policy *file*. The mesh needs the `database` policy mode so NebulaCtrl can write rules, so **an existing platform Headscale is migrated once**: redeployed with the new configuration. Fresh installs start with the new mode and have no window.

During the migration the mesh is **paused**:

- the Server mesh page shows a banner with the reason (for example "Headscale is being migrated");
- no key is minted, no node is synced or removed, no ACL is written, and replica and Promote operations wait (they resume by themselves);
- a consumer deploy that needs a **new** link fails with "Not deployed: mesh paused. No changes were made.";
- **established tunnels, existing nodes and [service sidecars](service-tailnet.md) keep working.**

The job redeploys Headscale through the platform service's own provisioning, waits for it to be healthy, writes the base policy plus the mesh rules, reads them back and un-pauses. Between the restart and the first write Headscale has **no policy, which means allow-all**; that lasts seconds, and nothing can join because the mesh is paused. If a step fails, the mesh **stays paused** with the reason (the banner starts with "Headscale is being migrated" and says what failed). **Retry** the Headscale service on the [Private network (Headscale)](platform-services.md#private-network-headscale) page (Team settings) to resume; the job also looks again every 10 minutes and is safe to run more than once. While Headscale itself is down, see the next section.

## Failure modes

| What happens | Effect | What you see |
|---|---|---|
| **The coordinator is down** (the Headscale host, or Tailscale's control plane) | Established WireGuard sessions keep carrying traffic, so a running replication stream continues (what a node does if it restarts while the coordinator is down was not verified: assume it cannot rejoin). No new joins, no ACL changes, no re-provisioning. | Banner "Mesh coordinator unreachable: existing connections keep working, changes wait". Adding a replica is refused with `mesh_coordinator_unreachable`. The page warns when Headscale and a replication member run on the **same server** (a shared failure). |
| **A server cannot reach DERP and has no direct path** | The link is down. | Link state `degraded` or `offline`; Test returns `no_path`. |
| **A server is offline** | Node and link states go stale. | Observations older than 60 seconds show "unknown", not "online". |
| **A server is revoked** | Its node and link devices are deleted through the API, exposures, grants and links are dropped and the rules are regenerated. Replicas on it become `failed`; replicas whose **primary** was on it become `broken` and Promote is offered. | Server list and Server mesh page. |
| **A node is deleted in the tailnet admin console** | The node reports `needs_attention`. | **Re-provision** mints a new key, wipes the node state and gives it a **new tailnet IP**; the rules follow. |
| **The integration is removed or changed** | Blocked while the mesh is enabled: 409 `integration_in_use`. | |
| **The mesh is paused** | See [above](#the-paused-migration-window). | Banner with the reason. |
| **A link was never granted (or the rule is missing)** | Connections time out. | The link's connectivity check returns `timeout`; a replica using it goes `broken`. |

An auth key that never produced an online node within an hour is revoked by a sweeper. Node keys have no expiry once joined (tagged devices are exempt on Tailscale and Headscale), so rotation means **re-provisioning** a node, rotating the OAuth secret or the Headscale API key (a platform Headscale's key lasts 365 days; **Reconnect** mints a new one).

NebulaCtrl never depends on MagicDNS for the mesh: `TS_ACCEPT_DNS=false` on every node so Docker's own DNS keeps resolving aliases. Names on the tailnet are cosmetic.

## Test

Connectivity is what proves a mesh works, in every ACL mode.

- **Server → Mesh → Test**, or `POST /servers/{id}/mesh/check` (`servers:write`, optionally `{"peerServerId": "…"}`; the default is every other server with a mesh node): the agent reads the server node's view of the tailnet (`tailscale status`, plus a ping) and reports, per peer server node, `connected`, `refused`, `timeout` or `no_path` with the latency and a message.
- **Link checks** run for you: before a replica or a deploy that needs a link goes on, and in advisory mode until the check connects, the agent dials `<link node>:<mesh port>` through the environment network for each grant. `connected` means the whole path (link node, policy, server node, provider) works for that pair; `timeout` usually means a missing rule or no path (an ACL that does not allow the port looks the same as a dead remote node); `refused` means the path works and nothing listens.
- The **ACL check** is a different, read-only comparison of policy against rules (see [the ACL mode](#the-acl-mode)).

The check proves **intent, not exclusivity**: it cannot show that nothing else is allowed. For that, read the rules (Preview) and your tailnet's own policy.

## Limits

- **Same environment only**: no cross-environment links.
- **TCP only**: no UDP, no Compose stacks, no TLS termination and no DNS names for remote services; a consumer dials `<slug>:<port>`.
- **Not verified here**: Tailscale SaaS (OAuth, policy API, tag ownership, device quota), NAT traversal and DERP fallback across real networks, WAN latency and MTU, hosts with ufw or firewalld enabled, and `/dev/net/tun` inside docker-in-docker. The design (server node, link node, policy, replication through it) has been exercised with Headscale 0.29.6 and the pinned `tailscale/tailscale` image on one machine, and the control plane and agent code is covered by unit tests against fake Tailscale, Headscale and Docker; run it on a staging tailnet before you rely on it.
- No exit nodes, subnet routes, Tailscale SSH or Taildrop, and no management of a Tailscale that already runs on the host.
- Headscale is a single instance: it is where changes happen, not a path for established traffic.

Related: [database replicas](database-replicas.md), [per-service tailnet](service-tailnet.md), [Private network (Headscale)](platform-services.md#private-network-headscale), [networking](networking.md), the [threat model](../security/threat-model.md).
