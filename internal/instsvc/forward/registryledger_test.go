package forward

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/instsvc"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

type fakeLedger struct {
	mu      sync.Mutex
	check   error
	checked []uuid.UUID
	pushes  []recordedPush
}

type recordedPush struct {
	team               uuid.UUID
	repository, digest string
	size               int64
}

func (l *fakeLedger) CheckRegistryWrite(_ context.Context, team uuid.UUID) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.checked = append(l.checked, team)
	return l.check
}

func (l *fakeLedger) RecordRegistryPush(_ context.Context, team uuid.UUID, repository, digest string, size int64) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.pushes = append(l.pushes, recordedPush{team, repository, digest, size})
	return nil
}

// A team at its registry quota (or a full disk) cannot push; it can
// still pull and delete, which is how it makes room.
func TestRegistryWritesStopAtTheQuotaAndTheDiskGuardButDeletesAndReadsPass(t *testing.T) {
	w := newWorld(t)
	l := &fakeLedger{check: instsvc.ErrQuotaExceeded}
	w.f.d.Ledger = l
	c := w.client(t, w.srvA, agentprotocol.TunnelRegistry)
	repo := w.teamA.String() + "/web"
	digest := "sha256:" + strings.Repeat("a", 64)

	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/v2/" + repo + "/blobs/uploads/"},
		{http.MethodPatch, "/v2/" + repo + "/blobs/uploads/0f1c2d3e-aaaa-bbbb-cccc-1234567890ab"},
		{http.MethodPut, "/v2/" + repo + "/manifests/latest"},
	} {
		resp := w.regDo(t, c, tc.method, tc.path, "x", nil)
		require.Equal(t, http.StatusForbidden, resp.StatusCode, "%s %s", tc.method, tc.path)
	}
	require.Empty(t, w.rg.got(), "nothing reached the registry")

	require.Equal(t, http.StatusOK, w.regDo(t, c, http.MethodDelete, "/v2/"+repo+"/manifests/"+digest, "", nil).StatusCode, "a delete makes room")
	require.Equal(t, http.StatusOK, w.regDo(t, c, http.MethodGet, "/v2/"+repo+"/manifests/latest", "", nil).StatusCode)
	require.Len(t, w.rg.got(), 2)

	l.check = instsvc.ErrDiskLow
	resp := w.regDo(t, c, http.MethodPost, "/v2/"+repo+"/blobs/uploads/", "", nil)
	require.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
	require.NotEmpty(t, resp.Header.Get("Retry-After"))

	l.check = nil
	require.Equal(t, http.StatusOK, w.regDo(t, c, http.MethodPost, "/v2/"+repo+"/blobs/uploads/", "", nil).StatusCode)
	for _, team := range l.checked {
		require.Equal(t, w.teamA, team, "the check is for the stream's team")
	}
}

// Every manifest the registry accepts is recorded, so hand-pushed images count
// against the quota and age out; a refused or failed push leaves no row.
func TestRegistryRecordsAcceptedManifestPushes(t *testing.T) {
	w := newWorld(t)
	l := &fakeLedger{}
	w.f.d.Ledger = l
	status := http.StatusCreated
	w.rg.h = func(rw http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			rw.Header().Set("Docker-Content-Digest", "sha256:"+strings.Repeat("c", 64))
			rw.WriteHeader(status)
		}
	}
	c := w.client(t, w.srvA, agentprotocol.TunnelRegistry)
	repo := w.teamA.String() + "/web"
	manifest := `{"schemaVersion":2,"config":{"size":10,"digest":"sha256:x"},"layers":[{"size":100},{"size":200}]}`

	require.Equal(t, http.StatusCreated, w.regDo(t, c, http.MethodPut, "/v2/"+repo+"/manifests/v1", manifest, nil).StatusCode)
	require.Equal(t, []recordedPush{{w.teamA, repo, "sha256:" + strings.Repeat("c", 64), 310}}, l.pushes)

	status = http.StatusInternalServerError
	w.regDo(t, c, http.MethodPut, "/v2/"+repo+"/manifests/v2", manifest, nil)
	status = http.StatusOK
	w.regDo(t, c, http.MethodPut, "/v2/"+repo+"/manifests/v3", manifest, nil)
	require.Len(t, l.pushes, 1, "only a 201 is a push")

	// A blob upload is not a manifest.
	status = http.StatusCreated
	w.regDo(t, c, http.MethodPut, "/v2/"+repo+"/blobs/uploads/0f1c2d3e-aaaa-bbbb-cccc-1234567890ab?digest=sha256:"+strings.Repeat("d", 64), "layer", nil)
	require.Len(t, l.pushes, 1)
	require.Equal(t, manifest, w.rg.got()[0].Body, "the manifest reaches the registry unchanged")
}

func TestManifestBytesSumsConfigLayersAndChildren(t *testing.T) {
	require.EqualValues(t, 310, manifestBytes([]byte(`{"config":{"size":10},"layers":[{"size":100},{"size":200}]}`)))
	require.EqualValues(t, 30, manifestBytes([]byte(`{"manifests":[{"size":10},{"size":20}]}`)), "an index counts its child manifests")
	require.EqualValues(t, 0, manifestBytes([]byte(`not json`)))
	require.EqualValues(t, 5, manifestBytes([]byte(`{"config":{"size":5},"layers":[{"size":-9}]}`)), "a negative size counts as nothing")
}
