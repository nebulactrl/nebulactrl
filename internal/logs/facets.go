package logs

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Bounds of the facets window: the last 15 minutes unless asked otherwise, at
// most the longest retention a server can be set to.
const (
	defaultFacetsWindow = 15 * time.Minute
	maxFacetsSpan       = time.Duration(agentprotocol.MaxLogRetentionDays) * 24 * time.Hour
)

// serviceFacet is the facet the environment endpoint adds: which service a
// line belongs to.
const serviceFacet = "service"

// FacetParams selects the lines whose facets are counted.
type FacetParams struct {
	From, To time.Time // zero To = now, zero From = 15 minutes before To
	Query    string
	// Sources is a comma-separated subset of runtime and requests (default runtime).
	Sources string
	// Buckets is the number of histogram buckets (0: the default, 60).
	Buckets int
}

// LogFacetsPage is the counts of the stored lines that match a query within a
// time range: how many matched, the most frequent values of every facet and
// attribute, and a histogram over time.
type LogFacetsPage struct {
	Matched int64 `json:"matched" doc:"Lines that matched the query."`
	Scanned int64 `json:"scanned" doc:"Lines that were read to find them."`
	// Partial: the agents' scan budget ended before the range was read (newest
	// first), so the counts cover [partialFrom, to) only.
	Partial     bool                            `json:"partial"`
	PartialFrom *time.Time                      `json:"partialFrom,omitempty" doc:"With partial: the counts cover only lines at or after this time."`
	Facets      []agentprotocol.FacetField      `json:"facets" doc:"level, stream, status, replica and deployment (and service for an environment): the most frequent values and their counts."`
	Attrs       []agentprotocol.FacetField      `json:"attrs" doc:"The most frequent log attributes (key with a leading @, from JSON and logfmt lines and request lines) and their top values."`
	Histogram   []agentprotocol.HistogramBucket `json:"histogram" doc:"Matching lines per equal time bucket over the range; errors counts level error or fatal and status 500 or more."`
	QueryErrors []string                        `json:"queryErrors,omitempty" doc:"The parts of the query that were ignored."`
	Unavailable []LogHistoryUnavailable         `json:"unavailable,omitempty" doc:"Services whose counts could not be read (their server is offline, for example)."`
	// Capturing is false when history capture is off on every server asked.
	Capturing         bool `json:"capturing"`
	RequestsCapturing bool `json:"requestsCapturing" doc:"True when a server asked is storing request lines."`
}

// resolveFacets checks the parameters and returns the query every agent receives
// (the same window and buckets, so histograms add up bucket by bucket) with the
// sources to ask.
func (s *Service) resolveFacets(p FacetParams) (agentprotocol.LogsFacetsQuery, []string, error) {
	sources, err := parseSources(p.Sources)
	if err != nil {
		return agentprotocol.LogsFacetsQuery{}, nil, err
	}
	if len(p.Query) > 2000 {
		return agentprotocol.LogsFacetsQuery{}, nil, bad("the query is too long (at most 2000 characters)")
	}
	if p.Buckets < 0 || p.Buckets > agentprotocol.LogFacetsMaxBuckets {
		return agentprotocol.LogsFacetsQuery{}, nil, bad(fmt.Sprintf("buckets must be between 1 and %d", agentprotocol.LogFacetsMaxBuckets))
	}
	from, to := p.From, p.To
	if to.IsZero() {
		to = s.now()
	}
	if from.IsZero() {
		from = to.Add(-defaultFacetsWindow)
	}
	if !from.Before(to) {
		return agentprotocol.LogsFacetsQuery{}, nil, bad("from must be before to")
	}
	if to.Sub(from) > maxFacetsSpan {
		return agentprotocol.LogsFacetsQuery{}, nil, bad(fmt.Sprintf("the range is too long (at most %d days)", agentprotocol.MaxLogRetentionDays))
	}
	return agentprotocol.LogsFacetsQuery{From: from, To: to, Query: p.Query, Buckets: p.Buckets}, sources, nil
}

// ServiceFacets returns the facets of one service's stored lines, of its
// runtime output, its request lines, or both (Sources).
func (s *Service) ServiceFacets(ctx context.Context, serviceID uuid.UUID, p FacetParams) (LogFacetsPage, error) {
	sc, err := services.LoadScope(ctx, s.q, policy.ScopeLogsRead, serviceID)
	if err != nil {
		return LogFacetsPage{}, err
	}
	if err := services.RequireContainer(sc, "logs"); err != nil {
		return LogFacetsPage{}, err
	}
	q, sources, err := s.resolveFacets(p)
	if err != nil {
		return LogFacetsPage{}, err
	}
	page, err := s.facets(ctx, []db.Service{sc.Service}, sources, q, false)
	if err != nil {
		return LogFacetsPage{}, err
	}
	if len(page.Unavailable) > 0 {
		return LogFacetsPage{}, unavailableError(page.Unavailable[0])
	}
	return page, nil
}

// EnvironmentFacets merges the facets of every service of an environment, with
// a service facet of their own. Services whose server is offline are listed in
// Unavailable.
func (s *Service) EnvironmentFacets(ctx context.Context, envID uuid.UUID, p FacetParams) (LogFacetsPage, error) {
	env, err := services.LoadEnvironment(ctx, s.q, policy.ScopeLogsRead, envID)
	if err != nil {
		return LogFacetsPage{}, err
	}
	q, sources, err := s.resolveFacets(p)
	if err != nil {
		return LogFacetsPage{}, err
	}
	all, err := s.q.ListAllEnvironmentServices(ctx, env.Env.ID)
	if err != nil {
		return LogFacetsPage{}, fmt.Errorf("list services: %w", err)
	}
	svcs := make([]db.Service, 0, len(all))
	for _, svc := range all {
		if svc.Kind != services.KindRoute {
			svcs = append(svcs, svc)
		}
	}
	return s.facets(ctx, svcs, sources, q, true)
}

// facetResult is the answer for one (service, source) pair.
type facetResult struct {
	svc      db.Service
	source   string
	res      agentprotocol.LogsFacetsResult
	unav     *LogHistoryUnavailable
	err      error
	answered bool // an agent replied (false: a service that was never deployed)
}

// facets asks the server of every service for every source and merges the
// answers.
func (s *Service) facets(ctx context.Context, svcs []db.Service, sources []string, q agentprotocol.LogsFacetsQuery, withService bool) (LogFacetsPage, error) {
	var asked []*facetResult
	for _, svc := range svcs {
		for _, source := range sources {
			asked = append(asked, &facetResult{svc: svc, source: source})
		}
	}
	if len(asked) == 0 {
		return mergeFacets(nil, withService), nil
	}
	forEachParallel(asked, func(r *facetResult) {
		pq := q
		pq.ServiceID, pq.ServiceName, pq.Source = r.svc.ID.String(), r.svc.Name, r.source
		never := agentprotocol.LogsFacetsResult{Facets: []agentprotocol.FacetField{}, Attrs: []agentprotocol.FacetField{}, Histogram: []agentprotocol.HistogramBucket{}, Capturing: true}
		r.res, r.answered, r.unav, r.err = askAgent(ctx, s, r.svc, r.source, agentprotocol.KindLogsFacets, pq, never)
	})

	var unavailable []LogHistoryUnavailable
	var parts []facetPart
	redactors := map[uuid.UUID]func(string) string{}
	for _, r := range asked {
		if r.err != nil {
			return LogFacetsPage{}, r.err
		}
		if r.unav != nil {
			unavailable = appendUnavailable(unavailable, *r.unav)
			continue
		}
		if _, ok := redactors[r.svc.ID]; !ok {
			red, err := s.redactor(ctx, r.svc.ID)
			if err != nil {
				return LogFacetsPage{}, err
			}
			redactors[r.svc.ID] = red
		}
		parts = append(parts, facetPart{service: r.svc.Name, source: r.source, res: r.res, never: !r.answered, redact: redactors[r.svc.ID]})
	}
	page := mergeFacets(parts, withService)
	if len(parts) == 0 {
		// Every pair was unavailable: no agent said that request logs are on.
		page.RequestsCapturing = false
	}
	page.Unavailable = unavailable
	return page, nil
}

// facetPart is one agent's answer, ready to merge.
type facetPart struct {
	service string // display name of the service the answer is about
	source  string
	res     agentprotocol.LogsFacetsResult
	// never marks the synthetic answer of a service that was never deployed:
	// no agent said anything, so it tells nothing about request capture.
	never bool
	// redact masks the secret values of the service in facet and attribute values.
	redact func(string) string
}

// mergeFacets adds the answers up: counts per facet and attribute key and
// value are summed, the most frequent values are kept, minimum and maximum
// merged, histograms summed bucket by bucket. With withService the page has a
// service facet whose values are the services and the lines of each that
// matched. No answers (nothing to ask) mean there is nothing that could be off.
func mergeFacets(parts []facetPart, withService bool) LogFacetsPage {
	page := LogFacetsPage{Facets: []agentprotocol.FacetField{}, Attrs: []agentprotocol.FacetField{}, Histogram: []agentprotocol.HistogramBucket{}}
	if len(parts) == 0 {
		page.Capturing, page.RequestsCapturing = true, true
		return page
	}
	facets, attrs := newFacetSet(), newFacetSet()
	for _, part := range parts {
		r := part.res
		page.Matched += r.Matched
		page.Scanned += r.Scanned
		if r.Partial {
			page.Partial = true
			if r.PartialFrom != nil && (page.PartialFrom == nil || r.PartialFrom.After(*page.PartialFrom)) {
				page.PartialFrom = new(*r.PartialFrom)
			}
		}
		page.QueryErrors = appendUnique(page.QueryErrors, r.QueryErrors)
		if r.Capturing {
			page.Capturing = true
			if part.source == agentprotocol.LogSourceRequests && !part.never {
				page.RequestsCapturing = true
			}
		}
		page.Histogram = addHistogram(page.Histogram, r.Histogram)
		if withService && r.Matched > 0 {
			facets.add(agentprotocol.FacetField{Key: serviceFacet, Count: r.Matched, Values: []agentprotocol.FacetValue{{Value: part.service, Count: r.Matched}}}, nil)
		}
		for _, f := range r.Facets {
			facets.add(f, part.redact)
		}
		for _, f := range r.Attrs {
			// A key the redactor changes is (part of) a secret: the entry goes,
			// a masked key would still tell the key exists and group unrelated ones.
			if part.redact != nil && part.redact(f.Key) != f.Key {
				continue
			}
			attrs.add(f, part.redact)
		}
	}
	page.Facets = facets.fields()
	sort.SliceStable(page.Facets, func(i, j int) bool { return page.Facets[i].Key == serviceFacet && page.Facets[j].Key != serviceFacet })
	page.Attrs = attrs.fields()
	sort.SliceStable(page.Attrs, func(i, j int) bool {
		if page.Attrs[i].Count != page.Attrs[j].Count {
			return page.Attrs[i].Count > page.Attrs[j].Count
		}
		return page.Attrs[i].Key < page.Attrs[j].Key
	})
	if len(page.Attrs) > agentprotocol.LogFacetsMaxAttrs {
		page.Attrs = page.Attrs[:agentprotocol.LogFacetsMaxAttrs]
	}
	return page
}

// addHistogram adds src to dst bucket by bucket (every agent got the same
// range and bucket count).
func addHistogram(dst, src []agentprotocol.HistogramBucket) []agentprotocol.HistogramBucket {
	for i, b := range src {
		if i == len(dst) {
			dst = append(dst, agentprotocol.HistogramBucket{Start: b.Start})
		}
		dst[i].Count += b.Count
		dst[i].Errors += b.Errors
	}
	return dst
}

// facetAcc is what is known of one facet or attribute key so far.
type facetAcc struct {
	count       int64
	answers     int  // answers that carry the key
	numeric     int  // of those, the ones that found only numbers
	masked      bool // a minimum or maximum would have been masked by the redactor
	min, max    *float64
	values      map[string]int64
	firstSeenAt int // position among the keys, in order of first appearance
}

// facetSet sums facet fields by key.
type facetSet struct {
	byKey map[string]*facetAcc
}

func newFacetSet() *facetSet { return &facetSet{byKey: map[string]*facetAcc{}} }

// add sums one agent's field in, redacting its values (nil: as they are).
func (fs *facetSet) add(f agentprotocol.FacetField, redact func(string) string) {
	a := fs.byKey[f.Key]
	if a == nil {
		a = &facetAcc{values: map[string]int64{}, firstSeenAt: len(fs.byKey)}
		fs.byKey[f.Key] = a
	}
	a.count += f.Count
	a.answers++
	if f.Numeric {
		a.numeric++
	}
	if redact != nil && (masksNumber(f.Min, redact) || masksNumber(f.Max, redact)) {
		a.masked = true
	}
	if f.Min != nil && (a.min == nil || *f.Min < *a.min) {
		a.min = new(*f.Min)
	}
	if f.Max != nil && (a.max == nil || *f.Max > *a.max) {
		a.max = new(*f.Max)
	}
	for _, v := range f.Values {
		value := v.Value
		if redact != nil {
			value = redact(value)
		}
		a.values[value] += v.Count
	}
}

// masksNumber reports whether the redactor would mask n as a line writes it:
// a minimum or maximum that is a secret value would leak it.
func masksNumber(n *float64, redact func(string) string) bool {
	if n == nil {
		return false
	}
	for _, format := range [...]byte{'f', 'g'} {
		s := strconv.FormatFloat(*n, format, -1, 64)
		if redact(s) != s {
			return true
		}
	}
	return false
}

// fields returns the merged fields in the order their keys first appeared,
// each with its most frequent values (LogFacetsMaxValues, most frequent first).
// A field is numeric only when every answer that carries it found numbers.
func (fs *facetSet) fields() []agentprotocol.FacetField {
	out := make([]agentprotocol.FacetField, len(fs.byKey))
	for key, a := range fs.byKey {
		values := make([]agentprotocol.FacetValue, 0, len(a.values))
		for v, n := range a.values {
			values = append(values, agentprotocol.FacetValue{Value: v, Count: n})
		}
		sort.Slice(values, func(i, j int) bool {
			if values[i].Count != values[j].Count {
				return values[i].Count > values[j].Count
			}
			return values[i].Value < values[j].Value
		})
		if len(values) > agentprotocol.LogFacetsMaxValues {
			values = values[:agentprotocol.LogFacetsMaxValues]
		}
		f := agentprotocol.FacetField{Key: key, Count: a.count, Numeric: a.numeric == a.answers, Min: a.min, Max: a.max, Values: values}
		if a.masked {
			// A bound the redactor would mask is a secret value: say nothing of the range.
			f.Numeric, f.Min, f.Max = false, nil, nil
		}
		out[a.firstSeenAt] = f
	}
	return out
}
