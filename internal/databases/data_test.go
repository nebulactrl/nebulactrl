package databases_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/databases"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/prodguard"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

type dataCall struct {
	serverID uuid.UUID
	kind     string
	payload  agentprotocol.DBQueryPayload
	secrets  map[string]string
	timeout  time.Duration
}

// fakeRequester answers db_query like an agent would.
type fakeRequester struct {
	calls []dataCall
	reply func(p agentprotocol.DBQueryPayload) (agentprotocol.ResultFrame, error)
}

func (r *fakeRequester) Request(_ context.Context, serverID uuid.UUID, kind string, payload any, secrets map[string]string, timeout time.Duration) (agentprotocol.ResultFrame, error) {
	raw, _ := json.Marshal(payload)
	var p agentprotocol.DBQueryPayload
	_ = json.Unmarshal(raw, &p)
	r.calls = append(r.calls, dataCall{serverID: serverID, kind: kind, payload: p, secrets: secrets, timeout: timeout})
	if r.reply != nil {
		return r.reply(p)
	}
	return dataOK(agentprotocol.DBQueryResult{}), nil
}

func dataOK(res agentprotocol.DBQueryResult) agentprotocol.ResultFrame {
	raw, _ := json.Marshal(res)
	return agentprotocol.ResultFrame{Outcome: agentprotocol.OutcomeSucceeded, Data: raw}
}

func (f *fixture) withRequester(r *fakeRequester) { f.dbs.SetRequester(r) }

func (f *fixture) setServerFacts(caps ...string) {
	f.t.Helper()
	facts, _ := json.Marshal(agentprotocol.Facts{OS: "linux", Capabilities: caps})
	_, err := f.pool.Exec(context.Background(), "UPDATE servers SET facts = $2 WHERE id = $1", f.server.ID, facts)
	require.NoError(f.t, err)
}

func httpStatus(t *testing.T, err error) int {
	t.Helper()
	e, ok := apierr.As(err)
	require.True(t, ok, "not an apierr: %v", err)
	return e.Status
}

func (f *fixture) auditMeta(action string) map[string]any {
	f.t.Helper()
	var raw []byte
	require.NoError(f.t, f.pool.QueryRow(context.Background(),
		"SELECT metadata FROM audit_events WHERE action = $1 ORDER BY created_at DESC LIMIT 1", action).Scan(&raw))
	var m map[string]any
	require.NoError(f.t, json.Unmarshal(raw, &m))
	return m
}

func TestSQLVerbIsTheFirstKeyword(t *testing.T) {
	for in, want := range map[string]string{
		"select 1":                               "SELECT",
		"  \n INSERT INTO t VALUES (1)":          "INSERT",
		"-- note\nupdate t set a = 1":            "UPDATE",
		"/* hi */ /* again */ DELETE FROM t":     "DELETE",
		"(SELECT 1) UNION (SELECT 2)":            "SELECT",
		";;WITH x AS (SELECT 1) SELECT * FROM x": "WITH",
		"# mysql comment\nshow tables":           "SHOW",
		"":                                       "OTHER",
		"-- only a comment":                      "OTHER",
		"/* unterminated":                        "OTHER",
		"123":                                    "OTHER",
		"EXPLAINANALYZEVERYLONGKEYWORDHERE":      "EXPLAINANALYZEVE",
	} {
		require.Equal(t, want, databases.SQLVerb(in), in)
	}
}

func TestDataOperationsRefuseWhatTheTabDoesNotServe(t *testing.T) {
	f := newFixture(t)
	f.withRequester(&fakeRequester{})
	vk := f.createDB("cache", "valkey", "9")
	f.deploy(vk.ID)
	_, err := f.dbs.ListTables(f.person(), vk.ID)
	require.Equal(t, databases.CodeDataNotSupported, apiCode(t, err))
	require.Equal(t, http.StatusConflict, httpStatus(t, err))

	app := f.app("web")
	_, err = f.dbs.ListTables(f.person(), app.ID)
	require.Equal(t, "not_a_managed_database", apiCode(t, err))
}

func TestDataPreflightCodes(t *testing.T) {
	f := newFixture(t)
	rq := &fakeRequester{}
	f.withRequester(rq)
	pg := f.createDB("pg", "postgres", "17")
	person := f.person()

	_, err := f.dbs.ListTables(person, pg.ID)
	require.Equal(t, databases.CodeNotDeployed, apiCode(t, err), "never deployed")

	f.deploy(pg.ID)
	_, err = f.pool.Exec(context.Background(), "UPDATE services SET desired_state = 'stopped' WHERE id = $1", pg.ID)
	require.NoError(t, err)
	_, err = f.dbs.ListTables(person, pg.ID)
	require.Equal(t, databases.CodeServiceStopped, apiCode(t, err))
	_, err = f.pool.Exec(context.Background(), "UPDATE services SET desired_state = 'running' WHERE id = $1", pg.ID)
	require.NoError(t, err)

	_, err = f.pool.Exec(context.Background(), "INSERT INTO database_fences (service_id, reason) VALUES ($1, 'test')", pg.ID)
	require.NoError(t, err)
	_, err = f.dbs.ListTables(person, pg.ID)
	require.Equal(t, databases.CodeDatabaseFenced, apiCode(t, err))
	_, err = f.pool.Exec(context.Background(), "DELETE FROM database_fences WHERE service_id = $1", pg.ID)
	require.NoError(t, err)

	f.setServerFacts(agentprotocol.CapProcesses)
	_, err = f.dbs.ListTables(person, pg.ID)
	require.Equal(t, databases.CodeAgentTooOld, apiCode(t, err))
	require.Contains(t, err.Error(), "Update the agent on agent1 to use the Data tab.")
	require.Empty(t, rq.calls, "nothing reached the agent")

	f.setServerFacts(agentprotocol.CapDBQuery)
	_, err = f.dbs.ListTables(person, pg.ID)
	require.NoError(t, err)
	require.Len(t, rq.calls, 1)
}

func TestDataNeedsTheDeployRoleAndScope(t *testing.T) {
	f := newFixture(t)
	f.withRequester(&fakeRequester{})
	pg := f.createDB("pg", "postgres", "17")
	f.deploy(pg.ID)

	viewer := auth.WithPrincipal(context.Background(), &auth.Principal{
		Type: auth.TypeUser, UserID: f.userID, WorkspaceID: f.ws.ID, Role: auth.RoleViewer, Label: "v", Scopes: policy.AllScopes,
	})
	_, err := f.dbs.ListTables(viewer, pg.ID)
	require.Equal(t, http.StatusForbidden, httpStatus(t, err), "a viewer reads no rows")

	readOnlyToken := auth.WithPrincipal(context.Background(), &auth.Principal{
		Type: auth.TypeToken, UserID: f.userID, TokenID: new(uuid.New()), WorkspaceID: f.ws.ID, Role: auth.RoleOwner,
		Label: "ro", Scopes: []string{policy.ScopeProjectsRead},
	})
	_, err = f.dbs.ListTables(readOnlyToken, pg.ID)
	require.Equal(t, http.StatusForbidden, httpStatus(t, err), "deployments:write is required")
}

func TestListTablesAsksTheAgentWithTheApplicationCredentials(t *testing.T) {
	f := newFixture(t)
	est := int64(7)
	rq := &fakeRequester{reply: func(p agentprotocol.DBQueryPayload) (agentprotocol.ResultFrame, error) {
		return dataOK(agentprotocol.DBQueryResult{Tables: []agentprotocol.DBTable{{Schema: "public", Name: "users", Kind: "table", RowEstimate: &est}}}), nil
	}}
	f.withRequester(rq)
	pg := f.createDB("pg", "postgres", "17")
	f.deploy(pg.ID)

	got, err := f.dbs.ListTables(f.person(), pg.ID)
	require.NoError(t, err)
	require.Equal(t, []databases.DataTable{{Schema: "public", Name: "users", Kind: "table", RowEstimate: &est}}, got.Tables)

	require.Len(t, rq.calls, 1)
	c := rq.calls[0]
	require.Equal(t, f.server.ID, c.serverID)
	require.Equal(t, agentprotocol.KindDBQuery, c.kind)
	require.Equal(t, agentprotocol.DBOpTables, c.payload.Op)
	require.Equal(t, pg.ID.String(), c.payload.ServiceID)
	require.Equal(t, "postgres", c.payload.Engine)
	require.Equal(t, "app", c.payload.Database)
	require.Equal(t, 30, c.payload.TimeoutSeconds)
	require.NotEmpty(t, c.secrets[agentprotocol.SecretDBUser])
	require.NotEmpty(t, c.secrets[agentprotocol.SecretDBPassword])
	require.Equal(t, 45*time.Second, c.timeout)
	require.Zero(t, f.auditCount(databases.ActionDatabaseQuery), "the table listing is not audited")
	require.Zero(t, f.auditCount(databases.ActionDatabaseBrowse))
}

func TestMySQLQueriesUseTheApplicationUserNotRoot(t *testing.T) {
	f := newFixture(t)
	rq := &fakeRequester{}
	f.withRequester(rq)
	my := f.createDB("my", "mysql", "8.4")
	f.deploy(my.ID)
	_, err := f.dbs.ListTables(f.person(), my.ID)
	require.NoError(t, err)
	require.NotEqual(t, "root", rq.calls[0].secrets[agentprotocol.SecretDBUser])
	require.Equal(t, "mysql", rq.calls[0].payload.Engine)
}

func TestBrowseIsAuditedAndPagesOfOneHundred(t *testing.T) {
	f := newFixture(t)
	s := "1"
	rq := &fakeRequester{reply: func(p agentprotocol.DBQueryPayload) (agentprotocol.ResultFrame, error) {
		return dataOK(agentprotocol.DBQueryResult{
			Results:    []agentprotocol.DBResultSet{{Columns: []agentprotocol.DBColumn{{Name: "a", Type: "int4"}}, Rows: [][]*string{{&s}, {nil}}, RowCount: 2, Truncated: true}},
			DurationMs: 3, TruncatedCells: true,
		}), nil
	}}
	f.withRequester(rq)
	pg := f.createDB("pg", "postgres", "17")
	f.deploy(pg.ID)

	res, err := f.dbs.BrowseTable(f.person(), pg.ID, databases.BrowseInput{Schema: "public", Table: "users", Offset: 200})
	require.NoError(t, err)
	require.Len(t, res.Results, 1)
	require.Equal(t, [][]*string{{&s}, {nil}}, res.Results[0].Rows)
	require.True(t, res.Results[0].Truncated)
	require.True(t, res.TruncatedCells)
	require.EqualValues(t, 3, res.DurationMs)

	c := rq.calls[0]
	require.Equal(t, agentprotocol.DBOpBrowse, c.payload.Op)
	require.Equal(t, "users", c.payload.Table)
	require.Equal(t, 200, c.payload.Offset)
	require.Equal(t, 100, c.payload.MaxRows)
	require.False(t, c.payload.AllowWrites)

	require.Equal(t, 1, f.auditCount(databases.ActionDatabaseBrowse))
	require.Equal(t, map[string]any{"service": "pg", "schema": "public", "table": "users"}, f.auditMeta(databases.ActionDatabaseBrowse))

	_, err = f.dbs.BrowseTable(f.person(), pg.ID, databases.BrowseInput{Table: "users", Offset: -1})
	require.Equal(t, "invalid_offset", apiCode(t, err))
}

func TestQueryAuditsTheVerbNeverTheSQL(t *testing.T) {
	f := newFixture(t)
	rq := &fakeRequester{}
	f.withRequester(rq)
	pg := f.createDB("pg", "postgres", "17")
	f.deploy(pg.ID)

	const sql = "  -- hi\nselect * from secrets where token = 'hunter2'"
	res, err := f.dbs.Query(f.person(), pg.ID, databases.QueryInput{SQL: sql})
	require.NoError(t, err)
	require.NotNil(t, res.Results)

	c := rq.calls[0]
	require.Equal(t, agentprotocol.DBOpSQL, c.payload.Op)
	require.Equal(t, sql, c.payload.SQL)
	require.False(t, c.payload.AllowWrites)
	require.Equal(t, 1000, c.payload.MaxRows)

	meta := f.auditMeta(databases.ActionDatabaseQuery)
	require.Equal(t, map[string]any{"service": "pg", "engine": "postgres", "production": true, "allowWrites": false, "verb": "SELECT"}, meta)
	var raw string
	require.NoError(t, f.pool.QueryRow(context.Background(), "SELECT metadata::text FROM audit_events WHERE action = 'database.query'").Scan(&raw))
	require.NotContains(t, raw, "hunter2")

	_, err = f.dbs.Query(f.person(), pg.ID, databases.QueryInput{SQL: "   "})
	require.Equal(t, "invalid_sql", apiCode(t, err))
}

func TestQueryWritesNeedConfirmationInProduction(t *testing.T) {
	f := newFixture(t) // the fixture environment is production
	rq := &fakeRequester{}
	f.withRequester(rq)
	pg := f.createDB("pg", "postgres", "17")
	f.deploy(pg.ID)
	person := f.person()

	_, err := f.dbs.Query(person, pg.ID, databases.QueryInput{SQL: "DELETE FROM t", AllowWrites: true})
	require.Equal(t, databases.CodeConfirmationRequired, apiCode(t, err))
	_, err = f.dbs.Query(person, pg.ID, databases.QueryInput{SQL: "DELETE FROM t", AllowWrites: true, Confirm: "wrong"})
	require.Equal(t, databases.CodeConfirmationRequired, apiCode(t, err))
	require.Empty(t, rq.calls)
	require.Zero(t, f.auditCount(databases.ActionDatabaseQuery), "a refused write is not run, so not audited")

	_, err = f.dbs.Query(person, pg.ID, databases.QueryInput{SQL: "DELETE FROM t", AllowWrites: true, Confirm: "pg"})
	require.NoError(t, err)
	require.True(t, rq.calls[0].payload.AllowWrites)
	require.Equal(t, true, f.auditMeta(databases.ActionDatabaseQuery)["allowWrites"])
	require.Equal(t, "DELETE", f.auditMeta(databases.ActionDatabaseQuery)["verb"])

	// A read needs no confirmation.
	_, err = f.dbs.Query(person, pg.ID, databases.QueryInput{SQL: "SELECT 1"})
	require.NoError(t, err)
}

func TestQueryWritesFromARESTTokenNeedAnApproval(t *testing.T) {
	f := newFixture(t)
	g := f.installGuard()
	rq := &fakeRequester{}
	f.withRequester(rq)
	pg := f.createDB("pg", "postgres", "17")
	f.deploy(pg.ID)
	rest := f.restToken()

	in := databases.QueryInput{SQL: "DROP TABLE t", AllowWrites: true, Confirm: "pg"}
	_, writeErr := f.dbs.Query(rest, pg.ID, in)
	ar, ok := prodguard.AsRequired(writeErr)
	require.True(t, ok, "got %v", writeErr)
	require.Contains(t, ar.Plan.Summary, "Run a write statement on pg")
	require.Empty(t, rq.calls)

	// Reads are not guarded.
	_, err := f.dbs.Query(rest, pg.ID, databases.QueryInput{SQL: "SELECT 1"})
	require.NoError(t, err)

	f.approve(g, writeErr)
	_, err = f.dbs.Query(rest, pg.ID, in)
	require.NoError(t, err)
	require.True(t, rq.calls[len(rq.calls)-1].payload.AllowWrites)
}

func TestQueryWritesAreRefusedOnAReadReplica(t *testing.T) {
	f := newReplicaFixture(t)
	rq := &fakeRequester{}
	f.withRequester(rq)
	for _, id := range []uuid.UUID{f.server.ID, f.server2.ID} {
		facts, _ := json.Marshal(agentprotocol.Facts{OS: "linux", Capabilities: []string{agentprotocol.CapReplication, agentprotocol.CapDBQuery, "mesh"}})
		_, err := f.pool.Exec(context.Background(), "UPDATE servers SET facts = $2 WHERE id = $1", id, facts)
		require.NoError(t, err)
	}
	pg := f.primary("pg")
	rep := f.streamingReplica(pg.ID, f.server2.ID)

	_, err := f.dbs.Query(f.person(), rep, databases.QueryInput{SQL: "INSERT INTO t VALUES (1)", AllowWrites: true, Confirm: "pg-replica"})
	require.Equal(t, databases.CodeReplicaReadOnly, apiCode(t, err))
	require.Empty(t, rq.calls)

	_, err = f.dbs.Query(f.person(), rep, databases.QueryInput{SQL: "SELECT 1"})
	require.NoError(t, err, "a replica can be read")
}

func TestAgentFailureAndDeliveryErrorsMapToAPIErrors(t *testing.T) {
	f := newFixture(t)
	rq := &fakeRequester{}
	f.withRequester(rq)
	pg := f.createDB("pg", "postgres", "17")
	f.deploy(pg.ID)
	person := f.person()
	in := databases.QueryInput{SQL: "INSERT INTO t VALUES (1)"}

	rq.reply = func(agentprotocol.DBQueryPayload) (agentprotocol.ResultFrame, error) {
		return agentprotocol.ResultFrame{Outcome: agentprotocol.OutcomeFailed, Error: "ERROR: cannot execute INSERT in a read-only transaction"}, nil
	}
	_, err := f.dbs.Query(person, pg.ID, in)
	require.Equal(t, databases.CodeQueryFailed, apiCode(t, err))
	require.Equal(t, http.StatusUnprocessableEntity, httpStatus(t, err))
	require.Contains(t, err.Error(), "read-only transaction")

	rq.reply = func(agentprotocol.DBQueryPayload) (agentprotocol.ResultFrame, error) {
		return agentprotocol.ResultFrame{Outcome: agentprotocol.OutcomeFailed}, nil
	}
	_, err = f.dbs.Query(person, pg.ID, in)
	require.Equal(t, databases.CodeQueryFailed, apiCode(t, err))

	rq.reply = func(agentprotocol.DBQueryPayload) (agentprotocol.ResultFrame, error) {
		return agentprotocol.ResultFrame{}, apierr.New(http.StatusServiceUnavailable, "server_offline", "the server is offline")
	}
	_, err = f.dbs.Query(person, pg.ID, in)
	require.Equal(t, "server_offline", apiCode(t, err))
	require.Equal(t, http.StatusServiceUnavailable, httpStatus(t, err))

	rq.reply = func(agentprotocol.DBQueryPayload) (agentprotocol.ResultFrame, error) {
		return agentprotocol.ResultFrame{}, apierr.New(http.StatusGatewayTimeout, "server_timeout", "the server did not answer in time")
	}
	_, err = f.dbs.Query(person, pg.ID, in)
	require.Equal(t, "server_timeout", apiCode(t, err))

	rq.reply = func(agentprotocol.DBQueryPayload) (agentprotocol.ResultFrame, error) {
		return agentprotocol.ResultFrame{}, context.DeadlineExceeded
	}
	_, err = f.dbs.Query(person, pg.ID, in)
	require.Equal(t, "server_timeout", apiCode(t, err))

	rq.reply = func(agentprotocol.DBQueryPayload) (agentprotocol.ResultFrame, error) {
		return agentprotocol.ResultFrame{}, assertAnError("the server disconnected before it answered")
	}
	_, err = f.dbs.Query(person, pg.ID, in)
	require.Equal(t, "server_offline", apiCode(t, err), "a lost connection reads as offline")
}

type assertAnError string

func (e assertAnError) Error() string { return string(e) }
