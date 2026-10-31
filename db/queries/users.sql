-- name: CountUsers :one
SELECT count(*) FROM users;

-- name: CreateUser :one
-- Creates a user who owns the given team (a membership with the owner role).
WITH u AS (
    INSERT INTO users (email, name, password_hash)
    VALUES (sqlc.arg(email), sqlc.arg(name), sqlc.arg(password_hash))
    RETURNING *
), m AS (
    INSERT INTO memberships (user_id, workspace_id, role)
    SELECT u.id, sqlc.arg(workspace_id), 'owner' FROM u
)
SELECT u.id, u.email, u.name, u.password_hash, u.created_at FROM u;

-- name: InsertUser :one
-- Creates a user without any team; the caller adds memberships.
INSERT INTO users (email, name, password_hash)
VALUES ($1, $2, $3)
RETURNING *;

-- name: GetUser :one
SELECT * FROM users WHERE id = $1;

-- name: GetUserByEmail :one
SELECT * FROM users WHERE lower(email) = lower($1);

-- name: UpdateUserProfile :one
UPDATE users SET name = $2 WHERE id = $1 RETURNING *;

-- name: UpdateUserPassword :exec
UPDATE users SET password_hash = $2 WHERE id = $1;

-- name: FirstUserID :one
-- The instance admin: the earliest-created user.
SELECT id FROM users ORDER BY created_at, id LIMIT 1;
