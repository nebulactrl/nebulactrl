//go:build integration

package integration

import (
	"context"
	"fmt"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

const (
	composeFile  = "deploy/dev/docker-compose.yml"
	host2Name    = "nebulactrl-dev-host2-1"
	agent2Volume = "nebulactrl-dev_agent2-state"
)

// 12. multi-server: a service placed on host2 (docker-in-docker, driven by the
// dev `multi` profile's agent2) runs on host2's daemon only; a service on the
// local server runs on the local daemon only.
//
// Needs NEBULACTRL_TEST_MULTI=1. The test brings the `multi` profile up itself
// (agent2 enrolls against this test's control plane) and removes it afterwards.
func TestMultiServer(t *testing.T) {
	requireTier(t, tierFull)
	if os.Getenv(envMulti) != "1" {
		t.Skipf("set %s=1 to run the multi-server test (starts the dev `multi` compose profile)", envMulti)
	}
	root, err := repoRoot()
	require.NoError(t, err)
	app := appFixture(t)
	s := ExclusiveStack(t, StackOptions{ListenAll: true})
	cpPort := s.Srv.Listener.Addr().(*net.TCPAddr).Port

	srv2, tok2 := s.EnrollmentToken("host2")
	env := append(os.Environ(),
		fmt.Sprintf("NEBULACTRL_DEV_CP_PORT=%d", cpPort),
		"NEBULACTRL_ENROLL_TOKEN_2="+tok2,
	)
	compose := func(timeout time.Duration, args ...string) (string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		full := []string{"compose", "-f", composeFile, "--profile", "multi"}
		if _, err := os.Stat(root + "/.env"); err == nil {
			full = []string{"compose", "--env-file", ".env", "-f", composeFile, "--profile", "multi"}
		}
		cmd := newCmd(ctx, root, "docker", append(full, args...)...)
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	down := func() {
		_, _ = compose(2*time.Minute, "rm", "-sf", "agent2", "host2")
		_, _ = sh(context.Background(), "", "docker", "volume", "rm", "-f", agent2Volume)
	}
	down() // a stale agent2 identity would refuse this test's control plane
	t.Cleanup(down)
	if out, err := compose(10*time.Minute, "up", "-d", "--build", "host2", "agent2"); err != nil {
		t.Fatalf("compose up multi profile: %v\n%s", err, out)
	}

	srv := s.WaitServer(srv2, 3*time.Minute)
	require.Equal(t, "online", srv.Str("status"))

	// One service per server.
	local := s.NewService(SvcSpec{Name: "local", Image: app.AppImage, Vars: map[string]string{"VERSION": "local"}})
	remote := s.NewService(SvcSpec{
		Name: "remote", ServerID: srv2, Image: "traefik/whoami:latest", Port: 80,
		Health: map[string]any{"type": "tcp", "deadlineSeconds": 180},
	})
	s.DeployOK(local.ID)
	d := s.WaitDeployment(s.Deploy(remote.ID), 5*time.Minute)
	require.Equal(t, "succeeded", d.Str("status"), "%v\n%s", d, s.DeploymentLogs(d.Str("id")))
	require.Equal(t, srv2, d.Str("serverId"))

	onHost2 := func(serviceID string) string {
		out, _ := sh(context.Background(), "", "docker", "exec", host2Name, "docker", "ps", "-a", "-q",
			"--filter", "label="+agentprotocol.LabelService+"="+serviceID)
		return out
	}
	require.NotEmpty(t, onHost2(remote.ID), "remote service container missing on host2's daemon")
	require.Empty(t, s.Containers(remote.ID), "remote service container must not exist on the local daemon")
	require.Len(t, s.RunningContainers(local.ID), 1)
	require.Empty(t, onHost2(local.ID), "local service container must not exist on host2's daemon")
}
