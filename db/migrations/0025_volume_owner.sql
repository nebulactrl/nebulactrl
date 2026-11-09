-- +goose Up
-- Who owns the root directory of a service volume: "" (as Docker created it),
-- "uid" or "uid:gid" (numeric). The agent chowns the volume root, never
-- recursively, so an image that runs as a non-root user can write to a volume
-- mounted at a path the image does not contain.
ALTER TABLE volumes ADD COLUMN owner text NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE volumes DROP COLUMN owner;
