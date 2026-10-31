package cleanup

import (
	"github.com/nebulactrl/nebulactrl/internal/agent/proxy"
	"github.com/nebulactrl/nebulactrl/internal/agent/tunnel"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// The infrastructure images the agent runs or pulls itself. Cleanup keeps them
// even while no container uses them (the tunnel is off, the Railpack builder
// has not run today): pulling them again is slow and may fail offline. A new
// helper image registers itself with agentprotocol.PinImage next to its
// constant.
func init() {
	for _, ref := range []string{
		proxy.TraefikImage, proxy.PagesImage, proxy.TailscaleImage, tunnel.Image,
		agentprotocol.BuildKitImage, agentprotocol.RailpackFrontendImage,
	} {
		agentprotocol.PinImage(ref)
	}
}
