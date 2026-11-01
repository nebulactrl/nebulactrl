package app_test

import (
	"sort"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/api"
	"github.com/nebulactrl/nebulactrl/internal/app"
	"github.com/nebulactrl/nebulactrl/internal/config"
	"github.com/nebulactrl/nebulactrl/internal/policy"
)

func pathOperations(item *huma.PathItem) map[string]*huma.Operation {
	return map[string]*huma.Operation{
		"GET": item.Get, "PUT": item.Put, "POST": item.Post, "DELETE": item.Delete,
		"OPTIONS": item.Options, "HEAD": item.Head, "PATCH": item.Patch, "TRACE": item.Trace,
	}
}

// TestEveryOperationDeclaresScopes is the contract of the MCP catalog: every
// operation is either api.Public or wrapped in api.WithScopes, the declared
// scopes are real scopes, and the published OpenAPI security requirement
// carries them.
func TestEveryOperationDeclaresScopes(t *testing.T) {
	a, err := app.Build(&config.Config{}, nil, nil)
	require.NoError(t, err)
	defer a.Close()

	var checked int
	for path, item := range a.API.OpenAPI().Paths {
		for method, op := range pathOperations(item) {
			if op == nil {
				continue
			}
			checked++
			where := method + " " + path + " (" + op.OperationID + ")"
			public, scoped := isPublic(op), api.ScopesOf(op) != nil
			require.True(t, public || scoped, "%s has neither api.WithScopes nor api.Public", where)
			require.False(t, public && scoped, "%s is both public and scoped", where)

			if public {
				require.Equal(t, []map[string][]string{{}}, op.Security, "%s: public security", where)
				continue
			}
			scopes := api.ScopesOf(op)
			require.NotNil(t, scopes, where)
			for _, s := range scopes {
				require.True(t, policy.ValidScope(s), "%s declares unknown scope %q", where, s)
			}
			require.Equal(t, []map[string][]string{
				{api.SchemeBearer: scopes},
				{api.SchemeCookie: scopes},
			}, op.Security, "%s: published security must match the declared scopes", where)
		}
	}
	require.Greater(t, checked, 300, "suspiciously few operations")
}

// TestPublicOperationsAreKnown pins the unauthenticated surface: adding a
// public operation is a deliberate act that changes this list.
func TestPublicOperationsAreKnown(t *testing.T) {
	a, err := app.Build(&config.Config{}, nil, nil)
	require.NoError(t, err)
	defer a.Close()

	var public []string
	for _, item := range a.API.OpenAPI().Paths {
		for _, op := range pathOperations(item) {
			if op != nil && isPublic(op) {
				public = append(public, op.OperationID)
			}
		}
	}
	sort.Strings(public)
	require.Equal(t, expectedPublicOperations, public)
}

var expectedPublicOperations = []string{
	"acceptInvite",
	"checkPasswordResetToken",
	"confirmPasswordReset",
	"getPasswordResetStatus",
	"getSecondFactorMethods",
	"getSetupStatus",
	"listLoginProviders",
	"login",
	"logout",
	"passkeyLogin",
	"passkeyLoginOptions",
	"passkeySecondFactor",
	"passkeySecondFactorOptions",
	"previewInvite",
	"providerLoginCallback",
	"requestPasswordReset",
	"setup",
	"startProviderLogin",
	"verifySecondFactor",
}

// isPublic reports whether op was marked with api.Public.
func isPublic(op *huma.Operation) bool {
	public, _ := op.Metadata[api.PublicKey].(bool)
	return public
}
