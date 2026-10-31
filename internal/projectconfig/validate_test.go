package projectconfig

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// ---- helpers shared by the validation test files ---------------------------

// t2doc wraps a body in a document header: schema 1 and a default server.
func t2doc(body string) string {
	return "schema = 1\nserver = \"srv\"\n\n" + body
}

// t2web is a valid image service named web; append sub-tables after it.
const t2web = "[services.web]\nimage = \"nginx:1\"\nport = 80\n\n"

// t2parse parses a document and requires that it decoded.
func t2parse(t *testing.T, text string) (*File, *Index, Diagnostics) {
	t.Helper()
	f, idx, ds := Parse([]byte(text), nil)
	require.NotNil(t, f, "document did not decode: %s", ds.RenderText(""))
	return f, idx, ds
}

// t2codes lists the codes of ds in order.
func t2codes(ds Diagnostics) []string {
	out := make([]string, 0, len(ds))
	for _, d := range ds {
		out = append(out, d.Code)
	}
	return out
}

// t2find returns the first diagnostic with code (and path, when not empty).
func t2find(ds Diagnostics, code, path string) *Diagnostic {
	for i := range ds {
		if ds[i].Code == code && (path == "" || ds[i].Path == path) {
			return &ds[i]
		}
	}
	return nil
}

// t2lineOf returns the 1-based line of the first line of text containing marker.
func t2lineOf(t *testing.T, text, marker string) int {
	t.Helper()
	for i, l := range strings.Split(text, "\n") {
		if strings.Contains(l, marker) {
			return i + 1
		}
	}
	t.Fatalf("marker %q not found in document", marker)
	return 0
}

// t2lines builds n lines from a format taking the index.
func t2lines(n int, format string) string {
	var b strings.Builder
	for i := range n {
		fmt.Fprintf(&b, format, i)
	}
	return b.String()
}

// t2tasks builds n valid task tables for services.web.
func t2tasks(n int) string {
	return t2lines(n, "[services.web.tasks.t%02d]\nschedule = \"0 7 * * *\"\ncommand = [\"run\"]\n\n")
}

// ---- the code catalog -------------------------------------------------------

type t2case struct {
	name string
	toml string
	// code is the diagnostic code under test.
	code string
	// path is the expected path of the diagnostic ("" = any path).
	path string
	// absent flips the case: no diagnostic with code (at path, when given) is allowed.
	absent bool
	// severity, when set, is the expected severity of the found diagnostic.
	severity string
	// env, when set, is the expected Environment tag of the diagnostic.
	env string
	// line, when set, is a marker: the diagnostic must be on the first line containing it.
	line string
}

func t2run(t *testing.T, cases []t2case) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, ds := Parse([]byte(tc.toml), nil)
			d := t2find(ds, tc.code, tc.path)
			if tc.absent {
				require.Nil(t, d, "unexpected %s diagnostic:\n%s", tc.code, ds.RenderText(""))
				return
			}
			require.NotNil(t, d, "want %s at %q, got:\n%s", tc.code, tc.path, ds.RenderText(""))
			if tc.severity != "" {
				require.Equal(t, tc.severity, d.Severity, d.Render(""))
			}
			if tc.env != "" {
				require.Equal(t, tc.env, d.Environment, d.Render(""))
			}
			if tc.line != "" {
				require.Equal(t, t2lineOf(t, tc.toml, tc.line), d.Line, d.Render(""))
			}
		})
	}
}

const t2pemKey = "-----BEGIN RSA PRIVATE KEY-----\\nMIIBOgIBAAJBAKj34GkxFhD90vcNLYLInFEX6Ppy1tPf9Cnzj4p4WGeKLs1Pt8Qu\\n-----END RSA PRIVATE KEY-----\\n"

func TestValidateCodeCatalog(t *testing.T) {
	t.Run("document", func(t *testing.T) { t2run(t, t2DocumentCases()) })
	t.Run("sources", func(t *testing.T) { t2run(t, t2SourceCases()) })
	t.Run("secrets", func(t *testing.T) { t2run(t, t2SecretCases()) })
	t.Run("references", func(t *testing.T) { t2run(t, t2ReferenceCases()) })
	t.Run("sections", func(t *testing.T) { t2run(t, t2SectionCases()) })
	t.Run("limits", func(t *testing.T) { t2run(t, t2LimitCases()) })
}

func t2DocumentCases() []t2case {
	hugeComment := func(total int) string {
		head := "schema = 1\n# "
		return head + strings.Repeat("a", total-len(head))
	}
	return []t2case{
		{name: "toml_syntax positive", toml: "schema = 1\nimage = = 3\n", code: CodeTOMLSyntax, line: "image = = 3"},
		{name: "toml_syntax unterminated table", toml: "schema = 1\n[services.web\nimage = \"x\"\n", code: CodeTOMLSyntax},
		{name: "toml_syntax duplicate key", toml: "schema = 1\nschema = 1\n", code: CodeTOMLSyntax},
		{name: "toml_syntax negative", toml: t2doc(t2web), code: CodeTOMLSyntax, absent: true},

		{name: "file_too_large positive", toml: hugeComment(MaxFileBytes + 1), code: CodeFileTooLarge},
		{name: "file_too_large negative at the limit", toml: hugeComment(MaxFileBytes), code: CodeFileTooLarge, absent: true},

		{name: "invalid_encoding BOM", toml: "\ufeffschema = 1\n", code: CodeInvalidEncoding},
		{name: "invalid_encoding NUL", toml: "schema = 1\n# a\x00b\n", code: CodeInvalidEncoding},
		{name: "invalid_encoding bad UTF-8", toml: "schema = 1\n# \xff\xfe\n", code: CodeInvalidEncoding},
		{name: "invalid_encoding negative", toml: "schema = 1\n# é ü 日本 ✓\n", code: CodeInvalidEncoding, absent: true},

		{name: "schema_unsupported positive", toml: "schema = 2\n", code: CodeSchemaUnsupported, path: "schema", line: "schema = 2"},
		{name: "schema_unsupported negative", toml: "schema = 1\n", code: CodeSchemaUnsupported, absent: true},

		{name: "missing_required schema", toml: "server = \"srv\"\n", code: CodeMissingRequired, path: "schema"},
		{name: "missing_required negative", toml: "schema = 1\n", code: CodeMissingRequired, absent: true},
		{name: "missing_required git branch", toml: t2doc("[services.web]\n\n[services.web.git]\nconnection = \"gh\"\nrepo = \"acme/web\"\n"), code: CodeMissingRequired, path: "services.web.git.branch"},
		{name: "missing_required git branch negative", toml: t2doc("[services.web]\n\n[services.web.git]\nconnection = \"gh\"\nrepo = \"acme/web\"\nbranch = \"main\"\n"), code: CodeMissingRequired, absent: true},

		{name: "unknown_key service", toml: t2doc("[services.web]\nimage = \"nginx:1\"\nbogus = 1\n"), code: CodeUnknownKey, path: "services.web.bogus", line: "bogus"},
		{name: "unknown_key nested", toml: t2doc(t2web + "[services.web.git]\nbransh = \"main\"\n"), code: CodeUnknownKey, path: "services.web.git.bransh"},
		{name: "unknown_key root", toml: "schema = 1\nbogus = 1\n", code: CodeUnknownKey, path: "bogus"},
		{name: "unknown_key in override", toml: t2doc(t2web + "[environments.staging.services.web]\nbogus = 1\n"), code: CodeUnknownKey, path: "environments.staging.services.web.bogus"},
		{name: "unknown_key variable table", toml: t2doc(t2web + "[services.web.variables]\nA = { value = \"1\", bogus = true }\n"), code: CodeUnknownKey, path: "services.web.variables.A.bogus"},
		{name: "unknown_key negative", toml: t2doc(t2web), code: CodeUnknownKey, absent: true},

		{name: "wrong_type port string", toml: t2doc("[services.web]\nimage = \"nginx:1\"\nport = \"80\"\n"), code: CodeWrongType, path: "services.web.port", line: "port = \"80\""},
		{name: "wrong_type variable integer", toml: t2doc(t2web + "[services.web.variables]\nLEVEL = 8080\n"), code: CodeWrongType, path: "services.web.variables.LEVEL"},
		{name: "wrong_type variable bool", toml: t2doc(t2web + "[services.web.variables]\nDEBUG = true\n"), code: CodeWrongType, path: "services.web.variables.DEBUG"},
		{name: "wrong_type datetime", toml: t2doc("[services.web]\nimage = \"nginx:1\"\nname = 1979-05-27T07:32:00Z\n"), code: CodeWrongType, path: "services.web.name"},
		{name: "wrong_type array expected", toml: t2doc("[services.web]\nimage = \"nginx:1\"\ncommand = \"run\"\n"), code: CodeWrongType, path: "services.web.command"},
		{name: "wrong_type negative", toml: t2doc(t2web), code: CodeWrongType, absent: true},

		{name: "invalid_value port range", toml: t2doc("[services.web]\nimage = \"nginx:1\"\nport = 70000\n"), code: CodeInvalidValue, path: "services.web.port", line: "port = 70000"},
		{name: "invalid_value memory below minimum", toml: t2doc("[services.web]\nimage = \"nginx:1\"\nmemory_mb = 3\n"), code: CodeInvalidValue, path: "services.web.memory_mb"},
		{name: "invalid_value negative", toml: t2doc("[services.web]\nimage = \"nginx:1\"\nmemory_mb = 6\nport = 65535\n"), code: CodeInvalidValue, absent: true},

		{name: "invalid_slug service", toml: t2doc("[services.Bad_Slug]\nimage = \"nginx:1\"\n"), code: CodeInvalidSlug, path: "services.Bad_Slug"},
		{name: "invalid_slug quoted service", toml: t2doc("[services.\"my svc\"]\nimage = \"nginx:1\"\n"), code: CodeInvalidSlug, path: "services.\"my svc\""},
		{name: "invalid_slug environment", toml: t2doc(t2web + "[environments.\"Prod Env\"]\nserver = \"x\"\n"), code: CodeInvalidSlug, path: "environments.\"Prod Env\""},
		{name: "invalid_slug project", toml: "schema = 1\nproject = \"Acme Shop\"\n", code: CodeInvalidSlug, path: "project"},
		{name: "invalid_slug depends_on", toml: t2doc("[services.web]\nimage = \"nginx:1\"\ndepends_on = [\"Bad Slug\"]\n"), code: CodeInvalidSlug, path: "services.web.depends_on[0]"},
		{name: "invalid_slug negative", toml: t2doc("[services.web-2]\nimage = \"nginx:1\"\n\n[environments.pr-123]\nserver = \"x\"\n"), code: CodeInvalidSlug, absent: true},

		{name: "reserved_slug shared", toml: t2doc("[services.shared]\nimage = \"nginx:1\"\n"), code: CodeReservedSlug, path: "services.shared"},
		{name: "reserved_slug team", toml: t2doc("[services.team]\nimage = \"nginx:1\"\n"), code: CodeReservedSlug, path: "services.team"},
		{name: "reserved_slug vault", toml: t2doc("[services.vault]\nimage = \"nginx:1\"\n"), code: CodeReservedSlug, path: "services.vault"},
		{name: "reserved_slug doppler", toml: t2doc("[services.doppler]\nimage = \"nginx:1\"\n"), code: CodeReservedSlug, path: "services.doppler"},
		{name: "reserved_slug in override", toml: t2doc(t2web + "[environments.staging.services.shared]\nimage = \"nginx:1\"\n"), code: CodeReservedSlug, path: "environments.staging.services.shared"},
		{name: "reserved_slug negative", toml: t2doc("[services.sharedx]\nimage = \"nginx:1\"\n"), code: CodeReservedSlug, absent: true},

		{name: "credential_key git token", toml: t2doc(t2web + "[services.web.git]\ntoken = \"abc\"\n"), code: CodeCredentialKey, path: "services.web.git.token", line: "token"},
		{name: "credential_key service level", toml: t2doc("[services.web]\nimage = \"nginx:1\"\nregistry_password = \"x\"\n"), code: CodeCredentialKey, path: "services.web.registry_password"},
		{name: "credential_key root", toml: "schema = 1\npassword = \"x\"\n", code: CodeCredentialKey, path: "password"},
		{name: "credential_key negative plain unknown key", toml: t2doc("[services.web]\nimage = \"nginx:1\"\nbogus = \"x\"\n"), code: CodeCredentialKey, absent: true},

		{name: "unset_in_base positive", toml: t2doc("[services.web]\nimage = \"nginx:1\"\nunset = [\"scale\"]\n"), code: CodeUnsetInBase, path: "services.web.unset", line: "unset"},
		{name: "unset_in_base negative in override", toml: t2doc(t2web + "[services.web.scale]\nreplicas = 2\n\n[environments.staging.services.web]\nunset = [\"scale\"]\n"), code: CodeUnsetInBase, absent: true},
	}
}

func t2SourceCases() []t2case {
	stackBody := "[services.s.stack]\ncompose = '''\nservices:\n  web:\n    image: nginx:1\n'''\n"
	return []t2case{
		{name: "missing_source positive", toml: t2doc("[services.web]\nname = \"Web\"\n"), code: CodeMissingSource, path: "services.web"},
		{name: "missing_source negative", toml: t2doc(t2web), code: CodeMissingSource, absent: true},
		{name: "missing_source env-only service tagged", toml: t2doc(t2web + "[environments.staging.services.api]\nname = \"API\"\n"), code: CodeMissingSource, path: "environments.staging.services.api", env: "staging"},
		{name: "missing_source env-only service with source", toml: t2doc(t2web + "[environments.staging.services.api]\nimage = \"nginx:1\"\n"), code: CodeMissingSource, absent: true},

		{name: "conflicting_source positive", toml: t2doc("[services.web]\nimage = \"nginx:1\"\n\n[services.web.git]\nconnection = \"gh\"\nrepo = \"a/b\"\nbranch = \"main\"\n"), code: CodeConflictingSource, path: "services.web.git"},
		{name: "conflicting_source database and route", toml: t2doc("[services.web]\n\n[services.web.database]\nengine = \"postgres\"\n\n[services.web.route]\nprotocol = \"http\"\n"), code: CodeConflictingSource},
		{name: "conflicting_source negative", toml: t2doc(t2web), code: CodeConflictingSource, absent: true},

		{name: "credential_in_url https userinfo", toml: t2doc("[services.web]\n\n[services.web.git]\nurl = \"https://user:tokvalue99@github.com/acme/web.git\"\nbranch = \"main\"\n"), code: CodeCredentialInURL, path: "services.web.git.url", line: "url ="},
		{name: "credential_in_url user only", toml: t2doc("[services.web]\n\n[services.web.git]\nurl = \"https://deploy@github.com/acme/web.git\"\nbranch = \"main\"\n"), code: CodeCredentialInURL, path: "services.web.git.url"},
		{name: "credential_in_url ssh password", toml: t2doc("[services.web]\n\n[services.web.git]\nurl = \"ssh://git:pw@github.com/acme/web.git\"\nbranch = \"main\"\n"), code: CodeCredentialInURL, path: "services.web.git.url"},
		{name: "credential_in_url image", toml: t2doc("[services.web]\nimage = \"user:pw@registry.example.com/app:1\"\n"), code: CodeCredentialInURL, path: "services.web.image"},
		{name: "credential_in_url negative https", toml: t2doc("[services.web]\n\n[services.web.git]\nurl = \"https://github.com/acme/web.git\"\nbranch = \"main\"\n"), code: CodeCredentialInURL, absent: true},
		{name: "credential_in_url negative ssh url", toml: t2doc("[services.web]\n\n[services.web.git]\nurl = \"ssh://git@github.com/acme/web.git\"\nbranch = \"main\"\n"), code: CodeCredentialInURL, absent: true},
		{name: "credential_in_url negative scp form", toml: t2doc("[services.web]\n\n[services.web.git]\nurl = \"git@github.com:acme/web.git\"\nbranch = \"main\"\n"), code: CodeCredentialInURL, absent: true},
		{name: "credential_in_url negative image digest", toml: t2doc("[services.web]\nimage = \"nginx@sha256:" + strings.Repeat("a", 64) + "\"\n"), code: CodeCredentialInURL, absent: true},

		{name: "path_escape dockerfile", toml: t2doc("[services.web]\n\n[services.web.git]\nconnection = \"gh\"\nrepo = \"a/b\"\nbranch = \"main\"\n\n[services.web.build]\ndockerfile = \"../Dockerfile\"\n"), code: CodePathEscape, path: "services.web.build.dockerfile", line: "../Dockerfile"},
		{name: "path_escape absolute context", toml: t2doc("[services.web]\n\n[services.web.git]\nconnection = \"gh\"\nrepo = \"a/b\"\nbranch = \"main\"\n\n[services.web.build]\ncontext = \"/abs\"\n"), code: CodePathEscape, path: "services.web.build.context"},
		{name: "path_escape compose_file", toml: t2doc("[services.s]\n\n[services.s.stack]\ncompose_file = \"../compose.yml\"\n"), code: CodePathEscape, path: "services.s.stack.compose_file"},
		{name: "path_escape file source", toml: t2doc(t2web + "[services.web.files.\"/etc/a.conf\"]\nsource = \"../a.conf\"\n"), code: CodePathEscape, path: "services.web.files.\"/etc/a.conf\".source"},
		{name: "path_escape negative dot context", toml: t2doc("[services.web]\n\n[services.web.git]\nconnection = \"gh\"\nrepo = \"a/b\"\nbranch = \"main\"\n\n[services.web.build]\ndockerfile = \"apps/web/Dockerfile\"\ncontext = \".\"\n"), code: CodePathEscape, absent: true},

		{name: "attachment_missing file source", toml: t2doc(t2web + "[services.web.files.\"/etc/a.conf\"]\nsource = \"conf/a.conf\"\n"), code: CodeAttachmentMissing, path: "services.web.files.\"/etc/a.conf\".source", line: "source ="},
		{name: "attachment_missing compose_file", toml: t2doc("[services.s]\n\n[services.s.stack]\ncompose_file = \"docker-compose.yml\"\n"), code: CodeAttachmentMissing, path: "services.s.stack.compose_file"},
		{name: "attachment_missing negative inline content", toml: t2doc(t2web + "[services.web.files.\"/etc/a.conf\"]\ncontent = \"x\"\n"), code: CodeAttachmentMissing, absent: true},

		{name: "license_not_accepted redis", toml: t2doc("[services.cache]\n\n[services.cache.database]\nengine = \"redis\"\n"), code: CodeLicenseNotAccepted, path: "services.cache.database.accept_license"},
		{name: "license_not_accepted mongodb", toml: t2doc("[services.m]\n\n[services.m.database]\nengine = \"mongodb\"\n"), code: CodeLicenseNotAccepted},
		{name: "license_not_accepted negative accepted", toml: t2doc("[services.cache]\n\n[services.cache.database]\nengine = \"redis\"\naccept_license = true\n"), code: CodeLicenseNotAccepted, absent: true},
		{name: "license_not_accepted negative permissive engine", toml: t2doc("[services.db]\n\n[services.db.database]\nengine = \"postgres\"\n"), code: CodeLicenseNotAccepted, absent: true},

		{name: "database unknown engine", toml: t2doc("[services.db]\n\n[services.db.database]\nengine = \"postgress\"\n"), code: CodeInvalidValue, path: "services.db.database.engine"},
		{name: "database bad version", toml: t2doc("[services.db]\n\n[services.db.database]\nengine = \"postgres\"\nversion = \"99\"\n"), code: CodeInvalidValue, path: "services.db.database.version"},
		{name: "database missing engine", toml: t2doc("[services.db]\n\n[services.db.database]\nversion = \"17\"\n"), code: CodeMissingRequired, path: "services.db.database.engine"},
		{name: "database valid version", toml: t2doc("[services.db]\n\n[services.db.database]\nengine = \"postgres\"\nversion = \"17\"\n"), code: CodeInvalidValue, absent: true},

		{name: "invalid_upstream both address and service", toml: t2doc("[services.r]\nserver = \"srv\"\n\n[services.r.route]\nprotocol = \"http\"\nupstreams = [{ address = \"10.0.0.1:80\", service = \"web\", port = 80 }]\n\n" + t2web), code: CodeInvalidUpstream, path: "services.r.route.upstreams[0]"},
		{name: "invalid_upstream bad address", toml: t2doc("[services.r]\nserver = \"srv\"\n\n[services.r.route]\nprotocol = \"http\"\nupstreams = [{ address = \"nocolon\" }]\n"), code: CodeInvalidUpstream, path: "services.r.route.upstreams[0].address"},
		{name: "invalid_upstream port out of range", toml: t2doc("[services.r]\nserver = \"srv\"\n\n[services.r.route]\nprotocol = \"http\"\nupstreams = [{ address = \"h:99999\" }]\n"), code: CodeInvalidUpstream, path: "services.r.route.upstreams[0].address"},
		{name: "invalid_upstream targets itself", toml: t2doc("[services.r]\nserver = \"srv\"\n\n[services.r.route]\nprotocol = \"http\"\nupstreams = [{ service = \"r\", port = 80 }]\n"), code: CodeInvalidUpstream, path: "services.r.route.upstreams[0].service"},
		{name: "invalid_upstream targets a stack", toml: t2doc("[services.r]\nserver = \"srv\"\n\n[services.r.route]\nprotocol = \"http\"\nupstreams = [{ service = \"s\", port = 80 }]\n\n" + stackBody), code: CodeInvalidUpstream, path: "services.r.route.upstreams[0].service"},
		{name: "invalid_upstream other server", toml: t2doc("[services.r]\nserver = \"srv\"\n\n[services.r.route]\nprotocol = \"http\"\nupstreams = [{ service = \"web\", port = 80 }]\n\n[services.web]\nserver = \"other\"\nimage = \"nginx:1\"\n"), code: CodeInvalidUpstream, path: "services.r.route.upstreams[0].service"},
		{name: "invalid_upstream negative address", toml: t2doc("[services.r]\nserver = \"srv\"\n\n[services.r.route]\nprotocol = \"http\"\nupstreams = [{ address = \"10.0.0.1:80\" }]\n"), code: CodeInvalidUpstream, absent: true},
		{name: "invalid_upstream negative managed service", toml: t2doc("[services.r]\nserver = \"srv\"\n\n[services.r.route]\nprotocol = \"http\"\nupstreams = [{ service = \"web\", port = 80 }]\n\n" + t2web), code: CodeInvalidUpstream, absent: true},

		{name: "route missing server", toml: "schema = 1\n\n[services.r]\n\n[services.r.route]\nprotocol = \"http\"\nupstreams = [{ address = \"10.0.0.1:80\" }]\n", code: CodeMissingRequired, path: "services.r.server"},
		{name: "route server from root", toml: "schema = 1\nserver = \"srv\"\n\n[services.r]\n\n[services.r.route]\nprotocol = \"http\"\nupstreams = [{ address = \"10.0.0.1:80\" }]\n", code: CodeMissingRequired, absent: true},
		{name: "route server only in an environment block is still missing in the base view", toml: "schema = 1\n\n[services.r]\n\n[services.r.route]\nprotocol = \"http\"\nupstreams = [{ address = \"10.0.0.1:80\" }]\n\n[environments.staging]\nserver = \"stg\"\n", code: CodeMissingRequired, path: "services.r.server"},
		{name: "route tcp without allow list", toml: t2doc("[services.r]\n\n[services.r.route]\nprotocol = \"tcp\"\nlisten_port = 25565\nupstreams = [{ address = \"10.0.0.1:25565\" }]\n"), code: CodeInvalidValue, path: "services.r.route.open_to_internet"},
		{name: "route tcp open to internet", toml: t2doc("[services.r]\n\n[services.r.route]\nprotocol = \"tcp\"\nlisten_port = 25565\nopen_to_internet = true\nupstreams = [{ address = \"10.0.0.1:25565\" }]\n"), code: CodeInvalidValue, absent: true},
		{name: "route tcp with allow cidrs", toml: t2doc("[services.r]\n\n[services.r.route]\nprotocol = \"tcp\"\nlisten_port = 25565\nallow_cidrs = [\"203.0.113.0/24\"]\nupstreams = [{ address = \"10.0.0.1:25565\" }]\n"), code: CodeInvalidValue, absent: true},
		{name: "route udp with allow cidrs", toml: t2doc("[services.r]\n\n[services.r.route]\nprotocol = \"udp\"\nlisten_port = 25565\nallow_cidrs = [\"203.0.113.0/24\"]\nupstreams = [{ address = \"10.0.0.1:25565\" }]\n"), code: CodeInvalidValue},
		{name: "route listen_port range", toml: t2doc("[services.r]\n\n[services.r.route]\nprotocol = \"tcp\"\nlisten_port = 80\nopen_to_internet = true\nupstreams = [{ address = \"10.0.0.1:25565\" }]\n"), code: CodeInvalidValue, path: "services.r.route.listen_port"},
		{name: "route http with listen_port", toml: t2doc("[services.r]\n\n[services.r.route]\nprotocol = \"http\"\nlisten_port = 8080\nupstreams = [{ address = \"10.0.0.1:80\" }]\n"), code: CodeInvalidValue, path: "services.r.route.listen_port"},
		{name: "route without upstreams", toml: t2doc("[services.r]\n\n[services.r.route]\nprotocol = \"http\"\n"), code: CodeMissingRequired, path: "services.r.route.upstreams"},
		{name: "route without protocol", toml: t2doc("[services.r]\n\n[services.r.route]\nupstreams = [{ address = \"10.0.0.1:80\" }]\n"), code: CodeMissingRequired, path: "services.r.route.protocol"},

		{name: "stack both compose and compose_file", toml: t2doc("[services.s]\n\n[services.s.stack]\ncompose = \"services:\\n  web:\\n    image: nginx:1\\n\"\ncompose_file = \"c.yml\"\n"), code: CodeInvalidValue, path: "services.s.stack.compose_file"},
		{name: "stack neither compose nor compose_file", toml: t2doc("[services.s]\n\n[services.s.stack]\nwait_seconds = 60\n"), code: CodeMissingRequired, path: "services.s.stack"},
		{name: "stack invalid yaml", toml: t2doc("[services.s]\n\n[services.s.stack]\ncompose = \"services:\\n  web:\\n    image: [unclosed\\n\"\n"), code: CodeInvalidValue, path: "services.s.stack.compose"},
		{name: "stack valid compose", toml: t2doc("[services.s]\n\n" + stackBody), code: CodeInvalidValue, absent: true},
		{name: "stack resources unknown component", toml: t2doc("[services.s]\n\n" + stackBody + "\n[services.s.stack.resources.ghost]\nmemory_mb = 256\n"), code: CodeInvalidValue, path: "services.s.stack.resources.ghost"},
		{name: "stack resources known component", toml: t2doc("[services.s]\n\n" + stackBody + "\n[services.s.stack.resources.web]\nmemory_mb = 256\n"), code: CodeInvalidValue, absent: true},
		{name: "stack domain unknown component", toml: t2doc("[services.s]\n\n" + stackBody + "\n[services.s.domains.\"s.example.com\"]\ncomponent = \"ghost\"\n"), code: CodeInvalidValue, path: "services.s.domains.\"s.example.com\".component"},
		{name: "stack domain without component", toml: t2doc("[services.s]\n\n" + stackBody + "\n[services.s.domains.\"s.example.com\"]\n"), code: CodeMissingRequired, path: "services.s.domains.\"s.example.com\".component"},
		{name: "stack domain with component", toml: t2doc("[services.s]\n\n" + stackBody + "\n[services.s.domains.\"s.example.com\"]\ncomponent = \"web\"\ntarget_port = 80\n"), code: CodeMissingRequired, absent: true},
	}
}

func t2SecretCases() []t2case {
	return []t2case{
		{name: "literal_secret positive", toml: t2doc(t2web + "[services.web.variables]\nDB_PASSWORD = \"hunter2\"\n"), code: CodeLiteralSecret, path: "services.web.variables.DB_PASSWORD.value", line: "DB_PASSWORD"},
		{name: "literal_secret lower-case name", toml: t2doc(t2web + "[services.web.variables]\napi_token = \"abcdef\"\n"), code: CodeLiteralSecret, path: "services.web.variables.api_token.value"},
		{name: "literal_secret table form", toml: t2doc(t2web + "[services.web.variables]\nAPI_KEY = { value = \"abcdef\", comment = \"x\" }\n"), code: CodeLiteralSecret, path: "services.web.variables.API_KEY.value"},
		{name: "literal_secret shared layer", toml: "schema = 1\n\n[variables]\nSHARED_TOKEN = \"abcdef\"\n", code: CodeLiteralSecret, path: "variables.SHARED_TOKEN.value"},
		{name: "literal_secret negative plain name", toml: t2doc(t2web + "[services.web.variables]\nAPP_NAME = \"hunter2\"\n"), code: CodeLiteralSecret, absent: true},
		{name: "literal_secret negative reference", toml: t2doc(t2web + "[services.web.variables]\nDB_PASSWORD = \"${{shared.DB_PASSWORD}}\"\n\n[variables]\nDB_PASSWORD = \"${{secret(32)}}\"\n"), code: CodeLiteralSecret, absent: true},
		{name: "literal_secret negative generator", toml: t2doc(t2web + "[services.web.variables]\nSESSION_SECRET = \"${{secret(32)}}\"\n"), code: CodeLiteralSecret, absent: true},
		{name: "literal_secret negative empty value", toml: t2doc(t2web + "[services.web.variables]\nDB_PASSWORD = \"\"\n"), code: CodeLiteralSecret, absent: true},
		{name: "literal_secret negative public", toml: t2doc(t2web + "[services.web.variables]\nTOKEN_URL = { value = \"https://auth.example.com/token\", public = true }\n"), code: CodeLiteralSecret, absent: true},
		{name: "literal_secret negative external reference", toml: t2doc(t2web + "[services.web.variables]\nDB_PASSWORD = \"${{vault:integration/path#key}}\"\n"), code: CodeLiteralSecret, absent: true},

		{name: "secret_in_file PEM in file content", toml: t2doc(t2web + "[services.web.files.\"/etc/key.pem\"]\ncontent = \"" + t2pemKey + "\"\n"), code: CodeSecretInFile, path: "services.web.files.\"/etc/key.pem\".content", line: "content ="},
		{name: "secret_in_file AWS key id", toml: t2doc(t2web + "[services.web.variables]\nAWS_KEY_ID = \"AKIAIOSFODNN7EXAMPLE\"\n"), code: CodeSecretInFile, path: "services.web.variables.AWS_KEY_ID"},
		{name: "secret_in_file ghp token", toml: t2doc(t2web + "[services.web.variables]\nGH = \"ghp_" + strings.Repeat("aB3", 12) + "\"\n"), code: CodeSecretInFile, path: "services.web.variables.GH"},
		{name: "secret_in_file compose text", toml: t2doc("[services.s]\n\n[services.s.stack]\ncompose = \"services:\\n  web:\\n    image: nginx:1\\n    environment:\\n      K: ghp_" + strings.Repeat("aB3", 12) + "\\n\"\n"), code: CodeSecretInFile, path: "services.s.stack.compose"},
		{name: "secret_in_file public does not silence", toml: t2doc(t2web + "[services.web.variables]\nGH = { value = \"ghp_" + strings.Repeat("aB3", 12) + "\", public = true }\n"), code: CodeSecretInFile, path: "services.web.variables.GH.value"},
		{name: "secret_in_file shared layer", toml: "schema = 1\n\n[variables]\nGH = \"ghp_" + strings.Repeat("aB3", 12) + "\"\n", code: CodeSecretInFile, path: "variables.GH"},
		{name: "secret_in_file negative url uuid digest", toml: t2doc(t2web + "[services.web.variables]\nURL = \"https://example.com/a/b?c=d\"\nID = \"123e4567-e89b-12d3-a456-426614174000\"\nDIGEST = \"sha256:" + strings.Repeat("a", 64) + "\"\n"), code: CodeSecretInFile, absent: true},
		{name: "secret_in_file negative reference", toml: t2doc(t2web + "[services.web.variables]\nTOKEN_VALUE = \"${{vault:integration/path#key}}\"\n"), code: CodeSecretInFile, absent: true},

		{name: "secret_has_value positive", toml: t2doc(t2web + "[services.web.variables]\nSTRIPE_KEY = \"abc\"\n\n[services.web.secrets]\nkeys = [\"STRIPE_KEY\"]\n"), code: CodeSecretHasValue, path: "services.web.variables.STRIPE_KEY"},
		{name: "secret_has_value negative names only", toml: t2doc(t2web + "[services.web.secrets]\nkeys = [\"STRIPE_KEY\"]\n"), code: CodeSecretHasValue, absent: true},
		{name: "secret_has_value negative reference value", toml: t2doc(t2web + "[services.web.variables]\nSTRIPE_KEY = \"${{vault:integration/path#key}}\"\n\n[services.web.secrets]\nkeys = [\"STRIPE_KEY\"]\n"), code: CodeSecretHasValue, absent: true},
		{name: "secret_has_value negative generator value", toml: t2doc(t2web + "[services.web.variables]\nSESSION = \"${{secret(32)}}\"\n\n[services.web.secrets]\nkeys = [\"SESSION\"]\n"), code: CodeSecretHasValue, absent: true},
		{name: "secret_has_value negative other name", toml: t2doc(t2web + "[services.web.variables]\nAPP_NAME = \"x\"\n\n[services.web.secrets]\nkeys = [\"STRIPE_KEY\"]\n"), code: CodeSecretHasValue, absent: true},
	}
}

func t2ReferenceCases() []t2case {
	return []t2case{
		{name: "unresolved_reference unknown service in variable", toml: t2doc(t2web + "[services.web.variables]\nDB = \"${{ghost.URL}}\"\n"), code: CodeUnresolvedReference, path: "services.web.variables.DB.value", severity: SeverityWarning, line: "DB ="},
		{name: "unresolved_reference unknown service in depends_on", toml: t2doc("[services.web]\nimage = \"nginx:1\"\ndepends_on = [\"ghost\"]\n"), code: CodeUnresolvedReference, path: "services.web.depends_on[0]", severity: SeverityWarning},
		{name: "unresolved_reference shared key absent", toml: t2doc(t2web + "[services.web.variables]\nA = \"${{shared.MISSING}}\"\n"), code: CodeUnresolvedReference, severity: SeverityWarning},
		{name: "unresolved_reference own key absent", toml: t2doc(t2web + "[services.web.variables]\nA = \"${{MISSING}}\"\n"), code: CodeUnresolvedReference, severity: SeverityWarning},
		{name: "unresolved_reference negative known service", toml: t2doc(t2web + "[services.web.variables]\nDB = \"${{db.URL}}\"\n\n[services.db]\nimage = \"postgres:17\"\n"), code: CodeUnresolvedReference, absent: true},
		{name: "unresolved_reference negative shared present", toml: t2doc(t2web + "[services.web.variables]\nA = \"${{shared.APP_NAME}}\"\n\n[variables]\nAPP_NAME = \"x\"\n"), code: CodeUnresolvedReference, absent: true},
		{name: "unresolved_reference negative shared secret name", toml: t2doc(t2web + "[services.web.variables]\nA = \"${{shared.SHARED_KEY}}\"\n\n[secrets]\nkeys = [\"SHARED_KEY\"]\n"), code: CodeUnresolvedReference, absent: true},
		{name: "unresolved_reference negative team", toml: t2doc(t2web + "[services.web.variables]\nA = \"${{team.ANYTHING}}\"\n"), code: CodeUnresolvedReference, absent: true},
		{name: "unresolved_reference negative vault", toml: t2doc(t2web + "[services.web.variables]\nA = \"${{vault:integration/path#key}}\"\n"), code: CodeUnresolvedReference, absent: true},
		{name: "unresolved_reference negative doppler", toml: t2doc(t2web + "[services.web.variables]\nA = \"${{doppler:integration#KEY}}\"\n"), code: CodeUnresolvedReference, absent: true},
		{name: "unresolved_reference negative own key", toml: t2doc(t2web + "[services.web.variables]\nA = \"1\"\nB = \"${{A}}\"\n"), code: CodeUnresolvedReference, absent: true},
		{name: "unresolved_reference service only in environment block", toml: t2doc(t2web + "[services.web.variables]\nA = \"${{api.URL}}\"\n\n[environments.staging.services.api]\nimage = \"nginx:1\"\n"), code: CodeUnresolvedReference, path: "services.web.variables.A.value"},

		{name: "reference_cycle self depends_on", toml: t2doc("[services.web]\nimage = \"nginx:1\"\ndepends_on = [\"web\"]\n"), code: CodeReferenceCycle, path: "services.web.depends_on[0]", severity: SeverityError},
		{name: "reference_cycle via depends_on", toml: t2doc("[services.a]\nimage = \"nginx:1\"\ndepends_on = [\"b\"]\n\n[services.b]\nimage = \"nginx:1\"\ndepends_on = [\"a\"]\n"), code: CodeReferenceCycle, severity: SeverityError},
		{name: "reference_cycle via variables across services", toml: t2doc("[services.a]\nimage = \"nginx:1\"\n\n[services.a.variables]\nX = \"${{b.Y}}\"\n\n[services.b]\nimage = \"nginx:1\"\n\n[services.b.variables]\nY = \"${{a.X}}\"\n"), code: CodeReferenceCycle, severity: SeverityError},
		{name: "reference_cycle mixed depends_on and variable", toml: t2doc("[services.a]\nimage = \"nginx:1\"\ndepends_on = [\"b\"]\n\n[services.b]\nimage = \"nginx:1\"\n\n[services.b.variables]\nY = \"${{a.X}}\"\n"), code: CodeReferenceCycle},
		{name: "reference_cycle between variables of one service", toml: t2doc(t2web + "[services.web.variables]\nA = \"${{B}}\"\nB = \"${{A}}\"\n"), code: CodeReferenceCycle, path: "services.web.variables.A.value"},
		{name: "reference_cycle negative chain", toml: t2doc("[services.a]\nimage = \"nginx:1\"\ndepends_on = [\"b\"]\n\n[services.b]\nimage = \"nginx:1\"\ndepends_on = [\"c\"]\n\n[services.c]\nimage = \"nginx:1\"\n"), code: CodeReferenceCycle, absent: true},
		{name: "reference_cycle negative diamond", toml: t2doc("[services.a]\nimage = \"nginx:1\"\ndepends_on = [\"b\", \"c\"]\n\n[services.b]\nimage = \"nginx:1\"\ndepends_on = [\"d\"]\n\n[services.c]\nimage = \"nginx:1\"\ndepends_on = [\"d\"]\n\n[services.d]\nimage = \"nginx:1\"\n"), code: CodeReferenceCycle, absent: true},
		{name: "reference_cycle negative variable chain", toml: t2doc(t2web + "[services.web.variables]\nA = \"${{B}}\"\nB = \"${{C}}\"\nC = \"x\"\n"), code: CodeReferenceCycle, absent: true},
		{name: "reference_cycle only in environment view", toml: t2doc("[services.a]\nimage = \"nginx:1\"\ndepends_on = [\"b\"]\n\n[services.b]\nimage = \"nginx:1\"\n\n[environments.staging.services.b]\ndepends_on = [\"a\"]\n"), code: CodeReferenceCycle, env: "staging"},

		{name: "reserved_variable PORT", toml: t2doc(t2web + "[services.web.variables]\nPORT = \"80\"\n"), code: CodeReservedVariable, path: "services.web.variables.PORT", line: "PORT ="},
		{name: "reserved_variable NEBULA_ prefix", toml: t2doc(t2web + "[services.web.variables]\nNEBULA_X = \"1\"\n"), code: CodeReservedVariable, path: "services.web.variables.NEBULA_X"},
		{name: "reserved_variable in secrets keys", toml: t2doc(t2web + "[services.web.secrets]\nkeys = [\"NEBULA_TOKEN\"]\n"), code: CodeReservedVariable, path: "services.web.secrets.keys[0]"},
		{name: "reserved_variable negative lookalikes", toml: t2doc(t2web + "[services.web.variables]\nPORTAL = \"1\"\nMY_NEBULA_X = \"1\"\nNEBULA = \"1\"\n"), code: CodeReservedVariable, absent: true},

		{name: "invalid_value variable name with hyphen", toml: t2doc(t2web + "[services.web.variables]\nMY-VAR = \"1\"\n"), code: CodeInvalidValue, path: "services.web.variables.MY-VAR"},
		{name: "invalid_value variable name starts with digit", toml: t2doc(t2web + "[services.web.variables]\n1ABC = \"1\"\n"), code: CodeInvalidValue, path: "services.web.variables.1ABC"},
		{name: "invalid_value variable name negative", toml: t2doc(t2web + "[services.web.variables]\n_ok1 = \"1\"\nAbc_9 = \"1\"\n"), code: CodeInvalidValue, absent: true},

		{name: "invalid_reference shared layer references a service", toml: "schema = 1\n\n[variables]\nA = \"${{db.URL}}\"\n", code: CodeInvalidReference, path: "variables.A.value", line: "A ="},
		{name: "invalid_reference negative shared layer shared ref", toml: "schema = 1\n\n[variables]\nA = \"x\"\nB = \"${{shared.A}}\"\n", code: CodeInvalidReference, absent: true},
		{name: "invalid_reference negative team ref in shared layer", toml: "schema = 1\n\n[variables]\nB = \"${{team.A}}\"\n", code: CodeInvalidReference, absent: true},

		{name: "invalid_template unterminated expression", toml: t2doc(t2web + "[services.web.variables]\nX = \"${{FOO\"\n"), code: CodeInvalidTemplate, path: "services.web.variables.X.value", line: "X ="},
		{name: "invalid_template unterminated call", toml: t2doc(t2web + "[services.web.variables]\nX = \"${{secret(32\"\n"), code: CodeInvalidTemplate, path: "services.web.variables.X.value"},
		{name: "invalid_template empty expression", toml: t2doc(t2web + "[services.web.variables]\nX = \"${{}}\"\n"), code: CodeInvalidTemplate, path: "services.web.variables.X.value"},
		{name: "invalid_template bad reference name", toml: t2doc(t2web + "[services.web.variables]\nX = \"${{Bad-NS.KEY}}\"\n"), code: CodeInvalidTemplate, path: "services.web.variables.X.value"},
		{name: "invalid_template unknown function", toml: t2doc(t2web + "[services.web.variables]\nX = \"${{nope(3)}}\"\n"), code: CodeInvalidTemplate, path: "services.web.variables.X.value"},
		{name: "invalid_template generator out of range", toml: t2doc(t2web + "[services.web.variables]\nX = \"${{secret(0)}}\"\n"), code: CodeInvalidTemplate, path: "services.web.variables.X.value"},
		{name: "invalid_template negative generators and escape", toml: t2doc(t2web + "[services.web.variables]\nA = \"${{secret(32)}}\"\nB = \"$${{literal}}\"\nC = \"${{uuid()}}\"\nD = \"${{urlencode(shared.X)}}\"\n\n[variables]\nX = \"1\"\n"), code: CodeInvalidTemplate, absent: true},

		{name: "invalid_value secret key in table form", toml: t2doc(t2web + "[services.web.variables]\nA = { value = \"${{secret(32)}}\", secret = false }\n"), code: CodeInvalidValue, path: "services.web.variables.A.secret", line: "A ="},
		{name: "generator variable with credential name is fine", toml: t2doc(t2web + "[services.web.variables]\nSESSION_SECRET = \"${{secret(32)}}\"\n"), code: CodeInvalidValue, absent: true},
		{name: "invalid_value build scope on non-git service", toml: t2doc(t2web + "[services.web.variables]\nA = { value = \"1\", scope = \"build\" }\n"), code: CodeInvalidValue, path: "services.web.variables.A.scope"},
		{name: "invalid_value both scope on non-git service", toml: t2doc(t2web + "[services.web.variables]\nA = { value = \"1\", scope = \"both\" }\n"), code: CodeInvalidValue, path: "services.web.variables.A.scope"},
		{name: "build scope on git service is fine", toml: t2doc("[services.web]\n\n[services.web.git]\nconnection = \"gh\"\nrepo = \"a/b\"\nbranch = \"main\"\n\n[services.web.variables]\nA = { value = \"1\", scope = \"build\" }\n"), code: CodeInvalidValue, absent: true},
		{name: "invalid_value unknown scope", toml: t2doc(t2web + "[services.web.variables]\nA = { value = \"1\", scope = \"never\" }\n"), code: CodeInvalidValue, path: "services.web.variables.A.scope"},
		{name: "invalid_value table form without value", toml: t2doc(t2web + "[services.web.variables]\nA = { comment = \"x\" }\n"), code: CodeMissingRequired, path: "services.web.variables.A"},
	}
}
