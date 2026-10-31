---
title: Status pages
description: "Public status pages on your own domain: components, hostname and DNS, publishing, incidents and maintenance windows, and what is public or never shown."
section: Observability
order: 43
---

# Status pages

A status page tells your customers whether your services work, without giving them a login. It shows the services you choose (its **components**) under names you choose, 90 days of availability per component, the incidents you write, and the maintenance you announce. It is served read-only on **your own hostname**, for example `status.example.com`, by the control plane's status listener. The dashboard is never reachable through that hostname.

The state of each component comes from [uptime monitoring](uptime.md): the probes that run from each server every 30 seconds. Incidents and maintenance are written by people. A probe-detected outage does **not** create a public incident by itself; it turns the component red, and you decide what to say about it.

Everything here needs the **admin scope** to change (workspace owners and admins, like [notification channels](notifications.md)) and the `projects:read` scope to read. A token restricted to some projects can read pages but not change them, and sees no service, environment or project names of projects it cannot access. Every change is audited and refreshes the web UI live.

## Create a page

**Status pages** in the web UI, or the API:

```sh
curl -X POST "$NEBULACTRL_URL/api/v1/status-pages" -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' -H 'Idempotency-Key: status-1' \
  -d '{"title": "Acme status", "hostname": "status.acme.com", "description": "Live status of the Acme platform."}'
```

| Field | Rules |
|---|---|
| `title` | 1-100 characters, the page heading |
| `slug` | lowercase letters, digits and single hyphens, up to 50 characters, unique in the workspace. Default: derived from the title (`acme-status`, then `acme-status-2`, ...) |
| `description` | up to 500 characters, plain text |
| `footer` | up to 500 characters, plain text with line breaks, shown under the page (a contact address, a link to your social account). It is never interpreted as markup |
| `hostname` | optional at first, see [Hostname and DNS](#hostname-and-dns) |
| `showUptime` | show the availability percentages next to the day bars. Default `true` |
| `theme` | `auto` (the visitor's preference, default), `light` or `dark` |
| `fromProjectId` | optional: start with a project's production services that have a public domain, see [Start from a project](#start-from-a-project) |

A new page is **unpublished**: nothing is served yet. `PATCH /status-pages/{pageId}` changes any field later (`GET /status-pages`, `GET /status-pages/{pageId}`, `DELETE /status-pages/{pageId}` complete the set). Deleting a page withdraws its route; incidents and maintenance windows that were posted to it stay.

### Start from a project

Send `fromProjectId` (the **Start from project** picker of the new-page dialog) to start the page with a project's components instead of an empty list. The page gets the services of the project's **production environment** that have **at least one public domain**, in name order, each under its service name and in no group. A domain is public when it serves the site (a redirect does not), has no IP allow list, and its service is not Tailnet-only. Other environments (staging, previews), deleted services and services without such a domain are left out; at most 100 are added. Everything is editable afterwards (`PUT /status-pages/{pageId}/components`). A project of another workspace is `422 unknown_project` and nothing is created. The audit entry of the page carries the project and the number of components.

## Components

A component is a service shown on the page. Set them all at once, in display order:

```sh
curl -X PUT "$NEBULACTRL_URL/api/v1/status-pages/$PAGE/components" -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"components": [
        {"serviceId": "...", "displayName": "Website", "groupName": "Customer-facing"},
        {"serviceId": "...", "displayName": "API",     "groupName": "Customer-facing"},
        {"serviceId": "...", "groupName": "Internal"}
      ]}'
```

- `PUT` **replaces** the list. Send the full ordered list; the order is the display order.
- Every service must be a live service of the workspace (`422 unknown_service`), at most once per page (`422 duplicate_component`), at most 100 components.
- `displayName` is the **only** name the public sees. It defaults to the service's name, may not be empty and is at most 60 characters. Name services for your customers ("API", not `acme-api-prod-eu`).
- `groupName` (up to 60 characters) groups components: ungrouped ones come first, then the groups in the order they first appear. Empty means ungrouped.
- `url` (optional, up to 300 characters) makes the component's name a link on the public page, for example to its documentation. It must be an absolute **https** URL with a host and no credentials (`422 invalid_url`); `http:`, relative and `javascript:` URLs are refused. Empty means no link.
- A service that is deleted disappears from the page.

## Hostname and DNS

The `hostname` is lowercased and must be a valid DNS name with at least two labels (not an IP address). It must be unique and:

- must not be the dashboard's own host (`409 hostname_reserved`);
- must not be a hostname of a service domain, and must not lie in a Cloudflare zone another team connected (`409 hostname_conflict`, "the hostname … is not available": the answer is the same for every reason, so it never says whether, or which team, uses a name; see [Cloudflare](cloudflare.md#connect-cloudflare));
- and the other way around: a service domain cannot take a hostname a status page uses (`409 domain_conflict`, the same "not available" answer, when you add the domain).

The page is served by the control-plane server, so the hostname's DNS must point there. `GET /status-pages/{pageId}/dns` tells you:

```json
{
  "hostname": "status.acme.com",
  "records": [{"type": "A", "name": "status.acme.com", "value": "203.0.113.10"}],
  "resolved": [],
  "status": "missing",
  "message": "No A or AAAA record found for status.acme.com yet.",
  "proxied": false,
  "cloudflareRecord": false,
  "nameChangedAt": "2026-10-30T12:00:00Z"
}
```

`records` are the control-plane server's public address as the dashboard's domain DNS guidance names it (an `A` record for an IPv4 address, `AAAA` for IPv6); `resolved` is what the name resolves to right now. `status` is:

| Status | Meaning |
|---|---|
| `ok` | the hostname resolves to the control-plane server and nowhere else |
| `mismatch` | it resolves, but to other addresses too or instead (a record proxied through a CDN shows its addresses here) |
| `missing` | no record yet |
| `unknown` | the control-plane server has no public IP set, or the lookup failed |

The check asks the zone's authoritative nameservers (recursion off), not the control plane's own resolver, so a cached "no such name" on the host never shows. If none of them answers it asks 1.1.1.1 and 8.8.8.8, then the host's resolver. Answers are kept for 60 seconds; `?fresh=true` (the dashboard's **Recheck**) skips that cache. `cloudflareRecord` is `true` when Cloudflare's API confirmed the page's record exists: a `missing` or `mismatch` hostname then only waits for DNS caches (up to 30 minutes for a new name), and the dashboard says so instead of asking you to create records.

`nameChangedAt` is the newer of the page's last edit (where the hostname is set) and NebulaCtrl's last change to its Cloudflare record. For 30 minutes after it, the dashboard adds a line under a passing check: a resolver on your own network may still remember the new name as missing, and it clears by itself.

When the hostname is not in a zone of a connected Cloudflare account, create the record at your DNS provider; changes can take a while to spread. The certificate is issued automatically (HTTP-01) the first time the hostname is served, which needs the record in place and ports 80 and 443 reachable. A hostname in a connected Cloudflare zone needs no record from you and gets its certificate through DNS-01 (see [Cloudflare DNS](#cloudflare-dns)).

### Cloudflare DNS

When the hostname is in a zone of the team's [Cloudflare connection](cloudflare.md), NebulaCtrl creates the DNS records itself, the way it does for service domains. There is no per-page switch: management is always on for such a hostname. The page's Cloudflare panel reads "NebulaCtrl manages this record in your Cloudflare zone *zone*." Without a connection, or for a hostname in none of the connected zones, the page editor shows the records above to create by hand and nothing else changes.

- **The records.** An `A` record per IPv4 address and an `AAAA` record per IPv6 address of the control-plane server's public IP (the server may have both). A Cloudflare Tunnel is never used for a status page: the tunnel's ingress only carries service hostnames. Every record NebulaCtrl creates is marked `nebulactrl:<pageId>` (and tagged where the plan allows) and only a marked record that NebulaCtrl recorded creating is ever modified or deleted. A record NebulaCtrl did not create is never touched by a sync, a sweep or a status read. One that already points at the right address is reported as `present` and offers **Adopt record** (marks it as managed and applies the proxy setting). One that differs from the wanted record or cannot sit next to it (a `CNAME` where `A`/`AAAA` records are wanted, a second `A` record) is reported as a `conflict` (**Conflicting record**, with what exists and where it points) and offers **Replace with NebulaCtrl's record**: NebulaCtrl deletes the blocking or surplus unmarked records of the conflicting types, then marks or points one record per address, and audits each change (`dns.record.update` with `adopted: true`, `dns.record.delete` with `replaced: true`, `dns.record.create`). `wrong_target` only means NebulaCtrl's own record drifted; the next sync corrects it. The ownership rules are those of [service domains](cloudflare.md#ownership-only-records-nebulactrl-created-are-touched).
- **Settings** (per page): **Proxied** (the orange cloud) and **Lock the origin to Cloudflare** (default on). A hostname that starts being managed (a new page in a connected zone, or an existing page when the connection is added and it had no zone yet) is created **proxied**; a private (non-public) server IP cannot be proxied and shows the usual warning. A page that already has a record keeps its current proxy setting: a DNS-only page is never flipped. A page that had management switched off in an earlier version is managed from the next sync.
- **They follow the server.** The records are re-synced when the control-plane server's public IP changes, when the hostname changes (the old hostname's records NebulaCtrl created are deleted) or is removed, and when the page is deleted; a periodic sweep (every ten minutes) repairs drift. A failing sync is shown as `error` in the status, retried in the background, and announced once as *DNS sync failed*.
- **A proxied record is correct.** The [DNS check](#hostname-and-dns) of a page whose record NebulaCtrl manages as proxied expects Cloudflare's addresses: it is `ok` (*Resolves through Cloudflare*) with `"proxied": true` when every address the hostname resolves to is in Cloudflare's published ranges. While the record still points at the server (not proxied yet) it is `ok` too, with `"proxied": false`. Anything else is a `mismatch`.
- **The lock.** A page that is **proxied**, **published**, not opted out and in a connected zone is **locked to Cloudflare**, exactly like a service domain: the status route on the control-plane server accepts only connections from Cloudflare's ranges (`403` otherwise), requires Cloudflare's client certificate when the zone has Authenticated Origin Pulls on (NebulaCtrl turns it on, if the token may), and trusts the forwarded visitor address only from Cloudflare. Layers, states and the IP ranges are described in [Cloudflare → Origin lock](cloudflare.md#origin-lock). The lock follows the proxied record: turning Proxied off, opting out, unpublishing or deleting the page opens the route again (the route table is re-sent before the DNS record changes). Only the status hostname is locked; the dashboard's own route and the other service routes are unaffected. An unpublished page has no route yet: its lock state says `not_published` and it is locked from the moment it is published.
- **The certificate.** For a hostname in a connected zone NebulaCtrl obtains the certificate with the `cloudflare` DNS-01 resolver automatically (proxied or DNS only), using the connection's token. It does so only after NebulaCtrl's own DNS sync wrote the page's record with that token (status `managed`, no sync error); before that, for a `present`, `conflict` or `missing` record, and after a refused DNS write (403) the hostname stays on HTTP-01. The resolver is applied with **Deploy proxy** (the plan shows a proxy restart); until then the hostname keeps HTTP-01. The server needs an ACME email and a DNS-01 capable agent. A hostname that already has a valid HTTP-01 certificate keeps it and renews it over HTTP-01, because Traefik does not request a new certificate for a name that already has one; DNS-01 issues for hostnames without a certificate. See [Names in a connected Cloudflare zone](dns01-wildcard-certificates.md#names-in-a-connected-cloudflare-zone-automatic).

```sh
# Cloudflare state of the hostname: zone, records, lock layers (changes nothing)
curl "$NEBULACTRL_URL/api/v1/status-pages/$PAGE/cloudflare" -H "Authorization: Bearer $TOKEN"
# Proxy it (and lock it); omitted fields stay as they are
curl -X PUT "$NEBULACTRL_URL/api/v1/status-pages/$PAGE/cloudflare" -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' -d '{"proxied": true}'
# Create or correct NebulaCtrl's own records; "adopt": true also adopts a matching record or replaces a conflicting one (without it: 409 adoption_required)
curl -X POST "$NEBULACTRL_URL/api/v1/status-pages/$PAGE/cloudflare/fix" -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' -d '{"adopt": true}'
```

The answers have the shape of the domain DNS endpoints (`GET`, `PUT /domains/{id}/dns`, `POST /domains/{id}/dns/fix`): `status` (`managed`, `present`, `conflict`, `missing`, `wrong_target`, `not_in_zone`), `zoneName`, `manage` (read-only: `true` in a connected zone), `proxied`, the records NebulaCtrl wants (`desiredRecords`) and the ones in the zone (`records`), `warnings` (a Flexible SSL/TLS mode breaks a proxied record) and `originLock` with `enabled`, `reason`, `aop`. `PUT` accepts only `proxied` and `originLock`; an unknown field such as `manage` gets `422`. `422 no_hostname` while the page has no hostname, `422 not_configured` or `not_in_zone` for **Fix** without a connection or zone, `409 adoption_required` when a record NebulaCtrl did not create differs and `adopt` is not set.

## Publish

```sh
curl -X PATCH "$NEBULACTRL_URL/api/v1/status-pages/$PAGE" -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' -d '{"published": true}'
```

Publishing needs a hostname (`422 publish_needs_hostname`) and at least one component (`422 publish_needs_components`). A published page needs its components too: replacing them with an empty list, or removing the hostname, is refused until you unpublish. Publishing, unpublishing, changing the hostname of a published page and deleting it **re-send the control-plane server's route table**, so the page appears (or is withdrawn) within moments, with no deployment. An unpublished page answers `404` on its hostname.

## What a visitor sees

- **Overall state**: the worst of the component states and of the impact of the active incidents, shown as a banner.

  | State | When |
  |---|---|
  | Major outage | a component is down and every component is down, or an active incident has impact `critical` |
  | Partial outage | some components are down, or an active incident has impact `major` |
  | Degraded | a component is degraded, or an active incident has impact `minor` |
  | Maintenance | nothing is worse, and a component is in maintenance or a window posted to the page is active |
  | Operational | everything else |
  | Unknown | no component has data yet (not probed, paused, or the agent stopped reporting less than 5 minutes ago) |

- **Components**, grouped, each with its current state (`operational`, `degraded`, `major_outage`, `maintenance`, `unknown`), its availability over the last 24 hours and the last 90 days and a bar of 90 UTC days. A component with a `url` shows its name as a link to it. A day shows its availability, an estimate of the outage (failed checks times 30 seconds), whether maintenance took part, and the titles of the incidents that affected the component that day.
- **Availability** is (up + degraded checks) / (all checks minus maintenance checks). Days or periods without checks (agent offline) have no data, never 0%. Next to the 90-day figure each component shows the availability of the **last 24 hours**, computed the same way from the minute rollups; no checks in that time (or only maintenance) means no figure.
- **Active incidents** with their timelines, **scheduled and active maintenance** that starts within 14 days, and **past incidents** resolved in the last 30 days.
- **Footer**: the plain text you set on the page, under the content (line breaks kept, never interpreted as markup or links).

### What is never shown

The page is built from a fixed view model that holds only the text above. It never contains: service, environment, project or server names (only your display names), IDs, hostnames of your services, error messages or probe details, latency, replica counts, deployments, who wrote an incident, or anything from probe-detected incidents. Incident and maintenance text is plain text, shown escaped. Incidents and maintenance windows carry an opaque **key** (the first 16 hex characters of a SHA-256 of their kind and ID) for anchors and feeds; it is stable and cannot be turned back into the ID. The only links on the page are the https URLs you set on components.

## Incidents

Write an incident when you want to say something. It is posted to the pages you choose and shows on each page that has the affected components.

```sh
# Open (the body is the first update of the timeline)
curl -X POST "$NEBULACTRL_URL/api/v1/status-incidents" -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' -H 'Idempotency-Key: inc-1' \
  -d '{"title": "Elevated API errors", "impact": "major", "status": "investigating",
       "body": "We are looking into failing API requests.", "pageIds": ["..."], "serviceIds": ["..."]}'

# Post an update; status resolved closes it
curl -X POST "$NEBULACTRL_URL/api/v1/status-incidents/$INCIDENT/updates" -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' -d '{"status": "resolved", "body": "A bad deploy was rolled back."}'
```

- **Impact** (`none`, `minor`, `major`, `critical`) moves the overall state of the pages while the incident is not resolved (see the table above). **Status** is `investigating`, `identified`, `monitoring` or `resolved`.
- `POST /status-incidents/{incidentId}/updates` adds a timeline entry and moves the incident to that status. `resolved` sets `resolvedAt`; a later update with another status reopens it.
- `PATCH /status-incidents/{incidentId}` changes the title, impact, pages or services (an empty `pageIds` list withdraws the incident from every page). `GET /status-incidents` filters by `status` (or `active`) and `pageId`; `DELETE` removes the incident and its timeline from the pages.
- Bodies are plain text of up to 4000 characters; titles up to 120.

## Maintenance windows

A window announces planned work and puts its services into maintenance while it runs: no `uptime.down` alerts and their checks are left out of the availability, so planned work does not cost you availability.

```sh
curl -X POST "$NEBULACTRL_URL/api/v1/maintenance-windows" -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' -H 'Idempotency-Key: mw-1' \
  -d '{"title": "Database upgrade", "description": "The API is read-only for a few minutes.",
       "startsAt": "2026-11-03T22:00:00Z", "endsAt": "2026-11-03T23:00:00Z",
       "serviceIds": ["..."], "pageIds": ["..."]}'
```

- `endsAt` must be after `startsAt` (`422 invalid_window`) and in the future (`422 window_in_past`).
- Pages announce a window it is posted to from **14 days ahead**, and while it is active.
- `GET /maintenance-windows?phase=upcoming|active|past` (past: ended or canceled) and `pageId` filter the list.
- `PATCH /maintenance-windows/{windowId}` edits a window that is not over (`409 window_ended`, `window_canceled`).
- `POST /maintenance-windows/{windowId}/cancel` ends it now and sets `canceledAt`; the services leave maintenance and the pages stop announcing it. Cancelling twice changes nothing.
- `DELETE /maintenance-windows/{windowId}` removes it.

## Permissions and audit

| Action | Needs |
|---|---|
| List and read pages, incidents, windows, DNS check, Cloudflare DNS status | `projects:read` |
| Create, change, publish, delete; change a page's Cloudflare settings or fix its records | `admin` scope (workspace owner or admin), and a token not restricted to projects |

Audit actions: `status_page.create`, `status_page.update`, `status_page.delete`, `status_page.components`, `status_page.dns.update` (the Proxied and origin lock settings, with the new values); `status_incident.create`, `status_incident.update`, `status_incident.post`, `status_incident.delete`; `maintenance_window.create`, `maintenance_window.update`, `maintenance_window.cancel`, `maintenance_window.delete`. The DNS records NebulaCtrl creates, changes or deletes for a page are audited as `dns.record.create`, `dns.record.update` (`adopted: true` when an existing record was adopted) and `dns.record.delete` (`replaced: true` when it was deleted by **Replace with NebulaCtrl's record**) on the `status_page` resource; turning on Authenticated Origin Pulls in the zone is audited as `cloudflare.zone.aop.enable`. Incident text is not copied into the audit log.

The MCP server exposes the reads to agents (including `getStatusPageCloudflare`); every write is denied, because a status page is a public statement that a person makes, and so is where its hostname points and whether it is proxied.

## Operations

| OperationID | Request |
|---|---|
| `listStatusPages`, `createStatusPage` | `GET`, `POST /status-pages` |
| `getStatusPage`, `updateStatusPage`, `deleteStatusPage` | `GET`, `PATCH`, `DELETE /status-pages/{pageId}` |
| `setStatusPageComponents` | `PUT /status-pages/{pageId}/components` |
| `checkStatusPageDNS` | `GET /status-pages/{pageId}/dns` |
| `getStatusPageCloudflare`, `updateStatusPageCloudflare` | `GET`, `PUT /status-pages/{pageId}/cloudflare` |
| `fixStatusPageCloudflare` | `POST /status-pages/{pageId}/cloudflare/fix` |
| `listStatusIncidents`, `createStatusIncident` | `GET`, `POST /status-incidents` |
| `getStatusIncident`, `updateStatusIncident`, `deleteStatusIncident` | `GET`, `PATCH`, `DELETE /status-incidents/{incidentId}` |
| `postStatusIncidentUpdate` | `POST /status-incidents/{incidentId}/updates` |
| `listMaintenanceWindows`, `createMaintenanceWindow` | `GET`, `POST /maintenance-windows` |
| `getMaintenanceWindow`, `updateMaintenanceWindow`, `deleteMaintenanceWindow` | `GET`, `PATCH`, `DELETE /maintenance-windows/{windowId}` |
| `cancelMaintenanceWindow` | `POST /maintenance-windows/{windowId}/cancel` |

Creates take an `Idempotency-Key` header and replay the original response for 24 hours.

## How public pages are served

A published page is not served by the dashboard. The control plane runs a **second, separate HTTP listener** (the *status listener*) that does one thing: look a page up by the request's `Host` header and render it. The dashboard, the API, `/agent/v1/*`, `/mcp`, the installer scripts and the SPA are not mounted on it, so there is nothing else to reach through a status hostname.

```
visitor ──443──▶ control-plane server's Traefik ──▶ nebulactrl:8081 (status listener)
                  status.acme.com  (public, TLS)       Host → page → HTML / JSON / badge
dashboard users ─▶ nebula.acme.com (allow list, TLS) ─▶ nebulactrl:8080 (dashboard listener)
```

For every published page with a hostname the control plane adds one route to the route table of the **control-plane server only** (re-sent whenever a page is published, unpublished, renamed or deleted):

- the page's hostname, to the status upstream, with a `/healthz` upstream health check;
- **public**: no allow list, whatever you set for the dashboard; basic security headers (`X-Frame-Options`, `nosniff`);
- TLS: automatic HTTP-01 by default. A hostname in a zone of the **control-plane team's own** connected Cloudflare account uses the [automatic Cloudflare DNS-01](#cloudflare-dns) resolver instead. The dashboard's own DNS-01 credential (Admin → Dashboard access) is the instance's: it is used for the dashboard's hostname only, **never for a status page**, whatever its hostname, so a team cannot make the instance issue certificates for names under its domain. A team that wants DNS-01 for its pages connects its own Cloudflare account; every other page uses HTTP-01 (its DNS must point at the control-plane server);
- a stable route id derived from the page id, so the proxy's objects and the certificate survive route rebuilds and restarts;
- never a route for a hostname another route already serves (the dashboard's, a service domain's).

### The listener and its settings

| Variable | Default | Meaning |
|---|---|---|
| `NEBULACTRL_STATUS_LISTEN` | `:8081` | Address of the status listener inside the control-plane container. A second `http.Server` with its own timeouts; it shuts down gracefully with the main one. |
| `NEBULACTRL_STATUS_UPSTREAM` | `nebulactrl:8081` | `host:port` the control-plane host's proxy sends status hostnames to, reachable on the edge network. A leading `http://` is dropped. The installer writes it in domain mode. |

**Never publish the status port on the host.** The compose file has no `ports` entry for it and the installer's tests check that nothing listens on `8081` on the host: only the host's proxy, over the edge network, reaches the listener. If you run the control plane by hand, keep it that way (and keep `NEBULACTRL_STATUS_LISTEN` off public interfaces). Status routes exist only on the server flagged as the control plane (the host the installer enrolls); without one there is nothing to route the hostnames.

**Keep the page off your app hosts.** The page is only as available as the control-plane host. If your services run on that host too (the installer's default, **Accept workloads** on), losing the host takes the page down with them. Turn [Accept workloads](../installing/installer.md#accept-workloads) off on the control-plane host and run your services on other servers. Then a dead app server shows on the page: its components turn to **Major outage** after its agent has not been heard from for 5 minutes, or **Maintenance** while maintenance covers them.

### What the listener serves

Only `GET` and `HEAD`; everything else is `405`.

| Path | Response |
|---|---|
| `/` | the page: server-rendered HTML, complete without JavaScript. Every minute the script fetches the page again and swaps in what changed, so a collapsed group, the scroll position and keyboard focus stay put (it waits while a day tooltip is open and in a hidden tab); without script a `<noscript>` meta refresh reloads the page every minute |
| `/status.css`, `/status.js` | the stylesheet and the one small script (the day tooltip of the bars and the soft refresh), versioned by content hash and served from the page's own origin |
| `/status.json` | the same model as JSON with stable, public-safe field names (below) |
| `/feed.atom` | an Atom feed (below) |
| `/badge/status.svg` | badge of the whole page's state |
| `/badge/<slug>/status.svg`, `/badge/<slug>/uptime.svg` | badges of one component, by the slug of its display name (below); `404` for an unknown component |
| `/status.css`, `/fonts/*` | the stylesheet (versioned URL, cached for a year) and the self-hosted IBM Plex fonts taken from the embedded web UI build (the system font stack when the build has none) |
| `/healthz` | `200 ok` for any `Host`: the proxy's health check |
| anything else | `404` with a minimal page. An unknown, unpublished or invalid `Host` gets the same `404`. |

`/status.json` is written field by field, not marshaled from the internal model, so a field added later never reaches it by accident. Keys: `title`, `description`, `footer`, `status {state, label}`, `showUptime`, `generatedAt`, `groups[] {name, components[] {name, slug, url, status, uptime, uptime24h, days[] {date, status, uptime, downMinutes, maintenance, incidents}}}`, `activeIncidents[]`, `maintenance[]`, `pastIncidents[]` (incidents and windows carry an opaque `key`). `uptime` (90 days) and `uptime24h` are percentages truncated (never rounded up) to two decimals; with **Show uptime** off they, `downMinutes` and the day figures are left out and a day only has its band (`operational` 99.9% or more, `minor_issues` 99%, `degraded` 95%, `outage`, `maintenance`, `no_data`). `slug` is the component's name in badge URLs and `url` its link, only when it is an `https` URL. The JSON sends `Access-Control-Allow-Origin: *`, so other sites can fetch it.

### The page

- **Header**: title, the overall state as a banner with an icon and a text label, the description.
- **Components** in groups: a named group is a `<details open>` that a visitor can collapse (no JavaScript), components without a group are always shown. Each shows its state, a name that links to its `url` when the page has one (`rel="noopener noreferrer nofollow"`), its availability for the last **24 hours and 90 days** when **Show uptime** is on (a window without data is left out), and the 90-day bar. Each day of the bar shows a tooltip above it on hover, with the date, the state word and, when **Show uptime** is on, its availability and estimated outage, then the day's incidents; the page's script draws it, and without script the day keeps a native hover title. The bar is one tab stop that announces "Last 90 days: 99.95% available, 187 min down"; the arrow keys, Home and End move between days (the tooltip follows and a polite live region reads the day), Escape dismisses it, and a tap on a touch screen shows a day. A mouse click draws no focus ring on the bar; keyboard focus does.
- **Footer**: your footer text (plain text, escaped, line breaks kept), "Last updated" as a `<time datetime>` in UTC with the age at render time ("2 min ago"), a note that the page refreshes every minute, links to the JSON and the feed, and "Powered by NebulaCtrl".
- **Head**: `<meta name="description">` (the description cut at 155 characters, or the overall state when there is none), Open Graph title and description, and `<link rel="alternate" type="application/atom+xml">` for the feed.
- **Anchors**: every incident and window has an element id (`inc-<key>`, `mw-<key>`) that the feed links to.

The page follows the page's **theme**: `light`, `dark`, or `auto`, which follows the visitor's `prefers-color-scheme`. Every state has an icon shape and a text label, so colour is never the only cue. A component that an active incident names says "Incident in progress" beside its state. On narrow screens the bar shows the last 30 days and a line under it says the uptime figure covers 90.

### Atom feed

`/feed.atom` has one entry for every update of every incident on the page (active and the last 30 days) and one for every maintenance window it announces, newest first. An incident update is dated by when it was posted; a maintenance window is dated by when it was posted or last changed, never by its start, so a window scheduled for next week is not listed as next week's news. The window's own start and end are in the entry text. Entry ids are `urn:nebulactrl:<key>:<n>`, where `<key>` is the opaque key of the incident or window and `<n>` numbers an incident's updates from the oldest (`0`), so an entry keeps its id when later updates are posted and a reader never shows it twice. The text is plain, and the feed is cached like the page.

### Badges

Embeddable status badges, so a README or a dashboard only needs a URL:

| URL | Shows |
|---|---|
| `/badge/status.svg` | the page's overall state |
| `/badge/<slug>/status.svg` | a component's state |
| `/badge/<slug>/uptime.svg?window=24h` or `90d` (default) | a component's availability; `404` when the page hides uptime, `N/A` without data |

`<slug>` is the display name in lower case with every run of other characters replaced by one hyphen (`Customer API (EU)` is `customer-api-eu`); two components that would share a slug get `-2`, `-3`, in page order. `/status.json` lists each `slug`. Query parameters, all optional:

| Parameter | Effect |
|---|---|
| `label` | the left text (default: the component's name, `status` for the page, `<name> uptime (90d)`) |
| `upLabel`, `downLabel`, `maintenanceLabel` | the right text for an operational, a down and a maintenance state (defaults `Up`, `Down`, `Maintenance`; degraded, partial outage and no data have fixed texts) |
| `style` | `flat` (default), `flat-square` or `for-the-badge` |
| `color`, `labelColor` | fill of the right and left part: a name (`green`, `red`, `orange`, `yellow`, `blue`, `grey`, `brightgreen`, ...) or a hex value (`ff8800`, `#f80`); anything else is ignored. The text colour follows the fill |

Texts are cut at 60 (label) and 40 (value) characters and everything is escaped. An unknown component is `404`, an unknown `window` is `400`.

### Caching, rate limit and headers

- **Cache.** Each host's rendered page, JSON and feed are cached in memory for **20 seconds** (badges are drawn per request from the cached page), with one lookup per host at a time (concurrent visitors share it). A `status_page.updated` or `uptime.state.changed` event makes the next request rebuild it, at most once every 2 seconds per host, so a posted incident or a state change shows within moments even on a busy instance. When the database is down a page that expired within the last 10 minutes is still served. The page, the JSON and the feed carry `Cache-Control: public, max-age=30` and an `ETag` (`304` on `If-None-Match`) and are gzip-compressed when the client accepts it; **badges** carry `Cache-Control: public, max-age=300` and `Access-Control-Allow-Origin: *`. Errors and the `404` page are `no-store`.
- **Rate limit.** A token bucket of **60 requests per client address** (one per second sustained), then `429` with `Retry-After: 1`. The client is the **visitor**, worked out like the sign-in limits ([client addresses](../security/login-methods.md#client-addresses-and-rate-limits)): the last `X-Forwarded-For` entry when the peer is the proxy (a private or loopback peer), the TCP peer otherwise, and for a page served through Cloudflare the `CF-Connecting-IP` Cloudflare set (or the entry before the edge address), so visitors behind one Cloudflare edge address do not share a bucket. An IPv6 client is its `/64`. The table of clients is bounded; when it is full, new clients share one overflow bucket instead of growing it. `/healthz` and unknown paths are not counted.
- **Headers** on every response:

  | Header | Value |
  |---|---|
  | `Content-Security-Policy` | `default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' data:; font-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'`: no inline script or style, no framing, nothing from another origin |
  | `X-Content-Type-Options` | `nosniff` |
  | `Referrer-Policy` | `no-referrer` (the proxy's *basic* preset may replace it with `strict-origin-when-cross-origin`; the page's only external link carries `rel="noopener noreferrer"`) |
  | `X-Frame-Options` | `DENY` |
  | `Cross-Origin-Opener-Policy`, `Cross-Origin-Resource-Policy` | `same-origin` (`cross-origin` on the JSON and the badge) |
  | `Permissions-Policy` | camera, microphone, geolocation, payment off |

### Troubleshooting

| Symptom | Check |
|---|---|
| `404 Page not found` on the hostname | The page is unpublished, the hostname differs (it is matched case-insensitively, without the port), or the route did not reach the proxy yet: the control-plane server's route table (Servers → Proxy) lists it as `Status page <hostname>`. |
| `502` or `no available server` | The proxy cannot reach `NEBULACTRL_STATUS_UPSTREAM`: the control plane must be on the edge network as `nebulactrl`, and `NEBULACTRL_STATUS_LISTEN` must be the port of the upstream. |
| The browser shows the dashboard's login on the hostname | It should be impossible: a status hostname only ever routes to the status listener. Check that the hostname is not also a service domain (the control plane refuses that) and that no other proxy fronts the host. |
| Certificate errors | For a hostname outside a connected Cloudflare zone, its DNS must point at the control-plane server, with ports 80 and 443 reachable for HTTP-01. In a connected zone the certificate comes through DNS-01 once the page's record is `managed` (written by NebulaCtrl's own sync) and **Deploy proxy** has applied the resolver; until then, and after a refused DNS write, the hostname stays on HTTP-01. |
| `403 Forbidden` on the hostname from a browser that goes through Cloudflare | The page is proxied and locked, and the visitor came from an address the stored list of Cloudflare's ranges lacks: see **Cloudflare IP ranges** in [Cloudflare](cloudflare.md#the-ip-ranges). A request that skips Cloudflare (straight to the server's IP) gets `403` by design. |
| `525` from Cloudflare on the hostname | The page is locked with Authenticated Origin Pulls but AOP is off in Cloudflare; the next sync (within ten minutes) turns it on, or switch **Lock the origin to Cloudflare** off for the page while you investigate. |
| The hostname's DNS check says `mismatch` although the record is proxied | NebulaCtrl only expects Cloudflare's addresses for a page whose record it manages as **proxied** (the page's Cloudflare panel says so). A record you proxied by hand is not known to be this server's: press **Adopt record** (or **Replace with NebulaCtrl's record** when the status is a conflict). |
