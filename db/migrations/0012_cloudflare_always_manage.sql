-- +goose Up
-- A hostname in a zone of the team's connected Cloudflare account always has
-- its DNS records managed by NebulaCtrl: it is no longer a per-domain, per-page
-- or per-team choice. The next sweep syncs every hostname that was opted out.
-- Names start proxied (orange cloud) unless they cannot be; the team-wide
-- default is gone with it, and names that exist keep the proxy setting they have.
ALTER TABLE domains DROP COLUMN cf_manage;
ALTER TABLE status_pages DROP COLUMN cf_manage;
ALTER TABLE cloudflare_connections
    DROP COLUMN auto_manage,
    DROP COLUMN default_proxied;

-- 'conflict': a record NebulaCtrl did not create differs from the wanted one or
-- blocks it. It is left untouched until the person replaces it.
ALTER TABLE domains DROP CONSTRAINT domains_cf_status_check;
ALTER TABLE domains ADD CONSTRAINT domains_cf_status_check
    CHECK (cf_status IN ('','managed','present','conflict','wrong_target','missing','not_in_zone'));
ALTER TABLE status_pages DROP CONSTRAINT status_pages_cf_status_check;
ALTER TABLE status_pages ADD CONSTRAINT status_pages_cf_status_check
    CHECK (cf_status IN ('','managed','present','conflict','wrong_target','missing','not_in_zone'));

-- +goose Down
UPDATE domains SET cf_status = 'wrong_target' WHERE cf_status = 'conflict';
UPDATE status_pages SET cf_status = 'wrong_target' WHERE cf_status = 'conflict';
ALTER TABLE status_pages DROP CONSTRAINT status_pages_cf_status_check;
ALTER TABLE status_pages ADD CONSTRAINT status_pages_cf_status_check
    CHECK (cf_status IN ('','managed','present','wrong_target','missing','not_in_zone'));
ALTER TABLE domains DROP CONSTRAINT domains_cf_status_check;
ALTER TABLE domains ADD CONSTRAINT domains_cf_status_check
    CHECK (cf_status IN ('','managed','present','wrong_target','missing','not_in_zone'));
ALTER TABLE cloudflare_connections
    ADD COLUMN auto_manage     boolean NOT NULL DEFAULT true,
    ADD COLUMN default_proxied boolean NOT NULL DEFAULT false;
ALTER TABLE status_pages ADD COLUMN cf_manage boolean NOT NULL DEFAULT true;
ALTER TABLE domains ADD COLUMN cf_manage boolean NOT NULL DEFAULT true;
