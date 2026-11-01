// Package proxyserver owns the server-level part of the reverse proxy: custom
// certificates, the upstream policy, the per-server tailnet and the
// "Deploy proxy" flow. BuildConfig and CertSecrets are consumed by deployments,
// the reconciler and the agent handler to attach the proxy configuration to
// every route push.
package proxyserver

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/dns01"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// PolicyFromRow decodes servers.upstream_policy, falling back to the default.
func PolicyFromRow(raw json.RawMessage) agentprotocol.UpstreamPolicy {
	p := agentprotocol.DefaultUpstreamPolicy()
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &p)
	}
	return p
}

// BuildConfig returns the APPLIED proxy configuration of a server: applied
// (not staged) certificates, the applied upstream policy and the applied tailnet
// (mode, hostname, MagicDNS).
// It carries no secret values: private keys are referenced by CertRef.KeyRef
// and resolved by CertSecrets at send time. The tailnet spec never asks for
// provisioning here (that is only set by the explicit "Deploy proxy" command).
func BuildConfig(ctx context.Context, q *db.Queries, serverID uuid.UUID) (*agentprotocol.ProxyConfig, error) {
	srv, err := q.LoadServerRow(ctx, db.LoadServerRowParams{ID: serverID})
	if err != nil {
		return nil, fmt.Errorf("load server: %w", err)
	}
	certs, err := q.ListAppliedCertificates(ctx, serverID)
	if err != nil {
		return nil, fmt.Errorf("list certificates: %w", err)
	}
	pc := &agentprotocol.ProxyConfig{UpstreamPolicy: PolicyFromRow(srv.AppliedUpstreamPolicy)}
	// The applied ports ride along on every proxy config: a config without them
	// would send the agent back to its own flags.
	if srv.ProxyAppliedHttpPort != nil {
		pc.HTTPPort = int(*srv.ProxyAppliedHttpPort)
	}
	if srv.ProxyAppliedHttpsPort != nil {
		pc.HTTPSPort = int(*srv.ProxyAppliedHttpsPort)
	}
	// DNS-01 resolvers and wildcard certificates: what the last deploy applied
	// (variable names only; the values are attached at send time by dns01.Secrets).
	dns01.AppliedSnapshot(srv).Apply(pc)
	for _, c := range certs {
		pc.Certificates = append(pc.Certificates, agentprotocol.CertRef{
			ID: c.ID.String(), Name: c.Name, Domains: c.Domains, CertPEM: c.CertPem,
			KeyRef: agentprotocol.SecretCertPrefix + c.ID.String(),
		})
	}
	if srv.TailnetAppliedMode != agentprotocol.TailnetOff && srv.TailnetAppliedMode != "" {
		ts := &agentprotocol.TailnetSpec{Mode: srv.TailnetAppliedMode, MagicDNS: srv.TailnetAppliedMagicDns}
		if srv.TailnetAppliedHostname != nil {
			ts.Hostname = *srv.TailnetAppliedHostname
		}
		pc.Tailnet = ts
	}
	return pc, nil
}

// CertSecrets returns the send-time secrets of a proxy config: one private key
// per certificate, under CertRef.KeyRef. Never persist the result.
func CertSecrets(ctx context.Context, q *db.Queries, box *secretbox.Box, pc *agentprotocol.ProxyConfig) (map[string]string, error) {
	out := map[string]string{}
	if pc == nil {
		return out, nil
	}
	for _, c := range pc.Certificates {
		id, err := uuid.Parse(c.ID)
		if err != nil {
			return nil, fmt.Errorf("certificate id %q: %w", c.ID, err)
		}
		row, err := q.GetCertificateKey(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("load certificate %s: %w", c.ID, err)
		}
		key, err := box.OpenString(secretbox.CertificatesKey, id.String(), row)
		if err != nil {
			return nil, fmt.Errorf("decrypt certificate key %s: %w", c.ID, err)
		}
		out[c.KeyRef] = key
	}
	return out, nil
}
