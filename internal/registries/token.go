package registries

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/google/uuid"
)

// RepoAccess is one resource and the actions a token grants on it. Type is
// "repository" when empty; the control plane's own purge also asks for
// "registry" ("catalog", "*").
type RepoAccess struct {
	Type    string
	Name    string
	Actions []string
}

// tokenAccess is one entry of the "access" claim.
type tokenAccess struct {
	Type    string   `json:"type"`
	Name    string   `json:"name"`
	Actions []string `json:"actions"`
}

// SignRepoToken signs a registry access token (Docker token authentication,
// https://distribution.github.io/distribution/spec/auth/token/) for exactly the
// given repositories and actions: ES256, kid = the key's thumbprint,
// iss = ManagedIssuer, aud = BuiltinService. With no access the token only
// authenticates (the /v2/ ping).
//
// There is no token endpoint: the control plane's forwarder signs the token for
// the request it has just checked and adds it to that request.
func SignRepoToken(ms ManagedSecrets, now time.Time, ttl time.Duration, access ...RepoAccess) (string, error) {
	if ttl <= 0 {
		return "", errors.New("token ttl must be positive")
	}
	key, err := ms.signingKey()
	if err != nil {
		return "", err
	}
	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.ES256, Key: jose.JSONWebKey{Key: key, KeyID: ms.RegistryKeyID}},
		(&jose.SignerOptions{}).WithType("JWT"),
	)
	if err != nil {
		return "", err
	}
	claims := make([]tokenAccess, 0, len(access))
	for _, a := range access {
		acts := slices.Clone(a.Actions)
		slices.Sort(acts)
		typ := a.Type
		if typ == "" {
			typ = "repository"
		}
		claims = append(claims, tokenAccess{Type: typ, Name: a.Name, Actions: slices.Compact(acts)})
	}
	std := jwt.Claims{
		Issuer: ManagedIssuer, Subject: "nebulactrl", Audience: jwt.Audience{BuiltinService},
		Expiry: jwt.NewNumericDate(now.Add(ttl)), NotBefore: jwt.NewNumericDate(now.Add(-5 * time.Second)),
		IssuedAt: jwt.NewNumericDate(now), ID: uuid.NewString(),
	}
	return jwt.Signed(signer).Claims(std).Claims(struct {
		Access []tokenAccess `json:"access"`
	}{Access: claims}).Serialize()
}

// signToken signs a token with the built-in registry's key (the retention sweep's delete token).
func (s *Service) signToken(ctx context.Context, ttl time.Duration, access ...RepoAccess) (string, error) {
	ms, ok, err := LoadManagedSecrets(ctx, s.q, s.Box)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", errors.New("the built-in registry has no signing key")
	}
	return SignRepoToken(ms, s.Now(), ttl, access...)
}
