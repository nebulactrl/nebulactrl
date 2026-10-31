package engine

import "github.com/nebulactrl/nebulactrl/pkg/agentprotocol"

// EndpointApplier starts and stops the local instance service listeners
// (*localproxy.Manager implements it). Apply is idempotent.
type EndpointApplier interface {
	Apply(eps agentprotocol.InstanceEndpoints)
}

// applyEndpoints hands the control plane's endpoint set to the listener
// manager. nil means unchanged (an older control plane, or a frame that does
// not carry it), an empty Listen stops every listener.
func (e *Engine) applyEndpoints(eps *agentprotocol.InstanceEndpoints) {
	if eps == nil || e.cfg.Endpoints == nil {
		return
	}
	e.cfg.Endpoints.Apply(*eps)
}
