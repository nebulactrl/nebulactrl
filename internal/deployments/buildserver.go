package deployments

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/buildplacement"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// PlacementStep is the deployment_events step of placement notices: where the
// build runs, a fallback to the target, a wait for a build slot. They are
// recorded with status PlacementInfo and are not deploy steps.
const (
	PlacementStep = "placement"
	PlacementInfo = "info"
)

// Timings of the build flow.
const (
	// buildSlotPoll is how often a deployment waiting for a build slot retries.
	buildSlotPoll = 10 * time.Second
	// registryCredTTL bounds the credentials minted for one build or pull.
	registryCredTTL = 30 * time.Minute
)

var digestRE = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// RegistryInfo is what the deploy flow needs to know about a registry.
type RegistryInfo struct {
	ID        uuid.UUID
	Host      string // registry.example.com[:port], no scheme
	Namespace string
	// Insecure marks a plain-http registry (loopback hosts only).
	Insecure bool
	// Healthy is false for the built-in registry while the instance service is not ready.
	Healthy       bool
	HealthMessage string
	// ReadOnly is true while the built-in registry is in its garbage-collection
	// window: pushes would fail, so builds wait.
	ReadOnly bool
}

// RegistryProvider is the part of the registries service the deploy flow uses.
// It is implemented by an adapter over internal/registries (wired in app).
type RegistryProvider interface {
	// RegistryInfo describes a registry.
	RegistryInfo(ctx context.Context, id uuid.UUID) (RegistryInfo, error)
	// RegistryCredentials returns base64url(JSON{username,password,serveraddress}),
	// the X-Registry-Auth shape, for the repository (path without host) and
	// actions ("push", "pull"). The built-in registry mints a short-lived credential.
	RegistryCredentials(ctx context.Context, id uuid.UUID, repository string, actions []string, ttl time.Duration) (string, error)
}

// SetRegistries wires the registries. Without it every build falls back to the target.
func (s *Service) SetRegistries(r RegistryProvider) { s.registries = r }

// buildDecision is the outcome of the placement for one new git release.
type buildDecision struct {
	place   buildplacement.Placement
	warns   []buildplacement.Warning
	info    RegistryInfo
	builder *db.Server
	// repository is host/ns/repo (no tag).
	repository string
	hash       string
	// reuse is a live image of the same service, build inputs and platform.
	reuse *db.RegistryImage
	// wait is non-empty when the build must wait (registry in maintenance).
	wait string
}

func (d *buildDecision) onBuildServer() bool {
	return d != nil && d.place.Location == buildplacement.LocationBuildServer
}

// fallback turns a build-server decision into a build on the target.
func (d *buildDecision) fallback(targetName, why string) {
	d.place = buildplacement.Placement{Location: buildplacement.LocationTarget, Platform: d.place.Platform, Fallback: true, Reason: "fallback: " + why}
	d.warns = append(d.warns, buildplacement.Warning{Code: buildplacement.WarnFallback, Message: buildplacement.FallbackMessage(targetName, why)})
	d.info, d.builder, d.repository, d.reuse = RegistryInfo{}, nil, "", nil
}

// placementScope is the placement scope of a service deploying to target.
func placementScope(sc services.Scope, cfg services.Config, target db.Server) buildplacement.Scope {
	return buildplacement.Scope{
		WorkspaceID: sc.WorkspaceID, ProjectID: sc.ProjectID, ServiceID: sc.Service.ID,
		Location: cfg.BuildLocation, BuildServerID: cfg.BuildServerID, RegistryID: cfg.RegistryID, Target: target,
	}
}

// buildsFromGit reports whether the service's deployments build a git source
// that a build server could build.
func buildsFromGit(sc services.Scope, cfg services.Config) bool {
	return sc.Service.SourceType == services.SourceGit && cfg.Stack == nil && cfg.Route == nil
}

// decideBuild resolves where the git source of sc is built. It needs the
// registry provider for a build server; without one the build falls back to the
// target. The reuse lookup needs hash (see buildHash).
func (s *Service) decideBuild(ctx context.Context, q *db.Queries, sc services.Scope, cfg services.Config, target db.Server, preview bool, hash func(platform string) (string, bool)) (*buildDecision, error) {
	place, warns, err := buildplacement.Resolve(ctx, q, placementScope(sc, cfg, target), preview)
	if err != nil {
		return nil, err
	}
	d := &buildDecision{place: place, warns: warns}
	if !d.onBuildServer() {
		return d, nil
	}
	if s.registries == nil {
		d.fallback(target.Name, "registries are not available on this control plane")
		return d, nil
	}
	info, err := s.registries.RegistryInfo(ctx, *place.RegistryID)
	if err != nil {
		return nil, fmt.Errorf("registry info: %w", err)
	}
	if !info.Healthy {
		why := "the registry is not healthy"
		if info.HealthMessage != "" {
			why += ": " + info.HealthMessage
		}
		d.fallback(target.Name, why)
		return d, nil
	}
	builder, err := q.LoadServerRow(ctx, db.LoadServerRowParams{ID: *place.BuildServerID})
	if err != nil {
		return nil, fmt.Errorf("load build server: %w", err)
	}
	d.info, d.builder = info, &builder
	d.place.RegistryHost = info.Host
	d.repository = info.Host + "/" + buildplacement.RepositoryName(info.Namespace, sc.ProjectSlug, sc.Service.Slug)
	if info.ReadOnly {
		d.wait = "Waiting for the registry (maintenance window)"
	}
	if h, ok := hash(place.Platform); ok {
		d.hash = h
		img, err := q.FindReusableRegistryImage(ctx, db.FindReusableRegistryImageParams{
			ServiceID: &sc.Service.ID, BuildHash: h, Platform: place.Platform, RegistryID: info.ID,
		})
		switch {
		case err == nil:
			d.reuse = &img
		case errors.Is(err, pgx.ErrNoRows):
		default:
			return nil, fmt.Errorf("find reusable image: %w", err)
		}
	}
	return d, nil
}

// buildHash identifies the inputs of a build: same hash, same registry and
// platform means the pushed image can be reused. Build variable values are
// included as HMACs (never the values). ok is false when the inputs cannot be
// pinned (a build variable comes from an external secret manager).
func (s *Service) buildHash(cfg services.Config, commit string, res services.Resolved, platform string) (string, bool) {
	if len(res.Ext) > 0 {
		return "", false
	}
	var keys []string
	for k, scope := range res.Scope {
		if scope == services.ScopeBuild || scope == services.ScopeBoth {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	var b strings.Builder
	fmt.Fprintf(&b, "git:%s\npath:%s\nctx:%s\ntype:%s\nplatform:%s\n", commit, cfg.DockerfilePath, cfg.BuildContext, cfg.BuildType, platform)
	if cfg.Submodules != "" { // on/off change the build context; the default keeps older hashes valid
		b.WriteString("submodules:" + cfg.Submodules + "\n")
	}
	for _, k := range keys {
		fmt.Fprintf(&b, "arg:%s=%s secret=%t\n", k, s.Box.Digest(res.Vars[k]), res.Secret[k])
	}
	return s.Box.Digest(b.String()), true
}

// buildWaitError says a deployment must wait before its build can start. The
// job snoozes and retries; the deployment stays in preparing.
type buildWaitError struct {
	message string
}

func (e *buildWaitError) Error() string { return e.message }

// recordPlacementEvent appends a placement notice once: an identical notice
// already on the timeline is not repeated (a waiting job retries every few
// seconds).
func (s *Service) recordPlacementEvent(ctx context.Context, depID uuid.UUID, message string) error {
	rows, err := s.q.ListDeploymentEvents(ctx, depID)
	if err != nil {
		return err
	}
	for _, r := range rows {
		if r.Step == PlacementStep && r.Message == message {
			return nil
		}
	}
	return s.q.InsertDeploymentEvent(ctx, db.InsertDeploymentEventParams{DeploymentID: depID, Step: PlacementStep, Status: PlacementInfo, Message: message})
}

// gitSource builds the GitSource of a payload for source, resolving the SSH key
// or token flags. The build (on the target or on a build server) needs them.
func (s *Service) gitSource(ctx context.Context, sc services.Scope, cfg services.Config, source services.ReleaseSource, withCreds bool) (*agentprotocol.GitSource, error) {
	g := &agentprotocol.GitSource{
		URL: source.GitURL, CommitSHA: source.CommitSHA, DockerfilePath: cfg.DockerfilePath, BuildContext: cfg.BuildContext,
		BuildType: cfg.BuildType, Submodules: cfg.Submodules,
	}
	if !withCreds {
		return g, nil
	}
	creds, err := s.sshCreds(ctx, sc.Service.ID, g.URL)
	if err != nil {
		return nil, userErrorf("%s", s.sshCredsMessage(ctx, sc.Service.ID, err))
	}
	if creds != nil {
		g.HasSSHKey, g.KnownHosts = true, creds.KnownHosts
	} else {
		g.HasToken = s.hasGitAuth(ctx, sc.Service)
	}
	return g, nil
}

// splitRegistryRef splits host/ns/repo@sha256:… into the host, the repository
// path without host and the digest.
func splitRegistryRef(ref string) (host, repo, digest string) {
	name, digest, _ := strings.Cut(ref, "@")
	host, repo, _ = strings.Cut(name, "/")
	return host, repo, digest
}

// registryImageSource is the ImageSource that makes a target pull a release's
// image from its registry by digest.
func registryImageSource(releaseID uuid.UUID, source services.ReleaseSource) *agentprotocol.ImageSource {
	bf := &agentprotocol.BuiltFrom{ReleaseID: releaseID.String(), CommitSHA: source.CommitSHA}
	if source.BuiltOn != nil {
		bf.BuildServerID = source.BuiltOn.String()
	}
	return &agentprotocol.ImageSource{Ref: source.RegistryRef, Digest: source.RegistryRef, HasRegistryAuth: true, BuiltFrom: bf}
}

// secretsForBuild attaches the send-time secrets of a build_image command: the
// git credentials, the build variables and the registry push credential.
func (s *Service) secretsForBuild(ctx context.Context, payload json.RawMessage) (map[string]string, error) {
	var p agentprotocol.BuildImagePayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return nil, fmt.Errorf("decode build_image payload: %w", err)
	}
	depID, err := uuid.Parse(p.DeploymentID)
	if err != nil {
		return nil, fmt.Errorf("build_image payload deployment id: %w", err)
	}
	dep, err := s.q.GetDeploymentScopeInternal(ctx, depID)
	if err != nil {
		return nil, fmt.Errorf("get deployment: %w", err)
	}
	if dep.Deployment.ReleaseID == nil {
		return nil, errors.New("the build's deployment has no release")
	}
	out, err := s.secretsForDeploy(ctx, agentprotocol.DeployPayload{
		DeploymentID: p.DeploymentID, ServiceID: p.ServiceID, ReleaseID: dep.Deployment.ReleaseID.String(),
		BuildArgKeys: p.BuildArgKeys, BuildSecretKeys: p.BuildSecretKeys, Git: &p.Git,
	})
	if err != nil {
		return nil, err
	}
	rel, err := s.q.GetReleaseByID(ctx, *dep.Deployment.ReleaseID)
	if err != nil {
		return nil, fmt.Errorf("get release: %w", err)
	}
	src := services.DecodeSource(rel.Source)
	if src.RegistryID == nil {
		return nil, errors.New("the release has no registry")
	}
	if s.registries == nil {
		return nil, errors.New("registries are not available on this control plane")
	}
	_, repo, _ := strings.Cut(p.Repository, "/")
	auth, err := s.registries.RegistryCredentials(ctx, *src.RegistryID, repo, []string{"push", "pull"}, registryCredTTL)
	if err != nil {
		return nil, fmt.Errorf("registry push credentials: %w", err)
	}
	out[agentprotocol.SecretRegistryPushAuth] = auth
	return out, nil
}

// reporterOwns reports whether the server may report frames (events, results,
// logs) for the deployment: it is the deployment's target, or it owns one of the
// deployment's commands (the build server of a build_image command).
func (s *Service) reporterOwns(ctx context.Context, dep db.Deployment, serverID uuid.UUID) (bool, error) {
	if dep.ServerID != nil && *dep.ServerID == serverID {
		return true, nil
	}
	_, err := s.q.GetDeploymentServiceForReporter(ctx, db.GetDeploymentServiceForReporterParams{ID: dep.ID, ServerID: &serverID})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// handleBuildResult applies the outcome of a build_image command. A successful
// build records the pushed image and enqueues the target's deploy; a failed or
// canceled build ends the deployment.
func (s *Service) handleBuildResult(ctx context.Context, serverID uuid.UUID, cmd db.AgentCommand, dc depCtx, res agentprotocol.ResultFrame) error {
	var br agentprotocol.BuildImageResult
	if len(res.Data) > 0 {
		_ = json.Unmarshal(res.Data, &br)
	}
	log := s.Logger.With("correlation_id", dc.Dep.ID.String(), "service", dc.ServiceSlug, "build_server_id", serverID.String())
	if res.Outcome == agentprotocol.OutcomeSucceeded {
		changed, err := s.buildSucceeded(ctx, serverID, cmd, dc, br)
		if err != nil {
			var ue *userError
			if errors.As(err, &ue) {
				log.WarnContext(ctx, "build result rejected", "err", ue.msg)
				return s.failDeployment(ctx, dc.Dep.ID, ue.msg)
			}
			return fmt.Errorf("handle build result: %w", err)
		}
		if changed {
			log.InfoContext(ctx, "image built and pushed", "digest", br.Digest)
			s.publish(services.EventDeploymentUpdated, dc)
			s.publishService(dc)
		}
		return nil
	}

	target := Failed
	if res.Outcome == agentprotocol.OutcomeCanceled {
		target = Canceled
	}
	msg := br.Message
	if msg == "" {
		msg = res.Error
	}
	if msg == "" {
		msg = "The build failed."
	}
	changed := false
	err := dbInTx(ctx, s, func(tx pgx.Tx) error {
		dep, ch, err := s.advance(ctx, tx, dc.Dep.ID, target, msg)
		if err != nil {
			return err
		}
		changed = ch
		if br.Build != nil {
			if raw, merr := json.Marshal(br.Build); merr == nil {
				if err := db.New(tx).SetDeploymentBuildInfo(ctx, db.SetDeploymentBuildInfoParams{ID: dep.ID, BuildInfo: raw}); err != nil {
					return err
				}
			}
		}
		if ch && target == Canceled && br.CanceledAt != "" {
			if err := db.New(tx).SetDeploymentCanceledStep(ctx, db.SetDeploymentCanceledStepParams{ID: dep.ID, Step: br.CanceledAt}); err != nil {
				return err
			}
			return audit.Record(ctx, tx, audit.Entry{
				Action: "deployment.canceled", ResourceType: "deployment", ResourceID: dep.ID.String(), ProjectID: &dc.ProjectID,
				Metadata: map[string]any{"service": dc.ServiceSlug, "step": br.CanceledAt, "outcome": res.Outcome, "buildServerId": serverID.String()}, WorkspaceID: dc.WorkspaceID,
			})
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("handle build result: %w", err)
	}
	if changed {
		log.InfoContext(ctx, "build finished", "status", string(target), "message", msg)
		s.publish(services.EventDeploymentUpdated, dc)
		s.publishService(dc)
		s.notifyFinished(ctx, dc, target, msg)
	}
	return nil
}

func dbInTx(ctx context.Context, s *Service, fn func(pgx.Tx) error) error {
	return db.InTx(ctx, s.Pool, fn)
}

// buildSucceeded records the pushed image on the release and the registry
// bookkeeping, then enqueues the job that deploys it on the target.
func (s *Service) buildSucceeded(ctx context.Context, serverID uuid.UUID, cmd db.AgentCommand, dc depCtx, br agentprotocol.BuildImageResult) (bool, error) {
	var bp agentprotocol.BuildImagePayload
	if err := json.Unmarshal(cmd.Payload, &bp); err != nil {
		return false, fmt.Errorf("decode build_image payload: %w", err)
	}
	if !digestRE.MatchString(br.Digest) {
		return false, userErrorf("The build server reported an invalid image digest.")
	}
	changed := false
	err := dbInTx(ctx, s, func(tx pgx.Tx) error {
		q := db.New(tx)
		cur, err := q.LockDeployment(ctx, dc.Dep.ID)
		if err != nil {
			return fmt.Errorf("lock deployment: %w", err)
		}
		if Status(cur.Status).IsTerminal() || cur.ReleaseID == nil {
			return nil
		}
		rel, err := q.GetReleaseByID(ctx, *cur.ReleaseID)
		if err != nil {
			return fmt.Errorf("get release: %w", err)
		}
		src := services.DecodeSource(rel.Source)
		if src.RegistryRef != "" {
			return nil // a redelivered result
		}
		if src.RegistryID == nil {
			return userErrorf("The release has no registry to record the image in.")
		}
		src.RegistryRef = bp.Repository + "@" + br.Digest
		src.Platform = bp.Platform
		src.BuiltOn = &serverID
		raw, err := json.Marshal(src)
		if err != nil {
			return err
		}
		if err := q.UpdateReleaseSource(ctx, db.UpdateReleaseSourceParams{ID: rel.ID, Source: raw}); err != nil {
			return err
		}
		var size *int64
		if br.SizeBytes > 0 {
			size = &br.SizeBytes
		}
		svcID, relID := dc.Dep.ServiceID, rel.ID
		if _, err := q.UpsertRegistryImage(ctx, db.UpsertRegistryImageParams{
			RegistryID: *src.RegistryID, ServiceID: &svcID, ReleaseID: &relID, Repository: bp.Repository, Digest: br.Digest,
			Platform: bp.Platform, BuildHash: src.BuildHash, SizeBytes: size,
		}); err != nil {
			return fmt.Errorf("record registry image: %w", err)
		}
		if br.Build != nil {
			if raw, merr := json.Marshal(br.Build); merr == nil {
				if err := q.SetDeploymentBuildInfo(ctx, db.SetDeploymentBuildInfoParams{ID: cur.ID, BuildInfo: raw}); err != nil {
					return err
				}
			}
		}
		msg := fmt.Sprintf("Built on %s; the target pulls %s", s.serverName(ctx, q, serverID), shortRef(src.RegistryRef))
		if err := q.InsertDeploymentEvent(ctx, db.InsertDeploymentEventParams{DeploymentID: cur.ID, Step: PlacementStep, Status: PlacementInfo, Message: msg}); err != nil {
			return err
		}
		if err := s.Jobs.InsertTx(ctx, tx, DeployArgs{DeploymentID: cur.ID, AfterBuild: true}, &river.InsertOpts{MaxAttempts: deployMaxAttempts}); err != nil {
			return fmt.Errorf("enqueue deploy after build: %w", err)
		}
		changed = true
		return nil
	})
	return changed, err
}

func (s *Service) serverName(ctx context.Context, q *db.Queries, id uuid.UUID) string {
	if srv, err := q.LoadServerRow(ctx, db.LoadServerRowParams{ID: id}); err == nil {
		return srv.Name
	}
	return id.String()
}

// shortRef shortens repo@sha256:<64 hex> for messages.
func shortRef(ref string) string {
	name, digest, ok := strings.Cut(ref, "@")
	if !ok || len(digest) < 19 {
		return ref
	}
	return name + "@" + digest[:19]
}

// buildFallbackAfterUndelivered is called when a build_image command expired before its
// build server took it: the deployment builds on the target instead.
func (s *Service) buildFallbackAfterUndelivered(ctx context.Context, cmd db.AgentCommand, dc depCtx) error {
	changed := false
	err := dbInTx(ctx, s, func(tx pgx.Tx) error {
		q := db.New(tx)
		cur, err := q.LockDeployment(ctx, dc.Dep.ID)
		if err != nil {
			return err
		}
		if Status(cur.Status).IsTerminal() {
			return nil
		}
		builder := s.serverName(ctx, q, cmd.ServerID)
		target := "the target"
		if cur.ServerID != nil {
			target = s.serverName(ctx, q, *cur.ServerID)
		}
		if err := q.SetDeploymentBuildServer(ctx, db.SetDeploymentBuildServerParams{ID: cur.ID, BuildServerID: nil}); err != nil {
			return err
		}
		msg := fmt.Sprintf("Build server %s did not take the build; building on %s.", builder, target)
		if err := q.InsertDeploymentEvent(ctx, db.InsertDeploymentEventParams{DeploymentID: cur.ID, Step: PlacementStep, Status: PlacementInfo, Message: msg}); err != nil {
			return err
		}
		if err := s.Jobs.InsertTx(ctx, tx, DeployArgs{DeploymentID: cur.ID, AfterBuild: true}, &river.InsertOpts{MaxAttempts: deployMaxAttempts}); err != nil {
			return err
		}
		changed = true
		return nil
	})
	if err == nil && changed {
		s.publish(services.EventDeploymentUpdated, dc)
	}
	return err
}

// retainRegistryImages is the retention bookkeeping of registry images: the
// images of the newest imageRetention succeeded releases (and the one that just
// deployed) stay live; the others of a registry with managed retention become
// expired, which makes a rollback to them 409 image_pruned and lets the registry's
// sweep delete the digest. Registries without managed retention are left alone.
func (s *Service) retainRegistryImages(ctx context.Context, q *db.Queries, sc services.Scope, releaseID uuid.UUID) error {
	keep, err := q.ListNewestSucceededReleaseIDs(ctx, db.ListNewestSucceededReleaseIDsParams{ServiceID: sc.Service.ID, RowLimit: int32(sc.Config().ImageRetention)})
	if err != nil {
		return fmt.Errorf("list retained releases: %w", err)
	}
	keep = append(keep, releaseID)
	svc := sc.Service.ID
	if _, err := q.ExpireServiceRegistryImages(ctx, db.ExpireServiceRegistryImagesParams{ServiceID: &svc, KeepReleaseIds: keep}); err != nil {
		return fmt.Errorf("expire registry images: %w", err)
	}
	return nil
}

// checkRegistryImage fails with 409 image_pruned when the registry image of a
// release is gone: retention expired it, its registry was deleted, or it was never recorded.
func (s *Service) checkRegistryImage(ctx context.Context, rel db.Release, source services.ReleaseSource) error {
	gone := func() error {
		return apierr.Conflict("image_pruned", "the built image of this release is no longer in its registry; rebuild the commit instead").
			WithDetails(map[string]any{"commitSha": source.CommitSHA, "releaseId": rel.ID.String()})
	}
	if source.RegistryID == nil {
		return gone()
	}
	_, _, digest := splitRegistryRef(source.RegistryRef)
	repo, _, _ := strings.Cut(source.RegistryRef, "@")
	img, err := s.q.GetRegistryImageByRef(ctx, db.GetRegistryImageByRefParams{RegistryID: *source.RegistryID, Repository: repo, Digest: digest})
	if errors.Is(err, pgx.ErrNoRows) {
		return gone()
	}
	if err != nil {
		return fmt.Errorf("get registry image: %w", err)
	}
	if img.State != "live" {
		return gone()
	}
	return nil
}

// planPlacement fills the plan's placement: where the build runs and why, the
// warnings (fallback, emulation, cold cache, queue) and whether an image is reused.
func (s *Service) planPlacement(ctx context.Context, plan *Plan, in *inputs, sc services.Scope) error {
	hash := func(platform string) (string, bool) {
		if in.resolveErr != nil || plan.Source.CommitSHA == "" {
			return "", false
		}
		return s.buildHash(in.cfg, plan.Source.CommitSHA, in.resolved, platform)
	}
	d, err := s.decideBuild(ctx, s.q, sc, in.cfg, *in.server, false, hash)
	if err != nil {
		return err
	}
	v := &PlacementView{
		Location: d.place.Location, BuildServerID: d.place.BuildServerID, BuildServerName: d.place.BuildServerName,
		RegistryID: d.place.RegistryID, RegistryHost: d.place.RegistryHost, Platform: d.place.Platform, Reason: d.place.Reason,
		Sticky: d.place.Sticky, Emulated: d.place.Emulated, Fallback: d.place.Fallback, Reuse: d.reuse != nil,
		MaxConcurrent: d.place.MaxConcurrent, Warnings: d.warns,
	}
	if v.Warnings == nil {
		v.Warnings = []buildplacement.Warning{}
	}
	if d.onBuildServer() {
		n, err := s.q.CountServerActiveBuilds(ctx, d.builder.ID)
		if err != nil {
			return fmt.Errorf("count active builds: %w", err)
		}
		v.ActiveBuilds = int(n)
		if d.reuse != nil {
			plan.Warnings = append(plan.Warnings, fmt.Sprintf("An image of this commit and build settings already exists in %s; no build is needed.", d.info.Host))
		} else if int(n) >= d.place.MaxConcurrent {
			plan.Warnings = append(plan.Warnings, fmt.Sprintf("%s is running %d of its %d builds; this build waits for a free slot.", d.builder.Name, n, d.place.MaxConcurrent))
		}
		if d.wait != "" {
			plan.Warnings = append(plan.Warnings, d.wait+".")
		}
	}
	for _, w := range d.warns {
		plan.Warnings = append(plan.Warnings, w.Message)
	}
	plan.Placement = v
	return nil
}

// buildHost is the server where the service's git source is built (and where a
// build plan or an env scan runs): the build server of the resolved placement,
// else the target. Registries that are not wired or not healthy fall back to the target.
func (s *Service) buildHost(ctx context.Context, q *db.Queries, sc services.Scope, cfg services.Config, target db.Server) (db.Server, error) {
	d, err := s.decideBuild(ctx, q, sc, cfg, target, false, func(string) (string, bool) { return "", false })
	if err != nil {
		return db.Server{}, err
	}
	if d.onBuildServer() {
		return *d.builder, nil
	}
	return target, nil
}
