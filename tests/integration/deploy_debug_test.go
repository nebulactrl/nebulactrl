//go:build integration

package integration

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// debugLogLines reads a deployment's persisted debug stream.
func (s *Stack) debugLogLines(id string) []string {
	s.t.Helper()
	r := s.Do("GET", "/deployments/"+id+"/logs?streams=debug", nil)
	require.Equal(s.t, http.StatusOK, r.Status, string(r.Raw))
	var out []string
	for _, it := range r.JSON().Items() {
		require.Equal(s.t, "debug", it.Str("stream"), "?streams=debug returns only the debug stream")
		out = append(out, it.Str("line"))
	}
	return out
}

// The deployment debug log: the agent's verbose trace (docker calls, every
// health probe attempt, route changes, timings) is stored with the deploy log
// but hidden from it, readable with ?streams=, and never holds a secret. The
// commands and release views of the debug panel are redacted too.
func TestDeploymentDebugLog(t *testing.T) {
	requireTier(t, tierFull)
	t.Parallel()
	app := appFixture(t)
	s := SharedStack(t)
	svc := s.NewService(SvcSpec{
		Name: "dbg", Image: app.AppImage, Domain: s.Host(),
		Vars:    map[string]string{"VERSION": "v1", "SECRET_ECHO": "API_KEY"},
		Secrets: map[string]string{"API_KEY": canary},
	})
	d := s.DeployOK(svc.ID)
	dep := d.Str("id")

	// The trace is flushed before the result, but give the intake a moment.
	var lines []string
	require.True(t, eventually(30*time.Second, 500*time.Millisecond, func() bool {
		lines = s.debugLogLines(dep)
		return strings.Contains(strings.Join(lines, "\n"), "finished: outcome succeeded")
	}), "the debug stream never ended: %v", lines)
	text := strings.Join(lines, "\n")

	// What the engine did, step by step.
	for _, want := range []string{
		"command ",                          // the deploy command and its shape
		"docker image pull " + app.AppImage, // the pull
		"docker container create nc-dbg-",   // the create call...
		"API_KEY=•••",                       // ...with env values masked
		"docker container start nc-dbg-",    // the start
		"health nc-dbg-",                    // every probe attempt, tagged with the replica
		"probe GET http://",                 // ...its URL
		"HTTP 200 in ",                      // ...status code and latency
		"(pass)",                            // ...and verdict
		"consecutive",                       // the success threshold
		"routes before:", "routes after:",   // the route change
		"proxy apply: ok", // ...and its result
		"verify 1/",       // verification through the proxy
		"phases: ",        // timings
		"finished: outcome succeeded",
	} {
		require.Contains(t, text, want)
	}
	require.LessOrEqual(t, len(lines), 501, "the debug stream is capped at 500 lines plus a marker")
	requireNoSecret(t, "debug stream", text)

	// Hidden from the ordinary log, in the REST read and in the follow stream.
	plain := s.Do("GET", "/deployments/"+dep+"/logs", nil)
	require.Equal(t, http.StatusOK, plain.Status, string(plain.Raw))
	for _, it := range plain.JSON().Items() {
		require.NotEqual(t, "debug", it.Str("stream"))
		require.NotContains(t, it.Str("line"), "probe GET")
	}
	all := s.Do("GET", "/deployments/"+dep+"/logs?streams=all", nil)
	require.Equal(t, http.StatusOK, all.Status, string(all.Raw))
	var sawDebug, sawDeploy bool
	for _, it := range all.JSON().Items() {
		sawDebug = sawDebug || it.Str("stream") == "debug"
		sawDeploy = sawDeploy || it.Str("stream") == "deploy"
	}
	require.True(t, sawDebug && sawDeploy, "streams=all carries both")
	require.Equal(t, http.StatusUnprocessableEntity, s.Do("GET", "/deployments/"+dep+"/logs?streams=bogus", nil).Status)

	// The commands of the deployment: redacted payload, delivery times.
	cr := s.Do("GET", "/deployments/"+dep+"/commands", nil)
	require.Equal(t, http.StatusOK, cr.Status, string(cr.Raw))
	items := cr.JSON().Items()
	require.NotEmpty(t, items)
	var deploy J
	for _, it := range items {
		if it.Str("kind") == "deploy" {
			deploy = it
		}
	}
	require.NotNil(t, deploy, "a deploy command: %s", cr.Raw)
	require.Equal(t, "succeeded", deploy.Str("status"))
	require.NotEmpty(t, deploy.Str("serverName"))
	require.NotEmpty(t, deploy.Str("ackedAt"))
	require.NotEmpty(t, deploy.Str("finishedAt"))
	require.Contains(t, string(cr.Raw), `"API_KEY"`, "variable keys are shown")
	requireNoSecret(t, "commands", string(cr.Raw))

	// The release: keys with flags, never values.
	rr := s.Do("GET", "/deployments/"+dep+"/release", nil)
	require.Equal(t, http.StatusOK, rr.Status, string(rr.Raw))
	require.Equal(t, d.Str("releaseId"), rr.JSON().Str("id"))
	var keyed bool
	for _, v := range rr.JSON()["variables"].([]any) {
		m := v.(map[string]any)
		if m["key"] == "API_KEY" {
			keyed = true
			require.Equal(t, true, m["secret"])
		}
	}
	require.True(t, keyed, "API_KEY is listed: %s", rr.Raw)
	requireNoSecret(t, "release", string(rr.Raw))

	// Nothing in the database holds it either (debug lines, command rows).
	requireNoSecret(t, "database dump", s.dumpAllTables())
}
