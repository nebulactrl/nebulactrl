package tailnet

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/internal/agent/proxy"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

const (
	svcID = "11111111-2222-3333-4444-555555555555"
	envID = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
)

func baseSpec() agentprotocol.ServiceTailnetSpec {
	return agentprotocol.ServiceTailnetSpec{
		Mode: agentprotocol.TailnetEnsure, ServiceID: svcID, ServiceSlug: "web", EnvironmentID: envID,
		Provider: agentprotocol.TailnetProviderTailscale, Hostname: "web-prod", Tags: []string{"tag:nebula-svc"},
		Web: true, Port: 8080, IdentityHash: "abc",
	}
}

// The sidecar is the most privileged thing a service setting can create: pin
// every property of its container spec.
func TestSidecarContainerSpec(t *testing.T) {
	spec := baseSpec()
	cs := ContainerSpec(spec, "")

	require.Equal(t, "nc-ts-11111111", cs.Name)
	require.Equal(t, proxy.TailscaleImage, cs.Image)
	require.Contains(t, cs.Image, "@sha256:", "pinned by digest")
	require.ElementsMatch(t, []string{
		"TS_USERSPACE=true",
		"TS_STATE_DIR=/var/lib/tailscale",
		"TS_AUTH_ONCE=true",
		"TS_ACCEPT_DNS=false",
		"TS_HOSTNAME=web-prod",
		"TS_SERVE_CONFIG=/config/serve.json",
		"TS_ENABLE_HEALTH_CHECK=true",
		"TS_LOCAL_ADDR_PORT=127.0.0.1:9002",
	}, cs.Env)

	require.Equal(t, "true", cs.Labels[agentprotocol.LabelManaged])
	require.Equal(t, svcID, cs.Labels[agentprotocol.LabelTailnet])
	require.Equal(t, envID, cs.Labels[agentprotocol.LabelEnvironment])
	require.NotEmpty(t, cs.Labels[agentprotocol.LabelTailnetSpec])
	_, hasService := cs.Labels[agentprotocol.LabelService]
	require.False(t, hasService, "the sidecar must not look like a replica: the orphan sweep, stats and replica listings key on the service label")

	// Only the environment network, no alias, no published ports.
	require.Equal(t, "nebulactrl-env-aaaaaaaa", cs.Networks[0].Network)
	require.Len(t, cs.Networks, 1)
	require.Empty(t, cs.Networks[0].Aliases)
	require.Empty(t, cs.Ports)
	require.Empty(t, cs.NetworkMode)

	// Userspace networking: nothing privileged.
	require.Equal(t, []string{"ALL"}, cs.CapDrop)
	require.Empty(t, cs.CapAdd)
	require.Empty(t, cs.Devices)
	require.Equal(t, []string{"no-new-privileges"}, cs.SecurityOpt)
	for _, m := range cs.Mounts {
		require.NotContains(t, m.Volume, "docker.sock")
		require.NotContains(t, m.Target, "docker.sock")
	}
	require.Equal(t, []docker.VolumeMount{
		{Volume: "nc-ts-11111111-state", Target: "/var/lib/tailscale"},
		{Volume: "nc-ts-11111111-config", Target: "/config"},
	}, cs.Mounts)

	require.Equal(t, "unless-stopped", cs.RestartPolicy)
	require.EqualValues(t, 256<<20, cs.MemoryBytes)
	require.NotNil(t, cs.Healthcheck)
	require.Equal(t, []string{"CMD", "wget", "-q", "-O", "/dev/null", "http://127.0.0.1:9002/healthz"}, cs.Healthcheck.Test)
	require.Equal(t, "json-file", cs.LogConfig.Type)
}

// A service that blocks egress sits on the internal network only, so its
// sidecar joins that one too (never the edge network), and its spec fingerprint
// changes so an existing sidecar is recreated with the new networks.
func TestSidecarOfAnEgressBlockedServiceAlsoJoinsTheInternalNetwork(t *testing.T) {
	plain := ContainerSpec(baseSpec(), "")
	spec := baseSpec()
	spec.BlockEgress = true
	cs := ContainerSpec(spec, "")

	require.Equal(t, []docker.NetworkAttachment{{Network: "nebulactrl-env-aaaaaaaa"}, {Network: "nebulactrl-env-aaaaaaaa-int"}}, cs.Networks)
	for _, n := range cs.Networks {
		require.NotEqual(t, agentprotocol.EdgeNetwork, n.Network)
	}
	require.NotEqual(t, plain.Labels[agentprotocol.LabelTailnetSpec], cs.Labels[agentprotocol.LabelTailnetSpec])
}

func TestAuthKeyIsNeverPartOfTheFingerprint(t *testing.T) {
	spec := baseSpec()
	plain := ContainerSpec(spec, "")
	keyed := ContainerSpec(spec, "tskey-auth-SECRET-1234567890")

	require.Equal(t, plain.Labels[agentprotocol.LabelTailnetSpec], keyed.Labels[agentprotocol.LabelTailnetSpec])
	require.Contains(t, keyed.Env, "TS_AUTHKEY=tskey-auth-SECRET-1234567890")
	for _, l := range keyed.Labels {
		require.NotContains(t, l, "SECRET")
	}
	for _, e := range plain.Env {
		require.False(t, strings.HasPrefix(e, "TS_AUTHKEY="))
	}

	// Exposures are applied live: they do not recreate the container.
	moved := spec
	moved.Port = 9000
	moved.TCP = []agentprotocol.TailnetTCP{{Listen: 5432, Target: 5432}}
	moved.Funnel = true
	require.Equal(t, plain.Labels[agentprotocol.LabelTailnetSpec], ContainerSpec(moved, "").Labels[agentprotocol.LabelTailnetSpec])

	// A different hostname is a different identity and recreates it.
	renamed := spec
	renamed.Hostname = "other"
	require.NotEqual(t, plain.Labels[agentprotocol.LabelTailnetSpec], ContainerSpec(renamed, "").Labels[agentprotocol.LabelTailnetSpec])
}

func TestHeadscaleLoginServerAndNoAdvertiseTags(t *testing.T) {
	spec := baseSpec()
	spec.Provider = agentprotocol.TailnetProviderHeadscale
	spec.LoginServer = "https://hs.example.com"
	cs := ContainerSpec(spec, "key-1234567")
	require.Contains(t, cs.Env, "TS_EXTRA_ARGS=--login-server=https://hs.example.com")
	for _, e := range cs.Env {
		require.NotContains(t, e, "advertise-tags", "a tagged key carries its tags; --advertise-tags makes Headscale reject the node")
	}
}

// jsonEq compares two JSON documents structurally.
func jsonEq(t *testing.T, want string, got []byte) {
	t.Helper()
	var w, g any
	require.NoError(t, json.Unmarshal([]byte(want), &w))
	require.NoError(t, json.Unmarshal(got, &g), string(got))
	require.Equal(t, w, g)
}

func TestServeConfigTailscale(t *testing.T) {
	spec := baseSpec()
	spec.TCP = []agentprotocol.TailnetTCP{{Listen: 5432, Target: 5432}, {Listen: 6380, Target: 6379, TLS: true}}
	got, err := ServeConfig(spec, "")
	require.NoError(t, err)
	jsonEq(t, `{
		"TCP": {
			"443": {"HTTPS": true},
			"5432": {"TCPForward": "web:5432"},
			"6380": {"TCPForward": "web:6379", "TerminateTLS": "${TS_CERT_DOMAIN}"}
		},
		"Web": {"${TS_CERT_DOMAIN}:443": {"Handlers": {"/": {"Proxy": "http://web:8080"}}}}
	}`, got)
	require.NotContains(t, string(got), "AllowFunnel", "Funnel is opt-in")

	spec.Funnel = true
	got, err = ServeConfig(spec, "")
	require.NoError(t, err)
	var cfg map[string]map[string]any
	require.NoError(t, json.Unmarshal(got, &cfg))
	require.Equal(t, map[string]any{"${TS_CERT_DOMAIN}:443": true}, cfg["AllowFunnel"])
}

func TestServeConfigTCPOnly(t *testing.T) {
	spec := baseSpec()
	spec.Web, spec.Port = false, 0
	spec.TCP = []agentprotocol.TailnetTCP{{Listen: 5432, Target: 5432}}
	got, err := ServeConfig(spec, "")
	require.NoError(t, err)
	jsonEq(t, `{"TCP": {"5432": {"TCPForward": "web:5432"}}}`, got)

	spec.TCP = nil
	got, err = ServeConfig(spec, "")
	require.NoError(t, err)
	jsonEq(t, `{}`, got)
}

func TestServeConfigHeadscaleNeedsTheRealName(t *testing.T) {
	spec := baseSpec()
	spec.Provider = agentprotocol.TailnetProviderHeadscale
	spec.LoginServer = "https://hs.example.com"
	spec.TCP = []agentprotocol.TailnetTCP{{Listen: 5432, Target: 5432}}

	// Before the node has a name only the TCP forwards are served.
	got, err := ServeConfig(spec, "")
	require.NoError(t, err)
	jsonEq(t, `{"TCP": {"5432": {"TCPForward": "web:5432"}}}`, got)

	got, err = ServeConfig(spec, "web-prod.hs.example.com")
	require.NoError(t, err)
	jsonEq(t, `{
		"TCP": {"80": {"HTTP": true}, "5432": {"TCPForward": "web:5432"}},
		"Web": {"web-prod.hs.example.com:80": {"Handlers": {"/": {"Proxy": "http://web:8080"}}}}
	}`, got)
	require.NotContains(t, string(got), "HTTPS")
	require.NotContains(t, string(got), "${TS_CERT_DOMAIN}", "containerboot would substitute no-https")
}

func TestValidate(t *testing.T) {
	require.NoError(t, Validate(baseSpec()))

	mut := func(f func(*agentprotocol.ServiceTailnetSpec)) agentprotocol.ServiceTailnetSpec {
		s := baseSpec()
		f(&s)
		return s
	}
	bad := map[string]agentprotocol.ServiceTailnetSpec{
		"no service":       mut(func(s *agentprotocol.ServiceTailnetSpec) { s.ServiceID = "" }),
		"slug injection":   mut(func(s *agentprotocol.ServiceTailnetSpec) { s.ServiceSlug = "web/../x" }),
		"hostname":         mut(func(s *agentprotocol.ServiceTailnetSpec) { s.Hostname = "Web Prod" }),
		"provider":         mut(func(s *agentprotocol.ServiceTailnetSpec) { s.Provider = "wireguard" }),
		"headscale no url": mut(func(s *agentprotocol.ServiceTailnetSpec) { s.Provider = agentprotocol.TailnetProviderHeadscale }),
		"headscale shell": mut(func(s *agentprotocol.ServiceTailnetSpec) {
			s.Provider, s.LoginServer = agentprotocol.TailnetProviderHeadscale, "https://hs.example.com; rm -rf /"
		}),
		"headscale funnel": mut(func(s *agentprotocol.ServiceTailnetSpec) {
			s.Provider, s.LoginServer, s.Funnel = agentprotocol.TailnetProviderHeadscale, "https://hs.example.com", true
		}),
		"headscale tls": mut(func(s *agentprotocol.ServiceTailnetSpec) {
			s.Provider, s.LoginServer = agentprotocol.TailnetProviderHeadscale, "https://hs.example.com"
			s.TCP = []agentprotocol.TailnetTCP{{Listen: 5432, Target: 5432, TLS: true}}
		}),
		"web no port":   mut(func(s *agentprotocol.ServiceTailnetSpec) { s.Port = 0 }),
		"funnel no web": mut(func(s *agentprotocol.ServiceTailnetSpec) { s.Web, s.Port, s.Funnel = false, 0, true }),
		"tcp range":     mut(func(s *agentprotocol.ServiceTailnetSpec) { s.TCP = []agentprotocol.TailnetTCP{{Listen: 0, Target: 1}} }),
		"tcp duplicate": mut(func(s *agentprotocol.ServiceTailnetSpec) {
			s.TCP = []agentprotocol.TailnetTCP{{Listen: 1, Target: 1}, {Listen: 1, Target: 2}}
		}),
		"tcp web clash": mut(func(s *agentprotocol.ServiceTailnetSpec) {
			s.TCP = []agentprotocol.TailnetTCP{{Listen: 443, Target: 443}}
		}),
	}
	for name, s := range bad {
		t.Run(name, func(t *testing.T) { require.Error(t, Validate(s)) })
	}
}
