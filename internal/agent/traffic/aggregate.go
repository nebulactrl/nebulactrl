package traffic

import (
	"crypto/sha256"
	"encoding/binary"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/axiomhq/hyperloglog"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

const (
	// maxAccumulators bounds live (minute, service, host) accumulators; events
	// beyond it are discarded.
	maxAccumulators = 2000
	// maxUnroutedHosts bounds distinct unrouted hosts per minute (scanners send
	// arbitrary Host headers); the rest share OtherHost.
	maxUnroutedHosts = 50
	// OtherHost aggregates unrouted hosts beyond maxUnroutedHosts.
	OtherHost = "(other)"
	// maxStatusCodes caps exact status codes per bucket.
	maxStatusCodes = 64
	// liveTopCap is the live key cap of a top-N map before it is compacted.
	liveTopCap = 2000
)

// SaltSource returns the random visitor-hash salt for a UTC day. It must be
// stable for the whole day (so a restart does not split visitors) and must
// never leave the agent.
type SaltSource interface {
	Salt(day time.Time) []byte
}

// Aggregator folds events into per-minute buckets.
type Aggregator struct {
	mu       sync.Mutex
	cls      *Classifier
	salts    SaltSource
	accs     map[accKey]*acc
	unrouted map[int64]map[string]struct{}
	geo      GeoLocator // nil or inactive: no origin counting; set by SetGeo before Add
}

type accKey struct {
	minute  int64
	service string
	host    string
}

type acc struct {
	requests, documents, bots int64
	bytesIn, bytesOut         int64
	s                         [6]int64 // 1xx..5xx, other
	codes                     map[int]int64
	hist                      [agentprotocol.LatencyBuckets]int64
	latSum                    float64
	hll                       *hyperloglog.Sketch

	paths, pages, referrers, browsers, oses, devices, notFound *topN
	geo                                                        *geoAcc // request origins; nil until the first request counted while geolocation is on
}

// NewAggregator returns an Aggregator using cls and salts.
func NewAggregator(cls *Classifier, salts SaltSource) *Aggregator {
	return &Aggregator{
		cls: cls, salts: salts,
		accs:     map[accKey]*acc{},
		unrouted: map[int64]map[string]struct{}{},
	}
}

func newAcc() *acc {
	return &acc{
		codes: map[int]int64{},
		paths: newTopN(), pages: newTopN(), referrers: newTopN(), browsers: newTopN(),
		oses: newTopN(), devices: newTopN(), notFound: newTopN(),
	}
}

// Add counts one request.
func (a *Aggregator) Add(ev Event) {
	minute := ev.Time.UTC().Truncate(time.Minute).Unix()
	client := a.cls.Classify(ev.UserAgent)
	org := a.locate(ev.ClientIP)

	a.mu.Lock()
	defer a.mu.Unlock()

	host := ev.Host
	if ev.ServiceID == "" {
		set := a.unrouted[minute]
		if set == nil {
			set = map[string]struct{}{}
			a.unrouted[minute] = set
		}
		if _, ok := set[host]; !ok {
			if len(set) >= maxUnroutedHosts {
				host = OtherHost
			} else {
				set[host] = struct{}{}
			}
		}
	}
	key := accKey{minute: minute, service: ev.ServiceID, host: host}
	c := a.accs[key]
	if c == nil {
		if len(a.accs) >= maxAccumulators {
			return
		}
		c = newAcc()
		a.accs[key] = c
	}

	c.requests++
	c.bytesIn += ev.BytesIn
	c.bytesOut += ev.BytesOut
	switch class := ev.Status / 100; {
	case class >= 1 && class <= 5:
		c.s[class-1]++
	default:
		c.s[5]++
	}
	if len(c.codes) < maxStatusCodes || c.codes[ev.Status] > 0 {
		c.codes[ev.Status]++
	}
	c.hist[agentprotocol.LatencyBucket(ev.DurationMs)]++
	c.latSum += ev.DurationMs
	if org.active {
		if c.geo == nil {
			c.geo = &geoAcc{}
		}
		c.geo.count(org)
	}

	path := NormalizePath(ev.Path)
	e5 := int64(0)
	if ev.Status >= 500 {
		e5 = 1
	}
	c.paths.add(path, 1, e5, ev.BytesOut, ev.DurationMs)
	c.devices.add(client.Device, 1, e5, ev.BytesOut, ev.DurationMs)
	if ev.Status == 404 {
		c.notFound.add(path, 1, 0, 0, 0)
	}

	document := isDocument(ev)
	if document {
		c.documents++
		c.pages.add(path, 1, e5, ev.BytesOut, ev.DurationMs)
	}

	if client.Bot {
		c.bots++
		return
	}
	c.browsers.add(client.Browser, 1, 0, 0, 0)
	c.oses.add(client.OS, 1, 0, 0, 0)
	if document {
		if ref := ReferrerHost(ev.Referer, ev.Host); ref != "" {
			c.referrers.add(ref, 1, 0, 0, 0)
		}
	}
	if c.hll == nil {
		c.hll, _ = hyperloglog.NewSketch(agentprotocol.HLLPrecision, true)
	}
	c.hll.InsertHash(a.visitorHash(ev))
}

// isDocument reports whether a request is a page view: GET/HEAD, 2xx/3xx and an
// HTML response. Without a recorded Content-Type (the proxy flag is missing)
// a path with no file extension stands in for one.
func isDocument(ev Event) bool {
	if (ev.Method != "GET" && ev.Method != "HEAD") || ev.Status < 200 || ev.Status >= 400 {
		return false
	}
	if ct := strings.TrimSpace(ev.ContentType); ct != "" {
		return strings.HasPrefix(strings.ToLower(ct), "text/html")
	}
	last := ev.Path[strings.LastIndexByte(ev.Path, '/')+1:]
	return !strings.Contains(last, ".") || strings.HasSuffix(last, ".html")
}

// visitorHash is SHA-256(salt ‖ host ‖ ip ‖ user-agent), truncated to 64 bits.
// The salt is random per UTC day and never leaves the agent, so the HLL
// registers cannot be reversed or linked across days.
func (a *Aggregator) visitorHash(ev Event) uint64 {
	h := sha256.New()
	h.Write(a.salts.Salt(ev.Time.UTC()))
	h.Write([]byte{0})
	h.Write([]byte(ev.Host))
	h.Write([]byte{0})
	h.Write([]byte(maskIP(ev.ClientIP)))
	h.Write([]byte{0})
	h.Write([]byte(ev.UserAgent))
	var sum [sha256.Size]byte
	return binary.LittleEndian.Uint64(h.Sum(sum[:0])[:8])
}

// maskIP truncates IPv6 addresses to their /64 (privacy extensions rotate the
// lower 64 bits); IPv4 and unparsable values are returned as is.
func maskIP(ip string) string {
	a, err := netip.ParseAddr(ip)
	if err != nil || !a.Is6() || a.Is4In6() {
		return ip
	}
	p, err := a.Prefix(64)
	if err != nil {
		return ip
	}
	return p.Addr().String()
}

// Flush removes and returns every bucket whose minute starts before cutoff
// (typically the start of the current minute), oldest first.
func (a *Aggregator) Flush(cutoff time.Time) []agentprotocol.TrafficBucket {
	limit := cutoff.UTC().Truncate(time.Minute).Unix()
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []agentprotocol.TrafficBucket
	for k, c := range a.accs {
		if k.minute >= limit {
			continue
		}
		out = append(out, c.bucket(k))
		delete(a.accs, k)
	}
	for m := range a.unrouted {
		if m < limit {
			delete(a.unrouted, m)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Bucket.Equal(out[j].Bucket) {
			return out[i].Bucket.Before(out[j].Bucket)
		}
		if out[i].Host != out[j].Host {
			return out[i].Host < out[j].Host
		}
		return out[i].ServiceID < out[j].ServiceID
	})
	return out
}

func (c *acc) bucket(k accKey) agentprotocol.TrafficBucket {
	b := agentprotocol.TrafficBucket{
		ServiceID: k.service, Host: k.host, Bucket: time.Unix(k.minute, 0).UTC(),
		Requests: c.requests, Documents: c.documents, BotRequests: c.bots,
		BytesIn: c.bytesIn, BytesOut: c.bytesOut,
		S1xx: c.s[0], S2xx: c.s[1], S3xx: c.s[2], S4xx: c.s[3], S5xx: c.s[4], SOther: c.s[5],
		LatHist: append([]int64(nil), c.hist[:]...), LatSumMs: c.latSum,
	}
	if c.hll != nil {
		if data, err := c.hll.MarshalBinary(); err == nil {
			b.HLL = data
		}
		b.Visitors = int64(c.hll.Estimate())
	}
	if len(c.codes) > 0 {
		b.StatusCodes = make(map[string]int64, len(c.codes))
		for code, n := range c.codes {
			b.StatusCodes[strconv.Itoa(code)] = n
		}
	}
	b.Top = agentprotocol.TrafficTop{
		Paths: c.paths.top(agentprotocol.TopN), Pages: c.pages.top(agentprotocol.TopN),
		Referrers: c.referrers.top(agentprotocol.TopN), Browsers: c.browsers.top(agentprotocol.TopN),
		OS: c.oses.top(agentprotocol.TopN), Devices: c.devices.top(agentprotocol.TopN),
		NotFound: c.notFound.top(agentprotocol.TopN),
	}
	b.Geo = c.geo.bucket()
	return b
}

// topN counts keys with a bounded live key set. When the set grows past
// twice liveTopCap the smaller half is folded into "Other" (space-saving
// style), so a path scan cannot grow memory without bound.
type topN struct {
	m     map[string]*agentprotocol.TopEntry
	other agentprotocol.TopEntry
}

func newTopN() *topN { return &topN{m: map[string]*agentprotocol.TopEntry{}} }

func (t *topN) add(k string, n, e, b int64, ms float64) {
	if e2 := t.m[k]; e2 != nil {
		e2.N += n
		e2.E += e
		e2.B += b
		e2.Ms += ms
		return
	}
	t.m[k] = &agentprotocol.TopEntry{K: k, N: n, E: e, B: b, Ms: ms}
	if len(t.m) > 2*liveTopCap {
		t.compact(liveTopCap)
	}
}

func (t *topN) sorted() []agentprotocol.TopEntry {
	all := make([]agentprotocol.TopEntry, 0, len(t.m))
	for _, v := range t.m {
		all = append(all, *v)
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].N != all[j].N {
			return all[i].N > all[j].N
		}
		return all[i].K < all[j].K
	})
	return all
}

func (t *topN) compact(keep int) {
	all := t.sorted()
	for _, v := range all[keep:] {
		addTo(&t.other, v)
		delete(t.m, v.K)
	}
}

func addTo(dst *agentprotocol.TopEntry, v agentprotocol.TopEntry) {
	dst.N += v.N
	dst.E += v.E
	dst.B += v.B
	dst.Ms += v.Ms
}

// top returns the limit largest entries plus an "Other" entry for the rest.
func (t *topN) top(limit int) []agentprotocol.TopEntry {
	all := t.sorted()
	if len(all) == 0 && t.other.N == 0 {
		return nil
	}
	other := t.other
	if len(all) > limit {
		for _, v := range all[limit:] {
			addTo(&other, v)
		}
		all = all[:limit]
	}
	if other.N > 0 {
		other.K = agentprotocol.OtherKey
		all = append(all, other)
	}
	return all
}
