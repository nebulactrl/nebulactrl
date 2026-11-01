-- name: InsertRelease :one
INSERT INTO releases (service_id, source, config, env_enc, image_ref, id)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: GetReleaseByID :one
SELECT * FROM releases WHERE id = $1;

-- name: UpdateReleaseSource :exec
UPDATE releases SET source = $2 WHERE id = $1;

-- name: ListRecentServiceReleases :many
SELECT * FROM releases WHERE service_id = $1 ORDER BY created_at DESC, id DESC LIMIT $2;

-- name: ListNewestSucceededReleaseIDs :many
SELECT release_id::uuid AS release_id FROM (
    SELECT release_id, max(finished_at) AS last_ok
    FROM deployments
    WHERE service_id = sqlc.arg(service_id) AND status = 'succeeded' AND release_id IS NOT NULL
    GROUP BY release_id
) t
ORDER BY last_ok DESC
LIMIT sqlc.arg(row_limit);

-- name: PruneReleaseEnv :execrows
-- Release secret retention: drops the sealed environment of every release that
-- is outside its service's rollback window. Kept: the newest imageRetention
-- succeeded releases of a service (the set a rollback can target), the release
-- of the running deployment and the release of any deployment in flight.
WITH ranked AS (
    SELECT d.service_id, d.release_id,
           row_number() OVER (PARTITION BY d.service_id ORDER BY max(d.finished_at) DESC) AS rn
    FROM deployments d
    WHERE d.status = 'succeeded' AND d.release_id IS NOT NULL
    GROUP BY d.service_id, d.release_id
), keep AS (
    SELECT r.release_id FROM ranked r
    JOIN services s ON s.id = r.service_id
    WHERE r.rn <= GREATEST(COALESCE(NULLIF(s.config->>'imageRetention', '')::int, 5), 1)
    UNION
    SELECT d.release_id FROM deployments d
    WHERE d.release_id IS NOT NULL AND d.status IN ('created', 'queued', 'preparing', 'building', 'releasing', 'verifying')
    UNION
    SELECT d.release_id FROM services s JOIN deployments d ON d.id = s.current_deployment_id
    WHERE d.release_id IS NOT NULL
)
UPDATE releases SET env_enc = NULL
WHERE env_enc IS NOT NULL AND id NOT IN (SELECT release_id FROM keep);

-- name: SetReleaseEnvIfPruned :execrows
-- Gives a release whose environment was pruned the environment it is deployed
-- with now (a rollback to a release outside the retention window).
UPDATE releases SET env_enc = sqlc.arg(env_enc) WHERE id = sqlc.arg(id) AND env_enc IS NULL;
