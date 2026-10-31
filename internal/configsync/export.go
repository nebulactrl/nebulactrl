package configsync

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/databases/engines"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/projectconfig"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/internal/varexpr"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Export writes the live state of one environment (or, with all, of every standard
// environment of the project, factored into one file) as a nebula.toml.
//
// The contract: a clean export plans to zero changes against the state it was
// generated from. Keys whose live value equals the key's default are left out unless
// defaults is set or the service already owns the key. Secret values are never read:
// the live loader hands over only what a file may show. Names that are not unique make
// the export fail with ambiguous_reference instead of writing a file that cannot be applied.
func (s *Service) Export(ctx context.Context, projectID uuid.UUID, env, base string, all, defaults bool) (ExportResult, error) {
	if _, err := loadProject(ctx, s.q, policy.ScopeProjectsRead, projectID); err != nil {
		return ExportResult{}, err
	}
	opts := projectconfig.RenderOptions{Origin: s.PublicURL}

	if !all {
		envRow, err := pickEnvironment(ctx, s.q, projectID, env)
		if err != nil {
			return ExportResult{}, err
		}
		scope, err := services.LoadEnvironment(ctx, s.q, policy.ScopeProjectsRead, envRow.ID)
		if err != nil {
			return ExportResult{}, err
		}
		live, err := loadLive(ctx, s.q, s.Box, scope)
		if err != nil {
			return ExportResult{}, err
		}
		v, err := newExporter(live, defaults).resolve()
		if err != nil {
			return ExportResult{}, err
		}
		opts.Comments, opts.NotRepresentable = v.Comments, v.NotRepresentable
		return ExportResult{File: string(projectconfig.Render(v.Res, opts)), Warnings: exWarnings(v.Warnings)}, nil
	}

	envRows, err := s.q.ListEnvironments(ctx, projectID)
	if err != nil {
		return ExportResult{}, fmt.Errorf("list environments: %w", err)
	}
	var standard []db.Environment
	for _, e := range envRows {
		if e.Kind == "standard" {
			standard = append(standard, e)
		}
	}
	if len(standard) == 0 {
		return ExportResult{}, apierr.Unprocessable(CodeUnknownEnvironment, "the project has no standard environment to export")
	}
	baseSlug, err := exBaseSlug(standard, base)
	if err != nil {
		return ExportResult{}, err
	}

	envs := make(map[string]*projectconfig.Resolved, len(standard))
	var warns Diagnostics
	notRep := map[string][]string{}
	comments := map[string][]string{}
	for _, e := range standard {
		scope, err := services.LoadEnvironment(ctx, s.q, policy.ScopeProjectsRead, e.ID)
		if err != nil {
			return ExportResult{}, err
		}
		live, err := loadLive(ctx, s.q, s.Box, scope)
		if err != nil {
			return ExportResult{}, err
		}
		v, err := newExporter(live, defaults).resolve()
		if err != nil {
			return ExportResult{}, err
		}
		envs[e.Slug] = v.Res
		for _, d := range v.Warnings {
			d.Environment = e.Slug
			warns = append(warns, d)
		}
		for slug, items := range v.NotRepresentable {
			notRep[slug] = exUnion(notRep[slug], items)
		}
		for path, lines := range v.Comments {
			comments[path] = exUnion(comments[path], lines)
		}
	}
	opts.Comments, opts.NotRepresentable = comments, notRep
	text, err := projectconfig.Factor(envs, baseSlug, opts)
	if err != nil {
		return ExportResult{}, fmt.Errorf("factor environments: %w", err)
	}
	return ExportResult{File: string(text), Warnings: exWarnings(warns)}, nil
}

// exBaseSlug picks the base environment of an --all export: the one named (slug or id),
// else the production environment, else the first.
func exBaseSlug(standard []db.Environment, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref != "" {
		id, idErr := uuid.Parse(ref)
		for _, e := range standard {
			if e.Slug == ref || (idErr == nil && e.ID == id) {
				return e.Slug, nil
			}
		}
		slugs := make([]string, len(standard))
		for i, e := range standard {
			slugs[i] = e.Slug
		}
		return "", apierr.Unprocessable(CodeUnknownEnvironment, fmt.Sprintf("the base environment %q is not a standard environment of the project", ref)).
			WithDetails(map[string]any{"environments": slugs})
	}
	for _, e := range standard {
		if e.IsProduction {
			return e.Slug, nil
		}
	}
	return standard[0].Slug, nil
}

// exWarnings returns the sorted, de-duplicated warnings (never nil, for the JSON body).
func exWarnings(d Diagnostics) Diagnostics {
	out := d.Dedup().Sorted()
	if out == nil {
		return Diagnostics{}
	}
	return out
}

// exUnion appends the items of b that a lacks, sorted.
func exUnion(a, b []string) []string {
	seen := make(map[string]bool, len(a)+len(b))
	var out []string
	for _, l := range [][]string{a, b} {
		for _, x := range l {
			if !seen[x] {
				seen[x] = true
				out = append(out, x)
			}
		}
	}
	sort.Strings(out)
	return out
}

// exportView is everything one environment contributes to an export.
type exportView struct {
	Res      *projectconfig.Resolved
	Warnings Diagnostics
	// NotRepresentable lists, per service slug, the live settings the format cannot express.
	NotRepresentable map[string][]string
	// Comments are the RenderOptions.Comments of the view (file key path -> lines).
	Comments map[string][]string
}

// exporter turns live state into a resolved view. It is pure: it reads only the liveEnv.
type exporter struct {
	live     *liveEnv
	defaults bool
	warns    Diagnostics
	notRep   map[string][]string
	comments map[string][]string
	ambig    map[string]bool
	slugByID map[uuid.UUID]string
}

func newExporter(live *liveEnv, defaults bool) *exporter {
	x := &exporter{
		live: live, defaults: defaults, notRep: map[string][]string{}, comments: map[string][]string{},
		ambig: map[string]bool{}, slugByID: make(map[uuid.UUID]string, len(live.Services)),
	}
	for slug, ls := range live.Services {
		x.slugByID[ls.Row.ID] = slug
	}
	return x
}

// exStrings copies a list; the result is never nil, so a present empty list stays present.
func exStrings(s []string) []string { return append([]string{}, s...) }

func exScope(s string) string {
	if s == "" {
		return "runtime"
	}
	return s
}

// resolve builds the view of the environment.
func (x *exporter) resolve() (*exportView, error) {
	live := x.live
	res := &projectconfig.Resolved{
		Environment: live.Env.Env.Slug, Project: live.Project.Slug,
		SharedSecretScope: "runtime", EnvSharedSecretScope: "runtime",
		Services: make(map[string]*projectconfig.ResolvedService, len(live.Services)),
	}

	// The default server: set when every service runs on the one and only server in use.
	serverOf := make(map[string]string, len(live.Services))
	distinct := map[string]bool{}
	every := len(live.Order) > 0
	for _, slug := range live.Order {
		ls := live.Services[slug]
		if ls.Row.ServerID == nil {
			every = false
			continue
		}
		name, ok := x.serverName(*ls.Row.ServerID)
		if !ok {
			every = false
			continue
		}
		serverOf[slug] = name
		distinct[strings.ToLower(name)] = true
	}
	if every && len(distinct) == 1 {
		res.Server = serverOf[live.Order[0]]
	}

	for _, slug := range live.Order {
		res.Services[slug] = x.service(slug, live.Services[slug], serverOf[slug], res.Server)
	}

	pv := x.variables(live.SharedProject, nil, "secrets", "")
	res.Shared, res.SharedSecrets, res.SharedSecretScope = pv.Vars, pv.Keys, pv.Scope
	ev := x.variables(live.SharedEnv, nil, projectconfig.PathString("environments", live.Env.Env.Slug, "secrets"), "")
	res.EnvShared, res.EnvSharedSecrets, res.EnvSharedSecretScope = ev.Vars, ev.Keys, ev.Scope

	if err := x.ambiguityError(); err != nil {
		return nil, err
	}
	for slug := range x.notRep {
		sort.Strings(x.notRep[slug])
	}
	return &exportView{Res: res, Warnings: x.warns, NotRepresentable: x.notRep, Comments: x.comments}, nil
}

// ---------------------------------------------------------------------------
// Names.

func (x *exporter) ambiguous(kind, name string) { x.ambig[kind+" "+strconv.Quote(name)] = true }

func (x *exporter) ambiguityError() error {
	if len(x.ambig) == 0 {
		return nil
	}
	names := make([]string, 0, len(x.ambig))
	for n := range x.ambig {
		names = append(names, n)
	}
	sort.Strings(names)
	return apierr.Unprocessable("ambiguous_reference",
		"the file would refer to names that are not unique ("+strings.Join(names, ", ")+"); rename them so that each name is used once, then export again").
		WithDetails(map[string]any{"names": names})
}

func (x *exporter) serverName(id uuid.UUID) (string, bool) {
	r := x.live.Refs
	srv, ok := r.serverByID(id)
	if !ok {
		return "", false
	}
	if _, res := r.server(srv.Name); res != refOK {
		x.ambiguous("server", srv.Name)
		return "", false
	}
	return srv.Name, true
}

func (x *exporter) connectionName(id uuid.UUID) (string, bool) {
	r := x.live.Refs
	c, ok := r.connectionByID(id)
	if !ok {
		return "", false
	}
	if _, res := r.connection(c.Name); res != refOK {
		x.ambiguous("git connection", c.Name)
		return "", false
	}
	return c.Name, true
}

func (x *exporter) registryName(id uuid.UUID) (string, bool) {
	r := x.live.Refs
	reg, ok := r.registryByID(id)
	if !ok {
		return "", false
	}
	if _, res := r.registry(reg.Name); res != refOK {
		x.ambiguous("registry", reg.Name)
		return "", false
	}
	return reg.Name, true
}

func (x *exporter) destinationName(id uuid.UUID) (string, bool) {
	r := x.live.Refs
	d, ok := r.destinationByID(id)
	if !ok {
		return "", false
	}
	if _, res := r.destination(d.Name); res != refOK {
		x.ambiguous("backup destination", d.Name)
		return "", false
	}
	return d.Name, true
}

func (x *exporter) networkName(name string) (string, bool) {
	if _, res := x.live.Refs.network(name); res != refOK {
		x.ambiguous("shared network", name)
		return "", false
	}
	return name, true
}

func (x *exporter) certificateName(id uuid.UUID) (string, bool) {
	r := x.live.Refs
	c, ok := r.certificateByID(id)
	if !ok {
		return "", false
	}
	if _, res := r.certificate(c.Name, c.ServerID); res != refOK {
		x.ambiguous("certificate", c.Name)
		return "", false
	}
	return c.Name, true
}

// notRepresentable records a live setting of a service that schema 1 cannot express.
func (x *exporter) notRepresentable(slug, item string) {
	for _, have := range x.notRep[slug] {
		if have == item {
			return
		}
	}
	x.notRep[slug] = append(x.notRep[slug], item)
	x.warns = append(x.warns, Diagnostic{
		Severity: projectconfig.SeverityInfo, Code: "not_representable", Path: projectconfig.PathString("services", slug),
		Message: item + " is not representable in schema 1 and is not written",
	})
}

// ---------------------------------------------------------------------------
// Variables.

// exCredentialNameRe mirrors templates.secretKeyRe and the literal_secret check of the file format.
var exCredentialNameRe = regexp.MustCompile(`(?i)(PASSWORD|PASSWD|SECRET|TOKEN|PRIVATE_KEY|API_KEY|ADMIN_KEY|MASTER_KEY|ENCRYPTION_KEY|SALT)`)

func exReservedVar(key string) bool { return key == "PORT" || strings.HasPrefix(key, "NEBULA_") }

// exLooksSecret reports whether a plain variable must be exported as a secret name: a
// literal value (no ${{ }}) under a credential-looking name, or one shaped like a credential.
func exLooksSecret(key, text string) bool {
	if text == "" || strings.Contains(text, "${{") {
		return false
	}
	if exCredentialNameRe.MatchString(key) {
		return true
	}
	_, found := projectconfig.ScanSecret(text)
	return found
}

// exVarClass says how one stored variable is written.
type exVarClass struct {
	text        string
	asVar       bool // written under variables
	asSecret    bool // named in secrets.keys
	mixed       bool // a generator template that is not exported
	flagDiffers bool // plain variable exported as a secret name
}

func exClassifyVar(lv liveVar) exVarClass {
	if lv.Opaque {
		return exVarClass{asSecret: true}
	}
	if !lv.Secret {
		if exLooksSecret(lv.Key, lv.Template) {
			return exVarClass{asSecret: true, flagDiffers: true}
		}
		return exVarClass{text: lv.Template, asVar: true}
	}
	if lv.Template == "" {
		return exVarClass{asSecret: true}
	}
	tpl, err := varexpr.Parse(lv.Template)
	if err != nil {
		return exVarClass{asSecret: true}
	}
	literal := false
	for _, p := range tpl.Parts {
		if p.Expr == nil && p.Text != "" {
			literal = true
		}
	}
	generated := tpl.HasGenerators()
	switch {
	case literal:
		return exVarClass{asSecret: true, mixed: generated}
	case !generated:
		// References only: the text is no secret itself, the flag is kept through secrets.keys.
		return exVarClass{text: lv.Template, asVar: true, asSecret: true}
	}
	if es := tpl.Exprs(); len(es) == 1 && es[0].Call != nil && varexpr.IsGenerator(es[0].Call.Fn) {
		// Exactly one generator call: the template text itself recreates the variable.
		return exVarClass{text: lv.Template, asVar: true, asSecret: !templateSecret(tpl)}
	}
	return exVarClass{asSecret: true, mixed: true}
}

// exVariable builds the file entry of a stored variable: the string form unless it has metadata.
func exVariable(lv liveVar, text string) projectconfig.Variable {
	v := projectconfig.Variable{Value: text, Comment: lv.Comment}
	if sc := exScope(lv.Scope); sc != "runtime" {
		v.Scope = sc
	}
	if lv.Rotate != nil && *lv.Rotate > 0 {
		n := *lv.Rotate
		v.RotateEveryDays = &n
	}
	return v
}

// exVarOut is the variables and secrets of one layer.
type exVarOut struct {
	Vars  map[string]projectconfig.Variable
	Keys  []string
	Scope string
}

// variables exports a set of stored variables. secretsPath is the key path of the layer's
// secrets table (comments and warnings point there); slug names the service ("" for a shared layer).
func (x *exporter) variables(lvs map[string]liveVar, skip func(key string) bool, secretsPath, slug string) exVarOut {
	out := exVarOut{Scope: "runtime"}
	type keyed struct{ key, scope string }
	var keys []keyed
	var mixed []string
	for _, key := range sortedKeys(lvs) {
		if exReservedVar(key) || (skip != nil && skip(key)) {
			continue
		}
		lv := lvs[key]
		c := exClassifyVar(lv)
		if c.asVar {
			if out.Vars == nil {
				out.Vars = map[string]projectconfig.Variable{}
			}
			out.Vars[key] = exVariable(lv, c.text)
		}
		if c.asSecret {
			keys = append(keys, keyed{key, exScope(lv.Scope)})
		}
		if c.mixed {
			mixed = append(mixed, key)
		}
		if c.flagDiffers {
			x.warns = append(x.warns, Diagnostic{
				Severity: projectconfig.SeverityWarning, Code: "secret_flag_differs", Path: secretsPath + ".keys",
				Message: key + " is stored as a plain variable but looks like a credential; it is written as a secret name, and the first apply marks it secret and keeps its value",
			})
		}
	}
	if len(keys) > 0 {
		scope := keys[0].scope
		same := true
		for _, k := range keys {
			if k.scope != scope {
				same = false
			}
		}
		if !same {
			scope = "runtime"
			var others []string
			for _, k := range keys {
				if k.scope != scope {
					others = append(others, k.key+" ("+k.scope+")")
				}
			}
			item := "the scope of secrets " + strings.Join(others, ", ") + " (a secrets table has one scope)"
			if slug != "" {
				x.notRepresentable(slug, item)
			} else {
				x.warns = append(x.warns, Diagnostic{Severity: projectconfig.SeverityInfo, Code: "not_representable", Path: secretsPath,
					Message: item + " is not representable in schema 1 and is not written"})
			}
		}
		out.Scope = scope
		for _, k := range keys {
			out.Keys = append(out.Keys, k.key)
		}
	}
	if len(mixed) > 0 {
		x.comments[secretsPath] = append(x.comments[secretsPath], "generated; template not exported: "+strings.Join(mixed, ", "))
	}
	return out
}

// ---------------------------------------------------------------------------
// Services.

// exCaps are the sections a service kind takes (the kind matrix).
type exCaps struct {
	runtime, tuning, docker, files, vars, domains, backups, networks, processes bool
}

func exCapsOf(kind string) exCaps {
	switch kind {
	case projectconfig.KindDatabase:
		return exCaps{tuning: true, docker: true, vars: true, backups: true, networks: true}
	case projectconfig.KindRoute:
		return exCaps{domains: true}
	case projectconfig.KindStack:
		return exCaps{vars: true, domains: true, networks: true}
	}
	return exCaps{runtime: true, tuning: true, docker: true, files: true, vars: true, domains: true, networks: true, processes: true}
}

// exService builds the definition of one live service.
type exService struct {
	x    *exporter
	slug string
	ls   *liveService
	kind string
	cfg  services.Config
	caps exCaps
	def  *projectconfig.Service
}

// owned reports whether the service's file owns a field.
func (s *exService) owned(id string) bool {
	_, ok := s.ls.Owned[id]
	return ok
}

// emitIf decides whether a key is written: when its live value differs from the default, or
// (where it applies) when defaults are asked for or the service owns it.
func (s *exService) emitIf(id string, differs, applicable bool) bool {
	return differs || (applicable && (s.x.defaults || s.owned(id)))
}

func (s *exService) emit(id string, differs bool) bool { return s.emitIf(id, differs, true) }

func (x *exporter) service(slug string, ls *liveService, serverName, rootServer string) *projectconfig.ResolvedService {
	kind := liveKindOf(ls.Row)
	s := &exService{x: x, slug: slug, ls: ls, kind: kind, cfg: ls.Cfg, caps: exCapsOf(kind), def: &projectconfig.Service{}}

	if ls.Row.Name != "" && s.emit("name", ls.Row.Name != slug) {
		s.def.Name = new(ls.Row.Name)
	}
	if serverName != "" && serverName != rootServer {
		s.def.Server = new(serverName)
	}
	if len(ls.Links) > 0 {
		s.def.DependsOn = exStrings(ls.Links)
	}
	if s.owned("generate_domain") && s.caps.domains {
		s.def.GenerateDomain = new(len(ls.Domains) > 0)
	}
	if s.caps.networks {
		s.networks()
	}
	if s.caps.runtime {
		s.runtime()
		s.healthCheck()
		s.hooks()
		if s.caps.processes {
			s.processes()
		}
	}
	if s.caps.tuning {
		s.tuning()
	}
	switch kind {
	case projectconfig.KindGit:
		s.git()
		s.build()
	case projectconfig.KindDatabase:
		s.database()
	case projectconfig.KindRoute:
		s.route()
	case projectconfig.KindStack:
		s.stack()
	}
	if s.cfg.Tailnet != nil {
		x.notRepresentable(slug, "tailnet")
	}
	if s.caps.docker {
		s.dockerOptions()
	}
	if s.caps.files {
		s.files()
		s.volumes()
		s.scale()
		s.tasks()
	}
	if s.caps.vars {
		s.variables()
	}
	if s.caps.domains {
		s.domains()
	}
	if s.caps.backups {
		s.backups()
	}
	s.uptime()

	return &projectconfig.ResolvedService{
		Slug: slug, Kind: kind, ServerName: serverName, Def: s.def,
		Fields: map[string]projectconfig.FieldValue{}, Path: projectconfig.PathString("services", slug),
	}
}

func (s *exService) networks() {
	seen := map[string]bool{}
	var names []string
	for _, n := range s.ls.Networks {
		if seen[n.NetworkName] {
			continue
		}
		seen[n.NetworkName] = true
		if name, ok := s.x.networkName(n.NetworkName); ok {
			names = append(names, name)
		}
	}
	if len(names) > 0 {
		sort.Strings(names)
		s.def.Networks = names
	}
}

// runtime writes the image, port, command and one_shot keys.
func (s *exService) runtime() {
	cfg, d := s.cfg, s.def
	if s.kind == projectconfig.KindImage && cfg.Image != "" {
		d.Image = new(cfg.Image)
	}
	if s.emit("config.port", cfg.Port != 0) {
		d.Port = new(cfg.Port)
	}
	if s.emit("config.startCommand", len(cfg.StartCommand) > 0) {
		d.Command = exStrings(cfg.StartCommand)
	}
	if s.emit("config.oneshot", cfg.OneShot) {
		d.OneShot = new(cfg.OneShot)
	}
	if s.caps.processes {
		if s.emit("config.processName", cfg.ProcessName != "" && cfg.ProcessName != services.DefaultProcessName) {
			d.ProcessName = new(cfg.DefaultProcessName())
		}
		if s.emit("config.singleton", cfg.Singleton) {
			d.Singleton = new(cfg.Singleton)
		}
	}
}

// tuning writes stop_grace_seconds, memory_mb, cpus, image_retention and block_egress.
func (s *exService) tuning() {
	cfg, d := s.cfg, s.def
	ref := services.Config{Port: cfg.Port}.WithDefaults()
	if s.emit("config.stopGraceSeconds", cfg.StopGraceSeconds != ref.StopGraceSeconds) {
		d.StopGraceSeconds = new(cfg.StopGraceSeconds)
	}
	if s.emit("config.memoryMb", cfg.MemoryMB != 0) {
		d.MemoryMB = new(cfg.MemoryMB)
	}
	if s.emit("config.cpus", cfg.CPUs != 0) {
		d.CPUs = new(cfg.CPUs)
	}
	if s.emit("config.imageRetention", cfg.ImageRetention != ref.ImageRetention) {
		d.ImageRetention = new(cfg.ImageRetention)
	}
	// A new database blocks outbound traffic by default.
	defEgress := s.kind == projectconfig.KindDatabase
	if s.emit("config.blockEgress", cfg.BlockEgress != defEgress) {
		d.BlockEgress = new(cfg.BlockEgress)
	}
}

func (s *exService) healthCheck() {
	cfg := s.cfg
	h := cfg.Healthcheck
	defType := services.Config{Port: cfg.Port}.WithDefaults().Healthcheck.Type
	ref := services.Config{Port: cfg.Port, Healthcheck: services.Healthcheck{Type: h.Type}}.WithDefaults().Healthcheck
	hc := &projectconfig.HealthCheck{}
	used := false
	use := func(ok bool) bool {
		used = used || ok
		return ok
	}
	httpOrTCP := h.Type == services.HealthHTTP || h.Type == services.HealthTCP
	if use(s.emit("config.healthcheck.type", h.Type != defType)) {
		hc.Type = new(h.Type)
	}
	if use(s.emitIf("config.healthcheck.path", h.Path != ref.Path, h.Type == services.HealthHTTP && h.Path != "")) {
		hc.Path = new(h.Path)
	}
	if use(s.emitIf("config.healthcheck.port", h.Port != ref.Port, httpOrTCP && h.Port != 0)) {
		hc.Port = new(h.Port)
	}
	if use(s.emit("config.healthcheck.intervalSeconds", h.IntervalSeconds != ref.IntervalSeconds)) {
		hc.IntervalSeconds = new(h.IntervalSeconds)
	}
	if use(s.emit("config.healthcheck.timeoutSeconds", h.TimeoutSeconds != ref.TimeoutSeconds)) {
		hc.TimeoutSeconds = new(h.TimeoutSeconds)
	}
	if use(s.emit("config.healthcheck.startPeriodSeconds", h.StartPeriodSeconds != ref.StartPeriodSeconds)) {
		hc.StartPeriodSeconds = new(h.StartPeriodSeconds)
	}
	if use(s.emit("config.healthcheck.deadlineSeconds", h.DeadlineSeconds != ref.DeadlineSeconds)) {
		hc.DeadlineSeconds = new(h.DeadlineSeconds)
	}
	if use(s.emit("config.healthcheck.successThreshold", h.SuccessThreshold != ref.SuccessThreshold)) {
		hc.SuccessThreshold = new(h.SuccessThreshold)
	}
	if use(s.emitIf("config.healthcheck.command", len(h.Command) > 0, h.Type == services.HealthExec)) {
		hc.Command = exStrings(h.Command)
	}
	if used {
		s.def.HealthCheck = hc
	}
}

func (s *exService) hooks() {
	cfg := s.cfg
	hook := func(cmdID, timeoutID string, cmd []string, timeout, defTimeout int) *projectconfig.Hook {
		h := &projectconfig.Hook{}
		used := false
		if s.emitIf(cmdID, len(cmd) > 0, false) { // a hook without a command does not exist: nothing to write, defaults or not
			h.Command, used = exStrings(cmd), true
		}
		ref := 0
		if len(cmd) > 0 {
			ref = defTimeout
		}
		if s.emitIf(timeoutID, len(cmd) > 0 && timeout != ref, len(cmd) > 0) {
			h.TimeoutSeconds, used = new(timeout), true
		}
		if !used {
			return nil
		}
		return h
	}
	s.def.PreDeploy = hook("config.preDeployCommand", "config.preDeployTimeoutSeconds", cfg.PreDeployCommand, cfg.PreDeployTimeoutSeconds, agentprotocol.DefaultPreDeployTimeoutSeconds)
	s.def.PostDeploy = hook("config.postDeployCommand", "config.postDeployTimeoutSeconds", cfg.PostDeployCommand, cfg.PostDeployTimeoutSeconds, agentprotocol.DefaultPostDeployTimeoutSeconds)
}

// exGitURL prepares a clone URL for the file: credentials before the host are never written.
// ok is false when the URL cannot be written at all; stripped reports that userinfo was removed.
func exGitURL(raw string) (out string, ok, stripped bool) {
	if !strings.Contains(raw, "://") {
		return raw, true, false // git@host:org/repo.git
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", false, false
	}
	if u.User == nil {
		return raw, true, false
	}
	if u.Scheme == "ssh" {
		if _, hasPassword := u.User.Password(); !hasPassword {
			return raw, true, false
		}
	}
	u.User = nil
	return u.String(), true, true
}

func (s *exService) git() {
	cfg, ls := s.cfg, s.ls
	g := &projectconfig.Git{Branch: new(cfg.GitBranch)}
	linked := false
	if ls.Git != nil {
		if name, ok := s.x.connectionName(ls.Git.ConnectionID); ok {
			g.Connection, g.Repo = new(name), new(ls.Git.RepoFullName)
			linked = true
			if s.emit("git.auto_deploy", !ls.Git.AutoDeploy) {
				g.AutoDeploy = new(ls.Git.AutoDeploy)
			}
			if s.emit("git.watch_paths", len(ls.Git.WatchPaths) > 0) {
				g.WatchPaths = exStrings(ls.Git.WatchPaths)
			}
		}
	}
	if !linked && cfg.GitURL != "" {
		if u, ok, stripped := exGitURL(cfg.GitURL); ok {
			g.URL = new(u)
			if stripped {
				s.x.notRepresentable(s.slug, "the credentials in git.url")
			}
		} else {
			s.x.notRepresentable(s.slug, "git.url")
		}
	}
	if cfg.Submodules != "" {
		g.Submodules = new(cfg.Submodules)
	} else if s.emit("config.submodules", false) {
		g.Submodules = new(agentprotocol.SubmodulesAuto)
	}
	s.def.Git = g
}

func (s *exService) build() {
	cfg := s.cfg
	b := &projectconfig.Build{}
	used := false
	use := func(ok bool) bool {
		used = used || ok
		return ok
	}
	if use(s.emit("config.buildType", cfg.BuildType != agentprotocol.BuildAuto)) {
		b.Type = new(cfg.BuildType)
	}
	if use(s.emit("config.dockerfilePath", cfg.DockerfilePath != "Dockerfile")) {
		b.Dockerfile = new(cfg.DockerfilePath)
	}
	if use(s.emit("config.buildContext", cfg.BuildContext != ".")) {
		b.Context = new(cfg.BuildContext)
	}
	if use(s.emit("config.buildLocation", cfg.BuildLocation != services.BuildLocationInherit)) {
		b.Location = new(cfg.BuildLocation)
	}
	if cfg.BuildServerID != nil {
		if name, ok := s.x.serverName(*cfg.BuildServerID); ok {
			use(true)
			b.Server = new(name)
		} else {
			s.x.notRepresentable(s.slug, "build.server")
		}
	}
	if cfg.RegistryID != nil {
		if name, ok := s.x.registryName(*cfg.RegistryID); ok {
			use(true)
			b.Registry = new(name)
		} else {
			s.x.notRepresentable(s.slug, "build.registry")
		}
	}
	if used {
		s.def.Build = b
	}
}

func (s *exService) database() {
	spec := s.cfg.Database
	if spec == nil {
		return
	}
	d := &projectconfig.Database{Engine: new(spec.Engine)}
	if spec.Version != "" {
		d.Version = new(spec.Version)
	}
	if spec.Flavor != "" {
		d.Flavor = new(spec.Flavor)
	}
	if spec.DatabaseName != "" {
		d.Name = new(spec.DatabaseName)
	}
	if spec.Username != "" {
		d.Username = new(spec.Username)
	}
	if e := engines.Lookup(spec.Engine); e != nil && e.LicenseClass == engines.LicenseSourceAvailable {
		d.AcceptLicense = new(true)
	}
	if spec.Replica != nil {
		s.x.notRepresentable(s.slug, "database.replica")
	}
	s.def.Database = d
}

func (s *exService) route() {
	r := s.cfg.Route
	if r == nil {
		return
	}
	rt := &projectconfig.Route{Protocol: new(r.Protocol)}
	for _, u := range r.Upstreams {
		switch {
		case u.ServiceID != nil:
			slug, ok := s.x.slugByID[*u.ServiceID]
			if !ok {
				s.x.notRepresentable(s.slug, "route.upstreams (a service outside this environment)")
				continue
			}
			rt.Upstreams = append(rt.Upstreams, projectconfig.Upstream{Service: new(slug), Port: new(u.Port)})
		default:
			rt.Upstreams = append(rt.Upstreams, projectconfig.Upstream{Address: new(strings.TrimSpace(u.Address))})
		}
	}
	scheme := r.Scheme
	if scheme == "" {
		scheme = "http"
	}
	if s.emit("config.route.scheme", scheme != "http") {
		rt.Scheme = new(scheme)
	}
	if s.emit("config.route.insecureSkipVerify", r.InsecureSkipVerify) {
		rt.InsecureSkipVerify = new(r.InsecureSkipVerify)
	}
	if s.emit("config.route.serverName", r.ServerName != "") {
		rt.ServerName = new(r.ServerName)
	}
	pass := r.PassHost == nil || *r.PassHost
	if s.emit("config.route.passHost", !pass) {
		rt.PassHost = new(pass)
	}
	if s.emitIf("config.route.healthPath", r.HealthPath != "", r.HealthPath != "") {
		rt.HealthPath = new(r.HealthPath)
	}
	if s.emit("config.route.verifySkip", r.VerifySkip) {
		rt.VerifySkip = new(r.VerifySkip)
	}
	if s.emitIf("config.route.listenPort", r.ListenPort != 0, r.ListenPort != 0) {
		rt.ListenPort = new(r.ListenPort)
	}
	if s.emit("config.route.allowCidrs", len(r.AllowCIDRs) > 0) {
		rt.AllowCIDRs = exStrings(r.AllowCIDRs)
	}
	if s.emit("config.route.denyCidrs", len(r.DenyCIDRs) > 0) {
		rt.DenyCIDRs = exStrings(r.DenyCIDRs)
	}
	if s.emit("config.route.openToInternet", r.OpenToInternet) {
		rt.OpenToInternet = new(r.OpenToInternet)
	}
	if s.emit("config.route.proxyProtocol", r.ProxyProtocol != 0) {
		rt.ProxyProtocol = new(r.ProxyProtocol)
	}
	if r.PublicDatabase {
		s.x.notRepresentable(s.slug, "route.publicDatabase")
	}
	s.def.Route = rt
}

func (s *exService) stack() {
	st := s.cfg.Stack
	if st == nil {
		return
	}
	out := &projectconfig.Stack{}
	if _, found := projectconfig.ScanSecret(st.Compose); found {
		s.x.notRepresentable(s.slug, "stack.compose (the text contains a credential)")
	} else {
		out.Compose = new(st.Compose)
	}
	if s.emit("config.stack.waitSeconds", st.WaitSeconds != agentprotocol.DefaultStackWaitSeconds) {
		out.WaitSeconds = new(st.WaitSeconds)
	}
	for _, comp := range sortedKeys(st.Resources) {
		r := st.Resources[comp]
		if !s.emit("config.stack.resources."+comp, r.MemoryMB != 0 || r.CPUs != 0) {
			continue
		}
		if out.Resources == nil {
			out.Resources = map[string]*projectconfig.StackResource{}
		}
		sr := &projectconfig.StackResource{}
		if r.MemoryMB != 0 {
			sr.MemoryMB = new(r.MemoryMB)
		}
		if r.CPUs != 0 {
			sr.CPUs = new(r.CPUs)
		}
		out.Resources[comp] = sr
	}
	s.def.Stack = out
}

func (s *exService) dockerOptions() {
	opts := s.cfg.Docker
	d := exDocker(opts, func(json string) bool { return s.owned("config.docker." + json) })
	if d != nil {
		s.def.Docker = d
	}
}

// exDocker is the inverse of projectconfig's docker table conversion: every option that is set
// (or that the service owns) becomes a key. It returns nil when no key is written.
func exDocker(o *agentprotocol.DockerOptions, owned func(jsonName string) bool) *projectconfig.Docker {
	n := o.Normalized()
	if n == nil {
		n = &agentprotocol.DockerOptions{}
	}
	d := &projectconfig.Docker{}
	present := false
	use := func(json string, set bool) bool {
		if set || owned(json) {
			present = true
			return true
		}
		return false
	}
	if use("capAdd", len(n.CapAdd) > 0) {
		d.CapAdd = exStrings(n.CapAdd)
	}
	if use("capDrop", len(n.CapDrop) > 0) {
		d.CapDrop = exStrings(n.CapDrop)
	}
	if use("noNewPrivileges", n.NoNewPrivileges) {
		d.NoNewPrivileges = new(n.NoNewPrivileges)
	}
	if use("readOnlyRootfs", n.ReadOnlyRootfs) {
		d.ReadOnlyRootfs = new(n.ReadOnlyRootfs)
	}
	if use("pidsLimit", n.PidsLimit != 0) {
		d.PidsLimit = new(int(n.PidsLimit))
	}
	if use("hostname", n.Hostname != "") {
		d.Hostname = new(n.Hostname)
	}
	if use("dns", len(n.DNS) > 0) {
		d.DNS = exStrings(n.DNS)
	}
	if use("sysctls", len(n.Sysctls) > 0) {
		d.Sysctls = make(map[string]string, len(n.Sysctls))
		for k, v := range n.Sysctls {
			d.Sysctls[k] = v
		}
	}
	if use("hostSysctls", len(n.HostSysctls) > 0) {
		d.HostSysctls = make(map[string]int, len(n.HostSysctls))
		for k, v := range n.HostSysctls {
			d.HostSysctls[k] = int(v)
		}
	}
	if use("user", n.User != "") {
		d.User = new(n.User)
	}
	if use("groupAdd", len(n.GroupAdd) > 0) {
		d.GroupAdd = exStrings(n.GroupAdd)
	}
	if use("ulimits", len(n.Ulimits) > 0) {
		d.Ulimits = make(map[string]*projectconfig.Ulimit, len(n.Ulimits))
		for _, u := range n.Ulimits {
			d.Ulimits[u.Name] = &projectconfig.Ulimit{Soft: new(u.Soft), Hard: new(u.Hard)}
		}
	}
	if use("shmSizeMb", n.ShmSizeMB != 0) {
		d.ShmSizeMB = new(n.ShmSizeMB)
	}
	if use("extraHosts", len(n.ExtraHosts) > 0) {
		d.ExtraHosts = exStrings(n.ExtraHosts)
	}
	if use("labels", len(n.Labels) > 0) {
		d.Labels = make(map[string]string, len(n.Labels))
		for k, v := range n.Labels {
			d.Labels[k] = v
		}
	}
	if use("init", n.Init) {
		d.Init = new(n.Init)
	}
	if use("stopSignal", n.StopSignal != "") {
		d.StopSignal = new(n.StopSignal)
	}
	if use("tmpfs", len(n.Tmpfs) > 0) {
		d.Tmpfs = make(map[string]*projectconfig.Tmpfs, len(n.Tmpfs))
		for _, t := range n.Tmpfs {
			e := &projectconfig.Tmpfs{}
			if t.SizeMB != 0 {
				e.SizeMB = new(t.SizeMB)
			}
			if t.Mode != "" {
				e.Mode = new(t.Mode)
			}
			d.Tmpfs[t.Path] = e
		}
	}
	if use("entrypoint", len(n.Entrypoint) > 0) {
		d.Entrypoint = exStrings(n.Entrypoint)
	}
	if use("devices", len(n.Devices) > 0) {
		d.Devices = make([]projectconfig.Device, 0, len(n.Devices))
		for _, dv := range n.Devices {
			e := projectconfig.Device{HostPath: new(dv.HostPath)}
			if dv.ContainerPath != "" {
				e.ContainerPath = new(dv.ContainerPath)
			}
			if dv.Permissions != "" {
				e.Permissions = new(dv.Permissions)
			}
			d.Devices = append(d.Devices, e)
		}
	}
	if use("gpus", n.GPUs != nil) {
		g := &projectconfig.GPUs{}
		if n.GPUs != nil {
			g.Count = new(n.GPUs.Count)
			if n.GPUs.Driver != "" {
				g.Driver = new(n.GPUs.Driver)
			}
			if len(n.GPUs.Capabilities) > 0 {
				g.Capabilities = exStrings(n.GPUs.Capabilities)
			}
		}
		d.GPUs = g
	}
	if use("runtime", n.Runtime != "") {
		d.Runtime = new(n.Runtime)
	}
	if use("privileged", n.Privileged) {
		d.Privileged = new(n.Privileged)
	}
	if use("pidHost", n.PidHost) {
		d.PidHost = new(n.PidHost)
	}
	if use("seccompUnconfined", n.SeccompUnconfined) {
		d.SeccompUnconfined = new(n.SeccompUnconfined)
	}
	if use("apparmorUnconfined", n.ApparmorUnconfined) {
		d.ApparmorUnconfined = new(n.ApparmorUnconfined)
	}
	if !present {
		return nil
	}
	return d
}

func (s *exService) files() {
	cfg := s.cfg
	if len(cfg.Files) == 0 && !s.owned("config.files") {
		return
	}
	m := make(map[string]*projectconfig.Files, len(cfg.Files))
	for _, f := range cfg.Files {
		if _, found := projectconfig.ScanSecret(f.Content); found {
			s.x.notRepresentable(s.slug, "files."+f.Path+" (the content contains a credential)")
			continue
		}
		e := &projectconfig.Files{Content: new(f.Content)}
		mode := f.Mode
		if mode == "" {
			mode = "0644"
		}
		if mode != "0644" || s.x.defaults {
			e.Mode = new(mode)
		}
		m[f.Path] = e
	}
	s.def.Files = m
}

func (s *exService) volumes() {
	if len(s.ls.Volumes) == 0 {
		return
	}
	m := make(map[string]string, len(s.ls.Volumes))
	for _, v := range s.ls.Volumes {
		m[v.Name] = v.MountPath
	}
	s.def.Volumes = m
}

func (s *exService) scale() {
	ls := s.ls
	sc := &projectconfig.Scale{}
	used := false
	replicas := int(ls.Row.Replicas)
	if s.emit("scale.replicas", replicas != 1) {
		sc.Replicas, used = new(replicas), true
	}
	def := services.DefaultAutoscaling()
	a := ls.Scale
	au := &projectconfig.Autoscaling{}
	auto := false
	if s.emit("scale.autoscaling.enabled", a.Enabled) {
		au.Enabled, auto = new(a.Enabled), true
	}
	if s.emit("scale.autoscaling.min", a.Min != def.Min) {
		au.Min, auto = new(a.Min), true
	}
	if s.emit("scale.autoscaling.max", a.Max != def.Max) {
		au.Max, auto = new(a.Max), true
	}
	if s.emit("scale.autoscaling.targetCpuPercent", a.TargetCPUPercent != def.TargetCPUPercent) {
		au.TargetCPUPercent, auto = new(a.TargetCPUPercent), true
	}
	if auto {
		sc.Autoscaling, used = au, true
	}
	if used {
		s.def.Scale = sc
	}
}

// processes writes the additional processes, sorted by name. The command is always written; every
// other key only when it differs from what the platform fills in (or when defaults are asked for).
func (s *exService) processes() {
	if len(s.cfg.Processes) == 0 {
		return
	}
	all := s.x.defaults
	out := make(map[string]*projectconfig.Process, len(s.cfg.Processes))
	for _, p := range s.cfg.Processes {
		e := &projectconfig.Process{Command: exStrings(p.StartCommand)}
		if p.Port != 0 || all {
			e.Port = new(p.Port)
		}
		if p.StopGraceSeconds != 0 { // 0 inherits the service's; the file's minimum is 1
			e.StopGraceSeconds = new(p.StopGraceSeconds)
		}
		if p.Singleton || all {
			e.Singleton = new(p.Singleton)
		}
		if p.MemoryMB != 0 || all {
			e.MemoryMB = new(p.MemoryMB)
		}
		if p.CPUs != 0 || all {
			e.CPUs = new(p.CPUs)
		}
		e.HealthCheck = s.processHealth(p)
		e.Scale = s.processScale(p.Name)
		out[p.Name] = e
	}
	s.def.Processes = out
}

// processHealth writes the health check keys of a process that differ from the defaults of its own port.
func (s *exService) processHealth(p services.ProcessConfig) *projectconfig.HealthCheck {
	all := s.x.defaults
	h := p.Healthcheck
	defType := services.Config{Port: p.Port}.WithDefaults().Healthcheck.Type
	ref := services.Config{Port: p.Port, Healthcheck: services.Healthcheck{Type: h.Type}}.WithDefaults().Healthcheck
	hc := &projectconfig.HealthCheck{}
	used := false
	set := func(differs bool) bool {
		ok := differs || all
		used = used || ok
		return ok
	}
	httpOrTCP := h.Type == services.HealthHTTP || h.Type == services.HealthTCP
	if set(h.Type != defType) {
		hc.Type = new(h.Type)
	}
	if h.Type == services.HealthHTTP && h.Path != "" && (h.Path != ref.Path || all) {
		hc.Path, used = new(h.Path), true
	}
	if httpOrTCP && h.Port != 0 && (h.Port != ref.Port || all) {
		hc.Port, used = new(h.Port), true
	}
	if set(h.IntervalSeconds != ref.IntervalSeconds) {
		hc.IntervalSeconds = new(h.IntervalSeconds)
	}
	if set(h.TimeoutSeconds != ref.TimeoutSeconds) {
		hc.TimeoutSeconds = new(h.TimeoutSeconds)
	}
	if set(h.StartPeriodSeconds != ref.StartPeriodSeconds) {
		hc.StartPeriodSeconds = new(h.StartPeriodSeconds)
	}
	if set(h.DeadlineSeconds != ref.DeadlineSeconds) {
		hc.DeadlineSeconds = new(h.DeadlineSeconds)
	}
	if set(h.SuccessThreshold != ref.SuccessThreshold) {
		hc.SuccessThreshold = new(h.SuccessThreshold)
	}
	if h.Type == services.HealthExec && len(h.Command) > 0 {
		hc.Command, used = exStrings(h.Command), true
	}
	if !used {
		return nil
	}
	return hc
}

// processScale writes the scale of one named process: replicas and autoscaling that differ from the
// defaults of a process that was never scaled, and the keys the file owns.
func (s *exService) processScale(name string) *projectconfig.Scale {
	cur := services.ProcessScalingOf(s.ls.ProcScales, name)
	id := "processes." + name + ".scale."
	sc := &projectconfig.Scale{}
	used := false
	if s.emit(id+"replicas", cur.Replicas != 1) {
		sc.Replicas, used = new(cur.Replicas), true
	}
	def := services.DefaultAutoscaling()
	a := cur.Autoscaling
	au := &projectconfig.Autoscaling{}
	auto := false
	if s.emit(id+"autoscaling.enabled", a.Enabled) {
		au.Enabled, auto = new(a.Enabled), true
	}
	if s.emit(id+"autoscaling.min", a.Min != def.Min) {
		au.Min, auto = new(a.Min), true
	}
	if s.emit(id+"autoscaling.max", a.Max != def.Max) {
		au.Max, auto = new(a.Max), true
	}
	if s.emit(id+"autoscaling.targetCpuPercent", a.TargetCPUPercent != def.TargetCPUPercent) {
		au.TargetCPUPercent, auto = new(a.TargetCPUPercent), true
	}
	if auto {
		sc.Autoscaling, used = au, true
	}
	if !used {
		return nil
	}
	return sc
}

func (s *exService) uptime() {
	enabled, alerts, fail, rec, rem := true, true, 3, 2, 0
	if m := s.ls.Monitor; m != nil {
		enabled, alerts, fail, rec, rem = m.Enabled, m.Alerts, int(m.FailureThreshold), int(m.RecoveryThreshold), int(m.ReminderMinutes)
	}
	up := &projectconfig.Uptime{}
	used := false
	if s.emit("uptime.enabled", !enabled) {
		up.Enabled, used = new(enabled), true
	}
	if s.emit("uptime.alerts", !alerts) {
		up.Alerts, used = new(alerts), true
	}
	if s.emit("uptime.failureThreshold", fail != 3) {
		up.FailureThreshold, used = new(fail), true
	}
	if s.emit("uptime.recoveryThreshold", rec != 2) {
		up.RecoveryThreshold, used = new(rec), true
	}
	if s.emit("uptime.reminderMinutes", rem != 0) {
		up.ReminderMinutes, used = new(rem), true
	}
	if used {
		s.def.Uptime = up
	}
}

func (s *exService) tasks() {
	if len(s.ls.Tasks) == 0 {
		return
	}
	out := make(map[string]*projectconfig.Task, len(s.ls.Tasks))
	for _, name := range sortedKeys(s.ls.Tasks) {
		t := s.ls.Tasks[name]
		var cmd []string
		if err := json.Unmarshal(t.Command, &cmd); err != nil || len(cmd) == 0 {
			s.x.notRepresentable(s.slug, "tasks."+name+" (its command cannot be read)")
			continue
		}
		e := &projectconfig.Task{Schedule: new(t.Schedule), Command: exStrings(cmd)}
		all := s.x.defaults
		if tz := t.Timezone; (tz != "" && tz != "UTC") || all {
			e.Timezone = new(exScopeTimezone(tz))
		}
		if !t.Enabled || all {
			e.Enabled = new(t.Enabled)
		}
		if t.TimeoutSeconds != 1800 || all {
			e.TimeoutSeconds = new(int(t.TimeoutSeconds))
		}
		if (t.Concurrency != "" && t.Concurrency != "forbid") || all {
			c := t.Concurrency
			if c == "" {
				c = "forbid"
			}
			e.Concurrency = new(c)
		}
		out[name] = e
	}
	if len(out) > 0 {
		s.def.Tasks = out
	}
}

func exScopeTimezone(tz string) string {
	if tz == "" {
		return "UTC"
	}
	return tz
}

func (s *exService) backups() {
	keyed := s.ls.backupKeys()
	if len(keyed) == 0 {
		return
	}
	out := make(map[string]*projectconfig.Backup, len(keyed))
	all := s.x.defaults
	for _, key := range sortedKeys(keyed) {
		b := keyed[key]
		dest, ok := s.x.destinationName(b.DestinationID)
		if !ok {
			s.x.notRepresentable(s.slug, "backups."+key+" (its destination)")
			continue
		}
		e := &projectconfig.Backup{Destination: new(dest), Cron: new(b.Cron)}
		if (b.Timezone != "" && b.Timezone != "UTC") || all {
			e.Timezone = new(exScopeTimezone(b.Timezone))
		}
		if !b.Enabled || all {
			e.Enabled = new(b.Enabled)
		}
		if b.RetentionCount != 7 || all {
			e.RetentionCount = new(int(b.RetentionCount))
		}
		if b.RetentionDays != 0 || all {
			e.RetentionDays = new(int(b.RetentionDays))
		}
		out[key] = e
	}
	if len(out) > 0 {
		s.def.Backups = out
	}
}

func (s *exService) variables() {
	cfg := s.cfg
	skip := func(key string) bool {
		if s.ls.Vars[key].Source == services.SourceTemplate {
			return true
		}
		if cfg.Database != nil {
			if cfg.Database.IsCredentialKey(key) {
				return true
			}
			for _, e := range cfg.Database.EnvDefaults() {
				if e.Key == key {
					return true
				}
			}
		}
		return false
	}
	v := s.x.variables(s.ls.Vars, skip, projectconfig.PathString("services", s.slug, "secrets"), s.slug)
	s.def.Variables = v.Vars
	if len(v.Keys) > 0 {
		sec := &projectconfig.Secrets{Keys: v.Keys}
		if v.Scope != "runtime" {
			sec.Scope = new(v.Scope)
		}
		s.def.Secrets = sec
	}
}

// ---------------------------------------------------------------------------
// Domains.

func (s *exService) domains() {
	ls := s.ls
	companions := map[uuid.UUID]bool{}
	for _, d := range ls.Domains {
		if c, ok := services.FindWWWCompanion(ls.Domains, d); ok {
			companions[c.ID] = true
		}
	}
	out := map[string]*projectconfig.Domain{}
	for _, d := range ls.Domains {
		if d.Generated || companions[d.ID] {
			continue
		}
		key := strings.ToLower(d.Hostname) + d.PathPrefix
		out[key] = s.domain(key, d)
	}
	if len(out) > 0 {
		s.def.Domains = out
	}
}

func (s *exService) domain(key string, d db.Domain) *projectconfig.Domain {
	all := s.x.defaults
	id := "domains." + key
	dm := &projectconfig.Domain{}
	own := s.cfg.Port
	if p, ok := s.cfg.Process(d.Component); ok && s.kind != projectconfig.KindStack {
		own = p.Port // a domain of a process targets that process' port by default
	}
	if tp := int(d.TargetPort); tp > 0 && (tp != own || all) {
		dm.TargetPort = new(tp)
	}
	if d.Component != "" {
		// An app's domain keeps its process in the component column; a stack's is a compose component.
		if s.kind == projectconfig.KindStack {
			dm.Component = new(d.Component)
		} else {
			dm.Process = new(d.Component)
		}
	}
	if d.StripPrefix || all {
		dm.StripPrefix = new(d.StripPrefix)
	}
	if d.AddPrefix != "" || all {
		dm.AddPrefix = new(d.AddPrefix)
	}
	kind := d.Kind
	if kind == "" {
		kind = agentprotocol.DomainServe
	}
	if kind != agentprotocol.DomainServe || all {
		dm.Kind = new(kind)
	}
	if kind == agentprotocol.DomainRedirect {
		if d.RedirectTo != nil {
			dm.RedirectTo = new(*d.RedirectTo)
		}
		if d.RedirectCode != 308 || all {
			dm.RedirectCode = new(int(d.RedirectCode))
		}
		if !d.PreservePath || all {
			dm.PreservePath = new(d.PreservePath)
		}
	}
	tls := d.TlsMode
	if tls == "" {
		tls = agentprotocol.TLSAuto
	}
	if tls != agentprotocol.TLSAuto || all {
		dm.TLS = new(tls)
	}
	if d.CertificateID != nil {
		switch name, ok := s.x.certificateName(*d.CertificateID); {
		case ok && tls == agentprotocol.TLSCustom:
			dm.Certificate = new(name)
		default:
			s.x.notRepresentable(s.slug, "domains."+key+".certificate")
		}
	}
	if d.DnsResolver != "" {
		dm.DNSResolver = new(d.DnsResolver)
	}
	_, www := services.FindWWWCompanion(s.ls.Domains, d)
	if _, partner := services.WWWPartner(d.Hostname); www || (all && kind == agentprotocol.DomainServe && d.PathPrefix == "" && partner) {
		dm.WWWRedirect = new(www)
	}
	if o := services.DomainOptions(d.Options); !o.IsZero() {
		opts, issues := exRouteOptions(o)
		dm.Options = opts
		for _, issue := range issues {
			s.x.notRepresentable(s.slug, "domains."+key+".options."+issue+" (the stored order or duplicates)")
		}
	}
	if s.x.live.Refs.Cloudflare {
		zone := d.CfZoneID != "" || d.CfStatus != ""
		cfOwned := s.owned(id + ".cloudflare")
		differs := !d.CfProxied || !d.CfOriginLock
		if cfOwned || (zone && (differs || all)) {
			every := all || cfOwned
			cf := &projectconfig.Cloudflare{}
			// Proxied is the default of a name in a connected zone, so only DNS only is written
			// unless every key is wanted.
			if !d.CfProxied || every {
				cf.Proxied = new(d.CfProxied)
			}
			if !d.CfOriginLock || every {
				cf.OriginLock = new(d.CfOriginLock)
			}
			dm.Cloudflare = cf
		}
	}
	return dm
}

// exHeaders converts a header list to the file's name = value table. ok is false when the
// list has duplicate names or is not sorted by name: the file form cannot say so exactly.
func exHeaders(h []agentprotocol.Header) (m map[string]string, ok bool) {
	if len(h) == 0 {
		return nil, true
	}
	ok = true
	m = make(map[string]string, len(h))
	for i, e := range h {
		if i > 0 && h[i-1].Name >= e.Name {
			ok = false
		}
		m[e.Name] = e.Value
	}
	return m, ok
}

// exRouteOptions is the inverse of the file's options table conversion. issues names the
// parts that cannot round-trip exactly.
func exRouteOptions(o agentprotocol.RouteOptions) (out *projectconfig.RouteOptions, issues []string) {
	if o.IsZero() {
		return nil, nil
	}
	out = &projectconfig.RouteOptions{}
	if o.ForceHTTPS != nil {
		out.ForceHTTPS = new(*o.ForceHTTPS)
	}
	if o.Compress {
		out.Compress = new(true)
	}
	if o.Noindex {
		out.Noindex = new(true)
	}
	if o.SecurityHeaders != "" {
		out.SecurityHeaders = new(o.SecurityHeaders)
	}
	if m, ok := exHeaders(o.RequestHeaders); m != nil {
		out.RequestHeaders = m
		if !ok {
			issues = append(issues, "request_headers")
		}
	}
	if m, ok := exHeaders(o.ResponseHeaders); m != nil {
		out.ResponseHeaders = m
		if !ok {
			issues = append(issues, "response_headers")
		}
	}
	if c := o.CORS; c != nil {
		cors := &projectconfig.CORS{Origins: c.Origins, Methods: c.Methods, Headers: c.Headers, ExposeHeaders: c.ExposeHeaders}
		if c.Credentials {
			cors.Credentials = new(true)
		}
		if c.MaxAgeSeconds != 0 {
			cors.MaxAgeSeconds = new(c.MaxAgeSeconds)
		}
		out.CORS = cors
	}
	for _, r := range o.Redirects {
		e := projectconfig.Redirect{Regex: new(r.Regex), Replacement: new(r.Replacement)}
		if r.Permanent {
			e.Permanent = new(true)
		}
		out.Redirects = append(out.Redirects, e)
	}
	if r := o.RateLimit; r != nil {
		rl := &projectconfig.RateLimit{Average: new(r.Average), PeriodSeconds: new(r.PeriodSeconds), Burst: new(r.Burst)}
		if r.TrustedProxyDepth != 0 {
			rl.TrustedProxyDepth = new(r.TrustedProxyDepth)
		}
		out.RateLimit = rl
	}
	if a := o.Access; a != nil {
		ac := &projectconfig.Access{AllowCIDRs: a.AllowCIDRs, DenyCIDRs: a.DenyCIDRs}
		if a.TrustedProxyDepth != 0 {
			ac.TrustedProxyDepth = new(a.TrustedProxyDepth)
		}
		if a.RejectStatus != 0 {
			ac.RejectStatus = new(a.RejectStatus)
		}
		out.Access = ac
	}
	if o.BasicAuth {
		out.BasicAuth = new(true)
	}
	if o.LargeUploads {
		out.LargeUploads = new(true)
	}
	return out, issues
}
