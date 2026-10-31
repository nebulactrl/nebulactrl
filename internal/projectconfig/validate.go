package projectconfig

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/nebulactrl/nebulactrl/internal/services"
)

var (
	slugRe      = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	varNameRe   = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,254}$`)
	reservedSlg = map[string]bool{"shared": true, "team": true, "vault": true, "doppler": true}
)

// sourceKeys are the keys that make a service's kind, in documentation order.
var sourceKeys = []string{"image", "git", "database", "route", "stack"}

// docChecks validates what is independent of any environment view: the root
// keys, slugs, where unset/only_in may appear, limits spanning the file and the
// secret scan over every string of the document.
func (f *File) docChecks(s *sink) {
	m := f.meta
	if _, present := m.tree["schema"]; !present {
		d := s.errorf(CodeMissingRequired, "schema", "`schema` is required")
		d.Hint = "write schema = 1 as the first key"
		if d.IsZeroPos() {
			d.Line, d.Column, d.EndLine, d.EndColumn = 1, 1, 1, 1
		}
	} else if f.Schema != nil && *f.Schema != SchemaVersion {
		s.errorf(CodeSchemaUnsupported, "schema", "schema = %d is not supported; this NebulaCtrl reads schema = %d", *f.Schema, SchemaVersion)
	}
	if f.Project != nil && !slugRe.MatchString(*f.Project) {
		s.errorf(CodeInvalidSlug, "project", "project %q is not a slug: lower-case letters, digits and hyphens, at most 63 characters", *f.Project)
	}
	if f.Server != nil {
		checkServerName(s, "server", *f.Server)
	}

	services, _ := asMap(m.tree["services"])
	if len(services) > MaxServices {
		s.errorf(CodeLimitExceeded, "services", "%d services; at most %d are accepted", len(services), MaxServices)
	}
	for _, slug := range sortedKeys(services) {
		checkSlug(s, PathString("services", slug), slug)
		if sm, ok := asMap(services[slug]); ok {
			if _, has := sm["unset"]; has {
				s.errorf(CodeUnsetInBase, PathString("services", slug, "unset"), "`unset` is only allowed in environment overrides; remove it or move it under [environments.<slug>.services.%s]", slug)
			}
			f.checkOnlyIn(s, slug, sm)
		}
	}
	envs, _ := asMap(m.tree["environments"])
	if len(envs) > MaxEnvironments {
		s.errorf(CodeLimitExceeded, "environments", "%d environment blocks; at most %d are accepted", len(envs), MaxEnvironments)
	}
	for _, env := range sortedKeys(envs) {
		ep := PathString("environments", env)
		if !slugRe.MatchString(env) {
			s.errorf(CodeInvalidSlug, ep, "environment %q is not a slug: lower-case letters, digits and hyphens, at most 63 characters", env)
		}
		em, ok := asMap(envs[env])
		if !ok {
			continue
		}
		if sv, ok := em["server"].(string); ok {
			checkServerName(s, PathString("environments", env, "server"), sv)
		}
		osvcs, _ := asMap(em["services"])
		for _, slug := range sortedKeys(osvcs) {
			sp := PathString("environments", env, "services", slug)
			checkSlug(s, sp, slug)
			sm, ok := asMap(osvcs[slug])
			if !ok {
				continue
			}
			if _, has := sm["only_in"]; has {
				s.errorf(CodeInvalidValue, sp+".only_in", "`only_in` belongs to the base service; it cannot be used in an environment override").Hint = "remove it, or use `unset` to drop keys in this environment"
			}
			if l, ok := sm["unset"].([]any); ok && len(l) > MaxUnsetPaths {
				s.errorf(CodeLimitExceeded, sp+".unset", "%d unset paths; at most %d", len(l), MaxUnsetPaths)
			}
		}
	}
	f.scanSecrets(s, m.tree, nil)
}

// IsZeroPos reports whether the diagnostic carries no position.
func (d *Diagnostic) IsZeroPos() bool { return d.Line == 0 }

func (f *File) checkOnlyIn(s *sink, slug string, sm map[string]any) {
	l, ok := sm["only_in"].([]any)
	if !ok {
		return
	}
	seen := map[string]bool{}
	for i, e := range l {
		str, ok := e.(string)
		if !ok {
			continue
		}
		p := PathString("services", slug, "only_in", "["+itoa(i)+"]")
		if !slugRe.MatchString(str) {
			s.errorf(CodeInvalidSlug, p, "%q is not an environment slug", str)
		}
		if seen[str] {
			s.errorf(CodeInvalidValue, p, "environment %q is listed twice", str)
		}
		seen[str] = true
	}
}

func checkSlug(s *sink, path, slug string) {
	switch {
	case !slugRe.MatchString(slug):
		d := s.errorf(CodeInvalidSlug, path, "%q is not a slug: lower-case letters, digits and hyphens, starting with a letter or digit, at most 63 characters", slug)
		d.Hint = "service keys are slugs; put the display name in `name`"
	case reservedSlg[slug]:
		s.errorf(CodeReservedSlug, path, "%q is reserved (it names a variable namespace) and cannot be a service slug", slug)
	}
}

func checkServerName(s *sink, path, name string) {
	if strings.TrimSpace(name) == "" || len(name) > 200 {
		s.errorf(CodeInvalidValue, path, "a server is named by its name: 1 to 200 characters")
	}
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// scanSecrets runs the secret scan over every string value of the tree.
func (f *File) scanSecrets(s *sink, n any, p []string) {
	switch t := n.(type) {
	case string:
		if kind, found := ScanSecret(t); found {
			s.errorf(CodeSecretInFile, PathString(p...), "the value looks like a credential (%s); secrets are never written in nebula.toml", kind).Hint = "declare the name in secrets.keys and set the value in the dashboard or CLI, or use a reference such as ${{vault:…}}"
		}
	case map[string]any:
		for _, k := range sortedKeys(t) {
			f.scanSecrets(s, t[k], append(append([]string(nil), p...), k))
		}
	case []any:
		for i, e := range t {
			f.scanSecrets(s, e, append(append([]string(nil), p...), "["+itoa(i)+"]"))
		}
	}
}

// validateRoot checks the shared layers of one view.
func (v *view) validateRoot(block *Environment) {
	f := v.f
	at := func(rel ...string) string { return PathString(rel...) }
	v.checkVars(f.Variables, at, "variables", f.Secrets, "secrets", nil)
	if block != nil {
		eat := func(rel ...string) string { return PathString(append([]string{"environments", v.env}, rel...)...) }
		v.checkVars(block.Variables, eat, "variables", block.Secrets, "secrets", nil)
	}
}

func hasKey(m map[string]any, k string) bool {
	_, ok := m[k]
	return ok
}

// validateService runs every static check on one merged service.
func (v *view) validateService(c *svcCtx) {
	before := len(v.s.list)
	c.inlineAttachments()
	c.checkKind()
	c.checkCommon()
	if c.def.Git != nil {
		c.checkGit()
	}
	if c.def.Build != nil {
		c.checkBuild()
	}
	c.checkRuntime()
	c.checkHealth()
	c.checkHook("pre_deploy", c.def.PreDeploy)
	c.checkHook("post_deploy", c.def.PostDeploy)
	c.checkDocker()
	c.checkDomains()
	c.checkVolumes()
	c.checkFiles()
	c.checkScale()
	c.checkUptime()
	c.checkProcesses()
	c.checkTasks()
	c.checkBackups()
	if c.def.Database != nil {
		c.checkDatabase()
	}
	if c.def.Route != nil {
		c.checkRoute()
	}
	if c.def.Stack != nil {
		c.checkStack()
	}
	c.checkServiceVars()
	c.checkLinks()
	// Catch-all: the platform's own validator, when nothing more precise was found.
	for _, d := range v.s.list[before:] {
		if d.Severity == SeverityError {
			return
		}
	}
	c.platformCheck()
}

// checkKind enforces exactly one source key and the kind matrix.
func (c *svcCtx) checkKind() {
	d := c.def
	present := map[string]bool{
		"image": d.Image != nil, "git": d.Git != nil, "database": d.Database != nil,
		"route": d.Route != nil, "stack": d.Stack != nil,
	}
	var have []string
	for _, k := range sourceKeys {
		if present[k] {
			have = append(have, k)
		}
	}
	switch {
	case len(have) == 0:
		g := c.err(CodeMissingSource, nil, "service %q has no source: write exactly one of image, git, database, route or stack", c.slug)
		g.Hint = "for example [services." + c.slug + `.git] or image = "repo:tag"`
		return
	case len(have) > 1:
		g := c.err(CodeConflictingSource, rp(have[1]), "service %q has more than one source (%s); a service has exactly one of image, git, database, route or stack", c.slug, strings.Join(have, ", "))
		g.Hint = "remove all but one"
	}
	kind := c.kind
	via := kindSource(kind)
	forbid := func(key string, set bool) {
		if !set {
			return
		}
		switch matrix(kind, key) {
		case mxForbidden:
			c.err(CodeNotAllowedForKind, rp(key), "%q is not allowed for a %s service (inferred from %q)", key, kind, via)
		case mxDerived:
			c.err(CodeDerivedField, rp(key), "%q is derived by the platform for a %s service (inferred from %q); remove it", key, kind, via)
		}
	}
	forbid("build", d.Build != nil)
	forbid("port", d.Port != nil)
	forbid("command", d.Command != nil)
	forbid("health_check", d.HealthCheck != nil)
	forbid("one_shot", d.OneShot != nil)
	forbid("stop_grace_seconds", d.StopGraceSeconds != nil)
	forbid("memory_mb", d.MemoryMB != nil)
	forbid("cpus", d.CPUs != nil)
	forbid("image_retention", d.ImageRetention != nil)
	forbid("block_egress", d.BlockEgress != nil)
	forbid("pre_deploy", d.PreDeploy != nil)
	forbid("post_deploy", d.PostDeploy != nil)
	forbid("files", d.Files != nil)
	forbid("volumes", d.Volumes != nil)
	forbid("docker", d.Docker != nil)
	forbid("variables", d.Variables != nil)
	forbid("secrets", d.Secrets != nil)
	forbid("domains", d.Domains != nil)
	forbid("scale", d.Scale != nil)
	forbid("processes", d.Processes != nil)
	forbid("process_name", d.ProcessName != nil)
	forbid("singleton", d.Singleton != nil)
	forbid("tasks", d.Tasks != nil)
	forbid("backups", d.Backups != nil)
	forbid("networks", d.Networks != nil)
	forbid("generate_domain", d.GenerateDomain != nil)
}

func kindSource(kind string) string {
	switch kind {
	case KindImage:
		return "image"
	case KindGit:
		return "git"
	}
	return kind
}

const (
	mxAllowed = iota
	mxForbidden
	mxDerived
)

// matrix implements the kind matrix for the keys it covers.
func matrix(kind, key string) int {
	runtime := kind == KindImage || kind == KindGit
	switch key {
	case "build":
		if kind != KindGit {
			return mxForbidden
		}
	case "port", "command", "health_check", "one_shot":
		switch {
		case runtime:
		case kind == KindDatabase:
			return mxDerived
		default:
			return mxForbidden
		}
	case "stop_grace_seconds", "memory_mb", "cpus", "image_retention", "block_egress", "docker":
		if !runtime && kind != KindDatabase {
			return mxForbidden
		}
	case "pre_deploy", "post_deploy", "files", "scale", "tasks", "generate_domain", "processes", "process_name", "singleton":
		if !runtime {
			return mxForbidden
		}
	case "volumes":
		switch {
		case runtime:
		case kind == KindDatabase:
			return mxDerived
		default:
			return mxForbidden
		}
	case "variables", "secrets":
		if kind == KindRoute {
			return mxForbidden
		}
	case "domains":
		if kind == KindDatabase {
			return mxForbidden
		}
	case "backups":
		if kind != KindDatabase {
			return mxForbidden
		}
	case "networks":
		if kind == KindRoute {
			return mxForbidden
		}
	}
	return mxAllowed
}

// checkCommon validates name, server, depends_on and networks.
func (c *svcCtx) checkCommon() {
	d := c.def
	if d.Name != nil && (strings.TrimSpace(*d.Name) == "" || len(*d.Name) > 120) {
		c.err(CodeInvalidValue, rp("name"), "name must be 1 to 120 characters")
	}
	if d.Server != nil {
		checkServerName(c.v.s, c.abs("server"), *d.Server)
	}
	if c.kind == KindRoute && c.rs.ServerName == "" {
		c.err(CodeMissingRequired, rp("server"), "route services need a server: write `server = \"<name>\"` on the service, the environment or at the root")
	}
	for i, sl := range d.DependsOn {
		if !slugRe.MatchString(sl) || reservedSlg[sl] {
			c.err(CodeInvalidSlug, rp("depends_on", "["+itoa(i)+"]"), "%q is not a service slug", sl)
		}
	}
	dupes(c, "depends_on", d.DependsOn)
	for i, n := range d.Networks {
		if strings.TrimSpace(n) == "" || len(n) > 200 {
			c.err(CodeInvalidValue, rp("networks", "["+itoa(i)+"]"), "a network is named by its name: 1 to 200 characters")
		}
	}
	dupes(c, "networks", d.Networks)
	if len(d.Networks) > 0 && d.BlockEgress != nil && *d.BlockEgress {
		c.err(CodeEgressBlocked, rp("networks"), "a service that blocks egress cannot join shared networks; set block_egress = false or remove networks")
	}
}

func dupes(c *svcCtx, key string, list []string) {
	seen := map[string]bool{}
	for i, x := range list {
		if seen[x] {
			c.err(CodeInvalidValue, rp(key, "["+itoa(i)+"]"), "%q is listed twice", x)
		}
		seen[x] = true
	}
}

func (c *svcCtx) intIn(key []string, v *int, lo, hi int) {
	if v == nil {
		return
	}
	if *v < lo || *v > hi {
		c.err(CodeInvalidValue, key, "%s must be between %d and %d", strings.Join(key, "."), lo, hi)
	}
}

func (c *svcCtx) intMin(key []string, v *int, lo int) {
	if v == nil {
		return
	}
	if *v < lo {
		c.err(CodeInvalidValue, key, "%s must be at least %d", strings.Join(key, "."), lo)
	}
}

// checkRuntime validates image, port, command and the resource keys.
func (c *svcCtx) checkRuntime() {
	d := c.def
	if d.Image != nil {
		img := *d.Image
		switch {
		case strings.TrimSpace(img) == "" || len(img) > 512:
			c.err(CodeInvalidValue, rp("image"), "image must be 1 to 512 characters")
		case strings.ContainsAny(img, " \t\r\n"):
			c.err(CodeInvalidValue, rp("image"), "image must not contain whitespace")
		case strings.Contains(img, "@") && !strings.Contains(img, "@sha256:") && !strings.Contains(img, "@sha512:"):
			c.err(CodeCredentialInURL, rp("image"), "an image reference has no credentials; use repo[:tag][@sha256:digest]")
		}
	}
	c.intIn(rp("port"), d.Port, 0, 65535)
	c.intIn(rp("stop_grace_seconds"), d.StopGraceSeconds, 1, 3600)
	c.intIn(rp("image_retention"), d.ImageRetention, 1, 50)
	if d.MemoryMB != nil && (*d.MemoryMB < 0 || (*d.MemoryMB > 0 && *d.MemoryMB < 6)) {
		c.err(CodeInvalidValue, rp("memory_mb"), "memory_mb is 0 (no limit) or at least 6")
	}
	if d.CPUs != nil && !(*d.CPUs >= 0) {
		c.err(CodeInvalidValue, rp("cpus"), "cpus must be 0 (no limit) or positive")
	}
	if len(d.Command) > 0 {
		if err := validArgv(d.Command); err != "" {
			c.err(CodeInvalidValue, rp("command"), "command: %s", err)
		}
	}
	if d.OneShot != nil && *d.OneShot {
		if d.Port != nil && *d.Port != 0 {
			c.err(CodeInvalidValue, rp("port"), "a one-shot service runs to completion and has no port")
		}
		if d.PreDeploy != nil || d.PostDeploy != nil {
			c.err(CodeInvalidValue, rp("one_shot"), "a one-shot service is itself a command; it has no pre_deploy or post_deploy")
		}
	}
}

func (c *svcCtx) checkHealth() {
	c.checkHealthTable(rp("health_check"), c.def.HealthCheck, c.def.Port, "service")
}

// checkHealthTable validates a health_check table found at key (the service's own,
// or a process'), judged against the port of its owner. owner names that owner
// in messages: "service" or "process".
func (c *svcCtx) checkHealthTable(key []string, h *HealthCheck, ownerPort *int, owner string) {
	if h == nil {
		return
	}
	at := func(k string) []string { return append(key[:len(key):len(key)], k) }
	label := key[len(key)-1]
	t := ""
	if h.Type != nil {
		t = *h.Type
		switch t {
		case services.HealthHTTP, services.HealthTCP, services.HealthDocker, services.HealthExec, services.HealthNone:
		default:
			c.err(CodeInvalidValue, at("type"), "%s.type must be http, tcp, docker, exec or none", label)
		}
	} else if ownerPort != nil && *ownerPort > 0 {
		t = services.HealthHTTP
	}
	if h.Path != nil && !strings.HasPrefix(*h.Path, "/") {
		c.err(CodeInvalidValue, at("path"), "%s.path must start with /", label)
	}
	c.intIn(at("port"), h.Port, 0, 65535)
	c.intMin(at("interval_seconds"), h.IntervalSeconds, 1)
	c.intMin(at("timeout_seconds"), h.TimeoutSeconds, 1)
	c.intMin(at("start_period_seconds"), h.StartPeriodSeconds, 0)
	c.intIn(at("deadline_seconds"), h.DeadlineSeconds, 1, 3600)
	c.intMin(at("success_threshold"), h.SuccessThreshold, 1)
	switch t {
	case services.HealthHTTP, services.HealthTCP:
		port := 0
		if ownerPort != nil {
			port = *ownerPort
		}
		if h.Port != nil {
			port = *h.Port
		}
		if port <= 0 && h.Type != nil {
			c.err(CodeInvalidValue, at("type"), "an %s health check needs a port: set `port` on the %s or %s.port", t, owner, label)
		}
	case services.HealthExec:
		if len(h.Command) < 2 || (h.Command[0] != "CMD" && h.Command[0] != "CMD-SHELL") {
			c.err(CodeInvalidValue, at("command"), "an exec health check needs a command starting with CMD or CMD-SHELL")
		}
	}
	if h.Command != nil && t != services.HealthExec {
		c.err(CodeInvalidValue, at("command"), "%s.command only applies to type = \"exec\"", label)
	}
}

func (c *svcCtx) checkHook(key string, h *Hook) {
	if h == nil {
		return
	}
	if len(h.Command) == 0 {
		if h.TimeoutSeconds != nil {
			c.err(CodeMissingRequired, rp(key, "command"), "%s.timeout_seconds needs %s.command", key, key)
		} else {
			c.err(CodeMissingRequired, rp(key, "command"), "%s needs `command`", key)
		}
		return
	}
	if err := validArgv(h.Command); err != "" {
		c.err(CodeInvalidValue, rp(key, "command"), "%s.command: %s", key, err)
	}
	c.intIn(rp(key, "timeout_seconds"), h.TimeoutSeconds, 1, 3600)
}

// checkLinks validates the volumes/scale interplay that spans sections. The
// volumes are shared by every process, so each process' scale is held to the
// same limit as the service's own.
func (c *svcCtx) checkLinks() {
	d := c.def
	if len(d.Volumes) == 0 {
		return
	}
	c.checkVolumeScale(rp("scale"), d.Scale, "a service")
	for _, name := range sortedKeys(d.Processes) {
		if p := d.Processes[name]; p != nil {
			c.checkVolumeScale(rp("processes", name, "scale"), p.Scale, "a process of a service")
		}
	}
}

// checkVolumeScale reports a scale table found at key that asks for more than one instance.
func (c *svcCtx) checkVolumeScale(key []string, s *Scale, who string) {
	if s == nil {
		return
	}
	at := func(k ...string) []string { return append(key[:len(key):len(key)], k...) }
	if s.Replicas != nil && *s.Replicas > 1 {
		c.err(CodeReplicasRequireStateles, at("replicas"), "%s with volumes cannot run more than one replica (volumes are not shared between replicas)", who)
	}
	if a := s.Autoscaling; a != nil && a.Enabled != nil && *a.Enabled && a.Max != nil && *a.Max > 1 {
		c.err(CodeReplicasRequireStateles, at("autoscaling", "max"), "%s with volumes cannot autoscale beyond one replica", who)
	}
}

func fmtErr(prefix string, err error) string {
	return fmt.Sprintf("%s: %s", prefix, strings.TrimPrefix(err.Error(), "invalid service config: "))
}
