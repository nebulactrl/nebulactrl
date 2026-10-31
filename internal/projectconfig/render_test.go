package projectconfig

import (
	"encoding/json"
	"math/rand/v2"
	"reflect"
	"strings"
	"testing"

	toml "github.com/pelletier/go-toml/v2"
)

func rtCanon(v any) string { return rtMust(json.Marshal(v)) }
func rtMust(b []byte, err error) string {
	if err != nil {
		panic(err)
	}
	return string(b)
}

// rtCanonDef is the canonical form of a definition: nil and empty slices and
// maps are equal, only_in and unset (file semantics) are ignored.
func rtCanonDef(d *Service) string {
	if d == nil {
		d = &Service{}
	}
	c := *d
	c.OnlyIn = nil
	c.Unset = nil
	return rtCanon(&c)
}

func rtCanonVars(m map[string]Variable) string {
	if len(m) == 0 {
		return ""
	}
	return rtCanon(m)
}

// rtView builds a resolved view of one environment from service definitions.
func rtView(env string, svcs map[string]*Service) *Resolved {
	r := &Resolved{Environment: env, Services: map[string]*ResolvedService{}}
	for slug, def := range svcs {
		r.Services[slug] = &ResolvedService{Slug: slug, Def: def, Path: "services." + slug}
	}
	return r
}

func rtParse(t *testing.T, text string) (*File, *Index) {
	t.Helper()
	f, idx, diags := Parse([]byte(text), nil)
	if diags.HasErrors() {
		t.Fatalf("parse errors:\n%s\n--- text ---\n%s", diags.RenderText("nebula.toml"), text)
	}
	return f, idx
}

// rtHygiene checks the formatting promises of the renderer.
func rtHygiene(t *testing.T, out []byte) {
	t.Helper()
	s := string(out)
	if !strings.HasSuffix(s, "\n") || strings.HasSuffix(s, "\n\n") {
		t.Errorf("output must end with exactly one newline: %q", s[max(0, len(s)-20):])
	}
	if strings.Contains(s, "\r") {
		t.Errorf("output contains a carriage return")
	}
	if strings.Contains(s, "\n\n\n") {
		t.Errorf("output contains two blank lines in a row")
	}
	for i, line := range strings.Split(s, "\n") {
		if line != strings.TrimRight(line, " \t") {
			t.Errorf("line %d has trailing whitespace: %q", i+1, line)
		}
	}
}

// rtGeneric converts a tree into what a TOML decoder returns for its text.
func rtGeneric(n any) any {
	switch x := n.(type) {
	case *emitTable:
		m := make(map[string]any, len(x.keys))
		for _, k := range x.keys {
			m[k] = rtGeneric(x.vals[k])
		}
		return m
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = rtGeneric(e)
		}
		return out
	}
	return n
}

func TestRenderGolden(t *testing.T) {
	r := rtView("", map[string]*Service{
		"web": {
			Name:    new("Web"),
			Port:    new(3000),
			Command: []string{"bun", "run", "start"},
			Git: &Git{
				Connection: new("github-acme"),
				Repo:       new("acme/shop"),
				Branch:     new("main"),
				AutoDeploy: new(true),
				WatchPaths: []string{"apps/web/**", "!**/*.md"},
			},
			Variables: map[string]Variable{
				"NODE_ENV":   {Value: "production"},
				"APP_URL":    {Value: "https://shop.acme.com"},
				"BUILD_FLAG": {Value: "1", Scope: "build", Comment: "read by the bundler"},
			},
		},
		"db": {Database: &Database{Engine: new("postgres"), Version: new("17")}},
	})
	r.Project = "acme-shop"
	r.Server = "fra-1"
	r.Shared = map[string]Variable{"APP_NAME": {Value: "Acme Shop"}}
	r.SharedSecrets = []string{"STRIPE_SECRET_KEY"}

	want := `#:schema https://nebula.example.com/schema/nebula.toml.json
schema = 1
project = "acme-shop"
server = "fra-1"

[variables]
APP_NAME = "Acme Shop"

[secrets]
keys = ["STRIPE_SECRET_KEY"]

[services.db.database]
engine = "postgres"
version = "17"

[services.web]
name = "Web"
port = 3000
command = ["bun", "run", "start"]

[services.web.git]
connection = "github-acme"
repo = "acme/shop"
branch = "main"
auto_deploy = true
watch_paths = ["apps/web/**", "!**/*.md"]

[services.web.variables]
APP_URL = "https://shop.acme.com"
BUILD_FLAG = { value = "1", scope = "build", comment = "read by the bundler" }
NODE_ENV = "production"
`
	got := Render(r, RenderOptions{Origin: "https://nebula.example.com"})
	if string(got) != want {
		t.Fatalf("golden mismatch\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
	rtHygiene(t, got)
}

func TestRenderScalarsBeforeTables(t *testing.T) {
	r := rtView("", map[string]*Service{
		"web": {
			Image: new("ghcr.io/acme/web:1"),
			Docker: &Docker{
				CapAdd:    []string{"NET_BIND_SERVICE"},
				Sysctls:   map[string]string{"net.core.somaxconn": "1024"},
				User:      new("1000:1000"),
				Ulimits:   map[string]*Ulimit{"nofile": {Soft: new(int64(65536)), Hard: new(int64(65536))}},
				ShmSizeMB: new(256),
				Labels:    map[string]string{"team": "shop"},
				Init:      new(true),
				Devices:   []Device{{HostPath: new("/dev/dri/renderD128"), Permissions: new("rw")}},
			},
			Domains: map[string]*Domain{
				"shop.acme.com": {
					WWWRedirect: new(true),
					Options: &RouteOptions{
						Compress:  new(true),
						Redirects: []Redirect{{Regex: new("^/old"), Permanent: new(true)}},
						Access:    &Access{AllowCIDRs: []string{"203.0.113.0/24"}},
					},
					Cloudflare: &Cloudflare{Proxied: new(true)},
				},
			},
		},
	})
	want := `schema = 1

[services.web]
image = "ghcr.io/acme/web:1"

[services.web.docker]
cap_add = ["NET_BIND_SERVICE"]
user = "1000:1000"
shm_size_mb = 256
init = true
devices = [{ host_path = "/dev/dri/renderD128", permissions = "rw" }]

[services.web.docker.sysctls]
"net.core.somaxconn" = "1024"

[services.web.docker.ulimits.nofile]
soft = 65536
hard = 65536

[services.web.docker.labels]
team = "shop"

[services.web.domains."shop.acme.com"]
www_redirect = true

[services.web.domains."shop.acme.com".options]
compress = true
redirects = [{ regex = "^/old", permanent = true }]

[services.web.domains."shop.acme.com".options.access]
allow_cidrs = ["203.0.113.0/24"]

[services.web.domains."shop.acme.com".cloudflare]
proxied = true
`
	got := Render(r, RenderOptions{})
	if string(got) != want {
		t.Fatalf("golden mismatch\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
	rtHygiene(t, got)
}

func TestRenderVariableForms(t *testing.T) {
	r := &Resolved{Shared: map[string]Variable{
		"A": {Value: "x"},
		"B": {Value: "x", Scope: "runtime"},
		"C": {Value: "x", Comment: "c"},
		"D": {Value: "x", Scope: "both", RotateEveryDays: new(90), Public: true},
		"E": {Value: "x", Scope: "build"},
		"F": {Value: "l1\nl2"},
		"G": {Value: "l1\nl2", Comment: "c"},
		"H": {Value: "x", RotateEveryDays: new(0)},
	}}
	want := `schema = 1

[variables]
A = "x"
B = "x"
C = { value = "x", comment = "c" }
D = { value = "x", scope = "both", rotate_every_days = 90, public = true }
E = { value = "x", scope = "build" }
F = """
l1
l2"""
G = { value = "l1\nl2", comment = "c" }
H = { value = "x", rotate_every_days = 0 }
`
	got := Render(r, RenderOptions{})
	if string(got) != want {
		t.Fatalf("golden mismatch\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
	f, _, _ := Parse(got, nil)
	if f == nil {
		t.Fatal("Parse returned no file")
	}
	if rtCanon(f.Variables) != rtCanon(map[string]Variable{
		"A": {Value: "x"}, "B": {Value: "x"}, "C": {Value: "x", Comment: "c"},
		"D": {Value: "x", Scope: "both", RotateEveryDays: new(90), Public: true},
		"E": {Value: "x", Scope: "build"}, "F": {Value: "l1\nl2"}, "G": {Value: "l1\nl2", Comment: "c"},
		"H": {Value: "x", RotateEveryDays: new(0)},
	}) {
		t.Errorf("variables did not round trip: %s", rtCanon(f.Variables))
	}
}

func TestRenderPresenceAndEmpty(t *testing.T) {
	tests := []struct {
		name string
		r    *Resolved
		want string
	}{
		{"nil view", nil, "schema = 1\n"},
		{"empty view", &Resolved{}, "schema = 1\n"},
		{"empty definition", rtView("", map[string]*Service{"y": {}}), "schema = 1\n\n[services.y]\n"},
		{"nil resolved service", &Resolved{Services: map[string]*ResolvedService{"y": nil}}, "schema = 1\n\n[services.y]\n"},
		{
			"present but zero",
			rtView("", map[string]*Service{"x": {
				Command:   []string{},
				Port:      new(0),
				OneShot:   new(false),
				Variables: map[string]Variable{},
				Scale:     &Scale{},
			}}),
			"schema = 1\n\n[services.x]\nport = 0\ncommand = []\none_shot = false\n\n[services.x.scale]\n\n[services.x.variables]\n",
		},
		{
			"floats",
			rtView("", map[string]*Service{"x": {CPUs: new(2.0)}, "y": {CPUs: new(1.5)}}),
			"schema = 1\n\n[services.x]\ncpus = 2.0\n\n[services.y]\ncpus = 1.5\n",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Render(tc.r, RenderOptions{})
			if string(got) != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRenderEnvironmentLayers(t *testing.T) {
	mk := func(env string) *Resolved {
		return &Resolved{
			Environment:          env,
			Server:               "fra-staging",
			Shared:               map[string]Variable{"APP_NAME": {Value: "Acme"}},
			EnvShared:            map[string]Variable{"APP_NAME": {Value: "Acme (staging)"}, "ONLY_STAGING": {Value: "1"}},
			SharedSecrets:        []string{"A_KEY"},
			EnvSharedSecrets:     []string{"B_KEY"},
			EnvSharedSecretScope: "both",
		}
	}
	t.Run("named environment keeps the layer", func(t *testing.T) {
		want := `schema = 1
server = "fra-staging"

[variables]
APP_NAME = "Acme"

[secrets]
keys = ["A_KEY"]

[environments.staging.variables]
APP_NAME = "Acme (staging)"
ONLY_STAGING = "1"

[environments.staging.secrets]
keys = ["B_KEY"]
scope = "both"
`
		if got := Render(mk("staging"), RenderOptions{}); string(got) != want {
			t.Fatalf("got\n%s\nwant\n%s", got, want)
		}
	})
	t.Run("unnamed view folds the layer", func(t *testing.T) {
		want := `schema = 1
server = "fra-staging"

[variables]
APP_NAME = "Acme (staging)"
ONLY_STAGING = "1"

[secrets]
keys = ["A_KEY", "B_KEY"]
`
		if got := Render(mk(""), RenderOptions{}); string(got) != want {
			t.Fatalf("got\n%s\nwant\n%s", got, want)
		}
	})
}

func TestRenderCommentsAndNotRepresentable(t *testing.T) {
	r := rtView("", map[string]*Service{
		"web": {
			Image:   new("ghcr.io/acme/web:1"),
			Port:    new(3000),
			Domains: map[string]*Domain{"shop.acme.com": {WWWRedirect: new(true)}},
		},
		"db": {Database: &Database{Engine: new("postgres")}},
	})
	r.Project = "acme-shop"
	o := RenderOptions{
		Origin: "https://nebula.example.com/",
		Comments: map[string][]string{
			"project":                              {"The shop"},
			"services.db":                          {"Database"},
			"services.web":                         {"Web app", "second line"},
			"services.web.port":                    {"listen port", ""},
			`services.web.domains."shop.acme.com"`: {"Domain note\nwith newline   "},
			"services.nothing":                     {"never written"},
		},
		NotRepresentable: map[string][]string{
			"web":     {"tailnet", "replicas"},
			"missing": {"x"},
		},
	}
	want := `#:schema https://nebula.example.com/schema/nebula.toml.json
schema = 1
# The shop
project = "acme-shop"

# Database
[services.db]

[services.db.database]
engine = "postgres"

# Web app
# second line
[services.web]
image = "ghcr.io/acme/web:1"
# listen port
#
port = 3000

# Domain note
# with newline
[services.web.domains."shop.acme.com"]
www_redirect = true

# not representable in schema 1: tailnet, replicas
`
	got := Render(r, o)
	if string(got) != want {
		t.Fatalf("golden mismatch\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
	rtHygiene(t, got)
	// The commented output is still the same document.
	plain := Render(r, RenderOptions{})
	var a, b map[string]any
	if err := toml.Unmarshal(got, &a); err != nil {
		t.Fatal(err)
	}
	if err := toml.Unmarshal(plain, &b); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a, b) {
		t.Errorf("comments changed the document: %v vs %v", a, b)
	}
}

func TestRenderDeterministic(t *testing.T) {
	r := rtKitchenSink()
	first := Render(r, RenderOptions{Origin: "https://x.example"})
	for i := 0; i < 20; i++ {
		if got := Render(r, RenderOptions{Origin: "https://x.example"}); string(got) != string(first) {
			t.Fatalf("render %d differs from the first:\n%s\n---\n%s", i, got, first)
		}
	}
	rtHygiene(t, first)
	// Rendering does not touch the input.
	before := rtCanon(r)
	Render(r, RenderOptions{})
	if after := rtCanon(r); after != before {
		t.Errorf("Render mutated its input")
	}
}

func TestEmitString(t *testing.T) {
	tests := []struct {
		name  string
		in    string
		multi bool
		want  string
	}{
		{"plain", "plain", true, `"plain"`},
		{"empty", "", true, `""`},
		{"quote", "a\"b", true, `"a\"b"`},
		{"backslash", `a\b`, true, `"a\\b"`},
		{"tab", "a\tb", true, `"a\tb"`},
		{"control characters", "\x01\x1f\x7f", true, `"\u0001\u001F\u007F"`},
		{"unicode stays", "é✓", true, `"é✓"`},
		{"invalid utf-8", "a\xffb", true, `"a\uFFFDb"`},
		{"newline on one line", "a\nb", false, `"a\nb"`},
		{"newline multi-line", "a\nb\n", true, "\"\"\"\na\nb\n\"\"\""},
		{"leading newline", "\na", true, "\"\"\"\n\na\"\"\""},
		{"triple quotes", "x\"\"\"y\n", true, "\"\"\"\nx\"\"\\\"y\n\"\"\""},
		{"quote at end", "a\n\"", true, "\"\"\"\na\n\\\"\"\"\""},
		{"space before newline", "a \nb", true, "\"\"\"\na\\u0020\nb\"\"\""},
		{"carriage return", "a\r\nb", true, "\"\"\"\na\\r\nb\"\"\""},
		{"backslash multi-line", "a\\\nb", true, "\"\"\"\na\\\\\nb\"\"\""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := emitString(tc.in, tc.multi); got != tc.want {
				t.Errorf("emitString(%q, %v) = %s, want %s", tc.in, tc.multi, got, tc.want)
			}
		})
	}
}

var rtNastyStrings = []string{
	"", "plain", "a\"b", `a\b`, "tab\there", "\x01\x02\x1f\x7f", "é✓ünï", "line1\nline2", "line1\nline2\n",
	"\nleading", "trailing\n\n", "x\"\"\"y", "\"\"\"", "\"\"\"\"\"\"\"", "ends with quote\"", "\"starts with quote",
	"two quotes at end\"\"", "cr\r\nlf\r", "space before nl \nnext", "tabs \t\nnext", "back\\\nslash newline",
	`C:\path\to "x"`, "#hash = [brackets] {braces}", "'single' ''' triple", "${{shared.APP_NAME}} $${{", "\u2028\u2029",
	"  leading and trailing  ", "\\n literal backslash n", "multi\n\n\nblank lines", "a\n \nb", "\"\n\"\n\"",
}

func TestEmitStringRoundTripsThroughTOML(t *testing.T) {
	for _, in := range rtNastyStrings {
		for _, multi := range []bool{false, true} {
			var m map[string]any
			doc := "k = " + emitString(in, multi) + "\n"
			if err := toml.Unmarshal([]byte(doc), &m); err != nil || m["k"] != in {
				t.Errorf("value %q (multi=%v): doc %q decoded to %v, err %v", in, multi, doc, m["k"], err)
			}
			doc = "k = [" + emitValue([]any{in, in}, true)[1:] + "\nt = { x = " + emitString(in, false) + " }\n"
			var m2 struct {
				K []string
				T map[string]string
			}
			if err := toml.Unmarshal([]byte(doc), &m2); err != nil || len(m2.K) != 2 || m2.K[0] != in || m2.T["x"] != in {
				t.Errorf("value %q in array/inline table: doc %q decoded to %+v, err %v", in, doc, m2, err)
			}
		}
		if in == "" {
			continue
		}
		var m map[string]any
		doc := emitKey(in) + " = 1\n"
		if err := toml.Unmarshal([]byte(doc), &m); err != nil {
			t.Errorf("key %q: doc %q: %v", in, doc, err)
			continue
		}
		if _, ok := m[in]; !ok {
			t.Errorf("key %q did not round trip, got %v", in, m)
		}
	}
}

func TestEmitKey(t *testing.T) {
	tests := map[string]string{
		"plain":         "plain",
		"with-dash_1":   "with-dash_1",
		"UPPER":         "UPPER",
		"shop.acme.com": `"shop.acme.com"`,
		"/etc/app.conf": `"/etc/app.conf"`,
		"with space":    `"with space"`,
		"":              `""`,
		"ünï":           `"ünï"`,
		`quo"te`:        `"quo\"te"`,
		"new\nline":     `"new\nline"`,
		"$schema":       `"$schema"`,
	}
	for in, want := range tests {
		if got := emitKey(in); got != want {
			t.Errorf("emitKey(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestEmitFloat(t *testing.T) {
	tests := []struct {
		in   float64
		want string
	}{
		{1.5, "1.5"}, {2, "2.0"}, {0, "0.0"}, {-0.5, "-0.5"}, {0.1, "0.1"}, {100000, "100000.0"},
		{1e21, "1e+21"}, {-3.5e-7, "-3.5e-07"},
	}
	for _, tc := range tests {
		got := emitFloat(tc.in)
		if got != tc.want {
			t.Errorf("emitFloat(%v) = %s, want %s", tc.in, got, tc.want)
		}
		var m map[string]any
		if err := toml.Unmarshal([]byte("f = "+got), &m); err != nil || m["f"] != tc.in {
			t.Errorf("emitFloat(%v) = %s decoded to %v (%T), err %v", tc.in, got, m["f"], m["f"], err)
		}
	}
}

// rtFiller fills every reachable field of a model value, with awkward strings.
type rtFiller struct {
	rng  *rand.Rand
	keys []string
}

var (
	rtFillInts   = []int64{0, 1, 42, -1, 65535, 1 << 40}
	rtFillFloats = []float64{1.5, 2, 0.25, 1e21, -3.5e-7, 0}
	rtKeysWild   = []string{"plain", "with.dot", "with space", "quo\"te", `back\slash`, "new\nline", "ünï", "ctrl\x01", "/abs/path", "host.example.com/prefix"}
	rtKeysMild   = []string{"plain", "with.dot", "with space", "/abs/path", "host.example.com/prefix", "ünï"}
)

func (f *rtFiller) fill(v reflect.Value) {
	switch v.Kind() {
	case reflect.Ptr:
		v.Set(reflect.New(v.Type().Elem()))
		f.fill(v.Elem())
	case reflect.Struct:
		for i := range v.NumField() {
			if v.Type().Field(i).IsExported() {
				f.fill(v.Field(i))
			}
		}
	case reflect.String:
		v.SetString(rtNastyStrings[f.rng.IntN(len(rtNastyStrings))])
	case reflect.Bool:
		v.SetBool(f.rng.IntN(2) == 0)
	case reflect.Int, reflect.Int64:
		v.SetInt(rtFillInts[f.rng.IntN(len(rtFillInts))])
	case reflect.Float64:
		v.SetFloat(rtFillFloats[f.rng.IntN(len(rtFillFloats))])
	case reflect.Slice:
		n := 1 + f.rng.IntN(2)
		s := reflect.MakeSlice(v.Type(), n, n)
		for i := range n {
			f.fill(s.Index(i))
		}
		v.Set(s)
	case reflect.Map:
		m := reflect.MakeMap(v.Type())
		for _, ix := range f.rng.Perm(len(f.keys))[:2] {
			elem := reflect.New(v.Type().Elem()).Elem()
			f.fill(elem)
			m.SetMapIndex(reflect.ValueOf(f.keys[ix]), elem)
		}
		v.Set(m)
	}
}

// TestRenderIsValidTOMLForEveryModelType fills every field of the model with
// awkward values, renders it and requires that an independent TOML decoder
// reads back exactly the tree the renderer meant to write.
func TestRenderIsValidTOMLForEveryModelType(t *testing.T) {
	for seed := int64(1); seed <= 12; seed++ {
		f := &rtFiller{rng: rand.New(rand.NewPCG(uint64(seed), 7)), keys: rtKeysWild}
		def := &Service{}
		f.fill(reflect.ValueOf(def).Elem())
		def.Unset = nil

		file := renderFile(&Resolved{
			Project:       "p",
			Shared:        map[string]Variable{"A": {Value: "x"}},
			SharedSecrets: []string{"K"},
			Services:      map[string]*ResolvedService{"s": {Def: def}},
		})
		tree := toTree(file)
		text := Render(&Resolved{
			Project:       "p",
			Shared:        map[string]Variable{"A": {Value: "x"}},
			SharedSecrets: []string{"K"},
			Services:      map[string]*ResolvedService{"s": {Def: def}},
		}, RenderOptions{})
		rtNoTrailingSpace(t, text)

		var got map[string]any
		if err := toml.Unmarshal(text, &got); err != nil {
			t.Fatalf("seed %d: output is not valid TOML: %v\n%s", seed, err, text)
		}
		want := rtGeneric(tree)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("seed %d: decoded document differs from the tree\n%s", seed, text)
		}
	}
}

// rtNoTrailingSpace checks that no line of out ends in whitespace and that
// out has no carriage returns (filled strings may contain blank lines, so the
// blank-line rules of rtHygiene do not apply).
func rtNoTrailingSpace(t *testing.T, out []byte) {
	t.Helper()
	if strings.Contains(string(out), "\r") {
		t.Errorf("output contains a carriage return")
	}
	for i, line := range strings.Split(string(out), "\n") {
		if line != strings.TrimRight(line, " \t") {
			t.Errorf("line %d has trailing whitespace: %q", i+1, line)
		}
	}
}

// TestRenderRoundTripsThroughDecoder renders filled definitions and requires
// the typed model decoded from the text (the decoder Parse uses, without the
// semantic validation that the nonsensical filled values would trip) to equal
// the original.
func TestRenderRoundTripsThroughDecoder(t *testing.T) {
	for seed := int64(1); seed <= 12; seed++ {
		f := &rtFiller{rng: rand.New(rand.NewPCG(uint64(seed), 7)), keys: rtKeysMild}
		def := &Service{}
		f.fill(reflect.ValueOf(def).Elem())
		def.Unset = nil
		def.Variables["PLAIN"] = Variable{Value: "plain"}

		text := Render(&Resolved{Services: map[string]*ResolvedService{"s": {Def: def}}}, RenderOptions{})
		var tree map[string]any
		if err := toml.Unmarshal(text, &tree); err != nil {
			t.Fatalf("seed %d: invalid TOML: %v\n%s", seed, err, text)
		}
		idx := BuildIndex(text)
		s := &sink{pos: func(p string, value bool) Pos { return idx.at(p, value) }}
		dec := &decoder{s: s, loc: func(p []string) string { return PathString(p...) }, idx: idx}
		var file File
		dec.structInto(reflect.ValueOf(&file).Elem(), tree, nil)
		if dec.errs > 0 || len(s.list) > 0 {
			t.Fatalf("seed %d: decoding failed:\n%s\n%s", seed, s.list.RenderText(""), text)
		}
		if file.Services["s"] == nil {
			t.Fatalf("seed %d: the decoder returned no service\n%s", seed, text)
		}
		want := rtCanon(def)
		if got := rtCanon(file.Services["s"]); got != want {
			t.Fatalf("seed %d: model did not round trip\n got: %s\nwant: %s\n%s", seed, got, want, text)
		}
	}
}

// rtKitchenSink is a valid project with one service of every kind.
func rtKitchenSink() *Resolved {
	r := rtView("", map[string]*Service{
		"web": {
			Name:             new("Web"),
			DependsOn:        []string{"db"},
			Image:            new("ghcr.io/acme/web:1.4.2"),
			Port:             new(3000),
			Command:          []string{"bun", "run", "start"},
			StopGraceSeconds: new(30),
			MemoryMB:         new(512),
			CPUs:             new(1.5),
			ImageRetention:   new(5),
			HealthCheck:      &HealthCheck{Type: new("http"), Path: new("/healthz"), DeadlineSeconds: new(90)},
			PreDeploy:        &Hook{Command: []string{"bun", "run", "db:migrate"}, TimeoutSeconds: new(300)},
			Scale:            &Scale{Replicas: new(2)},
			Uptime:           &Uptime{FailureThreshold: new(3), ReminderMinutes: new(60)},
			Secrets:          &Secrets{Keys: []string{"STRIPE_SECRET_KEY"}},
			Variables: map[string]Variable{
				"NODE_ENV":     {Value: "production"},
				"APP_URL":      {Value: "https://shop.acme.com"},
				"DATABASE_URL": {Value: "${{db.DATABASE_URL}}"},
				"FLAG":         {Value: "1", Comment: "read by the bundler"},
			},
			Domains: map[string]*Domain{
				"shop.acme.com": {
					WWWRedirect: new(true),
					Options:     &RouteOptions{Compress: new(true), SecurityHeaders: new("strict")},
					Cloudflare:  &Cloudflare{Proxied: new(true)},
				},
			},
			Files: map[string]*Files{
				"/etc/app/app.conf": {Mode: new("0644"), Content: new("listen = 8080\nname = \"web\"\n")},
			},
			Tasks: map[string]*Task{
				"digest": {
					Schedule:       new("0 7 * * 1-5"),
					Timezone:       new("Europe/Berlin"),
					Command:        []string{"bun", "run", "jobs:digest"},
					TimeoutSeconds: new(900),
				},
			},
		},
		"cache": {
			Image:   new("redis:7"),
			Volumes: map[string]string{"data": "/data"},
		},
		"api": {
			Git: &Git{
				Connection: new("github-acme"),
				Repo:       new("acme/shop"),
				Branch:     new("main"),
				WatchPaths: []string{"apps/api/**", "packages/**"},
			},
			Build:     &Build{Type: new("dockerfile"), Dockerfile: new("apps/api/Dockerfile"), Context: new(".")},
			Command:   []string{"bun", "run", "api"},
			Variables: map[string]Variable{"LOG_LEVEL": {Value: "info", Scope: "build"}},
		},
		"db": {
			Database: &Database{Engine: new("postgres"), Version: new("17"), Name: new("shop"), Username: new("shop")},
			Backups: map[string]*Backup{
				"nightly": {
					Destination:    new("offsite-s3"),
					Cron:           new("0 2 * * *"),
					Timezone:       new("Europe/Berlin"),
					RetentionCount: new(14),
					RetentionDays:  new(30),
				},
			},
		},
		"printer": {
			Server: new("fra-1"),
			Route: &Route{
				Protocol:   new("http"),
				Upstreams:  []Upstream{{Address: new("192.168.1.20:631")}},
				HealthPath: new("/"),
			},
			Domains: map[string]*Domain{
				"print.acme.com": {Options: &RouteOptions{Access: &Access{AllowCIDRs: []string{"203.0.113.0/24"}}}},
			},
		},
		"tools": {
			Stack: &Stack{
				Compose:     new("services:\n  web:\n    image: nginx:1.27\n    command: [\"nginx\", \"-g\", \"daemon off;\"]\n"),
				WaitSeconds: new(300),
				Resources:   map[string]*StackResource{"web": {MemoryMB: new(256), CPUs: new(0.5)}},
			},
			Domains: map[string]*Domain{"tools.acme.com": {Component: new("web")}},
		},
	})
	r.Project = "acme-shop"
	r.Server = "fra-1"
	r.Shared = map[string]Variable{"APP_NAME": {Value: "Acme Shop"}}
	return r
}

// TestRenderKitchenSinkRoundTrip parses the rendered project and resolves it:
// the resolved definitions must equal the originals.
func TestRenderKitchenSinkRoundTrip(t *testing.T) {
	want := rtKitchenSink()
	text := Render(want, RenderOptions{Origin: "https://nebula.example.com"})
	rtHygiene(t, text)
	f, idx := rtParse(t, string(text))
	got, diags := f.Resolve("", idx)
	if diags.HasErrors() {
		t.Fatalf("resolve errors:\n%s\n%s", diags.RenderText(""), text)
	}
	if got.Project != want.Project || got.Server != want.Server {
		t.Errorf("project/server = %q/%q, want %q/%q", got.Project, got.Server, want.Project, want.Server)
	}
	if rtCanonVars(got.Shared) != rtCanonVars(want.Shared) {
		t.Errorf("shared = %s, want %s", rtCanonVars(got.Shared), rtCanonVars(want.Shared))
	}
	if len(got.Services) != len(want.Services) {
		t.Fatalf("services = %v, want %v", got.SortedSlugs(), want.SortedSlugs())
	}
	for slug, ws := range want.Services {
		gs := got.Services[slug]
		if gs == nil {
			t.Errorf("service %s missing after round trip", slug)
			continue
		}
		if g, w := rtCanonDef(gs.Def), rtCanonDef(ws.Def); g != w {
			t.Errorf("service %s differs\n got: %s\nwant: %s", slug, g, w)
		}
	}
	// Rendering the round-tripped view gives the same text.
	if again := Render(got, RenderOptions{Origin: "https://nebula.example.com"}); string(again) != string(text) {
		t.Errorf("render of the resolved view differs:\n%s\n---\n%s", again, text)
	}
}
