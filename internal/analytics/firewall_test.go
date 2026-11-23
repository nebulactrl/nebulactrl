package analytics

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

func blockedRow(at time.Time, blocked map[string]int64) row {
	var r row
	r.Bucket, r.Host, r.Blocked = at, "app.example.com", blocked
	for _, n := range blocked {
		r.Requests += n
	}
	return r
}

func TestRuleBookNamesStandardOwnAndRemovedRules(t *testing.T) {
	raw := json.RawMessage(`{"rules":[
		{"id":"office","name":"Block the office","action":"block","conditions":[{"field":"ip","op":"in","values":["203.0.113.0/24"]}]},
		{"id":"vip","name":"","action":"allow","conditions":[{"field":"ip","op":"in","values":["198.51.100.1"]}]}]}`)
	book := newRuleBook(raw)

	got := book.counts(map[string]int64{"office": 5, "stdsecrets": 9, "vip": 1, "gone": 2, "stdold": 3, "zero": 0}, true)
	require.Equal(t, []FirewallRuleCount{
		{ID: "stdsecrets", Name: "Secret files", Kind: FirewallKindStandard, Action: agentprotocol.FirewallBlock, Requests: 9},
		{ID: "office", Name: "Block the office", Kind: FirewallKindRule, Action: agentprotocol.FirewallBlock, Requests: 5},
		{ID: "stdold", Name: "stdold", Kind: FirewallKindRemoved, Requests: 3},
		{ID: "gone", Name: "gone", Kind: FirewallKindRemoved, Requests: 2},
		{ID: "vip", Name: "vip", Kind: FirewallKindRule, Action: agentprotocol.FirewallAllow, Requests: 1},
	}, got, "largest first, an unnamed rule shows its id, a rule that is gone is 'removed', zero counts are left out")

	// Over several services the book cannot tell a deleted rule from another
	// service's: such ids stay plain rules, listed by id.
	many := newRuleBook(nil).counts(map[string]int64{"office": 5, "stdsecrets": 9}, false)
	require.Equal(t, FirewallKindStandard, many[0].Kind)
	require.Equal(t, FirewallRuleCount{ID: "office", Name: "office", Kind: FirewallKindRule, Requests: 5}, many[1])

	require.Empty(t, book.counts(nil, true))
	require.NotNil(t, book.counts(nil, true), "serializes as [] not null")
}

func TestBuildReportsBlockedRequestsPerRuleAndStep(t *testing.T) {
	to := time.Date(2026, 3, 10, 12, 30, 0, 0, time.UTC)
	from := to.Add(-10 * time.Minute)
	rows := []row{
		blockedRow(to.Add(-9*time.Minute), map[string]int64{"stdsecrets": 4, "office": 1}),
		blockedRow(to.Add(-9*time.Minute+time.Second), nil), // a row without blocks
		blockedRow(to.Add(-2*time.Minute), map[string]int64{"stdsecrets": 2}),
	}
	for i := range rows {
		rows[i].Requests += 10
		rows[i].Hist[agentprotocol.LatencyBucket(100)] = rows[i].Requests
	}
	rep := build(reportInput{
		rows: rows, from: from, to: to, step: time.Minute, tier: TierMinute, now: to,
		rules: newRuleBook(json.RawMessage(`{"rules":[{"id":"office","name":"Office","action":"block","conditions":[{"field":"ip","op":"in","values":["203.0.113.0/24"]}]}]}`)),
	})
	require.NotNil(t, rep.Firewall)
	require.EqualValues(t, 7, rep.Firewall.Blocked)
	require.Equal(t, []FirewallRuleCount{
		{ID: "stdsecrets", Name: "Secret files", Kind: FirewallKindStandard, Action: agentprotocol.FirewallBlock, Requests: 6},
		{ID: "office", Name: "Office", Kind: FirewallKindRule, Action: agentprotocol.FirewallBlock, Requests: 1},
	}, rep.Firewall.ByRule)
	require.Len(t, rep.Firewall.Series, len(rep.Series), "the blocked series shares the chart's x axis")
	var perStep int64
	for i, p := range rep.Firewall.Series {
		require.Equal(t, rep.Series[i].TS, p.TS)
		perStep += p.Blocked
	}
	require.EqualValues(t, 7, perStep)

	// A report over several services (rules nil) names the standard protection only.
	rep = build(reportInput{rows: rows, from: from, to: to, step: time.Minute, tier: TierMinute, now: to})
	require.EqualValues(t, 7, rep.Firewall.Blocked)
	require.Equal(t, FirewallKindRule, rep.Firewall.ByRule[1].Kind)
	require.Equal(t, "office", rep.Firewall.ByRule[1].Name)
}

func TestFoldMergesBlockedCountsLikeStatusCodes(t *testing.T) {
	at := time.Date(2026, 3, 10, 11, 0, 0, 0, time.UTC)
	mk := func(minute int, blocked string) db.Traffic1m {
		hist := make([]int64, agentprotocol.LatencyBuckets)
		hist[0] = 1
		return db.Traffic1m{
			Bucket: at.Add(time.Duration(minute) * time.Minute), Host: "app.example.com", Requests: 5, LatHist: hist,
			StatusCodes: json.RawMessage(`{"403":3}`), Blocked: json.RawMessage(blocked),
		}
	}
	out := fold([]db.Traffic1m{
		mk(0, `{"stdsecrets":2,"office":1}`),
		mk(1, `{"stdsecrets":3}`),
		mk(2, ``),              // a row from before the agent reported blocks
		mk(61, `{"office":4}`), // the next hour
	}, time.Hour)
	require.Len(t, out, 2)
	var first, second map[string]int64
	require.NoError(t, json.Unmarshal(out[0].Blocked, &first))
	require.NoError(t, json.Unmarshal(out[1].Blocked, &second))
	require.Equal(t, map[string]int64{"stdsecrets": 5, "office": 1}, first)
	require.Equal(t, map[string]int64{"office": 4}, second)

	// The daily rollup folds the hour rows the same way.
	days := fold([]db.Traffic1m{db.Traffic1m(out[0]), db.Traffic1m(out[1])}, 24*time.Hour)
	require.Len(t, days, 1)
	var day map[string]int64
	require.NoError(t, json.Unmarshal(days[0].Blocked, &day))
	require.Equal(t, map[string]int64{"stdsecrets": 5, "office": 5}, day)

	none := fold([]db.Traffic1m{mk(0, ``)}, time.Hour)
	require.JSONEq(t, `{}`, string(none[0].Blocked), "an hour without blocks stores {}")
}

func TestSanitizeBoundsBlockedCounts(t *testing.T) {
	now := time.Date(2026, 3, 10, 12, 30, 0, 0, time.UTC)
	b := mkBucket("", "app.example.com", now.Add(-time.Minute), 10, nil)
	b.Blocked = map[string]int64{"stdsecrets": 4, "Bad Id": 1, "": 1, strings.Repeat("a", 17): 1, "neg": -3, "huge": 1 << 60}
	p, ok := sanitize(b, now, "")
	require.True(t, ok)
	var got map[string]int64
	require.NoError(t, json.Unmarshal(p.Blocked, &got))
	require.Equal(t, map[string]int64{"stdsecrets": 4, "neg": 0, "huge": maxCounterValue}, got, "only rule-id-shaped keys, clamped counts")

	b.Blocked = map[string]int64{}
	for i := range 300 {
		b.Blocked["r"+strings.Repeat("x", i%5)+string(rune('a'+i%26))+string(rune('a'+i/26%26))+string(rune('a'+i/676))] = 1
	}
	p, _ = sanitize(b, now, "")
	got = nil
	require.NoError(t, json.Unmarshal(p.Blocked, &got))
	require.LessOrEqual(t, len(got), maxBlockedRules)

	b.Blocked = nil
	p, _ = sanitize(b, now, "")
	require.JSONEq(t, `{}`, string(p.Blocked))
}

func TestIngestReportAndBlocksViewEndToEnd(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	_, err := f.pool.Exec(ctx, `UPDATE services SET firewall = $2 WHERE id = $1`, f.service,
		`{"rules":[{"id":"office","name":"Block the office","action":"block","conditions":[{"field":"ip","op":"in","values":["203.0.113.0/24"]}]}]}`)
	require.NoError(t, err)
	_, err = f.pool.Exec(ctx, `UPDATE servers SET traffic_analytics_enabled = true WHERE id = $1`, f.server)
	require.NoError(t, err)

	svc := f.service.String()
	m1, m2 := fixedNow.Add(-2*time.Minute), fixedNow.Add(-time.Minute)
	a := mkBucket(svc, "app.example.com", m1, 10, nil)
	a.S2xx, a.S4xx, a.Blocked = 6, 4, map[string]int64{"stdsecrets": 3, "office": 1}
	b := mkBucket(svc, "app.example.com", m2, 10, nil)
	b.S2xx, b.S4xx, b.Blocked = 9, 1, map[string]int64{"stdsecrets": 1, "deleted": 2}
	require.NoError(t, f.svc.IngestTraffic(f.ctx, f.server, trafficOf(a, b)))

	rep, err := f.svc.ServiceTraffic(f.ctx, f.service, Query{Range: "1h"})
	require.NoError(t, err)
	require.NotNil(t, rep.Firewall)
	require.EqualValues(t, 7, rep.Firewall.Blocked)
	require.Equal(t, []FirewallRuleCount{
		{ID: "stdsecrets", Name: "Secret files", Kind: FirewallKindStandard, Action: agentprotocol.FirewallBlock, Requests: 4},
		{ID: "deleted", Name: "deleted", Kind: FirewallKindRemoved, Requests: 2},
		{ID: "office", Name: "Block the office", Kind: FirewallKindRule, Action: agentprotocol.FirewallBlock, Requests: 1},
	}, rep.Firewall.ByRule)

	// The rollups carry the counts into the hour and day tiers.
	require.NoError(t, (&rollupHourlyWorker{svc: f.svc}).Work(ctx, nil))
	require.NoError(t, (&rollupDailyWorker{svc: f.svc}).Work(ctx, nil))
	var hour, day string
	require.NoError(t, f.pool.QueryRow(ctx, `SELECT blocked::text FROM traffic_1h WHERE host = 'app.example.com'`).Scan(&hour))
	require.NoError(t, f.pool.QueryRow(ctx, `SELECT blocked::text FROM traffic_1d WHERE host = 'app.example.com'`).Scan(&day))
	require.JSONEq(t, `{"stdsecrets":4,"office":1,"deleted":2}`, hour)
	require.JSONEq(t, hour, day)

	week, err := f.svc.ServiceTraffic(f.ctx, f.service, Query{Range: "7d"})
	require.NoError(t, err)
	require.EqualValues(t, 7, week.Firewall.Blocked, "the hour tier answers a 7 day range")

	blocks, err := f.svc.ServiceFirewallBlocks(f.ctx, f.service, "24h")
	require.NoError(t, err)
	require.Equal(t, "24h", blocks.Range)
	require.EqualValues(t, 7, blocks.Total)
	require.Equal(t, rep.Firewall.ByRule, blocks.ByRule)
	require.True(t, blocks.TrafficAnalytics)
	require.Equal(t, "stream:requests @blocked_by:*", blocks.LogsQuery)

	blocks, err = f.svc.ServiceFirewallBlocks(f.ctx, f.service, "30d")
	require.NoError(t, err)
	require.EqualValues(t, 7, blocks.Total)

	_, err = f.pool.Exec(ctx, `UPDATE servers SET traffic_analytics_enabled = false WHERE id = $1`, f.server)
	require.NoError(t, err)
	blocks, err = f.svc.ServiceFirewallBlocks(f.ctx, f.service, "24h")
	require.NoError(t, err)
	require.False(t, blocks.TrafficAnalytics, "nothing is counted while traffic analytics is off")
}
