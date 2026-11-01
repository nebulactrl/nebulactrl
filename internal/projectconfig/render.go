package projectconfig

import (
	"bytes"
	"fmt"
	"math"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// RenderOptions tunes Render and Factor.
type RenderOptions struct {
	// Origin is the schema origin for the `#:schema <origin>/schema/nebula.toml.json`
	// header line. Empty omits the header line.
	Origin string
	// Comments maps a file key path (PathString form, e.g. `services.web.port`
	// or `services.web.domains."shop.acme.com"`) to comment lines written above
	// that key or table header, without the leading `# `.
	Comments map[string][]string
	// NotRepresentable maps a service slug to the names of live settings the
	// format cannot express. They are written as one trailing comment line,
	// `# not representable in schema 1: a, b`, after the last table of that service.
	NotRepresentable map[string][]string
}

// Render writes the deterministic nebula.toml text of one resolved view: the
// header, schema, project and server keys, [variables], [secrets], then the
// services sorted by slug. Keys follow the field order of the model types,
// every table lists its scalar, array and inline values before its
// sub-tables, and map keys are sorted. The output uses \n line endings, has
// no trailing whitespace and ends with exactly one newline.
//
// The environment layer of r (EnvShared, EnvSharedSecrets) is written as
// [environments.<r.Environment>] blocks when r.Environment is set, so that
// resolving the output for that environment gives r back; when r.Environment
// is empty the layer is folded over the project layer instead.
func Render(r *Resolved, o RenderOptions) []byte {
	return emitDocument(toTree(renderFile(r)), o)
}

// renderFile builds the typed file of one resolved view. It shares the Def
// pointers of r and never mutates them.
func renderFile(r *Resolved) *File {
	if r == nil {
		r = &Resolved{}
	}
	one := SchemaVersion
	f := &File{Schema: &one}
	if r.Project != "" {
		f.Project = &r.Project
	}
	if r.Server != "" {
		f.Server = &r.Server
	}
	vars, keys, scope := r.Shared, r.SharedSecrets, r.SharedSecretScope
	if r.Environment == "" {
		vars, keys, scope = foldLayers(r)
	} else if len(r.EnvShared) > 0 || len(r.EnvSharedSecrets) > 0 {
		env := &Environment{Secrets: secretsDecl(r.EnvSharedSecrets, r.EnvSharedSecretScope)}
		if len(r.EnvShared) > 0 {
			env.Variables = r.EnvShared
		}
		f.EnvironmentBlocks = map[string]*Environment{r.Environment: env}
	}
	if len(vars) > 0 {
		f.Variables = vars
	}
	f.Secrets = secretsDecl(keys, scope)
	if len(r.Services) > 0 {
		f.Services = make(map[string]*Service, len(r.Services))
		for slug, rs := range r.Services {
			f.Services[slug] = serviceDefOf(rs)
		}
	}
	return f
}

// serviceDefOf returns the definition of rs, never nil.
func serviceDefOf(rs *ResolvedService) *Service {
	if rs == nil || rs.Def == nil {
		return &Service{}
	}
	return rs.Def
}

// foldLayers returns the effective shared variables and secrets of a view
// that has no environment name: the environment layer over the project layer.
func foldLayers(r *Resolved) (map[string]Variable, []string, string) {
	if len(r.EnvShared) == 0 && len(r.EnvSharedSecrets) == 0 {
		return r.Shared, r.SharedSecrets, r.SharedSecretScope
	}
	vars := make(map[string]Variable, len(r.Shared)+len(r.EnvShared))
	for k, v := range r.Shared {
		vars[k] = v
	}
	for k, v := range r.EnvShared {
		vars[k] = v
	}
	keys := append([]string(nil), r.SharedSecrets...)
	seen := make(map[string]bool, len(keys))
	for _, k := range keys {
		seen[k] = true
	}
	for _, k := range r.EnvSharedSecrets {
		if !seen[k] {
			keys = append(keys, k)
		}
	}
	scope := r.SharedSecretScope
	if len(r.SharedSecrets) == 0 {
		scope = r.EnvSharedSecretScope
	}
	return vars, keys, scope
}

// secretsDecl returns the Secrets table of a layer, or nil without names.
func secretsDecl(keys []string, scope string) *Secrets {
	if len(keys) == 0 {
		return nil
	}
	s := &Secrets{Keys: keys}
	if scope != "" && scope != "runtime" {
		s.Scope = &scope
	}
	return s
}

// ---------------------------------------------------------------------------
// Tree: the generic, ordered document model shared by Render and Factor.

// emitTable is an ordered TOML table. Keys of model structs keep the field
// order of the struct; keys of maps are sorted. inline tables are written as
// `{ k = v }` values instead of [headers].
type emitTable struct {
	keys   []string
	vals   map[string]any
	inline bool
}

func newEmitTable() *emitTable { return &emitTable{vals: map[string]any{}} }

// set stores v under k, appending k to the key order when it is new.
func (t *emitTable) set(k string, v any) {
	if _, ok := t.vals[k]; !ok {
		t.keys = append(t.keys, k)
	}
	t.vals[k] = v
}

// insertAt stores v under the new key k at position i of the key order.
func (t *emitTable) insertAt(i int, k string, v any) {
	t.keys = append(t.keys, "")
	copy(t.keys[i+1:], t.keys[i:])
	t.keys[i] = k
	t.vals[k] = v
}

// remove deletes k.
func (t *emitTable) remove(k string) {
	if _, ok := t.vals[k]; !ok {
		return
	}
	delete(t.vals, k)
	for i, x := range t.keys {
		if x == k {
			t.keys = append(t.keys[:i], t.keys[i+1:]...)
			break
		}
	}
}

var emitVariableType = reflect.TypeOf(Variable{})

// toTree converts a model value (a struct or a pointer to one: File, Service,
// Secrets, ...) into its generic tree. Values of the tree are string, int64,
// float64, bool, []any (elements: scalars or inline tables) and *emitTable.
// Absent keys (nil pointers, slices and maps) are omitted; a present empty
// slice or map stays present.
func toTree(v any) *emitTable {
	n, ok := encodeNode(reflect.ValueOf(v))
	if !ok {
		return newEmitTable()
	}
	t, ok := n.(*emitTable)
	if !ok {
		return newEmitTable()
	}
	return t
}

// emitKeyOf returns the toml key name of a struct field, "" when it has none.
func emitKeyOf(f reflect.StructField) string {
	if !f.IsExported() {
		return ""
	}
	name, _, _ := strings.Cut(f.Tag.Get("toml"), ",")
	if name == "-" {
		return ""
	}
	return name
}

func encodeNode(rv reflect.Value) (any, bool) {
	switch rv.Kind() {
	case reflect.Pointer, reflect.Interface:
		if rv.IsNil() {
			return nil, false
		}
		return encodeNode(rv.Elem())
	case reflect.Struct:
		if rv.Type() == emitVariableType {
			return encodeVariable(rv.Interface().(Variable)), true
		}
		t := newEmitTable()
		rt := rv.Type()
		for i := range rt.NumField() {
			f := rt.Field(i)
			key := emitKeyOf(f)
			if key == "" {
				continue
			}
			fv := rv.Field(i)
			switch f.Type.Kind() {
			case reflect.Pointer, reflect.Slice, reflect.Map, reflect.Interface:
			default:
				if fv.IsZero() {
					continue
				}
			}
			if n, ok := encodeNode(fv); ok {
				t.set(key, n)
			}
		}
		return t, true
	case reflect.Slice:
		if rv.IsNil() {
			return nil, false
		}
		out := make([]any, 0, rv.Len())
		for i := range rv.Len() {
			n, ok := encodeNode(rv.Index(i))
			if !ok {
				n = newEmitTable()
			}
			if t, isT := n.(*emitTable); isT {
				t.inline = true
			}
			out = append(out, n)
		}
		return out, true
	case reflect.Map:
		if rv.IsNil() {
			return nil, false
		}
		keys := rv.MapKeys()
		sort.Slice(keys, func(i, j int) bool { return keys[i].String() < keys[j].String() })
		t := newEmitTable()
		for _, k := range keys {
			n, ok := encodeNode(rv.MapIndex(k))
			if !ok {
				n = newEmitTable()
			}
			t.set(k.String(), n)
		}
		return t, true
	case reflect.String:
		return rv.String(), true
	case reflect.Bool:
		return rv.Bool(), true
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return rv.Int(), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return int64(rv.Uint()), true
	case reflect.Float32, reflect.Float64:
		return rv.Float(), true
	}
	return nil, false
}

// encodeVariable writes the string form for a plain runtime variable and the
// inline table form `{ value, scope, comment, rotate_every_days, public }`
// when it carries metadata.
func encodeVariable(v Variable) any {
	if (v.Scope == "" || v.Scope == "runtime") && v.Comment == "" && v.RotateEveryDays == nil && !v.Public {
		return v.Value
	}
	t := newEmitTable()
	t.inline = true
	t.set("value", v.Value)
	if v.Scope != "" {
		t.set("scope", v.Scope)
	}
	if v.Comment != "" {
		t.set("comment", v.Comment)
	}
	if v.RotateEveryDays != nil {
		t.set("rotate_every_days", int64(*v.RotateEveryDays))
	}
	if v.Public {
		t.set("public", true)
	}
	return t
}

// ---------------------------------------------------------------------------
// Writer.

type emitWriter struct {
	buf bytes.Buffer
	o   RenderOptions
}

// emitDocument writes root as a complete document.
func emitDocument(root *emitTable, o RenderOptions) []byte {
	w := &emitWriter{o: o}
	if o.Origin != "" {
		w.buf.WriteString("#:schema " + strings.TrimRight(o.Origin, "/") + "/schema/nebula.toml.json\n")
	}
	w.body(nil, root)
	text := bytes.TrimRight(w.buf.Bytes(), "\n")
	out := make([]byte, 0, len(text)+1)
	out = append(out, text...)
	return append(out, '\n')
}

func childPath(p []string, k string) []string {
	out := make([]string, len(p)+1)
	copy(out, p)
	out[len(p)] = k
	return out
}

// blank writes one empty line unless the output is empty or already ends in one.
func (w *emitWriter) blank() {
	b := w.buf.Bytes()
	n := len(b)
	if n == 0 || (n >= 2 && b[n-1] == '\n' && b[n-2] == '\n') {
		return
	}
	w.buf.WriteByte('\n')
}

// commentLines returns the formatted comment lines configured for path.
func (w *emitWriter) commentLines(path []string) []string {
	src := w.o.Comments[PathString(path...)]
	if len(src) == 0 {
		return nil
	}
	var out []string
	for _, l := range src {
		for _, part := range strings.Split(strings.ReplaceAll(l, "\r\n", "\n"), "\n") {
			part = strings.Map(func(r rune) rune {
				if r != '\t' && (r < 0x20 || r == 0x7f) {
					return -1
				}
				return r
			}, part)
			part = strings.TrimRight(part, " \t")
			if part == "" {
				out = append(out, "#")
			} else {
				out = append(out, "# "+part)
			}
		}
	}
	return out
}

func (w *emitWriter) writeLines(lines []string) {
	for _, l := range lines {
		w.buf.WriteString(l)
		w.buf.WriteByte('\n')
	}
}

// body writes the values of t (scalars, arrays and inline tables) and then
// its sub-tables.
func (w *emitWriter) body(path []string, t *emitTable) {
	for _, k := range t.keys {
		v := t.vals[k]
		if sub, ok := v.(*emitTable); ok && !sub.inline {
			continue
		}
		w.writeLines(w.commentLines(childPath(path, k)))
		w.buf.WriteString(emitKey(k) + " = " + emitValue(v, true) + "\n")
	}
	for _, k := range t.keys {
		if sub, ok := t.vals[k].(*emitTable); ok && !sub.inline {
			w.table(childPath(path, k), sub)
		}
	}
}

// table writes the [header] of t (omitted when t only has sub-tables and no
// comment, since TOML creates it implicitly) and its content.
func (w *emitWriter) table(path []string, t *emitTable) {
	hasValues := false
	for _, k := range t.keys {
		if sub, ok := t.vals[k].(*emitTable); !ok || sub.inline {
			hasValues = true
			break
		}
	}
	lines := w.commentLines(path)
	if hasValues || len(t.keys) == 0 || len(lines) > 0 {
		w.blank()
		w.writeLines(lines)
		w.buf.WriteString("[" + emitPath(path) + "]\n")
	}
	w.body(path, t)
	if len(path) == 2 && path[0] == "services" {
		w.notRepresentable(path[1])
	}
}

func (w *emitWriter) notRepresentable(slug string) {
	names := w.o.NotRepresentable[slug]
	if len(names) == 0 {
		return
	}
	w.blank()
	w.buf.WriteString("# not representable in schema 1: " + strings.Join(names, ", ") + "\n")
}

func emitPath(path []string) string {
	parts := make([]string, len(path))
	for i, s := range path {
		parts[i] = emitKey(s)
	}
	return strings.Join(parts, ".")
}

// emitKey writes a key bare when it matches ^[A-Za-z0-9_-]+$, else as a
// basic string.
func emitKey(k string) string {
	if bareKey(k) {
		return k
	}
	return emitString(k, false)
}

// emitValue writes a value. top is true for the value of a key directly in a
// table body, where strings with newlines use the multi-line form; inside
// arrays and inline tables every string stays on one line.
func emitValue(v any, top bool) string {
	switch x := v.(type) {
	case string:
		return emitString(x, top)
	case int64:
		return strconv.FormatInt(x, 10)
	case float64:
		return emitFloat(x)
	case bool:
		return strconv.FormatBool(x)
	case []any:
		parts := make([]string, len(x))
		for i, e := range x {
			parts[i] = emitValue(e, false)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case *emitTable:
		if len(x.keys) == 0 {
			return "{}"
		}
		parts := make([]string, len(x.keys))
		for i, k := range x.keys {
			parts[i] = emitKey(k) + " = " + emitValue(x.vals[k], false)
		}
		return "{ " + strings.Join(parts, ", ") + " }"
	}
	panic(fmt.Sprintf("projectconfig: cannot render %T", v))
}

// emitFloat writes a float so that it parses back as a float: integral values
// keep a `.0`.
func emitFloat(f float64) string {
	switch {
	case math.IsNaN(f):
		return "nan"
	case math.IsInf(f, 1):
		return "inf"
	case math.IsInf(f, -1):
		return "-inf"
	}
	s := strconv.FormatFloat(f, 'g', -1, 64)
	if !strings.ContainsAny(s, ".eE") {
		s += ".0"
	}
	return s
}

// emitString writes s as a TOML basic string. With multiline set, a string
// that contains a newline becomes a multi-line basic string (`"""`, first
// newline trimmed by the parser). Everything else is a single-line basic
// string; every control character is escaped, so the text round-trips
// exactly.
func emitString(s string, multiline bool) string {
	var b strings.Builder
	if multiline && strings.Contains(s, "\n") {
		emitMultiline(&b, s)
		return b.String()
	}
	b.WriteByte('"')
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		i += size
		if r == utf8.RuneError && size == 1 {
			b.WriteString(`\uFFFD`)
			continue
		}
		emitEscape(&b, r)
	}
	b.WriteByte('"')
	return b.String()
}

// emitMultiline writes the `"""` form: backslashes and control characters
// are escaped, runs of three quotes and a quote at the end are broken up with
// `\"`, and a space before a newline is written as \u0020 so that no line
// ends in whitespace.
func emitMultiline(b *strings.Builder, s string) {
	b.WriteString("\"\"\"\n")
	quotes := 0
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		i += size
		switch {
		case r == utf8.RuneError && size == 1:
			b.WriteString(`\uFFFD`)
			quotes = 0
		case r == '"':
			if quotes >= 2 || i == len(s) {
				b.WriteString(`\"`)
				quotes = 0
			} else {
				b.WriteByte('"')
				quotes++
			}
		case r == '\n':
			b.WriteByte('\n')
			quotes = 0
		case r == ' ' && i < len(s) && s[i] == '\n':
			b.WriteString(`\u0020`)
			quotes = 0
		default:
			emitEscape(b, r)
			quotes = 0
		}
	}
	b.WriteString(`"""`)
}

func emitEscape(b *strings.Builder, r rune) {
	switch r {
	case '\\':
		b.WriteString(`\\`)
	case '"':
		b.WriteString(`\"`)
	case '\n':
		b.WriteString(`\n`)
	case '\r':
		b.WriteString(`\r`)
	case '\t':
		b.WriteString(`\t`)
	case '\b':
		b.WriteString(`\b`)
	case '\f':
		b.WriteString(`\f`)
	default:
		if r < 0x20 || r == 0x7f {
			fmt.Fprintf(b, `\u%04X`, r)
		} else {
			b.WriteRune(r)
		}
	}
}
