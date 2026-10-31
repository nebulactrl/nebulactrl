package registries

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/stretchr/testify/require"
)

// parseRepoToken verifies raw against the JWKS of ms exactly as Distribution
// does (ES256, kid from the file, issuer, audience, time) and returns the claims.
func parseRepoToken(t *testing.T, raw string, ms ManagedSecrets, at time.Time) (jwt.Claims, []tokenAccess, string) {
	t.Helper()
	jwks, err := ms.JWKS()
	require.NoError(t, err)
	var set jose.JSONWebKeySet
	require.NoError(t, json.Unmarshal([]byte(jwks), &set))
	parsed, err := jwt.ParseSigned(raw, []jose.SignatureAlgorithm{jose.ES256})
	require.NoError(t, err)
	require.Len(t, parsed.Headers, 1)
	kid := parsed.Headers[0].KeyID
	keys := set.Key(kid)
	require.Len(t, keys, 1, "the token's kid must name a key of the JWKS file")
	var std jwt.Claims
	var extra struct {
		Access []tokenAccess `json:"access"`
	}
	require.NoError(t, parsed.Claims(keys[0].Key, &std, &extra))
	require.NoError(t, std.Validate(jwt.Expected{Issuer: ManagedIssuer, AnyAudience: jwt.Audience{BuiltinService}, Time: at}))
	return std, extra.Access, kid
}

func TestSignRepoTokenIsAnExactNameES256JWT(t *testing.T) {
	ms, err := GenerateManagedSecrets()
	require.NoError(t, err)
	now := time.Now()

	tok, err := SignRepoToken(ms, now, 5*time.Minute,
		RepoAccess{Name: "team-a/app", Actions: []string{ActionPush, ActionPull, ActionPull}},
		RepoAccess{Name: "team-a/base", Actions: []string{ActionPull}})
	require.NoError(t, err)
	std, acc, kid := parseRepoToken(t, tok, ms, now)

	require.Equal(t, ms.RegistryKeyID, kid, "kid is the JWK thumbprint of the registry's key")
	require.Equal(t, ManagedIssuer, std.Issuer)
	require.Equal(t, []string{BuiltinService}, []string(std.Audience))
	require.NotEmpty(t, std.ID, "jti")
	require.WithinDuration(t, now.Add(5*time.Minute), std.Expiry.Time(), time.Second)
	require.True(t, std.NotBefore.Time().Before(now))
	require.Equal(t, []tokenAccess{
		{Type: "repository", Name: "team-a/app", Actions: []string{"pull", "push"}},
		{Type: "repository", Name: "team-a/base", Actions: []string{"pull"}},
	}, acc, "actions are sorted and de-duplicated; names are exactly what was asked, never a pattern")

	// The registry decodes the header: alg ES256, typ JWT.
	hdr, err := base64.RawURLEncoding.DecodeString(strings.Split(tok, ".")[0])
	require.NoError(t, err)
	var h map[string]any
	require.NoError(t, json.Unmarshal(hdr, &h))
	require.Equal(t, "ES256", h["alg"])
	require.Equal(t, "JWT", h["typ"])

	// No access: a token that only authenticates (the /v2/ ping). The claim is an
	// empty list, not null, which Distribution would not read as "no access".
	tok, err = SignRepoToken(ms, now, time.Minute)
	require.NoError(t, err)
	_, acc, _ = parseRepoToken(t, tok, ms, now)
	require.NotNil(t, acc)
	require.Empty(t, acc)
	payload, err := base64.RawURLEncoding.DecodeString(strings.Split(tok, ".")[1])
	require.NoError(t, err)
	require.Contains(t, string(payload), `"access":[]`)

	// It is a token of this key and nobody else's, and it expires.
	other, err := GenerateManagedSecrets()
	require.NoError(t, err)
	otherJWKS, err := other.JWKS()
	require.NoError(t, err)
	var set jose.JSONWebKeySet
	require.NoError(t, json.Unmarshal([]byte(otherJWKS), &set))
	parsed, err := jwt.ParseSigned(tok, []jose.SignatureAlgorithm{jose.ES256})
	require.NoError(t, err)
	var std2 jwt.Claims
	require.Error(t, parsed.Claims(set.Keys[0].Key, &std2), "another registry's key does not verify it")
	_, _, _ = parseRepoToken(t, tok, ms, now.Add(30*time.Second))
	std3, _, _ := parseRepoToken(t, tok, ms, now)
	require.Error(t, std3.Validate(jwt.Expected{Issuer: ManagedIssuer, Time: now.Add(2 * time.Minute)}), "expired")

	_, err = SignRepoToken(ms, now, 0)
	require.Error(t, err, "a token needs a positive life")
	_, err = SignRepoToken(ManagedSecrets{}, now, time.Minute)
	require.Error(t, err, "no key, no token")
}

func TestManagedSecretsAndJWKS(t *testing.T) {
	a, err := GenerateManagedSecrets()
	require.NoError(t, err)
	b, err := GenerateManagedSecrets()
	require.NoError(t, err)
	require.NotEqual(t, a.RegistryKeyPEM, b.RegistryKeyPEM)
	require.NotEqual(t, a.RegistryKeyID, b.RegistryKeyID)

	jwks, err := a.JWKS()
	require.NoError(t, err)
	var set struct {
		Keys []map[string]any `json:"keys"`
	}
	require.NoError(t, json.Unmarshal([]byte(jwks), &set))
	require.Len(t, set.Keys, 1)
	k := set.Keys[0]
	require.Equal(t, "EC", k["kty"])
	require.Equal(t, "P-256", k["crv"])
	require.Equal(t, "sig", k["use"])
	require.Equal(t, "ES256", k["alg"])
	require.Equal(t, a.RegistryKeyID, k["kid"])
	require.NotContains(t, k, "d", "the private scalar never leaves the control plane")
	require.NotContains(t, jwks, "PRIVATE")
}
