package composites_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/services"
)

// processFx seeds a deployed service "app" with the default process web (port
// 80) and the processes worker (no port) and api (port 9000).
func processFx(t *testing.T) (*fx, db.Service) {
	t.Helper()
	f := newFx(t)
	sv := f.service(svcOpts{
		env: f.staging, slug: "app",
		processes: []services.ProcessConfig{
			{Name: "worker", StartCommand: []string{"app", "worker"}},
			{Name: "api", StartCommand: []string{"app", "api"}, Port: 9000},
		},
	})
	return f, sv
}

func processObserved(f *fx, sv db.Service, procs ...map[string]any) {
	f.t.Helper()
	ready, desired := 0, 0
	for _, p := range procs {
		ready += p["replicasReady"].(int)
		desired += p["replicasDesired"].(int)
	}
	health := "healthy"
	if ready < desired {
		health = "degraded"
	}
	verifyObservedJSON(f, sv.ID, map[string]any{
		"state": "running", "health": health, "containerId": "abc", "deploymentId": sv.CurrentDeploymentID.String(),
		"replicasReady": ready, "replicasDesired": desired, "processes": procs,
	})
}

func proc(name string, ready, desired int) map[string]any {
	health := "healthy"
	state := "running"
	if ready == 0 {
		health, state = "unhealthy", "missing"
	} else if ready < desired {
		health = "degraded"
	}
	return map[string]any{"name": name, "state": state, "health": health, "replicasReady": ready, "replicasDesired": desired}
}

func TestVerifyServiceJudgesEveryProcess(t *testing.T) {
	f, sv := processFx(t)
	sel := f.selector(f.staging, "app")

	processObserved(f, sv, proc("web", 1, 1), proc("worker", 2, 2), proc("api", 1, 1))
	m := f.must("verify_service", map[string]any{"service": sel})
	require.Equal(t, "pass", m["result"], "%v", m["reasons"])
	obs := m["observed"].(map[string]any)
	require.Len(t, obs["processes"], 3)

	// One process short of replicas fails the service and the evidence names it.
	processObserved(f, sv, proc("web", 1, 1), proc("worker", 0, 2), proc("api", 1, 1))
	m = f.must("verify_service", map[string]any{"service": sel})
	require.Equal(t, "fail", m["result"])
	require.Contains(t, verifyReasonCodes(m), "process_degraded")
	require.NotContains(t, verifyReasonCodes(m), "replicas_degraded", "the per-process reason replaces the service-wide count")
	var msg string
	for _, r := range explainList(m, "reasons") {
		rm := r.(map[string]any)
		if rm["code"] == "process_degraded" {
			msg = rm["message"].(string)
		}
	}
	require.Contains(t, msg, "worker")
	require.Contains(t, msg, "0 of 2")

	// Partly ready is a warning, not a failure.
	processObserved(f, sv, proc("web", 1, 1), proc("worker", 1, 2), proc("api", 1, 1))
	m = f.must("verify_service", map[string]any{"service": sel})
	require.Equal(t, "warn", m["result"])
	require.Contains(t, verifyReasonCodes(m), "process_degraded")
}

func TestDiagnoseServiceNamesTheProcessThatIsNotReady(t *testing.T) {
	f, sv := processFx(t)
	sel := f.selector(f.staging, "app")
	processObserved(f, sv, proc("web", 1, 1), proc("worker", 0, 2), proc("api", 1, 1))

	m := f.must("diagnose_service", map[string]any{"service": sel})
	require.Equal(t, "degraded", m["verdict"], "%v", m)
	var found map[string]any
	for _, x := range explainList(m, "findings") {
		fm := x.(map[string]any)
		if fm["code"] == "process_not_ready" {
			found = fm
		}
	}
	require.NotNil(t, found, "findings: %v", diagFindingCodes(m))
	require.Equal(t, "critical", found["severity"])
	require.Contains(t, found["title"], "worker")
	require.Contains(t, found["evidence"].([]any)[0], "worker: 0 of 2")
	sug := found["suggest"].(map[string]any)
	require.Equal(t, "tail_logs", sug["tool"])
	require.Equal(t, "worker", sug["args"].(map[string]any)["process"])
	svc := m["service"].(map[string]any)
	require.Len(t, svc["processes"], 3)

	// Every process ready: healthy, no process finding.
	processObserved(f, sv, proc("web", 1, 1), proc("worker", 2, 2), proc("api", 1, 1))
	m = f.must("diagnose_service", map[string]any{"service": sel})
	require.Equal(t, "healthy", m["verdict"])
	require.NotContains(t, diagFindingCodes(m), "process_not_ready")
}

func TestGetOverviewCountsAServiceWithAnUnreadyProcessAsUnhealthy(t *testing.T) {
	f, sv := processFx(t)
	processObserved(f, sv, proc("web", 1, 1), proc("worker", 0, 2), proc("api", 1, 1))
	// serviceState drives the roll-ups, so the service is not "running".
	view, err := f.deps.Services.Get(f.ctx(f.admin), sv.ID)
	require.NoError(t, err)
	require.Len(t, view.Observed.Processes, 3)
	m := f.must("get_overview", map[string]any{"detail": "full"})
	require.Contains(t, chgJSON(t, m), "processesNotReady")
	require.Contains(t, chgJSON(t, m), "worker: 0 of 2")
}

func TestStageChangesReplacesTheProcessList(t *testing.T) {
	f := newFx(t)
	sv := f.service(svcOpts{env: f.staging, slug: "app"})
	sel := f.selector(f.staging, "app")

	m := f.must("stage_changes", map[string]any{
		"service": sel,
		"config": map[string]any{
			"processName": "web",
			"processes": []any{
				map[string]any{"name": "worker", "startCommand": []any{"app", "worker"}},
				map[string]any{"name": "scheduler", "startCommand": []any{"app", "clock"}, "singleton": true, "stopGraceSeconds": 5},
			},
		},
	})
	require.Equal(t, true, m["staged"], chgJSON(t, m))
	cfg := services.DecodeConfig(chgReload(f, sv.ID).Config)
	require.Equal(t, []string{"scheduler", "worker"}, cfg.ProcessNames()[1:], "processes are sorted by name")
	p, ok := cfg.Process("scheduler")
	require.True(t, ok)
	require.True(t, p.Singleton)
	require.Equal(t, 5, p.StopGraceSeconds)
	fields := chgFields(m)
	require.Contains(t, fields, "config.processes.worker")
	require.Contains(t, fields, "config.processes.scheduler")

	// The list is replaced as a whole: a process left out is removed.
	m = f.must("stage_changes", map[string]any{
		"service": sel,
		"config":  map[string]any{"processes": []any{map[string]any{"name": "worker", "startCommand": []any{"app", "worker", "--fast"}}}},
	})
	require.Equal(t, true, m["staged"], chgJSON(t, m))
	cfg = services.DecodeConfig(chgReload(f, sv.ID).Config)
	require.Equal(t, []string{"worker"}, cfg.ProcessNames()[1:])
	require.Equal(t, []string{"app", "worker", "--fast"}, cfg.Processes[0].StartCommand)

	// An empty list removes every process.
	f.must("stage_changes", map[string]any{"service": sel, "config": map[string]any{"processes": []any{}}})
	require.False(t, services.DecodeConfig(chgReload(f, sv.ID).Config).HasProcesses())
}

func TestStageChangesRefusesInvalidProcesses(t *testing.T) {
	f := newFx(t)
	sv := f.service(svcOpts{env: f.staging, slug: "app"})
	before := chgReload(f, sv.ID)
	sel := f.selector(f.staging, "app")
	cases := map[string][]any{
		"duplicate name":        {map[string]any{"name": "w", "startCommand": []any{"a"}}, map[string]any{"name": "w", "startCommand": []any{"b"}}},
		"the default's name":    {map[string]any{"name": "web", "startCommand": []any{"a"}}},
		"port of the default":   {map[string]any{"name": "w", "startCommand": []any{"a"}, "port": 80}},
		"no command":            {map[string]any{"name": "w"}},
		"name not a dns label":  {map[string]any{"name": "Bad_Name", "startCommand": []any{"a"}}},
		"too long a stop grace": {map[string]any{"name": "w", "startCommand": []any{"a"}, "stopGraceSeconds": 99999}},
	}
	for name, procs := range cases {
		_, err := f.call(f.admin, "stage_changes", map[string]any{"service": sel, "config": map[string]any{"processes": procs}})
		require.Error(t, err, name)
		require.Equal(t, before.Generation, chgReload(f, sv.ID).Generation, "%s: nothing may be staged", name)
	}
}

func TestSetupDomainRoutesToAProcess(t *testing.T) {
	f, _ := processFx(t)
	sel := f.selector(f.staging, "app")

	// Without `process` the domain routes to the default process, which has a port.
	m := f.must("setup_domain", map[string]any{"service": sel, "hostname": "www.example.org"})
	require.EqualValues(t, 80, m["domain"].(map[string]any)["targetPort"])

	// An explicit process routes to its own port.
	m = f.must("setup_domain", map[string]any{"service": sel, "hostname": "api.example.org", "process": "api"})
	dom := m["domain"].(map[string]any)
	require.EqualValues(t, 9000, dom["targetPort"])
	require.Equal(t, "api", dom["process"])

	// Asking again reports the same domain.
	m = f.must("setup_domain", map[string]any{"service": sel, "hostname": "api.example.org", "process": "api"})
	require.Equal(t, false, m["domain"].(map[string]any)["created"])

	// A process without a port cannot be routed; an unknown one neither.
	_, err := f.call(f.admin, "setup_domain", map[string]any{"service": sel, "hostname": "w.example.org", "process": "worker"})
	require.Equal(t, "process_has_no_port", errCode(err))
	_, err = f.call(f.admin, "setup_domain", map[string]any{"service": sel, "hostname": "x.example.org", "process": "nope"})
	require.Equal(t, "unknown_process", errCode(err))

	// A port that is not the process' port is refused.
	_, err = f.call(f.admin, "setup_domain", map[string]any{"service": sel, "hostname": "y.example.org", "process": "api", "targetPort": 80})
	require.Equal(t, "invalid_target_port", errCode(err))
}

func TestSetupDomainProcessNeedsAServiceWithProcesses(t *testing.T) {
	f := newFx(t)
	f.service(svcOpts{env: f.staging, slug: "plain"})
	_, err := f.call(f.admin, "setup_domain", map[string]any{"service": f.selector(f.staging, "plain"), "hostname": "p.example.org", "process": "web"})
	require.Equal(t, "invalid_process", errCode(err), "%v", err)
}

func TestTailLogsAsksForOneProcess(t *testing.T) {
	f, sv := processFx(t)
	sel := f.selector(f.staging, "app")
	f.ag.lines[sv.ID.String()] = []string{"worker | job 1 done", "worker | job 2 done"}

	m := f.must("tail_logs", map[string]any{"service": sel, "process": "worker"})
	require.Contains(t, m["source"], "process worker")
	reqs := f.ag.logRequests()
	require.NotEmpty(t, reqs)
	require.Equal(t, "worker", reqs[len(reqs)-1].Process)

	// Without a process every process is read (the agent prefixes the lines).
	f.must("tail_logs", map[string]any{"service": sel})
	reqs = f.ag.logRequests()
	require.Equal(t, "", reqs[len(reqs)-1].Process)

	// A process only makes sense for a service's runtime logs.
	_, err := f.call(f.admin, "tail_logs", map[string]any{"environment": "shop/staging", "process": "worker"})
	require.Equal(t, "invalid_argument", errCode(err))
}
