package proxy

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// certSpec describes a certificate the fake proxy serves.
type certSpec struct {
	cn        string
	issuerCN  string
	issuerOrg string
	dns       []string
	from, to  time.Time
}

// makeServedCert builds a certificate signed by a throwaway CA named after
// issuerCN/issuerOrg, ready for a tls.Config.
func makeServedCert(t *testing.T, s certSpec) tls.Certificate {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	caTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: s.issuerCN, Organization: orgs(s.issuerOrg)},
		NotBefore: s.from, NotAfter: s.to, IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	require.NoError(t, err)
	ca, err := x509.ParseCertificate(caDER)
	require.NoError(t, err)

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: s.cn}, DNSNames: s.dns, NotBefore: s.from, NotAfter: s.to}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, &key.PublicKey, caKey)
	require.NoError(t, err)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

func orgs(o string) []string {
	if o == "" {
		return nil
	}
	return []string{o}
}

// fakeProxyTLS serves the certificate chosen by SNI on a loopback port and
// records the server names it was asked for.
type fakeProxyTLS struct {
	addr string
	port int

	mu   sync.Mutex
	snis []string
}

func (f *fakeProxyTLS) asked() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.snis...)
}

func serveTLS(t *testing.T, bySNI map[string]tls.Certificate, fallback tls.Certificate) *fakeProxyTLS {
	t.Helper()
	f := &fakeProxyTLS{}
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		MinVersion: tls.VersionTLS12,
		GetCertificate: func(h *tls.ClientHelloInfo) (*tls.Certificate, error) {
			f.mu.Lock()
			f.snis = append(f.snis, h.ServerName)
			f.mu.Unlock()
			if c, ok := bySNI[h.ServerName]; ok {
				return &c, nil
			}
			return &fallback, nil
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	f.addr = ln.Addr().String()
	f.port = ln.Addr().(*net.TCPAddr).Port
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				_ = c.(*tls.Conn).Handshake()
			}()
		}
	}()
	return f
}

// noDNS makes every name lookup fail at once, so a test never reaches whatever
// the machine's resolver (search domains, VPN, wildcard records) would answer
// for the proxy container's name.
func noDNS(t *testing.T) {
	t.Helper()
	prev := net.DefaultResolver
	net.DefaultResolver = &net.Resolver{
		PreferGo: true,
		Dial: func(context.Context, string, string) (net.Conn, error) {
			return nil, errors.New("dns disabled in tests")
		},
	}
	t.Cleanup(func() { net.DefaultResolver = prev })
}

func closedPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := ln.Addr().(*net.TCPAddr).Port
	require.NoError(t, ln.Close())
	return port
}

func TestServedCertOK(t *testing.T) {
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	valid := &x509.Certificate{
		Subject: pkix.Name{CommonName: "*.example.com"}, DNSNames: []string{"*.example.com", "example.com"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour),
	}
	with := func(mut func(*x509.Certificate)) *x509.Certificate {
		c := *valid
		mut(&c)
		return &c
	}
	cases := []struct {
		name string
		leaf *x509.Certificate
		host string
		want bool
	}{
		{"valid wildcard cert for its own name", valid, "*.example.com", true},
		{"valid cert for one label under the wildcard", valid, "app.example.com", true},
		{"apex name in the SANs", valid, "example.com", true},
		{"no certificate", nil, "app.example.com", false},
		{"traefik's self-signed default", with(func(c *x509.Certificate) { c.Subject.CommonName = traefikDefaultCN }), "app.example.com", false},
		{"not valid yet", with(func(c *x509.Certificate) { c.NotBefore = now.Add(time.Minute) }), "app.example.com", false},
		{"expired", with(func(c *x509.Certificate) { c.NotAfter = now.Add(-time.Second) }), "app.example.com", false},
		{"different name", valid, "app.other.org", false},
		{"two labels under the wildcard", valid, "a.b.example.com", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			require.Equal(t, c.want, servedCertOK(c.leaf, c.host, now))
		})
	}
}

func TestIssuerName(t *testing.T) {
	require.Equal(t, "R11", issuerName(&x509.Certificate{Issuer: pkix.Name{CommonName: "R11", Organization: []string{"Let's Encrypt"}}}))
	require.Equal(t, "Let's Encrypt", issuerName(&x509.Certificate{Issuer: pkix.Name{Organization: []string{"Let's Encrypt", "x"}}}))
	require.Empty(t, issuerName(&x509.Certificate{}))
}

func TestAcmeTargets(t *testing.T) {
	cf := agentprotocol.DNSCloudflare
	cfg := agentprotocol.ProxyConfig{
		DNSResolvers: cfResolver(),
		WildcardCerts: []agentprotocol.WildcardCert{
			{ID: "w-run", Provider: cf, Domain: "example.com"},
			{ID: "w-norun", Provider: agentprotocol.DNSHetzner, Domain: "example.org"}, // its resolver is not configured
		},
	}
	l4 := agentprotocol.Route{ServiceID: svcDB, Upstreams: []string{"x:1"}, RouteExt: agentprotocol.RouteExt{Protocol: agentprotocol.ProtoTCP, ListenPort: 5432}}
	web := ruleRoute(
		rule(domA, "covered.example.com", func(d *agentprotocol.DomainRule) { d.DNSResolver = cf }), // under w-run
		rule(domB, "own.other.net", func(d *agentprotocol.DomainRule) { d.DNSResolver = cf }),
		rule(domC, "custom.other.net", func(d *agentprotocol.DomainRule) { d.DNSResolver, d.TLSMode = cf, agentprotocol.TLSCustom }),
		rule("d-none", "nontls.other.net", func(d *agentprotocol.DomainRule) { d.DNSResolver, d.TLSMode = cf, agentprotocol.TLSNone }),
		rule("d-plain", "plain.other.net", nil), // HTTP-01
		rule("d-wild", "*.preview.other.net", func(d *agentprotocol.DomainRule) { d.DNSResolver = cf }),
	)
	got := acmeTargets(cfg, []agentprotocol.Route{l4, web})

	require.Equal(t, []acmeTarget{
		{kind: agentprotocol.AcmeKindWildcard, id: "w-run", sni: "nc-probe.example.com", covered: "*.example.com", provider: cf},
		{kind: agentprotocol.AcmeKindDomain, id: domB, sni: "own.other.net", covered: "own.other.net", provider: cf},
		{kind: agentprotocol.AcmeKindDomain, id: "d-wild", sni: "nc-probe.preview.other.net", covered: "*.preview.other.net", provider: cf},
	}, got, "wildcards whose resolver runs, plus uncovered DNS-01 domains; wildcard hosts are probed under nc-probe")
}

func TestCoveredByWildcardRespectsGeneratedDomains(t *testing.T) {
	ws := []agentprotocol.WildcardCert{{ID: "w", Domain: "example.com"}, {ID: "g", Domain: "gen.example.com", CoverGenerated: true}}
	require.True(t, coveredByWildcard(ws, agentprotocol.DomainRule{Host: "app.example.com"}))
	require.False(t, coveredByWildcard(ws, agentprotocol.DomainRule{Host: "app.other.org"}))
	require.False(t, coveredByWildcard(ws, agentprotocol.DomainRule{Host: "svc.example.com", Generated: true}),
		"a generated domain needs a wildcard that opted in to cover generated names")
	require.True(t, coveredByWildcard(ws, agentprotocol.DomainRule{Host: "svc.gen.example.com", Generated: true}))
}

func TestFetchServedCert(t *testing.T) {
	now := time.Now()
	good := makeServedCert(t, certSpec{cn: "app", issuerCN: "Fake CA", dns: []string{"app.example.com"}, from: now.Add(-time.Hour), to: now.Add(time.Hour)})
	srv := serveTLS(t, map[string]tls.Certificate{"app.example.com": good}, makeServedCert(t, certSpec{cn: traefikDefaultCN, issuerCN: traefikDefaultCN, from: now.Add(-time.Hour), to: now.Add(time.Hour)}))

	leaf, err := fetchServedCert(context.Background(), []string{srv.addr}, "app.example.com")
	require.NoError(t, err)
	require.Equal(t, []string{"app.example.com"}, leaf.DNSNames)
	require.Equal(t, "Fake CA", leaf.Issuer.CommonName)
	require.Equal(t, []string{"app.example.com"}, srv.asked(), "the SNI is the name asked for")

	// Another name gets the default certificate: it is read, not trusted.
	leaf, err = fetchServedCert(context.Background(), []string{srv.addr}, "other.example.com")
	require.NoError(t, err)
	require.Equal(t, traefikDefaultCN, leaf.Subject.CommonName)

	// The first address that answers wins; earlier refusals are skipped.
	dead := net.JoinHostPort("127.0.0.1", strconv.Itoa(closedPort(t)))
	leaf, err = fetchServedCert(context.Background(), []string{dead, srv.addr}, "app.example.com")
	require.NoError(t, err)
	require.Equal(t, "app", leaf.Subject.CommonName)

	// Nothing answers: the last error is returned.
	_, err = fetchServedCert(context.Background(), []string{dead}, "app.example.com")
	require.Error(t, err)
	require.Contains(t, err.Error(), "refused")

	_, err = fetchServedCert(context.Background(), nil, "app.example.com")
	require.EqualError(t, err, "no address to dial")

	noDNS(t)
	_, err = fetchServedCert(context.Background(), []string{"nebulactrl-traefik:443"}, "app.example.com")
	require.ErrorContains(t, err, "dns disabled in tests")

	// A cancelled context fails the dial instead of waiting.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = fetchServedCert(ctx, []string{srv.addr}, "app.example.com")
	require.Error(t, err)
}

func TestTLSAddrs(t *testing.T) {
	m, eng := storeManager(t, nil, nil)
	m.cfg.HTTPSPort = 8443

	// No edge IP known: the container's DNS name and the published port.
	require.Equal(t, []string{"nebulactrl-traefik:443", "127.0.0.1:8443"}, m.tlsAddrs(context.Background()))

	// The IP on the edge network goes first; the server's port override applies to the published port.
	eng.ctrs[agentprotocol.ProxyContainer].info.Networks = map[string]docker.ContainerNetwork{agentprotocol.EdgeNetwork: {IP: "172.20.0.3"}}
	m.appliedCfg = agentprotocol.ProxyConfig{HTTPSPort: 9443}
	require.Equal(t, []string{"172.20.0.3:443", "nebulactrl-traefik:443", "127.0.0.1:9443"}, m.tlsAddrs(context.Background()))

	// An IP on another network is not used.
	eng.ctrs[agentprotocol.ProxyContainer].info.Networks = map[string]docker.ContainerNetwork{"other": {IP: "10.9.9.9"}}
	require.Equal(t, []string{"nebulactrl-traefik:443", "127.0.0.1:9443"}, m.tlsAddrs(context.Background()))
}

func TestResolverErrors(t *testing.T) {
	long := strings.Repeat("x", 500)
	logs := strings.Join([]string{
		`time="x" level=info msg="plain text line"`,
		`{"level":"info","providerName":"nc-dns-cloudflare.acme","message":"fine"}`,
		`{"level":"error","providerName":"nc-dns-cloudflare.acme","message":"first failure"}`,
		`{"level":"error","providerName":"nc-dns-cloudflare.acme","message":"Unable to obtain ACME certificate","error":"cloudflare: invalid token"}`,
		`{"level":"error","providerName":"le.acme","msg":"http-01 failed"}`,
		`{"level":"error","providerName":"docker","message":"not an acme provider"}`,
		`{"level":"error","message":"no provider"}`,
		`{"level":"error","providerName":"nc-dns-hetzner.acme","message":"` + long + `"}`,
		`{broken json`,
		"",
	}, "\n")
	m, eng := storeManager(t, nil, nil)
	fe := &faultEngine{fleetEngine: eng, logs: logs}
	m.cfg.Engine = fe

	got := m.resolverErrors(context.Background())
	require.Equal(t, map[string]string{
		"nc-dns-cloudflare": "Unable to obtain ACME certificate: cloudflare: invalid token", // the newest line wins
		"le":                "http-01 failed",
		"nc-dns-hetzner":    strings.Repeat("x", 400) + "…",
	}, got)
	require.Equal(t, []docker.LogOptions{{Tail: acmeLogLines}}, fe.logOpts, "only the tail of the log is read")

	fe.set("logs", errors.New("container gone"))
	require.Empty(t, m.resolverErrors(context.Background()), "an unreadable log reports no errors rather than failing the poll")
}

func TestParseACMELogErrorFields(t *testing.T) {
	res, msg, ok := parseACMELogError("  " + `{"level":"error","providerName":"le.acme","msg":"from msg","error":"boom"}` + "  ")
	require.True(t, ok)
	require.Equal(t, "le", res)
	require.Equal(t, "from msg: boom", msg, "msg is the fallback of message, the error is appended")

	for _, line := range []string{
		`{"level":"warning","providerName":"le.acme","message":"x"}`,
		`{"level":"error","providerName":"traefik","message":"x"}`,
		`{"level":"error","message":"x"}`,
		`[1,2]`,
		``,
	} {
		_, _, ok := parseACMELogError(line)
		require.False(t, ok, line)
	}
}

// acmeObserver is a manager that applied a table with a running wildcard and one
// DNS-01 domain, over a fake proxy whose TLS port is srv.
type acmeObserver struct {
	m     *Manager
	eng   *faultEngine
	srv   *fakeProxyTLS
	execs int
	mu    sync.Mutex
}

func (o *acmeObserver) execCount() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.execs
}

func newACMEObserver(t *testing.T, srv *fakeProxyTLS, httpsPort int) *acmeObserver {
	t.Helper()
	noDNS(t)
	files := map[string]string{
		cfFile: acmeJSON("nc-dns-cloudflare", []string{"own.other.net"}),
	}
	m, inner := storeManager(t, files, nil)
	o := &acmeObserver{srv: srv}
	base := inner.execFn
	inner.execFn = func(id string, argv []string) (string, int, error) {
		o.mu.Lock()
		o.execs++
		o.mu.Unlock()
		return base(id, argv)
	}
	o.eng = &faultEngine{fleetEngine: inner}
	m.cfg.Engine = o.eng
	m.cfg.HTTPSPort = httpsPort
	cf := agentprotocol.DNSCloudflare
	m.appliedCfg = agentprotocol.ProxyConfig{
		DNSResolvers:  cfResolver(),
		WildcardCerts: []agentprotocol.WildcardCert{{ID: "w1", Provider: cf, Domain: "example.com"}},
	}
	m.routes = []agentprotocol.Route{ruleRoute(
		rule(domB, "own.other.net", func(d *agentprotocol.DomainRule) { d.DNSResolver = cf }),
	)}
	o.m = m
	return o
}

func TestRefreshAcmeIfDueObservesServedCertificates(t *testing.T) {
	now := time.Now()
	notAfter := now.Add(30 * 24 * time.Hour).Truncate(time.Second)
	wild := makeServedCert(t, certSpec{cn: "*.example.com", issuerCN: "Fake CA", dns: []string{"*.example.com"}, from: now.Add(-time.Hour), to: notAfter})
	srv := serveTLS(t, map[string]tls.Certificate{"nc-probe.example.com": wild},
		makeServedCert(t, certSpec{cn: traefikDefaultCN, issuerCN: traefikDefaultCN, from: now.Add(-time.Hour), to: now.Add(time.Hour)}))
	o := newACMEObserver(t, srv, srv.port)
	o.eng.logs = `{"level":"error","providerName":"nc-dns-cloudflare.acme","message":"Unable to obtain ACME certificate","error":"cloudflare: invalid token"}` + "\n"

	o.m.refreshAcmeIfDue(context.Background())

	na := notAfter.UTC()
	require.Equal(t, []agentprotocol.AcmeCertStatus{
		{ // "domain" sorts before "wildcard"
			Kind: agentprotocol.AcmeKindDomain, ID: domB, Status: agentprotocol.AcmeStatusError,
			Message:   "Unable to obtain ACME certificate: cloudflare: invalid token",
			IssuedVia: agentprotocol.AcmeIssuedDNS01,
		},
		{
			Kind: agentprotocol.AcmeKindWildcard, ID: "w1", Status: agentprotocol.AcmeStatusActive,
			NotAfter: &na, Issuer: "Fake CA", IssuedVia: agentprotocol.AcmeIssuedDNS01,
		},
	}, o.m.AcmeStatuses())
	require.ElementsMatch(t, []string{"nc-probe.example.com", "own.other.net"}, srv.asked())

	// Something is not issued, so the poll stays fast: a second call inside
	// acmePendingEvery reads nothing again.
	execs, asked := o.execCount(), len(srv.asked())
	o.m.refreshAcmeIfDue(context.Background())
	require.Equal(t, execs, o.execCount())
	require.Len(t, srv.asked(), asked)

	// An Apply resets checkedAt: the next call observes again.
	o.m.mu.Lock()
	o.m.acme.checkedAt = time.Time{}
	o.m.mu.Unlock()
	o.m.refreshAcmeIfDue(context.Background())
	require.Greater(t, o.execCount(), execs)
	require.Greater(t, len(srv.asked()), asked)
}

func TestRefreshAcmeIfDueSteadyIntervalOnceEverythingIsActive(t *testing.T) {
	now := time.Now()
	wild := makeServedCert(t, certSpec{cn: "*.example.com", issuerCN: "Fake CA", dns: []string{"*.example.com"}, from: now.Add(-time.Hour), to: now.Add(time.Hour)})
	own := makeServedCert(t, certSpec{cn: "own", issuerOrg: "Fake Org", dns: []string{"own.other.net"}, from: now.Add(-time.Hour), to: now.Add(time.Hour)})
	srv := serveTLS(t, map[string]tls.Certificate{"nc-probe.example.com": wild, "own.other.net": own}, wild)
	o := newACMEObserver(t, srv, srv.port)

	o.m.refreshAcmeIfDue(context.Background())
	st := o.m.AcmeStatuses()
	require.Len(t, st, 2)
	for _, s := range st {
		require.Equal(t, agentprotocol.AcmeStatusActive, s.Status)
		require.Empty(t, s.Message)
	}
	require.Equal(t, "Fake Org", st[0].Issuer, "the organization names the issuer when there is no common name")
	o.m.mu.RLock()
	require.False(t, o.m.acme.pending)
	o.m.mu.RUnlock()

	// Everything is issued: a reading a minute old is not refreshed...
	execs := o.execCount()
	o.m.mu.Lock()
	o.m.acme.checkedAt = time.Now().Add(-time.Minute)
	o.m.mu.Unlock()
	o.m.refreshAcmeIfDue(context.Background())
	require.Equal(t, execs, o.execCount())

	// ...one older than acmeSteadyEvery is.
	o.m.mu.Lock()
	o.m.acme.checkedAt = time.Now().Add(-acmeSteadyEvery - time.Second)
	o.m.mu.Unlock()
	o.m.refreshAcmeIfDue(context.Background())
	require.Greater(t, o.execCount(), execs)
}

func TestRefreshAcmeReportsAnUnreadableTLSPortAsPending(t *testing.T) {
	o := newACMEObserver(t, nil, closedPort(t))
	o.m.refreshAcmeIfDue(context.Background())

	got := o.m.AcmeStatuses()
	require.Len(t, got, 2)
	for _, s := range got {
		require.Equal(t, agentprotocol.AcmeStatusPending, s.Status)
		require.Contains(t, s.Message, "The proxy's TLS port could not be read: ")
		require.Nil(t, s.NotAfter)
	}
	o.m.mu.RLock()
	require.True(t, o.m.acme.pending)
	o.m.mu.RUnlock()
}

func TestRefreshAcmeWaitsForTheDefaultCertificate(t *testing.T) {
	now := time.Now()
	def := makeServedCert(t, certSpec{cn: traefikDefaultCN, issuerCN: traefikDefaultCN, from: now.Add(-time.Hour), to: now.Add(time.Hour)})
	srv := serveTLS(t, nil, def)
	o := newACMEObserver(t, srv, srv.port)

	o.m.refreshAcmeIfDue(context.Background())
	for _, s := range o.m.AcmeStatuses() {
		require.Equal(t, agentprotocol.AcmeStatusPending, s.Status)
		require.Equal(t, "Waiting for the DNS-01 certificate; the proxy serves its default certificate meanwhile.", s.Message)
	}
}

func TestRefreshAcmeIfDueClearsTheStateWithoutTargets(t *testing.T) {
	m, _ := storeManager(t, nil, nil)
	m.acme = acmeState{statuses: []agentprotocol.AcmeCertStatus{{Kind: agentprotocol.AcmeKindWildcard, ID: "gone"}}, pending: true}
	require.Len(t, m.AcmeStatuses(), 1)

	m.refreshAcmeIfDue(context.Background())
	require.Empty(t, m.AcmeStatuses(), "a removed wildcard stops being reported")
	require.False(t, m.acme.pending)
}

func TestRefreshAcmeIfDueSkipsWhileARefreshRuns(t *testing.T) {
	o := newACMEObserver(t, nil, closedPort(t))
	o.m.acmeMu.Lock()
	defer o.m.acmeMu.Unlock()
	o.m.refreshAcmeIfDue(context.Background())
	require.Empty(t, o.m.AcmeStatuses())
	require.Zero(t, o.execCount())
}

func TestAcmeStatusesIsACopy(t *testing.T) {
	m, _ := storeManager(t, nil, nil)
	m.acme = acmeState{statuses: []agentprotocol.AcmeCertStatus{{ID: "a", Status: "active"}}}
	got := m.AcmeStatuses()
	got[0].Status = "tampered"
	require.Equal(t, "active", m.AcmeStatuses()[0].Status)
}
