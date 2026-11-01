package build

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Runtime versions Railpack builds use unless the application pins its own.
// Railpack's own default is an older "lts" that lags the maintained release.
const (
	defaultRailpackNode = "26.13.0"
	defaultRailpackBun  = "1.4.4"
)

var (
	// A `node`/`nodejs` (or `bun`) entry in mise.toml: bare key, quoted key, or
	// a table header, at the start of a line.
	miseNodeRE = regexp.MustCompile(`(?m)^\s*(?:\[tools\.)?["']?(?:node|nodejs)["']?\s*[=\]]`)
	miseBunRE  = regexp.MustCompile(`(?m)^\s*(?:\[tools\.)?["']?bun["']?\s*[=\]]`)
	// A `.tool-versions` line: `nodejs 22.1.0`.
	toolVersionsNodeRE = regexp.MustCompile(`(?m)^\s*(?:node|nodejs)\s+\S`)
	toolVersionsBunRE  = regexp.MustCompile(`(?m)^\s*bun\s+\S`)
)

// withRailpackDefaults returns vars plus RAILPACK_NODE_VERSION and
// RAILPACK_BUN_VERSION defaults for a Node/Bun project in srcDir. An
// application's own pin always wins: an explicit variable, `engines`,
// `packageManager`, `.nvmrc`, `.node-version`, `.bun-version`, mise.toml,
// `.tool-versions` or `packages` in railpack.json. vars is not modified.
func withRailpackDefaults(srcDir string, vars map[string]string) (out map[string]string, applied []string) {
	out = vars
	set := func(key, val string) {
		if len(applied) == 0 {
			out = make(map[string]string, len(vars)+2)
			for k, v := range vars {
				out[k] = v
			}
		}
		out[key] = val
		applied = append(applied, key+"="+val)
	}

	pkgRaw, err := readRepoFile(srcDir, "package.json")
	if err != nil {
		return out, nil // not a Node project: Railpack will not install Node or Bun from these variables
	}
	var pkg struct {
		Engines        map[string]any `json:"engines"`
		PackageManager string         `json:"packageManager"`
	}
	_ = json.Unmarshal(pkgRaw, &pkg) // a malformed package.json carries no pins
	engine := func(name string) bool {
		v, ok := pkg.Engines[name].(string)
		return ok && strings.TrimSpace(v) != ""
	}
	fileHas := func(name string) bool {
		raw, err := readRepoFile(srcDir, name)
		return err == nil && strings.TrimSpace(string(raw)) != ""
	}
	fileMatches := func(name string, re *regexp.Regexp) bool {
		raw, err := readRepoFile(srcDir, name)
		return err == nil && re.Match(raw)
	}
	railpackPins := func(pkgName string) bool {
		raw, err := readRepoFile(srcDir, "railpack.json")
		if err != nil {
			return false
		}
		var cfg struct {
			Packages map[string]any `json:"packages"`
		}
		if json.Unmarshal(raw, &cfg) != nil {
			return false
		}
		_, ok := cfg.Packages[pkgName]
		return ok
	}

	if strings.TrimSpace(vars["RAILPACK_NODE_VERSION"]) == "" &&
		!engine("node") && !fileHas(".nvmrc") && !fileHas(".node-version") &&
		!fileMatches("mise.toml", miseNodeRE) && !fileMatches(".mise.toml", miseNodeRE) &&
		!fileMatches(".tool-versions", toolVersionsNodeRE) && !railpackPins("node") {
		set("RAILPACK_NODE_VERSION", defaultRailpackNode)
	}

	usesBun := fileExists(filepath.Join(srcDir, "bun.lock")) || fileExists(filepath.Join(srcDir, "bun.lockb")) ||
		fileExists(filepath.Join(srcDir, "bunfig.toml")) || strings.HasPrefix(pkg.PackageManager, "bun@")
	if usesBun && strings.TrimSpace(vars["RAILPACK_BUN_VERSION"]) == "" &&
		!engine("bun") && !strings.HasPrefix(pkg.PackageManager, "bun@") && !fileHas(".bun-version") &&
		!fileMatches("mise.toml", miseBunRE) && !fileMatches(".mise.toml", miseBunRE) &&
		!fileMatches(".tool-versions", toolVersionsBunRE) && !railpackPins("bun") {
		set("RAILPACK_BUN_VERSION", defaultRailpackBun)
	}
	return out, applied
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
