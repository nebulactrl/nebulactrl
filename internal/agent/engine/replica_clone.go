package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/internal/agent/logs"
	"github.com/nebulactrl/nebulactrl/internal/databases/engines"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

const (
	// defaultCloneTimeout bounds a clone that names no timeout.
	defaultCloneTimeout = 6 * time.Hour
	// maxCloneTimeout is the longest a clone may be asked to run.
	maxCloneTimeout = 24 * time.Hour
	// cloneNotEmptyExit is the clone script's exit code for a data volume that
	// already holds a cluster; nothing was changed then.
	cloneNotEmptyExit = 4
)

func cloneResult(id, outcome, msg string, started time.Time) agentprotocol.ResultFrame {
	f := agentprotocol.ResultFrame{
		CommandID: id, Outcome: outcome,
		Data: agentprotocol.MarshalResultData(agentprotocol.ReplicaCloneResult{Message: msg, DurationMs: time.Since(started).Milliseconds()}),
	}
	if outcome != agentprotocol.OutcomeSucceeded {
		f.Error = msg
	}
	return f
}

// cloneNetwork validates a network the clone joins: only the managed
// environment networks (and their internal siblings) are allowed.
func cloneNetwork(name string) (internal bool, err error) {
	if !strings.HasPrefix(name, agentprotocol.EnvNetworkPrefix) || len(name) > 64 || strings.ContainsAny(name, " /:,") {
		return false, fmt.Errorf("network %q is not a managed environment network", name)
	}
	return strings.HasSuffix(name, agentprotocol.EnvInternalSuffix), nil
}

// handleReplicaClone runs pg_basebackup into the replica's data volume in a
// one-off container of the replica's image on the networks the replica will use.
// The replication password comes from Command.Secrets and the
// container's environment only. A failed or canceled clone deletes the
// half-written volume (never one that was not empty to begin with).
func (e *Engine) handleReplicaClone(ctx context.Context, cmd agentprotocol.Command) agentprotocol.ResultFrame {
	started := time.Now()
	var p agentprotocol.ReplicaClonePayload
	if err := json.Unmarshal(cmd.Payload, &p); err != nil {
		return failedResult(cmd.ID, "invalid db_replica_clone payload: "+err.Error())
	}
	spec := engines.Spec{Engine: p.Engine, Version: p.Version, Flavor: p.Flavor}
	switch {
	case p.ServiceID == "":
		return failedResult(cmd.ID, "serviceId is required")
	case spec.Engine != engines.Postgres:
		return failedResult(cmd.ID, engines.ErrNotPostgres.Error())
	case !engines.ValidVolumeName(p.DataVolume):
		return failedResult(cmd.ID, "invalid data volume name")
	case len(p.Networks) == 0:
		return failedResult(cmd.ID, "the clone needs at least one network")
	}
	image := spec.Image()
	if image == "" {
		return failedResult(cmd.ID, fmt.Sprintf("PostgreSQL %s%s is not an offered version", p.Version, flavorSuffix(p.Flavor)))
	}
	password := cmd.Secrets[agentprotocol.SecretReplPassword]
	env, err := engines.CloneEnv(p.Host, p.Port, p.User, p.Slot, password)
	if err != nil {
		return failedResult(cmd.ID, err.Error())
	}
	attach := make([]docker.NetworkAttachment, 0, len(p.Networks))
	type netReq struct {
		name     string
		internal bool
	}
	var nets []netReq
	for _, n := range p.Networks {
		internal, err := cloneNetwork(n)
		if err != nil {
			return failedResult(cmd.ID, err.Error())
		}
		nets = append(nets, netReq{n, internal})
		attach = append(attach, docker.NetworkAttachment{Network: n})
	}
	timeout := defaultCloneTimeout
	if p.TimeoutSeconds > 0 {
		timeout = min(time.Duration(p.TimeoutSeconds)*time.Second, maxCloneTimeout)
	}

	rd := newRedactor(cmd.Secrets, nil)
	// A cancel_deploy naming this command's id aborts the clone.
	ctx, _, release := e.cancel.start(ctx, cmd.ID)
	defer release()
	defer e.lockService(p.ServiceID)()

	var lb *logs.Batcher
	say := func(format string, a ...any) {
		line := fmt.Sprintf(format, a...)
		e.log.Info(rd.apply(line), "service_id", p.ServiceID, "command_id", cmd.ID)
		if lb != nil {
			lb.Add(line)
		}
	}
	if p.DeploymentID != "" {
		lb = logs.NewBatcher(agentprotocol.LogFrame{
			DeploymentID: p.DeploymentID, ServiceID: p.ServiceID, Stream: agentprotocol.StreamDeploy,
		}, e.cfg.Sender.Log, rd.apply)
		defer lb.Close()
	}

	for _, c := range e.currentReplicas(ctx, p.ServiceID) {
		if c.Running || c.Restarting {
			return cloneResult(cmd.ID, agentprotocol.OutcomeFailed, "the replica is running; stop it before cloning", started)
		}
	}

	d := e.cfg.Docker
	say("Cloning %s:%d as %s (slot %s) into volume %s", p.Host, p.Port, p.User, p.Slot, p.DataVolume)
	for _, n := range nets {
		labels := map[string]string{agentprotocol.LabelManaged: "true"}
		if err := e.ensureNetwork(ctx, n.name, labels, n.internal); err != nil {
			return cloneResult(cmd.ID, agentprotocol.OutcomeFailed, "ensure network "+n.name+": "+err.Error(), started)
		}
	}
	if _, err := d.ImageInspect(ctx, image); err != nil {
		say("Pulling %s", image)
		if err := d.ImagePull(ctx, image, "", nil); err != nil {
			return cloneResult(cmd.ID, agentprotocol.OutcomeFailed, rd.apply("pull "+image+": "+err.Error()), started)
		}
	}
	vol := agentprotocol.VolumeName(p.ServiceID, p.DataVolume)
	if err := d.EnsureVolume(ctx, vol, map[string]string{agentprotocol.LabelManaged: "true", agentprotocol.LabelService: p.ServiceID}); err != nil {
		return cloneResult(cmd.ID, agentprotocol.OutcomeFailed, "ensure volume: "+err.Error(), started)
	}

	_, mount := spec.DataMount()
	cs := docker.ContainerSpec{
		Name: "nc-replclone-" + agentprotocol.Short(p.ServiceID), Image: image,
		Entrypoint: []string{"sh", "-c", engines.CloneScript()},
		Env:        env,
		Labels:     map[string]string{agentprotocol.LabelManaged: "pg-replication", agentprotocol.LabelService: p.ServiceID},
		Networks:   attach,
		Mounts:     []docker.VolumeMount{{Volume: vol, Target: mount}},
		// The script runs as root only to chown the data directory to postgres and
		// drop to that user with gosu; no other capability is needed.
		CapDrop: []string{"ALL"}, CapAdd: []string{"CHOWN", "DAC_OVERRIDE", "FOWNER", "SETUID", "SETGID"},
		SecurityOpt: []string{"no-new-privileges:true"},
	}
	code, out, runErr := e.runHelper(ctx, cs, timeout, func(line string) {
		if lb != nil {
			lb.Add(line)
		}
	})

	discard := func() {
		cctx, cancel := detached(ctx, time.Minute)
		defer cancel()
		if err := d.RemoveVolume(cctx, vol, true); err != nil && !errors.Is(err, docker.ErrNotFound) {
			e.log.Warn("remove the half-written replica volume", "volume", vol, "err", err)
		}
	}
	switch {
	case runErr != nil && canceled(ctx):
		discard()
		say("Clone canceled; the half-written volume was removed")
		return cloneResult(cmd.ID, agentprotocol.OutcomeCanceled, "The clone was canceled; the half-written replica volume was removed.", started)
	case runErr != nil:
		discard()
		return cloneResult(cmd.ID, agentprotocol.OutcomeFailed, rd.apply("clone: "+runErr.Error()+"; the half-written replica volume was removed"), started)
	case code == cloneNotEmptyExit:
		return cloneResult(cmd.ID, agentprotocol.OutcomeFailed, "The replica's data volume is not empty; nothing was changed. Rebuild the replica on a new volume.", started)
	case code != 0:
		discard()
		return cloneResult(cmd.ID, agentprotocol.OutcomeFailed,
			rd.apply(fmt.Sprintf("pg_basebackup failed (exit code %d); the half-written replica volume was removed: %s", code, tail(out))), started)
	}
	say("Clone finished in %s", time.Since(started).Round(time.Second))
	return cloneResult(cmd.ID, agentprotocol.OutcomeSucceeded, "Cloned the primary into the replica's data volume.", started)
}

func flavorSuffix(f string) string {
	if f == "" {
		return ""
	}
	return " (" + f + ")"
}
