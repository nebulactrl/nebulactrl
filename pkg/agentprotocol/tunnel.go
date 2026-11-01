package agentprotocol

// Cloudflare Tunnel per server. A pinned
// cloudflare/cloudflared container joins the edge network next to the proxy and
// forwards every public hostname to the proxy's web entrypoint
// (http://nebulactrl-traefik:80). The tunnel itself is created, configured and
// deleted by the control plane through the Cloudflare API; the agent only runs
// the connector. The spec never carries the tunnel token: it travels in
// Command.Secrets under SecretTunnelToken, is fetched from the Cloudflare API at
// send time and is never persisted by the control plane.

const (
	// KindSyncTunnel ensures the cloudflared container (create, or recreate on a
	// changed spec or tunnel) and waits for the first connection. Payload:
	// SyncTunnelPayload. Secret: SecretTunnelToken. The result data is a
	// TunnelStatus.
	KindSyncTunnel = "sync_tunnel"
	// KindRemoveTunnel removes the cloudflared container. Payload:
	// RemoveTunnelPayload. The result data is a TunnelStatus (Removed).
	KindRemoveTunnel = "remove_tunnel"
)

func init() {
	AllowedKinds[KindSyncTunnel] = true
	AllowedKinds[KindRemoveTunnel] = true
}

// CapTunnel is advertised in Facts.Capabilities by agents that manage the
// cloudflared connector. The control plane refuses to enable a tunnel (422
// agent_too_old) on a server without it.
const CapTunnel = "tunnel.cloudflared"

// SecretTunnelToken is the Command.Secrets key of the tunnel's connector token
// (the remotely-managed tunnel token of the Cloudflare API).
const SecretTunnelToken = "tunnel:token"

// Well-known names on a host.
const (
	// TunnelContainer is the cloudflared container.
	TunnelContainer = "nebulactrl-cloudflared"
	// TunnelUpstream is where the tunnel's ingress rules send every request:
	// the proxy's plain-HTTP entrypoint on the edge network.
	TunnelUpstream = "http://" + ProxyContainer + ":80"
	// TunnelCNAMESuffix completes a tunnel's DNS target: <tunnelId>.cfargotunnel.com.
	TunnelCNAMESuffix = ".cfargotunnel.com"
	// LabelTunnel carries the tunnel ID the container connects to.
	LabelTunnel = "nebulactrl.tunnel"
	// LabelTunnelSpec fingerprints the container's non-secret configuration
	// (image, command, networks); never the token.
	LabelTunnelSpec = "nebulactrl.tunnel.spec"
	// TunnelMetricsPort is the port cloudflared serves /ready on (the edge
	// network only; nothing is published on the host).
	TunnelMetricsPort = 20241
)

// Tunnel states (TunnelStatus.State).
const (
	TunnelStateProvisioning = "provisioning" // container created or restarting, no connection yet
	TunnelStateOnline       = "online"       // at least one connection to Cloudflare's edge
	TunnelStateOffline      = "offline"      // container stopped or missing
	TunnelStateNeedsAttn    = "needs_attention"
	TunnelStateRemoved      = "removed" // result of remove_tunnel only
)

// TunnelSpec is the desired connector. The container is recreated when the
// tunnel ID changes.
type TunnelSpec struct {
	// TunnelID is the Cloudflare tunnel UUID.
	TunnelID string `json:"tunnelId"`
	// Name is the tunnel's display name (logs only).
	Name string `json:"name,omitempty"`
}

// SyncTunnelPayload is the payload of a sync_tunnel command.
type SyncTunnelPayload struct {
	Tunnel TunnelSpec `json:"tunnel"`
}

// RemoveTunnelPayload is the payload of a remove_tunnel command.
type RemoveTunnelPayload struct {
	TunnelID string `json:"tunnelId,omitempty"`
}

// TunnelStatus is the observed connector state, reported in heartbeats and as
// the result data of sync_tunnel and remove_tunnel. A nil Heartbeat.Tunnel means
// the host has no cloudflared container.
type TunnelStatus struct {
	State    string `json:"state" enum:"provisioning,online,offline,needs_attention,removed"`
	TunnelID string `json:"tunnelId,omitempty"`
	// Connections is the number of ready connections to Cloudflare's edge
	// (0 when /ready could not be read).
	Connections int `json:"connections"`
	// Image is the image reference the container runs.
	Image   string `json:"image,omitempty"`
	Message string `json:"message,omitempty"`
}
