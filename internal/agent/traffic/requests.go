package traffic

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/internal/agent/logstore"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Request logs: every routed request becomes one line of the host's request
// store (<state-dir>/requests), the text of which is an
// agentprotocol.RequestRecord as JSON. The lines never leave the host.
const (
	// requestQueue bounds the events waiting for the writer. A full queue drops
	// the event (counted): the access-log tail never waits for the disk.
	requestQueue = 4096
	// requestBatch and requestFlushEvery end a batch: whichever comes first.
	requestBatch      = 500
	requestFlushEvery = time.Second
	// requestDropReport is how often dropped events are logged.
	requestDropReport = time.Minute

	// Caps of the free-text fields of a stored line (bytes).
	maxRequestPath   = 2048
	maxRequestQuery  = 2048
	maxRequestUA     = 512
	maxRequestHost   = 253 // the longest DNS name
	maxRequestMethod = 16  // a longer one is stored as OTHER
	maxRequestLabel  = 256 // process and component names

	// routeRefreshEvery and backendRefreshEvery bound how often an unknown route
	// or upstream container makes the writer re-read the route table or list the
	// containers.
	routeRefreshEvery   = time.Second
	backendRefreshEvery = 5 * time.Second
	backendListTimeout  = 5 * time.Second
)

// RequestSink stores request lines per service. *logstore.Store implements it.
type RequestSink interface {
	Append(serviceID string, recs []logstore.Record) error
}

// IPPrefix returns the network a client address belongs to: "a.b.c.0/24" for
// IPv4 (also IPv4-mapped IPv6), the /48 for IPv6, "" for anything that is not
// an address. Request logs keep this and never the address itself.
func IPPrefix(ip string) string {
	a, err := netip.ParseAddr(strings.TrimSpace(ip))
	if err != nil {
		return ""
	}
	a = a.WithZone("").Unmap()
	bits := 48
	if a.Is4() {
		bits = 24
	}
	p, err := a.Prefix(bits)
	if err != nil {
		return ""
	}
	return p.String()
}

// capBytes shortens s to at most n bytes without cutting a UTF-8 sequence.
func capBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// backend is what the labels of the container that served a request say about
// it: the process or component and the replica of the deployment.
type backend struct {
	service    string
	process    string
	component  string
	deployment string
	replica    int
}

// requestLevel is the level of a request line, from its status alone: nothing
// the client sends (path, user agent) can set it.
func requestLevel(status int) string {
	switch {
	case status >= 500:
		return "error"
	case status >= 400:
		return "warn"
	}
	return "info"
}

// newRequestRecord builds the stored line of one request of the given kind
// (RequestKind). keepQuery is the server's setting; without it the query
// string is never copied. The line always fits logstore.MaxLineBytes as
// complete JSON: the free-text fields are cut, the user agent first, then the
// query, then the path, until it does.
func newRequestRecord(ev Event, keepQuery bool, kind string, b backend) (logstore.Record, error) {
	method := ev.Method
	if len(method) > maxRequestMethod {
		method = "OTHER" // any token is a valid method; a long one is not one worth keeping
	}
	rec := agentprotocol.RequestRecord{
		Method:     method,
		Kind:       kind,
		Level:      requestLevel(ev.Status),
		Host:       capBytes(cleanHost(ev.Host), maxRequestHost),
		Path:       capBytes(ev.Path, maxRequestPath),
		Status:     ev.Status,
		DurationMs: ev.DurationMs,
		Bytes:      ev.BytesOut,
		BytesIn:    ev.BytesIn,
		UserAgent:  capBytes(ev.UserAgent, maxRequestUA),
		IP:         IPPrefix(ev.ClientIP),
		Process:    capBytes(b.process, maxRequestLabel),
		Component:  capBytes(b.component, maxRequestLabel),
	}
	if keepQuery {
		rec.Query = capBytes(ev.Query, maxRequestQuery)
	}
	text, err := encodeRequest(rec)
	for _, field := range []*string{&rec.UserAgent, &rec.Query, &rec.Path} {
		if err != nil || len(text) <= logstore.MaxLineBytes {
			break
		}
		// The encoded size grows with the length of the field, by up to six
		// bytes a byte: find the longest cut that fits, or none at all.
		full := *field
		*field = ""
		if text, err = encodeRequest(rec); err != nil || len(text) > logstore.MaxLineBytes {
			continue // even without this field it does not fit: cut the next one too
		}
		lo, hi := 0, len(full) // lo bytes fit, hi bytes do not
		for hi-lo > 1 && err == nil {
			mid := (lo + hi) / 2
			*field = capBytes(full, mid)
			if text, err = encodeRequest(rec); len(text) <= logstore.MaxLineBytes {
				lo = mid
			} else {
				hi = mid
			}
		}
		*field = capBytes(full, lo)
		text, err = encodeRequest(rec)
	}
	if err != nil {
		return logstore.Record{}, err
	}
	return logstore.Record{TS: ev.End.UnixNano(), Replica: b.replica, DeploymentID: b.deployment, Text: text}, nil
}

// encodeRequest is the JSON text of a record. HTML characters stay as they
// are (they would grow to six bytes each), and the encoder's trailing newline
// is left off: a stored line has none.
func encodeRequest(rec agentprotocol.RequestRecord) (string, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(rec); err != nil {
		return "", err
	}
	return strings.TrimSuffix(buf.String(), "\n"), nil
}

// routeResolver maps the route ID of a request (the router name of the access
// log) to the service that owns it, and the upstream container to its labels.
// A route of a compose stack or of a named process has its own ID
// (StackRouteID, ProcessRouteID) and names its owner in Route.Stack; any other
// route is its own service. It is used by one goroutine.
type routeResolver struct {
	routes func() []agentprotocol.Route
	list   func(context.Context) ([]docker.ContainerInfo, error)
	now    func() time.Time
	log    *slog.Logger

	owners     map[string]string // route ID -> owning service ID
	routesAt   time.Time
	backends   map[string]backend // container name -> labels
	backendsAt time.Time
}

func newRouteResolver(routes func() []agentprotocol.Route, list func(context.Context) ([]docker.ContainerInfo, error), now func() time.Time, log *slog.Logger) *routeResolver {
	return &routeResolver{routes: routes, list: list, now: now, log: log}
}

// resolve returns the owning service of routeID and, when the upstream is a
// managed container, its labels. ok is false for a route the proxy does not
// have and for an external route: their requests belong to no stored service.
func (r *routeResolver) resolve(ctx context.Context, routeID, upstream string) (owner string, b backend, ok bool) {
	owner, ok = r.owners[routeID]
	if !ok && r.now().Sub(r.routesAt) >= routeRefreshEvery {
		r.refreshRoutes()
		owner, ok = r.owners[routeID]
	}
	if !ok {
		return "", backend{}, false
	}
	host := upstream
	if h, _, err := net.SplitHostPort(upstream); err == nil {
		host = h
	}
	if !strings.HasPrefix(host, agentprotocol.ContainerPrefix) {
		return owner, backend{}, true
	}
	b, found := r.backends[host]
	if !found && r.now().Sub(r.backendsAt) >= backendRefreshEvery {
		r.refreshBackends(ctx)
		b, found = r.backends[host]
	}
	if !found || b.service != owner {
		return owner, backend{}, true
	}
	return owner, b, true
}

func (r *routeResolver) refreshRoutes() {
	table := r.routes()
	r.owners = make(map[string]string, len(table))
	for _, rt := range table {
		switch {
		case rt.Kind == agentprotocol.RouteKindExternal:
			// Nothing reads an external route's requests: no line is kept.
		case rt.Stack != "":
			r.owners[rt.ServiceID] = rt.Stack
		default:
			r.owners[rt.ServiceID] = rt.ServiceID
		}
	}
	r.routesAt = r.now()
}

func (r *routeResolver) refreshBackends(ctx context.Context) {
	r.backendsAt = r.now()
	ctx, cancel := context.WithTimeout(ctx, backendListTimeout)
	defer cancel()
	list, err := r.list(ctx)
	if err != nil {
		r.log.Warn("request logs: list containers", "err", err)
		return
	}
	r.backends = make(map[string]backend, len(list))
	for _, ci := range list {
		replica, _ := strconv.Atoi(ci.Labels[agentprotocol.LabelReplica])
		r.backends[ci.Name] = backend{
			service:    ci.Labels[agentprotocol.LabelService],
			process:    ci.Labels[agentprotocol.LabelProcess],
			component:  ci.Labels[agentprotocol.LabelComponent],
			deployment: ci.Labels[agentprotocol.LabelDeployment],
			replica:    max(replica, 0),
		}
	}
}

// queuedRequest is an event with its kind and the server's keep-query setting
// at the time it was read.
type queuedRequest struct {
	ev        Event
	kind      string
	keepQuery bool
}

// requestWriter turns events into request lines and appends them to the sink in
// batches, off the goroutine that tails the access log.
type requestWriter struct {
	sink   RequestSink
	config func() *agentprotocol.RequestLogsConfig
	res    *routeResolver
	log    *slog.Logger
	// resume is, per owning service, the end time of its newest line stored
	// before this run. A request that ended at or before it was stored by
	// that run and is not stored again. It is set before Run and never
	// changed. Lines of one service end in nearly the order Traefik writes
	// them, so one resume point per service is exact except for lines of the
	// same nanosecond as the newest stored one (equal timestamps: dropped, as
	// the store cannot tell a duplicate from a twin cheaply).
	resume map[string]time.Time

	queue   chan queuedRequest
	dropped atomic.Int64
}

func newRequestWriter(sink RequestSink, config func() *agentprotocol.RequestLogsConfig, res *routeResolver, log *slog.Logger) *requestWriter {
	return &requestWriter{sink: sink, config: config, res: res, log: log, queue: make(chan queuedRequest, requestQueue)}
}

// Add queues one request. It never blocks: when request logs are off it does
// nothing, when the queue is full the request is dropped and counted. A
// successful asset request is not queued unless the server keeps them.
func (w *requestWriter) Add(ev Event) {
	if ev.ServiceID == "" || ev.Status == 0 {
		return
	}
	cfg := w.config()
	if cfg == nil {
		return
	}
	kind := RequestKind(ev.ContentType, ev.Path)
	if skipsAsset(kind, ev.Method, ev.ContentType, ev.Status, cfg.KeepAssets) {
		return
	}
	select {
	case w.queue <- queuedRequest{ev: ev, kind: kind, keepQuery: cfg.KeepQuery}:
	default:
		w.dropped.Add(1)
	}
}

// Run writes queued requests until ctx ends, then writes what is left.
func (w *requestWriter) Run(ctx context.Context) {
	flush := time.NewTicker(requestFlushEvery)
	report := time.NewTicker(requestDropReport)
	defer flush.Stop()
	defer report.Stop()
	batch := map[string][]logstore.Record{}
	n := 0
	write := func() {
		for id, recs := range batch {
			if err := w.sink.Append(id, recs); err != nil {
				w.log.Warn("request logs: append", "service_id", id, "lines", len(recs), "err", err)
			}
		}
		clear(batch)
		n = 0
	}
	add := func(rctx context.Context, q queuedRequest) {
		owner, b, ok := w.res.resolve(rctx, q.ev.ServiceID, q.ev.Upstream)
		if !ok {
			return
		}
		if !q.ev.End.After(w.resume[owner]) {
			return // stored by an earlier run
		}
		rec, err := newRequestRecord(q.ev, q.keepQuery, q.kind, b)
		if err != nil {
			w.log.Warn("request logs: encode", "err", err)
			return
		}
		batch[owner] = append(batch[owner], rec)
		n++
	}
	for {
		select {
		case <-ctx.Done():
			rctx := context.WithoutCancel(ctx)
			for {
				select {
				case q := <-w.queue:
					add(rctx, q)
					continue
				default:
				}
				break
			}
			write()
			return
		case q := <-w.queue:
			add(ctx, q)
			if n >= requestBatch {
				write()
			}
		case <-flush.C:
			write()
		case <-report.C:
			if d := w.dropped.Swap(0); d > 0 {
				w.log.Warn("request logs: queue full, requests dropped", "dropped", d)
			}
		}
	}
}
