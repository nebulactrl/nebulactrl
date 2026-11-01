package statuspages

import (
	"errors"
	"time"
)

// ErrNotFound is returned by Service.Public for a hostname with no published page.
var ErrNotFound = errors.New("status page not found")

// Public states of a component and of a whole page.
const (
	StateOperational   = "operational"
	StateDegraded      = "degraded"
	StatePartialOutage = "partial_outage" // page only: some components down
	StateMajorOutage   = "major_outage"   // component down, or every component of the page down
	StateMaintenance   = "maintenance"
	StateUnknown       = "unknown" // no data (paused, not yet checked, agent not reporting for less than serverGoneAfter)
)

// UptimeDays is how many days of history a public page shows.
const UptimeDays = 90

// PublicPage is everything a public status page shows, built by
// Service.Public and rendered by internal/statuspublic. It holds nothing
// internal: no IDs, hostnames, error messages or service names other than
// the display names chosen for the page.
type PublicPage struct {
	Title       string
	Description string
	// Footer is plain text shown under the page (line breaks kept, no markup).
	Footer     string
	Theme      string // auto, light or dark
	ShowUptime bool   // show availability percentages
	// Overall is the worst of the component states and the impact of the
	// active incidents (critical: major_outage, major: partial_outage, minor:
	// degraded), maintenance only when nothing is worse.
	Overall         string
	Groups          []PublicGroup
	ActiveIncidents []PublicIncident
	// Maintenance holds windows posted to the page that are active or start
	// within 14 days, soonest first.
	Maintenance []PublicMaintenance
	// PastIncidents holds incidents resolved in the last 30 days, newest first.
	PastIncidents []PublicIncident
	GeneratedAt   time.Time
}

// PublicGroup is a group of components in page order; Name is empty for
// components without a group (shown first).
type PublicGroup struct {
	Name       string
	Components []PublicComponent
}

// PublicComponent is one service as the page shows it.
type PublicComponent struct {
	Name string
	// URL is an optional https link for the name; empty for none.
	URL   string
	State string // operational, degraded, major_outage, maintenance or unknown
	// Availability over the last 24 hours (minute buckets) and the last
	// UptimeDays days; nil without any data.
	Availability24h *float64
	Availability    *float64
	// Days has UptimeDays entries, oldest first, ending today (UTC).
	Days []PublicDay
}

// PublicDay is one UTC day of a component.
type PublicDay struct {
	Date string // YYYY-MM-DD
	// Availability is (up + degraded) / (checks - maintenance); nil when the
	// day has no checks outside maintenance.
	Availability *float64
	// DownMinutes estimates the outage: down checks × the probe interval.
	DownMinutes int
	// Maintenance: some checks of the day fell in maintenance.
	Maintenance bool
	// Incidents are the titles of the page's incidents that affected this
	// component on this day.
	Incidents []string
}

// PublicIncident is an incident posted to the page.
type PublicIncident struct {
	// Key is an opaque stable identifier (a hash, never the database ID) for
	// feed entries and anchors.
	Key        string
	Title      string
	Impact     string // none, minor, major or critical
	Status     string // investigating, identified, monitoring or resolved
	StartedAt  time.Time
	ResolvedAt *time.Time
	// Components are the display names of the affected components on this page.
	Components []string
	// Updates newest first.
	Updates []PublicIncidentUpdate
}

// PublicIncidentUpdate is one entry of an incident's timeline.
type PublicIncidentUpdate struct {
	Status string
	Body   string // plain text; line breaks kept, no markup
	At     time.Time
}

// PublicMaintenance is a maintenance window posted to the page.
type PublicMaintenance struct {
	// Key is an opaque stable identifier like PublicIncident.Key.
	Key         string
	Title       string
	Description string
	StartsAt    time.Time
	EndsAt      time.Time
	// PostedAt is when the window was posted and UpdatedAt when it last changed.
	// They are what a feed reader sorts by; StartsAt may lie in the future.
	PostedAt  time.Time
	UpdatedAt time.Time
	Active    bool
	// Components are the display names of the covered components on this page.
	Components []string
}
