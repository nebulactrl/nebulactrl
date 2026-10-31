// Package tunnel manages the Cloudflare Tunnel connector of an agent host: a
// pinned cloudflare/cloudflared container on the edge network that forwards
// every hostname of the tunnel's ingress config to the proxy
// (http://nebulactrl-traefik:80). The tunnel itself lives at Cloudflare and is
// remotely managed: the control plane creates it, pushes its ingress rules and
// hands the agent the connector token as a send-time secret. The token is
// written to a file inside the container (never an environment variable or
// argument, so docker inspect and the process list do not show it) and is
// never logged.
package tunnel

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/internal/agent/proxy"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Image is the pinned cloudflared image: release 2026.10.4, the multi-arch
// (amd64, arm64) manifest list digest, verified against the registry without
// pulling the image.
const Image = "cloudflare/cloudflared:2026.10.4@sha256:c6c7ae1d5dfdcb094ad344ed68ccffc26635c9c6be80965664c33baf064b0515"

const (
	tokenDir  = "etc/cloudflared"
	tokenName = "token"
	// TokenPath is where the connector token lives inside the container.
	TokenPath = "/" + tokenDir + "/" + tokenName
	// nonroot is the user and group of the official image.
	nonroot = 65532
)

var (
	metricsListen = "0.0.0.0:" + strconv.Itoa(agentprotocol.TunnelMetricsPort)
	metricsLocal  = "127.0.0.1:" + strconv.Itoa(agentprotocol.TunnelMetricsPort)
)

// Cmd is the container command (after the image's `cloudflared --no-autoupdate`
// entrypoint). The metrics server answers /ready on the edge network only; the
// token is read from TokenPath.
func Cmd() []string {
	return []string{"tunnel", "--no-autoupdate", "--metrics", metricsListen, "run", "--token-file", TokenPath}
}

// ContainerSpec is the connector container. It joins only the edge network (the
// proxy's hostname resolves there), publishes no port, drops every capability
// and restarts unless stopped. The fingerprint label covers everything that
// shapes the container except the token, which only decides which tunnel it
// serves (LabelTunnel).
func ContainerSpec(spec agentprotocol.TunnelSpec) docker.ContainerSpec {
	cmd := Cmd()
	hash := fingerprint(append([]string{Image, agentprotocol.EdgeNetwork}, cmd...)...)
	return docker.ContainerSpec{
		Name:  agentprotocol.TunnelContainer,
		Image: Image,
		Cmd:   cmd,
		Labels: map[string]string{
			agentprotocol.LabelManaged:    "true",
			agentprotocol.LabelTunnel:     spec.TunnelID,
			agentprotocol.LabelTunnelSpec: hash,
		},
		Networks:      []docker.NetworkAttachment{{Network: agentprotocol.EdgeNetwork, GwPriority: agentprotocol.EdgeGwPriority}},
		RestartPolicy: "unless-stopped",
		CapDrop:       []string{"ALL"},
		SecurityOpt:   []string{"no-new-privileges"},
		Healthcheck: &docker.HealthSpec{
			Test:     []string{"CMD", "cloudflared", "tunnel", "--metrics", metricsLocal, "ready"},
			Interval: 30 * time.Second, Timeout: 5 * time.Second, StartPeriod: 30 * time.Second, Retries: 3,
		},
		LogConfig: &docker.LogConfig{Type: "json-file", Config: map[string]string{"max-size": proxy.LogMaxSize, "max-file": proxy.LogMaxFile}},
	}
}

func fingerprint(parts ...string) string {
	h := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(h[:8])
}

// TokenTar packs the token file for CopyToContainer at "/": the directory (the
// image has none) and the token, readable only by the image's user.
func TokenTar(token string) (io.Reader, error) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeDir, Name: tokenDir + "/", Mode: 0o755, Uid: nonroot, Gid: nonroot}); err != nil {
		return nil, fmt.Errorf("tar token dir: %w", err)
	}
	if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: tokenDir + "/" + tokenName, Mode: 0o400, Uid: nonroot, Gid: nonroot, Size: int64(len(token))}); err != nil {
		return nil, fmt.Errorf("tar token header: %w", err)
	}
	if _, err := io.WriteString(tw, token); err != nil {
		return nil, fmt.Errorf("tar token: %w", err)
	}
	if err := tw.Close(); err != nil {
		return nil, fmt.Errorf("tar token close: %w", err)
	}
	return bytes.NewReader(buf.Bytes()), nil
}
