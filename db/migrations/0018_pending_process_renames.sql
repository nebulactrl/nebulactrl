-- +goose Up
-- Renames of a named process that are staged but not deployed yet: the running release still
-- uses from_process (its scale row and domains keep that name), the staged config names it
-- to_process. A deploy moves the domains over (FinalizeProcessRenames) and clears the row.
CREATE TABLE service_process_rename (
    service_id   uuid NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    from_process text NOT NULL CHECK (from_process ~ '^[a-z0-9]([a-z0-9-]{0,29}[a-z0-9])?$'),
    to_process   text NOT NULL CHECK (to_process ~ '^[a-z0-9]([a-z0-9-]{0,29}[a-z0-9])?$'),
    PRIMARY KEY (service_id, to_process)
);

-- +goose Down
DROP TABLE service_process_rename;
