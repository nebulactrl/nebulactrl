package backups

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// fakeS3 is a minimal in-memory, path-style S3 endpoint for one bucket: object
// PUT/GET/HEAD/DELETE, multipart upload and ListObjectsV2 (two keys per page).
type fakeS3 struct {
	mu      sync.Mutex
	objs    map[string][]byte
	uploads map[string]map[int][]byte
	path    map[string]string
	next    int
	auths   []string
}

func newFakeS3(t *testing.T) (*fakeS3, *httptest.Server) {
	t.Helper()
	f := &fakeS3{objs: map[string][]byte{}, uploads: map[string]map[int][]byte{}, path: map[string]string{}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return f, srv
}

func (f *fakeS3) keys() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for k := range f.objs {
		out = append(out, strings.TrimPrefix(k, "/nebula-backups/"))
	}
	sort.Strings(out)
	return out
}

func (f *fakeS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.auths = append(f.auths, r.Header.Get("Authorization"))
	p, q := r.URL.Path, r.URL.Query()
	body, _ := io.ReadAll(r.Body)
	switch {
	case r.Method == http.MethodGet && q.Get("list-type") == "2":
		prefix := q.Get("prefix")
		var keys []string
		for k := range f.objs {
			if key := strings.TrimPrefix(k, "/nebula-backups/"); strings.HasPrefix(key, prefix) {
				keys = append(keys, key)
			}
		}
		sort.Strings(keys)
		start := 0
		if tok := q.Get("continuation-token"); tok != "" {
			start = sort.SearchStrings(keys, tok)
		}
		end := min(start+2, len(keys))
		var b strings.Builder
		b.WriteString(`<?xml version="1.0"?><ListBucketResult><Name>nebula-backups</Name>`)
		if end < len(keys) {
			b.WriteString("<IsTruncated>true</IsTruncated><NextContinuationToken>" + keys[end] + "</NextContinuationToken>")
		} else {
			b.WriteString("<IsTruncated>false</IsTruncated>")
		}
		for _, k := range keys[start:end] {
			fmt.Fprintf(&b, "<Contents><Key>%s</Key><LastModified>2026-10-27T03:30:00.000Z</LastModified><Size>%d</Size></Contents>", k, len(f.objs["/nebula-backups/"+k]))
		}
		b.WriteString("</ListBucketResult>")
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(b.String()))
	case r.Method == http.MethodPost && q.Has("uploads"):
		f.next++
		id := "up" + strconv.Itoa(f.next)
		f.uploads[id], f.path[id] = map[int][]byte{}, p
		fmt.Fprintf(w, `<?xml version="1.0"?><InitiateMultipartUploadResult><UploadId>%s</UploadId></InitiateMultipartUploadResult>`, id)
	case r.Method == http.MethodPut && q.Has("uploadId"):
		n, _ := strconv.Atoi(q.Get("partNumber"))
		f.uploads[q.Get("uploadId")][n] = body
		w.Header().Set("ETag", fmt.Sprintf(`"part-%d"`, n))
	case r.Method == http.MethodPost && q.Has("uploadId"):
		id := q.Get("uploadId")
		var nums []int
		for n := range f.uploads[id] {
			nums = append(nums, n)
		}
		sort.Ints(nums)
		var all []byte
		for _, n := range nums {
			all = append(all, f.uploads[id][n]...)
		}
		f.objs[f.path[id]] = all
		fmt.Fprint(w, `<?xml version="1.0"?><CompleteMultipartUploadResult><ETag>"done"</ETag></CompleteMultipartUploadResult>`)
	case r.Method == http.MethodDelete && q.Has("uploadId"):
		delete(f.uploads, q.Get("uploadId"))
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodPut:
		f.objs[p] = body
		w.Header().Set("ETag", `"single"`)
	case r.Method == http.MethodGet || r.Method == http.MethodHead:
		b, ok := f.objs[p]
		if !ok {
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(http.StatusNotFound)
			if r.Method == http.MethodGet {
				fmt.Fprint(w, `<?xml version="1.0"?><Error><Code>NoSuchKey</Code><Message>nope</Message></Error>`)
			}
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(b)))
		if r.Method == http.MethodGet {
			_, _ = w.Write(b)
		}
	case r.Method == http.MethodDelete:
		delete(f.objs, p)
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "unsupported", http.StatusNotImplemented)
	}
}

// The whole S3 path: configure a bucket, test it, back up, find the backup by
// listing the bucket the way `restore --from s3://…` does, verify the dump and
// let retention delete the oldest.
func TestInstanceBackupToS3(t *testing.T) {
	fake, srv := newFakeS3(t)
	e := newInstEnv(t, (&fakeTools{}).tools())
	_, err := e.svc.Update(e.ctx, InstanceBackupPatch{
		Enabled: new(true), Kind: new("s3"), Endpoint: new(srv.URL), Region: new("us-east-1"), Bucket: new("nebula-backups"),
		Prefix: new("prod"), PathStyle: new(true), AccessKey: new("AKIATESTKEY"), SecretKey: new(canaryS3Secret), RetentionCount: new(2),
	})
	require.NoError(t, err)

	v, err := e.svc.Test(e.ctx)
	require.NoError(t, err)
	require.NotNil(t, v.Destination.LastTestOK)
	require.True(t, *v.Destination.LastTestOK, v.Destination.LastTestMessage)
	require.Empty(t, fake.keys(), "the probe object is deleted again")

	var runs []string
	for range 3 {
		row := e.runOnce(t)
		require.Equal(t, "succeeded", row.Status, row.Error)
		require.Contains(t, row.ObjectKey, "prod/nebulactrl-instance/")
		runs = append(runs, row.ID.String())
		time.Sleep(5 * time.Millisecond)
	}

	// Retention kept the newest two (each with its manifest) in the bucket.
	keys := fake.keys()
	require.Len(t, keys, 4, "two dumps and two manifests: %v", keys)
	for _, k := range keys {
		require.True(t, strings.HasPrefix(k, "prod/nebulactrl-instance/"), k)
	}

	// The restore side reads the bucket with only the command-line inputs.
	src := RestoreSource{Endpoint: srv.URL, Region: "us-east-1", Bucket: "nebula-backups", Prefix: "prod", PathStyle: true, AccessKey: "AKIATESTKEY", SecretKey: canaryS3Secret}
	st, err := src.Store()
	require.NoError(t, err)
	found, err := FindBackups(context.Background(), st, src.Prefix)
	require.NoError(t, err)
	require.Len(t, found, 2)
	require.Equal(t, runs[2], found[0].Manifest.BackupID, "newest first")
	require.Equal(t, runs[1], found[1].Manifest.BackupID)
	require.Equal(t, KeyFingerprint(bytes.Repeat([]byte{3}, 32)), found[0].Manifest.KeyFingerprint)

	// A different prefix sees nothing.
	none, err := FindBackups(context.Background(), st, "staging")
	require.NoError(t, err)
	require.Empty(t, none)

	// The dump in the bucket verifies against its manifest (size and SHA-256).
	tmp, err := os.CreateTemp(t.TempDir(), "dl")
	require.NoError(t, err)
	defer tmp.Close()
	require.NoError(t, downloadVerified(context.Background(), st, found[0].Manifest, tmp))
	_, err = tmp.Seek(0, io.SeekStart)
	require.NoError(t, err)
	got, err := io.ReadAll(tmp)
	require.NoError(t, err)
	require.Equal(t, fakeDump, got)

	// Requests were signed with the stored credentials.
	fake.mu.Lock()
	defer fake.mu.Unlock()
	require.NotEmpty(t, fake.auths)
	for _, a := range fake.auths[:5] {
		require.Contains(t, a, "AKIATESTKEY")
	}
}
