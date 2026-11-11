package adoption

import (
	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// serviceRec is what the apply needs about an adoptable service beyond the
// plan the client sees: the configuration and release rebuilt from the
// containers. It holds container variable values: never log or serialise it.
type serviceRec struct {
	envID      uuid.UUID
	cfg        services.Config
	source     services.ReleaseSource
	imageRef   string
	releaseVar map[string]string // container variables minus the image's own and per-container ones
	procScale  map[string]int    // replicas of the named processes
}

// domainRec is a domain of a service as the proxy served it.
type domainRec struct {
	rule      agentprotocol.DomainRule
	component string
}
