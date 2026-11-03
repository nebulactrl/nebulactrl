package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/apiclient"
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

func TestDatabasesQueryReadsSQLFromArgsFileOrStdinAndAliasDB(t *testing.T) {
	e := newEnv(t)
	e.login()
	e.mustRun("", "projects", "create", "Acme")
	e.mustRun("", "databases", "create", "--project", "acme", "--env", "production", "--engine", "postgres", "--version", "17", "--name", "main")

	// The db alias reaches the same commands; a database that was never
	// deployed has no agent to ask.
	for _, args := range [][]string{
		{"db", "tables", "acme/production/main"},
		{"db", "query", "acme/production/main", "--", "SELECT", "1"},
		{"databases", "query", "acme/production/main"},
	} {
		in := ""
		if len(args) == 3 && args[1] == "query" {
			in = "SELECT 1"
		}
		r := e.run(in, args...)
		require.NotEqual(t, 0, r.code, args)
		require.Contains(t, r.all(), "not_deployed", args)
	}

	// Giving no SQL, or SQL twice, is a usage error before any request.
	r := e.run("", "databases", "query", "acme/production/main")
	require.NotEqual(t, 0, r.code)
	require.Contains(t, r.all(), "no SQL given")
	r = e.run("", "databases", "query", "acme/production/main", "--file", "x.sql", "--", "SELECT 1")
	require.Contains(t, r.all(), "not both")
	r = e.run("", "databases", "query", "acme/production/main", "--confirm", "main", "--", "SELECT 1")
	require.Contains(t, r.all(), "--confirm only applies with --allow-writes")
}

func TestPrintDataResultFormatsTablesNullsAndFooter(t *testing.T) {
	str := func(s string) *string { return &s }
	row1 := []*string{str("1"), str("a\tb\nc")}
	row2 := []*string{nil, str("")}
	allRows := []*[]*string{&row1, &row2}
	cols := []apiclient.DataColumn{{Name: "id", Type: "int4"}, {Name: "note", Type: "text"}}
	tag := "INSERT 0 1"
	yes := true
	set1 := apiclient.DataResultSet{Columns: &cols, Rows: &allRows, RowCount: 2, Truncated: &yes}
	noCols := []apiclient.DataColumn{}
	set2 := apiclient.DataResultSet{Columns: &noCols, CommandTag: &tag}
	results := []apiclient.DataResultSet{set1, set2}

	var out bytes.Buffer
	printDataResult(&out, &apiclient.DataResult{Results: &results, DurationMs: 12})
	got := out.String()
	require.Regexp(t, `id\s+note`, got)
	require.Regexp(t, `1\s+a\\tb\\nc`, got)
	require.Regexp(t, `NULL\s*\n`, got, "NULL is printed as NULL")
	require.Contains(t, got, "INSERT 0 1")
	require.True(t, strings.HasSuffix(got, "2 rows · 12ms (truncated)\n"), got)

	require.Equal(t, [][]string{{"public", "t", "table", "42"}, {"public", "v", "view", "-"}},
		tableRows([]apiclient.DataTable{
			{Schema: "public", Name: "t", Kind: apiclient.DataTableKind("table"), RowEstimate: new(int64(42))},
			{Schema: "public", Name: "v", Kind: apiclient.DataTableKind("view")},
		}))
}
