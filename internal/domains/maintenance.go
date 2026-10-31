package domains

import (
	"time"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// maintenanceFrom is the route flag of a service's maintenance settings: nil
// while maintenance is off.
func maintenanceFrom(enabled bool, title, message string, eta *time.Time) *agentprotocol.Maintenance {
	if !enabled {
		return nil
	}
	return &agentprotocol.Maintenance{Title: title, Message: message, ETA: eta}
}

// MaintenanceOf is the route flag of a service row (nil while off).
func MaintenanceOf(s db.Service) *agentprotocol.Maintenance {
	return maintenanceFrom(s.MaintenanceEnabled, s.MaintenanceTitle, s.MaintenanceMessage, s.MaintenanceEta)
}
