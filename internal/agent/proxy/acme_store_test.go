package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// acmeJSON is a Traefik ACME storage file with the given certificates (names
// only matter; the key material is a canary that must never leave the parser).
func acmeJSON(resolver string, certs ...[]string) string {
	var cs []map[string]any
	for _, names := range certs {
		cs = append(cs, map[string]any{
			"domain":      map[string]any{"main": names[0], "sans": names[1:]},
			"certificate": "CERT-PEM-CANARY",
			"key":         "PRIVATE-KEY-CANARY",
			"Store":       "default",
		})
	}
	raw, _ := json.Marshal(map[string]any{resolver: map[string]any{
		"Account":      map[string]any{"Email": "ops@example.com", "PrivateKey": "ACCOUNT-KEY-CANARY"},
		"Certificates": cs,
	}})
	return string(raw)
}

const (
	leFile = ACMEStorage
)

var cfFile = agentprotocol.DNSResolverStorage("cloudflare")

func TestParseACMEStoreNamesKeepsDomainNamesOnly(t *testing.T) {
	names, err := parseACMEStoreNames([]byte(acmeJSON("le", []string{"a.example.com", "www.a.example.com"}, []string{"*.b.example.com"})))
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"a.example.com", "www.a.example.com", "*.b.example.com"}, names)
	joined := strings.Join(names, ",")
	for _, canary := range []string{"CANARY", "PEM", "KEY"} {
		require.NotContains(t, joined, canary)
	}

	for _, empty := range []string{"", "  \n", "{}", `{"le":{"Account":null,"Certificates":null}}`} {
		names, err := parseACMEStoreNames([]byte(empty))
		require.NoError(t, err, empty)
		require.Empty(t, names, empty)
	}
	_, err = parseACMEStoreNames([]byte(`{"le":`))
	require.Error(t, err, "a cut file is unreadable, not empty")
}

func TestIssuedViaTellsTheStoreFromItsDomainNames(t *testing.T) {
	s := acmeStoreNames{
		http01: []string{"old.example.com", "*.wild.example.com", "both.example.com"},
		dns01:  []string{"new.example.com", "both.example.com", "*.preview.example.com"},
	}
	for host, want := range map[string]string{
		"old.example.com":           agentprotocol.AcmeIssuedHTTP01,
		"new.example.com":           agentprotocol.AcmeIssuedDNS01,
		"both.example.com":          agentprotocol.AcmeIssuedDNS01, // the DNS-01 issued one is the newer
		"app.wild.example.com":      agentprotocol.AcmeIssuedHTTP01,
		"*.preview.example.com":     agentprotocol.AcmeIssuedDNS01,
		"deep.app.wild.example.com": "", // one label only
		"none.example.com":          "",
	} {
		require.Equal(t, want, s.issuedVia(host), host)
	}
}

// storeManager is a manager whose proxy container runs and whose ACME storage
// files answer with the given contents (a missing key: no such file).
func storeManager(t *testing.T, files map[string]string, execErr error) (*Manager, *fleetEngine) {
	t.Helper()
	eng := newFleetEngine()
	eng.ctrs[agentprotocol.ProxyContainer] = &fleetCtr{info: docker.ContainerInfo{ID: "px1", Running: true, State: "running"}}
	eng.execFn = func(_ string, argv []string) (string, int, error) {
		if execErr != nil {
			return "", 0, execErr
		}
		require.Len(t, argv, 2)
		require.Equal(t, "cat", argv[0], "the agent only reads the storage files")
		out, ok := files[argv[1]]
		if !ok {
			return "", 1, nil
		}
		return out, 0, nil
	}
	return &Manager{cfg: Config{Engine: eng, HTTPPort: 80, HTTPSPort: 443, AdminPort: 9000}}, eng
}

func storeTargets() []acmeTarget {
	// Two hosts of connected-zone names whose resolver the proxy does not run yet
	// (store-only), one wildcard certificate.
	return []acmeTarget{
		{kind: agentprotocol.AcmeKindDomain, id: "dom-old", covered: "old.example.com", provider: "cloudflare", storeOnly: true},
		{kind: agentprotocol.AcmeKindDomain, id: "dom-new", covered: "new.example.com", provider: "cloudflare", storeOnly: true},
		{kind: agentprotocol.AcmeKindDomain, id: "dom-none", covered: "none.example.com", provider: "cloudflare", storeOnly: true},
		{kind: agentprotocol.AcmeKindWildcard, id: "wild", covered: "*.wild.example.com", provider: "cloudflare", sni: "x.wild.example.com"},
	}
}

func TestHeartbeatReportsWhichStoreHoldsEachHostsCertificate(t *testing.T) {
	m, _ := storeManager(t, map[string]string{
		leFile: acmeJSON("le", []string{"old.example.com"}),
		cfFile: acmeJSON("nc-dns-cloudflare", []string{"new.example.com"}),
	}, nil)
	cfg := agentprotocol.ProxyConfig{DNSResolvers: cfResolver()}
	targets := storeTargets()[:3] // store-only: no TLS handshake, no log read
	m.refreshAcme(context.Background(), cfg, targets)

	got := map[string]agentprotocol.AcmeCertStatus{}
	for _, st := range m.AcmeStatuses() {
		got[st.ID] = st
	}
	require.Equal(t, agentprotocol.AcmeIssuedHTTP01, got["dom-old"].IssuedVia, "a host that still holds its HTTP-01 certificate")
	require.Equal(t, agentprotocol.AcmeIssuedDNS01, got["dom-new"].IssuedVia)
	require.Empty(t, got["dom-none"].IssuedVia, "no certificate in any storage")
	for _, st := range got {
		require.Empty(t, st.Status, "a store-only host waits for nothing: it has no DNS-01 status")
	}
	raw, err := json.Marshal(m.AcmeStatuses())
	require.NoError(t, err)
	require.NotContains(t, string(raw), "CANARY", "no certificate, key or account leaves the agent")
	require.False(t, m.acme.pending, "store-only hosts do not make the poll fast")
}

func TestHeartbeatKeepsTheLastReadingWhileTheFilesCannotBeRead(t *testing.T) {
	files := map[string]string{leFile: acmeJSON("le", []string{"old.example.com"})}
	m, eng := storeManager(t, files, nil)
	cfg := agentprotocol.ProxyConfig{DNSResolvers: cfResolver()}
	targets := storeTargets()[:1]
	m.refreshAcme(context.Background(), cfg, targets)
	require.Equal(t, agentprotocol.AcmeIssuedHTTP01, m.AcmeStatuses()[0].IssuedVia)

	// The container cannot be asked: the answer does not flap to "unknown".
	eng.mu.Lock()
	eng.execFn = func(string, []string) (string, int, error) { return "", 0, errors.New("docker unavailable") }
	eng.mu.Unlock()
	m.refreshAcme(context.Background(), cfg, targets)
	require.Equal(t, agentprotocol.AcmeIssuedHTTP01, m.AcmeStatuses()[0].IssuedVia)

	// A cut file does not count as "no certificates" either.
	eng.mu.Lock()
	eng.execFn = func(string, []string) (string, int, error) { return `{"le":{"Certificates":[`, 0, nil }
	eng.mu.Unlock()
	m.refreshAcme(context.Background(), cfg, targets)
	require.Equal(t, agentprotocol.AcmeIssuedHTTP01, m.AcmeStatuses()[0].IssuedVia)
}

// A host that names a resolver the proxy does not run is a store-only target; once
// the proxy runs it, the host is observed like any DNS-01 certificate.
func TestStoreOnlyTargetsBecomeObservedOnceTheResolverRuns(t *testing.T) {
	route := ruleRoute(
		rule(domA, "app.example.com", func(d *agentprotocol.DomainRule) { d.DNSResolver = agentprotocol.DNSCloudflare }),
		rule(domB, "tunnel.example.com", func(d *agentprotocol.DomainRule) { d.DNSResolver, d.Tunnel = agentprotocol.DNSCloudflare, true }),
		rule(domC, "plain.example.com", nil),
	)
	before := acmeTargets(agentprotocol.ProxyConfig{}, []agentprotocol.Route{route})
	require.Len(t, before, 1, "no resolver: the host with a name in a connected zone only, not the tunnel name or the plain one")
	require.True(t, before[0].storeOnly)
	require.Equal(t, domA, before[0].id)

	after := acmeTargets(agentprotocol.ProxyConfig{DNSResolvers: cfResolver()}, []agentprotocol.Route{route})
	require.Len(t, after, 2, "the tunnel name that names the resolver is observed once the proxy runs it")
	require.False(t, after[0].storeOnly)
}
