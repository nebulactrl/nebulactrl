-- name: InsertServerMetricSample :exec
INSERT INTO metric_samples (server_id, cpu_percent, mem_bytes, mem_limit_bytes, disk_used_bytes, disk_total_bytes)
VALUES ($1, $2, $3, $4, $5, $6);

-- name: InsertServiceMetricSample :exec
-- One row per replica container. Every replica of a stats frame carries the
-- same ts so replicas can be summed per sample instant. process is the
-- container's process ('' for single-process services). Dropped silently when
-- the service does not run on server_id, so one agent cannot write another
-- server's series.
INSERT INTO metric_samples (server_id, service_id, ts, replica, process, cpu_percent, mem_bytes, mem_limit_bytes)
SELECT sv.server_id, sv.id, sqlc.arg(ts)::timestamptz, sqlc.arg(replica)::int, sqlc.arg(process)::text, sqlc.arg(cpu_percent)::float8, sqlc.arg(mem_bytes)::bigint, sqlc.arg(mem_limit_bytes)::bigint
FROM services sv
WHERE sv.id = sqlc.arg(service_id) AND sv.server_id = sqlc.arg(server_id) AND sv.deleted_at IS NULL;

-- name: LatestServerMetricSamples :many
SELECT DISTINCT ON (server_id) server_id, ts, cpu_percent, mem_bytes, mem_limit_bytes, disk_used_bytes, disk_total_bytes
FROM metric_samples
WHERE server_id = ANY(sqlc.arg(server_ids)::uuid[]) AND service_id IS NULL
ORDER BY server_id, ts DESC;

-- name: DeleteMetricSamplesBefore :execrows
DELETE FROM metric_samples WHERE ts < $1;
