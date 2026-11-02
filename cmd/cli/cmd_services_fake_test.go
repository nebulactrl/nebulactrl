package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

const (
	svEnvServices = cfgAPI + "/environments/" + cfgProdID + "/services"
	svSvc         = cfgAPI + "/services/" + fakeSvcID
	svLogs        = svSvc + "/logs"
)

func svView(slug string, extra map[string]any) map[string]any {
	m := map[string]any{
		"id": slug + "-id", "name": slug, "slug": slug, "kind": "git", "sourceType": "image",
		"config": map[string]any{"image": "traefik/whoami:latest"}, "scaling": map[string]any{"ready": 1, "replicas": 1},
		"server": map[string]any{"name": "edge-1"}, "latestDeployment": map[string]any{"id": "dep-1", "status": "succeeded"},
		"observed": map[string]any{"state": "running", "health": "healthy"},
	}
	for k, v := range extra {
		m[k] = v
	}
	return m
}

func TestServicesLsSummarisesStatusSourceAndReplicas(t *testing.T) {
	f := newCfgFake(t)
	f.reply("GET", svEnvServices, 200, map[string]any{"items": []any{
		svView("healthy", nil),
		svView("plain", map[string]any{"observed": map[string]any{"state": "running", "health": "none"}, "hasStagedChanges": true}),
		svView("building", map[string]any{"observed": map[string]any{}, "latestDeployment": map[string]any{"id": "dep-2", "status": "building"},
			"scaling": map[string]any{"ready": 2, "replicas": 3}}),
		svView("fresh", map[string]any{"latestDeployment": map[string]any{}, "server": map[string]any{}}),
		svView("failed", map[string]any{"observed": map[string]any{"state": "running"}, "latestDeployment": map[string]any{"id": "dep-3", "status": "failed"}}),
		svView("repo", map[string]any{"sourceType": "git", "config": map[string]any{"gitUrl": "https://git.example.com/acme/app.git", "gitBranch": "main"}}),
		svView("repo-nobranch", map[string]any{"sourceType": "git", "config": map[string]any{"gitUrl": "https://git.example.com/acme/x.git"}}),
		svView("noimage", map[string]any{"config": map[string]any{}}),
	}})

	r := f.run("services", "ls", "--project", "acme-shop", "--env", "production")
	require.Equal(t, 0, r.code, r.all())
	for _, h := range []string{"NAME", "SLUG", "KIND", "SOURCE", "STATUS", "REPLICAS", "SERVER", "STAGED", "ID"} {
		require.Contains(t, r.stdout, h)
	}
	require.Equal(t, []string{"healthy", "healthy", "git", "traefik/whoami:latest", "running", "(healthy)", "1/1", "edge-1", "no", "healthy-id"}, bkLine(t, r.stdout, "healthy-id"),
		"a succeeded deployment shows the observed state with its health")
	require.Equal(t, []string{"plain", "plain", "git", "traefik/whoami:latest", "running", "1/1", "edge-1", "yes", "plain-id"}, bkLine(t, r.stdout, "plain-id"),
		"health none is not shown; staged changes are flagged")
	require.Equal(t, []string{"building", "building", "git", "traefik/whoami:latest", "building", "2/3", "edge-1", "no", "building-id"}, bkLine(t, r.stdout, "building-id"),
		"without an observed state the deployment status is shown")
	require.Equal(t, []string{"fresh", "fresh", "git", "traefik/whoami:latest", "not", "deployed", "1/1", "-", "no", "fresh-id"}, bkLine(t, r.stdout, "fresh-id"),
		"no deployment yet, no server")
	require.Equal(t, []string{"failed", "failed", "git", "traefik/whoami:latest", "failed", "1/1", "edge-1", "no", "failed-id"}, bkLine(t, r.stdout, "failed-id"),
		"a failed latest deployment wins over an old observed state")
	require.Equal(t, "https://git.example.com/acme/app.git@main", bkLine(t, r.stdout, "repo-id")[3])
	require.Equal(t, "https://git.example.com/acme/x.git", bkLine(t, r.stdout, "repo-nobranch-id")[3])
	require.Equal(t, "-", bkLine(t, r.stdout, "noimage-id")[3], "no source shows a dash")

	r = f.run("--json", "services", "ls", "--project", "acme-shop", "--env", "production")
	require.Len(t, decode[[]any](t, r.stdout), 8)

	r = f.run("services", "ls", "--project", "acme-shop")
	require.Equal(t, 2, r.code)
	require.Contains(t, r.err, "--project and --env are required")
	r = f.run("services", "ls", "--project", "ghost", "--env", "production")
	require.Equal(t, 1, r.code)
	require.Contains(t, r.err, `project "ghost" not found`)
}

func TestServicesRestartReportsTheCommand(t *testing.T) {
	f := newCfgFake(t)
	f.reply("POST", svSvc+"/restart", 202, map[string]any{"commandId": "cmd-9"})

	r := f.run("--idempotency-key", "rs-key", "services", "restart", fakeSvcID)
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, "Restart requested (command cmd-9)\n", r.stdout)
	require.Equal(t, "rs-key", f.only("POST", svSvc+"/restart").header.Get("Idempotency-Key"))

	r = f.run("--json", "services", "restart", fakeSvcID)
	require.Equal(t, "cmd-9", decode[map[string]any](t, r.stdout)["commandId"])

	f.problem("POST", svSvc+"/restart", 409, "not_running", "the service is not deployed")
	r = f.run("services", "restart", fakeSvcID)
	require.Equal(t, 1, r.code)
	require.Contains(t, r.err, "the service is not deployed (not_running, HTTP 409)")
	require.Empty(t, r.stdout)
}

func TestServicesLogsPrintsRecentLinesWithOptionalTimestamps(t *testing.T) {
	f := newCfgFake(t)
	f.reply("GET", svLogs, 200, map[string]any{"items": []any{
		map[string]any{"ts": "2026-01-02T03:04:05.123456789Z", "line": "listening on :80"},
		map[string]any{"ts": "2026-01-02T03:04:06Z", "line": "GET / 200"},
	}})

	r := f.run("services", "logs", fakeSvcID)
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, "listening on :80\nGET / 200\n", r.stdout)
	q := f.only("GET", svLogs).values(t)
	require.Equal(t, "200", q.Get("tail"), "the default tail is 200")
	require.Empty(t, q.Get("follow"))
	require.Empty(t, q.Get("process"))

	r = f.run("services", "logs", fakeSvcID, "--timestamps", "--tail", "5", "--process", "worker")
	require.Equal(t, "2026-01-02T03:04:05.123456789Z listening on :80\n2026-01-02T03:04:06Z GET / 200\n", r.stdout)
	q = f.calls("GET", svLogs)[1].values(t)
	require.Equal(t, "5", q.Get("tail"))
	require.Equal(t, "worker", q.Get("process"))

	r = f.run("--json", "services", "logs", fakeSvcID)
	require.Len(t, decode[[]map[string]any](t, r.stdout), 2)

	f.reply("GET", svLogs, 200, map[string]any{})
	r = f.run("--json", "services", "logs", fakeSvcID)
	require.Equal(t, []any{}, decode[[]any](t, r.stdout), "no lines is an empty array")

	f.replyRaw("GET", svLogs, cfgReply{status: 200, ctype: "text/plain", body: "<html>"})
	r = f.run("services", "logs", fakeSvcID)
	require.Equal(t, 1, r.code)
	require.Contains(t, r.err, "decode logs")

	f.problem("GET", svLogs, 404, "not_found", "no such service")
	r = f.run("services", "logs", fakeSvcID)
	require.Equal(t, 1, r.code)
	require.Contains(t, r.err, "no such service (not_found, HTTP 404)")
}

func TestServicesLogsFollowStreamsUntilTheEndEvent(t *testing.T) {
	f := newCfgFake(t)
	f.replySSE(svLogs, sse(
		"", `{"ts":"2026-01-02T03:04:05Z","line":"one"}`,
		"heartbeat", "{}",
		"", `{"ts":"2026-01-02T03:04:06Z","line":"two"}`,
		"end", "{}",
		"", `{"ts":"2026-01-02T03:04:07Z","line":"after the end"}`,
	))

	r := f.run("services", "logs", fakeSvcID, "-f", "--tail", "10")
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, "one\ntwo\n", r.stdout, "events after `end` and unknown events are ignored")
	q := f.only("GET", svLogs).values(t)
	require.Equal(t, "true", q.Get("follow"))
	require.Equal(t, "10", q.Get("tail"))

	r = f.run("services", "logs", fakeSvcID, "--follow", "--timestamps")
	require.Equal(t, "2026-01-02T03:04:05Z one\n2026-01-02T03:04:06Z two\n", r.stdout)

	r = f.run("--json", "services", "logs", fakeSvcID, "-f")
	require.Equal(t, `{"ts":"2026-01-02T03:04:05Z","line":"one"}`+"\n"+`{"ts":"2026-01-02T03:04:06Z","line":"two"}`+"\n", r.stdout, "--json passes events through")
}

func TestServicesLogsFollowReportsBrokenEventsAndRefusals(t *testing.T) {
	f := newCfgFake(t)
	f.replySSE(svLogs, sse("", `{"line":"ok"}`, "", "not json"))
	r := f.run("services", "logs", fakeSvcID, "-f")
	require.Equal(t, 1, r.code)
	require.Equal(t, "ok\n", r.stdout, "lines before the broken event are printed")
	require.Contains(t, r.err, "decode log event")

	f.problem("GET", svLogs, 403, "forbidden", "needs logs:read")
	r = f.run("services", "logs", fakeSvcID, "-f")
	require.Equal(t, 1, r.code)
	require.Contains(t, r.err, "needs logs:read (forbidden, HTTP 403)")

	r = f.run("services", "logs", "not-a-ref", "-f")
	require.Equal(t, 1, r.code)
	require.Contains(t, r.err, "use a service ID or project/env/service-slug")
}
