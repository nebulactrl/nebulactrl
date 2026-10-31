package projectconfig

import (
	"encoding/json"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// t1Unknown is the diagnostic of the documented example.
func t1Unknown() Diagnostic {
	return Diagnostic{
		Severity: SeverityError, Code: CodeUnknownKey, Message: `unknown key "healthcheck"`,
		Path: "services.web.healthcheck", Line: 19, Column: 1, EndLine: 19, EndColumn: 12,
		Suggestions: []string{"health_check"},
	}
}

func TestDiagnosticRender(t *testing.T) {
	withHint := t1Unknown()
	withHint.Hint = "write `health_check`"

	withEnv := withHint
	withEnv.Environment = "staging"

	two := t1Unknown()
	two.Suggestions = []string{"health_check", "health"}
	three := t1Unknown()
	three.Suggestions = []string{"a", "b", "c"}

	quoted := t1Unknown()
	quoted.Suggestions = []string{`domains."a.b"`}

	noSuggestion := t1Unknown()
	noSuggestion.Suggestions = nil

	tests := []struct {
		name string
		d    Diagnostic
		file string
		want string
	}{
		{
			"the documented example", t1Unknown(), "",
			`nebula.toml:19:1: error[unknown_key] services.web.healthcheck: unknown key "healthcheck"; did you mean "health_check"?`,
		},
		{
			"another file name", t1Unknown(), "infra/nebula.toml",
			`infra/nebula.toml:19:1: error[unknown_key] services.web.healthcheck: unknown key "healthcheck"; did you mean "health_check"?`,
		},
		{
			"hint", withHint, "",
			"nebula.toml:19:1: error[unknown_key] services.web.healthcheck: unknown key \"healthcheck\"; did you mean \"health_check\"? (write `health_check`)",
		},
		{
			"environment", withEnv, "",
			"nebula.toml:19:1: error[unknown_key] services.web.healthcheck: unknown key \"healthcheck\"; did you mean \"health_check\"? (write `health_check`) [environment staging]",
		},
		{
			"two suggestions", two, "",
			`nebula.toml:19:1: error[unknown_key] services.web.healthcheck: unknown key "healthcheck"; did you mean "health_check" or "health"?`,
		},
		{
			"three suggestions", three, "",
			`nebula.toml:19:1: error[unknown_key] services.web.healthcheck: unknown key "healthcheck"; did you mean "a" or "b" or "c"?`,
		},
		{
			"a suggestion is quoted", quoted, "",
			`nebula.toml:19:1: error[unknown_key] services.web.healthcheck: unknown key "healthcheck"; did you mean "domains.\"a.b\""?`,
		},
		{
			"no suggestion", noSuggestion, "",
			`nebula.toml:19:1: error[unknown_key] services.web.healthcheck: unknown key "healthcheck"`,
		},
		{
			"hint without suggestion", Diagnostic{Severity: SeverityError, Code: CodeMissingRequired, Message: "`schema` is required", Path: "schema", Line: 1, Column: 1, Hint: "write schema = 1 as the first key"},
			"",
			"nebula.toml:1:1: error[missing_required] schema: `schema` is required (write schema = 1 as the first key)",
		},
		{
			"environment without hint", Diagnostic{Severity: SeverityError, Code: CodeInvalidValue, Message: "git.branch must be 1 to 255 characters", Path: "environments.staging.services.web.git.branch", Line: 9, Column: 10, Environment: "staging"},
			"",
			"nebula.toml:9:10: error[invalid_value] environments.staging.services.web.git.branch: git.branch must be 1 to 255 characters [environment staging]",
		},
		{
			"warning", Diagnostic{Severity: SeverityWarning, Code: CodeUnresolvedReference, Message: "service \"db\" is not in the file", Path: "services.web.depends_on[0]", Line: 7, Column: 15},
			"",
			`nebula.toml:7:15: warning[unresolved_reference] services.web.depends_on[0]: service "db" is not in the file`,
		},
		{
			"info", Diagnostic{Severity: SeverityInfo, Code: "unmanaged", Message: "a schedule made in the dashboard", Path: "services.db", Line: 3, Column: 2},
			"",
			"nebula.toml:3:2: info[unmanaged] services.db: a schedule made in the dashboard",
		},
		{
			"no position", Diagnostic{Severity: SeverityError, Code: CodeLimitExceeded, Message: "9 attachments; at most 8 are accepted"},
			"",
			"nebula.toml: error[limit_exceeded] 9 attachments; at most 8 are accepted",
		},
		{
			"path but no position", Diagnostic{Severity: SeverityWarning, Code: "secret_missing_value", Message: "set it before deploying", Path: "services.web.variables.KEY"},
			"",
			"nebula.toml: warning[secret_missing_value] services.web.variables.KEY: set it before deploying",
		},
		{
			"no position with an environment", Diagnostic{Severity: SeverityError, Code: CodeLimitExceeded, Message: "m", Environment: "staging"},
			"",
			"nebula.toml: error[limit_exceeded] m [environment staging]",
		},
		{
			"position without a path", Diagnostic{Severity: SeverityError, Code: CodeTOMLSyntax, Message: "invalid TOML: expected character =", Line: 3, Column: 7, EndLine: 3, EndColumn: 8},
			"",
			"nebula.toml:3:7: error[toml_syntax] invalid TOML: expected character =",
		},
		{
			"line without a column", Diagnostic{Severity: SeverityError, Code: CodeInvalidValue, Message: "m", Path: "a", Line: 5},
			"",
			"nebula.toml:5:1: error[invalid_value] a: m",
		},
		{
			"the range end is not rendered", Diagnostic{Severity: SeverityError, Code: CodeInvalidValue, Message: "m", Path: "a", Line: 5, Column: 3, EndLine: 8, EndColumn: 40},
			"",
			"nebula.toml:5:3: error[invalid_value] a: m",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, tc.d.Render(tc.file))
			require.Equal(t, tc.want, tc.d.Render(tc.file), "rendering is deterministic")
			require.NotContains(t, tc.want, "\n", "one diagnostic is one line")
		})
	}
}

func TestDiagnosticsRenderText(t *testing.T) {
	require.Equal(t, "", Diagnostics(nil).RenderText(""))
	require.Equal(t, "", Diagnostics{}.RenderText("x"))

	ds := Diagnostics{
		{Severity: SeverityError, Code: CodeLimitExceeded, Message: "no position"},
		{Severity: SeverityWarning, Code: CodeUnresolvedReference, Message: "second", Path: "b", Line: 9, Column: 1},
		{Severity: SeverityError, Code: CodeUnknownKey, Message: "first", Path: "a", Line: 2, Column: 4},
	}
	want := "my.toml:2:4: error[unknown_key] a: first\n" +
		"my.toml:9:1: warning[unresolved_reference] b: second\n" +
		"my.toml: error[limit_exceeded] no position\n"
	require.Equal(t, want, ds.RenderText("my.toml"), "sorted by position, one line each, positionless last")
	require.Equal(t, "no position", ds[0].Message, "the receiver is not reordered")

	text := ds.RenderText("")
	require.True(t, strings.HasPrefix(text, "nebula.toml:2:4:"), text)
	require.True(t, strings.HasSuffix(text, "\n"))
	require.Equal(t, 3, strings.Count(text, "\n"))
}

// The text a document produces is what the CLI prints.
func TestDiagnosticsRenderTextFromParse(t *testing.T) {
	_, _, ds := t1MustParse(t, "schema = 1\n[services.web]\nimage = \"x\"\nbogus = 1\nport = \"80\"\n")
	text := ds.RenderText("")
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	require.Len(t, lines, 2, text)
	require.True(t, strings.HasPrefix(lines[0], `nebula.toml:4:1: error[unknown_key] services.web.bogus: unknown key "bogus"`), lines[0])
	require.Equal(t, `nebula.toml:5:8: error[wrong_type] services.web.port: expected an integer, found a string`, lines[1])

	_, _, ds = t1MustParse(t, "schema = 1\n[services.web.git]\nconnection = \"gh\"\nrepo = \"acme/shop\"\nbranch = \"main\"\n[environments.staging.services.web.git]\nbranch = \"\"\n")
	text = ds.RenderText("")
	require.Contains(t, text, "nebula.toml:7:10: error[invalid_value] environments.staging.services.web.git.branch: ")
	require.True(t, strings.HasSuffix(strings.TrimSuffix(text, "\n"), " [environment staging]"), text)
}

func TestDiagnosticsSorted(t *testing.T) {
	mk := func(line, col int, code, path, msg string) Diagnostic {
		return Diagnostic{Severity: SeverityError, Code: code, Path: path, Line: line, Column: col, Message: msg}
	}
	in := Diagnostics{
		mk(5, 1, "b", "p", "d1"),
		mk(2, 9, "a", "", "d2"),
		mk(2, 3, "z", "", "d3"),
		mk(0, 0, "a", "", "d4"),
		mk(5, 1, "a", "q", "d5"),
		mk(5, 1, "a", "p", "d6"),
		mk(0, 0, "b", "", "d7"),
		mk(1, 1, "x", "same", "first"),
		mk(1, 1, "x", "same", "second"),
	}
	snapshot := append(Diagnostics(nil), in...)

	got := in.Sorted()
	var msgs []string
	for _, d := range got {
		msgs = append(msgs, d.Message)
	}
	require.Equal(t, []string{"first", "second", "d3", "d2", "d6", "d5", "d1", "d4", "d7"}, msgs,
		"line, column, code, path; equal keys keep their order; no position last")
	require.Equal(t, snapshot, in, "Sorted works on a copy")

	// Sorting twice changes nothing.
	require.Equal(t, got, got.Sorted())

	require.Empty(t, Diagnostics(nil).Sorted())
	require.Empty(t, Diagnostics{}.Sorted())
	require.Len(t, Diagnostics{mk(1, 1, "a", "", "x")}.Sorted(), 1)

	// The result is a copy: changing it leaves the input alone.
	got[0].Message = "changed"
	require.Equal(t, snapshot, in)
}

func TestDiagnosticsSortedOrdersLikeTheFile(t *testing.T) {
	_, _, ds := t1MustParse(t, "schema = 1\n[services.web]\nimage = \"x\"\nzzz = 1\nport = \"80\"\naaa = 2\n")
	sorted := ds.Sorted()
	require.True(t, sort.SliceIsSorted(sorted, func(i, j int) bool { return sorted[i].Line < sorted[j].Line }), sorted.RenderText(""))
}

func TestDiagnosticsDedup(t *testing.T) {
	base := Diagnostic{Severity: SeverityError, Code: CodeInvalidValue, Path: "a.b", Message: "m", Line: 3, Column: 1}
	later := base
	later.Line, later.Severity = 9, SeverityWarning // not part of the identity
	otherEnv := base
	otherEnv.Environment = "staging"
	otherEnv2 := otherEnv
	otherEnv2.Line = 12
	otherPath := base
	otherPath.Path = "a.c"
	otherCode := base
	otherCode.Code = CodeWrongType
	otherMsg := base
	otherMsg.Message = "n"

	got := Diagnostics{base, later, otherEnv, otherPath, base, otherEnv2, otherCode, otherMsg, otherPath}.Dedup()
	require.Equal(t, Diagnostics{base, otherEnv, otherPath, otherCode, otherMsg}, got, "the first of identical diagnostics stays, in order")
	require.Equal(t, 3, got[0].Line)
	require.Equal(t, SeverityError, got[0].Severity)

	require.Empty(t, Diagnostics(nil).Dedup())
	require.Equal(t, Diagnostics{base}, Diagnostics{base}.Dedup())

	// A diagnostic that Dedup keeps is untouched by a later duplicate.
	in := Diagnostics{base, later}
	out := in.Dedup()
	require.Len(t, out, 1)
	require.Equal(t, base, out[0])
	require.Len(t, in, 2, "the input keeps its length")
}

func TestDiagnosticsQueries(t *testing.T) {
	e := Diagnostic{Severity: SeverityError, Code: CodeUnknownKey}
	w := Diagnostic{Severity: SeverityWarning, Code: CodeUnresolvedReference}
	i := Diagnostic{Severity: SeverityInfo, Code: "unmanaged"}

	var nilDs Diagnostics
	require.False(t, nilDs.HasErrors())
	require.False(t, Diagnostics{}.HasErrors())
	require.False(t, Diagnostics{w, i}.HasErrors(), "warnings and infos are not errors")
	require.True(t, Diagnostics{w, e, i}.HasErrors())
	require.True(t, Diagnostics{e}.HasErrors())

	all := Diagnostics{e, w, e, i, w, e}
	require.Equal(t, 3, all.Count(SeverityError))
	require.Equal(t, 2, all.Count(SeverityWarning))
	require.Equal(t, 1, all.Count(SeverityInfo))
	require.Equal(t, 0, all.Count("fatal"))
	require.Equal(t, 0, nilDs.Count(SeverityError))

	require.True(t, all.HasCode(CodeUnknownKey))
	require.True(t, all.HasCode("unmanaged"))
	require.False(t, all.HasCode(CodeWrongType))
	require.False(t, nilDs.HasCode(CodeUnknownKey))
	require.False(t, Diagnostics{e}.HasCode(""), "no diagnostic has an empty code")
}

func TestDiagnosticPosition(t *testing.T) {
	d := Diagnostic{Severity: SeverityError, Code: CodeInvalidValue, Message: "m", Path: "a", Hint: "h", Environment: "e"}
	require.True(t, d.Pos().IsZero())

	p := Pos{Line: 4, Column: 2, EndLine: 5, EndColumn: 9}
	moved := d.WithPos(p)
	require.Equal(t, 4, moved.Line)
	require.Equal(t, 2, moved.Column)
	require.Equal(t, 5, moved.EndLine)
	require.Equal(t, 9, moved.EndColumn)
	require.Equal(t, p, moved.Pos())
	require.False(t, moved.Pos().IsZero())

	// The rest of the diagnostic is untouched, and the receiver is a copy.
	require.Equal(t, "m", moved.Message)
	require.Equal(t, "a", moved.Path)
	require.Equal(t, "h", moved.Hint)
	require.Equal(t, "e", moved.Environment)
	require.Zero(t, d.Line)

	cleared := moved.WithPos(Pos{})
	require.True(t, cleared.Pos().IsZero())

	require.True(t, (&d).IsZeroPos())
	require.False(t, (&moved).IsZeroPos())
}

func TestDiagnosticJSON(t *testing.T) {
	full := t1Unknown()
	full.Hint = "write `health_check`"
	full.Environment = "staging"

	b, err := json.Marshal(full)
	require.NoError(t, err)
	require.Equal(t,
		"{\"severity\":\"error\",\"code\":\"unknown_key\",\"message\":\"unknown key \\\"healthcheck\\\"\","+
			"\"path\":\"services.web.healthcheck\",\"line\":19,\"column\":1,\"endLine\":19,\"endColumn\":12,"+
			"\"hint\":\"write `health_check`\",\"suggestions\":[\"health_check\"],\"environment\":\"staging\"}",
		string(b), "the field names and their order are the contract")

	var keys map[string]any
	require.NoError(t, json.Unmarshal(b, &keys))
	var names []string
	for k := range keys {
		names = append(names, k)
	}
	sort.Strings(names)
	require.Equal(t, []string{"code", "column", "endColumn", "endLine", "environment", "hint", "line", "message", "path", "severity", "suggestions"}, names)
	require.Equal(t, float64(19), keys["line"], "numbers are JSON numbers")
	require.Equal(t, []any{"health_check"}, keys["suggestions"])

	// It reads back unchanged.
	var back Diagnostic
	require.NoError(t, json.Unmarshal(b, &back))
	require.Equal(t, full, back)

	// Everything but severity, code and message is left out when empty.
	bare := Diagnostic{Severity: SeverityWarning, Code: CodeUnresolvedReference, Message: "m"}
	b, err = json.Marshal(bare)
	require.NoError(t, err)
	require.Equal(t, `{"severity":"warning","code":"unresolved_reference","message":"m"}`, string(b))

	// A problem that is not in the file has no position keys at all.
	noPos := Diagnostic{Severity: SeverityError, Code: CodeLimitExceeded, Message: "m", Path: "services", Suggestions: []string{}}
	b, err = json.Marshal(noPos)
	require.NoError(t, err)
	require.Equal(t, `{"severity":"error","code":"limit_exceeded","message":"m","path":"services"}`, string(b), "an empty suggestion list is left out")

	b, err = json.Marshal(Diagnostic{})
	require.NoError(t, err)
	require.Equal(t, `{"severity":"","code":"","message":""}`, string(b))

	// Several suggestions keep their order.
	multi := bare
	multi.Suggestions = []string{"b", "a"}
	b, err = json.Marshal(multi)
	require.NoError(t, err)
	require.Contains(t, string(b), `"suggestions":["b","a"]`)

	// A list is a plain array of the objects.
	b, err = json.Marshal(Diagnostics{bare, noPos})
	require.NoError(t, err)
	require.Equal(t, `[{"severity":"warning","code":"unresolved_reference","message":"m"},{"severity":"error","code":"limit_exceeded","message":"m","path":"services"}]`, string(b))
}

func TestDiagnosticJSONFromParse(t *testing.T) {
	_, _, ds := t1MustParse(t, "schema = 1\n[services.web]\nimage = \"x\"\nhealthcheck = 1\n")
	d := t1Need(t, ds, CodeUnknownKey, "services.web.healthcheck")
	b, err := json.Marshal(d)
	require.NoError(t, err)
	require.JSONEq(t, `{
		"severity": "error", "code": "unknown_key", "message": "unknown key \"healthcheck\"",
		"path": "services.web.healthcheck", "line": 4, "column": 1, "endLine": 4, "endColumn": 12,
		"hint": "write `+"`health_check`"+`", "suggestions": ["health_check"]
	}`, string(b))
}

func TestPosIsZeroAndSeverityNames(t *testing.T) {
	require.Equal(t, "error", SeverityError)
	require.Equal(t, "warning", SeverityWarning)
	require.Equal(t, "info", SeverityInfo)
	require.True(t, Pos{}.IsZero())
	require.True(t, Pos{Column: 3}.IsZero(), "no line, no position")
	require.False(t, Pos{Line: 1}.IsZero())
}

// The diagnostic codes are stable snake_case names.
func TestDiagnosticCodeNames(t *testing.T) {
	codes := []string{
		CodeTOMLSyntax, CodeFileTooLarge, CodeInvalidEncoding, CodeLimitExceeded, CodeSchemaUnsupported, CodeMissingRequired,
		CodeUnknownKey, CodeWrongType, CodeInvalidValue, CodeInvalidSlug, CodeReservedSlug, CodeMissingSource,
		CodeConflictingSource, CodeNotAllowedForKind, CodeDerivedField, CodeUnsetInBase, CodeInvalidUnsetPath,
		CodeCredentialKey, CodeCredentialInURL, CodeLiteralSecret, CodeSecretInFile, CodeSecretHasValue,
		CodeInvalidReference, CodeUnknownServiceReference, CodeUnresolvedReference, CodeReferenceCycle,
		CodeReservedVariable, CodeInvalidTemplate, CodePathEscape, CodeAttachmentMissing, CodeUnknownEnvironment,
		CodeLicenseNotAccepted, CodeDockerUnrestricted, CodeReplicasRequireStateles, CodeEgressBlocked,
		CodeInvalidUpstream, CodeHostnameInUse, CodeProcessCommandRequired, CodeProcessNameReserved,
		CodeProcessPortConflict, CodeProcessSingletonScale,
	}
	seen := map[string]bool{}
	for _, c := range codes {
		require.Regexp(t, `^[a-z]+(_[a-z]+)*$`, c)
		require.False(t, seen[c], "%s is listed twice", c)
		seen[c] = true
	}
	require.Equal(t, "toml_syntax", CodeTOMLSyntax)
	require.Equal(t, "unknown_key", CodeUnknownKey)
	require.Equal(t, "credential_key", CodeCredentialKey)
	require.Equal(t, "unresolved_reference", CodeUnresolvedReference)
}
