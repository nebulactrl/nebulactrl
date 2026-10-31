package projectconfig

import (
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// t2resolve parses text (no errors allowed) and resolves it for env.
func t2resolve(t *testing.T, text, env string) *Resolved {
	t.Helper()
	f, idx, ds := t2parse(t, text)
	require.False(t, ds.HasErrors(), "parse errors:\n%s", ds.RenderText(""))
	r, rds := f.Resolve(env, idx)
	require.NotNil(t, r)
	require.Empty(t, rds, "Resolve repeats what Parse reported:\n%s", rds.RenderText(""))
	return r
}

// t2field returns the field of a service, failing when it is absent.
func t2field(t *testing.T, r *Resolved, slug, id string) FieldValue {
	t.Helper()
	rs := r.Services[slug]
	require.NotNil(t, rs, "service %q is not in the %q view", slug, r.Environment)
	fv, ok := rs.Fields[id]
	require.True(t, ok, "service %q has no field %q; has %v", slug, id, rs.FieldIDs())
	return fv
}

func t2noField(t *testing.T, r *Resolved, slug, id string) {
	t.Helper()
	rs := r.Services[slug]
	require.NotNil(t, rs, "service %q is not in the %q view", slug, r.Environment)
	_, ok := rs.Fields[id]
	require.False(t, ok, "service %q must not own %q", slug, id)
}

// t2idsWithPrefix lists the sorted field ids of a service that start with prefix.
func t2idsWithPrefix(r *Resolved, slug, prefix string) []string {
	var out []string
	for _, id := range r.Services[slug].FieldIDs() {
		if strings.HasPrefix(id, prefix) {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

// The merge rules, one document per rule, resolved through Parse + Resolve.
func TestMergeRules(t *testing.T) {
	tests := []struct {
		name  string
		toml  string
		env   string
		check func(t *testing.T, r *Resolved)
	}{
		{
			name: "tables merge by key recursively",
			toml: t2doc(`[services.web]
image = "nginx:1"
port = 80
name = "Web"

[services.web.health_check]
type = "http"
path = "/healthz"
deadline_seconds = 90

[environments.staging.services.web.health_check]
path = "/ready"
`),
			env: "staging",
			check: func(t *testing.T, r *Resolved) {
				require.Equal(t, "http", t2field(t, r, "web", "config.healthcheck.type").Value)
				require.Equal(t, "/ready", t2field(t, r, "web", "config.healthcheck.path").Value)
				require.Equal(t, 90, t2field(t, r, "web", "config.healthcheck.deadlineSeconds").Value)
				require.Equal(t, "Web", t2field(t, r, "web", "name").Value)
				require.Equal(t, 80, t2field(t, r, "web", "config.port").Value)
			},
		},
		{
			name: "tables merge by key across variables, domains and tasks",
			toml: t2doc(`[services.web]
image = "nginx:1"
port = 80

[services.web.variables]
A = "base-a"
B = "base-b"

[services.web.domains."a.example.com"]
www_redirect = true

[services.web.tasks.job]
schedule = "0 7 * * *"
command = ["run"]

[environments.staging.services.web.variables]
B = "env-b"
C = "env-c"

[environments.staging.services.web.domains."a.example.com"]
add_prefix = "/v1"

[environments.staging.services.web.domains."b.example.com"]

[environments.staging.services.web.tasks.job]
timeout_seconds = 60

[environments.staging.services.web.tasks.other]
schedule = "@daily"
command = ["other"]
`),
			env: "staging",
			check: func(t *testing.T, r *Resolved) {
				require.Equal(t, Variable{Value: "base-a"}, t2field(t, r, "web", "variables.A").Value)
				require.Equal(t, Variable{Value: "env-b"}, t2field(t, r, "web", "variables.B").Value)
				require.Equal(t, Variable{Value: "env-c"}, t2field(t, r, "web", "variables.C").Value)

				a := t2field(t, r, "web", "domains.a.example.com").Value.(DomainRule)
				require.NotNil(t, a.WWWRedirect, "the base key survives the merge")
				require.True(t, *a.WWWRedirect)
				require.NotNil(t, a.AddPrefix)
				require.Equal(t, "/v1", *a.AddPrefix)
				t2field(t, r, "web", "domains.b.example.com")

				job := t2field(t, r, "web", "tasks.job").Value.(TaskValue)
				require.Equal(t, []string{"run"}, job.Command)
				require.Equal(t, "0 7 * * *", job.Schedule)
				require.Equal(t, 60, job.TimeoutSeconds)
				t2field(t, r, "web", "tasks.other")
			},
		},
		{
			name: "scalars replace",
			toml: t2doc(`[services.web]
image = "nginx:1"
port = 80
name = "Web"
memory_mb = 256
cpus = 0.5

[environments.staging.services.web]
image = "nginx:2"
port = 8080
name = "Web (staging)"
memory_mb = 512
cpus = 2.0
`),
			env: "staging",
			check: func(t *testing.T, r *Resolved) {
				require.Equal(t, "nginx:2", t2field(t, r, "web", "config.image").Value)
				require.Equal(t, 8080, t2field(t, r, "web", "config.port").Value)
				require.Equal(t, "Web (staging)", t2field(t, r, "web", "name").Value)
				require.Equal(t, 512, t2field(t, r, "web", "config.memoryMb").Value)
				require.Equal(t, 2.0, t2field(t, r, "web", "config.cpus").Value)
			},
		},
		{
			name: "an override may restate a scalar with the zero value",
			toml: t2doc(`[services.web]
image = "nginx:1"
port = 80
memory_mb = 256

[environments.staging.services.web]
memory_mb = 0
`),
			env: "staging",
			check: func(t *testing.T, r *Resolved) {
				require.Equal(t, 0, t2field(t, r, "web", "config.memoryMb").Value, "a written zero is owned")
			},
		},
		{
			name: "arrays replace as a whole",
			toml: t2doc(`[services.web]
image = "nginx:1"
port = 80
command = ["run", "--fast"]
depends_on = ["db", "cache"]
networks = ["n1", "n2"]

[services.web.secrets]
keys = ["A_TOKEN", "B_TOKEN"]

[services.db]
image = "postgres:17"

[services.cache]
image = "valkey/valkey:8"

[services.g]

[services.g.git]
connection = "gh"
repo = "acme/g"
branch = "main"
watch_paths = ["a/**", "b/**"]

[services.r]

[services.r.route]
protocol = "tcp"
listen_port = 25565
allow_cidrs = ["10.0.0.0/8", "192.168.0.0/16"]
upstreams = [{ address = "10.0.0.1:1" }, { address = "10.0.0.2:1" }]

[environments.staging.services.web]
command = ["x"]
depends_on = ["db"]
networks = ["n3"]

[environments.staging.services.web.secrets]
keys = ["C_TOKEN"]

[environments.staging.services.g.git]
watch_paths = ["c/**"]

[environments.staging.services.r.route]
allow_cidrs = ["172.16.0.0/12"]
upstreams = [{ address = "10.9.9.9:1" }]
`),
			env: "staging",
			check: func(t *testing.T, r *Resolved) {
				require.Equal(t, []string{"x"}, t2field(t, r, "web", "config.startCommand").Value)
				require.Equal(t, []string{"links.db"}, t2idsWithPrefix(r, "web", "links."))
				require.Equal(t, []string{"networks.n3"}, t2idsWithPrefix(r, "web", "networks."))
				require.Equal(t, []string{"variables.C_TOKEN#secret"}, t2idsWithPrefix(r, "web", "variables."))
				require.Equal(t, []string{"c/**"}, t2field(t, r, "g", "git.watch_paths").Value)
				require.Equal(t, []string{"172.16.0.0/12"}, t2field(t, r, "r", "config.route.allowCidrs").Value)
				require.Equal(t, []UpstreamValue{{Address: "10.9.9.9:1"}}, t2field(t, r, "r", "config.route.upstreams").Value)
			},
		},
		{
			name: "the base keeps its arrays in the base view",
			toml: t2doc(`[services.web]
image = "nginx:1"
port = 80
command = ["run", "--fast"]
depends_on = ["db"]

[services.db]
image = "postgres:17"

[environments.staging.services.web]
command = ["x"]
depends_on = []
`),
			env: "",
			check: func(t *testing.T, r *Resolved) {
				require.Equal(t, []string{"run", "--fast"}, t2field(t, r, "web", "config.startCommand").Value)
				t2field(t, r, "web", "links.db")
			},
		},
		{
			name: "an override may empty an array",
			toml: t2doc(`[services.web]
image = "nginx:1"
port = 80
command = ["run", "--fast"]
depends_on = ["db"]

[services.db]
image = "postgres:17"

[environments.staging.services.web]
command = []
depends_on = []
`),
			env: "staging",
			check: func(t *testing.T, r *Resolved) {
				require.Equal(t, []string{}, t2field(t, r, "web", "config.startCommand").Value, "command = [] clears the start command")
				require.Empty(t, t2idsWithPrefix(r, "web", "links."))
			},
		},
		{
			name: "variable entries are leaves: table form metadata is not inherited",
			toml: t2doc(`[services.g]

[services.g.git]
connection = "gh"
repo = "acme/g"
branch = "main"

[services.g.variables]
BUILD = { value = "1", scope = "build", comment = "read by the bundler", rotate_every_days = 90 }
PLAIN = "p"
META = { value = "m", comment = "keep" }

[environments.staging.services.g.variables]
BUILD = "2"
PLAIN = { value = "p2", comment = "now documented" }
`),
			env: "staging",
			check: func(t *testing.T, r *Resolved) {
				require.Equal(t, Variable{Value: "2"}, t2field(t, r, "g", "variables.BUILD").Value, "a restated entry replaces scope, comment and rotation")
				require.Equal(t, Variable{Value: "p2", Comment: "now documented"}, t2field(t, r, "g", "variables.PLAIN").Value)
				require.Equal(t, Variable{Value: "m", Comment: "keep"}, t2field(t, r, "g", "variables.META").Value)
			},
		},
		{
			name: "a table form entry replaces a table form entry as a unit",
			toml: t2doc(`[services.g]

[services.g.git]
connection = "gh"
repo = "acme/g"
branch = "main"

[services.g.variables]
BUILD = { value = "1", scope = "both", comment = "base comment" }

[environments.staging.services.g.variables]
BUILD = { value = "2", scope = "build" }
`),
			env: "staging",
			check: func(t *testing.T, r *Resolved) {
				require.Equal(t, Variable{Value: "2", Scope: "build"}, t2field(t, r, "g", "variables.BUILD").Value)
			},
		},
		{
			name: "domain options are a leaf: the whole option table is replaced",
			toml: t2doc(`[services.web]
image = "nginx:1"
port = 80

[services.web.domains."shop.acme.com"]
www_redirect = true

[services.web.domains."shop.acme.com".options]
compress = true
security_headers = "strict"

[services.web.domains."shop.acme.com".options.request_headers]
X-Base = "1"

[environments.staging.services.web.domains."shop.acme.com".options]
noindex = true
`),
			env: "staging",
			check: func(t *testing.T, r *Resolved) {
				rule := t2field(t, r, "web", "domains.shop.acme.com").Value.(DomainRule)
				require.NotNil(t, rule.WWWRedirect, "the domain table itself merges by key")
				require.NotNil(t, rule.Options)
				require.True(t, rule.Options.Noindex)
				require.False(t, rule.Options.Compress, "base option keys are not inherited")
				require.Empty(t, rule.Options.SecurityHeaders)
				require.Empty(t, rule.Options.RequestHeaders)
			},
		},
		{
			name: "domain options of the base survive when the override does not touch them",
			toml: t2doc(`[services.web]
image = "nginx:1"
port = 80

[services.web.domains."shop.acme.com".options]
compress = true

[environments.staging.services.web.domains."shop.acme.com"]
add_prefix = "/v1"
`),
			env: "staging",
			check: func(t *testing.T, r *Resolved) {
				rule := t2field(t, r, "web", "domains.shop.acme.com").Value.(DomainRule)
				require.NotNil(t, rule.Options)
				require.True(t, rule.Options.Compress)
				require.NotNil(t, rule.AddPrefix)
			},
		},
		{
			name: "files are leaves: the mode is not inherited",
			toml: t2doc(`[services.web]
image = "nginx:1"
port = 80

[services.web.files."/etc/a.conf"]
mode = "0600"
content = "base"

[services.web.files."/etc/b.conf"]
content = "b"

[environments.staging.services.web.files."/etc/a.conf"]
content = "env"
`),
			env: "staging",
			check: func(t *testing.T, r *Resolved) {
				require.Equal(t, []FileMount{
					{Path: "/etc/a.conf", Mode: "0644", Content: "env"},
					{Path: "/etc/b.conf", Mode: "0644", Content: "b"},
				}, t2field(t, r, "web", "config.files").Value)
			},
		},
		{
			name: "stack resources are leaves per component",
			toml: t2doc(`[services.s]

[services.s.stack]
compose = '''
services:
  web:
    image: nginx:1
  cache:
    image: valkey/valkey:8
'''

[services.s.stack.resources.web]
memory_mb = 256
cpus = 1.0

[services.s.stack.resources.cache]
memory_mb = 128

[environments.staging.services.s.stack.resources.web]
memory_mb = 512
`),
			env: "staging",
			check: func(t *testing.T, r *Resolved) {
				require.Equal(t, StackResourceValue{Component: "web", MemoryMB: 512}, t2field(t, r, "s", "config.stack.resources.web").Value, "cpus of the base entry is not inherited")
				require.Equal(t, StackResourceValue{Component: "cache", MemoryMB: 128}, t2field(t, r, "s", "config.stack.resources.cache").Value)
			},
		},
		{
			name: "route upstream items replace the list",
			toml: t2doc(`[services.web]
image = "nginx:1"
port = 80

[services.r]

[services.r.route]
protocol = "http"
upstreams = [{ address = "10.0.0.1:80" }, { service = "web", port = 80 }]

[environments.staging.services.r.route]
upstreams = [{ service = "web", port = 8080 }]
`),
			env: "staging",
			check: func(t *testing.T, r *Resolved) {
				require.Equal(t, []UpstreamValue{{Service: "web", Port: 8080}}, t2field(t, r, "r", "config.route.upstreams").Value)
			},
		},
		{
			name: "override blocks of another environment are ignored",
			toml: t2doc(`[services.web]
image = "nginx:1"
port = 80

[environments.staging.services.web]
port = 8080

[environments.production.services.web]
port = 9090
`),
			env: "production",
			check: func(t *testing.T, r *Resolved) {
				require.Equal(t, 9090, t2field(t, r, "web", "config.port").Value)
			},
		},
		{
			name: "the base view ignores every override block",
			toml: t2doc(`[services.web]
image = "nginx:1"
port = 80

[environments.staging.services.web]
port = 8080
`),
			env: "",
			check: func(t *testing.T, r *Resolved) {
				require.Equal(t, 80, t2field(t, r, "web", "config.port").Value)
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tc.check(t, t2resolve(t, tc.toml, tc.env))
		})
	}
}

// unset removes key paths after the merge.
func TestUnset(t *testing.T) {
	t.Run("quoted dotted segment", func(t *testing.T) {
		text := t2doc(`[services.web]
image = "nginx:1"
port = 80

[services.web.domains."shop.acme.com"]

[services.web.domains."other.acme.com"]

[environments.staging.services.web]
unset = ['domains."shop.acme.com"']
`)
		staging := t2resolve(t, text, "staging")
		require.Equal(t, []string{"domains.other.acme.com"}, t2idsWithPrefix(staging, "web", "domains."))
		base := t2resolve(t, text, "")
		require.Equal(t, []string{"domains.other.acme.com", "domains.shop.acme.com"}, t2idsWithPrefix(base, "web", "domains."))
	})
	t.Run("nested path", func(t *testing.T) {
		r := t2resolve(t, t2doc(`[services.web]
image = "nginx:1"
port = 80

[services.web.health_check]
type = "http"
path = "/healthz"

[environments.staging.services.web]
unset = ["health_check.path"]
`), "staging")
		require.Equal(t, "http", t2field(t, r, "web", "config.healthcheck.type").Value)
		t2noField(t, r, "web", "config.healthcheck.path")
	})
	t.Run("variable, whole table and the last member of a collection", func(t *testing.T) {
		text := t2doc(`[services.web]
image = "nginx:1"
port = 80

[services.web.variables]
ONLY = "1"

[services.web.scale]
replicas = 2

[services.web.files."/etc/a.conf"]
content = "x"

[services.web.tasks.job]
schedule = "0 7 * * *"
command = ["run"]

[environments.staging.services.web]
unset = ["variables.ONLY", "scale", 'files."/etc/a.conf"', "tasks.job"]
`)
		f, idx, ds := t2parse(t, text)
		require.Empty(t, ds, "a valid unset of present keys is silent:\n%s", ds.RenderText(""))
		staging, rds := f.Resolve("staging", idx)
		require.Empty(t, rds)
		require.Empty(t, t2idsWithPrefix(staging, "web", "variables."))
		t2noField(t, staging, "web", "scale.replicas")
		t2noField(t, staging, "web", "config.files")
		require.Empty(t, t2idsWithPrefix(staging, "web", "tasks."))
		def := staging.Services["web"].Def
		require.Nil(t, def.Variables, "unsetting the last member leaves no empty collection")
		require.Nil(t, def.Scale)
		require.Nil(t, def.Files)
		require.Nil(t, def.Tasks)
		require.Nil(t, def.Unset, "unset is applied, not kept")

		base, _ := f.Resolve("", idx)
		t2field(t, base, "web", "variables.ONLY")
		t2field(t, base, "web", "scale.replicas")
		t2field(t, base, "web", "config.files")
	})
	t.Run("a member unset leaves the other members", func(t *testing.T) {
		r := t2resolve(t, t2doc(`[services.web]
image = "nginx:1"
port = 80

[services.web.variables]
A = "1"
B = "2"

[environments.staging.services.web]
unset = ["variables.A"]
`), "staging")
		require.Equal(t, []string{"variables.B"}, t2idsWithPrefix(r, "web", "variables."))
	})
	t.Run("unset applies after the merge of the same block", func(t *testing.T) {
		r := t2resolve(t, t2doc(`[services.web]
image = "nginx:1"
port = 80

[services.web.scale]
replicas = 2

[environments.staging.services.web]
unset = ["scale"]

[environments.staging.services.web.scale]
replicas = 3
`), "staging")
		t2noField(t, r, "web", "scale.replicas")
	})
	t.Run("unset is scoped to the service it is written in", func(t *testing.T) {
		text := t2doc(`[services.a]
image = "nginx:1"
port = 80
memory_mb = 256

[services.b]
image = "nginx:1"
port = 80
memory_mb = 256

[environments.staging.services.a]
unset = ["memory_mb"]
`)
		r := t2resolve(t, text, "staging")
		t2noField(t, r, "a", "config.memoryMb")
		t2field(t, r, "b", "config.memoryMb")
	})
	t.Run("other environments are not affected", func(t *testing.T) {
		text := t2doc(`[services.web]
image = "nginx:1"
port = 80
memory_mb = 256

[environments.staging.services.web]
unset = ["memory_mb"]

[environments.production.services.web]
port = 81
`)
		r := t2resolve(t, text, "production")
		t2field(t, r, "web", "config.memoryMb")
	})
}

func TestUnsetPathDiagnostics(t *testing.T) {
	base := `[services.web]
image = "nginx:1"
port = 80

[services.web.scale]
replicas = 2

[services.web.variables]
X = "1"

`
	tests := []struct {
		name     string
		unset    string
		want     string // code, "" = no invalid_unset_path at all
		severity string
	}{
		{"unknown key", `["healthcheck"]`, CodeInvalidUnsetPath, SeverityError},
		{"unknown nested key", `["scale.replica"]`, CodeInvalidUnsetPath, SeverityError},
		{"below a scalar", `["scale.replicas.x"]`, CodeInvalidUnsetPath, SeverityError},
		{"below a variable leaf", `["variables.X.value"]`, CodeInvalidUnsetPath, SeverityError},
		{"unterminated quote", `['domains."shop.acme.com']`, CodeInvalidUnsetPath, SeverityError},
		{"empty path", `[""]`, CodeInvalidUnsetPath, SeverityError},
		{"empty segment", `["scale..replicas"]`, CodeInvalidUnsetPath, SeverityError},
		{"trailing dot", `["scale."]`, CodeInvalidUnsetPath, SeverityError},
		{"stray characters", `["scale replicas"]`, CodeInvalidUnsetPath, SeverityError},
		{"unset itself", `["unset"]`, CodeInvalidUnsetPath, SeverityError},
		{"only_in", `["only_in"]`, CodeInvalidUnsetPath, SeverityError},
		{"valid but absent in this service", `["tasks.nightly"]`, CodeInvalidUnsetPath, SeverityWarning},
		{"valid but absent table", `["backups"]`, CodeInvalidUnsetPath, SeverityWarning},
		{"valid and present", `["scale"]`, "", ""},
		{"valid and present nested", `["scale.replicas", "variables.X"]`, "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			text := t2doc(base + "[environments.staging.services.web]\nunset = " + tc.unset + "\n")
			f, idx, ds := t2parse(t, text)
			d := t2find(ds, CodeInvalidUnsetPath, "")
			if tc.want == "" {
				require.Nil(t, d, ds.RenderText(""))
			} else {
				require.NotNil(t, d, ds.RenderText(""))
				require.Equal(t, tc.severity, d.Severity, d.Render(""))
				require.Equal(t, "staging", d.Environment, "the problem exists only in the environment view")
				require.Equal(t, "environments.staging.services.web.unset[0]", t2firstUnsetPath(ds), d.Render(""))
				require.Equal(t, t2lineOf(t, text, "unset ="), d.Line)
			}
			_, rds := f.Resolve("staging", idx)
			require.Empty(t, rds, "Resolve does not repeat the diagnostics")
			_, rds = f.Resolve("", idx)
			require.Empty(t, rds)
		})
	}

	t.Run("an invalid path is not applied", func(t *testing.T) {
		text := t2doc(base + "[environments.staging.services.web]\nunset = [\"healthcheck\", \"scale\"]\n")
		f, idx, ds := t2parse(t, text)
		require.True(t, ds.HasErrors())
		r, _ := f.Resolve("staging", idx)
		t2noField(t, r, "web", "scale.replicas")
		require.Equal(t, "environments.staging.services.web.unset[0]", t2find(ds, CodeInvalidUnsetPath, "").Path)
	})
	t.Run("a second entry is addressed by its index", func(t *testing.T) {
		text := t2doc(base + "[environments.staging.services.web]\nunset = [\"scale\", \"nope\"]\n")
		_, _, ds := t2parse(t, text)
		require.NotNil(t, t2find(ds, CodeInvalidUnsetPath, "environments.staging.services.web.unset[1]"), ds.RenderText(""))
		require.Nil(t, t2find(ds, CodeInvalidUnsetPath, "environments.staging.services.web.unset[0]"))
	})
	t.Run("did you mean for a typo", func(t *testing.T) {
		_, _, ds := t2parse(t, t2doc(base+"[environments.staging.services.web]\nunset = [\"healthcheck\"]\n"))
		d := t2find(ds, CodeInvalidUnsetPath, "")
		require.NotNil(t, d)
		require.Contains(t, d.Suggestions, "health_check")
	})
}

// t2firstUnsetPath returns the path of the first invalid_unset_path diagnostic.
func t2firstUnsetPath(ds Diagnostics) string {
	if d := t2find(ds, CodeInvalidUnsetPath, ""); d != nil {
		return d.Path
	}
	return ""
}

// An environment block merges processes key by key, like tasks: a process the
// base has keeps every key the block does not restate, and a process only the
// block has is added as it is written.
func TestMergeProcesses(t *testing.T) {
	text := t2doc(`[services.web]
image = "nginx:1"
port = 80

[services.web.processes.worker]
command = ["run", "worker"]
stop_grace_seconds = 120
port = 81

[services.web.processes.worker.health_check]
type = "tcp"
deadline_seconds = 90

[services.web.processes.worker.scale]
replicas = 2

[services.web.processes.clock]
command = ["run", "clock"]
singleton = true

[environments.staging.services.web.processes.worker]
stop_grace_seconds = 30

[environments.staging.services.web.processes.worker.health_check]
deadline_seconds = 30

[environments.staging.services.web.processes.worker.scale]
replicas = 1

[environments.staging.services.web.processes.mailer]
command = ["run", "mailer"]
`)
	base := t2resolve(t, text, "")
	require.Equal(t, []string{"config.processes.clock", "config.processes.worker"}, t2idsWithPrefix(base, "web", "config.processes."))
	require.Equal(t, []string{"processes.worker.scale.replicas"}, t2idsWithPrefix(base, "web", "processes."))
	worker := t2field(t, base, "web", "config.processes.worker").Value.(ProcessValue)
	require.Equal(t, ProcessValue{
		Name: "worker", Command: []string{"run", "worker"}, Port: 81, StopGraceSeconds: 120,
		HealthCheck: ProcessHealth{Type: "tcp", DeadlineSeconds: 90},
	}, worker)
	require.Equal(t, 2, t2field(t, base, "web", "processes.worker.scale.replicas").Value)

	staging := t2resolve(t, text, "staging")
	require.Equal(t, []string{"config.processes.clock", "config.processes.mailer", "config.processes.worker"}, t2idsWithPrefix(staging, "web", "config.processes."))
	worker = t2field(t, staging, "web", "config.processes.worker").Value.(ProcessValue)
	require.Equal(t, ProcessValue{
		Name: "worker", Command: []string{"run", "worker"}, Port: 81, StopGraceSeconds: 30,
		HealthCheck: ProcessHealth{Type: "tcp", DeadlineSeconds: 30},
	}, worker, "the block restates two keys; the rest comes from the base")
	require.Equal(t, 1, t2field(t, staging, "web", "processes.worker.scale.replicas").Value)
	require.Equal(t, ProcessValue{Name: "mailer", Command: []string{"run", "mailer"}}, t2field(t, staging, "web", "config.processes.mailer").Value)
	require.Equal(t, ProcessValue{Name: "clock", Command: []string{"run", "clock"}, Singleton: true}, t2field(t, staging, "web", "config.processes.clock").Value)
}

func TestUnsetProcesses(t *testing.T) {
	base := `[services.web]
image = "nginx:1"
port = 80

[services.web.processes.worker]
command = ["run", "worker"]

[services.web.processes.worker.scale]
replicas = 2

[services.web.processes.clock]
command = ["run", "clock"]
singleton = true

`
	resolve := func(unset string) (*Resolved, Diagnostics) {
		text := t2doc(base + "[environments.staging.services.web]\nunset = " + unset + "\n")
		f, idx, ds := t2parse(t, text)
		r, rds := f.Resolve("staging", idx)
		return r, append(ds, rds...)
	}

	t.Run("a whole process and its scale are gone", func(t *testing.T) {
		r, ds := resolve(`["processes.clock"]`)
		require.Empty(t, ds, ds.RenderText(""))
		require.Equal(t, []string{"config.processes.worker"}, t2idsWithPrefix(r, "web", "config.processes."))
		require.Equal(t, []string{"processes.worker.scale.replicas"}, t2idsWithPrefix(r, "web", "processes."))
	})
	t.Run("a process' scale only", func(t *testing.T) {
		r, ds := resolve(`["processes.worker.scale"]`)
		require.Empty(t, ds, ds.RenderText(""))
		require.Equal(t, []string{"config.processes.clock", "config.processes.worker"}, t2idsWithPrefix(r, "web", "config.processes."))
		require.Empty(t, t2idsWithPrefix(r, "web", "processes."))
		require.Nil(t, r.Services["web"].Def.Processes["worker"].Scale)
	})
	t.Run("a process' scale key", func(t *testing.T) {
		r, ds := resolve(`["processes.worker.scale.replicas"]`)
		require.Empty(t, ds, ds.RenderText(""))
		require.Empty(t, t2idsWithPrefix(r, "web", "processes."))
	})
	t.Run("the last process leaves no empty collection", func(t *testing.T) {
		r, ds := resolve(`["processes.worker", "processes.clock"]`)
		require.Empty(t, ds, ds.RenderText(""))
		require.Nil(t, r.Services["web"].Def.Processes)
		require.Empty(t, t2idsWithPrefix(r, "web", "config.processes."))
	})
	t.Run("an unknown process is a warning", func(t *testing.T) {
		_, ds := resolve(`["processes.ghost"]`)
		d := t2find(ds, CodeInvalidUnsetPath, "")
		require.NotNil(t, d, ds.RenderText(""))
		require.Equal(t, SeverityWarning, d.Severity)
	})
	t.Run("a key that is not a process key is an error", func(t *testing.T) {
		_, ds := resolve(`["processes.worker.bogus"]`)
		d := t2find(ds, CodeInvalidUnsetPath, "")
		require.NotNil(t, d, ds.RenderText(""))
		require.Equal(t, SeverityError, d.Severity)
	})
}
