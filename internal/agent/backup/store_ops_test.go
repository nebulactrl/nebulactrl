package backup

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

func newLocal(t *testing.T) (*LocalStore, string) {
	t.Helper()
	root := t.TempDir()
	st, err := NewLocalStore(root, "dest")
	require.NoError(t, err)
	return st, filepath.Join(root, "dest")
}

func TestLocalStoreGetAndSize(t *testing.T) {
	st, dir := newLocal(t)
	ctx := context.Background()
	require.NoError(t, st.Put(ctx, "a/b/obj.dump", strings.NewReader("hello")))

	rc, err := st.Get(ctx, "a/b/obj.dump")
	require.NoError(t, err)
	b, err := io.ReadAll(rc)
	require.NoError(t, err)
	require.NoError(t, rc.Close())
	require.Equal(t, "hello", string(b))

	n, err := st.Size(ctx, "a/b/obj.dump")
	require.NoError(t, err)
	require.Equal(t, int64(5), n)

	_, err = st.Get(ctx, "a/b/missing")
	require.ErrorIs(t, err, ErrNotFound)
	_, err = st.Size(ctx, "a/b/missing")
	require.ErrorIs(t, err, ErrNotFound)

	for _, bad := range []string{"../escape", "/abs", "a/../../b", ""} {
		_, err = st.Get(ctx, bad)
		require.Error(t, err, bad)
		require.NotErrorIs(t, err, ErrNotFound, bad)
		_, err = st.Size(ctx, bad)
		require.Error(t, err, bad)
	}

	info, err := os.Stat(filepath.Join(dir, "a", "b", "obj.dump"))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "dumps are private to the agent user")
}

func TestLocalStoreGetAndSizeReportOtherFilesystemErrors(t *testing.T) {
	st, dir := newLocal(t)
	// "file" is a regular file, so "file/x" fails with ENOTDIR, not "not exist".
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "file"), []byte("x"), 0o600))
	_, err := st.Get(context.Background(), "file/x")
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrNotFound)
	_, err = st.Size(context.Background(), "file/x")
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrNotFound)
}

func TestLocalStoreDeletePrunesEmptyDirectoriesUpToRoot(t *testing.T) {
	st, dir := newLocal(t)
	ctx := context.Background()
	require.NoError(t, st.Put(ctx, "a/b/c/one", strings.NewReader("1")))
	require.NoError(t, st.Put(ctx, "a/keep", strings.NewReader("k")))

	require.NoError(t, st.Delete(ctx, "a/b/c/one"))
	_, err := os.Stat(filepath.Join(dir, "a", "b"))
	require.ErrorIs(t, err, os.ErrNotExist, "emptied parents are removed")
	_, err = os.Stat(filepath.Join(dir, "a", "keep"))
	require.NoError(t, err, "a non-empty parent stays")

	require.NoError(t, st.Delete(ctx, "a/keep"))
	_, err = os.Stat(filepath.Join(dir, "a"))
	require.ErrorIs(t, err, os.ErrNotExist)
	_, err = os.Stat(dir)
	require.NoError(t, err, "the store root itself is never removed")

	require.NoError(t, st.Delete(ctx, "a/b/c/one"), "deleting a missing object is not an error")
}

func TestLocalStoreDeleteRejectsBadKeysAndNonEmptyDirectories(t *testing.T) {
	st, dir := newLocal(t)
	ctx := context.Background()
	require.Error(t, st.Delete(ctx, "../x"))

	require.NoError(t, st.Put(ctx, "d/inner", strings.NewReader("x")))
	err := st.Delete(ctx, "d")
	require.Error(t, err, "a key that names a non-empty directory is not an object")
	_, statErr := os.Stat(filepath.Join(dir, "d", "inner"))
	require.NoError(t, statErr)
}

func TestLocalStorePutFailures(t *testing.T) {
	st, dir := newLocal(t)
	ctx := context.Background()

	require.Error(t, st.Put(ctx, "../x", strings.NewReader("x")))

	// the parent "dir" is a file: the directory cannot be created
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "blocked"), nil, 0o600))
	err := st.Put(ctx, "blocked/obj", strings.NewReader("x"))
	require.ErrorContains(t, err, "create backup directory")

	// a failing source leaves neither the object nor a temp file
	boom := errors.New("source broke")
	err = st.Put(ctx, "sub/obj", io.MultiReader(strings.NewReader("part"), &failingReader{boom}))
	require.ErrorIs(t, err, boom)
	entries, readErr := os.ReadDir(filepath.Join(dir, "sub"))
	require.NoError(t, readErr)
	require.Empty(t, entries)

	// the destination of the rename is a directory: Put fails and removes its temp file
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "sub", "target", "child"), 0o700))
	err = st.Put(ctx, "sub/target", strings.NewReader("x"))
	require.Error(t, err)
	entries, readErr = os.ReadDir(filepath.Join(dir, "sub"))
	require.NoError(t, readErr)
	require.Len(t, entries, 1, "only the directory that was in the way is left")
	require.Equal(t, "target", entries[0].Name())
}

func TestLocalStoreListSkipsPartialFilesAndHonoursPrefix(t *testing.T) {
	st, dir := newLocal(t)
	ctx := context.Background()
	require.NoError(t, st.Put(ctx, "p/1.dump", strings.NewReader("1")))
	require.NoError(t, st.Put(ctx, "q/2.dump", strings.NewReader("22")))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "p", ".partial-123"), []byte("junk"), 0o600))

	all, err := st.List(ctx, "")
	require.NoError(t, err)
	require.Len(t, all, 2)

	got, err := st.List(ctx, "q/")
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, "q/2.dump", got[0].Key)
	require.Equal(t, int64(2), got[0].Size)
	require.False(t, got[0].ModTime.IsZero())
}

func TestLocalStoreListOfMissingRootIsEmpty(t *testing.T) {
	st, err := NewLocalStore(t.TempDir(), "never-written")
	require.NoError(t, err)
	got, err := st.List(context.Background(), "")
	require.NoError(t, err)
	require.Empty(t, got)
}

func TestLocalStoreListHonoursCancellation(t *testing.T) {
	st, _ := newLocal(t)
	require.NoError(t, st.Put(context.Background(), "p/1.dump", strings.NewReader("1")))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := st.List(ctx, "")
	require.ErrorIs(t, err, context.Canceled)
	require.ErrorContains(t, err, "list ")
}

func TestLocalStoreListReportsUnreadableDirectory(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("file permissions do not apply to root")
	}
	st, dir := newLocal(t)
	require.NoError(t, st.Put(context.Background(), "locked/1.dump", strings.NewReader("1")))
	require.NoError(t, os.Chmod(filepath.Join(dir, "locked"), 0))
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(dir, "locked"), 0o700) })
	_, err := st.List(context.Background(), "")
	require.Error(t, err)
	require.ErrorContains(t, err, "list "+dir)
}

// denyingS3 answers every request with a non-retryable 403 AccessDenied.
func denyingS3(t *testing.T) *S3Store {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusForbidden)
		if r.Method != http.MethodHead {
			_, _ = io.WriteString(w, `<?xml version="1.0"?><Error><Code>AccessDenied</Code><Message>no</Message></Error>`)
		}
	}))
	t.Cleanup(srv.Close)
	return newS3Store(t, srv.URL)
}

func TestS3StoreReportsNonNotFoundErrorsWithOperationContext(t *testing.T) {
	st := denyingS3(t)
	ctx := context.Background()

	err := st.Put(ctx, "p/k", strings.NewReader("x"))
	require.ErrorContains(t, err, "upload p/k: ")

	_, err = st.Get(ctx, "p/k")
	require.ErrorContains(t, err, "get p/k: ")
	require.NotErrorIs(t, err, ErrNotFound, "an access error is not a missing object")

	_, err = st.Size(ctx, "p/k")
	require.ErrorContains(t, err, "head p/k: ")
	require.NotErrorIs(t, err, ErrNotFound)

	err = st.Delete(ctx, "p/k")
	require.ErrorContains(t, err, "delete p/k: ")

	_, err = st.List(ctx, "p/")
	require.ErrorContains(t, err, "list bucket/p: ")
}

func TestS3StoreRejectsUnsafeKeys(t *testing.T) {
	_, srv := newFakeS3()
	defer srv.Close()
	st := newS3Store(t, srv.URL)
	ctx := context.Background()
	for _, key := range []string{"../x", "/abs", ""} {
		require.Error(t, st.Put(ctx, key, strings.NewReader("x")), key)
		_, err := st.Get(ctx, key)
		require.Error(t, err, key)
		_, err = st.Size(ctx, key)
		require.Error(t, err, key)
		require.Error(t, st.Delete(ctx, key), key)
	}
}

func TestNewS3StoreDefaultsRegionAndHonoursOptions(t *testing.T) {
	st, err := NewS3Store(agentprotocol.BackupDestination{Bucket: "b"}, "ak", "sk")
	require.NoError(t, err)
	opts := st.client.Options()
	require.Equal(t, "us-east-1", opts.Region, "an empty region falls back to us-east-1")
	require.False(t, opts.UsePathStyle)
	require.Nil(t, opts.BaseEndpoint)

	st, err = NewS3Store(agentprotocol.BackupDestination{Bucket: "b", Region: "eu-west-1", Endpoint: "https://s3.example.test", PathStyle: true}, "ak", "sk")
	require.NoError(t, err)
	opts = st.client.Options()
	require.Equal(t, "eu-west-1", opts.Region)
	require.True(t, opts.UsePathStyle)
	require.NotNil(t, opts.BaseEndpoint)
	require.Equal(t, "https://s3.example.test", *opts.BaseEndpoint)
	require.Equal(t, "b", st.bucket)

	_, err = NewS3Store(agentprotocol.BackupDestination{}, "ak", "sk")
	require.EqualError(t, err, "s3 destination has no bucket")
	_, err = NewS3Store(agentprotocol.BackupDestination{Bucket: "b"}, "ak", "")
	require.EqualError(t, err, "s3 credentials missing")
}
