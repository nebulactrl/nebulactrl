package composeimport

import (
	"fmt"
	"math"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/databases/engines"
	"github.com/nebulactrl/nebulactrl/internal/projects"
	"github.com/nebulactrl/nebulactrl/internal/services"
)

// RepoInfo describes the repository the Compose file was read from.
type RepoInfo struct {
	ConnectionID *uuid.UUID
	Repo         string
	Branch       string
	CloneURL     string
	Commit       string
}

// Context is everything the analysis needs from outside the Compose file.
type Context struct {
	// Taken returns the slugs of the target environment that start with base.
	Taken func(base string) []string
	// ComposeDir is the directory of the Compose file inside Repo ("" when pasted).
	ComposeDir string
	// Repo is set when the file was read from git.
	Repo *RepoInfo
	// Files reads files from the repository (consulted after Source.Files).
	Files FileFn
	// ResolveRepo maps a git connection and repository to a clone URL and
	// default branch.
	ResolveRepo func(connection uuid.UUID, repo string) (cloneURL, branch string, err error)
	// SharedNetworks maps shared network names of the team to their ids.
	SharedNetworks map[string]uuid.UUID
	// DefaultName titles the canvas group when nothing else does.
	DefaultName string
	// Unrestricted is the target server's Unrestricted switch. Docker options
	// that need it (devices, gpus, runtime, privileged, pid: host, unconfined
	// seccomp or AppArmor, NET_ADMIN, SYS_ADMIN) are applied only when it is
	// set; otherwise they are reported as needing an unrestricted server. False
	// also when no server was chosen yet.
	Unrestricted bool
}

// Result is an analysed import: the reviewable plan plus the unmasked values.
type Result struct {
	plan ComposePlan
	// refs[key] lists the Compose services key's variables point at.
	refs map[string][]string
	// groupName titles the canvas group.
	groupName string
}

// Plan returns the review view: secret values are removed.
func (r *Result) Plan() ComposePlan {
	p := r.plan
	p.Services = make([]ComposePlanService, len(r.plan.Services))
	for i, s := range r.plan.Services {
		c := s
		c.Env = make([]ComposePlanEnv, len(s.Env))
		for j, e := range s.Env {
			if e.Secret {
				e.Value = ""
			}
			c.Env[j] = e
		}
		p.Services[i] = c
	}
	return p
}

// Analyze reads a Compose project and decides, for every service, how it would
// be imported. It does not touch the database; Choices are applied on top of
// the defaults.
func Analyze(in ComposeSource, ch ComposeChoices, cx Context) (*Result, error) {
	dotenv, dotenvNotes := ParseDotenv(in.Env, nil)
	fileMap := map[string]string{}
	for k, v := range in.Files {
		fileMap[cleanRel(k)] = v
	}
	files := func(p string) ([]byte, bool) {
		p = cleanRel(p)
		if v, ok := fileMap[p]; ok {
			return []byte(v), true
		}
		if p == ".env" && in.Env != "" {
			return []byte(in.Env), true
		}
		if cx.Files != nil {
			return cx.Files(p)
		}
		return nil, false
	}
	proj, err := parseProject(in.Compose, dotenv, files)
	if err != nil {
		return nil, err
	}
	b := &builder{p: proj, ch: ch, cx: cx, files: files, byKey: map[string]*ComposePlanService{}, cs: map[string]*cService{}, refs: map[string][]string{}}
	for _, n := range dotenvNotes {
		b.warnings = append(b.warnings, ".env: "+n)
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		name = proj.Name
	}
	if name == "" {
		name = cx.DefaultName
	}
	if name == "" {
		name = "Compose project"
	}
	b.name = name
	return b.run()
}

type builder struct {
	p        *project
	ch       ComposeChoices
	cx       Context
	files    FileFn
	name     string
	warnings []string
	rewrites []ComposeRewrite

	svcs  []*ComposePlanService
	byKey map[string]*ComposePlanService
	cs    map[string]*cService
	refs  map[string][]string
}

func (b *builder) choice(key string) ComposeServiceChoice { return b.ch.Services[key] }

func (b *builder) run() (*Result, error) {
	b.assignSlugs()
	for _, s := range b.p.Services {
		b.cs[s.Key] = s
	}
	for _, ps := range b.svcs {
		b.mapService(b.cs[ps.Key], ps)
	}
	b.detectOneShots()
	for _, ps := range b.svcs {
		b.finishService(b.cs[ps.Key], ps)
	}
	b.rewriteEnv()
	b.markSecrets()
	b.linkServices()
	b.sharedVolumes()

	plan := ComposePlan{
		Name: b.name, Services: make([]ComposePlanService, len(b.svcs)), Rewrites: b.rewrites, Notes: b.p.Notes,
		Warnings: b.warnings, Checks: []ComposeCheck{}, NeededFiles: b.neededFiles(), Missing: b.p.Interp.Missing(),
		SourceKind: "paste", Mode: ModeSplit,
	}
	if b.cx.Repo != nil {
		plan.SourceKind, plan.Commit = "repo", b.cx.Repo.Commit
	}
	for i, s := range b.svcs {
		plan.Services[i] = *s
	}
	if plan.Rewrites == nil {
		plan.Rewrites = []ComposeRewrite{}
	}
	if plan.Notes == nil {
		plan.Notes = []ComposeNote{}
	}
	plan.Networks = b.networks()
	plan.Warnings = b.warnings
	if plan.Warnings == nil {
		plan.Warnings = []string{}
	}
	b.blockers(&plan)
	return &Result{plan: plan, refs: b.refs, groupName: b.name}, nil
}

// assignSlugs decides inclusion and predicts every included service's slug.
func (b *builder) assignSlugs() {
	taken := map[string]bool{}
	for _, s := range b.p.Services {
		c := b.choice(s.Key)
		ps := &ComposePlanService{
			Key: s.Key, Name: s.Key, Include: true, Env: []ComposePlanEnv{}, Volumes: []ComposePlanVolume{}, Files: []ComposePlanFile{},
			Binds: []ComposePlanBind{}, Ports: []ComposePlanPort{}, Domains: []ComposePlanDomain{}, DependsOn: []string{}, Networks: []string{},
			Notes: append([]ComposeNote{}, s.Notes...), Needs: []string{}, Replicas: 1, Kind: services.KindApp,
		}
		if len(s.Profiles) > 0 {
			ps.Include = false
			ps.IncludeNote = "Compose starts this service only with the profile " + strings.Join(s.Profiles, ", ") + "."
		}
		if c.Include != nil {
			ps.Include = *c.Include
		}
		if n := strings.TrimSpace(c.Name); n != "" {
			ps.Name = n
		}
		if ps.Include {
			base := projects.Slugify(ps.Name, "service")
			var existing []string
			if b.cx.Taken != nil {
				existing = append(existing, b.cx.Taken(base)...)
			}
			existing = append(existing, services.ReservedSlugs...)
			for k := range taken {
				existing = append(existing, k)
			}
			ps.Slug = projects.UniqueSlug(base, existing)
			taken[ps.Slug] = true
		} else {
			ps.Slug = projects.Slugify(ps.Name, "service")
		}
		b.svcs = append(b.svcs, ps)
		b.byKey[s.Key] = ps
	}
}

// ---- per service mapping ----

var (
	nonHTTPPorts = map[int]bool{21: true, 22: true, 25: true, 53: true, 110: true, 143: true, 389: true, 465: true, 587: true, 636: true,
		993: true, 995: true, 1433: true, 1521: true, 1883: true, 2181: true, 3306: true, 4222: true, 5432: true, 5672: true, 6379: true,
		8883: true, 9042: true, 9092: true, 11211: true, 27017: true}
	nonHTTPImages = []string{"postgres", "mysql", "mariadb", "redis", "valkey", "mongo", "memcached", "rabbitmq", "kafka", "zookeeper",
		"clickhouse", "mssql", "cassandra", "etcd", "nats", "mosquitto", "postfix", "smtp", "dragonfly", "keydb", "cockroach"}
)

// defaultImagePort is the port a well-known database or broker image listens
// on, for images that declare no port in the Compose file.
func defaultImagePort(image string) int {
	repo, _ := imageParts(image)
	if id := officialImages[repo]; id != "" {
		return engines.Lookup(id).Port
	}
	for _, k := range []struct {
		name string
		port int
	}{{"postgres", 5432}, {"mariadb", 3306}, {"mysql", 3306}, {"redis", 6379}, {"valkey", 6379}, {"dragonfly", 6379}, {"keydb", 6379},
		{"mongo", 27017}, {"memcached", 11211}, {"rabbitmq", 5672}, {"nats", 4222}, {"mosquitto", 1883}} {
		if strings.Contains(repo, k.name) {
			return k.port
		}
	}
	return 0
}

func isNonHTTPImage(image string) bool {
	repo, _ := imageParts(image)
	for _, n := range nonHTTPImages {
		if strings.Contains(repo, n) {
			return true
		}
	}
	return false
}

func cleanRel(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	return strings.TrimPrefix(path.Clean(p), "./")
}

func (b *builder) mapService(s *cService, ps *ComposePlanService) {
	c := b.choice(s.Key)
	note := func(k, sev, msg string) {
		ps.Notes = append(ps.Notes, ComposeNote{Key: s.Key + "." + k, Severity: sev, Message: msg})
	}

	pm, creds := evaluateManaged(s)
	if pm != nil {
		if e := engines.Lookup(pm.Engine); pm.Eligible && e.LicenseClass == engines.LicenseSourceAvailable {
			// Source-available engines are opt-in: enabling the switch acknowledges the licence.
			pm.Enabled = false
			pm.Reason = e.LicenseNote + " Switch the managed database on to accept the licence."
		}
		if c.ManagedDatabase != nil {
			pm.Enabled = pm.Eligible && *c.ManagedDatabase
		}
		ps.Managed = pm
		if pm.Enabled {
			ps.managedCreds = creds
		}
	}
	managed := pm != nil && pm.Enabled
	tr := parseTraefikLabels(s.Labels)

	// Source.
	switch {
	case managed:
		ps.Source = ComposePlanSource{Type: "managed", Image: s.Image}
		ps.Kind = services.KindDatabase
	case s.Build != nil && c.Source != "image":
		ps.Source = ComposePlanSource{Type: "git", Build: b.mapBuild(s, c, ps)}
	default:
		img := s.Image
		if c.Image != "" {
			img = strings.TrimSpace(c.Image)
		}
		ps.Source = ComposePlanSource{Type: "image", Image: img}
		if img == "" {
			ps.Needs = append(ps.Needs, "image")
		}
		if s.Build != nil && c.Image == "" && s.Image != "" {
			note("build", SevInfo, "This service has a build. Its image name "+s.Image+" is pulled instead; to build it, pick the Git source.")
		}
	}
	if s.HasCommand && !managed {
		ps.Command = s.Command
	}
	if managed {
		eng := engines.Lookup(pm.Engine)
		ps.Port = eng.Port
		ps.PortHTTP = false
		spec := engines.Spec{Engine: pm.Engine, Version: pm.Version}.Normalize()
		_, dataPath := spec.DataMount()
		ps.Volumes = append(ps.Volumes, ComposePlanVolume{Name: "data", Path: dataPath, From: "managed", Note: "Created empty by the managed database."})
		for _, n := range pm.Notes {
			note("managed", SevInfo, n)
		}
	}

	// Environment (skipped for managed databases: the engine owns its credentials).
	envPort := 0
	for _, e := range s.Env {
		pe := ComposePlanEnv{Key: e.Key, Origin: e.Origin, raw: e.Value, Value: e.Value}
		switch {
		case managed:
			pe.Skipped, pe.Note = true, "Managed by the database."
		case !validEnvKey(e.Key):
			pe.Skipped, pe.Note = true, e.Key+" is not a valid variable name here."
		case services.IsPlatformKey(e.Key):
			pe.Skipped, pe.Note = true, e.Key+" is provided by the platform."
			if e.Key == "PORT" {
				envPort, _ = strconv.Atoi(strings.TrimSpace(e.Value))
				pe.Note = "PORT is provided by the platform and matches the service port."
			}
		}
		if ch, ok := c.Env[e.Key]; ok && ch.Skip {
			pe.Skipped = true
			if pe.Note == "" {
				pe.Note = "Skipped by you."
			}
		}
		if len(e.Value) > 64<<10 {
			pe.Skipped, pe.Note = true, "The value is larger than 64 KiB."
		}
		ps.Env = append(ps.Env, pe)
	}

	// Ports and domains.
	if !managed {
		b.mapPorts(s, ps, c, tr, envPort)
	} else {
		for _, p := range s.Ports {
			if p.Published == 0 {
				continue
			}
			pp := ComposePlanPort{Port: p.Target, HostPort: p.Published, Protocol: p.Protocol, Role: p.Protocol, Listen: p.Published}
			pp.Publish = containsStr(c.PublishPorts, fmt.Sprintf("%d/%s", p.Target, p.Protocol))
			ps.Ports = append(ps.Ports, pp)
		}
	}

	// Mounts.
	if !managed {
		b.mapMounts(s, ps, c)
	}

	// Container options.
	if managed {
		if !s.Docker.zero() {
			note("docker", SevInfo, "Container options (capabilities, ulimits, shared memory, ...) are not applied to a managed database; set them in its settings if needed.")
		}
	} else {
		tg := dockerTarget{Unrestricted: b.cx.Unrestricted}
		for _, f := range ps.Files {
			tg.FileTargets = append(tg.FileTargets, f.Path)
		}
		for _, v := range ps.Volumes {
			tg.VolumeMounts = append(tg.VolumeMounts, v.Path)
		}
		ps.Docker = s.Docker.build(tg, note)
	}

	// Limits and lifecycle.
	if s.MemoryMB > 0 {
		if s.MemoryMB >= 6 {
			ps.MemoryMB = s.MemoryMB
		} else {
			note("memory", SevInfo, "The memory limit is below 6 MB and was not applied.")
		}
	}
	ps.CPUs = s.CPUs
	if s.StopGrace > 0 {
		ps.StopGraceSeconds = min(max(int(math.Ceil(s.StopGrace.Seconds())), 1), 3600)
	}
	ps.Replicas = s.Replicas
	for _, n := range s.Networks {
		ps.Networks = append(ps.Networks, n.Name)
	}
	ps.hostNames = append(ps.hostNames, strings.ToLower(s.Key))
	if s.ContainerName != "" {
		ps.hostNames = append(ps.hostNames, strings.ToLower(s.ContainerName))
	}
	for _, n := range s.Networks {
		for _, a := range n.Aliases {
			ps.hostNames = append(ps.hostNames, strings.ToLower(a))
		}
	}
}

var envKeyRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func validEnvKey(k string) bool {
	return envKeyRe.MatchString(k) && len(k) <= 255
}

func containsStr(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

// mapBuild describes where a service with a build is built from.
func (b *builder) mapBuild(s *cService, c ComposeServiceChoice, ps *ComposePlanService) *ComposePlanBuild {
	cb := s.Build
	pb := &ComposePlanBuild{Context: cb.Context, Dockerfile: cb.Dockerfile, Target: cb.Target, ArgNames: []string{}}
	for k := range cb.Args {
		pb.ArgNames = append(pb.ArgNames, k)
	}
	sort.Strings(pb.ArgNames)
	if len(pb.ArgNames) > 0 {
		ps.Notes = append(ps.Notes, ComposeNote{Key: s.Key + ".build.args", Severity: SevWarning, Message: "Build arguments (" + strings.Join(pb.ArgNames, ", ") + ") are not applied; bake them into the Dockerfile or the image."})
	}
	if cb.Target != "" {
		ps.Notes = append(ps.Notes, ComposeNote{Key: s.Key + ".build.target", Severity: SevWarning, Message: "The build target " + cb.Target + " is not applied; the Dockerfile's last stage is built."})
	}

	var ctxRel, subdir string
	switch {
	case c.ConnectionID != nil && c.Repo != "":
		pb.ConnectionID, pb.Repo = c.ConnectionID, c.Repo
		if b.cx.ResolveRepo != nil {
			if u, br, err := b.cx.ResolveRepo(*c.ConnectionID, c.Repo); err == nil {
				pb.GitURL, pb.GitBranch = u, br
			} else {
				ps.Needs = append(ps.Needs, "gitUrl")
				ps.Notes = append(ps.Notes, ComposeNote{Key: s.Key + ".build", Severity: SevWarning, Message: "Could not use the repository " + c.Repo + ": " + err.Error()})
			}
		}
		if c.GitBranch != "" {
			pb.GitBranch = c.GitBranch
		}
		ctxRel = cleanRel(cb.Context)
	case c.GitURL != "":
		pb.GitURL, pb.GitBranch = c.GitURL, c.GitBranch
		ctxRel = cleanRel(cb.Context)
	case cb.URL != "":
		u, ref, sub := splitGitContext(cb.URL)
		pb.GitURL, pb.GitBranch, subdir = u, ref, sub
	case b.cx.Repo != nil:
		pb.GitURL, pb.GitBranch = b.cx.Repo.CloneURL, b.cx.Repo.Branch
		pb.ConnectionID, pb.Repo = b.cx.Repo.ConnectionID, b.cx.Repo.Repo
		ctxRel = cleanRel(path.Join(b.cx.ComposeDir, cb.Context))
		if strings.HasPrefix(ctxRel, "..") || path.IsAbs(cb.Context) {
			ps.Notes = append(ps.Notes, ComposeNote{Key: s.Key + ".build.context", Severity: SevWarning, Message: "The build context " + cb.Context + " lies outside the repository."})
			ps.Needs = append(ps.Needs, "buildContext")
			ctxRel = "."
		}
	default:
		ctxRel = cleanRel(cb.Context)
	}
	if subdir != "" {
		ctxRel = cleanRel(path.Join(subdir, cb.Context))
		if cb.URL != "" {
			ctxRel = cleanRel(subdir)
		}
	}
	if ctxRel == "" {
		ctxRel = "."
	}
	if cb.URL == "" && (strings.HasPrefix(ctxRel, "..") || path.IsAbs(ctxRel)) && b.cx.Repo == nil {
		ps.Notes = append(ps.Notes, ComposeNote{Key: s.Key + ".build.context", Severity: SevWarning, Message: "The build context " + cb.Context + " points outside the repository; using the repository root."})
		ctxRel = "."
	}
	pb.BuildContext = ctxRel
	df := cb.Dockerfile
	if df == "" {
		df = "Dockerfile"
	}
	pb.DockerfilePath = cleanRel(path.Join(ctxRel, df))
	if pb.GitURL == "" {
		ps.Needs = append(ps.Needs, "gitUrl")
	} else if pb.GitBranch == "" {
		pb.GitBranch = "main"
		ps.Notes = append(ps.Notes, ComposeNote{Key: s.Key + ".build", Severity: SevInfo, Message: "No branch was given; the build uses main."})
	}
	return pb
}

// splitGitContext reads https://host/org/repo.git#branch:subdir.
func splitGitContext(ctx string) (repoURL, ref, subdir string) {
	repoURL = ctx
	if i := strings.IndexByte(ctx, '#'); i >= 0 {
		repoURL = ctx[:i]
		frag := ctx[i+1:]
		ref, subdir, _ = strings.Cut(frag, ":")
	}
	if strings.HasPrefix(repoURL, "github.com/") {
		repoURL = "https://" + repoURL
	}
	return repoURL, ref, subdir
}

// mapPorts picks the service port and lists published ports and domains.
func (b *builder) mapPorts(s *cService, ps *ComposePlanService, c ComposeServiceChoice, tr traefikInfo, envPort int) {
	nonHTTPImg := isNonHTTPImage(s.Image)
	var candidates []int
	seen := map[int]bool{}
	addCand := func(p int) {
		if p > 0 && !seen[p] {
			seen[p] = true
			candidates = append(candidates, p)
		}
	}
	for _, p := range s.Ports { // published ports first: they are the ones meant to be reached
		if p.Protocol == "tcp" && p.Published > 0 {
			addCand(p.Target)
		}
	}
	for _, p := range s.Ports {
		if p.Protocol == "tcp" {
			addCand(p.Target)
		}
	}
	for _, p := range s.Expose {
		addCand(p)
	}
	httpish := func(p int) bool { return !nonHTTPImg && !nonHTTPPorts[p] }

	port, explicit := 0, false
	switch {
	case c.Port != nil:
		port, explicit = *c.Port, true
	case tr.Enabled && tr.Port > 0:
		port, explicit = tr.Port, true
	case envPort > 0 && envPort < 65536:
		port, explicit = envPort, true
	default:
		for _, p := range candidates {
			if httpish(p) {
				port = p
				break
			}
		}
		if port == 0 && len(candidates) > 0 {
			port = candidates[0]
		}
	}
	if port == 0 {
		port = defaultImagePort(s.Image)
	}
	ps.Port = port
	ps.PortHTTP = port > 0 && (httpish(port) || (explicit && c.Port == nil && !nonHTTPPorts[port] && !nonHTTPImg))
	if port > 0 && !httpish(port) {
		ps.PortHTTP = false
	}

	servicePublished := false
	for _, p := range s.Ports {
		if p.Published == 0 {
			continue
		}
		role := p.Protocol
		if p.Target == port && p.Protocol == "tcp" && ps.PortHTTP {
			role = "http"
			servicePublished = true
		}
		pp := ComposePlanPort{Port: p.Target, HostPort: p.Published, Protocol: p.Protocol, Role: role, Listen: p.Published}
		if role != "http" {
			pp.Publish = containsStr(c.PublishPorts, fmt.Sprintf("%d/%s", p.Target, p.Protocol))
		}
		ps.Ports = append(ps.Ports, pp)
	}

	// Domains.
	switch {
	case !ps.PortHTTP:
		if c.Domain != nil && c.Domain.Enabled {
			ps.Notes = append(ps.Notes, ComposeNote{Key: s.Key + ".domain", Severity: SevInfo, Message: "A domain needs an HTTP port; this service has none."})
		}
	case c.Domain != nil:
		d := ComposePlanDomain{Enabled: c.Domain.Enabled, Source: "choice"}
		if h := strings.ToLower(strings.TrimSpace(c.Domain.Host)); h != "" {
			d.Host = h
		} else {
			d.Generate = true
		}
		ps.Domains = append(ps.Domains, d)
	case tr.Enabled && len(tr.Hosts) > 0:
		for _, h := range tr.Hosts {
			ps.Domains = append(ps.Domains, ComposePlanDomain{Host: h, Enabled: true, Source: "traefik"})
		}
	default:
		ps.Domains = append(ps.Domains, ComposePlanDomain{Generate: true, Enabled: servicePublished, Source: "suggested"})
	}
	if !tr.Enabled {
		return
	}
	if len(tr.Hosts) > 0 && c.Domain == nil {
		ps.Notes = append(ps.Notes, ComposeNote{Key: s.Key + ".labels", Severity: SevInfo, Message: "Traefik labels were read for the hostnames (" + strings.Join(tr.Hosts, ", ") + "); other labels are ignored."})
	}
}

// mapMounts turns volumes and bind mounts into volumes, files and review items.
func (b *builder) mapMounts(s *cService, ps *ComposePlanService, c ComposeServiceChoice) {
	usedNames := map[string]bool{}
	usedPaths := map[string]bool{}
	volName := func(base string) string {
		n := projects.Slugify(base, "data")
		if len(n) > 60 {
			n = strings.Trim(n[:60], "-")
		}
		out, i := n, 2
		for usedNames[out] {
			out = n + "-" + strconv.Itoa(i)
			i++
		}
		usedNames[out] = true
		return out
	}
	totalFile := 0
	for _, m := range s.Mounts {
		target := path.Clean(m.Target)
		if !path.IsAbs(target) || target == "/" || strings.Contains(target, ":") {
			ps.Binds = append(ps.Binds, ComposePlanBind{Source: m.Source, Target: m.Target, Status: "rejected", Reason: "The container path " + m.Target + " is not supported."})
			continue
		}
		if usedPaths[target] {
			continue
		}
		switch m.Type {
		case "tmpfs":
			ps.Notes = append(ps.Notes, ComposeNote{Key: s.Key + ".volumes", Severity: SevInfo, Message: "The tmpfs mount at " + target + " is not applied."})
		case "volume":
			usedPaths[target] = true
			if m.Source == "" {
				ps.Volumes = append(ps.Volumes, ComposePlanVolume{Name: volName("data" + strings.ReplaceAll(target, "/", "-")), Path: target, From: "anonymous", Note: "An anonymous volume in Compose; it becomes a named volume here."})
				continue
			}
			pv := ComposePlanVolume{Name: volName(m.Source), Path: target, From: "volume"}
			if v, ok := b.p.Volumes[m.Source]; ok {
				if v.External {
					pv.Note = "An external volume in Compose: a new empty volume is created here; existing data is not attached."
				}
				if v.Driver != "" && v.Driver != "local" || len(v.Opts) > 0 {
					pv.Note = "The volume uses driver options (" + v.Driver + "); a plain local volume is created instead."
				}
			}
			ps.Volumes = append(ps.Volumes, pv)
		default: // bind
			b.mapBind(s, ps, c, m, target, usedPaths, volName, &totalFile)
		}
	}
}

func (b *builder) mapBind(s *cService, ps *ComposePlanService, c ComposeServiceChoice, m cMount, target string, usedPaths map[string]bool, volName func(string) string, totalFile *int) {
	src := m.Source
	bind := ComposePlanBind{Source: src, Target: target}
	reject := func(reason string, convert bool) {
		bind.Status, bind.Reason, bind.CanConvert = "rejected", reason, convert
		if convert && containsStr(c.ConvertBinds, target) {
			usedPaths[target] = true
			ps.Volumes = append(ps.Volumes, ComposePlanVolume{Name: volName(path.Base(src)), Path: target, From: "bind", Note: "A directory bind mount in Compose; its existing files are not copied."})
			bind.Status, bind.Reason = "converted", "Turned into a named volume. Existing files in "+src+" are not copied."
		}
		ps.Binds = append(ps.Binds, bind)
	}
	switch {
	case strings.Contains(src, "docker.sock"):
		bind.Status, bind.Reason = "rejected", "The Docker socket is never mounted: it gives a container control of the host."
		ps.Notes = append(ps.Notes, ComposeNote{Key: s.Key + ".volumes", Severity: SevDanger, Message: "The Docker socket mount (" + src + ") is never applied. A service that needs it, such as a reverse proxy or a Docker UI, will not work here."})
		ps.Binds = append(ps.Binds, bind)
		return
	case src == "/etc/localtime" || src == "/etc/timezone":
		bind.Status, bind.Reason = "ignored", "Set the TZ variable instead."
		ps.Binds = append(ps.Binds, bind)
		return
	case strings.HasPrefix(src, "/") || strings.HasPrefix(src, "~"):
		reject("A host path can't be mounted. Use a named volume, or attach a small file.", true)
		return
	}
	rel := cleanRel(src)
	if rel == "" || strings.HasPrefix(rel, "..") {
		reject("The path "+src+" lies outside the project.", true)
		return
	}
	content, found := b.files(rel)
	base := path.Base(rel)
	looksFile := strings.Contains(base, ".") && !strings.HasSuffix(src, "/") && rel != "."
	switch {
	case found:
		if len(content) > services.MaxFileBytes {
			reject(fmt.Sprintf("The file is %d KiB; config files are limited to %d KiB. Bake it into the image or put it on a volume.", len(content)>>10+1, services.MaxFileBytes>>10), false)
			return
		}
		if len(ps.Files) >= services.MaxFiles || *totalFile+len(content) > services.MaxFilesTotalLen {
			reject("Too many or too large files: at most 16 files and 256 KiB in total can be attached.", false)
			return
		}
		usedPaths[target] = true
		*totalFile += len(content)
		mode := ""
		if strings.HasSuffix(base, ".sh") {
			mode = "0755"
		}
		ps.Files = append(ps.Files, ComposePlanFile{Path: target, Mode: mode, Bytes: len(content), Source: src, content: string(content)})
		bind.Status = "file"
		ps.Binds = append(ps.Binds, bind)
	case looksFile:
		bind.Status, bind.Reason = "missing", "The file "+src+" was not supplied: paste its contents to attach it."
		ps.Binds = append(ps.Binds, bind)
		b.p.NeededFiles = append(b.p.NeededFiles, ComposeNeededFile{Path: src, Purpose: "bind", Service: s.Key, Required: true})
	default:
		reject("Directory bind mounts aren't supported: "+src+" lives on the machine that ran Compose. Use a named volume, or put the files in the image.", true)
	}
}

// ---- one-shot detection and finishing ----

func (b *builder) detectOneShots() {
	for _, s := range b.p.Services {
		if !b.byKey[s.Key].Include {
			continue
		}
		for _, d := range s.DependsOn {
			if t := b.byKey[d.Name]; t != nil && d.Condition == "service_completed_successfully" {
				t.OneShot = true
			}
		}
	}
}

func (b *builder) finishService(s *cService, ps *ComposePlanService) {
	managed := ps.Managed != nil && ps.Managed.Enabled
	if ps.OneShot && ps.Port != 0 {
		ps.Notes = append(ps.Notes, ComposeNote{Key: s.Key + ".ports", Severity: SevInfo, Message: "A one-shot service has no port; the ports were not applied."})
		ps.Port, ps.PortHTTP, ps.Ports, ps.Domains = 0, false, []ComposePlanPort{}, []ComposePlanDomain{}
	}
	switch {
	case managed:
		ps.Kind = services.KindDatabase
	case ps.Port == 0:
		ps.Kind = services.KindWorker
	default:
		ps.Kind = services.KindApp
	}
	// Health check.
	if !managed {
		h := &ComposePlanHealth{FromCompose: s.Healthcheck != nil}
		switch {
		case ps.OneShot:
			h.Type, h.DeadlineSeconds, h.FromCompose = services.HealthNone, 900, false
		case s.Healthcheck != nil && s.Healthcheck.None:
			h.Type = services.HealthNone
		case s.Healthcheck != nil:
			hc := s.Healthcheck
			h.Type, h.Command = services.HealthExec, hc.Test
			interval := 30.0
			if hc.Interval > 0 {
				interval = hc.Interval.Seconds()
			}
			h.IntervalSeconds = min(max(int(math.Ceil(interval)), 1), 10)
			timeout := 30.0
			if hc.Timeout > 0 {
				timeout = hc.Timeout.Seconds()
			}
			h.TimeoutSeconds = min(max(int(math.Ceil(timeout)), 1), 60)
			h.StartPeriodSeconds = int(math.Ceil(hc.StartPeriod.Seconds()))
			retries := hc.Retries
			if retries <= 0 {
				retries = 3
			}
			h.DeadlineSeconds = min(max(h.StartPeriodSeconds+h.IntervalSeconds*retries, 120), 3600)
			h.SuccessThreshold = 1
		case ps.Port > 0:
			h.Type = services.HealthTCP
		default:
			h.Type = services.HealthNone
		}
		ps.Health = h
	}
	// Replicas apply to stateless HTTP services only.
	if ps.Replicas > 1 {
		switch {
		case managed:
			ps.ReplicasNote = "A managed database runs as one instance."
		case ps.OneShot:
			ps.ReplicasNote = "A one-shot service runs once."
		case len(ps.Volumes) > 0 || ps.Port == 0:
			ps.ReplicasNote = "Replicas need a stateless service: no volumes and an HTTP port."
		case ps.Replicas > services.MaxReplicas:
			ps.ReplicasNote = fmt.Sprintf("At most %d replicas are supported.", services.MaxReplicas)
			ps.Replicas = services.MaxReplicas
		}
		if ps.ReplicasNote != "" && ps.Replicas != services.MaxReplicas {
			ps.Replicas = 1
		}
	}
}

// ---- cross-service references ----

func (b *builder) rewriteEnv() {
	names := map[string]string{}
	for _, ps := range b.svcs {
		if !ps.Include {
			continue
		}
		for _, n := range ps.hostNames {
			if _, taken := names[n]; !taken {
				names[n] = ps.Key
			}
		}
	}
	managedByKey := map[string]*ComposePlanService{}
	for _, ps := range b.svcs {
		if ps.Include && ps.Managed != nil && ps.Managed.Enabled {
			managedByKey[ps.Key] = ps
		}
	}
	for _, ps := range b.svcs {
		if !ps.Include {
			continue
		}
		c := b.choice(ps.Key)
		for i := range ps.Env {
			pe := &ps.Env[i]
			if pe.Skipped {
				continue
			}
			var ss segs
			orig := pe.raw
			allowRewrite := true
			if ch, ok := c.Env[pe.Key]; ok && ch.Rewrite != nil && !*ch.Rewrite {
				allowRewrite = false
			}
			hits := []hostHit{}
			if allowRewrite {
				for _, h := range findHostRefs(pe.Key, orig, names) {
					if h.target != ps.Key {
						hits = append(hits, h)
					}
				}
			}
			pos := 0
			for _, h := range hits {
				ss = append(ss, lit(orig[pos:h.start])...)
				tgt := b.byKey[h.target]
				if m, ok := managedByKey[h.target]; ok && h.urlStart && (m.Managed.Engine == engines.Redis || m.Managed.Engine == engines.Valkey) {
					ss = append(ss, lit(":")...)
					ss = append(ss, seg{ref: tgt.Slug + "." + passwordExport(m.Managed.Engine)})
					ss = append(ss, lit("@")...)
					b.addRewrite(ps, pe, "auth", h.target, "A Redis URL without a password: the managed database requires one, so its password is added.", orig)
					pe.Rewritten = true
				}
				ss = append(ss, seg{ref: tgt.Slug + "." + services.VarPrivateHost})
				b.addRef(ps.Key, h.target)
				pe.Rewritten = true
				b.addRewrite(ps, pe, "host", h.target, h.reason+": "+orig[h.start:h.end]+" → the private host of "+tgt.Name, orig)
				pos = h.end
			}
			ss = append(ss, lit(orig[pos:])...)

			// Passwords of managed databases are regenerated: point at the export instead.
			for _, key := range sortedKeys(managedByKey) {
				m := managedByKey[key]
				if key == ps.Key {
					continue
				}
				out, replaced := substitutePassword(ss, pe.Key, m.managedCreds.Password, m.Slug+"."+passwordExport(m.Managed.Engine))
				if !replaced {
					continue
				}
				ss = out
				pe.Rewritten, pe.Secret = true, true
				b.addRef(ps.Key, key)
				b.addRewrite(ps, pe, "password", key, "The password of "+m.Name+" is generated by the managed database; the variable references it.", orig)
			}
			if pe.Rewritten {
				pe.raw = ss.render()
				pe.Value = pe.raw
			} else {
				pe.raw = ss.render() // escapes a literal ${{
				pe.Value = pe.raw
			}
		}
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

var passwordKeyRe = regexp.MustCompile(`(?i)(PASSWORD|PASSWD|PASS|PWD|SECRET)`)

// substitutePassword replaces the old password pw of a managed database by a
// reference where it is used as a password: the whole value of a password
// variable, the password of a URL (scheme://user:pw@host) or of a
// password=pw connection string entry. Other occurrences of the same text
// (a user or database that happens to be called like the password) stay.
func substitutePassword(ss segs, key, pw, ref string) (segs, bool) {
	if len(pw) < 4 {
		return ss, false
	}
	q := regexp.QuoteMeta(pw)
	urlRe := regexp.MustCompile(`(://[^:/@\s]*:)(` + q + `)(@)`)
	dsnRe := regexp.MustCompile(`(?i)((?:password|pwd|pass)\s*=\s*)(` + q + `)($|[\s;&'"])`)
	var out segs
	replaced := false
	for _, sg := range ss {
		if sg.ref != "" {
			out = append(out, sg)
			continue
		}
		if sg.lit == pw && len(ss) == 1 && passwordKeyRe.MatchString(key) {
			out = append(out, seg{ref: ref})
			replaced = true
			continue
		}
		cur := []seg{{lit: sg.lit}}
		for _, re := range []*regexp.Regexp{urlRe, dsnRe} {
			var next []seg
			for _, c := range cur {
				if c.ref != "" {
					next = append(next, c)
					continue
				}
				pos := 0
				for _, m := range re.FindAllStringSubmatchIndex(c.lit, -1) {
					g := m[4] // the password group
					next = append(next, lit(c.lit[pos:g])...)
					next = append(next, seg{ref: ref})
					pos = m[5]
					replaced = true
				}
				next = append(next, lit(c.lit[pos:])...)
			}
			cur = next
		}
		out = append(out, cur...)
	}
	return out, replaced
}

func (b *builder) addRef(from, to string) {
	for _, t := range b.refs[from] {
		if t == to {
			return
		}
	}
	b.refs[from] = append(b.refs[from], to)
}

func (b *builder) addRewrite(ps *ComposePlanService, pe *ComposePlanEnv, kind, target, reason, orig string) {
	// The "to" value is filled in after secrets are known (see markSecrets).
	b.rewrites = append(b.rewrites, ComposeRewrite{Service: ps.Key, Key: pe.Key, Kind: kind, Target: target, From: orig, Reason: reason})
}

var secretKeyRe = regexp.MustCompile(`(?i)(PASSWORD|PASSWD|SECRET|TOKEN|PRIVATE_KEY|API_KEY|ADMIN_KEY|MASTER_KEY|ENCRYPTION_KEY|SALT|CREDENTIAL|SIGNING|(^|_)AUTH(_|$)|(^|_)DSN$|(^|_)JWT|COOKIE_SECRET|(^|_)KEY$|(^|_)PASS$)`)
var tokenValueRe = regexp.MustCompile(`^[A-Za-z0-9+/=_-]{32,}$`)

func looksSecret(key, value string) bool {
	if secretKeyRe.MatchString(key) && value != "" && !isPlainFlag(value) {
		return true
	}
	if urlUserinfoRe.MatchString(value) {
		return true
	}
	if tokenValueRe.MatchString(value) && strings.IndexFunc(value, func(r rune) bool { return r >= '0' && r <= '9' }) >= 0 {
		return true
	}
	return false
}

func isPlainFlag(v string) bool {
	switch strings.ToLower(v) {
	case "true", "false", "0", "1", "yes", "no", "on", "off":
		return true
	}
	return false
}

// markSecrets applies the secret heuristic and the reviewer's choices, then
// masks the display values of the rewrites.
func (b *builder) markSecrets() {
	secretOf := map[string]bool{}
	for _, ps := range b.svcs {
		c := b.choice(ps.Key)
		for i := range ps.Env {
			pe := &ps.Env[i]
			if pe.Skipped {
				continue
			}
			guess := pe.Secret || looksSecret(pe.Key, pe.raw) || looksSecret(pe.Key, pe.Value)
			pe.Secret, pe.SecretGuess = guess, guess
			if ch, ok := c.Env[pe.Key]; ok && ch.Secret != nil {
				pe.Secret, pe.SecretGuess = *ch.Secret, false
			}
			if pe.Secret {
				secretOf[ps.Key+"\x00"+pe.Key] = true
			}
		}
	}
	for i := range b.rewrites {
		r := &b.rewrites[i]
		var rendered string
		for _, ps := range b.byKey[r.Service].envOf() {
			if ps.Key == r.Key {
				rendered = ps.raw
			}
		}
		r.To = maskRewriteValue(r.Key, maskDSN(maskURLSecrets(rendered)))
		r.From = maskRewriteValue(r.Key, maskDSN(maskURLSecrets(r.From)))
	}
}

// maskRewriteValue hides a value that is a bare secret (no URL or connection
// string shape to show).
func maskRewriteValue(key, v string) string {
	if secretKeyRe.MatchString(key) && !strings.Contains(v, "://") && !strings.Contains(v, "${{") && !strings.Contains(v, "=") {
		return "••••••"
	}
	return v
}

func (ps *ComposePlanService) envOf() []ComposePlanEnv { return ps.Env }

// linkServices turns depends_on and the references found in variables into
// links (deploy order) without creating cycles.
func (b *builder) linkServices() {
	edges := map[string]map[string]bool{}
	has := func(from, to string) bool { return edges[from][to] }
	reaches := func(from, to string) bool {
		seen := map[string]bool{}
		var walk func(n string) bool
		walk = func(n string) bool {
			if n == to {
				return true
			}
			if seen[n] {
				return false
			}
			seen[n] = true
			for m := range edges[n] {
				if walk(m) {
					return true
				}
			}
			return false
		}
		return walk(from)
	}
	add := func(from, to string, soft bool) bool {
		if from == to || has(from, to) {
			return true
		}
		if reaches(to, from) {
			return false
		}
		if edges[from] == nil {
			edges[from] = map[string]bool{}
		}
		edges[from][to] = true
		return true
	}
	for _, s := range b.p.Services {
		ps := b.byKey[s.Key]
		if !ps.Include {
			continue
		}
		for _, d := range s.DependsOn {
			t := b.byKey[d.Name]
			if t == nil {
				continue
			}
			if !t.Include {
				ps.Notes = append(ps.Notes, ComposeNote{Key: s.Key + ".depends_on", Severity: SevInfo, Message: d.Name + " is not imported, so " + s.Key + " does not wait for it."})
				continue
			}
			if !add(s.Key, d.Name, false) {
				ps.Notes = append(ps.Notes, ComposeNote{Key: s.Key + ".depends_on", Severity: SevWarning, Message: "depends_on on " + d.Name + " would create a deploy-order cycle and was skipped."})
			}
		}
	}
	for _, ps := range b.svcs {
		for _, t := range b.refs[ps.Key] {
			add(ps.Key, t, true)
		}
	}
	for _, ps := range b.svcs {
		keys := make([]string, 0, len(edges[ps.Key]))
		for k := range edges[ps.Key] {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool { return b.index(keys[i]) < b.index(keys[j]) })
		ps.DependsOn = append(ps.DependsOn, keys...)
	}
}

func (b *builder) index(key string) int {
	for i, ps := range b.svcs {
		if ps.Key == key {
			return i
		}
	}
	return len(b.svcs)
}

// sharedVolumes warns about named volumes used by more than one service.
func (b *builder) sharedVolumes() {
	users := map[string][]string{}
	for _, s := range b.p.Services {
		ps := b.byKey[s.Key]
		if !ps.Include || (ps.Managed != nil && ps.Managed.Enabled) {
			continue
		}
		seen := map[string]bool{}
		for _, m := range s.Mounts {
			if m.Type == "volume" && m.Source != "" && !seen[m.Source] {
				seen[m.Source] = true
				users[m.Source] = append(users[m.Source], s.Key)
			}
		}
	}
	names := make([]string, 0, len(users))
	for n := range users {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if l := users[n]; len(l) > 1 {
			b.warnings = append(b.warnings, fmt.Sprintf("The volume %s is shared by %s. Volumes belong to one service here, so each gets its own empty copy: data written by one is not seen by the others.", n, strings.Join(l, ", ")))
		}
	}
}

// networks maps Compose networks to the environment network and shared networks.
func (b *builder) networks() []ComposePlanNetwork {
	users := map[string][]string{}
	for _, ps := range b.svcs {
		if !ps.Include {
			continue
		}
		for _, n := range ps.Networks {
			users[n] = append(users[n], ps.Key)
		}
	}
	names := make([]string, 0, len(users))
	for n := range users {
		if n != "default" {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	out := []ComposePlanNetwork{}
	for _, n := range names {
		cn := b.p.Networks[n]
		pn := ComposePlanNetwork{Name: n, External: cn.External, Services: users[n]}
		switch {
		case cn.External:
			if id, ok := b.cx.SharedNetworks[cn.Name]; ok {
				pn.SharedNetworkID = &id
				pn.Note = "The services attach to the shared network " + cn.Name + "."
			} else {
				pn.Note = "An external network. No shared network named " + cn.Name + " exists on your team: create one under Networks and attach the services, or they use the environment network only."
			}
		default:
			pn.Note = "Merged into the environment network: every service of the environment reaches the others by name."
		}
		out = append(out, pn)
	}
	if len(names) > 1 {
		b.warnings = append(b.warnings, "Compose separated the services into several networks. Here every service of the environment can reach every other one; the separation is not kept.")
	}
	return out
}

func (b *builder) neededFiles() []ComposeNeededFile {
	seen := map[string]bool{}
	out := []ComposeNeededFile{}
	for _, f := range b.p.NeededFiles {
		k := f.Purpose + "\x00" + f.Path
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, f)
	}
	return out
}

func (b *builder) blockers(p *ComposePlan) {
	p.Blockers = []string{}
	for _, r := range b.p.Interp.Required() {
		p.Blockers = append(p.Blockers, "A required variable is missing: "+r+". Add it to the .env contents.")
	}
	included := 0
	for _, ps := range p.Services {
		if !ps.Include {
			continue
		}
		included++
		for _, n := range ps.Needs {
			switch n {
			case "gitUrl":
				p.Blockers = append(p.Blockers, ps.Key+" is built from source: choose the Git repository to build it from, or switch it to an image.")
			case "image":
				p.Blockers = append(p.Blockers, ps.Key+" has no image: enter one.")
			case "buildContext":
				p.Blockers = append(p.Blockers, ps.Key+": the build context is outside the repository; pick the image or another repository.")
			}
		}
	}
	for _, f := range p.NeededFiles {
		svc := b.byKey[f.Service]
		if svc == nil || !svc.Include || !f.Required {
			continue
		}
		what := "the file"
		if f.Purpose == "env_file" {
			what = "the env file"
		}
		p.Blockers = append(p.Blockers, fmt.Sprintf("%s %s of %s was not supplied: paste its contents, or skip the service.", what, f.Path, f.Service))
	}
	if included == 0 {
		p.Blockers = append(p.Blockers, "No service is selected.")
	}
	sort.Strings(p.Blockers)
	p.Installable = len(p.Blockers) == 0
}
