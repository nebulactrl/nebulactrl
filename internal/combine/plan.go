package combine

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/internal/varexpr"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

const (
	minMembers = 2
	maxMembers = 10
	// previewMax bounds the value of a non-secret variable shown in a plan.
	previewMax = 120
)

// member is one service of a combine with everything the plan needs to judge it.
type member struct {
	sc      services.Scope
	cfg     services.Config
	domains []db.Domain
	tasks   []db.ServiceTask
	volumes []db.Volume
	git     *db.ServiceGit
	vars    map[string]varRow
	nets    []services.NetworkRef
	// process is the process name the member gets on the target ("" for the target).
	process string
}

func (m *member) id() uuid.UUID { return m.sc.Service.ID }

// varRow is a stored variable with its value (and template) opened.
type varRow struct {
	row      db.ServiceVariable
	value    string
	template string
}

// analysis is a plan together with the data a start needs to carry it out.
type analysis struct {
	env     services.EnvScope
	plan    CombinePlan
	target  *member
	sources []*member
	// all lists the target first, then the sources by slug.
	all []*member
	// keys are the variables of the members, rewritten for the target.
	keys map[string]*keyInfo
	// edges are the variable references of the environment (rewriting needs them to
	// restage the dependents of a changed target variable).
	dependents map[uuid.UUID][]string
}

// keyInfo is one variable key across the members.
type keyInfo struct {
	key string
	// holders lists the members that set the key, target first.
	holders []keyHolder
	// identities groups the holders by (rewritten value, secret, scope).
	identities []*identity
}

type keyHolder struct {
	m     *member
	row   varRow
	value string // the value with references rewritten to the target
	tmpl  string
}

type identity struct {
	holders []keyHolder
	secret  bool
	scope   string
}

func scopeOf(v db.ServiceVariable) string {
	if v.Scope == "" {
		return services.ScopeRuntime
	}
	return v.Scope
}

// storedPlan is service_combines.plan: the accepted request and every resolved
// move, so the later steps never recompute what the user reviewed.
type storedPlan struct {
	Request            CombineRequest    `json:"request"`
	ProcessNames       map[string]string `json:"processNames"`
	DefaultProcessName string            `json:"defaultProcessName"`
	TargetSlug         string            `json:"targetSlug"`
	SourceSlugs        map[string]string `json:"sourceSlugs"`
	DomainMoves        []DomainMove      `json:"domainMoves"`
	TaskMoves          []TaskMove        `json:"taskMoves"`
	// StoppedServiceIDs are the sources stopped before the deployment
	// (stopSourcesFirst); a failed or aborted combine starts them again.
	StoppedServiceIDs []uuid.UUID `json:"stoppedServiceIds"`
	// StagedFields are the pending-change fields the start wrote on the target:
	// an abort discards exactly these.
	StagedFields []string `json:"stagedFields"`
	// OriginalProcessName and VariableBackups are what an abort puts back on a
	// target that was never deployed: there is no release to revert to then.
	OriginalProcessName string                    `json:"originalProcessName"`
	VariableBackups     map[string]variableBackup `json:"variableBackups,omitempty"`
}

// Plan reports what combining the services would do, and whether it can start.
// It changes nothing.
func (s *Service) Plan(ctx context.Context, envID uuid.UUID, in CombinePlanRequest) (CombinePlan, error) {
	env, err := services.LoadEnvironment(ctx, s.q, policy.ScopeProjectsWrite, envID)
	if err != nil {
		return CombinePlan{}, err
	}
	if err := requireWrite(ctx, &env.Env.ProjectID); err != nil {
		return CombinePlan{}, err
	}
	a, err := s.analyze(ctx, s.q, env, in.ServiceIDs, in.TargetServiceID, nil, "")
	if err != nil {
		return CombinePlan{}, err
	}
	return a.plan, nil
}

// analyze loads the members through q and judges them. names are the process
// names of the sources by id (nil: the suggested ones); defaultName renames the
// target's own process (empty: it keeps its name).
func (s *Service) analyze(ctx context.Context, q *db.Queries, env services.EnvScope, ids []uuid.UUID, targetID *uuid.UUID, names map[uuid.UUID]string, defaultName string) (*analysis, error) {
	if len(ids) < minMembers || len(ids) > maxMembers {
		return nil, apierr.Unprocessable("validation_failed", fmt.Sprintf("combine %d to %d services", minMembers, maxMembers))
	}
	seen := map[uuid.UUID]bool{}
	for _, id := range ids {
		if seen[id] {
			return nil, apierr.Unprocessable("validation_failed", "a service is listed twice")
		}
		seen[id] = true
	}
	if targetID != nil && !seen[*targetID] {
		return nil, apierr.Unprocessable("validation_failed", "the target is not one of the services")
	}
	a := &analysis{env: env, keys: map[string]*keyInfo{}, dependents: map[uuid.UUID][]string{}}
	byID := map[uuid.UUID]*member{}
	for _, id := range ids {
		sc, err := services.LoadScope(ctx, q, policy.ScopeProjectsWrite, id)
		if err != nil {
			return nil, err
		}
		if sc.EnvironmentID != env.Env.ID {
			return nil, apierr.Unprocessable("validation_failed", "every service must belong to the environment")
		}
		m, err := s.loadMember(ctx, q, sc)
		if err != nil {
			return nil, err
		}
		byID[id] = m
	}
	a.target = pickTarget(byID, targetID)
	a.all = append(a.all, a.target)
	for _, m := range byID {
		if m != a.target {
			a.sources = append(a.sources, m)
		}
	}
	slices.SortFunc(a.sources, func(x, y *member) int { return cmp.Compare(x.sc.Service.Slug, y.sc.Service.Slug) })
	a.all = append(a.all, a.sources...)

	defName := a.target.cfg.DefaultProcessName()
	if defaultName != "" {
		defName = defaultName
	}
	assignProcessNames(a, names, defName)
	if err := s.judge(ctx, q, a, defName, defaultName); err != nil {
		return nil, err
	}
	return a, nil
}

// pickTarget is the requested target, else the member with the most domains
// (the one that keeps serving the most traffic), then the oldest.
func pickTarget(byID map[uuid.UUID]*member, want *uuid.UUID) *member {
	if want != nil {
		return byID[*want]
	}
	var best *member
	for _, m := range byID {
		switch {
		case best == nil:
			best = m
		case len(m.domains) != len(best.domains):
			if len(m.domains) > len(best.domains) {
				best = m
			}
		case !m.sc.Service.CreatedAt.Equal(best.sc.Service.CreatedAt):
			if m.sc.Service.CreatedAt.Before(best.sc.Service.CreatedAt) {
				best = m
			}
		case m.id().String() < best.id().String():
			best = m
		}
	}
	return best
}

func (s *Service) loadMember(ctx context.Context, q *db.Queries, sc services.Scope) (*member, error) {
	id := sc.Service.ID
	m := &member{sc: sc, cfg: sc.Config(), vars: map[string]varRow{}}
	var err error
	if m.domains, err = q.ListServiceDomains(ctx, id); err != nil {
		return nil, fmt.Errorf("list domains: %w", err)
	}
	if m.tasks, err = q.ListServiceTasks(ctx, id); err != nil {
		return nil, fmt.Errorf("list tasks: %w", err)
	}
	if m.volumes, err = q.ListServiceVolumes(ctx, id); err != nil {
		return nil, fmt.Errorf("list volumes: %w", err)
	}
	g, err := q.GetServiceGit(ctx, id)
	switch {
	case err == nil:
		m.git = &g
	case !errors.Is(err, pgx.ErrNoRows):
		return nil, fmt.Errorf("get git link: %w", err)
	}
	rows, err := q.ListServiceVariables(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("list variables: %w", err)
	}
	for _, r := range rows {
		val, err := s.Box.OpenString(secretbox.ServiceVariablesValue, secretbox.RowID(id.String(), r.Key), r.ValueEnc)
		if err != nil {
			return nil, fmt.Errorf("open variable %s of %s: %w", r.Key, sc.Service.Slug, err)
		}
		vr := varRow{row: r, value: val}
		if len(r.TemplateEnc) > 0 {
			if vr.template, err = s.Box.OpenString(secretbox.ServiceVariablesTemplate, secretbox.RowID(id.String(), r.Key), r.TemplateEnc); err != nil {
				return nil, fmt.Errorf("open template of %s of %s: %w", r.Key, sc.Service.Slug, err)
			}
		}
		m.vars[r.Key] = vr
	}
	nets, err := services.ListNetworks(ctx, q, []uuid.UUID{id})
	if err != nil {
		return nil, err
	}
	m.nets = nets[id]
	return m, nil
}

// assignProcessNames gives each source its requested name, else a suggestion
// that is unique among the members and differs from the default process' name.
func assignProcessNames(a *analysis, names map[uuid.UUID]string, defName string) {
	taken := map[string]bool{defName: true}
	for _, m := range a.sources {
		if n, ok := names[m.id()]; ok {
			m.process = n
			taken[n] = true
		}
	}
	for _, m := range a.sources {
		if m.process == "" {
			m.process = suggestName(m.sc.Service.Slug, a.target.sc.Service.Slug, taken)
			taken[m.process] = true
		}
	}
}

// suggestName derives a process name from a slug: "shop-worker" next to the
// target "shop" becomes "worker". The name is unique among taken.
func suggestName(slug, targetSlug string, taken map[string]bool) string {
	base := strings.TrimPrefix(slug, targetSlug+"-")
	if base == "" {
		base = slug
	}
	var b strings.Builder
	for _, r := range strings.ToLower(base) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	base = strings.Trim(b.String(), "-")
	if len(base) > 27 { // room for a numeric suffix
		base = strings.Trim(base[:27], "-")
	}
	if base == "" {
		base = "worker"
	}
	name := base
	for i := 2; taken[name] || !services.ProcessNamePattern.MatchString(name); i++ {
		name = fmt.Sprintf("%s-%d", base, i)
	}
	return name
}

// judge fills the plan: members, blockers, warnings, variables, moves and the
// fingerprint.
func (s *Service) judge(ctx context.Context, q *db.Queries, a *analysis, defName, requestedDefault string) error {
	p := &a.plan
	p.TargetServiceID = a.target.id()
	p.Blocking, p.Warnings = []CombineBlocker{}, []string{}
	p.SettingsIgnored, p.DomainMoves, p.TaskMoves = []IgnoredSetting{}, []DomainMove{}, []TaskMove{}
	block := func(code string, m *member, msg string) *CombineBlocker {
		b := CombineBlocker{Code: code, Message: msg}
		if m != nil {
			id := m.id()
			b.ServiceID = &id
		}
		p.Blocking = append(p.Blocking, b)
		return &p.Blocking[len(p.Blocking)-1]
	}
	warn := func(format string, args ...any) { p.Warnings = append(p.Warnings, fmt.Sprintf(format, args...)) }

	memberIDs := map[uuid.UUID]bool{}
	for _, m := range a.all {
		memberIDs[m.id()] = true
		mv := Member{
			ServiceID: m.id(), Slug: m.sc.Service.Slug, Name: m.sc.Service.Name, Kind: m.sc.Service.Kind, Port: m.cfg.Port,
			Command: nonNilStrings(m.cfg.StartCommand), Generation: m.sc.Service.Generation,
			HasStagedChanges: m.sc.Service.Generation > m.sc.Service.AppliedGeneration, IsTarget: m == a.target,
			SuggestedProcessName: m.process, Domains: []DomainRef{},
		}
		if m == a.target {
			mv.SuggestedProcessName = defName
		}
		for _, d := range m.domains {
			mv.Domains = append(mv.Domains, DomainRef{ID: d.ID, Hostname: d.Hostname})
		}
		p.Members = append(p.Members, mv)
	}

	// structural is set when a member cannot become a process at all; the merged
	// configuration is then not validated (its errors would repeat the cause).
	structural := false

	// 1. Kind, source, build settings.
	for _, m := range a.all {
		if k := m.sc.Service.Kind; k != services.KindApp && k != services.KindWorker {
			block(CodeNotCombinable, m, fmt.Sprintf("%s is a %s service: only apps and workers can be combined", m.sc.Service.Slug, k))
			structural = true
			continue
		}
		if m.sc.Service.SourceType != services.SourceGit {
			block(CodeNotCombinable, m, fmt.Sprintf("%s is built from an image: only services built from a git repository can be combined", m.sc.Service.Slug))
			structural = true
			continue
		}
		if m.cfg.OneShot {
			block(CodeNotCombinable, m, fmt.Sprintf("%s is a one-shot service, which cannot run next to other processes", m.sc.Service.Slug))
			structural = true
		}
	}
	for _, m := range a.sources {
		if fields := sourceDiff(a.target, m); len(fields) > 0 {
			b := block(CodeSourceMismatch, m, fmt.Sprintf("%s is built differently from %s (%s): combined services share one build", m.sc.Service.Slug, a.target.sc.Service.Slug, strings.Join(fields, ", ")))
			b.Fields = fields
		}
	}

	// 2. Server and agent.
	for _, m := range a.sources {
		if !sameServer(a.target.sc.Service.ServerID, m.sc.Service.ServerID) {
			block(CodeServerMismatch, m, fmt.Sprintf("%s runs on another server than %s: combined services run on one server", m.sc.Service.Slug, a.target.sc.Service.Slug))
		}
	}
	if sid := a.target.sc.Service.ServerID; sid != nil {
		srv, err := q.LoadServerRow(ctx, db.LoadServerRowParams{ID: *sid})
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			block(CodeServerUnavailable, a.target, "the server of the services no longer exists")
		case err != nil:
			return fmt.Errorf("load server: %w", err)
		default:
			if srv.Status != "online" {
				block(CodeServerUnavailable, a.target, fmt.Sprintf("the server %s is %s", srv.Name, srv.Status))
			}
			if has, _ := services.ServerHasCapability(srv, agentprotocol.CapProcesses); !has {
				block(CodeAgentTooOld, a.target, fmt.Sprintf("the agent of %s does not run several processes of one service yet: upgrade it first", srv.Name))
			}
		}
	} else {
		warn("The services have no server yet: assign one before the combined service can deploy.")
	}

	// 3. Activity.
	for _, m := range a.all {
		if n, err := q.CountActiveDeployments(ctx, m.id()); err != nil {
			return fmt.Errorf("count active deployments: %w", err)
		} else if n > 0 {
			block(CodeDeploymentInProgress, m, fmt.Sprintf("a deployment of %s is in progress: wait for it to finish or cancel it", m.sc.Service.Slug))
		}
		if n, err := q.CountActiveCombinesForService(ctx, m.id()); err != nil {
			return fmt.Errorf("count active combines: %w", err)
		} else if n > 0 {
			block(CodeCombineInProgress, m, fmt.Sprintf("%s already takes part in a combine that is not finished", m.sc.Service.Slug))
		}
	}

	// 4. Volumes, existing processes, start commands.
	for _, m := range a.sources {
		if len(m.volumes) > 0 {
			block(CodeSourceHasVolumes, m, fmt.Sprintf("%s has volumes, which belong to one service: make it the target, or detach its volumes first", m.sc.Service.Slug))
		}
	}
	for _, m := range a.all {
		if m.cfg.HasProcesses() {
			block(CodeAlreadyHasProcesses, m, fmt.Sprintf("%s already has several processes", m.sc.Service.Slug))
			structural = true
		}
	}
	noCommand := false
	for _, m := range a.sources {
		if len(m.cfg.StartCommand) == 0 {
			noCommand = true
			block(CodeInvalidConfig, m, fmt.Sprintf("%s has no start command: a process needs one, so set the command the service runs first", m.sc.Service.Slug))
		}
	}
	if len(a.target.volumes) > 0 || len(a.target.cfg.Files) > 0 {
		warn("%s has volumes or files, so every process of the combined service runs as a single instance.", a.target.sc.Service.Slug)
	}

	// 5. Ignored settings and dropped commands.
	for _, m := range a.sources {
		s.ignoredSettings(p, a.target, m)
	}

	// 6. Ports and the merged configuration.
	portOwner := map[int]*member{}
	portClash := false
	for _, m := range a.all {
		if m.cfg.Port <= 0 {
			continue
		}
		if other, ok := portOwner[m.cfg.Port]; ok {
			portClash = true
			block(CodePortConflict, m, fmt.Sprintf("%s and %s both listen on port %d: every process of one service needs its own port", other.sc.Service.Slug, m.sc.Service.Slug, m.cfg.Port))
			continue
		}
		portOwner[m.cfg.Port] = m
	}
	if !portClash && !noCommand && !structural {
		if err := mergedConfig(a, requestedDefault).Validate(services.SourceGit); err != nil {
			block(CodeInvalidConfig, a.target, fmt.Sprintf("the combined configuration is not valid: %s", services.ConfigError(err)))
		}
	}

	// 7. Tasks.
	taskNames := map[string]*member{}
	for _, m := range a.all {
		for _, t := range m.tasks {
			key := strings.ToLower(t.Name)
			if other, ok := taskNames[key]; ok && other != m {
				block(CodeTaskNameConflict, m, fmt.Sprintf("%s and %s both have a task named %q: rename one first", other.sc.Service.Slug, m.sc.Service.Slug, t.Name))
				continue
			}
			taskNames[key] = m
		}
	}

	// 8. References from outside to a source.
	if err := s.referencesTo(ctx, q, a, memberIDs, block, warn); err != nil {
		return err
	}

	// 9. Variables, moves, warnings.
	s.classifyVariables(a)
	for _, m := range a.sources {
		for _, d := range m.domains {
			if m.cfg.Port > 0 && int(d.TargetPort) != m.cfg.Port {
				block(CodeInvalidConfig, m, fmt.Sprintf("the domain %s of %s routes to port %d, not to the service port %d: change it first", d.Hostname, m.sc.Service.Slug, d.TargetPort, m.cfg.Port))
			}
			p.DomainMoves = append(p.DomainMoves, DomainMove{DomainID: d.ID, Hostname: d.Hostname, FromServiceID: m.id(), Process: m.process})
		}
		for _, t := range m.tasks {
			p.TaskMoves = append(p.TaskMoves, TaskMove{TaskID: t.ID, Name: t.Name, FromServiceID: m.id()})
		}
		if m.sc.Service.Generation > m.sc.Service.AppliedGeneration {
			warn("%s has staged changes; they are not carried over and are lost with the service.", m.sc.Service.Slug)
		}
		if m.sc.Service.DesiredState == services.DesiredStopped {
			warn("%s is stopped; its process starts with the combined deployment.", m.sc.Service.Slug)
		}
	}
	if a.target.sc.Service.Generation > a.target.sc.Service.AppliedGeneration {
		warn("%s has staged changes; they are deployed together with the combine.", a.target.sc.Service.Slug)
	}
	p.Fingerprint = s.fingerprint(a)
	return nil
}

func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func sameServer(a, b *uuid.UUID) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// sourceDiff lists the build settings in which m differs from the target.
func sourceDiff(t, m *member) []string {
	var f []string
	add := func(name string, differs bool) {
		if differs {
			f = append(f, name)
		}
	}
	tc, mc := t.cfg, m.cfg
	add("config.gitUrl", tc.GitURL != mc.GitURL && !services.SameGitRepo(tc.GitURL, mc.GitURL))
	add("config.gitBranch", tc.GitBranch != mc.GitBranch)
	add("config.buildType", tc.BuildType != mc.BuildType)
	add("config.dockerfilePath", tc.DockerfilePath != mc.DockerfilePath)
	add("config.buildContext", tc.BuildContext != mc.BuildContext)
	add("config.submodules", tc.Submodules != mc.Submodules)
	add("config.buildLocation", tc.BuildLocation != mc.BuildLocation)
	add("config.buildServerId", !sameServer(tc.BuildServerID, mc.BuildServerID))
	add("config.registryId", !sameServer(tc.RegistryID, mc.RegistryID))
	switch {
	case t.git == nil && m.git == nil:
	case t.git == nil || m.git == nil:
		add("git connection", true)
	default:
		add("git connection", t.git.ConnectionID != m.git.ConnectionID)
		add("git repository", t.git.RepoFullName != m.git.RepoFullName)
	}
	return f
}

// ignoredSettings records the settings of a source that the target's values
// replace, and warns about the commands that are dropped.
func (s *Service) ignoredSettings(p *CombinePlan, t, m *member) {
	add := func(field, note string) {
		p.SettingsIgnored = append(p.SettingsIgnored, IgnoredSetting{ServiceID: m.id(), Field: field, Note: note})
	}
	const kept = "the target's value is kept"
	if len(m.cfg.Files) > 0 {
		add("config.files", "the files of the source are not mounted; the target's files are kept")
	}
	if jsonDiffers(t.cfg.Docker, m.cfg.Docker) {
		add("config.docker", kept)
	}
	if t.cfg.BlockEgress != m.cfg.BlockEgress {
		add("config.blockEgress", kept)
	}
	if t.cfg.ImageRetention != m.cfg.ImageRetention {
		add("config.imageRetention", kept)
	}
	const inherited = "the source ran without this limit; its process runs with the target's"
	if m.cfg.MemoryMB == 0 && t.cfg.MemoryMB != 0 {
		add("config.memoryMb", inherited)
		p.Warnings = append(p.Warnings, fmt.Sprintf("%s ran without a memory limit; its process runs with the target's %d MB.", m.sc.Service.Slug, t.cfg.MemoryMB))
	}
	if m.cfg.CPUs == 0 && t.cfg.CPUs != 0 {
		add("config.cpus", inherited)
		p.Warnings = append(p.Warnings, fmt.Sprintf("%s ran without a CPU limit; its process runs with the target's %g CPUs.", m.sc.Service.Slug, t.cfg.CPUs))
	}
	if len(m.cfg.PreDeployCommand) > 0 && jsonDiffers(t.cfg.PreDeployCommand, m.cfg.PreDeployCommand) {
		add("config.preDeployCommand", "the source's command is dropped; the target runs its own once per deployment")
		p.Warnings = append(p.Warnings, fmt.Sprintf("The pre-deploy command of %s is dropped.", m.sc.Service.Slug))
	}
	if len(m.cfg.PostDeployCommand) > 0 && jsonDiffers(t.cfg.PostDeployCommand, m.cfg.PostDeployCommand) {
		add("config.postDeployCommand", "the source's command is dropped; the target runs its own once per deployment")
		p.Warnings = append(p.Warnings, fmt.Sprintf("The post-deploy command of %s is dropped.", m.sc.Service.Slug))
	}
	onTarget := map[uuid.UUID]bool{}
	for _, n := range t.nets {
		onTarget[n.ID] = true
	}
	for _, n := range m.nets {
		if !onTarget[n.ID] {
			add("networks."+n.Name, "the process does not join this shared network; attach it to the target to keep access")
		}
	}
}

func jsonDiffers(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) != string(y)
}

// mergedConfig is the target's configuration with one process per source. The
// sources' commands, ports, health checks and limits become processes; a stop
// grace is only set when it differs from the target's.
func mergedConfig(a *analysis, requestedDefault string) services.Config {
	cfg := a.target.cfg
	if requestedDefault != "" {
		cfg.ProcessName = requestedDefault
	}
	cfg.Processes = nil
	for _, m := range a.sources {
		sp := services.ProcessConfig{
			Name: m.process, StartCommand: slices.Clone(m.cfg.StartCommand), Port: m.cfg.Port, Healthcheck: m.cfg.Healthcheck,
			Singleton: m.cfg.Singleton, MemoryMB: m.cfg.MemoryMB, CPUs: m.cfg.CPUs,
		}
		if m.cfg.StopGraceSeconds != cfg.StopGraceSeconds {
			sp.StopGraceSeconds = m.cfg.StopGraceSeconds
		}
		cfg.Processes = append(cfg.Processes, sp)
	}
	return cfg.WithDefaults()
}

// referencesTo blocks on everything outside the member set that points at a
// source: nothing is rewritten outside the set, the user wires it first.
func (s *Service) referencesTo(ctx context.Context, q *db.Queries, a *analysis, members map[uuid.UUID]bool, block func(string, *member, string) *CombineBlocker, warn func(string, ...any)) error {
	isSource := map[uuid.UUID]*member{}
	for _, m := range a.sources {
		isSource[m.id()] = m
	}
	refs := map[uuid.UUID][]Reference{}
	add := func(src *member, r Reference) { refs[src.id()] = append(refs[src.id()], r) }

	edges, err := services.ReferenceEdges(ctx, q, s.Box, a.env.Env.ID)
	if err != nil {
		return err
	}
	for _, e := range edges {
		if src, ok := isSource[e.To]; ok && !members[e.From] {
			add(src, Reference{FromServiceID: e.From, Kind: "variable", Detail: "variables " + strings.Join(e.Keys, ", ") + " reference " + src.sc.Service.Slug})
		}
		if a.target.id() == e.To && !members[e.From] {
			a.dependents[e.From] = append(a.dependents[e.From], e.Keys...)
		}
	}
	links, err := q.ListEnvironmentLinks(ctx, a.env.Env.ID)
	if err != nil {
		return fmt.Errorf("list links: %w", err)
	}
	lostOut := 0
	for _, l := range links {
		if src, ok := isSource[l.ToServiceID]; ok && !members[l.FromServiceID] {
			add(src, Reference{FromServiceID: l.FromServiceID, Kind: "link", Detail: "a canvas link to " + src.sc.Service.Slug})
		}
		if _, ok := isSource[l.FromServiceID]; ok && !members[l.ToServiceID] {
			lostOut++
		}
	}
	if lostOut > 0 {
		warn("%d canvas link(s) from the sources to other services are removed with them: draw them from the target afterwards.", lostOut)
	}
	envServices, err := q.ListAllEnvironmentServices(ctx, a.env.Env.ID)
	if err != nil {
		return fmt.Errorf("list services: %w", err)
	}
	for _, r := range envServices {
		if r.Kind != services.KindRoute {
			continue
		}
		rc := services.DecodeConfig(r.Config).Route
		if rc == nil {
			continue
		}
		for _, u := range rc.Upstreams {
			if u.ServiceID == nil {
				continue
			}
			if src, ok := isSource[*u.ServiceID]; ok {
				add(src, Reference{FromServiceID: r.ID, Kind: "route", Detail: "the route service " + r.Slug + " forwards to " + src.sc.Service.Slug})
			}
		}
	}
	comps, err := q.ListStatusPageComponents(ctx, db.ListStatusPageComponentsParams{WorkspaceID: a.env.WorkspaceID})
	if err != nil {
		return fmt.Errorf("list status page components: %w", err)
	}
	for _, c := range comps {
		if src, ok := isSource[c.ServiceID]; ok {
			add(src, Reference{FromServiceID: uuid.Nil, Kind: "status_page", Detail: "a status page lists " + src.sc.Service.Slug + " as " + c.DisplayName})
		}
	}
	for _, m := range a.sources {
		if n, err := q.CountServiceMeshState(ctx, m.id()); err != nil {
			return fmt.Errorf("count mesh state: %w", err)
		} else if n > 0 {
			add(m, Reference{FromServiceID: m.id(), Kind: "mesh", Detail: m.sc.Service.Slug + " has a mesh exposure, grant or database replica"})
		}
		if m.cfg.Tailnet != nil {
			add(m, Reference{FromServiceID: m.id(), Kind: "tailnet", Detail: m.sc.Service.Slug + " is exposed on a tailnet"})
		}
	}
	for _, m := range a.sources {
		if rs := refs[m.id()]; len(rs) > 0 {
			b := block(CodeSourceReferenced, m, fmt.Sprintf("%s is still referenced from outside the combine (%d): rewire those first", m.sc.Service.Slug, len(rs)))
			b.References = rs
		}
	}
	return nil
}

// classifyVariables groups the variables of the members. References among the
// members are rewritten to the target before the values are compared, so two
// services that both point at the same sibling agree.
func (s *Service) classifyVariables(a *analysis) {
	rewrite := map[string]string{}
	for _, m := range a.sources {
		rewrite[m.sc.Service.Slug] = a.target.sc.Service.Slug
	}
	apply := func(v string) string {
		for from, to := range rewrite {
			v, _ = varexpr.RewriteRefSlug(v, from, to)
		}
		return v
	}
	for _, m := range a.all {
		keys := make([]string, 0, len(m.vars))
		for k := range m.vars {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			vr := m.vars[k]
			ki := a.keys[k]
			if ki == nil {
				ki = &keyInfo{key: k}
				a.keys[k] = ki
			}
			h := keyHolder{m: m, row: vr, value: apply(vr.value), tmpl: apply(vr.template)}
			ki.holders = append(ki.holders, h)
			var id *identity
			for _, x := range ki.identities {
				if x.holders[0].value == h.value && x.secret == vr.row.IsSecret && x.scope == scopeOf(vr.row) {
					id = x
					break
				}
			}
			if id == nil {
				id = &identity{secret: vr.row.IsSecret, scope: scopeOf(vr.row)}
				ki.identities = append(ki.identities, id)
			}
			id.holders = append(id.holders, h)
		}
	}
	vp := &a.plan.Variables
	vp.OnlyIn, vp.Conflicts = []VariableOnly{}, []VariableConflict{}
	keys := make([]string, 0, len(a.keys))
	for k := range a.keys {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		ki := a.keys[k]
		onTarget := ki.holders[0].m == a.target
		switch {
		case len(ki.identities) > 1:
			c := VariableConflict{Key: k, Secret: slices.ContainsFunc(ki.identities, func(i *identity) bool { return i.secret }), Scope: ki.identities[0].scope}
			for _, id := range ki.identities {
				first := id.holders[0]
				opt := VariableOption{ServiceID: first.m.id(), Slug: first.m.sc.Service.Slug, SameAs: []uuid.UUID{}}
				if !id.secret {
					opt.Preview = preview(id.holders[0].value)
				}
				for _, h := range id.holders[1:] {
					opt.SameAs = append(opt.SameAs, h.m.id())
				}
				c.Options = append(c.Options, opt)
			}
			vp.Conflicts = append(vp.Conflicts, c)
		case !onTarget:
			vp.OnlyIn = append(vp.OnlyIn, VariableOnly{Key: k, ServiceID: ki.holders[0].m.id(), Secret: ki.identities[0].secret})
		case len(ki.holders) > 1:
			vp.SameCount++
		}
	}
}

func preview(v string) string {
	if len(v) <= previewMax {
		return v
	}
	return v[:previewMax] + "…"
}

// fingerprint hashes what the plan rests on: the members' generations, configs,
// domains, tasks, volumes, git links and a keyed fingerprint of every variable
// (never the value), so any change after the review makes the start stale.
func (s *Service) fingerprint(a *analysis) string {
	h := sha256.New()
	w := func(parts ...any) {
		for _, p := range parts {
			fmt.Fprint(h, p)
			h.Write([]byte{0})
		}
	}
	w("target", a.target.id())
	ms := slices.Clone(a.all)
	slices.SortFunc(ms, func(x, y *member) int { return strings.Compare(x.id().String(), y.id().String()) })
	for _, m := range ms {
		sv := m.sc.Service
		w("service", sv.ID, sv.Generation, sv.AppliedGeneration, sv.ServerID, sv.DesiredState, string(sv.Config))
		for _, d := range m.domains {
			w("domain", d.ID, d.Hostname, d.TargetPort, d.Component)
		}
		for _, t := range m.tasks {
			w("task", t.ID, t.Name, t.UpdatedAt.UnixNano())
		}
		for _, v := range m.volumes {
			w("volume", v.ID)
		}
		if m.git != nil {
			w("git", m.git.ConnectionID, m.git.RepoFullName)
		}
		keys := make([]string, 0, len(m.vars))
		for k := range m.vars {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			v := m.vars[k]
			w("var", k, v.row.IsSecret, v.row.Scope, v.row.Source, s.Box.Fingerprint(v.value), s.Box.Fingerprint(v.template))
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}
