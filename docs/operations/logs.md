---
title: Logs and log drains
description: "Runtime and request log history on each server, the Logs page, drains to Loki, OTLP, syslog and HTTP, and the optional 7-day central logs on the control-plane host."
section: Observability
order: 20
---

# Log history and log drains

Runtime logs (what your containers write to stdout and stderr) are **stored on the server that runs them** and can be **shipped from that server** to Loki, OpenTelemetry, syslog or any HTTP intake. [Request logs](#request-logs) (one line per request the proxy routed to a service) are stored on that server too, in a store of their own, and are **never shipped**. By default the control plane stores no log line of either kind: it asks the server's agent. The one exception is [central logs](#central-logs), which are off until a team turns them on and carry runtime lines only.

## Log history

Every agent captures the output of every managed service container, continuously. A viewer is not needed: closing the Logs tab discards nothing. Build and deploy output are separate: they are stored per deployment (10,000 lines), see [deployments](deployments.md).

| | |
|---|---|
| Where | `<state-dir>/logs/<serviceId>/` on the server (`/var/lib/nebulactrl-agent/logs` by default), compressed, nowhere else |
| How long | **7 days** by default (1–90) |
| How much | at most **500 MB per service** and **5 GB per server**, compressed on disk; the **oldest lines go first** (whole segments of at most 8 MB / 6 hours) |
| What | stdout and stderr of replicas of services; not task containers, helper containers or the proxy (the proxy's requests are [request logs](#request-logs)) |
| Configurable | **per server**: server page → Settings → Logs and disk, or `PUT /api/v1/servers/{serverId}/log-settings` (scope `servers:write`) |

Limits take effect at once; lowering them deletes the oldest segments within a minute. Turning **capture off** stops storing new lines; what is stored stays until it expires, and turning capture on again continues from where Docker still has the lines.

A container's output from before the agent first saw it is taken from Docker's own log (at most the retention window), so history starts with what Docker still has. Docker's own log rotation (`max-size` on the container) is independent of this store.

### Reading it

**Service → Logs** (the inspector tab) and the environment's [Logs page](#the-logs-page) have a **Live | History** switch and a time range (last 15 minutes … 7 days, or a custom range). History uses the same search box as the live view: the query is sent to the server and **evaluated there**, so searching 7 days does not move 7 days of log over the wire. The query language is the one documented in the search box: free text, `"phrases"`, `level:error`, `-service:web`, `@status:[400 TO 499]`, `@path.to.field:value` for JSON and logfmt lines, `OR`/`NOT`/parentheses, wildcards. The Go implementation on the agent is checked against the web one with a shared fixture (`tests/fixtures/logquery/cases.json`), so the same query matches the same lines in both.

API: `GET /api/v1/services/{serviceId}/logs/history` and `GET /api/v1/environments/{environmentId}/logs/history` (scope `projects:read`):

| Parameter | |
|---|---|
| `from`, `to` | RFC 3339; `from` inclusive, `to` exclusive. Default: everything the server keeps. |
| `q` | the search query |
| `limit` | 1–2000, default 500 |
| `order` | `desc` (default, newest first) or `asc` |
| `cursor` | `nextCursor` of the previous page |
| `sources` | comma-separated, a subset of `runtime,requests`; default `runtime`. Each item's `stream` is the source it comes from. The environment endpoint asks every (service, source) pair and merges by time; its cursor covers all of them |

The answer has `items` (`ts`, `line`, `serviceId`, `serviceName`, `replica`, `deploymentId`, `stream`), `nextCursor` when more may follow, `oldest`/`newest` (what the server still has), `capturing`, and `partial: true` when the agent stopped scanning before the page was full (a rare query over a lot of log; keep paging with the cursor). Secret values are masked like in the live view. For a request line, `line` is the JSON of the [stored request](#what-is-stored-per-request).

**History is unavailable while the server is offline.** The service endpoint answers `503 server_offline` and the UI says "History is stored on <server> and is unavailable while it is offline"; the environment endpoint lists the affected services in `unavailable` and still returns the others. An agent older than this feature answers `409 agent_outdated`. For the `requests` source, a server whose agent predates request logs is listed in `unavailable` with reason `agent_outdated` (history and facets alike) and is not asked, until its agent is updated; its runtime lines still come back.

A service that moved to another server keeps its older history on the old one; the API reads the server the service is placed on now.

### Facets

Counts over a time range, evaluated on the agent like History: `GET /api/v1/services/{serviceId}/logs/facets` and `GET /api/v1/environments/{environmentId}/logs/facets` (scope `logs:read`). The Logs page's sidebar, histogram and autocomplete use them.

| Parameter | |
|---|---|
| `from`, `to` | RFC 3339; `from` inclusive, `to` exclusive. Default: the last 15 minutes |
| `q` | the search query; only matching lines are counted |
| `sources` | as for History: `runtime`, `requests` or both; default `runtime` |
| `buckets` | histogram buckets over the range, default 60, at most 240 |

The answer:

| Field | |
|---|---|
| `matched`, `scanned` | lines that match the query, and lines the agent looked at |
| `partial`, `partialFrom` | the agent stopped before the start of the range (a scan reads at most 64 MiB of decompressed log or 2 seconds, newest first); `partialFrom` is the oldest time it reached, and the counts describe `[partialFrom, to)` |
| `facets` | the built-in facets `level`, `stream`, `status`, `replica`, `deployment`, and for an environment also `service` (the service's name, with the lines it matched). Each is `{key, count, values: [{value, count}]}`, the 20 most frequent values first |
| `attrs` | the same for `@attribute` paths found in JSON and logfmt lines (at most 50 paths, 20 values each). A numeric one is `numeric: true` with `min` and `max` |
| `histogram` | `[{start, count, errors}]`; `errors` counts lines with level `error` or `fatal` or a status of 500 or more |
| `queryErrors` | why part of the query was not understood, when it was not |
| `unavailable` | services whose server could not answer, as in History; the other services are still counted |
| `capturing`, `requestsCapturing` | runtime capture and request logs are on |

Counts are summed across services and sources, and histograms by bucket (every agent gets the same range). Attribute values are masked like History lines. The service endpoint answers `503 server_offline` while the server is offline.

### Downloading logs

To attach a log to a bug report or keep a copy, download it as a file. Secret values are masked exactly as in the log views, and every download is audited (`service.logs.download`, `deployment.logs.download`); the audit entry records the range and format, never the content.

| | Service runtime log | Deployment build and deploy log |
|---|---|---|
| API (scope `logs:read`) | `GET /api/v1/services/{serviceId}/logs/download` | `GET /api/v1/deployments/{deploymentId}/logs/download` |
| UI | **Download** in the service's Logs tab (Live and History; History sends its time range and search) | **Download** above the log of an opened deployment |
| CLI | `nebulactrl services download-logs <service>` | `nebulactrl deployments download-logs <deployment-id>` |
| Source | the server's stored history (agent; 503 `server_offline` while it is offline, 409 `history_disabled` when capture is off) | the persisted lines (at most 10,000; `streams=all` adds the 500-line debug trace) |

Query parameters: `format` (`txt`, default, one `<time> <line>` per line, with `[build]`, `[deploy]` or `[debug]` added to deployment lines (runtime lines carry none); or `ndjson`, one `{ts, stream, line, …}` object per line), `from` and `to` (RFC 3339, `from` inclusive, `to` exclusive), `q` (the log search language, service logs only), `component` (Compose stacks, service logs only), `streams` (deployment logs only). Service lines come oldest first. A service download holds at most 200,000 lines: a longer range ends with a `# download truncated …` line (text) or `{"note": …}` object (NDJSON), so narrow the range. A read error after the first page ends the file with the same kind of note, because the bytes already sent cannot become an error response.

```sh
nebulactrl services download-logs acme/production/web --from 24h -o web.log
nebulactrl services download-logs web --format ndjson --grep 'level:error' > errors.ndjson
nebulactrl deployments download-logs 6f1c0d5e-… --streams all -o deploy.log
```

`--from`/`--to` take an RFC 3339 time, a date, or a duration ago (`90m`, `24h`). Without `-o` the file goes to stdout; with it the file is created with mode 0600, and only once the server answered, so a refusal leaves nothing behind.

A service with [processes](processes.md) shows every process, each line prefixed `<process> | `. Filter to one process with `process` (API), `--process` (CLI) or the process selector (dashboard).

## Request logs

Request logs keep **one line per HTTP request that the proxy routed to a service**, on the server that served it. They answer "which requests failed, and what did they ask for", which [traffic analytics](traffic-analytics.md) (counts per minute) cannot.

### What is stored per request

The text of a line is one JSON object, with the stream `requests`. The replica and deployment travel beside it, as for runtime lines.

| Field | |
|---|---|
| `method`, `host`, `path` | the request line and `Host` header; `path` has no query string |
| `kind` | what was requested: `page`, `api`, `asset` or `other` ([how it is decided](#what-kind-a-request-is)) |
| `query` | the query string, **only when the server keeps query strings** (below) |
| `status` | the response status |
| `level` | from the status, never from the request: `error` for 5xx, `warn` for 4xx, `info` otherwise. `level:error` and the Logs page counts read it |
| `duration_ms` | time to serve the request |
| `bytes`, `bytes_in` | response and request body size (`bytes_in` only when it is not zero) |
| `ua` | the `User-Agent` header |
| `ip` | the network of the peer the proxy trusts as the client, the **/24 of an IPv4 or the /48 of an IPv6 address** (`203.0.113.0/24`); never the full address. It is not always the visitor's network, see [below](#which-network-ip-is) |
| `process`, `component` | the [process](processes.md) or Compose component that served it, when there is one |

The time of a request line is **when the response finished**: the request's start plus `duration_ms`. The list shows that time, and the Logs page histogram counts the request in the bucket where it ended, so a slow request lands where it completed, not where it began. [Traffic analytics](traffic-analytics.md) counts a request in the minute it **started**, so a long request can sit in different minutes in the two views.

#### Which network `ip` is

`ip` is the network of the address the proxy trusts as the client, not always the visitor's:

- Direct connections: the TCP peer, which is the visitor.
- Behind a CDN or proxy without the origin lock (for example a Cloudflare-proxied domain whose lock is off): the proxy trusts a forwarded visitor address only from Cloudflare's ranges and only for a locked domain, so `ip` is the network of the CDN edge. Many visitors share a few values.
- Through a Cloudflare Tunnel: every request comes from the tunnel container, so `ip` is the network of that container on the edge network.

To get the visitor's network for a Cloudflare-proxied domain, keep the [origin lock](cloudflare.md#origin-lock) on (**Origin locked to Cloudflare**, Service → Domains → the domain). A tunnelled domain cannot tell visitors apart.

### What kind a request is

Every stored line has a `kind`, searchable as `@kind:page`, `@kind:api`, `@kind:asset` or `@kind:other`. The agent decides it from the request, in this order:

1. **The extension of the path's last segment**, when it names an asset (`js`, `mjs`, `cjs`, `css`, `map`, `wasm`, images such as `png`, `jpg`, `gif`, `webp`, `avif`, `svg`, `ico`, fonts such as `woff2`, `ttf`, media such as `mp4`, `mp3`, and `webmanifest`) it is an `asset`; `.html` and `.htm` make it a `page`. Matrix parameters (everything from a `;`, as in `/admin;x.js`) do not count as part of the extension. The extension comes first because the `Content-Type` of a failed request describes the error body: a 404 on `/assets/app.js` usually answers `text/html`, and is still a request for an asset.
2. **The response `Content-Type`** (parameters such as `charset` ignored): `text/html` and `application/xhtml+xml` are a `page`; JSON (`application/json`, `+json` types such as `application/problem+json`), XML (`+xml` types, except `image/svg+xml`), form posts, `multipart/*`, gRPC, `text/event-stream` and `application/x-ndjson` are `api`; `text/css`, JavaScript, `application/wasm`, `image/*` (including `image/svg+xml`), `font/*`, `audio/*`, `video/*` and `application/manifest+json` are an `asset`. This is how an extensionless request such as `/_next/image` answering `image/webp` is found to be an asset.
3. **Anything else** is `other`: plain text, a redirect or an empty answer for a path without an asset extension, a `.txt` file.

**Successful asset loads are not stored.** A `GET` or `HEAD` request of kind `asset` whose status is 2xx or 304 and whose response `Content-Type` is an asset type or absent (a 304 has none) is left out: it is noise, and the counts of traffic analytics already include it. Every other request is stored: any other method (`POST /users/1.js` is a POST whatever the path ends in), an answer that is not an asset (`GET /app.js` answering `application/json`), and an asset request that failed (any other status: 3xx other than 304, 4xx, 5xx), so `@kind:asset @status:404` finds the missing files. A server that wants every asset request turns on **Keep successful asset requests** (`requestLogsKeepAssets`, below). Pages, API requests and `other` are stored whatever their status.

### Where, how long, how much

| | |
|---|---|
| Where | `<state-dir>/requests/<serviceId>/` on the server, in the format of the runtime store (`<state-dir>/logs`) but a separate store |
| How long | the server's log retention (7 days by default, 1–90) |
| How much | at most **200 MiB per service** and **2 GiB per server**, compressed on disk; the oldest lines go first. Separate from the runtime caps: neither store uses the other's budget |

### Privacy

- **The request store** keeps the client address only as its /24 or /48 network. The query string is dropped before the line is written unless **Keep query strings** is on for the server.
- **The proxy's own log is separate.** The Traefik container's log (Docker's `json-file` log, 3 files of 10 MB, rotating) holds the raw access lines: the full client address, query strings and the `Referer`. It did before request logs existed, it is not the request store, and **Keep query strings** does not change it. It never leaves the host.
- A request that matches no service (an unknown hostname, a scanner) is **never stored**. Requests to external route services are not stored either.
- Request lines are **never shipped**: [drains](#log-drains) and [central logs](#central-logs) read only the runtime store, and the control plane stores none. A query is evaluated on the server and only the matching page or the counts come back.
- Secret values of the service are masked in lines when they are read, as for runtime logs. A kept query string can still hold tokens a client put in a URL; leave it off unless you need it.

### Settings (per server)

Server page → Settings → Logs and disk, or `PUT /api/v1/servers/{serverId}/log-settings` (scope `servers:write`), together with the runtime settings.

| Field | Default | Effect |
|---|---|---|
| `requestLogs` | on | Store request lines. Needs `trafficAnalyticsEnabled` on the server: the lines come from the proxy's access log. |
| `requestLogsKeepQuery` | off | Keep the query string in `query`. |
| `requestLogsKeepAssets` | off | Also store the successful (2xx, 304) `GET` and `HEAD` requests of kind `asset`: scripts, stylesheets, images, fonts. Off, only the failed ones are stored. |

The view also reports `requestsTotalBytes`, the disk the request store uses. Changing any of these settings reaches the agent like any log setting and **does not restart the proxy**; a change applies to requests read from then on. Turning `requestLogs` off stops storing new lines; what is stored stays until it expires or the caps remove it. Turning off traffic analytics stops request logs too, and recreates the proxy ([monitoring](monitoring.md#traffic-analytics)).

## The Logs page

**Project → environment → Logs** (`/p/<project>/<environment>/logs`) is the one place to read the logs of an environment. Observability keeps Uptime, Traffic and Services and links here; the inspector's service **Logs** tab stays for one service.

- **Query bar.** The query language above, with autocomplete over keys and values and a count next to each suggestion. `stream:runtime` and `stream:requests` pick a source in the query itself.
- **Source.** Runtime, requests or both (the `sources` parameter).
- **Facets.** A sidebar with the counts of `level`, `stream`, `status`, `service`, `replica`, `deployment` and the attributes found in lines, **for the current query and range**. Choosing a value adds it to the query. `status` is grouped by class (2xx, 3xx, 4xx, 5xx); expand a class to see the exact codes. With the requests source on, a **Requests** group adds **Kind**, **Method**, **Paths** and **Duration** ranges.
- **Volume.** A histogram of matching lines over the range, with the lines that are errors marked. A request is counted where its response finished.
- **Live and History.** Live shows runtime lines from the log stream and **polls the request store every 2 seconds** for new request lines. The list stays on the newest line; scroll up to stop it, and **Jump to latest** takes it back. There is no Follow switch. History reads the range you pick, with paging.
- **Row detail.** A line opens with its fields: for a request, the method, host, path, status, duration, sizes, client network and user agent. **Show logs around this request** shows the logs around that request's time.

Query keys for requests (`@` reads an attribute of the line):

| Key | Matches |
|---|---|
| `stream:requests` | request lines only (`stream:runtime`: runtime lines only) |
| `@method:POST` | the method |
| `@kind:api` | the kind of request: `page`, `api`, `asset` or `other` |
| `@path:/api/*` | the path, wildcards allowed |
| `@host:app.example.com` | the `Host` header |
| `@status:[500 TO 599]` or `status:5*` | the response status (the `status` key works without `@`) |
| `@duration_ms:>500` | requests slower than 500 ms |
| `@bytes:>1000000` | responses larger than 1 MB |
| `@ip:203.0.113.0/24` | the client network |
| `@process:worker`, `@component:web` | the process or Compose component |

```text
stream:requests @status:[500 TO 599] @path:/api/*
stream:requests @duration_ms:>500 -@method:GET
```

## Log drains

A drain ships lines **from each agent, from its own host**, to a destination. Drains are configured **per team** (Team settings → Log forwarding, scope `admin`), optionally narrowed to projects, services and a query.

| Kind | Sends | Credentials |
|---|---|---|
| **Loki** | the push API (`…/loki/api/v1/push`), JSON, grouped into streams by `service`, `project`, `environment`, `server`, `level` plus your static labels; `X-Scope-OrgID` for a tenant | basic auth or bearer token, extra headers |
| **OpenTelemetry (OTLP/HTTP)** | logs in OTLP JSON (`…/v1/logs`), resource attributes `service.name`, `service.namespace`, `deployment.environment.name`, `host.name` | bearer token and/or headers |
| **Syslog** | RFC 5424 over UDP, TCP or TCP+TLS (octet-counting framing), the service labels in structured data | none |
| **HTTP JSON** | batches of records (`timestamp`, `message`, `level`, `service`, `project`, `environment`, `server`, …) as a JSON array or NDJSON, to any intake. Field names and static fields are configurable, which covers **Datadog** (`https://http-intake.logs.datadoghq.com/api/v2/logs`, header `DD-API-KEY`, field `ddsource`), **Better Stack** (bearer token) and **Axiom** (bearer token, dataset in the URL) | headers, bearer token, or basic auth |

- **Credentials are send-time secrets**: sealed in the database, never returned by the API (a mask and the header *names* only), delivered to the agents with the configuration and never persisted in a command. They are masked from every error message.
- **Secrets in lines**: before a line leaves the host, the agent masks the secret values of that service (variables, git token, deploy key, registry credentials; 6+ characters, also their URL-escaped and base64 forms) with `••••••`. The values come with the configuration and are refreshed at every deploy; a secret you changed and have not deployed yet is not known to the agent. A drain's query is matched against the masked line.
- **Start**: a new drain ships what is written after it was saved, not the stored history.
- **Delivery**: batches of up to 500 lines or 1 MB, at least once (a crash can repeat the last two seconds). A failing destination is retried with exponential backoff (1 s to 5 min, honouring `Retry-After`). The log store is the buffer: the drain keeps a position per service and reads from the disk, so a destination that is down for hours loses nothing as long as the lines are still stored and the backlog fits the **buffer bound** (default 256 MB per server and drain; the oldest lines are skipped and counted as *dropped* past it). A destination that **rejects** a batch for good (HTTP 400, 413, 422, other non-retryable 4xx) has that batch dropped and counted, so one bad record cannot block the drain; 401, 403, 408, 425, 429 and 5xx are retried.
- **Health**: Team settings → Log forwarding shows, per server, `Delivering`, `Retrying` (the last delivery failed), `Failing` (five failures in a row) or `Disabled`, the last error (masked), lines sent and dropped, and the backlog on disk. Agents report every 30 seconds.
- **Test**: *Send a test line* delivers one line from a connected server (or the one you pick), which is where reachability and credentials count.
- A drain ships from every server of the team that hosts matching services. A server that is offline ships nothing until it reconnects; its backlog waits on its disk.

### Quick reference

```sh
# Loki with basic auth, only errors of one project
curl -X POST $URL/api/v1/log-drains -H "Authorization: Bearer $TOKEN" -d '{
  "name":"grafana","kind":"loki",
  "config":{"loki":{"url":"https://logs-prod.grafana.net/loki/api/v1/push","tenantId":"12345"}},
  "secrets":{"username":"12345","password":"glc_…"},
  "allProjects":false,"projectIds":["<project-id>"],"query":"level:error"}'

# Datadog through the generic HTTP drain
curl -X POST $URL/api/v1/log-drains -H "Authorization: Bearer $TOKEN" -d '{
  "name":"datadog","kind":"http",
  "config":{"http":{"url":"https://http-intake.logs.datadoghq.com/api/v2/logs","extraFields":{"ddsource":"nebulactrl"}}},
  "secrets":{"headers":{"DD-API-KEY":"…"}}}'

curl -X POST $URL/api/v1/log-drains/<id>/test -H "Authorization: Bearer $TOKEN" -d '{}'
```

Changes are audited (`log_drain.create|update|delete|test`, `server.log_settings.update`); credentials appear only as "changed".

## Central logs

Central logs are a built-in drain that ships a team's service logs to the **log store that runs beside the control plane** ([built-in services](platform-services.md)): one Loki for the installation, a tenant per team, **7 days** of retention, no quota. NebulaCtrl runs it for you, so there is nothing to set up and no address, user or key to enter; teams never see them.

- **Off by default.** Nothing is collected until the team asks. **Team settings → Log forwarding** has a built-in **Central logs** switch (a log drain with `builtin: true`, created disabled). Turn it on and the team's servers start sending the logs of its services to the team's own tenant, from that moment: like any drain it does not copy the stored history. Turn it off to stop; lines already stored expire after 7 days. The drain cannot be renamed, reconfigured or deleted (409 `builtin_drain`); you can only turn it on or off and narrow its scope like any other drain.
- **Private.** The shipper on each server talks to `127.0.0.1:18100`, carried over the agent's existing connection to the control plane, which writes into the team's tenant, chosen from the agent's identity. The log store has no public hostname and no open port. Secret values are masked on the host before a line leaves it, like for every drain, and again when the lines are read.
- **Reading it.** A service's **Logs** tab has **Central logs (7 days)** next to Live and History (`?logs=central` in the address). It takes a search text (lines that contain it) and a time range up to 7 days. While forwarding is off the tab shows one line saying so and how to turn it on, and no lines.
- **No Grafana, no query language to learn.** The log store is not exposed: NebulaCtrl itself queries it, for the team's own tenant and one service at a time, through the Logs tab and the API below, so there is nothing else to run or sign in to. If you want your logs in Grafana, Loki or another tool, add a normal drain to it (above).
- **It answers while the server is offline.** History is stored on the server and is unavailable while the server is offline; central logs live on the control-plane host, so a server that is down, rebuilt or removed still has its last 7 days of lines there, up to the moment it stopped sending.

API: `GET /api/v1/services/{serviceId}/logs/central` (operationId `getServiceCentralLogs`, scope `logs:read`):

| Parameter | |
|---|---|
| `q` | keep the lines that contain this text (a plain substring, up to 500 characters; not the History query language) |
| `since` | a duration back from `to` (`15m`, `2h`, `3d`) or an RFC 3339 time; not together with `from` |
| `from`, `to` | RFC 3339. `to` defaults to now, `from` to an hour before `to`. A window longer than 7 days is cut to its newest 7 days |
| `limit` | 1–1000, default 200 |

The answer is `{enabled, hint, lines}`: `lines` hold `ts`, `server` (the name), `stream` and `line`, newest first. `enabled` is `false` while the team's Central logs switch is off, with `hint` saying how to turn it on and no lines. `502 central_logs_unavailable` means the log store did not answer; try again. AI clients (MCP) can read it ([MCP](../reference/mcp.md)).

## Limits to know

- Only stdout/stderr of containers Docker still has: a container that logs to a file inside itself is not captured. Stdout and stderr share one stream (no separate `stderr` facet).
- Central logs are one log store on the control-plane host, shared by every team: 7 days, no quota, and gone with the host. Use a drain to your own store for longer retention.
- Lines are ordered by arrival at the agent; lines of two replicas can be a few milliseconds out of time order. A page is sorted by time, paging is by arrival.
- History and drains need an agent of this version: an older one answers history requests with `409 agent_outdated`, and the Requests source reports its server as outdated (reason `agent_outdated`) until the agent is updated. Update agents with the install script.
- There is no log search across servers in one scan beyond the environment view (one request per service, merged by time) and no full-text index: a query scans the compressed blocks of its time range (blocks outside the range are skipped by the time index; a scan is bounded to about 256 MB of decompressed log or 4 seconds, then returns a cursor to continue).
- Request lines are a separate store, kept only on the host, with their own caps and no drain or central log: see [request logs](#request-logs). A server that is offline has neither request history nor facets.
