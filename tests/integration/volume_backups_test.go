//go:build integration

package integration

import (
	"context"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// volumeSh runs a shell snippet in a throwaway alpine container with the
// Docker volumes mounted at /v/<name> (the fixture app image has no shell).
func volumeSh(t *testing.T, script string, mounts ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	args := []string{"run", "--rm"}
	for _, m := range mounts {
		args = append(args, "-v", m)
	}
	args = append(args, "alpine:3", "sh", "-c", script)
	out, err := sh(ctx, "", "docker", args...)
	require.NoError(t, err, "docker %v: %s", args, out)
	return strings.TrimSpace(out)
}

func (s *Stack) waitVolumeBackup(id string, timeout time.Duration) J {
	s.t.Helper()
	var b J
	ok := eventually(timeout, 500*time.Millisecond, func() bool {
		r := s.Do("GET", "/volume-backups/"+id, nil)
		if r.Status != http.StatusOK {
			return false
		}
		b = r.JSON()
		switch b.Str("status") {
		case "succeeded", "failed", "skipped", "expired":
			return true
		}
		return false
	})
	require.True(s.t, ok, "volume backup %s did not finish within %s: %v", id, timeout, b)
	return b
}

func (s *Stack) waitVolumeRestore(serviceID, id string, timeout time.Duration) J {
	s.t.Helper()
	var found J
	ok := eventually(timeout, time.Second, func() bool {
		r := s.Do("GET", "/services/"+serviceID+"/volume-restores", nil)
		if r.Status != http.StatusOK {
			return false
		}
		for _, it := range r.JSON().Items() {
			if it.Str("id") == id && (it.Str("status") == "succeeded" || it.Str("status") == "failed") {
				found = it
				return true
			}
		}
		return false
	})
	require.True(s.t, ok, "volume restore %s did not finish within %s", id, timeout)
	return found
}

// TestVolumeBackupFreezeRestoreAndDownload covers volume backups end to end on
// a real container with a real volume: a freeze-mode backup to a local
// destination (restic repository on the host), the file browser and a
// single-file download of the snapshot, a restore into a NEW volume whose swap
// is staged and applied by a deploy (the old volume is kept and listed as
// retired), and the cleanup of everything the backup created.
func TestVolumeBackupFreezeRestoreAndDownload(t *testing.T) {
	requireTier(t, tierFull)
	s := ExclusiveStack(t)
	ctx := context.Background()
	app := appFixture(t)

	var volumeShort string
	t.Cleanup(func() {
		cctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		// The local repository was written by root in a helper: remove it the
		// same way so the test's temp dir can be deleted.
		_, _ = sh(cctx, "", "docker", "run", "--rm", "-v", filepath.Join(s.StateDir, "backups")+":/d", "alpine:3", "sh", "-c", "rm -rf /d/* /d/.[!.]*")
		if volumeShort != "" {
			out, _ := sh(cctx, "", "docker", "volume", "ls", "-q", "--filter", "name=nc-vol-"+volumeShort)
			for _, v := range strings.Fields(out) {
				_, _ = sh(cctx, "", "docker", "volume", "rm", "-f", v)
			}
		}
		out, _ := sh(cctx, "", "docker", "volume", "ls", "-q", "--filter", "name=nc-bak-")
		for _, v := range strings.Fields(out) {
			_, _ = sh(cctx, "", "docker", "volume", "rm", "-f", v)
		}
		_, _ = sh(cctx, "", "docker", "volume", "rm", "-f", agentprotocol.ResticCacheVolume)
		// The pinned restic image stays: the harness pre-pulls it.
	})

	svc := s.NewService(SvcSpec{Name: "files", Image: app.AppImage, Domain: s.Host(), Vars: map[string]string{"VERSION": "v1"}})
	volumeShort = svc.ID[:8]
	r := s.Do("POST", "/services/"+svc.ID+"/volumes", map[string]any{"name": "data", "mountPath": "/data"})
	require.Equal(t, http.StatusCreated, r.Status, string(r.Raw))
	s.DeployOK(svc.ID)
	px := NewProxyClient(s.Host())
	px.WaitBody(t, "/", "v1", 30*time.Second)

	oldVol := agentprotocol.VolumeName(svc.ID, "data")
	volumeSh(t, "mkdir -p /v/sub && echo 'original content' > /v/note.txt && echo deep > /v/sub/deep.txt", oldVol+":/v")

	// ---- configure + back up (freeze mode, local destination) -------------
	r = s.Do("POST", "/backup-destinations", map[string]any{"name": "host disk", "kind": "local", "localDir": "vb"})
	require.Equal(t, http.StatusCreated, r.Status, string(r.Raw))
	destID := r.JSON().Str("id")

	r = s.Do("GET", "/services/"+svc.ID+"/volume-backup", nil)
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	require.True(t, r.JSON().Bool("eligible"), string(r.Raw))
	require.False(t, r.JSON().Bool("configured"))

	r = s.Do("PUT", "/services/"+svc.ID+"/volume-backup", map[string]any{"destinationId": destID, "mode": "freeze", "freezeBudgetSeconds": 20})
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	require.Equal(t, "freeze", r.JSON().Str("mode"))

	r = s.Do("POST", "/services/"+svc.ID+"/volume-backups", map[string]any{})
	require.Equal(t, http.StatusAccepted, r.Status, string(r.Raw))
	b := s.waitVolumeBackup(r.JSON().Str("id"), 4*time.Minute)
	require.Equal(t, "succeeded", b.Str("status"), "volume backup: %v", b)
	require.Equal(t, "freeze", b.Str("consistency"))
	require.NotEmpty(t, b.Str("snapshotId"))
	require.Contains(t, jsonStrings(b, "volumes"), "data")
	require.Positive(t, b.Int("sizeBytes"))
	require.True(t, b.Bool("restorable"))
	// The service served throughout and is running again, not paused.
	px.WaitBody(t, "/", "v1", 15*time.Second)
	for _, c := range s.RunningContainers(svc.ID) {
		require.NotEqual(t, "paused", c.State, "the service must never stay frozen")
	}
	// The repository exists on the host; nothing but it is left behind. restic
	// writes it as the helper's user (root, 0700 directories, 0400 config), and
	// only the agent (root) and its helpers ever read it: the control plane
	// browses and downloads through the agent. On a Linux CI runner the test's
	// own user cannot even stat it, so check it the way the product reads it.
	repoDir := "/d/vb/volumes/" + svc.ID
	out, err := sh(ctx, "", "docker", "run", "--rm", "-v", filepath.Join(s.StateDir, "backups")+":/d:ro", "alpine:3", "sh", "-c", "test -f "+repoDir+"/config && test -d "+repoDir+"/data && echo present")
	require.NoError(t, err, "the restic repository must exist in the local destination")
	require.Equal(t, "present", out)
	// Retention (restic forget) runs in a helper right after the backup: wait for it.
	require.True(t, eventually(90*time.Second, time.Second, func() bool {
		out, _ := sh(ctx, "", "docker", "ps", "-a", "-q", "--filter", "label="+agentprotocol.LabelHelper+"="+agentprotocol.HelperBackup)
		return strings.TrimSpace(out) == ""
	}), "no backup helper container may be left")
	out, _ = sh(ctx, "", "docker", "volume", "ls", "-q", "--filter", "name=nc-bak-")
	require.Empty(t, strings.TrimSpace(out), "the staging volume must be removed")

	// ---- change the file after the backup --------------------------------
	volumeSh(t, "echo 'changed after the backup' > /v/note.txt", oldVol+":/v")

	// ---- file browser + single-file download -----------------------------
	r = s.Do("GET", "/volume-backups/"+b.Str("id")+"/files?volume=data&path=/", nil)
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	var names []string
	for _, e := range jsonObjects(r.JSON(), "entries") {
		names = append(names, e.Str("name"))
	}
	require.Contains(t, names, "note.txt")
	require.Contains(t, names, "sub")

	code, body := s.raw("GET", "/volume-backups/"+b.Str("id")+"/files/content?volume=data&path=/note.txt", nil)
	require.Equal(t, http.StatusOK, code, string(body))
	require.Equal(t, "original content\n", string(body), "the download is the file as of the backup, not the changed one")
	code, body = s.raw("GET", "/volume-backups/"+b.Str("id")+"/files/content?volume=data&path=/sub/deep.txt", nil)
	require.Equal(t, http.StatusOK, code, string(body))
	require.Equal(t, "deep\n", string(body))

	// ---- restore into a NEW volume; the swap is staged -------------------
	r = s.Do("POST", "/volume-backups/"+b.Str("id")+"/restore", map[string]any{"volume": "data", "mode": "new_volume"})
	require.Equal(t, http.StatusAccepted, r.Status, string(r.Raw))
	rs := s.waitVolumeRestore(svc.ID, r.JSON().Str("id"), 3*time.Minute)
	require.Equal(t, "succeeded", rs.Str("status"), "restore: %v", rs)
	newVol := rs.Str("dockerName")
	require.NotEmpty(t, newVol)
	require.NotEqual(t, oldVol, newVol)

	require.Equal(t, "changed after the backup", volumeSh(t, "cat /v/note.txt", oldVol+":/v"), "the old volume is untouched")
	require.Equal(t, "original content", volumeSh(t, "cat /v/note.txt", newVol+":/v"), "the new volume holds the backup")
	r = s.Do("GET", "/services/"+svc.ID+"/pending-changes", nil)
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	require.Contains(t, string(r.Raw), "volumes.data", "the swap is a staged change")
	// The running container still uses the old volume until the deploy.
	require.Equal(t, "changed after the backup", volumeSh(t, "cat /v/note.txt", oldVol+":/v"))

	// ---- deploy applies the swap -----------------------------------------
	s.DeployOK(svc.ID)
	px.WaitBody(t, "/", "v1", 30*time.Second)
	cs := s.RunningContainers(svc.ID)
	require.NotEmpty(t, cs)
	var mounted []string
	for _, m := range cs[0].Mounts {
		mounted = append(mounted, m.Name)
	}
	require.Contains(t, mounted, newVol, "the deploy mounts the restored volume")
	require.NotContains(t, mounted, oldVol)

	r = s.Do("GET", "/services/"+svc.ID+"/retired-volumes", nil)
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	var retired []string
	for _, it := range r.JSON().Items() {
		retired = append(retired, it.Str("dockerName"))
	}
	require.Equal(t, []string{oldVol}, retired, "the replaced volume is kept and listed")

	// The old content is back inside the service's volume.
	require.Equal(t, "original content", volumeSh(t, "cat /v/note.txt", newVol+":/v"))
}

func jsonStrings(j J, k string) []string {
	arr, _ := j[k].([]any)
	var out []string
	for _, v := range arr {
		if str, ok := v.(string); ok {
			out = append(out, str)
		}
	}
	return out
}

func jsonObjects(j J, k string) []J {
	arr, _ := j[k].([]any)
	var out []J
	for _, v := range arr {
		m, _ := v.(map[string]any)
		out = append(out, J(m))
	}
	return out
}
