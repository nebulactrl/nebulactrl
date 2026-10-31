// Package query is the Go port of the log search language the web UI
// implements in apps/web/src/features/logs/search (query.ts, match.ts,
// line.ts, level.ts). The agent evaluates history queries and drain filters
// with it, so it must give the same answer as the TypeScript code for every
// query and line; tests/fixtures/logquery/cases.json pins both.
//
// The grammar: free text (AND by default, case-insensitive substring), quoted
// phrases, key:value facets (service, level, stream, replica, status,
// deployment), @attr.path:value attributes of JSON/logfmt lines, -term / NOT,
// OR, AND, parentheses, key:(a OR b), `*` wildcards, @n:>500, @n:[400 TO 499].
package query

import (
	"regexp"
	"strings"
)

// ParseError is a mistake in a query; Start and End are byte offsets.
type ParseError struct {
	Start, End int
	Message    string
}

type tokenKind uint8

const (
	tkLParen tokenKind = iota
	tkRParen
	tkOr
	tkAnd
	tkNot
	tkMinus
	tkText
	tkPhrase
	tkKey
	tkValue
	tkRange
	// tkBogus is what the TypeScript tokenizer produces for the words that
	// name an Object.prototype property (`constructor`, `toString`, ...): its
	// keyword table is a plain object, so those lookups are truthy. The parser
	// reports them as "Unexpected <word>". Kept for exact parity.
	tkBogus
)

type rangeToken struct {
	lo, hi       string
	loInc, hiInc bool
	closed, ok   bool
}

type token struct {
	kind         tokenKind
	start, end   int
	text         string
	parts        []string
	attr         bool
	quoted       bool
	unterminated bool
	op           string
	rng          *rangeToken
	group        bool
}

func isSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }

func isKeyStart(c byte) bool { return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') }

func isKeyPart(c byte) bool {
	return isKeyStart(c) || (c >= '0' && c <= '9') || c == '.' || c == '-'
}

// keyAt matches /@?[A-Za-z_][\w.-]*:/y at i and returns its length, or 0.
func keyAt(in string, i int) int {
	j := i
	if j < len(in) && in[j] == '@' {
		j++
	}
	if j >= len(in) || !isKeyStart(in[j]) {
		return 0
	}
	j++
	for j < len(in) && isKeyPart(in[j]) {
		j++
	}
	if j < len(in) && in[j] == ':' {
		return j + 1 - i
	}
	return 0
}

type word struct {
	end   int
	text  string
	parts []string
}

// readWord reads up to whitespace or a parenthesis. `\x` is a literal x, so
// `\*` is not a wildcard.
func readWord(in string, from int) word {
	var parts []string
	var cur []byte
	i := from
	for i < len(in) {
		c := in[i]
		if isSpace(c) || c == '(' || c == ')' {
			break
		}
		if c == '\\' && i+1 < len(in) {
			cur = append(cur, in[i+1])
			i += 2
			continue
		}
		if c == '*' {
			parts = append(parts, string(cur))
			cur = cur[:0]
		} else {
			cur = append(cur, c)
		}
		i++
	}
	parts = append(parts, string(cur))
	return word{end: i, text: strings.Join(parts, "*"), parts: parts}
}

// readQuoted reads a quoted string starting at the opening quote.
func readQuoted(in string, from int) (end int, text string, unterminated bool) {
	var cur []byte
	i := from + 1
	for i < len(in) {
		c := in[i]
		if c == '\\' && i+1 < len(in) {
			cur = append(cur, in[i+1])
			i += 2
			continue
		}
		if c == '"' {
			return i + 1, string(cur), false
		}
		cur = append(cur, c)
		i++
	}
	return i, string(cur), true
}

var rangeInner = regexp.MustCompile(`(?i)^[` + jsWS + `]*([^` + jsWS + `]+)[` + jsWS + `]+TO[` + jsWS + `]+([^` + jsWS + `]+)[` + jsWS + `]*$`)

func readRange(in string, from int) token {
	loInc := in[from] == '['
	closeAt := -1
	for i := from + 1; i < len(in); i++ {
		if in[i] == ']' || in[i] == '}' {
			closeAt = i
			break
		}
	}
	closed := closeAt != -1
	innerEnd := len(in)
	if closed {
		innerEnd = closeAt
	}
	m := rangeInner.FindStringSubmatch(in[from+1 : innerEnd])
	end := len(in)
	if closed {
		end = closeAt + 1
	}
	r := &rangeToken{loInc: loInc, hiInc: true, closed: closed, ok: m != nil}
	if closed {
		r.hiInc = in[closeAt] == ']'
	}
	if m != nil {
		r.lo, r.hi = m[1], m[2]
	}
	text := in[from:end]
	return token{kind: tkRange, start: from, end: end, text: text, parts: []string{text}, rng: r}
}

// readValue reads a value after `key:` or inside `key:( ... )`: quoted, a
// range, `>5`, or a word.
func readValue(in string, from int) token {
	c := in[from]
	if c == '"' {
		end, text, unterminated := readQuoted(in, from)
		return token{kind: tkValue, start: from, end: end, text: text, parts: []string{text}, quoted: true, unterminated: unterminated}
	}
	if c == '[' || c == '{' {
		return readRange(in, from)
	}
	opEnd := from
	op := ""
	if c == '>' || c == '<' {
		opEnd = from + 1
		if from+1 < len(in) && in[from+1] == '=' {
			opEnd = from + 2
		}
		op = in[from:opEnd]
	}
	w := readWord(in, opEnd)
	return token{kind: tkValue, start: from, end: w.end, text: w.text, parts: w.parts, op: op}
}

// keywordKind mirrors the TypeScript KEYWORDS lookup, prototype names included.
func keywordKind(raw string) (tokenKind, bool) {
	switch raw {
	case "OR":
		return tkOr, true
	case "AND":
		return tkAnd, true
	case "NOT":
		return tkNot, true
	case "constructor", "__defineGetter__", "__defineSetter__", "hasOwnProperty",
		"__lookupGetter__", "__lookupSetter__", "isPrototypeOf", "propertyIsEnumerable",
		"toString", "valueOf", "__proto__", "toLocaleString":
		return tkBogus, true
	}
	return 0, false
}

func punct(kind tokenKind, at int, text string) token {
	return token{kind: kind, start: at, end: at + 1, text: text, parts: []string{text}}
}

// tokenize splits the input into tokens with positions. Whitespace is skipped.
func tokenize(in string) []token {
	var out []token
	// true: inside `key:( ... )`, where words are values of that key.
	var groups []bool
	i := 0
	for i < len(in) {
		c := in[i]
		if isSpace(c) {
			i++
			continue
		}
		inGroup := len(groups) > 0 && groups[len(groups)-1]
		if c == '(' {
			out = append(out, punct(tkLParen, i, "("))
			groups = append(groups, inGroup)
			i++
			continue
		}
		if c == ')' {
			out = append(out, punct(tkRParen, i, ")"))
			if len(groups) > 0 {
				groups = groups[:len(groups)-1]
			}
			i++
			continue
		}
		if c == '-' && i+1 < len(in) {
			next := in[i+1]
			if !isSpace(next) && next != ')' && next != '-' {
				out = append(out, punct(tkMinus, i, "-"))
				i++
				continue
			}
		}
		if inGroup {
			w := readValue(in, i)
			raw := in[w.start:w.end]
			if kind, ok := keywordKind(raw); ok && !w.quoted {
				out = append(out, token{kind: kind, start: i, end: w.end, text: raw, parts: []string{raw}})
			} else {
				out = append(out, w)
			}
			i = w.end
			continue
		}
		if c == '"' {
			end, text, unterminated := readQuoted(in, i)
			out = append(out, token{kind: tkPhrase, start: i, end: end, text: text, parts: []string{text}, quoted: true, unterminated: unterminated})
			i = end
			continue
		}
		if n := keyAt(in, i); n > 0 && !strings.HasPrefix(in[i+n:], "//") {
			at := in[i] == '@'
			name := in[i : i+n-1]
			if at {
				name = name[1:]
			}
			colon := i + n
			out = append(out, token{kind: tkKey, start: i, end: colon, text: name, parts: []string{name}, attr: at})
			i = colon
			if i >= len(in) || isSpace(in[i]) || in[i] == ')' {
				continue
			}
			if in[i] == '(' {
				t := punct(tkLParen, i, "(")
				t.group = true
				out = append(out, t)
				groups = append(groups, true)
				i++
				continue
			}
			v := readValue(in, i)
			out = append(out, v)
			i = v.end
			continue
		}
		w := readWord(in, i)
		raw := in[i:w.end]
		if kind, ok := keywordKind(raw); ok {
			out = append(out, token{kind: kind, start: i, end: w.end, text: raw, parts: []string{raw}})
		} else {
			out = append(out, token{kind: tkText, start: i, end: w.end, text: w.text, parts: w.parts})
		}
		i = w.end
	}
	return out
}

// --- AST ----------------------------------------------------------------------

type specKind uint8

const (
	specEq specKind = iota
	specCmp
	specRange
)

type valueSpec struct {
	kind  specKind
	parts []string // eq
	op    string   // cmp
	n     float64  // cmp
	// range: an open bound has hasLo/hasHi false.
	lo, hi       float64
	hasLo, hasHi bool
	loInc, hiInc bool
}

type nodeKind uint8

const (
	nodeText nodeKind = iota
	nodeField
	nodeAnd
	nodeOr
	nodeNot
)

type node struct {
	kind       nodeKind
	start, end int
	// text
	parts  []string
	phrase bool
	// field
	key  string
	attr bool
	spec valueSpec
	// and, or
	children []*node
	// not
	child *node
}

type fieldContext struct {
	key  string
	attr bool
}

type parser struct {
	in     string
	tokens []token
	i      int
	errs   []ParseError
}

func (p *parser) eof() bool { return p.i >= len(p.tokens) }

func (p *parser) peek() *token {
	if p.i < len(p.tokens) {
		return &p.tokens[p.i]
	}
	return nil
}

func (p *parser) peekKind(kind tokenKind) bool {
	t := p.peek()
	return t != nil && t.kind == kind
}

func (p *parser) fail(start, end int, msg string) {
	p.errs = append(p.errs, ParseError{Start: start, End: end, Message: msg})
}

// skipStray reports a `)` with no `(` and moves on.
func (p *parser) skipStray() {
	t := p.tokens[p.i]
	p.i++
	p.fail(t.start, t.end, "Unmatched closing parenthesis")
}

// parseOr parses alternatives, the loosest binding. It stops at `)` or the end.
func (p *parser) parseOr(ctx *fieldContext) *node {
	var parts []*node
	for p.peekKind(tkOr) {
		t := p.tokens[p.i]
		p.fail(t.start, t.end, "OR needs a term before it")
		p.i++
	}
	if first := p.parseAnd(ctx); first != nil {
		parts = append(parts, first)
	}
	for p.peekKind(tkOr) {
		or := p.tokens[p.i]
		p.i++
		if next := p.parseAnd(ctx); next != nil {
			parts = append(parts, next)
		} else {
			p.fail(or.start, or.end, "OR needs a term after it")
		}
	}
	return combine(nodeOr, parts)
}

func (p *parser) parseAnd(ctx *fieldContext) *node {
	var items []*node
	for {
		t := p.peek()
		if t == nil || t.kind == tkOr || t.kind == tkRParen {
			break
		}
		if t.kind == tkAnd {
			tok := *t
			p.i++
			next := p.peek()
			if len(items) == 0 || next == nil || next.kind == tkOr || next.kind == tkRParen {
				p.fail(tok.start, tok.end, "AND needs a term on both sides")
			}
			continue
		}
		if n := p.parseUnary(ctx); n != nil {
			items = append(items, n)
		}
	}
	return combine(nodeAnd, items)
}

func (p *parser) parseUnary(ctx *fieldContext) *node {
	t := p.tokens[p.i]
	if t.kind == tkNot || t.kind == tkMinus {
		p.i++
		next := p.peek()
		if next == nil || next.kind == tkOr || next.kind == tkAnd || next.kind == tkRParen {
			p.fail(t.start, t.end, "Nothing to negate")
			return nil
		}
		child := p.parseUnary(ctx)
		if child == nil {
			return nil
		}
		return &node{kind: nodeNot, child: child, start: t.start, end: child.end}
	}
	return p.parsePrimary(ctx)
}

func (p *parser) parsePrimary(ctx *fieldContext) *node {
	t := p.tokens[p.i]
	p.i++
	switch t.kind {
	case tkLParen:
		if p.peekKind(tkRParen) {
			p.i++
			p.fail(t.start, t.end, "Empty parentheses")
			return nil
		}
		inner := p.parseOr(ctx)
		if p.peekKind(tkRParen) {
			p.i++
		} else {
			p.fail(t.start, t.end, "Missing closing parenthesis")
		}
		return inner
	case tkText, tkPhrase:
		if t.unterminated {
			p.fail(t.start, t.end, "Missing closing quote")
		}
		return &node{kind: nodeText, parts: t.parts, phrase: t.kind == tkPhrase, start: t.start, end: t.end}
	case tkKey:
		return p.parseKey(t)
	case tkValue, tkRange:
		if ctx != nil {
			return p.field(ctx.key, ctx.attr, t, t.start, t.end)
		}
		return &node{kind: nodeText, parts: t.parts, start: t.start, end: t.end}
	default:
		// Operators are consumed by their parents; a stray one is skipped.
		p.fail(t.start, t.end, "Unexpected "+t.text)
		return nil
	}
}

func isFacetKey(key string) bool {
	switch key {
	case "level", "service", "stream", "replica", "status", "deployment":
		return true
	}
	return false
}

func unknownFacet(key string) string {
	return `Unknown facet "` + key + `". Write @` + key + ` for a log attribute`
}

func (p *parser) parseKey(key token) *node {
	known := key.attr || isFacetKey(key.text)
	raw := p.in[key.start:key.end]
	next := p.peek()
	if next != nil && next.kind == tkLParen && next.group && next.start == key.end {
		p.i++
		lp := *next
		if p.peekKind(tkRParen) {
			p.i++
			p.fail(lp.start, lp.end, "Empty parentheses")
			return nil
		}
		if !known {
			p.fail(key.start, key.end, unknownFacet(key.text))
		}
		inner := p.parseOr(&fieldContext{key: key.text, attr: key.attr || !known})
		if p.peekKind(tkRParen) {
			p.i++
		} else {
			p.fail(lp.start, lp.end, "Missing closing parenthesis")
		}
		return inner
	}
	if next != nil && (next.kind == tkValue || next.kind == tkRange) && next.start == key.end {
		val := *next
		p.i++
		if !known {
			// `localhost:8080`, `http:x`: prose, not a facet. Search it as text.
			p.fail(key.start, key.end, unknownFacet(key.text)+"; searching it as text")
			text := p.in[key.start:val.end]
			return &node{kind: nodeText, parts: []string{text}, phrase: true, start: key.start, end: val.end}
		}
		return p.field(key.text, key.attr, val, key.start, val.end)
	}
	if !known {
		// "Error:" at the end of a sentence is text.
		return &node{kind: nodeText, parts: []string{raw}, phrase: true, start: key.start, end: key.end}
	}
	p.fail(key.start, key.end, "Add a value after "+raw)
	return nil
}

func (p *parser) field(key string, attr bool, value token, start, end int) *node {
	spec, ok := p.valueSpec(value)
	if !ok {
		return nil
	}
	return &node{kind: nodeField, key: key, attr: attr, spec: spec, start: start, end: end}
}

func (p *parser) valueSpec(t token) (valueSpec, bool) {
	if t.kind == tkRange {
		r := t.rng
		if !r.ok {
			p.fail(t.start, t.end, "Write a range as [400 TO 499]")
			return valueSpec{}, false
		}
		if !r.closed {
			p.fail(t.start, t.end, "Missing closing ]")
			return valueSpec{}, false
		}
		lo, loOpen, loOK := bound(r.lo)
		hi, hiOpen, hiOK := bound(r.hi)
		if !loOK || !hiOK {
			p.fail(t.start, t.end, "Range bounds are numbers or *")
			return valueSpec{}, false
		}
		return valueSpec{kind: specRange, lo: lo, hi: hi, hasLo: !loOpen, hasHi: !hiOpen, loInc: r.loInc, hiInc: r.hiInc}, true
	}
	if t.unterminated {
		p.fail(t.start, t.end, "Missing closing quote")
	}
	if t.op != "" {
		n, ok := jsToNumber(t.text)
		if t.text == "" || !ok {
			p.fail(t.start, t.end, "Expected a number after "+t.op)
			return valueSpec{}, false
		}
		return valueSpec{kind: specCmp, op: t.op, n: n}, true
	}
	return valueSpec{kind: specEq, parts: t.parts}, true
}

// bound parses a range bound: "*" is open, a number is closed, anything else is invalid.
func bound(text string) (n float64, open, ok bool) {
	if text == "*" {
		return 0, true, true
	}
	n, ok = jsToNumber(text)
	return n, false, ok
}

func combine(kind nodeKind, parts []*node) *node {
	switch len(parts) {
	case 0:
		return nil
	case 1:
		return parts[0]
	}
	return &node{kind: kind, children: parts, start: parts[0].start, end: parts[len(parts)-1].end}
}

// --- Public ---------------------------------------------------------------------

// Query is a parsed query.
type Query struct {
	errs []ParseError
	m    Matcher
}

// Parse parses q. It never fails: mistakes are returned by Errors and the
// broken pieces are dropped, so a query being typed still filters by the
// parts that are valid.
func Parse(q string) *Query {
	p := &parser{in: q, tokens: tokenize(q)}
	var parts []*node
	// Only a stray `)` stops parseOr early; report it and carry on.
	for !p.eof() {
		if n := p.parseOr(nil); n != nil {
			parts = append(parts, n)
		}
		if !p.eof() {
			p.skipStray()
		}
	}
	return &Query{errs: p.errs, m: compileNode(combine(nodeAnd, parts))}
}

// Matcher returns the compiled query; it is never nil. A query that is empty
// or entirely invalid matches every line.
func (q *Query) Matcher() Matcher { return q.m }

// Errors returns the mistakes found while parsing, in source order of
// discovery. It is empty for a valid query.
func (q *Query) Errors() []ParseError { return q.errs }

// Compile parses q and compiles the valid part of it.
func Compile(q string) (m Matcher, errs []ParseError) {
	parsed := Parse(q)
	return parsed.Matcher(), parsed.Errors()
}
