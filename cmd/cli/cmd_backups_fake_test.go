package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const (
	bkID      = "cccccccc-3333-4333-8333-cccccccccccc"
	bkDestID  = "dddddddd-4444-4444-8444-dddddddddddd"
	bkDest2ID = "eeeeeeee-5555-4555-8555-eeeeeeeeeeee"
	bkRestID  = "ffffffff-6666-4666-8666-ffffffffffff"
	bkSchedID = "99999999-7777-4777-8777-999999999999"

	bkSvcBackups  = cfgAPI + "/services/" + fakeSvcID + "/backups"
	bkDestinaties = cfgAPI + "/backup-destinations"
	bkOne         = cfgAPI + "/backups/" + bkID
)

// bkLine returns the whitespace-split cells of the stdout line holding marker.
func bkLine(t *testing.T, stdout, marker string) []string {
	t.Helper()
	for _, l := range strings.Split(stdout, "\n") {
		if strings.Contains(l, marker) {
			return strings.Fields(l)
		}
	}
	t.Fatalf("no line with %q in:\n%s", marker, stdout)
	return nil
}

func bkDestinations() map[string]any {
	return map[string]any{"items": []any{
		map[string]any{"id": bkDestID, "name": "Offsite", "kind": "s3", "bucket": "nebula-backups", "offSite": true, "lastTestOk": true},
		map[string]any{"id": bkDest2ID, "name": "Disk", "kind": "local", "localDir": "pg", "offSite": false, "lastTestOk": false},
		map[string]any{"id": "11111111-0000-4000-8000-000000000000", "name": "Builtin", "kind": "builtin", "offSite": false},
	}}
}

func bkView(status string, extra map[string]any) map[string]any {
	m := map[string]any{
		"id": bkID, "serviceId": fakeSvcID, "destinationId": bkDestID, "status": status, "trigger": "manual",
		"createdAt": "2026-01-02T03:04:05Z", "sizeBytes": 2048, "locked": false, "engine": "postgres",
	}
	for k, v := range extra {
		m[k] = v
	}
	return m
}

func TestBackupsLsPrintsTheRowsAndJSONForAnEmptyListIsAnArray(t *testing.T) {
	f := newCfgFake(t)
	f.reply("GET", bkSvcBackups, 200, map[string]any{"items": []any{
		bkView("succeeded", map[string]any{"destinationName": "Offsite", "locked": true}),
		bkView("failed", map[string]any{"id": "bad00000-0000-4000-8000-000000000000", "trigger": "schedule", "error": "disk full"}),
	}})

	r := f.run("backups", "ls", fakeSvcID)
	require.Equal(t, 0, r.code, r.all())
	for _, h := range []string{"CREATED", "STATUS", "TRIGGER", "DESTINATION", "BYTES", "LOCKED", "ID", "ERROR"} {
		require.Contains(t, r.stdout, h)
	}
	ok := bkLine(t, r.stdout, bkID)
	require.Equal(t, []string{"2026-01-02T03:04:05Z", "succeeded", "manual", "Offsite", "2048", "yes", bkID}, ok)
	bad := bkLine(t, r.stdout, "bad00000")
	require.Equal(t, []string{"2026-01-02T03:04:05Z", "failed", "schedule", "-", "2048", "no", "bad00000-0000-4000-8000-000000000000", "disk", "full"}, bad,
		"a missing destination name prints a dash; the error closes the row")

	// Without --json an empty list prints just the header; with --json it is [] (never null).
	f.reply("GET", bkSvcBackups, 200, map[string]any{})
	r = f.run("backups", "ls", fakeSvcID)
	require.Equal(t, 0, r.code, r.all())
	require.Len(t, strings.Split(strings.TrimSpace(r.stdout), "\n"), 1)
	r = f.run("--json", "backups", "ls", fakeSvcID)
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, []any{}, decode[[]any](t, r.stdout))
}

func TestBackupsLsResolvesProjectEnvSlugAndSurfacesNotFound(t *testing.T) {
	f := newCfgFake(t)
	f.reply("GET", cfgAPI+"/environments/"+cfgProdID+"/services", 200, map[string]any{"items": []any{
		map[string]any{"id": fakeSvcID, "slug": "pg", "name": "pg"},
	}})
	f.reply("GET", bkSvcBackups, 200, map[string]any{"items": []any{}})

	r := f.run("backups", "ls", "acme-shop/production/PG")
	require.Equal(t, 0, r.code, r.all())
	require.Len(t, f.calls("GET", bkSvcBackups), 1)

	r = f.run("backups", "ls", "acme-shop/production/nope")
	require.Equal(t, 1, r.code)
	require.Contains(t, r.err, `service "nope" not found in acme-shop/production`)

	r = f.run("backups", "ls", "not-a-ref")
	require.Equal(t, 1, r.code)
	require.Contains(t, r.err, "use a service ID or project/env/service-slug")

	f.problem("GET", bkSvcBackups, 403, "forbidden", "token cannot read backups")
	r = f.run("backups", "ls", fakeSvcID)
	require.Equal(t, 1, r.code)
	require.Contains(t, r.err, "token cannot read backups (forbidden, HTTP 403)")
}

func TestBackupsCreateValidatesBeforeAnyRequest(t *testing.T) {
	f := newCfgFake(t)
	before := f.total()
	r := f.run("backups", "create", fakeSvcID)
	require.Equal(t, 2, r.code, r.all())
	require.Contains(t, r.err, "--destination is required")
	require.Equal(t, before, f.total(), "a usage error never reaches the API")

	f.reply("GET", bkDestinaties, 200, bkDestinations())
	r = f.run("backups", "create", fakeSvcID, "--destination", "nowhere")
	require.Equal(t, 1, r.code)
	require.Contains(t, r.err, `backup destination "nowhere" not found`)
	require.Empty(t, f.calls("POST", bkSvcBackups))
}

func TestBackupsCreateQueuesWithTheDestinationIDAndIdempotencyKey(t *testing.T) {
	f := newCfgFake(t)
	f.reply("GET", bkDestinaties, 200, bkDestinations())
	f.reply("POST", bkSvcBackups, 202, bkView("queued", nil))

	r := f.run("--idempotency-key", "key-1", "backups", "create", fakeSvcID, "--destination", "offsite")
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, "Backup "+bkID+" queued\n", r.stdout, "destination names match case-insensitively")
	post := f.only("POST", bkSvcBackups)
	require.Equal(t, map[string]any{"destinationId": bkDestID}, post.json(t))
	require.Equal(t, "key-1", post.header.Get("Idempotency-Key"))
	require.Equal(t, "Bearer nct_test", post.header.Get("Authorization"))

	// A destination can also be addressed by id; --json prints the accepted backup.
	r = f.run("--json", "backups", "create", fakeSvcID, "--destination", bkDest2ID)
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, "queued", decode[map[string]any](t, r.stdout)["status"])
	require.Equal(t, bkDest2ID, f.calls("POST", bkSvcBackups)[1].json(t)["destinationId"])
	require.NotEmpty(t, f.calls("POST", bkSvcBackups)[1].header.Get("Idempotency-Key"), "a random key is sent when none is given")

	f.problem("POST", bkSvcBackups, 409, "backup_replica_unsupported", "replicas cannot be backed up")
	r = f.run("backups", "create", fakeSvcID, "--destination", "Disk")
	require.Equal(t, 1, r.code)
	require.Contains(t, r.err, "replicas cannot be backed up (backup_replica_unsupported, HTTP 409)")
	require.Contains(t, r.err, "backups run on the primary", "the hint points at the replica commands")
}

func TestBackupsCreateWaitFollowsTheBackupToItsEnd(t *testing.T) {
	f := newCfgFake(t)
	f.reply("GET", bkDestinaties, 200, bkDestinations())
	f.reply("POST", bkSvcBackups, 202, bkView("queued", nil))
	f.replySeq(bkOne, bkView("queued", nil), bkView("running", nil), bkView("running", nil), bkView("succeeded", nil))

	r := f.run("backups", "create", fakeSvcID, "--destination", "Offsite", "--wait")
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, "backup "+bkID+": queued\nbackup "+bkID+": running\nbackup "+bkID+": succeeded\n", r.stdout,
		"each status is announced once")
	require.Len(t, f.calls("GET", bkOne), 4)
}

func TestBackupsCreateWaitExitsNonZeroUnlessSucceeded(t *testing.T) {
	for _, status := range []string{"failed", "skipped", "expired"} {
		t.Run(status, func(t *testing.T) {
			f := newCfgFake(t)
			f.reply("GET", bkDestinaties, 200, bkDestinations())
			f.reply("POST", bkSvcBackups, 202, bkView("queued", nil))
			f.reply("GET", bkOne, 200, bkView(status, map[string]any{"error": "no space left"}))

			r := f.run("--json", "backups", "create", fakeSvcID, "--destination", "Offsite", "--wait")
			require.Equal(t, 1, r.code, r.all())
			require.Contains(t, r.err, "backup "+bkID+" "+status+": no space left")
			require.Equal(t, status, decode[map[string]any](t, r.stdout)["status"], "--json still prints the final backup")
		})
	}
}

func TestBackupsCreateWaitTimesOutAndSurfacesPollErrors(t *testing.T) {
	f := newCfgFake(t)
	f.reply("GET", bkDestinaties, 200, bkDestinations())
	f.reply("POST", bkSvcBackups, 202, bkView("queued", nil))
	f.reply("GET", bkOne, 200, bkView("running", nil))

	r := f.run("backups", "create", fakeSvcID, "--destination", "Offsite", "--wait", "--timeout", "30ms")
	require.Equal(t, 1, r.code, r.all())
	require.Contains(t, r.err, "timed out after 30ms waiting for backup "+bkID+" (last status running); it may still be running")

	f.problem("GET", bkOne, 404, "not_found", "backup gone")
	r = f.run("backups", "create", fakeSvcID, "--destination", "Offsite", "--wait")
	require.Equal(t, 1, r.code, r.all())
	require.Contains(t, r.err, "backup gone (not_found, HTTP 404)")
}

func TestBackupsRestoreIntoANewDatabase(t *testing.T) {
	f := newCfgFake(t)
	path := cfgAPI + "/backups/" + bkID + "/restore"
	f.reply("POST", path, 202, map[string]any{"id": bkRestID, "mode": "new", "status": "queued", "backupId": bkID})

	r := f.run("backups", "restore", bkID)
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, "Restore "+bkRestID+" started\n", r.stdout)
	post := f.only("POST", path)
	require.Equal(t, map[string]any{"mode": "new"}, post.json(t), "no name or server is sent unless asked for")
	require.NotEmpty(t, post.header.Get("Idempotency-Key"))

	r = f.run("--json", "backups", "restore", bkID, "--name", "pg-copy", "--server", "srv-1")
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, bkRestID, decode[map[string]any](t, r.stdout)["id"])
	require.Equal(t, map[string]any{"mode": "new", "name": "pg-copy", "serverId": "srv-1"}, f.calls("POST", path)[1].json(t))
}

func TestBackupsRestoreInPlaceNeedsTheSlugConfirmation(t *testing.T) {
	f := newCfgFake(t)
	path := cfgAPI + "/backups/" + bkID + "/restore"
	before := f.total()
	r := f.run("backups", "restore", bkID, "--in-place")
	require.Equal(t, 2, r.code, r.all())
	require.Contains(t, r.err, "--confirm <database slug> is required with --in-place")
	require.Equal(t, before, f.total())

	f.reply("POST", path, 202, map[string]any{"id": bkRestID, "mode": "in_place", "status": "queued"})
	r = f.run("backups", "restore", bkID, "--in-place", "--confirm", "pg", "--name", "ignored")
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, map[string]any{"mode": "in_place", "confirmSlug": "pg"}, f.only("POST", path).json(t),
		"the new-database options are not sent for an in-place restore")

	f.problem("POST", path, 422, "confirm_mismatch", "confirmation does not match the database slug")
	r = f.run("backups", "restore", bkID, "--in-place", "--confirm", "other")
	require.Equal(t, 1, r.code)
	require.Contains(t, r.err, "confirm_mismatch")
}

func TestBackupsRestoreWaitReportsTheNewDatabaseOrTheFailure(t *testing.T) {
	f := newCfgFake(t)
	path := cfgAPI + "/backups/" + bkID + "/restore"
	one := cfgAPI + "/backup-restores/" + bkRestID
	f.reply("POST", path, 202, map[string]any{"id": bkRestID, "mode": "new", "status": "queued"})
	f.replySeq(one,
		map[string]any{"id": bkRestID, "mode": "new", "status": "restoring"},
		map[string]any{"id": bkRestID, "mode": "new", "status": "succeeded", "targetServiceId": fakeSvc2ID})

	r := f.run("backups", "restore", bkID, "--wait")
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, "restore "+bkRestID+": restoring\nrestore "+bkRestID+": succeeded\nrestored into the new database "+fakeSvc2ID+"\n", r.stdout)

	// In place there is no new database to announce.
	f.reply("GET", one, 200, map[string]any{"id": bkRestID, "mode": "in_place", "status": "succeeded", "targetServiceId": fakeSvc2ID})
	r = f.run("backups", "restore", bkID, "--in-place", "--confirm", "pg", "--wait")
	require.Equal(t, 0, r.code, r.all())
	require.NotContains(t, r.stdout, "restored into")

	f.reply("GET", one, 200, map[string]any{"id": bkRestID, "mode": "new", "status": "failed", "error": "checksum mismatch"})
	r = f.run("--json", "backups", "restore", bkID, "--wait")
	require.Equal(t, 1, r.code, r.all())
	require.Contains(t, r.err, "restore "+bkRestID+" failed: checksum mismatch")
	require.Equal(t, "failed", decode[map[string]any](t, r.stdout)["status"])

	f.reply("GET", one, 200, map[string]any{"id": bkRestID, "mode": "new", "status": "restoring"})
	r = f.run("backups", "restore", bkID, "--wait", "--timeout", "30ms")
	require.Equal(t, 1, r.code, r.all())
	require.Contains(t, r.err, "timed out after 30ms waiting for restore "+bkRestID+" (last status restoring)")

	f.problem("GET", one, 500, "internal", "boom")
	r = f.run("backups", "restore", bkID, "--wait")
	require.Equal(t, 1, r.code, r.all())
	require.Contains(t, r.err, "boom (internal, HTTP 500)")
}

func TestBackupsRmAndLockUnlock(t *testing.T) {
	f := newCfgFake(t)
	f.reply("DELETE", bkOne, 204, "")
	r := f.run("backups", "rm", bkID)
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, "Deleting backup "+bkID+" (the agent removes the stored object)\n", r.stdout)

	f.problem("DELETE", bkOne, 409, "backup_locked", "unlock the backup first")
	r = f.run("backups", "rm", bkID)
	require.Equal(t, 1, r.code)
	require.Contains(t, r.err, "unlock the backup first (backup_locked, HTTP 409)")

	lock := bkOne + "/lock"
	f.handle("PUT", lock, func(req cfgReq) cfgReply {
		return jsonReply(200, bkView("succeeded", map[string]any{"locked": req.json(t)["locked"]}))
	})
	r = f.run("backups", "lock", bkID)
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, "Backup "+bkID+" locked=true\n", r.stdout)
	require.Equal(t, map[string]any{"locked": true}, f.calls("PUT", lock)[0].json(t))

	r = f.run("backups", "unlock", bkID)
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, "Backup "+bkID+" locked=false\n", r.stdout)
	require.Equal(t, map[string]any{"locked": false}, f.calls("PUT", lock)[1].json(t))

	r = f.run("--json", "backups", "lock", bkID)
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, true, decode[map[string]any](t, r.stdout)["locked"])

	require.Equal(t, 2, f.run("backups", "lock").code, "the backup id is required")
}

func TestBackupDestinationsLsShowsWhereBackupsLandAndTheLastTest(t *testing.T) {
	f := newCfgFake(t)
	f.reply("GET", bkDestinaties, 200, bkDestinations())

	r := f.run("backups", "destinations", "ls")
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, []string{"Offsite", "s3", "nebula-backups", "yes", "ok", bkDestID}, bkLine(t, r.stdout, "Offsite"))
	require.Equal(t, []string{"Disk", "local", "agent", "backup", "directory/pg", "no", "failed", bkDest2ID}, bkLine(t, r.stdout, "Disk"))
	require.Equal(t, []string{"Builtin", "builtin", "built-in", "storage", "no", "untested", "11111111-0000-4000-8000-000000000000"}, bkLine(t, r.stdout, "Builtin"))

	r = f.run("--json", "backups", "destinations", "ls")
	require.Equal(t, 0, r.code, r.all())
	require.Len(t, decode[[]map[string]any](t, r.stdout), 3)

	require.Equal(t, 2, f.run("backups", "destinations", "ls", "extra").code, "ls takes no arguments")
}

func TestBackupDestinationsAddS3ReadsTheSecretFromStdinFileOrEnvNeverArgv(t *testing.T) {
	f := newCfgFake(t)
	f.reply("POST", bkDestinaties, 201, map[string]any{"id": bkDestID, "name": "Offsite", "kind": "s3"})

	// Standard input.
	r := f.runStdin("  s3cr3t-from-stdin \n", "backups", "destinations", "add", "--name", "Offsite", "--kind", "s3",
		"--bucket", "b", "--access-key", "AKIA", "--secret-key-file", "-", "--endpoint", "https://minio", "--region", "eu", "--path-style", "--prefix", "nebula/")
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, "Added destination Offsite ("+bkDestID+")\n", r.stdout)
	require.NotContains(t, r.all(), "s3cr3t")
	post := f.calls("POST", bkDestinaties)[0]
	require.Equal(t, map[string]any{
		"name": "Offsite", "kind": "s3", "bucket": "b", "accessKey": "AKIA", "secretKey": "s3cr3t-from-stdin",
		"pathStyle": true, "endpoint": "https://minio", "region": "eu", "prefix": "nebula/",
	}, post.json(t), "the secret is trimmed")

	// A file.
	file := filepath.Join(t.TempDir(), "secret")
	require.NoError(t, os.WriteFile(file, []byte("from-file\n"), 0o600))
	r = f.run("--json", "backups", "destinations", "add", "--name", "Offsite", "--kind", "s3", "--bucket", "b", "--access-key", "AKIA", "--secret-key-file", file)
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, bkDestID, decode[map[string]any](t, r.stdout)["id"])
	body := f.calls("POST", bkDestinaties)[1].json(t)
	require.Equal(t, "from-file", body["secretKey"])
	require.Equal(t, false, body["pathStyle"])
	require.NotContains(t, body, "endpoint")
	require.NotContains(t, body, "region")
	require.NotContains(t, body, "prefix")

	// The environment variable is the fallback; the file wins over it.
	t.Setenv("NEBULACTRL_BACKUP_SECRET_KEY", "from-env")
	r = f.run("backups", "destinations", "add", "--name", "Offsite", "--kind", "s3", "--bucket", "b", "--access-key", "AKIA")
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, "from-env", f.calls("POST", bkDestinaties)[2].json(t)["secretKey"])
	r = f.run("backups", "destinations", "add", "--name", "Offsite", "--kind", "s3", "--bucket", "b", "--access-key", "AKIA", "--secret-key-file", file)
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, "from-file", f.calls("POST", bkDestinaties)[3].json(t)["secretKey"])
}

func TestBackupDestinationsAddRejectsIncompleteInputBeforeAnyRequest(t *testing.T) {
	f := newCfgFake(t)
	t.Setenv("NEBULACTRL_BACKUP_SECRET_KEY", "")
	before := f.total()

	r := f.run("backups", "destinations", "add", "--name", "x")
	require.Equal(t, 2, r.code, r.all())
	require.Contains(t, r.err, "--name and --kind are required")

	r = f.run("backups", "destinations", "add", "--name", "x", "--kind", "s3", "--bucket", "b", "--access-key", "AKIA")
	require.Equal(t, 2, r.code, r.all())
	require.Contains(t, r.err, "an s3 destination needs --bucket, --access-key and a secret key")

	r = f.runStdin("   \n", "backups", "destinations", "add", "--name", "x", "--kind", "s3", "--bucket", "b", "--access-key", "AKIA", "--secret-key-file", "-")
	require.Equal(t, 2, r.code, r.all())
	require.Contains(t, r.err, "needs --bucket")

	r = f.run("backups", "destinations", "add", "--name", "x", "--kind", "s3", "--bucket", "b", "--access-key", "AKIA", "--secret-key-file", filepath.Join(t.TempDir(), "missing"))
	require.Equal(t, 1, r.code, r.all())
	require.Contains(t, r.err, "read secret key:")
	require.Equal(t, before, f.total())
}

func TestBackupDestinationsAddLocalAndServerRejection(t *testing.T) {
	f := newCfgFake(t)
	f.reply("POST", bkDestinaties, 201, map[string]any{"id": bkDest2ID, "name": "Disk", "kind": "local"})

	r := f.run("backups", "destinations", "add", "--name", "Disk", "--kind", "local", "--local-dir", "pg", "--prefix", "x")
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, map[string]any{"name": "Disk", "kind": "local", "localDir": "pg", "prefix": "x"}, f.only("POST", bkDestinaties).json(t),
		"a local destination never carries S3 fields")

	f.problem("POST", bkDestinaties, 422, "validation_failed", "kind must be s3, local")
	r = f.run("backups", "destinations", "add", "--name", "Disk", "--kind", "ftp")
	require.Equal(t, 1, r.code)
	require.Contains(t, r.err, "kind must be s3, local (validation_failed, HTTP 422)")
}

func TestBackupDestinationsRmAndTest(t *testing.T) {
	f := newCfgFake(t)
	f.reply("GET", bkDestinaties, 200, bkDestinations())
	f.reply("DELETE", bkDestinaties+"/"+bkDestID, 204, "")

	r := f.run("backups", "destinations", "rm", "OFFSITE")
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, "Removed destination OFFSITE\n", r.stdout)
	require.Len(t, f.calls("DELETE", bkDestinaties+"/"+bkDestID), 1)

	r = f.run("backups", "destinations", "rm", "ghost")
	require.Equal(t, 1, r.code)
	require.Contains(t, r.err, `backup destination "ghost" not found`)

	f.problem("DELETE", bkDestinaties+"/"+bkDest2ID, 409, "destination_in_use", "2 schedules use this destination")
	r = f.run("backups", "destinations", "rm", "Disk")
	require.Equal(t, 1, r.code)
	require.Contains(t, r.err, "2 schedules use this destination (destination_in_use, HTTP 409)")

	// test
	testPath := bkDestinaties + "/" + bkDestID + "/test"
	before := f.total()
	r = f.run("backups", "destinations", "test", "Offsite")
	require.Equal(t, 2, r.code, r.all())
	require.Contains(t, r.err, "--server is required")
	require.Equal(t, before, f.total())

	f.reply("POST", testPath, 200, map[string]any{"id": bkDestID, "name": "Offsite", "lastTestOk": true, "lastTestMessage": "wrote and read 12 bytes"})
	r = f.run("backups", "destinations", "test", "Offsite", "--server", "srv-1")
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, "Destination Offsite works: wrote and read 12 bytes\n", r.stdout)
	require.Equal(t, map[string]any{"serverId": "srv-1"}, f.only("POST", testPath).json(t))

	f.reply("POST", testPath, 200, map[string]any{"id": bkDestID, "name": "Offsite", "lastTestOk": false, "lastTestMessage": "AccessDenied"})
	r = f.run("--json", "backups", "destinations", "test", "Offsite", "--server", "srv-1")
	require.Equal(t, 1, r.code, r.all())
	require.Contains(t, r.err, "destination test failed: AccessDenied")
	require.Equal(t, false, decode[map[string]any](t, r.stdout)["lastTestOk"], "--json still prints the destination")

	f.reply("POST", testPath, 200, map[string]any{"id": bkDestID, "name": "Offsite"})
	r = f.run("backups", "destinations", "test", "Offsite", "--server", "srv-1")
	require.Equal(t, 1, r.code, r.all())
	require.Contains(t, r.err, "no answer from the agent yet")
}

func TestBackupSchedulesLsAddRm(t *testing.T) {
	f := newCfgFake(t)
	sched := cfgAPI + "/services/" + fakeSvcID + "/backup-schedules"
	f.reply("GET", sched, 200, map[string]any{"items": []any{
		map[string]any{"id": bkSchedID, "cron": "0 3 * * *", "timezone": "Europe/Berlin", "destinationName": "Offsite",
			"retentionCount": 7, "retentionDays": 30, "enabled": true, "nextRunAt": "2027-06-15T12:00:00Z"},
		map[string]any{"id": "sched-2", "cron": "@hourly", "timezone": "UTC", "retentionCount": 0, "retentionDays": 0, "enabled": false},
	}})

	r := f.run("backups", "schedules", "ls", fakeSvcID)
	require.Equal(t, 0, r.code, r.all())
	row := bkLine(t, r.stdout, bkSchedID)
	require.Equal(t, []string{"0", "3", "*", "*", "*", "Europe/Berlin", "Offsite", "7", "/", "30d", "yes"}, row[:11])
	require.Contains(t, row[11], "2027-06-15", "the next run prints as a local timestamp")
	require.Equal(t, []string{"@hourly", "UTC", "-", "0", "/", "0d", "no", "-", "sched-2"}, bkLine(t, r.stdout, "sched-2"))

	f.reply("GET", sched, 200, map[string]any{})
	r = f.run("--json", "backups", "schedules", "ls", fakeSvcID)
	require.Equal(t, []any{}, decode[[]any](t, r.stdout))

	// add
	f.reply("GET", bkDestinaties, 200, bkDestinations())
	f.reply("POST", sched, 201, map[string]any{"id": bkSchedID, "cron": "0 3 * * *", "nextRunAt": "2027-06-15T12:00:00Z"})
	before := f.total()
	r = f.run("backups", "schedules", "add", fakeSvcID, "--destination", "Offsite")
	require.Equal(t, 2, r.code, r.all())
	require.Contains(t, r.err, "--destination and --cron are required")
	require.Equal(t, before, f.total())

	r = f.run("backups", "schedules", "add", fakeSvcID, "--destination", "Offsite", "--cron", "0 3 * * *", "--timezone", "Europe/Berlin", "--keep", "14", "--keep-days", "90")
	require.Equal(t, 0, r.code, r.all())
	require.Contains(t, r.stdout, `Scheduled "0 3 * * *" (Europe/Berlin); next run `)
	require.Equal(t, map[string]any{
		"destinationId": bkDestID, "cron": "0 3 * * *", "timezone": "Europe/Berlin", "retentionCount": 14.0, "retentionDays": 90.0,
	}, f.only("POST", sched).json(t))

	r = f.run("--json", "backups", "schedules", "add", fakeSvcID, "--destination", bkDest2ID, "--cron", "@daily")
	require.Equal(t, 0, r.code, r.all())
	body := f.calls("POST", sched)[1].json(t)
	require.Equal(t, "UTC", body["timezone"], "defaults: UTC, keep 7, no day limit")
	require.EqualValues(t, 7, body["retentionCount"])
	require.EqualValues(t, 0, body["retentionDays"])
	require.Equal(t, bkSchedID, decode[map[string]any](t, r.stdout)["id"])

	f.problem("POST", sched, 422, "invalid_cron", "cron expression is invalid")
	r = f.run("backups", "schedules", "add", fakeSvcID, "--destination", "Offsite", "--cron", "nope")
	require.Equal(t, 1, r.code)
	require.Contains(t, r.err, "cron expression is invalid (invalid_cron, HTTP 422)")

	// rm
	f.reply("DELETE", cfgAPI+"/backup-schedules/"+bkSchedID, 204, "")
	r = f.run("backups", "schedules", "rm", bkSchedID)
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, "Removed schedule "+bkSchedID+"\n", r.stdout)
	f.problem("DELETE", cfgAPI+"/backup-schedules/"+bkSchedID, 403, "forbidden", "not allowed")
	r = f.run("backups", "schedules", "rm", bkSchedID)
	require.Equal(t, 1, r.code)
	require.Contains(t, r.err, "not allowed (forbidden, HTTP 403)")
}
