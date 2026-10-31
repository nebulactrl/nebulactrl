package proxy

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Observed state of the DNS-01 certificates. The agent asks the proxy what it
// serves: a TLS handshake with the SNI of a wildcard (or of a domain that uses
// a DNS-01 resolver) shows the certificate Traefik picked, so the status and
// expiry come from the certificate itself and need no parsing of acme.json. A
// certificate that is not there yet is "pending"; when Traefik's own log has an
// error for the resolver, the status is "error" with that message.

const (
	// acmePendingEvery polls while something is not issued yet; acmeSteadyEvery
	// once everything is active (the certificate only changes on renewal).
	acmePendingEvery = 30 * time.Second
	acmeSteadyEvery  = 10 * time.Minute
	acmeDialTimeout  = 3 * time.Second
	acmeLogLines     = 400
	traefikDefaultCN = "TRAEFIK DEFAULT CERT"
)

// acmeState is the manager's cached view of the DNS-01 certificates.
type acmeState struct {
	statuses  []agentprotocol.AcmeCertStatus
	checkedAt time.Time
	pending   bool
}

// AcmeStatuses is the observed DNS-01 certificate state for heartbeats; empty
// when no wildcard or DNS-01 domain is configured.
func (m *Manager) AcmeStatuses() []agentprotocol.AcmeCertStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return append([]agentprotocol.AcmeCertStatus(nil), m.acme.statuses...)
}

// acmeTarget is one certificate the proxy should serve.
type acmeTarget struct {
	kind, id, sni, covered, provider string
	// storeOnly: the host names a DNS-01 resolver the proxy does not run yet, so
	// it is still served over HTTP-01 and only the storage holding its
	// certificate is reported (no status: nothing waits for DNS-01).
	storeOnly bool
}

// acmeTargets lists what to observe: every wildcard certificate and every
// route domain that uses a DNS-01 resolver (or is a wildcard host) and is not
// already covered by a wildcard certificate. A domain that names a resolver the
// proxy does not run yet is listed store-only, so the control plane can say
// which certificate it is served with meanwhile.
func acmeTargets(cfg agentprotocol.ProxyConfig, routes []agentprotocol.Route) []acmeTarget {
	var out []acmeTarget
	for _, w := range cfg.WildcardCerts {
		if !cfg.HasDNSResolver(w.Provider) {
			continue
		}
		out = append(out, acmeTarget{kind: agentprotocol.AcmeKindWildcard, id: w.ID, sni: wildcardSNI(w), covered: "*." + w.Domain, provider: w.Provider})
	}
	for _, r := range routes {
		if r.IsL4() {
			continue
		}
		for _, d := range r.Domains {
			if d.TLSMode == agentprotocol.TLSNone || d.TLSMode == agentprotocol.TLSCustom || d.DNSResolver == "" {
				continue
			}
			if coveredByWildcard(cfg.WildcardCerts, d) {
				continue
			}
			t := acmeTarget{kind: agentprotocol.AcmeKindDomain, id: d.ID, sni: agentprotocol.ProbeHost(d.Host), covered: d.Host, provider: d.DNSResolver}
			if !cfg.HasDNSResolver(d.DNSResolver) {
				if d.Tunnel {
					continue // Cloudflare ends a tunnel name's TLS: no certificate at the origin
				}
				t.storeOnly = true
			}
			out = append(out, t)
		}
	}
	return out
}

func coveredByWildcard(ws []agentprotocol.WildcardCert, d agentprotocol.DomainRule) bool {
	for _, w := range ws {
		if agentprotocol.WildcardCovers(w, d.Host) && (!d.Generated || w.CoverGenerated) {
			return true
		}
	}
	return false
}

// refreshAcmeIfDue re-observes the DNS-01 certificates; the health loop calls
// it. It polls every acmePendingEvery while anything is not active and every
// acmeSteadyEvery otherwise.
func (m *Manager) refreshAcmeIfDue(ctx context.Context) {
	if !m.acmeMu.TryLock() {
		return
	}
	defer m.acmeMu.Unlock()
	m.mu.RLock()
	cfg, routes, at, pending := cloneProxyConfig(m.appliedCfg), cloneRoutes(m.routes), m.acme.checkedAt, m.acme.pending
	m.mu.RUnlock()
	targets := acmeTargets(cfg, routes)
	if len(targets) == 0 {
		m.mu.Lock()
		m.acme = acmeState{}
		m.mu.Unlock()
		return
	}
	every := acmeSteadyEvery
	if pending || at.IsZero() {
		every = acmePendingEvery
	}
	if time.Since(at) < every {
		return
	}
	m.refreshAcme(ctx, cfg, targets)
}

func (m *Manager) refreshAcme(ctx context.Context, cfg agentprotocol.ProxyConfig, targets []acmeTarget) {
	var addrs []string
	var logErrs map[string]string
	for _, t := range targets {
		if !t.storeOnly {
			addrs, logErrs = m.tlsAddrs(ctx), m.resolverErrors(ctx)
			break
		}
	}
	stores, storesOK := m.readACMEStores(ctx, cfg)
	m.mu.RLock()
	previous := make(map[string]string, len(m.acme.statuses))
	for _, st := range m.acme.statuses {
		previous[st.Kind+"/"+st.ID] = st.IssuedVia
	}
	m.mu.RUnlock()
	now := time.Now()
	statuses := make([]agentprotocol.AcmeCertStatus, 0, len(targets))
	pending := false
	for _, t := range targets {
		st := agentprotocol.AcmeCertStatus{Kind: t.kind, ID: t.id}
		// Which storage holds the certificate: read from the files, or what the
		// last good reading said while they cannot be read.
		switch {
		case t.kind == agentprotocol.AcmeKindWildcard:
			st.IssuedVia = agentprotocol.AcmeIssuedDNS01
		case storesOK:
			st.IssuedVia = stores.issuedVia(t.covered)
		default:
			st.IssuedVia = previous[t.kind+"/"+t.id]
		}
		if t.storeOnly {
			statuses = append(statuses, st)
			continue
		}
		st.Status = agentprotocol.AcmeStatusPending
		leaf, err := fetchServedCert(ctx, addrs, t.sni)
		switch {
		case err != nil:
			st.Message = "The proxy's TLS port could not be read: " + trimErr(err.Error())
		case servedCertOK(leaf, t.covered, now):
			st.Status = agentprotocol.AcmeStatusActive
			na := leaf.NotAfter.UTC()
			st.NotAfter = &na
			st.Issuer = issuerName(leaf)
		default:
			st.Message = "Waiting for the DNS-01 certificate; the proxy serves its default certificate meanwhile."
		}
		if st.Status != agentprotocol.AcmeStatusActive {
			if msg := logErrs[agentprotocol.DNSResolverName(t.provider)]; msg != "" {
				st.Status, st.Message = agentprotocol.AcmeStatusError, msg
			}
			pending = true
		}
		statuses = append(statuses, st)
	}
	sort.Slice(statuses, func(i, j int) bool {
		if statuses[i].Kind != statuses[j].Kind {
			return statuses[i].Kind < statuses[j].Kind
		}
		return statuses[i].ID < statuses[j].ID
	})
	m.mu.Lock()
	m.acme = acmeState{statuses: statuses, checkedAt: now, pending: pending}
	m.mu.Unlock()
}

// servedCertOK reports whether leaf is a real certificate for name: it covers
// it, is within its validity and is not Traefik's self-signed default.
func servedCertOK(leaf *x509.Certificate, name string, now time.Time) bool {
	if leaf == nil || leaf.Subject.CommonName == traefikDefaultCN {
		return false
	}
	if now.Before(leaf.NotBefore) || now.After(leaf.NotAfter) {
		return false
	}
	return certCovers(leaf.DNSNames, name)
}

func issuerName(leaf *x509.Certificate) string {
	if cn := leaf.Issuer.CommonName; cn != "" {
		return cn
	}
	if len(leaf.Issuer.Organization) > 0 {
		return leaf.Issuer.Organization[0]
	}
	return ""
}

// tlsAddrs lists the addresses of the proxy's HTTPS port as seen from the
// agent: the container's IP on the edge network, its DNS name (an agent in a
// container) and the host's published port.
func (m *Manager) tlsAddrs(ctx context.Context) []string {
	_, httpsPort := m.effectivePorts(m.appliedConfig())
	var addrs []string
	ictx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if info, err := m.cfg.Engine.ContainerInspect(ictx, agentprotocol.ProxyContainer); err == nil {
		if ep, ok := info.Networks[agentprotocol.EdgeNetwork]; ok && ep.IP != "" {
			addrs = append(addrs, net.JoinHostPort(ep.IP, "443"))
		}
	}
	addrs = append(addrs,
		net.JoinHostPort(agentprotocol.ProxyContainer, "443"),
		net.JoinHostPort("127.0.0.1", strconv.Itoa(httpsPort)),
	)
	return addrs
}

// fetchServedCert returns the leaf certificate the proxy presents for sni, from
// the first address that answers. Verification is off on purpose: this reads
// the certificate, it does not trust it.
func fetchServedCert(ctx context.Context, addrs []string, sni string) (*x509.Certificate, error) {
	var last error
	for _, a := range addrs {
		d := tls.Dialer{
			NetDialer: &net.Dialer{Timeout: acmeDialTimeout},
			Config:    &tls.Config{ServerName: sni, InsecureSkipVerify: true, MinVersion: tls.VersionTLS12}, //nolint:gosec // reads the served certificate only
		}
		dctx, cancel := context.WithTimeout(ctx, acmeDialTimeout+time.Second)
		conn, err := d.DialContext(dctx, "tcp", a)
		cancel()
		if err != nil {
			last = err
			continue
		}
		tc, ok := conn.(*tls.Conn)
		var leaf *x509.Certificate
		if ok {
			if certs := tc.ConnectionState().PeerCertificates; len(certs) > 0 {
				leaf = certs[0]
			}
		}
		_ = conn.Close()
		if leaf != nil {
			return leaf, nil
		}
		last = errors.New("the proxy presented no certificate")
	}
	if last == nil {
		last = errors.New("no address to dial")
	}
	return nil, last
}

// resolverErrors scans the tail of Traefik's log for ACME errors and returns
// the newest message per resolver name. Traefik logs JSON; the lines that matter
// carry level "error" and providerName "<resolver>.acme".
func (m *Manager) resolverErrors(ctx context.Context) map[string]string {
	out := map[string]string{}
	lctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	rc, err := m.cfg.Engine.ContainerLogs(lctx, agentprotocol.ProxyContainer, docker.LogOptions{Tail: acmeLogLines})
	if err != nil {
		return out
	}
	defer rc.Close()
	sc := bufio.NewScanner(rc)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		name, msg, ok := parseACMELogError(sc.Text())
		if ok {
			out[name] = msg
		}
	}
	return out
}

// parseACMELogError extracts the resolver name and message of an ACME error
// line of Traefik's JSON log.
func parseACMELogError(line string) (resolver, msg string, ok bool) {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "{") {
		return "", "", false
	}
	var l struct {
		Level    string `json:"level"`
		Message  string `json:"message"`
		Msg      string `json:"msg"`
		Error    string `json:"error"`
		Provider string `json:"providerName"`
	}
	if json.Unmarshal([]byte(line), &l) != nil || l.Level != "error" || l.Provider == "" {
		return "", "", false
	}
	resolver, found := strings.CutSuffix(l.Provider, ".acme")
	if !found {
		return "", "", false
	}
	text := l.Message
	if text == "" {
		text = l.Msg
	}
	if l.Error != "" {
		text += ": " + l.Error
	}
	return resolver, trimErr(text), true
}
