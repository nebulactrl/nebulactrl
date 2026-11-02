package docker

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/moby/moby/client"
	"github.com/stretchr/testify/require"
)

// fakeAPIVersion is the Engine API version the fake daemon is addressed with:
// it is new enough for GwPriority and for the 1.52 /system/df layout.
const fakeAPIVersion = "1.52"

// asyncTimeout bounds how long a test waits for something a goroutine of the engine
// does in the background (never used to pace the code under test).
const asyncTimeout = 10 * time.Second

var versionPrefix = regexp.MustCompile(`^/v[0-9]+\.[0-9]+`)

// recordedRequest is one request the fake daemon received.
type recordedRequest struct {
	Method string
	Path   string // without the /vX.YY prefix
	Query  url.Values
	Header http.Header
	Body   []byte
}

// fakeDaemon is an httptest server that plays the Docker Engine API. Routes are
// exact "METHOD /unversioned/path" strings; a HEAD request falls back to the
// GET route.
type fakeDaemon struct {
	t      *testing.T
	srv    *httptest.Server
	routes map[string]http.HandlerFunc // "METHOD /path"; registering again replaces
	done   chan struct{}               // closed on cleanup; blocking handlers must select on it

	mu   sync.Mutex
	reqs []recordedRequest
}

// newFakeDaemon starts a fake daemon and returns it with an SDKEngine speaking
// to it at fakeAPIVersion.
func newFakeDaemon(t *testing.T) (*fakeDaemon, *SDKEngine) {
	return newFakeDaemonAt(t, fakeAPIVersion)
}

func newFakeDaemonAt(t *testing.T, apiVersion string) (*fakeDaemon, *SDKEngine) {
	t.Helper()
	d := &fakeDaemon{t: t, routes: map[string]http.HandlerFunc{}, done: make(chan struct{})}
	d.srv = httptest.NewServer(http.HandlerFunc(d.serve))
	t.Cleanup(func() {
		close(d.done)
		d.srv.CloseClientConnections()
		d.srv.Close()
	})
	cli, err := client.New(client.WithHost("tcp://"+d.srv.Listener.Addr().String()), client.WithAPIVersion(apiVersion))
	require.NoError(t, err)
	t.Cleanup(func() { _ = cli.Close() })
	return d, &SDKEngine{cli: cli}
}

func (d *fakeDaemon) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	r.Body = io.NopCloser(bytes.NewReader(body))
	path := versionPrefix.ReplaceAllString(r.URL.Path, "")
	d.mu.Lock()
	d.reqs = append(d.reqs, recordedRequest{Method: r.Method, Path: path, Query: r.URL.Query(), Header: r.Header.Clone(), Body: body})
	d.mu.Unlock()
	d.mu.Lock()
	h, ok := d.routes[r.Method+" "+path]
	if !ok && r.Method == http.MethodHead {
		h, ok = d.routes["GET "+path]
	}
	d.mu.Unlock()
	if !ok {
		writeError(w, http.StatusNotImplemented, "fake daemon: no route for "+r.Method+" "+path)
		return
	}
	h(w, r)
}

// on registers (or replaces) the handler for "METHOD /path".
func (d *fakeDaemon) on(pattern string, h http.HandlerFunc) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.routes[pattern] = h
}

// onJSON answers pattern with status and the JSON encoding of v.
func (d *fakeDaemon) onJSON(pattern string, status int, v any) {
	d.on(pattern, func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, status, v) })
}

// onError answers pattern with a daemon-style {"message": msg} error.
func (d *fakeDaemon) onError(pattern string, status int, msg string) {
	d.on(pattern, func(w http.ResponseWriter, _ *http.Request) { writeError(w, status, msg) })
}

// onStatus answers pattern with an empty body and status.
func (d *fakeDaemon) onStatus(pattern string, status int) {
	d.on(pattern, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(status) })
}

// requests returns a snapshot of every request received so far.
func (d *fakeDaemon) requests() []recordedRequest {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]recordedRequest(nil), d.reqs...)
}

// find returns the requests matching method and the unversioned path.
func (d *fakeDaemon) find(method, path string) []recordedRequest {
	var out []recordedRequest
	for _, r := range d.requests() {
		if r.Method == method && r.Path == path {
			out = append(out, r)
		}
	}
	return out
}

// only returns the single request matching method and path, failing otherwise.
func (d *fakeDaemon) only(method, path string) recordedRequest {
	d.t.Helper()
	got := d.find(method, path)
	require.Len(d.t, got, 1, "requests %s %s; all: %s", method, path, d.summary())
	return got[0]
}

// calls lists "METHOD /path" of every request in arrival order.
func (d *fakeDaemon) calls() []string {
	var out []string
	for _, r := range d.requests() {
		out = append(out, r.Method+" "+r.Path)
	}
	return out
}

func (d *fakeDaemon) summary() string { return strings.Join(d.calls(), ", ") }

func (r recordedRequest) decode(t *testing.T, into any) {
	t.Helper()
	require.NoError(t, json.Unmarshal(r.Body, into), "request body %s", r.Body)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"message": msg})
}

// hijacked is the server side of an upgraded (attach / exec start) connection.
type hijacked struct {
	net.Conn
	// In reads what the client wrote after the upgrade.
	In *bufio.Reader
}

// hijack upgrades the request like the daemon does for attach and exec start.
// contentType is "application/vnd.docker.raw-stream" for TTY streams and
// "application/vnd.docker.multiplexed-stream" otherwise.
func hijack(t *testing.T, w http.ResponseWriter, contentType string) *hijacked {
	t.Helper()
	conn, brw, err := w.(http.Hijacker).Hijack()
	require.NoError(t, err)
	_, err = brw.WriteString("HTTP/1.1 101 UPGRADED\r\nContent-Type: " + contentType + "\r\nConnection: Upgrade\r\nUpgrade: tcp\r\n\r\n")
	require.NoError(t, err)
	require.NoError(t, brw.Flush())
	return &hijacked{Conn: conn, In: brw.Reader}
}

const (
	rawStream         = "application/vnd.docker.raw-stream"
	multiplexedStream = "application/vnd.docker.multiplexed-stream"
)

// flushHeaders sends the response head so a client blocked in the request
// (container wait) returns, leaving the body open.
func flushHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.(http.Flusher).Flush()
}

// encodeStatHeader is the value of the X-Docker-Container-Path-Stat header:
// the base64 of the path stat's JSON.
func encodeStatHeader(t *testing.T, stat any) string {
	t.Helper()
	b, err := json.Marshal(stat)
	require.NoError(t, err)
	return base64.StdEncoding.EncodeToString(b)
}
