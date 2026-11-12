---
title: Service firewall
description: "Block or allow requests by IP address, country, path, method or header before they reach a service."
section: Applications
order: 135
---

# Service firewall

A service's firewall is an ordered list of **block** and **allow** rules the proxy applies to every request for every domain of the service, before the request reaches a container. Use it to keep scanners out, lock an admin path to your own network, or refuse countries you do not serve.

It lives in the service's **Settings → Firewall**. Like maintenance mode it is operational: **Apply firewall** changes the proxy at once, without a deployment. Changes are never staged or rolled back, and they survive deployments.

## How rules are evaluated

- Rules are checked **top to bottom**. The **first rule whose conditions all match decides**:
  - **Block** answers HTTP **403** with the "Access blocked" page, and the request never reaches the service.
  - **Allow** lets the request through and **skips the rules below it**.
- A request that no rule matches reaches the service.
- A rule matches when **all of its conditions** match. A condition with several values matches when **any** of them does. Every condition can be negated ("is not", "does not contain", "is missing").
- A switched-off rule is kept but never checked.

| What | Operators | Notes |
|---|---|---|
| IP address | is, is not | Addresses or CIDRs, IPv4 and IPv6 (up to 200 per condition). An IPv4-mapped IPv6 visitor matches IPv4 networks |
| Country | is, is not | ISO 3166-1 codes. Needs geolocation on the server (see below) |
| Path | starts with, is, contains, matches | The decoded path, without the query |
| Method | is, is not | GET, HEAD, POST, PUT, PATCH, DELETE, OPTIONS, CONNECT, TRACE |
| User agent | contains, matches | |
| Header | is, contains, matches, is present | By name (case-insensitive) |
| Query parameter | is, contains, matches, is present | By name |

**contains** ignores case. **matches** takes a Go regular expression (RE2: no lookarounds, no backreferences) and is not anchored, so add `^` and `$` yourself. A firewall holds at most 50 rules, each with at most 10 conditions.

**Try a request** under the rules describes a request (method, path, address, country, user agent, headers) and shows which rule decides, using the rules as edited, before you apply them.

The empty firewall offers recipes: probes for secrets (`/.env`, `/.git/`, `wp-login.php`, `xmlrpc.php`, `phpmyadmin`), self-declared attack tools (sqlmap, Nikto, Nuclei, masscan, zgrab), a path locked to your network, and blocked countries.

## How the proxy enforces it

- **Without country conditions** (the common case) the agent compiles every rule into Traefik routers next to the domain's own: there is no extra hop, and nothing else runs. A firewall router repeats its domain's host and path and leaves out the more specific paths other services serve on the same host, so one service's rules never touch another service's requests.
- **With country conditions**, or with IP conditions on a domain behind Cloudflare (a [Cloudflare Tunnel](cloudflare.md) or a domain locked to Cloudflare, whose visitor address arrives in `Cf-Connecting-IP`), the **edge guard** evaluates the firewall instead. It is the agent's own binary running as the `nebulactrl-guard` container in the proxy's network namespace, so only Traefik can reach it (`forwardAuth` on `127.0.0.1:83`). It adds about a millisecond per request, and only on the services that need it. The agent creates the guard when a route needs it, gives it the rules and the server's GeoIP database, and removes it when no route needs it any more.
- The Settings page says which of the two runs ("in its own routing, with no extra hop" or "by a small guard next to the proxy").

### Country rules and geolocation

Country conditions read the server's GeoIP database, the one [traffic analytics](traffic-analytics.md) uses for request origins. Turn on traffic analytics and geolocation in the server's settings first: the free DB-IP country database needs no account. Saving a country rule for a service on a server without geolocation is refused (`422 geo_required`). Until the database is downloaded, the guard skips the rules that match countries rather than guess, so a "block everyone except Germany" rule never locks every visitor out.

## API

- `GET /api/v1/services/{serviceId}/firewall` (scope `projects:read`) returns `{rules, enforcement, hosts, deployed, server, geoAvailable, supported}`. `enforcement` is `none`, `routes` or `guard`.
- `PUT /api/v1/services/{serviceId}/firewall` with `{rules: [...]}` replaces the list (scopes `projects:write` and `proxy:write`; an empty list removes the firewall). A rule is `{id, name, action: "block" | "allow", disabled?, conditions: [{field, op, name?, values?, not?}]}`; a rule without an `id` gets one. It is audited as `service.firewall.update`, and a production service asks for approval when a token changes it (see [production approval](../reference/mcp.md#production-approval)).
- `422 invalid_firewall` names the rule and condition at fault, and `422 agent_too_old` means the server's agent predates firewalls (capability `proxy.firewall`). TCP and UDP route services have no firewall (`422 firewall_unsupported`).
