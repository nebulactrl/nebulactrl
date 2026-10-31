package statuspublic

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"time"

	"github.com/nebulactrl/nebulactrl/internal/statuspages"
)

// clock is a settable time source shared by the handler under test.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *clock { return &clock{t: time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)} }

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// fakeSource is a Source over a map of pages, counting lookups per host.
type fakeSource struct {
	mu    sync.Mutex
	pages map[string]statuspages.PublicPage
	calls map[string]int
	err   error
	// block, when set, holds every lookup until it is closed.
	block chan struct{}
}

func newSource(pages map[string]statuspages.PublicPage) *fakeSource {
	return &fakeSource{pages: pages, calls: map[string]int{}}
}

func (s *fakeSource) Public(ctx context.Context, host string) (statuspages.PublicPage, error) {
	s.mu.Lock()
	s.calls[host]++
	block, err := s.block, s.err
	p, ok := s.pages[host]
	s.mu.Unlock()
	if block != nil {
		<-block
	}
	if err != nil {
		return statuspages.PublicPage{}, err
	}
	if !ok {
		return statuspages.PublicPage{}, statuspages.ErrNotFound
	}
	return p, nil
}

func (s *fakeSource) count(host string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls[host]
}

func (s *fakeSource) set(host string, p statuspages.PublicPage) {
	s.mu.Lock()
	s.pages[host] = p
	s.mu.Unlock()
}

func (s *fakeSource) fail(err error) {
	s.mu.Lock()
	s.err = err
	s.mu.Unlock()
}

// fixtureDays are 90 days ending 2026-10-29: a day of every band and the rest up.
func fixtureDays(allUp bool) []statuspages.PublicDay {
	end := time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC)
	days := make([]statuspages.PublicDay, 90)
	for i := range days {
		d := statuspages.PublicDay{Date: end.AddDate(0, 0, i-89).Format("2006-01-02"), Availability: new(1.0)}
		if !allUp {
			switch i {
			case 0:
				d.Availability = nil // no data yet
			case 1:
				d.Availability, d.Maintenance = nil, true // a day of maintenance only
			case 2:
				d.Availability, d.DownMinutes, d.Incidents = new(0.9), 144, []string{"Database failover"}
			case 3:
				d.Availability, d.DownMinutes = new(0.97), 43
			case 4:
				d.Availability, d.DownMinutes = new(0.995), 7
			case 5:
				d.Availability, d.Maintenance = new(0.9995), true
			}
		}
		days[i] = d
	}
	return days
}

// fixturePage is a page with every section: grouped and ungrouped components
// in several states, an active incident with a timeline, active and upcoming
// maintenance and a resolved incident.
func fixturePage(theme string) statuspages.PublicPage {
	at := func(h int) time.Time { return time.Date(2026, 10, 29, h, 0, 0, 0, time.UTC) }
	resolved := at(9)
	return statuspages.PublicPage{
		Title:       "Acme Status",
		Description: "Live status of Acme services.\nQuestions: status@acme.example",
		Footer:      "Acme Inc.\nContact: status@acme.example <b>",
		Theme:       theme,
		ShowUptime:  true,
		Overall:     statuspages.StatePartialOutage,
		Groups: []statuspages.PublicGroup{
			{Name: "", Components: []statuspages.PublicComponent{
				{Name: "API", URL: "https://api.acme.example/docs", State: statuspages.StateOperational, Availability24h: new(0.9998), Availability: new(0.99996), Days: fixtureDays(true)},
			}},
			{Name: "Web", Components: []statuspages.PublicComponent{
				{Name: "Dashboard", URL: "javascript:alert(1)", State: statuspages.StateMajorOutage, Availability24h: new(0.95), Availability: new(0.9873), Days: fixtureDays(false)},
				{Name: "Docs", State: statuspages.StateMaintenance, Days: fixtureDays(true)},
				{Name: "Search", State: statuspages.StateDegraded, Availability24h: new(1.0), Availability: new(0.9995), Days: fixtureDays(true)},
				{Name: "Mail", State: "something-new", Days: nil},
			}},
		},
		ActiveIncidents: []statuspages.PublicIncident{{
			Key: "k-inc-1", Title: "Dashboard unavailable", Impact: "critical", Status: "identified",
			StartedAt: at(10), Components: []string{"Dashboard"},
			Updates: []statuspages.PublicIncidentUpdate{
				{Status: "identified", Body: "The cause is a failed migration.\nRolling back.", At: at(11)},
				{Status: "investigating", Body: "We are looking into it.", At: at(10)},
			},
		}},
		Maintenance: []statuspages.PublicMaintenance{
			{Key: "k-mw-1", Title: "Docs reindex", Description: "Search is read only.", StartsAt: at(11), EndsAt: at(13), PostedAt: at(8), UpdatedAt: at(8), Active: true, Components: []string{"Docs"}},
			{Key: "k-mw-2", Title: "Database upgrade", StartsAt: at(11).AddDate(0, 0, 3), EndsAt: at(13).AddDate(0, 0, 3), PostedAt: at(9), UpdatedAt: at(9), Components: []string{"API", "Dashboard"}},
		},
		PastIncidents: []statuspages.PublicIncident{{
			Key: "k-inc-2", Title: "Slow API", Impact: "minor", Status: "resolved",
			StartedAt: at(7), ResolvedAt: &resolved, Components: []string{"API"},
			Updates: []statuspages.PublicIncidentUpdate{{Status: "resolved", Body: "Fixed.", At: resolved}},
		}},
		GeneratedAt: at(12),
	}
}

// newTestHandler builds a handler over pages with a shared clock. Fonts are
// absent unless the test passes its own.
func newTestHandler(src Source, clk *clock, mut func(*Options)) *Handler {
	o := Options{
		Pages: src, Now: clk.now,
		Fonts: func(string) ([]byte, bool) { return nil, false },
	}
	if mut != nil {
		mut(&o)
	}
	return New(o)
}

// get performs a GET as if the proxy forwarded it: the peer is private, so the
// last X-Forwarded-For entry is the client.
func get(h http.Handler, host, path string, hdr ...string) *httptest.ResponseRecorder {
	return do(h, http.MethodGet, host, path, hdr...)
}

func do(h http.Handler, method, host, path string, hdr ...string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, nil)
	r.Host = host // set directly: tests use hosts that are not valid in a URL
	r.RemoteAddr = "10.0.0.9:5555"
	r.Header.Set("X-Forwarded-For", "198.51.100.1")
	for i := 0; i+1 < len(hdr); i += 2 {
		r.Header.Set(hdr[i], hdr[i+1])
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
