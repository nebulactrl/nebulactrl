package main

import (
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

func dockerFlagSet(t *testing.T, args ...string) (*cobra.Command, *dockerFlags) {
	t.Helper()
	var d dockerFlags
	cmd := &cobra.Command{Use: "x", RunE: func(*cobra.Command, []string) error { return nil }}
	d.register(cmd.Flags())
	require.NoError(t, cmd.ParseFlags(args))
	return cmd, &d
}

func TestApplyDockerFlagsSetsAndClears(t *testing.T) {
	docker := map[string]any{"capDrop": []any{"NET_RAW"}, "hostname": "old", "sysctls": map[string]any{"net.core.somaxconn": "128"}, "init": true}
	cmd, d := dockerFlagSet(t, "--cap-drop", "ALL", "--no-new-privileges", "--read-only", "--pids-limit", "512",
		"--hostname", "default", "--dns", "10.0.0.2,1.1.1.1", "--sysctl", "net.core.somaxconn=", "--sysctl", "net.ipv4.ip_unprivileged_port_start=0",
		"--user", "1000:1000", "--group-add", "122,44")
	require.NoError(t, applyDockerFlags(docker, cmd.Flags(), d))
	require.Equal(t, map[string]any{
		"capDrop": []string{"ALL"}, "noNewPrivileges": true, "readOnlyRootfs": true, "pidsLimit": int64(512),
		"dns": []string{"10.0.0.2", "1.1.1.1"}, "sysctls": map[string]any{"net.ipv4.ip_unprivileged_port_start": "0"},
		"user": "1000:1000", "groupAdd": []string{"122", "44"}, "init": true,
	}, docker, "hostname default resets, a key= removes one sysctl, untouched options stay")

	cmd, d = dockerFlagSet(t, "--no-new-privileges=false", "--pids-limit", "0", "--cap-drop", "none", "--sysctl", "net.ipv4.ip_unprivileged_port_start=")
	require.NoError(t, applyDockerFlags(docker, cmd.Flags(), d))
	require.Equal(t, map[string]any{"readOnlyRootfs": true, "dns": []string{"10.0.0.2", "1.1.1.1"}, "user": "1000:1000", "groupAdd": []string{"122", "44"}, "init": true}, docker)

	cmd, d = dockerFlagSet(t, "--clear")
	require.NoError(t, applyDockerFlags(docker, cmd.Flags(), d))
	require.Empty(t, docker)
}

func TestApplyDockerFlagsUnrestrictedTier(t *testing.T) {
	docker := map[string]any{}
	cmd, d := dockerFlagSet(t, "--device", "/dev/dri/renderD128", "--device", "/dev/kvm:/dev/kvm:rw", "--device", "/dev/fuse:rwm",
		"--gpus", "all", "--runtime", "runsc", "--privileged", "--pid-host", "--seccomp-unconfined", "--apparmor-unconfined")
	require.NoError(t, applyDockerFlags(docker, cmd.Flags(), d))
	require.Equal(t, map[string]any{
		"devices": []any{
			map[string]any{"hostPath": "/dev/dri/renderD128"},
			map[string]any{"hostPath": "/dev/kvm", "containerPath": "/dev/kvm", "permissions": "rw"},
			map[string]any{"hostPath": "/dev/fuse", "permissions": "rwm"},
		},
		"gpus": map[string]any{"count": -1, "driver": "nvidia"}, "runtime": "runsc",
		"privileged": true, "pidHost": true, "seccompUnconfined": true, "apparmorUnconfined": true,
	}, docker)

	cmd, d = dockerFlagSet(t, "--gpus", "2")
	require.NoError(t, applyDockerFlags(docker, cmd.Flags(), d))
	require.Equal(t, map[string]any{"count": 2, "driver": "nvidia"}, docker["gpus"])
	cmd, d = dockerFlagSet(t, "--gpus", "none", "--device", "none", "--runtime", "default", "--privileged=false")
	require.NoError(t, applyDockerFlags(docker, cmd.Flags(), d))
	require.NotContains(t, docker, "gpus")
	require.NotContains(t, docker, "devices")
	require.NotContains(t, docker, "runtime")
	require.NotContains(t, docker, "privileged")
}

func TestApplyDockerFlagsRejectsMalformedValues(t *testing.T) {
	for _, args := range [][]string{{"--sysctl", "nokeyvalue"}, {"--gpus", "lots"}, {"--gpus", "0"}, {"--device", ":/dev/x"}, {"--device", "a:b:c:d"}} {
		cmd, d := dockerFlagSet(t, args...)
		require.Error(t, applyDockerFlags(map[string]any{}, cmd.Flags(), d), args)
	}
}
