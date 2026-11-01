package upgrade

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/backups"
	"github.com/nebulactrl/nebulactrl/internal/testutil"
)

func TestReadStateAndCommit(t *testing.T) {
	pool := testutil.NewPool(t)

	st, err := ReadState(bg, pool)
	require.NoError(t, err)
	require.True(t, st.Existing, "a migrated database is an existing install")
	require.Empty(t, st.LastVersion, "the version was never tracked")

	// Commit stores the version and the backup outcome...
	res := backups.PreUpgradeResult{Status: backups.PreUpgradeSkipped, Message: "no destination", From: "", To: "1.5.0"}
	require.NoError(t, Commit(bg, pool, Decision{Result: &res}, "1.5.0"))
	st, err = ReadState(bg, pool)
	require.NoError(t, err)
	require.Equal(t, "1.5.0", st.LastVersion)
	require.Equal(t, "1.5.0", st.BackupTo)
	require.Equal(t, backups.PreUpgradeSkipped, st.BackupStatus)
	require.False(t, st.BackupAt.IsZero())

	// ...so the next start of the same version is not an upgrade.
	h := newHarness("1.5.0", st, ok)
	dec, err := Preflight(bg, h.d)
	require.NoError(t, err)
	require.Empty(t, h.calls)
	require.Nil(t, dec.Result)

	// Without a result only the version moves.
	require.NoError(t, Commit(bg, pool, Decision{}, "1.6.0"))
	st, err = ReadState(bg, pool)
	require.NoError(t, err)
	require.Equal(t, "1.6.0", st.LastVersion)
	require.Equal(t, "1.5.0", st.BackupTo)
}

func TestReadStateOnAnEmptyDatabase(t *testing.T) {
	pool := testutil.NewPool(t)
	_, err := pool.Exec(bg, `DROP TABLE instance_upgrade; DROP TABLE goose_db_version CASCADE`)
	require.NoError(t, err)
	st, err := ReadState(bg, pool)
	require.NoError(t, err)
	require.False(t, st.Existing)
	require.Empty(t, st.LastVersion)

	// A database from before the version was tracked: schema, but no instance_upgrade.
}
