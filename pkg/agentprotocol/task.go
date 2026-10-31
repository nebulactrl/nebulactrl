package agentprotocol

import (
	"strings"
	"time"
)

// KindRunTask runs a one-shot command in a throwaway container of a service's
// image: cron tasks, one-off commands ("run command"), preview seed commands.
// The pre-deploy command is not a command of its own: it is a step of deploy.
const KindRunTask = "run_task"

func init() { AllowedKinds[KindRunTask] = true }

// StepPredeploy is the deploy step that runs the service's pre-deploy command
// in a container of the new image, before any container of the new release
// starts. The control plane maps it to the `releasing` status.
const StepPredeploy = "predeploy"

// StepPostdeploy is the deploy step that runs the service's post-deploy
// command in a container of the new release after the swap, once the new
// release is live and the previous one retired. It never changes the deployment
// status (the deployment is already `verifying`), and its failure is
// warn-only: the step ends with StepWarning, not StepFailed, and the
// deployment still succeeds.
const StepPostdeploy = "postdeploy"

// StepWarning is the step status of a step that failed without failing the
// deployment (the post-deploy command). The control plane records it in the
// timeline and raises a notification.
const StepWarning = "warning"

// Labels of task containers. They deliberately do not carry LabelService:
// a task container is not a replica, must never be counted among a service's
// containers, and is removed by the task run itself (or by the orphan sweep of
// task containers).
const (
	LabelTaskRun     = "nebulactrl.task-run"
	LabelTaskService = "nebulactrl.task-service"
	// LabelTaskKind is "task" for run_task containers, "predeploy" for
	// pre-deploy containers and "postdeploy" for post-deploy containers.
	LabelTaskKind = "nebulactrl.task-kind"
)

// Task limits shared by the control plane and the agent.
const (
	DefaultTaskTimeoutSeconds = 30 * 60
	MaxTaskTimeoutSeconds     = 24 * 60 * 60
	// DefaultPreDeployTimeoutSeconds is the pre-deploy timeout when the
	// service does not set one.
	DefaultPreDeployTimeoutSeconds = 10 * 60
	MaxPreDeployTimeoutSeconds     = 60 * 60
	// DefaultPostDeployTimeoutSeconds is the post-deploy timeout when the
	// service does not set one.
	DefaultPostDeployTimeoutSeconds = 10 * 60
	MaxPostDeployTimeoutSeconds     = 60 * 60
)

// TaskContainerName returns nc-task-<slug>-<runId8>.
func TaskContainerName(slug, runID string) string {
	return ContainerPrefix + "task-" + slug + "-" + Short(runID)
}

// PreDeployContainerName returns nc-predeploy-<slug>-<deploymentId8>.
func PreDeployContainerName(slug, deploymentID string) string {
	return ContainerPrefix + "predeploy-" + slug + "-" + Short(deploymentID)
}

// PostDeployContainerName returns nc-postdeploy-<slug>-<deploymentId8>.
func PostDeployContainerName(slug, deploymentID string) string {
	return ContainerPrefix + "postdeploy-" + slug + "-" + Short(deploymentID)
}

// PreDeploySpec is the pre-deploy command of a deploy command. It runs once
// per deployment in a container of the new image with the deploy's
// environment. A non-zero exit fails the deployment at step "predeploy".
type PreDeploySpec struct {
	Command        []string `json:"command"`
	TimeoutSeconds int      `json:"timeoutSeconds,omitempty"`
}

// PostDeploySpec is the post-deploy command of a deploy command. It runs once
// per deployment in a container of the new release's image with the deploy's
// environment, after the new release is live. A non-zero exit, a timeout or a
// start error is a warning: the deployment still succeeds.
type PostDeploySpec struct {
	Command        []string `json:"command"`
	TimeoutSeconds int      `json:"timeoutSeconds,omitempty"`
}

// RunTaskPayload is the persisted payload of a run_task command. It never
// carries variable values: the environment is attached at send time in
// Command.Secrets ("env:<KEY>", registry auth) and exists only in agent memory.
type RunTaskPayload struct {
	RunID         string `json:"runId"`
	ServiceID     string `json:"serviceId"`
	ServiceSlug   string `json:"serviceSlug"`
	EnvironmentID string `json:"environmentId"`
	// DeploymentID and ReleaseID identify the service's current release, whose
	// environment the task runs with.
	DeploymentID string `json:"deploymentId,omitempty"`
	ReleaseID    string `json:"releaseId,omitempty"`
	// ImageRef is used when the service has no container on the host (stopped,
	// replaced): the release's image reference (digest when known).
	ImageRef        string `json:"imageRef,omitempty"`
	HasRegistryAuth bool   `json:"hasRegistryAuth,omitempty"`
	// Command is the argv, executed without a shell. Wrap in ["sh","-c",…] for
	// shell syntax.
	Command        []string `json:"command"`
	EnvKeys        []string `json:"envKeys,omitempty"`
	SecretEnvKeys  []string `json:"secretEnvKeys,omitempty"`
	TimeoutSeconds int      `json:"timeoutSeconds,omitempty"`
	MemoryMB       int      `json:"memoryMb,omitempty"`
	CPUs           float64  `json:"cpus,omitempty"`
	// Network membership of the task container (see network.go): the same
	// values as the service's DeployPayload.
	BlockEgress         bool                      `json:"blockEgress,omitempty"`
	SharedNetworks      []SharedNetworkAttachment `json:"sharedNetworks,omitempty"`
	EnvHasEgressBlocked bool                      `json:"envHasEgressBlocked,omitempty"`
}

// RunTaskResult is ResultFrame.Data of a run_task command. The result outcome
// is "succeeded" only for exit code 0.
type RunTaskResult struct {
	Outcome    string    `json:"outcome"`
	ExitCode   int       `json:"exitCode"`
	TimedOut   bool      `json:"timedOut,omitempty"`
	Message    string    `json:"message,omitempty"`
	StartedAt  time.Time `json:"startedAt,omitempty"`
	FinishedAt time.Time `json:"finishedAt,omitempty"`
}

// ValidTaskCommand reports whether argv is a runnable command: non-empty, no
// empty program name, no NUL bytes, bounded size.
func ValidTaskCommand(argv []string) error {
	if len(argv) == 0 || strings.TrimSpace(argv[0]) == "" {
		return errTaskCommand("the command is empty")
	}
	size := 0
	for _, a := range argv {
		if strings.ContainsRune(a, 0) {
			return errTaskCommand("the command contains a NUL byte")
		}
		size += len(a)
	}
	if len(argv) > 256 || size > 64<<10 {
		return errTaskCommand("the command is too long")
	}
	return nil
}

type errTaskCommand string

func (e errTaskCommand) Error() string { return string(e) }
