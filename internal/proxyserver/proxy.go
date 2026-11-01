package proxyserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/dns01"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// ProxyUpstreamPolicy is the upstream policy as the API shows it.
type ProxyUpstreamPolicy struct {
	AllowLoopback    bool `json:"allowLoopback" doc:"Allow external upstreams on 127.0.0.0/8 and localhost (administrators only)"`
	AllowLinkLocal   bool `json:"allowLinkLocal" doc:"Allow link-local addresses incl. cloud metadata 169.254.169.254 (administrators only)"`
	AllowHostGateway bool `json:"allowHostGateway" doc:"Resolve host.docker.internal to the Docker host from the proxy container"`
}

func policyView(p agentprotocol.UpstreamPolicy) ProxyUpstreamPolicy {
	return ProxyUpstreamPolicy{AllowLoopback: p.AllowLoopback, AllowLinkLocal: p.AllowLinkLocal, AllowHostGateway: p.AllowHostGateway}
}

// ProxyL4Port is a TCP/UDP listen port reserved on the server by a route service.
type ProxyL4Port struct {
	Protocol    string    `json:"protocol" enum:"tcp,udp"`
	Port        int       `json:"port"`
	ServiceID   uuid.UUID `json:"serviceId"`
	ServiceName string    `json:"serviceName"`
	Deployed    bool      `json:"deployed" doc:"False while the route service has no current deployment (the port is reserved but not listening)"`
}

// ProxyTailnet is the per-server tailnet state.
type ProxyTailnet struct {
	Mode                  string                       `json:"mode" enum:"off,managed,host" doc:"Desired mode (staged until Deploy proxy)"`
	AppliedMode           string                       `json:"appliedMode" enum:"off,managed,host"`
	Hostname              string                       `json:"hostname" doc:"Desired machine name of the managed node"`
	MagicDNS              bool                         `json:"magicDns"`
	Status                *agentprotocol.TailnetStatus `json:"status,omitempty" doc:"Observed state reported by the agent; absent when never reported"`
	StatusAt              *time.Time                   `json:"statusAt,omitempty"`
	HostTailscale         agentprotocol.HostTailscale  `json:"hostTailscale"`
	IntegrationConfigured bool                         `json:"integrationConfigured"`
}

// ProxyDeployInfo is the last "Deploy proxy" command of a server.
type ProxyDeployInfo struct {
	CommandID uuid.UUID  `json:"commandId"`
	Status    string     `json:"status" enum:"pending,sent,acked,succeeded,failed,expired,canceled"`
	Message   string     `json:"message"`
	StartedAt time.Time  `json:"startedAt"`
	EndedAt   *time.Time `json:"endedAt"`
}

// ServerProxy is the server-level proxy state.
type ServerProxy struct {
	Status            string              `json:"status" doc:"Traefik state reported by the agent: ok, starting, error, or empty"`
	Error             string              `json:"error"`
	Capabilities      []string            `json:"capabilities"`
	AgentTooOld       bool                `json:"agentTooOld" doc:"True when the agent lacks proxy.v2; staging proxy features returns 422 agent_too_old"`
	Generation        int64               `json:"generation"`
	AppliedGeneration int64               `json:"appliedGeneration"`
	Staged            bool                `json:"staged" doc:"Staged server-level changes await Deploy proxy"`
	UpstreamPolicy    ProxyUpstreamPolicy `json:"upstreamPolicy"`
	AppliedPolicy     ProxyUpstreamPolicy `json:"appliedUpstreamPolicy"`
	L4Ports           []ProxyL4Port       `json:"l4Ports"`
	Certificates      []ServerCertificate `json:"certificates"`
	Tailnet           ProxyTailnet        `json:"tailnet"`
	Ports             ProxyPorts          `json:"ports"`
	LastDeploy        *ProxyDeployInfo    `json:"lastDeploy,omitempty"`
}

func (s *Service) view(ctx context.Context, q *db.Queries, srv db.Server) (ServerProxy, error) {
	facts := factsOf(srv)
	v := ServerProxy{
		Status: derefStr(srv.ProxyStatus), Error: derefStr(srv.ProxyError), Capabilities: nonNilStrings(facts.Capabilities),
		AgentTooOld: !slices.Contains(facts.Capabilities, agentprotocol.CapProxyV2),
		Generation:  srv.ProxyGeneration, AppliedGeneration: srv.ProxyAppliedGeneration,
		Staged:         srv.ProxyGeneration > srv.ProxyAppliedGeneration,
		UpstreamPolicy: policyView(PolicyFromRow(srv.UpstreamPolicy)), AppliedPolicy: policyView(PolicyFromRow(srv.AppliedUpstreamPolicy)),
		L4Ports: []ProxyL4Port{}, Ports: portsView(srv),
	}
	// The automatic Cloudflare resolver is derived, not staged: a server that
	// starts serving a name in a connected zone differs from what it applied
	// without any generation having moved.
	if !v.Staged {
		dp, err := dns01.PlanFor(ctx, q, srv)
		if err != nil {
			return ServerProxy{}, err
		}
		v.Staged = len(dp.Diff) > 0
	}
	certs, err := s.certViews(ctx, q, srv.ID)
	if err != nil {
		return ServerProxy{}, err
	}
	v.Certificates = certs
	ports, err := q.ListServerL4Ports(ctx, srv.ID)
	if err != nil {
		return ServerProxy{}, fmt.Errorf("list l4 ports: %w", err)
	}
	for _, p := range ports {
		v.L4Ports = append(v.L4Ports, ProxyL4Port{Protocol: p.Protocol, Port: int(p.ListenPort), ServiceID: p.ServiceID, ServiceName: p.ServiceName, Deployed: p.Deployed})
	}
	v.Tailnet = ProxyTailnet{
		Mode: srv.TailnetMode, AppliedMode: srv.TailnetAppliedMode, Hostname: derefStr(srv.TailnetHostname), MagicDNS: srv.TailnetMagicDns,
		StatusAt: srv.TailnetStatusAt, IntegrationConfigured: s.integrationConfigured(ctx, srv.WorkspaceID),
	}
	if facts.HostTailscale != nil {
		v.Tailnet.HostTailscale = *facts.HostTailscale
	}
	if len(srv.TailnetStatus) > 0 {
		var ts agentprotocol.TailnetStatus
		if json.Unmarshal(srv.TailnetStatus, &ts) == nil {
			v.Tailnet.Status = &ts
		}
	}
	last, err := q.LastProxyConfigureCommand(ctx, srv.ID)
	switch {
	case err == nil:
		msg := last.Error
		if msg == "" {
			msg = resultMessage(last.Result)
		}
		v.LastDeploy = &ProxyDeployInfo{CommandID: last.ID, Status: last.Status, Message: msg, StartedAt: last.CreatedAt, EndedAt: last.FinishedAt}
	case errors.Is(err, pgx.ErrNoRows):
	default:
		return ServerProxy{}, fmt.Errorf("last proxy command: %w", err)
	}
	return v, nil
}

// GetProxy returns the server-level proxy state.
func (s *Service) GetProxy(ctx context.Context, serverID uuid.UUID) (ServerProxy, error) {
	srv, err := s.loadServer(ctx, s.q, policy.ScopeServersRead, serverID)
	if err != nil {
		return ServerProxy{}, err
	}
	return s.view(ctx, s.q, srv)
}

// SettingsInput is a PATCH of the server proxy settings. Nil fields are unchanged.
type SettingsInput struct {
	AllowLoopback    *bool
	AllowLinkLocal   *bool
	AllowHostGateway *bool

	TailnetMode     *string
	TailnetHostname *string
	TailnetMagicDNS *bool

	// HTTPPort and HTTPSPort set the host ports of the proxy (instance
	// administrator). nil = unchanged, 0 = the agent's own port.
	HTTPPort  *int
	HTTPSPort *int
}

// UpdateSettings stages upstream policy and tailnet changes (proxy_generation is
// bumped; nothing reaches the agent until Deploy proxy).
func (s *Service) UpdateSettings(ctx context.Context, serverID uuid.UUID, in SettingsInput) (ServerProxy, error) {
	srv, err := s.loadServer(ctx, s.q, policy.ScopeProxyWrite, serverID)
	if err != nil {
		return ServerProxy{}, err
	}
	if err := requireV2(srv); err != nil {
		return ServerProxy{}, err
	}

	pol := PolicyFromRow(srv.UpstreamPolicy)
	oldPol := pol
	changed := map[string]any{}
	if in.AllowLoopback != nil && *in.AllowLoopback != pol.AllowLoopback {
		pol.AllowLoopback = *in.AllowLoopback
		changed["allowLoopback"] = pol.AllowLoopback
	}
	if in.AllowLinkLocal != nil && *in.AllowLinkLocal != pol.AllowLinkLocal {
		pol.AllowLinkLocal = *in.AllowLinkLocal
		changed["allowLinkLocal"] = pol.AllowLinkLocal
	}
	if pol.AllowLoopback != oldPol.AllowLoopback || pol.AllowLinkLocal != oldPol.AllowLinkLocal {
		// Loosening (or tightening) the SSRF guard is an administrator decision.
		if err := policy.Require(ctx, policy.ScopeAdmin, nil); err != nil {
			return ServerProxy{}, err
		}
	}
	if in.AllowHostGateway != nil && *in.AllowHostGateway != pol.AllowHostGateway {
		pol.AllowHostGateway = *in.AllowHostGateway
		changed["allowHostGateway"] = pol.AllowHostGateway
	}

	mode, host, magic := srv.TailnetMode, derefStr(srv.TailnetHostname), srv.TailnetMagicDns
	tailnetTouched := false
	if in.TailnetMode != nil && *in.TailnetMode != mode {
		switch *in.TailnetMode {
		case agentprotocol.TailnetOff, agentprotocol.TailnetManaged, agentprotocol.TailnetHost:
		default:
			return ServerProxy{}, apierr.Unprocessable("validation_failed", "tailnet.mode must be off, managed or host")
		}
		mode = *in.TailnetMode
		changed["tailnetMode"] = mode
		tailnetTouched = true
	}
	if in.TailnetHostname != nil {
		h := strings.ToLower(strings.TrimSpace(*in.TailnetHostname))
		if h != host {
			if h != "" && !ValidTailnetHostname(h) {
				return ServerProxy{}, apierr.Unprocessable("invalid_tailnet_hostname", "tailnet.hostname must be a lowercase DNS label (letters, digits, hyphens, at most 63 characters)")
			}
			host = h
			changed["tailnetHostname"] = host
			tailnetTouched = true
		}
	}
	if in.TailnetMagicDNS != nil && *in.TailnetMagicDNS != magic {
		magic = *in.TailnetMagicDNS
		changed["tailnetMagicDns"] = magic
		tailnetTouched = true
	}
	switch mode {
	case agentprotocol.TailnetManaged:
		if tailnetTouched || mode != srv.TailnetMode {
			if !s.integrationConfigured(ctx, srv.WorkspaceID) {
				return ServerProxy{}, apierr.Unprocessable("tailnet_not_configured", "connect Tailscale in Team settings → Integrations before enabling a managed tailnet")
			}
		}
		if host == "" {
			host = DefaultTailnetHostname(srv.Name)
			if host != derefStr(srv.TailnetHostname) {
				changed["tailnetHostname"] = host
				tailnetTouched = true
			}
		}
	case agentprotocol.TailnetHost:
		if mode != srv.TailnetMode {
			if hs := factsOf(srv).HostTailscale; hs == nil || !hs.Detected {
				return ServerProxy{}, apierr.Unprocessable("host_tailscale_not_detected", "the agent did not detect Tailscale on the host (no 100.64.0.0/10 interface address); install and log in to Tailscale on the host first")
			}
		}
	}

	// The proxy's host ports: the instance administrator's decision (moving them
	// can cut the dashboard off). nil keeps a port, 0 resets it to the agent's own.
	var newHTTP, newHTTPS *int32
	portsTouched := false
	if in.HTTPPort != nil || in.HTTPSPort != nil {
		var portChanges map[string]any
		newHTTP, newHTTPS, portChanges, err = applyPortsInput(srv, PortsInput{HTTP: in.HTTPPort, HTTPS: in.HTTPSPort})
		if err != nil {
			return ServerProxy{}, err
		}
		if len(portChanges) > 0 {
			if err := s.requireInstanceAdmin(ctx); err != nil {
				return ServerProxy{}, err
			}
			if (newHTTP != nil || newHTTPS != nil) && !SupportsProxyPorts(srv) {
				return ServerProxy{}, ErrAgentTooOldForPorts()
			}
			staged := srv
			staged.ProxyHttpPort, staged.ProxyHttpsPort = newHTTP, newHTTPS
			wantHTTP, wantHTTPS := desiredWebPorts(staged)
			reserved, err := s.reservations(ctx, s.q, serverID)
			if err != nil {
				return ServerProxy{}, err
			}
			if err := ValidatePorts(wantHTTP, wantHTTPS, reserved); err != nil {
				return ServerProxy{}, err
			}
			portsTouched = true
			maps.Copy(changed, portChanges)
		}
	}

	if len(changed) == 0 {
		return s.view(ctx, s.q, srv)
	}
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		if _, err := q.LockServerForProxy(ctx, db.LockServerForProxyParams{ID: serverID, WorkspaceID: srv.WorkspaceID}); err != nil {
			return err
		}
		if pol != oldPol {
			raw, err := json.Marshal(pol)
			if err != nil {
				return err
			}
			if err := q.UpdateServerUpstreamPolicy(ctx, db.UpdateServerUpstreamPolicyParams{ID: serverID, UpstreamPolicy: raw}); err != nil {
				return err
			}
		}
		if tailnetTouched {
			var hp *string
			if host != "" {
				hp = &host
			}
			if err := q.UpdateServerTailnetSettings(ctx, db.UpdateServerTailnetSettingsParams{ID: serverID, TailnetMode: mode, TailnetHostname: hp, TailnetMagicDns: magic}); err != nil {
				return err
			}
		}
		if portsTouched {
			if err := q.UpdateServerProxyPorts(ctx, db.UpdateServerProxyPortsParams{ID: serverID, HttpPort: newHTTP, HttpsPort: newHTTPS}); err != nil {
				return err
			}
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: "server.proxy.update", ResourceType: "server", ResourceID: serverID.String(), Metadata: changed,
		})
	})
	if err != nil {
		return ServerProxy{}, fmt.Errorf("update proxy settings: %w", err)
	}
	s.publish(serverID)
	return s.GetProxy(ctx, serverID)
}

// ProxyPlanChange is one staged difference.
type ProxyPlanChange struct {
	Field string `json:"field"`
	From  string `json:"from"`
	To    string `json:"to"`
}

// ProxyPlan previews what "Deploy proxy" would do.
type ProxyPlan struct {
	RestartRequired bool              `json:"restartRequired" doc:"The proxy container is recreated (about 1 second of dropped connections)"`
	Reasons         []string          `json:"reasons"`
	Warnings        []string          `json:"warnings"`
	Diff            []ProxyPlanChange `json:"diff"`
}

func boolStr(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// needsProvision reports whether the desired tailnet needs a fresh node and key.
func needsProvision(srv db.Server, reprovision bool) bool {
	if srv.TailnetMode != agentprotocol.TailnetManaged {
		return false
	}
	return reprovision || srv.TailnetAppliedMode != agentprotocol.TailnetManaged ||
		derefStr(srv.TailnetAppliedHostname) != derefStr(srv.TailnetHostname)
}

// magicDNSEffective is whether the proxy container carries the tailnet resolver.
func magicDNSEffective(mode string, magic bool) bool {
	return mode == agentprotocol.TailnetManaged && magic
}

func (s *Service) plan(ctx context.Context, srv db.Server, reprovision bool) (ProxyPlan, error) {
	p := ProxyPlan{Reasons: []string{}, Warnings: []string{}, Diff: []ProxyPlanChange{}}
	if reprovision && srv.TailnetMode != agentprotocol.TailnetManaged {
		return p, apierr.Unprocessable("reprovision_not_applicable", "re-provisioning needs the tailnet mode managed")
	}
	switch srv.TailnetMode {
	case agentprotocol.TailnetManaged:
		if !s.integrationConfigured(ctx, srv.WorkspaceID) {
			return p, apierr.Unprocessable("tailnet_not_configured", "connect Tailscale in Team settings → Integrations before deploying a managed tailnet")
		}
	case agentprotocol.TailnetHost:
		if hs := factsOf(srv).HostTailscale; hs == nil || !hs.Detected {
			return p, apierr.Unprocessable("host_tailscale_not_detected", "the agent no longer detects Tailscale on the host")
		}
	}

	certs, err := s.q.ListServerCertificates(ctx, srv.ID)
	if err != nil {
		return p, fmt.Errorf("list certificates: %w", err)
	}
	for _, c := range certs {
		switch {
		case c.DeletedAt != nil && c.AppliedAt != nil:
			p.Diff = append(p.Diff, ProxyPlanChange{Field: "certificate." + c.Name, From: "installed", To: "removed"})
		case c.DeletedAt == nil && c.AppliedAt == nil:
			p.Diff = append(p.Diff, ProxyPlanChange{Field: "certificate." + c.Name, From: "-", To: "installed (" + strings.Join(c.Domains, ", ") + ")"})
		}
	}

	want, have := PolicyFromRow(srv.UpstreamPolicy), PolicyFromRow(srv.AppliedUpstreamPolicy)
	if want.AllowLoopback != have.AllowLoopback {
		p.Diff = append(p.Diff, ProxyPlanChange{Field: "upstreamPolicy.allowLoopback", From: boolStr(have.AllowLoopback), To: boolStr(want.AllowLoopback)})
	}
	if want.AllowLinkLocal != have.AllowLinkLocal {
		p.Diff = append(p.Diff, ProxyPlanChange{Field: "upstreamPolicy.allowLinkLocal", From: boolStr(have.AllowLinkLocal), To: boolStr(want.AllowLinkLocal)})
	}
	if want.AllowHostGateway != have.AllowHostGateway {
		p.Diff = append(p.Diff, ProxyPlanChange{Field: "upstreamPolicy.allowHostGateway", From: boolStr(have.AllowHostGateway), To: boolStr(want.AllowHostGateway)})
		p.RestartRequired = true
		p.Reasons = append(p.Reasons, fmt.Sprintf("Changing host gateway access updates the proxy container's %s mapping, so the proxy container is recreated.", agentprotocol.HostGatewayName))
	}

	if srv.TailnetMode != srv.TailnetAppliedMode {
		p.Diff = append(p.Diff, ProxyPlanChange{Field: "tailnet.mode", From: srv.TailnetAppliedMode, To: srv.TailnetMode})
	}
	if srv.TailnetMode == agentprotocol.TailnetManaged && derefStr(srv.TailnetHostname) != derefStr(srv.TailnetAppliedHostname) {
		p.Diff = append(p.Diff, ProxyPlanChange{Field: "tailnet.hostname", From: orDash(derefStr(srv.TailnetAppliedHostname)), To: derefStr(srv.TailnetHostname)})
	}
	if magicDNSEffective(srv.TailnetMode, srv.TailnetMagicDns) != magicDNSEffective(srv.TailnetAppliedMode, srv.TailnetAppliedMagicDns) {
		p.Diff = append(p.Diff, ProxyPlanChange{
			Field: "tailnet.magicDns", From: boolStr(magicDNSEffective(srv.TailnetAppliedMode, srv.TailnetAppliedMagicDns)),
			To: boolStr(magicDNSEffective(srv.TailnetMode, srv.TailnetMagicDns)),
		})
		p.RestartRequired = true
		p.Reasons = append(p.Reasons, "MagicDNS adds the tailnet resolver (100.100.100.100) to the proxy container's DNS servers, so the proxy container is recreated.")
	}

	prov := needsProvision(srv, reprovision)
	host := derefStr(srv.TailnetHostname)
	if prov {
		switch {
		case srv.TailnetAppliedMode == agentprotocol.TailnetManaged && reprovision && host == derefStr(srv.TailnetAppliedHostname):
			p.Warnings = append(p.Warnings, fmt.Sprintf("Re-provisioning deletes the current device and registers %q again with a new key; it gets a new tailnet IP.", host))
		case srv.TailnetAppliedMode == agentprotocol.TailnetManaged:
			p.Warnings = append(p.Warnings, fmt.Sprintf("Changing the hostname re-provisions the node: the old device is deleted from your tailnet and %q is registered with a new tailnet IP.", host))
		default:
			p.Warnings = append(p.Warnings, fmt.Sprintf("Creates a device %q on your tailnet with a single-use, pre-authorized key (valid for 1 hour) tagged with the integration's default tags. Needs /dev/net/tun and NET_ADMIN for the Tailscale container on the server.", host))
		}
		if reprovision {
			p.Diff = append(p.Diff, ProxyPlanChange{Field: "tailnet.provision", From: "-", To: "re-provision requested"})
		}
	}
	if srv.TailnetMode == agentprotocol.TailnetOff && srv.TailnetAppliedMode == agentprotocol.TailnetManaged {
		name := derefStr(srv.TailnetAppliedHostname)
		var ts agentprotocol.TailnetStatus
		if json.Unmarshal(srv.TailnetStatus, &ts) == nil && ts.FQDN != "" {
			name = ts.FQDN
		}
		p.Warnings = append(p.Warnings, fmt.Sprintf("Disabling removes the Tailscale container and its state and deletes the device %q from your tailnet. Re-enabling creates a new device with a new tailnet IP.", name))
	}
	if srv.TailnetMode == agentprotocol.TailnetHost && srv.TailnetAppliedMode != agentprotocol.TailnetHost {
		p.Warnings = append(p.Warnings, "Host mode relies on the Tailscale running on the host itself; NebulaCtrl does not manage that node. Routes may target the host's tailnet peers by their 100.x address.")
	}
	dp, err := dns01.PlanFor(ctx, s.q, srv)
	if err != nil {
		return p, err
	}
	for _, c := range dp.Diff {
		p.Diff = append(p.Diff, ProxyPlanChange{Field: c.Field, From: c.From, To: c.To})
	}
	if dp.Restart {
		p.RestartRequired = true
	}
	p.Reasons = append(p.Reasons, dp.Reasons...)
	p.Warnings = append(p.Warnings, dp.Warnings...)
	if err := s.portsPlan(ctx, srv, &p); err != nil {
		return p, err
	}
	if p.RestartRequired && srv.TailnetMode == agentprotocol.TailnetManaged && srv.TailnetAppliedMode == agentprotocol.TailnetManaged {
		p.Reasons = append(p.Reasons, "The Tailscale container shares the proxy container's network namespace and is recreated with it (its identity persists, no new key).")
	}
	if srv.Status != "online" {
		p.Warnings = append(p.Warnings, "The server is offline. The command is delivered when it reconnects (it expires after 15 minutes).")
	}
	return p, nil
}

// PlanDeploy previews "Deploy proxy" for a server. A non-nil preview plans the
// deploy as if that ports setting were staged too (nothing is stored): the web
// UI shows it before it stages the change.
func (s *Service) PlanDeploy(ctx context.Context, serverID uuid.UUID, reprovision bool, preview *PortsInput) (ProxyPlan, error) {
	srv, err := s.loadServer(ctx, s.q, policy.ScopeProxyWrite, serverID)
	if err != nil {
		return ProxyPlan{}, err
	}
	if err := requireV2(srv); err != nil {
		return ProxyPlan{}, err
	}
	if preview != nil {
		if srv, err = stagedPortRow(srv, *preview); err != nil {
			return ProxyPlan{}, err
		}
		httpPort, httpsPort := desiredWebPorts(srv)
		reserved, err := s.reservations(ctx, s.q, serverID)
		if err != nil {
			return ProxyPlan{}, err
		}
		if err := ValidatePorts(httpPort, httpsPort, reserved); err != nil {
			return ProxyPlan{}, err
		}
	}
	return s.plan(ctx, srv, reprovision)
}

// ProxyDeployResult is the 202 answer of Deploy.
type ProxyDeployResult struct {
	CommandID  uuid.UUID `json:"commandId"`
	Generation int64     `json:"generation"`
	Plan       ProxyPlan `json:"plan"`
}

// commandKey is the idempotency key of a proxy_configure command. It embeds
// the generation the command was built from, which the result handler reads.
func commandKey(serverID uuid.UUID, generation int64) string {
	return fmt.Sprintf("proxy_configure:%s:%d:%s", serverID, generation, uuid.NewString())
}

// generationOfKey extracts the generation from a commandKey.
func generationOfKey(key string) (int64, bool) {
	parts := strings.Split(key, ":")
	if len(parts) != 4 || parts[0] != "proxy_configure" {
		return 0, false
	}
	var g int64
	if _, err := fmt.Sscanf(parts[2], "%d", &g); err != nil {
		return 0, false
	}
	return g, true
}

// desiredPayload builds the proxy_configure payload from DESIRED state. It
// contains no secret: keys are referenced by CertRef.KeyRef and the auth key is
// attached at send time.
func desiredPayload(ctx context.Context, q *db.Queries, srv db.Server, reprovision bool) (agentprotocol.ProxyConfigurePayload, error) {
	certs, err := q.ListDesiredCertificates(ctx, srv.ID)
	if err != nil {
		return agentprotocol.ProxyConfigurePayload{}, fmt.Errorf("list certificates: %w", err)
	}
	pc := agentprotocol.ProxyConfig{UpstreamPolicy: PolicyFromRow(srv.UpstreamPolicy)}
	if srv.ProxyHttpPort != nil {
		pc.HTTPPort = int(*srv.ProxyHttpPort)
	}
	if srv.ProxyHttpsPort != nil {
		pc.HTTPSPort = int(*srv.ProxyHttpsPort)
	}
	for _, c := range certs {
		pc.Certificates = append(pc.Certificates, agentprotocol.CertRef{
			ID: c.ID.String(), Name: c.Name, Domains: c.Domains, CertPEM: c.CertPem, KeyRef: agentprotocol.SecretCertPrefix + c.ID.String(),
		})
	}
	dns, err := dns01.Desired(ctx, q, srv.ID)
	if err != nil {
		return agentprotocol.ProxyConfigurePayload{}, err
	}
	dns.Apply(&pc)
	switch srv.TailnetMode {
	case agentprotocol.TailnetManaged:
		pc.Tailnet = &agentprotocol.TailnetSpec{
			Mode: agentprotocol.TailnetManaged, Hostname: derefStr(srv.TailnetHostname), MagicDNS: srv.TailnetMagicDns,
			Provision: needsProvision(srv, reprovision),
		}
	case agentprotocol.TailnetHost:
		pc.Tailnet = &agentprotocol.TailnetSpec{Mode: agentprotocol.TailnetHost}
	default:
		if srv.TailnetAppliedMode == agentprotocol.TailnetManaged {
			pc.Tailnet = &agentprotocol.TailnetSpec{Mode: agentprotocol.TailnetOff, Remove: true}
		}
	}
	p := agentprotocol.ProxyConfigurePayload{Proxy: pc, ACMEEmail: derefStr(srv.AcmeEmail)}
	ta := srv.TrafficAnalyticsEnabled
	p.TrafficAnalytics = &ta
	return p, nil
}

// withTable adds the numbered route table to p. It is the last thing a
// transaction does before it enqueues p: the numbering locks the server's
// counter until the transaction ends, so it must be the last lock taken (see
// db.Queries.NumberRouteTable). A nil source sends no table.
func withTable(ctx context.Context, q *db.Queries, p *agentprotocol.ProxyConfigurePayload, serverID uuid.UUID, routes RouteSource) error {
	if routes == nil {
		return nil
	}
	seq, err := q.NumberRouteTable(ctx, serverID) // before the routes are read
	if err != nil {
		return fmt.Errorf("number the route table: %w", err)
	}
	table, err := routes(ctx, q, serverID)
	if err != nil {
		return fmt.Errorf("build route table: %w", err)
	}
	p.Routes, p.ReplaceRoutes, p.TableSeq = table, true, seq
	return nil
}

// Deploy sends proxy_configure with the DESIRED server-level configuration.
func (s *Service) Deploy(ctx context.Context, serverID uuid.UUID, reprovision bool) (ProxyDeployResult, error) {
	srv, err := s.loadServer(ctx, s.q, policy.ScopeProxyWrite, serverID)
	if err != nil {
		return ProxyDeployResult{}, err
	}
	if err := requireV2(srv); err != nil {
		return ProxyDeployResult{}, err
	}
	if srv.Status == "revoked" {
		return ProxyDeployResult{}, apierr.Conflict("server_revoked", "the server's agent was revoked")
	}
	var out ProxyDeployResult
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		locked, err := q.LockServerForProxy(ctx, db.LockServerForProxyParams{ID: serverID, WorkspaceID: srv.WorkspaceID})
		if err != nil {
			return err
		}
		busy, err := q.ProxyConfigureInFlight(ctx, serverID)
		if err != nil {
			return err
		}
		if busy {
			return apierr.Conflict("proxy_deploy_in_progress", "a proxy deploy for this server is still in progress")
		}
		plan, err := s.plan(ctx, locked, reprovision)
		if err != nil {
			return err
		}
		payload, err := desiredPayload(ctx, q, locked, reprovision)
		if err != nil {
			return err
		}
		if err := agentprotocol.ValidateDNS01(payload.Proxy, payload.ACMEEmail); err != nil {
			return apierr.Unprocessable("invalid_dns01", err.Error())
		}
		if payload.Proxy.HTTPPort != 0 || payload.Proxy.HTTPSPort != 0 {
			if !SupportsProxyPorts(locked) {
				return ErrAgentTooOldForPorts()
			}
			wantHTTP, wantHTTPS := desiredWebPorts(locked)
			reserved, err := s.reservations(ctx, q, serverID)
			if err != nil {
				return err
			}
			if err := ValidatePorts(wantHTTP, wantHTTPS, reserved); err != nil {
				return err
			}
		}
		// The audit entry first, the numbered table (and its enqueue) last: the
		// route counter's lock must be the last this transaction takes.
		if err := audit.Record(ctx, tx, audit.Entry{
			Action: "server.proxy.deploy", ResourceType: "server", ResourceID: serverID.String(),
			Metadata: map[string]any{
				"generation": locked.ProxyGeneration, "reprovision": reprovision, "certificates": len(payload.Proxy.Certificates),
				"dnsResolvers": len(payload.Proxy.DNSResolvers), "wildcardCerts": len(payload.Proxy.WildcardCerts),
				"tailnetMode": locked.TailnetMode, "restartRequired": plan.RestartRequired,
				"httpPort": payload.Proxy.HTTPPort, "httpsPort": payload.Proxy.HTTPSPort,
			},
		}); err != nil {
			return err
		}
		if err := withTable(ctx, q, &payload, serverID, s.routes); err != nil {
			return err
		}
		raw, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		id, err := s.disp.Enqueue(ctx, tx, agentsCommand(serverID, raw, commandKey(serverID, locked.ProxyGeneration)))
		if err != nil {
			return fmt.Errorf("enqueue proxy_configure: %w", err)
		}
		out = ProxyDeployResult{CommandID: id, Generation: locked.ProxyGeneration, Plan: plan}
		return nil
	})
	if err != nil {
		if _, ok := apierr.As(err); ok {
			return ProxyDeployResult{}, err
		}
		return ProxyDeployResult{}, fmt.Errorf("deploy proxy: %w", err)
	}
	s.publish(serverID)
	return out, nil
}

// resultMessage extracts ProxyConfigureResult.Message from a stored ResultFrame.
func resultMessage(stored json.RawMessage) string {
	var rf agentprotocol.ResultFrame
	if len(stored) == 0 || json.Unmarshal(stored, &rf) != nil || len(rf.Data) == 0 {
		return ""
	}
	var r agentprotocol.ProxyConfigureResult
	if json.Unmarshal(rf.Data, &r) != nil {
		return ""
	}
	return r.Message
}
