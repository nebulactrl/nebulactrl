package engine

import (
	"strconv"
	"strings"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// containerHostname is the hostname of replica idx of count of a service's
// containers: the configured one, else the service slug (an RFC 1123 label, so
// at most 63 characters). With several replicas every one gets
// "<name>-<idx>", so software that identifies itself by hostname (clustering,
// load-balancer tests) sees distinct nodes; a single replica keeps the plain
// name. The name depends only on the base name, the index and the count, so
// it survives every deploy; Docker's default, the container ID, changes with
// each one and breaks software that stores data by node name.
func containerHostname(o *agentprotocol.DockerOptions, slug string, idx, count int) string {
	base := strings.ToLower(slug)
	if o != nil && strings.TrimSpace(o.Hostname) != "" {
		base = strings.ToLower(strings.TrimSpace(o.Hostname))
	}
	suffix := ""
	if count > 1 {
		suffix = "-" + strconv.Itoa(idx)
	}
	if len(base) > 63-len(suffix) {
		base = base[:63-len(suffix)]
	}
	return strings.Trim(base, "-") + suffix
}

// dockerUnrestricted reports whether the payload's server policy lets Docker
// options reach the host (a missing policy is a restricted server).
func dockerUnrestricted(p agentprotocol.DeployPayload) bool {
	return p.DockerPolicy != nil && p.DockerPolicy.Unrestricted
}

// validateDockerOptions is the agent's re-validation of a payload's Docker
// options against the server policy it carries, and of the read-only root
// filesystem against the payload's files and volumes. Nothing is created when it fails.
func validateDockerOptions(p agentprotocol.DeployPayload) error {
	if err := p.Docker.Validate(dockerUnrestricted(p)); err != nil {
		return err
	}
	if p.Docker == nil || !p.Docker.ReadOnlyRootfs {
		return nil
	}
	files := make([]string, len(p.Files))
	for i, f := range p.Files {
		files[i] = f.Path
	}
	mounts := make([]string, len(p.Volumes))
	for i, v := range p.Volumes {
		mounts[i] = v.MountPath
	}
	return p.Docker.ValidateFiles(files, mounts)
}

// applyDockerOptions adds the service's Docker options to a container spec and
// sets its hostname. The payload was validated (validateDockerOptions) before
// the deploy started. A user label never replaces a label that is already set
// (validation also reserves the nebulactrl.* prefix).
func applyDockerOptions(spec *docker.ContainerSpec, o *agentprotocol.DockerOptions, slug string, idx, count int) {
	spec.Hostname = containerHostname(o, slug, idx, count)
	if o.IsZero() {
		return
	}
	o = o.Normalized()
	for k, v := range o.Labels {
		if _, taken := spec.Labels[k]; !taken {
			spec.Labels[k] = v
		}
	}
	spec.CapAdd = append(spec.CapAdd, o.CapAdd...)
	spec.CapDrop = append(spec.CapDrop, o.CapDrop...)
	if o.NoNewPrivileges {
		spec.SecurityOpt = append(spec.SecurityOpt, "no-new-privileges:true")
	}
	if o.SeccompUnconfined {
		spec.SecurityOpt = append(spec.SecurityOpt, "seccomp=unconfined")
	}
	if o.ApparmorUnconfined {
		spec.SecurityOpt = append(spec.SecurityOpt, "apparmor=unconfined")
	}
	spec.ReadonlyRootfs = o.ReadOnlyRootfs
	spec.PidsLimit = o.PidsLimit
	spec.DNS = append(spec.DNS, o.DNS...)
	if len(o.Sysctls) > 0 {
		spec.Sysctls = make(map[string]string, len(o.Sysctls))
		for k, v := range o.Sysctls {
			spec.Sysctls[k] = v
		}
	}
	spec.User = o.User
	spec.GroupAdd = append(spec.GroupAdd, o.GroupAdd...)
	for _, u := range o.Ulimits {
		spec.Ulimits = append(spec.Ulimits, docker.Ulimit{Name: u.Name, Soft: u.Soft, Hard: u.Hard})
	}
	if o.ShmSizeMB > 0 {
		spec.ShmSize = int64(o.ShmSizeMB) * 1024 * 1024
	}
	spec.ExtraHosts = append(spec.ExtraHosts, o.ExtraHosts...)
	spec.Init = o.Init
	spec.StopSignal = strings.ToUpper(o.StopSignal)
	if len(o.Entrypoint) > 0 {
		// Like `docker run --entrypoint`: the image's default command is
		// dropped, the service's start command (Cmd) is passed as arguments.
		spec.Entrypoint = append([]string(nil), o.Entrypoint...)
	}
	if len(o.Tmpfs) > 0 {
		spec.Tmpfs = make(map[string]string, len(o.Tmpfs))
		for _, t := range o.Tmpfs {
			spec.Tmpfs[t.Path] = t.TmpfsOption()
		}
	}

	// Unrestricted servers only (validateDockerOptions refused them otherwise).
	for _, d := range o.Devices {
		spec.Devices = append(spec.Devices, docker.DeviceMapping{HostPath: d.HostPath, ContainerPath: d.ContainerPath, Permissions: d.Permissions})
	}
	if g := o.GPUs; g != nil {
		spec.GPUs = &docker.GPURequest{Count: g.Count, Driver: g.Driver, Capabilities: append([]string(nil), g.Capabilities...)}
	}
	spec.Runtime = o.Runtime
	spec.Privileged = o.Privileged
	if o.PidHost {
		spec.PidMode = "host"
	}
}
