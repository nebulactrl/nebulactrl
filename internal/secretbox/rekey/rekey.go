// Package rekey re-encrypts every sealed database column to the newest key of
// the key ring and to the v2 ciphertext format (docs/security/secrets-at-rest.md).
//
// It walks the registry in internal/secretbox. A column is processed in batches,
// each batch in one transaction that locks its rows, so a run is resumable (rows
// already on the current key are skipped by a header comparison in SQL),
// idempotent (a second run changes nothing) and safe next to a running server
// (rows are locked while they are rewritten, and the UPDATE re-checks the old
// ciphertext).
package rekey

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nebulactrl/nebulactrl/internal/secretbox"
)

// DefaultBatch is the number of rows rewritten per transaction.
const DefaultBatch = 200

// lockID is the advisory lock that keeps two rekey runs apart ("nbrk").
const lockID int64 = 0x6e62726b

// Options configure Run.
type Options struct {
	// Batch is the rows per transaction (default DefaultBatch).
	Batch int
	// DryRun reads and decrypts every row that needs rewriting but writes nothing.
	DryRun bool
	// Columns overrides the registry (tests).
	Columns []secretbox.Column
	// Verify, when set, is a Box holding ONLY the current key. After the run
	// every non-empty value is opened with it: the old keys can be removed from
	// the environment once the report has no failures.
	Verify *secretbox.Box
	Logger *slog.Logger
}

// ColumnReport is the outcome for one sealed column.
type ColumnReport struct {
	Column secretbox.Column
	// Total counts non-empty values.
	Total int
	// Current counts values already on the current key in the v2 format.
	Current int
	// Legacy counts v1 values (no key id, no row binding).
	Legacy int
	// Rewritten counts values re-encrypted by this run (DryRun: that would be).
	Rewritten int
	// Failed counts values that did not decrypt (wrong or missing key, corrupt
	// data, or a ciphertext that was copied between rows). They are left as is.
	Failed int
	// Verified counts values that opened with the verify box (Verify only).
	Verified int
	// FailedRows are row ids of Failed values (at most 20).
	FailedRows []string
}

// Report is the outcome of a Run or Status.
type Report struct {
	KeyID   string
	DryRun  bool
	Columns []ColumnReport
}

// Pending is the number of values not yet on the current key.
func (r Report) Pending() int {
	n := 0
	for _, c := range r.Columns {
		n += c.Total - c.Current
	}
	return n
}

// Failed is the number of values that could not be decrypted.
func (r Report) Failed() int {
	n := 0
	for _, c := range r.Columns {
		n += c.Failed
	}
	return n
}

// Rewritten is the number of values re-encrypted (DryRun: that would be).
func (r Report) Rewritten() int {
	n := 0
	for _, c := range r.Columns {
		n += c.Rewritten
	}
	return n
}

func columns(opts Options) []secretbox.Column {
	if opts.Columns != nil {
		return opts.Columns
	}
	return secretbox.All()
}

func quote(c secretbox.Column) (table, column string) {
	return pgx.Identifier{c.Table}.Sanitize(), pgx.Identifier{c.Column}.Sanitize()
}

// Status counts, without decrypting anything, how many values of every sealed
// column are on the current key, in the legacy v1 format, or on another key.
func Status(ctx context.Context, pool *pgxpool.Pool, box *secretbox.Box, opts Options) (Report, error) {
	rep := Report{KeyID: box.KeyID()}
	for _, c := range columns(opts) {
		table, col := quote(c)
		cr := ColumnReport{Column: c}
		q := fmt.Sprintf(`SELECT count(*)::int,
			count(*) FILTER (WHERE substring(%[2]s from 1 for %[3]d) = $1)::int,
			count(*) FILTER (WHERE get_byte(%[2]s, 0) = %[4]d)::int
			FROM %[1]s WHERE %[2]s IS NOT NULL AND octet_length(%[2]s) > 0`, table, col, secretbox.HeaderSize, secretbox.FormatV1)
		if err := pool.QueryRow(ctx, q, box.Header()).Scan(&cr.Total, &cr.Current, &cr.Legacy); err != nil {
			return rep, fmt.Errorf("count %s: %w (has the database been migrated?)", c, err)
		}
		rep.Columns = append(rep.Columns, cr)
	}
	return rep, nil
}

// Run re-encrypts every registered column to the current key of box.
func Run(ctx context.Context, pool *pgxpool.Pool, box *secretbox.Box, opts Options) (Report, error) {
	if opts.Batch <= 0 {
		opts.Batch = DefaultBatch
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	rep := Report{KeyID: box.KeyID(), DryRun: opts.DryRun}

	// One run at a time: a session advisory lock on a dedicated connection.
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return rep, fmt.Errorf("acquire connection: %w", err)
	}
	defer conn.Release()
	var locked bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, lockID).Scan(&locked); err != nil {
		return rep, fmt.Errorf("advisory lock: %w", err)
	}
	if !locked {
		return rep, errors.New("another rekey is already running")
	}
	defer func() { _, _ = conn.Exec(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock($1)`, lockID) }()

	status, err := Status(ctx, pool, box, opts)
	if err != nil {
		return rep, err
	}
	for _, cr := range status.Columns {
		start := time.Now()
		if opts.DryRun {
			err = dryRun(ctx, pool, box, &cr)
		} else {
			err = rewrite(ctx, pool, box, &cr, opts)
		}
		if err != nil {
			rep.Columns = append(rep.Columns, cr)
			return rep, fmt.Errorf("%s: %w", cr.Column, err)
		}
		if opts.Verify != nil && !opts.DryRun {
			if err := verify(ctx, pool, opts.Verify, &cr); err != nil {
				rep.Columns = append(rep.Columns, cr)
				return rep, fmt.Errorf("verify %s: %w", cr.Column, err)
			}
		}
		rep.Columns = append(rep.Columns, cr)
		if cr.Rewritten > 0 || cr.Failed > 0 {
			opts.Logger.InfoContext(ctx, "rekey column", "column", cr.Column.String(), "rewritten", cr.Rewritten, "failed", cr.Failed,
				"dry_run", opts.DryRun, "took", time.Since(start).Round(time.Millisecond).String())
		}
	}
	return rep, nil
}

// needsRekey is the SQL predicate for "not yet on the current key".
func needsRekey(col string) string {
	return fmt.Sprintf(`%[1]s IS NOT NULL AND octet_length(%[1]s) > 0 AND substring(%[1]s from 1 for %[2]d) <> $1`, col, secretbox.HeaderSize)
}

func noteFailure(cr *ColumnReport, rowID string) {
	cr.Failed++
	if len(cr.FailedRows) < 20 {
		cr.FailedRows = append(cr.FailedRows, rowID)
	}
}

func dryRun(ctx context.Context, pool *pgxpool.Pool, box *secretbox.Box, cr *ColumnReport) error {
	table, col := quote(cr.Column)
	q := fmt.Sprintf(`SELECT (%s), %s FROM %s WHERE %s`, cr.Column.RowKey, col, table, needsRekey(col))
	rows, err := pool.Query(ctx, q, box.Header())
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var rowID string
		var ct []byte
		if err := rows.Scan(&rowID, &ct); err != nil {
			return err
		}
		plain, err := box.Open(cr.Column, rowID, ct)
		if err != nil {
			noteFailure(cr, rowID)
			continue
		}
		secretbox.Wipe(plain)
		cr.Rewritten++
	}
	return rows.Err()
}

func rewrite(ctx context.Context, pool *pgxpool.Pool, box *secretbox.Box, cr *ColumnReport, opts Options) error {
	table, col := quote(cr.Column)
	skip := []string{} // row ids that failed to decrypt: not selected again (never nil: NULL would match nothing)
	for {
		n, err := rewriteBatch(ctx, pool, box, cr, table, col, opts.Batch, &skip)
		if err != nil {
			return err
		}
		if n == 0 {
			return nil
		}
	}
}

// rewriteBatch rewrites up to batch rows in one transaction and returns how
// many rows it looked at (rewritten or failed).
func rewriteBatch(ctx context.Context, pool *pgxpool.Pool, box *secretbox.Box, cr *ColumnReport, table, col string, batch int, skip *[]string) (int, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	q := fmt.Sprintf(`SELECT ctid::text, (%[1]s), %[2]s FROM %[3]s WHERE %[4]s AND NOT ((%[1]s) = ANY($2::text[])) LIMIT %[5]d FOR UPDATE`,
		cr.Column.RowKey, col, table, needsRekey(col), batch)
	rows, err := tx.Query(ctx, q, box.Header(), *skip)
	if err != nil {
		return 0, err
	}
	type item struct {
		tid, rowID string
		ct         []byte
	}
	var items []item
	for rows.Next() {
		var it item
		if err := rows.Scan(&it.tid, &it.rowID, &it.ct); err != nil {
			rows.Close()
			return 0, err
		}
		items = append(items, it)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	upd := fmt.Sprintf(`UPDATE %s SET %s = $1 WHERE ctid = $2::tid AND %s = $3`, table, col, col)
	var rewritten int
	var failed []string
	for _, it := range items {
		plain, err := box.Open(cr.Column, it.rowID, it.ct)
		if err != nil {
			failed = append(failed, it.rowID)
			continue
		}
		sealed, err := box.Seal(cr.Column, it.rowID, plain)
		secretbox.Wipe(plain)
		if err != nil {
			return 0, err
		}
		tag, err := tx.Exec(ctx, upd, sealed, it.tid, it.ct)
		if err != nil {
			return 0, err
		}
		if tag.RowsAffected() != 1 {
			return 0, fmt.Errorf("row %s changed during the rewrite", it.rowID)
		}
		rewritten++
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	cr.Rewritten += rewritten
	for _, id := range failed {
		noteFailure(cr, id)
		*skip = append(*skip, id)
	}
	return len(items), nil
}

// verify opens every non-empty value with a box that holds only the current key.
func verify(ctx context.Context, pool *pgxpool.Pool, only *secretbox.Box, cr *ColumnReport) error {
	table, col := quote(cr.Column)
	q := fmt.Sprintf(`SELECT (%s), %s FROM %s WHERE %[2]s IS NOT NULL AND octet_length(%[2]s) > 0`, cr.Column.RowKey, col, table)
	rows, err := pool.Query(ctx, q)
	if err != nil {
		return err
	}
	defer rows.Close()
	cr.Verified, cr.Failed, cr.FailedRows = 0, 0, nil
	for rows.Next() {
		var rowID string
		var ct []byte
		if err := rows.Scan(&rowID, &ct); err != nil {
			return err
		}
		if only.NeedsRekey(ct) {
			noteFailure(cr, rowID)
			continue
		}
		plain, err := only.Open(cr.Column, rowID, ct)
		if err != nil {
			noteFailure(cr, rowID)
			continue
		}
		secretbox.Wipe(plain)
		cr.Verified++
	}
	return rows.Err()
}
