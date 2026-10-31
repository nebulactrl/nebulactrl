package backup

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// fakeS3 is a minimal in-memory, path-style S3 endpoint: object PUT/GET/HEAD/
// DELETE and multipart upload. It records authorization headers so tests can
// check the SDK signed requests.
type fakeS3 struct {
	mu        sync.Mutex
	objs      map[string][]byte
	uploads   map[string]map[int][]byte // uploadID -> part -> data
	uploadOf  map[string]string         // uploadID -> object path
	aborted   int
	started   int
	completed int
	auths     []string
	nextID    int
}

func newFakeS3() (*fakeS3, *httptest.Server) {
	f := &fakeS3{objs: map[string][]byte{}, uploads: map[string]map[int][]byte{}, uploadOf: map[string]string{}}
	return f, httptest.NewServer(f)
}

func (f *fakeS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.auths = append(f.auths, r.Header.Get("Authorization"))
	path := r.URL.Path
	q := r.URL.Query()
	body, _ := io.ReadAll(r.Body)
	switch {
	case r.Method == http.MethodPost && q.Has("uploads"):
		f.nextID++
		id := "up" + strconv.Itoa(f.nextID)
		f.uploads[id] = map[int][]byte{}
		f.uploadOf[id] = path
		f.started++
		fmt.Fprintf(w, `<?xml version="1.0"?><InitiateMultipartUploadResult><Bucket>b</Bucket><Key>k</Key><UploadId>%s</UploadId></InitiateMultipartUploadResult>`, id)
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
		f.objs[f.uploadOf[id]] = all
		delete(f.uploads, id)
		f.completed++
		_ = xml.NewEncoder(w)
		fmt.Fprint(w, `<?xml version="1.0"?><CompleteMultipartUploadResult><Location>x</Location><Bucket>b</Bucket><Key>k</Key><ETag>"done"</ETag></CompleteMultipartUploadResult>`)
	case r.Method == http.MethodDelete && q.Has("uploadId"):
		delete(f.uploads, q.Get("uploadId"))
		f.aborted++
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodPut:
		f.objs[path] = body
		w.Header().Set("ETag", `"single"`)
	case r.Method == http.MethodGet || r.Method == http.MethodHead:
		b, ok := f.objs[path]
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
		delete(f.objs, path)
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "unsupported", http.StatusNotImplemented)
	}
}

func (f *fakeS3) object(key string) ([]byte, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, ok := f.objs["/bucket/"+key]
	return b, ok
}

func newS3Store(t *testing.T, srvURL string) *S3Store {
	t.Helper()
	st, err := NewS3Store(agentprotocol.BackupDestination{
		Kind: agentprotocol.DestS3, Endpoint: srvURL, Region: "us-east-1", Bucket: "bucket", PathStyle: true, CompatMode: true,
	}, "AKIATESTKEY", "test-secret-key")
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func TestS3StoreRoundTrip(t *testing.T) {
	f, srv := newFakeS3()
	defer srv.Close()
	st := newS3Store(t, srv.URL)
	ctx := context.Background()

	if err := st.Put(ctx, "p/small.txt", strings.NewReader("hello s3")); err != nil {
		t.Fatal(err)
	}
	if b, ok := f.object("p/small.txt"); !ok || string(b) != "hello s3" {
		t.Fatalf("stored %q %v", b, ok)
	}
	if n, err := st.Size(ctx, "p/small.txt"); err != nil || n != 8 {
		t.Fatalf("size %d %v", n, err)
	}
	rc, err := st.Get(ctx, "p/small.txt")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(rc)
	rc.Close()
	if string(got) != "hello s3" {
		t.Fatalf("got %q", got)
	}
	if err := st.Delete(ctx, "p/small.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Size(ctx, "p/small.txt"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
	if _, err := st.Get(ctx, "p/small.txt"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
	if err := st.Delete(ctx, "p/small.txt"); err != nil {
		t.Fatalf("deleting a missing object must succeed: %v", err)
	}
	for _, a := range f.auths {
		if !strings.Contains(a, "AKIATESTKEY") || strings.Contains(a, "test-secret-key") {
			t.Fatalf("requests must be SigV4-signed without exposing the secret: %q", a)
		}
	}
}

func withSmallParts(t *testing.T) {
	t.Helper()
	old, oldC := s3PartSize, s3Concurrency
	s3PartSize, s3Concurrency = 5<<20, 1
	t.Cleanup(func() { s3PartSize, s3Concurrency = old, oldC })
}

func TestS3MultipartUploadStreams(t *testing.T) {
	withSmallParts(t)
	f, srv := newFakeS3()
	defer srv.Close()
	st := newS3Store(t, srv.URL)

	data := make([]byte, 12<<20+123)
	_, _ = rand.Read(data)
	if err := st.Put(context.Background(), "big.bin", bytes.NewReader(data)); err != nil {
		t.Fatal(err)
	}
	b, ok := f.object("big.bin")
	if !ok || !bytes.Equal(b, data) {
		t.Fatalf("multipart object differs (ok=%v len=%d want %d)", ok, len(b), len(data))
	}
	if f.started != 1 || f.completed != 1 || f.aborted != 0 {
		t.Fatalf("multipart bookkeeping: started=%d completed=%d aborted=%d", f.started, f.completed, f.aborted)
	}
}

// A reader that fails mid-stream (what a dump with a non-zero exit looks like)
// must abort the multipart upload and leave no object.
func TestS3FailedStreamAbortsAndLeavesNoObject(t *testing.T) {
	withSmallParts(t)
	f, srv := newFakeS3()
	defer srv.Close()
	st := newS3Store(t, srv.URL)

	err := st.Put(context.Background(), "big.bin", &errReader{n: 11 << 20})
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("want the reader error, got %v", err)
	}
	if _, ok := f.object("big.bin"); ok {
		t.Fatal("a failed stream must not leave an object")
	}
	if f.started != 1 || f.aborted != 1 || f.completed != 0 {
		t.Fatalf("started=%d aborted=%d completed=%d", f.started, f.aborted, f.completed)
	}
	if len(f.uploads) != 0 {
		t.Fatalf("dangling multipart uploads: %d", len(f.uploads))
	}

	// A small failing stream never even issues a PUT.
	if err := st.Put(context.Background(), "small.bin", &errReader{n: 10}); err == nil {
		t.Fatal("expected error")
	}
	if _, ok := f.object("small.bin"); ok {
		t.Fatal("small failed stream left an object")
	}
}

func TestS3BackupEndToEndWithFailingDump(t *testing.T) {
	withSmallParts(t)
	f, srv := newFakeS3()
	defer srv.Close()
	ex := &fakeExec{stdout: bytes.Repeat([]byte("row\n"), 3<<20), failMid: true, exit: 2, stderr: "pg_dump: lost connection"}
	r := &Runner{Docker: ex, Stores: NewStoreFactory(t.TempDir())}
	p := pgPayload()
	p.Destination = agentprotocol.BackupDestination{Kind: agentprotocol.DestS3, Endpoint: srv.URL, Region: "us-east-1", Bucket: "bucket", PathStyle: true, CompatMode: true}
	secrets := map[string]string{
		agentprotocol.SecretBackupAccessKey: "AKIATESTKEY", agentprotocol.SecretBackupSecretKey: "test-secret-key",
		agentprotocol.SecretDBPassword: "dbpass-1234",
	}
	res, err := r.Backup(context.Background(), "c", p, secrets, func(string) {})
	if err == nil || !strings.Contains(res.Message, "lost connection") {
		t.Fatalf("want dump failure, got %+v %v", res, err)
	}
	if _, ok := f.object(p.ObjectKey); ok {
		t.Fatal("failed dump left an S3 object")
	}
	if _, ok := f.object(p.ObjectKey + ".manifest.json"); ok {
		t.Fatal("failed dump left a manifest")
	}
	if f.completed != 0 || f.aborted != 1 {
		t.Fatalf("completed=%d aborted=%d", f.completed, f.aborted)
	}

	// And the happy path through the real S3 store.
	ex2 := &fakeExec{stdout: bytes.Repeat([]byte("row\n"), 1000)}
	r.Docker = ex2
	res, err = r.Backup(context.Background(), "c", p, secrets, func(string) {})
	if err != nil || res.Outcome != agentprotocol.OutcomeSucceeded {
		t.Fatalf("%+v %v", res, err)
	}
	if _, ok := f.object(p.ObjectKey); !ok {
		t.Fatal("object missing")
	}
	if _, ok := f.object(p.ObjectKey + ".manifest.json"); !ok {
		t.Fatal("manifest missing")
	}
	tr, err := r.Test(context.Background(), agentprotocol.BackupTestPayload{Destination: p.Destination, Prefix: "probe"}, secrets)
	if err != nil || tr.Outcome != agentprotocol.OutcomeSucceeded {
		t.Fatalf("destination test: %+v %v", tr, err)
	}
}

func TestS3StoreRequiresCredentialsAndBucket(t *testing.T) {
	if _, err := NewS3Store(agentprotocol.BackupDestination{Kind: "s3", Bucket: "b"}, "", ""); err == nil {
		t.Fatal("missing credentials must fail")
	}
	if _, err := NewS3Store(agentprotocol.BackupDestination{Kind: "s3"}, "a", "b"); err == nil {
		t.Fatal("missing bucket must fail")
	}
}
