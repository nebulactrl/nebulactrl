// Package guard is the edge guard: the forwardAuth server Traefik asks before
// it forwards a request of a route whose firewall the compiled routers cannot
// express (country conditions, or visitor addresses that arrive in
// Cf-Connecting-IP), or that has an auto-ban policy (the guard then decides
// allow list, bans and firewall rules in that order). It runs as
// `nebulactrl-agent guard` in a sidecar container that shares the proxy
// container's network namespace, so Traefik reaches it on
// 127.0.0.1:agentprotocol.GuardPort and nothing else can.
//
// The agent copies its own binary, the rules (RulesFile), the bans (BansFile)
// and the GeoIP database into the container's Dir; the server reloads them
// when their files change. It never talks to the agent.
package guard

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/oschwald/maxminddb-golang/v2"

	"github.com/nebulactrl/nebulactrl/internal/agent/pages"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Paths inside the guard container.
const (
	// Dir holds the binary, the rules and the GeoIP database.
	Dir = "/nebulactrl-guard"
	// Binary is the agent binary the container runs.
	Binary = Dir + "/nebulactrl-agent"
	// RulesFile is the JSON Rules document.
	RulesFile = "rules.json"
	// BansFile is the JSON agentprotocol.GuardBans document.
	BansFile = "bans.json"
)

// reloadEvery is how often the server looks for changed files.
const reloadEvery = 500 * time.Millisecond

// Rules is the content of RulesFile.
type Rules struct {
	// Routes maps a route id to the firewall the guard evaluates for it.
	Routes map[string]*agentprotocol.Firewall `json:"routes"`
	// Geo names the GeoIP database file in Dir ("" when the server has none:
	// country conditions then never match, see Server.compile).
	Geo string `json:"geo,omitempty"`
}

// Hash fingerprints an encoded Rules document; /v1/ready reports it.
func Hash(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:8])
}

// Server answers Traefik's forwardAuth requests.
type Server struct {
	dir string
	log *slog.Logger
	// now is the clock the ban expiry reads (tests replace it).
	now func() time.Time

	mu       sync.RWMutex
	matchers map[string]*agentprotocol.FirewallMatcher
	geo      *maxminddb.Reader
	geoName  string
	hash     string
	// rulesStamp is the size and modification time of the loaded rules file.
	rulesStamp string
	// bans holds the auto-ban state of the routes that have a policy, by
	// route id; bansHash fingerprints the loaded bans file and bansStamp is
	// its size and modification time.
	bans      map[string]*routeBans
	bansHash  string
	bansStamp string

	blocked []byte
}

// routeBans is the auto-ban state of one route: addresses never banned and
// the active bans by BanPrefix key.
type routeBans struct {
	allow []netip.Prefix
	bans  map[netip.Prefix]time.Time
}

// allows reports whether addr (already cleaned) is on the allow list.
func (b *routeBans) allows(addr netip.Addr) bool {
	for _, p := range b.allow {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}

// banned reports whether addr's ban key is banned at now.
func (b *routeBans) banned(addr netip.Addr, now time.Time) bool {
	exp, ok := b.bans[agentprotocol.BanPrefix(addr)]
	return ok && exp.After(now)
}

// NewServer returns a server reading dir.
func NewServer(dir string, log *slog.Logger) (*Server, error) {
	page, err := pages.Render(pages.Page{Kind: "403"})
	if err != nil {
		return nil, err
	}
	return &Server{
		dir: dir, log: log, now: time.Now, blocked: page,
		matchers: map[string]*agentprotocol.FirewallMatcher{}, bans: map[string]*routeBans{},
	}, nil
}

// Reload reads the rules (and the GeoIP database they name) and the bans
// when their files changed.
func (s *Server) Reload() error {
	return errors.Join(s.reloadRules(), s.reloadBans())
}

// reloadRules reads the rules file when it changed. A file that does not
// parse keeps the loaded rules: the agent rewrites it whole and waits for
// /v1/ready, so a half-written file is simply read again on the next tick.
func (s *Server) reloadRules() error {
	path := filepath.Join(s.dir, RulesFile)
	st, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	stamp := fmt.Sprintf("%d/%d", st.Size(), st.ModTime().UnixNano())
	s.mu.RLock()
	same := stamp == s.rulesStamp
	s.mu.RUnlock()
	if same {
		return nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var r Rules
	if err := json.Unmarshal(raw, &r); err != nil {
		return fmt.Errorf("parse rules: %w", err)
	}
	geo, geoName, err := s.openGeo(r.Geo)
	if err != nil {
		// Load the rules without countries now, and read the file again on the
		// next tick (no stamp recorded).
		s.log.Warn("guard: open geoip database", "file", r.Geo, "err", err)
		stamp = ""
	}
	matchers := make(map[string]*agentprotocol.FirewallMatcher, len(r.Routes))
	for id, fw := range r.Routes {
		m, err := compile(fw, geo != nil)
		if err != nil {
			return fmt.Errorf("route %s: %w", id, err)
		}
		matchers[id] = m
	}
	s.mu.Lock()
	var old *maxminddb.Reader
	if s.geo != nil && s.geo != geo {
		old = s.geo
	}
	s.geo, s.geoName = geo, geoName
	s.matchers, s.hash, s.rulesStamp = matchers, Hash(raw), stamp
	s.mu.Unlock()
	if old != nil {
		_ = old.Close() // under no lookup: lookups hold the read lock (check)
	}
	s.removeStaleGeo(geoName)
	s.log.Info("guard: rules loaded", "routes", len(matchers), "geo", geoName != "", "hash", Hash(raw))
	return nil
}

// reloadBans reads the bans file when it changed. Like the rules, a file that
// does not parse keeps the loaded state, and a missing one leaves it empty.
// Expired bans and entries that are not a ban key are left out.
func (s *Server) reloadBans() error {
	path := filepath.Join(s.dir, BansFile)
	st, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	stamp := fmt.Sprintf("%d/%d", st.Size(), st.ModTime().UnixNano())
	s.mu.RLock()
	same := stamp == s.bansStamp
	s.mu.RUnlock()
	if same {
		return nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var doc agentprotocol.GuardBans
	if err := json.Unmarshal(raw, &doc); err != nil {
		return fmt.Errorf("parse bans: %w", err)
	}
	now := s.now()
	bans := make(map[string]*routeBans, len(doc.Services))
	active := 0
	for id, rb := range doc.Services {
		out := &routeBans{allow: compileAllow(rb.Allow, s.log), bans: make(map[netip.Prefix]time.Time, len(rb.Bans))}
		for _, b := range rb.Bans {
			p, err := netip.ParsePrefix(b.Prefix)
			if err != nil || p != agentprotocol.BanPrefix(p.Addr()) {
				s.log.Warn("guard: ignoring ban that is not a ban key", "route", id, "prefix", b.Prefix)
				continue
			}
			if b.ExpiresAt.After(now) {
				out.bans[p] = b.ExpiresAt
				active++
			}
		}
		bans[id] = out
	}
	s.mu.Lock()
	s.bans, s.bansHash, s.bansStamp = bans, Hash(raw), stamp
	s.mu.Unlock()
	s.log.Info("guard: bans loaded", "routes", len(bans), "bans", active, "hash", Hash(raw))
	return nil
}

// compileAllow parses an allow list into masked prefixes (a bare address is a
// host prefix, an IPv4-mapped IPv6 prefix of at least /96 is the IPv4 one, as
// agentprotocol.AutoBan.Allows reads it); entries that do not parse are logged
// and left out.
func compileAllow(entries []string, log *slog.Logger) []netip.Prefix {
	var out []netip.Prefix
	for _, e := range entries {
		p, err := netip.ParsePrefix(e)
		if err != nil {
			a, aerr := netip.ParseAddr(e)
			if aerr != nil {
				log.Warn("guard: ignoring allow entry", "entry", e)
				continue
			}
			p = netip.PrefixFrom(a, a.BitLen())
		}
		if a := p.Addr(); a.Is4In6() && p.Bits() >= 96 {
			p = netip.PrefixFrom(a.Unmap(), p.Bits()-96)
		}
		out = append(out, p.Masked())
	}
	return out
}

// openGeo opens name in dir, or returns the loaded reader when it is the same file.
func (s *Server) openGeo(name string) (*maxminddb.Reader, string, error) {
	if name == "" || strings.ContainsAny(name, `/\`) {
		return nil, "", nil
	}
	s.mu.RLock()
	cur, curName := s.geo, s.geoName
	s.mu.RUnlock()
	if name == curName && cur != nil {
		return cur, name, nil
	}
	r, err := maxminddb.Open(filepath.Join(s.dir, name))
	if err != nil {
		return nil, "", err
	}
	return r, name, nil
}

// removeStaleGeo deletes the GeoIP files a newer database replaced.
func (s *Server) removeStaleGeo(keep string) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if n := e.Name(); strings.HasPrefix(n, "geo-") && strings.HasSuffix(n, ".mmdb") && n != keep {
			_ = os.Remove(filepath.Join(s.dir, n))
		}
	}
}

// compile prepares a firewall. Without a GeoIP database the rules that match
// countries are left out: a country condition cannot be evaluated, and
// failing it either way could block every visitor ("not in DE") or let a
// rule pass that should not. The control plane only accepts country rules on
// servers with geolocation, so this covers the first download.
func compile(fw *agentprotocol.Firewall, geo bool) (*agentprotocol.FirewallMatcher, error) {
	if fw == nil {
		return agentprotocol.CompileFirewall(nil)
	}
	if geo || !fw.UsesCountry() {
		return agentprotocol.CompileFirewall(fw)
	}
	kept := &agentprotocol.Firewall{}
	for _, r := range fw.Rules {
		country := false
		for _, c := range r.Conditions {
			country = country || c.Field == agentprotocol.FirewallFieldCountry
		}
		if !country {
			kept.Rules = append(kept.Rules, r)
		}
	}
	return agentprotocol.CompileFirewall(kept)
}

// ServeHTTP implements the guard's endpoints.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/v1/ready":
		s.ready(w, r)
	case strings.HasPrefix(r.URL.Path, "/v1/check/"):
		s.check(w, r, strings.TrimPrefix(r.URL.Path, "/v1/check/"))
	default:
		http.NotFound(w, r)
	}
}

// ready answers 200 once the loaded rules and bans have the hashes the agent
// asks for (rules=, bans=; either may be left out).
func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	hash, bansHash := s.hash, s.bansHash
	s.mu.RUnlock()
	q := r.URL.Query()
	if want := q.Get("rules"); want != "" && want != hash {
		http.Error(w, "rules "+hash, http.StatusServiceUnavailable)
		return
	}
	if want := q.Get("bans"); want != "" && want != bansHash {
		http.Error(w, "bans "+bansHash, http.StatusServiceUnavailable)
		return
	}
	_, _ = w.Write([]byte(hash))
}

// check decides one forwarded request. A route the guard does not know (a
// table being replaced) goes through: the routers only point at the guard
// for firewalls it was given before they went live. A route with an auto-ban
// policy is decided in order: allow list (never banned), active ban, then the
// firewall rules.
func (s *Server) check(w http.ResponseWriter, r *http.Request, route string) {
	req := request(r)
	s.mu.RLock()
	m := s.matchers[route]
	rb := s.bans[route]
	if m != nil && s.geo != nil && req.ClientIP.IsValid() {
		req.Country = country(s.geo, req.ClientIP)
	}
	s.mu.RUnlock()
	if m == nil {
		w.WriteHeader(http.StatusOK)
		return
	}
	if rb != nil && req.ClientIP.IsValid() {
		// An invalid visitor address fails open: nothing to ban it by.
		addr := req.ClientIP.WithZone("").Unmap()
		if !rb.allows(addr) && rb.banned(addr, s.now()) {
			s.deny(w, agentprotocol.FirewallRuleAutoBan)
			return
		}
	}
	if ruleID, action := m.Decide(req); action == agentprotocol.FirewallBlock {
		s.deny(w, ruleID)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// deny answers 403 with the page. The rule rides the 403 to the access log
// (forwardAuth passes the header of a refusal on), where the agent counts
// blocks by it.
func (s *Server) deny(w http.ResponseWriter, ruleID string) {
	w.Header().Set(agentprotocol.FirewallRuleHeader, ruleID)
	for k, v := range pages.SecurityHeaders() {
		w.Header().Set(k, v)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusForbidden)
	_, _ = w.Write(s.blocked)
}

// request rebuilds the visitor's request from Traefik's forwardAuth headers.
func request(r *http.Request) agentprotocol.FirewallRequest {
	out := agentprotocol.FirewallRequest{Method: r.Header.Get("X-Forwarded-Method"), Header: r.Header, Query: url.Values{}}
	var ip string
	if r.URL.Query().Get("client") == agentprotocol.GuardClientCloudflare {
		ip = r.Header.Get("Cf-Connecting-Ip")
	} else {
		// Traefik replaces X-Forwarded-For with the peer address (the
		// middleware does not trust the client's header).
		ip, _, _ = strings.Cut(r.Header.Get("X-Forwarded-For"), ",")
	}
	if a, err := netip.ParseAddr(strings.TrimSpace(ip)); err == nil {
		out.ClientIP = a
	}
	if u, err := url.ParseRequestURI(r.Header.Get("X-Forwarded-Uri")); err == nil {
		out.Path, out.Query = u.Path, u.Query()
	}
	if out.Path == "" {
		out.Path = "/"
	}
	return out
}

type countryRecord struct {
	Country struct {
		ISOCode string `maxminddb:"iso_code"`
	} `maxminddb:"country"`
}

func country(db *maxminddb.Reader, ip netip.Addr) string {
	ip = ip.Unmap()
	if ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() {
		return ""
	}
	res := db.Lookup(ip)
	if !res.Found() {
		return ""
	}
	var rec countryRecord
	if res.Decode(&rec) != nil {
		return ""
	}
	return strings.ToUpper(rec.Country.ISOCode)
}

// Run serves on listen until ctx ends, reloading dir every reloadEvery.
func Run(ctx context.Context, dir, listen string, log *slog.Logger) error {
	s, err := NewServer(dir, log)
	if err != nil {
		return err
	}
	if err := s.Reload(); err != nil {
		log.Warn("guard: load rules", "err", err)
	}
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: s, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 2 * time.Minute}
	go func() {
		t := time.NewTicker(reloadEvery)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if err := s.Reload(); err != nil {
					log.Warn("guard: reload rules", "err", err)
				}
			}
		}
	}()
	go func() {
		<-ctx.Done()
		// The run context is done; the shutdown gets its own short deadline.
		sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// Encode renders a Rules document as the agent writes it.
func Encode(r Rules) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(r); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// EncodeBans renders a bans document as the agent writes it. The bans of a
// route are ordered by prefix and the allow list is kept as given, so equal
// state encodes to equal bytes (and one Hash).
func EncodeBans(b agentprotocol.GuardBans) ([]byte, error) {
	out := agentprotocol.GuardBans{Services: make(map[string]agentprotocol.GuardRouteBans, len(b.Services))}
	for id, rb := range b.Services {
		rb.Bans = slices.SortedFunc(slices.Values(rb.Bans), func(x, y agentprotocol.GuardBan) int { return strings.Compare(x.Prefix, y.Prefix) })
		out.Services[id] = rb
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(out); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
