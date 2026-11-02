package docker

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// muxed encodes frames the way the daemon multiplexes stdout/stderr of a
// container without a TTY.
func muxed(t *testing.T, frames ...struct {
	typ  stdcopy.StdType
	text string
},
) []byte {
	t.Helper()
	var buf bytes.Buffer
	for _, f := range frames {
		hdr := [8]byte{byte(f.typ)}
		binary.BigEndian.PutUint32(hdr[4:], uint32(len(f.text)))
		buf.Write(hdr[:])
		buf.WriteString(f.text)
	}
	return buf.Bytes()
}

type frame = struct {
	typ  stdcopy.StdType
	text string
}

func stdoutFrame(s string) frame { return frame{stdcopy.Stdout, s} }
func stderrFrame(s string) frame { return frame{stdcopy.Stderr, s} }

func fakeInspectTTY(d *fakeDaemon, tty bool) {
	d.onJSON("GET /containers/c1/json", 200, obj{"Id": "c1", "Config": obj{"Tty": tty}})
}

func TestContainerLogsDemultiplexesStdoutAndStderr(t *testing.T) {
	d, e := newFakeDaemon(t)
	fakeInspectTTY(d, false)
	payload := muxed(t,
		stdoutFrame("2025-01-02T03:04:05.000000001Z hello\n"),
		stderrFrame("2025-01-02T03:04:06Z oops\n"),
	)
	d.on("GET /containers/c1/logs", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(payload) })

	rc, err := e.ContainerLogs(context.Background(), "c1", LogOptions{Tail: 50, Follow: true, Since: time.Unix(1700000000, 123)})
	require.NoError(t, err)
	defer rc.Close()
	got, err := io.ReadAll(rc)
	require.NoError(t, err)
	assert.Equal(t, "2025-01-02T03:04:05.000000001Z hello\n2025-01-02T03:04:06Z oops\n", string(got),
		"the frame headers are gone and both streams are interleaved in order")

	q := d.only("GET", "/containers/c1/logs").Query
	assert.Equal(t, "1", q.Get("stdout"))
	assert.Equal(t, "1", q.Get("stderr"))
	assert.Equal(t, "1", q.Get("timestamps"))
	assert.Equal(t, "1", q.Get("follow"))
	assert.Equal(t, "50", q.Get("tail"))
	assert.Equal(t, "1700000000.000000123", q.Get("since"))
}

func TestContainerLogsOptionVariants(t *testing.T) {
	d, e := newFakeDaemon(t)
	fakeInspectTTY(d, false)
	d.on("GET /containers/c1/logs", func(w http.ResponseWriter, _ *http.Request) {})

	for _, o := range []LogOptions{{Tail: -1, StdoutOnly: true}, {Tail: 0}} {
		rc, err := e.ContainerLogs(context.Background(), "c1", o)
		require.NoError(t, err)
		_, err = io.ReadAll(rc)
		require.NoError(t, err)
		require.NoError(t, rc.Close())
	}
	reqs := d.find("GET", "/containers/c1/logs")
	require.Len(t, reqs, 2)

	assert.False(t, reqs[0].Query.Has("tail"), "a negative tail means all lines")
	assert.False(t, reqs[0].Query.Has("stderr"), "StdoutOnly drops stderr")
	assert.False(t, reqs[0].Query.Has("follow"))
	assert.False(t, reqs[0].Query.Has("since"))
	assert.Equal(t, "0", reqs[1].Query.Get("tail"))
	assert.Equal(t, "1", reqs[1].Query.Get("stderr"))
}

func TestContainerLogsTTYStreamIsPassedThrough(t *testing.T) {
	d, e := newFakeDaemon(t)
	fakeInspectTTY(d, true)
	raw := "2025-01-02T03:04:05Z line one\r\n"
	d.on("GET /containers/c1/logs", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(raw)) })
	rc, err := e.ContainerLogs(context.Background(), "c1", LogOptions{Tail: -1})
	require.NoError(t, err)
	defer rc.Close()
	got, err := io.ReadAll(rc)
	require.NoError(t, err)
	assert.Equal(t, raw, string(got), "a TTY container's stream has no frame headers to strip")
}

func TestContainerLogsErrors(t *testing.T) {
	t.Run("container missing", func(t *testing.T) {
		d, e := newFakeDaemon(t)
		d.onError("GET /containers/ghost/json", 404, "No such container: ghost")
		_, err := e.ContainerLogs(context.Background(), "ghost", LogOptions{})
		require.ErrorIs(t, err, ErrNotFound)
		assert.Empty(t, d.find("GET", "/containers/ghost/logs"))
	})
	t.Run("logs endpoint fails", func(t *testing.T) {
		d, e := newFakeDaemon(t)
		fakeInspectTTY(d, false)
		d.onError("GET /containers/c1/logs", 500, "log driver does not support reading")
		_, err := e.ContainerLogs(context.Background(), "c1", LogOptions{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "logs of container c1")
		assert.Contains(t, err.Error(), "log driver does not support reading")
	})
	t.Run("bad frame header surfaces on read", func(t *testing.T) {
		d, e := newFakeDaemon(t)
		fakeInspectTTY(d, false)
		d.on("GET /containers/c1/logs", func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte{9, 0, 0, 0, 0, 0, 0, 1, 'x'})
		})
		rc, err := e.ContainerLogs(context.Background(), "c1", LogOptions{})
		require.NoError(t, err)
		defer rc.Close()
		_, err = io.ReadAll(rc)
		require.Error(t, err)
	})
}

func TestContainerLogsCloseUnblocksAFollowingReader(t *testing.T) {
	d, e := newFakeDaemon(t)
	fakeInspectTTY(d, false)
	first := muxed(t, stdoutFrame("first\n"))
	d.on("GET /containers/c1/logs", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(first)
		w.(http.Flusher).Flush()
		<-d.done // a followed log never ends by itself
	})
	rc, err := e.ContainerLogs(context.Background(), "c1", LogOptions{Follow: true})
	require.NoError(t, err)

	buf := make([]byte, len("first\n"))
	_, err = io.ReadFull(rc, buf)
	require.NoError(t, err)
	assert.Equal(t, "first\n", string(buf))

	require.NoError(t, rc.Close())
	_, err = rc.Read(make([]byte, 1))
	require.Error(t, err, "reads fail once the stream is closed")
}

func TestParseLogLine(t *testing.T) {
	ts := time.Date(2025, 1, 2, 3, 4, 5, 123456789, time.UTC)
	cases := []struct {
		name, line, text string
		when             time.Time
	}{
		{"timestamped", "2025-01-02T03:04:05.123456789Z hello world", "hello world", ts},
		{"empty text", "2025-01-02T03:04:05.123456789Z ", "", ts},
		{"text keeps inner spaces", "2025-01-02T03:04:05.123456789Z a  b", "a  b", ts},
		{"no timestamp", "hello world", "hello world", time.Time{}},
		{"no space", "2025-01-02T03:04:05.123456789Z", "2025-01-02T03:04:05.123456789Z", time.Time{}},
		{"empty", "", "", time.Time{}},
		{"leading space", " 2025-01-02T03:04:05Z x", " 2025-01-02T03:04:05Z x", time.Time{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			when, text := ParseLogLine(c.line)
			assert.True(t, c.when.Equal(when), "time %v != %v", when, c.when)
			assert.Equal(t, c.text, text)
		})
	}
}

// ---- stats ----

func TestContainerStats(t *testing.T) {
	d, e := newFakeDaemon(t)
	d.onJSON("GET /containers/c1/stats", 200, obj{
		"cpu_stats":    obj{"cpu_usage": obj{"total_usage": 200}, "system_cpu_usage": 2000, "online_cpus": 4},
		"precpu_stats": obj{"cpu_usage": obj{"total_usage": 100}, "system_cpu_usage": 1000},
		"memory_stats": obj{"usage": 1000, "limit": 4000, "stats": obj{"inactive_file": 200}},
	})
	got, err := e.ContainerStats(context.Background(), "c1")
	require.NoError(t, err)
	assert.InDelta(t, 40.0, got.CPUPercent, 1e-9)
	assert.EqualValues(t, 800, got.MemBytes)
	assert.EqualValues(t, 4000, got.MemLimit)

	q := d.only("GET", "/containers/c1/stats").Query
	assert.Equal(t, "false", q.Get("stream"), "a single sample, not a stream")
	assert.False(t, q.Has("one-shot"), "the previous sample is requested so CPU deltas exist")
}

func TestContainerStatsErrors(t *testing.T) {
	t.Run("not found", func(t *testing.T) {
		d, e := newFakeDaemon(t)
		d.onError("GET /containers/ghost/stats", 404, "No such container")
		_, err := e.ContainerStats(context.Background(), "ghost")
		require.ErrorIs(t, err, ErrNotFound)
		assert.Contains(t, err.Error(), "stats of container ghost")
	})
	t.Run("garbled body", func(t *testing.T) {
		d, e := newFakeDaemon(t)
		d.on("GET /containers/c1/stats", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("{not json")) })
		_, err := e.ContainerStats(context.Background(), "c1")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "decode stats of c1")
	})
}

func TestStatsFromResponse(t *testing.T) {
	cpu := func(total, sys uint64, online uint32, percpu ...uint64) container.CPUStats {
		return container.CPUStats{CPUUsage: container.CPUUsage{TotalUsage: total, PercpuUsage: percpu}, SystemUsage: sys, OnlineCPUs: online}
	}
	mem := func(usage uint64, stats map[string]uint64) container.MemoryStats {
		return container.MemoryStats{Usage: usage, Limit: 9999, Stats: stats}
	}
	cases := []struct {
		name    string
		in      container.StatsResponse
		cpu     float64
		memUsed uint64
	}{
		{"online cpus scale the percentage", container.StatsResponse{CPUStats: cpu(300, 3000, 2), PreCPUStats: cpu(100, 1000, 2), MemoryStats: mem(500, nil)}, 20, 500},
		{"percpu usage length when online cpus is unknown", container.StatsResponse{CPUStats: cpu(300, 3000, 0, 1, 2, 3), PreCPUStats: cpu(100, 1000, 0), MemoryStats: mem(500, nil)}, 30, 500},
		{"one cpu when nothing is known", container.StatsResponse{CPUStats: cpu(300, 3000, 0), PreCPUStats: cpu(100, 1000, 0), MemoryStats: mem(500, nil)}, 10, 500},
		{"no previous sample", container.StatsResponse{CPUStats: cpu(300, 3000, 2), MemoryStats: mem(500, nil)}, 20, 500},
		{"cpu counter went backwards", container.StatsResponse{CPUStats: cpu(50, 3000, 2), PreCPUStats: cpu(100, 1000, 2), MemoryStats: mem(500, nil)}, 0, 500},
		{"system counter did not advance", container.StatsResponse{CPUStats: cpu(300, 1000, 2), PreCPUStats: cpu(100, 1000, 2), MemoryStats: mem(500, nil)}, 0, 500},
		{"cgroup v2 page cache is not usage", container.StatsResponse{MemoryStats: mem(1000, map[string]uint64{"inactive_file": 300})}, 0, 700},
		{"cgroup v1 page cache is not usage", container.StatsResponse{MemoryStats: mem(1000, map[string]uint64{"total_inactive_file": 400})}, 0, 600},
		{"v2 key wins over v1", container.StatsResponse{MemoryStats: mem(1000, map[string]uint64{"inactive_file": 100, "total_inactive_file": 400})}, 0, 900},
		{"cache larger than usage is ignored", container.StatsResponse{MemoryStats: mem(100, map[string]uint64{"inactive_file": 300})}, 0, 100},
		{"cache equal to usage is ignored", container.StatsResponse{MemoryStats: mem(300, map[string]uint64{"inactive_file": 300})}, 0, 300},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := statsFromResponse(c.in)
			assert.InDelta(t, c.cpu, got.CPUPercent, 1e-9)
			assert.Equal(t, c.memUsed, got.MemBytes)
			assert.EqualValues(t, 9999*boolToUint(c.in.MemoryStats.Limit != 0), got.MemLimit)
		})
	}
}

func boolToUint(b bool) uint64 {
	if b {
		return 1
	}
	return 0
}

// ---- archive / images / cleanup ----

func TestCopyToContainer(t *testing.T) {
	d, e := newFakeDaemon(t)
	d.onStatus("PUT /containers/c1/archive", 200)
	tarBytes := []byte("pretend this is a tar archive")
	require.NoError(t, e.CopyToContainer(context.Background(), "c1", "/etc/app", bytes.NewReader(tarBytes)))
	req := d.only("PUT", "/containers/c1/archive")
	assert.Equal(t, "/etc/app", req.Query.Get("path"))
	assert.Equal(t, "true", req.Query.Get("noOverwriteDirNonDir"))
	assert.Equal(t, tarBytes, req.Body)
}

func TestCopyToContainerErrors(t *testing.T) {
	d, e := newFakeDaemon(t)
	d.onError("PUT /containers/ghost/archive", 404, "No such container: ghost")
	err := e.CopyToContainer(context.Background(), "ghost", "/x", bytes.NewReader(nil))
	require.ErrorIs(t, err, ErrNotFound)
	assert.Contains(t, err.Error(), "copy to container ghost")
}

func TestImageList(t *testing.T) {
	d, e := newFakeDaemon(t)
	d.onJSON("GET /images/json", 200, []obj{
		{"Id": "sha256:a", "RepoTags": []string{"app:1", "app:latest"}, "RepoDigests": []string{"app@sha256:x"}, "Created": 1700000000, "Size": 12345},
		{"Id": "sha256:b", "Created": 0, "Size": 0},
	})
	got, err := e.ImageList(context.Background(), "app")
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, ImageSummary{
		ID: "sha256:a", RepoTags: []string{"app:1", "app:latest"}, RepoDigests: []string{"app@sha256:x"},
		Created: time.Unix(1700000000, 0), Size: 12345,
	}, got[0])
	assert.Equal(t, "sha256:b", got[1].ID)
	assert.Empty(t, got[1].RepoTags)

	assert.Equal(t, `{"reference":{"app":true}}`, d.only("GET", "/images/json").Query.Get("filters"))
}

func TestImageListWithoutReferenceSendsNoFilter(t *testing.T) {
	d, e := newFakeDaemon(t)
	d.onJSON("GET /images/json", 200, []obj{})
	got, err := e.ImageList(context.Background(), "")
	require.NoError(t, err)
	assert.Empty(t, got)
	assert.False(t, d.only("GET", "/images/json").Query.Has("filters"))
}

func TestImageListError(t *testing.T) {
	d, e := newFakeDaemon(t)
	d.onError("GET /images/json", 500, "nope")
	_, err := e.ImageList(context.Background(), "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "list images")
}

func TestImagesSize(t *testing.T) {
	d, e := newFakeDaemon(t)
	d.onJSON("GET /system/df", 200, obj{"ImageUsage": obj{"TotalSize": 987654, "TotalCount": 3}})
	got, err := e.ImagesSize(context.Background())
	require.NoError(t, err)
	assert.EqualValues(t, 987654, got)
	assert.Equal(t, []string{"image"}, d.only("GET", "/system/df").Query["type"])
}

func TestImagesSizeLegacyDaemon(t *testing.T) {
	d, e := newFakeDaemonAt(t, "1.51")
	d.onJSON("GET /system/df", 200, obj{"LayersSize": 4242, "Images": []obj{{"Id": "a"}}})
	got, err := e.ImagesSize(context.Background())
	require.NoError(t, err)
	assert.EqualValues(t, 4242, got, "daemons before API 1.52 report the layer size")
}

func TestImagesSizeError(t *testing.T) {
	d, e := newFakeDaemon(t)
	d.onError("GET /system/df", 500, "nope")
	_, err := e.ImagesSize(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "image disk usage")
}

func TestVolumeList(t *testing.T) {
	d, e := newFakeDaemon(t)
	d.onJSON("GET /system/df", 200, obj{"VolumeUsage": obj{"Items": []obj{
		{"Name": "data", "Driver": "local", "Labels": obj{"app": "x"}, "UsageData": obj{"RefCount": 2, "Size": 4096}},
		{"Name": "unknown", "Driver": "local"},
		{"Name": "orphan", "Driver": "local", "UsageData": obj{"RefCount": 0, "Size": -1}},
	}}})
	got, err := e.VolumeList(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []VolumeInfo{
		{Name: "data", Driver: "local", Labels: map[string]string{"app": "x"}, RefCount: 2, Size: 4096},
		{Name: "unknown", Driver: "local", RefCount: -1, Size: -1},
		{Name: "orphan", Driver: "local", RefCount: 0, Size: -1},
	}, got, "a volume without usage data is reported as in use (-1), never as unreferenced")

	q := d.only("GET", "/system/df").Query
	assert.Equal(t, []string{"volume"}, q["type"])
	assert.Equal(t, "1", q.Get("verbose"))
}

func TestVolumeListLegacyDaemon(t *testing.T) {
	d, e := newFakeDaemonAt(t, "1.51")
	d.onJSON("GET /system/df", 200, obj{"Volumes": []obj{
		{"Name": "data", "Driver": "local", "UsageData": obj{"RefCount": 1, "Size": 10}},
	}})
	got, err := e.VolumeList(context.Background())
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "data", got[0].Name)
	assert.EqualValues(t, 1, got[0].RefCount)
	assert.EqualValues(t, 10, got[0].Size)
}

func TestVolumeListEmptyAndError(t *testing.T) {
	d, e := newFakeDaemon(t)
	d.onJSON("GET /system/df", 200, obj{})
	got, err := e.VolumeList(context.Background())
	require.NoError(t, err)
	assert.NotNil(t, got)
	assert.Empty(t, got)

	d2, e2 := newFakeDaemon(t)
	d2.onError("GET /system/df", 500, "nope")
	_, err = e2.VolumeList(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "volume disk usage")
}

// ---- terminal archive API ----

func statHeader(t *testing.T, st obj) string {
	t.Helper()
	return encodeStatHeader(t, st)
}

func TestContainerStatPath(t *testing.T) {
	d, e := newFakeDaemon(t)
	d.on("HEAD /containers/c1/archive", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Docker-Container-Path-Stat", statHeader(t, obj{
			"name": "app.conf", "size": 321, "mode": 420, "mtime": "2025-01-02T03:04:05Z", "linkTarget": "/etc/real.conf",
		}))
		w.WriteHeader(http.StatusOK)
	})
	got, err := e.ContainerStatPath(context.Background(), "c1", "/etc/app.conf")
	require.NoError(t, err)
	assert.Equal(t, "app.conf", got.Name)
	assert.EqualValues(t, 321, got.Size)
	assert.EqualValues(t, 0o644, got.Mode)
	assert.True(t, time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC).Equal(got.MTime))
	assert.Equal(t, "/etc/real.conf", got.LinkTarget)
	assert.Equal(t, "/etc/app.conf", d.only("HEAD", "/containers/c1/archive").Query.Get("path"))
}

func TestContainerStatPathErrors(t *testing.T) {
	t.Run("missing path", func(t *testing.T) {
		d, e := newFakeDaemon(t)
		d.onStatus("HEAD /containers/c1/archive", 404)
		_, err := e.ContainerStatPath(context.Background(), "c1", "/nope")
		require.ErrorIs(t, err, ErrNotFound)
		assert.Contains(t, err.Error(), "stat /nope in c1")
	})
	t.Run("missing stat header", func(t *testing.T) {
		d, e := newFakeDaemon(t)
		d.onStatus("HEAD /containers/c1/archive", 200)
		_, err := e.ContainerStatPath(context.Background(), "c1", "/x")
		require.Error(t, err)
	})
}

func TestCopyFromContainer(t *testing.T) {
	d, e := newFakeDaemon(t)
	tarBytes := []byte("tar bytes go here")
	d.on("GET /containers/c1/archive", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Docker-Container-Path-Stat", statHeader(t, obj{"name": "dir", "size": 0, "mode": 2147484141, "mtime": "2025-01-02T03:04:05Z", "linkTarget": ""}))
		_, _ = w.Write(tarBytes)
	})
	rc, st, err := e.CopyFromContainer(context.Background(), "c1", "/srv/dir")
	require.NoError(t, err)
	defer rc.Close()
	got, err := io.ReadAll(rc)
	require.NoError(t, err)
	assert.Equal(t, tarBytes, got)
	assert.Equal(t, "dir", st.Name)
	assert.True(t, st.Mode.IsDir())
	assert.Equal(t, "/srv/dir", d.only("GET", "/containers/c1/archive").Query.Get("path"))
}

func TestCopyFromContainerErrors(t *testing.T) {
	t.Run("missing path", func(t *testing.T) {
		d, e := newFakeDaemon(t)
		d.onError("GET /containers/c1/archive", 404, "Could not find the file /nope in container c1")
		rc, _, err := e.CopyFromContainer(context.Background(), "c1", "/nope")
		require.ErrorIs(t, err, ErrNotFound)
		assert.Nil(t, rc)
	})
	t.Run("bad stat header", func(t *testing.T) {
		d, e := newFakeDaemon(t)
		d.on("GET /containers/c1/archive", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("X-Docker-Container-Path-Stat", "%%%")
			_, _ = w.Write([]byte("x"))
		})
		rc, _, err := e.CopyFromContainer(context.Background(), "c1", "/x")
		require.Error(t, err)
		assert.Nil(t, rc)
		assert.Contains(t, err.Error(), "copy /x from c1")
	})
}

func TestCapWriter(t *testing.T) {
	w := &capWriter{max: 5}
	n, err := w.Write([]byte("abc"))
	require.NoError(t, err)
	assert.Equal(t, 3, n)
	assert.False(t, w.truncated)

	n, err = w.Write([]byte("defgh"))
	require.NoError(t, err)
	assert.Equal(t, 5, n, "a write is always reported complete so the container never stalls")
	assert.Equal(t, "abcde", w.buf.String())
	assert.True(t, w.truncated)

	n, err = w.Write([]byte("zzz"))
	require.NoError(t, err)
	assert.Equal(t, 3, n)
	assert.Equal(t, "abcde", w.buf.String())
}

func TestCapWriterExactFitIsNotTruncated(t *testing.T) {
	w := &capWriter{max: 4}
	_, err := w.Write([]byte("abcd"))
	require.NoError(t, err)
	assert.False(t, w.truncated)
	_, err = w.Write(nil)
	require.NoError(t, err)
	assert.False(t, w.truncated, "an empty write to a full buffer drops nothing")
	_, err = w.Write([]byte("e"))
	require.NoError(t, err)
	assert.True(t, w.truncated)
}
