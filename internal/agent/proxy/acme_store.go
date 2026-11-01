package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Which ACME storage holds a host's certificate. Each resolver has its own file on
// the ACME volume (the default HTTP-01 resolver's acme.json, a DNS-01 resolver's
// acme-<provider>.json) and Traefik asks a resolver for a certificate only for
// names its certificate store does not hold one for. A name that moved to a DNS-01
// resolver therefore keeps the certificate the HTTP-01 resolver issued, and that
// resolver renews it. The agent reads the domain names of the files, nothing
// else: no certificate, key or account leaves the proxy container's volume as
// anything but the names the parser keeps.

const (
	// maxACMEStoreBytes bounds what is read of one storage file; a file that is
	// larger (or cut) does not parse and counts as unreadable.
	maxACMEStoreBytes = 32 << 20
	acmeStoreTimeout  = 15 * time.Second
)

// acmeStoreNames are the certificate names of the storage files of a proxy.
type acmeStoreNames struct {
	http01 []string
	dns01  []string
}

// issuedVia says which storage holds a certificate that covers host. A name in
// both is reported as DNS-01: that certificate is the one the DNS-01 resolver
// issued after the name was in the HTTP-01 file.
func (s acmeStoreNames) issuedVia(host string) string {
	switch {
	case certCovers(s.dns01, host):
		return agentprotocol.AcmeIssuedDNS01
	case certCovers(s.http01, host):
		return agentprotocol.AcmeIssuedHTTP01
	}
	return ""
}

// parseACMEStoreNames returns the names (main and SANs) of the certificates of one
// Traefik ACME storage file: {"<resolver>": {"Account": ..., "Certificates":
// [{"domain": {"main": ..., "sans": [...]}, "certificate": ..., "key": ...}]}}.
// Everything but the domain names is skipped and dropped.
func parseACMEStoreNames(raw []byte) ([]string, error) {
	var file map[string]struct {
		Certificates []struct {
			Domain struct {
				Main string   `json:"main"`
				SANs []string `json:"sans"`
			} `json:"domain"`
		}
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, nil
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		return nil, fmt.Errorf("parse acme storage: %w", err)
	}
	var names []string
	for _, resolver := range file {
		for _, c := range resolver.Certificates {
			if c.Domain.Main != "" {
				names = append(names, c.Domain.Main)
			}
			names = append(names, c.Domain.SANs...)
		}
	}
	return names, nil
}

// readACMEStores reads the storage files of the proxy container: the default
// resolver's and one per DNS-01 resolver. ok is false when the container cannot
// be asked or a file that exists cannot be read; a file that does not exist holds
// no certificates (a resolver that has issued nothing yet has none).
func (m *Manager) readACMEStores(ctx context.Context, cfg agentprotocol.ProxyConfig) (names acmeStoreNames, ok bool) {
	cctx, cancel := context.WithTimeout(ctx, acmeStoreTimeout)
	defer cancel()
	info, err := m.cfg.Engine.ContainerInspect(cctx, agentprotocol.ProxyContainer)
	if err != nil || !info.Running {
		return acmeStoreNames{}, false
	}
	read := func(path string) ([]string, bool) {
		var out, errb bytes.Buffer
		code, err := m.cfg.Engine.ContainerExec(cctx, info.ID, []string{"cat", path}, docker.ExecSpec{},
			&limitWriter{w: &out, n: maxACMEStoreBytes}, &limitWriter{w: &errb, n: 1024})
		if err != nil {
			return nil, false
		}
		if code != 0 && out.Len() == 0 {
			return nil, true // no such file: nothing issued through it yet
		}
		ns, perr := parseACMEStoreNames(out.Bytes())
		return ns, perr == nil && code == 0
	}
	ok = true
	var good bool
	if names.http01, good = read(ACMEStorage); !good {
		ok = false
	}
	for _, r := range sortedResolvers(cfg.DNSResolvers) {
		ns, good := read(agentprotocol.DNSResolverStorage(r.Provider))
		if !good {
			ok = false
		}
		names.dns01 = append(names.dns01, ns...)
	}
	return names, ok
}
