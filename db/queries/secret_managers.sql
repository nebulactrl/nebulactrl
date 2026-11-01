-- name: ListSecretIntegrations :many
SELECT * FROM secret_integrations WHERE workspace_id = $1 ORDER BY name;

-- name: ListSecretIntegrationAccess :many
-- Every integration of the team with whether the given project and environment
-- may use it: team-wide integrations always, selected ones through a grant on
-- the project (environment_id NULL = every environment) or on the environment.
SELECT i.name, i.kind,
       (i.scope = 'team' OR EXISTS (
           SELECT 1 FROM secret_integration_scopes g
           WHERE g.integration_id = i.id AND g.project_id = sqlc.arg(project_id)::uuid
             AND (g.environment_id IS NULL OR g.environment_id = sqlc.arg(environment_id)::uuid)
       ))::boolean AS allowed
FROM secret_integrations i
WHERE i.workspace_id = sqlc.arg(workspace_id)::uuid;

-- name: SecretIntegrationAllowed :one
SELECT (i.scope = 'team' OR EXISTS (
           SELECT 1 FROM secret_integration_scopes g
           WHERE g.integration_id = i.id AND g.project_id = sqlc.arg(project_id)::uuid
             AND (g.environment_id IS NULL OR g.environment_id = sqlc.arg(environment_id)::uuid)
       ))::boolean AS allowed
FROM secret_integrations i
WHERE i.id = sqlc.arg(id)::uuid;

-- name: ListSecretIntegrationGrants :many
SELECT g.integration_id, g.project_id, g.environment_id,
       p.name AS project_name, p.slug AS project_slug,
       e.name AS environment_name, e.slug AS environment_slug
FROM secret_integration_scopes g
JOIN secret_integrations i ON i.id = g.integration_id
JOIN projects p ON p.id = g.project_id
LEFT JOIN environments e ON e.id = g.environment_id
WHERE i.workspace_id = $1
ORDER BY p.name, e.name NULLS FIRST;

-- name: DeleteSecretIntegrationGrants :exec
DELETE FROM secret_integration_scopes WHERE integration_id = $1;

-- name: InsertSecretIntegrationGrant :exec
INSERT INTO secret_integration_scopes (integration_id, project_id, environment_id) VALUES ($1, $2, $3);

-- name: SecretGrantProject :one
SELECT id FROM projects WHERE id = $1 AND workspace_id = $2;

-- name: SecretGrantEnvironment :one
SELECT id FROM environments WHERE id = $1 AND project_id = $2;

-- name: GetSecretIntegration :one
SELECT * FROM secret_integrations WHERE id = $1 AND workspace_id = $2;

-- name: GetSecretIntegrationByName :one
SELECT * FROM secret_integrations WHERE name = $1 AND workspace_id = $2;

-- name: InsertSecretIntegration :one
INSERT INTO secret_integrations (id, workspace_id, name, kind, config, credentials_enc, scope)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING *;

-- name: UpdateSecretIntegration :one
UPDATE secret_integrations
SET kind = $3, config = $4, credentials_enc = $5, scope = $6, updated_at = now()
WHERE id = $1 AND workspace_id = $2
RETURNING *;

-- name: SetSecretIntegrationTest :exec
UPDATE secret_integrations SET last_test_at = now(), last_test_ok = $2, last_test_error = $3 WHERE id = $1;

-- name: DeleteSecretIntegration :execrows
DELETE FROM secret_integrations WHERE id = $1 AND workspace_id = $2;

-- name: ListTeamSharedVariables :many
SELECT * FROM shared_variables WHERE workspace_id = $1 AND project_id IS NULL ORDER BY key;

-- name: GetSharedVariableTeam :one
SELECT * FROM shared_variables WHERE workspace_id = $1 AND project_id IS NULL AND key = $2;

-- name: ListWorkspaceEnvInfos :many
SELECT e.id, e.project_id, e.slug, e.name, p.name AS project_name
FROM environments e JOIN projects p ON p.id = e.project_id
WHERE p.workspace_id = $1
ORDER BY p.name, e.name;

-- name: ListWorkspaceSharedVariables :many
SELECT * FROM shared_variables WHERE workspace_id = $1 ORDER BY key;
