-- name: GetCanvasLayout :one
SELECT * FROM canvas_layouts WHERE environment_id = $1;

-- name: InsertCanvasLayout :one
INSERT INTO canvas_layouts (environment_id, layout, version)
VALUES ($1, $2, 1)
ON CONFLICT (environment_id) DO NOTHING
RETURNING *;

-- name: UpdateCanvasLayoutCAS :one
UPDATE canvas_layouts
SET layout = sqlc.arg(layout), version = version + 1, updated_at = now()
WHERE environment_id = sqlc.arg(environment_id) AND version = sqlc.arg(expected_version)
RETURNING *;
