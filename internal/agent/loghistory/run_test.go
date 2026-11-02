package loghistory

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nebulactrl/nebulactrl/internal/agent/logstore"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

func TestStatusReportsPerServiceUsage(t *testing.T) {
	s, _ := newService(t)
	older := time.Now().Add(-time.Hour).UnixNano()
	newer := time.Now().Add(-time.Minute).UnixNano()
	if err := s.Store.Append("svc1", []logstore.Record{{TS: older, Text: "one"}, {TS: newer, Text: "two"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Store.Append("svc2", []logstore.Record{{TS: newer, Text: "solo"}}); err != nil {
		t.Fatal(err)
	}

	st := s.Status()
	if len(st.Services) != 2 {
		t.Fatalf("services %+v", st.Services)
	}
	byID := map[string]agentprotocol.LogServiceUsage{}
	for _, u := range st.Services {
		byID[u.ServiceID] = u
	}
	if u := byID["svc1"]; u.Lines != 2 || u.Oldest == nil || u.Newest == nil || u.Oldest.UnixNano() != older || u.Newest.UnixNano() != newer {
		t.Fatalf("svc1 usage %+v", u)
	}
	if u := byID["svc2"]; u.Lines != 1 || u.Oldest == nil || u.Oldest.UnixNano() != newer {
		t.Fatalf("svc2 usage %+v", u)
	}
	if st.Services[0].Oldest.Location() != time.UTC {
		t.Fatal("timestamps are reported in UTC")
	}
	var sum int64
	for _, u := range st.Services {
		sum += u.Bytes
	}
	if st.TotalBytes != sum {
		t.Fatalf("total %d != sum of services %d", st.TotalBytes, sum)
	}
}

func TestStatusOfAnEmptyStoreHasNoServices(t *testing.T) {
	s, _ := newService(t)
	st := s.Status()
	if st.Services == nil || len(st.Services) != 0 || st.TotalBytes != 0 {
		t.Fatalf("status %+v", st)
	}
	if !st.Capturing {
		t.Fatal("capture is on until a configuration turns it off")
	}
}

func TestApplyNilConfigChangesNothing(t *testing.T) {
	s, _ := newService(t)
	before := s.Store.Retention()
	s.ApplyLogsConfig(nil, nil)
	if s.Store.Retention() != before || !s.Collector.Capturing() {
		t.Fatal("a nil configuration must leave the settings alone")
	}
	select {
	case <-s.kick:
		t.Fatal("a nil configuration must not trigger a status report")
	default:
	}
}

func TestNewFailsWhenTheStateDirIsNotADirectory(t *testing.T) {
	file := filepath.Join(t.TempDir(), "state")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := New(nopDocker{}, file, nil, nil); err == nil {
		t.Fatal("expected an error when <stateDir>/logs cannot be created")
	}
}

// Run flushes the store after the others stopped: lines still pending when the
// agent shuts down must be on disk for the next start.
func TestRunFlushesPendingLinesOnShutdown(t *testing.T) {
	dir := t.TempDir()
	sc := &statusCatcher{}
	s, err := New(nopDocker{}, dir, sc, nil)
	if err != nil {
		t.Fatal(err)
	}
	ts := time.Now().Add(-time.Second).UnixNano()
	if err := s.Store.Append("svc1", []logstore.Record{{TS: ts, Text: "last words"}}); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after its context ended")
	}

	reopened, err := logstore.Open(logstore.Options{Dir: filepath.Join(dir, "logs")})
	if err != nil {
		t.Fatal(err)
	}
	usage, _ := reopened.Usage()
	if len(usage) != 1 || usage[0].ServiceID != "svc1" || usage[0].Lines != 1 {
		t.Fatalf("usage after restart %+v", usage)
	}
}

func TestReportLoopWithoutSenderStillStopsOnCancel(t *testing.T) {
	s, err := New(nopDocker{}, t.TempDir(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	s.statusEvery = time.Hour
	s.ApplyLogsConfig(&agentprotocol.LogsConfig{Capture: true}, nil) // queue a kick
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.reportLoop(ctx); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("reportLoop ignored cancellation")
	}
}
