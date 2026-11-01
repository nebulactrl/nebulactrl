package rekey_test

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/secretbox/rekey"
	"github.com/nebulactrl/nebulactrl/internal/testutil"
)

func key(t *testing.T) []byte {
	t.Helper()
	k := make([]byte, secretbox.KeySize)
	_, err := rand.Read(k)
	require.NoError(t, err)
	return k
}

// sealV1 writes a pre-upgrade ciphertext (version 1, no key id, AAD = [1]).
func sealV1(t *testing.T, k, plain []byte) []byte {
	t.Helper()
	block, err := aes.NewCipher(k)
	require.NoError(t, err)
	aead, err := cipher.NewGCM(block)
	require.NoError(t, err)
	nonce := make([]byte, aead.NonceSize())
	_, err = rand.Read(nonce)
	require.NoError(t, err)
	return aead.Seal(append([]byte{secretbox.FormatV1}, nonce...), nonce, plain, []byte{secretbox.FormatV1})
}

// TestRegistryMatchesSchema fails when a migration adds a bytea *_enc column
// that nobody registered (rekey would silently skip it), or when the registry
// names a column that does not exist.
func TestRegistryMatchesSchema(t *testing.T) {
	pool := testutil.NewPool(t)
	rows, err := pool.Query(context.Background(), `
		SELECT table_name, column_name FROM information_schema.columns
		WHERE table_schema = current_schema() AND data_type = 'bytea' AND column_name LIKE '%\_enc'`)
	require.NoError(t, err)
	defer rows.Close()
	inSchema := map[string]bool{}
	for rows.Next() {
		var tbl, col string
		require.NoError(t, rows.Scan(&tbl, &col))
		inSchema[tbl+"."+col] = true
	}
	require.NoError(t, rows.Err())

	registered := map[string]bool{}
	for _, c := range secretbox.All() {
		registered[c.String()] = true
		require.True(t, inSchema[c.String()], "registry entry %s is not a bytea column of the schema", c)
	}
	for name := range inSchema {
		require.True(t, registered[name], "%s is a sealed-looking (bytea *_enc) column but is not in secretbox/registry.go; register it so rekey covers it", name)
	}
}

// clone creates a scratch table with the shape of the registry column's table,
// every column nullable and filled with random values, and returns the column
// pointing at it (same RowKey, same column name) plus the row ids.
func clone(t *testing.T, pool *pgxpool.Pool, n int, c secretbox.Column, rows int) (secretbox.Column, []string) {
	t.Helper()
	ctx := context.Background()
	name := fmt.Sprintf("rk_%d", n)
	_, err := pool.Exec(ctx, fmt.Sprintf(`CREATE TABLE %s (LIKE %s INCLUDING DEFAULTS)`, name, c.Table))
	require.NoError(t, err)
	cr, err := pool.Query(ctx, `SELECT column_name, data_type FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = $1`, name)
	require.NoError(t, err)
	var sets []string
	var cols []string
	for cr.Next() {
		var col, typ string
		require.NoError(t, cr.Scan(&col, &typ))
		cols = append(cols, col)
		switch typ {
		case "uuid":
			sets = append(sets, fmt.Sprintf(`%q = gen_random_uuid()`, col))
		case "text", "character varying":
			sets = append(sets, fmt.Sprintf(`%q = md5(random()::text)`, col))
		case "integer", "bigint", "smallint":
			sets = append(sets, fmt.Sprintf(`%q = (random() * 1000000)::int`, col))
		}
	}
	cr.Close()
	require.NoError(t, cr.Err())
	for _, col := range cols {
		_, err := pool.Exec(ctx, fmt.Sprintf(`ALTER TABLE %s ALTER COLUMN %q DROP NOT NULL`, name, col))
		require.NoError(t, err)
	}
	rowKey := c.RowKey
	if strings.HasPrefix(rowKey, "'") {
		// A constant row key (the instance_* singletons): the scratch table
		// holds several rows, so make their ids distinct.
		_, err := pool.Exec(ctx, fmt.Sprintf(`ALTER TABLE %s ADD COLUMN rk_seq bigserial`, name))
		require.NoError(t, err)
		rowKey = "(" + rowKey + " || '#' || rk_seq::text)"
	}
	for range rows {
		_, err := pool.Exec(ctx, fmt.Sprintf(`INSERT INTO %s DEFAULT VALUES`, name))
		require.NoError(t, err)
	}
	_, err = pool.Exec(ctx, fmt.Sprintf(`UPDATE %s SET %s`, name, strings.Join(sets, ", ")))
	require.NoError(t, err)

	out := secretbox.Column{Table: name, Column: c.Column, RowKey: rowKey}
	rr, err := pool.Query(ctx, fmt.Sprintf(`SELECT (%s) FROM %s ORDER BY 1`, rowKey, name))
	require.NoError(t, err)
	defer rr.Close()
	var ids []string
	for rr.Next() {
		var id string
		require.NoError(t, rr.Scan(&id))
		ids = append(ids, id)
	}
	require.NoError(t, rr.Err())
	require.Len(t, ids, rows)
	return out, ids
}

func setValue(t *testing.T, pool *pgxpool.Pool, c secretbox.Column, rowID string, ct []byte) {
	t.Helper()
	tag, err := pool.Exec(context.Background(), fmt.Sprintf(`UPDATE %s SET %q = $1 WHERE (%s) = $2`, c.Table, c.Column, c.RowKey), ct, rowID)
	require.NoError(t, err)
	require.EqualValues(t, 1, tag.RowsAffected())
}

func getValue(t *testing.T, pool *pgxpool.Pool, c secretbox.Column, rowID string) []byte {
	t.Helper()
	var ct []byte
	require.NoError(t, pool.QueryRow(context.Background(), fmt.Sprintf(`SELECT %q FROM %s WHERE (%s) = $1`, c.Column, c.Table, c.RowKey), rowID).Scan(&ct))
	return ct
}

// TestRotateEveryRegistryColumn seeds each sealed column with a legacy v1
// value, a v2 value on the old key and a v2 value on the new key, plus a NULL
// and an empty value, rotates the keys and checks that every value opens with
// the new key alone, in place, and that a second run changes nothing.
func TestRotateEveryRegistryColumn(t *testing.T) {
	pool := testutil.NewPool(t)
	oldKey, newKey := key(t), key(t)
	oldBox, err := secretbox.New(oldKey)
	require.NoError(t, err)
	ring, err := secretbox.NewRing(newKey, oldKey)
	require.NoError(t, err)
	only, err := secretbox.New(newKey)
	require.NoError(t, err)

	var cols []secretbox.Column
	seeded := map[string]map[string]string{} // column -> rowID -> plaintext
	var plains = func(c secretbox.Column) map[string]string { return seeded[c.String()] }

	for i, reg := range secretbox.All() {
		c, ids := clone(t, pool, i, reg, 5)
		cols = append(cols, c)
		seeded[c.String()] = map[string]string{}
		for j, id := range ids[:3] {
			plain := fmt.Sprintf("sekret-canary-%s-%d", c.Column, j)
			seeded[c.String()][id] = plain
			var ct []byte
			switch j {
			case 0:
				ct = sealV1(t, oldKey, []byte(plain))
			case 1:
				ct, err = oldBox.SealString(c, id, plain)
				require.NoError(t, err)
			case 2:
				ct, err = ring.SealString(c, id, plain)
				require.NoError(t, err)
			}
			setValue(t, pool, c, id, ct)
		}
		setValue(t, pool, c, ids[3], []byte{}) // empty: skipped
		// ids[4] stays NULL: skipped
	}

	st, err := rekey.Status(context.Background(), pool, ring, rekey.Options{Columns: cols})
	require.NoError(t, err)
	for _, cr := range st.Columns {
		require.Equal(t, 3, cr.Total, cr.Column.String())
		require.Equal(t, 1, cr.Current, cr.Column.String())
		require.Equal(t, 1, cr.Legacy, cr.Column.String())
	}
	require.Equal(t, 68, st.Pending())

	// A dry run decrypts everything and changes nothing.
	before := map[string][]byte{}
	for _, c := range cols {
		for id := range plains(c) {
			before[c.String()+id] = getValue(t, pool, c, id)
		}
	}
	dry, err := rekey.Run(context.Background(), pool, ring, rekey.Options{DryRun: true, Columns: cols})
	require.NoError(t, err)
	require.Zero(t, dry.Failed())
	require.Equal(t, 68, dry.Rewritten())
	for _, c := range cols {
		for id := range plains(c) {
			require.Equal(t, before[c.String()+id], getValue(t, pool, c, id), "dry run must not write")
		}
	}

	// The real run (batch of 1 to cross batch boundaries).
	rep, err := rekey.Run(context.Background(), pool, ring, rekey.Options{Batch: 1, Columns: cols, Verify: only})
	require.NoError(t, err)
	require.Zero(t, rep.Failed(), "%+v", rep)
	require.Equal(t, 68, rep.Rewritten())
	for _, cr := range rep.Columns {
		require.Equal(t, 3, cr.Verified, cr.Column.String())
	}
	for _, c := range cols {
		for id, plain := range plains(c) {
			ct := getValue(t, pool, c, id)
			require.Equal(t, ring.Header(), ct[:secretbox.HeaderSize], "%s %s on the current key", c, id)
			got, err := only.OpenString(c, id, ct) // the new key ALONE opens it, bound to this row
			require.NoError(t, err, "%s %s", c, id)
			require.Equal(t, plain, got)
		}
		// Empty and NULL values are untouched.
		var empties, nulls int
		require.NoError(t, pool.QueryRow(context.Background(),
			fmt.Sprintf(`SELECT count(*) FILTER (WHERE "%[1]s" = ''::bytea), count(*) FILTER (WHERE "%[1]s" IS NULL) FROM %[2]s`, c.Column, c.Table)).Scan(&empties, &nulls))
		require.Equal(t, [2]int{1, 1}, [2]int{empties, nulls}, c.String())
	}

	// Idempotent: a second run rewrites nothing.
	again, err := rekey.Run(context.Background(), pool, ring, rekey.Options{Columns: cols, Verify: only})
	require.NoError(t, err)
	require.Zero(t, again.Rewritten())
	require.Zero(t, again.Failed())
	require.Zero(t, again.Pending())
}

// TestSwappedCiphertextFails is the AAD check for every registry column: a
// ciphertext moved to another row does not open, and rekey leaves it alone and
// reports it instead of laundering it under a fresh, valid-looking seal.
func TestSwappedCiphertextFails(t *testing.T) {
	pool := testutil.NewPool(t)
	k := key(t)
	box, err := secretbox.New(k)
	require.NoError(t, err)

	var cols []secretbox.Column
	swapped := map[string]string{} // column -> row id that holds the stolen value
	for i, reg := range secretbox.All() {
		c, ids := clone(t, pool, i, reg, 2)
		cols = append(cols, c)
		a, b := ids[0], ids[1]

		ctA, err := box.SealString(c, a, "value-of-a")
		require.NoError(t, err)
		ctB, err := box.SealString(c, b, "value-of-b")
		require.NoError(t, err)
		// seal -> open round trip with the right row.
		got, err := box.OpenString(c, a, ctA)
		require.NoError(t, err)
		require.Equal(t, "value-of-a", got)

		// Someone with database write access copies A's value into B's row.
		setValue(t, pool, c, a, ctA)
		setValue(t, pool, c, b, ctB)
		setValue(t, pool, c, b, getValue(t, pool, c, a))
		stolen := getValue(t, pool, c, b)
		_, err = box.OpenString(c, b, stolen)
		require.ErrorIs(t, err, secretbox.ErrDecrypt, "%s: a ciphertext of another row must not open", c)
		// ... nor under another column of the same table.
		other := secretbox.Column{Table: c.Table, Column: c.Column + "_x", RowKey: c.RowKey}
		_, err = box.OpenString(other, a, ctA)
		require.ErrorIs(t, err, secretbox.ErrDecrypt, "%s: another column", c)
		swapped[c.String()] = b

	}

	newKey := key(t)
	ring, err := secretbox.NewRing(newKey, k)
	require.NoError(t, err)
	rep, err := rekey.Run(context.Background(), pool, ring, rekey.Options{Columns: cols})
	require.NoError(t, err)
	require.Equal(t, 34, rep.Failed())
	require.Equal(t, 34, rep.Rewritten())
	for _, cr := range rep.Columns {
		require.Equal(t, []string{swapped[cr.Column.String()]}, cr.FailedRows, cr.Column.String())
		require.Equal(t, 1, cr.Rewritten, cr.Column.String())
	}
	// The stolen value is byte-for-byte what it was (still sealed with the old key and A's context).
	for _, c := range cols {
		ct := getValue(t, pool, c, swapped[c.String()])
		require.True(t, ring.NeedsRekey(ct), "%s: the swapped value is not on the current key", c)
	}
}

// TestRekeyResumes shows that an interrupted run is finished by the next one,
// which only touches what is left.
func TestRekeyResumes(t *testing.T) {
	pool := testutil.NewPool(t)
	oldKey, newKey := key(t), key(t)
	oldBox, err := secretbox.New(oldKey)
	require.NoError(t, err)
	ring, err := secretbox.NewRing(newKey, oldKey)
	require.NoError(t, err)

	c, ids := clone(t, pool, 0, secretbox.ServiceVariablesValue, 7)
	for _, id := range ids {
		ct, err := oldBox.SealString(c, id, "v-"+id)
		require.NoError(t, err)
		setValue(t, pool, c, id, ct)
	}
	// "The first run got through three rows" before it was killed.
	for _, id := range ids[:3] {
		ct, err := ring.SealString(c, id, "v-"+id)
		require.NoError(t, err)
		setValue(t, pool, c, id, ct)
	}
	rep, err := rekey.Run(context.Background(), pool, ring, rekey.Options{Batch: 2, Columns: []secretbox.Column{c}})
	require.NoError(t, err)
	require.Equal(t, 4, rep.Rewritten())
	require.Zero(t, rep.Failed())
	for _, id := range ids {
		ct := getValue(t, pool, c, id)
		require.False(t, ring.NeedsRekey(ct))
		got, err := ring.OpenString(c, id, ct)
		require.NoError(t, err)
		require.Equal(t, "v-"+id, got)
	}
}

// TestRekeyReportsWrongKey: values on a key that is not in the ring are
// reported, never rewritten, and make the run fail verification.
func TestRekeyReportsWrongKey(t *testing.T) {
	pool := testutil.NewPool(t)
	lost, current := key(t), key(t)
	lostBox, err := secretbox.New(lost)
	require.NoError(t, err)
	box, err := secretbox.New(current)
	require.NoError(t, err)

	c, ids := clone(t, pool, 0, secretbox.UserTOTPSecret, 2)
	ct, err := lostBox.SealString(c, ids[0], "totp")
	require.NoError(t, err)
	setValue(t, pool, c, ids[0], ct)
	ok, err := box.SealString(c, ids[1], "totp2")
	require.NoError(t, err)
	setValue(t, pool, c, ids[1], ok)

	rep, err := rekey.Run(context.Background(), pool, box, rekey.Options{Columns: []secretbox.Column{c}, Verify: box})
	require.NoError(t, err)
	require.Equal(t, 1, rep.Failed())
	require.Equal(t, []string{ids[0]}, rep.Columns[0].FailedRows)
	require.True(t, bytes.Equal(ct, getValue(t, pool, c, ids[0])), "left untouched")
}

// TestOneRunPerSchema: the advisory lock keeps two runs in one schema apart,
// but does not block a run in another schema of the same database (each test
// package, like each installation sharing a database, has its own schema).
func TestOneRunPerSchema(t *testing.T) {
	ctx := context.Background()
	pool, other := testutil.NewPool(t), testutil.NewPool(t)
	ring, err := secretbox.NewRing(key(t))
	require.NoError(t, err)

	held, err := pool.Acquire(ctx)
	require.NoError(t, err)
	defer held.Release()
	var locked bool
	require.NoError(t, held.QueryRow(ctx, `SELECT pg_try_advisory_lock($1, hashtext(current_schema()))`, rekey.LockID).Scan(&locked))
	require.True(t, locked)

	_, err = rekey.Run(ctx, pool, ring, rekey.Options{})
	require.ErrorContains(t, err, "another rekey is already running")
	_, err = rekey.Run(ctx, other, ring, rekey.Options{})
	require.NoError(t, err, "a run in another schema is not blocked")
}
