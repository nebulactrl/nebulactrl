package volbackup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

var volumeNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,62}$`)

// validRef checks a volume reference: a safe snapshot directory name and a
// Docker volume this product owns.
func validRef(v agentprotocol.VolumeRef) error {
	if !volumeNameRe.MatchString(v.Name) {
		return fmt.Errorf("invalid volume name %q", v.Name)
	}
	if !strings.HasPrefix(v.DockerName, agentprotocol.VolumePrefix) || strings.ContainsAny(v.DockerName, " \t\n:/\\") {
		return fmt.Errorf("invalid Docker volume %q", v.DockerName)
	}
	return nil
}

func validHook(h *agentprotocol.VolumeHook) error {
	if h == nil {
		return nil
	}
	if len(h.Command) == 0 || h.Command[0] == "" {
		return errors.New("a backup command needs a program to run")
	}
	return nil
}

func hookTimeout(h *agentprotocol.VolumeHook) time.Duration {
	s := h.TimeoutSeconds
	if s <= 0 {
		s = agentprotocol.DefaultHookTimeoutSeconds
	}
	return time.Duration(min(s, agentprotocol.MaxHookTimeoutSeconds)) * time.Second
}

// copyScript copies every volume from /src/<name> to /vol/<name>.
const copyScript = `set -e
for n in "$@"; do
  mkdir -p "/vol/$n"
  cp -a --reflink=auto "/src/$n/." "/vol/$n/"
done`

// reflinkProbe succeeds (exit 0) when the staging filesystem can clone files.
const reflinkProbe = `echo probe > /vol/.nc-probe-a && cp --reflink=always /vol/.nc-probe-a /vol/.nc-probe-b; rc=$?; rm -f /vol/.nc-probe-a /vol/.nc-probe-b; exit $rc`

// diskProbe prints the used KiB of the sources and the free KiB of the staging volume.
const diskProbe = `used=0
for n in "$@"; do
  u=$(du -sk "/src/$n" | cut -f1)
  used=$((used + u))
done
free=$(df -Pk /vol | tail -n 1 | tr -s ' ' | cut -d' ' -f4)
echo "$used $free"`

// Backup takes one backup and returns its result. The returned error carries
// the failure message (also in res.Message); a skipped backup returns a nil
// error with Outcome == OutcomeSkipped.
func (r *Runner) Backup(ctx context.Context, p agentprotocol.VolumeBackupPayload, secrets map[string]string, logf func(string)) (agentprotocol.VolumeBackupResult, error) {
	if logf == nil {
		logf = nopLog
	}
	res := agentprotocol.VolumeBackupResult{Outcome: agentprotocol.OutcomeFailed, StartedAt: r.now().UTC()}
	fail := func(format string, a ...any) (agentprotocol.VolumeBackupResult, error) {
		res.Message = scrub(fmt.Sprintf(format, a...), secrets)
		res.FinishedAt = r.now().UTC()
		return res, errors.New(res.Message)
	}
	if err := validateBackup(&p); err != nil {
		return fail("%v", err)
	}
	ctx, cancel := context.WithTimeout(ctx, agentprotocol.VolumeBackupMaxSeconds*time.Second)
	defer cancel()
	defer r.track(p.BackupID)()

	spec, destMounts, network, err := r.destination(ctx, p.Repo)
	if err != nil {
		return fail("%v", err)
	}
	unlockRepo, err := r.lockRepo(ctx, p.ServiceID)
	if err != nil {
		return fail("waiting for another backup of this service: %v", err)
	}
	defer unlockRepo()

	cont, hasCont := r.Host.ServiceContainer(ctx, p.ServiceID)
	running := hasCont && cont.Running
	// A service that is not running writes nothing: its volumes are read
	// directly, without a freeze (consistent by definition). Live mode reads
	// the running service's volumes directly too.
	direct := p.Mode == agentprotocol.VolModeLive || !running
	switch {
	case !running:
		res.Consistency, res.StageMethod = agentprotocol.VolModeStop, agentprotocol.StageNone
		logf("the service is not running; its volumes are read directly")
	case direct:
		res.Consistency, res.StageMethod = agentprotocol.VolModeLive, agentprotocol.StageNone
	default:
		res.Consistency = p.Mode
	}

	// 1. The helper: sources (ro) + a staging volume, or just the live volumes.
	mounts := append([]docker.VolumeMount(nil), destMounts...)
	hopts := helperOpts{op: p.BackupID, kind: "b", serviceID: p.ServiceID, network: network, repo: spec, secrets: secrets}
	stageVol := ""
	if direct {
		for _, v := range p.Volumes {
			mounts = append(mounts, docker.VolumeMount{Volume: v.DockerName, Target: agentprotocol.HelperVolRoot + "/" + v.Name, ReadOnly: true})
		}
	} else {
		stageVol = agentprotocol.BackupStagePrefix + agentprotocol.Short(p.BackupID)
		if err := r.Docker.EnsureVolume(ctx, stageVol, map[string]string{
			agentprotocol.LabelHelper: agentprotocol.HelperBackup, agentprotocol.LabelBackupID: p.BackupID,
		}); err != nil {
			return fail("create the staging volume: %v", err)
		}
		mounts = append(mounts, docker.VolumeMount{Volume: stageVol, Target: agentprotocol.HelperVolRoot})
		for _, v := range p.Volumes {
			mounts = append(mounts, docker.VolumeMount{Volume: v.DockerName, Target: agentprotocol.HelperSrcRoot + "/" + v.Name, ReadOnly: true})
		}
		hopts.container, hopts.mode = cont.ID, p.Mode
	}
	hopts.mounts, hopts.stageVolume = mounts, stageVol
	h, err := r.newHelper(ctx, hopts, logf)
	if err != nil {
		if stageVol != "" {
			_ = r.Docker.RemoveVolume(context.WithoutCancel(ctx), stageVol, true)
		}
		return fail("%v", err)
	}
	defer h.close()

	// 2. The repository.
	if _, err := h.openRepo(ctx, true); err != nil {
		return fail("%v", err)
	}

	// 3. Disk and reflink checks (staged copies only).
	names := volumeNames(p.Volumes)
	if !direct {
		method, err := r.preflight(ctx, h, names)
		if err != nil {
			return fail("%v", err)
		}
		res.StageMethod = method
	}

	// 4. Before-backup command: runs while the container is running and not
	// frozen (exec into a paused container is refused).
	var postOnce sync.Once
	runPost := func() {}
	if running {
		if p.PreHook != nil {
			logf("running the before-backup command")
			if herr := r.runHook(ctx, cont, p.PreHook); herr != nil {
				if !p.PreHook.ContinueOnError {
					r.runPost(ctx, cont, p.PostHook, &res, logf)
					return fail("The before-backup command %v. Nothing was backed up.", herr)
				}
				res.HookOutcome = "failed_continued"
				res.Warnings = append(res.Warnings, "The before-backup command "+scrub(herr.Error(), secrets)+" (ignored: the backup went on)")
			} else {
				res.HookOutcome = "ok"
			}
		}
		if p.PostHook != nil {
			runPost = func() { postOnce.Do(func() { r.runPost(ctx, cont, p.PostHook, &res, logf) }) }
		}
	}
	defer runPost() // live mode: after the upload; staged modes already ran it

	// 5. The point-in-time copy.
	if !direct {
		if werr := r.window(ctx, h, p, names, &res, logf); werr != nil {
			runPost()
			if errors.Is(werr, ErrBusy) {
				res.Outcome = agentprotocol.OutcomeSkipped
				res.Message = "A deployment or another action on this service was in progress; the backup was skipped and the service was not touched."
				res.FinishedAt = r.now().UTC()
				return res, nil
			}
			return fail("%v", werr)
		}
		runPost()
	}

	// 6. Upload.
	logf("uploading")
	col := &summaryCollector{}
	var lastLog time.Time
	col.OnProgress = func(pct float64) {
		if time.Since(lastLog) > 30*time.Second {
			lastLog = time.Now()
			logf(fmt.Sprintf("uploading… %.0f%%", pct*100))
		}
	}
	code, msg, err := h.restic(ctx, col, backupArgs(p)...)
	col.flush()
	if err != nil {
		return fail("upload: %v", err)
	}
	if code == exitIncomplete {
		if col.found && col.s.SnapshotID != "" {
			// restic still wrote a snapshot of what it could read: it is not a
			// consistent backup, so it must not stay.
			_ = h.resticOK(context.WithoutCancel(ctx), nil, "forget", col.s.SnapshotID)
		}
		why := strings.Join(col.errs, "; ")
		if why == "" {
			why = msg
		}
		return fail("some files could not be read, so no snapshot was kept: %s", clip(why, 600))
	}
	if code != 0 {
		why := msg
		if len(col.errs) > 0 {
			why = strings.Join(col.errs, "; ")
		}
		if why == "" {
			why = "no error output"
		}
		return fail("upload failed (restic exit %d): %s", code, clip(why, 600))
	}
	if !col.found || col.s.SnapshotID == "" {
		return fail("restic finished without reporting a snapshot")
	}
	res.SnapshotID = col.s.SnapshotID
	res.BytesProcessed, res.BytesAdded = col.s.TotalBytes, col.s.DataAdded
	res.FilesNew, res.FilesChanged, res.FilesUnchanged = col.s.FilesNew, col.s.FilesChanged, col.s.FilesUnmodified

	res.Outcome = agentprotocol.OutcomeSucceeded
	res.FinishedAt = r.now().UTC()
	res.Message = fmt.Sprintf("snapshot %s: %d bytes processed, %d added", agentprotocol.Short(res.SnapshotID), res.BytesProcessed, res.BytesAdded)
	logf(res.Message)
	return res, nil
}

func volumeNames(vs []agentprotocol.VolumeRef) []string {
	out := make([]string, len(vs))
	for i, v := range vs {
		out[i] = v.Name
	}
	return out
}

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

// validateBackup checks the payload and fills in defaults.
func validateBackup(p *agentprotocol.VolumeBackupPayload) error {
	if p.BackupID == "" || p.ServiceID == "" {
		return errors.New("invalid volume_backup payload: backupId and serviceId are required")
	}
	if err := p.Repo.Validate(); err != nil {
		return err
	}
	if len(p.Volumes) == 0 {
		return errors.New("the service has no volumes to back up")
	}
	seen := map[string]bool{}
	for _, v := range p.Volumes {
		if err := validRef(v); err != nil {
			return err
		}
		if seen[v.Name] {
			return fmt.Errorf("volume %q is listed twice", v.Name)
		}
		seen[v.Name] = true
	}
	switch p.Mode {
	case "":
		p.Mode = agentprotocol.VolModeFreeze
	case agentprotocol.VolModeFreeze, agentprotocol.VolModeStop, agentprotocol.VolModeLive:
	default:
		return fmt.Errorf("unknown backup mode %q", p.Mode)
	}
	if p.FreezeBudgetSeconds <= 0 {
		p.FreezeBudgetSeconds = agentprotocol.DefaultFreezeBudgetSeconds
	}
	p.FreezeBudgetSeconds = min(max(p.FreezeBudgetSeconds, agentprotocol.MinFreezeBudgetSeconds), agentprotocol.MaxFreezeBudgetSeconds)
	if err := validHook(p.PreHook); err != nil {
		return fmt.Errorf("before-backup command: %w", err)
	}
	if err := validHook(p.PostHook); err != nil {
		return fmt.Errorf("after-backup command: %w", err)
	}
	return nil
}

// preflight probes the staging filesystem (clone support) and, when a plain
// copy is needed, that the disk has room for it. It returns the stage method.
func (r *Runner) preflight(ctx context.Context, h *helper, names []string) (string, error) {
	code, err := h.exec(ctx, []string{"sh", "-c", reflinkProbe}, nil, io.Discard, io.Discard)
	if err != nil {
		return "", fmt.Errorf("probe the staging volume: %w", err)
	}
	if code == 0 {
		return agentprotocol.StageReflink, nil
	}
	var out strings.Builder
	argv := append([]string{"sh", "-c", diskProbe, "sh"}, names...)
	code, err = h.exec(ctx, argv, nil, &out, io.Discard)
	if err != nil || code != 0 {
		// Without numbers the copy is attempted anyway; it is bounded by the budget.
		return agentprotocol.StageCopy, nil //nolint:nilerr // without disk numbers the copy is attempted anyway, bounded by the budget (see comment above)
	}
	var usedKB, freeKB int64
	if _, err := fmt.Sscanf(strings.TrimSpace(out.String()), "%d %d", &usedKB, &freeKB); err == nil && usedKB > 0 && freeKB < usedKB+usedKB/10 {
		return "", fmt.Errorf("not enough free disk for the temporary copy: the volumes use about %s, %s is free (copy-on-write is not available on this host's filesystem). Use Stop mode with a smaller volume set or free disk space",
			humanKB(usedKB), humanKB(freeKB))
	}
	return agentprotocol.StageCopy, nil
}

func humanKB(kb int64) string {
	const unit = 1024
	b := float64(kb) * unit
	suffixes := []string{"B", "KiB", "MiB", "GiB", "TiB"}
	i := 0
	for b >= unit && i < len(suffixes)-1 {
		b /= unit
		i++
	}
	return fmt.Sprintf("%.1f %s", b, suffixes[i])
}

// window takes the point-in-time copy under the service's command lock:
// freeze (pause) or stop, copy, thaw or start. It always puts the service back.
// ErrBusy means the service stayed busy and nothing was touched.
func (r *Runner) window(ctx context.Context, h *helper, p agentprotocol.VolumeBackupPayload, names []string, res *agentprotocol.VolumeBackupResult, logf func(string)) error {
	wait := time.Duration(agentprotocol.VolumeBackupWaitSeconds) * time.Second
	unlock, err := r.Host.LockService(ctx, p.ServiceID, wait)
	if err != nil {
		return err
	}
	defer unlock()

	// The container may have been replaced while the hook ran or the lock was awaited.
	cont, ok := r.Host.ServiceContainer(ctx, p.ServiceID)
	script := append([]string{"sh", "-c", copyScript, "sh"}, names...)
	if !ok || !cont.Running {
		// Nothing runs now: copy without touching anything.
		started := time.Now()
		code, err := h.exec(ctx, script, nil, io.Discard, &tailBuf{})
		res.StageMs = time.Since(started).Milliseconds()
		if err != nil || code != 0 {
			return fmt.Errorf("copy the volumes: %s", execFailure(code, err))
		}
		res.Consistency = agentprotocol.VolModeStop
		return nil
	}
	if p.Mode == agentprotocol.VolModeStop {
		return r.stopWindow(ctx, h, p, cont, script, res, logf)
	}
	return r.freezeWindow(ctx, h, p, cont, script, res, logf)
}

func execFailure(code int, err error) string {
	if err != nil {
		return err.Error()
	}
	return fmt.Sprintf("exit status %d", code)
}

// freezeWindow pauses the container for the copy. The pause never outlasts the
// budget: the copy runs under a deadline, a timer thaws independently, and the
// thaw is retried. Over budget the backup fails instead of falling back silently.
func (r *Runner) freezeWindow(ctx context.Context, h *helper, p agentprotocol.VolumeBackupPayload, cont docker.ContainerInfo, script []string, res *agentprotocol.VolumeBackupResult, logf func(string)) error {
	budget := time.Duration(p.FreezeBudgetSeconds) * time.Second
	thaw := r.thawer(cont.ID)
	logf(fmt.Sprintf("freezing the service for the local copy (budget %d s)", p.FreezeBudgetSeconds))

	frozenAt := time.Now()
	winCtx, cancel := context.WithDeadline(ctx, frozenAt.Add(budget))
	defer cancel()
	guard := time.AfterFunc(budget, func() { _ = thaw() })
	defer guard.Stop()
	// Whatever happens below, the service is thawed before this function returns.
	defer func() { _ = thaw() }()

	if err := r.Docker.ContainerPause(ctx, cont.ID); err != nil {
		return fmt.Errorf("could not freeze the service: %w", err)
	}
	stderr := &tailBuf{}
	code, execErr := h.exec(winCtx, script, nil, io.Discard, stderr)
	copyDone := time.Now()
	thawErr := thaw()
	frozen := time.Since(frozenAt)
	res.FreezeMs = frozen.Milliseconds()
	res.StageMs = copyDone.Sub(frozenAt).Milliseconds()
	if thawErr != nil {
		return fmt.Errorf("the copy finished but the service could not be unfrozen (%v); resume it with `docker unpause %s`", thawErr, cont.Name)
	}
	if winCtx.Err() != nil || frozen > budget {
		return fmt.Errorf("Snapshot took longer than %d s; use Stop mode or a pre-backup hook", p.FreezeBudgetSeconds)
	}
	if execErr != nil || code != 0 {
		return fmt.Errorf("copy the volumes: %s %s", execFailure(code, execErr), stderr.String())
	}
	logf(fmt.Sprintf("service was frozen for %d ms", res.FreezeMs))
	return nil
}

// stopWindow stops the container, copies and starts it again.
func (r *Runner) stopWindow(ctx context.Context, h *helper, p agentprotocol.VolumeBackupPayload, cont docker.ContainerInfo, script []string, res *agentprotocol.VolumeBackupResult, logf func(string)) error {
	grace := 30 * time.Second
	if p.StopGraceSeconds > 0 {
		grace = time.Duration(p.StopGraceSeconds) * time.Second
	}
	logf("stopping the service for the local copy")
	stoppedAt := time.Now()
	started := false
	start := func() error {
		if started {
			return nil
		}
		sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
		defer cancel()
		var err error
		for i := range 3 {
			if err = r.Docker.ContainerStart(sctx, cont.ID); err == nil {
				started = true
				return nil
			}
			time.Sleep(time.Duration(i+1) * 500 * time.Millisecond)
		}
		return err
	}
	defer func() { _ = start() }()

	if err := r.Docker.ContainerStop(ctx, cont.ID, &grace); err != nil {
		return fmt.Errorf("could not stop the service: %w", err)
	}
	copyStart := time.Now()
	stderr := &tailBuf{}
	code, execErr := h.exec(ctx, script, nil, io.Discard, stderr)
	res.StageMs = time.Since(copyStart).Milliseconds()
	startErr := start()
	res.FreezeMs = time.Since(stoppedAt).Milliseconds()
	if startErr != nil {
		return fmt.Errorf("the copy finished but the service could not be started again (%v); start it from the dashboard", startErr)
	}
	if execErr != nil || code != 0 {
		return fmt.Errorf("copy the volumes: %s %s", execFailure(code, execErr), stderr.String())
	}
	logf(fmt.Sprintf("service was stopped for %d ms", res.FreezeMs))
	return nil
}

// thawer returns an idempotent function that unpauses id, retrying, and
// treating "not paused" as done.
func (r *Runner) thawer(id string) func() error {
	var once sync.Once
	var result error
	return func() error {
		once.Do(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			for i := range 4 {
				err := r.Docker.ContainerUnpause(ctx, id)
				if err == nil || errors.Is(err, docker.ErrNotFound) {
					result = nil
					return
				}
				if info, ierr := r.Docker.ContainerInspect(ctx, id); ierr == nil && info.State != "paused" {
					result = nil // someone else thawed it
					return
				}
				result = err
				time.Sleep(time.Duration(i+1) * 300 * time.Millisecond)
			}
		})
		return result
	}
}

// runHook runs a hook in the service container.
func (r *Runner) runHook(ctx context.Context, cont docker.ContainerInfo, h *agentprotocol.VolumeHook) error {
	hctx, cancel := context.WithTimeout(ctx, hookTimeout(h))
	defer cancel()
	tail := &tailBuf{}
	code, err := r.Docker.ContainerExec(hctx, cont.ID, h.Command, docker.ExecSpec{User: h.User}, tail, tail)
	switch {
	case hctx.Err() != nil && ctx.Err() == nil:
		return fmt.Errorf("timed out after %s", hookTimeout(h))
	case err != nil:
		return fmt.Errorf("could not run: %w", err)
	case code != 0:
		out := tail.String()
		if out == "" {
			out = "no output"
		}
		return fmt.Errorf("failed (exit %d): %s", code, clip(out, 400))
	}
	return nil
}

// runPost runs the after-backup command; its failure is a warning only (the
// data is already safe).
func (r *Runner) runPost(ctx context.Context, cont docker.ContainerInfo, h *agentprotocol.VolumeHook, res *agentprotocol.VolumeBackupResult, logf func(string)) {
	if h == nil {
		return
	}
	// The container may have been replaced; use the current one.
	pctx := context.WithoutCancel(ctx)
	logf("running the after-backup command")
	if err := r.runHook(pctx, cont, h); err != nil {
		res.Warnings = append(res.Warnings, "The after-backup command "+err.Error())
	}
}
