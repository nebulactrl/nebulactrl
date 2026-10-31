package proxy

import (
	"context"
	"errors"
	"fmt"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// SetProxyConfig installs the server-level configuration (certificates with
// resolved keys, upstream policy, tailnet) the next Apply uses. A failed Apply
// puts the previous configuration back, so the pair (routes, config) is applied
// atomically. Use ResolveSecrets before calling it.
func (m *Manager) SetProxyConfig(cfg agentprotocol.ProxyConfig) {
	m.mu.Lock()
	m.proxyCfg = cloneProxyConfig(cfg)
	m.mu.Unlock()
}

// ProxyConfig returns a copy of the configuration the next Apply uses.
func (m *Manager) ProxyConfig() agentprotocol.ProxyConfig {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return cloneProxyConfig(m.proxyCfg)
}

func (m *Manager) appliedConfig() agentprotocol.ProxyConfig {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return cloneProxyConfig(m.appliedCfg)
}

// UpstreamPolicy is the upstream policy of the applied configuration: what the
// agent-side uptime probes of route upstreams are held to.
func (m *Manager) UpstreamPolicy() agentprotocol.UpstreamPolicy {
	return m.appliedConfig().UpstreamPolicy
}

// revertConfig drops a pending configuration after a failed Apply.
func (m *Manager) revertConfig() {
	m.mu.Lock()
	m.proxyCfg = cloneProxyConfig(m.appliedCfg)
	m.tailnetKey = ""
	m.mu.Unlock()
}

// finishTailnet marks the one-shot tailnet operations (Provision, Remove) as
// attempted and persists the configuration. The auth key never outlives the
// apply that used it, and it is never persisted.
func (m *Manager) finishTailnet() {
	m.mu.Lock()
	m.tailnetKey = ""
	for _, c := range []*agentprotocol.ProxyConfig{&m.proxyCfg, &m.appliedCfg} {
		if t := c.Tailnet; t != nil {
			t.Provision, t.Remove = false, false
		}
	}
	persist := cloneProxyConfig(m.appliedCfg)
	m.mu.Unlock()
	if m.cfg.Store != nil {
		if err := m.cfg.Store.SaveProxyConfig(persist); err != nil {
			m.log.Error("persist proxy config", "err", err)
		}
	}
}

// LastApply is the report of the most recent successful Apply: whether the
// proxy container was recreated, the load state of every certificate and the
// tailnet state. Outcome and Message are left to the caller.
func (m *Manager) LastApply() agentprotocol.ProxyConfigureResult {
	m.mu.RLock()
	defer m.mu.RUnlock()
	r := m.last
	r.Certificates = append([]agentprotocol.CertStatus(nil), m.last.Certificates...)
	r.Tailnet = cloneTailnetStatus(m.last.Tailnet)
	return r
}

// checkRoutes validates a table against the configuration it will run with:
// shape and upstream policy (ValidateRoutes), tcp/udp ports that collide with
// the proxy's own listeners, and the upstream name resolution (PinUpstreams).
func (m *Manager) checkRoutes(ctx context.Context, routes []agentprotocol.Route, cfg agentprotocol.ProxyConfig) error {
	if err := ValidateRoutes(routes, cfg.UpstreamPolicy); err != nil {
		return err
	}
	if err := validateDNS01(cfg, m.ACMEEmail()); err != nil {
		return fmt.Errorf("DNS-01 configuration rejected: %w", err)
	}
	if err := m.checkPorts(cfg); err != nil {
		return err
	}
	httpPort, httpsPort := m.effectivePorts(cfg)
	own := map[int]string{httpPort: "HTTP", httpsPort: "HTTPS", m.cfg.AdminPort: "admin"}
	for _, p := range L4Ports(routes) {
		if what, clash := own[p.Port]; clash {
			return fmt.Errorf("%s port %d is the proxy's own %s port", p.Proto, p.Port, what)
		}
	}
	_, err := PinUpstreams(ctx, routes, cfg.UpstreamPolicy, magicDNSOn(cfg))
	return err
}

// magicDNSOn reports whether the proxy container resolves tailnet names
// through MagicDNS under cfg.
func magicDNSOn(cfg agentprotocol.ProxyConfig) bool {
	return cfg.Tailnet != nil && cfg.Tailnet.Mode == agentprotocol.TailnetManaged && cfg.Tailnet.MagicDNS
}

// tailnetManaged reports whether a managed tailnet node is configured (applied
// or pending).
func (m *Manager) tailnetManaged() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, c := range []agentprotocol.ProxyConfig{m.proxyCfg, m.appliedCfg} {
		if c.Tailnet != nil && c.Tailnet.Mode == agentprotocol.TailnetManaged {
			return true
		}
	}
	return false
}

// dropTailnetContainer removes the tailscale container before the proxy
// container it shares a network namespace with goes away; reconcileTailnet
// recreates it against the new one (the identity stays on the state volume).
func (m *Manager) dropTailnetContainer(ctx context.Context) {
	if !m.tailnetManaged() {
		return
	}
	if err := m.cfg.Engine.ContainerRemove(ctx, agentprotocol.TailscaleContainer, true, false); err != nil && !errors.Is(err, docker.ErrNotFound) {
		m.log.Warn("remove tailscale container", "err", err)
	}
}

// reconcileAppliedTailnet recreates the tailnet node of the last applied
// configuration after the proxy container was put back.
func (m *Manager) reconcileAppliedTailnet(ctx context.Context) {
	spec := m.appliedConfig().Tailnet
	if spec == nil {
		return
	}
	if err := m.reconcileTailnet(ctx, spec, ""); err != nil {
		m.log.Warn("restore tailnet node", "err", err)
	}
}
