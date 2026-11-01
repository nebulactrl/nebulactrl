package projectconfig

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// kind sources: scalars go before the sub-tables of the service.
var t2kinds = []struct{ name, scalars, tables string }{
	{"image", "image = \"nginx:1\"\n", ""},
	{"git", "", "[services.x.git]\nconnection = \"gh\"\nrepo = \"acme/x\"\nbranch = \"main\"\n\n"},
	{"database", "", "[services.x.database]\nengine = \"postgres\"\n\n"},
	{"route", "server = \"srv\"\n", "[services.x.route]\nprotocol = \"http\"\nupstreams = [{ address = \"10.0.0.1:80\" }]\n\n"},
	{"stack", "", "[services.x.stack]\ncompose = '''\nservices:\n  web:\n    image: nginx:1\n'''\n\n"},
}

// every key of the kind matrix with a minimal valid body.
var t2keys = []struct{ key, scalar, table string }{
	{"build", "", "[services.x.build]\ntype = \"auto\"\n\n"},
	{"port", "port = 80\n", ""},
	{"command", "command = [\"run\"]\n", ""},
	{"health_check", "", "[services.x.health_check]\ntype = \"none\"\n\n"},
	{"one_shot", "one_shot = true\n", ""},
	{"stop_grace_seconds", "stop_grace_seconds = 30\n", ""},
	{"memory_mb", "memory_mb = 256\n", ""},
	{"cpus", "cpus = 1.5\n", ""},
	{"image_retention", "image_retention = 5\n", ""},
	{"block_egress", "block_egress = true\n", ""},
	{"pre_deploy", "", "[services.x.pre_deploy]\ncommand = [\"migrate\"]\n\n"},
	{"post_deploy", "", "[services.x.post_deploy]\ncommand = [\"notify\"]\n\n"},
	{"files", "", "[services.x.files.\"/etc/a.conf\"]\ncontent = \"x\"\n\n"},
	{"volumes", "", "[services.x.volumes]\ndata = \"/data\"\n\n"},
	{"docker", "", "[services.x.docker]\ninit = true\n\n"},
	{"variables", "", "[services.x.variables]\nFOO = \"bar\"\n\n"},
	{"secrets", "", "[services.x.secrets]\nkeys = [\"TOKEN_X\"]\n\n"},
	{"domains", "", "[services.x.domains.\"a.example.com\"]\n\n"},
	{"scale", "", "[services.x.scale]\nreplicas = 2\n\n"},
	{"tasks", "", "[services.x.tasks.job]\nschedule = \"0 7 * * *\"\ncommand = [\"run\"]\n\n"},
	{"processes", "", "[services.x.processes.worker]\ncommand = [\"run\"]\n\n"},
	{"process_name", "process_name = \"app\"\n", ""},
	{"singleton", "singleton = true\n", ""},
	{"backups", "", "[services.x.backups.nightly]\ndestination = \"d\"\ncron = \"0 2 * * *\"\n\n"},
	{"uptime", "", "[services.x.uptime]\nenabled = true\n\n"},
	{"depends_on", "depends_on = [\"other\"]\n", ""},
	{"networks", "networks = [\"backplane\"]\n", ""},
}

const (
	mxNo      = ""          // allowed
	mxNotAllo = "forbidden" // not_allowed_for_kind
	mxDerive  = "derived"   // derived_field
	mxEither  = "either"    // the matrix names the key both ways
)

// t2matrix is the kind matrix written out per kind: every key not
// listed is allowed.
var t2matrix = map[string]map[string]string{
	"image": {"build": mxNotAllo, "backups": mxNotAllo},
	"git":   {"backups": mxNotAllo},
	"database": {
		"build": mxNotAllo, "port": mxDerive, "command": mxDerive, "health_check": mxDerive, "one_shot": mxDerive,
		"pre_deploy": mxNotAllo, "post_deploy": mxNotAllo, "files": mxNotAllo, "volumes": mxEither,
		"domains": mxNotAllo, "scale": mxNotAllo, "tasks": mxNotAllo, "processes": mxNotAllo, "process_name": mxNotAllo, "singleton": mxNotAllo,
	},
	"route": {
		"build": mxNotAllo, "port": mxNotAllo, "command": mxNotAllo, "health_check": mxNotAllo, "one_shot": mxNotAllo,
		"stop_grace_seconds": mxNotAllo, "memory_mb": mxNotAllo, "cpus": mxNotAllo, "image_retention": mxNotAllo,
		"block_egress": mxNotAllo, "pre_deploy": mxNotAllo, "post_deploy": mxNotAllo, "files": mxNotAllo,
		"volumes": mxNotAllo, "docker": mxNotAllo, "variables": mxNotAllo, "secrets": mxNotAllo, "scale": mxNotAllo,
		"tasks": mxNotAllo, "backups": mxNotAllo, "networks": mxNotAllo, "processes": mxNotAllo, "process_name": mxNotAllo, "singleton": mxNotAllo,
	},
	"stack": {
		"build": mxNotAllo, "port": mxNotAllo, "command": mxNotAllo, "health_check": mxNotAllo, "one_shot": mxNotAllo,
		"stop_grace_seconds": mxNotAllo, "memory_mb": mxNotAllo, "cpus": mxNotAllo, "image_retention": mxNotAllo,
		"block_egress": mxNotAllo, "pre_deploy": mxNotAllo, "post_deploy": mxNotAllo, "files": mxNotAllo,
		"volumes": mxNotAllo, "docker": mxNotAllo, "scale": mxNotAllo, "tasks": mxNotAllo, "backups": mxNotAllo,
		"processes": mxNotAllo, "process_name": mxNotAllo, "singleton": mxNotAllo,
	},
}

func TestKindMatrix(t *testing.T) {
	for _, k := range t2kinds {
		for _, key := range t2keys {
			want := t2matrix[k.name][key.key]
			t.Run(k.name+"/"+key.key+"/"+want, func(t *testing.T) {
				text := t2doc("[services.x]\n" + k.scalars + key.scalar + "\n" + k.tables + key.table)
				_, _, ds := Parse([]byte(text), nil)
				path := "services.x." + key.key
				notAllowed := t2find(ds, CodeNotAllowedForKind, path)
				derived := t2find(ds, CodeDerivedField, path)
				switch want {
				case mxNotAllo:
					require.NotNil(t, notAllowed, "want not_allowed_for_kind at %s:\n%s", path, ds.RenderText(""))
					require.Nil(t, derived, ds.RenderText(""))
					require.Contains(t, notAllowed.Message, k.name, "the message names the kind")
					require.Equal(t, SeverityError, notAllowed.Severity)
					require.Equal(t, t2lineOf(t, text, key.key), notAllowed.Line, "a forbidden key is reported on its own key")
				case mxDerive:
					require.NotNil(t, derived, "want derived_field at %s:\n%s", path, ds.RenderText(""))
					require.Nil(t, notAllowed, ds.RenderText(""))
					require.Contains(t, derived.Message, k.name)
					require.Equal(t, SeverityError, derived.Severity)
				case mxEither:
					require.True(t, notAllowed != nil || derived != nil, "want not_allowed_for_kind or derived_field at %s:\n%s", path, ds.RenderText(""))
				default:
					require.Nil(t, notAllowed, "%s is allowed for %s:\n%s", key.key, k.name, ds.RenderText(""))
					require.Nil(t, derived, "%s is allowed for %s:\n%s", key.key, k.name, ds.RenderText(""))
				}
			})
		}
	}
}

// A forbidden key in an environment override is reported there, tagged with
// the environment, and not in the base view.
func TestKindMatrixInOverride(t *testing.T) {
	text := t2doc("[services.db]\n\n[services.db.database]\nengine = \"postgres\"\n\n[environments.staging.services.db]\nport = 5432\n")
	_, _, ds := Parse([]byte(text), nil)
	d := t2find(ds, CodeDerivedField, "environments.staging.services.db.port")
	require.NotNil(t, d, ds.RenderText(""))
	require.Equal(t, "staging", d.Environment)
	require.Equal(t, t2lineOf(t, text, "port = 5432"), d.Line)
}

// No diagnostic, hint or rendered line contains the text of a credential.
func TestDiagnosticsNeverContainSecretText(t *testing.T) {
	ghp := "ghp_" + strings.Repeat("Zq9", 12)
	tests := []struct {
		name   string
		toml   string
		secret string
	}{
		{"literal secret", t2doc(t2web + "[services.web.variables]\nDB_PASSWORD = \"hunter2xyz\"\n"), "hunter2xyz"},
		{"literal secret in table form", t2doc(t2web + "[services.web.variables]\nAPI_KEY = { value = \"zzTopSecretValue\" }\n"), "zzTopSecretValue"},
		{"url userinfo", t2doc("[services.web]\n\n[services.web.git]\nurl = \"https://deploy:Sup3rT0ken@github.com/acme/web.git\"\nbranch = \"main\"\n"), "Sup3rT0ken"},
		{"image userinfo", t2doc("[services.web]\nimage = \"user:Sup3rT0ken@registry.example.com/app:1\"\n"), "Sup3rT0ken"},
		{"pem in file", t2doc(t2web + "[services.web.files.\"/etc/k.pem\"]\ncontent = \"" + t2pemKey + "\"\n"), "MIIBOgIBAAJBAKj34GkxFhD90vcNLYLInFEX6Ppy1tPf9Cnzj4p4WGeKLs1Pt8Qu"},
		{"aws key id", t2doc(t2web + "[services.web.variables]\nSOME_ID = \"AKIAIOSFODNN7EXAMPLE\"\n"), "AKIAIOSFODNN7EXAMPLE"},
		{"github token", t2doc(t2web + "[services.web.variables]\nGH = \"" + ghp + "\"\n"), ghp},
		{"secret with public flag", t2doc(t2web + "[services.web.variables]\nGH = { value = \"" + ghp + "\", public = true }\n"), ghp},
		{"secret in compose", t2doc("[services.s]\n\n[services.s.stack]\ncompose = \"services:\\n  web:\\n    image: nginx:1\\n    environment:\\n      K: " + ghp + "\\n\"\n"), ghp},
		{"secret declared with value", t2doc(t2web + "[services.web.variables]\nSTRIPE_KEY = \"valueNotToLeak123\"\n\n[services.web.secrets]\nkeys = [\"STRIPE_KEY\"]\n"), "valueNotToLeak123"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, _, ds := Parse([]byte(tc.toml), nil)
			require.True(t, ds.HasErrors(), "the case must be rejected")
			for _, d := range ds {
				for _, field := range []string{d.Message, d.Hint, d.Path, strings.Join(d.Suggestions, " "), d.Render("")} {
					require.NotContains(t, field, tc.secret, "diagnostic leaks the secret: %s", d.Render(""))
				}
			}
			require.NotContains(t, ds.RenderText(""), tc.secret)
		})
	}
}

func TestAttachments(t *testing.T) {
	compose := "services:\n  web:\n    image: nginx:1\n"
	stackDoc := t2doc("[services.s]\n\n[services.s.stack]\ncompose_file = \"docker-compose.yml\"\n")
	filesDoc := t2doc(t2web + "[services.web.files.\"/etc/a.conf\"]\nsource = \"conf/a.conf\"\nmode = \"0600\"\n")

	t.Run("compose_file is inlined", func(t *testing.T) {
		f, idx, ds := Parse([]byte(stackDoc), Attachments{"docker-compose.yml": []byte(compose)})
		require.False(t, ds.HasErrors(), ds.RenderText(""))
		r, rds := f.Resolve("", idx)
		require.Empty(t, rds)
		st := r.Services["s"].Def.Stack
		require.NotNil(t, st.Compose)
		require.Equal(t, compose, *st.Compose)
		require.Nil(t, st.ComposeFile, "the source is cleared once inlined")
		require.Equal(t, compose, r.Services["s"].Fields["config.stack.compose"].Value)
	})
	t.Run("compose_file is found by its cleaned path", func(t *testing.T) {
		_, _, ds := Parse([]byte(strings.Replace(stackDoc, "docker-compose.yml", "./docker-compose.yml", 1)), Attachments{"docker-compose.yml": []byte(compose)})
		require.False(t, ds.HasErrors(), ds.RenderText(""))
	})
	t.Run("file source is inlined with its default mode applied", func(t *testing.T) {
		f, idx, ds := Parse([]byte(filesDoc), Attachments{"conf/a.conf": []byte("listen = 80\n")})
		require.False(t, ds.HasErrors(), ds.RenderText(""))
		r, _ := f.Resolve("", idx)
		require.Equal(t, []FileMount{{Path: "/etc/a.conf", Mode: "0600", Content: "listen = 80\n"}}, r.Services["web"].Fields["config.files"].Value)
		require.Nil(t, r.Services["web"].Def.Files["/etc/a.conf"].Source)
	})
	t.Run("attachment paths are listed", func(t *testing.T) {
		f, _, _ := Parse([]byte(filesDoc+"\n[services.s]\n\n[services.s.stack]\ncompose_file = \"docker-compose.yml\"\n"), nil)
		require.ElementsMatch(t, []string{"conf/a.conf", "docker-compose.yml"}, f.AttachmentPaths())
	})
	t.Run("secret in an attachment", func(t *testing.T) {
		bad := compose + "    environment:\n      K: ghp_" + strings.Repeat("Zq9", 12) + "\n"
		_, _, ds := Parse([]byte(stackDoc), Attachments{"docker-compose.yml": []byte(bad)})
		d := t2find(ds, CodeSecretInFile, "services.s.stack.compose_file")
		require.NotNil(t, d, ds.RenderText(""))
		require.NotContains(t, d.Message, "Zq9Zq9")
	})
	t.Run("secret in a file source", func(t *testing.T) {
		_, _, ds := Parse([]byte(filesDoc), Attachments{"conf/a.conf": []byte("-----BEGIN PRIVATE KEY-----\nabc\n")})
		require.NotNil(t, t2find(ds, CodeSecretInFile, "services.web.files.\"/etc/a.conf\".source"), ds.RenderText(""))
	})
	t.Run("attachment that is not UTF-8", func(t *testing.T) {
		_, _, ds := Parse([]byte(filesDoc), Attachments{"conf/a.conf": {0xff, 0xfe, 0x00}})
		require.NotNil(t, t2find(ds, CodeInvalidEncoding, "services.web.files.\"/etc/a.conf\".source"), ds.RenderText(""))
	})
	t.Run("too many attachments", func(t *testing.T) {
		att := Attachments{}
		for i := range MaxAttachments + 1 {
			att[fmt.Sprintf("f%d.txt", i)] = []byte("x")
		}
		_, _, ds := Parse([]byte(t2doc(t2web)), att)
		require.NotNil(t, t2find(ds, CodeLimitExceeded, ""), ds.RenderText(""))
	})
	t.Run("eight attachments are fine", func(t *testing.T) {
		att := Attachments{}
		for i := range MaxAttachments {
			att[fmt.Sprintf("f%d.txt", i)] = []byte("x")
		}
		_, _, ds := Parse([]byte(t2doc(t2web)), att)
		require.Nil(t, t2find(ds, CodeLimitExceeded, ""), ds.RenderText(""))
	})
	t.Run("attachment over 256 KiB", func(t *testing.T) {
		_, _, ds := Parse([]byte(t2doc(t2web)), Attachments{"big.txt": make([]byte, MaxAttachmentBytes+1)})
		require.NotNil(t, t2find(ds, CodeLimitExceeded, ""), ds.RenderText(""))
	})
	t.Run("attachments over 1 MiB in total", func(t *testing.T) {
		att := Attachments{}
		for i := range 5 {
			att[fmt.Sprintf("f%d.txt", i)] = make([]byte, MaxAttachmentBytes)
		}
		_, _, ds := Parse([]byte(t2doc(t2web)), att)
		require.NotNil(t, t2find(ds, CodeLimitExceeded, ""), ds.RenderText(""))
	})
	t.Run("files over 256 KiB in total", func(t *testing.T) {
		var b strings.Builder
		b.WriteString(t2doc(t2web))
		att := Attachments{}
		for i := range 5 {
			fmt.Fprintf(&b, "[services.web.files.\"/etc/f%d\"]\nsource = \"s%d\"\n\n", i, i)
			att[fmt.Sprintf("s%d", i)] = []byte(strings.Repeat("a", 60<<10))
		}
		_, _, ds := Parse([]byte(b.String()), att)
		require.NotNil(t, t2find(ds, CodeLimitExceeded, "services.web.files"), ds.RenderText(""))
	})
	t.Run("a missing attachment is only an error where it is used", func(t *testing.T) {
		_, _, ds := Parse([]byte(t2doc(t2web)), nil)
		require.False(t, ds.HasErrors(), ds.RenderText(""))
	})
}

// Positions come from the position index: key position for key problems,
// value position for value problems.
func TestDiagnosticPositions(t *testing.T) {
	text := t2doc("[services.web]\nimage = \"nginx:1\"\nport = \"80\"\nbogus = 1\n\n[services.web.git]\ntoken = \"x\"\n")
	_, _, ds := Parse([]byte(text), nil)

	d := t2find(ds, CodeWrongType, "services.web.port")
	require.NotNil(t, d, ds.RenderText(""))
	require.Equal(t, t2lineOf(t, text, "port = "), d.Line)
	require.GreaterOrEqual(t, d.Column, len("port = ")+1, "a wrong value is reported at the value, not at the key")
	require.Greater(t, d.EndColumn, d.Column)

	d = t2find(ds, CodeUnknownKey, "services.web.bogus")
	require.NotNil(t, d, ds.RenderText(""))
	require.Equal(t, t2lineOf(t, text, "bogus"), d.Line)
	require.Equal(t, 1, d.Column, "an unknown key is reported at the key")

	d = t2find(ds, CodeCredentialKey, "services.web.git.token")
	require.NotNil(t, d, ds.RenderText(""))
	require.Equal(t, t2lineOf(t, text, "token"), d.Line)
	require.Equal(t, 1, d.Column)
	require.Empty(t, d.Suggestions, "a credential key has no did-you-mean")

	require.Contains(t, ds.RenderText("nebula.toml"), fmt.Sprintf("nebula.toml:%d:1: error[unknown_key] services.web.bogus:", t2lineOf(t, text, "bogus")))
}

// A problem in a merged value points at the line that supplied the winning value.
func TestDiagnosticPositionsInOverride(t *testing.T) {
	text := t2doc(t2web + "[services.web.health_check]\npath = \"/ok\"\n\n[environments.staging.services.web.health_check]\npath = \"nope\"\n")
	_, _, ds := Parse([]byte(text), nil)
	d := t2find(ds, CodeInvalidValue, "environments.staging.services.web.health_check.path")
	require.NotNil(t, d, ds.RenderText(""))
	require.Equal(t, "staging", d.Environment)
	require.Equal(t, t2lineOf(t, text, "path = \"nope\""), d.Line)
	require.Nil(t, t2find(ds, CodeInvalidValue, "services.web.health_check.path"), "the base value is fine")
}

func TestDidYouMeanForKeys(t *testing.T) {
	_, _, ds := Parse([]byte(t2doc("[services.web]\nimage = \"nginx:1\"\nhealthcheck = { path = \"/\" }\n")), nil)
	d := t2find(ds, CodeUnknownKey, "services.web.healthcheck")
	require.NotNil(t, d, ds.RenderText(""))
	require.Contains(t, d.Suggestions, "health_check")
}

func TestSecretsAndKeysAcceptReferences(t *testing.T) {
	doc := t2doc(t2web + `[services.web.variables]
API_URL   = "${{db.URL}}"
VAULTED   = "${{vault:integration/path#key}}"
DOPPLERED = "${{doppler:integration#KEY}}"
TEAMED    = "${{team.SHARED}}"
SHARED_IN = "${{shared.APP_NAME}}"
ENCODED   = "${{urlencode(db.PASSWORD)}}"
ESCAPED   = "$${{not a reference}}"

[services.db]
image = "postgres:17"

[variables]
APP_NAME = "Acme"
`)
	_, _, ds := Parse([]byte(doc), nil)
	require.False(t, ds.HasErrors(), ds.RenderText(""))
	for _, d := range ds {
		require.Equal(t, SeverityWarning, d.Severity, d.Render(""))
	}
}
