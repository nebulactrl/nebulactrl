package agentprotocol

import "time"

// Adoption of a server that already runs NebulaCtrl workloads (a lost or
// reinstalled control plane, or a move from another instance). The first hello
// of a newly added server reports the services its managed containers belong
// to (Facts.ManagedServices); the control plane then holds the server
// (Reconcile.KeepUnknown) until a person reviews the workload inventory and
// adopts, leaves (Reconcile.Unmanaged) or removes each service.

// CapAdoption is advertised in Facts.Capabilities by agents that report
// Facts.ManagedServices, honour Reconcile.KeepUnknown and Reconcile.Unmanaged,
// and answer workload_inventory commands.
const CapAdoption = "server.adoption"

// KindWorkloadInventory lists the managed containers, volumes and live routes
// of the host. Ephemeral: the result carries container environment values and
// is never persisted (see EphemeralResultKinds).
const KindWorkloadInventory = "workload_inventory"

func init() {
	AllowedKinds[KindWorkloadInventory] = true
	EphemeralResultKinds[KindWorkloadInventory] = true
}

// WorkloadInventory is the result Data of workload_inventory.
type WorkloadInventory struct {
	Containers []InventoryContainer `json:"containers"`
	// Volumes are the names of the Docker volumes with VolumePrefix.
	Volumes []string `json:"volumes"`
	// Routes is the route table the proxy serves.
	Routes []Route `json:"routes"`
}

// InventoryContainer is one managed service container, as Docker reports it.
type InventoryContainer struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Image       string            `json:"image"`
	ImageID     string            `json:"imageId"`
	State       string            `json:"state"`
	Health      string            `json:"health"`
	RestartMode string            `json:"restartMode"`
	Running     bool              `json:"running"`
	Created     time.Time         `json:"created"`
	Labels      map[string]string `json:"labels"`
	// Env carries the variable values: never log or persist it.
	Env      []string         `json:"env"`
	Cmd      []string         `json:"cmd"`
	Networks []string         `json:"networks"`
	Mounts   []InventoryMount `json:"mounts"`
	// The image's own tags, digests and defaults (Config.Env, Config.Cmd), so
	// the control plane can tell the service's settings from the image's.
	ImageRepoTags    []string `json:"imageRepoTags"`
	ImageRepoDigests []string `json:"imageRepoDigests"`
	ImageEnv         []string `json:"imageEnv"`
	ImageCmd         []string `json:"imageCmd"`
}

// InventoryMount is one mount of an inventory container.
type InventoryMount struct {
	Type        string `json:"type"` // volume | bind | tmpfs
	Name        string `json:"name"` // the volume name of a volume mount
	Source      string `json:"source"`
	Destination string `json:"destination"`
}
