package coolify

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/nebulactrl/nebulactrl/internal/databases/engines"
)

// imageRepoTag splits an image reference into a normalised repository (Docker
// Hub prefixes and library/ removed, lower case) and its tag; a digest is dropped.
func imageRepoTag(ref string) (repo, tag string) {
	ref = strings.TrimSpace(ref)
	if i := strings.IndexByte(ref, '@'); i >= 0 {
		ref = ref[:i]
	}
	last := strings.LastIndexByte(ref, '/')
	if i := strings.LastIndexByte(ref, ':'); i > last {
		ref, tag = ref[:i], ref[i+1:]
	}
	for _, p := range []string{"docker.io/", "index.docker.io/", "registry-1.docker.io/"} {
		ref = strings.TrimPrefix(ref, p)
	}
	ref = strings.TrimPrefix(ref, "library/")
	return strings.ToLower(ref), tag
}

// engineImages maps the repository of a database image to its managed engine.
var engineImages = map[string]struct{ engine, flavor string }{
	"postgres":              {engines.Postgres, ""},
	"postgis/postgis":       {engines.Postgres, engines.FlavorPostGIS},
	"mysql":                 {engines.MySQL, ""},
	"mariadb":               {engines.MariaDB, ""},
	"mongo":                 {engines.MongoDB, ""},
	"redis":                 {engines.Redis, ""},
	"valkey/valkey":         {engines.Valkey, ""},
	"valkey":                {engines.Valkey, ""},
	"eqalpha/keydb":         {engines.Valkey, ""},
	"dragonflydb/dragonfly": {engines.Valkey, ""},
	"docker.dragonflydb.io/dragonflydb/dragonfly": {engines.Valkey, ""},
	"clickhouse/clickhouse-server":                {engines.ClickHouse, ""},
	"clickhouse":                                  {engines.ClickHouse, ""},
	"yandex/clickhouse-server":                    {engines.ClickHouse, ""},
}

// classifyDatabase decides which managed engine (and flavor) replaces a Coolify
// database. The engine is empty, with a reason, when there is none.
func classifyDatabase(d *Database) (engine, flavor, reason string) {
	if strings.HasSuffix(d.Type, "sqlite") {
		return "", "", "SQLite databases are files inside an application's volume; there is no server to create. Keep the file on the application's volume."
	}
	repo, _ := imageRepoTag(d.Image)
	if e, ok := engineImages[repo]; ok {
		return e.engine, e.flavor, ""
	}
	if d.Image == "" {
		switch {
		case strings.HasSuffix(d.Type, "postgresql"):
			return engines.Postgres, "", ""
		case strings.HasSuffix(d.Type, "mysql"):
			return engines.MySQL, "", ""
		case strings.HasSuffix(d.Type, "mariadb"):
			return engines.MariaDB, "", ""
		case strings.HasSuffix(d.Type, "mongodb"):
			return engines.MongoDB, "", ""
		case strings.HasSuffix(d.Type, "redis"), strings.HasSuffix(d.Type, "keydb"), strings.HasSuffix(d.Type, "dragonfly"):
			return engines.Valkey, "", ""
		case strings.HasSuffix(d.Type, "clickhouse"):
			return engines.ClickHouse, "", ""
		}
	}
	return "", "", fmt.Sprintf("The image %s has no managed equivalent (managed databases use the official images). Import it as an image service with the same image, or run it outside NebulaCtrl.", d.Image)
}

var tagVersionRe = regexp.MustCompile(`^v?(\d+)(?:\.(\d+))?`)

// resolveVersion maps the tag Coolify runs to a version of the managed
// catalog: the same version when it is offered, otherwise the closest one that
// is not older (a logical dump restores into a newer major). It reports every
// difference as a note. Newer than the catalog is an error.
func resolveVersion(e *engines.Engine, flavor, tag, override string) (version string, notes []string, err error) {
	if override != "" {
		if e.Version(override) == nil {
			return "", nil, fmt.Errorf("%s %s is not offered as a managed database (available: %s)", e.Label, override, strings.Join(e.SortedVersionIDs(), ", "))
		}
		return override, []string{"The version was chosen in the import: " + e.Label + " " + override + "."}, nil
	}
	if strings.Contains(tag, "alpine") {
		msg := "The Coolify image is an Alpine variant; the managed database uses the Debian image."
		if e.SQLLike {
			msg += " Behavior is the same; collation order can differ (musl vs glibc), which a dump/restore rebuilds."
		}
		notes = append(notes, msg)
	}
	m := tagVersionRe.FindStringSubmatch(tag)
	if m == nil {
		return e.DefaultVersion(), append(notes, fmt.Sprintf("The Coolify tag %q does not name a version; the managed database uses %s %s.", tag, e.Label, e.DefaultVersion())), nil
	}
	want := m[1]
	if m[2] != "" {
		want += "." + m[2]
	}
	major := engines.Major(want)
	var exact, sameMajor, higher string
	for _, id := range e.SortedVersionIDs() {
		switch {
		case id == want, !strings.Contains(id, ".") && engines.Major(id) == major:
			exact = id
		case engines.Major(id) == major:
			if engines.CompareVersions(id, want) >= 0 && sameMajor == "" {
				sameMajor = id
			}
		case engines.Major(id) > major && higher == "":
			higher = id
		}
	}
	pick := firstString(exact, sameMajor, higher)
	if pick == "" {
		// a same-major version with a lower minor than asked is the best older match
		for _, id := range e.SortedVersionIDs() {
			if engines.Major(id) == major {
				pick = id
			}
		}
	}
	if pick == "" {
		return "", nil, fmt.Errorf("%s %s is newer than the versions offered as managed databases (%s)", e.Label, want, strings.Join(e.SortedVersionIDs(), ", "))
	}
	if engines.Major(pick) != major || (strings.Contains(pick, ".") && pick != want) {
		notes = append(notes, fmt.Sprintf("Coolify runs %s %s; the managed database uses %s %s. Restoring a dump into a newer version works; plan an upgrade test before the cutover.", e.Label, want, e.Label, pick))
	}
	if flavor != "" {
		if fl := e.Flavor(flavor); fl != nil {
			if want, got := postgisSuffix(tag), strings.TrimPrefix(fl.Tags[pick], pick+"-"); want != "" && got != "" && want != got {
				notes = append(notes, fmt.Sprintf("Coolify runs PostGIS %s; the managed image ships PostGIS %s. Check your extensions (CREATE EXTENSION ... UPDATE) after the restore.", want, got))
			}
		}
	}
	return pick, notes, nil
}

var postgisTagRe = regexp.MustCompile(`^\d+(?:\.\d+)?-(\d+\.\d+)`)

func postgisSuffix(tag string) string {
	if m := postgisTagRe.FindStringSubmatch(tag); m != nil {
		return m[1]
	}
	return ""
}

func firstString(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

// planDatabase maps one Coolify database.
func (a *analyzer) planDatabase(d *Database, rc CoolifyResourceChoice) CoolifyPlanResource {
	name := d.Name
	if rc.Name != nil && strings.TrimSpace(*rc.Name) != "" {
		name = strings.TrimSpace(*rc.Name)
	}
	res := CoolifyPlanResource{
		UUID: d.UUID, Kind: "database", Name: name, Include: true, dbRec: d, choice: rc, hosts: []string{strings.ToLower(d.UUID)},
		Domains: []CoolifyPlanDomain{}, Volumes: []CoolifyPlanVolume{}, Env: []CoolifyPlanEnv{}, DependsOn: []string{}, Unsupported: []string{}, Notes: []CoolifyNote{},
		NeedsUnrestricted: []string{},
	}
	engine, flavor, reason := classifyDatabase(d)
	if engine == "" {
		res.Include, res.IncludeNote = false, reason
		res.Source = CoolifyPlanSource{Type: "managed"}
		return res
	}
	e := engines.Lookup(engine)
	res.engine = engine
	repo, tag := imageRepoTag(d.Image)
	if repo == "eqalpha/keydb" || repo == "keydb" || strings.Contains(repo, "dragonfly") {
		tag = "" // their versions are not Valkey's
	}
	override := ""
	if rc.EngineVersion != nil {
		override = strings.TrimSpace(*rc.EngineVersion)
	}
	version, notes, err := resolveVersion(e, flavor, tag, override)
	if err != nil {
		res.Include, res.IncludeNote = false, err.Error()
		res.Source = CoolifyPlanSource{Type: "managed", Managed: &CoolifyPlanManaged{Engine: engine, From: d.Image, Notes: []string{}}}
		return res
	}
	pm := &CoolifyPlanManaged{Engine: engine, Version: version, Flavor: flavor, From: d.Image, Notes: notes}
	switch repo {
	case "eqalpha/keydb", "keydb":
		pm.Notes = append(pm.Notes, "KeyDB is Redis-compatible; the managed database is Valkey. Clients connect the same way.")
	case "dragonflydb/dragonfly", "docker.dragonflydb.io/dragonflydb/dragonfly":
		pm.Notes = append(pm.Notes, "Dragonfly is Redis-compatible; the managed database is Valkey. Clients connect the same way.")
	}
	if e.HasUser() {
		user := d.User
		switch {
		case user == "" && engine == engines.Postgres:
			user = "postgres"
		case user == "":
			user = e.DefaultUser
		case (engine == engines.MySQL || engine == engines.MariaDB) && user == "root":
			pm.Notes = append(pm.Notes, "Coolify's user is root; a managed "+e.Label+" uses a dedicated user ("+e.DefaultUser+").")
			user = e.DefaultUser
		case !engines.ValidIdent(user):
			pm.Notes = append(pm.Notes, fmt.Sprintf("The user name %q is not supported for a managed database; %q is used.", user, e.DefaultUser))
			user = e.DefaultUser
		}
		pm.Username = user
	}
	if e.DatabaseKey != "" {
		dbn := d.DBName
		switch {
		case dbn == "" && engine == engines.Postgres:
			dbn = pm.Username
		case dbn == "":
			dbn = e.DefaultDatabase
		case !engines.ValidIdent(dbn):
			pm.Notes = append(pm.Notes, fmt.Sprintf("The database name %q is not supported for a managed database; %q is used.", dbn, e.DefaultDatabase))
			dbn = e.DefaultDatabase
		}
		pm.Database = dbn
	}
	if !d.SensitiveVisible && d.Password == "" {
		res.addNote("credentials", SevInfo, "Coolify did not return the credentials (the token lacks read:sensitive). The user and database name were guessed from the defaults; a new password is generated either way.")
	}
	res.Source = CoolifyPlanSource{Type: "managed", Managed: pm}
	res.Port = e.Port
	res.Health = &CoolifyPlanHealth{Type: "tcp", Port: e.Port}

	for _, n := range pm.Notes {
		res.addNote("version", SevInfo, n)
	}
	res.addNote("credentials", SevInfo, "A new password is generated. Variables of other services that held the old one (or the old host name) are rewritten to references to this database.")
	if e.LicenseClass == engines.LicenseSourceAvailable {
		res.licence = true
		res.Include = false
		res.IncludeNote = e.LicenseNote + " Include it explicitly to accept the licence."
		if engine == engines.Redis {
			res.IncludeNote += " Valkey is the BSD-licensed alternative: set the engine in a new database if you prefer it."
		}
	}
	if rc.Include != nil {
		res.Include = *rc.Include
		if *rc.Include {
			res.IncludeNote = ""
		}
	}

	// What a managed database does not carry over.
	if d.IsPublic {
		res.unsupported(fmt.Sprintf("public port %d", d.PublicPort), "The database was public in Coolify. Enable Public access in the database panel if you still need it (it is off by default).")
	}
	if d.CustomConfig {
		res.unsupported("custom configuration file", "Settings are not exposed on managed databases; the engine defaults apply.")
	}
	if d.InitScripts > 0 {
		res.unsupported(fmt.Sprintf("%d init script(s)", d.InitScripts), "Init scripts are not supported; run them against the restored database.")
	}
	if d.EnableSSL {
		res.unsupported("SSL mode", "Databases are reached over the private network without TLS.")
	}
	if strings.TrimSpace(d.CustomDockerRunOptions) != "" {
		res.unsupported("custom Docker run options", "Managed databases do not take Docker options.")
	}
	if strings.TrimSpace(d.LimitsMemory) != "" && d.LimitsMemory != "0" || strings.TrimSpace(d.LimitsCPUs) != "" && d.LimitsCPUs != "0" {
		res.unsupported("resource limits", "Limits are not imported for managed databases; set them in the database settings.")
	}
	for _, v := range d.Volumes {
		if v.MountPath != "" {
			res.unsupported("volume "+v.MountPath, "The data volume of a managed database is created empty; the data is not copied.")
		}
	}
	return res
}

// databaseFollowup tells how to copy the data of a migrated database.
func databaseFollowup(d *Database, slug string) string {
	const docs = "docs/operations/database-backups.md#restore-from-a-dump-file"
	switch engineOf(d) {
	case engines.Postgres:
		return fmt.Sprintf("%s: the data is not copied. Dump it on the Coolify host (docker exec %s pg_dump -U %s -Fc %s > %s.dump), then run: nebulactrl databases restore %s --file %s.dump (see %s).",
			d.Name, d.UUID, orDefault(d.User, "postgres"), orDefault(d.DBName, "postgres"), slug, slug, slug, docs)
	case engines.MySQL, engines.MariaDB:
		return fmt.Sprintf("%s: the data is not copied. Dump it on the Coolify host (docker exec %s mysqldump -u %s -p --single-transaction %s | gzip > %s.sql.gz), then run: nebulactrl databases restore %s --file %s.sql.gz (see %s).",
			d.Name, d.UUID, orDefault(d.User, "root"), orDefault(d.DBName, "<database>"), slug, slug, slug, docs)
	case engines.Redis, engines.Valkey:
		return fmt.Sprintf("%s: starts empty. Cache, queue and session data can usually be rebuilt: drain the queues first, then switch over. NebulaCtrl has no RDB import.", d.Name)
	case engines.MongoDB:
		return fmt.Sprintf("%s: the data is not copied. Use mongodump on the Coolify host and mongorestore into the new database; there is no automated dump restore for MongoDB yet.", d.Name)
	}
	return fmt.Sprintf("%s: the data is not copied. Export it from Coolify and load it into the new database with the engine's own tools.", d.Name)
}

func orDefault(s, d string) string {
	if strings.TrimSpace(s) == "" {
		return d
	}
	return s
}

func (r *CoolifyPlanResource) addNote(key, sev, msg string) {
	r.Notes = append(r.Notes, CoolifyNote{Key: key, Severity: sev, Message: msg})
}

// unsupported records something that is not imported, as a list entry and a review note.
func (r *CoolifyPlanResource) unsupported(what, why string) {
	r.Unsupported = append(r.Unsupported, what+": "+why)
	r.addNote(what, SevWarning, "Not imported: "+why)
}
