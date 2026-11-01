package mesh

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strconv"
	"time"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// ServeTarget is one `tailscale serve` TCP forward of a server node: the port
// on the node (the tailnet side) to host:ContainerPort inside the Docker
// networks the node joined.
type ServeTarget struct {
	MeshPort      int
	Host          string
	ContainerPort int
}

// ServeConfig renders the Serve configuration (ipn.ServeConfig JSON) of a
// server node: TCP forwards only, one per exposure, sorted by mesh port so the
// bytes are deterministic. An empty set renders an empty (but valid) config.
func ServeConfig(targets []ServeTarget) ([]byte, error) {
	sorted := append([]ServeTarget(nil), targets...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].MeshPort < sorted[j].MeshPort })
	tcp := map[string]any{}
	for _, t := range sorted {
		tcp[strconv.Itoa(t.MeshPort)] = map[string]any{"TCPForward": t.Host + ":" + strconv.Itoa(t.ContainerPort)}
	}
	cfg := map[string]any{}
	if len(tcp) > 0 {
		cfg["TCP"] = tcp
	}
	out, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(out, '\n'), nil
}

// serveTar packs the serve config for CopyToContainer into /config.
func serveTar(cfg []byte) (*bytes.Reader, error) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{Name: serveFile, Mode: 0o644, Size: int64(len(cfg)), ModTime: time.Unix(0, 0)}); err != nil {
		return nil, err
	}
	if _, err := tw.Write(cfg); err != nil {
		return nil, err
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	return bytes.NewReader(buf.Bytes()), nil
}

// serveTargets decides, per exposure, what the node forwards to. The target is
// never a slug (slugs are not unique across the environments the node is
// attached to): it is the unique alias nc-svc-<serviceId8> when a running
// container of the service carries it on any network, else the unique name of
// the first running container. A service with no running container yet is
// forwarded to its alias, which resolves as soon as a deployment that carries
// it lands. Exposing a running service therefore never restarts it.
func (m *Manager) serveTargets(ctx context.Context, exps []agentprotocol.MeshExposure) []ServeTarget {
	out := make([]ServeTarget, 0, len(exps))
	for _, e := range exps {
		alias := agentprotocol.MeshServiceAlias(e.ServiceID)
		host := alias
		cs, err := m.eng.ContainerList(ctx, docker.ListFilter{Labels: map[string]string{
			agentprotocol.LabelManaged: "true", agentprotocol.LabelService: e.ServiceID,
		}})
		if err != nil {
			m.log.Debug("serve targets: list containers", "service_id", e.ServiceID, "err", err)
		}
		sort.Slice(cs, func(i, j int) bool { return cs[i].Name < cs[j].Name })
		name, aliased := "", false
		for _, c := range cs {
			if !c.Running {
				continue
			}
			if name == "" {
				name = c.Name
			}
			for _, n := range c.Networks {
				for _, a := range n.Aliases {
					if a == alias {
						aliased = true
					}
				}
			}
		}
		if !aliased && name != "" {
			host = name
		}
		out = append(out, ServeTarget{MeshPort: e.MeshPort, Host: host, ContainerPort: e.ContainerPort})
	}
	return out
}

func hashBytes(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:8])
}
