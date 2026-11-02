package main

import (
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

const (
	cbID    = "c0c0c0c0-1111-4111-8111-c0c0c0c0c0c0"
	cbOne   = cfgAPI + "/combines/" + cbID
	cbAbort = cbOne + "/abort"
	cbRetry = cbOne + "/retry"
)

func cbView(state string, extra map[string]any) map[string]any {
	m := map[string]any{
		"id": cbID, "state": state, "targetServiceId": fakeSvcID, "sourceServiceIds": []string{fakeSvc2ID, "other-source"},
		"environmentId": cfgProdID, "createdAt": "2026-01-02T03:04:05Z",
	}
	for k, v := range extra {
		m[k] = v
	}
	return m
}

func TestCombineStatusExplainsWhatToDoNext(t *testing.T) {
	f := newCfgFake(t)
	f.reply("GET", cbOne, 200, cbView("failed", map[string]any{"deploymentId": "dep-1", "error": "deployment failed"}))

	r := f.run("services", "combine", "status", cbID)
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, "Combine "+cbID+": failed\n"+
		"Target:     "+fakeSvcID+"\n"+
		"Sources:    "+fakeSvc2ID+", other-source\n"+
		"Deployment: dep-1 (nebulactrl deployments status dep-1)\n"+
		"Error:      deployment failed\n"+
		"Nothing was deleted. Retry with `services combine retry "+cbID+"` or take it back with `services combine abort "+cbID+"`.\n", r.stdout)

	f.reply("GET", cbOne, 200, cbView("staged", map[string]any{"sourceServiceIds": []string{}}))
	r = f.run("services", "combine", "status", cbID)
	require.Equal(t, "Combine "+cbID+": staged\nTarget:     "+fakeSvcID+"\nSources:    -\n"+
		"Staged; the deployment starts next. Take it back with `services combine abort "+cbID+"`.\n", r.stdout)

	f.reply("GET", cbOne, 200, cbView("done", nil))
	r = f.run("services", "combine", "status", cbID)
	require.NotContains(t, r.stdout, "Nothing was deleted")
	require.NotContains(t, r.stdout, "Staged;")
	require.NotContains(t, r.stdout, "Deployment:")

	r = f.run("--json", "services", "combine", "status", cbID)
	require.Equal(t, "done", decode[map[string]any](t, r.stdout)["state"])

	f.problem("GET", cbOne, 404, "not_found", "no such combine")
	r = f.run("services", "combine", "status", cbID)
	require.Equal(t, 1, r.code)
	require.Contains(t, r.err, "no such combine (not_found, HTTP 404)")
}

func TestCombineAbortPrintsTheResultingState(t *testing.T) {
	f := newCfgFake(t)
	f.reply("POST", cbAbort, 200, cbView("aborted", nil))

	r := f.run("--idempotency-key", "ab-key", "services", "combine", "abort", cbID)
	require.Equal(t, 0, r.code, r.all())
	require.Contains(t, r.stdout, "Combine "+cbID+": aborted\n")
	require.Equal(t, "ab-key", f.only("POST", cbAbort).header.Get("Idempotency-Key"))

	f.problem("POST", cbAbort, 409, "combine_running", "the cutover is running")
	r = f.run("services", "combine", "abort", cbID)
	require.Equal(t, 1, r.code)
	require.Contains(t, r.err, "the cutover is running (combine_running, HTTP 409)")
	require.Empty(t, r.stdout)
}

func TestCombineRetryWithoutWaitPrintsTheNewState(t *testing.T) {
	f := newCfgFake(t)
	f.reply("POST", cbRetry, 202, cbView("deploying", map[string]any{"deploymentId": "dep-2"}))

	r := f.run("services", "combine", "retry", cbID)
	require.Equal(t, 0, r.code, r.all())
	require.Contains(t, r.stdout, "Combine "+cbID+": deploying\n")
	require.Contains(t, r.stdout, "Deployment: dep-2")
	require.Empty(t, f.calls("GET", cbOne), "without --wait nothing is polled")

	f.problem("POST", cbRetry, 409, "not_failed", "the combine did not fail")
	r = f.run("services", "combine", "retry", cbID)
	require.Equal(t, 1, r.code)
	require.Contains(t, r.err, "the combine did not fail (not_failed, HTTP 409)")
}

func TestCombineRetryWaitFollowsTheStatesToDone(t *testing.T) {
	f := newCfgFake(t)
	f.reply("POST", cbRetry, 202, cbView("deploying", nil))
	f.replySeq("GET", cbOne, 200, cbView("deploying", nil), cbView("cutover", nil), cbView("done", nil))

	r := f.run("services", "combine", "retry", cbID, "--wait")
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, "Combine "+cbID+" done\n", r.stdout)
	require.Contains(t, r.err, "Combine "+cbID+": deploying\n")
	require.Contains(t, r.err, "Combine "+cbID+": cutover\n")
	require.Equal(t, 1, strings.Count(r.err, "Combine "+cbID+": deploying"), "an unchanged state is reported once")

	f.replySeq("GET", cbOne, 200, cbView("done", nil))
	r = f.run("--json", "services", "combine", "retry", cbID, "--wait")
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, "done", decode[map[string]any](t, r.stdout)["state"])
}

func TestCombineRetryWaitFailsWithTheCombinesError(t *testing.T) {
	f := newCfgFake(t)
	f.reply("POST", cbRetry, 202, cbView("deploying", nil))
	f.reply("GET", cbOne, 200, cbView("failed", map[string]any{"error": "health check failed"}))

	r := f.run("services", "combine", "retry", cbID, "--wait")
	require.Equal(t, 1, r.code, r.all())
	require.Contains(t, r.err, "Error: combine "+cbID+" failed: health check failed\n")
	require.NotContains(t, r.stdout, "done")

	r = f.run("--json", "services", "combine", "retry", cbID, "--wait")
	require.Equal(t, 1, r.code, r.all())
	require.Equal(t, "failed", decode[map[string]any](t, r.stdout)["state"], "--json prints the final state before failing")

	f.reply("GET", cbOne, 200, cbView("aborted", nil))
	r = f.run("services", "combine", "retry", cbID, "--wait")
	require.Equal(t, 1, r.code, r.all())
	require.Contains(t, r.err, "Error: combine "+cbID+" aborted\n")
}

func TestCombineRetryWaitTimesOutAndToleratesShortOutages(t *testing.T) {
	f := newCfgFake(t)
	f.reply("POST", cbRetry, 202, cbView("deploying", nil))
	f.reply("GET", cbOne, 200, cbView("deploying", nil))
	r := f.run("services", "combine", "retry", cbID, "--wait", "--timeout", "100ms")
	require.Equal(t, 1, r.code, r.all())
	require.Contains(t, r.err, "timed out after 100ms waiting for combine "+cbID+" (last state deploying); it may still be running")

	var calls atomic.Int64
	f.handle("GET", cbOne, func(cfgReq) cfgReply {
		if calls.Add(1) <= 3 {
			return problemReply(503, "unavailable", "restarting", nil)
		}
		return jsonReply(200, cbView("done", nil))
	})
	r = f.run("services", "combine", "retry", cbID, "--wait")
	require.Equal(t, 0, r.code, "three failed polls are survivable: %s", r.all())

	calls.Store(-100)
	f.handle("GET", cbOne, func(cfgReq) cfgReply {
		calls.Add(1)
		return problemReply(503, "unavailable", "still restarting", nil)
	})
	r = f.run("services", "combine", "retry", cbID, "--wait")
	require.Equal(t, 1, r.code, r.all())
	require.Contains(t, r.err, "still restarting (unavailable, HTTP 503)")
	require.EqualValues(t, -96, calls.Load(), "the fourth consecutive failure ends the wait")

	f.problem("GET", cbOne, 403, "forbidden", "token cannot read combines")
	r = f.run("services", "combine", "retry", cbID, "--wait")
	require.Equal(t, 1, r.code, r.all())
	require.Contains(t, r.err, "token cannot read combines (forbidden, HTTP 403)")
}
