package analytics

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

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/events"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/services"
)

// Ranges accepted by the analytics endpoints, in display order.
var Ranges = []string{"5m", "10m", "15m", "30m", "1h", "6h", "12h", "24h", "7d", "30d", "90d"}

// DefaultRange is used when a query has none.
const DefaultRange = "24h"

// Retention defaults mirror the servers table defaults.
const (
	defaultHourlyDays = 7
	defaultDailyDays  = 90
)

type rangePlan struct {
	window time.Duration
	step   time.Duration
	tier   Tier
}

// plans maps each range to its window, step and tier (at most ~300 points).
var plans = map[string]rangePlan{
	"5m":  {5 * time.Minute, time.Minute, TierMinute},
	"10m": {10 * time.Minute, time.Minute, TierMinute},
	"15m": {15 * time.Minute, time.Minute, TierMinute},
	"30m": {30 * time.Minute, time.Minute, TierMinute},
	"1h":  {time.Hour, time.Minute, TierMinute},
	"6h":  {6 * time.Hour, 5 * time.Minute, TierMinute},
	"12h": {12 * time.Hour, 5 * time.Minute, TierMinute},
	"24h": {24 * time.Hour, 5 * time.Minute, TierMinute},
	"7d":  {7 * 24 * time.Hour, time.Hour, TierHour},
	"30d": {30 * 24 * time.Hour, 3 * time.Hour, TierHour},
	"90d": {90 * 24 * time.Hour, 24 * time.Hour, TierDay},
}

// planRange resolves a range under the servers' retention: when the hourly
// tier does not reach back far enough, the daily tier answers instead.
func planRange(name string, ret Retention) (p rangePlan, truncated bool, err error) {
	if name == "" {
		name = DefaultRange
	}
	p, ok := plans[name]
	if !ok {
		return rangePlan{}, false, apierr.Unprocessable("invalid_range", fmt.Sprintf("invalid range %q (use one of %v)", name, Ranges))
	}
	if p.tier == TierHour && p.window > time.Duration(ret.HourlyDays)*24*time.Hour {
		p.tier, p.step = TierDay, 24*time.Hour
	}
	switch p.tier {
	case TierDay:
		truncated = p.window > time.Duration(ret.DailyDays)*24*time.Hour
	case TierHour:
		truncated = p.window > time.Duration(ret.HourlyDays)*24*time.Hour
	}
	return p, truncated, nil
}

// Query selects the window of a report.
type Query struct {
	// Range is one of Ranges; empty means DefaultRange.
	Range string
	// Compare adds the previous equal window as Report.Comparison.
	Compare bool
	// Host restricts a service report to one hostname.
	Host string
	// AllPaths ranks every request in TopPaths instead of documents only.
	AllPaths bool
}

// Service implements ingest, rollups and the analytics queries.
type Service struct {
	pool *pgxpool.Pool
	q    *db.Queries
	bus  *events.Bus
	log  *slog.Logger
	now  func() time.Time
}

// NewService returns a Service (pool may be nil for OpenAPI generation).
func NewService(pool *pgxpool.Pool, bus *events.Bus, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{pool: pool, q: db.New(pool), bus: bus, log: logger.With("component", "analytics"), now: time.Now}
}

// target is what a report reads.
type target struct {
	serverIDs    []uuid.UUID
	serviceIDs   []uuid.UUID // nil = every host, including unrouted
	host         *string
	marker       *uuid.UUID // service whose deployments are marked
	withUnrouted bool
	retention    Retention
	// geoServerIDs are the servers whose geolocation setting applies to the
	// report (the service's current server); nil means serverIDs.
	geoServerIDs []uuid.UUID
}

// retentionFor derives the effective retention of a set of servers (the
// smallest of each tier, so a report never claims more history than exists).
func (s *Service) retentionFor(ctx context.Context, serverIDs []uuid.UUID) (Retention, error) {
	ret := Retention{MinuteHours: int(MinuteRetention / time.Hour), HourlyDays: defaultHourlyDays, DailyDays: defaultDailyDays}
	if len(serverIDs) == 0 {
		return ret, nil
	}
	rows, err := s.q.LoadServerRows(ctx, serverIDs)
	if err != nil {
		return ret, fmt.Errorf("load servers: %w", err)
	}
	for i, r := range rows {
		h, d := int(r.TrafficHourlyRetentionDays), int(r.TrafficDailyRetentionDays)
		if i == 0 || h < ret.HourlyDays {
			ret.HourlyDays = h
		}
		if i == 0 || d < ret.DailyDays {
			ret.DailyDays = d
		}
	}
	return ret, nil
}

// visibleServices returns the services a project-restricted principal may see,
// or nil, false for an unrestricted one.
func (s *Service) visibleServices(ctx context.Context) ([]uuid.UUID, bool, error) {
	p, err := policy.Authenticated(ctx)
	if err != nil {
		return nil, false, err
	}
	if !p.Restricted() {
		return nil, false, nil
	}
	ids, err := s.q.ListServiceIDsInProjects(ctx, p.ProjectIDs)
	if err != nil {
		return nil, false, fmt.Errorf("list visible services: %w", err)
	}
	if ids == nil {
		ids = []uuid.UUID{}
	}
	return ids, true, nil
}

func (s *Service) workspaceServers(ctx context.Context) ([]uuid.UUID, error) {
	ids, err := s.q.ListWorkspaceServerIDs(ctx, policy.WorkspaceID(ctx))
	if err != nil {
		return nil, fmt.Errorf("list servers: %w", err)
	}
	return ids, nil
}

func cleanHost(h string) *string {
	h = strings.ToLower(strings.TrimSpace(h))
	if h == "" {
		return nil
	}
	return &h
}

// ServiceTraffic reports the traffic of one service (scope projects:read; a
// service outside the caller's workspace or project restriction is a 404).
func (s *Service) ServiceTraffic(ctx context.Context, serviceID uuid.UUID, q Query) (Report, error) {
	sc, err := services.LoadScope(ctx, s.q, policy.ScopeProjectsRead, serviceID)
	if err != nil {
		return Report{}, err
	}
	// A service may have moved servers; read every server of the workspace
	// filtered by service.
	serverIDs, err := s.workspaceServers(ctx)
	if err != nil {
		return Report{}, err
	}
	ret := Retention{}
	if sc.Service.ServerID != nil {
		ret, err = s.retentionFor(ctx, []uuid.UUID{*sc.Service.ServerID})
	} else {
		ret, err = s.retentionFor(ctx, nil)
	}
	if err != nil {
		return Report{}, err
	}
	t := target{
		serverIDs: serverIDs, serviceIDs: []uuid.UUID{serviceID}, host: cleanHost(q.Host), marker: &serviceID, retention: ret,
	}
	if sc.Service.ServerID != nil {
		t.geoServerIDs = []uuid.UUID{*sc.Service.ServerID}
	}
	return s.report(ctx, t, q)
}

// ServerTraffic reports the traffic of every host of one server (scope
// servers:read). Project-restricted principals only see their services.
func (s *Service) ServerTraffic(ctx context.Context, serverID uuid.UUID, q Query) (Report, error) {
	if err := policy.Require(ctx, policy.ScopeServersRead, nil); err != nil {
		return Report{}, err
	}
	row, err := s.q.GetServerScoped(ctx, db.GetServerScopedParams{ID: serverID, WorkspaceID: policy.WorkspaceID(ctx)})
	if errors.Is(err, pgx.ErrNoRows) {
		return Report{}, apierr.NotFound("server")
	}
	if err != nil {
		return Report{}, fmt.Errorf("get server: %w", err)
	}
	ret, err := s.retentionFor(ctx, []uuid.UUID{row.ID})
	if err != nil {
		return Report{}, err
	}
	vis, restricted, err := s.visibleServices(ctx)
	if err != nil {
		return Report{}, err
	}
	t := target{serverIDs: []uuid.UUID{row.ID}, host: cleanHost(q.Host), retention: ret, withUnrouted: !restricted}
	if restricted {
		t.serviceIDs = vis
	}
	return s.report(ctx, t, q)
}

// Filter narrows a workspace report.
type Filter struct {
	ServerID      *uuid.UUID
	ServiceID     *uuid.UUID
	EnvironmentID *uuid.UUID
}

// WorkspaceTraffic reports traffic across servers, optionally filtered. With no
// service or environment filter it needs servers:read; with one it needs
// projects:read and the project must be visible to the caller.
func (s *Service) WorkspaceTraffic(ctx context.Context, f Filter, q Query) (Report, error) {
	filtered := f.ServiceID != nil || f.EnvironmentID != nil
	scope := policy.ScopeServersRead
	if filtered {
		scope = policy.ScopeProjectsRead
	}
	if err := policy.Require(ctx, scope, nil); err != nil {
		return Report{}, err
	}
	ws := policy.WorkspaceID(ctx)

	var serverIDs []uuid.UUID
	if f.ServerID != nil {
		row, err := s.q.GetServerScoped(ctx, db.GetServerScopedParams{ID: *f.ServerID, WorkspaceID: ws})
		if errors.Is(err, pgx.ErrNoRows) {
			return Report{}, apierr.NotFound("server")
		}
		if err != nil {
			return Report{}, fmt.Errorf("get server: %w", err)
		}
		serverIDs = []uuid.UUID{row.ID}
	} else {
		var err error
		if serverIDs, err = s.workspaceServers(ctx); err != nil {
			return Report{}, err
		}
	}
	ret, err := s.retentionFor(ctx, serverIDs)
	if err != nil {
		return Report{}, err
	}
	t := target{serverIDs: serverIDs, host: cleanHost(q.Host), retention: ret}

	switch {
	case f.ServiceID != nil:
		if _, err := services.LoadScope(ctx, s.q, policy.ScopeProjectsRead, *f.ServiceID); err != nil {
			return Report{}, err
		}
		t.serviceIDs, t.marker = []uuid.UUID{*f.ServiceID}, f.ServiceID
	case f.EnvironmentID != nil:
		if _, err := services.LoadEnvironment(ctx, s.q, policy.ScopeProjectsRead, *f.EnvironmentID); err != nil {
			return Report{}, err
		}
		ids, err := s.q.ListServiceIDsInEnvironment(ctx, *f.EnvironmentID)
		if err != nil {
			return Report{}, fmt.Errorf("list environment services: %w", err)
		}
		if ids == nil {
			ids = []uuid.UUID{}
		}
		t.serviceIDs = ids
	default:
		vis, restricted, err := s.visibleServices(ctx)
		if err != nil {
			return Report{}, err
		}
		if restricted {
			t.serviceIDs = vis
		} else {
			t.withUnrouted = true
		}
	}
	return s.report(ctx, t, q)
}

// fetch reads the rows of a tier for [from, to) matching the target.
func (s *Service) fetch(ctx context.Context, tier Tier, t target, from, to time.Time) ([]row, error) {
	switch tier {
	case TierMinute:
		rs, err := s.q.SelectTraffic1m(ctx, db.SelectTraffic1mParams{ServerIds: t.serverIDs, FromTs: from, ToTs: to, ServiceIds: t.serviceIDs, Host: t.host})
		if err != nil {
			return nil, fmt.Errorf("select traffic_1m: %w", err)
		}
		return rows1m(rs), nil
	case TierHour:
		rs, err := s.q.SelectTraffic1h(ctx, db.SelectTraffic1hParams{ServerIds: t.serverIDs, FromTs: from, ToTs: to, ServiceIds: t.serviceIDs, Host: t.host})
		if err != nil {
			return nil, fmt.Errorf("select traffic_1h: %w", err)
		}
		return rows1h(rs), nil
	default:
		rs, err := s.q.SelectTraffic1d(ctx, db.SelectTraffic1dParams{ServerIds: t.serverIDs, FromTs: from, ToTs: to, ServiceIds: t.serviceIDs, Host: t.host})
		if err != nil {
			return nil, fmt.Errorf("select traffic_1d: %w", err)
		}
		return rows1d(rs), nil
	}
}

func (s *Service) report(ctx context.Context, t target, q Query) (Report, error) {
	plan, truncated, err := planRange(q.Range, t.retention)
	if err != nil {
		return Report{}, err
	}
	now := s.now().UTC()
	from := now.Add(-plan.window)
	floor := from.Truncate(plan.tier.Width())

	in := reportInput{
		from: from, to: now, step: plan.step, tier: plan.tier, retention: t.retention, truncated: truncated,
		allPaths: q.AllPaths, compare: q.Compare, withUnrouted: t.withUnrouted, now: now,
	}
	end := now.Add(time.Minute)
	geoIDs := t.geoServerIDs
	if geoIDs == nil {
		geoIDs = t.serverIDs
	}
	if in.geo, err = s.geoSettingsFor(ctx, geoIDs); err != nil {
		return Report{}, err
	}
	if in.rows, err = s.fetch(ctx, plan.tier, t, floor, end); err != nil {
		return Report{}, err
	}
	if q.Compare {
		prevFrom := from.Add(-plan.window).Truncate(plan.tier.Width())
		if in.prev, err = s.fetch(ctx, plan.tier, t, prevFrom, floor); err != nil {
			return Report{}, err
		}
	}
	if plan.window >= 7*24*time.Hour {
		if plan.tier == TierHour {
			in.hourRows = in.rows
		} else {
			hfrom := now.Add(-time.Duration(t.retention.HourlyDays) * 24 * time.Hour)
			if hfrom.Before(from) {
				hfrom = from
			}
			if in.hourRows, err = s.fetch(ctx, TierHour, t, hfrom.Truncate(time.Hour), end); err != nil {
				return Report{}, err
			}
		}
	}
	if t.marker != nil {
		marks, err := s.q.ListDeploymentMarkers(ctx, db.ListDeploymentMarkersParams{ServiceID: *t.marker, FromTs: from, ToTs: end})
		if err != nil {
			return Report{}, fmt.Errorf("list deployment markers: %w", err)
		}
		for _, m := range marks {
			dm := DeploymentMarker{ID: m.ID, At: m.At, Status: m.Status}
			if len(m.CommitSha) >= 7 {
				dm.Sha7 = m.CommitSha[:7]
			}
			in.markers = append(in.markers, dm)
		}
	}
	return build(in), nil
}
