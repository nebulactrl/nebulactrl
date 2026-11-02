// Command pgdb creates and drops the throwaway database of the browser e2e
// stack (tests/e2e/run.sh).
//
//	pgdb create ADMIN_URL NAME   drops NAME if it exists, creates it empty and prints its connection URL
//	pgdb drop ADMIN_URL NAME     drops NAME (open connections are terminated)
//	pgdb sweep ADMIN_URL PREFIX  drops every database whose name starts with PREFIX
//
// A run uses a database name of its own (run.sh), so a control plane that an
// earlier, crashed run left behind can never reconnect to the database of the
// next one and work on its jobs with its own keys; sweep removes what such runs
// left.
//
// ADMIN_URL is any database of the same server the caller may create databases
// in (NEBULACTRL_TEST_DATABASE_URL). It is never modified, only used to run
// the two statements; NAME is restricted to [a-z0-9_] so it can be quoted
// safely as an identifier.
package main

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"
)

var validName = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "pgdb:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) != 3 || (args[0] != "create" && args[0] != "drop" && args[0] != "sweep") {
		return fmt.Errorf("usage: pgdb create|drop|sweep ADMIN_URL NAME|PREFIX")
	}
	cmd, adminURL, name := args[0], args[1], args[2]
	if !validName.MatchString(name) {
		return fmt.Errorf("database name %q must match %s", name, validName)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	conn, err := pgx.Connect(ctx, adminURL)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer func() { _ = conn.Close(ctx) }()

	if cmd == "sweep" {
		return sweep(ctx, conn, name)
	}
	if err := dropDatabase(ctx, conn, name); err != nil {
		return err
	}
	if cmd == "drop" {
		return nil
	}
	if _, err := conn.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		return fmt.Errorf("create database: %w", err)
	}
	u, err := url.Parse(adminURL)
	if err != nil {
		return fmt.Errorf("parse url: %w", err)
	}
	u.Path = "/" + name
	fmt.Println(u.String())
	return nil
}

func dropDatabase(ctx context.Context, conn *pgx.Conn, name string) error {
	stmt := "DROP DATABASE IF EXISTS " + pgx.Identifier{name}.Sanitize() + " WITH (FORCE)"
	if _, err := conn.Exec(ctx, stmt); err != nil {
		return fmt.Errorf("drop database %s: %w", name, err)
	}
	return nil
}

// sweep drops every database named prefix or prefix_*, except the one the
// connection is on.
func sweep(ctx context.Context, conn *pgx.Conn, prefix string) error {
	rows, err := conn.Query(ctx,
		`SELECT datname FROM pg_database
		 WHERE (datname = $1 OR starts_with(datname, $1 || '_')) AND datname <> current_database()`, prefix)
	if err != nil {
		return fmt.Errorf("list databases: %w", err)
	}
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return fmt.Errorf("list databases: %w", err)
	}
	for _, name := range names {
		if err := dropDatabase(ctx, conn, name); err != nil {
			return err
		}
	}
	return nil
}
