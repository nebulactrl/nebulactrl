-- ===== Replicas and autoscaling ==============================================

-- name: CountVolumesForServices :many
SELECT service_id, count(*)::int AS n FROM volumes WHERE service_id = ANY(sqlc.arg(service_ids)::uuid[]) GROUP BY service_id;

-- name: CountServiceVolumes :one
SELECT count(*)::int FROM volumes WHERE service_id = $1;

-- name: ApplyServiceScale :one
-- Compare-and-swap on the replica count: no row means a concurrent scale.
-- Operational settings only: generation is NOT bumped (never a staged change).
UPDATE services
SET replicas = sqlc.arg(replicas),
    last_scale_at = now(),
    last_scale_event = sqlc.arg(last_scale_event),
    scale_blocked_reason = NULL,
    updated_at = now()
WHERE id = sqlc.arg(id) AND replicas = sqlc.arg(expected_replicas) AND deleted_at IS NULL
RETURNING *;

-- name: SetServiceAutoscaling :one
UPDATE services
SET autoscaling = sqlc.arg(autoscaling), scale_blocked_reason = NULL, updated_at = now()
WHERE id = sqlc.arg(id) AND deleted_at IS NULL
RETURNING *;

-- name: SetServiceScaleBlocked :one
-- Returns a row only when the stored reason changed, so callers publish once.
UPDATE services
SET scale_blocked_reason = sqlc.narg(reason)::text
WHERE id = sqlc.arg(id) AND deleted_at IS NULL
  AND scale_blocked_reason IS DISTINCT FROM sqlc.narg(reason)::text
RETURNING id;

-- name: ListAutoscalingServices :many
-- Deployed services with autoscaling enabled, with the server of their current deployment.
SELECT sqlc.embed(s), d.server_id AS deployment_server_id
FROM services s
JOIN deployments d ON d.id = s.current_deployment_id
WHERE s.deleted_at IS NULL
  AND d.server_id IS NOT NULL
  AND (s.autoscaling ->> 'enabled')::boolean
ORDER BY s.created_at, s.id;

-- name: ServiceCPUBuckets :many
-- Per-replica CPU (percent of one core) of a service in fixed buckets since
-- `since`: the mean over every replica sample in the bucket. `process` '' reads
-- every sample of the service; a name reads that process' samples only.
SELECT date_bin(make_interval(secs => sqlc.arg(bucket_secs)::float8), ts, TIMESTAMPTZ '2001-01-01')::timestamptz AS bucket,
       avg(cpu_percent)::float8 AS cpu_avg,
       count(*)::int            AS samples
FROM metric_samples
WHERE service_id = sqlc.arg(service_id) AND ts >= sqlc.arg(since)::timestamptz
  AND (sqlc.arg(process)::text = '' OR process = sqlc.arg(process)::text)
GROUP BY 1
ORDER BY 1;

-- name: LatestServiceReplicaSamples :many
-- Newest sample of each replica of a service within the window. `process` '' reads
-- every sample of the service; a name reads that process' samples only.
SELECT DISTINCT ON (replica) replica, ts, cpu_percent, mem_bytes, mem_limit_bytes
FROM metric_samples
WHERE service_id = sqlc.arg(service_id) AND ts > sqlc.arg(since)::timestamptz
  AND (sqlc.arg(process)::text = '' OR process = sqlc.arg(process)::text)
ORDER BY replica, ts DESC;

-- name: LatestServiceInstantReplicaSamples :many
-- The replica samples of the newest sample instant of a service. `process` '' reads
-- every sample of the service; a name reads that process' samples only.
SELECT m.replica, m.ts, m.cpu_percent, m.mem_bytes, m.mem_limit_bytes
FROM metric_samples m
WHERE m.service_id = sqlc.arg(service_id)
  AND (sqlc.arg(process)::text = '' OR m.process = sqlc.arg(process)::text)
  AND m.ts = (SELECT max(m2.ts) FROM metric_samples m2
              WHERE m2.service_id = sqlc.arg(service_id) AND m2.ts > sqlc.arg(since)::timestamptz
                AND (sqlc.arg(process)::text = '' OR m2.process = sqlc.arg(process)::text))
ORDER BY m.replica;

-- name: LatestHostMetricSample :one
-- Newest host sample (heartbeat) of a server within the window.
SELECT ts, cpu_percent, mem_bytes, mem_limit_bytes, disk_used_bytes, disk_total_bytes
FROM metric_samples
WHERE server_id = sqlc.arg(server_id) AND service_id IS NULL AND ts > sqlc.arg(since)::timestamptz
ORDER BY ts DESC
LIMIT 1;
