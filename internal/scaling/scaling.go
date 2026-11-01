// Package scaling implements same-server replicas of stateless services:
// the manual scale API, the scale_service command payload and the CPU
// autoscaler. Replica settings are operational: they are not part of a
// release and never stage a change.
package scaling

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nebulactrl/nebulactrl/internal/agents"
	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/domains"
	"github.com/nebulactrl/nebulactrl/internal/events"
	"github.com/nebulactrl/nebulactrl/internal/notifications"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/prodguard"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Audit actions.
const (
	ActionScale     = "service.scale"
	ActionAutoscale = "service.autoscale"
)

// Service implements the scaling use cases.
type Service struct {
	pool   *pgxpool.Pool
	q      *db.Queries
	bus    *events.Bus
	disp   agents.Dispatcher
	svc    *services.Service
	logger *slog.Logger
	now    func() time.Time

	notifier notifications.Emitter
}

// NewService returns a scaling Service (pool, disp and svc may be nil for
// OpenAPI generation).
func NewService(pool *pgxpool.Pool, bus *events.Bus, disp agents.Dispatcher, svc *services.Service, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	s := &Service{pool: pool, bus: bus, disp: disp, svc: svc, logger: logger, now: time.Now}
	if pool != nil {
		s.q = db.New(pool)
	}
	return s
}

// AutoscalingPatch changes autoscaling settings; nil fields keep their value.
type AutoscalingPatch struct {
	Enabled          *bool `json:"enabled,omitempty"`
	Min              *int  `json:"min,omitempty" doc:"Lowest replica count, 1-10"`
	Max              *int  `json:"max,omitempty" doc:"Highest replica count, 1-10; at least min"`
	TargetCPUPercent *int  `json:"targetCpuPercent,omitempty" doc:"Per-replica CPU target as a percentage of the replica's CPU limit (one core without a limit), 20-95"`
}

// apply returns cur with the patch applied.
func (p *AutoscalingPatch) apply(cur services.Autoscaling) services.Autoscaling {
	if p == nil {
		return cur
	}
	if p.Enabled != nil {
		cur.Enabled = *p.Enabled
	}
	if p.Min != nil {
		cur.Min = *p.Min
	}
	if p.Max != nil {
		cur.Max = *p.Max
	}
	if p.TargetCPUPercent != nil {
		cur.TargetCPUPercent = *p.TargetCPUPercent
	}
	return cur
}

// SetInput is a scaling update. Nil fields are left unchanged.
type SetInput struct {
	Replicas    *int
	Autoscaling *AutoscalingPatch
}

func invalid(msg string) *apierr.Error { return apierr.Unprocessable("validation_failed", msg) }

// Set changes the replica count and/or the autoscaling settings of a service.
// A deployed service is scaled immediately through a scale_service command; an
// undeployed one only stores the setting.
func (s *Service) Set(ctx context.Context, id uuid.UUID, in SetInput) (services.ServiceView, error) {
	sc, err := services.LoadScope(ctx, s.q, policy.ScopeDeploymentsWrite, id)
	if err != nil {
		return services.ServiceView{}, err
	}
	if err := services.RequireContainer(sc, "scaling"); err != nil {
		return services.ServiceView{}, err
	}
	if err := services.RequireNotStack(sc); err != nil {
		return services.ServiceView{}, err
	}
	if in.Replicas == nil && in.Autoscaling == nil {
		return services.ServiceView{}, invalid("replicas or autoscaling is required")
	}
	vols, err := s.q.CountServiceVolumes(ctx, id)
	if err != nil {
		return services.ServiceView{}, fmt.Errorf("count volumes: %w", err)
	}
	eligible, _ := services.Eligibility(sc.Service.Kind, sc.Config(), int(vols))

	curAS := services.DecodeAutoscaling(sc.Service.Autoscaling)
	newAS := in.Autoscaling.apply(curAS)
	if in.Autoscaling != nil {
		if err := newAS.Validate(); err != nil {
			return services.ServiceView{}, invalid(err.Error())
		}
	}
	if in.Replicas != nil && (*in.Replicas < services.MinReplicas || *in.Replicas > services.MaxReplicas) {
		return services.ServiceView{}, invalid(fmt.Sprintf("replicas must be between %d and %d", services.MinReplicas, services.MaxReplicas))
	}
	if !eligible && ((in.Replicas != nil && *in.Replicas != services.MinReplicas) || (in.Autoscaling != nil && newAS.Enabled)) {
		return services.ServiceView{}, apierr.Unprocessable("replicas_require_stateless", services.ReplicasRequireStateless)
	}
	if in.Replicas != nil && newAS.Enabled {
		return services.ServiceView{}, apierr.Conflict("autoscaling_enabled", "autoscaling is enabled: turn it off to set the replica count manually")
	}

	from := int(sc.Service.Replicas)
	to := from
	reason := ""
	switch {
	case in.Replicas != nil:
		to, reason = *in.Replicas, "Set manually"
	case newAS.Enabled && (from < newAS.Min || from > newAS.Max):
		to, reason = min(max(from, newAS.Min), newAS.Max), fmt.Sprintf("Adjusted to the autoscaling range %d–%d", newAS.Min, newAS.Max)
	}
	asChanged := in.Autoscaling != nil && newAS != curAS

	if to != from {
		if err := s.requireScalable(ctx, sc); err != nil {
			return services.ServiceView{}, err
		}
	}
	if to == from && !asChanged {
		return s.svc.ViewFor(ctx, sc)
	}
	if prodguard.Applies(ctx, sc.IsProduction) {
		var eff []string
		if to != from {
			eff = append(eff, fmt.Sprintf("Changes the replica count of %s from %d to %d immediately, without a deployment.", sc.Service.Name, from, to))
		}
		if asChanged {
			eff = append(eff, fmt.Sprintf("Changes autoscaling (enabled: %t, %d–%d replicas, CPU target %d%%).", newAS.Enabled, newAS.Min, newAS.Max, newAS.TargetCPUPercent))
		}
		if err := sc.Guard(ctx, "set_scaling",
			fmt.Sprintf("Change the scaling of %s in production (%s / %s)", sc.Service.Name, sc.ProjectName, sc.EnvName), false, eff, to, newAS); err != nil {
			return services.ServiceView{}, err
		}
	}

	actor := actorLabel(ctx)
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		if asChanged {
			raw, _ := json.Marshal(newAS)
			if _, err := q.SetServiceAutoscaling(ctx, db.SetServiceAutoscalingParams{ID: id, Autoscaling: raw}); err != nil {
				return fmt.Errorf("store autoscaling: %w", err)
			}
		}
		meta := map[string]any{"slug": sc.Service.Slug, "from": from, "to": to, "actor": actor}
		if asChanged {
			meta["autoscaling"] = map[string]any{"from": curAS, "to": newAS}
		}
		if to != from {
			meta["reason"] = reason
			if err := s.applyScale(ctx, tx, sc, from, to, reason, actor); err != nil {
				return err
			}
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: ActionScale, ResourceType: "service", ResourceID: id.String(), ProjectID: &sc.ProjectID, WorkspaceID: sc.WorkspaceID,
			Metadata: meta,
		})
	})
	if err != nil {
		if _, ok := apierr.As(err); ok {
			return services.ServiceView{}, err
		}
		return services.ServiceView{}, fmt.Errorf("set scaling: %w", err)
	}
	services.PublishService(s.bus, services.EventServiceUpdated, sc)
	fresh, err := services.LoadScope(ctx, s.q, policy.ScopeProjectsRead, id)
	if err != nil {
		return services.ServiceView{}, err
	}
	return s.svc.ViewFor(ctx, fresh)
}

// SetProcess is Set for one process of a service: the default process is the
// service's own setting (Set), a named process keeps its replica count and
// autoscaling in its service_process_scale row. A deployed process is scaled
// immediately through a scale_service command; one that is staged but not in the
// running release only stores the setting.
func (s *Service) SetProcess(ctx context.Context, id uuid.UUID, process string, in SetInput) (services.ServiceView, error) {
	sc, err := services.LoadScope(ctx, s.q, policy.ScopeDeploymentsWrite, id)
	if err != nil {
		return services.ServiceView{}, err
	}
	if err := services.RequireContainer(sc, "scaling"); err != nil {
		return services.ServiceView{}, err
	}
	if err := services.RequireNotStack(sc); err != nil {
		return services.ServiceView{}, err
	}
	cfg := sc.Config()
	p, ok := cfg.Process(process)
	if !ok {
		return services.ServiceView{}, apierr.New(http.StatusNotFound, "unknown_process", fmt.Sprintf("the service has no process %q", process)).
			WithDetails(map[string]any{"processes": cfg.ProcessNames()})
	}
	if p.Default {
		return s.Set(ctx, id, in)
	}
	if in.Replicas == nil && in.Autoscaling == nil {
		return services.ServiceView{}, invalid("replicas or autoscaling is required")
	}
	vols, err := s.q.CountServiceVolumes(ctx, id)
	if err != nil {
		return services.ServiceView{}, fmt.Errorf("count volumes: %w", err)
	}
	eligible, why := services.ProcessEligibility(sc.Service.Kind, cfg, int(vols), p)
	rows, err := s.q.ListProcessScales(ctx, id)
	if err != nil {
		return services.ServiceView{}, fmt.Errorf("list process scales: %w", err)
	}
	cur := services.ProcessScalingOf(rows, p.Name)
	newAS := in.Autoscaling.apply(cur.Autoscaling)
	if in.Autoscaling != nil {
		if err := newAS.Validate(); err != nil {
			return services.ServiceView{}, invalid(err.Error())
		}
	}
	if in.Replicas != nil && (*in.Replicas < services.MinReplicas || *in.Replicas > services.MaxReplicas) {
		return services.ServiceView{}, invalid(fmt.Sprintf("replicas must be between %d and %d", services.MinReplicas, services.MaxReplicas))
	}
	if !eligible && ((in.Replicas != nil && *in.Replicas != services.MinReplicas) || (in.Autoscaling != nil && newAS.Enabled)) {
		return services.ServiceView{}, apierr.Unprocessable("replicas_require_stateless", why)
	}
	if in.Replicas != nil && newAS.Enabled {
		return services.ServiceView{}, apierr.Conflict("autoscaling_enabled", "autoscaling is enabled: turn it off to set the replica count manually")
	}

	from := cur.Replicas
	to := from
	reason := ""
	switch {
	case in.Replicas != nil:
		to, reason = *in.Replicas, "Set manually"
	case newAS.Enabled && (from < newAS.Min || from > newAS.Max):
		to, reason = min(max(from, newAS.Min), newAS.Max), fmt.Sprintf("Adjusted to the autoscaling range %d–%d", newAS.Min, newAS.Max)
	}
	asChanged := in.Autoscaling != nil && newAS != cur.Autoscaling

	if to != from {
		if err := s.requireScalable(ctx, sc); err != nil {
			return services.ServiceView{}, err
		}
	}
	if to == from && !asChanged {
		return s.svc.ViewFor(ctx, sc)
	}
	if prodguard.Applies(ctx, sc.IsProduction) {
		var eff []string
		if to != from {
			eff = append(eff, fmt.Sprintf("Changes the replica count of process %s of %s from %d to %d immediately, without a deployment.", p.Name, sc.Service.Name, from, to))
		}
		if asChanged {
			eff = append(eff, fmt.Sprintf("Changes autoscaling of process %s (enabled: %t, %d–%d replicas, CPU target %d%%).", p.Name, newAS.Enabled, newAS.Min, newAS.Max, newAS.TargetCPUPercent))
		}
		if err := sc.Guard(ctx, "set_scaling",
			fmt.Sprintf("Change the scaling of process %s of %s in production (%s / %s)", p.Name, sc.Service.Name, sc.ProjectName, sc.EnvName), false, eff, p.Name, to, newAS); err != nil {
			return services.ServiceView{}, err
		}
	}

	actor := actorLabel(ctx)
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		// The compare-and-swap of applyProcessScale needs a row to swap.
		if _, err := q.EnsureProcessScale(ctx, db.EnsureProcessScaleParams{ServiceID: id, Process: p.Name}); err != nil {
			return fmt.Errorf("ensure process scale: %w", err)
		}
		if asChanged {
			raw, _ := json.Marshal(newAS)
			if _, err := q.SetProcessAutoscaling(ctx, db.SetProcessAutoscalingParams{ServiceID: id, Process: p.Name, Autoscaling: raw}); err != nil {
				return fmt.Errorf("store autoscaling: %w", err)
			}
		}
		meta := map[string]any{"slug": sc.Service.Slug, "process": p.Name, "from": from, "to": to, "actor": actor}
		if asChanged {
			meta["autoscaling"] = map[string]any{"from": cur.Autoscaling, "to": newAS}
		}
		if to != from {
			meta["reason"] = reason
			if err := s.applyProcessScale(ctx, tx, sc, p.Name, from, to, reason, actor); err != nil {
				return err
			}
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: ActionScale, ResourceType: "service", ResourceID: id.String(), ProjectID: &sc.ProjectID, WorkspaceID: sc.WorkspaceID,
			Metadata: meta,
		})
	})
	if err != nil {
		if _, ok := apierr.As(err); ok {
			return services.ServiceView{}, err
		}
		return services.ServiceView{}, fmt.Errorf("set process scaling: %w", err)
	}
	services.PublishService(s.bus, services.EventServiceUpdated, sc)
	fresh, err := services.LoadScope(ctx, s.q, policy.ScopeProjectsRead, id)
	if err != nil {
		return services.ServiceView{}, err
	}
	return s.svc.ViewFor(ctx, fresh)
}

// requireScalable rejects a replica count change while the service cannot be
// scaled right now.
func (s *Service) requireScalable(ctx context.Context, sc services.Scope) error {
	active, err := s.q.CountActiveDeployments(ctx, sc.Service.ID)
	if err != nil {
		return fmt.Errorf("count active deployments: %w", err)
	}
	if active > 0 {
		return apierr.Conflict("deployment_in_progress", "a deployment is in progress; scale after it finishes")
	}
	if sc.Service.DesiredState == services.DesiredStopped && sc.Service.CurrentDeploymentID != nil {
		return apierr.Conflict("service_stopped", "the service is stopped; restart it before scaling")
	}
	return nil
}

// applyScale stores the new replica count of the default process (compare-and-swap
// on from) and, for a deployed service, enqueues the scale_service command, all
// inside tx.
func (s *Service) applyScale(ctx context.Context, tx pgx.Tx, sc services.Scope, from, to int, reason, actor string) error {
	q := db.New(tx)
	row, err := q.ApplyServiceScale(ctx, db.ApplyServiceScaleParams{
		ID: sc.Service.ID, Replicas: int32(to), ExpectedReplicas: int32(from),
		LastScaleEvent: services.NewScaleEvent(from, to, reason, actor),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return apierr.Conflict("scale_conflict", "the replica count changed concurrently; retry")
		}
		return fmt.Errorf("store replicas: %w", err)
	}
	if sc.Service.CurrentDeploymentID == nil {
		return nil
	}
	return s.enqueueScale(ctx, tx, sc, "", to, row.LastScaleAt)
}

// applyProcessScale is applyScale for a named process: the count lives in its
// service_process_scale row. A process that the running release does not have
// (staged, not deployed yet) only stores the setting.
func (s *Service) applyProcessScale(ctx context.Context, tx pgx.Tx, sc services.Scope, process string, from, to int, reason, actor string) error {
	q := db.New(tx)
	row, err := q.ApplyProcessScale(ctx, db.ApplyProcessScaleParams{
		ServiceID: sc.Service.ID, Process: process, Replicas: int32(to), ExpectedReplicas: int32(from),
		LastScaleEvent: services.NewScaleEvent(from, to, reason, actor),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return apierr.Conflict("scale_conflict", "the replica count changed concurrently; retry")
		}
		return fmt.Errorf("store replicas of process %s: %w", process, err)
	}
	deployed, err := processDeployed(ctx, q, sc.Service, process)
	if err != nil || !deployed {
		return err
	}
	return s.enqueueScale(ctx, tx, sc, process, to, row.LastScaleAt)
}

// processDeployed reports whether the running release has the named process.
func processDeployed(ctx context.Context, q *db.Queries, sv db.Service, process string) (bool, error) {
	rel, ok, err := services.AppliedRelease(ctx, q, sv)
	if err != nil || !ok {
		return false, err
	}
	applied := services.DecodeSnapshot(rel.Config).Config
	p, found := applied.Process(process)
	return applied.HasProcesses() && found && !p.Default, nil
}

// enqueueScale sends the scale_service command of one process ("" = the default
// process) to the server of the current deployment.
func (s *Service) enqueueScale(ctx context.Context, tx pgx.Tx, sc services.Scope, process string, to int, lastScaleAt *time.Time) error {
	var scaledAt time.Time
	if lastScaleAt != nil {
		scaledAt = *lastScaleAt
	}
	serverID, raw, err := s.scalePayload(ctx, db.New(tx), sc, process, to, scaledAt)
	if err != nil {
		return err
	}
	if _, err := s.disp.Enqueue(ctx, tx, agents.NewCommand{ServerID: serverID, Kind: agentprotocol.KindScaleService, Payload: raw}); err != nil {
		return fmt.Errorf("enqueue scale_service: %w", err)
	}
	return nil
}

// scalePayload builds the scale_service payload: the current deployment's
// persisted deploy payload (it never holds secret values; SecretsForCommand
// attaches them at send time) with the target replica count and a fresh route
// table of the server's other services. process "" scales the default process;
// in a multi-process payload the process' spec carries the count too.
func (s *Service) scalePayload(ctx context.Context, q *db.Queries, sc services.Scope, process string, replicas int, scaledAt time.Time) (uuid.UUID, json.RawMessage, error) {
	depID := *sc.Service.CurrentDeploymentID
	deps, err := q.ListDeploymentsByIDs(ctx, []uuid.UUID{depID})
	if err != nil {
		return uuid.Nil, nil, fmt.Errorf("current deployment: %w", err)
	}
	if len(deps) != 1 || deps[0].ServerID == nil {
		return uuid.Nil, nil, apierr.Conflict("not_deployed", "the service has no running deployment")
	}
	serverID := *deps[0].ServerID
	cmd, err := q.GetDeployCommandByDeployment(ctx, &depID)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, nil, apierr.Conflict("not_deployed", "the current deployment's command is no longer available; redeploy the service")
	}
	if err != nil {
		return uuid.Nil, nil, fmt.Errorf("current deploy command: %w", err)
	}
	var dp agentprotocol.DeployPayload
	if err := json.Unmarshal(cmd.Payload, &dp); err != nil {
		return uuid.Nil, nil, fmt.Errorf("decode deploy payload: %w", err)
	}
	isDefault := true
	if len(dp.Processes) > 0 {
		found := false
		for i := range dp.Processes {
			if (process == "" && dp.Processes[i].Default) || (process != "" && dp.Processes[i].Name == process) {
				dp.Processes[i].Replicas = replicas
				isDefault, found = dp.Processes[i].Default, true
			}
		}
		if !found {
			return uuid.Nil, nil, apierr.Conflict("not_deployed", fmt.Sprintf("the process %q is not part of the running deployment", process))
		}
	}
	if isDefault {
		dp.Replicas = replicas
	}
	// The server's Unrestricted switch may have been turned off since the
	// payload was built: new replicas must not get host access it no longer allows.
	srv, err := q.LoadServerRow(ctx, db.LoadServerRowParams{ID: serverID})
	if err != nil {
		return uuid.Nil, nil, fmt.Errorf("load server: %w", err)
	}
	if len(dp.Processes) > 0 {
		if has, known := services.ServerHasCapability(srv, agentprotocol.CapProcesses); known && !has {
			return uuid.Nil, nil, agentTooOld(srv.Name)
		}
	}
	if err := services.CheckDockerPlacement(services.Config{Docker: dp.Docker}, srv.Unrestricted); err != nil {
		return uuid.Nil, nil, err
	}
	dp.DockerPolicy = &agentprotocol.DockerPolicy{Unrestricted: srv.Unrestricted}
	routes, err := domains.ServerRoutes(ctx, q, serverID, sc.Service.ID)
	if err != nil {
		return uuid.Nil, nil, err
	}
	dp.Routes = routes
	// The environment may have gained or lost a service that blocks egress
	// since this payload was built.
	if dp.EnvHasEgressBlocked, err = services.EnvHasEgressBlocked(ctx, q, sc.EnvironmentID, serverID, sc.Service.ID, dp.BlockEgress); err != nil {
		return uuid.Nil, nil, err
	}
	raw, err := json.Marshal(agentprotocol.ScaleServicePayload{
		ServiceID: sc.Service.ID.String(), DeploymentID: depID.String(), Replicas: replicas, Process: process, ScaledAt: scaledAt, Deploy: dp,
	})
	if err != nil {
		return uuid.Nil, nil, err
	}
	return serverID, raw, nil
}

// agentTooOld is the 422 for a service with several processes on a server whose
// agent cannot run them.
func agentTooOld(server string) *apierr.Error {
	return apierr.Unprocessable("agent_too_old", fmt.Sprintf("The agent on server %s is too old to run a service with several processes. Update the agent, then deploy again. No changes were made.", server))
}

// notifyProcess is the process name a scale notice names: only services with
// several processes have one worth naming, the default process included.
func notifyProcess(cfg services.Config, process string) string {
	if !cfg.HasProcesses() {
		return ""
	}
	if p, ok := cfg.Process(process); ok {
		return p.Name
	}
	return process
}

// RecordScaleFailure stores why a scale_service command failed, so the node
// can show it (cleared by the next successful scale or autoscaling change).
// It describes the default process.
func (s *Service) RecordScaleFailure(ctx context.Context, serviceID uuid.UUID, msg string) error {
	return s.recordScaleFailure(ctx, serviceID, "", msg)
}

// recordScaleFailure is RecordScaleFailure for one process: "" and the default
// process' name store the reason on the service, a named process on its own
// scale row.
func (s *Service) recordScaleFailure(ctx context.Context, serviceID uuid.UUID, process, msg string) error {
	sc, err := services.LoadScopeInternal(ctx, s.q, serviceID)
	if err != nil {
		return nil // deleted
	}
	if msg == "" {
		msg = "the agent could not scale the service"
	}
	reason := "Scale failed: " + msg
	named := ""
	if process != "" {
		p, ok := sc.Config().Process(process)
		if !ok {
			return nil // the process left the configuration meanwhile
		}
		if !p.Default {
			named = p.Name
		}
	}
	if named != "" {
		_, err = s.q.SetProcessScaleBlocked(ctx, db.SetProcessScaleBlockedParams{ServiceID: serviceID, Process: named, Reason: &reason})
	} else {
		_, err = s.q.SetServiceScaleBlocked(ctx, db.SetServiceScaleBlockedParams{ID: serviceID, Reason: &reason})
	}
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil // unchanged
		}
		return fmt.Errorf("store scale failure: %w", err)
	}
	services.PublishService(s.bus, services.EventServiceUpdated, sc)
	s.notifyBlocked(ctx, sc, notifyProcess(sc.Config(), process), reason)
	return nil
}

// actorLabel names who scaled: the caller's label, or "system".
func actorLabel(ctx context.Context) string {
	if p, ok := auth.PrincipalFrom(ctx); ok && p.Label != "" {
		return p.Label
	}
	return "system"
}

// HandleResult records the outcome of a command that belongs to no
// deployment. Only a failed scale_service command matters: its error becomes
// the service's blocked reason. Other kinds are ignored.
func (s *Service) HandleResult(ctx context.Context, commandID uuid.UUID, res agentprotocol.ResultFrame) error {
	if res.Outcome == agentprotocol.OutcomeSucceeded {
		return nil
	}
	cmd, err := s.q.GetAgentCommand(ctx, commandID)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && cmd.Kind != agentprotocol.KindScaleService) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("load command: %w", err)
	}
	var p agentprotocol.ScaleServicePayload
	if err := json.Unmarshal(cmd.Payload, &p); err != nil {
		return nil
	}
	id, err := uuid.Parse(p.ServiceID)
	if err != nil {
		return nil
	}
	msg := res.Error
	if msg == "" {
		msg = res.Outcome
	}
	return s.recordScaleFailure(ctx, id, p.Process, msg)
}
