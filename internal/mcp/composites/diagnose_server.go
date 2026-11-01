package composites

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/mcp/mcpkit"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/servers"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

const (
	diagServerNameScanPages = 10
	diagServerCommandLimit  = 20
	diagServerCommandLookup = 10
)

type diagnoseServerInput struct {
	Server string `json:"server" jsonschema:"the server: id or exact name"`
	Detail string `json:"detail,omitempty" jsonschema:"concise (default, 20 placed services) or full (100 placed services, all facts)"`
}

type diagnoseServerOutput struct {
	Envelope
	Verdict  string             `json:"verdict" jsonschema:"healthy, degraded, down or unknown"`
	Findings []Finding          `json:"findings"`
	Server   diagServerInfo     `json:"server"`
	Usage    *diagServerUsage   `json:"usage,omitempty" jsonschema:"the latest heartbeat sample"`
	Metrics  *diagServerMetrics `json:"metrics,omitempty" jsonschema:"the last hour of CPU, memory and disk"`
	Services diagPlacement      `json:"services"`
	Commands []diagCommand      `json:"commands" jsonschema:"agent commands that are pending, sent or acked without a result"`
	Drain    diagDrain          `json:"drain"`
}

type diagServerInfo struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	Status           string `json:"status" jsonschema:"pending, online, offline or revoked"`
	PublicIP         string `json:"publicIp,omitempty"`
	BaseDomain       string `json:"baseDomain,omitempty"`
	LastSeen         string `json:"lastSeen,omitempty" jsonschema:"how long ago the agent was last heard from"`
	LastSeenAt       string `json:"lastSeenAt,omitempty"`
	OfflineSeconds   int64  `json:"offlineSeconds,omitempty" jsonschema:"how long the server has been silent, when it is not online"`
	AgentVersion     string `json:"agentVersion,omitempty"`
	AgentProtocol    int    `json:"agentProtocol,omitempty"`
	ControlProtocol  int    `json:"controlPlaneProtocol" jsonschema:"the protocol version this control plane speaks"`
	DockerVersion    string `json:"dockerVersion,omitempty"`
	ComposeVersion   string `json:"composeVersion,omitempty"`
	OS               string `json:"os,omitempty"`
	Arch             string `json:"arch,omitempty"`
	Kernel           string `json:"kernel,omitempty"`
	CPUs             int    `json:"cpus,omitempty"`
	MemTotalBytes    uint64 `json:"memTotalBytes,omitempty"`
	DiskTotalBytes   uint64 `json:"diskTotalBytes,omitempty"`
	Port80Free       *bool  `json:"port80Free,omitempty" jsonschema:"fact reported by the agent: nothing listened on port 80 when it looked"`
	Port443Free      *bool  `json:"port443Free,omitempty"`
	ProxyStatus      string `json:"proxyStatus,omitempty" jsonschema:"ok, starting, error or empty before the first heartbeat"`
	ProxyError       any    `json:"proxyError,omitempty" jsonschema:"error the proxy reported, as an untrusted block"`
	ProxyV2          bool   `json:"proxyV2"`
	ProxyGeneration  int64  `json:"proxyGeneration"`
	ProxyApplied     int64  `json:"proxyAppliedGeneration"`
	AcceptWorkloads  bool   `json:"acceptWorkloads"`
	BuildEnabled     bool   `json:"buildEnabled"`
	ControlPlaneHost bool   `json:"controlPlaneHost"`
	TunnelState      string `json:"tunnelState,omitempty"`
}

type diagServerUsage struct {
	SampledAt       string  `json:"sampledAt"`
	Age             string  `json:"age"`
	CPUPercent      float64 `json:"cpuPercent"`
	MemUsedBytes    int64   `json:"memUsedBytes"`
	MemTotalBytes   int64   `json:"memTotalBytes"`
	MemUsedPercent  float64 `json:"memUsedPercent"`
	DiskUsedBytes   int64   `json:"diskUsedBytes"`
	DiskTotalBytes  int64   `json:"diskTotalBytes"`
	DiskUsedPercent float64 `json:"diskUsedPercent"`
	DiskAlert       bool    `json:"diskAlert"`
	DiskThreshold   int     `json:"diskAlertThresholdPercent"`
}

type diagServerMetrics struct {
	Window            string  `json:"window"`
	Samples           int     `json:"samples"`
	CPUAvgPercent     float64 `json:"cpuAvgPercent"`
	CPUMaxPercent     float64 `json:"cpuMaxPercent"`
	MemStartPercent   float64 `json:"memStartPercent"`
	MemNowPercent     float64 `json:"memNowPercent"`
	MemMaxPercent     float64 `json:"memMaxPercent"`
	DiskStartPercent  float64 `json:"diskStartPercent"`
	DiskNowPercent    float64 `json:"diskNowPercent"`
	DiskPointsPerHour float64 `json:"diskPointsPerHour" jsonschema:"disk growth in percentage points per hour over the window"`
}

type diagPlacement struct {
	Total      int            `json:"total" jsonschema:"live services whose current deployment runs here"`
	ByState    map[string]int `json:"byState"`
	Items      []diagPlaced   `json:"items" jsonschema:"problem services first; capped (detail=full lists more)"`
	NotListed  int            `json:"notListed,omitempty" jsonschema:"services beyond the cap"`
	NotVisible int            `json:"notVisible,omitempty" jsonschema:"services of projects the token cannot see"`
}

type diagPlaced struct {
	ID         string `json:"id"`
	Selector   string `json:"selector"`
	Kind       string `json:"kind"`
	State      string `json:"state" jsonschema:"observed container state, or the desired state when stopped"`
	Health     string `json:"health,omitempty"`
	Production bool   `json:"production"`
	Stale      bool   `json:"stale"`
}

type diagCommand struct {
	ID           string `json:"id"`
	Kind         string `json:"kind"`
	Status       string `json:"status" jsonschema:"pending, sent or acked"`
	Age          string `json:"age"`
	AgeSeconds   int64  `json:"ageSeconds"`
	DeploymentID string `json:"deploymentId,omitempty"`
	Service      string `json:"service,omitempty"`
}

type diagDrain struct {
	State           string `json:"state" jsonschema:"none, draining, drained or failed"`
	Message         any    `json:"message,omitempty" jsonschema:"drain progress or failure message, as an untrusted block"`
	StartedAt       string `json:"startedAt,omitempty"`
	AcceptWorkloads bool   `json:"acceptWorkloads"`
}

func (c *composites) diagnoseServer() mcpkit.Tool {
	return mcpkit.Tool{
		Name:  "diagnose_server",
		Title: "Diagnose a server",
		Description: "Use when a host looks wrong: services stale or offline, deploys stuck, proxy errors, disk or memory pressure. One call returns the server's facts (OS, Docker, agent and protocol versions, ports 80/443), the latest heartbeat usage and its age, proxy status and error, the last hour of CPU/memory/disk with the disk trend, drain state, how long it has been offline, the services placed on it with their states (problem services first, capped) and agent commands that are pending, sent or acked without a result. " +
			"Returns a verdict (healthy|degraded|down|unknown) and ranked findings with evidence. " +
			"Argument: server as an id or exact name. Sections that need more scopes (placed services need projects:read) are listed in errors[] while the rest is returned. Read-only. " +
			"Next: diagnose_service for a placed service, server_get for the raw record, find_issues for the whole estate.",
		Toolset: mcpkit.ToolsetCore,
		Scopes:  []string{policy.ScopeServersRead},
		Hints:   mcpkit.Hints{ReadOnly: true, Destructive: false, Idempotent: true, OpenWorld: false},
		Input:   diagnoseServerInput{},
		Output:  diagnoseServerOutput{},
		Call: func(ctx context.Context, p *auth.Principal, raw json.RawMessage) (any, error) {
			in, err := mcpkit.Decode[diagnoseServerInput](raw)
			if err != nil {
				return nil, err
			}
			detail, err := mcpkit.ParseDetail(in.Detail)
			if err != nil {
				return nil, err
			}
			if strings.TrimSpace(in.Server) == "" {
				return nil, invalid("server is required: an id or an exact name")
			}
			srv, err := c.resolveServer(ctx, in.Server)
			if err != nil {
				return nil, err
			}
			return c.runDiagnoseServer(ctx, p, srv, detail), nil
		},
	}
}

// resolveServer finds a server by id or exact (case-insensitive) name.
func (c *composites) resolveServer(ctx context.Context, sel string) (servers.Server, error) {
	sel = strings.TrimSpace(sel)
	if id, err := uuid.Parse(sel); err == nil {
		return c.Servers.Get(ctx, id)
	}
	var all []servers.Server
	cursor := ""
	for range diagServerNameScanPages {
		items, next, err := c.Servers.List(ctx, cursor, mcpkit.MaxPage)
		if err != nil {
			return servers.Server{}, err
		}
		all = append(all, items...)
		if next == "" {
			break
		}
		cursor = next
	}
	var hits []servers.Server
	for _, s := range all {
		if strings.EqualFold(s.Name, sel) {
			hits = append(hits, s)
		}
	}
	choice := func(s servers.Server) mcpkit.Choice {
		return mcpkit.Choice{ID: s.ID.String(), Kind: "server", Path: s.Name, Name: s.Name}
	}
	switch len(hits) {
	case 1:
		return hits[0], nil
	case 0:
		var near []mcpkit.Choice
		for _, s := range all {
			if strings.Contains(strings.ToLower(s.Name), strings.ToLower(sel)) || strings.Contains(strings.ToLower(sel), strings.ToLower(s.Name)) {
				near = append(near, choice(s))
			}
		}
		if len(near) == 0 {
			for _, s := range all {
				near = append(near, choice(s))
			}
		}
		if len(near) > 8 {
			near = near[:8]
		}
		e := mcpkit.Errf(mcpkit.CodeNotFound, "no server is named %q; use an id or one of the names in choices", sel)
		if len(near) > 0 {
			e.Data = map[string]any{"choices": near}
		}
		return servers.Server{}, e
	}
	cs := make([]mcpkit.Choice, 0, len(hits))
	for _, s := range hits {
		cs = append(cs, choice(s))
	}
	e := mcpkit.Errf(mcpkit.CodeAmbiguous, "several servers are named %q; use an id from choices", sel)
	e.Data = map[string]any{"choices": cs}
	return servers.Server{}, e
}

func (c *composites) runDiagnoseServer(ctx context.Context, p *auth.Principal, srv servers.Server, detail mcpkit.Detail) *diagnoseServerOutput {
	now := c.Now()
	var (
		metrics   *diagServerMetrics
		placement = diagPlacement{ByState: map[string]int{}, Items: []diagPlaced{}}
		commands  = []diagCommand{}
	)

	pr := c.newPartial(ctx, "diagnose_server", 0)
	pr.run("metrics", func(ctx context.Context) error {
		res, err := c.Servers.Metrics(ctx, srv.ID, "1h")
		if err != nil {
			return err
		}
		metrics = diagServerTrend(res)
		return nil
	})
	if holds(p, policy.ScopeProjectsRead) {
		pr.run("services", func(ctx context.Context) error {
			pl, err := c.placedServices(ctx, srv.ID, detail)
			if err != nil {
				return err
			}
			placement = pl
			return nil
		})
	} else {
		pr.forbidden("services", "services placed on the server need the projects:read scope")
	}
	pr.run("commands", func(ctx context.Context) error {
		cmds, err := c.stuckCommands(ctx, srv.ID, now)
		if err != nil {
			return err
		}
		commands = cmds
		return nil
	})
	errs := pr.wait()

	info := c.diagServerInfo(srv, now)
	findings := diagnoseServerFindings(srv, info, metrics, placement, commands, now)
	sortFindings(findings)
	verdict := diagServerVerdict(srv)
	if verdict == diagHealthy {
		if w := worst(findings); w == SevCritical || w == SevWarning {
			verdict = diagDegraded
		}
	}

	out := &diagnoseServerOutput{
		Verdict: verdict, Findings: findings, Server: info, Metrics: metrics, Services: placement, Commands: commands,
		Drain: diagDrain{State: srv.DrainState, Message: untrustedText("server.drainMessage", clip(srv.DrainMessage, 300)), StartedAt: ptrTime(srv.DrainStartedAt), AcceptWorkloads: srv.AcceptWorkloads},
	}
	if srv.Latest != nil {
		out.Usage = diagUsage(srv, now)
	}
	out.Errors = errs
	out.NextTools = diagServerNext(srv, placement)
	out.Summary = diagServerSummary(srv, verdict, findings, placement, errs)
	return out
}

func (c *composites) diagServerInfo(srv servers.Server, now time.Time) diagServerInfo {
	info := diagServerInfo{
		ID: srv.ID.String(), Name: srv.Name, Status: srv.Status, ControlProtocol: agentprotocol.ProtocolVersion,
		LastSeenAt: ptrTime(srv.LastSeenAt), ProxyStatus: srv.Proxy.Status, ProxyError: untrustedText("proxy.error", clip(srv.Proxy.Error, 300)), ProxyV2: srv.Proxy.V2,
		ProxyGeneration: srv.Proxy.Generation, ProxyApplied: srv.Proxy.AppliedGeneration, AcceptWorkloads: srv.AcceptWorkloads,
		BuildEnabled: srv.BuildEnabled, ControlPlaneHost: srv.ControlPlane, ComposeVersion: srv.ComposeVersion,
	}
	if srv.PublicIP != nil {
		info.PublicIP = *srv.PublicIP
	}
	if srv.BaseDomain != nil {
		info.BaseDomain = *srv.BaseDomain
	}
	if srv.AgentVersion != nil {
		info.AgentVersion = *srv.AgentVersion
	}
	if srv.ProtocolVersion != nil {
		info.AgentProtocol = int(*srv.ProtocolVersion)
	}
	if srv.LastSeenAt != nil {
		info.LastSeen = ago(now, *srv.LastSeenAt)
		if srv.Status != "online" {
			info.OfflineSeconds = int64(now.Sub(*srv.LastSeenAt).Seconds())
		}
	}
	if f := srv.Facts; f != nil {
		info.DockerVersion, info.OS, info.Arch, info.Kernel = f.DockerVersion, f.OS, f.Arch, f.Kernel
		info.CPUs, info.MemTotalBytes, info.DiskTotalBytes = f.CPUs, f.MemTotal, f.DiskTotal
		info.Port80Free, info.Port443Free = &f.Port80Free, &f.Port443Free
	}
	if srv.Tunnel.Enabled {
		info.TunnelState = srv.Tunnel.State
	}
	return info
}

func diagUsage(srv servers.Server, now time.Time) *diagServerUsage {
	l := srv.Latest
	u := &diagServerUsage{
		SampledAt: utc(l.SampledAt), Age: ago(now, l.SampledAt), CPUPercent: diagRound1(l.CPUPercent),
		MemUsedBytes: l.MemUsed, MemTotalBytes: l.MemTotal, DiskUsedBytes: l.DiskUsed, DiskTotalBytes: l.DiskTotal,
		DiskAlert: srv.DiskAlert, DiskThreshold: srv.DiskAlertThresholdPercent,
	}
	if l.MemTotal > 0 {
		u.MemUsedPercent = diagRound1(float64(l.MemUsed) / float64(l.MemTotal) * 100)
	}
	if l.DiskTotal > 0 {
		u.DiskUsedPercent = diagRound1(float64(l.DiskUsed) / float64(l.DiskTotal) * 100)
	}
	return u
}

func diagServerTrend(res servers.MetricsResult) *diagServerMetrics {
	pts := res.Points
	if len(pts) == 0 {
		return nil
	}
	m := &diagServerMetrics{Window: "1h", Samples: len(pts)}
	first, last := pts[0], pts[len(pts)-1]
	var cpu float64
	for _, pt := range pts {
		cpu += pt.CPUPercent
		if pt.CPUMax > m.CPUMaxPercent {
			m.CPUMaxPercent = pt.CPUMax
		}
		if pt.MemPercent > m.MemMaxPercent {
			m.MemMaxPercent = pt.MemPercent
		}
	}
	m.CPUAvgPercent = diagRound1(cpu / float64(len(pts)))
	m.CPUMaxPercent = diagRound1(m.CPUMaxPercent)
	m.MemMaxPercent = diagRound1(m.MemMaxPercent)
	m.MemStartPercent, m.MemNowPercent = diagRound1(first.MemPercent), diagRound1(last.MemPercent)
	m.DiskStartPercent, m.DiskNowPercent = diagRound1(first.DiskPercent), diagRound1(last.DiskPercent)
	if hours := last.TS.Sub(first.TS).Hours(); hours >= 0.1 {
		m.DiskPointsPerHour = math.Round((last.DiskPercent-first.DiskPercent)/hours*100) / 100
	}
	return m
}

func diagServerVerdict(srv servers.Server) string {
	switch srv.Status {
	case "offline", "revoked":
		return diagDown
	case "pending":
		return diagUnknown
	}
	if srv.Proxy.Status == agentprotocol.ProxyError || srv.DiskAlert {
		return diagDegraded
	}
	return diagHealthy
}

// placedServices lists the live services whose current deployment runs on the
// server, problems first. The server id was validated by the caller's read of
// the server; every service still goes through the policy-checked scope load.
func (c *composites) placedServices(ctx context.Context, serverID uuid.UUID, detail mcpkit.Detail) (diagPlacement, error) {
	pl := diagPlacement{ByState: map[string]int{}, Items: []diagPlaced{}}
	rows, err := c.q().ListServerCurrentServices(ctx, &serverID)
	if err != nil {
		return pl, fmt.Errorf("list placed services: %w", err)
	}
	limit := 20
	if detail.Full() {
		limit = 100
	}
	// Bound the policy-checked loads; beyond the bound services are only counted.
	const maxLoads = 200
	all := make([]diagPlaced, 0, len(rows))
	for i, row := range rows {
		if i >= maxLoads {
			pl.NotListed += len(rows) - maxLoads
			break
		}
		sc, err := c.scope(ctx, row.Service.ID)
		if err != nil {
			pl.NotVisible++
			continue
		}
		all = append(all, diagPlacedOf(sc))
	}
	pl.Total = len(all) + pl.NotVisible + pl.NotListed
	for _, it := range all {
		pl.ByState[it.State]++
	}
	sort.SliceStable(all, func(i, j int) bool {
		ri, rj := diagPlacedRank(all[i]), diagPlacedRank(all[j])
		if ri != rj {
			return ri < rj
		}
		return all[i].Selector < all[j].Selector
	})
	if len(all) > limit {
		pl.NotListed += len(all) - limit
		all = all[:limit]
	}
	pl.Items = all
	return pl, nil
}

func diagPlacedOf(sc services.Scope) diagPlaced {
	it := diagPlaced{ID: sc.Service.ID.String(), Selector: selectorOf(sc), Kind: sc.Service.Kind, Production: sc.IsProduction, State: "unknown"}
	obs := services.DecodeObserved(sc.Service.Observed)
	if obs != nil {
		it.State, it.Health = obs.State, obs.Health
		if obs.State == "" {
			it.State = "unknown"
		}
	}
	if sc.Service.ObservedAt == nil || time.Since(*sc.Service.ObservedAt) > agentprotocol.OfflineAfter {
		it.Stale = obs != nil
	}
	if sc.Service.DesiredState == services.DesiredStopped {
		it.State = "stopped"
	}
	if sc.Service.Kind == services.KindRoute {
		it.State, it.Health, it.Stale = "route", "", false
	}
	return it
}

// diagPlacedRank orders problem services first.
func diagPlacedRank(it diagPlaced) int {
	switch {
	case it.State == "stopped" || it.State == "route":
		return 3
	case it.State == agentprotocol.StateRunning && (it.Health == agentprotocol.HealthHealthy || it.Health == "" || it.Health == agentprotocol.HealthUnknown) && !it.Stale:
		return 2
	case it.State == "unknown":
		return 1
	}
	return 0
}

func diagPlacedProblem(it diagPlaced) bool { return diagPlacedRank(it) <= 1 && it.State != "route" }

// stuckCommands lists the server's commands that are not finished: pending,
// sent, or acked without a result (the agent may be gone mid-command).
func (c *composites) stuckCommands(ctx context.Context, serverID uuid.UUID, now time.Time) ([]diagCommand, error) {
	rows, err := c.q().ListDeliverableAgentCommands(ctx, db.ListDeliverableAgentCommandsParams{ServerID: serverID, IncludeSent: true})
	if err != nil {
		return nil, fmt.Errorf("list agent commands: %w", err)
	}
	out := make([]diagCommand, 0, len(rows))
	for i, r := range rows {
		if i >= diagServerCommandLimit {
			break
		}
		cmd := diagCommand{ID: r.ID.String(), Kind: r.Kind, Status: r.Status, Age: ago(now, r.CreatedAt), AgeSeconds: int64(now.Sub(r.CreatedAt).Seconds())}
		if r.DeploymentID != nil && i < diagServerCommandLookup {
			// The deployment is shown only when the caller may read it.
			if d, err := c.Deployments.Get(ctx, *r.DeploymentID); err == nil {
				cmd.DeploymentID = d.ID.String()
				if sc, err := c.scope(ctx, d.ServiceID); err == nil {
					cmd.Service = selectorOf(sc)
				}
			}
		}
		out = append(out, cmd)
	}
	return out, nil
}

func diagnoseServerFindings(srv servers.Server, info diagServerInfo, m *diagServerMetrics, pl diagPlacement, cmds []diagCommand, now time.Time) []Finding {
	var fs []Finding
	add := func(f Finding) {
		f.Subject = srv.Name
		fs = append(fs, f)
	}
	sid := map[string]any{"serverId": srv.ID.String()}

	switch srv.Status {
	case "offline":
		ev := diagNonEmpty("status: offline", diagIfSet(info.LastSeen != "", "last heard from "+info.LastSeen))
		if pl.Total > 0 {
			ev = append(ev, fmt.Sprintf("%s placed here cannot be observed or changed", plural(pl.Total, "service", "services")))
		}
		add(Finding{Severity: SevCritical, Code: "server_offline", Title: "The agent is not connected", Evidence: ev,
			Suggest: &NextTool{Tool: "server_get", Args: sid, Why: "raw record, then check the host and the agent service"}})
	case "revoked":
		add(Finding{Severity: SevCritical, Code: "server_revoked", Title: "The server was revoked; its agent is rejected", Evidence: []string{"status: revoked"}})
	case "pending":
		add(Finding{Severity: SevInfo, Code: "never_connected", Title: "The agent never completed enrollment", Evidence: []string{"status: pending"},
			Suggest: &NextTool{Tool: "server_get", Args: sid, Why: "see the enrollment state"}})
	}

	switch srv.Proxy.Status {
	case agentprotocol.ProxyError:
		ev := []string{"proxy status: error"}
		if f := srv.Facts; f != nil && (!f.Port80Free || !f.Port443Free) {
			ev = append(ev, fmt.Sprintf("agent facts: port 80 free=%t, port 443 free=%t; if the proxy cannot bind, another process may hold the port", f.Port80Free, f.Port443Free))
		}
		add(Finding{Severity: SevWarning, Code: "proxy_error", Title: "The proxy reports an error: domains on this server may not be served", Evidence: ev,
			Messages: untrustedLines("proxy.error", clip(srv.Proxy.Error, 300)),
			Suggest:  &NextTool{Tool: "server_get", Args: sid, Why: "proxy details"}})
	case agentprotocol.ProxyStarting:
		if srv.Status == "online" {
			add(Finding{Severity: SevInfo, Code: "proxy_starting", Title: "The proxy is still starting", Evidence: []string{"proxy status: starting"}})
		}
	}
	if srv.Proxy.Generation > srv.Proxy.AppliedGeneration {
		add(Finding{Severity: SevInfo, Code: "proxy_changes_staged", Title: "Server-level proxy changes are staged and not applied",
			Evidence: []string{fmt.Sprintf("proxy generation %d is ahead of applied %d", srv.Proxy.Generation, srv.Proxy.AppliedGeneration)}})
	}

	if srv.Status == "online" && srv.ProtocolVersion != nil && int(*srv.ProtocolVersion) != agentprotocol.ProtocolVersion {
		add(Finding{Severity: SevWarning, Code: "protocol_mismatch", Title: "The agent speaks another protocol version than the control plane",
			Evidence: []string{fmt.Sprintf("agent protocol %d, control plane %d", *srv.ProtocolVersion, agentprotocol.ProtocolVersion)}})
	}

	if l := srv.Latest; l != nil {
		if l.DiskTotal > 0 {
			pct := float64(l.DiskUsed) / float64(l.DiskTotal) * 100
			threshold := float64(max(srv.DiskAlertThresholdPercent, 50))
			if srv.DiskAlert || pct >= threshold || pct >= 90 {
				sev := SevWarning
				if pct >= 95 {
					sev = SevCritical
				}
				add(Finding{Severity: sev, Code: "disk_full", Title: fmt.Sprintf("Root disk is %.0f%% full", pct),
					Evidence: []string{fmt.Sprintf("%d of %d bytes used; alert threshold %d%%", l.DiskUsed, l.DiskTotal, srv.DiskAlertThresholdPercent)}})
			}
		}
		if l.MemTotal > 0 {
			if pct := float64(l.MemUsed) / float64(l.MemTotal) * 100; pct >= 90 {
				add(Finding{Severity: SevWarning, Code: "memory_pressure", Title: fmt.Sprintf("Memory is %.0f%% used", pct),
					Evidence: []string{fmt.Sprintf("%d of %d bytes used", l.MemUsed, l.MemTotal)}})
			}
		}
		if srv.Status == "online" && now.Sub(l.SampledAt) > 5*time.Minute {
			add(Finding{Severity: SevInfo, Code: "metrics_stale", Title: "The newest resource sample is old although the agent is connected",
				Evidence: []string{"sampled " + ago(now, l.SampledAt)}})
		}
	}
	if m != nil && m.DiskPointsPerHour > 0.05 && m.DiskNowPercent < 100 {
		if hours := (100 - m.DiskNowPercent) / m.DiskPointsPerHour; hours < 24 {
			add(Finding{Severity: SevWarning, Code: "disk_filling", Title: fmt.Sprintf("Disk grows %.2f points per hour and fills in about %.0f hours", m.DiskPointsPerHour, hours),
				Evidence: []string{fmt.Sprintf("disk %.1f%% → %.1f%% over the last hour", m.DiskStartPercent, m.DiskNowPercent)}})
		}
	}

	switch srv.DrainState {
	case "draining":
		add(Finding{Severity: SevInfo, Code: "draining", Title: "The server is being drained: services move off it", Evidence: []string{"drain state: draining"}, Messages: untrustedLines("server.drainMessage", clip(srv.DrainMessage, 300))})
	case "drained":
		add(Finding{Severity: SevInfo, Code: "drained", Title: "The server is drained and accepts no new services", Evidence: []string{"drain state: drained"}})
	case "failed":
		add(Finding{Severity: SevWarning, Code: "drain_failed", Title: "The last drain failed", Evidence: []string{"drain state: failed"}, Messages: untrustedLines("server.drainMessage", clip(srv.DrainMessage, 300))})
	}
	if !srv.AcceptWorkloads && srv.DrainState == "none" {
		add(Finding{Severity: SevInfo, Code: "not_accepting_workloads", Title: "The server accepts no new services (existing ones keep running)", Evidence: []string{"acceptWorkloads: false"}})
	}

	// Services placed here that are not running well.
	var bad []string
	var first string
	for _, it := range pl.Items {
		if diagPlacedProblem(it) {
			if first == "" {
				first = it.ID
			}
			bad = append(bad, fmt.Sprintf("%s (%s)", it.Selector, it.State))
		}
	}
	if len(bad) > 0 && srv.Status == "online" {
		ev := bad
		if len(ev) > 5 {
			ev = append(ev[:5:5], fmt.Sprintf("… and %d more", len(bad)-5))
		}
		add(Finding{Severity: SevWarning, Code: "services_unhealthy", Title: plural(len(bad), "service on this server is", "services on this server are") + " not running well", Evidence: ev,
			Suggest: &NextTool{Tool: "diagnose_service", Args: map[string]any{"service": first}, Why: "reason for the first problem service"}})
	}

	// Agent commands that never finished.
	var acked, queued []diagCommand
	for _, cmd := range cmds {
		switch {
		case cmd.Status == "acked" && cmd.AgeSeconds > 120:
			acked = append(acked, cmd)
		case cmd.Status == "pending" && cmd.AgeSeconds > 60:
			queued = append(queued, cmd)
		}
	}
	if len(acked) > 0 {
		ev := make([]string, 0, len(acked))
		for _, cmd := range acked {
			ev = append(ev, fmt.Sprintf("%s acked %s ago, no result%s", cmd.Kind, cmd.Age, diagCmdWhere(cmd)))
		}
		title := "A command was acknowledged but never reported a result"
		if srv.Status != "online" {
			title += ": its outcome is uncertain while the agent is offline"
		}
		add(Finding{Severity: SevCritical, Code: "acked_without_result", Title: title, Evidence: ev})
	}
	if len(queued) > 0 {
		ev := make([]string, 0, len(queued))
		for _, cmd := range queued {
			ev = append(ev, fmt.Sprintf("%s queued %s%s", cmd.Kind, cmd.Age, diagCmdWhere(cmd)))
		}
		title := "Commands are waiting for the agent"
		if srv.Status == "online" {
			title = "Commands stay queued although the agent is connected"
		}
		add(Finding{Severity: SevWarning, Code: "commands_queued", Title: title, Evidence: ev})
	}
	return fs
}

func diagCmdWhere(c diagCommand) string {
	if c.Service != "" {
		return " (service " + c.Service + ")"
	}
	return ""
}

func diagIfSet(ok bool, s string) string {
	if ok {
		return s
	}
	return ""
}

func diagServerNext(srv servers.Server, pl diagPlacement) []NextTool {
	next := []NextTool{}
	for _, it := range pl.Items {
		if diagPlacedProblem(it) {
			next = append(next, NextTool{Tool: "diagnose_service", Args: map[string]any{"service": it.Selector}, Why: "service on this server in state " + it.State})
			break
		}
	}
	next = append(next, NextTool{Tool: "server_get", Args: map[string]any{"serverId": srv.ID.String()}, Why: "the full server record"})
	next = append(next, NextTool{Tool: "find_issues", Args: map[string]any{}, Why: "scan the whole estate for related problems"})
	return next
}

func diagServerSummary(srv servers.Server, verdict string, fs []Finding, pl diagPlacement, errs []SectionError) string {
	s := fmt.Sprintf("Server %s is %s (%s)", srv.Name, verdict, srv.Status)
	if len(fs) > 0 {
		s += ": " + plural(len(fs), "finding", "findings") + ". Top: " + fs[0].Title + "."
	} else {
		s += ": no findings."
	}
	if pl.Total > 0 {
		s += " " + plural(pl.Total, "service", "services") + " placed."
	}
	if len(errs) > 0 {
		names := make([]string, len(errs))
		for i, e := range errs {
			names[i] = e.Section
		}
		sort.Strings(names)
		s += " Missing sections: " + strings.Join(names, ", ") + " (see errors)."
	}
	return s
}
