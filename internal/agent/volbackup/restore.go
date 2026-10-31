package volbackup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// restoreCaps are the capabilities a restore needs on top of a copy: set
// owners, modes, xattrs and special files.
var restoreCaps = []string{"CHOWN", "DAC_OVERRIDE", "DAC_READ_SEARCH", "FOWNER", "FSETID", "MKNOD", "SETFCAP"}

// clearScript empties the restore target (including dotfiles).
const clearScript = `find /restore -mindepth 1 -maxdepth 1 -exec rm -rf {} +`

// Restore restores /vol/<name> of a snapshot into a Docker volume: a NEW volume
// (created here, removed again when the restore fails), or - InPlace - the
// service's own volume, with the service stopped for the duration.
func (r *Runner) Restore(ctx context.Context, p agentprotocol.VolumeRestorePayload, secrets map[string]string, logf func(string)) (agentprotocol.VolumeRestoreResult, error) {
	if logf == nil {
		logf = nopLog
	}
	res := agentprotocol.VolumeRestoreResult{Outcome: agentprotocol.OutcomeFailed, StartedAt: r.now().UTC()}
	fail := func(format string, a ...any) (agentprotocol.VolumeRestoreResult, error) {
		res.Message = scrub(fmt.Sprintf(format, a...), secrets)
		res.FinishedAt = r.now().UTC()
		return res, errors.New(res.Message)
	}
	if p.RestoreID == "" || p.ServiceID == "" || p.SnapshotID == "" {
		return fail("invalid volume_restore payload: restoreId, serviceId and snapshotId are required")
	}
	if err := validRef(p.Volume); err != nil {
		return fail("%v", err)
	}
	if !isSnapshotID(p.SnapshotID) {
		return fail("invalid snapshot id %q", p.SnapshotID)
	}
	ctx, cancel := context.WithTimeout(ctx, agentprotocol.VolumeBackupMaxSeconds*time.Second)
	defer cancel()
	defer r.track(p.RestoreID)()

	spec, destMounts, network, err := r.destination(ctx, p.Repo)
	if err != nil {
		return fail("%v", err)
	}
	unlockRepo, err := r.lockRepo(ctx, p.ServiceID)
	if err != nil {
		return fail("waiting for another backup of this service: %v", err)
	}
	defer unlockRepo()

	var cont docker.ContainerInfo
	var wasRunning bool
	if p.InPlace {
		unlock, err := r.Host.LockService(ctx, p.ServiceID, time.Duration(agentprotocol.VolumeBackupWaitSeconds)*time.Second)
		if err != nil {
			if errors.Is(err, ErrBusy) {
				return fail("A deployment or another action on this service is in progress; nothing was restored.")
			}
			return fail("%v", err)
		}
		defer unlock()
		cont, wasRunning = r.Host.ServiceContainer(ctx, p.ServiceID)
		wasRunning = wasRunning && cont.Running
	} else if err := r.Docker.EnsureVolume(ctx, p.Volume.DockerName, map[string]string{
		agentprotocol.LabelBackupService: p.ServiceID, "nebulactrl.restored-from": p.SnapshotID,
	}); err != nil {
		return fail("create the new volume: %v", err)
	}
	cleanupNew := func() {
		if !p.InPlace {
			_ = r.Docker.RemoveVolume(context.WithoutCancel(ctx), p.Volume.DockerName, true)
		}
	}

	mounts := append(append([]docker.VolumeMount(nil), destMounts...),
		docker.VolumeMount{Volume: p.Volume.DockerName, Target: agentprotocol.HelperRestore})
	h, err := r.newHelper(ctx, helperOpts{
		op: p.RestoreID, kind: "r", serviceID: p.ServiceID, mounts: mounts, network: network, caps: restoreCaps, repo: spec, secrets: secrets,
	}, logf)
	if err != nil {
		cleanupNew()
		return fail("%v", err)
	}
	defer h.close()
	exists, err := h.openRepo(ctx, false)
	if err != nil || !exists {
		cleanupNew()
		if err == nil {
			err = errors.New("the backup repository was not found at the destination")
		}
		return fail("%v", err)
	}

	started := true
	stoppedAt := time.Time{}
	if p.InPlace && wasRunning {
		grace := 30 * time.Second
		if p.StopGraceSeconds > 0 {
			grace = time.Duration(p.StopGraceSeconds) * time.Second
		}
		logf("stopping the service for the in-place restore")
		if err := r.Docker.ContainerStop(ctx, cont.ID, &grace); err != nil {
			return fail("could not stop the service: %v", err)
		}
		started, stoppedAt = false, time.Now()
	}
	startAgain := func() error {
		if started {
			return nil
		}
		sctx, scancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
		defer scancel()
		var err error
		for i := range 3 {
			if err = r.Docker.ContainerStart(sctx, cont.ID); err == nil {
				started = true
				res.StoppedMs = time.Since(stoppedAt).Milliseconds()
				return nil
			}
			time.Sleep(time.Duration(i+1) * 500 * time.Millisecond)
		}
		return err
	}
	defer func() { _ = startAgain() }()

	if p.InPlace {
		logf("emptying the volume")
		code, err := h.exec(ctx, []string{"sh", "-c", clearScript}, nil, io.Discard, &tailBuf{})
		if err != nil || code != 0 {
			return fail("empty the volume: %s", execFailure(code, err))
		}
	}
	logf("restoring " + p.Volume.Name)
	if err := h.resticOK(ctx, nil, "restore", p.SnapshotID+":"+agentprotocol.HelperVolRoot+"/"+p.Volume.Name, "--target", agentprotocol.HelperRestore); err != nil {
		cleanupNew()
		if p.InPlace {
			return fail("%v. The volume may be incomplete; the safety backup taken before the restore holds the previous data", err)
		}
		return fail("%v", err)
	}
	if err := startAgain(); err != nil {
		return fail("the data was restored but the service could not be started again (%v); start it from the dashboard", err)
	}
	res.Outcome, res.DockerName = agentprotocol.OutcomeSucceeded, p.Volume.DockerName
	res.Message = "restored"
	res.FinishedAt = r.now().UTC()
	logf(res.Message)
	return res, nil
}

// isSnapshotID reports whether s looks like a restic snapshot id (hex), so it
// can never be read as an option or a path.
func isSnapshotID(s string) bool {
	if len(s) < 8 || len(s) > 64 {
		return false
	}
	return strings.Trim(s, "0123456789abcdef") == ""
}

// RemoveVolumes deletes Docker volumes. Only volumes of this product are
// accepted; a volume in use is an error.
func (r *Runner) RemoveVolumes(ctx context.Context, p agentprotocol.VolumeRemovePayload) (agentprotocol.VolumeRemoveResult, error) {
	res := agentprotocol.VolumeRemoveResult{Outcome: agentprotocol.OutcomeFailed}
	var errs []string
	for _, n := range p.Names {
		if !strings.HasPrefix(n, agentprotocol.VolumePrefix) || strings.ContainsAny(n, " \t\n:/\\") {
			errs = append(errs, fmt.Sprintf("refusing to remove %q: not a NebulaCtrl volume", n))
			continue
		}
		if err := r.Docker.RemoveVolume(ctx, n, false); err != nil && !errors.Is(err, docker.ErrNotFound) {
			errs = append(errs, err.Error())
		}
	}
	if len(errs) > 0 {
		res.Message = strings.Join(errs, "; ")
		return res, errors.New(res.Message)
	}
	res.Outcome, res.Message = agentprotocol.OutcomeSucceeded, fmt.Sprintf("removed %d volume(s)", len(p.Names))
	return res, nil
}
