// Package loghistory wires the agent's log history: the segment store, the
// collector that feeds it from the managed containers, the drains and the
// control-plane facing pieces (history queries, drain tests, configuration,
// status reports).
package loghistory

import (
	"context"
	"encoding/json"
	"log/slog"
	"path/filepath"
	"sync"
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

// Service is the agent's log history.
type Service struct {
	Store     *logstore.Store
	Collector *logstore.Collector
	Drains    *drains.Manager

	log    *slog.Logger
	sender StatusSender

	// statusEvery is how often the status is reported (default LogStatusInterval).
	statusEvery time.Duration
	kick        chan struct{}
}

// New opens the store under <stateDir>/logs. docker feeds the collector.
func New(eng docker.Engine, stateDir string, sender StatusSender, log *slog.Logger) (*Service, error) {
	if log == nil {
		log = slog.Default()
	}
	root := filepath.Join(stateDir, "logs")
	st, err := logstore.Open(logstore.Options{Dir: root, Logger: log})
	if err != nil {
		return nil, err
	}
	col := logstore.NewCollector(eng, st, log)
	col.CheckpointFile = filepath.Join(stateDir, "logs-positions.json")
	return &Service{
		Store: st, Collector: col, Drains: drains.NewManager(st, filepath.Join(stateDir, "logdrains"), log),
		log: log, sender: sender, statusEvery: agentprotocol.LogStatusInterval, kick: make(chan struct{}, 1),
	}, nil
}

// Run captures, ships and reports until ctx ends. The store outlives the
// others: it flushes and closes once the collector and the drains have stopped.
func (s *Service) Run(ctx context.Context) {
	storeCtx, stopStore := context.WithCancel(context.Background())
	storeDone := make(chan struct{})
	go func() {
		defer close(storeDone)
		s.Store.Run(storeCtx)
	}()
	var wg sync.WaitGroup
	for _, fn := range []func(context.Context){s.Collector.Run, s.Drains.Run, s.reportLoop} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			fn(ctx)
		}()
	}
	wg.Wait()
	stopStore()
	<-storeDone
}

// ApplyLogsConfig implements engine.LogsService.
func (s *Service) ApplyLogsConfig(cfg *agentprotocol.LogsConfig, secrets map[string]string) {
	if cfg == nil {
		return
	}
	s.Store.SetRetention(cfg.Retention)
	s.Collector.SetCapture(cfg.Capture)
	s.Drains.Apply(cfg, secrets)
	select {
	case s.kick <- struct{}{}:
	default:
	}
}

// HandleLogsCommand implements engine.LogsService for KindLogsQuery and KindLogsDrainTest.
func (s *Service) HandleLogsCommand(ctx context.Context, cmd agentprotocol.Command) agentprotocol.ResultFrame {
	fail := func(msg string) agentprotocol.ResultFrame {
		return agentprotocol.ResultFrame{CommandID: cmd.ID, Outcome: agentprotocol.OutcomeFailed, Error: msg}
	}
	var data any
	switch cmd.Kind {
	case agentprotocol.KindLogsQuery:
		var q agentprotocol.LogsQuery
		if err := json.Unmarshal(cmd.Payload, &q); err != nil {
			return fail("invalid logs_query payload: " + err.Error())
		}
		if q.ServiceID == "" {
			return fail("serviceId is required")
		}
		res, err := s.Store.Query(q)
		if err != nil {
			return fail(err.Error())
		}
		res.Capturing = s.Collector.Capturing()
		data = res
	case agentprotocol.KindLogsDrainTest:
		var t agentprotocol.LogsDrainTest
		if err := json.Unmarshal(cmd.Payload, &t); err != nil {
			return fail("invalid logs_drain_test payload: " + err.Error())
		}
		data = s.Drains.Test(ctx, t.Drain, cmd.Secrets)
	default:
		return fail("unsupported command kind: " + cmd.Kind)
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return fail("encode result: " + err.Error())
	}
	return agentprotocol.ResultFrame{CommandID: cmd.ID, Outcome: agentprotocol.OutcomeSucceeded, Data: raw}
}

// Status is the report sent to the control plane.
func (s *Service) Status() agentprotocol.LogStatus {
	usage, total := s.Store.Usage()
	st := agentprotocol.LogStatus{
		Capturing: s.Collector.Capturing(), Retention: s.Store.Retention(), TotalBytes: total,
		Services: make([]agentprotocol.LogServiceUsage, 0, len(usage)), Drains: s.Drains.Statuses(),
	}
	for _, u := range usage {
		su := agentprotocol.LogServiceUsage{ServiceID: u.ServiceID, Bytes: u.Bytes, Lines: u.Lines}
		if !u.Oldest.IsZero() {
			o, n := u.Oldest.UTC(), u.Newest.UTC()
			su.Oldest, su.Newest = &o, &n
		}
		st.Services = append(st.Services, su)
	}
	return st
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
