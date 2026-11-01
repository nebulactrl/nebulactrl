package templates

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"slices"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/nebulactrl/nebulactrl/internal/databases/engines"
)

// Limits of the format.
const (
	maxServices   = 8
	maxInputs     = 24
	maxFiles      = 16
	maxFileBytes  = 64 << 10
	maxTotalFiles = 256 << 10
	maxEnvVars    = 200
)

var (
	idRe       = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	keyRe      = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,30}$`)
	inputKeyRe = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)
	envKeyRe   = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)
	volumeRe   = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,62}$`)
	digestRe   = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	modeRe     = regexp.MustCompile(`^0?[0-7]{3,4}$`)
	iconRe     = regexp.MustCompile(`^[a-z0-9]+$`)
	// secretKeyRe flags env names that look like credentials.
	secretKeyRe = regexp.MustCompile(`(?i)(PASSWORD|PASSWD|SECRET|TOKEN|PRIVATE_KEY|API_KEY|ADMIN_KEY|MASTER_KEY|ENCRYPTION_KEY|SALT)`)

	// inputRe matches ${input:KEY}.
	inputRe = regexp.MustCompile(`\$\{input:([A-Z][A-Z0-9_]*)\}`)
	// refRe matches a cross-service reference ${{key.VAR}} (optional spaces).
	// `$${{` is the escape for a literal and is not matched.
	refRe = regexp.MustCompile(`(^|[^$])\$\{\{\s*([a-z0-9][a-z0-9-]*)\.([A-Za-z_][A-Za-z0-9_]*)\s*(\|[^}]*)?\}\}`)
)

// KnownEngines are the managed database engines a template may request. It
// mirrors the engine catalog of the databases package.
var KnownEngines = []string{"postgres", "mysql", "mariadb", "valkey", "redis", "mongodb", "clickhouse"}

// reservedRefNamespaces are variable namespaces that are not service keys.
var reservedRefNamespaces = map[string]bool{"shared": true, "team": true, "vault": true}

// Parse strictly decodes one template: unknown fields and multiple documents
// are errors. It does not validate; call Validate.
func Parse(data []byte) (*CatalogTemplate, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var t CatalogTemplate
	if err := dec.Decode(&t); err != nil {
		return nil, fmt.Errorf("decode template: %w", err)
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, errors.New("decode template: exactly one YAML document is allowed")
	}
	return &t, nil
}

// ValidationError lists every problem found in one template.
type ValidationError struct {
	ID       string
	Problems []string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("template %q: %s", e.ID, strings.Join(e.Problems, "; "))
}

type checker struct {
	problems []string
	// needsUnrestricted is set when a service uses an option of unrestricted servers.
	needsUnrestricted bool
}

func (c *checker) add(msg string) { c.problems = append(c.problems, msg) }

func (c *checker) addf(format string, a ...any) {
	c.problems = append(c.problems, fmt.Sprintf(format, a...))
}

// Validate checks a template against the format rules (schema, pinned images,
// references, ports, files, licence). It never touches the network.
func Validate(t *CatalogTemplate) error {
	c := &checker{}
	c.header(t)
	keys := map[string]*TemplateService{}
	for i := range t.Services {
		s := &t.Services[i]
		if !keyRe.MatchString(s.Key) {
			c.addf("service %d: key %q must match %s", i, s.Key, keyRe)
			continue
		}
		if keys[s.Key] != nil {
			c.addf("duplicate service key %q", s.Key)
		}
		keys[s.Key] = s
	}
	if len(t.Services) == 0 || len(t.Services) > maxServices {
		c.addf("services: between 1 and %d required", maxServices)
	}
	inputs := map[string]TemplateInput{}
	if len(t.Inputs) > maxInputs {
		c.addf("inputs: at most %d", maxInputs)
	}
	for _, in := range t.Inputs {
		if !inputKeyRe.MatchString(in.Key) {
			c.addf("input %q: key must match %s", in.Key, inputKeyRe)
		}
		if _, dup := inputs[in.Key]; dup {
			c.addf("duplicate input %q", in.Key)
		}
		if strings.TrimSpace(in.Label) == "" {
			c.addf("input %q: label is required", in.Key)
		}
		if len(in.Options) > 0 && in.Default != "" && !slices.Contains(in.Options, in.Default) {
			c.addf("input %q: default must be one of options", in.Key)
		}
		c.inputFormat(in)
		inputs[in.Key] = in
	}
	hasPublished := false
	for i := range t.Services {
		s := &t.Services[i]
		if keys[s.Key] != s {
			continue
		}
		c.service(s, keys, inputs)
		for _, p := range s.Ports {
			hasPublished = hasPublished || p.Publish
		}
	}
	_ = hasPublished
	switch {
	case c.needsUnrestricted && !t.RequiresUnrestricted:
		c.add("a docker option needs an unrestricted server: set requiresUnrestricted: true")
	case !c.needsUnrestricted && t.RequiresUnrestricted:
		c.add("requiresUnrestricted is set, but no docker option needs an unrestricted server")
	}
	c.cycles(t, keys)
	c.unusedInputs(t, inputs)
	if len(c.problems) > 0 {
		return &ValidationError{ID: t.ID, Problems: c.problems}
	}
	return nil
}

func (c *checker) header(t *CatalogTemplate) {
	if t.Schema != SchemaVersion {
		c.addf("schema must be %d", SchemaVersion)
	}
	if !idRe.MatchString(t.ID) {
		c.addf("id %q must match %s", t.ID, idRe)
	}
	if t.Revision < 1 {
		c.add("revision must be >= 1")
	}
	if strings.TrimSpace(t.Name) == "" || len(t.Name) > 60 {
		c.add("name is required (max 60 characters)")
	}
	if s := strings.TrimSpace(t.Summary); s == "" || len(s) > 200 {
		c.add("summary is required (max 200 characters)")
	}
	if !slices.Contains(Categories, t.Category) {
		c.addf("category %q must be one of %s", t.Category, strings.Join(Categories, ", "))
	}
	if t.Icon != "" && !iconRe.MatchString(t.Icon) {
		c.addf("icon %q must be a simple-icons slug ([a-z0-9]+)", t.Icon)
	}
	if !isHTTPURL(t.Links.Website) {
		c.add("links.website must be an http(s) URL")
	}
	for name, u := range map[string]string{"docs": t.Links.Docs, "source": t.Links.Source} {
		if u != "" && !isHTTPURL(u) {
			c.addf("links.%s must be an http(s) URL", name)
		}
	}
	if strings.TrimSpace(t.Upstream.License) == "" {
		c.add("upstream.license is required")
	}
	switch t.Upstream.Class {
	case LicensePermissive, LicenseCopyleft:
	case LicenseSourceAvailable, LicenseProprietary:
		if !isHTTPURL(t.Upstream.LicenseURL) {
			c.add("upstream.licenseUrl is required for source-available and proprietary licences")
		}
	default:
		c.add("upstream.class must be permissive, copyleft, source-available or proprietary")
	}
	if t.Upstream.LicenseURL != "" && !isHTTPURL(t.Upstream.LicenseURL) {
		c.add("upstream.licenseUrl must be an http(s) URL")
	}
	if o := t.Origin; o != nil {
		switch o.Type {
		case OriginOriginal:
		case OriginCoolify, OriginDokploy:
			if o.Path == "" || o.Commit == "" || o.License == "" {
				c.addf("origin: path, commit and license are required for %s templates", o.Type)
			}
		default:
			c.add("origin.type must be original, coolify or dokploy")
		}
	}
	for _, a := range t.Requires.Arch {
		if a != "amd64" && a != "arm64" {
			c.addf("requires.arch: %q is not amd64 or arm64", a)
		}
	}
	if t.Requires.MemoryMB < 0 || t.Requires.DiskGB < 0 {
		c.add("requires must not be negative")
	}
}

func isHTTPURL(u string) bool {
	return strings.HasPrefix(u, "https://") || strings.HasPrefix(u, "http://")
}

func (c *checker) service(s *TemplateService, keys map[string]*TemplateService, inputs map[string]TemplateInput) {
	at := "service " + s.Key + ": "
	if strings.TrimSpace(s.Name) == "" {
		c.add(at + "name is required")
	}
	switch s.Kind {
	case "", "app", "worker", "database":
	default:
		c.addf(at+"kind %q must be app, worker or database", s.Kind)
	}
	if s.Database != nil {
		c.database(at, s)
	} else {
		c.image(at, s)
	}
	if s.Port < 0 || s.Port > 65535 {
		c.add(at + "port out of range")
	}
	if s.StopGraceSeconds < 0 || s.StopGraceSeconds > 3600 {
		c.add(at + "stopGraceSeconds must be between 0 and 3600")
	}
	if r := s.Resources; r != nil && (r.MemoryMB < 0 || (r.MemoryMB > 0 && r.MemoryMB < 6) || r.CPUs < 0) {
		c.add(at + "resources out of range")
	}
	c.health(at, s)
	c.env(at, s, keys, inputs)
	c.volumes(at, s)
	c.files(at, s, inputs)
	c.ports(at, s)
	if c.docker(at, s, inputs) {
		c.needsUnrestricted = true
	}
	if len(s.Domains) > 1 {
		c.add(at + "at most one domain per service")
	}
	for _, d := range s.Domains {
		if d.Generate == (d.Host != "") {
			c.add(at + "a domain needs exactly one of generate or host")
		}
		if s.Port <= 0 {
			c.add(at + "domains need an http port")
		}
		c.macros(at+"domain host", d.Host, inputs)
	}
	for _, dep := range s.DependsOn {
		if dep == s.Key {
			c.add(at + "dependsOn itself")
		} else if keys[dep] == nil {
			c.addf(at+"dependsOn unknown service %q", dep)
		}
	}
	if s.OneShot {
		if s.Port != 0 || len(s.Domains) > 0 || len(s.Ports) > 0 {
			c.add(at + "one-shot services have no port, domain or published ports")
		}
		if s.Database != nil {
			c.add(at + "a managed database cannot be one-shot")
		}
	}
	for _, a := range s.Command {
		c.macros(at+"command", a, inputs)
	}
}

func (c *checker) database(at string, s *TemplateService) {
	d := s.Database
	if !slices.Contains(KnownEngines, d.Engine) {
		c.addf(at+"database.engine %q must be one of %s", d.Engine, strings.Join(KnownEngines, ", "))
	}
	switch e := engines.Lookup(d.Engine); {
	case strings.TrimSpace(d.Version) == "":
		c.add(at + "database.version is required")
	case e != nil && e.Version(d.Version) == nil:
		// The install would fail with invalid_database, so the catalog must
		// only name versions the engine catalog offers.
		c.addf(at+"database.version %q is not offered for %s (one of %s)", d.Version, d.Engine, strings.Join(e.SortedVersionIDs(), ", "))
	}
	if s.Image != "" || s.Digest != "" || s.Port != 0 || len(s.Command) > 0 || len(s.Volumes) > 0 || len(s.Files) > 0 ||
		len(s.Env) > 0 || s.Healthcheck != nil || len(s.Ports) > 0 || len(s.Domains) > 0 || s.OneShot {
		c.add(at + "a database service takes only name, kind, database, dependsOn and resources")
	}
	if s.Kind != "" && s.Kind != "database" {
		c.add(at + "a database service must have kind database")
	}
}

// SplitImage splits "repo:tag" into repo and tag ("" when untagged). A port in
// the registry host ("host:5000/img") is not a tag.
func SplitImage(ref string) (repo, tag string) {
	slash := strings.LastIndex(ref, "/")
	colon := strings.LastIndex(ref, ":")
	if colon > slash {
		return ref[:colon], ref[colon+1:]
	}
	return ref, ""
}

func (c *checker) image(at string, s *TemplateService) {
	if s.Image == "" {
		c.add(at + "image is required (or a database block)")
		return
	}
	if strings.ContainsAny(s.Image, " \t\n@") {
		c.add(at + "image must be repo:tag without whitespace or digest (use the digest field)")
		return
	}
	_, tag := SplitImage(s.Image)
	switch strings.ToLower(tag) {
	case "":
		c.add(at + "image needs an explicit tag")
	case "latest", "stable", "main", "master", "edge", "nightly", "dev", "develop", "release":
		c.addf(at+"image tag %q is floating; pin a version", tag)
	}
	if !digestRe.MatchString(s.Digest) || s.Digest == "sha256:"+strings.Repeat("0", 64) {
		c.add(at + "digest must be sha256:<64 hex> (run tools/template-pin); the all-zero placeholder is not a digest")
	}
}

func (c *checker) health(at string, s *TemplateService) {
	h := s.Healthcheck
	if s.Database != nil {
		return
	}
	if h == nil {
		if !s.OneShot {
			c.add(at + "healthcheck is required")
		}
		return
	}
	if s.OneShot {
		c.add(at + "one-shot services have no healthcheck (success is exit code 0)")
		return
	}
	switch h.Type {
	case "http":
		if s.Port == 0 && h.Port == 0 {
			c.add(at + "http healthcheck needs a port")
		}
		if h.Path != "" && !strings.HasPrefix(h.Path, "/") {
			c.add(at + "healthcheck.path must start with /")
		}
	case "tcp":
		if s.Port == 0 && h.Port == 0 {
			c.add(at + "tcp healthcheck needs a port")
		}
	case "exec":
		if len(h.Command) < 2 || (h.Command[0] != "CMD" && h.Command[0] != "CMD-SHELL") {
			c.add(at + "exec healthcheck.command must start with CMD or CMD-SHELL followed by the command")
		}
	case "docker":
	case "none":
		c.add(at + "healthcheck type none is not allowed in catalog templates")
	default:
		c.addf(at+"unknown healthcheck type %q", h.Type)
	}
	for _, v := range []int{h.IntervalSeconds, h.TimeoutSeconds, h.StartPeriodSeconds, h.DeadlineSeconds, h.SuccessThreshold} {
		if v < 0 {
			c.add(at + "healthcheck timings must not be negative")
		}
	}
}

func (c *checker) env(at string, s *TemplateService, keys map[string]*TemplateService, inputs map[string]TemplateInput) {
	if len(s.Env) > maxEnvVars {
		c.addf(at+"at most %d env vars", maxEnvVars)
	}
	for k, v := range s.Env {
		if !envKeyRe.MatchString(k) {
			c.addf(at+"env key %q is invalid", k)
			continue
		}
		if strings.HasPrefix(strings.ToUpper(k), "NEBULA_") || k == "PORT" {
			c.addf(at+"env key %q is reserved by the platform", k)
		}
		c.macros(at+"env "+k, v, inputs)
		for _, m := range refRe.FindAllStringSubmatch(v, -1) {
			ns := m[2]
			if reservedRefNamespaces[ns] {
				continue
			}
			if keys[ns] == nil {
				c.addf(at+"env %s references unknown service %q", k, ns)
			}
		}
		if secretKeyRe.MatchString(k) && v != "" && !strings.Contains(v, "${") {
			c.addf(at+"env %s looks like a credential but has a literal value; use a generator or an input", k)
		}
	}
	for _, k := range s.Secrets {
		if _, ok := s.Env[k]; !ok {
			c.addf(at+"secrets lists %q which is not in env", k)
		}
	}
}

// macros checks that every ${input:KEY} in v names a declared input.
func (c *checker) macros(where, v string, inputs map[string]TemplateInput) {
	for _, m := range inputRe.FindAllStringSubmatch(v, -1) {
		if _, ok := inputs[m[1]]; !ok {
			c.addf("%s uses undeclared input %q", where, m[1])
		}
	}
}

func (c *checker) volumes(at string, s *TemplateService) {
	seenN, seenP := map[string]bool{}, map[string]bool{}
	for _, v := range s.Volumes {
		if !volumeRe.MatchString(v.Name) {
			c.addf(at+"volume name %q is invalid", v.Name)
		}
		if !path.IsAbs(v.Path) || v.Path == "/" || strings.ContainsAny(v.Path, ":") || path.Clean(v.Path) != strings.TrimSuffix(v.Path, "/") {
			c.addf(at+"volume path %q must be a clean absolute path", v.Path)
		}
		if seenN[v.Name] || seenP[v.Path] {
			c.addf(at+"duplicate volume %q", v.Name)
		}
		seenN[v.Name], seenP[v.Path] = true, true
	}
}

func (c *checker) files(at string, s *TemplateService, inputs map[string]TemplateInput) {
	if len(s.Files) > maxFiles {
		c.addf(at+"at most %d files", maxFiles)
	}
	total := 0
	seen := map[string]bool{}
	for _, f := range s.Files {
		if !path.IsAbs(f.Path) || strings.Contains(f.Path, "..") || path.Clean(f.Path) != f.Path || strings.HasSuffix(f.Path, "/") {
			c.addf(at+"file path %q must be a clean absolute path without ..", f.Path)
		}
		if seen[f.Path] {
			c.addf(at+"duplicate file %q", f.Path)
		}
		seen[f.Path] = true
		if f.Mode != "" && !modeRe.MatchString(f.Mode) {
			c.addf(at+"file %s: mode %q must be octal like 0644", f.Path, f.Mode)
		}
		if len(f.Content) > maxFileBytes {
			c.addf(at+"file %s exceeds %d bytes", f.Path, maxFileBytes)
		}
		if strings.Contains(f.Content, "${{") {
			c.addf(at+"file %s: variable references are not resolved inside files", f.Path)
		}
		c.macros(at+"file "+f.Path, f.Content, inputs)
		total += len(f.Content)
		for _, v := range s.Volumes {
			if f.Path == v.Path || strings.HasPrefix(f.Path, strings.TrimSuffix(v.Path, "/")+"/") {
				c.addf(at+"file %s lies inside volume %s and would overwrite user data on every deploy", f.Path, v.Name)
			}
		}
	}
	if total > maxTotalFiles {
		c.addf(at+"files exceed %d bytes in total", maxTotalFiles)
	}
}

func (c *checker) ports(at string, s *TemplateService) {
	type pp struct {
		port  int
		proto string
	}
	seen := map[pp]bool{}
	names := map[string]bool{}
	for _, p := range s.Ports {
		if p.Name == "" || names[p.Name] {
			c.addf(at+"port names must be unique and non-empty (%q)", p.Name)
		}
		names[p.Name] = true
		if p.Port < 1 || p.Port > 65535 {
			c.addf(at+"port %q out of range", p.Name)
		}
		if p.Protocol != "tcp" && p.Protocol != "udp" {
			c.addf(at+"port %q protocol must be tcp or udp", p.Name)
		}
		if seen[pp{p.Port, p.Protocol}] {
			c.addf(at+"duplicate %s port %d", p.Protocol, p.Port)
		}
		seen[pp{p.Port, p.Protocol}] = true
		if p.Publish && p.Port < 1024 {
			c.addf(at+"published port %d: ports below 1024 are reserved", p.Port)
		}
		if p.Port == s.Port && p.Protocol == "tcp" {
			c.addf(at+"port %q duplicates the http port", p.Name)
		}
	}
}

// edges returns the dependency edges consumer → provider: dependsOn plus
// template references in env values.
func edges(t *CatalogTemplate) map[string][]string {
	out := map[string][]string{}
	for _, s := range t.Services {
		set := map[string]bool{}
		for _, d := range s.DependsOn {
			set[d] = true
		}
		for _, v := range s.Env {
			for _, m := range refRe.FindAllStringSubmatch(v, -1) {
				if m[2] != s.Key && !reservedRefNamespaces[m[2]] {
					set[m[2]] = true
				}
			}
		}
		for d := range set {
			out[s.Key] = append(out[s.Key], d)
		}
		sort.Strings(out[s.Key])
	}
	return out
}

func (c *checker) cycles(t *CatalogTemplate, keys map[string]*TemplateService) {
	g := edges(t)
	state := map[string]int{} // 1 visiting, 2 done
	var visit func(k string) bool
	visit = func(k string) bool {
		switch state[k] {
		case 1:
			return true
		case 2:
			return false
		}
		state[k] = 1
		for _, d := range g[k] {
			if keys[d] != nil && visit(d) {
				return true
			}
		}
		state[k] = 2
		return false
	}
	for _, s := range t.Services {
		if keys[s.Key] != nil && visit(s.Key) {
			c.add("services form a dependency cycle (via dependsOn or references)")
			return
		}
	}
}

func (c *checker) unusedInputs(t *CatalogTemplate, inputs map[string]TemplateInput) {
	used := map[string]bool{}
	scan := func(v string) {
		for _, m := range inputRe.FindAllStringSubmatch(v, -1) {
			used[m[1]] = true
		}
	}
	for _, s := range t.Services {
		for _, v := range s.Env {
			scan(v)
		}
		for _, f := range s.Files {
			scan(f.Content)
		}
		for _, a := range s.Command {
			scan(a)
		}
		for _, d := range s.Domains {
			scan(d.Host)
		}
		if s.Docker != nil {
			_, _ = mapDockerStrings(&s.Docker.DockerOptions, func(v string) string { scan(v); return v })
		}
	}
	for k := range inputs {
		if !used[k] {
			c.addf("input %q is never used", k)
		}
	}
}
