package agentprotocol

// Cancel a running deployment.
//
// A person can cancel a deployment at any step. Before the agent acknowledged
// the deploy command the control plane simply cancels the command. Once the
// agent runs it, the control plane queues a cancel_deploy command and the
// agent aborts the deploy at its current step:
//
//   - pull, build, pre-deploy: the pull or build is killed (a build's work
//     directory is removed), nothing of the running release was touched;
//   - create, start, health, route: the containers of the new release are
//     removed, the running release keeps serving. A recreate deploy that already
//     stopped the old release starts it again;
//   - verify: the previous routes are restored first, then the new containers
//     are removed;
//   - a failed restore ends as rollback_failed (manual recovery), never as
//     canceled.
//
// The deploy command's result is then OutcomeCanceled, with
// DeployResult.CanceledAt naming the step reached. Once the new release is
// live (the verify step passed) a cancel is too late: the deploy finishes, and
// the agent reports that with a StepCancel event.

// KindCancelDeploy aborts a running deploy command of the same server.
const KindCancelDeploy = "cancel_deploy"

func init() { AllowedKinds[KindCancelDeploy] = true }

// CapCancelDeploy is advertised in Facts.Capabilities by agents that understand
// cancel_deploy. A server that has never connected has no facts yet and is
// assumed to enroll with a current agent.
const CapCancelDeploy = "deploy.cancel"

// OutcomeCanceled is the outcome of a deploy command that was canceled and
// fully unwound: the previous release (if any) serves exactly as before.
const OutcomeCanceled = "canceled"

// StepCancel is the event step the agent reports for a cancel request: started
// when it takes effect, failed when it came too late.
const StepCancel = "cancel"

// CancelDeployPayload is the payload of a cancel_deploy command. It carries no
// secrets.
type CancelDeployPayload struct {
	// DeploymentID is the deployment whose deploy command should abort.
	DeploymentID string `json:"deploymentId"`
	// RequestedBy is who asked (display label), for the deploy log.
	RequestedBy string `json:"requestedBy,omitempty"`
}
