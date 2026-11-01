package gitproviders

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// maxAPIBody bounds how much of a provider response is read.
const maxAPIBody = 8 << 20

// errNoAPI is returned by drivers that only receive webhooks (Bitbucket).
var errNoAPI = errors.New("this provider has no API integration: it only delivers push webhooks")

// apiError is a non-2xx provider API response.
type apiError struct {
	Status  int
	Message string
}

func (e *apiError) Error() string {
	return fmt.Sprintf("provider API returned %d: %s", e.Status, e.Message)
}

// newAPIError describes a non-2xx answer from its status and headers alone: the
// body is never read into the message. A rate limit is told apart the way the
// providers announce it: 429, or 403 with an exhausted X-RateLimit-Remaining
// (GitHub) or a Retry-After (GitHub's secondary limit).
func newAPIError(status int, h http.Header) *apiError {
	msg := http.StatusText(status)
	if status == http.StatusTooManyRequests ||
		(status == http.StatusForbidden && (h.Get("Retry-After") != "" || strings.TrimSpace(h.Get("X-RateLimit-Remaining")) == "0")) {
		msg = "rate limited"
	}
	return &apiError{Status: status, Message: msg}
}

// apiClient is an authenticated REST client for one provider API base.
type apiClient struct {
	hc    *http.Client
	base  string // API base URL without a trailing slash
	authz string // full Authorization header value ("" = anonymous)
	extra map[string]string
	// secrets are scrubbed from error messages.
	secrets []string
}

// do sends a JSON request and decodes a JSON response into out (when non-nil).
func (c *apiClient) do(ctx context.Context, method, path string, q url.Values, in, out any) error {
	u := c.base + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	var body io.Reader
	if in != nil {
		raw, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body) //nolint:gosec // requests go to the git connection base URL an admin configured
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.authz != "" {
		req.Header.Set("Authorization", c.authz)
	}
	req.Header.Set("User-Agent", "NebulaCtrl")
	for k, v := range c.extra {
		req.Header.Set(k, v)
	}
	resp, err := c.hc.Do(req) //nolint:gosec // requests go to the git connection base URL an admin configured
	if err != nil {
		return c.scrub(fmt.Errorf("%s %s: %w", method, path, err))
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxAPIBody))
	if err != nil {
		return c.scrub(fmt.Errorf("%s %s: read body: %w", method, path, err))
	}
	if resp.StatusCode/100 != 2 {
		// Only the status: the body of a remote that a team admin pointed us at
		// is not shown, so an internal service behind the URL cannot be read
		// through the error.
		return c.scrub(newAPIError(resp.StatusCode, resp.Header))
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("%s %s: decode response: %w", method, path, err)
		}
	}
	return nil
}

// scrub removes credentials from an error before it is logged or shown.
func (c *apiClient) scrub(err error) error {
	if err == nil {
		return nil
	}
	var ae *apiError
	if errors.As(err, &ae) {
		return &apiError{Status: ae.Status, Message: c.scrubString(ae.Message)}
	}
	if msg := c.scrubString(err.Error()); msg != err.Error() {
		return errors.New(msg)
	}
	return err
}

func (c *apiClient) scrubString(s string) string {
	for _, secret := range c.secrets {
		if len(secret) >= 6 {
			s = strings.ReplaceAll(s, secret, "••••••")
		}
	}
	return s
}

func isNotFound(err error) bool {
	var ae *apiError
	return errors.As(err, &ae) && ae.Status == http.StatusNotFound
}

// hmacSHA256Hex returns the lowercase hex HMAC-SHA256 of body.
func hmacSHA256Hex(secret string, body []byte) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write(body)
	return hex.EncodeToString(m.Sum(nil))
}

// verifyHMAC compares a hex signature (optionally prefixed "sha256=") with the
// HMAC-SHA256 of body in constant time.
func verifyHMAC(secret string, body []byte, sig string) bool {
	sig = strings.TrimSpace(sig)
	sig = strings.TrimPrefix(sig, "sha256=")
	if sig == "" || secret == "" {
		return false
	}
	want := hmacSHA256Hex(secret, body)
	return subtle.ConstantTimeCompare([]byte(strings.ToLower(sig)), []byte(want)) == 1
}

// secureEqual compares two strings in constant time.
func secureEqual(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func isZeroSHA(s string) bool { return strings.Trim(s, "0") == "" }

func branchFromRef(ref string) string {
	if b, ok := strings.CutPrefix(ref, "refs/heads/"); ok {
		return b
	}
	return ""
}

// firstLine is a commit message's subject.
func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return s
}

func addPaths(set map[string]struct{}, order *[]string, lists ...[]string) {
	for _, l := range lists {
		for _, p := range l {
			if _, ok := set[p]; !ok {
				set[p] = struct{}{}
				*order = append(*order, p)
			}
		}
	}
}

func trimBase(u string) string { return strings.TrimRight(strings.TrimSpace(u), "/") }

// hostOf returns the host[:port] of a URL ("" when unparsable).
func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Host
}
