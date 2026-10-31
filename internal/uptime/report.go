package uptime

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/services"
)

// Ranges of GET /services/{id}/uptime.
const (
	Range1h  = "1h"
	Range24h = "24h"
	Range7d  = "7d"
	Range30d = "30d"
	Range90d = "90d"
)

// recentIncidents is how many incidents a service report lists.
const recentIncidents = 20

const (
	minutePoints1h  = 60
	hourlyPoints24h = 24
	hourlyPoints7d  = 7 * 24
	dailyPoints90d  = 90
	// EnvironmentDays is how many daily points the environment overview carries.
	EnvironmentDays = 30
)

// UptimePoint is one bucket of a series: an hour or a UTC day. A bucket without
// checks (agent offline, service not monitored yet) is all zeros.
type UptimePoint struct {
	At          time.Time `json:"at" doc:"Start of the bucket (UTC)."`
	Checks      int64     `json:"checks" doc:"Probe cycles of the service: up + degraded + down + maintenance."`
	Up          int64     `json:"up"`
	Degraded    int64     `json:"degraded"`
	Down        int64     `json:"down"`
	Maintenance int64     `json:"maintenance" doc:"Checks taken in maintenance; excluded from availability."`
	AvgLatency  int64     `json:"avgLatencyMs" doc:"Mean latency of the passing checks that measured one; 0 when none did."`
	MaxLatency  int64     `json:"maxLatencyMs"`
}

// AvailabilityView is the share of the checks that passed, in percent, per
// window; null when there is nothing to judge (no checks, or only maintenance).
type AvailabilityView struct {
	Day     *float64 `json:"24h" doc:"The last 24 hours (hour buckets)."`
	Week    *float64 `json:"7d" doc:"The last 7 UTC days, today so far included."`
	Month   *float64 `json:"30d" doc:"The last 30 UTC days, today so far included."`
	Quarter *float64 `json:"90d" doc:"The last 90 UTC days, today so far included."`
}

// UptimeIncidentView is one outage found by the probes.
type UptimeIncidentView struct {
	ID              uuid.UUID  `json:"id"`
	StartedAt       time.Time  `json:"startedAt"`
	ResolvedAt      *time.Time `json:"resolvedAt" doc:"Null while the outage is open."`
	DurationSeconds int64      `json:"durationSeconds" doc:"Until resolvedAt, or until now while open."`
	Cause           string     `json:"cause" doc:"What the failing check reported when the outage opened."`
	Notified        bool       `json:"notified" doc:"uptime.down was sent."`
}

// ServiceReport is the uptime of one service.
type ServiceReport struct {
	ServiceID   uuid.UUID  `json:"serviceId"`
	State       string     `json:"state" enum:"up,degraded,down,maintenance,paused,unknown"`
	Since       *time.Time `json:"since" doc:"When the state was entered; null before the first check."`
	LastCheckAt *time.Time `json:"lastCheckAt"`
	LastOKAt    *time.Time `json:"lastOkAt"`
	LastError   string     `json:"lastError" doc:"What the last failing check reported; empty while it passes."`
	LatencyMs   *int64     `json:"latencyMs" doc:"Latency of the last passing check that measured one."`
	// ConsecutiveFailures and FailureThreshold show a failure that has not become an outage yet ("failing 2/3").
	ConsecutiveFailures int                  `json:"consecutiveFailures" doc:"Failed checks in a row right now. An outage opens when it reaches failureThreshold (during a deployment the count stays 0)."`
	FailureThreshold    int                  `json:"failureThreshold" doc:"The service's effective failure threshold (the default 3 without a monitor)."`
	Monitor             MonitorView          `json:"monitor"`
	Availability        AvailabilityView     `json:"availability"`
	Range               string               `json:"range" enum:"1h,24h,7d,30d,90d"`
	Granularity         string               `json:"granularity" enum:"minute,hour,day" doc:"Width of the series buckets: minutes for 1h, hours for 24h and 7d, UTC days for 30d and 90d."`
	Series              []UptimePoint        `json:"series" doc:"Oldest first, ending with the running bucket; every bucket of the range is present, with checks 0 where there was no data. 1h has 60 minute cells, 24h 24 hours, 7d 168 hours, 30d 30 days, 90d 90 days."`
	Incidents           []UptimeIncidentView `json:"incidents" doc:"The most recent 20, newest first."`
}

// EnvironmentService is one service of the environment overview.
type EnvironmentService struct {
	ServiceID       uuid.UUID     `json:"serviceId"`
	Name            string        `json:"name"`
	Slug            string        `json:"slug"`
	Kind            string        `json:"kind"`
	State           string        `json:"state" enum:"up,degraded,down,maintenance,paused,unknown"`
	Since           *time.Time    `json:"since"`
	LastCheckAt     *time.Time    `json:"lastCheckAt"`
	LastError       string        `json:"lastError"`
	LatencyMs       *int64        `json:"latencyMs"`
	Monitored       bool          `json:"monitored" doc:"False when monitoring is switched off for the service."`
	Availability30d *float64      `json:"availability30d" doc:"Null when there is nothing to judge."`
	Days            []UptimePoint `json:"days" doc:"The last 30 UTC days, oldest first."`
}

// counts is the additive part of a rollup row.
type counts struct {
	checks, up, degraded, down, maintenance int64
	latencySum                              int64
	latencyMax                              int64
}

func (c *counts) add(o counts) {
	c.checks += o.checks
	c.up += o.up
	c.degraded += o.degraded
	c.down += o.down
	c.maintenance += o.maintenance
	c.latencySum += o.latencySum
	c.latencyMax = max(c.latencyMax, o.latencyMax)
}

func countsOf(r db.Uptime1m) counts {
	return counts{
		checks: int64(r.Checks), up: int64(r.Up), degraded: int64(r.Degraded), down: int64(r.Down), maintenance: int64(r.Maintenance),
		latencySum: r.LatencySumMs, latencyMax: int64(r.LatencyMaxMs),
	}
}

// point renders a bucket. The mean latency is over the passing checks:
// failing and maintenance checks measure no latency.
func (c counts) point(at time.Time) UptimePoint {
	p := UptimePoint{
		At: at, Checks: c.checks, Up: c.up, Degraded: c.degraded, Down: c.down, Maintenance: c.maintenance,
		MaxLatency: c.latencyMax,
	}
	if pass := c.up + c.degraded; pass > 0 {
		p.AvgLatency = c.latencySum / pass
	}
	return p
}

// seriesSpec says how a series is cut: bucket width and number of buckets.
type seriesSpec struct {
	width time.Duration
	n     int
}

// buildSeries merges rows of the three tiers into n buckets of width ending
// with the bucket that contains now. Rows of finer tiers fold into the
// running (or, for day series, today's) bucket. Buckets without rows are zero.
// rows holds (bucket start, counts) pairs from any tier; each is folded into
// the bucket that contains its start.
func buildSeries(spec seriesSpec, now time.Time, rows []bucketRow) []UptimePoint {
	cur := now.UTC().Truncate(spec.width)
	start := cur.Add(-time.Duration(spec.n-1) * spec.width)
	acc := make([]counts, spec.n)
	for _, r := range rows {
		if r.at.Before(start) || !r.at.Before(cur.Add(spec.width)) {
			continue
		}
		i := int(r.at.UTC().Truncate(spec.width).Sub(start) / spec.width)
		acc[i].add(r.counts)
	}
	out := make([]UptimePoint, spec.n)
	for i := range out {
		out[i] = acc[i].point(start.Add(time.Duration(i) * spec.width))
	}
	return out
}

// bucketRow is one stored rollup row.
type bucketRow struct {
	at time.Time
	counts
}

// availabilityOf is the availability over points, nil when there is nothing
// to judge.
func availabilityOf(points []UptimePoint) *float64 {
	var checks, up, degraded, maintenance int64
	for _, p := range points {
		checks += p.Checks
		up += p.Up
		degraded += p.Degraded
		maintenance += p.Maintenance
	}
	pct, ok := Availability(checks, up, degraded, maintenance)
	if !ok {
		return nil
	}
	return &pct
}

// lastN returns the last n points.
func lastN(points []UptimePoint, n int) []UptimePoint {
	if len(points) <= n {
		return points
	}
	return points[len(points)-n:]
}

// Series kinds.
const (
	kindHour = "hour"
	kindDay  = "day"
)

// loadSeries reads the series of the given services, n buckets of the given
// kind, ending with the running bucket. Finished buckets come from the hour
// (or day) rollup; the rest of the running bucket comes from the finer tiers
// (a day: the hour rows of today, then the minute rows of the running hour), so
// the newest bucket is as fresh as the minute data. Every service has a series,
// zeros when it has no rows.
func (s *Service) loadSeries(ctx context.Context, ids []uuid.UUID, kind string, n int, now time.Time) (map[uuid.UUID][]UptimePoint, error) {
	now = now.UTC()
	spec := seriesSpec{width: time.Hour, n: n}
	if kind == kindDay {
		spec.width = 24 * time.Hour
	}
	curHour, cur := now.Truncate(time.Hour), now.Truncate(spec.width)
	start := cur.Add(-time.Duration(n-1) * spec.width)

	rows := make(map[uuid.UUID][]bucketRow, len(ids))
	if kind == kindDay {
		days, err := s.q.ListUptime1dRange(ctx, db.ListUptime1dRangeParams{ServiceIds: ids, FromTs: start, ToTs: cur})
		if err != nil {
			return nil, fmt.Errorf("list uptime_1d: %w", err)
		}
		for _, r := range days {
			rows[r.ServiceID] = append(rows[r.ServiceID], bucketRow{at: r.Bucket, counts: countsOf(db.Uptime1m(r))})
		}
	}
	// Hour rows: all finished hours of an hour series, today's hours of a day series.
	hoursFrom := start
	if kind == kindDay {
		hoursFrom = cur
	}
	hours, err := s.q.ListUptime1hRange(ctx, db.ListUptime1hRangeParams{ServiceIds: ids, FromTs: hoursFrom, ToTs: curHour})
	if err != nil {
		return nil, fmt.Errorf("list uptime_1h: %w", err)
	}
	for _, r := range hours {
		rows[r.ServiceID] = append(rows[r.ServiceID], bucketRow{at: r.Bucket, counts: countsOf(db.Uptime1m(r))})
	}
	mins, err := s.q.ListUptime1mRange(ctx, db.ListUptime1mRangeParams{ServiceIds: ids, FromTs: curHour, ToTs: now})
	if err != nil {
		return nil, fmt.Errorf("list uptime_1m: %w", err)
	}
	for _, r := range mins {
		rows[r.ServiceID] = append(rows[r.ServiceID], bucketRow{at: r.Bucket, counts: countsOf(r)})
	}
	out := make(map[uuid.UUID][]UptimePoint, len(ids))
	for _, id := range ids {
		out[id] = buildSeries(spec, now, rows[id])
	}
	return out, nil
}

// loadMinutes reads the last n minutes of a service from the minute rollup:
// one cell per minute, oldest first, and a cell of zeros for a minute without
// checks, so the dashboard can draw a strip of recent checks as it is.
func (s *Service) loadMinutes(ctx context.Context, id uuid.UUID, n int, now time.Time) ([]UptimePoint, error) {
	now = now.UTC()
	start := now.Truncate(time.Minute).Add(-time.Duration(n-1) * time.Minute)
	mins, err := s.q.ListUptime1mRange(ctx, db.ListUptime1mRangeParams{ServiceIds: []uuid.UUID{id}, FromTs: start, ToTs: now})
	if err != nil {
		return nil, fmt.Errorf("list uptime_1m: %w", err)
	}
	rows := make([]bucketRow, len(mins))
	for i, r := range mins {
		rows[i] = bucketRow{at: r.Bucket, counts: countsOf(r)}
	}
	return buildSeries(seriesSpec{width: time.Minute, n: n}, now, rows), nil
}

func monitoredState(st *string, enabled *bool, desired string) string {
	switch {
	case enabled != nil && !*enabled:
		return StatePaused
	case st != nil:
		return *st
	case desired == services.DesiredStopped:
		return StatePaused
	}
	return StateUnknown
}

// GetServiceUptime returns the uptime report of a service. Needs projects:read.
func (s *Service) GetServiceUptime(ctx context.Context, serviceID uuid.UUID, rng string) (ServiceReport, error) {
	sc, err := services.LoadScope(ctx, s.q, policy.ScopeProjectsRead, serviceID)
	if err != nil {
		return ServiceReport{}, err
	}
	switch rng {
	case "":
		rng = Range24h
	case Range1h, Range24h, Range7d, Range30d, Range90d:
	default:
		return ServiceReport{}, apierr.Unprocessable("invalid_range", "range must be 1h, 24h, 7d, 30d or 90d")
	}
	now := s.now().UTC()
	id := sc.Service.ID
	rep := ServiceReport{ServiceID: id, Range: rng, State: StateUnknown, Incidents: []UptimeIncidentView{}}
	if rep.Monitor, err = loadMonitor(ctx, s.q, id); err != nil {
		return rep, err
	}
	rep.FailureThreshold = rep.Monitor.FailureThreshold
	st, err := s.q.GetUptimeState(ctx, id)
	switch {
	case err == nil:
		rep.State, rep.Since = st.State, &st.Since
		rep.LastCheckAt, rep.LastOKAt, rep.LastError = st.LastCheckAt, st.LastOkAt, st.LastError
		if st.LastLatencyMs != nil {
			l := int64(*st.LastLatencyMs)
			rep.LatencyMs = &l
		}
		rep.ConsecutiveFailures = int(st.ConsecutiveFailures)
	case errors.Is(err, pgx.ErrNoRows):
		if sc.Service.DesiredState == services.DesiredStopped {
			rep.State = StatePaused
		}
	default:
		return rep, fmt.Errorf("get uptime state: %w", err)
	}
	if !rep.Monitor.Enabled {
		rep.State = StatePaused
	}

	hourN := hourlyPoints24h
	if rng == Range7d {
		hourN = hourlyPoints7d
	}
	hourly, err := s.loadSeries(ctx, []uuid.UUID{id}, kindHour, hourN, now)
	if err != nil {
		return rep, err
	}
	daily, err := s.loadSeries(ctx, []uuid.UUID{id}, kindDay, dailyPoints90d, now)
	if err != nil {
		return rep, err
	}
	h, d := hourly[id], daily[id]
	rep.Availability = AvailabilityView{
		Day: availabilityOf(lastN(h, hourlyPoints24h)), Week: availabilityOf(lastN(d, 7)),
		Month: availabilityOf(lastN(d, 30)), Quarter: availabilityOf(d),
	}
	switch rng {
	case Range1h:
		rep.Granularity = "minute"
		if rep.Series, err = s.loadMinutes(ctx, id, minutePoints1h, now); err != nil {
			return rep, err
		}
	case Range24h, Range7d:
		rep.Granularity, rep.Series = "hour", h
	case Range30d:
		rep.Granularity, rep.Series = "day", lastN(d, 30)
	default:
		rep.Granularity, rep.Series = "day", d
	}

	incs, err := s.q.ListUptimeIncidents(ctx, db.ListUptimeIncidentsParams{ServiceID: id, RowLimit: recentIncidents})
	if err != nil {
		return rep, fmt.Errorf("list uptime incidents: %w", err)
	}
	for _, i := range incs {
		end := now
		if i.ResolvedAt != nil {
			end = *i.ResolvedAt
		}
		rep.Incidents = append(rep.Incidents, UptimeIncidentView{
			ID: i.ID, StartedAt: i.StartedAt, ResolvedAt: i.ResolvedAt, Cause: i.Cause, Notified: i.NotifiedAt != nil,
			DurationSeconds: max(int64(end.Sub(i.StartedAt).Seconds()), 0),
		})
	}
	return rep, nil
}

// GetEnvironmentUptime lists every service of an environment with its state,
// 30-day availability and 30 daily points. Needs projects:read.
func (s *Service) GetEnvironmentUptime(ctx context.Context, environmentID uuid.UUID) ([]EnvironmentService, error) {
	env, err := services.LoadEnvironment(ctx, s.q, policy.ScopeProjectsRead, environmentID)
	if err != nil {
		return nil, err
	}
	rows, err := s.q.ListEnvironmentUptime(ctx, env.Env.ID)
	if err != nil {
		return nil, fmt.Errorf("list environment uptime: %w", err)
	}
	out := make([]EnvironmentService, 0, len(rows))
	if len(rows) == 0 {
		return out, nil
	}
	ids := make([]uuid.UUID, len(rows))
	for i, r := range rows {
		ids[i] = r.ServiceID
	}
	days, err := s.loadSeries(ctx, ids, kindDay, EnvironmentDays, s.now())
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		item := EnvironmentService{
			ServiceID: r.ServiceID, Name: r.Name, Slug: r.Slug, Kind: r.Kind,
			State: monitoredState(r.State, r.MonitorEnabled, r.DesiredState),
			Since: r.Since, LastCheckAt: r.LastCheckAt,
			Monitored: r.MonitorEnabled == nil || *r.MonitorEnabled,
			Days:      days[r.ServiceID], Availability30d: availabilityOf(days[r.ServiceID]),
		}
		if r.LastError != nil {
			item.LastError = *r.LastError
		}
		if r.LastLatencyMs != nil {
			l := int64(*r.LastLatencyMs)
			item.LatencyMs = &l
		}
		out = append(out, item)
	}
	return out, nil
}
