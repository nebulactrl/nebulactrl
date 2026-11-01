package main

// A deliberately small TOML reader. Dokploy's template.toml files only use
// tables, arrays of tables, strings (basic, literal and multi-line), integers
// (with underscores), booleans, arrays and inline tables. The repository has
// no TOML dependency and this tool must not add one, so the subset is parsed
// here. Values decode to map[string]any, []any, string, int64, float64, bool.

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

type tomlParser struct {
	s    string
	pos  int
	line int
}

func parseTOML(src string) (map[string]any, error) {
	p := &tomlParser{s: strings.ReplaceAll(src, "\r\n", "\n"), line: 1}
	root := map[string]any{}
	cur := root
	for {
		p.skipSpaceAndComments(true)
		if p.eof() {
			return root, nil
		}
		if p.peek() == '[' {
			arr := strings.HasPrefix(p.s[p.pos:], "[[")
			if arr {
				p.pos += 2
			} else {
				p.pos++
			}
			path, err := p.keyPath(']')
			if err != nil {
				return nil, err
			}
			if !p.consume("]") || (arr && !p.consume("]")) {
				return nil, p.errf("unterminated table header")
			}
			cur, err = descend(root, path, arr)
			if err != nil {
				return nil, p.errf("%v", err)
			}
			if err := p.endOfLine(); err != nil {
				return nil, err
			}
			continue
		}
		path, err := p.keyPath('=')
		if err != nil {
			return nil, err
		}
		p.skipSpaces()
		if !p.consume("=") {
			return nil, p.errf("expected '=' after key %q", strings.Join(path, "."))
		}
		p.skipSpaces()
		v, err := p.value()
		if err != nil {
			return nil, err
		}
		dst := cur
		for _, k := range path[:len(path)-1] {
			next, ok := dst[k].(map[string]any)
			if !ok {
				next = map[string]any{}
				dst[k] = next
			}
			dst = next
		}
		dst[path[len(path)-1]] = v
		if err := p.endOfLine(); err != nil {
			return nil, err
		}
	}
}

// descend walks (creating) the table path; for array tables it appends a new
// element and returns it.
func descend(root map[string]any, path []string, arr bool) (map[string]any, error) {
	cur := root
	for i, k := range path {
		last := i == len(path)-1
		switch ex := cur[k].(type) {
		case nil:
			if last && arr {
				m := map[string]any{}
				cur[k] = []any{m}
				return m, nil
			}
			m := map[string]any{}
			cur[k] = m
			cur = m
		case map[string]any:
			if last && arr {
				return nil, fmt.Errorf("table %q redefined as array", k)
			}
			cur = ex
		case []any:
			if last && arr {
				m := map[string]any{}
				cur[k] = append(ex, m)
				return m, nil
			}
			if len(ex) == 0 {
				return nil, fmt.Errorf("empty array %q", k)
			}
			m, ok := ex[len(ex)-1].(map[string]any)
			if !ok {
				return nil, fmt.Errorf("%q is not a table array", k)
			}
			cur = m
		default:
			return nil, fmt.Errorf("key %q is not a table", k)
		}
	}
	return cur, nil
}

func (p *tomlParser) eof() bool { return p.pos >= len(p.s) }
func (p *tomlParser) peek() byte {
	if p.eof() {
		return 0
	}
	return p.s[p.pos]
}
func (p *tomlParser) consume(tok string) bool {
	if strings.HasPrefix(p.s[p.pos:], tok) {
		p.pos += len(tok)
		return true
	}
	return false
}
func (p *tomlParser) errf(format string, a ...any) error {
	return fmt.Errorf("toml line %d: %s", p.line, fmt.Sprintf(format, a...))
}

func (p *tomlParser) skipSpaces() {
	for !p.eof() && (p.s[p.pos] == ' ' || p.s[p.pos] == '\t') {
		p.pos++
	}
}

// skipSpaceAndComments skips blanks, comments and (when nl) newlines.
func (p *tomlParser) skipSpaceAndComments(nl bool) {
	for !p.eof() {
		switch c := p.s[p.pos]; {
		case c == ' ' || c == '\t':
			p.pos++
		case c == '\n' && nl:
			p.pos++
			p.line++
		case c == '#':
			for !p.eof() && p.s[p.pos] != '\n' {
				p.pos++
			}
		default:
			return
		}
	}
}

func (p *tomlParser) endOfLine() error {
	p.skipSpaceAndComments(false)
	if p.eof() {
		return nil
	}
	if p.s[p.pos] != '\n' {
		return p.errf("unexpected %q after value", p.s[p.pos])
	}
	return nil
}

// keyPath reads a dotted key up to (not including) the terminator byte.
func (p *tomlParser) keyPath(term byte) ([]string, error) {
	var path []string
	for {
		p.skipSpaces()
		var k string
		switch c := p.peek(); {
		case c == '"':
			s, err := p.basicString()
			if err != nil {
				return nil, err
			}
			k = s
		case c == '\'':
			s, err := p.literalString()
			if err != nil {
				return nil, err
			}
			k = s
		default:
			start := p.pos
			for !p.eof() && isBareKey(p.s[p.pos]) {
				p.pos++
			}
			if start == p.pos {
				return nil, p.errf("invalid key near %q", p.near())
			}
			k = p.s[start:p.pos]
		}
		path = append(path, k)
		p.skipSpaces()
		if p.peek() == '.' {
			p.pos++
			continue
		}
		if p.peek() == term {
			return path, nil
		}
		return nil, p.errf("unexpected %q in key", p.peek())
	}
}

func (p *tomlParser) near() string {
	end := min(p.pos+20, len(p.s))
	return p.s[p.pos:end]
}

func isBareKey(c byte) bool {
	return c == '_' || c == '-' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

func (p *tomlParser) value() (any, error) {
	switch c := p.peek(); {
	case strings.HasPrefix(p.s[p.pos:], `"""`):
		return p.multiBasic()
	case strings.HasPrefix(p.s[p.pos:], `'''`):
		return p.multiLiteral()
	case c == '"':
		return p.basicString()
	case c == '\'':
		return p.literalString()
	case c == '[':
		return p.array()
	case c == '{':
		return p.inlineTable()
	}
	start := p.pos
	for !p.eof() {
		c := p.s[p.pos]
		if c == ',' || c == ']' || c == '}' || c == '\n' || c == '#' || c == ' ' || c == '\t' {
			break
		}
		p.pos++
	}
	tok := p.s[start:p.pos]
	switch tok {
	case "true":
		return true, nil
	case "false":
		return false, nil
	case "":
		return nil, p.errf("missing value")
	}
	clean := strings.ReplaceAll(tok, "_", "")
	if n, err := strconv.ParseInt(clean, 0, 64); err == nil {
		return n, nil
	}
	if f, err := strconv.ParseFloat(clean, 64); err == nil {
		return f, nil
	}
	// Dates and other exotic scalars are kept as text; the translator never
	// interprets them.
	return tok, nil
}

func (p *tomlParser) basicString() (string, error) {
	p.pos++ // opening quote
	var b strings.Builder
	for !p.eof() {
		c := p.s[p.pos]
		switch c {
		case '"':
			p.pos++
			return b.String(), nil
		case '\n':
			return "", p.errf("newline in string")
		case '\\':
			if err := p.escape(&b); err != nil {
				return "", err
			}
		default:
			b.WriteByte(c)
			p.pos++
		}
	}
	return "", p.errf("unterminated string")
}

func (p *tomlParser) escape(b *strings.Builder) error {
	p.pos++ // backslash
	if p.eof() {
		return p.errf("dangling escape")
	}
	c := p.s[p.pos]
	p.pos++
	switch c {
	case 'n':
		b.WriteByte('\n')
	case 't':
		b.WriteByte('\t')
	case 'r':
		b.WriteByte('\r')
	case 'b':
		b.WriteByte('\b')
	case 'f':
		b.WriteByte('\f')
	case '"', '\\':
		b.WriteByte(c)
	case 'u', 'U':
		n := 4
		if c == 'U' {
			n = 8
		}
		if p.pos+n > len(p.s) {
			return p.errf("short unicode escape")
		}
		r, err := strconv.ParseUint(p.s[p.pos:p.pos+n], 16, 32)
		if err != nil || r > utf8.MaxRune || !utf8.ValidRune(rune(r)) {
			return p.errf("bad unicode escape")
		}
		b.WriteRune(rune(r))
		p.pos += n
	default:
		return p.errf("unknown escape \\%c", c)
	}
	return nil
}

func (p *tomlParser) literalString() (string, error) {
	p.pos++
	start := p.pos
	for !p.eof() && p.s[p.pos] != '\'' && p.s[p.pos] != '\n' {
		p.pos++
	}
	if p.peek() != '\'' {
		return "", p.errf("unterminated literal string")
	}
	s := p.s[start:p.pos]
	p.pos++
	return s, nil
}

func (p *tomlParser) multiBasic() (string, error) {
	p.pos += 3
	p.trimFirstNewline()
	var b strings.Builder
	for !p.eof() {
		if strings.HasPrefix(p.s[p.pos:], `"""`) {
			p.pos += 3
			// up to two extra quotes belong to the content
			for i := 0; i < 2 && p.peek() == '"'; i++ {
				b.WriteByte('"')
				p.pos++
			}
			return b.String(), nil
		}
		c := p.s[p.pos]
		if c == '\\' {
			// line-ending backslash trims the following whitespace
			rest := p.s[p.pos+1:]
			trim := strings.TrimLeft(rest, " \t")
			if strings.HasPrefix(trim, "\n") {
				ws := strings.TrimLeft(rest, " \t\n")
				p.line += strings.Count(rest[:len(rest)-len(ws)], "\n")
				p.pos = len(p.s) - len(ws)
				continue
			}
			if err := p.escape(&b); err != nil {
				return "", err
			}
			continue
		}
		if c == '\n' {
			p.line++
		}
		b.WriteByte(c)
		p.pos++
	}
	return "", p.errf("unterminated multi-line string")
}

func (p *tomlParser) multiLiteral() (string, error) {
	p.pos += 3
	p.trimFirstNewline()
	end := strings.Index(p.s[p.pos:], "'''")
	if end < 0 {
		return "", p.errf("unterminated multi-line literal string")
	}
	s := p.s[p.pos : p.pos+end]
	p.line += strings.Count(s, "\n")
	p.pos += end + 3
	for i := 0; i < 2 && p.peek() == '\''; i++ {
		s += "'"
		p.pos++
	}
	return s, nil
}

func (p *tomlParser) trimFirstNewline() {
	if p.peek() == '\n' {
		p.pos++
		p.line++
	}
}

func (p *tomlParser) array() (any, error) {
	p.pos++
	out := []any{}
	for {
		p.skipSpaceAndComments(true)
		if p.consume("]") {
			return out, nil
		}
		v, err := p.value()
		if err != nil {
			return nil, err
		}
		out = append(out, v)
		p.skipSpaceAndComments(true)
		if p.consume(",") {
			continue
		}
		if p.consume("]") {
			return out, nil
		}
		return nil, p.errf("expected ',' or ']' in array")
	}
}

func (p *tomlParser) inlineTable() (any, error) {
	p.pos++
	out := map[string]any{}
	for {
		p.skipSpaceAndComments(true)
		if p.consume("}") {
			return out, nil
		}
		path, err := p.keyPath('=')
		if err != nil {
			return nil, err
		}
		p.skipSpaces()
		if !p.consume("=") {
			return nil, p.errf("expected '=' in inline table")
		}
		p.skipSpaces()
		v, err := p.value()
		if err != nil {
			return nil, err
		}
		dst := out
		for _, k := range path[:len(path)-1] {
			next, ok := dst[k].(map[string]any)
			if !ok {
				next = map[string]any{}
				dst[k] = next
			}
			dst = next
		}
		dst[path[len(path)-1]] = v
		p.skipSpaceAndComments(true)
		if p.consume(",") {
			continue
		}
		if p.consume("}") {
			return out, nil
		}
		return nil, p.errf("expected ',' or '}' in inline table")
	}
}
