// Package cloudflare is the control-plane client of the Cloudflare API (v4) and
// the team-level integration built on it: an API token (sealed at rest, never
// returned), the cached zone list, DNS records for service domains that carry an
// ownership marker, a per-domain proxy setting and a live DNS status with a
// one-click fix. Tunnels (tunnel.go) share the client.
package cloudflare

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/nebulactrl/nebulactrl/internal/cfzone"
)

// DefaultBaseURL is the Cloudflare API v4 root.
const DefaultBaseURL = "https://api.cloudflare.com/client/v4"

// requestTimeout bounds every call to the Cloudflare API.
const requestTimeout = 20 * time.Second

// maxResponseBytes bounds how much of an API response is read.
const maxResponseBytes = 8 << 20

// maxPages bounds paging loops (50 zones per page).
const maxPages = 100

// Errors callers branch on. They never contain the token.
var (
	// ErrNotFound is returned for a 404 (record or zone already gone).
	ErrNotFound = errors.New("cloudflare: not found")
	// ErrAuth is returned when the token is rejected or lacks a permission.
	ErrAuth = errors.New("cloudflare: token rejected or missing a permission")
)

// APIError is a failed answer of the Cloudflare API. Message is Cloudflare's
// own error text (with the token scrubbed out, should it ever echo it).
type APIError struct {
	Status     int
	Code       int
	Message    string
	RetryAfter time.Duration
}

func (e *APIError) Error() string {
	switch {
	case e.Message == "" && e.Code == 0:
		return fmt.Sprintf("cloudflare API answered %d", e.Status)
	case e.Code == 0:
		return fmt.Sprintf("cloudflare API answered %d: %s", e.Status, e.Message)
	default:
		return fmt.Sprintf("cloudflare API answered %d (code %d): %s", e.Status, e.Code, e.Message)
	}
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
	BaseURL    string
	Token      string
	HTTPClient *http.Client
}

// Client talks to the Cloudflare API with one API token.
type Client struct {
	base  string
	token string
	hc    *http.Client
}

var defaultHTTP = &http.Client{Timeout: requestTimeout}

// New returns a Client. It performs no I/O.
func New(cfg Config) *Client {
	base := strings.TrimRight(cfg.BaseURL, "/")
	if base == "" {
		base = DefaultBaseURL
	}
	hc := cfg.HTTPClient
	if hc == nil {
		hc = defaultHTTP
	}
	return &Client{base: base, token: cfg.Token, hc: hc}
}

// ResultInfo is the paging block of a list answer.
type ResultInfo struct {
	Page       int `json:"page"`
	PerPage    int `json:"per_page"`
	Count      int `json:"count"`
	TotalCount int `json:"total_count"`
	TotalPages int `json:"total_pages"`
}

type envelope struct {
	Success bool `json:"success"`
	Errors  []struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"errors"`
	Result     json.RawMessage `json:"result"`
	ResultInfo ResultInfo      `json:"result_info"`
}

// do performs one API call. path is relative to the API root ("/zones"). A JSON
// body is sent when body is non-nil; the envelope's result is decoded into out
// when out is non-nil. A failed call is an *APIError.
func (c *Client) do(ctx context.Context, method, path string, query url.Values, body, out any) error {
	_, err := c.doInfo(ctx, method, path, query, body, out)
	return err
}

// doInfo is do that also returns the paging block.
func (c *Client) doInfo(ctx context.Context, method, path string, query url.Values, body, out any) (ResultInfo, error) {
	u := c.base + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return ResultInfo{}, fmt.Errorf("cloudflare: encode request: %w", err)
		}
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return ResultInfo{}, fmt.Errorf("cloudflare: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "nebulactrl")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		// The URL never contains the token; the transport error is safe to wrap.
		return ResultInfo{}, fmt.Errorf("cloudflare: %s %s: %w", method, path, scrubURLError(err))
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return ResultInfo{}, fmt.Errorf("cloudflare: read response: %w", err)
	}
	var env envelope
	jsonErr := json.Unmarshal(raw, &env)
	if resp.StatusCode/100 != 2 || (jsonErr == nil && !env.Success) {
		ae := &APIError{Status: resp.StatusCode}
		if resp.StatusCode/100 == 2 {
			ae.Status = http.StatusBadGateway
		}
		if jsonErr == nil && len(env.Errors) > 0 {
			ae.Code = env.Errors[0].Code
			msgs := make([]string, 0, len(env.Errors))
			for _, e := range env.Errors {
				msgs = append(msgs, e.Message)
			}
			ae.Message = strings.Join(msgs, "; ")
		} else if jsonErr != nil {
			ae.Message = http.StatusText(resp.StatusCode)
		}
		if c.token != "" {
			ae.Message = strings.ReplaceAll(ae.Message, c.token, "[redacted]")
		}
		if s := resp.Header.Get("Retry-After"); s != "" {
			if n, err := strconv.Atoi(s); err == nil && n > 0 {
				ae.RetryAfter = time.Duration(n) * time.Second
			}
		}
		return ResultInfo{}, ae
	}
	if jsonErr != nil {
		return ResultInfo{}, fmt.Errorf("cloudflare: decode response: %w", jsonErr)
	}
	if out != nil && len(env.Result) > 0 && string(env.Result) != "null" {
		if err := json.Unmarshal(env.Result, out); err != nil {
			return ResultInfo{}, fmt.Errorf("cloudflare: decode result: %w", err)
		}
	}
	return env.ResultInfo, nil
}

// scrubURLError drops the request URL from a transport error (it is already
// token-free, but the shorter message reads better).
func scrubURLError(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}

// ---- token ----

// TokenStatus is the answer of the token verification endpoint.
type TokenStatus struct {
	ID        string     `json:"id"`
	Status    string     `json:"status"`
	NotBefore *time.Time `json:"not_before"`
	ExpiresOn *time.Time `json:"expires_on"`
}

// VerifyToken checks the token. User-owned tokens verify at /user/tokens/verify,
// account-owned ones at /accounts/{id}/tokens/verify: the account endpoint is
// tried when accountID is known and the user endpoint does not accept the token.
func (c *Client) VerifyToken(ctx context.Context, accountID string) (TokenStatus, error) {
	var st TokenStatus
	err := c.do(ctx, http.MethodGet, "/user/tokens/verify", nil, nil, &st)
	if err != nil && accountID != "" && errors.Is(err, ErrAuth) {
		st = TokenStatus{}
		if err2 := c.do(ctx, http.MethodGet, "/accounts/"+url.PathEscape(accountID)+"/tokens/verify", nil, nil, &st); err2 == nil {
			return st, nil
		}
	}
	return st, err
}

// ---- zones ----

// Zone is a DNS zone the token can see.
type Zone = cfzone.Zone

type zoneWire struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Status  string `json:"status"`
	Paused  bool   `json:"paused"`
	Account struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"account"`
}

// ListZones returns every zone the token can see, ordered by name.
func (c *Client) ListZones(ctx context.Context) ([]Zone, error) {
	var out []Zone
	for page := 1; page <= maxPages; page++ {
		var wire []zoneWire
		q := url.Values{"per_page": {"50"}, "page": {strconv.Itoa(page)}}
		info, err := c.doInfo(ctx, http.MethodGet, "/zones", q, nil, &wire)
		if err != nil {
			return nil, err
		}
		for _, z := range wire {
			out = append(out, Zone{ID: z.ID, Name: strings.ToLower(z.Name), Status: z.Status, Paused: z.Paused, AccountID: z.Account.ID, AccountName: z.Account.Name})
		}
		if info.TotalPages == 0 || page >= info.TotalPages || len(wire) == 0 {
			break
		}
	}
	sortZones(out)
	return out, nil
}

// ZoneSSLMode returns the zone's SSL/TLS encryption mode: off, flexible, full
// or strict. Reading it needs the Zone Settings: Read permission.
func (c *Client) ZoneSSLMode(ctx context.Context, zoneID string) (string, error) {
	var s struct {
		Value string `json:"value"`
	}
	if err := c.do(ctx, http.MethodGet, "/zones/"+url.PathEscape(zoneID)+"/settings/ssl", nil, nil, &s); err != nil {
		return "", err
	}
	return s.Value, nil
}

// ---- DNS records ----

// Record is one DNS record.
type Record struct {
	ID      string   `json:"id"`
	Type    string   `json:"type"`
	Name    string   `json:"name"`
	Content string   `json:"content"`
	Proxied bool     `json:"proxied"`
	TTL     int      `json:"ttl"`
	Comment string   `json:"comment"`
	Tags    []string `json:"tags"`
}

// RecordInput is the writable part of a record. Comment and Tags carry the
// ownership marker.
type RecordInput struct {
	Type    string   `json:"type,omitempty"`
	Name    string   `json:"name,omitempty"`
	Content string   `json:"content"`
	Proxied bool     `json:"proxied"`
	TTL     int      `json:"ttl"`
	Comment string   `json:"comment,omitempty"`
	Tags    []string `json:"tags,omitempty"`
}

// ListRecords returns the A, AAAA and CNAME records of one name in a zone.
func (c *Client) ListRecords(ctx context.Context, zoneID, name string) ([]Record, error) {
	var out []Record
	for page := 1; page <= maxPages; page++ {
		var recs []Record
		q := url.Values{"name.exact": {name}, "per_page": {"100"}, "page": {strconv.Itoa(page)}}
		info, err := c.doInfo(ctx, http.MethodGet, "/zones/"+url.PathEscape(zoneID)+"/dns_records", q, nil, &recs)
		if err != nil {
			return nil, err
		}
		for _, r := range recs {
			// Filter again: the name filter is case-insensitive server-side, and
			// only address and alias records matter here.
			if !strings.EqualFold(r.Name, name) {
				continue
			}
			switch r.Type {
			case "A", "AAAA", "CNAME":
				out = append(out, r)
			}
		}
		if info.TotalPages == 0 || page >= info.TotalPages || len(recs) == 0 {
			break
		}
	}
	return out, nil
}

// GetRecord returns one record (ErrNotFound when it is gone).
func (c *Client) GetRecord(ctx context.Context, zoneID, recordID string) (Record, error) {
	var r Record
	err := c.do(ctx, http.MethodGet, "/zones/"+url.PathEscape(zoneID)+"/dns_records/"+url.PathEscape(recordID), nil, nil, &r)
	return r, err
}

// tagsRefused reports whether an error looks like Cloudflare refusing record
// tags (not supported by the plan, or over the quota).
func tagsRefused(err error) bool {
	var ae *APIError
	return errors.As(err, &ae) && ae.Status >= 400 && ae.Status < 500 && ae.Status != http.StatusUnauthorized &&
		ae.Status != http.StatusForbidden && ae.Status != http.StatusNotFound && strings.Contains(strings.ToLower(ae.Message), "tag")
}

// CreateRecord creates a record. Record tags are best effort: when Cloudflare
// refuses them, the record is created with its comment alone.
func (c *Client) CreateRecord(ctx context.Context, zoneID string, in RecordInput) (Record, error) {
	var r Record
	path := "/zones/" + url.PathEscape(zoneID) + "/dns_records"
	err := c.do(ctx, http.MethodPost, path, nil, in, &r)
	if err != nil && len(in.Tags) > 0 && tagsRefused(err) {
		in.Tags = nil
		r = Record{}
		err = c.do(ctx, http.MethodPost, path, nil, in, &r)
	}
	return r, err
}

// UpdateRecord changes a record in place (PATCH) and returns the stored result.
func (c *Client) UpdateRecord(ctx context.Context, zoneID, recordID string, in RecordInput) (Record, error) {
	var r Record
	path := "/zones/" + url.PathEscape(zoneID) + "/dns_records/" + url.PathEscape(recordID)
	err := c.do(ctx, http.MethodPatch, path, nil, in, &r)
	if err != nil && len(in.Tags) > 0 && tagsRefused(err) {
		in.Tags = nil
		r = Record{}
		err = c.do(ctx, http.MethodPatch, path, nil, in, &r)
	}
	return r, err
}

// DeleteRecord deletes a record (ErrNotFound when it is already gone).
func (c *Client) DeleteRecord(ctx context.Context, zoneID, recordID string) error {
	return c.do(ctx, http.MethodDelete, "/zones/"+url.PathEscape(zoneID)+"/dns_records/"+url.PathEscape(recordID), nil, nil, nil)
}
