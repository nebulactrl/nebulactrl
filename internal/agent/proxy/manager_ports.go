package proxy

import (
	"context"
	"errors"
	"fmt"
	"net"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Host ports and where they come from. The agent is started with
// --proxy-http-port and --proxy-https-port (Config.HTTPPort, HTTPSPort; 80 and
// 443 by default). The control plane's server setting (ProxyConfig.HTTPPort and
// HTTPSPort, persisted with the rest of the proxy configuration) overrides them,
// and 0 in the configuration falls back to the flags again.

// effectivePorts are the host ports the proxy publishes for cfg: the
// configuration's override, else the agent's own flag value.
func (m *Manager) effectivePorts(cfg agentprotocol.ProxyConfig) (httpPort, httpsPort int) {
	httpPort, httpsPort = m.cfg.HTTPPort, m.cfg.HTTPSPort
	if cfg.HTTPPort != 0 {
		httpPort = cfg.HTTPPort
	}
	if cfg.HTTPSPort != 0 {
		httpsPort = cfg.HTTPSPort
	}
	return httpPort, httpsPort
}

// WebPorts are the host ports of the proxy's web entrypoints: the applied
// server setting, else the agent's flags.
func (m *Manager) WebPorts() (httpPort, httpsPort int) {
	return m.effectivePorts(m.appliedConfig())
}

// webPorts are the web ports a container built from in publishes: the inputs'
// own, else the agent's flags (inputs built without ports).
func (m *Manager) webPorts(in specInputs) (httpPort, httpsPort int) {
	httpPort, httpsPort = in.HTTPPort, in.HTTPSPort
	if httpPort == 0 {
		httpPort = m.cfg.HTTPPort
	}
	if httpsPort == 0 {
		httpsPort = m.cfg.HTTPSPort
	}
	return httpPort, httpsPort
}

// checkPorts validates the ports cfg asks for: in range, different from each
// other and from the admin port the agent runs with. tcp/udp route ports are
// checked against them by checkRoutes.
func (m *Manager) checkPorts(cfg agentprotocol.ProxyConfig) error {
	if err := agentprotocol.ValidateProxyConfigPorts(cfg); err != nil {
		return fmt.Errorf("proxy ports rejected: %w", err)
	}
	httpPort, httpsPort := m.effectivePorts(cfg)
	if err := agentprotocol.ValidateProxyPorts(httpPort, httpsPort, m.cfg.AdminPort); err != nil {
		return fmt.Errorf("proxy ports rejected: %w", err)
	}
	return nil
}

// Ports reports the host ports the proxy runs on: the last applied
// configuration's effective ports and where they come from, the agent's own
// flag values (what resetting the server setting returns to) and which of the
// well-known web ports another process holds.
func (m *Manager) Ports() agentprotocol.ProxyPorts {
	m.mu.RLock()
	cfg := m.appliedCfg
	m.mu.RUnlock()
	httpPort, httpsPort := m.effectivePorts(cfg)
	p := agentprotocol.ProxyPorts{
		HTTP: httpPort, HTTPS: httpsPort, Source: agentprotocol.ProxyPortsFlag,
		FlagHTTP: m.cfg.HTTPPort, FlagHTTPS: m.cfg.HTTPSPort,
	}
	if cfg.HTTPPort != 0 || cfg.HTTPSPort != 0 {
		p.Source = agentprotocol.ProxyPortsServer
	}
	p.BusyOther = m.busyOther(httpPort, httpsPort)
	p.Normalize()
	return p
}

// wellKnownPorts are the web ports a plan warns about.
var wellKnownPorts = []int{80, 443}

// busyOtherTTL bounds how often the host is probed: heartbeats come every
// 10 seconds.
const busyOtherTTL = 5 * time.Second

// busyOther lists the well-known ports a process other than the proxy listens
// on. A port the proxy publishes itself is not probed.
func (m *Manager) busyOther(httpPort, httpsPort int) []int {
	m.portsMu.Lock()
	defer m.portsMu.Unlock()
	if !m.busyAt.IsZero() && time.Since(m.busyAt) < busyOtherTTL && m.busyFor == [2]int{httpPort, httpsPort} {
		return slices.Clone(m.busy)
	}
	var busy []int
	for _, p := range wellKnownPorts {
		if p == httpPort || p == httpsPort {
			continue
		}
		if m.portInUse("tcp", p) {
			busy = append(busy, p)
		}
	}
	m.busy, m.busyAt, m.busyFor = busy, time.Now(), [2]int{httpPort, httpsPort}
	return slices.Clone(busy)
}

// portInUse reports whether a process of the host holds the port (tcp or udp).
func (m *Manager) portInUse(proto string, port int) bool {
	if m.cfg.PortInUse != nil {
		return m.cfg.PortInUse(proto, port)
	}
	return listenInUse(proto, port)
}

// listenInUse tries to bind the port. Only "address already in use" counts: a
// permission error or an unsupported protocol proves nothing.
func listenInUse(proto string, port int) bool {
	addr := ":" + strconv.Itoa(port)
	var err error
	if proto == "udp" {
		var pc net.PacketConn
		if pc, err = net.ListenPacket("udp", addr); err == nil {
			_ = pc.Close()
		}
	} else {
		var l net.Listener
		if l, err = net.Listen("tcp", addr); err == nil {
			_ = l.Close()
		}
	}
	return errors.Is(err, syscall.EADDRINUSE)
}

// publishedBefore reports whether the container's previous configuration
// already published host port/proto (the new container may take it over).
func publishedBefore(old []docker.PortPublish, port int, proto string) bool {
	return slices.ContainsFunc(old, func(p docker.PortPublish) bool {
		return p.HostPort == port && (p.Proto == proto || (p.Proto == "" && proto == "tcp"))
	})
}

// checkNewWebPorts refuses to recreate the proxy onto a web port another
// process or container holds, before the running container is touched: a
// failed bind after the old container was removed would drop connections until
// the previous container is back. Ports the old container publishes itself are
// skipped. The check is best effort (a published port may be bound by Docker
// without a listening socket, which the container list covers); the rollback in
// ensureContainer remains the safety net.
func (m *Manager) checkNewWebPorts(ctx context.Context, old docker.ContainerInfo, want docker.ContainerSpec) error {
	prev := slices.Clone(old.Ports)
	if m.applied != nil {
		prev = append(prev, m.specFor(*m.applied).Ports...)
	}
	var fresh []docker.PortPublish
	for _, p := range want.Ports {
		if p.ContainerPort != 80 && p.ContainerPort != 443 {
			continue // the admin port and tcp/udp routes keep the bind-failure handling
		}
		if !publishedBefore(prev, p.HostPort, p.Proto) {
			fresh = append(fresh, p)
		}
	}
	if len(fresh) == 0 {
		return nil
	}
	held := m.containerPorts(ctx)
	for _, p := range fresh {
		by := ""
		switch {
		case m.portInUse(p.Proto, p.HostPort):
			by = "another process"
		default:
			if name, ok := held[portKey(p.Proto, p.HostPort)]; ok {
				by = "the container " + name
			}
		}
		if by == "" {
			continue
		}
		what := "HTTP"
		if p.ContainerPort == 443 {
			what = "HTTPS"
		}
		keep := "the proxy keeps running on its current ports"
		if m.applied != nil {
			h, s := m.webPorts(*m.applied)
			keep = fmt.Sprintf("the proxy keeps running on :%d and :%d", h, s)
		}
		return fmt.Errorf("port %d (%s, %s) is already in use on this server by %s; %s. Free the port (stop that process or container) or choose another port", p.HostPort, what, p.Proto, by, keep)
	}
	return nil
}

func portKey(proto string, port int) string {
	if proto == "" {
		proto = "tcp"
	}
	return proto + "/" + strconv.Itoa(port)
}

// containerPorts maps "proto/port" to the name of a running container other
// than the proxy and its helpers that publishes it. A failed listing yields no
// answer rather than an error: the check is advisory.
func (m *Manager) containerPorts(ctx context.Context) map[string]string {
	out := map[string]string{}
	lctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	list, err := m.cfg.Engine.ContainerList(lctx, docker.ListFilter{})
	if err != nil {
		m.log.Warn("list containers for the proxy port check", "err", err)
		return out
	}
	for _, c := range list {
		if c.Name == agentprotocol.ProxyContainer || strings.HasPrefix(c.Name, agentprotocol.ProxyContainer+"-") {
			continue
		}
		for _, p := range c.Ports {
			if p.HostPort != 0 {
				out[portKey(p.Proto, p.HostPort)] = c.Name
			}
		}
	}
	return out
}
