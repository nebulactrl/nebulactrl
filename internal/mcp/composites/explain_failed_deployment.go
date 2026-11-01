package composites

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/deployments"
	"github.com/nebulactrl/nebulactrl/internal/mcp/mcpkit"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// explain_failed_deployment: why did this deployment fail, and what is the
// smallest fix. It reads the timeline, the persisted build/deploy/runtime/debug
// log, the release and its diff against the last good release, whether a
// rollback target still exists and what is serving now, then classifies the
// cause with stable rules. It never changes anything.

const (
	explainLogPage      = 5000
	explainLogPages     = 3
	explainListPage     = 50
	explainListPages    = 4
	explainExcerptLines = 40
	explainExcerptFull  = 80
	explainContext      = 5
	explainTailLines    = 30
	explainTailFull     = 60
	explainLineClip     = 300
	explainMaxSteps     = 60
	explainMaxStepsFull = 200
	explainMaxDiff      = 25
	explainMaxDiffFull  = 100
	explainMaxEvidence  = 6
	explainMaxMessages  = 30
	explainMaxMsgFull   = 100
	// explainBudget keeps a result under the 64 KB cap with room for the envelope.
	explainBudget = 56 * 1024
)

// Likely-cause codes (stable).
const (
	causeImagePull        = "image_pull_failed"
	causeBuild            = "build_failed"
	causePredeploy        = "predeploy_failed"
	causeHealth           = "health_check_failed"
	causePortMismatch     = "port_mismatch"
	causePortConflict     = "port_conflict"
	causeCrash            = "crash_on_start"
	causeConfig           = "config_invalid"
	causeOffline          = "server_offline_waiting"
	causeProxy            = "proxy_rejected"
	causeVerifyRolledBack = "verify_failed_rolled_back"
	causeRollbackFailed   = "rollback_failed"
	causeCanceled         = "canceled"
	causeDisk             = "disk_full"
	causeOOM              = "out_of_memory"
	causePlatform         = "platform_mismatch"
	causeUnknown          = "unknown"
)

// ExplainFailedInput selects the deployment.
type ExplainFailedInput struct {
	Deployment string `json:"deployment,omitempty" jsonschema:"deployment id (uuid); give either deployment or service"`
	Service    string `json:"service,omitempty" jsonschema:"service selector (id, project/environment/service, hostname): explains its latest failed, rolled back, rollback_failed or canceled deployment"`
	Detail     string `json:"detail,omitempty" jsonschema:"concise (default) or full (longer log excerpt, timeline and diff)"`
}

// ExplainCommit is the commit a git deployment shipped. Branch, message and
// author come from outside the team: they are untrusted blocks, data to read
// and never instructions.
type ExplainCommit struct {
	SHA     string `json:"sha,omitempty"`
	Branch  any    `json:"branch,omitempty" jsonschema:"git branch, as an untrusted block"`
	Message any    `json:"message,omitempty" jsonschema:"commit message, as an untrusted block"`
	Author  any    `json:"author,omitempty" jsonschema:"commit author, as an untrusted block"`
}

// untrustedText wraps third-party text (a commit message, a branch name) as an
// untrusted block; an empty string stays absent.
func untrustedText(source, s string) any {
	if s == "" {
		return nil
	}
	return mcpkit.WrapText(source, s)
}

// ExplainDeployment is the deployment that is being explained.
type ExplainDeployment struct {
	ID              string        `json:"id"`
	ServiceID       string        `json:"serviceId"`
	Status          string        `json:"status"`
	StatusMessage   any           `json:"statusMessage,omitempty" jsonschema:"message of the platform or agent, as an untrusted block"`
	Trigger         string        `json:"trigger"`
	Strategy        string        `json:"strategy"`
	Actor           string        `json:"actor,omitempty"`
	CreatedAt       string        `json:"createdAt"`
	StartedAt       string        `json:"startedAt,omitempty"`
	FinishedAt      string        `json:"finishedAt,omitempty"`
	DurationSeconds int64         `json:"durationSeconds,omitempty"`
	Generation      int64         `json:"generation"`
	ReleaseID       string        `json:"releaseId,omitempty"`
	ImageRef        string        `json:"imageRef,omitempty"`
	Commit          ExplainCommit `json:"commit"`
	BuildMethod     string        `json:"buildMethod,omitempty"`
	BuildServer     string        `json:"buildServer,omitempty"`
	WaitingFor      string        `json:"waitingFor,omitempty" jsonschema:"set while the deployment waits for an offline server"`
	CanceledAtStep  string        `json:"canceledAtStep,omitempty"`
	RolledBackFrom  string        `json:"rolledBackFrom,omitempty"`
}

// ExplainStep is one entry of the deployment timeline.
type ExplainStep struct {
	Step        string `json:"step"`
	Status      string `json:"status"`
	At          string `json:"at"`
	SinceStart  int64  `json:"sinceStartMs"`
	SincePrevMs int64  `json:"sincePrevMs"`
	// DurationMs is how long the step took (set on its succeeded/failed event).
	DurationMs int64 `json:"durationMs,omitempty"`
	Failing    bool  `json:"failing,omitempty" jsonschema:"true on the first failed step"`
}

// ExplainChange is one field that differs from the last good release.
type ExplainChange struct {
	Field string `json:"field"`
	From  string `json:"from"`
	To    string `json:"to"`
}

// ExplainVarChange is a variable whose flags differ between the releases.
type ExplainVarChange struct {
	Key    string `json:"key"`
	Change string `json:"change"`
}

// ExplainCommits is the commit range between the last good and the failed release.
type ExplainCommits struct {
	From      string `json:"from,omitempty"`
	To        string `json:"to,omitempty"`
	Branch    any    `json:"branch,omitempty" jsonschema:"git branch, as an untrusted block"`
	ToMessage any    `json:"toMessage,omitempty" jsonschema:"commit message of the failed release, as an untrusted block"`
}

// ExplainDiff is the release diff against the last good release.
type ExplainDiff struct {
	Compared           bool               `json:"compared" jsonschema:"false when there is no release or no earlier good release to compare with"`
	Note               string             `json:"note,omitempty"`
	BaselineDeployment string             `json:"baselineDeployment,omitempty" jsonschema:"the last succeeded deployment before this one"`
	BaselineRelease    string             `json:"baselineRelease,omitempty"`
	Changes            []ExplainChange    `json:"changes" jsonschema:"changed fields, secrets masked"`
	ChangesTruncated   bool               `json:"changesTruncated,omitempty"`
	Commits            ExplainCommits     `json:"commits"`
	VariablesAdded     []string           `json:"variablesAdded"`
	VariablesRemoved   []string           `json:"variablesRemoved"`
	VariablesChanged   []ExplainVarChange `json:"variablesChanged"`
	VariablesNote      string             `json:"variablesNote,omitempty"`
}

// ExplainServing says what is live now.
type ExplainServing struct {
	DeploymentID string `json:"deploymentId,omitempty"`
	ReleaseID    string `json:"releaseId,omitempty"`
	ImageRef     string `json:"imageRef,omitempty"`
	CommitSHA    string `json:"commitSha,omitempty"`
	State        string `json:"state,omitempty"`
	Health       string `json:"health,omitempty"`
	Serving      bool   `json:"serving" jsonschema:"a container of the service is running and not unhealthy"`
	// PreviousReleaseServing is true when the live release is not the one
	// this deployment tried to ship (a failed rolling deploy leaves it up).
	PreviousReleaseServing bool   `json:"previousReleaseServing"`
	Note                   string `json:"note,omitempty"`
	// ProcessesNotReady names each process of the live release that lacks ready replicas.
	ProcessesNotReady []string `json:"processesNotReady,omitempty"`
}

// ExplainRollbackTarget says whether rolling back to the last good release
// would be accepted (nothing is rolled back by this tool).
type ExplainRollbackTarget struct {
	Available       bool   `json:"available"`
	Code            string `json:"code,omitempty" jsonschema:"image_pruned, already_current or no_good_release when not available"`
	Reason          string `json:"reason,omitempty"`
	DeploymentID    string `json:"deploymentId,omitempty"`
	ReleaseID       string `json:"releaseId,omitempty"`
	ImageRef        string `json:"imageRef,omitempty"`
	CommitSHA       string `json:"commitSha,omitempty"`
	VariablesPruned bool   `json:"variablesPruned,omitempty" jsonschema:"the release's variable values were dropped after the rollback window"`
	Disclaimer      string `json:"disclaimer,omitempty"`
}

// ExplainFailedOutput is the explain_failed_deployment result.
type ExplainFailedOutput struct {
	Envelope
	Failed     bool              `json:"failed" jsonschema:"false when the deployment did not fail (its state is returned)"`
	Service    string            `json:"service,omitempty" jsonschema:"project/environment/service"`
	Deployment ExplainDeployment `json:"deployment"`
	FailedStep string            `json:"failedStep,omitempty" jsonschema:"first failing deploy step (pull, build, predeploy, create, start, health, route, verify, ...)"`
	// FailedProcess is the process a multi-process deployment failed in (read
	// from the [process] prefix of the failure message); processes after it were not started.
	FailedProcess string        `json:"failedProcess,omitempty"`
	Timeline      []ExplainStep `json:"timeline"`
	// EventMessages are the messages of the timeline events and the status message (untrusted text).
	EventMessages mcpkit.Block `json:"eventMessages"`
	// LogExcerpt is about 40 lines around the first error line (5 lines of
	// context before), else the last 30 lines; build, deploy, runtime and
	// debug streams as persisted.
	LogExcerpt       mcpkit.Block          `json:"logExcerpt"`
	LogExcerptAnchor string                `json:"logExcerptAnchor" jsonschema:"first_error, last_lines, none or unavailable"`
	LogLinesRead     int                   `json:"logLinesRead"`
	ReleaseDiff      ExplainDiff           `json:"releaseDiff"`
	ServingNow       ExplainServing        `json:"servingNow"`
	RollbackTarget   ExplainRollbackTarget `json:"rollbackTarget"`
	LikelyCause      string                `json:"likelyCause" jsonschema:"stable code: image_pull_failed, build_failed, predeploy_failed, health_check_failed, port_mismatch, port_conflict, crash_on_start, config_invalid, server_offline_waiting, proxy_rejected, verify_failed_rolled_back, rollback_failed, canceled, disk_full, out_of_memory, platform_mismatch or unknown"`
	// UnderlyingCause is the cause behind a rolled-back deployment.
	UnderlyingCause string `json:"underlyingCause,omitempty"`
	SmallestFix     string `json:"smallestFix" jsonschema:"one concrete sentence"`
	// CauseEvidence holds the lines that led to the classification (untrusted text).
	CauseEvidence mcpkit.Block `json:"causeEvidence"`
}

func (c *composites) explainFailedDeployment() mcpkit.Tool {
	return mcpkit.Tool{
		Name:  "explain_failed_deployment",
		Title: "Explain a failed deployment",
		Description: "Use when a deployment failed, was rolled back or canceled, to learn why and what the smallest fix is. Give a deployment id, or a service selector to take its latest failed deployment. " +
			"Returns: the failing step, the event timeline with durations, a log excerpt around the FIRST error (build, deploy, runtime and the engine's debug stream), the release diff against the last good release (changed config fields with secrets masked, variable KEYS added/removed/flag-changed, commit range, image, strategy), " +
			"whether a rollback target still exists (image_pruned), what is serving now (a failed rolling deploy leaves the previous release up), a rule-based likelyCause code and a one-sentence smallestFix. " +
			"If the deployment did not fail it says so and returns its state. Log text needs logs:read; without it the explanation uses the timeline and status only. " +
			"Never shows variable values and changes nothing. Next: stage_changes + apply_changes to fix, rollback_service to go back, tail_logs for more lines.",
		Toolset: mcpkit.ToolsetCore,
		Scopes:  []string{policy.ScopeProjectsRead},
		Hints:   mcpkit.Hints{ReadOnly: true, Destructive: false, Idempotent: true, OpenWorld: true},
		Input:   ExplainFailedInput{},
		Output:  ExplainFailedOutput{},
		Call: func(ctx context.Context, p *auth.Principal, raw json.RawMessage) (any, error) {
			in, err := mcpkit.Decode[ExplainFailedInput](raw)
			if err != nil {
				return nil, err
			}
			return c.explainCall(ctx, p, in)
		},
	}
}

// explainFailureStatus reports whether a terminal status is one this tool explains.
func explainFailureStatus(s string) bool {
	switch s {
	case "failed", "rolled_back", "rollback_failed", "canceled":
		return true
	}
	return false
}

func (c *composites) explainCall(ctx context.Context, p *auth.Principal, in ExplainFailedInput) (ExplainFailedOutput, error) {
	detail, err := mcpkit.ParseDetail(in.Detail)
	if err != nil {
		return ExplainFailedOutput{}, err
	}
	hasDep, hasSvc := strings.TrimSpace(in.Deployment) != "", strings.TrimSpace(in.Service) != ""
	if hasDep == hasSvc {
		return ExplainFailedOutput{}, invalid("give exactly one of deployment (uuid) or service (selector)")
	}

	var dep deployments.DeploymentView
	var view services.ServiceView
	if hasDep {
		id, err := parseUUID("deployment", in.Deployment)
		if err != nil {
			return ExplainFailedOutput{}, err
		}
		if dep, err = c.Deployments.Get(ctx, id); err != nil {
			return ExplainFailedOutput{}, err
		}
		if view, err = c.Services.Get(ctx, dep.ServiceID); err != nil {
			return ExplainFailedOutput{}, err
		}
	} else {
		if view, err = c.Resolver.Service(ctx, in.Service); err != nil {
			return ExplainFailedOutput{}, err
		}
	}
	sc, err := c.scope(ctx, view.ID)
	if err != nil {
		return ExplainFailedOutput{}, err
	}
	sel := selectorOf(sc)

	out := ExplainFailedOutput{
		Service: sel, Timeline: []ExplainStep{}, LikelyCause: causeUnknown,
		ReleaseDiff:   ExplainDiff{Changes: []ExplainChange{}, VariablesAdded: []string{}, VariablesRemoved: []string{}, VariablesChanged: []ExplainVarChange{}},
		EventMessages: mcpkit.WrapLines("deployment-events", []string{}),
		LogExcerpt:    mcpkit.WrapLines("deployment-log", []string{}), LogExcerptAnchor: "none",
		CauseEvidence: mcpkit.WrapLines("deployment-evidence", []string{}),
	}
	out.Errors, out.NextTools = []SectionError{}, []NextTool{}

	if hasSvc {
		latest, found, err := c.explainLatestFailed(ctx, view.ID)
		if err != nil {
			return ExplainFailedOutput{}, err
		}
		if latest == nil {
			out.Summary = fmt.Sprintf("%s has no deployments yet, so there is nothing to explain.", sel)
			out.ServingNow = explainServing(view, uuid.Nil)
			out.NextTools = []NextTool{{Tool: "diagnose_service", Args: map[string]any{"service": sel}, Why: "the service was never deployed; check what is missing"}}
			return out, nil
		}
		dep = *latest
		if !found {
			out.Deployment = explainDeployment(dep)
			out.ServingNow = explainServing(view, dep.ID)
			out.Summary = fmt.Sprintf("%s has no failed, rolled back or canceled deployment among its last %d; the latest (%s) is %s.", sel, explainListPage*explainListPages, shortID(dep.ID), dep.Status)
			out.NextTools = explainHealthyNext(sel, dep)
			return out, nil
		}
	}

	out.Deployment = explainDeployment(dep)
	if !explainFailureStatus(dep.Status) {
		return c.explainNotFailed(ctx, out, dep, view, sel), nil
	}
	out.Failed = true
	return c.explainFailure(ctx, p, out, dep, view, sel, detail)
}

// explainLatestFailed returns the newest failed-like deployment of a service;
// when there is none it returns the newest deployment (found=false), or nil
// for a service without deployments.
func (c *composites) explainLatestFailed(ctx context.Context, serviceID uuid.UUID) (*deployments.DeploymentView, bool, error) {
	var newest *deployments.DeploymentView
	cursor := ""
	for range explainListPages {
		page, next, err := c.Deployments.List(ctx, serviceID, cursor, explainListPage)
		if err != nil {
			return nil, false, err
		}
		for i := range page {
			if newest == nil {
				newest = &page[i]
			}
			if explainFailureStatus(page[i].Status) {
				return &page[i], true, nil
			}
		}
		if next == "" {
			break
		}
		cursor = next
	}
	return newest, false, nil
}

// explainNotFailed answers for a deployment that did not fail.
func (c *composites) explainNotFailed(ctx context.Context, out ExplainFailedOutput, dep deployments.DeploymentView, view services.ServiceView, sel string) ExplainFailedOutput {
	run := c.newPartial(ctx, "explain_failed_deployment", 2)
	var evs []deployments.EventView
	run.run("events", func(ctx context.Context) error {
		var err error
		evs, err = c.Deployments.Events(ctx, dep.ID)
		return err
	})
	out.Errors = run.wait()
	out.Timeline, _ = explainTimeline(evs, false)
	out.FailedStep = ""
	out.ServingNow = explainServing(view, dep.ID)
	switch {
	case dep.Status == "succeeded":
		out.Summary = fmt.Sprintf("Deployment %s of %s did not fail: it succeeded.", shortID(dep.ID), sel)
	case deployments.Status(dep.Status).IsActive():
		step := explainOpenStep(evs)
		at := ""
		if step != "" {
			at = " at step " + step
		}
		out.Summary = fmt.Sprintf("Deployment %s of %s has not failed: it is still %s%s.", shortID(dep.ID), sel, dep.Status, at)
	default:
		out.Summary = fmt.Sprintf("Deployment %s of %s did not fail (status %s).", shortID(dep.ID), sel, dep.Status)
	}
	out.NextTools = explainHealthyNext(sel, dep)
	return out
}

func explainHealthyNext(sel string, dep deployments.DeploymentView) []NextTool {
	if deployments.Status(dep.Status).IsActive() {
		return []NextTool{{Tool: "verify_service", Args: map[string]any{"service": sel}, Why: "check the service once the deployment ends"}}
	}
	return []NextTool{
		{Tool: "verify_service", Args: map[string]any{"service": sel}, Why: "confirm the service is actually serving"},
		{Tool: "diagnose_service", Args: map[string]any{"service": sel}, Why: "the deployment is fine; look at the service itself if it misbehaves"},
	}
}

// explainFailure gathers everything about a failed deployment and classifies it.
func (c *composites) explainFailure(ctx context.Context, p *auth.Principal, out ExplainFailedOutput, dep deployments.DeploymentView, view services.ServiceView, sel string, detail mcpkit.Detail) (ExplainFailedOutput, error) {
	full := detail.Full()
	run := c.newPartial(ctx, "explain_failed_deployment", 4)

	var evs []deployments.EventView
	run.run("events", func(ctx context.Context) error {
		var err error
		evs, err = c.Deployments.Events(ctx, dep.ID)
		return err
	})

	var lines []deployments.LogLineView
	logsOK := holds(p, policy.ScopeLogsRead)
	if logsOK {
		run.run("logs", func(ctx context.Context) error {
			var err error
			lines, err = c.explainLogs(ctx, dep.ID)
			if err != nil {
				logsOK = false
			}
			return err
		})
	} else {
		run.forbidden("logs", "log excerpt skipped: needs logs:read; explained from the timeline and status only")
	}

	var rel *deployments.ReleaseDetailView
	if dep.ReleaseID != nil {
		run.run("release", func(ctx context.Context) error {
			r, err := c.Deployments.ReleaseDetail(ctx, dep.ID)
			if err != nil {
				return err
			}
			rel = &r
			return nil
		})
	}

	var good *deployments.DeploymentView
	var goodRel *deployments.ReleaseDetailView
	run.run("baseline", func(ctx context.Context) error {
		g, err := c.explainLastGood(ctx, dep)
		if err != nil || g == nil {
			return err
		}
		good = g
		r, err := c.Deployments.ReleaseDetail(ctx, g.ID)
		if err != nil {
			return err
		}
		goodRel = &r
		return nil
	})
	errs := run.wait()

	// Timeline and failing step.
	steps, msgs := explainTimeline(evs, full)
	out.Timeline = steps
	failedStep := explainFailedStep(evs, dep)
	out.FailedStep = failedStep
	out.FailedProcess = explainFailedProcess(evs, dep)
	msgLines := msgs
	if dep.StatusMessage != "" {
		msgLines = append([]string{"deployment status message: " + clip(dep.StatusMessage, explainLineClip)}, msgLines...)
	}
	limit := explainMaxMessages
	if full {
		limit = explainMaxMsgFull
	}
	if len(msgLines) > limit {
		// Keep the status message and the newest events: failures come last.
		head := 0
		if dep.StatusMessage != "" {
			head = 1
		}
		msgLines = append(slices.Clone(msgLines[:head]), msgLines[len(msgLines)-(limit-head):]...)
	}
	srcID := shortID(dep.ID)
	out.EventMessages = mcpkit.WrapLines("deployment-events:"+srcID, msgLines)

	// Log excerpt.
	var excerptLines []string
	out.LogLinesRead = len(lines)
	switch {
	case !logsOK:
		out.LogExcerptAnchor = "unavailable"
	case len(lines) == 0:
		out.LogExcerptAnchor = "none"
	default:
		excerptLines, out.LogExcerptAnchor = explainExcerpt(lines, full)
		out.LogExcerpt = mcpkit.WrapLines("deployment-log:"+srcID, excerptLines)
	}

	// Release diff, rollback target, what is serving.
	out.ReleaseDiff = explainReleaseDiff(dep, rel, good, goodRel, full)
	out.ServingNow = explainServing(view, dep.ID)
	rt, rtErr := c.explainRollbackTarget(ctx, view, good, goodRel)
	if rtErr != nil {
		errs = append(errs, sectionError("rollbackTarget", rtErr))
		sort.SliceStable(errs, func(i, j int) bool { return errs[i].Section < errs[j].Section })
	}
	out.RollbackTarget = rt
	out.Errors = errs

	// Classification.
	facts := explainFactsOf(dep, view, failedStep, evs, lines)
	cls := explainClassify(facts)
	out.LikelyCause, out.UnderlyingCause, out.SmallestFix = cls.code, cls.underlying, cls.fix
	ev := cls.evidence
	if len(ev) > explainMaxEvidence {
		ev = ev[:explainMaxEvidence]
	}
	out.CauseEvidence = mcpkit.WrapLines("deployment-evidence:"+shortID(dep.ID), ev)

	out.Summary = explainSummary(sel, dep, out)
	out.NextTools = explainNextTools(sel, view.Generation, dep, out, holds(p, policy.ScopeLogsRead))

	// Stay well under the 64 KB result cap: shrink the biggest parts in turn.
	for i := 0; mcpkit.JSONSize(out) > explainBudget && i < 16; i++ {
		switch i % 4 {
		case 0:
			if len(excerptLines) > 12 {
				excerptLines = excerptLines[:len(excerptLines)/2]
				out.LogExcerpt = mcpkit.WrapLines("deployment-log:"+srcID, excerptLines)
			}
		case 1:
			if len(msgLines) > 6 {
				msgLines = append(slices.Clone(msgLines[:1]), msgLines[len(msgLines)-(len(msgLines)/2-1):]...)
				out.EventMessages = mcpkit.WrapLines("deployment-events:"+srcID, msgLines)
			}
		case 2:
			if n := len(out.ReleaseDiff.Changes); n > 6 {
				out.ReleaseDiff.Changes, out.ReleaseDiff.ChangesTruncated = out.ReleaseDiff.Changes[:n/2], true
			}
		case 3:
			if n := len(out.Timeline); n > 30 {
				out.Timeline = append(slices.Clone(out.Timeline[:10]), out.Timeline[n-20:]...)
			}
		}
	}
	return out, nil
}

func shortID(id uuid.UUID) string {
	s := id.String()
	return s[:8]
}

// explainLogs reads the persisted lines of every stream (build, deploy,
// runtime, debug) in order, up to explainLogPages pages.
func (c *composites) explainLogs(ctx context.Context, id uuid.UUID) ([]deployments.LogLineView, error) {
	var all []deployments.LogLineView
	var after int64
	for range explainLogPages {
		page, err := c.Deployments.LogLines(ctx, id, after, explainLogPage, deployments.AllLogStreams)
		if err != nil {
			return all, err
		}
		all = append(all, page...)
		if len(page) < explainLogPage {
			break
		}
		after = page[len(page)-1].ID
	}
	return all, nil
}

// explainLastGood finds the newest succeeded deployment of the service that
// was created before dep and has a release.
func (c *composites) explainLastGood(ctx context.Context, dep deployments.DeploymentView) (*deployments.DeploymentView, error) {
	cursor := ""
	for range explainListPages {
		page, next, err := c.Deployments.List(ctx, dep.ServiceID, cursor, explainListPage)
		if err != nil {
			return nil, err
		}
		for i := range page {
			d := page[i]
			if d.ID != dep.ID && d.Status == string(deployments.Succeeded) && d.ReleaseID != nil && d.CreatedAt.Before(dep.CreatedAt) {
				return &d, nil
			}
		}
		if next == "" {
			break
		}
		cursor = next
	}
	return nil, nil
}

func explainDeployment(d deployments.DeploymentView) ExplainDeployment {
	e := ExplainDeployment{
		ID: d.ID.String(), ServiceID: d.ServiceID.String(), Status: d.Status, StatusMessage: untrustedText("deployment.statusMessage", clip(d.StatusMessage, explainLineClip)),
		Trigger: d.Trigger, Strategy: d.Strategy, Actor: d.ActorLabel, CreatedAt: utc(d.CreatedAt), StartedAt: ptrTime(d.StartedAt),
		FinishedAt: ptrTime(d.FinishedAt), Generation: d.Generation, ReleaseID: idPtr(d.ReleaseID), CanceledAtStep: d.CanceledAtStep,
		RolledBackFrom: idPtr(d.RolledBackFrom), BuildServer: d.BuildServerName,
	}
	if d.FinishedAt != nil {
		start := d.CreatedAt
		if d.StartedAt != nil {
			start = *d.StartedAt
		}
		e.DurationSeconds = int64(d.FinishedAt.Sub(start).Seconds())
	}
	if d.Release != nil {
		e.ImageRef = clip(d.Release.ImageRef, 200)
	}
	if d.Commit != nil {
		e.Commit = ExplainCommit{
			SHA: d.Commit.SHA, Branch: untrustedText("commit.branch", d.Commit.Branch),
			Message: untrustedText("commit.message", clip(d.Commit.Message, 160)), Author: untrustedText("commit.author", d.Commit.Author),
		}
	} else if d.Release != nil && d.Release.Source.CommitSHA != "" {
		e.Commit = ExplainCommit{SHA: d.Release.Source.CommitSHA, Branch: untrustedText("commit.branch", d.Release.Source.Branch)}
	}
	if d.Build != nil {
		e.BuildMethod = d.Build.Method
	}
	if d.WaitingFor != nil {
		e.WaitingFor = d.WaitingFor.ServerName
		if d.WaitingFor.OfflineSince != nil {
			e.WaitingFor += " (offline since " + utc(*d.WaitingFor.OfflineSince) + ")"
		}
	}
	return e
}

// ---- timeline ----

// explainTimeline renders the events with offsets and step durations, plus the
// non-empty event messages as text lines (untrusted).
func explainTimeline(evs []deployments.EventView, full bool) ([]ExplainStep, []string) {
	out := make([]ExplainStep, 0, len(evs))
	var msgs []string
	if len(evs) == 0 {
		return out, msgs
	}
	first := evs[0].CreatedAt
	started := map[string]time.Time{}
	prev := first
	failedSeen := false
	for _, e := range evs {
		st := ExplainStep{Step: e.Step, Status: e.Status, At: utc(e.CreatedAt), SinceStart: e.CreatedAt.Sub(first).Milliseconds(), SincePrevMs: e.CreatedAt.Sub(prev).Milliseconds()}
		switch e.Status {
		case agentprotocol.StepStarted:
			started[e.Step] = e.CreatedAt
		case agentprotocol.StepSucceeded, agentprotocol.StepFailed:
			if t, ok := started[e.Step]; ok {
				st.DurationMs = e.CreatedAt.Sub(t).Milliseconds()
			}
		}
		if e.Status == agentprotocol.StepFailed && !failedSeen {
			st.Failing, failedSeen = true, true
		}
		prev = e.CreatedAt
		out = append(out, st)
		if m := strings.TrimSpace(e.Message); m != "" {
			msgs = append(msgs, fmt.Sprintf("%s %s: %s", e.Step, e.Status, clip(m, explainLineClip)))
		}
	}
	limit := explainMaxSteps
	if full {
		limit = explainMaxStepsFull
	}
	if len(out) > limit {
		keep := 10
		out = append(slices.Clone(out[:keep]), out[len(out)-(limit-keep):]...)
	}
	return out, msgs
}

// explainFailedStep is the first failed step event; else the step a canceled
// deployment stopped in; else the step still open when the deployment ended.
func explainFailedStep(evs []deployments.EventView, dep deployments.DeploymentView) string {
	for _, e := range evs {
		if e.Status == agentprotocol.StepFailed {
			return e.Step
		}
	}
	if dep.CanceledAtStep != "" {
		return dep.CanceledAtStep
	}
	return explainOpenStep(evs)
}

// explainProcessPrefix matches the "[worker] " prefix the agent puts on every
// message of a deploy that rolls several processes.
var explainProcessPrefix = regexp.MustCompile(`^\[([a-z0-9](?:[a-z0-9-]{0,29}[a-z0-9])?)\] `)

// explainFailedProcess names the process a multi-process deployment failed in:
// the prefix of the first failed event, else of the status message. "" for a
// single-process service or when no message names one.
func explainFailedProcess(evs []deployments.EventView, dep deployments.DeploymentView) string {
	for _, e := range evs {
		if e.Status != agentprotocol.StepFailed {
			continue
		}
		if m := explainProcessPrefix.FindStringSubmatch(strings.TrimSpace(e.Message)); m != nil {
			return m[1]
		}
	}
	if m := explainProcessPrefix.FindStringSubmatch(strings.TrimSpace(dep.StatusMessage)); m != nil {
		return m[1]
	}
	return ""
}

// explainOpenStep is the newest step that started and has not ended.
func explainOpenStep(evs []deployments.EventView) string {
	open := ""
	for _, e := range evs {
		switch e.Status {
		case agentprotocol.StepStarted:
			open = e.Step
		case agentprotocol.StepSucceeded, agentprotocol.StepFailed:
			if open == e.Step {
				open = ""
			}
		}
	}
	return open
}

// ---- log excerpt ----

type explainLine struct {
	stream, text string
}

func (l explainLine) String() string {
	return "[" + l.stream + "] " + clip(l.text, explainLineClip)
}

// explainExcerpt picks about 40 lines around the first error line (5 lines of
// context before it), else the last 30. The debug stream (the engine's verbose
// trace) is used only when nothing else was logged.
func explainExcerpt(lines []deployments.LogLineView, full bool) ([]string, string) {
	pick := make([]explainLine, 0, len(lines))
	for _, l := range lines {
		if l.Stream != agentprotocol.StreamDebug {
			pick = append(pick, explainLine{l.Stream, l.Line})
		}
	}
	if len(pick) == 0 {
		for _, l := range lines {
			pick = append(pick, explainLine{l.Stream, l.Line})
		}
	}
	span, tail := explainExcerptLines, explainTailLines
	if full {
		span, tail = explainExcerptFull, explainTailFull
	}
	for i, l := range pick {
		if !errorLine(l.text) {
			continue
		}
		start := max(0, i-explainContext)
		end := min(len(pick), start+span)
		return explainStrings(pick[start:end]), "first_error"
	}
	start := max(0, len(pick)-tail)
	return explainStrings(pick[start:]), "last_lines"
}

func explainStrings(ls []explainLine) []string {
	out := make([]string, len(ls))
	for i, l := range ls {
		out[i] = l.String()
	}
	return out
}

// ---- release diff ----

var explainSecretField = regexp.MustCompile(`(?i)(password|passwd|secret|token|credential|api_?key|authorization|private|bearer)`)

// explainReleaseDiff compares the failed release with the last good one.
// Config comes from the services' masked views; variables are keys only.
func explainReleaseDiff(dep deployments.DeploymentView, to *deployments.ReleaseDetailView, goodDep *deployments.DeploymentView, from *deployments.ReleaseDetailView, full bool) ExplainDiff {
	d := ExplainDiff{Changes: []ExplainChange{}, VariablesAdded: []string{}, VariablesRemoved: []string{}, VariablesChanged: []ExplainVarChange{}}
	switch {
	case dep.ReleaseID == nil || to == nil:
		d.Note = "this deployment never got a release (it failed before the release was created), so there is nothing to diff"
		return d
	case goodDep == nil || from == nil:
		d.Note = "no earlier succeeded deployment to compare with: this was the service's first release attempt, or its history is older than the last 200 deployments"
		return d
	}
	d.Compared = true
	d.BaselineDeployment, d.BaselineRelease = goodDep.ID.String(), from.ID.String()

	var ch []ExplainChange
	add := func(field, a, b string) {
		if a != b {
			ch = append(ch, ExplainChange{Field: field, From: clip(a, 160), To: clip(b, 160)})
		}
	}
	add("source.type", from.Source.Type, to.Source.Type)
	add("image", from.ImageRef, to.ImageRef)
	add("source.image", from.Source.Image, to.Source.Image)
	add("source.digest", firstNonEmpty(from.Source.RegistryRef, from.Source.Digest), firstNonEmpty(to.Source.RegistryRef, to.Source.Digest))
	add("source.branch", from.Source.Branch, to.Source.Branch)
	add("source.commit", from.Source.CommitSHA, to.Source.CommitSHA)
	add("source.platform", from.Source.Platform, to.Source.Platform)
	add("strategy", from.Placement.Strategy, to.Placement.Strategy)
	add("server", from.Placement.TargetServerName, to.Placement.TargetServerName)
	add("buildMethod", explainBuildMethod(from), explainBuildMethod(to))
	ch = append(ch, explainConfigDiff(from.Config, to.Config)...)

	limit := explainMaxDiff
	if full {
		limit = explainMaxDiffFull
	}
	if len(ch) > limit {
		ch, d.ChangesTruncated = ch[:limit], true
	}
	d.Changes = append(d.Changes, ch...)

	d.Commits = ExplainCommits{From: from.Source.CommitSHA, To: to.Source.CommitSHA, Branch: untrustedText("commit.branch", to.Source.Branch)}
	if dep.Commit != nil {
		d.Commits.ToMessage = untrustedText("commit.message", clip(dep.Commit.Message, 160))
	}
	if d.Commits.From == d.Commits.To {
		d.Commits.From, d.Commits.To = "", ""
	}

	switch {
	case from.VariablesPruned || to.VariablesPruned:
		which := "the last good release"
		if to.VariablesPruned {
			which = "this release"
		}
		d.VariablesNote = "the variable keys of " + which + " were pruned after the rollback window, so variables cannot be compared"
	default:
		explainVarDiff(&d, from.Variables, to.Variables)
		d.VariablesNote = "only keys and flags are compared; a changed value behind an unchanged key does not show here (values are never exposed)"
	}
	return d
}

func explainBuildMethod(r *deployments.ReleaseDetailView) string {
	if r.Build == nil {
		return ""
	}
	return r.Build.Method
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

func explainVarDiff(d *ExplainDiff, from, to []deployments.ReleaseVariableView) {
	old := make(map[string]deployments.ReleaseVariableView, len(from))
	for _, v := range from {
		old[v.Key] = v
	}
	seen := map[string]bool{}
	for _, v := range to {
		seen[v.Key] = true
		o, ok := old[v.Key]
		if !ok {
			d.VariablesAdded = append(d.VariablesAdded, v.Key)
			continue
		}
		var parts []string
		if o.Secret != v.Secret {
			parts = append(parts, fmt.Sprintf("secret %t→%t", o.Secret, v.Secret))
		}
		if o.Reference != v.Reference {
			parts = append(parts, fmt.Sprintf("reference %t→%t", o.Reference, v.Reference))
		}
		if o.Scope != v.Scope {
			parts = append(parts, fmt.Sprintf("scope %s→%s", o.Scope, v.Scope))
		}
		if o.External != v.External {
			parts = append(parts, fmt.Sprintf("external %t→%t", o.External, v.External))
		}
		if len(parts) > 0 {
			d.VariablesChanged = append(d.VariablesChanged, ExplainVarChange{Key: v.Key, Change: strings.Join(parts, ", ")})
		}
	}
	for _, v := range from {
		if !seen[v.Key] {
			d.VariablesRemoved = append(d.VariablesRemoved, v.Key)
		}
	}
	sort.Strings(d.VariablesAdded)
	sort.Strings(d.VariablesRemoved)
	const cap = 100
	if len(d.VariablesAdded) > cap {
		d.VariablesAdded = d.VariablesAdded[:cap]
	}
	if len(d.VariablesRemoved) > cap {
		d.VariablesRemoved = d.VariablesRemoved[:cap]
	}
	if len(d.VariablesChanged) > cap {
		d.VariablesChanged = d.VariablesChanged[:cap]
	}
}

// explainConfigDiff diffs two masked release configs (decoded JSON).
func explainConfigDiff(a, b any) []ExplainChange {
	fa, fb := explainFlatten(a), explainFlatten(b)
	keys := map[string]struct{}{}
	for k := range fa {
		keys[k] = struct{}{}
	}
	for k := range fb {
		keys[k] = struct{}{}
	}
	sorted := make([]string, 0, len(keys))
	for k := range keys {
		sorted = append(sorted, k)
	}
	sort.Strings(sorted)
	var out []ExplainChange
	for _, k := range sorted {
		if fa[k] == fb[k] {
			continue
		}
		from, to := fa[k], fb[k]
		if explainSecretField.MatchString(k) {
			from, to = explainMask(from), explainMask(to)
		}
		out = append(out, ExplainChange{Field: "config." + k, From: clip(from, 160), To: clip(to, 160)})
	}
	return out
}

func explainMask(s string) string {
	if s == "" {
		return ""
	}
	return "***"
}

// explainFlatten turns nested JSON into dotted keys; arrays stay one JSON value.
func explainFlatten(v any) map[string]string {
	out := map[string]string{}
	var walk func(prefix string, v any)
	walk = func(prefix string, v any) {
		switch x := v.(type) {
		case map[string]any:
			for k, vv := range x {
				key := k
				if prefix != "" {
					key = prefix + "." + k
				}
				walk(key, vv)
			}
		case string:
			out[prefix] = x
		case nil:
			out[prefix] = ""
		default:
			b, err := json.Marshal(x)
			if err != nil {
				out[prefix] = fmt.Sprint(x)
				return
			}
			out[prefix] = string(b)
		}
	}
	walk("", v)
	return out
}

// ---- serving now / rollback target ----

func explainServing(v services.ServiceView, failed uuid.UUID) ExplainServing {
	s := ExplainServing{}
	if v.CurrentDeploymentID != nil {
		s.DeploymentID = v.CurrentDeploymentID.String()
	}
	if v.CurrentRelease != nil {
		s.ReleaseID, s.ImageRef, s.CommitSHA = v.CurrentRelease.ID.String(), clip(v.CurrentRelease.ImageRef, 200), v.CurrentRelease.Source.CommitSHA
	}
	if o := v.Observed; o != nil {
		s.State, s.Health = o.State, o.Health
		s.Serving = o.State == agentprotocol.StateRunning && o.Health != agentprotocol.HealthUnhealthy
		for _, p := range processShortfalls(o) {
			s.ProcessesNotReady = append(s.ProcessesNotReady, processReadiness(p))
		}
	}
	switch {
	case v.CurrentDeploymentID == nil:
		s.Note = "nothing is deployed: no deployment of this service has succeeded"
	case failed != uuid.Nil && *v.CurrentDeploymentID != failed:
		s.PreviousReleaseServing = s.Serving
		if s.Serving {
			s.Note = "the previous release is still live, so the failed deployment did not take the service down"
		} else {
			s.Note = "the live release is not the failed one, but it is not running now (see state/health)"
		}
	}
	if v.Stale {
		s.Note = strings.TrimSpace(s.Note + " The observed state is stale (the server is offline or not reporting).")
	}
	return s
}

// explainRollbackTarget mirrors the checks deployments.Rollback makes for the
// last good release, without rolling back. The service id comes from a
// deployment the caller was already authorized to read.
func (c *composites) explainRollbackTarget(ctx context.Context, view services.ServiceView, good *deployments.DeploymentView, rel *deployments.ReleaseDetailView) (ExplainRollbackTarget, error) {
	if good == nil || rel == nil {
		return ExplainRollbackTarget{Code: "no_good_release", Reason: "no earlier succeeded deployment exists to roll back to"}, nil
	}
	t := ExplainRollbackTarget{
		DeploymentID: good.ID.String(), ReleaseID: rel.ID.String(), ImageRef: clip(rel.ImageRef, 200), CommitSHA: rel.Source.CommitSHA,
		VariablesPruned: rel.VariablesPruned, Disclaimer: deployments.RollbackDisclaimer,
	}
	if view.CurrentDeploymentID != nil && *view.CurrentDeploymentID == good.ID {
		t.Code, t.Reason = "already_current", "the last good release is already the one running; a rollback would change nothing"
		return t, nil
	}
	src := rel.Source
	q := c.q()
	switch {
	case src.Type == services.SourceGit && src.RegistryRef != "":
		repo, digest, _ := strings.Cut(src.RegistryRef, "@")
		gone := src.RegistryID == nil
		if !gone {
			img, err := q.GetRegistryImageByRef(ctx, db.GetRegistryImageByRefParams{RegistryID: *src.RegistryID, Repository: repo, Digest: digest})
			switch {
			case err == nil:
				gone = img.State != "live"
			case errors.Is(err, pgx.ErrNoRows):
				gone = true
			default:
				return t, err
			}
		}
		if gone {
			t.Code, t.Reason = "image_pruned", "the built image of the last good release is no longer in its registry; rebuild commit "+shortSHA(src.CommitSHA)+" instead of rolling back"
			return t, nil
		}
	case src.Type == services.SourceGit:
		retention := view.Config.ImageRetention
		if retention <= 0 {
			retention = 5
		}
		retained, err := q.ListNewestSucceededReleaseIDs(ctx, db.ListNewestSucceededReleaseIDsParams{ServiceID: view.ID, RowLimit: int32(retention)})
		if err != nil {
			return t, err
		}
		if src.ImageID == "" || !slices.Contains(retained, rel.ID) {
			t.Code, t.Reason = "image_pruned", "the built image of the last good release was pruned (only the newest "+strconv.Itoa(retention)+" are kept); rebuild commit "+shortSHA(src.CommitSHA)+" instead of rolling back"
			return t, nil
		}
	}
	t.Available = true
	if src.Type != services.SourceGit {
		t.Reason = "the release is an image that is pulled again; it works while that image tag still exists in its registry"
	}
	if rel.VariablesPruned {
		t.Reason = strings.TrimSpace(t.Reason + " Its variable values were removed after the rollback window.")
	}
	return t, nil
}

func shortSHA(s string) string {
	if len(s) > 7 {
		return s[:7]
	}
	return s
}

// ---- classification ----

type explainFacts struct {
	status, step, statusMsg string
	waiting                 bool
	waitingServer           string
	canceledAt, actor       string
	port                    int
	healthType, healthPath  string
	image                   string
	registryAuth            bool
	texts, raw              []string // error-looking and message lines: lowercase and original
	listenPorts             []int
	commit                  string
	serverName              string
}

var explainListen = regexp.MustCompile(`(?i)(?:listening|running|started|serving|ready|bound)[^\n]{0,60}?(?:port\s*|:)(\d{2,5})\b`)

func explainFactsOf(dep deployments.DeploymentView, view services.ServiceView, step string, evs []deployments.EventView, lines []deployments.LogLineView) explainFacts {
	f := explainFacts{
		status: dep.Status, step: step, statusMsg: dep.StatusMessage, canceledAt: dep.CanceledAtStep, actor: dep.ActorLabel,
		port: view.Config.Port, healthType: view.Config.Healthcheck.Type, healthPath: view.Config.Healthcheck.Path,
		image: view.Config.Image, registryAuth: view.RegistryAuthSet,
	}
	if dep.Release != nil {
		f.image = firstNonEmpty(dep.Release.ImageRef, f.image)
		f.commit = dep.Release.Source.CommitSHA
	}
	if dep.WaitingFor != nil {
		f.waiting, f.waitingServer = true, dep.WaitingFor.ServerName
	}
	if view.Server != nil {
		f.serverName = view.Server.Name
	}
	add := func(s string) {
		if s = strings.TrimSpace(s); s != "" && len(f.raw) < 600 {
			f.raw = append(f.raw, s)
			f.texts = append(f.texts, strings.ToLower(s))
		}
	}
	add(dep.StatusMessage)
	for _, e := range evs {
		add(e.Message)
	}
	seen := map[int]bool{}
	for _, l := range lines {
		if errorLine(l.Line) {
			add(l.Line)
		}
		if l.Stream == agentprotocol.StreamRuntime || l.Stream == agentprotocol.StreamDeploy {
			if m := explainListen.FindStringSubmatch(l.Line); m != nil {
				if n, err := strconv.Atoi(m[1]); err == nil && !seen[n] {
					seen[n] = true
					f.listenPorts = append(f.listenPorts, n)
				}
			}
		}
	}
	return f
}

// match returns the original lines containing any of the patterns.
func (f explainFacts) match(patterns ...string) []string {
	var out []string
	for i, t := range f.texts {
		for _, p := range patterns {
			if strings.Contains(t, p) {
				out = append(out, f.raw[i])
				break
			}
		}
	}
	return out
}

type explainClass struct {
	code, underlying, fix string
	evidence              []string
}

func explainClassify(f explainFacts) explainClass {
	switch f.status {
	case "canceled":
		at := firstNonEmpty(f.canceledAt, f.step)
		where := ""
		if at != "" {
			where = " at step " + at
		}
		by := ""
		if f.actor != "" {
			by = " by " + f.actor
		}
		return explainClass{code: causeCanceled, fix: "The deployment was canceled" + where + by + ", not broken: run apply_changes again if the change should still go out."}
	case "rollback_failed":
		u := explainRootCause(f)
		return explainClass{code: causeRollbackFailed, underlying: u.code, evidence: u.evidence,
			fix: "The new release failed and the previous release could not be put back, so the service may be down: roll back to the last good release with rollback_service (see rollbackTarget) or fix the cause (" + u.code + ": " + u.fix + ")"}
	case "rolled_back":
		u := explainRootCause(f)
		fix := "The new release started but failed verification after it was routed, so the previous release was put back: " + u.fix
		return explainClass{code: causeVerifyRolledBack, underlying: u.code, evidence: u.evidence, fix: fix}
	}
	return explainRootCause(f)
}

// explainRootCause classifies why a deployment failed from the step, status
// message and log text.
func explainRootCause(f explainFacts) explainClass {
	cls := func(code, fix string, ev []string) explainClass {
		return explainClass{code: code, fix: fix, evidence: ev}
	}

	if f.waiting || (f.step == "" || f.step == "queued") && len(f.match("is offline", "server offline", "agent offline", "did not take the request")) > 0 {
		server := firstNonEmpty(f.waitingServer, f.serverName, "the server")
		return cls(causeOffline, fmt.Sprintf("%s is offline, so the deployment cannot start: bring the agent back online (diagnose_server) or assign the service to another server; it continues by itself when the server returns.", server), f.match("offline"))
	}
	if ev := f.match("no space left on device"); len(ev) > 0 {
		return cls(causeDisk, "The server ran out of disk space: free space (docker cleanup, old images/volumes) on the server and redeploy.", ev)
	}
	if ev := f.match("exec format error", "no matching manifest for"); len(ev) > 0 {
		return cls(causePlatform, "The image was built for another CPU architecture than the server: build or pull an image for the server's platform (e.g. linux/amd64 vs arm64).", ev)
	}
	if ev := f.match("oomkilled", "out of memory", "exit code 137", "exited with code 137", "(137)", "signal: killed"); len(ev) > 0 && f.step != "pull" {
		where := "the container"
		if f.step == "build" || f.step == "push" {
			where = "the build"
		}
		return cls(causeOOM, "The kernel killed "+where+" for using too much memory: raise the memory limit (memoryMb) or reduce the app's memory use.", ev)
	}

	pullPatterns := []string{"pull access denied", "manifest unknown", "not found: manifest", "toomanyrequests", "rate limit", "failed to pull", "error pulling image", "authentication required", "requested access to the resource is denied", "no such image", "image not found", "failed to resolve reference"}
	switch {
	case f.step == "pull" || ((f.step == "" || f.step == "create") && len(f.match(pullPatterns...)) > 0):
		ev := f.match(pullPatterns...)
		switch {
		case len(f.match("denied", "unauthorized", "authentication required")) > 0:
			return cls(causeImagePull, fmt.Sprintf("The registry refused access to %s: set valid registry credentials on the service (or make the image public) and redeploy.", orUnknown(f.image)), ev)
		case len(f.match("manifest unknown", "not found")) > 0:
			return cls(causeImagePull, fmt.Sprintf("The image or tag %s does not exist in its registry: stage an existing tag or digest and redeploy.", orUnknown(f.image)), ev)
		case len(f.match("toomanyrequests", "rate limit")) > 0:
			return cls(causeImagePull, "The registry rate-limited the pull: add registry credentials to raise the limit, or retry later.", ev)
		}
		return cls(causeImagePull, fmt.Sprintf("The server could not pull %s: check the image reference and that the server can reach the registry, then redeploy.", orUnknown(f.image)), f.match("pull", "registry", "timeout"))
	case f.step == "build" || f.step == "push":
		fix := "The build failed: fix the first error in logExcerpt"
		if f.commit != "" {
			fix += " (commit " + shortSHA(f.commit) + ")"
		}
		fix += ", push the fix or redeploy; the previous release keeps serving."
		return cls(causeBuild, fix, f.match("error", "failed"))
	case f.step == "predeploy":
		return cls(causePredeploy, "The pre-deploy command (e.g. a migration) exited non-zero: fix it, or stage an empty preDeployCommand, then redeploy; the previous release keeps serving.", f.match("error", "failed", "exit"))
	case f.step == "route":
		return cls(causeProxy, "The container is healthy but the proxy refused the route (domain conflict, certificate or proxy error): check the domains with setup_domain and the server's proxy with diagnose_server.", f.match("route", "proxy", "certificate", "domain", "conflict"))
	case f.step == "create":
		if ev := f.match("port is already allocated", "address already in use", "bind for"); len(ev) > 0 {
			return cls(causePortConflict, "Another container or process on the server already uses the host port: stop it or change the service's published port, then redeploy.", ev)
		}
		return cls(causeConfig, "Docker rejected the container settings: review the staged config (ports, volumes, docker options, memory/cpu) in releaseDiff and fix the invalid value.", f.match("invalid", "error", "failed"))
	}

	crash := f.match("container exited", "exited with code", "exited (", "exit status", "panic", "traceback", "segfault", "fatal error", "uncaught exception", "unhandled", "modulenotfound", "cannot find module", "importerror", "crashloop", "restarting")
	configLines := f.match("executable file not found", "no such file or directory", "permission denied", "invalid reference format", "unresolved reference", "${{", "missing required", "invalid config", "unknown variable")
	switch f.step {
	case "start":
		if len(configLines) > 0 {
			return cls(causeConfig, "The container could not start its command: check the start command and the files/permissions it needs (see logExcerpt), then redeploy.", configLines)
		}
		return cls(causeCrash, "The container exits right after starting: read logExcerpt (runtime lines); usual causes are a missing or invalid variable or a failing start command.", orLines(crash, f.match("error", "failed")))
	case "health", "verify":
		if len(crash) > 0 {
			return cls(causeCrash, "The container starts and then exits before it becomes healthy: read the runtime lines in logExcerpt; usual causes are a missing or invalid variable or a failing start command.", crash)
		}
		for _, lp := range f.listenPorts {
			if f.port > 0 && lp != f.port {
				return cls(causePortMismatch, fmt.Sprintf("The app listens on port %d but the service is configured for port %d: stage port %d (or make the app listen on %d).", lp, f.port, lp, f.port), f.match(strconv.Itoa(lp)))
			}
		}
		if ev := f.match("connection refused", "connect: refused", "refused"); len(ev) > 0 {
			return cls(causePortMismatch, fmt.Sprintf("Connections to port %d were refused: make the app listen on 0.0.0.0:%d (not 127.0.0.1), or stage the port it really uses.", f.port, f.port), ev)
		}
		return cls(causeHealth, explainHealthFix(f), orLines(f.match("health", "unhealthy", "timed out", "deadline"), f.match("error", "failed")))
	}

	// No step reported: fall back on the message text alone.
	switch {
	case len(configLines) > 0 || len(f.match("invalid", "unprocessable", "no_server", "assign the service")) > 0:
		return cls(causeConfig, "The deployment was refused before anything ran because of the service configuration: read statusMessage and fix the named setting.", orLines(configLines, f.match("invalid", "assign the service")))
	case len(f.match("health check", "unhealthy", "did not become healthy")) > 0:
		return cls(causeHealth, explainHealthFix(f), f.match("health", "unhealthy"))
	case len(f.match("proxy", "traefik", "certificate", "route")) > 0:
		return cls(causeProxy, "The proxy rejected the route: check the domains with setup_domain and the server's proxy with diagnose_server.", f.match("proxy", "traefik", "certificate", "route"))
	}
	return cls(causeUnknown, "No known failure pattern matched: read statusMessage, the timeline and logExcerpt, or call tail_logs with streams:all for the engine's debug trace.", f.match("error", "failed"))
}

func explainHealthFix(f explainFacts) string {
	check := "its health check"
	switch f.healthType {
	case services.HealthHTTP:
		check = fmt.Sprintf("its http health check (%s on port %d)", firstNonEmpty(f.healthPath, "/"), f.port)
	case services.HealthTCP:
		check = fmt.Sprintf("its tcp health check on port %d", f.port)
	}
	return "The container started but did not pass " + check + " in time: make sure the endpoint answers 2xx quickly, fix the path or port, or raise the health check deadline."
}

func orUnknown(s string) string {
	if s == "" {
		return "the image"
	}
	return s
}

func orLines(a, b []string) []string {
	if len(a) > 0 {
		return a
	}
	return b
}

// ---- summary and next tools ----

func explainSummary(sel string, dep deployments.DeploymentView, out ExplainFailedOutput) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Deployment %s of %s ended %s", shortID(dep.ID), sel, dep.Status)
	if out.FailedStep != "" {
		sb.WriteString(" at step " + out.FailedStep)
	}
	if out.FailedProcess != "" {
		sb.WriteString(" in process " + out.FailedProcess)
	}
	sb.WriteString(". Likely cause: " + out.LikelyCause)
	if out.UnderlyingCause != "" {
		sb.WriteString(" (" + out.UnderlyingCause + ")")
	}
	sb.WriteString(". ")
	switch {
	case out.ServingNow.PreviousReleaseServing:
		sb.WriteString("The previous release is still serving. ")
	case out.ServingNow.DeploymentID == "":
		sb.WriteString("Nothing is deployed yet. ")
	case !out.ServingNow.Serving:
		sb.WriteString("The service is not serving right now. ")
	}
	if rt := out.RollbackTarget; rt.Code == "image_pruned" {
		sb.WriteString("The last good image was pruned, so rollback is not possible. ")
	}
	sb.WriteString("Fix: " + out.SmallestFix)
	return sb.String()
}

func explainNextTools(sel string, generation int64, dep deployments.DeploymentView, out ExplainFailedOutput, canLogs bool) []NextTool {
	svc := map[string]any{"service": sel}
	next := []NextTool{}
	switch out.LikelyCause {
	case causeCanceled:
		next = append(next, NextTool{Tool: "apply_changes", Args: map[string]any{"services": []any{map[string]any{"service": sel, "expectedGeneration": generation}}}, Why: "run the deployment again (the canceled change is still staged)"})
	case causeOffline:
		next = append(next, NextTool{Tool: "diagnose_service", Args: svc, Why: "shows the host server's health and how long it has been offline"})
	default:
		next = append(next,
			NextTool{Tool: "stage_changes", Args: svc, Why: "stage the fix for " + out.LikelyCause + " (config or variables); nothing changes until apply_changes"},
			NextTool{Tool: "apply_changes", Args: map[string]any{"services": []any{map[string]any{"service": sel, "expectedGeneration": generation}}}, Why: "deploy the staged fix; use the generation stage_changes returns, it moves when you stage"},
		)
	}
	if rt := out.RollbackTarget; rt.Available && !out.ServingNow.PreviousReleaseServing {
		next = append(next, NextTool{Tool: "rollback_service", Args: map[string]any{"service": sel, "to": rt.ReleaseID}, Why: "the service is not serving the last good release; put it back (application image and config only; every process goes back)"})
	}
	if canLogs && out.FailedProcess != "" {
		next = append(next, NextTool{Tool: "tail_logs", Args: map[string]any{"service": sel, "process": out.FailedProcess}, Why: "runtime output of the failing process only"})
	}
	if canLogs {
		next = append(next, NextTool{Tool: "tail_logs", Args: map[string]any{"deployment": dep.ID.String(), "streams": "all"}, Why: "more lines, including the engine's debug trace"})
	}
	return next
}
