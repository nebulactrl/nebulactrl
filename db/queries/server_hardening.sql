-- name: GetServerHardeningSettings :one
SELECT * FROM server_hardening_settings WHERE server_id = $1;

-- name: UpsertServerHardeningSettings :one
INSERT INTO server_hardening_settings (server_id, workspace_id, weekly)
VALUES ($1, $2, $3)
ON CONFLICT (server_id) DO UPDATE SET weekly = EXCLUDED.weekly, updated_at = now()
RETURNING *;

-- name: GetServerHardeningState :one
SELECT * FROM server_hardening_state WHERE server_id = $1;

-- name: UpsertServerHardeningState :one
-- Keeps the previous report and fix states when the new result has none (a
-- server without Lynis still reports its fixes), and the first index forever.
INSERT INTO server_hardening_state (server_id, workspace_id, lynis_installed, lynis_version, mode,
                                    first_index, first_audited_at, hardening_index, audited_at, report, fixes, reboot_required)
VALUES (sqlc.arg(server_id), sqlc.arg(workspace_id), sqlc.arg(lynis_installed), sqlc.arg(lynis_version), sqlc.arg(mode),
        sqlc.narg(hardening_index)::integer, CASE WHEN sqlc.narg(hardening_index)::integer IS NULL THEN NULL ELSE now() END,
        sqlc.narg(hardening_index)::integer, CASE WHEN sqlc.narg(hardening_index)::integer IS NULL THEN NULL ELSE now() END,
        sqlc.narg(report)::jsonb, sqlc.narg(fixes)::jsonb, sqlc.arg(reboot_required))
ON CONFLICT (server_id) DO UPDATE
SET lynis_installed = EXCLUDED.lynis_installed,
    lynis_version = CASE WHEN EXCLUDED.lynis_version = '' THEN server_hardening_state.lynis_version ELSE EXCLUDED.lynis_version END,
    mode = EXCLUDED.mode,
    first_index = COALESCE(server_hardening_state.first_index, EXCLUDED.first_index),
    first_audited_at = COALESCE(server_hardening_state.first_audited_at, EXCLUDED.first_audited_at),
    hardening_index = COALESCE(EXCLUDED.hardening_index, server_hardening_state.hardening_index),
    audited_at = COALESCE(EXCLUDED.audited_at, server_hardening_state.audited_at),
    report = COALESCE(EXCLUDED.report, server_hardening_state.report),
    fixes = COALESCE(EXCLUDED.fixes, server_hardening_state.fixes),
    reboot_required = EXCLUDED.reboot_required,
    updated_at = now()
RETURNING *;

-- name: InsertServerHardeningRun :one
INSERT INTO server_hardening_runs (server_id, workspace_id, action, trigger, requested_by, fix_ids, index_before)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING *;

-- name: SetServerHardeningRunCommand :exec
UPDATE server_hardening_runs SET command_id = $2 WHERE id = $1;

-- name: GetServerHardeningRunByCommand :one
SELECT * FROM server_hardening_runs WHERE command_id = $1;

-- name: ListServerHardeningRuns :many
SELECT * FROM server_hardening_runs WHERE server_id = $1 ORDER BY created_at DESC, id DESC LIMIT $2;

-- name: FinishServerHardeningRun :one
-- Records the outcome of a pending run once; a duplicate result changes nothing
-- and returns no row.
UPDATE server_hardening_runs
SET status = sqlc.arg(status)::text,
    hardening_index = sqlc.narg(hardening_index)::integer,
    lynis_version = sqlc.arg(lynis_version)::text,
    outcomes = sqlc.narg(outcomes)::jsonb,
    message = sqlc.arg(message)::text,
    error = sqlc.arg(error)::text,
    finished_at = now()
WHERE id = sqlc.arg(id) AND status = 'pending'
RETURNING *;

-- name: PruneServerHardeningRuns :exec
DELETE FROM server_hardening_runs r
WHERE r.server_id = sqlc.arg(server_id)
  AND r.id NOT IN (
      SELECT k.id FROM server_hardening_runs k WHERE k.server_id = sqlc.arg(server_id)
      ORDER BY k.created_at DESC, k.id DESC LIMIT sqlc.arg(keep)::int
  );

-- name: HasPendingServerHardeningRun :one
-- A run the agent has not answered yet. A pending run older than a day is not
-- counted (its command was lost); normally HandleUndelivered closes such runs.
SELECT EXISTS (
    SELECT 1 FROM server_hardening_runs
    WHERE server_id = $1 AND status = 'pending' AND created_at > now() - interval '1 day'
);

-- name: ListServersDueForHardening :many
-- Online servers with the weekly audit on (no settings row means on) that have
-- had no run for a week, with the facts the sweep needs to check the agent is
-- recent enough.
SELECT s.id, s.workspace_id, s.name, s.facts
FROM servers s
LEFT JOIN server_hardening_settings h ON h.server_id = s.id
WHERE s.status = 'online' AND COALESCE(h.weekly, true)
  AND NOT EXISTS (
      SELECT 1 FROM server_hardening_runs r
      WHERE r.server_id = s.id AND r.created_at > now() - interval '7 days'
  );
