package backups

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestHTTPDumpRestoreCreateValidationAndReads(t *testing.T) {
	f := newFixture(t)
	src := f.database("pg")
	base := "/services/" + src.ID.String() + "/dump-restores"

	require.Empty(t, items(t, f.call(http.MethodGet, base, nil)))
	require.Equal(t, http.StatusUnprocessableEntity, f.call(http.MethodPost, base, map[string]any{"mode": "swap"}).Code, "the schema lists the modes")
	require.Equal(t, http.StatusUnprocessableEntity, f.call(http.MethodPost, base, map[string]any{"sizeBytes": -1}).Code)
	rec := f.call(http.MethodPost, base, map[string]any{"sizeBytes": DefaultDumpMaxBytes + 1})
	require.Equal(t, http.StatusRequestEntityTooLarge, rec.Code, rec.Body.String())
	require.Equal(t, "file_too_large", errCode(t, rec))
	rec = f.call(http.MethodPost, base, map[string]any{"mode": "in_place"})
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code, "an in-place restore needs the typed slug: %s", rec.Body.String())
	require.Equal(t, http.StatusNotFound, f.call(http.MethodPost, "/services/"+uuid.NewString()+"/dump-restores", map[string]any{}).Code)
	require.Empty(t, items(t, f.call(http.MethodGet, base, nil)), "refusals created nothing")

	rec = f.call(http.MethodPost, base, map[string]any{"name": "pg-copy", "fileName": `C:\dumps\prod.pgdump`, "sizeBytes": 1234})
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	v := jsonBody(t, rec)
	require.Equal(t, "new", v["mode"])
	require.Equal(t, "provisioning", v["status"])
	require.Equal(t, "prod.pgdump", v["fileName"], "only the base name is kept")
	require.Equal(t, src.ID.String(), v["sourceServiceId"])
	require.NotNil(t, v["targetServiceId"])
	id := v["id"].(string)

	require.Equal(t, id, jsonBody(t, f.call(http.MethodGet, "/dump-restores/"+id, nil))["id"])
	require.Equal(t, http.StatusNotFound, f.call(http.MethodGet, "/dump-restores/"+uuid.NewString(), nil).Code)
	list := items(t, f.call(http.MethodGet, base, nil))
	require.Len(t, list, 1)
	require.NotContains(t, list[0], "log", "the list leaves the log out")

	ro := f.reader()
	require.Equal(t, http.StatusOK, f.serve(ro, httpRoute{method: http.MethodGet, path: "/dump-restores/" + id}).Code)
	require.Equal(t, http.StatusForbidden, f.serve(ro, httpRoute{method: http.MethodPost, path: base, body: map[string]any{}}).Code)
	require.Equal(t, http.StatusForbidden, f.serve(ro, httpRoute{method: http.MethodPost, path: "/dump-restores/" + id + "/cancel"}).Code)
	require.Equal(t, http.StatusForbidden, f.serve(ro, httpRoute{method: http.MethodPut, path: "/dump-restores/" + id + "/upload", raw: []byte("x"), rawLen: 1}).Code)
}

func TestHTTPDumpRestoreCancelBeforeTheFileArrives(t *testing.T) {
	f := newFixture(t)
	src := f.database("pg")
	rec := f.call(http.MethodPost, "/services/"+src.ID.String()+"/dump-restores", map[string]any{"name": "pg-copy"})
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	id := jsonBody(t, rec)["id"].(string)

	rec = f.call(http.MethodPost, "/dump-restores/"+id+"/cancel", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, "failed", jsonBody(t, rec)["status"])
	require.NotEmpty(t, jsonBody(t, rec)["error"])
	rec = f.call(http.MethodPost, "/dump-restores/"+id+"/cancel", nil)
	require.Equal(t, http.StatusConflict, rec.Code, "a finished restore cannot be canceled: %s", rec.Body.String())
	require.Equal(t, http.StatusNotFound, f.call(http.MethodPost, "/dump-restores/"+uuid.NewString()+"/cancel", nil).Code)
}

func TestHTTPDumpUploadStreamsTheRawBodyToTheAgentAndRefusesBadRequests(t *testing.T) {
	f := newFixture(t)
	ag := f.dumpAgent()
	aw, _ := f.awaitingDump(f.database("pg"))
	path := "/dump-restores/" + aw.ID.String() + "/upload"
	put := func(p string, body []byte, length int64) *httptest.ResponseRecorder {
		return f.serve(f.ctx, httpRoute{method: http.MethodPut, path: p, raw: body, rawLen: length})
	}

	// Requests that fail before a byte is read.
	require.Equal(t, http.StatusNotFound, put("/dump-restores/not-a-uuid/upload", pgDump, int64(len(pgDump))).Code)
	rec := put(path, pgDump, -1)
	require.Equal(t, http.StatusLengthRequired, rec.Code, rec.Body.String())
	require.Equal(t, "length_required", errCode(t, rec))
	require.Equal(t, "close", rec.Header().Get("Connection"), "an unread body must not be reused")
	f.svc.dumpMax = 1000
	rec = put(path, pgDump, int64(len(pgDump)))
	require.Equal(t, http.StatusRequestEntityTooLarge, rec.Code, rec.Body.String())
	require.Equal(t, "file_too_large", errCode(t, rec))
	f.svc.dumpMax = 0
	rec = put("/dump-restores/"+uuid.NewString()+"/upload", pgDump, int64(len(pgDump)))
	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
	_, eof := ag.received()
	require.False(t, eof, "nothing reached the agent")

	// A file that is not a database dump is refused before anything is sent.
	junk := bytes.Repeat([]byte{0x00, 0x01, 0x02, 0x03}, 512)
	rec = put(path, junk, int64(len(junk)))
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
	require.Equal(t, "unsupported_dump_format", errCode(t, rec))
	got, err := f.svc.GetDumpRestore(f.ctx, aw.ID)
	require.NoError(t, err)
	require.Equal(t, RestoreAwaitingUpload, got.Status, "the restore still awaits a good file")

	// The real file: 202 once the agent has it, then the restore finishes.
	rec = f.serve(f.ctx, httpRoute{method: http.MethodPut, path: path + "?fileName=%2Fhome%2Fme%2Fprod.pgdump", raw: pgDump, rawLen: int64(len(pgDump))})
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	require.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	started := jsonBody(t, rec)
	require.Equal(t, aw.ID.String(), started["id"])
	require.Contains(t, []any{"restoring", "succeeded"}, started["status"])
	done := f.waitDump(aw.ID, RestoreSucceeded)
	require.Equal(t, "prod.pgdump", done.FileName)
	received, eof := ag.received()
	require.True(t, eof)
	require.Equal(t, string(pgDump), received, "the agent received the body byte for byte")

	// A restore that already has its file takes no second one.
	rec = put(path, pgDump, int64(len(pgDump)))
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	require.Equal(t, "dump_restore_finished", errCode(t, rec))
}
