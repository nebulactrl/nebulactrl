package traffic

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/axiomhq/hyperloglog"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

type fixedSalt []byte

func (f fixedSalt) Salt(time.Time) []byte { return f }

func newAgg() *Aggregator { return NewAggregator(NewClassifier(), fixedSalt("test-salt")) }

func addFixtures(t *testing.T, a *Aggregator) {
	t.Helper()
	for _, l := range fixtureLines(t) {
		ev, err := ParseLine(l)
		if err != nil {
			continue
		}
		a.Add(ev)
	}
}

func bucketFor(bs []agentprotocol.TrafficBucket, service, host string) *agentprotocol.TrafficBucket {
	for i := range bs {
		if bs[i].ServiceID == service && bs[i].Host == host {
			return &bs[i]
		}
	}
	return nil
}

func TestAggregatorFoldsFixtureIntoMinuteBuckets(t *testing.T) {
	a := newAgg()
	addFixtures(t, a)
	out := a.Flush(time.Unix(1761500000, 0).Add(2 * time.Minute))
	require.Len(t, out, 2, "one routed host and one unrouted host in a single minute")

	b := bucketFor(out, svcA, "app.localhost")
	require.NotNil(t, b)
	require.Equal(t, time.Unix(1761499980, 0).UTC(), b.Bucket, "bucket is the UTC minute start")
	require.EqualValues(t, 4, b.Requests)
	require.EqualValues(t, 2, b.S2xx)
	require.EqualValues(t, 1, b.S4xx)
	require.EqualValues(t, 1, b.S5xx)
	require.EqualValues(t, 1, b.Documents, "only the text/html GET counts as a document")
	require.EqualValues(t, 0, b.BotRequests)
	require.EqualValues(t, 2048+40960+9+64, b.BytesOut)
	require.Equal(t, map[string]int64{"200": 2, "404": 1, "500": 1}, b.StatusCodes)
	require.Len(t, b.LatHist, agentprotocol.LatencyBuckets)
	var n int64
	for _, c := range b.LatHist {
		n += c
	}
	require.EqualValues(t, 4, n)
	require.EqualValues(t, 1, b.Visitors, "one client, one user agent")

	paths := map[string]int64{}
	for _, e := range b.Top.Paths {
		paths[e.K] = e.N
	}
	require.Equal(t, int64(1), paths["/pricing"], "query string never reaches the aggregate")
	require.Equal(t, int64(1), paths["/api/orders/:id"], "identifiers are normalized")
	require.Equal(t, []agentprotocol.TopEntry{{K: "/pricing", N: 1, B: 2048, Ms: b.Top.Pages[0].Ms}}, b.Top.Pages)
	require.Equal(t, "news.example.org", b.Top.Referrers[0].K)
	require.Equal(t, "/favicon.ico", b.Top.NotFound[0].K)
	require.Equal(t, "Chrome", b.Top.Browsers[0].K)
	var api agentprotocol.TopEntry
	for _, e := range b.Top.Paths {
		if e.K == "/api/orders/:id" {
			api = e
		}
	}
	require.EqualValues(t, 1, api.E, "5xx is tracked per path")

	u := bucketFor(out, "", "unknown.example.net")
	require.NotNil(t, u)
	require.EqualValues(t, 1, u.Requests)
	require.EqualValues(t, 1, u.BotRequests)
	require.EqualValues(t, 0, u.Visitors, "bots never enter the visitor sketch")
	require.Empty(t, u.HLL)
	require.Equal(t, "bot", u.Top.Devices[0].K)
	require.Empty(t, u.Top.Browsers, "bots are not counted as browsers")
}

func TestAggregatorFlushOnlyCompletedMinutes(t *testing.T) {
	a := newAgg()
	base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	a.Add(Event{Time: base, ServiceID: svcA, Host: "a.example.com", Status: 200, Path: "/", Method: "GET", UserAgent: "x"})
	a.Add(Event{Time: base.Add(time.Minute), ServiceID: svcA, Host: "a.example.com", Status: 200, Path: "/", Method: "GET", UserAgent: "x"})

	got := a.Flush(base.Truncate(time.Minute).Add(time.Minute))
	require.Len(t, got, 1, "the running minute stays")
	require.Equal(t, base.Truncate(time.Minute), got[0].Bucket)
	require.Len(t, a.Flush(base.Add(5*time.Minute)), 1)
	require.Empty(t, a.Flush(base.Add(10*time.Minute)))
}

func TestAggregatorNeverShipsIdentifiers(t *testing.T) {
	a := newAgg()
	ua := "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36 secret-ua-marker"
	a.Add(Event{
		Time: time.Now(), ServiceID: svcA, Host: "a.example.com", Status: 200, Path: "/x/9f8e7d6c5b4a", Method: "GET",
		ClientIP: "198.51.100.77", UserAgent: ua, Referer: "https://ref.example.org/secret/path?token=abc", ContentType: "text/html",
	})
	out := a.Flush(time.Now().Add(2 * time.Minute))
	require.Len(t, out, 1)
	raw, err := json.Marshal(out)
	require.NoError(t, err)
	for _, forbidden := range []string{"198.51.100.77", "secret-ua-marker", "token=abc", "/secret/path", "9f8e7d6c5b4a", "test-salt"} {
		require.NotContains(t, string(raw), forbidden)
	}
	require.NotEmpty(t, out[0].HLL)
}

func TestVisitorsDistinctAndStable(t *testing.T) {
	a := newAgg()
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	for i := 0; i < 10; i++ { // same client ten times
		a.Add(Event{Time: at, ServiceID: svcA, Host: "same.example.com", Status: 200, Method: "GET", Path: "/", ClientIP: "192.0.2.1", UserAgent: "Mozilla/5.0 (X11; Linux x86_64) Firefox/125.0"})
	}
	for i := 0; i < 10; i++ { // ten distinct clients
		a.Add(Event{Time: at, ServiceID: svcA, Host: "many.example.com", Status: 200, Method: "GET", Path: "/", ClientIP: fmt.Sprintf("192.0.2.%d", i+1), UserAgent: "Mozilla/5.0 (X11; Linux x86_64) Firefox/125.0"})
	}
	out := a.Flush(at.Add(2 * time.Minute))
	require.EqualValues(t, 1, bucketFor(out, svcA, "same.example.com").Visitors)
	require.EqualValues(t, 10, bucketFor(out, svcA, "many.example.com").Visitors)
}

func TestVisitorSaltRotationSplitsDays(t *testing.T) {
	a := NewAggregator(NewClassifier(), saltPerDay{})
	ev := func(day int) Event {
		return Event{Time: time.Date(2026, 1, day, 12, 0, 0, 0, time.UTC), ServiceID: svcA, Host: "a.example.com", Status: 200, Method: "GET", Path: "/", ClientIP: "192.0.2.9", UserAgent: "Mozilla/5.0 (X11; Linux x86_64) Firefox/125.0"}
	}
	a.Add(ev(1))
	a.Add(ev(2))
	out := a.Flush(time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC))
	require.Len(t, out, 2)
	d1, d2 := hll(t, out[0].HLL), hll(t, out[1].HLL)
	require.NoError(t, d1.Merge(d2))
	require.EqualValues(t, 2, d1.Estimate(), "the same person on two days is two visitors (salt rotates daily)")
}

type saltPerDay struct{}

func (saltPerDay) Salt(t time.Time) []byte { return []byte("salt-" + t.UTC().Format("2006-01-02")) }

func hll(t *testing.T, b []byte) *hyperloglog.Sketch {
	t.Helper()
	sk, err := hyperloglog.NewSketch(agentprotocol.HLLPrecision, true)
	require.NoError(t, err)
	require.NoError(t, sk.UnmarshalBinary(b))
	return sk
}

func TestIPv6VisitorsShareTheirSlash64(t *testing.T) {
	require.Equal(t, maskIP("2001:db8:1:2:aaaa:bbbb:cccc:dddd"), maskIP("2001:db8:1:2:1111:2222:3333:4444"))
	require.NotEqual(t, maskIP("2001:db8:1:2::1"), maskIP("2001:db8:1:3::1"))
	require.Equal(t, "192.0.2.1", maskIP("192.0.2.1"))
}

func TestHLLUnionAcrossHoursMatchesSingleSketch(t *testing.T) {
	const perHour = 7000
	single := newHLL()
	var parts [3]*hyperloglog.Sketch
	for h := range parts {
		parts[h] = newHLL()
		for i := 0; i < perHour; i++ {
			// Hours overlap by half so a real union is needed.
			id := uint64(h*perHour/2 + i)
			parts[h].InsertHash(mix(id))
			single.InsertHash(mix(id))
		}
	}
	union := newHLL()
	for _, p := range parts {
		// Round-trip through the wire encoding like a stored row.
		data, err := p.MarshalBinary()
		require.NoError(t, err)
		require.NoError(t, union.Merge(hll(t, data)))
	}
	want := float64(single.Estimate())
	got := float64(union.Estimate())
	require.InEpsilon(t, want, got, 0.02, "union of hourly sketches equals the single sketch")
	// The hours cover ids 0 .. 2*perHour-1: 14000 distinct values.
	require.InEpsilon(t, float64(perHour*2), got, 0.06, "estimate is within HLL error of the true distinct count")
}

func newHLL() *hyperloglog.Sketch {
	sk, _ := hyperloglog.NewSketch(agentprotocol.HLLPrecision, true)
	return sk
}

// mix is a SplitMix64 finalizer so sequential ids look like hash output.
func mix(x uint64) uint64 {
	x += 0x9e3779b97f4a7c15
	x = (x ^ (x >> 30)) * 0xbf58476d1ce4e5b9
	x = (x ^ (x >> 27)) * 0x94d049bb133111eb
	return x ^ (x >> 31)
}

func TestTopNIsBoundedAndKeepsTheRemainderInOther(t *testing.T) {
	a := newAgg()
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	const total = 3 * liveTopCap // scan: every path distinct
	for i := 0; i < total; i++ {
		a.Add(Event{Time: at, ServiceID: svcA, Host: "a.example.com", Status: 404, Method: "GET", Path: fmt.Sprintf("/wp-%s/x", strings.Repeat("z", 1)+fmt.Sprint(i)), UserAgent: "x"})
	}
	// Hot paths must survive compaction.
	for i := 0; i < 50; i++ {
		a.Add(Event{Time: at, ServiceID: svcA, Host: "a.example.com", Status: 200, Method: "GET", Path: "/hot", UserAgent: "x"})
	}
	for _, c := range a.accs {
		require.LessOrEqual(t, len(c.paths.m), 2*liveTopCap+1, "live key set is bounded")
	}
	out := a.Flush(at.Add(2 * time.Minute))
	require.Len(t, out, 1)
	paths := out[0].Top.Paths
	require.LessOrEqual(t, len(paths), agentprotocol.TopN+1)
	require.Equal(t, "/hot", paths[0].K)
	var sum int64
	for _, e := range paths {
		sum += e.N
	}
	require.EqualValues(t, total+50, sum, "evicted keys are summed into Other, nothing is lost")
	require.Equal(t, agentprotocol.OtherKey, paths[len(paths)-1].K)
}

func TestUnroutedHostsAreCapped(t *testing.T) {
	a := newAgg()
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	for i := 0; i < maxUnroutedHosts+30; i++ {
		a.Add(Event{Time: at, Host: fmt.Sprintf("scan%d.example.net", i), Status: 404, Method: "GET", Path: "/", UserAgent: "x"})
	}
	out := a.Flush(at.Add(2 * time.Minute))
	require.Len(t, out, maxUnroutedHosts+1)
	other := bucketFor(out, "", OtherHost)
	require.NotNil(t, other)
	require.EqualValues(t, 30, other.Requests)
}

func TestSlowLatenciesLandInTheRightHistogramBucket(t *testing.T) {
	a := newAgg()
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	a.Add(Event{Time: at, ServiceID: svcA, Host: "a.example.com", Status: 200, DurationMs: 0.4, UserAgent: "x", Method: "GET", Path: "/"})
	a.Add(Event{Time: at, ServiceID: svcA, Host: "a.example.com", Status: 200, DurationMs: 120, UserAgent: "x", Method: "GET", Path: "/"})
	a.Add(Event{Time: at, ServiceID: svcA, Host: "a.example.com", Status: 200, DurationMs: 90000, UserAgent: "x", Method: "GET", Path: "/"})
	b := a.Flush(at.Add(2 * time.Minute))[0]
	require.EqualValues(t, 1, b.LatHist[0], "≤ 1 ms")
	require.EqualValues(t, 1, b.LatHist[agentprotocol.LatencyBucket(120)])
	require.EqualValues(t, 1, b.LatHist[agentprotocol.LatencyBuckets-1], "overflow bucket")
	require.InDelta(t, 90120.4, b.LatSumMs, 0.001)
}

func TestDocumentDetectionFallsBackToPathExtension(t *testing.T) {
	get := func(path, ct string, status int) Event {
		return Event{Method: "GET", Path: path, ContentType: ct, Status: status}
	}
	require.True(t, isDocument(get("/pricing", "text/html; charset=utf-8", 200)))
	require.False(t, isDocument(get("/pricing", "application/json", 200)), "a recorded Content-Type wins")
	require.False(t, isDocument(get("/pricing", "text/html", 404)))
	require.False(t, isDocument(Event{Method: "POST", Path: "/", ContentType: "text/html", Status: 200}))
	require.True(t, isDocument(get("/blog/post", "", 200)), "no Content-Type, no extension")
	require.True(t, isDocument(get("/index.html", "", 200)))
	require.False(t, isDocument(get("/assets/app.js", "", 200)))
}
