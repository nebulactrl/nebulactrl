// Package guard is the edge guard: the forwardAuth server Traefik asks before
// it forwards a request of a route whose firewall the compiled routers cannot
// express (country conditions, or visitor addresses that arrive in
// Cf-Connecting-IP). It runs as `nebulactrl-agent guard` in a sidecar
// container that shares the proxy container's network namespace, so Traefik
// reaches it on 127.0.0.1:agentprotocol.GuardPort and nothing else can.
//
// The agent copies its own binary, the rules (RulesFile) and the GeoIP
// database into the container's Dir; the server reloads the rules and the
// database when their files change. It never talks to the agent.
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

	mu       sync.RWMutex
	matchers map[string]*agentprotocol.FirewallMatcher
	geo      *maxminddb.Reader
	geoName  string
	hash     string
	// rulesStamp is the size and modification time of the loaded rules file.
	rulesStamp string

	blocked []byte
}

// NewServer returns a server reading dir.
func NewServer(dir string, log *slog.Logger) (*Server, error) {
	page, err := pages.Render(pages.Page{Kind: "403"})
	if err != nil {
		return nil, err
	}
	return &Server{dir: dir, log: log, matchers: map[string]*agentprotocol.FirewallMatcher{}, blocked: page}, nil
}

// Reload reads the rules (and the GeoIP database they name) when the rules
// file changed. A file that does not parse keeps the loaded rules: the agent
// rewrites it whole and waits for /v1/ready, so a half-written file is
// simply read again on the next tick.
func (s *Server) Reload() error {
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

// ready answers 200 once the loaded rules have the hash the agent asks for.
func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	hash := s.hash
	s.mu.RUnlock()
	if want := r.URL.Query().Get("rules"); want != "" && want != hash {
		http.Error(w, "rules "+hash, http.StatusServiceUnavailable)
		return
	}
	_, _ = w.Write([]byte(hash))
}

// check decides one forwarded request. A route the guard does not know (a
// table being replaced) goes through: the routers only point at the guard
// for firewalls it was given before they went live.
func (s *Server) check(w http.ResponseWriter, r *http.Request, route string) {
	req := request(r)
	s.mu.RLock()
	m := s.matchers[route]
	if m != nil && s.geo != nil && req.ClientIP.IsValid() {
		req.Country = country(s.geo, req.ClientIP)
	}
	s.mu.RUnlock()
	if m == nil {
		w.WriteHeader(http.StatusOK)
		return
	}
	if _, action := m.Decide(req); action == agentprotocol.FirewallBlock {
		for k, v := range pages.SecurityHeaders() {
			w.Header().Set(k, v)
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write(s.blocked)
		return
	}
	w.WriteHeader(http.StatusOK)
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
