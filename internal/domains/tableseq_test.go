package domains

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/db"
)

// The tables of a server are numbered in the order of the states they read, and
// two tables are never built concurrently: the second builder waits for the
// first one's transaction, so it reads everything the first one committed.
func TestServerTableNumbersIncreaseAndSerializeBuilders(t *testing.T) {
	e := newStatusEnv(t)
	bg := context.Background()

	build := func(server uuid.UUID) uint64 {
		var seq uint64
		require.NoError(t, db.InTx(bg, e.pool, func(tx pgx.Tx) error {
			var err error
			_, seq, err = ServerTable(bg, db.New(tx), server, uuid.Nil)
			return err
		}))
		return seq
	}
	require.Equal(t, uint64(1), build(e.cp.ID))
	require.Equal(t, uint64(2), build(e.cp.ID))
	require.Equal(t, uint64(1), build(e.other.ID), "each server has its own numbers")

	// A builder that has not committed holds the next one back.
	txA, err := e.pool.Begin(bg)
	require.NoError(t, err)
	_, a, err := ServerTable(bg, db.New(txA), e.cp.ID, uuid.Nil)
	require.NoError(t, err)
	require.Equal(t, uint64(3), a)

	done := make(chan uint64, 1)
	go func() { done <- build(e.cp.ID) }()
	select {
	case n := <-done:
		require.FailNow(t, "a second builder did not wait for the first", "got %d", n)
	case <-time.After(300 * time.Millisecond):
	}
	require.NoError(t, txA.Commit(bg))
	select {
	case n := <-done:
		require.Equal(t, uint64(4), n)
	case <-time.After(5 * time.Second):
		require.FailNow(t, "the second builder never finished")
	}
}
