package s3admin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/instsvc"
)

type secrets map[string]string

func (secrets) TeamOfServer(context.Context, uuid.UUID) (uuid.UUID, error) { return uuid.Nil, nil }
func (secrets) ForTeam(context.Context, uuid.UUID) (instsvc.TeamSlices, error) {
	return instsvc.TeamSlices{}, nil
}
func (secrets) AccessKeyOwner(context.Context, string) (uuid.UUID, error) { return uuid.Nil, nil }
func (s secrets) Secret(_ context.Context, sl instsvc.Slice) (string, error) {
	return s[sl.AccessKey], nil
}

// fakeSeaweed is the slice of SeaweedFS's IAM and S3 API the backend uses.
type fakeSeaweed struct {
	mu      sync.Mutex
	buckets map[string]bool
	users   map[string]map[string]string // user -> access key -> secret
	policy  map[string]string
	quota   map[string]string
	calls   []string
	// uploads are the unfinished multipart uploads of every bucket; aborted
	// records the upload ids a DELETE ?uploadId removed.
	uploads []fakeUpload
	aborted []string
}

func newFake() *fakeSeaweed {
	return &fakeSeaweed{buckets: map[string]bool{}, users: map[string]map[string]string{}, policy: map[string]string{}, quota: map[string]string{}}
}

func (f *fakeSeaweed) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 Credential=ADMIN/") || r.Header.Get("X-Amz-Content-Sha256") == "" {
		http.Error(w, "unsigned", http.StatusForbidden)
		return
	}
	body, _ := io.ReadAll(r.Body)
	xmlErr := func(status int, code string) {
		w.WriteHeader(status)
		fmt.Fprintf(w, `<ErrorResponse><Error><Code>%s</Code><Message>m</Message></Error></ErrorResponse>`, code)
	}
	if r.Method == http.MethodPost && r.URL.Path == "/" {
		v, _ := url.ParseQuery(string(body))
		user := v.Get("UserName")
		f.calls = append(f.calls, v.Get("Action"))
		switch v.Get("Action") {
		case "ListUsers":
			fmt.Fprint(w, `<ListUsersResponse/>`)
		case "CreateUser":
			if f.users[user] != nil {
				xmlErr(409, "EntityAlreadyExists")
				return
			}
			f.users[user] = map[string]string{}
			fmt.Fprint(w, `<CreateUserResponse/>`)
		case "ListAccessKeys":
			if f.users[user] == nil {
				xmlErr(404, "NoSuchEntity")
				return
			}
			fmt.Fprint(w, `<ListAccessKeysResponse><ListAccessKeysResult><AccessKeyMetadata>`)
			for k := range f.users[user] {
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
			if len(f.users[user]) > 0 {
				xmlErr(409, "DeleteConflict")
				return
			}
			delete(f.users, user)
			fmt.Fprint(w, `<R/>`)
		default:
			xmlErr(400, "InvalidAction")
		}
		return
	}
	bucket := strings.TrimPrefix(r.URL.Path, "/")
	f.calls = append(f.calls, r.Method+" /"+bucket+"?"+r.URL.RawQuery)
	switch {
	case r.Method == http.MethodPut && strings.HasPrefix(r.URL.RawQuery, "seaweedfs-quota"):
		f.quota[bucket] = string(body)
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.RawQuery, "seaweedfs-quota"):
		if q, ok := f.quota[bucket]; ok {
			_, _ = io.WriteString(w, q)
		} else {
			_, _ = io.WriteString(w, `{"quota_size":0,"quota_unit":"B","quota_enabled":false}`)
		}
	case r.Method == http.MethodGet && r.URL.Query().Has("uploads"):
		fmt.Fprint(w, `<ListMultipartUploadsResult><IsTruncated>false</IsTruncated>`)
		for _, u := range f.uploads {
			fmt.Fprintf(w, `<Upload><Key>%s</Key><UploadId>%s</UploadId><Initiated>%s</Initiated></Upload>`, u.key, u.id, u.initiated.UTC().Format(time.RFC3339))
		}
		fmt.Fprint(w, `</ListMultipartUploadsResult>`)
	case r.Method == http.MethodGet && r.URL.Query().Has("uploadId"):
		fmt.Fprint(w, `<ListPartsResult><IsTruncated>false</IsTruncated>`)
		for _, u := range f.uploads {
			if u.id == r.URL.Query().Get("uploadId") {
				for _, p := range u.parts {
					fmt.Fprintf(w, `<Part><PartNumber>1</PartNumber><Size>%d</Size></Part>`, p)
				}
			}
		}
		fmt.Fprint(w, `</ListPartsResult>`)
	case r.Method == http.MethodDelete && r.URL.Query().Has("uploadId"):
		f.aborted = append(f.aborted, r.URL.Query().Get("uploadId"))
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodPut:
		if f.buckets[bucket] {
			w.WriteHeader(http.StatusConflict)
			fmt.Fprint(w, `<Error><Code>BucketAlreadyOwnedByYou</Code><Message>m</Message></Error>`)
			return
		}
		f.buckets[bucket] = true
	case r.Method == http.MethodDelete:
		if !f.buckets[bucket] {
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `<Error><Code>NoSuchBucket</Code><Message>m</Message></Error>`)
			return
		}
		delete(f.buckets, bucket)
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodGet && strings.Contains(r.URL.RawQuery, "list-type=2"):
		fmt.Fprint(w, `<ListBucketResult><IsTruncated>false</IsTruncated><Contents><Key>a</Key><Size>10</Size></Contents><Contents><Key>b</Key><Size>32</Size></Contents></ListBucketResult>`)
	}
}

func newBackend(t *testing.T, f *fakeSeaweed, s secrets) *Backend {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	b, err := New(Config{Endpoint: srv.URL, AdminKey: "ADMIN", AdminSecret: "adminsecret", Slices: s})
	require.NoError(t, err)
	return b
}

func TestEnsureSliceMakesABucketScopedKeyAndIsIdempotent(t *testing.T) {
	f := newFake()
	b := newBackend(t, f, secrets{"KEYA": "secret-a"})
	sl := instsvc.Slice{TeamID: uuid.New(), Kind: "s3", Ref: "nc-team-a", AccessKey: "KEYA"}
	ctx := context.Background()

	require.NoError(t, b.Health(ctx))
	require.NoError(t, b.EnsureSlice(ctx, sl, 1<<30))
	require.True(t, f.buckets["nc-team-a"])
	require.Equal(t, map[string]string{"KEYA": "secret-a"}, f.users["nc-team-a"], "one key: the slice's, with the secret the control plane stores")
	var pol struct {
		Statement []struct {
			Effect   string
			Action   []string
			Resource []string
		}
	}
	require.NoError(t, json.Unmarshal([]byte(f.policy["nc-team-a"]), &pol))
	require.Len(t, pol.Statement, 1)
	require.Equal(t, []string{"arn:aws:s3:::nc-team-a", "arn:aws:s3:::nc-team-a/*"}, pol.Statement[0].Resource, "the policy reaches this bucket and nothing else")
	require.JSONEq(t, `{"quota_size":1073741824,"quota_unit":"B","quota_enabled":true}`, f.quota["nc-team-a"])

	// Again: no error, no second key, the quota is not toggled (that would unlock an over-quota bucket).
	f.calls = nil
	require.NoError(t, b.EnsureSlice(ctx, sl, 1<<30))
	require.Len(t, f.users["nc-team-a"], 1)
	for _, c := range f.calls {
		require.NotContains(t, c, "PUT /nc-team-a?seaweedfs-quota", "an unchanged quota is left alone")
	}

	// A re-keyed slice: the old key is revoked.
	secretsOf := secrets{"KEYA": "secret-a", "KEYB": "secret-b"}
	b.slices = secretsOf
	require.NoError(t, b.EnsureSlice(ctx, instsvc.Slice{Kind: "s3", Ref: "nc-team-a", AccessKey: "KEYB"}, 1<<30))
	require.Equal(t, map[string]string{"KEYB": "secret-b"}, f.users["nc-team-a"])

	// Without a quota nothing is set; a changed quota is applied as disable-then-enable.
	require.NoError(t, b.SetQuota(ctx, "nc-team-a", 5<<30))
	require.JSONEq(t, `{"quota_size":5368709120,"quota_unit":"B","quota_enabled":true}`, f.quota["nc-team-a"])

	for _, bad := range []instsvc.Slice{{Kind: "s3", Ref: "", AccessKey: "K"}, {Kind: "s3", Ref: "x", AccessKey: ""}} {
		require.Error(t, b.EnsureSlice(ctx, bad, 0))
	}
}

func TestUsageSumsTheListing(t *testing.T) {
	b := newBackend(t, newFake(), secrets{})
	n, err := b.Usage(context.Background(), instsvc.Slice{Ref: "nc-x"})
	require.NoError(t, err)
	require.EqualValues(t, 42, n)
}

type fakeUpload struct {
	key, id   string
	initiated time.Time
	parts     []int64
}

func TestUsageCountsUnfinishedMultipartPartsAndAbortsStaleUploads(t *testing.T) {
	f := newFake()
	f.uploads = []fakeUpload{
		{key: "big/a", id: "u1", initiated: time.Now().Add(-time.Hour), parts: []int64{100, 200}},
		{key: "big/b", id: "stale", initiated: time.Now().Add(-48 * time.Hour), parts: []int64{1 << 20}},
	}
	b := newBackend(t, f, secrets{})
	n, err := b.Usage(context.Background(), instsvc.Slice{Ref: "nc-x"})
	require.NoError(t, err)
	require.EqualValues(t, 42+300, n, "listed objects plus the parts of the live upload; the stale upload does not count")
	require.Equal(t, []string{"stale"}, f.aborted, "an upload older than 24 h is aborted")
}

func TestPurgeDeletesBucketKeyAndUserAndIsIdempotent(t *testing.T) {
	f := newFake()
	b := newBackend(t, f, secrets{"KEYA": "s"})
	ctx := context.Background()
	sl := instsvc.Slice{TeamID: uuid.New(), Kind: "s3", Ref: "nc-team-a", AccessKey: "KEYA"}
	require.NoError(t, b.EnsureSlice(ctx, sl, 0))
	require.NoError(t, b.Purge(ctx, sl))
	require.Empty(t, f.buckets)
	require.Empty(t, f.users, "user and key are gone")
	require.Empty(t, f.policy)
	require.NoError(t, b.Purge(ctx, sl), "purging what is gone is not an error")
	require.Error(t, b.Purge(ctx, instsvc.Slice{Kind: "s3", Ref: instsvc.SystemBucket}), "the system bucket is never purged")
}

func TestUnsignedOrFailingCallsAreErrors(t *testing.T) {
	f := newFake()
	srv := httptest.NewServer(f)
	defer srv.Close()
	b, err := New(Config{Endpoint: srv.URL, AdminKey: "WRONG", AdminSecret: "x", Slices: secrets{}})
	require.NoError(t, err)
	require.ErrorContains(t, b.Health(context.Background()), "built-in S3")
	_, err = New(Config{Endpoint: "not a url"})
	require.Error(t, err)
}
