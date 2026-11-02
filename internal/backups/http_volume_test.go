package backups

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

func TestHTTPVolumeBackupConfigEligibilityAndValidation(t *testing.T) {
	f := newFixture(t)
	dest := f.s3Destination()
	pg := f.database("pg")
	app := f.app("web", true)
	path := func(id uuid.UUID) string { return "/services/" + id.String() + "/volume-backup" }

	// Without a saved configuration the answer is a 200 with the defaults.
	rec := f.call(http.MethodGet, path(app.ID), nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	cfg := jsonBody(t, rec)
	require.Equal(t, false, cfg["configured"])
	require.Equal(t, true, cfg["eligible"])
	rec = f.call(http.MethodGet, path(pg.ID), nil)
	require.Equal(t, false, jsonBody(t, rec)["eligible"], "managed databases use database backups")
	require.Contains(t, jsonBody(t, rec)["ineligibleReason"], "database backups")
	require.Equal(t, http.StatusNotFound, f.call(http.MethodGet, path(uuid.New()), nil).Code)

	// Ineligible services, bad schedules and bad enums are all refused with their code.
	rec = f.call(http.MethodPut, path(pg.ID), map[string]any{"destinationId": dest.ID})
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
	require.Equal(t, "volume_backup_unavailable", errCode(t, rec))
	rec = f.call(http.MethodPut, path(app.ID), map[string]any{"destinationId": dest.ID, "cron": "bogus", "enabled": true})
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
	require.Equal(t, "invalid_cron", errCode(t, rec))
	require.Equal(t, http.StatusUnprocessableEntity, f.call(http.MethodPut, path(app.ID), map[string]any{"destinationId": dest.ID, "mode": "yolo"}).Code)
	require.Equal(t, http.StatusUnprocessableEntity, f.call(http.MethodPut, path(app.ID), map[string]any{"destinationId": dest.ID, "freezeBudgetSeconds": 61}).Code)
	require.Equal(t, false, jsonBody(t, f.call(http.MethodGet, path(app.ID), nil))["configured"], "refusals saved nothing")

	rec = f.call(http.MethodPut, path(app.ID), map[string]any{
		"destinationId": dest.ID, "mode": "stop", "cron": "@daily", "enabled": true, "keepDaily": 3, "excludes": []string{"cache/**"},
	})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	saved := jsonBody(t, rec)
	require.Equal(t, true, saved["configured"])
	require.Equal(t, "stop", saved["mode"])
	require.Equal(t, "@daily", saved["cron"])
	require.Equal(t, float64(3), saved["keepDaily"])
	require.Equal(t, []any{"cache/**"}, saved["excludes"])
	require.NotContains(t, rec.Body.String(), canarySecret)

	ro := f.reader()
	require.Equal(t, http.StatusOK, f.serve(ro, httpRoute{method: http.MethodGet, path: path(app.ID)}).Code)
	require.Equal(t, http.StatusForbidden, f.serve(ro, httpRoute{method: http.MethodPut, path: path(app.ID), body: map[string]any{"destinationId": dest.ID}}).Code)
	require.Equal(t, http.StatusForbidden, f.serve(ro, httpRoute{method: http.MethodDelete, path: path(app.ID)}).Code)

	require.Equal(t, http.StatusNoContent, f.call(http.MethodDelete, path(app.ID), nil).Code)
	require.Equal(t, false, jsonBody(t, f.call(http.MethodGet, path(app.ID), nil))["configured"])
}

func TestHTTPVolumeBackupFromQueueToRestoreAndRetiredVolumes(t *testing.T) {
	f := newFixture(t)
	dest := f.s3Destination()
	app := f.app("web", true)
	svcPath := "/services/" + app.ID.String()

	// A backup needs a saved configuration.
	rec := f.call(http.MethodPost, svcPath+"/volume-backups", map[string]any{})
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
	require.Equal(t, "volume_backup_unavailable", errCode(t, rec))
	f.volConfig(app, dest, nil)

	rec = f.call(http.MethodPost, svcPath+"/volume-backups", map[string]any{})
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	b := jsonBody(t, rec)
	require.Equal(t, "queued", b["status"])
	id := b["id"].(string)
	require.Equal(t, 1, f.countCmds(agentprotocol.KindVolumeBackup))

	require.Len(t, items(t, f.call(http.MethodGet, svcPath+"/volume-backups?limit=5", nil)), 1)
	require.Equal(t, id, jsonBody(t, f.call(http.MethodGet, "/volume-backups/"+id, nil))["id"])
	require.Equal(t, http.StatusNotFound, f.call(http.MethodGet, "/volume-backups/"+uuid.NewString(), nil).Code)

	// Not finished: not restorable; lock toggles; a locked one is not deleted.
	rec = f.call(http.MethodPost, "/volume-backups/"+id+"/restore", map[string]any{"volume": "data"})
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	require.Equal(t, "backup_not_restorable", errCode(t, rec))
	require.Equal(t, true, jsonBody(t, f.call(http.MethodPut, "/volume-backups/"+id+"/lock", map[string]any{"locked": true}))["locked"])
	rec = f.call(http.MethodDelete, "/volume-backups/"+id, nil)
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	require.Equal(t, "backup_locked", errCode(t, rec))
	require.Equal(t, false, jsonBody(t, f.call(http.MethodPut, "/volume-backups/"+id+"/lock", map[string]any{"locked": false}))["locked"])

	// The agent finished: the backup is restorable.
	bid := uuid.MustParse(id)
	f.finishBackup(VolumeBackupView{ID: bid}, snap('a'))
	got := jsonBody(t, f.call(http.MethodGet, "/volume-backups/"+id, nil))
	require.Equal(t, "succeeded", got["status"])

	// Restores: the volume must exist; in-place needs the typed slug; new volume stages a swap.
	require.Equal(t, http.StatusUnprocessableEntity, f.call(http.MethodPost, "/volume-backups/"+id+"/restore", map[string]any{"volume": "", "mode": "new_volume"}).Code)
	require.Equal(t, http.StatusUnprocessableEntity, f.call(http.MethodPost, "/volume-backups/"+id+"/restore", map[string]any{"volume": "data", "mode": "swap"}).Code)
	rec = f.call(http.MethodPost, "/volume-backups/"+id+"/restore", map[string]any{"volume": "data", "mode": "in_place", "confirmSlug": "nope"})
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
	require.Equal(t, "confirmation_required", errCode(t, rec))
	rec = f.call(http.MethodPost, "/volume-backups/"+id+"/restore", map[string]any{"volume": "data"})
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	restore := jsonBody(t, rec)
	require.Equal(t, "new_volume", restore["mode"], "the default restore keeps the current volume")
	require.Equal(t, "data", restore["volumeName"])
	require.Equal(t, b["id"], restore["backupId"])
	require.Equal(t, http.StatusNotFound, f.call(http.MethodPost, "/volume-backups/"+uuid.NewString()+"/restore", map[string]any{"volume": "data"}).Code)
	restores := items(t, f.call(http.MethodGet, svcPath+"/volume-restores", nil))
	require.Len(t, restores, 1)
	require.Equal(t, restore["id"], restores[0].(map[string]any)["id"])

	// Retired volumes list and delete through the agent.
	require.Empty(t, items(t, f.call(http.MethodGet, svcPath+"/retired-volumes", nil)))
	require.NoError(t, f.q.InsertRetiredVolume(context.Background(), db.InsertRetiredVolumeParams{ServiceID: app.ID, VolumeName: "data", DockerName: "nc-vol-old-data", Reason: "test"}))
	retired := items(t, f.call(http.MethodGet, svcPath+"/retired-volumes", nil))
	require.Len(t, retired, 1)
	rid := retired[0].(map[string]any)["id"].(string)
	require.Equal(t, http.StatusAccepted, f.call(http.MethodDelete, "/retired-volumes/"+rid, nil).Code)
	require.Equal(t, 1, f.countCmds(agentprotocol.KindVolumeRemove))
	require.Equal(t, http.StatusNotFound, f.call(http.MethodDelete, "/retired-volumes/"+uuid.NewString(), nil).Code)

	// While a restore of the service runs its backups stay; afterwards deleting goes through the agent.
	rec = f.call(http.MethodDelete, "/volume-backups/"+id, nil)
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	require.Equal(t, "backup_in_progress", errCode(t, rec))
	_, err := f.pool.Exec(context.Background(), "UPDATE volume_restores SET status = 'failed' WHERE id = $1", restore["id"])
	require.NoError(t, err)
	forgets := f.countCmds(agentprotocol.KindVolumeForget)
	require.Equal(t, http.StatusNoContent, f.call(http.MethodDelete, "/volume-backups/"+id, nil).Code)
	require.Equal(t, forgets+1, f.countCmds(agentprotocol.KindVolumeForget))
	ro := f.reader()
	require.Equal(t, http.StatusOK, f.serve(ro, httpRoute{method: http.MethodGet, path: svcPath + "/volume-backups"}).Code)
	require.Equal(t, http.StatusOK, f.serve(ro, httpRoute{method: http.MethodGet, path: svcPath + "/retired-volumes"}).Code)
	for _, rt := range []httpRoute{
		{method: http.MethodPost, path: svcPath + "/volume-backups", body: map[string]any{}},
		{method: http.MethodPut, path: "/volume-backups/" + id + "/lock", body: map[string]any{"locked": true}},
		{method: http.MethodDelete, path: "/volume-backups/" + id},
		{method: http.MethodPost, path: "/volume-backups/" + id + "/restore", body: map[string]any{"volume": "data"}},
		{method: http.MethodDelete, path: "/retired-volumes/" + rid},
		{method: http.MethodGet, path: "/volume-backups/" + id + "/files?volume=data"},
		{method: http.MethodGet, path: "/volume-backups/" + id + "/files/content?volume=data&path=/a"},
	} {
		require.Equal(t, http.StatusForbidden, f.serve(ro, rt).Code, rt.method+" "+rt.path)
	}
}

func TestHTTPVolumeBackupFilesListAndDownload(t *testing.T) {
	f := newFixture(t)
	_, b := f.succeededVolumeBackup()
	fake := &volFakeStreams{}
	f.svc.streams = fake
	fake.hook = answers(func(fs *volFakeStream, req agentprotocol.VolFileRequest) {
		switch req.Op {
		case agentprotocol.VolFileList:
			fs.push(agentprotocol.TypeFSResponse, agentprotocol.FSResponse{
				RequestID: req.RequestID, Stage: agentprotocol.FSDone, Path: req.Path,
				Entries: []agentprotocol.FSEntry{{Name: "a.txt", Type: agentprotocol.FSTypeFile, Size: 12}},
			})
		default:
			fs.push(agentprotocol.TypeFSResponse, agentprotocol.FSResponse{
				RequestID: req.RequestID, Stage: agentprotocol.FSReady, Path: req.Path, Entry: &agentprotocol.FSEntry{Name: "a.txt", Type: agentprotocol.FSTypeFile, Size: 5},
			})
			fs.push(agentprotocol.TypeFSData, agentprotocol.FSData{RequestID: req.RequestID, Data: []byte("hello"), EOF: true})
		}
	})
	base := "/volume-backups/" + b.ID.String()

	rec := f.call(http.MethodGet, base+"/files?volume=data", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	listing := jsonBody(t, rec)
	require.Equal(t, "/", listing["path"], "the path defaults to the volume root")
	require.Len(t, listing["entries"], 1)
	require.Equal(t, http.StatusUnprocessableEntity, f.call(http.MethodGet, base+"/files", nil).Code, "the volume is required")
	rec = f.call(http.MethodGet, base+"/files?volume=data&path=/a/../b", nil)
	require.Equal(t, "invalid_path", errCode(t, rec))

	rec = f.call(http.MethodGet, base+"/files/content?volume=data&path=/a.txt", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, "hello", rec.Body.String())
	require.Contains(t, rec.Header().Get("Content-Disposition"), "attachment")
	require.Equal(t, 1, f.count("SELECT count(*) FROM audit_events WHERE action = 'volume_backup.file.download'"))

	// A path parameter that is no id, and a backup that does not exist, are plain 404s in the API's error format.
	rec = f.call(http.MethodGet, "/volume-backups/not-a-uuid/files/content?volume=data&path=/a.txt", nil)
	require.Equal(t, http.StatusNotFound, rec.Code)
	require.Equal(t, "not_found", errCode(t, rec))
	rec = f.call(http.MethodGet, "/volume-backups/"+uuid.NewString()+"/files/content?volume=data&path=/a.txt", nil)
	require.Equal(t, http.StatusNotFound, rec.Code)
	rec = f.call(http.MethodGet, base+"/files/content?volume=other&path=/a.txt", nil)
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
}

func (f *fixture) count(query string, args ...any) int {
	f.t.Helper()
	var n int
	require.NoError(f.t, f.pool.QueryRow(context.Background(), query, args...).Scan(&n))
	return n
}

func TestHTTPRevealVolumeBackupRecoveryKeyNeedsAdminAndARepository(t *testing.T) {
	f := newFixture(t)
	dest := f.s3Destination()
	app := f.app("web", true)
	f.volConfig(app, dest, nil)
	path := "/services/" + app.ID.String() + "/volume-backup/recovery-key"

	// The repository exists only after the first backup ran.
	rec := f.call(http.MethodPost, path, map[string]any{})
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
	require.Equal(t, "volume_backup_unavailable", errCode(t, rec))

	b, err := f.svc.CreateVolumeBackup(f.ctx, app.ID)
	require.NoError(t, err)
	f.finishBackup(b, snap('a'))
	rec = f.call(http.MethodPost, path, map[string]any{})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	key := jsonBody(t, rec)
	require.NotEmpty(t, key["password"])
	require.Contains(t, key["repository"], "volumes/"+app.ID.String())
	require.Equal(t, 1, f.count("SELECT count(*) FROM audit_events WHERE action = 'volume_backup.recovery_key.reveal'"))

	// A person with every scope but admin cannot see it.
	p, _ := auth.PrincipalFrom(f.ctx)
	noAdmin := auth.WithPrincipal(context.Background(), &auth.Principal{
		Type: auth.TypeToken, TokenID: new(uuid.New()), UserID: p.UserID, WorkspaceID: f.ws.ID, Label: "dev",
		Scopes: []string{policy.ScopeProjectsRead, policy.ScopeProjectsWrite, policy.ScopeDeploymentsWrite},
	})
	rec = f.serve(noAdmin, httpRoute{method: http.MethodPost, path: path, body: map[string]any{}})
	require.Equal(t, http.StatusForbidden, rec.Code)
	require.NotContains(t, rec.Body.String(), key["password"])
	require.False(t, strings.Contains(rec.Body.String(), "repository"))
}
