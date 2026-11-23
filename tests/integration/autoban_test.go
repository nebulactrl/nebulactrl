//go:build integration

package integration

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// activeBans lists the service's bans in force.
func (s *Stack) activeBans(serviceID string) []J {
	s.t.Helper()
	r := s.Do("GET", "/services/"+serviceID+"/bans?state=active", nil)
	require.Equal(s.t, http.StatusOK, r.Status, string(r.Raw))
	return r.JSON().Items()
}

// 16. auto-ban: a visitor that keeps tripping the standard protection is
// banned (the edge guard answers 403 with X-Nebula-Firewall-Rule: autoban for
// every path), the ban is listed with the visitor's address, an unban lifts it
// within seconds and keeps only the prefix in the ended list, and an allow-list
// entry for the visitor stops its strikes from banning it.
//
// The test's visitor reaches Traefik through the Docker gateway, a private
// address the engine never bans in production; the harness agent lifts that
// exemption (AgentOptions.AutoBanVisitorsNotExempt).
func TestAutoBanBansAndUnbans(t *testing.T) {
	requireTier(t, tierFull)
	app := appFixture(t)
	guard := guardAgent(t, app.arch)
	t.Cleanup(func() { _, _ = sh(context.Background(), "", "docker", "rm", "-f", agentprotocol.GuardContainer) })
	s := ExclusiveStack(t, StackOptions{Agent: func(o *AgentOptions) {
		guard(o)
		o.AutoBanVisitorsNotExempt = true
	}})

	svc := s.NewService(SvcSpec{
		Name: "web", Image: app.AppImage, Domain: s.Host(),
		Vars: map[string]string{"VERSION": "v1"},
	})
	s.DeployOK(svc.ID)
	px := NewProxyClient(s.Host())
	px.WaitBody(t, "/", "v1", 30*time.Second)
	apiPath := "/services/" + svc.ID + "/autoban"

	// -- policy: 3 strikes within a minute ban for five minutes ---------------
	policy := func(allow ...string) map[string]any {
		if allow == nil {
			allow = []string{}
		}
		return map[string]any{"enabled": true, "threshold": 3, "windowSeconds": 60, "durationSeconds": 300, "allow": allow}
	}
	r := s.Do("PUT", apiPath, policy())
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	g := s.Do("GET", apiPath, nil).JSON()
	require.True(t, g.Obj("policy").Bool("enabled"), "%v", g)
	require.Equal(t, 3, g.Obj("policy").Int("threshold"))
	require.True(t, g.Bool("analytics"), "auto-ban needs traffic analytics: %v", g)

	// -- strikes: /.env is blocked by the standard protection; the agent counts
	// it from the access log once the new route table (the policy) is live, so
	// the strikes are sent until a ban shows up.
	sent := 0
	var ban J
	ok := eventually(120*time.Second, 400*time.Millisecond, func() bool {
		code, _, err := px.Get("/.env")
		if err != nil || code != http.StatusForbidden {
			t.Fatalf("GET /.env: want 403, got %d (err %v)", code, err)
		}
		sent++
		if bans := s.activeBans(svc.ID); len(bans) > 0 {
			ban = bans[0]
			return true
		}
		return false
	})
	require.True(t, ok, "no ban after %d /.env requests: %v", sent, s.Do("GET", apiPath, nil).JSON())
	require.Len(t, s.activeBans(svc.ID), 1, "one visitor, one ban")
	require.GreaterOrEqual(t, ban.Int("strikes"), 3, "%v", ban)
	require.NotEmpty(t, ban.Str("address"), "an active ban shows the address: %v", ban)
	require.Equal(t, ban.Str("address")+"/32", ban.Str("prefix"), "an IPv4 visitor is banned as a /32: %v", ban)
	require.Equal(t, 0, ban.Int("endedAt"), "%v", ban)
	rules, _ := ban["rules"].(map[string]any)
	require.NotEmpty(t, rules["stdsecrets"], "the strikes name the standard protection: %v", ban)
	visitor := ban.Str("address")
	banID := ban.Str("id")
	require.NotEmpty(t, banID)

	// -- enforcement: / (normally 200) is 403 now, answered by the guard -----
	waitStatus(t, px, "/", http.StatusForbidden)
	logs, err := sh(context.Background(), "", "docker", "logs", "--tail", "600", agentprotocol.ProxyContainer)
	require.NoError(t, err)
	var sawBan bool
	for _, l := range strings.Split(logs, "\n") {
		if strings.Contains(l, `"RequestPath":"/"`) && strings.Contains(l, `"downstream_X-Nebula-Firewall-Rule":"autoban"`) {
			sawBan = true
		}
	}
	require.True(t, sawBan, "no access-log line of / carries X-Nebula-Firewall-Rule: autoban:\n%.3000s", logs)
	// A banned request is attributed to "autoban" in the request lines too, and
	// is not a strike (the ban is not extended, nothing is counted twice).
	var lines []agentprotocol.RequestRecord
	ok = eventually(90*time.Second, 2*time.Second, func() bool {
		lines = s.requestLines(svc.ID, "@blocked_by:autoban")
		return countLines(lines, "autoban", "/") >= 1
	})
	require.True(t, ok, "no request line of / blocked by autoban: %+v", lines)
	for _, l := range lines {
		require.Equal(t, http.StatusForbidden, l.Status, "%+v", l)
		require.True(t, l.IP == "" || cidrRE.MatchString(l.IP), "a request line holds a network, not an address: %+v", l)
	}
	require.Len(t, s.activeBans(svc.ID), 1, "banned requests are no strikes")

	// -- unban: 202 queued, the guard lets the visitor in again --------------
	r = s.Do("DELETE", "/services/"+svc.ID+"/bans/"+banID, nil)
	require.Equal(t, http.StatusAccepted, r.Status, string(r.Raw))
	require.Equal(t, "queued", r.JSON().Str("status"))
	waitStatus(t, px, "/", http.StatusOK)
	var ended J
	ok = eventually(60*time.Second, time.Second, func() bool {
		if len(s.activeBans(svc.ID)) > 0 {
			return false
		}
		er := s.Do("GET", "/services/"+svc.ID+"/bans?state=ended", nil)
		require.Equal(t, http.StatusOK, er.Status, string(er.Raw))
		for _, b := range er.JSON().Items() {
			if b.Str("id") == banID {
				ended = b
				return true
			}
		}
		return false
	})
	require.True(t, ok, "the unbanned ban never moved to the ended list")
	require.Equal(t, "unbanned", ended.Str("endReason"), "%v", ended)
	require.Equal(t, visitor+"/32", ended.Str("prefix"))
	require.Empty(t, ended.Str("address"), "an ended ban forgets the address: %v", ended)
	r = s.Do("DELETE", "/services/"+svc.ID+"/bans/"+banID, nil)
	require.Equal(t, http.StatusConflict, r.Status, "an ended ban cannot be unbanned again: %s", r.Raw)

	// -- allow list: the visitor's strikes no longer ban it -------------------
	r = s.Do("PUT", apiPath, policy(visitor))
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	// The policy reaches the agent with the route table: wait until / still
	// answers 200 after the change (the guard evaluates the allow list first).
	waitStatus(t, px, "/", http.StatusOK)
	time.Sleep(3 * time.Second)
	before := countLines(s.requestLines(svc.ID, "@blocked_by:stdsecrets"), "stdsecrets", "/.env")
	for range 5 {
		code, _, err := px.Get("/.env")
		require.NoError(t, err)
		require.Equal(t, http.StatusForbidden, code, "the standard protection still blocks an allow-listed visitor")
	}
	// The agent has read the five lines (they are in the request log) ...
	ok = eventually(90*time.Second, 2*time.Second, func() bool {
		return countLines(s.requestLines(svc.ID, "@blocked_by:stdsecrets"), "stdsecrets", "/.env") >= before+5
	})
	require.True(t, ok, "the five /.env requests never reached the request lines")
	// ... and counted none: the ban (strikes → engine → guard) takes about two
	// seconds, so wait out twice that before looking.
	time.Sleep(6 * time.Second)
	require.Empty(t, s.activeBans(svc.ID), "an allow-listed visitor is never banned")
	for range 3 {
		code, body, err := px.Get("/")
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, code)
		require.Equal(t, "v1", body)
	}
}
