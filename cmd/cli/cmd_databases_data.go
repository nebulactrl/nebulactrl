package main

import (
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/nebulactrl/nebulactrl/internal/apiclient"
)

func newDatabasesTablesCmd(c *cli) *cobra.Command {
	return &cobra.Command{
		Use: "tables <service>", Short: "List the tables and views of a SQL database", Args: exactArgs(1),
		Long: "Lists the tables and views of a running PostgreSQL, MySQL, MariaDB or ClickHouse database through its agent. " +
			"The row count is the engine's own estimate.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := c.connect(); err != nil {
				return err
			}
			ctx, cancel := c.ctx(cmd.Context())
			defer cancel()
			id, err := c.serviceID(ctx, args[0])
			if err != nil {
				return err
			}
			r, err := c.api.ListDatabaseTablesWithResponse(ctx, id)
			if err := check(r, err); err != nil {
				return err
			}
			tables := items(r.JSON200.Tables)
			if c.asJSON {
				return c.printJSON(tables)
			}
			c.table([]string{"SCHEMA", "NAME", "KIND", "ROWS"}, tableRows(tables))
			return nil
		},
	}
}

// tableRows formats the table listing; the row count is "-" when the engine
// has no estimate.
func tableRows(tables []apiclient.DataTable) [][]string {
	rows := make([][]string, 0, len(tables))
	for _, t := range tables {
		est := "-"
		if t.RowEstimate != nil {
			est = fmt.Sprint(*t.RowEstimate)
		}
		rows = append(rows, []string{t.Schema, t.Name, string(t.Kind), est})
	}
	return rows
}

func newDatabasesQueryCmd(c *cli) *cobra.Command {
	var file, confirm string
	var allowWrites bool
	cmd := &cobra.Command{
		Use: "query <service> [--file f.sql] [--allow-writes] [--confirm <slug>] [-- <sql>]", Short: "Run SQL on a database (audited)", Args: minArgs(1),
		Long: "Runs one or more statements on a running PostgreSQL, MySQL, MariaDB or ClickHouse database through its agent, with a 30 second timeout.\n" +
			"The SQL is the words after `--`, else the file given with --file, else standard input. The session is read-only unless --allow-writes is set;\n" +
			"in a production environment a write also needs --confirm <service slug>. Up to 1000 rows per result set are returned.\n" +
			"The run is recorded in the audit log with the first SQL keyword, never the statement. Needs the deployments:write scope.",
		Example: "  nebulactrl db query acme/production/main -- 'SELECT count(*) FROM users'\n" +
			"  nebulactrl databases query acme/staging/main --allow-writes --file migrate.sql",
		RunE: func(cmd *cobra.Command, args []string) error {
			sql, err := querySQL(cmd, args, file, c.in)
			if err != nil {
				return err
			}
			if confirm != "" && !allowWrites {
				return usagef("--confirm only applies with --allow-writes")
			}
			if err := c.connect(); err != nil {
				return err
			}
			ctx, cancel := c.ctx(cmd.Context())
			defer cancel()
			id, err := c.serviceID(ctx, args[0])
			if err != nil {
				return err
			}
			body := apiclient.QueryDatabaseInputBody{Sql: sql}
			if allowWrites {
				body.AllowWrites = &allowWrites
			}
			if confirm != "" {
				body.Confirm = &confirm
			}
			r, err := c.api.QueryDatabaseWithResponse(ctx, id, body)
			if err := check(r, err); err != nil {
				return err
			}
			if c.asJSON {
				return c.printJSON(r.JSON200)
			}
			printDataResult(c.out, r.JSON200)
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&file, "file", "", "read the SQL from this file (`-` for stdin)")
	f.BoolVar(&allowWrites, "allow-writes", false, "lift the read-only session so statements may change data")
	f.StringVar(&confirm, "confirm", "", "the service slug, required for a write in a production environment")
	return cmd
}

// querySQL picks the statement text: the words after `--`, else --file, else
// stdin.
func querySQL(cmd *cobra.Command, args []string, file string, in io.Reader) (string, error) {
	dash := cmd.ArgsLenAtDash()
	var sql string
	switch {
	case dash >= 0 && dash != 1:
		return "", usagef("expected exactly one <service> before `--`")
	case dash < 0 && len(args) != 1:
		return "", usagef("expected exactly one <service>; give the SQL after `--`")
	case dash == 1 && len(args) > 1:
		if file != "" {
			return "", usagef("give the SQL after `--` or with --file, not both")
		}
		sql = strings.Join(args[1:], " ")
	case file == "-" || file == "":
		raw, err := io.ReadAll(io.LimitReader(in, 1<<20))
		if err != nil {
			return "", fmt.Errorf("read the SQL from stdin: %w", err)
		}
		sql = string(raw)
	default:
		raw, err := os.ReadFile(file)
		if err != nil {
			return "", fmt.Errorf("read %s: %w", file, err)
		}
		sql = string(raw)
	}
	if strings.TrimSpace(sql) == "" {
		return "", usagef("no SQL given: put it after `--`, use --file, or pipe it to stdin")
	}
	return sql, nil
}

// printDataResult writes one table per result set and a footer line.
func printDataResult(out io.Writer, res *apiclient.DataResult) {
	var total int64
	var truncated bool
	for i, set := range items(res.Results) {
		if i > 0 {
			fmt.Fprintln(out)
		}
		total += set.RowCount
		truncated = truncated || deref(set.Truncated)
		cols := items(set.Columns)
		if len(cols) == 0 {
			fmt.Fprintln(out, orDefault(deref(set.CommandTag), "OK"))
			continue
		}
		tw := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
		names := make([]string, len(cols))
		for j, col := range cols {
			names[j] = oneLine(col.Name)
		}
		fmt.Fprintln(tw, strings.Join(names, "\t"))
		for _, row := range items(set.Rows) {
			cells := make([]string, len(cols))
			for j, v := range items(row) {
				if j >= len(cells) {
					break
				}
				cells[j] = "NULL"
				if v != nil {
					cells[j] = oneLine(*v)
				}
			}
			fmt.Fprintln(tw, strings.Join(cells, "\t"))
		}
		_ = tw.Flush()
	}
	note := ""
	if truncated {
		note = " (truncated)"
	}
	fmt.Fprintf(out, "%d rows · %dms%s\n", total, res.DurationMs, note)
}

// cell makes a value fit on one table line.
func oneLine(s string) string {
	return strings.NewReplacer("\t", `\t`, "\r", `\r`, "\n", `\n`).Replace(s)
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
