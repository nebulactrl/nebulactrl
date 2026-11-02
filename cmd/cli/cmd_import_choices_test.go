package main

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func choicesJSON(t *testing.T, f composeFlags) string {
	t.Helper()
	c, err := f.choices()
	require.NoError(t, err)
	raw, err := json.Marshal(c)
	require.NoError(t, err)
	return string(raw)
}

func TestComposeChoicesTranslateEveryPerServiceFlag(t *testing.T) {
	got := choicesJSON(t, composeFlags{
		include: []string{"worker"}, exclude: []string{"debug"}, managed: []string{"redis"}, noManaged: []string{"pg"},
		publish:     []string{"db=5432/tcp", "db=5353/udp"},
		git:         []string{"api=https://git.example.com/acme/api.git#release", "web=https://git.example.com/acme/web.git"},
		image:       []string{"cache=redis:7"},
		domain:      []string{"web=shop.example.com", "api=auto", "admin=off"},
		port:        []string{"web=8080"},
		convertBind: []string{"web=/data", "web=/uploads"},
		secret:      []string{"api.TOKEN"}, notSecret: []string{"api.LOG_LEVEL"}, skipVar: []string{"api.DEBUG"}, noRewrite: []string{"web.API_HOST"},
	})

	var want map[string]any
	require.NoError(t, json.Unmarshal([]byte(got), &want))
	svcs := want["services"].(map[string]any)
	require.Equal(t, true, svcs["worker"].(map[string]any)["include"])
	require.Equal(t, false, svcs["debug"].(map[string]any)["include"])
	require.Equal(t, true, svcs["redis"].(map[string]any)["managedDatabase"])
	require.Equal(t, false, svcs["pg"].(map[string]any)["managedDatabase"])
	require.Equal(t, []any{"5432/tcp", "5353/udp"}, svcs["db"].(map[string]any)["publishPorts"])

	api := svcs["api"].(map[string]any)
	require.Equal(t, "git", api["source"])
	require.Equal(t, "https://git.example.com/acme/api.git", api["gitUrl"])
	require.Equal(t, "release", api["gitBranch"], "#branch is split off the URL")
	require.Equal(t, map[string]any{"enabled": true}, api["domain"], "auto enables a generated domain")
	env := api["env"].(map[string]any)
	require.Equal(t, map[string]any{"secret": true}, env["TOKEN"])
	require.Equal(t, map[string]any{"secret": false}, env["LOG_LEVEL"])
	require.Equal(t, map[string]any{"skip": true}, env["DEBUG"])

	web := svcs["web"].(map[string]any)
	require.Equal(t, "git", web["source"])
	require.NotContains(t, web, "gitBranch", "no #branch, no branch")
	require.Equal(t, map[string]any{"enabled": true, "host": "shop.example.com"}, web["domain"])
	require.EqualValues(t, 8080, web["port"])
	require.Equal(t, []any{"/data", "/uploads"}, web["convertBinds"])
	require.Equal(t, map[string]any{"rewrite": false}, web["env"].(map[string]any)["API_HOST"])

	cache := svcs["cache"].(map[string]any)
	require.Equal(t, "image", cache["source"])
	require.Equal(t, "redis:7", cache["image"])
	require.Equal(t, map[string]any{"enabled": false}, svcs["admin"].(map[string]any)["domain"], "off disables the domain")
	require.NotContains(t, want, "variables", "per-service mode has no stack variables")
}

func TestComposeChoicesRejectMalformedValues(t *testing.T) {
	for name, tc := range map[string]struct {
		flags composeFlags
		want  string
	}{
		"publish without protocol": {composeFlags{publish: []string{"db=5432"}}, `--publish "db=5432": use <port>/<tcp|udp>`},
		"publish bad protocol":     {composeFlags{publish: []string{"db=5432/sctp"}}, "use <port>/<tcp|udp>"},
		"publish bad port":         {composeFlags{publish: []string{"db=x/tcp"}}, "use <port>/<tcp|udp>"},
		"no equals":                {composeFlags{domain: []string{"web"}}, `--domain "web": use <service>=<value>`},
		"empty value":              {composeFlags{git: []string{"web="}}, "use <service>=<value>"},
		"empty service":            {composeFlags{image: []string{"=nginx"}}, "use <service>=<value>"},
		"port not a number":        {composeFlags{port: []string{"web=http"}}, "not a port"},
		"port too big":             {composeFlags{port: []string{"web=70000"}}, "not a port"},
		"secret without key":       {composeFlags{secret: []string{"api"}}, `--secret "api": use <service>.<KEY>`},
		"skip-var empty service":   {composeFlags{skipVar: []string{".KEY"}}, "use <service>.<KEY>"},
		"no-rewrite empty key":     {composeFlags{noRewrite: []string{"api."}}, "use <service>.<KEY>"},
	} {
		_, err := tc.flags.choices()
		require.Error(t, err, name)
		var ue usageError
		require.ErrorAs(t, err, &ue, "%s is a usage error (exit 2)", name)
		require.Contains(t, err.Error(), tc.want, name)
	}
}

func TestComposeChoicesAsStackTakeVariableNamesAndRefuseServiceFlags(t *testing.T) {
	got := choicesJSON(t, composeFlags{
		asStack: true, secret: []string{"DB_PASSWORD"}, notSecret: []string{"LOG"}, skipVar: []string{"UNUSED"},
		domain: []string{"web=shop.example.com"}, port: []string{"web=8080"},
	})
	var want map[string]any
	require.NoError(t, json.Unmarshal([]byte(got), &want))
	require.Equal(t, map[string]any{
		"DB_PASSWORD": map[string]any{"secret": true}, "LOG": map[string]any{"secret": false}, "UNUSED": map[string]any{"skip": true},
	}, want["variables"], "a stack has one set of variables, named by <KEY>")
	web := want["services"].(map[string]any)["web"].(map[string]any)
	require.Equal(t, map[string]any{"enabled": true, "host": "shop.example.com"}, web["domain"], "domain and port take the component name")
	require.EqualValues(t, 8080, web["port"])

	for flag, f := range map[string]composeFlags{
		"include": {asStack: true, include: []string{"a"}}, "exclude": {asStack: true, exclude: []string{"a"}},
		"managed": {asStack: true, managed: []string{"a"}}, "no-managed": {asStack: true, noManaged: []string{"a"}},
		"publish": {asStack: true, publish: []string{"a=1/tcp"}}, "git": {asStack: true, git: []string{"a=https://x"}},
		"image": {asStack: true, image: []string{"a=x"}}, "convert-bind": {asStack: true, convertBind: []string{"a=/x"}},
		"no-rewrite": {asStack: true, noRewrite: []string{"A"}},
	} {
		_, err := f.choices()
		var ue usageError
		require.ErrorAs(t, err, &ue, flag)
		require.Contains(t, err.Error(), "--"+flag+" does not apply with --as-stack", flag)
	}
}
