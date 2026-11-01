package tailscale

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Headscale credentials (tailnet_integrations.headscale_credential).
const (
	// HeadscaleAPIKey is a Headscale API key: NebulaCtrl creates a single-use
	// pre-auth key per provisioning and deletes nodes through the REST API.
	HeadscaleAPIKey = "apikey"
	// HeadscalePreauth is a reusable, tagged pre-auth key handed to every
	// sidecar as is. Nodes cannot be removed through the API with it.
	HeadscalePreauth = "preauth"
)

// Providers (tailnet_integrations.provider).
const (
	ProviderTailscaleOAuth = "tailscale_oauth"
	ProviderHeadscale      = "headscale"
)

// HeadscaleClient talks to Headscale's own REST API (/api/v1, Bearer API key).
// It is not the Tailscale /api/v2 surface. The endpoints follow Headscale
// 0.26+ (grpc-gateway: ids are decimal strings); the server publishes its
// OpenAPI document at /swagger.
type HeadscaleClient struct {
	base string
	key  string
	user string
	hc   *http.Client
}

// HeadscaleConfig configures a HeadscaleClient.
type HeadscaleConfig struct {
	// LoginServer is the Headscale URL (also the REST base).
	LoginServer string
	APIKey      string
	// User is the Headscale user that owns minted pre-auth keys. Empty creates
	// tags-only keys (the nodes belong to the special tagged-devices user).
	User       string
	HTTPClient *http.Client
}

// NewHeadscale returns a client; it performs no I/O.
func NewHeadscale(cfg HeadscaleConfig) *HeadscaleClient {
	hc := cfg.HTTPClient
	if hc == nil {
		hc = defaultHTTP
	}
	return &HeadscaleClient{base: strings.TrimRight(cfg.LoginServer, "/"), key: cfg.APIKey, user: cfg.User, hc: hc}
}

// ValidLoginServer checks a Headscale control URL: https (plain http only to a
// loopback address, so the API key never crosses a network unencrypted), a
// host, no credentials, query or fragment.
func ValidLoginServer(raw string) (string, error) {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return "", errors.New("loginServer must be an https:// URL")
	}
	if u.Scheme == "http" && !isLoopbackHost(u.Hostname()) {
		return "", errors.New("loginServer must use https:// (http:// is accepted only for localhost)")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("loginServer must not contain credentials, a query or a fragment")
	}
	return u.Scheme + "://" + u.Host + strings.TrimRight(u.Path, "/"), nil
}

func isLoopbackHost(h string) bool {
	if strings.EqualFold(h, "localhost") {
		return true
	}
	ip, err := netip.ParseAddr(h)
	return err == nil && ip.IsLoopback()
}

// flexID accepts a Headscale id sent as a JSON string or number.
type flexID string

func (f *flexID) UnmarshalJSON(b []byte) error {
	s := strings.Trim(strings.TrimSpace(string(b)), `"`)
	if s == "null" {
		s = ""
	}
	*f = flexID(s)
	return nil
}

func (c *HeadscaleClient) do(ctx context.Context, method, path string, query url.Values, in, out any) error {
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
	req.Header.Set("Authorization", "Bearer "+c.key)
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return fmt.Errorf("headscale request failed: %s", redactURLErr(err))
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return fmt.Errorf("headscale response: %s", redactURLErr(err))
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
		return fmt.Errorf("headscale response is not valid JSON: %w", err)
	}
	return nil
}

// HeadscaleNode is a node of the Headscale tailnet.
type HeadscaleNode struct {
	ID        string
	Name      string
	GivenName string
	IPs       []string
	Online    bool
	Tags      []string
}

// Nodes lists every node.
func (c *HeadscaleClient) Nodes(ctx context.Context) ([]HeadscaleNode, error) {
	var raw struct {
		Nodes []struct {
			ID          flexID   `json:"id"`
			Name        string   `json:"name"`
			GivenName   string   `json:"givenName"`
			IPAddresses []string `json:"ipAddresses"`
			Online      bool     `json:"online"`
			ForcedTags  []string `json:"forcedTags"`
			ValidTags   []string `json:"validTags"`
		} `json:"nodes"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/v1/node", nil, nil, &raw); err != nil {
		return nil, err
	}
	out := make([]HeadscaleNode, 0, len(raw.Nodes))
	for _, n := range raw.Nodes {
		tags := n.ValidTags
		if len(tags) == 0 {
			tags = n.ForcedTags
		}
		out = append(out, HeadscaleNode{ID: string(n.ID), Name: n.Name, GivenName: n.GivenName, IPs: nonNil(n.IPAddresses), Online: n.Online, Tags: nonNil(tags)})
	}
	return out, nil
}

// DeleteNode removes a node by its Headscale id. A node that is gone is not an error.
func (c *HeadscaleClient) DeleteNode(ctx context.Context, id string) error {
	if _, err := strconv.ParseUint(id, 10, 64); err != nil {
		return fmt.Errorf("headscale: invalid node id %q", id)
	}
	err := c.do(ctx, http.MethodDelete, "/api/v1/node/"+id, nil, nil, nil)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	return err
}

// FindNode returns the node a tailnet device reference points at: the one that
// carries one of ips, else the one named hostname. ok is false when none matches.
func FindNode(nodes []HeadscaleNode, ips []string, hostname string) (HeadscaleNode, bool) {
	for _, n := range nodes {
		for _, ip := range ips {
			for _, nip := range n.IPs {
				if ip != "" && ip == nip {
					return n, true
				}
			}
		}
	}
	if hostname != "" {
		for _, n := range nodes {
			if n.GivenName == hostname || n.Name == hostname {
				return n, true
			}
		}
	}
	return HeadscaleNode{}, false
}

// userID resolves the configured user to its id ("" when no user is configured).
func (c *HeadscaleClient) userID(ctx context.Context) (string, error) {
	if c.user == "" {
		return "", nil
	}
	var raw struct {
		Users []struct {
			ID   flexID `json:"id"`
			Name string `json:"name"`
		} `json:"users"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/v1/user", url.Values{"name": {c.user}}, nil, &raw); err != nil {
		return "", err
	}
	for _, u := range raw.Users {
		if u.Name == c.user {
			return string(u.ID), nil
		}
	}
	return "", fmt.Errorf("headscale user %q does not exist", c.user)
}

// MintPreAuthKey creates a single-use, non-ephemeral pre-auth key carrying tags.
// The nodes it registers take their tags from the key, so the sidecar never
// passes --advertise-tags.
func (c *HeadscaleClient) MintPreAuthKey(ctx context.Context, tags []string, expiry time.Duration) (AuthKey, error) {
	uid, err := c.userID(ctx)
	if err != nil {
		return AuthKey{}, err
	}
	req := map[string]any{
		"reusable": false, "ephemeral": false,
		"expiration": time.Now().Add(expiry).UTC().Format(time.RFC3339),
		"aclTags":    tags,
	}
	if uid != "" {
		req["user"] = uid
	}
	var resp struct {
		PreAuthKey struct {
			ID         flexID    `json:"id"`
			Key        string    `json:"key"`
			Expiration time.Time `json:"expiration"`
		} `json:"preAuthKey"`
	}
	if err := c.do(ctx, http.MethodPost, "/api/v1/preauthkey", nil, req, &resp); err != nil {
		return AuthKey{}, err
	}
	if resp.PreAuthKey.Key == "" {
		return AuthKey{}, errors.New("headscale: the API returned no key")
	}
	return AuthKey{ID: string(resp.PreAuthKey.ID), Key: resp.PreAuthKey.Key, Expires: resp.PreAuthKey.Expiration}, nil
}

// ExpirePreAuthKey invalidates a pre-auth key. A key that is gone is not an error.
func (c *HeadscaleClient) ExpirePreAuthKey(ctx context.Context, id string) error {
	if id == "" {
		return nil
	}
	err := c.do(ctx, http.MethodPost, "/api/v1/preauthkey/expire", nil, map[string]string{"id": id}, nil)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	return err
}

// Test verifies the credentials without leaving residue: it lists nodes, finds
// the configured user, mints a 60 second key with tags and expires it.
func (c *HeadscaleClient) Test(ctx context.Context, tags []string) TailscaleTestRun {
	var res TailscaleTestRun
	add := func(ch TailscaleCheck) { res.Checks = append(res.Checks, ch) }

	nodes, err := c.Nodes(ctx)
	if err != nil {
		msg := "Could not reach the Headscale API"
		if errors.Is(err, ErrAuth) {
			msg = "Headscale rejected the API key"
		}
		add(TailscaleCheck{ID: "credentials", Message: fmt.Sprintf("%s: %v", msg, err)})
		return res
	}
	add(TailscaleCheck{ID: "credentials", OK: true, Message: fmt.Sprintf("The API key was accepted (%d nodes).", len(nodes))})

	if _, err := c.userID(ctx); err != nil {
		add(TailscaleCheck{ID: "user", Message: err.Error()})
	} else if c.user != "" {
		add(TailscaleCheck{ID: "user", OK: true, Message: fmt.Sprintf("Found the Headscale user %q.", c.user)})
	} else {
		add(TailscaleCheck{ID: "user", OK: true, Skipped: true, Message: "No user configured: keys are tags-only and nodes belong to tagged-devices."})
	}

	key, err := c.MintPreAuthKey(ctx, tags, time.Minute)
	if err != nil {
		add(TailscaleCheck{ID: "auth_keys", Message: fmt.Sprintf("Could not create a pre-auth key with tags %s (the tags must be allowed by Headscale's policy tagOwners): %v", strings.Join(tags, ", "), err)})
	} else if err := c.ExpirePreAuthKey(ctx, key.ID); err != nil {
		add(TailscaleCheck{ID: "auth_keys", OK: true, Message: fmt.Sprintf("Created a pre-auth key, but could not expire the test key (it expires on its own): %v", err)})
	} else {
		add(TailscaleCheck{ID: "auth_keys", OK: true, Message: fmt.Sprintf("Created and expired a test pre-auth key with tags %s.", strings.Join(tags, ", "))})
	}
	add(TailscaleCheck{ID: "https", OK: true, Skipped: true, Message: "Headscale has no Serve HTTPS or Funnel: services expose plain HTTP and raw TCP over WireGuard."})

	res.OK = true
	for _, ch := range res.Checks {
		if !ch.OK {
			res.OK = false
		}
	}
	return res
}

// HealthCheck probes GET <loginServer>/health for the pre-auth credential kind,
// which has no API access.
func HealthCheck(ctx context.Context, hc *http.Client, loginServer string) error {
	if hc == nil {
		hc = defaultHTTP
	}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(loginServer, "/")+"/health", nil)
	if err != nil {
		return err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("could not reach the Headscale server: %s", redactURLErr(err))
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<10))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("the Headscale server answered %d on /health", resp.StatusCode)
	}
	return nil
}
