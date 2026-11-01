package composites

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/deployments"
	"github.com/nebulactrl/nebulactrl/internal/mcp/mcpkit"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/servers"
	"github.com/nebulactrl/nebulactrl/internal/services"
)

const (
	searchDefaultLimit = 20
	searchMaxLimit     = 50
	searchMinQuery     = 2
	// searchDeploymentsPerService is how many recent deployments of a service
	// are searched for ids, commit SHAs and image digests.
	searchDeploymentsPerService = 10
	// searchMaxDeploymentServices bounds the services whose deployments are read.
	searchMaxDeploymentServices = 150
)

// Match types, in tie-break order.
var searchTypes = []string{"project", "environment", "service", "domain", "server", "deployment", "release"}

type searchResourcesInput struct {
	Query string   `json:"query" jsonschema:"What to look for (at least 2 characters): a name, slug, hostname, image, server name or IP, or the start of a deployment/release id, commit SHA or image digest. Case-insensitive, fuzzy."`
	Types []string `json:"types,omitempty" jsonschema:"Restrict to these types: project, environment, service, server, domain, deployment, release. Default: all (deployments and releases only when the query looks like an id, SHA or digest, or when asked for)."`
	Limit int      `json:"limit,omitempty" jsonschema:"Maximum matches (default 20, max 50)."`
}

type searchMatch struct {
	Type      string `json:"type" jsonschema:"project, environment, service, server, domain, deployment or release"`
	ID        string `json:"id"`
	Name      string `json:"name"`
	Path      string `json:"path" jsonschema:"selector to pass to other tools: project, project/env, project/env/service, hostname, server name or deployment id"`
	Detail    string `json:"detail"`
	Score     int    `json:"score" jsonschema:"0-100, higher is a closer match"`
	MatchedOn string `json:"matchedOn" jsonschema:"which field matched"`
}

type searchResourcesOutput struct {
	Envelope
	Query   string        `json:"query"`
	Matches []searchMatch `json:"matches"`
	// Total is the number of matches before `limit`.
	Total int `json:"total"`
}

func (c *composites) searchResources() mcpkit.Tool {
	return mcpkit.Tool{
		Name:  "search_resources",
		Title: "Search resources",
		Description: "Find resources by a fuzzy, case-insensitive query when you do not have their id: projects, environments, services (name, slug, image), servers (name, public IP, base domain; needs servers:read), " +
			"domains/hostnames, and deployments/releases by the start of an id, commit SHA or image digest. " +
			"Returns ranked typed matches {type, id, name, path, detail, score}; `path` is the selector to pass to other tools (project/env/service, hostname, server id). " +
			"Searches only what the token may read. It does not return logs, variables or status details: next use get_overview, diagnose_service, diagnose_server or explain_failed_deployment.",
		Toolset: mcpkit.ToolsetCore,
		Scopes:  []string{policy.ScopeProjectsRead},
		Hints:   mcpkit.Hints{ReadOnly: true, Destructive: false, Idempotent: true, OpenWorld: false},
		Input:   searchResourcesInput{},
		Output:  searchResourcesOutput{},
		Call: func(ctx context.Context, p *auth.Principal, raw json.RawMessage) (any, error) {
			in, err := mcpkit.Decode[searchResourcesInput](raw)
			if err != nil {
				return nil, err
			}
			q := strings.ToLower(strings.TrimSpace(in.Query))
			if utf8.RuneCountInString(q) < searchMinQuery {
				return nil, invalid("query must have at least %d characters", searchMinQuery)
			}
			types := map[string]bool{}
			for _, t := range in.Types {
				t = strings.ToLower(strings.TrimSpace(t))
				if !containsStr(searchTypes, t) {
					return nil, invalid("unknown type %q; use one of %s", t, strings.Join(searchTypes, ", "))
				}
				types[t] = true
			}
			if in.Limit < 0 {
				return nil, invalid("limit must be positive")
			}
			limit := in.Limit
			if limit == 0 {
				limit = searchDefaultLimit
			}
			limit = min(limit, searchMaxLimit)
			return c.search(ctx, p, q, types, limit)
		},
	}
}

func containsStr(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// ---- scoring ----

type searchField struct {
	name    string
	text    string
	penalty int
}

// fieldScore scores q (lowercase) against one text, 0 for no match.
func fieldScore(q, text string) int {
	t := strings.ToLower(text)
	if t == "" {
		return 0
	}
	if t == q {
		return 100
	}
	if strings.HasPrefix(t, q) {
		return 85
	}
	if i := strings.Index(t, q); i >= 0 {
		if i > 0 && strings.ContainsRune("-_./: @", rune(t[i-1])) {
			return 70
		}
		return 60
	}
	if nq, nt := alnum(q), alnum(t); len(nq) >= 3 && strings.Contains(nt, nq) {
		return 50
	}
	if len(q) >= 3 && subsequence(q, t) && 2*len(q) >= len(t) {
		return 30
	}
	return 0
}

func alnum(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func subsequence(q, t string) bool {
	i := 0
	for _, r := range t {
		if i < len(q) && rune(q[i]) == r {
			i++
		}
	}
	return i == len(q)
}

// best returns the highest score over fields and the field that gave it.
func best(q string, fields ...searchField) (int, string) {
	score, on := 0, ""
	for _, f := range fields {
		s := fieldScore(q, f.text) - f.penalty
		if s > score {
			score, on = s, f.name
		}
	}
	return score, on
}

// idPrefix scores a lowercase id prefix query against an id.
func idPrefix(q, id string) (int, string) {
	if !idLike(q) {
		return 0, ""
	}
	id = strings.ToLower(id)
	switch {
	case id == q:
		return 100, "id"
	case len(q) >= 4 && strings.HasPrefix(id, q):
		return 90, "id"
	}
	return 0, ""
}

// idLike reports a query that could be (the start of) a uuid or a hex SHA.
func idLike(q string) bool {
	if len(q) < 4 {
		return false
	}
	for _, r := range q {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f' || r == '-') {
			return false
		}
	}
	return true
}

// digestLike reports a query that names an image digest or registry ref.
func digestLike(q string) bool {
	return strings.Contains(q, "sha256:") || strings.Contains(q, "@sha256")
}

// ---- the search ----

func (c *composites) search(ctx context.Context, p *auth.Principal, q string, types map[string]bool, limit int) (*searchResourcesOutput, error) {
	want := func(t string) bool { return len(types) == 0 || types[t] }
	run := c.newPartial(ctx, "search_resources", 8)

	var (
		srvs    []servers.Server
		haveSrv bool
	)
	if want("server") {
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
			run.note("servers", mcpkit.CodeForbidden, "the token lacks servers:read; servers are not searched")
		}
	}
	est, err := c.walkEstate(ctx, run, estateSelector{})
	if err != nil {
		run.wait()
		return nil, err
	}
	errs := run.wait()

	var matches []searchMatch
	add := func(m searchMatch, s int, on string) {
		if s <= 0 {
			return
		}
		m.Score, m.MatchedOn = min(s, 100), on
		matches = append(matches, m)
	}

	if want("project") {
		for _, prj := range est.Projects {
			s, on := best(q, searchField{"slug", prj.Slug, 0}, searchField{"name", prj.Name, 2}, searchField{"description", prj.Description, 30})
			if is, ion := idPrefix(q, prj.ID.String()); is > s {
				s, on = is, ion
			}
			add(searchMatch{Type: "project", ID: idStr(prj.ID), Name: prj.Name, Path: prj.Slug, Detail: clip(prj.Description, 120)}, s, on)
		}
	}
	for _, n := range est.Envs {
		if want("environment") {
			s, on := best(q, searchField{"slug", n.Env.Slug, 0}, searchField{"name", n.Env.Name, 2}, searchField{"path", n.path(), 5})
			if is, ion := idPrefix(q, n.Env.ID.String()); is > s {
				s, on = is, ion
			}
			detail := fmt.Sprintf("%s environment, %s", envKind(n), plural(len(n.Services), "service", "services"))
			add(searchMatch{Type: "environment", ID: idStr(n.Env.ID), Name: n.Env.Name, Path: n.path(), Detail: detail}, s, on)
		}
		for _, v := range n.Services {
			sp := n.svcPath(v)
			if want("service") {
				fields := []searchField{
					{"slug", v.Slug, 0}, {"name", v.Name, 2}, {"path", sp, 5},
					{"image", v.Config.Image, 12}, {"gitUrl", services.RedactGitURL(v.Config.GitURL), 15},
				}
				if v.CurrentRelease != nil {
					fields = append(fields,
						searchField{"image", v.CurrentRelease.ImageRef, 12},
						searchField{"digest", v.CurrentRelease.Source.Digest, 12},
						searchField{"digest", v.CurrentRelease.Source.RegistryRef, 12})
				}
				s, on := best(q, fields...)
				if is, ion := idPrefix(q, v.ID.String()); is > s {
					s, on = is, ion
				}
				add(searchMatch{Type: "service", ID: idStr(v.ID), Name: v.Name, Path: sp, Detail: serviceDetail(v)}, s, on)
			}
			if want("domain") {
				for _, d := range v.Domains {
					s, on := best(q, searchField{"hostname", d.Hostname, 0})
					if is, ion := idPrefix(q, d.ID.String()); is > s {
						s, on = is, ion
					}
					dns := "dns unchecked"
					if d.DNSOk != nil {
						dns = "dns ok"
						if !*d.DNSOk {
							dns = "dns not ok"
						}
					}
					add(searchMatch{Type: "domain", ID: idStr(d.ID), Name: d.Hostname, Path: d.Hostname,
						Detail: "service " + sp + ", " + dns}, s, on)
				}
			}
		}
	}
	if want("server") && haveSrv {
		for _, s := range srvs {
			ip, base := "", ""
			if s.PublicIP != nil {
				ip = *s.PublicIP
			}
			if s.BaseDomain != nil {
				base = *s.BaseDomain
			}
			sc, on := best(q, searchField{"name", s.Name, 0}, searchField{"publicIp", ip, 0}, searchField{"baseDomain", base, 5})
			if is, ion := idPrefix(q, s.ID.String()); is > sc {
				sc, on = is, ion
			}
			detail := s.Status
			if ip != "" {
				detail += ", " + ip
			}
			detail += ", " + plural(s.ServiceCount, "service", "services")
			add(searchMatch{Type: "server", ID: idStr(s.ID), Name: s.Name, Path: idStr(s.ID), Detail: detail}, sc, on)
		}
	}

	// Deployments and releases are read per service, so only when asked for
	// or when the query looks like an id, a SHA or a digest.
	deployTypes := want("deployment") || want("release")
	if deployTypes && (len(types) > 0 && (types["deployment"] || types["release"]) || idLike(q) || digestLike(q)) {
		dm, derr := c.searchDeployments(ctx, est, q, want("deployment"), want("release"))
		matches = append(matches, dm...)
		if derr != nil {
			errs = append(errs, *derr)
		}
	}

	rank := map[string]int{}
	for i, t := range searchTypes {
		rank[t] = i
	}
	sort.SliceStable(matches, func(i, j int) bool {
		a, b := matches[i], matches[j]
		if a.Score != b.Score {
			return a.Score > b.Score
		}
		if a.Type != b.Type {
			return rank[a.Type] < rank[b.Type]
		}
		return a.Path < b.Path
	})
	out := &searchResourcesOutput{Envelope: Envelope{Errors: errs, NextTools: []NextTool{}}, Query: q, Total: len(matches)}
	out.Matches = matches
	if len(out.Matches) > limit {
		out.Matches = out.Matches[:limit]
	}
	if out.Matches == nil {
		out.Matches = []searchMatch{}
	}
	out.Summary = searchSummary(out, est)
	out.NextTools = searchNext(out)
	return out, nil
}

func envKind(n *envNode) string {
	switch {
	case n.Env.IsProduction:
		return "production"
	case n.Env.Kind == "preview":
		return "preview"
	}
	return "standard"
}

func serviceDetail(v services.ServiceView) string {
	parts := []string{v.Kind, serviceState(v)}
	switch {
	case v.Config.Image != "":
		parts = append(parts, v.Config.Image)
	case v.Config.GitURL != "":
		parts = append(parts, services.RedactGitURL(v.Config.GitURL))
	}
	if v.Server != nil {
		parts = append(parts, "on "+v.Server.Name)
	}
	return strings.Join(parts, ", ")
}

// searchDeployments looks for deployment ids, commit SHAs, release ids and
// image digests in the latest deployments of the walked services.
func (c *composites) searchDeployments(ctx context.Context, est *estate, q string, wantDep, wantRel bool) ([]searchMatch, *SectionError) {
	type target struct {
		v    services.ServiceView
		path string
	}
	var targets []target
	for _, n := range est.Envs {
		for _, v := range n.Services {
			if len(targets) < searchMaxDeploymentServices {
				targets = append(targets, target{v, n.svcPath(v)})
			}
		}
	}
	var (
		mu      sync.Mutex
		out     []searchMatch
		failed  int
		seenRel = map[string]bool{}
		sem     = make(chan struct{}, 8)
		wg      sync.WaitGroup
	)
	for _, t := range targets {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			ds, _, err := c.Deployments.List(ctx, t.v.ID, "", searchDeploymentsPerService)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				failed++
				return
			}
			for _, d := range ds {
				if wantDep {
					if m, ok := deploymentMatch(q, d, t.path); ok {
						out = append(out, m)
					}
				}
				if wantRel && d.Release != nil && !seenRel[d.Release.ID.String()] {
					if m, ok := releaseMatch(q, d, t.path); ok {
						seenRel[d.Release.ID.String()] = true
						out = append(out, m)
					}
				}
			}
		}()
	}
	wg.Wait()
	var se *SectionError
	if failed > 0 {
		se = &SectionError{Section: "deployments", Code: mcpkit.CodeUnavailable,
			Message: fmt.Sprintf("the deployments of %s could not be read", plural(failed, "service", "services"))}
	}
	return out, se
}

func deploymentMatch(q string, d deployments.DeploymentView, svcPath string) (searchMatch, bool) {
	s, on := idPrefix(q, d.ID.String())
	if d.Commit != nil && len(q) >= 4 && idLike(q) && strings.HasPrefix(strings.ToLower(d.Commit.SHA), q) && 88 > s {
		s, on = 88, "commitSha"
	}
	if d.Release != nil && len(q) >= 4 && idLike(q) && strings.HasPrefix(strings.ToLower(d.Release.Source.CommitSHA), q) && 88 > s {
		s, on = 88, "commitSha"
	}
	if s <= 0 {
		return searchMatch{}, false
	}
	detail := fmt.Sprintf("%s, %s, %s", d.Status, d.Trigger, svcPath)
	if d.Commit != nil && d.Commit.SHA != "" {
		detail += ", commit " + clip(d.Commit.SHA, 12)
	} else if d.Release != nil {
		detail += ", " + clip(d.Release.ImageRef, 80)
	}
	return searchMatch{Type: "deployment", ID: idStr(d.ID), Name: svcPath + " " + utc(d.CreatedAt), Path: idStr(d.ID), Detail: detail, Score: s, MatchedOn: on}, true
}

func releaseMatch(q string, d deployments.DeploymentView, svcPath string) (searchMatch, bool) {
	r := d.Release
	src := r.Source
	s, on := idPrefix(q, r.ID.String())
	for _, f := range []searchField{
		{"image", r.ImageRef, 12}, {"digest", src.Digest, 5}, {"digest", src.RegistryRef, 5}, {"imageId", src.ImageID, 5},
	} {
		if fs := fieldScore(q, f.text) - f.penalty; fs > s {
			s, on = fs, f.name
		}
	}
	if s <= 0 {
		return searchMatch{}, false
	}
	return searchMatch{Type: "release", ID: idStr(r.ID), Name: clip(r.ImageRef, 100), Path: svcPath,
		Detail: "shipped by deployment " + idStr(d.ID) + " (" + d.Status + ")", Score: s, MatchedOn: on}, true
}

func searchSummary(o *searchResourcesOutput, est *estate) string {
	var s string
	switch {
	case o.Total == 0:
		s = fmt.Sprintf("No match for %q.", o.Query)
		if idLike(o.Query) || digestLike(o.Query) {
			s += " Only the 10 latest deployments of each service are searched for ids, SHAs and digests."
		}
	default:
		counts := map[string]int{}
		for _, m := range o.Matches {
			counts[m.Type]++
		}
		var parts []string
		for _, t := range searchTypes {
			if counts[t] > 0 {
				parts = append(parts, fmt.Sprintf("%d %s", counts[t], t))
			}
		}
		s = fmt.Sprintf("%s for %q (%s); best: %s %s.", plural(o.Total, "match", "matches"), o.Query, strings.Join(parts, ", "), o.Matches[0].Type, o.Matches[0].Path)
		if o.Total > len(o.Matches) {
			s += fmt.Sprintf(" %d more are not shown; raise limit or filter with types.", o.Total-len(o.Matches))
		}
	}
	if est.Truncated {
		s += " The estate is larger than one search reads."
	}
	return s
}

func searchNext(o *searchResourcesOutput) []NextTool {
	out := []NextTool{}
	if len(o.Matches) == 0 {
		return out
	}
	m := o.Matches[0]
	switch m.Type {
	case "service":
		out = append(out, NextTool{Tool: "diagnose_service", Args: map[string]any{"service": m.Path}, Why: "Status, deployments, logs and findings of " + m.Path + "."})
	case "domain":
		out = append(out, NextTool{Tool: "diagnose_service", Args: map[string]any{"service": m.Path}, Why: "Diagnose the service behind " + m.Path + "."})
	case "project":
		out = append(out, NextTool{Tool: "get_overview", Args: map[string]any{"project": m.Path}, Why: "State of every environment of " + m.Path + "."})
	case "environment":
		out = append(out, NextTool{Tool: "get_overview", Args: map[string]any{"environment": m.Path}, Why: "State of the services of " + m.Path + "."})
	case "server":
		out = append(out, NextTool{Tool: "diagnose_server", Args: map[string]any{"server": m.ID}, Why: "Health, proxy and placed services of " + m.Name + "."})
	case "deployment":
		out = append(out, NextTool{Tool: "explain_failed_deployment", Args: map[string]any{"deployment": m.ID}, Why: "Timeline and failing step of this deployment."})
	case "release":
		out = append(out, NextTool{Tool: "diagnose_service", Args: map[string]any{"service": m.Path}, Why: "Diagnose the service this release belongs to."})
	}
	return out
}
