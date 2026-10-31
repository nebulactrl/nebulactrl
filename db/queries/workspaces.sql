-- name: GetWorkspace :one
SELECT * FROM workspaces WHERE id = $1;

-- name: FirstWorkspace :one
SELECT * FROM workspaces ORDER BY created_at LIMIT 1;

-- name: CreateWorkspace :one
INSERT INTO workspaces (name) VALUES ($1) RETURNING *;
