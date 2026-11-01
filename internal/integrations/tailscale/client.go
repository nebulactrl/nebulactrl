// Package tailscale is the control-plane client of the Tailscale API and the
// workspace integration around it: OAuth client credentials (sealed at rest,
// never returned), token exchange, device list, auth-key minting, device and key
// removal and a connection test. It is reused by per-server tailnet egress
// (internal/proxyserver) and per-service tailnets.
package tailscale

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/clientcredentials"
)

// DefaultBaseURL is the Tailscale control API.
const DefaultBaseURL = "https://api.tailscale.com"

// requestTimeout bounds every call to the Tailscale API (token exchange included).
const requestTimeout = 15 * time.Second

// maxResponseBytes bounds how much of an API response is read.
const maxResponseBytes = 4 << 20

// Errors callers branch on. They never contain credentials.
var (
	// ErrNotFound is returned for a 404 (device or key already gone).
	ErrNotFound = errors.New("tailscale: not found")
	// ErrAuth is returned when the credentials are rejected or lack a scope.
	ErrAuth = errors.New("tailscale: credentials rejected or missing a scope")
)

// APIError is a non-2xx answer of the Tailscale API.
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("tailscale API answered %d", e.Status)
	}
	return fmt.Sprintf("tailscale API answered %d: %s", e.Status, e.Message)
}

// Is maps statuses onto the sentinel errors.
func (e *APIError) Is(target error) bool {
	switch target {
	case ErrNotFound:
		return e.Status == http.StatusNotFound
	case ErrAuth:
		return e.Status == http.StatusUnauthorized || e.Status == http.StatusForbidden
	}
	return false
}

// Retryable reports whether trying again later may succeed.
func (e *APIError) Retryable() bool {
	return e.Status == http.StatusTooManyRequests || e.Status >= 500
}

// Config configures a Client. BaseURL defaults to DefaultBaseURL and is
// overridable for tests.
type Config struct {
	BaseURL      string
	Tailnet      string // "-" (default) = the tailnet of the OAuth client
	ClientID     string
	ClientSecret string
	HTTPClient   *http.Client
}

// Client talks to the Tailscale API with an OAuth access token that is cached
// until shortly before it expires.
type Client struct {
	base    string
	tailnet string
	hc      *http.Client
	ts      oauth2.TokenSource
	// cc is the credential of ts, kept to make a token exchange that bypasses
	// the cached source (CheckToken).
	cc clientcredentials.Config
}

// Token sources are cached per credential so a Client created per request does
// not exchange a token every time. The map is bounded: when it grows past
// maxCachedSources it is reset (a re-exchange is cheap).
const maxCachedSources = 64

var (
	sourcesMu sync.Mutex
	sources   = map[string]oauth2.TokenSource{}
)

func sourceKey(base string, hc *http.Client, id, secret string) string {
	h := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%p\x00%s\x00%s", base, hc, id, secret)))
	return hex.EncodeToString(h[:])
}

// ForgetCredentials drops the cached token of a credential (after rotation or removal).
func ForgetCredentials(c Config) {
	if c.HTTPClient == nil {
		c.HTTPClient = defaultHTTP
	}
	c = c.withDefaults()
	sourcesMu.Lock()
	delete(sources, sourceKey(c.BaseURL, c.HTTPClient, c.ClientID, c.ClientSecret))
	sourcesMu.Unlock()
}

func (c Config) withDefaults() Config {
	if c.BaseURL == "" {
		c.BaseURL = DefaultBaseURL
	}
	c.BaseURL = strings.TrimRight(c.BaseURL, "/")
	if c.Tailnet == "" {
		c.Tailnet = "-"
	}
	if c.HTTPClient == nil {
		c.HTTPClient = &http.Client{Timeout: requestTimeout}
	}
	return c
}

// defaultHTTP is shared so cached token sources keyed by it stay valid.
var defaultHTTP = &http.Client{Timeout: requestTimeout}

// New returns a Client. It performs no I/O.
func New(cfg Config) *Client {
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = defaultHTTP
	}
	cfg = cfg.withDefaults()
	key := sourceKey(cfg.BaseURL, cfg.HTTPClient, cfg.ClientID, cfg.ClientSecret)
	sourcesMu.Lock()
	ts, ok := sources[key]
	if !ok {
		if len(sources) >= maxCachedSources {
			sources = map[string]oauth2.TokenSource{}
		}
		cc := clientcredentials.Config{
			ClientID: cfg.ClientID, ClientSecret: cfg.ClientSecret,
			TokenURL: cfg.BaseURL + "/api/v2/oauth/token", AuthStyle: oauth2.AuthStyleInParams,
		}
		// The token source outlives the request that created it, so it gets its own
		// context carrying the (timeout-bound) HTTP client.
		ts = cc.TokenSource(context.WithValue(context.Background(), oauth2.HTTPClient, cfg.HTTPClient))
		sources[key] = ts
	}
	sourcesMu.Unlock()
	return &Client{base: cfg.BaseURL, tailnet: cfg.Tailnet, hc: cfg.HTTPClient, ts: ts, cc: clientcredentials.Config{
		ClientID: cfg.ClientID, ClientSecret: cfg.ClientSecret,
		TokenURL: cfg.BaseURL + "/api/v2/oauth/token", AuthStyle: oauth2.AuthStyleInParams,
	}}
}

// token returns a valid access token, mapping exchange failures to errors that
// carry no secret material.
func (c *Client) token(ctx context.Context) (string, error) {
	return exchange(ctx, c.ts)
}

// exchange takes a token from ts, mapping failures to errors that carry no
// secret material.
func exchange(ctx context.Context, ts oauth2.TokenSource) (string, error) {
	type result struct {
		tok *oauth2.Token
		err error
	}
	ch := make(chan result, 1)
	go func() {
		t, err := ts.Token()
		ch <- result{t, err}
	}()
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case r := <-ch:
		if r.err != nil {
			var re *oauth2.RetrieveError
			if errors.As(r.err, &re) && re.Response != nil {
				st := re.Response.StatusCode
				if st == http.StatusBadRequest || st == http.StatusUnauthorized || st == http.StatusForbidden {
					return "", fmt.Errorf("%w: token exchange refused (%s)", ErrAuth, safeToken(re.ErrorCode))
				}
				return "", &APIError{Status: st, Message: "token exchange failed"}
			}
			return "", fmt.Errorf("tailscale token exchange failed: %s", redactURLErr(r.err))
		}
		return r.tok.AccessToken, nil
	}
}

func safeToken(s string) string {
	if s == "" {
		return "invalid client"
	}
	if len(s) > 60 {
		s = s[:60]
	}
	return s
}

// redactURLErr keeps the transport reason ("connection refused", "timeout")
// without the request URL or body.
func redactURLErr(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err.Error()
	}
	return "network error"
}

func (c *Client) do(ctx context.Context, method, path string, query url.Values, in, out any) error {
	tok, err := c.token(ctx)
	if err != nil {
		return err
	}
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	u := c.base + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return fmt.Errorf("tailscale request failed: %s", redactURLErr(err))
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return fmt.Errorf("tailscale response: %s", redactURLErr(err))
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		var e struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(raw, &e)
		msg := e.Message
		if len(msg) > 300 {
			msg = msg[:300]
		}
		return &APIError{Status: resp.StatusCode, Message: msg}
	}
	if out == nil || len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("tailscale response is not valid JSON: %w", err)
	}
	return nil
}

func (c *Client) tailnetPath(rest string) string {
	return "/api/v2/tailnet/" + url.PathEscape(c.tailnet) + rest
}

// TailscaleDevice is a machine of the tailnet.
type TailscaleDevice struct {
	ID        string     `json:"id" doc:"Stable node id (use it to delete the device)"`
	Name      string     `json:"name" doc:"Fully qualified MagicDNS name"`
	Hostname  string     `json:"hostname"`
	Addresses []string   `json:"addresses" doc:"Tailnet IP addresses (100.x and fd7a:...)"`
	Tags      []string   `json:"tags"`
	OS        string     `json:"os"`
	Online    bool       `json:"online"`
	LastSeen  *time.Time `json:"lastSeen"`
}

// ListDevices returns every device of the tailnet.
func (c *Client) ListDevices(ctx context.Context) ([]TailscaleDevice, error) {
	var raw struct {
		Devices []struct {
			ID                 string     `json:"id"`
			NodeID             string     `json:"nodeId"`
			Name               string     `json:"name"`
			Hostname           string     `json:"hostname"`
			Addresses          []string   `json:"addresses"`
			Tags               []string   `json:"tags"`
			OS                 string     `json:"os"`
			ConnectedToControl bool       `json:"connectedToControl"`
			LastSeen           *time.Time `json:"lastSeen"`
		} `json:"devices"`
	}
	if err := c.do(ctx, http.MethodGet, c.tailnetPath("/devices"), url.Values{"fields": {"all"}}, nil, &raw); err != nil {
		return nil, err
	}
	out := make([]TailscaleDevice, 0, len(raw.Devices))
	for _, d := range raw.Devices {
		id := d.NodeID
		if id == "" {
			id = d.ID
		}
		out = append(out, TailscaleDevice{
			ID: id, Name: strings.TrimSuffix(d.Name, "."), Hostname: d.Hostname, Addresses: nonNil(d.Addresses),
			Tags: nonNil(d.Tags), OS: d.OS, Online: d.ConnectedToControl, LastSeen: d.LastSeen,
		})
	}
	return out, nil
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// AuthKey is a freshly minted key. Key is the secret and is only available now.
type AuthKey struct {
	ID      string
	Key     string
	Expires time.Time
}

type createKeyRequest struct {
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
	Description   string `json:"description,omitempty"`
}

const maxKeyDescription = 50

// keyDescription makes a description the Tailscale API accepts: it answers
// 400 "description had invalid characters" for anything but letters, digits
// and spaces (a service's "project/env/service" was refused). Every other
// character becomes a space, runs collapse, and it is cut at 50 characters.
func keyDescription(s string) string {
	var b strings.Builder
	space := false
	for _, r := range s {
		ok := r == ' ' || (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
		if !ok || r == ' ' {
			space = b.Len() > 0
			continue
		}
		if space {
			b.WriteByte(' ')
			space = false
		}
		b.WriteRune(r)
	}
	out := b.String()
	if len(out) > maxKeyDescription {
		out = strings.TrimRight(out[:maxKeyDescription], " ")
	}
	return out
}

// MintAuthKey creates a single-use, pre-authorized, non-ephemeral, tagged auth key.
func (c *Client) MintAuthKey(ctx context.Context, tags []string, expirySeconds int, description string) (AuthKey, error) {
	if len(tags) == 0 {
		return AuthKey{}, errors.New("tailscale: an auth key needs at least one tag")
	}
	if expirySeconds < 1 {
		return AuthKey{}, errors.New("tailscale: expirySeconds must be positive")
	}
	var req createKeyRequest
	req.Capabilities.Devices.Create.Preauthorized = true
	req.Capabilities.Devices.Create.Tags = tags
	req.ExpirySeconds = expirySeconds
	req.Description = keyDescription(description)
	var resp struct {
		ID      string    `json:"id"`
		Key     string    `json:"key"`
		Expires time.Time `json:"expires"`
	}
	if err := c.do(ctx, http.MethodPost, c.tailnetPath("/keys"), nil, req, &resp); err != nil {
		return AuthKey{}, err
	}
	if resp.Key == "" {
		return AuthKey{}, errors.New("tailscale: the API returned no key")
	}
	return AuthKey{ID: resp.ID, Key: resp.Key, Expires: resp.Expires}, nil
}

// DeleteKey revokes an auth key. A key that is already gone is not an error.
func (c *Client) DeleteKey(ctx context.Context, keyID string) error {
	err := c.do(ctx, http.MethodDelete, c.tailnetPath("/keys/"+url.PathEscape(keyID)), nil, nil, nil)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	return err
}

// DeleteDevice removes a device from the tailnet. It returns ErrNotFound when
// the device does not exist (callers treat that as done).
func (c *Client) DeleteDevice(ctx context.Context, deviceID string) error {
	if deviceID == "" {
		return errors.New("tailscale: device id required")
	}
	return c.do(ctx, http.MethodDelete, "/api/v2/device/"+url.PathEscape(deviceID), nil, nil, nil)
}

// TailscaleCheck is one line of a connection test.
type TailscaleCheck struct {
	ID      string `json:"id"`
	OK      bool   `json:"ok"`
	Skipped bool   `json:"skipped,omitempty" doc:"The check could not run (optional scope missing)"`
	Message string `json:"message"`
}

// TailscaleTestRun is the outcome of Test.
type TailscaleTestRun struct {
	Checks       []TailscaleCheck
	OK           bool
	HTTPSEnabled *bool
}

// Test verifies the credentials without leaving residue: it exchanges a token,
// lists devices, mints a 60 second single-use key with tags and revokes it, and
// reads the tailnet settings when the optional scope allows.
func (c *Client) Test(ctx context.Context, tags []string) TailscaleTestRun {
	var res TailscaleTestRun
	add := func(ch TailscaleCheck) { res.Checks = append(res.Checks, ch) }

	if _, err := c.token(ctx); err != nil {
		add(TailscaleCheck{ID: "credentials", Message: err.Error()})
		return res
	}
	add(TailscaleCheck{ID: "credentials", OK: true, Message: "The OAuth client was accepted."})

	devs, err := c.ListDevices(ctx)
	if err != nil {
		add(TailscaleCheck{ID: "devices", Message: fmt.Sprintf("Could not list devices (needs the devices:core scope): %v", err)})
	} else {
		add(TailscaleCheck{ID: "devices", OK: true, Message: fmt.Sprintf("Found %d devices.", len(devs))})
	}

	key, err := c.MintAuthKey(ctx, tags, 60, "nebulactrl connection test")
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.Status == http.StatusBadRequest && strings.Contains(strings.ToLower(apiErr.Message), "expir") {
		key, err = c.MintAuthKey(ctx, tags, 86400, "nebulactrl connection test")
	}
	if err != nil {
		add(TailscaleCheck{ID: "auth_keys", Message: fmt.Sprintf("Could not create an auth key with tags %s (needs the auth_keys scope and tag ownership): %v", strings.Join(tags, ", "), err)})
	} else {
		if derr := c.DeleteKey(ctx, key.ID); derr != nil {
			add(TailscaleCheck{ID: "auth_keys", OK: true, Message: fmt.Sprintf("Created an auth key, but could not revoke the test key (it expires on its own): %v", derr)})
		} else {
			add(TailscaleCheck{ID: "auth_keys", OK: true, Message: fmt.Sprintf("Created and revoked a test key with tags %s.", strings.Join(tags, ", "))})
		}
	}

	var settings struct {
		HTTPSEnabled *bool `json:"httpsEnabled"`
	}
	switch err := c.do(ctx, http.MethodGet, c.tailnetPath("/settings"), nil, nil, &settings); {
	case err == nil:
		res.HTTPSEnabled = settings.HTTPSEnabled
		add(TailscaleCheck{ID: "settings", OK: true, Message: "Read the tailnet settings."})
	case errors.Is(err, ErrAuth):
		add(TailscaleCheck{ID: "settings", OK: true, Skipped: true, Message: "Not checked: the optional feature_settings:read scope is missing."})
	default:
		add(TailscaleCheck{ID: "settings", OK: true, Skipped: true, Message: "Not checked: the tailnet settings could not be read."})
	}

	res.OK = true
	for _, ch := range res.Checks {
		if !ch.OK {
			res.OK = false
		}
	}
	return res
}
