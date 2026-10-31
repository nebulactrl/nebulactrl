-- name: ListServiceVolumes :many
SELECT * FROM volumes WHERE service_id = $1 ORDER BY created_at, id;

-- name: InsertVolume :one
INSERT INTO volumes (service_id, name, mount_path) VALUES ($1, $2, $3) RETURNING *;

-- name: GetVolumeByID :one
SELECT * FROM volumes WHERE id = $1;

-- name: DeleteVolume :execrows
DELETE FROM volumes WHERE id = $1;

-- name: DeleteServiceVolumes :exec
DELETE FROM volumes WHERE service_id = $1;

-- name: SetVolumeDockerName :exec
-- Points a volume row at another Docker volume (a staged restore swap); an
-- empty docker_name means the volume derived from the row's name.
UPDATE volumes SET docker_name = $2, restore_label = $3 WHERE id = $1;
