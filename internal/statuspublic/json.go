package statuspublic

import (
	"encoding/json"
	"time"

	"github.com/nebulactrl/nebulactrl/internal/statuspages"
)

// The status.json model. It is written out field by field instead of marshaling
// statuspages.PublicPage, so a field added to the view model never reaches the
// public JSON by accident, and so the names are stable public API.

type jsonStatus struct {
	State string `json:"state"` // operational, degraded, partial_outage, major_outage, maintenance or unknown
	Label string `json:"label"`
}

type jsonDay struct {
	Date string `json:"date"` // YYYY-MM-DD, UTC
	// Status is the day's band: operational (99.9% or more), minor_issues (99%),
	// degraded (95%), outage, maintenance or no_data.
	Status string `json:"status"`
	// Uptime is a percentage truncated to two decimals; omitted when the page
	// hides uptime figures or the day has no data.
	Uptime *float64 `json:"uptime,omitempty"`
	// DownMinutes is omitted when the page hides uptime figures.
	DownMinutes int      `json:"downMinutes,omitempty"`
	Maintenance bool     `json:"maintenance,omitempty"`
	Incidents   []string `json:"incidents"`
}

type jsonComponent struct {
	Name string `json:"name"`
	// Slug is the component's name in badge URLs (/badge/<slug>/status.svg).
	Slug string `json:"slug"`
	// URL is the component's https link, when the page has one.
	URL    string     `json:"url,omitempty"`
	Status jsonStatus `json:"status"`
	// Uptime is the percentage over the last 90 days and Uptime24h over the
	// last 24 hours; each is omitted when the page hides uptime figures or
	// there is no data.
	Uptime    *float64  `json:"uptime,omitempty"`
	Uptime24h *float64  `json:"uptime24h,omitempty"`
	Days      []jsonDay `json:"days"`
}

type jsonGroup struct {
	Name       string          `json:"name"` // empty for components without a group
	Components []jsonComponent `json:"components"`
}

type jsonUpdate struct {
	Status string    `json:"status"`
	Body   string    `json:"body"`
	At     time.Time `json:"at"`
}

type jsonIncident struct {
	Key        string       `json:"key,omitempty"` // opaque and stable, as in the feed's entry ids
	Title      string       `json:"title"`
	Impact     string       `json:"impact"` // none, minor, major or critical
	Status     string       `json:"status"` // investigating, identified, monitoring or resolved
	StartedAt  time.Time    `json:"startedAt"`
	ResolvedAt *time.Time   `json:"resolvedAt,omitempty"`
	Components []string     `json:"components"`
	Updates    []jsonUpdate `json:"updates"`
}

type jsonMaintenance struct {
	Key         string    `json:"key,omitempty"` // opaque and stable, as in the feed's entry ids
	Title       string    `json:"title"`
	Description string    `json:"description,omitempty"`
	StartsAt    time.Time `json:"startsAt"`
	EndsAt      time.Time `json:"endsAt"`
	Active      bool      `json:"active"`
	Components  []string  `json:"components"`
}

type jsonPage struct {
	Title           string            `json:"title"`
	Description     string            `json:"description,omitempty"`
	Footer          string            `json:"footer,omitempty"`
	Status          jsonStatus        `json:"status"`
	ShowUptime      bool              `json:"showUptime"`
	GeneratedAt     time.Time         `json:"generatedAt"`
	Groups          []jsonGroup       `json:"groups"`
	ActiveIncidents []jsonIncident    `json:"activeIncidents"`
	Maintenance     []jsonMaintenance `json:"maintenance"`
	PastIncidents   []jsonIncident    `json:"pastIncidents"`
}

func strs(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func jsonIncidentOf(i statuspages.PublicIncident) jsonIncident {
	out := jsonIncident{
		Key:   i.Key,
		Title: i.Title, Impact: i.Impact, Status: i.Status, StartedAt: i.StartedAt.UTC(),
		Components: strs(i.Components), Updates: make([]jsonUpdate, 0, len(i.Updates)),
	}
	if i.ResolvedAt != nil {
		t := i.ResolvedAt.UTC()
		out.ResolvedAt = &t
	}
	for _, u := range i.Updates {
		out.Updates = append(out.Updates, jsonUpdate{Status: u.Status, Body: u.Body, At: u.At.UTC()})
	}
	return out
}

func jsonComponentOf(c statuspages.PublicComponent, slug string, showUptime bool) jsonComponent {
	out := jsonComponent{
		Name:   c.Name,
		Slug:   slug,
		URL:    safeLink(c.URL),
		Status: jsonStatus{State: stateKey(c.State), Label: componentLabel(c.State)},
		Days:   make([]jsonDay, 0, len(c.Days)),
	}
	if showUptime && c.Availability != nil {
		v := percentValue(*c.Availability)
		out.Uptime = &v
	}
	if showUptime && c.Availability24h != nil {
		v := percentValue(*c.Availability24h)
		out.Uptime24h = &v
	}
	for _, d := range c.Days {
		day := jsonDay{Date: d.Date, Status: bandOf(d).Status, Maintenance: d.Maintenance, Incidents: strs(d.Incidents)}
		if showUptime {
			day.DownMinutes = d.DownMinutes
			if d.Availability != nil {
				v := percentValue(*d.Availability)
				day.Uptime = &v
			}
		}
		out.Days = append(out.Days, day)
	}
	return out
}

// renderJSON renders the public JSON of a page.
func renderJSON(p statuspages.PublicPage) ([]byte, error) {
	out := jsonPage{
		Title: p.Title, Description: p.Description, Footer: p.Footer,
		Status:     jsonStatus{State: stateKey(p.Overall), Label: overallLabel(p.Overall)},
		ShowUptime: p.ShowUptime, GeneratedAt: p.GeneratedAt.UTC(),
		Groups:          make([]jsonGroup, 0, len(p.Groups)),
		ActiveIncidents: make([]jsonIncident, 0, len(p.ActiveIncidents)),
		Maintenance:     make([]jsonMaintenance, 0, len(p.Maintenance)),
		PastIncidents:   make([]jsonIncident, 0, len(p.PastIncidents)),
	}
	refs := componentRefs(p)
	next := 0
	for _, g := range p.Groups {
		jg := jsonGroup{Name: g.Name, Components: make([]jsonComponent, 0, len(g.Components))}
		for _, c := range g.Components {
			jg.Components = append(jg.Components, jsonComponentOf(c, refs[next].Slug, p.ShowUptime))
			next++
		}
		out.Groups = append(out.Groups, jg)
	}
	for _, i := range p.ActiveIncidents {
		out.ActiveIncidents = append(out.ActiveIncidents, jsonIncidentOf(i))
	}
	for _, m := range p.Maintenance {
		out.Maintenance = append(out.Maintenance, jsonMaintenance{
			Key:   m.Key,
			Title: m.Title, Description: m.Description, StartsAt: m.StartsAt.UTC(), EndsAt: m.EndsAt.UTC(),
			Active: m.Active, Components: strs(m.Components),
		})
	}
	for _, i := range p.PastIncidents {
		out.PastIncidents = append(out.PastIncidents, jsonIncidentOf(i))
	}
	return json.Marshal(out)
}
