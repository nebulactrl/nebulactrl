package cloudflare

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/riverqueue/river"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/notifications"
	"github.com/nebulactrl/nebulactrl/internal/policy"
)

// fakeIPs serves Cloudflare's IP ranges the three ways the job reads them: the
// API (/client/v4/ips) and the ips-v4 / ips-v6 text files.
type fakeIPs struct {
	srv *httptest.Server

	mu         sync.Mutex
	apiV4      []string
	apiV6      []string
	apiStatus  int    // 0 = 200
	apiRaw     string // overrides the API body
	textV4     []string
	textV6     []string
	textStatus int
	hits       []string
}

func newFakeIPs(t *testing.T) *fakeIPs {
	t.Helper()
	b := Builtin()
	f := &fakeIPs{apiV4: b.V4, apiV6: b.V6, textV4: b.V4, textV6: b.V6}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeIPs) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hits = append(f.hits, r.URL.Path)
	if r.Header.Get("Authorization") != "" {
		http.Error(w, "the IP list needs no credentials", http.StatusBadRequest)
		return
	}
	switch r.URL.Path {
	case "/client/v4/ips":
		if f.apiStatus != 0 {
			http.Error(w, "boom", f.apiStatus)
			return
		}
		if f.apiRaw != "" {
			_, _ = w.Write([]byte(f.apiRaw))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": true, "errors": []any{}, "messages": []any{},
			"result": map[string]any{"ipv4_cidrs": f.apiV4, "ipv6_cidrs": f.apiV6, "etag": "x"},
		})
	case "/ips-v4", "/ips-v6":
		if f.textStatus != 0 {
			http.Error(w, "boom", f.textStatus)
			return
		}
		list := f.textV4
		if r.URL.Path == "/ips-v6" {
			list = f.textV6
		}
		_, _ = w.Write([]byte(strings.Join(list, "\n")))
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeIPs) failAll() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.apiStatus, f.textStatus = http.StatusInternalServerError, http.StatusInternalServerError
}

func (f *fakeIPs) heal() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.apiStatus, f.textStatus, f.apiRaw = 0, 0, ""
}

func (f *fakeIPs) setAPI(v4, v6 []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.apiV4, f.apiV6 = v4, v6
	f.textV4, f.textV6 = v4, v6
}

func (f *fakeIPs) hitCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.hits)
}

func (e *env) useIPs(t *testing.T) *fakeIPs {
	t.Helper()
	f := newFakeIPs(t)
	e.svc.SetIPSources(f.srv.URL+"/client/v4", f.srv.URL)
	return f
}

func rangesWith(base []string, drop int, add ...string) []string {
	out := append([]string(nil), base[drop:]...)
	return append(out, add...)
}

func TestBuiltinIPListIsValidAndCurrent(t *testing.T) {
	b := Builtin()
	require.Equal(t, IPSourceBuiltin, b.Source)
	require.GreaterOrEqual(t, len(b.V4), minV4Ranges)
	require.GreaterOrEqual(t, len(b.V6), minV6Ranges)
	require.Len(t, b.CIDRs(), len(b.V4)+len(b.V6))
	require.Contains(t, b.V4, "173.245.48.0/20")
	require.Contains(t, b.V6, "2606:4700::/32")
	require.Equal(t, b.Hash, Builtin().Hash, "the hash is stable")
	_, err := time.Parse("2006-01-02", BuiltinIPsDate)
	require.NoError(t, err)
}

func TestIPListValidation(t *testing.T) {
	b := Builtin()
	prev := b
	cases := []struct {
		name   string
		v4, v6 []string
		prev   *IPList
		err    string
	}{
		{"valid", b.V4, b.V6, &prev, ""},
		{"empty", nil, nil, nil, "empty"},
		{"empty v6", b.V4, nil, nil, "empty"},
		{"invalid CIDR", append([]string{"not-a-cidr"}, b.V4...), b.V6, nil, "not a CIDR"},
		{"bare address", append([]string{"1.2.3.4"}, b.V4...), b.V6, nil, "not a CIDR"},
		{"v6 in the v4 list", append([]string{"2606:4700::/32"}, b.V4...), b.V6, nil, "not an IPv4"},
		{"v4 in the v6 list", b.V4, append([]string{"104.16.0.0/13"}, b.V6...), nil, "not an IPv6"},
		{"everything", append([]string{"0.0.0.0/0"}, b.V4...), b.V6, nil, "implausibly wide"},
		{"everything v6", b.V4, append([]string{"::/0"}, b.V6...), nil, "implausibly wide"},
		{"too few v4", b.V4[:9], b.V6, nil, "IPv4 ranges"},
		{"too few v6", b.V4, b.V6[:4], nil, "IPv6 ranges"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			l, err := NewIPList(IPSourceAPI, c.v4, c.v6, c.prev)
			if c.err == "" {
				require.NoError(t, err)
				require.Equal(t, IPSourceAPI, l.Source)
				return
			}
			require.ErrorContains(t, err, c.err)
		})
	}

	t.Run("shrinking by more than half is refused, by half or less is fine", func(t *testing.T) {
		var big []string
		for i := range 40 {
			big = append(big, fmt.Sprintf("10.%d.0.0/16", i))
		}
		old, err := NewIPList(IPSourceAPI, big, b.V6, nil)
		require.NoError(t, err)
		_, err = NewIPList(IPSourceAPI, big[:19], b.V6, &old)
		require.ErrorContains(t, err, "shrink")
		_, err = NewIPList(IPSourceAPI, big[:20], b.V6, &old)
		require.NoError(t, err)
		v6big := append([]string(nil), b.V6...)
		v6big = append(v6big, "2001:db8::/32", "2001:db9::/32", "2001:dba::/32", "2001:dbb::/32", "2001:dbc::/32", "2001:dbd::/32", "2001:dbe::/32", "2001:dbf::/32")
		old6, err := NewIPList(IPSourceAPI, b.V4, v6big, nil)
		require.NoError(t, err)
		_, err = NewIPList(IPSourceAPI, b.V4, b.V6, &old6)
		require.ErrorContains(t, err, "IPv6 list would shrink")
	})

	t.Run("normalised, deduplicated and sorted", func(t *testing.T) {
		raw := append([]string{"104.16.5.5/13", " 173.245.48.0/20 "}, b.V4...) // host bits are masked, spaces trimmed
		l, err := NewIPList(IPSourceText, raw, b.V6, nil)
		require.NoError(t, err)
		require.Equal(t, b.V4, l.V4)
		require.Equal(t, b.Hash, l.Hash, "the hash covers the normalised content")
		reordered := append([]string(nil), b.V4...)
		reordered[0], reordered[len(reordered)-1] = reordered[len(reordered)-1], reordered[0]
		l2, err := NewIPList(IPSourceText, reordered, b.V6, nil)
		require.NoError(t, err)
		require.Equal(t, b.Hash, l2.Hash)
	})
}

func TestFirstRefreshStoresTheListWithoutAnnouncingAnything(t *testing.T) {
	e := newEnv(t)
	ips := e.useIPs(t)
	e.connect(t)
	bg := context.Background()

	v, err := e.svc.IPs(e.dev)
	require.NoError(t, err)
	require.Equal(t, IPSourceBuiltin, v.Source, "nothing stored yet: the built-in copy is in use")
	require.Nil(t, v.FetchedAt)
	require.Equal(t, BuiltinIPsDate, v.BuiltinDate)
	require.Equal(t, len(Builtin().V4), v.V4Count)

	res, err := e.svc.RefreshIPs(bg)
	require.NoError(t, err)
	require.NoError(t, res.Err)
	require.False(t, res.Changed, "Cloudflare's list equals the built-in one")
	require.Equal(t, IPSourceAPI, res.Source)
	require.Equal(t, 1, ips.hitCount(), "the API answered, the text files were not needed")

	v, err = e.svc.IPs(e.dev)
	require.NoError(t, err)
	require.Equal(t, IPSourceAPI, v.Source)
	require.NotNil(t, v.FetchedAt)
	require.Empty(t, v.LastError)
	require.Empty(t, e.audits(t, "cloudflare.ips.%"))
	require.Zero(t, e.notes.count())
}

func TestChangedListIsAuditedAnnouncedAndPushedToLockedServers(t *testing.T) {
	e, rec, _ := lockEnv(t)
	ips := e.useIPs(t)
	bg := context.Background()
	b := Builtin()

	_, err := e.svc.RefreshIPs(bg)
	require.NoError(t, err)
	before, err := e.q.GetCloudflareIPList(bg)
	require.NoError(t, err)
	rec.reset()
	notesBefore := e.notes.count()

	// Cloudflare adds a v4 range and drops one.
	next := rangesWith(b.V4, 1, "192.0.2.0/24")
	ips.setAPI(next, b.V6)
	res, err := e.svc.RefreshIPs(bg)
	require.NoError(t, err)
	require.True(t, res.Changed)

	cur, err := CurrentIPList(bg, e.q)
	require.NoError(t, err)
	require.Contains(t, cur.V4, "192.0.2.0/24")
	require.NotContains(t, cur.V4, b.V4[0])
	after, err := e.q.GetCloudflareIPList(bg)
	require.NoError(t, err)
	require.NotEqual(t, before.Hash, after.Hash)
	require.True(t, after.ChangedAt.After(before.ChangedAt) || after.ChangedAt.Equal(before.ChangedAt))

	// The lock's route push went to the one server with locked routes, once.
	require.Equal(t, 1, rec.count())
	require.Equal(t, e.srvA.ID, rec.servers[0])

	// Audited for the team, with what changed, no secrets.
	audits := e.audits(t, "cloudflare.ips.updated")
	require.Len(t, audits, 1)
	var md map[string]any
	require.NoError(t, json.Unmarshal([]byte(audits[0].Meta), &md))
	require.Equal(t, []any{"192.0.2.0/24"}, md["added"])
	require.Equal(t, []any{b.V4[0]}, md["removed"])
	require.Equal(t, "api", md["source"])
	var actor string
	require.NoError(t, e.pool.QueryRow(bg, `SELECT actor_type FROM audit_events WHERE action = 'cloudflare.ips.updated'`).Scan(&actor))
	require.Equal(t, "system", actor)

	// Announced.
	require.Equal(t, notesBefore+1, e.notes.count())
	e.notes.mu.Lock()
	ev := e.notes.events[len(e.notes.events)-1]
	e.notes.mu.Unlock()
	require.Equal(t, notifications.TypeCloudflareIPsChanged, ev.Type)
	require.Equal(t, e.ws, ev.WorkspaceID)
	require.Contains(t, ev.Body, "1 added, 1 removed")

	// The same list again changes nothing: no audit, no push, no announcement.
	rec.reset()
	res, err = e.svc.RefreshIPs(bg)
	require.NoError(t, err)
	require.False(t, res.Changed)
	require.Zero(t, rec.count())
	require.Len(t, e.audits(t, "cloudflare.ips.updated"), 1)
	require.Equal(t, notesBefore+1, e.notes.count())
	again, err := e.q.GetCloudflareIPList(bg)
	require.NoError(t, err)
	require.Equal(t, after.ChangedAt, again.ChangedAt, "changed_at only moves with the content")
	require.False(t, again.FetchedAt.Before(after.FetchedAt))
}

func TestTextFilesAreTheFallbackAndTheLastGoodListSurvivesFailures(t *testing.T) {
	e := newEnv(t)
	ips := e.useIPs(t)
	e.connect(t)
	bg := context.Background()
	b := Builtin()

	// The API is down: the text files answer.
	ips.mu.Lock()
	ips.apiStatus = http.StatusServiceUnavailable
	ips.textV4 = rangesWith(b.V4, 0, "192.0.2.0/24")
	ips.mu.Unlock()
	res, err := e.svc.RefreshIPs(bg)
	require.NoError(t, err)
	require.NoError(t, res.Err)
	require.True(t, res.Changed)
	require.Equal(t, IPSourceText, res.Source)
	good, err := CurrentIPList(bg, e.q)
	require.NoError(t, err)
	require.Contains(t, good.V4, "192.0.2.0/24")

	// Both down: the last good list stays, the failure is recorded.
	ips.failAll()
	res, err = e.svc.RefreshIPs(bg)
	require.NoError(t, err, "a failed fetch is not a job failure")
	require.Error(t, res.Err)
	kept, err := CurrentIPList(bg, e.q)
	require.NoError(t, err)
	require.Equal(t, good.Hash, kept.Hash)
	v, err := e.svc.IPs(e.dev)
	require.NoError(t, err)
	require.NotEmpty(t, v.LastError)
	require.NotNil(t, v.FailingSince)
	require.False(t, v.Stale)
	first := *v.FailingSince
	_, err = e.svc.RefreshIPs(bg)
	require.NoError(t, err)
	v, err = e.svc.IPs(e.dev)
	require.NoError(t, err)
	require.Equal(t, first.UnixMilli(), v.FailingSince.UnixMilli(), "failing_since keeps the first failure of the streak")

	// A garbage answer, an empty list, a shrunken list: all refused, list kept.
	ips.heal()
	for name, mutate := range map[string]func(){
		"not json": func() {
			ips.mu.Lock()
			ips.apiRaw = "<html>captive portal</html>"
			ips.textV4 = []string{"nope"}
			ips.mu.Unlock()
		},
		"empty": func() {
			ips.mu.Lock()
			ips.apiRaw = ""
			ips.apiV4, ips.apiV6, ips.textV4, ips.textV6 = nil, nil, nil, nil
			ips.mu.Unlock()
		},
		"api failure": func() {
			ips.mu.Lock()
			ips.apiRaw = `{"success":false,"result":null}`
			ips.textV4, ips.textV6 = nil, nil
			ips.mu.Unlock()
		},
		"invalid CIDR":   func() { ips.setAPI(append([]string{"999.1.1.1/8"}, b.V4...), b.V6) },
		"wide":           func() { ips.setAPI(append([]string{"0.0.0.0/0"}, b.V4...), b.V6) },
		"shrunk by half": func() { ips.setAPI(b.V4[:5], b.V6[:2]) },
	} {
		ips.heal()
		mutate()
		res, err = e.svc.RefreshIPs(bg)
		require.NoError(t, err, name)
		require.Error(t, res.Err, name)
		now, err := CurrentIPList(bg, e.q)
		require.NoError(t, err)
		require.Equal(t, good.Hash, now.Hash, name)
	}
	require.Len(t, e.audits(t, "cloudflare.ips.updated"), 1, "only the first (text fallback) update was audited; refused ones leave no trace")

	// No credentials were ever sent to the public endpoints (the fake rejects them).
	ips.heal()
	ips.setAPI(b.V4, b.V6)
	res, err = e.svc.RefreshIPs(bg)
	require.NoError(t, err)
	require.NoError(t, res.Err)
	v, err = e.svc.IPs(e.dev)
	require.NoError(t, err)
	require.Empty(t, v.LastError, "a good fetch clears the error")
	require.Nil(t, v.FailingSince)
}

func TestFirstFetchFailingKeepsTheBuiltInListAndRecordsIt(t *testing.T) {
	e := newEnv(t)
	ips := e.useIPs(t)
	ips.failAll()
	bg := context.Background()

	res, err := e.svc.RefreshIPs(bg)
	require.NoError(t, err)
	require.Error(t, res.Err)
	l, err := CurrentIPList(bg, e.q)
	require.NoError(t, err)
	require.Equal(t, Builtin().Hash, l.Hash)
	v, err := e.svc.IPs(e.dev)
	require.NoError(t, err)
	require.Equal(t, IPSourceBuiltin, v.Source)
	require.Nil(t, v.FetchedAt, "the seed row is not a fetch")
	require.NotEmpty(t, v.LastError)
}

func TestFailingForADayTellsTheTeamOnce(t *testing.T) {
	e := newEnv(t)
	ips := e.useIPs(t)
	e.connect(t)
	bg := context.Background()
	ips.failAll()

	_, err := e.svc.RefreshIPs(bg)
	require.NoError(t, err)
	require.Zero(t, e.notes.count(), "a first failure is not news")

	e.exec(t, `UPDATE cloudflare_ip_lists SET failing_since = now() - interval '25 hours'`)
	_, err = e.svc.RefreshIPs(bg)
	require.NoError(t, err)
	require.Equal(t, 1, e.notes.count())
	e.notes.mu.Lock()
	ev := e.notes.events[0]
	e.notes.mu.Unlock()
	require.Equal(t, notifications.TypeCloudflareIPsStale, ev.Type)
	require.Equal(t, notifications.SeverityWarning, ev.Severity)
	require.Equal(t, e.ws, ev.WorkspaceID)
	v, err := e.svc.IPs(e.dev)
	require.NoError(t, err)
	require.True(t, v.Stale)

	// Not again while it keeps failing.
	_, err = e.svc.RefreshIPs(bg)
	require.NoError(t, err)
	require.Equal(t, 1, e.notes.count())

	// It recovers; a later outage tells again.
	ips.heal()
	_, err = e.svc.RefreshIPs(bg)
	require.NoError(t, err)
	v, err = e.svc.IPs(e.dev)
	require.NoError(t, err)
	require.False(t, v.Stale)
	ips.failAll()
	_, err = e.svc.RefreshIPs(bg)
	require.NoError(t, err)
	e.exec(t, `UPDATE cloudflare_ip_lists SET failing_since = now() - interval '30 hours'`)
	_, err = e.svc.RefreshIPs(bg)
	require.NoError(t, err)
	require.Equal(t, 2, e.notes.count())
}

func TestManualRefreshIsForAdministrators(t *testing.T) {
	e := newEnv(t)
	ips := e.useIPs(t)
	bg := context.Background()

	_, err := e.svc.RefreshIPsNow(e.dev)
	require.Equal(t, "forbidden", code(t, err))
	_, err = e.svc.IPs(principal(e.ws, nil))
	require.Equal(t, "forbidden", code(t, err))
	_, err = e.svc.IPs(principal(e.ws, []string{policy.ScopeProjectsRead}))
	require.NoError(t, err)

	v, err := e.svc.RefreshIPsNow(e.admin)
	require.NoError(t, err)
	require.Equal(t, IPSourceAPI, v.Source)
	require.NotNil(t, v.FetchedAt)

	ips.failAll()
	_, err = e.svc.RefreshIPsNow(e.admin)
	require.Equal(t, "ip_refresh_failed", code(t, err))
	ae := err.(interface{ GetStatus() int })
	require.Equal(t, http.StatusBadGateway, ae.GetStatus())
	l, err := CurrentIPList(bg, e.q)
	require.NoError(t, err)
	require.Equal(t, Builtin().Hash, l.Hash)
}

func TestJobWorkerRefreshesAndNeverFailsOnABadFetch(t *testing.T) {
	e := newEnv(t)
	ips := e.useIPs(t)
	bg := context.Background()
	w := &ipRefreshWorker{svc: e.svc}

	require.NoError(t, w.Work(bg, &river.Job[IPRefreshArgs]{}))
	stored, err := e.q.GetCloudflareIPList(bg)
	require.NoError(t, err)
	require.Equal(t, IPSourceAPI, stored.Source)

	ips.failAll()
	require.NoError(t, w.Work(bg, &river.Job[IPRefreshArgs]{}), "River must not retry a fetch Cloudflare refused")

	require.Equal(t, "cloudflare_ip_refresh", IPRefreshArgs{}.Kind())
	require.Equal(t, 3*time.Hour, IPRefreshInterval)
}
