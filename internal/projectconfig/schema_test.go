package projectconfig

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	iofs "io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	toml "github.com/pelletier/go-toml/v2"
)

var updateGolden = flag.Bool("update", false, "rewrite schema/nebula.toml.json from GenerateSchema")

const schemaGoldenPath = "schema/nebula.toml.json"

func schemaDecode(t *testing.T, data []byte) map[string]any {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("schema is not valid JSON: %v", err)
	}
	return doc
}

func schemaCompile(t *testing.T, data []byte) *jsonschema.Resolved {
	t.Helper()
	var s jsonschema.Schema
	if err := json.Unmarshal(data, &s); err != nil {
		t.Fatalf("unmarshal schema: %v", err)
	}
	rs, err := s.Resolve(nil)
	if err != nil {
		t.Fatalf("resolve schema: %v", err)
	}
	return rs
}

func schemaFirstDiff(got, want []byte) string {
	g := strings.Split(string(got), "\n")
	w := strings.Split(string(want), "\n")
	for i := 0; i < len(g) || i < len(w); i++ {
		var gl, wl string
		if i < len(g) {
			gl = g[i]
		}
		if i < len(w) {
			wl = w[i]
		}
		if gl != wl {
			return fmt.Sprintf("line %d:\n generated: %s\n  embedded: %s", i+1, gl, wl)
		}
	}
	return "no difference"
}

func TestSchemaGolden(t *testing.T) {
	got, err := GenerateSchema()
	if err != nil {
		t.Fatalf("GenerateSchema: %v", err)
	}
	again, err := GenerateSchema()
	if err != nil {
		t.Fatalf("GenerateSchema (second run): %v", err)
	}
	if !bytes.Equal(got, again) {
		t.Fatalf("GenerateSchema is not deterministic: %s", schemaFirstDiff(got, again))
	}
	if *updateGolden {
		if err := os.WriteFile(schemaGoldenPath, got, 0o644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		return
	}
	if !bytes.Equal(got, Schema()) {
		t.Fatalf("generated schema differs from %s (run: go test ./internal/projectconfig -run TestSchemaGolden -update): %s",
			schemaGoldenPath, schemaFirstDiff(got, Schema()))
	}
}

func TestSchemaIsServedBytesCopy(t *testing.T) {
	doc := schemaDecode(t, Schema())
	if doc["$id"] != SchemaID {
		t.Errorf("$id = %v, want %s", doc["$id"], SchemaID)
	}
	if doc["$schema"] != schemaDialect {
		t.Errorf("$schema = %v, want %s", doc["$schema"], schemaDialect)
	}
	if doc["title"] != "nebula.toml" {
		t.Errorf("title = %v", doc["title"])
	}
	desc, _ := doc["description"].(string)
	for _, want := range []string{"export", "validate", "plan", "secret"} {
		if !strings.Contains(desc, want) {
			t.Errorf("root description does not mention %q", want)
		}
	}
}

func TestSchemaResolves(t *testing.T) {
	gen, err := GenerateSchema()
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{"embedded": Schema(), "generated": gen} {
		t.Run(name, func(t *testing.T) { schemaCompile(t, data) })
	}
}

var (
	schemaSingleChildren = []string{"items", "additionalProperties", "not", "then", "else", "propertyNames"}
	schemaListChildren   = []string{"allOf", "oneOf", "anyOf"}
)

// walkSchemaProperties calls visit for every `properties` map of the schema
// tree. It does not enter `if`: the condition of a rule only names keys.
func walkSchemaProperties(node any, path string, visit func(path string, props map[string]any)) {
	m, ok := node.(map[string]any)
	if !ok {
		return
	}
	if props, ok := m["properties"].(map[string]any); ok {
		visit(path+"/properties", props)
		for name, p := range props {
			walkSchemaProperties(p, path+"/properties/"+name, visit)
		}
	}
	if defs, ok := m["$defs"].(map[string]any); ok {
		for name, d := range defs {
			walkSchemaProperties(d, path+"/$defs/"+name, visit)
		}
	}
	for _, k := range schemaSingleChildren {
		walkSchemaProperties(m[k], path+"/"+k, visit)
	}
	for _, k := range schemaListChildren {
		if arr, ok := m[k].([]any); ok {
			for i, e := range arr {
				walkSchemaProperties(e, fmt.Sprintf("%s/%s/%d", path, k, i), visit)
			}
		}
	}
}

func TestSchemaCompleteness(t *testing.T) {
	gen, err := GenerateSchema()
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{"embedded": Schema(), "generated": gen} {
		t.Run(name, func(t *testing.T) {
			doc := schemaDecode(t, data)
			count := 0
			walkSchemaProperties(doc, "", func(path string, props map[string]any) {
				for key, v := range props {
					p, ok := v.(map[string]any)
					if !ok {
						continue // boolean schema, e.g. `unset: false`
					}
					count++
					if d, _ := p["description"].(string); strings.TrimSpace(d) == "" {
						t.Errorf("%s/%s has no description", path, key)
					}
					if ex, _ := p["examples"].([]any); len(ex) == 0 {
						t.Errorf("%s/%s has no examples", path, key)
					}
				}
			})
			if count < 200 {
				t.Errorf("walked only %d properties; the schema walk is broken", count)
			}
		})
	}
}

// schemaModelTypes collects the struct types reachable from File through
// fields that carry a toml tag.
func schemaModelTypes() []reflect.Type {
	var out []reflect.Type
	seen := map[reflect.Type]bool{}
	var visit func(t reflect.Type)
	visit = func(t reflect.Type) {
		for {
			switch t.Kind() {
			case reflect.Pointer, reflect.Slice, reflect.Array, reflect.Map:
				t = t.Elem()
				continue
			}
			break
		}
		if t.Kind() != reflect.Struct || seen[t] {
			return
		}
		seen[t] = true
		out = append(out, t)
		for i := range t.NumField() {
			if schemaTomlKey(t.Field(i)) != "" {
				visit(t.Field(i).Type)
			}
		}
	}
	visit(reflect.TypeFor[File]())
	return out
}

// schemaDefProperties lists the property names of a definition, looking into
// the branches of its oneOf.
func schemaDefProperties(def map[string]any) []string {
	var keys []string
	if props, ok := def["properties"].(map[string]any); ok {
		for k := range props {
			keys = append(keys, k)
		}
	}
	if branches, ok := def["oneOf"].([]any); ok {
		for _, b := range branches {
			if bm, ok := b.(map[string]any); ok {
				for _, k := range schemaDefProperties(bm) {
					if !slices.Contains(keys, k) {
						keys = append(keys, k)
					}
				}
			}
		}
	}
	slices.Sort(keys)
	return keys
}

func TestSchemaMatchesModel(t *testing.T) {
	doc := schemaDecode(t, Schema())
	defs, _ := doc["$defs"].(map[string]any)
	if defs == nil {
		t.Fatal("schema has no $defs")
	}
	root := reflect.TypeFor[File]()
	types := schemaModelTypes()

	used := map[string]bool{}
	for _, typ := range types {
		var keys []string
		for i := range typ.NumField() {
			f := typ.Field(i)
			key := schemaTomlKey(f)
			if key == "" {
				continue
			}
			keys = append(keys, key)
			if j, _, _ := strings.Cut(f.Tag.Get("json"), ","); j != key {
				t.Errorf("%s.%s: json tag name %q differs from toml tag name %q", typ.Name(), f.Name, j, key)
			}
		}
		slices.Sort(keys)

		var def map[string]any
		if typ == root {
			def = doc
		} else {
			name, ok := schemaTypeDefs[typ]
			if !ok {
				t.Errorf("%s has no schema definition name", typ.Name())
				continue
			}
			used[name] = true
			def, _ = defs[name].(map[string]any)
			if def == nil {
				t.Errorf("%s: schema has no $defs/%s", typ.Name(), name)
				continue
			}
		}
		got := schemaDefProperties(def)
		if !slices.Equal(got, keys) {
			t.Errorf("%s: schema properties %v differ from toml keys %v", typ.Name(), got, keys)
		}
		for _, r := range schemaRequired[typ] {
			if !slices.Contains(keys, r) {
				t.Errorf("%s: required key %q is not a field", typ.Name(), r)
			}
		}
	}
	for typ, name := range schemaTypeDefs {
		if !slices.Contains(types, typ) {
			t.Errorf("schemaTypeDefs names %s (%s), which File does not reach", typ, name)
		}
	}
	// Every $defs entry is either a model struct or one of the shared defs.
	shared := []string{"slug", "serviceSlug", "name", "argv", "relPath", "cidrList", "varName", "domainKey", "variables", "service", "gitSource", "stackSource"}
	for name := range defs {
		if !used[name] && !slices.Contains(shared, name) {
			t.Errorf("$defs/%s belongs to no model type", name)
		}
	}
}

func schemaInstance(t *testing.T, text string) any {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(text), &v); err != nil {
		t.Fatalf("bad test document %q: %v", text, err)
	}
	return v
}

// t2schemaProcesses writes n JSON process members p0..p(n-1) of a service's processes object.
func t2schemaProcesses(n int) string {
	parts := make([]string, n)
	for i := range parts {
		parts[i] = fmt.Sprintf(`"p%d":{"command":["run"]}`, i)
	}
	return strings.Join(parts, ",")
}

func TestSchemaRules(t *testing.T) {
	rs := schemaCompile(t, Schema())
	svc := func(body string) string {
		return `{"schema":1,"services":{"web":` + body + `}}`
	}
	override := func(body string) string {
		return `{"schema":1,"environments":{"staging":{"services":{"web":` + body + `}}}}`
	}
	const gitURL = `{"url":"https://github.com/acme/shop.git","branch":"main"}`
	tests := []struct {
		name  string
		doc   string
		valid bool
	}{
		{"minimal", `{"schema":1}`, true},
		{"missing schema", `{"services":{}}`, false},
		{"wrong schema version", `{"schema":2}`, false},
		{"unknown root key", `{"schema":1,"bogus":1}`, false},
		{"editor hint key", `{"schema":1,"$schema":"https://example.invalid/s.json"}`, true},
		{"project slug", `{"schema":1,"project":"acme-shop"}`, true},
		{"project slug uppercase", `{"schema":1,"project":"Acme"}`, false},
		{"empty server", `{"schema":1,"server":""}`, false},

		{"image service", svc(`{"image":"nginx"}`), true},
		{"no source key", svc(`{"port":3000}`), false},
		{"two source keys", svc(`{"image":"x","git":` + gitURL + `}`), false},
		{"image and build", svc(`{"image":"x","build":{"type":"auto"}}`), false},
		{"unknown service key", svc(`{"image":"x","bogus":true}`), false},
		{"unset in base service", svc(`{"image":"x","unset":["port"]}`), false},
		{"reserved slug shared", `{"schema":1,"services":{"shared":{"image":"x"}}}`, false},
		{"reserved slug vault", `{"schema":1,"services":{"vault":{"image":"x"}}}`, false},
		{"slug that only starts like a reserved name", `{"schema":1,"services":{"shared-db":{"image":"x"}}}`, true},
		{"uppercase service slug", `{"schema":1,"services":{"Web":{"image":"x"}}}`, false},
		{"memory 0", svc(`{"image":"x","memory_mb":0}`), true},
		{"memory 3", svc(`{"image":"x","memory_mb":3}`), false},
		{"memory 6", svc(`{"image":"x","memory_mb":6}`), true},
		{"port out of range", svc(`{"image":"x","port":70000}`), false},
		{"stop grace 0", svc(`{"image":"x","stop_grace_seconds":0}`), false},
		{"empty command clears", svc(`{"image":"x","command":[]}`), true},
		{"command with string", svc(`{"image":"x","command":"run"}`), false},
		{"depends_on duplicates", svc(`{"image":"x","depends_on":["db","db"]}`), false},
		{"depends_on reserved", svc(`{"image":"x","depends_on":["shared"]}`), false},

		{"override without source", override(`{"port":4000}`), true},
		{"override with unset", override(`{"unset":["scale","domains.\"a.example.com\""]}`), true},
		{"override restates git branch only", override(`{"git":{"branch":"develop"}}`), true},
		{"override restates stack wait only", override(`{"stack":{"wait_seconds":60}}`), true},
		{"override git with unknown key", override(`{"git":{"branch":"develop","bogus":1}}`), false},
		{"override with two sources is not a base rule", override(`{"image":"x","git":{"branch":"b"}}`), true},
		{"override reserved slug", `{"schema":1,"environments":{"staging":{"services":{"team":{"port":1}}}}}`, false},
		{"environment uppercase slug", `{"schema":1,"environments":{"Staging":{}}}`, false},
		{"environment unknown key", `{"schema":1,"environments":{"staging":{"bogus":1}}}`, false},

		{"variables string", `{"schema":1,"variables":{"APP":"x"}}`, true},
		{"variables table", `{"schema":1,"variables":{"APP":{"value":"x","scope":"build","public":true}}}`, true},
		{"variables table without value", `{"schema":1,"variables":{"APP":{"scope":"build"}}}`, false},
		{"variables table with secret key", `{"schema":1,"variables":{"APP":{"value":"x","secret":true}}}`, false},
		{"variables bad scope", `{"schema":1,"variables":{"APP":{"value":"x","scope":"none"}}}`, false},
		{"variables bad name", `{"schema":1,"variables":{"1APP":"x"}}`, false},
		{"variables integer value", `{"schema":1,"variables":{"APP":1}}`, false},
		{"secrets keys", `{"schema":1,"secrets":{"keys":["A_KEY"]}}`, true},
		{"secrets without keys", `{"schema":1,"secrets":{"scope":"both"}}`, false},
		{"secrets duplicate keys", `{"schema":1,"secrets":{"keys":["A","A"]}}`, false},

		{"git url", svc(`{"git":` + gitURL + `}`), true},
		{"git connection and repo", svc(`{"git":{"connection":"gh","repo":"acme/shop","branch":"main","watch_paths":["apps/**"]}}`), true},
		{"git without branch", svc(`{"git":{"url":"https://github.com/acme/shop.git"}}`), false},
		{"git connection without repo", svc(`{"git":{"connection":"gh","branch":"main"}}`), false},
		{"git url with connection", svc(`{"git":{"url":"https://github.com/acme/shop.git","connection":"gh","repo":"a/b","branch":"main"}}`), false},
		{"git url with auto_deploy", svc(`{"git":{"url":"https://github.com/acme/shop.git","branch":"main","auto_deploy":true}}`), false},
		{"git url with watch_paths", svc(`{"git":{"url":"https://github.com/acme/shop.git","branch":"main","watch_paths":["a"]}}`), false},
		{"git url not a url", svc(`{"git":{"url":"just text","branch":"main"}}`), false},
		{"git url scp style", svc(`{"git":{"url":"git@github.com:acme/shop.git","branch":"main"}}`), true},
		{"git bad submodules", svc(`{"git":{"url":"https://github.com/acme/shop.git","branch":"main","submodules":"yes"}}`), false},
		{"build dockerfile", svc(`{"git":` + gitURL + `,"build":{"dockerfile":"apps/web/Dockerfile","context":"."}}`), true},
		{"build absolute dockerfile", svc(`{"git":` + gitURL + `,"build":{"dockerfile":"/etc/passwd"}}`), false},
		{"build parent dockerfile", svc(`{"git":` + gitURL + `,"build":{"dockerfile":"../Dockerfile"}}`), false},
		{"build inner parent context", svc(`{"git":` + gitURL + `,"build":{"context":"a/../b"}}`), false},
		{"build dots in name", svc(`{"git":` + gitURL + `,"build":{"context":"a..b/c"}}`), true},
		{"build bad type", svc(`{"git":` + gitURL + `,"build":{"type":"starter:java"}}`), false},
		{"build starter type", svc(`{"git":` + gitURL + `,"build":{"type":"starter:node"}}`), true},

		{"database", svc(`{"database":{"engine":"postgres","version":"17"}}`), true},
		{"database without engine", svc(`{"database":{"version":"17"}}`), false},
		{"database unknown engine", svc(`{"database":{"engine":"oracle"}}`), false},
		{"database with port", svc(`{"database":{"engine":"postgres"},"port":5432}`), false},
		{"database with domains", svc(`{"database":{"engine":"postgres"},"domains":{"db.example.com":{}}}`), false},
		{"database with generate_domain", svc(`{"database":{"engine":"postgres"},"generate_domain":true}`), false},
		{"database with build", svc(`{"database":{"engine":"postgres"},"build":{}}`), false},
		{"database with backups", svc(`{"database":{"engine":"postgres"},"backups":{"nightly":{"destination":"s3","cron":"0 2 * * *"}}}`), true},
		{"database with variables", svc(`{"database":{"engine":"postgres"},"variables":{"A":"b"}}`), true},
		{"backups on image", svc(`{"image":"x","backups":{"nightly":{"destination":"s3","cron":"0 2 * * *"}}}`), false},
		{"backup without cron", svc(`{"database":{"engine":"postgres"},"backups":{"nightly":{"destination":"s3"}}}`), false},

		{"route", svc(`{"server":"fra-1","route":{"protocol":"http","upstreams":[{"address":"10.0.0.1:80"}]}}`), true},
		{"route managed upstream", svc(`{"route":{"protocol":"tcp","listen_port":25565,"open_to_internet":true,"upstreams":[{"service":"web","port":80}]}}`), true},
		{"route without upstreams", svc(`{"route":{"protocol":"http"}}`), false},
		{"route empty upstreams", svc(`{"route":{"protocol":"http","upstreams":[]}}`), false},
		{"route bad protocol", svc(`{"route":{"protocol":"ftp","upstreams":[{"address":"a:1"}]}}`), false},
		{"route upstream address and service", svc(`{"route":{"protocol":"http","upstreams":[{"address":"a:1","service":"web","port":80}]}}`), false},
		{"route upstream service without port", svc(`{"route":{"protocol":"http","upstreams":[{"service":"web"}]}}`), false},
		{"route upstream reserved service", svc(`{"route":{"protocol":"http","upstreams":[{"service":"shared","port":80}]}}`), false},
		{"route listen port too low", svc(`{"route":{"protocol":"tcp","listen_port":80,"upstreams":[{"address":"a:1"}]}}`), false},
		{"route proxy_protocol 3", svc(`{"route":{"protocol":"tcp","proxy_protocol":3,"upstreams":[{"address":"a:1"}]}}`), false},
		{"route proxy_protocol 2", svc(`{"route":{"protocol":"tcp","proxy_protocol":2,"upstreams":[{"address":"a:1"}]}}`), true},
		{"route with variables", svc(`{"route":{"protocol":"http","upstreams":[{"address":"a:1"}]},"variables":{"A":"b"}}`), false},
		{"route with networks", svc(`{"route":{"protocol":"http","upstreams":[{"address":"a:1"}]},"networks":["n"]}`), false},
		{"route with memory", svc(`{"route":{"protocol":"http","upstreams":[{"address":"a:1"}]},"memory_mb":0}`), false},
		{"route with domains", svc(`{"route":{"protocol":"http","upstreams":[{"address":"a:1"}]},"domains":{"app.example.com":{}}}`), true},

		{"stack compose", svc(`{"stack":{"compose":"services: {}"}}`), true},
		{"stack compose_file", svc(`{"stack":{"compose_file":"docker-compose.yml"}}`), true},
		{"stack both compose forms", svc(`{"stack":{"compose":"x","compose_file":"y.yml"}}`), false},
		{"stack neither compose form", svc(`{"stack":{"wait_seconds":60}}`), false},
		{"stack absolute compose_file", svc(`{"stack":{"compose_file":"/etc/compose.yml"}}`), false},
		{"stack with port", svc(`{"stack":{"compose":"x"},"port":80}`), false},
		{"stack with docker", svc(`{"stack":{"compose":"x"},"docker":{"init":true}}`), false},
		{"stack with domain", svc(`{"stack":{"compose":"x"},"domains":{"app.example.com":{"component":"web"}}}`), true},
		{"stack resources", svc(`{"stack":{"compose":"x","resources":{"web":{"memory_mb":256,"cpus":0.5}}}}`), true},
		{"stack resource tiny memory", svc(`{"stack":{"compose":"x","resources":{"web":{"memory_mb":2}}}}`), false},

		{"domain", svc(`{"image":"x","domains":{"app.example.com":{"target_port":3000,"tls":"auto"}}}`), true},
		{"domain with path", svc(`{"image":"x","domains":{"example.com/api":{"strip_prefix":true}}}`), true},
		{"domain wildcard", svc(`{"image":"x","domains":{"*.example.com":{}}}`), true},
		{"domain key not a host", svc(`{"image":"x","domains":{"not a host":{}}}`), false},
		{"domain key uppercase", svc(`{"image":"x","domains":{"App.Example.com":{}}}`), false},
		{"domain redirect without target", svc(`{"image":"x","domains":{"a.example.com":{"kind":"redirect"}}}`), false},
		{"domain redirect", svc(`{"image":"x","domains":{"a.example.com":{"kind":"redirect","redirect_to":"b.example.com","redirect_code":301}}}`), true},
		{"domain bad redirect code", svc(`{"image":"x","domains":{"a.example.com":{"kind":"redirect","redirect_to":"b.example.com","redirect_code":303}}}`), false},
		{"domain bad tls", svc(`{"image":"x","domains":{"a.example.com":{"tls":"maybe"}}}`), false},
		{"domain options", svc(`{"image":"x","domains":{"a.example.com":{"options":{"force_https":true,"cors":{"origins":["*"]},"rate_limit":{"average":10,"period_seconds":1,"burst":20},"redirects":[{"regex":"^a","replacement":"b"}]}}}}`), true},
		{"domain cors without origins", svc(`{"image":"x","domains":{"a.example.com":{"options":{"cors":{"methods":["GET"]}}}}}`), false},
		{"domain rate limit incomplete", svc(`{"image":"x","domains":{"a.example.com":{"options":{"rate_limit":{"average":10}}}}}`), false},
		{"domain bad security headers", svc(`{"image":"x","domains":{"a.example.com":{"options":{"security_headers":"loose"}}}}`), false},
		{"domain options unknown key", svc(`{"image":"x","domains":{"a.example.com":{"options":{"basic_auth_users":[]}}}}`), false},
		{"domain cloudflare", svc(`{"image":"x","domains":{"a.example.com":{"cloudflare":{"proxied":true,"origin_lock":false}}}}`), true},
		{"domain cloudflare manage removed", svc(`{"image":"x","domains":{"a.example.com":{"cloudflare":{"manage":true}}}}`), false},

		{"files content", svc(`{"image":"x","files":{"/etc/app.conf":{"content":"a=1","mode":"0600"}}}`), true},
		{"files source", svc(`{"image":"x","files":{"/etc/app.conf":{"source":"config/app.conf"}}}`), true},
		{"files both", svc(`{"image":"x","files":{"/etc/app.conf":{"content":"a","source":"b"}}}`), false},
		{"files neither", svc(`{"image":"x","files":{"/etc/app.conf":{"mode":"0600"}}}`), false},
		{"files relative target", svc(`{"image":"x","files":{"etc/app.conf":{"content":"a"}}}`), false},
		{"files bad mode", svc(`{"image":"x","files":{"/etc/app.conf":{"content":"a","mode":"rw"}}}`), false},
		{"volumes", svc(`{"image":"x","volumes":{"uploads":"/app/uploads"}}`), true},
		{"volume relative mount", svc(`{"image":"x","volumes":{"uploads":"app/uploads"}}`), false},
		{"volume bad name", svc(`{"image":"x","volumes":{"Up Loads":"/app/uploads"}}`), false},

		{"health check http", svc(`{"image":"x","health_check":{"type":"http","path":"/healthz","interval_seconds":5}}`), true},
		{"health check exec", svc(`{"image":"x","health_check":{"type":"exec","command":["CMD","true"]}}`), true},
		{"health check short exec", svc(`{"image":"x","health_check":{"type":"exec","command":["CMD"]}}`), false},
		{"health check path without slash", svc(`{"image":"x","health_check":{"path":"healthz"}}`), false},
		{"health check bad type", svc(`{"image":"x","health_check":{"type":"grpc"}}`), false},
		{"pre_deploy", svc(`{"image":"x","pre_deploy":{"command":["bun","run","migrate"],"timeout_seconds":300}}`), true},
		{"pre_deploy without command", svc(`{"image":"x","pre_deploy":{"timeout_seconds":300}}`), false},
		{"pre_deploy empty command", svc(`{"image":"x","pre_deploy":{"command":[]}}`), false},
		{"pre_deploy timeout too large", svc(`{"image":"x","pre_deploy":{"command":["a"],"timeout_seconds":4000}}`), false},

		{"scale", svc(`{"image":"x","scale":{"replicas":2,"autoscaling":{"enabled":true,"min":1,"max":4,"target_cpu_percent":70}}}`), true},
		{"scale too many replicas", svc(`{"image":"x","scale":{"replicas":11}}`), false},
		{"autoscaling cpu target too low", svc(`{"image":"x","scale":{"autoscaling":{"target_cpu_percent":10}}}`), false},
		{"uptime", svc(`{"image":"x","uptime":{"enabled":true,"failure_threshold":3}}`), true},
		{"uptime threshold too high", svc(`{"image":"x","uptime":{"failure_threshold":21}}`), false},
		{"task", svc(`{"image":"x","tasks":{"digest":{"schedule":"0 7 * * *","command":["bun","run","digest"]}}}`), true},
		{"task without command", svc(`{"image":"x","tasks":{"digest":{"schedule":"0 7 * * *"}}}`), false},
		{"task bad concurrency", svc(`{"image":"x","tasks":{"digest":{"schedule":"@daily","command":["a"],"concurrency":"queue"}}}`), false},
		{"task bad name", svc(`{"image":"x","tasks":{"Digest Job":{"schedule":"@daily","command":["a"]}}}`), false},

		{"process", svc(`{"image":"x","processes":{"worker":{"command":["run","worker"]}}}`), true},
		{"process with every key", svc(`{"image":"x","process_name":"app","singleton":false,"processes":{"worker":{"command":["run"],"port":3001,"stop_grace_seconds":120,"singleton":true,"memory_mb":256,"cpus":0.5,"health_check":{"type":"tcp","port":3001},"scale":{"replicas":2,"autoscaling":{"enabled":false,"min":1,"max":3,"target_cpu_percent":70}}}}}`), true},
		{"process without command", svc(`{"image":"x","processes":{"worker":{"port":3001}}}`), false},
		{"process with empty command", svc(`{"image":"x","processes":{"worker":{"command":[]}}}`), false},
		{"process command with string", svc(`{"image":"x","processes":{"worker":{"command":"run"}}}`), false},
		{"process unknown key", svc(`{"image":"x","processes":{"worker":{"command":["run"],"bogus":1}}}`), false},
		{"process name upper-case", svc(`{"image":"x","processes":{"Worker":{"command":["run"]}}}`), false},
		{"process name with underscore", svc(`{"image":"x","processes":{"my_worker":{"command":["run"]}}}`), false},
		{"process name with hyphen", svc(`{"image":"x","processes":{"my-worker":{"command":["run"]}}}`), true},
		{"process name too long", svc(`{"image":"x","processes":{"` + strings.Repeat("a", 32) + `":{"command":["run"]}}}`), false},
		{"process port out of range", svc(`{"image":"x","processes":{"worker":{"command":["run"],"port":70000}}}`), false},
		{"process stop grace 0", svc(`{"image":"x","processes":{"worker":{"command":["run"],"stop_grace_seconds":0}}}`), false},
		{"process memory 3", svc(`{"image":"x","processes":{"worker":{"command":["run"],"memory_mb":3}}}`), false},
		{"process memory 6", svc(`{"image":"x","processes":{"worker":{"command":["run"],"memory_mb":6}}}`), true},
		{"process scale too many replicas", svc(`{"image":"x","processes":{"worker":{"command":["run"],"scale":{"replicas":11}}}}`), false},
		{"process health check bad type", svc(`{"image":"x","processes":{"worker":{"command":["run"],"health_check":{"type":"grpc"}}}}`), false},
		{"nine processes", svc(`{"image":"x","processes":{` + t2schemaProcesses(9) + `}}`), true},
		{"ten processes", svc(`{"image":"x","processes":{` + t2schemaProcesses(10) + `}}`), false},
		{"process_name invalid", svc(`{"image":"x","process_name":"Web"}`), false},
		{"singleton not a bool", svc(`{"image":"x","singleton":"yes"}`), false},
		{"processes on a database", svc(`{"database":{"engine":"postgres"},"processes":{"worker":{"command":["run"]}}}`), false},
		{"process_name on a database", svc(`{"database":{"engine":"postgres"},"process_name":"web"}`), false},
		{"singleton on a database", svc(`{"database":{"engine":"postgres"},"singleton":true}`), false},
		{"processes on a stack", svc(`{"stack":{"compose":"x"},"processes":{"worker":{"command":["run"]}}}`), false},
		{"processes on a route", svc(`{"server":"fra-1","route":{"protocol":"http","upstreams":[{"address":"a:1"}]},"processes":{"worker":{"command":["run"]}}}`), false},
		{"processes on a git service", svc(`{"git":` + gitURL + `,"processes":{"worker":{"command":["run"]}}}`), true},
		{"domain with process", svc(`{"image":"x","domains":{"app.example.com":{"process":"worker"}}}`), true},
		{"domain with bad process", svc(`{"image":"x","domains":{"app.example.com":{"process":"Worker"}}}`), false},
		{"override restates one key of a process", override(`{"processes":{"worker":{"scale":{"replicas":1}}}}`), true},
		{"override unsets a process", override(`{"unset":["processes.worker","processes.clock.scale"]}`), true},
		{"override process with unknown key", override(`{"processes":{"worker":{"bogus":1}}}`), false},

		{"docker options", svc(`{"image":"x","docker":{"cap_drop":["ALL"],"dns":["1.1.1.1"],"ulimits":{"nofile":{"soft":65536,"hard":65536}},"tmpfs":{"/tmp":{"size_mb":64,"mode":"1777"}},"host_sysctls":{"vm.max_map_count":262144}}}`), true},
		{"docker too many dns", svc(`{"image":"x","docker":{"dns":["1.1.1.1","8.8.8.8","9.9.9.9","4.4.4.4"]}}`), false},
		{"docker unknown ulimit", svc(`{"image":"x","docker":{"ulimits":{"cpu":{"soft":1,"hard":1}}}}`), false},
		{"docker ulimit incomplete", svc(`{"image":"x","docker":{"ulimits":{"nofile":{"soft":1}}}}`), false},
		{"docker tmpfs relative", svc(`{"image":"x","docker":{"tmpfs":{"tmp":{"size_mb":1}}}}`), false},
		{"docker device", svc(`{"image":"x","docker":{"devices":[{"host_path":"/dev/dri/renderD128","permissions":"rw"}]}}`), true},
		{"docker device outside dev", svc(`{"image":"x","docker":{"devices":[{"host_path":"/etc/passwd"}]}}`), false},
		{"docker gpus", svc(`{"image":"x","docker":{"gpus":{"count":-1,"driver":"nvidia"}}}`), true},
		{"docker gpus other driver", svc(`{"image":"x","docker":{"gpus":{"count":1,"driver":"amd"}}}`), false},
		{"docker group_add not numeric", svc(`{"image":"x","docker":{"group_add":["video"]}}`), false},
		{"docker entrypoint empty", svc(`{"image":"x","docker":{"entrypoint":[]}}`), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := rs.Validate(schemaInstance(t, tc.doc))
			if tc.valid && err != nil {
				t.Errorf("want valid, got: %v\n%s", err, tc.doc)
			}
			if !tc.valid && err == nil {
				t.Errorf("want invalid, got valid\n%s", tc.doc)
			}
		})
	}
}

// fixtureFiles lists the .toml files under dir, or nothing when dir is absent.
func fixtureFiles(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(dir, func(path string, d iofs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return filepath.SkipDir
			}
			return err
		}
		if !d.IsDir() && filepath.Ext(path) == ".toml" {
			out = append(out, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}
	slices.Sort(out)
	return out
}

// fixtureAsJSON reads a TOML document into the JSON value model the schema
// validator expects.
func fixtureAsJSON(data []byte) (any, error) {
	var m map[string]any
	if err := toml.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	b, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return nil, err
	}
	return v, nil
}

func TestFixturesValidateAgainstSchema(t *testing.T) {
	rs := schemaCompile(t, Schema())
	for _, path := range fixtureFiles(t, filepath.Join("testdata", "valid")) {
		t.Run(filepath.ToSlash(path), func(t *testing.T) {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			v, err := fixtureAsJSON(data)
			if err != nil {
				t.Fatalf("fixture is not valid TOML: %v", err)
			}
			if err := rs.Validate(v); err != nil {
				t.Errorf("schema rejects a valid fixture: %v", err)
			}
		})
	}
}

// fixtureHeader reads the leading comment lines of an invalid fixture:
// `# expect: code[,code...]` and the optional `# schema: reject`.
func fixtureHeader(data []byte) (expect []string, reject bool) {
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "#") {
			break
		}
		body := strings.TrimSpace(strings.TrimPrefix(line, "#"))
		if rest, ok := strings.CutPrefix(body, "expect:"); ok {
			for _, c := range strings.Split(rest, ",") {
				if c = strings.TrimSpace(c); c != "" {
					expect = append(expect, c)
				}
			}
		}
		if rest, ok := strings.CutPrefix(body, "schema:"); ok && strings.TrimSpace(rest) == "reject" {
			reject = true
		}
	}
	return expect, reject
}

func TestInvalidFixturesStructural(t *testing.T) {
	rs := schemaCompile(t, Schema())
	for _, path := range fixtureFiles(t, filepath.Join("testdata", "invalid")) {
		t.Run(filepath.ToSlash(path), func(t *testing.T) {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			expect, reject := fixtureHeader(data)
			if len(expect) == 0 {
				t.Fatalf("fixture has no `# expect: <code>` header")
			}
			if !reject {
				return
			}
			v, err := fixtureAsJSON(data)
			if err != nil {
				t.Fatalf("`# schema: reject` fixtures must be valid TOML: %v", err)
			}
			if err := rs.Validate(v); err == nil {
				t.Errorf("schema accepts a fixture annotated `# schema: reject` (expect %v)", expect)
			}
		})
	}
}

func TestFixtureHeader(t *testing.T) {
	tests := []struct {
		name       string
		text       string
		wantExpect []string
		wantReject bool
	}{
		{"single code", "# expect: unknown_key\nschema = 1\n", []string{"unknown_key"}, false},
		{"several codes", "# expect: a, b ,c\n", []string{"a", "b", "c"}, false},
		{"reject", "# expect: wrong_type\n# schema: reject\nschema = 1\n", []string{"wrong_type"}, true},
		{"other schema value", "# expect: x\n# schema: accept\n", []string{"x"}, false},
		{"stops at first non comment", "schema = 1\n# expect: late\n", nil, false},
		{"no header", "", nil, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			expect, reject := fixtureHeader([]byte(tc.text))
			if !slices.Equal(expect, tc.wantExpect) || reject != tc.wantReject {
				t.Errorf("got (%v, %v), want (%v, %v)", expect, reject, tc.wantExpect, tc.wantReject)
			}
		})
	}
}
