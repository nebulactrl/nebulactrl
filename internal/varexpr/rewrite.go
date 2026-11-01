package varexpr

import (
	"strconv"
	"strings"
)

// RewriteRefSlug rewrites every reference `${{from.KEY}}` of template to
// `${{to.KEY}}`, including references nested in transforms such as
// urlencode(from.PASSWORD). It works on the parsed template, so text, escaped
// `$${{` runs and references to other namespaces are preserved byte for byte;
// only the expressions that change are re-rendered (without inner spaces). It
// reports whether anything changed. A template that does not parse is returned
// unchanged with false.
func RewriteRefSlug(template, from, to string) (string, bool) {
	if from == "" || to == "" || from == to || !strings.Contains(template, from) {
		return template, false
	}
	t, err := Parse(template)
	if err != nil {
		return template, false
	}
	var b strings.Builder
	changed := false
	for _, p := range t.Parts {
		if p.Expr == nil || !rewriteExpr(p.Expr, from, to) {
			b.WriteString(p.Raw)
			continue
		}
		changed = true
		b.WriteString("${{")
		b.WriteString(renderExpr(p.Expr))
		b.WriteString("}}")
	}
	if !changed {
		return template, false
	}
	return b.String(), true
}

func rewriteExpr(e *Expr, from, to string) bool {
	switch {
	case e.Ref != nil:
		if e.Ref.NS == from {
			e.Ref.NS = to
			return true
		}
	case e.Call != nil:
		return rewriteCall(e.Call, from, to)
	}
	return false
}

func rewriteCall(c *Call, from, to string) bool {
	changed := false
	for i := range c.Args {
		a := &c.Args[i]
		switch a.Kind {
		case ArgRef:
			if a.Ref.NS == from {
				a.Ref.NS = to
				changed = true
			}
		case ArgCall:
			if rewriteCall(a.Call, from, to) {
				changed = true
			}
		}
	}
	return changed
}

func renderExpr(e *Expr) string {
	switch {
	case e.Ref != nil:
		return e.Ref.String()
	case e.Ext != nil:
		return e.Ext.String()
	case e.Call != nil:
		return renderCall(e.Call)
	}
	return ""
}

func renderCall(c *Call) string {
	var b strings.Builder
	b.WriteString(c.Fn)
	b.WriteByte('(')
	for i, a := range c.Args {
		if i > 0 {
			b.WriteString(", ")
		}
		switch a.Kind {
		case ArgInt:
			b.WriteString(strconv.FormatInt(a.Int, 10))
		case ArgString:
			b.WriteByte('"')
			b.WriteString(strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(a.Str))
			b.WriteByte('"')
		case ArgIdent:
			b.WriteString(a.Str)
		case ArgRef:
			b.WriteString(a.Ref.String())
		case ArgCall:
			b.WriteString(renderCall(a.Call))
		case ArgExt:
			b.WriteString(a.Ext.String())
		}
	}
	b.WriteByte(')')
	return b.String()
}
