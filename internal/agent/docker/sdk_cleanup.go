package docker

import (
	"context"

	"github.com/moby/moby/client"
)

// VolumeInfo is one volume with its usage, as cleanup needs it.
type VolumeInfo struct {
	Name   string
	Driver string
	Labels map[string]string
	// RefCount is the number of containers (running or stopped) that mount the
	// volume; -1 when the daemon could not tell, which callers treat as in use.
	RefCount int64
	// Size is the volume's size in bytes; -1 when not available.
	Size int64
}

// ImagesSize returns the disk space all local images occupy in bytes, shared
// layers counted once. Cleanup compares it before and after to report what an
// image removal really freed.
func (e *SDKEngine) ImagesSize(ctx context.Context) (int64, error) {
	res, err := e.cli.DiskUsage(ctx, client.DiskUsageOptions{Images: true})
	if err != nil {
		return 0, wrap(err, "image disk usage")
	}
	return res.Images.TotalSize, nil
}

// VolumeList returns every local volume with its reference count.
func (e *SDKEngine) VolumeList(ctx context.Context) ([]VolumeInfo, error) {
	res, err := e.cli.DiskUsage(ctx, client.DiskUsageOptions{Volumes: true, Verbose: true})
	if err != nil {
		return nil, wrap(err, "volume disk usage")
	}
	out := make([]VolumeInfo, 0, len(res.Volumes.Items))
	for _, v := range res.Volumes.Items {
		vi := VolumeInfo{Name: v.Name, Driver: v.Driver, Labels: v.Labels, RefCount: -1, Size: -1}
		if v.UsageData != nil {
			vi.RefCount, vi.Size = v.UsageData.RefCount, v.UsageData.Size
		}
		out = append(out, vi)
	}
	return out, nil
}
