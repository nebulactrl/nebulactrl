package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Where install-agent.sh puts the agent. selfRemoval deletes the same files
// as `install-agent.sh --uninstall --purge`.
const (
	agentUnitName   = "nebulactrl-agent"
	agentUnitFile   = "/etc/systemd/system/nebulactrl-agent.service"
	agentUnitDropIn = "/etc/systemd/system/nebulactrl-agent.service.d"
	agentEnvFile    = "/etc/nebulactrl-agent.env"
	agentBinary     = "/usr/local/bin/nebulactrl-agent"
)

// selfRemoval deletes the agent from its host after a decommission: its
// identity and state, its systemd unit, its environment file (the enrollment
// token) and its binary. The containers, networks and volumes are already gone
// by then (internal/agent/decommission); Docker and everything that is not
// NebulaCtrl's is not touched.
type selfRemoval struct {
	// StateDir holds agent.db (the identity), the build workspaces, the stacks'
	// compose files, the log history and the default backup directory.
	StateDir string
	// UnitFile is the systemd unit: the service is disabled only when it exists.
	UnitFile string
	// Files are removed one by one, DropIns recursively.
	Files   []string
	DropIns []string
	// Systemctl runs systemctl with args; nil when the host has no systemd.
	Systemctl func(ctx context.Context, args ...string) error
	// StopRestarting keeps a container-run agent from being started again by its
	// restart policy (nil: the agent does not run in a container).
	StopRestarting func(ctx context.Context) error
	Log            *slog.Logger
	// Settle is the wait before the second removal of the state directory
	// (zero: no wait).
	Settle time.Duration
	// BackupDir is where local backups are stored (the agent's --backup-dir,
	// <state-dir>/backups by default). They are never deleted: a directory inside
	// StateDir is moved to KeepDir (or a fresh subdirectory of it when KeepDir
	// exists) before the state directory goes; one outside it is left alone.
	BackupDir string
	KeepDir   string
}

// newSelfRemoval is the removal of the agent as the installer set it up. exe is
// the running binary: removed too when it is the installed one (a binary with
// another name, like a test binary, is left alone).
func newSelfRemoval(stateDir, backupDir, exe string, stopRestarting func(context.Context) error, log *slog.Logger) selfRemoval {
	files := []string{agentUnitFile, agentEnvFile, agentBinary}
	if exe != "" && filepath.Base(exe) == filepath.Base(agentBinary) && exe != agentBinary {
		files = append(files, exe)
	}
	r := selfRemoval{
		StateDir: stateDir, UnitFile: agentUnitFile, Files: files, DropIns: []string{agentUnitDropIn},
		StopRestarting: stopRestarting, Log: log, Settle: 2 * time.Second, BackupDir: backupDir, KeepDir: agentBackupKeepDir,
	}
	if _, err := exec.LookPath("systemctl"); err == nil {
		r.Systemctl = func(ctx context.Context, args ...string) error {
			out, err := exec.CommandContext(ctx, "systemctl", args...).CombinedOutput()
			if err != nil {
				return fmt.Errorf("systemctl %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
			}
			return nil
		}
	}
	return r
}

// errUnsafeStateDir is returned for a state directory the agent will not delete.
var errUnsafeStateDir = errors.New("refusing to delete this state directory")

// safeStateDir reports whether dir is something an agent may delete recursively:
// absolute, below the root and not one of the system directories.
func safeStateDir(dir string) bool {
	d := filepath.Clean(dir)
	if !filepath.IsAbs(d) {
		return false
	}
	switch d {
	case "/", "/bin", "/boot", "/dev", "/etc", "/home", "/lib", "/opt", "/proc", "/root", "/run", "/sbin", "/srv", "/sys", "/tmp",
		"/usr", "/var", "/var/lib", "/var/log", "/var/tmp", "/usr/local", "/usr/local/bin", "/usr/local/lib":
		return false
	}
	return true
}

// Execute removes the agent. Every step is tried even when an earlier one
// failed; the errors are joined. The running process is not stopped here: the
// caller exits right after.
func (r selfRemoval) Execute(ctx context.Context) error {
	var errs []error
	note := func(step string, err error) {
		if err != nil {
			r.Log.Warn("decommission: agent removal step failed", "step", step, "err", err)
			errs = append(errs, fmt.Errorf("%s: %w", step, err))
		}
	}
	if r.StopRestarting != nil {
		note("stop the container restarting", r.StopRestarting(ctx))
	}
	// Disable, but do not stop: the unit is this process. A stop would end it
	// before the files are gone.
	if r.Systemctl != nil {
		if _, err := os.Stat(r.UnitFile); err == nil {
			note("disable the service", r.Systemctl(ctx, "disable", agentUnitName))
		}
	}
	for _, f := range r.Files {
		if err := os.Remove(f); err != nil && !errors.Is(err, os.ErrNotExist) {
			note("remove "+f, err)
		}
	}
	for _, d := range r.DropIns {
		note("remove "+d, os.RemoveAll(d))
	}
	if r.Systemctl != nil {
		note("reload systemd", r.Systemctl(ctx, "daemon-reload"))
	}
	if safeStateDir(r.StateDir) {
		keep := r.preserveBackups()
		note("remove "+r.StateDir, removeAllExcept(r.StateDir, keep))
		// The goroutines of the stopping agent may still write a file (the log
		// history, the command store): a second pass takes what they recreated.
		select {
		case <-time.After(r.Settle):
		case <-ctx.Done():
		}
		note("remove "+r.StateDir, removeAllExcept(r.StateDir, keep))
	} else {
		note("remove the state directory", fmt.Errorf("%w: %q", errUnsafeStateDir, r.StateDir))
	}
	// Ask systemd to stop the unit once this process is done with it, so that
	// Restart=always does not start it again.
	if r.Systemctl != nil {
		sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		_ = r.Systemctl(sctx, "stop", "--no-block", agentUnitName)
	}
	return errors.Join(errs...)
}

// agentBackupKeepDir is where local backups that lived inside the state
// directory are moved to: they are the only copy of what was backed up locally,
// so the removal never deletes them.
const agentBackupKeepDir = "/var/lib/nebulactrl-backups"

// within reports whether path is strictly inside dir.
func within(dir, path string) bool {
	rel, err := filepath.Rel(filepath.Clean(dir), filepath.Clean(path))
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// preserveBackups makes sure removing the state directory keeps the local
// backups. A backup directory outside the state directory stays where it is.
// One inside it is moved to KeepDir, or to a new timestamped subdirectory of
// KeepDir when that exists already (an earlier agent's backups are not mixed
// up with these). It logs where the backups are, and returns the path inside
// the state directory that the removal must skip: "" after a move or when
// there is nothing inside to keep, the backup directory itself when it could
// not be moved (it then stays in place, under the state directory).
func (r selfRemoval) preserveBackups() string {
	if r.BackupDir == "" || !within(r.StateDir, r.BackupDir) {
		if r.BackupDir != "" {
			r.Log.Info("decommission: local backups stay where they are", "path", r.BackupDir)
		}
		return ""
	}
	if _, err := os.Stat(r.BackupDir); err != nil {
		return ""
	}
	dest := r.KeepDir
	if _, err := os.Stat(dest); err == nil {
		dest = filepath.Join(r.KeepDir, "agent-"+time.Now().UTC().Format("20060102T150405"))
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err == nil { //nolint:gosec // only the parent of the kept backups; they keep their own modes
		if err = os.Rename(r.BackupDir, dest); err == nil {
			r.Log.Info("decommission: local backups moved out of the state directory", "from", r.BackupDir, "to", dest)
			return ""
		}
		r.Log.Warn("decommission: could not move the local backups; they stay in place", "path", r.BackupDir, "err", err)
		return r.BackupDir
	}
	r.Log.Warn("decommission: could not move the local backups; they stay in place", "path", r.BackupDir)
	return r.BackupDir
}

// removeAllExcept deletes dir with everything in it except keep (a path inside
// dir, "" for none) and the directories that lead to it.
func removeAllExcept(dir, keep string) error {
	if keep == "" {
		return os.RemoveAll(dir)
	}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var errs []error
	for _, e := range entries {
		p := filepath.Join(dir, e.Name())
		switch {
		case filepath.Clean(p) == filepath.Clean(keep):
		case within(p, keep):
			errs = append(errs, removeAllExcept(p, keep))
		default:
			errs = append(errs, os.RemoveAll(p))
		}
	}
	return errors.Join(errs...)
}
