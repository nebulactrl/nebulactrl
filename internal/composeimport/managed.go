package composeimport

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/nebulactrl/nebulactrl/internal/databases/engines"
)

// managedCreds are the credentials a Compose database was configured with. The
// managed database keeps the user and database name but generates a new
// password, so the old password is replaced by a reference wherever other
// services use it.
type managedCreds struct {
	Engine   string
	User     string
	Database string
	Password string
	Root     string
}

// officialImages maps the repository part of an official database image to its engine.
var officialImages = map[string]string{
	"postgres":                     engines.Postgres,
	"mysql":                        engines.MySQL,
	"mariadb":                      engines.MariaDB,
	"redis":                        engines.Redis,
	"valkey":                       engines.Valkey,
	"valkey/valkey":                engines.Valkey,
	"mongo":                        engines.MongoDB,
	"clickhouse":                   engines.ClickHouse,
	"clickhouse/clickhouse-server": engines.ClickHouse,
}

// imageParts splits an image reference into repository (registry prefix of
// Docker Hub and library/ removed) and tag; a digest is dropped.
func imageParts(ref string) (repo, tag string) {
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

var (
	tagVersionRe = regexp.MustCompile(`^v?(\d+(?:\.\d+){0,2})(?:-(.+))?$`)
	tagVariantRe = regexp.MustCompile(`^(alpine[0-9.]*|bookworm|bullseye|trixie|jammy|noble|slim|slim-bookworm|alpine-slim)$`)
)

// engineForImage returns the engine of an official database image, or "".
func engineForImage(ref string) string {
	repo, _ := imageParts(ref)
	return officialImages[repo]
}

// resolveEngineVersion maps an image tag to a version of the managed catalog.
func resolveEngineVersion(e *engines.Engine, tag string) (version, note string, err error) {
	if tag == "" || tag == "latest" {
		return e.DefaultVersion(), "The tag is " + quoteTag(tag) + "; the managed database uses " + e.Label + " " + e.DefaultVersion() + ".", nil
	}
	m := tagVersionRe.FindStringSubmatch(tag)
	if m == nil {
		return "", "", fmt.Errorf("the tag %q is not a plain version", tag)
	}
	if m[2] != "" && !tagVariantRe.MatchString(m[2]) {
		return "", "", fmt.Errorf("the tag %q is a custom image variant", tag)
	}
	want := m[1]
	if v := e.Version(want); v != nil {
		return v.ID, "", nil
	}
	var best string
	for _, id := range e.SortedVersionIDs() {
		switch {
		case strings.HasPrefix(want, id+"."): // 17.2 → 17
			return id, "", nil
		case strings.HasPrefix(id, want+"."): // 8 → 8.4
			best = id
		}
	}
	if best != "" {
		return best, "", nil
	}
	return "", "", fmt.Errorf("%s %s is not offered as a managed database (available: %s)", e.Label, want, strings.Join(e.SortedVersionIDs(), ", "))
}

func quoteTag(tag string) string {
	if tag == "" {
		return "not set"
	}
	return `"` + tag + `"`
}

// credentialEnvKeys are the variables the managed database owns.
func credentialEnvKeys(e *engines.Engine) map[string]bool {
	out := map[string]bool{}
	for _, k := range e.CredentialKeys() {
		out[k] = true
	}
	return out
}

// evaluateManaged decides whether svc can become a managed database. It never
// returns nil for an official database image; for anything else it returns nil.
func evaluateManaged(svc *cService) (*ComposePlanManaged, managedCreds) {
	if svc.Image == "" {
		return nil, managedCreds{}
	}
	engineID := engineForImage(svc.Image)
	if engineID == "" {
		return nil, managedCreds{}
	}
	e := engines.Lookup(engineID)
	pm := &ComposePlanManaged{Engine: engineID, Notes: []string{}}
	_, tag := imageParts(svc.Image)
	version, note, err := resolveEngineVersion(e, tag)
	if err != nil {
		pm.Reason = err.Error()
		return pm, managedCreds{}
	}
	pm.Version = version
	if note != "" {
		pm.Notes = append(pm.Notes, note)
	}

	env := map[string]string{}
	for _, p := range svc.Env {
		env[p.Key] = p.Value
	}
	creds := managedCreds{Engine: engineID, Password: env[e.PasswordKey], Root: ""}
	if e.RootPasswordKey != "" {
		creds.Root = env[e.RootPasswordKey]
	}
	allowed := credentialEnvKeys(e)
	allowed["TZ"] = true
	for _, p := range svc.Env {
		if !allowed[p.Key] {
			pm.Reason = fmt.Sprintf("%s sets %s, which a managed database does not support", svc.Key, p.Key)
			return pm, managedCreds{}
		}
	}
	if svc.HasCommand {
		pm.Reason = "The service overrides the command; a managed database uses the standard one."
		return pm, managedCreds{}
	}
	if e.HasUser() {
		user := env[e.UserKey]
		if user == "" {
			user = e.DefaultUser
		}
		if (engineID == engines.MySQL || engineID == engines.MariaDB) && user == "root" {
			pm.Reason = "The service connects as root; a managed " + e.Label + " uses a dedicated user."
			return pm, managedCreds{}
		}
		if !engines.ValidIdent(user) {
			pm.Reason = fmt.Sprintf("The user name %q is not supported for a managed database.", user)
			return pm, managedCreds{}
		}
		creds.User = user
		pm.Username = user
	}
	if e.DatabaseKey != "" {
		name := env[e.DatabaseKey]
		if name == "" && engineID == engines.Postgres {
			name = creds.User // the postgres image creates a database named after the user
		}
		if name == "" {
			name = e.DefaultDatabase
		}
		if !engines.ValidIdent(name) {
			pm.Reason = fmt.Sprintf("The database name %q is not supported for a managed database.", name)
			return pm, managedCreds{}
		}
		creds.Database = name
		pm.Database = name
	}
	spec := engines.Spec{Engine: engineID, Version: version}.Normalize()
	_, dataPath := spec.DataMount()
	for _, m := range svc.Mounts {
		switch m.Type {
		case "volume":
			if m.Target == dataPath || (engineID == engines.Postgres && (m.Target == "/var/lib/postgresql" || m.Target == "/var/lib/postgresql/data")) {
				pm.Notes = append(pm.Notes, "The data volume is created empty: existing data is not copied. Restore a dump or backup after the first deploy.")
				continue
			}
			pm.Reason = "The service mounts a volume at " + m.Target + ", which a managed database does not support."
			return pm, managedCreds{}
		default:
			pm.Reason = "The service mounts " + m.Source + " at " + m.Target + " (for example init scripts or configuration), which a managed database does not support."
			return pm, managedCreds{}
		}
	}
	pm.Eligible, pm.Enabled = true, true
	return pm, creds
}

// passwordExport is the export that holds the managed database's password.
func passwordExport(engine string) string {
	if engine == engines.Redis || engine == engines.Valkey {
		return "REDIS_PASSWORD"
	}
	return "DATABASE_PASSWORD"
}
