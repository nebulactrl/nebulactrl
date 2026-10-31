package projectconfig

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func ftScope(s string) string {
	if s == "" {
		return "runtime"
	}
	return s
}

func ftSameStrings(a, b []string) bool {
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	return reflect.DeepEqual(a, b)
}

// ftSameView reports every difference between the wanted view of an
// environment and the view resolved from the factored file.
func ftSameView(t *testing.T, env string, want, got *Resolved) {
	t.Helper()
	if got == nil {
		t.Fatalf("env %s: Resolve returned nil", env)
	}
	if got.Server != want.Server {
		t.Errorf("env %s: server = %q, want %q", env, got.Server, want.Server)
	}
	if got.Project != want.Project {
		t.Errorf("env %s: project = %q, want %q", env, got.Project, want.Project)
	}
	if g, w := rtCanonVars(got.Shared), rtCanonVars(want.Shared); g != w {
		t.Errorf("env %s: shared = %s, want %s", env, g, w)
	}
	if g, w := rtCanonVars(got.EnvShared), rtCanonVars(want.EnvShared); g != w {
		t.Errorf("env %s: env shared = %s, want %s", env, g, w)
	}
	if !ftSameStrings(got.SharedSecrets, want.SharedSecrets) || (len(want.SharedSecrets) > 0 && ftScope(got.SharedSecretScope) != ftScope(want.SharedSecretScope)) {
		t.Errorf("env %s: shared secrets = %v/%s, want %v/%s", env, got.SharedSecrets, got.SharedSecretScope, want.SharedSecrets, want.SharedSecretScope)
	}
	if !ftSameStrings(got.EnvSharedSecrets, want.EnvSharedSecrets) || (len(want.EnvSharedSecrets) > 0 && ftScope(got.EnvSharedSecretScope) != ftScope(want.EnvSharedSecretScope)) {
		t.Errorf("env %s: env shared secrets = %v/%s, want %v/%s", env, got.EnvSharedSecrets, got.EnvSharedSecretScope, want.EnvSharedSecrets, want.EnvSharedSecretScope)
	}
	if g, w := got.SortedSlugs(), want.SortedSlugs(); !reflect.DeepEqual(g, w) {
		t.Errorf("env %s: services = %v, want %v", env, g, w)
		return
	}
	for slug, ws := range want.Services {
		gs := got.Services[slug]
		if g, w := rtCanonDef(gs.Def), rtCanonDef(ws.Def); g != w {
			t.Errorf("env %s: service %s differs\n got: %s\nwant: %s", env, slug, g, w)
		}
	}
}

// ftRoundTrip factors envs, checks determinism and that the inputs are left
// alone, parses the text and requires Resolve(text, e) == envs[e] for every e.
func ftRoundTrip(t *testing.T, envs map[string]*Resolved, base string) string {
	t.Helper()
	before := rtCanon(envs)
	text, err := Factor(envs, base, RenderOptions{})
	if err != nil {
		t.Fatalf("Factor: %v", err)
	}
	if after := rtCanon(envs); after != before {
		t.Errorf("Factor mutated its input")
	}
	again, err := Factor(envs, base, RenderOptions{})
	if err != nil || string(again) != string(text) {
		t.Errorf("Factor is not deterministic (err %v):\n%s\n---\n%s", err, again, text)
	}
	rtHygiene(t, text)

	f, idx := rtParse(t, string(text))
	slugs := make([]string, 0, len(envs))
	for e := range envs {
		slugs = append(slugs, e)
	}
	sort.Strings(slugs)
	for _, e := range slugs {
		got, diags := f.Resolve(e, idx)
		if diags.HasErrors() {
			t.Fatalf("env %s: resolve errors:\n%s\n--- text ---\n%s", e, diags.RenderText(""), text)
		}
		ftSameView(t, e, envs[e], got)
	}
	if t.Failed() {
		t.Logf("--- factored text ---\n%s", text)
	}
	return string(text)
}

func ftCaseA() map[string]*Resolved {
	prod := rtView("production", map[string]*Service{
		"web": {
			Name:    new("Web"),
			Image:   new("ghcr.io/acme/web:1"),
			Port:    new(3000),
			Command: []string{"bun", "run", "start"},
			Variables: map[string]Variable{
				"LOG_LEVEL":  {Value: "info"},
				"NODE_ENV":   {Value: "production"},
				"APP_URL":    {Value: "https://shop.acme.com"},
				"BUILD_FLAG": {Value: "1", Comment: "x"},
			},
			Domains: map[string]*Domain{
				"shop.acme.com": {
					WWWRedirect: new(true),
					Cloudflare:  &Cloudflare{Proxied: new(true)},
					Options:     &RouteOptions{Compress: new(true), SecurityHeaders: new("strict")},
				},
			},
			Scale:  &Scale{Replicas: new(2)},
			Uptime: &Uptime{FailureThreshold: new(3)},
		},
		"printer": {
			Server: new("fra-1"),
			Route:  &Route{Protocol: new("http"), Upstreams: []Upstream{{Address: new("192.168.1.20:631")}}},
		},
	})
	prod.Project = "acme-shop"
	prod.Server = "fra-1"
	prod.Shared = map[string]Variable{"APP_NAME": {Value: "Acme Shop"}}
	prod.SharedSecrets = []string{"STRIPE_SECRET_KEY"}

	staging := rtView("staging", map[string]*Service{
		"web": {
			Name:    new("Web"),
			Image:   new("ghcr.io/acme/web:1"),
			Port:    new(3000),
			Command: []string{"bun", "run", "start", "--staging"},
			Variables: map[string]Variable{
				"LOG_LEVEL":  {Value: "info"},
				"NODE_ENV":   {Value: "staging"},
				"APP_URL":    {Value: "https://shop.staging.acme.com"},
				"BUILD_FLAG": {Value: "1", Comment: "y"},
			},
			Domains: map[string]*Domain{
				"shop.staging.acme.com": {Cloudflare: &Cloudflare{Proxied: new(false)}},
			},
		},
		"worker": {Image: new("ghcr.io/acme/worker:1"), Command: []string{"bun", "run", "worker"}},
	})
	staging.Project = "acme-shop"
	staging.Server = "fra-staging"
	staging.Shared = prod.Shared
	staging.EnvShared = map[string]Variable{"APP_NAME": {Value: "Acme Shop (staging)"}}
	staging.SharedSecrets = prod.SharedSecrets
	staging.EnvSharedSecrets = []string{"STAGING_ONLY_KEY"}
	return map[string]*Resolved{"production": prod, "staging": staging}
}

func TestFactorCaseA(t *testing.T) {
	text := ftRoundTrip(t, ftCaseA(), "production")

	mustContain := []string{
		"[services.printer]\nserver = \"fra-1\"\nonly_in = [\"production\"]\n",
		"[services.worker]\nonly_in = [\"staging\"]\n",
		"[environments.staging]\nserver = \"fra-staging\"\n",
		"[environments.staging.variables]\nAPP_NAME = \"Acme Shop (staging)\"\n",
		"[environments.staging.secrets]\nkeys = [\"STAGING_ONLY_KEY\"]\n",
		"[environments.staging.services.web]\n" +
			"unset = [\"domains.\\\"shop.acme.com\\\"\", \"scale\", \"uptime\"]\n" +
			"command = [\"bun\", \"run\", \"start\", \"--staging\"]\n",
		"[environments.staging.services.web.variables]\n" +
			"APP_URL = \"https://shop.staging.acme.com\"\n" +
			"BUILD_FLAG = { value = \"1\", comment = \"y\" }\n" +
			"NODE_ENV = \"staging\"\n",
		"[environments.staging.services.web.domains.\"shop.staging.acme.com\".cloudflare]\nproxied = false\n",
	}
	for _, s := range mustContain {
		if !strings.Contains(text, s) {
			t.Errorf("output lacks:\n%s\n--- output ---\n%s", s, text)
		}
	}
	if n := strings.Count(text, "only_in"); n != 2 {
		t.Errorf("only_in appears %d times, want 2 (printer, worker; web exists everywhere)", n)
	}
	if strings.Contains(text, "[environments.production") {
		t.Errorf("the base environment has no differences and must have no block")
	}
	region := text[strings.Index(text, "[environments."):]
	for _, s := range []string{"LOG_LEVEL", "image", "name = ", "port", "worker", "printer"} {
		if strings.Contains(region, s) {
			t.Errorf("environment blocks restate %q although it does not differ:\n%s", s, region)
		}
	}
	if strings.Index(text, "[environments.") < strings.Index(text, "[services.worker]") {
		t.Errorf("environments must come after the services")
	}
}

func TestFactorGolden(t *testing.T) {
	mk := func(env string, port int, vars map[string]Variable) *Resolved {
		return rtView(env, map[string]*Service{"web": {Image: new("img"), Port: new(port), Variables: vars}})
	}
	envs := map[string]*Resolved{
		"production": mk("production", 3000, map[string]Variable{"A": {Value: "1"}}),
		"staging":    mk("staging", 8080, map[string]Variable{"A": {Value: "2"}, "B": {Value: "3"}}),
	}
	want := `schema = 1

[services.web]
image = "img"
port = 3000

[services.web.variables]
A = "1"

[environments.staging.services.web]
port = 8080

[environments.staging.services.web.variables]
A = "2"
B = "3"
`
	text := ftRoundTrip(t, envs, "production")
	if text != want {
		t.Fatalf("golden mismatch\n--- got ---\n%s\n--- want ---\n%s", text, want)
	}
	// With another base the roles swap.
	text = ftRoundTrip(t, envs, "staging")
	want = `schema = 1

[services.web]
image = "img"
port = 8080

[services.web.variables]
A = "2"
B = "3"

[environments.production.services.web]
unset = ["variables.B"]
port = 3000

[environments.production.services.web.variables]
A = "1"
`
	if text != want {
		t.Fatalf("golden mismatch (base staging)\n--- got ---\n%s\n--- want ---\n%s", text, want)
	}
}

func TestFactorNestedDifferences(t *testing.T) {
	base := &Service{
		Image:       new("img"),
		Port:        new(3000),
		HealthCheck: &HealthCheck{Type: new("http"), Path: new("/x"), DeadlineSeconds: new(90)},
		Domains: map[string]*Domain{
			"a.example.com": {Options: &RouteOptions{Compress: new(true), CORS: &CORS{Origins: []string{"*"}}}},
		},
		Tasks: map[string]*Task{
			"t1": {Schedule: new("0 7 * * *"), Command: []string{"echo", "1"}},
			"t2": {Schedule: new("0 8 * * *"), Command: []string{"echo", "2"}},
		},
		Files:     map[string]*Files{"/etc/a": {Mode: new("0644"), Content: new("x\n")}},
		Volumes:   map[string]string{"data": "/data", "cache": "/cache"},
		Variables: map[string]Variable{"A": {Value: "1"}, "B": {Value: "2"}},
		Docker:    &Docker{Labels: map[string]string{"team": "a", "tier": "web"}, CapAdd: []string{"NET_BIND_SERVICE"}},
	}
	env := &Service{
		Image:       new("img"),
		Port:        new(3000),
		HealthCheck: &HealthCheck{Type: new("http"), DeadlineSeconds: new(120)},
		Domains: map[string]*Domain{
			"a.example.com": {Options: &RouteOptions{Compress: new(false)}},
		},
		Tasks: map[string]*Task{
			"t2": {Schedule: new("0 8 * * *"), Command: []string{"echo", "2"}},
		},
		Files:     map[string]*Files{"/etc/a": {Mode: new("0600"), Content: new("y\n")}},
		Volumes:   map[string]string{"data": "/data"},
		Variables: map[string]Variable{"A": {Value: "1"}},
		Docker:    &Docker{Labels: map[string]string{"team": "b", "tier": "web"}},
	}
	envs := map[string]*Resolved{
		"production": rtView("production", map[string]*Service{"web": base}),
		"staging":    rtView("staging", map[string]*Service{"web": env}),
	}
	text := ftRoundTrip(t, envs, "production")

	want := `[environments.staging.services.web]
unset = ["docker.cap_add", "health_check.path", "tasks.t1", "variables.B", "volumes.cache"]

[environments.staging.services.web.health_check]
deadline_seconds = 120

[environments.staging.services.web.docker.labels]
team = "b"

[environments.staging.services.web.domains."a.example.com".options]
compress = false

[environments.staging.services.web.files."/etc/a"]
mode = "0600"
content = """
y
"""
`
	if !strings.HasSuffix(text, want) {
		// Table order follows the model: docker before domains before files;
		// health_check comes first. Show the whole thing on failure.
		t.Errorf("environment block differs\n--- got ---\n%s\n--- want suffix ---\n%s", text[strings.Index(text, "[environments."):], want)
	}
}

// Processes merge key by key: an environment restates only what differs, drops
// a process with unset, adds one of its own and keeps a process' scale apart.
func TestFactorProcesses(t *testing.T) {
	base := &Service{
		Image:       new("img"),
		Port:        new(3000),
		ProcessName: new("web"),
		Processes: map[string]*Process{
			"sidekiq": {
				Command: []string{"bundle", "exec", "sidekiq"}, StopGraceSeconds: new(120),
				HealthCheck: &HealthCheck{Type: new("none")},
				Scale:       &Scale{Replicas: new(2)},
			},
			"clock": {Command: []string{"bundle", "exec", "clockwork"}, Singleton: new(true)},
		},
	}
	env := &Service{
		Image:       new("img"),
		Port:        new(3000),
		ProcessName: new("web"),
		Processes: map[string]*Process{
			"sidekiq": {
				Command: []string{"bundle", "exec", "sidekiq"}, StopGraceSeconds: new(60),
				HealthCheck: &HealthCheck{Type: new("none")},
				Scale:       &Scale{Replicas: new(1)},
			},
			"mailer": {Command: []string{"bundle", "exec", "mailer"}, Port: new(3001)},
		},
	}
	envs := map[string]*Resolved{
		"production": rtView("production", map[string]*Service{"web": base}),
		"staging":    rtView("staging", map[string]*Service{"web": env}),
	}
	text := ftRoundTrip(t, envs, "production")
	for _, want := range []string{
		"[services.web.processes.sidekiq]\ncommand = [\"bundle\", \"exec\", \"sidekiq\"]\nstop_grace_seconds = 120\n",
		"[services.web.processes.clock]\ncommand = [\"bundle\", \"exec\", \"clockwork\"]\nsingleton = true\n",
		"[environments.staging.services.web]\nunset = [\"processes.clock\"]\n",
		"[environments.staging.services.web.processes.sidekiq]\nstop_grace_seconds = 60\n",
		"[environments.staging.services.web.processes.sidekiq.scale]\nreplicas = 1\n",
		"[environments.staging.services.web.processes.mailer]\ncommand = [\"bundle\", \"exec\", \"mailer\"]\nport = 3001\n",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("output lacks:\n%s\n--- output ---\n%s", want, text)
		}
	}
	if strings.Contains(text, "[environments.staging.services.web.processes.sidekiq.health_check]") {
		t.Errorf("an unchanged health check is not restated:\n%s", text)
	}
}

func TestFactorServicesInSomeEnvironments(t *testing.T) {
	worker := func(cmd string) *Service {
		return &Service{Image: new("img:worker"), Command: []string{"run", cmd}}
	}
	web := &Service{Image: new("img:web"), Port: new(3000)}
	envs := map[string]*Resolved{
		"production": rtView("production", map[string]*Service{"web": web}),
		"staging":    rtView("staging", map[string]*Service{"web": web, "worker": worker("b")}),
		"dev":        rtView("dev", map[string]*Service{"web": web, "worker": worker("a")}),
	}
	text := ftRoundTrip(t, envs, "production")
	// The base lacks the worker: its definition comes from the first
	// environment by slug that has it (dev), and staging overrides the diff.
	for _, s := range []string{
		"[services.worker]\nonly_in = [\"dev\", \"staging\"]\nimage = \"img:worker\"\ncommand = [\"run\", \"a\"]\n",
		"[environments.staging.services.worker]\ncommand = [\"run\", \"b\"]\n",
	} {
		if !strings.Contains(text, s) {
			t.Errorf("output lacks:\n%s\n--- output ---\n%s", s, text)
		}
	}
	if strings.Count(text, "only_in") != 1 {
		t.Errorf("web exists everywhere and must not have only_in:\n%s", text)
	}
	if strings.Contains(text, "[environments.dev") || strings.Contains(text, "[environments.production") {
		t.Errorf("dev and production have no differences, no blocks expected:\n%s", text)
	}
}

func TestFactorServerAndLayers(t *testing.T) {
	svc := func() map[string]*Service { return map[string]*Service{"web": {Image: new("img")}} }
	mk := func(env, server string) *Resolved {
		r := rtView(env, svc())
		r.Project = "p"
		r.Server = server
		return r
	}
	tests := []struct {
		name      string
		servers   map[string]string // env -> default server
		wantRoot  string            // expected root server line, "" for none
		wantInEnv map[string]string
	}{
		{"same everywhere", map[string]string{"production": "a", "staging": "a"}, `server = "a"`, nil},
		{"one differs", map[string]string{"production": "a", "staging": "b", "dev": "a"}, `server = "a"`, map[string]string{"staging": "b"}},
		{"base has none", map[string]string{"production": "", "staging": "b"}, "", map[string]string{"staging": "b"}},
		{"an environment has none", map[string]string{"production": "a", "staging": ""}, "", map[string]string{"production": "a"}},
		{"none at all", map[string]string{"production": "", "staging": ""}, "", nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			envs := map[string]*Resolved{}
			for e, s := range tc.servers {
				envs[e] = mk(e, s)
			}
			text := ftRoundTrip(t, envs, "production")
			head, _, _ := strings.Cut(text, "\n\n")
			hasRoot := strings.Contains("\n"+head+"\n", "\nserver = ")
			if tc.wantRoot == "" && hasRoot {
				t.Errorf("unexpected root server:\n%s", text)
			}
			if tc.wantRoot != "" && !strings.Contains("\n"+head+"\n", "\n"+tc.wantRoot+"\n") {
				t.Errorf("root server %s missing:\n%s", tc.wantRoot, text)
			}
			for e, s := range tc.wantInEnv {
				if want := fmt.Sprintf("[environments.%s]\nserver = %q\n", e, s); !strings.Contains(text, want) {
					t.Errorf("missing %q:\n%s", want, text)
				}
			}
			if got := strings.Count(text, "[environments."); got != len(tc.wantInEnv) {
				t.Errorf("%d environment blocks, want %d:\n%s", got, len(tc.wantInEnv), text)
			}
		})
	}
}

func TestFactorSharedLayers(t *testing.T) {
	prod := rtView("production", map[string]*Service{"web": {Image: new("img")}})
	prod.Shared = map[string]Variable{"APP_NAME": {Value: "Acme"}}
	prod.EnvShared = map[string]Variable{"REGION": {Value: "eu"}}
	prod.SharedSecrets = []string{"STRIPE_SECRET_KEY"}
	prod.EnvSharedSecrets = []string{"PROD_ONLY_KEY"}
	staging := rtView("staging", map[string]*Service{"web": {Image: new("img")}})
	staging.Shared = prod.Shared
	staging.EnvShared = map[string]Variable{"REGION": {Value: "us"}, "EXTRA": {Value: "1", Comment: "c"}}
	staging.SharedSecrets = prod.SharedSecrets
	dev := rtView("dev", map[string]*Service{"web": {Image: new("img")}})
	dev.Shared = prod.Shared
	dev.SharedSecrets = prod.SharedSecrets

	text := ftRoundTrip(t, map[string]*Resolved{"production": prod, "staging": staging, "dev": dev}, "production")
	for _, s := range []string{
		"[variables]\nAPP_NAME = \"Acme\"\n",
		"[secrets]\nkeys = [\"STRIPE_SECRET_KEY\"]\n",
		"[environments.production.variables]\nREGION = \"eu\"\n",
		"[environments.production.secrets]\nkeys = [\"PROD_ONLY_KEY\"]\n",
		"[environments.staging.variables]\nEXTRA = { value = \"1\", comment = \"c\" }\nREGION = \"us\"\n",
	} {
		if !strings.Contains(text, s) {
			t.Errorf("output lacks:\n%s\n--- output ---\n%s", s, text)
		}
	}
	if strings.Contains(text, "[environments.dev") {
		t.Errorf("dev has no environment layer:\n%s", text)
	}
}

func TestFactorErrors(t *testing.T) {
	one := map[string]*Resolved{"production": rtView("production", nil)}
	tests := []struct {
		name string
		envs map[string]*Resolved
		base string
		want string
	}{
		{"nil map", nil, "production", "no environments"},
		{"empty map", map[string]*Resolved{}, "production", "no environments"},
		{"missing base", one, "staging", `"staging"`},
		{"empty base slug", one, "", "base environment"},
		{"nil view", map[string]*Resolved{"production": nil}, "production", `"production"`},
		{"nil view of another environment", map[string]*Resolved{"production": rtView("production", nil), "dev": nil}, "production", `"dev"`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out, err := Factor(tc.envs, tc.base, RenderOptions{})
			if err == nil {
				t.Fatalf("expected an error, got output:\n%s", out)
			}
			if out != nil {
				t.Errorf("output must be nil on error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not mention %q", err, tc.want)
			}
		})
	}
	// A single environment factors to the plain render.
	text, err := Factor(one, "production", RenderOptions{})
	if err != nil || string(text) != "schema = 1\n" {
		t.Errorf("single empty environment: %q, %v", text, err)
	}
}

func TestFactorOptionsApply(t *testing.T) {
	envs := ftCaseA()
	o := RenderOptions{
		Origin:           "https://nebula.example.com",
		Comments:         map[string][]string{"environments.staging": {"staging overrides"}, "services.web": {"the shop"}},
		NotRepresentable: map[string][]string{"printer": {"tailnet"}},
	}
	text, err := Factor(envs, "production", o)
	if err != nil {
		t.Fatal(err)
	}
	s := string(text)
	for _, want := range []string{
		"#:schema https://nebula.example.com/schema/nebula.toml.json\nschema = 1\n",
		"\n# the shop\n[services.web]\n",
		"\n# not representable in schema 1: tailnet\n",
		"\n# staging overrides\n[environments.staging]\nserver = \"fra-staging\"\n",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("output lacks %q:\n%s", want, s)
		}
	}
	f, idx := rtParse(t, s)
	for e, want := range envs {
		got, _ := f.Resolve(e, idx)
		ftSameView(t, e, want, got)
	}
}

// ---------------------------------------------------------------------------
// Generated environments.

type ftGen struct{ rng *rand.Rand }

func (g *ftGen) one(n int) bool { return g.rng.IntN(n) == 0 }

func (g *ftGen) str(opts ...string) string { return opts[g.rng.IntN(len(opts))] }

func (g *ftGen) num(opts ...int) int { return opts[g.rng.IntN(len(opts))] }

func (g *ftGen) command() []string {
	switch g.rng.IntN(3) {
	case 0:
		return []string{}
	case 1:
		return []string{"run"}
	}
	return []string{"run", g.str("--a", "--b")}
}

// service builds a valid image service from a small pool of keys and values.
func (g *ftGen) service(slug string) *Service {
	d := &Service{Image: new(g.str("img:1", "img:2")), Port: new(g.num(3000, 8080))}
	if g.one(2) {
		d.Name = new(g.str("Web", "Api"))
	}
	if g.one(3) {
		d.Command = g.command()
	}
	if g.one(3) {
		d.MemoryMB = new(g.num(256, 512))
	}
	if g.one(3) {
		d.CPUs = new(g.str2f(0.5, 1.5, 2))
	}
	if g.one(3) {
		d.StopGraceSeconds = new(g.num(10, 30))
	}
	if g.one(2) {
		hc := &HealthCheck{}
		if g.one(2) {
			hc.Type = new("http")
		}
		if g.one(2) {
			hc.Path = new(g.str("/a", "/b"))
		}
		if g.one(2) {
			hc.DeadlineSeconds = new(g.num(60, 90))
		}
		d.HealthCheck = hc
	}
	if g.one(3) {
		d.Scale = &Scale{}
		if g.one(2) {
			d.Scale.Replicas = new(g.num(1, 2, 3))
		}
	}
	if g.one(3) {
		d.Uptime = &Uptime{FailureThreshold: new(g.num(2, 5))}
	}
	if g.one(2) {
		d.Variables = map[string]Variable{}
		for _, k := range []string{"A_VAR", "B_VAR", "C_VAR"} {
			if g.one(2) {
				v := Variable{Value: g.str("1", "2", "x y")}
				if g.one(4) {
					v.Comment = "note"
				}
				d.Variables[k] = v
			}
		}
	}
	if g.one(2) {
		d.Domains = map[string]*Domain{}
		for _, h := range []string{slug + "-a.example.com", slug + "-b.example.com"} {
			if !g.one(2) {
				continue
			}
			dom := &Domain{}
			if g.one(2) {
				dom.WWWRedirect = new(g.one(2))
			}
			if g.one(2) {
				dom.Options = &RouteOptions{Compress: new(g.one(2))}
				if g.one(3) {
					dom.Options.Access = &Access{AllowCIDRs: []string{g.str("203.0.113.0/24", "198.51.100.0/24")}}
				}
			}
			if g.one(3) {
				dom.Cloudflare = &Cloudflare{Proxied: new(g.one(2))}
			}
			d.Domains[h] = dom
		}
	}
	if g.one(3) {
		d.Files = map[string]*Files{"/etc/app.conf": {Mode: new(g.str("0644", "0600")), Content: new(g.str("a\n", "b = \"q\"\nc\n"))}}
	}
	if g.one(3) {
		d.Tasks = map[string]*Task{"job": {Schedule: new(g.str("0 7 * * *", "*/10 * * * *")), Command: []string{"run", g.str("a", "b")}}}
	}
	if g.one(3) {
		d.Docker = &Docker{}
		if g.one(2) {
			d.Docker.Labels = map[string]string{"team": g.str("shop", "ops")}
		}
		if g.one(2) {
			d.Docker.CapDrop = []string{"ALL"}
		}
		if g.one(2) {
			d.Docker.Init = new(g.one(2))
		}
	}
	return d
}

func (g *ftGen) str2f(opts ...float64) float64 { return opts[g.rng.IntN(len(opts))] }

// clone copies a definition through its JSON form (a fresh tree of maps).
func ftClone(d *Service) *Service {
	var c Service
	if err := json.Unmarshal([]byte(rtCanon(d)), &c); err != nil {
		panic(err)
	}
	return &c
}

// mutate changes one random aspect of d.
func (g *ftGen) mutate(d *Service, slug string) {
	switch g.rng.IntN(9) {
	case 0:
		d.Port = new(g.num(3000, 8080, 9000))
	case 1:
		d.Scale = nil
	case 2:
		d.Command = g.command()
	case 3:
		if d.Variables == nil {
			d.Variables = map[string]Variable{}
		}
		if _, ok := d.Variables["A_VAR"]; ok {
			delete(d.Variables, "A_VAR")
		} else {
			d.Variables["A_VAR"] = Variable{Value: "m"}
		}
	case 4:
		d.Domains = nil
	case 5:
		d.HealthCheck = &HealthCheck{Path: new(g.str("/a", "/b"))}
	case 6:
		d.Uptime = &Uptime{FailureThreshold: new(g.num(2, 5))}
	case 7:
		d.Docker = nil
	case 8:
		d.Files = nil
	}
}

func (g *ftGen) environments() map[string]*Resolved {
	slugs := []string{"production", "staging", "preview"}[:2+g.rng.IntN(2)]
	names := []string{"web", "worker", "api"}
	bases := map[string]*Service{}
	for _, n := range names {
		bases[n] = g.service(n)
	}
	shared := map[string]Variable{}
	if g.one(2) {
		shared["APP_NAME"] = Variable{Value: g.str("Acme", "Acme Shop")}
	}
	envs := map[string]*Resolved{}
	for _, e := range slugs {
		svcs := map[string]*Service{}
		for _, n := range names {
			if g.one(4) {
				continue
			}
			var d *Service
			if g.one(3) {
				d = g.service(n)
			} else {
				d = ftClone(bases[n])
				for range 1 + g.rng.IntN(3) {
					g.mutate(d, n)
				}
			}
			svcs[n] = d
		}
		r := rtView(e, svcs)
		r.Project = "p"
		r.Server = g.str("", "fra-1", "fra-2")
		r.Shared = shared
		if g.one(2) {
			r.EnvShared = map[string]Variable{"X_VAR": {Value: g.str("a", "b")}}
		}
		envs[e] = r
	}
	return envs
}

// TestFactorProperty checks Resolve(Parse(Factor(envs, base)), e) == envs[e]
// over generated environment sets.
func TestFactorProperty(t *testing.T) {
	for seed := uint64(1); seed <= 60; seed++ {
		t.Run(fmt.Sprintf("seed-%d", seed), func(t *testing.T) {
			g := &ftGen{rng: rand.New(rand.NewPCG(seed, 99))}
			envs := g.environments()
			ftRoundTrip(t, envs, "production")
		})
	}
}

// TestFactorPropertyOtherBase factors the same sets around a non-default base.
func TestFactorPropertyOtherBase(t *testing.T) {
	for seed := uint64(100); seed < 130; seed++ {
		t.Run(fmt.Sprintf("seed-%d", seed), func(t *testing.T) {
			g := &ftGen{rng: rand.New(rand.NewPCG(seed, 5))}
			envs := g.environments()
			ftRoundTrip(t, envs, "staging")
		})
	}
}
