---
title: Certificates, upstream policy and tailnet egress
description: "Upload a certificate, the upstream (SSRF) policy and routing to tailnet devices."
section: Servers
order: 40
---

# Custom certificates, upstream policy and tailnet egress

Server-level proxy settings. Every server runs its own Traefik, so everything on this page belongs to **one server** and is **staged** until you press **Deploy proxy** (`POST /servers/{id}/proxy/deploy`). Nothing reaches the agent before that, except that the *applied* configuration keeps riding along with every route push.

All of it needs an agent that advertises the `proxy.v2` capability. For an older agent, staging any of it answers `422 agent_too_old`; update the agent first (the proxy view shows `agentTooOld`).

Scopes: certificates, policy and tailnet changes need `proxy:write`; reads need `servers:read`. The loopback/link-local toggles and the Tailscale integration need `admin`. MCP token presets never receive `proxy:write`.

## Staged, plan, deploy

| Step | Endpoint | Effect |
|---|---|---|
| Stage | `POST /servers/{id}/certificates`, `DELETE /certificates/{id}`, `PATCH /servers/{id}/proxy/settings` | Bumps `proxy.generation`. The proxy view shows `staged: true` while `generation > appliedGeneration`. |
| Plan | `POST /servers/{id}/proxy/plan` | `{restartRequired, reasons[], warnings[], diff[]}`. Contacts nothing. |
| Deploy | `POST /servers/{id}/proxy/deploy` (`Idempotency-Key`, 202) | Sends a `proxy_configure` command built from the **desired** state (staged uploads included, staged removals excluded). One at a time per server (409 `proxy_deploy_in_progress`). |
| Result | agent → control plane | On success: certificates become applied (removed ones are deleted for good), policy and tailnet are copied to their *applied* columns, `appliedGeneration` catches up, the tailnet device id and status are stored. On failure nothing changes; `lastDeploy` shows the agent's message. |

The command payload contains **no** private key, bcrypt hash or Tailscale auth key. They are attached to the outgoing frame at send time and never persisted (`agent_commands.payload`, audit rows, API responses and logs are all free of them).

The commands wait for a server that is offline and expire after 15 minutes, like every command.

## Custom certificates

Use them for wildcard or internal certificates that Let's Encrypt (HTTP-01) cannot issue, for certificates from your own CA, and for hosts that are not reachable from the internet.

```
curl -X POST https://nebula.example.com/api/v1/servers/$SERVER/certificates \
  -H "Authorization: Bearer $TOKEN" -H "Idempotency-Key: $(uuidgen)" -H 'Content-Type: application/json' \
  -d "$(jq -n --rawfile c fullchain.pem --rawfile k privkey.pem '{name:"wildcard-apps", certPem:$c, keyPem:$k}')"
```

What is checked (each failure is a 422 with its own `code`):

| Check | Code |
|---|---|
| each PEM at most 64 KB | `pem_too_large` |
| `certPem` holds only `CERTIFICATE` blocks (the **first** is the leaf, the rest is the chain) | `invalid_certificate` |
| the key is an unencrypted PKCS#8, PKCS#1 (RSA) or SEC1 (EC) key; Ed25519 works | `invalid_key`, `encrypted_key` (remove the passphrase: `openssl pkey -in key.pem -out key-plain.pem`) |
| key and leaf match | `key_mismatch` |
| not expired, not yet valid (5 minutes of clock skew tolerated) | `certificate_expired`, `certificate_not_yet_valid` |
| at least one DNS or IP SAN (browsers ignore the common name) | `certificate_no_san` |
| same certificate twice / same name twice on a server | 409 `certificate_exists`, `certificate_name_taken` |

NebulaCtrl records the SANs as `domains`, the issuer, validity and the SHA-256 fingerprint of the leaf. The key is sealed with the workspace secret key (`NEBULACTRL_SECRET_KEY`) and is **never returned** by any endpoint. The audit event `certificate.upload` carries the name and the domains only.

How Traefik uses it: the certificate goes to the proxy inline (no files, no restart) and is selected by **SNI** against its SANs, wildcards included (`*.apps.example.com` matches exactly one label). A host without a matching certificate gets Traefik's default certificate. Pin a certificate to a domain with `tlsMode: custom` and `certificateId` (the domain must be covered by the certificate, otherwise 422 `certificate_mismatch`); `tlsMode: auto` keeps using Let's Encrypt where the server has an ACME email.

Deleting: a certificate that was never deployed disappears at once; an installed one is **staged for removal** (`change: remove`) until the next deploy. A certificate a domain still pins cannot be deleted (409 `certificate_in_use`).

Status and expiry: `status` is `pending` (never applied), `active` (loaded by the proxy) or `error` (the proxy could not load it, or it expired). The daily `CertExpiryJob` writes `expires in N days` into the status message at 30 and 7 days, records the audit event `certificate.expiring` once per threshold, and marks expired certificates `error`. **There is no renewal for custom certificates**: upload the new one, repoint the domains, deploy, delete the old one.

## Upstream policy (SSRF guard)

External route services and TCP/UDP routes dial operator-supplied addresses, so the same policy runs on the control plane (create/update → 422 `upstream_denied`) and on the agent (before applying a table).

| Target | Default |
|---|---|
| unspecified addresses, `nebulactrl-*` containers, the proxy admin port | always denied |
| loopback (`127.0.0.0/8`, `::1`, `localhost`) | denied; **admin** can allow per server (`allowLoopback`) |
| link-local incl. cloud metadata `169.254.0.0/16`, `metadata.google.internal` | denied; **admin** can allow per server (`allowLinkLocal`) |
| `host.docker.internal` (the Docker host) | allowed; `allowHostGateway: false` removes the mapping from the proxy container |
| LAN, tailnet CGNAT `100.64.0.0/10`, public addresses, DNS names | allowed |

`PATCH /servers/{id}/proxy/settings {"upstreamPolicy": {...}}` stages the change. The *applied* policy (what the agent enforces and what route pushes carry) only changes with a successful **Deploy proxy**. Changing `allowHostGateway` recreates the proxy container (the plan says so).

## Tailnet egress

Goal: route to services on your tailnet (a NAS, another VPS) from this server's proxy. This is **egress from the proxy**, not publishing NebulaCtrl on the tailnet.

| Mode | What runs | When |
|---|---|---|
| `off` | nothing | default |
| `managed` | a `tailscale/tailscale` container `nebulactrl-tailscale` joined to the proxy container's network namespace in **kernel mode**; identity in the volume `nebulactrl-tailscale-state` | the host has no Tailscale; needs the Tailscale integration |
| `host` | nothing: the host already runs Tailscale and the proxy reaches 100.x addresses through it | `facts.hostTailscale.detected` (an interface address in `100.64.0.0/10`); 422 `host_tailscale_not_detected` otherwise |

Routes then target the device's tailnet IP (`100.x.y.z:port`) from the device picker (`GET /integrations/tailscale/devices`). External routes to `100.64.0.0/10` are allowed by the default policy.

### Managed mode details

- **Kernel mode needs `/dev/net/tun` and `NET_ADMIN`/`NET_RAW` on the Tailscale container only.** It is not privileged, has no Docker socket and no host network. The Traefik container gets no extra capability.
- Sharing the proxy's network namespace means the Tailscale container is **recreated whenever the proxy container is** (new TCP/UDP port, host gateway change, MagicDNS toggle). Its identity persists in the volume, so no new key is needed.
- Hostname: default `nebula-<server name slug>`. The real name is read back from the node (`status.fqdn`); a name clash makes Tailscale append `-1`, so never construct it yourself.
- **Provisioning** happens when the mode becomes `managed`, the hostname changes, or you ask for it (`reprovision`). The control plane mints a **single-use, pre-authorized, non-ephemeral, tagged** key (1 hour, description `nebulactrl server <name>`) at send time and attaches it to the command frame; the agent recreates the node with it. Steady-state deploys carry no key (`TS_AUTH_ONCE`).
- **Disabling deletes the device** from your tailnet: the agent removes the container and the state volume, then a River job (`tailnet_delete_device`, retried with backoff, a no-op when the integration is gone or the device already is) deletes it through the API. Re-enabling creates a new device with a new tailnet IP. Re-provisioning and hostname changes replace the device the same way. The plan states it.
- **Device removed in the admin console, or the key was rejected**: the node reports `needs_attention` ("Re-provision"). `POST /servers/{id}/proxy/tailnet/reprovision` mints a fresh key, wipes the state and recreates the node.
- **MagicDNS** is off by default. When on, `100.100.100.100` is added to the proxy container's DNS so routes may name devices (`nas.tailnet.ts.net`). It restarts the proxy container. Prefer IPs; MagicDNS names also depend on your tailnet's DNS settings, and a MagicDNS resolver in the proxy container affects every upstream hostname lookup of that container.
- Heartbeats carry the observed state (`off | provisioning | online | offline | needs_attention`, FQDN, tailnet IPs, device id) into `servers.tailnet_status`; the server list shows it for badges.

### Setting up the Tailscale integration

One per workspace: *Team settings → Integrations → Tailscale* (`PUT /integrations/tailscale`, admin). Credentials are an **OAuth client**, not an API access token. The same integration powers the [per-service tailnet](service-tailnet.md) (one sidecar and device per service), the [server mesh](mesh.md) (server and link nodes) and can instead point at a **Headscale** server; per-server egress (this page) is Tailscale-OAuth only and is unavailable with Headscale (`ErrWrongProvider`).

1. In the Tailscale admin console create **tags** and let the OAuth client's tag own them (policy file):

   ```jsonc
   {
     "tagOwners": {
       "tag:nebula":     ["autogroup:admin"],   // held by the OAuth client; owns the service tags
       "tag:nebula-svc": ["tag:nebula"],
       "tag:nebula-mesh": ["tag:nebula"]        // only if you use the server mesh
     },
     "acls": [
       // Let your people reach what NebulaCtrl publishes, and nothing from the nodes back:
       // add NO rule with src "tag:nebula-svc" unless the proxy must dial tailnet devices.
       // The proxy node DOES need to reach the devices you route to, e.g.:
       {"action": "accept", "src": ["tag:nebula-svc"], "dst": ["tag:homelab:*"]}
     ]
   }
   ```

   New tailnets default to allow-all: tighten the ACLs, otherwise every tailnet member can reach the proxy node and the proxy node can reach everything.
2. *Settings → OAuth clients → Generate*: scopes **`auth_keys`** (write) and **`devices:core`** (write), restricted to the owner tag (`tag:nebula`). Optional `feature_settings:read` lets the test report whether HTTPS certificates are enabled, and optional `policy_file:read` lets the [mesh](mesh.md#the-acl-mode) run its read-only drift check. Do **not** grant `all`, `dns` or `users`. **`policy_file` (write) is requested only if you opt in to the mesh's managed ACL push on Tailscale; a token with it can rewrite your entire tailnet policy, so leave it off unless you want that** (the default mesh mode is advisory: NebulaCtrl shows the rules and you paste them).
3. In NebulaCtrl save the client id, the secret, the tailnet (`-` = the client's own tailnet) and the tags (default `tag:nebula-svc`; allowed tags must include the defaults, and `tag:nebula-mesh` if you use the [server mesh](mesh.md#providers)). The secret is sealed with `NEBULACTRL_SECRET_KEY`, write-only (`secretSet: true` in responses; omit `clientSecret` on later PUTs to keep it) and absent from logs and audit entries (`integration.tailscale.update` lists changed field names only).
4. **Test connection** (`POST /integrations/tailscale/test`): exchanges a token, lists devices, mints a 60-second single-use key with the default tags and revokes it. No residue. If Tailscale rejects 60 seconds, it retries with 1 day.

The integration cannot be removed while a server uses (or is staged to use) a managed tailnet (409 `integration_in_use`).

### Trust notes

- Whoever holds `proxy:write` and the integration can put a node with your **tag** on the tailnet; the tag's ACL is the boundary. Keep the tag's reach minimal and review the ACL.
- The OAuth secret can mint keys for every tag its owner tag owns. Rotate it (new secret, `PUT`) if it leaks, and revoke the client in the admin console.
- Auth keys live one hour, are single use, and never touch disk on the control plane. A command delivered twice mints twice; an unused key expires on its own.
- The [mesh](mesh.md) adds another tag, `tag:nebula-mesh`, for every server and link node of the team. Its generated rules are by default **advisory**: you paste them. Only if you opt in does NebulaCtrl write the policy (`policy_file` scope), and then the control plane holds a credential that can rewrite the whole policy; the mode is audited as `mesh.acl.managed.enable`, is refused if another team shares the tailnet, and replaces only rules whose every source and destination is a mesh-node IP.
- Nothing contacts Tailscale until Test, Deploy proxy (key mint), a device removal job, or the device picker.

## Limitations

- **Not verified against a real tailnet.** The Tailscale API client is tested against a fake server, and the container shape follows Tailscale's documented kernel-mode setup. Exercise it on a staging tailnet first.
- No renewal for custom certificates. For automatic issuance, including wildcards, see [DNS-01 and wildcard certificates](dns01-wildcard-certificates.md).
- The upstream policy and the tailnet MagicDNS/host-gateway toggles take effect per server, not per route.
- A removed tailnet device is deleted by a background job; if the Tailscale API is unreachable the job retries (10 attempts) and then gives up, leaving the device for you to delete in the admin console.
- Tailscale-side name clashes (`name-1`) are surfaced, not prevented.
