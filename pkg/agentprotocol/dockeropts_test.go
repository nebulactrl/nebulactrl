package agentprotocol

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDockerOptionsValid(t *testing.T) {
	o := &DockerOptions{
		CapAdd:     []string{"net_bind_service", "CAP_SYS_NICE", "IPC_LOCK"},
		Ulimits:    []Ulimit{{Name: "nofile", Soft: 1024, Hard: 65536}, {Name: "memlock", Soft: -1, Hard: -1}},
		ShmSizeMB:  512,
		ExtraHosts: []string{"db.internal:10.0.0.5", "gw:host-gateway", "v6:fd00::1"},
		Labels:     map[string]string{"com.example.team": "payments", "app.version": "3"},
		Init:       true,
		StopSignal: "sigquit",
		Tmpfs:      []TmpfsMount{{Path: "/tmp/cache", SizeMB: 128, Mode: "1777"}, {Path: "/run/app"}},
	}
	require.NoError(t, o.Validate(false))
	n := o.Normalized()
	require.Equal(t, []string{"IPC_LOCK", "NET_BIND_SERVICE", "SYS_NICE"}, n.CapAdd)
	require.Equal(t, "SIGQUIT", n.StopSignal)
	require.Equal(t, "memlock", n.Ulimits[0].Name, "ulimits sorted by name")
	require.Equal(t, "/run/app", n.Tmpfs[0].Path)
	require.Equal(t, "size=128m,mode=1777", n.Tmpfs[1].TmpfsOption())
	require.Equal(t, "size=64m", n.Tmpfs[0].TmpfsOption(), "default tmpfs size")
}

func TestDockerOptionsRejects(t *testing.T) {
	cases := map[string]DockerOptions{
		"cap not listed":         {CapAdd: []string{"SYS_MODULE"}},
		"cap ALL":                {CapAdd: []string{"ALL"}},
		"cap twice":              {CapAdd: []string{"CHOWN", "CAP_CHOWN"}},
		"ulimit core":            {Ulimits: []Ulimit{{Name: "core", Soft: 0, Hard: 0}}},
		"ulimit twice":           {Ulimits: []Ulimit{{Name: "nofile", Soft: 1, Hard: 1}, {Name: "nofile", Soft: 2, Hard: 2}}},
		"ulimit soft > hard":     {Ulimits: []Ulimit{{Name: "nofile", Soft: 10, Hard: 5}}},
		"ulimit soft unlimited":  {Ulimits: []Ulimit{{Name: "nofile", Soft: -1, Hard: 5}}},
		"ulimit negative":        {Ulimits: []Ulimit{{Name: "nofile", Soft: -2, Hard: -2}}},
		"ulimit nproc":           {Ulimits: []Ulimit{{Name: "nproc", Soft: 10, Hard: 10}}},
		"shm negative":           {ShmSizeMB: -1},
		"shm huge":               {ShmSizeMB: MaxShmSizeMB + 1},
		"host no ip":             {ExtraHosts: []string{"a:b"}},
		"host no colon":          {ExtraHosts: []string{"a"}},
		"host bad name":          {ExtraHosts: []string{"-a:10.0.0.1"}},
		"label docker":           {Labels: map[string]string{"com.docker.compose.project": "x"}},
		"label nebulactrl":       {Labels: map[string]string{"NebulaCtrl.service": "x"}},
		"label traefik":          {Labels: map[string]string{"traefik.enable": "true"}},
		"label bad key":          {Labels: map[string]string{"bad key": "x"}},
		"label newline":          {Labels: map[string]string{"a": "x\ny"}},
		"stop signal":            {StopSignal: "SIGSEGV"},
		"entrypoint blank first": {Entrypoint: []string{" ", "x"}},
		"entrypoint too many":    {Entrypoint: make([]string, MaxEntrypointArgs+1)},
		"entrypoint nul":         {Entrypoint: []string{"/bin/sh", "a\x00b"}},
		"entrypoint too long":    {Entrypoint: []string{strings.Repeat("a", MaxEntrypointArgLen+1)}},
		"tmpfs relative":         {Tmpfs: []TmpfsMount{{Path: "tmp"}}},
		"tmpfs root":             {Tmpfs: []TmpfsMount{{Path: "/"}}},
		"tmpfs dotdot":           {Tmpfs: []TmpfsMount{{Path: "/a/../etc"}}},
		"tmpfs proc":             {Tmpfs: []TmpfsMount{{Path: "/proc/x"}}},
		"tmpfs dev":              {Tmpfs: []TmpfsMount{{Path: "/dev"}}},
		"tmpfs comma":            {Tmpfs: []TmpfsMount{{Path: "/a,b"}}},
		"tmpfs mode":             {Tmpfs: []TmpfsMount{{Path: "/a", Mode: "9999"}}},
		"tmpfs twice":            {Tmpfs: []TmpfsMount{{Path: "/a"}, {Path: "/a"}}},
		"too many labels":        {Labels: manyLabels(MaxDockerLabels + 1)},
	}
	for name, o := range cases {
		t.Run(name, func(t *testing.T) { require.Error(t, o.Validate(false)) })
	}
	// NET_ADMIN and SYS_ADMIN are well-formed but need an unrestricted server; nothing else opens up.
	adv := &DockerOptions{CapAdd: []string{"SYS_ADMIN", "cap_net_admin"}}
	var need *RequiresUnrestrictedError
	require.ErrorAs(t, adv.Validate(false), &need)
	require.Equal(t, []string{"capAdd NET_ADMIN", "capAdd SYS_ADMIN"}, need.Fields)
	require.NoError(t, adv.Validate(true))
	require.Error(t, (&DockerOptions{CapAdd: []string{"SYS_MODULE"}}).Validate(true), "unrestricted does not open the whole list")
	require.True(t, adv.NeedsUnrestricted())
	require.False(t, (&DockerOptions{CapAdd: []string{"CHOWN"}}).NeedsUnrestricted())
	require.False(t, (*DockerOptions)(nil).NeedsUnrestricted())
}

func manyLabels(n int) map[string]string {
	m := map[string]string{}
	for i := range n {
		m["k"+strings.Repeat("x", i%5)+string(rune('a'+i%26))+string(rune('a'+i/26))] = "v"
	}
	return m
}

func TestDockerOptionsNormalizeAndZero(t *testing.T) {
	require.Nil(t, (*DockerOptions)(nil).Normalized())
	require.Nil(t, (&DockerOptions{}).Normalized())
	require.Nil(t, (&DockerOptions{Labels: map[string]string{}}).Normalized(), "an empty set is no set")
	require.True(t, (*DockerOptions)(nil).IsZero())
	require.False(t, (&DockerOptions{Init: true}).IsZero())
	require.NoError(t, (*DockerOptions)(nil).Validate(false))
}

func TestDockerOptionsWarnings(t *testing.T) {
	require.Empty(t, (&DockerOptions{CapAdd: []string{"CHOWN"}}).Warnings())
	require.Len(t, (&DockerOptions{CapAdd: []string{"SYS_PTRACE"}}).Warnings(), 1)
	require.Len(t, (&DockerOptions{CapAdd: []string{"SYS_ADMIN"}}).Warnings(), 1)
	require.Len(t, (&DockerOptions{ReadOnlyRootfs: true}).Warnings(), 1, "read-only root without tmpfs")
	require.Empty(t, (&DockerOptions{ReadOnlyRootfs: true, Tmpfs: []TmpfsMount{{Path: "/tmp"}}}).Warnings())
	require.NotEmpty(t, (&DockerOptions{Privileged: true}).Warnings())
}

func TestDockerOptionsEntrypoint(t *testing.T) {
	o := &DockerOptions{Entrypoint: []string{"/app/entry.sh", "--serve", "a b", ""}}
	require.NoError(t, o.Validate(false), "later elements may be empty")
	require.False(t, o.IsZero(), "an entrypoint alone is a set of options")
	n := o.Normalized()
	require.Equal(t, o.Entrypoint, n.Entrypoint, "order is kept")
	n.Entrypoint[0] = "changed"
	require.Equal(t, "/app/entry.sh", o.Entrypoint[0], "Normalized copies the argv")
	require.NoError(t, (&DockerOptions{Entrypoint: make([]string, 0)}).Validate(false))
}

func TestDockerOptionsHardeningFieldsValid(t *testing.T) {
	o := &DockerOptions{
		CapDrop: []string{"all", "CAP_NET_RAW"}, CapAdd: []string{"NET_BIND_SERVICE"}, NoNewPrivileges: true, ReadOnlyRootfs: true,
		PidsLimit: 512, Hostname: "Rabbit-1", DNS: []string{"10.0.0.2", "2606:4700:4700::1111"},
		Sysctls:     map[string]string{"net.core.somaxconn": "4096", "kernel.sem": "250 32000 32 128", "net.ipv4.ip_unprivileged_port_start": "0"},
		HostSysctls: map[string]int64{"vm.max_map_count": 262144},
		User:        "1000:1000", GroupAdd: []string{"122", "44"},
	}
	require.NoError(t, o.Validate(false), "the whole ungated tier works on a restricted server")
	require.False(t, o.NeedsUnrestricted())
	require.False(t, o.IsZero())
	n := o.Normalized()
	require.Equal(t, []string{"ALL", "NET_RAW"}, n.CapDrop)
	require.Equal(t, "rabbit-1", n.Hostname)

	for _, u := range []string{"1000", "1000:1000", "app", "app:app", "www-data:33", "0", "_svc", "2147483647"} {
		require.NoError(t, (&DockerOptions{User: u}).Validate(false), u)
	}
}

func TestDockerOptionsHardeningFieldsRejected(t *testing.T) {
	cases := map[string]DockerOptions{
		"capDrop unknown":       {CapDrop: []string{"NOT_A_CAP"}},
		"capDrop twice":         {CapDrop: []string{"NET_RAW", "cap_net_raw"}},
		"capDrop and capAdd":    {CapDrop: []string{"CHOWN"}, CapAdd: []string{"CHOWN"}},
		"pids negative":         {PidsLimit: -1},
		"pids huge":             {PidsLimit: MaxPidsLimit + 1},
		"hostname underscore":   {Hostname: "my_host"},
		"hostname dot":          {Hostname: "a.b"},
		"hostname dash edge":    {Hostname: "-a"},
		"hostname too long":     {Hostname: strings.Repeat("a", 64)},
		"dns hostname":          {DNS: []string{"dns.example.com"}},
		"dns with port":         {DNS: []string{"10.0.0.1:53"}},
		"dns unspecified":       {DNS: []string{"0.0.0.0"}},
		"dns multicast":         {DNS: []string{"224.0.0.1"}},
		"dns twice":             {DNS: []string{"10.0.0.1", "10.0.0.1"}},
		"dns four":              {DNS: []string{"10.0.0.1", "10.0.0.2", "10.0.0.3", "10.0.0.4"}},
		"sysctl host wide":      {Sysctls: map[string]string{"vm.max_map_count": "262144"}},
		"sysctl vm other":       {Sysctls: map[string]string{"vm.swappiness": "1"}},
		"sysctl not listed":     {Sysctls: map[string]string{"net.ipv4.tcp_rmem": "4096"}},
		"sysctl kernel":         {Sysctls: map[string]string{"kernel.core_pattern": "x"}},
		"sysctl value text":     {Sysctls: map[string]string{"net.core.somaxconn": "high"}},
		"sysctl value newline":  {Sysctls: map[string]string{"net.core.somaxconn": "1\n2"}},
		"sysctl value negative": {Sysctls: map[string]string{"net.core.somaxconn": "-1"}},
		"sysctl value empty":    {Sysctls: map[string]string{"net.core.somaxconn": ""}},
		"hostSysctl unknown":    {HostSysctls: map[string]int64{"kernel.core_pattern": 1}},
		"hostSysctl zero":       {HostSysctls: map[string]int64{"vm.max_map_count": 0}},
		"user injection":        {User: "root; reboot"},
		"user too many parts":   {User: "1:2:3"},
		"user empty gid":        {User: "1000:"},
		"user too big":          {User: "4294967295"},
		"user uppercase":        {User: "Root"},
		"groupAdd zero":         {GroupAdd: []string{"0"}},
		"groupAdd name":         {GroupAdd: []string{"render"}},
		"groupAdd too big":      {GroupAdd: []string{"65536"}},
		"groupAdd twice":        {GroupAdd: []string{"44", "44"}},
		"groupAdd nine":         {GroupAdd: []string{"1", "2", "3", "4", "5", "6", "7", "8", "9"}},
	}
	for name, o := range cases {
		t.Run(name, func(t *testing.T) {
			err := o.Validate(true)
			require.Error(t, err)
			var need *RequiresUnrestrictedError
			require.NotErrorAs(t, err, &need, "a malformed option is a format error, not a policy one")
		})
	}
	for _, k := range AllowedSysctls {
		require.NoError(t, (&DockerOptions{Sysctls: map[string]string{k: "1"}}).Validate(false), k)
	}
	require.Error(t, (&DockerOptions{Sysctls: manySysctls(MaxDockerSysctls + 1)}).Validate(true))
}

func manySysctls(n int) map[string]string {
	m := map[string]string{}
	for _, k := range AllowedSysctls[:n] {
		m[k] = "1"
	}
	return m
}

func TestDockerOptionsUnrestrictedTier(t *testing.T) {
	good := map[string]DockerOptions{
		"devices":    {Devices: []DeviceMapping{{HostPath: "/dev/dri/renderD128", ContainerPath: "/dev/dri/renderD128", Permissions: "rw"}, {HostPath: "/dev/kfd"}}},
		"gpus all":   {GPUs: &GPURequest{Count: -1, Driver: "nvidia", Capabilities: []string{"compute", "UTILITY"}}},
		"gpus two":   {GPUs: &GPURequest{Count: 2, Driver: "nvidia"}},
		"runtime":    {Runtime: "runsc"},
		"runtime 2":  {Runtime: "sysbox-runc"},
		"privileged": {Privileged: true},
		"pidHost":    {PidHost: true},
		"seccomp":    {SeccompUnconfined: true},
		"apparmor":   {ApparmorUnconfined: true},
		"NET_ADMIN":  {CapAdd: []string{"NET_ADMIN"}},
		"SYS_ADMIN":  {CapAdd: []string{"cap_sys_admin"}},
	}
	for name, o := range good {
		t.Run("good "+name, func(t *testing.T) {
			require.NoError(t, o.Validate(true))
			require.True(t, o.NeedsUnrestricted())
			var need *RequiresUnrestrictedError
			require.ErrorAs(t, o.Validate(false), &need, "a restricted server refuses it with the policy error")
			require.NotEmpty(t, need.Fields)
			require.Contains(t, need.Error(), "unrestricted server")
		})
	}

	bad := map[string]DockerOptions{
		"device relative":     {Devices: []DeviceMapping{{HostPath: "dev/kvm"}}},
		"device outside dev":  {Devices: []DeviceMapping{{HostPath: "/etc/shadow"}}},
		"device dev itself":   {Devices: []DeviceMapping{{HostPath: "/dev/"}}},
		"device dotdot":       {Devices: []DeviceMapping{{HostPath: "/dev/../etc/passwd"}}},
		"device colon":        {Devices: []DeviceMapping{{HostPath: "/dev/a:b"}}},
		"device container":    {Devices: []DeviceMapping{{HostPath: "/dev/kvm", ContainerPath: "kvm"}}},
		"device perms":        {Devices: []DeviceMapping{{HostPath: "/dev/kvm", Permissions: "rx"}}},
		"device perms twice":  {Devices: []DeviceMapping{{HostPath: "/dev/kvm", Permissions: "rr"}}},
		"device same inside":  {Devices: []DeviceMapping{{HostPath: "/dev/a"}, {HostPath: "/dev/b", ContainerPath: "/dev/a"}}},
		"gpus zero":           {GPUs: &GPURequest{Count: 0, Driver: "nvidia"}},
		"gpus negative":       {GPUs: &GPURequest{Count: -2, Driver: "nvidia"}},
		"gpus too many":       {GPUs: &GPURequest{Count: MaxDockerGPUs + 1, Driver: "nvidia"}},
		"gpus driver":         {GPUs: &GPURequest{Count: 1, Driver: "amd"}},
		"gpus no driver":      {GPUs: &GPURequest{Count: 1}},
		"gpus capability":     {GPUs: &GPURequest{Count: 1, Driver: "nvidia", Capabilities: []string{"root"}}},
		"gpus capability dup": {GPUs: &GPURequest{Count: 1, Driver: "nvidia", Capabilities: []string{"video", "VIDEO"}}},
		"runtime spaces":      {Runtime: "my runtime"},
		"runtime path":        {Runtime: "/usr/bin/runc"},
	}
	for name, o := range bad {
		t.Run("bad "+name, func(t *testing.T) { require.Error(t, o.Validate(true)) })
	}
	require.Error(t, (&DockerOptions{Devices: make([]DeviceMapping, MaxDockerDevices+1)}).Validate(true))

	// The format is checked first: a typo on a restricted server is a typo.
	err := (&DockerOptions{Privileged: true, DNS: []string{"nope"}}).Validate(false)
	var need *RequiresUnrestrictedError
	require.Error(t, err)
	require.NotErrorAs(t, err, &need)

	// UnrestrictedFields lists everything, deterministically.
	all := &DockerOptions{Devices: []DeviceMapping{{HostPath: "/dev/kvm"}}, GPUs: &GPURequest{Count: 1, Driver: "nvidia"}, Runtime: "runsc",
		Privileged: true, PidHost: true, SeccompUnconfined: true, ApparmorUnconfined: true, CapAdd: []string{"SYS_ADMIN", "NET_ADMIN", "CHOWN"}}
	require.Equal(t, []string{"devices", "gpus", "runtime", "privileged", "pidHost", "seccompUnconfined", "apparmorUnconfined", "capAdd NET_ADMIN", "capAdd SYS_ADMIN"},
		all.UnrestrictedFields())
	require.Empty(t, (&DockerOptions{CapAdd: []string{"CHOWN"}, CapDrop: []string{"ALL"}, ReadOnlyRootfs: true}).UnrestrictedFields())
}

func TestDockerOptionsNormalizeCopiesNewFields(t *testing.T) {
	o := &DockerOptions{
		Devices: []DeviceMapping{{HostPath: "/dev/kvm"}}, GPUs: &GPURequest{Count: 1, Driver: "nvidia", Capabilities: []string{"Video", "compute", "video"}},
		DNS: []string{"10.0.0.1"}, GroupAdd: []string{"44"}, Sysctls: map[string]string{"net.core.somaxconn": "1"},
	}
	n := o.Normalized()
	require.Equal(t, []string{"compute", "video"}, n.GPUs.Capabilities, "GPU capabilities are lower-cased, de-duplicated and sorted")
	n.GPUs.Capabilities[0] = "changed"
	n.DNS[0], n.GroupAdd[0], n.Devices[0].HostPath = "changed", "changed", "changed"
	require.Equal(t, []string{"Video", "compute", "video"}, o.GPUs.Capabilities, "Normalized copies the GPU request")
	require.Equal(t, "10.0.0.1", o.DNS[0])
	require.Equal(t, "44", o.GroupAdd[0])
	require.Equal(t, "/dev/kvm", o.Devices[0].HostPath)

	// Every new field alone is a set of options.
	for name, set := range map[string]DockerOptions{
		"capDrop": {CapDrop: []string{"ALL"}}, "noNewPrivileges": {NoNewPrivileges: true}, "readOnlyRootfs": {ReadOnlyRootfs: true},
		"pidsLimit": {PidsLimit: 1}, "hostname": {Hostname: "a"}, "dns": {DNS: []string{"1.1.1.1"}}, "sysctls": {Sysctls: map[string]string{"a": "1"}},
		"hostSysctls": {HostSysctls: map[string]int64{"vm.max_map_count": 1}}, "user": {User: "1"}, "groupAdd": {GroupAdd: []string{"1"}},
		"devices": {Devices: []DeviceMapping{{HostPath: "/dev/a"}}}, "gpus": {GPUs: &GPURequest{}}, "runtime": {Runtime: "x"},
		"privileged": {Privileged: true}, "pidHost": {PidHost: true}, "seccomp": {SeccompUnconfined: true}, "apparmor": {ApparmorUnconfined: true},
	} {
		require.False(t, set.IsZero(), name)
		require.NotNil(t, set.Normalized(), name)
	}
}

func TestDockerOptionsReadOnlyRootfsVersusFiles(t *testing.T) {
	ro := &DockerOptions{ReadOnlyRootfs: true}
	require.NoError(t, ro.ValidateFiles(nil, nil), "no files, no problem")
	require.Error(t, ro.ValidateFiles([]string{"/etc/app.conf"}, nil), "a file in the read-only root")
	require.Error(t, ro.ValidateFiles([]string{"/etc/app.conf"}, []string{"/data"}), "a volume elsewhere does not help")
	require.Error(t, ro.ValidateFiles([]string{"/database/x.conf"}, []string{"/data"}), "prefix of a name is not the directory")
	require.NoError(t, ro.ValidateFiles([]string{"/data/app.conf", "/data/sub/b"}, []string{"/data"}))
	require.NoError(t, ro.ValidateFiles([]string{"/data"}, []string{"/data/"}), "a file at the mount itself")
	require.Error(t, ro.ValidateFiles([]string{"/data/../etc/x"}, []string{"/data"}), "paths are cleaned first")
	require.NoError(t, (&DockerOptions{}).ValidateFiles([]string{"/etc/app.conf"}, nil), "a writable root takes files anywhere")
	require.NoError(t, (*DockerOptions)(nil).ValidateFiles([]string{"/etc/app.conf"}, nil))
	// Tmpfs paths do not make files writable: Docker mounts the tmpfs over the copy at start.
	require.Error(t, (&DockerOptions{ReadOnlyRootfs: true, Tmpfs: []TmpfsMount{{Path: "/scratch"}}}).ValidateFiles([]string{"/scratch/a"}, nil))
}
