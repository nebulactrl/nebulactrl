package logstore

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

func seedHistory(t *testing.T) (*Store, time.Time) {
	t.Helper()
	ck := &clock{t: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)}
	st := openTest(t, t.TempDir(), ck, func(o *Options) { o.BlockMaxLines = 50 })
	base := ck.now()
	var recs []Record
	for i := range 300 {
		text := fmt.Sprintf("request %d ok", i)
		switch {
		case i%50 == 7:
			text = fmt.Sprintf(`{"level":"error","msg":"boom %d","status":500}`, i)
		case i%25 == 3:
			text = fmt.Sprintf("2026-03-01T00:00:00Z WARN slow request %d", i)
		}
		recs = append(recs, Record{TS: base.Add(time.Duration(i) * time.Second).UnixNano(), Replica: i % 2, DeploymentID: "d1", Text: text})
	}
	if err := st.Append("svc1", recs); err != nil {
		t.Fatal(err)
	}
	if err := st.Flush(); err != nil {
		t.Fatal(err)
	}
	return st, base
}

func TestQueryFiltersWithTheSearchLanguage(t *testing.T) {
	st, base := seedHistory(t)
	cases := []struct {
		query string
		want  int
	}{
		{"", 300},
		{"boom", 6},
		{"level:error", 6},
		{"level:warn", 12},
		{"@status:500", 6},
		{"@status:>=500 -boom", 0},
		{`"slow request"`, 12},
		{"service:web level:error", 6},
		{"service:other", 0},
	}
	for _, c := range cases {
		res, err := st.Query(agentprotocol.LogsQuery{ServiceID: "svc1", ServiceName: "web", Query: c.query, Limit: 2000})
		if err != nil {
			t.Fatal(err)
		}
		n := len(res.Lines)
		if n != c.want {
			t.Errorf("query %q: %d lines, want %d", c.query, n, c.want)
		}
	}

	// Facets come from the line's own record: replica and deployment.
	res, err := st.Query(agentprotocol.LogsQuery{ServiceID: "svc1", Query: "replica:1 deployment:d1 request", Limit: 2000})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Lines) == 0 {
		t.Fatal("replica:1 matched nothing")
	}
	for _, l := range res.Lines {
		if l.Replica != 1 || !strings.Contains(l.Text, "request") {
			t.Fatalf("replica:1 matched %+v", l)
		}
	}

	// A broken query still filters by its valid part and reports what it ignored.
	res, err = st.Query(agentprotocol.LogsQuery{ServiceID: "svc1", Query: "boom (", Limit: 2000})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Lines) != 6 || len(res.QueryErrors) == 0 {
		t.Fatalf("lines=%d errors=%v", len(res.Lines), res.QueryErrors)
	}

	// The time range is [from, to).
	res, err = st.Query(agentprotocol.LogsQuery{ServiceID: "svc1", From: base.Add(100 * time.Second), To: base.Add(110 * time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Lines) != 10 || res.Lines[0].Text != "request 109 ok" {
		t.Fatalf("range: %d lines, first %q", len(res.Lines), res.Lines[0].Text)
	}
}

func TestQueryPagesNewestFirstWithoutGapsOrRepeats(t *testing.T) {
	st, _ := seedHistory(t)
	for _, order := range []string{agentprotocol.LogOrderDesc, agentprotocol.LogOrderAsc} {
		var seqs []uint64
		cursor := ""
		pages := 0
		for {
			res, err := st.Query(agentprotocol.LogsQuery{ServiceID: "svc1", Limit: 70, Cursor: cursor, Order: order})
			if err != nil {
				t.Fatal(err)
			}
			pages++
			for _, l := range res.Lines {
				seqs = append(seqs, l.Seq)
			}
			if res.NextCursor == "" {
				break
			}
			cursor = res.NextCursor
		}
		if len(seqs) != 300 || pages != 5 {
			t.Fatalf("%s: %d lines in %d pages", order, len(seqs), pages)
		}
		for i := 1; i < len(seqs); i++ {
			if (order == agentprotocol.LogOrderDesc && seqs[i] != seqs[i-1]-1) || (order == agentprotocol.LogOrderAsc && seqs[i] != seqs[i-1]+1) {
				t.Fatalf("%s: seq %d then %d", order, seqs[i-1], seqs[i])
			}
		}
	}
}

func TestQueryReportsBoundsAndRejectsABadCursor(t *testing.T) {
	st, base := seedHistory(t)
	res, err := st.Query(agentprotocol.LogsQuery{ServiceID: "svc1", Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if res.Oldest == nil || !res.Oldest.Equal(base) || res.Newest == nil || !res.Newest.Equal(base.Add(299*time.Second)) {
		t.Fatalf("bounds %v %v", res.Oldest, res.Newest)
	}
	if _, err := st.Query(agentprotocol.LogsQuery{ServiceID: "svc1", Cursor: "nope"}); err == nil {
		t.Fatal("a bad cursor was accepted")
	}
	// A service the agent has never seen is empty, not an error.
	res, err = st.Query(agentprotocol.LogsQuery{ServiceID: "ghost"})
	if err != nil || len(res.Lines) != 0 || res.Oldest != nil {
		t.Fatalf("ghost: %+v %v", res, err)
	}
}
