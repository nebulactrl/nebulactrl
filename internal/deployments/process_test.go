package deployments

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/domains"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// processesConfig is a service with the default process "web" (port 3000) and the
// processes api (port 4000), worker (no port, own stop grace) and clock (a
// singleton). The service-level memory, CPUs and stop grace are inherited.
func processesConfig() services.Config {
	return services.Config{
		Image: "traefik/whoami:latest", Port: 3000, StopGraceSeconds: 45, MemoryMB: 256, CPUs: 0.5,
		Processes: []services.ProcessConfig{
			{Name: "worker", StartCommand: []string{"bin/worker"}, StopGraceSeconds: 120},
			{Name: "clock", StartCommand: []string{"bin/clock"}, Singleton: true},
			{Name: "api", StartCommand: []string{"bin/api"}, Port: 4000},
		},
	}.WithDefaults()
}

// keepProcesses is cfg with only the named processes.
func keepProcesses(cfg services.Config, names ...string) services.Config {
	var keep []services.ProcessConfig
	for _, p := range cfg.Processes {
		if slices.Contains(names, p.Name) {
			keep = append(keep, p)
		}
	}
	cfg.Processes = keep
	return cfg.WithDefaults()
}

func configJSON(t *testing.T, cfg services.Config) string {
	t.Helper()
	raw, err := json.Marshal(cfg)
	require.NoError(t, err)
	return string(raw)
}

func specNames(specs []agentprotocol.ProcessSpec) []string {
	names := make([]string, len(specs))
	for i, s := range specs {
		names[i] = s.Name
	}
	return names
}

func specOf(t *testing.T, specs []agentprotocol.ProcessSpec, name string) agentprotocol.ProcessSpec {
	t.Helper()
	for _, s := range specs {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("no process %q in %v", name, specNames(specs))
	return agentprotocol.ProcessSpec{}
}

func TestBuildProcessSpecsOrderReplicasAndInheritance(t *testing.T) {
	cfg := processesConfig()
	rows := []db.ServiceProcessScale{{Process: "worker", Replicas: 3}, {Process: "clock", Replicas: 4}, {Process: "api", Replicas: 2}}

	specs := buildProcessSpecs(services.KindApp, cfg, 0, 2, rows, nil)
	require.Equal(t, []string{"web", "api", "clock", "worker"}, specNames(specs), "processes with a port first (default first), then the rest by name")

	web, api, clock, worker := specOf(t, specs, "web"), specOf(t, specs, "api"), specOf(t, specs, "clock"), specOf(t, specs, "worker")
	require.True(t, web.Default)
	require.False(t, api.Default)
	require.Equal(t, 2, web.Replicas, "the default process takes the service's own setting")
	require.Equal(t, 2, api.Replicas)
	require.Equal(t, 3, worker.Replicas, "a port-less process scales")
	require.Equal(t, 1, clock.Replicas, "a singleton runs one instance whatever its stored count")
	require.Equal(t, agentprotocol.StrategyRolling, web.Strategy)
	require.Equal(t, agentprotocol.StrategyRolling, api.Strategy)
	require.Equal(t, agentprotocol.StrategyRolling, worker.Strategy, "a port-less process of a multi-process service rolls")
	require.Equal(t, agentprotocol.StrategyRecreate, clock.Strategy, "a singleton stops the old instance before the new starts")

	// Stop grace, memory and CPUs come from the service unless the process sets its own.
	require.Equal(t, 45, web.StopGraceSeconds)
	require.Equal(t, 45, api.StopGraceSeconds)
	require.Equal(t, 120, worker.StopGraceSeconds)
	for _, s := range specs {
		require.Equal(t, 256, s.MemoryMB, s.Name)
		require.InDelta(t, 0.5, s.CPUs, 1e-9, s.Name)
	}
	require.Equal(t, []string{"bin/worker"}, worker.StartCommand)
	require.Equal(t, 3000, web.Port)
	require.Equal(t, 4000, api.Port)
	require.Zero(t, worker.Port)

	// Each health check follows its own process' port; a process without one has none.
	require.Equal(t, services.HealthHTTP, web.Healthcheck.Type)
	require.Equal(t, 3000, web.Healthcheck.Port)
	require.Equal(t, 4000, api.Healthcheck.Port)
	require.Equal(t, services.HealthNone, worker.Healthcheck.Type)
}

func TestBuildProcessSpecsVolumesForceSingleInstanceRecreate(t *testing.T) {
	cfg := processesConfig()
	rows := []db.ServiceProcessScale{{Process: "worker", Replicas: 3}, {Process: "api", Replicas: 2}}
	for _, s := range buildProcessSpecs(services.KindApp, cfg, 1, 5, rows, nil) {
		require.Equal(t, 1, s.Replicas, s.Name+": a service with volumes runs every process once")
		require.Equal(t, agentprotocol.StrategyRecreate, s.Strategy, s.Name)
	}
}

func TestBuildProcessSpecsInheritsDefaultStopGraceWhenUnset(t *testing.T) {
	cfg := services.Config{
		Image: "x", Port: 3000,
		Processes: []services.ProcessConfig{{Name: "worker", StartCommand: []string{"bin/worker"}}},
	}.WithDefaults()
	specs := buildProcessSpecs(services.KindApp, cfg, 0, 1, nil, nil)
	for _, s := range specs {
		require.Equal(t, services.DefaultStopGraceSeconds, s.StopGraceSeconds, s.Name+": the effective grace is never zero")
	}
	require.Equal(t, 1, specOf(t, specs, "worker").Replicas, "no scale row: one replica")
}

func TestMirrorDefaultProcessFillsTheLegacyFields(t *testing.T) {
	cfg := processesConfig()
	routing := map[string]processRouting{
		"web": {Hosts: []string{"shop.example.com"}, Rules: []agentprotocol.DomainRule{{Host: "shop.example.com"}}},
		"api": {Hosts: []string{"api.example.com"}, Rules: []agentprotocol.DomainRule{{Host: "api.example.com"}}},
	}
	specs := buildProcessSpecs(services.KindApp, cfg, 0, 2, nil, routing)
	require.Equal(t, []string{"shop.example.com"}, specOf(t, specs, "web").Hosts)
	require.Equal(t, []string{"api.example.com"}, specOf(t, specs, "api").Hosts)
	require.Empty(t, specOf(t, specs, "worker").Hosts, "no domains: no route")

	var pl agentprotocol.DeployPayload
	mirrorDefaultProcess(&pl, specs)
	web := specOf(t, specs, "web")
	require.Equal(t, web.Port, pl.Port)
	require.Equal(t, web.Replicas, pl.Replicas)
	require.Equal(t, web.Strategy, pl.Strategy)
	require.Equal(t, web.StopGraceSeconds, pl.StopGraceSeconds)
	require.Equal(t, web.MemoryMB, pl.MemoryMB)
	require.Equal(t, web.Healthcheck, pl.Healthcheck)
	require.Equal(t, web.Hosts, pl.Hosts)
	require.Equal(t, web.DomainRules, pl.DomainRules)
	require.Len(t, pl.Processes, 4)

	// Every rule of every process is attached its send-time secrets.
	require.Len(t, payloadRules(pl), 2)
}

func TestChoosePlanStrategy(t *testing.T) {
	cfg := processesConfig()
	st, reasons := ChoosePlanStrategy(cfg, 0)
	require.Equal(t, agentprotocol.StrategyRolling, st, "rolling as soon as one process rolls")
	require.Empty(t, reasons)

	st, reasons = ChoosePlanStrategy(cfg, 1)
	require.Equal(t, agentprotocol.StrategyRecreate, st)
	require.Equal(t, []string{ReasonHasVolumes}, reasons)

	// Without processes it is the single-process rule, unchanged.
	single := services.Config{Image: "x", Port: 80}.WithDefaults()
	want, wantReasons := ChooseStrategy(single, 0)
	st, reasons = ChoosePlanStrategy(single, 0)
	require.Equal(t, want, st)
	require.Equal(t, wantReasons, reasons)
}

func TestPlanProcessesOrderAndWillChange(t *testing.T) {
	cfg := processesConfig()
	running := cfg
	running.Processes = append([]services.ProcessConfig(nil), cfg.Processes...)
	for i := range running.Processes {
		if running.Processes[i].Name == "worker" {
			running.Processes[i].StartCommand = []string{"bin/old-worker"}
		}
	}

	got := planProcesses(services.KindApp, cfg, &running, 0, nil, 2, nil)
	require.Len(t, got, 4)
	names := make([]string, len(got))
	for i, p := range got {
		names[i] = p.Name
		require.Equal(t, i+1, p.Order)
		require.Equal(t, p.Name == "web", p.Default)
	}
	require.Equal(t, []string{"web", "api", "clock", "worker"}, names)
	for _, p := range got {
		require.Equal(t, p.Name == "worker", p.WillChange, p.Name+": only the worker's command differs from the running release")
	}
	require.Equal(t, 2, got[0].Replicas)
	require.True(t, got[2].Singleton)
	require.Equal(t, agentprotocol.StrategyRecreate, got[2].Strategy)

	// A change every process shares (a variable) reaches all of them; a domain does not.
	all := planProcesses(services.KindApp, cfg, &cfg, 0, nil, 1, []services.FieldChange{{Field: "variables.API_KEY"}})
	for _, p := range all {
		require.True(t, p.WillChange, p.Name)
	}
	none := planProcesses(services.KindApp, cfg, &cfg, 0, nil, 1, []services.FieldChange{{Field: "domains.shop.example.com"}, {Field: "config.processes.worker.startCommand"}})
	for _, p := range none {
		require.False(t, p.WillChange, p.Name)
	}
	// Before the first deployment everything is new.
	for _, p := range planProcesses(services.KindApp, cfg, nil, 0, nil, 1, nil) {
		require.True(t, p.WillChange, p.Name)
	}
	require.Empty(t, planProcesses(services.KindApp, services.Config{Image: "x", Port: 80}.WithDefaults(), nil, 0, nil, 1, nil), "no processes: an empty list")
}

func TestProcessTimelineListsOnlyProcessesThatDidNotSucceed(t *testing.T) {
	evs := processTimeline([]agentprotocol.ProcessResult{
		{Name: "web", Outcome: "succeeded"},
		{Name: "api", Outcome: "rolled_back", Message: "rolled back after worker failed"},
		{Name: "worker", Outcome: "failed", Message: "health check did not pass"},
		{Name: "clock", Outcome: "skipped", Message: "not started: process worker failed"},
	})
	require.Len(t, evs, 3)
	for _, e := range evs {
		require.Equal(t, "release", e.Step)
	}
	require.Equal(t, "info", evs[0].Status)
	require.Equal(t, "[api] rolled back after worker failed", evs[0].Message)
	require.Equal(t, agentprotocol.StepFailed, evs[1].Status)
	require.Equal(t, "[worker] health check did not pass", evs[1].Message)
	require.Equal(t, "info", evs[2].Status)
}

// allowProcesses sets the facts of the fixture's server: whether its agent
// advertises the processes capability.
func (f *routeFixture) allowProcesses(has bool) {
	f.t.Helper()
	caps := []string{agentprotocol.CapProxyV2}
	if has {
		caps = append(caps, agentprotocol.CapProcesses)
	}
	facts, _ := json.Marshal(agentprotocol.Facts{OS: "linux", Capabilities: caps})
	_, err := f.pool.Exec(context.Background(), "UPDATE servers SET facts = $2 WHERE id = $1", f.server.ID, facts)
	require.NoError(f.t, err)
}

func (f *routeFixture) setScale(id uuid.UUID, process string, replicas int) {
	f.t.Helper()
	_, err := f.pool.Exec(context.Background(),
		`INSERT INTO service_process_scale (service_id, process, replicas) VALUES ($1, $2, $3)
		 ON CONFLICT (service_id, process) DO UPDATE SET replicas = $3`, id, process, replicas)
	require.NoError(f.t, err)
}

func (f *routeFixture) scales(id uuid.UUID) map[string]int {
	f.t.Helper()
	rows, err := f.q.ListProcessScales(context.Background(), id)
	require.NoError(f.t, err)
	out := map[string]int{}
	for _, r := range rows {
		out[r.Process] = int(r.Replicas)
	}
	return out
}

func requireStatusCode(t *testing.T, err error, status int, code string) {
	t.Helper()
	ae, ok := apierr.As(err)
	require.True(t, ok, "want an API error, got %v", err)
	require.Equal(t, status, ae.Status)
	require.Equal(t, code, ae.Code)
}

func (f *routeFixture) deploymentCount(id uuid.UUID) int {
	f.t.Helper()
	var n int
	require.NoError(f.t, f.pool.QueryRow(context.Background(), "SELECT count(*) FROM deployments WHERE service_id = $1", id).Scan(&n))
	return n
}

func TestDeployPayloadCarriesTheProcessesAndTheReleaseKeepsThem(t *testing.T) {
	f := newRouteFixture(t)
	f.allowProcesses(true)
	web := f.web("shop")
	setConfig(t, f, web.ID, configJSON(t, processesConfig()))
	f.setScale(web.ID, "worker", 3)
	f.setScale(web.ID, "clock", 4)
	f.setScale(web.ID, "api", 2)

	// A domain of the default process and one of the api process.
	_, err := f.doms.Create(f.ctx, web.ID, "shop.example.com", 0, domains.RuleInput{})
	require.NoError(t, err)
	apiDom, err := f.doms.CreateForComponent(f.ctx, web.ID, "api.example.com", 0, "api", domains.RuleInput{})
	require.NoError(t, err)
	require.Equal(t, 4000, apiDom.TargetPort, "a process domain targets the process' own port")
	require.Equal(t, "api", apiDom.Process)

	depID, p := f.deployWeb(web.ID)

	require.Equal(t, []string{"web", "api", "clock", "worker"}, specNames(p.Processes))
	web0, api, clock, worker := specOf(t, p.Processes, "web"), specOf(t, p.Processes, "api"), specOf(t, p.Processes, "clock"), specOf(t, p.Processes, "worker")
	require.Equal(t, []string{"shop.example.com"}, web0.Hosts)
	require.Equal(t, []string{"api.example.com"}, api.Hosts, "per-process hosts")
	require.Len(t, api.DomainRules, 1)
	require.Equal(t, "api.example.com", api.DomainRules[0].Host)
	require.Empty(t, worker.Hosts)
	require.Equal(t, 1, clock.Replicas)
	require.Equal(t, 3, worker.Replicas)
	require.Equal(t, 2, api.Replicas)

	// The top-level fields are the default process'.
	require.Equal(t, web0.Hosts, p.Hosts)
	require.Equal(t, web0.Port, p.Port)
	require.Equal(t, web0.Strategy, p.Strategy)
	require.Equal(t, web0.Replicas, p.Replicas)
	require.Equal(t, web0.StopGraceSeconds, p.StopGraceSeconds)
	require.Len(t, p.DomainRules, 1)

	var strategy string
	require.NoError(t, f.pool.QueryRow(context.Background(), "SELECT strategy FROM deployments WHERE id = $1", depID).Scan(&strategy))
	require.Equal(t, agentprotocol.StrategyRolling, strategy, "rolling when any process rolls")

	// One release holds every process; reading it back gives the same set.
	var relCfg json.RawMessage
	require.NoError(t, f.pool.QueryRow(context.Background(),
		"SELECT r.config FROM releases r JOIN deployments d ON d.release_id = r.id WHERE d.id = $1", depID).Scan(&relCfg))
	snap := services.DecodeSnapshot(relCfg)
	require.Equal(t, []string{"web", "api", "clock", "worker"}, snap.Config.ProcessNames())
	got, ok := snap.Config.Process("worker")
	require.True(t, ok)
	require.Equal(t, []string{"bin/worker"}, got.Command)
	require.Equal(t, 120, got.StopGraceSeconds)
	require.Len(t, snap.Domains, 2)
}

func TestApplyProcessesTailnetOnlyClearsEveryRoute(t *testing.T) {
	f := newRouteFixture(t)
	f.allowProcesses(true)
	web := f.web("shop")
	setConfig(t, f, web.ID, configJSON(t, processesConfig()))
	_, err := f.doms.Create(f.ctx, web.ID, "shop.example.com", 0, domains.RuleInput{})
	require.NoError(t, err)
	_, err = f.doms.CreateForComponent(f.ctx, web.ID, "api.example.com", 0, "api", domains.RuleInput{})
	require.NoError(t, err)

	sc, err := services.LoadScopeInternal(context.Background(), f.q, web.ID)
	require.NoError(t, err)
	rows, err := f.q.ListServiceDomains(context.Background(), web.ID)
	require.NoError(t, err)
	cfg := sc.Config()

	var visible agentprotocol.DeployPayload
	require.NoError(t, f.deps.applyProcesses(context.Background(), f.q, sc, f.server.ID, cfg, 0, rows, false, 1, &visible))
	require.NotEmpty(t, visible.Hosts)
	require.NotEmpty(t, specOf(t, visible.Processes, "api").Hosts)

	var hidden agentprotocol.DeployPayload
	require.NoError(t, f.deps.applyProcesses(context.Background(), f.q, sc, f.server.ID, cfg, 0, rows, true, 1, &hidden))
	require.Len(t, hidden.Processes, 4)
	require.Empty(t, hidden.Hosts)
	require.Empty(t, hidden.DomainRules)
	require.Nil(t, hidden.OriginLock)
	for _, s := range hidden.Processes {
		require.Empty(t, s.Hosts, s.Name)
		require.Empty(t, s.DomainRules, s.Name)
		require.Nil(t, s.OriginLock, s.Name)
	}
}

func TestApplyProcessesRoutesADomainOfAStagedRenameToTheNewName(t *testing.T) {
	f := newRouteFixture(t)
	f.allowProcesses(true)
	web := f.web("shop")
	withQueue := services.Config{
		Image: "traefik/whoami:latest", Port: 3000,
		Processes: []services.ProcessConfig{{Name: "queue", StartCommand: []string{"bin/queue"}, Port: 4000}},
	}.WithDefaults()
	setConfig(t, f, web.ID, configJSON(t, withQueue))
	_, err := f.doms.CreateForComponent(f.ctx, web.ID, "queue.example.com", 0, "queue", domains.RuleInput{})
	require.NoError(t, err)

	// The rename is staged: the config names the process worker, the domain keeps queue.
	renamed := services.Config{
		Image: "traefik/whoami:latest", Port: 3000,
		Processes: []services.ProcessConfig{{Name: "worker", StartCommand: []string{"bin/queue"}, Port: 4000}},
	}.WithDefaults()
	setConfig(t, f, web.ID, configJSON(t, renamed))
	require.NoError(t, f.q.UpsertProcessRename(context.Background(), db.UpsertProcessRenameParams{ServiceID: web.ID, FromProcess: "queue", ToProcess: "worker"}))

	sc, err := services.LoadScopeInternal(context.Background(), f.q, web.ID)
	require.NoError(t, err)
	rows, err := f.q.ListServiceDomains(context.Background(), web.ID)
	require.NoError(t, err)
	require.Equal(t, "queue", rows[0].Component, "the domain still names the old process")

	var pl agentprotocol.DeployPayload
	require.NoError(t, f.deps.applyProcesses(context.Background(), f.q, sc, f.server.ID, sc.Config(), 0, rows, false, 1, &pl))
	worker := specOf(t, pl.Processes, "worker")
	require.Equal(t, []string{"queue.example.com"}, worker.Hosts)
	require.Len(t, worker.DomainRules, 1)
	require.Equal(t, "queue.example.com", worker.DomainRules[0].Host)
	require.Empty(t, pl.Hosts, "the default process serves nothing")

	// Without the staged rename the domain matches no process of the config.
	require.NoError(t, f.q.DeleteProcessRename(context.Background(), db.DeleteProcessRenameParams{ServiceID: web.ID, ToProcess: "worker"}))
	var plain agentprotocol.DeployPayload
	require.NoError(t, f.deps.applyProcesses(context.Background(), f.q, sc, f.server.ID, sc.Config(), 0, rows, false, 1, &plain))
	require.Empty(t, specOf(t, plain.Processes, "worker").Hosts)
}

func TestProcessesNeedACapableAgent(t *testing.T) {
	f := newRouteFixture(t)
	web := f.web("shop")
	setConfig(t, f, web.ID, configJSON(t, processesConfig()))
	before := f.deploymentCount(web.ID)

	// Staging was never blocked: only deploying is.
	f.allowProcesses(false)
	_, err := f.deps.Deploy(f.ctx, web.ID, "", "")
	requireStatusCode(t, err, 422, "agent_too_old")
	require.Contains(t, err.Error(), "too old to run a service with several processes")
	require.Equal(t, before, f.deploymentCount(web.ID), "nothing was persisted")

	plan, err := f.deps.Plan(f.ctx, web.ID)
	require.NoError(t, err)
	var codes []string
	for _, b := range plan.Blocking {
		codes = append(codes, b.Code)
	}
	require.Contains(t, codes, "agent_too_old")
	require.Len(t, plan.Processes, 4, "the plan still lists the processes")

	f.allowProcesses(true)
	plan, err = f.deps.Plan(f.ctx, web.ID)
	require.NoError(t, err)
	require.Empty(t, plan.Blocking)
	require.Equal(t, agentprotocol.StrategyRolling, plan.Strategy)
	require.Equal(t, "web", plan.Processes[0].Name)
	require.True(t, plan.Processes[0].Default)
	require.Equal(t, 1, plan.Processes[0].Order)
	for _, p := range plan.Processes {
		require.True(t, p.WillChange, p.Name)
	}

	// A server that never connected has no facts and is assumed current.
	_, err = f.pool.Exec(context.Background(), "UPDATE servers SET facts = NULL WHERE id = $1", f.server.ID)
	require.NoError(t, err)
	plan, err = f.deps.Plan(f.ctx, web.ID)
	require.NoError(t, err)
	require.Empty(t, plan.Blocking)
}

func TestRollbackAndReconcileApplyTheCapabilityGateAgain(t *testing.T) {
	f := newRouteFixture(t)
	f.allowProcesses(true)
	web := f.web("shop")
	setConfig(t, f, web.ID, configJSON(t, processesConfig()))
	first, _ := f.deployWeb(web.ID)
	_, err := f.pool.Exec(context.Background(), "UPDATE services SET generation = generation + 1 WHERE id = $1", web.ID)
	require.NoError(t, err)
	f.deployWeb(web.ID)

	f.allowProcesses(false)

	// Rollback: refused up front, nothing created.
	n := f.deploymentCount(web.ID)
	_, err = f.deps.Rollback(f.ctx, first)
	requireStatusCode(t, err, 422, "agent_too_old")
	require.Equal(t, n, f.deploymentCount(web.ID))

	// Reconcile redeploy of the current release: the deployment fails while preparing.
	sc, err := services.LoadScopeInternal(context.Background(), f.q, web.ID)
	require.NoError(t, err)
	cur, err := f.q.ListDeploymentsByIDs(context.Background(), []uuid.UUID{*sc.Service.CurrentDeploymentID})
	require.NoError(t, err)
	dep, err := f.deps.StartRedeploy(context.Background(), sc, *cur[0].ReleaseID, f.server.ID)
	require.NoError(t, err)
	require.NoError(t, f.deps.runDeploy(context.Background(), dep.ID, ""))
	var status, msg string
	require.NoError(t, f.pool.QueryRow(context.Background(), "SELECT status, status_message FROM deployments WHERE id = $1", dep.ID).Scan(&status, &msg))
	require.Equal(t, string(Failed), status)
	require.Contains(t, msg, "too old to run a service with several processes")

	// With a capable agent the same release redeploys, every process included.
	f.allowProcesses(true)
	dep, err = f.deps.StartRedeploy(context.Background(), sc, *cur[0].ReleaseID, f.server.ID)
	require.NoError(t, err)
	require.NoError(t, f.deps.runDeploy(context.Background(), dep.ID, ""))
	var p agentprotocol.DeployPayload
	require.NoError(t, json.Unmarshal(f.lastCommand(agentprotocol.KindDeploy).Payload, &p))
	require.Len(t, p.Processes, 4)
}

func TestRollbackRevertsEveryProcessToTheTargetRelease(t *testing.T) {
	f := newRouteFixture(t)
	f.allowProcesses(true)
	web := f.web("shop")
	f.setScale(web.ID, "worker", 3)
	setConfig(t, f, web.ID, configJSON(t, processesConfig()))
	first, _ := f.deployWeb(web.ID)

	// The next release drops the clock and renames nothing else.
	cfg := processesConfig()
	cfg = keepProcesses(cfg, "api", "worker")
	setConfig(t, f, web.ID, configJSON(t, cfg.WithDefaults()))
	_, p2 := f.deployWeb(web.ID)
	require.Equal(t, []string{"web", "api", "worker"}, specNames(p2.Processes))

	_, err := f.deps.Rollback(f.ctx, first)
	require.NoError(t, err)
	var rb uuid.UUID
	require.NoError(t, f.pool.QueryRow(context.Background(), `SELECT id FROM deployments WHERE service_id = $1 AND trigger = 'rollback'`, web.ID).Scan(&rb))
	require.NoError(t, f.deps.runDeploy(context.Background(), rb, ""))
	var p3 agentprotocol.DeployPayload
	require.NoError(t, json.Unmarshal(f.lastCommand(agentprotocol.KindDeploy).Payload, &p3))
	require.Equal(t, []string{"web", "api", "clock", "worker"}, specNames(p3.Processes), "the target release' process set, in one deployment")
	require.Equal(t, 3, specOf(t, p3.Processes, "worker").Replicas, "replicas come from the current scale rows")
	require.Nil(t, p3.PreDeploy)
}

func TestFinalizeClampsIneligibleProcessesAndPrunesDroppedOnes(t *testing.T) {
	f := newRouteFixture(t)
	f.allowProcesses(true)
	web := f.web("shop")
	setConfig(t, f, web.ID, configJSON(t, processesConfig()))
	f.setScale(web.ID, "worker", 3)
	f.setScale(web.ID, "clock", 4) // a singleton: ineligible
	f.setScale(web.ID, "gone", 2)  // not in the release
	_, err := f.pool.Exec(context.Background(),
		`UPDATE service_process_scale SET autoscaling = '{"enabled":true,"min":1,"max":3,"targetCpuPercent":70}' WHERE service_id = $1 AND process = 'clock'`, web.ID)
	require.NoError(t, err)

	f.deployWeb(web.ID)

	require.Equal(t, map[string]int{"worker": 3, "clock": 1}, f.scales(web.ID), "the singleton runs one, the dropped process' scale is forgotten, the worker keeps its count")
	rows, err := f.q.ListProcessScales(context.Background(), web.ID)
	require.NoError(t, err)
	require.False(t, services.ProcessScalingOf(rows, "clock").Autoscaling.Enabled, "autoscaling of an ineligible process is switched off")
}

func TestHandleResultWritesOneTimelineEventPerProcessThatDidNotSucceed(t *testing.T) {
	f := newRouteFixture(t)
	f.allowProcesses(true)
	web := f.web("shop")
	setConfig(t, f, web.ID, configJSON(t, processesConfig()))
	v, err := f.deps.Deploy(f.ctx, web.ID, "", "")
	require.NoError(t, err)
	require.NoError(t, f.deps.runDeploy(context.Background(), v.ID, ""))

	data, _ := json.Marshal(agentprotocol.DeployResult{
		Outcome: agentprotocol.OutcomeRolledBack, Message: "Process worker failed: the health check did not pass",
		Processes: []agentprotocol.ProcessResult{
			{Name: "web", Outcome: "rolled_back", Message: "rolled back after worker failed"},
			{Name: "api", Outcome: "rolled_back"},
			{Name: "worker", Outcome: "failed", Message: "the health check did not pass"},
			{Name: "clock", Outcome: "skipped", Message: "not started: process worker failed"},
		},
	})
	res := agentprotocol.ResultFrame{Outcome: agentprotocol.OutcomeRolledBack, Data: data}
	require.NoError(t, f.deps.HandleResult(context.Background(), f.server.ID, uuid.New(), &v.ID, res))
	require.NoError(t, f.deps.HandleResult(context.Background(), f.server.ID, uuid.New(), &v.ID, res), "a redelivered result writes nothing new")

	var status, msg string
	require.NoError(t, f.pool.QueryRow(context.Background(), "SELECT status, status_message FROM deployments WHERE id = $1", v.ID).Scan(&status, &msg))
	require.Equal(t, string(RolledBack), status)
	require.Equal(t, "Process worker failed: the health check did not pass", msg)

	rows, err := f.pool.Query(context.Background(), "SELECT status, message FROM deployment_events WHERE deployment_id = $1 AND step = 'release' ORDER BY id", v.ID)
	require.NoError(t, err)
	defer rows.Close()
	type ev struct{ status, message string }
	var got []ev
	for rows.Next() {
		var e ev
		require.NoError(t, rows.Scan(&e.status, &e.message))
		got = append(got, e)
	}
	require.Equal(t, []ev{
		{"info", "[web] rolled back after worker failed"},
		{"info", "[api] rolled_back"},
		{"failed", "[worker] the health check did not pass"},
		{"info", "[clock] not started: process worker failed"},
	}, got)

	// A failed deployment promotes nothing: the scale rows stay as they were.
	require.Empty(t, f.scales(web.ID))
}

func TestDeployOfAServiceInACombineIsRefusedExceptByTheCombine(t *testing.T) {
	f := newRouteFixture(t)
	web := f.web("shop")
	other := f.web("blog")
	combine, err := f.q.InsertCombine(context.Background(), db.InsertCombineParams{
		EnvironmentID: f.env.ID, TargetServiceID: web.ID, SourceServiceIds: []uuid.UUID{other.ID}, Plan: json.RawMessage(`{}`), CreatedBy: "test",
	})
	require.NoError(t, err)
	before := f.deploymentCount(web.ID)

	_, err = f.deps.Deploy(f.ctx, web.ID, "", "")
	requireStatusCode(t, err, 409, "combine_in_progress")
	_, err = f.deps.Deploy(f.ctx, other.ID, "", "")
	requireStatusCode(t, err, 409, "combine_in_progress")
	_, err = f.deps.DeployWithOptions(f.ctx, other.ID, DeployOptions{ByCombine: combine.ID})
	requireStatusCode(t, err, 409, "combine_in_progress") // the combine's deployment is of its target only
	require.Equal(t, before, f.deploymentCount(web.ID))

	v, err := f.deps.DeployWithOptions(f.ctx, web.ID, DeployOptions{ByCombine: combine.ID, Message: "Combine services: shop, blog"})
	require.NoError(t, err)
	require.Equal(t, web.ID, v.ServiceID)
	require.Equal(t, before+1, f.deploymentCount(web.ID))

	// A batch skips a service in a combine unless it was named.
	_, err = f.pool.Exec(context.Background(), "UPDATE services SET generation = generation + 1 WHERE id = $1", other.ID)
	require.NoError(t, err)
	res, err := f.deps.DeployStaged(f.ctx, f.env.ID, nil, "")
	require.NoError(t, err)
	require.Empty(t, res.Items)
	_, err = f.deps.DeployStaged(f.ctx, f.env.ID, []uuid.UUID{other.ID}, "")
	requireStatusCode(t, err, 409, "combine_in_progress")
}

func TestRevertConfigPathOfProcesses(t *testing.T) {
	released := processesConfig()
	cur := processesConfig()

	// A process added since the release is removed again.
	added := cur
	added.Processes = append(append([]services.ProcessConfig(nil), cur.Processes...), services.ProcessConfig{Name: "mailer", StartCommand: []string{"bin/mailer"}})
	added = added.WithDefaults()
	got, err := RevertConfigPath(added, released, "processes.mailer")
	require.NoError(t, err)
	require.Equal(t, released.ProcessNames(), got.ProcessNames())

	// A process removed since the release comes back as it was.
	removed := cur
	removed.Processes = keepProcesses(cur, "api", "clock").Processes
	removed = removed.WithDefaults()
	got, err = RevertConfigPath(removed, released, "processes.worker")
	require.NoError(t, err)
	require.Equal(t, released.ProcessNames(), got.ProcessNames())
	w, ok := got.Process("worker")
	require.True(t, ok)
	require.Equal(t, []string{"bin/worker"}, w.Command)
	require.Equal(t, 120, w.StopGraceSeconds)

	// One field of a process goes back; the others keep their staged values.
	edited := processesConfig()
	for i := range edited.Processes {
		switch edited.Processes[i].Name {
		case "worker":
			edited.Processes[i].StartCommand = []string{"bin/new-worker"}
			edited.Processes[i].StopGraceSeconds = 10
		case "api":
			edited.Processes[i].Port = 4100
		}
	}
	edited = edited.WithDefaults()
	got, err = RevertConfigPath(edited, released, "processes.worker.startCommand")
	require.NoError(t, err)
	w, _ = got.Process("worker")
	require.Equal(t, []string{"bin/worker"}, w.Command)
	require.Equal(t, 10, w.StopGraceSeconds, "the other field stays staged")
	a, _ := got.Process("api")
	require.Equal(t, 4100, a.Port, "another process is untouched")

	// The default process' own fields and the plain scalars revert like any config field.
	renamed := released
	renamed.ProcessName, renamed.Singleton = "main", true
	got, err = RevertConfigPath(renamed, released, "processName")
	require.NoError(t, err)
	require.Equal(t, "", got.ProcessName)
	require.True(t, got.Singleton)
	got, err = RevertConfigPath(got, released, "singleton")
	require.NoError(t, err)
	require.False(t, got.Singleton)
}

func TestDiscardingAStagedProcessAndAStagedProcessField(t *testing.T) {
	f := newRouteFixture(t)
	f.allowProcesses(true)
	web := f.web("shop")
	base := processesConfig()
	base = keepProcesses(base, "worker")
	setConfig(t, f, web.ID, configJSON(t, base.WithDefaults()))
	f.deployWeb(web.ID)

	// Stage a new process and an edit of the worker's command.
	staged := processesConfig()
	for i := range staged.Processes {
		if staged.Processes[i].Name == "worker" {
			staged.Processes[i].StartCommand = []string{"bin/new-worker"}
		}
	}
	setConfig(t, f, web.ID, configJSON(t, staged.WithDefaults()))
	changes, err := f.deps.PendingChanges(f.ctx, web.ID)
	require.NoError(t, err)
	var fields []string
	for _, c := range changes {
		fields = append(fields, c.Field)
	}
	require.Contains(t, fields, "config.processes.api")
	require.Contains(t, fields, "config.processes.worker.startCommand")

	_, err = f.deps.DiscardPendingChange(f.ctx, web.ID, "config.processes.api")
	require.NoError(t, err)
	_, err = f.deps.DiscardPendingChange(f.ctx, web.ID, "config.processes.clock")
	require.NoError(t, err)
	_, err = f.deps.DiscardPendingChange(f.ctx, web.ID, "config.processes.worker.startCommand")
	require.NoError(t, err)

	sc, err := services.LoadScopeInternal(context.Background(), f.q, web.ID)
	require.NoError(t, err)
	require.Equal(t, []string{"web", "worker"}, sc.Config().ProcessNames())
	w, _ := sc.Config().Process("worker")
	require.Equal(t, []string{"bin/worker"}, w.Command)
	changes, err = f.deps.PendingChanges(f.ctx, web.ID)
	require.NoError(t, err)
	for _, c := range changes {
		require.NotContains(t, c.Field, "processes", "everything staged about processes is discarded")
	}

	// DiscardFields skips what is not pending any more.
	require.NoError(t, f.deps.DiscardFields(f.ctx, web.ID, []string{"config.processes.api", "config.processName"}))
}

func TestDiscardedDomainRemovalRestoresItsProcessAndPort(t *testing.T) {
	f := newRouteFixture(t)
	f.allowProcesses(true)
	web := f.web("shop")
	setConfig(t, f, web.ID, configJSON(t, processesConfig()))
	_, err := f.doms.Create(f.ctx, web.ID, "shop.example.com", 0, domains.RuleInput{})
	require.NoError(t, err)
	apiDom, err := f.doms.CreateForComponent(f.ctx, web.ID, "api.example.com", 0, "api", domains.RuleInput{})
	require.NoError(t, err)
	f.deployWeb(web.ID)

	require.NoError(t, f.doms.Delete(f.ctx, apiDom.ID))
	_, err = f.deps.DiscardPendingChange(f.ctx, web.ID, "domains.api.example.com")
	require.NoError(t, err)

	rows, err := f.q.ListServiceDomains(context.Background(), web.ID)
	require.NoError(t, err)
	var restored *db.Domain
	for i := range rows {
		if rows[i].Hostname == "api.example.com" {
			restored = &rows[i]
		}
	}
	require.NotNil(t, restored, "the domain is back")
	require.Equal(t, "api", restored.Component, "on the process it served")
	require.EqualValues(t, 4000, restored.TargetPort, "with the process' port, not the service port")
}
