package engine

import (
	"strconv"
	"strings"

	"github.com/nebulactrl/nebulactrl/internal/agent/build"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// buildVars collects the build-time variables delivered with a deploy command.
// Plain build args and secrets both arrive as Secrets["build:<KEY>"]; the
// payload says which is which. Secret values never reach argv (see build.Vars).
func (d *deploy) buildVars() build.Vars {
	v := build.Vars{Args: map[string]string{}, Secrets: map[string]string{}}
	for _, k := range d.p.BuildArgKeys {
		if val, ok := d.cmd.Secrets[agentprotocol.SecretBuildPrefix+k]; ok {
			v.Args[k] = val
		}
	}
	for _, k := range d.p.BuildSecretKeys {
		if val, ok := d.cmd.Secrets[agentprotocol.SecretBuildPrefix+k]; ok {
			v.Secrets[k] = val
		}
	}
	return v
}

// secretKeysForRedaction lists the secret command keys (variables and build
// secrets) the redactor masks, in the `env:` / `build:` namespaces.
func secretKeysForRedaction(p agentprotocol.DeployPayload) []string {
	keys := append([]string(nil), p.SecretEnvKeys...)
	for _, k := range p.BuildSecretKeys {
		keys = append(keys, agentprotocol.SecretBuildPrefix+k)
	}
	return keys
}

// replicaEnv returns env with the container-scoped platform variables of
// replica idx of count set (existing NEBULA_REPLICA_* entries, e.g. cloned from
// a running replica, are replaced).
func replicaEnv(env []string, idx, count int) []string {
	out := make([]string, 0, len(env)+2)
	for _, e := range env {
		if strings.HasPrefix(e, agentprotocol.ReplicaIndexEnv+"=") || strings.HasPrefix(e, agentprotocol.ReplicaCountEnv+"=") {
			continue
		}
		out = append(out, e)
	}
	return append(out, agentprotocol.ReplicaIndexEnv+"="+strconv.Itoa(idx), agentprotocol.ReplicaCountEnv+"="+strconv.Itoa(max(count, 1)))
}

// processEnvVar names the process in the environment of every container of a
// multi-process deployment.
const processEnvVar = "NEBULA_PROCESS"

// processEnv returns env with NEBULA_PROCESS set to process (an entry cloned
// from a running replica is replaced). A single-process deployment has none.
func processEnv(env []string, process string) []string {
	if process == "" {
		return env
	}
	out := make([]string, 0, len(env)+1)
	for _, e := range env {
		if !strings.HasPrefix(e, processEnvVar+"=") {
			out = append(out, e)
		}
	}
	return append(out, processEnvVar+"="+process)
}

// replicaCount is the replica count recorded in the environment of the
// replicas this command creates.
func (d *deploy) replicaCount() int {
	if d.index != nil { // scale: the command carries the final count
		return d.p.Replicas
	}
	return len(d.names)
}
