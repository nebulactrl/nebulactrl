package proxyroutes

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

func principalCtx(scopes ...string) context.Context {
	return auth.WithPrincipal(context.Background(), &auth.Principal{Type: auth.TypeUser, Label: "t", Scopes: scopes})
}

func stubLookup(t *testing.T, answers map[string][]string) {
	t.Helper()
	orig := lookupHost
	t.Cleanup(func() { lookupHost = orig })
	lookupHost = func(_ context.Context, host string) ([]string, error) {
		if a, ok := answers[host]; ok {
			return a, nil
		}
		return nil, errors.New("no such host")
	}
}

// A developer cannot point a public-looking name at loopback, link-local or a
// private address, and cannot type a managed container name as an upstream.
func TestUpstreamNamesAreJudgedByTheirAddresses(t *testing.T) {
	stubLookup(t, map[string][]string{
		"meta.attacker.example": {"169.254.169.254"},
		"lo.attacker.example":   {"127.0.0.1"},
		"mixed.example.com":     {"93.184.216.34", "10.0.0.5"},
		"lan.example.com":       {"192.168.1.5"},
		"public.example.com":    {"93.184.216.34"},
	})
	dev := principalCtx(policy.ScopeProjectsWrite)
	adm := principalCtx(policy.ScopeAdmin)
	def := agentprotocol.DefaultUpstreamPolicy()

	for _, up := range []string{
		"meta.attacker.example:80", "lo.attacker.example:80", "mixed.example.com:80", "lan.example.com:80",
		"nc-m.attacker.example:80", "nc-web-deadbeef-0:80",
	} {
		require.Error(t, checkUpstreamName(dev, up, def), "developer: "+up)
	}
	require.NoError(t, checkUpstreamName(dev, "public.example.com:80", def))
	require.NoError(t, checkUpstreamName(dev, "unresolvable.lan:80", def), "names only the server's DNS knows are left to the agent")

	// Loopback and metadata results stay denied for an administrator unless the server's policy allows them.
	require.Error(t, checkUpstreamName(adm, "meta.attacker.example:80", def))
	require.Error(t, checkUpstreamName(adm, "lo.attacker.example:80", def))
	require.NoError(t, checkUpstreamName(adm, "lan.example.com:80", def), "an admin may use a private address")
	require.NoError(t, checkUpstreamName(adm, "meta.attacker.example:80", agentprotocol.UpstreamPolicy{AllowLinkLocal: true}))
	require.Error(t, checkUpstreamName(adm, "nc-m.attacker.example:80", def), "managed names are never typed")
}
