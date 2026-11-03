package engine

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"strconv"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/nebulactrl/nebulactrl/internal/databases/engines"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// firstReachable runs connect against each host in turn and returns the first
// connection that works. The error of the last attempt is returned when none do.
func firstReachable[T any](ctx context.Context, hosts []string, connect func(ctx context.Context, host string) (T, error)) (T, error) {
	var zero T
	var last error
	for _, h := range hosts {
		if h == "" {
			continue
		}
		c, err := connect(ctx, h)
		if err == nil {
			return c, nil
		}
		last = err
		if ctx.Err() != nil {
			break
		}
	}
	if last == nil {
		last = errors.New("no address to reach the database")
	}
	return zero, last
}

// detachedClose gives cleanup its own short deadline: the query's context may
// already be spent.
func detachedClose(fn func(ctx context.Context)) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	fn(ctx)
}

var pgTypes = pgtype.NewMap()

// pgColumns describes the fields of a result set.
func pgColumns(fds []pgconn.FieldDescription) []agentprotocol.DBColumn {
	cols := make([]agentprotocol.DBColumn, len(fds))
	for i, fd := range fds {
		typ := "oid " + strconv.FormatUint(uint64(fd.DataTypeOID), 10)
		if t, ok := pgTypes.TypeForOID(fd.DataTypeOID); ok {
			typ = t.Name
		}
		cols[i] = agentprotocol.DBColumn{Name: fd.Name, Type: typ}
	}
	return cols
}

// pgConfig is the connection settings for one host. Nothing comes from the
// agent's environment: every field the PG* variables could set is overridden.
func pgConfig(t dbTarget, host string, run dbRun) (*pgconn.Config, error) {
	cfg, err := pgconn.ParseConfig("")
	if err != nil {
		return nil, err
	}
	cfg.Host, cfg.Port = host, uint16(t.port)
	cfg.User, cfg.Password, cfg.Database = t.user, t.password, t.database
	cfg.TLSConfig, cfg.Fallbacks = nil, nil
	cfg.ConnectTimeout = dbQueryConnectTimeout
	cfg.RuntimeParams = map[string]string{
		"statement_timeout": strconv.FormatInt(run.timeout.Milliseconds(), 10),
		"application_name":  "nebulactrl-data",
	}
	if !run.allowWrites {
		cfg.RuntimeParams["default_transaction_read_only"] = "on"
	}
	return cfg, nil
}

// queryPostgres runs run.sql (several statements allowed) over the simple
// query protocol and collects every result set into b.
func queryPostgres(ctx context.Context, t dbTarget, run dbRun, b *dbResultBuilder) error {
	ctx, cancel := context.WithTimeout(ctx, run.timeout+5*time.Second)
	defer cancel()
	conn, err := firstReachable(ctx, t.hosts, func(ctx context.Context, host string) (*pgconn.PgConn, error) {
		cfg, err := pgConfig(t, host, run)
		if err != nil {
			return nil, err
		}
		return pgconn.ConnectConfig(ctx, cfg)
	})
	if err != nil {
		return fmt.Errorf("connect to the database: %w", err)
	}
	defer detachedClose(func(ctx context.Context) { _ = conn.Close(ctx) })

	mrr := conn.Exec(ctx, run.sql)
	defer func() { _ = mrr.Close() }()
	for mrr.NextResult() {
		rr := mrr.ResultReader()
		wanted := b.beginSet(pgColumns(rr.FieldDescriptions()))
		for rr.NextRow() {
			if wanted {
				// Rows are read to the end even when the set is full: stopping
				// would cancel the rest of a multi-statement batch.
				wanted = b.addRow(rr.Values())
			}
		}
		tag, err := rr.Close()
		if err != nil {
			return err
		}
		b.endSet(tag.String())
	}
	return mrr.Close()
}

// mysqlSession is one dedicated connection and the pool that owns it.
type mysqlSession struct {
	pool *sql.DB
	conn *sql.Conn
}

func (m *mysqlSession) close() {
	_ = m.conn.Close()
	_ = m.pool.Close()
}

// mysqlSessionSQL are the statements that put a fresh session in the mode the
// run asks for: read-only unless writes are allowed, and a statement timeout
// (max_execution_time in milliseconds for MySQL, max_statement_time in seconds
// for MariaDB).
func mysqlSessionSQL(engine string, run dbRun) []string {
	var out []string
	if !run.allowWrites {
		out = append(out, "SET SESSION TRANSACTION READ ONLY")
	}
	if engine == engines.MariaDB {
		out = append(out, "SET SESSION max_statement_time="+strconv.FormatFloat(run.timeout.Seconds(), 'f', 3, 64))
	} else {
		out = append(out, "SET SESSION max_execution_time="+strconv.FormatInt(run.timeout.Milliseconds(), 10))
	}
	return out
}

// queryMySQL runs run.sql (several statements allowed) on one dedicated
// connection and collects every result set into b.
func queryMySQL(ctx context.Context, t dbTarget, run dbRun, b *dbResultBuilder) error {
	ctx, cancel := context.WithTimeout(ctx, run.timeout+5*time.Second)
	defer cancel()
	sess, err := firstReachable(ctx, t.hosts, func(ctx context.Context, host string) (*mysqlSession, error) {
		cfg := mysql.NewConfig()
		cfg.Net, cfg.Addr = "tcp", net.JoinHostPort(host, strconv.Itoa(t.port))
		cfg.User, cfg.Passwd, cfg.DBName = t.user, t.password, t.database
		cfg.MultiStatements = true
		cfg.Timeout = dbQueryConnectTimeout
		cfg.ReadTimeout = run.timeout + 5*time.Second
		connector, err := mysql.NewConnector(cfg)
		if err != nil {
			return nil, err
		}
		pool := sql.OpenDB(connector)
		pool.SetMaxOpenConns(1)
		c, err := pool.Conn(ctx)
		if err != nil {
			_ = pool.Close()
			return nil, err
		}
		return &mysqlSession{pool: pool, conn: c}, nil
	})
	if err != nil {
		return fmt.Errorf("connect to the database: %w", err)
	}
	defer sess.close()

	for _, stmt := range mysqlSessionSQL(t.engine, run) {
		if _, err := sess.conn.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	rows, err := sess.conn.QueryContext(ctx, run.sql)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for {
		types, err := rows.ColumnTypes()
		if err != nil {
			return err
		}
		cols := make([]agentprotocol.DBColumn, len(types))
		for i, ct := range types {
			cols[i] = agentprotocol.DBColumn{Name: ct.Name(), Type: ct.DatabaseTypeName()}
		}
		wanted := b.beginSet(cols)
		if len(cols) > 0 {
			cells := make([]sql.RawBytes, len(cols))
			dest := make([]any, len(cols))
			for i := range cells {
				dest[i] = &cells[i]
			}
			for rows.Next() {
				if err := rows.Scan(dest...); err != nil {
					return err
				}
				if wanted {
					vals := make([][]byte, len(cells))
					for i, c := range cells {
						vals[i] = c
					}
					wanted = b.addRow(vals)
				}
			}
			b.endSet("")
		} else {
			b.endSet("OK") // a statement without a result set
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if !rows.NextResultSet() {
			return rows.Err()
		}
	}
}
