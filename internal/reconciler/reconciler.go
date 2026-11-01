// Package reconciler keeps observed container state in sync with the control
// plane's desired state: it stores what agents observe, tells agents what
// should be running, and redeploys current releases whose container went
// missing (at most three times per service per hour).
package reconciler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/deployments"
	"github.com/nebulactrl/nebulactrl/internal/domains"
	"github.com/nebulactrl/nebulactrl/internal/events"
	"github.com/nebulactrl/nebulactrl/internal/jobs"
	"github.com/nebulactrl/nebulactrl/internal/networks"
	"github.com/nebulactrl/nebulactrl/internal/proxyserver"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/internal/svcalerts"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Automatic recovery limits.
const (
	MaxRedeploysPerHour = 3
	redeployWindow      = time.Hour
)

// LimitMessage is shown on a node whose recovery gave up.
const LimitMessage = "The container is missing and automatic recovery stopped after 3 attempts in the last hour. Redeploy manually."

var (
	_ interface {
		HandleObserved(context.Context, uuid.UUID, agentprotocol.Observed) error
		DesiredState(context.Context, uuid.UUID) (agentprotocol.Reconcile, error)
	} = (*Reconciler)(nil)
)

// Reconciler implements the observed/desired halves of agents.ResultHandler.
type Reconciler struct {
	pool   *pgxpool.Pool
	q      *db.Queries
	bus    *events.Bus
	jm     *jobs.Manager
	deps   *deployments.Service
	logger *slog.Logger
	now    func() time.Time
	geo    GeoSource
	logs   LogsSource
	alerts Alerter
	uptime UptimeSource
	// instanceTunnel: the instance services are set up, so capable agents
	// listen on the local endpoints that reach them (Reconcile.Endpoints).
	instanceTunnel bool
}

// New returns a Reconciler (pool and jm may be nil for OpenAPI generation).
func New(pool *pgxpool.Pool, bus *events.Bus, jm *jobs.Manager, deps *deployments.Service, logger *slog.Logger) *Reconciler {
	if logger == nil {
		logger = slog.Default()
	}
	return &Reconciler{pool: pool, q: db.New(pool), bus: bus, jm: jm, deps: deps, logger: logger, now: time.Now}
}

// GeoSource supplies a server's request-origin geolocation setting and, for
// the MaxMind source, the team's credentials as send-time secrets.
// *servers.Service implements it.
type GeoSource interface {
	GeoConfigFor(ctx context.Context, serverID uuid.UUID) (*agentprotocol.GeoConfig, map[string]string, error)
}

// SetGeo installs the geolocation source (nil leaves Reconcile.Geo unset).
func (r *Reconciler) SetGeo(g GeoSource) { r.geo = g }

// SetInstanceEndpoints turns on the local endpoints of the instance services
// (S3, registry, Loki) for every server whose agent can carry them.
func (r *Reconciler) SetInstanceEndpoints(on bool) { r.instanceTunnel = on }

// instanceEndpoints is the listener set a server's agent is offered: the three
// instance services when they are set up and the agent advertised
// CapInstanceTunnel, nil (unchanged) otherwise. An agent that never connected
// has no facts, so it is offered nothing; it gets them on its first reconcile
// after its facts arrive.
func instanceEndpoints(on bool, facts []byte) *agentprotocol.InstanceEndpoints {
	if !on || len(facts) == 0 {
		return nil
	}
	var f agentprotocol.Facts
	if json.Unmarshal(facts, &f) != nil || !slices.Contains(f.Capabilities, agentprotocol.CapInstanceTunnel) {
		return nil
	}
	return agentprotocol.StandardEndpoints()
}

// LogsSource supplies a server's log history configuration (retention, hosted
// services, drains) and the drains' credentials as send-time secrets.
// *logs.Service implements it.
type LogsSource interface {
	LogsConfigFor(ctx context.Context, serverID uuid.UUID) (*agentprotocol.LogsConfig, map[string]string, error)
}

// SetLogs installs the log configuration source (nil leaves Reconcile.Logs unset).
func (r *Reconciler) SetLogs(l LogsSource) { r.logs = l }

// UptimeSource supplies a server's uptime probing setting: the services whose
// monitoring is switched off. *uptime.Service implements it.
type UptimeSource interface {
	UptimeConfigFor(ctx context.Context, serverID uuid.UUID) (*agentprotocol.UptimeSettings, error)
}

// SetUptime installs the uptime setting source (nil leaves Reconcile.Uptime unset,
// so agents probe every service).
func (r *Reconciler) SetUptime(u UptimeSource) { r.uptime = u }

// Alerter is told about every observed report of a service's current
// deployment (service.crashed, service.unhealthy, service.restart_loop).
// *svcalerts.Service implements it.
type Alerter interface {
	Observe(ctx context.Context, in svcalerts.Input)
}

// SetAlerts installs the health alert detector (nil disables it).
func (r *Reconciler) SetAlerts(a Alerter) { r.alerts = a }

// alertOn feeds the health alert detector. A report that looks unhealthy is
// marked suppressed while a deployment is active (the container is being
// replaced on purpose); a service the owner stopped is marked stopped.
func (r *Reconciler) alertOn(ctx context.Context, sc services.Scope, o agentprotocol.ObservedService, at time.Time) {
	if r.alerts == nil {
		return
	}
	obs := svcalerts.Observation{
		At: at, DeploymentID: sc.Service.CurrentDeploymentID.String(), State: o.State, Health: o.Health,
		Stopped: sc.Service.DesiredState == services.DesiredStopped,
	}
	for _, rep := range o.Replicas {
		obs.Replicas = append(obs.Replicas, svcalerts.Replica{
			Key: fmt.Sprintf("%s/%d", replicaScope(rep), rep.Index), ContainerID: rep.ContainerID,
			State: rep.State, Health: rep.Health, RestartCount: rep.RestartCount,
		})
	}
	if !obs.Stopped && (o.State != agentprotocol.StateRunning || o.Health != agentprotocol.HealthHealthy) {
		if active, err := r.q.CountActiveDeployments(ctx, sc.Service.ID); err == nil && active > 0 {
			obs.Suppressed = true
		}
	}
	r.alerts.Observe(ctx, svcalerts.Input{
		WorkspaceID: sc.WorkspaceID, ProjectID: sc.ProjectID, EnvironmentID: sc.EnvironmentID,
		ServiceID: sc.Service.ID, ServiceName: sc.Service.Name, Observation: obs,
	})
}

// DesiredState lists, for one server, the services whose current deployment
// runs there, with their container names and routes.
func (r *Reconciler) DesiredState(ctx context.Context, serverID uuid.UUID) (agentprotocol.Reconcile, error) {
	out := agentprotocol.Reconcile{Services: []agentprotocol.DesiredService{}}
	srv, err := r.q.LoadServerRow(ctx, db.LoadServerRowParams{ID: serverID})
	if err != nil {
		return out, fmt.Errorf("load server: %w", err)
	}
	if srv.AcmeEmail != nil {
		out.ACMEEmail = *srv.AcmeEmail
	}
	traffic := srv.TrafficAnalyticsEnabled
	out.TrafficAnalytics = &traffic
	out.Endpoints = instanceEndpoints(r.instanceTunnel, srv.Facts)
	rows, err := r.q.ListServerCurrentServices(ctx, &serverID)
	if err != nil {
		return out, fmt.Errorf("list current services: %w", err)
	}
	// The table is numbered and read in one transaction, the number first (see
	// domains.ServerTable); the agent applies no table older than one it has.
	var routes []agentprotocol.Route
	err = db.InTx(ctx, r.pool, func(tx pgx.Tx) error {
		var seq uint64
		var err error
		if routes, seq, err = domains.ServerTable(ctx, db.New(tx), serverID, uuid.Nil); err != nil {
			return err
		}
		out.TableSeq = seq
		return nil
	})
	if err != nil {
		return out, err
	}
	// A stack's routes (one per component and port) carry the stack's service
	// ID in Route.Stack; every other route is keyed by its own service ID.
	routeOwner := func(rt agentprotocol.Route) string {
		if rt.Stack != "" {
			return rt.Stack
		}
		return rt.ServiceID
	}
	bySvc := map[string][]agentprotocol.Route{}
	for _, rt := range routes {
		bySvc[routeOwner(rt)] = append(bySvc[routeOwner(rt)], rt)
	}
	regular := map[string]bool{}
	for _, row := range rows {
		sv := row.Service
		if sv.CurrentDeploymentID == nil || sv.Kind == services.KindRoute {
			continue // route services have no container: their routes are ExternalRoutes
		}
		regular[sv.ID.String()] = true
		replicas := max(int(sv.Replicas), 1)
		var snap services.Snapshot
		if row.CurrentReleaseID != nil {
			rel, err := r.q.GetReleaseByID(ctx, *row.CurrentReleaseID)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return out, fmt.Errorf("get release: %w", err)
			}
			if err == nil {
				snap = services.DecodeSnapshot(rel.Config)
				// The release runs what a deploy of it runs: one replica unless it is eligible.
				replicas = services.EffectiveReplicas(sv.Kind, snap.Config, len(snap.Volumes), replicas)
			}
		}
		mem, err := networks.Resolve(ctx, r.q, sv.EnvironmentID, serverID, sv.ID, snap.Config.BlockEgress, snap.Networks)
		if err != nil {
			return out, err
		}
		ds := agentprotocol.DesiredService{
			ServiceID:      sv.ID.String(),
			DeploymentID:   sv.CurrentDeploymentID.String(),
			Replicas:       replicas,
			ContainerNames: agentprotocol.ContainerNames(sv.Slug, sv.CurrentDeploymentID.String(), replicas),
			Routes:         bySvc[sv.ID.String()],

			BlockEgress: mem.BlockEgress, SharedNetworks: mem.SharedNetworks, EnvHasEgressBlocked: mem.EnvHasEgressBlocked,
		}
		if sv.LastScaleAt != nil {
			ds.ScaledAt = *sv.LastScaleAt
		}
		if snap.Config.HasProcesses() && sv.Kind != services.KindStack {
			// The release runs several processes: the agent converges each on its own
			// replica count; the top-level fields mirror the default process.
			ds.Processes = desiredProcesses(sv, snap, row.ProcessReplicas, row.ProcessScaledAt)
			def := ds.Processes[0]
			ds.Replicas, ds.ContainerNames, ds.ScaledAt = def.Replicas, def.ContainerNames, def.ScaledAt
		}
		if sv.Kind == services.KindStack {
			// The agent finds a stack's containers by service and deployment label.
			ds.Stack, ds.Replicas, ds.ContainerNames = true, 1, nil
		}
		out.Services = append(out.Services, ds)
	}
	for _, rt := range routes {
		if !regular[routeOwner(rt)] {
			out.ExternalRoutes = append(out.ExternalRoutes, rt)
		}
	}
	if out.Proxy, err = proxyserver.BuildConfig(ctx, r.q, serverID); err != nil {
		return out, err
	}
	// Basic-auth hashes and certificate keys ride in the (never persisted)
	// reconcile frame; the agent resolves them before rendering.
	if out.Secrets, err = r.deps.ProxySecretsForServer(ctx, serverID, out.Proxy, routes); err != nil {
		return out, err
	}
	if r.geo != nil {
		cfg, secrets, err := r.geo.GeoConfigFor(ctx, serverID)
		if err != nil {
			return out, err
		}
		out.Geo = cfg
		if len(secrets) > 0 && out.Secrets == nil {
			out.Secrets = make(map[string]string, len(secrets))
		}
		for k, v := range secrets {
			out.Secrets[k] = v
		}
	}
	if r.logs != nil {
		cfg, secrets, err := r.logs.LogsConfigFor(ctx, serverID)
		if err != nil {
			return out, err
		}
		out.Logs = cfg
		if len(secrets) > 0 && out.Secrets == nil {
			out.Secrets = make(map[string]string, len(secrets))
		}
		for k, v := range secrets {
			out.Secrets[k] = v
		}
	}
	if r.uptime != nil {
		cfg, err := r.uptime.UptimeConfigFor(ctx, serverID)
		if err != nil {
			return out, err
		}
		out.Uptime = cfg
	}
	return out, nil
}

// desiredProcesses lists the processes of a release that runs several, the
// default process first. The replica count of a process is its stored setting
// as the release can run it (volumes, singleton): a process without a scale row
// runs one replica.
func desiredProcesses(sv db.Service, snap services.Snapshot, replicasRaw, scaledAtRaw json.RawMessage) []agentprotocol.DesiredProcess {
	replicas, scaledAt := domains.DecodeProcessReplicas(replicasRaw), domains.DecodeProcessScaledAt(scaledAtRaw)
	dep := sv.CurrentDeploymentID.String()
	var out []agentprotocol.DesiredProcess
	for _, p := range snap.Config.EffectiveProcesses() {
		want := 1
		var at time.Time
		if p.Default {
			want = max(int(sv.Replicas), 1)
			if sv.LastScaleAt != nil {
				at = *sv.LastScaleAt
			}
		} else {
			if n, ok := replicas[p.Name]; ok {
				want = n
			}
			at = scaledAt[p.Name]
		}
		n := services.EffectiveProcessReplicas(sv.Kind, snap.Config, len(snap.Volumes), p, want)
		out = append(out, agentprotocol.DesiredProcess{
			Name: p.Name, Default: p.Default, Replicas: n, ScaledAt: at,
			ContainerNames: agentprotocol.ProcessContainerNames(sv.Slug, dep, p.Name, p.Default, n),
		})
	}
	return out
}

// replicaScope is the part of a replica's alert key before the index: the compose
// component of a stack, the process of a multi-process service, else empty.
func replicaScope(rep agentprotocol.ObservedReplica) string {
	if rep.Component != "" {
		return rep.Component
	}
	return rep.Process
}

// HandleObserved stores the observed state of each reported service and
// schedules recovery for current deployments whose container is missing.
func (r *Reconciler) HandleObserved(ctx context.Context, serverID uuid.UUID, obs agentprotocol.Observed) error {
	var firstErr error
	for _, o := range obs.Services {
		if err := r.observeOne(ctx, serverID, o); err != nil {
			r.logger.ErrorContext(ctx, "handle observed service", "service_id", o.ServiceID, "server_id", serverID.String(), "err", err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

func (r *Reconciler) observeOne(ctx context.Context, serverID uuid.UUID, o agentprotocol.ObservedService) error {
	svcID, err := uuid.Parse(o.ServiceID)
	if err != nil {
		return nil
	}
	sc, err := services.LoadScopeInternal(ctx, r.q, svcID)
	if err != nil {
		return nil // unknown or deleted service
	}
	sv := sc.Service
	if sv.CurrentDeploymentID == nil {
		return nil
	}
	if o.DeploymentID != "" && o.DeploymentID != sv.CurrentDeploymentID.String() {
		return nil // a container of another deployment; not the one we track
	}
	cur, err := r.q.ListDeploymentsByIDs(ctx, []uuid.UUID{*sv.CurrentDeploymentID})
	if err != nil {
		return fmt.Errorf("current deployment: %w", err)
	}
	if len(cur) != 1 || cur[0].ServerID == nil || *cur[0].ServerID != serverID {
		return nil // reported by a server that does not run the service
	}

	view := services.ObservedView{
		State: o.State, Health: o.Health, ContainerID: o.ContainerID, ImageID: o.ImageID, DeploymentID: sv.CurrentDeploymentID.String(),
	}
	if !o.StartedAt.IsZero() {
		t := o.StartedAt
		view.StartedAt = &t
	}
	view.ReplicasReady, view.ReplicasDesired = o.ReplicasReady, o.ReplicasDesired
	view.Replicas = make([]services.ObservedReplicaView, 0, len(o.Replicas))
	for _, rep := range o.Replicas {
		rv := services.ObservedReplicaView{
			Index: rep.Index, ContainerID: rep.ContainerID, State: rep.State, Health: rep.Health, RestartCount: rep.RestartCount,
			Component: rep.Component, Process: rep.Process,
		}
		if !rep.StartedAt.IsZero() {
			t := rep.StartedAt
			rv.StartedAt = &t
		}
		view.Replicas = append(view.Replicas, rv)
	}
	view.Processes = make([]services.ObservedProcessView, 0, len(o.Processes))
	for _, p := range o.Processes {
		view.Processes = append(view.Processes, services.ObservedProcessView{
			Name: p.Name, State: p.State, Health: p.Health, ReplicasReady: p.ReplicasReady, ReplicasDesired: p.ReplicasDesired,
		})
	}

	if o.State == agentprotocol.StateMissing || anyProcessMissing(o.Processes) {
		msg, err := r.recover(ctx, sc, cur[0])
		if err != nil {
			return err
		}
		view.Error = msg
	}

	prev := services.DecodeObserved(sv.Observed)
	raw, err := json.Marshal(view)
	if err != nil {
		return err
	}
	now := r.now()
	if err := r.q.SetServiceObserved(ctx, db.SetServiceObservedParams{ID: svcID, Observed: raw, ObservedAt: &now}); err != nil {
		return fmt.Errorf("store observed: %w", err)
	}
	if prev == nil || prev.State != view.State || prev.Health != view.Health || prev.Error != view.Error || prev.ContainerID != view.ContainerID ||
		prev.ReplicasReady != view.ReplicasReady || prev.ReplicasDesired != view.ReplicasDesired || replicasChanged(prev.Replicas, view.Replicas) ||
		processesChanged(prev.Processes, view.Processes) {
		services.PublishService(r.bus, services.EventServiceUpdated, sc)
	}
	r.alertOn(ctx, sc, o, now)
	return nil
}

// replicasChanged reports whether the per-replica state or health differs.
func replicasChanged(a, b []services.ObservedReplicaView) bool {
	if len(a) != len(b) {
		return true
	}
	for i := range a {
		if a[i].Index != b[i].Index || a[i].Component != b[i].Component || a[i].Process != b[i].Process || a[i].State != b[i].State || a[i].Health != b[i].Health || a[i].ContainerID != b[i].ContainerID {
			return true
		}
	}
	return false
}

// processesChanged reports whether the per-process aggregates differ.
func processesChanged(a, b []services.ObservedProcessView) bool {
	return !slices.Equal(a, b)
}

// anyProcessMissing reports whether every container of some process vanished
// while others still run: the service aggregate then says running, but the
// process needs the whole-service redeploy.
func anyProcessMissing(ps []agentprotocol.ObservedProcess) bool {
	return slices.ContainsFunc(ps, func(p agentprotocol.ObservedProcess) bool { return p.State == agentprotocol.StateMissing })
}

// recover handles a missing container of a current deployment. It returns the
// node error text when the redeploy limit is exhausted.
func (r *Reconciler) recover(ctx context.Context, sc services.Scope, cur db.Deployment) (string, error) {
	active, err := r.q.CountActiveDeployments(ctx, sc.Service.ID)
	if err != nil {
		return "", fmt.Errorf("count active deployments: %w", err)
	}
	if active > 0 {
		return "", nil // a deployment is already replacing it
	}
	since := r.now().Add(-redeployWindow)
	n, err := r.q.CountReconcileDeploymentsSince(ctx, db.CountReconcileDeploymentsSinceParams{ServiceID: sc.Service.ID, CreatedAt: since})
	if err != nil {
		return "", fmt.Errorf("count reconcile deployments: %w", err)
	}
	if n >= MaxRedeploysPerHour {
		seen, err := r.q.CountRecentReconcileLimitEvents(ctx, db.CountRecentReconcileLimitEventsParams{ResourceID: sc.Service.ID.String(), CreatedAt: since})
		if err != nil {
			return "", fmt.Errorf("count limit events: %w", err)
		}
		if seen == 0 {
			err := db.InTx(ctx, r.pool, func(tx pgx.Tx) error {
				return audit.Record(ctx, tx, audit.Entry{
					Action: "reconcile.limit_reached", ResourceType: "service", ResourceID: sc.Service.ID.String(),
					ProjectID: &sc.ProjectID, WorkspaceID: sc.WorkspaceID,
					Metadata: map[string]any{"service": sc.Service.Slug, "attempts": n, "windowMinutes": int(redeployWindow.Minutes())},
				})
			})
			if err != nil {
				return "", fmt.Errorf("audit reconcile limit: %w", err)
			}
			r.logger.WarnContext(ctx, "reconcile limit reached", "service", sc.Service.Slug, "service_id", sc.Service.ID.String())
		}
		return LimitMessage, nil
	}
	err = r.jm.Insert(ctx, RedeployArgs{ServiceID: sc.Service.ID}, &river.InsertOpts{
		MaxAttempts: 3,
		UniqueOpts: river.UniqueOpts{
			ByArgs: true,
			ByState: []rivertype.JobState{
				rivertype.JobStateAvailable, rivertype.JobStatePending, rivertype.JobStateRetryable,
				rivertype.JobStateRunning, rivertype.JobStateScheduled,
			},
		},
	})
	if err != nil {
		return "", fmt.Errorf("enqueue reconcile redeploy: %w", err)
	}
	return "", nil
}

// RedeployArgs redeploys the current release of a service whose container is missing.
type RedeployArgs struct {
	ServiceID uuid.UUID `json:"serviceId"`
}

// Kind implements river.JobArgs.
func (RedeployArgs) Kind() string { return "reconcile_redeploy" }

type redeployWorker struct {
	river.WorkerDefaults[RedeployArgs]
	r *Reconciler
}

// RegisterJobs registers the worker that runs RedeployArgs.
func RegisterJobs(jm *jobs.Manager, r *Reconciler) {
	jm.AddWorker(func(w *river.Workers) { river.AddWorker(w, &redeployWorker{r: r}) })
}

func (w *redeployWorker) Work(ctx context.Context, job *river.Job[RedeployArgs]) error {
	return w.r.redeploy(ctx, job.Args.ServiceID)
}

// redeploy starts a `reconcile` deployment of the service's current release.
func (r *Reconciler) redeploy(ctx context.Context, serviceID uuid.UUID) error {
	sc, err := services.LoadScopeInternal(ctx, r.q, serviceID)
	if err != nil || sc.Service.CurrentDeploymentID == nil {
		return nil
	}
	if active, err := r.q.CountActiveDeployments(ctx, serviceID); err != nil || active > 0 {
		return err
	}
	n, err := r.q.CountReconcileDeploymentsSince(ctx, db.CountReconcileDeploymentsSinceParams{ServiceID: serviceID, CreatedAt: r.now().Add(-redeployWindow)})
	if err != nil {
		return err
	}
	if n >= MaxRedeploysPerHour {
		return nil
	}
	cur, err := r.q.ListDeploymentsByIDs(ctx, []uuid.UUID{*sc.Service.CurrentDeploymentID})
	if err != nil {
		return err
	}
	if len(cur) != 1 || cur[0].ReleaseID == nil || cur[0].ServerID == nil {
		return nil
	}
	_, err = r.deps.StartRedeploy(ctx, sc, *cur[0].ReleaseID, *cur[0].ServerID)
	var ae *apierr.Error
	if errors.As(err, &ae) && ae.Code == "deployment_in_progress" {
		return nil
	}
	return err
}
