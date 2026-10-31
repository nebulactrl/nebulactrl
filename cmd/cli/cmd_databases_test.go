package main

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDatabasesCreateInfoRevealRotateDryRun(t *testing.T) {
	e := newEnv(t)
	e.login()
	e.mustRun("", "projects", "create", "Acme")

	// The catalog lists Valkey as the default key-value engine and flags licences.
	r := e.mustRun("", "databases", "engines")
	require.Contains(t, r.stdout, "valkey")
	require.Contains(t, r.stdout, "postgres")
	require.Regexp(t, `mongodb .*source-available: --accept-license`, r.stdout)

	// Source-available engines need an explicit acknowledgement.
	bad := e.run("", "databases", "create", "--project", "acme", "--env", "production", "--engine", "mongodb")
	require.NotEqual(t, 0, bad.code)
	require.Contains(t, bad.all(), "license_not_accepted")

	r = e.mustRun("", "--json", "databases", "create", "--project", "acme", "--env", "production",
		"--engine", "postgres", "--version", "17", "--name", "main")
	created := decode[map[string]any](t, r.stdout)
	require.Equal(t, "database", created["kind"])
	require.Equal(t, "main", created["slug"])

	r = e.mustRun("", "--json", "databases", "info", "acme/production/main")
	info := decode[map[string]any](t, r.stdout)
	require.Equal(t, "postgres:17", info["image"])
	require.Contains(t, r.stdout, "${{main.DATABASE_URL}}")
	require.NotContains(t, r.stdout, "postgres://", "info lists export names, never values")

	// Reveal prints the connection string (and is audited).
	r = e.mustRun("", "databases", "reveal", "acme/production/main")
	require.True(t, strings.HasPrefix(strings.TrimSpace(r.stdout), "postgres://postgres:"), r.stdout)
	var audited int
	require.NoError(t, e.pool.QueryRow(t.Context(), "SELECT count(*) FROM audit_events WHERE action = 'secret.reveal'").Scan(&audited))
	require.Equal(t, 1, audited)

	// A dry-run rotation changes nothing.
	r = e.mustRun("", "databases", "rotate", "acme/production/main", "--dry-run")
	require.Contains(t, r.stdout, "Dry run only")
	again := e.mustRun("", "databases", "reveal", "acme/production/main")
	require.True(t, strings.HasPrefix(strings.TrimSpace(again.stdout), "postgres://postgres:"))
}

func TestDatabasesUpgradePlanDoesNotApplyWithoutYes(t *testing.T) {
	e := newEnv(t)
	e.login()
	e.mustRun("", "projects", "create", "Acme")
	e.mustRun("", "databases", "create", "--project", "acme", "--env", "production", "--engine", "postgres", "--version", "17", "--name", "main")
	r := e.mustRun("", "databases", "upgrade", "acme/production/main", "18")
	require.Contains(t, r.all(), "Run again with --yes")
	r = e.mustRun("", "--json", "databases", "info", "acme/production/main")
	require.Regexp(t, `"version":\s*"17"`, r.stdout)
}
