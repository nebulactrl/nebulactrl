package volbackup

import (
	"context"
	"errors"
	"io"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// failDocker is the fake Docker with a hook per call: a hook returning a
// non-nil error makes the call fail (a nil one lets the fake answer).
type failDocker struct {
	*fakeDocker
	onPull     func() error
	onVolume   func(name string) error
	onRmVolume func(name string) error
	onCreate   func(spec docker.ContainerSpec) error
	onStart    func(id string) error
	onStop     func(id string) error
	onPause    func(id string) error
	onUnpause  func(id string) error
	onRemove   func(id string) error
	onInspect  func(id string) error
	onList     func(f docker.ListFilter) error
	// onExec may answer an exec itself.
	onExec func(id string, argv []string, stdout, stderr io.Writer) (code int, err error, handled bool)
}

func (f *failDocker) ImageInspect(ctx context.Context, ref string) (docker.ImageInfo, error) {
	if f.onPull != nil {
		return docker.ImageInfo{}, docker.ErrNotFound
	}
	return f.fakeDocker.ImageInspect(ctx, ref)
}

func (f *failDocker) ImagePull(ctx context.Context, ref, auth string, p func(string)) error {
	if f.onPull != nil {
		return f.onPull()
	}
	return f.fakeDocker.ImagePull(ctx, ref, auth, p)
}

func (f *failDocker) EnsureVolume(ctx context.Context, name string, l map[string]string) error {
	if f.onVolume != nil {
		if err := f.onVolume(name); err != nil {
			return err
		}
	}
	return f.fakeDocker.EnsureVolume(ctx, name, l)
}

func (f *failDocker) RemoveVolume(ctx context.Context, name string, force bool) error {
	if f.onRmVolume != nil {
		if err := f.onRmVolume(name); err != nil {
			return err
		}
	}
	return f.fakeDocker.RemoveVolume(ctx, name, force)
}

func (f *failDocker) ContainerCreate(ctx context.Context, spec docker.ContainerSpec) (string, error) {
	if f.onCreate != nil {
		if err := f.onCreate(spec); err != nil {
			return "", err
		}
	}
	return f.fakeDocker.ContainerCreate(ctx, spec)
}

func (f *failDocker) ContainerStart(ctx context.Context, id string) error {
	if f.onStart != nil {
		if err := f.onStart(id); err != nil {
			return err
		}
	}
	return f.fakeDocker.ContainerStart(ctx, id)
}

func (f *failDocker) ContainerStop(ctx context.Context, id string, t *time.Duration) error {
	if f.onStop != nil {
		if err := f.onStop(id); err != nil {
			return err
		}
	}
	return f.fakeDocker.ContainerStop(ctx, id, t)
}

func (f *failDocker) ContainerPause(ctx context.Context, id string) error {
	if f.onPause != nil {
		if err := f.onPause(id); err != nil {
			return err
		}
	}
	return f.fakeDocker.ContainerPause(ctx, id)
}

func (f *failDocker) ContainerUnpause(ctx context.Context, id string) error {
	if f.onUnpause != nil {
		if err := f.onUnpause(id); err != nil {
			return err
		}
	}
	return f.fakeDocker.ContainerUnpause(ctx, id)
}

func (f *failDocker) ContainerRemove(ctx context.Context, id string, force, vols bool) error {
	if f.onRemove != nil {
		if err := f.onRemove(id); err != nil {
			return err
		}
	}
	return f.fakeDocker.ContainerRemove(ctx, id, force, vols)
}

func (f *failDocker) ContainerInspect(ctx context.Context, id string) (docker.ContainerInfo, error) {
	if f.onInspect != nil {
		if err := f.onInspect(id); err != nil {
			return docker.ContainerInfo{}, err
		}
	}
	return f.fakeDocker.ContainerInspect(ctx, id)
}

func (f *failDocker) ContainerList(ctx context.Context, flt docker.ListFilter) ([]docker.ContainerInfo, error) {
	if f.onList != nil {
		if err := f.onList(flt); err != nil {
			return nil, err
		}
	}
	return f.fakeDocker.ContainerList(ctx, flt)
}

func (f *failDocker) ContainerExec(ctx context.Context, id string, argv []string, spec docker.ExecSpec, stdout, stderr io.Writer) (int, error) {
	if f.onExec != nil {
		if code, err, handled := f.onExec(id, argv, stdout, stderr); handled {
			return code, err
		}
	}
	return f.fakeDocker.ContainerExec(ctx, id, argv, spec, stdout, stderr)
}

// resticSub is the restic subcommand of an exec ("" for other programs).
func resticSub(argv []string) string {
	if len(argv) == 0 || argv[0] != "restic" {
		return ""
	}
	for _, a := range argv[1:] {
		if !strings.HasPrefix(a, "-") && !strings.Contains(a, "=") {
			return a
		}
	}
	return ""
}

func setupFail() (*Runner, *failDocker, *fakeHost, *docker.ContainerInfo) {
	d := newFake()
	svc := d.addService("nc-app-1", svcID, true)
	h := &fakeHost{d: d, service: svcID}
	fd := &failDocker{fakeDocker: d}
	return &Runner{Docker: fd, Host: h, BackupDir: "/var/lib/agent/backups", MkdirAll: func(string, os.FileMode) error { return nil }}, fd, h, svc
}

var errBoom = errors.New("boom")

func TestBackupSetupFailuresLeaveNothingBehind(t *testing.T) {
	cases := []struct {
		name  string
		setup func(f *failDocker)
		want  string
	}{
		{"image pull", func(f *failDocker) { f.onPull = func() error { return errBoom } }, "pull the backup helper image: boom"},
		{"cache volume", func(f *failDocker) {
			f.onVolume = func(n string) error {
				if n == agentprotocol.ResticCacheVolume {
					return errBoom
				}
				return nil
			}
		}, "create the backup cache volume: boom"},
		{"staging volume", func(f *failDocker) {
			f.onVolume = func(n string) error {
				if strings.HasPrefix(n, agentprotocol.BackupStagePrefix) {
					return errBoom
				}
				return nil
			}
		}, "create the staging volume: boom"},
		{"helper create", func(f *failDocker) { f.onCreate = func(docker.ContainerSpec) error { return errBoom } }, "create the backup helper: boom"},
		{"helper start", func(f *failDocker) {
			f.onStart = func(id string) error {
				if strings.HasPrefix(id, "hlp") {
					return errBoom
				}
				return nil
			}
		}, "start the backup helper: boom"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, f, _, svc := setupFail()
			tc.setup(f)
			res, err := r.Backup(context.Background(), payload(), secrets(), nil)
			require.Error(t, err)
			assert.Equal(t, agentprotocol.OutcomeFailed, res.Outcome)
			assert.Equal(t, tc.want, res.Message)
			assert.Equal(t, "running", f.state(svc.ID).State, "the service was never touched")
			assert.False(t, f.has("pause"))
			for id, c := range f.containers {
				assert.NotContains(t, c.Name, agentprotocol.BackupHelperPrefix, "helper %s must be removed", id)
			}
			for name := range f.volumes {
				assert.False(t, strings.HasPrefix(name, agentprotocol.BackupStagePrefix), "staging volume %s must be removed", name)
			}
		})
	}
}

func TestBackupReportsAnUnopenableRepository(t *testing.T) {
	r, f, _, _ := setupFail()
	f.onExec = func(_ string, argv []string, _, stderr io.Writer) (int, error, bool) {
		if resticSub(argv) == "cat" {
			_, _ = io.WriteString(stderr, "unable to open config file: Stat: 403 Forbidden "+s3Secret)
			return 1, nil, true
		}
		return 0, nil, false
	}
	res, err := r.Backup(context.Background(), payload(), secrets(), nil)
	require.Error(t, err)
	assert.Contains(t, res.Message, "cannot open the backup repository (restic exit 1)")
	assert.Contains(t, res.Message, "403 Forbidden")
	assert.NotContains(t, res.Message, s3Secret, "secrets are masked in the message that reaches the control plane")

	f.onExec = func(_ string, argv []string, _, _ io.Writer) (int, error, bool) {
		if resticSub(argv) == "cat" {
			return exitWrongKey, nil, true
		}
		return 0, nil, false
	}
	res, err = r.Backup(context.Background(), payload(), secrets(), nil)
	require.Error(t, err)
	assert.Contains(t, res.Message, "password does not match")
}

func TestFreezeFailuresAreReportedAndTheServiceIsLeftRunning(t *testing.T) {
	r, f, _, svc := setupFail()
	f.onPause = func(string) error { return errBoom }
	res, err := r.Backup(context.Background(), payload(), secrets(), nil)
	require.Error(t, err)
	assert.Equal(t, "could not freeze the service: boom", res.Message)
	assert.Equal(t, "running", f.state(svc.ID).State)
	assert.False(t, f.has("restic -o s3.region=eu-1 -o s3.bucket-lookup=path backup"), "nothing is uploaded")

	r, f, _, svc = setupFail()
	f.copyExit = 3
	res, err = r.Backup(context.Background(), payload(), secrets(), nil)
	require.Error(t, err)
	assert.Contains(t, res.Message, "copy the volumes: exit status 3")
	assert.Equal(t, "running", f.state(svc.ID).State, "a failed copy still thaws the service")
	assert.True(t, f.has("unpause nc-app-1"))
}

func TestStopModeFailsCleanlyWhenTheServiceCannotBeStopped(t *testing.T) {
	r, f, _, svc := setupFail()
	f.onStop = func(string) error { return errBoom }
	p := payload()
	p.Mode = agentprotocol.VolModeStop
	res, err := r.Backup(context.Background(), p, secrets(), nil)
	require.Error(t, err)
	assert.Equal(t, "could not stop the service: boom", res.Message)
	assert.Equal(t, "running", f.state(svc.ID).State)
	assert.False(t, f.has("copy start"))
}

// flakyHost reports the service as gone from the second lookup on, like a
// container that stopped while the backup waited for the lock.
type flakyHost struct {
	*fakeHost
	lookups int
}

func (h *flakyHost) ServiceContainer(ctx context.Context, id string) (docker.ContainerInfo, bool) {
	h.lookups++
	if h.lookups > 1 {
		return docker.ContainerInfo{}, false
	}
	return h.fakeHost.ServiceContainer(ctx, id)
}

func TestBackupCopiesWithoutTouchingAServiceThatStoppedMeanwhile(t *testing.T) {
	r, f, h, _ := setupFail()
	r.Host = &flakyHost{fakeHost: h}
	res, err := r.Backup(context.Background(), payload(), secrets(), nil)
	require.NoError(t, err)
	assert.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome)
	assert.Equal(t, agentprotocol.VolModeStop, res.Consistency, "what was copied is consistent by definition: nothing was running")
	assert.False(t, f.has("pause"))
	assert.True(t, f.has("copy done"))

	// If that copy fails the message says so.
	r, f, h, _ = setupFail()
	r.Host = &flakyHost{fakeHost: h}
	f.copyExit = 2
	res, err = r.Backup(context.Background(), payload(), secrets(), nil)
	require.Error(t, err)
	assert.Contains(t, res.Message, "copy the volumes: exit status 2")
}

func TestBackupProbeFailureOfTheStagingVolume(t *testing.T) {
	r, f, _, _ := setupFail()
	f.onExec = func(_ string, argv []string, _, _ io.Writer) (int, error, bool) {
		if len(argv) > 2 && strings.Contains(argv[2], "cp --reflink=always") {
			return -1, errBoom, true
		}
		return 0, nil, false
	}
	res, err := r.Backup(context.Background(), payload(), secrets(), nil)
	require.Error(t, err)
	assert.Equal(t, "probe the staging volume: boom", res.Message)
}

func TestBackupUploadFailures(t *testing.T) {
	long := strings.Repeat("x", 900)
	cases := []struct {
		name   string
		out    string
		exit   int
		want   string
		absent string
	}{
		{"exit with restic errors", `{"message_type":"error","error":{"message":"permission denied"},"item":"/vol/data/secret"}` + "\n", 1, "upload failed (restic exit 1): /vol/data/secret: permission denied", ""},
		{"exit without output", "", 1, "upload failed (restic exit 1)", ""},
		{"no snapshot reported", `{"message_type":"status","percent_done":1}` + "\n", 0, "restic finished without reporting a snapshot", ""},
		{"long reasons are clipped", `{"message_type":"error","error":{"message":"` + long + `"}}` + "\n", 1, "…", long},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, f, _, _ := setupFail()
			f.backupOut, f.backupExit = tc.out, tc.exit
			res, err := r.Backup(context.Background(), payload(), secrets(), nil)
			require.Error(t, err)
			assert.Equal(t, agentprotocol.OutcomeFailed, res.Outcome)
			assert.Contains(t, res.Message, tc.want)
			if tc.absent != "" {
				assert.NotContains(t, res.Message, tc.absent)
			}
			assert.Empty(t, res.SnapshotID)
		})
	}

	r, f, _, _ := setupFail()
	f.onExec = func(_ string, argv []string, _, _ io.Writer) (int, error, bool) {
		if resticSub(argv) == "backup" {
			return -1, errBoom, true
		}
		return 0, nil, false
	}
	res, err := r.Backup(context.Background(), payload(), secrets(), nil)
	require.Error(t, err)
	assert.Equal(t, "upload: boom", res.Message)
}

func TestHookFailureModes(t *testing.T) {
	hookExec := func(err error) func(string, []string, io.Writer, io.Writer) (int, error, bool) {
		return func(id string, argv []string, _, _ io.Writer) (int, error, bool) {
			if len(argv) > 0 && argv[0] == "pg_dump" {
				return -1, err, true
			}
			return 0, nil, false
		}
	}
	p := payload()
	p.PreHook = &agentprotocol.VolumeHook{Command: []string{"pg_dump"}}

	r, f, _, _ := setupFail()
	f.onExec = hookExec(errBoom)
	res, err := r.Backup(context.Background(), p, secrets(), nil)
	require.Error(t, err)
	assert.Equal(t, "The before-backup command could not run: boom. Nothing was backed up.", res.Message)

	// A hook that outlives its timeout is reported as such.
	r, f, _, _ = setupFail()
	f.onExec = func(_ string, argv []string, _, _ io.Writer) (int, error, bool) {
		return 0, nil, false
	}
	slow := &Runner{Docker: &hangingHooks{failDocker: f}, Host: r.Host, BackupDir: r.BackupDir, MkdirAll: r.MkdirAll, secondUnit: 20 * time.Millisecond}
	p2 := payload()
	p2.PreHook = &agentprotocol.VolumeHook{Command: []string{"sleep", "99"}, TimeoutSeconds: 1}
	res, err = slow.Backup(context.Background(), p2, secrets(), nil)
	require.Error(t, err)
	assert.Contains(t, res.Message, "The before-backup command timed out after 1s")

	// A failing after-backup command is a warning, not a failed backup.
	r, f, _, _ = setupFail()
	f.hookExit["nc-app-1"] = 4
	p3 := payload()
	p3.PostHook = &agentprotocol.VolumeHook{Command: []string{"rm", "-f", "/data/.b"}}
	res, err = r.Backup(context.Background(), p3, secrets(), nil)
	require.NoError(t, err)
	assert.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome)
	require.Len(t, res.Warnings, 1)
	assert.Contains(t, res.Warnings[0], "The after-backup command failed (exit 4)")
	assert.Contains(t, res.Warnings[0], "no output")
}

// hangingHooks blocks every exec in the service container until its context ends.
type hangingHooks struct{ *failDocker }

func (h *hangingHooks) ContainerExec(ctx context.Context, id string, argv []string, spec docker.ExecSpec, stdout, stderr io.Writer) (int, error) {
	if len(argv) > 0 && argv[0] == "sleep" {
		<-ctx.Done()
		return -1, ctx.Err()
	}
	return h.failDocker.ContainerExec(ctx, id, argv, spec, stdout, stderr)
}

func TestBackupValidation(t *testing.T) {
	r, _, _, _ := setupFail()
	for name, mut := range map[string]func(*agentprotocol.VolumeBackupPayload){
		"no ids":       func(p *agentprotocol.VolumeBackupPayload) { p.BackupID = "" },
		"no volumes":   func(p *agentprotocol.VolumeBackupPayload) { p.Volumes = nil },
		"duplicate":    func(p *agentprotocol.VolumeBackupPayload) { p.Volumes = append(p.Volumes, p.Volumes[0]) },
		"foreign vol":  func(p *agentprotocol.VolumeBackupPayload) { p.Volumes[0].DockerName = "postgres_data" },
		"bad vol name": func(p *agentprotocol.VolumeBackupPayload) { p.Volumes[0].Name = "../etc" },
		"unknown mode": func(p *agentprotocol.VolumeBackupPayload) { p.Mode = "yolo" },
		"empty hook":   func(p *agentprotocol.VolumeBackupPayload) { p.PreHook = &agentprotocol.VolumeHook{} },
		"empty program": func(p *agentprotocol.VolumeBackupPayload) {
			p.PostHook = &agentprotocol.VolumeHook{Command: []string{""}}
		},
	} {
		p := payload()
		p.Volumes = append([]agentprotocol.VolumeRef(nil), p.Volumes...)
		mut(&p)
		res, err := r.Backup(context.Background(), p, secrets(), nil)
		require.Error(t, err, name)
		assert.Equal(t, agentprotocol.OutcomeFailed, res.Outcome, name)
		assert.NotEmpty(t, res.Message, name)
	}
}

func TestBackupWaitsForTheRepositoryLockAndGivesUpWithItsContext(t *testing.T) {
	r, f, _, _ := setupFail()
	unlock, err := r.lockRepo(context.Background(), svcID) // another backup of the service holds the repository
	require.NoError(t, err)
	defer unlock()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res, err := r.Backup(ctx, payload(), secrets(), nil)
	require.Error(t, err)
	assert.Contains(t, res.Message, "waiting for another backup of this service")
	assert.Empty(t, f.created, "no helper is created while the repository is in use")

	_, err = r.Forget(ctx, agentprotocol.VolumeForgetPayload{ServiceID: svcID, Repo: s3Repo1(), SnapshotIDs: []string{"ab"}}, secrets(), nil)
	require.ErrorContains(t, err, "waiting for another backup of this service")
	_, err = r.Restore(ctx, restorePayload(false), secrets(), nil)
	require.ErrorContains(t, err, "waiting for another backup of this service")
}

func TestForgetFailures(t *testing.T) {
	full := strings.Repeat("1", 64)
	r, _, _, _ := setupFail()
	res, err := r.Forget(context.Background(), agentprotocol.VolumeForgetPayload{Repo: s3Repo1()}, secrets(), nil)
	require.ErrorContains(t, err, "serviceId is required")
	assert.Equal(t, agentprotocol.OutcomeFailed, res.Outcome)

	_, err = r.Forget(context.Background(), agentprotocol.VolumeForgetPayload{ServiceID: svcID}, secrets(), nil)
	require.Error(t, err, "a payload without a repository is rejected")

	// The helper cannot be created.
	r, f, _, _ := setupFail()
	f.onCreate = func(docker.ContainerSpec) error { return errBoom }
	_, err = r.Forget(context.Background(), agentprotocol.VolumeForgetPayload{ServiceID: svcID, Repo: s3Repo1(), SnapshotIDs: []string{full}}, secrets(), nil)
	require.ErrorContains(t, err, "create the backup helper: boom")

	// The repository cannot be opened.
	r, f, _, _ = setupFail()
	f.onExec = func(_ string, argv []string, _, _ io.Writer) (int, error, bool) {
		if resticSub(argv) == "cat" {
			return exitWrongKey, nil, true
		}
		return 0, nil, false
	}
	_, err = r.Forget(context.Background(), agentprotocol.VolumeForgetPayload{ServiceID: svcID, Repo: s3Repo1(), SnapshotIDs: []string{full}}, secrets(), nil)
	require.ErrorContains(t, err, "password does not match")

	// The snapshot list is unreadable.
	r, f, _, _ = setupFail()
	f.onExec = func(_ string, argv []string, stdout, _ io.Writer) (int, error, bool) {
		if resticSub(argv) == "snapshots" {
			_, _ = io.WriteString(stdout, "not json")
			return 0, nil, true
		}
		return 0, nil, false
	}
	_, err = r.Forget(context.Background(), agentprotocol.VolumeForgetPayload{ServiceID: svcID, Repo: s3Repo1(), SnapshotIDs: []string{full}}, secrets(), nil)
	require.ErrorContains(t, err, "parse restic snapshots")

	// A policy run that restic rejects, and an unreadable dry-run answer.
	r, f, _, _ = setupFail()
	f.snapshots = []string{full}
	f.onExec = func(_ string, argv []string, stdout, stderr io.Writer) (int, error, bool) {
		if resticSub(argv) == "forget" && contains(argv, "--dry-run") {
			_, _ = io.WriteString(stderr, "Fatal: invalid policy")
			return 1, nil, true
		}
		return 0, nil, false
	}
	keep := &agentprotocol.VolumeKeep{Daily: 1}
	_, err = r.Forget(context.Background(), agentprotocol.VolumeForgetPayload{ServiceID: svcID, Repo: s3Repo1(), Keep: keep}, secrets(), nil)
	require.ErrorContains(t, err, "restic forget exited with status 1: Fatal: invalid policy")

	r, f, _, _ = setupFail()
	f.snapshots = []string{full}
	f.forgetOut = "garbage"
	_, err = r.Forget(context.Background(), agentprotocol.VolumeForgetPayload{ServiceID: svcID, Repo: s3Repo1(), Keep: keep}, secrets(), nil)
	require.Error(t, err)

	// The removal itself fails, or the list afterwards cannot be read.
	r, f, _, _ = setupFail()
	f.snapshots = []string{full}
	f.onExec = func(_ string, argv []string, _, stderr io.Writer) (int, error, bool) {
		if resticSub(argv) == "forget" && !contains(argv, "--dry-run") {
			_, _ = io.WriteString(stderr, "Fatal: unable to lock repository")
			return 1, nil, true
		}
		return 0, nil, false
	}
	_, err = r.Forget(context.Background(), agentprotocol.VolumeForgetPayload{ServiceID: svcID, Repo: s3Repo1(), SnapshotIDs: []string{full}}, secrets(), nil)
	require.ErrorContains(t, err, "unable to lock repository")

	r, f, _, _ = setupFail()
	f.snapshots = []string{full}
	calls := 0
	f.onExec = func(_ string, argv []string, stdout, _ io.Writer) (int, error, bool) {
		if resticSub(argv) == "snapshots" {
			calls++
			if calls == 2 {
				_, _ = io.WriteString(stdout, "oops")
				return 0, nil, true
			}
		}
		return 0, nil, false
	}
	_, err = r.Forget(context.Background(), agentprotocol.VolumeForgetPayload{ServiceID: svcID, Repo: s3Repo1(), SnapshotIDs: []string{full}}, secrets(), nil)
	require.ErrorContains(t, err, "parse restic snapshots")
}

func TestForgetWithNothingMatchingRemovesNothing(t *testing.T) {
	r, f, _, _ := setupFail()
	f.snapshots = []string{strings.Repeat("1", 64)}
	res, err := r.Forget(context.Background(), agentprotocol.VolumeForgetPayload{ServiceID: svcID, Repo: s3Repo1(), SnapshotIDs: []string{"ffff", ""}}, secrets(), nil)
	require.NoError(t, err)
	assert.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome)
	assert.Equal(t, "nothing to remove", res.Message)
	assert.Empty(t, res.Removed)
	assert.False(t, f.has("restic -o s3.region=eu-1 -o s3.bucket-lookup=path forget"))
}

func TestRestoreFailures(t *testing.T) {
	// Busy service: nothing is restored and the service is untouched.
	r, f, h, svc := setupFail()
	h.busy = true
	p := restorePayload(true)
	p.Volume.DockerName = "nc-vol-11111111-data"
	res, err := r.Restore(context.Background(), p, secrets(), nil)
	require.Error(t, err)
	assert.Contains(t, res.Message, "in progress; nothing was restored")
	assert.Equal(t, "running", f.state(svc.ID).State)
	assert.False(t, f.has("stop"))

	// A payload with a missing field.
	_, err = r.Restore(context.Background(), agentprotocol.VolumeRestorePayload{}, secrets(), nil)
	require.ErrorContains(t, err, "restoreId, serviceId and snapshotId are required")

	// A volume that is not this product's.
	bad := restorePayload(false)
	bad.Volume.DockerName = "postgres_data"
	_, err = r.Restore(context.Background(), bad, secrets(), nil)
	require.ErrorContains(t, err, "invalid Docker volume")

	// The new volume cannot be created.
	r, f, _, _ = setupFail()
	f.onVolume = func(n string) error {
		if strings.HasPrefix(n, "nc-vol-") {
			return errBoom
		}
		return nil
	}
	_, err = r.Restore(context.Background(), restorePayload(false), secrets(), nil)
	require.ErrorContains(t, err, "create the new volume: boom")

	// The helper fails: the new volume is cleaned up again.
	r, f, _, _ = setupFail()
	f.onCreate = func(docker.ContainerSpec) error { return errBoom }
	_, err = r.Restore(context.Background(), restorePayload(false), secrets(), nil)
	require.ErrorContains(t, err, "create the backup helper: boom")
	assert.True(t, f.has("volume remove nc-vol-11111111-data-r261027120000"))

	// The repository is missing at the destination.
	r, f, _, _ = setupFail()
	f.repoExists = false
	_, err = r.Restore(context.Background(), restorePayload(false), secrets(), nil)
	require.ErrorContains(t, err, "the backup repository was not found at the destination")
	assert.True(t, f.has("volume remove nc-vol-11111111-data-r261027120000"))

	// The service cannot be stopped for an in-place restore.
	r, f, _, svc = setupFail()
	f.onStop = func(string) error { return errBoom }
	_, err = r.Restore(context.Background(), p, secrets(), nil)
	require.ErrorContains(t, err, "could not stop the service: boom")
	assert.Equal(t, "running", f.state(svc.ID).State)
	assert.False(t, f.has("clear restore"), "the volume is not emptied while the service still runs")

	// Emptying the volume fails: the service is started again.
	r, f, _, svc = setupFail()
	f.onExec = func(_ string, argv []string, _, _ io.Writer) (int, error, bool) {
		if len(argv) > 2 && strings.Contains(argv[2], "find /restore") {
			return 1, nil, true
		}
		return 0, nil, false
	}
	_, err = r.Restore(context.Background(), p, secrets(), nil)
	require.ErrorContains(t, err, "empty the volume: exit status 1")
	assert.Equal(t, "running", f.state(svc.ID).State)
	assert.False(t, f.has("restic -o s3.region=eu-1 -o s3.bucket-lookup=path restore"), "nothing is restored into a volume that could not be emptied")
}

func TestRestoreInPlaceOfAStoppedServiceDoesNotStartIt(t *testing.T) {
	r, f, _, svc := setupFail()
	f.mu.Lock()
	f.containers[svc.ID].State, f.containers[svc.ID].Running = "exited", false
	f.mu.Unlock()
	p := restorePayload(true)
	p.Volume.DockerName = "nc-vol-11111111-data"
	res, err := r.Restore(context.Background(), p, secrets(), nil)
	require.NoError(t, err)
	assert.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome)
	assert.False(t, f.has("start nc-app-1"), "a service that was stopped stays stopped")
	assert.False(t, f.has("stop nc-app-1"))
	assert.Equal(t, "exited", f.state(svc.ID).State)
}

func TestRemoveVolumesReportsEachProblem(t *testing.T) {
	r, f, _, _ := setupFail()
	f.volumes["nc-vol-11111111-gone"] = nil
	f.onRmVolume = func(n string) error {
		if n == "nc-vol-11111111-busy" {
			return errors.New("volume is in use")
		}
		return nil
	}
	res, err := r.RemoveVolumes(context.Background(), agentprotocol.VolumeRemovePayload{
		ServiceID: svcID,
		Names:     []string{"nc-vol-11111111-gone", "nc-vol-11111111-missing", "nc-vol-11111111-busy", "other", "nc-vol-a/b"},
	})
	require.Error(t, err)
	assert.Equal(t, agentprotocol.OutcomeFailed, res.Outcome)
	assert.Contains(t, res.Message, "volume is in use")
	assert.Contains(t, res.Message, `refusing to remove "other"`)
	assert.Contains(t, res.Message, `refusing to remove "nc-vol-a/b"`)
	assert.NotContains(t, res.Message, "missing", "a volume that is already gone is not an error")
	assert.NotContains(t, f.volumes, "nc-vol-11111111-gone")

	res, err = r.RemoveVolumes(context.Background(), agentprotocol.VolumeRemovePayload{ServiceID: svcID, Names: []string{"nc-vol-11111111-missing"}})
	require.NoError(t, err)
	assert.Equal(t, "removed 1 volume(s)", res.Message)
}

// ---- sweep ----

func deadHelper(f *failDocker, svc *docker.ContainerInfo, mode string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.containers["hlpdead"] = &docker.ContainerInfo{
		ID: "hlpdead", Name: "nebulactrl-bak-bdeadbeef", State: "running", Running: true,
		Labels: map[string]string{
			agentprotocol.LabelHelper: agentprotocol.HelperBackup, agentprotocol.LabelBackupID: "deadbeef-0000",
			agentprotocol.LabelBackupContainer: svc.ID, agentprotocol.LabelBackupMode: mode,
		},
	}
	f.volumes["nc-bak-deadbeef"] = nil
}

func TestSweepReportsWhatItCouldNotRecover(t *testing.T) {
	// Listing fails.
	r, f, _, _ := setupFail()
	f.onList = func(docker.ListFilter) error { return errBoom }
	require.ErrorContains(t, r.Sweep(context.Background(), nil), "sweep: list backup helpers: boom")

	// The second listing (service containers) fails after helpers were handled.
	r, f, _, svc := setupFail()
	deadHelper(f, svc, agentprotocol.VolModeFreeze)
	n := 0
	f.onList = func(docker.ListFilter) error {
		n++
		if n == 2 {
			return errBoom
		}
		return nil
	}
	require.ErrorContains(t, r.Sweep(context.Background(), nil), "sweep: list containers: boom")
	assert.True(t, f.has("remove nebulactrl-bak-bdeadbeef"), "the stale helper was still cleaned up")

	// A paused service container that cannot be thawed keeps its helper.
	r, f, _, svc = setupFail()
	deadHelper(f, svc, agentprotocol.VolModeFreeze)
	f.mu.Lock()
	f.containers[svc.ID].State = "paused"
	f.mu.Unlock()
	f.onUnpause = func(string) error { return errBoom }
	err := r.Sweep(context.Background(), func(string) bool { return false })
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sweep: unpause nc-app-1: boom")
	assert.False(t, f.has("remove nebulactrl-bak-bdeadbeef"), "the helper (the evidence) stays until the service is back")
	assert.Contains(t, f.volumes, "nc-bak-deadbeef")

	// A stopped service whose start fails.
	r, f, _, svc = setupFail()
	deadHelper(f, svc, agentprotocol.VolModeStop)
	f.mu.Lock()
	f.containers[svc.ID].State, f.containers[svc.ID].Running = "exited", false
	f.mu.Unlock()
	f.onStart = func(id string) error {
		if id == svc.ID {
			return errBoom
		}
		return nil
	}
	err = r.Sweep(context.Background(), nil)
	require.ErrorContains(t, err, "sweep: start nc-app-1: boom")
	assert.False(t, f.has("remove nebulactrl-bak-bdeadbeef"))

	// The service container cannot be inspected.
	r, f, _, svc = setupFail()
	deadHelper(f, svc, agentprotocol.VolModeFreeze)
	f.onInspect = func(id string) error {
		if id == svc.ID {
			return errBoom
		}
		return nil
	}
	require.ErrorContains(t, r.Sweep(context.Background(), nil), "sweep: inspect "+svc.ID+": boom")

	// The service container is gone: nothing to recover, the helper is removed.
	r, f, _, svc = setupFail()
	deadHelper(f, svc, agentprotocol.VolModeFreeze)
	f.mu.Lock()
	delete(f.containers, svc.ID)
	f.mu.Unlock()
	require.NoError(t, r.Sweep(context.Background(), nil))
	assert.True(t, f.has("remove nebulactrl-bak-bdeadbeef"))

	// The helper itself cannot be removed; a missing staging volume is only logged.
	r, f, _, svc = setupFail()
	deadHelper(f, svc, agentprotocol.VolModeFreeze)
	f.onRemove = func(id string) error {
		if id == "hlpdead" {
			return errBoom
		}
		return nil
	}
	require.ErrorContains(t, r.Sweep(context.Background(), nil), "sweep: remove nebulactrl-bak-bdeadbeef: boom")

	r, f, _, svc = setupFail()
	deadHelper(f, svc, agentprotocol.VolModeFreeze)
	f.onRmVolume = func(string) error { return errBoom }
	require.NoError(t, r.Sweep(context.Background(), nil), "a staging volume that cannot be removed is not a sweep failure")
	assert.True(t, f.has("remove nebulactrl-bak-bdeadbeef"))
}

func TestSweepLeavesCreatedAndRunningServiceContainersAlone(t *testing.T) {
	r, f, _, svc := setupFail()
	deadHelper(f, svc, agentprotocol.VolModeStop)
	f.mu.Lock()
	f.containers[svc.ID].State, f.containers[svc.ID].Running = "created", false
	f.mu.Unlock()
	require.NoError(t, r.Sweep(context.Background(), nil))
	assert.False(t, f.has("start nc-app-1"), "a container that was never started is not started by the sweep")
	assert.True(t, f.has("remove nebulactrl-bak-bdeadbeef"))
}

func TestOrphanedPauseThawFailureIsReported(t *testing.T) {
	r, f, _, svc := setupFail()
	f.mu.Lock()
	f.containers[svc.ID].State = "paused"
	f.mu.Unlock()
	f.onUnpause = func(string) error { return errBoom }
	require.ErrorContains(t, r.Sweep(context.Background(), nil), "sweep: unpause nc-app-1: boom")
}

// ---- file requests ----

func fileReq(op string) agentprotocol.VolFileRequest {
	return agentprotocol.VolFileRequest{
		RequestID: "req-5", Op: op, ServiceID: svcID, Repo: s3Repo1(), SnapshotID: strings.Repeat("a", 64),
		Volume: "data", Path: "/", Secrets: secrets(),
	}
}

func TestFileErrorsCarryAProtocolCode(t *testing.T) {
	r, f, _, _ := setupFail()
	f.onExec = func(_ string, argv []string, _, stderr io.Writer) (int, error, bool) {
		if resticSub(argv) == "ls" {
			_, _ = io.WriteString(stderr, "Fatal: path /vol/data/gone: file does not exist, no such file")
			return 1, nil, true
		}
		return 0, nil, false
	}
	var fe *FileError
	_, _, err := r.ListFiles(context.Background(), fileReq(agentprotocol.VolFileList), nil)
	require.ErrorAs(t, err, &fe)
	assert.Equal(t, agentprotocol.FSErrNotFound, fe.Code)
	assert.Equal(t, "no such file or directory", err.Error())

	_, _, err = r.OpenFile(context.Background(), fileReq(agentprotocol.VolFileRead), nil)
	require.ErrorAs(t, err, &fe)
	assert.Equal(t, agentprotocol.FSErrNotFound, fe.Code)

	f.onExec = func(_ string, argv []string, _, stderr io.Writer) (int, error, bool) {
		if resticSub(argv) == "ls" {
			_, _ = io.WriteString(stderr, "Fatal: wrong password or no key found")
			return 1, nil, true
		}
		return 0, nil, false
	}
	_, _, err = r.ListFiles(context.Background(), fileReq(agentprotocol.VolFileList), nil)
	require.ErrorAs(t, err, &fe)
	assert.Equal(t, agentprotocol.FSErrFailed, fe.Code)
	assert.Contains(t, err.Error(), "list files: restic exit 1: Fatal: wrong password")
	_, _, err = r.OpenFile(context.Background(), fileReq(agentprotocol.VolFileRead), nil)
	require.ErrorAs(t, err, &fe)
	assert.Contains(t, err.Error(), "stat: restic exit 1")

	f.onExec = func(_ string, argv []string, _, _ io.Writer) (int, error, bool) {
		if resticSub(argv) == "ls" {
			return -1, errBoom, true
		}
		return 0, nil, false
	}
	_, _, err = r.ListFiles(context.Background(), fileReq(agentprotocol.VolFileList), nil)
	require.ErrorContains(t, err, "list files: boom")
	_, _, err = r.OpenFile(context.Background(), fileReq(agentprotocol.VolFileRead), nil)
	require.ErrorContains(t, err, "stat: boom")
}

func TestFileHelperFailuresAndInvalidRequests(t *testing.T) {
	r, f, _, _ := setupFail()
	f.onCreate = func(docker.ContainerSpec) error { return errBoom }
	_, _, err := r.ListFiles(context.Background(), fileReq(agentprotocol.VolFileList), nil)
	require.ErrorContains(t, err, "create the backup helper: boom")
	_, _, err = r.OpenFile(context.Background(), fileReq(agentprotocol.VolFileRead), nil)
	require.ErrorContains(t, err, "create the backup helper: boom")

	bad := fileReq(agentprotocol.VolFileList)
	bad.Repo = agentprotocol.VolumeRepo{}
	_, _, err = r.ListFiles(context.Background(), bad, nil)
	require.Error(t, err)

	bad = fileReq(agentprotocol.VolFileList)
	bad.RequestID = ""
	var fe *FileError
	_, _, err = r.ListFiles(context.Background(), bad, nil)
	require.ErrorAs(t, err, &fe)
	assert.Equal(t, "invalid file request", fe.Error())
}

func TestOpenFileRefusesWhatCannotBeRead(t *testing.T) {
	r, f, _, _ := setupFail()
	read := fileReq(agentprotocol.VolFileRead)
	read.Path = "/a.txt"
	var fe *FileError

	// The path is not in the snapshot.
	f.lsOut = `{"message_type":"snapshot"}` + "\n"
	_, _, err := r.OpenFile(context.Background(), read, nil)
	require.ErrorAs(t, err, &fe)
	assert.Equal(t, agentprotocol.FSErrNotFound, fe.Code)

	// A symlink is not a regular file.
	f.lsOut = `{"name":"a.txt","type":"symlink","path":"/vol/data/a.txt","message_type":"node"}` + "\n"
	_, _, err = r.OpenFile(context.Background(), read, nil)
	require.ErrorAs(t, err, &fe)
	assert.Equal(t, agentprotocol.FSErrFailed, fe.Code)
	assert.Contains(t, err.Error(), "not a regular file")

	// A file over the caller's limit is refused before anything is dumped.
	f.lsOut = `{"name":"a.txt","type":"file","path":"/vol/data/a.txt","size":5000,"mode":420,"message_type":"node"}` + "\n"
	read.MaxBytes = 1000
	_, _, err = r.OpenFile(context.Background(), read, nil)
	require.ErrorAs(t, err, &fe)
	assert.Equal(t, agentprotocol.FSErrTooLarge, fe.Code)
	assert.False(t, f.has("restic -o s3.region=eu-1 -o s3.bucket-lookup=path dump"))

	// The archive of the volume root is named after the volume.
	f.lsOut = `{"name":"data","type":"dir","path":"/vol/data","message_type":"node"}` + "\n"
	arch := fileReq(agentprotocol.VolFileArchive)
	entry, rc, err := r.OpenFile(context.Background(), arch, nil)
	require.NoError(t, err)
	_, _ = io.ReadAll(rc)
	require.NoError(t, rc.Close())
	assert.Equal(t, "data.zip", entry.Name)
}

func TestOpenFileDumpFailureSurfacesWhileReading(t *testing.T) {
	r, f, _, _ := setupFail()
	f.lsOut = `{"name":"a.txt","type":"file","path":"/vol/data/a.txt","size":6,"mode":420,"message_type":"node"}` + "\n"
	f.onExec = func(_ string, argv []string, _, stderr io.Writer) (int, error, bool) {
		if resticSub(argv) == "dump" {
			_, _ = io.WriteString(stderr, "Fatal: blob not found")
			return 1, nil, true
		}
		return 0, nil, false
	}
	req := fileReq(agentprotocol.VolFileRead)
	req.Path = "/a.txt"
	_, rc, err := r.OpenFile(context.Background(), req, nil)
	require.NoError(t, err)
	_, err = io.ReadAll(rc)
	require.ErrorContains(t, err, "restic dump exited with status 1: Fatal: blob not found")
	require.NoError(t, rc.Close())
	require.NoError(t, rc.Close(), "Close is idempotent")
}

func TestListFilesTruncatesHugeDirectories(t *testing.T) {
	r, f, _, _ := setupFail()
	var b strings.Builder
	b.WriteString(`{"name":"data","type":"dir","path":"/vol/data","message_type":"node"}` + "\n")
	for i := range agentprotocol.VolFileListMax + 5 {
		name := "f" + strconv.Itoa(i)
		b.WriteString(`{"name":"` + name + `","type":"file","path":"/vol/data/` + name + `","size":1,"mode":420,"message_type":"node"}` + "\n")
	}
	f.lsOut = b.String()
	entries, truncated, err := r.ListFiles(context.Background(), fileReq(agentprotocol.VolFileList), nil)
	require.NoError(t, err)
	assert.True(t, truncated)
	assert.Len(t, entries, agentprotocol.VolFileListMax)
}

func TestListingAFileIsNotADirectoryError(t *testing.T) {
	r, f, _, _ := setupFail()
	f.lsOut = `{"name":"a.txt","type":"file","path":"/vol/data/a.txt","size":6,"mode":420,"message_type":"node"}` + "\n"
	req := fileReq(agentprotocol.VolFileList)
	req.Path = "/a.txt"
	var fe *FileError
	_, _, err := r.ListFiles(context.Background(), req, nil)
	require.ErrorAs(t, err, &fe)
	assert.Equal(t, agentprotocol.FSErrNotDir, fe.Code)
}

func TestEntryOfMapsNodeTypes(t *testing.T) {
	for typ, want := range map[string]string{
		"file": agentprotocol.FSTypeFile, "dir": agentprotocol.FSTypeDir, "symlink": agentprotocol.FSTypeLink, "fifo": agentprotocol.FSTypeOther,
	} {
		e := entryOf(lsNode{Name: "n", Type: typ, Size: 12, Mode: 0o100755, MTime: "2026-10-27T07:15:23Z", Link: "target"})
		assert.Equal(t, want, e.Type, typ)
		assert.Equal(t, "0755", e.Mode)
		assert.Equal(t, "target", e.LinkTarget)
		assert.Equal(t, time.Date(2026, 10, 27, 7, 15, 23, 0, time.UTC).Unix(), e.MTime)
	}
	assert.Zero(t, entryOf(lsNode{Type: "dir", Size: 99}).Size, "a directory has no size")
	assert.Zero(t, entryOf(lsNode{Type: "file", MTime: "yesterday"}).MTime, "an unreadable time is left out")
}

func TestSummaryCollectorFlushesATrailingLine(t *testing.T) {
	c := &summaryCollector{}
	_, _ = c.Write([]byte(strings.TrimSuffix(summaryLine("cafe"), "\n")))
	assert.False(t, c.found, "an unterminated line is not parsed yet")
	c.flush()
	assert.True(t, c.found)
	assert.Equal(t, "cafe", c.s.SnapshotID)
	c.flush() // nothing left
}
