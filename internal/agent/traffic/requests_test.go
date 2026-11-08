package traffic

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/internal/agent/logstore"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

const svcB = "22222222-2222-2222-2222-222222222222"

func TestIPPrefix(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"203.0.113.77", "203.0.113.0/24"},
		{" 203.0.113.77 ", "203.0.113.0/24"}, // surrounding space is trimmed
		{"2001:db8:abcd:1234::1", "2001:db8:abcd::/48"},
		{"2001:db8:abcd:ffff:1:2:3:4", "2001:db8:abcd::/48"},
		{"::ffff:203.0.113.9", "203.0.113.0/24"},
		{"fe80::1%eth0", "fe80::/48"},
		{"", ""},
		{"not-an-ip", ""},
		{"203.0.113", ""},
		{"203.0.113.9:8080", ""},
	} {
		require.Equal(t, tc.want, IPPrefix(tc.in), "IPPrefix(%q)", tc.in)
	}
}

func TestParseLineKeepsQueryAndUpstreamApart(t *testing.T) {
	line := func(path, addr, rawURL string) []byte {
		b, err := json.Marshal(map[string]any{
			"StartUTC": "2025-10-26T17:33:20.5Z", "RouterName": "nc-" + svcA + "-https@file",
			"RequestHost": "a.example.com", "RequestPath": path, "RequestMethod": "get", "DownstreamStatus": 200,
			"ServiceAddr": addr, "ServiceURL": rawURL,
		})
		require.NoError(t, err)
		return b
	}
	for _, tc := range []struct {
		name, path, addr, url string
		wantPath, wantQuery   string
		wantUpstream          string
	}{
		{"query", "/pricing?utm=1&email=a@b.c#frag", "nc-x:80", "http://nc-x:80", "/pricing", "utm=1&email=a@b.c", "nc-x:80"},
		{"no query", "/pricing", "nc-x:80", "", "/pricing", "", "nc-x:80"},
		{"empty query", "/pricing?", "", "http://nc-y:8080", "/pricing", "", "nc-y:8080"},
		{"no upstream", "/", "", "", "/", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ev, err := ParseLine(line(tc.path, tc.addr, tc.url))
			require.NoError(t, err)
			require.Equal(t, tc.wantPath, ev.Path)
			require.Equal(t, tc.wantQuery, ev.Query)
			require.Equal(t, tc.wantUpstream, ev.Upstream)
		})
	}
}

func decodeRecord(t *testing.T, rec logstore.Record) agentprotocol.RequestRecord {
	t.Helper()
	var out agentprotocol.RequestRecord
	require.NoError(t, json.Unmarshal([]byte(rec.Text), &out))
	return out
}

func TestNewRequestRecord(t *testing.T) {
	at := time.Unix(1761500000, 123).UTC()
	ev := Event{
		Time: at, End: at.Add(12500 * time.Microsecond), ServiceID: svcA, Host: "app.example.com", Path: "/Users/42", Query: "token=secret&x=1",
		Method: "POST", Status: 502, DurationMs: 12.5, BytesOut: 2048, BytesIn: 10,
		ClientIP: "203.0.113.77", UserAgent: "curl/8",
	}
	b := backend{process: "worker", component: "web", deployment: "dep1", replica: 2}

	t.Run("query dropped by default", func(t *testing.T) {
		rec, err := newRequestRecord(ev, false, agentprotocol.RequestKindAPI, b)
		require.NoError(t, err)
		require.Equal(t, at.Add(12500*time.Microsecond).UnixNano(), rec.TS, "the line is stamped with the end of the request")
		require.Equal(t, 2, rec.Replica)
		require.Equal(t, "dep1", rec.DeploymentID)
		require.Equal(t, agentprotocol.RequestRecord{
			Method: "POST", Host: "app.example.com", Path: "/Users/42", Kind: agentprotocol.RequestKindAPI, Level: "error", Status: 502, DurationMs: 12.5,
			Bytes: 2048, BytesIn: 10, UserAgent: "curl/8", IP: "203.0.113.0/24", Process: "worker", Component: "web",
		}, decodeRecord(t, rec))
		require.NotContains(t, rec.Text, "secret")
		require.NotContains(t, rec.Text, "203.0.113.77")
		require.NotContains(t, rec.Text, `"query"`)
		require.Contains(t, rec.Text, `{"method":"POST","host":"app.example.com","path":"/Users/42","kind":"api","level":"error","status":502`)
	})

	t.Run("query kept on request", func(t *testing.T) {
		rec, err := newRequestRecord(ev, true, agentprotocol.RequestKindOther, backend{})
		require.NoError(t, err)
		got := decodeRecord(t, rec)
		require.Equal(t, "token=secret&x=1", got.Query)
		require.Empty(t, got.Process)
		require.NotContains(t, rec.Text, "203.0.113.77")
	})

	t.Run("ip literal host is masked", func(t *testing.T) {
		e := ev
		e.Host = "203.0.113.5"
		rec, err := newRequestRecord(e, false, agentprotocol.RequestKindOther, backend{})
		require.NoError(t, err)
		require.Equal(t, agentprotocol.IPLiteralHost, decodeRecord(t, rec).Host)
		require.NotContains(t, rec.Text, "203.0.113.5")
	})

	t.Run("unparsable client address stores no ip", func(t *testing.T) {
		e := ev
		e.ClientIP = "garbage"
		rec, err := newRequestRecord(e, false, agentprotocol.RequestKindOther, backend{})
		require.NoError(t, err)
		require.Empty(t, decodeRecord(t, rec).IP)
		require.NotContains(t, rec.Text, `"ip"`)
	})

	t.Run("caps", func(t *testing.T) {
		e := ev
		e.Path = "/" + strings.Repeat("a", 3000)
		e.Query = strings.Repeat("q", 3000)
		e.UserAgent = strings.Repeat("u", 600)
		rec, err := newRequestRecord(e, true, agentprotocol.RequestKindOther, backend{})
		require.NoError(t, err)
		got := decodeRecord(t, rec)
		require.Len(t, got.Path, maxRequestPath)
		require.Len(t, got.Query, maxRequestQuery)
		require.Len(t, got.UserAgent, maxRequestUA)
		require.LessOrEqual(t, len(rec.Text), logstore.MaxLineBytes)

		e.Path = strings.Repeat("é", 2000) // 2-byte runes: the cut must not split one
		rec, err = newRequestRecord(e, false, agentprotocol.RequestKindOther, backend{})
		require.NoError(t, err)
		got = decodeRecord(t, rec)
		require.LessOrEqual(t, len(got.Path), maxRequestPath)
		require.True(t, utf8.ValidString(got.Path))
		require.NotContains(t, got.Path, string(utf8.RuneError))
	})

	t.Run("method and host are bounded", func(t *testing.T) {
		e := ev
		e.Method = strings.Repeat("M", 15<<10)
		e.Host = strings.Repeat("h", 400) + ".example.com"
		rec, err := newRequestRecord(e, false, agentprotocol.RequestKindOther, backend{})
		require.NoError(t, err)
		got := decodeRecord(t, rec)
		require.Equal(t, "OTHER", got.Method)
		require.Len(t, got.Host, maxRequestHost)
		require.Less(t, len(rec.Text), 1<<10)

		e.Method = strings.Repeat("M", maxRequestMethod)
		rec, err = newRequestRecord(e, false, agentprotocol.RequestKindOther, backend{})
		require.NoError(t, err)
		require.Equal(t, e.Method, decodeRecord(t, rec).Method, "a method at the cap is kept")
	})

	t.Run("html characters are not escaped and the text has no newline", func(t *testing.T) {
		e := ev
		e.Path = "/a&b<c>"
		e.UserAgent = strings.Repeat("<", maxRequestUA)
		rec, err := newRequestRecord(e, false, agentprotocol.RequestKindOther, backend{})
		require.NoError(t, err)
		require.Contains(t, rec.Text, `"path":"/a&b<c>"`)
		require.NotContains(t, rec.Text, `\u003c`)
		require.NotContains(t, rec.Text, `\u0026`)
		require.NotContains(t, rec.Text, "\n")
		require.Equal(t, e.UserAgent, decodeRecord(t, rec).UserAgent)
	})

	t.Run("a pathological record keeps all fields as valid JSON under the store limit", func(t *testing.T) {
		e := ev
		e.Method = strings.Repeat("M", 15<<10)
		e.Path = strings.Repeat("&", 2048)
		e.UserAgent = strings.Repeat("<", 512)
		rec, err := newRequestRecord(e, false, agentprotocol.RequestKindOther, backend{})
		require.NoError(t, err)
		require.LessOrEqual(t, len(rec.Text), logstore.MaxLineBytes)
		got := decodeRecord(t, rec)
		require.Equal(t, "OTHER", got.Method)
		require.Len(t, got.Path, 2048)
		require.Equal(t, e.UserAgent, got.UserAgent)
		require.Equal(t, 502, got.Status)
		require.Equal(t, "203.0.113.0/24", got.IP)
	})

	t.Run("control characters expand and are shrunk user agent first, then query, then path", func(t *testing.T) {
		ctl := strings.Repeat("\x01", 2048) // six bytes each once encoded
		e := ev
		e.Path = "/" + ctl[:2047]
		e.Query = ctl
		e.UserAgent = strings.Repeat("\x02", maxRequestUA)
		rec, err := newRequestRecord(e, true, agentprotocol.RequestKindOther, backend{})
		require.NoError(t, err)
		require.LessOrEqual(t, len(rec.Text), logstore.MaxLineBytes)
		got := decodeRecord(t, rec)
		require.Empty(t, got.UserAgent, "the user agent is cut first")
		require.NotEmpty(t, got.Query)
		require.Less(t, len(got.Query), maxRequestQuery, "then the query, only as far as needed")
		require.Len(t, got.Path, maxRequestPath, "the path is whole")
		require.Equal(t, "POST", got.Method)
		require.Equal(t, "app.example.com", got.Host)

		// Larger than the limit with the user agent gone: the query goes next.
		e.Query = strings.Repeat("\x01", maxRequestQuery)
		e.Path = "/" + strings.Repeat("\x01", 2047)
		e.Host = strings.Repeat("\x01", 200)
		e.UserAgent = strings.Repeat("\x02", maxRequestUA)
		b := backend{process: strings.Repeat("\x03", 300), component: strings.Repeat("\x03", 300)}
		rec, err = newRequestRecord(e, true, agentprotocol.RequestKindOther, b)
		require.NoError(t, err)
		require.LessOrEqual(t, len(rec.Text), logstore.MaxLineBytes)
		got = decodeRecord(t, rec)
		require.Empty(t, got.UserAgent)
		require.Empty(t, got.Query, "the query is cut before the path")
		require.NotEmpty(t, got.Path)
		require.Less(t, len(got.Path), maxRequestPath, "the path is the last to be cut, and only as far as needed")
		require.Equal(t, "POST", got.Method)

		// Invalid UTF-8 grows to six bytes a byte as well and is still fitted.
		e.Path = "/" + strings.Repeat("\xff", 2047)
		e.Query = strings.Repeat("\xff", maxRequestQuery)
		rec, err = newRequestRecord(e, true, agentprotocol.RequestKindOther, b)
		require.NoError(t, err)
		require.LessOrEqual(t, len(rec.Text), logstore.MaxLineBytes)
		require.True(t, json.Valid([]byte(rec.Text)))
	})

	t.Run("level follows the status only", func(t *testing.T) {
		for _, tc := range []struct {
			status int
			want   string
		}{{200, "info"}, {204, "info"}, {304, "info"}, {399, "info"}, {400, "warn"}, {404, "warn"}, {499, "warn"}, {500, "error"}, {502, "error"}} {
			e := ev
			e.Status = tc.status
			e.Path = "/x,level=error"
			e.UserAgent = "bot level=fatal"
			rec, err := newRequestRecord(e, false, agentprotocol.RequestKindOther, backend{})
			require.NoError(t, err)
			require.Equal(t, tc.want, decodeRecord(t, rec).Level, "status %d", tc.status)
		}
	})
}

type memSink struct {
	mu   sync.Mutex
	recs map[string][]logstore.Record
}

func (m *memSink) Append(id string, recs []logstore.Record) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.recs == nil {
		m.recs = map[string][]logstore.Record{}
	}
	m.recs[id] = append(m.recs[id], recs...)
	return nil
}

func processRoutes() []agentprotocol.Route {
	return []agentprotocol.Route{
		{ServiceID: svcA},
		{ServiceID: agentprotocol.ProcessRouteID(svcA, "worker"), Stack: svcA},
		{ServiceID: agentprotocol.StackRouteID(svcB, "web", 8080), Stack: svcB},
	}
}

func TestRouteResolverMapsRouteIDsToTheOwningService(t *testing.T) {
	now := time.Unix(1761500000, 0)
	var routeCalls, listCalls int
	r := newRouteResolver(
		func() []agentprotocol.Route { routeCalls++; return processRoutes() },
		func(context.Context) ([]docker.ContainerInfo, error) {
			listCalls++
			return []docker.ContainerInfo{
				{Name: "nc-aaaa-worker-0", Labels: map[string]string{
					agentprotocol.LabelService: svcA, agentprotocol.LabelProcess: "worker",
					agentprotocol.LabelReplica: "1", agentprotocol.LabelDeployment: "dep1",
				}},
				{Name: "nc-bbbb-web", Labels: map[string]string{
					agentprotocol.LabelService: svcB, agentprotocol.LabelComponent: "web", agentprotocol.LabelReplica: "3",
				}},
				{Name: "nc-cccc-other", Labels: map[string]string{agentprotocol.LabelService: svcB, agentprotocol.LabelProcess: "x"}},
			}, nil
		},
		func() time.Time { return now }, slog.Default(),
	)
	ctx := context.Background()

	owner, b, ok := r.resolve(ctx, agentprotocol.ProcessRouteID(svcA, "worker"), "nc-aaaa-worker-0:8080")
	require.True(t, ok)
	require.Equal(t, svcA, owner, "a process route belongs to its service")
	require.Equal(t, "worker", b.process)
	require.Equal(t, 1, b.replica)
	require.Equal(t, "dep1", b.deployment)

	owner, b, ok = r.resolve(ctx, agentprotocol.StackRouteID(svcB, "web", 8080), "nc-bbbb-web:8080")
	require.True(t, ok)
	require.Equal(t, svcB, owner, "a stack route belongs to the stack")
	require.Equal(t, "web", b.component)
	require.Equal(t, 3, b.replica)

	owner, _, ok = r.resolve(ctx, svcA, "10.0.0.5:80")
	require.True(t, ok)
	require.Equal(t, svcA, owner, "a plain route is its own service")
	require.Equal(t, 1, listCalls, "an upstream that is not a managed container is never looked up")

	// A container labelled for another service says nothing about this route.
	_, b, ok = r.resolve(ctx, svcA, "nc-cccc-other:80")
	require.True(t, ok)
	require.Equal(t, backend{}, b)

	require.Equal(t, 1, routeCalls, "known routes come from the cache")

	// An unknown route is not stored; the table is re-read at most once a second.
	_, _, ok = r.resolve(ctx, "33333333-3333-3333-3333-333333333333", "nc-x:80")
	require.False(t, ok)
	_, _, ok = r.resolve(ctx, "33333333-3333-3333-3333-333333333333", "nc-x:80")
	require.False(t, ok)
	require.Equal(t, 1, routeCalls)
	now = now.Add(2 * time.Second)
	_, _, ok = r.resolve(ctx, "33333333-3333-3333-3333-333333333333", "nc-x:80")
	require.False(t, ok)
	require.Equal(t, 2, routeCalls)

	// An unknown container is looked up again at most every backendRefreshEvery.
	_, _, _ = r.resolve(ctx, svcA, "nc-gone:80")
	require.Equal(t, 1, listCalls)
	now = now.Add(backendRefreshEvery)
	_, _, _ = r.resolve(ctx, svcA, "nc-gone:80")
	require.Equal(t, 2, listCalls)
}

func TestRequestWriterStoresRoutedRequestsPerOwningService(t *testing.T) {
	at := time.Unix(1761500000, 0).UTC()
	sink := &memSink{}
	cfg := &agentprotocol.RequestLogsConfig{}
	res := newRouteResolver(processRoutes, func(context.Context) ([]docker.ContainerInfo, error) {
		return []docker.ContainerInfo{{Name: "nc-aaaa-worker-0", Labels: map[string]string{
			agentprotocol.LabelService: svcA, agentprotocol.LabelProcess: "worker", agentprotocol.LabelReplica: "1",
		}}}, nil
	}, func() time.Time { return at }, slog.Default())
	w := newRequestWriter(sink, func() *agentprotocol.RequestLogsConfig { return cfg }, res, slog.Default())

	base := Event{Time: at, End: at, Host: "a.example.com", Path: "/p", Query: "k=v", Method: "GET", Status: 200, ClientIP: "203.0.113.9"}
	routed := base
	routed.ServiceID = agentprotocol.ProcessRouteID(svcA, "worker")
	routed.Upstream = "nc-aaaa-worker-0:8080"
	unrouted := base // no service
	noStatus := routed
	noStatus.Status = 0
	gone := base
	gone.ServiceID = "33333333-3333-3333-3333-333333333333"

	for _, ev := range []Event{routed, unrouted, noStatus, gone} {
		w.Add(ev)
	}
	require.Len(t, w.queue, 2, "unrouted and status-0 events are never queued")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	w.Run(ctx) // writes what is queued, then returns

	require.Len(t, sink.recs, 1, "the route the proxy does not have stores nothing")
	got := sink.recs[svcA]
	require.Len(t, got, 1)
	rec := decodeRecord(t, got[0])
	require.Equal(t, "worker", rec.Process)
	require.Equal(t, 1, got[0].Replica)
	require.Empty(t, rec.Query, "the query string is dropped unless the server keeps it")
	require.Equal(t, "203.0.113.0/24", rec.IP)
}

func TestRequestWriterSkipsSuccessfulAssets(t *testing.T) {
	at := time.Unix(1761500000, 0).UTC()
	cfg := &agentprotocol.RequestLogsConfig{}
	res := newRouteResolver(processRoutes, func(context.Context) ([]docker.ContainerInfo, error) { return nil, nil }, func() time.Time { return at }, slog.Default())
	req := func(path, contentType string, status int) Event {
		return Event{
			Time: at, End: at, ServiceID: agentprotocol.ProcessRouteID(svcA, "worker"), Host: "a.example.com", Method: "GET",
			Path: path, ContentType: contentType, Status: status, ClientIP: "203.0.113.9",
		}
	}
	// stored runs the events through a writer with the current config and returns
	// the stored lines.
	stored := func(evs ...Event) []agentprotocol.RequestRecord {
		sink := &memSink{}
		w := newRequestWriter(sink, func() *agentprotocol.RequestLogsConfig { return cfg }, res, slog.Default())
		for _, ev := range evs {
			w.Add(ev)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		w.Run(ctx)
		var out []agentprotocol.RequestRecord
		for _, rec := range sink.recs[svcA] {
			out = append(out, decodeRecord(t, rec))
		}
		return out
	}

	t.Run("a successful asset is not stored", func(t *testing.T) {
		require.Empty(t, stored(
			req("/assets/app.js", "text/javascript", 200),
			req("/assets/app.4f2c1.js", "", 304),
			req("/logo.png", "image/png", 204),
		))
	})

	t.Run("a failed asset is stored with its kind", func(t *testing.T) {
		got := stored(
			req("/assets/app.js", "text/html", 404),
			req("/assets/app.css", "", 500),
			req("/assets/app.css", "", 301),
		)
		require.Len(t, got, 3)
		for _, rec := range got {
			require.Equal(t, agentprotocol.RequestKindAsset, rec.Kind)
		}
		require.Equal(t, 404, got[0].Status)
	})

	t.Run("pages and api requests are always stored", func(t *testing.T) {
		got := stored(
			req("/api/orders", "application/json", 200),
			req("/pricing", "text/html; charset=utf-8", 200),
		)
		require.Len(t, got, 2)
		require.Equal(t, agentprotocol.RequestKindAPI, got[0].Kind)
		require.Equal(t, agentprotocol.RequestKindPage, got[1].Kind)
	})

	t.Run("an asset extension does not hide other methods or other answers", func(t *testing.T) {
		cfg = &agentprotocol.RequestLogsConfig{}
		post := req("/users/1.js", "text/javascript", 200)
		post.Method = "POST"
		html := req("/admin;x.js", "text/html", 200)
		got := stored(post, html, req("/assets/app.js", "text/javascript", 200))
		require.Len(t, got, 2, "only the GET of an asset answering an asset is skipped")
		require.Equal(t, "POST", got[0].Method)
		require.Equal(t, agentprotocol.RequestKindAsset, got[0].Kind)
		require.Equal(t, "/admin;x.js", got[1].Path)
		require.Equal(t, agentprotocol.RequestKindPage, got[1].Kind)
	})

	t.Run("the server keeps assets", func(t *testing.T) {
		cfg = &agentprotocol.RequestLogsConfig{KeepAssets: true}
		got := stored(req("/assets/app.js", "text/javascript", 200), req("/assets/app.js", "", 304))
		require.Len(t, got, 2)
		require.Equal(t, agentprotocol.RequestKindAsset, got[0].Kind)
	})
}

func TestRequestWriterOffAndOverflow(t *testing.T) {
	var cfg *agentprotocol.RequestLogsConfig
	res := newRouteResolver(processRoutes, nil, time.Now, slog.Default())
	w := newRequestWriter(&memSink{}, func() *agentprotocol.RequestLogsConfig { return cfg }, res, slog.Default())
	ev := Event{Time: time.Now(), End: time.Now(), ServiceID: svcA, Status: 200}

	w.Add(ev)
	require.Empty(t, w.queue, "request logs off: nothing is queued")

	cfg = &agentprotocol.RequestLogsConfig{KeepQuery: true}
	for range requestQueue + 7 {
		w.Add(ev)
	}
	require.Len(t, w.queue, requestQueue)
	require.EqualValues(t, 7, w.dropped.Load(), "a full queue drops and counts instead of blocking")
	q := <-w.queue
	require.True(t, q.keepQuery, "the setting at read time travels with the event")
}
