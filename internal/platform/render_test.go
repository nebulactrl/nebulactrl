package platform

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

func mustYAML(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	require.NoError(t, yaml.Unmarshal([]byte(s), &m), s)
	return m
}

func TestHeadscaleConfigUsesDatabasePolicyMode(t *testing.T) {
	m := mustYAML(t, headscaleConfig("https://hs.example.com/"))
	require.Equal(t, "https://hs.example.com", m["server_url"], "no trailing slash")
	pol := m["policy"].(map[string]any)
	require.Equal(t, "database", pol["mode"], "NebulaCtrl writes the policy through the API (internal/mesh)")
	require.NotContains(t, pol, "path", "no static policy file any more")
	require.Equal(t, false, m["derp"].(map[string]any)["server"].(map[string]any)["enabled"])
}

func TestResolvePublicURL(t *testing.T) {
	u, err := resolvePublicURL("", "s3.example.com")
	require.NoError(t, err)
	require.Equal(t, "https://s3.example.com", u)
	u, err = resolvePublicURL("http://s3.example.com:8080/", "s3.example.com")
	require.NoError(t, err)
	require.Equal(t, "http://s3.example.com:8080", u)
	for _, bad := range []string{"ftp://s3.example.com", "https://other.example.com", "https://u:p@s3.example.com", "https://s3.example.com/x", "https://s3.example.com?x=1", "not a url"} {
		_, err := resolvePublicURL(bad, "s3.example.com")
		require.Error(t, err, bad)
	}
}

func TestExecOutputCleanup(t *testing.T) {
	require.Equal(t, "User created\nhskey-api-abc\n", cleanOutput("\x1b[32mUser created\x1b[0m\r\nhskey-api-abc\r\n"))
	require.Equal(t, "hskey-api-abc", lastLine("User created\nhskey-api-abc\n\n"))
}
