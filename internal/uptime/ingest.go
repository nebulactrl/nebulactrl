package uptime

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/agents"
	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/notifications"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Limits applied to agent-reported results. The agent is trusted to be
// enrolled but not to be well-formed: a bug or a compromised host must not be
// able to bloat the database or write into another server's services.
const (
	// maxResultAge: older results were dropped by the agent itself (ProbeRetention).
	maxResultAge = agentprotocol.ProbeRetention + time.Hour
	// maxResultAhead tolerates clock skew. A result from the future would also
	// make every honest result after it look like a duplicate.
	maxResultAhead = 2 * time.Minute
	maxReplicas    = 1000
	maxLatencyMs   = 10 * 60 * 1000
)

var _ agents.ProbeSink = (*Service)(nil)

// window is a maintenance window of one service.
type window struct{ start, end time.Time }

// serviceCtx is what ingest knows about one service of a frame.
type serviceCtx struct {
	ws        uuid.UUID
	ref       db.ListUptimeServicesOnServerRow
	set       Settings
	windows   []window
	deploying bool
}

// IngestProbes stores the results of a Probes frame: the minute rollups, the
// state of every service (Step), and its incidents, in one transaction. A nil
// error means everything is durable and the agent may forget the frame.
//
// Results are deduplicated by time: an agent resends unacknowledged results
// after a reconnect, in time order, so a result at or before the service's
// last_check_at is one this service already stored. Counting happens in the
// same transaction as that bookkeeping, so a frame never counts twice.
//
// Services that do not belong to the server (unknown, deleted, another
// workspace, running elsewhere) are dropped, as are malformed results and
// results of services whose monitoring is off.
func (s *Service) IngestProbes(ctx context.Context, serverID uuid.UUID, p agentprotocol.Probes) error {
	if len(p.Results) == 0 {
		return nil
	}
	if len(p.Results) > agentprotocol.MaxProbeResults {
		return apierr.BadRequest("too_many_results", fmt.Sprintf("a probes frame carries at most %d results", agentprotocol.MaxProbeResults))
	}
	srv, err := s.q.GetServerByID(ctx, serverID)
	if err != nil {
		return fmt.Errorf("load server: %w", err)
	}
	if srv.Status == "revoked" {
		return nil // acknowledged and dropped
	}

	now := s.now().UTC()
	byService, dropped := sanitize(p.Results, now)
	if dropped > 0 {
		s.log.WarnContext(ctx, "dropping malformed probe results", "server_id", serverID, "count", dropped)
	}
	if len(byService) == 0 {
		return nil
	}
	claimed := sortedIDs(byService)
	frame, err := s.loadFrame(ctx, serverID, srv.WorkspaceID, claimed, byService, now)
	if err != nil {
		return err
	}
	if len(frame.svc) < len(claimed) {
		// Normal: agents also probe routes that are no service (the dashboard's
		// own route, status pages, UDP ports). Those results are dropped and the
		// frame is still stored and acknowledged.
		s.log.DebugContext(ctx, "dropping probe results of unknown or foreign services", "server_id", serverID, "services", len(claimed)-len(frame.svc))
	}
	if len(frame.ids) == 0 {
		return nil
	}

	var changes []stateChange
	var notices []notice
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		changes, notices = nil, nil
		c, n, err := s.apply(ctx, db.New(tx), srv.WorkspaceID, frame, byService)
		changes, notices = c, n
		return err
	})
	if err != nil {
		return err
	}
	for _, c := range changes {
		s.publish(c.projectID, c.environmentID, c.serviceID)
	}
	for _, n := range notices {
		s.emit(ctx, n)
	}
	return nil
}

// frameCtx is what loadFrame resolves for the services of one frame.
type frameCtx struct {
	ids []uuid.UUID // the services that belong to the server and are monitored, sorted
	svc map[uuid.UUID]*serviceCtx
}

// loadFrame validates ownership of the claimed services and loads their
// monitors, maintenance windows and deployments.
func (s *Service) loadFrame(ctx context.Context, serverID, workspaceID uuid.UUID, claimed []uuid.UUID, byService map[uuid.UUID][]agentprotocol.ProbeResult, now time.Time) (*frameCtx, error) {
	rows, err := s.q.ListUptimeServicesOnServer(ctx, db.ListUptimeServicesOnServerParams{ServiceIds: claimed, WorkspaceID: workspaceID, ServerID: serverID})
	if err != nil {
		return nil, fmt.Errorf("validate services: %w", err)
	}
	f := &frameCtx{svc: make(map[uuid.UUID]*serviceCtx, len(rows))}
	owned := make([]uuid.UUID, 0, len(rows))
	for _, r := range rows {
		f.svc[r.Service.ID] = &serviceCtx{ws: workspaceID, ref: r, set: DefaultSettings()}
		owned = append(owned, r.Service.ID)
	}
	if len(owned) == 0 {
		return f, nil
	}
	monitors, err := s.q.ListUptimeMonitors(ctx, owned)
	if err != nil {
		return nil, fmt.Errorf("load monitors: %w", err)
	}
	for _, m := range monitors {
		if sc := f.svc[m.ServiceID]; sc != nil {
			sc.set = settingsOf(m)
		}
	}
	from, to := now, now
	for _, id := range owned {
		for _, r := range byService[id] {
			from, to = earliest(from, r.At), latest(to, r.At)
		}
	}
	wins, err := s.q.ListUptimeMaintenanceWindows(ctx, db.ListUptimeMaintenanceWindowsParams{ServiceIds: owned, FromTs: from, ToTs: to})
	if err != nil {
		return nil, fmt.Errorf("load maintenance windows: %w", err)
	}
	for _, w := range wins {
		if sc := f.svc[w.ServiceID]; sc != nil {
			sc.windows = append(sc.windows, window{start: w.StartsAt, end: w.EndsAt})
		}
	}
	deploying, err := s.q.ListUptimeDeployingServices(ctx, owned)
	if err != nil {
		return nil, fmt.Errorf("load deployments: %w", err)
	}
	for _, id := range deploying {
		if sc := f.svc[id]; sc != nil {
			sc.deploying = true
		}
	}
	for _, id := range owned {
		if f.svc[id].set.Enabled {
			f.ids = append(f.ids, id)
		}
	}
	slices.SortFunc(f.ids, func(a, b uuid.UUID) int { return slices.Compare(a[:], b[:]) })
	return f, nil
}

// stateChange is a state transition to announce on the event bus.
type stateChange struct{ projectID, environmentID, serviceID uuid.UUID }

// apply runs the frame's results through the state machine inside tx and
// returns what to announce once the transaction committed.
func (s *Service) apply(ctx context.Context, q *db.Queries, workspaceID uuid.UUID, f *frameCtx, byService map[uuid.UUID][]agentprotocol.ProbeResult) ([]stateChange, []notice, error) {
	now := s.now().UTC()
	for _, id := range f.ids {
		if err := q.EnsureUptimeState(ctx, db.EnsureUptimeStateParams{ServiceID: id, WorkspaceID: workspaceID, Since: now}); err != nil {
			return nil, nil, fmt.Errorf("ensure uptime state: %w", err)
		}
	}
	stateRows, err := q.LockUptimeStates(ctx, f.ids)
	if err != nil {
		return nil, nil, fmt.Errorf("lock uptime states: %w", err)
	}
	states := make(map[uuid.UUID]db.UptimeState, len(stateRows))
	for _, r := range stateRows {
		states[r.ServiceID] = r
	}
	incRows, err := q.ListOpenUptimeIncidents(ctx, f.ids)
	if err != nil {
		return nil, nil, fmt.Errorf("load incidents: %w", err)
	}
	open := make(map[uuid.UUID]db.UptimeIncident, len(incRows))
	for _, r := range incRows {
		open[r.ServiceID] = r
	}

	var (
		changes []stateChange
		notices []notice
		agg     = map[minuteKey]*minuteCounts{}
	)
	for _, id := range f.ids {
		sc := f.svc[id]
		row, ok := states[id]
		if !ok {
			continue // cannot happen: the row was ensured and locked above
		}
		st := stateOf(row)
		inc, hasInc := open[id]
		st.IncidentOpen, st.IncidentNotified = hasInc, hasInc && inc.NotifiedAt != nil
		before := st.State

		touched := false
		for _, r := range byService[id] {
			if st.LastCheckAt != nil && !r.At.After(*st.LastCheckAt) {
				continue // already stored
			}
			touched = true
			maint := inMaintenance(sc.ref.Service, sc.windows, r.At)
			if r.Outcome != agentprotocol.ProbePaused {
				k := minuteKey{service: id, bucket: r.At.Truncate(time.Minute)}
				if agg[k] == nil {
					agg[k] = &minuteCounts{}
				}
				agg[k].add(r, maint)
			}
			next, acts := Step(st, r, sc.set, Context{InDeploy: sc.deploying, InMaintenance: maint})
			for _, a := range acts {
				switch a.Kind {
				case ActOpen:
					var notified *time.Time
					if a.Notify {
						at := r.At
						notified = &at
					}
					created, err := q.OpenUptimeIncident(ctx, db.OpenUptimeIncidentParams{
						WorkspaceID: workspaceID, ServiceID: id, StartedAt: r.At, Cause: a.Cause, NotifiedAt: notified,
					})
					if err != nil {
						return nil, nil, fmt.Errorf("open uptime incident: %w", err)
					}
					inc = created
					if a.Notify {
						notices = append(notices, newDownNotice(sc, a.Cause, next.Failures))
					}
				case ActResolve:
					var notified *time.Time
					if a.Notify {
						at := r.At
						notified = &at
					}
					if err := q.ResolveUptimeIncident(ctx, db.ResolveUptimeIncidentParams{ID: inc.ID, ResolvedAt: r.At, RecoveryNotifiedAt: notified}); err != nil {
						return nil, nil, fmt.Errorf("resolve uptime incident: %w", err)
					}
					if a.Notify {
						notices = append(notices, newUpNotice(sc, r.At.Sub(inc.StartedAt)))
					}
				}
			}
			st = next
		}
		if !touched {
			continue
		}
		if err := q.SaveUptimeState(ctx, saveParams(id, st)); err != nil {
			return nil, nil, fmt.Errorf("save uptime state: %w", err)
		}
		if st.State != before {
			changes = append(changes, stateChange{projectID: sc.ref.ProjectID, environmentID: sc.ref.Service.EnvironmentID, serviceID: id})
		}
	}

	keys := make([]minuteKey, 0, len(agg))
	for k := range agg {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if c := slices.Compare(keys[i].service[:], keys[j].service[:]); c != 0 {
			return c < 0
		}
		return keys[i].bucket.Before(keys[j].bucket)
	})
	for _, k := range keys {
		m := agg[k]
		if err := q.AddUptime1m(ctx, db.AddUptime1mParams{
			ServiceID: k.service, Bucket: k.bucket, Checks: m.checks, Up: m.up, Degraded: m.degraded, Down: m.down,
			Maintenance: m.maintenance, LatencySumMs: m.latencySum, LatencyMaxMs: m.latencyMax,
		}); err != nil {
			return nil, nil, fmt.Errorf("add uptime_1m: %w", err)
		}
	}
	return changes, notices, nil
}

// minuteKey identifies one uptime_1m row.
type minuteKey struct {
	service uuid.UUID
	bucket  time.Time
}

// minuteCounts is the number of checks to add to one minute.
type minuteCounts struct {
	checks, up, degraded, down, maintenance int32
	latencySum                              int64
	latencyMax                              int32
}

// add counts one check. A check taken in maintenance counts as maintenance
// whatever it found; only passing checks contribute latency.
func (m *minuteCounts) add(r agentprotocol.ProbeResult, maintenance bool) {
	m.checks++
	if maintenance {
		m.maintenance++
		return
	}
	switch r.Outcome {
	case agentprotocol.ProbeUp:
		m.up++
	case agentprotocol.ProbeDegraded:
		m.degraded++
	default:
		m.down++
		return
	}
	m.latencySum += int64(r.LatencyMs)
	m.latencyMax = max(m.latencyMax, int32(r.LatencyMs))
}

// inMaintenance reports whether a check taken at time at ran under maintenance:
// the service's maintenance mode (on since before the check) or a window
// that covered the check.
func inMaintenance(sv db.Service, wins []window, at time.Time) bool {
	if sv.MaintenanceEnabled && (sv.MaintenanceSince == nil || !sv.MaintenanceSince.After(at)) {
		return true
	}
	for _, w := range wins {
		if !at.Before(w.start) && at.Before(w.end) {
			return true
		}
	}
	return false
}

// sanitize validates the results of a frame and groups them by service in
// time order. It returns the number of results it dropped.
func sanitize(in []agentprotocol.ProbeResult, now time.Time) (map[uuid.UUID][]agentprotocol.ProbeResult, int) {
	out := map[uuid.UUID][]agentprotocol.ProbeResult{}
	dropped := 0
	for _, r := range in {
		id, err := uuid.Parse(r.ServiceID)
		if err != nil || r.At.IsZero() || r.At.Before(now.Add(-maxResultAge)) || r.At.After(now.Add(maxResultAhead)) {
			dropped++
			continue
		}
		switch r.Outcome {
		case agentprotocol.ProbeUp, agentprotocol.ProbeDegraded, agentprotocol.ProbeDown, agentprotocol.ProbePaused:
		default:
			dropped++
			continue
		}
		r.At = r.At.UTC()
		r.Replicas = min(max(r.Replicas, 0), maxReplicas)
		r.Passed = min(max(r.Passed, 0), r.Replicas)
		r.LatencyMs = min(max(r.LatencyMs, 0), maxLatencyMs)
		r.Error = truncate(oneLine(r.Error), maxCause)
		out[id] = append(out[id], r)
	}
	for _, rs := range out {
		sort.SliceStable(rs, func(i, j int) bool { return rs[i].At.Before(rs[j].At) })
	}
	return out, dropped
}

func sortedIDs(m map[uuid.UUID][]agentprotocol.ProbeResult) []uuid.UUID {
	ids := make([]uuid.UUID, 0, len(m))
	for id := range m {
		ids = append(ids, id)
	}
	slices.SortFunc(ids, func(a, b uuid.UUID) int { return slices.Compare(a[:], b[:]) })
	return ids
}

func earliest(a, b time.Time) time.Time {
	if b.Before(a) {
		return b
	}
	return a
}

func latest(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

// notice is a notification to send once the transaction committed.
type notice struct {
	up bool
	// reminder: uptime.down repeated while the outage lasts; duration is how long it has.
	reminder                          bool
	workspaceID, projectID, serviceID uuid.UUID
	service, environment, project     string
	cause                             string
	failures                          int
	duration                          time.Duration
}

func newDownNotice(sc *serviceCtx, cause string, failures int) notice {
	n := baseNotice(sc)
	n.cause, n.failures = cause, failures
	return n
}

func newUpNotice(sc *serviceCtx, d time.Duration) notice {
	n := baseNotice(sc)
	n.up, n.duration = true, max(d, 0)
	return n
}

func baseNotice(sc *serviceCtx) notice {
	return notice{
		workspaceID: sc.ws, projectID: sc.ref.ProjectID, serviceID: sc.ref.Service.ID,
		service: sc.ref.Service.Name, environment: sc.ref.EnvironmentName, project: sc.ref.ProjectName,
	}
}

// event builds the notification of n.
func (n notice) event() notifications.Event {
	name := n.service
	if name == "" {
		name = "service"
	}
	where := fmt.Sprintf("%s in %s (project %s)", name, n.environment, n.project)
	ev := notifications.Event{
		ProjectID: &n.projectID, ServiceID: &n.serviceID, WorkspaceID: n.workspaceID,
		Fields: map[string]string{}, URL: "?tab=overview",
	}
	if n.up {
		// The recovery carries the severity of the alert it closes, so a
		// channel that got the alert gets the recovery.
		ev.Type, ev.Severity = notifications.TypeUptimeUp, notifications.SeverityError
		ev.Title = name + " is back up"
		ev.Body = fmt.Sprintf("%s passes its uptime checks again after %s of downtime.", where, humanDuration(n.duration))
		ev.Fields["Duration"] = humanDuration(n.duration)
		return ev
	}
	ev.Type, ev.Severity = notifications.TypeUptimeDown, notifications.SeverityError
	if n.reminder {
		ev.Title = name + " is still down"
		ev.Body = fmt.Sprintf("Reminder: %s has been down for %s: %s.", where, humanDuration(n.duration), n.cause)
		ev.Fields["Cause"] = n.cause
		ev.Fields["Duration"] = humanDuration(n.duration)
		return ev
	}
	ev.Title = name + " is down"
	ev.Body = fmt.Sprintf("%s failed %d uptime checks in a row: %s.", where, n.failures, n.cause)
	ev.Fields["Cause"] = n.cause
	return ev
}

// emit sends a notice. It never fails the ingest: delivery is asynchronous and
// has its own retries.
func (s *Service) emit(ctx context.Context, n notice) {
	if s.notifier == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), notifyTimeout)
	defer cancel()
	notifications.Emit(ctx, s.notifier, n.event())
}

// humanDuration renders a duration the way a message reads: "45 seconds",
// "12 minutes", "3 h 20 min", "2 d 4 h".
func humanDuration(d time.Duration) string {
	d = d.Round(time.Second)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%d seconds", int(d.Seconds()))
	case d < time.Hour:
		m := int(d.Minutes())
		if m == 1 {
			return "1 minute"
		}
		return fmt.Sprintf("%d minutes", m)
	case d < 24*time.Hour:
		h, m := int(d.Hours()), int(d.Minutes())%60
		if m == 0 {
			return fmt.Sprintf("%d h", h)
		}
		return fmt.Sprintf("%d h %d min", h, m)
	}
	days, h := int(d.Hours())/24, int(d.Hours())%24
	if h == 0 {
		return fmt.Sprintf("%d d", days)
	}
	return fmt.Sprintf("%d d %d h", days, h)
}
