package tailnetcfg

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNormalizeCollapsesDisabledAndCanonicalizes(t *testing.T) {
	require.Nil(t, Normalize(nil))
	require.Nil(t, Normalize(&TailnetConfig{Enabled: false, Hostname: "web", Web: true}), "a disabled config is not stored")

	n := Normalize(&TailnetConfig{
		Enabled: true, Hostname: "  Web-Prod ", Web: true,
		ExtraTags: []string{"tag:b", " tag:a", "tag:b", ""},
		TCP:       []TCPForward{{Listen: 6379}, {Listen: 5432, Target: 5433}},
	})
	require.NotNil(t, n)
	require.Equal(t, "web-prod", n.Hostname)
	require.Equal(t, []string{"tag:a", "tag:b"}, n.ExtraTags)
	require.Equal(t, []TCPForward{{Listen: 5432, Target: 5433}, {Listen: 6379, Target: 6379}}, n.TCP, "sorted by listen port, target defaults to listen")

	empty := Normalize(&TailnetConfig{Enabled: true, Web: true})
	require.Nil(t, empty.ExtraTags)
	require.Nil(t, empty.TCP)
}

func TestValidate(t *testing.T) {
	ok := TailnetConfig{Enabled: true, Web: true}
	require.NoError(t, ok.Validate(8080))

	cases := []struct {
		name string
		c    TailnetConfig
		port int
		want string
	}{
		{"nothing exposed", TailnetConfig{Enabled: true}, 8080, "at least one TCP port"},
		{"web without http port", TailnetConfig{Enabled: true, Web: true}, 0, "HTTP port"},
		{"funnel without web", TailnetConfig{Enabled: true, Funnel: true, TCP: []TCPForward{{Listen: 5432, Target: 5432}}}, 0, "enable web"},
		{"bad hostname", TailnetConfig{Enabled: true, Web: true, Hostname: "Not_Valid"}, 80, "RFC 1123"},
		{"long hostname", TailnetConfig{Enabled: true, Web: true, Hostname: strings.Repeat("a", 64)}, 80, "RFC 1123"},
		{"bad tag", TailnetConfig{Enabled: true, Web: true, ExtraTags: []string{"nebula"}}, 80, "not a valid tag"},
		{"port range", TailnetConfig{Enabled: true, TCP: []TCPForward{{Listen: 70000, Target: 1}}}, 0, "out of range"},
		{"target range", TailnetConfig{Enabled: true, TCP: []TCPForward{{Listen: 5432, Target: 0}}}, 0, "out of range"},
		{"duplicate listen", TailnetConfig{Enabled: true, TCP: []TCPForward{{Listen: 5432, Target: 1}, {Listen: 5432, Target: 2}}}, 0, "used twice"},
		{"web collision", TailnetConfig{Enabled: true, Web: true, TCP: []TCPForward{{Listen: 443, Target: 443}}}, 80, "used by the web exposure"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.c.Validate(tc.port)
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.want)
		})
	}

	many := TailnetConfig{Enabled: true}
	for i := 1; i <= MaxTCPForwards+1; i++ {
		many.TCP = append(many.TCP, TCPForward{Listen: 1000 + i, Target: 1000 + i})
	}
	require.ErrorContains(t, many.Validate(0), "at most")
}

func TestDefaultHostname(t *testing.T) {
	require.Equal(t, "web-prod", DefaultHostname("web", "prod"))
	require.Equal(t, "web", DefaultHostname("web", "web"), "no stutter")
	require.Equal(t, "my-api-staging-1", DefaultHostname("My_API", "Staging 1"))
	require.Equal(t, "service", DefaultHostname("", ""))
	long := DefaultHostname(strings.Repeat("a", 50), strings.Repeat("b", 50))
	require.LessOrEqual(t, len(long), 63)
	require.NoError(t, ValidHostname(long))
	require.False(t, strings.HasSuffix(long, "-"))

	c := TailnetConfig{Enabled: true, Hostname: "custom"}
	require.Equal(t, "custom", c.EffectiveHostname("web", "prod"))
	require.Equal(t, "web-prod", TailnetConfig{Enabled: true}.EffectiveHostname("web", "prod"))
}

func TestTagsAreTheSortedUnionAndIdentityTracksIdentityOnly(t *testing.T) {
	c := TailnetConfig{Enabled: true, ExtraTags: []string{"tag:db", "tag:nebula-svc"}}
	require.Equal(t, []string{"tag:db", "tag:nebula-svc"}, c.Tags([]string{"tag:nebula-svc"}))

	a := IdentityHash("tailscale", "", "web-prod", []string{"tag:b", "tag:a"})
	require.Equal(t, a, IdentityHash("tailscale", "", "web-prod", []string{"tag:a", "tag:b"}), "tag order is irrelevant")
	require.NotEqual(t, a, IdentityHash("tailscale", "", "web-prod2", []string{"tag:a", "tag:b"}))
	require.NotEqual(t, a, IdentityHash("headscale", "https://hs.example.com", "web-prod", []string{"tag:a", "tag:b"}))
	require.NotEqual(t, a, IdentityHash("tailscale", "", "web-prod", []string{"tag:a"}))
}
