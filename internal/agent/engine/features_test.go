package engine

import (
	"archive/tar"
	"bytes"
	"errors"
	"io"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

func untar(t *testing.T, data []byte) map[string]struct {
	mode    int64
	content string
} {
	t.Helper()
	out := map[string]struct {
		mode    int64
		content string
	}{}
	tr := tar.NewReader(bytes.NewReader(data))
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return out
		}
		require.NoError(t, err)
		b, err := io.ReadAll(tr)
		require.NoError(t, err)
		out[h.Name] = struct {
			mode    int64
			content string
		}{h.Mode, string(b)}
	}
}

func TestFilesAreCopiedIntoTheCreatedContainerBeforeStart(t *testing.T) {
	r := newRig(t, nil)
	p := basePayload()
	p.Files = []agentprotocol.FileSpec{
		{Path: "/etc/app/config.toml", Content: "a = 1\n"},
		{Path: "/etc/app/secret.key", Mode: "0400", Content: "k"},
	}
	res, dr := r.deploy(p, nil)
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, dr.Message)

	require.Len(t, r.dk.copies, 1)
	cp := r.dk.copies[0]
	require.Equal(t, "/", cp.dir)
	files := untar(t, cp.data)
	require.Equal(t, "a = 1\n", files["etc/app/config.toml"].content)
	require.EqualValues(t, 0o644, files["etc/app/config.toml"].mode)
	require.EqualValues(t, 0o400, files["etc/app/secret.key"].mode)
}

func TestFilesTarRejectsTraversalAndBadModes(t *testing.T) {
	_, err := filesTar([]agentprotocol.FileSpec{{Path: "/../etc/passwd", Content: "x"}})
	require.Error(t, err)
	_, err = filesTar([]agentprotocol.FileSpec{{Path: "/etc/x", Mode: "9z", Content: "x"}})
	require.Error(t, err)
}

func TestOneShotContainerHasNoRestartPolicyAndIsLabeled(t *testing.T) {
	r := newRig(t, nil)
	p := basePayload()
	p.OneShot, p.Port, p.Hosts, p.Strategy = true, 0, nil, agentprotocol.StrategyRecreate
	p.Healthcheck = agentprotocol.HealthcheckSpec{Type: "none", DeadlineSeconds: 5}
	res, dr := r.deploy(p, nil)
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, dr.Message)
	spec := r.dk.created[0]
	require.Equal(t, "no", spec.RestartPolicy)
	require.Equal(t, "true", spec.Labels[agentprotocol.LabelOneShot])
}

func TestExecHealthcheckBecomesTheContainerHealthcheck(t *testing.T) {
	r := newRig(t, nil)
	p := basePayload()
	p.Healthcheck = agentprotocol.HealthcheckSpec{
		Type: "exec", Command: []string{"CMD-SHELL", "pg_isready -U app"}, IntervalSeconds: 7, TimeoutSeconds: 4, StartPeriodSeconds: 20, DeadlineSeconds: 5,
	}
	res, dr := r.deploy(p, nil)
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, dr.Message)
	h := r.dk.created[0].Healthcheck
	require.NotNil(t, h)
	require.Equal(t, []string{"CMD-SHELL", "pg_isready -U app"}, h.Test)
	require.EqualValues(t, 7e9, int64(h.Interval))
	require.EqualValues(t, 4e9, int64(h.Timeout))
	require.EqualValues(t, 20e9, int64(h.StartPeriod))

	// Other check types install no container healthcheck.
	r2 := newRig(t, nil)
	res, dr = r2.deploy(basePayload(), nil)
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, dr.Message)
	require.Nil(t, r2.dk.created[0].Healthcheck)
}

func TestReplicaObservedOneShotCleanExitIsHealthy(t *testing.T) {
	ok := replicaObserved(0, dockerExited(0, true))
	require.Equal(t, agentprotocol.StateExited, ok.State)
	require.Equal(t, agentprotocol.HealthHealthy, ok.Health)
	failed := replicaObserved(0, dockerExited(3, true))
	require.Equal(t, agentprotocol.HealthUnhealthy, failed.Health)
	notOneShot := replicaObserved(0, dockerExited(0, false))
	require.Equal(t, agentprotocol.HealthUnhealthy, notOneShot.Health, "a long-running service that exited is unhealthy")

	o := agentprotocol.ObservedService{}
	aggregateObserved(&o, []agentprotocol.ObservedReplica{ok})
	require.Equal(t, agentprotocol.HealthHealthy, o.Health)
	require.Equal(t, 1, o.ReplicasReady)
}

func dockerExited(code int, oneShot bool) docker.ContainerInfo {
	labels := map[string]string{}
	if oneShot {
		labels[agentprotocol.LabelOneShot] = "true"
	}
	return docker.ContainerInfo{State: "exited", ExitCode: code, Labels: labels}
}
