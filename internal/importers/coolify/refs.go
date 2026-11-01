package coolify

import (
	"net/url"
	"regexp"
	"sort"
	"strings"

	"github.com/nebulactrl/nebulactrl/internal/databases/engines"
)

// seg is a piece of a variable value: literal text or a reference ("slug.KEY").
type seg struct {
	lit string
	ref string
}

type segs []seg

// render produces the stored value: literals escape "${{", references become ${{ns.KEY}}.
func (ss segs) render() string {
	var b strings.Builder
	for _, s := range ss {
		if s.ref != "" {
			b.WriteString("${{" + s.ref + "}}")
			continue
		}
		b.WriteString(strings.ReplaceAll(s.lit, "${{", "$${{"))
	}
	return b.String()
}

// target is a resource of the same environment that variables may point at.
type target struct {
	res *CoolifyPlanResource
	// hosts are the names the resource is reached by inside Coolify's network
	// (the UUID, plus custom network aliases), lower case.
	hosts []string
	// database credentials; nil for an application
	db *Database
}

func (t *target) isDB() bool { return t.db != nil }

// refCtx resolves one resource's variables against the other resources of its environment.
type refCtx struct {
	targets []*target
	// others are resources the variables may mention that cannot be referenced:
	// another environment, or not imported.
	others []otherHost
	// linked marks the databases the resource demonstrably uses (their host or password appears in a value).
	linked map[*target]bool
}

type otherHost struct{ host, name, why string }

// hit is one rewritten place in a value.
type hit struct {
	start, end int
	ref        string
	kind       string
	target     *target
}

var (
	userKeyHint = regexp.MustCompile(`(?i)(USER|USERNAME|LOGIN)$`)
	nameKeyHint = regexp.MustCompile(`(?i)(^|_)(DB|DATABASE|DB_NAME|DATABASE_NAME|SCHEMA)$`)
	portKeyHint = regexp.MustCompile(`(?i)PORT$`)
)

func isHostChar(c byte) bool {
	return c == '-' || c == '_' || c == '.' || (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// hostBoundary checks that v[s:e] is a whole host name token: not part of a longer name.
func hostBoundary(v string, s, e int) bool {
	if s > 0 && isHostChar(v[s-1]) {
		return false
	}
	if e < len(v) {
		if c := v[e]; c == '-' || c == '_' {
			return false
		} else if c == '.' && e+1 < len(v) && isHostChar(v[e+1]) {
			return false
		} else if (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') {
			return false
		}
	}
	return true
}

// findAll returns the start of every occurrence of needle in hay (case-insensitive when fold).
func findAll(hay, needle string, fold bool) []int {
	if needle == "" {
		return nil
	}
	if fold {
		hay, needle = strings.ToLower(hay), strings.ToLower(needle)
	}
	var out []int
	for off := 0; ; {
		i := strings.Index(hay[off:], needle)
		if i < 0 {
			return out
		}
		out = append(out, off+i)
		off += i + len(needle)
	}
}

// databaseRef names the export of a managed database for a part of a connection.
func (t *target) exportFor(kind string) string {
	switch kind {
	case "host":
		return "NEBULA_PRIVATE_HOST"
	}
	if !t.isDB() {
		return ""
	}
	eng := engineOf(t.db)
	p := engines.LinkPrefix(eng)
	switch kind {
	case "password":
		return p + "PASSWORD"
	case "user":
		return p + "USER"
	case "database":
		return p + "NAME"
	case "port":
		return p + "PORT"
	case "url":
		return engines.PrimaryExport(eng)
	}
	return ""
}

func (t *target) ref(kind string) string {
	if k := t.exportFor(kind); k != "" {
		return t.res.Slug + "." + k
	}
	return ""
}

// dbPorts are Coolify's internal ports of the engines.
func dbInternalPort(d *Database) string {
	switch engineOf(d) {
	case engines.Postgres:
		return "5432"
	case engines.MySQL, engines.MariaDB:
		return "3306"
	case engines.Redis, engines.Valkey:
		return "6379"
	case engines.MongoDB:
		return "27017"
	case engines.ClickHouse:
		return "9000"
	}
	return ""
}

func engineOf(d *Database) string {
	if d == nil {
		return ""
	}
	e, _, _ := classifyDatabase(d)
	return e
}

// matchURL reports whether v is exactly a connection URL of t's database: same
// credentials, host and database, no query.
func (t *target) matchURL(v string) bool {
	d := t.db
	v = strings.TrimSpace(v)
	if d == nil || !strings.Contains(v, "://") {
		return false
	}
	if d.InternalURL != "" && v == strings.TrimSpace(d.InternalURL) {
		return true
	}
	u, err := url.Parse(v)
	if err != nil || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	eng := engineOf(d)
	okScheme := false
	for _, s := range urlSchemes(eng) {
		if strings.EqualFold(u.Scheme, s) {
			okScheme = true
		}
	}
	if !okScheme {
		return false
	}
	host := strings.ToLower(u.Hostname())
	hostOK := false
	for _, h := range t.hosts {
		if host == h {
			hostOK = true
		}
	}
	if !hostOK || (u.Port() != "" && u.Port() != dbInternalPort(d)) {
		return false
	}
	pw, _ := u.User.Password()
	if d.Password == "" || pw != d.Password {
		return false
	}
	switch eng {
	case engines.Redis, engines.Valkey:
		return u.Path == "" || u.Path == "/"
	default:
		if d.User != "" && u.User.Username() != d.User {
			return false
		}
		return strings.TrimPrefix(u.Path, "/") == d.DBName
	}
}

func urlSchemes(engine string) []string {
	switch engine {
	case engines.Postgres:
		return []string{"postgres", "postgresql"}
	case engines.MySQL, engines.MariaDB:
		return []string{"mysql", "mariadb"}
	case engines.Redis, engines.Valkey:
		return []string{"redis", "rediss", "valkey"}
	case engines.MongoDB:
		return []string{"mongodb"}
	case engines.ClickHouse:
		return []string{"clickhouse"}
	}
	return nil
}

// scan finds the places in v that point at another resource. linkedOnly runs the
// host and password rules only (the first pass that decides which databases a
// resource uses); the second pass adds the rules that need that knowledge.
func (c *refCtx) scan(key, v string, self *CoolifyPlanResource, linkedOnly bool) (hits []hit, notes []string) {
	if v == "" {
		return nil, nil
	}
	var cands []hit
	for _, t := range c.targets {
		if t.res == self {
			continue
		}
		// 1. a whole connection URL
		if t.matchURL(v) {
			cands = append(cands, hit{0, len(v), t.ref("url"), "url", t})
			continue
		}
		// 2. host names
		for _, h := range t.hosts {
			for _, s := range findAll(v, h, true) {
				if hostBoundary(v, s, s+len(h)) {
					cands = append(cands, hit{s, s + len(h), t.ref("host"), "host", t})
				}
			}
		}
		// 3. the password of a database
		if t.isDB() && t.db.Password != "" {
			pw := t.db.Password
			ref := t.ref("password")
			if v == pw {
				cands = append(cands, hit{0, len(v), ref, "password", t})
			}
			for _, enc := range uniqueStrings([]string{pw, url.PathEscape(pw), url.QueryEscape(pw)}) {
				for _, s := range findAll(v, ":"+enc+"@", false) {
					cands = append(cands, hit{s + 1, s + 1 + len(enc), ref, "password", t})
				}
			}
			if len(pw) >= 12 {
				for _, s := range findAll(v, pw, false) {
					if (s == 0 || !isHostChar(v[s-1])) && (s+len(pw) == len(v) || !isHostChar(v[s+len(pw)])) {
						cands = append(cands, hit{s, s + len(pw), ref, "password", t})
					}
				}
			}
		}
	}
	if !linkedOnly {
		// 4. user, database name and port, only as the whole value of a fitting variable
		// and only for databases the resource uses.
		whole := func(kind, val string, hint *regexp.Regexp) {
			if val == "" || v != val || !hint.MatchString(key) {
				return
			}
			var match []*target
			for _, t := range c.targets {
				if t.isDB() && t.res != self && c.linked[t] && valueOf(t.db, kind) == val && t.exportFor(kind) != "" {
					match = append(match, t)
				}
			}
			switch len(match) {
			case 0:
			case 1:
				cands = append(cands, hit{0, len(v), match[0].ref(kind), kind, match[0]})
			default:
				notes = append(notes, "The value of "+key+" matches several databases; it was left as it is.")
			}
		}
		for _, kind := range []string{"user", "database", "port"} {
			var hint *regexp.Regexp
			switch kind {
			case "user":
				hint = userKeyHint
			case "database":
				hint = nameKeyHint
			default:
				hint = portKeyHint
			}
			whole(kind, v, hint)
		}
	}
	for _, o := range c.others {
		for _, s := range findAll(v, o.host, true) {
			if hostBoundary(v, s, s+len(o.host)) {
				notes = append(notes, "The value of "+key+" points at "+o.name+", which is "+o.why+": the address was left as it is.")
				break
			}
		}
	}
	// keep the first, longest non-overlapping hits
	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].start != cands[j].start {
			return cands[i].start < cands[j].start
		}
		return cands[i].end-cands[i].start > cands[j].end-cands[j].start
	})
	last := -1
	for _, h := range cands {
		if h.ref == "" || h.start < last {
			continue
		}
		hits = append(hits, h)
		last = h.end
	}
	return hits, uniqueStrings(notes)
}

func valueOf(d *Database, kind string) string {
	switch kind {
	case "user":
		return d.User
	case "database":
		return d.DBName
	case "port":
		return dbInternalPort(d)
	}
	return ""
}

// apply renders v with the hits replaced by references.
func apply(v string, hits []hit) segs {
	var out segs
	pos := 0
	for _, h := range hits {
		if h.start > pos {
			out = append(out, seg{lit: v[pos:h.start]})
		}
		out = append(out, seg{ref: h.ref})
		pos = h.end
	}
	if pos < len(v) {
		out = append(out, seg{lit: v[pos:]})
	}
	return out
}

// remapRefs renames the namespace of ${{ns.KEY}} references (planned slug →
// actual slug), leaving escapes and other text alone.
func remapRefs(v string, slugs map[string]string) string {
	if !strings.Contains(v, "${{") {
		return v
	}
	var out strings.Builder
	for i := 0; i < len(v); {
		switch {
		case strings.HasPrefix(v[i:], "$${{"):
			out.WriteString("$${{")
			i += 4
		case strings.HasPrefix(v[i:], "${{"):
			end := strings.Index(v[i:], "}}")
			if end < 0 {
				out.WriteString(v[i:])
				return out.String()
			}
			inner := v[i+3 : i+end]
			if ns, key, ok := strings.Cut(inner, "."); ok {
				if real, found := slugs[strings.TrimSpace(ns)]; found {
					inner = real + "." + key
				}
			}
			out.WriteString("${{" + inner + "}}")
			i += end + 2
		default:
			out.WriteByte(v[i])
			i++
		}
	}
	return out.String()
}
