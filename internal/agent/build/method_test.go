package build

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

func writeFiles(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
	}
	return dir
}

// The detection matrix: which build method each build type resolves to for a
// repository with and without a Dockerfile.
func TestDecideMatrix(t *testing.T) {
	with := writeFiles(t, map[string]string{"Dockerfile": "FROM scratch\n", "package.json": "{}"})
	without := writeFiles(t, map[string]string{"package.json": "{}"})
	custom := writeFiles(t, map[string]string{"docker/prod.Dockerfile": "FROM scratch\n"})

	cases := []struct {
		name       string
		repo       string
		buildType  string
		dockerfile string
		method     string
		starter    string
		errContain string
		found      bool
	}{
		{"auto+dockerfile", with, "auto", "Dockerfile", "dockerfile", "", "", true},
		{"empty type is auto", with, "", "", "dockerfile", "", "", true},
		{"auto without dockerfile -> railpack", without, "auto", "Dockerfile", "railpack", "", "", false},
		{"empty type without dockerfile -> railpack", without, "", "", "railpack", "", "", false},
		{"auto with custom path present", custom, "auto", "docker/prod.Dockerfile", "dockerfile", "", "", true},
		{"auto with custom path missing is an error, not railpack", without, "auto", "docker/prod.Dockerfile", "", "", "not found", false},
		{"dockerfile present", with, "dockerfile", "Dockerfile", "dockerfile", "", "", true},
		{"dockerfile missing", without, "dockerfile", "Dockerfile", "", "", "not found", false},
		{"railpack ignores a Dockerfile", with, "railpack", "Dockerfile", "railpack", "", "", true},
		{"railpack without dockerfile", without, "railpack", "Dockerfile", "railpack", "", "", false},
		{"starter:node", without, "starter:node", "Dockerfile", "starter", "node", "", false},
		{"starter wins over a Dockerfile", with, "starter:static", "Dockerfile", "starter", "static", "", true},
		{"unknown type", with, "nixpacks", "Dockerfile", "", "", "unknown build type", false},
		{"unknown starter", with, "starter:cobol", "Dockerfile", "", "", "unknown build type", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d, err := Decide(tc.buildType, tc.repo, tc.dockerfile)
			if tc.errContain != "" {
				require.ErrorContains(t, err, tc.errContain)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.method, d.Method)
			require.Equal(t, tc.starter, d.Starter)
			require.Equal(t, tc.found, d.DockerfileFound)
			require.NotEmpty(t, d.Reason)
			if tc.method == "dockerfile" {
				require.True(t, filepath.IsAbs(d.DockerfileAbs))
			}
		})
	}
}

func TestDecideRefusesDockerfileEscapingTheRepository(t *testing.T) {
	outside := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(outside, "Dockerfile"), []byte("FROM scratch"), 0o644))
	repo := t.TempDir()
	require.NoError(t, os.Symlink(outside, filepath.Join(repo, "evil")))
	_, err := Decide("dockerfile", repo, "evil/Dockerfile")
	require.ErrorContains(t, err, "escapes the repository")
	d, err := Decide("auto", repo, "Dockerfile")
	require.NoError(t, err)
	require.Equal(t, "railpack", d.Method)
}

func TestDetectStarter(t *testing.T) {
	for want, files := range map[string]map[string]string{
		"node":   {"package.json": "{}", "index.html": ""},
		"go":     {"go.mod": "module x"},
		"python": {"requirements.txt": ""},
		"ruby":   {"Gemfile": ""},
		"php":    {"composer.json": "{}"},
		"static": {"index.html": ""},
		"":       {"README.md": ""},
	} {
		require.Equal(t, want, DetectStarter(writeFiles(t, files)), want)
	}
}

func TestBuildTypeValidation(t *testing.T) {
	for _, ok := range []string{"auto", "dockerfile", "railpack", "starter:node", "starter:python", "starter:go", "starter:ruby", "starter:php", "starter:static"} {
		require.True(t, agentprotocol.ValidBuildType(ok), ok)
	}
	for _, bad := range []string{"", "nixpacks", "starter:", "starter:java", "Railpack", "starter:node;rm"} {
		require.False(t, agentprotocol.ValidBuildType(bad), bad)
	}
}
