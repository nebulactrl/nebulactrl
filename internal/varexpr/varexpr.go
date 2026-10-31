// Package varexpr parses and validates variable templates:
//
//	template   = { text | escape | expr }
//	escape     = "$${{"                    ; emits the literal characters "${{"
//	expr       = "${{" ws ( ref | ext | call ) ws "}}"
//	ref        = [ ns "." ] key            ; ns = service slug | "shared" | "team"
//	ext        = "vault:" integration "/" path "#" key   ; external secret manager
//	           | "doppler:" integration [ "/" project "/" config ] "#" key
//	ns         = [a-z0-9][a-z0-9-]*
//	key        = [A-Za-z_][A-Za-z0-9_]*
//	call       = fn "(" ws [ arg { ws "," ws arg } ] ws ")"
//	fn         = generator | transform
//	generator  = "secret" | "randomInt" | "uuid" | "randomBytes"
//	transform  = "urlencode" | "base64"
//	arg        = int | string | ident | ref | ext | call
//	ws         = { " " | "\t" }
//
// The parser is a hand-written scanner: a regular expression cannot handle
// quoted "}}" inside arguments. It never evaluates anything; generators run in
// the write path only (see Materialize), transforms in the resolver.
package varexpr

import (
	"fmt"
	"strconv"
	"strings"
)

// Limits on a single value.
const (
	MaxValueLen  = 64 * 1024
	MaxExprs     = 64
	MaxCallDepth = 3
)

// SyntaxError is a parse or validation failure at a byte offset.
type SyntaxError struct {
	Offset  int
	Message string
}

func (e *SyntaxError) Error() string { return fmt.Sprintf("offset %d: %s", e.Offset, e.Message) }

// ExtRef is a reference to a secret in an external secret manager:
// `${{vault:<integration>/<path>#<key>}}` (Vault, OpenBao, Infisical) or
// `${{doppler:<integration>/<project>/<config>#<key>}}` (Doppler; the project
// and config may be left out when the integration or its service token fixes
// them). Its value is fetched at deploy time and never stored.
type ExtRef struct {
	// Scheme is DopplerScheme for a Doppler reference; empty means ExtScheme
	// ("vault"), so the JSON form of a Vault reference carries no scheme.
	Scheme      string `json:"s,omitempty"`
	Integration string `json:"i"`
	// Path is the secret's path in the manager. For Doppler it is
	// "<project>/<config>", or empty to use the integration's defaults.
	Path string `json:"p"`
	Key  string `json:"k"`
}

// Doppler reports whether the reference is a Doppler one.
func (r ExtRef) Doppler() bool { return r.Scheme == DopplerScheme }

// SchemeName is the reference's scheme as written: "vault" or "doppler".
func (r ExtRef) SchemeName() string {
	if r.Doppler() {
		return DopplerScheme
	}
	return ExtScheme
}

// String renders the reference without the braces.
func (r ExtRef) String() string {
	if r.Doppler() {
		if r.Path == "" {
			return DopplerScheme + ":" + r.Integration + "#" + r.Key
		}
		return DopplerScheme + ":" + r.Integration + "/" + r.Path + "#" + r.Key
	}
	return ExtScheme + ":" + r.Integration + "/" + r.Path + "#" + r.Key
}

// Limits of an external reference.
const (
	MaxExtPathLen = 512
	MaxExtKeyLen  = 255
)

// isExtStart reports whether s begins an external reference.
func isExtStart(s string) bool {
	return strings.HasPrefix(s, ExtScheme+":") || strings.HasPrefix(s, DopplerScheme+":")
}

// ParseExtRef parses `vault:<integration>/<path>#<key>` or
// `doppler:<integration>[/<project>/<config>]#<key>` (no braces).
func ParseExtRef(s string) (ExtRef, error) {
	if rest, ok := strings.CutPrefix(s, DopplerScheme+":"); ok {
		return parseDopplerRef(rest)
	}
	rest, ok := strings.CutPrefix(s, ExtScheme+":")
	if !ok {
		return ExtRef{}, fmt.Errorf("an external reference starts with %q or %q", ExtScheme+":", DopplerScheme+":")
	}
	integ, rest, ok := strings.Cut(rest, "/")
	if !ok || len(integ) > 63 || !IsNamespace(integ) {
		return ExtRef{}, fmt.Errorf("expected vault:<integration>/<path>#<key>; the integration is a name of lowercase letters, digits and dashes")
	}
	i := strings.LastIndexByte(rest, '#')
	if i < 0 {
		return ExtRef{}, fmt.Errorf("missing #<key> (the field to read from the secret)")
	}
	path, key := rest[:i], rest[i+1:]
	if path == "" || len(path) > MaxExtPathLen {
		return ExtRef{}, fmt.Errorf("the secret path is empty or longer than %d characters", MaxExtPathLen)
	}
	for _, seg := range strings.Split(path, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return ExtRef{}, fmt.Errorf("the secret path has an empty, . or .. segment")
		}
	}
	for j := 0; j < len(path); j++ {
		c := path[j]
		if !(isTokenChar(c) || c == '.' || c == '/' || c == '~' || c == '@' || c == '+' || c == '=' || c == ':') {
			return ExtRef{}, fmt.Errorf("the secret path contains %q; allowed: letters, digits and . _ - / ~ @ + = :", string(c))
		}
	}
	if err := checkExtKey(key); err != nil {
		return ExtRef{}, err
	}
	return ExtRef{Integration: integ, Path: path, Key: key}, nil
}

func checkExtKey(key string) error {
	if key == "" || len(key) > MaxExtKeyLen {
		return fmt.Errorf("the key after # is empty or longer than %d characters", MaxExtKeyLen)
	}
	for j := range len(key) {
		c := key[j]
		if !(isTokenChar(c) || c == '.' || c == '@' || c == ':') {
			return fmt.Errorf("the key contains %q; allowed: letters, digits and . _ - @ :", string(c))
		}
	}
	return nil
}

// parseDopplerRef parses what follows `doppler:`:
// `<integration>[/<project>/<config>]#<key>`.
func parseDopplerRef(rest string) (ExtRef, error) {
	const form = "expected doppler:<integration>/<project>/<config>#<key> or doppler:<integration>#<key>"
	i := strings.LastIndexByte(rest, '#')
	if i < 0 {
		return ExtRef{}, fmt.Errorf("missing #<key> (the Doppler secret's name)")
	}
	head, key := rest[:i], rest[i+1:]
	integ, path, hasPath := strings.Cut(head, "/")
	if len(integ) > 63 || !IsNamespace(integ) {
		return ExtRef{}, fmt.Errorf("%s; the integration is a name of lowercase letters, digits and dashes", form)
	}
	if hasPath {
		segs := strings.Split(path, "/")
		if len(segs) != 2 {
			return ExtRef{}, fmt.Errorf("%s: the path is the Doppler project and config, like backend/prd", form)
		}
		for _, seg := range segs {
			if seg == "" || seg == "." || seg == ".." || len(seg) > 64 {
				return ExtRef{}, fmt.Errorf("the Doppler project and config are 1-64 characters, not empty, . or ..")
			}
			for j := range len(seg) {
				if c := seg[j]; !(isTokenChar(c) || c == '.') {
					return ExtRef{}, fmt.Errorf("the Doppler project or config contains %q; allowed: letters, digits and . _ -", string(c))
				}
			}
		}
	}
	if err := checkExtKey(key); err != nil {
		return ExtRef{}, err
	}
	return ExtRef{Scheme: DopplerScheme, Integration: integ, Path: path, Key: key}, nil
}

// Ref is a variable reference: `${{KEY}}` (NS == "") or `${{ns.KEY}}`.
type Ref struct {
	NS  string
	Key string
}

// String renders the reference without the braces.
func (r Ref) String() string {
	if r.NS == "" {
		return r.Key
	}
	return r.NS + "." + r.Key
}

// ArgKind is the kind of a call argument.
type ArgKind int

// Argument kinds.
const (
	ArgInt ArgKind = iota
	ArgString
	ArgIdent
	ArgRef
	ArgCall
	ArgExt
)

// Arg is one call argument.
type Arg struct {
	Kind ArgKind
	Int  int64
	Str  string // ArgString (decoded) and ArgIdent (the identifier)
	Ref  Ref    // ArgRef
	Call *Call  // ArgCall
	Ext  ExtRef // ArgExt
}

// Call is a function call such as secret(32).
type Call struct {
	Fn   string
	Args []Arg
}

// Generator and transform names.
const (
	FnSecret      = "secret"
	FnRandomInt   = "randomInt"
	FnUUID        = "uuid"
	FnRandomBytes = "randomBytes"
	FnURLEncode   = "urlencode"
	FnBase64      = "base64"
)

// IsGenerator reports whether fn is a generator (runs once, at write time).
func IsGenerator(fn string) bool {
	switch fn {
	case FnSecret, FnRandomInt, FnUUID, FnRandomBytes:
		return true
	}
	return false
}

// IsTransform reports whether fn is a pure transform (runs at resolve time).
func IsTransform(fn string) bool { return fn == FnURLEncode || fn == FnBase64 }

// Part is a literal text run or an expression of a template.
type Part struct {
	// Raw is the exact source text of the part, including `$${{` escapes.
	Raw string
	// Text is the decoded literal text (Expr == nil only).
	Text string
	// Expr is set for `${{ … }}` parts.
	Expr *Expr
	// Offset is the byte offset of the part in the source.
	Offset int
}

// Expr is a parsed `${{ … }}` expression: exactly one of Ref, Ext and Call is set.
type Expr struct {
	Ref  *Ref
	Ext  *ExtRef
	Call *Call
}

// Template is a parsed value.
type Template struct {
	Parts []Part
}

// Exprs returns the expressions in source order.
func (t *Template) Exprs() []*Expr {
	var out []*Expr
	for _, p := range t.Parts {
		if p.Expr != nil {
			out = append(out, p.Expr)
		}
	}
	return out
}

// Refs returns every reference, including those nested in transforms.
func (t *Template) Refs() []Ref {
	var out []Ref
	for _, e := range t.Exprs() {
		collectRefs(e, &out)
	}
	return out
}

// ExtRefs returns every external secret manager reference, including those
// nested in transforms.
func (t *Template) ExtRefs() []ExtRef {
	var out []ExtRef
	for _, e := range t.Exprs() {
		if e.Ext != nil {
			out = append(out, *e.Ext)
			continue
		}
		if e.Call == nil {
			continue
		}
		var walk func(c *Call)
		walk = func(c *Call) {
			for _, a := range c.Args {
				switch a.Kind {
				case ArgExt:
					out = append(out, a.Ext)
				case ArgCall:
					walk(a.Call)
				}
			}
		}
		walk(e.Call)
	}
	return out
}

func collectRefs(e *Expr, out *[]Ref) {
	if e.Ext != nil {
		return
	}
	if e.Ref != nil {
		*out = append(*out, *e.Ref)
		return
	}
	var walk func(c *Call)
	walk = func(c *Call) {
		for _, a := range c.Args {
			switch a.Kind {
			case ArgRef:
				*out = append(*out, a.Ref)
			case ArgIdent:
				if IsTransform(c.Fn) { // a bare identifier argument of a transform is a same-service reference
					*out = append(*out, Ref{Key: a.Str})
				}
			case ArgCall:
				walk(a.Call)
			}
		}
	}
	walk(e.Call)
}

// HasGenerators reports whether any top-level expression is a generator call.
func (t *Template) HasGenerators() bool {
	for _, e := range t.Exprs() {
		if e.Call != nil && IsGenerator(e.Call.Fn) {
			return true
		}
	}
	return false
}

// Parse scans s into a Template. Syntax problems return *SyntaxError.
func Parse(s string) (*Template, error) {
	if len(s) > MaxValueLen {
		return nil, &SyntaxError{Offset: MaxValueLen, Message: "value exceeds 64 KiB"}
	}
	p := &parser{s: s}
	t := &Template{}
	var text strings.Builder
	textStart := 0
	flush := func(end int) {
		if end > textStart {
			t.Parts = append(t.Parts, Part{Raw: s[textStart:end], Text: text.String(), Offset: textStart})
		}
		text.Reset()
	}
	exprs := 0
	for p.i < len(s) {
		switch {
		case strings.HasPrefix(s[p.i:], "$${{"):
			text.WriteString("${{")
			p.i += 4
		case strings.HasPrefix(s[p.i:], "${{"):
			flush(p.i)
			start := p.i
			p.i += 3
			e, err := p.expr()
			if err != nil {
				return nil, err
			}
			exprs++
			if exprs > MaxExprs {
				return nil, &SyntaxError{Offset: start, Message: fmt.Sprintf("more than %d expressions in one value", MaxExprs)}
			}
			t.Parts = append(t.Parts, Part{Raw: s[start:p.i], Expr: e, Offset: start})
			textStart = p.i
		default:
			text.WriteByte(s[p.i])
			p.i++
		}
	}
	flush(len(s))
	return t, nil
}

type parser struct {
	s string
	i int
}

func (p *parser) errf(format string, a ...any) error {
	return &SyntaxError{Offset: p.i, Message: fmt.Sprintf(format, a...)}
}

func (p *parser) ws() {
	for p.i < len(p.s) && (p.s[p.i] == ' ' || p.s[p.i] == '\t') {
		p.i++
	}
}

func isTokenChar(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-'
}

func (p *parser) token() string {
	st := p.i
	for p.i < len(p.s) && isTokenChar(p.s[p.i]) {
		p.i++
	}
	return p.s[st:p.i]
}

// IsKey reports whether s is a valid variable key.
func IsKey(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z':
		case c >= '0' && c <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}

// IsNamespace reports whether s is a valid reference namespace (service slug,
// "shared" or "team").
func IsNamespace(s string) bool {
	if s == "" || s[0] == '-' {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
			return false
		}
	}
	return true
}

func (p *parser) expr() (*Expr, error) {
	p.ws()
	st := p.i
	if isExtStart(p.s[p.i:]) {
		x, err := p.ext()
		if err != nil {
			return nil, err
		}
		p.ws()
		if !strings.HasPrefix(p.s[p.i:], "}}") {
			return nil, p.errf("expected }} to close the expression")
		}
		p.i += 2
		return &Expr{Ext: &x}, nil
	}
	tok := p.token()
	if tok == "" {
		return nil, p.errf("expected a variable name or function")
	}
	var e *Expr
	switch {
	case p.i < len(p.s) && p.s[p.i] == '(':
		c, err := p.call(tok, 1)
		if err != nil {
			return nil, err
		}
		e = &Expr{Call: c}
	default:
		r, err := p.refFrom(tok, st)
		if err != nil {
			return nil, err
		}
		e = &Expr{Ref: &r}
	}
	p.ws()
	if !strings.HasPrefix(p.s[p.i:], "}}") {
		return nil, p.errf("expected }} to close the expression")
	}
	p.i += 2
	return e, nil
}

// ext scans an external reference at p.i: everything up to whitespace, a
// closing brace, a parenthesis, a comma or a quote.
func (p *parser) ext() (ExtRef, error) {
	st := p.i
	for p.i < len(p.s) {
		switch p.s[p.i] {
		case ' ', '\t', '\n', '\r', '}', ')', ',', '"':
			goto done
		}
		p.i++
	}
done:
	x, err := ParseExtRef(p.s[st:p.i])
	if err != nil {
		return ExtRef{}, &SyntaxError{Offset: st, Message: err.Error()}
	}
	return x, nil
}

// refFrom finishes a reference whose first token tok starts at st.
func (p *parser) refFrom(tok string, st int) (Ref, error) {
	if p.i < len(p.s) && p.s[p.i] == '.' {
		if tok == ExtScheme {
			return Ref{}, &SyntaxError{Offset: st, Message: "external secrets are written vault:<integration>/<path>#<key>, not vault.KEY"}
		}
		if !IsNamespace(tok) {
			return Ref{}, &SyntaxError{Offset: st, Message: fmt.Sprintf("invalid namespace %q (lowercase letters, digits, dashes)", tok)}
		}
		p.i++
		kst := p.i
		key := p.token()
		if !IsKey(key) {
			return Ref{}, &SyntaxError{Offset: kst, Message: fmt.Sprintf("invalid variable name %q", key)}
		}
		return Ref{NS: tok, Key: key}, nil
	}
	if !IsKey(tok) {
		return Ref{}, &SyntaxError{Offset: st, Message: fmt.Sprintf("invalid variable name %q", tok)}
	}
	return Ref{Key: tok}, nil
}

func (p *parser) call(fn string, depth int) (*Call, error) {
	if depth > MaxCallDepth {
		return nil, p.errf("functions nest at most %d levels", MaxCallDepth)
	}
	p.i++ // (
	c := &Call{Fn: fn}
	p.ws()
	if p.i < len(p.s) && p.s[p.i] == ')' {
		p.i++
		return c, nil
	}
	for {
		p.ws()
		a, err := p.arg(depth)
		if err != nil {
			return nil, err
		}
		c.Args = append(c.Args, a)
		p.ws()
		if p.i >= len(p.s) {
			return nil, p.errf("unterminated function call")
		}
		switch p.s[p.i] {
		case ',':
			p.i++
		case ')':
			p.i++
			return c, nil
		default:
			return nil, p.errf("expected , or ) in the argument list")
		}
	}
}

func (p *parser) arg(depth int) (Arg, error) {
	if p.i >= len(p.s) {
		return Arg{}, p.errf("unterminated function call")
	}
	c := p.s[p.i]
	switch {
	case isExtStart(p.s[p.i:]):
		x, err := p.ext()
		if err != nil {
			return Arg{}, err
		}
		return Arg{Kind: ArgExt, Ext: x}, nil
	case c == '"':
		p.i++
		var b strings.Builder
		for p.i < len(p.s) {
			ch := p.s[p.i]
			switch {
			case ch == '\\' && p.i+1 < len(p.s) && (p.s[p.i+1] == '"' || p.s[p.i+1] == '\\'):
				b.WriteByte(p.s[p.i+1])
				p.i += 2
			case ch == '"':
				p.i++
				return Arg{Kind: ArgString, Str: b.String()}, nil
			default:
				b.WriteByte(ch)
				p.i++
			}
		}
		return Arg{}, p.errf("unterminated string")
	case c == '-' || c >= '0' && c <= '9':
		st := p.i
		if c == '-' {
			p.i++
		}
		ds := p.i
		for p.i < len(p.s) && p.s[p.i] >= '0' && p.s[p.i] <= '9' {
			p.i++
		}
		// A token such as 1abc or a namespace like 3d.KEY is not an integer.
		if p.i == ds || (p.i < len(p.s) && isTokenChar(p.s[p.i])) {
			if c != '-' {
				p.i = st
				return p.identOrRef(depth)
			}
			return Arg{}, &SyntaxError{Offset: st, Message: "invalid number"}
		}
		n, err := strconv.ParseInt(p.s[st:p.i], 10, 64)
		if err != nil {
			return Arg{}, &SyntaxError{Offset: st, Message: "number out of range"}
		}
		return Arg{Kind: ArgInt, Int: n}, nil
	default:
		return p.identOrRef(depth)
	}
}

func (p *parser) identOrRef(depth int) (Arg, error) {
	st := p.i
	tok := p.token()
	if tok == "" {
		return Arg{}, p.errf("expected an argument")
	}
	switch {
	case p.i < len(p.s) && p.s[p.i] == '(':
		c, err := p.call(tok, depth+1)
		if err != nil {
			return Arg{}, err
		}
		return Arg{Kind: ArgCall, Call: c}, nil
	case p.i < len(p.s) && p.s[p.i] == '.':
		r, err := p.refFrom(tok, st)
		if err != nil {
			return Arg{}, err
		}
		return Arg{Kind: ArgRef, Ref: r}, nil
	}
	return Arg{Kind: ArgIdent, Str: tok}, nil
}
