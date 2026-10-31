package forward

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agents"
	"github.com/nebulactrl/nebulactrl/internal/instsvc"
	"github.com/nebulactrl/nebulactrl/internal/registries"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// ---- fakes ---------------------------------------------------------------------------------

type fakeSlices struct {
	mu      sync.Mutex
	servers map[uuid.UUID]uuid.UUID // server -> team
	teams   map[uuid.UUID]instsvc.TeamSlices
	keys    map[string]uuid.UUID
}

func (f *fakeSlices) TeamOfServer(_ context.Context, id uuid.UUID) (uuid.UUID, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if t, ok := f.servers[id]; ok {
		return t, nil
	}
	return uuid.Nil, instsvc.ErrNoSlice
}

func (f *fakeSlices) ForTeam(_ context.Context, id uuid.UUID) (instsvc.TeamSlices, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.teams[id], nil
}

func (f *fakeSlices) Secret(context.Context, instsvc.Slice) (string, error) { return "secret", nil }

func (f *fakeSlices) AccessKeyOwner(_ context.Context, k string) (uuid.UUID, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if t, ok := f.keys[k]; ok {
		return t, nil
	}
	return uuid.Nil, instsvc.ErrNoSlice
}

type fakeGate struct {
	mu       sync.Mutex
	admitted []int64
	teams    []uuid.UUID
	released atomic.Int32
	err      error
}

func (g *fakeGate) Admit(_ context.Context, team uuid.UUID, n int64) (func(), error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.err != nil {
		return nil, g.err
	}
	g.admitted = append(g.admitted, n)
	g.teams = append(g.teams, team)
	return func() { g.released.Add(1) }, nil
}

type fakeRegistry struct {
	ms       registries.ManagedSecrets
	readOnly atomic.Bool
	signed   atomic.Int32
}

func (r *fakeRegistry) SignRepoToken(_ context.Context, access ...registries.RepoAccess) (string, error) {
	r.signed.Add(1)
	return registries.SignRepoToken(r.ms, time.Now(), time.Minute, access...)
}

func (r *fakeRegistry) ReadOnly(context.Context) bool { return r.readOnly.Load() }

// upstream records what reached a fake instance service.
type upstream struct {
	*httptest.Server
	mu   sync.Mutex
	reqs []recorded
	h    http.HandlerFunc
}

type recorded struct {
	Method, Path, RawQuery, Host string
	Header                       http.Header
	Body                         string
}

func newUpstream(t *testing.T, h http.HandlerFunc) *upstream {
	t.Helper()
	u := &upstream{h: h}
	u.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		u.mu.Lock()
		u.reqs = append(u.reqs, recorded{r.Method, r.URL.Path, r.URL.RawQuery, r.Host, r.Header.Clone(), string(b)})
		u.mu.Unlock()
		if u.h != nil {
			u.h(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(u.Close)
	return u
}

func (u *upstream) got() []recorded {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]recorded(nil), u.reqs...)
}

// world is two teams, a server each, and a forwarder in front of fake services.
type world struct {
	f             *Forwarder
	slices        *fakeSlices
	gate          *fakeGate
	reg           *fakeRegistry
	s3, rg, lk    *upstream
	teamA, teamB  uuid.UUID
	srvA, srvB    uuid.UUID
	bucketA, bktB string
	keyA, keyB    string
	regMS         registries.ManagedSecrets
	ctx           context.Context
	cancel        context.CancelFunc
}

func newWorld(t *testing.T) *world {
	t.Helper()
	w := &world{teamA: uuid.New(), teamB: uuid.New(), srvA: uuid.New(), srvB: uuid.New(), keyA: "AKIATEAMAAAAAAAAAAAA", keyB: "AKIATEAMBBBBBBBBBBBB"}
	w.bucketA, w.bktB = "nc-"+w.teamA.String(), "nc-"+w.teamB.String()
	slice := func(team uuid.UUID, kind, ref, key string) *instsvc.Slice {
		return &instsvc.Slice{ID: uuid.New(), TeamID: team, Kind: kind, Ref: ref, AccessKey: key, State: "ready"}
	}
	w.slices = &fakeSlices{
		servers: map[uuid.UUID]uuid.UUID{w.srvA: w.teamA, w.srvB: w.teamB},
		teams: map[uuid.UUID]instsvc.TeamSlices{
			w.teamA: {S3: slice(w.teamA, "s3", w.bucketA, w.keyA), Registry: slice(w.teamA, "registry", w.teamA.String()+"/", ""), Loki: slice(w.teamA, "loki", w.teamA.String(), "")},
			w.teamB: {S3: slice(w.teamB, "s3", w.bktB, w.keyB), Registry: slice(w.teamB, "registry", w.teamB.String()+"/", ""), Loki: slice(w.teamB, "loki", w.teamB.String(), "")},
		},
		keys: map[string]uuid.UUID{w.keyA: w.teamA, w.keyB: w.teamB},
	}
	w.gate = &fakeGate{}
	ms, err := registries.GenerateManagedSecrets()
	require.NoError(t, err)
	w.regMS = ms
	w.reg = &fakeRegistry{ms: ms}
	w.s3 = newUpstream(t, nil)
	w.rg = newUpstream(t, nil)
	w.lk = newUpstream(t, nil)
	w.f, err = New(Deps{Slices: w.slices, Gate: w.gate, S3URL: w.s3.URL, RegistryURL: w.rg.URL, LokiURL: w.lk.URL, Registry: w.reg})
	require.NoError(t, err)
	w.f.cacheTTL = 0 // every request reads the slices afresh; TestSliceLookupsAreCached covers the cache
	w.ctx, w.cancel = context.WithCancel(context.Background())
	t.Cleanup(w.cancel)
	return w
}

// client returns an HTTP client whose every connection is a tunnel stream of server for service.
func (w *world) client(t *testing.T, server uuid.UUID, service string) *http.Client {
	t.Helper()
	return tunnelClient(t, w.ctx, w.f, server, service)
}

// tunnelClient returns an HTTP client whose every connection is a tunnel stream of
// server for service, served by f: what an agent's local listener does.
func tunnelClient(t *testing.T, ctx context.Context, f *Forwarder, server uuid.UUID, service string) *http.Client {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				_ = f.AcceptTunnel(ctx, agents.TunnelPeer{ServerID: server, Service: service}, c)
			}()
		}
	}()
	tr := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", ln.Addr().String())
	}, DisableKeepAlives: true}
	t.Cleanup(tr.CloseIdleConnections)
	return &http.Client{Transport: tr, Timeout: 60 * time.Second}
}

func authHeader(key string) string {
	return "AWS4-HMAC-SHA256 Credential=" + key + "/20261030/us-east-1/s3/aws4_request, SignedHeaders=host;x-amz-content-sha256, Signature=abcdef"
}

func (w *world) s3Do(t *testing.T, c *http.Client, method, path, key, body string, hdr map[string]string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, "http://127.0.0.1:18333"+path, strings.NewReader(body))
	require.NoError(t, err)
	req.Host = "127.0.0.1:18333"
	if key != "" {
		req.Header.Set("Authorization", authHeader(key))
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := c.Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func code(t *testing.T, resp *http.Response) string {
	t.Helper()
	b, _ := io.ReadAll(resp.Body)
	var e struct {
		Code string `xml:"Code"`
	}
	_ = xml.Unmarshal(b, &e)
	return e.Code
}

// ---- S3 --------------------------------------------------------------------------------------

func TestS3ForwardsTheTeamsOwnRequestsUnchangedWithHostPreserved(t *testing.T) {
	w := newWorld(t)
	c := w.client(t, w.srvA, agentprotocol.TunnelS3)

	resp := w.s3Do(t, c, http.MethodPut, "/"+w.bucketA+"/restic/data/ab/cd%20ef", w.keyA, "hello", map[string]string{
		"X-Amz-Content-Sha256": "UNSIGNED-PAYLOAD", "X-Forwarded-For": "6.6.6.6", "X-Forwarded-Host": "evil.example",
	})
	require.Equal(t, http.StatusOK, resp.StatusCode)

	got := w.s3.got()
	require.Len(t, got, 1)
	require.Equal(t, "127.0.0.1:18333", got[0].Host, "SigV4 covers Host: it must reach the service as the client signed it")
	require.Equal(t, "/"+w.bucketA+"/restic/data/ab/cd ef", got[0].Path)
	require.Equal(t, "hello", got[0].Body)
	require.Equal(t, authHeader(w.keyA), got[0].Header.Get("Authorization"), "the signature passes through untouched")
	require.Empty(t, got[0].Header.Get("X-Forwarded-For"), "client-supplied forwarding headers are dropped")
	require.Empty(t, got[0].Header.Get("X-Forwarded-Host"))
	require.Equal(t, []int64{5}, w.gate.admitted, "a PutObject is admitted against the quota by its length")
	require.Equal(t, []uuid.UUID{w.teamA}, w.gate.teams, "…of the team the stream belongs to")
	require.Eventually(t, func() bool { return w.gate.released.Load() == 1 }, time.Second, 10*time.Millisecond, "the reservation is released when the request ends")

	// Reads, listings, multipart and bulk delete go through; reads are not admitted.
	for _, tc := range []struct{ method, path, body string }{
		{http.MethodGet, "/" + w.bucketA + "/k", ""},
		{http.MethodHead, "/" + w.bucketA + "/k", ""},
		{http.MethodGet, "/" + w.bucketA + "?list-type=2&prefix=restic%2F&max-keys=1000", ""},
		{http.MethodHead, "/" + w.bucketA, ""},
		{http.MethodGet, "/" + w.bucketA + "?location", ""},
		{http.MethodPost, "/" + w.bucketA + "/big?uploads", ""},
		{http.MethodPut, "/" + w.bucketA + "/big?partNumber=1&uploadId=u1&x-id=UploadPart", "part"},
		{http.MethodPost, "/" + w.bucketA + "/big?uploadId=u1", "<xml/>"},
		{http.MethodDelete, "/" + w.bucketA + "/big?uploadId=u1", ""},
		{http.MethodGet, "/" + w.bucketA + "?uploads", ""},
		{http.MethodDelete, "/" + w.bucketA + "/k", ""},
		{http.MethodPost, "/" + w.bucketA + "?delete", "<Delete/>"},
	} {
		r := w.s3Do(t, c, tc.method, tc.path, w.keyA, tc.body, nil)
		require.Equal(t, http.StatusOK, r.StatusCode, "%s %s", tc.method, tc.path)
	}
	require.Equal(t, []int64{5, 4}, w.gate.admitted, "UploadPart is admitted too; nothing else is")
}

func TestS3AdmitsAChunkedUploadByItsDecodedLength(t *testing.T) {
	w := newWorld(t)
	c := w.client(t, w.srvA, agentprotocol.TunnelS3)
	body := strings.Repeat("x", 300) // the aws-chunked envelope is longer than the object
	resp := w.s3Do(t, c, http.MethodPut, "/"+w.bucketA+"/k", w.keyA, body, map[string]string{
		"X-Amz-Decoded-Content-Length": "200", "Content-Encoding": "aws-chunked", "X-Amz-Content-Sha256": "STREAMING-AWS4-HMAC-SHA256-PAYLOAD",
	})
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, []int64{200}, w.gate.admitted)
}

func TestS3RefusesEverythingOutsideTheTeamsSlice(t *testing.T) {
	w := newWorld(t)
	c := w.client(t, w.srvA, agentprotocol.TunnelS3)

	denied := func(name string, resp *http.Response) {
		t.Helper()
		require.Equal(t, http.StatusForbidden, resp.StatusCode, name)
		require.Equal(t, "AccessDenied", code(t, resp), name)
	}
	// Another team's bucket, with our own key.
	denied("other team's bucket", w.s3Do(t, c, http.MethodGet, "/"+w.bktB+"/k", w.keyA, "", nil))
	denied("other team's bucket (list)", w.s3Do(t, c, http.MethodGet, "/"+w.bktB+"?list-type=2", w.keyA, "", nil))
	// Another team's key on this team's stream, on this team's bucket and on its own.
	denied("other team's key, our bucket", w.s3Do(t, c, http.MethodGet, "/"+w.bucketA+"/k", w.keyB, "", nil))
	denied("other team's key, its bucket", w.s3Do(t, c, http.MethodGet, "/"+w.bktB+"/k", w.keyB, "", nil))
	denied("unknown key", w.s3Do(t, c, http.MethodGet, "/"+w.bucketA+"/k", "AKIANOSUCHKEY", "", nil))
	denied("unsigned", w.s3Do(t, c, http.MethodGet, "/"+w.bucketA+"/k", "", "", nil))
	denied("legacy signature", w.s3Do(t, c, http.MethodGet, "/"+w.bucketA+"/k", "", "", map[string]string{"Authorization": "AWS " + w.keyA + ":sig"}))
	// The service root and bucket-level management.
	denied("list buckets", w.s3Do(t, c, http.MethodGet, "/", w.keyA, "", nil))
	denied("create bucket", w.s3Do(t, c, http.MethodPut, "/"+w.bucketA, w.keyA, "", nil))
	denied("delete bucket", w.s3Do(t, c, http.MethodDelete, "/"+w.bucketA, w.keyA, "", nil))
	denied("another bucket of ours", w.s3Do(t, c, http.MethodPut, "/"+w.bucketA+"-x", w.keyA, "", nil))
	denied("IAM action", w.s3Do(t, c, http.MethodPost, "/", w.keyA, "Action=CreateUser&UserName=x", nil))
	// Sub-resources: policy, acl, tagging, the native quota.
	for _, q := range []string{"policy", "acl", "tagging", "lifecycle", "versioning", "cors", "seaweedfs-quota", "retention", "versionId=1"} {
		denied("sub-resource "+q, w.s3Do(t, c, http.MethodPut, "/"+w.bucketA+"?"+q, w.keyA, "{}", nil))
		denied("object sub-resource "+q, w.s3Do(t, c, http.MethodGet, "/"+w.bucketA+"/k?"+q, w.keyA, "", nil))
	}
	// Dot segments that a server might clean into another bucket.
	denied("dot segments", w.s3Do(t, c, http.MethodGet, "/"+w.bucketA+"/../"+w.bktB+"/k", w.keyA, "", nil))
	// Presigned form of another team's key.
	denied("presigned, other team", w.s3Do(t, c, http.MethodGet, "/"+w.bktB+"/k?X-Amz-Credential="+w.keyB+"%2F20261030%2Fus-east-1%2Fs3%2Faws4_request&X-Amz-Signature=abc", "", "", nil))

	require.Empty(t, w.s3.got(), "none of it reached the service")
	require.Empty(t, w.gate.admitted)

	// A key owner lookup that errors (database down) is a refusal, not a pass.
	w.slices.mu.Lock()
	delete(w.slices.keys, w.keyA)
	w.slices.mu.Unlock()
	denied("key of a deleted team", w.s3Do(t, c, http.MethodGet, "/"+w.bucketA+"/k", w.keyA, "", nil))
}

func TestS3RefusesAKeyThatIsNoLongerTheSlicesKey(t *testing.T) {
	w := newWorld(t)
	c := w.client(t, w.srvA, agentprotocol.TunnelS3)
	// The key still belongs to the team but the slice was re-keyed.
	w.slices.mu.Lock()
	ts := w.slices.teams[w.teamA]
	ts.S3.AccessKey = "AKIAROTATEDKEYAAAAAA"
	w.slices.teams[w.teamA] = ts
	w.slices.mu.Unlock()
	resp := w.s3Do(t, c, http.MethodGet, "/"+w.bucketA+"/k", w.keyA, "", nil)
	require.Equal(t, http.StatusForbidden, resp.StatusCode)
}

func TestS3RefusesCopies(t *testing.T) {
	w := newWorld(t)
	c := w.client(t, w.srvA, agentprotocol.TunnelS3)
	// A copy has no size to admit and would duplicate bytes past the quota.
	resp := w.s3Do(t, c, http.MethodPut, "/"+w.bucketA+"/dst", w.keyA, "", map[string]string{"X-Amz-Copy-Source": "/" + w.bucketA + "/src"})
	require.Equal(t, http.StatusNotImplemented, resp.StatusCode)
	resp = w.s3Do(t, c, http.MethodPut, "/"+w.bucketA+"/dst?partNumber=1&uploadId=u", w.keyA, "", map[string]string{"X-Amz-Copy-Source": "/" + w.bktB + "/src"})
	require.Equal(t, http.StatusNotImplemented, resp.StatusCode)
	require.Empty(t, w.s3.got())
}

func TestS3QuotaFullRefusesWritesWithAPlainError(t *testing.T) {
	w := newWorld(t)
	c := w.client(t, w.srvA, agentprotocol.TunnelS3)
	w.gate.err = instsvc.ErrQuotaExceeded

	resp := w.s3Do(t, c, http.MethodPut, "/"+w.bucketA+"/k", w.keyA, "data", nil)
	require.Equal(t, http.StatusForbidden, resp.StatusCode)
	require.Equal(t, "built-in backup storage is full", resp.Header.Get("nebulactrl-quota-exceeded"))
	require.Equal(t, "QuotaExceeded", code(t, resp))

	// Reads and deletes are never blocked: nothing is deleted to make room, and nothing is locked in.
	for _, m := range []string{http.MethodGet, http.MethodHead, http.MethodDelete} {
		require.Equal(t, http.StatusOK, w.s3Do(t, c, m, "/"+w.bucketA+"/k", w.keyA, "", nil).StatusCode, m)
	}
	require.Len(t, w.s3.got(), 3)

	// Any other gate failure is a retriable 503, not a silent pass.
	w.gate.err = errors.New("database down")
	resp = w.s3Do(t, c, http.MethodPut, "/"+w.bucketA+"/k", w.keyA, "data", nil)
	require.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
	require.Len(t, w.s3.got(), 3)
}

func TestS3ServesNothingWhileTheSliceIsNotReady(t *testing.T) {
	w := newWorld(t)
	c := w.client(t, w.srvA, agentprotocol.TunnelS3)
	w.slices.mu.Lock()
	w.slices.teams[w.teamA].S3.State = "failed"
	w.slices.mu.Unlock()
	require.Equal(t, http.StatusServiceUnavailable, w.s3Do(t, c, http.MethodGet, "/"+w.bucketA+"/k", w.keyA, "", nil).StatusCode)

	// A "full" slice still serves (reads, deletes).
	w.slices.mu.Lock()
	w.slices.teams[w.teamA].S3.State = "full"
	w.slices.mu.Unlock()
	require.Equal(t, http.StatusOK, w.s3Do(t, c, http.MethodGet, "/"+w.bucketA+"/k", w.keyA, "", nil).StatusCode)
}

func TestAServerOfNoTeamGetsNoStream(t *testing.T) {
	w := newWorld(t)
	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()
	err := w.f.AcceptTunnel(w.ctx, agents.TunnelPeer{ServerID: uuid.New(), Service: agentprotocol.TunnelS3}, c2)
	require.Error(t, err)
	err = w.f.AcceptTunnel(w.ctx, agents.TunnelPeer{ServerID: w.srvA, Service: "ssh"}, c2)
	require.Error(t, err)

	off, err := New(Deps{Slices: w.slices, Gate: w.gate, S3URL: w.s3.URL, Registry: w.reg})
	require.NoError(t, err)
	require.Error(t, off.AcceptTunnel(w.ctx, agents.TunnelPeer{ServerID: w.srvA, Service: agentprotocol.TunnelLoki}, c2), "a service without a URL is off")
}

func TestSliceLookupsAreCachedBrieflyAndRefreshed(t *testing.T) {
	w := newWorld(t)
	w.f.cacheTTL = time.Hour
	c := w.client(t, w.srvA, agentprotocol.TunnelS3)
	require.Equal(t, http.StatusOK, w.s3Do(t, c, http.MethodGet, "/"+w.bucketA+"/k", w.keyA, "", nil).StatusCode)
	w.slices.mu.Lock()
	delete(w.slices.keys, w.keyA)
	w.slices.mu.Unlock()
	require.Equal(t, http.StatusOK, w.s3Do(t, c, http.MethodGet, "/"+w.bucketA+"/k", w.keyA, "", nil).StatusCode, "inside the TTL a lookup is served from memory")
	w.f.cacheTTL = 0
	w.f.owners = ttlCache[string, uuid.UUID]{}
	require.Equal(t, http.StatusForbidden, w.s3Do(t, c, http.MethodGet, "/"+w.bucketA+"/k", w.keyA, "", nil).StatusCode, "after it the removal is seen")
}

// ---- registry ---------------------------------------------------------------------------------

func (w *world) regDo(t *testing.T, c *http.Client, method, path, body string, hdr map[string]string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, "http://127.0.0.1:18500"+path, strings.NewReader(body))
	require.NoError(t, err)
	req.Host = "127.0.0.1:18500"
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := c.Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

// tokenAccessOf decodes the access claim of the bearer token the registry received.
func tokenAccessOf(t *testing.T, r recorded) []map[string]any {
	t.Helper()
	tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	require.True(t, ok, "the registry got no bearer token: %q", r.Header.Get("Authorization"))
	raw, err := base64.RawURLEncoding.DecodeString(strings.Split(tok, ".")[1])
	require.NoError(t, err)
	var claims struct {
		Access []map[string]any `json:"access"`
		Aud    any              `json:"aud"`
	}
	require.NoError(t, json.Unmarshal(raw, &claims))
	require.Equal(t, registries.BuiltinService, claims.Aud)
	return claims.Access
}

func TestRegistrySignsAnExactNameTokenForTheTeamsRepository(t *testing.T) {
	w := newWorld(t)
	c := w.client(t, w.srvA, agentprotocol.TunnelRegistry)
	repo := w.teamA.String() + "/shop-web"

	// The ping needs a token that only authenticates.
	require.Equal(t, http.StatusOK, w.regDo(t, c, http.MethodGet, "/v2/", "", nil).StatusCode)
	require.Empty(t, tokenAccessOf(t, w.rg.got()[0]))

	for _, tc := range []struct {
		method, path string
		want         []string
	}{
		{http.MethodGet, "/v2/" + repo + "/manifests/latest", []string{"pull"}},
		{http.MethodHead, "/v2/" + repo + "/blobs/sha256:" + strings.Repeat("a", 64), []string{"pull"}},
		{http.MethodGet, "/v2/" + repo + "/tags/list", []string{"pull"}},
		{http.MethodPost, "/v2/" + repo + "/blobs/uploads/", []string{"pull", "push"}},
		{http.MethodPatch, "/v2/" + repo + "/blobs/uploads/0f1c2d3e-aaaa-bbbb-cccc-1234567890ab", []string{"pull", "push"}},
		{http.MethodPut, "/v2/" + repo + "/manifests/v1", []string{"pull", "push"}},
		{http.MethodDelete, "/v2/" + repo + "/manifests/sha256:" + strings.Repeat("b", 64), []string{"delete"}},
	} {
		before := len(w.rg.got())
		resp := w.regDo(t, c, tc.method, tc.path, "", nil)
		require.Equal(t, http.StatusOK, resp.StatusCode, "%s %s", tc.method, tc.path)
		got := w.rg.got()
		require.Len(t, got, before+1)
		acc := tokenAccessOf(t, got[before])
		require.Len(t, acc, 1, tc.path)
		require.Equal(t, "repository", acc[0]["type"])
		require.Equal(t, repo, acc[0]["name"], "the token names exactly the repository of the request")
		require.ElementsMatch(t, tc.want, acc[0]["actions"], "%s %s", tc.method, tc.path)
		require.Equal(t, "127.0.0.1:18500", got[before].Host, "Host is preserved so Location URLs stay on the client's address")
	}
	require.Zero(t, w.reg.readOnly.Load())
}

func TestRegistryRefusesRepositoriesOutsideTheTeamPrefix(t *testing.T) {
	w := newWorld(t)
	c := w.client(t, w.srvA, agentprotocol.TunnelRegistry)
	other := w.teamB.String() + "/shop-web"
	mine := w.teamA.String() + "/shop-web"
	digest := "sha256:" + strings.Repeat("c", 64)

	denied := func(name, method, path string) {
		t.Helper()
		resp := w.regDo(t, c, method, path, "", nil)
		require.Equal(t, http.StatusForbidden, resp.StatusCode, name)
		var e struct {
			Errors []struct{ Code string } `json:"errors"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&e), name)
		require.Equal(t, "DENIED", e.Errors[0].Code, name)
	}
	denied("another team's repository", http.MethodGet, "/v2/"+other+"/manifests/latest")
	denied("push to another team's repository", http.MethodPut, "/v2/"+other+"/manifests/latest")
	denied("blob of another team", http.MethodGet, "/v2/"+other+"/blobs/"+digest)
	denied("upload to another team", http.MethodPost, "/v2/"+other+"/blobs/uploads/")
	denied("delete of another team", http.MethodDelete, "/v2/"+other+"/manifests/"+digest)
	denied("no prefix at all", http.MethodGet, "/v2/shop-web/manifests/latest")
	denied("the prefix without a name", http.MethodGet, "/v2/"+w.teamA.String()+"/manifests/latest")
	denied("prefix lookalike", http.MethodGet, "/v2/"+w.teamA.String()+"x/shop/manifests/latest")
	denied("traversal", http.MethodGet, "/v2/"+mine+"/../"+other+"/manifests/latest")
	denied("encoded traversal", http.MethodGet, "/v2/"+mine+"/%2e%2e/"+other+"/manifests/latest")
	denied("encoded slash", http.MethodGet, "/v2/"+w.teamA.String()+"%2Fshop/manifests/latest")
	denied("uppercase", http.MethodGet, "/v2/"+w.teamA.String()+"/Shop/manifests/latest")
	denied("the catalog", http.MethodGet, "/v2/_catalog")
	denied("the catalog with paging", http.MethodGet, "/v2/_catalog?n=10")
	denied("the team's own prefix as catalog", http.MethodGet, "/v2/"+w.teamA.String()+"/_catalog")
	denied("a route that does not exist", http.MethodGet, "/v2/"+mine+"/admin")
	denied("unknown method", http.MethodTrace, "/v2/"+mine+"/manifests/latest")
	// Cross-repository mount: the source must be ours as well.
	denied("mount from another team", http.MethodPost, "/v2/"+mine+"/blobs/uploads/?mount="+digest+"&from="+other)
	denied("mount from outside any prefix", http.MethodPost, "/v2/"+mine+"/blobs/uploads/?mount="+digest+"&from=library/alpine")
	denied("mount without a source", http.MethodPost, "/v2/"+mine+"/blobs/uploads/?mount="+digest)
	denied("from on a read", http.MethodGet, "/v2/"+mine+"/manifests/latest?from="+other)

	require.Empty(t, w.rg.got(), "none of it reached the registry")
	require.Zero(t, w.reg.signed.Load(), "and no token was signed for any of it")
}

func TestRegistryMountFromTheTeamsOwnRepositoryGetsAPullGrantOnTheSource(t *testing.T) {
	w := newWorld(t)
	c := w.client(t, w.srvA, agentprotocol.TunnelRegistry)
	mine, src := w.teamA.String()+"/web", w.teamA.String()+"/base"
	digest := "sha256:" + strings.Repeat("d", 64)
	resp := w.regDo(t, c, http.MethodPost, "/v2/"+mine+"/blobs/uploads/?mount="+digest+"&from="+src, "", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	acc := tokenAccessOf(t, w.rg.got()[0])
	require.Len(t, acc, 2)
	require.Equal(t, mine, acc[0]["name"])
	require.ElementsMatch(t, []any{"pull", "push"}, acc[0]["actions"])
	require.Equal(t, src, acc[1]["name"])
	require.Equal(t, []any{"pull"}, acc[1]["actions"], "the source is only read")
}

func TestRegistryReplacesClientAuthorizationAndHidesTheChallenge(t *testing.T) {
	w := newWorld(t)
	w.rg.h = func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("WWW-Authenticate", `Bearer realm="http://registry.invalid/token",service="x"`)
		rw.WriteHeader(http.StatusUnauthorized)
	}
	c := w.client(t, w.srvA, agentprotocol.TunnelRegistry)
	repo := w.teamA.String() + "/web"
	resp := w.regDo(t, c, http.MethodGet, "/v2/"+repo+"/manifests/latest", "", map[string]string{"Authorization": "Bearer stolen-token"})
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	require.Empty(t, resp.Header.Get("Www-Authenticate"), "a challenge would send the Docker daemon to a token endpoint that does not exist")
	require.NotContains(t, w.rg.got()[0].Header.Get("Authorization"), "stolen-token", "a client-supplied credential is never forwarded")
}

func TestRegistryWritesStopInTheGarbageCollectionWindowReadsDoNot(t *testing.T) {
	w := newWorld(t)
	c := w.client(t, w.srvA, agentprotocol.TunnelRegistry)
	repo := w.teamA.String() + "/web"
	w.reg.readOnly.Store(true)

	resp := w.regDo(t, c, http.MethodPut, "/v2/"+repo+"/manifests/latest", "{}", nil)
	require.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
	require.NotEmpty(t, resp.Header.Get("Retry-After"))
	require.Equal(t, http.StatusServiceUnavailable, w.regDo(t, c, http.MethodPost, "/v2/"+repo+"/blobs/uploads/", "", nil).StatusCode)
	require.Equal(t, http.StatusServiceUnavailable, w.regDo(t, c, http.MethodDelete, "/v2/"+repo+"/manifests/sha256:"+strings.Repeat("e", 64), "", nil).StatusCode)
	require.Equal(t, http.StatusOK, w.regDo(t, c, http.MethodGet, "/v2/"+repo+"/manifests/latest", "", nil).StatusCode, "pulls work during the window")
	require.Len(t, w.rg.got(), 1)
}

func TestRegistryCountsWritesInFlight(t *testing.T) {
	w := newWorld(t)
	started, release := make(chan struct{}), make(chan struct{})
	w.rg.h = func(rw http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			close(started)
			<-release
		}
	}
	c := w.client(t, w.srvA, agentprotocol.TunnelRegistry)
	repo := w.teamA.String() + "/web"
	done := make(chan struct{})
	go func() {
		defer close(done)
		resp, err := c.Do(mustReq(t, http.MethodPut, "http://127.0.0.1:18500/v2/"+repo+"/manifests/latest"))
		if err == nil {
			resp.Body.Close()
		}
	}()
	<-started
	require.Equal(t, 1, w.f.RegistryWrites(), "the garbage-collection window waits for this to reach zero")
	require.Equal(t, http.StatusOK, w.regDo(t, c, http.MethodGet, "/v2/"+repo+"/manifests/latest", "", nil).StatusCode)
	require.Equal(t, 1, w.f.RegistryWrites(), "reads are not counted")
	close(release)
	<-done
	require.Eventually(t, func() bool { return w.f.RegistryWrites() == 0 }, time.Second, 5*time.Millisecond)
}

func mustReq(t *testing.T, method, url string) *http.Request {
	t.Helper()
	r, err := http.NewRequest(method, url, nil)
	require.NoError(t, err)
	return r
}

func TestRegistryPathParsingKeepsTheLongestName(t *testing.T) {
	for in, want := range map[string]string{
		"t/app/manifests/latest":                                       "t/app",
		"t/blobs/manifests/latest":                                     "t/blobs", // a repository called "blobs"
		"t/app/blobs/uploads/":                                         "t/app",
		"t/app/blobs/uploads/abc-123":                                  "t/app",
		"t/blobs/blobs/sha256:" + strings.Repeat("a", 64):              "t/blobs",
		"t/a/b/c/tags/list":                                            "t/a/b/c",
		"t/app/referrers/sha256:" + strings.Repeat("a", 64):            "t/app",
		"t/manifests/blobs/sha256:" + strings.Repeat("a", 64):          "t/manifests",
		"t/app/manifests/sha256:" + strings.Repeat("a", 64):            "t/app",
		"t/tags/tags/list":                                             "t/tags",
		"t/uploads/blobs/uploads/00000000-0000-0000-0000-000000000000": "t/uploads",
	} {
		r, ok := parseRegistryPath(in)
		require.True(t, ok, in)
		require.Equal(t, want, r.name, in)
	}
	for _, in := range []string{"_catalog", "t/app", "t/app/manifests", "t/app/manifests/", "t/app/blobs/notadigest", "t/app/tags/list/x", "t/app/other/x"} {
		_, ok := parseRegistryPath(in)
		require.False(t, ok, in)
	}
}

// ---- Loki -------------------------------------------------------------------------------------

func (w *world) lokiDo(t *testing.T, c *http.Client, method, path, body string, hdr map[string]string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, "http://127.0.0.1:18100"+path, strings.NewReader(body))
	require.NoError(t, err)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := c.Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func TestLokiTenantIsTheTeamWhateverTheClientSends(t *testing.T) {
	w := newWorld(t)
	c := w.client(t, w.srvA, agentprotocol.TunnelLoki)

	resp := w.lokiDo(t, c, http.MethodPost, "/loki/api/v1/push", `{"streams":[]}`, map[string]string{
		"X-Scope-OrgID": w.teamB.String(), "x-scope-orgid": "fake", "X-Forwarded-For": "1.2.3.4", "Authorization": "Basic Zm9vOmJhcg==", "Content-Type": "application/json",
	})
	require.Equal(t, http.StatusOK, resp.StatusCode)
	got := w.lk.got()
	require.Len(t, got, 1)
	require.Equal(t, []string{w.teamA.String()}, got[0].Header.Values("X-Scope-Orgid"), "exactly one tenant header: the stream's team")
	require.Empty(t, got[0].Header.Get("X-Forwarded-For"))
	require.Empty(t, got[0].Header.Get("Authorization"))
	require.Equal(t, `{"streams":[]}`, got[0].Body)

	// The other team's stream gets its own tenant.
	cb := w.client(t, w.srvB, agentprotocol.TunnelLoki)
	require.Equal(t, http.StatusOK, w.lokiDo(t, cb, http.MethodPost, "/loki/api/v1/push", `{}`, map[string]string{"X-Scope-OrgID": w.teamA.String()}).StatusCode)
	require.Equal(t, []string{w.teamB.String()}, w.lk.got()[1].Header.Values("X-Scope-Orgid"))
}

func TestLokiServesOnlyPushes(t *testing.T) {
	w := newWorld(t)
	c := w.client(t, w.srvA, agentprotocol.TunnelLoki)
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/loki/api/v1/query_range?query=%7Bserver%3D~%22.%2B%22%7D"},
		{http.MethodGet, "/loki/api/v1/push"},
		{http.MethodPost, "/loki/api/v1/delete?query=%7Bserver%3D~%22.%2B%22%7D&start=0"},
		{http.MethodPost, "/loki/api/v1/push/extra"},
		{http.MethodGet, "/ready"},
		{http.MethodGet, "/config"},
		{http.MethodPost, "/ingester/flush"},
	} {
		resp := w.lokiDo(t, c, tc.method, tc.path, "x", nil)
		require.Equal(t, http.StatusForbidden, resp.StatusCode, "%s %s", tc.method, tc.path)
	}
	big := strings.Repeat("x", MaxLokiPush+1)
	// The refusal comes before the body is read; a client still sending may see the stream close instead of the 413.
	if resp, err := c.Post("http://127.0.0.1:18100/loki/api/v1/push", "application/json", strings.NewReader(big)); err == nil {
		resp.Body.Close()
		require.Equal(t, http.StatusRequestEntityTooLarge, resp.StatusCode)
	}
	require.Empty(t, w.lk.got())
}

func TestLokiNeedsTheTeamsSlice(t *testing.T) {
	w := newWorld(t)
	c := w.client(t, w.srvA, agentprotocol.TunnelLoki)
	w.slices.mu.Lock()
	w.slices.teams[w.teamA] = instsvc.TeamSlices{}
	w.slices.mu.Unlock()
	require.Equal(t, http.StatusServiceUnavailable, w.lokiDo(t, c, http.MethodPost, "/loki/api/v1/push", "{}", nil).StatusCode)
	require.Empty(t, w.lk.got())
}

// ---- streaming --------------------------------------------------------------------------------

// A large upload is streamed, not buffered: the service sees bytes while the client still sends.
func TestS3UploadsAreStreamedNotBuffered(t *testing.T) {
	w := newWorld(t)
	firstChunk := make(chan struct{})
	w.s3.h = nil
	w.s3.Close()
	w.s3 = &upstream{}
	w.s3.Server = httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 1024)
		n, _ := r.Body.Read(buf)
		if n > 0 {
			close(firstChunk)
		}
		_, _ = io.Copy(io.Discard, r.Body)
		rw.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(w.s3.Close)
	var err error
	w.f, err = New(Deps{Slices: w.slices, Gate: w.gate, S3URL: w.s3.URL, Registry: w.reg})
	require.NoError(t, err)
	c := w.client(t, w.srvA, agentprotocol.TunnelS3)

	pr, pw := io.Pipe()
	req, err := http.NewRequest(http.MethodPut, "http://127.0.0.1:18333/"+w.bucketA+"/big", pr)
	require.NoError(t, err)
	req.Host = "127.0.0.1:18333"
	req.ContentLength = 64 << 20
	req.Header.Set("Authorization", authHeader(w.keyA))
	done := make(chan error, 1)
	go func() {
		resp, err := c.Do(req)
		if err == nil {
			resp.Body.Close()
		}
		done <- err
	}()
	_, err = pw.Write(make([]byte, 64<<10))
	require.NoError(t, err)
	select {
	case <-firstChunk:
	case <-time.After(5 * time.Second):
		t.Fatal("the service saw no bytes while the client was still uploading: the body is buffered")
	}
	rest := make([]byte, 64<<20-64<<10)
	_, err = pw.Write(rest)
	require.NoError(t, err)
	pw.Close()
	require.NoError(t, <-done)
	require.Equal(t, []int64{64 << 20}, w.gate.admitted)
}
