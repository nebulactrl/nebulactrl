package statuspages

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"net"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

const (
	// pastIncidentDays is how long resolved incidents stay on the page.
	pastIncidentDays = 30
	// maintenanceHorizon is how far ahead scheduled maintenance is announced.
	maintenanceHorizon = 14 * 24 * time.Hour
	// serverGoneAfter is how long the control plane must not have heard from a
	// server, while it was itself running, before the public page shows the
	// services on it as down rather than as "no data".
	serverGoneAfter = 5 * time.Minute
)

// usage counts the checks of a component over a period (a day or the 90 days).
type usage struct{ checks, up, degraded, down, maintenance int }

func (u *usage) add(o usage) {
	u.checks += o.checks
	u.up += o.up
	u.degraded += o.degraded
	u.down += o.down
	u.maintenance += o.maintenance
}

// availability is (up + degraded) / (checks - maintenance), 0..1; nil without
// any check outside maintenance.
func (u usage) availability() *float64 {
	den := u.checks - u.maintenance
	if den <= 0 {
		return nil
	}
	a := math.Min(1, math.Max(0, float64(u.up+u.degraded)/float64(den)))
	return &a
}

// componentState maps the stored state of a service to its public state:
// paused, unknown and a missing state are all "no data".
func componentState(state string) string {
	switch state {
	case "up":
		return StateOperational
	case "degraded":
		return StateDegraded
	case "down":
		return StateMajorOutage
	case "maintenance":
		return StateMaintenance
	}
	return StateUnknown
}

// publicComponentState is a component's public state: the stored uptime state,
// except that an unknown state (not paused) whose server has not been heard from
// for serverGoneAfter, counted from when this control plane started at the
// earliest, is a major outage, or maintenance when maintenance covers the service now.
func publicComponentState(state string, serverLastSeen *time.Time, inMaintenance bool, now, started time.Time) string {
	if pub := componentState(state); pub != StateUnknown {
		return pub
	}
	// Paused is stopped on purpose; no server is no server, or a pending or revoked one.
	if state == "paused" || serverLastSeen == nil {
		return StateUnknown
	}
	since := *serverLastSeen
	if started.After(since) {
		since = started
	}
	if now.Sub(since) < serverGoneAfter {
		return StateUnknown
	}
	if inMaintenance {
		return StateMaintenance
	}
	return StateMajorOutage
}

// stateRank orders the states by severity; unknown has none.
func stateRank(state string) int {
	switch state {
	case StateOperational:
		return 1
	case StateMaintenance:
		return 2
	case StateDegraded:
		return 3
	case StatePartialOutage:
		return 4
	case StateMajorOutage:
		return 5
	}
	return 0
}

// impactState is the page state an active incident of this impact asks for.
func impactState(impact string) string {
	switch impact {
	case ImpactCritical:
		return StateMajorOutage
	case ImpactMajor:
		return StatePartialOutage
	case ImpactMinor:
		return StateDegraded
	}
	return StateOperational
}

// overallState is the state of the whole page: the worst of the components
// (some down: partial outage; every one down: major outage) and of the
// impacts of the active incidents; maintenance only when nothing is worse
// (a component in maintenance, or an active window posted to the page);
// unknown when no component has data.
func overallState(components []string, incidentImpacts []string, windowActive bool) string {
	var down, degraded, maintenance, known int
	for _, c := range components {
		switch c {
		case StateMajorOutage:
			down++
		case StateDegraded:
			degraded++
		case StateMaintenance:
			maintenance++
		}
		if c != StateUnknown {
			known++
		}
	}
	worst := StateOperational
	raise := func(s string) {
		if stateRank(s) > stateRank(worst) {
			worst = s
		}
	}
	switch {
	case down > 0 && down == len(components):
		raise(StateMajorOutage)
	case down > 0:
		raise(StatePartialOutage)
	}
	if degraded > 0 {
		raise(StateDegraded)
	}
	for _, imp := range incidentImpacts {
		raise(impactState(imp))
	}
	switch {
	case stateRank(worst) > stateRank(StateMaintenance):
		return worst
	case maintenance > 0 || windowActive:
		return StateMaintenance
	case known == 0:
		return StateUnknown
	}
	return worst
}

// normalizeHost lowercases a Host header value and drops its port and trailing dot.
func normalizeHost(h string) string {
	h = strings.ToLower(strings.TrimSpace(h))
	if host, _, err := net.SplitHostPort(h); err == nil {
		h = host
	}
	return strings.TrimSuffix(h, ".")
}

func dayKey(t time.Time) string { return t.UTC().Format("2006-01-02") }

// dayStart is the UTC midnight of t's day.
func dayStart(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

// incidentDayKeys lists the UTC days, from first on, that an incident running
// from start to end (nil: still going, up to now) touches.
func incidentDayKeys(start time.Time, end *time.Time, now, first time.Time) []string {
	last := now
	if end != nil {
		last = *end
	}
	var keys []string
	for d := dayStart(start); !d.After(dayStart(last)); d = d.AddDate(0, 0, 1) {
		if !d.Before(first) {
			keys = append(keys, dayKey(d))
		}
	}
	return keys
}

// buildDays lays out the UptimeDays days ending with today (oldest first).
// titles maps a day to the titles of the incidents that affected the component.
func buildDays(today time.Time, perDay map[string]usage, titles map[string][]string) []PublicDay {
	days := make([]PublicDay, UptimeDays)
	for i := range days {
		key := dayKey(today.AddDate(0, 0, i-(UptimeDays-1)))
		u := perDay[key]
		days[i] = PublicDay{
			Date:         key,
			Availability: u.availability(),
			DownMinutes:  int(math.Round(float64(u.down) * agentprotocol.UptimeInterval.Seconds() / 60)),
			Maintenance:  u.maintenance > 0,
			Incidents:    titles[key],
		}
	}
	return days
}

// Public builds the view model of the published page served on hostname. It
// returns ErrNotFound for an unknown or unpublished hostname. A call runs a
// fixed number of queries whatever the number of components and days.
func (s *Service) Public(ctx context.Context, hostname string) (PublicPage, error) {
	host := normalizeHost(hostname)
	if host == "" {
		return PublicPage{}, ErrNotFound
	}
	page, err := s.q.GetPublishedStatusPageByHostname(ctx, host)
	if errors.Is(err, pgx.ErrNoRows) {
		return PublicPage{}, ErrNotFound
	}
	if err != nil {
		return PublicPage{}, fmt.Errorf("load status page: %w", err)
	}
	now := s.now().UTC()
	today := dayStart(now)
	windowStart := today.AddDate(0, 0, -(UptimeDays - 1))

	comps, err := s.q.ListPublicComponents(ctx, db.ListPublicComponentsParams{PageID: page.ID, NowTs: now})
	if err != nil {
		return PublicPage{}, fmt.Errorf("load components: %w", err)
	}
	perService, err := s.publicUsage(ctx, page.ID, windowStart, today)
	if err != nil {
		return PublicPage{}, err
	}
	recent, err := s.publicRecentUsage(ctx, page.ID, now.Add(-24*time.Hour))
	if err != nil {
		return PublicPage{}, err
	}
	incidents, err := s.publicIncidents(ctx, page.ID, now, windowStart)
	if err != nil {
		return PublicPage{}, err
	}

	names := make(map[uuid.UUID]string, len(comps))
	order := make(map[uuid.UUID]int, len(comps))
	for i, c := range comps {
		names[c.ServiceID] = c.DisplayName
		order[c.ServiceID] = i
	}
	componentNames := func(serviceIDs []uuid.UUID) []string {
		in := slices.Clone(serviceIDs)
		in = slices.DeleteFunc(in, func(id uuid.UUID) bool { _, ok := order[id]; return !ok })
		slices.SortFunc(in, func(a, b uuid.UUID) int { return order[a] - order[b] })
		out := make([]string, 0, len(in))
		for _, id := range in {
			out = append(out, names[id])
		}
		return out
	}

	// Incident titles per component and day.
	titles := make(map[uuid.UUID]map[string][]string)
	for _, inc := range incidents.all {
		keys := incidentDayKeys(inc.startedAt, inc.resolvedAt, now, windowStart)
		for _, sv := range inc.services {
			if _, ok := order[sv]; !ok {
				continue
			}
			if titles[sv] == nil {
				titles[sv] = map[string][]string{}
			}
			for _, k := range keys {
				if !slices.Contains(titles[sv][k], inc.title) {
					titles[sv][k] = append(titles[sv][k], inc.title)
				}
			}
		}
	}

	out := PublicPage{
		Title: page.Title, Description: page.Description, Footer: page.Footer, Theme: page.Theme, ShowUptime: page.ShowUptime, GeneratedAt: now,
		Groups: []PublicGroup{}, ActiveIncidents: []PublicIncident{}, Maintenance: []PublicMaintenance{}, PastIncidents: []PublicIncident{},
	}
	states := make([]string, 0, len(comps))
	groupAt := map[string]int{}
	var groups []PublicGroup
	for _, c := range comps {
		state := publicComponentState(c.State, c.ServerLastSeenAt, c.InMaintenance, now, s.started)
		states = append(states, state)
		total := usage{}
		for _, u := range perService[c.ServiceID] {
			total.add(u)
		}
		pc := PublicComponent{
			Name: c.DisplayName, URL: c.Url, State: state, Availability: total.availability(), Availability24h: recent[c.ServiceID].availability(),
			Days: buildDays(today, perService[c.ServiceID], titles[c.ServiceID]),
		}
		gi, ok := groupAt[c.GroupName]
		if !ok {
			gi = len(groups)
			groupAt[c.GroupName] = gi
			groups = append(groups, PublicGroup{Name: c.GroupName})
		}
		groups[gi].Components = append(groups[gi].Components, pc)
	}
	// Ungrouped components come first, the named groups in page order.
	if i, ok := groupAt[""]; ok && i != 0 {
		ungrouped := groups[i]
		groups = append([]PublicGroup{ungrouped}, slices.Delete(groups, i, i+1)...)
	}
	if groups != nil {
		out.Groups = groups
	}

	var impacts []string
	for _, inc := range incidents.active {
		impacts = append(impacts, inc.impact)
		out.ActiveIncidents = append(out.ActiveIncidents, inc.public(componentNames(inc.services)))
	}
	for _, inc := range incidents.past {
		out.PastIncidents = append(out.PastIncidents, inc.public(componentNames(inc.services)))
	}

	windows, err := s.publicMaintenance(ctx, page.ID, now, componentNames)
	if err != nil {
		return PublicPage{}, err
	}
	out.Maintenance = windows
	windowActive := slices.ContainsFunc(windows, func(w PublicMaintenance) bool { return w.Active })
	out.Overall = overallState(states, impacts, windowActive)
	return out, nil
}

// publicUsage returns the checks of the page's services per service and day:
// the day rollups, with today's partial day taken from the minute rollups
// (fresher than the hourly day rollup) when they hold it.
func (s *Service) publicUsage(ctx context.Context, pageID uuid.UUID, from, today time.Time) (map[uuid.UUID]map[string]usage, error) {
	days, err := s.q.ListPublicDailyUptime(ctx, db.ListPublicDailyUptimeParams{PageID: pageID, FromTs: from, ToTs: today.AddDate(0, 0, 1)})
	if err != nil {
		return nil, fmt.Errorf("load day uptime: %w", err)
	}
	partial, err := s.q.ListPublicPartialDayUptime(ctx, db.ListPublicPartialDayUptimeParams{PageID: pageID, SinceTs: today})
	if err != nil {
		return nil, fmt.Errorf("load today's uptime: %w", err)
	}
	out := make(map[uuid.UUID]map[string]usage)
	put := func(id uuid.UUID, key string, u usage) {
		if out[id] == nil {
			out[id] = map[string]usage{}
		}
		out[id][key] = u
	}
	for _, d := range days {
		put(d.ServiceID, dayKey(d.Bucket), usage{int(d.Checks), int(d.Up), int(d.Degraded), int(d.Down), int(d.Maintenance)})
	}
	for _, p := range partial {
		if p.Checks > 0 {
			put(p.ServiceID, dayKey(today), usage{int(p.Checks), int(p.Up), int(p.Degraded), int(p.Down), int(p.Maintenance)})
		}
	}
	return out, nil
}

// opaqueKey is the public, stable identifier of an incident or maintenance
// window: the first 16 hex characters of sha256(kind + ":" + id). It reveals
// nothing of the database ID and cannot be reversed to it.
func opaqueKey(kind string, id uuid.UUID) string {
	sum := sha256.Sum256([]byte(kind + ":" + id.String()))
	return hex.EncodeToString(sum[:])[:16]
}

// publicRecentUsage sums the minute rollups since since per service (the
// last 24 hours); services without checks are absent.
func (s *Service) publicRecentUsage(ctx context.Context, pageID uuid.UUID, since time.Time) (map[uuid.UUID]usage, error) {
	rows, err := s.q.ListPublicPartialDayUptime(ctx, db.ListPublicPartialDayUptimeParams{PageID: pageID, SinceTs: since})
	if err != nil {
		return nil, fmt.Errorf("load recent uptime: %w", err)
	}
	out := make(map[uuid.UUID]usage, len(rows))
	for _, r := range rows {
		out[r.ServiceID] = usage{int(r.Checks), int(r.Up), int(r.Degraded), int(r.Down), int(r.Maintenance)}
	}
	return out, nil
}

// publicIncident is an incident of a page before it is mapped to the view.
type publicIncident struct {
	id         uuid.UUID
	title      string
	impact     string
	status     string
	startedAt  time.Time
	resolvedAt *time.Time
	services   []uuid.UUID
	updates    []PublicIncidentUpdate
}

func (i publicIncident) public(components []string) PublicIncident {
	return PublicIncident{
		Key: opaqueKey("incident", i.id), Title: i.title, Impact: i.impact, Status: i.status, StartedAt: i.startedAt, ResolvedAt: i.resolvedAt,
		Components: components, Updates: i.updates,
	}
}

// pageIncidents are the incidents of a page: all those touching the 90 days
// (for the day bars), the active ones (newest first) and those resolved in the
// last 30 days (latest resolution first).
type pageIncidents struct {
	all, active, past []publicIncident
}

func (s *Service) publicIncidents(ctx context.Context, pageID uuid.UUID, now, windowStart time.Time) (pageIncidents, error) {
	rows, err := s.q.ListPublicIncidents(ctx, db.ListPublicIncidentsParams{PageID: pageID, SinceTs: windowStart})
	if err != nil {
		return pageIncidents{}, fmt.Errorf("load incidents: %w", err)
	}
	var out pageIncidents
	if len(rows) == 0 {
		return out, nil
	}
	ids := make([]uuid.UUID, len(rows))
	for i, r := range rows {
		ids[i] = r.ID
	}
	svcRows, err := s.q.ListStatusIncidentServiceIDs(ctx, ids)
	if err != nil {
		return pageIncidents{}, fmt.Errorf("load incident services: %w", err)
	}
	services := map[uuid.UUID][]uuid.UUID{}
	for _, r := range svcRows {
		services[r.IncidentID] = append(services[r.IncidentID], r.ServiceID)
	}
	pastFrom := now.AddDate(0, 0, -pastIncidentDays)
	var shown []uuid.UUID
	for _, r := range rows {
		inc := publicIncident{
			id: r.ID, title: r.Title, impact: r.Impact, status: r.Status, startedAt: r.CreatedAt, resolvedAt: r.ResolvedAt,
			services: services[r.ID], updates: []PublicIncidentUpdate{},
		}
		out.all = append(out.all, inc)
		switch {
		case r.ResolvedAt == nil:
			out.active = append(out.active, inc)
			shown = append(shown, r.ID)
		case !r.ResolvedAt.Before(pastFrom):
			out.past = append(out.past, inc)
			shown = append(shown, r.ID)
		}
	}
	slices.SortStableFunc(out.past, func(a, b publicIncident) int { return b.resolvedAt.Compare(*a.resolvedAt) })
	if len(shown) == 0 {
		return out, nil
	}
	ups, err := s.q.ListPublicIncidentUpdates(ctx, shown)
	if err != nil {
		return pageIncidents{}, fmt.Errorf("load incident updates: %w", err)
	}
	updates := map[uuid.UUID][]PublicIncidentUpdate{}
	for _, u := range ups {
		updates[u.IncidentID] = append(updates[u.IncidentID], PublicIncidentUpdate{Status: u.Status, Body: u.Body, At: u.CreatedAt})
	}
	for _, list := range [][]publicIncident{out.active, out.past} {
		for i := range list {
			if u := updates[list[i].id]; u != nil {
				list[i].updates = u
			}
		}
	}
	return out, nil
}

// publicMaintenance returns the windows posted to the page that are active or
// start within the horizon, soonest first.
func (s *Service) publicMaintenance(ctx context.Context, pageID uuid.UUID, now time.Time, componentNames func([]uuid.UUID) []string) ([]PublicMaintenance, error) {
	rows, err := s.q.ListPublicMaintenance(ctx, db.ListPublicMaintenanceParams{PageID: pageID, AsOf: now, Horizon: now.Add(maintenanceHorizon)})
	if err != nil {
		return nil, fmt.Errorf("load maintenance: %w", err)
	}
	out := make([]PublicMaintenance, 0, len(rows))
	if len(rows) == 0 {
		return out, nil
	}
	ids := make([]uuid.UUID, len(rows))
	for i, r := range rows {
		ids[i] = r.ID
	}
	svcRows, err := s.q.ListMaintenanceWindowServiceIDs(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("load maintenance services: %w", err)
	}
	services := map[uuid.UUID][]uuid.UUID{}
	for _, r := range svcRows {
		services[r.WindowID] = append(services[r.WindowID], r.ServiceID)
	}
	for _, r := range rows {
		out = append(out, PublicMaintenance{
			Key: opaqueKey("maintenance", r.ID), Title: r.Title, Description: r.Description, StartsAt: r.StartsAt, EndsAt: r.EndsAt,
			PostedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
			Active: !now.Before(r.StartsAt), Components: componentNames(services[r.ID]),
		})
	}
	return out, nil
}
