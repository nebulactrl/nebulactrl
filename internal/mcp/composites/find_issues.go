package composites

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/deployments"
	"github.com/nebulactrl/nebulactrl/internal/mcp/mcpkit"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/servers"
)

const (
	findIssuesDefaultLimit = 25
	findIssuesMaxLimit     = 100
	// findIssuesMaxPlans bounds the deploy plans read for staged services.
	findIssuesMaxPlans = 25
)

type findIssuesInput struct {
	Project     string `json:"project,omitempty" jsonschema:"Limit the scan to one project (id, slug or name)."`
	Environment string `json:"environment,omitempty" jsonschema:"Limit the scan to one environment: id, project/env, or a slug together with project."`
	Severity    string `json:"severity,omitempty" jsonschema:"Minimum severity to report: info, warning (default) or critical."`
	Limit       int    `json:"limit,omitempty" jsonschema:"Maximum findings returned (default 25, max 100); counts always cover all findings."`
}

type findIssuesCounts struct {
	Critical int `json:"critical"`
	Warning  int `json:"warning"`
	Info     int `json:"info"`
}

type findIssuesScanned struct {
	Projects     int `json:"projects"`
	Environments int `json:"environments"`
	Services     int `json:"services"`
	Servers      int `json:"servers"`
}

type findIssuesOutput struct {
	Envelope
	Findings []Finding         `json:"findings" jsonschema:"ranked: critical first, then warning, then info"`
	Counts   findIssuesCounts  `json:"counts" jsonschema:"all findings at or above the requested severity"`
	Scanned  findIssuesScanned `json:"scanned"`
	// Omitted is the number of findings beyond `limit`.
	Omitted int `json:"omitted"`
}

func (c *composites) findIssues() mcpkit.Tool {
	return mcpkit.Tool{
		Name:  "find_issues",
		Title: "Find issues",
		Description: "Ranked scan of the whole estate (or one project/environment) for problems, each with evidence and the tool that helps: unhealthy, crash-looping or unexpectedly stopped services; " +
			"a failed latest deployment while an older release serves (stale code); rollback_failed; a deployment waiting on an offline server (outcome uncertain) or stuck; " +
			"staged changes forgotten for more than a day; staged changes that would be refused (unresolved ${{reference}}, no server); domains with DNS mismatch, errors or certificate errors; " +
			"offline or late servers, proxy errors, full disks (server findings need servers:read, offline servers are still found without it); stale observations. " +
			"Finding codes are stable snake_case strings. Use `severity` to filter (default warning). Read-only; it does not read logs or fix anything. " +
			"Next: diagnose_service, explain_failed_deployment, diagnose_server, review_changes (see each finding's `suggest`).",
		Toolset: mcpkit.ToolsetCore,
		Scopes:  []string{policy.ScopeProjectsRead},
		Hints:   mcpkit.Hints{ReadOnly: true, Destructive: false, Idempotent: true, OpenWorld: false},
		Input:   findIssuesInput{},
		Output:  findIssuesOutput{},
		Call: func(ctx context.Context, p *auth.Principal, raw json.RawMessage) (any, error) {
			in, err := mcpkit.Decode[findIssuesInput](raw)
			if err != nil {
				return nil, err
			}
			minSev := SevWarning
			if s := strings.ToLower(strings.TrimSpace(in.Severity)); s != "" {
				if _, ok := severityRank[s]; !ok {
					return nil, invalid("severity must be info, warning or critical")
				}
				minSev = s
			}
			if in.Limit < 0 {
				return nil, invalid("limit must be positive")
			}
			limit := in.Limit
			if limit == 0 {
				limit = findIssuesDefaultLimit
			}
			limit = min(limit, findIssuesMaxLimit)
			return c.findIssuesRun(ctx, p, in, minSev, limit)
		},
	}
}

func (c *composites) findIssuesRun(ctx context.Context, p *auth.Principal, in findIssuesInput, minSev string, limit int) (*findIssuesOutput, error) {
	now := c.Now()
	run := c.newPartial(ctx, "find_issues", 8)

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
		run.forbidden("servers", "the token lacks servers:read; proxy, disk and heartbeat checks are skipped (offline servers are still found through their services)")
	}
	est, err := c.walkEstate(ctx, run, estateSelector{Project: in.Project, Environment: in.Environment})
	if err != nil {
		run.wait()
		return nil, err
	}
	errs := run.wait()

	// Deploy plans of staged services reveal changes that would be refused
	// (unresolved references, no server, invalid config).
	plans := map[uuid.UUID]deployments.Plan{}
	var mu sync.Mutex
	pp := c.newPartial(ctx, "find_issues", 4)
	queued := 0
	for _, n := range est.Envs {
		for _, v := range n.Services {
			if !v.HasStagedChanges || queued >= findIssuesMaxPlans {
				continue
			}
			queued++
			pp.run("plan "+n.svcPath(v), func(ctx context.Context) error {
				plan, err := c.Deployments.Plan(ctx, v.ID)
				if err != nil {
					return err
				}
				mu.Lock()
				plans[v.ID] = plan
				mu.Unlock()
				return nil
			})
		}
	}
	errs = append(errs, pp.wait()...)

	all := estateFindings(est, findOpts{Now: now, Servers: srvs, HaveServers: haveSrv, Plans: plans})
	kept := make([]Finding, 0, len(all))
	var counts findIssuesCounts
	for _, f := range all {
		if severityRank[f.Severity] > severityRank[minSev] {
			continue
		}
		kept = append(kept, f)
		switch f.Severity {
		case SevCritical:
			counts.Critical++
		case SevWarning:
			counts.Warning++
		default:
			counts.Info++
		}
	}
	out := &findIssuesOutput{
		Envelope: Envelope{Errors: errs, NextTools: []NextTool{}},
		Counts:   counts,
		Scanned:  findIssuesScanned{Projects: len(est.Projects), Environments: len(est.Envs), Services: est.serviceCount(), Servers: len(srvs)},
		Findings: kept,
	}
	if len(out.Findings) > limit {
		out.Omitted = len(out.Findings) - limit
		out.Findings = out.Findings[:limit]
	}
	// The evidence of each finding is clipped, but guard the 64 KB cap anyway.
	for mcpkit.JSONSize(out) > mcpkit.MaxResultBytes-1024 && len(out.Findings) > 1 {
		cut := len(out.Findings) * 3 / 4
		out.Omitted += len(out.Findings) - cut
		out.Findings = out.Findings[:cut]
	}

	scope := fmt.Sprintf("%s, %s, %s", plural(out.Scanned.Projects, "project", "projects"),
		plural(out.Scanned.Environments, "environment", "environments"), plural(out.Scanned.Services, "service", "services"))
	if haveSrv {
		scope += ", " + plural(out.Scanned.Servers, "server", "servers")
	}
	switch len(kept) {
	case 0:
		out.Summary = "No problems at severity " + minSev + " or above across " + scope + "."
		if minSev != SevInfo {
			out.NextTools = append(out.NextTools, NextTool{Tool: "find_issues", Args: map[string]any{"severity": "info"}, Why: "Include informational findings such as stopped services."})
		}
	default:
		out.Summary = fmt.Sprintf("%s (%d critical, %d warning, %d info) across %s.", plural(len(kept), "finding", "findings"), counts.Critical, counts.Warning, counts.Info, scope)
		if out.Omitted > 0 {
			out.Summary += fmt.Sprintf(" %d more are not shown; narrow with project/environment or severity.", out.Omitted)
		}
	}
	if est.Truncated {
		out.Summary += " The estate is larger than one scan reads; narrow with `project` or `environment`."
	}
	seen := map[string]bool{}
	for _, f := range out.Findings {
		if f.Suggest == nil || len(out.NextTools) >= 4 {
			continue
		}
		key := f.Suggest.Tool + fmt.Sprint(f.Suggest.Args)
		if seen[key] {
			continue
		}
		seen[key] = true
		out.NextTools = append(out.NextTools, NextTool{Tool: f.Suggest.Tool, Args: f.Suggest.Args, Why: f.Title})
	}
	if out.Findings == nil {
		out.Findings = []Finding{}
	}
	return out, nil
}
