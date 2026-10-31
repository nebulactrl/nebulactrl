package composites

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/mcp/mcpkit"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/services"
)

// ReviewChangesInput selects what to review: one service or a whole
// environment.
type ReviewChangesInput struct {
	Service     string `json:"service,omitempty" jsonschema:"service selector: id, project/env/service, hostname or a unique slug (give service OR environment)"`
	Environment string `json:"environment,omitempty" jsonschema:"environment selector: id, project/env or a project with one production environment; reviews every service with staged changes"`
	Detail      string `json:"detail,omitempty" jsonschema:"concise (default) caps the change list per service; full lists up to 200 changes with longer values"`
}

// ChangeEntry is one field-level pending change. Variable values are never
// included: only that a key was added, changed or removed.
type ChangeEntry struct {
	Field string `json:"field" jsonschema:"config.<path>, server, variables.<KEY>, domains.<host>, volumes.<name> or networks.<name>"`
	Kind  string `json:"kind" jsonschema:"added, changed, removed or inherited (a variable that resolves differently because a reference changed)"`
	From  string `json:"from,omitempty"`
	To    string `json:"to,omitempty"`
	Via   string `json:"via,omitempty" jsonschema:"for inherited changes: the references that changed"`
}

// ReviewBlocker is a problem that prevents deploying a service now.
type ReviewBlocker struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// ReviewedService is the staged state of one service.
type ReviewedService struct {
	Service           string          `json:"service" jsonschema:"project/env/service selector"`
	ServiceID         string          `json:"serviceId"`
	Kind              string          `json:"kind"`
	Generation        int64           `json:"generation" jsonschema:"pass this as expectedGeneration to apply_changes"`
	AppliedGeneration int64           `json:"appliedGeneration"`
	Production        bool            `json:"production" jsonschema:"true when the service is in a production environment (applying needs a human approval)"`
	Deployable        bool            `json:"deployable" jsonschema:"false when blocking problems prevent a deployment"`
	FirstDeployment   bool            `json:"firstDeployment" jsonschema:"true when the service was never deployed"`
	Strategy          string          `json:"strategy" jsonschema:"rolling, recreate or route_only"`
	Reasons           []string        `json:"reasons" jsonschema:"why the strategy is recreate"`
	Downtime          string          `json:"downtime"`
	Warnings          []string        `json:"warnings"`
	Blocking          []ReviewBlocker `json:"blocking"`
	UnresolvedRefs    []string        `json:"unresolvedRefs" jsonschema:"variable reference problems (also listed in blocking)"`
	ChangeCount       int             `json:"changeCount"`
	Changes           []ChangeEntry   `json:"changes"`
	ChangesTruncated  bool            `json:"changesTruncated,omitempty"`
	Processes         []ReviewProcess `json:"processes,omitempty" jsonschema:"a service with several processes: the rollout in order (port processes first), one entry per process"`
}

// ReviewProcess is one process of the rollout a deployment would run.
type ReviewProcess struct {
	Name       string `json:"name"`
	Default    bool   `json:"default"`
	Port       int    `json:"port"`
	Strategy   string `json:"strategy" jsonschema:"rolling or recreate"`
	Replicas   int    `json:"replicas"`
	Singleton  bool   `json:"singleton"`
	Order      int    `json:"order" jsonschema:"position in the rollout: processes with an HTTP port roll first"`
	WillChange bool   `json:"willChange" jsonschema:"the deployment changes this process"`
}

// ReviewChangesOutput is the result of review_changes.
type ReviewChangesOutput struct {
	Envelope
	Environment  string            `json:"environment" jsonschema:"project/env selector"`
	Production   bool              `json:"production"`
	PendingCount int               `json:"pendingCount" jsonschema:"number of services with staged changes"`
	Services     []ReviewedService `json:"services"`
}

const (
	reviewMaxServices      = 40
	reviewChangesConcise   = 30
	reviewChangesFull      = 200
	reviewValueClipConcise = 200
	reviewValueClipFull    = 1000
)

func (c *composites) reviewChanges() mcpkit.Tool {
	return mcpkit.Tool{
		Name:  "review_changes",
		Title: "Review staged changes",
		Description: "Gate before apply_changes. Shows, for one service or every service of an environment with staged changes (generation > appliedGeneration): " +
			"the field-level pending changes (secrets masked; variables appear by key only, never with values), the deploy plan (strategy rolling or recreate, reasons, " +
			"warnings, downtime), blocking problems and unresolved variable references, the generation to pass as expectedGeneration, and whether the environment is production. " +
			"Use it after stage_changes (or after any edit) and before apply_changes. Give `service` OR `environment`. " +
			"It changes nothing and deploys nothing. Next: apply_changes with the exact expectedGeneration per service (ready in nextTools); " +
			"services that cannot deploy are listed with why.",
		Toolset: mcpkit.ToolsetCore,
		Scopes:  []string{policy.ScopeProjectsRead},
		Hints:   mcpkit.Hints{ReadOnly: true, Destructive: false, Idempotent: true, OpenWorld: false},
		Input:   ReviewChangesInput{},
		Output:  ReviewChangesOutput{},
		Call: func(ctx context.Context, p *auth.Principal, raw json.RawMessage) (any, error) {
			in, err := mcpkit.Decode[ReviewChangesInput](raw)
			if err != nil {
				return nil, err
			}
			return c.review(ctx, in)
		},
	}
}

func (c *composites) review(ctx context.Context, in ReviewChangesInput) (ReviewChangesOutput, error) {
	detail, err := mcpkit.ParseDetail(in.Detail)
	if err != nil {
		return ReviewChangesOutput{}, err
	}
	in.Service, in.Environment = strings.TrimSpace(in.Service), strings.TrimSpace(in.Environment)
	if (in.Service == "") == (in.Environment == "") {
		return ReviewChangesOutput{}, invalid("give exactly one of service or environment")
	}

	out := ReviewChangesOutput{Services: []ReviewedService{}}
	type target struct {
		view services.ServiceView
		sel  string
	}
	var targets []target
	if in.Service != "" {
		sv, err := c.Resolver.Service(ctx, in.Service)
		if err != nil {
			return out, err
		}
		sc, err := c.scope(ctx, sv.ID)
		if err != nil {
			return out, err
		}
		out.Environment = path(sc.ProjectSlug, sc.EnvSlug)
		out.Production = sc.IsProduction
		if sv.HasStagedChanges {
			targets = append(targets, target{view: sv, sel: selectorOf(sc)})
		}
	} else {
		env, err := c.Resolver.Environment(ctx, in.Environment)
		if err != nil {
			return out, err
		}
		proj, err := c.Projects.Get(ctx, env.ProjectID)
		if err != nil {
			return out, err
		}
		out.Environment = path(proj.Slug, env.Slug)
		out.Production = env.IsProduction
		cursor := ""
		for {
			page, next, err := c.Services.List(ctx, env.ID, cursor, mcpkit.MaxPage)
			if err != nil {
				return out, err
			}
			for _, sv := range page {
				if sv.HasStagedChanges {
					targets = append(targets, target{view: sv, sel: path(proj.Slug, env.Slug, sv.Slug)})
				}
			}
			if next == "" {
				break
			}
			cursor = next
		}
	}

	omitted := 0
	if len(targets) > reviewMaxServices {
		omitted = len(targets) - reviewMaxServices
		targets = targets[:reviewMaxServices]
	}
	out.PendingCount = len(targets) + omitted

	results := make([]*ReviewedService, len(targets))
	pr := c.newPartial(ctx, "review_changes", 4)
	for i, t := range targets {
		pr.run("plan "+t.sel, func(ctx context.Context) error {
			r, err := c.reviewOne(ctx, t.sel, t.view, out.Production, detail.Full())
			if err != nil {
				return err
			}
			results[i] = &r
			return nil
		})
	}
	out.Errors = pr.wait()
	for _, r := range results {
		if r != nil {
			out.Services = append(out.Services, *r)
		}
	}
	sort.SliceStable(out.Services, func(i, j int) bool { return out.Services[i].Service < out.Services[j].Service })

	c.reviewSummary(&out, omitted)
	return out, nil
}

// reviewOne reads the plan (which carries the pending changes) of a service.
func (c *composites) reviewOne(ctx context.Context, sel string, sv services.ServiceView, production, full bool) (ReviewedService, error) {
	plan, err := c.Deployments.Plan(ctx, sv.ID)
	if err != nil {
		return ReviewedService{}, err
	}
	r := ReviewedService{
		Service: sel, ServiceID: sv.ID.String(), Kind: sv.Kind,
		Generation: sv.Generation, AppliedGeneration: sv.AppliedGeneration, Production: production,
		FirstDeployment: sv.CurrentDeploymentID == nil,
		Strategy:        plan.Strategy, Reasons: nonNil(plan.Reasons), Warnings: nonNil(plan.Warnings),
		Blocking: []ReviewBlocker{}, UnresolvedRefs: []string{},
	}
	r.Downtime = downtimeNote(plan.Strategy, plan.Reasons)
	for _, b := range plan.Blocking {
		r.Blocking = append(r.Blocking, ReviewBlocker{Code: b.Code, Message: clip(b.Message, 500)})
		if refProblem(b.Code) {
			r.UnresolvedRefs = append(r.UnresolvedRefs, clip(b.Message, 500))
		}
	}
	r.Deployable = len(r.Blocking) == 0
	for _, p := range plan.Processes {
		r.Processes = append(r.Processes, ReviewProcess{
			Name: p.Name, Default: p.Default, Port: p.Port, Strategy: p.Strategy, Replicas: p.Replicas,
			Singleton: p.Singleton, Order: p.Order, WillChange: p.WillChange,
		})
	}

	limit, valClip := reviewChangesConcise, reviewValueClipConcise
	if full {
		limit, valClip = reviewChangesFull, reviewValueClipFull
	}
	r.ChangeCount = len(plan.Changes)
	r.Changes = make([]ChangeEntry, 0, min(len(plan.Changes), limit))
	for i, fc := range plan.Changes {
		if i == limit {
			r.ChangesTruncated = true
			break
		}
		r.Changes = append(r.Changes, changeEntryOf(fc, valClip))
	}
	return r, nil
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// refProblem reports whether a blocking code is a variable reference problem.
func refProblem(code string) bool {
	switch code {
	case "unresolved_reference", "reference_cycle", "invalid_variables", "invalid_template", "ungenerated_function":
		return true
	}
	return false
}

func downtimeNote(strategy string, reasons []string) string {
	switch strategy {
	case "recreate":
		note := "Brief downtime: the running container stops before the new one starts"
		if len(reasons) > 0 {
			note += " (" + strings.Join(reasons, ", ") + ")"
		}
		return note + "."
	case "rolling":
		return "No downtime expected: the new container starts and must pass its health check before the old one stops."
	case "route_only":
		return "No container is started: only the proxy route changes."
	}
	return ""
}

// changeEntryOf renders a pending change. Variable values never appear:
// from/to say only whether the key is set; a generator descriptor is kept.
func changeEntryOf(fc services.FieldChange, valClip int) ChangeEntry {
	e := ChangeEntry{Field: fc.Field, Via: fc.Via}
	switch {
	case fc.Origin == services.OriginInherited:
		e.Kind = "inherited"
	case fc.From == "" && fc.To != "":
		e.Kind = "added"
	case fc.To == "" && fc.From != "":
		e.Kind = "removed"
	default:
		e.Kind = "changed"
	}
	if strings.HasPrefix(fc.Field, "variables.") && !strings.HasSuffix(fc.Field, ".scope") {
		if fc.From != "" {
			e.From = "(set)"
		}
		switch {
		case strings.HasPrefix(fc.To, "generated · "):
			e.To = fc.To
		case fc.To != "":
			e.To = "(set)"
		}
		return e
	}
	e.From, e.To = clip(fc.From, valClip), clip(fc.To, valClip)
	return e
}

func (c *composites) reviewSummary(out *ReviewChangesOutput, omitted int) {
	if out.PendingCount == 0 {
		out.Summary = "No staged changes in " + out.Environment + ": everything staged has been applied."
		out.NextTools = []NextTool{}
		return
	}
	var ready, blocked []ReviewedService
	for _, s := range out.Services {
		if s.Deployable {
			ready = append(ready, s)
		} else {
			blocked = append(blocked, s)
		}
	}
	sum := plural(out.PendingCount, "service has", "services have") + " staged changes in " + out.Environment
	if len(blocked) > 0 {
		sum += fmt.Sprintf("; %d cannot deploy until the blocking problems are fixed", len(blocked))
	}
	if out.Production {
		sum += ". This is a production environment: apply_changes needs a human approval"
	}
	if omitted > 0 {
		sum += fmt.Sprintf(". Only the first %d services are listed; review the rest one service at a time", reviewMaxServices)
	}
	out.Summary = sum + "."

	out.NextTools = []NextTool{}
	if len(ready) > 0 {
		items := make([]any, 0, len(ready))
		for _, s := range ready {
			items = append(items, map[string]any{"service": s.Service, "expectedGeneration": s.Generation})
		}
		why := "Deploy the reviewed changes; expectedGeneration makes apply_changes refuse if anything changed since this review."
		if out.Production {
			why += " A human must approve the production deploy."
		}
		out.NextTools = append(out.NextTools, NextTool{Tool: "apply_changes", Args: map[string]any{"services": items}, Why: why})
	}
	for _, s := range blocked {
		out.NextTools = append(out.NextTools, NextTool{
			Tool: "stage_changes", Args: map[string]any{"service": s.Service},
			Why: "Fix the blocking problem of " + s.Service + ": " + firstBlocker(s),
		})
	}
}

func firstBlocker(s ReviewedService) string {
	if len(s.Blocking) == 0 {
		return ""
	}
	return s.Blocking[0].Code + ": " + clip(s.Blocking[0].Message, 200)
}
