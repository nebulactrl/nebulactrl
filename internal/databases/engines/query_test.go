package engines

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestIdentifierQuoting(t *testing.T) {
	require.Equal(t, "`users`", QuoteMySQLIdent("users"))
	require.Equal(t, "`we``ird`", QuoteMySQLIdent("we`ird"))
	require.Equal(t, "`a\\`b\\\\`", QuoteClickHouseIdent("a`b\\"))
}

func TestQueryPlanClickHouse(t *testing.T) {
	c := Credentials{User: "app", Password: "pw-secret", Database: "appdb"}
	plan, err := QueryPlan(c, "SELECT 1; SELECT 2", false, 30*time.Second, 1000)
	require.NoError(t, err)
	require.Equal(t, "clickhouse-client", plan.Argv[0])
	require.Contains(t, plan.Argv, "--readonly=1")
	require.Contains(t, plan.Argv, "--multiquery")
	require.Contains(t, plan.Argv, "--max_execution_time=30")
	require.Contains(t, plan.Argv, "--max_result_rows=1001")
	require.Contains(t, plan.Argv, "--result_overflow_mode=break")
	require.Contains(t, plan.Argv, "JSONCompact")
	require.Equal(t, []string{"CLICKHOUSE_PASSWORD=pw-secret"}, plan.Env)
	require.Equal(t, "SELECT 1; SELECT 2", plan.Stdin)
	for _, a := range plan.Argv {
		require.NotContains(t, a, "pw-secret")
	}

	plan, err = QueryPlan(c, "INSERT INTO t VALUES (1)", true, 500*time.Millisecond, 0)
	require.NoError(t, err)
	require.NotContains(t, plan.Argv, "--readonly=1")
	require.Contains(t, plan.Argv, "--max_execution_time=1", "the timeout is at least a second")
	require.Contains(t, plan.Argv, "--max_result_rows=2")

	_, err = QueryPlan(Credentials{User: "app; DROP", Database: "appdb"}, "SELECT 1", false, time.Second, 10)
	require.Error(t, err, "names reach argv, so they are validated")
}
