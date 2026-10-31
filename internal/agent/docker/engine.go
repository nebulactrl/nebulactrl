// Package docker defines the agent's view of the Docker daemon.
//
// Everything above this package (proxy manager, deploy engine, reconciler)
// depends on the Engine interface and the plain types declared here, never on
// the moby SDK, so tests can substitute a fake. SDKEngine is the production
// implementation.
package docker

import (
	"context"
	"errors"
	"io"
	"strings"
	"time"
)

// ErrNotFound is returned (wrapped) by Engine methods when the referenced
// container, image, network or volume does not exist. Test with errors.Is.
var ErrNotFound = errors.New("docker: not found")

// IsPoolOverlap reports whether err is the daemon refusing a network because
// its subnet overlaps another network or a host route ("Pool overlaps with
// other one on this address space").
func IsPoolOverlap(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "overlap")
}

// Info is the subset of daemon information the agent reports.
type Info struct {
	ServerVersion string
	CgroupVersion string // "1" or "2"
	KernelVersion string
	NCPU          int
	MemTotal      uint64
}

// PortPublish publishes a container port on the host.
type PortPublish struct {
	HostIP        string // empty = all interfaces
	HostPort      int
	ContainerPort int
	Proto         string // "tcp" (default) or "udp"
}

// VolumeMount mounts a named volume (or, with HostPath, a host directory) into
// a container.
type VolumeMount struct {
	Volume string
	// HostPath, when set, bind-mounts this host directory instead of Volume.
	HostPath string
	Target   string
	ReadOnly bool
}

// MountInfo is one mount of an inspected container.
type MountInfo struct {
	Type        string // "volume", "bind", "tmpfs"
	Name        string // volume name; empty for binds
	Source      string // host path (binds) or the volume's storage path
	Destination string
	ReadOnly    bool
}

// NetworkAttachment joins a container to a network with optional DNS aliases.
type NetworkAttachment struct {
	Network string
	Aliases []string
	// GwPriority makes the network provide the container's default gateway
	// over lower values (0, the default, leaves it to Docker). It needs Engine
	// API 1.48; the SDK engine drops it on an older daemon.
	GwPriority int
}

// ContainerSpec describes a container to create. The first entry in Networks
// becomes the container's primary network.
type ContainerSpec struct {
	Name  string
	Image string
	Env   []string // KEY=VALUE
	Cmd   []string
	// Entrypoint, when set, replaces the image's entrypoint (and drops its
	// default command): task containers run argv directly, like docker exec.
	Entrypoint    []string
	Labels        map[string]string
	Networks      []NetworkAttachment
	Ports         []PortPublish
	Mounts        []VolumeMount
	RestartPolicy string // e.g. "unless-stopped"; empty = no restart
	MemoryBytes   int64
	NanoCPUs      int64
	StopTimeout   *int // seconds; nil = daemon default
	// LogConfig sets the container's log driver and options (e.g. json-file
	// with max-size/max-file). Nil keeps the daemon default.
	LogConfig *LogConfig
	// Healthcheck installs a container-level HEALTHCHECK (exec checks); Docker
	// runs it inside the container and reports Health in ContainerInfo.
	Healthcheck *HealthSpec
	// ExtraHosts are "name:ip" entries for /etc/hosts ("name:host-gateway" resolves to the Docker host).
	ExtraHosts []string
	// DNS are resolver addresses written to the container's resolv.conf.
	DNS []string
	// CapAdd / CapDrop adjust the container's Linux capabilities.
	CapAdd  []string
	CapDrop []string
	// Devices maps host device nodes into the container.
	Devices []DeviceMapping
	// GPUs requests GPUs through the NVIDIA Container Toolkit (nil = none).
	GPUs *GPURequest
	// Runtime selects an OCI runtime configured in the daemon (empty = default).
	Runtime string
	// Privileged gives the container all capabilities and devices (host root).
	Privileged bool
	// PidMode "host" shares the host's PID namespace (empty = private).
	PidMode string
	// Hostname sets the container's hostname (empty = Docker's default, the container ID).
	Hostname string
	// User runs the container as uid[:gid] (empty = the image's user).
	User string
	// GroupAdd adds supplementary group IDs.
	GroupAdd []string
	// Sysctls are namespaced kernel parameters of the container.
	Sysctls map[string]string
	// PidsLimit caps the processes and threads (0 = no limit).
	PidsLimit int64
	// NetworkMode, when set, replaces the primary network (e.g. "container:<name>"
	// shares another container's network namespace). Networks must then be empty.
	NetworkMode string
	SecurityOpt []string
	// ShmSize is the size of /dev/shm in bytes (0 = daemon default).
	ShmSize int64
	// Ulimits are resource limits (nofile, memlock; nproc for internal containers).
	Ulimits []Ulimit
	// Init runs a minimal init as PID 1.
	Init bool
	// StopSignal overrides the image's stop signal (for example "SIGQUIT").
	StopSignal string
	// Tmpfs maps a container path to its tmpfs options ("size=64m,mode=1777").
	Tmpfs map[string]string
	// ReadonlyRootfs mounts the root filesystem read-only (writable paths come
	// from Mounts and Tmpfs).
	ReadonlyRootfs bool
}

// DeviceMapping maps a host device node into the container.
type DeviceMapping struct {
	HostPath      string
	ContainerPath string // empty = HostPath
	Permissions   string // subset of "rwm"; empty = "rwm"
}

// GPURequest is a --gpus request: Count GPUs (-1 = all) of Driver with the
// driver Capabilities (the "gpu" capability is always requested).
type GPURequest struct {
	Count        int
	Driver       string
	Capabilities []string
}

// Ulimit is one resource limit; -1 means unlimited.
type Ulimit struct {
	Name       string
	Soft, Hard int64
}

// HealthSpec is a container HEALTHCHECK. Test starts with CMD or CMD-SHELL.
type HealthSpec struct {
	Test        []string
	Interval    time.Duration
	Timeout     time.Duration
	StartPeriod time.Duration
	Retries     int
}

// LogConfig is a container log driver configuration.
type LogConfig struct {
	Type   string
	Config map[string]string
}

// ContainerInfo is the inspected state of a container.
type ContainerInfo struct {
	ID           string
	Name         string // without leading slash
	Image        string // image reference the container was created with
	ImageID      string
	Labels       map[string]string
	Env          []string
	Cmd          []string
	State        string // created, running, paused, restarting, removing, exited, dead
	Running      bool
	Restarting   bool
	OOMKilled    bool
	ExitCode     int
	Health       string // "" when the image has no healthcheck, else starting|healthy|unhealthy
	RestartCount int
	Created      time.Time
	StartedAt    time.Time
	FinishedAt   time.Time
	Ports        []PortPublish // published ports from the host config
	RestartMode  string
	LogConfig    LogConfig
	Networks     map[string]ContainerNetwork
	// NetworkMode, ExtraHosts and DNS echo the host config (spec matching).
	NetworkMode string
	ExtraHosts  []string
	DNS         []string
	// Mounts are the container's volume and bind mounts.
	Mounts []MountInfo
}

// ContainerNetwork is a container's endpoint on one network.
type ContainerNetwork struct {
	IP      string
	Aliases []string
}

// NetworkInfo is a network and its members.
type NetworkInfo struct {
	ID         string
	Name       string
	Internal   bool
	Labels     map[string]string
	Created    time.Time
	Containers []string // IDs of the connected containers
	// Subnets are the network's IPAM subnets in CIDR form.
	Subnets []string
}

// NetworkOptions are the optional settings of EnsureNetwork and
// EnsureInternalNetwork.
type NetworkOptions struct {
	// Subnet, when set, is the IPAM subnet (CIDR) of a newly created network;
	// otherwise the daemon picks one from its default address pools.
	Subnet string
}

// NetworkOption customises a network that is about to be created.
type NetworkOption func(*NetworkOptions)

// WithSubnet creates the network with the given IPAM subnet (CIDR).
func WithSubnet(cidr string) NetworkOption {
	return func(o *NetworkOptions) { o.Subnet = cidr }
}

// ResolveNetworkOptions applies opts to a zero NetworkOptions.
func ResolveNetworkOptions(opts ...NetworkOption) NetworkOptions {
	var o NetworkOptions
	for _, fn := range opts {
		fn(&o)
	}
	return o
}

// ListFilter selects containers. Labels are exact "key=value" matches, or
// "key" alone when the value is empty.
type ListFilter struct {
	All    bool
	Labels map[string]string
	Names  []string
}

// ImageInfo is the inspected state of a local image.
type ImageInfo struct {
	ID             string // content-addressable image ID (sha256:...)
	RepoTags       []string
	RepoDigests    []string
	HasHealthcheck bool
	Entrypoint     []string
	Labels         map[string]string
	Created        time.Time
	// LastTagged is when the image was last tagged, which a pull or a build
	// does; zero for an untagged image. Cleanup ages an image by the later of
	// Created and LastTagged, because a pulled image keeps its build date.
	LastTagged time.Time
}

// ImageSummary is one entry of an image listing.
type ImageSummary struct {
	ID          string
	RepoTags    []string
	RepoDigests []string
	Created     time.Time
	// Size is the image's size on disk in bytes (shared layers included).
	Size int64
}

// ContainerStats is one resource-usage sample of a running container.
type ContainerStats struct {
	CPUPercent float64 // percent of one CPU, docker stats semantics (may exceed 100)
	MemBytes   uint64  // working set: usage minus inactive file cache
	MemLimit   uint64
}

// LogOptions selects container log output.
type LogOptions struct {
	// Tail is the number of trailing lines to return; negative means all.
	Tail   int
	Follow bool
	// Since, when non-zero, returns only lines at or after that time.
	Since time.Time
	// StdoutOnly drops the container's stderr stream.
	StdoutOnly bool
}

// Engine is the Docker daemon surface used by the agent. Unless noted, methods
// wrap ErrNotFound when the target does not exist.
type Engine interface {
	Ping(ctx context.Context) error
	Info(ctx context.Context) (Info, error)
	ServerVersion(ctx context.Context) (string, error)

	// EnsureNetwork creates a bridge network if it does not exist. Options
	// only apply to a network that is created; an existing network of that name
	// is left as it is, whatever its subnet.
	EnsureNetwork(ctx context.Context, name string, labels map[string]string, opts ...NetworkOption) error
	// EnsureInternalNetwork is EnsureNetwork for a bridge network with no route
	// to the outside (Docker's "internal": true): members reach each other but
	// not the internet. An existing network of that name is left as it is.
	EnsureInternalNetwork(ctx context.Context, name string, labels map[string]string, opts ...NetworkOption) error
	// NetworkDisconnect removes a container from a network. A container that is
	// not connected, or a network that does not exist, is not an error.
	NetworkDisconnect(ctx context.Context, network, container string) error
	// NetworkList lists networks whose name starts with prefix (empty = all),
	// with their member container IDs and IPAM subnets. Only networks carrying
	// every label in labels are returned ("key=value" exact matches).
	NetworkList(ctx context.Context, prefix string, labels map[string]string) ([]NetworkInfo, error)
	// NetworkRemove deletes a network; ErrNotFound when it does not exist, an
	// error when containers are still connected.
	NetworkRemove(ctx context.Context, name string) error
	// EnsureVolume creates a named volume if it does not exist.
	EnsureVolume(ctx context.Context, name string, labels map[string]string) error
	RemoveVolume(ctx context.Context, name string, force bool) error
	NetworkConnect(ctx context.Context, network, container string, aliases []string) error

	// ImagePull pulls ref and blocks until the pull completes. registryAuth is
	// the base64-encoded auth JSON expected by the daemon (may be empty).
	// progress, when non-nil, receives human-readable status lines.
	ImagePull(ctx context.Context, ref, registryAuth string, progress func(line string)) error
	ImageInspect(ctx context.Context, ref string) (ImageInfo, error)
	ImageRemove(ctx context.Context, ref string, force bool) error

	ContainerCreate(ctx context.Context, spec ContainerSpec) (id string, err error)
	ContainerStart(ctx context.Context, id string) error
	// ContainerStop stops the container, waiting timeout (nil = daemon/container default).
	ContainerStop(ctx context.Context, id string, timeout *time.Duration) error
	ContainerRemove(ctx context.Context, id string, force, removeVolumes bool) error
	ContainerInspect(ctx context.Context, idOrName string) (ContainerInfo, error)
	ContainerList(ctx context.Context, f ListFilter) ([]ContainerInfo, error)

	// ContainerLogs returns the container's demultiplexed stdout+stderr as one
	// stream of lines, each prefixed with an RFC 3339 nano timestamp and a
	// space. With Follow the stream stays open until ctx ends or the container
	// stops. The caller must Close the reader.
	ContainerLogs(ctx context.Context, id string, opts LogOptions) (io.ReadCloser, error)
	// ContainerStats takes a single resource-usage sample (may block ~1s).
	ContainerStats(ctx context.Context, id string) (ContainerStats, error)
	// ImageList lists images whose reference matches the filter (a repository
	// name such as "nc-build/web", or a full reference).
	ImageList(ctx context.Context, reference string) ([]ImageSummary, error)
	// CopyToContainer extracts the tar archive in content into dir inside the
	// container. It works on created (not yet started) containers, so files can
	// be in place before the first start.
	CopyToContainer(ctx context.Context, id, dir string, content io.Reader) error
	// ContainerExec runs argv (no shell) inside a running container and
	// demultiplexes its output into stdout and stderr (nil discards). It
	// blocks until the process exits and returns its exit code; the error is
	// non-nil only when the exec could not run or the stream broke. A non-zero
	// exit code is NOT an error, so callers must check it.
	ContainerExec(ctx context.Context, id string, argv []string, spec ExecSpec, stdout, stderr io.Writer) (exitCode int, err error)
	// ContainerPause freezes every process of the container (cgroup freezer);
	// connections stay open and nothing is reset. A paused container still
	// reports Running=true: callers must look at State == "paused".
	ContainerPause(ctx context.Context, id string) error
	// ContainerUnpause thaws a paused container.
	ContainerUnpause(ctx context.Context, id string) error
}

// ExecSpec configures ContainerExec.
type ExecSpec struct {
	// Env is KEY=VALUE pairs for the exec process only (not the container).
	// Use it to pass credentials without putting them in argv.
	Env  []string
	User string
	// Stdin, when non-nil, is copied to the process and then closed.
	Stdin io.Reader
}
