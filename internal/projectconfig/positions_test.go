package projectconfig

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
)

func t1P(line, col, endLine, endCol int) Pos {
	return Pos{Line: line, Column: col, EndLine: endLine, EndColumn: endCol}
}

// t1Col returns the 1-based rune column of the first sub in line.
func t1Col(t *testing.T, line, sub string) int {
	t.Helper()
	i := strings.Index(line, sub)
	require.GreaterOrEqual(t, i, 0, "%q is not in %q", sub, line)
	return utf8.RuneCountInString(line[:i]) + 1
}

func t1KeyPos(t *testing.T, ix *Index, path string) Pos {
	t.Helper()
	p, ok := ix.Key(path)
	require.True(t, ok, "no key position for %q", path)
	return p
}

func t1ValuePos(t *testing.T, ix *Index, path string) Pos {
	t.Helper()
	p, ok := ix.Value(path)
	require.True(t, ok, "no value position for %q", path)
	return p
}

func TestPathString(t *testing.T) {
	tests := []struct {
		name string
		segs []string
		want string
	}{
		{"no segments", nil, ""},
		{"one bare", []string{"schema"}, "schema"},
		{"bare segments", []string{"services", "web", "git"}, "services.web.git"},
		{"bare with dash, underscore and digits", []string{"a-b", "c_d", "E1"}, "a-b.c_d.E1"},
		{"hostname is quoted", []string{"services", "web", "domains", "shop.acme.com"}, `services.web.domains."shop.acme.com"`},
		{"file path is quoted", []string{"files", "/etc/app.conf", "source"}, `files."/etc/app.conf".source`},
		{"space is quoted", []string{"a b"}, `"a b"`},
		{"quote is escaped", []string{`say "hi"`}, `"say \"hi\""`},
		{"backslash is escaped", []string{`back\slash`}, `"back\\slash"`},
		{"tab is escaped", []string{"tab\there"}, `"tab\there"`},
		{"non-ASCII stays readable", []string{"é"}, `"é"`},
		{"empty segment", []string{"a", ""}, `a.""`},
		{"index is appended without a dot", []string{"a", "[0]"}, "a[0]"},
		{"index between keys", []string{"a", "[0]", "b"}, "a[0].b"},
		{"two indexes", []string{"a", "[1]", "[2]"}, "a[1][2]"},
		{"index after a quoted key", []string{"a", "x.y", "[3]"}, `a."x.y"[3]`},
		{"leading index", []string{"[1]"}, "[1]"},
		{"index with a large number", []string{"a", "[10]"}, "a[10]"},
		{"not an index: letters", []string{"a", "[x]"}, `a."[x]"`},
		{"not an index: empty", []string{"a", "[]"}, `a."[]"`},
		{"not an index: sign", []string{"a", "[-1]"}, `a."[-1]"`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, PathString(tc.segs...))
		})
	}
}

func TestJoin(t *testing.T) {
	require.Equal(t, "x", join("", "x"))
	require.Equal(t, "a.b", join("a", "b"))
	require.Equal(t, "a[2]", join("a", "[2]"))
	require.Equal(t, `a."x y"`, join("a", "x y"))
	require.Equal(t, `"x y"`, join("", "x y"))
	require.Equal(t, `a."b.c".d`, join(join("a", "b.c"), "d"))
	require.Equal(t, "services.web.command[1]", join(join(join("services", "web"), "command"), "[1]"))
}

func TestSplitKeyPath(t *testing.T) {
	valid := []struct {
		name, in string
		want     []string
	}{
		{"one", "a", []string{"a"}},
		{"dotted", "a.b.c", []string{"a", "b", "c"}},
		{"bare characters", "a-b_c.D1", []string{"a-b_c", "D1"}},
		{"quoted segment with dots", `domains."shop.acme.com"`, []string{"domains", "shop.acme.com"}},
		{"literal quoted segment", `'literal.key'.x`, []string{"literal.key", "x"}},
		{"literal quoted keeps quotes and backslashes", `'a"b\c'`, []string{`a"b\c`}},
		{"two quoted segments", `"a.b"."c.d"`, []string{"a.b", "c.d"}},
		{"spaces around the dots", " a . b ", []string{"a", "b"}},
		{"escaped quote", `"a\"b"`, []string{`a"b`}},
		{"escaped backslash", `"a\\b"`, []string{`a\b`}},
		{"escaped tab", `"tab\there"`, []string{"tab\there"}},
		{"escaped newline", `"new\nline"`, []string{"new\nline"}},
		{"non-ASCII", `"é"`, []string{"é"}},
		{"quoted with a space", `"a b".c`, []string{"a b", "c"}},
	}
	for _, tc := range valid {
		t.Run("valid/"+tc.name, func(t *testing.T) {
			got, ok := splitKeyPath(tc.in)
			require.True(t, ok)
			require.Equal(t, tc.want, got)
		})
	}

	malformed := []struct{ name, in string }{
		{"empty", ""},
		{"blank", "  "},
		{"trailing dot", "a."},
		{"trailing dots", "a.b."},
		{"leading dot", ".a"},
		{"empty segment", "a..b"},
		{"unterminated basic quote", `"abc`},
		{"unterminated literal quote", `'abc`},
		{"unterminated after a dot", `a."b`},
		{"backslash at the end", `"a\`},
		{"unsupported escape", `"a\u0041"`},
		{"unknown escape", `"a\qb"`},
		{"text after a quoted segment", `"a"b`},
		{"two quoted segments without a dot", `"x" "y"`},
		{"space inside a bare segment", "a b"},
		{"bad bare character", "a$b"},
		{"bad bare character after a dot", "a.b/c"},
		{"empty basic key", `""`},
		{"empty literal key", `''`},
		{"index segment", "a[0]"},
		{"table header syntax", "[a]"},
	}
	for _, tc := range malformed {
		t.Run("malformed/"+tc.name, func(t *testing.T) {
			got, ok := splitKeyPath(tc.in)
			require.False(t, ok)
			require.Nil(t, got)
		})
	}
}

func TestPathStringSplitKeyPathRoundTrip(t *testing.T) {
	for _, segs := range [][]string{
		{"a"},
		{"a", "b", "c"},
		{"services", "web", "domains", "shop.acme.com"},
		{"files", "/etc/app.conf", "source"},
		{`say "hi"`},
		{`back\slash`, "x"},
		{"tab\there"},
		{"é", "ü"},
		{"a b"},
		{"x-y_z", "Q9"},
		{"with.dot", "and space", "end"},
		{"domains", "shop.acme.com/api"},
	} {
		path := PathString(segs...)
		got, ok := splitKeyPath(path)
		require.True(t, ok, path)
		require.Equal(t, segs, got, path)
	}

	// An index path is not a key path: unset paths never contain one.
	_, ok := splitKeyPath(PathString("a", "[0]"))
	require.False(t, ok)
}

func TestParentPath(t *testing.T) {
	tests := []struct{ in, want string }{
		{"", ""},
		{"a", ""},
		{"a.b", "a"},
		{"a.b.c", "a.b"},
		{`a."b.c"`, "a"},
		{`"b.c"`, ""},
		{`a."b.c".d`, `a."b.c"`},
		{`a."b.c".d.e`, `a."b.c".d`},
		{`a."say \"hi\""`, "a"},
		{`"say \"hi\""`, ""},
		{`a."q\\"`, "a"},
		{`a."x\""`, "a"},
		{"a.b[1]", "a.b"},
		{`a."x.y"[0]`, `a."x.y"`},
		{"a[0].b", "a[0]"},
		{"a[1][2]", "a[1]"},
		{"a[10]", "a"},
		{"services.web.command[3]", "services.web.command"},
		{`services.web.domains."shop.acme.com".options`, `services.web.domains."shop.acme.com"`},
		{`services.web.domains."shop.acme.com"`, "services.web.domains"},
		{`services.web.files."/etc/a.conf".source`, `services.web.files."/etc/a.conf"`},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			require.Equal(t, tc.want, parentPath(tc.in))
		})
	}

	// parentPath undoes join.
	for _, segs := range [][]string{{"a", "b"}, {"a", "b.c"}, {"a", "b.c", "d"}, {`say "hi"`, "x"}, {"files", "/etc/a", "source"}} {
		full := PathString(segs...)
		require.Equal(t, PathString(segs[:len(segs)-1]...), parentPath(full), full)
	}
}

func TestIndexKeysAndValues(t *testing.T) {
	const text = "a = 1\n\"quoted key\" = 2\nb.c.d = 3\nbare-key_1 = \"v\"\n"
	ix := BuildIndex([]byte(text))

	require.Equal(t, t1P(1, 1, 1, 2), t1KeyPos(t, ix, "a"), "bare key")
	require.Equal(t, t1P(1, 5, 1, 6), t1ValuePos(t, ix, "a"))

	require.Equal(t, t1P(2, 1, 2, 13), t1KeyPos(t, ix, `"quoted key"`), "quoted key: the range includes the quotes")
	require.Equal(t, t1P(2, 16, 2, 17), t1ValuePos(t, ix, `"quoted key"`))

	require.Equal(t, t1P(3, 1, 3, 2), t1KeyPos(t, ix, "b"), "dotted key: every segment has its own position")
	require.Equal(t, t1P(3, 3, 3, 4), t1KeyPos(t, ix, "b.c"))
	require.Equal(t, t1P(3, 5, 3, 6), t1KeyPos(t, ix, "b.c.d"))
	require.Equal(t, t1P(3, 9, 3, 10), t1ValuePos(t, ix, "b.c.d"))
	_, ok := ix.Value("b.c")
	require.False(t, ok, "only the leaf has a value")

	require.Equal(t, t1P(4, 1, 4, 11), t1KeyPos(t, ix, "bare-key_1"))
	require.Equal(t, t1P(4, 14, 4, 17), t1ValuePos(t, ix, "bare-key_1"), "a string value covers its quotes")

	// Pos prefers the key, ValuePos the value.
	require.Equal(t, t1P(1, 1, 1, 2), ix.Pos("a"))
	require.Equal(t, t1P(1, 5, 1, 6), ix.ValuePos("a"))
	require.Equal(t, t1P(3, 3, 3, 4), ix.ValuePos("b.c"), "a table has no value: the key stands in")
}

func TestIndexScalarKinds(t *testing.T) {
	lines := []string{
		"i = 42",
		"f = 1.5",
		"neg = -7",
		"t = true",
		"fl = false",
		"d = 2024-01-02T10:00:00Z",
		"ld = 2024-01-02",
		"lt = 07:32:00",
		"s = 'literal'",
	}
	ix := BuildIndex([]byte(strings.Join(lines, "\n") + "\n"))
	for i, line := range lines {
		key, _, _ := strings.Cut(line, " ")
		want := t1Col(t, line, "= ") + 2 // the value starts after "= "
		got := t1ValuePos(t, ix, key)
		require.Equal(t, i+1, got.Line, key)
		require.Equal(t, want, got.Column, "%s: values without a range of their own are found after the key", key)
		require.Equal(t, i+1, t1KeyPos(t, ix, key).Line)
	}
	require.Equal(t, t1P(1, 5, 1, 7), t1ValuePos(t, ix, "i"), "an integer covers its digits")
}

func TestIndexNoTrailingNewline(t *testing.T) {
	ix := BuildIndex([]byte("x = true"))
	require.Equal(t, t1P(1, 1, 1, 2), t1KeyPos(t, ix, "x"))
	require.Equal(t, t1P(1, 5, 1, 9), t1ValuePos(t, ix, "x"), "a scalar covers its whole text, also at the end of a file without a newline")

	ix = BuildIndex([]byte("a = 1\nb = \"s\""))
	require.Equal(t, t1P(2, 5, 2, 8), t1ValuePos(t, ix, "b"))
}

func TestIndexTableHeaders(t *testing.T) {
	const text = "[a.b]\nx = 1\n[c]\n[a.d]\ny = 2\n[ spaced . \"q.r\" ]\nz = 3\n"
	ix := BuildIndex([]byte(text))

	require.Equal(t, t1P(1, 2, 1, 3), t1KeyPos(t, ix, "a"), "a segment of a header is positioned at the segment")
	require.Equal(t, t1P(1, 4, 1, 5), t1KeyPos(t, ix, "a.b"))
	require.Equal(t, t1P(2, 1, 2, 2), t1KeyPos(t, ix, "a.b.x"))
	require.Equal(t, t1P(2, 5, 2, 6), t1ValuePos(t, ix, "a.b.x"))
	require.Equal(t, t1P(3, 2, 3, 3), t1KeyPos(t, ix, "c"))

	require.Equal(t, t1P(4, 4, 4, 5), t1KeyPos(t, ix, "a.d"))
	require.Equal(t, 1, t1KeyPos(t, ix, "a").Line, "the first appearance of a table wins")
	require.Equal(t, 5, t1KeyPos(t, ix, "a.d.y").Line)

	require.Equal(t, 6, t1KeyPos(t, ix, "spaced").Line)
	require.Equal(t, t1Col(t, "[ spaced . \"q.r\" ]", "spaced"), t1KeyPos(t, ix, "spaced").Column)
	require.Equal(t, t1Col(t, "[ spaced . \"q.r\" ]", "\"q.r\""), t1KeyPos(t, ix, `spaced."q.r"`).Column, "a quoted segment is found under its quoted path")
	require.Equal(t, 7, t1KeyPos(t, ix, `spaced."q.r".z`).Line)
}

// The key of the n-th element of an array of tables is the header of the n-th
// element, not the header of the first.
func TestIndexArrayTables(t *testing.T) {
	const text = "[[a.b]]\nn = 1\n[[a.b]]\nn = 2\n[[a.b]]\nn = 3\n[[x]]\nm = 1\n"
	ix := BuildIndex([]byte(text))

	require.Equal(t, t1P(1, 3, 1, 4), t1KeyPos(t, ix, "a"))
	require.Equal(t, t1P(1, 5, 1, 6), t1KeyPos(t, ix, "a.b"), "the table itself: its first header")

	require.Equal(t, t1P(1, 5, 1, 6), t1KeyPos(t, ix, "a.b[0]"))
	require.Equal(t, t1P(3, 5, 3, 6), t1KeyPos(t, ix, "a.b[1]"), "the second header")
	require.Equal(t, t1P(5, 5, 5, 6), t1KeyPos(t, ix, "a.b[2]"), "the third header")

	// The keys inside each element.
	require.Equal(t, 2, t1KeyPos(t, ix, "a.b[0].n").Line)
	require.Equal(t, 4, t1KeyPos(t, ix, "a.b[1].n").Line)
	require.Equal(t, 6, t1KeyPos(t, ix, "a.b[2].n").Line)
	require.Equal(t, t1P(4, 5, 4, 6), t1ValuePos(t, ix, "a.b[1].n"))

	// Counting restarts for another array of tables.
	require.Equal(t, 7, t1KeyPos(t, ix, "x[0]").Line)
	_, ok := ix.Key("x[1]")
	require.False(t, ok)

	// Diagnostics about an element point at that element.
	require.Equal(t, 3, ix.Pos("a.b[1]").Line)
}

func TestIndexInlineTablesAndArrays(t *testing.T) {
	lines := []string{
		`t = { a = 1, "b.c" = "x", d = { e = true } }`,
		`x = [10, 20, 30]`,
		`y = ["p", "q"]`,
		`u = [{ k = 1 }, { k = 2 }]`,
	}
	ix := BuildIndex([]byte(strings.Join(lines, "\n") + "\n"))

	t.Run("inline table", func(t *testing.T) {
		l := lines[0]
		require.Equal(t, t1P(1, 5, 1, 6), t1ValuePos(t, ix, "t"), "an inline table is at its opening brace")
		require.Equal(t, t1P(1, t1Col(t, l, "a = 1"), 1, t1Col(t, l, "a = 1")+1), t1KeyPos(t, ix, "t.a"))
		require.Equal(t, t1Col(t, l, "1,"), t1ValuePos(t, ix, "t.a").Column)
		require.Equal(t, t1Col(t, l, `"b.c"`), t1KeyPos(t, ix, `t."b.c"`).Column)
		require.Equal(t, t1Col(t, l, `"x"`), t1ValuePos(t, ix, `t."b.c"`).Column)
		require.Equal(t, t1Col(t, l, "d ="), t1KeyPos(t, ix, "t.d").Column)
		require.Equal(t, t1Col(t, l, "e ="), t1KeyPos(t, ix, "t.d.e").Column)
		require.Equal(t, t1Col(t, l, "true"), t1ValuePos(t, ix, "t.d.e").Column, "a boolean has no range of its own")
		require.Equal(t, 1, t1KeyPos(t, ix, "t.d.e").Line)
	})

	t.Run("array of integers", func(t *testing.T) {
		l := lines[1]
		require.Equal(t, t1P(2, 5, 2, 6), t1ValuePos(t, ix, "x"), "an array is at its opening bracket")
		for i, sub := range []string{"10", "20", "30"} {
			path := "x[" + string(rune('0'+i)) + "]"
			got := t1ValuePos(t, ix, path)
			require.Equal(t, 2, got.Line, path)
			require.Equal(t, t1Col(t, l, sub), got.Column, path)
			require.Equal(t, got.Column+2, got.EndColumn, path)
			require.Equal(t, got, ix.ValuePos(path))
		}
		_, ok := ix.Value("x[3]")
		require.False(t, ok)
	})

	t.Run("array of strings", func(t *testing.T) {
		l := lines[2]
		require.Equal(t, t1Col(t, l, `"p"`), t1ValuePos(t, ix, "y[0]").Column)
		require.Equal(t, t1Col(t, l, `"q"`), t1ValuePos(t, ix, "y[1]").Column)
	})

	t.Run("array of inline tables", func(t *testing.T) {
		l := lines[3]
		require.Equal(t, 4, t1KeyPos(t, ix, "u[0].k").Line)
		require.Equal(t, t1Col(t, l, "k = 1"), t1KeyPos(t, ix, "u[0].k").Column)
		require.Equal(t, t1Col(t, l, "k = 2"), t1KeyPos(t, ix, "u[1].k").Column)
		require.Equal(t, t1Col(t, l, "2 }"), t1ValuePos(t, ix, "u[1].k").Column)
		require.Equal(t, t1Col(t, l, "{ k = 2"), t1ValuePos(t, ix, "u[1]").Column)
	})
}

// Elements that carry no range of their own (booleans, dates, nested arrays)
// are found at their own place, not at the first element of the array.
func TestIndexArrayElementsWithoutRange(t *testing.T) {
	lines := []string{
		`flags = [true, false, true]`,
		`mixed = [1, true, "s"]`,
		`nested = [[1], [2, 3], [4]]`,
		`dates = [2024-01-02, 2024-02-03]`,
	}
	ix := BuildIndex([]byte(strings.Join(lines, "\n") + "\n"))

	require.Equal(t, t1Col(t, lines[0], "true"), ix.ValuePos("flags[0]").Column)
	require.Equal(t, t1Col(t, lines[0], "false"), ix.ValuePos("flags[1]").Column)
	require.Equal(t, t1Col(t, lines[0], "true]"), ix.ValuePos("flags[2]").Column)

	require.Equal(t, t1Col(t, lines[1], "1,"), ix.ValuePos("mixed[0]").Column)
	require.Equal(t, t1Col(t, lines[1], "true"), ix.ValuePos("mixed[1]").Column)
	require.Equal(t, t1Col(t, lines[1], `"s"`), ix.ValuePos("mixed[2]").Column)

	require.Equal(t, t1Col(t, lines[2], "[1]"), ix.ValuePos("nested[0]").Column)
	require.Equal(t, t1Col(t, lines[2], "[2, 3]"), ix.ValuePos("nested[1]").Column)
	require.Equal(t, t1Col(t, lines[2], "[4]"), ix.ValuePos("nested[2]").Column)
	require.Equal(t, t1Col(t, lines[2], "2,"), ix.ValuePos("nested[1][0]").Column)
	require.Equal(t, t1Col(t, lines[2], "3]"), ix.ValuePos("nested[1][1]").Column)

	require.Equal(t, t1Col(t, lines[3], "2024-01-02"), ix.ValuePos("dates[0]").Column)
	require.Equal(t, t1Col(t, lines[3], "2024-02-03"), ix.ValuePos("dates[1]").Column)
}

func TestIndexMultiLineValues(t *testing.T) {
	const text = "s = \"\"\"\nline one\nline two\"\"\"\nn = 1\nl = '''\nraw\n'''\nm = 2\narr = [\n  1,\n  2,\n]\nafter = 3\n"
	ix := BuildIndex([]byte(text))

	s := t1ValuePos(t, ix, "s")
	require.Equal(t, 1, s.Line)
	require.Equal(t, 5, s.Column)
	require.Equal(t, 3, s.EndLine, "the range of a multi-line string ends on its last line")
	require.Greater(t, s.EndLine, s.Line)
	require.Greater(t, s.EndColumn, 1)
	require.Equal(t, 4, t1KeyPos(t, ix, "n").Line, "the lines after a multi-line string are counted")
	require.Equal(t, t1P(4, 5, 4, 6), t1ValuePos(t, ix, "n"))

	l := t1ValuePos(t, ix, "l")
	require.Equal(t, 5, l.Line)
	require.Equal(t, 7, l.EndLine)
	require.Equal(t, 8, t1KeyPos(t, ix, "m").Line)

	require.Equal(t, 9, t1KeyPos(t, ix, "arr").Line)
	require.Equal(t, t1P(9, 7, 9, 8), t1ValuePos(t, ix, "arr"))
	require.Equal(t, t1P(10, 3, 10, 4), t1ValuePos(t, ix, "arr[0]"))
	require.Equal(t, t1P(11, 3, 11, 4), t1ValuePos(t, ix, "arr[1]"))
	require.Equal(t, 13, t1KeyPos(t, ix, "after").Line)
}

func TestIndexCommentsAndBlankLines(t *testing.T) {
	const text = "# header comment\n\n# another\nk = 1 # trailing\n\n[t] # table comment\n# inside\nv = [ # after bracket\n  # own line\n  5,\n]\n"
	ix := BuildIndex([]byte(text))
	require.Equal(t, t1P(4, 1, 4, 2), t1KeyPos(t, ix, "k"))
	require.Equal(t, t1P(4, 5, 4, 6), t1ValuePos(t, ix, "k"))
	require.Equal(t, 6, t1KeyPos(t, ix, "t").Line)
	require.Equal(t, 8, t1KeyPos(t, ix, "t.v").Line)
	require.Equal(t, t1P(10, 3, 10, 4), t1ValuePos(t, ix, "t.v[0]"), "comments inside an array are skipped")
}

func TestIndexColumnsCountRunes(t *testing.T) {
	lines := []string{
		`s = { u = "日本語", k = 1 }`,
		`"日本" = 1`,
		`comment = "ü"`,
		`after = "日本語" # ✓`,
		`last = 1`,
	}
	ix := BuildIndex([]byte(strings.Join(lines, "\n") + "\n"))

	k := t1KeyPos(t, ix, "s.k")
	require.Equal(t, 1, k.Line)
	require.Equal(t, 18, k.Column, "columns count characters, not bytes (a byte count gives 22)")
	require.Equal(t, utf8.RuneCountInString(`s = { u = "日本語", `)+1, k.Column)
	require.Equal(t, 22, t1ValuePos(t, ix, "s.k").Column)

	quoted := t1KeyPos(t, ix, `"日本"`)
	require.Equal(t, t1P(2, 1, 2, 5), quoted, "the range of a quoted key is counted in characters")
	require.Equal(t, 8, t1ValuePos(t, ix, `"日本"`).Column)

	require.Equal(t, 4, t1KeyPos(t, ix, "after").Line)
	require.Equal(t, t1P(4, 9, 4, 14), t1ValuePos(t, ix, "after"), "a string with multi-byte characters ends after its closing quote")
	require.Equal(t, 5, t1KeyPos(t, ix, "last").Line)
	require.Equal(t, 1, t1KeyPos(t, ix, "last").Column)
}

func TestIndexColumnAfterMultiByteLine(t *testing.T) {
	ix := BuildIndex([]byte("# 日本語 ✓\nk = 1\n"))
	require.Equal(t, t1P(2, 1, 2, 2), t1KeyPos(t, ix, "k"))
}

func TestIndexCRLF(t *testing.T) {
	ix := BuildIndex([]byte("a = 1\r\nb = 2\r\n[t]\r\nc = \"x\"\r\nd = [1,\r\n 2]\r\n"))
	require.Equal(t, t1P(1, 1, 1, 2), t1KeyPos(t, ix, "a"))
	require.Equal(t, t1P(2, 1, 2, 2), t1KeyPos(t, ix, "b"))
	require.Equal(t, t1P(2, 5, 2, 6), t1ValuePos(t, ix, "b"))
	require.Equal(t, t1P(3, 2, 3, 3), t1KeyPos(t, ix, "t"))
	require.Equal(t, t1P(4, 1, 4, 2), t1KeyPos(t, ix, "t.c"))
	require.Equal(t, t1P(4, 5, 4, 8), t1ValuePos(t, ix, "t.c"))
	require.Equal(t, t1P(5, 5, 5, 6), t1ValuePos(t, ix, "t.d"))
	require.Equal(t, t1P(6, 2, 6, 3), t1ValuePos(t, ix, "t.d[1]"))

	// The same file through Parse: diagnostics carry the same positions.
	_, _, ds := t1MustParse(t, "schema = 1\r\n[services.web]\r\nimage = \"x\"\r\nbogus = 1\r\n")
	d := t1Need(t, ds, CodeUnknownKey, "services.web.bogus")
	require.Equal(t, 4, d.Line)
	require.Equal(t, 1, d.Column)
	require.Equal(t, 4, d.EndLine)
	require.Equal(t, 6, d.EndColumn)

	_, _, ds = t1MustParse(t, "schema = 1\r\n[services.web]\r\nimage = \"x\"\r\nport = \"80\"\r\n")
	d = t1Need(t, ds, CodeWrongType, "services.web.port")
	require.Equal(t, 4, d.Line)
	require.Equal(t, 8, d.Column)
}

func TestIndexPosFallsBackToTheNearestAncestor(t *testing.T) {
	const text = "[services.web]\nimage = \"x\"\n\n[services.web.git]\nbranch = \"main\"\n"
	ix := BuildIndex([]byte(text))

	web := t1KeyPos(t, ix, "services.web")
	require.Equal(t, t1P(1, 11, 1, 14), web)
	require.Equal(t, web, ix.Pos("services.web.bogus"), "an absent key is placed at its table")
	require.Equal(t, web, ix.Pos("services.web.command[3]"), "an absent index is placed at the nearest ancestor that exists")
	require.Equal(t, web, ix.ValuePos("services.web.bogus"))
	require.Equal(t, t1KeyPos(t, ix, "services.web.git"), ix.Pos("services.web.git.submodules"))
	require.Equal(t, t1KeyPos(t, ix, "services"), ix.Pos("services.other.image"), "the services table itself is the nearest ancestor")
	require.Equal(t, t1KeyPos(t, ix, "services"), ix.Pos(`services."a.b".image`), "a quoted segment is stepped over")

	require.Equal(t, t1P(2, 1, 2, 6), ix.Pos("services.web.image"), "the key of an existing path")
	require.Equal(t, t1P(2, 9, 2, 12), ix.ValuePos("services.web.image"), "the value of an existing path")
	require.Equal(t, t1P(5, 10, 5, 16), ix.ValuePos("services.web.git.branch"))

	require.True(t, ix.Pos("").IsZero())
	require.True(t, ix.Pos("nothing.here").IsZero(), "no ancestor, no position")
	require.True(t, ix.ValuePos("nothing").IsZero())

	_, ok := ix.Key("services.web.bogus")
	require.False(t, ok, "Key does not fall back")
	_, ok = ix.Value("services.web.bogus")
	require.False(t, ok, "Value does not fall back")
	_, ok = ix.Value("services.web")
	require.False(t, ok, "a table has no value")
}

func TestIndexNilSafety(t *testing.T) {
	var ix *Index
	_, ok := ix.Key("a")
	require.False(t, ok)
	_, ok = ix.Value("a")
	require.False(t, ok)
	require.True(t, ix.Pos("a").IsZero())
	require.True(t, ix.ValuePos("a").IsZero())
	require.Equal(t, 1<<30, ix.order("a"))

	var r *Resolved
	require.True(t, r.Pos("a").IsZero())
	require.True(t, (&Resolved{}).Pos("a").IsZero())
}

func TestIndexOrder(t *testing.T) {
	ix := BuildIndex([]byte("a = 1\nb = 2\n[t]\nc = 3\n[[arr]]\nz = 1\n"))
	paths := []string{"a", "b", "t", "t.c", "arr", "arr[0]", "arr[0].z"}
	for i := 1; i < len(paths); i++ {
		require.Less(t, ix.order(paths[i-1]), ix.order(paths[i]), "%s before %s", paths[i-1], paths[i])
	}
	require.Equal(t, 1<<30, ix.order("missing"))
}

func TestIndexPartialDocuments(t *testing.T) {
	// A document the parser rejects yields what was read before the error.
	ix := BuildIndex([]byte("a = 1\nb = \nc = 3\n"))
	require.Equal(t, t1P(1, 1, 1, 2), t1KeyPos(t, ix, "a"))
	_, ok := ix.Key("c")
	require.False(t, ok)

	for name, text := range map[string][]byte{
		"nil":        nil,
		"empty":      {},
		"BOM":        []byte("\xef\xbb\xbfa = 1\n"),
		"NUL":        []byte("a = \"\x00\"\n"),
		"bad UTF-8":  []byte("a = \"\xff\"\n"),
		"unclosed":   []byte("a = [1, 2\n"),
		"just a key": []byte("a"),
	} {
		require.NotPanics(t, func() {
			ix := BuildIndex(text)
			ix.Pos("a")
			ix.ValuePos("a[0]")
		}, name)
	}
	require.True(t, BuildIndex(nil).Pos("a").IsZero())
}

func TestPosIsZero(t *testing.T) {
	require.True(t, Pos{}.IsZero())
	require.False(t, Pos{Line: 1}.IsZero())
	require.False(t, t1P(3, 4, 3, 5).IsZero())
}
