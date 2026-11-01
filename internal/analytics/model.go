// Package analytics ingests the traffic aggregates agents ship, keeps the
// minute, hour and day tiers, and serves the analytics reports: requests,
// bandwidth, visitors, latency percentiles, status codes and top lists per
// service, server or workspace. Raw access logs are never stored.
package analytics

import (
	"encoding/json"
	"sort"
	"strconv"
	"time"

	"github.com/axiomhq/hyperloglog"
	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Tier is one stored traffic resolution.
type Tier int

const (
	// TierMinute is traffic_1m (24 h).
	TierMinute Tier = iota
	// TierHour is traffic_1h.
	TierHour
	// TierDay is traffic_1d.
	TierDay
)

// Width is the bucket width of the tier.
func (t Tier) Width() time.Duration {
	switch t {
	case TierMinute:
		return time.Minute
	case TierHour:
		return time.Hour
	default:
		return 24 * time.Hour
	}
}

// MinuteRetention is how long traffic_1m rows are kept.
const MinuteRetention = 24 * time.Hour

// Top list dimensions of a stored row.
const (
	dimPaths     = "paths"
	dimPages     = "pages"
	dimReferrers = "referrers"
	dimBrowsers  = "browsers"
	dimOS        = "os"
	dimDevices   = "devices"
	dimNotFound  = "notFound"
)

var dimensions = []string{dimPaths, dimPages, dimReferrers, dimBrowsers, dimOS, dimDevices, dimNotFound}

// row is one stored traffic row of any tier.
type row struct {
	Bucket    time.Time
	ServiceID *uuid.UUID
	Host      string
	Requests  int64
	Documents int64
	Bots      int64
	BytesIn   int64
	BytesOut  int64
	// S holds the 1xx..5xx and other status class counts.
	S      [6]int64
	Hist   [agentprotocol.LatencyBuckets]int64
	LatSum float64
	HLL    []byte
	Codes  map[string]int64
	Top    agentprotocol.TrafficTop
	Geo    agentprotocol.GeoBucket
}

func rowFrom(t db.Traffic1m) row {
	r := row{
		Bucket: t.Bucket.UTC(), ServiceID: t.ServiceID, Host: t.Host, Requests: t.Requests, Documents: t.Documents,
		Bots: t.BotRequests, BytesIn: t.BytesIn, BytesOut: t.BytesOut,
		S:      [6]int64{t.S1xx, t.S2xx, t.S3xx, t.S4xx, t.S5xx, t.SOther},
		LatSum: t.LatSumMs, HLL: t.Hll, Geo: decodeGeo(t.Geo),
	}
	copy(r.Hist[:], t.LatHist)
	if len(t.StatusCodes) > 0 {
		_ = json.Unmarshal(t.StatusCodes, &r.Codes)
	}
	if len(t.Top) > 0 {
		_ = json.Unmarshal(t.Top, &r.Top)
	}
	return r
}

func rows1m(in []db.Traffic1m) []row {
	out := make([]row, len(in))
	for i, t := range in {
		out[i] = rowFrom(t)
	}
	return out
}

// The three tiers share one column layout, so their row types convert directly.

func rows1h(in []db.Traffic1h) []row {
	out := make([]row, len(in))
	for i, t := range in {
		out[i] = rowFrom(db.Traffic1m(t))
	}
	return out
}

func rows1d(in []db.Traffic1d) []row {
	out := make([]row, len(in))
	for i, t := range in {
		out[i] = rowFrom(db.Traffic1m(t))
	}
	return out
}

// sum accumulates rows. Counters, the latency histogram and the visitor sketch
// merge exactly (addition and union); the top lists merge approximately (a key
// ranked below N in every contributing row is lost to "Other").
type sum struct {
	Requests, Documents, Bots, BytesIn, BytesOut int64
	S                                            [6]int64
	Hist                                         [agentprotocol.LatencyBuckets]int64
	LatSum                                       float64

	hll   *hyperloglog.Sketch
	codes map[string]int64
	tops  map[string]map[string]*agentprotocol.TopEntry
	geo   *geoSum // request origins; only in detailed sums
}

func newSum(detail bool) *sum {
	s := &sum{}
	if detail {
		s.codes = map[string]int64{}
		s.geo = &geoSum{}
		s.tops = map[string]map[string]*agentprotocol.TopEntry{}
		for _, d := range dimensions {
			s.tops[d] = map[string]*agentprotocol.TopEntry{}
		}
	}
	return s
}

// decodeHLL parses a stored sketch; invalid or empty blobs yield nil.
func decodeHLL(b []byte) *hyperloglog.Sketch {
	if len(b) == 0 {
		return nil
	}
	sk, err := hyperloglog.NewSketch(agentprotocol.HLLPrecision, true)
	if err != nil || sk.UnmarshalBinary(b) != nil {
		return nil
	}
	return sk
}

func newSketch() *hyperloglog.Sketch {
	sk, _ := hyperloglog.NewSketch(agentprotocol.HLLPrecision, true)
	return sk
}

// add folds one row (and its already decoded sketch) into s.
func (s *sum) add(r *row, sk *hyperloglog.Sketch) {
	s.Requests += r.Requests
	s.Documents += r.Documents
	s.Bots += r.Bots
	s.BytesIn += r.BytesIn
	s.BytesOut += r.BytesOut
	for i := range s.S {
		s.S[i] += r.S[i]
	}
	for i := range s.Hist {
		s.Hist[i] += r.Hist[i]
	}
	s.LatSum += r.LatSum
	if sk != nil {
		if s.hll == nil {
			s.hll = newSketch()
		}
		_ = s.hll.Merge(sk)
	}
	if s.tops != nil {
		for k, n := range r.Codes {
			s.codes[k] += n
		}
		s.geo.add(&r.Geo)
		for dim, list := range map[string][]agentprotocol.TopEntry{
			dimPaths: r.Top.Paths, dimPages: r.Top.Pages, dimReferrers: r.Top.Referrers, dimBrowsers: r.Top.Browsers,
			dimOS: r.Top.OS, dimDevices: r.Top.Devices, dimNotFound: r.Top.NotFound,
		} {
			m := s.tops[dim]
			for _, e := range list {
				cur := m[e.K]
				if cur == nil {
					c := e
					m[e.K] = &c
					continue
				}
				cur.N += e.N
				cur.E += e.E
				cur.B += e.B
				cur.Ms += e.Ms
			}
		}
	}
}

// merge folds the counters, histogram and sketch of o into s (no top lists).
func (s *sum) merge(o *sum) {
	s.Requests += o.Requests
	s.Documents += o.Documents
	s.Bots += o.Bots
	s.BytesIn += o.BytesIn
	s.BytesOut += o.BytesOut
	for i := range s.S {
		s.S[i] += o.S[i]
	}
	for i := range s.Hist {
		s.Hist[i] += o.Hist[i]
	}
	s.LatSum += o.LatSum
	if o.hll != nil {
		if s.hll == nil {
			s.hll = newSketch()
		}
		_ = s.hll.Merge(o.hll)
	}
}

func (s *sum) visitorsEstimate() int64 {
	if s.hll == nil {
		return 0
	}
	return int64(s.hll.Estimate())
}

func (s *sum) quantile(q float64) float64 { return agentprotocol.LatencyQuantile(s.Hist[:], q) }

// ranked returns the dimension's entries sorted by count, descending. Merged
// "Other" entries stay last.
func (s *sum) ranked(dim string, limit int) []agentprotocol.TopEntry {
	m := s.tops[dim]
	var list []agentprotocol.TopEntry
	var other agentprotocol.TopEntry
	for k, e := range m {
		if k == agentprotocol.OtherKey {
			other = *e
			continue
		}
		list = append(list, *e)
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].N != list[j].N {
			return list[i].N > list[j].N
		}
		return list[i].K < list[j].K
	})
	if limit > 0 && len(list) > limit {
		for _, e := range list[limit:] {
			other.N += e.N
			other.E += e.E
			other.B += e.B
			other.Ms += e.Ms
		}
		list = list[:limit]
	}
	if other.N > 0 {
		other.K = agentprotocol.OtherKey
		list = append(list, other)
	}
	return list
}

// statusCodes returns exact status code counts, largest first.
func (s *sum) statusCodes() []StatusCount {
	out := make([]StatusCount, 0, len(s.codes))
	for k, n := range s.codes {
		code, err := strconv.Atoi(k)
		if err != nil {
			continue
		}
		out = append(out, StatusCount{Code: code, N: n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].N != out[j].N {
			return out[i].N > out[j].N
		}
		return out[i].Code < out[j].Code
	})
	return out
}
