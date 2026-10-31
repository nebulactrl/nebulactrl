package projectconfig

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	toml "github.com/pelletier/go-toml/v2"
)

// decoder strictly maps a generic TOML tree (map[string]any, []any, int64,
// float64, string, bool, time values) onto the typed model. It reports
// unknown keys and wrong types as diagnostics and never coerces.
type decoder struct {
	s *sink
	// loc maps a decode path to the file key path used in diagnostics (and to
	// look up positions). For the document it is the identity; for merged
	// service trees it maps to the line that supplied the winning value.
	loc func(path []string) string
	// logical is prefixed to the decode path to form the table path handed to
	// Suggest ("services", slug for a service).
	logical []string
	idx     *Index
	// errs counts the errors this decoder reported.
	errs int
}

type structInfo struct {
	byKey map[string]int
	keys  []string
}

var structCache sync.Map // reflect.Type -> *structInfo

func infoOf(t reflect.Type) *structInfo {
	if v, ok := structCache.Load(t); ok {
		return v.(*structInfo)
	}
	si := &structInfo{byKey: map[string]int{}}
	for i := range t.NumField() {
		f := t.Field(i)
		key, ok := f.Tag.Lookup("toml")
		if !ok || key == "" || key == "-" || !f.IsExported() {
			continue
		}
		si.byKey[key] = i
		si.keys = append(si.keys, key)
	}
	structCache.Store(t, si)
	return si
}

var variableType = reflect.TypeOf(Variable{})

func (d *decoder) fail(code string, p []string, msg string) *Diagnostic {
	d.errs++
	return d.s.add(SeverityError, code, d.loc(p), msg)
}

func kindName(n any) string {
	switch n.(type) {
	case string:
		return "a string"
	case int64:
		return "an integer"
	case float64:
		return "a float"
	case bool:
		return "a boolean"
	case []any:
		return "an array"
	case map[string]any:
		return "a table"
	case time.Time, toml.LocalDate, toml.LocalTime, toml.LocalDateTime:
		return "a datetime"
	case nil:
		return "nothing"
	}
	return "an unsupported value"
}

func (d *decoder) wrongType(p []string, want string, got any) *Diagnostic {
	if isDatetime(got) {
		return d.fail(CodeWrongType, p, "datetime values are not supported; write a string").
			withHint("nebula.toml has no datetime values")
	}
	return d.fail(CodeWrongType, p, fmt.Sprintf("expected %s, found %s", want, kindName(got)))
}

func isDatetime(n any) bool {
	switch n.(type) {
	case time.Time, toml.LocalDate, toml.LocalTime, toml.LocalDateTime:
		return true
	}
	return false
}

// failAtKey is fail for a problem with the key itself, not its value: the
// diagnostic is positioned at the key whatever the code's default is.
func (d *decoder) failAtKey(code string, p []string, msg string) *Diagnostic {
	g := d.fail(code, p, msg)
	if d.s.pos != nil && g.Path != "" {
		*g = g.WithPos(d.s.pos(g.Path, false))
	}
	return g
}

func (g *Diagnostic) withHint(h string) *Diagnostic {
	g.Hint = h
	return g
}

// into decodes node into v (settable). p is the decode path of node.
func (d *decoder) into(v reflect.Value, node any, p []string) {
	t := v.Type()
	if t == variableType {
		if vr, ok := d.variable(node, p); ok {
			v.Set(reflect.ValueOf(vr))
		}
		return
	}
	switch t.Kind() {
	case reflect.Ptr:
		elem := reflect.New(t.Elem())
		before := d.errs
		d.into(elem.Elem(), node, p)
		if d.errs == before || t.Elem().Kind() == reflect.Struct {
			v.Set(elem)
		}
	case reflect.Struct:
		m, ok := node.(map[string]any)
		if !ok {
			d.wrongType(p, "a table", node)
			return
		}
		d.structInto(v, m, p)
	case reflect.String:
		s, ok := node.(string)
		if !ok {
			d.wrongType(p, "a string", node)
			return
		}
		v.SetString(s)
	case reflect.Bool:
		b, ok := node.(bool)
		if !ok {
			d.wrongType(p, "a boolean", node)
			return
		}
		v.SetBool(b)
	case reflect.Int, reflect.Int64:
		n, ok := node.(int64)
		if !ok {
			g := d.wrongType(p, "an integer", node)
			if f, isF := node.(float64); isF && f == float64(int64(f)) {
				g.Hint = fmt.Sprintf("write %d", int64(f))
			}
			return
		}
		v.SetInt(n)
	case reflect.Float64:
		switch n := node.(type) {
		case float64:
			v.SetFloat(n)
		case int64:
			v.SetFloat(float64(n))
		default:
			d.wrongType(p, "a number", node)
		}
	case reflect.Slice:
		arr, ok := node.([]any)
		if !ok {
			d.wrongType(p, "an array", node)
			return
		}
		if len(arr) > maxDocumentCollection {
			d.fail(CodeLimitExceeded, p, fmt.Sprintf("more than %d array elements", maxDocumentCollection))
			return
		}
		out := reflect.MakeSlice(t, 0, len(arr))
		ok = true
		for i, e := range arr {
			ep := append(append([]string(nil), p...), fmt.Sprintf("[%d]", i))
			elem := reflect.New(t.Elem()).Elem()
			before := d.errs
			d.into(elem, e, ep)
			if d.errs != before {
				ok = false
				continue
			}
			out = reflect.Append(out, elem)
		}
		if ok {
			v.Set(out)
		}
	case reflect.Map:
		m, ok := node.(map[string]any)
		if !ok {
			d.wrongType(p, "a table", node)
			return
		}
		if len(m) > maxDocumentCollection {
			d.fail(CodeLimitExceeded, p, fmt.Sprintf("more than %d keys in one table", maxDocumentCollection))
			return
		}
		out := reflect.MakeMapWithSize(t, len(m))
		for _, k := range d.sortedKeys(m, p) {
			kp := append(append([]string(nil), p...), k)
			elem := reflect.New(t.Elem()).Elem()
			before := d.errs
			d.into(elem, m[k], kp)
			if d.errs != before {
				continue
			}
			out.SetMapIndex(reflect.ValueOf(k), elem)
		}
		v.Set(out)
	default:
		d.fail(CodeWrongType, p, "unsupported target type "+t.String())
	}
}

// sortedKeys orders the keys of m by position in the document, then name.
func (d *decoder) sortedKeys(m map[string]any, p []string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	if d.idx == nil {
		sort.Strings(keys)
		return keys
	}
	ord := make(map[string]int, len(keys))
	for _, k := range keys {
		ord[k] = d.idx.order(d.loc(append(append([]string(nil), p...), k)))
	}
	sort.Slice(keys, func(i, j int) bool {
		if ord[keys[i]] != ord[keys[j]] {
			return ord[keys[i]] < ord[keys[j]]
		}
		return keys[i] < keys[j]
	})
	return keys
}

func (d *decoder) structInto(v reflect.Value, m map[string]any, p []string) {
	si := infoOf(v.Type())
	for _, k := range d.sortedKeys(m, p) {
		kp := append(append([]string(nil), p...), k)
		fi, ok := si.byKey[k]
		if !ok {
			d.unknownKey(p, k, kp)
			continue
		}
		d.into(v.Field(fi), m[k], kp)
	}
}

func (d *decoder) unknownKey(table []string, key string, kp []string) {
	logical := append(append([]string(nil), d.logical...), table...)
	if credentialKey(key) {
		g := d.fail(CodeCredentialKey, kp, fmt.Sprintf("key %q is a credential: %s", key, credentialMessage()))
		g.Hint = "use secrets.keys to declare the name and set the value in the dashboard or CLI"
		return
	}
	if key == "manage" && len(table) >= 3 && table[len(table)-1] == "cloudflare" && table[len(table)-3] == "domains" {
		g := d.fail(CodeUnknownKey, kp, `key "manage" was removed: NebulaCtrl always manages the DNS record of a hostname whose zone is in the connected Cloudflare account`)
		g.Hint = "delete the line"
		return
	}
	g := d.fail(CodeUnknownKey, kp, fmt.Sprintf("unknown key %q", key))
	if sug := Suggest(logical, key); len(sug) > 0 {
		g.Suggestions = sug
		g.Hint = "write `" + sug[0] + "`"
	}
}

// variable decodes a variable entry: a string, or a table with metadata.
func (d *decoder) variable(node any, p []string) (Variable, bool) {
	switch n := node.(type) {
	case string:
		return Variable{Value: n}, true
	case map[string]any:
		var out Variable
		ok := true
		for _, k := range d.sortedKeys(n, p) {
			kp := append(append([]string(nil), p...), k)
			switch k {
			case "value":
				s, isS := n[k].(string)
				if !isS {
					d.wrongType(kp, "a string", n[k]).Hint = `variable values are always strings: write "8080"`
					ok = false
					continue
				}
				out.Value = s
			case "scope", "comment":
				s, isS := n[k].(string)
				if !isS {
					d.wrongType(kp, "a string", n[k])
					ok = false
					continue
				}
				if k == "scope" {
					out.Scope = s
				} else {
					out.Comment = s
				}
			case "rotate_every_days":
				i, isI := n[k].(int64)
				if !isI {
					d.wrongType(kp, "an integer", n[k])
					ok = false
					continue
				}
				x := int(i)
				out.RotateEveryDays = &x
			case "public":
				b, isB := n[k].(bool)
				if !isB {
					d.wrongType(kp, "a boolean", n[k])
					ok = false
					continue
				}
				out.Public = b
			case "secret":
				d.failAtKey(CodeInvalidValue, kp, "a variable table has no `secret` key: a variable is secret when its value is a generator or a reference, or when its name is listed in secrets.keys").
					withHint("declare the name in secrets.keys; write generators as ${{secret(32)}}")
				ok = false
			default:
				d.unknownKey(p, k, kp)
				ok = false
			}
		}
		if _, has := n["value"]; !has {
			d.fail(CodeMissingRequired, p, "a variable table needs `value`").Hint = `write KEY = { value = "…" }`
			ok = false
		}
		return out, ok
	default:
		g := d.wrongType(p, "a string or a table", node)
		switch node.(type) {
		case int64, float64, bool:
			g.Hint = `variable values are always strings: write "` + fmt.Sprint(node) + `"`
		}
		return Variable{}, false
	}
}

// treeDepth returns the table nesting depth of a tree (arrays of tables count
// one level per table).
func treeDepth(n any, depth int) int {
	best := depth
	switch t := n.(type) {
	case map[string]any:
		for _, c := range t {
			if x := treeDepth(c, depth+1); x > best {
				best = x
			}
		}
	case []any:
		for _, c := range t {
			if x := treeDepth(c, depth); x > best {
				best = x
			}
		}
	}
	return best
}

// walkLimits reports strings above MaxStringBytes and over-deep tables. It
// returns false once depth is exceeded (the walk stops there).
func (d *decoder) walkLimits(n any, p []string, depth int) bool {
	switch t := n.(type) {
	case string:
		if len(t) > MaxStringBytes {
			d.fail(CodeLimitExceeded, p, fmt.Sprintf("value is %d bytes; at most %d", len(t), MaxStringBytes))
		}
	case map[string]any:
		if depth > MaxTableDepth {
			d.fail(CodeLimitExceeded, p, fmt.Sprintf("tables are nested more than %d levels deep", MaxTableDepth))
			return false
		}
		for _, k := range d.sortedKeys(t, p) {
			if len(k) > 4096 {
				d.fail(CodeLimitExceeded, append(append([]string(nil), p...), k[:32]), "key is longer than 4096 bytes")
				continue
			}
			if !d.walkLimits(t[k], append(append([]string(nil), p...), k), depth+1) {
				return false
			}
		}
	case []any:
		if depth > MaxTableDepth {
			d.fail(CodeLimitExceeded, p, fmt.Sprintf("arrays and tables are nested more than %d levels deep", MaxTableDepth))
			return false
		}
		for i, c := range t {
			if !d.walkLimits(c, append(append([]string(nil), p...), fmt.Sprintf("[%d]", i)), depth+1) {
				return false
			}
		}
	}
	return true
}

// keyList renders a sorted key list for messages.
func keyList(keys []string) string {
	k := append([]string(nil), keys...)
	sort.Strings(k)
	return strings.Join(k, ", ")
}
