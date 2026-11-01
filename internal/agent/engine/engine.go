// Package engine is the agent's deployment engine: it executes control-plane
// commands (deploy, restart, stop, remove, route sync, log subscriptions)
// against the local Docker daemon and Traefik proxy, and reconciles the host to
// the control plane's desired state (ADR 0003/0004).
package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nebulactrl/nebulactrl/internal/agent/build"
	"github.com/nebulactrl/nebulactrl/internal/agent/compose"
	"github.com/nebulactrl/nebulactrl/internal/agent/decommission"
	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/internal/agent/health"
	"github.com/nebulactrl/nebulactrl/internal/agent/logs"
	"github.com/nebulactrl/nebulactrl/internal/agent/mesh"
	"github.com/nebulactrl/nebulactrl/internal/agent/netpool"
	"github.com/nebulactrl/nebulactrl/internal/agent/tailnet"
	"github.com/nebulactrl/nebulactrl/internal/agent/tunnel"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Sender emits agent → control-plane frames. *conn.Client implements it.
type Sender interface {
	Event(agentprotocol.EventFrame)
	Log(agentprotocol.LogFrame)
	LogEnd(agentprotocol.LogEnd)
	Stats(agentprotocol.Stats)
	Observed(agentprotocol.Observed)
}

// Proxy is the part of proxy.Manager the engine uses.
type Proxy interface {
	Apply(routes []agentprotocol.Route) error
	Current() []agentprotocol.Route
	Probe(ctx context.Context, host, path string) (int, error)
	SetACMEEmail(email string)
}

// Builder builds git sources into local images. *build.Builder implements it.
type Builder interface {
	Build(ctx context.Context, req build.Request, logf func(string)) (imageID string, err error)
}

// HealthFunc waits for a container to become healthy.
type HealthFunc func(ctx context.Context, t health.Target) health.Result

// UptimeRecorder takes the results of one uptime probe cycle for durable
// delivery to the control plane. *uptime.Collector implements it.
type UptimeRecorder interface {
	Record(results []agentprotocol.ProbeResult)
}

// Config configures an Engine. Docker, Proxy and Sender are required.
type Config struct {
	Docker  docker.Engine
	Proxy   Proxy
	Builder Builder // nil disables git builds
	Sender  Sender
	Logger  *slog.Logger

	// Health defaults to health.Wait.
	Health HealthFunc
	// DrainDelay is the wait between re-routing and stopping the old container
	// (default 2s).
	DrainDelay time.Duration
	// VerifyTries and VerifyInterval control post-route verification
	// (defaults 3 and 2s).
	VerifyTries    int
	VerifyInterval time.Duration
	// OrphanMinAge is how old an unreferenced managed container must be before
	// the sweep removes it (default 10m).
	OrphanMinAge time.Duration
	// NetworkMinAge is how old an empty managed network must be before the
	// sweep removes it (default 60s): a deploy creates the network a moment
	// before its first container, so a younger one may be about to be used.
	NetworkMinAge time.Duration
	// Networks allocates the subnets of the networks the engine creates
	// (default: netpool.DefaultPool split into netpool.DefaultPrefixLen).
	Networks *netpool.Allocator
	// AgentContainer names (or IDs) the container the agent itself runs in
	// (NEBULACTRL_AGENT_CONTAINER). Empty means: detect it from Hostname.
	AgentContainer string
	// Hostname is the process hostname (HOSTNAME); inside a container it starts
	// the container's ID. A hostname that matches no container ID means the
	// agent runs on the bare host and needs no network attachment to probe.
	Hostname string
	// Dial opens the tcp connection a route-only deployment uses to verify a
	// tcp route (default net.Dialer); tests replace it.
	Dial func(ctx context.Context, network, addr string) (net.Conn, error)
	// BackupDir is the host directory local backup destinations live under
	// (empty disables local destinations).
	BackupDir string
	// Geo, when set, receives the control plane's request-origin geolocation
	// setting with every reconcile (*traffic.GeoManager implements it).
	Geo GeoApplier
	// Endpoints, when set, receives the instance service listeners the control
	// plane asks for (localproxy.Manager implements it). Unlike everything
	// else a reconcile does it is applied on a build server too: build
	// servers push images to the built-in registry through it.
	Endpoints EndpointApplier
	// Tunnel manages the Cloudflare Tunnel connector. The agent passes the
	// manager its heartbeat reads from; nil makes the engine create its own.
	Tunnel *tunnel.Manager
	// Mesh manages the server mesh nodes. The agent passes the manager its
	// heartbeat reads from; nil makes the engine create its own.
	Mesh *mesh.Manager
	// Logs serves log history queries and drain tests and receives the log
	// configuration (nil: those commands fail).
	Logs LogsService
	// CleanupTune, when set, adjusts the Docker cleanup before each run. Tests
	// use it to move the clock and to scope the cleanup to their own images on
	// a daemon they share; the agent leaves it nil.
	CleanupTune CleanupTuner
	// Compose runs compose stacks (docker compose). nil, or a host without the
	// plugin, makes stack deploys fail.
	Compose compose.Runner
	// StackDir is the directory the stacks' compose files live under
	// (<state-dir>/stacks); each stack has <StackDir>/<serviceId8>/.
	StackDir string
	// BuildOnly makes the engine serve a build server that hosts no workloads
	// and runs no proxy: Proxy may be nil, Reconcile does nothing (so no sweep
	// ever touches containers or networks of other agents on a shared daemon)
	// and Run starts no periodic loops.
	BuildOnly bool
	// RegistryHTTP is the HTTP client of registry checks (nil: a default
	// client). Tests inject one.
	RegistryHTTP *http.Client
	// Uptime receives the results of the periodic uptime probes (every
	// UptimeInterval). nil disables uptime probing.
	Uptime UptimeRecorder
	// UptimeInterval is the probe period (--uptime-interval,
	// NEBULACTRL_UPTIME_INTERVAL: a test and diagnostic knob); zero means
	// agentprotocol.UptimeInterval.
	UptimeInterval time.Duration
	// StopProxy, when set, stops the loop that repairs the proxy container. A
	// decommission calls it before it removes the proxy, so it is not started
	// again. cmd/agent passes the cancel of the proxy manager's context.
	StopProxy func()
	// DecommissionTune, when set, adjusts a decommission before it runs. Tests
	// use it to scope the removal to their own resources on a daemon they share
	// with the developer; the agent leaves it nil.
	DecommissionTune func(*decommission.Config)
}

// Engine executes commands. It implements conn.Handler and conn.Reconciler.
type Engine struct {
	cfg   Config
	log   *slog.Logger
	subs  *logs.Subscriptions
	tn    *tailnet.Manager // per-service tailnet sidecars
	mesh  *mesh.Manager    // server mesh nodes (server node + link nodes)
	tun   *tunnel.Manager  // Cloudflare Tunnel connector
	tunMu sync.Mutex       // serializes the connector's mutating commands

	an agentNet // the agent's own container and the networks it joined to probe

	up agentUptime // uptime probing: which services are switched off

	locks sync.Map // serviceID -> *sync.Mutex

	// routeMu serializes route-table changes: a deploy holds it from applying
	// its route through verification/restore, and reconcile/sync/remove take it
	// for their Apply, so a stale table can never overwrite an in-flight one.
	routeMu sync.Mutex

	// tabSeq is the number of the newest whole route table applied (tableseq.go).
	tabSeq tableSeqState

	mu       sync.Mutex
	inflight map[string]int          // serviceID -> running mutating commands
	desired  map[string]desiredEntry // last known desired set (control plane + local deploys)
	local    map[string]desiredEntry // latest successful local deploys

	// bg tracks work that outlives the command or frame that started it
	// (replica convergence, mesh syncs, fencing); Run waits for it.
	bg sync.WaitGroup
	// life ends when Run returns: work started with background ends with it.
	life     context.Context
	stopLife context.CancelFunc

	// stream carries interactive stream frames (terminal, file browser); nil
	// when Config.Sender cannot (HandleStream then ignores stream frames).
	stream StreamSender
	term   termState
	vol    volState // volume backups (volbackup.go)

	// cancel is the registry of running deploys a cancel_deploy command aborts.
	cancel cancelState

	// builds counts running builds; Docker cleanup does not prune while it is
	// non-zero.
	builds buildActivity

	// repl holds the replication poll requests, their last results and the
	// fences of the control plane's reconcile frame (replication.go).
	repl replState

	// decom is set while a decommission runs and after it succeeded: nothing
	// is deployed, reconciled or recreated on a host that is being emptied.
	decom atomic.Bool
}

// desiredEntry is what the agent believes should be running for a service.
type desiredEntry struct {
	DeploymentID string
	// Replicas is the desired replica count; ContainerNames lists the
	// containers of replicas 0..Replicas-1. With Processes they mirror the
	// default process.
	Replicas       int
	ContainerNames []string
	Routes         []agentprotocol.Route
	// ScaledAt orders the replica count against the control plane's frames:
	// the ScaledAt of the scale_service command (or frame) it came from, zero
	// for a deploy. A frame whose ScaledAt is older than this carries a count
	// the control plane has since replaced (see scaleSuperseded).
	ScaledAt time.Time
	// Stack marks a compose stack: its containers are found by service and
	// deployment label (ContainerNames stays empty), one per component.
	Stack bool
	// Processes are the processes of a multi-process deployment, each with
	// its own replica set. Empty for a single-process service (see procs).
	Processes []desiredProcess
}

// desiredProcess is the replica set of one process of a multi-process service.
type desiredProcess struct {
	Name           string
	Default        bool
	Replicas       int
	ContainerNames []string
	ScaledAt       time.Time
}

// procs lists the entry's processes; a single-process service is one process
// with an empty name (its containers carry no process label).
func (ent desiredEntry) procs() []desiredProcess {
	if len(ent.Processes) > 0 {
		return ent.Processes
	}
	return []desiredProcess{{Default: true, Replicas: ent.Replicas, ContainerNames: ent.ContainerNames, ScaledAt: ent.ScaledAt}}
}

// proc finds a process by name; "" is the default process.
func (ent desiredEntry) proc(name string) (desiredProcess, bool) {
	for _, p := range ent.procs() {
		if p.Name == name || (name == "" && p.Default) {
			return p, true
		}
	}
	return desiredProcess{}, false
}

// mirrorDefault sets the top-level replica fields from the default process,
// which is what everything that predates processes reads.
func (ent *desiredEntry) mirrorDefault() {
	if len(ent.Processes) == 0 {
		return
	}
	lead := ent.Processes[0]
	for _, p := range ent.Processes {
		if p.Default {
			lead = p
			break
		}
	}
	ent.Replicas, ent.ContainerNames, ent.ScaledAt = lead.Replicas, lead.ContainerNames, lead.ScaledAt
}

// withProcess returns the entry with process p replaced (or added): a scale of
// one process leaves the others as they are.
func (ent desiredEntry) withProcess(p desiredProcess) desiredEntry {
	procs := slices.Clone(ent.Processes)
	if i := slices.IndexFunc(procs, func(x desiredProcess) bool { return x.Name == p.Name }); i >= 0 {
		procs[i] = p
	} else {
		procs = append(procs, p)
	}
	ent.Processes = procs
	ent.mirrorDefault()
	return ent
}

// owns reports whether container c is one of the entry's replicas: named in
// ContainerNames (of any process), or - when the entry names none - of its
// deployment. A container of the same deployment under another name (a replica
// beyond the desired count, or one named without a replica index) is not
// owned. The orphan sweep relies on this: a process container it did not
// recognise would be deleted.
func (ent desiredEntry) owns(c docker.ContainerInfo) bool {
	named := false
	for _, p := range ent.procs() {
		if len(p.ContainerNames) > 0 {
			named = true
			if indexOfName(p.ContainerNames, c.Name) >= 0 {
				return true
			}
		}
	}
	if named {
		return false
	}
	return ent.DeploymentID != "" && c.Labels[agentprotocol.LabelDeployment] == ent.DeploymentID
}

// New builds an Engine.
func New(cfg Config) (*Engine, error) {
	if cfg.Docker == nil || cfg.Sender == nil || (cfg.Proxy == nil && !cfg.BuildOnly) {
		return nil, fmt.Errorf("engine: Docker, Proxy and Sender are required")
	}
	if cfg.Proxy == nil {
		cfg.Proxy = noProxy{}
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Health == nil {
		cfg.Health = health.Wait
	}
	if cfg.DrainDelay <= 0 {
		cfg.DrainDelay = 2 * time.Second
	}
	if cfg.VerifyTries <= 0 {
		cfg.VerifyTries = 3
	}
	if cfg.VerifyInterval <= 0 {
		cfg.VerifyInterval = 2 * time.Second
	}
	if cfg.OrphanMinAge <= 0 {
		cfg.OrphanMinAge = 10 * time.Minute
	}
	if cfg.NetworkMinAge <= 0 {
		cfg.NetworkMinAge = 60 * time.Second
	}
	if cfg.Networks == nil {
		var err error
		if cfg.Networks, err = netpool.New(netpool.DefaultPool, netpool.DefaultPrefixLen); err != nil {
			return nil, err
		}
	}
	e := &Engine{
		cfg:      cfg,
		log:      cfg.Logger,
		inflight: map[string]int{},
		desired:  map[string]desiredEntry{},
		local:    map[string]desiredEntry{},
	}
	e.life, e.stopLife = context.WithCancel(context.Background())
	e.subs = logs.NewSubscriptions(cfg.Docker, cfg.Sender, cfg.Logger)
	e.tn = tailnet.New(cfg.Docker, cfg.Logger)
	e.tn.EnsureNetwork = e.ensureNetwork
	if e.mesh = cfg.Mesh; e.mesh == nil {
		e.mesh = mesh.New(cfg.Docker, cfg.Logger)
	}
	e.mesh.EnsureNetwork = e.ensureNetwork
	e.mesh.Access = e.probeAccess
	if cfg.Dial != nil {
		e.mesh.Dial = cfg.Dial
	}
	if e.tun = cfg.Tunnel; e.tun == nil {
		e.tun = tunnel.New(cfg.Docker, cfg.Logger)
	}
	if ss, ok := cfg.Sender.(StreamSender); ok {
		e.stream = ss
	}
	return e, nil
}

// lockService serializes mutating work on one service and marks it in flight.
func (e *Engine) lockService(serviceID string) (unlock func()) {
	v, _ := e.locks.LoadOrStore(serviceID, &sync.Mutex{})
	m := v.(*sync.Mutex)
	m.Lock()
	e.mu.Lock()
	e.inflight[serviceID]++
	e.mu.Unlock()
	return func() {
		e.mu.Lock()
		if e.inflight[serviceID]--; e.inflight[serviceID] <= 0 {
			delete(e.inflight, serviceID)
		}
		e.mu.Unlock()
		m.Unlock()
	}
}

// isInflight reports whether a mutating command is running for the service.
func (e *Engine) isInflight(serviceID string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.inflight[serviceID] > 0
}

// HandleCommand implements conn.Handler.
func (e *Engine) HandleCommand(ctx context.Context, cmd agentprotocol.Command) agentprotocol.ResultFrame {
	if e.decom.Load() && cmd.Kind != agentprotocol.KindDecommission {
		return failedResult(cmd.ID, errDecommissioning)
	}
	switch cmd.Kind {
	case agentprotocol.KindDeploy:
		return e.handleDeploy(ctx, cmd)
	case agentprotocol.KindCancelDeploy:
		return e.handleCancelDeploy(cmd)
	case agentprotocol.KindScaleService:
		return e.handleScale(ctx, cmd)
	case agentprotocol.KindRestartService:
		return e.handleRestart(ctx, cmd)
	case agentprotocol.KindStopService:
		return e.handleStop(ctx, cmd)
	case agentprotocol.KindRemoveService:
		return e.handleRemove(ctx, cmd)
	case agentprotocol.KindSyncRoutes:
		return e.handleSyncRoutes(ctx, cmd)
	case agentprotocol.KindProxyConfigure:
		return e.handleProxyConfigure(ctx, cmd)
	case agentprotocol.KindLogsSubscribe:
		return e.handleLogsSubscribe(ctx, cmd)
	case agentprotocol.KindLogsUnsubscribe:
		return e.handleLogsUnsubscribe(cmd)
	case agentprotocol.KindLogsQuery, agentprotocol.KindLogsDrainTest:
		return e.handleLogsCommand(ctx, cmd)
	case agentprotocol.KindBuildPlan:
		return e.handleBuildPlan(ctx, cmd)
	case agentprotocol.KindEnvScan:
		return e.handleEnvScan(ctx, cmd)
	case agentprotocol.KindDBAdmin:
		return e.handleDBAdmin(ctx, cmd)
	case agentprotocol.KindDBUpgrade:
		return e.handleDBUpgrade(ctx, cmd)
	case agentprotocol.KindDBReplicaClone:
		return e.handleReplicaClone(ctx, cmd)
	case agentprotocol.KindDBBackup:
		return e.handleDBBackup(ctx, cmd)
	case agentprotocol.KindDBRestore:
		return e.handleDBRestore(ctx, cmd)
	case agentprotocol.KindBackupDelete:
		return e.handleBackupDelete(ctx, cmd)
	case agentprotocol.KindBackupTest:
		return e.handleBackupTest(ctx, cmd)
	case agentprotocol.KindRunTask:
		return e.handleRunTask(ctx, cmd)
	case agentprotocol.KindSyncTailnet:
		return e.handleSyncTailnet(ctx, cmd)
	case agentprotocol.KindRemoveTailnet:
		return e.handleRemoveTailnet(ctx, cmd)
	case agentprotocol.KindSyncTunnel:
		return e.handleSyncTunnel(ctx, cmd)
	case agentprotocol.KindRemoveTunnel:
		return e.handleRemoveTunnel(ctx, cmd)
	case agentprotocol.KindSyncMesh:
		return e.handleSyncMesh(ctx, cmd)
	case agentprotocol.KindRemoveMesh:
		return e.handleRemoveMesh(ctx, cmd)
	case agentprotocol.KindMeshCheck:
		return e.handleMeshCheck(ctx, cmd)
	case agentprotocol.KindDockerCleanup:
		return e.handleDockerCleanup(ctx, cmd)
	case agentprotocol.KindDecommission:
		return e.handleDecommission(ctx, cmd)
	case agentprotocol.KindHostHardening:
		return e.handleHostHardening(ctx, cmd)
	case agentprotocol.KindHostPatching:
		return e.handleHostPatching(ctx, cmd)
	case agentprotocol.KindUpdateControlPlane:
		return e.handleUpdateControlPlane(ctx, cmd)
	case agentprotocol.KindInstanceOp:
		return e.handleInstanceOp(ctx, cmd)
	case agentprotocol.KindBuildImage:
		return e.handleBuildImage(ctx, cmd)
	case agentprotocol.KindRegistryCheck:
		return e.handleRegistryCheck(ctx, cmd)
	case agentprotocol.KindVolumeBackup:
		return e.handleVolumeBackup(ctx, cmd)
	case agentprotocol.KindVolumeForget:
		return e.handleVolumeForget(ctx, cmd)
	case agentprotocol.KindVolumeRestore:
		return e.handleVolumeRestore(ctx, cmd)
	case agentprotocol.KindVolumeRemove:
		return e.handleVolumeRemove(ctx, cmd)
	default:
		return failedResult(cmd.ID, "unsupported command kind: "+cmd.Kind)
	}
}

func failedResult(id, msg string) agentprotocol.ResultFrame {
	data, _ := json.Marshal(agentprotocol.DeployResult{Outcome: agentprotocol.OutcomeFailed, Message: msg})
	return agentprotocol.ResultFrame{CommandID: id, Outcome: agentprotocol.OutcomeFailed, Data: data, Error: msg}
}

func okResult(id, containerID, msg string) agentprotocol.ResultFrame {
	data, _ := json.Marshal(agentprotocol.DeployResult{
		Outcome: agentprotocol.OutcomeSucceeded, ContainerID: containerID, Message: msg,
	})
	return agentprotocol.ResultFrame{CommandID: id, Outcome: agentprotocol.OutcomeSucceeded, Data: data}
}

// ResultSent implements conn.ResultObserver. The control plane clears a
// service's observed state when a deploy succeeds and ignores observations
// that arrive before it; reporting right behind the result makes the new
// replicas visible (terminal, status) within moments instead of at the next
// periodic report.
func (e *Engine) ResultSent(ctx context.Context, cmd agentprotocol.Command, res agentprotocol.ResultFrame) {
	if cmd.Kind == agentprotocol.KindDeploy && res.Outcome == agentprotocol.OutcomeSucceeded {
		e.reportObserved(ctx)
		e.refreshMesh(ctx)
	}
}

func deployResultFrame(id string, r agentprotocol.DeployResult) agentprotocol.ResultFrame {
	data, _ := json.Marshal(r)
	f := agentprotocol.ResultFrame{CommandID: id, Outcome: r.Outcome, Data: data}
	if r.Outcome != agentprotocol.OutcomeSucceeded {
		f.Error = r.Message
	}
	return f
}

// detached returns a context for cleanup that survives cancellation of ctx.
func detached(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), d)
}

// background returns a context for work tracked in e.bg: it outlives ctx (the
// command or frame that started the work) but ends when the engine stops, so
// a stopping agent never goes on changing the host. d > 0 bounds it as well.
func (e *Engine) background(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	c, cancel := context.WithCancel(context.WithoutCancel(ctx))
	if d > 0 {
		c, cancel = context.WithTimeout(context.WithoutCancel(ctx), d)
	}
	stop := context.AfterFunc(e.life, cancel)
	return c, func() {
		stop()
		cancel()
	}
}

func graceOf(seconds int) time.Duration {
	if seconds <= 0 {
		seconds = 30
	}
	return time.Duration(seconds) * time.Second
}
