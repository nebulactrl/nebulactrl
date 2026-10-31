package hardening

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDirectRunnerDoesNotPassTheAgentEnvironment(t *testing.T) {
	t.Setenv("NEBULACTRL_TOKEN", "enrolment-secret")
	t.Setenv("NEBULACTRL_URL", "https://control.example")
	out, err := DirectRunner{Env: []string{"NC_ROOT=/mnt"}}.Run(context.Background(),
		`printf '%s|%s|%s' "${NEBULACTRL_TOKEN-unset}" "${NEBULACTRL_URL-unset}" "$NC_ROOT"`)
	require.NoError(t, err)
	require.Equal(t, "unset|unset|/mnt", out.Stdout)
}
