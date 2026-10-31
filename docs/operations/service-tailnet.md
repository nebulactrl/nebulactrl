---
title: Per-service tailnet
description: "Make one service reachable on your Tailscale or Headscale tailnet."
section: Servers
order: 70
---

# Per-service tailnet (Tailscale and Headscale)

Put a single service on your tailnet: a private HTTPS (or HTTP) address and raw TCP ports reachable only from your tailnet, with no public port. Typical uses: an admin UI, a database you reach from your laptop, a game server for friends on the tailnet.

This is **ingress to one service**. Routing the proxy to devices on your tailnet is a different feature (see [tailnet egress](proxy-tailnet-certificates.md#tailnet-egress)), and so is letting services of one environment on different servers reach each other (the [server mesh](mesh.md)). The mesh uses the same integration and the same pinned image, but its own devices (server nodes and link nodes, tag `tag:nebula-mesh`), and does not touch a service's sidecar: both can be on at once, and each is a tagged device that counts against your plan.

## How it works

- One sidecar container per service (`nc-ts-<serviceId8>`) runs the pinned `tailscale/tailscale` image in **userspace networking mode**. It joins only the service's environment network and proxies to `http://<service-slug>:<port>`, the alias every replica shares, so one device fronts all replicas and survives rolling deploys and scaling.
- No `NET_ADMIN`, no `/dev/net/tun`, `cap_drop: ALL`, `no-new-privileges`, no published ports, no Docker socket, no routes advertised: a tailnet peer reaches only the ports the serve config names. The agent reads the node's state with `docker exec tailscale status --json` (a fixed argv, no shell).
- Settings are `config.tailnet` on the service. They are **staged** like every setting (the Staged chip, `pending-changes`, per-field discard) and applied by the next deployment. Nothing contacts Tailscale until you deploy.
- The device is created with a **single-use, pre-authorized, tagged, non-ephemeral auth key minted at the moment the command is sent**. The key travels in the outgoing frame only: it is not in `agent_commands.payload`, the audit log, the API, deployment logs or agent state, and the agent masks it in everything it reports. One-off keys are consumed at the first login; a redelivered command mints another and the unused one expires within an hour.
- The device identity lives on the volume `nc-ts-<serviceId8>-state` (and survives restarts, recreations and agent upgrades). The serve config lives on `nc-ts-<serviceId8>-config`, is written by the agent and re-applied live by Tailscale's file watcher, so changing exposures never restarts the sidecar.
- The real MagicDNS name is **read back from the node** (`status.fqdn`), never constructed: a name clash makes Tailscale append `-1`.

## Setting it up

1. Connect a provider in *Team settings → Integrations → Tailscale*: Tailscale (OAuth client) or Headscale. See [Tailscale](proxy-tailnet-certificates.md#setting-up-the-tailscale-integration) for the OAuth client, tag ownership and ACLs, and [Headscale](#headscale) below. The integration's **allowed tags** bound what services may use.
2. Service → Settings → **Tailnet**: switch on *Reachable over your tailnet*, then pick what to expose:
   - **Web**: HTTPS on 443 to the service port (Tailscale; needs *HTTPS Certificates* enabled in the tailnet DNS settings) or plain HTTP on 80 (Headscale).
   - **TCP ports**: `listen → target` forwards (per connection, encrypted by WireGuard). Chips fill common database ports. *TLS* terminates TLS with the node certificate (Tailscale only).
   - Optional hostname (default `<service>-<environment>`), extra tags (from the allow-list), **Tailnet only** and Funnel.
3. Save, then **Deploy**. The plan says what will happen ("creates device `web-production` tagged `tag:nebula-svc`").

The canvas card shows a chip next to the host chip with the node name and a status dot, with the text state (`Online`, `Provisioning…`, `Offline`, `Needs attention`) in its tooltip and accessible label, a "Public" mark when Funnel is on and a lock icon (tooltip "Tailnet only") when public domains are paused.

## Tailnet only

*Tailnet only* (`hidePublic`) removes the service's public routers: its domains are kept (and shown paused) but never routed, on any route-table push, until you turn it off and deploy. It follows the **applied** configuration, so a staged toggle changes nothing until the deploy that applies it. Services that other route services target stay reachable through those routes.

## Funnel

Funnel publishes the web exposure on the **public internet** through Tailscale's relays. It is off by default, lives under *Advanced* and needs a typed confirmation (the hostname). It bypasses NebulaCtrl's proxy: no access logs, no traffic analytics, bandwidth limits apply and the hostname is public. Your tailnet policy must grant the `funnel` node attribute to the tags. Audit: `tailnet.funnel_enabled`. Funnel is not available with Headscale (422 `tailnet_funnel_unsupported`).

## What the plan and API refuse

| Code | Meaning |
|---|---|
| `tailnet_not_configured` (422) | no Tailscale/Headscale integration in this team |
| `tailnet_tag_not_allowed` (422) | an extra tag is not in the integration's allowed tags (stops a project member from assigning a more privileged tag) |
| `tailnet_https_disabled` (422) | web on Tailscale while a connection test found HTTPS certificates disabled; enable them or expose TCP ports only |
| `tailnet_funnel_unsupported` / `tailnet_tls_unsupported` (422) | Headscale has no Funnel or TLS termination |
| `agent_too_old` (422) | the server's agent does not advertise `tailnet.service` |
| `invalid_config` (422) | hostname not an RFC 1123 label, bad tag syntax, no exposure chosen, web without an HTTP port, duplicate or colliding ports, one-shot or route services |

## Identity, re-provisioning and removal

- A node is **re-created** (old device deleted first, so the new one keeps its name; new tailnet IP) when its identity changes (hostname, tags, provider or login server), when the service moves to another server, or when it needs attention. Exposure changes never re-create it.
- **Needs attention** appears when the device was deleted in the admin console or its key was rejected: the sidecar exits or sits in `NeedsLogin`, the agent reports it and the control plane flags it. *Re-provision* (`POST /services/{id}/tailnet/reprovision`, `deployments:write`) mints a fresh key, wipes the state volume and recreates the node; the next deployment also does it. The sidecar's last log lines are included in the message.
- **Disabling** (or deleting the service) removes the sidecar and both volumes, then a background job (`tailnet_delete_device`, retried with backoff, a no-op when the integration or the device is gone) deletes the device through the API. Re-enabling creates a new device with a new IP. The plan states it. Orphaned devices from a failed job are left for you to delete in the admin console.
- **Rollback never touches tailnet state.** A rollback, a reconcile redeploy and a drain run an existing release: they keep the node (and "Tailnet only") as last applied, or re-create it on the new server for a drain. The staged-changes list afterwards may show `config.tailnet.*` differences from the old release; deploying them is a no-op unless you changed the tailnet since.
- A failure of the tailnet step **never fails or rolls back the app**: the deployment keeps the app's outcome and carries a `Warning: tailnet …` message; the node shows `Needs attention` with the reason.
- If the Tailscale API is unreachable at send time the deployment is *not delivered* ("could not resolve the command's secrets … No changes were made."), as for any send-time secret. Only deployments that create or re-create the device need the API.

## Headscale

Pick *Headscale* in the integration card:

- **Login server**: the Headscale URL (also the REST base).
- **Credential**:
  - *API key*: NebulaCtrl creates a **single-use pre-auth key per provisioning** (`POST /api/v1/preauthkey`, tags from the key, never `--advertise-tags`), expires unused keys, finds nodes by tailnet IP and deletes them (`DELETE /api/v1/node/{id}`) on disable and re-provision. Optional *user* owns the keys; empty creates tags-only keys (nodes belong to `tagged-devices`).
  - *Pre-auth key*: one **reusable tagged key** handed to every sidecar. Nodes cannot be deleted through the API, and the key stays visible to anyone with Docker access on the hosts. Prefer the API key.
- Headscale has **no Serve HTTPS and no Funnel** (feature matrix, issue juanfont/headscale#1921): the web exposure is plain HTTP on port 80 and TCP ports forward over WireGuard. The agent writes the HTTP serve config only after the node has its real DNS name. The tags must be allowed by Headscale's policy (`tagOwners`); the connection test mints and expires a key to check that. Clients need Headscale ≥ 0.26 API shape (`/api/v1`, string ids) and a Tailscale client ≥ the version Headscale requires; check your server's `/swagger` if a call is rejected.

## Tailscale policy

Do not give the sidecars more reach than they need: they never initiate, so add no ACL with `src: tag:nebula-svc`. Example (see the OAuth page for `tagOwners`):

```jsonc
"acls": [
  {"action": "accept", "src": ["autogroup:member"], "dst": ["tag:nebula-svc:443", "tag:nebula-svc:5432"]}
]
```

A new tailnet is allow-all: tighten it. Hostnames become public in Certificate Transparency logs when HTTPS is on; do not put secrets in hostnames. One tagged device per service counts against your plan's device quota.

## Limits and known behaviour

- **Not verified against a real tailnet.** The API clients are tested against fake Tailscale and Headscale servers and the container shape follows the documented userspace setup. Serve HTTPS, Funnel, OAuth key minting and device deletion on Tailscale SaaS need one run on a staging tailnet before you rely on them.
- The web path load-balances **per connection, not per request** across replicas (connection pooling in Tailscale Serve); TCP forwards balance per connection too. Idle pooled connections to a retired replica are replaced.
- Serve adds `Tailscale-User-*` identity headers only for user nodes and the backend is reachable on the environment network: **do not use them as authentication.**
- Memory: the sidecar is capped at 256 MiB (not measured per node).
- Sidecar auth key lives in the container environment until the node is recreated; one-off keys are inert after first login.
- Egress from the app to tailnet hosts is not provided by this feature (the app container does not share the sidecar's network namespace). Reaching a service on **another server of the same environment** is what the [mesh](mesh.md) does; reaching arbitrary tailnet hosts is not available.
