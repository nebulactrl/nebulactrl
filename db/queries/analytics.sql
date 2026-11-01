-- ===== Resource metric tiers ================================================

-- name: LatestMetricBucket1m :one
-- 'epoch' when the tier is empty.
SELECT COALESCE(max(bucket), 'epoch'::timestamptz)::timestamptz AS latest FROM metric_samples_1m;

-- name: LatestMetricBucket15m :one
SELECT COALESCE(max(bucket), 'epoch'::timestamptz)::timestamptz AS latest FROM metric_samples_15m;

-- name: OldestRawMetricSample :one
SELECT COALESCE(min(ts), 'epoch'::timestamptz)::timestamptz AS oldest FROM metric_samples;

-- name: DownsampleMetrics1m :execrows
-- Recomputes every complete minute from `since`; idempotent. Replica samples of
-- one service share their sample instant: they are summed per instant first
-- (cpu/mem/limit of the whole service), then averaged over the minute.
-- `replicas` is the most replica containers seen at one instant.
-- Two kinds of rows come out, told apart by `process`: the service (or host)
-- total, process '' over ALL samples, and, for samples that carry a process,
-- that process' own sums.
INSERT INTO metric_samples_1m (server_id, service_id, process, bucket, cpu_avg, cpu_max, mem_avg, mem_max, mem_limit, disk_used, disk_total, samples, replicas)
SELECT server_id, service_id, process, date_trunc('minute', ts),
       avg(cpu)::float8, max(cpu)::float8,
       avg(mem)::bigint, max(mem)::bigint, max(lim)::bigint,
       max(du)::bigint, max(dt)::bigint, count(*)::int, max(n)::int
FROM (
    SELECT server_id, service_id, ''::text AS process, ts,
           sum(cpu_percent)::float8 AS cpu, sum(mem_bytes)::bigint AS mem, sum(mem_limit_bytes)::bigint AS lim,
           max(disk_used_bytes) AS du, max(disk_total_bytes) AS dt, count(*) AS n
    FROM metric_samples
    WHERE ts >= sqlc.arg(since)::timestamptz AND ts < date_trunc('minute', now())
    GROUP BY server_id, service_id, ts
    UNION ALL
    SELECT server_id, service_id, process, ts,
           sum(cpu_percent)::float8 AS cpu, sum(mem_bytes)::bigint AS mem, sum(mem_limit_bytes)::bigint AS lim,
           max(disk_used_bytes) AS du, max(disk_total_bytes) AS dt, count(*) AS n
    FROM metric_samples
    WHERE ts >= sqlc.arg(since)::timestamptz AND ts < date_trunc('minute', now()) AND process <> ''
    GROUP BY server_id, service_id, process, ts
) t
GROUP BY server_id, service_id, process, date_trunc('minute', ts)
ON CONFLICT (server_id, (COALESCE(service_id, '00000000-0000-0000-0000-000000000000'::uuid)), process, bucket) DO UPDATE
SET cpu_avg = EXCLUDED.cpu_avg, cpu_max = EXCLUDED.cpu_max, mem_avg = EXCLUDED.mem_avg, mem_max = EXCLUDED.mem_max,
    mem_limit = EXCLUDED.mem_limit, disk_used = EXCLUDED.disk_used, disk_total = EXCLUDED.disk_total,
    samples = EXCLUDED.samples, replicas = EXCLUDED.replicas;

-- name: DownsampleMetrics15m :execrows
-- Recomputes every 15-minute bucket touched since `since` from the 1m tier
-- (sample-weighted average), per process ('' = the total); idempotent.
INSERT INTO metric_samples_15m (server_id, service_id, process, bucket, cpu_avg, cpu_max, mem_avg, mem_max, mem_limit, disk_used, disk_total, samples, replicas)
SELECT server_id, service_id, process, date_bin(interval '15 minutes', bucket, TIMESTAMPTZ '2001-01-01') AS b,
       (sum(cpu_avg * samples) / GREATEST(sum(samples), 1))::float8, max(cpu_max)::float8,
       (sum(mem_avg::float8 * samples) / GREATEST(sum(samples), 1))::bigint, max(mem_max)::bigint, max(mem_limit)::bigint,
       max(disk_used)::bigint, max(disk_total)::bigint, sum(samples)::int, max(replicas)::int
FROM metric_samples_1m
WHERE bucket >= date_bin(interval '15 minutes', sqlc.arg(since)::timestamptz, TIMESTAMPTZ '2001-01-01')
GROUP BY server_id, service_id, process, b
ON CONFLICT (server_id, (COALESCE(service_id, '00000000-0000-0000-0000-000000000000'::uuid)), process, bucket) DO UPDATE
SET cpu_avg = EXCLUDED.cpu_avg, cpu_max = EXCLUDED.cpu_max, mem_avg = EXCLUDED.mem_avg, mem_max = EXCLUDED.mem_max,
    mem_limit = EXCLUDED.mem_limit, disk_used = EXCLUDED.disk_used, disk_total = EXCLUDED.disk_total,
    samples = EXCLUDED.samples, replicas = EXCLUDED.replicas;

-- name: PruneMetricSamples1m :execrows
DELETE FROM metric_samples_1m m
USING servers s
WHERE m.server_id = s.id AND m.bucket < now() - make_interval(days => s.metrics_retention_days);

-- name: PruneMetricSamples15m :execrows
DELETE FROM metric_samples_15m m
USING servers s
WHERE m.server_id = s.id AND m.bucket < now() - make_interval(days => s.metrics_retention_days);

-- name: ServerMetricsRaw :many
SELECT date_bin(make_interval(secs => sqlc.arg(bucket_secs)::float8), ts, TIMESTAMPTZ '2001-01-01')::timestamptz AS bucket,
       avg(cpu_percent)::float8      AS cpu_avg,
       max(cpu_percent)::float8      AS cpu_max,
       avg(mem_bytes)::bigint        AS mem_avg,
       max(mem_bytes)::bigint        AS mem_max,
       max(mem_limit_bytes)::bigint  AS mem_limit,
       avg(disk_used_bytes)::bigint  AS disk_used,
       max(disk_total_bytes)::bigint AS disk_total
FROM metric_samples
WHERE server_id = sqlc.arg(server_id) AND service_id IS NULL AND ts >= sqlc.arg(since)::timestamptz
GROUP BY 1
ORDER BY 1;

-- name: ServerMetrics1m :many
SELECT date_bin(make_interval(secs => sqlc.arg(bucket_secs)::float8), bucket, TIMESTAMPTZ '2001-01-01')::timestamptz AS bucket,
       (sum(cpu_avg * samples) / GREATEST(sum(samples), 1))::float8 AS cpu_avg,
       max(cpu_max)::float8     AS cpu_max,
       (sum(mem_avg::float8 * samples) / GREATEST(sum(samples), 1))::bigint AS mem_avg,
       max(mem_max)::bigint     AS mem_max,
       max(mem_limit)::bigint   AS mem_limit,
       max(disk_used)::bigint   AS disk_used,
       max(disk_total)::bigint  AS disk_total
FROM metric_samples_1m
WHERE server_id = sqlc.arg(server_id) AND service_id IS NULL AND bucket >= sqlc.arg(since)::timestamptz
GROUP BY 1
ORDER BY 1;

-- name: ServerMetrics15m :many
SELECT date_bin(make_interval(secs => sqlc.arg(bucket_secs)::float8), bucket, TIMESTAMPTZ '2001-01-01')::timestamptz AS bucket,
       (sum(cpu_avg * samples) / GREATEST(sum(samples), 1))::float8 AS cpu_avg,
       max(cpu_max)::float8     AS cpu_max,
       (sum(mem_avg::float8 * samples) / GREATEST(sum(samples), 1))::bigint AS mem_avg,
       max(mem_max)::bigint     AS mem_max,
       max(mem_limit)::bigint   AS mem_limit,
       max(disk_used)::bigint   AS disk_used,
       max(disk_total)::bigint  AS disk_total
FROM metric_samples_15m
WHERE server_id = sqlc.arg(server_id) AND service_id IS NULL AND bucket >= sqlc.arg(since)::timestamptz
GROUP BY 1
ORDER BY 1;

-- name: ServiceMetricsRaw :many
-- Replica samples of one instant are summed (whole-service usage); `replicas`
-- is the number of replica containers sampled. `process` '' is the service total
-- (no filter); a name reads that process' samples only.
SELECT date_bin(make_interval(secs => sqlc.arg(bucket_secs)::float8), ts, TIMESTAMPTZ '2001-01-01')::timestamptz AS bucket,
       avg(cpu)::float8      AS cpu_avg,
       max(cpu)::float8      AS cpu_max,
       avg(mem)::bigint      AS mem_avg,
       max(mem)::bigint      AS mem_max,
       max(lim)::bigint      AS mem_limit,
       max(n)::int           AS replicas
FROM (
    SELECT ts, sum(cpu_percent)::float8 AS cpu, sum(mem_bytes)::bigint AS mem, sum(mem_limit_bytes)::bigint AS lim, count(*) AS n
    FROM metric_samples
    WHERE service_id = sqlc.arg(service_id) AND ts >= sqlc.arg(since)::timestamptz
      AND (sqlc.arg(process)::text = '' OR process = sqlc.arg(process)::text)
    GROUP BY ts
) t
GROUP BY 1
ORDER BY 1;

-- name: ServiceMetrics1m :many
-- `process` '' reads the service total rows; a name reads that process' rows.
SELECT date_bin(make_interval(secs => sqlc.arg(bucket_secs)::float8), bucket, TIMESTAMPTZ '2001-01-01')::timestamptz AS bucket,
       (sum(cpu_avg * samples) / GREATEST(sum(samples), 1))::float8 AS cpu_avg,
       max(cpu_max)::float8     AS cpu_max,
       (sum(mem_avg::float8 * samples) / GREATEST(sum(samples), 1))::bigint AS mem_avg,
       max(mem_max)::bigint     AS mem_max,
       max(mem_limit)::bigint   AS mem_limit,
       max(replicas)::int       AS replicas
FROM metric_samples_1m
WHERE service_id = sqlc.arg(service_id) AND process = sqlc.arg(process)::text AND bucket >= sqlc.arg(since)::timestamptz
GROUP BY 1
ORDER BY 1;

-- name: ServiceMetrics15m :many
-- `process` '' reads the service total rows; a name reads that process' rows.
SELECT date_bin(make_interval(secs => sqlc.arg(bucket_secs)::float8), bucket, TIMESTAMPTZ '2001-01-01')::timestamptz AS bucket,
       (sum(cpu_avg * samples) / GREATEST(sum(samples), 1))::float8 AS cpu_avg,
       max(cpu_max)::float8     AS cpu_max,
       (sum(mem_avg::float8 * samples) / GREATEST(sum(samples), 1))::bigint AS mem_avg,
       max(mem_max)::bigint     AS mem_max,
       max(mem_limit)::bigint   AS mem_limit,
       max(replicas)::int       AS replicas
FROM metric_samples_15m
WHERE service_id = sqlc.arg(service_id) AND process = sqlc.arg(process)::text AND bucket >= sqlc.arg(since)::timestamptz
GROUP BY 1
ORDER BY 1;

-- ===== Disk alert ===========================================================

-- name: SetServerDiskAlert :one
-- Flips disk_alert_active; returns a row only when the value changed, so the
-- caller emits the crossing event exactly once.
UPDATE servers
SET disk_alert_active = sqlc.arg(active)::boolean
WHERE id = sqlc.arg(id) AND disk_alert_active <> sqlc.arg(active)::boolean
RETURNING id, workspace_id, name, disk_alert_threshold_percent;

-- ===== Traffic ==============================================================

-- name: ListValidTrafficServices :many
-- Services (live) in the same workspace as the server.
SELECT sv.id
FROM services sv
JOIN environments e ON e.id = sv.environment_id
JOIN projects p ON p.id = e.project_id
WHERE sv.id = ANY(sqlc.arg(service_ids)::uuid[]) AND sv.deleted_at IS NULL AND p.workspace_id = sqlc.arg(workspace_id);

-- name: ListDomainServicesByHost :many
-- Fallback attribution when an access line carries no service stamp.
SELECT d.hostname, d.service_id
FROM domains d
JOIN services sv ON sv.id = d.service_id
WHERE d.hostname = ANY(sqlc.arg(hostnames)::text[]) AND sv.deleted_at IS NULL AND sv.server_id = sqlc.arg(server_id);

-- name: InsertTraffic1m :exec
INSERT INTO traffic_1m (server_id, service_id, host, bucket, requests, documents, bot_requests, bytes_in, bytes_out,
                        s1xx, s2xx, s3xx, s4xx, s5xx, s_other, lat_hist, lat_sum_ms, visitors, hll, status_codes, top, geo)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22)
ON CONFLICT (server_id, host, bucket) DO NOTHING;

-- name: UpsertTraffic1h :exec
INSERT INTO traffic_1h (server_id, service_id, host, bucket, requests, documents, bot_requests, bytes_in, bytes_out,
                        s1xx, s2xx, s3xx, s4xx, s5xx, s_other, lat_hist, lat_sum_ms, visitors, hll, status_codes, top, geo)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22)
ON CONFLICT (server_id, host, bucket) DO UPDATE SET
    service_id = EXCLUDED.service_id, requests = EXCLUDED.requests, documents = EXCLUDED.documents,
    bot_requests = EXCLUDED.bot_requests, bytes_in = EXCLUDED.bytes_in, bytes_out = EXCLUDED.bytes_out,
    s1xx = EXCLUDED.s1xx, s2xx = EXCLUDED.s2xx, s3xx = EXCLUDED.s3xx, s4xx = EXCLUDED.s4xx, s5xx = EXCLUDED.s5xx,
    s_other = EXCLUDED.s_other, lat_hist = EXCLUDED.lat_hist, lat_sum_ms = EXCLUDED.lat_sum_ms,
    visitors = EXCLUDED.visitors, hll = EXCLUDED.hll, status_codes = EXCLUDED.status_codes, top = EXCLUDED.top, geo = EXCLUDED.geo;

-- name: UpsertTraffic1d :exec
INSERT INTO traffic_1d (server_id, service_id, host, bucket, requests, documents, bot_requests, bytes_in, bytes_out,
                        s1xx, s2xx, s3xx, s4xx, s5xx, s_other, lat_hist, lat_sum_ms, visitors, hll, status_codes, top, geo)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22)
ON CONFLICT (server_id, host, bucket) DO UPDATE SET
    service_id = EXCLUDED.service_id, requests = EXCLUDED.requests, documents = EXCLUDED.documents,
    bot_requests = EXCLUDED.bot_requests, bytes_in = EXCLUDED.bytes_in, bytes_out = EXCLUDED.bytes_out,
    s1xx = EXCLUDED.s1xx, s2xx = EXCLUDED.s2xx, s3xx = EXCLUDED.s3xx, s4xx = EXCLUDED.s4xx, s5xx = EXCLUDED.s5xx,
    s_other = EXCLUDED.s_other, lat_hist = EXCLUDED.lat_hist, lat_sum_ms = EXCLUDED.lat_sum_ms,
    visitors = EXCLUDED.visitors, hll = EXCLUDED.hll, status_codes = EXCLUDED.status_codes, top = EXCLUDED.top, geo = EXCLUDED.geo;

-- name: RollupSourceTraffic1m :many
SELECT * FROM traffic_1m WHERE bucket >= sqlc.arg(from_ts)::timestamptz AND bucket < sqlc.arg(to_ts)::timestamptz;

-- name: RollupSourceTraffic1h :many
SELECT * FROM traffic_1h WHERE bucket >= sqlc.arg(from_ts)::timestamptz AND bucket < sqlc.arg(to_ts)::timestamptz;

-- name: SelectTraffic1m :many
SELECT * FROM traffic_1m
WHERE server_id = ANY(sqlc.arg(server_ids)::uuid[])
  AND bucket >= sqlc.arg(from_ts)::timestamptz AND bucket < sqlc.arg(to_ts)::timestamptz
  AND (sqlc.narg(service_ids)::uuid[] IS NULL OR service_id = ANY(sqlc.narg(service_ids)::uuid[]))
  AND (sqlc.narg(host)::text IS NULL OR host = sqlc.narg(host)::text)
ORDER BY bucket;

-- name: SelectTraffic1h :many
SELECT * FROM traffic_1h
WHERE server_id = ANY(sqlc.arg(server_ids)::uuid[])
  AND bucket >= sqlc.arg(from_ts)::timestamptz AND bucket < sqlc.arg(to_ts)::timestamptz
  AND (sqlc.narg(service_ids)::uuid[] IS NULL OR service_id = ANY(sqlc.narg(service_ids)::uuid[]))
  AND (sqlc.narg(host)::text IS NULL OR host = sqlc.narg(host)::text)
ORDER BY bucket;

-- name: SelectTraffic1d :many
SELECT * FROM traffic_1d
WHERE server_id = ANY(sqlc.arg(server_ids)::uuid[])
  AND bucket >= sqlc.arg(from_ts)::timestamptz AND bucket < sqlc.arg(to_ts)::timestamptz
  AND (sqlc.narg(service_ids)::uuid[] IS NULL OR service_id = ANY(sqlc.narg(service_ids)::uuid[]))
  AND (sqlc.narg(host)::text IS NULL OR host = sqlc.narg(host)::text)
ORDER BY bucket;

-- name: PruneTraffic1m :execrows
DELETE FROM traffic_1m WHERE bucket < sqlc.arg(cutoff)::timestamptz;

-- name: PruneTraffic1h :execrows
DELETE FROM traffic_1h t
USING servers s
WHERE t.server_id = s.id AND t.bucket < now() - make_interval(days => s.traffic_hourly_retention_days);

-- name: PruneTraffic1d :execrows
DELETE FROM traffic_1d t
USING servers s
WHERE t.server_id = s.id AND t.bucket < now() - make_interval(days => s.traffic_daily_retention_days);

-- name: ListWorkspaceServerIDs :many
SELECT id FROM servers WHERE workspace_id = $1;

-- name: ListServiceIDsInProjects :many
SELECT sv.id
FROM services sv
JOIN environments e ON e.id = sv.environment_id
WHERE e.project_id = ANY(sqlc.arg(project_ids)::uuid[]) AND sv.deleted_at IS NULL;

-- name: ListServiceIDsInEnvironment :many
SELECT id FROM services WHERE environment_id = $1 AND deleted_at IS NULL;

-- name: ListDeploymentMarkers :many
SELECT d.id, COALESCE(d.started_at, d.created_at)::timestamptz AS at, d.status,
       COALESCE(r.source->>'commitSha', '')::text AS commit_sha
FROM deployments d
LEFT JOIN releases r ON r.id = d.release_id
WHERE d.service_id = sqlc.arg(service_id)
  AND COALESCE(d.started_at, d.created_at) >= sqlc.arg(from_ts)::timestamptz
  AND COALESCE(d.started_at, d.created_at) < sqlc.arg(to_ts)::timestamptz
ORDER BY 2;
