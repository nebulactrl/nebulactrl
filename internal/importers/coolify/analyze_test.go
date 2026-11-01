package coolify

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func fetchInv(t *testing.T, f *fakeCoolify) *Inventory {
	t.Helper()
	cl, err := NewClient(f.url(), fakeToken, nil)
	require.NoError(t, err)
	inv, err := cl.Fetch(context.Background())
	require.NoError(t, err)
	return inv
}

func planOf(t *testing.T, f *fakeCoolify, ch CoolifyChoices, tg Target) *CoolifyPlan {
	t.Helper()
	return Analyze(fetchInv(t, f), ch, tg)
}

func defaultPlan(t *testing.T) *CoolifyPlan {
	t.Helper()
	return planOf(t, newFakeCoolify(t), CoolifyChoices{}, Target{Server: &TargetServer{ID: uuid.New(), Name: "homelab-1"}})
}

func res(t *testing.T, p *CoolifyPlan, name string) *CoolifyPlanResource {
	t.Helper()
	for i := range p.Projects {
		for j := range p.Projects[i].Environments {
			for k := range p.Projects[i].Environments[j].Resources {
				if r := &p.Projects[i].Environments[j].Resources[k]; r.Name == name {
					return r
				}
			}
		}
	}
	t.Fatalf("resource %q not in plan", name)
	return nil
}

func envv(t *testing.T, r *CoolifyPlanResource, key string) CoolifyPlanEnv {
	t.Helper()
	for _, e := range r.Env {
		if e.Key == key {
			return e
		}
	}
	t.Fatalf("variable %q not in %s", key, r.Name)
	return CoolifyPlanEnv{}
}

func hasEnv(r *CoolifyPlanResource, key string) bool {
	for _, e := range r.Env {
		if e.Key == key {
			return true
		}
	}
	return false
}

func TestPlanStructure(t *testing.T) {
	p := defaultPlan(t)
	require.True(t, p.Installable, "%v", p.Blockers)
	require.Equal(t, "4.5.0", p.CoolifyVersion)
	require.Len(t, p.Projects, 2)
	require.Equal(t, "Dashboard", p.Projects[0].Name)
	require.Equal(t, "create", p.Projects[0].Action)
	require.Equal(t, "dashboard", p.Projects[0].Slug)
	require.Equal(t, "shop", p.Projects[1].Slug)
	for _, pr := range p.Projects {
		require.Len(t, pr.Environments, 1)
		require.Equal(t, "production", pr.Environments[0].Slug)
		require.True(t, pr.Environments[0].Production)
	}
	require.Len(t, p.Projects[0].Environments[0].Resources, 2)
	require.Len(t, p.Projects[1].Environments[0].Resources, 5)
	// databases are listed (and so deployed) before applications
	require.Equal(t, "database", p.Projects[1].Environments[0].Resources[0].Kind)
	require.Equal(t, 3, p.Totals.Databases)
	require.Equal(t, 4, p.Totals.Services)
	require.Equal(t, 2, p.Totals.Projects)
	require.Equal(t, 3, p.Totals.Domains, "home, share and shop.example.com")
}

func TestOneClickServicesAreListedNotImported(t *testing.T) {
	p := defaultPlan(t)
	var found *CoolifyPlanUnsupported
	for i := range p.Unsupported {
		if p.Unsupported[i].Kind == "service" {
			found = &p.Unsupported[i]
		}
	}
	require.NotNil(t, found)
	require.Equal(t, "plausible", found.Name)
	require.Equal(t, "shop", found.Project)
	require.Contains(t, found.Reason, "import compose")
}

func TestGitDockerfileApplication(t *testing.T) {
	p := defaultPlan(t)
	r := res(t, p, "Dashboard")
	require.Equal(t, "app", r.Kind)
	require.Equal(t, "git", r.Source.Type)
	g := r.Source.Git
	require.Equal(t, "https://github.com/acme/tracker", g.URL)
	require.Equal(t, "acme/tracker", g.Repo)
	require.Equal(t, "main", g.Branch)
	require.Equal(t, "dockerfile", g.BuildType)
	require.Equal(t, "Dockerfile", g.DockerfilePath, "relative to the repository root, base directory /")
	require.Equal(t, ".", g.BuildContext)
	require.Equal(t, 8080, r.Port)
	require.Equal(t, "none", r.Health.Type, "no health check in Coolify")
	require.Len(t, r.Domains, 2)
	require.Equal(t, "home.example.com", r.Domains[0].Host)
	require.Equal(t, "share.example.com", r.Domains[1].Host)
	require.True(t, r.Domains[1].Noindex)
	require.False(t, r.Domains[0].Noindex)
	require.True(t, r.Domains[0].Compress, "the gzip label")
	require.True(t, r.Domains[0].ForceHTTPS)
	require.True(t, notesContain(r, "git access", "select the connection"))
	require.True(t, anyContains(r.Unsupported, "custom label"))
}

func notesContain(r *CoolifyPlanResource, key, sub string) bool {
	for _, n := range r.Notes {
		if n.Key == key && strings.Contains(n.Message, sub) {
			return true
		}
	}
	return false
}

func anyContains(list []string, sub string) bool {
	for _, s := range list {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

func TestImageApplications(t *testing.T) {
	p := defaultPlan(t)
	web := res(t, p, "shop-web")
	require.Equal(t, "image", web.Source.Type)
	require.Equal(t, "ghcr.io/acme/shop:v0.6.0", web.Source.Image)
	require.Equal(t, "ghcr.io", web.Source.Registry)
	require.Equal(t, 8080, web.Port)
	require.Equal(t, "exec", web.Health.Type, "Coolify's check runs inside the container")
	require.Equal(t, []string{"CMD-SHELL", "curl -fsk -o /dev/null 'http://localhost:8080/up' || wget -q --no-check-certificate -O /dev/null 'http://localhost:8080/up'"}, web.Health.command)
	require.Equal(t, []string{"php", "artisan", "migrate", "--force"}, web.PreDeploy)
	require.Len(t, web.Domains, 1)
	require.Equal(t, "shop.example.com", web.Domains[0].Host)
	require.Equal(t, []CoolifyPlanVolume{{Name: "storage", MountPath: "/var/www/html/storage/app"}}, web.Volumes)
	require.True(t, anyContains(web.Unsupported, "scheduled task schedule"))

	ssr := res(t, p, "shop-ssr")
	require.Equal(t, "app", ssr.Kind, "another service points at it")
	require.Equal(t, 13714, ssr.Port)
	require.Equal(t, "exec", ssr.Health.Type)
	require.Contains(t, ssr.Health.command[1], "'http://localhost:13714/health'")
	require.Empty(t, ssr.Domains)
	require.NotNil(t, ssr.Docker)
	require.Equal(t, []string{"docker-php-serversideup-entrypoint", "bun", "/var/www/html/bootstrap/ssr/ssr.js"}, ssr.Docker.Entrypoint)
	require.Empty(t, ssr.Command, "an entrypoint override drops the image's command")

	worker := res(t, p, "shop-worker")
	require.Equal(t, "worker", worker.Kind, "no domain, no health check, nobody points at it")
	require.Equal(t, 0, worker.Port)
	require.Equal(t, "none", worker.Health.Type)
	require.Equal(t, []string{"docker-php-serversideup-entrypoint", "php", "/var/www/html/artisan", "queue:work", "--tries=3", "--timeout=60"}, worker.Docker.Entrypoint)

	require.Equal(t, []CoolifyPlanRegistry{{Host: "ghcr.io", Username: "acme-bot", Services: []string{"shop-web", "shop-ssr", "shop-worker"}}}, sortedRegs(p.Registries))
}

func sortedRegs(in []CoolifyPlanRegistry) []CoolifyPlanRegistry {
	for i := range in {
		// the order of services follows the plan (web, ssr, worker sorted by name): normalise
		m := map[string]bool{}
		for _, s := range in[i].Services {
			m[s] = true
		}
		in[i].Services = nil
		for _, s := range []string{"shop-web", "shop-ssr", "shop-worker"} {
			if m[s] {
				in[i].Services = append(in[i].Services, s)
			}
		}
	}
	return in
}

func TestManagedDatabases(t *testing.T) {
	p := defaultPlan(t)

	g := res(t, p, "tracker-postgres")
	require.Equal(t, "database", g.Kind)
	require.True(t, g.Include)
	m := g.Source.Managed
	require.Equal(t, "postgres", m.Engine)
	require.Equal(t, "18", m.Version)
	require.Empty(t, m.Flavor)
	require.Equal(t, "tracker", m.Username)
	require.Equal(t, "tracker", m.Database)
	require.True(t, anyNote(m.Notes, "Alpine"), "alpine becomes Debian with a note: %v", m.Notes)
	require.Contains(t, g.Followup, "nebulactrl databases restore tracker-postgres --file")
	require.Contains(t, g.Followup, "pg_dump")

	x := res(t, p, "shop-postgres")
	require.Equal(t, "postgres", x.Source.Managed.Engine)
	require.Equal(t, "postgis", x.Source.Managed.Flavor)
	require.Equal(t, "18", x.Source.Managed.Version)
	require.Equal(t, "postgis/postgis:18-3.6", x.Source.Managed.From)
	require.True(t, anyContains(x.Unsupported, "public port 54321"))
	require.True(t, anyContains(x.Unsupported, "custom configuration"))

	v := res(t, p, "shop-valkey")
	require.Equal(t, "valkey", v.Source.Managed.Engine)
	require.Equal(t, "8", v.Source.Managed.Version)
	require.Empty(t, v.Source.Managed.Username, "key-value engines have no user")
	require.Contains(t, v.Followup, "starts empty")
	require.True(t, anyNote(v.Source.Managed.Notes, "Alpine"))
}

func anyNote(list []string, sub string) bool { return anyContains(list, sub) }

func TestReferencesToDatabasesAndApplications(t *testing.T) {
	p := defaultPlan(t)
	for _, name := range []string{"shop-web", "shop-worker"} {
		r := res(t, p, name)
		want := map[string]string{
			"DB_HOST":         "${{shop-postgres.NEBULA_PRIVATE_HOST}}",
			"DB_PORT":         "${{shop-postgres.DATABASE_PORT}}",
			"DB_DATABASE":     "${{shop-postgres.DATABASE_NAME}}",
			"DB_USERNAME":     "${{shop-postgres.DATABASE_USER}}",
			"DB_PASSWORD":     "${{shop-postgres.DATABASE_PASSWORD}}",
			"DATABASE_URL":    "${{shop-postgres.DATABASE_URL}}",
			"REDIS_HOST":      "${{shop-valkey.NEBULA_PRIVATE_HOST}}",
			"REDIS_PORT":      "${{shop-valkey.REDIS_PORT}}",
			"REDIS_PASSWORD":  "${{shop-valkey.REDIS_PASSWORD}}",
			"REDIS_URL":       "${{shop-valkey.REDIS_URL}}",
			"REDIS_CACHE_URL": "redis://default:${{shop-valkey.REDIS_PASSWORD}}@${{shop-valkey.NEBULA_PRIVATE_HOST}}:6379/1",
			"INERTIA_SSR_URL": "http://${{shop-ssr.NEBULA_PRIVATE_HOST}}:13714",
		}
		for k, v := range want {
			e := envv(t, r, k)
			require.Equal(t, v, e.value, "%s.%s", name, k)
			require.True(t, e.Rewritten, k)
		}
		require.False(t, envv(t, r, "DB_CONNECTION").Rewritten)
		require.Equal(t, "pgsql", envv(t, r, "DB_CONNECTION").value)
		require.Equal(t, []string{"shop-postgres", "shop-ssr", "shop-valkey"}, r.DependsOn)
	}
	// the application that does not use the database keeps its value
	ssr := res(t, p, "shop-ssr")
	require.Equal(t, "13714", envv(t, ssr, "SSR_PORT").value)
	require.Empty(t, ssr.DependsOn)

	dash := res(t, p, "Dashboard")
	require.Equal(t, "${{tracker-postgres.DATABASE_URL}}", envv(t, dash, "DATABASE_URL").value)
	require.Equal(t, []string{"tracker-postgres"}, dash.DependsOn)

	kinds := map[string]bool{}
	for _, rw := range p.Rewrites {
		kinds[rw.Kind] = true
		require.True(t, strings.HasPrefix(rw.To, "${{"))
		require.NotContains(t, rw.To, pgPassword)
	}
	for _, k := range []string{"host", "password", "user", "port", "database", "url"} {
		require.True(t, kinds[k], k)
	}
}

func TestReferenceRewritingCanBeTurnedOff(t *testing.T) {
	f := newFakeCoolify(t)
	off := false
	p := planOf(t, f, CoolifyChoices{Resources: map[string]CoolifyResourceChoice{uuidWeb: {Env: map[string]CoolifyEnvChoice{"DB_HOST": {Rewrite: &off}}}}}, Target{})
	e := envv(t, res(t, p, "shop-web"), "DB_HOST")
	require.Equal(t, uuidPGExp, e.value)
	require.False(t, e.Rewritten)
}

func TestHostsAreMatchedOnWholeNamesOnly(t *testing.T) {
	c := &refCtx{targets: []*target{{res: &CoolifyPlanResource{Slug: "api"}, hosts: []string{"abc123"}}}, linked: map[*target]bool{}}
	for in, want := range map[string]int{
		"http://abc123:80":          1,
		"abc123":                    1,
		"http://abc1234:80":         0,
		"http://x-abc123:80":        0,
		"http://abc123.example.com": 0,
		"host=abc123;port=1":        1,
	} {
		hits, _ := c.scan("URL", in, &CoolifyPlanResource{}, true)
		require.Len(t, hits, want, in)
	}
}

func TestSecretsAreMarked(t *testing.T) {
	p := defaultPlan(t)
	web := res(t, p, "shop-web")
	secret := func(k string) bool { return envv(t, web, k).Secret }
	for _, k := range []string{"APP_KEY", "MAIL_PASSWORD", "AWS_SECRET_ACCESS_KEY", "DB_PASSWORD", "DATABASE_URL", "REDIS_PASSWORD", "REDIS_URL", "REDIS_CACHE_URL", "LOCKED_SECRET"} {
		require.True(t, secret(k), k)
	}
	for _, k := range []string{"APP_NAME", "APP_URL", "DB_HOST", "MAIL_USERNAME", "AWS_ACCESS_KEY_ID", "AUTORUN_LARAVEL_MIGRATION", "INERTIA_SSR_URL"} {
		require.False(t, secret(k), k)
	}
	require.Equal(t, SecretName, envv(t, web, "APP_KEY").SecretReason)
	require.Equal(t, SecretValue, envv(t, web, "DATABASE_URL").SecretReason)
	require.Equal(t, SecretLocked, envv(t, web, "LOCKED_SECRET").SecretReason)

	dash := res(t, p, "Dashboard")
	require.True(t, envv(t, dash, "TRACKER_MASTER_KEY").Secret)
	require.True(t, envv(t, dash, "OIDC_CLIENT_SECRET").Secret)
	require.True(t, envv(t, dash, "VAPID_PRIVATE_KEY").Secret)
	require.False(t, envv(t, dash, "VAPID_PUBLIC_KEY").Secret)
	require.False(t, envv(t, dash, "S3_ENDPOINT").Secret)
}

func TestSecretChoiceOverridesHeuristics(t *testing.T) {
	f := newFakeCoolify(t)
	yes, no := true, false
	p := planOf(t, f, CoolifyChoices{Resources: map[string]CoolifyResourceChoice{uuidWeb: {Env: map[string]CoolifyEnvChoice{
		"APP_NAME": {Secret: &yes}, "APP_KEY": {Secret: &no},
	}}}}, Target{})
	web := res(t, p, "shop-web")
	require.True(t, envv(t, web, "APP_NAME").Secret)
	require.Equal(t, SecretChoice, envv(t, web, "APP_NAME").SecretReason)
	require.False(t, envv(t, web, "APP_KEY").Secret)
}

func TestVariablesNotImported(t *testing.T) {
	p := defaultPlan(t)
	web := res(t, p, "shop-web")
	require.False(t, hasEnv(web, "PREVIEW_ONLY"), "preview variables are never imported")
	for _, k := range []string{"PORT", "COOLIFY_FQDN", "SERVICE_FQDN_WEB"} {
		e := envv(t, web, k)
		require.True(t, e.Skipped, k)
		require.NotEmpty(t, e.Note, k)
	}
	locked := envv(t, web, "LOCKED_SECRET")
	require.True(t, locked.Skipped)
	require.True(t, locked.NeedsValue)
	require.True(t, notesContain(web, "locked variables", "LOCKED_SECRET"))
}

func TestLockedVariableValueCanBeSupplied(t *testing.T) {
	f := newFakeCoolify(t)
	v := explicitCanary
	p := planOf(t, f, CoolifyChoices{Resources: map[string]CoolifyResourceChoice{uuidWeb: {Env: map[string]CoolifyEnvChoice{"LOCKED_SECRET": {Value: &v}}}}}, Target{})
	e := envv(t, res(t, p, "shop-web"), "LOCKED_SECRET")
	require.False(t, e.Skipped)
	require.True(t, e.Secret)
	require.Equal(t, explicitCanary, e.value)
	require.False(t, e.NeedsValue)
}

func TestBuildTimeVariables(t *testing.T) {
	p := defaultPlan(t)
	// An image service has no build: build flags fall back to runtime.
	web := res(t, p, "shop-web")
	require.Equal(t, "runtime", envv(t, web, "VITE_APP_NAME").Scope)
	require.Equal(t, "runtime", envv(t, web, "NODE_ENV").Scope)
	require.Contains(t, envv(t, web, "NODE_ENV").Note, "Build-time only")
	// A git service keeps them.
	dash := res(t, p, "Dashboard")
	require.Equal(t, "both", envv(t, dash, "DATABASE_URL").Scope)
	require.Equal(t, "runtime", envv(t, dash, "TRACKER_PUBLIC_URL").Scope)
	require.True(t, notesContain(dash, "build variables", "BuildKit"))
}

func TestLiteralBracesAreEscaped(t *testing.T) {
	p := defaultPlan(t)
	e := envv(t, res(t, p, "shop-web"), "LITERAL_BRACES")
	require.Equal(t, "price $${{ 5 }} usd", e.value)
	_, err := validTemplate(e.value)
	require.NoError(t, err)
}

func TestEveryRenderedValueIsAValidTemplate(t *testing.T) {
	p := defaultPlan(t)
	for _, pr := range p.Projects {
		for _, pe := range pr.Environments {
			for _, r := range pe.Resources {
				for _, e := range r.Env {
					if e.Skipped {
						continue
					}
					_, err := validTemplate(e.value)
					require.NoError(t, err, "%s.%s", r.Name, e.Key)
				}
			}
		}
	}
}

func TestPlanNeverContainsSecrets(t *testing.T) {
	p := defaultPlan(t)
	raw, err := json.Marshal(p)
	require.NoError(t, err)
	body := string(raw)
	for _, canary := range []string{fakeToken, pgPassword, pgPassword2, valkeyPassword, appKeyValue, mailPassword, lockedHidden, s3Secret, "mk-canary-1", "oidc-canary", "priv-key-canary"} {
		require.NotContains(t, body, canary)
	}
	// nor the Coolify UUIDs of databases inside values: only references remain
	require.NotContains(t, body, "postgres://")
}

func TestWithoutReadSensitiveTheImportIsBlocked(t *testing.T) {
	f := newFakeCoolify(t)
	f.sensitive = false
	p := planOf(t, f, CoolifyChoices{}, Target{})
	require.False(t, p.Installable)
	require.Contains(t, strings.Join(p.Blockers, " "), "read:sensitive")
	var check *CoolifyCheck
	for i := range p.Checks {
		if p.Checks[i].ID == "sensitive" {
			check = &p.Checks[i]
		}
	}
	require.NotNil(t, check)
	require.Equal(t, CheckFail, check.Status)
}

func TestCollidingProjectGetsASuffix(t *testing.T) {
	f := newFakeCoolify(t)
	existing := uuid.New()
	tg := Target{Projects: []TargetProject{{ID: existing, Name: "shop", Slug: "shop", Envs: []TargetEnv{{ID: uuid.New(), Name: "production", Slug: "production", Production: true, ServiceSlugs: []string{"shop-web"}}}}}}
	p := planOf(t, f, CoolifyChoices{}, tg)
	pp := p.Projects[1]
	require.True(t, pp.Collision)
	require.Equal(t, "create", pp.Action)
	require.Equal(t, "shop (Coolify)", pp.Name)
	require.Equal(t, "shop-coolify", pp.Slug)

	// the reviewer renames it
	name := "Experts"
	p = planOf(t, f, CoolifyChoices{Projects: map[string]CoolifyProjectChoice{uuidProjExp: {Name: &name}}}, tg)
	require.Equal(t, "Experts", p.Projects[1].Name)
	require.Equal(t, "experts", p.Projects[1].Slug)
}

func TestImportIntoAnExistingProject(t *testing.T) {
	f := newFakeCoolify(t)
	pid, eid := uuid.New(), uuid.New()
	tg := Target{Projects: []TargetProject{{ID: pid, Name: "Prod", Slug: "prod", Envs: []TargetEnv{{ID: eid, Name: "Production", Slug: "production", Production: true, ServiceSlugs: []string{"shop-web"}}}}}}
	p := planOf(t, f, CoolifyChoices{Projects: map[string]CoolifyProjectChoice{uuidProjExp: {ExistingProjectID: &pid}}}, tg)
	pp := p.Projects[1]
	require.Equal(t, "existing", pp.Action)
	require.Equal(t, "prod", pp.Slug)
	require.Equal(t, "existing", pp.Environments[0].Action)
	require.NotNil(t, pp.Environments[0].existingID)
	// the slug of the service that is already there is taken: the import never overwrites
	require.Equal(t, "shop-web-2", res(t, p, "shop-web").Slug)
	require.Equal(t, "${{shop-postgres.NEBULA_PRIVATE_HOST}}", envv(t, res(t, p, "shop-web"), "DB_HOST").value)

	bogus := uuid.New()
	p = planOf(t, f, CoolifyChoices{Projects: map[string]CoolifyProjectChoice{uuidProjExp: {ExistingProjectID: &bogus}}}, tg)
	require.False(t, p.Installable)
}

func TestSkipAndExcludeChoices(t *testing.T) {
	f := newFakeCoolify(t)
	yes, no := true, false
	name := "renamed-worker"
	p := planOf(t, f, CoolifyChoices{
		Projects:  map[string]CoolifyProjectChoice{uuidProjDsh: {Skip: &yes}},
		Resources: map[string]CoolifyResourceChoice{uuidSSR: {Include: &no}, uuidWorker: {Name: &name}},
	}, Target{})
	require.Equal(t, "skip", p.Projects[0].Action)
	require.Empty(t, p.Projects[0].Environments)
	require.Equal(t, 1, p.Totals.Projects)
	ssr := res(t, p, "shop-ssr")
	require.False(t, ssr.Include)
	require.Equal(t, "renamed-worker", res(t, p, "renamed-worker").Slug)
	// pointing at a service that is not imported is reported, not rewritten
	web := res(t, p, "shop-web")
	require.Equal(t, "http://"+uuidSSR+":13714", envv(t, web, "INERTIA_SSR_URL").value)
	require.Contains(t, envv(t, web, "INERTIA_SSR_URL").Note, "not imported")
	found := false
	for _, u := range p.Unsupported {
		if u.Name == "shop-ssr" {
			found = true
		}
	}
	require.True(t, found)
}

func TestPortAndKindChoices(t *testing.T) {
	f := newFakeCoolify(t)
	port := 9000
	worker := "worker"
	p := planOf(t, f, CoolifyChoices{Resources: map[string]CoolifyResourceChoice{uuidWorker: {Port: &port}, uuidWeb: {Kind: &worker}}}, Target{})
	require.Equal(t, "app", res(t, p, "shop-worker").Kind)
	require.Equal(t, 9000, res(t, p, "shop-worker").Port)
	w := res(t, p, "shop-web")
	require.Equal(t, "worker", w.Kind)
	require.Equal(t, 0, w.Port)
}

func TestDomainChoicesAndConflicts(t *testing.T) {
	f := newFakeCoolify(t)
	off := false
	host := "staging.example.com"
	tg := Target{HostTaken: func(h string) bool { return h == "share.example.com" }}
	p := planOf(t, f, CoolifyChoices{Resources: map[string]CoolifyResourceChoice{uuidDash: {Domains: map[string]CoolifyDomainChoice{"home.example.com": {Host: &host}}}, uuidWeb: {Domains: map[string]CoolifyDomainChoice{"shop.example.com": {Enabled: &off}}}}}, tg)
	d := res(t, p, "Dashboard")
	require.Equal(t, "staging.example.com", d.Domains[0].Host)
	require.True(t, d.Domains[0].Enabled)
	require.True(t, d.Domains[1].Conflict)
	require.False(t, d.Domains[1].Enabled)
	require.False(t, res(t, p, "shop-web").Domains[0].Enabled)
}

func TestDomainShapes(t *testing.T) {
	f := newFakeCoolify(t)
	f.extraApps = []map[string]any{{
		"id": 9, "uuid": "extra000000000000000000x", "name": "Extra", "environment_id": 1, "build_pack": "dockerimage", "docker_registry_image_name": "traefik/whoami",
		"docker_registry_image_tag": "latest", "ports_exposes": "80", "fqdn": "http://a.example.com:8081/api/,https://xyz.167.1.1.1.sslip.io,https://a.example.com:8081/api",
	}}
	p := planOf(t, f, CoolifyChoices{}, Target{})
	d := res(t, p, "Extra").Domains
	require.Len(t, d, 2, "the duplicate is dropped")
	require.Equal(t, "a.example.com", d[0].Host)
	require.Equal(t, "/api", d[0].Path)
	require.Equal(t, 8081, d[0].Port)
	require.False(t, d[0].ForceHTTPS)
	require.True(t, d[1].Generate)
}

func TestRegistryCredentialsNeedAnAnswer(t *testing.T) {
	f := newFakeCoolify(t)
	p := planOf(t, f, CoolifyChoices{}, Target{})
	require.False(t, p.Registries[0].Provided)
	require.Contains(t, strings.Join(p.Warnings, "\n"), "ghcr.io")
	p = planOf(t, f, CoolifyChoices{Registries: map[string]CoolifyRegistryAuth{"ghcr.io": {Username: "u", Password: ghcrCanaryToken}}}, Target{})
	require.True(t, p.Registries[0].Provided)
	raw, _ := json.Marshal(p)
	require.NotContains(t, string(raw), ghcrCanaryToken)
}

func TestVolumesAndFollowupsAreReported(t *testing.T) {
	p := defaultPlan(t)
	text := strings.Join(p.Followups, "\n")
	require.Contains(t, text, "volume storage")
	require.Contains(t, text, "DNS still points at Coolify")
	require.Contains(t, text, "nebulactrl databases restore shop-postgres --file")
	require.Contains(t, text, "database-backups.md#restore-from-a-dump-file")
}

func TestDatabaseVersions(t *testing.T) {
	cases := []struct {
		image, wantEngine, wantVersion, wantFlavor string
		unsupported                                bool
	}{
		{image: "postgres:15", wantEngine: "postgres", wantVersion: "16"},
		{image: "postgres:17.2-alpine", wantEngine: "postgres", wantVersion: "17"},
		{image: "postgres:latest", wantEngine: "postgres", wantVersion: "18"},
		{image: "postgis/postgis:16-3.4", wantEngine: "postgres", wantVersion: "16", wantFlavor: "postgis"},
		{image: "mysql:8.0", wantEngine: "mysql", wantVersion: "8.4"},
		{image: "mariadb:10.11", wantEngine: "mariadb", wantVersion: "11.8"},
		{image: "redis:7.2", wantEngine: "redis", wantVersion: "8"},
		{image: "eqalpha/keydb:latest", wantEngine: "valkey", wantVersion: "9"},
		{image: "docker.dragonflydb.io/dragonflydb/dragonfly:v1.20", wantEngine: "valkey", wantVersion: "9"},
		{image: "mongo:7", wantEngine: "mongodb", wantVersion: "7.0"},
		{image: "clickhouse/clickhouse-server:24.8", wantEngine: "clickhouse", wantVersion: "25.8"},
		{image: "bitnami/postgresql:16", unsupported: true},
		{image: "timescale/timescaledb:latest-pg16", unsupported: true},
		{image: "postgres:99", unsupported: true},
	}
	for _, c := range cases {
		d := &Database{UUID: "u", Name: "db", Type: "standalone-postgresql", Image: c.image, User: "app", DBName: "app", SensitiveVisible: true}
		a := &analyzer{tg: Target{}}
		r := a.planDatabase(d, CoolifyResourceChoice{}, nil)
		if c.unsupported {
			require.False(t, r.Include, c.image)
			require.NotEmpty(t, r.IncludeNote, c.image)
			continue
		}
		// source-available engines are opt-in
		require.Equal(t, c.wantEngine, r.Source.Managed.Engine, c.image)
		require.Equal(t, c.wantVersion, r.Source.Managed.Version, c.image)
		require.Equal(t, c.wantFlavor, r.Source.Managed.Flavor, c.image)
	}
}

func TestSourceAvailableEnginesAreOptIn(t *testing.T) {
	d := &Database{UUID: "u", Name: "cache", Type: "standalone-redis", Image: "redis:7", SensitiveVisible: true}
	a := &analyzer{}
	r := a.planDatabase(d, CoolifyResourceChoice{}, nil)
	require.False(t, r.Include)
	require.Contains(t, r.IncludeNote, "licence")
	yes := true
	r = a.planDatabase(d, CoolifyResourceChoice{Include: &yes}, nil)
	require.True(t, r.Include)
}

func TestSQLiteIsNotImported(t *testing.T) {
	d := &Database{UUID: "u", Name: "lite", Type: "standalone-sqlite"}
	r := (&analyzer{}).planDatabase(d, CoolifyResourceChoice{}, nil)
	require.False(t, r.Include)
	require.Contains(t, r.IncludeNote, "files")
}

func TestHealthCheckMappings(t *testing.T) {
	mk := func(mod func(*Application)) *CoolifyPlanResource {
		app := &Application{UUID: "a", Name: "a", BuildPack: "dockerimage", RegistryImage: "x/y", RegistryTag: "1", PortsExposes: "3000", SensitiveVisible: true,
			Health: Health{Enabled: true, Type: "http", Path: "ready", Interval: 10, Timeout: 4, Retries: 5, StartPeriod: 20, ReturnCode: 204, Method: "HEAD", Scheme: "https", ResponseText: "ok"}}
		mod(app)
		a := &analyzer{}
		r := a.planApplication(app, CoolifyResourceChoice{}, nil)
		return &r
	}
	r := mk(func(*Application) {})
	require.Equal(t, "exec", r.Health.Type)
	require.Equal(t, []string{"CMD-SHELL", "curl -fsk -o /dev/null 'https://localhost:3000/ready' || wget -q --no-check-certificate -O /dev/null 'https://localhost:3000/ready'"}, r.Health.command,
		"Coolify's own request: its scheme, localhost, the service port, the path with a leading slash")
	require.Equal(t, 10, r.Health.IntervalSeconds)
	require.Equal(t, 4, r.Health.TimeoutSeconds)
	require.Equal(t, 20, r.Health.StartPeriodSeconds)
	require.Equal(t, 70, r.Health.DeadlineSeconds, "start period + retries × interval")
	for _, sub := range []string{"204", "HEAD", "response text"} {
		found := false
		for _, n := range r.Notes {
			if n.Key == "health check" && strings.Contains(n.Message, sub) {
				found = true
			}
		}
		require.True(t, found, sub)
	}
	r = mk(func(a *Application) { a.Health.Port, a.Health.Host, a.Health.Path = "9000", "app.internal", "/it's" })
	require.Contains(t, r.Health.command[1], `'https://app.internal:9000/it'\''s'`, "Coolify's health_check_host and port; the URL is shell-quoted")
	r = mk(func(a *Application) { a.PortsExposes = "" })
	require.Equal(t, "none", r.Health.Type, "no port to check")
	r = mk(func(a *Application) { a.Health.Type, a.Health.Command = "cmd", "pg_isready -U app" })
	require.Equal(t, "exec", r.Health.Type)
	require.Equal(t, []string{"CMD-SHELL", "pg_isready -U app"}, r.Health.command)
	r = mk(func(a *Application) { a.Health.Enabled, a.CustomHealthcheckFound = false, true })
	require.Equal(t, "docker", r.Health.Type)
}

func TestBuildPacks(t *testing.T) {
	mk := func(pack string, mod func(*Application)) *CoolifyPlanResource {
		app := &Application{UUID: "a", Name: "a", BuildPack: pack, GitRepository: "https://gitlab.com/g/p.git", GitBranch: "dev", PortsExposes: "3000", BaseDirectory: "/apps/web",
			DockerfileLocation: "/docker/Dockerfile", InstallCommand: "npm ci", BuildCommand: "npm run build", StartCommand: "npm start", PublishDirectory: "/dist", SensitiveVisible: true}
		if mod != nil {
			mod(app)
		}
		r := (&analyzer{}).planApplication(app, CoolifyResourceChoice{}, nil)
		return &r
	}
	df := mk("dockerfile", nil)
	require.Equal(t, "dockerfile", df.Source.Git.BuildType)
	require.Equal(t, "apps/web/docker/Dockerfile", df.Source.Git.DockerfilePath)
	require.Equal(t, "apps/web", df.Source.Git.BuildContext)
	require.Equal(t, "https://gitlab.com/g/p", df.Source.Git.URL)
	require.Equal(t, "g/p", df.Source.Git.Repo)
	require.Equal(t, "dev", df.Source.Git.Branch)

	nx := mk("nixpacks", nil)
	require.Equal(t, "railpack", nx.Source.Git.BuildType)
	require.Equal(t, "apps/web", nx.Source.Git.BuildContext)
	require.True(t, notesContain(nx, "build pack", "Nixpacks"))
	st := mk("static", nil)
	require.Equal(t, "railpack", st.Source.Git.BuildType)

	pinned := mk("nixpacks", func(a *Application) { a.GitCommitSHA = "0123456789abcdef" })
	require.True(t, notesContain(pinned, "git commit", "pinned"))
	deployKey := mk("dockerfile", func(a *Application) { a.PrivateKeyID = 3 })
	require.True(t, notesContain(deployKey, "git access", "deploy key"))

	compose := mk("dockercompose", nil)
	require.False(t, compose.Include)
	require.Contains(t, compose.IncludeNote, "Compose")
	inline := mk("dockerfile", func(a *Application) { a.GitRepository, a.InlineDockerfile = "", true })
	require.False(t, inline.Include)
	require.Contains(t, inline.IncludeNote, "stored inside Coolify")
}

func TestResourceLimits(t *testing.T) {
	app := &Application{UUID: "a", Name: "a", BuildPack: "dockerimage", RegistryImage: "x/y", PortsExposes: "80", LimitsMemory: "512m", LimitsCPUs: "1.5",
		LimitsMemorySwap: "1g", LimitsCPUShares: 512, SensitiveVisible: true}
	r := (&analyzer{}).planApplication(app, CoolifyResourceChoice{}, nil)
	require.Equal(t, 512, r.MemoryMB)
	require.Equal(t, 1.5, r.CPUs)
	require.True(t, anyContains(r.Unsupported, "memory swap limit"))
	require.True(t, anyContains(r.Unsupported, "CPU shares 512"))
	require.True(t, notesContain(&r, "image", "latest"))
	require.Equal(t, "x/y:latest", r.Source.Image)
}

func TestInlineDockerOptionsNeedAServer(t *testing.T) {
	// privileged needs an unrestricted server: flagged, never silently dropped
	app := &Application{UUID: "a", Name: "a", BuildPack: "dockerimage", RegistryImage: "x/y", RegistryTag: "1", PortsExposes: "80", SensitiveVisible: true,
		CustomDockerRunOptions: "--privileged --cap-add NET_ADMIN --init"}
	restricted := (&analyzer{tg: Target{Server: &TargetServer{}}}).planApplication(app, CoolifyResourceChoice{}, nil)
	require.Contains(t, restricted.NeedsUnrestricted, "privileged")
	require.NotNil(t, restricted.Docker)
	require.False(t, restricted.Docker.Privileged)
	require.True(t, restricted.Docker.Init)
	require.True(t, notesContain(&restricted, "privileged", "unrestricted server"))
	open := (&analyzer{tg: Target{Server: &TargetServer{Unrestricted: true}}}).planApplication(app, CoolifyResourceChoice{}, nil)
	require.True(t, open.Docker.Privileged)
	require.Equal(t, []string{"NET_ADMIN"}, open.Docker.CapAdd)
}
