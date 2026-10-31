package agentprotocol

import (
	"math"
	"net/netip"
	"strings"
	"time"
)

// Traffic analytics wire types. The agent aggregates Traefik access logs into
// per-minute, per-host buckets; raw lines, client IPs, user agents, referrer
// URLs and query strings never leave the host.

// LatencyBounds are the upper-inclusive bounds, in milliseconds, of the first
// 27 latency histogram buckets. A 28th overflow bucket counts everything above
// the last bound. Histograms merge by element-wise addition; percentiles are
// computed from the merged counts (see LatencyQuantile), never averaged.
var LatencyBounds = [27]float64{
	1, 2, 3, 5, 7, 10, 15, 20, 30, 50, 75, 100, 150, 200, 300, 500, 750,
	1000, 1500, 2000, 3000, 5000, 7500, 10000, 15000, 30000, 60000,
}

// IPLiteralHost replaces a Host header (or referrer host) that is an IP
// address. Scanners and health probes address the proxy by IP; the address is
// never stored, in line with the no-raw-IP rule.
const IPLiteralHost = "<ip address>"

// MaskIPHost returns IPLiteralHost when h is an IPv4 or IPv6 literal (optionally
// bracketed, with an IPv6 zone), and h unchanged otherwise. h must already
// have its port removed.
func MaskIPHost(h string) string {
	t := strings.TrimSuffix(strings.TrimPrefix(h, "["), "]")
	if _, err := netip.ParseAddr(t); err == nil {
		return IPLiteralHost
	}
	return h
}

// LatencyBuckets is the number of histogram counters (27 bounds + overflow).
const LatencyBuckets = len(LatencyBounds) + 1

// HLLPrecision is the HyperLogLog precision (p=12: 4 KB dense, ~1.6 % error)
// used for visitor sketches.
const HLLPrecision = 12

// LatencyBucket returns the histogram index for a latency in milliseconds.
func LatencyBucket(ms float64) int {
	for i, b := range LatencyBounds {
		if ms <= b {
			return i
		}
	}
	return len(LatencyBounds)
}

// LatencyQuantile estimates the q-quantile (0..1) in milliseconds from
// histogram counts, interpolating in log space inside the holding bucket. It
// returns 0 for an empty histogram. The overflow bucket reports the last bound.
func LatencyQuantile(counts []int64, q float64) float64 {
	var total int64
	for _, c := range counts {
		total += c
	}
	if total == 0 {
		return 0
	}
	rank := q * float64(total)
	var cum int64
	for i, c := range counts {
		if c == 0 {
			continue
		}
		if float64(cum+c) < rank && i < len(counts)-1 {
			cum += c
			continue
		}
		if i >= len(LatencyBounds) {
			return LatencyBounds[len(LatencyBounds)-1]
		}
		lo := 0.5
		if i > 0 {
			lo = LatencyBounds[i-1]
		}
		hi := LatencyBounds[i]
		f := (rank - float64(cum)) / float64(c)
		f = math.Min(1, math.Max(0, f))
		return lo * math.Pow(hi/lo, f)
	}
	return LatencyBounds[len(LatencyBounds)-1]
}

// TopEntry is one ranked item in a top-N list. Lists carry at most TopN
// entries plus an "Other" entry holding the remainder.
type TopEntry struct {
	K  string  `json:"k"`
	N  int64   `json:"n"`
	E  int64   `json:"e,omitempty"`  // 5xx responses
	B  int64   `json:"b,omitempty"`  // bytes sent
	Ms float64 `json:"ms,omitempty"` // summed latency in ms
}

// TopN is the list length kept per dimension (excluding "Other").
const TopN = 20

// OtherKey labels the remainder entry of a top-N list.
const OtherKey = "Other"

// TrafficTop holds the top-N lists of a bucket. paths counts every request,
// pages only documents (GET/HEAD text/html 2xx/3xx).
type TrafficTop struct {
	Paths     []TopEntry `json:"paths,omitempty"`
	Pages     []TopEntry `json:"pages,omitempty"`
	Referrers []TopEntry `json:"referrers,omitempty"`
	Browsers  []TopEntry `json:"browsers,omitempty"`
	OS        []TopEntry `json:"os,omitempty"`
	Devices   []TopEntry `json:"devices,omitempty"`
	NotFound  []TopEntry `json:"notFound,omitempty"`
}

// TrafficBucket is the aggregate of one (service, host) for one UTC minute.
type TrafficBucket struct {
	// ServiceID is the nc_service stamp of the matched route; empty for
	// requests that matched no service route (unrouted).
	ServiceID string    `json:"serviceId,omitempty"`
	Host      string    `json:"host"`
	Bucket    time.Time `json:"bucket"` // start of the minute, UTC

	Requests    int64 `json:"requests"`
	Documents   int64 `json:"documents"`
	BotRequests int64 `json:"botRequests"`
	BytesIn     int64 `json:"bytesIn"`
	BytesOut    int64 `json:"bytesOut"`
	S1xx        int64 `json:"s1xx"`
	S2xx        int64 `json:"s2xx"`
	S3xx        int64 `json:"s3xx"`
	S4xx        int64 `json:"s4xx"`
	S5xx        int64 `json:"s5xx"`
	SOther      int64 `json:"sOther"`

	// LatHist has LatencyBuckets counters.
	LatHist  []int64 `json:"latHist"`
	LatSumMs float64 `json:"latSumMs"`

	// HLL is a marshaled HyperLogLog (precision HLLPrecision) of visitor
	// hashes, bots excluded. Hashes use a per-day random salt that never
	// leaves the agent.
	HLL         []byte           `json:"hll,omitempty"`
	Visitors    int64            `json:"visitors"`
	StatusCodes map[string]int64 `json:"statusCodes,omitempty"`
	Top         TrafficTop       `json:"top"`
	// Geo counts where the bucket's requests came from; nil while geolocation is off.
	Geo *GeoBucket `json:"geo,omitempty"`
}

// Traffic is the agent → control-plane frame carrying completed minutes. The
// agent keeps buckets until it receives a TrafficAck for Seq and resends them
// after a reconnect; the control plane ignores duplicates.
type Traffic struct {
	Seq     uint64          `json:"seq"`
	Buckets []TrafficBucket `json:"buckets"`
}

// TrafficAck acknowledges durable storage of a Traffic frame.
type TrafficAck struct {
	Seq uint64 `json:"seq"`
}

// TrafficRetention is how long the agent buffers unsent minutes.
const TrafficRetention = 24 * time.Hour

// MaxTrafficBuckets bounds the buckets of one Traffic frame.
const MaxTrafficBuckets = 500
