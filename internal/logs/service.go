// Package logs is the control-plane side of log history and log drains.
//
// Runtime logs are stored by the agent that runs the container (see
// internal/agent/logstore); this package never holds a runtime line. It proxies
// history queries to the agent, keeps the per-server retention settings and the
// team's log drains (destinations the agents ship to), and tells every agent
// what to do through Reconcile.Logs (limits, drains, credentials as send-time
// secrets). Agents report the log store's usage and the health of every drain
// back, and the settings pages show them.
package logs

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/events"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Live-update event types.
const (
	EventDrainUpdated    = "log_drain.updated"
	EventSettingsUpdated = "server.log_settings.updated"
)

// Audit actions.
const (
	ActionDrainCreate    = "log_drain.create"
	ActionDrainUpdate    = "log_drain.update"
	ActionDrainDelete    = "log_drain.delete"
	ActionDrainTest      = "log_drain.test"
	ActionSettingsUpdate = "server.log_settings.update"
)

// Requester asks an agent a question and waits for the answer
// (*agents.Hub implements it).
type Requester interface {
	Request(ctx context.Context, serverID uuid.UUID, kind string, payload any, secrets map[string]string, timeout time.Duration) (agentprotocol.ResultFrame, error)
	Online(serverID uuid.UUID) bool
	// Resync sends the server's desired state (including Reconcile.Logs) to its agent.
	Resync(ctx context.Context, serverID uuid.UUID) error
}

// Redactor masks secret values in a service's log lines (*services.Service implements it).
type Redactor interface {
	RedactorFor(ctx context.Context, serviceID uuid.UUID) (func(string) string, error)
}

// SecretValueSource lists the secret values tied to a service (variables, git
// token, deploy key, registry credentials); *services.Service implements it.
type SecretValueSource interface {
	SecretValues(ctx context.Context, sc services.Scope) ([]string, error)
}

// Deps are the collaborators of a Service. Pool and Box may be nil for OpenAPI
// generation only.
type Deps struct {
	Pool     *pgxpool.Pool
	Bus      *events.Bus
	Box      *secretbox.Box
	Agents   Requester
	Redactor Redactor
	// Secrets supplies the values agents mask in the lines their drains ship.
	Secrets SecretValueSource
	Logger  *slog.Logger
	// Central reads the team's lines from the built-in log store; nil leaves
	// the central log view off (same as a team without the built-in drain on).
	Central CentralLogs
}

// Service implements history queries, drains and retention settings.
type Service struct {
	pool *pgxpool.Pool
	q    *db.Queries
	bus  *events.Bus
	box  *secretbox.Box
	hub  Requester
	red  Redactor
	sec  SecretValueSource
	log  *slog.Logger
	// central is nil until the built-in log store is wired.
	central CentralLogs
	now     func() time.Time

	// QueryTimeout bounds one history request to an agent.
	QueryTimeout time.Duration
}

// NewService returns a Service.
func NewService(d Deps) *Service {
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	s := &Service{
		pool: d.Pool, bus: d.Bus, box: d.Box, hub: d.Agents, red: d.Redactor, sec: d.Secrets, central: d.Central,
		log: d.Logger.With("component", "logs"), now: time.Now, QueryTimeout: 25 * time.Second,
	}
	if d.Pool != nil {
		s.q = db.New(d.Pool)
	}
	return s
}

// publish announces a change of resource to team ws. The team may be nil when
// resource is a server id: the feed resolves its team.
func (s *Service) publish(typ string, ws, resource uuid.UUID) {
	if s.bus == nil {
		return
	}
	ev := events.Event{Type: typ, ResourceID: resource.String()}
	if ws != uuid.Nil {
		ev.WorkspaceID = ws.String()
	}
	s.bus.Publish(ev)
}

// ---- retention settings -------------------------------------------------------------

// settingsRow returns a server's retention (defaults when it has no row).
func (s *Service) settingsRow(ctx context.Context, serverID uuid.UUID) (capture bool, ret agentprotocol.LogRetention, row *db.ServerLogSetting, err error) {
	capture, ret = true, agentprotocol.LogRetention{}.WithDefaults()
	r, err := s.q.GetServerLogSettings(ctx, serverID)
	switch {
	case err == nil:
		return r.Capture, agentprotocol.LogRetention{
			Days: int(r.RetentionDays), MaxServiceBytes: r.MaxServiceBytes, MaxServerBytes: r.MaxServerBytes,
		}, &r, nil
	case isNoRows(err):
		return capture, ret, nil, nil
	}
	return capture, ret, nil, err
}

// LogsConfigFor builds the log configuration of one server for its agent:
// retention, the services it hosts and every drain of the team, with the
// credentials of the enabled ones as send-time secrets. It implements
// reconciler.LogsSource; never persist the secrets.
func (s *Service) LogsConfigFor(ctx context.Context, serverID uuid.UUID) (*agentprotocol.LogsConfig, map[string]string, error) {
	srv, err := s.q.LoadServerRow(ctx, db.LoadServerRowParams{ID: serverID})
	if err != nil {
		return nil, nil, fmt.Errorf("load server: %w", err)
	}
	capture, ret, _, err := s.settingsRow(ctx, serverID)
	if err != nil {
		return nil, nil, fmt.Errorf("log settings: %w", err)
	}
	cfg := &agentprotocol.LogsConfig{Capture: capture, Retention: ret, ServerName: srv.Name, Now: time.Now()}
	rows, err := s.q.LogServicesOnServer(ctx, &serverID)
	if err != nil {
		return nil, nil, fmt.Errorf("list services: %w", err)
	}
	for _, r := range rows {
		cfg.Services = append(cfg.Services, agentprotocol.LogServiceMeta{
			ServiceID: r.ServiceID.String(), Name: r.ServiceName, Slug: r.ServiceSlug,
			ProjectID: r.ProjectID.String(), Project: r.ProjectName,
			EnvironmentID: r.EnvironmentID.String(), Environment: r.EnvironmentName,
		})
	}
	drains, err := s.q.ListLogDrains(ctx, srv.WorkspaceID)
	if err != nil {
		return nil, nil, fmt.Errorf("list drains: %w", err)
	}
	var secrets map[string]string
	if anyEnabled(drains) {
		// Drains ship to third parties: the agent masks each service's secrets.
		if secrets, err = s.redactionValues(ctx, rows); err != nil {
			return nil, nil, err
		}
	}
	for _, d := range drains {
		ad, err := s.agentDrain(d)
		if err != nil {
			s.log.WarnContext(ctx, "log drain skipped: unreadable configuration", "drain_id", d.ID, "err", err)
			continue
		}
		cfg.Drains = append(cfg.Drains, ad)
		if !d.Enabled {
			continue
		}
		sec, err := s.openSecrets(d.ID, d.SecretsEnc)
		if err != nil {
			s.log.WarnContext(ctx, "log drain skipped: unreadable credentials", "drain_id", d.ID, "err", err)
			cfg.Drains = cfg.Drains[:len(cfg.Drains)-1]
			continue
		}
		if m := sec.send(d.ID.String()); len(m) > 0 {
			if secrets == nil {
				secrets = map[string]string{}
			}
			for k, v := range m {
				secrets[k] = v
			}
		}
	}
	return cfg, secrets, nil
}

func anyEnabled(drains []db.LogDrain) bool {
	for _, d := range drains {
		if d.Enabled {
			return true
		}
	}
	return false
}

// redactionValues returns, per service on the server, the secret values its
// lines must not carry out of the host.
func (s *Service) redactionValues(ctx context.Context, rows []db.LogServicesOnServerRow) (map[string]string, error) {
	if s.sec == nil {
		return nil, nil
	}
	out := map[string]string{}
	for _, r := range rows {
		sc, err := services.LoadScopeInternal(ctx, s.q, r.ServiceID)
		if err != nil {
			continue // gone meanwhile
		}
		values, err := s.sec.SecretValues(ctx, sc)
		if err != nil {
			return nil, fmt.Errorf("secret values of %s: %w", r.ServiceSlug, err)
		}
		var keep []string
		for _, v := range values {
			if len(v) >= services.MinRedactLen {
				keep = append(keep, v)
			}
		}
		if len(keep) == 0 {
			continue
		}
		raw, _ := json.Marshal(keep)
		out[agentprotocol.LogRedactSecretKey(r.ServiceID.String())] = string(raw)
	}
	return out, nil
}

// resyncAll pushes the log configuration to every connected server of the team.
func (s *Service) resyncAll(ctx context.Context, ws uuid.UUID) {
	if s.hub == nil {
		return
	}
	servers, err := s.q.ListLogCapableServers(ctx, ws)
	if err != nil {
		s.log.WarnContext(ctx, "resync log configuration", "err", err)
		return
	}
	for _, sv := range servers {
		if !s.hub.Online(sv.ID) {
			continue // it receives the configuration on its next hello
		}
		if err := s.hub.Resync(context.WithoutCancel(ctx), sv.ID); err != nil {
			s.log.WarnContext(ctx, "resync log configuration", "server_id", sv.ID, "err", err)
		}
	}
}

// ---- status reports -----------------------------------------------------------------

// LogServerUsage is the usage snapshot an agent reported.
type LogServerUsage struct {
	TotalBytes int64                           `json:"totalBytes"`
	Services   []agentprotocol.LogServiceUsage `json:"services"`
	ReportedAt time.Time                       `json:"reportedAt"`
}

// HandleLogStatus stores an agent's log status (agents.LogStatusSink).
func (s *Service) HandleLogStatus(ctx context.Context, serverID uuid.UUID, st agentprotocol.LogStatus) error {
	raw, err := json.Marshal(struct {
		TotalBytes int64                           `json:"totalBytes"`
		Services   []agentprotocol.LogServiceUsage `json:"services"`
	}{st.TotalBytes, st.Services})
	if err != nil {
		return err
	}
	if err := s.q.SetServerLogUsage(ctx, db.SetServerLogUsageParams{ServerID: serverID, Usage: raw}); err != nil {
		return fmt.Errorf("store usage: %w", err)
	}
	ids := make([]uuid.UUID, 0, len(st.Drains))
	for _, d := range st.Drains {
		id, err := uuid.Parse(d.ID)
		if err != nil {
			continue
		}
		ids = append(ids, id)
		var errAt *time.Time = d.LastErrorAt
		if err := s.q.UpsertLogDrainStatus(ctx, db.UpsertLogDrainStatusParams{
			DrainID: id, ServerID: serverID, State: d.State, LastSuccessAt: d.LastSuccessAt, LastError: truncate(d.LastError, 500),
			LastErrorAt: errAt, SentLines: d.SentLines, DroppedLines: d.DroppedLines, BacklogBytes: d.BacklogBytes,
		}); err != nil {
			return fmt.Errorf("store drain status: %w", err)
		}
	}
	if err := s.q.DeleteLogDrainStatusExcept(ctx, db.DeleteLogDrainStatusExceptParams{ServerID: serverID, Column2: ids}); err != nil {
		return fmt.Errorf("prune drain status: %w", err)
	}
	s.publish(EventDrainUpdated, uuid.Nil, serverID)
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
