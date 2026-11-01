// Package meshtest holds in-memory stand-ins for the two tailnet control
// planes NebulaCtrl talks to (Headscale's REST API and Tailscale's API v2), for
// control-plane tests of the mesh. Nothing here starts a tailnet: nodes are
// rows. The fakes are stateful (keys are single-use and expire, policy is
// stored, nodes are listed and deleted) and record what they were asked, so a
// test can assert on requests ("no key was minted while the mesh was paused")
// and on stored state ("the policy holds exactly these rules").
//
// The JSON shapes follow Headscale v0.29.6 (grpc-gateway: ids are decimal
// strings) and the Tailscale API v2 as used by internal/integrations/tailscale.
// Wire details the real servers have that matter to callers are reproduced:
// Headscale answers 500 `loading ACL from database: acl policy not found` until
// a policy exists, rejects PUT /policy outside policy.mode database, and its
// /health needs no credentials.
package meshtest

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// HeadscaleAPIKey is the API key the fake accepts unless WithAPIKey is used.
const HeadscaleAPIKey = "hs-apikey-1234567"

// Policy modes of Headscale (policy.mode).
const (
	PolicyModeDatabase = "database"
	PolicyModeFile     = "file"
)

// ErrPolicyDisabled is the message Headscale answers a PUT /policy with when
// policy.mode is file: NebulaCtrl detects "advisory" from it.
const ErrPolicyDisabled = "policy update is disabled: policy.mode is file"

// HeadscaleNode is a node row of the fake.
type HeadscaleNode struct {
	ID     string
	Name   string
	IPs    []string
	Online bool
	Tags   []string
	// KeyID is the pre-auth key the node joined with ("" for JoinNode).
	KeyID string
}

// HeadscaleKey is a pre-auth key row of the fake. Key is the secret: tests
// scan their own output for it.
type HeadscaleKey struct {
	ID        string
	Key       string
	User      string
	Tags      []string
	Reusable  bool
	Ephemeral bool
	Used      bool
	Expired   bool
	Expiry    time.Time
}

// HeadscaleCall is one recorded request.
type HeadscaleCall struct {
	Method, Path string
	Body         string
}

// HeadscaleOption configures NewHeadscale.
type HeadscaleOption func(*Headscale)

// WithAPIKey sets the accepted API key.
func WithAPIKey(k string) HeadscaleOption { return func(h *Headscale) { h.apiKey = k } }

// WithPolicyMode sets policy.mode (default database). In file mode PUT /policy
// fails with ErrPolicyDisabled and GET /policy answers the file's policy.
func WithPolicyMode(m string) HeadscaleOption { return func(h *Headscale) { h.policyMode = m } }

// WithUsers sets the Headscale users (name -> id). The default is {"ops": "7"}.
func WithUsers(users map[string]string) HeadscaleOption {
	return func(h *Headscale) { h.users = users }
}

// Headscale is a fake Headscale server. Create it with NewHeadscale.
type Headscale struct {
	// URL is the base URL (the login server and the REST base).
	URL string

	srv        *httptest.Server
	apiKey     string
	policyMode string
	users      map[string]string

	mu         sync.Mutex
	calls      []HeadscaleCall
	nodes      []*HeadscaleNode
	keys       []*HeadscaleKey
	nextNode   int
	nextKey    int
	policy     string
	hasPolicy  bool
	history    []string
	down       bool
	failNext   map[string]int // "METHOD path" -> status for the next request
	filePolicy string
}

// NewHeadscale starts the fake; it stops when the test ends.
func NewHeadscale(t testing.TB, opts ...HeadscaleOption) *Headscale {
	t.Helper()
	h := &Headscale{
		apiKey: HeadscaleAPIKey, policyMode: PolicyModeDatabase,
		users: map[string]string{"ops": "7"}, failNext: map[string]int{},
		filePolicy: `{"acls":[]}`,
	}
	for _, o := range opts {
		o(h)
	}
	h.srv = httptest.NewServer(http.HandlerFunc(h.handle))
	h.URL = h.srv.URL
	t.Cleanup(h.srv.Close)
	return h
}

// APIKey is the key a client must present.
func (h *Headscale) APIKey() string { return h.apiKey }

// Client returns the HTTP client of the fake's server.
func (h *Headscale) Client() *http.Client { return h.srv.Client() }

// SetPolicyMode changes policy.mode at runtime: a Headscale that is redeployed
// with another config (the platform migration from file to database).
func (h *Headscale) SetPolicyMode(m string) {
	h.mu.Lock()
	h.policyMode = m
	h.mu.Unlock()
}

// PolicyMode returns the current policy.mode.
func (h *Headscale) PolicyMode() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.policyMode
}

// SetDown makes every request (including /health) answer 503: the coordinator
// is unreachable. Established tunnels are not modelled.
func (h *Headscale) SetDown(down bool) {
	h.mu.Lock()
	h.down = down
	h.mu.Unlock()
}

// FailNext makes the next "<METHOD> <path>" request answer status once.
func (h *Headscale) FailNext(method, path string, status int) {
	h.mu.Lock()
	h.failNext[method+" "+path] = status
	h.mu.Unlock()
}

// SetFilePolicy sets the policy GET /policy answers in file mode.
func (h *Headscale) SetFilePolicy(p string) {
	h.mu.Lock()
	h.filePolicy = p
	h.mu.Unlock()
}

// JoinNode registers a node as if a tailscaled had joined (no key involved)
// and returns it. IPs are allocated from 100.64.0.0/10 in order.
func (h *Headscale) JoinNode(name string, tags ...string) HeadscaleNode {
	h.mu.Lock()
	defer h.mu.Unlock()
	return *h.addNodeLocked(name, tags, "")
}

// JoinWithKey registers a node using a pre-auth key like tailscaled does: the
// key must exist, be unexpired and (unless reusable) unused. The node takes the
// key's tags. It returns an error describing the rejection otherwise.
func (h *Headscale) JoinWithKey(key, name string) (HeadscaleNode, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, k := range h.keys {
		if k.Key != key {
			continue
		}
		switch {
		case k.Expired || time.Now().After(k.Expiry):
			return HeadscaleNode{}, fmt.Errorf("pre-auth key %s is expired", k.ID)
		case k.Used && !k.Reusable:
			return HeadscaleNode{}, fmt.Errorf("pre-auth key %s was already used", k.ID)
		}
		k.Used = true
		return *h.addNodeLocked(name, k.Tags, k.ID), nil
	}
	return HeadscaleNode{}, fmt.Errorf("unknown pre-auth key")
}

func (h *Headscale) addNodeLocked(name string, tags []string, keyID string) *HeadscaleNode {
	h.nextNode++
	n := &HeadscaleNode{
		ID: strconv.Itoa(h.nextNode), Name: name, Online: true,
		IPs:   []string{fmt.Sprintf("100.64.%d.%d", (h.nextNode-1)/250, (h.nextNode-1)%250+1), fmt.Sprintf("fd7a:115c:a1e0::%x", h.nextNode)},
		Tags:  append([]string(nil), tags...),
		KeyID: keyID,
	}
	h.nodes = append(h.nodes, n)
	return n
}

// SetOnline flips a node's online flag (false: the node's agent is gone).
func (h *Headscale) SetOnline(id string, online bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, n := range h.nodes {
		if n.ID == id {
			n.Online = online
		}
	}
}

// Nodes returns a snapshot of the nodes.
func (h *Headscale) Nodes() []HeadscaleNode {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]HeadscaleNode, 0, len(h.nodes))
	for _, n := range h.nodes {
		out = append(out, *n)
	}
	return out
}

// Keys returns a snapshot of every pre-auth key ever minted.
func (h *Headscale) Keys() []HeadscaleKey {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]HeadscaleKey, 0, len(h.keys))
	for _, k := range h.keys {
		out = append(out, *k)
	}
	return out
}

// ActiveKeys returns the minted keys that are neither used, expired nor out of time.
func (h *Headscale) ActiveKeys() []HeadscaleKey {
	var out []HeadscaleKey
	for _, k := range h.Keys() {
		if !k.Used && !k.Expired && time.Now().Before(k.Expiry) {
			out = append(out, k)
		}
	}
	return out
}

// Policy returns the stored policy document and whether one was ever written.
func (h *Headscale) Policy() (string, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.policy, h.hasPolicy
}

// PolicyHistory returns every policy accepted by PUT /policy, oldest first.
func (h *Headscale) PolicyHistory() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.history...)
}

// Calls returns the recorded API requests (health checks excluded), oldest first.
func (h *Headscale) Calls() []HeadscaleCall {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]HeadscaleCall(nil), h.calls...)
}

// CallLog is Calls as "METHOD path" strings.
func (h *Headscale) CallLog() []string {
	var out []string
	for _, c := range h.Calls() {
		out = append(out, c.Method+" "+c.Path)
	}
	return out
}

// Count returns how many recorded requests were "<METHOD> <path>".
func (h *Headscale) Count(method, path string) int {
	n := 0
	for _, c := range h.Calls() {
		if c.Method == method && c.Path == path {
			n++
		}
	}
	return n
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func gwError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"code": 2, "message": msg, "details": []any{}})
}

func (h *Headscale) handle(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.down {
		gwError(w, http.StatusServiceUnavailable, "unavailable")
		return
	}
	if r.URL.Path == "/health" {
		writeJSON(w, http.StatusOK, map[string]any{"databaseConnectivity": true})
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+h.apiKey {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"message": "Unauthorized"})
		return
	}
	h.calls = append(h.calls, HeadscaleCall{Method: r.Method, Path: r.URL.Path, Body: string(body)})
	if st, ok := h.failNext[r.Method+" "+r.URL.Path]; ok {
		delete(h.failNext, r.Method+" "+r.URL.Path)
		gwError(w, st, "injected failure")
		return
	}
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/api/v1/user":
		h.listUsers(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/api/v1/preauthkey":
		h.mint(w, body)
	case r.Method == http.MethodPost && r.URL.Path == "/api/v1/preauthkey/expire":
		var in struct {
			ID any `json:"id"`
		}
		_ = json.Unmarshal(body, &in)
		id := fmt.Sprint(in.ID)
		for _, k := range h.keys {
			if k.ID == id {
				k.Expired = true
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{})
	case r.Method == http.MethodGet && r.URL.Path == "/api/v1/node":
		h.listNodes(w)
	case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/api/v1/node/"):
		id := strings.TrimPrefix(r.URL.Path, "/api/v1/node/")
		for i, n := range h.nodes {
			if n.ID == id {
				h.nodes = append(h.nodes[:i], h.nodes[i+1:]...)
				writeJSON(w, http.StatusOK, map[string]any{})
				return
			}
		}
		gwError(w, http.StatusNotFound, "node not found")
	case r.URL.Path == "/api/v1/policy" && r.Method == http.MethodGet:
		h.getPolicy(w)
	case r.URL.Path == "/api/v1/policy" && r.Method == http.MethodPut:
		h.putPolicy(w, body)
	case r.URL.Path == "/api/v1/policy/check" && r.Method == http.MethodPost:
		h.checkPolicy(w, body)
	default:
		writeJSON(w, http.StatusNotFound, map[string]any{"message": "not found"})
	}
}

func (h *Headscale) listUsers(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	users := []map[string]string{}
	for n, id := range h.users {
		if name == "" || name == n {
			users = append(users, map[string]string{"id": id, "name": n})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"users": users})
}

func (h *Headscale) mint(w http.ResponseWriter, body []byte) {
	var in struct {
		User       string   `json:"user"`
		Reusable   bool     `json:"reusable"`
		Ephemeral  bool     `json:"ephemeral"`
		Expiration string   `json:"expiration"`
		ACLTags    []string `json:"aclTags"`
	}
	_ = json.Unmarshal(body, &in)
	exp, err := time.Parse(time.RFC3339, in.Expiration)
	if err != nil {
		exp = time.Now().Add(time.Hour)
	}
	h.nextKey++
	k := &HeadscaleKey{
		ID: strconv.Itoa(h.nextKey), Key: fmt.Sprintf("hskey-MINTED-%d-%s", h.nextKey, strings.Repeat("x", 12)),
		User: in.User, Tags: in.ACLTags, Reusable: in.Reusable, Ephemeral: in.Ephemeral, Expiry: exp,
	}
	h.keys = append(h.keys, k)
	var user any
	if in.User != "" {
		user = map[string]string{"id": in.User}
	}
	writeJSON(w, http.StatusOK, map[string]any{"preAuthKey": map[string]any{
		"id": k.ID, "key": k.Key, "user": user, "reusable": k.Reusable, "ephemeral": k.Ephemeral,
		"used": false, "expiration": exp.UTC().Format(time.RFC3339), "aclTags": k.Tags,
	}})
}

func (h *Headscale) listNodes(w http.ResponseWriter) {
	nodes := make([]map[string]any, 0, len(h.nodes))
	for _, n := range h.nodes {
		nodes = append(nodes, map[string]any{
			"id": n.ID, "name": n.Name, "givenName": n.Name, "ipAddresses": n.IPs,
			"online": n.Online, "forcedTags": n.Tags, "validTags": n.Tags,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"nodes": nodes})
}

func (h *Headscale) getPolicy(w http.ResponseWriter) {
	if h.policyMode == PolicyModeFile {
		writeJSON(w, http.StatusOK, map[string]any{"policy": h.filePolicy, "updatedAt": nil})
		return
	}
	if !h.hasPolicy {
		gwError(w, http.StatusInternalServerError, "loading ACL from database: acl policy not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"policy": h.policy, "updatedAt": time.Now().UTC().Format(time.RFC3339)})
}

// parsePolicy validates a policy body: {"policy": "<json or hujson document>"}.
// A document must be a JSON object once // comments are stripped; "acls" must
// be an array when present.
func parsePolicy(body []byte) (string, error) {
	var in struct {
		Policy string `json:"policy"`
	}
	if err := json.Unmarshal(body, &in); err != nil {
		return "", fmt.Errorf("invalid request: %w", err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal([]byte(stripLineComments(in.Policy)), &doc); err != nil {
		return "", fmt.Errorf("parsing policy: %w", err)
	}
	if raw, ok := doc["acls"]; ok {
		var acls []json.RawMessage
		if err := json.Unmarshal(raw, &acls); err != nil {
			return "", fmt.Errorf("parsing policy: acls must be an array")
		}
	}
	return in.Policy, nil
}

func stripLineComments(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		if idx := strings.Index(l, "//"); idx >= 0 && !strings.Contains(l[:idx], `"`) {
			lines[i] = l[:idx]
		}
	}
	return strings.Join(lines, "\n")
}

func (h *Headscale) putPolicy(w http.ResponseWriter, body []byte) {
	if h.policyMode != PolicyModeDatabase {
		gwError(w, http.StatusInternalServerError, ErrPolicyDisabled)
		return
	}
	p, err := parsePolicy(body)
	if err != nil {
		gwError(w, http.StatusBadRequest, err.Error())
		return
	}
	h.policy, h.hasPolicy = p, true
	h.history = append(h.history, p)
	writeJSON(w, http.StatusOK, map[string]any{"policy": p, "updatedAt": time.Now().UTC().Format(time.RFC3339)})
}

func (h *Headscale) checkPolicy(w http.ResponseWriter, body []byte) {
	if _, err := parsePolicy(body); err != nil {
		gwError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{})
}
