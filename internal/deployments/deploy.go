package deployments

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"github.com/nebulactrl/nebulactrl/internal/api"
	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/prodguard"
	"github.com/nebulactrl/nebulactrl/internal/releases"
	"github.com/nebulactrl/nebulactrl/internal/services"
)

// deployMaxAttempts bounds River retries of the prepare phase.
const deployMaxAttempts = 5

// actorOf describes the caller for deployments.actor_type / actor_id.
func actorOf(ctx context.Context) (typ, id string) {
	if p, ok := auth.PrincipalFrom(ctx); ok {
		return p.Type, p.ID().String()
	}
	return audit.ActorSystem, ""
}

type createParams struct {
	sc             services.Scope
	trigger        string
	serverID       uuid.UUID
	strategy       string
	generation     int64
	releaseID      *uuid.UUID
	commitSHA      string
	rolledBackFrom *uuid.UUID
	auditAction    string
	auditMeta      map[string]any
	message        string
	// waitFor lists deployments of the same batch that must finish before this
	// one is prepared (references: providers deploy first).
	waitFor []uuid.UUID
	// commit is stored on the deployment (push and pull request triggers).
	commit *CommitInfo
	// imageRef overrides the configured image of an image service for this
	// deployment (a deploy hook with ?tag= or ?digest=).
	imageRef string
	// noCache builds this deployment's git source with --no-cache --pull and
	// never reuses an image a build server already pushed.
	noCache bool
	// byCombine is the combine the deployment belongs to (uuid.Nil: none); see
	// DeployOptions.ByCombine.
	byCombine uuid.UUID
}

// create inserts a deployment, walks it created → queued and schedules its
// DeployJob, all in one transaction. A service can have only one active
// deployment (partial unique index): a second request gets 409.
func (s *Service) create(ctx context.Context, p createParams) (db.Deployment, error) {
	actorType, actorID := actorOf(ctx)
	var dep db.Deployment
	err := db.InTx(ctx, s.Pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		if err := checkCombine(ctx, q, p.sc.Service.ID, p.byCombine); err != nil {
			return err
		}
		var err error
		dep, err = q.InsertDeployment(ctx, db.InsertDeploymentParams{
			ServiceID: p.sc.Service.ID, ReleaseID: p.releaseID, ServerID: &p.serverID, Trigger: p.trigger,
			ActorType: actorType, ActorID: actorID, Strategy: p.strategy, RolledBackFrom: p.rolledBackFrom, Generation: p.generation,
			Message: p.message,
		})
		if err != nil {
			return err
		}
		if p.noCache {
			if err := q.SetDeploymentNoCache(ctx, dep.ID); err != nil {
				return err
			}
		}
		if p.commit != nil {
			if err := q.SetDeploymentCommit(ctx, db.SetDeploymentCommitParams{
				ID: dep.ID, CommitSha: p.commit.SHA, CommitMessage: p.commit.Message, CommitAuthor: p.commit.Author, CommitBranch: p.commit.Branch,
			}); err != nil {
				return err
			}
		}
		if err := q.InsertDeploymentEvent(ctx, db.InsertDeploymentEventParams{DeploymentID: dep.ID, Step: StatusStep, Status: string(Created)}); err != nil {
			return err
		}
		if err := q.SetServiceDesiredState(ctx, db.SetServiceDesiredStateParams{ID: p.sc.Service.ID, DesiredState: services.DesiredRunning}); err != nil {
			return err
		}
		if dep, _, err = s.advance(ctx, tx, dep.ID, Queued, ""); err != nil {
			return err
		}
		if err := s.Jobs.InsertTx(ctx, tx, DeployArgs{DeploymentID: dep.ID, CommitSHA: p.commitSHA, ImageRef: p.imageRef, WaitFor: p.waitFor}, &river.InsertOpts{MaxAttempts: deployMaxAttempts}); err != nil {
			return fmt.Errorf("enqueue deploy job: %w", err)
		}
		meta := map[string]any{"service": p.sc.Service.Slug, "trigger": p.trigger, "strategy": p.strategy, "serverId": p.serverID.String()}
		if p.noCache {
			meta["noCache"] = true
		}
		for k, v := range p.auditMeta {
			meta[k] = v
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: p.auditAction, ResourceType: "deployment", ResourceID: dep.ID.String(), ProjectID: &p.sc.ProjectID,
			Metadata: meta, WorkspaceID: p.sc.WorkspaceID,
		})
	})
	if services.IsUniqueViolation(err) {
		return db.Deployment{}, apierr.Conflict("deployment_in_progress", "a deployment is already in progress for this service")
	}
	if err != nil {
		if _, ok := apierr.As(err); ok {
			return db.Deployment{}, err
		}
		return db.Deployment{}, fmt.Errorf("create deployment: %w", err)
	}
	services.Publish(s.Bus, services.EventDeploymentUpdated, p.sc.ProjectID, p.sc.EnvironmentID, dep.ID)
	services.PublishService(s.Bus, services.EventServiceUpdated, p.sc)
	return dep, nil
}

// checkCombine refuses a deployment of a service that is part of an active
// combine (409 combine_in_progress), except the combine's own deployment of its
// target, which names the combine. A named combine that is not for this service
// grants nothing.
func checkCombine(ctx context.Context, q *db.Queries, serviceID, byCombine uuid.UUID) error {
	if byCombine != uuid.Nil {
		c, err := q.GetCombine(ctx, byCombine)
		if err == nil && c.TargetServiceID == serviceID {
			return nil
		}
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("get combine: %w", err)
		}
	}
	n, err := q.CountActiveCombinesForService(ctx, serviceID)
	if err != nil {
		return fmt.Errorf("count active combines: %w", err)
	}
	if n > 0 {
		return apierr.Conflict("combine_in_progress", "the service is part of a combine in progress; wait for it to finish or abort it first")
	}
	return nil
}

// DeployOptions are the optional inputs of a manual deploy.
type DeployOptions struct {
	// CommitSHA pins a git service's build to that full 40-character commit
	// instead of the branch head ("Rebuild commit" after a release image was
	// pruned); it is rejected for image services.
	CommitSHA string
	Message   string
	// NoCache builds this one deployment without any cache (--no-cache --pull,
	// no reuse of a build server's earlier image). Git services only. Later
	// deployments build normally.
	NoCache bool
	// ByCombine is the id of the combine whose deployment this is. A service in an
	// active combine takes no deployment of its own (409 combine_in_progress); the
	// combine's deployment of its target passes the guard by naming it.
	ByCombine uuid.UUID
}

// Deploy deploys the service's current configuration (202: the deployment
// runs asynchronously). commitSHA, when set, pins a git service's build to
// that full 40-character commit; see DeployOptions.
func (s *Service) Deploy(ctx context.Context, serviceID uuid.UUID, commitSHA, message string) (DeploymentView, error) {
	return s.DeployWithOptions(ctx, serviceID, DeployOptions{CommitSHA: commitSHA, Message: message})
}

// DeployWithOptions is Deploy with the optional inputs of DeployOptions.
func (s *Service) DeployWithOptions(ctx context.Context, serviceID uuid.UUID, o DeployOptions) (DeploymentView, error) {
	message, err := cleanMessage(o.Message)
	if err != nil {
		return DeploymentView{}, err
	}
	commitSHA := o.CommitSHA
	sc, err := services.LoadScope(ctx, s.q, policy.ScopeDeploymentsWrite, serviceID)
	if err != nil {
		return DeploymentView{}, err
	}
	if commitSHA != "" {
		if sc.Service.SourceType != services.SourceGit {
			return DeploymentView{}, apierr.Unprocessable("validation_failed", "commitSha applies to git services only")
		}
		if !fullSHA.MatchString(commitSHA) {
			return DeploymentView{}, apierr.Unprocessable("validation_failed", "commitSha must be a full 40-character hexadecimal commit SHA")
		}
	}
	if o.NoCache && !buildsFromGit(sc, sc.Config()) {
		return DeploymentView{}, apierr.Unprocessable("validation_failed", "noCache applies to services built from a git source only")
	}
	return s.deployScope(ctx, sc, TriggerManual, commitSHA, message, o.NoCache, o.ByCombine)
}

// DeployDrain deploys the service's current configuration to its (new) server
// on behalf of a server drain. commitSHA pins a git service to the commit that
// is running now, so evacuating a host never ships new code.
func (s *Service) DeployDrain(ctx context.Context, serviceID uuid.UUID, commitSHA string) (DeploymentView, error) {
	sc, err := services.LoadScope(ctx, s.q, policy.ScopeDeploymentsWrite, serviceID)
	if err != nil {
		return DeploymentView{}, err
	}
	if sc.Service.SourceType != services.SourceGit || !fullSHA.MatchString(commitSHA) {
		commitSHA = ""
	}
	return s.deployScope(ctx, sc, TriggerDrain, commitSHA, "", false, uuid.Nil)
}

var fullSHA = regexp.MustCompile(`^[0-9a-f]{40}$`)

func (s *Service) deployScope(ctx context.Context, sc services.Scope, trigger, commitSHA, message string, noCache bool, byCombine uuid.UUID) (DeploymentView, error) {
	in, err := s.gather(ctx, s.q, sc)
	if err != nil {
		return DeploymentView{}, err
	}
	if err := blockingError(in.blocking()); err != nil {
		return DeploymentView{}, err
	}
	if noCache && !buildAgentSupports(*in.server) {
		return DeploymentView{}, noCacheAgentError(in.server.Name)
	}
	if trigger == TriggerManual && prodguard.Applies(ctx, sc.IsProduction) {
		if err := prodguard.Check(ctx, deployGuard(in, commitSHA), guardDeploy, prodguard.Digest(guardDeploy, sc.Service.ID, commitSHA, message)); err != nil {
			return DeploymentView{}, err
		}
	}
	strategy, _ := ChoosePlanStrategy(in.cfg, len(in.volumes))
	dep, err := s.create(ctx, createParams{
		sc: sc, trigger: trigger, serverID: in.server.ID, strategy: strategy, generation: sc.Service.Generation,
		auditAction: "deployment.create", commitSHA: commitSHA, message: message, noCache: noCache, byCombine: byCombine,
	})
	if err != nil {
		return DeploymentView{}, err
	}
	return s.oneView(ctx, dep)
}

func (s *Service) oneView(ctx context.Context, dep db.Deployment) (DeploymentView, error) {
	vs, err := s.Views(ctx, []db.Deployment{dep})
	if err != nil {
		return DeploymentView{}, err
	}
	return vs[0], nil
}

// DeployStaged deploys every service of the environment with staged changes
// (generation > applied_generation). With serviceIDs only those services are
// considered. Services start in reference order: a service whose variables
// reference another service of the batch is deployed after it, and its
// DeployJob waits (River snooze) until that provider's deployment is finished.
// Without references databases go first. Services that cannot deploy are
// skipped unless they were named explicitly, in which case nothing is started
// and the first problem is returned.
func (s *Service) DeployStaged(ctx context.Context, envID uuid.UUID, serviceIDs []uuid.UUID, message string) (StagedResult, error) {
	return s.DeployStagedWith(ctx, envID, serviceIDs, message, false)
}

// DeployStagedWith is DeployStaged; with noCache every service of the batch that
// builds from git builds without cache (--no-cache --pull, no reuse of a build
// server's earlier image) for this batch only. Other services deploy as usual.
func (s *Service) DeployStagedWith(ctx context.Context, envID uuid.UUID, serviceIDs []uuid.UUID, message string, noCache bool) (StagedResult, error) {
	message, err := cleanMessage(message)
	if err != nil {
		return StagedResult{}, err
	}
	env, err := services.LoadEnvironment(ctx, s.q, policy.ScopeDeploymentsWrite, envID)
	if err != nil {
		return StagedResult{}, err
	}
	rows, err := s.q.ListAllEnvironmentServices(ctx, envID)
	if err != nil {
		return StagedResult{}, fmt.Errorf("list services: %w", err)
	}
	explicit := len(serviceIDs) > 0
	byID := map[uuid.UUID]bool{}
	for _, r := range rows {
		byID[r.ID] = true
	}
	for _, id := range serviceIDs {
		if !byID[id] {
			return StagedResult{}, apierr.NotFound("service")
		}
	}
	type target = stagedTarget
	// The services of this batch: a route to one of them is not blocked by the
	// target being undeployed, because the target deploys first (the route's
	// deployment waits for it).
	batch := map[uuid.UUID]bool{}
	for _, r := range rows {
		if (!explicit || slices.Contains(serviceIDs, r.ID)) && r.Generation > r.AppliedGeneration {
			batch[r.ID] = true
		}
	}
	var targets []target
	for _, r := range rows {
		if explicit && !slices.Contains(serviceIDs, r.ID) {
			continue
		}
		if r.Generation <= r.AppliedGeneration {
			continue
		}
		sc := services.Scope{
			Service: r, WorkspaceID: env.WorkspaceID, ProjectID: env.Env.ProjectID, EnvironmentID: envID,
			ProjectSlug: env.ProjectSlug, EnvSlug: env.Env.Slug, EnvName: env.Env.Name, IsProduction: env.Env.IsProduction,
		}
		active, err := s.q.CountActiveDeployments(ctx, r.ID)
		if err != nil {
			return StagedResult{}, fmt.Errorf("count active deployments: %w", err)
		}
		if active > 0 {
			if explicit {
				return StagedResult{}, apierr.Conflict("deployment_in_progress", fmt.Sprintf("a deployment is already in progress for %s", r.Slug))
			}
			continue
		}
		in, err := s.gather(ctx, s.q, sc)
		if err != nil {
			return StagedResult{}, err
		}
		if b := withoutBatchUpstreams(in, in.blocking(), batch); len(b) > 0 {
			if explicit {
				return StagedResult{}, blockingError(b)
			}
			continue
		}
		targets = append(targets, target{sc: sc, in: in})
	}
	if noCache {
		for _, t := range targets {
			if buildsFromGit(t.sc, t.in.cfg) && !buildAgentSupports(*t.in.server) {
				return StagedResult{}, noCacheAgentError(t.in.server.Name)
			}
		}
	}
	sort.SliceStable(targets, func(i, j int) bool {
		return targets[i].sc.Service.Kind == services.KindDatabase && targets[j].sc.Service.Kind != services.KindDatabase
	})
	if prodguard.Applies(ctx, env.Env.IsProduction) && len(targets) > 0 {
		tids := make([]uuid.UUID, len(targets))
		for i, t := range targets {
			tids[i] = t.sc.Service.ID
		}
		if err := prodguard.Check(ctx, stagedGuard(env, targets), guardDeployStaged, stagedDigest(envID, tids, message)); err != nil {
			return StagedResult{}, err
		}
	}
	edges, err := services.DeployEdges(ctx, s.q, s.Box, envID)
	if err != nil {
		return StagedResult{}, err
	}
	ids := make([]uuid.UUID, len(targets))
	byTarget := make(map[uuid.UUID]target, len(targets))
	for i, t := range targets {
		ids[i] = t.sc.Service.ID
		byTarget[ids[i]] = t
	}
	order := OrderByReferences(ids, edges)

	res := StagedResult{Items: make([]DeploymentView, 0, len(targets)), Order: make([]uuid.UUID, 0, len(targets))}
	created := map[uuid.UUID]uuid.UUID{} // service id → deployment id
	for _, id := range order {
		t := byTarget[id]
		var waitFor []uuid.UUID
		for _, e := range edges {
			if e.From != id {
				continue
			}
			if depID, ok := created[e.To]; ok {
				waitFor = append(waitFor, depID)
			}
		}
		strategy, _ := ChoosePlanStrategy(t.in.cfg, len(t.in.volumes))
		bust := noCache && buildsFromGit(t.sc, t.in.cfg)
		dep, err := s.create(ctx, createParams{
			sc: t.sc, trigger: TriggerStaged, serverID: t.in.server.ID, strategy: strategy,
			generation: t.sc.Service.Generation, auditAction: "deployment.create", message: message, waitFor: waitFor, noCache: bust,
		})
		if err != nil {
			var ae *apierr.Error
			if errors.As(err, &ae) && (ae.Code == "deployment_in_progress" || ae.Code == "combine_in_progress") && !explicit {
				continue
			}
			return res, err
		}
		created[id] = dep.ID
		v, err := s.oneView(ctx, dep)
		if err != nil {
			return res, err
		}
		res.Items = append(res.Items, v)
		res.Order = append(res.Order, id)
	}
	return res, nil
}

// Rollback redeploys the release of a previous successful deployment. It never
// touches data (volumes, databases). The new deployment records the one that
// was running in rolled_back_from.
func (s *Service) Rollback(ctx context.Context, targetDeploymentID uuid.UUID) (RollbackView, error) {
	dc, err := s.loadDeployment(ctx, policy.ScopeDeploymentsRollback, targetDeploymentID)
	if err != nil {
		return RollbackView{}, err
	}
	target := dc.Dep
	if Status(target.Status) != Succeeded || target.ReleaseID == nil {
		return RollbackView{}, apierr.Unprocessable("not_rollbackable", "only a successful deployment can be rolled back to")
	}
	sc, err := services.LoadScope(ctx, s.q, policy.ScopeDeploymentsRollback, target.ServiceID)
	if err != nil {
		return RollbackView{}, err
	}
	if sc.Service.CurrentDeploymentID != nil && *sc.Service.CurrentDeploymentID == target.ID {
		return RollbackView{}, apierr.Unprocessable("already_current", "that deployment is already the running one")
	}
	rel, err := s.q.GetReleaseByID(ctx, *target.ReleaseID)
	if err != nil {
		return RollbackView{}, fmt.Errorf("get release: %w", err)
	}
	source := services.DecodeSource(rel.Source)
	snap := services.DecodeSnapshot(rel.Config)
	if source.Type == services.SourceGit && source.RegistryRef != "" {
		// Pushed to a registry: the target pulls it by digest; no build server is needed.
		if err := s.checkRegistryImage(ctx, rel, source); err != nil {
			return RollbackView{}, err
		}
	} else if source.Type == services.SourceGit {
		retained, err := s.q.ListNewestSucceededReleaseIDs(ctx, db.ListNewestSucceededReleaseIDsParams{ServiceID: sc.Service.ID, RowLimit: int32(sc.Config().ImageRetention)})
		if err != nil {
			return RollbackView{}, fmt.Errorf("list retained releases: %w", err)
		}
		if source.ImageID == "" || !slices.Contains(retained, rel.ID) {
			return RollbackView{}, apierr.Conflict("image_pruned", "the built image of this release was pruned; rebuild the commit instead").
				WithDetails(map[string]any{"commitSha": source.CommitSHA, "releaseId": rel.ID.String()})
		}
	}
	if sc.Service.ServerID == nil {
		return RollbackView{}, apierr.Unprocessable("no_server", "Assign the service to a server before deploying.")
	}
	srv, err := s.q.LoadServerRow(ctx, db.LoadServerRowParams{ID: *sc.Service.ServerID})
	if err != nil {
		return RollbackView{}, fmt.Errorf("load server: %w", err)
	}
	if srv.Status == "revoked" {
		return RollbackView{}, apierr.Unprocessable("server_revoked", "server revoked: "+srv.Name+" was revoked; assign the service to another server.")
	}
	if b := processesNeedAgent(snap.Config, &srv); b != nil {
		return RollbackView{}, blockingError([]PlanBlocking{*b})
	}
	vols, err := s.q.ListServiceVolumes(ctx, sc.Service.ID)
	if err != nil {
		return RollbackView{}, fmt.Errorf("list volumes: %w", err)
	}
	if prodguard.Applies(ctx, sc.IsProduction) {
		strategy, reasons := ChoosePlanStrategy(snap.Config, len(vols))
		eff := []string{
			"Redeploys the release of an earlier successful deployment of " + sc.Service.Name + " (release " + rel.ID.String()[:8] + ").",
			strategyEffect(strategy, reasons),
			RollbackDisclaimer,
		}
		if err := prodguard.Check(ctx,
			sc.ProdScope(fmt.Sprintf("Roll back %s to an earlier release in production (%s / %s)", sc.Service.Name, sc.ProjectName, sc.EnvName), false, eff...),
			guardRollback, prodguard.Digest(guardRollback, target.ID)); err != nil {
			return RollbackView{}, err
		}
	}
	strategy, _ := ChoosePlanStrategy(snap.Config, len(vols))
	dep, err := s.create(ctx, createParams{
		sc: sc, trigger: TriggerRollback, serverID: srv.ID, strategy: strategy, generation: sc.Service.Generation,
		releaseID: &rel.ID, rolledBackFrom: sc.Service.CurrentDeploymentID, auditAction: "deployment.rollback",
		auditMeta: map[string]any{"targetDeployment": target.ID.String(), "release": rel.ID.String()},
	})
	if err != nil {
		return RollbackView{}, err
	}
	v, err := s.oneView(ctx, dep)
	if err != nil {
		return RollbackView{}, err
	}
	disclaimer := RollbackDisclaimer
	if len(rel.EnvEnc) == 0 {
		disclaimer += " " + releases.PrunedNotice
	}
	return RollbackView{DeploymentView: v, Disclaimer: disclaimer}, nil
}

// Get returns one deployment.
func (s *Service) Get(ctx context.Context, id uuid.UUID) (DeploymentView, error) {
	dc, err := s.loadDeployment(ctx, policy.ScopeProjectsRead, id)
	if err != nil {
		return DeploymentView{}, err
	}
	return s.oneView(ctx, dc.Dep)
}

// List returns a service's deployments, newest first.
func (s *Service) List(ctx context.Context, serviceID uuid.UUID, cursor string, limit int) ([]DeploymentView, string, error) {
	if _, err := services.LoadScope(ctx, s.q, policy.ScopeProjectsRead, serviceID); err != nil {
		return nil, "", err
	}
	params := db.ListServiceDeploymentsPageParams{ServiceID: serviceID, RowLimit: int32(limit + 1)}
	ts, id, ok, err := api.DecodeTimeCursor(cursor)
	if err != nil {
		return nil, "", err
	}
	if ok {
		params.CursorCreatedAt, params.CursorID = &ts, &id
	}
	rows, err := s.q.ListServiceDeploymentsPage(ctx, params)
	if err != nil {
		return nil, "", fmt.Errorf("list deployments: %w", err)
	}
	next := ""
	if len(rows) > limit {
		rows = rows[:limit]
		last := rows[len(rows)-1]
		next = api.EncodeTimeCursor(last.CreatedAt, last.ID)
	}
	views, err := s.Views(ctx, rows)
	return views, next, err
}

// Events returns a deployment's timeline.
func (s *Service) Events(ctx context.Context, id uuid.UUID) ([]EventView, error) {
	if _, err := s.loadDeployment(ctx, policy.ScopeProjectsRead, id); err != nil {
		return nil, err
	}
	rows, err := s.q.ListDeploymentEvents(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("list deployment events: %w", err)
	}
	out := make([]EventView, len(rows))
	for i, r := range rows {
		out[i] = EventView{ID: r.ID, Step: r.Step, Status: r.Status, Message: r.Message, CreatedAt: r.CreatedAt}
	}
	return out, nil
}

// StartRedeploy starts a `reconcile` deployment of an existing release on
// serverID, e.g. after the container went missing. It is for system callers
// (the reconciler) and performs no authorization. Its generation is the
// service's applied generation, so staged changes stay staged.
func (s *Service) StartRedeploy(ctx context.Context, sc services.Scope, releaseID, serverID uuid.UUID) (db.Deployment, error) {
	rel, err := s.q.GetReleaseByID(ctx, releaseID)
	if err != nil {
		return db.Deployment{}, fmt.Errorf("get release: %w", err)
	}
	vols, err := s.q.ListServiceVolumes(ctx, sc.Service.ID)
	if err != nil {
		return db.Deployment{}, fmt.Errorf("list volumes: %w", err)
	}
	strategy, _ := ChoosePlanStrategy(services.DecodeSnapshot(rel.Config).Config, len(vols))
	return s.create(ctx, createParams{
		sc: sc, trigger: TriggerReconcile, serverID: serverID, strategy: strategy, generation: sc.Service.AppliedGeneration,
		releaseID: &rel.ID, auditAction: "deployment.create", auditMeta: map[string]any{"release": rel.ID.String()},
	})
}

// withoutBatchUpstreams drops the upstream_not_deployed problems of a route
// service whose upstream services are all part of the same staged batch: they
// deploy first, and the route's deployment waits for them.
func withoutBatchUpstreams(in *inputs, blocking []PlanBlocking, batch map[uuid.UUID]bool) []PlanBlocking {
	rc := in.cfg.Route
	if rc == nil || in.sc.Service.Kind != services.KindRoute {
		return blocking
	}
	for _, u := range rc.Upstreams {
		if u.ServiceID != nil && !batch[*u.ServiceID] {
			return blocking
		}
	}
	return slices.DeleteFunc(slices.Clone(blocking), func(b PlanBlocking) bool { return b.Code == "upstream_not_deployed" })
}
