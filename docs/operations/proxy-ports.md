---
title: Proxy ports
description: "Move the proxy's HTTP and HTTPS ports from the server, with a plan and a confirm, and what that means for Let's Encrypt."
section: Servers
order: 42
---

# Proxy ports

Every server's Traefik publishes **HTTP** and **HTTPS** (tcp, plus udp for HTTP/3) on host ports. The agent starts with `--proxy-http-port` and `--proxy-https-port` (80 and 443 by default; the installer writes them into the agent's systemd unit). The **Proxy ports** card on **Servers, the server, Proxy** moves them later, without touching the host: for example to run NebulaCtrl beside another proxy first, and to take over 80 and 443 at cutover.

It follows the same staged pattern as the other server-level proxy settings (see [Certificates, upstream policy and tailnet egress](proxy-tailnet-certificates.md)): you stage a change, the plan says what it does, and **Deploy proxy** applies it.

## Where the ports come from

The proxy view (`GET /servers/{id}/proxy`) has a `ports` object:

| Field | Meaning |
|---|---|
| `appliedHttp`, `appliedHttps` | The host ports the proxy listens on now, as the agent reports them in its heartbeat |
| `source` | `flag`: the agent's own service flags. `server`: this server setting. Empty until the agent reports |
| `http`, `https` | The ports a Deploy proxy would apply: the staged setting, else the agent's own |
| `setHttp`, `setHttps` | The staged setting (`null` = the agent's own port) |
| `agentHttp`, `agentHttps` | The ports of the agent's flags: what **Reset to agent defaults** returns to |
| `staged` | The setting differs from the applied one and awaits Deploy proxy |
| `busyOther` | Of 80 and 443, the ports another process on the host holds right now (reported by the agent) |
| `supported` | The agent can change its ports from the server (capability `proxy.ports`) |

A server setting **overrides the agent's flags**, and the agent keeps it across restarts. Re-running the agent installer with other `--proxy-http-port` / `--proxy-https-port` flags then changes nothing: use **Reset to agent defaults** first, or change the setting here. Without a setting the flags rule.

## Change the ports

Only the **instance administrator** (the first account) can change them, because a wrong port can cut the dashboard off. Everyone with `servers:read` sees them.

```bash
# Preview: the plan of the deploy as if the ports were staged. Stores nothing.
curl -X POST $URL/api/v1/servers/$ID/proxy/plan -H "Authorization: Bearer $TOKEN" \
  -d '{"ports": {"http": 80, "https": 443}}'

# Stage them (needs proxy:write and the instance administrator).
curl -X PATCH $URL/api/v1/servers/$ID/proxy/settings -H "Authorization: Bearer $TOKEN" \
  -d '{"ports": {"http": 80, "https": 443}}'

# Apply (recreates the proxy container).
curl -X POST $URL/api/v1/servers/$ID/proxy/deploy -H "Authorization: Bearer $TOKEN" -H "Idempotency-Key: $(uuidgen)" -d '{}'
```

`0` in `ports.http` or `ports.https` resets that port to the agent's own. A token can stage ports only with the `admin` scope and only when its creator is the instance administrator. The change is audited as `server.proxy.update` with the keys `httpPort` and `httpsPort` and their new values (`null` for a reset); the deploy is audited as `server.proxy.deploy` with `httpPort` and `httpsPort` (`0` = the agent's own).

Refused with `422`:

| Code | When |
|---|---|
| `invalid_proxy_ports` | A port outside 1 to 65535, HTTP and HTTPS equal, or a port equal to the proxy's admin port (9000) |
| `proxy_port_clash` | A TCP route service reserves one of them (a UDP route clashes only with the HTTPS port) |
| `agent_too_old` | The agent lacks `proxy.ports` |

Also `403 instance_admin_required` for everyone else.

### What the plan says

A changed port adds `proxy.httpPort` / `proxy.httpsPort` to the plan's diff and sets `restartRequired`: *Changing a published port recreates the proxy container (about 1 second of dropped connections).* Warnings:

- **A port another process holds**, from the agent's `busyOther`: the agent refuses to move onto it (see below).
- **The dashboard**, when the server runs the NebulaCtrl dashboard: `NEBULACTRL_PUBLIC_URL` and the agent's own URL are not changed, but the dashboard is then served on the new HTTPS port.
- **Let's Encrypt HTTP-01**, once per affected domain (see below).

### What the agent does

Before it touches the running container, the agent checks that every web port the proxy does not publish yet is free: no process listens on it and no other container publishes it (Coolify's proxy, for instance). If one is taken the deploy **fails with a result that names the port** (`port 443 (HTTPS, tcp) is already in use on this server by the container coolify-proxy; the proxy keeps running on :8080 and :8443 ...`) and the old container keeps serving. Otherwise it recreates Traefik; if the new container still cannot start (a race), the previous container is created again, exactly as for a failed TCP/UDP port. The applied ports, the `source` and the redirects' public HTTPS port follow the change.

## Let's Encrypt HTTP-01 needs port 80

Let's Encrypt validates HTTP-01 on **port 80** only. While the proxy listens elsewhere, a domain with TLS `auto`, no DNS-01 resolver, no covering wildcard or uploaded certificate and no Cloudflare Tunnel cannot get its certificate. A name in a zone of your connected Cloudflare account has a DNS-01 resolver automatically once Deploy proxy applied it, so it is not affected. The domain's certificate status (`cert.warning` of the domain view), the service deploy plan and the proxy plan say:

> This server's proxy listens on :8080, not :80: Let's Encrypt HTTP-01 cannot validate app.example.com. Use a DNS-01 resolver (Servers, Proxy, DNS-01) for this domain.

Use [DNS-01 or wildcard certificates](dns01-wildcard-certificates.md) to issue every certificate before the move to 80 and 443. The warning disappears when the proxy listens on 80.

## Moving beside another proxy, then to 80 and 443

1. Install the agent with `--proxy-http-port 8080 --proxy-https-port 8443`, or set 8080 and 8443 here.
2. Issue the certificates through DNS-01.
3. At cutover, stop the other proxy, then set the ports to 80 and 443 (or **Reset to agent defaults** if the flags say 80 and 443). The plan lists the restart; Deploy proxy applies it. If the other proxy still holds a port the deploy fails and the proxy keeps running on its alternate ports.

## Limitations

- The admin entrypoint (default 127.0.0.1:9000) is not part of this setting: it stays an agent flag (`--proxy-admin-port`).
- The control plane's own port and `NEBULACTRL_PUBLIC_URL` are not changed; update them yourself when the dashboard's server moves its HTTPS port.
