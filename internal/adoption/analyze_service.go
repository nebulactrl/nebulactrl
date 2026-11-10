package adoption

import (
	"fmt"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/databases/engines"
	"github.com/nebulactrl/nebulactrl/internal/projects"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// maxReplicas is the CHECK on services.replicas and service_process_scale.replicas.
const maxReplicas = 10

var (
	serviceSlugRe   = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	anonymousVolume = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// perContainerVars are the variables the agent sets on each container; they are
// not part of a release's environment.
var perContainerVars = map[string]bool{
	services.VarDeploymentID: true, services.VarReleaseID: true, services.VarReplicaIndex: true, services.VarReplicaCount: true,
	"NEBULA_PROCESS": true,
}

const (
	reasonLabels      = "missing NebulaCtrl labels"
	reasonNameMatches = "container name does not match its labels"
)

// service analyses the containers of one service ID.
func (a *analyzer) service(g *group, ch AdoptionServiceChoice) *AdoptionService {
	ps := &AdoptionService{
		ID: g.id, Slug: agentprotocol.Short(g.id.String()), Kind: services.KindApp, SourceType: services.SourceImage, Adoptable: true,
		Processes: []string{}, Containers: []AdoptionContainer{}, Volumes: []AdoptionVolume{}, Domains: []AdoptionDomain{}, Env: []AdoptionVariable{}, Notes: []string{},
	}
	defer func() {
		ps.Name = strings.TrimSpace(ch.Name)
		if ps.Name == "" {
			ps.Name = ps.Slug
		}
	}()
	fail := func(format string, args ...any) *AdoptionService {
		ps.Adoptable = false
		ps.Reason = fmt.Sprintf(format, args...)
		return ps
	}
	note := func(format string, args ...any) { ps.Notes = append(ps.Notes, fmt.Sprintf(format, args...)) }

	if g.routeOnly {
		ps.Kind, ps.SourceType = services.KindRoute, services.SourceExternal
		return fail("route services are not adopted; add the route again")
	}

	// The deployment that runs: the newest running container's, else the newest container's.
	chosenDep := g.cs[0].Labels[agentprotocol.LabelDeployment]
	for _, c := range g.cs {
		if c.Running {
			chosenDep = c.Labels[agentprotocol.LabelDeployment]
			break
		}
	}
	var chosen []agentprotocol.InventoryContainer
	leftover := 0
	for _, c := range g.cs {
		lo := c.Labels[agentprotocol.LabelDeployment] != chosenDep
		if lo {
			leftover++
		} else {
			chosen = append(chosen, c)
		}
		ps.Containers = append(ps.Containers, AdoptionContainer{Name: containerName(c), State: c.State, Leftover: lo})
	}
	sort.SliceStable(ps.Containers, func(i, j int) bool { return ps.Containers[i].Name < ps.Containers[j].Name })
	if leftover > 0 {
		note("Older containers of this service are removed after adoption, as after any deploy.")
	}
	if g.tailnet > 0 {
		note("The tailnet sidecar is not carried over: set up the tailnet again under the service's networking settings.")
	}

	envID, _ := uuid.Parse(chosen[0].Labels[agentprotocol.LabelEnvironment])
	for _, c := range g.cs {
		if _, ok := c.Labels[agentprotocol.LabelComponent]; ok {
			ps.Kind, ps.SourceType = services.KindStack, services.SourceCompose
			ps.rec = &serviceRec{envID: envID}
			return fail("Compose stacks are not adopted: it keeps running; import its compose file again with `nebulactrl import compose --as-stack` "+
				"(it is under /var/lib/nebulactrl-agent/stacks/%s/current/compose.yaml)", agentprotocol.Short(g.id.String()))
		}
	}

	depID, derr := uuid.Parse(chosenDep)
	relID, rerr := uuid.Parse(chosen[0].Labels[agentprotocol.LabelRelease])
	if derr != nil || rerr != nil || envID == uuid.Nil {
		return fail(reasonLabels)
	}
	ps.DeploymentID, ps.ReleaseID = &depID, &relID
	for _, c := range chosen[1:] {
		if c.Labels[agentprotocol.LabelRelease] != chosen[0].Labels[agentprotocol.LabelRelease] || c.Labels[agentprotocol.LabelEnvironment] != chosen[0].Labels[agentprotocol.LabelEnvironment] {
			ps.rec = &serviceRec{envID: envID}
			return fail("containers of one deployment disagree on their release or environment")
		}
	}
	rec := &serviceRec{envID: envID}
	ps.rec = rec

	// Label-trust guard: a container that runs under another name than its
	// labels say could make the agent adopt or remove the wrong thing.
	specs := make([]agentprotocol.SpecLabel, len(chosen))
	for i, c := range chosen {
		sl, ok := agentprotocol.ParseSpecLabel(c.Labels[agentprotocol.LabelSpec])
		if !ok {
			return fail(reasonLabels)
		}
		specs[i] = sl
	}
	slug := specs[0].Slug
	if !serviceSlugRe.MatchString(slug) || services.IsReservedSlug(slug) {
		return fail("the service name %q on its containers is not a valid slug", slug)
	}
	ps.Slug = slug
	for i, c := range chosen {
		sl := specs[i]
		idx, err := strconv.Atoi(c.Labels[agentprotocol.LabelReplica])
		if err != nil || idx < 0 || sl.Slug != slug {
			return fail(reasonNameMatches)
		}
		want := agentprotocol.ContainerName(slug, chosenDep, idx)
		if sl.Process != "" {
			want = agentprotocol.ProcessContainerName(slug, chosenDep, sl.Process, sl.ProcessDefault, idx)
		}
		if containerName(c) != want {
			return fail(reasonNameMatches)
		}
	}

	// Processes.
	byProc := map[string][]int{}
	defaultName := ""
	multi := false
	for i, sl := range specs {
		byProc[sl.Process] = append(byProc[sl.Process], i)
		if sl.Process != "" {
			multi = true
		}
		if sl.ProcessDefault {
			defaultName = sl.Process
		}
	}
	if multi && defaultName == "" {
		return fail("the containers do not say which process is the default one")
	}
	if !multi {
		defaultName = ""
	}
	var named []string
	for p := range byProc {
		if p != defaultName {
			named = append(named, p)
		}
	}
	sort.Strings(named)
	if multi {
		ps.Processes = append([]string{defaultName}, named...)
	}
	// rep is the container the service's own settings are read from: the newest running replica of the default process.
	rep := byProc[defaultName][0]
	for _, i := range byProc[defaultName] {
		if chosen[i].Running {
			rep = i
			break
		}
	}
	repC, repSpec := chosen[rep], specs[rep]
	env := parseEnv(repC.Env)
	imageEnv := map[string]bool{}
	for _, kv := range repC.ImageEnv {
		imageEnv[kv] = true
	}

	for _, c := range chosen {
		if _, ok := c.Labels[agentprotocol.LabelOneShot]; ok {
			return fail("one-shot services are not adopted; add the service again")
		}
	}
	if _, has := env[engines.ReplicationPasswordKey]; has {
		ps.Kind = services.KindDatabase
		return fail("standby replicas are not adopted; add a new replica to the adopted primary")
	}

	// Replicas.
	ps.Replicas = len(byProc[defaultName])
	rec.procScale = map[string]int{}
	for _, p := range named {
		rec.procScale[p] = len(byProc[p])
	}
	if ps.Replicas > maxReplicas {
		return fail("it runs %d replicas; at most %d are supported", ps.Replicas, maxReplicas)
	}
	for _, p := range named {
		if rec.procScale[p] > maxReplicas {
			return fail("the process %s runs %d replicas; at most %d are supported", p, rec.procScale[p], maxReplicas)
		}
	}

	// Source and configuration.
	ref := imageRef(repC)
	if ref == "" {
		return fail("the image of the containers is unknown")
	}
	var cfg services.Config
	var dbSpec *engines.Spec
	if sp, why := a.database(repC, env); sp != nil {
		dbSpec = sp
		ps.Kind = services.KindDatabase
		cfg = services.Config{Database: sp, BlockEgress: repSpec.BlockEgress, MemoryMB: repSpec.MemoryMB, CPUs: repSpec.CPUs}
		ps.Database = &AdoptionDatabase{Engine: sp.Engine, Version: sp.Version, Flavor: sp.Flavor, DatabaseName: sp.DatabaseName, Username: sp.Username}
		if e := engines.Lookup(sp.Engine); e != nil && e.LicenseClass == engines.LicenseSourceAvailable && e.LicenseNote != "" {
			note("%s", e.LicenseNote)
		}
		if len(named) > 0 {
			return fail("a managed database has one process")
		}
	} else {
		if why != "" {
			note("%s", why)
		}
		cfg = services.Config{
			Image: ref, Port: repSpec.Port, Healthcheck: healthcheckOf(repSpec.Healthcheck), StopGraceSeconds: repSpec.StopGraceSeconds,
			RestartPolicy: repSpec.RestartPolicy, RestartMaxRetries: repSpec.RestartMaxRetries, MemoryMB: repSpec.MemoryMB, CPUs: repSpec.CPUs,
			Docker: repSpec.Docker, BlockEgress: repSpec.BlockEgress, Singleton: repSpec.Singleton,
		}
		if len(repC.Cmd) > 0 && !equalStrings(repC.Cmd, repC.ImageCmd) {
			cfg.StartCommand = repC.Cmd
		}
		if multi && defaultName != services.DefaultProcessName {
			cfg.ProcessName = defaultName
		}
		for _, p := range named {
			pi := byProc[p][0]
			for _, i := range byProc[p] {
				if chosen[i].Running {
					pi = i
					break
				}
			}
			pc, psl := chosen[pi], specs[pi]
			cmd := pc.Cmd
			if len(cmd) == 0 {
				cmd = pc.ImageCmd
			}
			cfg.Processes = append(cfg.Processes, services.ProcessConfig{
				Name: p, StartCommand: cmd, Port: psl.Port, Healthcheck: healthcheckOf(psl.Healthcheck), StopGraceSeconds: psl.StopGraceSeconds,
				Singleton: psl.Singleton, MemoryMB: psl.MemoryMB, CPUs: psl.CPUs,
			})
		}
		if len(repSpec.SharedNetworks) > 0 {
			note("Shared networks are not carried over: attach the service to them again under Networks.")
		}
		if repSpec.MeshAlias != "" {
			note("The mesh exposure is not carried over: expose the service again under Networking.")
		}
		hasPort := cfg.Port > 0 || len(g.routes) > 0
		for _, pc := range cfg.Processes {
			hasPort = hasPort || pc.Port > 0
		}
		if !hasPort {
			ps.Kind = services.KindWorker
		}
	}
	cfg = cfg.WithDefaults()
	if err := cfg.Validate(services.SourceImage); err != nil {
		return fail("%s", strings.TrimPrefix(err.Error(), services.ErrInvalidConfig.Error()+": "))
	}
	if err := services.CheckDockerPlacement(cfg, a.cx.Server.Unrestricted); err != nil {
		if ae, ok := apierr.As(err); ok && ae.Code == services.CodeRequiresUnrestricted {
			return fail("needs an unrestricted server: switch it to Unrestricted, then review again")
		}
		return fail("%s", err.Error())
	}
	rec.cfg = cfg
	ps.Image = cfg.Image
	rec.imageRef = cfg.Image
	rec.source = services.ReleaseSource{Type: services.SourceImage, Image: cfg.Image, ImageID: repC.ImageID, Digest: repC.ImageID}
	if len(repC.ImageRepoDigests) > 0 {
		rec.source.Digest = repC.ImageRepoDigests[0]
	}
	if strings.HasPrefix(ref, agentprotocol.BuildImagePrefix) {
		note("Built from git by an earlier NebulaCtrl: connect the repository under Source before the next deploy; until then restarts and rollbacks reuse the image on this host.")
	}

	a.volumes(ps, g, chosen, note)
	a.variables(ps, rec, env, imageEnv, ch, dbSpec)
	a.domains(ps, g, cfg, named, ch)
	// The agent would remove the replicas the service may not have.
	for _, n := range append([]int{ps.Replicas}, procCounts(rec.procScale)...) {
		if services.EffectiveReplicas(ps.Kind, cfg, len(ps.Volumes), n) != n {
			_, why := services.Eligibility(ps.Kind, cfg, len(ps.Volumes))
			return fail("it runs %d replicas, but %s", n, why)
		}
	}
	return a.existing(ps, depID, envID)
}

// existing checks the service against what this instance already knows
// about its ID.
func (a *analyzer) existing(ps *AdoptionService, depID, envID uuid.UUID) *AdoptionService {
	ex, ok := a.cx.Services[ps.ID]
	if !ok {
		return ps
	}
	switch {
	case !ex.SameTeam:
		ps.Adoptable, ps.Reason = false, reasonOtherTeam
	case ex.Deleted || ex.CurrentDeploymentID == nil || *ex.CurrentDeploymentID != depID:
		ps.Adoptable, ps.Reason = false, reasonOtherRelease
	case ex.EnvironmentID != envID:
		ps.Adoptable, ps.Reason = false, "this instance already has this service in another environment"
	case !ex.HostGone:
		ps.Adoptable, ps.Reason = false, "this instance runs this service on another server"
	default:
		ps.Notes = append(ps.Notes, "This instance already has this service; it is moved to this server.")
	}
	return ps
}

func containerName(c agentprotocol.InventoryContainer) string { return strings.TrimPrefix(c.Name, "/") }

// imageRef is the reference the service's image config is rebuilt from: its
// first tag, else its first digest, else what the container was created with.
func imageRef(c agentprotocol.InventoryContainer) string {
	if tags := sortedCopy(c.ImageRepoTags); len(tags) > 0 {
		return tags[0]
	}
	if len(c.ImageRepoDigests) > 0 {
		return c.ImageRepoDigests[0]
	}
	if c.Image != "" {
		return c.Image
	}
	return c.ImageID
}

// database decides whether the container is a managed database. When its image
// is an engine's but cannot be one, why says so (the service is then an image
// service).
func (a *analyzer) database(c agentprotocol.InventoryContainer, env map[string]string) (spec *engines.Spec, why string) {
	refs := append(sortedCopy(c.ImageRepoTags), c.Image)
	refs = append(refs, c.ImageRepoDigests...)
	var firstHit string
	for _, ref := range refs {
		engine, flavor, tag, ok := engines.FromImage(ref)
		if !ok {
			continue
		}
		e := engines.Lookup(engine)
		if firstHit == "" {
			firstHit = tag
		}
		version, ok := engines.ExactVersion(e, flavor, tag)
		if !ok {
			continue
		}
		sp := engines.Spec{Engine: engine, Version: version, Flavor: flavor, DatabaseName: env[e.DatabaseKey], Username: env[e.UserKey]}.Normalize()
		// A look-alike (KeyDB, Dragonfly) shares the engine but not the image.
		wantRepo, _ := engines.RepoTag(sp.Image())
		gotRepo, _ := engines.RepoTag(ref)
		if wantRepo != gotRepo {
			continue
		}
		if err := sp.Validate(); err != nil {
			return nil, fmt.Sprintf("It cannot be a managed database (%s); adopted as an image service.", err)
		}
		return &sp, ""
	}
	if firstHit != "" {
		return nil, fmt.Sprintf("Image tag %s is not a managed catalog version; adopted as an image service.", firstHit)
	}
	return nil, ""
}

func healthcheckOf(h agentprotocol.HealthcheckSpec) services.Healthcheck {
	return services.Healthcheck{
		Type: h.Type, Path: h.Path, Port: h.Port, IntervalSeconds: h.IntervalSeconds, TimeoutSeconds: h.TimeoutSeconds,
		StartPeriodSeconds: h.StartPeriodSeconds, DeadlineSeconds: h.DeadlineSeconds, SuccessThreshold: h.SuccessThreshold, Command: h.Command,
	}
}

// volumes lists the Docker volumes the containers mount.
func (a *analyzer) volumes(ps *AdoptionService, g *group, chosen []agentprotocol.InventoryContainer, note func(string, ...any)) {
	prefix := agentprotocol.VolumeName(g.id.String(), "")
	seen := map[string]bool{}
	names := map[string]bool{}
	for _, c := range chosen {
		for _, m := range c.Mounts {
			switch m.Type {
			case "bind":
				if !seen["bind:"+m.Destination] {
					seen["bind:"+m.Destination] = true
					note("The bind mount %s on %s is not carried over: it stays on the host; mount the files again with a volume or a file mount.", m.Source, m.Destination)
				}
			case "volume":
				if seen[m.Destination] {
					continue
				}
				seen[m.Destination] = true
				if anonymousVolume.MatchString(m.Name) {
					note("The anonymous volume at %s is not carried over: its data is not adopted.", m.Destination)
					continue
				}
				v := AdoptionVolume{MountPath: m.Destination}
				if n, ok := strings.CutPrefix(m.Name, prefix); ok && n != "" {
					v.Name = n
				} else {
					v.Name = projects.Slugify(path.Base(m.Destination), "data")
					v.DockerName = m.Name
				}
				base := v.Name
				for i := 2; names[v.Name]; i++ {
					v.Name = base + "-" + strconv.Itoa(i)
				}
				names[v.Name] = true
				ps.Volumes = append(ps.Volumes, v)
			}
		}
	}
	sort.SliceStable(ps.Volumes, func(i, j int) bool { return ps.Volumes[i].Name < ps.Volumes[j].Name })
}

// variables reads the container's variables. The plan lists the user's by
// key; the release keeps all but the image's own and the per-container ones.
func (a *analyzer) variables(ps *AdoptionService, rec *serviceRec, env map[string]string, imageEnv map[string]bool,
	ch AdoptionServiceChoice, db *engines.Spec) {
	var engineEnv map[string]string
	var eng *engines.Engine
	if db != nil {
		engineEnv, eng = db.ContainerEnv(), engines.Lookup(db.Engine)
	}
	rec.releaseVar = map[string]string{}
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v := env[k]
		if imageEnv[k+"="+v] || perContainerVars[k] {
			continue
		}
		if ev, ok := engineEnv[k]; ok && ev == v {
			continue
		}
		rec.releaseVar[k] = v
		if services.IsPlatformKey(k) {
			continue
		}
		pe := AdoptionVariable{Key: k, value: v}
		if s, ok := ch.Secret[k]; ok {
			pe.Secret, pe.SecretReason = s, "choice"
		} else {
			switch {
			case eng != nil && (k == eng.PasswordKey || k == eng.RootPasswordKey):
				pe.Secret, pe.SecretReason = true, "database password"
			case services.LooksSecretKey(k):
				pe.Secret, pe.SecretReason = true, "name"
			case services.ValueHasCredentials(v):
				pe.Secret, pe.SecretReason = true, "url credentials"
			}
		}
		if !pe.Secret {
			pe.SecretReason = ""
		}
		ps.Env = append(ps.Env, pe)
	}
}

// domains lists the host names the proxy served for the service.
func (a *analyzer) domains(ps *AdoptionService, g *group, cfg services.Config, named []string, ch AdoptionServiceChoice) {
	procOf := map[string]string{}
	procPort := map[string]int{"": cfg.Port}
	for _, p := range named {
		procOf[agentprotocol.ProcessRouteID(g.id.String(), p)] = p
		for _, pc := range cfg.Processes {
			if pc.Name == p {
				procPort[p] = pc.Port
			}
		}
	}
	skip := map[string]bool{}
	for _, h := range ch.SkipHosts {
		skip[strings.ToLower(strings.TrimSpace(h))] = true
	}
	l4 := 0
	for _, r := range g.routes {
		if r.IsL4() {
			l4++
			continue
		}
		component := ""
		if r.Stack != "" && r.ServiceID != r.Stack {
			p, ok := procOf[r.ServiceID]
			if !ok {
				ps.Notes = append(ps.Notes, "A route of a process that is not running was left out.")
				continue
			}
			component = p
		}
		rules := map[string][]agentprotocol.DomainRule{}
		for _, d := range r.Domains {
			h := strings.ToLower(d.Host)
			rules[h] = append(rules[h], d)
		}
		port := 0
		if len(r.Upstreams) > 0 {
			u := r.Upstreams[0]
			port, _ = strconv.Atoi(u[strings.LastIndexByte(u, ':')+1:])
		}
		if port == 0 {
			port = procPort[component]
		}
		for _, h := range r.Hosts {
			h = strings.ToLower(h)
			rs := rules[h]
			if len(rs) == 0 {
				rs = []agentprotocol.DomainRule{{Host: h}}
			}
			for _, rule := range rs {
				rule.Host = h
				pd := AdoptionDomain{Host: h, PathPrefix: rule.PathPrefix, TargetPort: port, Include: true}
				key := h + rule.PathPrefix
				acc := rule.Options.Access
				switch {
				case rule.AuthRef != "" || len(rule.AuthUsers) > 0 || rule.Options.BasicAuth || rule.OriginLocked || rule.RequireClientCert || rule.Tunnel ||
					len(r.AllowCIDRs) > 0 || len(r.DenyCIDRs) > 0 || (acc != nil && (len(acc.AllowCIDRs) > 0 || len(acc.DenyCIDRs) > 0)):
					pd.Include, pd.Note = false, "access rules cannot be read back; the domain is left out so it does not become public: add it again with its rules"
				case rule.TLSMode == agentprotocol.TLSCustom:
					pd.Include, pd.Note = false, "custom certificate: add the domain again and upload the certificate"
				case a.cx.Domains[key] || a.seenDomains[key]:
					pd.Include, pd.Note = false, "already used by a service of this instance"
				case skip[h]:
					pd.Include, pd.Note = false, "left out by choice"
				case port == 0:
					pd.Include, pd.Note = false, "the service has no port to route to"
				}
				if pd.Include {
					// Only what a new domain on this instance would be allowed to be: the same checks
					// the normal create path runs (domains.CheckNewDomain), and the service's port.
					if _, _, err := cfg.ResolveDomainProcess(component, port); err != nil {
						pd.Include, pd.Note = false, "the service cannot route this host name: "+errText(err)
					} else if a.cx.CheckDomain != nil {
						if err := a.cx.CheckDomain(rule); err != nil {
							pd.Include, pd.Note = false, "this host name is not available here: "+errText(err)
						}
					}
				}
				if pd.Include {
					a.seenDomains[key] = true
					pd.rule = &domainRec{rule: rule, component: component}
				}
				ps.Domains = append(ps.Domains, pd)
			}
		}
	}
	if l4 > 0 {
		ps.Notes = append(ps.Notes, fmt.Sprintf("%d tcp/udp route(s) are not adopted: add them again as route services.", l4))
	}
	sort.SliceStable(ps.Domains, func(i, j int) bool {
		if ps.Domains[i].Host != ps.Domains[j].Host {
			return ps.Domains[i].Host < ps.Domains[j].Host
		}
		return ps.Domains[i].PathPrefix < ps.Domains[j].PathPrefix
	})
}

func procCounts(m map[string]int) []int {
	out := make([]int, 0, len(m))
	for _, n := range m {
		out = append(out, n)
	}
	return out
}

func parseEnv(kv []string) map[string]string {
	out := make(map[string]string, len(kv))
	for _, s := range kv {
		if k, v, ok := strings.Cut(s, "="); ok && k != "" {
			out[k] = v
		}
	}
	return out
}

func sortedCopy(s []string) []string {
	out := append([]string(nil), s...)
	sort.Strings(out)
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
