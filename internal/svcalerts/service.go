// Package svcalerts tells people when a running service goes wrong: it
// crashed, its health check fails, or it restarts in a loop (service.crashed,
// service.unhealthy, service.restart_loop), and when it is well again
// (service.recovered).
//
// It reads what the control plane already ingests: every observed report an
// agent sends (state, health and Docker's restart counter per replica) passes
// through Service.Observe, which feeds one Tracker per service (tracker.go).
// The Tracker is a pure state machine that debounces, announces once per
// incident, escalates but never repeats, closes an incident only after the
// service has been healthy for a while, and rate-limits a flapping service.
// Open and closed incidents are kept in service_incidents, written to the audit
// log, shown in the service's activity and announced through the notification
// channels, whose own event selection decides who hears what.
package svcalerts

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/events"
	"github.com/nebulactrl/nebulactrl/internal/notifications"
)

// Audit actions and the live-update event type.
const (
	ActionIncidentOpened    = "service.incident_opened"
	ActionIncidentEscalated = "service.incident_escalated"
	ActionIncidentResolved  = "service.incident_resolved"
	EventIncidentUpdated    = "service.incident.updated"
)

const (
	settingsTTL   = time.Minute
	notifyTimeout = 20 * time.Second
	maxLogLine    = 200
)

// LogTailer reads a service's recent runtime output. *services.Service implements it.
type LogTailer interface {
	TailRuntimeLogsInternal(ctx context.Context, id uuid.UUID, n int) ([]string, error)
}

// Deps are the collaborators of a Service. Pool may be nil for OpenAPI
// generation only; Notifier and Logs may be nil.
type Deps struct {
	Pool     *pgxpool.Pool
	Bus      *events.Bus
	Notifier notifications.Emitter
	Logs     LogTailer
	Logger   *slog.Logger
}

// Input is one observed report of one service, with what the detector needs to
// know about the service.
type Input struct {
	WorkspaceID   uuid.UUID
	ProjectID     uuid.UUID
	EnvironmentID uuid.UUID
	ServiceID     uuid.UUID
	ServiceName   string
	Observation
}

type entry struct {
	mu         sync.Mutex
	t          *Tracker
	incidentID uuid.UUID
}

type cachedSettings struct {
	s       Settings
	expires time.Time
}

// Service runs the detectors.
type Service struct {
	pool     *pgxpool.Pool
	bus      *events.Bus
	notifier notifications.Emitter
	logs     LogTailer
	log      *slog.Logger
	now      func() time.Time

	mu       sync.Mutex
	trackers map[uuid.UUID]*entry
	settings map[uuid.UUID]cachedSettings
	pending  sync.WaitGroup // message goroutines in flight (tests wait on it)
}

// NewService returns a Service.
func NewService(d Deps) *Service {
	s := &Service{
		pool: d.Pool, bus: d.Bus, notifier: d.Notifier, logs: d.Logs, log: d.Logger, now: time.Now,
		trackers: map[uuid.UUID]*entry{}, settings: map[uuid.UUID]cachedSettings{},
	}
	if s.log == nil {
		s.log = slog.Default()
	}
	return s
}

// Wait blocks until every message started so far was handed to the notifier.
func (s *Service) Wait() { s.pending.Wait() }

func (s *Service) settingsFor(ctx context.Context, ws uuid.UUID) Settings {
	s.mu.Lock()
	c, ok := s.settings[ws]
	s.mu.Unlock()
	if ok && s.now().Before(c.expires) {
		return c.s
	}
	cur, err := LoadSettings(ctx, s.pool, ws)
	if err != nil {
		s.log.WarnContext(ctx, "load alert settings", "err", err)
		if ok {
			return c.s
		}
		return DefaultSettings()
	}
	s.mu.Lock()
	s.settings[ws] = cachedSettings{s: cur, expires: s.now().Add(settingsTTL)}
	s.mu.Unlock()
	return cur
}

func (s *Service) forgetSettings(ws uuid.UUID) {
	s.mu.Lock()
	delete(s.settings, ws)
	s.mu.Unlock()
}

// entryFor returns the service's tracker, resumed from its open incident.
func (s *Service) entryFor(ctx context.Context, id uuid.UUID) (*entry, error) {
	s.mu.Lock()
	if e, ok := s.trackers[id]; ok {
		s.mu.Unlock()
		return e, nil
	}
	s.mu.Unlock()
	var (
		inc      Incident
		incID    uuid.UUID
		kind     string
		notified *time.Time
	)
	err := s.pool.QueryRow(ctx, `SELECT id, kind, flapping, detail, restarts, opened_at, notified_at
		FROM service_incidents WHERE service_id = $1 AND resolved_at IS NULL`, id).
		Scan(&incID, &kind, &inc.Flapping, &inc.Detail, &inc.Restarts, &inc.OpenedAt, &notified)
	e := &entry{}
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		e.t = NewTracker(nil)
	case err != nil:
		return nil, fmt.Errorf("load open incident: %w", err)
	default:
		inc.Kind, inc.Notified = Kind(kind), notified != nil
		e.t, e.incidentID = NewTracker(&inc), incID
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if cur, ok := s.trackers[id]; ok { // lost a race: use the winner
		return cur, nil
	}
	s.trackers[id] = e
	return e, nil
}

// Observe feeds one observed report. It never fails the caller: problems are
// logged and the report is dropped.
func (s *Service) Observe(ctx context.Context, in Input) {
	if s == nil || s.pool == nil {
		return
	}
	e, err := s.entryFor(ctx, in.ServiceID)
	if err != nil {
		s.log.WarnContext(ctx, "service alerts", "service_id", in.ServiceID.String(), "err", err)
		return
	}
	th := s.settingsFor(ctx, in.WorkspaceID)
	if in.At.IsZero() {
		in.At = s.now()
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, a := range e.t.Step(in.Observation, th) {
		s.apply(ctx, e, in, a, th)
	}
}

// apply records one action and announces it.
func (s *Service) apply(ctx context.Context, e *entry, in Input, a Action, th Settings) {
	now := in.At
	err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		var action string
		switch a.Type {
		case ActOpen:
			action = ActionIncidentOpened
			var notified *time.Time
			if a.Notify {
				notified = &now
			}
			var dep *uuid.UUID
			if id, err := uuid.Parse(in.DeploymentID); err == nil {
				dep = &id
			}
			if err := tx.QueryRow(ctx, `INSERT INTO service_incidents
				(workspace_id, service_id, kind, flapping, detail, restarts, deployment_id, opened_at, notified_at)
				VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9) RETURNING id`,
				in.WorkspaceID, in.ServiceID, string(a.Kind), a.Flapping, a.Detail, a.Restarts, dep, now, notified).Scan(&e.incidentID); err != nil {
				return fmt.Errorf("insert incident: %w", err)
			}
		case ActEscalate:
			action = ActionIncidentEscalated
			if _, err := tx.Exec(ctx, `UPDATE service_incidents SET kind = $2, detail = $3, restarts = $4,
				notified_at = CASE WHEN $5::boolean THEN $6::timestamptz ELSE notified_at END WHERE id = $1`,
				e.incidentID, string(a.Kind), a.Detail, a.Restarts, a.Notify, now); err != nil {
				return fmt.Errorf("escalate incident: %w", err)
			}
		case ActResolve:
			action = ActionIncidentResolved
			if _, err := tx.Exec(ctx, `UPDATE service_incidents SET resolved_at = $2::timestamptz, resolution = $3,
				recovery_notified_at = CASE WHEN $4::boolean THEN $2::timestamptz ELSE NULL END WHERE id = $1`,
				e.incidentID, now, a.Resolution, a.Notify); err != nil {
				return fmt.Errorf("resolve incident: %w", err)
			}
		default:
			return nil
		}
		meta := map[string]any{"service": in.ServiceName, "kind": string(a.Kind), "incident": e.incidentID.String()}
		if a.Detail != "" {
			meta["detail"] = a.Detail
		}
		if a.Flapping {
			meta["flapping"] = true
		}
		if a.Resolution != "" {
			meta["resolution"] = a.Resolution
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: action, ResourceType: "service", ResourceID: in.ServiceID.String(),
			ProjectID: &in.ProjectID, WorkspaceID: in.WorkspaceID, Metadata: meta,
		})
	})
	incidentID := e.incidentID
	if err != nil {
		s.log.ErrorContext(ctx, "record service incident", "service_id", in.ServiceID.String(), "action", string(a.Type), "err", err)
	} else if s.bus != nil {
		s.bus.Publish(events.Event{
			Type: EventIncidentUpdated, ProjectID: in.ProjectID.String(), EnvironmentID: in.EnvironmentID.String(), ResourceID: in.ServiceID.String(),
		})
	}
	if a.Type == ActResolve {
		e.incidentID = uuid.Nil
	}
	if a.Notify && s.notifier != nil {
		s.pending.Add(1)
		go func() {
			defer s.pending.Done()
			s.announce(context.WithoutCancel(ctx), in, a, incidentID, th)
		}()
	}
}

func alertType(k Kind) (typ, severity string) {
	switch k {
	case KindCrashed:
		return notifications.TypeServiceCrashed, notifications.SeverityError
	case KindRestartLoop:
		return notifications.TypeServiceRestartLoop, notifications.SeverityError
	}
	return notifications.TypeServiceUnhealthy, notifications.SeverityWarning
}

// announce builds and sends the message of an action.
func (s *Service) announce(ctx context.Context, in Input, a Action, incidentID uuid.UUID, th Settings) {
	ctx, cancel := context.WithTimeout(ctx, notifyTimeout)
	defer cancel()
	ev := notifications.Event{
		ProjectID: &in.ProjectID, ServiceID: &in.ServiceID, WorkspaceID: in.WorkspaceID,
		Fields: map[string]string{"Incident": shortID(incidentID)},
	}
	name := in.ServiceName
	if name == "" {
		name = "service"
	}
	if a.Type == ActResolve {
		_, sev := alertType(a.Kind)
		ev.Type, ev.Severity = notifications.TypeServiceRecovered, sev
		ev.Title = name + " recovered"
		ev.Body = fmt.Sprintf("The service has been healthy again for %s after %s of trouble (%s).",
			humanDuration(th.stableFor(a.Flapping)), humanDuration(in.At.Sub(a.Incident.OpenedAt)), kindLabel(a.Kind))
		ev.Fields["Duration"] = humanDuration(in.At.Sub(a.Incident.OpenedAt))
		ev.URL = "?tab=overview"
		notifications.Emit(ctx, s.notifier, ev)
		return
	}
	ev.Type, ev.Severity = alertType(a.Kind)
	switch {
	case a.Flapping:
		ev.Title = name + " keeps failing"
	case a.Kind == KindCrashed:
		ev.Title = name + " crashed"
	case a.Kind == KindRestartLoop:
		ev.Title = name + " is restart-looping"
	default:
		ev.Title = name + " is unhealthy"
	}
	body := a.Detail
	if a.Kind != KindUnhealthy && th.LogLines > 0 && s.logs != nil {
		if lines := s.tail(ctx, in.ServiceID, th.LogLines); len(lines) > 0 {
			body += "\n\nLast output:\n" + strings.Join(lines, "\n")
		}
	}
	ev.Body = body
	if a.Restarts > 0 {
		ev.Fields["Restarts"] = fmt.Sprint(a.Restarts)
	}
	ev.URL = "?tab=logs"
	notifications.Emit(ctx, s.notifier, ev)
}

// stableFor is how long a service must look healthy before its incident closes.
func (th Settings) stableFor(flapping bool) time.Duration {
	if flapping {
		return th.FlapWindow
	}
	return th.RecoverAfter
}

func (s *Service) tail(ctx context.Context, id uuid.UUID, n int) []string {
	lines, err := s.logs.TailRuntimeLogsInternal(ctx, id, n)
	if err != nil {
		s.log.DebugContext(ctx, "alert log tail unavailable", "service_id", id.String(), "err", err)
		return nil
	}
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		l = strings.TrimSpace(strings.Map(func(r rune) rune {
			if r < 0x20 || r == 0x7f {
				return -1
			}
			return r
		}, l))
		if l == "" {
			continue
		}
		if r := []rune(l); len(r) > maxLogLine {
			l = string(r[:maxLogLine-1]) + "\u2026"
		}
		out = append(out, l)
	}
	return out
}

func kindLabel(k Kind) string {
	switch k {
	case KindCrashed:
		return "crashed"
	case KindRestartLoop:
		return "restart loop"
	}
	return "unhealthy"
}

func shortID(id uuid.UUID) string {
	if id == uuid.Nil {
		return ""
	}
	return id.String()[:8]
}
