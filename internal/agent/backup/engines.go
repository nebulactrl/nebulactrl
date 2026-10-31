package backup

import (
	"fmt"
	"net/url"
	"regexp"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// identRe restricts database and user names passed as argv/URI parts: no
// leading '-' (option injection), no shell or URI metacharacters.
var identRe = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.$-]{0,62}$`)

func checkIdent(what, v string) error {
	if !identRe.MatchString(v) {
		return fmt.Errorf("invalid %s %q", what, v)
	}
	return nil
}

// dumpSpec is the fixed, typed dump command of an engine. It is an argv vector
// (no shell); credentials go through the exec's Env, never argv, except for
// MongoDB whose tools only accept them as flags or URI.
type dumpSpec struct {
	argv   []string
	env    []string
	gzip   bool // compress the stream before storing (the tool does not)
	format string
	tool   string
}

// restoreSpec is the fixed restore command of an engine; the dump arrives on stdin.
type restoreSpec struct {
	argv   []string
	env    []string
	gunzip bool
	tool   string
	// scratch: argv restores into a scratch database the runner creates from
	// template0 and swaps in afterwards (PostgreSQL), see pgScratch.
	scratch bool
}

func dumpFor(t agentprotocol.BackupTarget, secrets map[string]string) (dumpSpec, error) {
	pw := secrets[agentprotocol.SecretDBPassword]
	switch t.Engine {
	case "postgres":
		if err := checkIdent("database", t.Database); err != nil {
			return dumpSpec{}, err
		}
		if err := checkIdent("user", t.User); err != nil {
			return dumpSpec{}, err
		}
		return dumpSpec{
			tool: "pg_dump", format: agentprotocol.FormatPGCustom,
			argv: []string{"pg_dump", "--format=custom", "--no-owner", "--no-acl", "--username", t.User, "--dbname", t.Database},
			env:  []string{"PGPASSWORD=" + pw},
		}, nil
	case "mysql", "mariadb":
		if err := checkIdent("database", t.Database); err != nil {
			return dumpSpec{}, err
		}
		if err := checkIdent("user", t.User); err != nil {
			return dumpSpec{}, err
		}
		tool, format := "mysqldump", agentprotocol.FormatMySQLGz
		if t.Engine == "mariadb" {
			tool, format = "mariadb-dump", agentprotocol.FormatMariaDBGz
		}
		return dumpSpec{
			tool: tool, format: format, gzip: true,
			argv: []string{tool, "--single-transaction", "--quick", "--no-tablespaces", "--routines", "--events", "--triggers", "-u", t.User, t.Database},
			env:  []string{"MYSQL_PWD=" + pw},
		}, nil
	case "mongodb":
		if err := checkIdent("user", t.User); err != nil {
			return dumpSpec{}, err
		}
		return dumpSpec{
			tool: "mongodump", format: agentprotocol.FormatMongoArchive,
			argv: []string{"mongodump", "--archive", "--gzip", "--quiet", "--uri", mongoURI(t.User, pw)},
		}, nil
	case "valkey", "redis":
		tool := "valkey-cli"
		if t.Engine == "redis" {
			tool = "redis-cli"
		}
		argv := []string{tool, "--rdb", "-"}
		if t.User != "" && t.User != "default" {
			if err := checkIdent("user", t.User); err != nil {
				return dumpSpec{}, err
			}
			argv = []string{tool, "--user", t.User, "--rdb", "-"}
		}
		var env []string
		if pw != "" {
			env = []string{"REDISCLI_AUTH=" + pw, "VALKEYCLI_AUTH=" + pw}
		}
		return dumpSpec{tool: tool, format: agentprotocol.FormatRDB, argv: argv, env: env}, nil
	}
	return dumpSpec{}, fmt.Errorf("backups are not supported for engine %q", t.Engine)
}

// restoreFor builds the restore command of a payload. PostgreSQL restores into
// an empty scratch database whatever the mode: a dump creates its own
// extensions and schemas, so the target database, which an image such as
// PostGIS has already filled with them, can not take it as it is. Clean is
// therefore not needed for PostgreSQL: the replaced data goes with the swap.
func restoreFor(p agentprotocol.DBRestorePayload, secrets map[string]string) (restoreSpec, error) {
	t, format, clean := p.Target, p.Format, p.Clean
	pw := secrets[agentprotocol.SecretDBPassword]
	switch t.Engine {
	case "postgres":
		if format != agentprotocol.FormatPGCustom {
			return restoreSpec{}, fmt.Errorf("a %s dump cannot be restored into postgres", format)
		}
		if err := checkIdent("database", t.Database); err != nil {
			return restoreSpec{}, err
		}
		if err := checkIdent("user", t.User); err != nil {
			return restoreSpec{}, err
		}
		argv := []string{"pg_restore", "--no-owner", "--no-acl", "--exit-on-error", "--username", t.User, "--dbname", namesFor(p.RestoreID, t.Database).scratch}
		return restoreSpec{tool: "pg_restore", argv: argv, env: []string{"PGPASSWORD=" + pw}, scratch: true}, nil
	case "mysql", "mariadb":
		tool := "mysql"
		want := agentprotocol.FormatMySQLGz
		if t.Engine == "mariadb" {
			tool, want = "mariadb", agentprotocol.FormatMariaDBGz
		}
		// Dumps are interchangeable between the two; accept either.
		if format != agentprotocol.FormatMySQLGz && format != agentprotocol.FormatMariaDBGz {
			return restoreSpec{}, fmt.Errorf("a %s dump cannot be restored into %s (want %s)", format, t.Engine, want)
		}
		if err := checkIdent("database", t.Database); err != nil {
			return restoreSpec{}, err
		}
		if err := checkIdent("user", t.User); err != nil {
			return restoreSpec{}, err
		}
		return restoreSpec{
			tool: tool, gunzip: true,
			argv: []string{tool, "--binary-mode", "-u", t.User, t.Database},
			env:  []string{"MYSQL_PWD=" + pw},
		}, nil
	case "mongodb":
		if format != agentprotocol.FormatMongoArchive {
			return restoreSpec{}, fmt.Errorf("a %s dump cannot be restored into mongodb", format)
		}
		if err := checkIdent("user", t.User); err != nil {
			return restoreSpec{}, err
		}
		argv := []string{"mongorestore", "--archive", "--gzip", "--quiet", "--uri", mongoURI(t.User, pw)}
		if clean {
			argv = append(argv, "--drop")
		}
		return restoreSpec{tool: "mongorestore", argv: argv}, nil
	}
	return restoreSpec{}, fmt.Errorf("restoring %s dumps is not supported", t.Engine)
}

// mongoURI authenticates against the admin database on the container's own
// loopback; the credentials are URL-escaped.
func mongoURI(user, pw string) string {
	u := url.URL{Scheme: "mongodb", Host: "127.0.0.1:27017", Path: "/", RawQuery: "authSource=admin"}
	if pw != "" {
		u.User = url.UserPassword(user, pw)
	} else {
		u.User = url.User(user)
	}
	return u.String()
}
