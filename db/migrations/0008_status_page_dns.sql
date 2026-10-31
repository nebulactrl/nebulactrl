-- +goose Up
-- DNS of a status page's hostname in a connected Cloudflare zone
-- (internal/integrations/cloudflare): the same settings a service domain has.
-- cf_manage lets NebulaCtrl keep the page's A/AAAA records (the control-plane
-- server's public IPs) in sync, cf_proxied turns on the orange cloud, and a
-- proxied page is locked to Cloudflare while cf_origin_lock is true (the
-- default): the status route of the control-plane server then only accepts
-- Cloudflare's ranges and, where available, Authenticated Origin Pulls.
-- cf_status is the last evaluated DNS status ('' = never evaluated), cf_error
-- the last sync failure.
ALTER TABLE status_pages
    ADD COLUMN cf_zone_id      text    NOT NULL DEFAULT '',
    ADD COLUMN cf_zone_name    text    NOT NULL DEFAULT '',
    ADD COLUMN cf_manage       boolean NOT NULL DEFAULT true,
    ADD COLUMN cf_proxied      boolean NOT NULL DEFAULT false,
    ADD COLUMN cf_origin_lock  boolean NOT NULL DEFAULT true,
    ADD COLUMN cf_status       text    NOT NULL DEFAULT '' CHECK (cf_status IN ('','managed','present','wrong_target','missing','not_in_zone')),
    ADD COLUMN cf_message      text    NOT NULL DEFAULT '',
    ADD COLUMN cf_error        text    NOT NULL DEFAULT '',
    ADD COLUMN cf_checked_at   timestamptz;
CREATE INDEX status_pages_cf_zone_idx ON status_pages (cf_zone_id) WHERE cf_zone_id <> '';

-- The tracked records of Cloudflare belong to an owner: a service domain or a
-- status page. owner_id has no foreign key (a record outlives its owner until
-- the collector has deleted it from Cloudflare).
ALTER TABLE cloudflare_records RENAME COLUMN domain_id TO owner_id;
ALTER TABLE cloudflare_records
    ADD COLUMN owner_kind text NOT NULL DEFAULT 'domain' CHECK (owner_kind IN ('domain','status_page'));
ALTER INDEX cloudflare_records_domain_idx RENAME TO cloudflare_records_owner_idx;

-- +goose Down
ALTER INDEX cloudflare_records_owner_idx RENAME TO cloudflare_records_domain_idx;
ALTER TABLE cloudflare_records DROP COLUMN owner_kind;
ALTER TABLE cloudflare_records RENAME COLUMN owner_id TO domain_id;
DROP INDEX status_pages_cf_zone_idx;
ALTER TABLE status_pages
    DROP COLUMN cf_checked_at,
    DROP COLUMN cf_error,
    DROP COLUMN cf_message,
    DROP COLUMN cf_status,
    DROP COLUMN cf_origin_lock,
    DROP COLUMN cf_proxied,
    DROP COLUMN cf_manage,
    DROP COLUMN cf_zone_name,
    DROP COLUMN cf_zone_id;
