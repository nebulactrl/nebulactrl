package variables

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/internal/varexpr"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// SuggestedVariable sources.
const (
	SuggestDatabase   = "database"
	SuggestEnvExample = "env-example"
)

// SuggestedVariable is a proposed variable. Accepting it is a normal staged write:
// the client PUTs the value it shows (with `source: "suggested"` for an
// env-example key).
type SuggestedVariable struct {
	Key            string   `json:"key"`
	Value          string   `json:"value" doc:"Proposed value; may be a reference or a generator expression. Empty when the user must provide one."`
	Source         string   `json:"source" enum:"database,env-example"`
	Reason         string   `json:"reason"`
	Comment        string   `json:"comment,omitempty" doc:"The comment above the key in the example file, as a hint"`
	ProposedSecret bool     `json:"proposedSecret"`
	NeedsInput     bool     `json:"needsInput"`
	File           string   `json:"file,omitempty"`
	Warnings       []string `json:"warnings"`
}

// SkippedVariable is a key of an example file that was left out on purpose.
type SkippedVariable struct {
	Key    string `json:"key"`
	Reason string `json:"reason"`
}

// SuggestedVariableList is the response of the suggestions endpoint.
type SuggestedVariableList struct {
	Items   []SuggestedVariable `json:"items"`
	Skipped []SkippedVariable   `json:"skipped" doc:"Keys of the example files left out on purpose (platform variables such as PORT)"`
	Commit  string              `json:"commit,omitempty" doc:"Commit the .env.example files were read from (empty when they were read through the git provider)"`
	Branch  string              `json:"branch,omitempty" doc:"Branch the .env.example files were read from"`
}

type suggestedEnv struct {
	Commit string                      `json:"commit"`
	Branch string                      `json:"branch,omitempty"`
	Files  []agentprotocol.EnvFileScan `json:"files"`
}

var (
	secretNameRe  = regexp.MustCompile(`(?i)(SECRET|TOKEN|KEY|PASSWORD|PASSWD|PRIVATE|SALT|SIGNING)`)
	placeholderRe = regexp.MustCompile(`(?i)^(|changeme|change[-_ ]?me|change[-_ ]?this.*|secret|password|replace[-_ ]?me|todo|example|none|null|x+|\*+|\.\.\.|<.*>|your[-_ ].*|.*[-_]here|insert.*|generate.*)$`)
	// fillerRe matches text that only stands in for a real value ("changeme",
	// "<your-key>", "your-key-here"). Narrower than placeholderRe: "none", "null"
	// and "example" are real values for many non-secret settings.
	fillerRe      = regexp.MustCompile(`(?i)^(changeme|change[-_ ]?me|change[-_ ]?this.*|replace[-_ ]?me|todo|x+|\*+|\.\.\.|<.*>|your[-_ ].*|.*[-_]here|insert.*|generate.*)$`)
	localhostRe   = regexp.MustCompile(`(?i)(^|[/@:])(localhost|127\.0\.0\.1|0\.0\.0\.0|\[::1\])([:/]|$)`)
	exportAliases = map[string]string{
		"POSTGRES_URL": "DATABASE_URL", "POSTGRESQL_URL": "DATABASE_URL", "PG_URL": "DATABASE_URL", "DB_URL": "DATABASE_URL",
		"DATABASE_URI": "DATABASE_URL", "MONGODB_URL": "MONGO_URL", "MONGODB_URI": "MONGO_URL", "MONGO_URI": "MONGO_URL",
		"REDIS_URI": "REDIS_URL", "CACHE_URL": "REDIS_URL", "VALKEY_URI": "VALKEY_URL",
	}
	preferredExports = []string{"DATABASE_URL", "REDIS_URL", "VALKEY_URL", "MONGO_URL", "MYSQL_URL", "CLICKHOUSE_URL"}
)

func isPlaceholder(v string) bool { return placeholderRe.MatchString(strings.TrimSpace(v)) }

// isFiller reports whether v is stand-in text rather than a usable example
// value; such text is never proposed as a variable's value.
func isFiller(v string) bool { return fillerRe.MatchString(strings.TrimSpace(v)) }

// generatorFor picks the generator expression proposed for a secret-like key.
func generatorFor(key string) string {
	up := strings.ToUpper(key)
	switch {
	case strings.HasSuffix(up, "_KEY_BASE"):
		return "${{secret(64)}}"
	case up == "APP_KEY" || strings.HasSuffix(up, "_ENCRYPTION_KEY"):
		return "${{randomBytes(32)}}"
	}
	return "${{secret(32)}}"
}

// HandleSourceScan stores the template env files an agent found in a checked
// out source (agents.SourceScanSink). The content is untrusted: sizes, counts
// and key names are re-validated here.
func (s *Service) HandleSourceScan(ctx context.Context, serverID uuid.UUID, scan agentprotocol.SourceScan) error {
	if len(scan.Files) == 0 {
		return nil // the frame reports a project config file only
	}
	id, err := uuid.Parse(scan.ServiceID)
	if err != nil {
		return fmt.Errorf("source scan service id %q: %w", scan.ServiceID, err) // from the agent: logged by the hub, never retried
	}
	sc, err := services.LoadScopeInternal(ctx, s.q, id)
	if apierr.IsNotFound(err) {
		return nil // deleted service: nothing to store
	}
	if err != nil {
		return fmt.Errorf("load service: %w", err)
	}
	if sc.Service.SourceType != services.SourceGit {
		return nil
	}
	if sc.Service.ServerID == nil || *sc.Service.ServerID != serverID {
		// A build server scans the checkout of the deployments it builds.
		built, err := s.q.ServerBuiltForService(ctx, db.ServerBuiltForServiceParams{ServerID: serverID, ServiceID: id})
		if err != nil {
			return fmt.Errorf("check build server of service: %w", err)
		}
		if !built {
			return nil // not this server's service: drop the scan
		}
	}
	clean := suggestedEnv{Commit: scan.Commit}
	total := 0
	for _, f := range scan.Files {
		if len(clean.Files) >= 8 || len(f.Path) > 200 {
			break
		}
		cf := agentprotocol.EnvFileScan{Path: f.Path}
		for _, v := range f.Vars {
			if total >= agentprotocol.ScanMaxVars {
				break
			}
			if !varexpr.IsKey(v.Key) || len(v.Key) > maxKeyLen || len(v.Value) > 512 || len(v.Comment) > 200 {
				continue
			}
			cf.Vars = append(cf.Vars, v)
			total++
		}
		if len(cf.Vars) > 0 {
			clean.Files = append(clean.Files, cf)
		}
	}
	raw, err := json.Marshal(clean)
	if err != nil {
		return err
	}
	if err := s.q.SetServiceSuggestedEnv(ctx, db.SetServiceSuggestedEnvParams{ID: id, SuggestedEnv: raw}); err != nil {
		return fmt.Errorf("store suggested env: %w", err)
	}
	services.PublishService(s.bus, services.EventServiceUpdated, sc)
	return nil
}

// Suggestions proposes variables for a service: references to databases of the
// environment that nothing references yet, and the keys of the repository's
// .env.example files (with smart defaults).
func (s *Service) Suggestions(ctx context.Context, serviceID uuid.UUID) (SuggestedVariableList, error) {
	sc, err := services.LoadScope(ctx, s.q, policy.ScopeProjectsRead, serviceID)
	if err != nil {
		return SuggestedVariableList{}, err
	}
	return s.suggestions(ctx, sc)
}

// suggestions is Suggestions for an already authorized service.
func (s *Service) suggestions(ctx context.Context, sc services.Scope) (SuggestedVariableList, error) {
	res, err := services.LoadResolver(ctx, s.q, s.box, sc.EnvInfo())
	if err != nil {
		return SuggestedVariableList{}, err
	}
	me, ok := res.Service(sc.Service.Slug)
	if !ok {
		return SuggestedVariableList{}, apierr.NotFound("service")
	}
	existing := map[string]bool{}
	referenced := map[string]bool{}
	for k, d := range me.Vars {
		existing[k] = true
		for _, ref := range services.References(d.Value) {
			referenced[ref[0]] = true
		}
	}
	out := SuggestedVariableList{Items: []SuggestedVariable{}, Skipped: []SkippedVariable{}}
	providers, exportKeys := exportProviders(res, sc.Service.Slug)
	used := map[string]bool{}

	// 1. Keys of the repository's template env files (most specific: they win).
	if len(sc.Service.SuggestedEnv) > 0 && sc.Service.SourceType == services.SourceGit {
		var se suggestedEnv
		if json.Unmarshal(sc.Service.SuggestedEnv, &se) == nil {
			out.Commit, out.Branch = se.Commit, se.Branch
			items, skipped := exampleSuggestions(se.Files, providers, func(key string) bool { return existing[key] })
			for _, g := range items {
				used[g.Key] = true
				out.Items = append(out.Items, g)
			}
			out.Skipped = skipped
		}
	}
	// 2. Databases of this environment that this service does not use yet.
	slugs := make([]string, 0, len(exportKeys))
	for slug := range exportKeys {
		slugs = append(slugs, slug)
	}
	sort.Strings(slugs)
	for _, slug := range slugs {
		if referenced[slug] {
			continue
		}
		key := primaryExport(exportKeys[slug])
		if key == "" || existing[key] || used[key] || services.IsPlatformKey(key) {
			continue
		}
		used[key] = true
		out.Items = append(out.Items, SuggestedVariable{
			Key: key, Value: "${{" + slug + "." + key + "}}", Source: SuggestDatabase, ProposedSecret: false, Warnings: []string{},
			Reason: fmt.Sprintf("%s runs in this environment and nothing in %s references it.", slug, sc.Service.Slug),
		})
	}

	return out, nil
}

// exportProviders maps every export key of the environment's services (except
// slug `except`) to the services that provide it, and every service to its
// export keys.
func exportProviders(res *services.Resolver, except string) (providers, exportKeys map[string][]string) {
	providers, exportKeys = map[string][]string{}, map[string][]string{}
	for _, slug := range res.Slugs() {
		if slug == except {
			continue
		}
		for _, e := range res.Exports(slug) {
			providers[e.Key] = append(providers[e.Key], slug)
			exportKeys[slug] = append(exportKeys[slug], e.Key)
		}
	}
	return providers, exportKeys
}

// exampleSuggestions turns scanned template files into suggestions (smart
// defaults, see suggestFromExample). A key for which skip returns true is left
// out silently (it is set already); a platform key such as PORT is left out
// and reported in the second result. A key listed twice appears once, from
// the first file.
func exampleSuggestions(files []agentprotocol.EnvFileScan, providers map[string][]string, skip func(key string) bool) ([]SuggestedVariable, []SkippedVariable) {
	items, skipped := []SuggestedVariable{}, []SkippedVariable{}
	seen := map[string]bool{}
	for _, f := range files {
		for _, v := range f.Vars {
			if seen[v.Key] {
				continue
			}
			seen[v.Key] = true
			switch {
			case services.IsPlatformKey(v.Key):
				skipped = append(skipped, SkippedVariable{Key: v.Key, Reason: v.Key + " is set by NebulaCtrl for every service, so it is not added."})
			case skip != nil && skip(v.Key):
			default:
				if g, ok := suggestFromExample(v, f.Path, providers); ok {
					if g.Warnings == nil {
						g.Warnings = []string{}
					}
					items = append(items, g)
				}
			}
		}
	}
	return items, skipped
}

func primaryExport(keys []string) string {
	have := map[string]bool{}
	for _, k := range keys {
		have[k] = true
	}
	for _, k := range preferredExports {
		if have[k] {
			return k
		}
	}
	sorted := append([]string(nil), keys...)
	sort.Strings(sorted)
	for _, k := range sorted {
		if strings.HasSuffix(k, "_URL") {
			return k
		}
	}
	return ""
}

func suggestFromExample(v agentprotocol.EnvVarHint, file string, providers map[string][]string) (SuggestedVariable, bool) {
	if services.IsPlatformKey(v.Key) {
		return SuggestedVariable{}, false
	}
	g := SuggestedVariable{Key: v.Key, Source: SuggestEnvExample, File: file, Comment: v.Comment}
	// A database export of this environment fits the key.
	want := v.Key
	if alias, ok := exportAliases[want]; ok {
		want = alias
	}
	if ps := providers[want]; len(ps) > 0 && (isPlaceholder(v.Value) || localhostRe.MatchString(v.Value) || strings.HasSuffix(v.Key, "_URL") || strings.HasSuffix(v.Key, "_URI")) {
		g.Value = "${{" + ps[0] + "." + want + "}}"
		g.Reason = fmt.Sprintf("%s in %s matches the %s export of %s.", v.Key, file, want, ps[0])
		return g, true
	}
	if secretNameRe.MatchString(v.Key) && isPlaceholder(v.Value) {
		g.Value = generatorFor(v.Key)
		g.ProposedSecret = true
		g.Reason = fmt.Sprintf("%s in %s looks like a secret with a placeholder value; generate one.", v.Key, file)
		return g, true
	}
	g.Value = v.Value
	g.ProposedSecret = secretNameRe.MatchString(v.Key)
	g.NeedsInput = strings.TrimSpace(v.Value) == ""
	g.Reason = fmt.Sprintf("Listed in %s.", file)
	switch {
	case isFiller(v.Value):
		// "changeme" and "<your-key>" are never stored as the value.
		g.Value, g.NeedsInput = "", true
		g.Reason = fmt.Sprintf("Listed in %s with a placeholder value.", file)
	case localhostRe.MatchString(v.Value):
		g.Warnings = append(g.Warnings, "The example value points at localhost; inside a container that is the container itself.")
		g.NeedsInput = true
		g.Value = ""
	}
	g.Warnings = append(g.Warnings, hygieneStrings(v.Key, v.Value)...)
	return g, true
}

func hygieneStrings(key, value string) []string {
	var out []string
	for _, w := range hygiene(key, value) {
		out = append(out, w.Message)
	}
	return out
}
