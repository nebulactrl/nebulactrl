// Package lokiadmin is the control plane's side of the built-in Loki: one
// instance, one tenant per team (the team's id). The forwarder
// (internal/instsvc/forward) sets the tenant of every push from the stream's
// team; this package reads a tenant's logs (the dashboard's central-logs view,
// which replaces Grafana) and deletes a purged team's logs.
package lokiadmin

import (
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

	"github.com/nebulactrl/nebulactrl/internal/instsvc"
)

// Config is how the control plane reaches Loki.
type Config struct {
	// URL is Loki on the internal network (http://loki:3100).
	URL  string
	HTTP *http.Client
}

// Backend implements instsvc.Backend for the loki kind.
type Backend struct {
	base string
	http *http.Client
}

var _ instsvc.Backend = (*Backend)(nil)

// New returns a Backend.
func New(cfg Config) (*Backend, error) {
	u, err := url.Parse(strings.TrimRight(cfg.URL, "/"))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("loki url %q is not an http(s) URL", cfg.URL)
	}
	hc := cfg.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	return &Backend{base: u.String(), http: hc}, nil
}

// Health checks that Loki answers /ready.
func (b *Backend) Health(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.base+"/ready", nil)
	if err != nil {
		return err
	}
	resp, err := b.http.Do(req)
	if err != nil {
		return fmt.Errorf("built-in log store: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("built-in log store is not ready (status %d)", resp.StatusCode)
	}
	return nil
}

// EnsureSlice has nothing to create: a tenant exists when its first push arrives.
func (b *Backend) EnsureSlice(context.Context, instsvc.Slice, int64) error { return nil }

// Usage is 0: logs have no quota (they follow retention).
func (b *Backend) Usage(context.Context, instsvc.Slice) (int64, error) { return 0, nil }

// purgeSince (2020-01-01 UTC) is before anything the built-in store ever held.
const purgeSince = 1577836800

// Purge asks Loki to delete everything of the tenant (the compactor does it).
func (b *Backend) Purge(ctx context.Context, s instsvc.Slice) error {
	if s.Ref == "" {
		return errors.New("a log slice needs its tenant")
	}
	// Loki rejects a start of 0; any time before the retention window reaches every line.
	q := url.Values{"query": {`{server=~".+"}`}, "start": {strconv.FormatInt(purgeSince, 10)}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.base+"/loki/api/v1/delete?"+q.Encode(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-Scope-OrgID", s.Ref)
	resp, err := b.http.Do(req)
	if err != nil {
		return fmt.Errorf("delete logs: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("delete logs: loki answered %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

// Entry is one log line.
type Entry struct {
	Time time.Time
	Line string
}

// Stream is the lines of one label set, oldest or newest first as Loki returned them.
type Stream struct {
	Labels  map[string]string
	Entries []Entry
}

// QueryRange runs a LogQL query for one tenant, newest lines first, at most
// limit lines. The caller builds logql; the tenant decides whose data it sees.
func (b *Backend) QueryRange(ctx context.Context, tenant, logql string, from, to time.Time, limit int) ([]Stream, error) {
	if tenant == "" {
		return nil, errors.New("a log query needs a tenant")
	}
	if limit <= 0 || limit > 5000 {
		limit = 1000
	}
	q := url.Values{
		"query": {logql}, "limit": {strconv.Itoa(limit)}, "direction": {"backward"},
		"start": {strconv.FormatInt(from.UnixNano(), 10)}, "end": {strconv.FormatInt(to.UnixNano(), 10)},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.base+"/loki/api/v1/query_range?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Scope-OrgID", tenant)
	resp, err := b.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("query logs: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("query logs: loki answered %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var out struct {
		Data struct {
			Result []struct {
				Stream map[string]string `json:"stream"`
				Values [][2]string       `json:"values"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<20)).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode log query: %w", err)
	}
	streams := make([]Stream, 0, len(out.Data.Result))
	for _, r := range out.Data.Result {
		s := Stream{Labels: r.Stream, Entries: make([]Entry, 0, len(r.Values))}
		for _, v := range r.Values {
			ns, err := strconv.ParseInt(v[0], 10, 64)
			if err != nil {
				continue
			}
			s.Entries = append(s.Entries, Entry{Time: time.Unix(0, ns).UTC(), Line: v[1]})
		}
		streams = append(streams, s)
	}
	return streams, nil
}
