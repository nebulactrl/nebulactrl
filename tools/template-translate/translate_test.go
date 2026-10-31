package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nebulactrl/nebulactrl/internal/templates"
)

const coolifyFixture = `# documentation: https://example.org/docs
# slogan: A tiny example app.
# category: productivity
# tags: example, demo
# logo: svgs/example.svg
# port: 3000

services:
  app:
    image: ghcr.io/example/app:latest
    environment:
      - SERVICE_URL_APP_3000
      - APP_URL=$SERVICE_URL_APP
      - DB_HOST=db
      - DB_PASSWORD=$SERVICE_PASSWORD_DB
      - SESSION_KEY=$SERVICE_BASE64_64_SESSION
      - REDIS_HOST=cache
      - LOG_LEVEL=${LOG_LEVEL:-info}
      - SMTP_FROM=${SMTP_FROM:?}
    volumes:
      - app-data:/data
      - type: bind
        source: ./app.conf
        target: /etc/app/app.conf
        content: |
          listen = 3000
    depends_on:
      db:
        condition: service_healthy
      migrate:
        condition: service_completed_successfully
    healthcheck:
      test: ["CMD", "curl", "-f", "http://127.0.0.1:3000/health"]
      interval: 5s
      timeout: 20s
      retries: 10
    mem_limit: 512m
  migrate:
    image: ghcr.io/example/app:1.2.3
    command: ["app", "migrate"]
    environment:
      - DB_PASSWORD=$SERVICE_PASSWORD_DB
    depends_on:
      db:
        condition: service_healthy
  db:
    image: postgis/postgis:16-3.4
    environment:
      - POSTGRES_USER=app
      - POSTGRES_PASSWORD=$SERVICE_PASSWORD_DB
    volumes:
      - db-data:/var/lib/postgresql/data
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U $${POSTGRES_USER}"]
  cache:
    image: redis:7-alpine
    command: ["redis-server", "--requirepass", "$SERVICE_PASSWORD_REDIS"]
`

const dokployCompose = `services:
  web:
    image: example/web:2.0.1
    environment:
      SITE_HOST: ${SITE_HOST}
      DB_PASSWORD: ${DB_PASSWORD}
    volumes:
      - ../files/web.toml:/etc/web/web.toml
    depends_on:
      - store
  store:
    image: postgis/postgis:16-3.4
    environment:
      POSTGRES_PASSWORD: ${DB_PASSWORD}
    volumes:
      - store-data:/var/lib/postgresql/data
volumes:
  store-data:
`

const dokployTOML = `[variables]
main_domain = "${domain}"
db_pass = "${password:32}"

[config]
env = [
  "SITE_HOST=${main_domain}",
  "DB_PASSWORD=${db_pass}",
]

[[config.domains]]
serviceName = "web"
port = 8_080
host = "${main_domain}"

[[config.mounts]]
filePath = "/web.toml"
content = """
[server]
port = 8080
"""
`

const dokployMeta = `{"id":"webapp","name":"WebApp","description":"A web app. Deploy it with Dokploy in a few clicks.","version":"2.0.1","links":{"github":"https://github.com/example/webapp","website":"https://webapp.example","docs":"https://webapp.example/docs"},"tags":["web","cms"]}`

func mustCoolify(t *testing.T, id string) *source {
	t.Helper()
	s, err := parseCoolify(id, "templates/compose/"+id+".yaml", "abc1234", []byte(coolifyFixture))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func mustDokploy(t *testing.T, id string) *source {
	t.Helper()
	s, err := parseDokploy(id, "blueprints/"+id, "def5678", []byte(dokployCompose), []byte(dokployTOML), []byte(dokployMeta))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// translateParsed translates and round-trips through YAML and templates.Parse.
func translateParsed(t *testing.T, src *source) (*templates.CatalogTemplate, string) {
	t.Helper()
	d, err := translate(src)
	if err != nil {
		t.Fatalf("translate: %v", err)
	}
	data, err := render(d, src)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	tm, err := templates.Parse(data)
	if err != nil {
		t.Fatalf("draft does not parse: %v\n%s", err, data)
	}
	return tm, string(data)
}

func svcOf(t *testing.T, tm *templates.CatalogTemplate, key string) templates.TemplateService {
	t.Helper()
	for _, s := range tm.Services {
		if s.Key == key {
			return s
		}
	}
	t.Fatalf("service %q not in draft", key)
	return templates.TemplateService{}
}

func TestCoolifyDraft(t *testing.T) {
	tm, text := translateParsed(t, mustCoolify(t, "example"))

	// header, origin and the deliberately unfinished fields
	if tm.Origin == nil || tm.Origin.Type != "coolify" || tm.Origin.Commit != "abc1234" || tm.Origin.License != "Apache-2.0" || tm.Origin.Path != "templates/compose/example.yaml" {
		t.Errorf("origin = %+v", tm.Origin)
	}
	if tm.Upstream.License != "" || tm.Upstream.Class != templates.LicensePermissive {
		t.Errorf("upstream = %+v", tm.Upstream)
	}
	if tm.Category != templates.CategoryProductivity {
		t.Errorf("category = %q", tm.Category)
	}
	for _, want := range []string{"# REVIEW: floating tag, pick a stable version", "digest: " + digestStub, "# DRAFT generated"} {
		if !strings.Contains(text, want) {
			t.Errorf("draft misses %q", want)
		}
	}

	// secrets: one generator, shared through the service that does not
	// depend on the others (db), referenced from app and migrate
	db, app, migrate := svcOf(t, tm, "db"), svcOf(t, tm, "app"), svcOf(t, tm, "migrate")
	if got := db.Env["SERVICE_PASSWORD_DB"]; got != "${{secret(32)}}" {
		t.Errorf("db generator = %q", got)
	}
	if got := db.Env["POSTGRES_PASSWORD"]; got != "${{SERVICE_PASSWORD_DB}}" {
		t.Errorf("db password = %q", got)
	}
	if got := app.Env["DB_PASSWORD"]; got != "${{db.SERVICE_PASSWORD_DB}}" {
		t.Errorf("app password = %q", got)
	}
	if got := migrate.Env["DB_PASSWORD"]; got != "${{db.SERVICE_PASSWORD_DB}}" {
		t.Errorf("migrate password = %q", got)
	}
	// single-use magic variable is inlined
	if got := app.Env["SESSION_KEY"]; got != "${{secret(64)}}" {
		t.Errorf("session key = %q", got)
	}

	// SERVICE_URL_* → domain + platform variables
	if len(app.Domains) != 1 || !app.Domains[0].Generate || app.Port != 3000 {
		t.Errorf("app domain/port = %+v / %d", app.Domains, app.Port)
	}
	if got := app.Env["APP_URL"]; got != "${{NEBULA_PUBLIC_URL}}" {
		t.Errorf("APP_URL = %q", got)
	}
	if _, ok := app.Env["SERVICE_URL_APP_3000"]; ok {
		t.Error("the SERVICE_URL declaration must not leak into env")
	}

	// depends_on completed_successfully → oneshot target
	if !migrate.OneShot || migrate.Healthcheck != nil || migrate.Port != 0 {
		t.Errorf("migrate = %+v", migrate)
	}
	if strings.Join(app.DependsOn, ",") != "db,migrate" {
		t.Errorf("app dependsOn = %v", app.DependsOn)
	}

	// inline content bind mount → file; named volume → volume
	if len(app.Files) != 1 || app.Files[0].Path != "/etc/app/app.conf" || !strings.Contains(app.Files[0].Content, "listen = 3000") {
		t.Errorf("files = %+v", app.Files)
	}
	if len(app.Volumes) != 1 || app.Volumes[0].Name != "app-data" || app.Volumes[0].Path != "/data" {
		t.Errorf("volumes = %+v", app.Volumes)
	}

	// curl healthcheck → http
	if h := app.Healthcheck; h == nil || h.Type != "http" || h.Path != "/health" || h.Port != 0 || h.IntervalSeconds != 5 {
		t.Errorf("app healthcheck = %+v", h)
	}
	// exec healthcheck keeps the shell form, $$ → $
	if h := db.Healthcheck; h == nil || h.Type != "exec" || len(h.Command) != 2 || h.Command[0] != "CMD-SHELL" || h.Command[1] != "pg_isready -U ${POSTGRES_USER}" {
		t.Errorf("db healthcheck = %+v", h)
	}

	// ${VAR:-default} / ${VAR:?} → inputs; hostnames → references
	inputs := map[string]templates.TemplateInput{}
	for _, in := range tm.Inputs {
		inputs[in.Key] = in
	}
	if in := inputs["LOG_LEVEL"]; in.Default != "info" || in.Required {
		t.Errorf("LOG_LEVEL input = %+v", in)
	}
	if in := inputs["SMTP_FROM"]; !in.Required {
		t.Errorf("SMTP_FROM input = %+v", in)
	}
	if app.Env["LOG_LEVEL"] != "${input:LOG_LEVEL}" {
		t.Errorf("LOG_LEVEL env = %q", app.Env["LOG_LEVEL"])
	}
	if got := app.Env["DB_HOST"]; got != "${{db.NEBULA_PRIVATE_HOST}}" {
		t.Errorf("DB_HOST = %q", got)
	}

	// official redis → managed database, host via its exported variable
	cache := svcOf(t, tm, "cache")
	if cache.Database == nil || cache.Database.Engine != "redis" || cache.Database.Version != "7" || cache.Image != "" {
		t.Errorf("cache = %+v", cache)
	}
	if got := app.Env["REDIS_HOST"]; got != "${{cache.REDIS_HOST}}" {
		t.Errorf("REDIS_HOST = %q", got)
	}

	// resources
	if app.Resources == nil || app.Resources.MemoryMB != 512 {
		t.Errorf("resources = %+v", app.Resources)
	}
}

func TestDokployDraft(t *testing.T) {
	tm, text := translateParsed(t, mustDokploy(t, "webapp"))
	if tm.Origin == nil || tm.Origin.Type != "dokploy" || tm.Origin.License != "MIT" || tm.Origin.Path != "blueprints/webapp" || tm.Origin.Commit != "def5678" {
		t.Errorf("origin = %+v", tm.Origin)
	}
	if tm.Name != "WebApp" || tm.Links.Website != "https://webapp.example" || tm.Summary != "A web app." {
		t.Errorf("meta = %q %+v %q", tm.Name, tm.Links, tm.Summary)
	}
	if strings.Contains(tm.Description, "Dokploy") {
		t.Errorf("description advertises Dokploy: %q", tm.Description)
	}
	web, store := svcOf(t, tm, "web"), svcOf(t, tm, "store")

	// [[config.domains]] → domain + port; ${main_domain} → platform variable
	if len(web.Domains) != 1 || !web.Domains[0].Generate || web.Port != 8080 {
		t.Errorf("web domain/port = %+v / %d", web.Domains, web.Port)
	}
	if got := web.Env["SITE_HOST"]; got != "${{NEBULA_PUBLIC_DOMAIN}}" {
		t.Errorf("SITE_HOST = %q", got)
	}
	// ${password:32} shared by name between web and store
	if got := store.Env["DB_PASSWORD"]; got != "${{secret(32)}}" {
		t.Errorf("store generator = %q", got)
	}
	if got := store.Env["POSTGRES_PASSWORD"]; got != "${{DB_PASSWORD}}" {
		t.Errorf("store password = %q", got)
	}
	if got := web.Env["DB_PASSWORD"]; got != "${{store.DB_PASSWORD}}" {
		t.Errorf("web password = %q", got)
	}
	// [[config.mounts]] → file
	if len(web.Files) != 1 || web.Files[0].Path != "/etc/web/web.toml" || !strings.Contains(web.Files[0].Content, "port = 8080") {
		t.Errorf("web files = %+v", web.Files)
	}
	if !strings.Contains(text, "# REVIEW: no compose healthcheck") && !strings.Contains(text, "# REVIEW: no usable compose healthcheck") {
		t.Error("missing healthcheck review note")
	}
	if web.Image != "example/web:2.0.1" {
		t.Errorf("image = %q", web.Image)
	}
}

func TestDockerOptionsTranslated(t *testing.T) {
	const compose = `services:
  app:
    image: ghcr.io/example/app:1.2.3
    entrypoint: ["/bin/sh", "-c"]
    command: ["echo $${HOME} && exec app"]
    shm_size: 1gb
    cap_add: [IPC_LOCK]
    ulimits:
      nofile: {soft: 1024, hard: 2048}
    security_opt: ["no-new-privileges:true"]
    healthcheck:
      test: ["CMD", "true"]
  worker:
    image: ghcr.io/example/app:1.2.3
    entrypoint: []
    command: ["app", "work", "--fast"]
`
	src, err := parseCoolify("dock", "templates/compose/dock.yaml", "abc", []byte("# documentation: https://x.org\n\n"+compose))
	if err != nil {
		t.Fatal(err)
	}
	tm, _ := translateParsed(t, src)
	app := svcOf(t, tm, "app")
	if app.Docker == nil {
		t.Fatal("no docker block")
	}
	if got := strings.Join(app.Docker.Entrypoint, "|"); got != "/bin/sh|-c" {
		t.Errorf("entrypoint = %q", got)
	}
	if app.Docker.ShmSizeMB != 1024 || !app.Docker.NoNewPrivileges || len(app.Docker.CapAdd) != 1 || len(app.Docker.Ulimits) != 1 {
		t.Errorf("docker = %+v", app.Docker)
	}
	if len(app.Command) != 1 || app.Command[0] != "echo ${HOME} && exec app" {
		t.Errorf("command = %q", app.Command)
	}
	// entrypoint: [] moves the command's program into the entrypoint
	w := svcOf(t, tm, "worker")
	if w.Docker == nil || strings.Join(w.Docker.Entrypoint, "|") != "app" || strings.Join(w.Command, "|") != "work|--fast" {
		t.Errorf("worker = %+v / %q", w.Docker, w.Command)
	}
}

func TestUnsupportedFeaturesAreSkipped(t *testing.T) {
	cases := map[string]string{
		"build":        "services:\n  a:\n    build: .\n",
		"privileged":   "services:\n  a:\n    image: x:1\n    privileged: true\n",
		"network_mode": "services:\n  a:\n    image: x:1\n    network_mode: host\n",
		"devices":      "services:\n  a:\n    image: x:1\n    devices: [\"/dev/kvm\"]\n",
		"cap_add":      "services:\n  a:\n    image: x:1\n    cap_add: [NET_ADMIN]\n",
		"volumes_from": "services:\n  a:\n    image: x:1\n    volumes_from: [b]\n",
		"networks":     "services:\n  a:\n    image: x:1\n    networks: [backend]\n",
		"host bind":    "services:\n  a:\n    image: x:1\n    volumes:\n      - /var/run/docker.sock:/var/run/docker.sock\n",
	}
	for name, compose := range cases {
		src, _ := parseCoolify(name, "templates/compose/"+name+".yaml", "abc", []byte("# documentation: https://x.org\n\n"+compose))
		_, err := translate(src)
		var se *skipError
		if !errors.As(err, &se) {
			t.Errorf("%s: want skipError, got %v", name, err)
		}
	}

	var many strings.Builder
	many.WriteString("services:\n")
	for _, n := range []string{"a", "b", "c", "d", "e", "f", "g", "h", "i"} {
		many.WriteString("  " + n + ":\n    image: x:1\n")
	}
	src, _ := parseCoolify("many", "templates/compose/many.yaml", "abc", []byte(many.String()))
	if _, err := translate(src); err == nil || !strings.Contains(err.Error(), "more than 8 services") {
		t.Errorf("9 services: %v", err)
	}
}

func TestParseTOML(t *testing.T) {
	root, err := parseTOML(dokployTOML)
	if err != nil {
		t.Fatal(err)
	}
	cfg := root["config"].(map[string]any)
	doms := cfg["domains"].([]any)
	if len(doms) != 1 || doms[0].(map[string]any)["port"].(int64) != 8080 {
		t.Errorf("domains = %v", doms)
	}
	env := cfg["env"].([]any)
	if len(env) != 2 || env[1] != "DB_PASSWORD=${db_pass}" {
		t.Errorf("env = %v", env)
	}
	mounts := cfg["mounts"].([]any)
	if c := mounts[0].(map[string]any)["content"].(string); c != "[server]\nport = 8080\n" {
		t.Errorf("mount content = %q", c)
	}
	if _, err := parseTOML("a = \n"); err == nil {
		t.Error("missing value must be an error")
	}
}

func TestSelectionAndOutput(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "drafts")
	catalog := filepath.Join(dir, "catalog")
	if err := os.MkdirAll(catalog, 0o755); err != nil {
		t.Fatal(err)
	}
	// "taken" already exists in the catalog and must be left alone
	if err := os.WriteFile(filepath.Join(catalog, "taken.yaml"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	bad, _ := parseCoolify("bad", "templates/compose/bad.yaml", "abc", []byte("services:\n  a:\n    build: .\n"))
	ignored, _ := parseCoolify("hidden", "templates/compose/hidden.yaml", "abc", []byte("# ignore: true\n"+coolifyFixture))
	cool := []*source{
		mustCoolify(t, "alpha"), // coolify only
		mustCoolify(t, "webapp"),
		mustCoolify(t, "taken"),
		bad, ignored,
	}
	cool[1].Compose.Services = cool[1].Compose.Services[:3] // <= 3 services
	cool[0].Compose.Services = cool[0].Compose.Services[:3]
	cool[2].Compose.Services = cool[2].Compose.Services[:3]
	dok := []*source{mustDokploy(t, "webapp"), mustDokploy(t, "zeta")}

	res, err := translateAll(cool, dok, options{Out: out, Catalog: catalog})
	if err != nil {
		t.Fatal(err)
	}
	// apps in both catalogs first, then Coolify order, then Dokploy-only
	if got := strings.Join(res.Written, ","); got != "webapp,alpha,zeta" {
		t.Errorf("written = %s", got)
	}
	for _, id := range res.Written {
		data, err := os.ReadFile(filepath.Join(out, id+".yaml"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := templates.Parse(data); err != nil {
			t.Errorf("%s does not parse: %v", id, err)
		}
	}
	if _, err := os.Stat(filepath.Join(out, "taken.yaml")); err == nil {
		t.Error("catalog ids must not be drafted")
	}
	if _, err := os.Stat(filepath.Join(catalog, "alpha.yaml")); err == nil {
		t.Error("drafts must never be written to the catalog")
	}

	// -only translates the named template and lists unsupported ones
	res, err = translateAll(cool, dok, options{Out: out, Catalog: catalog, Only: []string{"bad"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Written) != 0 || len(res.Skipped) != 1 || !strings.Contains(res.Skipped[0].Reason, "build:") {
		t.Errorf("only=bad: %+v", res)
	}
	skippedTxt, err := os.ReadFile(filepath.Join(out, "SKIPPED.txt"))
	if err != nil || !strings.Contains(string(skippedTxt), "bad [coolify]: coolify: build: directive") {
		t.Errorf("SKIPPED.txt = %q (%v)", skippedTxt, err)
	}

	// -limit
	res, err = translateAll(cool, dok, options{Out: out, Catalog: catalog, Limit: 1})
	if err != nil || len(res.Written) != 1 {
		t.Errorf("limit: %+v %v", res, err)
	}
}
