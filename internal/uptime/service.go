package uptime

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/events"
	"github.com/nebulactrl/nebulactrl/internal/notifications"
)

// Live-update event and audit action of this package.
const (
	// EventStateChanged is published whenever a service's uptime state
	// changes. Like every bus event it is an invalidation hint: the project,
	// environment and service ID tell the dashboard what to refetch.
	EventStateChanged = "uptime.state.changed"
	// ActionMonitorUpdate is the audit action of a monitor settings change.
	ActionMonitorUpdate = "uptime_monitor.update"
)

const notifyTimeout = 20 * time.Second

// Resyncer pushes a server's desired state (including Reconcile.Uptime) to its
// agent. *agents.Hub implements it.
type Resyncer interface {
	Resync(ctx context.Context, serverID uuid.UUID) error
}

// Deps are the collaborators of a Service. Pool may be nil for OpenAPI
// generation only; Bus, Notifier and Resync may be nil.
type Deps struct {
	Pool     *pgxpool.Pool
	Bus      *events.Bus
	Notifier notifications.Emitter
	Resync   Resyncer
	Logger   *slog.Logger
	// Interval is the agents' probe period (NEBULACTRL_UPTIME_INTERVAL); zero
	// means agentprotocol.UptimeInterval.
	Interval time.Duration
}

// Service ingests probe results, keeps the state of every service, runs the
// rollups and serves the uptime API.
type Service struct {
	pool     *pgxpool.Pool
	q        *db.Queries
	bus      *events.Bus
	notifier notifications.Emitter
	resync   Resyncer
	log      *slog.Logger
	now      func() time.Time
	// staleAfter is how long a service may go without a check before it is
	// unknown: three intervals and a margin.
	staleAfter time.Duration
}

// NewService returns a Service.
func NewService(d Deps) *Service {
	log := d.Logger
	if log == nil {
		log = slog.Default()
	}
	stale := StaleAfter
	if d.Interval > 0 {
		stale = 3*d.Interval + 30*time.Second
	}
	return &Service{
		pool: d.Pool, q: db.New(d.Pool), bus: d.Bus, notifier: d.Notifier, resync: d.Resync,
		log: log.With("component", "uptime"), now: time.Now, staleAfter: stale,
	}
}

// settingsOf converts a monitor row into Step settings.
func settingsOf(m db.UptimeMonitor) Settings {
	return Settings{
		Enabled: m.Enabled, Alerts: m.Alerts,
		FailureThreshold: int(m.FailureThreshold), RecoveryThreshold: int(m.RecoveryThreshold),
	}
}

// stateOf converts a state row into a State; the incident flags are the caller's.
func stateOf(r db.UptimeState) State {
	st := State{
		State: r.State, Since: r.Since,
		Failures: int(r.ConsecutiveFailures), Successes: int(r.ConsecutiveSuccesses),
		LastCheckAt: r.LastCheckAt, LastOKAt: r.LastOkAt, LastError: r.LastError,
	}
	if r.LastLatencyMs != nil {
		l := int(*r.LastLatencyMs)
		st.LastLatencyMs = &l
	}
	return st
}

// saveParams converts a State into the update of its row.
func saveParams(id uuid.UUID, st State) db.SaveUptimeStateParams {
	p := db.SaveUptimeStateParams{
		ServiceID: id, State: st.State, Since: st.Since,
		ConsecutiveFailures: int32(st.Failures), ConsecutiveSuccesses: int32(st.Successes),
		LastCheckAt: st.LastCheckAt, LastOkAt: st.LastOKAt, LastError: st.LastError,
	}
	if st.LastLatencyMs != nil {
		l := int32(*st.LastLatencyMs)
		p.LastLatencyMs = &l
	}
	return p
}

// publish tells the dashboard a service's state changed.
func (s *Service) publish(projectID, environmentID, serviceID uuid.UUID) {
	if s.bus == nil {
		return
	}
	s.bus.Publish(events.Event{
		Type: EventStateChanged, ProjectID: projectID.String(), EnvironmentID: environmentID.String(), ResourceID: serviceID.String(),
	})
}
