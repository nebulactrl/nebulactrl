package agentprotocol

import "slices"

// Per-service tailnet: one Tailscale (or
// Headscale) sidecar per service fronts all of the service's replicas through
// the environment alias. Serve/Funnel and TCP forwards are rendered by the
// agent from ServiceTailnetSpec. The spec never carries a credential: the
// single-use, pre-authorized, tagged auth key travels in Command.Secrets under
// SecretServiceTailnetKey and only when ServiceTailnetSpec.Provision is true. It is
// never persisted by the control plane.
//
// (The per-server egress node uses TailnetSpec/TailnetStatus in
// proxy.go; the two features are independent.)

const (
	// KindSyncTailnet ensures (or re-provisions) the sidecar of one service
	// without deploying the app. Payload: SyncTailnetPayload. The result data is
	// a ServiceTailnetStatus.
	KindSyncTailnet = "sync_tailnet"
	// KindRemoveTailnet removes the sidecar and its volumes. Payload:
	// RemoveTailnetPayload. The result data is a ServiceTailnetStatus (Removed).
	KindRemoveTailnet = "remove_tailnet"
)

func init() {
	AllowedKinds[KindSyncTailnet] = true
	AllowedKinds[KindRemoveTailnet] = true
}

// StepTailnet is the deploy step that ensures the service's tailnet sidecar
// after the app is live. The control plane maps it to `verifying`. It never
// fails the deployment: problems are reported in DeployResult.Tailnet.
const StepTailnet = "tailnet"

// CapServiceTailnet is advertised in Facts.Capabilities by agents that manage
// per-service tailnet sidecars. The control plane blocks deploying a service
// with a tailnet to a server without it (an old agent would ignore the spec).
const CapServiceTailnet = "tailnet.service"

// SecretServiceTailnetKey is the Command.Secrets key of a service sidecar's
// single-use auth key, present only when ServiceTailnetSpec.Provision is true.
// It differs from SecretTailnetAuthKey (the per-server egress node) because a
// deploy command can carry both.
const SecretServiceTailnetKey = "tailnet:service-authkey"

// LabelTailnet marks a service's tailnet sidecar; its value is the service ID.
// The sidecar deliberately does not carry LabelService: it is not a replica and
// the orphan sweep, stats and replica listings must never see it.
const LabelTailnet = "nebulactrl.tailnet"

// LabelTailnetSpec fingerprints the sidecar's non-secret container
// configuration (never the auth key). Exposure changes do not change it: they
// are applied live through the watched serve config.
const LabelTailnetSpec = "nebulactrl.tailnet.spec"

// Tailnet sidecar naming.
const (
	TailnetContainerPrefix = "nc-ts-"
	TailnetVolumePrefix    = "nc-ts-"
)

// TailnetContainerName returns nc-ts-<serviceId8>.
func TailnetContainerName(serviceID string) string { return TailnetContainerPrefix + Short(serviceID) }

// TailnetStateVolumeName returns nc-ts-<serviceId8>-state (the node identity).
func TailnetStateVolumeName(serviceID string) string {
	return TailnetVolumePrefix + Short(serviceID) + "-state"
}

// TailnetConfigVolumeName returns nc-ts-<serviceId8>-config (the serve config).
func TailnetConfigVolumeName(serviceID string) string {
	return TailnetVolumePrefix + Short(serviceID) + "-config"
}

// Tailnet providers (ServiceTailnetSpec.Provider).
const (
	TailnetProviderTailscale = "tailscale"
	TailnetProviderHeadscale = "headscale"
)

// Tailnet modes of ServiceTailnetSpec.
const (
	// TailnetEnsure creates or updates the sidecar to match the spec.
	TailnetEnsure = "ensure"
	// TailnetRemove removes the sidecar and its volumes.
	TailnetRemove = "remove"
)

// Observed/reported states of a service tailnet node (ServiceTailnetStatus.State).
const (
	ServiceTailnetProvisioning = "provisioning"
	ServiceTailnetOnline       = "online"
	ServiceTailnetOffline      = "offline"
	ServiceTailnetNeedsAttn    = "needs_attention"
	ServiceTailnetRemoved      = "removed"
)

// Serve listen ports. Tailscale Serve terminates HTTPS on 443 (Funnel can use
// 443, 8443 and 10000; NebulaCtrl uses 443). Headscale has no Serve HTTPS, so
// the web exposure is plain HTTP on 80.
const (
	TailnetHTTPSPort = 443
	TailnetHTTPPort  = 80
)

// TailnetTCP forwards a tailnet port to a port of the service (raw TCP over
// WireGuard). TLS terminates TLS with the node's certificate (Tailscale only).
type TailnetTCP struct {
	Listen int  `json:"listen"`
	Target int  `json:"target"`
	TLS    bool `json:"tls,omitempty"`
}

// ServiceTailnetSpec is the desired tailnet node of one service. It is part of
// DeployPayload and SyncTailnetPayload and holds no secret.
type ServiceTailnetSpec struct {
	// Mode is TailnetEnsure or TailnetRemove. A remove spec needs only
	// ServiceID and Mode.
	Mode      string `json:"mode"`
	ServiceID string `json:"serviceId"`
	// ServiceSlug is the environment alias the sidecar proxies to.
	ServiceSlug   string `json:"serviceSlug,omitempty"`
	EnvironmentID string `json:"environmentId,omitempty"`
	// BlockEgress is set when the service blocks outbound internet: the app is
	// then only on the environment's internal network, so the sidecar also
	// joins it (keeping the environment network for its own control-plane
	// traffic) to reach the app by its alias.
	BlockEgress bool `json:"blockEgress,omitempty"`

	Provider string `json:"provider,omitempty"`
	// LoginServer is the Headscale control URL (TS_EXTRA_ARGS --login-server).
	LoginServer string `json:"loginServer,omitempty"`
	// Hostname is the tailnet machine name (TS_HOSTNAME).
	Hostname string `json:"hostname,omitempty"`
	// Tags are informational (the auth key carries them); the agent never passes
	// --advertise-tags with a tagged key.
	Tags []string `json:"tags,omitempty"`

	// Web serves the service's HTTP port: HTTPS 443 on Tailscale, plain HTTP 80
	// on Headscale. Port is the service port it proxies to.
	Web  bool `json:"web,omitempty"`
	Port int  `json:"port,omitempty"`
	// TCP are extra raw TCP forwards.
	TCP []TailnetTCP `json:"tcp,omitempty"`
	// Funnel makes the web exposure public through Tailscale's relays.
	Funnel bool `json:"funnel,omitempty"`

	// HidePublic is "Tailnet only": the control plane omits the service's public
	// domains from the route table. The agent records it but never acts on it.
	HidePublic bool `json:"hidePublic,omitempty"`

	// Provision tells the agent to (re)create the node from scratch (remove the
	// container and both volumes) using Secrets[SecretServiceTailnetKey]. False
	// keeps the node's identity and only re-applies exposures.
	Provision bool `json:"provision,omitempty"`
	// IdentityHash fingerprints what makes the device identity (provider,
	// hostname, tags, login server). The control plane re-provisions when it
	// changes. The agent echoes it in its status.
	IdentityHash string `json:"identityHash,omitempty"`
}

// ServiceTailnetStatus is the observed or reported state of a service's node.
type ServiceTailnetStatus struct {
	State    string   `json:"state"`
	Message  string   `json:"message,omitempty"`
	FQDN     string   `json:"fqdn,omitempty"`
	IPs      []string `json:"ips,omitempty"`
	DeviceID string   `json:"deviceId,omitempty"`
	// Removed is true when the sidecar and its volumes were removed.
	Removed bool `json:"removed,omitempty"`
	// Provisioned is true when the node was (re)created by this command, so the
	// auth key was consumed and the previous device is gone from the host.
	Provisioned bool `json:"provisioned,omitempty"`
	// IdentityHash echoes the spec the node was last ensured with.
	IdentityHash string `json:"identityHash,omitempty"`
}

// SyncTailnetPayload is the payload of a sync_tailnet command.
type SyncTailnetPayload struct {
	Tailnet ServiceTailnetSpec `json:"tailnet"`
}

// RemoveTailnetPayload is the payload of a remove_tailnet command.
type RemoveTailnetPayload struct {
	ServiceID string `json:"serviceId"`
}

// ValidTailnetProvider reports whether p is a known provider.
func ValidTailnetProvider(p string) bool {
	return slices.Contains([]string{TailnetProviderTailscale, TailnetProviderHeadscale}, p)
}
