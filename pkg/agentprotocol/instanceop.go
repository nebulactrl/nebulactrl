package agentprotocol

// KindInstanceOp is sent to the agent on the control plane's own host (the
// server the installer enrolled, servers.is_control_plane). It runs one
// allow-listed maintenance operation on the instance services that live in the
// control plane's compose project. The operation is a name, never a command
// line: the agent maps it to a fixed argv inside a fixed container.
//
// The command carries no secrets.
const KindInstanceOp = "instance_op"

func init() { AllowedKinds[KindInstanceOp] = true }

// Instance operations (InstanceOpPayload.Op).
const (
	// InstanceOpRegistryGC runs the registry's garbage collector (blobs no
	// manifest references) in the registry container, without deleting untagged
	// manifests. The control plane puts the registry in read-only mode first.
	InstanceOpRegistryGC = "registry_gc"
)

// RegistryContainerName is the container_name of the registry in
// deploy/install/docker-compose.yml; deploy/install/embed_test.go asserts that
// the compose file still names it so.
const RegistryContainerName = "nebulactrl-registry-1"

// InstanceOpPayload is the persisted payload of an instance_op command.
type InstanceOpPayload struct {
	Op string `json:"op"`
}

// InstanceOpResult is the Data of a succeeded instance_op result.
type InstanceOpResult struct {
	Op string `json:"op"`
	// Output is the tail of the operation's output.
	Output string `json:"output,omitempty"`
}
