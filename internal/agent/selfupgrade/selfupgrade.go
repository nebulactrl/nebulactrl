// Package selfupgrade replaces the running agent binary with the one its
// control plane serves.
//
// The steps of Upgrade, in order, and what a failure at each leaves behind:
//
//  1. download to a temporary file next to the running binary (same file
//     system, so the final rename is atomic) while hashing; nothing changed;
//  2. compare the SHA-256 with the one in the authenticated command; a mismatch
//     deletes the file; nothing changed;
//  3. run `<new> --version` and require the expected version (catches a wrong
//     architecture or a corrupt binary); nothing changed;
//  4. keep the running binary as <exe>.prev (hard link, else copy) and write
//     upgrade.json;
//  5. rename the new file over the binary: atomic, so the path always holds a
//     whole binary.
//
// The agent then restarts when its other commands are done (systemd's Restart=
// or the container's restart policy brings it back). A new binary that does not
// start, or does not stay connected, is replaced by <exe>.prev again (Start and
// Watch): after MaxStarts starts that never reached "connected", or when it was
// not connected for StartDeadline.
package selfupgrade

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Limits and timings. The fields of Controller override them in tests.
const (
	// MaxStarts is how many starts of a new binary may fail to connect before
	// the previous binary is restored.
	MaxStarts = 3
	// ConfirmAfter is how long the new binary must stay connected to be kept.
	ConfirmAfter = 20 * time.Second
	// StartDeadline is how long after its start a new binary may go without a
	// confirmed connection before it is rolled back.
	StartDeadline = 5 * time.Minute
	// RetryBlock is how long a rolled-back version is refused without Force.
	RetryBlock = 24 * time.Hour

	maxBinarySize = 512 << 20
	probeTimeout  = 15 * time.Second
)

// ErrRestart tells the caller to exit so the supervisor starts the binary that
// is now on disk.
var ErrRestart = errors.New("restart to apply the agent upgrade")

// Controller performs and supervises self-upgrades. Create one per agent.
type Controller struct {
	// Exe is the resolved path of the running binary.
	Exe string
	// StateDir holds upgrade.json.
	StateDir string
	// Version is the running binary's version.
	Version string
	// GOOS and GOARCH default to the running platform.
	GOOS, GOARCH string
	// BaseURL is the control plane's HTTP(S) base URL without a trailing slash.
	BaseURL string
	HTTP    *http.Client
	// Probe runs the candidate binary and returns the version it reports
	// (default: `<path> --version`).
	Probe func(ctx context.Context, path string) (string, error)
	Now   func() time.Time
	Log   *slog.Logger

	// Timings (defaults above).
	ConfirmAfter  time.Duration
	StartDeadline time.Duration
	Poll          time.Duration

	mu sync.Mutex
}

// New returns a Controller for the running binary. It fails when the binary's
// path cannot be resolved.
func New(stateDir, version, baseURL string, client *http.Client, log *slog.Logger) (*Controller, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("locate the agent binary: %w", err)
	}
	if r, err := filepath.EvalSymlinks(exe); err == nil {
		exe = r
	}
	return &Controller{Exe: exe, StateDir: stateDir, Version: version, BaseURL: strings.TrimRight(baseURL, "/"), HTTP: client, Log: log}, nil
}

func (c *Controller) goos() string {
	if c.GOOS != "" {
		return c.GOOS
	}
	return runtime.GOOS
}

func (c *Controller) goarch() string {
	if c.GOARCH != "" {
		return c.GOARCH
	}
	return runtime.GOARCH
}

func (c *Controller) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *Controller) logger() *slog.Logger {
	if c.Log != nil {
		return c.Log
	}
	return slog.Default()
}

// Supported reports whether this agent can replace itself: Linux on amd64 or
// arm64 (the architectures the control plane ships), and a writable directory
// for the binary. A read-only container image is the usual reason for no.
func (c *Controller) Supported() error {
	if c.goos() != "linux" {
		return fmt.Errorf("self-upgrade is supported on linux only, not %s", c.goos())
	}
	if a := c.goarch(); a != "amd64" && a != "arm64" {
		return fmt.Errorf("self-upgrade: no agent binary is shipped for %s", a)
	}
	if c.Exe == "" {
		return errors.New("the agent binary path is unknown")
	}
	f, err := os.CreateTemp(filepath.Dir(c.Exe), ".nebulactrl-agent.write-test-*")
	if err != nil {
		return fmt.Errorf("the directory of the agent binary is not writable: %w", err)
	}
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)
	return nil
}

func (c *Controller) prevPath() string { return c.Exe + ".prev" }

// Upgrade downloads, verifies and installs the binary described by p. On
// success the new binary is on disk and the caller should restart the agent
// (see ErrRestart); on any error the installed binary is unchanged.
func (c *Controller) Upgrade(ctx context.Context, p agentprotocol.UpgradePayload) (agentprotocol.UpgradeResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	res := agentprotocol.UpgradeResult{From: c.Version, To: p.Version}

	want := strings.ToLower(strings.TrimSpace(p.SHA256))
	if p.Version == "" || len(want) != sha256.Size*2 {
		return res, errors.New("the upgrade command carries no version or no valid SHA-256")
	}
	if _, err := hex.DecodeString(want); err != nil {
		return res, errors.New("the upgrade command carries no valid SHA-256")
	}
	if err := c.Supported(); err != nil {
		return res, err
	}
	if p.Version == c.Version {
		return res, fmt.Errorf("the agent already runs %s", p.Version)
	}
	st, err := c.load()
	if err != nil {
		return res, fmt.Errorf("read upgrade state: %w", err)
	}
	if st.State == agentprotocol.UpgradeSwapped && st.To == p.Version && c.Version != p.Version {
		// Redelivery of a command whose binary is installed and waiting for the restart.
		res.Message = "already installed; waiting to restart"
		return res, nil
	}
	if st.FailedVersion == p.Version && !p.Force && c.now().Sub(st.FailedAt) < RetryBlock {
		return res, fmt.Errorf("%s was rolled back on this host at %s; it is not installed again without a forced upgrade", p.Version, st.FailedAt.UTC().Format(time.RFC3339))
	}

	tmp, err := os.CreateTemp(filepath.Dir(c.Exe), ".nebulactrl-agent.new-*")
	if err != nil {
		return res, fmt.Errorf("create the new binary next to %s: %w", c.Exe, err)
	}
	tmpName := tmp.Name()
	keep := false
	defer func() {
		if !keep {
			_ = os.Remove(tmpName)
		}
	}()

	got, err := c.download(ctx, tmp, p.Size)
	if err != nil {
		_ = tmp.Close()
		return res, err
	}
	if got != want {
		_ = tmp.Close()
		return res, fmt.Errorf("checksum mismatch: the downloaded binary has SHA-256 %s, the control plane announced %s", got, want)
	}
	mode := os.FileMode(0o755)
	if fi, err := os.Stat(c.Exe); err == nil {
		mode = fi.Mode().Perm() | 0o111
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return res, err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return res, err
	}
	if err := tmp.Close(); err != nil {
		return res, err
	}

	reported, err := c.probe(ctx, tmpName)
	if err != nil {
		return res, fmt.Errorf("the downloaded binary does not run: %w", err)
	}
	if normalize(reported) != normalize(p.Version) {
		return res, fmt.Errorf("the downloaded binary reports version %q, expected %q", reported, p.Version)
	}

	if err := c.keepPrevious(); err != nil {
		return res, fmt.Errorf("keep the current binary for rollback: %w", err)
	}
	next := state{
		State: agentprotocol.UpgradeSwapped, From: c.Version, To: p.Version, Exe: c.Exe, Prev: c.prevPath(),
		At: c.now().UTC(), FailedVersion: st.FailedVersion, FailedAt: st.FailedAt,
	}
	// The record goes first: a crash between the two steps leaves a record that
	// Start recognizes as "the swap never took effect".
	if err := c.save(next); err != nil {
		return res, fmt.Errorf("write upgrade state: %w", err)
	}
	if err := os.Rename(tmpName, c.Exe); err != nil {
		_ = os.Remove(c.statePath())
		return res, fmt.Errorf("install the new binary: %w", err)
	}
	keep = true
	c.logger().Info("agent binary replaced", "from", c.Version, "to", p.Version)
	res.Message = "installed; restarting"
	return res, nil
}

// download streams the binary into dst and returns its SHA-256.
func (c *Controller) download(ctx context.Context, dst io.Writer, wantSize int64) (string, error) {
	if c.BaseURL == "" {
		return "", errors.New("no control plane URL to download from")
	}
	url := c.BaseURL + "/agent/v1/download/" + c.goarch()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Minute}
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("download %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download %s: HTTP %d", url, resp.StatusCode)
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(dst, h), io.LimitReader(resp.Body, maxBinarySize+1))
	if err != nil {
		return "", fmt.Errorf("download %s: %w", url, err)
	}
	if n > maxBinarySize {
		return "", fmt.Errorf("download %s: larger than %d MiB", url, maxBinarySize>>20)
	}
	if n == 0 {
		return "", fmt.Errorf("download %s: empty body", url)
	}
	if wantSize > 0 && n != wantSize {
		return "", fmt.Errorf("download %s: got %d bytes, the control plane announced %d", url, n, wantSize)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func (c *Controller) probe(ctx context.Context, path string) (string, error) {
	pctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	if c.Probe != nil {
		return c.Probe(pctx, path)
	}
	return ExecProbe(pctx, path)
}

// ExecProbe runs `<path> --version` and returns the version it prints (the last
// word of cobra's "nebulactrl-agent version 1.5.0").
func ExecProbe(ctx context.Context, path string) (string, error) {
	out, err := exec.CommandContext(ctx, path, "--version").Output()
	if err != nil {
		return "", err
	}
	f := strings.Fields(string(out))
	if len(f) == 0 {
		return "", errors.New("--version printed nothing")
	}
	return f[len(f)-1], nil
}

func normalize(v string) string { return strings.TrimPrefix(strings.TrimSpace(v), "v") }

// keepPrevious preserves the running binary as <exe>.prev: a hard link when the
// file system allows it (instant, and the old inode survives the rename), else a
// copy.
func (c *Controller) keepPrevious() error {
	prev := c.prevPath()
	if err := os.Remove(prev); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Link(c.Exe, prev); err == nil {
		return nil
	}
	return copyFile(c.Exe, prev)
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	fi, err := in.Stat()
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".nebulactrl-agent.prev-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := io.Copy(tmp, in); err != nil {
		_ = tmp.Close()
		_ = os.Remove(name)
		return err
	}
	if err := tmp.Chmod(fi.Mode().Perm() | 0o111); err != nil {
		_ = tmp.Close()
		_ = os.Remove(name)
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return err
	}
	return os.Rename(name, dst)
}

// Start runs first thing in a starting agent. It returns ErrRestart when it just
// restored the previous binary (the caller exits; the supervisor starts the
// restored one), nil otherwise. A new binary under probation has its start
// counted; Watch confirms it.
func (c *Controller) Start() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	st, err := c.load()
	if err != nil || st.State != agentprotocol.UpgradeSwapped {
		return nil
	}
	switch {
	case c.Version == st.To:
		st.Starts++
		if st.Starts > MaxStarts {
			return c.rollbackLocked(st, fmt.Sprintf("%s started %d times without staying connected", st.To, MaxStarts))
		}
		if err := c.save(st); err != nil {
			c.logger().Warn("record upgrade start", "err", err)
		}
		c.logger().Info("agent upgraded; confirming the new binary", "from", st.From, "to", st.To, "start", st.Starts)
	default:
		// Running something else than the new version: the swap never took
		// effect (the container was recreated from its image, or the binary was
		// replaced by hand). Nothing to supervise.
		st.State = agentprotocol.UpgradeAbandoned
		st.Message = fmt.Sprintf("the swap to %s did not take effect: the agent runs %s", st.To, c.Version)
		st.At = c.now().UTC()
		if err := c.save(st); err != nil {
			c.logger().Warn("record abandoned upgrade", "err", err)
		}
	}
	return nil
}

// Watch confirms a new binary once connected() has been true for ConfirmAfter,
// and rolls it back when that does not happen within StartDeadline. It blocks
// until the outcome (or ctx ends) and returns ErrRestart after a rollback. It
// does nothing when no upgrade is under probation.
func (c *Controller) Watch(ctx context.Context, connected func() bool) error {
	c.mu.Lock()
	st, err := c.load()
	c.mu.Unlock()
	if err != nil || st.State != agentprotocol.UpgradeSwapped || c.Version != st.To {
		return nil
	}
	confirmAfter, deadline, poll := c.ConfirmAfter, c.StartDeadline, c.Poll
	if confirmAfter == 0 {
		confirmAfter = ConfirmAfter
	}
	if deadline == 0 {
		deadline = StartDeadline
	}
	if poll == 0 {
		poll = time.Second
	}
	started := time.Now()
	var upSince time.Time
	t := time.NewTicker(poll)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
		if connected() {
			if upSince.IsZero() {
				upSince = time.Now()
			}
			if time.Since(upSince) >= confirmAfter {
				return c.confirm()
			}
		} else {
			upSince = time.Time{}
		}
		if time.Since(started) >= deadline {
			c.mu.Lock()
			defer c.mu.Unlock()
			st, _ := c.load()
			return c.rollbackLocked(st, fmt.Sprintf("%s did not connect to the control plane within %s", st.To, deadline.Round(time.Second)))
		}
	}
}

func (c *Controller) confirm() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	st, err := c.load()
	if err != nil || st.State != agentprotocol.UpgradeSwapped {
		return nil
	}
	st.State, st.Starts, st.At = agentprotocol.UpgradeCommitted, 0, c.now().UTC()
	st.Message = ""
	if err := c.save(st); err != nil {
		c.logger().Warn("record upgrade confirmation", "err", err)
	}
	c.logger().Info("agent upgrade confirmed", "from", st.From, "to", st.To)
	return nil
}

// rollbackLocked puts <exe>.prev back (an atomic rename over the new binary),
// records why, and returns ErrRestart.
func (c *Controller) rollbackLocked(st state, reason string) error {
	prev := st.Prev
	if prev == "" {
		prev = c.prevPath()
	}
	exe := st.Exe
	if exe == "" {
		exe = c.Exe
	}
	if _, err := os.Stat(prev); err != nil {
		st.State, st.Message, st.At = agentprotocol.UpgradeRolledBack, reason+"; the previous binary is gone, reinstall the agent: "+err.Error(), c.now().UTC()
		_ = c.save(st)
		return fmt.Errorf("roll back the agent upgrade: %w", err)
	}
	if err := os.Rename(prev, exe); err != nil {
		return fmt.Errorf("roll back the agent upgrade: %w", err)
	}
	st.State, st.Message, st.At = agentprotocol.UpgradeRolledBack, reason, c.now().UTC()
	st.FailedVersion, st.FailedAt = st.To, st.At
	if err := c.save(st); err != nil {
		c.logger().Warn("record rollback", "err", err)
	}
	c.logger().Error("agent upgrade rolled back", "from", st.From, "to", st.To, "reason", reason)
	return ErrRestart
}
