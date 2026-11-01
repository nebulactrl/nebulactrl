//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/notifications"
	"github.com/nebulactrl/nebulactrl/internal/statusroute"
)

// The uptime tests run the whole path: the harness agent probes the service's
// health check every 2 s (NEBULACTRL_UPTIME_INTERVAL, harness_timers_test.go),
// sends the results, the control plane keeps the state, the outages and the
// alerts. A service is broken by stopping its process with SIGSTOP
// (freezeService): the container stays "running" (so the service is not
// "paused" in the uptime sense), the app hangs, and every probe times out.

// ---- helpers -----------------------------------------------------------------

// uptimeReport fetches GET /services/{id}/uptime (24h).
func (s *Stack) uptimeReport(id string) J {
	s.t.Helper()
	r := s.Do("GET", "/services/"+id+"/uptime", nil)
	require.Equal(s.t, http.StatusOK, r.Status, string(r.Raw))
	return r.JSON()
}

// jlist returns the objects of the array under key.
func jlist(j J, key string) []J {
	arr, _ := j[key].([]any)
	out := make([]J, 0, len(arr))
	for _, v := range arr {
		m, _ := v.(map[string]any)
		out = append(out, J(m))
	}
	return out
}

// seriesTotal adds field (checks, up, down, maintenance, ...) over the report's series.
func seriesTotal(rep J, field string) int {
	n := 0
	for _, p := range jlist(rep, "series") {
		n += p.Int(field)
	}
	return n
}

// availability24h is the report's 24 hour availability in percent.
func availability24h(rep J) (float64, bool) {
	v, ok := rep.Obj("availability")["24h"].(float64)
	return v, ok
}

// openIncident reports whether the report lists an unresolved outage.
func openIncident(rep J) bool {
	for _, i := range jlist(rep, "incidents") {
		if i["resolvedAt"] == nil {
			return true
		}
	}
	return false
}

func parseReportTime(t *testing.T, raw string) time.Time {
	t.Helper()
	ts, err := time.Parse(time.RFC3339Nano, raw)
	require.NoError(t, err, "time %q", raw)
	return ts
}

// incidentRows counts the uptime_incidents rows of a service; openOnly counts the unresolved ones.
func (s *Stack) incidentRows(serviceID string, openOnly bool) int {
	s.t.Helper()
	q := `SELECT count(*) FROM uptime_incidents WHERE service_id = $1`
	if openOnly {
		q += ` AND resolved_at IS NULL`
	}
	var n int
	require.NoError(s.t, s.Pool.QueryRow(context.Background(), q, serviceID).Scan(&n))
	return n
}

// freezeService stops the main process of the service's only container with
// SIGSTOP: the app hangs (an http probe times out) while Docker still reports
// the container as running. `docker pause` would not do: the agent thaws any
// paused service container no backup owns on every reconcile (volbackup's
// Sweep), which other tests trigger all the time on the shared stack. The
// process is continued at cleanup, before the stack deletes the service.
func freezeService(t *testing.T, s *Stack, serviceID string) (thaw func()) {
	t.Helper()
	cs := s.RunningContainers(serviceID)
	require.Len(t, cs, 1)
	name := cs[0].Name
	_, err := sh(context.Background(), "", "docker", "kill", "--signal", "SIGSTOP", name)
	require.NoError(t, err)
	cont := func() { _, _ = sh(context.Background(), "", "docker", "kill", "--signal", "SIGCONT", name) }
	t.Cleanup(cont)
	return func() {
		t.Helper()
		_, err := sh(context.Background(), "", "docker", "kill", "--signal", "SIGCONT", name)
		require.NoError(t, err)
	}
}

// hookEvent is one webhook delivery as the receiver saw it.
type hookEvent struct {
	Type, Severity, Title, ServiceID string
	SignatureOK                      bool
}

// webhookSink is a local webhook receiver plus a webhook channel of the
// test's project that delivers uptime.down and uptime.up to it.
type webhookSink struct {
	srv       *httptest.Server
	channelID string

	mu     sync.Mutex
	secret string
	events []hookEvent
}

func newWebhookSink(t *testing.T, s *Stack) *webhookSink {
	t.Helper()
	h := &webhookSink{}
	h.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		var b struct {
			Type      string `json:"type"`
			Severity  string `json:"severity"`
			Title     string `json:"title"`
			ServiceID string `json:"serviceId"`
			Test      bool   `json:"test"`
		}
		if json.Unmarshal(body, &b) != nil || b.Test {
			w.WriteHeader(http.StatusOK)
			return
		}
		h.mu.Lock()
		secret := h.secret
		sigOK := signatureOK(secret, r.Header.Get(notifications.SignatureHeader), body)
		h.events = append(h.events, hookEvent{Type: b.Type, Severity: b.Severity, Title: b.Title, ServiceID: b.ServiceID, SignatureOK: sigOK})
		h.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(h.srv.Close)

	r := s.Do("POST", "/notifications/channels", map[string]any{
		"name":    "uptime-" + s.hostBase,
		"kind":    "webhook",
		"secrets": map[string]any{"url": h.srv.URL},
		"rule": map[string]any{
			"eventTypes":  []string{"uptime.down", "uptime.up"},
			"allProjects": false,
			"projectIds":  []string{s.ProjectID},
		},
	})
	require.Equal(t, http.StatusCreated, r.Status, string(r.Raw))
	h.channelID = r.JSON().Obj("channel").Str("id")
	h.mu.Lock()
	h.secret = r.JSON().Str("signingSecret")
	h.mu.Unlock()
	require.NotEmpty(t, h.channelID)
	require.NotEmpty(t, h.secret)
	t.Cleanup(func() { s.Do("DELETE", "/notifications/channels/"+h.channelID, nil) })
	return h
}

// received returns the events of one type and service (serviceID "" matches any), oldest first.
func (h *webhookSink) received(eventType, serviceID string) []hookEvent {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []hookEvent
	for _, e := range h.events {
		if (eventType == "" || e.Type == eventType) && (serviceID == "" || e.ServiceID == serviceID) {
			out = append(out, e)
		}
	}
	return out
}

// order returns the types of all received events, in arrival order.
func (h *webhookSink) order() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]string, len(h.events))
	for i, e := range h.events {
		out[i] = e.Type
	}
	return out
}

// setMonitor sets the failure and recovery thresholds of a service's uptime monitor (alerts on).
func (s *Stack) setMonitor(serviceID string, failure, recovery int) {
	s.t.Helper()
	r := s.Do("PUT", "/services/"+serviceID+"/uptime-monitor", map[string]any{
		"enabled": true, "alerts": true, "failureThreshold": failure, "recoveryThreshold": recovery, "reminderMinutes": 0,
	})
	require.Equal(s.t, http.StatusOK, r.Status, string(r.Raw))
}

// uptimeService deploys the fixture app with a 2 s probe timeout (a frozen
// container then fails a probe in 2 s) and returns it once it is deployed.
func uptimeService(t *testing.T, s *Stack) Service {
	t.Helper()
	app := appFixture(t)
	svc := s.NewService(SvcSpec{
		Name: "web", Image: app.AppImage, Vars: map[string]string{"VERSION": "v1"},
		Health: map[string]any{"timeoutSeconds": 2},
	})
	s.DeployOK(svc.ID)
	return svc
}

// waitUptimeUp waits for the first passing checks of a service.
func waitUptimeUp(t *testing.T, s *Stack, serviceID string) J {
	t.Helper()
	var rep J
	require.Truef(t, eventually(90*time.Second, time.Second, func() bool {
		rep = s.uptimeReport(serviceID)
		return rep.Str("state") == "up" && seriesTotal(rep, "checks") > 0
	}), "the service never showed state up with checks: %v", rep)
	return rep
}

// ---- tests -------------------------------------------------------------------

// TestUptimeDetectsOutageAndRecovery: a frozen container fails the agent's http
// probes, the control plane opens an outage after three failed checks and sends
// uptime.down once, and once the container runs again two passing checks
// resolve the outage and send uptime.up once. The 24 h availability sits
// strictly between 0 and 100 %.
func TestUptimeDetectsOutageAndRecovery(t *testing.T) {
	requireTier(t, tierFull)
	t.Parallel()
	s := SharedStack(t)
	hook := newWebhookSink(t, s)
	svc := uptimeService(t, s)

	// ---- up -----------------------------------------------------------------
	rep := waitUptimeUp(t, s, svc.ID)
	require.Empty(t, jlist(rep, "incidents"), "no outage before anything broke")
	require.Zero(t, s.incidentRows(svc.ID, false))
	require.Equal(t, 3, rep.Int("failureThreshold"), "the default monitor")

	// ---- outage -------------------------------------------------------------
	thaw := freezeService(t, s, svc.ID)
	require.Truef(t, eventually(2*time.Minute, time.Second, func() bool {
		rep = s.uptimeReport(svc.ID)
		return rep.Str("state") == "down" && openIncident(rep)
	}), "no open outage after the container froze: %v", rep)
	require.NotEmpty(t, rep.Str("lastError"), "the failing check says why")
	require.Equal(t, 1, s.incidentRows(svc.ID, true), "one open uptime_incidents row")
	incs := jlist(rep, "incidents")
	require.Len(t, incs, 1)
	require.NotEmpty(t, incs[0].Str("cause"))
	require.True(t, incs[0].Bool("notified"), "uptime.down was sent for the outage")

	require.Truef(t, eventually(60*time.Second, 500*time.Millisecond, func() bool {
		return len(hook.received("uptime.down", svc.ID)) >= 1
	}), "uptime.down never reached the webhook; received %v", hook.order())
	down := hook.received("uptime.down", svc.ID)
	require.Len(t, down, 1, "one alert per outage, not one per failed check")
	require.Equal(t, "error", down[0].Severity)
	require.True(t, down[0].SignatureOK, "the delivery is signed with the channel's secret")
	require.NotEmpty(t, down[0].Title)
	require.Empty(t, hook.received("uptime.up", svc.ID), "nothing recovered yet")

	// ---- recovery -----------------------------------------------------------
	thaw()
	require.Truef(t, eventually(2*time.Minute, time.Second, func() bool {
		rep = s.uptimeReport(svc.ID)
		return rep.Str("state") == "up" && !openIncident(rep)
	}), "the outage did not resolve after the container ran again: %v", rep)
	require.Zero(t, s.incidentRows(svc.ID, true), "no open uptime_incidents row")
	require.Equal(t, 1, s.incidentRows(svc.ID, false), "the one outage is kept as history")
	incs = jlist(rep, "incidents")
	require.Len(t, incs, 1)
	require.NotNil(t, incs[0]["resolvedAt"])

	require.Truef(t, eventually(60*time.Second, 500*time.Millisecond, func() bool {
		return len(hook.received("uptime.up", svc.ID)) >= 1
	}), "uptime.up never reached the webhook; received %v", hook.order())

	// Let a few more checks pass: neither alert is repeated.
	last := parseReportTime(t, rep.Str("lastCheckAt"))
	require.Truef(t, eventually(30*time.Second, time.Second, func() bool {
		rep = s.uptimeReport(svc.ID)
		return parseReportTime(t, rep.Str("lastCheckAt")).After(last.Add(6 * time.Second))
	}), "checks stopped arriving: %v", rep)
	require.Len(t, hook.received("uptime.up", svc.ID), 1, "uptime.up is sent once")
	require.Len(t, hook.received("uptime.down", svc.ID), 1, "uptime.down is sent once")
	require.Equal(t, []string{"uptime.down", "uptime.up"}, hook.order(), "down first, then up, nothing else")
	require.True(t, hook.received("uptime.up", svc.ID)[0].SignatureOK)
	require.Equal(t, "up", rep.Str("state"))

	// The delivery log of the channel agrees.
	r := s.Do("GET", "/notifications/channels/"+hook.channelID+"/deliveries?status=delivered", nil)
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	types := map[string]int{}
	for _, d := range r.JSON().Items() {
		types[d.Str("eventType")]++
	}
	require.Equal(t, map[string]int{"uptime.down": 1, "uptime.up": 1}, types, string(r.Raw))

	// ---- availability ---------------------------------------------------------
	require.Positive(t, seriesTotal(rep, "up"), "passing checks are counted")
	require.Positive(t, seriesTotal(rep, "down"), "failing checks are counted")
	avail, ok := availability24h(rep)
	require.True(t, ok, "availability has data: %v", rep["availability"])
	require.Greater(t, avail, 0.0)
	require.Less(t, avail, 100.0, "the outage costs availability (a percentage)")
}

// TestUptimeMaintenanceSuppressesAlerts: while a maintenance window covers the
// service, failing checks make the state maintenance, open no outage (even with
// a threshold of one), send nothing, and are counted as maintenance checks,
// which availability leaves out.
func TestUptimeMaintenanceSuppressesAlerts(t *testing.T) {
	requireTier(t, tierFull)
	t.Parallel()
	s := SharedStack(t)
	hook := newWebhookSink(t, s)
	svc := uptimeService(t, s)
	// One failed check would open an outage and alert, were it not for the window.
	s.setMonitor(svc.ID, 1, 1)
	// Checks taken while the service was still starting may have failed (recorded
	// as down without an outage: a deployment was running); what counts is that
	// the window adds none.
	downBefore := seriesTotal(waitUptimeUp(t, s, svc.ID), "down")

	now := time.Now().UTC()
	r := s.Do("POST", "/maintenance-windows", map[string]any{
		"title":      "itest window " + s.hostBase,
		"startsAt":   now.Add(-time.Minute).Format(time.RFC3339),
		"endsAt":     now.Add(time.Hour).Format(time.RFC3339),
		"serviceIds": []string{svc.ID},
	})
	require.Equal(t, http.StatusCreated, r.Status, string(r.Raw))
	windowID := r.JSON().Str("id")
	require.NotEmpty(t, windowID)
	t.Cleanup(func() { s.Do("DELETE", "/maintenance-windows/"+windowID, nil) })

	freezeService(t, s, svc.ID)

	// A failing check was recorded, and it was judged as maintenance.
	var rep J
	require.Truef(t, eventually(2*time.Minute, time.Second, func() bool {
		rep = s.uptimeReport(svc.ID)
		return rep.Str("state") == "maintenance" && rep.Str("lastError") != ""
	}), "state maintenance with a failing check never showed: %v", rep)
	// Several checks later (each failing check takes the probe timeout) it is still no outage.
	require.Truef(t, eventually(2*time.Minute, time.Second, func() bool {
		rep = s.uptimeReport(svc.ID)
		return seriesTotal(rep, "maintenance") >= 3
	}), "maintenance checks were not counted: %v", jlist(rep, "series"))

	require.Equal(t, "maintenance", rep.Str("state"))
	require.Empty(t, jlist(rep, "incidents"), "no outage during maintenance")
	require.Zero(t, rep.Int("consecutiveFailures"), "maintenance is not judged")
	require.Zero(t, s.incidentRows(svc.ID, false), "no uptime_incidents row")
	require.Equal(t, downBefore, seriesTotal(rep, "down"), "failing checks of the window count as maintenance, not as down")
	require.Positive(t, seriesTotal(rep, "up"), "the checks before the window are counted as up")
	require.Empty(t, hook.received("", ""), "no alert: %v", hook.order())

	// Availability leaves the maintenance checks out: (up + degraded) / (checks - maintenance).
	avail, ok := availability24h(rep)
	require.True(t, ok, "the checks before the window give availability data")
	judged := seriesTotal(rep, "checks") - seriesTotal(rep, "maintenance")
	require.Positive(t, judged)
	require.InDelta(t, 100*float64(seriesTotal(rep, "up")+seriesTotal(rep, "degraded"))/float64(judged), avail, 0.01)
}

// TestStatusPagePublicHostServesOnlyThePage: a published status page is served
// on its own hostname through the real proxy by the control plane's status
// listener, which has nothing but the page on it: the page, its JSON and its
// badge answer, the dashboard, the API, the login and the MCP endpoint answer
// 404 on that host. Unpublishing withdraws the route and a service domain can
// not take the hostname.
//
// Exclusive: the control-plane server flag and the status upstream are
// process-wide settings of the routes the control plane pushes.
func TestStatusPagePublicHostServesOnlyThePage(t *testing.T) {
	requireTier(t, tierFull)
	app := appFixture(t)
	s := ExclusiveStack(t)

	// The status listener, on a host port the proxy reaches through the host
	// gateway (as in the external route tests); in production it is a second
	// listener of the control plane container (NEBULACTRL_STATUS_LISTEN).
	ln, err := net.Listen("tcp", "0.0.0.0:0")
	require.NoError(t, err)
	require.NotNil(t, s.App.Status, "the control plane has a status listener handler")
	statusSrv := &http.Server{Handler: s.App.Status, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = statusSrv.Serve(ln) }()
	t.Cleanup(func() { _ = statusSrv.Close() })
	statusroute.Set(fmt.Sprintf("host.docker.internal:%d", ln.Addr().(*net.TCPAddr).Port)) // NEBULACTRL_STATUS_UPSTREAM
	t.Cleanup(func() { statusroute.Set("") })
	// Only the server flagged as the control plane's host carries status routes.
	_, err = s.Pool.Exec(context.Background(), `UPDATE servers SET is_control_plane = true WHERE id = $1`, s.ServerID)
	require.NoError(t, err)

	const title, display = "Acme Status", "Public API"
	svc := s.NewService(SvcSpec{Name: "origin-db-primary", Image: app.AppImage})
	host := s.Host("status")

	r := s.Do("POST", "/status-pages", map[string]any{"title": title, "hostname": host})
	require.Equal(t, http.StatusCreated, r.Status, string(r.Raw))
	pageID := r.JSON().Str("id")
	require.NotEmpty(t, pageID)
	r = s.Do("PUT", "/status-pages/"+pageID+"/components", map[string]any{
		"components": []map[string]any{{"serviceId": svc.ID, "displayName": display}},
	})
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))

	// A service domain cannot take the hostname of a status page, published or not.
	r = s.Do("POST", "/services/"+svc.ID+"/domains", map[string]any{"hostname": host})
	require.Equal(t, http.StatusConflict, r.Status, string(r.Raw))
	require.Equal(t, "domain_conflict", r.JSON().Str("code"), string(r.Raw))

	// Not served while unpublished: the proxy has no route for the host.
	pr, err := proxyGet(host, "/", false)
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, pr.Status)

	// ---- published ------------------------------------------------------------
	r = s.Do("PATCH", "/status-pages/"+pageID, map[string]any{"published": true})
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	require.True(t, r.JSON().Bool("published"))

	page := waitPage(t, host, 90*time.Second, func(p pageResp) bool { return p.Status == http.StatusOK })
	require.Contains(t, page.Header.Get("Content-Type"), "text/html")
	require.Contains(t, page.Body, title)
	require.Contains(t, page.Body, display)
	require.NotContains(t, page.Body, "origin-db-primary", "the internal service name never reaches the public page")
	require.NotContains(t, page.Body, svc.ID)
	require.NotContains(t, page.Body, s.ProjectID)

	uuidRe := regexp.MustCompile(`(?i)[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)
	js, err := proxyGet(host, "/status.json", false)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, js.Status, js.Body)
	require.Contains(t, js.Header.Get("Content-Type"), "application/json")
	require.True(t, json.Valid([]byte(js.Body)), "status.json is JSON: %.200s", js.Body)
	require.Contains(t, js.Body, display)
	require.Empty(t, uuidRe.FindString(js.Body), "no identifier of the platform in status.json: %s", js.Body)
	require.NotContains(t, js.Body, "origin-db-primary")

	badge, err := proxyGet(host, "/badge/status.svg", false)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, badge.Status, badge.Body)
	require.Contains(t, badge.Header.Get("Content-Type"), "image/svg+xml")
	require.True(t, strings.Contains(badge.Body, "<svg"), "an SVG badge: %.200s", badge.Body)

	// The dashboard is not reachable on the status hostname: the route goes to a
	// listener that does not mount it.
	for _, path := range []string{"/api/v1/setup", "/api/v1/services", "/login", "/mcp", "/agent/v1/connect", "/api/v1/events"} {
		got, err := proxyGet(host, path, false)
		require.NoError(t, err, path)
		require.Equalf(t, http.StatusNotFound, got.Status, "GET https://%s%s must not reach the dashboard: %.200s", host, path, got.Body)
	}

	// ---- unpublished ----------------------------------------------------------
	r = s.Do("PATCH", "/status-pages/"+pageID, map[string]any{"published": false})
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	require.False(t, r.JSON().Bool("published"))
	waitPage(t, host, 90*time.Second, func(p pageResp) bool { return p.Status == http.StatusNotFound })
	js, err = proxyGet(host, "/status.json", false)
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, js.Status, js.Body)
	require.NotContains(t, js.Body, display)
}

// signatureOK reports whether header is the signature the documented scheme
// gives body under secret: the same bytes Sign produces for the header's own
// timestamp.
func signatureOK(secret, header string, body []byte) bool {
	ts, _, _ := strings.Cut(strings.TrimPrefix(header, "t="), ",")
	sec, err := strconv.ParseInt(ts, 10, 64)
	return err == nil && header == notifications.Sign(secret, body, time.Unix(sec, 0))
}
