package loghistory

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/internal/agent/logstore"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

type nopDocker struct{ docker.Engine }

func (nopDocker) ContainerList(context.Context, docker.ListFilter) ([]docker.ContainerInfo, error) {
	return nil, nil
}

type statusCatcher struct {
	mu  sync.Mutex
	got []agentprotocol.LogStatus
}

func (c *statusCatcher) LogStatus(s agentprotocol.LogStatus) {
	c.mu.Lock()
	c.got = append(c.got, s)
	c.mu.Unlock()
}

func newService(t *testing.T) (*Service, *statusCatcher) {
	t.Helper()
	sc := &statusCatcher{}
	s, err := New(nopDocker{}, t.TempDir(), sc, nil)
	if err != nil {
		t.Fatal(err)
	}
	return s, sc
}

func command(t *testing.T, kind string, payload any) agentprotocol.Command {
	t.Helper()
	raw, _ := json.Marshal(payload)
	return agentprotocol.Command{ID: "c1", Kind: kind, Payload: raw}
}

func TestQueryCommandAnswersFromTheStore(t *testing.T) {
	s, _ := newService(t)
	now := time.Now()
	if err := s.Store.Append("svc1", []logstore.Record{
		{TS: now.Add(-2 * time.Second).UnixNano(), Text: "level=info hello"},
		{TS: now.Add(-1 * time.Second).UnixNano(), Text: "level=error broken"},
	}); err != nil {
		t.Fatal(err)
	}
	res := s.HandleLogsCommand(context.Background(), command(t, agentprotocol.KindLogsQuery, agentprotocol.LogsQuery{ServiceID: "svc1", Query: "level:error"}))
	if res.Outcome != agentprotocol.OutcomeSucceeded || res.CommandID != "c1" {
		t.Fatalf("result %+v", res)
	}
	var out agentprotocol.LogsQueryResult
	if err := json.Unmarshal(res.Data, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Lines) != 1 || out.Lines[0].Text != "level=error broken" || !out.Capturing || out.Oldest == nil {
		t.Fatalf("answer %+v", out)
	}

	for _, bad := range []agentprotocol.Command{
		{ID: "c2", Kind: agentprotocol.KindLogsQuery, Payload: json.RawMessage(`{`)},
		command(t, agentprotocol.KindLogsQuery, agentprotocol.LogsQuery{}),
		command(t, agentprotocol.KindLogsQuery, agentprotocol.LogsQuery{ServiceID: "svc1", Cursor: "x"}),
		command(t, "deploy", struct{}{}),
	} {
		if r := s.HandleLogsCommand(context.Background(), bad); r.Outcome != agentprotocol.OutcomeFailed || r.Error == "" {
			t.Errorf("%s accepted: %+v", bad.Kind, r)
		}
	}
}

func TestConfigAppliesRetentionCaptureAndReportsStatus(t *testing.T) {
	s, sc := newService(t)
	s.ApplyLogsConfig(&agentprotocol.LogsConfig{
		Capture:   false,
		Retention: agentprotocol.LogRetention{Days: 3, MaxServiceBytes: 64 << 20, MaxServerBytes: 1 << 30},
		Drains:    []agentprotocol.LogDrain{{ID: "d1", Name: "d1", Kind: agentprotocol.DrainHTTP, Enabled: false}},
	}, nil)
	if s.Collector.Capturing() {
		t.Fatal("capture should be off")
	}
	if r := s.Store.Retention(); r.Days != 3 || r.MaxServiceBytes != 64<<20 || r.MaxServerBytes != 1<<30 {
		t.Fatalf("retention %+v", r)
	}
	st := s.Status()
	if st.Capturing || st.Retention.Days != 3 || len(st.Drains) != 1 || st.Drains[0].State != agentprotocol.DrainDisabled {
		t.Fatalf("status %+v", st)
	}

	// A configuration kicks a report.
	s.statusEvery = time.Hour
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.reportLoop(ctx); close(done) }()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		sc.mu.Lock()
		n := len(sc.got)
		sc.mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	<-done
	sc.mu.Lock()
	defer sc.mu.Unlock()
	if len(sc.got) == 0 {
		t.Fatal("no status was reported")
	}
}

func TestDrainTestCommandReportsFailureAsAResult(t *testing.T) {
	s, _ := newService(t)
	res := s.HandleLogsCommand(context.Background(), command(t, agentprotocol.KindLogsDrainTest, agentprotocol.LogsDrainTest{
		Drain: agentprotocol.LogDrain{ID: "d1", Name: "x", Kind: agentprotocol.DrainHTTP, Enabled: true, HTTP: &agentprotocol.HTTPDrainConfig{URL: "ftp://nope"}},
	}))
	if res.Outcome != agentprotocol.OutcomeSucceeded {
		t.Fatalf("a failed test is an answer, not a command failure: %+v", res)
	}
	var tr agentprotocol.LogsDrainTestResult
	if err := json.Unmarshal(res.Data, &tr); err != nil {
		t.Fatal(err)
	}
	if tr.OK || tr.Message == "" {
		t.Fatalf("result %+v", tr)
	}
}
