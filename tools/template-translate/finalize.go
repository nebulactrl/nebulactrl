package main

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/nebulactrl/nebulactrl/internal/templates"
)

// slot is one string of a service that may carry placeholders.
type slot struct {
	st  *svcState
	ctx ctxKind
	key string // env key for env slots
	ptr *string
}

func (t *translator) slots() []slot {
	var out []slot
	for _, st := range t.states {
		if st.db != nil {
			continue
		}
		for i := range st.env {
			out = append(out, slot{st, ctxEnv, st.env[i].k, &st.env[i].v})
		}
		for i := range st.svc.Command {
			out = append(out, slot{st, ctxArg, "", &st.svc.Command[i]})
		}
		for i := range st.svc.Files {
			out = append(out, slot{st, ctxFile, st.svc.Files[i].Path, &st.svc.Files[i].Content})
		}
		if h := st.svc.Healthcheck; h != nil {
			for i := range h.Command {
				out = append(out, slot{st, ctxHealth, "", &h.Command[i]})
			}
		}
	}
	return out
}

func (t *translator) finalize() error {
	owners := t.settleDomains()
	for _, st := range t.states {
		if st.db == nil {
			t.health(st)
		}
	}
	t.resolvePlaceholders(owners)
	if t.err != nil {
		return t.err
	}
	t.finishServices()
	t.pruneInputs()
	return nil
}

// settleDomains assigns every domain id to the service that serves it and sets
// that service's http port. It returns id → owner.
func (t *translator) settleDomains() map[string]*svcState {
	owners := map[string]*svcState{}
	headerUsed := false
	for _, id := range t.domOrder {
		d := t.domains[id]
		var st *svcState
		if d.service != "" {
			st = t.byName[d.service]
		} else {
			st = t.guessOwner(id)
			if st != nil {
				t.review("services."+st.key+".domains", "domain for "+id+" is not declared in the source (SERVICE_URL_"+id+"); service guessed")
			}
		}
		if st == nil || st.db != nil {
			continue
		}
		owners[id] = st
		p := "services." + st.key
		port := d.port
		if port == 0 && !headerUsed && t.src.Port > 0 {
			port, headerUsed = t.src.Port, true
			t.review(p+".port", "port taken from the source header, check it")
		}
		if st.hasDom {
			t.review(p+".domains", "source declares more than one domain for this service ("+id+"); only the first is kept")
			continue
		}
		if st.oneshot {
			t.review(p, "service has a domain in the source but is also a one-shot job; domain dropped")
			continue
		}
		st.hasDom = true
		st.svc.Domains = []templates.TemplateDomain{{Generate: true}}
		st.svc.Port = port
		if port == 0 {
			t.review(p+".port", "http port unknown; set it")
		}
	}
	return owners
}

func (t *translator) guessOwner(id string) *svcState {
	want := normID(id)
	for _, st := range t.states {
		if st.db == nil && (normID(st.c.Name) == want || normID(st.key) == want) {
			return st
		}
	}
	for _, st := range t.states {
		if st.db == nil && !st.oneshot {
			return st
		}
	}
	return nil
}

type use struct {
	st  *svcState
	ctx ctxKind
}

func (t *translator) resolvePlaceholders(owners map[string]*svcState) {
	slots := t.slots()
	uses := map[string][]use{}
	for _, sl := range slots {
		for _, m := range placeholderRe.FindAllStringSubmatch(*sl.ptr, -1) {
			if m[1] == "G" {
				uses[m[2]] = append(uses[m[2]], use{sl.st, sl.ctx})
			}
		}
	}
	type genPlan struct {
		inline bool
		owner  *svcState
		envKey string
	}
	plans := map[string]*genPlan{}
	for _, name := range t.genOrder {
		us := uses[name]
		if len(us) == 0 {
			continue
		}
		g := t.gens[name]
		if len(us) == 1 && us[0].ctx == ctxEnv {
			plans[name] = &genPlan{inline: true}
			continue
		}
		var users []*svcState
		for _, u := range us {
			if !slices.Contains(users, u.st) {
				users = append(users, u.st)
			}
		}
		owner := t.pickOwner(users)
		plans[name] = &genPlan{owner: owner, envKey: g.envKey}
	}
	// Define the shared variables on their owners. An owner env entry that is
	// exactly the placeholder under the same key is the definition itself.
	for _, name := range t.genOrder {
		pl := plans[name]
		if pl == nil || pl.inline {
			continue
		}
		g := t.gens[name]
		ph := placeholder("G", name)
		defined := false
		for i := range pl.owner.env {
			e := &pl.owner.env[i]
			if e.k != pl.envKey {
				continue
			}
			if e.v == ph {
				e.v = g.expr
				defined = true
			} else {
				pl.envKey += "_SHARED"
			}
			break
		}
		if !defined {
			pl.owner.env = append(pl.owner.env, kv{pl.envKey, g.expr})
		}
	}
	slots = t.slots() // definitions may have reallocated the env slices
	var pending []pendingEnv
	urlRe := regexp.MustCompile(`https://\$\{\{((?:[a-z0-9-]+\.)?)NEBULA_PUBLIC_DOMAIN\}\}`)
	for _, sl := range slots {
		s := *sl.ptr
		if !strings.Contains(s, "\x00") {
			continue
		}
		s = placeholderRe.ReplaceAllStringFunc(s, func(m string) string {
			sub := placeholderRe.FindStringSubmatch(m)
			kind, name := sub[1], sub[2]
			p := "services." + sl.st.key
			switch kind {
			case "G":
				pl := plans[name]
				g := t.gens[name]
				if pl.inline {
					return g.expr
				}
				if sl.ctx == ctxEnv {
					if pl.owner == sl.st {
						// the definition env entry was rewritten to the expression
						if sl.key == pl.envKey && *sl.ptr == g.expr {
							return g.expr
						}
						return "${{" + pl.envKey + "}}"
					}
					return "${{" + pl.owner.key + "." + pl.envKey + "}}"
				}
				if pl.owner != sl.st {
					pending = append(pending, pendingEnv{sl.st, pl.envKey, "${{" + pl.owner.key + "." + pl.envKey + "}}"})
				}
				t.review(p+ctxPath(sl), "uses generated value $"+pl.envKey+" (expanded by the container shell, not by NebulaCtrl)")
				return "$" + pl.envKey
			default:
				own := owners[name]
				if own == nil {
					return m
				}
				v := "NEBULA_PUBLIC_DOMAIN"
				if kind == "U" {
					v = "NEBULA_PUBLIC_URL"
				}
				if sl.ctx != ctxEnv {
					t.review(p+ctxPath(sl), "references the public domain; NebulaCtrl does not resolve ${{…}} here")
				}
				if own == sl.st {
					return "${{" + v + "}}"
				}
				return "${{" + own.key + "." + v + "}}"
			}
		})
		*sl.ptr = urlRe.ReplaceAllString(s, "$${{${1}NEBULA_PUBLIC_URL}}")
	}
	for _, pe := range pending {
		if !hasEnv(pe.st, pe.k) {
			pe.st.env = append(pe.st.env, kv{pe.k, pe.v})
		}
	}
}

func ctxPath(sl slot) string {
	switch sl.ctx {
	case ctxArg:
		return ".command"
	case ctxFile:
		return ".files." + sl.key
	case ctxHealth:
		return ".healthcheck"
	}
	return ".env." + sl.key
}

func hasEnv(st *svcState, k string) bool {
	for _, e := range st.env {
		if e.k == k {
			return true
		}
	}
	return false
}

// pickOwner chooses the service that defines a shared value: one that does not
// (transitively) depend on the other users, so no dependency cycle appears.
func (t *translator) pickOwner(users []*svcState) *svcState {
	for _, u := range users {
		deps := t.closure(u)
		ok := true
		for _, o := range users {
			if o != u && deps[o] {
				ok = false
				break
			}
		}
		if ok {
			return u
		}
	}
	t.review("services."+users[0].key, "shared generated value forms a dependency cycle; reorder by hand")
	return users[0]
}

func (t *translator) closure(st *svcState) map[*svcState]bool {
	seen := map[*svcState]bool{}
	var walk func(s *svcState)
	walk = func(s *svcState) {
		for _, d := range s.c.DependsOn {
			if tgt := t.byName[d.Name]; tgt != nil && !seen[tgt] {
				seen[tgt] = true
				walk(tgt)
			}
		}
	}
	walk(st)
	return seen
}

var dbImageRe = regexp.MustCompile(`(?i)(postgres|postgis|pgvector|mysql|mariadb|mongo|redis|valkey|clickhouse|memcache|couchdb|cassandra|elasticsearch|opensearch|influxdb|timescale|surreal|dragonfly|keydb|rabbitmq)`)

// dbPort is the default port of well-known database images (0 if unknown).
func dbPort(repo string) int {
	r := strings.ToLower(repo)
	for _, e := range []struct {
		word string
		port int
	}{{"postgres", 5432}, {"postgis", 5432}, {"pgvector", 5432}, {"timescale", 5432}, {"mysql", 3306}, {"mariadb", 3306}, {"mongo", 27017}, {"redis", 6379}, {"valkey", 6379}, {"keydb", 6379}, {"dragonfly", 6379}, {"memcache", 11211}, {"clickhouse", 8123}, {"couchdb", 5984}, {"rabbitmq", 5672}} {
		if strings.Contains(r, e.word) {
			return e.port
		}
	}
	return 0
}

// finishServices applies the cross-field rules of the format.
func (t *translator) finishServices() {
	volUsers := map[string][]*svcState{}
	for _, st := range t.states {
		for _, v := range st.svc.Volumes {
			volUsers[v.Name] = append(volUsers[v.Name], st)
		}
	}
	for name, users := range volUsers {
		if len(users) > 1 {
			for _, st := range users {
				t.review("services."+st.key+".volumes."+name, "volume shared between services in the source; template volumes are per service")
			}
		}
	}
	for _, st := range t.states {
		p := "services." + st.key
		s := &st.svc
		if len(st.dropped) > 0 {
			t.review(p, "dropped from the compose service: "+strings.Join(st.dropped, ", "))
		}
		if st.db != nil {
			s.Database = st.db
			s.Kind = "database"
			for _, d := range st.c.DependsOn {
				if tgt := t.byName[d.Name]; tgt != nil && tgt != st && !contains(s.DependsOn, tgt.key) {
					s.DependsOn = append(s.DependsOn, tgt.key)
				}
			}
			if r := memoryMB(st.c.MemLimit); r > 0 || st.c.Cpus > 0 {
				s.Resources = &templates.TemplateLimits{MemoryMB: r, CPUs: st.c.Cpus}
			}
			continue
		}
		for _, e := range st.env {
			v := e.v
			if secretKeyRe.MatchString(e.k) && v != "" && !strings.Contains(v, "${") {
				ref := t.inputRef(e.k, v, true, false)
				st.setEnv(e.k, ref)
				t.review(p+".env."+e.k, "credential with a literal value in the source turned into a secret input")
			}
		}
		for _, e := range st.env {
			if s.Env == nil {
				s.Env = map[string]string{}
			}
			s.Env[e.k] = e.v
		}
		switch {
		case st.oneshot:
			s.OneShot = true
			s.Kind = "worker"
			if s.Port != 0 || len(s.Ports) > 0 || s.Healthcheck != nil {
				t.review(p, "one-shot job: port, published ports and healthcheck dropped")
			}
			s.Port, s.Ports, s.Healthcheck, s.Domains = 0, nil, nil, nil
		default:
			s.Kind = "app"
			if s.Port == 0 && len(s.Ports) == 0 && len(s.Domains) == 0 {
				s.Kind = "worker"
			}
			if dbImageRe.MatchString(st.repo) && !st.hasDom {
				s.Kind = "database"
			}
			if s.Healthcheck == nil {
				switch {
				case s.Port > 0:
					s.Healthcheck = &templates.TemplateHealth{Type: "tcp"}
					t.review(p+".healthcheck", "no usable compose healthcheck; tcp probe on the http port is a guess")
				case len(s.Ports) > 0:
					s.Healthcheck = &templates.TemplateHealth{Type: "tcp", Port: s.Ports[0].Port}
					t.review(p+".healthcheck", "no usable compose healthcheck; tcp probe is a guess")
				case s.Kind == "database" && dbPort(st.repo) > 0:
					s.Healthcheck = &templates.TemplateHealth{Type: "tcp", Port: dbPort(st.repo)}
					t.review(p+".healthcheck", "no usable compose healthcheck; tcp probe on the database's default port is a guess")
				default:
					t.review(p+".healthcheck", "no compose healthcheck: add one")
				}
			}
		}
		if t.src.Compose != nil && len(s.Ports) > 0 {
			t.review(p+".ports", "published ports default to publish: false; game servers usually need true")
		}
	}
}

// pruneInputs drops inputs nothing references (the validator rejects them).
func (t *translator) pruneInputs() {
	used := map[string]bool{}
	scan := func(v string) {
		for _, m := range regexp.MustCompile(`\$\{input:([A-Z][A-Z0-9_]*)\}`).FindAllStringSubmatch(v, -1) {
			used[m[1]] = true
		}
	}
	for _, st := range t.states {
		for _, v := range st.svc.Env {
			scan(v)
		}
		for _, f := range st.svc.Files {
			scan(f.Content)
		}
		for _, a := range st.svc.Command {
			scan(a)
		}
		if h := st.svc.Healthcheck; h != nil {
			for _, a := range h.Command {
				scan(a)
			}
		}
	}
	kept := t.inputs[:0]
	for _, in := range t.inputs {
		if used[in.Key] {
			kept = append(kept, in)
		}
	}
	t.inputs = kept
}

// ---- header ----------------------------------------------------------------

func (t *translator) assemble() *draft {
	src := t.src
	tm := &templates.CatalogTemplate{Schema: templates.SchemaVersion, ID: templateID(src.ID), Revision: 1}
	tm.Name = truncate(src.Name, 60)
	if src.Name == titleFromID(src.ID) {
		t.review("name", "name guessed from the upstream id")
	}
	tm.Summary = truncate(src.Summary, 200)
	if tm.Summary == "" {
		tm.Summary = tm.Name
		t.review("summary", "no upstream summary")
	}
	if src.Description != "" && src.Description != tm.Summary {
		tm.Description = src.Description
	}
	tm.Category = mapCategory(src)
	t.review("category", fmt.Sprintf("mapped from source category %q and tags %v", src.Category, src.Tags))
	tm.Tags = cleanTags(src.Tags)
	tm.Icon = normID(src.ID)
	t.review("icon", "slug guessed from the upstream name; check simple-icons")
	tm.Links = linksFor(src)
	t.review("links", "website/source guessed from upstream metadata")
	if tm.Links.Website == "" {
		t.review("links", "no website known: set links.website")
	}
	tm.Upstream = templates.TemplateUpstream{License: "", Class: templates.LicensePermissive}
	t.review("upstream.license", "unknown, look it up in the upstream repository")
	t.review("upstream.class", "placeholder: permissive | copyleft | source-available (+ licenseUrl)")
	tm.Origin = &templates.TemplateOrigin{Type: src.Kind, Path: src.Path, Commit: src.Commit, License: src.License}
	if src.AmdOnly || t.amdOnly() {
		tm.Requires.Arch = []string{"amd64"}
		t.review("requires.arch", "amd64 only in the source")
	}
	tm.Inputs = t.inputs
	// service names
	primary := t.states[0]
	for _, st := range t.states {
		if st.hasDom {
			primary = st
			break
		}
	}
	for _, st := range t.states {
		switch {
		case st == primary:
			st.svc.Name = tm.Name
		case st.db != nil:
			st.svc.Name = truncate(tm.Name+" "+strings.ToUpper(st.dbEng[:1])+st.dbEng[1:], 60)
		default:
			st.svc.Name = truncate(tm.Name+" "+titleFromID(st.key), 60)
		}
		tm.Services = append(tm.Services, st.svc)
	}
	tm.Group = tm.Name
	if err := templates.Validate(tm); err != nil {
		var ve *templates.ValidationError
		if errors.As(err, &ve) {
			for _, p := range ve.Problems {
				if strings.Contains(p, "dependency cycle") {
					t.review("services", "services form a dependency cycle (via dependsOn or references); break it by hand")
				}
			}
		}
	}
	return &draft{T: tm, Reviews: t.reviews}
}

func (t *translator) amdOnly() bool {
	for _, st := range t.states {
		if strings.Contains(scalar(st.c.Raw["platform"]), "amd64") {
			return true
		}
	}
	return false
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(strings.Join(strings.Fields(s), " "))
	if len(s) <= n {
		return s
	}
	cut := s[:n-3]
	if i := strings.LastIndex(cut, " "); i > n/2 {
		cut = cut[:i]
	}
	return cut + "..."
}

type pendingEnv struct {
	st   *svcState
	k, v string
}
