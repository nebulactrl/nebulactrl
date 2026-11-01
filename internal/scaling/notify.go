package scaling

import (
	"context"

	"github.com/nebulactrl/nebulactrl/internal/notifications"
	"github.com/nebulactrl/nebulactrl/internal/services"
)

// SetNotifier installs where blocked scale-outs are announced. Without one
// they only show on the service's node.
func (s *Service) SetNotifier(n notifications.Emitter) { s.notifier = n }

// notifyBlocked announces a scale-out that can not happen. It is called only
// when the stored reason changed (SetServiceScaleBlocked returns a row then),
// so a block is announced once, not on every autoscaler tick. process names the
// process of a service with several processes ("" otherwise).
func (s *Service) notifyBlocked(ctx context.Context, sc services.Scope, process, reason string) {
	if s.notifier == nil {
		return
	}
	title, body := sc.Service.Name+" can not scale out", reason
	if process != "" {
		title = sc.Service.Name + " (" + process + ") can not scale out"
		body = "Process " + process + ": " + reason
	}
	notifications.Emit(ctx, s.notifier, notifications.Event{
		Type: notifications.TypeScaleBlocked, ProjectID: &sc.ProjectID, ServiceID: &sc.Service.ID, Severity: notifications.SeverityWarning,
		Title: title,
		Body:  body,
	})
}
