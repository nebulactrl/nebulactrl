package agentprotocol

import "time"

// Managed tailnet server mesh.
//
// Two kinds of managed containers, none in the host's network namespace:
//
//   - the server node (nc-mesh-<serverId8>), userspace tailscale, one per
//     mesh-enabled server. It is the server's tailnet identity and the ingress:
//     `tailscale serve` TCP forwards MeshPort -> the exposed service.
//   - the link node (nc-meshl-<linkId8>), kernel tailscale in its own netns,
//     one per environment x local server x remote server x purpose. It is the
//     egress: containerboot DNATs everything that reaches it to the remote
//     server node (TS_TAILNET_TARGET_IP), and its aliases on the environment
//     networks are the names consumers dial.
//
// Nothing here carries a credential: the single-use, pre-authorized, tagged
// node key travels in Command.Secrets under SecretMeshKeyPrefix+NodeID, only
// when MeshNodeSpec.Provision is true, and is never persisted.

const (
	// CapMesh is advertised in Facts.Capabilities by agents that manage mesh nodes.
	CapMesh = "mesh"

	// KindSyncMesh applies the COMPLETE desired mesh set of this server
	// (idempotent). Payload: SyncMeshPayload. The result data is a MeshStatus.
	KindSyncMesh = "sync_mesh"
	// KindRemoveMesh removes the named nodes (containers and volumes). Payload:
	// RemoveMeshPayload. The result data is a MeshStatus.
	KindRemoveMesh = "remove_mesh"
	// KindMeshCheck dials a mesh endpoint from the agent and reports whether the
	// whole path works. Payload: MeshCheckPayload. The result data is a MeshCheckResult.
	KindMeshCheck = "mesh_check"
)

func init() {
	AllowedKinds[KindSyncMesh] = true
	AllowedKinds[KindRemoveMesh] = true
	AllowedKinds[KindMeshCheck] = true
}

// SecretMeshKeyPrefix + NodeID is the Command.Secrets key of one node's
// single-use auth key (only present when MeshNodeSpec.Provision is true).
const SecretMeshKeyPrefix = "mesh:key:"

// Labels of mesh containers. Like the tailnet sidecar they deliberately do not
// carry LabelService: they are not replicas and the orphan sweep, stats and
// replica listings must never see them.
const (
	LabelMesh     = "nebulactrl.mesh"      // value: the node id (server id or link id)
	LabelMeshKind = "nebulactrl.mesh.kind" // MeshKindServer | MeshKindLink
	LabelMeshSpec = "nebulactrl.mesh.spec" // fingerprint of the non-secret container configuration, never a key
)

// Mesh node kinds (MeshNodeSpec.Kind, MeshNodeStatus.Kind).
const (
	MeshKindServer = "server"
	MeshKindLink   = "link"
)

// Mesh purposes (mesh_links.purpose, mesh_exposures.purpose, MeshExposure.Purpose).
const (
	// MeshPurposeReplication carries the WAL stream of a read replica.
	MeshPurposeReplication = "replication"
	// MeshPurposeService carries a consumer's traffic to a service on another server.
	MeshPurposeService = "service"
)

// Reported states of a mesh node (MeshNodeStatus.State).
const (
	MeshStateProvisioning = "provisioning"
	MeshStateOnline       = "online"
	MeshStateDegraded     = "degraded"
	MeshStateOffline      = "offline"
	MeshStateNeedsAttn    = "needs_attention"
	MeshStateRemoved      = "removed"
)

// Mesh ports. Replication exposures are allocated from MeshPortMin-MeshPortMax;
// service exposures keep the container port when it is free on the server node.
const (
	MeshPortMin = 15000
	MeshPortMax = 15999
)

// MeshNetwork is the server-wide, non-internal bridge the server node uses for
// its own control-plane and DERP reachability.
const MeshNetwork = "nebulactrl-mesh"

// Mesh naming.
const (
	MeshServerContainerPrefix = "nc-mesh-"
	MeshLinkContainerPrefix   = "nc-meshl-"
	meshServiceAliasPrefix    = "nc-svc-"
	meshLinkAliasPrefix       = "mesh-"
)

// MeshServerContainer returns nc-mesh-<serverId8>.
func MeshServerContainer(serverID string) string { return MeshServerContainerPrefix + Short(serverID) }

// MeshLinkContainer returns nc-meshl-<linkId8>.
func MeshLinkContainer(linkID string) string { return MeshLinkContainerPrefix + Short(linkID) }

// MeshStateVolume returns the node-identity volume of a mesh node (server id or link id).
func MeshStateVolume(nodeID string) string { return "nc-mesh-" + Short(nodeID) + "-state" }

// MeshConfigVolume returns the serve-config volume of a server node.
func MeshConfigVolume(nodeID string) string { return "nc-mesh-" + Short(nodeID) + "-config" }

// MeshReplicationAlias returns the alias a replication link node has on the
// environment networks: mesh-<remoteServerId8>.
func MeshReplicationAlias(remoteServerID string) string {
	return meshLinkAliasPrefix + Short(remoteServerID)
}

// MeshServiceAlias returns nc-svc-<serviceId8>: an extra network alias an
// exposed service's containers carry when deployed after the exposure exists.
// The server node forwards to it when the running container has it, else to the
// container name, so exposing a running service never needs a restart.
func MeshServiceAlias(serviceID string) string { return meshServiceAliasPrefix + Short(serviceID) }

// MeshExposure is one service port reachable on a server node.
type MeshExposure struct {
	ServiceID     string `json:"serviceId"`
	ServiceSlug   string `json:"serviceSlug"`
	EnvironmentID string `json:"environmentId"`
	Purpose       string `json:"purpose"`
	ContainerPort int    `json:"containerPort"`
	// MeshPort is the port on the server node (the tailnet side).
	MeshPort int `json:"meshPort"`
	// BlockEgress is set when the target lives only on the environment's
	// internal network, so the server node must join that network.
	BlockEgress bool `json:"blockEgress,omitempty"`
}

// MeshNodeSpec is the desired state of one mesh node. It holds no secret.
type MeshNodeSpec struct {
	// NodeID is the server id (Kind server) or the link id (Kind link).
	NodeID string `json:"nodeId"`
	Kind   string `json:"kind"`
	// Provider is TailnetProviderTailscale or TailnetProviderHeadscale.
	Provider string `json:"provider"`
	// LoginServer is the Headscale control URL (TS_EXTRA_ARGS --login-server).
	LoginServer string `json:"loginServer,omitempty"`
	// Hostname is the tailnet machine name (TS_HOSTNAME).
	Hostname string `json:"hostname"`
	// Tags are informational (a tagged key carries them); the agent never passes
	// --advertise-tags with a tagged key.
	Tags         []string `json:"tags,omitempty"`
	IdentityHash string   `json:"identityHash,omitempty"`
	// Provision asks the agent to (re)create the node with the key in Secrets.
	Provision bool `json:"provision,omitempty"`

	// Server node: the services it forwards to.
	Exposures []MeshExposure `json:"exposures,omitempty"`

	// Link node.
	EnvironmentID  string `json:"environmentId,omitempty"`
	RemoteServerID string `json:"remoteServerId,omitempty"`
	// TargetIP is the remote server node's tailnet IPv4 (TS_TAILNET_TARGET_IP).
	TargetIP string `json:"targetIp,omitempty"`
	// Aliases are the names the link node answers to on the environment networks:
	// the slugs of the granted service providers, or mesh-<remoteServerId8> for a
	// replication link. Changing them re-attaches the node to the networks.
	Aliases []string `json:"aliases,omitempty"`
	// HasInternalNet is set when the environment has its -int network, which the
	// link node then joins too (consumers that block egress live only there).
	HasInternalNet bool `json:"hasInternalNet,omitempty"`
}

// SyncMeshPayload is the complete desired mesh set of one server. It is also
// carried by Reconcile.Mesh on every hello, so a command that expired never
// leaves a server unconverged.
type SyncMeshPayload struct {
	Nodes []MeshNodeSpec `json:"nodes"`
}

// RemoveMeshPayload names the nodes to remove.
type RemoveMeshPayload struct {
	NodeIDs []string `json:"nodeIds"`
}

// MeshPeer is a mesh peer as seen from a node.
type MeshPeer struct {
	IP        string `json:"ip"`
	Host      string `json:"host,omitempty"`
	Online    bool   `json:"online"`
	Direct    bool   `json:"direct"`
	Relay     string `json:"relay,omitempty"`
	LatencyMs *int   `json:"latencyMs,omitempty"`
}

// MeshNodeStatus is the observed state of one mesh node.
type MeshNodeStatus struct {
	NodeID   string     `json:"nodeId"`
	Kind     string     `json:"kind"`
	State    string     `json:"state"`
	Message  string     `json:"message,omitempty"`
	FQDN     string     `json:"fqdn,omitempty"`
	IPs      []string   `json:"ips,omitempty"`
	DeviceID string     `json:"deviceId,omitempty"`
	Peers    []MeshPeer `json:"peers,omitempty"`
	// TargetReachable is the link node's ping of its target (nil = not checked).
	TargetReachable *bool     `json:"targetReachable,omitempty"`
	CheckedAt       time.Time `json:"checkedAt,omitzero"`
}

// MeshStatus is the observed mesh of one server (heartbeat and command results).
type MeshStatus struct {
	Nodes []MeshNodeStatus `json:"nodes"`
	// TunDevice reports whether /dev/net/tun exists (link nodes need it).
	TunDevice bool `json:"tunDevice"`
}

// MeshCheckPayload asks the agent to dial a mesh endpoint.
type MeshCheckPayload struct {
	// LinkID is the link node whose address on its environment network is dialed.
	LinkID string `json:"linkId,omitempty"`
	// Port is the mesh port to dial on that address.
	Port int `json:"port,omitempty"`
	// PeerNodeID makes the agent report the tailnet reachability of a peer
	// server node instead (read from `tailscale status --json`).
	PeerNodeID string `json:"peerNodeId,omitempty"`
}

// Outcomes of a MeshCheckResult.
const (
	MeshCheckConnected = "connected"
	MeshCheckRefused   = "refused"
	MeshCheckTimeout   = "timeout"
	MeshCheckNoPath    = "no_path"
)

// MeshCheckResult is ResultFrame.Data of mesh_check.
type MeshCheckResult struct {
	Result    string `json:"result"`
	Message   string `json:"message,omitempty"`
	LatencyMs int    `json:"latencyMs,omitempty"`
}
