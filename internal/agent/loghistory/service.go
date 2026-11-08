// Package loghistory wires the agent's log history: the segment store, the
// collector that feeds it from the managed containers, the drains and the
// control-plane facing pieces (history queries, drain tests, configuration,
// status reports).
package loghistory

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/internal/agent/drains"
	"github.com/nebulactrl/nebulactrl/internal/agent/logstore"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// StatusSender reports the store and drains to the control plane
// (*conn.Client implements it).
type StatusSender interface {
	LogStatus(agentprotocol.LogStatus)
}

// maxConcurrentReads is how many log read commands (KindLogsQuery and
// KindLogsFacets together) run at once on an agent. Further ones wait for a
// slot until their context ends.
const maxConcurrentReads = 4

// Service is the agent's log history.
type Service struct {
	// Store is the runtime log; the drains and the live view read it.
	Store *logstore.Store
	// Requests holds one line per proxied request (agentprotocol.RequestRecord),
	// under <stateDir>/requests. It is separate from Store so that no drain and
	// no runtime query can reach it.
	Requests  *logstore.Store
	Collector *logstore.Collector
	Drains    *drains.Manager

	log    *slog.Logger
	sender StatusSender
	// writers are the held request-store writers (Hold).
	writers sync.WaitGroup
	// reqCfg is the request-log setting of the control plane; nil = off.
	reqCfg atomic.Pointer[agentprotocol.RequestLogsConfig]

	// statusEvery is how often the status is reported (default LogStatusInterval).
	statusEvery time.Duration
	kick        chan struct{}

	// readSlots bounds the log reads (history queries and facets) that run at
	// once: a slot is a held send, so at most cap(readSlots) reads scan the
	// stores together.
	readSlots chan struct{}
}

// New opens the stores under <stateDir>/logs and <stateDir>/requests. docker
// feeds the collector.
func New(eng docker.Engine, stateDir string, sender StatusSender, log *slog.Logger) (*Service, error) {
	if log == nil {
		log = slog.Default()
	}
	root := filepath.Join(stateDir, "logs")
	st, err := logstore.Open(logstore.Options{Dir: root, Logger: log})
	if err != nil {
		return nil, err
	}
	reqSt, err := logstore.Open(logstore.Options{
		Dir: filepath.Join(stateDir, "requests"), Logger: log, Retention: requestRetention(&agentprotocol.LogsConfig{}),
	})
	if err != nil {
		_ = st.Close()
		return nil, fmt.Errorf("open request log store: %w", err)
	}
	col := logstore.NewCollector(eng, st, log)
	col.CheckpointFile = filepath.Join(stateDir, "logs-positions.json")
	return &Service{
		Store: st, Requests: reqSt, Collector: col, Drains: drains.NewManager(st, filepath.Join(stateDir, "logdrains"), log),
		log: log, sender: sender, statusEvery: agentprotocol.LogStatusInterval, kick: make(chan struct{}, 1),
		readSlots: make(chan struct{}, maxConcurrentReads),
	}, nil
}

// requestRetention is the limit of the request store: the age of the server's
// log retention, the request byte caps unless the configuration sets its own.
func requestRetention(cfg *agentprotocol.LogsConfig) agentprotocol.LogRetention {
	var r agentprotocol.LogRetention
	if cfg.Requests != nil {
		r = cfg.Requests.Retention
	}
	if r.Days <= 0 {
		r.Days = cfg.Retention.Days
	}
	if r.MaxServiceBytes <= 0 {
		r.MaxServiceBytes = agentprotocol.DefaultRequestLogMaxServiceBytes
	}
	if r.MaxServerBytes <= 0 {
		r.MaxServerBytes = agentprotocol.DefaultRequestLogMaxServerBytes
	}
	return r.WithDefaults()
}

// RequestLogs returns the request-log setting (nil: request logs are off). The
// traffic collector asks it for every request.
func (s *Service) RequestLogs() *agentprotocol.RequestLogsConfig { return s.reqCfg.Load() }

// RequestsResume returns, per service, the time of its newest stored request
// line (services without a line are absent). It is the resume point of the
// request writer: lines that ended at or before it are already stored.
func (s *Service) RequestsResume() map[string]time.Time {
	usage, _ := s.Requests.Usage()
	newest := make(map[string]time.Time, len(usage))
	for _, u := range usage {
		newest[u.ServiceID] = u.Newest
	}
	return newest
}

// Hold registers a writer of the request store and returns the function that
// reports it finished. Run closes the stores only after every held writer is
// released, so the writer's last lines reach the disk. Call it before Run.
func (s *Service) Hold() (release func()) {
	s.writers.Add(1)
	return s.writers.Done
}

// Run captures, ships and reports until ctx ends. The stores outlive the
// others: they flush and close once the collector and the drains have stopped.
func (s *Service) Run(ctx context.Context) {
	storeCtx, stopStore := context.WithCancel(context.Background())
	var stores sync.WaitGroup
	for _, st := range []*logstore.Store{s.Store, s.Requests} {
		stores.Add(1)
		go func() {
			defer stores.Done()
			st.Run(storeCtx)
		}()
	}
	var wg sync.WaitGroup
	for _, fn := range []func(context.Context){s.Collector.Run, s.Drains.Run, s.reportLoop} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			fn(ctx)
		}()
	}
	wg.Wait()
	s.writers.Wait()
	stopStore()
	stores.Wait()
}

// ApplyLogsConfig implements engine.LogsService.
func (s *Service) ApplyLogsConfig(cfg *agentprotocol.LogsConfig, secrets map[string]string) {
	if cfg == nil {
		return
	}
	s.Store.SetRetention(cfg.Retention)
	// Off, the request store only stops taking lines: what it holds ages out.
	if cfg.Requests == nil {
		s.reqCfg.Store(nil)
	} else {
		rc := *cfg.Requests
		s.reqCfg.Store(&rc)
	}
	s.Requests.SetRetention(requestRetention(cfg))
	s.Collector.SetCapture(cfg.Capture)
	s.Drains.Apply(cfg, secrets)
	select {
	case s.kick <- struct{}{}:
	default:
	}
}

// logSource is the store a query reads and what describes its lines.
type logSource struct {
	store     *logstore.Store
	stream    string // the `stream` facet value of every line of the store
	capturing bool
}

// source resolves LogsQuery.Source ("" is the runtime store).
func (s *Service) source(name string) (logSource, error) {
	switch name {
	case "", agentprotocol.LogSourceRuntime:
		return logSource{store: s.Store, stream: agentprotocol.StreamRuntime, capturing: s.Collector.Capturing()}, nil
	case agentprotocol.LogSourceRequests:
		return logSource{store: s.Requests, stream: agentprotocol.StreamRequests, capturing: s.reqCfg.Load() != nil}, nil
	}
	return logSource{}, fmt.Errorf("unknown log source %q", name)
}

func failedFrame(cmd agentprotocol.Command, msg string) agentprotocol.ResultFrame {
	return agentprotocol.ResultFrame{CommandID: cmd.ID, Outcome: agentprotocol.OutcomeFailed, Error: msg}
}

func succeededFrame(cmd agentprotocol.Command, data any) agentprotocol.ResultFrame {
	raw, err := json.Marshal(data)
	if err != nil {
		return failedFrame(cmd, "encode result: "+err.Error())
	}
	return agentprotocol.ResultFrame{CommandID: cmd.ID, Outcome: agentprotocol.OutcomeSucceeded, Data: raw}
}

// readSlotWait is how long a read command without an expiry waits for a slot.
const readSlotWait = 25 * time.Second

// readDeadline is when a read command stops being worth answering: the control
// plane gives up one minute before cmd.ExpiresAt (the hub sets it to the
// request timeout plus a minute), so a scan that starts later only burns the
// agent's disk for a reply nobody reads. A command without an expiry gets
// readSlotWait from now.
func readDeadline(cmd agentprotocol.Command, now time.Time) time.Time {
	if cmd.ExpiresAt.IsZero() {
		return now.Add(readSlotWait)
	}
	return cmd.ExpiresAt.Add(-time.Minute)
}

// acquireRead waits for a read slot until the command's deadline or ctx ends.
// It returns the function that releases the slot, or a failed frame when the
// deadline passed first (a command already past it is not run at all).
func (s *Service) acquireRead(ctx context.Context, cmd agentprotocol.Command) (release func(), busy *agentprotocol.ResultFrame) {
	wait := time.Until(readDeadline(cmd, time.Now()))
	if wait <= 0 {
		f := failedFrame(cmd, "the log read expired before it could start")
		return nil, &f
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case s.readSlots <- struct{}{}:
		return func() { <-s.readSlots }, nil
	case <-timer.C:
	case <-ctx.Done():
	}
	f := failedFrame(cmd, fmt.Sprintf("timed out waiting for a log read slot: %d log reads are already running on this server", cap(s.readSlots)))
	return nil, &f
}

// HandleLogsCommand implements engine.LogsService for KindLogsQuery,
// KindLogsFacets and KindLogsDrainTest.
func (s *Service) HandleLogsCommand(ctx context.Context, cmd agentprotocol.Command) agentprotocol.ResultFrame {
	switch cmd.Kind {
	case agentprotocol.KindLogsQuery:
		var q agentprotocol.LogsQuery
		if err := json.Unmarshal(cmd.Payload, &q); err != nil {
			return failedFrame(cmd, "invalid logs_query payload: "+err.Error())
		}
		if q.ServiceID == "" {
			return failedFrame(cmd, "serviceId is required")
		}
		src, err := s.source(q.Source)
		if err != nil {
			return failedFrame(cmd, err.Error())
		}
		release, busy := s.acquireRead(ctx, cmd)
		if busy != nil {
			return *busy
		}
		defer release()
		res, err := src.store.Query(q, src.stream)
		if err != nil {
			return failedFrame(cmd, err.Error())
		}
		res.Capturing = src.capturing
		return succeededFrame(cmd, res)
	case agentprotocol.KindLogsFacets:
		return s.HandleFacetsCommand(ctx, cmd)
	case agentprotocol.KindLogsDrainTest:
		var t agentprotocol.LogsDrainTest
		if err := json.Unmarshal(cmd.Payload, &t); err != nil {
			return failedFrame(cmd, "invalid logs_drain_test payload: "+err.Error())
		}
		return succeededFrame(cmd, s.Drains.Test(ctx, t.Drain, cmd.Secrets))
	}
	return failedFrame(cmd, "unsupported command kind: "+cmd.Kind)
}

// HandleFacetsCommand answers KindLogsFacets from the store of the query's source.
func (s *Service) HandleFacetsCommand(ctx context.Context, cmd agentprotocol.Command) agentprotocol.ResultFrame {
	var q agentprotocol.LogsFacetsQuery
	if err := json.Unmarshal(cmd.Payload, &q); err != nil {
		return failedFrame(cmd, "invalid logs_facets payload: "+err.Error())
	}
	if q.ServiceID == "" {
		return failedFrame(cmd, "serviceId is required")
	}
	src, err := s.source(q.Source)
	if err != nil {
		return failedFrame(cmd, err.Error())
	}
	release, busy := s.acquireRead(ctx, cmd)
	if busy != nil {
		return *busy
	}
	defer release()
	res, err := src.store.Facets(ctx, q, src.stream)
	if err != nil {
		return failedFrame(cmd, err.Error())
	}
	res.Capturing = src.capturing
	return succeededFrame(cmd, res)
}

// Status is the report sent to the control plane.
func (s *Service) Status() agentprotocol.LogStatus {
	services, total := usageOf(s.Store)
	requests, requestsTotal := usageOf(s.Requests)
	return agentprotocol.LogStatus{
		Capturing: s.Collector.Capturing(), Retention: s.Store.Retention(), TotalBytes: total,
		Services: services, Drains: s.Drains.Statuses(),
		RequestsCapturing: s.reqCfg.Load() != nil, RequestsTotalBytes: requestsTotal, Requests: requests,
	}
}

// usageOf converts the stored volume of a store for the status report.
func usageOf(st *logstore.Store) ([]agentprotocol.LogServiceUsage, int64) {
	usage, total := st.Usage()
	out := make([]agentprotocol.LogServiceUsage, 0, len(usage))
	for _, u := range usage {
		su := agentprotocol.LogServiceUsage{ServiceID: u.ServiceID, Bytes: u.Bytes, Lines: u.Lines}
		if !u.Oldest.IsZero() {
			o, n := u.Oldest.UTC(), u.Newest.UTC()
			su.Oldest, su.Newest = &o, &n
		}
		out = append(out, su)
	}
	return out, total
}

func (s *Service) reportLoop(ctx context.Context) {
	t := time.NewTimer(5 * time.Second) // the first report soon after start
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-s.kick:
			// A new configuration: let the first drain pass run, then report.
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
			}
		}
		if s.sender != nil {
			s.sender.LogStatus(s.Status())
		}
		t.Reset(s.statusEvery)
	}
}
