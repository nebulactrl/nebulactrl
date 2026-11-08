package logstore

import (
	"context"
	"math"
	"math/bits"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/nebulactrl/nebulactrl/internal/agent/logstore/query"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Budget of one facets query: how much it may decompress and how long it may
// run before it reports the counts of what it read as partial. Variables so
// tests can shrink them.
var (
	facetScanBytes = int64(64) << 20
	facetDeadline  = 2 * time.Second
)

// Bounds of the counters kept while scanning. Past them a new attribute path
// is ignored, and a new value (or one too long to be a facet) goes uncounted
// while its key's Count still grows.
const (
	maxFacetAttrs    = 200
	maxFacetValues   = 1000
	maxFacetValueLen = 256
)

// builtinFacets are the facets counted for every line, in the order reported.
var builtinFacets = [...]string{"level", "stream", "status", "replica", "deployment"}

const errBadRange = queryError("logstore: facets need a time range with from before to")

// facetCounter counts the lines that carry one key and their values.
type facetCounter struct {
	count  int64
	values map[string]int64
	// numbers: how many values read as finite numbers; mixed: a value did not.
	numbers  int64
	mixed    bool
	min, max float64
}

func newFacetCounter() *facetCounter { return &facetCounter{values: map[string]int64{}} }

// see records one value of the key. first is set for the first value of the
// key on a line (an array has several), which is what Count counts.
func (c *facetCounter) see(first bool, text string, n float64, isNumber bool) {
	if first {
		c.count++
	}
	if isNumber {
		if c.numbers == 0 || n < c.min {
			c.min = n
		}
		if c.numbers == 0 || n > c.max {
			c.max = n
		}
		c.numbers++
	} else {
		c.mixed = true
	}
	if len(text) > maxFacetValueLen {
		return
	}
	if _, known := c.values[text]; known || len(c.values) < maxFacetValues {
		c.values[text]++
	}
}

// field renders the counter: the most frequent values (ties by value) and, for
// a key whose every value is a number, its range.
func (c *facetCounter) field(key string) agentprotocol.FacetField {
	values := make([]agentprotocol.FacetValue, 0, len(c.values))
	for v, n := range c.values {
		values = append(values, agentprotocol.FacetValue{Value: v, Count: n})
	}
	slices.SortFunc(values, func(a, b agentprotocol.FacetValue) int {
		if a.Count != b.Count {
			return int(b.Count - a.Count)
		}
		return strings.Compare(a.Value, b.Value)
	})
	if len(values) > agentprotocol.LogFacetsMaxValues {
		values = values[:agentprotocol.LogFacetsMaxValues]
	}
	f := agentprotocol.FacetField{Key: key, Count: c.count, Values: values}
	if c.numbers > 0 && !c.mixed {
		lo, hi := c.min, c.max
		f.Numeric, f.Min, f.Max = true, &lo, &hi
	}
	return f
}

// attrCounters counts the attribute paths of the matching lines.
type attrCounters struct {
	byPath map[string]*facetCounter
	// seen holds the paths already counted on the current line.
	seen map[string]struct{}
}

func newAttrCounters() *attrCounters {
	return &attrCounters{byPath: map[string]*facetCounter{}, seen: map[string]struct{}{}}
}

// addLine counts every scalar of the line's parsed fields. Nested objects
// flatten to dotted paths; an array counts each scalar element under its
// path. Nulls, objects inside arrays and arrays inside arrays are skipped.
func (a *attrCounters) addLine(attrs map[string]any) {
	clear(a.seen)
	a.walk("", attrs)
}

func (a *attrCounters) walk(prefix string, m map[string]any) {
	for k, v := range m {
		path := k
		if prefix != "" {
			path = prefix + "." + k
		}
		switch x := v.(type) {
		case map[string]any:
			a.walk(path, x)
		case []any:
			for _, e := range x {
				a.scalar(path, e)
			}
		default:
			a.scalar(path, v)
		}
	}
}

func (a *attrCounters) scalar(path string, v any) {
	text, ok := query.AttrText(v)
	if !ok {
		return
	}
	c := a.byPath[path]
	if c == nil {
		if len(a.byPath) >= maxFacetAttrs {
			return
		}
		c = newFacetCounter()
		a.byPath[path] = c
	}
	_, dup := a.seen[path]
	a.seen[path] = struct{}{}
	n, isNumber := query.AttrNumber(v)
	c.see(!dup, text, n, isNumber && !math.IsInf(n, 0))
}

// fields renders the most common paths, keys written with their "@".
func (a *attrCounters) fields() []agentprotocol.FacetField {
	type pathCount struct {
		path string
		c    *facetCounter
	}
	all := make([]pathCount, 0, len(a.byPath))
	for p, c := range a.byPath {
		all = append(all, pathCount{p, c})
	}
	slices.SortFunc(all, func(x, y pathCount) int {
		if x.c.count != y.c.count {
			return int(y.c.count - x.c.count)
		}
		return strings.Compare(x.path, y.path)
	})
	if len(all) > agentprotocol.LogFacetsMaxAttrs {
		all = all[:agentprotocol.LogFacetsMaxAttrs]
	}
	out := make([]agentprotocol.FacetField, len(all))
	for i, pc := range all {
		out[i] = pc.c.field("@" + pc.path)
	}
	return out
}

// Facets counts the lines of one service within [From, To) that match the
// query: the built-in facets (level, stream, status, replica, deployment), the
// attribute paths of JSON and logfmt lines and a histogram over time. stream is
// the `stream` facet of every line of this store (the log source). It reads
// newest first within a budget; when the budget or ctx ends first the result is
// Partial and covers [PartialFrom, To). Capturing is the caller's to set.
func (st *Store) Facets(ctx context.Context, q agentprotocol.LogsFacetsQuery, stream string) (agentprotocol.LogsFacetsResult, error) {
	if q.From.IsZero() || !q.From.Before(q.To) {
		return agentprotocol.LogsFacetsResult{}, errBadRange
	}
	buckets := agentprotocol.LogFacetsDefaultBuckets
	if q.Buckets > 0 && q.Buckets <= agentprotocol.LogFacetsMaxBuckets {
		buckets = q.Buckets
	} else if q.Buckets > agentprotocol.LogFacetsMaxBuckets {
		buckets = agentprotocol.LogFacetsMaxBuckets
	}

	match, perrs := query.Compile(q.Query)
	from, to := q.From.UnixNano(), q.To.UnixNano()
	span := uint64(to - from)
	width := time.Duration(span) / time.Duration(buckets)

	res := agentprotocol.LogsFacetsResult{
		Facets:    []agentprotocol.FacetField{},
		Attrs:     []agentprotocol.FacetField{},
		Histogram: make([]agentprotocol.HistogramBucket, buckets),
	}
	for i := range res.Histogram {
		res.Histogram[i].Start = q.From.UTC().Add(time.Duration(i) * width)
	}
	for _, e := range perrs {
		res.QueryErrors = append(res.QueryErrors, e.Message)
	}

	builtin := make(map[string]*facetCounter, len(builtinFacets))
	for _, key := range builtinFacets {
		builtin[key] = newFacetCounter()
	}
	attrs := newAttrCounters()
	oldest := int64(math.MaxInt64)
	cancelled := false
	opts := ScanOptions{From: from, To: to, Desc: true, MaxScanBytes: facetScanBytes, Deadline: time.Now().Add(facetDeadline)}
	scan, err := st.Scan(q.ServiceID, opts, func(r Record) bool {
		if res.Scanned&0xff == 0 && ctx.Err() != nil {
			cancelled = true
			return false
		}
		res.Scanned++
		oldest = min(oldest, r.TS)
		l := query.NewLine(query.Input{
			Line: r.Text, Stream: stream, ServiceName: q.ServiceName,
			Replica: strconv.Itoa(r.Replica), DeploymentID: r.DeploymentID,
		})
		if !match(l) {
			return true
		}
		res.Matched++
		// (ts-from)*buckets/span, in 128 bits so a long range cannot overflow.
		hi, lo := bits.Mul64(uint64(r.TS-from), uint64(buckets))
		idx, _ := bits.Div64(hi, lo, span)
		b := &res.Histogram[min(int(idx), buckets-1)]
		b.Count++
		if l.Level() == "error" || l.Status() >= 500 {
			b.Errors++
		}
		for _, key := range builtinFacets {
			if v, ok := l.Facet(key); ok {
				builtin[key].see(true, v, 0, false)
			}
		}
		attrs.addLine(l.Attrs())
		return true
	})
	if err != nil {
		return res, err
	}
	if scan.Partial || cancelled {
		res.Partial = true
		partialFrom := q.To.UTC()
		if res.Scanned > 0 {
			partialFrom = time.Unix(0, oldest).UTC()
		}
		res.PartialFrom = &partialFrom
	}
	for _, key := range builtinFacets {
		if c := builtin[key]; c.count > 0 {
			res.Facets = append(res.Facets, c.field(key))
		}
	}
	res.Attrs = attrs.fields()
	return res, nil
}
