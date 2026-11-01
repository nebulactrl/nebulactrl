// Package mesh manages the managed tailnet server mesh of an agent host.
//
// Two kinds of containers, none in the host's network namespace and none with
// the Docker socket:
//
//   - the server node (nc-mesh-<serverId8>): tailscale in userspace mode, no
//     capabilities, no TUN device. It is the server's tailnet identity and the
//     ingress: `tailscale serve` TCP forwards meshPort -> the exposed service.
//   - the link node (nc-meshl-<linkId8>): tailscale in kernel mode in its own
//     network namespace (NET_ADMIN, NET_RAW, /dev/net/tun, ip_forward) attached
//     to one environment's networks under aliases. containerboot DNATs
//     everything that reaches it to the remote server node
//     (TS_TAILNET_TARGET_IP), so a consumer dials <alias>:<meshPort>.
//
// Node keys travel only as arguments (never persisted, never a label, redacted
// from every log and status).
package mesh

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/internal/agent/proxy"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Fixed paths and addresses inside the node containers.
const (
	stateDir     = "/var/lib/tailscale"
	configDir    = "/config"
	serveFile    = "serve.json"
	healthAddr   = "127.0.0.1:9002"
	memoryBytes  = 256 << 20
	healthPeriod = 10 * time.Second

	// labelKeyFP records which auth key provisioned the node (a truncated
	// SHA-256 of it, never the key), so a hello reconcile that repeats the same
	// provisioning does not wipe a node that is already enrolled.
	labelKeyFP = "nebulactrl.mesh.keyfp"
)

var (
	idRe       = regexp.MustCompile(`^[0-9a-f][0-9a-f-]{7,35}$`)
	slugRe     = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	hostnameRe = slugRe
)

// ContainerName returns the container name of a node spec.
func ContainerName(spec agentprotocol.MeshNodeSpec) string {
	if spec.Kind == agentprotocol.MeshKindLink {
		return agentprotocol.MeshLinkContainer(spec.NodeID)
	}
	return agentprotocol.MeshServerContainer(spec.NodeID)
}

// Validate checks a node spec before anything touches Docker. Every value that
// ends up in an environment variable, a network name, an alias or the serve
// config is checked here.
func Validate(spec agentprotocol.MeshNodeSpec) error {
	if !idRe.MatchString(spec.NodeID) {
		return fmt.Errorf("invalid node id %q", spec.NodeID)
	}
	if spec.Kind != agentprotocol.MeshKindServer && spec.Kind != agentprotocol.MeshKindLink {
		return fmt.Errorf("unknown node kind %q", spec.Kind)
	}
	if !agentprotocol.ValidTailnetProvider(spec.Provider) {
		return fmt.Errorf("unknown tailnet provider %q", spec.Provider)
	}
	if spec.Provider == agentprotocol.TailnetProviderHeadscale {
		if !strings.HasPrefix(spec.LoginServer, "https://") && !strings.HasPrefix(spec.LoginServer, "http://") {
			return errors.New("a headscale tailnet needs an http(s) login server")
		}
		if strings.ContainsAny(spec.LoginServer, " \t\r\n\"'`$;&|<>") {
			return errors.New("invalid login server")
		}
	}
	if !hostnameRe.MatchString(spec.Hostname) {
		return fmt.Errorf("invalid tailnet hostname %q", spec.Hostname)
	}
	if spec.Kind == agentprotocol.MeshKindServer {
		return validateServer(spec)
	}
	return validateLink(spec)
}

func validateServer(spec agentprotocol.MeshNodeSpec) error {
	if spec.EnvironmentID != "" || spec.RemoteServerID != "" || spec.TargetIP != "" || len(spec.Aliases) > 0 {
		return errors.New("a server node has no environment, remote server, target or aliases")
	}
	ports := map[int]bool{}
	for _, e := range spec.Exposures {
		switch {
		case !idRe.MatchString(e.ServiceID):
			return fmt.Errorf("invalid exposure service id %q", e.ServiceID)
		case !idRe.MatchString(e.EnvironmentID):
			return fmt.Errorf("invalid exposure environment id %q", e.EnvironmentID)
		case !slugRe.MatchString(e.ServiceSlug):
			return fmt.Errorf("invalid exposure service slug %q", e.ServiceSlug)
		case e.Purpose != agentprotocol.MeshPurposeReplication && e.Purpose != agentprotocol.MeshPurposeService:
			return fmt.Errorf("unknown exposure purpose %q", e.Purpose)
		case e.ContainerPort < 1 || e.ContainerPort > 65535 || e.MeshPort < 1 || e.MeshPort > 65535:
			return fmt.Errorf("exposure %d -> %d is out of range", e.MeshPort, e.ContainerPort)
		case ports[e.MeshPort]:
			return fmt.Errorf("mesh port %d is used twice", e.MeshPort)
		}
		ports[e.MeshPort] = true
	}
	return nil
}

func validateLink(spec agentprotocol.MeshNodeSpec) error {
	if len(spec.Exposures) > 0 {
		return errors.New("a link node has no exposures")
	}
	if !idRe.MatchString(spec.EnvironmentID) {
		return fmt.Errorf("invalid environment id %q", spec.EnvironmentID)
	}
	if !idRe.MatchString(spec.RemoteServerID) {
		return fmt.Errorf("invalid remote server id %q", spec.RemoteServerID)
	}
	ip, err := netip.ParseAddr(spec.TargetIP)
	if err != nil || !ip.Is4() || ip.IsUnspecified() || ip.IsLoopback() || ip.IsMulticast() {
		return fmt.Errorf("invalid target ip %q: the remote server node's tailnet IPv4 is required", spec.TargetIP)
	}
	if len(spec.Aliases) == 0 {
		return errors.New("a link node needs at least one alias")
	}
	for _, a := range spec.Aliases {
		if !slugRe.MatchString(a) {
			return fmt.Errorf("invalid alias %q", a)
		}
	}
	return nil
}

// sortedAliases returns the deduplicated, sorted alias set of a link node.
func sortedAliases(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, a := range in {
		if a != "" && !seen[a] {
			seen[a] = true
			out = append(out, a)
		}
	}
	sort.Strings(out)
	return out
}

// ContainerSpec is the node container. authKey is set only while provisioning;
// the node's identity then lives on the state volume (with TS_AUTH_ONCE a
// restart never logs in again). The spec fingerprint label never covers the
// key, the exposures (applied live through the watched serve config), the
// aliases or the networks beyond the first (both applied live).
func ContainerSpec(spec agentprotocol.MeshNodeSpec, authKey string) docker.ContainerSpec {
	if spec.Kind == agentprotocol.MeshKindLink {
		return LinkContainerSpec(spec, authKey)
	}
	return ServerContainerSpec(spec, authKey)
}

func baseEnv(spec agentprotocol.MeshNodeSpec, userspace bool) []string {
	env := []string{
		fmt.Sprintf("TS_USERSPACE=%t", userspace),
		"TS_STATE_DIR=" + stateDir,
		"TS_AUTH_ONCE=true",
		"TS_ACCEPT_DNS=false", // Docker's DNS must keep resolving the aliases
		"TS_HOSTNAME=" + spec.Hostname,
		"TS_ENABLE_HEALTH_CHECK=true",
		"TS_LOCAL_ADDR_PORT=" + healthAddr,
	}
	return env
}

func withLoginServer(env []string, spec agentprotocol.MeshNodeSpec) []string {
	if spec.Provider == agentprotocol.TailnetProviderHeadscale {
		// A tagged key carries the tags: never pass --advertise-tags with it.
		env = append(env, "TS_EXTRA_ARGS=--login-server="+spec.LoginServer)
	}
	return env
}

func withKey(env []string, authKey string) []string {
	if authKey != "" {
		return append(env, "TS_AUTHKEY="+authKey)
	}
	return env
}

func fingerprint(parts ...string) string {
	h := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(h[:8])
}

// KeyFingerprint is the non-reversible marker of a provisioning key.
func KeyFingerprint(key string) string {
	h := sha256.Sum256([]byte("nebulactrl-mesh-key\x00" + key))
	return hex.EncodeToString(h[:8])
}

func labelsOf(spec agentprotocol.MeshNodeSpec, hash, authKey string) map[string]string {
	l := map[string]string{
		agentprotocol.LabelManaged:  "true",
		agentprotocol.LabelMesh:     spec.NodeID,
		agentprotocol.LabelMeshKind: spec.Kind,
		agentprotocol.LabelMeshSpec: hash,
	}
	if spec.Kind == agentprotocol.MeshKindLink {
		l[agentprotocol.LabelEnvironment] = spec.EnvironmentID
	}
	if authKey != "" {
		l[labelKeyFP] = KeyFingerprint(authKey)
	}
	return l
}

func healthcheck() *docker.HealthSpec {
	return &docker.HealthSpec{
		Test:     []string{"CMD", "wget", "-q", "-O", "/dev/null", "http://" + healthAddr + "/healthz"},
		Interval: healthPeriod, Timeout: 3 * time.Second, StartPeriod: 30 * time.Second, Retries: 3,
	}
}

func logConfig() *docker.LogConfig {
	return &docker.LogConfig{Type: "json-file", Config: map[string]string{"max-size": proxy.LogMaxSize, "max-file": proxy.LogMaxFile}}
}

// ServerContainerSpec is the userspace server node: no capabilities, no TUN
// device, no published ports, no Docker socket. It starts on the server-wide
// mesh network only; the networks of the exposed services are connected live.
func ServerContainerSpec(spec agentprotocol.MeshNodeSpec, authKey string) docker.ContainerSpec {
	env := withLoginServer(append(baseEnv(spec, true), "TS_SERVE_CONFIG="+configDir+"/"+serveFile), spec)
	hash := fingerprint(append([]string{proxy.TailscaleImage, spec.Kind, agentprotocol.MeshNetwork}, env...)...)
	return docker.ContainerSpec{
		Name:   ContainerName(spec),
		Image:  proxy.TailscaleImage,
		Env:    withKey(env, authKey),
		Labels: labelsOf(spec, hash, authKey),
		// The mesh bridge is the default route (control plane, DERP); the
		// environment networks of the exposures are connected live.
		Networks: []docker.NetworkAttachment{{Network: agentprotocol.MeshNetwork, GwPriority: agentprotocol.EdgeGwPriority}},
		Mounts: []docker.VolumeMount{
			{Volume: agentprotocol.MeshStateVolume(spec.NodeID), Target: stateDir},
			{Volume: agentprotocol.MeshConfigVolume(spec.NodeID), Target: configDir},
		},
		RestartPolicy: "unless-stopped",
		MemoryBytes:   memoryBytes,
		CapDrop:       []string{"ALL"},
		SecurityOpt:   []string{"no-new-privileges"},
		Healthcheck:   healthcheck(),
		LogConfig:     logConfig(),
	}
}

// LinkNetworks is the desired network membership of a link node: the
// environment network and, when the environment has it, its internal sibling,
// both under the link's aliases.
func LinkNetworks(spec agentprotocol.MeshNodeSpec) []docker.NetworkAttachment {
	al := sortedAliases(spec.Aliases)
	out := []docker.NetworkAttachment{{Network: agentprotocol.EnvNetwork(spec.EnvironmentID), Aliases: al}}
	if spec.HasInternalNet {
		out = append(out, docker.NetworkAttachment{Network: agentprotocol.EnvInternalNetwork(spec.EnvironmentID), Aliases: al})
	}
	return out
}

// LinkContainerSpec is the kernel-mode link node: NET_ADMIN, NET_RAW and the TUN
// device inside its own network namespace only, ip_forward so containerboot can
// DNAT everything it receives to the remote server node. No Docker socket, no
// host network, no published ports.
func LinkContainerSpec(spec agentprotocol.MeshNodeSpec, authKey string) docker.ContainerSpec {
	env := append(baseEnv(spec, false), "TS_TAILNET_TARGET_IP="+spec.TargetIP)
	env = withLoginServer(env, spec)
	sysctls := map[string]string{"net.ipv4.ip_forward": "1"}
	hash := fingerprint(append([]string{
		proxy.TailscaleImage, spec.Kind, agentprotocol.EnvNetwork(spec.EnvironmentID), "net.ipv4.ip_forward=1", "NET_ADMIN,NET_RAW", "/dev/net/tun",
	}, env...)...)
	return docker.ContainerSpec{
		Name:          ContainerName(spec),
		Image:         proxy.TailscaleImage,
		Env:           withKey(env, authKey),
		Labels:        labelsOf(spec, hash, authKey),
		Networks:      LinkNetworks(spec),
		Mounts:        []docker.VolumeMount{{Volume: agentprotocol.MeshStateVolume(spec.NodeID), Target: stateDir}},
		RestartPolicy: "unless-stopped",
		MemoryBytes:   memoryBytes,
		CapAdd:        []string{"NET_ADMIN", "NET_RAW"},
		Devices:       []docker.DeviceMapping{{HostPath: "/dev/net/tun"}},
		Sysctls:       sysctls,
		Healthcheck:   healthcheck(),
		LogConfig:     logConfig(),
	}
}

// exposureNetwork is the Docker network the server node joins to reach an
// exposed service: the internal network when the service lives only there.
func exposureNetwork(e agentprotocol.MeshExposure) string {
	if e.BlockEgress {
		return agentprotocol.EnvInternalNetwork(e.EnvironmentID)
	}
	return agentprotocol.EnvNetwork(e.EnvironmentID)
}

// isInternalNetwork reports whether name is an environment's internal network.
func isInternalNetwork(name string) bool {
	return strings.HasSuffix(name, agentprotocol.EnvInternalSuffix)
}

// isEnvNetwork reports whether name is an environment or internal network.
func isEnvNetwork(name string) bool { return strings.HasPrefix(name, agentprotocol.EnvNetworkPrefix) }
