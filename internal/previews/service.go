package previews

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/config"
	"github.com/nebulactrl/nebulactrl/internal/databases"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/events"
	"github.com/nebulactrl/nebulactrl/internal/jobs"
	"github.com/nebulactrl/nebulactrl/internal/notifications"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/services"
)

// Deps are the collaborators of a Service. Pool, Jobs and Box may be nil for
// OpenAPI generation only.
type Deps struct {
	Pool     *pgxpool.Pool
	Bus      *events.Bus
	Jobs     *jobs.Manager
	Box      *secretbox.Box
	Config   *config.Config
	Services *services.Service
	// Databases creates the fresh, empty databases of a preview.
	Databases *databases.Service
	Git       Git
	Deployer  Deployer
	Seeder    Seeder
	Notifier  notifications.Emitter
	Logger    *slog.Logger
	// Now returns the current time (tests inject a clock).
	Now func() time.Time
}

// Service implements preview environments.
type Service struct {
	Deps
	q *db.Queries
}

// NewService returns a Service.
func NewService(d Deps) *Service {
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	return &Service{Deps: d, q: db.New(d.Pool)}
}

// systemCtx returns ctx carrying the control plane's own principal for the
// project: the services layer authorizes it like a token holding exactly the
// scopes previews need, and audit rows say "system".
func systemCtx(ctx context.Context, workspaceID, projectID uuid.UUID) context.Context {
	return auth.WithPrincipal(ctx, auth.System(workspaceID, projectID, "Previews",
		policy.ScopeProjectsRead, policy.ScopeProjectsWrite, policy.ScopeDeploymentsWrite))
}

func (s *Service) sysCtx(ctx context.Context, pv db.Preview) context.Context {
	return systemCtx(ctx, pv.WorkspaceID, pv.ProjectID)
}

// lock takes a transaction-scoped advisory lock on key, serializing work on
// one pull request (or one preview's deploy wave) across workers.
func (s *Service) lock(ctx context.Context, tx pgx.Tx, key string) error {
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1, 0))", key); err != nil {
		return fmt.Errorf("lock %s: %w", key, err)
	}
	return nil
}

func (s *Service) publish(pv db.Preview) {
	if s.Bus == nil {
		return
	}
	s.Bus.Publish(events.Event{
		Type: EventPreviewUpdated, ProjectID: pv.ProjectID.String(), EnvironmentID: pv.EnvironmentID.String(), ResourceID: pv.ID.String(),
		WorkspaceID: pv.WorkspaceID.String(),
	})
}

// EventPreviewUpdated is published whenever a preview changes (the web app
// refreshes its previews list and environment switcher).
const EventPreviewUpdated = "preview.updated"

func (s *Service) emit(ctx context.Context, ev notifications.Event) {
	notifications.Emit(ctx, s.Notifier, ev)
}

// deployState is the progress of the current deploy wave of a preview,
// persisted in previews.deploy_state so any worker can continue it.
type deployState struct {
	// Seq numbers the waves; a worker finishing an older wave never overwrites
	// the status of a newer one.
	Seq     int       `json:"seq"`
	SHA     string    `json:"sha"`
	Mode    string    `json:"mode"` // create | push | resume
	Started time.Time `json:"started"`
	// Targets are the services this wave deploys.
	Targets []uuid.UUID `json:"targets"`
	// Restart are stopped services started again when a paused preview resumes.
	Restart []uuid.UUID `json:"restart,omitempty"`
	// Restarted is set once Restart has been issued.
	Restarted bool `json:"restarted,omitempty"`
	// Deployments maps service id → deployment id of this wave.
	Deployments map[string]uuid.UUID `json:"deployments,omitempty"`
	// Seeds maps seed index → task run id.
	Seeds map[string]uuid.UUID `json:"seeds,omitempty"`
	// Warnings are shown in the PR comment (a service without a domain, ...).
	Warnings []string `json:"warnings,omitempty"`
	// Done is set when the wave reached ready or failed.
	Done bool `json:"done,omitempty"`
}

func decodeState(raw json.RawMessage) deployState {
	var st deployState
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &st)
	}
	return st
}

func encodeState(st deployState) json.RawMessage {
	b, _ := json.Marshal(st)
	return b
}

func envName(number int, title string) string {
	t := []rune(title)
	if len(t) > 60 {
		title = string(t[:60]) + "…"
	}
	if title == "" {
		return fmt.Sprintf("PR #%d", number)
	}
	return fmt.Sprintf("PR #%d · %s", number, title)
}
