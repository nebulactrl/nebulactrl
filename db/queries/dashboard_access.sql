-- Dashboard access (allow list + DNS-01 provider of the system route) -----------

-- name: GetDashboardAccess :one
SELECT * FROM dashboard_access WHERE id;

-- name: SeedDashboardAccess :one
-- The installer's settings: written until an admin edits the row, never after.
INSERT INTO dashboard_access (id, allow_cidrs, dns_resolver, seeded_from_installer)
VALUES (true, sqlc.arg(allow_cidrs)::text[], sqlc.arg(dns_resolver), true)
ON CONFLICT (id) DO UPDATE SET
    allow_cidrs = EXCLUDED.allow_cidrs,
    dns_resolver = EXCLUDED.dns_resolver,
    seeded_from_installer = true,
    updated_at = now()
WHERE dashboard_access.edited_at IS NULL
  AND (dashboard_access.allow_cidrs IS DISTINCT FROM EXCLUDED.allow_cidrs
       OR dashboard_access.dns_resolver IS DISTINCT FROM EXCLUDED.dns_resolver
       OR NOT dashboard_access.seeded_from_installer)
RETURNING *;

-- name: UpdateDashboardAccess :one
-- An admin's edit: from now on the installer's settings are ignored.
INSERT INTO dashboard_access (id, allow_cidrs, dns_resolver, edited_at, edited_by)
VALUES (true, sqlc.arg(allow_cidrs)::text[], sqlc.arg(dns_resolver), now(), sqlc.narg(edited_by))
ON CONFLICT (id) DO UPDATE SET
    allow_cidrs = EXCLUDED.allow_cidrs,
    dns_resolver = EXCLUDED.dns_resolver,
    edited_at = now(),
    edited_by = EXCLUDED.edited_by,
    updated_at = now()
RETURNING *;

-- name: SetDashboardAccessResolverFromInstaller :one
-- The installer's DNS-01 provider, once its resolver is live on the proxy. A
-- row an admin edited is left alone.
UPDATE dashboard_access SET dns_resolver = sqlc.arg(dns_resolver), updated_at = now()
WHERE id AND edited_at IS NULL AND dns_resolver IS DISTINCT FROM sqlc.arg(dns_resolver)
RETURNING *;
