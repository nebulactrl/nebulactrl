package services

import (
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// RouteConfig is services.config.route of a kind=route service (source_type
// external): a node with no container that publishes an upstream the platform
// does not run (a LAN box, a host port, a tailnet device) or exposes a managed
// service's TCP/UDP port on the server. Owned by internal/proxyroutes.
type RouteConfig struct {
	Protocol  string          `json:"protocol"` // http | tcp | udp
	Upstreams []RouteUpstream `json:"upstreams"`

	// http
	Scheme             string `json:"scheme,omitempty"` // http (default) | https
	InsecureSkipVerify bool   `json:"insecureSkipVerify,omitempty"`
	ServerName         string `json:"serverName,omitempty"`
	PassHost           *bool  `json:"passHost,omitempty"` // default true
	HealthPath         string `json:"healthPath,omitempty"`
	VerifySkip         bool   `json:"verifySkip,omitempty"`

	// tcp/udp
	ListenPort     int      `json:"listenPort,omitempty"`
	AllowCIDRs     []string `json:"allowCidrs,omitempty"`
	DenyCIDRs      []string `json:"denyCidrs,omitempty"`      // refused first, even when AllowCIDRs admits them
	OpenToInternet bool     `json:"openToInternet,omitempty"` // required when AllowCIDRs is empty
	ProxyProtocol  int      `json:"proxyProtocol,omitempty"`  // tcp only: 0, 1 or 2
	// PublicDatabase marks the tcp route that publishes a managed database's
	// port (the "public database port" switch). It needs an allow-list; a range
	// that matches every address (0.0.0.0/0, ::/0) also needs OpenToInternet.
	PublicDatabase bool `json:"publicDatabase,omitempty"`
}

// RouteUpstream is one upstream: an external "host:port" Address, or a managed
// service (ServiceID + Port, resolved to its current containers on the route's
// server when the table is built).
type RouteUpstream struct {
	Address   string     `json:"address,omitempty"`
	ServiceID *uuid.UUID `json:"serviceId,omitempty"`
	Port      int        `json:"port,omitempty"`
}

// Validate checks the shape of the route (not the server's upstream policy,
// which proxyroutes enforces, and not DB uniqueness).
func (r *RouteConfig) Validate() error {
	if r == nil {
		return invalid("route config is required for route services")
	}
	switch r.Protocol {
	case agentprotocol.ProtoHTTP, agentprotocol.ProtoTCP, agentprotocol.ProtoUDP:
	default:
		return invalid("route protocol must be http, tcp or udp")
	}
	if len(r.Upstreams) == 0 || len(r.Upstreams) > 20 {
		return invalid("a route needs 1-20 upstreams")
	}
	for _, u := range r.Upstreams {
		switch {
		case u.ServiceID != nil && u.Address != "":
			return invalid("an upstream is either an address or a managed service, not both")
		case u.ServiceID != nil:
			if u.Port < 1 || u.Port > 65535 {
				return invalid("managed upstream needs a port between 1 and 65535")
			}
		case u.Address == "":
			return invalid("upstream address is required")
		}
	}
	if r.Protocol == agentprotocol.ProtoHTTP {
		switch r.Scheme {
		case "", "http", "https":
		default:
			return invalid("route scheme must be http or https")
		}
		if r.HealthPath != "" && !strings.HasPrefix(r.HealthPath, "/") {
			return invalid("route healthPath must start with /")
		}
		if r.ListenPort != 0 || len(r.AllowCIDRs) > 0 || len(r.DenyCIDRs) > 0 || r.ProxyProtocol != 0 || r.PublicDatabase {
			return invalid("listenPort, allowCidrs, denyCidrs, proxyProtocol and publicDatabase apply to tcp/udp routes only")
		}
		return nil
	}
	if err := agentprotocol.ValidateListenPort(r.ListenPort); err != nil {
		return invalid("%v", err)
	}
	if r.ProxyProtocol < 0 || r.ProxyProtocol > 2 || (r.ProxyProtocol != 0 && r.Protocol == agentprotocol.ProtoUDP) {
		return invalid("proxyProtocol must be 0, 1 or 2 and applies to tcp only")
	}
	if err := agentprotocol.ValidateAccessLists(r.AllowCIDRs, r.DenyCIDRs, false); err != nil {
		return invalid("%v", err)
	}
	if r.Protocol == agentprotocol.ProtoUDP && (len(r.AllowCIDRs) > 0 || len(r.DenyCIDRs) > 0) {
		return invalid("UDP routes cannot be restricted by IP: Traefik has no UDP middleware; use a firewall or VPN")
	}
	if r.Protocol == agentprotocol.ProtoTCP && len(r.AllowCIDRs) == 0 && !r.OpenToInternet {
		return invalid("a tcp route needs allowCidrs, or openToInternet=true to publish it to everyone")
	}
	if r.PublicDatabase {
		if r.Protocol != agentprotocol.ProtoTCP {
			return invalid("a public database port is a tcp route")
		}
		if len(r.AllowCIDRs) == 0 {
			return invalid("a public database port needs an allow-list of client networks")
		}
		if agentprotocol.AnyOpenCIDR(r.AllowCIDRs) && !r.OpenToInternet {
			return invalid("allowing every address (0.0.0.0/0 or ::/0) on a database port needs an explicit confirmation")
		}
		if len(r.Upstreams) != 1 || r.Upstreams[0].ServiceID == nil {
			return invalid("a public database port targets exactly one managed database")
		}
	}
	return nil
}

// Describe returns a short human label such as "tcp :25565 → service:25565".
func (r *RouteConfig) Describe() string {
	if r == nil {
		return ""
	}
	ups := make([]string, 0, len(r.Upstreams))
	for _, u := range r.Upstreams {
		if u.ServiceID != nil {
			ups = append(ups, fmt.Sprintf("service:%d", u.Port))
		} else {
			ups = append(ups, u.Address)
		}
	}
	s := r.Protocol
	if r.ListenPort != 0 {
		s += fmt.Sprintf(" :%d", r.ListenPort)
	}
	return s + " → " + strings.Join(ups, ", ")
}
