package deployments

import (
	"context"
	"strings"

	"github.com/nebulactrl/nebulactrl/internal/notifications"
)

// notifyFinished tells the notification channels how a deployment ended.
// Canceled deployments are the user's own doing and are not announced. It is
// called once per deployment: only the transition into a terminal status
// reaches it. message is the status message of that transition.
func (s *Service) notifyFinished(ctx context.Context, dc depCtx, status Status, message string) {
	if s.Notifier == nil {
		return
	}
	ev := notifications.Event{ProjectID: &dc.ProjectID, ServiceID: &dc.Dep.ServiceID, URL: "?tab=deployments"}
	name := dc.ServiceName
	if name == "" {
		name = dc.ServiceSlug
	}
	switch status {
	case Succeeded:
		ev.Type = notifications.TypeDeploySucceeded
		ev.Title = name + " deployed"
		ev.Body = "The new release is live."
	case RolledBack:
		ev.Type = notifications.TypeDeployRolledBack
		ev.Title = name + " was rolled back"
		ev.Body = "The new release did not pass verification, so the previous release was put back."
	case Failed, RollbackFailed:
		ev.Type = notifications.TypeDeployFailed
		ev.Title = name + " failed to deploy"
		ev.Body = "The deployment failed."
		if status == RollbackFailed {
			ev.Title = name + " failed to deploy and could not be restored"
			ev.Body = "The deployment failed and the previous release could not be put back. The service may be down."
		}
	default:
		return
	}
	// The status message can end with container log lines: send the first line only.
	if line, _, _ := strings.Cut(strings.TrimSpace(message), "\n"); line != "" && status != Succeeded {
		ev.Body = strings.TrimSpace(line) + "\n" + ev.Body
	}
	ev.Fields = map[string]string{"Trigger": dc.Dep.Trigger, "Deployment": dc.Dep.ID.String()[:8]}
	if dc.Dep.CommitSha != "" {
		ev.Fields["Commit"] = dc.Dep.CommitSha[:min(len(dc.Dep.CommitSha), 7)]
	}
	if dc.Dep.CommitBranch != "" {
		ev.Fields["Branch"] = dc.Dep.CommitBranch
	}
	notifications.Emit(ctx, s.Notifier, ev)
}

// notifyPostDeployWarning tells the notification channels that a deployment's
// post-deploy command failed. The deployment itself succeeded, so this is a
// warning on the task.failed type (a command exited with an error), not a
// deployment failure. message is the agent's warning (no command output).
func (s *Service) notifyPostDeployWarning(ctx context.Context, dc depCtx, message string) {
	if s.Notifier == nil {
		return
	}
	name := dc.ServiceName
	if name == "" {
		name = dc.ServiceSlug
	}
	body := "The new release is live, but its post-deploy command did not finish cleanly. Open the deployment's log for the command output."
	if line, _, _ := strings.Cut(strings.TrimSpace(message), "\n"); line != "" {
		body = strings.TrimSpace(line) + "\n" + body
	}
	projectID, serviceID := dc.ProjectID, dc.Dep.ServiceID
	notifications.Emit(ctx, s.Notifier, notifications.Event{
		Type: notifications.TypeTaskFailed, ProjectID: &projectID, ServiceID: &serviceID, ServerID: dc.Dep.ServerID,
		Severity: notifications.SeverityWarning, Title: "Post-deploy command failed on " + name, Body: body, URL: "?tab=deployments",
		Fields: map[string]string{"Task": "post-deploy", "Trigger": dc.Dep.Trigger, "Deployment": dc.Dep.ID.String()[:8]},
	})
}
