package agentprotocol

import (
	"fmt"
	"net/netip"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// DockerOptions are the typed container options of a service (release config,
// so staged like every setting). There is no free-text flag parser: only what is
// listed here can reach Docker, and never host networking on a plain service,
// ip/ip6, restart or log options, ipc host, host binds or the Docker socket.
// The control plane validates on every write and the agent re-validates before
// it creates a container, against the policy of the server (DockerPolicy).
//
// Two tiers: most fields work on every server. The fields marked "unrestricted
// servers only" (see NeedsUnrestricted) hand a container the host's devices,
// GPUs, a runtime or host-root equivalents, so they work only on a server whose
// instance admin switched "Unrestricted" on. Nobody below instance admin can
// gain host root otherwise.
type DockerOptions struct {
	// CapAdd adds Linux capabilities from SafeCapabilities. NET_ADMIN and
	// SYS_ADMIN (UnrestrictedCapabilities) need an unrestricted server.
	CapAdd []string `json:"capAdd,omitempty"`
	// CapDrop removes capabilities ("ALL" or names), applied before CapAdd:
	// drop ALL and add back only what the application needs.
	CapDrop []string `json:"capDrop,omitempty"`
	// NoNewPrivileges sets the no-new-privileges security option (setuid
	// helpers such as sudo stop working).
	NoNewPrivileges bool `json:"noNewPrivileges,omitempty"`
	// ReadOnlyRootfs mounts the root filesystem read-only; writable paths are
	// volumes and Tmpfs. Config files cannot be written to a read-only root, so
	// every file must sit on a volume (see ValidateFiles).
	ReadOnlyRootfs bool `json:"readOnlyRootfs,omitempty"`
	// PidsLimit caps the processes and threads of the container (cgroup). It
	// is the real per-container limit: the nproc ulimit is per user ID and not
	// enforced for root.
	PidsLimit int64 `json:"pidsLimit,omitempty"`
	// Hostname is the container's hostname (an RFC 1123 label). Empty means the
	// service slug, so it survives every deploy. With several replicas every
	// replica gets "<hostname>-<replica index>" (the slug likewise), so the
	// replicas stay distinct; the name is cut to keep the result one DNS label.
	Hostname string `json:"hostname,omitempty"`
	// DNS lists resolver IP addresses for the container (at most 3).
	DNS []string `json:"dns,omitempty"`
	// Sysctls sets namespaced kernel parameters from a curated list
	// (AllowedSysctls): IPC and a few network parameters. Host-wide sysctls
	// cannot be set from a container; see HostSysctls.
	Sysctls map[string]string `json:"sysctls,omitempty"`
	// HostSysctls are kernel parameters the application needs on the HOST (for
	// example vm.max_map_count for Elasticsearch), as minimum values. Nothing
	// applies them: the plan warns, with the command to run, when the server's
	// value is lower.
	HostSysctls map[string]int64 `json:"hostSysctls,omitempty"`
	// User runs the container as uid[:gid] (numbers or names).
	User string `json:"user,omitempty"`
	// GroupAdd adds supplementary numeric group IDs (for example the host's
	// render group for a device).
	GroupAdd []string `json:"groupAdd,omitempty"`
	// Ulimits sets nofile and memlock limits.
	Ulimits []Ulimit `json:"ulimits,omitempty"`
	// ShmSizeMB is the size of /dev/shm in MiB (Docker default: 64).
	ShmSizeMB int `json:"shmSizeMb,omitempty"`
	// ExtraHosts are /etc/hosts entries, "name:ip" or "name:host-gateway".
	ExtraHosts []string `json:"extraHosts,omitempty"`
	// Labels are added to the container. com.docker.*, nebulactrl.* and
	// traefik.* are reserved.
	Labels map[string]string `json:"labels,omitempty"`
	// Init runs a minimal init (tini) as PID 1 that reaps zombies and forwards signals.
	Init bool `json:"init,omitempty"`
	// StopSignal replaces the image's stop signal (for example SIGQUIT for nginx).
	StopSignal string `json:"stopSignal,omitempty"`
	// Tmpfs mounts in-memory filesystems.
	Tmpfs []TmpfsMount `json:"tmpfs,omitempty"`
	// Entrypoint replaces the image's ENTRYPOINT (the start command stays the
	// container's CMD and is passed to it). It is an exec-form argv: no shell
	// is involved unless the first element is one.
	Entrypoint []string `json:"entrypoint,omitempty"`

	// Unrestricted servers only from here on.

	// Devices maps host device nodes into the container (--device).
	Devices []DeviceMapping `json:"devices,omitempty"`
	// GPUs requests GPUs through the NVIDIA Container Toolkit (--gpus).
	GPUs *GPURequest `json:"gpus,omitempty"`
	// Runtime selects an OCI runtime configured in the daemon (--runtime), for
	// example nvidia, runsc or sysbox-runc.
	Runtime string `json:"runtime,omitempty"`
	// Privileged gives the container all capabilities, all devices and no
	// seccomp or AppArmor profile (--privileged): host root.
	Privileged bool `json:"privileged,omitempty"`
	// PidHost shares the host's PID namespace (--pid host).
	PidHost bool `json:"pidHost,omitempty"`
	// SeccompUnconfined removes the default syscall filter.
	SeccompUnconfined bool `json:"seccompUnconfined,omitempty"`
	// ApparmorUnconfined removes the default AppArmor profile.
	ApparmorUnconfined bool `json:"apparmorUnconfined,omitempty"`
}

// DockerPolicy is the server policy the control plane carries next to the
// options (DeployPayload.DockerPolicy). The agent re-validates every option
// against it, so a stale or forged payload cannot bypass the gate. A missing
// policy is a restricted server.
type DockerPolicy struct {
	// Unrestricted is the server's "Unrestricted" switch (instance admin only).
	Unrestricted bool `json:"unrestricted"`
}

// Ulimit is one resource limit. Soft and Hard may be -1 (unlimited).
type Ulimit struct {
	Name string `json:"name" enum:"nofile,memlock"`
	Soft int64  `json:"soft"`
	Hard int64  `json:"hard"`
}

// TmpfsMount is an in-memory filesystem at Path.
type TmpfsMount struct {
	Path   string `json:"path"`
	SizeMB int    `json:"sizeMb,omitempty"` // default 64
	Mode   string `json:"mode,omitempty"`   // octal, for example 1777
}

// DeviceMapping maps a host device node into the container.
type DeviceMapping struct {
	// HostPath is the device node on the host, under /dev (for example /dev/dri/renderD128).
	HostPath string `json:"hostPath"`
	// ContainerPath is where it appears; empty means the same path.
	ContainerPath string `json:"containerPath,omitempty"`
	// Permissions is a subset of "rwm" (read, write, mknod); empty means rwm.
	Permissions string `json:"permissions,omitempty"`
}

// GPURequest asks for GPUs.
type GPURequest struct {
	// Count is the number of GPUs, or -1 for all.
	Count int `json:"count"`
	// Driver is the GPU driver; only nvidia.
	Driver string `json:"driver" enum:"nvidia"`
	// Capabilities narrows the driver capabilities (compute, utility, video,
	// graphics, display, compat32); empty means the toolkit's default.
	Capabilities []string `json:"capabilities,omitempty"`
}

// SafeCapabilities may always be added. Docker already grants most of the
// default set; they are listed so a service can say so explicitly after a
// CapDrop or on a hardened daemon.
var SafeCapabilities = []string{
	"AUDIT_WRITE", "CHOWN", "DAC_OVERRIDE", "FOWNER", "FSETID", "IPC_LOCK", "KILL", "MKNOD",
	"NET_BIND_SERVICE", "NET_RAW", "SETFCAP", "SETGID", "SETPCAP", "SETUID", "SYS_CHROOT",
	"SYS_NICE", "SYS_PTRACE",
}

// WarnCapabilities are safe-listed but deserve a warning in the UI and plan.
var WarnCapabilities = []string{"SYS_PTRACE"}

// UnrestrictedCapabilities need an unrestricted server. Both widen the
// container's reach over the host network stack or kernel.
var UnrestrictedCapabilities = []string{"NET_ADMIN", "SYS_ADMIN"}

// KnownCapabilities are the Linux capabilities CapDrop may name (besides ALL).
var KnownCapabilities = []string{
	"AUDIT_CONTROL", "AUDIT_READ", "AUDIT_WRITE", "BLOCK_SUSPEND", "BPF", "CHECKPOINT_RESTORE", "CHOWN",
	"DAC_OVERRIDE", "DAC_READ_SEARCH", "FOWNER", "FSETID", "IPC_LOCK", "IPC_OWNER", "KILL", "LEASE",
	"LINUX_IMMUTABLE", "MAC_ADMIN", "MAC_OVERRIDE", "MKNOD", "NET_ADMIN", "NET_BIND_SERVICE", "NET_BROADCAST",
	"NET_RAW", "PERFMON", "SETFCAP", "SETGID", "SETPCAP", "SETUID", "SYSLOG", "SYS_ADMIN", "SYS_BOOT",
	"SYS_CHROOT", "SYS_MODULE", "SYS_NICE", "SYS_PACCT", "SYS_PTRACE", "SYS_RAWIO", "SYS_RESOURCE", "SYS_TIME",
	"SYS_TTY_CONFIG", "WAKE_ALARM",
}

// AllowedSysctls is the curated list of container sysctls. Docker supports
// sysctls only in the IPC (kernel.msg*, kernel.sem, kernel.shm*, fs.mqueue.*)
// and network (net.*) namespaces and "does not support changing sysctls inside
// of a container that also modify the host system" (docker run reference).
// Containers never use the host network or IPC namespace, so each entry
// changes only the container's own namespace. The network entries are the ones
// real applications ask for: listen backlog (somaxconn), binding below port
// 1024 without a capability, forwarding and source-validation for VPN
// containers, keepalives and port ranges. vm.max_map_count and other
// host-wide parameters are NOT here: see DockerOptions.HostSysctls.
var AllowedSysctls = []string{
	"fs.mqueue.msg_default", "fs.mqueue.msg_max", "fs.mqueue.msgsize_default", "fs.mqueue.msgsize_max", "fs.mqueue.queues_max",
	"kernel.msgmax", "kernel.msgmnb", "kernel.msgmni", "kernel.sem", "kernel.shm_rmid_forced", "kernel.shmall", "kernel.shmmax", "kernel.shmmni",
	"net.core.somaxconn",
	"net.ipv4.conf.all.forwarding", "net.ipv4.conf.all.rp_filter", "net.ipv4.conf.all.route_localnet", "net.ipv4.conf.all.src_valid_mark",
	"net.ipv4.conf.default.forwarding", "net.ipv4.ip_forward", "net.ipv4.ip_local_port_range", "net.ipv4.ip_unprivileged_port_start",
	"net.ipv4.ping_group_range", "net.ipv4.tcp_fin_timeout", "net.ipv4.tcp_keepalive_intvl", "net.ipv4.tcp_keepalive_probes",
	"net.ipv4.tcp_keepalive_time", "net.ipv4.tcp_syncookies",
	"net.ipv6.conf.all.disable_ipv6", "net.ipv6.conf.all.forwarding", "net.ipv6.conf.default.disable_ipv6",
}

// HostSysctlKeys are the host kernel parameters a service may declare as
// requirements (DockerOptions.HostSysctls). Each is a "needs at least" value.
var HostSysctlKeys = []string{
	"fs.file-max", "fs.inotify.max_user_instances", "fs.inotify.max_user_watches", "kernel.pid_max",
	"net.core.rmem_max", "net.core.wmem_max", "vm.max_map_count",
}

// GPUCapabilities are the NVIDIA driver capabilities GPURequest may narrow to.
var GPUCapabilities = []string{"compat32", "compute", "display", "graphics", "utility", "video"}

// Limits of DockerOptions.
const (
	MaxDockerLabels     = 30
	MaxDockerExtraHosts = 20
	MaxDockerTmpfs      = 10
	MaxDockerDNS        = 3
	MaxDockerGroupAdd   = 8
	MaxDockerSysctls    = 16
	MaxDockerDevices    = 16
	MaxDockerGPUs       = 16
	MaxEntrypointArgs   = 32
	MaxEntrypointArgLen = 4096
	MaxShmSizeMB        = 65536
	MaxTmpfsSizeMB      = 65536
	MaxPidsLimit        = 4194304
	MaxHostSysctlValue  = 1 << 32
	DefaultTmpfsSizeMB  = 64
)

var (
	labelKeyRe     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,127}$`)
	reservedLabels = []string{"com.docker.", "nebulactrl.", "traefik."}
	stopSignals    = map[string]bool{
		"SIGTERM": true, "SIGINT": true, "SIGQUIT": true, "SIGHUP": true, "SIGUSR1": true, "SIGUSR2": true, "SIGWINCH": true, "SIGKILL": true,
	}
	ulimitNames  = map[string]bool{"nofile": true, "memlock": true}
	tmpfsModeRe  = regexp.MustCompile(`^[0-7]{3,4}$`)
	reservedTmpf = []string{"/proc", "/sys", "/dev", "/run/docker", "/var/run/docker"}

	hostnameLabelRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	sysctlValueRe   = regexp.MustCompile(`^[0-9]+( [0-9]+)*$`)
	userPartRe      = regexp.MustCompile(`^([0-9]{1,10}|[a-z_][a-z0-9_.-]{0,31}\$?)$`)
	gidRe           = regexp.MustCompile(`^[0-9]{1,5}$`)
	runtimeRe       = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,62}$`)
	devicePermRe    = regexp.MustCompile(`^[rwm]{1,3}$`)
)

// RequiresUnrestrictedError is returned by Validate when the options are
// well-formed but need an unrestricted server (see DockerOptions.NeedsUnrestricted).
type RequiresUnrestrictedError struct {
	// Fields names what needs it, for example "devices" or "capAdd NET_ADMIN".
	Fields []string
}

func (e *RequiresUnrestrictedError) Error() string {
	return strings.Join(e.Fields, ", ") + " need an unrestricted server: an instance admin has to enable \"Unrestricted\" on it first"
}

// NormalizeCapability upper-cases a capability name and drops a CAP_ prefix.
func NormalizeCapability(c string) string {
	c = strings.ToUpper(strings.TrimSpace(c))
	return strings.TrimPrefix(c, "CAP_")
}

func inList(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// IsUnrestrictedCapability reports whether c needs an unrestricted server.
func IsUnrestrictedCapability(c string) bool {
	return inList(UnrestrictedCapabilities, NormalizeCapability(c))
}

// UnrestrictedFields lists what in the options needs an unrestricted server
// (empty when nothing does): the names of the unrestricted-only fields and
// "capAdd <NAME>" for NET_ADMIN and SYS_ADMIN.
func (o *DockerOptions) UnrestrictedFields() []string {
	if o == nil {
		return nil
	}
	var out []string
	if len(o.Devices) > 0 {
		out = append(out, "devices")
	}
	if o.GPUs != nil {
		out = append(out, "gpus")
	}
	if o.Runtime != "" {
		out = append(out, "runtime")
	}
	if o.Privileged {
		out = append(out, "privileged")
	}
	if o.PidHost {
		out = append(out, "pidHost")
	}
	if o.SeccompUnconfined {
		out = append(out, "seccompUnconfined")
	}
	if o.ApparmorUnconfined {
		out = append(out, "apparmorUnconfined")
	}
	for _, n := range normalizeCaps(o.CapAdd) {
		if IsUnrestrictedCapability(n) {
			out = append(out, "capAdd "+n)
		}
	}
	return out
}

// NeedsUnrestricted reports whether any option needs an unrestricted server.
func (o *DockerOptions) NeedsUnrestricted() bool { return len(o.UnrestrictedFields()) > 0 }

// Normalized returns a copy with capability names normalized (upper case, no
// CAP_ prefix, de-duplicated and sorted) and the lists sorted so equal option
// sets compare equal. A set with nothing in it normalizes to nil.
func (o *DockerOptions) Normalized() *DockerOptions {
	if o == nil {
		return nil
	}
	n := *o
	n.CapAdd = normalizeCaps(o.CapAdd)
	n.CapDrop = normalizeCaps(o.CapDrop)
	n.Ulimits = append([]Ulimit(nil), o.Ulimits...)
	sort.Slice(n.Ulimits, func(i, j int) bool { return n.Ulimits[i].Name < n.Ulimits[j].Name })
	n.ExtraHosts = append([]string(nil), o.ExtraHosts...)
	n.Tmpfs = append([]TmpfsMount(nil), o.Tmpfs...)
	n.Entrypoint = append([]string(nil), o.Entrypoint...)
	sort.Slice(n.Tmpfs, func(i, j int) bool { return n.Tmpfs[i].Path < n.Tmpfs[j].Path })
	if len(n.Labels) == 0 {
		n.Labels = nil
	}
	if len(n.Sysctls) == 0 {
		n.Sysctls = nil
	}
	if len(n.HostSysctls) == 0 {
		n.HostSysctls = nil
	}
	n.DNS = append([]string(nil), o.DNS...)
	n.GroupAdd = append([]string(nil), o.GroupAdd...)
	n.Devices = append([]DeviceMapping(nil), o.Devices...)
	if o.GPUs != nil {
		g := *o.GPUs
		g.Capabilities = normalizeStrings(g.Capabilities)
		n.GPUs = &g
	}
	n.StopSignal = strings.ToUpper(strings.TrimSpace(n.StopSignal))
	n.Hostname = strings.ToLower(strings.TrimSpace(n.Hostname))
	n.User = strings.TrimSpace(n.User)
	n.Runtime = strings.TrimSpace(n.Runtime)
	if n.IsZero() {
		return nil
	}
	return &n
}

// normalizeCaps upper-cases, strips CAP_, de-duplicates and sorts; empty is nil.
func normalizeCaps(in []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, c := range in {
		c = NormalizeCapability(c)
		if !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
	}
	sort.Strings(out)
	return out
}

// normalizeStrings lower-cases, de-duplicates and sorts; empty is nil.
func normalizeStrings(in []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, c := range in {
		c = strings.ToLower(strings.TrimSpace(c))
		if !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
	}
	sort.Strings(out)
	return out
}

// IsZero reports whether no option is set.
func (o *DockerOptions) IsZero() bool {
	return o == nil || (len(o.CapAdd) == 0 && len(o.CapDrop) == 0 && !o.NoNewPrivileges && !o.ReadOnlyRootfs && o.PidsLimit == 0 &&
		o.Hostname == "" && len(o.DNS) == 0 && len(o.Sysctls) == 0 && len(o.HostSysctls) == 0 && o.User == "" && len(o.GroupAdd) == 0 &&
		len(o.Ulimits) == 0 && o.ShmSizeMB == 0 && len(o.ExtraHosts) == 0 &&
		len(o.Labels) == 0 && !o.Init && o.StopSignal == "" && len(o.Tmpfs) == 0 && len(o.Entrypoint) == 0 &&
		len(o.Devices) == 0 && o.GPUs == nil && o.Runtime == "" && !o.Privileged && !o.PidHost && !o.SeccompUnconfined && !o.ApparmorUnconfined)
}

// Validate checks every option. unrestricted says whether the target server
// is unrestricted (DockerPolicy.Unrestricted); when it is not, well-formed
// options that need it return a *RequiresUnrestrictedError after the format
// checks (so a typo is reported as a typo). The control plane validates the
// format with unrestricted=true on every write and applies the server policy
// when the placement is known; the agent validates with the payload's policy.
func (o *DockerOptions) Validate(unrestricted bool) error {
	if o == nil {
		return nil
	}
	if err := o.validateFormat(); err != nil {
		return err
	}
	if !unrestricted {
		if f := o.UnrestrictedFields(); len(f) > 0 {
			return &RequiresUnrestrictedError{Fields: f}
		}
	}
	return nil
}

// ValidateFiles checks the options against the service's config files and
// volumes: a read-only root filesystem cannot take files (docker cp refuses
// it, and a tmpfs is mounted over whatever is copied before start), so each
// file must sit on a volume mount.
func (o *DockerOptions) ValidateFiles(filePaths, volumeMounts []string) error {
	if o == nil || !o.ReadOnlyRootfs {
		return nil
	}
	for _, f := range filePaths {
		f = path.Clean(f)
		ok := false
		for _, v := range volumeMounts {
			v = path.Clean(v)
			if f == v || strings.HasPrefix(f, v+"/") {
				ok = true
				break
			}
		}
		if !ok {
			return fmt.Errorf("readOnlyRootfs: the config file %s is written into the container's root filesystem, which is read-only; mount a volume that contains it or turn readOnlyRootfs off", f)
		}
	}
	return nil
}

func (o *DockerOptions) validateFormat() error {
	seen := map[string]bool{}
	for _, raw := range o.CapAdd {
		c := NormalizeCapability(raw)
		switch {
		case seen[c]:
			return fmt.Errorf("capability %s is listed twice", c)
		case inList(SafeCapabilities, c), inList(UnrestrictedCapabilities, c):
		default:
			return fmt.Errorf("capability %q is not allowed; allowed: %s (and %s on an unrestricted server)", raw,
				strings.Join(SafeCapabilities, ", "), strings.Join(UnrestrictedCapabilities, ", "))
		}
		seen[c] = true
	}

	dropped := map[string]bool{}
	for _, raw := range o.CapDrop {
		c := NormalizeCapability(raw)
		switch {
		case dropped[c]:
			return fmt.Errorf("capDrop lists %s twice", c)
		case c != "ALL" && !inList(KnownCapabilities, c):
			return fmt.Errorf("capDrop: %q is not a Linux capability; use ALL or a name such as NET_RAW", raw)
		}
		dropped[c] = true
	}
	for c := range seen {
		if dropped[c] {
			return fmt.Errorf("capability %s is in both capAdd and capDrop", c)
		}
	}

	if o.PidsLimit < 0 || o.PidsLimit > MaxPidsLimit {
		return fmt.Errorf("pidsLimit must be 0 (no limit) or 1-%d", MaxPidsLimit)
	}

	if h := strings.TrimSpace(o.Hostname); h != "" && !hostnameLabelRe.MatchString(strings.ToLower(h)) {
		return fmt.Errorf("hostname %q must be one RFC 1123 label: letters, digits and dashes, at most 63 characters", o.Hostname)
	}

	if len(o.DNS) > MaxDockerDNS {
		return fmt.Errorf("at most %d dns servers", MaxDockerDNS)
	}
	dns := map[netip.Addr]bool{}
	for _, d := range o.DNS {
		ip, err := netip.ParseAddr(d)
		if err != nil || ip.Zone() != "" {
			return fmt.Errorf("dns %q must be an IP address (hostnames are not accepted)", d)
		}
		if ip.IsUnspecified() || ip.IsMulticast() {
			return fmt.Errorf("dns %q is not a usable resolver address", d)
		}
		if dns[ip] {
			return fmt.Errorf("dns %s is listed twice", d)
		}
		dns[ip] = true
	}

	if len(o.Sysctls) > MaxDockerSysctls {
		return fmt.Errorf("at most %d sysctls", MaxDockerSysctls)
	}
	for k, v := range o.Sysctls {
		if !inList(AllowedSysctls, k) {
			if inList(HostSysctlKeys, k) || strings.HasPrefix(k, "vm.") {
				return fmt.Errorf("sysctl %q changes the host and cannot be set from a container; declare it in hostSysctls so the plan can warn", k)
			}
			return fmt.Errorf("sysctl %q is not allowed; allowed: %s", k, strings.Join(AllowedSysctls, ", "))
		}
		if len(v) > 64 || !sysctlValueRe.MatchString(v) {
			return fmt.Errorf("sysctl %s: the value must be a number, or numbers separated by single spaces", k)
		}
	}

	if len(o.HostSysctls) > len(HostSysctlKeys) {
		return fmt.Errorf("at most %d host sysctls", len(HostSysctlKeys))
	}
	for k, v := range o.HostSysctls {
		if !inList(HostSysctlKeys, k) {
			return fmt.Errorf("hostSysctls %q is not supported; supported: %s", k, strings.Join(HostSysctlKeys, ", "))
		}
		if v < 1 || v > MaxHostSysctlValue {
			return fmt.Errorf("hostSysctls %s must be 1-%d", k, int64(MaxHostSysctlValue))
		}
	}

	if u := strings.TrimSpace(o.User); u != "" {
		uid, gid, hasGID := strings.Cut(u, ":")
		if !userPartRe.MatchString(uid) || (hasGID && !userPartRe.MatchString(gid)) || tooBigID(uid) || tooBigID(gid) {
			return fmt.Errorf("user %q must be uid[:gid]: numbers (up to 2147483647) or user and group names", o.User)
		}
	}

	if len(o.GroupAdd) > MaxDockerGroupAdd {
		return fmt.Errorf("at most %d supplementary groups", MaxDockerGroupAdd)
	}
	groups := map[string]bool{}
	for _, g := range o.GroupAdd {
		n, err := strconv.Atoi(g)
		if !gidRe.MatchString(g) || err != nil || n < 1 || n > 65535 {
			return fmt.Errorf("groupAdd %q must be a numeric group ID from 1 to 65535", g)
		}
		if groups[g] {
			return fmt.Errorf("groupAdd %s is listed twice", g)
		}
		groups[g] = true
	}

	if len(o.Ulimits) > len(ulimitNames) {
		return fmt.Errorf("at most %d ulimits", len(ulimitNames))
	}
	seen = map[string]bool{}
	for _, u := range o.Ulimits {
		if u.Name == "nproc" {
			return fmt.Errorf("ulimit nproc is not supported: it is per user ID and not enforced for root; use pidsLimit")
		}
		if !ulimitNames[u.Name] {
			return fmt.Errorf("ulimit %q is not allowed; allowed: nofile, memlock", u.Name)
		}
		if seen[u.Name] {
			return fmt.Errorf("ulimit %s is listed twice", u.Name)
		}
		seen[u.Name] = true
		const maxLimit = 1<<31 - 1
		if u.Soft < -1 || u.Hard < -1 || u.Soft > maxLimit || u.Hard > maxLimit {
			return fmt.Errorf("ulimit %s: soft and hard must be -1 (unlimited) or 0-%d", u.Name, maxLimit)
		}
		if u.Hard != -1 && (u.Soft == -1 || u.Soft > u.Hard) {
			return fmt.Errorf("ulimit %s: soft must not exceed hard", u.Name)
		}
	}

	if o.ShmSizeMB < 0 || o.ShmSizeMB > MaxShmSizeMB {
		return fmt.Errorf("shmSizeMb must be 0-%d", MaxShmSizeMB)
	}

	if len(o.ExtraHosts) > MaxDockerExtraHosts {
		return fmt.Errorf("at most %d extra hosts", MaxDockerExtraHosts)
	}
	for _, h := range o.ExtraHosts {
		name, ip, ok := strings.Cut(h, ":")
		if !ok || !hostPattern.MatchString(name) {
			return fmt.Errorf("extra host %q must be name:ip or name:host-gateway", h)
		}
		if ip != "host-gateway" {
			if _, err := netip.ParseAddr(ip); err != nil {
				return fmt.Errorf("extra host %q: %q is not an IP address or host-gateway", h, ip)
			}
		}
	}

	if len(o.Labels) > MaxDockerLabels {
		return fmt.Errorf("at most %d labels", MaxDockerLabels)
	}
	for k, v := range o.Labels {
		if !labelKeyRe.MatchString(k) {
			return fmt.Errorf("invalid label key %q", k)
		}
		lk := strings.ToLower(k)
		for _, p := range reservedLabels {
			if strings.HasPrefix(lk, p) {
				return fmt.Errorf("label %q is reserved: %s* labels are managed by NebulaCtrl", k, p)
			}
		}
		if len(v) > 512 || strings.ContainsAny(v, "\r\n\x00") {
			return fmt.Errorf("invalid value for label %q", k)
		}
	}

	if o.StopSignal != "" && !stopSignals[strings.ToUpper(o.StopSignal)] {
		return fmt.Errorf("stopSignal %q is not allowed; allowed: SIGTERM, SIGINT, SIGQUIT, SIGHUP, SIGUSR1, SIGUSR2, SIGWINCH, SIGKILL", o.StopSignal)
	}

	if err := validateEntrypoint(o.Entrypoint); err != nil {
		return err
	}

	if len(o.Tmpfs) > MaxDockerTmpfs {
		return fmt.Errorf("at most %d tmpfs mounts", MaxDockerTmpfs)
	}
	paths := map[string]bool{}
	for _, t := range o.Tmpfs {
		p := path.Clean(t.Path)
		if !path.IsAbs(t.Path) || p != t.Path || p == "/" || strings.ContainsAny(p, ",: \x00") {
			return fmt.Errorf("tmpfs path %q must be a clean absolute path without spaces, commas or colons", t.Path)
		}
		for _, r := range reservedTmpf {
			if p == r || strings.HasPrefix(p, r+"/") {
				return fmt.Errorf("tmpfs path %q is reserved", t.Path)
			}
		}
		if paths[p] {
			return fmt.Errorf("tmpfs path %s is listed twice", p)
		}
		paths[p] = true
		if t.SizeMB < 0 || t.SizeMB > MaxTmpfsSizeMB {
			return fmt.Errorf("tmpfs %s: sizeMb must be 0-%d", p, MaxTmpfsSizeMB)
		}
		if t.Mode != "" && !tmpfsModeRe.MatchString(t.Mode) {
			return fmt.Errorf("tmpfs %s: mode must be octal such as 1777", p)
		}
	}

	if err := o.validateDevices(); err != nil {
		return err
	}
	if g := o.GPUs; g != nil {
		if g.Count != -1 && (g.Count < 1 || g.Count > MaxDockerGPUs) {
			return fmt.Errorf("gpus.count must be -1 (all) or 1-%d", MaxDockerGPUs)
		}
		if g.Driver != "nvidia" {
			return fmt.Errorf("gpus.driver %q is not supported; only nvidia", g.Driver)
		}
		capSeen := map[string]bool{}
		for _, c := range g.Capabilities {
			c = strings.ToLower(strings.TrimSpace(c))
			if !inList(GPUCapabilities, c) {
				return fmt.Errorf("gpus.capabilities %q is not supported; supported: %s", c, strings.Join(GPUCapabilities, ", "))
			}
			if capSeen[c] {
				return fmt.Errorf("gpus.capabilities lists %s twice", c)
			}
			capSeen[c] = true
		}
	}
	if r := strings.TrimSpace(o.Runtime); r != "" && !runtimeRe.MatchString(r) {
		return fmt.Errorf("runtime %q must be the name of an OCI runtime configured in the Docker daemon", o.Runtime)
	}
	return nil
}

func tooBigID(s string) bool {
	if s == "" {
		return false
	}
	n, err := strconv.ParseInt(s, 10, 64)
	return err == nil && n > 1<<31-1
}

func (o *DockerOptions) validateDevices() error {
	if len(o.Devices) > MaxDockerDevices {
		return fmt.Errorf("at most %d devices", MaxDockerDevices)
	}
	inside := map[string]bool{}
	for _, d := range o.Devices {
		if err := validDevicePath(d.HostPath); err != nil {
			return fmt.Errorf("device hostPath %q: %w", d.HostPath, err)
		}
		cp := d.ContainerPath
		if cp == "" {
			cp = d.HostPath
		}
		if err := validDevicePath(cp); err != nil {
			return fmt.Errorf("device containerPath %q: %w", cp, err)
		}
		if d.Permissions != "" {
			if !devicePermRe.MatchString(d.Permissions) || strings.Count(d.Permissions, "r") > 1 ||
				strings.Count(d.Permissions, "w") > 1 || strings.Count(d.Permissions, "m") > 1 {
				return fmt.Errorf("device %s: permissions must be a subset of rwm, for example rw", d.HostPath)
			}
		}
		if inside[cp] {
			return fmt.Errorf("device containerPath %s is listed twice", cp)
		}
		inside[cp] = true
	}
	return nil
}

// validDevicePath accepts a clean absolute path below /dev without separators
// Docker's --device syntax uses.
func validDevicePath(p string) error {
	switch {
	case !path.IsAbs(p) || path.Clean(p) != p:
		return fmt.Errorf("must be a clean absolute path")
	case !strings.HasPrefix(p, "/dev/") || p == "/dev/":
		return fmt.Errorf("must be a device node below /dev")
	case strings.ContainsAny(p, ",: \x00"):
		return fmt.Errorf("must not contain spaces, commas or colons")
	}
	return nil
}

// validateEntrypoint checks an exec-form entrypoint: a program followed by
// its arguments.
func validateEntrypoint(argv []string) error {
	if len(argv) == 0 {
		return nil
	}
	if len(argv) > MaxEntrypointArgs {
		return fmt.Errorf("entrypoint has at most %d elements", MaxEntrypointArgs)
	}
	if strings.TrimSpace(argv[0]) == "" {
		return fmt.Errorf("entrypoint: the first element must be the program to run")
	}
	for i, a := range argv {
		if len(a) > MaxEntrypointArgLen || strings.ContainsRune(a, 0) {
			return fmt.Errorf("entrypoint: element %d is longer than %d bytes or contains a NUL byte", i+1, MaxEntrypointArgLen)
		}
	}
	return nil
}

// TmpfsOption renders the Docker tmpfs option string of a mount ("size=64m,mode=1777").
func (t TmpfsMount) TmpfsOption() string {
	size := t.SizeMB
	if size <= 0 {
		size = DefaultTmpfsSizeMB
	}
	opt := fmt.Sprintf("size=%dm", size)
	if t.Mode != "" {
		opt += ",mode=" + t.Mode
	}
	return opt
}

// Warnings returns non-blocking notes for the plan and the UI.
func (o *DockerOptions) Warnings() []string {
	if o == nil {
		return nil
	}
	var out []string
	for _, c := range o.CapAdd {
		if inList(WarnCapabilities, NormalizeCapability(c)) {
			out = append(out, "SYS_PTRACE lets processes in the container inspect and tamper with each other; add it only when the application needs it.")
		}
		if IsUnrestrictedCapability(c) {
			out = append(out, NormalizeCapability(c)+" widens the container's power over the host and works only on an unrestricted server. Prefer a narrower capability.")
		}
	}
	if o.ReadOnlyRootfs && len(o.Tmpfs) == 0 {
		out = append(out, "The root filesystem is read-only and no tmpfs is mounted: many applications write to /tmp or /run and will fail. Add tmpfs mounts or volumes for the paths that need writing.")
	}
	if o.Privileged {
		out = append(out, "Privileged mode gives the container all capabilities and devices: it is equivalent to root on the host.")
	}
	if o.PidHost {
		out = append(out, "The container shares the host's process namespace: it can see and signal every process on the host.")
	}
	if o.SeccompUnconfined || o.ApparmorUnconfined {
		out = append(out, "The default syscall or AppArmor profile is switched off: a kernel exploit from inside the container has fewer barriers.")
	}
	if len(o.Devices) > 0 || o.GPUs != nil {
		out = append(out, "Host devices and GPUs widen the container's access to host hardware and drivers; the image must be trusted.")
	}
	return out
}

// ReservedLabelPrefixes lists the label prefixes a service cannot set.
func ReservedLabelPrefixes() []string { return append([]string(nil), reservedLabels...) }

// StopSignals lists the allowed stop signals.
func StopSignals() []string {
	out := make([]string, 0, len(stopSignals))
	for s := range stopSignals {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}
