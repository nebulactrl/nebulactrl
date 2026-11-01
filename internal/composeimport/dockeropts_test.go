package composeimport

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

type wantNote struct {
	key, sev, msg string
}

type dockerCase struct {
	name string
	// yaml is the body of the service "app" (image busybox is added).
	yaml string
	// files are bind-mountable files next to the Compose file.
	files        map[string]string
	unrestricted bool
	// want are the options expected on the plan; nil means none.
	want *agentprotocol.DockerOptions
	// notes must each appear (key is relative to the service).
	notes []wantNote
	// quiet asserts the service has no notes at all.
	quiet bool
}

func planDocker(t *testing.T, c dockerCase) ComposePlanService {
	t.Helper()
	src := ComposeSource{
		Compose: "services:\n  app:\n    image: busybox\n    " + strings.ReplaceAll(c.yaml, "\n", "\n    ") + "\n",
		Files:   c.files,
	}
	r, err := Analyze(src, ComposeChoices{}, Context{Unrestricted: c.unrestricted})
	require.NoError(t, err)
	return svc(t, r.Plan(), "app")
}

func runDockerCases(t *testing.T, cases []dockerCase) {
	t.Helper()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := planDocker(t, c)
			require.Equal(t, c.want.Normalized(), s.Docker, "notes: %+v", s.Notes)
			for _, n := range c.notes {
				found := false
				for _, got := range s.Notes {
					if got.Key == "app."+n.key && strings.Contains(got.Message, n.msg) && (n.sev == "" || got.Severity == n.sev) {
						found = true
					}
				}
				require.True(t, found, "missing note %+v in %+v", n, s.Notes)
			}
			if c.quiet {
				require.Empty(t, s.Notes)
			}
		})
	}
}

func opts(f func(o *agentprotocol.DockerOptions)) *agentprotocol.DockerOptions {
	o := &agentprotocol.DockerOptions{}
	f(o)
	return o
}

const needsUnrestricted = "Needs an unrestricted server"

func TestDockerCapDrop(t *testing.T) {
	runDockerCases(t, []dockerCase{
		{name: "ALL", yaml: "cap_drop: [ALL]", want: opts(func(o *agentprotocol.DockerOptions) { o.CapDrop = []string{"ALL"} }), quiet: true},
		{name: "names normalised", yaml: "cap_drop: [cap_net_raw, mknod]", want: opts(func(o *agentprotocol.DockerOptions) { o.CapDrop = []string{"MKNOD", "NET_RAW"} }), quiet: true},
		{name: "drop ALL and add back", yaml: "cap_drop: [ALL]\ncap_add: [NET_BIND_SERVICE]",
			want: opts(func(o *agentprotocol.DockerOptions) {
				o.CapDrop = []string{"ALL"}
				o.CapAdd = []string{"NET_BIND_SERVICE"}
			}), quiet: true},
		{name: "twice", yaml: "cap_drop: [NET_RAW, NET_RAW]", want: opts(func(o *agentprotocol.DockerOptions) { o.CapDrop = []string{"NET_RAW"} }), quiet: true},
		{name: "not a capability", yaml: "cap_drop: [NOPE]", notes: []wantNote{{"cap_drop.NOPE", SevWarning, "not a Linux capability"}}},
		{name: "also added", yaml: "cap_add: [NET_RAW]\ncap_drop: [NET_RAW]",
			want:  opts(func(o *agentprotocol.DockerOptions) { o.CapAdd = []string{"NET_RAW"} }),
			notes: []wantNote{{"cap_drop.NET_RAW", SevWarning, "also listed in cap_add"}}},
	})
}

func TestDockerCapAddTiers(t *testing.T) {
	runDockerCases(t, []dockerCase{
		{name: "safe", yaml: "cap_add: [CHOWN]", want: opts(func(o *agentprotocol.DockerOptions) { o.CapAdd = []string{"CHOWN"} }), quiet: true},
		{name: "NET_ADMIN restricted", yaml: "cap_add: [NET_ADMIN]", notes: []wantNote{{"cap_add.NET_ADMIN", SevWarning, needsUnrestricted}}},
		{name: "NET_ADMIN unrestricted", yaml: "cap_add: [NET_ADMIN]", unrestricted: true,
			want:  opts(func(o *agentprotocol.DockerOptions) { o.CapAdd = []string{"NET_ADMIN"} }),
			notes: []wantNote{{"cap_add.NET_ADMIN", SevWarning, "Applied because the target server is unrestricted"}}},
		{name: "SYS_ADMIN restricted", yaml: "cap_add: [SYS_ADMIN]", notes: []wantNote{{"cap_add.SYS_ADMIN", SevWarning, needsUnrestricted}}},
		{name: "never supported", yaml: "cap_add: [SYS_MODULE]", unrestricted: true, notes: []wantNote{{"cap_add.SYS_MODULE", SevWarning, "not allowed"}}},
		{name: "twice", yaml: "cap_add: [CHOWN, CAP_CHOWN]", want: opts(func(o *agentprotocol.DockerOptions) { o.CapAdd = []string{"CHOWN"} }), quiet: true},
	})
}

func TestDockerSecurityOpt(t *testing.T) {
	nnp := opts(func(o *agentprotocol.DockerOptions) { o.NoNewPrivileges = true })
	runDockerCases(t, []dockerCase{
		{name: "no-new-privileges:true", yaml: "security_opt: ['no-new-privileges:true']", want: nnp, quiet: true},
		{name: "no-new-privileges=true", yaml: "security_opt: ['no-new-privileges=true']", want: nnp, quiet: true},
		{name: "no-new-privileges bare", yaml: "security_opt: [no-new-privileges]", want: nnp, quiet: true},
		{name: "no-new-privileges:false is the default", yaml: "security_opt: ['no-new-privileges:false']", quiet: true},
		{name: "seccomp unconfined restricted", yaml: "security_opt: ['seccomp=unconfined']", notes: []wantNote{{"security_opt", SevDanger, needsUnrestricted}}},
		{name: "seccomp unconfined unrestricted", yaml: "security_opt: ['seccomp:unconfined']", unrestricted: true,
			want:  opts(func(o *agentprotocol.DockerOptions) { o.SeccompUnconfined = true }),
			notes: []wantNote{{"security_opt", SevWarning, "Applied because the target server is unrestricted"}}},
		{name: "apparmor unconfined restricted", yaml: "security_opt: ['apparmor=unconfined']", notes: []wantNote{{"security_opt", SevDanger, needsUnrestricted}}},
		{name: "apparmor unconfined unrestricted", yaml: "security_opt: ['apparmor:unconfined']", unrestricted: true,
			want: opts(func(o *agentprotocol.DockerOptions) { o.ApparmorUnconfined = true })},
		{name: "both and no-new-privileges", yaml: "security_opt: ['no-new-privileges:true', 'seccomp=unconfined', 'apparmor=unconfined']", unrestricted: true,
			want: opts(func(o *agentprotocol.DockerOptions) {
				o.NoNewPrivileges, o.SeccompUnconfined, o.ApparmorUnconfined = true, true, true
			})},
		{name: "mixed on a restricted server keeps the safe one", yaml: "security_opt: ['no-new-privileges:true', 'seccomp=unconfined']", want: nnp,
			notes: []wantNote{{"security_opt", SevDanger, needsUnrestricted}}},
		{name: "seccomp profile", yaml: "security_opt: ['seccomp=/etc/profile.json']", unrestricted: true, notes: []wantNote{{"security_opt", SevWarning, "custom seccomp profiles"}}},
		{name: "apparmor profile", yaml: "security_opt: ['apparmor=custom']", unrestricted: true, notes: []wantNote{{"security_opt", SevWarning, "custom apparmor profiles"}}},
		{name: "selinux label", yaml: "security_opt: ['label=disable']", unrestricted: true, notes: []wantNote{{"security_opt", SevWarning, "not supported"}}},
	})
}

func TestDockerReadOnly(t *testing.T) {
	ro := opts(func(o *agentprotocol.DockerOptions) { o.ReadOnlyRootfs = true })
	runDockerCases(t, []dockerCase{
		{name: "plain", yaml: "read_only: true", want: ro, quiet: true},
		{name: "false", yaml: "read_only: false", quiet: true},
		{name: "config file in the root filesystem", yaml: "read_only: true\nvolumes: ['./app.yml:/etc/app.yml']", files: map[string]string{"app.yml": "a: 1\n"},
			notes: []wantNote{{"read_only", SevWarning, "read-only"}}},
		{name: "config file on a volume", yaml: "read_only: true\nvolumes: ['./app.yml:/etc/app.yml', 'conf:/etc']", files: map[string]string{"app.yml": "a: 1\n"}, want: ro},
	})
}

func TestDockerPidsLimit(t *testing.T) {
	runDockerCases(t, []dockerCase{
		{name: "limit", yaml: "pids_limit: 200", want: opts(func(o *agentprotocol.DockerOptions) { o.PidsLimit = 200 }), quiet: true},
		{name: "string", yaml: "pids_limit: '64'", want: opts(func(o *agentprotocol.DockerOptions) { o.PidsLimit = 64 }), quiet: true},
		{name: "unlimited", yaml: "pids_limit: -1", quiet: true},
		{name: "too big", yaml: "pids_limit: 999999999", notes: []wantNote{{"pids_limit", SevWarning, "pidsLimit must be"}}},
	})
}

func TestDockerHostname(t *testing.T) {
	runDockerCases(t, []dockerCase{
		{name: "label", yaml: "hostname: web", want: opts(func(o *agentprotocol.DockerOptions) { o.Hostname = "web" }), quiet: true},
		{name: "lower-cased", yaml: "hostname: Web-1", want: opts(func(o *agentprotocol.DockerOptions) { o.Hostname = "web-1" }), quiet: true},
		{name: "fqdn", yaml: "hostname: mail.example.com", notes: []wantNote{{"hostname", SevWarning, "RFC 1123 label"}}},
		{name: "invalid", yaml: "hostname: '-bad'", notes: []wantNote{{"hostname", SevWarning, "Not applied"}}},
	})
}

func TestDockerDNS(t *testing.T) {
	runDockerCases(t, []dockerCase{
		{name: "string", yaml: "dns: 1.1.1.1", want: opts(func(o *agentprotocol.DockerOptions) { o.DNS = []string{"1.1.1.1"} }), quiet: true},
		{name: "list with ipv6", yaml: "dns: [1.1.1.1, '2606:4700:4700::1111']",
			want: opts(func(o *agentprotocol.DockerOptions) { o.DNS = []string{"1.1.1.1", "2606:4700:4700::1111"} }), quiet: true},
		{name: "duplicate", yaml: "dns: [1.1.1.1, 1.1.1.1]", want: opts(func(o *agentprotocol.DockerOptions) { o.DNS = []string{"1.1.1.1"} }), quiet: true},
		{name: "hostname", yaml: "dns: [dns.example.com, 9.9.9.9]", want: opts(func(o *agentprotocol.DockerOptions) { o.DNS = []string{"9.9.9.9"} }),
			notes: []wantNote{{"dns", SevWarning, "must be an IP address"}}},
		{name: "more than three", yaml: "dns: [1.1.1.1, 1.0.0.1, 9.9.9.9, 8.8.8.8]",
			want:  opts(func(o *agentprotocol.DockerOptions) { o.DNS = []string{"1.1.1.1", "1.0.0.1", "9.9.9.9"} }),
			notes: []wantNote{{"dns", SevWarning, "at most 3"}}},
	})
}

func TestDockerSysctls(t *testing.T) {
	runDockerCases(t, []dockerCase{
		{name: "map", yaml: "sysctls: {net.core.somaxconn: 1024, net.ipv4.ip_unprivileged_port_start: 0}",
			want: opts(func(o *agentprotocol.DockerOptions) {
				o.Sysctls = map[string]string{"net.core.somaxconn": "1024", "net.ipv4.ip_unprivileged_port_start": "0"}
			}), quiet: true},
		{name: "list", yaml: "sysctls: ['kernel.shmmax=268435456', 'net.ipv4.ip_local_port_range=1024 65000']",
			want: opts(func(o *agentprotocol.DockerOptions) {
				o.Sysctls = map[string]string{"kernel.shmmax": "268435456", "net.ipv4.ip_local_port_range": "1024 65000"}
			}), quiet: true},
		{name: "host sysctl", yaml: "sysctls: {vm.max_map_count: 262144}", notes: []wantNote{{"sysctls.vm.max_map_count", SevWarning, "changes the host"}}},
		{name: "not curated", yaml: "sysctls: {net.ipv4.tcp_rmem: '4096 87380 6291456'}", unrestricted: true, notes: []wantNote{{"sysctls.net.ipv4.tcp_rmem", SevWarning, "not allowed"}}},
		{name: "kept next to a refused one", yaml: "sysctls: {net.core.somaxconn: 128, kernel.core_pattern: x}",
			want:  opts(func(o *agentprotocol.DockerOptions) { o.Sysctls = map[string]string{"net.core.somaxconn": "128"} }),
			notes: []wantNote{{"sysctls.kernel.core_pattern", SevWarning, "Not applied"}}},
		{name: "bad value", yaml: "sysctls: {net.core.somaxconn: lots}", notes: []wantNote{{"sysctls.net.core.somaxconn", SevWarning, "must be a number"}}},
	})
}

func TestDockerUser(t *testing.T) {
	runDockerCases(t, []dockerCase{
		{name: "uid:gid", yaml: "user: '1000:1000'", want: opts(func(o *agentprotocol.DockerOptions) { o.User = "1000:1000" }), quiet: true},
		{name: "number", yaml: "user: 1000", want: opts(func(o *agentprotocol.DockerOptions) { o.User = "1000" }), quiet: true},
		{name: "name", yaml: "user: www-data", want: opts(func(o *agentprotocol.DockerOptions) { o.User = "www-data" }), quiet: true},
		{name: "invalid", yaml: "user: 'a b:c'", notes: []wantNote{{"user", SevWarning, "uid[:gid]"}}},
	})
}

func TestDockerGroupAdd(t *testing.T) {
	runDockerCases(t, []dockerCase{
		{name: "numeric", yaml: "group_add: [44, '109']", want: opts(func(o *agentprotocol.DockerOptions) { o.GroupAdd = []string{"44", "109"} }), quiet: true},
		{name: "duplicate", yaml: "group_add: [44, 44]", want: opts(func(o *agentprotocol.DockerOptions) { o.GroupAdd = []string{"44"} }), quiet: true},
		{name: "name", yaml: "group_add: [video, 44]", want: opts(func(o *agentprotocol.DockerOptions) { o.GroupAdd = []string{"44"} }),
			notes: []wantNote{{"group_add", SevWarning, "numeric group IDs"}}},
		{name: "root group", yaml: "group_add: [0]", notes: []wantNote{{"group_add", SevWarning, "Not applied"}}},
	})
}

func TestDockerDevices(t *testing.T) {
	dri := []agentprotocol.DeviceMapping{{HostPath: "/dev/dri", ContainerPath: "/dev/dri"}}
	runDockerCases(t, []dockerCase{
		{name: "restricted", yaml: "devices: ['/dev/dri:/dev/dri']", notes: []wantNote{{"devices", SevDanger, needsUnrestricted}}},
		{name: "unrestricted short", yaml: "devices: ['/dev/dri:/dev/dri']", unrestricted: true,
			want:  opts(func(o *agentprotocol.DockerOptions) { o.Devices = dri }),
			notes: []wantNote{{"devices", SevWarning, "Applied because the target server is unrestricted"}}},
		{name: "host path only", yaml: "devices: ['/dev/fuse']", unrestricted: true,
			want: opts(func(o *agentprotocol.DockerOptions) {
				o.Devices = []agentprotocol.DeviceMapping{{HostPath: "/dev/fuse"}}
			})},
		{name: "permissions as second part", yaml: "devices: ['/dev/fuse:rw']", unrestricted: true,
			want: opts(func(o *agentprotocol.DockerOptions) {
				o.Devices = []agentprotocol.DeviceMapping{{HostPath: "/dev/fuse", Permissions: "rw"}}
			})},
		{name: "all three parts", yaml: "devices: ['/dev/ttyUSB0:/dev/ttyS0:rwm']", unrestricted: true,
			want: opts(func(o *agentprotocol.DockerOptions) {
				o.Devices = []agentprotocol.DeviceMapping{{HostPath: "/dev/ttyUSB0", ContainerPath: "/dev/ttyS0", Permissions: "rwm"}}
			})},
		{name: "long form", yaml: "devices:\n  - source: /dev/dri\n    target: /dev/dri\n    permissions: rw", unrestricted: true,
			want: opts(func(o *agentprotocol.DockerOptions) {
				o.Devices = []agentprotocol.DeviceMapping{{HostPath: "/dev/dri", ContainerPath: "/dev/dri", Permissions: "rw"}}
			})},
		{name: "outside /dev", yaml: "devices: ['/sys/class/gpio:/gpio']", unrestricted: true, notes: []wantNote{{"devices", SevWarning, "below /dev"}}},
		{name: "CDI name", yaml: "devices: ['vendor.com/gpu=all']", unrestricted: true, notes: []wantNote{{"devices", SevDanger, "CDI"}}},
		{name: "mapped twice", yaml: "devices: ['/dev/a:/dev/x', '/dev/b:/dev/x']", unrestricted: true,
			want: opts(func(o *agentprotocol.DockerOptions) {
				o.Devices = []agentprotocol.DeviceMapping{{HostPath: "/dev/a", ContainerPath: "/dev/x"}}
			}),
			notes: []wantNote{{"devices", SevWarning, "mapped twice"}}},
	})
}

func TestDockerGPUs(t *testing.T) {
	all := &agentprotocol.GPURequest{Count: -1, Driver: "nvidia"}
	runDockerCases(t, []dockerCase{
		{name: "gpus all restricted", yaml: "gpus: all", notes: []wantNote{{"gpus", SevWarning, needsUnrestricted}}},
		{name: "gpus all", yaml: "gpus: all", unrestricted: true, want: opts(func(o *agentprotocol.DockerOptions) { o.GPUs = all })},
		{name: "gpus count", yaml: "gpus: 2", unrestricted: true,
			want: opts(func(o *agentprotocol.DockerOptions) { o.GPUs = &agentprotocol.GPURequest{Count: 2, Driver: "nvidia"} })},
		{name: "gpus list", yaml: "gpus:\n  - driver: nvidia\n    count: 1\n    capabilities: [gpu, compute]", unrestricted: true,
			want: opts(func(o *agentprotocol.DockerOptions) {
				o.GPUs = &agentprotocol.GPURequest{Count: 1, Driver: "nvidia", Capabilities: []string{"compute"}}
			})},
		{name: "reservation restricted", yaml: "deploy:\n  resources:\n    reservations:\n      devices:\n        - capabilities: [gpu]",
			notes: []wantNote{{"deploy.resources.reservations.devices", SevWarning, needsUnrestricted}}},
		{name: "reservation", yaml: "deploy:\n  resources:\n    reservations:\n      devices:\n        - {driver: nvidia, count: all, capabilities: [gpu]}", unrestricted: true,
			want: opts(func(o *agentprotocol.DockerOptions) { o.GPUs = all })},
		{name: "reservation with count and capabilities", yaml: "deploy:\n  resources:\n    reservations:\n      devices:\n        - {count: 1, capabilities: [gpu, utility, video]}", unrestricted: true,
			want: opts(func(o *agentprotocol.DockerOptions) {
				o.GPUs = &agentprotocol.GPURequest{Count: 1, Driver: "nvidia", Capabilities: []string{"utility", "video"}}
			})},
		{name: "reservation of something else", yaml: "deploy:\n  resources:\n    reservations:\n      devices:\n        - {driver: foo, capabilities: [tpu]}", unrestricted: true,
			notes: []wantNote{{"deploy.resources.reservations.devices", SevWarning, "gpu capability"}}},
		{name: "reservation by device id", yaml: "deploy:\n  resources:\n    reservations:\n      devices:\n        - {driver: nvidia, device_ids: ['0'], capabilities: [gpu]}", unrestricted: true,
			notes: []wantNote{{"deploy.resources.reservations.devices", SevWarning, "device_ids"}}},
		{name: "other driver", yaml: "gpus:\n  - driver: amd\n    count: 1", unrestricted: true, notes: []wantNote{{"gpus", SevWarning, "only nvidia"}}},
		{name: "first request wins", yaml: "gpus: all\ndeploy:\n  resources:\n    reservations:\n      devices:\n        - {count: 1, capabilities: [gpu]}", unrestricted: true,
			want:  opts(func(o *agentprotocol.DockerOptions) { o.GPUs = all }),
			notes: []wantNote{{"deploy.resources.reservations.devices", SevWarning, "only one GPU request"}}},
	})
}

func TestDockerRuntime(t *testing.T) {
	runDockerCases(t, []dockerCase{
		{name: "restricted", yaml: "runtime: nvidia", notes: []wantNote{{"runtime", SevWarning, needsUnrestricted}}},
		{name: "unrestricted", yaml: "runtime: sysbox-runc", unrestricted: true, want: opts(func(o *agentprotocol.DockerOptions) { o.Runtime = "sysbox-runc" })},
		{name: "runc is the default", yaml: "runtime: runc", notes: []wantNote{{"runtime", SevInfo, "Docker's default runtime"}}},
		{name: "invalid", yaml: "runtime: 'Bad Runtime'", unrestricted: true, notes: []wantNote{{"runtime", SevWarning, "OCI runtime"}}},
	})
}

func TestDockerPrivilegedAndPidHost(t *testing.T) {
	runDockerCases(t, []dockerCase{
		{name: "privileged restricted", yaml: "privileged: true", notes: []wantNote{{"privileged", SevDanger, needsUnrestricted}}},
		{name: "privileged unrestricted", yaml: "privileged: true", unrestricted: true,
			want:  opts(func(o *agentprotocol.DockerOptions) { o.Privileged = true }),
			notes: []wantNote{{"privileged", SevWarning, "Applied because the target server is unrestricted"}}},
		{name: "privileged false", yaml: "privileged: false", quiet: true},
		{name: "pid host restricted", yaml: "pid: host", notes: []wantNote{{"pid", SevDanger, needsUnrestricted}}},
		{name: "pid host unrestricted", yaml: "pid: host", unrestricted: true, want: opts(func(o *agentprotocol.DockerOptions) { o.PidHost = true })},
		{name: "pid private", yaml: "pid: private", quiet: true},
		{name: "pid of another service", yaml: "pid: 'service:other'", unrestricted: true, notes: []wantNote{{"pid", SevDanger, "not applied"}}},
		{name: "pid of a container", yaml: "pid: 'container:x'", notes: []wantNote{{"pid", SevDanger, "not applied"}}},
	})
}

// Every option is judged on its own: the ones the server allows are applied
// next to the ones it refuses, and the refusals are listed.
func TestDockerMixedTiersOnARestrictedServer(t *testing.T) {
	c := dockerCase{
		name: "mixed",
		yaml: `cap_drop: [ALL]
cap_add: [NET_BIND_SERVICE, NET_ADMIN]
security_opt: ['no-new-privileges:true', 'seccomp=unconfined']
read_only: true
pids_limit: 128
hostname: api
dns: [1.1.1.1]
sysctls: {net.core.somaxconn: 512}
user: '1000:1000'
group_add: [44]
devices: ['/dev/dri:/dev/dri']
runtime: nvidia
privileged: true
pid: host
gpus: all`,
	}
	s := planDocker(t, c)
	require.Equal(t, (&agentprotocol.DockerOptions{
		CapDrop: []string{"ALL"}, CapAdd: []string{"NET_BIND_SERVICE"}, NoNewPrivileges: true, ReadOnlyRootfs: true, PidsLimit: 128,
		Hostname: "api", DNS: []string{"1.1.1.1"}, Sysctls: map[string]string{"net.core.somaxconn": "512"}, User: "1000:1000", GroupAdd: []string{"44"},
	}).Normalized(), s.Docker)
	for _, k := range []string{"cap_add.NET_ADMIN", "security_opt", "devices", "runtime", "privileged", "pid", "gpus"} {
		found := false
		for _, n := range s.Notes {
			found = found || (n.Key == "app."+k && strings.Contains(n.Message, needsUnrestricted))
		}
		require.True(t, found, "%s must say it needs an unrestricted server: %+v", k, s.Notes)
	}
	require.False(t, s.Docker.NeedsUnrestricted())

	// The same file on an unrestricted server applies all of it.
	c.unrestricted = true
	u := planDocker(t, c)
	require.True(t, u.Docker.NeedsUnrestricted())
	require.Equal(t, []string{"devices", "gpus", "runtime", "privileged", "pidHost", "seccompUnconfined", "capAdd NET_ADMIN"}, u.Docker.UnrestrictedFields())
	require.NoError(t, u.Docker.Validate(true))
	require.Error(t, u.Docker.Validate(false))
}

// A managed database does not take container options; the review says so for
// the new keys too.
func TestDockerOptionsOnAManagedDatabaseAreNoted(t *testing.T) {
	src := ComposeSource{Compose: "services:\n  db:\n    image: postgres:16\n    user: '999:999'\n    volumes: ['data:/var/lib/postgresql/data']\nvolumes:\n  data: {}\n"}
	r, err := Analyze(src, ComposeChoices{}, Context{})
	require.NoError(t, err)
	db := svc(t, r.Plan(), "db")
	require.NotNil(t, db.Managed)
	if db.Managed.Enabled {
		require.Nil(t, db.Docker)
		require.Contains(t, noteKeys(db), "db.docker")
	}
}
