package deployments

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/agents"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

var (
	_ agents.SecretSource = (*Service)(nil)
)

// SecretsForCommand attaches the send-time secrets of a command, never
// persisted: for deploy and scale_service the release's resolved environment
// as `env:<KEY>`, the git token, the registry credentials and the proxy
// secrets (basic-auth hashes `auth:<domainId>` of every route in the payload,
// certificate keys `cert:<id>`, the tailnet auth key); for sync_routes and
// proxy_configure the proxy secrets only. Other kinds carry no secrets.
func (s *Service) SecretsForCommand(ctx context.Context, id uuid.UUID, kind string, payload json.RawMessage) (map[string]string, error) {
	var p agentprotocol.DeployPayload
	switch kind {
	case agentprotocol.KindDeploy:
		if err := json.Unmarshal(payload, &p); err != nil {
			return nil, fmt.Errorf("decode deploy payload: %w", err)
		}
	case agentprotocol.KindScaleService:
		// New replicas are created from the current deployment's payload, so
		// they need the same environment and credentials as the deploy.
		var sp agentprotocol.ScaleServicePayload
		if err := json.Unmarshal(payload, &sp); err != nil {
			return nil, fmt.Errorf("decode scale_service payload: %w", err)
		}
		p = sp.Deploy
	case agentprotocol.KindBuildImage:
		return s.secretsForBuild(ctx, payload)
	case agentprotocol.KindBuildPlan:
		return s.buildPlanSecrets(ctx, payload)
	case agentprotocol.KindEnvScan:
		return s.envScanSecrets(ctx, payload)
	case agentprotocol.KindSyncRoutes, agentprotocol.KindProxyConfigure:
		return s.secretsForProxyCommand(ctx, id, kind, payload)
	default:
		return nil, nil
	}
	out, err := s.secretsForDeploy(ctx, p)
	if err != nil {
		return nil, err
	}
	serverID := uuid.Nil
	// The server is needed to read the proxy's DNS-01 credentials (a credential is only
	// readable for a server of its team) and to mint a tailnet key.
	if p.Proxy != nil && ((p.Proxy.Tailnet != nil && p.Proxy.Tailnet.Provision) || len(p.Proxy.DNSResolvers) > 0) {
		cmd, err := s.q.GetAgentCommand(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("get command: %w", err)
		}
		serverID = cmd.ServerID
	}
	tables := [][]agentprotocol.Route{p.Routes}
	if p.Stack != nil {
		tables = append(tables, p.Stack.Routes) // basic-auth hashes of the stack's own domains
	}
	px, err := s.proxySecrets(ctx, serverID, payloadRules(p), tables, p.Proxy)
	if err != nil {
		return nil, err
	}
	for k, v := range px {
		out[k] = v
	}
	// The service's own tailnet node (not the per-server egress node above): its
	// single-use key is minted now, only for deploy commands, and never stored.
	if kind == agentprotocol.KindDeploy && p.Tailnet != nil && p.Tailnet.Provision {
		if s.ServiceTailnet == nil {
			return nil, fmt.Errorf("the service tailnet is not available on this control plane")
		}
		svcID, err := uuid.Parse(p.ServiceID)
		if err != nil {
			return nil, fmt.Errorf("deploy payload service id: %w", err)
		}
		ts, err := s.ServiceTailnet.Secrets(ctx, svcID, *p.Tailnet)
		if err != nil {
			return nil, err
		}
		for k, v := range ts {
			out[k] = v
		}
	}
	return out, nil
}

// secretsForDeploy resolves the secrets a deploy payload refers to.
func (s *Service) secretsForDeploy(ctx context.Context, p agentprotocol.DeployPayload) (map[string]string, error) {
	relID, err := uuid.Parse(p.ReleaseID)
	if err != nil {
		return nil, fmt.Errorf("deploy payload release id: %w", err)
	}
	rel, err := s.q.GetReleaseByID(ctx, relID)
	if err != nil {
		return nil, fmt.Errorf("get release: %w", err)
	}
	out := map[string]string{}
	if len(rel.EnvEnc) > 0 {
		plain, err := s.Box.Open(secretbox.ReleasesEnv, rel.ID.String(), rel.EnvEnc)
		if err != nil {
			return nil, fmt.Errorf("decrypt release env: %w", err)
		}
		var env services.EnvSnapshot
		err = json.Unmarshal(plain, &env)
		secretbox.Wipe(plain)
		if err != nil {
			return nil, fmt.Errorf("decode release env: %w", err)
		}
		// External secret manager values are fetched now, held in memory for
		// this frame and masked in logs; they are never stored.
		var fetcher services.ExtFetcher
		extSvc := uuid.Nil
		if len(env.Ext) > 0 {
			if extSvc, err = uuid.Parse(p.ServiceID); err != nil {
				return nil, fmt.Errorf("deploy payload service id: %w", err)
			}
			sc, err := services.LoadScopeInternal(ctx, s.q, extSvc)
			if err != nil {
				return nil, fmt.Errorf("load service: %w", err)
			}
			xs := sc.ExtScope(services.ExtPurposeDeploy)
			xs.DeploymentID, xs.StartedBy = p.DeploymentID, s.startedBy(ctx, p.DeploymentID)
			fetcher = s.ExtSecrets.Fetcher(xs)
		}
		vars, leaves, err := env.Materialize(ctx, fetcher)
		if err != nil {
			return nil, err
		}
		s.ExtSecrets.Remember(extSvc, leaves)
		for _, k := range p.EnvKeys {
			if v, ok := vars[k]; ok {
				out[agentprotocol.SecretEnvPrefix+k] = v
			}
		}
		for _, k := range p.BuildArgKeys {
			out[agentprotocol.SecretBuildPrefix+k] = vars[k]
		}
		for _, k := range p.BuildSecretKeys {
			out[agentprotocol.SecretBuildPrefix+k] = vars[k]
		}
	}
	// Deployment-scoped platform variables (not frozen in the release); only
	// when the payload asks for them.
	for _, k := range p.EnvKeys {
		switch k {
		case services.VarDeploymentID:
			out[agentprotocol.SecretEnvPrefix+k] = p.DeploymentID
		case services.VarReleaseID:
			out[agentprotocol.SecretEnvPrefix+k] = p.ReleaseID
		}
	}
	needGit := p.Git != nil && p.Git.HasToken
	needSSH := p.Git != nil && p.Git.HasSSHKey
	builtReg := p.Image != nil && p.Image.BuiltFrom != nil
	needReg := p.Image != nil && p.Image.HasRegistryAuth && !builtReg
	if builtReg {
		// The image a build server pushed: pull it with a credential of the
		// registry the release recorded (not the service's own pull credentials).
		src := services.DecodeSource(rel.Source)
		if src.RegistryID == nil || s.registries == nil {
			return nil, errors.New("the release's registry is not available")
		}
		_, repo, _ := splitRegistryRef(src.RegistryRef)
		auth, err := s.registries.RegistryCredentials(ctx, *src.RegistryID, repo, []string{"pull"}, registryCredTTL)
		if err != nil {
			return nil, fmt.Errorf("registry pull credentials: %w", err)
		}
		out[agentprotocol.SecretRegistryAuth] = auth
	}
	if needGit || needSSH || needReg {
		svcID, err := uuid.Parse(p.ServiceID)
		if err != nil {
			return nil, fmt.Errorf("deploy payload service id: %w", err)
		}
		sc, err := services.LoadScopeInternal(ctx, s.q, svcID)
		if err != nil {
			return nil, fmt.Errorf("load service: %w", err)
		}
		if needGit {
			c, err := s.gitCreds(ctx, sc.Service, p.Git.URL)
			if err != nil {
				return nil, err
			}
			c.attach(out)
		}
		if needSSH {
			creds, err := s.sshCreds(ctx, sc.Service.ID, p.Git.URL)
			if err != nil {
				return nil, fmt.Errorf("deploy key: %w", err)
			}
			if creds == nil {
				return nil, errors.New("deploy key: the git url is no longer an SSH URL")
			}
			out[agentprotocol.SecretGitSSHKey] = string(creds.PrivateKeyPEM)
		}
		if needReg && len(sc.Service.RegistryAuthEnc) > 0 {
			raw, err := s.Box.Open(secretbox.ServicesRegistryAuth, sc.Service.ID.String(), sc.Service.RegistryAuthEnc)
			if err != nil {
				return nil, fmt.Errorf("decrypt registry auth: %w", err)
			}
			// Docker's X-Registry-Auth: base64url(JSON{username,password}).
			out[agentprotocol.SecretRegistryAuth] = base64.URLEncoding.EncodeToString(raw)
		}
	}
	return out, nil
}

// statusRank orders the active statuses so late or repeated agent events never
// move a deployment backwards.
func statusRank(s Status) int {
	switch s {
	case Created:
		return 1
	case Queued:
		return 2
	case Preparing:
		return 3
	case Building:
		return 4
	case Releasing:
		return 5
	case Verifying:
		return 6
	}
	return 100
}

// HandleEvent records an agent deploy step and advances the deployment status
// it implies. Events for unknown, foreign or finished deployments are ignored.
func (s *Service) HandleEvent(ctx context.Context, serverID uuid.UUID, ev agentprotocol.EventFrame) error {
	depID, err := uuid.Parse(ev.DeploymentID)
	if err != nil {
		return nil
	}
	dc, err := s.loadDeploymentInternal(ctx, depID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("load deployment: %w", err)
	}
	// The target and every server that owns a command of the deployment (the
	// build server of a build_image command) may report its events.
	owns, err := s.reporterOwns(ctx, dc.Dep, serverID)
	if err != nil {
		return fmt.Errorf("check event owner: %w", err)
	}
	if !owns {
		s.Logger.WarnContext(ctx, "event from a server that does not own the deployment ignored", "correlation_id", depID.String(), "server_id", serverID.String())
		return nil
	}
	changed := false
	err = db.InTx(ctx, s.Pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		cur, err := q.LockDeployment(ctx, depID)
		if err != nil {
			return err
		}
		if Status(cur.Status).IsTerminal() {
			return nil
		}
		if err := q.InsertDeploymentEvent(ctx, db.InsertDeploymentEventParams{
			DeploymentID: depID, Step: ev.Step, Status: ev.Status, Message: ev.Message,
		}); err != nil {
			return err
		}
		changed = true
		if ev.Status == agentprotocol.StepFailed {
			return nil // the command result decides the outcome
		}
		target, ok := stepStatus(ev.Step)
		if !ok || statusRank(target) <= statusRank(Status(cur.Status)) {
			return nil
		}
		_, _, err = s.advance(ctx, tx, depID, target, "")
		return err
	})
	if err != nil {
		return fmt.Errorf("handle event: %w", err)
	}
	if changed {
		s.publish(services.EventDeploymentUpdated, dc)
		s.publishService(dc)
		if ev.Step == agentprotocol.StepPostdeploy && ev.Status == agentprotocol.StepWarning {
			s.notifyPostDeployWarning(ctx, dc, ev.Message)
		}
	}
	return nil
}

// HandleResult applies a deploy command's outcome. It is idempotent: a result
// for an already finished deployment is a no-op.
func (s *Service) HandleResult(ctx context.Context, serverID uuid.UUID, commandID uuid.UUID, deploymentID *uuid.UUID, res agentprotocol.ResultFrame) error {
	if deploymentID == nil {
		if res.Outcome != agentprotocol.OutcomeSucceeded {
			s.Logger.WarnContext(ctx, "agent command failed", "command_id", commandID.String(), "server_id", serverID.String(), "outcome", res.Outcome, "err", res.Error)
		}
		return nil
	}
	dc, err := s.loadDeploymentInternal(ctx, *deploymentID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("load deployment: %w", err)
	}
	cmd, err := s.q.GetAgentCommand(ctx, commandID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("load command: %w", err)
	}
	if err == nil && cmd.Kind == agentprotocol.KindBuildImage {
		if cmd.ServerID != serverID {
			return nil
		}
		return s.handleBuildResult(ctx, serverID, cmd, dc, res)
	}
	if dc.Dep.ServerID != nil && *dc.Dep.ServerID != serverID {
		return nil
	}
	var dr agentprotocol.DeployResult
	if len(res.Data) > 0 {
		_ = json.Unmarshal(res.Data, &dr)
	}
	target := outcomeStatus(res.Outcome)
	msg := dr.Message
	if msg == "" {
		msg = res.Error
	}
	if msg == "" && target == Failed {
		msg = "The deployment failed."
	}
	log := s.Logger.With("correlation_id", deploymentID.String(), "service", dc.ServiceSlug)

	changed := false
	var resyncServer *uuid.UUID
	err = db.InTx(ctx, s.Pool, func(tx pgx.Tx) error {
		if target == Succeeded && dc.Dep.Strategy == StrategyRouteOnly {
			// A route-only agent may report no step events: a deployment still
			// in preparing passes through releasing, as a container deploy would.
			if cur, err := db.New(tx).LockDeployment(ctx, *deploymentID); err == nil && Status(cur.Status) == Preparing {
				if _, _, err := s.advance(ctx, tx, *deploymentID, Releasing, ""); err != nil {
					return err
				}
			}
		}
		if err := s.recordProcessOutcomes(ctx, db.New(tx), *deploymentID, dr.Processes); err != nil {
			return err
		}
		dep, ch, err := s.advance(ctx, tx, *deploymentID, target, msg)
		if err != nil {
			return err
		}
		if dr.Build != nil {
			if raw, merr := json.Marshal(dr.Build); merr == nil {
				if err := db.New(tx).SetDeploymentBuildInfo(ctx, db.SetDeploymentBuildInfoParams{ID: *deploymentID, BuildInfo: raw}); err != nil {
					return err
				}
			}
		}
		changed = ch
		if ch && dr.CanceledAt != "" {
			// A cancel the agent carried out (or tried to: rollback_failed).
			if err := db.New(tx).SetDeploymentCanceledStep(ctx, db.SetDeploymentCanceledStepParams{ID: *deploymentID, Step: dr.CanceledAt}); err != nil {
				return err
			}
			if err := audit.Record(ctx, tx, audit.Entry{
				Action: "deployment.canceled", ResourceType: "deployment", ResourceID: deploymentID.String(), ProjectID: &dc.ProjectID,
				Metadata: map[string]any{"service": dc.ServiceSlug, "step": dr.CanceledAt, "outcome": res.Outcome}, WorkspaceID: dc.WorkspaceID,
			}); err != nil {
				return err
			}
		}
		if !ch || target != Succeeded {
			return nil
		}
		if s.resync != nil {
			// Decide before the service points at this deployment.
			if cur, err := services.LoadScopeInternal(ctx, db.New(tx), dep.ServiceID); err == nil &&
				changesEgressState(ctx, db.New(tx), log, dep, cur.Service.CurrentDeploymentID) {
				resyncServer = dep.ServerID
			}
		}
		return s.finalizeSuccess(ctx, tx, dc, dep, dr)
	})
	if err != nil {
		return fmt.Errorf("handle result: %w", err)
	}
	if changed {
		log.InfoContext(ctx, "deployment finished", "status", string(target), "message", msg)
		if resyncServer != nil {
			// The environment's blocked state changed: running sibling replicas
			// learn it from the desired state, no redeploy needed.
			if err := s.resync.Resync(context.WithoutCancel(ctx), *resyncServer); err != nil {
				log.WarnContext(ctx, "resync after egress change", "server_id", resyncServer.String(), "err", err)
			}
		}
		s.publish(services.EventDeploymentUpdated, dc)
		s.publishService(dc)
		if target.IsTerminal() {
			s.notifyFinished(ctx, dc, target, msg)
		}
	}
	return nil
}

// finalizeSuccess promotes a successful deployment: it becomes the service's
// current deployment, the release learns its digest, domains turn active and, if
// the service moved servers, the old container is scheduled for removal.
func (s *Service) finalizeSuccess(ctx context.Context, tx pgx.Tx, dc depCtx, dep db.Deployment, dr agentprotocol.DeployResult) error {
	q := db.New(tx)
	sc, err := services.LoadScopeInternal(ctx, q, dep.ServiceID)
	if err != nil {
		return nil // service deleted while deploying
	}
	prev := sc.Service.CurrentDeploymentID
	if err := q.MarkServiceDeployed(ctx, db.MarkServiceDeployedParams{ID: sc.Service.ID, CurrentDeploymentID: &dep.ID, AppliedGeneration: dep.Generation}); err != nil {
		return err
	}
	observed, observedAt := stackObserved(dep.ID, dr), (*time.Time)(nil)
	if observed != nil {
		now := time.Now().UTC()
		observedAt = &now
	}
	if err := q.SetServiceObserved(ctx, db.SetServiceObservedParams{ID: sc.Service.ID, Observed: observed, ObservedAt: observedAt}); err != nil {
		return err
	}
	var rel db.Release
	if dep.ReleaseID != nil {
		if rel, err = q.GetReleaseByID(ctx, *dep.ReleaseID); err != nil {
			return err
		}
		if images := stackImages(dr); images != nil || dr.ImageDigest != "" {
			src := services.DecodeSource(rel.Source)
			switch {
			case images != nil:
				src.Components = images
			case src.Type == services.SourceGit && src.RegistryRef != "":
				// Pulled from a registry by digest: the release already names it.
			case src.Type == services.SourceGit:
				src.ImageID = dr.ImageDigest
			default:
				src.Digest = dr.ImageDigest
			}
			raw, err := json.Marshal(src)
			if err != nil {
				return err
			}
			if err := q.UpdateReleaseSource(ctx, db.UpdateReleaseSourceParams{ID: rel.ID, Source: raw}); err != nil {
				return err
			}
		}
	}
	if dep.ReleaseID != nil && services.DecodeSource(rel.Source).RegistryRef != "" {
		if err := s.retainRegistryImages(ctx, q, sc, *dep.ReleaseID); err != nil {
			return err
		}
	}
	if dep.ReleaseID != nil && sc.Service.Kind != services.KindRoute {
		// A service deployed without eligibility (volume added, port removed)
		// runs one replica from now on.
		snap := services.DecodeSnapshot(rel.Config)
		vols, err := q.CountServiceVolumes(ctx, sc.Service.ID)
		if err != nil {
			return err
		}
		// A staged rename of a process takes its scale to the new name now that
		// the release runs it.
		if err := services.FinalizeProcessRenames(ctx, tx, sc, snap.Config); err != nil {
			return err
		}
		if _, err := services.ClampReplicas(ctx, tx, sc, snap.Config, int(vols)); err != nil {
			return err
		}
		// Processes that cannot scale run one replica from now on, and the scale of
		// processes neither the release nor the staged configuration has is forgotten.
		if err := services.ClampProcessReplicas(ctx, tx, sc, snap.Config, int(vols)); err != nil {
			return err
		}
	}
	if err := q.SetServiceDomainsActive(ctx, sc.Service.ID); err != nil {
		return err
	}
	if s.dns != nil {
		// The service now runs on dep's server: point its DNS records there.
		if err := s.dns.EnqueueSyncService(ctx, tx, sc.Service.ID); err != nil {
			return err
		}
	}
	if s.ServiceTailnet != nil && dr.Tailnet != nil {
		if err := s.ServiceTailnet.RecordDeploy(ctx, tx, dc.WorkspaceID, dep, dr.Tailnet); err != nil {
			return err
		}
	}

	// A rollback leaves the service config ahead of the running release:
	// stage the difference so it shows up as pending changes.
	if dep.Trigger == TriggerRollback && dep.ReleaseID != nil {
		fresh, err := services.LoadScopeInternal(ctx, q, sc.Service.ID)
		if err == nil {
			if in, err := s.gather(ctx, q, fresh); err == nil {
				if diff, err := s.diffAgainstRelease(ctx, q, in, rel); err == nil && len(diff) > 0 && fresh.Service.Generation <= dep.Generation {
					if _, err := q.BumpServiceGeneration(ctx, sc.Service.ID); err != nil {
						return err
					}
				}
			}
		}
	}

	if sc.Service.Kind == services.KindRoute {
		return s.finalizeRoute(ctx, tx, dep, prev)
	}
	// Moved to another server: remove the container left on the old one.
	if prev != nil && *prev != dep.ID {
		old, err := q.ListDeploymentsByIDs(ctx, []uuid.UUID{*prev})
		if err == nil && len(old) == 1 && old[0].ServerID != nil && dep.ServerID != nil && *old[0].ServerID != *dep.ServerID {
			raw, _ := json.Marshal(agentprotocol.ServiceActionPayload{
				ServiceID: sc.Service.ID.String(), StopGraceSeconds: sc.Config().StopGraceSeconds, Stack: sc.Service.Kind == services.KindStack,
			})
			if _, err := s.Dispatcher.Enqueue(ctx, tx, agents.NewCommand{ServerID: *old[0].ServerID, Kind: agentprotocol.KindRemoveService, Payload: raw}); err != nil {
				s.Logger.WarnContext(ctx, "could not queue removal of the container on the previous server", "correlation_id", dep.ID.String(), "err", err)
			}
		}
	}
	return nil
}

// HandleUndelivered fails the deployment of a command that never reached its agent.
func (s *Service) HandleUndelivered(ctx context.Context, _ uuid.UUID, commandID uuid.UUID, deploymentID *uuid.UUID, msg string) error {
	if deploymentID == nil {
		return nil
	}
	s.Logger.InfoContext(ctx, "deployment command was not delivered", "correlation_id", deploymentID.String(), "command_id", commandID.String())
	if cmd, err := s.q.GetAgentCommand(ctx, commandID); err == nil && cmd.Kind == agentprotocol.KindBuildImage {
		// The build server never took the build: build on the target instead.
		dc, err := s.loadDeploymentInternal(ctx, *deploymentID)
		if err == nil && !Status(dc.Dep.Status).IsTerminal() {
			return s.buildFallbackAfterUndelivered(ctx, cmd, dc)
		}
	}
	return s.failDeployment(ctx, *deploymentID, msg)
}
