package tailscale

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"golang.org/x/oauth2"
)

// ErrPolicyConflict is returned by SetPolicy when the policy changed since it
// was read (HTTP 412 on If-Match): read it again and retry.
var ErrPolicyConflict = errors.New("tailscale: the policy changed since it was read")

// ErrPolicyNotConditional is returned by SetPolicy when no ETag was given: a
// write without If-Match would overwrite whatever a human saved in the console
// in the meantime.
var ErrPolicyNotConditional = errors.New("tailscale: refusing to write the policy without the ETag it was read with")

// CheckToken verifies that the OAuth credentials are accepted: it makes a real
// token exchange, bypassing the cached token (which stays valid for an hour and
// would hide an outage of the API). It is the coordinator health probe for
// Tailscale.
func (c *Client) CheckToken(ctx context.Context) error {
	ts := c.cc.TokenSource(context.WithValue(context.Background(), oauth2.HTTPClient, c.hc))
	_, err := exchange(ctx, ts)
	return err
}

// policyRequest performs a policy call that needs headers the JSON helper does
// not expose (Accept: application/hujson, ETag, If-Match).
func (c *Client) policyRequest(ctx context.Context, method, path string, body []byte, ifMatch string) (http.Header, []byte, error) {
	tok, err := c.token(ctx)
	if err != nil {
		return nil, nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rd)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Accept", "application/hujson")
	if body != nil {
		req.Header.Set("Content-Type", "application/hujson")
	}
	if ifMatch != "" {
		req.Header.Set("If-Match", ifMatch)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("tailscale request failed: %s", redactURLErr(err))
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, nil, fmt.Errorf("tailscale response: %s", redactURLErr(err))
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
		return resp.Header, raw, &APIError{Status: resp.StatusCode, Message: msg}
	}
	return resp.Header, raw, nil
}

// GetPolicy reads the tailnet policy file as HuJSON (comments preserved) with
// its ETag. It needs the policy_file:read scope (or policy_file).
func (c *Client) GetPolicy(ctx context.Context) (Policy, error) {
	h, raw, err := c.policyRequest(ctx, http.MethodGet, c.tailnetPath("/acl"), nil, "")
	if err != nil {
		return Policy{}, err
	}
	return Policy{Raw: string(raw), Version: h.Get("ETag")}, nil
}

// ValidatePolicy asks Tailscale to validate a policy file without storing it
// (POST /acl/validate). A rejected policy is returned as an error carrying
// Tailscale's message.
func (c *Client) ValidatePolicy(ctx context.Context, doc []byte) error {
	_, raw, err := c.policyRequest(ctx, http.MethodPost, c.tailnetPath("/acl/validate"), doc, "")
	if err != nil {
		return err
	}
	// A valid policy answers 200 with an empty object; a body with data lists
	// failed ACL tests.
	var out struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(raw, &out) == nil && out.Message != "" {
		return fmt.Errorf("tailscale rejected the policy: %s", out.Message)
	}
	return nil
}

// SetPolicy stores the policy file, guarded by ifMatch (the ETag read with
// GetPolicy). It needs the policy_file scope: it can rewrite the whole policy.
// An empty ifMatch is refused (ErrPolicyNotConditional): the policy is never
// overwritten blind.
func (c *Client) SetPolicy(ctx context.Context, doc []byte, ifMatch string) (Policy, error) {
	if ifMatch == "" {
		return Policy{}, ErrPolicyNotConditional
	}
	h, _, err := c.policyRequest(ctx, http.MethodPost, c.tailnetPath("/acl"), doc, ifMatch)
	if err != nil {
		var ae *APIError
		if errors.As(err, &ae) && (ae.Status == http.StatusPreconditionFailed || ae.Status == http.StatusConflict) {
			return Policy{}, ErrPolicyConflict
		}
		return Policy{}, err
	}
	return Policy{Raw: string(doc), Version: h.Get("ETag")}, nil
}
