package loghistory

import (
	"context"
	"encoding/json"
	"strings"
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

func TestRequestsSourceReadsTheRequestStoreOnly(t *testing.T) {
	s, _ := newService(t)
	now := time.Now()
	reqLine := `{"method":"GET","host":"a.example.com","path":"/","status":503,"duration_ms":4,"bytes":1}`
	if err := s.Store.Append("svc1", []logstore.Record{{TS: now.Add(-2 * time.Second).UnixNano(), Text: "level=info runtime"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Requests.Append("svc1", []logstore.Record{{TS: now.Add(-time.Second).UnixNano(), Text: reqLine}}); err != nil {
		t.Fatal(err)
	}
	if resume := s.RequestsResume(); len(resume) != 1 || !resume["svc1"].After(now.Add(-2*time.Second)) {
		t.Fatalf("request resume points %v", resume)
	}

	query := func(source string) agentprotocol.LogsQueryResult {
		t.Helper()
		res := s.HandleLogsCommand(context.Background(), command(t, agentprotocol.KindLogsQuery, agentprotocol.LogsQuery{ServiceID: "svc1", Source: source}))
		if res.Outcome != agentprotocol.OutcomeSucceeded {
			t.Fatalf("%s: %+v", source, res)
		}
		var out agentprotocol.LogsQueryResult
		if err := json.Unmarshal(res.Data, &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	if out := query(agentprotocol.LogSourceRequests); len(out.Lines) != 1 || out.Lines[0].Text != reqLine || out.Capturing {
		t.Fatalf("requests before any configuration: %+v", out)
	}
	if out := query(""); len(out.Lines) != 1 || out.Lines[0].Text != "level=info runtime" {
		t.Fatalf("runtime: %+v", out)
	}

	s.ApplyLogsConfig(&agentprotocol.LogsConfig{Capture: true, Requests: &agentprotocol.RequestLogsConfig{KeepQuery: true}}, nil)
	if cfg := s.RequestLogs(); cfg == nil || !cfg.KeepQuery {
		t.Fatalf("request log setting %+v", cfg)
	}
	if out := query(agentprotocol.LogSourceRequests); !out.Capturing {
		t.Fatalf("requests with the setting on: %+v", out)
	}
	if r := s.Requests.Retention(); r.MaxServiceBytes != agentprotocol.DefaultRequestLogMaxServiceBytes ||
		r.MaxServerBytes != agentprotocol.DefaultRequestLogMaxServerBytes || r.Days != agentprotocol.DefaultLogRetentionDays {
		t.Fatalf("request retention %+v", r)
	}
	st := s.Status()
	if !st.RequestsCapturing || len(st.Requests) != 1 || st.Requests[0].ServiceID != "svc1" {
		t.Fatalf("status %+v", st)
	}
	if len(st.Services) != 1 {
		t.Fatalf("runtime usage must not include request lines: %+v", st.Services)
	}

	// Off: new lines are refused by the writer, stored ones stay.
	s.ApplyLogsConfig(&agentprotocol.LogsConfig{Capture: true}, nil)
	if s.RequestLogs() != nil || s.Status().RequestsCapturing {
		t.Fatal("request logs should be off")
	}
	if out := query(agentprotocol.LogSourceRequests); len(out.Lines) != 1 || out.Capturing {
		t.Fatalf("stored request lines stay readable while off: %+v", out)
	}

	bad := s.HandleLogsCommand(context.Background(), command(t, agentprotocol.KindLogsQuery, agentprotocol.LogsQuery{ServiceID: "svc1", Source: "nope"}))
	if bad.Outcome != agentprotocol.OutcomeFailed || bad.Error == "" {
		t.Fatalf("unknown source accepted: %+v", bad)
	}
}

func TestFacetsCommandAnswersFromTheMatchingStore(t *testing.T) {
	s, _ := newService(t)
	now := time.Now()
	if err := s.Requests.Append("svc1", []logstore.Record{
		{TS: now.Add(-2 * time.Second).UnixNano(), Text: `{"method":"GET","host":"a","path":"/","status":200,"duration_ms":1,"bytes":1}`},
		{TS: now.Add(-time.Second).UnixNano(), Text: `{"method":"POST","host":"a","path":"/","status":503,"duration_ms":1,"bytes":1}`},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Store.Append("svc1", []logstore.Record{{TS: now.Add(-time.Second).UnixNano(), Text: "level=info runtime"}}); err != nil {
		t.Fatal(err)
	}
	facets := func(source string) agentprotocol.LogsFacetsResult {
		t.Helper()
		res := s.HandleFacetsCommand(context.Background(), command(t, agentprotocol.KindLogsFacets, agentprotocol.LogsFacetsQuery{
			ServiceID: "svc1", Source: source, From: now.Add(-time.Minute), To: now.Add(time.Minute),
		}))
		if res.Outcome != agentprotocol.OutcomeSucceeded {
			t.Fatalf("%s: %+v", source, res)
		}
		var out agentprotocol.LogsFacetsResult
		if err := json.Unmarshal(res.Data, &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	if out := facets(agentprotocol.LogSourceRequests); out.Matched != 2 {
		t.Fatalf("requests facets %+v", out)
	}
	if out := facets(""); out.Matched != 1 {
		t.Fatalf("runtime facets %+v", out)
	}
	// KindLogsFacets also arrives through the engine's HandleLogsCommand.
	dispatch := command(t, agentprotocol.KindLogsFacets, agentprotocol.LogsFacetsQuery{ServiceID: "svc1", From: now.Add(-time.Minute), To: now.Add(time.Minute)})
	if res := s.HandleLogsCommand(context.Background(), dispatch); res.Outcome != agentprotocol.OutcomeSucceeded {
		t.Fatalf("dispatch: %+v", res)
	}
	for _, bad := range []agentprotocol.Command{
		{ID: "c2", Kind: agentprotocol.KindLogsFacets, Payload: json.RawMessage(`{`)},
		command(t, agentprotocol.KindLogsFacets, agentprotocol.LogsFacetsQuery{}),
		command(t, agentprotocol.KindLogsFacets, agentprotocol.LogsFacetsQuery{ServiceID: "svc1", Source: "nope"}),
	} {
		if r := s.HandleFacetsCommand(context.Background(), bad); r.Outcome != agentprotocol.OutcomeFailed || r.Error == "" {
			t.Errorf("accepted: %+v", r)
		}
	}
}

func TestLogReadsAreCappedAndWaitForASlot(t *testing.T) {
	s, _ := newService(t)
	now := time.Now()
	facets := command(t, agentprotocol.KindLogsFacets, agentprotocol.LogsFacetsQuery{ServiceID: "svc1", From: now.Add(-time.Minute), To: now.Add(time.Minute)})
	query := command(t, agentprotocol.KindLogsQuery, agentprotocol.LogsQuery{ServiceID: "svc1", From: now.Add(-time.Minute), To: now.Add(time.Minute)})
	// Four reads hold every slot.
	for range maxConcurrentReads {
		s.readSlots <- struct{}{}
	}
	run := func(ctx context.Context, cmd agentprotocol.Command) <-chan agentprotocol.ResultFrame {
		out := make(chan agentprotocol.ResultFrame, 1)
		go func() { out <- s.HandleLogsCommand(ctx, cmd) }()
		return out
	}
	fifth := run(context.Background(), facets)
	cancelled, cancel := context.WithCancel(context.Background())
	sixth := run(cancelled, query)
	select {
	case res := <-fifth:
		t.Fatalf("the fifth read ran with every slot taken: %+v", res)
	case res := <-sixth:
		t.Fatalf("the sixth read ran with every slot taken: %+v", res)
	case <-time.After(100 * time.Millisecond):
	}

	// A query that gives up fails with a clear error and takes no slot.
	cancel()
	res := <-sixth
	if res.Outcome != agentprotocol.OutcomeFailed || !strings.Contains(res.Error, "slot") {
		t.Fatalf("cancelled read: %+v", res)
	}
	select {
	case res := <-fifth:
		t.Fatalf("the fifth read ran with every slot taken: %+v", res)
	case <-time.After(50 * time.Millisecond):
	}

	// A finished read frees a slot for the waiting one.
	<-s.readSlots
	select {
	case res := <-fifth:
		if res.Outcome != agentprotocol.OutcomeSucceeded {
			t.Fatalf("fifth read: %+v", res)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the fifth read did not get the freed slot")
	}
	if got := len(s.readSlots); got != maxConcurrentReads-1 {
		t.Fatalf("slots held after the fifth finished: %d, want %d", got, maxConcurrentReads-1)
	}
}

// The wait for a slot ends with the command: the control plane stopped waiting
// one minute before ExpiresAt, so an endless context must not hold the command.
func TestReadSlotWaitEndsWithTheCommandDeadline(t *testing.T) {
	s, _ := newService(t)
	now := time.Now()
	q := agentprotocol.LogsQuery{ServiceID: "svc1", From: now.Add(-time.Minute), To: now.Add(time.Minute)}
	for range maxConcurrentReads {
		s.readSlots <- struct{}{}
	}
	run := func(cmd agentprotocol.Command) <-chan agentprotocol.ResultFrame {
		out := make(chan agentprotocol.ResultFrame, 1)
		go func() { out <- s.HandleLogsCommand(context.Background(), cmd) }()
		return out
	}

	// A deadline 200 ms away: the command waits for it, then fails.
	soon := command(t, agentprotocol.KindLogsQuery, q)
	soon.ExpiresAt = time.Now().Add(time.Minute + 200*time.Millisecond)
	waiting := run(soon)
	select {
	case res := <-waiting:
		t.Fatalf("the read gave up before its deadline: %+v", res)
	case <-time.After(50 * time.Millisecond):
	}
	select {
	case res := <-waiting:
		if res.Outcome != agentprotocol.OutcomeFailed || !strings.Contains(res.Error, "slot") {
			t.Fatalf("read past its deadline: %+v", res)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the read kept waiting for a slot after the command's deadline")
	}

	// A deadline already past: no wait, and no scan even with a free slot.
	late := command(t, agentprotocol.KindLogsFacets, agentprotocol.LogsFacetsQuery{ServiceID: "svc1", From: q.From, To: q.To})
	late.ExpiresAt = time.Now().Add(time.Minute - time.Second)
	select {
	case res := <-run(late):
		if res.Outcome != agentprotocol.OutcomeFailed {
			t.Fatalf("expired read: %+v", res)
		}
	case <-time.After(time.Second):
		t.Fatal("an expired read waited")
	}
	<-s.readSlots
	select {
	case res := <-run(late):
		if res.Outcome != agentprotocol.OutcomeFailed || !strings.Contains(res.Error, "expired") {
			t.Fatalf("expired read with a free slot: %+v", res)
		}
	case <-time.After(time.Second):
		t.Fatal("an expired read waited")
	}
}
