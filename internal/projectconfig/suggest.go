package projectconfig

import (
	"reflect"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"
)

// maxSuggestions bounds the "did you mean" list of one unknown key.
const maxSuggestions = 3

// maxSuggestKeyRunes is the longest unknown key that is compared by edit
// distance; a longer key is not a typo of anything.
const maxSuggestKeyRunes = 64

// credentialMessage is the text of a credential_key diagnostic. Credential
// keys get no suggestion: there is no key to rename them to.
func credentialMessage() string {
	return "credentials are never stored in nebula.toml; set the value in the dashboard or CLI, or use secrets.keys"
}

// Suggest returns the "did you mean" candidates for an unknown key. path is the
// key path of the table that contains the key, for example {"services", "web"}
// or {"environments", "staging", "services", "web"}; nil is the root table.
//
// The result lists, in this order and without duplicates: valid keys that
// differ from the unknown key only in case and "_"/"-" (stopGraceSeconds,
// stop-grace-seconds), entries of the alias table that are valid in this table,
// and valid keys within Damerau-Levenshtein distance max(2, len/3), nearest
// first and alphabetical on ties. At most three are returned. Credential keys
// have no suggestion.
func Suggest(path []string, key string) []string {
	if credentialKey(key) {
		return nil
	}
	valid := validKeys(path)
	if len(valid) == 0 {
		return nil
	}
	norm := normaliseKey(key)

	var out []string
	add := func(s string) {
		if !slices.Contains(out, s) {
			out = append(out, s)
		}
	}

	if norm != "" {
		for _, k := range valid {
			if k != key && normaliseKey(k) == norm {
				add(k)
			}
		}
		if a, ok := keyAliases[norm]; ok && a.applies(path) && aliasTargetValid(path, a.target) {
			add(a.target)
		}
	}

	if n := utf8.RuneCountInString(key); n <= maxSuggestKeyRunes {
		threshold := max(2, n/3)
		lower := []rune(strings.ToLower(key))
		type cand struct {
			key  string
			dist int
		}
		var cands []cand
		for _, k := range valid {
			if k == key {
				continue
			}
			kr := []rune(k)
			if d := len(kr) - len(lower); d > threshold || -d > threshold {
				continue
			}
			if d := osaDistance(lower, kr); d <= threshold {
				cands = append(cands, cand{k, d})
			}
		}
		sort.Slice(cands, func(i, j int) bool {
			if cands[i].dist != cands[j].dist {
				return cands[i].dist < cands[j].dist
			}
			return cands[i].key < cands[j].key
		})
		for _, c := range cands {
			add(c.key)
		}
	}

	if len(out) > maxSuggestions {
		out = out[:maxSuggestions]
	}
	return out
}

var keySeparators = strings.NewReplacer("_", "", "-", "")

// normaliseKey lower-cases a key and drops "_" and "-", so snake_case,
// kebab-case and camelCase spellings of one key compare equal.
func normaliseKey(k string) string {
	return strings.ToLower(keySeparators.Replace(k))
}

// osaDistance is the optimal-string-alignment (restricted Damerau-Levenshtein)
// distance: insertions, deletions, substitutions and adjacent transpositions.
func osaDistance(a, b []rune) int {
	prev2 := make([]int, len(b)+1)
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			v := min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
			if i > 1 && j > 1 && a[i-1] == b[j-2] && a[i-2] == b[j-1] {
				v = min(v, prev2[j-2]+1)
			}
			cur[j] = v
		}
		prev2, prev, cur = prev, cur, prev2
	}
	return prev[len(b)]
}

type aliasScope int

const (
	aliasAnywhere aliasScope = iota // any table where the target is valid
	aliasService                    // service tables only
	aliasNotRoot                    // every table except the root
	aliasRoot                       // the root table only
)

type keyAlias struct {
	// target is the suggested key path inside the table, dotted. A segment in
	// angle brackets stands for a name the user chooses.
	target string
	scope  aliasScope
}

// applies reports whether the alias may fire in the table at path.
func (a keyAlias) applies(path []string) bool {
	switch a.scope {
	case aliasService:
		return isServiceTable(path)
	case aliasNotRoot:
		return len(path) > 0
	case aliasRoot:
		return len(path) == 0
	}
	return true
}

// isServiceTable reports whether path is services.<slug> or
// environments.<env>.services.<slug>.
func isServiceTable(path []string) bool {
	switch len(path) {
	case 2:
		return path[0] == "services"
	case 4:
		return path[0] == "environments" && path[2] == "services"
	}
	return false
}

// aliasTargetValid reports whether every non-placeholder segment of target is
// a valid key at its place below path.
func aliasTargetValid(path []string, target string) bool {
	cur := slices.Clone(path)
	for seg := range strings.SplitSeq(target, ".") {
		if strings.HasPrefix(seg, "<") {
			return true
		}
		if !slices.Contains(validKeys(cur), seg) {
			return false
		}
		cur = append(cur, seg)
	}
	return true
}

// keyAliases maps a normalised misspelling (lower case, no "_" or "-") to the
// key the author most likely meant. It is extended from the mistakes agents and
// people make most often.
var keyAliases = map[string]keyAlias{
	"healthcheck":         {"health_check", aliasAnywhere},
	"healthcheckpath":     {"health_check.path", aliasService},
	"healthcheckport":     {"health_check.port", aliasService},
	"healthcheckinterval": {"health_check.interval_seconds", aliasService},
	"healthchecktimeout":  {"health_check.timeout_seconds", aliasService},

	"env":                  {"variables", aliasAnywhere},
	"envs":                 {"variables", aliasAnywhere},
	"envvars":              {"variables", aliasAnywhere},
	"envvariables":         {"variables", aliasAnywhere},
	"environmentvariables": {"variables", aliasAnywhere},
	"vars":                 {"variables", aliasAnywhere},
	// At the root "environment" is a misspelling of the environments table.
	"environment": {"variables", aliasNotRoot},

	"startcommand": {"command", aliasService},
	"entrypoint":   {"command", aliasService},
	"args":         {"command", aliasService},
	"cmd":          {"command", aliasAnywhere},

	"memory":      {"memory_mb", aliasAnywhere},
	"mem":         {"memory_mb", aliasAnywhere},
	"memorylimit": {"memory_mb", aliasAnywhere},
	"memlimit":    {"memory_mb", aliasAnywhere},
	"cpu":         {"cpus", aliasAnywhere},
	"cpulimit":    {"cpus", aliasAnywhere},

	"domain":        {"domains", aliasAnywhere},
	"hosts":         {"domains", aliasAnywhere},
	"hostname":      {"domains", aliasAnywhere},
	"hostnames":     {"domains", aliasAnywhere},
	"customdomains": {"domains", aliasAnywhere},

	"replicas":     {"scale.replicas", aliasService},
	"instances":    {"scale.replicas", aliasService},
	"replicacount": {"scale.replicas", aliasService},
	"scaling":      {"scale", aliasService},
	"autoscale":    {"scale.autoscaling", aliasService},
	"autoscaling":  {"scale.autoscaling", aliasService},

	"cron":     {"tasks.<name>", aliasService},
	"schedule": {"tasks.<name>", aliasService},
	"jobs":     {"tasks", aliasService},
	"cronjobs": {"tasks", aliasService},

	"workers": {"processes", aliasService},
	"process": {"processes.<name>", aliasService},
	"worker":  {"processes.<name>", aliasService},

	"volume":  {"volumes", aliasAnywhere},
	"mounts":  {"volumes", aliasService},
	"file":    {"files", aliasService},
	"configs": {"files", aliasService},

	"stopgrace":              {"stop_grace_seconds", aliasService},
	"graceperiod":            {"stop_grace_seconds", aliasService},
	"terminationgraceperiod": {"stop_grace_seconds", aliasService},

	"ports":  {"port", aliasService},
	"expose": {"port", aliasService},

	"depends":      {"depends_on", aliasService},
	"dependencies": {"depends_on", aliasService},
	"needs":        {"depends_on", aliasService},

	"releasecommand": {"pre_deploy", aliasService},
	"migrate":        {"pre_deploy", aliasService},

	"dockerfile":     {"build.dockerfile", aliasService},
	"dockerfilepath": {"build.dockerfile", aliasService},
	"buildcontext":   {"build.context", aliasService},
	"builder":        {"build.type", aliasService},
	"buildtype":      {"build.type", aliasService},

	"repo":       {"git.repo", aliasService},
	"repository": {"git.repo", aliasService},
	"gitrepo":    {"git.repo", aliasService},
	"giturl":     {"git.url", aliasService},
	"branch":     {"git.branch", aliasService},
	"gitbranch":  {"git.branch", aliasService},

	"labels":     {"docker.labels", aliasService},
	"privileged": {"docker.privileged", aliasService},
	"capadd":     {"docker.cap_add", aliasService},
	"capdrop":    {"docker.cap_drop", aliasService},
	"readonly":   {"docker.read_only_rootfs", aliasService},
	"shmsize":    {"docker.shm_size_mb", aliasService},

	"version":       {"schema", aliasRoot},
	"schemaversion": {"schema", aliasRoot},
}

// validKeys returns the sorted TOML keys that are valid in the table at path,
// read from the toml tags of the File model. A nil path is the root. Map-keyed
// fields (services.<slug>, domains.<host>, …) consume the dynamic key segment;
// array-of-table fields are traversed with their element type and consume no
// segment. A table whose keys are all dynamic (the services table itself, the
// variables table, string maps) and an unknown path have no valid keys.
func validKeys(path []string) []string {
	t := reflect.TypeOf(File{})
	for i := 0; i < len(path); {
		f, ok := fieldByTOMLKey(t, path[i])
		if !ok {
			return nil
		}
		i++
		ft := derefType(f.Type)
		switch ft.Kind() {
		case reflect.Map:
			if i >= len(path) {
				return nil
			}
			i++
			ft = derefType(ft.Elem())
		case reflect.Slice:
			ft = derefType(ft.Elem())
		}
		if ft.Kind() != reflect.Struct {
			return nil
		}
		t = ft
	}

	keys := make([]string, 0, t.NumField())
	for i := range t.NumField() {
		if name, ok := tomlKeyName(t.Field(i)); ok {
			keys = append(keys, name)
		}
	}
	if t == reflect.TypeOf(Service{}) {
		// unset exists only in environment overrides; only_in only in the base.
		drop := "unset"
		if len(path) > 0 && path[0] == "environments" {
			drop = "only_in"
		}
		keys = slices.DeleteFunc(keys, func(k string) bool { return k == drop })
	}
	sort.Strings(keys)
	return keys
}

func derefType(t reflect.Type) reflect.Type {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t
}

func fieldByTOMLKey(t reflect.Type, key string) (reflect.StructField, bool) {
	for i := range t.NumField() {
		f := t.Field(i)
		if name, ok := tomlKeyName(f); ok && name == key {
			return f, true
		}
	}
	return reflect.StructField{}, false
}

// tomlKeyName returns the key a struct field is written under.
func tomlKeyName(f reflect.StructField) (string, bool) {
	if !f.IsExported() {
		return "", false
	}
	name, _, _ := strings.Cut(f.Tag.Get("toml"), ",")
	if name == "" || name == "-" {
		return "", false
	}
	return name, true
}
