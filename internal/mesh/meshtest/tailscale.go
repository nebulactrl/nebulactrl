package meshtest

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tailscale/hujson"
)

// Defaults of the fake Tailscale API.
const (
	TailscaleClientID     = "client-1"
	TailscaleClientSecret = "tskey-client-SUPERSECRET-value"
)

// OAuth scopes of the fake (the ones NebulaCtrl asks for and the policy ones).
const (
	ScopeAuthKeys   = "auth_keys"
	ScopeDevices    = "devices:core"
	ScopePolicyRead = "policy_file:read"
	ScopePolicy     = "policy_file"
)

// TailscaleDevice is a device row of the fake.
type TailscaleDevice struct {
	ID       string
	Hostname string
	Addrs    []string
	Tags     []string
	Online   bool
}

// TailscaleKey is an auth key row of the fake. Key is the secret.
type TailscaleKey struct {
	ID            string
	Key           string
	Tags          []string
	Reusable      bool
	Ephemeral     bool
	Preauthorized bool
	ExpirySeconds int
	Description   string
	Used          bool
	Revoked       bool
	Created       time.Time
}

// TailscaleRequest is one recorded API request (the token exchange is counted
// separately and not recorded).
type TailscaleRequest struct {
	Method, Path, Query string
	Body                string
	IfMatch             string
}

// TailscaleOption configures NewTailscale.
type TailscaleOption func(*Tailscale)

// WithScopes sets the OAuth scopes the token carries (default auth_keys and
// devices:core: the policy endpoints answer 403, like a tailnet whose OAuth
// client was created without policy_file).
func WithScopes(scopes ...string) TailscaleOption {
	return func(t *Tailscale) {
		t.scopes = map[string]bool{}
		for _, s := range scopes {
			t.scopes[s] = true
		}
	}
}

// WithPolicy seeds the tailnet policy document (HuJSON text).
func WithPolicy(doc string) TailscaleOption {
	return func(t *Tailscale) { t.policy = doc; t.etag = etagOf(doc) }
}

// Tailscale is a fake api.tailscale.com. Create it with NewTailscale.
type Tailscale struct {
	// URL is the API base URL (Config.BaseURL).
	URL string

	srv    *httptest.Server
	scopes map[string]bool

	mu        sync.Mutex
	tokenReqs int
	requests  []TailscaleRequest
	devices   []*TailscaleDevice
	keys      []*TailscaleKey
	// editAfterRead is set by ExternalEditAfterNextRead.
	editAfterRead *string
	nextDevice    int
	nextKey       int
	policy        string
	etag          string
	policyPuts    []string
	failKeys      int
	down          bool
}

// NewTailscale starts the fake; it stops when the test ends.
func NewTailscale(t testing.TB, opts ...TailscaleOption) *Tailscale {
	t.Helper()
	f := &Tailscale{
		scopes: map[string]bool{ScopeAuthKeys: true, ScopeDevices: true},
		policy: "{\n\t// the tailnet's own policy\n\t\"acls\": [{\"action\": \"accept\", \"src\": [\"*\"], \"dst\": [\"*:*\"]}],\n}\n",
	}
	f.etag = etagOf(f.policy)
	for _, o := range opts {
		o(f)
	}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	f.URL = f.srv.URL
	t.Cleanup(f.srv.Close)
	return f
}

func etagOf(doc string) string {
	sum := sha256.Sum256([]byte(doc))
	return `"` + hex.EncodeToString(sum[:8]) + `"`
}

// Client returns the HTTP client of the fake's server.
func (f *Tailscale) Client() *http.Client { return f.srv.Client() }

// GrantScope adds an OAuth scope (the user edited the client in the console).
func (f *Tailscale) GrantScope(s string) {
	f.mu.Lock()
	f.scopes[s] = true
	f.mu.Unlock()
}

// SetDown makes every request answer 503 (the coordinator is unreachable).
func (f *Tailscale) SetDown(down bool) {
	f.mu.Lock()
	f.down = down
	f.mu.Unlock()
}

// FailKeys makes the next n key creations answer status 400 (tag not permitted).
func (f *Tailscale) FailKeys(n int) {
	f.mu.Lock()
	f.failKeys = n
	f.mu.Unlock()
}

// AddDevice registers a device as if a tailscaled had joined.
func (f *Tailscale) AddDevice(hostname string, tags ...string) TailscaleDevice {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextDevice++
	d := &TailscaleDevice{
		ID: strconv.Itoa(1000 + f.nextDevice), Hostname: hostname, Online: true,
		Addrs: []string{fmt.Sprintf("100.64.%d.%d", (f.nextDevice-1)/250, (f.nextDevice-1)%250+1), fmt.Sprintf("fd7a:115c:a1e0::%x", f.nextDevice)},
		Tags:  append([]string(nil), tags...),
	}
	f.devices = append(f.devices, d)
	return *d
}

// JoinWithKey registers a device using an auth key: the key must exist, not be
// revoked and (unless reusable) unused. The device takes the key's tags.
func (f *Tailscale) JoinWithKey(key, hostname string) (TailscaleDevice, error) {
	f.mu.Lock()
	for _, k := range f.keys {
		if k.Key != key {
			continue
		}
		if k.Revoked {
			f.mu.Unlock()
			return TailscaleDevice{}, fmt.Errorf("auth key %s is revoked", k.ID)
		}
		if k.Used && !k.Reusable {
			f.mu.Unlock()
			return TailscaleDevice{}, fmt.Errorf("auth key %s was already used", k.ID)
		}
		k.Used = true
		tags := k.Tags
		f.mu.Unlock()
		return f.AddDevice(hostname, tags...), nil
	}
	f.mu.Unlock()
	return TailscaleDevice{}, fmt.Errorf("unknown auth key")
}

// Devices returns a snapshot of the devices.
func (f *Tailscale) Devices() []TailscaleDevice {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]TailscaleDevice, 0, len(f.devices))
	for _, d := range f.devices {
		out = append(out, *d)
	}
	return out
}

// Keys returns a snapshot of every auth key ever created.
func (f *Tailscale) Keys() []TailscaleKey {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]TailscaleKey, 0, len(f.keys))
	for _, k := range f.keys {
		out = append(out, *k)
	}
	return out
}

// Policy returns the stored policy document and its ETag.
func (f *Tailscale) Policy() (doc, etag string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.policy, f.etag
}

// SetPolicyExternally replaces the policy as a human in the admin console
// would (new ETag): the next If-Match write of NebulaCtrl gets 412.
func (f *Tailscale) SetPolicyExternally(doc string) {
	f.mu.Lock()
	f.policy, f.etag = doc, etagOf(doc)
	f.mu.Unlock()
}

// ExternalEditAfterNextRead makes the next GET /acl answer the current policy
// and then replace it with doc (a human saving in the console between
// NebulaCtrl's read and its write): the write that follows gets 412.
func (f *Tailscale) ExternalEditAfterNextRead(doc string) {
	f.mu.Lock()
	f.editAfterRead = &doc
	f.mu.Unlock()
}

// PolicyWrites returns every document accepted by POST /acl, oldest first.
func (f *Tailscale) PolicyWrites() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.policyPuts...)
}

// Requests returns the recorded API requests.
func (f *Tailscale) Requests() []TailscaleRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]TailscaleRequest(nil), f.requests...)
}

// TokenExchanges returns how many OAuth token requests were made.
func (f *Tailscale) TokenExchanges() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.tokenReqs
}

func (f *Tailscale) message(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"message": msg})
}

func (f *Tailscale) handle(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.down {
		f.message(w, http.StatusServiceUnavailable, "unavailable")
		return
	}
	if r.URL.Path == "/api/v2/oauth/token" {
		f.tokenReqs++
		form, _ := url.ParseQuery(string(body))
		if form.Get("grant_type") != "client_credentials" || form.Get("client_id") != TailscaleClientID || form.Get("client_secret") != TailscaleClientSecret {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_client"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"access_token": "tok-abc", "token_type": "Bearer", "expires_in": 3600})
		return
	}
	if r.Header.Get("Authorization") != "Bearer tok-abc" {
		f.message(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	f.requests = append(f.requests, TailscaleRequest{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Body: string(body), IfMatch: r.Header.Get("If-Match")})
	p := r.URL.Path
	switch {
	case r.Method == http.MethodGet && strings.HasSuffix(p, "/devices"):
		f.listDevices(w)
	case r.Method == http.MethodPost && strings.HasSuffix(p, "/keys"):
		f.createKey(w, body)
	case r.Method == http.MethodDelete && strings.Contains(p, "/keys/"):
		id := p[strings.LastIndex(p, "/")+1:]
		for _, k := range f.keys {
			if k.ID == id {
				k.Revoked = true
			}
		}
		w.WriteHeader(http.StatusOK)
	case r.Method == http.MethodDelete && strings.HasPrefix(p, "/api/v2/device/"):
		id := strings.TrimPrefix(p, "/api/v2/device/")
		for i, d := range f.devices {
			if d.ID == id || "n"+d.ID+"CNTRL" == id { // the real API takes the numeric id or the node id
				f.devices = append(f.devices[:i], f.devices[i+1:]...)
				w.WriteHeader(http.StatusOK)
				return
			}
		}
		f.message(w, http.StatusNotFound, "device not found")
	case r.Method == http.MethodGet && strings.HasSuffix(p, "/settings"):
		writeJSON(w, http.StatusOK, map[string]any{"httpsEnabled": true, "devicesApprovalOn": false})
	case strings.HasSuffix(p, "/acl/validate") && r.Method == http.MethodPost:
		f.validate(w, body)
	case strings.HasSuffix(p, "/acl") && r.Method == http.MethodGet:
		f.getACL(w)
	case strings.HasSuffix(p, "/acl") && r.Method == http.MethodPost:
		f.setACL(w, r, body)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (f *Tailscale) listDevices(w http.ResponseWriter) {
	devs := make([]map[string]any, 0, len(f.devices))
	for _, d := range f.devices {
		devs = append(devs, map[string]any{
			"id": d.ID, "nodeId": "n" + d.ID + "CNTRL", "name": d.Hostname + ".tail1.ts.net.", "hostname": d.Hostname,
			"addresses": d.Addrs, "tags": d.Tags, "os": "linux", "connectedToControl": d.Online,
			"lastSeen": time.Now().UTC().Format(time.RFC3339),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"devices": devs})
}

func (f *Tailscale) createKey(w http.ResponseWriter, body []byte) {
	if f.failKeys > 0 {
		f.failKeys--
		f.message(w, http.StatusBadRequest, "requested tags [tag:nope] are invalid or not permitted")
		return
	}
	var req struct {
		Capabilities struct {
			Devices struct {
				Create struct {
					Reusable      bool     `json:"reusable"`
					Ephemeral     bool     `json:"ephemeral"`
					Preauthorized bool     `json:"preauthorized"`
					Tags          []string `json:"tags"`
				} `json:"create"`
			} `json:"devices"`
		} `json:"capabilities"`
		ExpirySeconds int    `json:"expirySeconds"`
		Description   string `json:"description"`
	}
	_ = json.Unmarshal(body, &req)
	c := req.Capabilities.Devices.Create
	f.nextKey++
	k := &TailscaleKey{
		ID: fmt.Sprintf("kKEY%dCNTRL", f.nextKey), Key: fmt.Sprintf("tskey-auth-FRESH-%d-%s", f.nextKey, strings.Repeat("x", 12)),
		Tags: c.Tags, Reusable: c.Reusable, Ephemeral: c.Ephemeral, Preauthorized: c.Preauthorized,
		ExpirySeconds: req.ExpirySeconds, Description: req.Description, Created: time.Now(),
	}
	f.keys = append(f.keys, k)
	writeJSON(w, http.StatusOK, map[string]any{
		"id": k.ID, "key": k.Key, "created": k.Created.UTC().Format(time.RFC3339),
		"expires": k.Created.Add(time.Duration(req.ExpirySeconds) * time.Second).UTC().Format(time.RFC3339),
	})
}

func (f *Tailscale) needPolicyScope(w http.ResponseWriter, write bool) bool {
	if f.scopes[ScopePolicy] || (!write && f.scopes[ScopePolicyRead]) {
		return true
	}
	f.message(w, http.StatusForbidden, "calling actor does not have enough permissions to perform this function")
	return false
}

func (f *Tailscale) getACL(w http.ResponseWriter) {
	if !f.needPolicyScope(w, false) {
		return
	}
	w.Header().Set("Content-Type", "application/hujson")
	w.Header().Set("ETag", f.etag)
	_, _ = w.Write([]byte(f.policy))
	if f.editAfterRead != nil {
		// A human saves right after NebulaCtrl read the policy.
		f.policy, f.etag = *f.editAfterRead, etagOf(*f.editAfterRead)
		f.editAfterRead = nil
	}
}

func (f *Tailscale) validate(w http.ResponseWriter, body []byte) {
	if !f.needPolicyScope(w, false) {
		return
	}
	if err := checkHuJSON(body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{})
}

func (f *Tailscale) setACL(w http.ResponseWriter, r *http.Request, body []byte) {
	if !f.needPolicyScope(w, true) {
		return
	}
	if im := r.Header.Get("If-Match"); im != "" && im != f.etag {
		f.message(w, http.StatusPreconditionFailed, "precondition failed, invalid old hash")
		return
	}
	if err := checkHuJSON(body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return
	}
	f.policy, f.etag = string(body), etagOf(string(body))
	f.policyPuts = append(f.policyPuts, f.policy)
	w.Header().Set("ETag", f.etag)
	w.Header().Set("Content-Type", "application/hujson")
	_, _ = w.Write(body)
}

// checkHuJSON accepts what Tailscale accepts: a HuJSON object (comments and
// trailing commas, any formatting).
func checkHuJSON(doc []byte) error {
	std, err := hujson.Standardize(bytes.Clone(doc))
	if err != nil {
		return fmt.Errorf("invalid policy: %v", err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(std, &m); err != nil {
		return fmt.Errorf("invalid policy: %v", err)
	}
	return nil
}
