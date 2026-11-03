package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/internal/databases/engines"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Limits of one db_query answer. The control plane asks for smaller per-set row
// limits; these bound what it may ask for.
const (
	dbQueryMaxSets      = 10
	dbQueryMaxCellBytes = 4096
	dbQueryMaxBytes     = 4 << 20
	dbQueryMaxRows      = 10000
	dbQueryDefaultRows  = 1000
	dbQueryMaxTimeout   = 120
	dbQueryTimeout      = 30
	dbQueryTableLimit   = 2000
	// dbQueryConnectTimeout bounds one connection attempt (one address).
	dbQueryConnectTimeout = 3 * time.Second
	// dbQueryMaxSQL bounds the statement text the agent accepts.
	dbQueryMaxSQL = 1 << 20
)

func dbQueryFailed(id, msg string) agentprotocol.ResultFrame {
	return agentprotocol.ResultFrame{CommandID: id, Outcome: agentprotocol.OutcomeFailed, Error: msg}
}

// dbTarget is how the agent reaches the database of a db_query.
type dbTarget struct {
	engine   string
	hosts    []string // addresses to try in order: container IPs, then its name
	port     int
	user     string
	password string
	database string
}

// dbRun is one statement batch to run against a dbTarget.
type dbRun struct {
	sql         string
	allowWrites bool
	timeout     time.Duration
	maxRows     int
}

// handleDBQuery answers the Data tab: it lists tables, pages through one table
// or runs SQL against a managed SQL database running on this host. PostgreSQL
// and MySQL/MariaDB are reached over the container network with the application
// credentials the control plane sends; ClickHouse is reached with
// clickhouse-client inside the container. Sessions are read-only unless the
// control plane allows writes.
func (e *Engine) handleDBQuery(ctx context.Context, cmd agentprotocol.Command) agentprotocol.ResultFrame {
	var p agentprotocol.DBQueryPayload
	if err := json.Unmarshal(cmd.Payload, &p); err != nil {
		return dbQueryFailed(cmd.ID, "invalid db_query payload: "+err.Error())
	}
	rd := newRedactor(cmd.Secrets, nil)
	fail := func(msg string) agentprotocol.ResultFrame { return dbQueryFailed(cmd.ID, rd.apply(msg)) }

	if p.ServiceID == "" {
		return fail("serviceId is required")
	}
	eng := engines.Lookup(p.Engine)
	if eng == nil || !dbQuerySupported(p.Engine) {
		return fail("the Data tab does not support engine " + strconv.Quote(p.Engine))
	}
	user, password := cmd.Secrets[agentprotocol.SecretDBUser], cmd.Secrets[agentprotocol.SecretDBPassword]
	if user == "" {
		return fail("database credentials are missing")
	}
	run, err := planDBQuery(p)
	if err != nil {
		return fail(err.Error())
	}

	target := e.runningDB(ctx, p.ServiceID)
	if target == nil {
		return fail("the database is not running")
	}

	started := time.Now()
	b := newDBResultBuilder(run.maxRows)
	switch p.Engine {
	case engines.ClickHouse:
		err = e.queryClickHouse(ctx, target.ID, engines.Credentials{User: user, Password: password, Database: p.Database}, run, rd, b)
	default:
		var nets []string
		if n := probeNetworkOf(*target); n != "" {
			nets = []string{n}
		} else {
			nets = dbQueryNetworks(*target)
		}
		release := e.probeAccess(ctx, nets...)
		defer release()
		t := dbTarget{
			engine: p.Engine, hosts: append(probeIPs(*target, nets...), target.Name), port: eng.Port,
			user: user, password: password, database: p.Database,
		}
		if p.Engine == engines.Postgres {
			err = queryPostgres(ctx, t, run, b)
		} else {
			err = queryMySQL(ctx, t, run, b)
		}
	}
	if err != nil {
		return fail(dbQueryErrorText(err))
	}

	res := b.result()
	res.DurationMs = time.Since(started).Milliseconds()
	if p.Op == agentprotocol.DBOpTables {
		res.Tables = tablesFromSet(b.firstSet())
		res.Results = nil
	}
	data, err := json.Marshal(res)
	if err != nil {
		return fail("encode result: " + err.Error())
	}
	return agentprotocol.ResultFrame{CommandID: cmd.ID, Outcome: agentprotocol.OutcomeSucceeded, Data: data}
}

// dbQuerySupported reports whether the Data tab serves engine.
func dbQuerySupported(engine string) bool {
	switch engine {
	case engines.Postgres, engines.MySQL, engines.MariaDB, engines.ClickHouse:
		return true
	}
	return false
}

// planDBQuery validates the payload and turns the operation into the
// statements to run and the limits that apply.
func planDBQuery(p agentprotocol.DBQueryPayload) (dbRun, error) {
	timeout := p.TimeoutSeconds
	if timeout <= 0 {
		timeout = dbQueryTimeout
	}
	timeout = min(timeout, dbQueryMaxTimeout)
	maxRows := p.MaxRows
	if maxRows <= 0 {
		maxRows = dbQueryDefaultRows
	}
	maxRows = min(maxRows, dbQueryMaxRows)
	run := dbRun{timeout: time.Duration(timeout) * time.Second, maxRows: maxRows}

	switch p.Op {
	case agentprotocol.DBOpTables:
		run.sql = tablesSQL(p.Engine)
		run.maxRows = dbQueryTableLimit
	case agentprotocol.DBOpBrowse:
		if p.Offset < 0 {
			return dbRun{}, errors.New("offset must not be negative")
		}
		sql, err := browseSQL(p.Engine, p.Schema, p.Table, maxRows+1, p.Offset)
		if err != nil {
			return dbRun{}, err
		}
		run.sql = sql
	case agentprotocol.DBOpSQL:
		if strings.TrimSpace(p.SQL) == "" {
			return dbRun{}, errors.New("sql is required")
		}
		if len(p.SQL) > dbQueryMaxSQL {
			return dbRun{}, errors.New("the statement is too long")
		}
		run.sql = p.SQL
		run.allowWrites = p.AllowWrites
	default:
		return dbRun{}, fmt.Errorf("unsupported db_query op %q", p.Op)
	}
	return run, nil
}

// tablesSQL lists the tables and views of the connected database (at most
// dbQueryTableLimit): schema, name, kind (table|view|other), row estimate.
func tablesSQL(engine string) string {
	switch engine {
	case engines.Postgres:
		return `SELECT t.table_schema, t.table_name,
  CASE t.table_type WHEN 'BASE TABLE' THEN 'table' WHEN 'VIEW' THEN 'view' ELSE 'other' END,
  CASE WHEN c.reltuples >= 0 THEN c.reltuples::bigint END
FROM information_schema.tables t
LEFT JOIN pg_namespace n ON n.nspname = t.table_schema
LEFT JOIN pg_class c ON c.relnamespace = n.oid AND c.relname = t.table_name
WHERE t.table_schema NOT IN ('pg_catalog', 'information_schema')
ORDER BY 1, 2 LIMIT ` + strconv.Itoa(dbQueryTableLimit)
	case engines.ClickHouse:
		return `SELECT database, name, if(engine LIKE '%View', 'view', 'table'), total_rows
FROM system.tables WHERE database = currentDatabase() ORDER BY name LIMIT ` + strconv.Itoa(dbQueryTableLimit)
	default: // MySQL, MariaDB
		return `SELECT table_schema, table_name,
  CASE table_type WHEN 'BASE TABLE' THEN 'table' WHEN 'VIEW' THEN 'view' ELSE 'other' END, table_rows
FROM information_schema.tables WHERE table_schema = DATABASE() ORDER BY table_name LIMIT ` + strconv.Itoa(dbQueryTableLimit)
	}
}

// browseSQL is the page query of one table: every column, limit rows from
// offset. Identifiers are quoted for the engine.
func browseSQL(engine, schema, table string, limit, offset int) (string, error) {
	if table == "" {
		return "", errors.New("table is required")
	}
	var ref string
	switch engine {
	case engines.Postgres:
		id := pgx.Identifier{table}
		if schema != "" {
			id = pgx.Identifier{schema, table}
		}
		ref = id.Sanitize()
	case engines.ClickHouse:
		ref = engines.QuoteClickHouseIdent(table)
		if schema != "" {
			ref = engines.QuoteClickHouseIdent(schema) + "." + ref
		}
	default:
		ref = engines.QuoteMySQLIdent(table)
		if schema != "" {
			ref = engines.QuoteMySQLIdent(schema) + "." + ref
		}
	}
	return fmt.Sprintf("SELECT * FROM %s LIMIT %d OFFSET %d", ref, limit, offset), nil
}

// dbQueryNetworks lists the container's networks the agent may use to reach it:
// every one except the shared edge network, in name order.
func dbQueryNetworks(info docker.ContainerInfo) []string {
	var nets []string
	for n := range info.Networks {
		if n != agentprotocol.EdgeNetwork {
			nets = append(nets, n)
		}
	}
	sort.Strings(nets)
	return nets
}

// dbQueryErrorText is the message of a failed query: the server's own text for
// SQL errors, a plain sentence for transport problems.
func dbQueryErrorText(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "the query timed out"
	case errors.Is(err, context.Canceled):
		return "the query was canceled"
	}
	return err.Error()
}

// tablesFromSet reads the four-column table listing result.
func tablesFromSet(set *agentprotocol.DBResultSet) []agentprotocol.DBTable {
	if set == nil {
		return nil
	}
	cell := func(row []*string, i int) string {
		if i < len(row) && row[i] != nil {
			return *row[i]
		}
		return ""
	}
	out := make([]agentprotocol.DBTable, 0, len(set.Rows))
	for _, row := range set.Rows {
		t := agentprotocol.DBTable{Schema: cell(row, 0), Name: cell(row, 1), Kind: cell(row, 2)}
		if len(row) > 3 && row[3] != nil {
			if n, err := strconv.ParseInt(*row[3], 10, 64); err == nil && n >= 0 {
				t.RowEstimate = &n
			}
		}
		out = append(out, t)
	}
	return out
}

// dbResultBuilder collects the result sets of one query within the answer's
// limits: maxRows rows per set, dbQueryMaxSets sets, dbQueryMaxCellBytes per
// cell and dbQueryMaxBytes in all. Rows beyond a limit are dropped and the set
// is marked truncated.
type dbResultBuilder struct {
	maxRows int
	sets    []agentprotocol.DBResultSet
	// cur is the open set (nil when none is open or the set limit was hit).
	cur            *agentprotocol.DBResultSet
	full           bool // the current set stopped accepting rows
	bytes          int
	truncatedCells bool
}

func newDBResultBuilder(maxRows int) *dbResultBuilder { return &dbResultBuilder{maxRows: maxRows} }

// beginSet opens a result set. It reports false when the set limit is hit; the
// caller then still reads and discards the rows.
func (b *dbResultBuilder) beginSet(cols []agentprotocol.DBColumn) bool {
	b.endSet("")
	if len(b.sets) >= dbQueryMaxSets {
		return false
	}
	b.sets = append(b.sets, agentprotocol.DBResultSet{Columns: cols, Rows: [][]*string{}})
	b.cur = &b.sets[len(b.sets)-1]
	b.full = false
	return true
}

// endSet closes the open set, recording the server's completion tag.
func (b *dbResultBuilder) endSet(tag string) {
	if b.cur != nil && tag != "" {
		b.cur.CommandTag = tag
	}
	b.cur = nil
}

// addRow appends one row; a nil cell is NULL. It reports whether more rows are
// still wanted.
func (b *dbResultBuilder) addRow(vals [][]byte) bool {
	if b.cur == nil || b.full {
		return false
	}
	if len(b.cur.Rows) >= b.maxRows {
		b.cur.Truncated, b.full = true, true
		return false
	}
	row := make([]*string, len(vals))
	size := 0
	for i, v := range vals {
		if v == nil {
			continue
		}
		s, clipped := clipCell(v)
		row[i] = &s
		size += len(s) + 8
		b.truncatedCells = b.truncatedCells || clipped
	}
	if b.bytes+size > dbQueryMaxBytes {
		b.cur.Truncated, b.full = true, true
		return false
	}
	b.bytes += size
	b.cur.Rows = append(b.cur.Rows, row)
	b.cur.RowCount = len(b.cur.Rows)
	return true
}

func (b *dbResultBuilder) result() agentprotocol.DBQueryResult {
	b.endSet("")
	return agentprotocol.DBQueryResult{Results: b.sets, TruncatedCells: b.truncatedCells}
}

func (b *dbResultBuilder) firstSet() *agentprotocol.DBResultSet {
	if len(b.sets) == 0 {
		return nil
	}
	return &b.sets[0]
}

// clipCell returns v as text, cut to dbQueryMaxCellBytes (at a character
// boundary, with a trailing ellipsis) when longer.
func clipCell(v []byte) (string, bool) {
	if len(v) <= dbQueryMaxCellBytes {
		return string(v), false
	}
	cut := dbQueryMaxCellBytes
	for cut > 0 && !utf8.RuneStart(v[cut]) {
		cut--
	}
	return string(v[:cut]) + "…", true
}
