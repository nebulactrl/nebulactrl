package mesh

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/internal/agent/proxy"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

var update = flag.Bool("update", false, "rewrite the golden files")

func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name+".golden")
	if *update {
		require.NoError(t, os.MkdirAll("testdata", 0o755))
		require.NoError(t, os.WriteFile(path, []byte(got), 0o644))
	}
	want, err := os.ReadFile(path)
	require.NoError(t, err, "missing golden file; run go test -update")
	require.Equal(t, string(want), got)
}

func asJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.MarshalIndent(v, "", "  ")
	require.NoError(t, err)
	return string(b) + "\n"
}

// The server node is the least privileged thing here: pin every property.
func TestServerNodeSpecGolden(t *testing.T) {
	cs := ServerContainerSpec(serverSpec(), "")
	golden(t, "server_node_spec", asJSON(t, cs))

	require.Equal(t, "nc-mesh-5e5e5e5e", cs.Name)
	require.Equal(t, proxy.TailscaleImage, cs.Image)
	require.Contains(t, cs.Image, "@sha256:", "pinned by digest")
	require.ElementsMatch(t, []string{
		"TS_USERSPACE=true",
		"TS_STATE_DIR=/var/lib/tailscale",
		"TS_AUTH_ONCE=true",
		"TS_ACCEPT_DNS=false",
		"TS_HOSTNAME=nc-hetzner",
		"TS_ENABLE_HEALTH_CHECK=true",
		"TS_LOCAL_ADDR_PORT=127.0.0.1:9002",
		"TS_SERVE_CONFIG=/config/serve.json",
	}, cs.Env)

	// Userspace networking: nothing privileged, nothing published.
	require.Equal(t, []string{"ALL"}, cs.CapDrop)
	require.Empty(t, cs.CapAdd)
	require.Empty(t, cs.Devices)
	require.Empty(t, cs.Sysctls)
	require.Empty(t, cs.Ports)
	require.Empty(t, cs.NetworkMode)
	require.False(t, cs.Privileged)
	require.Empty(t, cs.PidMode)
	require.Equal(t, []string{"no-new-privileges"}, cs.SecurityOpt)
	for _, m := range cs.Mounts {
		require.NotContains(t, m.Volume+m.Target, "docker.sock")
	}

	// Starts on the server-wide mesh bridge only; exposure networks join live.
	require.Len(t, cs.Networks, 1)
	require.Equal(t, agentprotocol.MeshNetwork, cs.Networks[0].Network)
	require.Empty(t, cs.Networks[0].Aliases)

	// Never a replica: the orphan sweep and stats key on the service label.
	_, hasService := cs.Labels[agentprotocol.LabelService]
	require.False(t, hasService)
	require.Equal(t, srvID, cs.Labels[agentprotocol.LabelMesh])
	require.Equal(t, agentprotocol.MeshKindServer, cs.Labels[agentprotocol.LabelMeshKind])
	require.Equal(t, "true", cs.Labels[agentprotocol.LabelManaged])
}

func TestLinkNodeSpecGolden(t *testing.T) {
	cs := LinkContainerSpec(linkSpec(), "")
	golden(t, "link_node_spec", asJSON(t, cs))

	require.Equal(t, "nc-meshl-1a1a1a1a", cs.Name)
	require.Equal(t, proxy.TailscaleImage, cs.Image)
	require.ElementsMatch(t, []string{
		"TS_USERSPACE=false",
		"TS_STATE_DIR=/var/lib/tailscale",
		"TS_AUTH_ONCE=true",
		"TS_ACCEPT_DNS=false",
		"TS_HOSTNAME=nc-aaaaaaaa-web-to-db",
		"TS_ENABLE_HEALTH_CHECK=true",
		"TS_LOCAL_ADDR_PORT=127.0.0.1:9002",
		"TS_TAILNET_TARGET_IP=100.64.0.9",
		"TS_EXTRA_ARGS=--login-server=https://hs.example.com",
	}, cs.Env)

	// Kernel mode inside its own netns only.
	require.Equal(t, []string{"NET_ADMIN", "NET_RAW"}, cs.CapAdd)
	require.Equal(t, []docker.DeviceMapping{{HostPath: "/dev/net/tun"}}, cs.Devices)
	require.Equal(t, map[string]string{"net.ipv4.ip_forward": "1"}, cs.Sysctls)
	require.False(t, cs.Privileged)
	require.Empty(t, cs.NetworkMode, "no host network")
	require.Empty(t, cs.PidMode)
	require.Empty(t, cs.Ports)
	require.Empty(t, cs.CapDrop)
	for _, m := range cs.Mounts {
		require.NotContains(t, m.Volume+m.Target, "docker.sock")
	}
	require.Equal(t, []docker.VolumeMount{{Volume: "nc-mesh-1a1a1a1a-state", Target: "/var/lib/tailscale"}}, cs.Mounts)
	for _, e := range cs.Env {
		require.False(t, strings.HasPrefix(e, "TS_SERVE_CONFIG="), "a link node serves nothing")
		require.False(t, strings.HasPrefix(e, "TS_AUTHKEY="))
	}

	// The environment network and its -int sibling under the sorted aliases.
	require.Equal(t, []docker.NetworkAttachment{
		{Network: "nebulactrl-env-aaaaaaaa", Aliases: []string{"api", "postgres"}},
		{Network: "nebulactrl-env-aaaaaaaa-int", Aliases: []string{"api", "postgres"}},
	}, cs.Networks)

	_, hasService := cs.Labels[agentprotocol.LabelService]
	require.False(t, hasService)
	require.Equal(t, envA, cs.Labels[agentprotocol.LabelEnvironment])
	require.Equal(t, agentprotocol.MeshKindLink, cs.Labels[agentprotocol.LabelMeshKind])
}

func TestLinkNodeWithoutInternalNetworkAndReplicationAlias(t *testing.T) {
	s := linkSpec()
	s.Provider, s.LoginServer = agentprotocol.TailnetProviderTailscale, ""
	s.HasInternalNet = false
	s.Aliases = []string{agentprotocol.MeshReplicationAlias(remote)}
	cs := LinkContainerSpec(s, "")
	require.Equal(t, []docker.NetworkAttachment{{Network: "nebulactrl-env-aaaaaaaa", Aliases: []string{"mesh-7b7b7b7b"}}}, cs.Networks)
	for _, e := range cs.Env {
		require.False(t, strings.HasPrefix(e, "TS_EXTRA_ARGS="), "plain Tailscale needs no login server")
	}
}

func TestProvisioningKeyIsOnlyAnEnvValueNeverALabelOrFingerprint(t *testing.T) {
	for _, s := range []agentprotocol.MeshNodeSpec{serverSpec(), linkSpec()} {
		keyed, plain := ContainerSpec(s, testKey), ContainerSpec(s, "")
		require.Contains(t, keyed.Env, "TS_AUTHKEY="+testKey)
		require.NotContains(t, plain.Env, "TS_AUTHKEY="+testKey)
		for k, v := range keyed.Labels {
			require.NotContains(t, k+v, testKey)
		}
		require.Equal(t, KeyFingerprint(testKey), keyed.Labels[labelKeyFP])
		require.NotContains(t, keyed.Labels[labelKeyFP], testKey)
		require.Equal(t, plain.Labels[agentprotocol.LabelMeshSpec], keyed.Labels[agentprotocol.LabelMeshSpec],
			"the spec fingerprint never covers the key")
	}
}

// What is applied live must not recreate the node: the fingerprint ignores
// exposures, aliases and the exposure networks.
func TestSpecFingerprintCoversOnlyWhatNeedsARecreate(t *testing.T) {
	base := ContainerSpec(serverSpec(), "").Labels[agentprotocol.LabelMeshSpec]
	s := serverSpec()
	s.Exposures = s.Exposures[:1]
	require.Equal(t, base, ContainerSpec(s, "").Labels[agentprotocol.LabelMeshSpec], "exposures are applied through the watched serve config")
	s.Hostname = "nc-renamed"
	require.NotEqual(t, base, ContainerSpec(s, "").Labels[agentprotocol.LabelMeshSpec])

	lbase := ContainerSpec(linkSpec(), "").Labels[agentprotocol.LabelMeshSpec]
	l := linkSpec()
	l.Aliases = []string{"redis"}
	l.HasInternalNet = false
	require.Equal(t, lbase, ContainerSpec(l, "").Labels[agentprotocol.LabelMeshSpec], "aliases are re-attached, not recreated")
	l.TargetIP = "100.64.0.10"
	require.NotEqual(t, lbase, ContainerSpec(l, "").Labels[agentprotocol.LabelMeshSpec], "the DNAT target is container configuration")
	l = linkSpec()
	l.LoginServer = "https://other.example.com"
	require.NotEqual(t, lbase, ContainerSpec(l, "").Labels[agentprotocol.LabelMeshSpec])
}

func TestValidate(t *testing.T) {
	cases := map[string]func(s *agentprotocol.MeshNodeSpec){
		"bad node id":        func(s *agentprotocol.MeshNodeSpec) { s.NodeID = "../x" },
		"unknown kind":       func(s *agentprotocol.MeshNodeSpec) { s.Kind = "host" },
		"unknown provider":   func(s *agentprotocol.MeshNodeSpec) { s.Provider = "wireguard" },
		"bad hostname":       func(s *agentprotocol.MeshNodeSpec) { s.Hostname = "Bad Host" },
		"hostname env break": func(s *agentprotocol.MeshNodeSpec) { s.Hostname = "a\nTS_AUTHKEY=x" },
		"server with target": func(s *agentprotocol.MeshNodeSpec) { s.TargetIP = "100.64.0.1" },
		"server with alias":  func(s *agentprotocol.MeshNodeSpec) { s.Aliases = []string{"x"} },
		"exposure purpose":   func(s *agentprotocol.MeshNodeSpec) { s.Exposures[0].Purpose = "other" },
		"exposure slug":      func(s *agentprotocol.MeshNodeSpec) { s.Exposures[0].ServiceSlug = "A_B" },
		"exposure port":      func(s *agentprotocol.MeshNodeSpec) { s.Exposures[0].MeshPort = 70000 },
		"duplicate port":     func(s *agentprotocol.MeshNodeSpec) { s.Exposures[1].MeshPort = s.Exposures[0].MeshPort },
	}
	for name, mut := range cases {
		s := serverSpec()
		mut(&s)
		require.Error(t, Validate(s), name)
	}
	require.NoError(t, Validate(serverSpec()))

	lcases := map[string]func(s *agentprotocol.MeshNodeSpec){
		"headscale no login": func(s *agentprotocol.MeshNodeSpec) { s.LoginServer = "" },
		"login injection":    func(s *agentprotocol.MeshNodeSpec) { s.LoginServer = "https://hs.example.com --advertise-tags=x" },
		"login scheme":       func(s *agentprotocol.MeshNodeSpec) { s.LoginServer = "ftp://hs" },
		"link exposures":     func(s *agentprotocol.MeshNodeSpec) { s.Exposures = serverSpec().Exposures },
		"no environment":     func(s *agentprotocol.MeshNodeSpec) { s.EnvironmentID = "" },
		"no remote":          func(s *agentprotocol.MeshNodeSpec) { s.RemoteServerID = "" },
		"target not ip":      func(s *agentprotocol.MeshNodeSpec) { s.TargetIP = "db.example.com" },
		"target v6":          func(s *agentprotocol.MeshNodeSpec) { s.TargetIP = "fd7a:115c:a1e0::7" },
		"target loopback":    func(s *agentprotocol.MeshNodeSpec) { s.TargetIP = "127.0.0.1" },
		"target unspecified": func(s *agentprotocol.MeshNodeSpec) { s.TargetIP = "0.0.0.0" },
		"no aliases":         func(s *agentprotocol.MeshNodeSpec) { s.Aliases = nil },
		"alias with dot":     func(s *agentprotocol.MeshNodeSpec) { s.Aliases = []string{"a.b"} },
		"alias with newline": func(s *agentprotocol.MeshNodeSpec) { s.Aliases = []string{"a\nb"} },
	}
	for name, mut := range lcases {
		s := linkSpec()
		mut(&s)
		require.Error(t, Validate(s), name)
	}
	require.NoError(t, Validate(linkSpec()))
}

func TestAliasesAreSortedAndDeduplicated(t *testing.T) {
	require.Equal(t, []string{"a", "b"}, sortedAliases([]string{"b", "", "a", "b"}))
}
