package gitproviders_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/gitproviders"
	"github.com/nebulactrl/nebulactrl/internal/netguard"
)

// The base URL of a connection is set by a team admin: the control plane must
// not fetch loopback, private or metadata addresses with it.
func TestProviderRequestsToInternalAddressesAreRefused(t *testing.T) {
	f := newFixture(t)
	t.Setenv(netguard.AllowEnv, "") // the fixture allows loopback for its fake provider
	f.provider.json("GET /api/v1/user", 200, map[string]string{"login": "alice"})

	_, err := f.svc.CreateConnection(f.ctx, gitproviders.CreateConnectionInput{
		Provider: gitproviders.Gitea, Name: "Internal", BaseURL: f.provider.srv.URL, AuthType: gitproviders.AuthToken, Token: "gta_secret_token_123456",
	})
	var ae *apierr.Error
	require.ErrorAs(t, err, &ae)
	require.Equal(t, "credentials_invalid", ae.Code)
	require.Contains(t, ae.Message, "refused")
	require.Empty(t, f.provider.callsTo("GET", "/api/v1/user"), "the request never reached the internal server")
}

// Error answers of a remote are reduced to their status: an internal service
// behind a base URL must not be readable through the error message.
func TestProviderErrorBodiesAreNotEchoed(t *testing.T) {
	f := newFixture(t)
	f.provider.on("GET /api/v1/user", func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("INTERNAL-ONLY-BODY-7f3a"))
	})
	_, err := f.svc.CreateConnection(f.ctx, gitproviders.CreateConnectionInput{
		Provider: gitproviders.Gitea, Name: "Leaky", BaseURL: f.provider.srv.URL, AuthType: gitproviders.AuthToken, Token: "gta_secret_token_123456",
	})
	var ae *apierr.Error
	require.ErrorAs(t, err, &ae)
	require.Contains(t, ae.Message, "403")
	require.NotContains(t, ae.Message, "INTERNAL-ONLY-BODY")
}
