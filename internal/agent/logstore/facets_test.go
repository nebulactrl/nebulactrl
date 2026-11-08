package logstore

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// seedRequests stores 100 request lines, one per second from the returned base:
// every 4th is a POST (the others GET), status 503 when i%20 == 5, 404 when
// i%10 == 3 and 200 otherwise, duration_ms 10+i, replica i%2, deployment d1
// for the first 50 lines and d2 for the rest.
func seedRequests(t *testing.T) (*Store, time.Time) {
	t.Helper()
	ck := &clock{t: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)}
	st := openTest(t, t.TempDir(), ck, nil)
	base := ck.now()
	var recs []Record
	for i := range 100 {
		rec := agentprotocol.RequestRecord{Method: "GET", Host: "a.example", Path: "/api/x", Status: 200, DurationMs: float64(10 + i), Bytes: 512}
		if i%4 == 0 {
			rec.Method = "POST"
		}
		switch {
		case i%20 == 5:
			rec.Status = 503
		case i%10 == 3:
			rec.Status = 404
		}
		text, err := json.Marshal(rec)
		if err != nil {
			t.Fatal(err)
		}
		dep := "d1"
		if i >= 50 {
			dep = "d2"
		}
		recs = append(recs, Record{TS: base.Add(time.Duration(i) * time.Second).UnixNano(), Replica: i % 2, DeploymentID: dep, Text: string(text)})
	}
	if err := st.Append("svc1", recs); err != nil {
		t.Fatal(err)
	}
	if err := st.Flush(); err != nil {
		t.Fatal(err)
	}
	return st, base
}

// seedRuntime stores 30 runtime lines, one per second from the returned base:
// a logfmt error line, a JSON info line and a plain line, in turn.
func seedRuntime(t *testing.T) (*Store, time.Time) {
	t.Helper()
	ck := &clock{t: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)}
	st := openTest(t, t.TempDir(), ck, nil)
	base := ck.now()
	var recs []Record
	for i := range 30 {
		var text string
		switch i % 3 {
		case 0:
			text = fmt.Sprintf("level=error code=E1 took_ms=%d", i)
		case 1:
			text = `{"level":"info","msg":"ok","user":{"id":7},"tags":["a","b"]}`
		default:
			text = "plain line"
		}
		recs = append(recs, Record{TS: base.Add(time.Duration(i) * time.Second).UnixNano(), DeploymentID: "d1", Text: text})
	}
	if err := st.Append("rt", recs); err != nil {
		t.Fatal(err)
	}
	if err := st.Flush(); err != nil {
		t.Fatal(err)
	}
	return st, base
}

func facetOf(fs []agentprotocol.FacetField, key string) *agentprotocol.FacetField {
	for i := range fs {
		if fs[i].Key == key {
			return &fs[i]
		}
	}
	return nil
}

func keysOf(fs []agentprotocol.FacetField) []string {
	keys := make([]string, len(fs))
	for i, f := range fs {
		keys[i] = f.Key
	}
	return keys
}

func sumHistogram(h []agentprotocol.HistogramBucket) (count, errors int64) {
	for _, b := range h {
		count += b.Count
		errors += b.Errors
	}
	return count, errors
}

func requestFacets(t *testing.T, st *Store, base time.Time, q string, buckets int) agentprotocol.LogsFacetsResult {
	t.Helper()
	res, err := st.Facets(context.Background(), agentprotocol.LogsFacetsQuery{
		ServiceID: "svc1", From: base, To: base.Add(100 * time.Second), Query: q, Buckets: buckets,
	}, agentprotocol.StreamRequests)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestFacetsCountRequestLines(t *testing.T) {
	st, base := seedRequests(t)
	res := requestFacets(t, st, base, "", 10)
	if res.Matched != 100 || res.Scanned != 100 || res.Partial || res.PartialFrom != nil {
		t.Fatalf("counts %+v", res)
	}

	// Request lines state no level; the other built-in facets keep their order.
	if got, want := keysOf(res.Facets), []string{"stream", "status", "replica", "deployment"}; !slices.Equal(got, want) {
		t.Fatalf("facet keys %v, want %v", got, want)
	}
	stream := facetOf(res.Facets, "stream")
	if stream.Count != 100 || len(stream.Values) != 1 || stream.Values[0] != (agentprotocol.FacetValue{Value: "requests", Count: 100}) {
		t.Fatalf("stream %+v", stream)
	}
	status := facetOf(res.Facets, "status")
	if want := []agentprotocol.FacetValue{{Value: "200", Count: 85}, {Value: "404", Count: 10}, {Value: "503", Count: 5}}; !slices.Equal(status.Values, want) || status.Numeric {
		t.Fatalf("status %+v", status)
	}
	// Equal counts order by value.
	replica := facetOf(res.Facets, "replica")
	if want := []agentprotocol.FacetValue{{Value: "0", Count: 50}, {Value: "1", Count: 50}}; !slices.Equal(replica.Values, want) {
		t.Fatalf("replica %+v", replica)
	}
	deployment := facetOf(res.Facets, "deployment")
	if want := []agentprotocol.FacetValue{{Value: "d1", Count: 50}, {Value: "d2", Count: 50}}; !slices.Equal(deployment.Values, want) {
		t.Fatalf("deployment %+v", deployment)
	}

	method := facetOf(res.Attrs, "@method")
	if method == nil || method.Count != 100 || method.Numeric || method.Min != nil {
		t.Fatalf("@method %+v", method)
	}
	if want := []agentprotocol.FacetValue{{Value: "GET", Count: 75}, {Value: "POST", Count: 25}}; !slices.Equal(method.Values, want) {
		t.Fatalf("@method values %+v", method.Values)
	}
	duration := facetOf(res.Attrs, "@duration_ms")
	if duration == nil || !duration.Numeric || duration.Count != 100 || *duration.Min != 10 || *duration.Max != 109 {
		t.Fatalf("@duration_ms %+v", duration)
	}
	if path := facetOf(res.Attrs, "@path"); path == nil || len(path.Values) != 1 || path.Values[0].Value != "/api/x" {
		t.Fatalf("@path %+v", path)
	}
	// The attribute list is the most common paths first, ties by key.
	for i := 1; i < len(res.Attrs); i++ {
		a, b := res.Attrs[i-1], res.Attrs[i]
		if a.Count < b.Count || (a.Count == b.Count && a.Key >= b.Key) {
			t.Fatalf("attrs out of order at %d: %s (%d), %s (%d)", i, a.Key, a.Count, b.Key, b.Count)
		}
	}
}

func TestFacetsHistogramPlacesLinesAndErrors(t *testing.T) {
	st, base := seedRequests(t)
	res := requestFacets(t, st, base, "", 10)
	if len(res.Histogram) != 10 {
		t.Fatalf("%d buckets", len(res.Histogram))
	}
	for i, b := range res.Histogram {
		// 503s are the lines 5, 25, 45, 65 and 85: one in each even bucket.
		wantErrors := int64(1 - i%2)
		if !b.Start.Equal(base.Add(time.Duration(i)*10*time.Second)) || b.Count != 10 || b.Errors != wantErrors {
			t.Fatalf("bucket %d: %+v", i, b)
		}
	}
	if count, errs := sumHistogram(res.Histogram); count != res.Matched || errs != 5 {
		t.Fatalf("histogram sums %d lines, %d errors; matched %d", count, errs, res.Matched)
	}

	// A bucket count that does not divide the range still places every line once.
	res = requestFacets(t, st, base, "", 7)
	if count, _ := sumHistogram(res.Histogram); len(res.Histogram) != 7 || count != 100 {
		t.Fatalf("7 buckets: %d entries, %d lines", len(res.Histogram), count)
	}
	if last := res.Histogram[6]; last.Count == 0 {
		t.Fatalf("last bucket is empty: %+v", last)
	}
}

func TestFacetsClampTheBucketCount(t *testing.T) {
	st, base := seedRequests(t)
	for buckets, want := range map[int]int{
		0:                                     agentprotocol.LogFacetsDefaultBuckets,
		-3:                                    agentprotocol.LogFacetsDefaultBuckets,
		5:                                     5,
		agentprotocol.LogFacetsMaxBuckets:     agentprotocol.LogFacetsMaxBuckets,
		agentprotocol.LogFacetsMaxBuckets * 2: agentprotocol.LogFacetsMaxBuckets,
	} {
		if got := len(requestFacets(t, st, base, "", buckets).Histogram); got != want {
			t.Errorf("buckets %d: %d entries, want %d", buckets, got, want)
		}
	}
}

func TestFacetsNarrowWithTheQuery(t *testing.T) {
	st, base := seedRequests(t)

	res := requestFacets(t, st, base, "status:5*", 10)
	if res.Matched != 5 || res.Scanned != 100 {
		t.Fatalf("status:5* matched %d of %d", res.Matched, res.Scanned)
	}
	if status := facetOf(res.Facets, "status"); status.Count != 5 || !slices.Equal(status.Values, []agentprotocol.FacetValue{{Value: "503", Count: 5}}) {
		t.Fatalf("status %+v", status)
	}
	if count, errs := sumHistogram(res.Histogram); count != 5 || errs != 5 {
		t.Fatalf("histogram %d lines, %d errors", count, errs)
	}

	res = requestFacets(t, st, base, "@method:POST", 10)
	if res.Matched != 25 {
		t.Fatalf("@method:POST matched %d", res.Matched)
	}
	method := facetOf(res.Attrs, "@method")
	if method.Count != 25 || !slices.Equal(method.Values, []agentprotocol.FacetValue{{Value: "POST", Count: 25}}) {
		t.Fatalf("@method %+v", method)
	}
	if duration := facetOf(res.Attrs, "@duration_ms"); *duration.Min != 10 || *duration.Max != 106 {
		t.Fatalf("POST @duration_ms %+v", duration)
	}

	// Nothing matches: every list is empty but the histogram keeps its shape.
	res = requestFacets(t, st, base, "@method:POST status:5*", 10)
	if res.Matched != 0 || len(res.Facets) != 0 || len(res.Attrs) != 0 || len(res.Histogram) != 10 {
		t.Fatalf("empty result %+v", res)
	}
	if res.Facets == nil || res.Attrs == nil {
		t.Fatal("empty lists must not be nil")
	}

	// A broken query filters by its valid part and reports what it ignored.
	res = requestFacets(t, st, base, "@method:POST (", 10)
	if res.Matched != 25 || len(res.QueryErrors) == 0 {
		t.Fatalf("broken query: matched %d, errors %v", res.Matched, res.QueryErrors)
	}

	// The stream facet is the stream the caller names, so it narrows too.
	if res = requestFacets(t, st, base, "stream:requests", 10); res.Matched != 100 {
		t.Fatalf("stream:requests matched %d", res.Matched)
	}
	if res = requestFacets(t, st, base, "stream:runtime", 10); res.Matched != 0 {
		t.Fatalf("stream:runtime matched %d", res.Matched)
	}
}

func TestFacetsOfRuntimeLines(t *testing.T) {
	st, base := seedRuntime(t)
	res, err := st.Facets(context.Background(), agentprotocol.LogsFacetsQuery{
		ServiceID: "rt", From: base, To: base.Add(30 * time.Second), Buckets: 3,
	}, agentprotocol.StreamRuntime)
	if err != nil {
		t.Fatal(err)
	}
	if res.Matched != 30 || res.Scanned != 30 {
		t.Fatalf("counts %+v", res)
	}
	if got, want := keysOf(res.Facets), []string{"level", "stream", "replica", "deployment"}; !slices.Equal(got, want) {
		t.Fatalf("facet keys %v, want %v", got, want)
	}
	level := facetOf(res.Facets, "level")
	if want := []agentprotocol.FacetValue{{Value: "error", Count: 10}, {Value: "info", Count: 10}}; level.Count != 20 || !slices.Equal(level.Values, want) {
		t.Fatalf("level %+v", level)
	}
	if stream := facetOf(res.Facets, "stream"); stream.Values[0].Value != "runtime" || stream.Count != 30 {
		t.Fatalf("stream %+v", stream)
	}
	// Errors are the level:error lines: the logfmt ones, i%3 == 0.
	wantErrors := []int64{4, 3, 3}
	for i, b := range res.Histogram {
		if b.Count != 10 || b.Errors != wantErrors[i] {
			t.Fatalf("bucket %d: %+v", i, b)
		}
	}

	// Attributes of both formats, nested objects flattened, arrays by element,
	// most common path first and ties by key.
	if got, want := keysOf(res.Attrs), []string{"@level", "@code", "@msg", "@tags", "@took_ms", "@user.id"}; !slices.Equal(got, want) {
		t.Fatalf("attr keys %v, want %v", got, want)
	}
	code := facetOf(res.Attrs, "@code")
	if code.Count != 10 || code.Numeric || code.Min != nil || code.Max != nil || !slices.Equal(code.Values, []agentprotocol.FacetValue{{Value: "E1", Count: 10}}) {
		t.Fatalf("@code %+v", code)
	}
	tags := facetOf(res.Attrs, "@tags")
	if want := []agentprotocol.FacetValue{{Value: "a", Count: 10}, {Value: "b", Count: 10}}; tags.Count != 10 || !slices.Equal(tags.Values, want) {
		t.Fatalf("@tags %+v", tags)
	}
	// logfmt values are text, but a value that reads as a number counts as one.
	took := facetOf(res.Attrs, "@took_ms")
	if !took.Numeric || took.Count != 10 || *took.Min != 0 || *took.Max != 27 {
		t.Fatalf("@took_ms %+v", took)
	}
	user := facetOf(res.Attrs, "@user.id")
	if !user.Numeric || user.Count != 10 || *user.Min != 7 || *user.Max != 7 {
		t.Fatalf("@user.id %+v", user)
	}
}

func TestFacetsAttrIsNumericOnlyWhenEveryValueIs(t *testing.T) {
	ck := &clock{t: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)}
	st := openTest(t, t.TempDir(), ck, nil)
	base := ck.now()
	var recs []Record
	for i, text := range []string{`{"n":5}`, `{"n":"x"}`, `{"n":7}`, `{"m":1e999}`, `{"m":3}`} {
		recs = append(recs, Record{TS: base.Add(time.Duration(i) * time.Second).UnixNano(), Text: text})
	}
	if err := st.Append("svc1", recs); err != nil {
		t.Fatal(err)
	}
	res, err := st.Facets(context.Background(), agentprotocol.LogsFacetsQuery{ServiceID: "svc1", From: base, To: base.Add(time.Minute)}, agentprotocol.StreamRuntime)
	if err != nil {
		t.Fatal(err)
	}
	n := facetOf(res.Attrs, "@n")
	if n.Count != 3 || n.Numeric || n.Min != nil || n.Max != nil {
		t.Fatalf("@n %+v", n)
	}
	// An infinite value has no JSON form, so it makes the key non-numeric.
	if m := facetOf(res.Attrs, "@m"); m.Numeric || m.Min != nil {
		t.Fatalf("@m %+v", m)
	}
	if _, err := json.Marshal(res); err != nil {
		t.Fatalf("result does not marshal: %v", err)
	}
}

func TestFacetsReportOnlyTheTopValues(t *testing.T) {
	ck := &clock{t: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)}
	st := openTest(t, t.TempDir(), ck, nil)
	base := ck.now()
	var recs []Record
	for i := range 1200 {
		// The two newest lines share "v1198"; every other value is distinct.
		recs = append(recs, Record{TS: base.Add(time.Duration(i) * time.Millisecond).UnixNano(), Text: fmt.Sprintf(`{"id":"v%04d","k":"x"}`, min(i, 1198))})
	}
	if err := st.Append("svc1", recs); err != nil {
		t.Fatal(err)
	}
	res, err := st.Facets(context.Background(), agentprotocol.LogsFacetsQuery{ServiceID: "svc1", From: base, To: base.Add(time.Minute)}, agentprotocol.StreamRuntime)
	if err != nil {
		t.Fatal(err)
	}
	id := facetOf(res.Attrs, "@id")
	if id.Count != 1200 || len(id.Values) != agentprotocol.LogFacetsMaxValues {
		t.Fatalf("@id count %d, %d values", id.Count, len(id.Values))
	}
	// Newest first, only the first 1000 distinct values are counted: v1198 down to v0199.
	if id.Values[0] != (agentprotocol.FacetValue{Value: "v1198", Count: 2}) || id.Values[1] != (agentprotocol.FacetValue{Value: "v0199", Count: 1}) {
		t.Fatalf("@id values %+v", id.Values[:2])
	}
}

func TestFacetCounterBoundsItsValues(t *testing.T) {
	c := newFacetCounter()
	for i := range 2 * maxFacetValues {
		c.see(true, fmt.Sprintf("v%d", i), 0, false)
	}
	c.see(true, "v0", 0, false) // a value already counted keeps counting
	long := make([]byte, maxFacetValueLen+1)
	c.see(true, string(long), 0, false)
	if c.count != 2*maxFacetValues+2 || len(c.values) != maxFacetValues || c.values["v0"] != 2 {
		t.Fatalf("count %d, %d values, v0 %d", c.count, len(c.values), c.values["v0"])
	}

	a := newAttrCounters()
	for i := range 2 * maxFacetAttrs {
		a.addLine(map[string]any{fmt.Sprintf("k%d", i): "x"})
	}
	if len(a.byPath) != maxFacetAttrs {
		t.Fatalf("%d attribute paths", len(a.byPath))
	}
}

func TestFacetsCountAKeyOncePerLine(t *testing.T) {
	a := newAttrCounters()
	// "a.b" is both a flat key and a nested path: one line, one count.
	a.addLine(map[string]any{"a.b": "x", "a": map[string]any{"b": "y"}, "t": []any{"p", "p", "q"}})
	if c := a.byPath["a.b"]; c.count != 1 || c.values["x"] != 1 || c.values["y"] != 1 {
		t.Fatalf("a.b %+v", c)
	}
	if c := a.byPath["t"]; c.count != 1 || c.values["p"] != 2 || c.values["q"] != 1 {
		t.Fatalf("t %+v", c)
	}
}

func TestFacetsPartialWhenTheBudgetEnds(t *testing.T) {
	st, base := seedRequests(t)
	old := facetScanBytes
	facetScanBytes = 1 // the first block exhausts it
	t.Cleanup(func() { facetScanBytes = old })

	res := requestFacets(t, st, base, "", 10)
	if !res.Partial || res.PartialFrom == nil || res.Scanned == 0 || res.Scanned >= 100 {
		t.Fatalf("partial %+v", res)
	}
	// Newest first: what was read is the newest Scanned lines.
	if want := base.Add(time.Duration(100-res.Scanned) * time.Second); !res.PartialFrom.Equal(want) {
		t.Fatalf("partialFrom %v, want %v", res.PartialFrom, want)
	}
	if count, _ := sumHistogram(res.Histogram); count != res.Matched || res.Matched != res.Scanned {
		t.Fatalf("matched %d, scanned %d, histogram %d", res.Matched, res.Scanned, count)
	}

	facetScanBytes = old
	if res = requestFacets(t, st, base, "", 10); res.Partial || res.PartialFrom != nil {
		t.Fatalf("a full budget reads everything: %+v", res)
	}
}

func TestFacetsPartialWhenTheContextEnds(t *testing.T) {
	st, base := seedRequests(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res, err := st.Facets(ctx, agentprotocol.LogsFacetsQuery{ServiceID: "svc1", From: base, To: base.Add(100 * time.Second)}, agentprotocol.StreamRequests)
	if err != nil {
		t.Fatal(err)
	}
	// Nothing was read: the counts cover [To, To).
	if !res.Partial || res.Scanned != 0 || res.PartialFrom == nil || !res.PartialFrom.Equal(base.Add(100*time.Second)) {
		t.Fatalf("cancelled %+v", res)
	}
}

func TestFacetsRejectABadRange(t *testing.T) {
	st, base := seedRequests(t)
	for name, q := range map[string]agentprotocol.LogsFacetsQuery{
		"empty":    {ServiceID: "svc1", From: base, To: base},
		"reversed": {ServiceID: "svc1", From: base.Add(time.Minute), To: base},
		"no from":  {ServiceID: "svc1", To: base},
		"no range": {ServiceID: "svc1"},
	} {
		if _, err := st.Facets(context.Background(), q, agentprotocol.StreamRequests); err == nil {
			t.Errorf("%s: a bad range was accepted", name)
		}
	}
	// A service the agent has never seen is empty, not an error.
	res, err := st.Facets(context.Background(), agentprotocol.LogsFacetsQuery{ServiceID: "ghost", From: base, To: base.Add(time.Minute)}, agentprotocol.StreamRequests)
	if err != nil || res.Matched != 0 || res.Partial || len(res.Histogram) != agentprotocol.LogFacetsDefaultBuckets {
		t.Fatalf("ghost: %+v %v", res, err)
	}
}

func TestFacetsRangeIsHalfOpen(t *testing.T) {
	st, base := seedRequests(t)
	res, err := st.Facets(context.Background(), agentprotocol.LogsFacetsQuery{
		ServiceID: "svc1", From: base.Add(10 * time.Second), To: base.Add(20 * time.Second), Buckets: 2,
	}, agentprotocol.StreamRequests)
	if err != nil {
		t.Fatal(err)
	}
	if res.Matched != 10 || res.Histogram[0].Count != 5 || res.Histogram[1].Count != 5 {
		t.Fatalf("range %+v", res)
	}
	if duration := facetOf(res.Attrs, "@duration_ms"); *duration.Min != 20 || *duration.Max != 29 {
		t.Fatalf("@duration_ms %+v", duration)
	}
}
