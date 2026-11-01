package deployments

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/services"
)

const canary = "sekret-canary-12345"

func identity(s string) string { return s }

func redacted(t *testing.T, doc string, scrub func(string) string) map[string]any {
	t.Helper()
	out, ok := redactJSON(json.RawMessage(doc), scrub).(map[string]any)
	require.True(t, ok, "an object")
	return out
}

func TestRedactJSONMasksCredentialKeysAndKeepsNamesAndFlags(t *testing.T) {
	out := redacted(t, `{
		"serviceSlug": "web",
		"envKeys": ["API_KEY", "PORT"],
		"secretEnvKeys": ["API_KEY"],
		"buildSecretKeys": ["NPM_TOKEN"],
		"image": {"ref": "nginx:1", "hasRegistryAuth": true},
		"git": {"url": "https://git.example/app.git", "hasToken": true, "token": "`+canary+`"},
		"registryPassword": "`+canary+`",
		"tailnet": {"authKey": "`+canary+`", "hostname": "web"},
		"routes": [{"hosts": ["a.example"], "basicAuthHash": "$2y$10$abcdefghij"}],
		"replicas": 2
	}`, identity)

	require.Equal(t, "web", out["serviceSlug"])
	require.Equal(t, []any{"API_KEY", "PORT"}, out["envKeys"], "key names stay")
	require.Equal(t, []any{"API_KEY"}, out["secretEnvKeys"])
	require.Equal(t, []any{"NPM_TOKEN"}, out["buildSecretKeys"])
	require.Equal(t, true, out["image"].(map[string]any)["hasRegistryAuth"], "a flag stays")
	git := out["git"].(map[string]any)
	require.Equal(t, true, git["hasToken"])
	require.Equal(t, services.Mask, git["token"])
	require.Equal(t, services.Mask, out["registryPassword"])
	require.Equal(t, services.Mask, out["tailnet"].(map[string]any)["authKey"])
	require.Equal(t, "web", out["tailnet"].(map[string]any)["hostname"])
	route := out["routes"].([]any)[0].(map[string]any)
	require.Equal(t, services.Mask, route["basicAuthHash"])
	require.Equal(t, json.Number("2"), out["replicas"])

	raw, err := json.Marshal(out)
	require.NoError(t, err)
	require.NotContains(t, string(raw), canary)
	require.NotContains(t, string(raw), "$2y$10$")
}

func TestRedactJSONMasksEnvMapsHidesDocumentsAndScrubsStrings(t *testing.T) {
	scrub := func(s string) string { return strings.ReplaceAll(s, canary, services.Mask) }
	out := redacted(t, `{
		"env": {"A": "plain-value", "B": "`+canary+`", "C": ""},
		"files": [{"path": "/etc/app.conf", "content": "password=`+canary+`"}],
		"stack": {"compose": "services:\n  web:\n    environment:\n      K: `+canary+`"},
		"message": "failed while using `+canary+` as the password",
		"url": "x"
	}`, scrub)

	env := out["env"].(map[string]any)
	require.Equal(t, services.Mask, env["A"], "every env value is masked, secret or not")
	require.Equal(t, services.Mask, env["B"])
	require.Equal(t, "", env["C"])
	file := out["files"].([]any)[0].(map[string]any)
	require.Equal(t, "/etc/app.conf", file["path"])
	require.Regexp(t, `^\[\d+ bytes hidden\]$`, file["content"])
	require.Regexp(t, `^\[\d+ bytes hidden\]$`, out["stack"].(map[string]any)["compose"])
	require.Equal(t, "failed while using "+services.Mask+" as the password", out["message"])

	raw, err := json.Marshal(out)
	require.NoError(t, err)
	require.NotContains(t, string(raw), canary)
}

func TestRedactJSONIsNilForNothingOrGarbageAndBoundsLongStrings(t *testing.T) {
	require.Nil(t, redactJSON(nil, identity))
	require.Nil(t, redactJSON(json.RawMessage(`not json`), identity))

	out := redacted(t, `{"message": "`+strings.Repeat("é", 5000)+`"}`, identity)
	msg := out["message"].(string)
	require.LessOrEqual(t, len(msg), maxDebugString+len("…"))
	require.True(t, strings.HasSuffix(msg, "…"))
}

func TestMaskURLPasswords(t *testing.T) {
	require.Equal(t, "https://ci:"+services.Mask+"@git.example/app.git", maskURLPasswords("https://ci:"+canary+"@git.example/app.git"))
	require.Equal(t, "https://git.example/app.git", maskURLPasswords("https://git.example/app.git"))
	require.Equal(t, "git@github.com:org/app.git", maskURLPasswords("git@github.com:org/app.git"))
}

func TestReleaseVariablesListKeysOnlyWithFlags(t *testing.T) {
	env := &services.EnvSnapshot{
		Vars: map[string]string{
			"PORT": "80", "DATABASE_URL": "postgres://x:" + canary + "@db/app", "API_KEY": canary, "NEBULA_SERVICE_NAME": "web", "TOKEN": "t",
		},
		Secret: []string{"API_KEY", "DATABASE_URL"},
		Raw: map[string]services.RawVar{
			"DATABASE_URL": {Value: "postgres://x:${{db.PASSWORD}}@db/app", Secret: true},
			"API_KEY":      {Value: canary, Secret: true},
			"TOKEN":        {Value: "t", Scope: services.ScopeBuild},
		},
		Scope: map[string]string{"TOKEN": services.ScopeBuild},
		Ext:   map[string]services.ExtTpl{"API_KEY": {}},
	}
	got := releaseVariables(env)

	keys := make([]string, len(got))
	for i, v := range got {
		keys[i] = v.Key
	}
	require.Equal(t, []string{"API_KEY", "DATABASE_URL", "NEBULA_SERVICE_NAME", "PORT", "TOKEN"}, keys, "sorted")
	by := map[string]ReleaseVariableView{}
	for _, v := range got {
		by[v.Key] = v
	}
	require.True(t, by["API_KEY"].Secret)
	require.True(t, by["API_KEY"].External)
	require.True(t, by["DATABASE_URL"].Secret)
	require.True(t, by["DATABASE_URL"].Reference)
	require.True(t, by["PORT"].Platform)
	require.False(t, by["API_KEY"].Platform)
	require.Equal(t, services.ScopeBuild, by["TOKEN"].Scope)
	require.Equal(t, services.ScopeRuntime, by["PORT"].Scope)

	raw, err := json.Marshal(got)
	require.NoError(t, err)
	require.NotContains(t, string(raw), canary, "values never reach the view")
}

func TestParseLogStreams(t *testing.T) {
	def, err := ParseLogStreams("")
	require.NoError(t, err)
	require.Equal(t, []string{"build", "deploy", "runtime"}, def, "the debug stream is opt-in")

	all, err := ParseLogStreams("all")
	require.NoError(t, err)
	require.Equal(t, []string{"build", "deploy", "runtime", "debug"}, all)

	some, err := ParseLogStreams("debug, deploy,debug")
	require.NoError(t, err)
	require.Equal(t, []string{"debug", "deploy"}, some)

	_, err = ParseLogStreams("debug,secrets")
	require.Error(t, err)
}
