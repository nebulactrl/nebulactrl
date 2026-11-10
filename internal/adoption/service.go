package adoption

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/domains"
	"github.com/nebulactrl/nebulactrl/internal/events"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// inventoryTimeout bounds the agent's answer: it inspects every managed container and image.
const inventoryTimeout = 60 * time.Second

// Hub is the part of the agent hub the adoption needs (agents.Hub).
type Hub interface {
	Request(ctx context.Context, serverID uuid.UUID, kind string, payload any, secrets map[string]string, timeout time.Duration) (agentprotocol.ResultFrame, error)
	Resync(ctx context.Context, serverID uuid.UUID) error
}

// Deps are the collaborators of the adoption service.
type Deps struct {
	Pool    *pgxpool.Pool
	Bus     *events.Bus
	Hub     Hub
	Domains *domains.Service
	Box     *secretbox.Box
	Logger  *slog.Logger
}

// Service implements the server adoption use cases.
type Service struct {
	Deps
	q *db.Queries
}

// NewService returns a Service (Pool may be nil for OpenAPI generation).
func NewService(d Deps) *Service {
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	s := &Service{Deps: d}
	if d.Pool != nil {
		s.q = db.New(d.Pool)
	}
	return s
}

// authorize requires servers:write and projects:write on an unrestricted
// principal: the adoption creates projects and reads the variable values of
// the containers on the host.
func authorize(ctx context.Context) error {
	if err := policy.Require(ctx, policy.ScopeServersWrite, nil); err != nil {
		return err
	}
	if err := policy.Require(ctx, policy.ScopeProjectsWrite, nil); err != nil {
		return err
	}
	if p, ok := auth.PrincipalFrom(ctx); ok && p.Restricted() {
		return apierr.Forbidden()
	}
	return nil
}

// Plan asks the agent what runs on the host and reports what adopting it would
// do. Nothing is written and the plan carries no variable values.
func (s *Service) Plan(ctx context.Context, serverID uuid.UUID, req AdoptionPlanRequest) (AdoptionPlan, error) {
	if err := authorize(ctx); err != nil {
		return AdoptionPlan{}, err
	}
	p, _, _, err := s.compute(ctx, serverID, req.Choices)
	if err != nil {
		return AdoptionPlan{}, err
	}
	return *p, nil
}

// hostState is what the control plane knew about the server when a plan was
// computed: the apply checks it again under the server's lock.
type hostState struct {
	held      bool
	current   []uuid.UUID // services this instance runs on the server, sorted
	unmanaged []uuid.UUID // services left running unmanaged, sorted
}

func stateOf(held bool, current, unmanaged []uuid.UUID) hostState {
	sortIDs := func(ids []uuid.UUID) []uuid.UUID {
		out := slices.Clone(ids)
		slices.SortFunc(out, func(a, b uuid.UUID) int { return strings.Compare(a.String(), b.String()) })
		return out
	}
	return hostState{held: held, current: sortIDs(current), unmanaged: sortIDs(unmanaged)}
}

func (h hostState) equal(o hostState) bool {
	return h.held == o.held && slices.Equal(h.current, o.current) && slices.Equal(h.unmanaged, o.unmanaged)
}

// compute loads the server, asks its agent for the inventory and analyses it.
func (s *Service) compute(ctx context.Context, serverID uuid.UUID, ch AdoptionChoices) (*AdoptionPlan, db.Server, hostState, error) {
	var none hostState
	ws := policy.WorkspaceID(ctx)
	srv, err := s.q.LoadServerRow(ctx, db.LoadServerRowParams{ID: serverID, WorkspaceID: &ws})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, srv, none, apierr.NotFound("server")
	}
	if err != nil {
		return nil, srv, none, fmt.Errorf("load server: %w", err)
	}
	if srv.Status == "revoked" {
		return nil, srv, none, apierr.Unprocessable("server_revoked", srv.Name+" has been revoked")
	}
	inv, err := s.inventory(ctx, srv)
	if err != nil {
		return nil, srv, none, err
	}
	cx, err := s.loadContext(ctx, srv, inv)
	if err != nil {
		return nil, srv, none, err
	}
	plan := Analyze(inv, cx, ch)
	if len(plan.Environments) == 0 {
		return nil, srv, none, apierr.Conflict("nothing_to_adopt", "No workloads of an earlier NebulaCtrl are waiting for review on this server.")
	}
	return plan, srv, stateOf(cx.Server.Held, mapIDs(cx.Server.Current), mapIDs(cx.Server.Unmanaged)), nil
}

func mapIDs(m map[uuid.UUID]bool) []uuid.UUID {
	out := make([]uuid.UUID, 0, len(m))
	for id := range m {
		out = append(out, id)
	}
	return out
}

// inventory asks the agent for the managed workloads of the host.
func (s *Service) inventory(ctx context.Context, srv db.Server) (agentprotocol.WorkloadInventory, error) {
	var inv agentprotocol.WorkloadInventory
	var facts agentprotocol.Facts
	if len(srv.Facts) > 0 {
		_ = json.Unmarshal(srv.Facts, &facts)
	}
	if !slices.Contains(facts.Capabilities, agentprotocol.CapAdoption) {
		return inv, apierr.Unprocessable("agent_too_old", "The agent on "+srv.Name+" cannot list its workloads. Upgrade the agent, then review again.")
	}
	res, err := s.Hub.Request(ctx, srv.ID, agentprotocol.KindWorkloadInventory, struct{}{}, nil, inventoryTimeout)
	if err != nil {
		if _, ok := apierr.As(err); ok || ctx.Err() != nil {
			return inv, err
		}
		return inv, apierr.New(http.StatusServiceUnavailable, "server_offline", "the server disconnected before it answered")
	}
	if res.Outcome != agentprotocol.OutcomeSucceeded {
		return inv, apierr.New(http.StatusBadGateway, "inventory_failed", "The agent could not list its workloads: "+res.Error)
	}
	if err := json.Unmarshal(res.Data, &inv); err != nil {
		return inv, apierr.New(http.StatusBadGateway, "inventory_failed", "The agent sent an inventory this control plane cannot read.")
	}
	return inv, nil
}

// loadContext reads what the analysis needs to know about this instance.
func (s *Service) loadContext(ctx context.Context, srv db.Server, inv agentprotocol.WorkloadInventory) (Context, error) {
	ws := policy.WorkspaceID(ctx)
	cx := Context{
		Server: ContextServer{ID: srv.ID, Name: srv.Name, Held: srv.AdoptionState == "held", Unrestricted: srv.Unrestricted,
			Current: map[uuid.UUID]bool{}, Unmanaged: map[uuid.UUID]bool{}},
		Environments: map[uuid.UUID]ContextEnv{}, Services: map[uuid.UUID]ContextService{}, Domains: map[string]bool{},
	}
	if s.Domains != nil {
		domainSrv := srv
		cx.CheckDomain = func(r agentprotocol.DomainRule) error {
			return s.Domains.CheckNewDomain(ctx, ws, &domainSrv, r.Host, ruleInput(r))
		}
	}
	cur, err := s.q.ListServerCurrentServices(ctx, &srv.ID)
	if err != nil {
		return cx, fmt.Errorf("list current services: %w", err)
	}
	for _, c := range cur {
		cx.Server.Current[c.Service.ID] = true
	}
	un, err := s.q.ListServerUnmanagedServices(ctx, srv.ID)
	if err != nil {
		return cx, fmt.Errorf("list unmanaged services: %w", err)
	}
	for _, id := range un {
		cx.Server.Unmanaged[id] = true
	}

	rows, err := s.q.ListProjects(ctx, db.ListProjectsParams{WorkspaceID: ws, Limit: 5000})
	if err != nil {
		return cx, fmt.Errorf("list projects: %w", err)
	}
	projName := map[uuid.UUID]string{}
	for _, p := range rows {
		cp := ContextProject{ID: p.ID, Name: p.Name, Slug: p.Slug}
		envs, err := s.q.ListEnvironments(ctx, p.ID)
		if err != nil {
			return cx, fmt.Errorf("list environments: %w", err)
		}
		for _, e := range envs {
			cp.EnvSlugs = append(cp.EnvSlugs, e.Slug)
		}
		cx.Projects = append(cx.Projects, cp)
		projName[p.ID] = p.Name
	}

	envIDs, svcIDs := map[uuid.UUID]bool{}, map[uuid.UUID]bool{}
	var hosts []string
	for _, c := range inv.Containers {
		if id, err := uuid.Parse(c.Labels[agentprotocol.LabelEnvironment]); err == nil {
			envIDs[id] = true
		}
		if id, err := uuid.Parse(c.Labels[agentprotocol.LabelService]); err == nil {
			svcIDs[id] = true
		}
	}
	for _, r := range inv.Routes {
		if id, err := uuid.Parse(ownerOf(r)); err == nil {
			svcIDs[id] = true
		}
		hosts = append(hosts, r.Hosts...)
	}
	for id := range envIDs {
		e, err := s.q.GetEnvironmentInternal(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return cx, fmt.Errorf("load environment: %w", err)
		}
		ce := ContextEnv{ID: id, ProjectID: e.Environment.ProjectID, ProjectSlug: e.ProjectSlug, Name: e.Environment.Name, Slug: e.Environment.Slug,
			Production: e.Environment.IsProduction, SameTeam: e.WorkspaceID == ws && !e.ProjectSystem, ServiceSlugs: map[string]uuid.UUID{}}
		if ce.SameTeam {
			ce.ProjectName = projName[e.Environment.ProjectID]
			svcs, err := s.q.ListAllEnvironmentServices(ctx, id)
			if err != nil {
				return cx, fmt.Errorf("list services: %w", err)
			}
			for _, sv := range svcs {
				ce.ServiceSlugs[sv.Slug] = sv.ID
			}
		}
		cx.Environments[id] = ce
	}
	if len(svcIDs) > 0 {
		ids := make([]uuid.UUID, 0, len(svcIDs))
		for id := range svcIDs {
			ids = append(ids, id)
		}
		existing, err := s.q.ListServicesForAdoption(ctx, ids)
		if err != nil {
			return cx, fmt.Errorf("list services: %w", err)
		}
		for _, e := range existing {
			cx.Services[e.ID] = ContextService{
				ID: e.ID, EnvironmentID: e.EnvironmentID, SameTeam: e.WorkspaceID == ws, Deleted: e.DeletedAt != nil, CurrentDeploymentID: e.CurrentDeploymentID,
				HostGone: e.DepServerID == nil || e.DepServerStatus == "" || e.DepServerStatus == "revoked",
			}
		}
	}
	if len(hosts) > 0 {
		keys, err := s.q.ListDomainKeysByHostnames(ctx, hosts)
		if err != nil {
			return cx, fmt.Errorf("list domains: %w", err)
		}
		for _, k := range keys {
			cx.Domains[k.Hostname+k.PathPrefix] = true
		}
	}
	return cx, nil
}

// Apply rebuilds the planned services around their running containers: no
// container is touched and no deployment runs. The plan is recomputed from the
// host, so a stale review cannot be applied.
func (s *Service) Apply(ctx context.Context, serverID uuid.UUID, req AdoptionApplyRequest) (AdoptionResult, error) {
	if err := authorize(ctx); err != nil {
		return AdoptionResult{}, err
	}
	if err := policy.Require(ctx, policy.ScopeDeploymentsWrite, nil); err != nil {
		return AdoptionResult{}, err
	}
	plan, srv, state, err := s.compute(ctx, serverID, req.Choices)
	if err != nil {
		return AdoptionResult{}, err
	}
	if len(plan.Blockers) > 0 {
		return AdoptionResult{}, apierr.Unprocessable("adoption_blocked", plan.Blockers[0]).WithDetails(map[string]any{"blockers": plan.Blockers})
	}
	if err := s.guardExisting(ctx, plan, srv); err != nil {
		return AdoptionResult{}, err
	}
	ap := &applier{s: s, ctx: ctx, plan: plan, srv: srv, state: state}
	if err := db.InTx(ctx, s.Pool, ap.run); err != nil {
		if _, ok := apierr.As(err); ok {
			return AdoptionResult{}, err
		}
		if services.IsUniqueViolation(err) {
			return AdoptionResult{}, apierr.Conflict("adoption_conflict", "a project, environment or service was just created by someone else; review the plan and retry")
		}
		return AdoptionResult{}, fmt.Errorf("adopt server: %w", err)
	}
	ap.afterCommit()
	return ap.result(), nil
}

// ---- apply ----

type adopted struct {
	row db.Service
	env services.EnvScope
}

type applier struct {
	s    *Service
	ctx  context.Context
	plan *AdoptionPlan
	srv  db.Server
	// state is what the plan was computed from; run checks it under the server lock.
	state hostState

	// reset on every attempt
	projects   []AdoptionProject
	svcs       []adopted
	left       int
	removed    int
	reattached int
	envs       map[uuid.UUID]services.EnvScope
	afters     []func(context.Context)
	warnings   []string
}

func (ap *applier) run(tx pgx.Tx) error {
	ctx, q := ap.ctx, db.New(tx)
	ap.projects, ap.svcs, ap.warnings, ap.afters, ap.envs = nil, nil, nil, nil, map[uuid.UUID]services.EnvScope{}
	ap.left, ap.removed, ap.reattached = 0, 0, 0
	ws := policy.WorkspaceID(ctx)
	// Serialises concurrent applies of the same server.
	locked, err := q.LockServer(ctx, db.LockServerParams{ID: ap.srv.ID, WorkspaceID: ws})
	if err != nil {
		return fmt.Errorf("lock server: %w", err)
	}
	if err := ap.recheck(ctx, q, locked); err != nil {
		return err
	}

	newProject := map[string]uuid.UUID{} // slug → ID
	var planIDs []uuid.UUID
	for i := range ap.plan.Environments {
		pe := &ap.plan.Environments[i]
		if pe.Action == actCreate || pe.Action == actExisting {
			env, err := ap.environment(tx, q, pe, newProject)
			if err != nil {
				return err
			}
			ap.envs[pe.ID] = env
		}
		for j := range pe.Services {
			ps := &pe.Services[j]
			planIDs = append(planIDs, ps.ID)
			switch ps.Action {
			case ActionAdopt:
				row, err := ap.adopt(tx, q, ap.envs[pe.ID], ps)
				if err != nil {
					return err
				}
				ap.svcs = append(ap.svcs, adopted{row: row, env: ap.envs[pe.ID]})
			case ActionReattach:
				if err := ap.reattach(q, ap.envs[pe.ID], ps); err != nil {
					return err
				}
				ap.reattached++
			case ActionLeave:
				ap.left++
			case ActionRemove:
				ap.removed++
			}
			if ps.Action == ActionLeave {
				if err := q.InsertServerUnmanagedService(ctx, db.InsertServerUnmanagedServiceParams{ServerID: ap.srv.ID, ServiceID: ps.ID}); err != nil {
					return fmt.Errorf("record unmanaged service: %w", err)
				}
				continue
			}
			if err := q.DeleteServerUnmanagedService(ctx, db.DeleteServerUnmanagedServiceParams{ServerID: ap.srv.ID, ServiceID: ps.ID}); err != nil {
				return fmt.Errorf("clear unmanaged service: %w", err)
			}
		}
	}
	// Cleanup: a service left unmanaged by an earlier review that no longer runs on
	// the host (its containers are gone) is not offered again.
	old, err := q.ListServerUnmanagedServices(ctx, ap.srv.ID)
	if err != nil {
		return fmt.Errorf("list unmanaged services: %w", err)
	}
	for _, id := range old {
		if !slices.Contains(planIDs, id) {
			if err := q.DeleteServerUnmanagedService(ctx, db.DeleteServerUnmanagedServiceParams{ServerID: ap.srv.ID, ServiceID: id}); err != nil {
				return fmt.Errorf("clear unmanaged service: %w", err)
			}
		}
	}
	if err := q.ClearServerAdoption(ctx, ap.srv.ID); err != nil {
		return fmt.Errorf("end hold: %w", err)
	}
	return audit.Record(ctx, tx, audit.Entry{
		Action: "server.adopt", ResourceType: "server", ResourceID: ap.srv.ID.String(),
		Metadata: map[string]any{"services": len(ap.svcs), "left": ap.left, "removed": ap.removed, "reattached": ap.reattached, "projects": len(ap.projects)},
	})
}

// environment creates the project and environment of a plan entry that needs
// them (reusing the label ID) and returns its scope.
func (ap *applier) environment(tx pgx.Tx, q *db.Queries, pe *AdoptionEnvironment, newProject map[string]uuid.UUID) (services.EnvScope, error) {
	ctx := ap.ctx
	if pe.Action == actCreate {
		var projectID uuid.UUID
		if pe.ProjectAction == actExisting && pe.ProjectID != nil {
			projectID = *pe.ProjectID
		} else {
			id, ok := newProject[pe.ProjectSlug]
			if !ok {
				row, err := q.CreateProject(ctx, db.CreateProjectParams{WorkspaceID: policy.WorkspaceID(ctx), Name: pe.ProjectName, Slug: pe.ProjectSlug, Description: "Adopted from " + ap.srv.Name})
				if err != nil {
					return services.EnvScope{}, fmt.Errorf("create project: %w", err)
				}
				id = row.ID
				newProject[pe.ProjectSlug] = id
				ap.projects = append(ap.projects, AdoptionProject{ID: row.ID, Name: row.Name, Slug: row.Slug})
				if err := audit.Record(ctx, tx, audit.Entry{
					Action: "project.create", ResourceType: "project", ResourceID: row.ID.String(), ProjectID: &row.ID,
					Metadata: map[string]any{"name": row.Name, "slug": row.Slug, "via": "adoption"},
				}); err != nil {
					return services.EnvScope{}, err
				}
			}
			projectID = id
		}
		row, err := q.CreateEnvironmentWithID(ctx, db.CreateEnvironmentWithIDParams{ID: pe.ID, ProjectID: projectID, Name: pe.Name, Slug: pe.Slug, IsProduction: pe.Production})
		if err != nil {
			return services.EnvScope{}, fmt.Errorf("create environment: %w", err)
		}
		if err := audit.Record(ctx, tx, audit.Entry{
			Action: "environment.create", ResourceType: "environment", ResourceID: row.ID.String(), ProjectID: &projectID,
			Metadata: map[string]any{"name": row.Name, "slug": row.Slug, "via": "adoption"},
		}); err != nil {
			return services.EnvScope{}, err
		}
	}
	return services.LoadEnvironment(ctx, q, policy.ScopeProjectsWrite, pe.ID)
}

// escapeLiteral keeps a container's variable value literal: the resolver would
// read "${{" in a stored value as a reference.
func escapeLiteral(v string) string { return strings.ReplaceAll(v, "${{", "$${{") }

// adopt rebuilds one service, its volumes, variables, domains, release and
// deployment around the running containers, with the IDs found on them.
func (ap *applier) adopt(tx pgx.Tx, q *db.Queries, env services.EnvScope, ps *AdoptionService) (db.Service, error) {
	ctx, rec := ap.ctx, ps.rec
	cfgJSON, err := json.Marshal(rec.cfg)
	if err != nil {
		return db.Service{}, err
	}
	srvID := ap.srv.ID
	row, err := q.InsertService(ctx, db.InsertServiceParams{
		ID: ps.ID, EnvironmentID: env.Env.ID, Name: ps.Name, Slug: ps.Slug, Kind: ps.Kind, SourceType: ps.SourceType, Config: cfgJSON, ServerID: &srvID,
	})
	if err != nil {
		return db.Service{}, fmt.Errorf("insert service %s: %w", ps.Slug, err)
	}
	sc := services.ScopeOf(env, row)

	var vols []db.Volume
	for _, v := range ps.Volumes {
		var vr db.Volume
		if v.DockerName != "" {
			vr, err = q.InsertVolumeWithDockerName(ctx, db.InsertVolumeWithDockerNameParams{ServiceID: row.ID, Name: v.Name, MountPath: v.MountPath, DockerName: v.DockerName})
		} else {
			vr, err = q.InsertVolume(ctx, db.InsertVolumeParams{ServiceID: row.ID, Name: v.Name, MountPath: v.MountPath})
		}
		if err != nil {
			return db.Service{}, fmt.Errorf("insert volume %s.%s: %w", ps.Slug, v.Name, err)
		}
		vols = append(vols, vr)
	}

	raw := map[string]services.RawVar{}
	var secrets []string
	for _, e := range ps.Env {
		if _, err := services.WriteServiceVariable(ctx, q, ap.s.Box, sc, services.VarWrite{
			Key: e.Key, Value: escapeLiteral(e.value), Secret: e.Secret, Scope: services.ScopeRuntime, Source: services.SourceImported,
		}); err != nil {
			return db.Service{}, fmt.Errorf("write variable %s.%s: %w", ps.Slug, e.Key, err)
		}
		raw[e.Key] = services.RawVar{Value: escapeLiteral(e.value), Secret: e.Secret}
		if e.Secret {
			secrets = append(secrets, e.Key)
		}
	}
	sort.Strings(secrets)

	// Domains go through the normal create path, which checks the host name again
	// inside this transaction (the review's check can be stale) and never takes the
	// generated flag from the host.
	var addedDomain bool
	for _, d := range ps.Domains {
		if !d.Include || d.rule == nil {
			continue
		}
		dsc, err := services.LoadScope(ctx, q, policy.ScopeProjectsWrite, row.ID)
		if err != nil {
			return db.Service{}, err
		}
		_, after, err := ap.s.Domains.CreateTx(ctx, tx, dsc, d.Host, d.TargetPort, d.rule.component, ruleInput(d.rule.rule))
		if err != nil {
			return db.Service{}, fmt.Errorf("add domain %s: %w", d.Host, err)
		}
		ap.afters = append(ap.afters, after)
		addedDomain = true
	}
	var snapDomains []services.SnapshotDomain
	if addedDomain {
		doms, err := q.ListServiceDomains(ctx, row.ID)
		if err != nil {
			return db.Service{}, fmt.Errorf("list domains: %w", err)
		}
		for _, dom := range doms {
			snapDomains = append(snapDomains, services.SnapshotDomainOf(dom, nil))
		}
	}

	// Adding a domain bumps the generation: the release and the deployment record the current one.
	cur, err := q.GetServiceScopeInternal(ctx, row.ID)
	if err != nil {
		return db.Service{}, fmt.Errorf("reload service: %w", err)
	}
	row = cur.Service

	snap := services.Snapshot{Config: rec.cfg, ServerID: &srvID, EnvironmentID: &env.Env.ID, Domains: snapDomains}
	for _, v := range vols {
		snap.Volumes = append(snap.Volumes, services.SnapshotVolumeOf(v))
	}
	snapJSON, err := json.Marshal(snap)
	if err != nil {
		return db.Service{}, err
	}
	srcJSON, err := json.Marshal(rec.source)
	if err != nil {
		return db.Service{}, err
	}
	envJSON, err := json.Marshal(services.EnvSnapshot{Vars: rec.releaseVar, Secret: secrets, Raw: raw})
	if err != nil {
		return db.Service{}, err
	}
	envEnc, err := ap.s.Box.Seal(secretbox.ReleasesEnv, ps.ReleaseID.String(), envJSON)
	secretbox.Wipe(envJSON)
	if err != nil {
		return db.Service{}, fmt.Errorf("seal release env: %w", err)
	}
	if _, err := q.InsertRelease(ctx, db.InsertReleaseParams{
		ID: *ps.ReleaseID, ServiceID: row.ID, Source: srcJSON, Config: snapJSON, EnvEnc: envEnc, ImageRef: rec.imageRef,
	}); err != nil {
		return db.Service{}, fmt.Errorf("insert release: %w", err)
	}
	if err := q.MarkVariableVersionsReleased(ctx, db.MarkVariableVersionsReleasedParams{ServiceID: row.ID, ReleaseID: ps.ReleaseID}); err != nil {
		return db.Service{}, fmt.Errorf("mark variable versions: %w", err)
	}
	actorType, actorID := audit.ActorSystem, ""
	if p, ok := auth.PrincipalFrom(ctx); ok {
		actorType, actorID = p.Type, p.ID().String()
	}
	if _, err := q.InsertAdoptedDeployment(ctx, db.InsertAdoptedDeploymentParams{
		ID: *ps.DeploymentID, ServiceID: row.ID, ReleaseID: ps.ReleaseID, ServerID: &srvID, ActorType: actorType, ActorID: actorID,
		Generation: row.Generation, Message: "Adopted from the running containers",
	}); err != nil {
		return db.Service{}, fmt.Errorf("insert deployment: %w", err)
	}
	if err := q.SetServiceReplicas(ctx, db.SetServiceReplicasParams{ID: row.ID, Replicas: int32(ps.Replicas)}); err != nil {
		return db.Service{}, fmt.Errorf("set replicas: %w", err)
	}
	for p, n := range rec.procScale {
		if err := q.UpsertProcessReplicas(ctx, db.UpsertProcessReplicasParams{ServiceID: row.ID, Process: p, Replicas: int32(n)}); err != nil {
			return db.Service{}, fmt.Errorf("set replicas of %s: %w", p, err)
		}
	}
	if err := q.MarkServiceDeployed(ctx, db.MarkServiceDeployedParams{ID: row.ID, CurrentDeploymentID: ps.DeploymentID, AppliedGeneration: row.Generation}); err != nil {
		return db.Service{}, fmt.Errorf("mark deployed: %w", err)
	}
	row.CurrentDeploymentID, row.AppliedGeneration, row.Replicas = ps.DeploymentID, row.Generation, int32(ps.Replicas)
	return row, nil
}

// reattach moves a service this instance already has, with the release the
// containers run, to this server.
func (ap *applier) reattach(q *db.Queries, env services.EnvScope, ps *AdoptionService) error {
	ctx := ap.ctx
	if ps.DeploymentID == nil {
		return errors.New("reattach: no deployment")
	}
	if err := q.ReattachDeploymentServer(ctx, db.ReattachDeploymentServerParams{ID: *ps.DeploymentID, ServerID: &ap.srv.ID}); err != nil {
		return fmt.Errorf("reattach deployment: %w", err)
	}
	if err := q.ReattachServiceServer(ctx, db.ReattachServiceServerParams{ID: ps.ID, ServerID: &ap.srv.ID}); err != nil {
		return fmt.Errorf("reattach service: %w", err)
	}
	// The release the deployment runs names the server it ran on; it runs here now, so the service shows no staged server change.
	if err := q.ReattachReleaseServer(ctx, db.ReattachReleaseServerParams{ServerID: ap.srv.ID.String(), DeploymentID: *ps.DeploymentID}); err != nil {
		return fmt.Errorf("reattach release: %w", err)
	}
	cur, err := q.GetServiceScopeInternal(ctx, ps.ID)
	if err != nil {
		return fmt.Errorf("reload service: %w", err)
	}
	ap.svcs = append(ap.svcs, adopted{row: cur.Service, env: env})
	return nil
}

func (ap *applier) afterCommit() {
	ctx := ap.ctx
	for _, after := range ap.afters {
		after(ctx)
	}
	for _, c := range ap.svcs {
		services.PublishService(ap.s.Bus, services.EventServiceUpdated, services.ScopeOf(c.env, c.row))
	}
	seen := map[uuid.UUID]bool{}
	for _, c := range ap.svcs {
		if !seen[c.env.Env.ID] {
			seen[c.env.Env.ID] = true
			services.Publish(ap.s.Bus, services.EventCanvasUpdated, c.env.Env.ProjectID, c.env.Env.ID, c.env.Env.ID)
		}
	}
	if ap.s.Bus != nil {
		ap.s.Bus.Publish(events.Event{Type: "server.updated", ResourceID: ap.srv.ID.String()})
	}
	// The hold ended: the agent gets the full desired state, which now lists the adopted services.
	if err := ap.s.Hub.Resync(ctx, ap.srv.ID); err != nil {
		ap.s.Logger.WarnContext(ctx, "resync after adoption failed", "server_id", ap.srv.ID, "error", err)
		ap.warnings = append(ap.warnings, "The server will pick up the adoption when its agent next connects.")
	}
}

func (ap *applier) result() AdoptionResult {
	res := AdoptionResult{
		Projects: ap.projects, Services: []AdoptedService{}, Left: ap.left, Removed: ap.removed, Reattached: ap.reattached,
		Warnings: append(append([]string{}, ap.plan.Warnings...), ap.warnings...),
	}
	if res.Projects == nil {
		res.Projects = []AdoptionProject{}
	}
	for _, c := range ap.svcs {
		res.Services = append(res.Services, AdoptedService{
			ID: c.row.ID, Name: c.row.Name, Slug: c.row.Slug, EnvironmentID: c.env.Env.ID, EnvSlug: c.env.Env.Slug, ProjectID: c.env.Env.ProjectID, ProjectSlug: c.env.ProjectSlug,
		})
	}
	return res
}
