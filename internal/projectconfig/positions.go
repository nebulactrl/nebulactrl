package projectconfig

import (
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/pelletier/go-toml/v2/unstable"
)

// maxIndexDepth bounds the key path depth the index records. Documents nested
// deeper are rejected by the limits pass; the cap keeps building the index
// linear for hostile input.
const maxIndexDepth = 40

// PathString renders key path segments as a TOML-style dotted path. A segment
// is written bare when it matches [A-Za-z0-9_-]+ and as a basic quoted string
// otherwise (hostnames, file paths). A segment of the form "[n]" is an array
// index and is appended to the previous segment without a dot.
func PathString(segs ...string) string {
	var b strings.Builder
	for i, s := range segs {
		if isIndexSeg(s) {
			b.WriteString(s)
			continue
		}
		if i > 0 {
			b.WriteByte('.')
		}
		if bareKey(s) {
			b.WriteString(s)
		} else {
			b.WriteString(strconv.Quote(s))
		}
	}
	return b.String()
}

func isIndexSeg(s string) bool {
	if len(s) < 3 || s[0] != '[' || s[len(s)-1] != ']' {
		return false
	}
	for _, c := range s[1 : len(s)-1] {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func bareKey(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

// join appends one segment to a path string.
func join(path string, seg string) string {
	if path == "" {
		return PathString(seg)
	}
	if isIndexSeg(seg) {
		return path + seg
	}
	return path + "." + keySeg(seg)
}

func keySeg(s string) string {
	if bareKey(s) {
		return s
	}
	return strconv.Quote(s)
}

// splitKeyPath parses a TOML key path (`domains."shop.acme.com"`, `a.b`) into
// segments. Basic ("...") and literal ('...') quoted segments are supported.
// ok is false on malformed input (empty segment, unterminated quote, stray
// characters).
func splitKeyPath(s string) (segs []string, ok bool) {
	i := 0
	for {
		for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
			i++
		}
		if i >= len(s) {
			return nil, false
		}
		var seg string
		switch s[i] {
		case '"':
			j := i + 1
			var b strings.Builder
			closed := false
			for j < len(s) {
				c := s[j]
				if c == '\\' {
					if j+1 >= len(s) {
						return nil, false
					}
					switch s[j+1] {
					case '"', '\\':
						b.WriteByte(s[j+1])
					case 'n':
						b.WriteByte('\n')
					case 't':
						b.WriteByte('\t')
					default:
						return nil, false
					}
					j += 2
					continue
				}
				if c == '"' {
					closed = true
					j++
					break
				}
				b.WriteByte(c)
				j++
			}
			if !closed {
				return nil, false
			}
			seg, i = b.String(), j
		case '\'':
			j := strings.IndexByte(s[i+1:], '\'')
			if j < 0 {
				return nil, false
			}
			seg, i = s[i+1:i+1+j], i+j+2
		default:
			j := i
			for j < len(s) && s[j] != '.' && s[j] != ' ' && s[j] != '\t' {
				c := s[j]
				if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
					return nil, false
				}
				j++
			}
			seg, i = s[i:j], j
		}
		if seg == "" {
			return nil, false
		}
		segs = append(segs, seg)
		for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
			i++
		}
		if i >= len(s) {
			return segs, true
		}
		if s[i] != '.' {
			return nil, false
		}
		i++
	}
}

// Index maps key paths of a document to their positions. It is built with the
// TOML library's unstable parser (keys, structure) plus a scan of the text for
// value extents, so positions are exact.
type Index struct {
	lineStarts []int
	text       []byte
	keys       map[string]Pos // the key (or table header segment) of a path
	vals       map[string]Pos // the value of a path
	seq        map[string]int // order of first appearance
	dup        Pos            // position of the first repeated key or table header

	// anchor is the last byte offset converted to a column, with its line and
	// its 1-based rune column. Positions are asked for in nearly increasing
	// order, so a column is counted from the anchor instead of from the line
	// start: a document of one very long line stays linear.
	anchorLine, anchorOff, anchorCol int
}

// BuildIndex builds the position index of text. It never fails: a document
// the parser rejects yields the entries read before the error (Parse reports
// the syntax error itself).
func BuildIndex(text []byte) *Index {
	ix := &Index{
		text: text,
		keys: map[string]Pos{},
		vals: map[string]Pos{},
		seq:  map[string]int{},
	}
	ix.lineStarts = append(ix.lineStarts, 0)
	for i, c := range text {
		if c == '\n' {
			ix.lineStarts = append(ix.lineStarts, i+1)
		}
	}
	var p unstable.Parser
	p.Reset(text)
	arrays := map[string]int{}
	explicit := map[string]bool{}
	var cur []string
	for p.NextExpression() {
		n := p.Expression()
		switch n.Kind {
		case unstable.Table, unstable.ArrayTable:
			var segs []string
			var last Pos
			it := n.Key()
			for it.Next() {
				if len(segs) >= maxIndexDepth {
					break
				}
				kn := it.Node()
				segs = append(segs, string(kn.Data))
				last = ix.rangePos(kn.Raw)
				ix.setKey(segs, last)
			}
			if n.Kind == unstable.ArrayTable {
				base := PathString(segs...)
				idx := arrays[base]
				arrays[base] = idx + 1
				segs = append(segs, "["+strconv.Itoa(idx)+"]")
				ix.setKey(segs, last)
			} else {
				path := PathString(segs...)
				if explicit[path] && ix.dup.IsZero() {
					ix.dup = last
				}
				explicit[path] = true
			}
			cur = segs
		case unstable.KeyValue:
			ix.keyValue(cur, n)
		}
	}
	return ix
}

func (ix *Index) rangePos(r unstable.Range) Pos {
	if r.Length == 0 {
		return Pos{}
	}
	return ix.span(int(r.Offset), int(r.Offset+r.Length))
}

// span returns the position of the byte range [start, end).
func (ix *Index) span(start, end int) Pos {
	if start >= len(ix.text) {
		start = len(ix.text) - 1
	}
	if start < 0 {
		return Pos{}
	}
	if end <= start {
		end = start + 1
	}
	sl, sc := ix.lineCol(start)
	el, ec := ix.lineCol(end)
	return Pos{Line: sl, Column: sc, EndLine: el, EndColumn: ec}
}

func (ix *Index) lineCol(off int) (int, int) {
	if off > len(ix.text) {
		off = len(ix.text)
	}
	l := sort.Search(len(ix.lineStarts), func(i int) bool { return ix.lineStarts[i] > off }) - 1
	if l < 0 {
		l = 0
	}
	ls := ix.lineStarts[l]
	var col int
	switch {
	case ix.anchorCol > 0 && ix.anchorLine == l && off >= ix.anchorOff:
		col = ix.anchorCol + utf8.RuneCount(ix.text[ix.anchorOff:off])
	case ix.anchorCol > 0 && ix.anchorLine == l && ix.anchorOff-off < off-ls:
		col = ix.anchorCol - utf8.RuneCount(ix.text[off:ix.anchorOff])
	default:
		col = utf8.RuneCount(ix.text[ls:off]) + 1
	}
	ix.anchorLine, ix.anchorOff, ix.anchorCol = l, off, col
	return l + 1, col
}

func (ix *Index) setKey(segs []string, pos Pos) {
	path := PathString(segs...)
	if _, ok := ix.keys[path]; !ok {
		ix.keys[path] = pos
		ix.seq[path] = len(ix.seq)
	}
}

// dupPos returns the position of the first key or table that is written
// twice, which the TOML decoder reports without a position.
func (ix *Index) dupPos() Pos { return ix.dup }

func (ix *Index) keyValue(cur []string, n *unstable.Node) {
	segs := append([]string(nil), cur...)
	it := n.Key()
	var last *unstable.Node
	var lastPos Pos
	truncated := false
	for it.Next() {
		if len(segs) >= maxIndexDepth {
			truncated = true
			break
		}
		kn := it.Node()
		last = kn
		segs = append(segs, string(kn.Data))
		lastPos = ix.rangePos(kn.Raw)
		ix.setKey(segs, lastPos)
	}
	if truncated {
		return
	}
	val := n.Value()
	if val == nil || last == nil {
		return
	}
	if _, exists := ix.vals[PathString(segs...)]; exists && ix.dup.IsZero() {
		ix.dup = lastPos
	}
	ix.value(segs, val, int(last.Raw.Offset+last.Raw.Length))
}

// value records the position of node v at path segs and returns the byte
// offset where the value ends. from is where to start looking for the value.
// Scalars span their whole text; arrays and inline tables are positioned at
// their opening bracket.
func (ix *Index) value(segs []string, v *unstable.Node, from int) int {
	start, end := ix.scanValue(from)
	if len(segs) >= maxIndexDepth {
		return end
	}
	path := PathString(segs...)
	switch v.Kind {
	case unstable.Array, unstable.InlineTable:
		ix.vals[path] = ix.span(start, start+1)
	default:
		ix.vals[path] = ix.span(start, end)
	}
	switch v.Kind {
	case unstable.Array:
		i := 0
		it := v.Children()
		cursor := start + 1
		for it.Next() {
			cs := append(append([]string(nil), segs...), "["+strconv.Itoa(i)+"]")
			cstart, _ := ix.scanValue(cursor)
			ix.setKey(cs, ix.span(cstart, cstart+1))
			cursor = ix.value(cs, it.Node(), cursor)
			i++
		}
	case unstable.InlineTable:
		it := v.Children()
		for it.Next() {
			kv := it.Node()
			if kv.Kind != unstable.KeyValue {
				continue
			}
			cs := append([]string(nil), segs...)
			kit := kv.Key()
			var last *unstable.Node
			truncated := false
			for kit.Next() {
				if len(cs) >= maxIndexDepth {
					truncated = true
					break
				}
				kn := kit.Node()
				last = kn
				cs = append(cs, string(kn.Data))
				ix.setKey(cs, ix.rangePos(kn.Raw))
			}
			if truncated {
				continue
			}
			if cv := kv.Value(); cv != nil && last != nil {
				ix.value(cs, cv, int(last.Raw.Offset+last.Raw.Length))
			}
		}
	}
	return end
}

// scanValue finds the extent [start, end) of the value that begins at or after
// from: it skips blanks, '=', commas, newlines and comments, then scans one
// TOML value (string, bracketed array or inline table, or a bare scalar).
func (ix *Index) scanValue(from int) (start, end int) {
	t := ix.text
	i := from
	for i < len(t) {
		switch t[i] {
		case ' ', '\t', '=', '\r', '\n', ',':
			i++
			continue
		case '#':
			for i < len(t) && t[i] != '\n' {
				i++
			}
			continue
		}
		break
	}
	if i >= len(t) {
		return len(t), len(t)
	}
	return i, ix.valueEnd(i)
}

// valueEnd returns the offset just past the value starting at i.
func (ix *Index) valueEnd(i int) int {
	t := ix.text
	switch c := t[i]; c {
	case '"', '\'':
		if i+2 < len(t) && t[i+1] == c && t[i+2] == c {
			j := i + 3
			for j < len(t) {
				if c == '"' && t[j] == '\\' {
					j += 2
					continue
				}
				if j+2 < len(t) && t[j] == c && t[j+1] == c && t[j+2] == c {
					j += 3
					for k := 0; k < 2 && j < len(t) && t[j] == c; k++ {
						j++
					}
					return j
				}
				j++
			}
			return len(t)
		}
		j := i + 1
		for j < len(t) && t[j] != '\n' {
			if c == '"' && t[j] == '\\' {
				j += 2
				continue
			}
			if t[j] == c {
				return j + 1
			}
			j++
		}
		return min(j, len(t))
	case '[', '{':
		depth := 0
		j := i
		for j < len(t) {
			switch t[j] {
			case '"', '\'':
				j = ix.valueEnd(j)
				continue
			case '#':
				for j < len(t) && t[j] != '\n' {
					j++
				}
				continue
			case '[', '{':
				depth++
			case ']', '}':
				depth--
				if depth == 0 {
					return j + 1
				}
			}
			j++
		}
		return len(t)
	}
	j := i
	for j < len(t) {
		switch t[j] {
		case ',', ']', '}', '\n', '\r', '#', ' ', '\t':
			return j
		}
		j++
	}
	return j
}

// Key returns the position of the key (or table header segment) of path.
func (ix *Index) Key(path string) (Pos, bool) {
	if ix == nil {
		return Pos{}, false
	}
	p, ok := ix.keys[path]
	return p, ok
}

// Value returns the position of the value of path.
func (ix *Index) Value(path string) (Pos, bool) {
	if ix == nil {
		return Pos{}, false
	}
	p, ok := ix.vals[path]
	return p, ok
}

// Pos returns the best position of path: its key, else its value, else the
// position of the nearest ancestor that has one.
func (ix *Index) Pos(path string) Pos { return ix.at(path, false) }

// ValuePos is like Pos but prefers the value of the key.
func (ix *Index) ValuePos(path string) Pos { return ix.at(path, true) }

func (ix *Index) at(path string, value bool) Pos {
	if ix == nil {
		return Pos{}
	}
	for path != "" {
		a, aok := ix.keys[path]
		b, bok := ix.vals[path]
		if value {
			a, aok, b, bok = b, bok, a, aok
		}
		if aok && !a.IsZero() {
			return a
		}
		if bok && !b.IsZero() {
			return b
		}
		path = parentPath(path)
	}
	return Pos{}
}

// order returns the order of first appearance of path (large when unknown).
func (ix *Index) order(path string) int {
	if ix == nil {
		return 1 << 30
	}
	if n, ok := ix.seq[path]; ok {
		return n
	}
	return 1 << 30
}

// parentPath drops the last segment of a path string.
func parentPath(p string) string {
	if p == "" {
		return ""
	}
	// Strip a trailing array index first.
	if p[len(p)-1] == ']' {
		if i := strings.LastIndexByte(p, '['); i > 0 {
			return p[:i]
		}
	}
	if p[len(p)-1] == '"' {
		// Quoted segment: find its opening quote, honouring escapes.
		i := len(p) - 2
		for i >= 0 {
			if p[i] == '"' {
				bs := 0
				for j := i - 1; j >= 0 && p[j] == '\\'; j-- {
					bs++
				}
				if bs%2 == 0 {
					break
				}
			}
			i--
		}
		if i <= 0 {
			return ""
		}
		return strings.TrimSuffix(p[:i], ".")
	}
	if i := strings.LastIndexByte(p, '.'); i >= 0 {
		return p[:i]
	}
	return ""
}
