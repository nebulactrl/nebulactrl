package engine

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

func dbQueryCommand(t *testing.T, p agentprotocol.DBQueryPayload, secrets map[string]string) agentprotocol.Command {
	t.Helper()
	raw, err := json.Marshal(p)
	require.NoError(t, err)
	return agentprotocol.Command{ID: "cmd-q", Kind: agentprotocol.KindDBQuery, Payload: raw, Secrets: secrets}
}

func TestDBQueryKindIsAllowedAndEphemeral(t *testing.T) {
	require.True(t, agentprotocol.AllowedKinds[agentprotocol.KindDBQuery])
	require.True(t, agentprotocol.EphemeralResultKinds[agentprotocol.KindDBQuery])
}

func TestBrowseSQLQuotesIdentifiersPerEngine(t *testing.T) {
	for _, c := range []struct {
		engine, schema, table, want string
	}{
		{"postgres", "public", "users", `SELECT * FROM "public"."users" LIMIT 101 OFFSET 200`},
		{"postgres", "My Schema", `we"ird`, `SELECT * FROM "My Schema"."we""ird" LIMIT 101 OFFSET 200`},
		{"mysql", "appdb", "users", "SELECT * FROM `appdb`.`users` LIMIT 101 OFFSET 200"},
		{"mariadb", "", "we`ird", "SELECT * FROM `we``ird` LIMIT 101 OFFSET 200"},
		{"clickhouse", "appdb", "we`ird\\", "SELECT * FROM `appdb`.`we\\`ird\\\\` LIMIT 101 OFFSET 200"},
	} {
		got, err := browseSQL(c.engine, c.schema, c.table, 101, 200)
		require.NoError(t, err, c.engine)
		require.Equal(t, c.want, got, c.engine)
	}
	_, err := browseSQL("postgres", "public", "", 101, 0)
	require.Error(t, err)
}

func TestPlanDBQueryAppliesDefaultsAndBounds(t *testing.T) {
	run, err := planDBQuery(agentprotocol.DBQueryPayload{Engine: "postgres", Op: agentprotocol.DBOpSQL, SQL: "SELECT 1", AllowWrites: true, TimeoutSeconds: 9999, MaxRows: 99999})
	require.NoError(t, err)
	require.Equal(t, 120*time.Second, run.timeout)
	require.Equal(t, dbQueryMaxRows, run.maxRows)
	require.True(t, run.allowWrites)

	run, err = planDBQuery(agentprotocol.DBQueryPayload{Engine: "postgres", Op: agentprotocol.DBOpSQL, SQL: "SELECT 1"})
	require.NoError(t, err)
	require.Equal(t, 30*time.Second, run.timeout)
	require.Equal(t, dbQueryDefaultRows, run.maxRows)
	require.False(t, run.allowWrites)

	// Browse reads one row past the page to learn whether another page exists,
	// and never writes, whatever the payload says.
	run, err = planDBQuery(agentprotocol.DBQueryPayload{Engine: "mysql", Op: agentprotocol.DBOpBrowse, Schema: "app", Table: "t", Offset: 100, MaxRows: 100, AllowWrites: true})
	require.NoError(t, err)
	require.Contains(t, run.sql, "LIMIT 101 OFFSET 100")
	require.False(t, run.allowWrites)
	require.Equal(t, 100, run.maxRows)

	run, err = planDBQuery(agentprotocol.DBQueryPayload{Engine: "postgres", Op: agentprotocol.DBOpTables, AllowWrites: true})
	require.NoError(t, err)
	require.False(t, run.allowWrites)
	require.Equal(t, dbQueryTableLimit, run.maxRows)

	for _, bad := range []agentprotocol.DBQueryPayload{
		{Engine: "postgres", Op: agentprotocol.DBOpSQL, SQL: "  "},
		{Engine: "postgres", Op: agentprotocol.DBOpBrowse, Table: "t", Offset: -1},
		{Engine: "postgres", Op: agentprotocol.DBOpBrowse},
		{Engine: "postgres", Op: "drop"},
		{Engine: "postgres", Op: agentprotocol.DBOpSQL, SQL: strings.Repeat("x", dbQueryMaxSQL+1)},
	} {
		_, err := planDBQuery(bad)
		require.Error(t, err, "%+v", bad)
	}
}

func TestResultBuilderKeepsMaxRowsAndMarksTruncation(t *testing.T) {
	b := newDBResultBuilder(2)
	require.True(t, b.beginSet([]agentprotocol.DBColumn{{Name: "a", Type: "int"}}))
	require.True(t, b.addRow([][]byte{[]byte("1")}))
	require.True(t, b.addRow([][]byte{nil}))
	require.False(t, b.addRow([][]byte{[]byte("3")}), "the third row is over the limit")
	require.False(t, b.addRow([][]byte{[]byte("4")}))
	b.endSet("SELECT 3")

	res := b.result()
	require.Len(t, res.Results, 1)
	set := res.Results[0]
	require.Equal(t, 2, set.RowCount)
	require.True(t, set.Truncated)
	require.Equal(t, "SELECT 3", set.CommandTag)
	require.Equal(t, [][]*string{{new("1")}, {nil}}, set.Rows, "NULL stays nil, never an empty string")
}

func TestResultBuilderDistinguishesEmptyStringFromNull(t *testing.T) {
	b := newDBResultBuilder(10)
	b.beginSet(nil)
	b.addRow([][]byte{{}, nil})
	set := b.result().Results[0]
	require.Equal(t, []*string{new(""), nil}, set.Rows[0])
	require.False(t, set.Truncated)
}

func TestResultBuilderClipsLongCellsAtACharacterBoundary(t *testing.T) {
	long := []byte(strings.Repeat("é", dbQueryMaxCellBytes)) // 2 bytes each: the cut falls inside a rune boundary-wise
	b := newDBResultBuilder(10)
	b.beginSet(nil)
	b.addRow([][]byte{long, []byte("short")})
	res := b.result()
	require.True(t, res.TruncatedCells)
	got := *res.Results[0].Rows[0][0]
	require.True(t, strings.HasSuffix(got, "…"))
	require.LessOrEqual(t, len(got), dbQueryMaxCellBytes+len("…"))
	require.True(t, strings.HasPrefix(got, "éé"))
	require.NotContains(t, strings.TrimSuffix(got, "…"), "\ufffd", "no half character")
	require.Equal(t, "short", *res.Results[0].Rows[0][1])

	exact := []byte(strings.Repeat("a", dbQueryMaxCellBytes))
	s, clipped := clipCell(exact)
	require.False(t, clipped)
	require.Equal(t, string(exact), s)
}

func TestResultBuilderStopsAtTheByteLimit(t *testing.T) {
	b := newDBResultBuilder(dbQueryMaxRows)
	b.beginSet(nil)
	cellBytes := []byte(strings.Repeat("x", dbQueryMaxCellBytes))
	kept := 0
	for b.addRow([][]byte{cellBytes}) {
		kept++
		require.Less(t, kept, dbQueryMaxRows, "the byte limit must stop the set first")
	}
	set := b.result().Results[0]
	require.True(t, set.Truncated)
	require.Equal(t, kept, set.RowCount)
	require.LessOrEqual(t, kept*(dbQueryMaxCellBytes+8), dbQueryMaxBytes)
	require.Greater(t, kept, 0)
}

func TestResultBuilderCapsTheNumberOfSets(t *testing.T) {
	b := newDBResultBuilder(10)
	for i := range dbQueryMaxSets {
		require.True(t, b.beginSet(nil), "set %d", i)
		b.endSet("")
	}
	require.False(t, b.beginSet(nil), "the eleventh set is dropped")
	require.False(t, b.addRow([][]byte{[]byte("1")}), "rows of a dropped set are discarded")
	require.Len(t, b.result().Results, dbQueryMaxSets)
}

func TestDecodeClickHouseResultsReadsConcatenatedDocuments(t *testing.T) {
	out := `{
	"meta": [ {"name": "a", "type": "UInt8"}, {"name": "b", "type": "Nullable(String)"} ],
	"data": [ [1, "x"], [2, null], [3, ""] ],
	"rows": 3,
	"statistics": {"elapsed": 0.001}
}
{"meta":[{"name":"n","type":"UInt64"},{"name":"arr","type":"Array(Int32)"},{"name":"big","type":"UInt64"}],"data":[[5,[1,2],"18446744073709551615"]],"rows":1}
`
	b := newDBResultBuilder(10)
	require.NoError(t, decodeClickHouseResults(out, b))
	res := b.result()
	require.Len(t, res.Results, 2)
	require.Equal(t, []agentprotocol.DBColumn{{Name: "a", Type: "UInt8"}, {Name: "b", Type: "Nullable(String)"}}, res.Results[0].Columns)
	require.Equal(t, [][]*string{{new("1"), new("x")}, {new("2"), nil}, {new("3"), new("")}}, res.Results[0].Rows)
	require.Equal(t, [][]*string{{new("5"), new("[1,2]"), new("18446744073709551615")}}, res.Results[1].Rows)
}

func TestDecodeClickHouseResultsTruncatesAndRejectsGarbage(t *testing.T) {
	out := `{"meta":[{"name":"n","type":"UInt8"}],"data":[[1],[2],[3]]}`
	b := newDBResultBuilder(2)
	require.NoError(t, decodeClickHouseResults(out, b))
	set := b.result().Results[0]
	require.True(t, set.Truncated)
	require.Equal(t, 2, set.RowCount)

	require.Error(t, decodeClickHouseResults(`{"meta":[`, newDBResultBuilder(2)))
	require.NoError(t, decodeClickHouseResults("", newDBResultBuilder(2)), "a statement batch without output is fine")
}

func TestTablesFromSet(t *testing.T) {
	set := &agentprotocol.DBResultSet{Rows: [][]*string{
		{new("public"), new("users"), new("table"), new("42")},
		{new("public"), new("v"), new("view"), nil},
		{new("s"), new("odd"), new("other"), new("-1")},
	}}
	got := tablesFromSet(set)
	require.Len(t, got, 3)
	require.Equal(t, "users", got[0].Name)
	require.EqualValues(t, 42, *got[0].RowEstimate)
	require.Nil(t, got[1].RowEstimate)
	require.Nil(t, got[2].RowEstimate, "a negative estimate means unknown")
	require.Nil(t, tablesFromSet(nil))
}

func TestMySQLSessionStatementsPerEngine(t *testing.T) {
	ro := dbRun{timeout: 30 * time.Second}
	require.Equal(t, []string{"SET SESSION TRANSACTION READ ONLY", "SET SESSION max_execution_time=30000"}, mysqlSessionSQL("mysql", ro))
	require.Equal(t, []string{"SET SESSION TRANSACTION READ ONLY", "SET SESSION max_statement_time=30.000"}, mysqlSessionSQL("mariadb", ro))
	rw := dbRun{timeout: 5 * time.Second, allowWrites: true}
	require.Equal(t, []string{"SET SESSION max_execution_time=5000"}, mysqlSessionSQL("mysql", rw))
}

func TestDBQueryNetworksSkipTheEdgeNetwork(t *testing.T) {
	info := docker.ContainerInfo{Networks: map[string]docker.ContainerNetwork{
		"nebulactrl-env-b": {IP: "10.0.0.2"}, agentprotocol.EdgeNetwork: {IP: "10.9.0.2"}, "nebulactrl-env-a": {IP: "10.0.1.2"},
	}}
	require.Equal(t, []string{"nebulactrl-env-a", "nebulactrl-env-b"}, dbQueryNetworks(info))
}

func TestDBQueryFailsWithoutARunningDatabase(t *testing.T) {
	r := newRig(t, nil)
	r.dk.seed("nc-pg-d1d1d1d1-0", svcID, depV1, false, time.Hour)
	res := r.eng.HandleCommand(context.Background(), dbQueryCommand(t, agentprotocol.DBQueryPayload{
		ServiceID: svcID, Engine: "postgres", Database: "app", Op: agentprotocol.DBOpTables,
	}, map[string]string{agentprotocol.SecretDBUser: "app", agentprotocol.SecretDBPassword: "secretsecret12345"}))
	require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome)
	require.Equal(t, "the database is not running", res.Error)
}

func TestDBQueryRejectsUnsupportedEnginesAndMissingCredentials(t *testing.T) {
	r := newRig(t, nil)
	r.dk.seed("nc-pg-d1d1d1d1-0", svcID, depV1, true, time.Hour)
	creds := map[string]string{agentprotocol.SecretDBUser: "app", agentprotocol.SecretDBPassword: "secretsecret12345"}
	for _, c := range []struct {
		p       agentprotocol.DBQueryPayload
		secrets map[string]string
		want    string
	}{
		{agentprotocol.DBQueryPayload{ServiceID: svcID, Engine: "valkey", Op: agentprotocol.DBOpTables}, creds, "does not support engine"},
		{agentprotocol.DBQueryPayload{ServiceID: svcID, Engine: "mongodb", Op: agentprotocol.DBOpTables}, creds, "does not support engine"},
		{agentprotocol.DBQueryPayload{Engine: "postgres", Op: agentprotocol.DBOpTables}, creds, "serviceId is required"},
		{agentprotocol.DBQueryPayload{ServiceID: svcID, Engine: "postgres", Op: agentprotocol.DBOpTables}, nil, "credentials are missing"},
		{agentprotocol.DBQueryPayload{ServiceID: svcID, Engine: "postgres", Op: agentprotocol.DBOpSQL}, creds, "sql is required"},
	} {
		res := r.eng.HandleCommand(context.Background(), dbQueryCommand(t, c.p, c.secrets))
		require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome, c.want)
		require.Contains(t, res.Error, c.want)
	}
}

func TestDBQueryClickHouseRunsClientInTheContainerReadOnly(t *testing.T) {
	r := newRig(t, nil)
	ctr := r.dk.seed("nc-ch-d1d1d1d1-0", svcID, depV1, true, time.Hour)
	var call execCall
	r.dk.onExec = func(id string, argv []string, spec docker.ExecSpec, stdout, _ io.Writer) (int, error) {
		b, _ := io.ReadAll(spec.Stdin)
		call = execCall{id: id, argv: argv, env: spec.Env, stdin: string(b)}
		_, _ = stdout.Write([]byte(`{"meta":[{"name":"a","type":"UInt8"}],"data":[[1],[null]],"rows":2}` + "\n"))
		return 0, nil
	}
	const pw = "secretsecret12345"
	res := r.eng.HandleCommand(context.Background(), dbQueryCommand(t, agentprotocol.DBQueryPayload{
		ServiceID: svcID, Engine: "clickhouse", Database: "appdb", Op: agentprotocol.DBOpSQL, SQL: "SELECT a FROM t", TimeoutSeconds: 30, MaxRows: 1000,
	}, map[string]string{agentprotocol.SecretDBUser: "app", agentprotocol.SecretDBPassword: pw}))
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, res.Error)

	require.Equal(t, ctr.ID, call.id)
	require.Equal(t, "clickhouse-client", call.argv[0])
	require.Contains(t, call.argv, "--readonly=1")
	require.Contains(t, call.argv, "--multiquery")
	require.Contains(t, call.argv, "--max_execution_time=30")
	require.Contains(t, call.argv, "--max_result_rows=1001")
	require.NotContains(t, strings.Join(call.argv, " "), pw, "the password stays out of argv")
	require.Contains(t, call.env, "CLICKHOUSE_PASSWORD="+pw)
	require.Equal(t, "SELECT a FROM t", call.stdin)

	var out agentprotocol.DBQueryResult
	require.NoError(t, json.Unmarshal(res.Data, &out))
	require.Len(t, out.Results, 1)
	require.Equal(t, [][]*string{{new("1")}, {nil}}, out.Results[0].Rows)
}

func TestDBQueryClickHouseAllowWritesDropsReadonly(t *testing.T) {
	r := newRig(t, nil)
	r.dk.seed("nc-ch-d1d1d1d1-0", svcID, depV1, true, time.Hour)
	var argv []string
	r.dk.onExec = func(_ string, a []string, _ docker.ExecSpec, _, _ io.Writer) (int, error) {
		argv = a
		return 0, nil
	}
	res := r.eng.HandleCommand(context.Background(), dbQueryCommand(t, agentprotocol.DBQueryPayload{
		ServiceID: svcID, Engine: "clickhouse", Database: "appdb", Op: agentprotocol.DBOpSQL, SQL: "INSERT INTO t VALUES (1)", AllowWrites: true,
	}, map[string]string{agentprotocol.SecretDBUser: "app", agentprotocol.SecretDBPassword: "secretsecret12345"}))
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, res.Error)
	require.NotContains(t, argv, "--readonly=1")
}

func TestDBQueryClickHouseTablesAndRedactedErrors(t *testing.T) {
	r := newRig(t, nil)
	r.dk.seed("nc-ch-d1d1d1d1-0", svcID, depV1, true, time.Hour)
	const pw = "secretsecret12345"
	creds := map[string]string{agentprotocol.SecretDBUser: "app", agentprotocol.SecretDBPassword: pw}
	r.dk.onExec = func(_ string, _ []string, _ docker.ExecSpec, stdout, _ io.Writer) (int, error) {
		_, _ = stdout.Write([]byte(`{"meta":[],"data":[["appdb","t","table",2],["appdb","v","view",null]]}`))
		return 0, nil
	}
	res := r.eng.HandleCommand(context.Background(), dbQueryCommand(t, agentprotocol.DBQueryPayload{
		ServiceID: svcID, Engine: "clickhouse", Database: "appdb", Op: agentprotocol.DBOpTables,
	}, creds))
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, res.Error)
	var out agentprotocol.DBQueryResult
	require.NoError(t, json.Unmarshal(res.Data, &out))
	require.Empty(t, out.Results, "the table listing is not a result set")
	require.Len(t, out.Tables, 2)
	require.Equal(t, "view", out.Tables[1].Kind)
	require.EqualValues(t, 2, *out.Tables[0].RowEstimate)

	r.dk.onExec = func(_ string, _ []string, _ docker.ExecSpec, _, stderr io.Writer) (int, error) {
		_, _ = stderr.Write([]byte("Code: 516. password " + pw + " rejected: Cannot execute query in readonly mode"))
		return 164, nil
	}
	res = r.eng.HandleCommand(context.Background(), dbQueryCommand(t, agentprotocol.DBQueryPayload{
		ServiceID: svcID, Engine: "clickhouse", Database: "appdb", Op: agentprotocol.DBOpSQL, SQL: "INSERT INTO t VALUES (1)",
	}, creds))
	require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome)
	require.Contains(t, res.Error, "readonly")
	require.NotContains(t, res.Error, pw)
}

func TestDBQueryClickHouseOversizedOutputFails(t *testing.T) {
	r := newRig(t, nil)
	r.dk.seed("nc-ch-d1d1d1d1-0", svcID, depV1, true, time.Hour)
	r.dk.onExec = func(_ string, _ []string, _ docker.ExecSpec, stdout, _ io.Writer) (int, error) {
		_, _ = stdout.Write([]byte(`{"meta":[{"name":"a","type":"String"}],"data":[["`))
		_, _ = stdout.Write([]byte(strings.Repeat("x", clickHouseOutputLimit+1)))
		return 0, nil
	}
	res := r.eng.HandleCommand(context.Background(), dbQueryCommand(t, agentprotocol.DBQueryPayload{
		ServiceID: svcID, Engine: "clickhouse", Database: "appdb", Op: agentprotocol.DBOpSQL, SQL: "SELECT a FROM t",
	}, map[string]string{agentprotocol.SecretDBUser: "app", agentprotocol.SecretDBPassword: "secretsecret12345"}))
	require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome)
	require.Contains(t, res.Error, "too large")
}
