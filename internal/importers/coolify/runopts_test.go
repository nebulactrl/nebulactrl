package coolify

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestShellWords(t *testing.T) {
	got, err := shellWords(`sh -c "echo 'a b' && run \"x\" \$HOME" plain\ word 'single quoted' mixed"dq"'sq'`)
	require.NoError(t, err)
	require.Equal(t, []string{"sh", "-c", `echo 'a b' && run "x" $HOME`, "plain word", "single quoted", "mixeddqsq"}, got)

	got, err = shellWords("--a 1 \\\n --b 2\n--c 3")
	require.NoError(t, err)
	require.Equal(t, []string{"--a", "1", "--b", "2", "--c", "3"}, got)

	got, err = shellWords(`a "" b ''`)
	require.NoError(t, err)
	require.Equal(t, []string{"a", "", "b", ""}, got)

	_, err = shellWords(`a "unterminated`)
	require.Error(t, err)
	_, err = shellWords(`a 'unterminated`)
	require.Error(t, err)
}

func TestCommandArgv(t *testing.T) {
	require.Equal(t, []string{"php", "artisan", "migrate", "--force"}, commandArgv("php artisan migrate --force"))
	require.Equal(t, []string{"sh", "-c", "php artisan migrate && php artisan cache:clear"}, commandArgv("php artisan migrate && php artisan cache:clear"))
	require.Equal(t, []string{"sh", "-c", "echo $APP_ENV"}, commandArgv("echo $APP_ENV"))
	require.Equal(t, []string{"a", "b c"}, commandArgv(`["a","b c"]`))
	require.Equal(t, []string{"sh", "-c", `echo "unterminated`}, commandArgv(`echo "unterminated`))
	require.Nil(t, commandArgv("  "))
}

func TestEntrypointOptionIsSplitIntoArgv(t *testing.T) {
	cases := map[string][]string{
		`--entrypoint "docker-php-serversideup-entrypoint bun /var/www/html/bootstrap/ssr/ssr.js"`: {"docker-php-serversideup-entrypoint", "bun", "/var/www/html/bootstrap/ssr/ssr.js"},
		`--entrypoint=php`:                                 {"php"},
		`--entrypoint 'sh -c "echo hi there"'`:             {"sh", "-c", "echo hi there"},
		`--entrypoint "python -c \"print('hi')\""`:         {"python", "-c", "print('hi')"},
		`--entrypoint '["/bin/app","--flag","two words"]'`: {"/bin/app", "--flag", "two words"},
		"--entrypoint \"a b\"\n--init":                     {"a", "b"},
		`--init --entrypoint="docker-php-serversideup-entrypoint php /var/www/html/artisan queue:work --tries=3 --timeout=60"`: {"docker-php-serversideup-entrypoint", "php", "/var/www/html/artisan", "queue:work", "--tries=3", "--timeout=60"},
	}
	for in, want := range cases {
		r := mapRunOptions(in, false)
		require.NotNil(t, r.Docker, in)
		require.Equal(t, want, r.Docker.Entrypoint, in)
		require.Empty(t, r.Unsupported, in)
	}
}

func TestRunOptionsMapToTypedOptions(t *testing.T) {
	in := `--cap-add=SYS_PTRACE --cap-add CHOWN --cap-drop ALL --shm-size 1g --ulimit nofile=65536:65536 --ulimit memlock=-1 --add-host db.internal:10.0.0.5 ` +
		`--init --tmpfs /run:rw,size=64m,mode=1777 --hostname Web-1 --dns 1.1.1.1 --sysctl net.core.somaxconn=1024 --user 1000:1000 --group-add 44 ` +
		`--pids-limit 512 --read-only --stop-signal sigquit --security-opt no-new-privileges`
	r := mapRunOptions(in, false)
	require.Empty(t, r.NeedsUnrestricted)
	require.Empty(t, r.Unsupported, "%v", r.Unsupported)
	d := r.Docker
	require.NotNil(t, d)
	require.Equal(t, []string{"CHOWN", "SYS_PTRACE"}, d.CapAdd)
	require.Equal(t, []string{"ALL"}, d.CapDrop)
	require.Equal(t, 1024, d.ShmSizeMB)
	require.Len(t, d.Ulimits, 2)
	require.Equal(t, []string{"db.internal:10.0.0.5"}, d.ExtraHosts)
	require.True(t, d.Init)
	require.Equal(t, "/run", d.Tmpfs[0].Path)
	require.Equal(t, 64, d.Tmpfs[0].SizeMB)
	require.Equal(t, "1777", d.Tmpfs[0].Mode)
	require.Equal(t, "web-1", d.Hostname)
	require.Equal(t, []string{"1.1.1.1"}, d.DNS)
	require.Equal(t, map[string]string{"net.core.somaxconn": "1024"}, d.Sysctls)
	require.Equal(t, "1000:1000", d.User)
	require.Equal(t, []string{"44"}, d.GroupAdd)
	require.EqualValues(t, 512, d.PidsLimit)
	require.True(t, d.ReadOnlyRootfs)
	require.Equal(t, "SIGQUIT", d.StopSignal)
	require.True(t, d.NoNewPrivileges)
}

func TestRunOptionsThatNeedAnUnrestrictedServer(t *testing.T) {
	in := `--privileged --device /dev/dri:/dev/dri --gpus all --cap-add NET_ADMIN --security-opt seccomp=unconfined --pid host --runtime nvidia`
	r := mapRunOptions(in, false)
	require.ElementsMatch(t, []string{"privileged", "devices", "gpus", "capAdd NET_ADMIN", "seccompUnconfined", "pidHost", "runtime"}, r.NeedsUnrestricted)
	require.Nil(t, r.Docker, "nothing was applied")
	require.Len(t, r.Unsupported, 7, "each refused option is listed: %v", r.Unsupported)
	for _, n := range r.Notes {
		require.Contains(t, n.Message, "unrestricted server", n.Key)
	}
	dangerous := 0
	for _, n := range r.Notes {
		if n.Severity == SevDanger {
			dangerous++
		}
	}
	require.GreaterOrEqual(t, dangerous, 4)

	r = mapRunOptions(in, true)
	require.NotNil(t, r.Docker)
	require.True(t, r.Docker.Privileged)
	require.True(t, r.Docker.PidHost)
	require.Equal(t, "/dev/dri", r.Docker.Devices[0].HostPath)
	require.Equal(t, -1, r.Docker.GPUs.Count)
	require.Equal(t, "nvidia", r.Docker.Runtime)
	require.Empty(t, r.Unsupported)
}

func TestUnsupportedRunOptionsAreListedNeverDropped(t *testing.T) {
	in := `--network host --restart always -p 8080:80 -v /a:/b -e A=B --label x=y --memory-swap 2g --cpuset-cpus 0-1 --ip 10.0.0.9 --frobnicate yes -it --rm ` +
		`--ulimit nproc=100 --security-opt label=disable stray-word`
	r := mapRunOptions(in, false)
	for _, want := range []string{"--network", "--restart", "--publish", "--volume", "--env", "--label", "--memory-swap", "--cpuset-cpus", "--ip", "--frobnicate", "--interactive", "--tty", "ulimit nproc", "security-opt label=disable"} {
		found := false
		for _, u := range r.Unsupported {
			if strings.Contains(u, strings.TrimPrefix(want, "--")) {
				found = true
			}
		}
		require.True(t, found, "%s missing from %v", want, r.Unsupported)
	}
	require.Nil(t, r.Docker)
	require.Contains(t, notesText(r.Notes), "stray-word")
	require.NotContains(t, strings.Join(r.Unsupported, "|"), "--rm", "rm changes nothing")
}

func notesText(n []CoolifyNote) string {
	var parts []string
	for _, x := range n {
		parts = append(parts, x.Message)
	}
	return strings.Join(parts, "\n")
}

func TestRunOptionsLimitsAndShortFlags(t *testing.T) {
	r := mapRunOptions(`-m 512m --cpus 1.5 -u 1000 -h Box`, false)
	require.Equal(t, 512, r.MemoryMB)
	require.Equal(t, 1.5, r.CPUs)
	require.Equal(t, "1000", r.Docker.User)
	require.Equal(t, "box", r.Docker.Hostname)

	r = mapRunOptions(`--pids-limit -1`, false)
	require.Nil(t, r.Docker)
	require.Len(t, r.Unsupported, 1)
}

func TestRunOptionsErrorsAreReported(t *testing.T) {
	r := mapRunOptions(`--entrypoint "unterminated`, false)
	require.Nil(t, r.Docker)
	require.NotEmpty(t, r.Unsupported)
	require.Equal(t, SevWarning, r.Notes[0].Severity)

	r = mapRunOptions("", false)
	require.Nil(t, r.Docker)
	require.Empty(t, r.Notes)

	r = mapRunOptions(`--hostname "Not Valid!" --ulimit nofile=abc --shm-size nonsense`, false)
	require.Nil(t, r.Docker)
	require.Len(t, r.Unsupported, 3)
}

func TestParseSize(t *testing.T) {
	for in, want := range map[string]int{"512m": 512, "2g": 2048, "1gb": 1024, "65536k": 64, "1048576": 1, "1.5g": 1536, "100": 1} {
		got, ok := parseSizeMB(in)
		require.True(t, ok, in)
		require.Equal(t, want, got, in)
	}
	for _, in := range []string{"", "0", "abc", "-1m"} {
		_, ok := parseSizeMB(in)
		require.False(t, ok, in)
	}
}
