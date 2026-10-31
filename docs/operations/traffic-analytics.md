---
title: Traffic analytics
description: "What the proxy access log becomes: requests, status classes and origins."
section: Observability
order: 30
---

# Traffic analytics and request origins

NebulaCtrl counts requests at each host's proxy. The agent reads the Traefik access log, folds it into **per-minute aggregates** and ships only those. Raw log lines, client IPs, user agents, referrer URLs and query strings never leave the host and are never stored by the control plane. Retention and the other traffic settings are in [Monitoring](monitoring.md#traffic-analytics).

Views: service inspector → Analytics, server page → Traffic (the Overview shows the last 24 hours in four numbers), environment → Observability.

## Where requests come from

The "Where requests come from" card shows a dot map with the busiest places marked (size and brightness follow their share of requests), beside the top countries and, at the city level, the top cities. It appears in the service, server and environment views, and in the Observability summary.

### How it works

1. For every access-log line the agent looks the client IP (the same field the visitor count uses) up in a **local GeoIP database** inside the traffic aggregator.
2. Per minute and host it keeps counts per country (ISO 3166-1 alpha-2) and, at the city level, the busiest 20 cities with their coordinates rounded to 0.1° plus an "other" counter. Requests the database cannot place (private, loopback or unlisted addresses) go in an "unknown" counter.
3. Only those counts are sent. **The IP is never stored, logged or sent**: it is used for the lookup and dropped.
4. The control plane stores the counts next to the other traffic aggregates, in the same minute, hour and day tiers and under the same retention, and rolls them up by addition (exact for countries; cities keep the busiest 100 per row).

Reports gain a `geo` section: `level`, `source`, `attribution`, `countries` (`iso2`, `name`, `requests`, `share`) and `cities` (`name`, `country`, `lat`, `lon`, `requests`, `share`), plus `locatedRequests` and `unknownRequests`. `share` is the fraction of the located requests. When geolocation is off for every server in scope, `geo` is absent and `geoReason` says why (`geo_disabled` or `traffic_disabled`). The MCP tool `service_analytics` returns the same report, countries included, for a service.

### Settings (per server)

Server page → Settings → Monitoring ("Where requests come from"), or `PATCH /api/v1/servers/{serverId}` (scope `servers:write`).

| Field | Default | Effect |
|---|---|---|
| `geoEnabled` | on | Count origins. Needs `trafficAnalyticsEnabled`. Turning it off deletes the database on the server. |
| `geoSource` | `dbip` | `dbip` = DB-IP Lite, no key. `maxmind` = MaxMind GeoLite2 with the team's account (below). |
| `geoLevel` | `country` | `country` needs a ~8 MB database on the server. `city` adds city pins but needs ~130 MB of disk **per server**. Changing the level or source deletes the old database and downloads the new one. |
| `geoRefreshDays` | 7 | How often the agent checks for a newer release (1–90). It downloads only when a newer one exists. |

Settings reach the agent with its next reconcile (sent at once to online agents).

### Databases and attribution

| Source | Licence | Download |
|---|---|---|
| **DB-IP Lite** (default) | [CC BY 4.0](https://creativecommons.org/licenses/by/4.0/); credit "IP Geolocation by DB-IP" with a link to <https://db-ip.com> | `https://download.db-ip.com/free/dbip-{country\|city}-lite-YYYY-MM.mmdb.gz`, one file per month. Early in a month the new file may not exist yet; the agent then uses the previous month. |
| **MaxMind GeoLite2** | [GeoLite2 EULA](https://www.maxmind.com/en/geolite2/eula); credit "This product includes GeoLite2 data created by MaxMind" with a link to <https://www.maxmind.com> | `https://download.maxmind.com/geoip/databases/GeoLite2-{Country\|City}/download?suffix=tar.gz` with HTTP basic auth (account ID and licence key); the agent checks the release date with a `HEAD` request first (these do not count against MaxMind's download limit of 30 per day for GeoLite). |

The card shows the attribution wherever the data appears. Downloads happen **on each agent**, over HTTPS, straight from the vendor; the control plane never proxies the database. A server needs outbound HTTPS to `download.db-ip.com`, or to `download.maxmind.com` and MaxMind's presigned-URL host (`*.r2.cloudflarestorage.com`).

**MaxMind key.** Create a free MaxMind account, generate a licence key and enter the account ID and key once under any server's settings (or `PUT /api/v1/geo/maxmind`). They are stored **encrypted per team** with the instance secret key, are write-only (only the account ID is ever returned) and are delivered to agents as send-time secrets that are never persisted in a command or on the agent's disk. Removing the account is refused while a server still uses MaxMind.

### What the agent does with the database

- Downloads into `<state-dir>/geoip/`, **verifies** the file by opening it with the MaxMind DB reader (metadata, edition matches the level, the whole search tree and data section), then swaps it in atomically. A corrupt or wrong-edition download is discarded and the previous database keeps serving.
- Keeps only the current database. The old file is deleted after the swap; everything is deleted when geolocation is turned off, and the database is deleted when the level or source changes.
- Re-opens the stored database after an agent restart, so counting continues before the agent reconnects.
- Retries a failed download with backoff (1 min doubling to 1 h) and logs the reason (for example `MaxMind rejected the account ID or licence key`).
- Reports its database with every heartbeat: whether it is downloaded, the source and level, the release (DB-IP month, or MaxMind build date), the size on disk, when it last confirmed the database is current and the last error. The server's `geoStatus` field carries it, and **Settings → Monitoring → Database status** shows it. It is absent while the agent is offline or geolocation is off.

### Troubleshooting

| Symptom | Cause |
|---|---|
| Card says "No located requests yet" right after enabling | The agent is still downloading (the city database is ~130 MB), or the requests came from private addresses (for example a proxy or load balancer in front of the server records its own address as the client). Check **Settings → Monitoring → Database status**: it says whether the database is downloaded and the reason if the download failed. |
| Everything is "unlisted or private" | The client address Traefik logs is the address of an upstream proxy. Traefik must see the real client address (no extra hop, or a trusted forwarded-headers setup). |
| MaxMind selected but the card stays empty | Wrong account ID or key, or no outbound access to MaxMind. Check the agent log (`component=geoip`). |
| Cities missing | The server is at the country level, or the database edition lacks a city for that address (the request still counts for its country). |
