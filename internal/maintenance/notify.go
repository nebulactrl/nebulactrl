package maintenance

import (
	"context"
	"time"

	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/notifications"
	"github.com/nebulactrl/nebulactrl/internal/services"
)

// SetNotifier installs where maintenance changes are announced.
func (s *Service) SetNotifier(n notifications.Emitter) { s.notifier = n }

// notifyChanged announces that a service entered or left maintenance mode.
// Set only reaches it when the state really changed.
func (s *Service) notifyChanged(ctx context.Context, sc services.Scope, in SetInput) {
	if s.notifier == nil {
		return
	}
	ev := notifications.Event{
		ProjectID: &sc.ProjectID, ServiceID: &sc.Service.ID, Severity: notifications.SeverityInfo,
		Fields: map[string]string{},
	}
	if p, ok := auth.PrincipalFrom(ctx); ok && p.Label != "" {
		ev.Fields["Changed by"] = p.Label
	}
	if in.Enabled {
		ev.Type = notifications.TypeMaintenanceOn
		ev.Title = sc.Service.Name + " is in maintenance mode"
		ev.Body = "Visitors see the maintenance page (HTTP 503) until it is switched off."
		if in.ETA != nil {
			ev.Fields["Expected back"] = in.ETA.UTC().Format(time.RFC1123)
		}
	} else {
		ev.Type = notifications.TypeMaintenanceOff
		ev.Title = sc.Service.Name + " is out of maintenance mode"
		ev.Body = "Traffic reaches the service again."
	}
	notifications.Emit(ctx, s.notifier, ev)
}
