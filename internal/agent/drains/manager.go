// Package drains ships the agent's stored runtime logs to external systems
// (Loki, OTLP/HTTP, syslog, generic HTTP JSON).
//
// The log store is the disk buffer: every drain keeps one position (sequence
// number) per service and reads batches from the store after it, so a down
// destination only makes the position lag; nothing is queued twice. A drain
// delivers at least once (a crash repeats at most the last two seconds), retries
// with backoff and, when its backlog outgrows its bound, skips the oldest lines
// and counts them as dropped. A new drain starts at the end of the log and ships
// what is written after it was created: lines stamped before its creation are
// never shipped, even when the log store received them later.
package drains

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nebulactrl/nebulactrl/internal/agent/drains/sinks"
	"github.com/nebulactrl/nebulactrl/internal/agent/logstore"
	"github.com/nebulactrl/nebulactrl/internal/agent/logstore/query"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// drainIDRe matches the ids the control plane gives drains (UUIDs).
var drainIDRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

// Delivery policy (variables so tests can run fast).
var (
	baseBackoff  = time.Second
	maxBackoff   = 5 * time.Minute
	idleWait     = 2 * time.Second
	persistEvery = 2 * time.Second
)

const (
	// failingAfter consecutive failures turn a retrying drain into a failing one.
	failingAfter = 5
	scanChunk    = int64(32) << 20
)

// Manager runs the drains of one agent.
type Manager struct {
	st  *logstore.Store
	dir string
	log *slog.Logger

	// NewSink builds a drain's sink (default sinks.New); tests replace it.
	NewSink func(d agentprotocol.LogDrain, secrets map[string]string) (sinks.Sink, error)

	mu      sync.Mutex
	ctx     context.Context
	runners map[string]*runner
	statics map[string]agentprotocol.LogDrainStatus // disabled drains
	wg      sync.WaitGroup

	// metaMu guards what runners read while Apply (holding mu) stops them.
	metaMu sync.RWMutex
	meta   map[string]agentprotocol.LogServiceMeta
	server string
	redact map[string]func(string) string // service ID → mask for its secrets
}

// NewManager builds a Manager keeping drain positions under dir.
func NewManager(st *logstore.Store, dir string, log *slog.Logger) *Manager {
	if log == nil {
		log = slog.Default()
	}
	return &Manager{
		st: st, dir: dir, log: log, NewSink: sinks.New,
		runners: map[string]*runner{}, statics: map[string]agentprotocol.LogDrainStatus{}, meta: map[string]agentprotocol.LogServiceMeta{},
	}
}

// Run blocks until ctx ends; drains started by Apply run under it.
func (m *Manager) Run(ctx context.Context) {
	m.mu.Lock()
	m.ctx = ctx
	for _, r := range m.runners {
		r.start(ctx, &m.wg)
	}
	m.mu.Unlock()
	<-ctx.Done()
	m.wg.Wait()
}

// Apply installs the control plane's drain list: new and changed drains start,
// removed ones stop (and forget their position), disabled ones are only reported.
func (m *Manager) Apply(cfg *agentprotocol.LogsConfig, secrets map[string]string) {
	if cfg == nil {
		return
	}
	meta := make(map[string]agentprotocol.LogServiceMeta, len(cfg.Services))
	for _, s := range cfg.Services {
		meta[s.ServiceID] = s
	}
	m.metaMu.Lock()
	m.server, m.meta, m.redact = cfg.ServerName, meta, redactors(secrets, cfg.Services)
	m.metaMu.Unlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	want := map[string]bool{}
	m.statics = map[string]agentprotocol.LogDrainStatus{}
	for _, d := range cfg.Drains {
		if !drainIDRe.MatchString(d.ID) {
			continue // the id names the position file, so it must be a plain identifier
		}
		want[d.ID] = true
		if !d.Enabled {
			if r := m.runners[d.ID]; r != nil {
				r.stop()
				delete(m.runners, d.ID)
			}
			m.statics[d.ID] = agentprotocol.LogDrainStatus{ID: d.ID, State: agentprotocol.DrainDisabled}
			continue
		}
		own := ownSecrets(d.ID, secrets)
		sig := signature(d, own)
		if r := m.runners[d.ID]; r != nil {
			if r.sig == sig {
				continue
			}
			r.stop()
			delete(m.runners, d.ID)
		}
		r := newRunner(m, d, own, sig, drainStart(cfg.Now, d.CreatedAt, time.Now()))
		m.runners[d.ID] = r
		if m.ctx != nil {
			r.start(m.ctx, &m.wg)
		}
	}
	for id, r := range m.runners {
		if !want[id] {
			r.stop()
			delete(m.runners, id)
			_ = os.Remove(m.statePath(id))
		}
	}
}

// Statuses reports every drain.
func (m *Manager) Statuses() []agentprotocol.LogDrainStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]agentprotocol.LogDrainStatus, 0, len(m.runners)+len(m.statics))
	for _, r := range m.runners {
		out = append(out, r.status())
	}
	for _, s := range m.statics {
		out = append(out, s)
	}
	slices.SortFunc(out, func(a, b agentprotocol.LogDrainStatus) int { return strings.Compare(a.ID, b.ID) })
	return out
}

// Test sends one test line through the drain from this host.
func (m *Manager) Test(ctx context.Context, d agentprotocol.LogDrain, secrets map[string]string) agentprotocol.LogsDrainTestResult {
	own := ownSecrets(d.ID, secrets)
	sink, err := m.NewSink(d, own)
	if err != nil {
		return agentprotocol.LogsDrainTestResult{Message: sinks.Redact(err.Error(), own)}
	}
	defer sink.Close()
	_, server := m.snapshotMeta()
	tctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := sink.Send(tctx, []sinks.Entry{sinks.TestEntry(d.Name, server)}); err != nil {
		var se *sinks.SendError
		if errors.As(err, &se) && se.StatusCode != 0 {
			// The destination's answer is not shown: it comes from a URL the
			// control plane chose and could be an internal service.
			return agentprotocol.LogsDrainTestResult{
				StatusCode: se.StatusCode,
				Message:    "The destination answered HTTP " + strconv.Itoa(se.StatusCode) + " " + http.StatusText(se.StatusCode) + ".",
			}
		}
		return agentprotocol.LogsDrainTestResult{Message: sinks.Redact(err.Error(), own)}
	}
	return agentprotocol.LogsDrainTestResult{OK: true, Message: "The test line was delivered."}
}

func (m *Manager) statePath(id string) string { return filepath.Join(m.dir, id+".json") }

func (m *Manager) snapshotMeta() (map[string]agentprotocol.LogServiceMeta, string) {
	m.metaMu.RLock()
	defer m.metaMu.RUnlock()
	return m.meta, m.server
}

// redactorFor returns the mask of a service's secrets (identity when it has none).
func (m *Manager) redactorFor(serviceID string) func(string) string {
	m.metaMu.RLock()
	defer m.metaMu.RUnlock()
	if fn := m.redact[serviceID]; fn != nil {
		return fn
	}
	return func(s string) string { return s }
}

// ownSecrets returns the secrets of one drain under their short names.
func ownSecrets(id string, all map[string]string) map[string]string {
	out := map[string]string{}
	for _, name := range []string{
		agentprotocol.LogDrainSecretToken, agentprotocol.LogDrainSecretUsername,
		agentprotocol.LogDrainSecretPassword, agentprotocol.LogDrainSecretHeaders,
	} {
		if v, ok := all[agentprotocol.LogDrainSecretKey(id, name)]; ok && v != "" {
			out[name] = v
		}
	}
	return out
}

// drainStart places a drain's creation on the agent's clock: the control plane
// reports its own clock (cfgNow) and the creation time on it, so only their
// difference, the drain's age, crosses the two clocks. Zero means no bound.
func drainStart(cfgNow, createdAt, now time.Time) int64 {
	if cfgNow.IsZero() || createdAt.IsZero() {
		return 0
	}
	return now.Add(-max(cfgNow.Sub(createdAt), 0)).UnixNano()
}

func signature(d agentprotocol.LogDrain, secrets map[string]string) string {
	raw, _ := json.Marshal(struct {
		D agentprotocol.LogDrain
		S map[string]string
	}{d, secrets})
	return string(raw)
}

// ---- one drain ---------------------------------------------------------------------

type state struct {
	Initialized bool              `json:"initialized"`
	Cursors     map[string]uint64 `json:"cursors"`
	// Since is the agent-clock time (UnixNano) from which lines are shipped: the
	// drain's creation. Lines stored later but stamped earlier (the collector can
	// lag the container by seconds) are history, not shipped. 0 = no bound.
	Since   int64 `json:"since,omitempty"`
	Sent    int64 `json:"sent"`
	Dropped int64 `json:"dropped"`
}

type runner struct {
	m       *Manager
	d       agentprotocol.LogDrain
	secrets map[string]string
	sig     string
	since   int64 // agent-clock start of this drain (see state.Since), applied on first initialize
	match   query.Matcher
	bufMax  int64

	cancel context.CancelFunc
	done   chan struct{}

	mu           sync.Mutex
	st           state
	dirty        bool
	lastPersist  time.Time
	failures     int
	lastSuccess  time.Time
	lastErr      string
	lastErrAt    time.Time
	backlogBytes int64
}

func newRunner(m *Manager, d agentprotocol.LogDrain, secrets map[string]string, sig string, since int64) *runner {
	r := &runner{m: m, d: d, secrets: secrets, sig: sig, since: since, done: make(chan struct{})}
	r.match, _ = query.Compile(d.Query)
	r.bufMax = d.BufferMaxBytes
	if r.bufMax <= 0 {
		r.bufMax = agentprotocol.DefaultDrainBufferBytes
	}
	r.st = state{Cursors: map[string]uint64{}}
	if raw, err := os.ReadFile(m.statePath(d.ID)); err == nil {
		var s state
		if json.Unmarshal(raw, &s) == nil {
			if s.Cursors == nil {
				s.Cursors = map[string]uint64{}
			}
			r.st = s
		}
	}
	return r
}

func (r *runner) start(ctx context.Context, wg *sync.WaitGroup) {
	if r.cancel != nil {
		return
	}
	ctx, r.cancel = context.WithCancel(ctx)
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(r.done)
		r.run(ctx)
	}()
}

// stop ends the runner and waits for it (it persists its position on the way out).
func (r *runner) stop() {
	if r.cancel == nil {
		return
	}
	r.cancel()
	<-r.done
}

func (r *runner) status() agentprotocol.LogDrainStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := agentprotocol.LogDrainStatus{
		ID: r.d.ID, State: agentprotocol.DrainOK, SentLines: r.st.Sent, DroppedLines: r.st.Dropped,
		BacklogBytes: r.backlogBytes, LastError: r.lastErr,
	}
	switch {
	case r.failures >= failingAfter:
		s.State = agentprotocol.DrainFailing
	case r.failures > 0:
		s.State = agentprotocol.DrainRetrying
	}
	if !r.lastSuccess.IsZero() {
		t := r.lastSuccess.UTC()
		s.LastSuccessAt = &t
	}
	if !r.lastErrAt.IsZero() {
		t := r.lastErrAt.UTC()
		s.LastErrorAt = &t
	}
	return s
}

func (r *runner) inScope(serviceID string, meta map[string]agentprotocol.LogServiceMeta) bool {
	if len(r.d.ServiceIDs) > 0 && !slices.Contains(r.d.ServiceIDs, serviceID) {
		return false
	}
	if r.d.AllProjects {
		return true
	}
	m, ok := meta[serviceID]
	return ok && slices.Contains(r.d.ProjectIDs, m.ProjectID)
}

func (r *runner) persist(force bool) {
	r.mu.Lock()
	if !r.dirty || (!force && time.Since(r.lastPersist) < persistEvery) {
		r.mu.Unlock()
		return
	}
	raw, _ := json.Marshal(r.st)
	r.dirty = false
	r.lastPersist = time.Now()
	r.mu.Unlock()
	if err := os.MkdirAll(r.m.dir, 0o700); err != nil {
		return
	}
	tmp := r.m.statePath(r.d.ID) + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err == nil {
		_ = os.Rename(tmp, r.m.statePath(r.d.ID))
	}
}

func (r *runner) run(ctx context.Context) {
	defer r.persist(true)
	sink, err := r.m.NewSink(r.d, r.secrets)
	if err != nil {
		r.fail(err)
		r.mu.Lock()
		r.failures = failingAfter
		r.mu.Unlock()
		r.m.log.Warn("log drain misconfigured", "drain", r.d.Name, "err", sinks.Redact(err.Error(), r.secrets))
		<-ctx.Done()
		return
	}
	defer sink.Close()
	r.initialize()
	var failures int
	for ctx.Err() == nil {
		changed := r.m.st.Changed()
		moved, retryAfter, err := r.pass(ctx, sink)
		r.persist(false)
		switch {
		case err != nil:
			failures++
			wait := backoff(failures)
			if retryAfter > wait {
				wait = retryAfter
			}
			sleep(ctx, wait)
		case moved:
			failures = 0
			// More may be waiting: go again at once.
		default:
			failures = 0
			select {
			case <-ctx.Done():
			case <-changed:
			case <-time.After(idleWait):
			}
		}
	}
}

// initialize starts a new drain at the end of the log of every service the
// store knows.
func (r *runner) initialize() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.st.Initialized {
		return
	}
	r.st.Since = r.since
	for _, id := range r.m.st.ServiceIDs() {
		if b, err := r.m.st.Bounds(id); err == nil && b.LastSeq != 0 {
			r.st.Cursors[id] = b.LastSeq
		}
	}
	r.st.Initialized, r.dirty = true, true
}

func backoff(failures int) time.Duration {
	d := baseBackoff << min(failures-1, 9)
	if d > maxBackoff || d <= 0 {
		d = maxBackoff
	}
	return d/2 + time.Duration(rand.Int64N(int64(d/2)+1))
}

func sleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

func (r *runner) fail(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.failures++
	r.lastErr = sinks.Redact(err.Error(), r.secrets)
	r.lastErrAt = time.Now()
}

// pass ships one batch of every service in scope. moved: some position
// advanced. A returned error means a delivery failed (retry with backoff).
func (r *runner) pass(ctx context.Context, sink sinks.Sink) (moved bool, retryAfter time.Duration, err error) {
	meta, server := r.m.snapshotMeta()
	var backlog int64
	for _, id := range r.m.st.ServiceIDs() {
		if ctx.Err() != nil {
			return moved, 0, nil
		}
		if !r.inScope(id, meta) {
			r.skipTo(id)
			continue
		}
		r.mu.Lock()
		cursor, known := r.st.Cursors[id]
		r.mu.Unlock()
		cursor = r.bound(id, cursor, known)
		b, berr := r.m.st.Bounds(id)
		if berr != nil || b.LastSeq == 0 || b.LastSeq <= cursor {
			continue
		}
		batch, next, rerr := r.read(id, cursor, meta[id], server)
		if rerr != nil {
			r.m.log.Warn("log drain: read store", "drain", r.d.Name, "service_id", id, "err", rerr)
			continue
		}
		if len(batch) > 0 {
			if serr := sink.Send(ctx, batch); serr != nil {
				if ctx.Err() != nil {
					return moved, 0, nil
				}
				var se *sinks.SendError
				if errors.As(serr, &se) && se.Permanent {
					// The destination will never take this batch: skip it, say so.
					r.fail(serr)
					r.mu.Lock()
					r.st.Dropped += int64(len(batch))
					r.st.Cursors[id], r.dirty = next, true
					r.failures = 0 // the drain works; this batch did not
					r.mu.Unlock()
					r.m.log.Warn("log drain: batch rejected and dropped", "drain", r.d.Name, "lines", len(batch), "err", sinks.Redact(serr.Error(), r.secrets))
					moved = true
					continue
				}
				r.fail(serr)
				if se != nil {
					retryAfter = se.RetryAfter
				}
				r.mu.Lock()
				r.backlogBytes = backlog + r.m.st.BacklogBytes(id, cursor)
				r.mu.Unlock()
				return moved, retryAfter, serr
			}
			r.mu.Lock()
			r.st.Sent += int64(len(batch))
			r.failures, r.lastErr = 0, ""
			r.lastSuccess = time.Now()
			r.mu.Unlock()
		}
		if next != cursor {
			r.mu.Lock()
			r.st.Cursors[id], r.dirty = next, true
			r.mu.Unlock()
			moved = true
		}
		backlog += r.m.st.BacklogBytes(id, next)
	}
	r.mu.Lock()
	r.backlogBytes = backlog
	r.mu.Unlock()
	return moved, 0, nil
}

// skipTo moves the position of a service outside the drain's scope to the end of
// its log, so lines written while it was out of scope are not shipped if the
// scope widens later.
func (r *runner) skipTo(id string) {
	b, err := r.m.st.Bounds(id)
	if err != nil || b.LastSeq == 0 {
		return
	}
	r.mu.Lock()
	if cur, ok := r.st.Cursors[id]; !ok || cur < b.LastSeq {
		r.st.Cursors[id], r.dirty = b.LastSeq, true
	}
	r.mu.Unlock()
}

// bound enforces the drain's backlog bound and accounts for lines the store
// already collected: it returns the position to read from.
func (r *runner) bound(id string, cursor uint64, known bool) uint64 {
	b, err := r.m.st.Bounds(id)
	if err != nil || b.FirstSeq == 0 {
		return cursor
	}
	var dropped int64
	if !known { // a service this drain has not seen: from its first line, nothing was lost
		cursor = b.FirstSeq - 1
	}
	if cursor > b.LastSeq { // the store was wiped and restarted
		cursor = b.LastSeq
	}
	if known && cursor+1 < b.FirstSeq { // expired or capped before it was delivered
		dropped += int64(b.FirstSeq - cursor - 1)
		cursor = b.FirstSeq - 1
	}
	// Skip the oldest lines until the backlog fits: only what exceeds the bound
	// (the scan stops once it has covered the excess), never the newest lines.
	for {
		excess := r.m.st.BacklogBytes(id, cursor) - r.bufMax
		if excess <= 0 {
			break
		}
		from := cursor
		var n, skipped int64
		res, err := r.m.st.Scan(id, logstore.ScanOptions{Cursor: cursor, MaxScanBytes: scanChunk}, func(rec logstore.Record) bool {
			cursor = rec.Seq
			n++
			skipped += int64(len(rec.Text) + 40)
			return skipped < excess
		})
		dropped += n
		if err != nil || res.Done {
			break
		}
		if res.Cursor > cursor {
			cursor = res.Cursor
		}
		if cursor == from {
			break // no progress
		}
	}
	if dropped > 0 {
		r.mu.Lock()
		r.st.Dropped += dropped
		r.st.Cursors[id], r.dirty = cursor, true
		r.mu.Unlock()
	}
	return cursor
}

// read collects the next batch of the service after cursor: the lines that pass
// the drain's query, labelled. next is the position after the last line read
// (matching or not).
func (r *runner) read(id string, cursor uint64, meta agentprotocol.LogServiceMeta, server string) (batch []sinks.Entry, next uint64, err error) {
	next, scanned, size := cursor, 0, 0
	r.mu.Lock()
	since := r.st.Since
	r.mu.Unlock()
	name := meta.Name
	if name == "" {
		name = id
	}
	redact := r.m.redactorFor(id)
	_, err = r.m.st.Scan(id, logstore.ScanOptions{Cursor: cursor, MaxScanBytes: scanChunk, Deadline: time.Now().Add(5 * time.Second)}, func(rec logstore.Record) bool {
		next = rec.Seq
		scanned++
		text := redact(rec.Text) // lines leave the host: secrets are masked before anything else sees them
		l := query.NewLine(query.Input{
			Line: text, Stream: agentprotocol.StreamRuntime, ServiceName: name,
			Replica: strconv.Itoa(rec.Replica), DeploymentID: rec.DeploymentID,
		})
		if rec.TS >= since && r.match(l) {
			batch = append(batch, sinks.Entry{
				TS: time.Unix(0, rec.TS).UTC(), Text: text, Level: l.Level(), Stream: agentprotocol.StreamRuntime,
				ServiceID: id, Service: name, ServiceSlug: meta.Slug, ProjectID: meta.ProjectID, Project: meta.Project,
				EnvironmentID: meta.EnvironmentID, Environment: meta.Environment, DeploymentID: rec.DeploymentID,
				Replica: rec.Replica, Server: server,
			})
			size += len(text)
		}
		return len(batch) < sinks.MaxBatchLines && size < sinks.MaxBatchBytes && scanned < 20_000
	})
	return batch, next, err
}
