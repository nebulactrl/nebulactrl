package engine

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

func dockerOpts() *agentprotocol.DockerOptions {
	return &agentprotocol.DockerOptions{
		CapAdd:     []string{"NET_BIND_SERVICE"},
		Ulimits:    []agentprotocol.Ulimit{{Name: "nofile", Soft: 4096, Hard: 8192}},
		ShmSizeMB:  256,
		ExtraHosts: []string{"db.internal:10.0.0.5", "gw:host-gateway"},
		Labels:     map[string]string{"com.example.team": "payments", agentprotocol.LabelService: "ignored-by-validation"},
		Init:       true,
		StopSignal: "SIGQUIT",
		Tmpfs:      []agentprotocol.TmpfsMount{{Path: "/tmp/cache", SizeMB: 32, Mode: "1777"}},
	}
}

func TestDeployAppliesDockerOptionsToTheContainer(t *testing.T) {
	r := newRig(t, nil)
	p := basePayload()
	p.Docker = dockerOpts()
	p.Docker.Labels = map[string]string{"com.example.team": "payments"}

	res, dr := r.deploy(p, nil)
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, dr.Message)
	spec := r.dk.created[0]
	require.Equal(t, []string{"NET_BIND_SERVICE"}, spec.CapAdd)
	require.Equal(t, []docker.Ulimit{{Name: "nofile", Soft: 4096, Hard: 8192}}, spec.Ulimits)
	require.Equal(t, int64(256*1024*1024), spec.ShmSize)
	require.Equal(t, []string{"db.internal:10.0.0.5", "gw:host-gateway"}, spec.ExtraHosts)
	require.True(t, spec.Init)
	require.Equal(t, "SIGQUIT", spec.StopSignal)
	require.Equal(t, map[string]string{"/tmp/cache": "size=32m,mode=1777"}, spec.Tmpfs)
	require.Equal(t, "payments", spec.Labels["com.example.team"])
	require.Equal(t, svcID, spec.Labels[agentprotocol.LabelService], "managed labels are untouched")
}

func TestDockerOptionsSurviveTheSpecLabel(t *testing.T) {
	p := basePayload()
	p.Docker = &agentprotocol.DockerOptions{CapAdd: []string{"cap_chown"}, ShmSizeMB: 64, Init: true}
	r := newRig(t, nil)
	res, dr := r.deploy(p, nil)
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, dr.Message)

	got, ok := payloadFromContainer(docker.ContainerInfo{Labels: r.dk.created[0].Labels})
	require.True(t, ok)
	require.Equal(t, &agentprotocol.DockerOptions{CapAdd: []string{"CHOWN"}, ShmSizeMB: 64, Init: true}, got.Docker,
		"a replica added later by the agent gets the same options")
}

func TestDeployRejectsForbiddenDockerOptions(t *testing.T) {
	for name, o := range map[string]*agentprotocol.DockerOptions{
		"capability": {CapAdd: []string{"SYS_MODULE"}},
		"label":      {Labels: map[string]string{agentprotocol.LabelManaged: "false"}},
		"ulimit":     {Ulimits: []agentprotocol.Ulimit{{Name: "core", Soft: 1, Hard: 1}}},
		"nproc":      {Ulimits: []agentprotocol.Ulimit{{Name: "nproc", Soft: 1, Hard: 1}}},
		"sysctl":     {Sysctls: map[string]string{"vm.max_map_count": "262144"}},
		"dns":        {DNS: []string{"dns.example.com"}},
		"user":       {User: "root; reboot"},
		"tmpfs":      {Tmpfs: []agentprotocol.TmpfsMount{{Path: "/proc"}}},
	} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t, nil)
			p := basePayload()
			p.Docker = o
			raw, err := json.Marshal(p)
			require.NoError(t, err)
			res := r.eng.HandleCommand(t.Context(), agentprotocol.Command{ID: "c", Kind: agentprotocol.KindDeploy, Payload: raw})
			require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome)
			require.Contains(t, res.Error+string(res.Data), "docker options")
			require.Empty(t, r.dk.created, "nothing was created")
		})
	}
}

func TestEntrypointOverrideReachesTheContainerAndDropsTheShellWrapper(t *testing.T) {
	r := newRig(t, nil)
	p := basePayload()
	p.Docker = &agentprotocol.DockerOptions{Entrypoint: []string{"/app/entry.sh", "--verbose"}}
	p.StartCommand = []string{"serve", "--port", "80"}
	res, dr := r.deploy(p, nil)
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, dr.Message)
	spec := r.dk.created[0]
	require.Equal(t, []string{"/app/entry.sh", "--verbose"}, spec.Entrypoint)
	require.Equal(t, []string{"serve", "--port", "80"}, spec.Cmd, "the start command is plain argv for the override")

	// Without an override the spec carries none.
	r = newRig(t, nil)
	p.Docker = nil
	_, _ = r.deploy(p, nil)
	require.Empty(t, r.dk.created[0].Entrypoint)
}

func TestEntrypointOverrideSurvivesThroughTheSpecLabel(t *testing.T) {
	r := newRig(t, nil)
	p := basePayload()
	p.Docker = &agentprotocol.DockerOptions{Entrypoint: []string{"/entry"}}
	_, _ = r.deploy(p, nil)
	got, ok := payloadFromContainer(docker.ContainerInfo{Labels: r.dk.created[0].Labels})
	require.True(t, ok)
	require.Equal(t, []string{"/entry"}, got.Docker.Entrypoint)
}

func TestDeployMapsHardeningOptionsToTheContainer(t *testing.T) {
	r := newRig(t, nil)
	p := basePayload()
	p.Docker = &agentprotocol.DockerOptions{
		CapDrop: []string{"all"}, CapAdd: []string{"net_bind_service"}, NoNewPrivileges: true, ReadOnlyRootfs: true,
		PidsLimit: 256, Hostname: "Rabbit-1", DNS: []string{"10.0.0.2", "1.1.1.1"},
		Sysctls: map[string]string{"net.core.somaxconn": "1024"}, User: "1000:1000", GroupAdd: []string{"122"},
		Tmpfs: []agentprotocol.TmpfsMount{{Path: "/tmp"}},
	}
	res, dr := r.deploy(p, nil)
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, dr.Message)
	spec := r.dk.created[0]
	require.Equal(t, []string{"ALL"}, spec.CapDrop)
	require.Equal(t, []string{"NET_BIND_SERVICE"}, spec.CapAdd)
	require.Equal(t, []string{"no-new-privileges:true"}, spec.SecurityOpt)
	require.True(t, spec.ReadonlyRootfs)
	require.EqualValues(t, 256, spec.PidsLimit)
	require.Equal(t, "rabbit-1", spec.Hostname)
	require.Equal(t, []string{"10.0.0.2", "1.1.1.1"}, spec.DNS)
	require.Equal(t, map[string]string{"net.core.somaxconn": "1024"}, spec.Sysctls)
	require.Equal(t, "1000:1000", spec.User)
	require.Equal(t, []string{"122"}, spec.GroupAdd)
	// Nothing of the unrestricted tier leaks in.
	require.Empty(t, spec.Devices)
	require.Nil(t, spec.GPUs)
	require.False(t, spec.Privileged)
	require.Empty(t, spec.PidMode)
	require.Empty(t, spec.Runtime)
}

func TestHostnameDefaultsToTheServiceSlug(t *testing.T) {
	r := newRig(t, nil)
	res, dr := r.deploy(basePayload(), nil) // no Docker options at all
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, dr.Message)
	require.Equal(t, "web", r.dk.created[0].Hostname)

	require.Equal(t, "web", containerHostname(&agentprotocol.DockerOptions{Init: true}, "web", 0, 1))
	require.Equal(t, "custom", containerHostname(&agentprotocol.DockerOptions{Hostname: "custom"}, "web", 0, 1))
	long := "a" + strings.Repeat("b", 80)
	require.Len(t, containerHostname(nil, long, 0, 1), 63, "a long slug is cut to one DNS label")
	require.Equal(t, "ab", containerHostname(nil, "ab-"+strings.Repeat("-", 5), 0, 1), "no trailing dash")
}

func TestHostnameIsPerReplicaWhenThereAreSeveral(t *testing.T) {
	require.Equal(t, "web-0", containerHostname(nil, "web", 0, 3))
	require.Equal(t, "web-2", containerHostname(nil, "web", 2, 3))
	require.Equal(t, "web", containerHostname(nil, "web", 0, 0), "an unknown count is a single replica")

	// An explicit hostname gets the same suffix, so replicas stay distinct.
	opts := &agentprotocol.DockerOptions{Hostname: "Rabbit"}
	require.Equal(t, "rabbit", containerHostname(opts, "web", 0, 1))
	require.Equal(t, "rabbit-0", containerHostname(opts, "web", 0, 2))
	require.Equal(t, "rabbit-1", containerHostname(opts, "web", 1, 2))

	// The suffix never pushes the name over one DNS label (63 characters).
	long := strings.Repeat("b", 63)
	h := containerHostname(&agentprotocol.DockerOptions{Hostname: long}, "web", 10, 12)
	require.Len(t, h, 63)
	require.True(t, strings.HasSuffix(h, "b-10"), h)
	require.Equal(t, "web", containerHostname(nil, "web-", 0, 1))
	require.Equal(t, "web-1", containerHostname(nil, "web-", 1, 2), "no doubled dash before the suffix")
}

func TestReplicasGetDistinctStableHostnames(t *testing.T) {
	r := newRig(t, nil)
	p := basePayload()
	p.Replicas = 3
	res, dr := r.deploy(p, nil)
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, dr.Message)
	require.Len(t, r.dk.created, 3)
	for i, c := range r.dk.created {
		require.Equal(t, fmt.Sprintf("web-%d", i), c.Hostname)
	}

	// The next deploy gives the same index the same hostname.
	p.DeploymentID = depV1
	res, dr = r.deploy(p, nil)
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, dr.Message)
	require.Len(t, r.dk.created, 6)
	for i, c := range r.dk.created[3:] {
		require.Equal(t, fmt.Sprintf("web-%d", i), c.Hostname)
	}
}

func TestScaleUpNamesNewReplicasByIndex(t *testing.T) {
	r := newRig(t, nil)
	p := basePayload()
	res, dr := r.deploy(p, nil)
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, dr.Message)
	res, dr = r.scale(p, 3)
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, dr.Message)
	require.Len(t, r.dk.created, 3)
	require.Equal(t, "web", r.dk.created[0].Hostname)
	require.Equal(t, "web-1", r.dk.created[1].Hostname)
	require.Equal(t, "web-2", r.dk.created[2].Hostname)
}

func unrestrictedOptions() *agentprotocol.DockerOptions {
	return &agentprotocol.DockerOptions{
		Devices:           []agentprotocol.DeviceMapping{{HostPath: "/dev/dri/renderD128", Permissions: "rw"}, {HostPath: "/dev/kvm", ContainerPath: "/dev/kvm"}},
		GPUs:              &agentprotocol.GPURequest{Count: -1, Driver: "nvidia", Capabilities: []string{"compute", "utility"}},
		Runtime:           "nvidia",
		Privileged:        true,
		PidHost:           true,
		SeccompUnconfined: true, ApparmorUnconfined: true,
		CapAdd: []string{"NET_ADMIN", "SYS_ADMIN"},
	}
}

func TestUnrestrictedOptionsReachTheContainerOnlyWithThePolicy(t *testing.T) {
	// Restricted (no policy at all, or an explicit false): refused, nothing created.
	for name, policy := range map[string]*agentprotocol.DockerPolicy{"no policy": nil, "restricted": {Unrestricted: false}} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t, nil)
			p := basePayload()
			p.Docker, p.DockerPolicy = unrestrictedOptions(), policy
			raw, err := json.Marshal(p)
			require.NoError(t, err)
			res := r.eng.HandleCommand(t.Context(), agentprotocol.Command{ID: "c", Kind: agentprotocol.KindDeploy, Payload: raw})
			require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome)
			require.Contains(t, res.Error+string(res.Data), "unrestricted server")
			require.Empty(t, r.dk.created, "nothing was created")
		})
	}

	// Each unrestricted option alone is refused on a restricted server too.
	for name, o := range map[string]*agentprotocol.DockerOptions{
		"devices":    {Devices: []agentprotocol.DeviceMapping{{HostPath: "/dev/kvm"}}},
		"gpus":       {GPUs: &agentprotocol.GPURequest{Count: 1, Driver: "nvidia"}},
		"runtime":    {Runtime: "runsc"},
		"privileged": {Privileged: true},
		"pidHost":    {PidHost: true},
		"seccomp":    {SeccompUnconfined: true},
		"apparmor":   {ApparmorUnconfined: true},
		"NET_ADMIN":  {CapAdd: []string{"NET_ADMIN"}},
		"SYS_ADMIN":  {CapAdd: []string{"SYS_ADMIN"}},
	} {
		p := basePayload()
		p.Docker = o
		require.Error(t, validateDockerOptions(p), name)
		p.DockerPolicy = &agentprotocol.DockerPolicy{Unrestricted: true}
		require.NoError(t, validateDockerOptions(p), name)
	}

	// Unrestricted: everything is mapped.
	r := newRig(t, nil)
	p := basePayload()
	p.Docker, p.DockerPolicy = unrestrictedOptions(), &agentprotocol.DockerPolicy{Unrestricted: true}
	res, dr := r.deploy(p, nil)
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, dr.Message)
	spec := r.dk.created[0]
	require.Equal(t, []docker.DeviceMapping{
		{HostPath: "/dev/dri/renderD128", Permissions: "rw"}, {HostPath: "/dev/kvm", ContainerPath: "/dev/kvm"},
	}, spec.Devices)
	require.Equal(t, &docker.GPURequest{Count: -1, Driver: "nvidia", Capabilities: []string{"compute", "utility"}}, spec.GPUs)
	require.Equal(t, "nvidia", spec.Runtime)
	require.True(t, spec.Privileged)
	require.Equal(t, "host", spec.PidMode)
	require.ElementsMatch(t, []string{"seccomp=unconfined", "apparmor=unconfined"}, spec.SecurityOpt)
	require.Equal(t, []string{"NET_ADMIN", "SYS_ADMIN"}, spec.CapAdd)
}

func TestDockerPolicySurvivesTheSpecLabel(t *testing.T) {
	r := newRig(t, nil)
	p := basePayload()
	p.Docker = &agentprotocol.DockerOptions{Privileged: true}
	p.DockerPolicy = &agentprotocol.DockerPolicy{Unrestricted: true}
	res, dr := r.deploy(p, nil)
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, dr.Message)
	got, ok := payloadFromContainer(docker.ContainerInfo{Labels: r.dk.created[0].Labels})
	require.True(t, ok)
	require.Equal(t, &agentprotocol.DockerPolicy{Unrestricted: true}, got.DockerPolicy, "a replica added later is validated against the same policy")
	require.NoError(t, validateDockerOptions(got))
}

func TestReadOnlyRootfsNeedsFilesOnAVolume(t *testing.T) {
	p := basePayload()
	p.Docker = &agentprotocol.DockerOptions{ReadOnlyRootfs: true, Tmpfs: []agentprotocol.TmpfsMount{{Path: "/scratch"}}}
	p.Files = []agentprotocol.FileSpec{{Path: "/etc/app.conf", Content: "x"}}
	require.ErrorContains(t, validateDockerOptions(p), "read-only", "docker cp cannot write into a read-only root")

	p.Files = []agentprotocol.FileSpec{{Path: "/scratch/app.conf", Content: "x"}}
	require.Error(t, validateDockerOptions(p), "a tmpfs is mounted over files copied before start")

	p.Volumes = []agentprotocol.VolumeMount{{Name: "nc-vol-a-data", MountPath: "/data"}}
	p.Files = []agentprotocol.FileSpec{{Path: "/data/conf/app.conf", Content: "x"}}
	require.NoError(t, validateDockerOptions(p), "a file on a volume mount is writable")

	p.Docker.ReadOnlyRootfs = false
	p.Files = []agentprotocol.FileSpec{{Path: "/etc/app.conf", Content: "x"}}
	require.NoError(t, validateDockerOptions(p), "writable root: files anywhere")
}
