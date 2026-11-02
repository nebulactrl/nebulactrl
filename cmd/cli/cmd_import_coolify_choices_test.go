package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/apiclient"
)

const (
	cfApp   = "aaaa1111-0000-4000-8000-000000000001"
	cfDB    = "aaaa1111-0000-4000-8000-000000000002"
	cfApp2  = "aaaa1111-0000-4000-8000-000000000003"
	cfProj  = "pppp1111-0000-4000-8000-000000000001"
	cfProj2 = "pppp1111-0000-4000-8000-000000000002"
)

func cfPlan(t *testing.T) *apiclient.CoolifyPlan {
	t.Helper()
	var p apiclient.CoolifyPlan
	require.NoError(t, json.Unmarshal([]byte(`{
	  "projects": [
	    {"uuid": "`+cfProj+`", "name": "Shop", "environments": [
	      {"name": "production", "resources": [
	        {"uuid": "`+cfApp+`", "name": "web", "slug": "web"},
	        {"uuid": "`+cfDB+`", "name": "pg.main", "slug": "pg-main"}]}]},
	    {"uuid": "`+cfProj2+`", "name": "Blog", "environments": [
	      {"name": "staging", "resources": [
	        {"uuid": "`+cfApp2+`", "name": "web", "slug": "web-2"}]}]}
	  ],
	  "registries": [{"host": "ghcr.io", "username": "octocat", "provided": false}, {"host": "quay.io", "provided": false}]
	}`), &p))
	return &p
}

func cfCLI(t *testing.T, f *cfgFake) *cli {
	t.Helper()
	c := &cli{in: strings.NewReader(""), out: &bytes.Buffer{}, errOut: &bytes.Buffer{}, pollEvery: time.Millisecond}
	require.NoError(t, c.dial(config{URL: f.srv.URL, Token: "nct_test"}))
	return c
}

func cfChoices(t *testing.T, f *cfgFake, flags coolifyFlags) (map[string]any, error) {
	t.Helper()
	ch, err := flags.choices(context.Background(), cfCLI(t, f), cfPlan(t))
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(ch)
	require.NoError(t, err)
	var out map[string]any
	require.NoError(t, json.Unmarshal(raw, &out))
	return out, nil
}

func TestCoolifyChoicesResolveResourcesByNameSlugOrUUID(t *testing.T) {
	f := newCfgFake(t)
	t.Setenv("CF_TOKEN_VALUE", "s3cret")
	got, err := cfChoices(t, f, coolifyFlags{
		exclude: []string{"PG-MAIN"}, include: []string{cfApp}, rename: []string{"Shop/production/web=storefront"},
		engineVersion: []string{"pg.main=17"}, port: []string{"Shop/production/web=8080"}, kind: []string{"shop/production/web=worker"},
		domain: []string{cfApp + "/shop.example.com=store.example.com", cfApp + "/old.example.com=off"},
		secret: []string{"Shop/production/web.API_KEY"}, notSecret: []string{"Shop/production/web.LOG"}, skipVar: []string{"pg.main.PASSWORD_HINT"},
		noRewrite: []string{"Shop/production/web.DB_HOST"},
		set:       []string{"Shop/production/web.LOCKED=value=with=equals"}, setEnv: []string{"Shop/production/web.TOKEN=CF_TOKEN_VALUE"},
	})
	require.NoError(t, err)

	res := got["resources"].(map[string]any)
	web := res[cfApp].(map[string]any)
	require.Equal(t, true, web["include"], "--include marks the resource (named by UUID)")
	require.Equal(t, "storefront", web["name"])
	require.EqualValues(t, 8080, web["port"])
	require.Equal(t, "worker", web["kind"])
	require.Equal(t, map[string]any{
		"shop.example.com": map[string]any{"host": "store.example.com"},
		"old.example.com":  map[string]any{"enabled": false},
	}, web["domains"])
	require.Equal(t, map[string]any{
		"API_KEY": map[string]any{"secret": true},
		"LOG":     map[string]any{"secret": false},
		"DB_HOST": map[string]any{"rewrite": false},
		"LOCKED":  map[string]any{"value": "value=with=equals"},
		"TOKEN":   map[string]any{"value": "s3cret"},
	}, web["env"])

	pg := res[cfDB].(map[string]any)
	require.Equal(t, false, pg["include"], "a slug (case-insensitive) names the resource")
	require.Equal(t, "17", pg["engineVersion"])
	require.Equal(t, map[string]any{"PASSWORD_HINT": map[string]any{"skip": true}}, pg["env"],
		"a resource name with a dot is split at the last dot that leaves a known resource")
	require.NotContains(t, got, "projects")
	require.NotContains(t, got, "registries")
}

func TestCoolifyChoicesRejectWhatDoesNotResolve(t *testing.T) {
	f := newCfgFake(t)
	for name, tc := range map[string]struct {
		flags coolifyFlags
		want  string
	}{
		"unknown resource":   {coolifyFlags{exclude: []string{"ghost"}}, `no Coolify application or database "ghost" (found: web, pg.main, web)`},
		"ambiguous name":     {coolifyFlags{exclude: []string{"web"}}, `"web" is ambiguous: Shop/production/web (` + cfApp + `), Blog/staging/web (` + cfApp2 + `); use the UUID`},
		"rename without =":   {coolifyFlags{rename: []string{"pg-main"}}, `--rename "pg-main": use <name>=<value>`},
		"bad port":           {coolifyFlags{port: []string{"pg-main=http"}}, `--port "pg-main=http": not a port`},
		"port too large":     {coolifyFlags{port: []string{"pg-main=70000"}}, "not a port"},
		"bad kind":           {coolifyFlags{kind: []string{"pg-main=service"}}, "use app or worker"},
		"domain without /":   {coolifyFlags{domain: []string{"pg-main=x.example.com"}}, "use <resource>/<coolify host>=<new host>|off"},
		"domain without =":   {coolifyFlags{domain: []string{"pg-main/x.example.com"}}, `--domain "pg-main/x.example.com": use <name>=<value>`},
		"var without key":    {coolifyFlags{secret: []string{"pg-main"}}, `--secret "pg-main": use <resource>.<KEY>`},
		"var empty key":      {coolifyFlags{skipVar: []string{"pg-main."}}, "use <resource>.<KEY>"},
		"var unknown":        {coolifyFlags{notSecret: []string{"ghost.KEY"}}, `no Coolify application or database "ghost"`},
		"set without =":      {coolifyFlags{set: []string{"pg-main.KEY"}}, `--set "pg-main.KEY": use <name>=<value>`},
		"set-env missing":    {coolifyFlags{setEnv: []string{"pg-main.KEY=CF_UNSET_ENV_VAR"}}, `--set-env "pg-main.KEY=CF_UNSET_ENV_VAR": $CF_UNSET_ENV_VAR is not set`},
		"unknown project":    {coolifyFlags{skipProject: []string{"Nope"}}, `no Coolify project "Nope"`},
		"rename project bad": {coolifyFlags{renameProject: []string{"Shop"}}, `--rename-project "Shop": use <name>=<value>`},
		"into unknown":       {coolifyFlags{into: []string{"Shop=ghost"}}, `project "ghost" not found`},
		"registry user bad":  {coolifyFlags{registryUser: []string{"ghcr.io"}}, `--registry-user "ghcr.io": use <name>=<value>`},
		"token env bad":      {coolifyFlags{registryTokenEnv: []string{"ghcr.io"}}, `--registry-token-env "ghcr.io": use <name>=<value>`},
		"token env unset":    {coolifyFlags{registryTokenEnv: []string{"ghcr.io=CF_UNSET_ENV_VAR"}}, "$CF_UNSET_ENV_VAR is not set"},
		"git connection":     {coolifyFlags{gitConnection: "ghost"}, `git connection "ghost" not found`},
	} {
		f.reply("GET", cfgAPI+"/git/connections", 200, map[string]any{"items": []any{}})
		_, err := cfChoices(t, f, tc.flags)
		require.Error(t, err, name)
		require.Contains(t, err.Error(), tc.want, name)
	}
}

func TestCoolifyChoicesProjectsAndRegistries(t *testing.T) {
	f := newCfgFake(t)
	f.reply("GET", cfgAPI+"/git/connections", 200, map[string]any{"items": []any{map[string]any{"id": "gc-1", "name": "GitHub"}}})
	t.Setenv("CF_GHCR_TOKEN", "ghp_secret")
	t.Setenv("CF_QUAY_TOKEN", "quay_secret")

	got, err := cfChoices(t, f, coolifyFlags{
		skipProject: []string{"blog"}, renameProject: []string{cfProj + "=Shop (imported)"}, into: []string{"Shop=acme-shop"},
		registryUser:     []string{"QUAY.io=robot"},
		registryTokenEnv: []string{"ghcr.io=CF_GHCR_TOKEN", "quay.io=CF_QUAY_TOKEN"},
		gitConnection:    "github",
	})
	require.NoError(t, err)
	require.Equal(t, map[string]any{
		cfProj:  map[string]any{"name": "Shop (imported)", "existingProjectId": cfgProjectID},
		cfProj2: map[string]any{"skip": true},
	}, got["projects"])
	require.Equal(t, map[string]any{
		"ghcr.io": map[string]any{"username": "octocat", "password": "ghp_secret"},
		"quay.io": map[string]any{"username": "robot", "password": "quay_secret"},
	}, got["registries"], "the username comes from --registry-user, else from the plan's hint")
	require.Equal(t, "gc-1", got["gitConnectionId"])
	require.NotContains(t, got, "resources")
}

func TestCoolifyChoicesRegistryCredentialsNeedBothParts(t *testing.T) {
	f := newCfgFake(t)
	t.Setenv("CF_QUAY_TOKEN", "quay_secret")
	_, err := cfChoices(t, f, coolifyFlags{registryTokenEnv: []string{"quay.io=CF_QUAY_TOKEN"}})
	require.ErrorContains(t, err, "no username known for quay.io; add --registry-user quay.io=<username>")

	_, err = cfChoices(t, f, coolifyFlags{registryUser: []string{"ghcr.io=octocat"}})
	require.ErrorContains(t, err, "--registry-user ghcr.io needs --registry-token-env ghcr.io=<ENV_VAR>")

	t.Setenv("CF_EMPTY_TOKEN", "")
	_, err = cfChoices(t, f, coolifyFlags{registryTokenEnv: []string{"ghcr.io=CF_EMPTY_TOKEN"}})
	require.ErrorContains(t, err, "$CF_EMPTY_TOKEN is not set", "an empty token is as good as none")

	var ue usageError
	require.ErrorAs(t, err, &ue, "all of them are usage errors (exit 2)")
}

func TestCoolifyReadTokenPrefersTheMostExplicitSource(t *testing.T) {
	t.Setenv("MY_COOLIFY_TOKEN", " from-env \n")
	file := filepath.Join(t.TempDir(), "token")
	require.NoError(t, os.WriteFile(file, []byte("from-file\n"), 0o600))
	stdin := func(s string) *cli { return &cli{in: strings.NewReader(s)} }

	got, err := (&coolifyFlags{token: " from-flag ", tokenFile: file, tokenEnv: "MY_COOLIFY_TOKEN"}).readToken(stdin(""))
	require.NoError(t, err)
	require.Equal(t, "from-flag", got, "--token wins over everything")

	got, err = (&coolifyFlags{tokenFile: "-", tokenEnv: "MY_COOLIFY_TOKEN"}).readToken(stdin("from-stdin\n"))
	require.NoError(t, err)
	require.Equal(t, "from-stdin", got)

	got, err = (&coolifyFlags{tokenFile: file, tokenEnv: "MY_COOLIFY_TOKEN"}).readToken(stdin(""))
	require.NoError(t, err)
	require.Equal(t, "from-file", got)

	got, err = (&coolifyFlags{tokenEnv: "MY_COOLIFY_TOKEN"}).readToken(stdin(""))
	require.NoError(t, err)
	require.Equal(t, "from-env", got)

	_, err = (&coolifyFlags{tokenFile: filepath.Join(t.TempDir(), "missing"), tokenEnv: "MY_COOLIFY_TOKEN"}).readToken(stdin(""))
	require.ErrorContains(t, err, "read --token-file:")

	t.Setenv("MY_COOLIFY_TOKEN", "  ")
	_, err = (&coolifyFlags{tokenEnv: "MY_COOLIFY_TOKEN"}).readToken(stdin(""))
	var ue usageError
	require.ErrorAs(t, err, &ue)
	require.Contains(t, err.Error(), "a Coolify API token is required: set $MY_COOLIFY_TOKEN")
}
