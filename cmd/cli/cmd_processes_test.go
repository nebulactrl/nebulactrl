package main

import (
	"slices"
	"testing"

	"github.com/stretchr/testify/require"
)

// processList is `services processes ls --json`.
func (e *env) processList(service string) []map[string]any {
	e.t.Helper()
	r := e.mustRun("", "--json", "services", "processes", "ls", service)
	return decode[[]map[string]any](e.t, r.stdout)
}

func processNamesOfList(ps []map[string]any) []string {
	names := make([]string, 0, len(ps))
	for _, p := range ps {
		names = append(names, p["name"].(string))
	}
	return names
}

func processByName(t *testing.T, ps []map[string]any, name string) map[string]any {
	t.Helper()
	i := slices.IndexFunc(ps, func(p map[string]any) bool { return p["name"] == name })
	require.GreaterOrEqual(t, i, 0, "no process %s in %v", name, processNamesOfList(ps))
	return ps[i]
}

func TestProcessesLsShowsTheServiceItselfAsTheDefaultProcess(t *testing.T) {
	e := newEnv(t)
	e.login()
	e.seed()

	r := e.mustRun("", "services", "processes", "ls", "acme/production/web")
	for _, h := range []string{"NAME", "DEFAULT", "PORT", "COMMAND", "REPLICAS", "STATE"} {
		require.Contains(t, r.stdout, h)
	}
	require.Contains(t, r.stdout, "web")
	require.Contains(t, r.stdout, "80")
	require.Contains(t, r.stdout, "0/1", "ready/desired")

	ps := e.processList("acme/production/web")
	require.Len(t, ps, 1)
	require.Equal(t, "web", ps[0]["name"])
	require.Equal(t, true, ps[0]["default"])
	require.EqualValues(t, 80, ps[0]["port"])
}

func TestProcessesSetAddsChangesAndKeepsTheRest(t *testing.T) {
	e := newEnv(t)
	e.login()
	e.seed()
	web := "acme/production/web"

	// A new process needs its command.
	r := e.run("", "services", "processes", "set", web, "worker", "--stop-grace-seconds", "120")
	require.Equal(t, 2, r.code, r.all())
	require.Contains(t, r.err, "start command")

	// The command in all three spellings.
	r = e.mustRun("", "services", "processes", "set", web, "worker",
		"--command", "bundle", "--command", "exec", "--command", "sidekiq", "--stop-grace-seconds", "120")
	require.Contains(t, r.stdout, "Added process worker")
	require.Contains(t, r.stdout, "staged until the next deployment")
	e.mustRun("", "services", "processes", "set", web, "queue", "--command-json", `["php","artisan","queue:work"]`, "--memory-mb", "256")
	r = e.mustRun("", "--json", "services", "processes", "set", web, "scheduler", "--singleton", "--", "php", "artisan", "schedule:work")
	cfg := decode[map[string]any](t, r.stdout)["config"].(map[string]any)
	require.Len(t, cfg["processes"], 3, "the whole list round-trips through the config PATCH")

	ps := e.processList(web)
	require.Equal(t, []string{"web", "queue", "scheduler", "worker"}, processNamesOfList(ps), "the default first, then by name")
	worker := processByName(t, ps, "worker")
	require.Equal(t, []any{"bundle", "exec", "sidekiq"}, worker["command"])
	require.EqualValues(t, 120, worker["stopGraceSeconds"])
	require.Equal(t, false, worker["default"])
	require.Equal(t, []any{"php", "artisan", "queue:work"}, processByName(t, ps, "queue")["command"])
	require.Equal(t, true, processByName(t, ps, "scheduler")["singleton"])

	// Changing one setting keeps the command and the other settings; the health check follows the port.
	r = e.mustRun("", "services", "processes", "set", web, "worker", "--port", "9000")
	require.Contains(t, r.stdout, "Updated process worker")
	worker = processByName(t, e.processList(web), "worker")
	require.EqualValues(t, 9000, worker["port"])
	require.Equal(t, []any{"bundle", "exec", "sidekiq"}, worker["command"])
	require.EqualValues(t, 120, worker["stopGraceSeconds"])
	r = e.mustRun("", "--json", "services", "processes", "set", web, "worker", "--health-type", "tcp", "--health-interval", "5")
	for _, p := range decode[map[string]any](t, r.stdout)["config"].(map[string]any)["processes"].([]any) {
		if p.(map[string]any)["name"] == "worker" {
			hc := p.(map[string]any)["healthcheck"].(map[string]any)
			require.Equal(t, "tcp", hc["type"])
			require.EqualValues(t, 5, hc["intervalSeconds"])
			require.EqualValues(t, 9000, hc["port"], "a tcp check watches the process' port")
		}
	}

	// Ports are unique across the service's processes.
	r = e.run("", "services", "processes", "set", web, "worker", "--port", "80")
	require.Equal(t, 1, r.code, r.all())

	// The default process: by flag or by its own name.
	e.mustRun("", "services", "processes", "set", web, "--default", "--port", "8080")
	require.EqualValues(t, 8080, processByName(t, e.processList(web), "web")["port"])
	e.mustRun("", "services", "processes", "set", web, "web", "--singleton=false")
	r = e.mustRun("", "--json", "services", "processes", "set", web, "--default", "--name", "app")
	require.Equal(t, "app", decode[map[string]any](t, r.stdout)["config"].(map[string]any)["processName"])
	require.Equal(t, []string{"app", "queue", "scheduler", "worker"}, processNamesOfList(e.processList(web)))

	for name, args := range map[string][]string{
		"nothing to change":     {"services", "processes", "set", web, "worker"},
		"no name":               {"services", "processes", "set", web, "--port", "1"},
		"--name on a named one": {"services", "processes", "set", web, "worker", "--name", "w2"},
		"two commands":          {"services", "processes", "set", web, "worker", "--command", "a", "--command-json", `["b"]`},
		"bad health type":       {"services", "processes", "set", web, "worker", "--health-type", "ping"},
		"--default, wrong name": {"services", "processes", "set", web, "worker", "--default", "--port", "1"},
		"bad json":              {"services", "processes", "set", web, "worker", "--command-json", "nope"},
	} {
		require.Equal(t, 2, e.run("", args...).code, name)
	}
}

func TestProcessesRenameAndRemove(t *testing.T) {
	e := newEnv(t)
	e.login()
	e.seed()
	web := "acme/production/web"
	e.mustRun("", "services", "processes", "set", web, "worker", "--command", "bin/worker")
	e.mustRun("", "services", "processes", "set", web, "cron", "--command", "bin/cron", "--singleton")
	e.mustRun("", "services", "scale", web, "2", "--process", "worker")

	// Renaming keeps the settings, including the replicas.
	r := e.mustRun("", "services", "processes", "rename", web, "worker", "jobs")
	require.Contains(t, r.stdout, "Renamed process worker to jobs")
	ps := e.processList(web)
	require.Equal(t, []string{"web", "cron", "jobs"}, processNamesOfList(ps))
	jobs := processByName(t, ps, "jobs")
	require.Equal(t, []any{"bin/worker"}, jobs["command"])
	require.EqualValues(t, 2, jobs["scaling"].(map[string]any)["replicas"], "renameProcesses carries the replica setting over")

	// The default process is renamed through processName.
	e.mustRun("", "services", "processes", "rename", web, "web", "app")
	require.Equal(t, "app", processByName(t, e.processList(web), "app")["name"])
	e.mustRun("", "services", "processes", "rename", web, "app", "web")
	require.Equal(t, []string{"web", "cron", "jobs"}, processNamesOfList(e.processList(web)))

	for name, args := range map[string][]string{
		"unknown old":     {"services", "processes", "rename", web, "nope", "x"},
		"existing new":    {"services", "processes", "rename", web, "jobs", "cron"},
		"the default new": {"services", "processes", "rename", web, "jobs", "web"},
		"same name":       {"services", "processes", "rename", web, "jobs", "jobs"},
		"remove unknown":  {"services", "processes", "rm", web, "nope"},
		"remove default":  {"services", "processes", "rm", web, "web"},
	} {
		require.Equal(t, 2, e.run("", args...).code, name)
	}

	r = e.mustRun("", "services", "processes", "rm", web, "cron")
	require.Contains(t, r.stdout, "Removed process cron")
	require.Equal(t, []string{"web", "jobs"}, processNamesOfList(e.processList(web)))
	e.mustRun("", "services", "processes", "rm", web, "jobs")
	ps = e.processList(web)
	require.Len(t, ps, 1, "the last named process gone: a plain service again")
	require.Equal(t, "web", ps[0]["name"])
}

func TestProcessesScale(t *testing.T) {
	e := newEnv(t)
	e.login()
	e.seed()
	web := "acme/production/web"
	e.mustRun("", "services", "processes", "set", web, "worker", "--command", "bin/worker")
	e.mustRun("", "services", "processes", "set", web, "cron", "--command", "bin/cron", "--singleton")

	r := e.mustRun("", "services", "processes", "scale", web, "worker", "3")
	require.Contains(t, r.stdout, "Process worker: Replicas: 3 desired")
	require.EqualValues(t, 3, processByName(t, e.processList(web), "worker")["scaling"].(map[string]any)["replicas"])
	require.EqualValues(t, 1, processByName(t, e.processList(web), "web")["scaling"].(map[string]any)["replicas"], "the default process is untouched")

	r = e.mustRun("", "--json", "services", "processes", "scale", web, "worker", "--autoscale", "1:4:60")
	var viewProcesses []map[string]any
	for _, p := range decode[map[string]any](t, r.stdout)["processes"].([]any) {
		viewProcesses = append(viewProcesses, p.(map[string]any))
	}
	require.Equal(t, true, processByName(t, viewProcesses, "worker")["scaling"].(map[string]any)["autoscaling"].(map[string]any)["enabled"], "the response is the service")
	r = e.mustRun("", "services", "processes", "scale", web, "worker", "--autoscale", "1:4:60")
	require.Contains(t, r.stdout, "Process worker: Autoscaling 1-4 replicas at 60% CPU")
	manual := e.run("", "services", "processes", "scale", web, "worker", "2")
	require.Equal(t, 1, manual.code, manual.all())
	require.Contains(t, manual.all(), "autoscaling_enabled")
	e.mustRun("", "services", "processes", "scale", web, "worker", "--autoscale", "off")

	// `services scale --process` is the same call; a process named like the default is the default process.
	r = e.mustRun("", "services", "scale", web, "2", "--process", "worker")
	require.Contains(t, r.stdout, "Process worker: Replicas: 2 desired")
	r = e.mustRun("", "services", "processes", "scale", web, "web", "2")
	require.Contains(t, r.stdout, "Process web: Replicas: 2 desired")
	require.EqualValues(t, 2, processByName(t, e.processList(web), "web")["scaling"].(map[string]any)["replicas"])
	require.EqualValues(t, 2, processByName(t, e.processList(web), "worker")["scaling"].(map[string]any)["replicas"])

	// A singleton runs exactly one instance.
	r = e.run("", "services", "processes", "scale", web, "cron", "2")
	require.Equal(t, 1, r.code, r.all())
	require.Contains(t, r.all(), "replicas_require_stateless")
	r = e.run("", "services", "processes", "scale", web, "nope", "2")
	require.Equal(t, 1, r.code, r.all())
	require.Contains(t, r.all(), "unknown_process")

	for name, args := range map[string][]string{
		"neither replicas nor autoscale": {"services", "processes", "scale", web, "worker"},
		"no process":                     {"services", "processes", "scale", web},
		"too many replicas":              {"services", "processes", "scale", web, "worker", "11"},
		"both":                           {"services", "scale", web, "2", "--autoscale", "1:2", "--process", "worker"},
		"bad autoscale":                  {"services", "processes", "scale", web, "worker", "--autoscale", "x:y"},
	} {
		require.Equal(t, 2, e.run("", args...).code, name)
	}
}

// A domain routes to the process named by --process and pins the process in use.
func TestDomainsAddRoutesToAProcess(t *testing.T) {
	e := newEnv(t)
	e.login()
	e.seed()
	web := "acme/production/web"
	e.mustRun("", "services", "processes", "set", web, "api", "--command", "bin/api", "--port", "9000")
	e.mustRun("", "services", "processes", "set", web, "jobs", "--command", "bin/jobs")

	r := e.run("", "domains", "add", web, "jobs.example.com", "--process", "jobs")
	require.Equal(t, 1, r.code, "a process without a port cannot be routed to: %s", r.all())
	require.Contains(t, r.all(), "process_has_no_port")
	r = e.run("", "domains", "add", web, "x.example.com", "--process", "nope")
	require.Equal(t, 1, r.code, r.all())
	require.Contains(t, r.all(), "unknown_process")

	r = e.mustRun("", "--json", "domains", "add", web, "api.example.com", "--process", "api")
	d := decode[map[string]any](t, r.stdout)
	require.Equal(t, "api", d["process"])
	require.EqualValues(t, 9000, d["targetPort"], "the target port is the process' port")
	r = e.mustRun("", "domains", "add", web, "www.example.com")
	require.Contains(t, r.stdout, "Added www.example.com")

	r = e.mustRun("", "domains", "ls", web)
	require.Contains(t, r.stdout, "PROCESS")
	require.Contains(t, r.stdout, "api.example.com")

	r = e.run("", "services", "processes", "rm", web, "api")
	require.Equal(t, 1, r.code, "the domain keeps the process alive: %s", r.all())
	require.Contains(t, r.all(), "process_in_use")
}

func TestProcessFlagsAreOffered(t *testing.T) {
	e := newEnv(t)
	for _, args := range [][]string{
		{"services", "logs"},
		{"services", "download-logs"},
		{"services", "shell"},
		{"services", "scale"},
		{"domains", "add"},
		{"domains", "generate"},
	} {
		r := e.mustRun("", append(args, "--help")...)
		require.Contains(t, r.stdout, "--process", args)
	}
	r := e.mustRun("", "services", "processes", "set", "--help")
	for _, f := range []string{"--command", "--command-json", "--port", "--stop-grace-seconds", "--singleton", "--memory-mb", "--cpus", "--health-type", "--health-path", "--default", "--name"} {
		require.Contains(t, r.stdout, f)
	}
}

// A deployment of a service with several processes says how each one rolls.
func TestServicesDeployPrintsOneLinePerProcess(t *testing.T) {
	e := newEnv(t)
	e.login()
	e.seed()
	web := "acme/production/web"

	// The plan lists the processes in rollout order: the ones with a port first.
	e.mustRun("", "services", "processes", "set", web, "worker", "--command", "bin/worker")
	r := e.mustRun("", "services", "deploy", web)
	require.Contains(t, r.stdout, "accepted")
	require.Contains(t, r.stdout, "1. web (default): port 80")
	require.Contains(t, r.stdout, "2. worker: no port")
	require.Contains(t, r.stdout, "replica(s)")
}
