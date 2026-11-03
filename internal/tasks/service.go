package tasks

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nebulactrl/nebulactrl/internal/agents"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/events"
	"github.com/nebulactrl/nebulactrl/internal/jobs"
	"github.com/nebulactrl/nebulactrl/internal/notifications"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/services"
)

// Redactor masks the secret values of a service in log lines and messages.
// *services.Service implements it.
type Redactor interface {
	RedactorFor(ctx context.Context, serviceID uuid.UUID) (func(string) string, error)
}

// Restarter queues the restart of a service's running container inside tx on
// behalf of a schedule. *services.Service implements it.
type Restarter interface {
	RestartForSchedule(ctx context.Context, tx pgx.Tx, sc services.Scope, actorLabel string) (uuid.UUID, error)
}

// Service implements the task use cases.
type Service struct {
	pool      *pgxpool.Pool
	q         *db.Queries
	bus       *events.Bus
	jm        *jobs.Manager
	box       *secretbox.Box
	disp      agents.Dispatcher
	redactor  Redactor
	ext       *services.ExtSecrets
	restarter Restarter
	notifier  notifications.Emitter
	log       *slog.Logger

	now func() time.Time
}

// Deps are the collaborators of a Service. Pool, Bus, Jobs, Box, Dispatcher and
// Redactor may be nil for OpenAPI generation only.
type Deps struct {
	Pool       *pgxpool.Pool
	Bus        *events.Bus
	Jobs       *jobs.Manager
	Box        *secretbox.Box
	Dispatcher agents.Dispatcher
	Redactor   Redactor
	// Notifier receives task failures (task.failed); may be nil.
	Notifier notifications.Emitter
	Logger   *slog.Logger
	// ExtSecrets fetches `${{vault:…}}` values at command send time (nil: none).
	ExtSecrets *services.ExtSecrets
	// Restarter queues the restart of a service for restart tasks.
	// *services.Service implements it. May be nil for OpenAPI generation only.
	Restarter Restarter
}

// NewService returns a Service.
func NewService(d Deps) *Service {
	s := &Service{
		pool: d.Pool, bus: d.Bus, jm: d.Jobs, box: d.Box, disp: d.Dispatcher, redactor: d.Redactor,
		notifier: d.Notifier, log: d.Logger, now: time.Now, ext: d.ExtSecrets, restarter: d.Restarter,
	}
	if s.log == nil {
		s.log = slog.Default()
	}
	if d.Pool != nil {
		s.q = db.New(d.Pool)
	}
	return s
}

// actorLabel names who triggered an action: the caller's label, or "system".
func actorLabel(ctx context.Context) string {
	if p, ok := auth.PrincipalFrom(ctx); ok && p.Label != "" {
		return p.Label
	}
	return "system"
}

func (s *Service) publishRun(sc services.Scope, runID uuid.UUID) {
	services.Publish(s.bus, EventTaskRunUpdated, sc.ProjectID, sc.EnvironmentID, runID)
}

func (s *Service) publishTasks(sc services.Scope) {
	services.Publish(s.bus, EventTaskUpdated, sc.ProjectID, sc.EnvironmentID, sc.Service.ID)
}

func clip(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > MaxMessage {
		s = s[:MaxMessage] + "…"
	}
	return s
}

// serverState reports whether the server can take commands now.
func serverState(ctx context.Context, q *db.Queries, serverID uuid.UUID) (srv db.Server, online bool, err error) {
	srv, err = q.GetServerByID(ctx, serverID)
	if errors.Is(err, pgx.ErrNoRows) {
		return db.Server{}, false, nil
	}
	if err != nil {
		return db.Server{}, false, fmt.Errorf("load server: %w", err)
	}
	return srv, srv.Status == "online", nil
}
