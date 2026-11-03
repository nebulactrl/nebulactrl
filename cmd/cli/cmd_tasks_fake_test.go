package main

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const (
	tkID     = "12121212-1111-4111-8111-121212121212"
	tkID2    = "13131313-1111-4111-8111-131313131313"
	tkRunID  = "14141414-1111-4111-8111-141414141414"
	tkSvcRun = cfgAPI + "/services/" + fakeSvcID

	tkTasks = tkSvcRun + "/tasks"
	tkRuns  = tkSvcRun + "/task-runs"
	tkRun   = cfgAPI + "/task-runs/" + tkRunID
	tkLogs  = tkRun + "/logs"
)

func tkTaskList() map[string]any {
	return map[string]any{"items": []any{
		map[string]any{
			"id": tkID, "name": "Nightly", "action": "command", "schedule": "0 3 * * *", "timezone": "Europe/Berlin", "enabled": true,
			"command": []string{"rake", "db:cleanup"}, "nextRunAt": "2026-03-01T02:00:00Z",
			"lastRun": map[string]any{"status": "succeeded"},
		},
		map[string]any{
			"id": tkID2, "name": "paused", "action": "command", "schedule": "@daily", "timezone": "Nowhere/Land", "enabled": false,
			"command": []string{"sh", "-c", "echo hi"}, "nextRunAt": "2026-03-01T02:00:00Z",
		},
		map[string]any{
			"id": "15151515-1111-4111-8111-151515151515", "name": "utc", "action": "command", "schedule": "@hourly", "timezone": "", "enabled": true,
			"command": nil, "nextRunAt": "2026-03-01T02:30:00Z",
		},
	}}
}

// sse renders server-sent events: each pair is (event name, data).
func sse(events ...string) string {
	var b strings.Builder
	for i := 0; i+1 < len(events); i += 2 {
		if events[i] != "" {
			b.WriteString("event: " + events[i] + "\n")
		}
		b.WriteString("data: " + events[i+1] + "\n\n")
	}
	return b.String()
}

func (f *cfgFake) replySSE(path, body string) {
	f.replyRaw("GET", path, cfgReply{status: 200, ctype: "text/event-stream", body: body})
}

func TestTasksLsShowsScheduleInTheTasksZoneAndFallsBackToUTC(t *testing.T) {
	f := newCfgFake(t)
	f.reply("GET", tkTasks, 200, tkTaskList())

	r := f.run("tasks", "ls", fakeSvcID)
	require.Equal(t, 0, r.code, r.all())
	nightly := bkLine(t, r.stdout, "Nightly")
	require.Equal(t, []string{"Nightly", "command", "0", "3", "*", "*", "*", "Europe/Berlin", "yes", "2026-03-01", "03:00", "CET", "succeeded", "rake", "db:cleanup", tkID}, nightly,
		"the next run is shown in the task's own zone; the last run is its status")
	paused := bkLine(t, r.stdout, "paused")
	require.Equal(t, []string{"paused", "command", "@daily", "Nowhere/Land", "no", "-", "-", "sh", "-c", "echo", "hi", tkID2}, paused,
		"a disabled task has no next run")
	utc := bkLine(t, r.stdout, "@hourly")
	require.Equal(t, []string{"utc", "command", "@hourly"}, utc[0:3])
	require.Contains(t, strings.Join(utc, " "), "2026-03-01 02:30 UTC", "an empty zone renders as UTC")
	require.Contains(t, r.stdout, "NEXT RUN")

	r = f.run("--json", "tasks", "ls", fakeSvcID)
	require.Equal(t, 0, r.code, r.all())
	require.Len(t, decode[[]any](t, r.stdout), 3)

	f.reply("GET", tkTasks, 200, map[string]any{})
	r = f.run("--json", "tasks", "ls", fakeSvcID)
	require.Equal(t, []any{}, decode[[]any](t, r.stdout))

	f.problem("GET", tkTasks, 404, "not_found", "service not found")
	r = f.run("tasks", "ls", fakeSvcID)
	require.Equal(t, 1, r.code)
	require.Contains(t, r.err, "service not found (not_found, HTTP 404)")
}

func TestTasksCreateChecksTheCommandLineBeforeTheAPI(t *testing.T) {
	f := newCfgFake(t)
	before := f.total()
	for name, tc := range map[string]struct {
		args []string
		want string
	}{
		"no command":    {[]string{"tasks", "create", fakeSvcID, "--name", "n", "--schedule", "@daily"}, "give the command after `--`"},
		"no service":    {[]string{"tasks", "create", "--name", "n", "--schedule", "@daily", "--", "ls"}, "expected exactly one <service>"},
		"two services":  {[]string{"tasks", "create", fakeSvcID, fakeSvc2ID, "--name", "n", "--schedule", "@daily", "--", "ls"}, "expected exactly one <service>"},
		"no name":       {[]string{"tasks", "create", fakeSvcID, "--schedule", "@daily", "--", "ls"}, "--name and --schedule are required"},
		"no schedule":   {[]string{"tasks", "create", fakeSvcID, "--name", "n", "--", "ls"}, "--name and --schedule are required"},
		"empty command": {[]string{"tasks", "create", fakeSvcID, "--name", "n", "--schedule", "@daily", "--"}, "give the command after `--`"},
	} {
		r := f.run(tc.args...)
		require.Equal(t, 2, r.code, "%s: %s", name, r.all())
		require.Contains(t, r.err, tc.want, name)
		require.Contains(t, r.err, "Run with --help for usage.", name)
	}
	require.Equal(t, before, f.total(), "usage errors never reach the API")
}

func TestTasksCreateSendsEveryFlagAndAnnouncesTheTask(t *testing.T) {
	f := newCfgFake(t)
	created := map[string]any{"id": tkID, "name": "digest", "schedule": "*/15 * * * *"}
	f.reply("POST", tkTasks, 201, created)

	r := f.run("--idempotency-key", "k-1", "tasks", "create", fakeSvcID, "--name", "digest", "--schedule", "*/15 * * * *",
		"--timezone", "Europe/Berlin", "--timeout", "90s", "--concurrency", "allow", "--disabled", "--", "sh", "-c", "echo hi")
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, "Task \"digest\" created ("+tkID+")\n", r.stdout)
	post := f.only("POST", tkTasks)
	require.Equal(t, map[string]any{
		"name": "digest", "action": "command", "schedule": "*/15 * * * *", "command": []any{"sh", "-c", "echo hi"},
		"timezone": "Europe/Berlin", "enabled": false, "timeoutSeconds": float64(90), "concurrency": "allow",
	}, post.json(t))
	require.Equal(t, "k-1", post.header.Get("Idempotency-Key"))

	// Only what was asked for is sent.
	r = f.run("--json", "tasks", "create", fakeSvcID, "--name", "digest", "--schedule", "@daily", "--", "rake")
	require.Equal(t, 0, r.code, r.all())
	got := decode[map[string]any](t, r.stdout)
	require.Equal(t, tkID, got["id"])
	require.Equal(t, "digest", got["name"])
	require.Equal(t, map[string]any{"name": "digest", "action": "command", "schedule": "@daily", "command": []any{"rake"}}, f.calls("POST", tkTasks)[1].json(t))
}

func TestTasksCreateRestartSendsNoCommand(t *testing.T) {
	f := newCfgFake(t)
	f.reply("POST", tkTasks, 201, map[string]any{"id": tkID, "name": "weekly", "action": "restart"})

	r := f.run("tasks", "create", fakeSvcID, "--name", "weekly", "--action", "restart", "--schedule", "0 4 * * 0")
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, map[string]any{"name": "weekly", "action": "restart", "schedule": "0 4 * * 0"}, f.only("POST", tkTasks).json(t))

	before := f.total()
	for name, args := range map[string][]string{
		"command given":  {"tasks", "create", fakeSvcID, "--name", "n", "--action", "restart", "--schedule", "@daily", "--", "ls"},
		"timeout given":  {"tasks", "create", fakeSvcID, "--name", "n", "--action", "restart", "--schedule", "@daily", "--timeout", "90s"},
		"unknown action": {"tasks", "create", fakeSvcID, "--name", "n", "--action", "reboot", "--schedule", "@daily", "--", "ls"},
		"restart no svc": {"tasks", "create", "--name", "n", "--action", "restart", "--schedule", "@daily"},
	} {
		r := f.run(args...)
		require.Equal(t, 2, r.code, "%s: %s", name, r.all())
	}
	require.Equal(t, before, f.total(), "usage errors never reach the API")
}

func TestTasksCreateSurfacesAServerRefusal(t *testing.T) {
	f := newCfgFake(t)
	f.problem("POST", tkTasks, 422, "invalid_schedule", "schedule: not a cron expression")
	r := f.run("tasks", "create", fakeSvcID, "--name", "n", "--schedule", "nope", "--", "ls")
	require.Equal(t, 1, r.code)
	require.Contains(t, r.err, "schedule: not a cron expression (invalid_schedule, HTTP 422)")
	require.Empty(t, r.stdout)
}

func TestTasksRmDeletesByNameOrIDAndReportsUnknownTasks(t *testing.T) {
	f := newCfgFake(t)
	f.reply("GET", tkTasks, 200, tkTaskList())
	f.reply("DELETE", cfgAPI+"/tasks/"+tkID, 204, "")

	r := f.run("tasks", "rm", fakeSvcID, "NIGHTLY")
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, "Task \"Nightly\" deleted\n", r.stdout)
	require.Len(t, f.calls("DELETE", cfgAPI+"/tasks/"+tkID), 1)

	r = f.run("tasks", "rm", fakeSvcID, tkID)
	require.Equal(t, 0, r.code, r.all())
	require.Len(t, f.calls("DELETE", cfgAPI+"/tasks/"+tkID), 2)

	r = f.run("tasks", "rm", fakeSvcID, "ghost")
	require.Equal(t, 1, r.code)
	require.Contains(t, r.err, `task "ghost" not found on this service`)
	require.Len(t, f.calls("DELETE", cfgAPI+"/tasks/"+tkID), 2, "nothing else was deleted")

	f.problem("DELETE", cfgAPI+"/tasks/"+tkID, 403, "forbidden", "needs deployments:write")
	r = f.run("tasks", "rm", fakeSvcID, "nightly")
	require.Equal(t, 1, r.code)
	require.Contains(t, r.err, "needs deployments:write (forbidden, HTTP 403)")
	require.NotContains(t, r.stdout, "deleted")
}

func TestTasksRunsListsHistoryAndFiltersByTask(t *testing.T) {
	f := newCfgFake(t)
	f.reply("GET", tkTasks, 200, tkTaskList())
	f.reply("GET", tkRuns, 200, map[string]any{"items": []any{
		map[string]any{
			"id": tkRunID, "name": "Nightly", "status": "succeeded", "exitCode": 0, "source": "cron",
			"createdAt": "2026-03-01T02:00:00Z", "startedAt": "2026-03-01T02:00:01Z", "finishedAt": "2026-03-01T02:01:31Z",
		},
		map[string]any{
			"id": "16161616-1111-4111-8111-161616161616", "name": "one-off", "status": "failed", "exitCode": 7, "source": "one_off",
			"createdAt": "2026-03-01T03:00:00Z", "error": "command exited 7",
		},
	}})

	r := f.run("tasks", "runs", fakeSvcID, "--task", "nightly", "--limit", "5")
	require.Equal(t, 0, r.code, r.all())
	q := f.only("GET", tkRuns).values(t)
	require.Equal(t, tkID, q.Get("taskId"), "the name is resolved to the task's id")
	require.Equal(t, "5", q.Get("limit"))
	ok := bkLine(t, r.stdout, tkRunID)
	require.Equal(t, []string{"2026-03-01T02:00:00Z", "Nightly", "succeeded", "0", "cron", "1m30s", tkRunID}, ok)
	bad := bkLine(t, r.stdout, "one-off")
	require.Equal(t, []string{"2026-03-01T03:00:00Z", "one-off", "failed", "7", "one_off", "-", "16161616-1111-4111-8111-161616161616", "command", "exited", "7"}, bad,
		"a run without start/finish has no duration; the error closes the row")

	r = f.run("tasks", "runs", fakeSvcID)
	require.Equal(t, 0, r.code, r.all())
	q = f.calls("GET", tkRuns)[1].values(t)
	require.Empty(t, q.Get("taskId"))
	require.Empty(t, q.Get("limit"))

	r = f.run("tasks", "runs", fakeSvcID, "--task", "ghost")
	require.Equal(t, 1, r.code)
	require.Contains(t, r.err, `task "ghost" not found`)

	f.reply("GET", tkRuns, 200, map[string]any{})
	r = f.run("--json", "tasks", "runs", fakeSvcID)
	require.Equal(t, []any{}, decode[[]any](t, r.stdout))
}

func TestTasksRunStartsTheTaskAndStreamsItsOutput(t *testing.T) {
	f := newCfgFake(t)
	f.reply("GET", tkTasks, 200, tkTaskList())
	f.reply("POST", cfgAPI+"/tasks/"+tkID+"/run", 202, map[string]any{"id": tkRunID})
	f.replySSE(tkLogs, ": ping\n\n"+sse("", `{"line":"cleaning"}`, "", `{"line":"done"}`, "end", "{}"))
	f.reply("GET", tkRun, 200, map[string]any{"id": tkRunID, "status": "succeeded", "exitCode": 0})

	r := f.run("--idempotency-key", "k-run", "tasks", "run", fakeSvcID, "nightly")
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, "cleaning\ndone\n", r.stdout)
	require.Equal(t, "k-run", f.only("POST", cfgAPI+"/tasks/"+tkID+"/run").header.Get("Idempotency-Key"))
	require.Equal(t, "true", f.only("GET", tkLogs).values(t).Get("follow"), "output is followed while the run is active")

	r = f.run("--json", "tasks", "run", fakeSvcID, "nightly")
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, `{"line":"cleaning"}`+"\n"+`{"line":"done"}`+"\n", r.stdout, "--json passes the events through")
}

func TestTasksRunOfACronServiceNeedsNoTaskName(t *testing.T) {
	f := newCfgFake(t)
	f.reply("GET", svSvc, 200, map[string]any{"id": fakeSvcID, "cron": map[string]any{"taskId": tkID, "schedule": "0 3 * * *", "timezone": "UTC"}})
	f.reply("POST", cfgAPI+"/tasks/"+tkID+"/run", 202, map[string]any{"id": tkRunID})

	r := f.run("tasks", "run", fakeSvcID, "--detach")
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, "Run "+tkRunID+" started\n", r.stdout)
	require.Len(t, f.calls("POST", cfgAPI+"/tasks/"+tkID+"/run"), 1)

	// A service without a schedule needs the task named.
	f.reply("GET", svSvc, 200, map[string]any{"id": fakeSvcID})
	r = f.run("tasks", "run", fakeSvcID, "--detach")
	require.Equal(t, 2, r.code, r.all())
	require.Contains(t, r.err, "is not a cron service")
}

func TestTasksRunDetachStartsWithoutFollowing(t *testing.T) {
	f := newCfgFake(t)
	f.reply("GET", tkTasks, 200, tkTaskList())
	f.reply("POST", cfgAPI+"/tasks/"+tkID+"/run", 202, map[string]any{"id": tkRunID})

	r := f.run("tasks", "run", fakeSvcID, "nightly", "--detach")
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, "Run "+tkRunID+" started\n", r.stdout)
	require.Empty(t, f.calls("GET", tkLogs), "a detached run is not followed")

	r = f.run("--json", "tasks", "run", fakeSvcID, "nightly", "--detach")
	require.Equal(t, map[string]any{"id": tkRunID}, decode[map[string]any](t, r.stdout))
}

func TestTasksRunRefusesUnknownTaskAndServerErrors(t *testing.T) {
	f := newCfgFake(t)
	f.reply("GET", tkTasks, 200, tkTaskList())
	r := f.run("tasks", "run", fakeSvcID, "ghost")
	require.Equal(t, 1, r.code)
	require.Contains(t, r.err, `task "ghost" not found`)

	f.problem("POST", cfgAPI+"/tasks/"+tkID+"/run", 409, "run_active", "a run is already active")
	r = f.run("tasks", "run", fakeSvcID, "nightly")
	require.Equal(t, 1, r.code)
	require.Contains(t, r.err, "a run is already active (run_active, HTTP 409)")

	r = f.run("tasks", "run", "not-a-ref", "nightly")
	require.Equal(t, 1, r.code)
	require.Contains(t, r.err, "use a service ID or project/env/service-slug")
}

func TestFollowRunExitsWithTheCommandsExitCode(t *testing.T) {
	f := newCfgFake(t)
	f.replySSE(tkLogs, sse("", `{"line":"boom"}`, "end", "{}"))

	for name, tc := range map[string]struct {
		run      map[string]any
		wantCode int
		wantErr  string
	}{
		"exit code of the command":    {map[string]any{"status": "failed", "exitCode": 7, "error": "command exited 7"}, 7, "run " + tkRunID + " failed: command exited 7"},
		"failed without exit code":    {map[string]any{"status": "failed"}, 1, "run " + tkRunID + " failed\n"},
		"exit code beyond one byte":   {map[string]any{"status": "failed", "exitCode": 300}, 1, "run " + tkRunID + " failed"},
		"timed out":                   {map[string]any{"status": "timed_out", "error": "exceeded 30m"}, 1, "run " + tkRunID + " timed_out: exceeded 30m"},
		"skipped":                     {map[string]any{"status": "skipped", "exitCode": 0}, 1, "run " + tkRunID + " skipped"},
		"stream ended before the end": {map[string]any{"status": "running"}, 1, "the output stream ended but run " + tkRunID + " is still running"},
	} {
		tc.run["id"] = tkRunID
		f.reply("GET", tkRun, 200, tc.run)
		r := f.run("tasks", "logs", tkRunID)
		require.Equal(t, tc.wantCode, r.code, "%s: %s", name, r.all())
		require.Equal(t, "boom\n", r.stdout, name)
		require.Contains(t, r.err, tc.wantErr, name)
	}
}

func TestFollowRunReportsABrokenStream(t *testing.T) {
	f := newCfgFake(t)
	f.replySSE(tkLogs, sse("", `not json`))
	r := f.run("tasks", "logs", tkRunID)
	require.Equal(t, 1, r.code)
	require.Contains(t, r.err, "decode output event")

	f.problem("GET", tkLogs, 404, "not_found", "run not found")
	r = f.run("tasks", "logs", tkRunID)
	require.Equal(t, 1, r.code)
	require.Contains(t, r.err, "run not found (not_found, HTTP 404)")

	f.replySSE(tkLogs, sse("", `{"line":"x"}`, "end", "{}"))
	f.problem("GET", tkRun, 500, "internal", "database down")
	r = f.run("tasks", "logs", tkRunID)
	require.Equal(t, 1, r.code)
	require.Contains(t, r.err, "database down (internal, HTTP 500)")
}

func TestServicesRunStartsAOneOffCommand(t *testing.T) {
	f := newCfgFake(t)
	f.reply("POST", tkSvcRun+"/run", 202, map[string]any{"id": tkRunID})
	f.replySSE(tkLogs, sse("", `{"line":"migrated"}`, "end", "{}"))
	f.reply("GET", tkRun, 200, map[string]any{"id": tkRunID, "status": "succeeded"})

	r := f.run("services", "run", fakeSvcID, "--timeout", "2m", "--", "rake", "db:migrate")
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, "migrated\n", r.stdout)
	require.Equal(t, map[string]any{"command": []any{"rake", "db:migrate"}, "timeoutSeconds": float64(120)}, f.only("POST", tkSvcRun+"/run").json(t))

	r = f.run("services", "run", fakeSvcID, "--detach", "--", "ls")
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, "Run "+tkRunID+" started\n", r.stdout)
	require.Equal(t, map[string]any{"command": []any{"ls"}}, f.calls("POST", tkSvcRun+"/run")[1].json(t))

	for _, args := range [][]string{
		{"services", "run", fakeSvcID},
		{"services", "run", "--", "ls"},
		{"services", "run", fakeSvcID, fakeSvc2ID, "--", "ls"},
	} {
		r = f.run(args...)
		require.Equal(t, 2, r.code, "%v: %s", args, r.all())
	}
	require.Len(t, f.calls("POST", tkSvcRun+"/run"), 2, "malformed command lines are rejected before the request")

	f.problem("POST", tkSvcRun+"/run", 403, "forbidden", "needs deployments:write")
	r = f.run("services", "run", fakeSvcID, "--", "ls")
	require.Equal(t, 1, r.code)
	require.Contains(t, r.err, "needs deployments:write (forbidden, HTTP 403)")
}
