package engines

import (
	"fmt"
	"strings"

	"github.com/google/uuid"
)

// Spec is the database section of a service config (services.config.database).
// Image, port, start command, health check and data volume of a managed
// database are all derived from it; they are not edited separately.
type Spec struct {
	Engine       string `json:"engine"`
	Version      string `json:"version"`
	Flavor       string `json:"flavor,omitempty"`
	DatabaseName string `json:"databaseName,omitempty"`
	Username     string `json:"username,omitempty"`
	// Replica is set on a PostgreSQL read replica: the service clones and
	// follows another service of its environment. It is release-snapshotted
	// and drives the start command; nil on every standalone database and on
	// every primary. The group topology and lifecycle live in database_replicas.
	Replica *ReplicaSpec `json:"replica,omitempty"`
}

// ReplicaSpec is the replica section of a Spec (services.config.database.replica).
// It holds no secret: the replication password is the service variable
// POSTGRES_REPLICATION_PASSWORD.
type ReplicaSpec struct {
	// PrimaryServiceID is the service the replica follows.
	PrimaryServiceID string `json:"primaryServiceId"`
	// Host and Port are what the replica dials: the primary's slug and port on
	// the same server, or the replication link alias (mesh-<id8>) and its mesh
	// port across servers.
	Host string `json:"host"`
	Port int    `json:"port"`
	// Slot is the physical replication slot and the replica's application_name
	// (nc_r_<replicaId8>); User is its replication role (nebula_r_<replicaId8>).
	Slot string `json:"slot"`
	User string `json:"user"`
}

// ReplicationPasswordKey is the variable that holds a replica's replication
// password; the replica's start command hands it to the walreceiver as PGPASSWORD.
const ReplicationPasswordKey = "POSTGRES_REPLICATION_PASSWORD"

// Validate checks the replica section for an engine.
func (r ReplicaSpec) Validate(engine string) error {
	if engine != Postgres {
		return fmt.Errorf("read replicas are only supported for PostgreSQL")
	}
	if _, err := uuid.Parse(r.PrimaryServiceID); err != nil {
		return fmt.Errorf("replica.primaryServiceId must be a service id")
	}
	if !ValidReplHost(r.Host) {
		return fmt.Errorf("replica.host is not a valid host name")
	}
	if r.Port < 1 || r.Port > 65535 {
		return fmt.Errorf("replica.port must be 1-65535")
	}
	if !ValidIdent(r.Slot) || !ValidIdent(r.User) {
		return fmt.Errorf("replica.slot and replica.user must match ^[a-z_][a-z0-9_]{0,62}$")
	}
	return nil
}

// Normalize fills defaults (version, database name, user name) without
// validating.
func (s Spec) Normalize() Spec {
	e := Lookup(s.Engine)
	if e == nil {
		return s
	}
	if s.Version == "" {
		s.Version = e.DefaultVersion()
	}
	if e.DatabaseKey != "" && s.DatabaseName == "" {
		s.DatabaseName = e.DefaultDatabase
	}
	if e.HasUser() && s.Username == "" {
		s.Username = e.DefaultUser
	}
	return s
}

// Validate checks a normalized spec against the catalog.
func (s Spec) Validate() error {
	e := Lookup(s.Engine)
	if e == nil {
		return fmt.Errorf("unknown database engine %q (one of %s)", s.Engine, strings.Join(IDs(), ", "))
	}
	if e.Version(s.Version) == nil {
		return fmt.Errorf("%s version %q is not offered (one of %s)", e.Label, s.Version, strings.Join(e.SortedVersionIDs(), ", "))
	}
	if s.Flavor != "" {
		f := e.Flavor(s.Flavor)
		if f == nil {
			return fmt.Errorf("%s has no flavor %q", e.Label, s.Flavor)
		}
		if _, ok := f.Tags[s.Version]; !ok {
			return fmt.Errorf("flavor %s is not available for %s %s", f.Label, e.Label, s.Version)
		}
	}
	if e.DatabaseKey != "" && !ValidIdent(s.DatabaseName) {
		return fmt.Errorf("databaseName must match ^[a-z_][a-z0-9_]{0,62}$")
	}
	if !e.HasUser() && s.Username != "" {
		return fmt.Errorf("%s has no user name", e.Label)
	}
	if e.HasUser() && !ValidIdent(s.Username) {
		return fmt.Errorf("username must match ^[a-z_][a-z0-9_]{0,62}$")
	}
	if e.DatabaseKey == "" && s.DatabaseName != "" {
		return fmt.Errorf("%s has no database name", e.Label)
	}
	if s.Replica != nil {
		if err := s.Replica.Validate(s.Engine); err != nil {
			return err
		}
	}
	return nil
}

// Image returns the image reference of the spec (a major-version tag; the
// release records the resolved digest).
func (s Spec) Image() string {
	e := Lookup(s.Engine)
	if e == nil {
		return ""
	}
	if s.Flavor != "" {
		if f := e.Flavor(s.Flavor); f != nil {
			return f.Image + ":" + f.Tags[s.Version]
		}
	}
	if v := e.Version(s.Version); v != nil {
		return e.Image + ":" + v.Tag
	}
	return ""
}

// Port is the engine's container port.
func (s Spec) Port() int {
	if e := Lookup(s.Engine); e != nil {
		return e.Port
	}
	return 0
}

// StartCommand is the container command, or nil for the image default.
// Key-value engines read their password from the environment on every start,
// which needs a shell for expansion; the official entrypoint still drops
// privileges because it is invoked with the server binary as its first argument.
// A PostgreSQL read replica starts through a guard that refuses an empty data
// directory (the stock entrypoint would initdb a fresh, non-replicating
// primary) and hands the replication password to the walreceiver.
func (s Spec) StartCommand() []string {
	switch s.Engine {
	case Valkey:
		return []string{"sh", "-c", `exec docker-entrypoint.sh valkey-server --requirepass "$VALKEY_PASSWORD" --appendonly yes`}
	case Redis:
		return []string{"sh", "-c", `exec docker-entrypoint.sh redis-server --requirepass "$REDIS_PASSWORD" --appendonly yes`}
	case Postgres:
		if s.Replica != nil {
			return ReplicaStartCommand()
		}
	}
	return nil
}

// DataMount returns the name and container path of the data volume. PostgreSQL 18
// moved the data directory below /var/lib/postgresql (PGDATA=.../18/docker), so
// the volume is mounted one level higher there.
func (s Spec) DataMount() (volume, mountPath string) {
	switch s.Engine {
	case Postgres:
		if Major(s.Version) >= 18 {
			return "data", "/var/lib/postgresql"
		}
		return "data", "/var/lib/postgresql/data"
	case MySQL, MariaDB:
		return "data", "/var/lib/mysql"
	case Valkey, Redis:
		return "data", "/data"
	case MongoDB:
		return "data", "/data/db"
	case ClickHouse:
		return "data", "/var/lib/clickhouse"
	}
	return "data", "/data"
}

// EnvVar is a variable a new database is created with.
type EnvVar struct {
	Key    string
	Value  string // literal value; empty with Generate means "generate a password"
	Secret bool
	// Generate marks a value created by the control plane once at creation.
	Generate bool
}

// EnvDefaults lists the engine-native variables of a new database in order.
func (s Spec) EnvDefaults() []EnvVar {
	e := Lookup(s.Engine)
	if e == nil {
		return nil
	}
	var out []EnvVar
	if e.RootPasswordKey != "" {
		out = append(out, EnvVar{Key: e.RootPasswordKey, Secret: true, Generate: true})
	}
	if e.HasUser() {
		out = append(out, EnvVar{Key: e.UserKey, Value: s.Username})
	}
	out = append(out, EnvVar{Key: e.PasswordKey, Secret: true, Generate: true})
	if e.DatabaseKey != "" {
		out = append(out, EnvVar{Key: e.DatabaseKey, Value: s.DatabaseName})
	}
	if s.Engine == ClickHouse {
		out = append(out, EnvVar{Key: "CLICKHOUSE_DEFAULT_ACCESS_MANAGEMENT", Value: "1"})
	}
	return out
}

// ContainerEnv lists the variables the platform sets on the database
// container for this version on top of the service's own variables, which
// override them (see EnvDefault).
func (s Spec) ContainerEnv() map[string]string {
	e := Lookup(s.Engine)
	if e == nil {
		return nil
	}
	major := Major(s.Normalize().Version)
	var out map[string]string
	for _, d := range e.Env {
		if major < d.MinMajor {
			continue
		}
		if out == nil {
			out = map[string]string{}
		}
		out[d.Key] = d.Value
	}
	return out
}

// CredentialKeys are the variable names holding credentials; the Variables UI
// marks them engine-bound and rotation goes through the database panel.
// PostgreSQL also lists the replication password, which only replicas use.
func (e *Engine) CredentialKeys() []string {
	var out []string
	for _, k := range []string{e.UserKey, e.PasswordKey, e.RootPasswordKey, e.DatabaseKey} {
		if k != "" {
			out = append(out, k)
		}
	}
	if e.ID == Postgres {
		out = append(out, ReplicationPasswordKey)
	}
	return out
}

// Credentials are a database's resolved connection credentials.
type Credentials struct {
	User         string
	Password     string
	RootPassword string
	Database     string
}

// Creds reads the credentials from the service's resolved user variables,
// falling back to the spec for the user and database names.
func (e *Engine) Creds(s Spec, v func(key string) (string, bool)) Credentials {
	get := func(key, def string) string {
		if key == "" {
			return def
		}
		if val, ok := v(key); ok && val != "" {
			return val
		}
		return def
	}
	c := Credentials{
		User:         get(e.UserKey, s.Username),
		Password:     get(e.PasswordKey, ""),
		RootPassword: get(e.RootPasswordKey, ""),
		Database:     get(e.DatabaseKey, s.DatabaseName),
	}
	if c.User == "" {
		c.User = e.DefaultUser
	}
	if c.Database == "" {
		c.Database = e.DefaultDatabase
	}
	return c
}

// IsCredentialKey reports whether key is one of the engine's credential
// variables (user, password, root password, database name). Once a database has
// been deployed these must not be edited as plain variables: the image reads
// them only when it initialises an empty data directory, so changing them would
// desynchronise the database and its clients. Passwords change through
// credential rotation.
func (s Spec) IsCredentialKey(key string) bool {
	e := Lookup(s.Engine)
	if e == nil {
		return false
	}
	for _, k := range e.CredentialKeys() {
		if k == key {
			return true
		}
	}
	return false
}
