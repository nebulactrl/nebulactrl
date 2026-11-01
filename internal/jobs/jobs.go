// Package jobs owns the River client. Domains register workers and periodic
// jobs before Start; afterwards they insert jobs through Insert / InsertTx.
package jobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"

	"github.com/nebulactrl/nebulactrl/internal/db"
)

// Manager wraps the River client lifecycle.
type Manager struct {
	pool   *pgxpool.Pool
	logger *slog.Logger

	mu       sync.Mutex
	workers  *river.Workers
	periodic []*river.PeriodicJob
	client   *river.Client[pgx.Tx]
}

// New returns a Manager with the built-in jobs registered. Nothing touches the
// database until Migrate, Start or an Insert call.
func New(pool *pgxpool.Pool, logger *slog.Logger) *Manager {
	if logger == nil {
		logger = slog.Default()
	}
	m := &Manager{pool: pool, logger: logger, workers: river.NewWorkers()}
	m.registerBuiltins()
	return m
}

// AddWorker registers workers (call river.AddWorker inside fn). Must be called
// before Start.
func (m *Manager) AddWorker(fn func(*river.Workers)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.client != nil {
		panic("jobs: AddWorker called after the River client was built")
	}
	fn(m.workers)
}

// AddPeriodic registers a periodic job. Must be called before Start.
func (m *Manager) AddPeriodic(j *river.PeriodicJob) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.client != nil {
		panic("jobs: AddPeriodic called after the River client was built")
	}
	m.periodic = append(m.periodic, j)
}

// Migrate applies River's own schema migrations.
func (m *Manager) Migrate(ctx context.Context) error {
	mig, err := rivermigrate.New(riverpgxv5.New(m.pool), &rivermigrate.Config{Logger: m.logger})
	if err != nil {
		return fmt.Errorf("river migrator: %w", err)
	}
	if _, err := mig.Migrate(ctx, rivermigrate.DirectionUp, nil); err != nil {
		return fmt.Errorf("river migrate: %w", err)
	}
	return nil
}

// build creates the client on first use, freezing the worker set.
func (m *Manager) build() (*river.Client[pgx.Tx], error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.client != nil {
		return m.client, nil
	}
	if m.pool == nil {
		return nil, errors.New("jobs: no database pool")
	}
	c, err := river.NewClient(riverpgxv5.New(m.pool), &river.Config{
		Logger:       m.logger,
		Queues:       map[string]river.QueueConfig{river.QueueDefault: {MaxWorkers: 20}},
		Workers:      m.workers,
		PeriodicJobs: m.periodic,
	})
	if err != nil {
		return nil, fmt.Errorf("river client: %w", err)
	}
	m.client = c
	return c, nil
}

// Start begins processing jobs. River's schema must already be migrated.
func (m *Manager) Start(ctx context.Context) error {
	c, err := m.build()
	if err != nil {
		return err
	}
	return c.Start(ctx)
}

// Stop waits for running jobs to finish (bounded by ctx).
func (m *Manager) Stop(ctx context.Context) error {
	m.mu.Lock()
	c := m.client
	m.mu.Unlock()
	if c == nil {
		return nil
	}
	if err := c.Stop(ctx); err != nil {
		return err
	}
	return nil
}

// Insert enqueues a job outside a transaction.
func (m *Manager) Insert(ctx context.Context, args river.JobArgs, opts *river.InsertOpts) error {
	c, err := m.build()
	if err != nil {
		return err
	}
	_, err = c.Insert(ctx, args, opts)
	return err
}

// InsertTx enqueues a job inside tx, so it is only visible when tx commits.
func (m *Manager) InsertTx(ctx context.Context, tx pgx.Tx, args river.JobArgs, opts *river.InsertOpts) error {
	c, err := m.build()
	if err != nil {
		return err
	}
	_, err = c.InsertTx(ctx, tx, args, opts)
	return err
}

// Pool returns the database pool the manager was built with (nil when none).
// Periodic workers registered by domains use it at Work time.
func (m *Manager) Pool() *pgxpool.Pool { return m.pool }

// Logger returns the manager's logger.
func (m *Manager) Logger() *slog.Logger { return m.logger }

// Client exposes the River client for advanced use (e.g. cancelling jobs).
func (m *Manager) Client() (*river.Client[pgx.Tx], error) { return m.build() }

func newQueries(pool *pgxpool.Pool) *db.Queries { return db.New(pool) }
