package templates

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

// withDocker inserts a docker block (and optionally the template flag) into goodTemplate.
func withDocker(block string, unrestricted bool) string {
	y := strings.Replace(goodTemplate, "    port: 8080\n", "    port: 8080\n"+block, 1)
	if unrestricted {
		y = strings.Replace(y, "revision: 1\n", "revision: 1\nrequiresUnrestricted: true\n", 1)
	}
	return y
}

func TestDockerBlockParsesJSONNames(t *testing.T) {
	tpl := parse(t, withDocker(`    docker:
      entrypoint: [/bin/sh, -c]
      shmSizeMb: 512
      capAdd: [IPC_LOCK]
      ulimits:
        - {name: nofile, soft: 262144, hard: 262144}
`, false))
	require.NoError(t, Validate(tpl))
	d := tpl.Services[0].Docker
	require.NotNil(t, d)
	require.Equal(t, []string{"/bin/sh", "-c"}, d.Entrypoint)
	require.Equal(t, 512, d.ShmSizeMB)
	require.Equal(t, []string{"IPC_LOCK"}, d.CapAdd)
	require.Equal(t, int64(262144), d.Ulimits[0].Soft)
}

func TestDockerBlockRejectsUnknownKey(t *testing.T) {
	_, err := Parse([]byte(withDocker("    docker:\n      network: host\n", false)))
	require.ErrorContains(t, err, "network")
}

func TestDockerBlockValidation(t *testing.T) {
	cases := map[string]struct {
		block        string
		unrestricted bool
		want         string // empty: valid
	}{
		"valid":                   {"    docker: {shmSizeMb: 256}\n", false, ""},
		"bad shm":                 {"    docker: {shmSizeMb: 999999}\n", false, "shmSizeMb"},
		"empty block":             {"    docker: {}\n", false, "empty"},
		"device without flag":     {"    docker:\n      devices: [{hostPath: /dev/dri/renderD128}]\n", false, "unrestricted"},
		"device with flag":        {"    docker:\n      devices: [{hostPath: /dev/dri/renderD128}]\n", true, ""},
		"gpu with flag":           {"    docker:\n      gpus: {count: -1, driver: nvidia, capabilities: [compute, utility]}\n", true, ""},
		"flag without option":     {"    docker: {shmSizeMb: 256}\n", true, "no docker option needs"},
		"managed db options":      {"", false, ""},
		"entrypoint input":        {"    docker:\n      entrypoint: [/bin/sh, -c, \"echo ${input:TITLE}\"]\n", false, ""},
		"entrypoint undeclared":   {"    docker:\n      entrypoint: [/bin/sh, -c, \"echo ${input:NOPE}\"]\n", false, "undeclared input"},
		"entrypoint first empty":  {"    docker:\n      entrypoint: [\"\"]\n", false, "entrypoint"},
		"host path outside /dev":  {"    docker:\n      devices: [{hostPath: /etc/shadow}]\n", true, "/dev"},
		"privileged needs a flag": {"    docker: {privileged: true}\n", false, "requiresUnrestricted"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			tpl, err := Parse([]byte(withDocker(tc.block, tc.unrestricted)))
			require.NoError(t, err)
			err = Validate(tpl)
			if tc.want == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tc.want)
		})
	}
}

func TestDockerBlockSecretInput(t *testing.T) {
	y := withDocker("    docker:\n      entrypoint: [/bin/sh, -c, \"echo ${input:TITLE}\"]\n", false)
	y = strings.Replace(y, "{key: TITLE, label: Title, default: Hello}", "{key: TITLE, label: Title, default: Hello, secret: true}", 1)
	require.ErrorContains(t, Validate(parse(t, y)), "clear text")
}

func TestDockerBlockOnManagedDatabase(t *testing.T) {
	y := strings.Replace(goodTemplate, "    database: {engine: postgres, version: \"17\"}",
		"    database: {engine: postgres, version: \"17\"}\n    docker: {shmSizeMb: 256}", 1)
	require.ErrorContains(t, Validate(parse(t, y)), "managed database")
}

func TestDockerBlockRoundTrip(t *testing.T) {
	tpl := parse(t, withDocker(`    docker:
      entrypoint:
        - /bin/sh
        - -c
        - |
          set -e
          exec "$@"
      shmSizeMb: 256
      ulimits:
        - {name: nofile, soft: 1024, hard: 2048}
`, false))
	out, err := yaml.Marshal(tpl)
	require.NoError(t, err)
	require.Contains(t, string(out), "shmSizeMb: 256")
	require.Contains(t, string(out), "set -e")
	back, err := Parse(out)
	require.NoError(t, err)
	require.Equal(t, tpl.Services[0].Docker, back.Services[0].Docker)
}

func TestServiceConfigCarriesDocker(t *testing.T) {
	tpl := parse(t, withDocker(`    docker:
      entrypoint: [/bin/sh, -c, "echo ${input:TITLE}"]
      shmSizeMb: 128
`, false))
	cfg := serviceConfig(&tpl.Services[0], map[string]string{"TITLE": "Hi"})
	require.NotNil(t, cfg.Docker)
	require.Equal(t, []string{"/bin/sh", "-c", "echo Hi"}, cfg.Docker.Entrypoint)
	require.Equal(t, 128, cfg.Docker.ShmSizeMB)
}

func TestSummaryRequiresUnrestricted(t *testing.T) {
	tpl := parse(t, withDocker("    docker:\n      devices: [{hostPath: /dev/dri/renderD128}]\n", true))
	require.NoError(t, Validate(tpl))
	require.True(t, summaryOf(tpl).RequiresUnrestricted)
	require.False(t, summaryOf(parse(t, goodTemplate)).RequiresUnrestricted)
}

// A docker block with dotted map keys (sysctls, hostSysctls) is one update
// change that patches back as a whole.
func TestUpdateDiffTreatsDockerAsOneField(t *testing.T) {
	old := parse(t, goodTemplate).Services[0]
	cur := parse(t, withDocker(`    docker:
      hostSysctls: {vm.max_map_count: 262144}
      sysctls: {net.core.somaxconn: "1024"}
      shmSizeMb: 256
`, false)).Services[0]

	vals := map[string]string{"TITLE": "Hello"}
	changes := configLeaf(&old, &cur, vals)
	var fields []string
	for _, c := range changes {
		fields = append(fields, strings.TrimPrefix(c.Field, "config."))
	}
	require.Equal(t, []string{"docker"}, fields)
	require.Empty(t, changes[0].From)
	require.Contains(t, changes[0].To, "vm.max_map_count")

	live := serviceConfig(&old, vals).WithDefaults()
	next := serviceConfig(&cur, vals).WithDefaults()
	patched, err := patchConfig(live, next, fields)
	require.NoError(t, err)
	require.NotNil(t, patched.Docker)
	require.Equal(t, int64(262144), patched.Docker.HostSysctls["vm.max_map_count"])
	require.Equal(t, "1024", patched.Docker.Sysctls["net.core.somaxconn"])
	require.Equal(t, 256, patched.Docker.ShmSizeMB)

	// and back: removing the block restores the default
	back, err := patchConfig(patched, live, fields)
	require.NoError(t, err)
	require.Nil(t, back.Docker)
}
