package deployments

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"github.com/nebulactrl/nebulactrl/internal/agents"
	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/buildplacement"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/dns01"
	"github.com/nebulactrl/nebulactrl/internal/domains"
	"github.com/nebulactrl/nebulactrl/internal/integrations/cloudflare"
	"github.com/nebulactrl/nebulactrl/internal/jobs"
	"github.com/nebulactrl/nebulactrl/internal/networks"
	"github.com/nebulactrl/nebulactrl/internal/proxyserver"
	"github.com/nebulactrl/nebulactrl/internal/releases"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// DeployArgs is the River job that prepares a queued deployment: it resolves
// the source, writes the immutable release and enqueues the agent command.
type DeployArgs struct {
	DeploymentID uuid.UUID `json:"deploymentId"`
	// CommitSHA pins a git build to this commit instead of resolving the
	// branch head ("Rebuild commit" after an image was pruned).
	CommitSHA string `json:"commitSha,omitempty"`
	// ImageRef deploys this image reference instead of the configured image
	// (deploy hook with ?tag= / ?digest=); it is recorded in the release source.
	ImageRef string `json:"imageRef,omitempty"`
	// WaitFor lists deployments of the same deploy-staged batch (providers of
	// variable references) that must reach a terminal status before this
	// deployment is prepared. The job snoozes while they run and stops waiting
	// after 10 minutes.
	WaitFor []uuid.UUID `json:"waitFor,omitempty"`
	// AfterBuild marks the second job of a deployment built on a build server:
	// the image is pushed and recorded on the release, the target now deploys
	// it. Also used to build on the target after a build server did not take the
	// build (the release then has no registry ref).
	AfterBuild bool `json:"afterBuild,omitempty"`
}

// Kind implements river.JobArgs.
func (DeployArgs) Kind() string { return "deploy_service" }

type deployWorker struct {
	river.WorkerDefaults[DeployArgs]
	s *Service
}

// RegisterJobs registers the deploy worker.
func RegisterJobs(jm *jobs.Manager, s *Service) {
	jm.AddWorker(func(w *river.Workers) { river.AddWorker(w, &deployWorker{s: s}) })
}

func (w *deployWorker) Work(ctx context.Context, job *river.Job[DeployArgs]) error {
	if len(job.Args.WaitFor) > 0 {
		if dc, err := w.s.loadDeploymentInternal(ctx, job.Args.DeploymentID); err == nil && Status(dc.Dep.Status).IsTerminal() {
			return nil // canceled (or failed) while waiting
		}
		pending, perr := w.s.providersPending(ctx, job.Args.WaitFor, job.CreatedAt)
		if perr != nil {
			return perr
		}
		if pending {
			return river.JobSnooze(providerWaitPoll)
		}
	}
	err := w.s.runDeployOpts(ctx, job.Args.DeploymentID, job.Args.CommitSHA, job.Args.ImageRef, job.Args.AfterBuild)
	var wait *buildWaitError
	if errors.As(err, &wait) {
		// The build waits for a slot (or the registry): keep the deployment in
		// preparing and look again shortly. A snooze does not use up an attempt.
		return river.JobSnooze(buildSlotPoll)
	}
	if err != nil && job.Attempt >= job.MaxAttempts {
		w.s.Logger.ErrorContext(ctx, "deployment preparation gave up", "correlation_id", job.Args.DeploymentID.String(), "err", err)
		if ferr := w.s.failDeployment(ctx, job.Args.DeploymentID, "Internal error while preparing the deployment. No changes were made."); ferr != nil {
			return errors.Join(err, ferr)
		}
		return nil
	}
	return err
}

// userError is a problem the user can act on; it fails the deployment instead
// of retrying the job.
type userError struct{ msg string }

func (e *userError) Error() string { return e.msg }

func userErrorf(format string, a ...any) error { return &userError{msg: fmt.Sprintf(format, a...)} }

// prepared is everything runDeploy needs to persist and dispatch.
type prepared struct {
	newRelease *db.InsertReleaseParams // nil when an existing release is redeployed
	releaseID  uuid.UUID
	// rehydrateEnv is the sealed environment given back to an existing release
	// whose own was pruned (rollback outside the retention window).
	rehydrateEnv []byte
	payload      agentprotocol.DeployPayload
	// route is the sync_routes payload of a route-only deployment (replaces payload).
	route      *agentprotocol.SyncRoutesPayload
	strategy   string
	generation int64
	serverID   uuid.UUID
	// commitSHA / commitBranch are the git commit the release pins (empty for
	// images); they are recorded on the deployment.
	commitSHA, commitBranch string
	// abandoned is set when the deployment was canceled before the command was
	// written: nothing is persisted.
	abandoned bool

	// newReleaseID is the id the new release is inserted with (the registry tag
	// r-<release8> must be known before the row exists).
	newReleaseID uuid.UUID
	// fresh is true for a release that has not run yet: a new release, or the
	// deploy of a release right after its build (pre/post-deploy commands and
	// tailnet changes apply; rollbacks and reconcile redeploys skip them).
	fresh bool
	// build, when set, replaces the deploy command: the image is built on a
	// build server first. builder is that server.
	build   *agentprotocol.BuildImagePayload
	builder *db.Server
	// events are placement notices written to the timeline with the dispatch.
	events []string
	// reuse is a live registry image of the same build inputs: no build runs.
	reuse *db.RegistryImage
}

// runDeployOpts prepares and dispatches a deployment. afterBuild resumes a
// deployment whose image a build server pushed (see DeployArgs.AfterBuild). A
// *buildWaitError means the build must wait for a slot: nothing was persisted
// and the caller retries later.
func (s *Service) runDeployOpts(ctx context.Context, depID uuid.UUID, commitSHA, overrideImage string, afterBuild bool) error {
	log := s.Logger.With("correlation_id", depID.String())
	dc, err := s.loadDeploymentInternal(ctx, depID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("load deployment: %w", err)
	}
	log = log.With("service", dc.ServiceSlug)
	switch Status(dc.Dep.Status) {
	case Queued:
		if afterBuild {
			return nil
		}
		moved := false
		if err := db.InTx(ctx, s.Pool, func(tx pgx.Tx) error {
			var err error
			_, moved, err = s.advance(ctx, tx, depID, Preparing, "")
			return err
		}); err != nil {
			return err
		}
		if !moved {
			return nil // canceled before preparation started
		}
		s.publish(dc)
	case Preparing, Building:
		if Status(dc.Dep.Status) == Building && !afterBuild {
			return nil
		}
		// A retry: done if the command was already written (the deploy, or for a
		// build server the build).
		dep := depID
		if _, err := s.q.GetDeployCommandByDeployment(ctx, &dep); err == nil {
			return nil
		}
		if !afterBuild {
			if _, err := s.q.GetBuildCommandByDeployment(ctx, &dep); err == nil {
				return nil
			}
		}
	default:
		return nil // already past preparation, or finished
	}

	var p *prepared
	if !afterBuild {
		// Cross-server links first: the release resolves the providers' mesh ports.
		err = s.meshGate(ctx, dc)
	}
	if err == nil {
		p, err = s.prepare(ctx, dc, commitSHA, overrideImage, afterBuild, log)
	}
	if err == nil {
		err = s.persistAndDispatch(ctx, dc, p)
	}
	var wait *buildWaitError
	if errors.As(err, &wait) {
		if rerr := s.recordPlacementEvent(ctx, depID, wait.message); rerr != nil {
			log.WarnContext(ctx, "record placement event", "err", rerr)
		} else {
			s.publish(dc)
		}
		return wait
	}
	if err == nil && p.abandoned {
		log.InfoContext(ctx, "deployment canceled during preparation")
		return nil
	}
	if err == nil {
		log.InfoContext(ctx, "deployment dispatched", "release_id", p.releaseID.String(), "strategy", p.strategy, "server_id", p.serverID.String())
		return nil
	}
	var ue *userError
	var ae *apierr.Error
	switch {
	case errors.As(err, &ue):
		log.WarnContext(ctx, "deployment failed during preparation", "err", ue.msg)
		return s.failDeployment(ctx, depID, ue.msg)
	case errors.As(err, &ae):
		log.WarnContext(ctx, "deployment failed during preparation", "err", ae.Message)
		return s.failDeployment(ctx, depID, ae.Message)
	}
	log.ErrorContext(ctx, "deployment preparation error", "err", err)
	return err
}

// prepare reads a consistent snapshot of the service and builds the release
// and the deploy payload (no secret values).
func (s *Service) prepare(ctx context.Context, dc depCtx, commitSHA, overrideImage string, afterBuild bool, log *slog.Logger) (*prepared, error) {
	dep := dc.Dep
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, fmt.Errorf("begin snapshot: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	q := db.New(tx)

	sc, err := services.LoadScopeInternal(ctx, q, dep.ServiceID)
	if err != nil {
		if _, ok := apierr.As(err); ok {
			return nil, userErrorf("The service no longer exists.")
		}
		return nil, err
	}
	// A git push of a production service with staged changes deploys the new
	// commit with the configuration and variables of the running release: the
	// staged changes are not shipped and stay staged.
	gatherScope := sc
	var baseline *db.Release
	if dep.ReleaseID == nil && dep.Trigger == TriggerGitPush && sc.ProductionHasStaged() {
		applied, rel, ok, err := services.AppliedScope(ctx, q, sc)
		if err != nil {
			return nil, err
		}
		if ok {
			gatherScope, baseline = applied, &rel
		}
	}
	in, err := s.gather(ctx, q, gatherScope)
	if err != nil {
		return nil, err
	}

	p := &prepared{generation: dep.Generation, fresh: afterBuild}
	var dec *buildDecision
	var (
		cfg    services.Config
		source services.ReleaseSource
		env    services.EnvSnapshot
		srv    db.Server
		// relNets are the shared networks the release joins.
		relNets []services.SnapshotNetwork
	)
	if dep.ReleaseID != nil {
		// Rollback / reconcile: redeploy the stored release as it was.
		rel, err := q.GetReleaseByID(ctx, *dep.ReleaseID)
		if err != nil {
			return nil, fmt.Errorf("get release: %w", err)
		}
		snap := services.DecodeSnapshot(rel.Config)
		cfg, source = snap.Config, services.DecodeSource(rel.Source)
		relNets = snap.Networks
		if len(rel.EnvEnc) > 0 {
			plain, err := s.Box.Open(secretbox.ReleasesEnv, rel.ID.String(), rel.EnvEnc)
			if err != nil {
				return nil, fmt.Errorf("decrypt release env: %w", err)
			}
			err = json.Unmarshal(plain, &env)
			secretbox.Wipe(plain)
			if err != nil {
				return nil, fmt.Errorf("decode release env: %w", err)
			}
		} else {
			// The release is outside the rollback window: its variable values
			// were pruned (internal/releases). Deploying it with an empty
			// environment would start the old image without its configuration, so
			// the current variables are resolved instead (or the rollback fails
			// when they do not resolve), and the timeline says so.
			if env, err = prunedReleaseEnv(in, cfg, source, rel); err != nil {
				return nil, err
			}
			envJSON, err := json.Marshal(env)
			if err != nil {
				return nil, err
			}
			p.rehydrateEnv, err = s.Box.Seal(secretbox.ReleasesEnv, rel.ID.String(), envJSON)
			secretbox.Wipe(envJSON)
			if err != nil {
				return nil, fmt.Errorf("seal release env: %w", err)
			}
			p.events = append(p.events, releases.PrunedNotice)
		}
		if dep.ServerID == nil {
			return nil, userErrorf("The deployment has no server.")
		}
		if srv, err = q.LoadServerRow(ctx, db.LoadServerRowParams{ID: *dep.ServerID}); err != nil {
			return nil, fmt.Errorf("load server: %w", err)
		}
		if srv.Status == "revoked" {
			return nil, userErrorf("server revoked: %s was revoked; assign the service to another server.", srv.Name)
		}
		p.releaseID = rel.ID
		if cfg.Stack != nil {
			// A rollback or reconcile redeploy runs the release's compose file on
			// the server as it is now: Unrestricted Compose and the compose
			// plugin are checked again.
			probs, err := stackProblems(ctx, q, sc, cfg, &srv)
			if err != nil {
				return nil, err
			}
			if len(probs) > 0 {
				return nil, blockingError(probs)
			}
		}
	} else {
		if baseline != nil {
			if err := s.useAppliedRelease(ctx, q, in, *baseline); err != nil {
				return nil, err
			}
		}
		if b := in.blocking(); len(b) > 0 {
			return nil, userErrorf("%s", b[0].Message)
		}
		if msg := in.meshPendingMessage(); msg != "" && s.Mesh != nil {
			return nil, userErrorf("Not deployed: %s. No changes were made.", msg)
		}
		cfg, srv = in.cfg, *in.server
		p.generation = sc.Service.Generation
		if baseline != nil {
			p.generation = sc.Service.AppliedGeneration
		}
		p.fresh, p.newReleaseID = true, uuid.New()
		switch sc.Service.SourceType {
		case services.SourceGit:
			sha, err := commitSHA, error(nil)
			if sha == "" {
				sha, err = s.resolveGit(ctx, sc.Service, cfg)
			}
			if err != nil {
				if errors.Is(err, errGitToken) {
					return nil, err
				}
				return nil, userErrorf("Could not resolve the git source: %s", err.Error())
			}
			source = services.ReleaseSource{Type: services.SourceGit, GitURL: cfg.GitURL, Branch: cfg.GitBranch, CommitSHA: sha}
			if buildsFromGit(sc, cfg) {
				commit := sha
				if dec, err = s.decideBuild(ctx, q, sc, cfg, srv, dep.Trigger == TriggerPreview, func(platform string) (string, bool) {
					return s.buildHash(cfg, commit, in.resolved, platform)
				}); err != nil {
					return nil, err
				}
				if dec.wait != "" {
					return nil, &buildWaitError{message: dec.wait}
				}
				if dep.NoCache {
					dec.reuse = nil // a clean rebuild never reuses an image a build server already pushed
				}
				if dec.onBuildServer() && (dep.NoCache || cfg.Submodules == agentprotocol.SubmodulesOn) && !buildAgentSupports(*dec.builder) {
					return nil, userErrorf("The agent on build server %s is too old for this build (deploy without cache or required submodules); update it first. No changes were made.", dec.builder.Name)
				}
				if dec.onBuildServer() {
					source.RegistryID, source.Platform, source.BuildHash = &dec.info.ID, dec.place.Platform, dec.hash
					if dec.reuse != nil {
						source.RegistryRef = dec.reuse.Repository + "@" + dec.reuse.Digest
						p.reuse = dec.reuse
					}
				}
			}
		case services.SourceExternal:
			source = services.ReleaseSource{Type: services.SourceExternal}
		case services.SourceCompose:
			source = services.ReleaseSource{Type: services.SourceCompose}
		default:
			source = services.ReleaseSource{Type: services.SourceImage, Image: cfg.Image}
			if overrideImage != "" {
				// A deploy hook named an image: the release records it, the
				// service configuration keeps its own image.
				source.Image = overrideImage
			}
		}
		secrets := make([]string, 0, len(in.resolved.Secret))
		for k := range in.resolved.Secret {
			secrets = append(secrets, k)
		}
		sort.Strings(secrets)
		// Release-scoped platform variables that are only known once the
		// source is pinned: frozen into the release, visible to the service itself.
		vars := make(map[string]string, len(in.resolved.Vars)+3)
		for k, v := range in.resolved.Vars {
			vars[k] = v
		}
		vars[services.VarGitCommitSHA], vars[services.VarGitBranch], vars[services.VarImage] = "", "", cfg.Image
		if source.Type == services.SourceImage && overrideImage != "" {
			vars[services.VarImage] = overrideImage
		}
		if source.Type == services.SourceGit {
			vars[services.VarGitCommitSHA], vars[services.VarGitBranch] = source.CommitSHA, source.Branch
			vars[services.VarImage] = agentprotocol.BuildImagePrefix + sc.Service.Slug + ":" + agentprotocol.Short(dep.ID.String())
			if dec.onBuildServer() {
				vars[services.VarImage] = dec.repository + ":" + buildplacement.Tag(p.newReleaseID)
				if source.RegistryRef != "" {
					vars[services.VarImage] = source.RegistryRef
				}
			}
		}
		env = services.EnvSnapshot{Vars: vars, Secret: secrets, Raw: in.raw, Scope: in.resolved.Scope, Leaves: in.resolved.Leaves, Ext: in.resolved.Ext}

		relNets = services.SnapshotNetworks(in.networks)
		snap := services.Snapshot{Config: cfg, ServerID: &srv.ID, EnvironmentID: &sc.EnvironmentID, Networks: relNets}
		domIDs := make([]uuid.UUID, len(in.domains))
		for i, d := range in.domains {
			domIDs[i] = d.ID
		}
		domUsers, err := services.DomainAuthUsernames(ctx, q, domIDs)
		if err != nil {
			return nil, err
		}
		for _, d := range in.domains {
			snap.Domains = append(snap.Domains, services.SnapshotDomainOf(d, domUsers[d.ID]))
		}
		for _, v := range in.volumes {
			snap.Volumes = append(snap.Volumes, services.SnapshotVolume{Name: v.Name, MountPath: v.MountPath, DockerName: v.DockerName, RestoreLabel: v.RestoreLabel})
		}
		srcJSON, err := json.Marshal(source)
		if err != nil {
			return nil, err
		}
		snapJSON, err := json.Marshal(snap)
		if err != nil {
			return nil, err
		}
		envJSON, err := json.Marshal(env)
		if err != nil {
			return nil, err
		}
		envEnc, err := s.Box.Seal(secretbox.ReleasesEnv, p.newReleaseID.String(), envJSON)
		secretbox.Wipe(envJSON)
		if err != nil {
			return nil, fmt.Errorf("seal release env: %w", err)
		}
		imageRef := source.Image
		if source.Type == services.SourceGit {
			imageRef = vars[services.VarImage]
		}
		p.newRelease = &db.InsertReleaseParams{ID: p.newReleaseID, ServiceID: sc.Service.ID, Source: srcJSON, Config: snapJSON, EnvEnc: envEnc, ImageRef: imageRef}
		if dec != nil {
			if dep.NoCache {
				p.events = append(p.events, "Deploying without build cache: the build runs with --no-cache --pull and does not reuse an earlier image.")
			}
			for _, w := range dec.warns {
				if w.Code == buildplacement.WarnFallback {
					p.events = append(p.events, w.Message)
				}
			}
			switch {
			case dec.onBuildServer() && dec.reuse != nil:
				p.events = append(p.events, fmt.Sprintf("Reusing the image already built for commit %s (no build needed).", agentprotocol.Short(source.CommitSHA)))
			case dec.onBuildServer():
				p.events = append(p.events, fmt.Sprintf("Building on %s (%s), pushing to %s.", dec.builder.Name, dec.place.Reason, dec.info.Host))
				p.builder = dec.builder
			}
		}
	}

	p.serverID = srv.ID
	if source.Type == services.SourceGit {
		p.commitSHA, p.commitBranch = source.CommitSHA, source.Branch
	}
	p.strategy, _ = ChoosePlanStrategy(cfg, len(in.volumes))
	if cfg.HasProcesses() {
		// Before anything is persisted: an old agent cannot run the processes, and
		// only an app or worker has them.
		if err := checkProcessDeploy(sc.Service.Kind, cfg, srv); err != nil {
			return nil, err
		}
	}
	if sc.Service.Kind == services.KindRoute {
		// Route-only deployment: no container, the agent just applies the table.
		if cfg.Route == nil {
			return nil, userErrorf("The route service has no route configuration.")
		}
		rp, err := routePayload(ctx, q, sc, dep.ID, *cfg.Route, srv)
		if err != nil {
			return nil, err
		}
		p.route = &rp
		return p, nil
	}

	// Deploys keep the current replica count; a service that is not
	// stateless (volumes, no HTTP port, database) always runs one.
	replicas := services.EffectiveReplicas(sc.Service.Kind, cfg, len(in.volumes), int(sc.Service.Replicas))
	// Route services that target this service must point at the NEW
	// containers in the table the agent applies after the health check.
	over := domains.Overrides{
		sc.Service.ID: {Slug: sc.Service.Slug, DeploymentID: dep.ID.String(), Replicas: replicas, Networks: services.RouteNetworks(sc.EnvironmentID, cfg.BlockEgress)},
	}
	if cfg.Stack != nil {
		over = nil // route services cannot target a stack: its containers are named per component
	}
	routes, err := domains.ServerRoutesWith(ctx, q, srv.ID, sc.Service.ID, over)
	if err != nil {
		return nil, err
	}
	pl := agentprotocol.DeployPayload{
		DeploymentID: dep.ID.String(), ServiceID: sc.Service.ID.String(), ServiceSlug: sc.Service.Slug,
		EnvironmentID: sc.EnvironmentID.String(), ProjectID: sc.ProjectID.String(), Strategy: p.strategy, Port: cfg.Port, StartCommand: cfg.StartCommand,
		Healthcheck: cfg.Healthcheck.Spec(), StopGraceSeconds: cfg.StopGraceSeconds, MemoryMB: cfg.MemoryMB, CPUs: cfg.CPUs,
		ImageRetention: cfg.ImageRetention, Routes: routes, Replicas: replicas, Docker: cfg.Docker,
	}
	// The server's Unrestricted switch can be turned off after a config was
	// saved: a deploy, rollback or reconcile then fails instead of granting
	// host access. The agent re-validates against the policy carried here.
	if err := services.CheckDockerPlacement(cfg, srv.Unrestricted); err != nil {
		return nil, err
	}
	if err := services.CheckDockerFiles(cfg, in.volumes); err != nil {
		return nil, services.ConfigError(err)
	}
	pl.DockerPolicy = &agentprotocol.DockerPolicy{Unrestricted: srv.Unrestricted}
	if pl.Proxy, err = proxyserver.BuildConfig(ctx, q, srv.ID); err != nil {
		return nil, err
	}
	if srv.AcmeEmail != nil {
		pl.ACMEEmail = *srv.AcmeEmail
	}
	traffic := srv.TrafficAnalyticsEnabled
	pl.TrafficAnalytics = &traffic
	for _, v := range in.volumes {
		pl.Volumes = append(pl.Volumes, agentprotocol.VolumeMount{Name: services.DockerVolumeName(v), MountPath: v.MountPath})
	}
	if cfg.Stack == nil && !cfg.HasProcesses() { // a stack's routes are StackSpec.Routes
		pl.DomainRules, pl.Hosts = domains.RulesAndHosts(in.domains)
		if err := dns01.ApplyAutoToRules(ctx, q, srv.ID, pl.DomainRules); err != nil {
			return nil, err
		}
		if pl.OriginLock, err = cloudflare.ApplyOriginLockToRules(ctx, q, srv.ID, pl.DomainRules); err != nil {
			return nil, err
		}
		if err := domains.ApplyPlatformAccessToRules(ctx, q, srv.ID, sc.Service.ID, pl.DomainRules); err != nil {
			return nil, err
		}
	}
	mem, err := networks.Resolve(ctx, q, sc.EnvironmentID, srv.ID, sc.Service.ID, cfg.BlockEgress, relNets)
	if err != nil {
		return nil, err
	}
	pl.BlockEgress, pl.SharedNetworks, pl.EnvHasEgressBlocked = mem.BlockEgress, mem.SharedNetworks, mem.EnvHasEgressBlocked
	tailnetOnly := false
	if s.ServiceTailnet != nil && cfg.Stack == nil {
		// Rollbacks, reconcile redeploys and drains run an existing release and
		// never change the tailnet's configuration (keep).
		spec, hide, err := s.ServiceTailnet.Spec(ctx, q, sc, cfg, srv, !p.fresh)
		if err != nil {
			return nil, err
		}
		pl.Tailnet = spec
		if hide { // "Tailnet only": no public router for this service
			pl.DomainRules, pl.Hosts, pl.OriginLock = nil, nil, nil
			tailnetOnly = true
		}
	}
	if cfg.HasProcesses() {
		// The payload names every process; its top-level runtime fields are the
		// default process' (the agent uses Processes only).
		if err := s.applyProcesses(ctx, q, sc, srv.ID, cfg, len(in.volumes), in.domains, tailnetOnly, replicas, &pl); err != nil {
			return nil, err
		}
	}
	if s.Mesh != nil && cfg.Stack == nil {
		// A service exposed on the mesh carries nc-svc-<id8> on every network, so
		// the server node's serve target keeps working without restarting anyone.
		if pl.MeshAlias, err = s.Mesh.Alias(ctx, q, sc.Service.ID); err != nil {
			return nil, err
		}
	}
	if s.Databases != nil && sc.Service.Kind == services.KindDatabase {
		if pl.Database, err = s.Databases.DatabaseRuntime(ctx, q, sc); err != nil {
			return nil, err
		}
	}
	secretKey := make(map[string]bool, len(env.Secret))
	for _, k := range env.Secret {
		secretKey[k] = true
	}
	for k := range env.Vars {
		switch env.Scope[k] {
		case services.ScopeBuild:
			pl.BuildArgKeys, pl.BuildSecretKeys = classifyBuildVar(pl.BuildArgKeys, pl.BuildSecretKeys, k, secretKey[k])
			continue
		case services.ScopeBoth:
			pl.BuildArgKeys, pl.BuildSecretKeys = classifyBuildVar(pl.BuildArgKeys, pl.BuildSecretKeys, k, secretKey[k])
		}
		pl.EnvKeys = append(pl.EnvKeys, k)
	}
	// Deployment-scoped platform variables are not frozen: a rollback carries
	// the new deployment's id.
	pl.EnvKeys = append(pl.EnvKeys, services.VarDeploymentID, services.VarReleaseID)
	sort.Strings(pl.EnvKeys)
	sort.Strings(pl.BuildArgKeys)
	sort.Strings(pl.BuildSecretKeys)
	for _, k := range env.Secret {
		if env.Scope[k] != services.ScopeBuild {
			pl.SecretEnvKeys = append(pl.SecretEnvKeys, k)
		}
	}
	sort.Strings(pl.SecretEnvKeys)
	switch source.Type {
	case services.SourceGit:
		if source.RegistryRef != "" {
			// Built on a build server (or reused): the target pulls it by digest.
			rid := p.releaseID
			if p.newReleaseID != uuid.Nil {
				rid = p.newReleaseID
			}
			pl.Image = registryImageSource(rid, source)
			break
		}
		g, err := s.gitSource(ctx, sc, cfg, source, source.ImageID == "")
		if err != nil {
			return nil, err
		}
		g.ExistingImageID = source.ImageID
		g.NoCache = dep.NoCache
		pl.Git = g
	case services.SourceCompose:
		if pl.Stack, err = stackSpec(ctx, q, sc, cfg, srv, source, in.domains); err != nil {
			return nil, err
		}
	default:
		pl.Image = &agentprotocol.ImageSource{Ref: source.Image, Digest: source.Digest, HasRegistryAuth: len(sc.Service.RegistryAuthEnc) > 0}
	}
	for _, f := range cfg.Files {
		pl.Files = append(pl.Files, agentprotocol.FileSpec{Path: f.Path, Mode: f.Mode, Content: f.Content})
	}
	pl.OneShot = cfg.OneShot
	// The pre-deploy command (migrations) belongs to a new release; rollbacks,
	// reconcile redeploys and drains re-run an existing release without it.
	if p.fresh && len(cfg.PreDeployCommand) > 0 {
		pl.PreDeploy = &agentprotocol.PreDeploySpec{Command: cfg.PreDeployCommand, TimeoutSeconds: cfg.PreDeployTimeoutSeconds}
	}
	// Likewise the post-deploy command: it follows a new release's swap only.
	if p.fresh && len(cfg.PostDeployCommand) > 0 {
		pl.PostDeploy = &agentprotocol.PostDeploySpec{Command: cfg.PostDeployCommand, TimeoutSeconds: cfg.PostDeployTimeoutSeconds}
	}
	if p.builder != nil {
		g, err := s.gitSource(ctx, sc, cfg, source, true)
		if err != nil {
			return nil, err
		}
		g.NoCache = dep.NoCache
		p.build = &agentprotocol.BuildImagePayload{
			DeploymentID: dep.ID.String(), ServiceID: sc.Service.ID.String(), ProjectID: sc.ProjectID.String(), Git: *g,
			Platform: dec.place.Platform, Repository: dec.repository, Tag: buildplacement.Tag(p.newReleaseID),
			Builder: buildplacement.BuilderName(sc.ProjectID), BuildArgKeys: pl.BuildArgKeys, BuildSecretKeys: pl.BuildSecretKeys,
			Insecure: dec.info.Insecure,
		}
	}
	p.payload = pl
	log.DebugContext(ctx, "deployment prepared", "strategy", p.strategy, "env_keys", len(pl.EnvKeys))
	return p, nil
}

// persistAndDispatch writes the release (new releases only), records it on the
// deployment and enqueues the deploy command, atomically.
func (s *Service) persistAndDispatch(ctx context.Context, dc depCtx, p *prepared) error {
	dep := dc.Dep
	return db.InTx(ctx, s.Pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		cur, err := q.LockDeployment(ctx, dep.ID)
		if err != nil {
			return fmt.Errorf("lock deployment: %w", err)
		}
		if Status(cur.Status).IsTerminal() {
			p.abandoned = true
			return nil
		}
		if p.build != nil {
			// Account the build slot before anything is written: a full build
			// server leaves the deployment waiting in preparing.
			ok, running, err := buildplacement.Slots{}.Acquire(ctx, tx, p.builder.ID, int(p.builder.BuildMaxConcurrent))
			if err != nil {
				return err
			}
			if !ok {
				return &buildWaitError{message: fmt.Sprintf("Waiting for a build slot on %s (%d %s running).", p.builder.Name, running, plural(running, "build", "builds"))}
			}
		}
		releaseID := p.releaseID
		if p.newRelease != nil {
			rel, err := q.InsertRelease(ctx, *p.newRelease)
			if err != nil {
				return fmt.Errorf("insert release: %w", err)
			}
			releaseID = rel.ID
			if err := q.MarkVariableVersionsReleased(ctx, db.MarkVariableVersionsReleasedParams{ServiceID: dep.ServiceID, ReleaseID: &releaseID}); err != nil {
				return fmt.Errorf("mark variable versions: %w", err)
			}
		}
		if p.rehydrateEnv != nil {
			// A rollback to a pruned release: the release now records the
			// environment it is deployed with. 0 rows = already restored.
			if _, err := q.SetReleaseEnvIfPruned(ctx, db.SetReleaseEnvIfPrunedParams{ID: p.releaseID, EnvEnc: p.rehydrateEnv}); err != nil {
				return fmt.Errorf("restore release env: %w", err)
			}
		}
		if err := q.SetDeploymentPrepared(ctx, db.SetDeploymentPreparedParams{
			ID: dep.ID, ReleaseID: &releaseID, Strategy: p.strategy, Generation: p.generation, ServerID: &p.serverID,
		}); err != nil {
			return fmt.Errorf("record release on deployment: %w", err)
		}
		if p.commitSHA != "" {
			if err := q.SetDeploymentCommitSHA(ctx, db.SetDeploymentCommitSHAParams{ID: dep.ID, CommitSha: p.commitSHA, CommitBranch: p.commitBranch}); err != nil {
				return fmt.Errorf("record commit on deployment: %w", err)
			}
		}
		for _, m := range p.events {
			if err := q.InsertDeploymentEvent(ctx, db.InsertDeploymentEventParams{DeploymentID: dep.ID, Step: PlacementStep, Status: PlacementInfo, Message: m}); err != nil {
				return fmt.Errorf("record placement event: %w", err)
			}
		}
		if p.reuse != nil {
			// The new release now holds the image: retention keeps it alive with this release.
			rid, sid := releaseID, dep.ServiceID
			if _, err := q.UpsertRegistryImage(ctx, db.UpsertRegistryImageParams{
				RegistryID: p.reuse.RegistryID, ServiceID: &sid, ReleaseID: &rid, Repository: p.reuse.Repository, Digest: p.reuse.Digest,
				Platform: p.reuse.Platform, BuildHash: p.reuse.BuildHash, SizeBytes: p.reuse.SizeBytes,
			}); err != nil {
				return fmt.Errorf("touch registry image: %w", err)
			}
		}
		if p.build != nil {
			raw, err := json.Marshal(*p.build)
			if err != nil {
				return err
			}
			if err := q.SetDeploymentBuildServer(ctx, db.SetDeploymentBuildServerParams{ID: dep.ID, BuildServerID: &p.builder.ID}); err != nil {
				return fmt.Errorf("record build server: %w", err)
			}
			depID := dep.ID
			if _, err := s.Dispatcher.Enqueue(ctx, tx, agents.NewCommand{
				ServerID: p.builder.ID, DeploymentID: &depID, Kind: agentprotocol.KindBuildImage, Payload: raw,
				IdempotencyKey: "build:" + dep.ID.String(),
			}); err != nil {
				return err
			}
			p.releaseID = releaseID
			return nil
		}
		kind := agentprotocol.KindDeploy
		var raw []byte
		if p.route != nil {
			kind = agentprotocol.KindSyncRoutes
			raw, err = json.Marshal(*p.route)
		} else {
			pl := p.payload
			pl.ReleaseID = releaseID.String()
			raw, err = json.Marshal(pl)
		}
		if err != nil {
			return err
		}
		depID := dep.ID
		if _, err := s.Dispatcher.Enqueue(ctx, tx, agents.NewCommand{
			ServerID: p.serverID, DeploymentID: &depID, Kind: kind, Payload: raw,
			IdempotencyKey: "deploy:" + dep.ID.String(),
		}); err != nil {
			return err
		}
		p.releaseID = releaseID
		return nil
	})
}

// classifyBuildVar files a build-scoped variable under the plain build args or
// the BuildKit secrets.
func classifyBuildVar(args, secrets []string, key string, secret bool) ([]string, []string) {
	if secret {
		return args, append(secrets, key)
	}
	return append(args, key), secrets
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
