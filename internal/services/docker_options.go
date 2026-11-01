package services

import (
	"context"
	"fmt"
	"path"
	"sort"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// validateDocker checks the format of the service's Docker options. Whether the
// target server allows them is a placement question (CheckDockerPlacement), and
// config files against a read-only root filesystem needs the service's
// volumes (CheckDockerFiles).
func (c Config) validateDocker() error {
	if c.Docker == nil {
		return nil
	}
	if c.Route != nil {
		return invalid("route services have no container: docker options do not apply")
	}
	if err := c.Docker.Validate(true); err != nil {
		return invalid("docker: %s", err)
	}
	for _, t := range c.Docker.Tmpfs {
		for _, f := range c.Files {
			if path.Clean(f.Path) == t.Path {
				return invalid("docker: tmpfs %s collides with the file mount at the same path", t.Path)
			}
		}
	}
	return nil
}

// CodeRequiresUnrestricted is the 422 code for options that need an unrestricted server.
const CodeRequiresUnrestricted = "requires_unrestricted_server"

// CheckDockerPlacement returns 422 requires_unrestricted_server when the Docker
// options need an unrestricted server and the target is not one. It runs on
// create, on every config or placement change, on template install, on plan,
// and again at deploy, rollback and reconcile: disabling the switch blocks the
// next deploy but never stops running containers. serverUnrestricted is the
// server's switch; a service with no server yet is not checked.
func CheckDockerPlacement(cfg Config, serverUnrestricted bool) error {
	if cfg.Route != nil {
		return nil
	}
	fields := cfg.Docker.UnrestrictedFields()
	if len(fields) == 0 || serverUnrestricted {
		return nil
	}
	return apierr.Unprocessable(CodeRequiresUnrestricted,
		(&agentprotocol.RequiresUnrestrictedError{Fields: fields}).Error()+
			" (Servers → the server → Unrestricted). Pick an unrestricted server or remove these options.").
		WithDetails(map[string]any{"fields": fields})
}

// CheckDockerFiles refuses a read-only root filesystem together with config
// files that are not on one of the service's volumes: the files are copied into
// the created container, which Docker refuses for a read-only root.
func CheckDockerFiles(cfg Config, volumes []db.Volume) error {
	if cfg.Docker == nil || !cfg.Docker.ReadOnlyRootfs || len(cfg.Files) == 0 {
		return nil
	}
	files := make([]string, len(cfg.Files))
	for i, f := range cfg.Files {
		files[i] = f.Path
	}
	mounts := make([]string, len(volumes))
	for i, v := range volumes {
		mounts[i] = v.MountPath
	}
	if err := cfg.Docker.ValidateFiles(files, mounts); err != nil {
		return invalid("docker: %s", err)
	}
	return nil
}

// HostRequirementWarnings compares the service's declared host kernel
// parameters (docker.hostSysctls) with the values the server's agent reported
// and returns one warning per shortfall, with the command that fixes it. A
// container cannot set these, so the plan can only tell.
func HostRequirementWarnings(cfg Config, srv *db.Server) []string {
	if cfg.Docker == nil || len(cfg.Docker.HostSysctls) == 0 {
		return nil
	}
	var facts agentprotocol.Facts
	known := false
	if srv != nil {
		if f, ok := ServerFacts(*srv); ok {
			facts, known = f, true
		}
	}
	keys := make([]string, 0, len(cfg.Docker.HostSysctls))
	for k := range cfg.Docker.HostSysctls {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var out []string
	for _, k := range keys {
		want := cfg.Docker.HostSysctls[k]
		have, ok := facts.HostSysctls[k]
		switch {
		case known && ok && have >= want:
		case known && ok:
			out = append(out, fmt.Sprintf("This service needs %s >= %d on the host, but %s has %d. Run `sysctl -w %s=%d` on the server and add `%s = %d` to /etc/sysctl.d/99-nebulactrl.conf to keep it after a reboot.",
				k, want, srv.Name, have, k, want, k, want))
		default:
			out = append(out, fmt.Sprintf("This service needs %s >= %d on the host; the agent has not reported the current value. Check it with `sysctl %s` and raise it with `sysctl -w %s=%d` if needed.", k, want, k, k, want))
		}
	}
	return out
}

// DockerOptionsCatalog lists what the Docker options accept, for the UI.
type DockerOptionsCatalog struct {
	SafeCapabilities         []string `json:"safeCapabilities"`
	WarnCapabilities         []string `json:"warnCapabilities" doc:"Safe-listed capabilities the UI warns about"`
	UnrestrictedCapabilities []string `json:"unrestrictedCapabilities" doc:"Capabilities that need an unrestricted server"`
	KnownCapabilities        []string `json:"knownCapabilities" doc:"Capability names capDrop accepts besides ALL"`
	AllowedSysctls           []string `json:"allowedSysctls" doc:"Container sysctls (IPC and network namespaced) a service may set"`
	HostSysctlKeys           []string `json:"hostSysctlKeys" doc:"Host kernel parameters a service may declare as requirements (hostSysctls)"`
	GPUCapabilities          []string `json:"gpuCapabilities"`
	ReservedLabels           []string `json:"reservedLabels" doc:"Label prefixes services cannot set"`
	StopSignals              []string `json:"stopSignals"`
	UnrestrictedFields       []string `json:"unrestrictedFields" doc:"Options that work only on an unrestricted server"`
}

// DockerOptionsPolicy returns what the Docker options accept (any signed-in caller).
func (s *Service) DockerOptionsPolicy(ctx context.Context) (DockerOptionsCatalog, error) {
	if err := policy.Require(ctx, policy.ScopeProjectsRead, nil); err != nil {
		return DockerOptionsCatalog{}, err
	}
	if _, err := policy.Authenticated(ctx); err != nil {
		return DockerOptionsCatalog{}, err
	}
	return DockerOptionsCatalog{
		SafeCapabilities: agentprotocol.SafeCapabilities, WarnCapabilities: agentprotocol.WarnCapabilities,
		UnrestrictedCapabilities: agentprotocol.UnrestrictedCapabilities, KnownCapabilities: agentprotocol.KnownCapabilities,
		AllowedSysctls: agentprotocol.AllowedSysctls, HostSysctlKeys: agentprotocol.HostSysctlKeys,
		GPUCapabilities: agentprotocol.GPUCapabilities, ReservedLabels: agentprotocol.ReservedLabelPrefixes(),
		StopSignals:        agentprotocol.StopSignals(),
		UnrestrictedFields: []string{"devices", "gpus", "runtime", "privileged", "pidHost", "seccompUnconfined", "apparmorUnconfined", "capAdd NET_ADMIN", "capAdd SYS_ADMIN"},
	}, nil
}
