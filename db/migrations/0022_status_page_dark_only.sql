-- +goose Up
-- Public status pages are always dark: the per-page theme is gone.
ALTER TABLE status_pages DROP COLUMN theme;

-- +goose Down
ALTER TABLE status_pages ADD COLUMN theme text NOT NULL DEFAULT 'auto' CHECK (theme IN ('auto','light','dark'));
