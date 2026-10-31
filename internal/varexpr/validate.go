package varexpr

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/nebulactrl/nebulactrl/internal/secretgen"
)

// Reserved namespaces that are not service slugs. ExtScheme introduces a
// reference to an external secret manager (`vault:<integration>/<path>#<key>`);
// DopplerScheme introduces a Doppler one
// (`doppler:<integration>[/<project>/<config>]#<key>`). Only "vault" is a
// reserved slug: `doppler.KEY` stays an ordinary service reference.
const (
	NSShared      = "shared"
	NSTeam        = "team"
	ExtScheme     = "vault"
	DopplerScheme = "doppler"
)

// WeakSecretLen is the secret length below which a warning is raised: values
// shorter than services.MinRedactLen are also not masked in logs.
const WeakSecretLen = 8

// Call descriptors never contain literal text: a descriptor is built from the
// parsed function and its arguments only, so it is safe to show for secrets.

// GeneratorCall describes one generator expression of a template.
type GeneratorCall struct {
	Fn   string   `json:"fn"`
	Args []string `json:"args"`
}

// String renders `secret(32)`, `randomBytes(32, hex)` …
func (g GeneratorCall) String() string {
	return g.Fn + "(" + strings.Join(g.Args, ", ") + ")"
}

// Warning is a non-blocking concern found by Validate.
type Warning struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Validate checks function names, argument counts, types and ranges. Generators
// must be top-level expressions; transforms take a reference, a string or
// another transform. It returns warnings for acceptable but risky input.
func Validate(t *Template) ([]Warning, error) {
	var warns []Warning
	for _, p := range t.Parts {
		if p.Expr == nil || p.Expr.Call == nil {
			continue
		}
		w, err := validateCall(p.Expr.Call, p.Offset, true)
		if err != nil {
			return nil, err
		}
		warns = append(warns, w...)
	}
	return warns, nil
}

func serr(off int, format string, a ...any) error {
	return &SyntaxError{Offset: off, Message: fmt.Sprintf(format, a...)}
}

func validateCall(c *Call, off int, top bool) ([]Warning, error) {
	switch {
	case IsGenerator(c.Fn):
		if !top {
			return nil, serr(off, "%s() can only be used on its own, not inside another function", c.Fn)
		}
		return validateGenerator(c, off)
	case IsTransform(c.Fn):
		if len(c.Args) != 1 {
			return nil, serr(off, "%s() takes exactly one argument", c.Fn)
		}
		a := c.Args[0]
		switch a.Kind {
		case ArgRef, ArgIdent, ArgString, ArgExt:
		case ArgCall:
			if !IsTransform(a.Call.Fn) {
				return nil, serr(off, "%s() cannot wrap %s()", c.Fn, a.Call.Fn)
			}
			return validateCall(a.Call, off, false)
		default:
			return nil, serr(off, "%s() takes a variable reference or a string", c.Fn)
		}
		if a.Kind == ArgIdent && !IsKey(a.Str) {
			return nil, serr(off, "invalid variable name %q", a.Str)
		}
		return nil, nil
	}
	return nil, serr(off, "unknown function %q (secret, randomInt, uuid, randomBytes, urlencode, base64)", c.Fn)
}

func intArg(c *Call, i int, def int64, off int) (int64, error) {
	if i >= len(c.Args) {
		return def, nil
	}
	if c.Args[i].Kind != ArgInt {
		return 0, serr(off, "argument %d of %s() must be an integer", i+1, c.Fn)
	}
	return c.Args[i].Int, nil
}

func validateGenerator(c *Call, off int) ([]Warning, error) {
	var warns []Warning
	switch c.Fn {
	case FnSecret:
		if len(c.Args) > 2 {
			return nil, serr(off, "secret(length, alphabet) takes at most two arguments")
		}
		n, err := intArg(c, 0, 32, off)
		if err != nil {
			return nil, err
		}
		if n < 1 || n > secretgen.MaxSecretLen {
			return nil, serr(off, "secret length must be 1..%d", secretgen.MaxSecretLen)
		}
		if len(c.Args) == 2 {
			a := c.Args[1]
			switch a.Kind {
			case ArgIdent:
				if _, ok := secretgen.Preset(a.Str); !ok {
					return nil, serr(off, "unknown alphabet %q (%s) or quote a literal alphabet", a.Str, strings.Join(secretgen.PresetNames(), ", "))
				}
			case ArgString:
				if err := secretgen.ValidateAlphabet(a.Str); err != nil {
					return nil, serr(off, "%s", err.Error())
				}
			default:
				return nil, serr(off, "the alphabet of secret() is a preset name or a quoted string")
			}
		}
		if n < WeakSecretLen {
			warns = append(warns, Warning{Code: "weak_secret", Message: fmt.Sprintf("secret(%d) is shorter than %d characters; values under 6 characters are also not masked in logs", n, WeakSecretLen)})
		} else if a, ok := alphabetOf(c); ok && len(a) < 8 && n < 16 {
			warns = append(warns, Warning{Code: "weak_secret", Message: "a small alphabet with a short length gives low entropy"})
		}
	case FnRandomInt:
		if len(c.Args) > 2 {
			return nil, serr(off, "randomInt(min, max) takes at most two arguments")
		}
		lo, err := intArg(c, 0, 0, off)
		if err != nil {
			return nil, err
		}
		hi, err := intArg(c, 1, 100, off)
		if err != nil {
			return nil, err
		}
		if lo > hi {
			return nil, serr(off, "randomInt: min must not exceed max")
		}
		if hi-lo < 0 || hi-lo >= 1<<62 {
			return nil, serr(off, "randomInt: range too large")
		}
	case FnUUID:
		if len(c.Args) != 0 {
			return nil, serr(off, "uuid() takes no arguments")
		}
	case FnRandomBytes:
		if len(c.Args) > 2 {
			return nil, serr(off, "randomBytes(n, encoding) takes at most two arguments")
		}
		n, err := intArg(c, 0, 32, off)
		if err != nil {
			return nil, err
		}
		if n < 1 || n > secretgen.MaxBytes {
			return nil, serr(off, "randomBytes count must be 1..%d", secretgen.MaxBytes)
		}
		if len(c.Args) == 2 {
			a := c.Args[1]
			if a.Kind != ArgIdent && a.Kind != ArgString {
				return nil, serr(off, "the encoding of randomBytes() is hex, base64 or base64url")
			}
			switch strings.ToLower(a.Str) {
			case secretgen.EncHex, secretgen.EncBase64, secretgen.EncBase64URL:
			default:
				return nil, serr(off, "unknown encoding %q (hex, base64, base64url)", a.Str)
			}
		}
		if n < 8 {
			warns = append(warns, Warning{Code: "weak_secret", Message: fmt.Sprintf("randomBytes(%d) is shorter than 8 bytes", n)})
		}
	}
	return warns, nil
}

func alphabetOf(c *Call) (string, bool) {
	if len(c.Args) < 2 {
		a, _ := secretgen.Preset(secretgen.AlphaAlnum)
		return a, true
	}
	a := c.Args[1]
	if a.Kind == ArgIdent {
		s, ok := secretgen.Preset(a.Str)
		return s, ok
	}
	return a.Str, a.Kind == ArgString
}

// Describe renders a generator call from its arguments (never from template
// text, so literal prefixes stay private).
func Describe(c *Call) GeneratorCall {
	g := GeneratorCall{Fn: c.Fn, Args: []string{}}
	for _, a := range c.Args {
		switch a.Kind {
		case ArgInt:
			g.Args = append(g.Args, strconv.FormatInt(a.Int, 10))
		case ArgIdent:
			g.Args = append(g.Args, a.Str)
		case ArgString:
			// A literal alphabet is a configuration choice, not content; show its size only.
			g.Args = append(g.Args, fmt.Sprintf("%d-char alphabet", len(a.Str)))
		}
	}
	return g
}

// Materialized is a template whose generator calls have been run once.
type Materialized struct {
	// Stored is the template text to persist: generators replaced by their
	// output, everything else verbatim.
	Stored string
	// Secret is true when a secret-class generator (secret, randomBytes) ran.
	Secret bool
	// Calls describes the generators that ran, in source order.
	Calls []GeneratorCall
	// Warnings are the Validate warnings.
	Warnings []Warning
}

// Generated reports whether any generator ran.
func (m Materialized) Generated() bool { return len(m.Calls) > 0 }

// Materialize parses, validates and evaluates the generators of template once.
func Materialize(template string) (Materialized, error) {
	t, err := Parse(template)
	if err != nil {
		return Materialized{}, err
	}
	return MaterializeTemplate(t)
}

// MaterializeTemplate is Materialize for an already parsed template.
func MaterializeTemplate(t *Template) (Materialized, error) {
	warns, err := Validate(t)
	if err != nil {
		return Materialized{}, err
	}
	m := Materialized{Warnings: warns}
	var b strings.Builder
	for _, p := range t.Parts {
		if p.Expr == nil || p.Expr.Call == nil || !IsGenerator(p.Expr.Call.Fn) {
			b.WriteString(p.Raw)
			continue
		}
		out, err := generate(p.Expr.Call)
		if err != nil {
			return Materialized{}, serr(p.Offset, "%s", err.Error())
		}
		b.WriteString(out)
		m.Calls = append(m.Calls, Describe(p.Expr.Call))
		if p.Expr.Call.Fn == FnSecret || p.Expr.Call.Fn == FnRandomBytes {
			m.Secret = true
		}
	}
	m.Stored = b.String()
	return m, nil
}

func generate(c *Call) (string, error) {
	switch c.Fn {
	case FnSecret:
		n, _ := intArg(c, 0, 32, 0)
		alpha := secretgen.AlphaAlnum
		if len(c.Args) == 2 {
			alpha = c.Args[1].Str
		}
		return secretgen.Secret(int(n), alpha)
	case FnRandomInt:
		lo, _ := intArg(c, 0, 0, 0)
		hi, _ := intArg(c, 1, 100, 0)
		v, err := secretgen.Int(lo, hi)
		return strconv.FormatInt(v, 10), err
	case FnUUID:
		return secretgen.UUID()
	case FnRandomBytes:
		n, _ := intArg(c, 0, 32, 0)
		enc := secretgen.EncBase64
		if len(c.Args) == 2 {
			enc = c.Args[1].Str
		}
		return secretgen.Bytes(int(n), enc)
	}
	return "", fmt.Errorf("%s() is not a generator", c.Fn)
}
