package composites

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/deployments"
	"github.com/nebulactrl/nebulactrl/internal/mcp/mcpkit"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/services"
)

// RollbackServiceInput selects the service and the release to go back to.
type RollbackServiceInput struct {
	Service     string `json:"service" jsonschema:"service selector: id, project/env/service, hostname or a unique slug"`
	To          string `json:"to,omitempty" jsonschema:"previous (default: the newest succeeded deployment that is not running now), a release id, a deployment id, or a commit sha prefix of at least 7 hex characters"`
	DryRun      bool   `json:"dryRun,omitempty" jsonschema:"true: only preview what would roll back, start nothing. Default false: the rollback starts (production needs a human approval)"`
	WaitSeconds int    `json:"waitSeconds,omitempty" jsonschema:"0 (default) returns right after starting; 1..50 waits for the terminal outcome"`
	Detail      string `json:"detail,omitempty" jsonschema:"concise (default) or full (more config changes and a longer log tail)"`
}

// RollbackTarget describes a deployment and its release.
type RollbackTarget struct {
	DeploymentID string `json:"deploymentId"`
	ReleaseID    string `json:"releaseId"`
	Image        string `json:"image,omitempty"`
	SourceType   string `json:"sourceType,omitempty"`
	Commit       string `json:"commit,omitempty" jsonschema:"git commit of the release (full sha)"`
	FinishedAt   string `json:"finishedAt,omitempty"`
}

// RollbackPreview states what a rollback changes and what it does not.
type RollbackPreview struct {
	Target  RollbackTarget  `json:"target"`
	Current *RollbackTarget `json:"current,omitempty" jsonschema:"what runs now"`
	// RollsBack lists what the rollback restores.
	RollsBack []string `json:"rollsBack"`
	// NotRolledBack is the fixed disclaimer: data is never rolled back.
	NotRolledBack string `json:"notRolledBack"`
	// ConfigChanges are the settings that differ between what runs now and the target release.
	ConfigChanges        []ChangeEntry `json:"configChanges"`
	ConfigChangesCount   int           `json:"configChangesCount"`
	VariablesSnapshot    string        `json:"variablesSnapshot" jsonschema:"whether the target release still carries its variables (kept) or they were pruned (the current variables are used)"`
	VariablesAdded       []string      `json:"variablesAdded" jsonschema:"variable keys the target release has and the running one lacks (keys only)"`
	VariablesRemoved     []string      `json:"variablesRemoved" jsonschema:"variable keys the running release has and the target lacks"`
	VariablesChanged     []string      `json:"variablesChanged" jsonschema:"variable keys whose value differs (values never shown)"`
	Strategy             string        `json:"strategy" jsonschema:"rolling, recreate or route_only"`
	Downtime             string        `json:"downtime"`
	ImageCheck           string        `json:"imageCheck" jsonschema:"whether the image of the target release is still available"`
	RebuildCommit        string        `json:"rebuildCommit,omitempty" jsonschema:"for git services: the commit to rebuild when the image was pruned (image_pruned)"`
	StagedChangesInfo    string        `json:"stagedChanges,omitempty" jsonschema:"set when the service has staged changes: a rollback does not touch them"`
	DeploymentInProgress bool          `json:"deploymentInProgress"`
}

// RollbackServiceOutput is the result of rollback_service.
type RollbackServiceOutput struct {
	Envelope
	Service    string `json:"service"`
	ServiceID  string `json:"serviceId"`
	DryRun     bool   `json:"dryRun"`
	Production bool   `json:"production"`
	// Started is true when a rollback deployment was created.
	Started    bool               `json:"started"`
	Preview    RollbackPreview    `json:"preview"`
	Deployment *AppliedDeployment `json:"deployment,omitempty"`
	Disclaimer string             `json:"disclaimer"`
}

const (
	rollbackPage     = 50
	rollbackMaxPages = 10
)

var rollbackShaRe = regexp.MustCompile(`^[0-9a-fA-F]{7,40}$`)

func (c *composites) rollbackService() mcpkit.Tool {
	return mcpkit.Tool{
		Name:  "rollback_service",
		Title: "Roll back a service",
		Description: "Go back to the release of an earlier SUCCEEDED deployment: `to` is previous (default), a release id, a deployment id or a commit sha prefix (7+ hex). " +
			"Always includes a preview: which image, config and variable keys come back, the downtime to expect, and the fixed disclaimer \"" + deployments.RollbackDisclaimer + "\" " +
			"(data is never rolled back). Use dryRun:true to only preview. A real rollback creates a new deployment of the old release; " +
			"production needs a human approval (the server asks the user and re-runs the call once approved; do not try to work around it). " +
			"Errors with a code: already_current (that release already runs), not_rollbackable (target never succeeded), not_found (no match; lists candidates), " +
			"image_pruned (a git release's built image is gone: rebuild its commit instead; the error names it). Staged changes are untouched. " +
			"With waitSeconds (1..50) it waits for the outcome and adds the failure summary and an untrusted log tail. Next: verify_service.",
		Toolset: mcpkit.ToolsetCore,
		Scopes:  []string{policy.ScopeDeploymentsRollback},
		Hints:   mcpkit.Hints{ReadOnly: false, Destructive: true, Idempotent: false, OpenWorld: false},
		Input:   RollbackServiceInput{},
		Output:  RollbackServiceOutput{},
		Call: func(ctx context.Context, p *auth.Principal, raw json.RawMessage) (any, error) {
			in, err := mcpkit.Decode[RollbackServiceInput](raw)
			if err != nil {
				return nil, err
			}
			return c.rollback(ctx, p, in)
		},
	}
}

func (c *composites) rollback(ctx context.Context, p *auth.Principal, in RollbackServiceInput) (RollbackServiceOutput, error) {
	detail, err := mcpkit.ParseDetail(in.Detail)
	if err != nil {
		return RollbackServiceOutput{}, err
	}
	wait, err := parseWaitSeconds(in.WaitSeconds)
	if err != nil {
		return RollbackServiceOutput{}, err
	}
	to := strings.ToLower(strings.TrimSpace(in.To))
	if to == "" {
		to = "previous"
	}
	if strings.TrimSpace(in.Service) == "" {
		return RollbackServiceOutput{}, invalid("service is required")
	}
	sv, err := c.Resolver.Service(ctx, in.Service)
	if err != nil {
		return RollbackServiceOutput{}, err
	}
	sc, err := c.scope(ctx, sv.ID)
	if err != nil {
		return RollbackServiceOutput{}, err
	}
	sel := selectorOf(sc)

	history, err := c.rollbackHistory(ctx, sv.ID)
	if err != nil {
		return RollbackServiceOutput{}, err
	}
	target, current, err := rollbackResolve(history, sc.Service.CurrentDeploymentID, to, sel)
	if err != nil {
		return RollbackServiceOutput{}, err
	}

	out := RollbackServiceOutput{
		Service: sel, ServiceID: sv.ID.String(), DryRun: in.DryRun, Production: sc.IsProduction,
		Disclaimer: deployments.RollbackDisclaimer, Errors: []SectionError{}, NextTools: []NextTool{},
	}
	pr, perr := c.rollbackPreview(ctx, sc, target, current, history, detail.Full())
	out.Preview = pr
	if perr != nil {
		out.Errors = append(out.Errors, sectionError("preview", perr))
	}
	out.Preview.StagedChangesInfo = ""
	if sv.HasStagedChanges {
		out.Preview.StagedChangesInfo = "The service has staged changes; a rollback does not touch them and they stay staged."
	}

	if in.DryRun {
		out.Summary = fmt.Sprintf("Preview: roll %s back to %s (deployment %s). Nothing was started. %s",
			sel, rollbackLabel(target), target.ID, deployments.RollbackDisclaimer)
		args := map[string]any{"service": sel, "to": target.ID.String()}
		why := "Start the rollback."
		if sc.IsProduction {
			why += " Production: a human must approve."
		}
		out.NextTools = append(out.NextTools, NextTool{Tool: "rollback_service", Args: args, Why: why})
		return out, nil
	}

	rv, err := c.Deployments.Rollback(ctx, target.ID)
	if err != nil {
		return RollbackServiceOutput{}, c.rollbackErr(err, sv.ID, target)
	}
	out.Started = true
	out.Disclaimer = rv.Disclaimer
	views := map[uuid.UUID]deployments.DeploymentView{rv.ID: rv.DeploymentView}
	if wait > 0 {
		var errs map[uuid.UUID]error
		views, errs = c.waitDeployments(ctx, views, wait)
		for id, e := range errs {
			out.Errors = append(out.Errors, sectionError("read deployment "+id.String(), e))
		}
	}
	v := views[rv.ID]
	a := appliedFrom(sel, v)
	if wait > 0 {
		tail := 20
		if detail.Full() {
			tail = 50
		}
		out.Errors = append(out.Errors, c.describeOutcome(ctx, p, &a, v, tail)...)
	}
	out.Deployment = &a

	sum := fmt.Sprintf("Rollback of %s to %s started (deployment %s, %s)", sel, rollbackLabel(target), a.DeploymentID, a.Outcome)
	switch {
	case a.Outcome == mcpkit.CodeOutcomeUncertain:
		sum += ". The agent acknowledged it but its server went offline; the outcome is unknown, do not retry"
	case a.Terminal && a.Outcome != string(deployments.Succeeded):
		sum += ". It did not succeed: see statusMessage"
	case !a.Terminal && wait == 0:
		sum += "; it runs in the background"
	}
	out.Summary = sum + ". " + out.Disclaimer
	switch {
	case a.Terminal && a.Outcome != string(deployments.Succeeded):
		out.NextTools = append(out.NextTools, NextTool{Tool: "explain_failed_deployment", Args: map[string]any{"deployment": a.DeploymentID}, Why: "Find why the rollback deployment failed."})
	default:
		out.NextTools = append(out.NextTools, NextTool{Tool: "verify_service", Args: map[string]any{"service": sel}, Why: "Check the rolled back service is healthy."})
	}
	return out, nil
}

// rollbackErr maps a Rollback error to a tool error. The production guard's
// approval request is returned unchanged; image_pruned names the commit to
// rebuild.
func (c *composites) rollbackErr(err error, serviceID uuid.UUID, target deployments.DeploymentView) error {
	if isApproval(err) {
		return err
	}
	me := changesErr(err)
	var te *mcpkit.Error
	if !errors.As(me, &te) || te.Code != "image_pruned" {
		return me
	}
	data, _ := te.Data.(map[string]any)
	if data == nil {
		data = map[string]any{}
	}
	if sha, _ := data["commitSha"].(string); sha != "" {
		data["next"] = map[string]any{
			"tool": "call_operation",
			"args": map[string]any{"operationId": "deployService", "arguments": map[string]any{"serviceId": serviceID.String(), "commitSha": sha}},
			"why":  "Rebuild and deploy that commit instead of rolling back to the pruned image.",
		}
	}
	data["releaseId"] = idPtr(target.ReleaseID)
	te.Data = data
	te.Message += ". Rebuild the commit with call_operation deployService (commitSha in data.next)."
	return te
}

// rollbackHistory returns the service's deployments, newest first (bounded).
func (c *composites) rollbackHistory(ctx context.Context, serviceID uuid.UUID) ([]deployments.DeploymentView, error) {
	var all []deployments.DeploymentView
	cursor := ""
	for range rollbackMaxPages {
		page, next, err := c.Deployments.List(ctx, serviceID, cursor, rollbackPage)
		if err != nil {
			return nil, err
		}
		all = append(all, page...)
		if next == "" {
			break
		}
		cursor = next
	}
	return all, nil
}

// rollbackResolve picks the target deployment and the one running now.
func rollbackResolve(history []deployments.DeploymentView, currentID *uuid.UUID, to, sel string) (target deployments.DeploymentView, current *deployments.DeploymentView, err error) {
	if currentID != nil {
		for i := range history {
			if history[i].ID == *currentID {
				current = &history[i]
				break
			}
		}
	}
	isCurrent := func(v deployments.DeploymentView) bool {
		return currentID != nil && v.ID == *currentID
	}
	sameRelease := func(v deployments.DeploymentView) bool {
		return current != nil && current.ReleaseID != nil && v.ReleaseID != nil && *current.ReleaseID == *v.ReleaseID
	}
	rollbackable := func(v deployments.DeploymentView) bool {
		return v.Status == string(deployments.Succeeded) && v.ReleaseID != nil
	}

	var matches []deployments.DeploymentView
	switch {
	case to == "previous":
		for _, v := range history {
			if rollbackable(v) && !isCurrent(v) && !sameRelease(v) {
				return v, current, nil
			}
		}
		return target, current, rollbackNotFound(history, "no earlier succeeded deployment with another release exists for "+sel, currentID)
	case rollbackShaRe.MatchString(to) && !isUUID(to):
		for _, v := range history {
			sha := ""
			if v.Release != nil {
				sha = v.Release.Source.CommitSHA
			}
			if sha == "" && v.Commit != nil {
				sha = v.Commit.SHA
			}
			if sha != "" && strings.HasPrefix(strings.ToLower(sha), to) {
				matches = append(matches, v)
			}
		}
	case isUUID(to):
		id := uuid.MustParse(to)
		for _, v := range history {
			if v.ID == id || (v.ReleaseID != nil && *v.ReleaseID == id) {
				matches = append(matches, v)
			}
		}
	default:
		return target, current, invalid("to must be previous, a release id, a deployment id or a commit sha prefix of at least 7 hex characters")
	}
	var cur *deployments.DeploymentView
	var notOK *deployments.DeploymentView
	for i, v := range matches {
		switch {
		case rollbackable(v) && isCurrent(v):
			cur = &matches[i]
		case rollbackable(v):
			return v, current, nil
		default:
			if notOK == nil {
				notOK = &matches[i]
			}
		}
	}
	switch {
	case cur != nil:
		return target, current, mcpkit.Errf("already_current", "that deployment is already the running one")
	case notOK != nil:
		return target, current, mcpkit.Errf("not_rollbackable", "only a successful deployment can be rolled back to; %s has status %s", notOK.ID, notOK.Status)
	}
	return target, current, rollbackNotFound(history, fmt.Sprintf("no deployment, release or commit of %s matches %q", sel, to), currentID)
}

func isUUID(s string) bool {
	_, err := uuid.Parse(s)
	return err == nil
}

// rollbackNotFound lists the newest rollback candidates so the model can pick.
func rollbackNotFound(history []deployments.DeploymentView, msg string, currentID *uuid.UUID) error {
	var cands []map[string]any
	for _, v := range history {
		if v.Status != string(deployments.Succeeded) || v.ReleaseID == nil || (currentID != nil && v.ID == *currentID) {
			continue
		}
		cand := map[string]any{"deploymentId": v.ID.String(), "releaseId": v.ReleaseID.String(), "finishedAt": ptrTime(v.FinishedAt)}
		if v.Release != nil {
			cand["image"] = v.Release.ImageRef
			if v.Release.Source.CommitSHA != "" {
				cand["commit"] = v.Release.Source.CommitSHA
			}
		}
		cands = append(cands, cand)
		if len(cands) == 5 {
			break
		}
	}
	e := mcpkit.Errf(mcpkit.CodeNotFound, "%s", msg)
	e.Data = map[string]any{"candidates": cands}
	return e
}

func rollbackLabel(v deployments.DeploymentView) string {
	if v.Release != nil {
		if sha := v.Release.Source.CommitSHA; sha != "" {
			return "commit " + sha[:min(7, len(sha))] + " (" + v.Release.ImageRef + ")"
		}
		return v.Release.ImageRef
	}
	return "release " + idPtr(v.ReleaseID)
}

func rollbackTargetOf(v deployments.DeploymentView) RollbackTarget {
	t := RollbackTarget{DeploymentID: v.ID.String(), ReleaseID: idPtr(v.ReleaseID), FinishedAt: ptrTime(v.FinishedAt)}
	if v.Release != nil {
		t.Image, t.SourceType, t.Commit = v.Release.ImageRef, v.Release.Source.Type, v.Release.Source.CommitSHA
	}
	return t
}

// rollbackPreview describes the rollback. It never decides whether the image
// is still available: Deployments.Rollback does, and its image_pruned error
// is surfaced as is.
func (c *composites) rollbackPreview(ctx context.Context, sc services.Scope, target deployments.DeploymentView, current *deployments.DeploymentView, history []deployments.DeploymentView, full bool) (RollbackPreview, error) {
	pr := RollbackPreview{
		Target: rollbackTargetOf(target), NotRolledBack: deployments.RollbackDisclaimer,
		ConfigChanges: []ChangeEntry{}, VariablesAdded: []string{}, VariablesRemoved: []string{}, VariablesChanged: []string{},
		RollsBack: []string{}, VariablesSnapshot: "unknown",
	}
	for _, h := range history {
		if h.Status != "" && deployments.Status(h.Status).IsActive() {
			pr.DeploymentInProgress = true
			break
		}
	}
	if current != nil {
		t := rollbackTargetOf(*current)
		pr.Current = &t
	}
	if target.Release != nil {
		pr.RollsBack = append(pr.RollsBack, "Application image: "+target.Release.ImageRef)
		src := target.Release.Source
		switch {
		case src.Type == services.SourceGit && src.RegistryRef != "":
			pr.ImageCheck = "Pulled from the registry by digest; the rollback verifies it still exists and answers image_pruned if it does not."
			pr.RebuildCommit = src.CommitSHA
		case src.Type == services.SourceGit:
			pr.ImageCheck = "The built image is kept only for the newest releases (image retention). The rollback verifies it still exists and answers image_pruned (with the commit to rebuild) if it was pruned."
			pr.RebuildCommit = src.CommitSHA
		default:
			pr.ImageCheck = "Pulled from " + target.Release.ImageRef + " when the rollback runs."
		}
	}
	pr.RollsBack = append(pr.RollsBack, "Service configuration of that release (ports, health check, commands, resources)", "The variables frozen into that release")

	var firstErr error
	note := func(err error) {
		if firstErr == nil {
			firstErr = err
		}
	}
	// Config and variable keys: target release against the running one.
	tgtRel, err := c.q().GetReleaseByID(ctx, *target.ReleaseID)
	if err != nil {
		note(fmt.Errorf("get release: %w", err))
		return pr, firstErr
	}
	tgtSnap := services.DecodeSnapshot(tgtRel.Config)
	vols, verr := c.Services.ListVolumes(ctx, sc.Service.ID)
	if verr != nil {
		note(verr)
	}
	strategy, reasons := deployments.ChooseStrategy(tgtSnap.Config, len(vols))
	pr.Strategy, pr.Downtime = strategy, downtimeNote(strategy, reasons)

	var curRel *db.Release
	if current != nil && current.ReleaseID != nil {
		if r, err := c.q().GetReleaseByID(ctx, *current.ReleaseID); err == nil {
			curRel = &r
		} else {
			note(fmt.Errorf("get release: %w", err))
		}
	}
	if curRel != nil {
		limit, valClip := reviewChangesConcise, reviewValueClipConcise
		if full {
			limit, valClip = reviewChangesFull, reviewValueClipFull
		}
		diff := services.DiffConfig("config", services.DecodeSnapshot(curRel.Config).Config, tgtSnap.Config)
		pr.ConfigChangesCount = len(diff)
		for i, d := range diff {
			if i == limit {
				break
			}
			pr.ConfigChanges = append(pr.ConfigChanges, changeEntryOf(d, valClip))
		}
	}

	if len(tgtRel.EnvEnc) == 0 {
		pr.VariablesSnapshot = "pruned: the variable values of this release were removed after the rollback window; the current variables are used"
		return pr, firstErr
	}
	box := c.Deployments.Box
	if box == nil {
		return pr, firstErr
	}
	tgtRaw, err := services.ReleaseRaw(box, tgtRel)
	if err != nil {
		note(err)
		return pr, firstErr
	}
	pr.VariablesSnapshot = fmt.Sprintf("kept: %s", plural(len(tgtRaw), "variable", "variables"))
	if curRel == nil {
		return pr, firstErr
	}
	curRaw, err := services.ReleaseRaw(box, *curRel)
	if err != nil {
		note(err)
		return pr, firstErr
	}
	for k, tv := range tgtRaw {
		cv, ok := curRaw[k]
		switch {
		case !ok:
			pr.VariablesAdded = append(pr.VariablesAdded, k)
		case cv.Value != tv.Value || cv.Secret != tv.Secret:
			pr.VariablesChanged = append(pr.VariablesChanged, k)
		}
	}
	for k := range curRaw {
		if _, ok := tgtRaw[k]; !ok {
			pr.VariablesRemoved = append(pr.VariablesRemoved, k)
		}
	}
	sort.Strings(pr.VariablesAdded)
	sort.Strings(pr.VariablesRemoved)
	sort.Strings(pr.VariablesChanged)
	return pr, firstErr
}
