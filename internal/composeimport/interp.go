package composeimport

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Interpolation implements the variable syntax of the Compose specification:
//
//	$$          a literal dollar sign
//	$VAR        the value of VAR
//	${VAR}      the value of VAR
//	${VAR:-d}   VAR, or d when VAR is unset or empty      (${VAR-d}: only when unset)
//	${VAR:+r}   r when VAR is set and not empty           (${VAR+r}: when set)
//	${VAR:?e}   VAR, or an error e when unset or empty    (${VAR?e}: only when unset)
//
// Defaults and replacements may contain nested interpolations. An unset
// variable without a default is the empty string and is reported as missing.

var varNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*`)

// interpolator expands variables against one environment and records which
// variables were missing or required but absent.
type interpolator struct {
	env      map[string]string
	missing  map[string]bool
	required map[string]string // variable → error message of ${VAR:?msg}
}

func newInterpolator(env map[string]string) *interpolator {
	return &interpolator{env: env, missing: map[string]bool{}, required: map[string]string{}}
}

// Missing lists the unset variables that had no default, sorted.
func (ip *interpolator) Missing() []string {
	out := make([]string, 0, len(ip.missing))
	for k := range ip.missing {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Required lists the ${VAR:?err} failures as "VAR: message", sorted.
func (ip *interpolator) Required() []string {
	out := make([]string, 0, len(ip.required))
	for k, m := range ip.required {
		if m == "" {
			out = append(out, k)
		} else {
			out = append(out, k+": "+m)
		}
	}
	sort.Strings(out)
	return out
}

// Expand interpolates s.
func (ip *interpolator) Expand(s string) string {
	if !strings.Contains(s, "$") {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		c := s[i]
		if c != '$' {
			b.WriteByte(c)
			i++
			continue
		}
		if i+1 >= len(s) {
			b.WriteByte('$')
			i++
			continue
		}
		switch n := s[i+1]; {
		case n == '$':
			b.WriteByte('$')
			i += 2
		case n == '{':
			end := matchBrace(s, i+1)
			if end < 0 { // unterminated: keep literally
				b.WriteString(s[i:])
				return b.String()
			}
			b.WriteString(ip.expandBraced(s[i+2 : end]))
			i = end + 1
		default:
			name := varNameRe.FindString(s[i+1:])
			if name == "" {
				b.WriteByte('$')
				i++
				continue
			}
			b.WriteString(ip.lookup(name))
			i += 1 + len(name)
		}
	}
	return b.String()
}

// matchBrace returns the index of the "}" closing the "{" at open, or -1.
func matchBrace(s string, open int) int {
	depth := 0
	for i := open; i < len(s); i++ {
		switch s[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

func (ip *interpolator) lookup(name string) string {
	v, ok := ip.env[name]
	if !ok {
		ip.missing[name] = true
	}
	return v
}

func (ip *interpolator) expandBraced(body string) string {
	name := varNameRe.FindString(body)
	if name == "" {
		return "${" + body + "}"
	}
	rest := body[len(name):]
	val, set := ip.env[name]
	empty := !set || val == ""
	switch {
	case rest == "":
		if !set {
			ip.missing[name] = true
		}
		return val
	case strings.HasPrefix(rest, ":-"):
		if empty {
			return ip.Expand(rest[2:])
		}
		return val
	case strings.HasPrefix(rest, "-"):
		if !set {
			return ip.Expand(rest[1:])
		}
		return val
	case strings.HasPrefix(rest, ":+"):
		if !empty {
			return ip.Expand(rest[2:])
		}
		return ""
	case strings.HasPrefix(rest, "+"):
		if set {
			return ip.Expand(rest[1:])
		}
		return ""
	case strings.HasPrefix(rest, ":?"):
		if empty {
			ip.required[name] = ip.Expand(rest[2:])
		}
		return val
	case strings.HasPrefix(rest, "?"):
		if !set {
			ip.required[name] = ip.Expand(rest[1:])
		}
		return val
	}
	return "${" + body + "}"
}

// ---- .env files ----

// ParseDotenv parses a dotenv file the way Compose reads .env and env_file
// entries: KEY=VALUE lines, an optional "export" prefix, "#" comments, single
// quotes (literal), double quotes (with \n \r \t \" \\ escapes), values that
// continue over several lines inside quotes, and ${VAR} interpolation of
// unquoted and double-quoted values against the keys defined before them (and
// base). Lines that are not assignments are reported in the returned notes.
func ParseDotenv(src string, base map[string]string) (map[string]string, []string) {
	out := map[string]string{}
	var notes []string
	env := map[string]string{}
	for k, v := range base {
		env[k] = v
	}
	src = strings.ReplaceAll(strings.ReplaceAll(src, "\r\n", "\n"), "\r", "\n")
	src = strings.TrimPrefix(src, "\ufeff")
	lines := strings.Split(src, "\n")
	for n := 0; n < len(lines); n++ {
		line := strings.TrimSpace(lines[n])
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		eq := strings.IndexByte(line, '=')
		if eq <= 0 {
			if varNameRe.MatchString(line) && varNameRe.FindString(line) == line {
				continue // "KEY" alone: passes through from the shell; nothing to import
			}
			notes = append(notes, fmt.Sprintf("line %d is not KEY=VALUE and was skipped", n+1))
			continue
		}
		key := strings.TrimSpace(line[:eq])
		if varNameRe.FindString(key) != key {
			notes = append(notes, fmt.Sprintf("line %d: %q is not a valid variable name and was skipped", n+1, key))
			continue
		}
		raw := strings.TrimLeft(line[eq+1:], " \t")
		var val string
		switch {
		case strings.HasPrefix(raw, `"`):
			body, end, ok := scanQuoted(raw, lines, &n, '"')
			if !ok {
				notes = append(notes, fmt.Sprintf("line %d: unterminated quote for %s", n+1, key))
				continue
			}
			_ = end
			val = newInterpolator(env).Expand(unescapeDouble(body))
		case strings.HasPrefix(raw, `'`):
			body, _, ok := scanQuoted(raw, lines, &n, '\'')
			if !ok {
				notes = append(notes, fmt.Sprintf("line %d: unterminated quote for %s", n+1, key))
				continue
			}
			val = body
		default:
			if i := strings.Index(raw, " #"); i >= 0 {
				raw = raw[:i]
			}
			val = newInterpolator(env).Expand(strings.TrimSpace(raw))
		}
		out[key] = val
		env[key] = val
	}
	return out, notes
}

// scanQuoted reads a quoted value that starts at raw[0], continuing over
// following lines (advancing *n) until the closing quote. It returns the text
// between the quotes.
func scanQuoted(raw string, lines []string, n *int, q byte) (string, int, bool) {
	var b strings.Builder
	cur := raw[1:]
	for {
		for i := 0; i < len(cur); i++ {
			if cur[i] == '\\' && q == '"' && i+1 < len(cur) {
				b.WriteByte(cur[i])
				b.WriteByte(cur[i+1])
				i++
				continue
			}
			if cur[i] == q {
				return b.String(), i, true
			}
			b.WriteByte(cur[i])
		}
		if *n+1 >= len(lines) {
			return "", 0, false
		}
		b.WriteByte('\n')
		*n++
		cur = lines[*n]
	}
}

func unescapeDouble(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i+1 >= len(s) {
			b.WriteByte(s[i])
			continue
		}
		i++
		switch s[i] {
		case 'n':
			b.WriteByte('\n')
		case 'r':
			b.WriteByte('\r')
		case 't':
			b.WriteByte('\t')
		case '"', '\\', '$':
			b.WriteByte(s[i])
		default:
			b.WriteByte('\\')
			b.WriteByte(s[i])
		}
	}
	return b.String()
}
