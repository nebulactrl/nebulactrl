package engine

import "github.com/nebulactrl/nebulactrl/pkg/agentprotocol"

// GeoApplier keeps the GeoIP database in step with the server's geolocation
// setting. The MaxMind credentials arrive in secrets (never persisted).
type GeoApplier interface {
	Apply(cfg agentprotocol.GeoConfig, secrets map[string]string)
}

// applyGeo hands the reconcile frame's geolocation setting to the manager. A
// nil setting leaves the current one unchanged.
func (e *Engine) applyGeo(cfg *agentprotocol.GeoConfig, secrets map[string]string) {
	if cfg == nil || e.cfg.Geo == nil {
		return
	}
	e.cfg.Geo.Apply(*cfg, secrets)
}
