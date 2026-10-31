package projectconfig

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"github.com/nebulactrl/nebulactrl/internal/databases/engines"
)

// SchemaID is the $id of the JSON Schema of nebula.toml. The host is a
// placeholder: the server that serves the file answers on its own origin.
const SchemaID = "https://origin.nebulactrl.invalid/schema/nebula.toml.json"

const schemaDialect = "https://json-schema.org/draft/2020-12/schema"

// schemaRootDescription is the root description of the schema: the short AI
// authoring guide.
const schemaRootDescription = "NebulaCtrl project file. One file describes a project: services, databases, domains, variables (names and references only), tasks, uptime monitors, backup schedules and per-environment overrides.\n\n" +
	"Authoring guide:\n" +
	"1. Start from `nebulactrl config export` of the live project, or `config init` for a new repository. Never write the file from memory.\n" +
	"2. Always run validate, then plan, before apply. Read every diagnostic and fix errors by their path, line and suggestions. Apply only a plan with valid: true.\n" +
	"3. Never write secret values. Use `secrets.keys = [\"NAME\"]`, a generator such as `${{secret(32)}}` or a reference such as `${{db.DATABASE_URL}}`. Never put tokens, passwords or private keys in the file or in URLs.\n" +
	"4. Use only keys from this schema; unknown keys are errors. Keys are snake_case, units are in the key name, variable values are always strings.\n" +
	"5. One source key per service (image, git, database, route or stack); the key decides the kind.\n" +
	"6. Tables merge by key; scalars and arrays replace. Put per-environment values inside [environments.<slug>] and drop keys there with unset.\n" +
	"7. A key you write is owned by the file and overwrites dashboard edits; a key you omit is left alone.\n" +
	"8. Apply stages changes and never deploys; review and deploy them afterwards."

//go:embed schema/nebula.toml.json
var schemaJSON []byte

// Schema returns the JSON Schema of nebula.toml (draft 2020-12), the bytes of
// schema/nebula.toml.json. The slice is shared: callers must not modify it.
func Schema() []byte { return schemaJSON }

// schemaTypeDefs names the $defs entry of every struct of the model but File,
// which is the root of the schema. Service is the body shared by base services
// and environment overrides; the `service` def adds the kind rules on top.
var schemaTypeDefs = map[reflect.Type]string{
	reflect.TypeFor[Environment]():   "environment",
	reflect.TypeFor[Secrets]():       "secrets",
	reflect.TypeFor[Variable]():      "variableEntry",
	reflect.TypeFor[Service]():       "serviceBody",
	reflect.TypeFor[Git]():           "git",
	reflect.TypeFor[Build]():         "build",
	reflect.TypeFor[HealthCheck]():   "healthCheck",
	reflect.TypeFor[Hook]():          "hook",
	reflect.TypeFor[Database]():      "database",
	reflect.TypeFor[Route]():         "route",
	reflect.TypeFor[Upstream]():      "upstream",
	reflect.TypeFor[Stack]():         "stack",
	reflect.TypeFor[StackResource](): "stackResource",
	reflect.TypeFor[Domain]():        "domain",
	reflect.TypeFor[Cloudflare]():    "cloudflare",
	reflect.TypeFor[RouteOptions]():  "routeOptions",
	reflect.TypeFor[CORS]():          "cors",
	reflect.TypeFor[Redirect]():      "redirect",
	reflect.TypeFor[RateLimit]():     "rateLimit",
	reflect.TypeFor[Access]():        "access",
	reflect.TypeFor[Files]():         "file",
	reflect.TypeFor[Scale]():         "scale",
	reflect.TypeFor[Autoscaling]():   "autoscaling",
	reflect.TypeFor[Uptime]():        "uptime",
	reflect.TypeFor[Task]():          "task",
	reflect.TypeFor[Process]():       "process",
	reflect.TypeFor[Backup]():        "backup",
	reflect.TypeFor[Docker]():        "docker",
	reflect.TypeFor[Ulimit]():        "ulimit",
	reflect.TypeFor[Tmpfs]():         "tmpfs",
	reflect.TypeFor[Device]():        "device",
	reflect.TypeFor[GPUs]():          "gpus",
}

// schemaRequired lists the required keys of each table, in the order the
// schema writes them. A table that is absent here has no required key.
var schemaRequired = map[reflect.Type][]string{
	reflect.TypeFor[File]():      {"schema"},
	reflect.TypeFor[Variable]():  {"value"},
	reflect.TypeFor[Secrets]():   {"keys"},
	reflect.TypeFor[Git]():       {"branch"},
	reflect.TypeFor[Hook]():      {"command"},
	reflect.TypeFor[Database]():  {"engine"},
	reflect.TypeFor[Route]():     {"protocol", "upstreams"},
	reflect.TypeFor[Task]():      {"schedule", "command"},
	reflect.TypeFor[Backup]():    {"destination", "cron"},
	reflect.TypeFor[CORS]():      {"origins"},
	reflect.TypeFor[Redirect]():  {"regex", "replacement"},
	reflect.TypeFor[RateLimit](): {"average", "period_seconds", "burst"},
	reflect.TypeFor[Ulimit]():    {"soft", "hard"},
	reflect.TypeFor[Device]():    {"host_path"},
	reflect.TypeFor[GPUs]():      {"count", "driver"},
}

// Keys below are "Struct.toml_key".

// schemaPropRefs replaces the whole property schema with a $ref to a shared
// definition. Only default and minItems survive next to the $ref.
var schemaPropRefs = map[string]string{
	"File.project":       "slug",
	"Upstream.service":   "serviceSlug",
	"Route.allow_cidrs":  "cidrList",
	"Route.deny_cidrs":   "cidrList",
	"Access.allow_cidrs": "cidrList",
	"Access.deny_cidrs":  "cidrList",
	"Service.command":    "argv",
	"Hook.command":       "argv",
	"Task.command":       "argv",
	"Process.command":    "argv",
	"Docker.entrypoint":  "argv",
	"Build.dockerfile":   "relPath",
	"Build.context":      "relPath",
	"Stack.compose_file": "relPath",
	"Files.source":       "relPath",
	"Git.connection":     "name",
	"Build.server":       "name",
	"Build.registry":     "name",
	"Domain.certificate": "name",
	"Backup.destination": "name",
}

// schemaItemRefs makes the items of a unique string array a $ref.
var schemaItemRefs = map[string]string{
	"Service.only_in":    "slug",
	"Service.depends_on": "serviceSlug",
	"Service.networks":   "name",
	"Secrets.keys":       "varName",
}

// schemaItemExtras adds keywords to the items of a string array.
var schemaItemExtras = map[string]map[string]any{
	"Git.watch_paths":  {"minLength": 1, "maxLength": 255},
	"Service.unset":    {"maxLength": 300},
	"Docker.group_add": {"pattern": "^[0-9]+$"},
}

// schemaPropExtras adds keywords that no tag can express to a property.
var schemaPropExtras = map[string]map[string]any{
	"Git.url":                 {"pattern": `^(https?://[^/@\s]+(/\S*)?|git://\S+|ssh://[^@\s]*@?\S+|git@\S+:\S+)$`},
	"Service.memory_mb":       {"not": map[string]any{"minimum": 1, "maximum": 5}},
	"StackResource.memory_mb": {"not": map[string]any{"minimum": 1, "maximum": 5}},
	"Process.memory_mb":       {"not": map[string]any{"minimum": 1, "maximum": 5}},
}

// schemaMapExtras adds keywords to an object property that is a map: size
// limits, the rule for its keys and, where it differs, the value schema.
var schemaMapExtras = map[string]map[string]any{
	"File.services": {
		"maxProperties":        MaxServices,
		"propertyNames":        schemaRef("serviceSlug"),
		"additionalProperties": schemaRef("service"),
	},
	"File.environments": {
		"maxProperties": MaxEnvironments,
		"propertyNames": schemaRef("slug"),
	},
	"Environment.services": {
		"propertyNames": schemaRef("serviceSlug"),
	},
	"Service.domains": {
		"maxProperties": MaxDomainsPerService,
		"propertyNames": schemaRef("domainKey"),
	},
	"Service.volumes": {
		"maxProperties":        MaxVolumesPerService,
		"propertyNames":        map[string]any{"pattern": "^[a-z0-9][a-z0-9_.-]{0,62}$"},
		"additionalProperties": map[string]any{"type": "string", "pattern": "^/.+"},
	},
	"Service.files": {
		"maxProperties": MaxFilesPerService,
		"propertyNames": map[string]any{"pattern": "^/.+"},
	},
	"Service.tasks": {
		"maxProperties": MaxTasksPerService,
		"propertyNames": map[string]any{"pattern": "^[a-z0-9][a-z0-9_-]{0,62}$"},
	},
	"Service.processes": {
		"maxProperties": MaxProcessesPerService,
		"propertyNames": map[string]any{"pattern": "^[a-z0-9]([a-z0-9-]{0,29}[a-z0-9])?$"},
	},
	"Service.backups": {
		"maxProperties": MaxBackupsPerService,
		"propertyNames": map[string]any{"pattern": "^[a-z0-9][a-z0-9_-]{0,62}$"},
	},
	"Docker.ulimits": {
		"propertyNames": map[string]any{"enum": []string{"nofile", "memlock"}},
	},
	"Docker.tmpfs": {
		"propertyNames": map[string]any{"pattern": "^/.+"},
	},
}

// schemaExampleFallbacks gives the example of map properties that have no
// example tag and no value that could be derived from their type.
var schemaExampleFallbacks = map[string]any{
	"Environment.variables": map[string]any{"APP_NAME": "Acme Shop (staging)"},
	"Service.variables":     map[string]any{"LOG_LEVEL": "info"},
	"Stack.resources":       map[string]any{"web": map[string]any{"memory_mb": 256, "cpus": 0.5}},
}

// GenerateSchema builds the JSON Schema of nebula.toml from the struct tags of
// File and the post-processing rules of this file. The output is
// deterministic: object keys are sorted, indent is two spaces, and the text
// ends in a newline. It is pinned by the golden test against Schema().
func GenerateSchema() ([]byte, error) {
	g := &schemaGen{defs: map[string]any{}}
	root, err := g.object(reflect.TypeFor[File]())
	if err != nil {
		return nil, err
	}
	g.shared()
	root["$schema"] = schemaDialect
	root["$id"] = SchemaID
	root["title"] = "nebula.toml"
	root["description"] = schemaRootDescription
	root["$defs"] = g.defs

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(root); err != nil {
		return nil, fmt.Errorf("projectconfig: encoding schema: %w", err)
	}
	return buf.Bytes(), nil
}

type schemaGen struct {
	defs map[string]any
}

func schemaRef(name string) map[string]any {
	return map[string]any{"$ref": "#/$defs/" + name}
}

func schemaRequire(keys ...string) map[string]any {
	return map[string]any{"required": keys}
}

// schemaForbid is satisfied by an object that has none of the keys.
func schemaForbid(keys ...string) map[string]any {
	opts := make([]any, len(keys))
	for i, k := range keys {
		opts[i] = schemaRequire(k)
	}
	return map[string]any{"not": map[string]any{"anyOf": opts}}
}

func schemaWhen(cond, then map[string]any) map[string]any {
	return map[string]any{"if": cond, "then": then}
}

// schemaExactlyOne is satisfied by an object that has exactly one of the key
// sets.
func schemaExactlyOne(sets ...[]string) []any {
	out := make([]any, len(sets))
	for i, keys := range sets {
		out[i] = schemaRequire(keys...)
	}
	return out
}

func schemaTomlKey(f reflect.StructField) string {
	key, _, _ := strings.Cut(f.Tag.Get("toml"), ",")
	if key == "-" {
		return ""
	}
	return key
}

func schemaDeref(t reflect.Type) reflect.Type {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t
}

func schemaJSONKind(k reflect.Kind) string {
	switch k {
	case reflect.String:
		return "string"
	case reflect.Bool:
		return "boolean"
	case reflect.Int, reflect.Int64:
		return "integer"
	case reflect.Float64:
		return "number"
	}
	return ""
}

// schemaParseScalar reads a tag value as the JSON type of the field.
func schemaParseScalar(kind reflect.Kind, raw string) (any, error) {
	switch kind {
	case reflect.String:
		return raw, nil
	case reflect.Bool:
		return strconv.ParseBool(raw)
	case reflect.Int, reflect.Int64:
		return strconv.ParseInt(raw, 10, 64)
	case reflect.Float64:
		return strconv.ParseFloat(raw, 64)
	}
	return nil, fmt.Errorf("unsupported kind %s", kind)
}

func schemaIsScalar(k reflect.Kind) bool {
	return schemaJSONKind(k) != ""
}

// object builds the object schema of a struct: its properties, closed with
// additionalProperties false, and the required list.
func (g *schemaGen) object(t reflect.Type) (map[string]any, error) {
	props := map[string]any{}
	for i := range t.NumField() {
		f := t.Field(i)
		key := schemaTomlKey(f)
		if key == "" {
			continue
		}
		ps, err := g.property(t, f)
		if err != nil {
			return nil, err
		}
		props[key] = ps
	}
	s := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties":           props,
	}
	if req := schemaRequired[t]; len(req) > 0 {
		for _, k := range req {
			if _, ok := props[k]; !ok {
				return nil, fmt.Errorf("projectconfig: %s requires unknown key %q", t.Name(), k)
			}
		}
		s["required"] = slices.Clone(req)
	}
	return s, nil
}

// def returns the name of the $defs entry of t, building it on first use.
func (g *schemaGen) def(t reflect.Type) (string, error) {
	name, ok := schemaTypeDefs[t]
	if !ok {
		return "", fmt.Errorf("projectconfig: no schema definition for %s", t)
	}
	if _, done := g.defs[name]; done {
		return name, nil
	}
	g.defs[name] = nil // reserves the name while the struct is built
	s, err := g.object(t)
	if err != nil {
		return "", err
	}
	g.defs[name] = g.refine(name, s)
	return name, nil
}

// refine adds the cross-key rules of a table that tags cannot express.
func (g *schemaGen) refine(name string, s map[string]any) any {
	switch name {
	case "upstream":
		props := s["properties"].(map[string]any)
		branch := func(keys ...string) map[string]any {
			sub := map[string]any{}
			for _, k := range keys {
				sub[k] = props[k]
			}
			return map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"required":             keys,
				"properties":           sub,
			}
		}
		return map[string]any{"oneOf": []any{branch("address"), branch("service", "port")}}
	case "file":
		s["oneOf"] = schemaExactlyOne([]string{"content"}, []string{"source"})
	case "domain":
		s["allOf"] = []any{
			schemaWhen(map[string]any{
				"properties": map[string]any{"kind": map[string]any{"const": "redirect"}},
				"required":   []string{"kind"},
			}, schemaRequire("redirect_to")),
		}
	}
	return s
}

// shared adds the definitions that are not a struct of the model.
func (g *schemaGen) shared() {
	const slugPattern = "^[a-z0-9][a-z0-9-]{0,62}$"
	g.defs["slug"] = map[string]any{"type": "string", "pattern": slugPattern}
	g.defs["serviceSlug"] = map[string]any{
		"type":    "string",
		"pattern": slugPattern,
		"not":     map[string]any{"enum": []string{"shared", "team", "vault", "doppler"}},
	}
	g.defs["name"] = map[string]any{"type": "string", "minLength": 1, "maxLength": 200}
	g.defs["argv"] = map[string]any{
		"type":     "array",
		"maxItems": 64,
		"items":    map[string]any{"type": "string", "maxLength": 4096},
	}
	g.defs["relPath"] = map[string]any{
		"type":      "string",
		"minLength": 1,
		"maxLength": 512,
		"pattern":   "^[^/]",
		"not":       map[string]any{"pattern": `(^|/)\.\.(/|$)`},
	}
	g.defs["cidrList"] = map[string]any{
		"type":     "array",
		"maxItems": 200,
		"items":    map[string]any{"type": "string", "maxLength": 64},
	}
	g.defs["varName"] = map[string]any{"type": "string", "pattern": "^[A-Za-z_][A-Za-z0-9_]{0,254}$"}
	g.defs["domainKey"] = map[string]any{
		"type":      "string",
		"maxLength": 300,
		"pattern":   `^(\*\.)?[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+(/[^\s]*)?$`,
	}
	g.defs["variables"] = map[string]any{
		"type":          "object",
		"maxProperties": MaxVariablesPerLayer,
		"propertyNames": schemaRef("varName"),
		"additionalProperties": map[string]any{"oneOf": []any{
			map[string]any{"type": "string", "maxLength": MaxStringBytes},
			schemaRef("variableEntry"),
		}},
	}
	// The exactly-one rules of git and stack constrain a whole table, so they
	// apply to base services only: an environment override may restate a
	// single key of either table.
	g.defs["gitSource"] = map[string]any{"allOf": []any{
		map[string]any{"oneOf": schemaExactlyOne([]string{"url"}, []string{"connection", "repo"})},
		schemaWhen(schemaRequire("url"), schemaForbid("connection", "repo", "auto_deploy", "watch_paths")),
	}}
	g.defs["stackSource"] = map[string]any{"oneOf": schemaExactlyOne([]string{"compose"}, []string{"compose_file"})}
	g.defs["service"] = g.serviceDef()
}

// bodyProperty returns the schema of one key of the shared service body, the
// source of the description and examples that refinements of that key repeat.
func (g *schemaGen) bodyProperty(key string) map[string]any {
	body, _ := g.defs["serviceBody"].(map[string]any)
	props, _ := body["properties"].(map[string]any)
	p, _ := props[key].(map[string]any)
	return p
}

// documented is the $ref to def that carries the description and examples of
// the service key, so a refinement of a key reads like the key itself.
func (g *schemaGen) documented(key, def string) map[string]any {
	r := schemaRef(def)
	p := g.bodyProperty(key)
	for _, kw := range []string{"description", "examples"} {
		if v, ok := p[kw]; ok {
			r[kw] = v
		}
	}
	return r
}

// serviceDef is the definition of a base service: the shared body plus
// the kind rules. Environment overrides use serviceBody directly.
func (g *schemaGen) serviceDef() map[string]any {
	databaseForbids := []string{
		"port", "command", "health_check", "volumes", "domains", "pre_deploy", "post_deploy",
		"one_shot", "files", "tasks", "scale", "build", "generate_domain", "processes", "process_name", "singleton",
	}
	stackForbids := []string{
		"port", "command", "health_check", "memory_mb", "cpus", "stop_grace_seconds", "image_retention",
		"one_shot", "block_egress", "pre_deploy", "post_deploy", "docker", "files", "volumes", "build",
		"tasks", "scale", "backups", "processes", "process_name", "singleton",
	}
	routeForbids := append(slices.Clone(stackForbids), "variables", "secrets", "networks")
	// A process of a base service needs its command; an environment override may restate a single key of it.
	processesRule := map[string]any{"additionalProperties": map[string]any{"required": []string{"command"}}}
	for kw, v := range g.bodyProperty("processes") {
		if kw == "description" || kw == "examples" {
			processesRule[kw] = v
		}
	}
	return map[string]any{"allOf": []any{
		schemaRef("serviceBody"),
		map[string]any{"oneOf": schemaExactlyOne(
			[]string{"image"}, []string{"git"}, []string{"database"}, []string{"route"}, []string{"stack"},
		)},
		map[string]any{"properties": map[string]any{"unset": false}},
		map[string]any{"properties": map[string]any{"git": g.documented("git", "gitSource"), "stack": g.documented("stack", "stackSource")}},
		map[string]any{"properties": map[string]any{"processes": processesRule}},
		schemaWhen(schemaRequire("database"), schemaForbid(databaseForbids...)),
		schemaWhen(map[string]any{"not": schemaRequire("database")}, map[string]any{"not": schemaRequire("backups")}),
		schemaWhen(schemaRequire("route"), schemaForbid(routeForbids...)),
		schemaWhen(schemaRequire("stack"), schemaForbid(stackForbids...)),
		schemaWhen(schemaRequire("image"), schemaForbid("build")),
	}}
}

// property builds the schema of one field of owner.
func (g *schemaGen) property(owner reflect.Type, f reflect.StructField) (map[string]any, error) {
	id := owner.Name() + "." + schemaTomlKey(f)
	doc := f.Tag.Get("doc")
	if doc == "" {
		return nil, fmt.Errorf("projectconfig: %s has no doc tag", id)
	}
	ft := schemaDeref(f.Type)
	s := map[string]any{}
	var err error
	switch {
	case schemaIsScalar(ft.Kind()):
		err = schemaScalar(s, f.Tag, ft.Kind())
	case ft.Kind() == reflect.Slice:
		err = g.array(s, ft, f.Tag, id)
	case ft.Kind() == reflect.Map:
		err = g.mapping(s, ft, id)
	case ft.Kind() == reflect.Struct:
		var name string
		if name, err = g.def(ft); err == nil {
			s["$ref"] = schemaRef(name)["$ref"]
		}
	default:
		err = fmt.Errorf("unsupported type %s", f.Type)
	}
	if err != nil {
		return nil, fmt.Errorf("projectconfig: %s: %w", id, err)
	}
	if def, ok := schemaPropRefs[id]; ok {
		ref := schemaRef(def)
		for _, kw := range []string{"default", "minItems"} {
			if v, ok := s[kw]; ok {
				ref[kw] = v
			}
		}
		s = ref
	}
	for k, v := range schemaPropExtras[id] {
		s[k] = v
	}
	if id == "Database.engine" {
		s["enum"] = engines.IDs()
	}
	s["description"] = doc
	ex, err := g.fieldExample(owner, f)
	if err != nil {
		return nil, fmt.Errorf("projectconfig: %s: %w", id, err)
	}
	s["examples"] = []any{ex}
	return s, nil
}

// schemaScalar writes type and the value constraints of a scalar field. A
// field with an enum has no type keyword, and an enum of one value is a const.
func schemaScalar(s map[string]any, tag reflect.StructTag, kind reflect.Kind) error {
	s["type"] = schemaJSONKind(kind)
	for _, kw := range []string{"minimum", "maximum", "default"} {
		if raw, ok := tag.Lookup(kw); ok {
			v, err := schemaParseScalar(kind, raw)
			if err != nil {
				return fmt.Errorf("%s tag: %w", kw, err)
			}
			s[kw] = v
		}
	}
	for _, kw := range []string{"minLength", "maxLength"} {
		if raw, ok := tag.Lookup(kw); ok {
			n, err := strconv.Atoi(raw)
			if err != nil {
				return fmt.Errorf("%s tag: %w", kw, err)
			}
			s[kw] = n
		}
	}
	if raw, ok := tag.Lookup("pattern"); ok {
		s["pattern"] = raw
	}
	if raw := tag.Get("enum"); raw != "" {
		var vals []any
		for _, part := range strings.Split(raw, ",") {
			v, err := schemaParseScalar(kind, part)
			if err != nil {
				return fmt.Errorf("enum tag: %w", err)
			}
			vals = append(vals, v)
		}
		delete(s, "type")
		if len(vals) == 1 {
			s["const"] = vals[0]
		} else {
			s["enum"] = vals
		}
	}
	return nil
}

// array writes the schema of a slice field.
func (g *schemaGen) array(s map[string]any, ft reflect.Type, tag reflect.StructTag, id string) error {
	s["type"] = "array"
	elem := schemaDeref(ft.Elem())
	var items map[string]any
	switch elem.Kind() {
	case reflect.String:
		items = map[string]any{"type": "string"}
		for k, v := range schemaItemExtras[id] {
			items[k] = v
		}
	case reflect.Struct:
		name, err := g.def(elem)
		if err != nil {
			return err
		}
		items = schemaRef(name)
	default:
		return fmt.Errorf("unsupported element type %s", ft.Elem())
	}
	if def, ok := schemaItemRefs[id]; ok {
		items = schemaRef(def)
		s["uniqueItems"] = true
	}
	s["items"] = items
	for _, kw := range []string{"minItems", "maxItems"} {
		raw, ok := tag.Lookup(kw)
		if !ok {
			continue
		}
		n, err := strconv.Atoi(raw)
		if err != nil {
			return fmt.Errorf("%s tag: %w", kw, err)
		}
		if n > 0 || kw == "maxItems" {
			s[kw] = n
		}
	}
	return nil
}

// mapping writes the schema of a map field. A map of Variable is the shared
// `variables` definition.
func (g *schemaGen) mapping(s map[string]any, ft reflect.Type, id string) error {
	elem := schemaDeref(ft.Elem())
	var values map[string]any
	switch elem.Kind() {
	case reflect.String:
		values = map[string]any{"type": "string"}
	case reflect.Int, reflect.Int64:
		values = map[string]any{"type": "integer"}
	case reflect.Struct:
		name, err := g.def(elem)
		if err != nil {
			return err
		}
		if elem == reflect.TypeFor[Variable]() {
			s["$ref"] = schemaRef("variables")["$ref"]
			return nil
		}
		values = schemaRef(name)
	default:
		return fmt.Errorf("unsupported element type %s", ft.Elem())
	}
	s["type"] = "object"
	s["additionalProperties"] = values
	for k, v := range schemaMapExtras[id] {
		s[k] = v
	}
	return nil
}

// fieldExample is the example of a field: its example tag, else its default,
// else its first enum value, else a value derived from the type of a table.
// Numbers and booleans are typed after the field; an array written as JSON is
// an array; any other example of a map or table is the text of the tag.
func (g *schemaGen) fieldExample(owner reflect.Type, f reflect.StructField) (any, error) {
	id := owner.Name() + "." + schemaTomlKey(f)
	ft := schemaDeref(f.Type)
	scalar := schemaIsScalar(ft.Kind())
	if raw, ok := f.Tag.Lookup("example"); ok {
		switch {
		case scalar:
			return schemaParseScalar(ft.Kind(), raw)
		case ft.Kind() == reflect.Slice:
			var arr []any
			if json.Unmarshal([]byte(raw), &arr) == nil {
				return arr, nil
			}
			if schemaDeref(ft.Elem()).Kind() == reflect.Struct {
				return g.derivedExample(ft)
			}
			return raw, nil
		default:
			return raw, nil
		}
	}
	if scalar {
		if raw, ok := f.Tag.Lookup("default"); ok {
			return schemaParseScalar(ft.Kind(), raw)
		}
		if raw := f.Tag.Get("enum"); raw != "" {
			first, _, _ := strings.Cut(raw, ",")
			return schemaParseScalar(ft.Kind(), first)
		}
		return nil, fmt.Errorf("no example, default or enum")
	}
	if ex, ok := schemaExampleFallbacks[id]; ok {
		return ex, nil
	}
	return g.derivedExample(ft)
}

// derivedExample builds an example for a table (or an array of tables) from
// the examples of its required keys, or of its first key when none is
// required.
func (g *schemaGen) derivedExample(ft reflect.Type) (any, error) {
	switch ft.Kind() {
	case reflect.Struct:
		return g.structExample(ft)
	case reflect.Slice:
		elem := schemaDeref(ft.Elem())
		if elem.Kind() == reflect.Struct {
			ex, err := g.structExample(elem)
			if err != nil {
				return nil, err
			}
			return []any{ex}, nil
		}
	}
	return nil, fmt.Errorf("no example tag and none derivable from %s", ft)
}

func (g *schemaGen) structExample(t reflect.Type) (any, error) {
	want := schemaRequired[t]
	if len(want) == 0 {
		for i := range t.NumField() {
			if key := schemaTomlKey(t.Field(i)); key != "" {
				want = []string{key}
				break
			}
		}
	}
	out := map[string]any{}
	for i := range t.NumField() {
		f := t.Field(i)
		key := schemaTomlKey(f)
		if key == "" || !slices.Contains(want, key) {
			continue
		}
		ex, err := g.fieldExample(t, f)
		if err != nil {
			return nil, fmt.Errorf("%s.%s: %w", t.Name(), key, err)
		}
		out[key] = ex
	}
	return out, nil
}
