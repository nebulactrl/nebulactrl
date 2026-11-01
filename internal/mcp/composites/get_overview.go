package composites

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/deployments"
	"github.com/nebulactrl/nebulactrl/internal/mcp/mcpkit"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/servers"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

const (
	overviewDefaultEnvs = 20
	overviewMaxEnvs     = 100
	overviewMaxAttn     = 10
	// overviewRowsPerEnv and overviewRowsTotal cap the per-service rows of
	// detail:full so the result stays well under 64 KB.
	overviewRowsPerEnv = 40
	overviewRowsTotal  = 200
	overviewMaxDomains = 10
	overviewMaxServers = 10
)

type getOverviewInput struct {
	Project     string `json:"project,omitempty" jsonschema:"Limit to one project (id, slug or name). Omit both project and environment for the whole estate."`
	Environment string `json:"environment,omitempty" jsonschema:"Limit to one environment: id, project/env, or an environment slug together with project."`
	Detail      string `json:"detail,omitempty" jsonschema:"concise (default): counts and attention items; full: also one row per service (capped)."`
	Limit       int    `json:"limit,omitempty" jsonschema:"Maximum number of environments listed (default 20, max 100); totals always cover everything read."`
}

type overviewCounts struct {
	Total         int `json:"total"`
	Running       int `json:"running"`
	Stopped       int `json:"stopped"`
	Unhealthy     int `json:"unhealthy"`
	Unknown       int `json:"unknown"`
	NeverDeployed int `json:"neverDeployed"`
}

type overviewService struct {
	Path             string   `json:"path" jsonschema:"project/env/service selector"`
	ID               string   `json:"id"`
	Kind             string   `json:"kind"`
	State            string   `json:"state" jsonschema:"running, stopped, unhealthy, unknown or never_deployed"`
	Health           string   `json:"health,omitempty"`
	Staged           bool     `json:"staged,omitempty"`
	LatestDeployment string   `json:"latestDeployment,omitempty" jsonschema:"status of the newest deployment"`
	Server           string   `json:"server,omitempty"`
	Domains          []string `json:"domains"`
	// ProcessesNotReady names each process of a service with several
	// processes that lacks ready replicas.
	ProcessesNotReady []string `json:"processesNotReady,omitempty"`
}

type overviewEnvironment struct {
	Path                string            `json:"path" jsonschema:"project/env selector"`
	ID                  string            `json:"id"`
	Project             string            `json:"project"`
	Environment         string            `json:"environment"`
	Production          bool              `json:"production"`
	Preview             bool              `json:"preview,omitempty"`
	Services            overviewCounts    `json:"services"`
	StagedServices      int               `json:"stagedServices" jsonschema:"services with staged changes (generation > appliedGeneration)"`
	InFlight            int               `json:"inFlightDeployments"`
	FailedLast24h       int               `json:"failedDeployments24h" jsonschema:"deployments that failed, rolled back or failed to roll back in the last 24 hours"`
	DomainsNotOK        int               `json:"domainsNotOk" jsonschema:"domains whose DNS check failed or that are in an error state"`
	StaleServices       int               `json:"staleServices" jsonschema:"services whose runtime state is out of date (host offline or silent)"`
	ServicesTruncated   bool              `json:"servicesTruncated,omitempty"`
	ServiceRows         []overviewService `json:"serviceRows,omitempty"`
	ServiceRowsOmitted  int               `json:"serviceRowsOmitted,omitempty"`
	attention, severity int
}

type overviewServerRow struct {
	ID     string   `json:"id"`
	Name   string   `json:"name"`
	Status string   `json:"status"`
	Issues []string `json:"issues"`
	// ProxyError is the error the proxy reported, as an untrusted block.
	ProxyError any `json:"proxyError,omitempty" jsonschema:"error the proxy reported, as an untrusted block"`
}

type overviewServers struct {
	Available      bool                `json:"available" jsonschema:"false when the token lacks servers:read"`
	Total          int                 `json:"total"`
	Online         int                 `json:"online"`
	Offline        int                 `json:"offline"`
	Pending        int                 `json:"pending"`
	StaleHeartbeat int                 `json:"staleHeartbeat"`
	ProxyErrors    int                 `json:"proxyErrors"`
	DiskAlerts     int                 `json:"diskAlerts"`
	Problems       []overviewServerRow `json:"problems" jsonschema:"servers with an issue (capped)"`
}

type overviewDomain struct {
	Hostname string `json:"hostname"`
	Service  string `json:"service" jsonschema:"project/env/service selector"`
	Status   string `json:"status"`
	DNSOK    string `json:"dns" jsonschema:"ok, failed or unknown"`
	Message  any    `json:"message,omitempty" jsonschema:"status message of the domain, as an untrusted block"`
}

type overviewTotals struct {
	Projects      int            `json:"projects"`
	Environments  int            `json:"environments"`
	Services      overviewCounts `json:"services"`
	Staged        int            `json:"stagedServices"`
	InFlight      int            `json:"inFlightDeployments"`
	FailedLast24h int            `json:"failedDeployments24h"`
	DomainsNotOK  int            `json:"domainsNotOk"`
}

type getOverviewOutput struct {
	Envelope
	Totals       overviewTotals        `json:"totals"`
	Environments []overviewEnvironment `json:"environments"`
	// EnvironmentsOmitted counts environments beyond `limit` (or beyond the
	// walk cap) that are included in totals but not listed.
	EnvironmentsOmitted int              `json:"environmentsOmitted"`
	Servers             overviewServers  `json:"servers"`
	DomainsNotOK        []overviewDomain `json:"domainsNotOk"`
	AttentionTotal      int              `json:"attentionTotal" jsonschema:"warnings and criticals found; attention lists the worst"`
	Attention           []Finding        `json:"attention" jsonschema:"worst problems first (max 10); find_issues has the full ranked list"`
}

func (c *composites) getOverview() mcpkit.Tool {
	return mcpkit.Tool{
		Name:  "get_overview",
		Title: "Estate overview",
		Description: "Start here. One call returns the state of the world: per environment the services by state (running, stopped, unhealthy, unknown, never deployed), " +
			"how many services have staged (unapplied) changes, in-flight deployments, deployments that failed in the last 24 hours, domains whose DNS is not ok, " +
			"a servers summary (online/offline/pending, late heartbeat, proxy errors, disk alerts; only with the servers:read scope) and the top 10 things needing attention. " +
			"Omit project and environment for the whole estate, or narrow with `project` / `environment` (id, slug, project/env). " +
			"detail:full adds one row per service (capped). Returns counts and findings, never logs or variable values. " +
			"It does not diagnose: follow nextTools to diagnose_service, find_issues (full ranked list), review_changes or get_recent_changes.",
		Toolset: mcpkit.ToolsetCore,
		Scopes:  []string{policy.ScopeProjectsRead},
		Hints:   mcpkit.Hints{ReadOnly: true, Destructive: false, Idempotent: true, OpenWorld: false},
		Input:   getOverviewInput{},
		Output:  getOverviewOutput{},
		Call: func(ctx context.Context, p *auth.Principal, raw json.RawMessage) (any, error) {
			in, err := mcpkit.Decode[getOverviewInput](raw)
			if err != nil {
				return nil, err
			}
			detail, err := mcpkit.ParseDetail(in.Detail)
			if err != nil {
				return nil, err
			}
			if in.Limit < 0 {
				return nil, invalid("limit must be positive")
			}
			limit := in.Limit
			if limit == 0 {
				limit = overviewDefaultEnvs
			}
			limit = min(limit, overviewMaxEnvs)
			return c.overview(ctx, p, in, detail, limit)
		},
	}
}

func (c *composites) overview(ctx context.Context, p *auth.Principal, in getOverviewInput, detail mcpkit.Detail, limit int) (*getOverviewOutput, error) {
	now := c.Now()
	run := c.newPartial(ctx, "get_overview", 8)

	var (
		srvs    []servers.Server
		haveSrv bool
	)
	if holds(p, policy.ScopeServersRead) {
		run.run("servers", func(ctx context.Context) error {
			l, err := c.listAllServers(ctx)
			if err != nil {
				return err
			}
			srvs, haveSrv = l, true
			return nil
		})
	} else {
		run.forbidden("servers", "the token lacks servers:read; the servers summary is omitted")
	}

	est, err := c.walkEstate(ctx, run, estateSelector{Project: in.Project, Environment: in.Environment})
	if err != nil {
		run.wait()
		return nil, err
	}

	// Recent failures per environment (the latest deployment of each service
	// only shows the newest outcome).
	var mu sync.Mutex
	failed := map[string]int{}
	for _, n := range est.Envs {
		run.run("deployments "+n.path(), func(ctx context.Context) error {
			items, _, err := c.Deployments.ListEnvironment(ctx, n.Env.ID,
				string(deployments.Failed)+","+string(deployments.RolledBack)+","+string(deployments.RollbackFailed), nil, "", 50)
			if err != nil {
				return err
			}
			cut := 0
			for _, d := range items {
				t := d.CreatedAt
				if d.FinishedAt != nil {
					t = *d.FinishedAt
				}
				if now.Sub(t) <= 24*time.Hour {
					cut++
				}
			}
			mu.Lock()
			failed[n.Env.ID.String()] = cut
			mu.Unlock()
			return nil
		})
	}
	errs := run.wait()

	out := &getOverviewOutput{
		Envelope:       Envelope{Errors: errs, NextTools: []NextTool{}},
		Environments:   []overviewEnvironment{},
		DomainsNotOK:   []overviewDomain{},
		Attention:      []Finding{},
		Servers:        overviewServers{Problems: []overviewServerRow{}},
		AttentionTotal: 0,
	}
	out.Totals.Projects = len(est.Projects)
	out.Totals.Environments = len(est.Envs)

	findings := estateFindings(est, findOpts{Now: now, Servers: srvs, HaveServers: haveSrv})
	var attn []Finding
	critical := 0
	for _, f := range findings {
		if severityRank[f.Severity] <= severityRank[SevWarning] {
			attn = append(attn, f)
			if f.Severity == SevCritical {
				critical++
			}
		}
	}
	out.AttentionTotal = len(attn)
	out.Attention = attn[:min(len(attn), overviewMaxAttn)]
	if out.Attention == nil {
		out.Attention = []Finding{}
	}
	attnByEnv := map[string]int{}
	for _, f := range attn {
		for _, n := range est.Envs {
			if len(f.Subject) > len(n.path()) && f.Subject[:len(n.path())+1] == n.path()+"/" {
				attnByEnv[n.Env.ID.String()]++
			}
		}
	}

	rows := 0
	for _, n := range est.Envs {
		eo := overviewEnvironment{
			Path: n.path(), ID: idStr(n.Env.ID), Project: n.Project.Slug, Environment: n.Env.Slug,
			Production: n.Env.IsProduction, Preview: n.Env.Kind == "preview", ServicesTruncated: n.ServicesTruncated,
			FailedLast24h: failed[n.Env.ID.String()],
		}
		eo.attention = attnByEnv[n.Env.ID.String()]
		for _, v := range n.Services {
			st := serviceState(v)
			count(&eo.Services, st)
			if v.HasStagedChanges {
				eo.StagedServices++
			}
			if v.LatestDeployment != nil && deploymentActive(v.LatestDeployment.Status) {
				eo.InFlight++
			}
			if v.Stale {
				eo.StaleServices++
			}
			doms := make([]string, 0, len(v.Domains))
			for _, d := range v.Domains {
				doms = append(doms, d.Hostname)
				if d.Status == "error" || (d.DNSOk != nil && !*d.DNSOk) {
					eo.DomainsNotOK++
					if len(out.DomainsNotOK) < overviewMaxDomains {
						dns := "unknown"
						if d.DNSOk != nil {
							dns = "ok"
							if !*d.DNSOk {
								dns = "failed"
							}
						}
						out.DomainsNotOK = append(out.DomainsNotOK, overviewDomain{
							Hostname: d.Hostname, Service: n.svcPath(v), Status: d.Status, DNSOK: dns, Message: untrustedText("domain.message", clip(d.StatusMessage, 160)),
						})
					}
				}
			}
			if detail.Full() {
				if len(eo.ServiceRows) >= overviewRowsPerEnv || rows >= overviewRowsTotal {
					eo.ServiceRowsOmitted++
					continue
				}
				row := overviewService{
					Path: n.svcPath(v), ID: idStr(v.ID), Kind: v.Kind, State: st, Staged: v.HasStagedChanges, Domains: clipStrings(doms, 5),
				}
				if v.Observed != nil {
					row.Health = v.Observed.Health
					for _, p := range processShortfalls(v.Observed) {
						row.ProcessesNotReady = append(row.ProcessesNotReady, processReadiness(p))
					}
				}
				if v.LatestDeployment != nil {
					row.LatestDeployment = v.LatestDeployment.Status
				}
				if v.Server != nil {
					row.Server = v.Server.Name
				}
				eo.ServiceRows = append(eo.ServiceRows, row)
				rows++
			}
		}
		addCounts(&out.Totals.Services, eo.Services)
		out.Totals.Staged += eo.StagedServices
		out.Totals.InFlight += eo.InFlight
		out.Totals.FailedLast24h += eo.FailedLast24h
		out.Totals.DomainsNotOK += eo.DomainsNotOK
		eo.severity = envSeverity(eo)
		out.Environments = append(out.Environments, eo)
	}
	sort.SliceStable(out.Environments, func(i, j int) bool {
		a, b := out.Environments[i], out.Environments[j]
		if (a.severity > 0) != (b.severity > 0) {
			return a.severity > 0
		}
		if a.Production != b.Production {
			return a.Production
		}
		if a.attention != b.attention {
			return a.attention > b.attention
		}
		return a.Path < b.Path
	})
	if len(out.Environments) > limit {
		out.EnvironmentsOmitted = len(out.Environments) - limit
		out.Environments = out.Environments[:limit]
	}

	out.Servers = summarizeServers(srvs, haveSrv, now)

	out.Summary = overviewSummary(out, critical, est)
	out.NextTools = overviewNext(out, in, attn)
	return out, nil
}

// envSeverity scores an environment for ordering: anything wrong first.
func envSeverity(e overviewEnvironment) int {
	return e.Services.Unhealthy*4 + e.FailedLast24h*3 + e.DomainsNotOK*2 + e.InFlight + e.StagedServices
}

func count(c *overviewCounts, state string) {
	c.Total++
	switch state {
	case stateRunning:
		c.Running++
	case stateStopped:
		c.Stopped++
	case stateUnhealthy:
		c.Unhealthy++
	case stateNever:
		c.NeverDeployed++
	default:
		c.Unknown++
	}
}

func addCounts(dst *overviewCounts, src overviewCounts) {
	dst.Total += src.Total
	dst.Running += src.Running
	dst.Stopped += src.Stopped
	dst.Unhealthy += src.Unhealthy
	dst.Unknown += src.Unknown
	dst.NeverDeployed += src.NeverDeployed
}

func clipStrings(in []string, n int) []string {
	if len(in) > n {
		in = in[:n]
	}
	if in == nil {
		return []string{}
	}
	return in
}

func summarizeServers(srvs []servers.Server, have bool, now time.Time) overviewServers {
	out := overviewServers{Available: have, Problems: []overviewServerRow{}}
	if !have {
		return out
	}
	out.Total = len(srvs)
	for _, s := range srvs {
		var issues []string
		var proxyErr any
		switch s.Status {
		case "online":
			out.Online++
			if s.LastSeenAt != nil && now.Sub(*s.LastSeenAt) > staleHeartbeat {
				out.StaleHeartbeat++
				issues = append(issues, "heartbeat late ("+ago(now, *s.LastSeenAt)+")")
			}
		case "offline":
			out.Offline++
			issues = append(issues, "offline")
		case "pending":
			out.Pending++
			issues = append(issues, "agent has not connected yet")
		}
		if s.Status != "revoked" && s.Proxy.Status == agentprotocol.ProxyError {
			out.ProxyErrors++
			issues = append(issues, "proxy error")
			proxyErr = untrustedText("proxy.error", clip(s.Proxy.Error, 120))
		}
		if s.DiskAlert {
			out.DiskAlerts++
			issues = append(issues, "disk alert")
		}
		if len(issues) > 0 && len(out.Problems) < overviewMaxServers {
			out.Problems = append(out.Problems, overviewServerRow{ID: idStr(s.ID), Name: s.Name, Status: s.Status, Issues: issues, ProxyError: proxyErr})
		}
	}
	return out
}

func overviewSummary(o *getOverviewOutput, critical int, est *estate) string {
	t := o.Totals
	s := fmt.Sprintf("%s, %s, %s: %d running, %d unhealthy, %d stopped, %d unknown, %d never deployed.",
		plural(t.Projects, "project", "projects"), plural(t.Environments, "environment", "environments"),
		plural(t.Services.Total, "service", "services"),
		t.Services.Running, t.Services.Unhealthy, t.Services.Stopped, t.Services.Unknown, t.Services.NeverDeployed)
	if t.Staged > 0 {
		s += " " + plural(t.Staged, "service has", "services have") + " staged changes."
	}
	if t.InFlight > 0 {
		s += " " + plural(t.InFlight, "deployment is", "deployments are") + " in flight."
	}
	if t.FailedLast24h > 0 {
		s += " " + plural(t.FailedLast24h, "deployment", "deployments") + " failed in the last 24h."
	}
	if o.Servers.Available {
		s += fmt.Sprintf(" Servers: %d online, %d offline, %d pending.", o.Servers.Online, o.Servers.Offline, o.Servers.Pending)
	}
	switch {
	case o.AttentionTotal == 0:
		s += " Nothing needs attention."
	case critical > 0:
		s += fmt.Sprintf(" %s need attention (%d critical).", plural(o.AttentionTotal, "item", "items"), critical)
	default:
		s += " " + plural(o.AttentionTotal, "item needs", "items need") + " attention."
	}
	if o.EnvironmentsOmitted > 0 {
		s += fmt.Sprintf(" %d more environments are in the totals but not listed; raise `limit` or narrow with `project`.", o.EnvironmentsOmitted)
	}
	if est.Truncated {
		s += " The estate is larger than one call reads; narrow with `project` or `environment`."
	}
	return s
}

func overviewNext(o *getOverviewOutput, in getOverviewInput, attn []Finding) []NextTool {
	var out []NextTool
	seen := map[string]bool{}
	for _, f := range attn {
		if f.Suggest == nil || len(out) >= 3 {
			continue
		}
		key := f.Suggest.Tool + fmt.Sprint(f.Suggest.Args)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, NextTool{Tool: f.Suggest.Tool, Args: f.Suggest.Args, Why: f.Title})
	}
	if o.AttentionTotal > len(o.Attention) {
		args := map[string]any{}
		if in.Project != "" {
			args["project"] = in.Project
		}
		if in.Environment != "" {
			args["environment"] = in.Environment
		}
		out = append(out, NextTool{Tool: "find_issues", Args: args, Why: "Full ranked list of the " + plural(o.AttentionTotal, "problem", "problems") + "."})
	}
	if o.Totals.Staged > 0 && len(o.Environments) > 0 {
		for _, e := range o.Environments {
			if e.StagedServices > 0 {
				out = append(out, NextTool{Tool: "review_changes", Args: map[string]any{"environment": e.Path}, Why: "Review the staged changes before applying them."})
				break
			}
		}
	}
	if len(out) == 0 && len(o.Environments) > 0 {
		out = append(out, NextTool{Tool: "get_overview", Args: map[string]any{"environment": o.Environments[0].Path, "detail": "full"}, Why: "Per-service rows of one environment."})
	}
	if out == nil {
		out = []NextTool{}
	}
	return out
}
