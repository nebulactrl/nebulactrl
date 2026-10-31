package build

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func repoDir(t *testing.T, files map[string]string) string {
	dir := t.TempDir()
	for name, content := range files {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644))
	}
	return dir
}

func TestRailpackDefaultsApplyUnlessTheAppPins(t *testing.T) {
	const pkg = `{"scripts":{"start":"node server.js"}}`
	tests := []struct {
		name  string
		files map[string]string
		vars  map[string]string
		want  map[string]string
	}{
		{"node default", map[string]string{"package.json": pkg}, nil,
			map[string]string{"RAILPACK_NODE_VERSION": "26.13.0"}},
		{"keeps other variables", map[string]string{"package.json": pkg}, map[string]string{"A": "1"},
			map[string]string{"A": "1", "RAILPACK_NODE_VERSION": "26.13.0"}},
		{"not a node project", map[string]string{"main.py": ""}, nil, map[string]string{}},
		{"explicit variable wins", map[string]string{"package.json": pkg},
			map[string]string{"RAILPACK_NODE_VERSION": "22"}, map[string]string{"RAILPACK_NODE_VERSION": "22"}},
		{"engines wins", map[string]string{"package.json": `{"engines":{"node":">=22 <23"}}`}, nil, map[string]string{}},
		{"empty engines does not pin", map[string]string{"package.json": `{"engines":{"npm":"10"}}`}, nil,
			map[string]string{"RAILPACK_NODE_VERSION": "26.13.0"}},
		{".nvmrc wins", map[string]string{"package.json": pkg, ".nvmrc": "v20\n"}, nil, map[string]string{}},
		{".node-version wins", map[string]string{"package.json": pkg, ".node-version": "20.11.0"}, nil, map[string]string{}},
		{"blank .nvmrc does not pin", map[string]string{"package.json": pkg, ".nvmrc": "\n"}, nil,
			map[string]string{"RAILPACK_NODE_VERSION": "26.13.0"}},
		{"mise.toml wins", map[string]string{"package.json": pkg, "mise.toml": "[tools]\nnode = \"22\"\n"}, nil, map[string]string{}},
		{".tool-versions wins", map[string]string{"package.json": pkg, ".tool-versions": "nodejs 22.1.0\n"}, nil, map[string]string{}},
		{"railpack.json packages wins", map[string]string{"package.json": pkg, "railpack.json": `{"packages":{"node":"22"}}`}, nil, map[string]string{}},
		{"bun lockfile", map[string]string{"package.json": pkg, "bun.lock": ""}, nil,
			map[string]string{"RAILPACK_NODE_VERSION": "26.13.0", "RAILPACK_BUN_VERSION": "1.4.4"}},
		{"bun variable wins", map[string]string{"package.json": pkg, "bun.lockb": ""},
			map[string]string{"RAILPACK_BUN_VERSION": "1.3.0"},
			map[string]string{"RAILPACK_NODE_VERSION": "26.13.0", "RAILPACK_BUN_VERSION": "1.3.0"}},
		{"bun packageManager wins", map[string]string{"package.json": `{"packageManager":"bun@1.3.2"}`}, nil,
			map[string]string{"RAILPACK_NODE_VERSION": "26.13.0"}},
		{"bun engines wins", map[string]string{"package.json": `{"engines":{"bun":">=1.3"}}`, "bun.lock": ""}, nil,
			map[string]string{"RAILPACK_NODE_VERSION": "26.13.0"}},
		{".bun-version wins", map[string]string{"package.json": pkg, "bun.lock": "", ".bun-version": "1.3.1"}, nil,
			map[string]string{"RAILPACK_NODE_VERSION": "26.13.0"}},
		{"npm project gets no bun default", map[string]string{"package.json": pkg, "package-lock.json": "{}"}, nil,
			map[string]string{"RAILPACK_NODE_VERSION": "26.13.0"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vars := map[string]string{}
			for k, v := range tt.vars {
				vars[k] = v
			}
			got, _ := withRailpackDefaults(repoDir(t, tt.files), vars)
			if got == nil {
				got = map[string]string{}
			}
			want := map[string]string{}
			for k, v := range tt.vars {
				want[k] = v
			}
			for k, v := range tt.want {
				want[k] = v
			}
			require.Equal(t, want, got)
			require.Len(t, vars, len(tt.vars), "the input map is not modified")
		})
	}
}
