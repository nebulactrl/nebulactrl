-- name: InsertTemplateInstance :one
INSERT INTO template_instances (workspace_id, environment_id, template_id, template_revision, template_version, template_snapshot, name, group_id, inputs, created_by_type, created_by)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
RETURNING *;

-- name: GetTemplateInstance :one
SELECT * FROM template_instances WHERE id = $1 AND deleted_at IS NULL;

-- name: ListEnvironmentTemplateInstances :many
SELECT * FROM template_instances
WHERE environment_id = $1 AND deleted_at IS NULL
ORDER BY created_at, id;

-- name: SetTemplateInstanceVersion :one
-- Records an applied template update: the new revision, app version and snapshot, and the merged
-- non-secret inputs.
UPDATE template_instances
SET template_revision = $2, template_version = $3, template_snapshot = $4, inputs = $5,
    updated_at = now(), updated_by_type = $6, updated_by = $7
WHERE id = $1 AND deleted_at IS NULL
RETURNING *;

-- name: SoftDeleteTemplateInstance :exec
UPDATE template_instances SET deleted_at = now() WHERE id = $1 AND deleted_at IS NULL;

-- name: SetServiceTemplate :exec
UPDATE services SET template_instance_id = $2, template_key = $3 WHERE id = $1;

-- name: ListTemplateInstanceServices :many
SELECT * FROM services
WHERE template_instance_id = $1 AND deleted_at IS NULL
ORDER BY created_at, id;

-- name: CountDomainsByHostname :one
SELECT count(*) FROM domains WHERE hostname = $1;
