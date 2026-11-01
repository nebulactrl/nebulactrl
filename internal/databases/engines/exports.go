package engines

import (
	"net"
	"net/url"
	"strconv"
	"strings"
)

// Export is a derived connection variable of a database service, referenced as
// ${{<slug>.<Key>}}. Exports are computed, never stored.
type Export struct {
	Key         string
	Value       string
	Secret      bool
	Description string
}

// PrimaryExport is the export consumers normally reference.
func PrimaryExport(engine string) string {
	switch engine {
	case Valkey, Redis:
		return "REDIS_URL"
	case MongoDB:
		return "MONGO_URL"
	case ClickHouse:
		return "CLICKHOUSE_URL"
	case MySQL, MariaDB:
		return "DATABASE_URL"
	}
	return "DATABASE_URL"
}

// LinkPrefix is the variable-name prefix link proposals use for the engine.
func LinkPrefix(engine string) string {
	if engine == Valkey || engine == Redis {
		return "REDIS_"
	}
	return "DATABASE_"
}

// ExportKeys lists the export names of an engine in display order.
func ExportKeys(engine string) []string {
	exs := ExportList(engine)
	out := make([]string, len(exs))
	for i, e := range exs {
		out[i] = e.Key
	}
	return out
}

// ExportList lists the exports of an engine in display order with their
// descriptions and secrecy; values are placeholders. It is what a reference
// to the engine can name.
func ExportList(engine string) []Export {
	return Exports(Spec{Engine: engine}.Normalize(), "host", Spec{Engine: engine}.Port(), func(string) (string, bool) { return "x", true })
}

// Exports computes the derived connection variables of a database. host is the
// service's private hostname; v reads the database service's resolved user
// variables.
func Exports(s Spec, host string, port int, v func(key string) (string, bool)) []Export {
	e := Lookup(s.Engine)
	if e == nil {
		return nil
	}
	c := e.Creds(s, v)
	hostport := net.JoinHostPort(host, strconv.Itoa(port))
	portStr := strconv.Itoa(port)
	user := func() *url.Userinfo {
		if e.HasUser() {
			return url.UserPassword(c.User, c.Password)
		}
		return url.UserPassword("", c.Password)
	}
	build := func(scheme, db, query string) string {
		u := url.URL{Scheme: scheme, User: user(), Host: hostport, RawQuery: query}
		if db != "" {
			u.Path = "/" + db
		}
		return u.String()
	}
	plain := func(key, val, desc string) Export { return Export{Key: key, Value: val, Description: desc} }
	secret := func(key, val, desc string) Export {
		return Export{Key: key, Value: val, Secret: true, Description: desc}
	}
	generic := func() []Export {
		return []Export{
			plain("DATABASE_HOST", host, "Private hostname of the database."),
			plain("DATABASE_PORT", portStr, "Port the database listens on."),
			plain("DATABASE_NAME", c.Database, "Database name."),
			plain("DATABASE_USER", c.User, "Database user."),
			secret("DATABASE_PASSWORD", c.Password, "Database password."),
		}
	}

	var out []Export
	switch s.Engine {
	case Postgres:
		out = append(out, secret("DATABASE_URL", build("postgres", c.Database, ""), "Connection string for the private network."))
		out = append(out, generic()...)
		out = append(out,
			plain("PGHOST", host, "libpq host."), plain("PGPORT", portStr, "libpq port."),
			plain("PGUSER", c.User, "libpq user."), secret("PGPASSWORD", c.Password, "libpq password."),
			plain("PGDATABASE", c.Database, "libpq database."),
		)
		// A standalone database is its own writer and reader; the provider swaps
		// in the replica group's members (GroupExports) when there are any.
		self := GroupTarget{Host: host, Port: port}
		out = append(out, GroupExports(s, v, self, nil)...)
	case MySQL, MariaDB:
		conn := build("mysql", c.Database, "")
		out = append(out, secret("DATABASE_URL", conn, "Connection string for the private network."))
		out = append(out, generic()...)
		out = append(out, secret("MYSQL_URL", conn, "Alias of DATABASE_URL."))
	case MongoDB:
		conn := build("mongodb", c.Database, "authSource=admin")
		out = append(out, secret("MONGO_URL", conn, "Connection string for the private network."),
			secret("DATABASE_URL", conn, "Alias of MONGO_URL."))
		out = append(out, generic()...)
	case ClickHouse:
		conn := build("clickhouse", c.Database, "")
		out = append(out, secret("CLICKHOUSE_URL", conn, "Native-protocol connection string."),
			secret("DATABASE_URL", conn, "Alias of CLICKHOUSE_URL."))
		out = append(out, generic()...)
	case Valkey, Redis:
		conn := build("redis", "", "")
		out = append(out, secret("REDIS_URL", conn, "Connection string for the private network."))
		if s.Engine == Valkey {
			out = append(out, secret("VALKEY_URL", conn, "Alias of REDIS_URL."))
		}
		out = append(out,
			plain("REDIS_HOST", host, "Private hostname of the database."),
			plain("REDIS_PORT", portStr, "Port the database listens on."),
			secret("REDIS_PASSWORD", c.Password, "Database password."),
		)
	}
	return out
}

// GroupTarget is a member of a replica group as the group exports address it:
// the host a consumer dials (the member's slug) and the port that consumer
// reaches it on (the container port, or the mesh port across servers).
type GroupTarget struct {
	Host string
	Port int
}

// Keys of the writer/reader exports of a PostgreSQL replica group.
const (
	KeyWriteURL  = "DATABASE_WRITE_URL"
	KeyWriteHost = "DATABASE_WRITE_HOST"
	KeyWritePort = "DATABASE_WRITE_PORT"
	KeyReadURL   = "DATABASE_READ_URL"
	KeyReadHost  = "DATABASE_READ_HOST"
	KeyReadPort  = "DATABASE_READ_PORT"
	KeyReadHosts = "DATABASE_READ_HOSTS"
)

// GroupExports computes DATABASE_WRITE_* (the group's current primary) and
// DATABASE_READ_* (the replicas the consumer reaches) of a PostgreSQL service.
// With no readers the read exports fall back to the writer, so an application
// can be written against them before the first replica exists. Several readers
// give a libpq multi-host URL (postgres://u:p@h1:5432,h2:5432/db); drivers
// without multi-host support use DATABASE_READ_HOST. Credentials come from the
// service's own variables, which are the primary's for every member.
func GroupExports(s Spec, v func(key string) (string, bool), writer GroupTarget, readers []GroupTarget) []Export {
	e := Lookup(s.Engine)
	if e == nil || s.Engine != Postgres {
		return nil
	}
	c := e.Creds(s, v)
	fellBack := len(readers) == 0
	if fellBack {
		readers = []GroupTarget{writer}
	}
	conn := func(ts []GroupTarget) string {
		hosts := make([]string, len(ts))
		for i, t := range ts {
			hosts[i] = net.JoinHostPort(t.Host, strconv.Itoa(t.Port))
		}
		// Built by hand: url.URL would escape the commas of a multi-host authority.
		return "postgres://" + url.UserPassword(c.User, c.Password).String() + "@" + strings.Join(hosts, ",") + "/" + url.PathEscape(c.Database)
	}
	hosts := make([]string, len(readers))
	for i, t := range readers {
		hosts[i] = net.JoinHostPort(t.Host, strconv.Itoa(t.Port))
	}
	readNote := "The streaming read replicas the consumer reaches (those on its own server first); the writer while none is streaming."
	if !fellBack {
		readNote = "The streaming read replicas the consumer reaches (those on its own server first)."
	}
	return []Export{
		{Key: KeyWriteURL, Value: conn([]GroupTarget{writer}), Secret: true, Description: "Connection string of the group's current primary (the writer)."},
		{Key: KeyWriteHost, Value: writer.Host, Description: "Private hostname of the group's current primary."},
		{Key: KeyWritePort, Value: strconv.Itoa(writer.Port), Description: "Port of the group's current primary, as the consumer reaches it."},
		{Key: KeyReadURL, Value: conn(readers), Secret: true, Description: readNote + " Several replicas give a multi-host URL."},
		{Key: KeyReadHost, Value: readers[0].Host, Description: "Hostname of the first read replica (the writer while none is streaming)."},
		{Key: KeyReadPort, Value: strconv.Itoa(readers[0].Port), Description: "Port of the first read replica, as the consumer reaches it."},
		{Key: KeyReadHosts, Value: strings.Join(hosts, ","), Description: "Comma-separated host:port list of the read replicas (the writer while none is streaming)."},
	}
}
