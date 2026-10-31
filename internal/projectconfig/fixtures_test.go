package projectconfig

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

const t1SchemaComment = "#:schema https://nebula.example.com/schema/nebula.toml.json\n"

// t1WarnHeader reads the codes of a `# warn: code[, code...]` line in the
// leading comments of a valid fixture: the warnings the fixture is allowed to
// produce.
func t1WarnHeader(data []byte) map[string]bool {
	out := map[string]bool{}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "#") {
			break
		}
		if rest, ok := strings.CutPrefix(strings.TrimSpace(strings.TrimPrefix(line, "#")), "warn:"); ok {
			for _, c := range strings.Split(rest, ",") {
				if c = strings.TrimSpace(c); c != "" {
					out[c] = true
				}
			}
		}
	}
	return out
}

func t1Load(t *testing.T, name string) (text string, f *File, idx *Index) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "valid", name))
	require.NoError(t, err)
	f, idx, ds := Parse(data, nil)
	require.NotNil(t, f)
	require.NotNil(t, idx)
	require.False(t, ds.HasErrors(), "%s has errors:\n%s", name, ds.RenderText(name))
	return string(data), f, idx
}

func t1View(t *testing.T, f *File, idx *Index, env string) *Resolved {
	t.Helper()
	res, extra := f.Resolve(env, idx)
	require.NotNil(t, res, "environment %q", env)
	require.Empty(t, extra, "environment %q", env)
	require.Equal(t, env, res.Environment)
	return res
}

func t1Svc(t *testing.T, res *Resolved, slug string) *ResolvedService {
	t.Helper()
	rs, ok := res.Services[slug]
	require.True(t, ok, "service %q is not in the view of %q; have %v", slug, res.Environment, res.SortedSlugs())
	require.Equal(t, slug, rs.Slug)
	return rs
}

func t1Field(t *testing.T, rs *ResolvedService, id string) FieldValue {
	t.Helper()
	fv, ok := rs.Fields[id]
	require.True(t, ok, "service %q has no field %q; have %v", rs.Slug, id, rs.FieldIDs())
	require.Equal(t, id, fv.ID)
	return fv
}

func t1IDs(rs *ResolvedService, prefix string) []string {
	var out []string
	for _, id := range rs.FieldIDs() {
		if strings.HasPrefix(id, prefix) {
			out = append(out, id)
		}
	}
	return out
}

// t1LineOf returns the 1-based number of the first line containing sub.
func t1LineOf(t *testing.T, text, sub string) int {
	t.Helper()
	for i, line := range strings.Split(text, "\n") {
		if strings.Contains(line, sub) {
			return i + 1
		}
	}
	require.Failf(t, "line not found", "no line contains %q", sub)
	return 0
}

func TestValidFixtures(t *testing.T) {
	files := fixtureFiles(t, filepath.Join("testdata", "valid"))
	require.NotEmpty(t, files, "testdata/valid has no fixtures")

	// The fixtures this suite relies on.
	for _, name := range []string{"acme-shop", "single-image", "stack", "route-tcp", "database-redis", "environments", "minimal"} {
		require.FileExists(t, filepath.Join("testdata", "valid", name+".toml"))
	}

	for _, path := range files {
		t.Run(filepath.ToSlash(path), func(t *testing.T) {
			data, err := os.ReadFile(path)
			require.NoError(t, err)
			require.True(t, strings.HasPrefix(string(data), t1SchemaComment), "a fixture starts with the #:schema line")

			f, idx, ds := Parse(data, nil)
			require.NotNil(t, f)
			require.NotNil(t, idx)
			require.False(t, ds.HasErrors(), "errors:\n%s", ds.RenderText(path))
			warn := t1WarnHeader(data)
			for _, d := range ds {
				require.True(t, d.Severity == SeverityWarning && warn[d.Code],
					"unexpected diagnostic (a warning needs a `# warn: %s` header): %s", d.Code, d.Render(path))
			}

			for _, env := range append([]string{""}, f.Environments()...) {
				res, extra := f.Resolve(env, idx)
				require.NotNil(t, res, "environment %q", env)
				require.False(t, extra.HasErrors(), "environment %q:\n%s", env, extra.RenderText(path))
				require.Equal(t, env, res.Environment)
				require.NotNil(t, res.Services)
				for slug, rs := range res.Services {
					require.Equal(t, slug, rs.Slug)
					require.NotNil(t, rs.Def, "%s/%s", env, slug)
					require.NotEmpty(t, rs.Kind, "%s/%s", env, slug)
					require.NotEmpty(t, rs.Fields, "%s/%s has no owned field", env, slug)
				}
			}
		})
	}
}

// Every field id a resolved view produces is one the catalog knows, with the
// same apply kind; a field exists only for the kinds the catalog lists, and
// carries the position of the line that set it.
func TestFieldIDsResolveInCatalog(t *testing.T) {
	for _, path := range fixtureFiles(t, filepath.Join("testdata", "valid")) {
		t.Run(filepath.ToSlash(path), func(t *testing.T) {
			data, err := os.ReadFile(path)
			require.NoError(t, err)
			f, idx, ds := Parse(data, nil)
			require.False(t, ds.HasErrors())
			for _, env := range append([]string{""}, f.Environments()...) {
				res, _ := f.Resolve(env, idx)
				require.NotNil(t, res)
				for _, slug := range res.SortedSlugs() {
					rs := res.Services[slug]
					for id, fv := range rs.Fields {
						where := env + "/" + slug + "/" + id
						require.Equal(t, id, fv.ID, where)
						spec, ok := LookupField(id)
						require.True(t, ok, "%s is not in the catalog", where)
						require.Equal(t, spec.Applies, fv.Applies, "apply kind of %s", where)
						if len(spec.Kinds) > 0 {
							require.True(t, slices.Contains(spec.Kinds, rs.Kind), "%s: the catalog does not list kind %q for %s", where, rs.Kind, spec.ID)
						}
						require.Contains(t, []string{AppliesStaged, AppliesImmediate}, fv.Applies, where)
						require.NotEmpty(t, fv.Path, where)
						require.False(t, fv.Pos.IsZero(), "%s has no source position", where)
						require.NotNil(t, fv.Value, where)
					}
				}
			}
		})
	}
}

// Every invalid fixture carries `# expect: code[,code...]`; every expected code
// must be among the diagnostics of Parse, and every diagnostic must be a
// well-formed object that points inside the file.
func TestInvalidFixtures(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("testdata", "invalid", "*.toml"))
	require.NoError(t, err)
	if len(files) == 0 {
		t.Skip("testdata/invalid has no fixtures")
	}
	for _, path := range files {
		t.Run(filepath.Base(path), func(t *testing.T) {
			data, err := os.ReadFile(path)
			require.NoError(t, err)
			lines := strings.Split(string(data), "\n")
			require.True(t, strings.HasPrefix(lines[0], "# expect:"), "the first line of a fixture is `# expect: code[,code...]`")

			expect, _ := fixtureHeader(data)
			require.NotEmpty(t, expect, "the `# expect:` line names no code")

			f, idx, ds := Parse(data, nil)
			if f != nil {
				require.NotNil(t, idx)
			}
			for _, code := range expect {
				require.True(t, ds.HasCode(code), "expected a %s diagnostic; got:\n%s", code, ds.RenderText(path))
			}

			for _, d := range ds {
				require.NotEmpty(t, d.Code)
				require.NotEmpty(t, d.Message)
				require.Contains(t, []string{SeverityError, SeverityWarning, SeverityInfo}, d.Severity, d.Render(path))
				if d.Line > 0 {
					require.Greater(t, d.Column, 0, d.Render(path))
					require.LessOrEqual(t, d.Line, len(lines)+1, "line beyond the file: %s", d.Render(path))
				}
			}
		})
	}
}

func TestAcmeShopFixtureStaging(t *testing.T) {
	text, f, idx := t1Load(t, "acme-shop.toml")
	require.Equal(t, []string{"staging"}, f.Environments())
	res := t1View(t, f, idx, "staging")

	require.Equal(t, "acme-shop", res.Project)
	require.Equal(t, "fra-staging", res.Server)
	require.Equal(t, []string{"db", "web", "worker"}, res.SortedSlugs(), "printer exists only in production (only_in)")
	require.NotContains(t, res.Services, "printer")

	// The shared layers are not merged: the environment layer stands next to the project layer.
	require.Equal(t, "Acme Shop (staging)", res.EnvShared["APP_NAME"].Value)
	require.Equal(t, "Acme Shop", res.Shared["APP_NAME"].Value)

	web := t1Svc(t, res, "web")
	require.Equal(t, KindGit, web.Kind)
	require.Equal(t, "services.web", web.Path)
	require.Equal(t, "fra-staging", web.ServerName)
	require.Equal(t, "develop", *web.Def.Git.Branch)
	require.Equal(t, "github-acme", *web.Def.Git.Connection, "the base keys stay")
	require.Nil(t, web.Def.Scale, "unset")
	require.Nil(t, web.Def.Uptime, "unset")
	require.Nil(t, web.Def.Unset, "unset is applied")
	require.Empty(t, t1IDs(web, "scale."))
	require.Empty(t, t1IDs(web, "uptime."))

	branch := t1Field(t, web, "config.gitBranch")
	require.Equal(t, "develop", branch.Value)
	require.Equal(t, AppliesStaged, branch.Applies)
	require.Equal(t, "environments.staging.services.web.git.branch", branch.Path)
	require.Equal(t, t1LineOf(t, text, `branch = "develop"`), branch.Pos.Line, "the line that set the winning value")
	require.Equal(t, "services.web.git.connection", t1Field(t, web, "git.link").Path)

	nodeEnv := t1Field(t, web, "variables.NODE_ENV")
	require.Equal(t, Variable{Value: "staging"}, nodeEnv.Value)
	require.Equal(t, "environments.staging.services.web.variables.NODE_ENV", nodeEnv.Path)
	require.Equal(t, t1LineOf(t, text, `NODE_ENV = "staging"`), nodeEnv.Pos.Line)
	require.Equal(t, Variable{Value: "https://shop.staging.acme.com"}, t1Field(t, web, "variables.APP_URL").Value)
	require.Equal(t, Variable{Value: "${{shared.APP_NAME}}"}, t1Field(t, web, "variables.SITE_NAME").Value)
	require.Equal(t, Variable{Value: "${{db.DATABASE_URL}}"}, t1Field(t, web, "variables.DATABASE_URL").Value)
	require.Equal(t, Variable{Value: "${{secret(48)}}"}, t1Field(t, web, "variables.SESSION_SECRET").Value)
	require.Equal(t, Variable{Value: "1", Scope: "build", Comment: "read by the bundler"}, t1Field(t, web, "variables.BUILD_FLAG").Value)
	require.Equal(t, SecretDecl{Name: "STRIPE_SECRET_KEY", Scope: "runtime"}, t1Field(t, web, "variables.STRIPE_SECRET_KEY#secret").Value)

	// Exactly one domain: the base one is unset, the staging one is added, not proxied.
	require.Equal(t, []string{"domains.shop.staging.acme.com", "domains.shop.staging.acme.com.cloudflare"}, t1IDs(web, "domains."))
	rule, ok := t1Field(t, web, "domains.shop.staging.acme.com").Value.(DomainRule)
	require.True(t, ok)
	require.Equal(t, DomainRule{Host: "shop.staging.acme.com"}, rule)
	cf := t1Field(t, web, "domains.shop.staging.acme.com.cloudflare")
	require.Equal(t, AppliesImmediate, cf.Applies)
	cfRule, ok := cf.Value.(CloudflareRule)
	require.True(t, ok)
	require.NotNil(t, cfRule.Proxied)
	require.False(t, *cfRule.Proxied)
	require.Nil(t, cfRule.OriginLock)

	// What the environment does not touch stays.
	require.Equal(t, 3000, t1Field(t, web, "config.port").Value)
	require.Equal(t, "develop", *t1Svc(t, res, "worker").Def.Git.Branch)
	t1Field(t, web, "tasks.digest")
	t1Field(t, web, "config.preDeployCommand")
	require.Equal(t, "db", t1Field(t, web, "links.db").Value)

	server := t1Field(t, web, "server")
	require.Equal(t, "fra-staging", server.Value)
	require.Equal(t, "environments.staging.server", server.Path)

	worker := t1Svc(t, res, "worker")
	require.Equal(t, "fra-staging", worker.ServerName)
	require.Equal(t, "develop", t1Field(t, worker, "config.gitBranch").Value)
	require.Equal(t, "environments.staging.services.worker.git.branch", t1Field(t, worker, "config.gitBranch").Path)
	require.Equal(t, 512, t1Field(t, worker, "config.memoryMb").Value)
	require.Equal(t, []string{"apps/worker/**", "packages/**"}, t1Field(t, worker, "git.watch_paths").Value)
	require.Equal(t, "services.worker.git.watch_paths", t1Field(t, worker, "git.watch_paths").Path)

	db := t1Svc(t, res, "db")
	require.Equal(t, KindDatabase, db.Kind)
	require.Empty(t, t1IDs(db, "backups."), "the staging block unsets the backup schedule")
	require.Nil(t, db.Def.Backups)
	require.Equal(t, "postgres", t1Field(t, db, "config.database.engine").Value)
	require.Equal(t, 30, t1Field(t, db, "uptime.reminderMinutes").Value)
}

func TestAcmeShopFixtureBaseAndProduction(t *testing.T) {
	text, f, idx := t1Load(t, "acme-shop.toml")
	base := t1View(t, f, idx, "")
	prod := t1View(t, f, idx, "production")

	for _, res := range []*Resolved{base, prod} {
		require.Equal(t, "fra-1", res.Server)
		require.Equal(t, []string{"db", "printer", "web", "worker"}, res.SortedSlugs())
		require.Empty(t, res.EnvShared)
		require.Equal(t, "Acme Shop", res.Shared["APP_NAME"].Value)
		require.Equal(t, "acme-shop", res.Project)
	}
	// An environment without a block is the base.
	for _, slug := range base.SortedSlugs() {
		require.Equal(t, base.Services[slug].FieldIDs(), prod.Services[slug].FieldIDs(), slug)
	}

	web := t1Svc(t, prod, "web")
	require.Equal(t, "main", t1Field(t, web, "config.gitBranch").Value)
	require.Equal(t, "services.web.git.branch", t1Field(t, web, "config.gitBranch").Path)
	require.Equal(t, t1LineOf(t, text, `branch      = "main"`), t1Field(t, web, "config.gitBranch").Pos.Line)
	require.Equal(t, "Web", t1Field(t, web, "name").Value)
	require.Equal(t, AppliesImmediate, t1Field(t, web, "name").Applies)

	// Types of values.
	port := t1Field(t, web, "config.port")
	require.Equal(t, 3000, port.Value)
	require.Equal(t, AppliesStaged, port.Applies)
	require.Equal(t, "services.web.port", port.Path)
	require.Equal(t, t1LineOf(t, text, "port               = 3000"), port.Pos.Line)
	require.Equal(t, 30, t1Field(t, web, "config.stopGraceSeconds").Value)
	require.Equal(t, "/healthz", t1Field(t, web, "config.healthcheck.path").Value)
	require.Equal(t, "http", t1Field(t, web, "config.healthcheck.type").Value)
	require.Equal(t, 90, t1Field(t, web, "config.healthcheck.deadlineSeconds").Value)
	require.Empty(t, t1IDs(web, "config.healthcheck.port"), "the key is absent, so it is not owned")
	require.Equal(t, []string{"bun", "run", "db:migrate"}, t1Field(t, web, "config.preDeployCommand").Value)
	require.Equal(t, 300, t1Field(t, web, "config.preDeployTimeoutSeconds").Value)
	require.Equal(t, "dockerfile", t1Field(t, web, "config.buildType").Value)
	require.Equal(t, "apps/web/Dockerfile", t1Field(t, web, "config.dockerfilePath").Value)
	require.Equal(t, ".", t1Field(t, web, "config.buildContext").Value)

	link := t1Field(t, web, "git.link")
	require.Equal(t, GitLink{ConnectionName: "github-acme", Repo: "acme/shop"}, link.Value)
	require.Equal(t, AppliesImmediate, link.Applies)
	require.Equal(t, true, t1Field(t, web, "git.auto_deploy").Value)
	wp := t1Field(t, web, "git.watch_paths")
	require.Equal(t, []string{"apps/web/**", "packages/**", "!**/*.md"}, wp.Value)
	require.Equal(t, AppliesImmediate, wp.Applies)

	nodeEnv := t1Field(t, web, "variables.NODE_ENV")
	require.Equal(t, Variable{Value: "production"}, nodeEnv.Value)
	require.Equal(t, AppliesStaged, nodeEnv.Applies)
	secret := t1Field(t, web, "variables.STRIPE_SECRET_KEY#secret")
	require.Equal(t, SecretDecl{Name: "STRIPE_SECRET_KEY", Scope: "runtime"}, secret.Value)
	require.Equal(t, "services.web.secrets.keys[0]", secret.Path)

	replicas := t1Field(t, web, "scale.replicas")
	require.Equal(t, 2, replicas.Value)
	require.Equal(t, AppliesImmediate, replicas.Applies)
	require.Equal(t, 3, t1Field(t, web, "uptime.failureThreshold").Value)
	require.Equal(t, 60, t1Field(t, web, "uptime.reminderMinutes").Value)
	require.Empty(t, t1IDs(web, "uptime.recoveryThreshold"))

	require.Equal(t, TaskValue{
		Name: "digest", Schedule: "0 7 * * 1-5", Timezone: "Europe/Berlin", Command: []string{"bun", "run", "jobs:digest"},
		Enabled: true, TimeoutSeconds: 900, Concurrency: "forbid",
	}, t1Field(t, web, "tasks.digest").Value)
	require.Equal(t, AppliesImmediate, t1Field(t, web, "tasks.digest").Applies)

	// One domain.
	require.Equal(t, []string{"domains.shop.acme.com", "domains.shop.acme.com.cloudflare"}, t1IDs(web, "domains."))
	rule, ok := t1Field(t, web, "domains.shop.acme.com").Value.(DomainRule)
	require.True(t, ok)
	require.Equal(t, "shop.acme.com", rule.Host)
	require.Empty(t, rule.PathPrefix)
	require.NotNil(t, rule.WWWRedirect)
	require.True(t, *rule.WWWRedirect)
	require.Nil(t, rule.TargetPort, "undeclared keys stay nil so the live value is kept")
	require.NotNil(t, rule.Options)
	require.True(t, rule.Options.Compress)
	require.Equal(t, "strict", rule.Options.SecurityHeaders)
	cf, ok := t1Field(t, web, "domains.shop.acme.com.cloudflare").Value.(CloudflareRule)
	require.True(t, ok)
	require.NotNil(t, cf.Proxied)
	require.True(t, *cf.Proxied)
	require.Equal(t, AppliesImmediate, t1Field(t, web, "domains.shop.acme.com.cloudflare").Applies)

	// The worker is a git service without a port.
	worker := t1Svc(t, prod, "worker")
	require.Equal(t, []string{"bun", "run", "worker"}, t1Field(t, worker, "config.startCommand").Value)
	require.Empty(t, t1IDs(worker, "config.port"))
	require.Equal(t, "main", t1Field(t, worker, "config.gitBranch").Value)

	// The database keeps its backup schedule.
	db := t1Svc(t, prod, "db")
	require.Equal(t, "17", t1Field(t, db, "config.database.version").Value)
	require.Equal(t, "shop", t1Field(t, db, "config.database.databaseName").Value)
	require.Equal(t, "shop", t1Field(t, db, "config.database.username").Value)
	backup := t1Field(t, db, "backups.nightly")
	require.Equal(t, BackupValue{
		Key: "nightly", DestinationName: "offsite-s3", Cron: "0 2 * * *", Timezone: "Europe/Berlin",
		Enabled: true, RetentionCount: 14, RetentionDays: 30,
	}, backup.Value)
	require.Equal(t, AppliesImmediate, backup.Applies)
	require.Equal(t, "services.db.backups.nightly", backup.Path)

	// The external route.
	printer := t1Svc(t, prod, "printer")
	require.Equal(t, KindRoute, printer.Kind)
	require.Equal(t, "fra-1", printer.ServerName, "the root server")
	require.Equal(t, "Office printer", t1Field(t, printer, "name").Value)
	require.Equal(t, "http", t1Field(t, printer, "config.route.protocol").Value)
	require.Equal(t, []UpstreamValue{{Address: "192.168.1.20:631"}}, t1Field(t, printer, "config.route.upstreams").Value)
	require.Equal(t, "/", t1Field(t, printer, "config.route.healthPath").Value)
	require.Equal(t, []string{"domains.print.acme.com"}, t1IDs(printer, "domains."))
	pr, ok := t1Field(t, printer, "domains.print.acme.com").Value.(DomainRule)
	require.True(t, ok)
	require.NotNil(t, pr.Options)
	require.NotNil(t, pr.Options.Access)
	require.Equal(t, []string{"203.0.113.0/24"}, pr.Options.Access.AllowCIDRs)
}

func TestSingleImageFixture(t *testing.T) {
	text, f, idx := t1Load(t, "single-image.toml")
	require.Empty(t, f.Environments())
	res := t1View(t, f, idx, "")
	require.Equal(t, []string{"api"}, res.SortedSlugs())
	api := t1Svc(t, res, "api")
	require.Equal(t, KindImage, api.Kind)
	require.Equal(t, "fra-1", api.ServerName)

	require.Equal(t, "ghcr.io/acme/api:1.4.2", t1Field(t, api, "config.image").Value)
	require.Equal(t, 8080, t1Field(t, api, "config.port").Value)
	require.Equal(t, []string{"/app/server", "--listen", ":8080"}, t1Field(t, api, "config.startCommand").Value)
	require.Equal(t, false, t1Field(t, api, "config.oneshot").Value, "a false that is written is owned")
	require.Equal(t, false, t1Field(t, api, "config.blockEgress").Value)
	require.Equal(t, 1.5, t1Field(t, api, "config.cpus").Value)
	require.Equal(t, 512, t1Field(t, api, "config.memoryMb").Value)
	require.Equal(t, 20, t1Field(t, api, "config.stopGraceSeconds").Value)
	require.Equal(t, 5, t1Field(t, api, "config.imageRetention").Value)
	require.Equal(t, "backplane", t1Field(t, api, "networks.backplane").Value)
	require.Equal(t, true, t1Field(t, api, "generate_domain").Value)
	require.Len(t, t1IDs(api, "config.healthcheck."), 8)
	require.Equal(t, 10, t1Field(t, api, "config.healthcheck.startPeriodSeconds").Value)
	require.Equal(t, 2, t1Field(t, api, "config.healthcheck.successThreshold").Value)
	require.Equal(t, []string{"/app/server", "migrate"}, t1Field(t, api, "config.preDeployCommand").Value)
	require.Equal(t, 120, t1Field(t, api, "config.postDeployTimeoutSeconds").Value)

	// Variables: plain, referencing, generated, table form.
	require.Equal(t, Variable{Value: "${{LOG_DIR}}/api.log"}, t1Field(t, api, "variables.LOG_FILE").Value)
	require.Equal(t, Variable{Value: "${{shared.REGION}}"}, t1Field(t, api, "variables.REGION_NAME").Value)
	flag, ok := t1Field(t, api, "variables.FEATURE_FLAG").Value.(Variable)
	require.True(t, ok)
	require.Equal(t, "on", flag.Value)
	require.Equal(t, "runtime", flag.Scope)
	require.Equal(t, "rolled out to everyone", flag.Comment)
	require.NotNil(t, flag.RotateEveryDays)
	require.Equal(t, 0, *flag.RotateEveryDays)
	pub, ok := t1Field(t, api, "variables.PUBLIC_API_KEY").Value.(Variable)
	require.True(t, ok)
	require.True(t, pub.Public, "public silences literal_secret")
	require.Equal(t, SecretDecl{Name: "SENTRY_AUTH_TOKEN", Scope: "runtime"}, t1Field(t, api, "variables.SENTRY_AUTH_TOKEN#secret").Value)
	require.Equal(t, "services.api.secrets.keys[1]", t1Field(t, api, "variables.SENTRY_AUTH_TOKEN#secret").Path)

	require.Equal(t, Volume{Name: "uploads", MountPath: "/app/uploads"}, t1Field(t, api, "volumes.uploads").Value)
	require.Equal(t, Volume{Name: "cache", MountPath: "/var/cache/api"}, t1Field(t, api, "volumes.cache").Value)

	require.Equal(t, []FileMount{
		{Path: "/etc/api/api.conf", Mode: "0644", Content: "listen = 8080\nlog    = \"info\"\n"},
		{Path: "/etc/api/motd", Mode: "0644", Content: "welcome\n"},
	}, t1Field(t, api, "config.files").Value, "one field for the whole set, sorted by path, default mode")
	require.Equal(t, t1LineOf(t, text, `[services.api.files."/etc/api/api.conf"]`), t1Field(t, api, "config.files").Pos.Line, "the position of the files table")

	// Domains with options.
	require.Equal(t, []string{
		"domains.api.acme.com", "domains.api.acme.com.cloudflare", "domains.api.acme.com/v1", "domains.old-api.acme.com",
	}, t1IDs(api, "domains."))
	main, ok := t1Field(t, api, "domains.api.acme.com").Value.(DomainRule)
	require.True(t, ok)
	require.Equal(t, 8080, *main.TargetPort)
	require.Equal(t, "auto", *main.TLS)
	require.Equal(t, "cloudflare", *main.DNSResolver)
	o := main.Options
	require.NotNil(t, o)
	require.True(t, o.Compress)
	require.False(t, o.Noindex)
	require.Equal(t, "basic", o.SecurityHeaders)
	require.NotNil(t, o.ForceHTTPS)
	require.True(t, *o.ForceHTTPS)
	require.Equal(t, []agentprotocol.Header{{Name: "X-Forwarded-Prefix", Value: "/v1"}}, o.RequestHeaders)
	require.Equal(t, []agentprotocol.Header{{Name: "X-Frame-Options", Value: "DENY"}}, o.ResponseHeaders)
	require.NotNil(t, o.CORS)
	require.Equal(t, []string{"https://app.acme.com"}, o.CORS.Origins)
	require.Equal(t, 600, o.CORS.MaxAgeSeconds)
	require.True(t, o.CORS.Credentials)
	require.Len(t, o.Redirects, 2, "redirects keep their order")
	require.Equal(t, "^https://api.acme.com/old/(.*)", o.Redirects[0].Regex)
	require.True(t, o.Redirects[0].Permanent)
	require.False(t, o.Redirects[1].Permanent)
	require.NotNil(t, o.RateLimit)
	require.Equal(t, 100, o.RateLimit.Average)
	require.Equal(t, 200, o.RateLimit.Burst)
	require.NotNil(t, o.Access)
	require.Equal(t, []string{"203.0.113.0/24", "198.51.100.0/24"}, o.Access.AllowCIDRs)
	require.Equal(t, []string{"203.0.113.66/32"}, o.Access.DenyCIDRs)
	require.Equal(t, 403, o.Access.RejectStatus)
	sub, ok := t1Field(t, api, "domains.api.acme.com/v1").Value.(DomainRule)
	require.True(t, ok)
	require.Equal(t, "api.acme.com", sub.Host)
	require.Equal(t, "/v1", sub.PathPrefix)
	require.True(t, *sub.StripPrefix)
	require.Equal(t, "/v2", *sub.AddPrefix)
	require.Nil(t, sub.Options)
	old, ok := t1Field(t, api, "domains.old-api.acme.com").Value.(DomainRule)
	require.True(t, ok)
	require.Equal(t, "redirect", *old.Kind)
	require.Equal(t, "api.acme.com", *old.RedirectTo)
	require.Equal(t, 301, *old.RedirectCode)
	cf, ok := t1Field(t, api, "domains.api.acme.com.cloudflare").Value.(CloudflareRule)
	require.True(t, ok)
	require.True(t, *cf.Proxied)
	require.False(t, *cf.OriginLock)

	// Scale, uptime, tasks.
	require.Equal(t, 1, t1Field(t, api, "scale.replicas").Value)
	require.Equal(t, false, t1Field(t, api, "scale.autoscaling.enabled").Value)
	require.Equal(t, 3, t1Field(t, api, "scale.autoscaling.max").Value)
	require.Equal(t, 70, t1Field(t, api, "scale.autoscaling.targetCpuPercent").Value)
	require.Len(t, t1IDs(api, "uptime."), 5)
	require.Equal(t, TaskValue{Name: "report", Schedule: "@daily", Timezone: "UTC", Command: []string{"/app/server", "report"},
		Enabled: true, TimeoutSeconds: 1800, Concurrency: "allow"}, t1Field(t, api, "tasks.report").Value, "defaults are applied")
	require.Equal(t, TaskValue{Name: "cleanup", Schedule: "0 3 * * *", Timezone: "UTC", Command: []string{"/app/server", "cleanup"},
		Enabled: true, TimeoutSeconds: 600, Concurrency: "forbid"}, t1Field(t, api, "tasks.cleanup").Value)

	// Docker options: the safe set.
	require.Equal(t, []string{"NET_BIND_SERVICE"}, t1Field(t, api, "config.docker.capAdd").Value)
	require.Equal(t, []string{"ALL"}, t1Field(t, api, "config.docker.capDrop").Value)
	require.Equal(t, true, t1Field(t, api, "config.docker.noNewPrivileges").Value)
	require.Equal(t, false, t1Field(t, api, "config.docker.readOnlyRootfs").Value)
	require.Equal(t, int64(512), t1Field(t, api, "config.docker.pidsLimit").Value)
	require.Equal(t, "api", t1Field(t, api, "config.docker.hostname").Value)
	require.Equal(t, []string{"1.1.1.1", "9.9.9.9"}, t1Field(t, api, "config.docker.dns").Value)
	require.Equal(t, map[string]string{"net.core.somaxconn": "1024"}, t1Field(t, api, "config.docker.sysctls").Value)
	require.Equal(t, []agentprotocol.Ulimit{{Name: "nofile", Soft: 65536, Hard: 65536}}, t1Field(t, api, "config.docker.ulimits").Value)
	require.Equal(t, map[string]string{"team": "platform"}, t1Field(t, api, "config.docker.labels").Value)
	require.Equal(t, []agentprotocol.TmpfsMount{{Path: "/tmp", SizeMB: 64, Mode: "1777"}}, t1Field(t, api, "config.docker.tmpfs").Value)
	require.Equal(t, "1000:1000", t1Field(t, api, "config.docker.user").Value)
	require.Equal(t, 128, t1Field(t, api, "config.docker.shmSizeMb").Value)
	require.Equal(t, true, t1Field(t, api, "config.docker.init").Value)
	require.Equal(t, "SIGQUIT", t1Field(t, api, "config.docker.stopSignal").Value)
	require.Equal(t, []string{"/entrypoint.sh"}, t1Field(t, api, "config.docker.entrypoint").Value)
	require.Empty(t, t1IDs(api, "config.docker.devices"), "unrestricted-only keys are not in the fixture")
	require.Empty(t, t1IDs(api, "config.docker.privileged"))
}

func TestStackFixture(t *testing.T) {
	_, f, idx := t1Load(t, "stack.toml")
	res := t1View(t, f, idx, "")
	require.Equal(t, []string{"media", "tracker"}, res.SortedSlugs())
	media := t1Svc(t, res, "media")
	require.Equal(t, KindStack, media.Kind)

	compose, ok := t1Field(t, media, "config.stack.compose").Value.(string)
	require.True(t, ok)
	require.True(t, strings.HasPrefix(compose, "services:\n  web:\n    image: nginx:1.27-alpine\n"), "the text starts right after the opening quotes: %q", compose)
	require.Contains(t, compose, "GREETING: ${GREETING}", "compose interpolation is not a variable macro")
	require.True(t, strings.HasSuffix(compose, "image: valkey/valkey:8\n"))
	require.Equal(t, 420, t1Field(t, media, "config.stack.waitSeconds").Value)
	require.Equal(t, StackResourceValue{Component: "web", MemoryMB: 256, CPUs: 0.5}, t1Field(t, media, "config.stack.resources.web").Value)
	require.Equal(t, StackResourceValue{Component: "cache", MemoryMB: 128}, t1Field(t, media, "config.stack.resources.cache").Value)
	require.Equal(t, Variable{Value: "hello"}, t1Field(t, media, "variables.GREETING").Value)
	require.Equal(t, SecretDecl{Name: "CACHE_PASSWORD", Scope: "runtime"}, t1Field(t, media, "variables.CACHE_PASSWORD#secret").Value)
	require.Equal(t, "tracker", t1Field(t, media, "links.tracker").Value)

	rule, ok := t1Field(t, media, "domains.media.acme.com").Value.(DomainRule)
	require.True(t, ok)
	require.Equal(t, "web", *rule.Component)
	require.Equal(t, 80, *rule.TargetPort)
	require.Empty(t, t1IDs(media, "config.port"), "a stack has no port of its own")
	require.Equal(t, 5, t1Field(t, media, "uptime.failureThreshold").Value)
}

func TestRouteFixture(t *testing.T) {
	_, f, idx := t1Load(t, "route-tcp.toml")
	res := t1View(t, f, idx, "")
	require.Equal(t, []string{"echo", "game", "relay", "voice"}, res.SortedSlugs())

	game := t1Svc(t, res, "game")
	require.Equal(t, KindRoute, game.Kind)
	require.Equal(t, "fra-1", game.ServerName)
	require.Equal(t, "tcp", t1Field(t, game, "config.route.protocol").Value)
	require.Equal(t, 25565, t1Field(t, game, "config.route.listenPort").Value)
	require.Equal(t, []string{"203.0.113.0/24", "198.51.100.7/32"}, t1Field(t, game, "config.route.allowCidrs").Value)
	require.Equal(t, []string{"203.0.113.99/32"}, t1Field(t, game, "config.route.denyCidrs").Value)
	require.Equal(t, 2, t1Field(t, game, "config.route.proxyProtocol").Value)
	require.Equal(t, []UpstreamValue{{Address: "10.0.0.5:25565"}}, t1Field(t, game, "config.route.upstreams").Value)
	require.Empty(t, t1IDs(game, "config.route.openToInternet"))
	require.Equal(t, AppliesStaged, t1Field(t, game, "config.route.listenPort").Applies)

	voice := t1Svc(t, res, "voice")
	require.Equal(t, "udp", t1Field(t, voice, "config.route.protocol").Value)
	require.Equal(t, true, t1Field(t, voice, "config.route.openToInternet").Value)
	require.Empty(t, t1IDs(voice, "config.route.allowCidrs"), "a udp route cannot be restricted")

	relay := t1Svc(t, res, "relay")
	require.Equal(t, []UpstreamValue{{Service: "echo", Port: 80}}, t1Field(t, relay, "config.route.upstreams").Value)
	require.Equal(t, "echo", t1Field(t, relay, "links.echo").Value)
}

func TestDatabaseFixture(t *testing.T) {
	_, f, idx := t1Load(t, "database-redis.toml")
	res := t1View(t, f, idx, "")
	require.Equal(t, []string{"cache", "docs"}, res.SortedSlugs())

	cache := t1Svc(t, res, "cache")
	require.Equal(t, KindDatabase, cache.Kind)
	require.Equal(t, "valkey", t1Field(t, cache, "config.database.engine").Value)
	require.Equal(t, "8", t1Field(t, cache, "config.database.version").Value)
	require.Empty(t, t1IDs(cache, "config.database.databaseName"))
	require.Equal(t, 256, t1Field(t, cache, "config.memoryMb").Value)
	require.Equal(t, 0.5, t1Field(t, cache, "config.cpus").Value)
	require.Equal(t, 60, t1Field(t, cache, "config.stopGraceSeconds").Value)
	require.Equal(t, Variable{Value: "allkeys-lru"}, t1Field(t, cache, "variables.MAXMEMORY_POLICY").Value)
	require.Equal(t, BackupValue{Key: "hourly", DestinationName: "offsite-s3", Cron: "0 * * * *", Timezone: "UTC",
		Enabled: true, RetentionCount: 24, RetentionDays: 7}, t1Field(t, cache, "backups.hourly").Value)
	require.Empty(t, t1IDs(cache, "config.image"), "the image of a database is derived")
	require.Empty(t, t1IDs(cache, "config.port"))

	docs := t1Svc(t, res, "docs")
	require.Equal(t, "mongodb", t1Field(t, docs, "config.database.engine").Value)
	require.Equal(t, "8.0", t1Field(t, docs, "config.database.version").Value)
	require.Equal(t, "catalog", t1Field(t, docs, "config.database.databaseName").Value)
	require.Equal(t, "catalog_rw", t1Field(t, docs, "config.database.username").Value)
	require.True(t, *docs.Def.Database.AcceptLicense)
	require.Empty(t, t1IDs(docs, "config.database.accept"), "accept_license is not stored")
	require.Equal(t, true, t1Field(t, docs, "config.blockEgress").Value)
	require.Equal(t, BackupValue{Key: "nightly", DestinationName: "offsite-s3", Cron: "30 2 * * *", Timezone: "UTC",
		Enabled: true, RetentionCount: 14}, t1Field(t, docs, "backups.nightly").Value)
}

func TestEnvironmentsFixture(t *testing.T) {
	text, f, idx := t1Load(t, "environments.toml")
	require.Equal(t, []string{"production", "staging"}, f.Environments())

	base := t1View(t, f, idx, "")
	require.Equal(t, []string{"canary", "web", "worker"}, base.SortedSlugs(), "only_in is not applied to the base view")
	require.Equal(t, "fra-1", base.Server)
	require.Equal(t, "fra-web", t1Svc(t, base, "web").ServerName)
	require.Equal(t, "fra-1", t1Svc(t, base, "worker").ServerName)
	require.Equal(t, "eu", base.Shared["REGION"].Value)
	require.Equal(t, []string{"SHARED_SIGNING_KEY"}, base.SharedSecrets)
	require.Empty(t, base.EnvShared)
	require.Empty(t, base.EnvSharedSecrets)

	staging := t1View(t, f, idx, "staging")
	require.Equal(t, []string{"canary", "jobs", "web", "worker"}, staging.SortedSlugs(), "canary is in only_in; jobs is defined in the block")
	require.Equal(t, "fra-staging", staging.Server)
	require.Equal(t, "fra-web", t1Svc(t, staging, "web").ServerName, "the service's own server wins")
	for _, slug := range []string{"canary", "jobs", "worker"} {
		require.Equal(t, "fra-staging", t1Svc(t, staging, slug).ServerName, slug)
	}
	require.Equal(t, "services.web.server", t1Field(t, t1Svc(t, staging, "web"), "server").Path)
	require.Equal(t, "environments.staging.server", t1Field(t, t1Svc(t, staging, "worker"), "server").Path)
	// Shared layers are layers, not a merge.
	require.Equal(t, "eu", staging.Shared["REGION"].Value)
	require.Equal(t, "eu-staging", staging.EnvShared["REGION"].Value)
	require.Equal(t, []string{"SHARED_SIGNING_KEY"}, staging.SharedSecrets)
	require.Equal(t, []string{"STAGING_ONLY_KEY"}, staging.EnvSharedSecrets)
	require.Equal(t, "runtime", staging.EnvSharedSecretScope)

	web := t1Svc(t, staging, "web")
	require.Empty(t, t1IDs(web, "domains."), "unset with a quoted dotted segment")
	require.Empty(t, t1IDs(web, "scale."))
	require.NotContains(t, web.Fields, "variables.FEATURE_X")
	require.Equal(t, Variable{Value: "debug"}, t1Field(t, web, "variables.LOG_LEVEL").Value)
	require.Equal(t, t1LineOf(t, text, `LOG_LEVEL = "debug"`), t1Field(t, web, "variables.LOG_LEVEL").Pos.Line)
	t1Field(t, web, "variables.REGION_NAME")
	jobs := t1Svc(t, staging, "jobs")
	require.Equal(t, KindImage, jobs.Kind)
	require.Equal(t, "environments.staging.services.jobs", jobs.Path)
	require.Equal(t, []string{"jobs", "run"}, t1Field(t, jobs, "config.startCommand").Value)

	prod := t1View(t, f, idx, "production")
	require.Equal(t, []string{"web", "worker"}, prod.SortedSlugs(), "canary is only in staging")
	require.Equal(t, "fra-prod", prod.Server)
	require.Equal(t, "fra-prod", t1Svc(t, prod, "worker").ServerName)
	require.Equal(t, "fra-web", t1Svc(t, prod, "web").ServerName)
	require.Empty(t, prod.EnvShared)
	pweb := t1Svc(t, prod, "web")
	replicas := t1Field(t, pweb, "scale.replicas")
	require.Equal(t, 5, replicas.Value)
	require.Equal(t, "environments.production.services.web.scale.replicas", replicas.Path)
	require.Equal(t, t1LineOf(t, text, "replicas = 5"), replicas.Pos.Line)
	require.Equal(t, Variable{Value: "warn"}, t1Field(t, pweb, "variables.LOG_LEVEL").Value)
	require.Equal(t, Variable{Value: "off"}, t1Field(t, pweb, "variables.FEATURE_X").Value)
	require.Equal(t, []string{"domains.app.acme.com", "domains.web.acme.com"}, t1IDs(pweb, "domains."), "tables merge by key")
	require.Equal(t, DomainRule{Host: "app.acme.com"}, t1Field(t, pweb, "domains.app.acme.com").Value)
}

func TestMinimalFixture(t *testing.T) {
	_, f, idx := t1Load(t, "minimal.toml")
	res := t1View(t, f, idx, "")
	require.Equal(t, []string{"hello"}, res.SortedSlugs())
	hello := t1Svc(t, res, "hello")
	require.Equal(t, KindImage, hello.Kind)
	require.Equal(t, []string{"config.image"}, hello.FieldIDs())
	require.Empty(t, hello.ServerName)
	require.Empty(t, res.Server)
	require.Empty(t, res.Project)
}

func TestFixtureHeaderWarn(t *testing.T) {
	require.Equal(t, map[string]bool{"unresolved_reference": true, "invalid_unset_path": true},
		t1WarnHeader([]byte("# warn: unresolved_reference, invalid_unset_path\nschema = 1\n# warn: ignored\n")))
	require.Empty(t, t1WarnHeader([]byte("#:schema x\nschema = 1\n")))
}
