package cloudflare

import (
	"context"
	"crypto/sha256"
	_ "embed" // the built-in IP list
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/notifications"
	"github.com/nebulactrl/nebulactrl/internal/policy"
)

// The built-in copy of Cloudflare's IP ranges, used on first start and on
// installs without internet access. Fetched on BuiltinIPsDate from
// https://www.cloudflare.com/ips-v4 and https://www.cloudflare.com/ips-v6 and
// identical to https://api.cloudflare.com/client/v4/ips (etag
// 38f79d050aa027e3be3865e495dcc9bc) at that time. Refresh it with
// `curl -fsS https://www.cloudflare.com/ips-v4 -o internal/integrations/cloudflare/data/ips-v4.txt`
// (and ips-v6) when releasing; the running job keeps the list current anyway.
//
//go:embed data/ips-v4.txt
var builtinV4Text []byte

//go:embed data/ips-v6.txt
var builtinV6Text []byte

// BuiltinIPsDate is the day the built-in list was fetched (UTC).
const BuiltinIPsDate = "2026-10-27"

// Sources of an IP list.
const (
	IPSourceBuiltin = "builtin"
	IPSourceAPI     = "api"
	IPSourceText    = "text"
)

// Sanity bounds of a fetched list (at least 10 IPv4 and 5 IPv6 ranges, never shrink by
// more than half). The prefix floors keep a poisoned answer from admitting the
// whole internet through a /0.
const (
	minV4Ranges   = 10
	minV6Ranges   = 5
	minV4PrefixLn = 8
	minV6PrefixLn = 16
	maxRanges     = 500
)

// IPRefreshInterval is how often the list is fetched.
const IPRefreshInterval = 3 * time.Hour

// ipsStaleAfter is how long fetches may fail before the team is told.
const ipsStaleAfter = 24 * time.Hour

const (
	ipsAPIPath    = "/ips"
	ipsTextBase   = "https://www.cloudflare.com"
	ipsFetchLimit = 1 << 20
)

// IPList is a validated set of Cloudflare ranges, normalised and sorted.
type IPList struct {
	V4     []string
	V6     []string
	Hash   string
	Source string
}

// CIDRs returns the v4 ranges followed by the v6 ranges (a copy).
func (l IPList) CIDRs() []string {
	out := make([]string, 0, len(l.V4)+len(l.V6))
	out = append(out, l.V4...)
	return append(out, l.V6...)
}

// Contains reports whether ip lies in one of the ranges.
func (l IPList) Contains(ip netip.Addr) bool {
	ip = ip.Unmap()
	ranges := l.V6
	if ip.Is4() {
		ranges = l.V4
	}
	for _, r := range ranges {
		if p, err := netip.ParsePrefix(r); err == nil && p.Contains(ip) {
			return true
		}
	}
	return false
}

// Builtin returns the list compiled into the binary.
func Builtin() IPList {
	l, err := NewIPList(IPSourceBuiltin, splitLines(builtinV4Text), splitLines(builtinV6Text), nil)
	if err != nil {
		// The embedded files are checked by a unit test; reaching this means the build is broken.
		panic("cloudflare: invalid built-in IP list: " + err.Error())
	}
	return l
}

func splitLines(b []byte) []string {
	return strings.Fields(string(b))
}

// NewIPList validates and normalises raw ranges. prev, when non-nil, is the
// list in use: the update is refused when it shrinks either family by more
// than half. Errors name the first problem and never contain a response body.
func NewIPList(source string, v4, v6 []string, prev *IPList) (IPList, error) {
	nv4, err := normalizeFamily(v4, true)
	if err != nil {
		return IPList{}, err
	}
	nv6, err := normalizeFamily(v6, false)
	if err != nil {
		return IPList{}, err
	}
	switch {
	case len(nv4) == 0 || len(nv6) == 0:
		return IPList{}, errors.New("the list is empty")
	case len(nv4) < minV4Ranges:
		return IPList{}, fmt.Errorf("only %d IPv4 ranges (at least %d expected)", len(nv4), minV4Ranges)
	case len(nv6) < minV6Ranges:
		return IPList{}, fmt.Errorf("only %d IPv6 ranges (at least %d expected)", len(nv6), minV6Ranges)
	case len(nv4) > maxRanges || len(nv6) > maxRanges:
		return IPList{}, fmt.Errorf("more than %d ranges", maxRanges)
	}
	if prev != nil {
		if len(nv4)*2 < len(prev.V4) {
			return IPList{}, fmt.Errorf("the IPv4 list would shrink from %d to %d ranges (more than half)", len(prev.V4), len(nv4))
		}
		if len(nv6)*2 < len(prev.V6) {
			return IPList{}, fmt.Errorf("the IPv6 list would shrink from %d to %d ranges (more than half)", len(prev.V6), len(nv6))
		}
	}
	return IPList{V4: nv4, V6: nv6, Hash: hashList(nv4, nv6), Source: source}, nil
}

func normalizeFamily(in []string, v4 bool) ([]string, error) {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, raw := range in {
		p, err := netip.ParsePrefix(strings.TrimSpace(raw))
		if err != nil {
			return nil, fmt.Errorf("%q is not a CIDR range", truncateText(raw, 40))
		}
		p = p.Masked()
		if p.Addr().Is4In6() {
			return nil, fmt.Errorf("%q is an IPv4-mapped IPv6 range", p)
		}
		switch {
		case v4 && !p.Addr().Is4():
			return nil, fmt.Errorf("%q is not an IPv4 range", p)
		case !v4 && !p.Addr().Is6():
			return nil, fmt.Errorf("%q is not an IPv6 range", p)
		case v4 && p.Bits() < minV4PrefixLn, !v4 && p.Bits() < minV6PrefixLn:
			return nil, fmt.Errorf("%q is implausibly wide for a Cloudflare range", p)
		}
		s := p.String()
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	slices.Sort(out)
	return out, nil
}

func truncateText(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

func hashList(v4, v6 []string) string {
	h := sha256.New()
	h.Write([]byte("v4\n" + strings.Join(v4, "\n") + "\nv6\n" + strings.Join(v6, "\n")))
	return hex.EncodeToString(h.Sum(nil))
}

// ---- fetching ----

// SetIPSources points the list fetch at other URLs (tests): apiBase replaces
// the Cloudflare API root and textBase the host of the ips-v4/ips-v6 files.
func (s *Service) SetIPSources(apiBase, textBase string) {
	s.mu.Lock()
	s.ipsAPIBase, s.ipsTextBase = strings.TrimRight(apiBase, "/"), strings.TrimRight(textBase, "/")
	s.mu.Unlock()
}

func (s *Service) ipURLs() (api, v4, v6 string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	base := s.ipsAPIBase
	if base == "" {
		base = s.baseURL
	}
	if base == "" {
		base = DefaultBaseURL
	}
	text := s.ipsTextBase
	if text == "" {
		text = ipsTextBase
	}
	return base + ipsAPIPath, text + "/ips-v4", text + "/ips-v6"
}

func (s *Service) httpClient() *http.Client {
	if s.hc != nil {
		return s.hc
	}
	return defaultHTTP
}

// httpGet reads one public URL (no credentials are sent).
func (s *Service) httpGet(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "nebulactrl")
	resp, err := s.httpClient().Do(req)
	if err != nil {
		return nil, scrubURLError(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("answered %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, ipsFetchLimit+1))
	if err != nil {
		return nil, err
	}
	if len(b) > ipsFetchLimit {
		return nil, errors.New("the answer is too large")
	}
	return b, nil
}

func (s *Service) fetchAPI(ctx context.Context, prev *IPList) (IPList, error) {
	api, _, _ := s.ipURLs()
	raw, err := s.httpGet(ctx, api)
	if err != nil {
		return IPList{}, err
	}
	var env struct {
		Success bool `json:"success"`
		Result  struct {
			V4 []string `json:"ipv4_cidrs"`
			V6 []string `json:"ipv6_cidrs"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return IPList{}, errors.New("the answer is not valid JSON")
	}
	if !env.Success {
		return IPList{}, errors.New("the API reported a failure")
	}
	return NewIPList(IPSourceAPI, env.Result.V4, env.Result.V6, prev)
}

func (s *Service) fetchText(ctx context.Context, prev *IPList) (IPList, error) {
	_, u4, u6 := s.ipURLs()
	b4, err := s.httpGet(ctx, u4)
	if err != nil {
		return IPList{}, err
	}
	b6, err := s.httpGet(ctx, u6)
	if err != nil {
		return IPList{}, err
	}
	return NewIPList(IPSourceText, splitLines(b4), splitLines(b6), prev)
}

// ---- stored list ----

func listOfRow(r db.CloudflareIpList) IPList {
	return IPList{V4: r.V4, V6: r.V6, Hash: r.Hash, Source: r.Source}
}

// CurrentIPList returns the list in force: the stored one, else the built-in.
func CurrentIPList(ctx context.Context, q *db.Queries) (IPList, error) {
	r, err := q.GetCloudflareIPList(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return Builtin(), nil
	}
	if err != nil {
		return IPList{}, fmt.Errorf("get cloudflare ip list: %w", err)
	}
	return listOfRow(r), nil
}

// RefreshResult says what a refresh did.
type RefreshResult struct {
	Changed bool
	Source  string
	Count   int
	// Err is why the list could not be refreshed (the stored list stays).
	Err error
}

// RefreshIPs fetches the list and stores it when it validates. The API is
// tried first, the text files second; the last good list stays on failure. A
// changed list is audited per team with a Cloudflare connection, pushed to
// every server that serves origin-locked domains (a route update, no redeploy,
// in the same transaction as the update) and announced.
func (s *Service) RefreshIPs(ctx context.Context) (RefreshResult, error) {
	seed := Builtin()
	if err := s.q.SeedCloudflareIPList(ctx, db.SeedCloudflareIPListParams{V4: seed.V4, V6: seed.V6, Hash: seed.Hash}); err != nil {
		return RefreshResult{}, fmt.Errorf("seed ip list: %w", err)
	}
	row, err := s.q.GetCloudflareIPList(ctx)
	if err != nil {
		return RefreshResult{}, fmt.Errorf("get ip list: %w", err)
	}
	prev := listOfRow(row)

	list, ferr := s.fetchAPI(ctx, &prev)
	if ferr != nil {
		s.log.WarnContext(ctx, "cloudflare ip list: api fetch failed, trying the text files", "err", ferr)
		var terr error
		if list, terr = s.fetchText(ctx, &prev); terr != nil {
			ferr = fmt.Errorf("API: %v; text files: %v", ferr, terr)
			return s.ipsFailed(ctx, row, ferr)
		}
	}
	changed := list.Hash != prev.Hash
	var newRow db.CloudflareIpList
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		newRow, err = db.New(tx).UpsertCloudflareIPList(ctx, db.UpsertCloudflareIPListParams{V4: list.V4, V6: list.V6, Hash: list.Hash, Source: list.Source})
		if err != nil || !changed {
			return err
		}
		if err := s.auditIPsUpdated(ctx, tx, prev, list); err != nil {
			return err
		}
		// The new ranges reach the proxies in the same transaction: a failed push
		// keeps the old list, so the next attempt pushes again.
		return s.pushZoneInTx(ctx, tx, "")
	})
	if err != nil {
		return RefreshResult{}, fmt.Errorf("store ip list: %w", err)
	}
	if row.FailingSince != nil {
		s.log.InfoContext(ctx, "cloudflare ip list: fetching works again", "failing_since", row.FailingSince)
	}
	res := RefreshResult{Changed: changed, Source: newRow.Source, Count: len(list.V4) + len(list.V6)}
	if changed {
		s.log.InfoContext(ctx, "cloudflare ip list updated", "source", list.Source, "v4", len(list.V4), "v6", len(list.V6))
		s.announceIPsChanged(ctx, prev, list)
	}
	return res, nil
}

// ipsFailed records a failed fetch and tells the teams once it has lasted a day.
func (s *Service) ipsFailed(ctx context.Context, row db.CloudflareIpList, ferr error) (RefreshResult, error) {
	msg := truncateText(ferr.Error(), 500)
	if err := s.q.SetCloudflareIPListFailure(ctx, msg); err != nil {
		return RefreshResult{Err: ferr}, fmt.Errorf("record ip list failure: %w", err)
	}
	s.log.WarnContext(ctx, "cloudflare ip list: refresh failed, keeping the last good list", "err", msg)
	since := s.now()
	if row.FailingSince != nil {
		since = *row.FailingSince
	}
	if s.now().Sub(since) >= ipsStaleAfter && row.StaleNotifiedAt == nil {
		if err := s.q.MarkCloudflareIPListStaleNotified(ctx); err == nil {
			s.announceIPsStale(ctx, since, msg)
		}
	}
	return RefreshResult{Err: ferr}, nil
}

func (s *Service) connectedWorkspaces(ctx context.Context) []uuid.UUID {
	wss, err := s.q.ListCloudflareWorkspaces(ctx)
	if err != nil {
		s.log.WarnContext(ctx, "list cloudflare workspaces", "err", err)
		return nil
	}
	return wss
}

func (s *Service) auditIPsUpdated(ctx context.Context, tx pgx.Tx, prev, next IPList) error {
	added, removed := diffRanges(prev.CIDRs(), next.CIDRs())
	md := map[string]any{
		"source": next.Source, "v4": len(next.V4), "v6": len(next.V6), "hash": next.Hash,
		"added": added, "removed": removed,
	}
	for _, ws := range s.connectedWorkspaces(ctx) {
		if err := audit.Record(audit.WithActorType(ctx, audit.ActorSystem), tx, audit.Entry{
			Action: "cloudflare.ips.updated", ResourceType: "cloudflare", ResourceID: next.Hash, WorkspaceID: ws, Metadata: md,
		}); err != nil {
			return err
		}
	}
	return nil
}

func diffRanges(old, next []string) (added, removed []string) {
	added, removed = []string{}, []string{}
	for _, c := range next {
		if !slices.Contains(old, c) {
			added = append(added, c)
		}
	}
	for _, c := range old {
		if !slices.Contains(next, c) {
			removed = append(removed, c)
		}
	}
	return added, removed
}

func (s *Service) announceIPsChanged(ctx context.Context, prev, next IPList) {
	added, removed := diffRanges(prev.CIDRs(), next.CIDRs())
	for _, ws := range s.connectedWorkspaces(ctx) {
		notifications.Emit(ctx, s.emitter(), notifications.Event{
			Type: notifications.TypeCloudflareIPsChanged, Severity: notifications.SeverityInfo, WorkspaceID: ws,
			Title: "Cloudflare IP ranges changed",
			Body: fmt.Sprintf("Cloudflare published new IP ranges (%d added, %d removed). Origin-locked routes were updated; no redeploy was needed.",
				len(added), len(removed)),
			URL:    "/settings/integrations",
			Fields: map[string]string{"Ranges": fmt.Sprintf("%d IPv4, %d IPv6", len(next.V4), len(next.V6)), "Source": next.Source},
		})
	}
}

func (s *Service) announceIPsStale(ctx context.Context, since time.Time, reason string) {
	for _, ws := range s.connectedWorkspaces(ctx) {
		notifications.Emit(ctx, s.emitter(), notifications.Event{
			Type: notifications.TypeCloudflareIPsStale, Severity: notifications.SeverityWarning, WorkspaceID: ws,
			Title: "Cloudflare IP ranges cannot be refreshed",
			Body: "NebulaCtrl has not been able to fetch Cloudflare's IP ranges since " + since.UTC().Format("2006-01-02 15:04") +
				" UTC. Origin-locked domains keep using the last good list; if Cloudflare adds ranges meanwhile, their visitors may be blocked.",
			URL:    "/settings/integrations",
			Fields: map[string]string{"Last error": reason},
		})
	}
}

// ---- API ----

// IPListView is the API view of the IP list in force.
type IPListView struct {
	Source       string     `json:"source" enum:"builtin,api,text" doc:"Where the list came from: the copy built into the binary, the Cloudflare API or the published text files"`
	V4Count      int        `json:"v4Count"`
	V6Count      int        `json:"v6Count"`
	Hash         string     `json:"hash"`
	FetchedAt    *time.Time `json:"fetchedAt" doc:"The last successful fetch; null while the built-in list is in use"`
	ChangedAt    *time.Time `json:"changedAt" doc:"The last time the content changed; null while the built-in list is in use"`
	BuiltinDate  string     `json:"builtinDate" doc:"The day the built-in list was fetched (YYYY-MM-DD)"`
	LastError    string     `json:"lastError" doc:"Why the last fetch failed; empty after a successful one"`
	FailingSince *time.Time `json:"failingSince"`
	Stale        bool       `json:"stale" doc:"Fetching has failed for more than 24 hours"`
}

// IPs returns the list in force, its age and the state of the refresh.
func (s *Service) IPs(ctx context.Context) (IPListView, error) {
	if err := policy.Require(ctx, policy.ScopeProjectsRead, nil); err != nil {
		return IPListView{}, err
	}
	return s.ipView(ctx)
}

func (s *Service) ipView(ctx context.Context) (IPListView, error) {
	r, err := s.q.GetCloudflareIPList(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		b := Builtin()
		return IPListView{Source: IPSourceBuiltin, V4Count: len(b.V4), V6Count: len(b.V6), Hash: b.Hash, BuiltinDate: BuiltinIPsDate}, nil
	}
	if err != nil {
		return IPListView{}, fmt.Errorf("get cloudflare ip list: %w", err)
	}
	v := IPListView{
		Source: r.Source, V4Count: len(r.V4), V6Count: len(r.V6), Hash: r.Hash, BuiltinDate: BuiltinIPsDate,
		LastError: r.LastError, FailingSince: r.FailingSince,
		Stale: r.FailingSince != nil && s.now().Sub(*r.FailingSince) >= ipsStaleAfter,
	}
	if r.Source != IPSourceBuiltin {
		f, c := r.FetchedAt, r.ChangedAt
		v.FetchedAt, v.ChangedAt = &f, &c
	}
	return v, nil
}

// RefreshIPsNow is the manual refresh (team admins). A failed fetch is a 502
// (`ip_refresh_failed`) so the button can say so; the stored list stays.
func (s *Service) RefreshIPsNow(ctx context.Context) (IPListView, error) {
	if err := policy.Require(ctx, policy.ScopeAdmin, nil); err != nil {
		return IPListView{}, err
	}
	res, err := s.RefreshIPs(ctx)
	if err != nil {
		return IPListView{}, fmt.Errorf("refresh cloudflare ip list: %w", err)
	}
	if res.Err != nil {
		return IPListView{}, apierr.New(http.StatusBadGateway, "ip_refresh_failed",
			"Cloudflare's IP ranges could not be fetched ("+truncateText(res.Err.Error(), 300)+"). The last good list stays in use.")
	}
	return s.ipView(ctx)
}
