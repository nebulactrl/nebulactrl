package composeimport

import (
	"regexp"
	"strings"
)

// seg is a piece of a variable value: literal text or a reference.
type seg struct {
	lit string
	ref string // "slug.KEY" when this is a reference
}

type segs []seg

func lit(s string) segs {
	if s == "" {
		return nil
	}
	return segs{{lit: s}}
}

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

var (
	hostTokenRe  = regexp.MustCompile(`[A-Za-z0-9_][A-Za-z0-9._-]*`)
	portAfterRe  = regexp.MustCompile(`^:\d{1,5}(?:$|[/?#,;\s"')])`)
	userinfoRe   = regexp.MustCompile(`://[^/\s@]*@$`)
	hostKeyRe    = regexp.MustCompile(`(?i)(HOST|SERVER|ADDR|ENDPOINT|URL|URI|DSN|BROKER|CONNECT|REDIS|CACHE|BACKEND|QUEUE)`)
	notHostKeyRe = regexp.MustCompile(`(?i)(_DB|_NAME|_USER|_USERNAME|_PASSWORD|_PASS|_PORT|_SCHEMA|_DATABASE|_TOKEN|_SECRET|_KEY)$`)
)

// hostRule says why an occurrence of a service name was treated as a reference.
type hostHit struct {
	start, end int
	target     string
	urlStart   bool // directly after scheme:// (no user info)
	reason     string
}

// findHostRefs locates the names in v that unambiguously refer to another
// service: after scheme:// or scheme://user:pw@, written as name:port at the
// start of the value or after a separator, after host=, or the whole value of a
// host-like variable. names maps a lower-case host name to the Compose key of
// the service it reaches.
func findHostRefs(key, v string, names map[string]string) []hostHit {
	var hits []hostHit
	for _, loc := range hostTokenRe.FindAllStringIndex(v, -1) {
		s, e := loc[0], loc[1]
		target, ok := names[strings.ToLower(v[s:e])]
		if !ok {
			continue
		}
		before, after := v[:s], v[e:]
		boundary := after == "" || strings.IndexByte(`:/?#,;"') `+"\t", after[0]) >= 0
		lower := strings.ToLower(before)
		h := hostHit{start: s, end: e, target: target}
		switch {
		case boundary && strings.HasSuffix(before, "://"):
			h.urlStart, h.reason = true, "host in a URL"
		case boundary && userinfoRe.MatchString(before):
			h.reason = "host in a URL"
		case boundary && (strings.HasSuffix(lower, "host=") || strings.HasSuffix(lower, "hostname=") || strings.HasSuffix(lower, "server=") || strings.HasSuffix(lower, "addr=")):
			h.reason = "host in a connection string"
		case portAfterRe.MatchString(after) && (s == 0 || strings.IndexByte(" ,=;\"'(", before[len(before)-1]) >= 0):
			h.reason = "host:port address"
		case s == 0 && e == len(v) && hostKeyRe.MatchString(key) && !notHostKeyRe.MatchString(key):
			h.reason = "host name"
		default:
			continue
		}
		hits = append(hits, h)
	}
	return hits
}
