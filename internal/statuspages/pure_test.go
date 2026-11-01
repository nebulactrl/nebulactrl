package statuspages

import (
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/config"
	"github.com/nebulactrl/nebulactrl/internal/systemroute"
)

func requireAPIError(t *testing.T, err error, status int, code string) {
	t.Helper()
	var ae *apierr.Error
	require.ErrorAs(t, err, &ae, "want an API error")
	require.Equal(t, status, ae.Status, ae.Message)
	require.Equal(t, code, ae.Code, ae.Message)
}

func TestOverallState(t *testing.T) {
	const (
		op, deg, down, maint, unk = StateOperational, StateDegraded, StateMajorOutage, StateMaintenance, StateUnknown
	)
	cases := []struct {
		name       string
		components []string
		impacts    []string
		window     bool
		want       string
	}{
		{"all operational", []string{op, op, op}, nil, false, StateOperational},
		{"no components", nil, nil, false, StateUnknown},
		{"one of three down is a partial outage", []string{op, down, op}, nil, false, StatePartialOutage},
		{"every component down is a major outage", []string{down, down}, nil, false, StateMajorOutage},
		{"the only component down is a major outage", []string{down}, nil, false, StateMajorOutage},
		{"degraded", []string{op, deg}, nil, false, StateDegraded},
		{"down beats degraded", []string{deg, down, op}, nil, false, StatePartialOutage},
		{"maintenance when nothing is worse", []string{op, maint}, nil, false, StateMaintenance},
		{"degraded beats maintenance", []string{maint, deg}, nil, false, StateDegraded},
		{"down beats maintenance", []string{maint, down}, nil, false, StatePartialOutage},
		{"unknown components do not worsen the page", []string{op, unk}, nil, false, StateOperational},
		{"all unknown", []string{unk, unk}, nil, false, StateUnknown},
		{"an unknown component is not a down one", []string{unk, down}, nil, false, StatePartialOutage},
		{"critical incident", []string{op}, []string{ImpactCritical}, false, StateMajorOutage},
		{"major incident", []string{op}, []string{ImpactMajor}, false, StatePartialOutage},
		{"minor incident", []string{op}, []string{ImpactMinor}, false, StateDegraded},
		{"incident without impact", []string{op}, []string{ImpactNone}, false, StateOperational},
		{"worst incident wins", []string{op}, []string{ImpactMinor, ImpactCritical, ImpactMajor}, false, StateMajorOutage},
		{"a minor incident does not soften a partial outage", []string{op, down, op}, []string{ImpactMinor}, false, StatePartialOutage},
		{"a critical incident worsens a partial outage", []string{op, down, op}, []string{ImpactCritical}, false, StateMajorOutage},
		{"an active window alone is maintenance", []string{op}, nil, true, StateMaintenance},
		{"an active window with no data is maintenance", []string{unk}, nil, true, StateMaintenance},
		{"an incident beats the window", []string{op}, []string{ImpactMinor}, true, StateDegraded},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, overallState(tc.components, tc.impacts, tc.window))
		})
	}
}

func TestComponentStateMapping(t *testing.T) {
	for in, want := range map[string]string{
		"up": StateOperational, "degraded": StateDegraded, "down": StateMajorOutage, "maintenance": StateMaintenance,
		"paused": StateUnknown, "unknown": StateUnknown, "": StateUnknown, "anything else": StateUnknown,
	} {
		require.Equal(t, want, componentState(in), in)
	}
}

func TestPublicComponentStateOfAGoneServer(t *testing.T) {
	t0 := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	ago := func(d time.Duration) *time.Time { ts := t0.Add(-d); return &ts }
	cases := []struct {
		name     string
		state    string
		lastSeen *time.Time
		inMaint  bool
		started  time.Time
		want     string
	}{
		{"a stored verdict wins", "up", ago(10 * time.Minute), false, time.Time{}, StateOperational},
		{"a stored outage stays one", "down", ago(10 * time.Minute), false, time.Time{}, StateMajorOutage},
		{"no server", "unknown", nil, false, time.Time{}, StateUnknown},
		{"inside the grace", "unknown", ago(5*time.Minute - time.Second), false, time.Time{}, StateUnknown},
		{"gone for the grace", "unknown", ago(5 * time.Minute), false, time.Time{}, StateMajorOutage},
		{"gone under maintenance", "unknown", ago(5 * time.Minute), true, time.Time{}, StateMaintenance},
		{"paused is no data", "paused", ago(time.Hour), false, time.Time{}, StateUnknown},
		{"control plane just started", "unknown", ago(time.Hour), false, t0.Add(-2 * time.Minute), StateUnknown},
		{"control plane up for the grace", "unknown", ago(time.Hour), false, t0.Add(-5 * time.Minute), StateMajorOutage},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			started := tc.started
			if started.IsZero() {
				started = t0.Add(-time.Hour)
			}
			require.Equal(t, tc.want, publicComponentState(tc.state, tc.lastSeen, tc.inMaint, t0, started))
		})
	}
}

func TestAvailability(t *testing.T) {
	require.Nil(t, usage{}.availability(), "no checks: no data")
	require.Nil(t, usage{checks: 10, maintenance: 10}.availability(), "only maintenance checks: no data, not 0%")

	a := usage{checks: 100, up: 90, degraded: 6, down: 4}.availability()
	require.NotNil(t, a)
	require.InDelta(t, 0.96, *a, 1e-9, "degraded counts as available")

	a = usage{checks: 100, up: 60, maintenance: 40}.availability()
	require.NotNil(t, a)
	require.InDelta(t, 1.0, *a, 1e-9, "maintenance checks leave numerator and denominator")

	a = usage{checks: 4, up: 9}.availability()
	require.NotNil(t, a)
	require.InDelta(t, 1.0, *a, 1e-9, "inconsistent rows are clamped")

	var sum usage
	sum.add(usage{checks: 100, up: 96, down: 4})
	sum.add(usage{checks: 100, up: 60, maintenance: 40})
	require.Equal(t, usage{checks: 200, up: 156, down: 4, maintenance: 40}, sum)
}

func TestBuildDaysCoversNinetyDaysEndingToday(t *testing.T) {
	today := time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)
	days := buildDays(today,
		map[string]usage{
			"2026-10-28": {checks: 10, up: 10},
			"2026-10-27": {checks: 100, up: 90, degraded: 6, down: 4, maintenance: 0},
			"2026-10-26": {checks: 50, up: 10, maintenance: 40},
			"2026-10-25": {checks: 5, maintenance: 5},
		},
		map[string][]string{"2026-10-27": {"API errors"}},
	)
	require.Len(t, days, UptimeDays)
	require.Equal(t, "2026-10-28", days[UptimeDays-1].Date)
	require.Equal(t, "2026-07-31", days[0].Date, "89 days before today")

	last := days[UptimeDays-1]
	require.InDelta(t, 1.0, *last.Availability, 1e-9)
	require.Zero(t, last.DownMinutes)

	y := days[UptimeDays-2]
	require.InDelta(t, 0.96, *y.Availability, 1e-9)
	require.Equal(t, 2, y.DownMinutes, "4 failed checks of 30 s")
	require.Equal(t, []string{"API errors"}, y.Incidents)
	require.False(t, y.Maintenance)

	m := days[UptimeDays-3]
	require.True(t, m.Maintenance)
	require.InDelta(t, 1.0, *m.Availability, 1e-9)

	onlyMaint := days[UptimeDays-4]
	require.True(t, onlyMaint.Maintenance)
	require.Nil(t, onlyMaint.Availability)

	empty := days[0]
	require.Nil(t, empty.Availability, "a day without rows has no data")
	require.Zero(t, empty.DownMinutes)
	require.False(t, empty.Maintenance)
	require.Empty(t, empty.Incidents)
}

func TestIncidentDayKeys(t *testing.T) {
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	first := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 10, 26, 3, 0, 0, 0, time.UTC)

	// Resolved: its days from start through the resolution day.
	require.Equal(t, []string{"2026-10-24", "2026-10-25", "2026-10-26"},
		incidentDayKeys(time.Date(2026, 10, 24, 23, 59, 0, 0, time.UTC), &end, now, first))
	// Still open: up to today.
	require.Equal(t, []string{"2026-10-27", "2026-10-28"},
		incidentDayKeys(time.Date(2026, 10, 27, 1, 0, 0, 0, time.UTC), nil, now, first))
	// Started before the window: clipped to it.
	keys := incidentDayKeys(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), &end, now, first)
	require.Equal(t, "2026-10-20", keys[0])
	require.Equal(t, "2026-10-26", keys[len(keys)-1])
}

func TestNormalizeHost(t *testing.T) {
	for in, want := range map[string]string{
		"status.example.com":      "status.example.com",
		" Status.Example.COM ":    "status.example.com", //nolint:gocritic // the padded mixed-case key deliberately checks that normalizeHost trims and lowercases
		"status.example.com:8081": "status.example.com",
		"status.example.com.":     "status.example.com",
		"":                        "",
	} {
		require.Equal(t, want, normalizeHost(in), in)
	}
}

func TestValidateHostnameNormalizesValidatesAndReservesTheDashboardHost(t *testing.T) {
	systemroute.Configure(&config.Config{PublicURL: "https://nebula.example.com", Installer: config.Installer{DashboardUpstream: "nebulactrl:8080"}})
	t.Cleanup(func() { systemroute.Configure(nil) })

	h, err := ValidateHostname(" Status.Example.COM. ")
	require.NoError(t, err)
	require.Equal(t, "status.example.com", h)

	for _, bad := range []string{"", "localhost", "203.0.113.9", "-a.example.com", "a b.example.com", "a_b.example.com", "*.example.com", "https://status.example.com", strings.Repeat("a", 64) + ".example.com"} {
		_, err := ValidateHostname(bad)
		requireAPIError(t, err, 422, "invalid_hostname")
	}

	_, err = ValidateHostname("nebula.example.com")
	requireAPIError(t, err, 409, "hostname_reserved")
	_, err = ValidateHostname("NEBULA.example.com.")
	requireAPIError(t, err, 409, "hostname_reserved")
}

func TestCheckPublishable(t *testing.T) {
	host := "status.example.com"
	empty := ""
	requireAPIError(t, checkPublishable(nil, 3), 422, "publish_needs_hostname")
	requireAPIError(t, checkPublishable(&empty, 3), 422, "publish_needs_hostname")
	requireAPIError(t, checkPublishable(&host, 0), 422, "publish_needs_components")
	require.NoError(t, checkPublishable(&host, 1))
}

func TestSlugs(t *testing.T) {
	for in, want := range map[string]string{
		"Acme status":              "acme-status",
		"  API & Web -- status!  ": "api-web-status",
		"Ünïcode Dienst":           "n-code-dienst",
		"!!!":                      "status",
		"":                         "status",
	} {
		require.Equal(t, want, slugify(in), in)
	}
	require.LessOrEqual(t, len(slugify(strings.Repeat("ab ", 40))), maxSlugLen)
	require.False(t, strings.HasSuffix(slugify(strings.Repeat("abcdefghi ", 10)), "-"))

	require.Equal(t, "acme", freeSlug("acme", map[string]bool{}))
	require.Equal(t, "acme-2", freeSlug("acme", map[string]bool{"acme": true}))
	require.Equal(t, "acme-3", freeSlug("acme", map[string]bool{"acme": true, "acme-2": true}))
	long := strings.Repeat("a", maxSlugLen)
	got := freeSlug(long, map[string]bool{long: true})
	require.LessOrEqual(t, len(got), maxSlugLen)
	require.True(t, strings.HasSuffix(got, "-2"))

	for _, ok := range []string{"a", "acme-status", "a1-b2", "9"} {
		_, err := checkSlug(ok)
		require.NoError(t, err, ok)
	}
	for _, bad := range []string{"", "-a", "a-", "a--b", "A", "a_b", "a b", strings.Repeat("a", maxSlugLen+1)} {
		_, err := checkSlug(bad)
		requireAPIError(t, err, 422, "invalid_slug")
	}
}

func TestTextCleaning(t *testing.T) {
	got, err := cleanLine("title", "  Hello  ", 1, 10)
	require.NoError(t, err)
	require.Equal(t, "Hello", got)
	_, err = cleanLine("title", "   ", 1, 10)
	requireAPIError(t, err, 422, "invalid_title")
	_, err = cleanLine("title", "12345678901", 1, 10)
	requireAPIError(t, err, 422, "invalid_title")
	_, err = cleanLine("title", "two\nlines", 1, 10)
	requireAPIError(t, err, 422, "invalid_title")
	_, err = cleanLine("display_name", "nul\x00", 1, 10)
	requireAPIError(t, err, 422, "invalid_display_name")
	got, err = cleanLine("title", strings.Repeat("é", 10), 1, 10)
	require.NoError(t, err, "the limit counts characters, not bytes")
	require.Equal(t, strings.Repeat("é", 10), got)

	got, err = cleanText("body", "line one\r\nline two\tindented \n", 1, 100)
	require.NoError(t, err)
	require.Equal(t, "line one\nline two\tindented", got)
	_, err = cleanText("body", "bell\x07", 1, 100)
	requireAPIError(t, err, 422, "invalid_body")
	_, err = cleanText("body", "", 1, 100)
	requireAPIError(t, err, 422, "invalid_body")
	got, err = cleanText("description", "", 0, 100)
	require.NoError(t, err)
	require.Empty(t, got)
}

func TestResolvedAtFor(t *testing.T) {
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	earlier := now.Add(-time.Hour)
	require.Nil(t, resolvedAtFor(IncidentInvestigating, nil, now))
	require.Nil(t, resolvedAtFor(IncidentMonitoring, &earlier, now), "a reopened incident is no longer resolved")
	require.Equal(t, now, *resolvedAtFor(IncidentResolved, nil, now))
	require.Equal(t, earlier, *resolvedAtFor(IncidentResolved, &earlier, now), "resolving twice keeps the first time")
}

func TestPhaseOfAndWindowTimes(t *testing.T) {
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	canceled := now.Add(-time.Minute)
	require.Equal(t, PhaseUpcoming, PhaseOf(nil, now.Add(time.Hour), now.Add(2*time.Hour), now))
	require.Equal(t, PhaseActive, PhaseOf(nil, now, now.Add(time.Hour), now), "a window starts at startsAt")
	require.Equal(t, PhaseActive, PhaseOf(nil, now.Add(-time.Hour), now.Add(time.Second), now))
	require.Equal(t, PhaseEnded, PhaseOf(nil, now.Add(-2*time.Hour), now, now), "a window ends at endsAt")
	require.Equal(t, PhaseCanceled, PhaseOf(&canceled, now.Add(-time.Hour), now.Add(time.Hour), now))
	require.Equal(t, PhaseCanceled, PhaseOf(&canceled, now.Add(-3*time.Hour), now.Add(-2*time.Hour), now), "canceled wins over ended")

	requireAPIError(t, checkWindowTimes(now.Add(time.Hour), now.Add(time.Hour), now), 422, "invalid_window")
	requireAPIError(t, checkWindowTimes(now.Add(2*time.Hour), now.Add(time.Hour), now), 422, "invalid_window")
	requireAPIError(t, checkWindowTimes(now.Add(-2*time.Hour), now.Add(-time.Hour), now), 422, "window_in_past")
	require.NoError(t, checkWindowTimes(now.Add(-time.Hour), now.Add(time.Hour), now), "a running window may be created")
}

func TestEvaluateDNS(t *testing.T) {
	v4, v6 := net.ParseIP("203.0.113.10"), net.ParseIP("2001:db8::10")
	other := net.ParseIP("198.51.100.7")
	notFound := &net.DNSError{Err: "no such host", Name: "status.example.com", IsNotFound: true}
	timeout := &net.DNSError{Err: "i/o timeout", Name: "status.example.com", IsTimeout: true}
	host := "status.example.com"

	st, msg := evaluateDNS(host, nil, nil, nil)
	require.Equal(t, DNSUnknown, st, "no server address to compare with")
	require.Contains(t, msg, "no public IP")

	st, _ = evaluateDNS(host, []net.IP{v4}, nil, notFound)
	require.Equal(t, DNSMissing, st)
	st, _ = evaluateDNS(host, []net.IP{v4}, nil, nil)
	require.Equal(t, DNSMissing, st, "an empty answer is missing too")
	st, _ = evaluateDNS(host, []net.IP{v4}, nil, timeout)
	require.Equal(t, DNSUnknown, st, "a failing lookup proves nothing")
	st, _ = evaluateDNS(host, []net.IP{v4}, nil, errors.New("boom"))
	require.Equal(t, DNSUnknown, st)

	st, _ = evaluateDNS(host, []net.IP{v4}, []net.IP{v4}, nil)
	require.Equal(t, DNSOK, st)
	st, _ = evaluateDNS(host, []net.IP{v4, v6}, []net.IP{v6, v4, v4}, nil)
	require.Equal(t, DNSOK, st, "order and duplicates do not matter")

	st, msg = evaluateDNS(host, []net.IP{v4}, []net.IP{other}, nil)
	require.Equal(t, DNSMismatch, st)
	require.Contains(t, msg, "198.51.100.7")
	require.Contains(t, msg, "203.0.113.10")

	st, msg = evaluateDNS(host, []net.IP{v4}, []net.IP{v4, other}, nil)
	require.Equal(t, DNSMismatch, st, "a stray record to another server breaks some visitors")
	require.Contains(t, msg, "198.51.100.7")

	st, _ = evaluateDNS(host, []net.IP{v4, v6}, []net.IP{v4}, nil)
	require.Equal(t, DNSMismatch, st, "a missing AAAA record")
}

func TestParseIPs(t *testing.T) {
	require.Empty(t, parseIPs(""))
	require.Empty(t, parseIPs("not an ip"))
	got := parseIPs("203.0.113.10, 2001:db8::1 junk")
	require.Equal(t, []string{"2001:db8::1", "203.0.113.10"}, ipStrings(got), "ipStrings sorts as strings")
}
