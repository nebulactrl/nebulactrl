package sinks

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// ---- helpers ----

type captured struct {
	method   string
	path     string
	rawQuery string
	header   http.Header
	body     []byte // decoded (gunzipped) body
}

type captureServer struct {
	*httptest.Server
	mu   sync.Mutex
	reqs []captured
}

func (c *captureServer) requests() []captured {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]captured(nil), c.reqs...)
}

func (c *captureServer) only(t *testing.T) captured {
	t.Helper()
	r := c.requests()
	if len(r) != 1 {
		t.Fatalf("got %d requests, want 1", len(r))
	}
	return r[0]
}

// newCaptureServer answers every request with status and records it.
func newCaptureServer(t *testing.T, status int, respBody string, respHdr map[string]string) *captureServer {
	t.Helper()
	cs := &captureServer{}
	cs.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		body := raw
		if r.Header.Get("Content-Encoding") == "gzip" {
			zr, err := gzip.NewReader(bytes.NewReader(raw))
			if err != nil {
				t.Errorf("gzip header: %v", err)
			} else if body, err = io.ReadAll(zr); err != nil {
				t.Errorf("gunzip: %v", err)
			}
		}
		cs.mu.Lock()
		cs.reqs = append(cs.reqs, captured{r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Clone(), body})
		cs.mu.Unlock()
		for k, v := range respHdr {
			w.Header().Set(k, v)
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, respBody)
	}))
	t.Cleanup(cs.Close)
	return cs
}

func mustNew(t *testing.T, d agentprotocol.LogDrain, secrets map[string]string) Sink {
	t.Helper()
	s, err := New(d, secrets)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func lokiDrain(url string, c agentprotocol.LokiDrainConfig) agentprotocol.LogDrain {
	c.URL = url
	return agentprotocol.LogDrain{ID: "d1", Name: "loki", Kind: agentprotocol.DrainLoki, Loki: &c}
}

func otlpDrain(url string, c agentprotocol.OTLPDrainConfig) agentprotocol.LogDrain {
	c.URL = url
	return agentprotocol.LogDrain{ID: "d1", Name: "otlp", Kind: agentprotocol.DrainOTLP, OTLP: &c}
}

func httpDrain(url string, c agentprotocol.HTTPDrainConfig) agentprotocol.LogDrain {
	c.URL = url
	return agentprotocol.LogDrain{ID: "d1", Name: "http", Kind: agentprotocol.DrainHTTP, HTTP: &c}
}

func syslogDrain(c agentprotocol.SyslogDrainConfig) agentprotocol.LogDrain {
	return agentprotocol.LogDrain{ID: "d1", Name: "syslog", Kind: agentprotocol.DrainSyslog, Syslog: &c}
}

func decodeJSON(t *testing.T, b []byte) any {
	t.Helper()
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatalf("invalid JSON %q: %v", b, err)
	}
	return v
}

func assertJSON(t *testing.T, got []byte, want string) {
	t.Helper()
	g, w := decodeJSON(t, got), decodeJSON(t, []byte(want))
	if !reflect.DeepEqual(g, w) {
		t.Fatalf("JSON mismatch\n got: %s\nwant: %s", got, want)
	}
}

var base = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

func ns(t time.Time) string { return strconv.FormatInt(t.UnixNano(), 10) }

func apiEntry() Entry {
	return Entry{
		TS: base, Text: "first", Level: "info", Stream: "runtime",
		ServiceID: "s1", Service: "api", ServiceSlug: "api",
		ProjectID: "p1", Project: "shop", EnvironmentID: "e1", Environment: "prod",
		DeploymentID: "dep1", Replica: 1, Server: "srv-1",
	}
}

func sendErr(t *testing.T, err error) *SendError {
	t.Helper()
	var se *SendError
	if !errors.As(err, &se) {
		t.Fatalf("error %v (%T) is not a *SendError", err, err)
	}
	return se
}

// ---- Loki ----

func TestLokiBody(t *testing.T) {
	srv := newCaptureServer(t, http.StatusNoContent, "", nil)
	s := mustNew(t, lokiDrain(srv.URL+"/loki/api/v1/push", agentprotocol.LokiDrainConfig{
		TenantID: "tenant-1",
		Labels:   map[string]string{"team": "core", "bad-name.x": "v", "1st": "a", "service": "override", "empty": ""},
	}), map[string]string{
		agentprotocol.LogDrainSecretUsername: "alice",
		agentprotocol.LogDrainSecretPassword: "s3cretpw",
		agentprotocol.LogDrainSecretToken:    "ignored-because-basic",
	})

	a1 := apiEntry()
	a1.TS, a1.Text = base.Add(2*time.Second), "second"
	a0 := apiEntry()
	w := apiEntry()
	w.TS, w.Text, w.Level, w.ServiceID, w.Service = base.Add(time.Second), "boom <&>", "ERROR", "s2", "worker"

	if err := s.Send(t.Context(), []Entry{a1, w, a0}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	r := srv.only(t)
	if r.method != http.MethodPost || r.path != "/loki/api/v1/push" {
		t.Fatalf("request = %s %s", r.method, r.path)
	}
	if got := r.header.Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q", got)
	}
	if got := r.header.Get("User-Agent"); got != "nebulactrl-agent" {
		t.Fatalf("User-Agent = %q", got)
	}
	if got := r.header.Get("X-Scope-OrgID"); got != "tenant-1" {
		t.Fatalf("X-Scope-OrgID = %q", got)
	}
	if u, p, ok := parseBasic(r.header); !ok || u != "alice" || p != "s3cretpw" {
		t.Fatalf("basic auth = %q %q %v (%q)", u, p, ok, r.header.Get("Authorization"))
	}
	common := `"_1st":"a","bad_name_x":"v","environment":"prod","project":"shop","server":"srv-1","stream":"runtime","team":"core"`
	// Streams sorted by label string: level="error" sorts before level="info".
	assertJSON(t, r.body, fmt.Sprintf(`{"streams":[
		{"stream":{%s,"level":"error","service":"worker","service_id":"s2"},"values":[[%q,"boom <&>"]]},
		{"stream":{%s,"level":"info","service":"api","service_id":"s1"},"values":[[%q,"first"],[%q,"second"]]}
	]}`, common, ns(base.Add(time.Second)), common, ns(base), ns(base.Add(2*time.Second))))
	if !strings.Contains(string(r.body), "boom <&>") {
		t.Fatalf("body is HTML-escaped: %s", r.body)
	}
}

func parseBasic(h http.Header) (string, string, bool) {
	r := &http.Request{Header: h}
	return r.BasicAuth()
}

func TestLokiOmitsEmptyLabelsAndUnknownLevel(t *testing.T) {
	srv := newCaptureServer(t, http.StatusNoContent, "", nil)
	s := mustNew(t, lokiDrain(srv.URL, agentprotocol.LokiDrainConfig{}), nil)
	if err := s.Send(t.Context(), []Entry{{TS: base, Text: "x", Level: "weird", Service: "api"}}); err != nil {
		t.Fatal(err)
	}
	assertJSON(t, srv.only(t).body, fmt.Sprintf(`{"streams":[{"stream":{"service":"api"},"values":[[%q,"x"]]}]}`, ns(base)))
	if got := srv.only(t).header.Get("X-Scope-OrgID"); got != "" {
		t.Fatalf("X-Scope-OrgID = %q without tenant", got)
	}
	if got := srv.only(t).header.Get("Authorization"); got != "" {
		t.Fatalf("Authorization = %q without credentials", got)
	}
}

func TestAuthHeaders(t *testing.T) {
	headers := `{"X-Api-Key":"key-123456","X-Other":"o"}`
	tests := []struct {
		name    string
		drain   func(url string) agentprotocol.LogDrain
		secrets map[string]string
		auth    string
		extra   bool
	}{
		{"loki bearer", func(u string) agentprotocol.LogDrain { return lokiDrain(u, agentprotocol.LokiDrainConfig{}) },
			map[string]string{"token": "tok-abcdef"}, "Bearer tok-abcdef", false},
		{"loki bearer+headers", func(u string) agentprotocol.LogDrain { return lokiDrain(u, agentprotocol.LokiDrainConfig{}) },
			map[string]string{"token": "tok-abcdef", "headers": headers}, "Bearer tok-abcdef", true},
		{"loki username without password", func(u string) agentprotocol.LogDrain { return lokiDrain(u, agentprotocol.LokiDrainConfig{}) },
			map[string]string{"username": "bob"}, "Basic " + b64("bob:"), false},
		{"otlp bearer+headers", func(u string) agentprotocol.LogDrain { return otlpDrain(u, agentprotocol.OTLPDrainConfig{}) },
			map[string]string{"token": "tok-abcdef", "headers": headers}, "Bearer tok-abcdef", true},
		{"otlp ignores basic", func(u string) agentprotocol.LogDrain { return otlpDrain(u, agentprotocol.OTLPDrainConfig{}) },
			map[string]string{"username": "u", "password": "p", "headers": headers}, "", true},
		{"http basic", func(u string) agentprotocol.LogDrain { return httpDrain(u, agentprotocol.HTTPDrainConfig{}) },
			map[string]string{"username": "u", "password": "pw", "token": "tok"}, "Basic " + b64("u:pw"), false},
		{"http bearer+headers", func(u string) agentprotocol.LogDrain { return httpDrain(u, agentprotocol.HTTPDrainConfig{}) },
			map[string]string{"token": "tok-abcdef", "headers": headers}, "Bearer tok-abcdef", true},
		{"http headers only", func(u string) agentprotocol.LogDrain { return httpDrain(u, agentprotocol.HTTPDrainConfig{}) },
			map[string]string{"headers": headers}, "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := newCaptureServer(t, http.StatusOK, "", nil)
			s := mustNew(t, tc.drain(srv.URL), tc.secrets)
			if err := s.Send(t.Context(), []Entry{apiEntry()}); err != nil {
				t.Fatal(err)
			}
			h := srv.only(t).header
			if got := h.Get("Authorization"); got != tc.auth {
				t.Fatalf("Authorization = %q, want %q", got, tc.auth)
			}
			if tc.extra && (h.Get("X-Api-Key") != "key-123456" || h.Get("X-Other") != "o") {
				t.Fatalf("headers secret not applied: %v", h)
			}
			if !tc.extra && h.Get("X-Api-Key") != "" {
				t.Fatalf("unexpected X-Api-Key")
			}
		})
	}
}

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

func TestBadSecrets(t *testing.T) {
	for name, secrets := range map[string]map[string]string{
		"headers not an object": {"headers": `["a"]`},
		"headers bad json":      {"headers": `{`},
		"headers non-string":    {"headers": `{"X":1}`},
		"headers bad name":      {"headers": `{"X Y":"v"}`},
		"headers newline value": {"headers": "{\"X\":\"a\\nb\"}"},
		"headers reserved":      {"headers": `{"Content-Length":"1"}`},
		"token newline":         {"token": "a\nb"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := New(lokiDrain("http://loki:3100/push", agentprotocol.LokiDrainConfig{}), secrets)
			if err == nil {
				t.Fatal("expected an error")
			}
			var se *SendError
			if errors.As(err, &se) {
				t.Fatalf("misconfiguration must be a plain error, got %T", err)
			}
		})
	}
}

func TestGzip(t *testing.T) {
	big := make([]Entry, 200)
	for i := range big {
		big[i] = apiEntry()
		big[i].TS = base.Add(time.Duration(i) * time.Millisecond)
		big[i].Text = strings.Repeat("log line ", 12) + strconv.Itoa(i)
	}
	small := []Entry{apiEntry()}

	for _, tc := range []struct {
		name  string
		drain func(url string) agentprotocol.LogDrain
		gzip  bool
	}{
		{"loki", func(u string) agentprotocol.LogDrain { return lokiDrain(u, agentprotocol.LokiDrainConfig{}) }, true},
		{"otlp", func(u string) agentprotocol.LogDrain { return otlpDrain(u, agentprotocol.OTLPDrainConfig{}) }, true},
		{"http", func(u string) agentprotocol.LogDrain { return httpDrain(u, agentprotocol.HTTPDrainConfig{}) }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := newCaptureServer(t, http.StatusOK, "", nil)
			s := mustNew(t, tc.drain(srv.URL), nil)
			if err := s.Send(t.Context(), big); err != nil {
				t.Fatal(err)
			}
			if err := s.Send(t.Context(), small); err != nil {
				t.Fatal(err)
			}
			reqs := srv.requests()
			if len(reqs) != 2 {
				t.Fatalf("got %d requests", len(reqs))
			}
			if got := reqs[0].header.Get("Content-Encoding") == "gzip"; got != tc.gzip {
				t.Fatalf("large body gzip = %v, want %v", got, tc.gzip)
			}
			if len(reqs[0].body) <= 1024 {
				t.Fatalf("decoded body too small to test: %d", len(reqs[0].body))
			}
			decodeJSON(t, reqs[0].body) // the decoded body is valid JSON
			if reqs[1].header.Get("Content-Encoding") != "" {
				t.Fatalf("small body compressed")
			}
		})
	}
}

// ---- OTLP ----

func TestOTLPBody(t *testing.T) {
	srv := newCaptureServer(t, http.StatusOK, `{"partialSuccess":{"rejectedLogRecords":"2","errorMessage":"nope"}}`, nil)
	s := mustNew(t, otlpDrain(srv.URL+"/v1/logs", agentprotocol.OTLPDrainConfig{
		ResourceAttributes: map[string]string{"k": "v", "host.name": "ignored", "deployment.environment": "legacy", "blank": ""},
	}), nil)
	observed := time.Date(2026, 1, 1, 12, 0, 5, 0, time.UTC)
	s.(*otlpSink).now = func() time.Time { return observed }

	a := apiEntry()
	b := apiEntry()
	b.TS, b.Text, b.Level = base.Add(time.Second), "careful", "warn"
	c := Entry{TS: base.Add(2 * time.Second), Text: "", Service: "worker", ServiceID: "s2", Project: "shop", ProjectID: "p1",
		Environment: "prod", EnvironmentID: "e1", Server: "srv-1", Level: "nonsense"}
	a.Text = "hello"

	if err := s.Send(t.Context(), []Entry{a, c, b}); err != nil {
		t.Fatalf("a 2xx with partialSuccess must not be an error: %v", err)
	}
	r := srv.only(t)
	if r.path != "/v1/logs" || r.method != http.MethodPost || r.header.Get("Content-Type") != "application/json" {
		t.Fatalf("request = %s %s %q", r.method, r.path, r.header.Get("Content-Type"))
	}
	obs := ns(observed)
	attrs := func(name string) string {
		return `{"key":"service.name","value":{"stringValue":"` + name + `"}},
			{"key":"service.namespace","value":{"stringValue":"shop"}},
			{"key":"deployment.environment.name","value":{"stringValue":"prod"}},
			{"key":"host.name","value":{"stringValue":"srv-1"}}`
	}
	assertJSON(t, r.body, fmt.Sprintf(`{"resourceLogs":[
	 {"resource":{"attributes":[%s,
	    {"key":"nebulactrl.service.id","value":{"stringValue":"s1"}},
	    {"key":"nebulactrl.project.id","value":{"stringValue":"p1"}},
	    {"key":"nebulactrl.environment.id","value":{"stringValue":"e1"}},
	    {"key":"deployment.environment","value":{"stringValue":"legacy"}},
	    {"key":"k","value":{"stringValue":"v"}}]},
	  "scopeLogs":[{"scope":{"name":"nebulactrl"},"logRecords":[
	    {"timeUnixNano":%q,"observedTimeUnixNano":%q,"severityNumber":9,"severityText":"INFO","body":{"stringValue":"hello"},
	     "attributes":[{"key":"nebulactrl.deployment.id","value":{"stringValue":"dep1"}},
	                   {"key":"nebulactrl.replica","value":{"intValue":"1"}},
	                   {"key":"log.iostream","value":{"stringValue":"runtime"}}]},
	    {"timeUnixNano":%q,"observedTimeUnixNano":%q,"severityNumber":13,"severityText":"WARN","body":{"stringValue":"careful"},
	     "attributes":[{"key":"nebulactrl.deployment.id","value":{"stringValue":"dep1"}},
	                   {"key":"nebulactrl.replica","value":{"intValue":"1"}},
	                   {"key":"log.iostream","value":{"stringValue":"runtime"}}]}
	  ]}]},
	 {"resource":{"attributes":[%s,
	    {"key":"nebulactrl.service.id","value":{"stringValue":"s2"}},
	    {"key":"nebulactrl.project.id","value":{"stringValue":"p1"}},
	    {"key":"nebulactrl.environment.id","value":{"stringValue":"e1"}},
	    {"key":"deployment.environment","value":{"stringValue":"legacy"}},
	    {"key":"k","value":{"stringValue":"v"}}]},
	  "scopeLogs":[{"scope":{"name":"nebulactrl"},"logRecords":[
	    {"timeUnixNano":%q,"observedTimeUnixNano":%q,"severityNumber":0,"body":{"stringValue":""},
	     "attributes":[{"key":"nebulactrl.replica","value":{"intValue":"0"}}]}
	  ]}]}
	]}`, attrs("api"), ns(base), obs, ns(base.Add(time.Second)), obs, attrs("worker"), ns(base.Add(2*time.Second)), obs))
}

func TestOTLPSeverity(t *testing.T) {
	o := &otlpSink{now: time.Now}
	for level, want := range map[string]int{"debug": 5, "INFO": 9, "warning": 13, "error": 17, "": 0, "trace": 0} {
		e := apiEntry()
		e.Level = level
		var req otlpRequest
		body, err := o.encode([]Entry{e}, base)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatal(err)
		}
		if got := req.ResourceLogs[0].ScopeLogs[0].LogRecords[0].SeverityNumber; got != want {
			t.Errorf("level %q: severity %d, want %d", level, got, want)
		}
	}
}

// ---- generic HTTP ----

func TestHTTPJSONArray(t *testing.T) {
	srv := newCaptureServer(t, http.StatusAccepted, "", nil)
	s := mustNew(t, httpDrain(srv.URL+"/intake?x=1", agentprotocol.HTTPDrainConfig{
		ExtraFields: map[string]string{"ddsource": "nebulactrl", "service": "custom-service", "message": "must not win", "timestamp": "no"},
	}), nil)
	a := apiEntry()
	a.TS = time.Date(2026, 1, 1, 14, 0, 0, 500, time.FixedZone("x", 2*3600))
	a.Text = "hello"
	blank := Entry{TS: base, Text: "bare"}
	if err := s.Send(t.Context(), []Entry{a, blank}); err != nil {
		t.Fatal(err)
	}
	r := srv.only(t)
	if r.method != http.MethodPost || r.path != "/intake" || r.rawQuery != "x=1" || r.header.Get("Content-Type") != "application/json" {
		t.Fatalf("request = %s %s?%s %q", r.method, r.path, r.rawQuery, r.header.Get("Content-Type"))
	}
	assertJSON(t, r.body, `[
	 {"timestamp":"2026-01-01T12:00:00.0000005Z","message":"hello","level":"info","stream":"runtime","service":"custom-service","service_id":"s1",
	  "project":"shop","project_id":"p1","environment":"prod","environment_id":"e1","deployment_id":"dep1","replica":1,"server":"srv-1","ddsource":"nebulactrl"},
	 {"timestamp":"2026-01-01T12:00:00Z","message":"bare","replica":0,"ddsource":"nebulactrl","service":"custom-service"}
	]`)
}

func TestHTTPJSONNDJSONPutRenamedFields(t *testing.T) {
	srv := newCaptureServer(t, http.StatusOK, "", nil)
	s := mustNew(t, httpDrain(srv.URL, agentprotocol.HTTPDrainConfig{
		Method: "put", Format: agentprotocol.HTTPFormatNDJSON, TimestampField: "@timestamp", MessageField: "msg",
	}), nil)
	a, b := apiEntry(), apiEntry()
	a.Text, b.Text = "one", "two"
	b.TS = base.Add(time.Second)
	if err := s.Send(t.Context(), []Entry{a, b}); err != nil {
		t.Fatal(err)
	}
	r := srv.only(t)
	if r.method != http.MethodPut || r.header.Get("Content-Type") != "application/x-ndjson" {
		t.Fatalf("request = %s %q", r.method, r.header.Get("Content-Type"))
	}
	if !bytes.HasSuffix(r.body, []byte("}\n")) {
		t.Fatalf("ndjson must end with a newline: %q", r.body)
	}
	lines := strings.Split(strings.TrimSuffix(string(r.body), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines: %q", len(lines), r.body)
	}
	for i, want := range []struct{ ts, msg string }{{"2026-01-01T12:00:00Z", "one"}, {"2026-01-01T12:00:01Z", "two"}} {
		var m map[string]any
		if err := json.Unmarshal([]byte(lines[i]), &m); err != nil {
			t.Fatal(err)
		}
		if m["@timestamp"] != want.ts || m["msg"] != want.msg || m["message"] != nil || m["timestamp"] != nil {
			t.Fatalf("line %d = %v", i, m)
		}
	}
}

// ---- error classification ----

func TestHTTPErrorClassification(t *testing.T) {
	tests := []struct {
		status    int
		permanent bool
	}{
		{400, true}, {404, true}, {413, true}, {422, true}, {409, true},
		{401, false}, {403, false}, {408, false}, {425, false}, {429, false},
		{500, false}, {502, false}, {503, false}, {504, false},
		{301, false},
	}
	for _, tc := range tests {
		t.Run(strconv.Itoa(tc.status), func(t *testing.T) {
			srv := newCaptureServer(t, tc.status, "rejected: bad batch\n", nil)
			s := mustNew(t, lokiDrain(srv.URL, agentprotocol.LokiDrainConfig{}), nil)
			err := s.Send(t.Context(), []Entry{apiEntry()})
			se := sendErr(t, err)
			if se.StatusCode != tc.status || se.Permanent != tc.permanent {
				t.Fatalf("SendError = status %d permanent %v, want %d %v", se.StatusCode, se.Permanent, tc.status, tc.permanent)
			}
			if !strings.Contains(err.Error(), strconv.Itoa(tc.status)) || !strings.Contains(err.Error(), "rejected: bad batch") {
				t.Fatalf("message lacks status/body: %q", err)
			}
		})
	}
}

func TestRetryAfter(t *testing.T) {
	date := time.Now().Add(90 * time.Second).UTC().Format(http.TimeFormat)
	for _, tc := range []struct {
		status int
		header string
		min    time.Duration
		max    time.Duration
	}{
		{429, "7", 7 * time.Second, 7 * time.Second},
		{503, date, 80 * time.Second, 91 * time.Second},
		{429, "garbage", 0, 0},
		{500, "", 0, 0},
		{429, "-5", 0, 0},
	} {
		srv := newCaptureServer(t, tc.status, "", map[string]string{"Retry-After": tc.header})
		s := mustNew(t, otlpDrain(srv.URL, agentprotocol.OTLPDrainConfig{}), nil)
		se := sendErr(t, s.Send(t.Context(), []Entry{apiEntry()}))
		if se.Permanent || se.RetryAfter < tc.min || se.RetryAfter > tc.max {
			t.Errorf("Retry-After %q: permanent=%v RetryAfter=%v, want [%v,%v]", tc.header, se.Permanent, se.RetryAfter, tc.min, tc.max)
		}
	}
}

func TestNetworkErrorIsRetryableAndRedacted(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL + "/push?api_key=querysecret99"
	srv.Close() // nothing listens any more
	s := mustNew(t, httpDrain(url, agentprotocol.HTTPDrainConfig{}), map[string]string{"token": "tokensecret99"})
	err := s.Send(t.Context(), []Entry{apiEntry()})
	se := sendErr(t, err)
	if se.Permanent || se.StatusCode != 0 {
		t.Fatalf("network error = %+v", se)
	}
	if strings.Contains(err.Error(), "querysecret99") || strings.Contains(err.Error(), "tokensecret99") {
		t.Fatalf("secret leaked: %q", err)
	}
	if !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("message lost the cause: %q", err)
	}
}

func TestContextCancelKeepsCause(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-release }))
	defer srv.Close()
	defer close(release)
	s := mustNew(t, lokiDrain(srv.URL, agentprotocol.LokiDrainConfig{}), nil)
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	err := s.Send(ctx, []Entry{apiEntry()})
	if se := sendErr(t, err); se.Permanent {
		t.Fatal("a cancelled send is not permanent")
	}
	if !errors.Is(err, ctx.Err()) {
		t.Fatalf("errors.Is(err, ctx.Err()) = false for %v", err)
	}
}

func TestStatusBodyIsRedacted(t *testing.T) {
	srv := newCaptureServer(t, http.StatusUnauthorized, `invalid token tok-abcdef for user "alice" (hdr key-123456) see https://x.example/?token=abc`, nil)
	secrets := map[string]string{"token": "tok-abcdef", "headers": `{"X-Api-Key":"key-123456"}`, "username": "alice"}
	s := mustNew(t, lokiDrain(srv.URL, agentprotocol.LokiDrainConfig{}), secrets)
	err := s.Send(t.Context(), []Entry{apiEntry()})
	se := sendErr(t, err)
	if se.Permanent || se.StatusCode != 401 {
		t.Fatalf("401 must be retryable: %+v", se)
	}
	for _, leak := range []string{"tok-abcdef", "key-123456", "token=abc"} {
		if strings.Contains(err.Error(), leak) {
			t.Fatalf("%q leaked: %q", leak, err)
		}
	}
	if !strings.Contains(err.Error(), "••••••") {
		t.Fatalf("expected a mask: %q", err)
	}
}

func TestErrorBodyLimit(t *testing.T) {
	srv := newCaptureServer(t, http.StatusBadRequest, strings.Repeat("x", 100000), nil)
	s := mustNew(t, lokiDrain(srv.URL, agentprotocol.LokiDrainConfig{}), nil)
	err := s.Send(t.Context(), []Entry{apiEntry()})
	if n := len(err.Error()); n > 4200 {
		t.Fatalf("error message is %d bytes", n)
	}
}

// ---- redirects ----

func TestRedirectToOtherHostDropsCredentials(t *testing.T) {
	target := newCaptureServer(t, http.StatusNoContent, "", nil)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/final", http.StatusTemporaryRedirect)
	}))
	defer origin.Close()
	s := mustNew(t, lokiDrain(origin.URL, agentprotocol.LokiDrainConfig{TenantID: "t"}),
		map[string]string{"token": "tok-abcdef", "headers": `{"X-Api-Key":"key-123456"}`})
	if err := s.Send(t.Context(), []Entry{apiEntry()}); err != nil {
		t.Fatal(err)
	}
	r := target.only(t)
	if r.path != "/final" || len(r.body) == 0 {
		t.Fatalf("redirect not followed with the body: %s %q", r.path, r.body)
	}
	for _, h := range []string{"Authorization", "X-Api-Key", "X-Scope-Orgid"} {
		if v := r.header.Get(h); v != "" {
			t.Fatalf("%s leaked to the other host: %q", h, v)
		}
	}
	if r.header.Get("Content-Type") != "application/json" || r.header.Get("User-Agent") != "nebulactrl-agent" {
		t.Fatalf("lost body headers: %v", r.header)
	}
}

func TestRedirectSameHostKeepsCredentialsAndLoopStops(t *testing.T) {
	var hits int
	var mu sync.Mutex
	var auth string
	mux := http.NewServeMux()
	mux.HandleFunc("/a", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/b", http.StatusTemporaryRedirect) })
	mux.HandleFunc("/b", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		auth = r.Header.Get("Authorization")
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/loop", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits++
		mu.Unlock()
		http.Redirect(w, r, "/loop", http.StatusTemporaryRedirect)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	s := mustNew(t, lokiDrain(srv.URL+"/a", agentprotocol.LokiDrainConfig{}), map[string]string{"token": "tok-abcdef"})
	if err := s.Send(t.Context(), []Entry{apiEntry()}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	if auth != "Bearer tok-abcdef" {
		t.Fatalf("same-host redirect lost Authorization: %q", auth)
	}
	mu.Unlock()

	s2 := mustNew(t, lokiDrain(srv.URL+"/loop", agentprotocol.LokiDrainConfig{}), nil)
	err := s2.Send(t.Context(), []Entry{apiEntry()})
	if se := sendErr(t, err); se.Permanent {
		t.Fatal("redirect loop is not permanent")
	}
	mu.Lock()
	defer mu.Unlock()
	if hits != 4 { // the original request plus three redirects
		t.Fatalf("loop endpoint hit %d times, want 4", hits)
	}
}

// ---- Validate / New ----

func TestValidate(t *testing.T) {
	okSyslog := agentprotocol.SyslogDrainConfig{Address: "logs.example.com:514", Protocol: agentprotocol.SyslogTCP}
	with := func(f func(c *agentprotocol.SyslogDrainConfig)) agentprotocol.SyslogDrainConfig {
		c := okSyslog
		f(&c)
		return c
	}
	tests := []struct {
		name  string
		drain agentprotocol.LogDrain
		ok    bool
	}{
		{"loki ok", lokiDrain("https://loki.example.com/loki/api/v1/push", agentprotocol.LokiDrainConfig{}), true},
		{"loki http ok", lokiDrain("http://loki:3100/loki/api/v1/push?x=1", agentprotocol.LokiDrainConfig{}), true},
		{"loki ftp", lokiDrain("ftp://loki.example.com/push", agentprotocol.LokiDrainConfig{}), false},
		{"loki no scheme", lokiDrain("loki.example.com/push", agentprotocol.LokiDrainConfig{}), false},
		{"loki userinfo", lokiDrain("https://user:pw@loki.example.com/push", agentprotocol.LokiDrainConfig{}), false},
		{"loki user only", lokiDrain("https://user@loki.example.com/push", agentprotocol.LokiDrainConfig{}), false},
		{"loki no host", lokiDrain("https:///push", agentprotocol.LokiDrainConfig{}), false},
		{"loki port only host", lokiDrain("http://:3100/push", agentprotocol.LokiDrainConfig{}), false},
		{"loki empty", lokiDrain("", agentprotocol.LokiDrainConfig{}), false},
		{"loki garbage", lokiDrain("http://[::1", agentprotocol.LokiDrainConfig{}), false},
		{"loki missing config", agentprotocol.LogDrain{Kind: agentprotocol.DrainLoki}, false},
		{"otlp ok", otlpDrain("https://otel.example.com/v1/logs", agentprotocol.OTLPDrainConfig{}), true},
		{"otlp ws", otlpDrain("ws://otel.example.com/v1/logs", agentprotocol.OTLPDrainConfig{}), false},
		{"otlp missing config", agentprotocol.LogDrain{Kind: agentprotocol.DrainOTLP}, false},
		{"http ok", httpDrain("https://in.example.com/x", agentprotocol.HTTPDrainConfig{Method: "PUT", Format: "ndjson"}), true},
		{"http bad method", httpDrain("https://in.example.com/x", agentprotocol.HTTPDrainConfig{Method: "DELETE"}), false},
		{"http bad format", httpDrain("https://in.example.com/x", agentprotocol.HTTPDrainConfig{Format: "csv"}), false},
		{"http userinfo", httpDrain("https://a:b@in.example.com/x", agentprotocol.HTTPDrainConfig{}), false},
		{"http missing config", agentprotocol.LogDrain{Kind: agentprotocol.DrainHTTP}, false},
		{"syslog ok", syslogDrain(okSyslog), true},
		{"syslog ipv6", syslogDrain(with(func(c *agentprotocol.SyslogDrainConfig) { c.Address = "[::1]:6514" })), true},
		{"syslog udp", syslogDrain(with(func(c *agentprotocol.SyslogDrainConfig) { c.Protocol = "udp" })), true},
		{"syslog tls facility 23", syslogDrain(with(func(c *agentprotocol.SyslogDrainConfig) { c.Protocol = "tcp+tls"; c.Facility = 23 })), true},
		{"syslog bad protocol", syslogDrain(with(func(c *agentprotocol.SyslogDrainConfig) { c.Protocol = "sctp" })), false},
		{"syslog empty protocol", syslogDrain(with(func(c *agentprotocol.SyslogDrainConfig) { c.Protocol = "" })), false},
		{"syslog facility 24", syslogDrain(with(func(c *agentprotocol.SyslogDrainConfig) { c.Facility = 24 })), false},
		{"syslog facility -1", syslogDrain(with(func(c *agentprotocol.SyslogDrainConfig) { c.Facility = -1 })), false},
		{"syslog no port", syslogDrain(with(func(c *agentprotocol.SyslogDrainConfig) { c.Address = "logs.example.com" })), false},
		{"syslog no host", syslogDrain(with(func(c *agentprotocol.SyslogDrainConfig) { c.Address = ":514" })), false},
		{"syslog bad port", syslogDrain(with(func(c *agentprotocol.SyslogDrainConfig) { c.Address = "h:99999" })), false},
		{"syslog non-numeric port", syslogDrain(with(func(c *agentprotocol.SyslogDrainConfig) { c.Address = "h:syslog" })), false},
		{"syslog missing config", agentprotocol.LogDrain{Kind: agentprotocol.DrainSyslog}, false},
		{"unknown kind", agentprotocol.LogDrain{Kind: "carrier-pigeon"}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			verr := Validate(tc.drain)
			s, nerr := New(tc.drain, nil)
			if s != nil {
				_ = s.Close()
			}
			if (verr == nil) != tc.ok || (nerr == nil) != tc.ok {
				t.Fatalf("Validate err = %v, New err = %v, want ok=%v", verr, nerr, tc.ok)
			}
			if tc.drain.Kind != "" && verr != nil && nerr != nil && verr.Error() != nerr.Error() {
				t.Fatalf("Validate (%v) and New (%v) disagree", verr, nerr)
			}
		})
	}
}

func TestValidateErrorDoesNotEchoURL(t *testing.T) {
	for _, u := range []string{"https://user:hunter2hunter2@h.example.com/x", "ftp://h/x?token=hunter2hunter2", "http://[::1?token=hunter2hunter2"} {
		err := Validate(lokiDrain(u, agentprotocol.LokiDrainConfig{}))
		if err == nil || strings.Contains(err.Error(), "hunter2") {
			t.Fatalf("Validate(%q) = %v", u, err)
		}
	}
}

// ---- Redact / TestEntry ----

func TestRedact(t *testing.T) {
	secrets := map[string]string{
		"token":    "tok-abcdef",
		"password": "pw12",
		"username": "alice1",
		"headers":  `{"X-Api-Key":"key-123456","Short":"abc"}`,
	}
	tests := []struct{ in, want string }{
		{`Post "https://user:pass@host.example.com:3100/loki/api/v1/push?api_key=abc&x=1": dial tcp 1.2.3.4:3100: refused`,
			`Post "https://host.example.com:3100/…": dial tcp 1.2.3.4:3100: refused`},
		{"see http://h/p#frag and https://u@h2/q?z=1 done", "see http://h/… and https://h2/… done"},
		{"hook https://hooks.example.com/services/T000/B000/XXXXXXXX failed, root https://h3/?k=1", "hook https://hooks.example.com/… failed, root https://h3/"},
		{"Bearer tok-abcdef used", "Bearer •••••• used"},
		{"key key-123456 and tok-abcdef twice tok-abcdef", "key •••••• and •••••• twice ••••••"},
		{"password pw12 is short and abc too", "password pw12 is short and abc too"},
		{"alice1 logged in", "•••••• logged in"},
		{"Basic " + b64("alice1:pw12"), "Basic ••••••"},
		{"nothing to hide", "nothing to hide"},
	}
	for _, tc := range tests {
		if got := Redact(tc.in, secrets); got != tc.want {
			t.Errorf("Redact(%q)\n got %q\nwant %q", tc.in, got, tc.want)
		}
	}
	if got := Redact("x https://h/p?q=1", nil); got != "x https://h/…" {
		t.Errorf("nil secrets: %q", got)
	}
	// The raw headers JSON is masked as a whole, longest secret first.
	if got := Redact(`got {"X-Api-Key":"key-123456","Short":"abc"}`, secrets); strings.Contains(got, "key-123456") {
		t.Errorf("headers secret leaked: %q", got)
	}
}

func TestTestEntry(t *testing.T) {
	e := TestEntry("My drain", "srv-1")
	if e.Server != "srv-1" || !strings.Contains(e.Text, "My drain") || e.Level != "info" || e.Stream != "runtime" || e.TS.IsZero() {
		t.Fatalf("TestEntry = %+v", e)
	}
}

func TestEmptyBatchIsNoop(t *testing.T) {
	srv := newCaptureServer(t, http.StatusOK, "", nil)
	for _, d := range []agentprotocol.LogDrain{
		lokiDrain(srv.URL, agentprotocol.LokiDrainConfig{}),
		otlpDrain(srv.URL, agentprotocol.OTLPDrainConfig{}),
		httpDrain(srv.URL, agentprotocol.HTTPDrainConfig{}),
		syslogDrain(agentprotocol.SyslogDrainConfig{Address: "127.0.0.1:1", Protocol: "tcp"}),
	} {
		if err := mustNew(t, d, nil).Send(t.Context(), nil); err != nil {
			t.Fatalf("%s: %v", d.Kind, err)
		}
	}
	if n := len(srv.requests()); n != 0 {
		t.Fatalf("%d requests for empty batches", n)
	}
}
