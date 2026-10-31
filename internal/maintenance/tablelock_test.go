package maintenance_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agents"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/domains"
	"github.com/nebulactrl/nebulactrl/internal/maintenance"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// queueDispatcher inserts the command like the hub does (with the foreign keys
// and their row locks) and is safe for concurrent transactions.
type queueDispatcher struct{}

func (queueDispatcher) Enqueue(ctx context.Context, tx pgx.Tx, c agents.NewCommand) (uuid.UUID, error) {
	row, err := db.New(tx).InsertAgentCommand(ctx, db.InsertAgentCommandParams{
		ID: uuid.New(), ServerID: c.ServerID, DeploymentID: c.DeploymentID, Kind: c.Kind, Payload: c.Payload,
		IdempotencyKey: uuid.NewString(), ExpiresAt: time.Now().Add(time.Hour),
	})
	return row.ID, err
}

func (queueDispatcher) SubscribeLogs(context.Context, uuid.UUID, agentprotocol.LogsSubscribe) (<-chan agentprotocol.LogFrame, func(), error) {
	return nil, nil, io.EOF
}

func isDeadlock(err error) bool {
	var pe *pgconn.PgError
	return errors.As(err, &pe) && pe.Code == "40P01"
}

// numberedTx is the shape of every transaction that sends a whole route table
// (finalizeRoute, the proxy deploy, a service delete): its own writes and row
// locks first, then the numbered table and its command, then commit.
func numberedTx(ctx context.Context, f *fixture, before func(ctx context.Context, tx pgx.Tx) error) error {
	return db.InTx(ctx, f.pool, func(tx pgx.Tx) error {
		if err := before(ctx, tx); err != nil {
			return err
		}
		routes, seq, err := domains.ServerTable(ctx, db.New(tx), f.server.ID, uuid.Nil)
		if err != nil {
			return err
		}
		raw := []byte(fmt.Sprintf(`{"routes":%d,"tableSeq":%d}`, len(routes), seq))
		_, err = queueDispatcher{}.Enqueue(ctx, tx, agents.NewCommand{ServerID: f.server.ID, Kind: agentprotocol.KindSyncRoutes, Payload: raw})
		return err
	})
}

// The route counter's lock lasts to the end of the transaction. The callers
// take it last, after every row they write or lock, so transactions that wait
// for each other do so in one direction. Here the real ones run against each
// other, with row locks held across the numbering: a maintenance toggle (service
// row, audit, table), a route finalisation (service row FOR UPDATE, table), a
// proxy deploy (server row FOR UPDATE, table) and a bare numbering.
func TestNumberingTransactionsNeverDeadlock(t *testing.T) {
	f := newFixture(t)
	a := f.deployedWeb("web-a", "a.example.com")
	b := f.deployedWeb("web-b", "b.example.com")

	box, err := secretbox.New(bytes.Repeat([]byte{7}, 32))
	require.NoError(t, err)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	disp := queueDispatcher{}
	svc := maintenance.NewService(f.pool, f.bus, disp, services.NewService(f.pool, f.bus, box, disp, logger), logger)

	lockService := func(id uuid.UUID) func(context.Context, pgx.Tx) error {
		return func(ctx context.Context, tx pgx.Tx) error {
			_, err := tx.Exec(ctx, "SELECT id FROM services WHERE id = $1 FOR UPDATE", id)
			return err
		}
	}
	lockServer := func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, "SELECT id FROM servers WHERE id = $1 FOR UPDATE", f.server.ID)
		return err
	}

	const rounds = 50
	for i := range rounds {
		start := make(chan struct{})
		var wg sync.WaitGroup
		errs := make(chan error, 8)
		run := func(name string, fn func() error) {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				if err := fn(); err != nil {
					errs <- fmt.Errorf("%s: %w", name, err)
				}
			}()
		}
		run("toggle a", func() error {
			_, err := svc.Set(f.ctx, a.ID, maintenance.SetInput{Enabled: true, Title: fmt.Sprintf("round %d", i)})
			return err
		})
		run("toggle b", func() error {
			_, err := svc.Set(f.ctx, b.ID, maintenance.SetInput{Enabled: i%2 == 0, Title: fmt.Sprintf("round %d", i)})
			return err
		})
		run("finalize a", func() error { return numberedTx(f.ctx, f, lockService(a.ID)) })
		run("finalize b", func() error { return numberedTx(f.ctx, f, lockService(b.ID)) })
		run("proxy deploy", func() error { return numberedTx(f.ctx, f, lockServer) })
		run("bare", func() error { return numberedTx(f.ctx, f, func(context.Context, pgx.Tx) error { return nil }) })
		close(start)
		wg.Wait()
		close(errs)
		for err := range errs {
			require.False(t, isDeadlock(err), "round %d: %v", i, err)
			require.NoError(t, err, "round %d", i)
		}
	}
}

// Why the rule exists: a transaction that locks a service row AFTER numbering
// waits for the counter's lock holders in the opposite direction to one that
// locks the row first, and Postgres aborts one of them. The harness above
// detects exactly this.
func TestRowLockAfterNumberingDeadlocks(t *testing.T) {
	f := newFixture(t)
	a := f.deployedWeb("web-a", "a.example.com")

	rowThenCounter := make(chan struct{}) // closed once the first transaction holds the service row
	counterThenRow := make(chan struct{}) // closed once the second holds the counter
	var wg sync.WaitGroup
	results := make([]error, 2)
	wg.Add(2)
	go func() { // the rule: the row, then the counter
		defer wg.Done()
		results[0] = db.InTx(f.ctx, f.pool, func(tx pgx.Tx) error {
			if _, err := tx.Exec(f.ctx, "SELECT id FROM services WHERE id = $1 FOR UPDATE", a.ID); err != nil {
				return err
			}
			close(rowThenCounter)
			<-counterThenRow
			_, _, err := domains.ServerTable(f.ctx, db.New(tx), f.server.ID, uuid.Nil)
			return err
		})
	}()
	go func() { // against the rule: the counter, then the row
		defer wg.Done()
		<-rowThenCounter
		results[1] = db.InTx(f.ctx, f.pool, func(tx pgx.Tx) error {
			if _, _, err := domains.ServerTable(f.ctx, db.New(tx), f.server.ID, uuid.Nil); err != nil {
				return err
			}
			close(counterThenRow)
			time.Sleep(200 * time.Millisecond) // the first one is now waiting for the counter
			_, err := tx.Exec(f.ctx, "SELECT id FROM services WHERE id = $1 FOR UPDATE", a.ID)
			return err
		})
	}()
	wg.Wait()
	require.True(t, isDeadlock(results[0]) != isDeadlock(results[1]), "exactly one transaction is the deadlock victim: %v / %v", results[0], results[1])
}
