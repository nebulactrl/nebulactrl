package projectconfig

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
)

// t1Marker marks a position inside a test document. t1Mark removes it and
// returns the 1-based line and rune column it stood at, so tests never count
// columns by hand.
const t1Marker = "§"

func t1Mark(t *testing.T, text string) (clean string, line, col int) {
	t.Helper()
	i := strings.Index(text, t1Marker)
	require.GreaterOrEqual(t, i, 0, "the test document has no %s marker", t1Marker)
	line = 1 + strings.Count(text[:i], "\n")
	start := strings.LastIndex(text[:i], "\n") + 1
	col = utf8.RuneCountInString(text[start:i]) + 1
	return text[:i] + text[i+len(t1Marker):], line, col
}

// t1MustParse parses text without attachments and checks the contract every
// Parse result keeps: a non-nil File comes with a non-nil Index, and a nil File
// comes with an error.
func t1MustParse(t *testing.T, text string) (*File, *Index, Diagnostics) {
	t.Helper()
	f, idx, ds := Parse([]byte(text), nil)
	if f != nil {
		require.NotNil(t, idx, "Parse returned a File without an Index")
	} else {
		require.Nil(t, idx, "Parse returned an Index without a File")
		require.True(t, ds.HasErrors(), "Parse returned neither a File nor an error")
	}
	return f, idx, ds
}

func t1Codes(ds Diagnostics) []string {
	out := make([]string, len(ds))
	for i, d := range ds {
		out[i] = d.Code
	}
	return out
}

// t1Find returns the first diagnostic with code (and path, when not empty).
func t1Find(ds Diagnostics, code, path string) (Diagnostic, bool) {
	for _, d := range ds {
		if d.Code == code && (path == "" || d.Path == path) {
			return d, true
		}
	}
	return Diagnostic{}, false
}

func t1Need(t *testing.T, ds Diagnostics, code, path string) Diagnostic {
	t.Helper()
	d, ok := t1Find(ds, code, path)
	require.True(t, ok, "no %s diagnostic at %q; got:\n%s", code, path, ds.RenderText(""))
	return d
}

func t1Count(ds Diagnostics, code string) int {
	n := 0
	for _, d := range ds {
		if d.Code == code {
			n++
		}
	}
	return n
}

func t1Errors(ds Diagnostics) Diagnostics {
	var out Diagnostics
	for _, d := range ds {
		if d.Severity == SeverityError {
			out = append(out, d)
		}
	}
	return out
}

const (
	t1Head = "schema = 1\n[services.web]\nimage = \"x\"\n"
	// t1Compose is the smallest compose text the stack rules accept.
	t1Compose = "services:\n  web:\n    image: nginx:1.27\n"
	// t1Token is shaped like a GitHub token; built from parts so the file does
	// not contain a literal credential.
	t1Token = "ghp_" + "aB3aB3aB3aB3aB3aB3aB3aB3aB3aB3aB3aB3"
)

func TestParseEmptyInput(t *testing.T) {
	for name, text := range map[string][]byte{"nil": nil, "empty": {}, "comment only": []byte("# nothing here\n")} {
		t.Run(name, func(t *testing.T) {
			f, idx, ds := Parse(text, nil)
			require.NotNil(t, f)
			require.NotNil(t, idx)
			d := t1Need(t, ds, CodeMissingRequired, "schema")
			require.Equal(t, SeverityError, d.Severity)
			require.Equal(t, 1, d.Line)
			require.Equal(t, 1, d.Column)
			require.Len(t, ds, 1)
			require.Empty(t, f.Environments())
			require.Empty(t, f.AttachmentPaths())

			res, extra := f.Resolve("", idx)
			require.NotNil(t, res)
			require.Empty(t, extra)
			require.Empty(t, res.Services)
		})
	}

	f, idx, ds := Parse(nil, Attachments{})
	require.NotNil(t, f)
	require.NotNil(t, idx)
	require.True(t, ds.HasCode(CodeMissingRequired))
}

func TestResolveNilSafety(t *testing.T) {
	var f *File
	res, ds := f.Resolve("", nil)
	require.Nil(t, res)
	require.Empty(t, ds)
	require.Nil(t, f.AttachmentPaths())

	// A File that Parse did not build has no parse state.
	res, ds = (&File{}).Resolve("staging", nil)
	require.Nil(t, res)
	require.Empty(t, ds)
	require.Nil(t, (&File{}).AttachmentPaths())
}

func TestParseIndexNeverNilWithFile(t *testing.T) {
	deep := "schema = 1\nx = " + strings.Repeat("{ a = ", 15) + "1" + strings.Repeat(" }", 15) + "\n"
	texts := []string{
		"",
		"schema = 1\n",
		"schema = 2\n",
		"schema = \"1\"\n",
		t1Head + "bogus = 1\n",
		t1Head + "port = \"80\"\n",
		"schema = 1\n[a.b.c.d.e.f.g.h.i.j.k.l]\n",
		deep,
		"schema = 1\n[environments]\nbad = 5\n",
		"schema = 1\nservices = []\n",
		"schema = 1\nschema = 1\n",
		"schema = 1\nname = \"unterminated\n",
		"\xef\xbb\xbfschema = 1\n",
		"schema = 1\nname = \"\xff\"\n",
		"schema = 1\n---\n",
	}
	for _, text := range texts {
		f, idx, ds := Parse([]byte(text), nil)
		if f != nil {
			require.NotNil(t, idx, "text %q", text)
		} else {
			require.Nil(t, idx, "text %q", text)
			require.True(t, ds.HasErrors(), "text %q", text)
		}
	}

	// A file that is too large is the other way to get no File.
	f, idx, ds := Parse(bytes.Repeat([]byte("#"), MaxFileBytes+1), nil)
	require.Nil(t, f)
	require.Nil(t, idx)
	require.True(t, ds.HasCode(CodeFileTooLarge))
}

func TestParseSchemaKey(t *testing.T) {
	tests := []struct {
		name     string
		text     string
		wantCode string // "" = no diagnostic at all
		hint     string
	}{
		{"valid", "schema = 1\n", "", ""},
		{"valid with editor key", "schema = 1\n\"$schema\" = \"https://nebula.example.com/schema/nebula.toml.json\"\n", "", ""},
		{"valid with editor comment", "#:schema https://nebula.example.com/schema/nebula.toml.json\nschema = 1\n", "", ""},
		{"missing", "[services.web]\nimage = \"x\"\n", CodeMissingRequired, ""},
		{"two", "schema = §2\n", CodeSchemaUnsupported, ""},
		{"zero", "schema = §0\n", CodeSchemaUnsupported, ""},
		{"negative", "schema = §-1\n", CodeSchemaUnsupported, ""},
		{"string", "schema = §\"1\"\n", CodeWrongType, ""},
		{"float", "schema = §1.0\n", CodeWrongType, "write 1"},
		{"bool", "schema = §true\n", CodeWrongType, ""},
		{"array", "schema = §[1]\n", CodeWrongType, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			text := tc.text
			line, col := 1, 1
			if strings.Contains(text, t1Marker) {
				text, line, col = t1Mark(t, text)
			}
			f, idx, ds := t1MustParse(t, text)
			require.NotNil(t, f)
			require.NotNil(t, idx)
			if tc.wantCode == "" {
				require.Empty(t, ds)
				return
			}
			d := t1Need(t, ds, tc.wantCode, "schema")
			require.Equal(t, SeverityError, d.Severity)
			require.Equal(t, line, d.Line)
			require.Equal(t, col, d.Column)
			require.Contains(t, d.Hint, tc.hint)
			// Exactly one problem is reported for the key.
			require.Len(t, ds, 1, "got:\n%s", ds.RenderText(""))
		})
	}

	t.Run("editor key must be a string", func(t *testing.T) {
		text, line, col := t1Mark(t, "\"$schema\" = §1\nschema = 1\n")
		_, _, ds := t1MustParse(t, text)
		d := t1Need(t, ds, CodeWrongType, `"$schema"`)
		require.Equal(t, line, d.Line)
		require.Equal(t, col, d.Column)
	})

	t.Run("editor key is kept", func(t *testing.T) {
		f, _, ds := t1MustParse(t, "schema = 1\n\"$schema\" = \"https://nebula.example.com/schema/nebula.toml.json\"\n")
		require.Empty(t, ds)
		require.NotNil(t, f.SchemaURL)
		require.Equal(t, "https://nebula.example.com/schema/nebula.toml.json", *f.SchemaURL)
		require.NotNil(t, f.Schema)
		require.Equal(t, SchemaVersion, *f.Schema)
	})
}

// Every table of the format rejects an unknown key at the key's own position.
// The marker stands in front of the unknown key.
func TestParseUnknownKeysEveryDepth(t *testing.T) {
	tests := []struct{ name, path, text string }{
		{"root", "bogus", "schema = 1\n§bogus = 1\n"},
		{"service", "services.web.bogus", t1Head + "§bogus = 1\n"},
		{"git", "services.web.git.bogus", "schema = 1\n[services.web.git]\nbranch = \"main\"\n§bogus = 1\n"},
		{"build", "services.web.build.bogus", "schema = 1\n[services.web.build]\ntype = \"auto\"\n§bogus = 1\n"},
		{"health_check", "services.web.health_check.bogus", "schema = 1\n[services.web.health_check]\ntype = \"http\"\n§bogus = 1\n"},
		{"pre_deploy hook", "services.web.pre_deploy.bogus", "schema = 1\n[services.web.pre_deploy]\ncommand = [\"a\"]\n§bogus = 1\n"},
		{"post_deploy hook", "services.web.post_deploy.bogus", "schema = 1\n[services.web.post_deploy]\ncommand = [\"a\"]\n§bogus = 1\n"},
		{"database", "services.db.database.bogus", "schema = 1\n[services.db.database]\nengine = \"postgres\"\n§bogus = 1\n"},
		{"route", "services.r.route.bogus", "schema = 1\n[services.r.route]\nprotocol = \"http\"\n§bogus = 1\n"},
		{"upstream inline table", "services.r.route.upstreams[0].bogus", "schema = 1\n[services.r.route]\nupstreams = [{ address = \"a:1\", §bogus = 1 }]\n"},
		{"stack", "services.s.stack.bogus", "schema = 1\n[services.s.stack]\nwait_seconds = 5\n§bogus = 1\n"},
		{"stack resources", "services.s.stack.resources.web.bogus", "schema = 1\n[services.s.stack.resources.web]\n§bogus = 1\n"},
		{"domain", `services.web.domains."a.acme.com".bogus`, "schema = 1\n[services.web.domains.\"a.acme.com\"]\n§bogus = 1\n"},
		{"options", `services.web.domains."a.acme.com".options.bogus`, "schema = 1\n[services.web.domains.\"a.acme.com\".options]\n§bogus = 1\n"},
		{"cors", `services.web.domains."a.acme.com".options.cors.bogus`, "schema = 1\n[services.web.domains.\"a.acme.com\".options.cors]\n§bogus = 1\n"},
		{"redirect item", `services.web.domains."a.acme.com".options.redirects[0].bogus`, "schema = 1\n[[services.web.domains.\"a.acme.com\".options.redirects]]\nregex = \"a\"\n§bogus = 1\n"},
		{"rate_limit", `services.web.domains."a.acme.com".options.rate_limit.bogus`, "schema = 1\n[services.web.domains.\"a.acme.com\".options.rate_limit]\n§bogus = 1\n"},
		{"access", `services.web.domains."a.acme.com".options.access.bogus`, "schema = 1\n[services.web.domains.\"a.acme.com\".options.access]\n§bogus = 1\n"},
		{"cloudflare", `services.web.domains."a.acme.com".cloudflare.bogus`, "schema = 1\n[services.web.domains.\"a.acme.com\".cloudflare]\n§bogus = 1\n"},
		{"files entry", `services.web.files."/etc/a".bogus`, "schema = 1\n[services.web.files.\"/etc/a\"]\ncontent = \"x\"\n§bogus = 1\n"},
		{"scale", "services.web.scale.bogus", "schema = 1\n[services.web.scale]\nreplicas = 1\n§bogus = 1\n"},
		{"autoscaling", "services.web.scale.autoscaling.bogus", "schema = 1\n[services.web.scale.autoscaling]\nenabled = true\n§bogus = 1\n"},
		{"uptime", "services.web.uptime.bogus", "schema = 1\n[services.web.uptime]\nenabled = true\n§bogus = 1\n"},
		{"tasks entry", "services.web.tasks.t.bogus", "schema = 1\n[services.web.tasks.t]\n§bogus = 1\n"},
		{"backups entry", "services.db.backups.nightly.bogus", "schema = 1\n[services.db.backups.nightly]\n§bogus = 1\n"},
		{"docker", "services.web.docker.bogus", "schema = 1\n[services.web.docker]\ninit = true\n§bogus = 1\n"},
		{"ulimit", "services.web.docker.ulimits.nofile.bogus", "schema = 1\n[services.web.docker.ulimits.nofile]\nsoft = 1\nhard = 1\n§bogus = 1\n"},
		{"tmpfs", `services.web.docker.tmpfs."/tmp".bogus`, "schema = 1\n[services.web.docker.tmpfs.\"/tmp\"]\n§bogus = 1\n"},
		{"device", "services.web.docker.devices[0].bogus", "schema = 1\n[[services.web.docker.devices]]\nhost_path = \"/dev/x\"\n§bogus = 1\n"},
		{"gpus", "services.web.docker.gpus.bogus", "schema = 1\n[services.web.docker.gpus]\ncount = 1\n§bogus = 1\n"},
		{"variable table", "services.web.variables.X.bogus", "schema = 1\n[services.web.variables]\nX = { value = \"1\", §bogus = 1 }\n"},
		{"service secrets", "services.web.secrets.bogus", "schema = 1\n[services.web.secrets]\nkeys = []\n§bogus = 1\n"},
		{"root secrets", "secrets.bogus", "schema = 1\n[secrets]\n§bogus = 1\n"},
		{"root variable table", "variables.X.bogus", "schema = 1\n[variables]\nX = { value = \"1\", §bogus = 1 }\n"},
		{"environment", "environments.staging.bogus", "schema = 1\n[environments.staging]\n§bogus = 1\n"},
		{"environment secrets", "environments.staging.secrets.bogus", "schema = 1\n[environments.staging.secrets]\n§bogus = 1\n"},
		{"environment service", "environments.staging.services.web.bogus", "schema = 1\n[environments.staging.services.web]\n§bogus = 1\n"},
		{"environment service git", "environments.staging.services.web.git.bogus", "schema = 1\n[environments.staging.services.web.git]\n§bogus = 1\n"},
		{"table header", "services.web.bogus", "schema = 1\n[services.web.§bogus]\nx = 1\n"},
		{"dotted key", "services.web.git.bogus", "schema = 1\n[services.web]\nimage = \"x\"\ngit.§bogus = 1\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			text, line, col := t1Mark(t, tc.text)
			_, _, ds := t1MustParse(t, text)
			d := t1Need(t, ds, CodeUnknownKey, tc.path)
			require.Equal(t, SeverityError, d.Severity)
			require.Equal(t, line, d.Line, "line of %s", tc.path)
			require.Equal(t, col, d.Column, "column of %s", tc.path)
			require.Contains(t, d.Message, `"bogus"`)
			require.Equal(t, 1, t1Count(ds, CodeUnknownKey), "got:\n%s", ds.RenderText(""))
		})
	}
}

// A value of the wrong TOML type is reported at the value, never coerced.
func TestParseWrongTypes(t *testing.T) {
	// Each row puts § in front of the offending value (or key, for a table).
	tests := []struct {
		name, text, path string
		msg, hint        string
	}{
		{"string given integer", t1Head + "name = §5\n", "services.web.name", "expected a string, found an integer", ""},
		{"string given bool", t1Head + "name = §true\n", "services.web.name", "expected a string, found a boolean", ""},
		{"string given array", t1Head + "name = §[\"a\"]\n", "services.web.name", "expected a string, found an array", ""},
		{"string given table", t1Head + "name = §{ a = 1 }\n", "services.web.name", "expected a string, found a table", ""},
		{"integer given string", t1Head + "port = §\"80\"\n", "services.web.port", "expected an integer, found a string", ""},
		{"integer given float", t1Head + "port = §80.0\n", "services.web.port", "expected an integer, found a float", "write 80"},
		{"integer given fractional float", t1Head + "port = §80.5\n", "services.web.port", "expected an integer, found a float", ""},
		{"integer given bool", t1Head + "port = §true\n", "services.web.port", "expected an integer, found a boolean", ""},
		{"number given string", t1Head + "cpus = §\"2\"\n", "services.web.cpus", "expected a number, found a string", ""},
		{"bool given string", t1Head + "one_shot = §\"true\"\n", "services.web.one_shot", "expected a boolean, found a string", ""},
		{"bool given integer", t1Head + "block_egress = §1\n", "services.web.block_egress", "expected a boolean, found an integer", ""},
		{"array given string", t1Head + "command = §\"run\"\n", "services.web.command", "expected an array, found a string", ""},
		{"array element of the wrong type", t1Head + "command = [\"a\", §1]\n", "services.web.command[1]", "expected a string, found an integer", ""},
		{"second array element of the wrong type", t1Head + "depends_on = [\"db\", §2.5]\n", "services.web.depends_on[1]", "expected a string, found a float", ""},
		{"table given array of tables", "schema = 1\n[[services.web.§git]]\nbranch = \"x\"\n", "services.web.git", "expected a table, found an array", ""},
		{"table given inline array", t1Head + "health_check = §[{ type = \"http\" }]\n", "services.web.health_check", "expected a table, found an array", ""},
		{"table given string", t1Head + "health_check = §\"http\"\n", "services.web.health_check", "expected a table, found a string", ""},
		{"map given string", t1Head + "variables = §\"x\"\n", "services.web.variables", "expected a table, found a string", ""},
		{"map given array", t1Head + "volumes = §[\"a\"]\n", "services.web.volumes", "expected a table, found an array", ""},
		{"offset date-time", t1Head + "port = §2024-01-02T10:00:00Z\n", "services.web.port", "datetime", "no datetime values"},
		{"local date-time", t1Head + "name = §2024-01-02T10:00:00\n", "services.web.name", "datetime", "no datetime values"},
		{"local date", t1Head + "name = §2024-01-02\n", "services.web.name", "datetime", "no datetime values"},
		{"local time", t1Head + "name = §07:32:00\n", "services.web.name", "datetime", "no datetime values"},
		{"root string", "schema = 1\nproject = §5\n", "project", "expected a string, found an integer", ""},
		{"root bool", "schema = 1\nserver = §true\n", "server", "expected a string, found a boolean", ""},
		{"root table given array", "schema = 1\nservices = §[]\n", "services", "expected a table, found an array", ""},
		{"environment override", "schema = 1\n[environments.staging.services.web]\nport = §\"x\"\n", "environments.staging.services.web.port", "expected an integer, found a string", ""},
		{"inline table field", "schema = 1\n[services.r.route]\nupstreams = [{ address = §5 }]\n", "services.r.route.upstreams[0].address", "expected a string, found an integer", ""},
		{"string map value", "schema = 1\n[services.web.docker.sysctls]\n\"net.core.somaxconn\" = §1024\n", `services.web.docker.sysctls."net.core.somaxconn"`, "expected a string, found an integer", ""},
		{"ulimit value", "schema = 1\n[services.web.docker.ulimits]\nnofile = { soft = §\"1\", hard = 1 }\n", "services.web.docker.ulimits.nofile.soft", "expected an integer, found a string", ""},
		{"autoscaling flag", "schema = 1\n[services.web.scale.autoscaling]\nenabled = §\"yes\"\n", "services.web.scale.autoscaling.enabled", "expected a boolean, found a string", ""},
		{"secret names", "schema = 1\n[services.web.secrets]\nkeys = §\"A\"\n", "services.web.secrets.keys", "expected an array, found a string", ""},
		{"file mode", "schema = 1\n[services.web.files.\"/a\"]\nmode = §644\n", `services.web.files."/a".mode`, "expected a string, found an integer", ""},
		{"table given array value", t1Head + "git = §[\"x\"]\n", "services.web.git", "expected a table, found an array", ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			text, line, col := t1Mark(t, tc.text)
			_, _, ds := t1MustParse(t, text)
			d := t1Need(t, ds, CodeWrongType, tc.path)
			require.Equal(t, SeverityError, d.Severity)
			require.Equal(t, line, d.Line, "line of %s", tc.path)
			require.Equal(t, col, d.Column, "column of %s", tc.path)
			require.Contains(t, d.Message, tc.msg)
			if tc.hint == "" {
				require.NotContains(t, d.Hint, "write ", "hint of %s", tc.path)
			} else {
				require.Contains(t, d.Hint, tc.hint)
			}
			require.Equal(t, 1, t1Count(ds, CodeWrongType), "got:\n%s", ds.RenderText(""))
		})
	}
}

func TestParseVariableValuesAreStrings(t *testing.T) {
	tests := []struct {
		name, text, path string
		hint             string
	}{
		{"integer (the documented example)", "schema = 1\n[services.web.variables]\nPORT = §8080\n", "services.web.variables.PORT", `"8080"`},
		{"integer in the table form", "schema = 1\n[services.web.variables]\nX = { value = §8080 }\n", "services.web.variables.X.value", `"8080"`},
		{"boolean", "schema = 1\n[services.web.variables]\nDEBUG = §true\n", "services.web.variables.DEBUG", `"true"`},
		{"float", "schema = 1\n[services.web.variables]\nRATIO = §0.5\n", "services.web.variables.RATIO", `"0.5"`},
		{"project layer", "schema = 1\n[variables]\nPORT = §8080\n", "variables.PORT", `"8080"`},
		{"environment layer", "schema = 1\n[environments.staging.variables]\nLEVEL = §3\n", "environments.staging.variables.LEVEL", `"3"`},
		{"environment service", "schema = 1\n[environments.staging.services.web.variables]\nLEVEL = §3\n", "environments.staging.services.web.variables.LEVEL", `"3"`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			text, line, col := t1Mark(t, tc.text)
			_, _, ds := t1MustParse(t, text)
			d := t1Need(t, ds, CodeWrongType, tc.path)
			require.Equal(t, SeverityError, d.Severity)
			require.Equal(t, line, d.Line)
			require.Equal(t, col, d.Column)
			require.Contains(t, d.Hint, tc.hint)
			require.Contains(t, d.Hint, "strings")
		})
	}

	t.Run("array", func(t *testing.T) {
		text, line, col := t1Mark(t, "schema = 1\n[services.web.variables]\nX = §[\"a\"]\n")
		_, _, ds := t1MustParse(t, text)
		d := t1Need(t, ds, CodeWrongType, "services.web.variables.X")
		require.Equal(t, line, d.Line)
		require.Equal(t, col, d.Column)
		require.Contains(t, d.Message, "string or a table")
	})

	t.Run("table without value", func(t *testing.T) {
		text, line, _ := t1Mark(t, "schema = 1\n[services.web.variables]\n§X = { scope = \"runtime\" }\n")
		_, _, ds := t1MustParse(t, text)
		d := t1Need(t, ds, CodeMissingRequired, "services.web.variables.X")
		require.Equal(t, line, d.Line)
	})

	t.Run("secret key is not a variable key", func(t *testing.T) {
		text, line, col := t1Mark(t, "schema = 1\n[services.web.variables]\nX = { value = \"1\", §secret = true }\n")
		_, _, ds := t1MustParse(t, text)
		d := t1Need(t, ds, CodeInvalidValue, "services.web.variables.X.secret")
		require.Equal(t, line, d.Line)
		require.Equal(t, col, d.Column)
		require.Contains(t, d.Hint, "secrets.keys")
	})

	t.Run("strings stay strings", func(t *testing.T) {
		f, _, ds := t1MustParse(t, t1Head+"[services.web.variables]\nPORT_NUMBER = \"8080\"\nX = { value = \"1\", scope = \"runtime\", comment = \"c\", rotate_every_days = 30, public = true }\n")
		require.Empty(t, t1Errors(ds), "got:\n%s", ds.RenderText(""))
		vars := f.Services["web"].Variables
		require.Equal(t, "8080", vars["PORT_NUMBER"].Value)
		x := vars["X"]
		require.Equal(t, "1", x.Value)
		require.Equal(t, "runtime", x.Scope)
		require.Equal(t, "c", x.Comment)
		require.NotNil(t, x.RotateEveryDays)
		require.Equal(t, 30, *x.RotateEveryDays)
		require.True(t, x.Public)
	})
}

func TestParseSyntaxErrors(t *testing.T) {
	tests := []struct{ name, text string }{
		{"missing equals sign", "schema = 1\n\nimage §\"x\"\n"},
		{"unterminated string", "schema = 1\nname = \"abc§\nport = 1\n"},
		{"unterminated table header", "schema = 1\n[services.web§\nx = 1\n"},
		{"trailing characters after a value", "schema = 1\nport = 80 §90\n"},
		{"second document marker", "schema = 1\n---§\nschema = 1\n"},
		{"invalid key character", "schema = 1\n§@x = 1\n"},
		{"invalid escape", "schema = 1\nname = \"a§\\qb\"\n"},
		{"CRLF line endings", "schema = 1\r\nname = \"abc§\r\nport = 1\r\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			text, line, col := t1Mark(t, tc.text)
			f, idx, ds := Parse([]byte(text), nil)
			require.Nil(t, f)
			require.Nil(t, idx)
			require.Len(t, ds, 1)
			d := ds[0]
			require.Equal(t, CodeTOMLSyntax, d.Code)
			require.Equal(t, SeverityError, d.Severity)
			require.True(t, strings.HasPrefix(d.Message, "invalid TOML: "), d.Message)
			require.Equal(t, line, d.Line)
			require.Equal(t, col, d.Column)
			require.Equal(t, line, d.EndLine)
			require.Equal(t, col+1, d.EndColumn)
			require.Empty(t, d.Path)
		})
	}

	// The library reports a repeated key or table without a position; the
	// diagnostic must still be a toml_syntax error that says what is repeated.
	dupes := []struct{ name, text, msg string }{
		{"duplicate key", "schema = 1\nschema = 2\n", "already defined"},
		{"duplicate nested key", "schema = 1\n[services.web]\nimage = \"a\"\nimage = \"b\"\n", "already defined"},
		{"duplicate table", "schema = 1\n[services.web]\nimage = \"a\"\n[services.web]\nport = 1\n", "already exists"},
		{"key redefined as table", "schema = 1\nservices = 1\n[services.web]\n", "services"},
	}
	for _, tc := range dupes {
		t.Run(tc.name, func(t *testing.T) {
			f, idx, ds := Parse([]byte(tc.text), nil)
			require.Nil(t, f)
			require.Nil(t, idx)
			require.Len(t, ds, 1)
			require.Equal(t, CodeTOMLSyntax, ds[0].Code)
			require.Equal(t, SeverityError, ds[0].Severity)
			require.Contains(t, ds[0].Message, tc.msg)
		})
	}
}

func TestParseEncoding(t *testing.T) {
	tests := []struct {
		name       string
		text       string
		line, col  int
		wantInText string
	}{
		{"byte order mark", "\xef\xbb\xbfschema = 1\n", 1, 1, "byte order mark"},
		{"NUL byte", "schema = 1\nname = \"a\x00b\"\n", 2, 10, "NUL"},
		{"invalid UTF-8", "schema = 1\nname = \"a\xffb\"\n", 2, 10, "UTF-8"},
		{"invalid UTF-8 after a multi-byte character", "schema = 1\nname = \"é\xffb\"\n", 2, 10, "UTF-8"},
		{"first of several bad bytes", "schema = 1\n\xff\xfe\n", 2, 1, "UTF-8"},
		{"truncated multi-byte sequence", "schema = 1\nname = \"\xe2\x82\"\n", 2, 9, "UTF-8"},
		{"NUL wins over later invalid UTF-8", "schema = 1\n\x00\n\xff\n", 2, 1, "NUL"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f, idx, ds := Parse([]byte(tc.text), nil)
			require.Nil(t, f)
			require.Nil(t, idx)
			require.Len(t, ds, 1)
			d := ds[0]
			require.Equal(t, CodeInvalidEncoding, d.Code)
			require.Equal(t, SeverityError, d.Severity)
			require.Equal(t, tc.line, d.Line)
			require.Equal(t, tc.col, d.Column)
			require.Contains(t, d.Message, tc.wantInText)
		})
	}
}

func TestParseSizeLimit(t *testing.T) {
	over := bytes.Repeat([]byte("#"), MaxFileBytes+1)
	f, idx, ds := Parse(over, nil)
	require.Nil(t, f)
	require.Nil(t, idx)
	require.Len(t, ds, 1)
	require.Equal(t, CodeFileTooLarge, ds[0].Code)
	require.Equal(t, SeverityError, ds[0].Severity)
	require.Zero(t, ds[0].Line, "a size problem is not at a position")
	require.Contains(t, ds[0].Message, fmt.Sprint(len(over)))

	// The size check comes before the encoding check.
	f, _, ds = Parse(append(bytes.Repeat([]byte{0xff}, MaxFileBytes), 'x'), nil)
	require.Nil(t, f)
	require.Equal(t, []string{CodeFileTooLarge}, t1Codes(ds))

	// Exactly the limit is accepted.
	f, _, ds = Parse(bytes.Repeat([]byte("#"), MaxFileBytes), nil)
	require.NotNil(t, f)
	require.False(t, ds.HasCode(CodeFileTooLarge))
}

func t1ManyServices(n int) string {
	var b strings.Builder
	b.WriteString("schema = 1\n")
	for i := range n {
		fmt.Fprintf(&b, "\n[services.s%02d]\nimage = \"x\"\n", i)
	}
	return b.String()
}

func t1ManyEnvironments(n int) string {
	var b strings.Builder
	b.WriteString("schema = 1\n")
	for i := range n {
		fmt.Fprintf(&b, "[environments.e%02d]\n", i)
	}
	return b.String()
}

func TestParseLimits(t *testing.T) {
	t.Run("table nesting", func(t *testing.T) {
		f, _, ds := t1MustParse(t, "schema = 1\n[a.b.c.d.e.f.g.h.i.j.k.l.m]\nx = 1\n")
		require.NotNil(t, f, "a limit problem still returns the File")
		require.Len(t, ds, 1, "the walk stops at the first over-deep table")
		d := t1Need(t, ds, CodeLimitExceeded, "a.b.c.d.e.f.g.h.i.j.k")
		require.Equal(t, SeverityError, d.Severity)
		require.Equal(t, 2, d.Line)
	})

	t.Run("table nesting at the limit is accepted", func(t *testing.T) {
		_, _, ds := t1MustParse(t, "schema = 1\n[a.b.c.d.e.f.g.h.i.j]\nx = 1\n")
		require.False(t, ds.HasCode(CodeLimitExceeded))
		require.True(t, ds.HasCode(CodeUnknownKey), "the decoder keeps going and reports the unknown key")
	})

	t.Run("deeply nested inline table", func(t *testing.T) {
		deep := "schema = 1\nx = " + strings.Repeat("{ a = ", 15) + "1" + strings.Repeat(" }", 15) + "\n"
		f, _, ds := t1MustParse(t, deep)
		require.NotNil(t, f)
		d := t1Need(t, ds, CodeLimitExceeded, "")
		require.Equal(t, 2, d.Line)
	})

	t.Run("services", func(t *testing.T) {
		f, _, ds := t1MustParse(t, t1ManyServices(MaxServices+1))
		require.NotNil(t, f)
		d := t1Need(t, ds, CodeLimitExceeded, "services")
		require.Equal(t, 3, d.Line, "the position of the services table")
		require.Contains(t, d.Message, fmt.Sprint(MaxServices+1))
		require.Contains(t, d.Message, fmt.Sprint(MaxServices))
	})

	t.Run("services at the limit", func(t *testing.T) {
		_, _, ds := t1MustParse(t, t1ManyServices(MaxServices))
		require.Empty(t, t1Errors(ds), "got:\n%s", ds.RenderText(""))
	})

	t.Run("environment blocks", func(t *testing.T) {
		f, _, ds := t1MustParse(t, t1ManyEnvironments(MaxEnvironments+1))
		require.NotNil(t, f)
		d := t1Need(t, ds, CodeLimitExceeded, "environments")
		require.Equal(t, 2, d.Line)
		require.Len(t, f.Environments(), MaxEnvironments+1)
	})

	t.Run("environment blocks at the limit", func(t *testing.T) {
		_, _, ds := t1MustParse(t, t1ManyEnvironments(MaxEnvironments))
		require.False(t, ds.HasCode(CodeLimitExceeded))
	})

	t.Run("string over 64 KiB", func(t *testing.T) {
		text := t1Head + "name = \"" + strings.Repeat("a", MaxStringBytes+1) + "\"\n"
		f, _, ds := t1MustParse(t, text)
		require.NotNil(t, f)
		d := t1Need(t, ds, CodeLimitExceeded, "services.web.name")
		require.Equal(t, 4, d.Line)
		require.Contains(t, d.Message, fmt.Sprint(MaxStringBytes+1))
		require.NotContains(t, d.Message, "aaaa", "the value is never echoed")
	})

	t.Run("string at 64 KiB", func(t *testing.T) {
		text := t1Head + "name = \"" + strings.Repeat("a", MaxStringBytes) + "\"\n"
		_, _, ds := t1MustParse(t, text)
		require.False(t, ds.HasCode(CodeLimitExceeded))
	})

	t.Run("string inside an array", func(t *testing.T) {
		text := t1Head + "command = [\"ok\", \"" + strings.Repeat("a", MaxStringBytes+1) + "\"]\n"
		_, _, ds := t1MustParse(t, text)
		t1Need(t, ds, CodeLimitExceeded, "services.web.command[1]")
	})
}

func TestParseCredentialKeys(t *testing.T) {
	const value = "hunter2-hunter2"
	tests := []struct{ name, path, text string }{
		{"service token", "services.web.token", t1Head + "§token = \"" + value + "\"\n"},
		{"service password", "services.web.password", t1Head + "§password = \"" + value + "\"\n"},
		{"registry auth table", "services.web.registry_auth", t1Head + "§registry_auth = { user = \"a\", password = \"" + value + "\" }\n"},
		{"deploy key", "services.web.deploy_key", t1Head + "§deploy_key = \"" + value + "\"\n"},
		{"git token", "services.web.git.git_token", "schema = 1\n[services.web.git]\n§git_token = \"" + value + "\"\n"},
		{"git password", "services.web.git.password", "schema = 1\n[services.web.git]\nbranch = \"main\"\n§password = \"" + value + "\"\n"},
		{"kebab case", "services.web.git-token", t1Head + "§git-token = \"" + value + "\"\n"},
		{"upper case", "services.web.Token", t1Head + "§Token = \"" + value + "\"\n"},
		{"root api key", "api_key", "schema = 1\n§api_key = \"" + value + "\"\n"},
		{"docker credentials", "services.web.docker.credentials", "schema = 1\n[services.web.docker]\n§credentials = \"" + value + "\"\n"},
		{"environment override", "environments.staging.services.web.password", "schema = 1\n[environments.staging.services.web]\n§password = \"" + value + "\"\n"},
		{"environment block", "environments.staging.token", "schema = 1\n[environments.staging]\n§token = \"" + value + "\"\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			text, line, col := t1Mark(t, tc.text)
			_, _, ds := t1MustParse(t, text)
			d := t1Need(t, ds, CodeCredentialKey, tc.path)
			require.Equal(t, SeverityError, d.Severity)
			require.Equal(t, line, d.Line)
			require.Equal(t, col, d.Column)
			require.Empty(t, d.Suggestions, "a credential key has nothing to be renamed to")
			require.Contains(t, d.Hint, "secrets.keys")
			require.Contains(t, d.Message, "credential")
			require.False(t, ds.HasCode(CodeUnknownKey), "got:\n%s", ds.RenderText(""))
			for _, d := range ds {
				require.NotContains(t, d.Message, "hunter2")
				require.NotContains(t, d.Hint, "hunter2")
			}
		})
	}
}

func TestParseUnknownKeySuggestions(t *testing.T) {
	tests := []struct {
		name, path, text string
		want             string
	}{
		{"alias health_check", "services.web.healthcheck", t1Head + "§healthcheck = { type = \"http\" }\n", "health_check"},
		{"alias memory", "services.web.memory", t1Head + "§memory = 512\n", "memory_mb"},
		{"alias env", "services.web.env", t1Head + "§env = { A = \"1\" }\n", "variables"},
		{"alias cpu", "services.web.cpu", t1Head + "§cpu = 1\n", "cpus"},
		{"alias replicas", "services.web.replicas", t1Head + "§replicas = 2\n", "scale.replicas"},
		{"alias domain", "services.web.domain", t1Head + "§domain = \"a.acme.com\"\n", "domains"},
		{"alias start_command", "services.web.start_command", t1Head + "§start_command = [\"a\"]\n", "command"},
		{"alias volume", "services.web.volume", t1Head + "§volume = { a = \"/a\" }\n", "volumes"},
		{"alias stop_grace", "services.web.stop_grace", t1Head + "§stop_grace = 5\n", "stop_grace_seconds"},
		{"edit distance", "services.web.git.brnch", "schema = 1\n[services.web.git]\n§brnch = \"main\"\n", "branch"},
		{"root table", "servics", "schema = 1\n[§servics.web]\nimage = \"x\"\n", "services"},
		{"environment block", "environments.staging.servr", "schema = 1\n[environments.staging]\n§servr = \"x\"\n", "server"},
		{"environment service", "environments.staging.services.web.healthcheck", "schema = 1\n[environments.staging.services.web]\n§healthcheck = 1\n", "health_check"},
		{"variable table", "services.web.variables.X.val", "schema = 1\n[services.web.variables]\nX = { §val = \"1\" }\n", "value"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			text, line, col := t1Mark(t, tc.text)
			_, _, ds := t1MustParse(t, text)
			d := t1Need(t, ds, CodeUnknownKey, tc.path)
			require.Equal(t, line, d.Line)
			require.Equal(t, col, d.Column)
			require.NotEmpty(t, d.Suggestions)
			require.Equal(t, tc.want, d.Suggestions[0])
			require.Equal(t, "write `"+tc.want+"`", d.Hint)
			require.LessOrEqual(t, len(d.Suggestions), 3)
		})
	}

	t.Run("nothing close", func(t *testing.T) {
		_, _, ds := t1MustParse(t, t1Head+"zzzzzzzz = 1\n")
		d := t1Need(t, ds, CodeUnknownKey, "services.web.zzzzzzzz")
		require.Empty(t, d.Suggestions)
		require.Empty(t, d.Hint)
	})

	t.Run("rendered like the spec", func(t *testing.T) {
		text, line, _ := t1Mark(t, "schema = 1\n[services.web]\nimage = \"x\"\n§healthcheck = 1\n")
		_, _, ds := t1MustParse(t, text)
		d := t1Need(t, ds, CodeUnknownKey, "services.web.healthcheck")
		want := fmt.Sprintf("nebula.toml:%d:1: error[unknown_key] services.web.healthcheck: unknown key \"healthcheck\"; did you mean \"health_check\"? (write `health_check`)", line)
		require.Equal(t, want, d.Render(""))
	})
}

// manage was removed from [domains.<host>.cloudflare]: DNS is always managed in a connected zone.
// It is an error, not a silent no-op, so a file that said manage = false never starts managing DNS unnoticed.
func TestParseCloudflareManageWasRemoved(t *testing.T) {
	const path = `services.web.domains."a.acme.com".cloudflare.manage`
	for _, val := range []string{"true", "false"} {
		text, line, col := t1Mark(t, "schema = 1\n[services.web.domains.\"a.acme.com\".cloudflare]\n§manage = "+val+"\n")
		_, _, ds := t1MustParse(t, text)
		d := t1Need(t, ds, CodeUnknownKey, path)
		require.Equal(t, SeverityError, d.Severity)
		require.Equal(t, line, d.Line)
		require.Equal(t, col, d.Column)
		require.Contains(t, d.Message, "was removed")
		require.Equal(t, "delete the line", d.Hint)
	}

	_, _, ds := t1MustParse(t, "schema = 1\n[environments.staging.services.web.domains.\"a.acme.com\".cloudflare]\nmanage = true\n")
	t1Need(t, ds, CodeUnknownKey, `environments.staging.services.web.domains."a.acme.com".cloudflare.manage`)
}

// A problem found only while resolving an environment carries that
// environment; the same problem in the base does not, and is reported once.
func TestParseEnvironmentTag(t *testing.T) {
	const baseGit = "schema = 1\n\n[services.web.git]\nconnection = \"gh\"\nrepo = \"acme/shop\"\nbranch = \"main\"\n"

	t.Run("problem in an override block", func(t *testing.T) {
		text, line, col := t1Mark(t, baseGit+"\n[environments.staging.services.web.git]\nbranch = §\"\"\n")
		f, idx, ds := t1MustParse(t, text)
		errs := t1Errors(ds)
		require.Len(t, errs, 1, "got:\n%s", ds.RenderText(""))
		d := errs[0]
		require.Equal(t, CodeInvalidValue, d.Code)
		require.Equal(t, "environments.staging.services.web.git.branch", d.Path)
		require.Equal(t, "staging", d.Environment)
		require.Equal(t, line, d.Line)
		require.Equal(t, col, d.Column)

		// Parse already reported everything Resolve would.
		for _, env := range []string{"", "staging", "production"} {
			res, extra := f.Resolve(env, idx)
			require.NotNil(t, res, env)
			require.Empty(t, extra, env)
		}
		res, _ := f.Resolve("production", idx)
		require.Equal(t, "main", *res.Services["web"].Def.Git.Branch, "other environments do not see the override")
		res, _ = f.Resolve("staging", idx)
		require.Equal(t, "", *res.Services["web"].Def.Git.Branch)
		require.Equal(t, "environments.staging.services.web.git.branch", res.Services["web"].Fields["config.gitBranch"].Path)
	})

	t.Run("problem in the base", func(t *testing.T) {
		text, line, col := t1Mark(t, "schema = 1\n\n[services.web.git]\nconnection = \"gh\"\nrepo = \"acme/shop\"\nbranch = §\"\"\n\n[environments.staging]\nserver = \"fra-2\"\n\n[environments.production]\nserver = \"fra-1\"\n")
		f, idx, ds := t1MustParse(t, text)
		errs := t1Errors(ds)
		require.Len(t, errs, 1, "the environments inherit the base problem without repeating it; got:\n%s", ds.RenderText(""))
		d := errs[0]
		require.Equal(t, CodeInvalidValue, d.Code)
		require.Equal(t, "services.web.git.branch", d.Path)
		require.Empty(t, d.Environment)
		require.Equal(t, line, d.Line)
		require.Equal(t, col, d.Column)
		for _, env := range []string{"", "staging", "production"} {
			_, extra := f.Resolve(env, idx)
			require.Empty(t, extra, env)
		}
	})

	t.Run("same problem in the base and in an override", func(t *testing.T) {
		text := "schema = 1\n\n[services.web.git]\nconnection = \"gh\"\nrepo = \"acme/shop\"\nbranch = \"\"\n\n[environments.staging.services.web.git]\nbranch = \"\"\n"
		_, _, ds := t1MustParse(t, text)
		base := t1Need(t, ds, CodeInvalidValue, "services.web.git.branch")
		require.Empty(t, base.Environment)
		over := t1Need(t, ds, CodeInvalidValue, "environments.staging.services.web.git.branch")
		require.Equal(t, "staging", over.Environment)
		require.Equal(t, 6, base.Line)
		require.Equal(t, 9, over.Line)
	})

	t.Run("one problem per environment", func(t *testing.T) {
		text := baseGit + "\n[environments.staging.services.web.git]\nbranch = \"\"\n\n[environments.production.services.web.git]\nbranch = \" \"\n"
		_, _, ds := t1MustParse(t, text)
		errs := t1Errors(ds)
		require.Len(t, errs, 2, "got:\n%s", ds.RenderText(""))
		got := map[string]string{}
		for _, d := range errs {
			got[d.Environment] = d.Path
		}
		require.Equal(t, map[string]string{
			"staging":    "environments.staging.services.web.git.branch",
			"production": "environments.production.services.web.git.branch",
		}, got)
	})

	t.Run("problem in an environment only service", func(t *testing.T) {
		text := "schema = 1\n[environments.staging.services.jobs]\ncommand = [\"a\"]\n"
		_, _, ds := t1MustParse(t, text)
		d := t1Need(t, ds, CodeMissingSource, "")
		require.Equal(t, "staging", d.Environment)
		require.Equal(t, "environments.staging.services.jobs", d.Path)
	})

	t.Run("unset in a base service", func(t *testing.T) {
		text, line, _ := t1Mark(t, t1Head+"§unset = [\"scale\"]\n")
		_, _, ds := t1MustParse(t, text)
		d := t1Need(t, ds, CodeUnsetInBase, "services.web.unset")
		require.Empty(t, d.Environment)
		require.Equal(t, line, d.Line)
	})

	t.Run("only_in in an override", func(t *testing.T) {
		text := t1Head + "\n[environments.staging.services.web]\nonly_in = [\"staging\"]\n"
		_, _, ds := t1MustParse(t, text)
		d := t1Need(t, ds, CodeInvalidValue, "environments.staging.services.web.only_in")
		require.Empty(t, d.Environment, "a document problem is not tied to a resolved view")
		require.Equal(t, 6, d.Line)
	})
}

func TestFileEnvironments(t *testing.T) {
	t.Run("sorted", func(t *testing.T) {
		f, _, _ := t1MustParse(t, "schema = 1\n[environments.zeta]\n[environments.alpha]\n[environments.mid]\nserver = \"x\"\n")
		require.Equal(t, []string{"alpha", "mid", "zeta"}, f.Environments())
	})

	t.Run("none", func(t *testing.T) {
		f, _, _ := t1MustParse(t, "schema = 1\n")
		require.Empty(t, f.Environments())
	})

	t.Run("a block that is not a table", func(t *testing.T) {
		text, line, col := t1Mark(t, "schema = 1\n[environments]\nbad = §5\n[environments.ok]\nserver = \"x\"\n")
		f, idx, ds := t1MustParse(t, text)
		d := t1Need(t, ds, CodeWrongType, "environments.bad")
		require.Equal(t, line, d.Line)
		require.Equal(t, col, d.Column)
		require.Equal(t, []string{"ok"}, f.Environments())
		res, _ := f.Resolve("bad", idx)
		require.NotNil(t, res, "resolving a block that did not decode must not fail")
	})

	t.Run("a block with a bad body is still listed", func(t *testing.T) {
		f, _, ds := t1MustParse(t, "schema = 1\n[environments.staging]\nserver = 5\n")
		require.True(t, ds.HasCode(CodeWrongType))
		require.Equal(t, []string{"staging"}, f.Environments())
	})
}

func TestAttachmentPaths(t *testing.T) {
	const text = `schema = 1

[services.a.stack]
compose_file = "./stacks//a.yml"

[services.b.stack]
compose_file = "stacks/a.yml"

[services.c.files."/etc/c.conf"]
source = "conf/c.conf"

[services.d.files."/etc/d.conf"]
source = "../escape.conf"

[services.e.files."/etc/e.conf"]
source = "/abs.conf"

[services.f.stack]
compose_file = ""

[services.g.stack]
compose_file = 5

[services.h.files."/etc/h.conf"]
content = "inline"

[environments.prod.services.a.files."/etc/x.conf"]
source = "env/x.conf"

[environments.prod.services.c.files."/etc/c2.conf"]
source = "conf/./c.conf"
`
	f, _, ds := t1MustParse(t, text)
	require.NotNil(t, f)
	require.True(t, ds.HasCode(CodeWrongType), "the bad entries are reported by Parse, not by AttachmentPaths")
	want := []string{"stacks/a.yml", "conf/c.conf", "env/x.conf"}
	for range 20 {
		require.Equal(t, want, f.AttachmentPaths(), "cleaned, deduplicated, in file order; invalid paths skipped; overrides included")
	}

	f, _, _ = t1MustParse(t, "schema = 1\n")
	require.Empty(t, f.AttachmentPaths())

	// Order follows the file, not the alphabet.
	f, _, _ = t1MustParse(t, "schema = 1\n[services.z.stack]\ncompose_file = \"zz.yml\"\n[services.a.stack]\ncompose_file = \"aa.yml\"\n")
	require.Equal(t, []string{"zz.yml", "aa.yml"}, f.AttachmentPaths())
}

func TestParseAttachmentsResolved(t *testing.T) {
	const doc = `schema = 1

[services.s.stack]
compose_file = "docker-compose.yml"

[services.web]
image = "nginx:1.27"

[services.web.files."/etc/app.conf"]
source = "conf/app.conf"
`
	att := Attachments{"docker-compose.yml": []byte(t1Compose), "conf/app.conf": []byte("listen = 80\n")}
	f, idx, ds := Parse([]byte(doc), att)
	require.NotNil(t, f)
	require.Empty(t, ds, "got:\n%s", ds.RenderText(""))
	require.Equal(t, []string{"docker-compose.yml", "conf/app.conf"}, f.AttachmentPaths())

	// The document keeps what was written.
	require.NotNil(t, f.Services["s"].Stack.ComposeFile)
	require.Equal(t, "docker-compose.yml", *f.Services["s"].Stack.ComposeFile)
	require.Nil(t, f.Services["s"].Stack.Compose)
	require.NotNil(t, f.Services["web"].Files["/etc/app.conf"].Source)

	res, extra := f.Resolve("", idx)
	require.NotNil(t, res)
	require.Empty(t, extra)

	stack := res.Services["s"]
	require.NotNil(t, stack.Def.Stack.Compose)
	require.Equal(t, t1Compose, *stack.Def.Stack.Compose)
	require.Nil(t, stack.Def.Stack.ComposeFile)
	require.Equal(t, t1Compose, stack.Fields["config.stack.compose"].Value)

	web := res.Services["web"]
	file := web.Def.Files["/etc/app.conf"]
	require.NotNil(t, file.Content)
	require.Equal(t, "listen = 80\n", *file.Content)
	require.Nil(t, file.Source)
	require.Equal(t, []FileMount{{Path: "/etc/app.conf", Mode: "0644", Content: "listen = 80\n"}}, web.Fields["config.files"].Value)
}

func TestParseAttachmentKeys(t *testing.T) {
	tests := []struct {
		name, written, key string
	}{
		{"as written", "docker-compose.yml", "docker-compose.yml"},
		{"as written with dot", "./docker-compose.yml", "./docker-compose.yml"},
		{"cleaned form of a dotted path", "./docker-compose.yml", "docker-compose.yml"},
		{"dotted form of a clean path", "docker-compose.yml", "./docker-compose.yml"},
		{"cleaned form of a doubled slash", "stacks//a.yml", "stacks/a.yml"},
		{"cleaned form of an inner dot", "stacks/./a.yml", "stacks/a.yml"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			doc := fmt.Sprintf("schema = 1\n[services.s.stack]\ncompose_file = %q\n", tc.written)
			f, idx, ds := Parse([]byte(doc), Attachments{tc.key: []byte(t1Compose)})
			require.Empty(t, ds, "got:\n%s", ds.RenderText(""))
			res, _ := f.Resolve("", idx)
			require.Equal(t, t1Compose, *res.Services["s"].Def.Stack.Compose)
		})
	}
}

func TestParseAttachmentMissing(t *testing.T) {
	t.Run("compose file", func(t *testing.T) {
		text, line, col := t1Mark(t, "schema = 1\n[services.s.stack]\ncompose_file = §\"docker-compose.yml\"\n")
		_, _, ds := t1MustParse(t, text)
		d := t1Need(t, ds, CodeAttachmentMissing, "services.s.stack.compose_file")
		require.Equal(t, SeverityError, d.Severity)
		require.Equal(t, line, d.Line)
		require.Equal(t, col, d.Column)
		require.Contains(t, d.Hint, "attachment")
		require.Contains(t, d.Message, "docker-compose.yml")
	})

	t.Run("file source", func(t *testing.T) {
		text, line, col := t1Mark(t, t1Head+"[services.web.files.\"/etc/a.conf\"]\nsource = §\"conf/a.conf\"\n")
		_, _, ds := t1MustParse(t, text)
		d := t1Need(t, ds, CodeAttachmentMissing, `services.web.files."/etc/a.conf".source`)
		require.Equal(t, line, d.Line)
		require.Equal(t, col, d.Column)
	})

	t.Run("another file was supplied", func(t *testing.T) {
		doc := "schema = 1\n[services.s.stack]\ncompose_file = \"docker-compose.yml\"\n"
		_, _, ds := Parse([]byte(doc), Attachments{"other.yml": []byte(t1Compose)})
		require.True(t, ds.HasCode(CodeAttachmentMissing))
	})

	t.Run("only in an environment", func(t *testing.T) {
		text, line, col := t1Mark(t, t1Head+"\n[environments.staging.services.web.files.\"/x\"]\nsource = §\"missing.conf\"\n")
		_, _, ds := t1MustParse(t, text)
		d := t1Need(t, ds, CodeAttachmentMissing, `environments.staging.services.web.files."/x".source`)
		require.Equal(t, "staging", d.Environment)
		require.Equal(t, line, d.Line)
		require.Equal(t, col, d.Column)
	})

	t.Run("both content and source", func(t *testing.T) {
		doc := t1Head + "[services.web.files.\"/a\"]\ncontent = \"x\"\nsource = \"a.conf\"\n"
		_, _, ds := Parse([]byte(doc), Attachments{"a.conf": []byte("y")})
		t1Need(t, ds, CodeInvalidValue, `services.web.files."/a".source`)
	})
}

func TestParseAttachmentPathEscape(t *testing.T) {
	for _, written := range []string{`../x.yml`, `/abs/x.yml`, `a/../../x.yml`, `a/..`, `C:\\x.yml`, `..\\x.yml`, `\\share\\x.yml`} {
		t.Run("compose_file "+written, func(t *testing.T) {
			text, line, col := t1Mark(t, "schema = 1\n[services.s.stack]\ncompose_file = §\""+written+"\"\n")
			doc := []byte(text)
			// Even when the escaping key is supplied it is never looked up.
			f, _, ds := Parse(doc, Attachments{strings.ReplaceAll(written, `\\`, `\`): []byte(t1Compose), "x.yml": []byte(t1Compose)})
			require.NotNil(t, f)
			d := t1Need(t, ds, CodePathEscape, "services.s.stack.compose_file")
			require.Equal(t, SeverityError, d.Severity)
			require.Equal(t, line, d.Line)
			require.Equal(t, col, d.Column)
			require.False(t, ds.HasCode(CodeAttachmentMissing), "got:\n%s", ds.RenderText(""))
			require.Empty(t, f.AttachmentPaths(), "an escaping path is never handed out for reading")
		})
	}

	t.Run("file source", func(t *testing.T) {
		text, line, col := t1Mark(t, t1Head+"[services.web.files.\"/etc/a\"]\nsource = §\"../secret.conf\"\n")
		_, _, ds := t1MustParse(t, text)
		d := t1Need(t, ds, CodePathEscape, `services.web.files."/etc/a".source`)
		require.Equal(t, line, d.Line)
		require.Equal(t, col, d.Column)
		require.False(t, ds.HasCode(CodeAttachmentMissing))
	})

	t.Run("build paths", func(t *testing.T) {
		text, line, col := t1Mark(t, "schema = 1\n[services.web.build]\ndockerfile = §\"../Dockerfile\"\n")
		_, _, ds := t1MustParse(t, text)
		d := t1Need(t, ds, CodePathEscape, "services.web.build.dockerfile")
		require.Equal(t, line, d.Line)
		require.Equal(t, col, d.Column)
	})
}

func TestParseAttachmentContent(t *testing.T) {
	t.Run("secret in a compose file", func(t *testing.T) {
		compose := t1Compose + "    environment:\n      API: " + t1Token + "\n"
		text, line, col := t1Mark(t, "schema = 1\n[services.s.stack]\ncompose_file = §\"docker-compose.yml\"\n")
		_, _, ds := Parse([]byte(text), Attachments{"docker-compose.yml": []byte(compose)})
		d := t1Need(t, ds, CodeSecretInFile, "services.s.stack.compose_file")
		require.Equal(t, SeverityError, d.Severity)
		require.Equal(t, line, d.Line)
		require.Equal(t, col, d.Column)
		require.Equal(t, 1, t1Count(ds, CodeSecretInFile))
		for _, d := range ds {
			require.NotContains(t, d.Message, t1Token)
			require.NotContains(t, d.Hint, t1Token)
		}
	})

	t.Run("secret in a mounted file", func(t *testing.T) {
		text, line, col := t1Mark(t, t1Head+"[services.web.files.\"/etc/app.conf\"]\nsource = §\"app.conf\"\n")
		_, _, ds := Parse([]byte(text), Attachments{"app.conf": []byte("key = " + t1Token + "\n")})
		d := t1Need(t, ds, CodeSecretInFile, `services.web.files."/etc/app.conf".source`)
		require.Equal(t, line, d.Line)
		require.Equal(t, col, d.Column)
		for _, d := range ds {
			require.NotContains(t, d.Message, t1Token)
		}
	})

	t.Run("secret in inline content", func(t *testing.T) {
		text, line, col := t1Mark(t, t1Head+"[services.web.files.\"/etc/app.conf\"]\ncontent = §\"key = "+t1Token+"\"\n")
		_, _, ds := t1MustParse(t, text)
		d := t1Need(t, ds, CodeSecretInFile, `services.web.files."/etc/app.conf".content`)
		require.Equal(t, line, d.Line)
		require.Equal(t, col, d.Column)
	})

	t.Run("attachment that is not UTF-8", func(t *testing.T) {
		// invalid_encoding is reported at the key, not the value.
		text, line, col := t1Mark(t, "schema = 1\n[services.s.stack]\n§compose_file = \"docker-compose.yml\"\n")
		_, _, ds := Parse([]byte(text), Attachments{"docker-compose.yml": {0xff, 0xfe, 0x00}})
		d := t1Need(t, ds, CodeInvalidEncoding, "services.s.stack.compose_file")
		require.Equal(t, line, d.Line)
		require.Equal(t, col, d.Column)
	})

	t.Run("compose and compose_file together", func(t *testing.T) {
		doc := "schema = 1\n[services.s.stack]\ncompose = '''\n" + t1Compose + "'''\ncompose_file = \"docker-compose.yml\"\n"
		_, _, ds := Parse([]byte(doc), Attachments{"docker-compose.yml": []byte(t1Compose)})
		t1Need(t, ds, CodeInvalidValue, "services.s.stack.compose_file")
	})
}

func TestParseAttachmentLimits(t *testing.T) {
	const doc = "schema = 1\n"

	t.Run("too many", func(t *testing.T) {
		att := Attachments{}
		for i := range MaxAttachments + 1 {
			att[fmt.Sprintf("a%d.txt", i)] = []byte("x")
		}
		f, _, ds := Parse([]byte(doc), att)
		require.NotNil(t, f)
		d := t1Need(t, ds, CodeLimitExceeded, "")
		require.Equal(t, SeverityError, d.Severity)
		require.Zero(t, d.Line)
		require.Contains(t, d.Message, fmt.Sprint(MaxAttachments+1))
	})

	t.Run("at the count limit", func(t *testing.T) {
		att := Attachments{}
		for i := range MaxAttachments {
			att[fmt.Sprintf("a%d.txt", i)] = []byte("x")
		}
		_, _, ds := Parse([]byte(doc), att)
		require.False(t, ds.HasCode(CodeLimitExceeded))
	})

	t.Run("one too large", func(t *testing.T) {
		_, _, ds := Parse([]byte(doc), Attachments{"big.txt": bytes.Repeat([]byte("a"), MaxAttachmentBytes+1)})
		d := t1Need(t, ds, CodeLimitExceeded, "")
		require.Contains(t, d.Message, "big.txt")
		require.NotContains(t, d.Message, "aaaa")
	})

	t.Run("one at the size limit", func(t *testing.T) {
		_, _, ds := Parse([]byte(doc), Attachments{"big.txt": bytes.Repeat([]byte("a"), MaxAttachmentBytes)})
		require.False(t, ds.HasCode(CodeLimitExceeded))
	})

	t.Run("total too large", func(t *testing.T) {
		att := Attachments{}
		for i := range MaxAttachmentsTotal/MaxAttachmentBytes + 1 {
			att[fmt.Sprintf("a%d.txt", i)] = bytes.Repeat([]byte("a"), MaxAttachmentBytes)
		}
		_, _, ds := Parse([]byte(doc), att)
		d := t1Need(t, ds, CodeLimitExceeded, "")
		require.Contains(t, d.Message, "total")
	})

	t.Run("total at the limit", func(t *testing.T) {
		att := Attachments{}
		for i := range MaxAttachmentsTotal / MaxAttachmentBytes {
			att[fmt.Sprintf("a%d.txt", i)] = bytes.Repeat([]byte("a"), MaxAttachmentBytes)
		}
		_, _, ds := Parse([]byte(doc), att)
		require.False(t, ds.HasCode(CodeLimitExceeded))
	})
}
