package backup

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

func keysOf(objs []Object) []string {
	out := make([]string, len(objs))
	for i, o := range objs {
		out[i] = o.Key
	}
	sort.Strings(out)
	return out
}

func TestLocalStoreList(t *testing.T) {
	root := t.TempDir()
	st, err := NewLocalStore(root, "")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	// A store nobody wrote to has nothing.
	if got, err := (&LocalStore{root: filepath.Join(root, "missing")}).List(ctx, ""); err != nil || len(got) != 0 {
		t.Fatalf("missing dir: %v %v", got, err)
	}

	for _, key := range []string{"a/one.dump", "a/b/two.dump", "c/three.dump"} {
		if err := st.Put(ctx, key, strings.NewReader("x")); err != nil {
			t.Fatal(err)
		}
	}
	// A hidden partial upload is not an object.
	if err := os.WriteFile(filepath.Join(root, "a", ".partial-123"), []byte("half"), 0o600); err != nil {
		t.Fatal(err)
	}

	all, err := st.List(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(keysOf(all), ","), "a/b/two.dump,a/one.dump,c/three.dump"; got != want {
		t.Fatalf("List(\"\") = %s, want %s", got, want)
	}
	if all[0].Size != 1 || all[0].ModTime.IsZero() {
		t.Fatalf("size and time are filled in: %+v", all[0])
	}
	sub, err := st.List(ctx, "a/")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(keysOf(sub), ","), "a/b/two.dump,a/one.dump"; got != want {
		t.Fatalf("List(a/) = %s, want %s", got, want)
	}

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := st.List(cancelled, ""); err == nil {
		t.Fatal("a cancelled context stops the listing")
	}
}

// fakeLister serves ListObjectsV2 for one bucket, two keys per page.
func fakeLister(t *testing.T, keys []string) *httptest.Server {
	t.Helper()
	sort.Strings(keys)
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if r.Method != http.MethodGet || q.Get("list-type") != "2" {
			http.Error(w, "unsupported", http.StatusNotImplemented)
			return
		}
		prefix := q.Get("prefix")
		var matching []string
		for _, k := range keys {
			if strings.HasPrefix(k, prefix) {
				matching = append(matching, k)
			}
		}
		start := 0
		if tok := q.Get("continuation-token"); tok != "" {
			for i, k := range matching {
				if k == tok {
					start = i
				}
			}
		}
		const page = 2
		end := min(start+page, len(matching))
		var b strings.Builder
		b.WriteString(`<?xml version="1.0"?><ListBucketResult><Name>bucket</Name><Prefix>` + prefix + `</Prefix>`)
		fmt.Fprintf(&b, "<KeyCount>%d</KeyCount><MaxKeys>%d</MaxKeys>", end-start, page)
		if end < len(matching) {
			b.WriteString("<IsTruncated>true</IsTruncated><NextContinuationToken>" + matching[end] + "</NextContinuationToken>")
		} else {
			b.WriteString("<IsTruncated>false</IsTruncated>")
		}
		for _, k := range matching[start:end] {
			b.WriteString("<Contents><Key>" + k + "</Key><LastModified>2026-10-27T03:30:00.000Z</LastModified><Size>7</Size></Contents>")
		}
		b.WriteString("</ListBucketResult>")
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(b.String()))
	}))
}

func TestS3StoreListPagesAndFiltersByPrefix(t *testing.T) {
	srv := fakeLister(t, []string{
		"nebulactrl-instance/2026/10/a.dump", "nebulactrl-instance/2026/10/a.dump.manifest.json",
		"nebulactrl-instance/2026/10/b.dump", "nebulactrl-instance/2026/10/b.dump.manifest.json",
		"other/x.dump",
	})
	defer srv.Close()
	st, err := NewS3Store(agentprotocol.BackupDestination{
		Kind: agentprotocol.DestS3, Endpoint: srv.URL, Region: "us-east-1", Bucket: "bucket", PathStyle: true, CompatMode: true,
	}, "AKIATESTKEY", "test-secret-key")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	all, err := st.List(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 5 {
		t.Fatalf("all pages are read: got %d objects: %v", len(all), keysOf(all))
	}
	if all[0].Size != 7 || all[0].ModTime.IsZero() {
		t.Fatalf("size and time are filled in: %+v", all[0])
	}
	sub, err := st.List(ctx, "nebulactrl-instance/")
	if err != nil {
		t.Fatal(err)
	}
	if len(sub) != 4 {
		t.Fatalf("prefix narrows the listing: %v", keysOf(sub))
	}
}
