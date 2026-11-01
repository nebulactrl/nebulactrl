package engine

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agent/build"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

func TestReplicaEnvReplacesStaleEntries(t *testing.T) {
	env := replicaEnv([]string{"A=1", "NEBULA_REPLICA_INDEX=0", "NEBULA_REPLICA_COUNT=1"}, 2, 3)
	require.Equal(t, []string{"A=1", "NEBULA_REPLICA_INDEX=2", "NEBULA_REPLICA_COUNT=3"}, env)
	require.Contains(t, replicaEnv(nil, 0, 0), "NEBULA_REPLICA_COUNT=1", "count is at least 1")
}

func TestBuildVarsSplitArgsAndSecrets(t *testing.T) {
	d := &deploy{
		p: agentprotocol.DeployPayload{BuildArgKeys: []string{"ARG"}, BuildSecretKeys: []string{"TOKEN"}},
		cmd: agentprotocol.Command{Secrets: map[string]string{
			"build:ARG": "v1", "build:TOKEN": "sekret-canary-12345", "env:RUNTIME": "r",
		}},
	}
	v := d.buildVars()
	require.Equal(t, map[string]string{"ARG": "v1"}, v.Args)
	require.Equal(t, map[string]string{"TOKEN": "sekret-canary-12345"}, v.Secrets)
	require.NotContains(t, v.DockerArgs(), "sekret-canary-12345")
	var _ build.Vars = v
}

func TestRedactorMasksBuildSecretsAndEncodedForms(t *testing.T) {
	secrets := map[string]string{
		"build:TOKEN": "p@ss/w:rd#12345", "build:ARG": "plain-build-arg", "env:PW": "p@ss/w:rd#12345", "env:PLAIN": "readable-value",
		"git_token": "ghp_abcdef123456",
	}
	red := newRedactor(secrets, []string{"PW", "build:TOKEN"})
	out := red.apply("pw=p@ss/w:rd#12345 url=p%40ss%2Fw%3Ard%2312345 b64=cEBzcy93OnJkIzEyMzQ1 arg=plain-build-arg env=readable-value git=ghp_abcdef123456")
	require.NotContains(t, out, "p@ss")
	require.NotContains(t, out, "p%40ss")
	require.NotContains(t, out, "cEBzcy93")
	require.NotContains(t, out, "ghp_abcdef")
	require.Contains(t, out, "plain-build-arg", "plain build args are not secret")
	require.Contains(t, out, "readable-value")
}
