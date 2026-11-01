package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Cancel a running deploy (see pkg/agentprotocol/deploycontrols.go for the
// contract). A cancel_deploy command finds the deploy by deployment ID and
// cancels its context with errDeployCanceled; every step of deploy.run
// observes the context, and run turns the cancellation into an unwind plus the
// `canceled` outcome. The point of no return is the end of the verify step:
// from then on the new release is live and a cancel is too late (apart from
// the post-deploy command, which can still be stopped).

// errDeployCanceled is the cancel cause of a deploy canceled by a person. A
// context that ends for any other reason (the agent shutting down) has a
// different cause, which keeps "agent stopped" and "canceled" apart.
var errDeployCanceled = errors.New("deployment canceled")

// pendingCancelTTL is how long a cancel request that found no deploy waits for
// the deploy command to arrive (it can race it by milliseconds, or reach a
// restarted agent first).
const pendingCancelTTL = 10 * time.Minute

// stepStop is the cancel handle's name for the recreate strategy's "stop the
// previous release" phase, which has no event of its own.
const stepStop = "stop"

// canceled reports whether ctx ended because a person canceled the deploy.
func canceled(ctx context.Context) bool {
	return ctx.Err() != nil && errors.Is(context.Cause(ctx), errDeployCanceled)
}

// deployRun is the cancel handle of one running deploy command.
type deployRun struct {
	mu        sync.Mutex
	step      string
	cancel    context.CancelCauseFunc
	by        string
	canceled  bool                    // the whole deploy was canceled
	committed bool                    // the new release is live: too late to abort
	soft      context.CancelCauseFunc // set while post-deploy runs: aborts just that command
	softDone  bool
}

// setStep records the step the deploy has reached.
func (r *deployRun) setStep(step string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.step = step
	r.mu.Unlock()
}

// stepName is the step the deploy is in ("queued" before it started one).
func (r *deployRun) stepName() string {
	if r == nil {
		return ""
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.step == "" {
		return "queued"
	}
	return r.step
}

// canceledBy is who asked for the cancel, for messages.
func (r *deployRun) canceledBy() string {
	if r == nil {
		return ""
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.by
}

// commit marks the new release live. It returns false, and the deploy must
// unwind, when a cancel got in first.
func (r *deployRun) commit() bool {
	if r == nil {
		return true
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.canceled {
		return false
	}
	r.committed = true
	return true
}

// request asks the deploy to stop. taken says whether it will (the step is
// where it was); false means it is too late.
func (r *deployRun) request(by string) (taken bool, step string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	step = r.step
	if step == "" {
		step = "queued"
	}
	switch {
	case r.canceled:
		return true, step // asked twice
	case !r.committed:
		r.canceled, r.by = true, by
		r.cancel(errDeployCanceled)
		return true, step
	case r.soft != nil && !r.softDone:
		r.softDone, r.by = true, by
		r.soft(errDeployCanceled)
		return true, agentprotocol.StepPostdeploy
	}
	return false, step
}

// postDeployContext returns the context the post-deploy command runs under:
// the deploy's, plus a handle a cancel request uses to stop only that command.
func (r *deployRun) postDeployContext(ctx context.Context) (context.Context, func()) {
	if r == nil {
		return ctx, func() {}
	}
	pctx, cancel := context.WithCancelCause(ctx)
	r.mu.Lock()
	r.soft = cancel
	r.mu.Unlock()
	return pctx, func() {
		r.mu.Lock()
		r.soft = nil
		r.mu.Unlock()
		cancel(nil)
	}
}

// cancelState is the engine's registry of running deploys.
type cancelState struct {
	mu      sync.Mutex
	runs    map[string]*deployRun
	pending map[string]time.Time // deployment ID -> when a cancel arrived before its deploy
}

// start registers a deploy and returns its context. A cancel that arrived
// before the deploy command makes the context canceled right away.
func (c *cancelState) start(ctx context.Context, deploymentID string) (context.Context, *deployRun, func()) {
	dctx, cancel := context.WithCancelCause(ctx)
	run := &deployRun{cancel: cancel}
	c.mu.Lock()
	if c.runs == nil {
		c.runs = map[string]*deployRun{}
	}
	c.runs[deploymentID] = run
	at, early := c.pending[deploymentID]
	delete(c.pending, deploymentID)
	c.mu.Unlock()
	if early && time.Since(at) < pendingCancelTTL {
		run.canceled = true
		cancel(errDeployCanceled)
	}
	return dctx, run, func() {
		c.mu.Lock()
		if c.runs[deploymentID] == run {
			delete(c.runs, deploymentID)
		}
		c.mu.Unlock()
		cancel(nil)
	}
}

// request routes a cancel to the running deploy. found is false when no deploy
// of that deployment runs here; the request is then remembered for pendingCancelTTL.
func (c *cancelState) request(deploymentID, by string) (found, taken bool, step string) {
	c.mu.Lock()
	run := c.runs[deploymentID]
	if run == nil {
		if c.pending == nil {
			c.pending = map[string]time.Time{}
		}
		now := time.Now()
		for id, at := range c.pending {
			if now.Sub(at) > pendingCancelTTL {
				delete(c.pending, id)
			}
		}
		c.pending[deploymentID] = now
		c.mu.Unlock()
		return false, false, ""
	}
	c.mu.Unlock()
	taken, step = run.request(by)
	return true, taken, step
}

// handleCancelDeploy implements the cancel_deploy command.
func (e *Engine) handleCancelDeploy(cmd agentprotocol.Command) agentprotocol.ResultFrame {
	var p agentprotocol.CancelDeployPayload
	if err := json.Unmarshal(cmd.Payload, &p); err != nil || p.DeploymentID == "" {
		return failedResult(cmd.ID, "invalid cancel_deploy payload")
	}
	by := p.RequestedBy
	if by == "" {
		by = "a user"
	}
	found, taken, step := e.cancel.request(p.DeploymentID, by)
	switch {
	case !found:
		return okResult(cmd.ID, "", "No deployment "+p.DeploymentID+" is running on this agent; it is canceled if it starts.")
	case !taken:
		e.cfg.Sender.Event(agentprotocol.EventFrame{
			DeploymentID: p.DeploymentID, Step: agentprotocol.StepCancel, Status: agentprotocol.StepFailed,
			Message: "Too late to cancel: the new release is already live (" + step + ").",
		})
		return okResult(cmd.ID, "", "too late: the new release is live")
	}
	e.cfg.Sender.Event(agentprotocol.EventFrame{
		DeploymentID: p.DeploymentID, Step: agentprotocol.StepCancel, Status: agentprotocol.StepStarted,
		Message: "Canceled by " + by + " during " + stepLabel(step) + "; stopping.",
	})
	return okResult(cmd.ID, "", "cancel requested during "+step)
}

// stepLabel is a step name for sentences.
func stepLabel(step string) string {
	switch step {
	case agentprotocol.StepPull:
		return "the image pull"
	case agentprotocol.StepBuild:
		return "the build"
	case agentprotocol.StepPush:
		return "the image push"
	case agentprotocol.StepPredeploy:
		return "the pre-deploy command"
	case agentprotocol.StepCreate:
		return "container creation"
	case agentprotocol.StepStart:
		return "container start"
	case agentprotocol.StepHealth:
		return "the health check"
	case agentprotocol.StepRoute:
		return "the route update"
	case agentprotocol.StepVerify:
		return "verification"
	case agentprotocol.StepPostdeploy:
		return "the post-deploy command"
	case stepStop:
		return "stopping the previous release"
	case "queued", "":
		return "start-up"
	}
	return step
}

// interrupted ends a deploy whose context ended during a step: a canceled
// deploy is unwound and reported as canceled, anything else (the agent is
// shutting down) leaves state for the redelivered command to continue.
func (d *deploy) interrupted(ctx context.Context, st *deployState, hadPrevious bool) agentprotocol.DeployResult {
	if canceled(ctx) {
		return d.abortCanceled(ctx, st, hadPrevious)
	}
	return d.fail("agent stopped during deployment")
}

// abortCanceled unwinds a canceled deploy at the step it reached and reports
// the outcome:
//
//   - containers of the new release (including a half-created one that never
//     returned its ID) are removed, so the previous release keeps serving;
//   - a recreate deploy that stopped the previous release starts it again;
//   - a verify cancel has already restored the previous routes (routePhase);
//   - when the previous release or its routes cannot be restored the outcome is
//     rollback_failed with the manual recovery steps, never canceled.
func (d *deploy) abortCanceled(ctx context.Context, st *deployState, hadPrevious bool) agentprotocol.DeployResult {
	e := d.e
	cctx, cancel := detached(ctx, 2*time.Minute)
	defer cancel()

	step := d.ctl.stepName()
	by := d.ctl.canceledBy()
	if by == "" {
		by = "a user"
	}
	d.logf("Canceled by %s during %s; undoing what was done", by, stepLabel(step))

	var problems []string
	if st.routeErr != nil {
		problems = append(problems, "could not restore previous routes: "+st.routeErr.Error())
	}
	restoreFailed := st.routeErr != nil
	removedNew, restarted := false, false
	if st.routeErr == nil {
		if len(d.created) > 0 {
			d.removeCreated(cctx)
			removedNew = true
		}
		// A create or start that was interrupted may have left a container the
		// call never reported: remove it by its deterministic name.
		if step == agentprotocol.StepCreate || step == agentprotocol.StepStart {
			removedNew = d.removeByName(cctx) || removedNew
		}
		if st.recreate {
			for _, c := range st.toRestart {
				d.logf("Restarting previous container %s", c.Name)
				if err := e.cfg.Docker.ContainerStart(cctx, c.ID); err != nil {
					restoreFailed = true
					problems = append(problems, fmt.Sprintf("could not restart %s: %v", c.Name, err))
					continue
				}
				restarted = true
			}
		}
	}

	serving := len(st.serving) > 0 || len(st.toRestart) > 0 || hadPrevious
	if !serving && len(st.prev) == 0 && step != "queued" {
		// Canceled before the inventory: is a release running?
		if running, err := e.containersOf(cctx, d.p.ServiceID, false); err == nil && len(running) > 0 {
			serving = true
		}
	}

	head := "Canceled by " + by + " during " + stepLabel(step) + "."
	outcome := agentprotocol.OutcomeCanceled
	var msg strings.Builder
	if restoreFailed {
		outcome = agentprotocol.OutcomeRollbackFailed
		msg.WriteString(strings.TrimSuffix(head, ".") + ", but the previous release could not be restored: " + strings.Join(problems, "; ") +
			". Manual recovery is required: check `docker ps --filter label=nebulactrl.service=" + d.p.ServiceID + "` and redeploy.")
	} else {
		msg.WriteString(head)
		if st.routesRestored {
			msg.WriteString(" The previous routes were restored.")
		}
		if removedNew {
			msg.WriteString(" The new containers were removed.")
		}
		switch {
		case restarted:
			msg.WriteString(" The previous release was started again.")
		case serving:
			msg.WriteString(" The previous release is still serving.")
		case !st.routesRestored && !removedNew:
			msg.WriteString(" Nothing was changed.")
		}
	}
	status := agentprotocol.StepSucceeded
	if outcome != agentprotocol.OutcomeCanceled {
		status = agentprotocol.StepFailed
	}
	d.event(agentprotocol.StepCancel, status, msg.String())
	d.log.Info("deploy canceled", "outcome", outcome, "step", step)
	res := d.result(outcome, msg.String(), "")
	res.CanceledAt = step
	return res
}

// removeByName force-removes a container of this deploy by its deterministic
// name when it is not already tracked. It reports whether it removed any.
func (d *deploy) removeByName(ctx context.Context) bool {
	eng := d.e.cfg.Docker
	removed := false
	for _, name := range d.names {
		if err := eng.ContainerRemove(ctx, name, true, false); err == nil {
			removed = true
		} else if !errors.Is(err, docker.ErrNotFound) {
			d.logf("Warning: remove container %s: %v", name, err)
		}
	}
	return removed
}
