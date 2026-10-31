// Package previews runs pull-request previews: a real environment cloned from
// the project's base environment for every open PR, deployed with the PR's
// head commit, paused when too many run at once, and deleted when the PR is
// closed or has been idle for too long.
package previews

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Preview statuses.
const (
	StatusCreating  = "creating"
	StatusDeploying = "deploying"
	StatusSeeding   = "seeding"
	StatusReady     = "ready"
	StatusFailed    = "failed"
	StatusPaused    = "paused"
	StatusDeleting  = "deleting"
)

// Defaults of the per-project settings.
const (
	DefaultMaxConcurrent = 5
	DefaultIdleDays      = 7
)

// Seed command triggers.
const (
	RunOnCreate = "create"
	RunOnPush   = "push"
)

// PR actions of a PullRequest.
const (
	ActionOpened      = "opened"
	ActionSynchronize = "synchronize"
	ActionReopened    = "reopened"
	ActionClosed      = "closed"
	ActionMerged      = "merged"
)

// CommitContext is the commit status context of the aggregate preview status.
const CommitContext = "nebulactrl/preview"

// PullRequest is a verified pull-request event of a git provider. It mirrors
// gitproviders.PullRequestEvent; the app wiring converts.
type PullRequest struct {
	ConnectionID uuid.UUID `json:"connectionId"`
	Provider     string    `json:"provider"`
	// Repo is the base repository's full name ("owner/name").
	Repo string `json:"repo"`
	// Action is opened, synchronize, reopened, closed or merged.
	Action     string `json:"action"`
	Number     int    `json:"number"`
	Title      string `json:"title"`
	Author     string `json:"author"`
	HeadBranch string `json:"headBranch"`
	HeadSHA    string `json:"headSha"`
	BaseBranch string `json:"baseBranch"`
	// HeadCloneURL is the public clone URL of the head repository (the fork for
	// fork PRs).
	HeadCloneURL string `json:"headCloneUrl"`
	HTMLURL      string `json:"htmlUrl"`
	FromFork     bool   `json:"fromFork"`
}

// CommitStatus is a commit status to report on the PR's head commit.
type CommitStatus struct {
	State       string // pending | success | failure | error
	TargetURL   string
	Description string
	Context     string
}

// Git is what previews need from the git providers.
type Git interface {
	// ServicesForRepo lists the services connected to repo through connectionID.
	ServicesForRepo(ctx context.Context, connectionID uuid.UUID, repo string) ([]uuid.UUID, error)
	// CopyServiceGitTx links to the same connection and repository as from (no
	// auto deploy). It is a no-op when from has no link.
	CopyServiceGitTx(ctx context.Context, tx pgx.Tx, from, to uuid.UUID) error
	SetCommitStatus(ctx context.Context, connectionID uuid.UUID, repo, sha string, st CommitStatus) error
	// UpsertPRComment creates or updates the one comment of the PR whose body
	// contains marker.
	UpsertPRComment(ctx context.Context, connectionID uuid.UUID, repo string, number int, marker, body string) error
}

// Commit is the pull request's head commit, shown on the deployments of a
// preview. SHA is empty for services that do not build the PR's repository.
type Commit struct {
	SHA, Message, Author, Branch string
}

// Deployer starts deployments on behalf of the control plane.
type Deployer interface {
	// Deploy deploys the service's current configuration with trigger
	// `preview`. A non-empty commit.SHA pins a git service to that commit. A
	// running deployment of the service yields deployment_in_progress.
	Deploy(ctx context.Context, serviceID uuid.UUID, commit Commit, message string) (uuid.UUID, error)
	// Cancel cancels a deployment the agent has not started yet.
	Cancel(ctx context.Context, deploymentID uuid.UUID) error
}

// Seeder runs one-off commands (Tasks).
type Seeder interface {
	// RunSeed queues command (an argv) as a one-off run in the service's
	// deployed container image, inside tx.
	RunSeed(ctx context.Context, tx pgx.Tx, serviceID uuid.UUID, command []string) (uuid.UUID, error)
	// RunStatus reports a run: queued | running | succeeded | failed | timed_out.
	RunStatus(ctx context.Context, runID uuid.UUID) (status string, exitCode *int, message string, err error)
}

// PreviewSeedCommand is a one-off command run in an app service of the preview after
// its deployment succeeded, typically a migration or seed script against the
// preview's fresh database.
type PreviewSeedCommand struct {
	// Service is the slug of the service (a database consumer) that runs it.
	Service string `json:"service" minLength:"1" maxLength:"63"`
	// Command is a shell command line, run with `sh -c`.
	Command string `json:"command" minLength:"1" maxLength:"2000"`
	// RunOn is create (first deployment only, default) or push (every push).
	RunOn string `json:"runOn" enum:"create,push" default:"create"`
}

// PreviewSettings are a project's preview settings.
type PreviewSettings struct {
	ProjectID         uuid.UUID            `json:"projectId"`
	Enabled           bool                 `json:"enabled"`
	BaseEnvironmentID *uuid.UUID           `json:"baseEnvironmentId" nullable:"true"`
	ForkPRs           bool                 `json:"forkPrs"`
	MaxConcurrent     int                  `json:"maxConcurrent"`
	IdleDays          int                  `json:"idleDays"`
	SeedCommands      []PreviewSeedCommand `json:"seedCommands"`
	ExcludedServices  []string             `json:"excludedServices"`
	UpdatedAt         *time.Time           `json:"updatedAt"`
}

// PreviewView is the API view of a preview.
type PreviewView struct {
	ID              uuid.UUID  `json:"id"`
	ProjectID       uuid.UUID  `json:"projectId"`
	EnvironmentID   uuid.UUID  `json:"environmentId"`
	EnvironmentSlug string     `json:"environmentSlug"`
	Status          string     `json:"status" enum:"creating,deploying,seeding,ready,failed,paused,deleting"`
	StatusMessage   string     `json:"statusMessage"`
	Provider        string     `json:"provider"`
	Repo            string     `json:"repo"`
	PRNumber        int        `json:"prNumber"`
	PRTitle         string     `json:"prTitle"`
	PRAuthor        string     `json:"prAuthor"`
	PRURL           string     `json:"prUrl"`
	HeadBranch      string     `json:"headBranch"`
	HeadSHA         string     `json:"headSha"`
	FromFork        bool       `json:"fromFork"`
	URLs            []string   `json:"urls" nullable:"false"`
	CreatedAt       time.Time  `json:"createdAt"`
	LastPushAt      time.Time  `json:"lastPushAt"`
	PausedAt        *time.Time `json:"pausedAt"`
}
