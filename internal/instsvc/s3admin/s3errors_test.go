package s3admin

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/instsvc"
)

// failure makes one operation answer an error. n is how many requests fail
// (0 or negative: all of them).
type failure struct {
	status int
	code   string
	raw    string // sent verbatim instead of a coded XML error
	n      int
}

type fakeMPUpload struct {
	key, id   string
	initiated time.Time
	parts     []int64
	gone      bool // the upload vanished after it was listed: its parts answer NoSuchUpload
}

// s3Fake is an S3/IAM server that answers like SeaweedFS: real XML errors
// (BucketNotEmpty, NoSuchBucket, ...), paged listings and multi-delete.
type s3Fake struct {
	mu      sync.Mutex
	buckets map[string]map[string]int64
	users   map[string]map[string]string
	policy  map[string]string
	quota   map[string]string
	uploads []*fakeMPUpload

	pageSize   int // objects per listing page (0: all)
	uploadPage int // uploads per listing page (0: all)
	partPage   int // parts per listing page (0: all)

	failures map[string]*failure
	calls    []string
	queries  map[string][]string // op -> raw queries seen
	deleted  []string            // object keys removed by multi-delete, in order
	quiet    []bool
	aborted  []string
}

func newS3Fake() *s3Fake {
	return &s3Fake{
		buckets:  map[string]map[string]int64{},
		users:    map[string]map[string]string{},
		policy:   map[string]string{},
		quota:    map[string]string{},
		failures: map[string]*failure{},
		queries:  map[string][]string{},
	}
}

func (f *s3Fake) fail(op string, fl failure) {
	if fl.n == 0 {
		fl.n = -1
	}
	f.failures[op] = &fl
}

// answerFailure writes the programmed failure for op, if any.
func (f *s3Fake) answerFailure(w http.ResponseWriter, op string, iam bool) bool {
	fl := f.failures[op]
	if fl == nil || fl.n == 0 {
		return false
	}
	if fl.n > 0 {
		fl.n--
	}
	w.WriteHeader(fl.status)
	switch {
	case fl.raw != "":
		fmt.Fprint(w, fl.raw)
	case iam:
		fmt.Fprintf(w, `<ErrorResponse><Error><Code>%s</Code><Message>iam says %s</Message></Error></ErrorResponse>`, fl.code, fl.code)
	default:
		fmt.Fprintf(w, `<Error><Code>%s</Code><Message>s3 says %s</Message></Error>`, fl.code, fl.code)
	}
	return true
}

func (f *s3Fake) s3Error(w http.ResponseWriter, status int, code string) {
	w.WriteHeader(status)
	fmt.Fprintf(w, `<Error><Code>%s</Code><Message>s3 says %s</Message></Error>`, code, code)
}

func (f *s3Fake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 Credential=ADMIN/") {
		http.Error(w, "unsigned", http.StatusForbidden)
		return
	}
	body, _ := io.ReadAll(r.Body)
	if r.Method == http.MethodPost && r.URL.Path == "/" {
		f.serveIAM(w, string(body))
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/")
	bucket, key, _ := strings.Cut(path, "/")
	q := r.URL.Query()
	op := ""
	switch {
	case key != "" && r.Method == http.MethodGet && q.Has("uploadId"):
		op = "ListParts"
	case key != "" && r.Method == http.MethodDelete && q.Has("uploadId"):
		op = "AbortUpload"
	case r.Method == http.MethodPut && q.Has("seaweedfs-quota"):
		op = "PutQuota"
	case r.Method == http.MethodGet && q.Has("seaweedfs-quota"):
		op = "GetQuota"
	case r.Method == http.MethodGet && q.Has("uploads"):
		op = "ListUploads"
	case r.Method == http.MethodPost && q.Has("delete"):
		op = "DeleteObjects"
	case r.Method == http.MethodGet:
		op = "ListObjects"
	case r.Method == http.MethodPut:
		op = "PutBucket"
	case r.Method == http.MethodDelete:
		op = "DeleteBucket"
	}
	f.calls = append(f.calls, "s3:"+op)
	f.queries[op] = append(f.queries[op], r.URL.RawQuery)
	if f.answerFailure(w, op, false) {
		return
	}
	switch op {
	case "PutQuota":
		f.quota[bucket] = string(body)
		w.WriteHeader(http.StatusNoContent)
	case "GetQuota":
		if v, ok := f.quota[bucket]; ok {
			_, _ = io.WriteString(w, v)
		} else {
			_, _ = io.WriteString(w, `{"quota_size":0,"quota_unit":"B","quota_enabled":false}`)
		}
	case "PutBucket":
		if _, ok := f.buckets[bucket]; ok {
			f.s3Error(w, http.StatusConflict, "BucketAlreadyOwnedByYou")
			return
		}
		f.buckets[bucket] = map[string]int64{}
	case "DeleteBucket":
		objs, ok := f.buckets[bucket]
		switch {
		case !ok:
			f.s3Error(w, http.StatusNotFound, "NoSuchBucket")
		case len(objs) > 0:
			f.s3Error(w, http.StatusConflict, "BucketNotEmpty")
		default:
			delete(f.buckets, bucket)
			w.WriteHeader(http.StatusNoContent)
		}
	case "ListObjects":
		f.listObjects(w, bucket, q)
	case "DeleteObjects":
		var req struct {
			Quiet   bool `xml:"Quiet"`
			Objects []struct {
				Key string `xml:"Key"`
			} `xml:"Object"`
		}
		if err := xml.Unmarshal(body, &req); err != nil {
			f.s3Error(w, http.StatusBadRequest, "MalformedXML")
			return
		}
		f.quiet = append(f.quiet, req.Quiet)
		for _, o := range req.Objects {
			delete(f.buckets[bucket], o.Key)
			f.deleted = append(f.deleted, o.Key)
		}
		fmt.Fprint(w, `<DeleteResult/>`)
	case "ListUploads":
		f.listUploads(w, q)
	case "ListParts":
		f.listParts(w, key, q)
	case "AbortUpload":
		for _, u := range f.uploads {
			if u.id == q.Get("uploadId") && u.key == key {
				if u.gone {
					f.s3Error(w, http.StatusNotFound, "NoSuchUpload")
					return
				}
				f.aborted = append(f.aborted, u.id)
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		f.s3Error(w, http.StatusNotFound, "NoSuchUpload")
	default:
		f.s3Error(w, http.StatusMethodNotAllowed, "MethodNotAllowed")
	}
}

func (f *s3Fake) listObjects(w http.ResponseWriter, bucket string, q url.Values) {
	objs, ok := f.buckets[bucket]
	if !ok {
		f.s3Error(w, http.StatusNotFound, "NoSuchBucket")
		return
	}
	keys := make([]string, 0, len(objs))
	for k := range objs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if tok := q.Get("continuation-token"); tok != "" {
		i := sort.SearchStrings(keys, tok)
		if i < len(keys) && keys[i] == tok {
			i++
		}
		keys = keys[i:]
	}
	truncated := f.pageSize > 0 && len(keys) > f.pageSize
	if truncated {
		keys = keys[:f.pageSize]
	}
	fmt.Fprintf(w, `<ListBucketResult><IsTruncated>%t</IsTruncated>`, truncated)
	if truncated {
		fmt.Fprintf(w, `<NextContinuationToken>%s</NextContinuationToken>`, keys[len(keys)-1])
	}
	for _, k := range keys {
		fmt.Fprintf(w, `<Contents><Key>%s</Key><Size>%d</Size></Contents>`, k, objs[k])
	}
	fmt.Fprint(w, `</ListBucketResult>`)
}

func (f *s3Fake) listUploads(w http.ResponseWriter, q url.Values) {
	list := f.uploads
	if m := q.Get("upload-id-marker"); m != "" {
		for i, u := range list {
			if u.id == m {
				list = list[i+1:]
				break
			}
		}
	}
	truncated := f.uploadPage > 0 && len(list) > f.uploadPage
	if truncated {
		list = list[:f.uploadPage]
	}
	fmt.Fprintf(w, `<ListMultipartUploadsResult><IsTruncated>%t</IsTruncated>`, truncated)
	if truncated {
		last := list[len(list)-1]
		fmt.Fprintf(w, `<NextKeyMarker>%s</NextKeyMarker><NextUploadIdMarker>%s</NextUploadIdMarker>`, last.key, last.id)
	}
	for _, u := range list {
		fmt.Fprintf(w, `<Upload><Key>%s</Key><UploadId>%s</UploadId><Initiated>%s</Initiated></Upload>`, u.key, u.id, u.initiated.UTC().Format(time.RFC3339))
	}
	fmt.Fprint(w, `</ListMultipartUploadsResult>`)
}

func (f *s3Fake) listParts(w http.ResponseWriter, key string, q url.Values) {
	for _, u := range f.uploads {
		if u.id != q.Get("uploadId") || u.key != key {
			continue
		}
		if u.gone {
			f.s3Error(w, http.StatusNotFound, "NoSuchUpload")
			return
		}
		from, _ := strconv.Atoi(q.Get("part-number-marker"))
		type part struct {
			n    int
			size int64
		}
		var parts []part
		for i, s := range u.parts {
			if i+1 > from {
				parts = append(parts, part{i + 1, s})
			}
		}
		truncated := f.partPage > 0 && len(parts) > f.partPage
		if truncated {
			parts = parts[:f.partPage]
		}
		fmt.Fprintf(w, `<ListPartsResult><IsTruncated>%t</IsTruncated>`, truncated)
		if truncated {
			fmt.Fprintf(w, `<NextPartNumberMarker>%d</NextPartNumberMarker>`, parts[len(parts)-1].n)
		}
		for _, p := range parts {
			fmt.Fprintf(w, `<Part><PartNumber>%d</PartNumber><Size>%d</Size></Part>`, p.n, p.size)
		}
		fmt.Fprint(w, `</ListPartsResult>`)
		return
	}
	f.s3Error(w, http.StatusNotFound, "NoSuchUpload")
}

func (f *s3Fake) serveIAM(w http.ResponseWriter, body string) {
	v, _ := url.ParseQuery(body)
	action, user := v.Get("Action"), v.Get("UserName")
	f.calls = append(f.calls, "iam:"+action)
	if f.answerFailure(w, "iam:"+action, true) {
		return
	}
	iamErr := func(status int, code string) {
		w.WriteHeader(status)
		fmt.Fprintf(w, `<ErrorResponse><Error><Code>%s</Code><Message>m</Message></Error></ErrorResponse>`, code)
	}
	switch action {
	case "CreateUser":
		if f.users[user] != nil {
			iamErr(http.StatusConflict, "EntityAlreadyExists")
			return
		}
		f.users[user] = map[string]string{}
		fmt.Fprint(w, `<CreateUserResponse/>`)
	case "ListAccessKeys":
		if f.users[user] == nil {
			iamErr(http.StatusNotFound, "NoSuchEntity")
			return
		}
		fmt.Fprint(w, `<ListAccessKeysResponse><ListAccessKeysResult><AccessKeyMetadata>`)
		ids := make([]string, 0, len(f.users[user]))
		for k := range f.users[user] {
			ids = append(ids, k)
		}
		sort.Strings(ids)
		for _, k := range ids {
			fmt.Fprintf(w, `<member><AccessKeyId>%s</AccessKeyId></member>`, k)
		}
		fmt.Fprint(w, `</AccessKeyMetadata></ListAccessKeysResult></ListAccessKeysResponse>`)
	case "CreateAccessKey":
		f.users[user][v.Get("AccessKeyId")] = v.Get("SecretAccessKey")
		fmt.Fprint(w, `<CreateAccessKeyResponse/>`)
	case "DeleteAccessKey":
		delete(f.users[user], v.Get("AccessKeyId"))
		fmt.Fprint(w, `<DeleteAccessKeyResponse/>`)
	case "PutUserPolicy":
		f.policy[user] = v.Get("PolicyDocument")
		fmt.Fprint(w, `<PutUserPolicyResponse/>`)
	case "DeleteUserPolicy":
		delete(f.policy, user)
		fmt.Fprint(w, `<R/>`)
	case "DeleteUser":
		delete(f.users, user)
		fmt.Fprint(w, `<R/>`)
	default:
		iamErr(http.StatusBadRequest, "InvalidAction")
	}
}

func newS3Backend(t *testing.T, f *s3Fake, s secrets) *Backend {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	b, err := New(Config{Endpoint: srv.URL, AdminKey: "ADMIN", AdminSecret: "adminsecret", Slices: s})
	require.NoError(t, err)
	return b
}

func withObjects(f *s3Fake, bucket string, sizes map[string]int64) {
	f.buckets[bucket] = map[string]int64{}
	for k, v := range sizes {
		f.buckets[bucket][k] = v
	}
}

var ctx = context.Background()

func TestPurgeEmptiesABucketThatAnswersBucketNotEmpty(t *testing.T) {
	f := newS3Fake()
	f.pageSize = 2
	b := newS3Backend(t, f, secrets{"KEYA": "secret-a"})
	sl := instsvc.Slice{TeamID: uuid.New(), Kind: "s3", Ref: "nc-team-a", AccessKey: "KEYA"}
	require.NoError(t, b.EnsureSlice(ctx, sl, 0))
	withObjects(f, "nc-team-a", map[string]int64{"a": 1, "b": 2, "c": 3, "d/e": 4, "d/f": 5})

	require.NoError(t, b.Purge(ctx, sl))

	require.Empty(t, f.buckets, "the bucket is deleted once empty")
	require.ElementsMatch(t, []string{"a", "b", "c", "d/e", "d/f"}, f.deleted, "every object, across listing pages, is deleted exactly once")
	require.Len(t, f.deleted, 5)
	for _, q := range f.quiet {
		require.True(t, q, "multi-delete asks for a quiet answer")
	}
	require.Empty(t, f.users, "the slice user is deleted after the bucket")
	require.Empty(t, f.policy)
	// The first DeleteBucket was refused, the retry after emptying succeeded.
	var dels int
	for _, c := range f.calls {
		if c == "s3:DeleteBucket" {
			dels++
		}
	}
	require.Equal(t, 2, dels)
}

func TestPurgeKeepsEmptyingWhileObjectsKeepArriving(t *testing.T) {
	f := newS3Fake()
	withObjects(f, "nc-busy", map[string]int64{"a": 1})
	// A writer keeps refilling the bucket: BucketNotEmpty is not a final answer.
	f.fail("DeleteBucket", failure{status: 409, code: "BucketNotEmpty", n: 3})
	b := newS3Backend(t, f, secrets{})
	require.NoError(t, b.Purge(ctx, instsvc.Slice{Kind: "s3", Ref: "nc-busy", AccessKey: "K"}))
	require.Empty(t, f.buckets)
	var dels int
	for _, c := range f.calls {
		if c == "s3:DeleteBucket" {
			dels++
		}
	}
	require.Equal(t, 4, dels, "three BucketNotEmpty answers, then the delete goes through")
}

func TestPurgeOfAMissingBucketStillRemovesTheUser(t *testing.T) {
	f := newS3Fake()
	b := newS3Backend(t, f, secrets{"KEYA": "s"})
	sl := instsvc.Slice{Kind: "s3", Ref: "nc-half", AccessKey: "KEYA"}
	// A half-made slice: user and key exist, the bucket was never created.
	f.users["nc-half"] = map[string]string{"KEYA": "s", "OLD": "x"}
	f.policy["nc-half"] = "{}"
	require.NoError(t, b.Purge(ctx, sl))
	require.Empty(t, f.users)
	require.Empty(t, f.policy)
}

func TestPurgeOfASliceWithNeitherBucketNorUserIsANoOp(t *testing.T) {
	f := newS3Fake()
	b := newS3Backend(t, f, secrets{})
	require.NoError(t, b.Purge(ctx, instsvc.Slice{Kind: "s3", Ref: "nc-never"}))
	require.NoError(t, b.Purge(ctx, instsvc.Slice{Kind: "s3", Ref: ""}), "a slice without a bucket has nothing to purge")
	require.NotContains(t, f.calls, "iam:DeleteUser", "no user: nothing to delete")
}

func TestPurgeFailuresAreSurfacedAndStopBeforeTheUserIsRemoved(t *testing.T) {
	sl := instsvc.Slice{Kind: "s3", Ref: "nc-x", AccessKey: "KEYA"}
	cases := []struct {
		name    string
		fail    string
		fl      failure
		wantErr string
		user    bool // the user must still exist afterwards
	}{
		{"bucket delete denied", "DeleteBucket", failure{status: 403, code: "AccessDenied"}, "delete bucket", true},
		{"listing for emptying fails", "ListObjects", failure{status: 500, code: "InternalError"}, "list bucket", true},
		{"multi-delete rejected", "DeleteObjects", failure{status: 400, code: "MalformedXML"}, "delete objects", true},
		{"key list fails", "iam:ListAccessKeys", failure{status: 500, code: "ServiceFailure"}, "list keys", true},
		{"key delete fails", "iam:DeleteAccessKey", failure{status: 500, code: "ServiceFailure"}, "delete key", true},
		{"policy delete fails", "iam:DeleteUserPolicy", failure{status: 500, code: "ServiceFailure"}, "delete policy", true},
		{"user delete fails", "iam:DeleteUser", failure{status: 500, code: "ServiceFailure"}, "delete user", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newS3Fake()
			withObjects(f, "nc-x", map[string]int64{"o": 1})
			f.users["nc-x"] = map[string]string{"KEYA": "s"}
			f.policy["nc-x"] = "{}"
			f.fail(tc.fail, tc.fl)
			b := newS3Backend(t, f, secrets{})
			err := b.Purge(ctx, sl)
			require.ErrorContains(t, err, tc.wantErr)
			require.ErrorContains(t, err, tc.fl.code, "the service's own error code reaches the caller")
			require.Equal(t, tc.user, f.users["nc-x"] != nil)
		})
	}
}

func TestPurgeTreatsAlreadyGoneKeyPolicyAndUserAsDone(t *testing.T) {
	f := newS3Fake()
	f.users["nc-x"] = map[string]string{"KEYA": "s"}
	f.fail("iam:DeleteAccessKey", failure{status: 404, code: "NoSuchEntity", n: -1})
	f.fail("iam:DeleteUserPolicy", failure{status: 404, code: "NoSuchEntity", n: -1})
	f.fail("iam:DeleteUser", failure{status: 404, code: "NoSuchEntity", n: -1})
	b := newS3Backend(t, f, secrets{})
	require.NoError(t, b.Purge(ctx, instsvc.Slice{Kind: "s3", Ref: "nc-x"}))
}

func TestPurgeRefusesTheSystemBucketWithoutTouchingAnything(t *testing.T) {
	f := newS3Fake()
	withObjects(f, instsvc.SystemBucket, map[string]int64{"keep": 1})
	b := newS3Backend(t, f, secrets{})
	require.Error(t, b.Purge(ctx, instsvc.Slice{Kind: "s3", Ref: instsvc.SystemBucket}))
	require.Empty(t, f.calls)
	require.Contains(t, f.buckets[instsvc.SystemBucket], "keep")
}

func TestUsageSumsEveryListingPage(t *testing.T) {
	f := newS3Fake()
	f.pageSize = 2
	withObjects(f, "nc-x", map[string]int64{"a": 1, "b": 20, "c": 300, "d": 4000, "e": 50000})
	b := newS3Backend(t, f, secrets{})
	n, err := b.Usage(ctx, instsvc.Slice{Ref: "nc-x"})
	require.NoError(t, err)
	require.EqualValues(t, 54321, n)
	lists := f.queries["ListObjects"]
	require.Len(t, lists, 3, "5 objects at 2 per page")
	require.NotContains(t, lists[0], "continuation-token")
	require.Contains(t, lists[1], "continuation-token=b")
	require.Contains(t, lists[2], "continuation-token=d")
}

func TestUsageFollowsUploadAndPartPagination(t *testing.T) {
	f := newS3Fake()
	f.uploadPage, f.partPage = 1, 2
	withObjects(f, "nc-x", map[string]int64{"obj": 7})
	now := time.Now()
	f.uploads = []*fakeMPUpload{
		{key: "u/1", id: "id1", initiated: now.Add(-time.Hour), parts: []int64{10, 20, 30, 40, 50}},
		{key: "u/2", id: "id2", initiated: now.Add(-2 * time.Hour), parts: []int64{1000}},
		{key: "u/3", id: "id3", initiated: now.Add(-3 * time.Hour)},
	}
	b := newS3Backend(t, f, secrets{})
	n, err := b.Usage(ctx, instsvc.Slice{Ref: "nc-x"})
	require.NoError(t, err)
	require.EqualValues(t, 7+150+1000, n)
	require.Empty(t, f.aborted)
	require.Len(t, f.queries["ListUploads"], 3)
	require.Contains(t, f.queries["ListUploads"][1], "key-marker=u%2F1")
	require.Contains(t, f.queries["ListUploads"][1], "upload-id-marker=id1")
	require.Len(t, f.queries["ListParts"], 3+1+1, "five parts at two per page, then two single-page uploads")
}

func TestUsageIgnoresUploadsThatVanishAfterBeingListed(t *testing.T) {
	f := newS3Fake()
	withObjects(f, "nc-x", nil)
	now := time.Now()
	f.uploads = []*fakeMPUpload{
		{key: "u/1", id: "live", initiated: now.Add(-time.Minute), parts: []int64{5}},
		{key: "u/2", id: "done", initiated: now.Add(-time.Minute), parts: []int64{999}, gone: true},
	}
	b := newS3Backend(t, f, secrets{})
	n, err := b.Usage(ctx, instsvc.Slice{Ref: "nc-x"})
	require.NoError(t, err)
	require.EqualValues(t, 5, n, "an upload completed since the listing adds nothing and is no error")
}

func TestUsageToleratesAStaleUploadThatIsAlreadyGone(t *testing.T) {
	f := newS3Fake()
	withObjects(f, "nc-x", nil)
	f.uploads = []*fakeMPUpload{{key: "u/1", id: "old", initiated: time.Now().Add(-72 * time.Hour), parts: []int64{999}, gone: true}}
	b := newS3Backend(t, f, secrets{})
	n, err := b.Usage(ctx, instsvc.Slice{Ref: "nc-x"})
	require.NoError(t, err)
	require.Zero(t, n)
}

func TestUsageAbortsUploadsBeyondTheScanBound(t *testing.T) {
	f := newS3Fake()
	withObjects(f, "nc-x", nil)
	now := time.Now()
	extra := 3
	for i := range maxUploadsScanned + extra {
		// No parts: the scanned ones add nothing; the point is which get aborted.
		f.uploads = append(f.uploads, &fakeMPUpload{key: fmt.Sprintf("k%04d", i), id: fmt.Sprintf("id%04d", i), initiated: now})
	}
	b := newS3Backend(t, f, secrets{})
	n, err := b.Usage(ctx, instsvc.Slice{Ref: "nc-x"})
	require.NoError(t, err)
	require.Zero(t, n)
	require.Equal(t, []string{
		fmt.Sprintf("id%04d", maxUploadsScanned), fmt.Sprintf("id%04d", maxUploadsScanned+1), fmt.Sprintf("id%04d", maxUploadsScanned+2),
	}, f.aborted, "only the surplus beyond the bound is aborted")
}

func TestUsageFailuresAreErrors(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name    string
		setup   func(f *s3Fake)
		wantErr string
	}{
		{"listing fails", func(f *s3Fake) { f.fail("ListObjects", failure{status: 500, code: "InternalError"}) }, "list bucket"},
		{"missing bucket", func(f *s3Fake) { delete(f.buckets, "nc-x") }, "NoSuchBucket"},
		{"listing is not XML", func(f *s3Fake) { f.fail("ListObjects", failure{status: 200, raw: "<ListBucketResult><Contents>"}) }, "decode listing"},
		{"uploads listing fails", func(f *s3Fake) { f.fail("ListUploads", failure{status: 500, code: "InternalError"}) }, "list multipart uploads"},
		{"uploads listing is not XML", func(f *s3Fake) { f.fail("ListUploads", failure{status: 200, raw: "<a><b>"}) }, "decode multipart uploads"},
		{"parts listing fails", func(f *s3Fake) {
			f.uploads = []*fakeMPUpload{{key: "k", id: "i", initiated: now}}
			f.fail("ListParts", failure{status: 500, code: "InternalError"})
		}, "list parts"},
		{"parts listing is not XML", func(f *s3Fake) {
			f.uploads = []*fakeMPUpload{{key: "k", id: "i", initiated: now}}
			f.fail("ListParts", failure{status: 200, raw: "<a><b>"})
		}, "decode parts"},
		{"abort of a stale upload fails", func(f *s3Fake) {
			f.uploads = []*fakeMPUpload{{key: "k", id: "i", initiated: now.Add(-48 * time.Hour)}}
			f.fail("AbortUpload", failure{status: 403, code: "AccessDenied"})
		}, "abort multipart upload"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newS3Fake()
			withObjects(f, "nc-x", map[string]int64{"a": 1})
			tc.setup(f)
			b := newS3Backend(t, f, secrets{})
			_, err := b.Usage(ctx, instsvc.Slice{Ref: "nc-x"})
			require.ErrorContains(t, err, tc.wantErr)
		})
	}
}

func TestUsageAcceptsAnEmptyUploadsAnswer(t *testing.T) {
	f := newS3Fake()
	withObjects(f, "nc-x", map[string]int64{"a": 5})
	f.fail("ListUploads", failure{status: 200, raw: "  \n", n: -1})
	b := newS3Backend(t, f, secrets{})
	n, err := b.Usage(ctx, instsvc.Slice{Ref: "nc-x"})
	require.NoError(t, err)
	require.EqualValues(t, 5, n)
}

func TestEnsureSliceCreatesBucketAgainstRealS3Errors(t *testing.T) {
	f := newS3Fake()
	b := newS3Backend(t, f, secrets{"KEYA": "secret-a"})
	sl := instsvc.Slice{Kind: "s3", Ref: "nc-team-a", AccessKey: "KEYA"}
	// The bucket exists already (BucketAlreadyOwnedByYou) and so does the user (EntityAlreadyExists): both are fine.
	f.buckets["nc-team-a"] = map[string]int64{"keep": 1}
	f.users["nc-team-a"] = map[string]string{"STRAY": "x"}
	require.NoError(t, b.EnsureSlice(ctx, sl, 0))
	require.Equal(t, map[string]string{"KEYA": "secret-a"}, f.users["nc-team-a"], "the stray key is revoked, the slice key created")
	require.Contains(t, f.buckets["nc-team-a"], "keep", "an existing bucket is not recreated")
	require.NotEmpty(t, f.policy["nc-team-a"])
	require.Empty(t, f.quota, "quota 0 sets nothing")
}

func TestEnsureSliceStopsAtTheFirstFailureAndNeverLeavesAWiderPolicy(t *testing.T) {
	sl := instsvc.Slice{Kind: "s3", Ref: "nc-team-a", AccessKey: "KEYA"}
	cases := []struct {
		name    string
		fail    string
		fl      failure
		quota   int64
		wantErr string
		never   string // a call that must not have happened
	}{
		{"bucket owned by someone else", "PutBucket", failure{status: 409, code: "BucketAlreadyExists", n: -1}, 0, "create bucket", "iam:CreateUser"},
		{"create user fails", "iam:CreateUser", failure{status: 500, code: "ServiceFailure", n: -1}, 0, "create user", "iam:ListAccessKeys"},
		{"list keys fails", "iam:ListAccessKeys", failure{status: 500, code: "ServiceFailure", n: -1}, 0, "list keys", "iam:PutUserPolicy"},
		{"revoke of an old key fails", "iam:DeleteAccessKey", failure{status: 500, code: "ServiceFailure", n: -1}, 0, "revoke old key", "iam:PutUserPolicy"},
		{"create key fails", "iam:CreateAccessKey", failure{status: 500, code: "ServiceFailure", n: -1}, 0, "create key", "iam:PutUserPolicy"},
		{"put policy fails", "iam:PutUserPolicy", failure{status: 500, code: "ServiceFailure", n: -1}, 1 << 20, "put policy", "s3:PutQuota"},
		{"quota write fails", "PutQuota", failure{status: 500, code: "InternalError", n: -1}, 1 << 20, "set bucket quota", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newS3Fake()
			f.users["nc-team-a"] = map[string]string{"OLD": "x"} // an old key to revoke
			f.fail(tc.fail, tc.fl)
			b := newS3Backend(t, f, secrets{"KEYA": "secret-a"})
			err := b.EnsureSlice(ctx, sl, tc.quota)
			require.ErrorContains(t, err, tc.wantErr)
			if tc.never != "" {
				require.NotContains(t, f.calls, tc.never)
			}
		})
	}
}

func TestEnsureSliceFailsWhenTheSliceSecretCannotBeOpened(t *testing.T) {
	f := newS3Fake()
	srv := httptest.NewServer(f)
	defer srv.Close()
	b, err := New(Config{Endpoint: srv.URL, AdminKey: "ADMIN", AdminSecret: "s", Slices: failingSecrets{}})
	require.NoError(t, err)
	err = b.EnsureSlice(ctx, instsvc.Slice{Kind: "s3", Ref: "nc-a", AccessKey: "K"}, 0)
	require.ErrorContains(t, err, "open slice key")
	require.Empty(t, f.calls, "nothing is created without the key's secret")
}

type failingSecrets struct{ secrets }

func (failingSecrets) Secret(context.Context, instsvc.Slice) (string, error) {
	return "", errors.New("vault sealed")
}

func TestEnsureSliceRepairsAMissingKeyOfAnExistingUser(t *testing.T) {
	f := newS3Fake()
	b := newS3Backend(t, f, secrets{"KEYA": "secret-a"})
	f.buckets["nc-a"] = map[string]int64{}
	f.users["nc-a"] = map[string]string{} // user without key and policy: a half-made slice
	require.NoError(t, b.EnsureSlice(ctx, instsvc.Slice{Kind: "s3", Ref: "nc-a", AccessKey: "KEYA"}, 0))
	require.Equal(t, map[string]string{"KEYA": "secret-a"}, f.users["nc-a"])
	require.NotEmpty(t, f.policy["nc-a"])
}

func TestSetQuotaZeroRemovesAnExistingQuota(t *testing.T) {
	f := newS3Fake()
	b := newS3Backend(t, f, secrets{})
	f.quota["nc-a"] = `{"quota_size":100,"quota_unit":"B","quota_enabled":true}`
	require.NoError(t, b.SetQuota(ctx, "nc-a", 0))
	require.JSONEq(t, `{"quota_size":0,"quota_unit":"B","quota_enabled":false}`, f.quota["nc-a"], "disabled, not re-enabled")
	q, err := b.Quota(ctx, "nc-a")
	require.NoError(t, err)
	require.Zero(t, q)
}

func TestSetQuotaDisablesBeforeEnablingAChangedQuota(t *testing.T) {
	f := newS3Fake()
	b := newS3Backend(t, f, secrets{})
	f.quota["nc-a"] = `{"quota_size":100,"quota_unit":"B","quota_enabled":true}`
	require.NoError(t, b.SetQuota(ctx, "nc-a", 500))
	var puts []string
	for _, c := range f.calls {
		if c == "s3:PutQuota" {
			puts = append(puts, c)
		}
	}
	require.Len(t, puts, 2, "disable (clears read-only), then enable")
	q, err := b.Quota(ctx, "nc-a")
	require.NoError(t, err)
	require.EqualValues(t, 500, q)
}

func TestSetQuotaStillWritesWhenTheCurrentQuotaCannotBeRead(t *testing.T) {
	f := newS3Fake()
	b := newS3Backend(t, f, secrets{})
	f.fail("GetQuota", failure{status: 500, code: "InternalError", n: -1})
	require.NoError(t, b.SetQuota(ctx, "nc-a", 77))
	require.JSONEq(t, `{"quota_size":77,"quota_unit":"B","quota_enabled":true}`, f.quota["nc-a"])
}

func TestSetQuotaReportsAFailedWriteAndDoesNotEnableAfterAFailedDisable(t *testing.T) {
	f := newS3Fake()
	b := newS3Backend(t, f, secrets{})
	f.fail("PutQuota", failure{status: 403, code: "AccessDenied", n: -1})
	err := b.SetQuota(ctx, "nc-a", 77)
	require.ErrorContains(t, err, "set bucket quota")
	require.ErrorContains(t, err, "AccessDenied")
	require.Empty(t, f.quota)
	var puts int
	for _, c := range f.calls {
		if c == "s3:PutQuota" {
			puts++
		}
	}
	require.Equal(t, 1, puts)
}

func TestQuotaErrors(t *testing.T) {
	f := newS3Fake()
	b := newS3Backend(t, f, secrets{})
	f.fail("GetQuota", failure{status: 404, code: "NoSuchBucket", n: 1})
	_, err := b.Quota(ctx, "nc-a")
	require.ErrorContains(t, err, "get bucket quota")
	require.True(t, hasCode(err, "NoSuchBucket"), "the cause stays inspectable")

	f.fail("GetQuota", failure{status: 200, raw: "not json", n: 1})
	_, err = b.Quota(ctx, "nc-a")
	require.ErrorContains(t, err, "decode bucket quota")
}

func TestHealthReportsAnIAMErrorWithItsCode(t *testing.T) {
	f := newS3Fake()
	b := newS3Backend(t, f, secrets{})
	f.fail("iam:ListUsers", failure{status: 403, code: "SignatureDoesNotMatch", n: 1})
	err := b.Health(ctx)
	require.ErrorContains(t, err, "built-in S3")
	require.ErrorContains(t, err, "403 SignatureDoesNotMatch")
	require.True(t, hasCode(err, "SignatureDoesNotMatch"))
}

func TestAPIErrorWithoutAnXMLBodyReportsOnlyTheStatus(t *testing.T) {
	f := newS3Fake()
	b := newS3Backend(t, f, secrets{})
	f.fail("DeleteBucket", failure{status: 502, raw: "<html>bad gateway</html>", n: 1})
	err := b.Purge(ctx, instsvc.Slice{Kind: "s3", Ref: "nc-a"})
	require.EqualError(t, err, "delete bucket: s3 service answered 502")
	require.False(t, hasCode(err, "NoSuchBucket"))
}

func TestParseErrorReadsBothS3AndIAMShapes(t *testing.T) {
	err := parseError(409, []byte(`<Error><Code>BucketNotEmpty</Code><Message>The bucket you tried to delete is not empty</Message></Error>`))
	require.EqualError(t, err, "s3 service answered 409 BucketNotEmpty: The bucket you tried to delete is not empty")
	err = parseError(404, []byte(`<ErrorResponse><Error><Code>NoSuchEntity</Code><Message>gone</Message></Error></ErrorResponse>`))
	require.EqualError(t, err, "s3 service answered 404 NoSuchEntity: gone")
	require.True(t, hasCode(fmt.Errorf("wrapped: %w", err), "NoSuchEntity"))
	require.False(t, hasCode(errors.New("plain"), "NoSuchEntity"))
	require.False(t, hasCode(nil, "NoSuchEntity"))
}

func TestNewRejectsEndpointsThatAreNotHTTP(t *testing.T) {
	for _, ep := range []string{"", "ftp://s3:8333", "s3:8333", "http://"} {
		_, err := New(Config{Endpoint: ep, AdminKey: "k", AdminSecret: "s", Slices: secrets{}})
		require.Error(t, err, "endpoint %q", ep)
	}
}

func TestCallOnUnreachableEndpointIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	endpoint := srv.URL
	srv.Close()
	b, err := New(Config{Endpoint: endpoint, AdminKey: "k", AdminSecret: "s", Slices: secrets{}})
	require.NoError(t, err)
	require.Error(t, b.Health(ctx))
	_, err = b.Usage(ctx, instsvc.Slice{Ref: "x"})
	require.Error(t, err)
}
