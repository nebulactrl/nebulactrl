package engines

import "strings"

// RepoTag splits an image reference into a normalised repository (Docker Hub
// prefixes and library/ removed, lower case) and its tag; a digest is dropped.
func RepoTag(ref string) (repo, tag string) {
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

// imageEngines maps the repository of a database image to its managed engine.
var imageEngines = map[string]struct{ engine, flavor string }{
	"postgres":              {Postgres, ""},
	"postgis/postgis":       {Postgres, FlavorPostGIS},
	"mysql":                 {MySQL, ""},
	"mariadb":               {MariaDB, ""},
	"mongo":                 {MongoDB, ""},
	"redis":                 {Redis, ""},
	"valkey/valkey":         {Valkey, ""},
	"valkey":                {Valkey, ""},
	"eqalpha/keydb":         {Valkey, ""},
	"dragonflydb/dragonfly": {Valkey, ""},
	"docker.dragonflydb.io/dragonflydb/dragonfly": {Valkey, ""},
	"clickhouse/clickhouse-server":                {ClickHouse, ""},
	"clickhouse":                                  {ClickHouse, ""},
	"yandex/clickhouse-server":                    {ClickHouse, ""},
}

// FromImage maps a database image reference to the managed engine (and flavor)
// that replaces it, with the tag of the reference. ok is false when no managed
// engine uses the image. Several images map to one engine (KeyDB and Dragonfly
// to Valkey), so a hit does not mean the image is the engine's own: see
// ExactVersion.
func FromImage(ref string) (engine, flavor, tag string, ok bool) {
	repo, tag := RepoTag(ref)
	e, ok := imageEngines[repo]
	if !ok {
		return "", "", "", false
	}
	return e.engine, e.flavor, tag, true
}

// ExactVersion returns the catalog version of e whose image tag is tag (the
// flavor's tag when flavor is set). ok is false for any other tag.
func ExactVersion(e *Engine, flavor, tag string) (version string, ok bool) {
	if e == nil || tag == "" {
		return "", false
	}
	if flavor != "" {
		f := e.Flavor(flavor)
		if f == nil {
			return "", false
		}
		for _, id := range e.SortedVersionIDs() {
			if f.Tags[id] == tag {
				return id, true
			}
		}
		return "", false
	}
	for _, v := range e.Versions {
		if v.Tag == tag {
			return v.ID, true
		}
	}
	return "", false
}
