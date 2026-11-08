---
title: 0009 Request logs and the log explorer
description: "Why proxy request lines are stored on the host in a store of their own, and why one Logs page with server-side facets reads them."
section: Internals
order: 9
---

# 0009. Host-local request logs and the Logs page

- Status: accepted
- Date: 2026-11-08
- Amends: [0003](0003-traefik-proxy.md) (the access log is still read by the agent; one line per routed request is now also kept on the host)

## Context

[Traffic analytics](../operations/traffic-analytics.md) turns the Traefik access log into per-minute aggregates and drops the lines. That answers "how many 5xx did `web` serve at 14:05", and cannot answer "which requests were they": there is no path, no status per request, no slow request to open. Runtime logs ([log history](../operations/logs.md)) already have a per-host store, a query language evaluated on the agent, and a Live | History view, but they hold only what containers write.

Two constraints decide the design:

- NebulaCtrl keeps no raw logs on the control plane. Aggregates and counts travel; lines stay on the host that produced them.
- Request lines are a different kind of data from runtime lines. They are far more numerous, they carry client addresses and URLs, and nobody expects them in a log drain or in the 7-day central logs.

A log explorer in the style of Datadog (a query bar, a facet sidebar with counts, a volume histogram) needs counts over the whole queried range, not over the page of lines the browser holds. Counting in the browser would move the log over the wire, which the history endpoint was built to avoid.

## Decision

**Request lines are stored only on the host, in a store of their own, and the Logs page reads them through the same agent-side query path as runtime logs.**

1. **A second log store per host**, `<state-dir>/requests`, with the on-disk format of the runtime store (`<state-dir>/logs`) and its own caps: **200 MiB per service** and **2 GiB per server**, compressed, oldest segments first. Retention in days is the server's log retention. The request store never counts against the runtime caps and the reverse.
2. **One line per routed request.** The text of a line is a `RequestRecord` as JSON: `method`, `host`, `path`, `kind`, `query` (only when kept), `status`, `duration_ms`, `bytes`, `bytes_in`, `ua`, `ip`, `process`, `component`. The stream of the line is `requests`. The line's time is when the response finished (the request's start plus its duration), because the proxy writes a line at the end of the request; the histogram buckets by it. Traffic analytics keeps counting a request in the minute it started. A request that matches no service route is **never stored**, and neither is a request to an external route service. `kind` is `page`, `api`, `asset` or `other`, decided on the agent from the path's extension when it is an asset or HTML one (a failed request's `Content-Type` describes the error body) and otherwise from the response `Content-Type`.
3. **Masking at the source.** The client address is stored only as its network, the /24 of an IPv4 or the /48 of an IPv6 address (`203.0.113.0/24`). The **query string is dropped** unless the server's *Keep query strings* setting is on. Both happen on the agent before a line is written.
4. **Never leaves the host as lines.** No log drain and no central log reads the request store (drains read only the runtime store), and the control plane stores none of it. A viewer's query is sent to the agent and evaluated there; only the matching page, or counts, come back.
5. **On by default, tied to traffic analytics.** `requestLogs` defaults to on and needs `trafficAnalyticsEnabled`; `requestLogsKeepQuery` and `requestLogsKeepAssets` default to off. All are server log settings, delivered in the agent's log configuration. Changing them does not restart the proxy: the access log was already being read, so turning request logs on or off only decides whether the agent writes a line.
6. **No new query grammar.** The existing language covers it: request fields are attributes (`@method:POST`, `@path:/api/*`, `@host:`, `@duration_ms:>500`, `@bytes:`, `@ip:`, `@process:`, `@component:`), `status:5*` and `@status:[500 TO 599]` use the `status` key, and `stream:requests` or `stream:runtime` selects a source. The Go matcher on the agent and the web matcher stay checked by the shared fixture.
7. **Server-side facets.** A new agent command, `logs_facets`, scans the range newest first within a budget (64 MiB decompressed or 2 seconds), matches with the same query language and returns counts for the built-in facets (`level`, `stream`, `status`, `replica`, `deployment`), for `@attribute` paths (up to 50 paths, 20 values each, min and max for numeric ones) and a volume histogram (60 buckets by default, at most 240). A scan that stops early says so (`partial`, `partialFrom`).
8. **Two sources, one API.** `GET …/logs/history` gains `sources` (`runtime`, `requests`; default `runtime`), and `GET /services/{serviceId}/logs/facets` and `GET /environments/{environmentId}/logs/facets` are new. The environment endpoints fan out over (service, source) pairs and merge by time (history) or by summed counts (facets); every agent gets the same `from`, `to` and bucket count, so histograms add by bucket index. Attribute values pass the same secret masking as history lines.
9. **One Logs page for the environment**, `/p/<project>/<environment>/logs`. It replaces the log section that lived in Observability, which keeps Uptime, Traffic and Services and links to it. The service inspector's Logs tab stays. The page has a query bar with autocomplete over keys and values (with counts), a facet sidebar, a volume histogram, **Live** and **History**, a source selection (runtime, requests) and a row detail.
10. **Live requests poll.** Live mode streams runtime lines over the existing stream and polls the request store every 2 seconds for lines newer than the newest shown. It opens no further stream.

## Consequences

- A request that failed can be found, read and filtered by path, status, duration and client network, with no second system. The cost is disk on every proxy host: up to 2 GiB per server for request lines, and a write per routed request on busy hosts.
- Privacy is a property of what the request store holds, not of who can read it: it keeps only the /24 or /48 network of the client and no query string by default. It is not a claim about the host. The proxy's own container log (Docker's `json-file` log of the Traefik container, 3 files of 10 MB, rotating) holds the raw access lines with the full client address, query strings and `Referer`, as it did before this change; it never leaves the host and the request settings do not affect it. A team that needs query strings (search terms, tracking ids) opts in per server and takes on whatever those strings contain, including tokens a client put in a URL. Secret masking applies when lines are read, as for runtime logs, but it knows only the service's own secrets.
- `ip` is the network of the peer the proxy trusts as the client. Behind a CDN without the origin lock it is the network of the CDN edge; through a Cloudflare Tunnel it is the network of the tunnel container. For a Cloudflare-proxied domain the origin lock makes it the visitor's network. The proxy configuration is not changed for this.
- A server that is offline has no request history and no facets: the service endpoints answer `503 server_offline` and the environment endpoints list the service in `unavailable`, exactly as history does today. There is no copy to read instead.
- Drains and central logs are unchanged and cannot carry request lines. A team that wants them in Loki or Datadog has to do it at the proxy or the application.
- Facet counts are bounded by the scan budget: over a wide range of a busy service they describe the newest part, and the response says so. Counts are exact for what was scanned.
- An agent older than this change cannot answer `logs_facets` or a `requests` source; the API reports it as `agent_outdated`, as for history. For the `requests` source, the control plane lists the service in `unavailable` with reason `agent_outdated` without asking the agent, until the agent is updated. Aggregates for traffic analytics are unaffected.
- Turning request logs off stops writing; stored lines stay until retention or the caps remove them. Lowering a cap deletes the oldest segments within a minute.
- A request that matches no service route (scanners, wrong hostnames) is not in the request store at all.
- Successful asset requests (kind `asset`, status 2xx or 304) are not stored unless the server turns on `requestLogsKeepAssets`; failed ones always are, and traffic analytics counts all of them.
- Match counts can confirm a guess about a masked value: a query for a candidate secret returns a count even though the value itself is masked in the lines. History could always be used this way; facets add nothing a history query did not already allow.
- One flooded service can push other services' request lines out of the shared 4096-line writer queue under extreme load. Dropped lines are counted and logged; the queue is shared, not per service.

## Alternatives considered

- **Store request lines in the runtime store.** One store, no new code path. Rejected: request volume would evict the runtime lines that the caps protect, and the drains and central logs read the runtime store, so every request line would leave the host to Loki and the control-plane host with its client address and URL. Separate stores keep the caps, the retention and the "drains never see this" rule independent and easy to state.
- **Ship request lines to the control plane.** Gives a cross-host view and works while a server is offline. Rejected: it contradicts the rule that the control plane stores no raw logs, makes the control-plane database or log store the largest and most sensitive dataset of the installation, and adds a quota and retention problem that the hosts already solve.
- **A new query grammar for requests.** Typed fields (`method=POST`) would be a little shorter. Rejected: the existing language already addresses attributes (`@method:POST`), numeric ranges (`@duration_ms:>500`) and the `status` key, and two grammars on one page would need two matchers kept in step.
- **An SSE live tail for requests.** Real streaming, no polling delay. Rejected: a tab holds at most 2 streams (the shared event feed and one log tail, because browsers allow six connections per host), so a request stream would displace the runtime tail or the feed; a 2-second poll for lines newer than the newest shown looks the same to a person and needs no stream.
- **Facets computed in the browser from the loaded page.** No new endpoint. Rejected: counts would describe the 500 lines on screen, not the range, and loading the range to count it moves the log over the wire.
