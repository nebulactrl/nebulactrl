package composites

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/deployments"
	"github.com/nebulactrl/nebulactrl/internal/mcp/mcpkit"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/prodguard"
)

// maxWaitSeconds keeps a waiting call inside typical MCP client timeouts.
const maxWaitSeconds = 50

// waitPollInterval is how often waitDeployments re-reads the deployments.
const waitPollInterval = time.Second

// waitLogPage is the page size used to find the end of a deployment log.
const waitLogPage = 500

// waitLogPages bounds the pages read (MaxLogLines / waitLogPage).
const waitLogPages = deployments.MaxLogLines / waitLogPage

// parseWaitSeconds validates the waitSeconds argument (0 = do not wait).
func parseWaitSeconds(n int) (time.Duration, error) {
	if n < 0 || n > maxWaitSeconds {
		return 0, invalid("waitSeconds must be between 0 and %d", maxWaitSeconds)
	}
	return time.Duration(n) * time.Second, nil
}

// AppliedDeployment is one deployment a tool started (apply_changes,
// rollback_service), optionally with its terminal outcome.
type AppliedDeployment struct {
	Service      string `json:"service" jsonschema:"project/env/service selector"`
	ServiceID    string `json:"serviceId"`
	DeploymentID string `json:"deploymentId"`
	Trigger      string `json:"trigger"`
	Status       string `json:"status" jsonschema:"deployment status right now (created, queued, preparing, building, releasing, verifying, succeeded, failed, canceled, rolled_back, rollback_failed)"`
	Strategy     string `json:"strategy"`
	// Outcome is in_progress until the deployment is terminal; then the
	// terminal status. outcome_uncertain: the agent acknowledged the command
	// but its server went offline, so the result is unknown (do not retry).
	Outcome  string `json:"outcome" jsonschema:"in_progress, the terminal status, or outcome_uncertain"`
	Terminal bool   `json:"terminal"`
	// StatusMessage is the message of the platform or agent for this
	// deployment; it can echo build or registry output, so it is untrusted.
	StatusMessage any `json:"statusMessage,omitempty" jsonschema:"message of the platform or agent, as an untrusted block"`
	// Note is NebulaCtrl's own guidance for an outcome the caller must treat
	// with care (outcome_uncertain).
	Note string `json:"note,omitempty"`
	// FailedStep is the deploy step that failed (pull, build, predeploy, start, health, ...).
	FailedStep string `json:"failedStep,omitempty"`
	// WaitingFor names the offline server a not yet delivered deployment waits for.
	WaitingFor string `json:"waitingFor,omitempty"`
	// LogTail is the end of the build/deploy log (untrusted text), present
	// when the call waited for a terminal outcome and logs:read is held.
	LogTail *mcpkit.Block `json:"logTail,omitempty"`
}

// appliedFrom renders a deployment view without any waiting.
func appliedFrom(selector string, v deployments.DeploymentView) AppliedDeployment {
	a := AppliedDeployment{
		Service: selector, ServiceID: v.ServiceID.String(), DeploymentID: v.ID.String(), Trigger: v.Trigger,
		Status: v.Status, Strategy: v.Strategy, StatusMessage: untrustedText("deployment.statusMessage", clip(v.StatusMessage, 500)),
		Terminal: deployments.Status(v.Status).IsTerminal(), Outcome: "in_progress",
	}
	if a.Terminal {
		a.Outcome = v.Status
	}
	if v.WaitingFor != nil && !a.Terminal {
		a.WaitingFor = v.WaitingFor.ServerName
	}
	return a
}

// waitDeployments polls the deployments every waitPollInterval until all are
// terminal, d elapsed or ctx ended, and returns the last view of each. d == 0
// reads nothing. A deployment whose read fails keeps its previous view and the
// error is returned under its id.
func (c *composites) waitDeployments(ctx context.Context, views map[uuid.UUID]deployments.DeploymentView, d time.Duration) (map[uuid.UUID]deployments.DeploymentView, map[uuid.UUID]error) {
	errs := map[uuid.UUID]error{}
	out := make(map[uuid.UUID]deployments.DeploymentView, len(views))
	pending := map[uuid.UUID]bool{}
	for id, v := range views {
		out[id] = v
		if !deployments.Status(v.Status).IsTerminal() {
			pending[id] = true
		}
	}
	if d <= 0 || len(pending) == 0 {
		return out, errs
	}
	deadline := time.NewTimer(d)
	defer deadline.Stop()
	tick := time.NewTicker(waitPollInterval)
	defer tick.Stop()
	for {
		for id := range pending {
			v, err := c.Deployments.Get(ctx, id)
			if err != nil {
				if ctx.Err() == nil {
					errs[id] = err
				}
				delete(pending, id)
				continue
			}
			out[id] = v
			if deployments.Status(v.Status).IsTerminal() {
				delete(pending, id)
			}
		}
		if len(pending) == 0 || ctx.Err() != nil {
			return out, errs
		}
		select {
		case <-ctx.Done():
			return out, errs
		case <-deadline.C:
			return out, errs
		case <-tick.C:
		}
	}
}

// describeOutcome fills the terminal outcome (failure summary, failed step,
// log tail) of a finished deployment, and the outcome_uncertain marker of an
// unfinished one whose command the agent acknowledged before going offline.
// Sections that need logs:read degrade quietly into notes.
func (c *composites) describeOutcome(ctx context.Context, p *auth.Principal, a *AppliedDeployment, v deployments.DeploymentView, tail int) (notes []SectionError) {
	id := v.ID
	if !a.Terminal {
		if v.WaitingFor != nil && c.commandAcked(ctx, id) {
			a.Outcome = mcpkit.CodeOutcomeUncertain
			a.Note = clip(fmt.Sprintf("The agent acknowledged the deployment but server %q is offline; the outcome is unknown until it reconnects. Do not retry blindly.", v.WaitingFor.ServerName), 500)
		}
		return nil
	}
	if v.Status != string(deployments.Succeeded) {
		evs, err := c.Deployments.Events(ctx, id)
		if err != nil {
			notes = append(notes, sectionError("events "+a.DeploymentID, err))
		} else {
			a.FailedStep = failedStepOf(evs)
		}
	}
	if tail <= 0 {
		return notes
	}
	if !holds(p, policy.ScopeLogsRead) {
		return append(notes, SectionError{Section: "logTail " + a.DeploymentID, Code: mcpkit.CodeForbidden, Message: "the log tail needs logs:read"})
	}
	lines, err := c.deploymentLogTail(ctx, id, tail)
	if err != nil {
		if isForbidden(err) {
			return append(notes, SectionError{Section: "logTail " + a.DeploymentID, Code: mcpkit.CodeForbidden, Message: "the log tail needs logs:read"})
		}
		return append(notes, sectionError("logTail "+a.DeploymentID, err))
	}
	if len(lines) > 0 {
		b := mcpkit.WrapLines("deployment/"+a.DeploymentID, lines)
		a.LogTail = &b
	}
	return notes
}

// failedStepOf names the step of the first non-status event that failed;
// without one, the last non-status step before the failure.
func failedStepOf(evs []deployments.EventView) string {
	last := ""
	for _, e := range evs {
		if e.Step == deployments.StatusStep {
			continue
		}
		if e.Status == "failed" {
			return e.Step
		}
		last = e.Step
	}
	return last
}

// deploymentLogTail returns the last n persisted build/deploy/runtime lines of
// a deployment (already redacted at write time), oldest first.
func (c *composites) deploymentLogTail(ctx context.Context, id uuid.UUID, n int) ([]string, error) {
	ring := make([]string, 0, n)
	var after int64
	for range waitLogPages {
		page, err := c.Deployments.LogLines(ctx, id, after, waitLogPage, deployments.DefaultLogStreams)
		if err != nil {
			return nil, err
		}
		for _, l := range page {
			after = l.ID
			if len(ring) == n {
				copy(ring, ring[1:])
				ring = ring[:n-1]
			}
			ring = append(ring, l.Line)
		}
		if len(page) < waitLogPage {
			break
		}
	}
	return ring, nil
}

// commandAcked reports whether the agent acknowledged the deployment's deploy
// command (so it may have started on the host). The id comes from a service
// call of this tool, never from the caller.
func (c *composites) commandAcked(ctx context.Context, deploymentID uuid.UUID) bool {
	if c.Pool == nil {
		return false
	}
	var status string
	err := c.Pool.QueryRow(ctx,
		`SELECT status FROM agent_commands WHERE deployment_id = $1 AND kind = 'deploy' ORDER BY created_at DESC LIMIT 1`,
		deploymentID).Scan(&status)
	return err == nil && status == "acked"
}

// changesErr maps an error of a write path to a tool error carrying its
// stable code. The production guard's *prodguard.ErrApprovalRequired is
// returned UNCHANGED (the MCP layer turns it into a confirmation_required
// result with the approval page; the retried call runs the tool), as is any
// error that already is a tool error.
func changesErr(err error) error {
	if err == nil {
		return nil
	}
	if _, ok := prodguard.AsRequired(err); ok {
		return err
	}
	var te *mcpkit.Error
	if errors.As(err, &te) {
		return err
	}
	if ae, ok := apierr.As(err); ok && ae.Status < 500 {
		out := &mcpkit.Error{Code: ae.Code, Message: ae.Message}
		if len(ae.Details) > 0 {
			out.Data = ae.Details
		}
		return out
	}
	return err
}

// isApproval reports whether err is the production guard's approval request.
func isApproval(err error) bool {
	_, ok := prodguard.AsRequired(err)
	return ok
}
