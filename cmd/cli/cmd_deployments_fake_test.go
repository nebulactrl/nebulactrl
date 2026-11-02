package main

import (
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

const (
	dpID1 = "e1e1e1e1-1111-4111-8111-e1e1e1e1e1e1"
	dpID2 = "e2e2e2e2-1111-4111-8111-e2e2e2e2e2e2"
	dpNew = "e3e3e3e3-1111-4111-8111-e3e3e3e3e3e3"

	dpList = cfgAPI + "/services/" + fakeSvcID + "/deployments"
	dpOne  = cfgAPI + "/deployments/" + dpID1
)

func dpView(id, status string, extra map[string]any) map[string]any {
	m := map[string]any{
		"id": id, "serviceId": fakeSvcID, "status": status, "strategy": "recreate", "trigger": "manual", "actorType": "user",
		"releaseId": "rel-1", "createdAt": "2026-01-02T03:04:05Z",
		"release": map[string]any{"source": map[string]any{"type": "image", "image": "traefik/whoami:v1"}},
	}
	for k, v := range extra {
		m[k] = v
	}
	return m
}

func TestDeploymentsLsSummarisesTheReleaseOfEachDeploymentAcrossPages(t *testing.T) {
	f := newCfgFake(t)
	f.handle("GET", dpList, func(req cfgReq) cfgReply {
		if req.values(t).Get("cursor") == "" {
			return jsonReply(200, map[string]any{"nextCursor": "page2", "items": []any{
				dpView(dpID1, "succeeded", map[string]any{"release": map[string]any{"source": map[string]any{
					"type": "git", "commitSha": "0123456789abcdef0123456789abcdef01234567"}}}),
			}})
		}
		return jsonReply(200, map[string]any{"items": []any{
			dpView(dpID2, "failed", map[string]any{"trigger": "webhook", "release": map[string]any{"source": map[string]any{
				"type": "image", "image": "ghcr.io/acme/web", "digest": "ghcr.io/acme/web@sha256:0123456789abcdef0123456789abcdef"}}}),
			dpView(dpNew, "pending", map[string]any{"release": map[string]any{"source": map[string]any{"type": "image"}}}),
		}})
	})

	r := f.run("deployments", "ls", fakeSvcID)
	require.Equal(t, 0, r.code, r.all())
	for _, h := range []string{"ID", "STATUS", "TRIGGER", "STRATEGY", "RELEASE", "CREATED"} {
		require.Contains(t, r.stdout, h)
	}
	require.Equal(t, []string{dpID1, "succeeded", "manual", "recreate", "git", "0123456"}, bkLine(t, r.stdout, dpID1)[:6],
		"a git release shows the first 7 characters of the commit")
	require.Equal(t, []string{dpID2, "failed", "webhook", "recreate", "ghcr.io/acme/web@sha256:0123456789ab"}, bkLine(t, r.stdout, dpID2)[:5],
		"an image release shows the digest cut to sha256 + 12 hex")
	require.Equal(t, []string{dpNew, "pending", "manual", "recreate", "-"}, bkLine(t, r.stdout, dpNew)[:5], "no image, no digest: a dash")
	require.Len(t, f.calls("GET", dpList), 2, "both pages are fetched")
	require.Equal(t, "page2", f.calls("GET", dpList)[1].values(t).Get("cursor"))

	r = f.run("--json", "deployments", "ls", fakeSvcID)
	require.Len(t, decode[[]any](t, r.stdout), 3)

	f.problem("GET", dpList, 404, "not_found", "no such service")
	r = f.run("deployments", "ls", fakeSvcID)
	require.Equal(t, 1, r.code)
	require.Contains(t, r.err, "no such service (not_found, HTTP 404)")
}

func TestDeploymentsStatusPrintsTheDeploymentAndItsTimeline(t *testing.T) {
	f := newCfgFake(t)
	f.reply("GET", dpOne, 200, dpView(dpID1, "waiting", map[string]any{
		"statusMessage": "server is offline", "startedAt": "2026-01-02T03:05:00Z",
		"waitingFor": map[string]any{"serverId": dbSrvID, "serverName": "edge-1", "offlineSince": "2026-01-02T03:00:00Z"},
		"release":    map[string]any{"source": map[string]any{"type": "image", "image": "traefik/whoami:v1", "digest": "sha256:abcdef0123456789abcdef"}},
	}))
	f.reply("GET", dpOne+"/events", 200, map[string]any{"items": []any{
		map[string]any{"createdAt": "2026-01-02T03:04:06Z", "step": "pull", "status": "succeeded", "message": "pulled image"},
		map[string]any{"createdAt": "2026-01-02T03:04:07Z", "step": "start", "status": "failed", "message": "port in use"},
	}})

	r := f.run("deployments", "status", dpID1)
	require.Equal(t, 0, r.code, r.all())
	for _, want := range []string{
		"Deployment  " + dpID1, "Service     " + fakeSvcID, "Status      waiting", "Message     server is offline",
		"Waiting     for edge-1 (offline since ", "Strategy    recreate", "Trigger     manual (user)",
		"Release     rel-1  traefik/whoami:v1@sha256:abcdef012345", "Started     ", "Finished    -",
	} {
		require.Contains(t, r.stdout, want)
	}
	require.Equal(t, []string{"pull", "succeeded", "pulled", "image"}, bkLine(t, r.stdout, "pulled image")[2:])
	require.Equal(t, []string{"start", "failed", "port", "in", "use"}, bkLine(t, r.stdout, "port in use")[2:])

	// A plain deployment prints no waiting line and, without events, no table.
	f.reply("GET", dpOne, 200, dpView(dpID1, "succeeded", nil))
	f.reply("GET", dpOne+"/events", 200, map[string]any{})
	r = f.run("deployments", "status", dpID1)
	require.Equal(t, 0, r.code, r.all())
	require.NotContains(t, r.stdout, "Waiting")
	require.NotContains(t, r.stdout, "Message")
	require.NotContains(t, r.stdout, "TIME")

	r = f.run("--json", "deployments", "status", dpID1)
	require.Equal(t, dpID1, decode[map[string]any](t, r.stdout)["id"])
	require.Len(t, f.calls("GET", dpOne+"/events"), 2, "--json does not fetch the timeline (only the two text runs did)")

	f.problem("GET", dpOne+"/events", 500, "internal", "events unavailable")
	r = f.run("deployments", "status", dpID1)
	require.Equal(t, 1, r.code)
	require.Contains(t, r.err, "events unavailable (internal, HTTP 500)")

	f.problem("GET", dpOne, 404, "not_found", "no such deployment")
	r = f.run("deployments", "status", dpID1)
	require.Equal(t, 1, r.code)
	require.Contains(t, r.err, "no such deployment (not_found, HTTP 404)")
}

func TestDeploymentsCancelDistinguishesCanceledFromRequested(t *testing.T) {
	f := newCfgFake(t)
	f.reply("POST", dpOne+"/cancel", 200, dpView(dpID1, "canceled", nil))

	r := f.run("--idempotency-key", "cancel-key", "deployments", "cancel", dpID1)
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, "Deployment "+dpID1+" canceled\n", r.stdout)
	require.Equal(t, "cancel-key", f.only("POST", dpOne+"/cancel").header.Get("Idempotency-Key"))

	f.reply("POST", dpOne+"/cancel", 200, dpView(dpID1, "building", nil))
	r = f.run("deployments", "cancel", dpID1)
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, "Cancel requested for deployment "+dpID1+" (building); the agent is stopping it\n", r.stdout)

	r = f.run("--json", "deployments", "cancel", dpID1)
	require.Equal(t, "building", decode[map[string]any](t, r.stdout)["status"])

	f.problem("POST", dpOne+"/cancel", 409, "too_late", "the new release is already live")
	r = f.run("deployments", "cancel", dpID1)
	require.Equal(t, 1, r.code)
	require.Contains(t, r.err, "the new release is already live (too_late, HTTP 409)")

	r = f.run("deployments", "cancel")
	require.Equal(t, 2, r.code)
}

func dpRollbackReply(status string) map[string]any {
	return dpView(dpNew, status, map[string]any{"disclaimer": "Databases and volumes are not rolled back.", "rolledBackFrom": dpID1})
}

func TestDeploymentsRollbackAcceptsAndWarnsAboutWhatIsNotRolledBack(t *testing.T) {
	f := newCfgFake(t)
	f.reply("POST", dpOne+"/rollback", 202, dpRollbackReply("pending"))

	r := f.run("--idempotency-key", "rb-key", "deployments", "rollback", dpID1)
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, "Rollback deployment "+dpNew+" accepted (pending, release traefik/whoami:v1)\n", r.stdout)
	require.Equal(t, "Databases and volumes are not rolled back.\n", r.err, "the disclaimer goes to stderr")
	require.Equal(t, "rb-key", f.only("POST", dpOne+"/rollback").header.Get("Idempotency-Key"))

	r = f.run("--json", "deployments", "rollback", dpID1)
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, dpNew, decode[map[string]any](t, r.stdout)["id"])
	require.Empty(t, r.err, "--json keeps stderr quiet")

	f.problem("POST", dpOne+"/rollback", 409, "no_release", "the deployment has no release")
	r = f.run("deployments", "rollback", dpID1)
	require.Equal(t, 1, r.code)
	require.Contains(t, r.err, "the deployment has no release (no_release, HTTP 409)")
}

func TestDeploymentsRollbackWaitFollowsTheNewDeployment(t *testing.T) {
	f := newCfgFake(t)
	newOne := cfgAPI + "/deployments/" + dpNew
	f.reply("POST", dpOne+"/rollback", 202, dpRollbackReply("pending"))
	f.replySeq("GET", newOne, 200,
		dpView(dpNew, "pending", nil),
		dpView(dpNew, "waiting", map[string]any{"statusMessage": "server offline", "waitingFor": map[string]any{"serverId": dbSrvID, "serverName": "edge-1"}}),
		dpView(dpNew, "succeeded", nil))

	r := f.run("deployments", "rollback", dpID1, "--wait")
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, "Deployment "+dpNew+" succeeded\n", r.stdout)
	require.Contains(t, r.err, "Deployment "+dpNew+": pending (recreate)\n")
	require.Contains(t, r.err, "Deployment "+dpNew+": waiting (waiting for edge-1) - server offline\n")
	require.NotContains(t, r.err, ": pending -", "an unchanged status is not repeated")
}

func TestDeploymentsRollbackWaitFailsWithTheDeploymentsOutcome(t *testing.T) {
	f := newCfgFake(t)
	newOne := cfgAPI + "/deployments/" + dpNew
	f.reply("POST", dpOne+"/rollback", 202, dpRollbackReply("pending"))
	f.reply("GET", newOne, 200, dpView(dpNew, "rollback_failed", map[string]any{"statusMessage": "health check failed"}))

	r := f.run("deployments", "rollback", dpID1, "--wait")
	require.Equal(t, 1, r.code, r.all())
	require.Contains(t, r.err, "Error: deployment "+dpNew+" rollback_failed: health check failed")
	require.NotContains(t, r.stdout, "succeeded")

	r = f.run("--json", "deployments", "rollback", dpID1, "--wait")
	require.Equal(t, 1, r.code, r.all())
	require.Equal(t, "rollback_failed", decode[map[string]any](t, r.stdout)["status"], "--json still prints the final deployment")

	f.reply("GET", newOne, 200, dpView(dpNew, "canceled", nil))
	r = f.run("deployments", "rollback", dpID1, "--wait")
	require.Equal(t, 1, r.code, r.all())
	require.Contains(t, r.err, "Error: deployment "+dpNew+" canceled\n")
}

func TestDeploymentsRollbackWaitTimesOutButLeavesTheDeploymentRunning(t *testing.T) {
	f := newCfgFake(t)
	f.reply("POST", dpOne+"/rollback", 202, dpRollbackReply("pending"))
	f.reply("GET", cfgAPI+"/deployments/"+dpNew, 200, dpView(dpNew, "deploying", nil))

	r := f.run("deployments", "rollback", dpID1, "--wait", "--timeout", "100ms")
	require.Equal(t, 1, r.code, r.all())
	require.Contains(t, r.err, "timed out after 100ms waiting for deployment "+dpNew+" (last status deploying); it may still be running")
}

func TestDeploymentsRollbackWaitToleratesThreeServerErrorsButNotMore(t *testing.T) {
	f := newCfgFake(t)
	newOne := cfgAPI + "/deployments/" + dpNew
	f.reply("POST", dpOne+"/rollback", 202, dpRollbackReply("pending"))

	var calls atomic.Int64
	failFirst := func(n int64) func(cfgReq) cfgReply {
		return func(cfgReq) cfgReply {
			if calls.Add(1) <= n {
				return problemReply(503, "unavailable", "control plane restarting", nil)
			}
			return jsonReply(200, dpView(dpNew, "succeeded", nil))
		}
	}

	f.handle("GET", newOne, failFirst(3))
	r := f.run("deployments", "rollback", dpID1, "--wait")
	require.Equal(t, 0, r.code, "three failed polls in a row are survivable: %s", r.all())

	calls.Store(0)
	f.handle("GET", newOne, failFirst(10))
	r = f.run("deployments", "rollback", dpID1, "--wait")
	require.Equal(t, 1, r.code, r.all())
	require.Contains(t, r.err, "control plane restarting (unavailable, HTTP 503)")
	require.EqualValues(t, 4, calls.Load(), "the fourth failure in a row ends the wait")

	f.problem("GET", newOne, 403, "forbidden", "token cannot read deployments")
	polled := len(f.calls("GET", newOne))
	r = f.run("deployments", "rollback", dpID1, "--wait")
	require.Equal(t, 1, r.code, r.all())
	require.Contains(t, r.err, "token cannot read deployments (forbidden, HTTP 403)", "a client error is not retried")
	require.Len(t, f.calls("GET", newOne), polled+1, "a single poll for the 403")
}
