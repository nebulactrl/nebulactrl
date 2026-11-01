package statuspublic

import (
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/nebulactrl/nebulactrl/internal/statuspages"
)

// Geometry of the uptime bar in SVG user units: one cell per day, a 2 unit gap.
// A day without checks is a short stub at the foot of the bar, so it reads as
// missing by its shape, not only by its colour.
const (
	dayPitch   = 10
	dayWidth   = 8
	barHeight  = 36
	stubHeight = 12
)

// stateKey normalizes a state from the model to one this renderer has styles
// and labels for; anything else is "unknown".
func stateKey(s string) string {
	switch s {
	case statuspages.StateOperational, statuspages.StateDegraded, statuspages.StatePartialOutage,
		statuspages.StateMajorOutage, statuspages.StateMaintenance:
		return s
	}
	return statuspages.StateUnknown
}

// componentLabel is the text next to a component.
func componentLabel(state string) string {
	switch stateKey(state) {
	case statuspages.StateOperational:
		return "Operational"
	case statuspages.StateDegraded:
		return "Degraded performance"
	case statuspages.StatePartialOutage:
		return "Partial outage"
	case statuspages.StateMajorOutage:
		return "Major outage"
	case statuspages.StateMaintenance:
		return "Under maintenance"
	}
	return "No data"
}

// overallLabel is the banner's sentence.
func overallLabel(state string) string {
	switch stateKey(state) {
	case statuspages.StateOperational:
		return "All systems operational"
	case statuspages.StateDegraded:
		return "Degraded performance"
	case statuspages.StatePartialOutage:
		return "Partial outage"
	case statuspages.StateMajorOutage:
		return "Major outage"
	case statuspages.StateMaintenance:
		return "Maintenance in progress"
	}
	return "No status data yet"
}

// impactState maps an incident impact to the state whose colour it takes.
func impactState(impact string) string {
	switch impact {
	case "critical":
		return statuspages.StateMajorOutage
	case "major":
		return statuspages.StatePartialOutage
	case "minor":
		return statuspages.StateDegraded
	}
	return statuspages.StateMaintenance
}

func impactLabel(impact string) string {
	switch impact {
	case "critical":
		return "Critical"
	case "major":
		return "Major"
	case "minor":
		return "Minor"
	}
	return "Notice"
}

func incidentStatusLabel(status string) string {
	switch status {
	case "investigating":
		return "Investigating"
	case "identified":
		return "Identified"
	case "monitoring":
		return "Monitoring"
	case "resolved":
		return "Resolved"
	}
	return "Update"
}

// band is how a day is drawn: a flat cell (class c-<Key> in the stylesheet)
// and the word that stands in for the availability when the page hides numbers.
type band struct {
	Key  string // "up", "good", "warn", "down", "none", "maint"
	Word string
	// Status is the machine-readable name in status.json.
	Status string
	// Legend is the legend entry.
	Legend string
}

var (
	bandUp    = band{Key: "up", Word: "Operational", Status: "operational", Legend: "99.9% or more"}
	bandGood  = band{Key: "good", Word: "Minor issues", Status: "minor_issues", Legend: "99% or more"}
	bandWarn  = band{Key: "warn", Word: "Degraded", Status: "degraded", Legend: "95% or more"}
	bandDown  = band{Key: "down", Word: "Down", Status: "outage", Legend: "below 95%"}
	bandNone  = band{Key: "none", Word: "No data", Status: "no_data", Legend: "no data"}
	bandMaint = band{Key: "maint", Word: "Maintenance", Status: "maintenance", Legend: "maintenance"}

	allBands = []band{bandUp, bandGood, bandWarn, bandDown, bandNone, bandMaint}
)

// bandOf classifies a day. availability is a fraction (0..1); a day that is
// nothing but maintenance has none and is drawn as maintenance.
func bandOf(d statuspages.PublicDay) band {
	if d.Availability == nil {
		if d.Maintenance {
			return bandMaint
		}
		return bandNone
	}
	switch a := *d.Availability; {
	case a >= 0.999:
		return bandUp
	case a >= 0.99:
		return bandGood
	case a >= 0.95:
		return bandWarn
	}
	return bandDown
}

// percentValue converts an availability fraction to a percentage truncated
// (never rounded up) to two decimals, so 99.996% is shown as 99.99%, not 100%.
func percentValue(f float64) float64 {
	f = math.Min(math.Max(f, 0), 1)
	return math.Floor(f*10000+1e-9) / 100
}

// percentText formats an availability fraction: "100%", "99.9%", "99.95%".
func percentText(f float64) string {
	return strconv.FormatFloat(percentValue(f), 'f', -1, 64) + "%"
}

const timeLayout = "2 Jan 2006, 15:04 MST"

func fmtTime(t time.Time) string { return t.UTC().Format(timeLayout) }

func isoTime(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// dayDate is a day as the page words dates: "30 Oct 2026".
func dayDate(d statuspages.PublicDay) string {
	if t, err := time.Parse("2006-01-02", d.Date); err == nil {
		return t.Format("2 Jan 2006")
	}
	return d.Date
}

// dayNote is what the hover adds to a day's state: the estimated outage (only
// where figures are shown) and a mention of maintenance within the day.
func dayNote(d statuspages.PublicDay, showUptime bool) string {
	var parts []string
	if showUptime && d.DownMinutes > 0 {
		parts = append(parts, fmt.Sprintf("about %d min down", d.DownMinutes))
	}
	if d.Maintenance && d.Availability != nil {
		parts = append(parts, "some maintenance")
	}
	return strings.Join(parts, ", ")
}

// dayIncidents joins a day's incident titles one per line, for a data attribute.
func dayIncidents(d statuspages.PublicDay) string {
	titles := make([]string, len(d.Incidents))
	for i, t := range d.Incidents {
		titles[i] = strings.Join(strings.Fields(t), " ")
	}
	return strings.Join(titles, "\n")
}

// dayTitle is the native hover text of one day of a bar, which the page's
// script replaces by its own tooltip.
func dayTitle(d statuspages.PublicDay, b band, showUptime bool) string {
	var sb strings.Builder
	sb.WriteString(dayDate(d))
	sb.WriteString(": ")
	if showUptime && d.Availability != nil {
		sb.WriteString(percentText(*d.Availability))
		sb.WriteString(" uptime")
	} else {
		sb.WriteString(b.Word)
	}
	if note := dayNote(d, showUptime); note != "" {
		sb.WriteString(", ")
		sb.WriteString(note)
	}
	if len(d.Incidents) > 0 {
		sb.WriteString(", incidents: ")
		sb.WriteString(strings.Join(d.Incidents, "; "))
	}
	return sb.String()
}

// ---- view models handed to the template -----------------------------------

type stateView struct {
	Key   string
	Label string
}

// cellView is one day of a bar. Band, Date, State, Uptime, Note and Incidents
// are the data attributes the page's script draws its tooltip from; Title is
// the native tooltip without it. Uptime and the down minutes of Note are empty
// when the page hides its figures.
type cellView struct {
	X int
	// Y and H place the cell: the full height, or a stub for a day without data.
	Y, H      int
	Key       string
	Title     string
	Date      string
	State     string
	Uptime    string
	Note      string
	Incidents string
}

type barView struct {
	Cells []cellView
	Width int
	Days  int
	// Label is the accessible name of the whole bar, which is focusable.
	Label string
}

type componentView struct {
	Name string
	// URL is the https link of the name; empty for none.
	URL   string
	State stateView
	// HasUptime says whether the availability figures are shown; Uptime24 and
	// Uptime90 are each empty when that window has no data.
	HasUptime bool
	Uptime24  string
	Uptime90  string
	Bar       barView
	// Incident says an active incident names this component.
	Incident bool
}

type groupView struct {
	Name       string
	Components []componentView
}

type updateView struct {
	Status string
	Label  string
	Body   string
	At     string
	AtISO  string
}

type incidentView struct {
	// Anchor is the element id (and feed link fragment); empty without a key.
	Anchor     string
	Title      string
	Impact     stateView // key = the state whose colour the impact takes, label = impact
	Status     string
	Label      string
	Resolved   bool
	Started    string
	StartedISO string
	Ended      string
	EndedISO   string
	Components string
	Updates    []updateView
}

type maintenanceView struct {
	Anchor      string
	Title       string
	Description string
	Active      bool
	Starts      string
	StartsISO   string
	Ends        string
	EndsISO     string
	Components  string
}

type legendView struct {
	Key   string
	Label string
	// Stub draws the swatch as the bar draws a day without data.
	Stub bool
}

type pageView struct {
	Title       string
	Description string
	Footer      string
	Theme       string
	ColorScheme string
	ShowUptime  bool
	Overall     stateView
	Groups      []groupView
	Incidents   []incidentView
	Maintenance []maintenanceView
	Past        []incidentView
	Legend      []legendView
	Updated     string
	UpdatedISO  string
	// Age is the relative text next to the update time ("2 min ago"), computed
	// when the page is rendered.
	Age     string
	Refresh int
}

// refreshSeconds is the page's meta refresh: the page needs no script to stay current.
const refreshSeconds = 60

// relativeAge words how long before now t was.
func relativeAge(now, t time.Time) string {
	d := now.Sub(t)
	switch {
	case d < 45*time.Second:
		return "just now"
	case d < 90*time.Second:
		return "1 min ago"
	case d < time.Hour:
		return fmt.Sprintf("%d min ago", int(d.Round(time.Minute)/time.Minute))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d h ago", int(d.Round(time.Hour)/time.Hour))
	}
	return fmt.Sprintf("%d d ago", int(d/(24*time.Hour)))
}

// safeLink returns raw when it is an absolute https URL with a host, and ""
// otherwise: a component link never carries another scheme.
func safeLink(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return ""
	}
	return u.String()
}

// safeKey reduces an opaque key to characters that are safe in an element id,
// a URN and a URL fragment.
func safeKey(k string) string {
	var sb strings.Builder
	for _, r := range k {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			sb.WriteRune(r)
		default:
			sb.WriteByte('_')
		}
	}
	return sb.String()
}

func anchorOf(prefix, key string) string {
	if key == "" {
		return ""
	}
	return prefix + safeKey(key)
}

func buildView(p statuspages.PublicPage, now time.Time) pageView {
	theme := p.Theme
	if theme != "light" && theme != "dark" {
		theme = "auto"
	}
	scheme := "light dark"
	if theme != "auto" {
		scheme = theme
	}
	v := pageView{
		Title: p.Title, Description: p.Description, Footer: p.Footer, Theme: theme, ColorScheme: scheme,
		ShowUptime: p.ShowUptime,
		Overall:    stateView{Key: stateKey(p.Overall), Label: overallLabel(p.Overall)},
		Updated:    fmtTime(p.GeneratedAt), UpdatedISO: isoTime(p.GeneratedAt),
		Age:     relativeAge(now, p.GeneratedAt),
		Refresh: refreshSeconds,
	}
	// A component an active incident names says so beside its state: the
	// banner's incident must reach the row it is about, whatever the row's
	// uptime history (a new component reads "No data" while it is down).
	affected := map[string]bool{}
	for _, i := range p.ActiveIncidents {
		for _, name := range i.Components {
			affected[name] = true
		}
	}
	for _, g := range p.Groups {
		gv := groupView{Name: g.Name}
		for _, c := range g.Components {
			cv := buildComponent(c, p.ShowUptime)
			cv.Incident = affected[c.Name]
			gv.Components = append(gv.Components, cv)
		}
		v.Groups = append(v.Groups, gv)
	}
	for _, i := range p.ActiveIncidents {
		v.Incidents = append(v.Incidents, buildIncident(i))
	}
	for _, m := range p.Maintenance {
		v.Maintenance = append(v.Maintenance, maintenanceView{
			Anchor: anchorOf("mw-", m.Key),
			Title:  m.Title, Description: m.Description, Active: m.Active,
			Starts: fmtTime(m.StartsAt), StartsISO: isoTime(m.StartsAt),
			Ends: fmtTime(m.EndsAt), EndsISO: isoTime(m.EndsAt),
			Components: strings.Join(m.Components, ", "),
		})
	}
	for _, i := range p.PastIncidents {
		v.Past = append(v.Past, buildIncident(i))
	}
	for _, b := range allBands {
		v.Legend = append(v.Legend, legendView{Key: b.Key, Label: b.Legend, Stub: b == bandNone})
	}
	return v
}

func buildComponent(c statuspages.PublicComponent, showUptime bool) componentView {
	cv := componentView{
		Name:  c.Name,
		URL:   safeLink(c.URL),
		State: stateView{Key: stateKey(c.State), Label: componentLabel(c.State)},
	}
	if showUptime {
		if c.Availability24h != nil {
			cv.Uptime24 = percentText(*c.Availability24h)
		}
		if c.Availability != nil {
			cv.Uptime90 = percentText(*c.Availability)
		}
		cv.HasUptime = cv.Uptime24 != "" || cv.Uptime90 != ""
	}
	n := len(c.Days)
	bar := barView{Days: n, Width: max(n*dayPitch-(dayPitch-dayWidth), 0)}
	bar.Cells = make([]cellView, 0, n)
	down := 0
	for i, d := range c.Days {
		b := bandOf(d)
		down += d.DownMinutes
		cell := cellView{
			X: i * dayPitch, Y: 0, H: barHeight, Key: b.Key, Title: dayTitle(d, b, showUptime),
			Date: dayDate(d), State: b.Word, Note: dayNote(d, showUptime), Incidents: dayIncidents(d),
		}
		if b == bandNone {
			cell.Y, cell.H = barHeight-stubHeight, stubHeight
		}
		if showUptime && d.Availability != nil {
			cell.Uptime = percentText(*d.Availability)
		}
		bar.Cells = append(bar.Cells, cell)
	}
	switch {
	case showUptime && cv.Uptime90 != "":
		bar.Label = fmt.Sprintf("Last %d days: %s available, %d min down", n, cv.Uptime90, down)
	case showUptime:
		bar.Label = fmt.Sprintf("Last %d days: no data", n)
	default:
		bar.Label = fmt.Sprintf("Daily status over the last %d days", n)
	}
	cv.Bar = bar
	return cv
}

func buildIncident(i statuspages.PublicIncident) incidentView {
	iv := incidentView{
		Anchor:     anchorOf("inc-", i.Key),
		Title:      i.Title,
		Impact:     stateView{Key: impactState(i.Impact), Label: impactLabel(i.Impact)},
		Status:     i.Status,
		Label:      incidentStatusLabel(i.Status),
		Resolved:   i.Status == "resolved",
		Started:    fmtTime(i.StartedAt),
		StartedISO: isoTime(i.StartedAt),
		Components: strings.Join(i.Components, ", "),
	}
	if i.ResolvedAt != nil {
		iv.Resolved = true
		iv.Ended, iv.EndedISO = fmtTime(*i.ResolvedAt), isoTime(*i.ResolvedAt)
	}
	for _, u := range i.Updates {
		iv.Updates = append(iv.Updates, updateView{
			Status: u.Status, Label: incidentStatusLabel(u.Status), Body: u.Body, At: fmtTime(u.At), AtISO: isoTime(u.At),
		})
	}
	return iv
}
