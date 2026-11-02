package coolify

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

func TestParseRunFlagsReadsLongShortAndInlineForms(t *testing.T) {
	cases := []struct {
		in    string
		flags []runFlag
		stray []string
	}{
		{"-it", []runFlag{{Name: "interactive"}, {Name: "tty"}}, nil},
		{"-u 1000 -h box", []runFlag{{"user", "1000"}, {"hostname", "box"}}, nil},
		{"-u=1000", []runFlag{{"user", "1000"}}, nil},
		{"-m512m", []runFlag{{"memory", "512m"}}, nil},
		{"-d -q -P", []runFlag{{Name: "detach"}, {Name: "quiet"}, {Name: "publish-all"}}, nil},
		{"-x", []runFlag{{Name: "-x"}}, nil},
		{"--cpus=1.5 --init extra", []runFlag{{"cpus", "1.5"}, {Name: "init"}}, []string{"extra"}},
		{"--memory -1", []runFlag{{"memory", "-1"}}, nil},
		{"-5 --hostname", []runFlag{{Name: "hostname"}}, []string{"-5"}},
		{"nginx:latest --user=app", []runFlag{{"user", "app"}}, []string{"nginx:latest"}},
		{`--label "a b=c d"`, []runFlag{{"label", "a b=c d"}}, nil},
	}
	for _, tc := range cases {
		flags, stray, err := parseRunFlags(tc.in)
		require.NoError(t, err, tc.in)
		require.Equal(t, tc.flags, flags, tc.in)
		require.Equal(t, tc.stray, stray, tc.in)
	}
	_, _, err := parseRunFlags(`--entrypoint "unterminated`)
	require.Error(t, err)
}

func TestRunOptionsWithoutAValueAreIgnoredWithANote(t *testing.T) {
	r := mapRunOptions(`--hostname --dns --user --group-add --stop-signal --runtime --entrypoint`, false)
	require.Nil(t, r.Docker, "nothing without a value is applied")
	require.Empty(t, r.Unsupported)
	var keys []string
	for _, n := range r.Notes {
		require.Equal(t, SevWarning, n.Severity)
		require.Contains(t, n.Message, "no value")
		keys = append(keys, n.Key)
	}
	require.Equal(t, []string{"--hostname", "--dns", "--user", "--group-add", "--stop-signal", "--runtime", "--entrypoint"}, keys)
}

func TestRunOptionsThatCannotBeReadAreListedWithTheirReason(t *testing.T) {
	cases := []struct{ in, want string }{
		{`--entrypoint 'a "b'`, "entrypoint"},
		{`--sysctl noequals`, "--sysctl noequals: use key=value"},
		{`--ulimit nofile`, "--ulimit nofile: use name=soft[:hard]"},
		{`--ulimit nofile=1:x`, "--ulimit nofile=1:x: not a number"},
		{`--ulimit nofile=x`, "--ulimit nofile=x: not a number"},
		{`--ulimit nproc=5`, "--ulimit nproc=5: only nofile and memlock are supported (use --pids-limit for processes)"},
		{`--tmpfs :size=1m`, "--tmpfs :size=1m: no path"},
		{`--gpus 0`, "--gpus 0: use all, a count or device=<ids>"},
		{`--gpus abc`, "--gpus abc: use all, a count or device=<ids>"},
		{`--gpus device=`, "--gpus device=: no device ids"},
		{`--pid container:web`, "--pid container:web: only --pid=host (on an unrestricted server) is supported"},
		{`--memory abc`, "--memory abc: not a size"},
		{`--cpus abc`, "--cpus abc: not a number"},
		{`--cpus 0`, "--cpus 0: not a number"},
		{`--shm-size 0`, "--shm-size 0: not a size"},
		{`--pids-limit abc`, "--pids-limit abc: only a positive limit is supported"},
		{`--security-opt label=disable`, "--security-opt label=disable: only no-new-privileges and seccomp/apparmor=unconfined are supported"},
	}
	for _, tc := range cases {
		for _, unrestricted := range []bool{false, true} {
			r := mapRunOptions(tc.in, unrestricted)
			require.Nil(t, r.Docker, tc.in)
			require.Equal(t, []string{tc.want}, r.Unsupported, tc.in)
			require.NotEmpty(t, r.Notes, "the review shows why: %s", tc.in)
		}
	}

	r := mapRunOptions(`-x`, false)
	require.Nil(t, r.Docker)
	require.Equal(t, []string{"-x: not recognised"}, r.Unsupported)

	long := strings.Repeat("v", 80)
	r = mapRunOptions(`--sysctl `+long, false)
	require.Equal(t, []string{"--sysctl " + strings.Repeat("v", 40) + "…: use key=value"}, r.Unsupported, "a long value is cut in the review")
}

func TestRunOptionsSwitchedOffExplicitlyChangeNothing(t *testing.T) {
	r := mapRunOptions(`--init=false --privileged=false --read-only=false`, false)
	require.Nil(t, r.Docker)
	require.Empty(t, r.Unsupported)
	require.Empty(t, r.NeedsUnrestricted)
	require.Empty(t, r.Notes)

	r = mapRunOptions(`--init=true --read-only=true --tmpfs /tmp`, false)
	require.True(t, r.Docker.Init)
	require.True(t, r.Docker.ReadOnlyRootfs)
}

func TestReadOnlyRootWarnsUnlessTheImageGetsAWritableTmpfs(t *testing.T) {
	r := mapRunOptions(`--read-only`, false)
	require.True(t, r.Docker.ReadOnlyRootfs)
	require.Contains(t, notesText(r.Notes), "root filesystem is read-only")

	r = mapRunOptions(`--read-only --tmpfs /tmp`, false)
	require.True(t, r.Docker.ReadOnlyRootfs)
	require.NotContains(t, notesText(r.Notes), "root filesystem is read-only")
}

func TestRuntimeRuncIsDockersDefaultAndNeedsNothing(t *testing.T) {
	r := mapRunOptions(`--runtime RUNC`, false)
	require.Nil(t, r.Docker)
	require.Empty(t, r.Unsupported)
	require.Empty(t, r.NeedsUnrestricted)
	require.Len(t, r.Notes, 1)
	require.Equal(t, SevInfo, r.Notes[0].Severity)
	require.Contains(t, r.Notes[0].Message, "Docker's default")
}

func TestOptionsThatAreFineAloneButNotTogetherAreRefusedAsAWhole(t *testing.T) {
	r := mapRunOptions(`--user 1000 --cap-add CHOWN --cap-drop CHOWN`, false)
	require.Nil(t, r.Docker, "nothing is applied when the combination is invalid")
	require.Len(t, r.Unsupported, 1)
	require.True(t, strings.HasPrefix(r.Unsupported[0], "all options: "), r.Unsupported[0])
	require.Contains(t, r.Unsupported[0], "CHOWN")
	require.Contains(t, notesText(r.Notes), "The container options were not applied")
}

func TestRunOptionsOnAnUnrestrictedServerKeepWhatARestrictedOneRefuses(t *testing.T) {
	in := `--security-opt apparmor=unconfined --security-opt seccomp:unconfined --security-opt no-new-privileges:true ` +
		`--device /dev/ttyUSB0:rw --device /dev/fuse:/dev/fuse2:rwm --device /dev/snd:/dev/sound --device /dev/kvm ` +
		`--gpus 2 --runtime nvidia`
	r := mapRunOptions(in, true)
	require.Empty(t, r.Unsupported)
	require.NotNil(t, r.Docker)
	require.True(t, r.Docker.ApparmorUnconfined)
	require.True(t, r.Docker.SeccompUnconfined)
	require.True(t, r.Docker.NoNewPrivileges)
	require.ElementsMatch(t, []agentprotocol.DeviceMapping{
		{HostPath: "/dev/ttyUSB0", Permissions: "rw"},
		{HostPath: "/dev/fuse", ContainerPath: "/dev/fuse2", Permissions: "rwm"},
		{HostPath: "/dev/snd", ContainerPath: "/dev/sound"},
		{HostPath: "/dev/kvm"},
	}, r.Docker.Devices)
	require.Equal(t, 2, r.Docker.GPUs.Count)
	require.Equal(t, "nvidia", r.Docker.GPUs.Driver)
	require.NotEmpty(t, r.NeedsUnrestricted, "what needed the unrestricted server is still reported")
	for _, n := range r.Notes {
		if n.Severity == SevWarning {
			require.Contains(t, n.Message, "Applied because the target server is unrestricted", n.Key)
		}
	}

	restricted := mapRunOptions(`--security-opt apparmor=unconfined`, false)
	require.Nil(t, restricted.Docker)
	require.NotEmpty(t, restricted.NeedsUnrestricted)
	require.Equal(t, []string{"security-opt apparmor=unconfined: needs an unrestricted server"}, restricted.Unsupported)
}

func TestGPUOptionsMapToCounts(t *testing.T) {
	for in, want := range map[string]int{"all": -1, "1": 1, "4": 4, "device=0": 1, "device=0,2,3": 3, `"device=1,2"`: 2} {
		g, err := parseGPUs(in)
		require.NoError(t, err, in)
		require.Equal(t, want, g.Count, in)
		require.Equal(t, "nvidia", g.Driver, in)
	}
	for _, in := range []string{"", "0", "-1", "two", "device="} {
		_, err := parseGPUs(in)
		require.Error(t, err, in)
	}
}

func TestTmpfsOptionsKeepSizeAndModeAndExplainTheRest(t *testing.T) {
	var notes []string
	note := func(key, sev, msg string) { notes = append(notes, key+" "+sev+" "+msg) }
	tm, err := parseTmpfs("/run:size=64m,mode=0755,noexec,nosuid,foo=bar", note)
	require.NoError(t, err)
	require.Equal(t, "/run", tm.Path)
	require.Equal(t, 64, tm.SizeMB)
	require.Equal(t, "755", tm.Mode)
	require.Len(t, notes, 3)
	require.Contains(t, notes[0], "The mount flag noexec is not applied")
	require.Contains(t, notes[1], "The mount flag nosuid is not applied")
	require.Contains(t, notes[2], "The tmpfs option foo is not applied")

	tm, err = parseTmpfs("/tmp:mode=000", note)
	require.NoError(t, err)
	require.Equal(t, "0", tm.Mode, "an all-zero mode is kept as 0, not dropped")

	tm, err = parseTmpfs("/tmp", note)
	require.NoError(t, err)
	require.Equal(t, "/tmp", tm.Path)
	require.Zero(t, tm.SizeMB)

	_, err = parseTmpfs("", note)
	require.EqualError(t, err, "no path")
}

func TestParseDeviceReadsPermissionsAndContainerPath(t *testing.T) {
	require.Equal(t, agentprotocol.DeviceMapping{HostPath: "/dev/a"}, parseDevice("/dev/a"))
	require.Equal(t, agentprotocol.DeviceMapping{HostPath: "/dev/a", Permissions: "rw"}, parseDevice("/dev/a:rw"))
	require.Equal(t, agentprotocol.DeviceMapping{HostPath: "/dev/a", ContainerPath: "/dev/b"}, parseDevice("/dev/a:/dev/b"))
	require.Equal(t, agentprotocol.DeviceMapping{HostPath: "/dev/a", ContainerPath: "/dev/b", Permissions: "rwm"}, parseDevice("/dev/a:/dev/b:rwm"))
	require.Equal(t, agentprotocol.DeviceMapping{HostPath: "/dev/a"}, parseDevice("/dev/a:"), "an empty second part is not a permission")
	require.Equal(t, agentprotocol.DeviceMapping{HostPath: "/dev/a", ContainerPath: "rx"}, parseDevice("/dev/a:rx"), "letters other than r, w and m are a path")
}

func TestUlimitsAndSizesAtTheirEdges(t *testing.T) {
	u, err := parseUlimit("NOFILE=1024")
	require.NoError(t, err)
	require.Equal(t, "nofile", u.Name)
	require.EqualValues(t, 1024, u.Soft)
	require.EqualValues(t, 1024, u.Hard, "a single limit is both soft and hard")
	u, err = parseUlimit("memlock=-1:-1")
	require.NoError(t, err)
	require.EqualValues(t, -1, u.Soft)
	require.EqualValues(t, -1, u.Hard)
	u, err = parseUlimit("nofile=1024:4096")
	require.NoError(t, err)
	require.EqualValues(t, 4096, u.Hard)

	mb, ok := parseSizeMB("1t")
	require.True(t, ok)
	require.Equal(t, 1024*1024, mb)
	mb, ok = parseSizeMB("1tb")
	require.True(t, ok)
	require.Equal(t, 1024*1024, mb)
	mb, ok = parseSizeMB("0.5m")
	require.True(t, ok)
	require.Equal(t, 1, mb, "sizes round up to a whole MiB")
	mb, ok = parseSizeMB("1b")
	require.True(t, ok)
	require.Equal(t, 1, mb)
}

func TestShortValueCutsAtFortyBytes(t *testing.T) {
	require.Equal(t, "short", shortValue("short"))
	require.Equal(t, strings.Repeat("a", 40), shortValue(strings.Repeat("a", 40)))
	require.Equal(t, strings.Repeat("a", 40)+"…", shortValue(strings.Repeat("a", 41)))
}

func TestUniqueStringsKeepsFirstOccurrenceOrder(t *testing.T) {
	require.Equal(t, []string{"b", "a", "c"}, uniqueStrings([]string{"b", "a", "b", "c", "a"}))
	require.Nil(t, uniqueStrings(nil))
}

func TestCommonShortFlagsMapToTheirLongOptions(t *testing.T) {
	r := mapRunOptions(`-m512m -u=1000 -d -q`, false)
	require.Equal(t, 512, r.MemoryMB)
	require.Equal(t, "1000", r.Docker.User)
	require.Empty(t, r.Unsupported, "detach and quiet change nothing for a managed container")

	r = mapRunOptions(`-e FOO=bar -P -c 512 -w /app`, false)
	require.Nil(t, r.Docker)
	joined := strings.Join(r.Unsupported, "|")
	for _, want := range []string{"--env", "--publish-all", "--cpu-shares", "--workdir"} {
		require.Contains(t, joined, want)
	}
}

func TestAddHostAcceptsEqualsAsASeparator(t *testing.T) {
	r := mapRunOptions(`--add-host db=10.0.0.5 --add-host cache:10.0.0.6`, false)
	require.ElementsMatch(t, []string{"db:10.0.0.5", "cache:10.0.0.6"}, r.Docker.ExtraHosts)
}
