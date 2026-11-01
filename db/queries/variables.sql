-- name: ListServiceVariables :many
SELECT * FROM service_variables WHERE service_id = $1 ORDER BY key;

-- name: GetServiceVariable :one
SELECT * FROM service_variables WHERE service_id = $1 AND key = $2;

-- name: UpsertServiceVariable :one
INSERT INTO service_variables (service_id, key, value_enc, is_secret)
VALUES ($1, $2, $3, $4)
ON CONFLICT (service_id, key) DO UPDATE
SET value_enc = EXCLUDED.value_enc, is_secret = EXCLUDED.is_secret, updated_at = now()
RETURNING *;

-- name: DeleteServiceVariable :execrows
DELETE FROM service_variables WHERE service_id = $1 AND key = $2;

-- name: ListEnvironmentVariables :many
SELECT v.* FROM service_variables v
JOIN services s ON s.id = v.service_id
WHERE s.environment_id = $1 AND s.deleted_at IS NULL
ORDER BY v.service_id, v.key;

-- name: ListEnvironmentFirstDomains :many
SELECT DISTINCT ON (d.service_id) d.* FROM domains d
JOIN services s ON s.id = d.service_id
WHERE s.environment_id = $1 AND s.deleted_at IS NULL
ORDER BY d.service_id, d.created_at, d.id;

-- name: PutServiceVariable :one
INSERT INTO service_variables (service_id, key, value_enc, is_secret, scope, source, template_enc, gen_calls, version, generated_at, rotate_every_days, comment)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
ON CONFLICT (service_id, key) DO UPDATE
SET value_enc = EXCLUDED.value_enc, is_secret = EXCLUDED.is_secret, scope = EXCLUDED.scope, source = EXCLUDED.source,
    template_enc = EXCLUDED.template_enc, gen_calls = EXCLUDED.gen_calls, version = EXCLUDED.version,
    generated_at = EXCLUDED.generated_at, rotate_every_days = EXCLUDED.rotate_every_days, comment = EXCLUDED.comment,
    updated_at = now()
RETURNING *;

-- name: InsertVariableVersion :exec
INSERT INTO variable_versions (workspace_id, project_id, owner_kind, owner_id, key, version, op, is_secret, source, scope, value_enc, fingerprint, gen_calls, actor_type, actor_id)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15);

-- name: NextVariableVersion :one
SELECT (coalesce(max(version), 0) + 1)::int AS next FROM variable_versions
WHERE owner_kind = $1 AND owner_id = $2 AND key = $3;

-- name: ListVariableVersions :many
SELECT * FROM variable_versions WHERE owner_kind = $1 AND owner_id = $2 AND key = $3 ORDER BY id DESC LIMIT $4;

-- name: GetVariableVersion :one
SELECT * FROM variable_versions WHERE owner_kind = $1 AND owner_id = $2 AND key = $3 AND version = $4 ORDER BY id DESC LIMIT 1;

-- name: PruneVariableVersions :execrows
WITH ranked AS (
    SELECT id, created_at, row_number() OVER (PARTITION BY owner_kind, owner_id, key ORDER BY id DESC) AS rn
    FROM variable_versions
)
DELETE FROM variable_versions WHERE id IN (
    SELECT ranked.id FROM ranked WHERE ranked.rn > sqlc.arg(keep)::int OR ranked.created_at < sqlc.arg(before)::timestamptz
);

-- name: MarkVariableVersionsReleased :exec
UPDATE variable_versions v SET release_id = $2
FROM service_variables sv
WHERE sv.service_id = $1 AND v.owner_kind = 'service' AND v.owner_id = sv.service_id
  AND v.key = sv.key AND v.version = sv.version AND v.release_id IS NULL;

-- name: ListSharedVariablesProject :many
SELECT * FROM shared_variables WHERE project_id = sqlc.arg(project_id)::uuid AND environment_id IS NULL ORDER BY key;

-- name: ListSharedVariablesEnv :many
SELECT * FROM shared_variables WHERE environment_id = $1 ORDER BY key;

-- name: ListEffectiveSharedVariables :many
SELECT DISTINCT ON (key) * FROM shared_variables
WHERE project_id = sqlc.arg(project_id)::uuid AND (environment_id IS NULL OR environment_id = sqlc.narg(environment_id))
ORDER BY key, (environment_id IS NULL) ASC;

-- name: GetSharedVariableProject :one
SELECT * FROM shared_variables WHERE project_id = sqlc.arg(project_id)::uuid AND environment_id IS NULL AND key = sqlc.arg(key);

-- name: GetSharedVariableEnv :one
SELECT * FROM shared_variables WHERE environment_id = $1 AND key = $2;

-- name: InsertSharedVariable :one
INSERT INTO shared_variables (workspace_id, project_id, environment_id, key, value_enc, is_secret, source, template_enc, gen_calls, version, generated_at, comment, id)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
RETURNING *;

-- name: UpdateSharedVariable :one
UPDATE shared_variables
SET value_enc = $2, is_secret = $3, source = $4, template_enc = $5, gen_calls = $6, version = $7, generated_at = $8, comment = $9, updated_at = now()
WHERE id = $1
RETURNING *;

-- name: DeleteSharedVariable :execrows
DELETE FROM shared_variables WHERE id = $1;

-- name: SetServiceSuggestedEnv :exec
UPDATE services SET suggested_env = $2 WHERE id = $1;

-- name: ListRotationDueVariables :many
SELECT sv.service_id, sv.key, sv.rotate_every_days, sv.updated_at
FROM service_variables sv JOIN services s ON s.id = sv.service_id
WHERE s.deleted_at IS NULL AND sv.rotate_every_days IS NOT NULL
  AND sv.updated_at + make_interval(days => sv.rotate_every_days) < now();

-- name: GetEnvInfo :one
SELECT e.id, e.project_id, e.slug, e.name, p.name AS project_name, p.workspace_id
FROM environments e JOIN projects p ON p.id = e.project_id
WHERE e.id = $1;
