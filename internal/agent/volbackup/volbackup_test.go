package volbackup

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

const (
	svcID    = "11111111-2222-3333-4444-555555555555"
	backupID = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	pw       = "restic-pass-4242"
	s3Secret = "s3-secret-key-9999"
)

func secrets() map[string]string {
	return map[string]string{
		agentprotocol.SecretResticPassword: pw, agentprotocol.SecretBackupAccessKey: "AKIATEST", agentprotocol.SecretBackupSecretKey: s3Secret,
	}
}

func s3Repo1() agentprotocol.VolumeRepo {
	return agentprotocol.VolumeRepo{
		Destination: agentprotocol.BackupDestination{Kind: agentprotocol.DestS3, Endpoint: "https://s3.example.com", Bucket: "bk", Region: "eu-1", PathStyle: true},
		Path:        "volumes/" + svcID,
	}
}

func vols() []agentprotocol.VolumeRef {
	return []agentprotocol.VolumeRef{{Name: "data", DockerName: "nc-vol-11111111-data", MountPath: "/data"}}
}

func payload() agentprotocol.VolumeBackupPayload {
	return agentprotocol.VolumeBackupPayload{BackupID: backupID, ServiceID: svcID, Repo: s3Repo1(), Volumes: vols(), Trigger: "manual"}
}

func setup() (*Runner, *fakeDocker, *fakeHost, *docker.ContainerInfo) {
	d := newFake()
	svc := d.addService("nc-app-1", svcID, true)
	h := &fakeHost{d: d, service: svcID}
	return &Runner{Docker: d, Host: h, BackupDir: "/var/lib/agent/backups", MkdirAll: func(string, os.FileMode) error { return nil }}, d, h, svc
}

func TestBackupFreezeFlow(t *testing.T) {
	r, d, h, svc := setup()
	res, err := r.Backup(context.Background(), payload(), secrets(), nil)
	require.NoError(t, err)
	assert.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome)
	assert.Equal(t, agentprotocol.VolModeFreeze, res.Consistency)
	assert.Equal(t, agentprotocol.StageReflink, res.StageMethod)
	assert.Equal(t, strings.Repeat("ab", 32), res.SnapshotID)
	assert.EqualValues(t, 4096, res.BytesProcessed)
	assert.EqualValues(t, 2048, res.BytesAdded)
	assert.Equal(t, int64(3), res.FilesNew)

	// The repository is opened, then the copy happens inside pause/unpause
	// inside the service lock, and only afterwards the upload starts.
	order := []string{"restic -o s3.region=eu-1 -o s3.bucket-lookup=path cat config", "probe reflink", "lock", "pause nc-app-1", "copy start", "copy done", "unpause nc-app-1", "unlock"}
	last := -1
	for _, ev := range order {
		i := d.index(ev)
		require.GreaterOrEqual(t, i, 0, "missing event %q in %v", ev, d.eventLog())
		require.Greater(t, i, last, "event %q out of order in %v", ev, d.eventLog())
		last = i
	}
	up := d.index("restic -o s3.region=eu-1 -o s3.bucket-lookup=path backup /vol")
	require.Greater(t, up, last, "the upload must start after the thaw: %v", d.eventLog())
	assert.Equal(t, "running", d.state(svc.ID).State)
	assert.Equal(t, 1, h.locks)

	// Everything the backup created is gone again.
	assert.True(t, d.has("remove nebulactrl-bak-b"+agentprotocol.Short(backupID)))
	assert.True(t, d.has("volume remove nc-bak-"+agentprotocol.Short(backupID)))
}

func TestBackupFreezeBudgetExceededThawsAndFails(t *testing.T) {
	r, d, _, svc := setup()
	d.copyDelay = 5 * time.Second
	p := payload()
	p.FreezeBudgetSeconds = 1
	started := time.Now()
	res, err := r.Backup(context.Background(), p, secrets(), nil)
	require.Error(t, err)
	assert.Less(t, time.Since(started), 4*time.Second, "the copy must be cut off at the budget")
	assert.Equal(t, agentprotocol.OutcomeFailed, res.Outcome)
	assert.Equal(t, "Snapshot took longer than 1 s; use Stop mode or a pre-backup hook", res.Message)
	assert.True(t, d.has("pause nc-app-1"))
	assert.True(t, d.has("unpause nc-app-1"), "the service must be thawed: %v", d.eventLog())
	assert.Equal(t, "running", d.state(svc.ID).State)
	assert.False(t, d.has("restic -o s3.region=eu-1 -o s3.bucket-lookup=path backup"), "nothing may be uploaded")
	assert.True(t, d.has("volume remove nc-bak-"), "the staging volume must go")
}

func TestBackupPreHookFailureDoesNotFreeze(t *testing.T) {
	r, d, h, _ := setup()
	d.hookExit["nc-app-1"] = 1
	d.hookOut = "sqlite3: database is locked"
	p := payload()
	p.PreHook = &agentprotocol.VolumeHook{Command: []string{"sqlite3", "/data/app.db", ".backup /data/.b"}}
	p.PostHook = &agentprotocol.VolumeHook{Command: []string{"rm", "-f", "/data/.b"}}
	res, err := r.Backup(context.Background(), p, secrets(), nil)
	require.Error(t, err)
	assert.Contains(t, res.Message, "The before-backup command failed (exit 1)")
	assert.Contains(t, res.Message, "database is locked")
	assert.Contains(t, res.Message, "Nothing was backed up.")
	assert.False(t, d.has("pause"), "a failed hook must not freeze: %v", d.eventLog())
	assert.Zero(t, h.locks)
	assert.False(t, d.has("restic -o s3.region=eu-1 -o s3.bucket-lookup=path backup"))
	assert.True(t, d.has("hook nc-app-1 rm -f /data/.b"), "the after-backup command still cleans up: %v", d.eventLog())
}

func TestBackupPreHookContinueOnError(t *testing.T) {
	r, d, _, _ := setup()
	d.hookExit["nc-app-1"] = 2
	p := payload()
	p.PreHook = &agentprotocol.VolumeHook{Command: []string{"redis-cli", "BGSAVE"}, ContinueOnError: true}
	res, err := r.Backup(context.Background(), p, secrets(), nil)
	require.NoError(t, err)
	assert.Equal(t, "failed_continued", res.HookOutcome)
	require.Len(t, res.Warnings, 1)
	assert.Contains(t, res.Warnings[0], "ignored")
	assert.Less(t, d.index("hook nc-app-1 redis-cli BGSAVE"), d.index("pause nc-app-1"), "the hook runs before the freeze")
}

func TestBackupStopMode(t *testing.T) {
	r, d, _, svc := setup()
	p := payload()
	p.Mode = agentprotocol.VolModeStop
	res, err := r.Backup(context.Background(), p, secrets(), nil)
	require.NoError(t, err)
	assert.Equal(t, agentprotocol.VolModeStop, res.Consistency)
	assert.False(t, d.has("pause"))
	stop, copyDone, start := d.index("stop nc-app-1"), d.index("copy done"), d.index("start nc-app-1")
	require.True(t, 0 <= stop && stop < copyDone && copyDone < start, "stop -> copy -> start expected: %v", d.eventLog())
	assert.Less(t, start, d.index("restic -o s3.region=eu-1 -o s3.bucket-lookup=path backup"), "the service is back before the upload")
	assert.Equal(t, "running", d.state(svc.ID).State)
	// The helper records which container it stopped, for the crash sweep.
	var helper docker.ContainerSpec
	for _, c := range d.created {
		if c.Image == agentprotocol.ResticImage {
			helper = c
		}
	}
	assert.Equal(t, svc.ID, helper.Labels[agentprotocol.LabelBackupContainer])
	assert.Equal(t, agentprotocol.VolModeStop, helper.Labels[agentprotocol.LabelBackupMode])
}

func TestBackupStopModeStartsAgainWhenCopyFails(t *testing.T) {
	r, d, _, svc := setup()
	d.copyExit = 1
	p := payload()
	p.Mode = agentprotocol.VolModeStop
	_, err := r.Backup(context.Background(), p, secrets(), nil)
	require.Error(t, err)
	assert.Equal(t, "running", d.state(svc.ID).State)
}

func TestBackupLiveModeReadsVolumesDirectly(t *testing.T) {
	r, d, h, _ := setup()
	p := payload()
	p.Mode = agentprotocol.VolModeLive
	p.PostHook = &agentprotocol.VolumeHook{Command: []string{"rm", "-f", "/data/.b"}}
	res, err := r.Backup(context.Background(), p, secrets(), nil)
	require.NoError(t, err)
	assert.Equal(t, agentprotocol.VolModeLive, res.Consistency)
	assert.Equal(t, agentprotocol.StageNone, res.StageMethod)
	assert.Zero(t, h.locks)
	assert.False(t, d.has("pause"))
	assert.False(t, d.has("volume create nc-bak-"))
	var helper docker.ContainerSpec
	for _, c := range d.created {
		helper = c
	}
	assert.Contains(t, helper.Mounts, docker.VolumeMount{Volume: "nc-vol-11111111-data", Target: "/vol/data", ReadOnly: true})
	// Live mode keeps the hook's artefact until the upload is done.
	assert.Less(t, d.index("restic -o s3.region=eu-1 -o s3.bucket-lookup=path backup"), d.index("hook nc-app-1 rm"))
}

func TestBackupOfAStoppedServiceReadsDirectly(t *testing.T) {
	r, d, _, svc := setup()
	d.mu.Lock()
	d.containers[svc.ID].State, d.containers[svc.ID].Running = "exited", false
	d.mu.Unlock()
	res, err := r.Backup(context.Background(), payload(), secrets(), nil)
	require.NoError(t, err)
	assert.Equal(t, agentprotocol.VolModeStop, res.Consistency, "a stopped service is consistent by definition")
	assert.False(t, d.has("pause"))
	assert.False(t, d.has("lock"))
}

func TestBackupSkippedWhenServiceStaysBusy(t *testing.T) {
	r, d, h, _ := setup()
	h.busy = true
	res, err := r.Backup(context.Background(), payload(), secrets(), nil)
	require.NoError(t, err)
	assert.Equal(t, agentprotocol.OutcomeSkipped, res.Outcome)
	assert.False(t, d.has("pause"))
	assert.False(t, d.has("restic -o s3.region=eu-1 -o s3.bucket-lookup=path backup"))
	assert.True(t, d.has("remove nebulactrl-bak-"), "the helper is removed")
}

func TestBackupFailsWithoutDiskForAPlainCopy(t *testing.T) {
	r, d, _, _ := setup()
	d.reflink = false
	d.diskProbe = "500000 1000" // 500 MB used, 1 MB free
	res, err := r.Backup(context.Background(), payload(), secrets(), nil)
	require.Error(t, err)
	assert.Contains(t, res.Message, "not enough free disk")
	assert.False(t, d.has("pause"), "the check happens before the freeze")
}

func TestBackupPlainCopyWhenNoReflink(t *testing.T) {
	r, d, _, _ := setup()
	d.reflink = false
	res, err := r.Backup(context.Background(), payload(), secrets(), nil)
	require.NoError(t, err)
	assert.Equal(t, agentprotocol.StageCopy, res.StageMethod)
}

func TestBackupIncompleteSnapshotIsForgotten(t *testing.T) {
	r, d, _, _ := setup()
	d.backupExit = exitIncomplete
	d.backupOut = `{"message_type":"error","error":{"message":"permission denied"},"item":"/vol/data/x"}` + "\n" + summaryLine("cd"+strings.Repeat("0", 62))
	res, err := r.Backup(context.Background(), payload(), secrets(), nil)
	require.Error(t, err)
	assert.Contains(t, res.Message, "some files could not be read")
	assert.Contains(t, res.Message, "permission denied")
	assert.True(t, d.has("restic -o s3.region=eu-1 -o s3.bucket-lookup=path forget cd"), "the partial snapshot must not stay: %v", d.eventLog())
}

func TestHelperIsHardenedLabelledAndHoldsNoSecrets(t *testing.T) {
	r, d, _, _ := setup()
	_, err := r.Backup(context.Background(), payload(), secrets(), nil)
	require.NoError(t, err)
	var helper *docker.ContainerSpec
	for i := range d.created {
		if d.created[i].Image == agentprotocol.ResticImage {
			helper = &d.created[i]
		}
	}
	require.NotNil(t, helper)
	assert.Equal(t, agentprotocol.HelperBackup, helper.Labels[agentprotocol.LabelHelper])
	assert.NotContains(t, helper.Labels, agentprotocol.LabelManaged, "helpers must never look like managed services")
	assert.Equal(t, "nebulactrl-bak-b"+agentprotocol.Short(backupID), helper.Name)
	assert.Equal(t, []string{"ALL"}, helper.CapDrop)
	assert.NotContains(t, helper.CapAdd, "NET_ADMIN")
	assert.True(t, helper.ReadonlyRootfs)
	assert.Contains(t, helper.SecurityOpt, "no-new-privileges")
	assert.EqualValues(t, agentprotocol.HelperMemoryBytes, helper.MemoryBytes)
	assert.Empty(t, helper.Ports)
	assert.Empty(t, helper.Env, "secrets are passed per exec, never in the create config")
	// Source volumes are read-only, the staging volume is writable.
	assert.Contains(t, helper.Mounts, docker.VolumeMount{Volume: "nc-vol-11111111-data", Target: "/src/data", ReadOnly: true})
	assert.Contains(t, helper.Mounts, docker.VolumeMount{Volume: "nc-bak-" + agentprotocol.Short(backupID), Target: "/vol"})
	assert.Contains(t, helper.Mounts, docker.VolumeMount{Volume: agentprotocol.ResticCacheVolume, Target: "/cache"})

	raw, _ := json.Marshal(d.created)
	for _, s := range []string{pw, s3Secret} {
		assert.NotContains(t, string(raw), s)
		assert.NotContains(t, strings.Join(d.eventLog(), "\n"), s, "secrets must not appear in argv")
	}
	var sawPassword bool
	for _, env := range d.envs {
		for _, e := range env {
			if e == "RESTIC_PASSWORD="+pw {
				sawPassword = true
			}
		}
	}
	assert.True(t, sawPassword, "restic gets the password through the exec environment")
}

func TestBackupPullsTheImageOnlyWhenMissing(t *testing.T) {
	r, d, _, _ := setup()
	d.hasImage = false
	_, err := r.Backup(context.Background(), payload(), secrets(), nil)
	require.NoError(t, err)
	assert.Equal(t, []string{agentprotocol.ResticImage}, d.pulled)
	d.pulled = nil
	_, err = r.Backup(context.Background(), payload(), secrets(), nil)
	require.NoError(t, err)
	assert.Empty(t, d.pulled)
}

func TestBackupCreatesAMissingRepositoryButNotOnWrongKey(t *testing.T) {
	r, d, _, _ := setup()
	d.repoExists = false
	_, err := r.Backup(context.Background(), payload(), secrets(), nil)
	require.NoError(t, err)
	assert.True(t, d.has("restic -o s3.region=eu-1 -o s3.bucket-lookup=path init --repository-version 2"))
}

func TestBackupRejectsBadPayloads(t *testing.T) {
	r, _, _, _ := setup()
	for name, mut := range map[string]func(*agentprotocol.VolumeBackupPayload){
		"no volumes":      func(p *agentprotocol.VolumeBackupPayload) { p.Volumes = nil },
		"foreign volume":  func(p *agentprotocol.VolumeBackupPayload) { p.Volumes[0].DockerName = "postgres_data" },
		"path in name":    func(p *agentprotocol.VolumeBackupPayload) { p.Volumes[0].Name = "../etc" },
		"bad repo path":   func(p *agentprotocol.VolumeBackupPayload) { p.Repo.Path = "../x" },
		"bad mode":        func(p *agentprotocol.VolumeBackupPayload) { p.Mode = "pause-everything" },
		"empty hook argv": func(p *agentprotocol.VolumeBackupPayload) { p.PreHook = &agentprotocol.VolumeHook{} },
	} {
		p := payload()
		p.Volumes = vols()
		mut(&p)
		res, err := r.Backup(context.Background(), p, secrets(), nil)
		require.Error(t, err, name)
		assert.Equal(t, agentprotocol.OutcomeFailed, res.Outcome, name)
	}
}

func TestForgetAppliesRetentionFlagsAndSkipsProtected(t *testing.T) {
	r, d, _, _ := setup()
	full := func(c string) string { return strings.Repeat(c, 64) }
	d.snapshots = []string{full("1"), full("2"), full("3")}
	d.snapshotsAf = []string{full("2"), full("3")}
	d.forgetOut = fmt.Sprintf(`[{"keep":[{"id":%q}],"remove":[{"id":%q},{"id":%q}]}]`, full("3"), full("1"), full("2"))
	res, err := r.Forget(context.Background(), agentprotocol.VolumeForgetPayload{
		ServiceID: svcID, Repo: s3Repo1(),
		Keep:    &agentprotocol.VolumeKeep{Daily: agentprotocol.DefaultKeepDaily, Weekly: agentprotocol.DefaultKeepWeekly, Monthly: agentprotocol.DefaultKeepMonthly},
		Protect: []string{full("2")},
	}, secrets(), nil)
	require.NoError(t, err)
	assert.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome)
	assert.Equal(t, []string{full("1")}, res.Removed)

	dry := ""
	for _, e := range d.eventLog() {
		if strings.Contains(e, "forget --dry-run") {
			dry = e
		}
	}
	require.NotEmpty(t, dry)
	assert.Contains(t, dry, "--json --keep-daily 7 --keep-weekly 4 --keep-monthly 6 --host nebulactrl-11111111 --tag nc-svc="+svcID)
	// Only the unprotected victim is forgotten, with --prune.
	assert.True(t, d.has("restic -o s3.region=eu-1 -o s3.bucket-lookup=path forget --prune "+full("1")), "%v", d.eventLog())
	assert.False(t, d.has("restic -o s3.region=eu-1 -o s3.bucket-lookup=path forget --prune "+full("1")+" "+full("2")))
}

func TestForgetExplicitSnapshotsByPrefix(t *testing.T) {
	r, d, _, _ := setup()
	full := strings.Repeat("9", 64)
	d.snapshots = []string{full, strings.Repeat("8", 64)}
	d.snapshotsAf = []string{strings.Repeat("8", 64)}
	res, err := r.Forget(context.Background(), agentprotocol.VolumeForgetPayload{
		ServiceID: svcID, Repo: s3Repo1(), SnapshotIDs: []string{full[:8]},
	}, secrets(), nil)
	require.NoError(t, err)
	assert.Equal(t, []string{full}, res.Removed)
	assert.False(t, d.has("restic -o s3.region=eu-1 -o s3.bucket-lookup=path forget --dry-run"), "no policy, no dry run")
}

func TestForgetWithoutRepositoryIsANoOp(t *testing.T) {
	r, d, _, _ := setup()
	d.repoExists = false
	res, err := r.Forget(context.Background(), agentprotocol.VolumeForgetPayload{
		ServiceID: svcID, Repo: s3Repo1(), SnapshotIDs: []string{"deadbeef"},
	}, secrets(), nil)
	require.NoError(t, err)
	assert.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome)
	assert.False(t, d.has("restic -o s3.region=eu-1 -o s3.bucket-lookup=path init"))
}

func restorePayload(inPlace bool) agentprotocol.VolumeRestorePayload {
	return agentprotocol.VolumeRestorePayload{
		RestoreID: "rrrrrrrr-0000-0000-0000-000000000000", ServiceID: svcID, Repo: s3Repo1(), SnapshotID: strings.Repeat("7", 64),
		Volume:  agentprotocol.VolumeRef{Name: "data", DockerName: "nc-vol-11111111-data-r261027120000"},
		InPlace: inPlace,
	}
}

func TestRestoreIntoNewVolume(t *testing.T) {
	r, d, h, svc := setup()
	res, err := r.Restore(context.Background(), restorePayload(false), secrets(), nil)
	require.NoError(t, err)
	assert.Equal(t, "nc-vol-11111111-data-r261027120000", res.DockerName)
	assert.True(t, d.has("volume create nc-vol-11111111-data-r261027120000"))
	assert.True(t, d.has("restic -o s3.region=eu-1 -o s3.bucket-lookup=path restore "+strings.Repeat("7", 64)+":/vol/data --target /restore"), "%v", d.eventLog())
	assert.Zero(t, h.locks, "restoring into a new volume does not touch the service")
	assert.False(t, d.has("stop"))
	assert.Equal(t, "running", d.state(svc.ID).State)
	assert.False(t, d.has("volume remove nc-vol-11111111-data-r261027120000"), "the restored volume is kept")
	var helper docker.ContainerSpec
	for _, c := range d.created {
		helper = c
	}
	assert.Contains(t, helper.Mounts, docker.VolumeMount{Volume: "nc-vol-11111111-data-r261027120000", Target: "/restore"})
	assert.Contains(t, helper.CapAdd, "MKNOD")
}

func TestRestoreFailureRemovesTheNewVolume(t *testing.T) {
	r, d, _, _ := setup()
	d.restoreExit = 1
	_, err := r.Restore(context.Background(), restorePayload(false), secrets(), nil)
	require.Error(t, err)
	assert.True(t, d.has("volume remove nc-vol-11111111-data-r261027120000"))
}

func TestRestoreInPlaceStopsClearsRestoresStarts(t *testing.T) {
	r, d, _, svc := setup()
	p := restorePayload(true)
	p.Volume.DockerName = "nc-vol-11111111-data"
	_, err := r.Restore(context.Background(), p, secrets(), nil)
	require.NoError(t, err)
	seq := []string{"lock", "stop nc-app-1", "clear restore", "restic -o s3.region=eu-1 -o s3.bucket-lookup=path restore", "start nc-app-1", "unlock"}
	last := -1
	for _, ev := range seq {
		i := d.index(ev)
		require.Greater(t, i, last, "%q out of order in %v", ev, d.eventLog())
		last = i
	}
	assert.Equal(t, "running", d.state(svc.ID).State)
	assert.False(t, d.has("volume create nc-vol"), "an in-place restore creates no volume")
}

func TestRestoreInPlaceStartsServiceEvenWhenRestoreFails(t *testing.T) {
	r, d, _, svc := setup()
	d.restoreExit = 1
	p := restorePayload(true)
	p.Volume.DockerName = "nc-vol-11111111-data"
	_, err := r.Restore(context.Background(), p, secrets(), nil)
	require.Error(t, err)
	assert.Equal(t, "running", d.state(svc.ID).State)
	assert.False(t, d.has("volume remove"), "the live volume is never removed")
}

func TestRestoreRejectsBadSnapshotId(t *testing.T) {
	r, _, _, _ := setup()
	p := restorePayload(false)
	p.SnapshotID = "--target=/etc"
	_, err := r.Restore(context.Background(), p, secrets(), nil)
	require.Error(t, err)
}

func TestRemoveVolumesOnlyTakesProductVolumes(t *testing.T) {
	r, d, _, _ := setup()
	d.volumes["nc-vol-11111111-data-r1"] = nil
	d.volumes["postgres_data"] = nil
	_, err := r.RemoveVolumes(context.Background(), agentprotocol.VolumeRemovePayload{ServiceID: svcID, Names: []string{"nc-vol-11111111-data-r1", "postgres_data"}})
	require.Error(t, err)
	assert.NotContains(t, d.volumes, "nc-vol-11111111-data-r1")
	assert.Contains(t, d.volumes, "postgres_data")
}

func TestLocalDestinationOnTheBareHost(t *testing.T) {
	r, d, _, _ := setup()
	p := payload()
	p.Repo.Destination = agentprotocol.BackupDestination{Kind: agentprotocol.DestLocal, LocalDir: "nightly"}
	_, err := r.Backup(context.Background(), p, secrets(), nil)
	require.NoError(t, err)
	var helper docker.ContainerSpec
	for _, c := range d.created {
		helper = c
	}
	assert.Contains(t, helper.Mounts, docker.VolumeMount{HostPath: "/var/lib/agent/backups", Target: "/nc-dest"})
	assert.Equal(t, "none", helper.NetworkMode, "a local repository needs no network")
	assert.True(t, d.has("restic cat config"))
	var repo string
	for _, env := range d.envs {
		for _, e := range env {
			if v, ok := strings.CutPrefix(e, "RESTIC_REPOSITORY="); ok {
				repo = v
			}
		}
	}
	assert.Equal(t, "/nc-dest/nightly/volumes/"+svcID, repo)
}

func TestLocalDestinationOfAContainerizedAgent(t *testing.T) {
	r, d, h, _ := setup()
	self := d.addService("nebulactrl-agent", "", true)
	d.mu.Lock()
	delete(self.Labels, agentprotocol.LabelManaged)
	self.Mounts = []docker.MountInfo{
		{Type: "bind", Source: "/var/run/docker.sock", Destination: "/var/run/docker.sock"},
		{Type: "volume", Name: "agent1-state", Source: "/var/lib/docker/volumes/agent1-state/_data", Destination: "/var/lib/nebulactrl-agent"},
	}
	d.mu.Unlock()
	h.self = self.ID
	r.BackupDir = "/var/lib/nebulactrl-agent/backups"
	p := payload()
	p.Repo.Destination = agentprotocol.BackupDestination{Kind: agentprotocol.DestLocal}
	_, err := r.Backup(context.Background(), p, secrets(), nil)
	require.NoError(t, err)
	var helper docker.ContainerSpec
	for _, c := range d.created {
		helper = c
	}
	assert.Contains(t, helper.Mounts, docker.VolumeMount{Volume: "agent1-state", Target: "/nc-dest"}, "the volume under the agent's backup dir is mounted, not the whole socket")
	var repo string
	for _, env := range d.envs {
		for _, e := range env {
			if v, ok := strings.CutPrefix(e, "RESTIC_REPOSITORY="); ok {
				repo = v
			}
		}
	}
	assert.Equal(t, "/nc-dest/backups/volumes/"+svcID, repo)
}

func TestLocalDestinationOutsideAnyMountIsRefused(t *testing.T) {
	r, d, h, _ := setup()
	self := d.addService("nebulactrl-agent", "", true)
	h.self = self.ID
	p := payload()
	p.Repo.Destination = agentprotocol.BackupDestination{Kind: agentprotocol.DestLocal}
	res, err := r.Backup(context.Background(), p, secrets(), nil)
	require.Error(t, err)
	assert.Contains(t, res.Message, "not on a volume or bind mount")
}

func TestSweepThawsAndCleansUpAfterADeadBackup(t *testing.T) {
	r, d, _, svc := setup()
	d.mu.Lock()
	d.containers[svc.ID].State = "paused"
	d.mu.Unlock()
	// A helper of a backup that no longer runs, with its staging volume.
	d.mu.Lock()
	d.seq++
	d.containers["hlpdead"] = &docker.ContainerInfo{
		ID: "hlpdead", Name: "nebulactrl-bak-bdeadbeef", State: "running", Running: true,
		Labels: map[string]string{
			agentprotocol.LabelHelper: agentprotocol.HelperBackup, agentprotocol.LabelBackupID: "deadbeef-0000",
			agentprotocol.LabelBackupContainer: svc.ID, agentprotocol.LabelBackupMode: agentprotocol.VolModeFreeze,
		},
	}
	d.volumes["nc-bak-deadbeef"] = nil
	d.mu.Unlock()

	require.NoError(t, r.Sweep(context.Background(), func(string) bool { return false }))
	assert.Equal(t, "running", d.state(svc.ID).State, "the frozen service must be thawed")
	assert.True(t, d.has("remove nebulactrl-bak-bdeadbeef"))
	assert.True(t, d.has("volume remove nc-bak-deadbeef"))
}

func TestSweepStartsAServiceADeadStopBackupLeftStopped(t *testing.T) {
	r, d, _, svc := setup()
	d.mu.Lock()
	d.containers[svc.ID].State, d.containers[svc.ID].Running = "exited", false
	d.containers["hlpdead"] = &docker.ContainerInfo{
		ID: "hlpdead", Name: "nebulactrl-bak-bdeadbeef", State: "running", Running: true,
		Labels: map[string]string{
			agentprotocol.LabelHelper: agentprotocol.HelperBackup, agentprotocol.LabelBackupID: "deadbeef-0000",
			agentprotocol.LabelBackupContainer: svc.ID, agentprotocol.LabelBackupMode: agentprotocol.VolModeStop,
		},
	}
	d.mu.Unlock()
	require.NoError(t, r.Sweep(context.Background(), nil))
	assert.Equal(t, "running", d.state(svc.ID).State)
}

func TestSweepLeavesRunningOperationsAlone(t *testing.T) {
	r, d, _, svc := setup()
	d.mu.Lock()
	d.containers[svc.ID].State = "paused"
	d.containers["hlplive"] = &docker.ContainerInfo{
		ID: "hlplive", Name: "nebulactrl-bak-blive", State: "running", Running: true,
		Labels: map[string]string{agentprotocol.LabelHelper: agentprotocol.HelperBackup, agentprotocol.LabelBackupID: "live-op", agentprotocol.LabelBackupContainer: svc.ID},
	}
	d.mu.Unlock()
	defer r.track("live-op")()
	// The service is busy (the backup window holds its lock): even the paused
	// container is left to the running backup.
	require.NoError(t, r.Sweep(context.Background(), func(string) bool { return true }))
	assert.Equal(t, "paused", d.state(svc.ID).State)
	assert.False(t, d.has("remove nebulactrl-bak-blive"))
}

func TestSweepThawsAnOrphanedPause(t *testing.T) {
	r, d, _, svc := setup()
	d.mu.Lock()
	d.containers[svc.ID].State = "paused"
	d.mu.Unlock()
	require.NoError(t, r.Sweep(context.Background(), func(string) bool { return false }))
	assert.Equal(t, "running", d.state(svc.ID).State)
}

func TestS3RepoSpecs(t *testing.T) {
	aws := s3Repo(agentprotocol.BackupDestination{Kind: agentprotocol.DestS3, Bucket: "b", Region: "eu-west-1"}, "volumes/x")
	assert.Equal(t, "s3:s3.eu-west-1.amazonaws.com/b/volumes/x", aws.URL)
	assert.Equal(t, []string{"s3.region=eu-west-1"}, aws.Opts)

	custom := s3Repo(agentprotocol.BackupDestination{Kind: agentprotocol.DestS3, Endpoint: "http://seaweed:8333/", Bucket: "b", PathStyle: true}, "pre/volumes/x")
	assert.Equal(t, "s3:http://seaweed:8333/b/pre/volumes/x", custom.URL)
	assert.Equal(t, []string{"s3.bucket-lookup=path"}, custom.Opts)

	dns := s3Repo(agentprotocol.BackupDestination{Kind: agentprotocol.DestS3, Endpoint: "https://s3.example.com", Bucket: "b"}, "v")
	assert.Equal(t, []string{"s3.bucket-lookup=dns"}, dns.Opts)
}

func TestBackupArgs(t *testing.T) {
	args := strings.Join(backupArgs(agentprotocol.VolumeBackupPayload{
		BackupID: "b1", ServiceID: svcID, Trigger: "schedule", Excludes: []string{"*.tmp", " "},
	}), " ")
	assert.Contains(t, args, "backup /vol --host nebulactrl-11111111 --tag nc-svc="+svcID+",nc-backup=b1,nc-trigger=schedule")
	for _, flag := range []string{"--ignore-ctime", "--ignore-inode", "--exclude-caches", "--no-scan", "--json", "--exclude *.tmp", "--exclude /vol/*/lost+found"} {
		assert.Contains(t, args, flag)
	}
}

func TestParseLsSkipsHeaderAndTheDirectoryItself(t *testing.T) {
	out := `{"message_type":"snapshot","id":"x"}
{"name":"sub","type":"dir","path":"/vol/data/sub","mode":2147484141,"message_type":"node"}
{"name":"b.txt","type":"file","path":"/vol/data/sub/b.txt","size":5,"mode":420,"mtime":"2026-10-27T07:15:23Z","message_type":"node"}
`
	nodes := parseLs([]byte(out), "/vol/data/sub")
	require.Len(t, nodes, 1)
	assert.Equal(t, "b.txt", nodes[0].Name)
	e := entryOf(nodes[0])
	assert.Equal(t, agentprotocol.FSTypeFile, e.Type)
	assert.Equal(t, "0644", e.Mode)
	assert.EqualValues(t, 5, e.Size)
}

func TestListFilesSortsDirectoriesFirst(t *testing.T) {
	r, d, _, _ := setup()
	d.lsOut = `{"message_type":"snapshot"}
{"name":"data","type":"dir","path":"/vol/data","message_type":"node"}
{"name":"b.txt","type":"file","path":"/vol/data/b.txt","size":5,"mode":420,"message_type":"node"}
{"name":"a.txt","type":"file","path":"/vol/data/a.txt","size":6,"mode":420,"message_type":"node"}
{"name":"sub","type":"dir","path":"/vol/data/sub","message_type":"node"}
`
	entries, truncated, err := r.ListFiles(context.Background(), agentprotocol.VolFileRequest{
		RequestID: "req-1", Op: agentprotocol.VolFileList, ServiceID: svcID, Repo: s3Repo1(), SnapshotID: strings.Repeat("a", 64),
		Volume: "data", Path: "/", Secrets: secrets(),
	}, nil)
	require.NoError(t, err)
	assert.False(t, truncated)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name)
	}
	assert.Equal(t, []string{"sub", "a.txt", "b.txt"}, names)
	assert.True(t, d.has("remove nebulactrl-bak-lreq-1"), "the helper is removed after the listing")
}

func TestOpenFileStreamsAFileAndAnArchive(t *testing.T) {
	r, d, _, _ := setup()
	d.lsOut = `{"message_type":"snapshot"}
{"name":"a.txt","type":"file","path":"/vol/data/a.txt","size":6,"mode":420,"message_type":"node"}
`
	d.dumpOut = "hello\n"
	req := agentprotocol.VolFileRequest{
		RequestID: "req-2", Op: agentprotocol.VolFileRead, ServiceID: svcID, Repo: s3Repo1(), SnapshotID: strings.Repeat("a", 64),
		Volume: "data", Path: "/a.txt", Secrets: secrets(),
	}
	entry, rc, err := r.OpenFile(context.Background(), req, nil)
	require.NoError(t, err)
	body, err := io.ReadAll(rc)
	require.NoError(t, err)
	require.NoError(t, rc.Close())
	assert.Equal(t, "hello\n", string(body))
	assert.Equal(t, "a.txt", entry.Name)
	assert.EqualValues(t, 6, entry.Size)
	assert.True(t, d.has("remove nebulactrl-bak-lreq-2"))

	// A directory is only readable as an archive.
	d.lsOut = `{"message_type":"snapshot"}
{"name":"data","type":"dir","path":"/vol/data","message_type":"node"}
`
	req.RequestID, req.Path = "req-3", "/"
	_, _, err = r.OpenFile(context.Background(), req, nil)
	var fe *FileError
	require.ErrorAs(t, err, &fe)
	assert.Equal(t, agentprotocol.FSErrIsDir, fe.Code)
	req.RequestID, req.Op = "req-4", agentprotocol.VolFileArchive
	entry, rc, err = r.OpenFile(context.Background(), req, nil)
	require.NoError(t, err)
	_, _ = io.ReadAll(rc)
	_ = rc.Close()
	assert.Equal(t, "data.zip", entry.Name)
	assert.EqualValues(t, -1, entry.Size)
	assert.True(t, d.has("restic -o s3.region=eu-1 -o s3.bucket-lookup=path dump --archive zip"))
}

func TestFileRequestsRejectUnsafeInput(t *testing.T) {
	r, _, _, _ := setup()
	base := agentprotocol.VolFileRequest{
		RequestID: "req-9", Op: agentprotocol.VolFileRead, ServiceID: svcID, Repo: s3Repo1(), SnapshotID: strings.Repeat("a", 64), Volume: "data", Path: "/a",
	}
	for name, mut := range map[string]func(*agentprotocol.VolFileRequest){
		"dotdot":       func(q *agentprotocol.VolFileRequest) { q.Path = "/a/../../b" },
		"bad snapshot": func(q *agentprotocol.VolFileRequest) { q.SnapshotID = "-o evil" },
		"bad volume":   func(q *agentprotocol.VolFileRequest) { q.Volume = "../x" },
	} {
		q := base
		mut(&q)
		_, _, err := r.OpenFile(context.Background(), q, nil)
		require.Error(t, err, name)
		_, _, err = r.ListFiles(context.Background(), q, nil)
		require.Error(t, err, name)
	}
}

func TestSummaryCollectorAndForgetVictims(t *testing.T) {
	c := &summaryCollector{}
	_, _ = c.Write([]byte(`{"message_type":"status","percent_done":0.5}
{"message_type":"error","error":{"message":"boom"},"item":"/vol/x"}
` + summaryLine("ff")))
	c.flush()
	require.True(t, c.found)
	assert.Equal(t, "ff", c.s.SnapshotID)
	assert.Equal(t, []string{"/vol/x: boom"}, c.errs)

	v, err := forgetVictims([]byte(`[{"remove":[{"id":"aaaa1111"},{"id":"bbbb2222"}]},{"remove":null}]`), []string{"bbbb"})
	require.NoError(t, err)
	assert.Equal(t, []string{"aaaa1111"}, v)
}

// The built-in backup storage is offered by the agent on 127.0.0.1 (localproxy).
// A helper on the default bridge cannot reach the host's loopback, so a
// loopback endpoint runs the helper on the host network; any other S3 stays on
// the default bridge.
func TestHelperNetworkForS3Endpoints(t *testing.T) {
	for _, tc := range []struct {
		endpoint string
		want     string
	}{
		{"http://127.0.0.1:18333", "host"},
		{"http://127.0.0.1:18333/", "host"},
		{"http://localhost:18333", "host"},
		{"http://[::1]:18333", "host"},
		{"http://127.1.2.3:9000", "host"},
		{"https://s3.example.com", ""},
		{"http://10.0.0.5:9000", ""},
		{"http://127.0.0.1.example.com:9000", ""}, // a name, not the loopback address
		{"", ""}, // AWS
	} {
		t.Run(tc.endpoint, func(t *testing.T) {
			r, d, _, _ := setup()
			p := payload()
			p.Repo.Destination.Endpoint = tc.endpoint
			_, err := r.Backup(context.Background(), p, secrets(), nil)
			require.NoError(t, err)
			var helper docker.ContainerSpec
			for _, c := range d.created {
				helper = c
			}
			assert.Equal(t, tc.want, helper.NetworkMode)
		})
	}
}
