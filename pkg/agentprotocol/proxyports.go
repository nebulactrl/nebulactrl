package agentprotocol

import (
	"fmt"
	"sort"
)

// CapProxyPorts is advertised in Facts.Capabilities by agents that honour
// ProxyConfig.HTTPPort and HTTPSPort and report ProxyHealth.Ports. An older
// agent would silently drop the fields, so the control plane refuses to stage
// ports (422 agent_too_old) for servers without it.
const CapProxyPorts = "proxy.ports"

// Where the proxy's effective host ports come from (ProxyPorts.Source).
const (
	// ProxyPortsFlag: the agent's own --proxy-http-port / --proxy-https-port
	// (80 and 443 by default).
	ProxyPortsFlag = "flag"
	// ProxyPortsServer: the control plane's server setting (ProxyConfig.HTTPPort
	// and HTTPSPort), which overrides the flags.
	ProxyPortsServer = "server"
)

// ProxyPorts is the observed port state of the managed proxy, reported in
// heartbeats (ProxyHealth.Ports) and kept in the server's facts (proxyPorts).
type ProxyPorts struct {
	// HTTP and HTTPS are the host ports the proxy container publishes (the
	// effective ports).
	HTTP  int `json:"http"`
	HTTPS int `json:"https"`
	// Source is ProxyPortsFlag or ProxyPortsServer.
	Source string `json:"source"`
	// FlagHTTP and FlagHTTPS are the ports of the agent's own flags: what a
	// reset of the server setting returns to.
	FlagHTTP  int `json:"flagHttp"`
	FlagHTTPS int `json:"flagHttps"`
	// BusyOther lists the well-known web ports (80, 443) that a process other
	// than the proxy listens on right now, so a plan can warn before the proxy
	// is moved onto them. Ascending; empty when none.
	BusyOther []int `json:"busyOther,omitempty"`
}

// Normalize sorts BusyOther so two reports of the same state compare equal.
func (p *ProxyPorts) Normalize() {
	sort.Ints(p.BusyOther)
}

// Valid ports for the proxy's published HTTP and HTTPS entrypoints.
const (
	MinProxyPort = 1
	MaxProxyPort = 65535
)

// ValidateProxyPorts checks the proxy's effective host ports: both in range,
// different from each other and from the admin port.
func ValidateProxyPorts(httpPort, httpsPort, adminPort int) error {
	for _, p := range []struct {
		name string
		port int
	}{{"HTTP", httpPort}, {"HTTPS", httpsPort}} {
		if p.port < MinProxyPort || p.port > MaxProxyPort {
			return fmt.Errorf("the %s port %d is not between %d and %d", p.name, p.port, MinProxyPort, MaxProxyPort)
		}
	}
	if httpPort == httpsPort {
		return fmt.Errorf("the HTTP and HTTPS ports must differ (both are %d)", httpPort)
	}
	if adminPort != 0 {
		for _, p := range []struct {
			name string
			port int
		}{{"HTTP", httpPort}, {"HTTPS", httpsPort}} {
			if p.port == adminPort {
				return fmt.Errorf("the %s port %d is the proxy's admin port", p.name, p.port)
			}
		}
	}
	return nil
}

// ValidateProxyConfigPorts checks the optional override of a ProxyConfig:
// zero keeps the agent's own port, anything else must be a valid port, and
// two overrides must differ. Whether the result clashes with the agent's own
// other port or admin port is decided where the effective pair is known.
func ValidateProxyConfigPorts(cfg ProxyConfig) error {
	for _, p := range []struct {
		name string
		port int
	}{{"HTTP", cfg.HTTPPort}, {"HTTPS", cfg.HTTPSPort}} {
		if p.port != 0 && (p.port < MinProxyPort || p.port > MaxProxyPort) {
			return fmt.Errorf("the %s port %d is not between %d and %d", p.name, p.port, MinProxyPort, MaxProxyPort)
		}
	}
	if cfg.HTTPPort != 0 && cfg.HTTPPort == cfg.HTTPSPort {
		return fmt.Errorf("the HTTP and HTTPS ports must differ (both are %d)", cfg.HTTPPort)
	}
	return nil
}
