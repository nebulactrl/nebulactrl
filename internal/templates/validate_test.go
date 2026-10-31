package templates

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const goodTemplate = `
schema: 1
id: demo
revision: 1
name: Demo
summary: A demo.
category: devtools
links: {website: "https://example.com"}
upstream: {license: MIT, class: permissive}
inputs:
  - {key: TITLE, label: Title, default: Hello}
services:
  - key: app
    name: App
    image: example/app:1.2.3
    digest: sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
    port: 8080
    healthcheck: {type: http, path: /}
    dependsOn: [db]
    env:
      TITLE: ${input:TITLE}
      DB: ${{db.DATABASE_URL}}
      TOKEN: ${{secret(32)}}
  - key: db
    name: DB
    kind: database
    database: {engine: postgres, version: "17"}
`

func parse(t *testing.T, y string) *CatalogTemplate {
	t.Helper()
	tpl, err := Parse([]byte(y))
	require.NoError(t, err)
	return tpl
}

func TestValidateAcceptsGoodTemplate(t *testing.T) {
	require.NoError(t, Validate(parse(t, goodTemplate)))
}

func TestParseRejectsUnknownFields(t *testing.T) {
	_, err := Parse([]byte(goodTemplate + "\nbogus: 1\n"))
	require.Error(t, err)
}

func TestValidateProblems(t *testing.T) {
	cases := map[string]struct{ from, to, want string }{
		"floating tag":       {"example/app:1.2.3", "example/app:latest", "floating"},
		"no tag":             {"example/app:1.2.3", "example/app", "explicit tag"},
		"bad digest":         {"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "sha256:abc", "digest"},
		"placeholder digest": {"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "sha256:0000000000000000000000000000000000000000000000000000000000000000", "placeholder"},
		"missing licence":    {"license: MIT", "license: \"\"", "upstream.license"},
		"unknown category":   {"category: devtools", "category: weird", "category"},
		"unknown reference":  {"${{db.DATABASE_URL}}", "${{nope.DATABASE_URL}}", "unknown service"},
		"undeclared input":   {"${input:TITLE}", "${input:NOPE}", "undeclared input"},
		"literal secret":     {"TOKEN: ${{secret(32)}}", "TOKEN: hunter2", "credential"},
		"unknown engine":     {"engine: postgres", "engine: oracle", "engine"},
		"reserved env":       {"TITLE: ${input:TITLE}", "NEBULA_X: ${input:TITLE}", "reserved"},
		"missing health":     {"    healthcheck: {type: http, path: /}\n", "", "healthcheck is required"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			y := strings.Replace(goodTemplate, tc.from, tc.to, 1)
			require.NotEqual(t, goodTemplate, y)
			tpl, err := Parse([]byte(y))
			require.NoError(t, err)
			err = Validate(tpl)
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.want)
		})
	}
}

func TestValidateCycleAndFiles(t *testing.T) {
	y := strings.Replace(goodTemplate, "    database: {engine: postgres, version: \"17\"}",
		"    database: {engine: postgres, version: \"17\"}\n    dependsOn: [app]", 1)
	err := Validate(parse(t, y))
	require.ErrorContains(t, err, "cycle")

	y = strings.Replace(goodTemplate, "    dependsOn: [db]\n", "    dependsOn: [db]\n    files:\n      - {path: ../etc/x, content: hi}\n", 1)
	require.ErrorContains(t, Validate(parse(t, y)), "clean absolute path")
}

func TestSourceAvailableNeedsLicenseURL(t *testing.T) {
	y := strings.Replace(goodTemplate, "class: permissive", "class: source-available", 1)
	require.ErrorContains(t, Validate(parse(t, y)), "licenseUrl")
}

func TestSplitImage(t *testing.T) {
	r, tag := SplitImage("registry:5000/a/b:1.0")
	require.Equal(t, "registry:5000/a/b", r)
	require.Equal(t, "1.0", tag)
	r, tag = SplitImage("registry:5000/a/b")
	require.Equal(t, "registry:5000/a/b", r)
	require.Empty(t, tag)
}
