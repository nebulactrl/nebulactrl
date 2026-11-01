//go:build integration

package integration

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agent/proxy"
	"github.com/nebulactrl/nebulactrl/internal/agent/traffic"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

const (
	cfSvc       = "cfcfcfcf-cfcf-4fcf-8fcf-cfcfcfcfcfcf"
	cfLockedID  = "cfd00000-0000-4000-8000-000000000001"
	cfOpenID    = "cfd10000-0000-4000-8000-000000000002"
	cfLockedHos = "cf-locked.localhost"
	cfOpenHost  = "cf-open.localhost"
)

// cfClient requests https://host/ (or http) from the harness proxy's published
// ports, like a client that reaches Traefik directly. cert, when set, is the
// TLS client certificate.
type cfClient struct {
	cert *tls.Certificate
}

func (c cfClient) do(scheme, host string, hdr map[string]string) (int, string, http.Header, error) {
	port := proxyPorts.HTTP
	if scheme == "https" {
		port = proxyPorts.HTTPS
	}
	addr := net.JoinHostPort("127.0.0.1", fmt.Sprint(port))
	cfg := &tls.Config{InsecureSkipVerify: true, ServerName: host} //nolint:gosec // test-only self-signed default cert
	if c.cert != nil {
		cfg.Certificates = []tls.Certificate{*c.cert}
	}
	hc := &http.Client{
		Timeout: 8 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, network, addr)
			},
			TLSClientConfig:   cfg,
			DisableKeepAlives: true,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	req, err := http.NewRequest(http.MethodGet, scheme+"://"+host+"/", nil)
	if err != nil {
		return 0, "", nil, err
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	res, err := hc.Do(req)
	if err != nil {
		return 0, "", nil, err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<16))
	return res.StatusCode, strings.TrimSpace(string(b)), res.Header, nil
}

// get is do with the error turned into a test failure.
func (c cfClient) get(t *testing.T, scheme, host string, hdr map[string]string) (int, string) {
	t.Helper()
	code, body, _, err := c.do(scheme, host, hdr)
	require.NoError(t, err)
	return code, body
}

// traefikAccessLines returns the JSON access-log lines of the proxy container.
func traefikAccessLines(t *testing.T) []map[string]any {
	t.Helper()
	out, err := sh(context.Background(), "", "docker", "logs", "--tail", "400", agentprotocol.ProxyContainer)
	require.NoError(t, err)
	var lines []map[string]any
	sc := bufio.NewScanner(strings.NewReader(out))
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		var m map[string]any
		if json.Unmarshal(sc.Bytes(), &m) == nil && m["DownstreamStatus"] != nil {
			m["_raw"] = sc.Text()
			lines = append(lines, m)
		}
	}
	return lines
}

// waitAccessLine polls the access log until a line matches.
func waitAccessLine(t *testing.T, what string, match func(map[string]any) bool) map[string]any {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		lines := traefikAccessLines(t)
		for i := len(lines) - 1; i >= 0; i-- {
			if match(lines[i]) {
				return lines[i]
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("no access log line for %s in %d lines", what, len(lines))
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// cfTestPKI makes a CA and a client certificate it signed (Cloudflare's origin
// pull certificate stands in as this pair).
func cfTestPKI(t *testing.T, cn string) (caPEM string, client tls.Certificate) {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	caTpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: cn + " CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTpl, caTpl, &caKey.PublicKey, caKey)
	require.NoError(t, err)
	caCert, err := x509.ParseCertificate(caDER)
	require.NoError(t, err)

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: cn + " client"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, caCert, &key.PublicKey, caKey)
	require.NoError(t, err)
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})),
		tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// TestCloudflareOriginLock runs a locked route on the real Traefik:
//
//   - a peer outside the Cloudflare ranges gets 403 (https and http), while an
//     unlocked domain of the same route stays reachable;
//   - with the test client's own address in the ranges (it plays Cloudflare) the
//     request is admitted, the user's deny rule evaluates the visitor address
//     Cloudflare put at the end of X-Forwarded-For, and a forged left entry
//     neither admits nor denies anyone;
//   - Authenticated Origin Pulls: a handshake without the client certificate, or
//     with one from another CA, fails; the right one passes; port 80 is unaffected;
//   - Traefik's access log shows the whole X-Forwarded-For as ClientHost and the
//     traffic parser reads the visitor from its end;
//   - removing the lock restores plain behaviour.
func TestCloudflareOriginLock(t *testing.T) {
	requireTier(t, tierFull)
	eng := requireDocker(t)
	stopSharedAgent() // a leftover shared agent would fight over Traefik
	appFixture(t)
	ctx := context.Background()

	px, err := proxy.NewManager(proxy.Config{
		Engine: eng, AdminPort: proxyPorts.Admin, HTTPPort: proxyPorts.HTTP, HTTPSPort: proxyPorts.HTTPS,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = eng.ContainerRemove(context.Background(), agentprotocol.ProxyContainer, true, false) })

	upstream := fmt.Sprintf("nebulactrl-itest-cf-%d", time.Now().UnixNano()%1_000_000)
	table := func(lock *agentprotocol.OriginLock, requireCert bool, access *agentprotocol.Access) []agentprotocol.Route {
		locked := agentprotocol.DomainRule{ID: cfLockedID, Host: cfLockedHos, Options: agentprotocol.RouteOptions{Access: access}}
		if lock != nil {
			locked.OriginLocked, locked.RequireClientCert = true, requireCert
		}
		return []agentprotocol.Route{{
			ServiceID: cfSvc, Hosts: []string{cfLockedHos, cfOpenHost}, Upstreams: []string{upstream + ":8080"},
			RouteExt: agentprotocol.RouteExt{
				Domains:    []agentprotocol.DomainRule{locked, {ID: cfOpenID, Host: cfOpenHost}},
				OriginLock: lock,
			},
		}}
	}

	// The edge network exists once the proxy runs; the upstream joins it after.
	docRange := &agentprotocol.OriginLock{CloudflareCIDRs: []string{"192.0.2.0/24", "2001:db8::/32"}}
	require.NoError(t, px.Apply(table(docRange, false, nil)))
	_, err = sh(ctx, "", "docker", "run", "-d", "--name", upstream, "--network", agentprotocol.EdgeNetwork,
		"-e", "VERSION=cf-v1", appImageLocal)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = sh(context.Background(), "", "docker", "rm", "-f", upstream) })

	var c cfClient
	visitor := map[string]string{"X-Forwarded-For": "203.0.113.9", "CF-Connecting-IP": "203.0.113.9"}

	// 1. The peer (this test) is not a Cloudflare address: 403, whatever it claims.
	require.Eventually(t, func() bool {
		code, _, _, err := c.do("https", cfOpenHost, nil)
		return err == nil && code == http.StatusOK
	}, 30*time.Second, 300*time.Millisecond, "the unlocked domain of the route serves")
	code, body := c.get(t, "https", cfLockedHos, visitor)
	require.Equal(t, http.StatusForbidden, code)
	require.Equal(t, "Forbidden", body, "the 403 names nothing internal")
	code, _ = c.get(t, "http", cfLockedHos, visitor)
	require.Equal(t, http.StatusForbidden, code, "port 80 of a locked domain is locked too, not redirected")
	code, body = c.get(t, "https", cfOpenHost, nil)
	require.Equal(t, http.StatusOK, code, "a DNS-only domain on the same server stays reachable")
	require.Equal(t, "cf-v1", body)

	// The address Traefik saw is in its access log: use it as "Cloudflare".
	denied := waitAccessLine(t, "the 403 of the locked domain", func(m map[string]any) bool {
		return m["RequestHost"] == cfLockedHos && m["DownstreamStatus"] == float64(http.StatusForbidden)
	})
	peer, _, err := net.SplitHostPort(fmt.Sprint(denied["ClientAddr"]))
	require.NoError(t, err)
	peerAddr, err := netip.ParseAddr(peer)
	require.NoError(t, err)
	require.Equal(t, peer, denied["ClientHost"], "an untrusted peer's X-Forwarded-For is deleted before anything reads it")
	t.Logf("proxy sees the test client as %s", peer)
	cf := &agentprotocol.OriginLock{CloudflareCIDRs: []string{netip.PrefixFrom(peerAddr, peerAddr.BitLen()).String(), "192.0.2.0/24"}}

	// 2. The client is now "Cloudflare": admitted, with a user deny rule on the visitor.
	deny := &agentprotocol.Access{DenyCIDRs: []string{"198.51.100.7"}}
	require.NoError(t, px.Apply(table(cf, false, deny)))
	for name, tc := range map[string]struct {
		hdr  map[string]string
		want int
	}{
		"visitor admitted":                {visitor, http.StatusOK},
		"visitor denied by the user rule": {map[string]string{"X-Forwarded-For": "198.51.100.7", "CF-Connecting-IP": "198.51.100.7"}, http.StatusForbidden},
		"forged left entry cannot deny":   {map[string]string{"X-Forwarded-For": "198.51.100.7, 203.0.113.9", "CF-Connecting-IP": "203.0.113.9"}, http.StatusOK},
		"forged left entry cannot admit":  {map[string]string{"X-Forwarded-For": "203.0.113.9, 198.51.100.7", "CF-Connecting-IP": "198.51.100.7"}, http.StatusForbidden},
		"no visitor address fails closed": {nil, http.StatusForbidden},
	} {
		code, body := c.get(t, "https", cfLockedHos, tc.hdr)
		require.Equal(t, tc.want, code, "%s: %s", name, body)
		if tc.want == http.StatusOK {
			require.Equal(t, "cf-v1", body, name)
		}
	}
	code, _, hdr, err := c.do("http", cfLockedHos, visitor)
	require.NoError(t, err)
	require.Contains(t, []int{http.StatusMovedPermanently, http.StatusPermanentRedirect}, code, "an admitted request on :80 only redirects to https")
	require.True(t, strings.HasPrefix(hdr.Get("Location"), "https://"+cfLockedHos), hdr.Get("Location"))

	// Traefik logs the whole X-Forwarded-For as ClientHost; the parser takes its end.
	line := waitAccessLine(t, "the forged-left request", func(m map[string]any) bool {
		return m["ClientHost"] == "198.51.100.7, 203.0.113.9" && m["DownstreamStatus"] == float64(http.StatusOK)
	})
	ev, err := traffic.ParseLine([]byte(fmt.Sprint(line["_raw"])))
	require.NoError(t, err)
	require.Equal(t, "203.0.113.9", ev.ClientIP, "the visitor is the last entry, not what the visitor sent first")

	// 3. Authenticated Origin Pulls.
	caPEM, good := cfTestPKI(t, "origin-pull")
	_, other := cfTestPKI(t, "someone-else")
	aop := &agentprotocol.OriginLock{CloudflareCIDRs: cf.CloudflareCIDRs, ClientCAPEM: caPEM}
	require.NoError(t, px.Apply(table(aop, true, deny)))
	_, _, _, err = cfClient{}.do("https", cfLockedHos, visitor)
	require.Error(t, err, "no client certificate: the handshake fails")
	_, _, _, err = cfClient{cert: &other}.do("https", cfLockedHos, visitor)
	require.Error(t, err, "a client certificate from another CA: the handshake fails")
	code, body = cfClient{cert: &good}.get(t, "https", cfLockedHos, visitor)
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, "cf-v1", body)
	code, _ = cfClient{cert: &good}.get(t, "https", cfLockedHos, map[string]string{"X-Forwarded-For": "198.51.100.7"})
	require.Equal(t, http.StatusForbidden, code, "the user rules still run behind the certificate")
	code, _ = cfClient{}.get(t, "http", cfLockedHos, visitor)
	require.Contains(t, []int{http.StatusMovedPermanently, http.StatusPermanentRedirect}, code, "the certificate requirement is https only: port 80 (ACME HTTP-01) is unaffected")
	code, body = cfClient{}.get(t, "https", cfOpenHost, nil)
	require.Equal(t, http.StatusOK, code, "the unlocked domain needs no certificate")
	require.Equal(t, "cf-v1", body)

	// 4. Opting out: the domain is plain again and the trust set is gone.
	require.NoError(t, px.Apply(table(nil, false, nil)))
	code, body = c.get(t, "https", cfLockedHos, nil)
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, "cf-v1", body)
}
