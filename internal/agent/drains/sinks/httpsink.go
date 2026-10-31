package sinks

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

const (
	userAgent        = "nebulactrl-agent"
	httpTimeout      = 30 * time.Second
	maxRedirects     = 3
	gzipThreshold    = 1 << 10 // bodies above 1 KiB are compressed where enabled
	maxErrBody       = 4 << 10 // response bytes read for an error message
	maxErrMsgRunes   = 1024
	maxDrainRespBody = 1 << 20 // extra response bytes discarded to keep connections reusable
)

// httpSender is the shared HTTP delivery of the Loki, OTLP and generic HTTP sinks.
type httpSender struct {
	client  *http.Client
	method  string
	url     string
	header  http.Header // static request headers: custom, auth
	useGzip bool
	secrets map[string]string
}

func newHTTPSender(method, rawURL string, header http.Header, useGzip bool, secrets map[string]string) *httpSender {
	return &httpSender{
		client: &http.Client{
			Timeout: httpTimeout,
			Transport: &http.Transport{
				Proxy:                 http.ProxyFromEnvironment,
				DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
				TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
				TLSHandshakeTimeout:   10 * time.Second,
				ForceAttemptHTTP2:     true,
				MaxIdleConns:          4,
				MaxIdleConnsPerHost:   2,
				IdleConnTimeout:       60 * time.Second,
				ResponseHeaderTimeout: httpTimeout,
			},
			CheckRedirect: checkRedirect,
		},
		method:  method,
		url:     rawURL,
		header:  header,
		useGzip: useGzip,
		secrets: secrets,
	}
}

// checkRedirect follows at most three redirects, never from https to http, and
// drops every header except the body descriptors when the host changes, so
// credentials and API keys never reach another host.
func checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) > maxRedirects {
		return errors.New("stopped after 3 redirects")
	}
	first := via[0]
	if first.URL.Scheme == "https" && req.URL.Scheme != "https" {
		return errors.New("refusing redirect from https to http")
	}
	if req.URL.Host != first.URL.Host {
		for k := range req.Header {
			switch http.CanonicalHeaderKey(k) {
			case "User-Agent", "Content-Type", "Content-Encoding", "Content-Length":
			default:
				delete(req.Header, k)
			}
		}
	}
	return nil
}

func (s *httpSender) close() error {
	s.client.CloseIdleConnections()
	return nil
}

// send performs one request. A 2xx answer is success, anything else a *SendError.
func (s *httpSender) send(ctx context.Context, contentType string, body []byte) error {
	hdr := s.header.Clone()
	if hdr == nil {
		hdr = http.Header{}
	}
	hdr.Set("Content-Type", contentType)
	hdr.Set("User-Agent", userAgent)
	payload := body
	if s.useGzip && len(body) > gzipThreshold {
		var buf bytes.Buffer
		zw, _ := gzip.NewWriterLevel(&buf, gzip.BestSpeed)
		_, _ = zw.Write(body)
		if err := zw.Close(); err != nil {
			return err
		}
		payload = buf.Bytes()
		hdr.Set("Content-Encoding", "gzip")
	}
	req, err := http.NewRequestWithContext(ctx, s.method, s.url, bytes.NewReader(payload))
	if err != nil {
		return errors.New("build request: " + Redact(err.Error(), s.secrets))
	}
	req.Header = hdr

	resp, err := s.client.Do(req)
	if err != nil {
		inner := err
		var ue *url.Error
		if errors.As(err, &ue) {
			inner = ue.Err
		}
		return &SendError{Err: &redactedError{msg: Redact(err.Error(), s.secrets), err: inner}}
	}
	defer resp.Body.Close()
	snippet, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrBody))
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxDrainRespBody))

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	msg := "HTTP " + strconv.Itoa(resp.StatusCode) + " " + http.StatusText(resp.StatusCode)
	if t := responseText(snippet, s.secrets); t != "" {
		msg += ": " + t
	}
	return &SendError{
		Err:        errors.New(msg),
		StatusCode: resp.StatusCode,
		Permanent:  permanentStatus(resp.StatusCode),
		RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After"), time.Now()),
	}
}

// permanentStatus reports whether retrying the same batch cannot succeed.
func permanentStatus(code int) bool {
	if code < 400 || code >= 500 {
		return false
	}
	switch code {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusRequestTimeout, 425, http.StatusTooManyRequests:
		return false
	}
	return true
}

// parseRetryAfter reads a Retry-After header: delta seconds or an HTTP date.
func parseRetryAfter(v string, now time.Time) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if secs, err := strconv.ParseInt(v, 10, 64); err == nil {
		if secs <= 0 {
			return 0
		}
		return time.Duration(min(secs, 24*3600)) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := t.Sub(now); d > 0 {
			return min(d, 24*time.Hour)
		}
	}
	return 0
}

// responseText turns a response body snippet into one redacted line.
func responseText(b []byte, secrets map[string]string) string {
	s := strings.ToValidUTF8(string(b), "\uFFFD")
	s = strings.Join(strings.Fields(s), " ")
	s = Redact(s, secrets)
	if utf8.RuneCountInString(s) > maxErrMsgRunes {
		s = string([]rune(s)[:maxErrMsgRunes]) + "…"
	}
	return s
}

// redactedError carries a message already stripped of secrets over the
// original error, so errors.Is/As (context cancellation, timeouts) keep working.
type redactedError struct {
	msg string
	err error
}

func (e *redactedError) Error() string { return e.msg }
func (e *redactedError) Unwrap() error { return e.err }

// buildHeaders assembles the static request headers from the secrets: the
// headers secret (a JSON object of strings), then authentication. Basic
// (username) wins over bearer (token) when allowBasic is set.
func buildHeaders(secrets map[string]string, allowBasic bool) (http.Header, error) {
	h := http.Header{}
	if raw := strings.TrimSpace(secrets[agentprotocol.LogDrainSecretHeaders]); raw != "" {
		var m map[string]string
		if err := json.Unmarshal([]byte(raw), &m); err != nil {
			return nil, errors.New("the headers secret must be a JSON object of string names to string values")
		}
		for k, v := range m {
			if !validHeaderName(k) {
				return nil, errors.New("the headers secret has an invalid header name")
			}
			if strings.ContainsAny(v, "\r\n\x00") {
				return nil, errors.New("the headers secret has an invalid value for header " + k)
			}
			switch http.CanonicalHeaderKey(k) {
			case "Host", "Content-Length", "Transfer-Encoding", "Connection", "Content-Encoding":
				return nil, errors.New("the headers secret must not set the " + http.CanonicalHeaderKey(k) + " header")
			}
			h.Set(k, v)
		}
	}
	user := secrets[agentprotocol.LogDrainSecretUsername]
	token := secrets[agentprotocol.LogDrainSecretToken]
	switch {
	case allowBasic && user != "":
		cred := base64.StdEncoding.EncodeToString([]byte(user + ":" + secrets[agentprotocol.LogDrainSecretPassword]))
		h.Set("Authorization", "Basic "+cred)
	case token != "":
		if strings.ContainsAny(token, "\r\n\x00") {
			return nil, errors.New("the token secret contains invalid characters")
		}
		h.Set("Authorization", "Bearer "+token)
	}
	return h, nil
}

func validHeaderName(s string) bool {
	if s == "" {
		return false
	}
	for i := range len(s) {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case strings.IndexByte("!#$%&'*+-.^_`|~", c) >= 0:
		default:
			return false
		}
	}
	return true
}

// marshalJSON encodes v without HTML escaping and without a trailing newline.
func marshalJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}
