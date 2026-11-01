package forward

import (
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agents"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Object names outside the URL path are held to the dot-segment rule.
func TestS3RefusesObjectNamesThatWalkOutOfTheBucket(t *testing.T) {
	w := newWorld(t)
	c := w.client(t, w.srvA, agentprotocol.TunnelS3)
	denied := func(name string, resp *http.Response) {
		t.Helper()
		require.Equal(t, http.StatusForbidden, resp.StatusCode, name)
		require.Equal(t, "AccessDenied", code(t, resp), name)
	}
	del := func(keys ...string) string {
		var b strings.Builder
		b.WriteString("<Delete>")
		for _, k := range keys {
			b.WriteString("<Object><Key>" + k + "</Key></Object>")
		}
		return b.String() + "</Delete>"
	}
	bad := []string{
		"../" + w.bktB + "/k",
		"a/../../" + w.bktB + "/k",
		"..",
		".",
		"a/./b",
		"/" + w.bktB + "/k",
		"a//b",
		`a\..\b`,
		"../",
	}
	for _, k := range bad {
		denied("delete key "+k, w.s3Do(t, c, http.MethodPost, "/"+w.bucketA+"?delete", w.keyA, del(k), nil))
	}
	denied("one bad key among good ones", w.s3Do(t, c, http.MethodPost, "/"+w.bucketA+"?delete", w.keyA, del("ok/1", "ok/2", "../"+w.bktB+"/k"), nil))
	denied("key in CDATA", w.s3Do(t, c, http.MethodPost, "/"+w.bucketA+"?delete", w.keyA, `<Delete><Object><Key><![CDATA[../`+w.bktB+`/k]]></Key></Object></Delete>`, nil))
	denied("key split by an entity", w.s3Do(t, c, http.MethodPost, "/"+w.bucketA+"?delete", w.keyA, `<Delete><Object><Key>&#46;&#46;/`+w.bktB+`/k</Key></Object></Delete>`, nil))
	denied("not xml", w.s3Do(t, c, http.MethodPost, "/"+w.bucketA+"?delete", w.keyA, "<Delete><Object><Key>a", nil))
	tooMany := make([]string, maxDeleteKeys+1)
	for i := range tooMany {
		tooMany[i] = "k"
	}
	denied("more than 1000 keys", w.s3Do(t, c, http.MethodPost, "/"+w.bucketA+"?delete", w.keyA, del(tooMany...), nil))
	denied("body over the cap", w.s3Do(t, c, http.MethodPost, "/"+w.bucketA+"?delete", w.keyA, "<Delete>"+strings.Repeat(" ", maxDeleteBody)+"</Delete>", nil))
	denied("aws-chunked delete", w.s3Do(t, c, http.MethodPost, "/"+w.bucketA+"?delete", w.keyA, del("ok"), map[string]string{"X-Amz-Content-Sha256": "STREAMING-UNSIGNED-PAYLOAD-TRAILER"}))

	for _, q := range []string{
		"prefix=../" + w.bktB + "/",
		"prefix=" + url.QueryEscape("../"+w.bktB+"/"),
		"prefix=a/../b",
		"prefix=/x",
		"prefix=..",
		"start-after=../" + w.bktB + "/k",
		"marker=../" + w.bktB + "/k",
		"marker=a%00b",
		"prefix=ok&prefix=../" + w.bktB + "/",
		"continuation-token=" + url.QueryEscape("Li4vbmMtb3RoZXIvaw=="), // base64 of "../nc-other/k"
		"continuation-token=..",
	} {
		denied("list "+q, w.s3Do(t, c, http.MethodGet, "/"+w.bucketA+"?list-type=2&"+q, w.keyA, "", nil))
	}
	for _, q := range []string{
		"uploads&key-marker=../" + w.bktB + "/k",
		"uploads&upload-id-marker=../x",
		"uploads&upload-id-marker=a/b",
	} {
		denied("list uploads "+q, w.s3Do(t, c, http.MethodGet, "/"+w.bucketA+"?"+q, w.keyA, "", nil))
	}
	for _, id := range []string{"../x", "a/b", "", "a b", strings.Repeat("a", 129), "%2e%2e"} {
		denied("uploadId "+id, w.s3Do(t, c, http.MethodDelete, "/"+w.bucketA+"/big?uploadId="+url.QueryEscape(id), w.keyA, "", nil))
		denied("uploadId (part) "+id, w.s3Do(t, c, http.MethodPut, "/"+w.bucketA+"/big?partNumber=1&uploadId="+url.QueryEscape(id), w.keyA, "x", nil))
	}
	require.Empty(t, w.s3.got(), "none of it reached the service")

	// What a backup actually sends still passes, and the delete body arrives byte for byte.
	body := del("restic/data/ab/cd ef", "daily/2026-10-30.dump")
	for _, tc := range []struct{ method, path, body string }{
		{http.MethodPost, "/" + w.bucketA + "?delete", body},
		{http.MethodGet, "/" + w.bucketA + "?list-type=2&prefix=restic%2Fdata%2F&delimiter=%2F&start-after=restic%2Fa", ""},
		{http.MethodGet, "/" + w.bucketA + "?list-type=2&prefix=&continuation-token=" + url.QueryEscape("cmVzdGljL2E="), ""},
		{http.MethodGet, "/" + w.bucketA + "?uploads&key-marker=big%2Fa&upload-id-marker=0a1b-2c3d", ""},
		{http.MethodDelete, "/" + w.bucketA + "/big?uploadId=0a1b-2c3d_4e", ""},
	} {
		r := w.s3Do(t, c, tc.method, tc.path, w.keyA, tc.body, nil)
		require.Equal(t, http.StatusOK, r.StatusCode, "%s %s", tc.method, tc.path)
	}
	got := w.s3.got()
	require.Len(t, got, 5)
	require.Equal(t, body, got[0].Body, "the checked body is what the service receives")
}

// The quota gate counts what is really sent.
func TestS3QuotaCountsTheBytesActuallySent(t *testing.T) {
	w := newWorld(t)
	c := w.client(t, w.srvA, agentprotocol.TunnelS3)

	// A decoded length on a body that is not aws-chunked is not believed.
	r := w.s3Do(t, c, http.MethodPut, "/"+w.bucketA+"/k", w.keyA, strings.Repeat("x", 1000), map[string]string{
		"X-Amz-Decoded-Content-Length": "0", "X-Amz-Content-Sha256": "UNSIGNED-PAYLOAD",
	})
	require.Equal(t, http.StatusBadRequest, r.StatusCode)
	r = w.s3Do(t, c, http.MethodPut, "/"+w.bucketA+"/k", w.keyA, strings.Repeat("x", 1000), map[string]string{"X-Amz-Decoded-Content-Length": "0"})
	require.Equal(t, http.StatusBadRequest, r.StatusCode)
	// aws-chunked without a decoded length, or with a body far larger than it.
	r = w.s3Do(t, c, http.MethodPut, "/"+w.bucketA+"/k", w.keyA, strings.Repeat("x", 1000), map[string]string{"X-Amz-Content-Sha256": "STREAMING-UNSIGNED-PAYLOAD-TRAILER"})
	require.Equal(t, http.StatusBadRequest, r.StatusCode)
	r = w.s3Do(t, c, http.MethodPut, "/"+w.bucketA+"/k", w.keyA, strings.Repeat("x", 64<<10), map[string]string{
		"X-Amz-Decoded-Content-Length": "0", "X-Amz-Content-Sha256": "STREAMING-UNSIGNED-PAYLOAD-TRAILER",
	})
	require.Equal(t, http.StatusBadRequest, r.StatusCode)
	require.Empty(t, w.s3.got())
	require.Empty(t, w.gate.admitted)

	// A plain body is admitted at its Content-Length.
	r = w.s3Do(t, c, http.MethodPut, "/"+w.bucketA+"/k", w.keyA, strings.Repeat("x", 1000), map[string]string{"X-Amz-Content-Sha256": "UNSIGNED-PAYLOAD"})
	require.Equal(t, http.StatusOK, r.StatusCode)
	require.Equal(t, []int64{1000}, w.gate.admitted)
}

func TestS3RefusesAWriteWithoutAContentLength(t *testing.T) {
	w := newWorld(t)
	c := w.client(t, w.srvA, agentprotocol.TunnelS3)
	req, err := http.NewRequest(http.MethodPut, "http://127.0.0.1:18333/"+w.bucketA+"/k", io.NopCloser(strings.NewReader(strings.Repeat("x", 4096))))
	require.NoError(t, err) // an opaque body: the client sends Transfer-Encoding: chunked
	req.Host = "127.0.0.1:18333"
	req.Header.Set("Authorization", authHeader(w.keyA))
	req.Header.Set("X-Amz-Decoded-Content-Length", "0")
	req.Header.Set("X-Amz-Content-Sha256", "STREAMING-UNSIGNED-PAYLOAD-TRAILER")
	resp, err := c.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	require.Empty(t, w.s3.got())
	require.Empty(t, w.gate.admitted)
}

func TestLimitBodyStopsAtTheDeclaredSize(t *testing.T) {
	b := &limitBody{rc: io.NopCloser(strings.NewReader(strings.Repeat("x", 100))), left: 60}
	got, err := io.ReadAll(b)
	require.ErrorIs(t, err, errBodyTooLarge)
	require.Len(t, got, 60, "not one byte beyond the limit is delivered")
	require.True(t, b.exceeded)

	b = &limitBody{rc: io.NopCloser(strings.NewReader(strings.Repeat("x", 60))), left: 60}
	got, err = io.ReadAll(b)
	require.NoError(t, err)
	require.Len(t, got, 60, "a body of exactly the limit passes")
}

func TestWriteSizeAdmitsStreamingBodiesWithRoomForTheFraming(t *testing.T) {
	req := func(cl int64, h map[string]string) *http.Request {
		r, _ := http.NewRequest(http.MethodPut, "http://x/b/k", nil)
		r.ContentLength = cl
		for k, v := range h {
			r.Header.Set(k, v)
		}
		return r
	}
	streaming := map[string]string{"X-Amz-Content-Sha256": "STREAMING-AWS4-HMAC-SHA256-PAYLOAD", "X-Amz-Decoded-Content-Length": "1000000"}
	n, limit, err := writeSize(req(1_020_000, streaming))
	require.NoError(t, err)
	require.EqualValues(t, 1_000_000, n)
	require.Greater(t, limit, int64(1_020_000))
	_, _, err = writeSize(req(2_000_000, streaming))
	require.Error(t, err, "twice the declared size is not framing")
}

// serveRaw runs the forwarder on one end of a pipe, as the hub does for a tunnel
// stream, and returns the client end plus a channel that closes when
// AcceptTunnel returns (the moment the hub frees the stream's slot).
func serveRaw(t *testing.T, w *world, service string) (net.Conn, <-chan struct{}) {
	t.Helper()
	cli, srv := net.Pipe()
	ended := make(chan struct{})
	go func() {
		defer close(ended)
		_ = w.f.AcceptTunnel(w.ctx, agents.TunnelPeer{ServerID: w.srvA, Service: service}, srv)
		srv.Close()
	}()
	t.Cleanup(func() { cli.Close() })
	go func() { _, _ = io.Copy(io.Discard, cli) }() // whatever the service answers
	return cli, ended
}

func rawPut(w *world, contentLength int) string {
	return "PUT /" + w.bucketA + "/slow HTTP/1.1\r\nHost: 127.0.0.1:18333\r\nAuthorization: " + authHeader(w.keyA) +
		"\r\nX-Amz-Content-Sha256: UNSIGNED-PAYLOAD\r\nContent-Length: " + strconv.Itoa(contentLength) + "\r\n\r\n"
}

// A body that stops arriving ends the request and frees the stream.
func TestS3BodyThatStopsArrivingIsCutOffAndFreesTheStream(t *testing.T) {
	w := newWorld(t)
	w.f.bodyIdle = 300 * time.Millisecond
	cli, ended := serveRaw(t, w, agentprotocol.TunnelS3)

	_, err := io.WriteString(cli, rawPut(w, 100)+"0123456789") // ten of the hundred bytes, then silence
	require.NoError(t, err)
	select {
	case <-ended:
	case <-time.After(10 * time.Second):
		t.Fatal("a stalled body held the stream open")
	}
	for _, r := range w.s3.got() {
		require.NotEqual(t, strings.Repeat("x", 100), r.Body, "the stalled upload is not stored")
	}
}

// The idle timeout is not a total timeout: a body that keeps arriving, however
// long it takes overall, is served.
func TestS3BodyThatKeepsArrivingIsNotCutOff(t *testing.T) {
	w := newWorld(t)
	w.f.bodyIdle = 400 * time.Millisecond
	cli, ended := serveRaw(t, w, agentprotocol.TunnelS3)

	_, err := io.WriteString(cli, rawPut(w, 10)+"0")
	require.NoError(t, err)
	for i := 1; i < 10; i++ { // 9 more bytes, 150 ms apart: 1.35 s in all, over three idle periods
		time.Sleep(150 * time.Millisecond)
		_, err = io.WriteString(cli, "x")
		require.NoError(t, err)
	}
	require.Eventually(t, func() bool { return len(w.s3.got()) == 1 }, 5*time.Second, 20*time.Millisecond)
	require.Equal(t, "0xxxxxxxxx", w.s3.got()[0].Body)
	select {
	case <-ended:
		t.Fatal("a finished request closed a keep-alive stream")
	default:
	}
}
