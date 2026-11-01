package analytics

import (
	"sort"
	"time"

	"github.com/axiomhq/hyperloglog"
	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Retention reports what the queried servers keep.
type Retention struct {
	MinuteHours int `json:"minuteHours"`
	HourlyDays  int `json:"hourlyDays"`
	DailyDays   int `json:"dailyDays"`
}

// RangeInfo describes the answered window.
type RangeInfo struct {
	From        time.Time `json:"from"`
	To          time.Time `json:"to"`
	StepSeconds int       `json:"stepSeconds"`
	Retention   Retention `json:"retention"`
	// Truncated is true when the requested range is longer than the history the
	// serving tier keeps; show "History kept for N days".
	Truncated bool `json:"truncated"`
}

// Summary holds the headline numbers of a window.
type Summary struct {
	Requests        int64   `json:"requests"`
	Documents       int64   `json:"documents" doc:"GET/HEAD text/html responses (page views)"`
	BytesOut        int64   `json:"bytesOut"`
	BytesIn         int64   `json:"bytesIn"`
	Visitors        int64   `json:"visitors" doc:"Unique non-bot visitors, counted per UTC day (a person on two days counts twice)"`
	BotRequests     int64   `json:"botRequests"`
	P50Ms           float64 `json:"p50Ms"`
	P95Ms           float64 `json:"p95Ms"`
	P99Ms           float64 `json:"p99Ms"`
	ErrorRate       float64 `json:"errorRate" doc:"5xx responses / requests"`
	ClientErrorRate float64 `json:"clientErrorRate" doc:"4xx responses / requests"`
	PeakRps         float64 `json:"peakRps" doc:"Highest per-bucket request rate at the stored resolution"`
}

// Sparklines are short series (at most 40 points) for KPI tiles.
type Sparklines struct {
	Requests []int64   `json:"requests"`
	Visitors []int64   `json:"visitors"`
	BytesOut []int64   `json:"bytesOut"`
	P95Ms    []float64 `json:"p95Ms"`
}

// SeriesPoint is one step of the main chart.
type SeriesPoint struct {
	TS       time.Time `json:"ts"`
	Requests int64     `json:"requests"`
	S2xx     int64     `json:"s2xx"`
	S3xx     int64     `json:"s3xx"`
	S4xx     int64     `json:"s4xx"`
	S5xx     int64     `json:"s5xx"`
	BytesOut int64     `json:"bytesOut"`
	Visitors int64     `json:"visitors" doc:"Approximate distinct visitors inside this step; not additive"`
	P50Ms    float64   `json:"p50Ms"`
	P95Ms    float64   `json:"p95Ms"`
	P99Ms    float64   `json:"p99Ms"`
}

// DeploymentMarker marks a deployment on the time axis.
type DeploymentMarker struct {
	ID     uuid.UUID `json:"id"`
	At     time.Time `json:"at"`
	Status string    `json:"status"`
	Sha7   string    `json:"sha7,omitempty"`
}

// StatusCount is the number of responses with one exact status code.
type StatusCount struct {
	Code int   `json:"code"`
	N    int64 `json:"n"`
}

// TopItem is one row of a ranked list.
type TopItem struct {
	K        string  `json:"k"`
	N        int64   `json:"n"`
	S5xx     int64   `json:"s5xx,omitempty"`
	BytesOut int64   `json:"bytesOut,omitempty"`
	AvgMs    float64 `json:"avgMs,omitempty"`
}

// Unrouted counts requests that matched no service route (unknown hosts,
// scanners, DNS pointing at the server before a domain exists).
type Unrouted struct {
	Requests int64     `json:"requests"`
	Hosts    []TopItem `json:"hosts"`
}

// Report is the analytics payload of a service, a server or the workspace.
type Report struct {
	Range       RangeInfo          `json:"range"`
	Summary     Summary            `json:"summary"`
	Comparison  *Summary           `json:"comparison,omitempty" doc:"Same fields for the previous equal window; present with compare=prev"`
	Sparklines  Sparklines         `json:"sparklines"`
	Series      []SeriesPoint      `json:"series"`
	Deployments []DeploymentMarker `json:"deployments"`
	StatusCodes []StatusCount      `json:"statusCodes"`
	// TopPathsKind is "pages" (documents only) or "requests" (every request).
	TopPathsKind string    `json:"topPathsKind" enum:"pages,requests"`
	TopPaths     []TopItem `json:"topPaths"`
	TopHosts     []TopItem `json:"topHosts"`
	TopReferrers []TopItem `json:"topReferrers"`
	TopBrowsers  []TopItem `json:"topBrowsers"`
	TopOS        []TopItem `json:"topOS"`
	TopDevices   []TopItem `json:"topDevices"`
	NotFound     []TopItem `json:"notFound" doc:"Most requested paths answered 404"`
	// ActiveHours is requests by weekday (0 = Sunday) and UTC hour; only for
	// ranges of 7 days or more.
	ActiveHours [][]int64 `json:"activeHours,omitempty"`
	// Unrouted is set on server reports.
	Unrouted *Unrouted `json:"unrouted,omitempty"`
	// Geo says where the window's requests came from (country, and city at the
	// city level). Absent (null) when geolocation is off for every server the
	// report reads; GeoReason then says why.
	Geo       *Geo   `json:"geo,omitempty"`
	GeoReason string `json:"geoReason,omitempty" enum:"geo_disabled,traffic_disabled" doc:"Why geo is absent"`
}

// topListLen is the length of every ranked list in a report: the length the
// agent keeps per dimension.
const topListLen = agentprotocol.TopN

type reportInput struct {
	rows         []row
	hourRows     []row // hourly rows for the active-hours heatmap (ranges ≥ 7 d)
	prev         []row // previous window, nil without compare
	compare      bool
	from, to     time.Time
	step         time.Duration
	tier         Tier
	retention    Retention
	truncated    bool
	allPaths     bool
	withUnrouted bool
	markers      []DeploymentMarker
	now          time.Time
	geo          geoSettings
}

func toItems(in []agentprotocol.TopEntry) []TopItem {
	out := make([]TopItem, 0, len(in))
	for _, e := range in {
		it := TopItem{K: e.K, N: e.N, S5xx: e.E, BytesOut: e.B}
		if e.N > 0 {
			it.AvgMs = e.Ms / float64(e.N)
		}
		out = append(out, it)
	}
	return out
}

// summarize turns a window total into headline numbers. peak is the highest
// per-bucket request count and bucket its width.
func summarize(total *sum, visitors int64, peak int64, bucket time.Duration) Summary {
	s := Summary{
		Requests: total.Requests, Documents: total.Documents, BytesOut: total.BytesOut, BytesIn: total.BytesIn,
		Visitors: visitors, BotRequests: total.Bots,
		P50Ms: total.quantile(0.5), P95Ms: total.quantile(0.95), P99Ms: total.quantile(0.99),
	}
	if total.Requests > 0 {
		s.ErrorRate = float64(total.S[4]) / float64(total.Requests)
		s.ClientErrorRate = float64(total.S[3]) / float64(total.Requests)
	}
	if bucket > 0 {
		s.PeakRps = float64(peak) / bucket.Seconds()
	}
	return s
}

// scan folds rows into a window total, per-UTC-day visitor unions and the
// busiest bucket.
type scanResult struct {
	total    *sum
	visitors int64
	peak     int64
}

func scan(rows []row, detail bool, each func(r *row, sk *hyperloglog.Sketch)) scanResult {
	res := scanResult{total: newSum(detail)}
	days := map[int64]*hyperloglog.Sketch{}
	perBucket := map[int64]int64{}
	for i := range rows {
		r := &rows[i]
		sk := decodeHLL(r.HLL)
		res.total.add(r, sk)
		if sk != nil {
			day := r.Bucket.UTC().Truncate(24 * time.Hour).Unix()
			d := days[day]
			if d == nil {
				d = newSketch()
				days[day] = d
			}
			_ = d.Merge(sk)
		}
		perBucket[r.Bucket.Unix()] += r.Requests
		if each != nil {
			each(r, sk)
		}
	}
	for _, d := range days {
		res.visitors += int64(d.Estimate())
	}
	for _, n := range perBucket {
		res.peak = max(res.peak, n)
	}
	return res
}

func build(in reportInput) Report {
	rep := Report{
		Range: RangeInfo{
			From: in.from, To: in.to, StepSeconds: int(in.step.Seconds()), Retention: in.retention, Truncated: in.truncated,
		},
		Deployments: in.markers,
	}
	if rep.Deployments == nil {
		rep.Deployments = []DeploymentMarker{}
	}

	// Series skeleton: one zero-filled sum per step, so charts have a
	// continuous x axis.
	first := in.from.UTC().Truncate(in.step)
	n := int(in.to.Sub(first)/in.step) + 1
	if n < 1 {
		n = 1
	}
	steps := make([]*sum, n)
	for i := range steps {
		steps[i] = newSum(false)
	}
	hosts := map[string]*agentprotocol.TopEntry{}
	var unroutedHosts = map[string]*agentprotocol.TopEntry{}
	var unroutedReq int64

	sc := scan(in.rows, true, func(r *row, sk *hyperloglog.Sketch) {
		if idx := int(r.Bucket.Sub(first) / in.step); idx >= 0 && idx < n {
			steps[idx].add(r, sk)
		}
		h := hosts[r.Host]
		if h == nil {
			h = &agentprotocol.TopEntry{K: r.Host}
			hosts[r.Host] = h
		}
		h.N += r.Requests
		h.E += r.S[4]
		h.B += r.BytesOut
		h.Ms += r.LatSum
		if in.withUnrouted && r.ServiceID == nil {
			unroutedReq += r.Requests
			u := unroutedHosts[r.Host]
			if u == nil {
				u = &agentprotocol.TopEntry{K: r.Host}
				unroutedHosts[r.Host] = u
			}
			u.N += r.Requests
			u.E += r.S[4]
			u.B += r.BytesOut
			u.Ms += r.LatSum
		}
	})
	rep.Summary = summarize(sc.total, sc.visitors, sc.peak, in.tier.Width())

	rep.Series = make([]SeriesPoint, n)
	for i, s := range steps {
		rep.Series[i] = SeriesPoint{
			TS: first.Add(time.Duration(i) * in.step), Requests: s.Requests,
			S2xx: s.S[1], S3xx: s.S[2], S4xx: s.S[3], S5xx: s.S[4], BytesOut: s.BytesOut, Visitors: s.visitorsEstimate(),
			P50Ms: s.quantile(0.5), P95Ms: s.quantile(0.95), P99Ms: s.quantile(0.99),
		}
	}
	rep.Sparklines = sparklines(steps)

	rep.StatusCodes = sc.total.statusCodes()
	rep.TopPathsKind = "pages"
	pages := sc.total.ranked(dimPages)
	if in.allPaths || len(pages) == 0 {
		rep.TopPathsKind = "requests"
		pages = sc.total.ranked(dimPaths)
	}
	rep.TopPaths = toItems(pages)
	rep.TopHosts = toItems(rankMap(hosts, topListLen))
	rep.TopReferrers = toItems(sc.total.ranked(dimReferrers))
	rep.TopBrowsers = toItems(sc.total.ranked(dimBrowsers))
	rep.TopOS = toItems(sc.total.ranked(dimOS))
	rep.TopDevices = toItems(sc.total.ranked(dimDevices))
	rep.NotFound = toItems(sc.total.ranked(dimNotFound))

	if in.compare {
		ps := scan(in.prev, false, nil)
		prev := summarize(ps.total, ps.visitors, ps.peak, in.tier.Width())
		rep.Comparison = &prev
	}
	if in.to.Sub(in.from) >= 7*24*time.Hour {
		rep.ActiveHours = activeHours(in.hourRows)
	}
	if in.withUnrouted {
		rep.Unrouted = &Unrouted{Requests: unroutedReq, Hosts: toItems(rankMap(unroutedHosts, 5))}
	}
	if in.geo.enabled {
		rep.Geo = buildGeo(in.geo, sc.total.geo)
	} else {
		rep.GeoReason = in.geo.reason
	}
	return rep
}

func rankMap(m map[string]*agentprotocol.TopEntry, limit int) []agentprotocol.TopEntry {
	list := make([]agentprotocol.TopEntry, 0, len(m))
	for _, e := range m {
		list = append(list, *e)
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].N != list[j].N {
			return list[i].N > list[j].N
		}
		return list[i].K < list[j].K
	})
	if len(list) > limit {
		list = list[:limit]
	}
	return list
}

// maxSparkPoints bounds a sparkline.
const maxSparkPoints = 40

// sparklines regroups the steps into at most maxSparkPoints groups, merging
// counters, histograms and sketches (never averaging percentiles).
func sparklines(steps []*sum) Sparklines {
	group := (len(steps) + maxSparkPoints - 1) / maxSparkPoints
	if group < 1 {
		group = 1
	}
	sp := Sparklines{Requests: []int64{}, Visitors: []int64{}, BytesOut: []int64{}, P95Ms: []float64{}}
	for i := 0; i < len(steps); i += group {
		merged := newSum(false)
		for _, s := range steps[i:min(i+group, len(steps))] {
			merged.merge(s)
		}
		sp.Requests = append(sp.Requests, merged.Requests)
		sp.Visitors = append(sp.Visitors, merged.visitorsEstimate())
		sp.BytesOut = append(sp.BytesOut, merged.BytesOut)
		sp.P95Ms = append(sp.P95Ms, merged.quantile(0.95))
	}
	return sp
}

// activeHours is the 7×24 request heatmap (weekday 0 = Sunday, UTC hour).
func activeHours(rows []row) [][]int64 {
	grid := make([][]int64, 7)
	for i := range grid {
		grid[i] = make([]int64, 24)
	}
	for i := range rows {
		t := rows[i].Bucket.UTC()
		grid[int(t.Weekday())][t.Hour()] += rows[i].Requests
	}
	return grid
}
