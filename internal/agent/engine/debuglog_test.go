package engine

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/internal/agent/health"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// debugLines returns the text of every line the sender received on the debug
// stream, and checks no other stream carries debug-only content.
func debugLines(s *fakeSender) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, f := range s.logs {
		if f.Stream != agentprotocol.StreamDebug {
			continue
		}
		for _, l := range f.Lines {
			out = append(out, l.Text)
		}
	}
	return out
}

func TestDebugLogIsCappedWithOneMarker(t *testing.T) {
	snd := &fakeSender{}
	l := newDebugLog(agentprotocol.DeployPayload{DeploymentID: depV2, ServiceID: svcID}, snd.Log, nil)
	for i := range maxDebugLines + 250 {
		l.Add(fmt.Sprintf("line %d", i))
	}
	l.Close()

	lines := debugLines(snd)
	require.Len(t, lines, maxDebugLines+1, "the cap plus the truncation marker")
	require.Equal(t, "line 0", lines[0])
	require.Equal(t, fmt.Sprintf("line %d", maxDebugLines-1), lines[maxDebugLines-1])
	require.Equal(t, DebugTruncatedMarker, lines[maxDebugLines])
}

func TestDebugLogCountsEmbeddedNewlinesAndAcceptsNil(t *testing.T) {
	var nilLog *debugLog
	nilLog.Add("ignored")
	nilLog.Close()

	snd := &fakeSender{}
	l := newDebugLog(agentprotocol.DeployPayload{DeploymentID: depV2, ServiceID: svcID}, snd.Log, nil)
	l.Add(strings.Repeat("a\n", maxDebugLines-1) + "a") // exactly the cap
	l.Add("one more")
	l.Close()
	lines := debugLines(snd)
	require.Len(t, lines, maxDebugLines+1)
	require.Equal(t, DebugTruncatedMarker, lines[len(lines)-1])
}

func TestSpecSummaryMasksEnvValues(t *testing.T) {
	zero := 5
	spec := docker.ContainerSpec{
		Name: "nc-web-d2d2d2d2-0", Image: "sha256:0123456789abcdef0123456789abcdef", RestartPolicy: "unless-stopped", StopTimeout: &zero,
		Env:      []string{"API_KEY=sekret-canary-12345", "PORT=80"},
		Cmd:      []string{"/app/server", "--token=sekret-canary-12345"},
		Networks: []docker.NetworkAttachment{{Network: "nc-env-aaaa", Aliases: []string{"web"}}},
		Mounts:   []docker.VolumeMount{{Volume: "nc-vol-data", Target: "/data"}},
		Labels:   map[string]string{agentprotocol.LabelService: svcID},
	}
	text := strings.Join(specSummary(spec), "\n")
	require.NotContains(t, text, "sekret-canary-12345", "neither an env value nor a command argument is shown")
	require.Contains(t, text, "API_KEY=•••")
	require.Contains(t, text, "PORT=•••")
	require.Contains(t, text, "program \"/app/server\"")
	require.Contains(t, text, "nc-env-aaaa")
	require.Contains(t, text, "nc-vol-data -> /data")
}

func TestDeployWritesDebugStreamWithProbesAndNoSecret(t *testing.T) {
	const secret = "sekret-canary-12345"
	traced := func(_ context.Context, tg health.Target) health.Result {
		tg.Trace("probe GET http://172.20.0.5:80/: HTTP 200 in 3ms (pass)")
		tg.Trace("pass 2/2 consecutive")
		return health.Result{Healthy: true}
	}
	r := newRig(t, traced)
	r.previous()
	p := basePayload()
	p.EnvKeys = []string{"API_KEY", "PORT"}
	p.SecretEnvKeys = []string{"API_KEY"}
	res, dr := r.deploy(p, map[string]string{"env:API_KEY": secret, "env:PORT": "80"})
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, dr.Message)

	lines := debugLines(r.snd)
	text := strings.Join(lines, "\n")
	for _, want := range []string{
		"docker image pull traefik/whoami:latest",
		"docker container create " + newCtr,
		"API_KEY=•••",
		"health " + newCtr + ": probe GET http://172.20.0.5:80/: HTTP 200 in 3ms (pass)",
		"routes before:",
		"routes after:",
		"proxy apply: ok",
		"verify 1/",
		"docker container stop " + oldCtr,
		"finished: outcome succeeded",
	} {
		require.Contains(t, text, want)
	}
	require.NotContains(t, text, secret)

	// The ordinary deploy log carries none of the debug detail.
	r.snd.mu.Lock()
	defer r.snd.mu.Unlock()
	for _, f := range r.snd.logs {
		if f.Stream == agentprotocol.StreamDebug {
			continue
		}
		for _, l := range f.Lines {
			require.NotContains(t, l.Text, "docker container create")
		}
	}
}

func TestLeveledPutsTheLevelWordFirstSoTheLogViewerReadsIt(t *testing.T) {
	require.Equal(t, "WARN health web: probe GET http://x/: HTTP 503", leveled("WARN probe GET http://x/: HTTP 503", "health web"))
	require.Equal(t, "ERROR health web: boom", leveled("ERROR boom", "health web"))
	require.Equal(t, "health web: probe ok", leveled("probe ok", "health web"))
}

func TestFailedDeployDebugLogCarriesWarnLines(t *testing.T) {
	failing := func(_ context.Context, tg health.Target) health.Result {
		tg.Trace("WARN probe GET http://172.20.0.5:80/: HTTP 503 in 3ms (fail: expected 200-399)")
		return health.Result{Reason: "boom"}
	}
	r := newRig(t, failing)
	r.previous()
	res, _ := r.deploy(basePayload(), nil)
	require.NotEqual(t, agentprotocol.OutcomeSucceeded, res.Outcome)

	text := strings.Join(debugLines(r.snd), "\n")
	require.Contains(t, text, "WARN health "+newCtr+": probe GET http://172.20.0.5:80/: HTTP 503")
	require.Contains(t, text, "WARN health "+newCtr+": unhealthy: boom")
	require.Contains(t, text, "ERROR abort:")
	require.Contains(t, text, "docker container remove", "the failed container is removed")
}
