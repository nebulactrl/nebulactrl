package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestVarsGeneratedRotateHistoryAndShared(t *testing.T) {
	e := newEnv(t)
	e.login()
	svcID := e.seed()
	stored := func() (string, int) {
		var enc []byte
		var ver int
		require.NoError(t, e.pool.QueryRow(context.Background(),
			"SELECT value_enc, version FROM service_variables WHERE service_id = $1 AND key = 'TOKEN'", svcID).Scan(&enc, &ver))
		return string(enc), ver
	}

	// A generator runs once: running the same command again keeps the value.
	e.mustRun("", "vars", "set", svcID, "TOKEN=${{secret(32)}}")
	first, v1 := stored()
	r := e.mustRun("", "--json", "vars", "set", svcID, "TOKEN=${{secret(32)}}")
	again, v2 := stored()
	require.Equal(t, first, again)
	require.Equal(t, v1, v2)
	require.Contains(t, r.stdout, `"generated"`)
	require.NotContains(t, r.all(), "${{secret(32)}}=", "no value echo")

	// Rotation: dry-run changes nothing, the real run stages a new version.
	r = e.mustRun("", "vars", "rotate", svcID, "TOKEN", "--dry-run")
	require.Contains(t, r.all(), "Dry run")
	unchanged, _ := stored()
	require.Equal(t, first, unchanged)
	e.mustRun("", "vars", "rotate", svcID, "TOKEN")
	rotated, v3 := stored()
	require.NotEqual(t, first, rotated)
	require.Equal(t, v2+1, v3)

	r = e.mustRun("", "vars", "history", svcID, "TOKEN")
	require.Contains(t, r.stdout, "rotate")
	require.Contains(t, r.stdout, secretMask)

	// Build scope is rejected for image services with a clear error.
	r = e.run("", "vars", "set", svcID, "ARG=1", "--scope", "build")
	require.NotEqual(t, 0, r.code)
	require.Contains(t, r.all(), "git source")

	// A non-generated variable cannot be rotated.
	e.mustRun("", "vars", "set", svcID, "PLAIN=x")
	r = e.run("", "vars", "rotate", svcID, "PLAIN")
	require.NotEqual(t, 0, r.code)
}

// `@team` addresses the team layer: set, list, share (as ${{team.KEY}}) and
// delete, with a secret never echoed.
func TestVarsSharedTeamLayer(t *testing.T) {
	e := newEnv(t)
	e.login()
	svcID := e.seed()

	e.mustRun("", "vars", "shared", "set", "@team", "REGION=eu-west")
	e.mustRun("", "vars", "shared", "set", "@team", "TEAM_SECRET=team-secret-123456", "--secret")
	r := e.mustRun("", "vars", "shared", "ls", "@team")
	require.Contains(t, r.stdout, "REGION")
	require.Contains(t, r.stdout, "eu-west")
	require.Contains(t, r.stdout, "team")
	require.Contains(t, r.stdout, secretMask)
	require.NotContains(t, r.all(), "team-secret-123456")

	r = e.mustRun("", "vars", "shared", "share", "@team", "REGION", svcID)
	require.Contains(t, r.all(), "REGION=${{team.REGION}}")
	var enc []byte
	require.NoError(t, e.pool.QueryRow(context.Background(),
		"SELECT value_enc FROM service_variables WHERE service_id = $1 AND key = 'REGION'", svcID).Scan(&enc))
	require.NotEmpty(t, enc)

	e.mustRun("", "vars", "shared", "rm", "@team", "REGION", "TEAM_SECRET")
	r = e.mustRun("", "--json", "vars", "shared", "ls", "@team")
	require.NotContains(t, r.stdout, "REGION")
}
