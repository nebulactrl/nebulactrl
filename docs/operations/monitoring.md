---
title: Monitoring and retention
description: "Health endpoints, JSON logs, metrics and disk alerts."
section: Observability
order: 10
---

# Monitoring, logs and retention

## Health endpoints

| Endpoint | Meaning |
|---|---|
| `GET /healthz` | process is up (always `{"status":"ok"}`) |
| `GET /readyz` | process is up **and the database answers** (`200 {"status":"ready"}`, otherwise 503) |
| `nebulactrl-server healthcheck` | the same readiness probe run inside the container (used by the image's `HEALTHCHECK` and Compose, so the check needs no curl). Reads `NEBULACTRL_LISTEN`; `--url` overrides. |

Use `/readyz` for load balancers and uptime monitors.

## Logs

The control plane writes **JSON, one object per line, to stdout**: `docker compose logs -f nebulactrl`. Level via `NEBULACTRL_LOG_LEVEL`. Every HTTP request is logged with method, path, status, duration and a request id. Lines produced while handling a deployment carry **`correlation_id`** = the deployment ID, so one deployment is traceable end to end:

```sh
docker compose logs nebulactrl | jq -c 'select(.correlation_id=="<deployment-uuid>")'
```

Secrets are never logged. Values of secret variables (6+ characters) are replaced with `••••••` before build/deploy/runtime log lines are stored or streamed.

Agent logs: `journalctl -u nebulactrl-agent -f` on the host (also JSON).

### In the UI

| What | Where | Retention |
|---|---|---|
| Build and deploy output | Deployment row → logs | stored per deployment, **capped at 10,000 lines** (a final marker line says so) |
| Runtime logs | Service → Logs, Environment → Observability | **stored on each server by its agent**, 7 days and 500 MB per service by default (configurable per server); **Live** streams, **History** searches the stored lines. Not available while the server is offline. [Log history and drains](logs.md) |
| Audit log | Team settings → Audit log (`GET /api/v1/audit-events`) | kept indefinitely (not pruned) |

## Resource metrics

Collected from agent heartbeats (host: CPU, memory, disk) and container stats (per service: CPU, memory), sampled about every 10 seconds. Container memory is reported as usage minus inactive file cache.

| Setting (per server) | Default | Range | Effect |
|---|---|---|---|
| `metricsRetentionDays` | 7 | 1–90 | Samples older than this are pruned by an hourly job. A `30d` chart range only shows the history that is retained ("History kept for N days"). |
| `diskAlertThresholdPercent` | 80 | 50–99 | Root-filesystem usage above this shows a warning on the server card and server page. |

Chart ranges: `5m`, `10m`, `30m`, `1h`, `6h`, `12h`, `24h`, `7d`, `30d`; the response is bucketed to at most about 300 points, and the short ranges refresh every 5 s, the others every 15 s. Older data is downsampled into coarser buckets (the API reports `stepSeconds`).

Change the settings on the server page (Settings → Monitoring), or with `PATCH /api/v1/servers/{serverId}` (scope `servers:write`).

## Traffic analytics

Request analytics (requests, bandwidth, status classes, p95 latency, unique visitors, top paths, referrers, browsers, devices) are collected **from the agent-managed Traefik's access log, on each host, as aggregates only**. Raw access logs are never shipped or stored by the control plane. Unique visitors are hashed with a salt that rotates daily; raw IP addresses are never sent.

| Setting (per server) | Default | Effect |
|---|---|---|
| `trafficAnalyticsEnabled` | on | Turn collection off per server. This recreates the proxy container (a brief proxy restart), because the access log is a static Traefik setting. |
| `trafficHourlyRetentionDays` | 7 | Hourly buckets (fine-grained charts) kept this long. |
| `trafficDailyRetentionDays` | 90 | Daily roll-ups kept this long. |

Views: service inspector → Analytics, server page → Traffic, environment → Observability. Each has a "Where requests come from" map (country, optionally city); see [Traffic analytics and request origins](traffic-analytics.md) for the GeoIP databases, licences and settings. Runtime logs can be shipped to Loki, OTLP, syslog or an HTTP intake: [log drains](logs.md#log-drains).

## Liveness semantics

- A server is **offline** after 30 seconds without a heartbeat (the agent sends one every 10 s). The UI marks observations older than 30 s as *stale* instead of showing them as current.
- Each state change in the UI also appears as text or a glyph, not only as animation.
- Live updates use Server-Sent Events. If the connection drops the UI shows "Live updates paused — retrying" and falls back to polling every 15 s.

## Capacity planning

- Metrics volume is roughly (servers + running containers) samples per 10 s per retention window; the default 7 days is small. Raising retention to 90 days multiplies it by ~13.
- The agent keeps the 5 newest built images per service (`imageRetention`) and prunes the builder cache older than 7 days daily. Watch the disk gauge on the server card.
