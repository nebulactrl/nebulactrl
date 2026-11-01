package statuspublic

import (
	"encoding/xml"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/nebulactrl/nebulactrl/internal/statuspages"
)

// The Atom feed of a page: one entry per incident update and one per
// maintenance window, so a subscriber sees every statement the page makes.
// Entry ids are urn:nebulactrl:<key>:<n>, where the key is the opaque key of
// the incident or window and n numbers an incident's updates from the oldest
// (0), so an id never changes when later updates are posted.

type atomLink struct {
	Rel  string `xml:"rel,attr,omitempty"`
	Type string `xml:"type,attr,omitempty"`
	Href string `xml:"href,attr"`
}

type atomText struct {
	Type string `xml:"type,attr"`
	Text string `xml:",chardata"`
}

type atomEntry struct {
	ID        string   `xml:"id"`
	Title     string   `xml:"title"`
	Updated   string   `xml:"updated"`
	Published string   `xml:"published"`
	Link      atomLink `xml:"link"`
	Content   atomText `xml:"content"`

	updated time.Time // sort key
}

type atomFeed struct {
	XMLName  xml.Name    `xml:"http://www.w3.org/2005/Atom feed"`
	ID       string      `xml:"id"`
	Title    string      `xml:"title"`
	Subtitle string      `xml:"subtitle,omitempty"`
	Updated  string      `xml:"updated"`
	Links    []atomLink  `xml:"link"`
	Entries  []atomEntry `xml:"entry"`
}

func atomTime(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// feedKey is the key an incident or window goes by in the feed: its own, made
// URN-safe, or (for a model without one) a hash of its title and start.
func feedKey(key, title string, started time.Time) string {
	if k := safeKey(key); k != "" {
		return k
	}
	return "t" + strings.Trim(etagOf([]byte(title+"\x00"+atomTime(started))), `"`)
}

func joinLines(parts ...string) string {
	var out []string
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, "\n\n")
}

func affects(components []string) string {
	if len(components) == 0 {
		return ""
	}
	return "Affects: " + strings.Join(components, ", ")
}

// renderFeed renders the Atom feed of p, served on host.
func renderFeed(p statuspages.PublicPage, host string) ([]byte, error) {
	base := "https://" + host
	var entries []atomEntry

	incident := func(i statuspages.PublicIncident) {
		key := feedKey(i.Key, i.Title, i.StartedAt)
		link := base + "/"
		if a := anchorOf("inc-", i.Key); a != "" {
			link += "#" + a
		}
		ups := i.Updates
		if len(ups) == 0 { // an incident without a timeline still announces itself
			ups = []statuspages.PublicIncidentUpdate{{Status: i.Status, At: i.StartedAt}}
		}
		for idx, u := range ups {
			n := len(ups) - 1 - idx // updates arrive newest first; number from the oldest
			entries = append(entries, atomEntry{
				ID:      fmt.Sprintf("urn:nebulactrl:%s:%d", key, n),
				Title:   i.Title + ": " + incidentStatusLabel(u.Status),
				Updated: atomTime(u.At), Published: atomTime(u.At), updated: u.At,
				Link:    atomLink{Rel: "alternate", Type: "text/html", Href: link},
				Content: atomText{Type: "text", Text: joinLines(u.Body, impactLabel(i.Impact)+" impact. "+affects(i.Components))},
			})
		}
	}
	for _, i := range p.ActiveIncidents {
		incident(i)
	}
	for _, i := range p.PastIncidents {
		incident(i)
	}
	for _, m := range p.Maintenance {
		key := feedKey(m.Key, m.Title, m.StartsAt)
		link := base + "/"
		if a := anchorOf("mw-", m.Key); a != "" {
			link += "#" + a
		}
		state := "Scheduled"
		if m.Active {
			state = "In progress"
		}
		// Entries sort and show by when the window was posted or last changed,
		// never by its start: a window scheduled for next week is not from next
		// week. The window's own times are in the body.
		updated := m.UpdatedAt
		if updated.Before(m.PostedAt) {
			updated = m.PostedAt
		}
		entries = append(entries, atomEntry{
			ID:      fmt.Sprintf("urn:nebulactrl:%s:%d", key, 0),
			Title:   "Maintenance: " + m.Title + " (" + state + ")",
			Updated: atomTime(updated), Published: atomTime(m.PostedAt), updated: updated,
			Link:    atomLink{Rel: "alternate", Type: "text/html", Href: link},
			Content: atomText{Type: "text", Text: joinLines(m.Description, "From "+fmtTime(m.StartsAt)+" to "+fmtTime(m.EndsAt)+".", affects(m.Components))},
		})
	}
	sort.SliceStable(entries, func(a, b int) bool {
		if !entries[a].updated.Equal(entries[b].updated) {
			return entries[a].updated.After(entries[b].updated)
		}
		return entries[a].ID < entries[b].ID
	})

	updated := p.GeneratedAt
	if len(entries) > 0 && entries[0].updated.After(updated) {
		updated = entries[0].updated
	}
	feed := atomFeed{
		ID: base + "/", Title: p.Title, Subtitle: strings.Join(strings.Fields(p.Description), " "),
		Updated: atomTime(updated),
		Links: []atomLink{
			{Rel: "self", Type: "application/atom+xml", Href: base + "/feed.atom"},
			{Rel: "alternate", Type: "text/html", Href: base + "/"},
		},
		Entries: entries,
	}
	body, err := xml.MarshalIndent(feed, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("render status feed: %w", err)
	}
	return append([]byte(xml.Header), append(body, '\n')...), nil
}
