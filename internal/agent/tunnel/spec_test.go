package tunnel

import (
	"archive/tar"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

const (
	tunnelID = "5a1b2c3d-0000-4000-8000-123456789abc"
	// testToken has the shape of a connector token: base64 of a small JSON document.
	testToken = "eyJhIjoiYWNjdCIsInQiOiI1YTFiMmMzZC0wMDAwLTQwMDAtODAwMC0xMjM0NTY3ODlhYmMiLCJzIjoic2VjcmV0LXNlY3JldC1zZWNyZXQifQ=="
)

// The connector reaches the internet and the proxy: pin every property of its
// container spec. The fingerprint (7dabbbfd56b3657a) is sha256 over
// image, edge network and command, joined with NUL bytes.
func TestContainerSpecGolden(t *testing.T) {
	got := ContainerSpec(agentprotocol.TunnelSpec{TunnelID: tunnelID, Name: "nebulactrl-edge-1"})

	want := docker.ContainerSpec{
		Name:  "nebulactrl-cloudflared",
		Image: "cloudflare/cloudflared:2026.10.4@sha256:c6c7ae1d5dfdcb094ad344ed68ccffc26635c9c6be80965664c33baf064b0515",
		Cmd:   []string{"tunnel", "--no-autoupdate", "--metrics", "0.0.0.0:20241", "run", "--token-file", "/etc/cloudflared/token"},
		Labels: map[string]string{
			"nebulactrl.managed":     "true",
			"nebulactrl.tunnel":      tunnelID,
			"nebulactrl.tunnel.spec": "7dabbbfd56b3657a",
		},
		Networks:      []docker.NetworkAttachment{{Network: "nebulactrl-edge", GwPriority: agentprotocol.EdgeGwPriority}},
		RestartPolicy: "unless-stopped",
		CapDrop:       []string{"ALL"},
		SecurityOpt:   []string{"no-new-privileges"},
		Healthcheck: &docker.HealthSpec{
			Test:     []string{"CMD", "cloudflared", "tunnel", "--metrics", "127.0.0.1:20241", "ready"},
			Interval: 30 * time.Second, Timeout: 5 * time.Second, StartPeriod: 30 * time.Second, Retries: 3,
		},
		LogConfig: &docker.LogConfig{Type: "json-file", Config: map[string]string{"max-size": "10m", "max-file": "3"}},
	}
	require.Equal(t, want, got)
}

func TestContainerSpecIsLockedDown(t *testing.T) {
	cs := ContainerSpec(agentprotocol.TunnelSpec{TunnelID: tunnelID})
	require.Contains(t, cs.Image, "@sha256:", "pinned by digest")
	require.Empty(t, cs.Ports, "nothing is published on the host")
	require.Empty(t, cs.Mounts, "no volumes, no docker socket")
	require.Empty(t, cs.CapAdd)
	require.Empty(t, cs.Devices)
	require.Empty(t, cs.NetworkMode)
	require.Empty(t, cs.Env, "the token is never an environment variable")
	_, hasService := cs.Labels[agentprotocol.LabelService]
	require.False(t, hasService, "the connector must not look like a replica: the orphan sweep keys on the service label")
	require.Len(t, cs.Networks, 1)
	require.Equal(t, agentprotocol.EdgeNetwork, cs.Networks[0].Network)
	require.NotContains(t, strings.Join(cs.Cmd, " "), "--token ", "the token is read from a file, not passed as an argument")
	require.Equal(t, TokenPath, cs.Cmd[len(cs.Cmd)-1])
}

func TestContainerSpecFingerprintIgnoresTunnelAndToken(t *testing.T) {
	a := ContainerSpec(agentprotocol.TunnelSpec{TunnelID: tunnelID})
	b := ContainerSpec(agentprotocol.TunnelSpec{TunnelID: "6b1b2c3d-0000-4000-8000-123456789abc", Name: "other"})
	require.Equal(t, a.Labels[agentprotocol.LabelTunnelSpec], b.Labels[agentprotocol.LabelTunnelSpec])
	require.NotEqual(t, a.Labels[agentprotocol.LabelTunnel], b.Labels[agentprotocol.LabelTunnel])
}

func TestTokenTar(t *testing.T) {
	r, err := TokenTar(testToken)
	require.NoError(t, err)
	tr := tar.NewReader(r)

	h, err := tr.Next()
	require.NoError(t, err)
	require.Equal(t, "etc/cloudflared/", h.Name)
	require.Equal(t, byte(tar.TypeDir), h.Typeflag)
	require.Equal(t, 65532, h.Uid)

	h, err = tr.Next()
	require.NoError(t, err)
	require.Equal(t, "etc/cloudflared/token", h.Name)
	require.Equal(t, int64(0o400), h.Mode, "readable only by the image's user")
	require.Equal(t, 65532, h.Uid)
	require.Equal(t, 65532, h.Gid)
	b, err := io.ReadAll(tr)
	require.NoError(t, err)
	require.Equal(t, testToken, string(b))
}

func TestValidate(t *testing.T) {
	ok := agentprotocol.TunnelSpec{TunnelID: tunnelID}
	require.NoError(t, Validate(ok, testToken))
	require.Error(t, Validate(agentprotocol.TunnelSpec{TunnelID: "not-a-uuid"}, testToken))
	require.Error(t, Validate(agentprotocol.TunnelSpec{TunnelID: "../../etc"}, testToken))
	require.Error(t, Validate(ok, ""))
	require.Error(t, Validate(ok, "has spaces"))
	require.Error(t, Validate(ok, "tok;rm -rf /"))
	require.Error(t, Validate(ok, strings.Repeat("a", maxTokenLen+1)))
}
