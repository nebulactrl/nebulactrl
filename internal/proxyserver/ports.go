package proxyserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/dns01"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Default host ports of the proxy when the agent has not reported its own.
const (
	defaultHTTPPort  = 80
	defaultHTTPSPort = 443
)

// AdminChecker tells whether a user is the instance administrator (the first
// account). auth.Service implements it.
type AdminChecker interface {
	IsInstanceAdmin(ctx context.Context, userID uuid.UUID) (bool, error)
}

// ProxyPorts is the host-port state of a server's proxy.
type ProxyPorts struct {
	HTTP         int    `json:"http" doc:"HTTP host port the proxy will listen on after Deploy proxy (the setting, else the agent's own port)"`
	HTTPS        int    `json:"https" doc:"HTTPS host port (tcp and udp) the proxy will listen on after Deploy proxy"`
	AppliedHTTP  int    `json:"appliedHttp" doc:"HTTP host port the proxy listens on now (as reported by the agent)"`
	AppliedHTTPS int    `json:"appliedHttps" doc:"HTTPS host port the proxy listens on now"`
	Source       string `json:"source" enum:",flag,server" doc:"Where the ports the proxy runs on come from: flag = the agent's own service flags (--proxy-http-port, --proxy-https-port), server = the server setting; empty until the agent reports"`
	Staged       bool   `json:"staged" doc:"The ports setting differs from the applied one and awaits Deploy proxy"`
	SetHTTP      *int   `json:"setHttp" doc:"The staged HTTP port setting; null = the agent's own port"`
	SetHTTPS     *int   `json:"setHttps" doc:"The staged HTTPS port setting; null = the agent's own port"`
	AgentHTTP    int    `json:"agentHttp" doc:"The HTTP port of the agent's own flags (what Reset to agent defaults returns to)"`
	AgentHTTPS   int    `json:"agentHttps" doc:"The HTTPS port of the agent's own flags"`
	BusyOther    []int  `json:"busyOther" doc:"Of ports 80 and 443, those another process holds right now (reported by the agent)"`
	Reported     bool   `json:"reported" doc:"The agent has reported its ports"`
	Supported    bool   `json:"supported" doc:"The agent can change its ports from the server (capability proxy.ports)"`
}

// factsPorts decodes facts.proxyPorts: the ports the agent reports in heartbeats.
func factsPorts(row db.Server) *agentprotocol.ProxyPorts {
	if len(row.Facts) == 0 {
		return nil
	}
	var f struct {
		ProxyPorts *agentprotocol.ProxyPorts `json:"proxyPorts"`
	}
	if json.Unmarshal(row.Facts, &f) != nil || f.ProxyPorts == nil || f.ProxyPorts.HTTP == 0 || f.ProxyPorts.HTTPS == 0 {
		return nil
	}
	return f.ProxyPorts
}

// SupportsProxyPorts reports whether the server's agent advertised proxy.ports.
func SupportsProxyPorts(row db.Server) bool {
	return slices.Contains(factsOf(row).Capabilities, agentprotocol.CapProxyPorts)
}

// ErrAgentTooOldForPorts is the 422 for staging ports on an agent that would
// silently drop them.
func ErrAgentTooOldForPorts() error {
	return apierr.Unprocessable("agent_too_old", "this server's agent is too old to change the proxy ports from the server (it lacks proxy.ports); update the agent, or restart it with --proxy-http-port and --proxy-https-port")
}

func intPtr(p *int32) *int {
	if p == nil {
		return nil
	}
	v := int(*p)
	return &v
}

func int32Of(v *int) *int32 {
	if v == nil || *v == 0 {
		return nil
	}
	x := int32(*v)
	return &x
}

func sameInt32(a, b *int32) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// agentWebPorts are the ports of the agent's own flags, as reported (80 and
// 443 until it reports).
func agentWebPorts(row db.Server) (httpPort, httpsPort int) {
	httpPort, httpsPort = defaultHTTPPort, defaultHTTPSPort
	if rp := factsPorts(row); rp != nil {
		if rp.FlagHTTP > 0 {
			httpPort = rp.FlagHTTP
		}
		if rp.FlagHTTPS > 0 {
			httpsPort = rp.FlagHTTPS
		}
	}
	return httpPort, httpsPort
}

// desiredWebPorts are the host ports Deploy proxy would apply: the staged
// setting, else the agent's own.
func desiredWebPorts(row db.Server) (httpPort, httpsPort int) {
	httpPort, httpsPort = agentWebPorts(row)
	if row.ProxyHttpPort != nil {
		httpPort = int(*row.ProxyHttpPort)
	}
	if row.ProxyHttpsPort != nil {
		httpsPort = int(*row.ProxyHttpsPort)
	}
	return httpPort, httpsPort
}

// appliedWebPorts are the host ports the proxy listens on now: what the agent
// reports, else what the last deploy applied.
func appliedWebPorts(row db.Server) (httpPort, httpsPort int) {
	if rp := factsPorts(row); rp != nil {
		return rp.HTTP, rp.HTTPS
	}
	httpPort, httpsPort = defaultHTTPPort, defaultHTTPSPort
	if row.ProxyAppliedHttpPort != nil {
		httpPort = int(*row.ProxyAppliedHttpPort)
	}
	if row.ProxyAppliedHttpsPort != nil {
		httpsPort = int(*row.ProxyAppliedHttpsPort)
	}
	return httpPort, httpsPort
}

// portsStaged reports whether the ports setting awaits Deploy proxy.
func portsStaged(row db.Server) bool {
	return !sameInt32(row.ProxyHttpPort, row.ProxyAppliedHttpPort) || !sameInt32(row.ProxyHttpsPort, row.ProxyAppliedHttpsPort)
}

func portsView(row db.Server) ProxyPorts {
	httpPort, httpsPort := desiredWebPorts(row)
	appliedHTTP, appliedHTTPS := appliedWebPorts(row)
	agentHTTP, agentHTTPS := agentWebPorts(row)
	v := ProxyPorts{
		HTTP: httpPort, HTTPS: httpsPort, AppliedHTTP: appliedHTTP, AppliedHTTPS: appliedHTTPS,
		Staged: portsStaged(row), SetHTTP: intPtr(row.ProxyHttpPort), SetHTTPS: intPtr(row.ProxyHttpsPort),
		AgentHTTP: agentHTTP, AgentHTTPS: agentHTTPS, BusyOther: []int{}, Supported: SupportsProxyPorts(row),
	}
	if rp := factsPorts(row); rp != nil {
		v.Reported, v.Source = true, rp.Source
		v.BusyOther = append(v.BusyOther, rp.BusyOther...)
	}
	return v
}

// l4Reservation is a TCP/UDP route port reserved on the server.
type l4Reservation struct {
	Protocol string
	Port     int
	Service  string
}

// ValidatePorts checks the effective web ports of a server: in range, different
// from each other and from the proxy's admin port (422 invalid_proxy_ports), and
// not a port a TCP or UDP route service reserved (422 proxy_port_clash). The
// HTTPS port is published for tcp and udp, the HTTP port for tcp.
func ValidatePorts(httpPort, httpsPort int, reserved []l4Reservation) error {
	if err := agentprotocol.ValidateProxyPorts(httpPort, httpsPort, agentprotocol.ProxyAdminPort); err != nil {
		return apierr.Unprocessable("invalid_proxy_ports", err.Error())
	}
	for _, r := range reserved {
		switch {
		case r.Protocol == agentprotocol.ProtoTCP && r.Port == httpPort,
			r.Protocol == agentprotocol.ProtoTCP && r.Port == httpsPort,
			r.Protocol == agentprotocol.ProtoUDP && r.Port == httpsPort:
			return apierr.Unprocessable("proxy_port_clash", fmt.Sprintf("%s port %d is reserved by the route service %s on this server; move that route to another port or choose different proxy ports", strings.ToUpper(r.Protocol), r.Port, r.Service))
		}
	}
	return nil
}

func (s *Service) reservations(ctx context.Context, q *db.Queries, serverID uuid.UUID) ([]l4Reservation, error) {
	rows, err := q.ListServerL4Ports(ctx, serverID)
	if err != nil {
		return nil, fmt.Errorf("list l4 ports: %w", err)
	}
	out := make([]l4Reservation, len(rows))
	for i, r := range rows {
		out[i] = l4Reservation{Protocol: r.Protocol, Port: int(r.ListenPort), Service: r.ServiceName}
	}
	return out, nil
}

// requireInstanceAdmin allows the instance administrator (the first account; a
// token counts through its creator and needs the admin scope). Moving the
// proxy's ports can cut the dashboard off, so it is not a team-admin setting.
func (s *Service) requireInstanceAdmin(ctx context.Context) error {
	p, err := policy.Authenticated(ctx)
	if err != nil {
		return err
	}
	if p.Type != auth.TypeUser && !policy.HasScope(p, policy.ScopeAdmin) {
		return apierr.Forbidden()
	}
	if s.admins == nil {
		return apierr.Forbidden()
	}
	ok, err := s.admins.IsInstanceAdmin(ctx, p.UserID)
	if err != nil {
		return err
	}
	if !ok {
		return apierr.New(http.StatusForbidden, "instance_admin_required", "Only the instance administrator can change the proxy ports.")
	}
	return nil
}

// PortsInput is a change of the ports setting: nil keeps a port, 0 resets it to
// the agent's own.
type PortsInput struct {
	HTTP  *int
	HTTPS *int
}

// applyPortsInput merges in into the server's staged settings. It returns the
// new settings, the audit metadata of what changed (port or nil for a reset)
// and whether anything changed. Ranges are checked here, the effective pair by
// ValidatePorts.
func applyPortsInput(row db.Server, in PortsInput) (httpPort, httpsPort *int32, changed map[string]any, err error) {
	httpPort, httpsPort = row.ProxyHttpPort, row.ProxyHttpsPort
	changed = map[string]any{}
	for _, p := range []struct {
		name string
		in   *int
		cur  **int32
	}{{"httpPort", in.HTTP, &httpPort}, {"httpsPort", in.HTTPS, &httpsPort}} {
		if p.in == nil {
			continue
		}
		if *p.in < 0 || *p.in > agentprotocol.MaxProxyPort {
			return nil, nil, nil, apierr.Unprocessable("invalid_proxy_ports", fmt.Sprintf("%s must be between %d and %d (0 resets it to the agent's own port)", p.name, agentprotocol.MinProxyPort, agentprotocol.MaxProxyPort))
		}
		next := int32Of(p.in)
		if sameInt32(*p.cur, next) {
			continue
		}
		*p.cur = next
		if next == nil {
			changed[p.name] = nil
		} else {
			changed[p.name] = int(*next)
		}
	}
	return httpPort, httpsPort, changed, nil
}

// stagedPortRow is row with the ports setting of a PortsInput applied (a
// preview that is not stored).
func stagedPortRow(row db.Server, in PortsInput) (db.Server, error) {
	h, s, _, err := applyPortsInput(row, in)
	if err != nil {
		return row, err
	}
	row.ProxyHttpPort, row.ProxyHttpsPort = h, s
	return row, nil
}

// portsPlan adds the ports part of "Deploy proxy" to p: diff entries, the
// restart and its reason, and the warnings (a port another process holds, the
// dashboard URL, HTTP-01 certificates).
func (s *Service) portsPlan(ctx context.Context, srv db.Server, p *ProxyPlan) error {
	wantHTTP, wantHTTPS := desiredWebPorts(srv)
	haveHTTP, haveHTTPS := appliedWebPorts(srv)
	if wantHTTP == haveHTTP && wantHTTPS == haveHTTPS {
		return nil
	}
	if wantHTTP != haveHTTP {
		p.Diff = append(p.Diff, ProxyPlanChange{Field: "proxy.httpPort", From: fmt.Sprint(haveHTTP), To: fmt.Sprint(wantHTTP)})
	}
	if wantHTTPS != haveHTTPS {
		p.Diff = append(p.Diff, ProxyPlanChange{Field: "proxy.httpsPort", From: fmt.Sprint(haveHTTPS), To: fmt.Sprint(wantHTTPS)})
	}
	p.RestartRequired = true
	p.Reasons = append(p.Reasons, "Changing a published port recreates the proxy container (about 1 second of dropped connections).")

	// Another process on a port the proxy moves to: the agent refuses the deploy
	// and leaves the running proxy alone.
	if rp := factsPorts(srv); rp != nil {
		for _, port := range rp.BusyOther {
			if (port == wantHTTP && wantHTTP != haveHTTP) || (port == wantHTTPS && wantHTTPS != haveHTTPS) {
				p.Warnings = append(p.Warnings, fmt.Sprintf("Port %d is in use by another process on %s right now. The agent refuses to move the proxy onto it and keeps the proxy running on :%d and :%d; stop that process first (for example another proxy such as Coolify's).", port, srv.Name, haveHTTP, haveHTTPS))
			}
		}
	}
	if srv.IsControlPlane {
		msg := "This server runs the NebulaCtrl dashboard. NEBULACTRL_PUBLIC_URL and the agent's own URL are not changed"
		if wantHTTPS != haveHTTPS {
			msg += fmt.Sprintf(", but the dashboard is then served on the HTTPS port %d (https://<dashboard host>:%d); update the address you open and NEBULACTRL_PUBLIC_URL if you rely on it", wantHTTPS, wantHTTPS)
		}
		msg += "."
		if wantHTTP != 80 {
			msg += " The dashboard's certificate cannot be issued or renewed with HTTP-01 off :80 unless it uses a DNS-01 resolver."
		}
		p.Warnings = append(p.Warnings, msg)
	}
	if wantHTTP != haveHTTP {
		ws, err := dns01.HTTP01Warnings(ctx, s.q, srv, wantHTTP)
		if err != nil {
			return err
		}
		const most = 10
		if len(ws) > most {
			rest := len(ws) - most
			ws = append(ws[:most:most], fmt.Sprintf("%d more domains on this server depend on HTTP-01 too.", rest))
		}
		p.Warnings = append(p.Warnings, ws...)
	}
	return nil
}
