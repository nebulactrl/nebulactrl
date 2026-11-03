package engine

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

func cronDeployPayload() agentprotocol.DeployPayload {
	p := basePayload()
	p.OneShot, p.NoStart, p.Port, p.Hosts, p.Routes, p.Strategy = true, true, 0, nil, nil, agentprotocol.StrategyRecreate
	p.Healthcheck = agentprotocol.HealthcheckSpec{Type: "none", DeadlineSeconds: 5}
	return p
}

func TestNoStartDeployStartsNothingAndForgetsTheService(t *testing.T) {
	r := newRig(t, nil)
	r.previous() // a running container of the previous (non-cron) release
	r.eng.mu.Lock()
	r.eng.local[svcID] = desiredEntry{DeploymentID: depV1}
	r.eng.desired[svcID] = desiredEntry{DeploymentID: depV1}
	r.eng.mu.Unlock()

	res, dr := r.deploy(cronDeployPayload(), nil)
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, dr.Message)
	require.Equal(t, "Image ready; runs on schedule", dr.Message)
	require.NotEmpty(t, dr.ImageDigest, "the image identity is reported like on a normal deploy")

	require.Equal(t, []string{"traefik/whoami:latest"}, r.dk.pulled)
	require.Empty(t, r.dk.created, "no container is created")
	require.Empty(t, r.dk.started)
	_, exists := r.dk.state(oldCtr)
	require.False(t, exists, "the previous release's container is stopped and removed")
	require.Contains(t, r.dk.stopped, oldCtr)

	r.eng.mu.Lock()
	_, local := r.eng.local[svcID]
	_, desired := r.eng.desired[svcID]
	r.eng.mu.Unlock()
	require.False(t, local)
	require.False(t, desired)
}

func TestNoStartRequiresOneShot(t *testing.T) {
	r := newRig(t, nil)
	p := basePayload()
	p.NoStart = true
	res, _ := r.deploy(p, nil)
	require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome)
	require.Contains(t, res.Error, "noStart needs a one-shot service")
}

func TestRunTaskServiceRunUsesTheServiceSetup(t *testing.T) {
	r := newRig(t, nil)
	p := agentprotocol.RunTaskPayload{
		RunID: runID, ServiceID: svcID, ServiceSlug: "web", EnvironmentID: envID,
		ImageRef: "busybox:latest", ServiceRun: true, TimeoutSeconds: 30,
		StartCommand: []string{"sh", "-c", "echo tick"},
		Volumes:      []agentprotocol.VolumeMount{{Name: "nc-vol-aaaaaaaa-data", MountPath: "/data"}},
		Files:        []agentprotocol.FileSpec{{Path: "/etc/app.conf", Content: "a = 1\n"}},
		Docker:       &agentprotocol.DockerOptions{Hostname: "cronjob", NoNewPrivileges: true},
	}
	exitWith(r, taskCtr, 0)
	r.dk.logText[taskCtr] = "tick\n"

	res := r.eng.HandleCommand(context.Background(), taskCmd(t, p, nil))
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, res.Error)

	require.Len(t, r.dk.created, 1)
	spec := r.dk.created[0]
	require.Equal(t, taskCtr, spec.Name)
	require.Nil(t, spec.Entrypoint, "the image's own entrypoint stays")
	require.Equal(t, []string{"sh", "-c", "echo tick"}, spec.Cmd)
	require.Equal(t, []docker.VolumeMount{{Volume: "nc-vol-aaaaaaaa-data", Target: "/data"}}, spec.Mounts)
	require.Equal(t, "cronjob", spec.Hostname)
	require.Contains(t, spec.SecurityOpt, "no-new-privileges:true")
	require.Empty(t, spec.RestartPolicy)
	_, hasService := spec.Labels[agentprotocol.LabelService]
	require.False(t, hasService, "a run container is not a replica of the service")
	require.Len(t, r.dk.copies, 1, "the files are written before the container starts")
	require.Contains(t, r.snd.allLogText(), "tick")
}

func TestRunTaskServiceRunWithoutStartCommandRunsImageCmd(t *testing.T) {
	r := newRig(t, nil)
	p := agentprotocol.RunTaskPayload{
		RunID: runID, ServiceID: svcID, ServiceSlug: "web", EnvironmentID: envID,
		ImageRef: "busybox:latest", ServiceRun: true,
	}
	exitWith(r, taskCtr, 0)
	res := r.eng.HandleCommand(context.Background(), taskCmd(t, p, nil))
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, res.Error)
	require.Nil(t, r.dk.created[0].Entrypoint)
	require.Empty(t, r.dk.created[0].Cmd)
}

func TestRunTaskServiceRunRejectsCommandAndUnallowedOptions(t *testing.T) {
	r := newRig(t, nil)
	p := agentprotocol.RunTaskPayload{
		RunID: runID, ServiceID: svcID, ServiceSlug: "web", EnvironmentID: envID,
		ImageRef: "busybox:latest", ServiceRun: true, Command: []string{"ls"},
	}
	res := r.eng.HandleCommand(context.Background(), taskCmd(t, p, nil))
	require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome)
	require.Contains(t, res.Error, "takes no command")

	p.Command = nil
	p.Docker = &agentprotocol.DockerOptions{Privileged: true} // needs an unrestricted server
	res = r.eng.HandleCommand(context.Background(), taskCmd(t, p, nil))
	require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome)
	require.Contains(t, res.Error, "docker options")
	require.Empty(t, r.dk.created)
}

func TestRunTaskServiceRunJoinsStartCommandForShellEntrypointImages(t *testing.T) {
	r := newRig(t, nil)
	r.dk.images["rp:1"] = docker.ImageInfo{ID: "sha256:rp", Entrypoint: []string{"/bin/sh", "-c"}}
	p := agentprotocol.RunTaskPayload{
		RunID: runID, ServiceID: svcID, ServiceSlug: "web", EnvironmentID: envID,
		ImageRef: "rp:1", ServiceRun: true, StartCommand: []string{"node", "my job.js"},
	}
	exitWith(r, taskCtr, 0)
	res := r.eng.HandleCommand(context.Background(), taskCmd(t, p, nil))
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, res.Error)
	require.Equal(t, []string{"node 'my job.js'"}, r.dk.created[0].Cmd)

	// An entrypoint override replaces the shell entrypoint: plain argv.
	r2 := newRig(t, nil)
	r2.dk.images["rp:1"] = docker.ImageInfo{ID: "sha256:rp", Entrypoint: []string{"/bin/sh", "-c"}}
	p.Docker = &agentprotocol.DockerOptions{Entrypoint: []string{"/usr/bin/env"}}
	exitWith(r2, taskCtr, 0)
	res = r2.eng.HandleCommand(context.Background(), taskCmd(t, p, nil))
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, res.Error)
	require.Equal(t, []string{"/usr/bin/env"}, r2.dk.created[0].Entrypoint)
	require.Equal(t, []string{"node", "my job.js"}, r2.dk.created[0].Cmd)
}
