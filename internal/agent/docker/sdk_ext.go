package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

// ContainerLogs implements Engine. Managed containers never allocate a TTY,
// so the daemon multiplexes stdout and stderr; both are demultiplexed with
// stdcopy into a single timestamped line stream.
func (e *SDKEngine) ContainerLogs(ctx context.Context, id string, o LogOptions) (io.ReadCloser, error) {
	insp, err := e.cli.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
	if err != nil {
		return nil, wrap(err, "inspect container "+id)
	}
	tail := "all"
	if o.Tail >= 0 {
		tail = strconv.Itoa(o.Tail)
	}
	opts := client.ContainerLogsOptions{
		ShowStdout: true, ShowStderr: !o.StdoutOnly, Timestamps: true, Follow: o.Follow, Tail: tail,
	}
	if !o.Since.IsZero() {
		opts.Since = fmt.Sprintf("%d.%09d", o.Since.Unix(), o.Since.Nanosecond())
	}
	res, err := e.cli.ContainerLogs(ctx, id, opts)
	if err != nil {
		return nil, wrap(err, "logs of container "+id)
	}
	if insp.Container.Config != nil && insp.Container.Config.Tty {
		return res, nil
	}
	pr, pw := io.Pipe()
	go func() {
		_, err := stdcopy.StdCopy(pw, pw, res)
		_ = res.Close()
		_ = pw.CloseWithError(err) // nil error closes with io.EOF
	}()
	return &demuxedLogs{pr: pr, src: res}, nil
}

type demuxedLogs struct {
	pr  *io.PipeReader
	src io.Closer
}

func (d *demuxedLogs) Read(p []byte) (int, error) { return d.pr.Read(p) }

func (d *demuxedLogs) Close() error {
	_ = d.src.Close()
	return d.pr.Close()
}

// ContainerStats implements Engine using a one-shot sample with a previous
// sample so CPU deltas are populated.
func (e *SDKEngine) ContainerStats(ctx context.Context, id string) (ContainerStats, error) {
	res, err := e.cli.ContainerStats(ctx, id, client.ContainerStatsOptions{IncludePreviousSample: true})
	if err != nil {
		return ContainerStats{}, wrap(err, "stats of container "+id)
	}
	defer res.Body.Close()
	var s container.StatsResponse
	if err := json.NewDecoder(res.Body).Decode(&s); err != nil {
		return ContainerStats{}, fmt.Errorf("decode stats of %s: %w", id, err)
	}
	return statsFromResponse(s), nil
}

func statsFromResponse(s container.StatsResponse) ContainerStats {
	out := ContainerStats{MemLimit: s.MemoryStats.Limit}

	cpuTotal, preTotal := s.CPUStats.CPUUsage.TotalUsage, s.PreCPUStats.CPUUsage.TotalUsage
	sys, preSys := s.CPUStats.SystemUsage, s.PreCPUStats.SystemUsage
	if cpuTotal > preTotal && sys > preSys {
		cpus := float64(s.CPUStats.OnlineCPUs)
		if cpus == 0 {
			cpus = float64(len(s.CPUStats.CPUUsage.PercpuUsage))
		}
		if cpus == 0 {
			cpus = 1
		}
		out.CPUPercent = float64(cpuTotal-preTotal) / float64(sys-preSys) * cpus * 100
	}

	used := s.MemoryStats.Usage
	cache := s.MemoryStats.Stats["inactive_file"] // cgroup v2
	if cache == 0 {
		cache = s.MemoryStats.Stats["total_inactive_file"] // cgroup v1
	}
	if cache < used {
		used -= cache
	}
	out.MemBytes = used
	return out
}

// CopyToContainer implements Engine.
func (e *SDKEngine) CopyToContainer(ctx context.Context, id, dir string, content io.Reader) error {
	_, err := e.cli.CopyToContainer(ctx, id, client.CopyToContainerOptions{DestinationPath: dir, Content: content})
	return wrap(err, "copy to container "+id)
}

// ImageList implements Engine.
func (e *SDKEngine) ImageList(ctx context.Context, reference string) ([]ImageSummary, error) {
	filters := client.Filters{}
	if reference != "" {
		filters = filters.Add("reference", reference)
	}
	res, err := e.cli.ImageList(ctx, client.ImageListOptions{Filters: filters})
	if err != nil {
		return nil, wrap(err, "list images")
	}
	out := make([]ImageSummary, 0, len(res.Items))
	for _, it := range res.Items {
		out = append(out, ImageSummary{
			ID:          it.ID,
			RepoTags:    it.RepoTags,
			RepoDigests: it.RepoDigests,
			Created:     time.Unix(it.Created, 0),
			Size:        it.Size,
		})
	}
	return out, nil
}

// ParseLogLine splits a timestamped log line ("<RFC3339Nano> <text>") as
// produced by ContainerLogs. Lines without a parsable timestamp are returned
// whole with the zero time.
func ParseLogLine(line string) (time.Time, string) {
	i := strings.IndexByte(line, ' ')
	if i <= 0 {
		return time.Time{}, line
	}
	if ts, err := time.Parse(time.RFC3339Nano, line[:i]); err == nil {
		return ts, line[i+1:]
	}
	return time.Time{}, line
}
