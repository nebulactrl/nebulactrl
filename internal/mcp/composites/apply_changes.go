package composites

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/deployments"
	"github.com/nebulactrl/nebulactrl/internal/mcp/mcpkit"
	"github.com/nebulactrl/nebulactrl/internal/policy"
)

// ApplyChangesInput names what to deploy. Give `services` (each with the
// generation you reviewed) or `environment` (with confirmPending).
type ApplyChangesInput struct {
	Services       []ApplyChangesService `json:"services,omitempty" jsonschema:"services to deploy, each with the generation you reviewed (from review_changes)"`
	Environment    string                `json:"environment,omitempty" jsonschema:"environment selector: deploy every service with staged changes in it (give confirmPending)"`
	ConfirmPending *int                  `json:"confirmPending,omitempty" jsonschema:"with environment: the number of services with staged changes you reviewed; a different current number fails with plan_changed and deploys nothing"`
	Message        string                `json:"message,omitempty" jsonschema:"optional note stored on the deployments (max 200 characters)"`
	NoCache        bool                  `json:"noCache,omitempty" jsonschema:"build every git service of this call without cache (--no-cache --pull) for these deployments only; other services deploy as usual. apply_changes only deploys services with staged changes; a service without any is rebuilt from the web app, the CLI (--no-cache) or the REST API"`
	WaitSeconds    int                   `json:"waitSeconds,omitempty" jsonschema:"0 (default) returns right after starting; 1..50 waits up to that long for a terminal outcome, with failure summary and log tail"`
	Detail         string                `json:"detail,omitempty" jsonschema:"concise (default) or full (longer log tail)"`
}

// ApplyChangesService is one service to deploy.
type ApplyChangesService struct {
	Service            string `json:"service" jsonschema:"service selector: id, project/env/service, hostname or a unique slug"`
	ExpectedGeneration int64  `json:"expectedGeneration" jsonschema:"REQUIRED. The generation review_changes showed; if the service moved on, nothing is deployed (generation_conflict)"`
}

// ApplyPlan is the plan of a service just before it was deployed.
type ApplyPlan struct {
	Service  string   `json:"service"`
	Strategy string   `json:"strategy" jsonschema:"rolling, recreate or route_only"`
	Reasons  []string `json:"reasons"`
	Warnings []string `json:"warnings"`
	Downtime string   `json:"downtime"`
	Changes  int      `json:"changes" jsonschema:"number of field changes this deployment ships"`
}

// ApplySkipped is a staged service of an environment deploy that was not started.
type ApplySkipped struct {
	Service string `json:"service"`
	Reason  string `json:"reason"`
}

// ApplyChangesOutput is the result of apply_changes.
type ApplyChangesOutput struct {
	Envelope
	Production  bool                `json:"production" jsonschema:"true when a production environment was deployed (a human approved it)"`
	Deployments []AppliedDeployment `json:"deployments"`
	Plans       []ApplyPlan         `json:"plans"`
	Skipped     []ApplySkipped      `json:"skipped" jsonschema:"staged services of an environment deploy that were not started, with why"`
	WaitedSec   int                 `json:"waitedSeconds" jsonschema:"seconds the call was allowed to wait (0 = it did not wait)"`
}

const applyMaxServices = 50

func (c *composites) applyChanges() mcpkit.Tool {
	return mcpkit.Tool{
		Name:  "apply_changes",
		Title: "Apply staged changes (deploy)",
		Description: "Deploy staged changes: the only way staged edits become running. Give `services` [{service, expectedGeneration}] (the generation review_changes showed) " +
			"or `environment` with confirmPending (the number of staged services you reviewed). " +
			"Every generation is verified first: if anything moved since your review, NOTHING is deployed and you get generation_conflict (re-run review_changes). " +
			"Services are grouped by environment and started in dependency order (databases and referenced providers first). " +
			"Production environments need a human approval: the server asks the user and re-runs the call once approved; never try to work around it. " +
			"Returns the deployments (id, status, strategy) and the plan of each service (strategy, reasons, warnings). " +
			"With waitSeconds (1..50) it waits for the terminal outcome and adds the failure summary (status message, failed step) and a short untrusted log tail; " +
			"without it the deployments run on in the background (check with verify_service). outcome_uncertain means the agent acknowledged the command but its server went offline: do not retry. " +
			"Next: verify_service, explain_failed_deployment if it failed, rollback_service to go back.",
		Toolset: mcpkit.ToolsetCore,
		Scopes:  []string{policy.ScopeDeploymentsWrite},
		Hints:   mcpkit.Hints{ReadOnly: false, Destructive: false, Idempotent: false, OpenWorld: false},
		Input:   ApplyChangesInput{},
		Output:  ApplyChangesOutput{},
		Call: func(ctx context.Context, p *auth.Principal, raw json.RawMessage) (any, error) {
			in, err := mcpkit.Decode[ApplyChangesInput](raw)
			if err != nil {
				return nil, err
			}
			return c.apply(ctx, p, in)
		},
	}
}

// applyTarget is a service about to be deployed.
type applyTarget struct {
	id         uuid.UUID
	sel        string
	envID      uuid.UUID
	envSel     string
	production bool
}

// applyBatch is the services of one environment.
type applyBatch struct {
	envID      uuid.UUID
	envSel     string
	production bool
	ids        []uuid.UUID // nil = every staged service
}

func (c *composites) apply(ctx context.Context, p *auth.Principal, in ApplyChangesInput) (ApplyChangesOutput, error) {
	detail, err := mcpkit.ParseDetail(in.Detail)
	if err != nil {
		return ApplyChangesOutput{}, err
	}
	wait, err := parseWaitSeconds(in.WaitSeconds)
	if err != nil {
		return ApplyChangesOutput{}, err
	}
	if n := utf8.RuneCountInString(strings.TrimSpace(in.Message)); n > deployments.MaxMessageLen {
		return ApplyChangesOutput{}, invalid("message must be at most %d characters (got %d)", deployments.MaxMessageLen, n)
	}
	in.Environment = strings.TrimSpace(in.Environment)
	if (len(in.Services) == 0) == (in.Environment == "") {
		return ApplyChangesOutput{}, invalid("give exactly one of services or environment")
	}

	var targets []applyTarget
	var batches []applyBatch
	if in.Environment == "" {
		if targets, err = c.applyVerifyServices(ctx, in.Services); err != nil {
			return ApplyChangesOutput{}, err
		}
		if batches, err = applyGroup(targets); err != nil {
			return ApplyChangesOutput{}, err
		}
	} else {
		if in.ConfirmPending == nil {
			return ApplyChangesOutput{}, invalid("confirmPending is required with environment: the number of services with staged changes you reviewed (run review_changes)")
		}
		var b applyBatch
		if targets, b, err = c.applyVerifyEnvironment(ctx, in.Environment, *in.ConfirmPending); err != nil {
			return ApplyChangesOutput{}, err
		}
		batches = []applyBatch{b}
	}

	out := ApplyChangesOutput{Deployments: []AppliedDeployment{}, Plans: []ApplyPlan{}, Skipped: []ApplySkipped{}, Errors: []SectionError{}, NextTools: []NextTool{}, WaitedSec: in.WaitSeconds}
	if len(targets) == 0 {
		out.Summary = "Nothing to apply: no service in " + in.Environment + " has staged changes."
		return out, nil
	}
	for _, b := range batches {
		out.Production = out.Production || b.production
	}

	// Plan of every service, read right before deploying.
	plans := make([]*deployments.Plan, len(targets))
	pr := c.newPartial(ctx, "apply_changes", 4)
	for i, t := range targets {
		pr.run("plan "+t.sel, func(ctx context.Context) error {
			pl, err := c.Deployments.Plan(ctx, t.id)
			if err != nil {
				return err
			}
			plans[i] = &pl
			return nil
		})
	}
	out.Errors = append(out.Errors, pr.wait()...)
	for i, t := range targets {
		if pl := plans[i]; pl != nil {
			out.Plans = append(out.Plans, ApplyPlan{
				Service: t.sel, Strategy: pl.Strategy, Reasons: nonNil(pl.Reasons), Warnings: nonNil(pl.Warnings),
				Downtime: downtimeNote(pl.Strategy, pl.Reasons), Changes: len(pl.Changes),
			})
		}
	}

	// Deploy, one DeployStaged call (one production approval) per environment.
	created := map[uuid.UUID]deployments.DeploymentView{}
	var order []uuid.UUID
	var batchErr error
	for i, b := range batches {
		res, err := c.Deployments.DeployStagedWith(ctx, b.envID, b.ids, in.Message, in.NoCache)
		for _, v := range res.Items {
			created[v.ServiceID] = v
			order = append(order, v.ServiceID)
		}
		if err == nil {
			continue
		}
		if isApproval(err) {
			return ApplyChangesOutput{}, err
		}
		if len(created) == 0 {
			return ApplyChangesOutput{}, changesErr(err)
		}
		batchErr = err
		out.Errors = append(out.Errors, sectionError("deploy "+b.envSel, err))
		for _, rest := range batches[i+1:] {
			out.Errors = append(out.Errors, SectionError{Section: "deploy " + rest.envSel, Code: "not_attempted", Message: "not started because an earlier environment failed"})
		}
		break
	}

	info := map[uuid.UUID]applyTarget{}
	for _, t := range targets {
		info[t.id] = t
	}
	views := make(map[uuid.UUID]deployments.DeploymentView, len(created))
	for _, sid := range order {
		v := created[sid]
		views[v.ID] = v
	}
	if wait > 0 {
		var errs map[uuid.UUID]error
		views, errs = c.waitDeployments(ctx, views, wait)
		for id, err := range errs {
			out.Errors = append(out.Errors, sectionError("read deployment "+id.String(), err))
		}
	}
	tail := 0
	if wait > 0 {
		tail = 20
		if detail.Full() {
			tail = 50
		}
	}
	for _, sid := range order {
		v := views[created[sid].ID]
		a := appliedFrom(info[sid].sel, v)
		if wait > 0 {
			out.Errors = append(out.Errors, c.describeOutcome(ctx, p, &a, v, tail)...)
		}
		out.Deployments = append(out.Deployments, a)
	}

	// Staged services that were not started.
	planByID := map[uuid.UUID]*deployments.Plan{}
	for i, t := range targets {
		planByID[t.id] = plans[i]
	}
	if batchErr == nil {
		for _, t := range targets {
			if _, ok := created[t.id]; ok {
				continue
			}
			reason := "not started: a deployment is already in progress, or its blocking problems prevent it"
			if pl := planByID[t.id]; pl != nil && len(pl.Blocking) > 0 {
				reason = "cannot deploy: " + pl.Blocking[0].Code + ": " + clip(pl.Blocking[0].Message, 300)
			}
			out.Skipped = append(out.Skipped, ApplySkipped{Service: t.sel, Reason: reason})
		}
	}
	sort.SliceStable(out.Errors, func(i, j int) bool { return out.Errors[i].Section < out.Errors[j].Section })
	c.applySummary(&out, in.WaitSeconds)
	return out, nil
}

// applyVerifyServices resolves the requested services and checks every
// expectedGeneration. Any problem fails the whole call before anything is
// deployed.
func (c *composites) applyVerifyServices(ctx context.Context, items []ApplyChangesService) ([]applyTarget, error) {
	if len(items) > applyMaxServices {
		return nil, invalid("at most %d services per call", applyMaxServices)
	}
	var targets []applyTarget
	seen := map[uuid.UUID]bool{}
	var moved []map[string]any
	var notStaged []string
	for i, it := range items {
		if strings.TrimSpace(it.Service) == "" {
			return nil, invalid("services[%d].service is required", i)
		}
		if it.ExpectedGeneration <= 0 {
			return nil, invalid("services[%d].expectedGeneration is required (the generation review_changes showed for %s)", i, it.Service)
		}
		sv, err := c.Resolver.Service(ctx, it.Service)
		if err != nil {
			return nil, err
		}
		if seen[sv.ID] {
			return nil, invalid("service %s is listed twice", it.Service)
		}
		seen[sv.ID] = true
		sc, err := c.scope(ctx, sv.ID)
		if err != nil {
			return nil, err
		}
		t := applyTarget{id: sv.ID, sel: selectorOf(sc), envID: sv.EnvironmentID, envSel: path(sc.ProjectSlug, sc.EnvSlug), production: sc.IsProduction}
		switch {
		case sv.Generation != it.ExpectedGeneration:
			moved = append(moved, map[string]any{"service": t.sel, "expectedGeneration": it.ExpectedGeneration, "currentGeneration": sv.Generation})
		case !sv.HasStagedChanges:
			notStaged = append(notStaged, t.sel)
		}
		targets = append(targets, t)
	}
	if len(moved) > 0 {
		m := moved[0]
		e := mcpkit.Errf("generation_conflict",
			"%s is at generation %v, not the expected %v: the change set moved since your review. Nothing was deployed. Run review_changes again and apply the new generation.",
			m["service"], m["currentGeneration"], m["expectedGeneration"])
		e.Data = map[string]any{"mismatches": moved}
		return nil, e
	}
	if len(notStaged) > 0 {
		return nil, mcpkit.Errf("nothing_staged", "no staged changes to apply for %s. Nothing was deployed.", strings.Join(notStaged, ", "))
	}
	return targets, nil
}

// applyVerifyEnvironment lists the environment's staged services and checks
// their number against confirmPending.
func (c *composites) applyVerifyEnvironment(ctx context.Context, sel string, confirm int) ([]applyTarget, applyBatch, error) {
	env, err := c.Resolver.Environment(ctx, sel)
	if err != nil {
		return nil, applyBatch{}, err
	}
	proj, err := c.Projects.Get(ctx, env.ProjectID)
	if err != nil {
		return nil, applyBatch{}, err
	}
	envSel := path(proj.Slug, env.Slug)
	var targets []applyTarget
	cursor := ""
	for {
		page, next, err := c.Services.List(ctx, env.ID, cursor, mcpkit.MaxPage)
		if err != nil {
			return nil, applyBatch{}, err
		}
		for _, sv := range page {
			if sv.HasStagedChanges {
				targets = append(targets, applyTarget{id: sv.ID, sel: path(proj.Slug, env.Slug, sv.Slug), envID: env.ID, envSel: envSel, production: env.IsProduction})
			}
		}
		if next == "" {
			break
		}
		cursor = next
	}
	if len(targets) != confirm {
		names := make([]string, len(targets))
		for i, t := range targets {
			names[i] = t.sel
		}
		e := mcpkit.Errf("plan_changed",
			"%s has %d services with staged changes, not the %d you reviewed. Nothing was deployed. Run review_changes for the environment again.", envSel, len(targets), confirm)
		e.Data = map[string]any{"staged": len(targets), "confirmPending": confirm, "services": names}
		return nil, applyBatch{}, e
	}
	return targets, applyBatch{envID: env.ID, envSel: envSel, production: env.IsProduction}, nil
}

// applyGroup groups targets by environment, production first: its approval
// is requested before anything is deployed, so a re-run after the approval
// never meets deployments this call already started. Two production
// environments in one call are refused for the same reason.
func applyGroup(targets []applyTarget) ([]applyBatch, error) {
	idx := map[uuid.UUID]int{}
	var batches []applyBatch
	for _, t := range targets {
		i, ok := idx[t.envID]
		if !ok {
			i = len(batches)
			idx[t.envID] = i
			batches = append(batches, applyBatch{envID: t.envID, envSel: t.envSel, production: t.production})
		}
		batches[i].ids = append(batches[i].ids, t.id)
	}
	prod := 0
	for _, b := range batches {
		if b.production {
			prod++
		}
	}
	if prod > 1 {
		return nil, invalid("apply production environments one at a time: each needs its own human approval")
	}
	sort.SliceStable(batches, func(i, j int) bool { return batches[i].production && !batches[j].production })
	return batches, nil
}

func (c *composites) applySummary(out *ApplyChangesOutput, waited int) {
	var parts []string
	failed, uncertain, inProgress, ok := 0, 0, 0, 0
	for _, d := range out.Deployments {
		switch {
		case d.Outcome == mcpkit.CodeOutcomeUncertain:
			uncertain++
		case !d.Terminal:
			inProgress++
		case d.Outcome == string(deployments.Succeeded):
			ok++
		default:
			failed++
		}
		line := fmt.Sprintf("%s: %s", d.Service, d.Outcome)
		if d.StatusMessage != nil && d.Terminal && d.Outcome != string(deployments.Succeeded) {
			line += " (see statusMessage)"
		}
		parts = append(parts, line)
	}
	sum := fmt.Sprintf("Started %s", plural(len(out.Deployments), "deployment", "deployments"))
	if waited > 0 {
		sum = fmt.Sprintf("Started %s and waited up to %ds: %d succeeded, %d failed, %d still running", plural(len(out.Deployments), "deployment", "deployments"), waited, ok, failed, inProgress)
		if uncertain > 0 {
			sum += fmt.Sprintf(", %d with an uncertain outcome (agent acknowledged, server offline; do not retry)", uncertain)
		}
	} else {
		sum += "; they run in the background (use waitSeconds or verify_service to follow them)"
	}
	if len(out.Skipped) > 0 {
		sum += fmt.Sprintf(". %d staged services were not started (see skipped)", len(out.Skipped))
	}
	out.Summary = sum + ". " + strings.Join(parts, "; ")

	out.NextTools = []NextTool{}
	for _, d := range out.Deployments {
		if len(out.NextTools) >= 6 {
			break
		}
		switch {
		case d.Terminal && d.Outcome != string(deployments.Succeeded):
			out.NextTools = append(out.NextTools,
				NextTool{Tool: "explain_failed_deployment", Args: map[string]any{"deployment": d.DeploymentID}, Why: "Find the cause of the failed deployment of " + d.Service + " and the smallest fix."},
				NextTool{Tool: "rollback_service", Args: map[string]any{"service": d.Service, "to": "previous", "dryRun": true}, Why: "Preview going back to the last good release of " + d.Service + "."})
		case d.Outcome == mcpkit.CodeOutcomeUncertain:
			out.NextTools = append(out.NextTools,
				NextTool{Tool: "verify_service", Args: map[string]any{"service": d.Service}, Why: "The outcome is unknown until the server reconnects; check the observed state later. Do not redeploy blindly."})
		case d.Terminal:
			out.NextTools = append(out.NextTools,
				NextTool{Tool: "verify_service", Args: map[string]any{"service": d.Service}, Why: "Smoke-test " + d.Service + " after the deployment."},
				NextTool{Tool: "rollback_service", Args: map[string]any{"service": d.Service, "to": "previous", "dryRun": true}, Why: "Preview going back if verify_service finds a problem."})
		default:
			out.NextTools = append(out.NextTools,
				NextTool{Tool: "verify_service", Args: map[string]any{"service": d.Service}, Why: "The deployment of " + d.Service + " is still running; check again once it finished."},
				NextTool{Tool: "rollback_service", Args: map[string]any{"service": d.Service, "to": "previous", "dryRun": true}, Why: "Preview going back to the last good release if the deployment fails."})
		}
	}
	if len(out.NextTools) > 6 {
		out.NextTools = out.NextTools[:6]
	}
}
