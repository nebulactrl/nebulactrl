package projectconfig

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOnlyInFiltersServices(t *testing.T) {
	text := t2doc(`[services.web]
image = "nginx:1"
port = 80

[services.printer]
image = "nginx:1"
only_in = ["production"]

[services.both]
image = "nginx:1"
only_in = ["staging", "production"]

[environments.staging.services.printer]
name = "Printer override"
`)
	f, idx, ds := t2parse(t, text)
	require.False(t, ds.HasErrors(), ds.RenderText(""))

	view := func(env string) []string {
		r, rds := f.Resolve(env, idx)
		require.Empty(t, rds)
		return r.SortedSlugs()
	}
	require.Equal(t, []string{"both", "printer", "web"}, view(""), "no environment keeps every service")
	require.Equal(t, []string{"both", "printer", "web"}, view("production"))
	require.Equal(t, []string{"both", "web"}, view("staging"), "only_in filters before the override is merged")
	require.Equal(t, []string{"web"}, view("preview"), "an environment not listed in only_in does not see the service")
	require.Equal(t, []string{"web"}, view("no-such-block"))
}

func TestServicesOnlyInEnvironmentBlocks(t *testing.T) {
	text := t2doc(`[services.web]
image = "nginx:1"
port = 80

[environments.staging.services.api]
image = "nginx:2"
port = 8080

[environments.staging.services.api.variables]
MODE = "staging"
`)
	f, idx, ds := t2parse(t, text)
	require.False(t, ds.HasErrors(), ds.RenderText(""))

	base, _ := f.Resolve("", idx)
	require.Equal(t, []string{"web"}, base.SortedSlugs())

	staging, rds := f.Resolve("staging", idx)
	require.Empty(t, rds)
	require.Equal(t, []string{"api", "web"}, staging.SortedSlugs())
	api := staging.Services["api"]
	require.Equal(t, KindImage, api.Kind)
	require.Equal(t, "environments.staging.services.api", api.Path)
	require.Equal(t, "environments.staging.services.api.image", t2field(t, staging, "api", "config.image").Path)
	require.Equal(t, Variable{Value: "staging"}, t2field(t, staging, "api", "variables.MODE").Value)
	require.Equal(t, t2lineOf(t, text, "image = \"nginx:2\""), t2field(t, staging, "api", "config.image").Pos.Line)

	other, _ := f.Resolve("production", idx)
	require.Equal(t, []string{"web"}, other.SortedSlugs())
}

func TestEnvironmentOnlyServiceWithoutSource(t *testing.T) {
	text := t2doc(t2web + "[environments.staging.services.api]\nname = \"API\"\n")
	f, idx, ds := t2parse(t, text)
	d := t2find(ds, CodeMissingSource, "environments.staging.services.api")
	require.NotNil(t, d, ds.RenderText(""))
	require.Equal(t, "staging", d.Environment, "the problem exists only in the staging view")
	require.Equal(t, t2lineOf(t, text, "[environments.staging.services.api]"), d.Line)
	require.Equal(t, 1, ds.Count(SeverityError), ds.RenderText(""))

	r, rds := f.Resolve("staging", idx)
	require.Empty(t, rds, "Resolve returns only what Parse did not report")
	require.NotNil(t, r.Services["api"], "the service is listed even when it has errors")
	require.Equal(t, "", r.Services["api"].Kind, "no source key, no kind")
	base, _ := f.Resolve("", idx)
	require.Nil(t, base.Services["api"])
}

func TestServerPrecedence(t *testing.T) {
	text := `schema = 1
server = "root-srv"

[services.a]
image = "nginx:1"

[services.b]
image = "nginx:1"
server = "b-srv"

[services.c]
image = "nginx:1"

[environments.staging]
server = "stg-srv"

[environments.staging.services.c]
server = "c-stg"

[environments.production.services.a]
name = "A"
`
	f, idx, ds := t2parse(t, text)
	require.False(t, ds.HasErrors(), ds.RenderText(""))
	rootLine := t2lineOf(t, text, "server = \"root-srv\"")
	stagingLine := t2lineOf(t, text, "server = \"stg-srv\"")
	bLine := t2lineOf(t, text, "server = \"b-srv\"")
	cLine := t2lineOf(t, text, "server = \"c-stg\"")

	tests := []struct {
		env, slug, server, path string
		line                    int
	}{
		{"", "a", "root-srv", "server", rootLine},
		{"", "b", "b-srv", "services.b.server", bLine},
		{"", "c", "root-srv", "server", rootLine},
		{"staging", "a", "stg-srv", "environments.staging.server", stagingLine},
		{"staging", "b", "b-srv", "services.b.server", bLine},
		{"staging", "c", "c-stg", "environments.staging.services.c.server", cLine},
		{"production", "a", "root-srv", "server", rootLine},
		{"production", "c", "root-srv", "server", rootLine},
		{"unknown", "a", "root-srv", "server", rootLine},
	}
	for _, tc := range tests {
		t.Run(tc.env+"/"+tc.slug, func(t *testing.T) {
			r, rds := f.Resolve(tc.env, idx)
			require.Empty(t, rds)
			rs := r.Services[tc.slug]
			require.Equal(t, tc.server, rs.ServerName)
			fv := t2field(t, r, tc.slug, "server")
			require.Equal(t, tc.server, fv.Value)
			require.Equal(t, tc.path, fv.Path)
			require.Equal(t, tc.line, fv.Pos.Line, "the server is positioned at the line that set it")
			require.Equal(t, AppliesStaged, fv.Applies)
		})
	}

	staging, _ := f.Resolve("staging", idx)
	require.Equal(t, "stg-srv", staging.Server, "the environment server is the default of the view")
	base, _ := f.Resolve("", idx)
	require.Equal(t, "root-srv", base.Server)
}

func TestNoServerNoServerField(t *testing.T) {
	r := t2resolve(t, "schema = 1\n\n[services.a]\nimage = \"nginx:1\"\n", "")
	require.Equal(t, "", r.Services["a"].ServerName)
	t2noField(t, r, "a", "server")
}

func TestProvenanceAfterMerge(t *testing.T) {
	text := t2doc(`[services.web]
image = "nginx:1"
port = 3000
depends_on = ["db"]

[services.web.health_check]
type = "http"
path = "/healthz"

[services.web.variables]
NODE_ENV = "production"
SAME = "same"

[services.web.secrets]
keys = ["STRIPE_KEY"]

[services.web.domains."shop.acme.com"]

[services.db]
image = "postgres:17"

[services.other]
image = "nginx:1"

[environments.staging.services.web]
port = 4000
depends_on = ["other"]

[environments.staging.services.web.health_check]
path = "/ready"

[environments.staging.services.web.variables]
NODE_ENV = "staging"
`)
	f, idx, ds := t2parse(t, text)
	require.False(t, ds.HasErrors(), ds.RenderText(""))
	staging, _ := f.Resolve("staging", idx)
	base, _ := f.Resolve("", idx)

	tests := []struct {
		name   string
		r      *Resolved
		id     string
		path   string
		marker string
	}{
		{"override scalar wins", staging, "config.port", "environments.staging.services.web.port", "port = 4000"},
		{"base scalar in base view", base, "config.port", "services.web.port", "port = 3000"},
		{"override nested key wins", staging, "config.healthcheck.path", "environments.staging.services.web.health_check.path", "path = \"/ready\""},
		{"base nested key survives next to an override", staging, "config.healthcheck.type", "services.web.health_check.type", "type = \"http\""},
		{"override variable wins", staging, "variables.NODE_ENV", "environments.staging.services.web.variables.NODE_ENV", "NODE_ENV = \"staging\""},
		{"base variable survives", staging, "variables.SAME", "services.web.variables.SAME", "SAME ="},
		{"array element of the override", staging, "links.other", "environments.staging.services.web.depends_on[0]", "depends_on = [\"other\"]"},
		{"array element of the base", base, "links.db", "services.web.depends_on[0]", "depends_on = [\"db\"]"},
		{"declared secret", staging, "variables.STRIPE_KEY#secret", "services.web.secrets.keys[0]", "keys = [\"STRIPE_KEY\"]"},
		{"domain", staging, "domains.shop.acme.com", "services.web.domains.\"shop.acme.com\"", "[services.web.domains.\"shop.acme.com\"]"},
		{"image", staging, "config.image", "services.web.image", "image = \"nginx:1\""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fv := t2field(t, tc.r, "web", tc.id)
			require.Equal(t, tc.path, fv.Path)
			want := t2lineOf(t, text, tc.marker)
			require.Equal(t, want, fv.Pos.Line, "FieldValue.Pos")
			require.Equal(t, want, tc.r.Pos(fv.Path).Line, "Resolved.Pos(path)")
		})
	}

	// a path the file does not contain is not positioned
	require.True(t, staging.Pos("nothing.here").IsZero())
	require.True(t, (*Resolved)(nil).Pos("services.web").IsZero())
}

func TestFieldsAppliesAndPathsAreFilled(t *testing.T) {
	r := t2resolve(t, t2doc(t2web+"[services.web.scale]\nreplicas = 2\n\n[services.web.variables]\nA = \"1\"\n"), "")
	for _, id := range r.Services["web"].FieldIDs() {
		fv := r.Services["web"].Fields[id]
		require.Equal(t, id, fv.ID)
		require.NotEmpty(t, fv.Path, id)
		require.NotZero(t, fv.Pos.Line, id)
		require.Contains(t, []string{AppliesStaged, AppliesImmediate}, fv.Applies, id)
		spec, ok := LookupField(id)
		require.True(t, ok, id)
		require.Equal(t, spec.Applies, fv.Applies, id)
	}
}

func TestSharedLayersAreNotMerged(t *testing.T) {
	text := t2doc(`[variables]
APP_NAME = "Acme"
PROJECT_ONLY = "p"

[secrets]
keys = ["SHARED_KEY"]

[services.web]
image = "nginx:1"
port = 80

[services.web.variables]
NAME = "${{shared.APP_NAME}}"

[environments.staging.variables]
APP_NAME = "Acme (staging)"
ENV_ONLY = "e"

[environments.staging.secrets]
keys = ["ENV_KEY"]
scope = "both"
`)
	f, idx, ds := t2parse(t, text)
	require.False(t, ds.HasErrors(), ds.RenderText(""))

	staging, rds := f.Resolve("staging", idx)
	require.Empty(t, rds)
	require.Equal(t, map[string]Variable{"APP_NAME": {Value: "Acme"}, "PROJECT_ONLY": {Value: "p"}}, staging.Shared, "the project layer is as written")
	require.Equal(t, map[string]Variable{"APP_NAME": {Value: "Acme (staging)"}, "ENV_ONLY": {Value: "e"}}, staging.EnvShared)
	require.Equal(t, []string{"SHARED_KEY"}, staging.SharedSecrets)
	require.Equal(t, []string{"ENV_KEY"}, staging.EnvSharedSecrets)
	require.Equal(t, "runtime", staging.SharedSecretScope)
	require.Equal(t, "both", staging.EnvSharedSecretScope)

	base, _ := f.Resolve("", idx)
	require.Equal(t, staging.Shared, base.Shared)
	require.Empty(t, base.EnvShared, "the base view has no environment layer")
	require.Empty(t, base.EnvSharedSecrets)
	require.Equal(t, "runtime", base.EnvSharedSecretScope)

	other, _ := f.Resolve("production", idx)
	require.Empty(t, other.EnvShared)
	require.Equal(t, staging.Shared, other.Shared)
}

func TestResolveUnknownEnvironmentEqualsBase(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "valid", "docs-full.toml"))
	require.NoError(t, err)
	f, idx, ds := t2parse(t, string(data))
	require.False(t, ds.HasErrors(), ds.RenderText(""))

	base, bds := f.Resolve("", idx)
	unknown, uds := f.Resolve("no-such-environment", idx)
	require.Empty(t, bds)
	require.Empty(t, uds)
	require.Equal(t, "no-such-environment", unknown.Environment)
	require.Equal(t, base.Shared, unknown.Shared)
	require.Equal(t, base.Server, unknown.Server)
	// printer carries only_in = ["production"]: an environment that is not listed does not see it.
	require.Equal(t, []string{"db", "web", "worker"}, unknown.SortedSlugs())
	require.Equal(t, []string{"db", "printer", "web", "worker"}, base.SortedSlugs())
	for _, slug := range unknown.SortedSlugs() {
		require.Equal(t, base.Services[slug].Fields, unknown.Services[slug].Fields, slug)
	}
}

func TestResolveUnknownEnvironmentWithoutOnlyIn(t *testing.T) {
	text := t2doc(t2web + "[services.web.variables]\nA = \"1\"\n\n[environments.staging.services.web]\nport = 81\n\n[environments.staging.variables]\nE = \"1\"\n")
	f, idx, _ := t2parse(t, text)
	base, _ := f.Resolve("", idx)
	unknown, uds := f.Resolve("no-such-environment", idx)
	require.Empty(t, uds)
	require.Equal(t, base.SortedSlugs(), unknown.SortedSlugs())
	for slug, rs := range base.Services {
		require.Equal(t, rs.Fields, unknown.Services[slug].Fields, slug)
		require.Equal(t, rs.Def, unknown.Services[slug].Def, slug)
	}
	require.Equal(t, base.Shared, unknown.Shared)
	require.Equal(t, base.EnvShared, unknown.EnvShared)
}

// Resolve returns only the diagnostics Parse did not report: for every
// environment, and whether the document has problems or not.
func TestResolveRepeatsNothingParseReported(t *testing.T) {
	problems := t2doc(`[services.web]
image = "nginx:1"
port = 80
depends_on = ["ghost"]

[services.web.variables]
A = "${{missing.KEY}}"
DB_PASSWORD = "hunter2"

[environments.staging.services.web]
port = 70000
unset = ["nope", "scale"]

[environments.staging.services.api]
name = "no source"

[environments.production.services.web.variables]
PORT = "81"
`)
	docs := map[string]string{"problems": problems}
	for _, name := range []string{"docs-full", "docs-stack", "docs-single-image"} {
		data, err := os.ReadFile(filepath.Join("testdata", "valid", name+".toml"))
		require.NoError(t, err)
		docs[name] = string(data)
	}
	for name, text := range docs {
		t.Run(name, func(t *testing.T) {
			f, idx, parsed := t2parse(t, text)
			if name == "problems" {
				require.True(t, parsed.HasErrors())
				require.NotNil(t, t2find(parsed, CodeInvalidValue, "environments.staging.services.web.port"), parsed.RenderText(""))
				require.NotNil(t, t2find(parsed, CodeMissingSource, "environments.staging.services.api"), parsed.RenderText(""))
				require.NotNil(t, t2find(parsed, CodeReservedVariable, "environments.production.services.web.variables.PORT"), parsed.RenderText(""))
				require.NotNil(t, t2find(parsed, CodeLiteralSecret, ""))
				require.NotNil(t, t2find(parsed, CodeUnresolvedReference, ""))
			}
			envs := append([]string{"", "unknown"}, f.Environments()...)
			for _, env := range envs {
				r, rds := f.Resolve(env, idx)
				require.NotNil(t, r)
				require.Empty(t, rds, "environment %q:\n%s", env, rds.RenderText(""))
			}
		})
	}
}

func TestResolveIsDeterministicAndDoesNotMutateTheFile(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "valid", "docs-full.toml"))
	require.NoError(t, err)
	f, idx, _ := Parse(data, nil)
	require.NotNil(t, f)
	pristine, _, _ := Parse(data, nil)

	for _, env := range []string{"", "staging", "production", "unknown"} {
		t.Run("env "+env, func(t *testing.T) {
			first, d1 := f.Resolve(env, idx)
			second, d2 := f.Resolve(env, idx)
			require.Equal(t, d1, d2)
			require.Equal(t, first.Environment, second.Environment)
			require.Equal(t, first.Server, second.Server)
			require.Equal(t, first.Shared, second.Shared)
			require.Equal(t, first.EnvShared, second.EnvShared)
			require.Equal(t, first.SortedSlugs(), second.SortedSlugs())
			for slug, rs := range first.Services {
				other := second.Services[slug]
				require.Equal(t, rs.Fields, other.Fields, slug)
				require.Equal(t, rs.Def, other.Def, slug)
				require.Equal(t, rs.Kind, other.Kind)
				require.Equal(t, rs.Path, other.Path)
				require.NotSame(t, rs.Def, other.Def, "every resolution builds its own definition")
			}
		})
	}
	require.Equal(t, pristine.Services, f.Services, "resolving leaves the parsed services untouched")
	require.Equal(t, pristine.EnvironmentBlocks, f.EnvironmentBlocks)
	require.Equal(t, pristine.Variables, f.Variables)
	require.Equal(t, pristine.meta.tree, f.meta.tree, "resolving leaves the decoded tree untouched")
}

func TestResolveOfAnUnparsedFile(t *testing.T) {
	var f *File
	r, ds := f.Resolve("", nil)
	require.Nil(t, r)
	require.Nil(t, ds)
	r, ds = (&File{}).Resolve("", nil)
	require.Nil(t, r, "a File not built by Parse has no parse state")
	require.Nil(t, ds)
}

func TestResolveKeepsUndecodableServices(t *testing.T) {
	f, idx, ds := Parse([]byte(t2doc("[services.web]\nimage = \"nginx:1\"\nport = \"80\"\n\n[services.ok]\nimage = \"nginx:1\"\n")), nil)
	require.NotNil(t, f)
	require.True(t, ds.HasErrors())
	r, rds := f.Resolve("", idx)
	require.NotNil(t, r)
	require.Empty(t, rds)
	require.NotNil(t, r.Services["web"], "a service whose body could not be decoded is still listed")
	require.Empty(t, r.Services["web"].Fields)
	require.NotEmpty(t, r.Services["ok"].Fields)
}

func TestResolveSortedHelpers(t *testing.T) {
	r := t2resolve(t, t2doc("[services.b]\nimage = \"x:1\"\nport = 80\nname = \"B\"\n\n[services.a]\nimage = \"x:1\"\n"), "")
	require.Equal(t, []string{"a", "b"}, r.SortedSlugs())
	ids := r.Services["b"].FieldIDs()
	require.Equal(t, []string{"config.image", "config.port", "name", "server"}, ids)
	require.Equal(t, "server", ids[3])
}
