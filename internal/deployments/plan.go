package deployments

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/dns01"
	"github.com/nebulactrl/nebulactrl/internal/domains"
	"github.com/nebulactrl/nebulactrl/internal/networks"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Strategy reasons reported in a plan when `recreate` is chosen.
const (
	ReasonHasVolumes = "has volumes"
	ReasonNoHTTPPort = "no http port"
)

// ChooseStrategy picks rolling (new container first) only for services
// without volumes that expose an HTTP port; everything else is recreate. The
// reasons explain a recreate.
func ChooseStrategy(cfg services.Config, volumeCount int) (strategy string, reasons []string) {
	if cfg.Stack != nil {
		return agentprotocol.StrategyRecreate, []string{ReasonStack}
	}
	if cfg.Route != nil {
		return StrategyRouteOnly, []string{"route service: nothing to build or start"}
	}
	if volumeCount > 0 {
		reasons = append(reasons, ReasonHasVolumes)
	}
	if cfg.Port <= 0 {
		reasons = append(reasons, ReasonNoHTTPPort)
	}
	if len(reasons) == 0 {
		return agentprotocol.StrategyRolling, []string{}
	}
	return agentprotocol.StrategyRecreate, reasons
}

// ReasonAllSingletons explains a recreate of a service whose every process is a singleton.
const ReasonAllSingletons = "every process is a singleton"

// ChoosePlanStrategy is the strategy of a deployment as a whole. For a service
// with several processes it is rolling as soon as any process rolls (the others
// stop before they start, one by one), recreate only when none can: volumes, or
// every process a singleton. Without processes it is ChooseStrategy.
func ChoosePlanStrategy(cfg services.Config, volumeCount int) (strategy string, reasons []string) {
	if !cfg.HasProcesses() {
		return ChooseStrategy(cfg, volumeCount)
	}
	for _, p := range cfg.EffectiveProcesses() {
		if services.ProcessStrategy(cfg, volumeCount, p) == agentprotocol.StrategyRolling {
			return agentprotocol.StrategyRolling, []string{}
		}
	}
	if volumeCount > 0 {
		return agentprotocol.StrategyRecreate, []string{ReasonHasVolumes}
	}
	return agentprotocol.StrategyRecreate, []string{ReasonAllSingletons}
}

// inputs is a consistent read of everything a deployment of a service depends on.
type inputs struct {
	sc         services.Scope
	cfg        services.Config
	server     *db.Server
	volumes    []db.Volume
	domains    []db.Domain
	raw        map[string]services.RawVar
	resolved   services.Resolved
	resolveErr error
	// extra holds blocking problems found by gather (route services).
	extra []PlanBlocking
	// tailnetWarnings are the tailnet part's non-blocking concerns.
	tailnetWarnings []string
	// databaseWarnings are the replication part's non-blocking concerns.
	databaseWarnings []string
	// mesh is what reaching providers on other servers needs from the mesh.
	mesh services.MeshPlan
	// networks are the staged shared-network attachments.
	networks []services.NetworkRef
	// egressHints are variables that look like they reach the internet; they
	// matter only while the service blocks outbound internet.
	egressHints []string
	// otherDeployed counts the other services of the environment running on the
	// service's server.
	otherDeployed int64
	// emptySuggested are variables added from .env.example that still have no value.
	emptySuggested []string
}

// gather reads the service's deployable state with q. Reference errors are
// kept in resolveErr so a plan can report them; callers that must fail return them.
func (s *Service) gather(ctx context.Context, q *db.Queries, sc services.Scope) (*inputs, error) {
	in := &inputs{sc: sc, cfg: sc.Config(), raw: map[string]services.RawVar{}}
	if sc.Service.ServerID != nil {
		srv, err := q.LoadServerRow(ctx, db.LoadServerRowParams{ID: *sc.Service.ServerID})
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("load server: %w", err)
		}
		if err == nil {
			in.server = &srv
		}
	}
	var err error
	if in.volumes, err = q.ListServiceVolumes(ctx, sc.Service.ID); err != nil {
		return nil, fmt.Errorf("list volumes: %w", err)
	}
	if in.domains, err = q.ListServiceDomains(ctx, sc.Service.ID); err != nil {
		return nil, fmt.Errorf("list domains: %w", err)
	}
	nets, err := services.ListNetworks(ctx, q, []uuid.UUID{sc.Service.ID})
	if err != nil {
		return nil, err
	}
	in.networks = nets[sc.Service.ID]
	vars, err := q.ListServiceVariables(ctx, sc.Service.ID)
	if err != nil {
		return nil, fmt.Errorf("list variables: %w", err)
	}
	for _, v := range vars {
		val, err := s.Box.OpenString(secretbox.ServiceVariablesValue, secretbox.RowID(sc.Service.ID.String(), v.Key), v.ValueEnc)
		if err != nil {
			return nil, fmt.Errorf("decrypt variable %s: %w", v.Key, err)
		}
		rv := services.RawVar{Value: val, Secret: v.IsSecret}
		if v.Scope != services.ScopeRuntime {
			rv.Scope = v.Scope
		}
		if v.Source == services.SourceGenerated {
			rv.Gen = services.GenDescriptor(v.GenCalls)
		}
		if v.Source == services.SourceSuggested && val == "" {
			in.emptySuggested = append(in.emptySuggested, v.Key)
		}
		in.raw[v.Key] = rv
	}
	res, err := services.LoadResolver(ctx, q, s.Box, sc.EnvInfo())
	if err != nil {
		return nil, err
	}
	in.resolved, in.resolveErr = res.ResolveService(sc.Service.Slug)
	if in.cfg.BlockEgress && sc.Service.Kind != services.KindRoute {
		if in.resolveErr == nil {
			in.egressHints = networks.OutboundHints(in.resolved.Vars)
		}
		if in.server != nil {
			if in.otherDeployed, err = q.CountEnvDeployedServices(ctx, db.CountEnvDeployedServicesParams{
				EnvironmentID: sc.EnvironmentID, ServerID: &in.server.ID, ExcludeServiceID: sc.Service.ID,
			}); err != nil {
				return nil, fmt.Errorf("count environment services: %w", err)
			}
		}
	}
	if sc.Service.Kind == services.KindRoute {
		if in.extra, err = routeProblems(ctx, q, in); err != nil {
			return nil, err
		}
	}
	if sc.Service.Kind == services.KindStack {
		probs, err := stackProblems(ctx, q, sc, in.cfg, in.server)
		if err != nil {
			return nil, err
		}
		in.extra = append(in.extra, probs...)
	}
	if s.ServiceTailnet != nil && sc.Service.Kind != services.KindRoute && sc.Service.Kind != services.KindStack {
		warns, probs, err := s.ServiceTailnet.PlanCheck(ctx, q, sc, in.cfg, in.server)
		if err != nil {
			return nil, err
		}
		in.tailnetWarnings = warns
		for _, p := range probs {
			in.extra = append(in.extra, PlanBlocking{Code: p.Code, Message: p.Message})
		}
	}
	in.mesh = services.EmptyMeshPlan()
	if s.Mesh != nil && sc.Service.Kind != services.KindRoute && sc.Service.Kind != services.KindStack {
		plan, err := s.Mesh.PlanCheck(ctx, q, sc)
		if err != nil {
			return nil, err
		}
		in.mesh = plan
		for _, b := range plan.Blockers {
			in.extra = append(in.extra, PlanBlocking{Code: b.Code, Message: b.Message})
		}
	}
	if s.Databases != nil && sc.Service.Kind == services.KindDatabase {
		warns, probs, err := s.Databases.PlanCheck(ctx, q, sc, in.cfg)
		if err != nil {
			return nil, err
		}
		in.databaseWarnings = warns
		in.extra = append(in.extra, probs...)
	}
	if in.server != nil {
		// The switch can be turned off after a config was saved: the plan says so
		// the same way a deploy, rollback or reconcile will refuse it.
		if err := services.CheckDockerPlacement(in.cfg, in.server.Unrestricted); err != nil {
			in.extra = append(in.extra, PlanBlocking{Code: services.CodeRequiresUnrestricted, Message: err.Error()})
		}
	}
	if err := services.CheckDockerFiles(in.cfg, in.volumes); err != nil {
		in.extra = append(in.extra, PlanBlocking{Code: "invalid_config", Message: strings.TrimPrefix(err.Error(), services.ErrInvalidConfig.Error()+": ")})
	}
	return in, nil
}

// blocking lists the reasons the service cannot be deployed right now.
func (in *inputs) blocking() []PlanBlocking {
	out := []PlanBlocking{}
	switch {
	case in.sc.Service.ServerID == nil || in.server == nil:
		out = append(out, PlanBlocking{Code: "no_server", Message: "Assign the service to a server before deploying."})
	case in.server.Status == "revoked":
		out = append(out, PlanBlocking{Code: "server_revoked", Message: "server revoked: " + in.server.Name + " was revoked; assign the service to another server."})
	}
	if err := in.cfg.Validate(in.sc.Service.SourceType); err != nil {
		out = append(out, PlanBlocking{Code: "invalid_config", Message: strings.TrimPrefix(err.Error(), services.ErrInvalidConfig.Error()+": ")})
	}
	if b := in.buildVarProblems(); b != nil {
		out = append(out, *b)
	}
	if in.resolveErr != nil {
		code, msg := "invalid_variables", in.resolveErr.Error()
		var ae *apierr.Error
		if errors.As(in.resolveErr, &ae) {
			code, msg = ae.Code, ae.Message
		}
		out = append(out, PlanBlocking{Code: code, Message: msg})
	}
	if in.server != nil && in.cfg.Route == nil && usesProxyV2(in.domains) && !domains.SupportsProxyV2(*in.server) {
		out = append(out, PlanBlocking{Code: "agent_too_old", Message: "The agent on " + in.server.Name + " is too old for domain options, paths, redirects and custom certificates; update the agent first."})
	}
	if b := processesNeedAgent(in.cfg, in.server); b != nil {
		out = append(out, *b)
	}
	if in.sc.Service.SourceType == services.SourceGit {
		if b := submodulesNeedAgent(in.cfg, in.server); b != nil {
			out = append(out, *b)
		}
	}
	out = append(out, in.networkProblems()...)
	out = append(out, in.extra...)
	return out
}

// blockingError converts the first blocking problem into an API error.
func blockingError(b []PlanBlocking) error {
	if len(b) == 0 {
		return nil
	}
	e := apierr.Unprocessable(b[0].Code, b[0].Message)
	if len(b[0].Violations) > 0 {
		e = e.WithDetails(map[string]any{"violations": b[0].Violations})
	}
	return e
}

// warnings lists non-blocking concerns about deploying now.
func (in *inputs) warnings(changes []services.FieldChange, strategy string) []string {
	out := []string{}
	if in.cfg.Healthcheck.Type == services.HealthNone && in.cfg.Route == nil && in.cfg.Stack == nil {
		out = append(out, "No health check configured: the container is considered healthy once it has run for 10 seconds without restarting.")
	}
	if in.server != nil {
		switch in.server.Status {
		case "offline":
			out = append(out, fmt.Sprintf("Server %s is offline. The deployment waits for it to reconnect; if it is not delivered within 15 minutes it fails without changes.", in.server.Name))
		case "pending":
			out = append(out, fmt.Sprintf("Server %s has not connected yet. Finish enrolling its agent first.", in.server.Name))
		}
		if len(in.domains) > 0 && in.server.ProxyStatus != nil && *in.server.ProxyStatus == agentprotocol.ProxyError {
			msg := "The proxy on " + in.server.Name + " is not running, so domains will not route"
			if in.server.ProxyError != nil && *in.server.ProxyError != "" {
				msg += ": " + *in.server.ProxyError
			}
			out = append(out, msg+".")
		}
	}
	for _, d := range in.domains {
		if d.DnsOk != nil && !*d.DnsOk {
			out = append(out, fmt.Sprintf("DNS for %s does not point to the server yet; HTTPS certificates cannot be issued until it does.", d.Hostname))
		}
	}
	if strategy == agentprotocol.StrategyRecreate && in.cfg.Stack != nil {
		out = append(out, stackWarnings(in.cfg)...)
	} else if strategy == agentprotocol.StrategyRecreate {
		out = append(out, "The recreate strategy stops the running container before starting the new one: expect brief downtime.")
	}
	for _, c := range changes {
		if strings.HasPrefix(c.Field, "variables.") && (c.From == services.Mask || c.To == services.Mask) {
			out = append(out, "Secret variables changed since the last release.")
			break
		}
	}
	out = append(out, in.tailnetWarnings...)
	out = append(out, in.databaseWarnings...)
	out = append(out, in.meshWarnings()...)
	out = append(out, in.egressWarnings(changes)...)
	out = append(out, emptySuggestedWarnings(in.emptySuggested)...)
	return out
}

// pendingChanges compares the service's current configuration with the
// snapshot of its current release. A never-deployed service reports a single
// "first deployment" change.
func (s *Service) pendingChanges(ctx context.Context, q *db.Queries, in *inputs) ([]services.FieldChange, error) {
	sv := in.sc.Service
	if sv.CurrentDeploymentID == nil {
		return []services.FieldChange{{Field: "deployment", From: "", To: "first deployment"}}, nil
	}
	deps, err := q.ListDeploymentsByIDs(ctx, []uuid.UUID{*sv.CurrentDeploymentID})
	if err != nil || len(deps) != 1 || deps[0].ReleaseID == nil {
		return []services.FieldChange{}, err
	}
	rel, err := q.GetReleaseByID(ctx, *deps[0].ReleaseID)
	if err != nil {
		return nil, fmt.Errorf("get release: %w", err)
	}
	return s.diffAgainstRelease(ctx, q, in, rel)
}

func (s *Service) diffAgainstRelease(ctx context.Context, q *db.Queries, in *inputs, rel db.Release) ([]services.FieldChange, error) {
	return services.DiffRelease(ctx, q, s.Box, rel, in.state())
}

// state is the staged state of the service as read by gather.
func (in *inputs) state() services.CurrentState {
	st := services.CurrentState{Config: in.cfg, SourceType: in.sc.Service.SourceType, ServerID: in.sc.Service.ServerID, Raw: in.raw, Domains: in.domains, Volumes: in.volumes, Networks: in.networks}
	if in.resolveErr == nil {
		r := in.resolved
		st.Resolved = &r
	}
	return st
}

// PendingChanges returns the field-level differences between the service's
// staged configuration and its current release (secrets masked).
func (s *Service) PendingChanges(ctx context.Context, serviceID uuid.UUID) ([]services.FieldChange, error) {
	sc, err := services.LoadScope(ctx, s.q, policy.ScopeProjectsRead, serviceID)
	if err != nil {
		return nil, err
	}
	in, err := s.gather(ctx, s.q, sc)
	if err != nil {
		return nil, err
	}
	changes, err := s.pendingChanges(ctx, s.q, in)
	if err != nil {
		return nil, err
	}
	return append(changes, in.meshChanges()...), nil
}

// Plan reports what deploying the service now would do, without doing it.
func (s *Service) Plan(ctx context.Context, serviceID uuid.UUID) (Plan, error) {
	sc, err := services.LoadScope(ctx, s.q, policy.ScopeProjectsRead, serviceID)
	if err != nil {
		return Plan{}, err
	}
	in, err := s.gather(ctx, s.q, sc)
	if err != nil {
		return Plan{}, err
	}
	strategy, reasons := ChoosePlanStrategy(in.cfg, len(in.volumes))
	changes, err := s.pendingChanges(ctx, s.q, in)
	if err != nil {
		return Plan{}, err
	}
	changes = append(changes, in.meshChanges()...)
	plan := Plan{
		Strategy: strategy, Reasons: reasons, Changes: changes, Blocking: in.blocking(), Mesh: in.mesh,
		Source: PlanSource{Type: sc.Service.SourceType}, Processes: []PlanProcess{},
	}
	if in.cfg.HasProcesses() {
		scales, err := s.q.ListProcessScales(ctx, serviceID)
		if err != nil {
			return Plan{}, fmt.Errorf("list process scales: %w", err)
		}
		running, err := s.runningConfig(ctx, s.q, sc.Service)
		if err != nil {
			return Plan{}, err
		}
		plan.Processes = planProcesses(sc.Service.Kind, in.cfg, running, len(in.volumes), scales, int(sc.Service.Replicas), changes)
	}
	plan.Warnings = in.warnings(changes, strategy)
	plan.Warnings = append(plan.Warnings, in.cfg.Docker.Warnings()...)
	plan.Warnings = append(plan.Warnings, services.HostRequirementWarnings(in.cfg, in.server)...)
	if len(in.domains) > 0 && in.server != nil {
		// A domain that depends on HTTP-01 on a proxy that does not listen on :80.
		certs, err := dns01.DomainCerts(ctx, s.q, in.domains)
		if err != nil {
			return Plan{}, err
		}
		for _, d := range in.domains {
			if w := certs[d.ID].Warning; w != "" {
				plan.Warnings = append(plan.Warnings, w)
			}
		}
	}
	if in.cfg.Route != nil {
		rw, err := s.routeWarnings(ctx, s.q, in)
		if err != nil {
			return Plan{}, err
		}
		plan.Warnings = append(plan.Warnings, rw...)
	}
	switch sc.Service.SourceType {
	case services.SourceImage:
		plan.Source.Image = in.cfg.Image
	case services.SourceGit:
		plan.Source.GitURL, plan.Source.Branch = services.RedactGitURL(in.cfg.GitURL), in.cfg.GitBranch
		if sha, err := s.resolveGit(ctx, sc.Service, in.cfg); err != nil {
			plan.Warnings = append(plan.Warnings, "Could not resolve branch "+in.cfg.GitBranch+": "+err.Error())
		} else {
			plan.Source.CommitSHA = sha
		}
	}
	if in.server != nil && buildsFromGit(sc, in.cfg) {
		if err := s.planPlacement(ctx, &plan, in, sc); err != nil {
			return Plan{}, err
		}
	}
	if in.server != nil {
		plan.Server = &services.ServerRef{ID: in.server.ID, Name: in.server.Name, Status: in.server.Status, LastSeenAt: in.server.LastSeenAt}
	}
	return plan, nil
}

// buildVarProblems checks the build-scoped variables: they need a git source,
// and secret ones need BuildKit on the target host.
func (in *inputs) buildVarProblems() *PlanBlocking {
	if in.resolveErr != nil {
		return nil
	}
	var buildKeys, secretBuild []string
	for k, sc := range in.resolved.Scope {
		if sc == services.ScopeBuild || sc == services.ScopeBoth {
			buildKeys = append(buildKeys, k)
			if in.resolved.Secret[k] {
				secretBuild = append(secretBuild, k)
			}
		}
	}
	if len(buildKeys) == 0 {
		return nil
	}
	sort.Strings(buildKeys)
	sort.Strings(secretBuild)
	if in.sc.Service.SourceType != services.SourceGit {
		return &PlanBlocking{Code: "build_scope_requires_git", Message: "Build-time variables (" + strings.Join(buildKeys, ", ") + ") only apply to git sources; set their scope to runtime."}
	}
	if len(secretBuild) > 0 && in.server != nil && !BuildKitSupported(in.server.Facts) {
		return &PlanBlocking{Code: "buildkit_required", Message: "Secret build variables (" + strings.Join(secretBuild, ", ") + ") need BuildKit (Docker 23 or newer) on " + in.server.Name + "."}
	}
	return nil
}

// BuildKitSupported reports whether the host's Docker uses BuildKit by default
// (Docker 23+). Unknown versions are assumed to support it.
func BuildKitSupported(facts json.RawMessage) bool {
	var f agentprotocol.Facts
	if len(facts) == 0 || json.Unmarshal(facts, &f) != nil || f.DockerVersion == "" {
		return true
	}
	major, _, _ := strings.Cut(strings.TrimPrefix(f.DockerVersion, "v"), ".")
	n, err := strconv.Atoi(major)
	if err != nil {
		return true
	}
	return n >= 23
}
