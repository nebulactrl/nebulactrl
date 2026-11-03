//go:build integration

package integration

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// dataSets returns the result sets of a DataResult.
func dataSets(t *testing.T, r Resp) []J {
	t.Helper()
	arr, _ := r.JSON()["results"].([]any)
	out := make([]J, 0, len(arr))
	for _, v := range arr {
		m, _ := v.(map[string]any)
		out = append(out, J(m))
	}
	return out
}

// dataRows returns the rows of one result set: a cell is its text, or nil for NULL.
func dataRows(set J) [][]any {
	arr, _ := set["rows"].([]any)
	out := make([][]any, 0, len(arr))
	for _, v := range arr {
		row, _ := v.([]any)
		out = append(out, row)
	}
	return out
}

// The Data tab's operations against a real managed PostgreSQL, MySQL and
// ClickHouse: SQL runs read-only unless writes are allowed, rows come back as
// text with NULL kept apart, tables are listed and browsed, and every run is
// audited without its SQL.
func TestDatabaseData(t *testing.T) {
	requireTier(t, tierFull)
	t.Parallel()
	s := SharedStackN(t, 2) // environment + the internal network of the databases

	engines := []struct {
		name, engine, version string
		// create runs with writes allowed; the table t holds the rows 1 and NULL afterwards.
		create string
		// readOnlyErr is part of the error of a write in a read-only session.
		readOnlyErr string
		// tableSchema is the schema the table listing reports for t.
		tableSchema string
	}{
		{"pg", "postgres", "17", "CREATE TABLE t(a int); INSERT INTO t VALUES (1), (NULL);", "read-only transaction", "public"},
		{"my", "mysql", "8.4", "CREATE TABLE t(a int); INSERT INTO t VALUES (1), (NULL);", "READ ONLY", "app"},
		{"ch", "clickhouse", "25.8", "CREATE TABLE t(a Nullable(Int32)) ENGINE = Memory; INSERT INTO t VALUES (1), (NULL);", "readonly", "app"},
	}
	ids := map[string]Service{}
	for _, e := range engines {
		ids[e.name] = s.createDB(e.name, e.engine, e.version)
	}
	s.deployStaged()

	for _, e := range engines {
		svc := ids[e.name]
		base := "/services/" + svc.ID + "/database"

		// A write needs allowWrites (and the slug as confirmation in production).
		r := s.Do("POST", base+"/query", map[string]any{"sql": e.create, "allowWrites": true, "confirm": svc.Slug})
		require.Equal(t, http.StatusOK, r.Status, "%s: %s", e.name, r.Raw)

		// A read-only session returns the rows as text, NULL as null.
		r = s.Do("POST", base+"/query", map[string]any{"sql": "SELECT * FROM t ORDER BY a IS NULL, a"})
		require.Equal(t, http.StatusOK, r.Status, "%s: %s", e.name, r.Raw)
		sets := dataSets(t, r)
		require.Len(t, sets, 1, "%s: %s", e.name, r.Raw)
		require.Equal(t, [][]any{{"1"}, {nil}}, dataRows(sets[0]), e.name)

		// The same session refuses to change data.
		r = s.Do("POST", base+"/query", map[string]any{"sql": "INSERT INTO t VALUES (2)"})
		require.Equal(t, http.StatusUnprocessableEntity, r.Status, "%s: %s", e.name, r.Raw)
		require.Equal(t, "query_failed", r.JSON().Str("code"), "%s: %s", e.name, r.Raw)
		require.Contains(t, r.JSON().Str("detail")+r.JSON().Str("title"), e.readOnlyErr, "%s: %s", e.name, r.Raw)

		// Several statements give several result sets.
		r = s.Do("POST", base+"/query", map[string]any{"sql": "SELECT 1; SELECT 2"})
		require.Equal(t, http.StatusOK, r.Status, "%s: %s", e.name, r.Raw)
		require.Len(t, dataSets(t, r), 2, "%s: %s", e.name, r.Raw)

		// The table is listed and can be paged.
		r = s.Do("GET", base+"/tables", nil)
		require.Equal(t, http.StatusOK, r.Status, "%s: %s", e.name, r.Raw)
		var found bool
		for _, v := range r.JSON()["tables"].([]any) {
			tb := J(v.(map[string]any))
			if tb.Str("name") == "t" {
				found = true
				require.Equal(t, e.tableSchema, tb.Str("schema"), e.name)
				require.Equal(t, "table", tb.Str("kind"), e.name)
			}
		}
		require.True(t, found, "%s: table t is not listed: %s", e.name, r.Raw)

		r = s.Do("POST", base+"/rows", map[string]any{"schema": e.tableSchema, "table": "t", "offset": 0})
		require.Equal(t, http.StatusOK, r.Status, "%s: %s", e.name, r.Raw)
		sets = dataSets(t, r)
		require.Len(t, sets, 1, "%s: %s", e.name, r.Raw)
		require.Len(t, dataRows(sets[0]), 2, e.name)
		r = s.Do("POST", base+"/rows", map[string]any{"schema": e.tableSchema, "table": "t", "offset": 1})
		require.Equal(t, http.StatusOK, r.Status, "%s: %s", e.name, r.Raw)
		require.Len(t, dataRows(dataSets(t, r)[0]), 1, "%s: offset 1 skips a row", e.name)

		// The audit log names the run and its first keyword, never the SQL.
		var queries, browses int
		require.NoError(t, s.Pool.QueryRow(t.Context(),
			"SELECT count(*) FROM audit_events WHERE action = 'database.query' AND resource_id = $1", svc.ID).Scan(&queries))
		require.NoError(t, s.Pool.QueryRow(t.Context(),
			"SELECT count(*) FROM audit_events WHERE action = 'database.browse' AND resource_id = $1", svc.ID).Scan(&browses))
		require.Equal(t, 4, queries, "%s: create, select, refused insert, two selects", e.name)
		require.Equal(t, 2, browses, e.name)
		var meta string
		require.NoError(t, s.Pool.QueryRow(t.Context(),
			"SELECT string_agg(metadata::text, ' ') FROM audit_events WHERE action = 'database.query' AND resource_id = $1", svc.ID).Scan(&meta))
		require.Contains(t, meta, `"verb": "CREATE"`, e.name)
		require.False(t, strings.Contains(meta, "INSERT INTO"), "%s: the SQL stays out of the audit log: %s", e.name, meta)
	}

	// A token without deployments:write reads no rows.
	ro := s.Token([]string{"projects:read", "servers:read"}, nil)
	r := s.DoToken(ro, "POST", "/services/"+ids["pg"].ID+"/database/query", map[string]any{"sql": "SELECT 1"})
	require.Equal(t, http.StatusForbidden, r.Status, string(r.Raw))

	// An engine without a Data tab says so.
	kv := s.createDB("cache", "valkey", "9")
	s.deployStaged(kv.ID)
	r = s.Do("GET", "/services/"+kv.ID+"/database/tables", nil)
	require.Equal(t, http.StatusConflict, r.Status, string(r.Raw))
	require.Equal(t, "data_not_supported", r.JSON().Str("code"), string(r.Raw))
}
