package engines

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// ExecPlan is a command the agent runs inside a database container. It is an
// argv vector (never a shell string); credentials travel in Env and the
// statements on Stdin, so no secret appears in the process list.
type ExecPlan struct {
	Argv  []string
	Env   []string
	Stdin string
	// User is the exec user inside the container ("" = the container's user).
	// Only the fixed value "postgres" is ever set (replication scripts); the
	// agent refuses anything else.
	User string
}

// Environment variable names the plans use.
const (
	envNewPassword = "NEBULA_DB_NEW_PASSWORD"
	envUser        = "NEBULA_DB_USER"
	envAuthPass    = "NEBULA_DB_PASSWORD"
	envDatabase    = "NEBULA_DB_NAME"
)

var newPasswordRe = regexp.MustCompile(`^[A-Za-z0-9_-]{16,128}$`)

// ValidNewPassword reports whether pw is acceptable for an exec rotation
// (generated passwords are alphanumeric; this keeps every engine's quoting safe).
func ValidNewPassword(pw string) bool { return newPasswordRe.MatchString(pw) }

// ErrNoExecRotation is returned for engines that rotate through a redeploy.
var ErrNoExecRotation = errors.New("engine rotates credentials through a redeploy")

// RotatePlan builds the command that changes the application user's password
// inside the running database. cur holds the current credentials.
func RotatePlan(s Spec, cur Credentials, newPassword string) (ExecPlan, error) {
	e := Lookup(s.Engine)
	if e == nil {
		return ExecPlan{}, fmt.Errorf("unknown engine %q", s.Engine)
	}
	if e.Rotation != RotateExec {
		return ExecPlan{}, ErrNoExecRotation
	}
	if !ValidNewPassword(newPassword) {
		return ExecPlan{}, errors.New("new password must be 16-128 characters of [A-Za-z0-9_-]")
	}
	if !ValidIdent(cur.User) {
		return ExecPlan{}, errors.New("invalid database user")
	}
	switch s.Engine {
	case Postgres:
		// psql reads the values with \getenv, so nothing is interpolated into SQL.
		return ExecPlan{
			Argv: []string{"psql", "-X", "-q", "-v", "ON_ERROR_STOP=1", "-U", cur.User, "-d", cur.Database},
			Env:  []string{"PGPASSWORD=" + cur.Password, envUser + "=" + cur.User, envNewPassword + "=" + newPassword},
			Stdin: "\\getenv nebula_role " + envUser + "\n\\getenv nebula_pw " + envNewPassword + "\n" +
				"ALTER ROLE :\"nebula_role\" WITH PASSWORD :'nebula_pw';\n",
		}, nil
	case MySQL, MariaDB:
		client := "mysql"
		if s.Engine == MariaDB {
			client = "mariadb"
		}
		return ExecPlan{
			Argv:  []string{client, "-u", "root", "--batch", "--silent"},
			Env:   []string{"MYSQL_PWD=" + cur.RootPassword},
			Stdin: fmt.Sprintf("ALTER USER '%s'@'%%' IDENTIFIED BY '%s';\n", cur.User, newPassword),
		}, nil
	case MongoDB:
		return ExecPlan{
			Argv: []string{"mongosh", "--quiet", "--norc"},
			Env:  []string{envUser + "=" + cur.User, envAuthPass + "=" + cur.Password, envNewPassword + "=" + newPassword},
			Stdin: `const a = db.getSiblingDB("admin");` + "\n" +
				`a.auth(process.env.` + envUser + `, process.env.` + envAuthPass + `);` + "\n" +
				`a.changeUserPassword(process.env.` + envUser + `, process.env.` + envNewPassword + `);` + "\n",
		}, nil
	case ClickHouse:
		return ExecPlan{
			Argv:  []string{"clickhouse-client", "--user", cur.User},
			Env:   []string{"CLICKHOUSE_PASSWORD=" + cur.Password},
			Stdin: fmt.Sprintf("ALTER USER %s IDENTIFIED BY '%s'\n", cur.User, newPassword),
		}, nil
	}
	return ExecPlan{}, ErrNoExecRotation
}

// validNames checks the user and database names of c, which come from service
// variables an operator can edit, before they reach an argv or a script.
func validNames(c Credentials) error {
	if !ValidIdent(c.User) {
		return errors.New("invalid database user")
	}
	if !ValidIdent(c.Database) {
		return errors.New("invalid database name")
	}
	return nil
}

// Info is a snapshot of a running database.
type Info struct {
	Version     string `json:"version"`
	SizeBytes   int64  `json:"sizeBytes"`
	Connections int    `json:"connections"`
}

// InfoPlan builds the command that reports the database's version, size and
// open connections.
func InfoPlan(s Spec, c Credentials) (ExecPlan, error) {
	switch s.Engine {
	case Postgres:
		// The user and database come from service variables and reach psql's argv.
		if err := validNames(c); err != nil {
			return ExecPlan{}, err
		}
		return ExecPlan{
			Argv: []string{"psql", "-X", "-At", "-F", "|", "-v", "ON_ERROR_STOP=1", "-U", c.User, "-d", c.Database},
			Env:  []string{"PGPASSWORD=" + c.Password},
			Stdin: "SELECT current_setting('server_version') || '|' || pg_database_size(current_database()) || '|' || " +
				"(SELECT count(*) FROM pg_stat_activity WHERE datname = current_database());\n",
		}, nil
	case MySQL, MariaDB:
		client := "mysql"
		if s.Engine == MariaDB {
			client = "mariadb"
		}
		return ExecPlan{
			Argv: []string{client, "-u", "root", "--batch", "--silent", "--skip-column-names"},
			Env:  []string{"MYSQL_PWD=" + c.RootPassword},
			Stdin: "SELECT CONCAT_WS('|', VERSION(), " +
				"(SELECT COALESCE(SUM(data_length + index_length), 0) FROM information_schema.tables WHERE table_schema = " + mysqlLiteral(c.Database) + "), " +
				"(SELECT COUNT(*) FROM information_schema.processlist));\n",
		}, nil
	case Valkey, Redis:
		cli := "valkey-cli"
		if s.Engine == Redis {
			cli = "redis-cli"
		}
		return ExecPlan{Argv: []string{cli, "--no-auth-warning", "INFO"}, Env: []string{"REDISCLI_AUTH=" + c.Password}}, nil
	case MongoDB:
		if err := validNames(c); err != nil {
			return ExecPlan{}, err
		}
		return ExecPlan{
			Argv: []string{"mongosh", "--quiet", "--norc"},
			Env:  []string{envUser + "=" + c.User, envAuthPass + "=" + c.Password, envDatabase + "=" + c.Database},
			Stdin: `const a = db.getSiblingDB("admin");` + "\n" +
				`a.auth(process.env.` + envUser + `, process.env.` + envAuthPass + `);` + "\n" +
				`const st = db.getSiblingDB(process.env.` + envDatabase + `).stats();` + "\n" +
				`print(JSON.stringify({version: db.version(), size: (st.dataSize || 0) + (st.indexSize || 0), conns: a.serverStatus().connections.current}));` + "\n",
		}, nil
	case ClickHouse:
		if !ValidIdent(c.User) {
			return ExecPlan{}, errors.New("invalid database user")
		}
		return ExecPlan{
			Argv: []string{"clickhouse-client", "--user", c.User, "--format", "TSVRaw"},
			Env:  []string{"CLICKHOUSE_PASSWORD=" + c.Password},
			Stdin: "SELECT concat(version(), '|', toString((SELECT coalesce(sum(bytes_on_disk), 0) FROM system.parts WHERE active AND database = currentDatabase())), " +
				"'|', toString((SELECT count() FROM system.processes)))\n",
		}, nil
	}
	return ExecPlan{}, fmt.Errorf("unknown engine %q", s.Engine)
}

// ParseInfo parses the output of an InfoPlan command.
func ParseInfo(engine, out string) (Info, error) {
	out = strings.TrimSpace(out)
	switch engine {
	case Postgres, MySQL, MariaDB, ClickHouse:
		line := out
		if i := strings.LastIndexByte(out, '\n'); i >= 0 {
			line = out[i+1:]
		}
		parts := strings.Split(strings.TrimSpace(line), "|")
		if len(parts) != 3 {
			return Info{}, fmt.Errorf("unexpected info output %q", truncate(line))
		}
		size, err1 := strconv.ParseInt(strings.TrimSpace(parts[1]), 10, 64)
		conns, err2 := strconv.Atoi(strings.TrimSpace(parts[2]))
		if err1 != nil || err2 != nil {
			return Info{}, fmt.Errorf("unexpected info output %q", truncate(line))
		}
		return Info{Version: shortVersion(parts[0]), SizeBytes: size, Connections: conns}, nil
	case Valkey, Redis:
		var info Info
		var found bool
		for _, l := range strings.Split(out, "\n") {
			k, v, ok := strings.Cut(strings.TrimSpace(l), ":")
			if !ok {
				continue
			}
			switch k {
			case "valkey_version":
				info.Version = v
				found = true
			case "redis_version":
				if info.Version == "" {
					info.Version = v
				}
				found = true
			case "used_memory":
				info.SizeBytes, _ = strconv.ParseInt(v, 10, 64)
			case "connected_clients":
				info.Connections, _ = strconv.Atoi(v)
			}
		}
		if !found {
			return Info{}, fmt.Errorf("unexpected info output %q", truncate(out))
		}
		return info, nil
	case MongoDB:
		line := out
		if i := strings.LastIndexByte(out, '\n'); i >= 0 {
			line = out[i+1:]
		}
		var raw struct {
			Version string  `json:"version"`
			Size    float64 `json:"size"`
			Conns   int     `json:"conns"`
		}
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			return Info{}, fmt.Errorf("unexpected info output %q", truncate(line))
		}
		return Info{Version: raw.Version, SizeBytes: int64(raw.Size), Connections: raw.Conns}, nil
	}
	return Info{}, fmt.Errorf("unknown engine %q", engine)
}

// mysqlLiteral quotes s as a MySQL string literal. The database name comes from
// a service variable, so it is escaped rather than trusted.
func mysqlLiteral(s string) string {
	return "'" + strings.NewReplacer(`\`, `\\`, `'`, `''`).Replace(s) + "'"
}

func truncate(s string) string {
	if len(s) > 120 {
		return s[:120] + "…"
	}
	return s
}

// shortVersion trims distribution suffixes: "17.6 (Debian 17.6-1.pgdg120+1)" → "17.6".
func shortVersion(v string) string {
	v = strings.TrimSpace(v)
	if i := strings.IndexByte(v, ' '); i > 0 {
		v = v[:i]
	}
	return v
}
