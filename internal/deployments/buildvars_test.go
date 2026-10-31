package deployments

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/services"
)

func TestBuildKitSupported(t *testing.T) {
	for facts, want := range map[string]bool{
		`{"dockerVersion":"29.0.1"}`: true, `{"dockerVersion":"23.0.0"}`: true, `{"dockerVersion":"v27.1"}`: true,
		`{"dockerVersion":"20.10.24"}`: false, `{"dockerVersion":"19.03.1"}`: false,
		``: true, `{}`: true, `{"dockerVersion":"weird"}`: true, `not json`: true,
	} {
		require.Equal(t, want, BuildKitSupported(json.RawMessage(facts)), facts)
	}
}

func TestBuildVarProblems(t *testing.T) {
	mk := func(source string, scope map[string]string, secret map[string]bool, facts string) *inputs {
		in := &inputs{resolved: services.Resolved{Vars: map[string]string{}, Secret: secret, Scope: scope}}
		in.sc.Service.SourceType = source
		in.server = &db.Server{Name: "old-host", Facts: json.RawMessage(facts)}
		return in
	}
	// Image sources cannot have build variables.
	p := mk(services.SourceImage, map[string]string{"ARG": "build"}, nil, `{}`).buildVarProblems()
	require.NotNil(t, p)
	require.Equal(t, "build_scope_requires_git", p.Code)
	// Runtime-only variables are fine anywhere.
	require.Nil(t, mk(services.SourceImage, nil, nil, `{}`).buildVarProblems())
	// A secret build variable needs BuildKit; plain build args do not.
	p = mk(services.SourceGit, map[string]string{"T": "both"}, map[string]bool{"T": true}, `{"dockerVersion":"20.10.1"}`).buildVarProblems()
	require.NotNil(t, p)
	require.Equal(t, "buildkit_required", p.Code)
	require.Contains(t, p.Message, "old-host")
	require.Nil(t, mk(services.SourceGit, map[string]string{"A": "build"}, nil, `{"dockerVersion":"20.10.1"}`).buildVarProblems())
	require.Nil(t, mk(services.SourceGit, map[string]string{"T": "both"}, map[string]bool{"T": true}, `{"dockerVersion":"29.0.1"}`).buildVarProblems())
}

func TestClassifyBuildVar(t *testing.T) {
	args, secrets := classifyBuildVar(nil, nil, "PLAIN", false)
	args, secrets = classifyBuildVar(args, secrets, "TOKEN", true)
	require.Equal(t, []string{"PLAIN"}, args)
	require.Equal(t, []string{"TOKEN"}, secrets, "a secret is never offered as a build arg")
}
