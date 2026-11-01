// Package engines is the managed-database engine catalog: which engines and
// versions NebulaCtrl offers, how each one is configured and connected to, and
// the typed admin commands (rotate, info) the agent runs inside the container.
//
// It is a leaf package (no imports of other NebulaCtrl packages) so the service
// config, the resolver, the agent and the control plane can all share it.
package engines

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Engine identifiers.
const (
	Postgres   = "postgres"
	MySQL      = "mysql"
	MariaDB    = "mariadb"
	Valkey     = "valkey"
	Redis      = "redis"
	MongoDB    = "mongodb"
	ClickHouse = "clickhouse"
)

// Flavors.
const FlavorPostGIS = "postgis"

// Categories group engines in the create UI.
const (
	CategoryRelational = "relational"
	CategoryKeyValue   = "keyvalue"
	CategoryDocument   = "document"
	CategoryAnalytics  = "analytics"
)

// License classes shown as labels.
const (
	LicensePermissive      = "permissive"
	LicenseCopyleft        = "copyleft"
	LicenseSourceAvailable = "source-available"
)

// RotationMode says how a credential change reaches the running database.
type RotationMode string

const (
	// RotateExec changes the password inside the running database through the
	// agent (the image only reads its password variable on first init).
	RotateExec RotationMode = "exec"
	// RotateRedeploy changes the variable only: the engine reads its password
	// from the environment on every start, so the database is redeployed.
	RotateRedeploy RotationMode = "redeploy"
)

// Version is one selectable major version of an engine.
type Version struct {
	ID      string `json:"id"`      // "17"
	Tag     string `json:"tag"`     // image tag
	Label   string `json:"label"`   // "PostgreSQL 17"
	Default bool   `json:"default"` // preselected
	// Notes are shown next to the version (EOL, layout changes).
	Notes string `json:"notes,omitempty"`
}

// Flavor is an image variant of an engine (e.g. PostGIS).
type Flavor struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Image string `json:"image"`
	// Tags maps version id to the image tag of the flavor.
	Tags map[string]string `json:"tags"`
	Note string            `json:"note,omitempty"`
}

// EnvDefault is a variable the platform sets on every container of an engine
// from MinMajor on. It is not a service variable: it never appears in the
// Variables UI, and a variable of the service with the same key wins.
type EnvDefault struct {
	Key, Value string
	// MinMajor is the first major version the variable applies to (0 = all).
	MinMajor int
}

// Engine describes one database engine.
type Engine struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Description string `json:"description"`
	Category    string `json:"category"`
	Image       string `json:"image"`
	Port        int    `json:"port"`
	// Default marks the engine preselected in its category (Valkey for key-value).
	Default bool `json:"default"`
	// OptIn engines (source-available licences) are shown behind an
	// acknowledgement.
	OptIn        bool      `json:"optIn"`
	License      string    `json:"license"`      // SPDX-style id
	LicenseClass string    `json:"licenseClass"` // permissive | copyleft | source-available
	LicenseNote  string    `json:"licenseNote,omitempty"`
	Versions     []Version `json:"versions"`
	Flavors      []Flavor  `json:"flavors,omitempty"`
	// Rotation is how credentials are changed on a deployed database.
	Rotation RotationMode `json:"rotation"`

	// Credential variable names (engine-native, stored as service variables).
	UserKey, PasswordKey, RootPasswordKey, DatabaseKey string
	DefaultUser, DefaultDatabase                       string
	// SQLLike engines support DATABASE_* exports and a database name.
	SQLLike bool
	// Env is set on the container in addition to the service's variables.
	Env []EnvDefault `json:"-"`
}

var catalog = []Engine{
	{
		ID: Postgres, Label: "PostgreSQL", Category: CategoryRelational, Image: "postgres", Port: 5432, Rotation: RotateExec,
		Description: "The reference relational database.",
		License:     "PostgreSQL", LicenseClass: LicensePermissive,
		Versions: []Version{
			{ID: "18", Tag: "18", Label: "PostgreSQL 18", Default: true, Notes: "New on-disk layout: data lives under /var/lib/postgresql/18/docker."},
			{ID: "17", Tag: "17", Label: "PostgreSQL 17"},
			{ID: "16", Tag: "16", Label: "PostgreSQL 16"},
		},
		Flavors: []Flavor{{
			ID: FlavorPostGIS, Label: "PostGIS", Image: "postgis/postgis",
			Tags: map[string]string{"18": "18-3.6", "17": "17-3.5", "16": "16-3.5"},
			Note: "Geospatial extension (GPL-2.0 image from the PostGIS project).",
		}},
		UserKey: "POSTGRES_USER", PasswordKey: "POSTGRES_PASSWORD", DatabaseKey: "POSTGRES_DB",
		DefaultUser: "postgres", DefaultDatabase: "app", SQLLike: true,
	},
	{
		ID: MySQL, Label: "MySQL", Category: CategoryRelational, Image: "mysql", Port: 3306, Rotation: RotateExec,
		Description: "MySQL community server.",
		License:     "GPL-2.0", LicenseClass: LicenseCopyleft,
		Versions: []Version{
			{ID: "8.4", Tag: "8.4", Label: "MySQL 8.4 LTS", Default: true},
			{ID: "9.7", Tag: "9.7", Label: "MySQL 9.7", Notes: "Innovation release."},
		},
		UserKey: "MYSQL_USER", PasswordKey: "MYSQL_PASSWORD", RootPasswordKey: "MYSQL_ROOT_PASSWORD", DatabaseKey: "MYSQL_DATABASE",
		DefaultUser: "app", DefaultDatabase: "app", SQLLike: true,
	},
	{
		ID: MariaDB, Label: "MariaDB", Category: CategoryRelational, Image: "mariadb", Port: 3306, Rotation: RotateExec,
		Description: "MySQL-compatible community fork.",
		License:     "GPL-2.0", LicenseClass: LicenseCopyleft,
		Versions: []Version{
			{ID: "11.8", Tag: "11.8", Label: "MariaDB 11.8 LTS", Default: true},
			{ID: "12.3", Tag: "12.3", Label: "MariaDB 12.3"},
		},
		UserKey: "MARIADB_USER", PasswordKey: "MARIADB_PASSWORD", RootPasswordKey: "MARIADB_ROOT_PASSWORD", DatabaseKey: "MARIADB_DATABASE",
		DefaultUser: "app", DefaultDatabase: "app", SQLLike: true,
	},
	{
		ID: Valkey, Label: "Valkey", Category: CategoryKeyValue, Image: "valkey/valkey", Port: 6379, Rotation: RotateRedeploy, Default: true,
		Description: "Redis-compatible key-value store (Linux Foundation project).",
		License:     "BSD-3-Clause", LicenseClass: LicensePermissive,
		Versions: []Version{
			{ID: "9", Tag: "9", Label: "Valkey 9", Default: true},
			{ID: "8", Tag: "8", Label: "Valkey 8"},
		},
		PasswordKey: "VALKEY_PASSWORD",
	},
	{
		ID: Redis, Label: "Redis", Category: CategoryKeyValue, Image: "redis", Port: 6379, Rotation: RotateRedeploy,
		Description: "In-memory key-value store.",
		License:     "RSALv2 / SSPLv1 / AGPLv3", LicenseClass: LicenseSourceAvailable,
		LicenseNote: "Redis 8 is tri-licensed (RSALv2, SSPLv1, AGPLv3). Valkey is the BSD-licensed alternative.",
		Versions: []Version{
			{ID: "8", Tag: "8", Label: "Redis 8", Default: true},
		},
		PasswordKey: "REDIS_PASSWORD",
	},
	{
		ID: MongoDB, Label: "MongoDB", Category: CategoryDocument, Image: "mongo", Port: 27017, Rotation: RotateExec, OptIn: true,
		Description: "Document database.",
		License:     "SSPL-1.0", LicenseClass: LicenseSourceAvailable,
		LicenseNote: "MongoDB is licensed under the SSPL, which is not OSI-approved. Review the licence before offering it as a service.",
		Versions: []Version{
			{ID: "8.0", Tag: "8.0", Label: "MongoDB 8.0", Default: true},
			{ID: "7.0", Tag: "7.0", Label: "MongoDB 7.0"},
		},
		UserKey: "MONGO_INITDB_ROOT_USERNAME", PasswordKey: "MONGO_INITDB_ROOT_PASSWORD", DatabaseKey: "MONGO_INITDB_DATABASE",
		DefaultUser: "root", DefaultDatabase: "app", SQLLike: true,
		// The official image sets glibc.pthread.rseq=0 so TCMalloc registers rseq
		// itself and uses per-CPU caches; those crash mongod's startup check on
		// Linux 6.19-7.0.13 (https://jira.mongodb.org/browse/SERVER-121912).
		// With rseq=1 glibc owns the registration, TCMalloc falls back to
		// per-thread caches (the allocator of MongoDB < 8.0) and mongod starts
		// on every kernel.
		Env: []EnvDefault{{Key: "GLIBC_TUNABLES", Value: "glibc.pthread.rseq=1", MinMajor: 8}},
	},
	{
		ID: ClickHouse, Label: "ClickHouse", Category: CategoryAnalytics, Image: "clickhouse/clickhouse-server", Port: 9000, Rotation: RotateExec,
		Description: "Column-oriented analytics database.",
		License:     "Apache-2.0", LicenseClass: LicensePermissive,
		Versions: []Version{
			{ID: "26.8", Tag: "26.8", Label: "ClickHouse 26.8 LTS", Default: true},
			{ID: "25.8", Tag: "25.8", Label: "ClickHouse 25.8 LTS"},
		},
		UserKey: "CLICKHOUSE_USER", PasswordKey: "CLICKHOUSE_PASSWORD", DatabaseKey: "CLICKHOUSE_DB",
		DefaultUser: "app", DefaultDatabase: "app", SQLLike: true,
	},
}

// Catalog returns every engine in display order (copy-safe: callers must not
// mutate the nested slices).
func Catalog() []Engine { return catalog }

// IDs lists the engine identifiers.
func IDs() []string {
	out := make([]string, len(catalog))
	for i, e := range catalog {
		out[i] = e.ID
	}
	return out
}

// Lookup returns the engine with id, or nil.
func Lookup(id string) *Engine {
	for i := range catalog {
		if catalog[i].ID == id {
			return &catalog[i]
		}
	}
	return nil
}

// Version returns the version entry, or nil.
func (e *Engine) Version(id string) *Version {
	for i := range e.Versions {
		if e.Versions[i].ID == id {
			return &e.Versions[i]
		}
	}
	return nil
}

// DefaultVersion is the preselected version id.
func (e *Engine) DefaultVersion() string {
	for _, v := range e.Versions {
		if v.Default {
			return v.ID
		}
	}
	return e.Versions[0].ID
}

// Flavor returns the flavor entry, or nil.
func (e *Engine) Flavor(id string) *Flavor {
	for i := range e.Flavors {
		if e.Flavors[i].ID == id {
			return &e.Flavors[i]
		}
	}
	return nil
}

// HasUser reports whether the engine has a username (key-value engines do not).
func (e *Engine) HasUser() bool { return e.UserKey != "" }

// Major returns the numeric major of a version id ("17" → 17, "8.4" → 8).
func Major(version string) int {
	head := version
	if i := strings.IndexByte(head, '.'); i >= 0 {
		head = head[:i]
	}
	n, _ := strconv.Atoi(head)
	return n
}

// CompareVersions orders version ids numerically component by component.
func CompareVersions(a, b string) int {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(pa) || i < len(pb); i++ {
		var x, y int
		if i < len(pa) {
			x, _ = strconv.Atoi(pa[i])
		}
		if i < len(pb) {
			y, _ = strconv.Atoi(pb[i])
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}

// SortedVersionIDs returns the engine's version ids, oldest first.
func (e *Engine) SortedVersionIDs() []string {
	ids := make([]string, len(e.Versions))
	for i, v := range e.Versions {
		ids[i] = v.ID
	}
	sort.Slice(ids, func(i, j int) bool { return CompareVersions(ids[i], ids[j]) < 0 })
	return ids
}

var identRe = regexp.MustCompile(`^[a-z_][a-z0-9_]{0,62}$`)

// ValidIdent reports whether s is a safe database or user name.
func ValidIdent(s string) bool { return identRe.MatchString(s) }
