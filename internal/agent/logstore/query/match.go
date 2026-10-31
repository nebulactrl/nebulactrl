package query

import (
	"math"
	"strings"
)

// Port of apps/web/src/features/logs/search/match.ts.

// Matcher reports whether a line matches a compiled query.
type Matcher func(l *Line) bool

func matchAll(*Line) bool { return true }

// globMatch matches `*` wildcards over a whole value: `err*`, `*timeout`,
// `a*b`. parts are the pieces between the stars.
func globMatch(parts []string, value string) bool {
	first := parts[0]
	if len(parts) == 1 {
		return value == first
	}
	last := parts[len(parts)-1]
	if len(value) < len(first)+len(last) {
		return false
	}
	if !strings.HasPrefix(value, first) || !strings.HasSuffix(value, last) {
		return false
	}
	at := len(first)
	end := len(value) - len(last)
	for _, part := range parts[1 : len(parts)-1] {
		found := strings.Index(value[at:], part)
		if found < 0 {
			return false
		}
		found += at
		if found+len(part) > end {
			return false
		}
		at = found + len(part)
	}
	return true
}

// containsInOrder reports whether the pieces appear in order somewhere in the text.
func containsInOrder(parts []string, text string) bool {
	at := 0
	for _, part := range parts {
		if part == "" {
			continue
		}
		found := strings.Index(text[at:], part)
		if found < 0 {
			return false
		}
		at += found + len(part)
	}
	return true
}

// scalarText is the lower-case text of a string, number or boolean value.
func scalarText(v any) (string, bool) {
	switch x := v.(type) {
	case string:
		return jsLower(x), true
	case float64:
		return jsNumString(x), true
	case bool:
		if x {
			return "true", true
		}
		return "false", true
	}
	return "", false
}

func numberOf(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, !math.IsNaN(x)
	case string:
		if jsTrim(x) != "" {
			return jsToNumber(x)
		}
	}
	return 0, false
}

func valueTest(spec valueSpec, facet string) func(any) bool {
	switch spec.kind {
	case specEq:
		parts := make([]string, len(spec.parts))
		for i, p := range spec.parts {
			parts[i] = jsLower(p)
		}
		// `level:warning` and `level:WARN` both mean warn.
		if facet == "level" && len(parts) == 1 {
			if level := levelFromName(parts[0]); level != "" {
				parts = []string{level}
			}
		}
		return func(v any) bool {
			text, ok := scalarText(v)
			return ok && globMatch(parts, text)
		}
	case specCmp:
		op, n := spec.op, spec.n
		return func(v any) bool {
			x, ok := numberOf(v)
			if !ok {
				return false
			}
			switch op {
			case ">":
				return x > n
			case ">=":
				return x >= n
			case "<":
				return x < n
			}
			return x <= n
		}
	}
	s := spec
	return func(v any) bool {
		x, ok := numberOf(v)
		if !ok {
			return false
		}
		if s.hasLo && ((s.loInc && x < s.lo) || (!s.loInc && x <= s.lo)) {
			return false
		}
		if s.hasHi && ((s.hiInc && x > s.hi) || (!s.hiInc && x >= s.hi)) {
			return false
		}
		return true
	}
}

// anyValue: a field matches when its value does; an array attribute matches
// when any element does.
func anyValue(v any, test func(any) bool) bool {
	if arr, ok := v.([]any); ok {
		for _, e := range arr {
			if test(e) {
				return true
			}
		}
		return false
	}
	if v == nil {
		return false
	}
	return test(v)
}

func compileNode(n *node) Matcher {
	if n == nil {
		return matchAll
	}
	switch n.kind {
	case nodeText:
		parts := make([]string, len(n.parts))
		for i, p := range n.parts {
			parts[i] = jsLower(p)
		}
		if len(parts) == 1 {
			needle := parts[0]
			return func(l *Line) bool { return strings.Contains(l.lowerText(), needle) }
		}
		return func(l *Line) bool { return containsInOrder(parts, l.lowerText()) }
	case nodeField:
		facet := n.key
		if n.attr {
			facet = ""
		}
		test := valueTest(n.spec, facet)
		if n.attr {
			path := n.key
			return func(l *Line) bool { return anyValue(getAttr(l.fields().attrs, path), test) }
		}
		key := n.key
		return func(l *Line) bool {
			v, ok := l.facetValue(key)
			return ok && test(v)
		}
	case nodeAnd:
		children := compileAll(n.children)
		return func(l *Line) bool {
			for _, c := range children {
				if !c(l) {
					return false
				}
			}
			return true
		}
	case nodeOr:
		children := compileAll(n.children)
		return func(l *Line) bool {
			for _, c := range children {
				if c(l) {
					return true
				}
			}
			return false
		}
	}
	child := compileNode(n.child)
	return func(l *Line) bool { return !child(l) }
}

func compileAll(nodes []*node) []Matcher {
	out := make([]Matcher, len(nodes))
	for i, n := range nodes {
		out[i] = compileNode(n)
	}
	return out
}
