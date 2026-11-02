package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeSystemctl puts a systemctl first on PATH that records its arguments and
// exits with code, printing out on stdout.
func fakeSystemctl(t *testing.T, code, out string) (argsFile string) {
	t.Helper()
	bin := t.TempDir()
	argsFile = filepath.Join(bin, "args")
	script := "#!/bin/sh\necho \"$@\" >> '" + argsFile + "'\necho '" + out + "'\nexit " + code + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(bin, "systemctl"), []byte(script), 0o755))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return argsFile
}

func TestNewSelfRemovalRunsSystemctlWhenTheHostHasIt(t *testing.T) {
	argsFile := fakeSystemctl(t, "0", "")
	r := newSelfRemoval("/var/lib/x", "", "", nil, quietLog())
	require.NotNil(t, r.Systemctl)

	require.NoError(t, r.Systemctl(context.Background(), "disable", "nebulactrl-agent.service"))
	got, err := os.ReadFile(argsFile)
	require.NoError(t, err)
	assert.Equal(t, "disable nebulactrl-agent.service\n", string(got))
}

func TestNewSelfRemovalSystemctlErrorsCarryTheCommandAndItsOutput(t *testing.T) {
	fakeSystemctl(t, "3", "Failed to connect to bus")
	r := newSelfRemoval("/var/lib/x", "", "", nil, quietLog())
	require.NotNil(t, r.Systemctl)

	err := r.Systemctl(context.Background(), "daemon-reload")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "systemctl daemon-reload")
	assert.Contains(t, err.Error(), "Failed to connect to bus")
}

func TestNewSelfRemovalWithoutSystemctlSkipsSystemdSteps(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	r := newSelfRemoval("/var/lib/x", "", "", nil, quietLog())
	assert.Nil(t, r.Systemctl, "a host without systemd has nothing to disable or reload")
}
