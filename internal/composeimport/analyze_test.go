package composeimport

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

func analyze(t *testing.T, src ComposeSource, ch ComposeChoices) *Result {
	t.Helper()
	r, err := Analyze(src, ch, Context{})
	require.NoError(t, err)
	return r
}

func rewritesOf(p ComposePlan, service, key string) []ComposeRewrite {
	var out []ComposeRewrite
	for _, r := range p.Rewrites {
		if r.Service == service && r.Key == key {
			out = append(out, r)
		}
	}
	return out
}

func noteKeys(s ComposePlanService) string {
	var keys []string
	for _, n := range s.Notes {
		keys = append(keys, n.Key)
	}
	return strings.Join(keys, ",")
}

func TestN8N(t *testing.T) {
	r := analyze(t, loadFixture(t, "n8n"), ComposeChoices{})
	p := r.Plan()
	require.True(t, p.Installable, "%v", p.Blockers)
	require.Len(t, p.Services, 2)

	pg := svc(t, p, "postgres")
	require.NotNil(t, pg.Managed)
	require.False(t, pg.Managed.Eligible)
	require.Contains(t, pg.Managed.Reason, "POSTGRES_NON_ROOT_USER")
	require.Equal(t, "image", pg.Source.Type)
	require.Equal(t, 5432, pg.Port)
	require.False(t, pg.PortHTTP)
	require.Equal(t, "exec", pg.Health.Type)
	require.Equal(t, []string{"CMD-SHELL", "pg_isready -h localhost -U changeUser -d n8n"}, pg.Health.Command)
	require.Equal(t, []ComposePlanVolume{{Name: "db-storage", Path: "/var/lib/postgresql/data", From: "volume"}}, pg.Volumes)
	require.Len(t, pg.Files, 1)
	require.Equal(t, "/docker-entrypoint-initdb.d/init-data.sh", pg.Files[0].Path)
	require.Equal(t, "0755", pg.Files[0].Mode)
	require.Contains(t, r.plan.Services[0].Files[0].content, "CREATE USER")
	require.Empty(t, pg.Domains)

	n8n := svc(t, p, "n8n")
	require.Equal(t, 5678, n8n.Port)
	require.True(t, n8n.PortHTTP)
	require.Equal(t, "app", n8n.Kind)
	require.Equal(t, []string{"postgres"}, n8n.DependsOn)
	require.Equal(t, []ComposePlanDomain{{Generate: true, Enabled: true, Source: "suggested"}}, n8n.Domains)

	host, _ := envOf(n8n, "DB_POSTGRESDB_HOST")
	require.Equal(t, "${{postgres.NEBULA_PRIVATE_HOST}}", host.Value)
	require.True(t, host.Rewritten)
	require.Len(t, rewritesOf(p, "n8n", "DB_POSTGRESDB_HOST"), 1)

	// Secrets are flagged and never returned; the stored value is still there.
	for _, k := range []string{"DB_POSTGRESDB_PASSWORD", "N8N_ENCRYPTION_KEY"} {
		e, ok := envOf(n8n, k)
		require.True(t, ok, k)
		require.True(t, e.Secret, k)
		require.Empty(t, e.Value, k)
	}
	pw, _ := envOf(r.plan.Services[1], "DB_POSTGRESDB_PASSWORD")
	require.Equal(t, "changePassword", pw.raw)
	web, _ := envOf(n8n, "WEBHOOK_URL")
	require.Equal(t, "https://n8n.example.com/", web.Value, "the ${N8N_HOST:-…} default applies")
	user, _ := envOf(n8n, "DB_POSTGRESDB_USER")
	require.Equal(t, "changeUser", user.Value)
	require.False(t, user.Secret)

	// A managed database cannot be forced when the service is not eligible.
	yes := true
	p2 := analyze(t, loadFixture(t, "n8n"), ComposeChoices{Services: map[string]ComposeServiceChoice{"postgres": {ManagedDatabase: &yes}}}).Plan()
	require.Equal(t, "image", svc(t, p2, "postgres").Source.Type)
}

func TestPlausible(t *testing.T) {
	r := analyze(t, loadFixture(t, "plausible"), ComposeChoices{})
	p := r.Plan()
	require.True(t, p.Installable, "%v", p.Blockers)

	db := svc(t, p, "plausible_db")
	require.Equal(t, "plausible-db", db.Slug)
	require.Equal(t, "managed", db.Source.Type)
	require.Equal(t, "database", db.Kind)
	require.True(t, db.Managed.Enabled)
	require.Equal(t, "postgres", db.Managed.Engine)
	require.Equal(t, "16", db.Managed.Version, "postgres:16-alpine maps to the managed 16")
	require.Equal(t, "postgres", db.Managed.Username)
	require.Equal(t, "postgres", db.Managed.Database, "the postgres image names the database after the user")
	pw, _ := envOf(db, "POSTGRES_PASSWORD")
	require.True(t, pw.Skipped, "a managed database owns its credentials")

	ch := svc(t, p, "plausible_events_db")
	require.False(t, ch.Managed.Eligible)
	require.Contains(t, ch.Managed.Reason, "24.3.3.102-alpine")
	require.Equal(t, "image", ch.Source.Type)
	require.Len(t, ch.Files, 2, "the bind-mounted config files are attached")
	require.Equal(t, []agentprotocol.Ulimit{{Name: "nofile", Soft: 262144, Hard: 262144}}, ch.Docker.Ulimits, "ulimits become Docker options")
	require.Equal(t, "exec", ch.Health.Type)
	require.Equal(t, 60, ch.Health.StartPeriodSeconds)

	app := svc(t, p, "plausible")
	require.ElementsMatch(t, []string{"mail", "plausible_db", "plausible_events_db"}, app.DependsOn)
	require.Equal(t, 8000, app.Port)
	require.Equal(t, []string{"sh", "-c", "/entrypoint.sh db createdb && /entrypoint.sh db migrate && /entrypoint.sh run"}, app.Command)

	// env_file entries are read; the database URL points at the managed database.
	r.plan.Services[3].Env = r.plan.Services[3].Env[:len(r.plan.Services[3].Env):len(r.plan.Services[3].Env)]
	var stored string
	for _, e := range r.plan.Services[3].Env {
		if e.Key == "DATABASE_URL" {
			stored = e.raw
		}
	}
	require.Equal(t, "postgres://postgres:${{plausible-db.DATABASE_PASSWORD}}@${{plausible-db.NEBULA_PRIVATE_HOST}}:5432/plausible_db", stored)
	du, _ := envOf(app, "DATABASE_URL")
	require.True(t, du.Secret)
	require.Empty(t, du.Value)
	smtp, _ := envOf(app, "SMTP_HOST_ADDR")
	require.Equal(t, "${{mail.NEBULA_PRIVATE_HOST}}", smtp.Value)
	chURL, _ := envOf(app, "CLICKHOUSE_DATABASE_URL")
	require.Equal(t, "http://${{plausible-events-db.NEBULA_PRIVATE_HOST}}:8123/plausible_events_db", chURL.Value)
	for _, k := range []string{"SECRET_KEY_BASE", "TOTP_VAULT_KEY"} {
		e, _ := envOf(app, k)
		require.True(t, e.Secret, k)
	}
	// No password appears anywhere in the review.
	for _, rw := range p.Rewrites {
		require.NotContains(t, rw.From, "postgres:postgres@", rw.Key)
		require.NotContains(t, rw.To, ":postgres@", rw.Key)
	}

	// Opting out keeps a plain image service and the literal credentials.
	no := false
	p2 := analyze(t, loadFixture(t, "plausible"), ComposeChoices{Services: map[string]ComposeServiceChoice{"plausible_db": {ManagedDatabase: &no}}}).Plan()
	db2 := svc(t, p2, "plausible_db")
	require.Equal(t, "image", db2.Source.Type)
	require.Equal(t, "postgres:16-alpine", db2.Source.Image)
	require.Equal(t, 5432, db2.Port)
	du2, _ := envOf(svc(t, p2, "plausible"), "DATABASE_URL")
	require.True(t, du2.Rewritten)
	require.Empty(t, rewritesOfKind(p2, "password"))
}

func rewritesOfKind(p ComposePlan, kind string) []ComposeRewrite {
	var out []ComposeRewrite
	for _, r := range p.Rewrites {
		if r.Kind == kind {
			out = append(out, r)
		}
	}
	return out
}

func TestImmich(t *testing.T) {
	src := loadFixture(t, "immich")
	r := analyze(t, src, ComposeChoices{})
	p := r.Plan()
	require.True(t, p.Installable, "%v", p.Blockers)
	require.Equal(t, "immich", p.Name, "the project name titles the group")

	redis := svc(t, p, "redis")
	require.Equal(t, "managed", redis.Source.Type)
	require.Equal(t, "valkey", redis.Managed.Engine)
	require.Equal(t, "8", redis.Managed.Version, "a digest-pinned 8-bookworm tag maps to Valkey 8")

	srv := svc(t, p, "immich-server")
	require.Equal(t, 2283, srv.Port)
	require.Equal(t, "ghcr.io/immich-app/immich-server:v2", srv.Source.Image, "${IMMICH_VERSION} comes from .env")
	var up, tz *ComposePlanBind
	for i := range srv.Binds {
		switch srv.Binds[i].Target {
		case "/usr/src/app/upload":
			up = &srv.Binds[i]
		case "/etc/localtime":
			tz = &srv.Binds[i]
		}
	}
	require.NotNil(t, up)
	require.Equal(t, "rejected", up.Status)
	require.True(t, up.CanConvert)
	require.Equal(t, "ignored", tz.Status)
	// .env is both the interpolation file and the env_file of the service.
	e, ok := envOf(srv, "DB_PASSWORD")
	require.True(t, ok)
	require.True(t, e.Secret)
	_, ok = envOf(srv, "UPLOAD_LOCATION")
	require.True(t, ok)

	dbs := svc(t, p, "database")
	require.Nil(t, dbs.Managed, "an unofficial postgres image is a plain image")
	require.Equal(t, 5432, dbs.Port)
	require.Equal(t, 128, dbs.Docker.ShmSizeMB, "shm_size becomes a Docker option")

	// Converting the directory bind makes a named volume.
	conv := analyze(t, src, ComposeChoices{Services: map[string]ComposeServiceChoice{"immich-server": {ConvertBinds: []string{"/usr/src/app/upload"}}}}).Plan()
	s2 := svc(t, conv, "immich-server")
	require.Len(t, s2.Volumes, 1)
	require.Equal(t, "/usr/src/app/upload", s2.Volumes[0].Path)
	require.Equal(t, "bind", s2.Volumes[0].From)
	require.Equal(t, "library", s2.Volumes[0].Name)
}

func TestWordpress(t *testing.T) {
	r := analyze(t, loadFixture(t, "wordpress"), ComposeChoices{})
	p := r.Plan()
	require.True(t, p.Installable, "%v", p.Blockers)
	db := svc(t, p, "db")
	require.False(t, db.Managed.Eligible)
	require.Contains(t, db.Managed.Reason, "8.0 is not offered")
	require.Equal(t, 3306, db.Port, "the first exposed port")
	require.False(t, db.PortHTTP)
	require.Equal(t, "tcp", db.Health.Type)
	require.Equal(t, []string{"--default-authentication-plugin=mysql_native_password"}, db.Command)

	wp := svc(t, p, "wordpress")
	require.Equal(t, 80, wp.Port)
	h, _ := envOf(wp, "WORDPRESS_DB_HOST")
	require.Equal(t, "${{db.NEBULA_PRIVATE_HOST}}", h.Value)
	user, _ := envOf(wp, "WORDPRESS_DB_USER")
	require.Equal(t, "wordpress", user.Value, "a user name is not rewritten")
	require.Len(t, wp.Files, 1)
	require.Equal(t, "/usr/local/etc/php/conf.d/uploads.ini", wp.Files[0].Path)
	require.Equal(t, []string{"db"}, wp.DependsOn)
}

func TestEdgeCases(t *testing.T) {
	src := loadFixture(t, "edge")
	r := analyze(t, src, ComposeChoices{})
	p := r.Plan()

	// Blockers: the build has no repository, two bind files are missing.
	require.False(t, p.Installable)
	joined := strings.Join(p.Blockers, "\n")
	require.Contains(t, joined, "api is built from source")
	require.Contains(t, joined, "./cfg/app.yml")
	require.Contains(t, joined, "./big.bin")

	// Profiles are opt-in; extends resolves.
	require.False(t, svc(t, p, "debug").Include)
	require.Contains(t, svc(t, p, "debug").IncludeNote, "debug")
	require.False(t, svc(t, p, "base").Include)
	web := svc(t, p, "web")
	require.True(t, web.Include)
	require.Equal(t, "nginx:1.27", web.Source.Image, "extends inherits the image")
	shared, _ := envOf(web, "SHARED")
	require.Equal(t, "from-base", shared.Value)
	extra, _ := envOf(web, "EXTRA")
	require.Equal(t, "hello", extra.Value)
	lit, _ := envOf(web, "LITERAL")
	require.Equal(t, "a $${{braces}} b", lit.Value, "a literal ${{ is escaped so it never becomes a reference")

	// Traefik labels give the hostnames; Compose limits and replicas map over.
	require.Equal(t, []ComposePlanDomain{
		{Host: "app.example.com", Enabled: true, Source: "traefik"},
		{Host: "www.example.com", Enabled: true, Source: "traefik"},
	}, web.Domains)
	require.Equal(t, 256, web.MemoryMB)
	require.InDelta(t, 0.5, web.CPUs, 0)
	require.Equal(t, 90, web.StopGraceSeconds)
	require.Equal(t, 3, web.Replicas)
	require.Empty(t, web.ReplicasNote)
	cache, _ := envOf(web, "CACHE_URL")
	require.Equal(t, "redis://${{cache.NEBULA_PRIVATE_HOST}}:6379/1", cache.Value)
	api, _ := envOf(web, "API_URL")
	require.Equal(t, "http://${{api.NEBULA_PRIVATE_HOST}}:3000/v1", api.Value)
	require.ElementsMatch(t, []string{"api", "cache"}, web.DependsOn)

	// A build service, entrypoint, user, caps, PORT.
	a := svc(t, p, "api")
	require.Equal(t, "git", a.Source.Type)
	require.Equal(t, []string{"gitUrl"}, a.Needs)
	require.Equal(t, "api", a.Source.Build.BuildContext)
	require.Equal(t, "api/Dockerfile.prod", a.Source.Build.DockerfilePath)
	require.Equal(t, []string{"VERSION"}, a.Source.Build.ArgNames)
	require.Equal(t, []string{"serve", "--port", "3000"}, a.Command)
	require.Equal(t, 3000, a.Port)
	require.Contains(t, noteKeys(a), "entrypoint")
	require.Contains(t, noteKeys(a), "cap_add.NET_ADMIN", "NET_ADMIN needs the instance switch, so it is not applied")
	require.NotNil(t, a.Docker, "the entrypoint override is applied")
	require.Equal(t, []string{"/app/entry.sh"}, a.Docker.Entrypoint)
	require.Empty(t, a.Docker.CapAdd)
	require.Equal(t, "1000:1000", a.Docker.User, "user is a Docker option on every server")
	port, _ := envOf(a, "PORT")
	require.True(t, port.Skipped)
	require.Equal(t, []string{"migrate"}, a.DependsOn)
	require.Equal(t, []ComposePlanDomain{{Generate: true, Source: "suggested"}}, a.Domains, "suggested but off: the port was not published")

	// service_completed_successfully makes the dependency a one-shot.
	m := svc(t, p, "migrate")
	require.True(t, m.OneShot)
	require.Equal(t, "none", m.Health.Type)

	// An old Redis stays a plain image with the engine's port.
	c := svc(t, p, "cache")
	require.Contains(t, c.Managed.Reason, "7.2")
	require.Equal(t, 6379, c.Port)

	// Everything that is never applied.
	ag := svc(t, p, "agent")
	for _, k := range []string{"privileged", "network_mode", "pid", "devices", "agent.volumes"} {
		require.Contains(t, noteKeys(ag), k)
	}
	for _, n := range ag.Notes {
		require.Equal(t, SevDanger, n.Severity, n.Key)
	}
	byTarget := map[string]ComposePlanBind{}
	for _, b := range ag.Binds {
		byTarget[b.Target] = b
	}
	require.Equal(t, "rejected", byTarget["/var/run/docker.sock"].Status)
	require.Equal(t, "rejected", byTarget["/data"].Status)
	require.True(t, byTarget["/data"].CanConvert)
	require.Equal(t, "missing", byTarget["/etc/app.yml"].Status)

	// The reviewer fixes the blockers.
	yes, no := true, false
	src.Files = map[string]string{"cfg/app.yml": "a: 1\n", "big.bin": strings.Repeat("x", 70<<10)}
	ch := ComposeChoices{Services: map[string]ComposeServiceChoice{
		"api":   {GitURL: "https://example.com/org/repo.git", GitBranch: "release"},
		"debug": {Include: &yes},
		"agent": {Include: &no},
	}}
	p2 := analyze(t, src, ch).Plan()
	require.True(t, p2.Installable, "%v", p2.Blockers)
	a2 := svc(t, p2, "api")
	require.Equal(t, "https://example.com/org/repo.git", a2.Source.Build.GitURL)
	require.Equal(t, "release", a2.Source.Build.GitBranch)
	require.True(t, svc(t, p2, "debug").Include)
	require.False(t, svc(t, p2, "agent").Include)

	// Switching the build to its image.
	p3 := analyze(t, src, ComposeChoices{Services: map[string]ComposeServiceChoice{
		"api": {Source: "image", Image: "ghcr.io/example/api:2"}, "agent": {Include: &no},
	}}).Plan()
	require.True(t, p3.Installable, "%v", p3.Blockers)
	require.Equal(t, "image", svc(t, p3, "api").Source.Type)
	require.Equal(t, "ghcr.io/example/api:2", svc(t, p3, "api").Source.Image)
}

func TestSlugsAvoidTakenAndRewritesUseThem(t *testing.T) {
	src := ComposeSource{Compose: `
services:
  app:
    image: nginx
    environment:
      UPSTREAM: http://db:5432
  db:
    image: postgres:15
`}
	r, err := Analyze(src, ComposeChoices{}, Context{Taken: func(base string) []string {
		if base == "db" {
			return []string{"db"}
		}
		return nil
	}})
	require.NoError(t, err)
	p := r.Plan()
	require.Equal(t, "db-2", svc(t, p, "db").Slug)
	up, _ := envOf(svc(t, p, "app"), "UPSTREAM")
	require.Equal(t, "http://${{db-2.NEBULA_PRIVATE_HOST}}:5432", up.Value)
}

func TestRewriteGuards(t *testing.T) {
	src := ComposeSource{Compose: `
services:
  app:
    image: nginx
    environment:
      POSTGRES_DB: app
      ADMIN_EMAIL: admin@db
      NAME: db
      DB_HOST: db
      PG_CONNINFO: host=db port=5432 user=x
      CHAIN: db.example.com:443
      SELF_URL: http://app:80
      LOCAL: localhost:5432
      MULTI: db:5432,db:5433
      OPT_OUT: http://db:1
      EMBED: mydb
    ports: ["80:80"]
  db:
    image: postgres:15
`}
	r := analyze(t, src, ComposeChoices{Services: map[string]ComposeServiceChoice{"app": {Env: map[string]ComposeEnvChoice{"OPT_OUT": {Rewrite: new(false)}}}}})
	app := svc(t, r.Plan(), "app")
	val := func(k string) string { e, _ := envOf(app, k); return e.Value }
	require.Equal(t, "app", val("POSTGRES_DB"))
	require.Equal(t, "admin@db", val("ADMIN_EMAIL"), "an e-mail address is not a host")
	require.Equal(t, "db", val("NAME"), "a bare value needs a host-like variable name")
	require.Equal(t, "${{db.NEBULA_PRIVATE_HOST}}", val("DB_HOST"))
	require.Equal(t, "host=${{db.NEBULA_PRIVATE_HOST}} port=5432 user=x", val("PG_CONNINFO"))
	require.Equal(t, "db.example.com:443", val("CHAIN"), "a longer host name is not the service")
	require.Equal(t, "http://app:80", val("SELF_URL"), "a service never references itself")
	require.Equal(t, "localhost:5432", val("LOCAL"))
	require.Equal(t, "${{db.NEBULA_PRIVATE_HOST}}:5432,${{db.NEBULA_PRIVATE_HOST}}:5433", val("MULTI"))
	require.Equal(t, "http://db:1", val("OPT_OUT"), "the reviewer can keep a value as it is")
	require.Equal(t, "mydb", val("EMBED"))
}

func TestManagedValkeyGetsItsPassword(t *testing.T) {
	r := analyze(t, loadFixture(t, "kv"), ComposeChoices{})
	p := r.Plan()
	require.True(t, p.Installable, "%v", p.Blockers)
	kv := svc(t, p, "valkey")
	require.Equal(t, "managed", kv.Source.Type)
	require.Equal(t, "8", kv.Managed.Version)
	app := svc(t, p, "app")
	url, _ := envOf(app, "KV_URL")
	require.True(t, url.Secret, "it now carries the generated password")
	var stored string
	for _, e := range r.plan.Services[0].Env {
		if e.Key == "KV_URL" {
			stored = e.raw
		}
	}
	require.Equal(t, "redis://:${{valkey.REDIS_PASSWORD}}@${{valkey.NEBULA_PRIVATE_HOST}}:6379/0", stored)
	require.Equal(t, []string{"valkey"}, app.DependsOn)
	require.Len(t, rewritesOfKind(p, "auth"), 1)

	no := false
	p2 := analyze(t, loadFixture(t, "kv"), ComposeChoices{Services: map[string]ComposeServiceChoice{"valkey": {ManagedDatabase: &no}}}).Plan()
	u2, _ := envOf(svc(t, p2, "app"), "KV_URL")
	require.Equal(t, "redis://${{valkey.NEBULA_PRIVATE_HOST}}:6379/0", u2.Value)
	require.False(t, u2.Secret)
	require.Equal(t, []ComposePlanVolume{{Name: "kvdata", Path: "/data", From: "volume"}}, svc(t, p2, "valkey").Volumes)
}

func TestSourceAvailableEnginesAreOptIn(t *testing.T) {
	src := ComposeSource{Compose: "services:\n  cache:\n    image: redis:8\n"}
	p := analyze(t, src, ComposeChoices{}).Plan()
	c := svc(t, p, "cache")
	require.True(t, c.Managed.Eligible)
	require.False(t, c.Managed.Enabled)
	require.Contains(t, c.Managed.Reason, "licence")
	yes := true
	p = analyze(t, src, ComposeChoices{Services: map[string]ComposeServiceChoice{"cache": {ManagedDatabase: &yes}}}).Plan()
	require.True(t, svc(t, p, "cache").Managed.Enabled)
}

func TestMutualReferencesDoNotCycle(t *testing.T) {
	src := ComposeSource{Compose: `
services:
  a:
    image: x
    environment:
      B_URL: http://b:80
  b:
    image: x
    environment:
      A_URL: http://a:80
`}
	p := analyze(t, src, ComposeChoices{}).Plan()
	require.Equal(t, []string{"b"}, svc(t, p, "a").DependsOn)
	require.Empty(t, svc(t, p, "b").DependsOn, "the second reference would close a cycle")
	// The reference itself is still rewritten.
	e, _ := envOf(svc(t, p, "b"), "A_URL")
	require.Equal(t, "http://${{a.NEBULA_PRIVATE_HOST}}:80", e.Value)
}

func TestSecretChoicesAndMissingInputs(t *testing.T) {
	src := ComposeSource{Compose: `
services:
  app:
    image: nginx:${TAG}
    environment:
      TOKEN: abc
      PLAIN: yes
      REQUIRED: ${NEED:?please set NEED}
    env_file: [./app.env]
`}
	r := analyze(t, src, ComposeChoices{})
	p := r.Plan()
	require.Equal(t, []string{"TAG"}, p.Missing)
	require.Contains(t, strings.Join(p.Blockers, "\n"), "NEED")
	require.Contains(t, strings.Join(p.Blockers, "\n"), "app.env")
	require.False(t, p.Installable)
	tok, _ := envOf(svc(t, p, "app"), "TOKEN")
	require.True(t, tok.Secret)
	require.True(t, tok.SecretGuess)

	src.Env = "NEED=1\nTAG=1.27\n"
	src.Files = map[string]string{"app.env": "FROM_FILE=1\nPLAIN=overridden\n"}
	yes, no := true, false
	r = analyze(t, src, ComposeChoices{Services: map[string]ComposeServiceChoice{"app": {Env: map[string]ComposeEnvChoice{
		"TOKEN": {Secret: &no}, "PLAIN": {Secret: &yes}, "FROM_FILE": {Skip: true},
	}}}})
	p = r.Plan()
	require.True(t, p.Installable, "%v", p.Blockers)
	app := svc(t, p, "app")
	require.Equal(t, "nginx:1.27", app.Source.Image)
	tok, _ = envOf(app, "TOKEN")
	require.False(t, tok.Secret)
	require.Equal(t, "abc", tok.Value)
	pl, _ := envOf(app, "PLAIN")
	require.True(t, pl.Secret)
	require.Equal(t, "environment", pl.Origin, "environment wins over env_file")
	ff, _ := envOf(app, "FROM_FILE")
	require.True(t, ff.Skipped)
}

func TestRepoMode(t *testing.T) {
	files := map[string]string{"deploy/conf/nginx.conf": "events {}\n"}
	compose := `
name: shop
services:
  web:
    build:
      context: ..
      dockerfile: deploy/web.Dockerfile
    volumes: ["./conf/nginx.conf:/etc/nginx/nginx.conf:ro"]
    ports: ["8080:80"]
  worker:
    build: ../worker
`
	cx := Context{
		ComposeDir: "deploy",
		Repo:       &RepoInfo{Repo: "org/shop", Branch: "main", CloneURL: "https://github.com/org/shop.git", Commit: "abc"},
		Files:      func(p string) ([]byte, bool) { v, ok := files["deploy/"+p]; return []byte(v), ok },
	}
	r, err := Analyze(ComposeSource{Compose: compose}, ComposeChoices{}, cx)
	require.NoError(t, err)
	p := r.Plan()
	require.True(t, p.Installable, "%v", p.Blockers)
	require.Equal(t, "repo", p.SourceKind)
	w := svc(t, p, "web")
	require.Equal(t, "git", w.Source.Type)
	require.Equal(t, "https://github.com/org/shop.git", w.Source.Build.GitURL)
	require.Equal(t, "main", w.Source.Build.GitBranch)
	require.Equal(t, ".", w.Source.Build.BuildContext, "../ from deploy/ is the repository root")
	require.Equal(t, "deploy/web.Dockerfile", w.Source.Build.DockerfilePath, "the Dockerfile is relative to the context")
	require.Len(t, w.Files, 1)
	wk := svc(t, p, "worker")
	require.Equal(t, "worker", wk.Source.Build.BuildContext)
	require.Equal(t, "worker/Dockerfile", wk.Source.Build.DockerfilePath)
	require.Empty(t, wk.Needs)
}

func TestGitContextURL(t *testing.T) {
	p := analyze(t, ComposeSource{Compose: "services:\n  a:\n    build: https://github.com/org/repo.git#dev:services/a\n"}, ComposeChoices{}).Plan()
	b := svc(t, p, "a").Source.Build
	require.Equal(t, "https://github.com/org/repo.git", b.GitURL)
	require.Equal(t, "dev", b.GitBranch)
	require.Equal(t, "services/a", b.BuildContext)
	require.Equal(t, "services/a/Dockerfile", b.DockerfilePath)
}

func TestMountsAndLimits(t *testing.T) {
	src := ComposeSource{
		Compose: `
services:
  a:
    image: x
    volumes:
      - data:/data
      - data2:/data2
      - /anon
      - ./a.conf:/etc/a.conf
      - ./huge.txt:/etc/huge.txt
      - ./cert:/certs
      - ./dir/:/dir
      - /home/me:/home/me
    tmpfs: /tmp
volumes:
  data:
    external: true
  data2:
    driver_opts: {type: nfs}
`,
		Files: map[string]string{"a.conf": "x", "huge.txt": strings.Repeat("x", 65<<10)},
	}
	a := svc(t, analyze(t, src, ComposeChoices{}).Plan(), "a")
	vols := map[string]ComposePlanVolume{}
	for _, v := range a.Volumes {
		vols[v.Path] = v
	}
	require.Equal(t, "data", vols["/data"].Name)
	require.Contains(t, vols["/data"].Note, "external")
	require.Contains(t, vols["/data2"].Note, "driver options")
	require.Equal(t, "anonymous", vols["/anon"].From)
	require.Len(t, a.Files, 1)
	status := map[string]string{}
	for _, b := range a.Binds {
		status[b.Target] = b.Status
	}
	require.Equal(t, map[string]string{"/etc/a.conf": "file", "/etc/huge.txt": "rejected", "/certs": "rejected", "/dir": "rejected", "/home/me": "rejected"}, status)
	require.Equal(t, []agentprotocol.TmpfsMount{{Path: "/tmp"}}, a.Docker.Tmpfs)
}

func TestSharedVolumeWarning(t *testing.T) {
	p := analyze(t, ComposeSource{Compose: `
services:
  a:
    image: x
    volumes: [up:/up]
  b:
    image: x
    volumes: [up:/up]
volumes:
  up:
`}, ComposeChoices{}).Plan()
	require.Len(t, p.Warnings, 1)
	require.Contains(t, p.Warnings[0], "shared by a, b")
}

func TestNetworks(t *testing.T) {
	src := ComposeSource{Compose: `
services:
  a:
    image: x
    networks: [front, shared]
  b:
    image: x
    networks: [back]
networks:
  front:
  back:
  shared:
    external: true
    name: team-net
`}
	p := analyze(t, src, ComposeChoices{}).Plan()
	require.Len(t, p.Networks, 3)
	var ext ComposePlanNetwork
	for _, n := range p.Networks {
		if n.Name == "shared" {
			ext = n
		}
	}
	require.True(t, ext.External)
	require.Nil(t, ext.SharedNetworkID)
	require.Contains(t, ext.Note, "team-net")
	require.Len(t, p.Warnings, 1, "several Compose networks are merged")
}

func TestReplicasNeedStatelessServices(t *testing.T) {
	p := analyze(t, ComposeSource{Compose: `
services:
  a:
    image: x
    ports: ["80"]
    deploy: {replicas: 2}
    volumes: [d:/d]
  b:
    image: x
    deploy: {replicas: 2}
  c:
    image: x
    ports: ["80"]
    scale: 20
`}, ComposeChoices{}).Plan()
	require.Equal(t, 1, svc(t, p, "a").Replicas)
	require.Contains(t, svc(t, p, "a").ReplicasNote, "stateless")
	require.Equal(t, 1, svc(t, p, "b").Replicas)
	require.Equal(t, 10, svc(t, p, "c").Replicas)
	require.NotEmpty(t, svc(t, p, "c").ReplicasNote)
}

func TestHealthcheckMapping(t *testing.T) {
	p := analyze(t, ComposeSource{Compose: `
services:
  a:
    image: x
    healthcheck:
      test: ["CMD", "curl", "-f", "http://localhost/"]
      interval: 1m
      timeout: 2s
      start_period: 20s
      retries: 4
  b:
    image: x
    ports: ["80"]
    healthcheck: {disable: true}
  c:
    image: x
    healthcheck:
      test: NONE
`}, ComposeChoices{}).Plan()
	a := svc(t, p, "a").Health
	require.Equal(t, "exec", a.Type)
	require.Equal(t, []string{"CMD", "curl", "-f", "http://localhost/"}, a.Command)
	require.Equal(t, 10, a.IntervalSeconds, "long intervals are capped so deploys stay quick")
	require.Equal(t, 2, a.TimeoutSeconds)
	require.Equal(t, 20, a.StartPeriodSeconds)
	require.Equal(t, 120, a.DeadlineSeconds)
	require.Equal(t, "none", svc(t, p, "b").Health.Type)
	require.Equal(t, "none", svc(t, p, "c").Health.Type)
}

func TestRemapRefs(t *testing.T) {
	m := map[string]string{"db": "db-2"}
	require.Equal(t, "x ${{db-2.NEBULA_PRIVATE_HOST}} $${{db.X}} ${{other.K}} ${{PORT}}", remapRefs("x ${{db.NEBULA_PRIVATE_HOST}} $${{db.X}} ${{other.K}} ${{PORT}}", m))
	require.Equal(t, "${{db-2.K}}", remapRefs("${{db.K}}", m))
}

func TestRenderEscapes(t *testing.T) {
	require.Equal(t, "a $${{b}} ${{x.Y}}", segs{{lit: "a ${{b}} "}, {ref: "x.Y"}}.render())
}

func TestMaskURLSecrets(t *testing.T) {
	require.Equal(t, "postgres://u:••••••@h/db", maskURLSecrets("postgres://u:pw@h/db"))
	require.Equal(t, "postgres://u:${{db.DATABASE_PASSWORD}}@h/db", maskURLSecrets("postgres://u:${{db.DATABASE_PASSWORD}}@h/db"))
	require.Equal(t, "host=h password=••••••", maskDSN("host=h password=secret"))
}

func TestDockerOptions(t *testing.T) {
	p := analyze(t, ComposeSource{Compose: `
services:
  a:
    image: x
    cap_add: [NET_BIND_SERVICE, SYS_ADMIN, ALL, CAP_SYS_NICE]
    ulimits:
      nofile: {soft: 1024, hard: 2048}
      core: 0
      memlock: -1
    shm_size: 256m
    extra_hosts: ["db.local:10.0.0.5", "bad host:1.2.3.4", "gw=host-gateway"]
    init: true
    stop_signal: SIGQUIT
    tmpfs:
      - /run:size=16m,mode=1777
      - /proc/x
    volumes:
      - ./c.conf:/etc/c.conf
`, Files: map[string]string{"c.conf": "x"}}, ComposeChoices{}).Plan()
	a := svc(t, p, "a")
	d := a.Docker
	require.NotNil(t, d)
	require.Equal(t, []string{"NET_BIND_SERVICE", "SYS_NICE"}, d.CapAdd)
	require.Equal(t, []agentprotocol.Ulimit{{Name: "memlock", Soft: -1, Hard: -1}, {Name: "nofile", Soft: 1024, Hard: 2048}}, d.Ulimits)
	require.Equal(t, 256, d.ShmSizeMB)
	require.Equal(t, []string{"db.local:10.0.0.5", "gw:host-gateway"}, d.ExtraHosts)
	require.True(t, d.Init)
	require.Equal(t, "SIGQUIT", d.StopSignal)
	require.Equal(t, []agentprotocol.TmpfsMount{{Path: "/run", SizeMB: 16, Mode: "1777"}}, d.Tmpfs)
	keys := noteKeys(a)
	for _, k := range []string{"cap_add.SYS_ADMIN", "cap_add.ALL", "ulimits.core", "extra_hosts", "tmpfs"} {
		require.Contains(t, keys, k)
	}
}
