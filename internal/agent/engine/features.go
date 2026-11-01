package engine

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// applyTemplateFeatures sets the container spec parts that come from the
// service's optional features: one-shot jobs run to completion without a
// restart policy, and exec health checks are installed as the container's
// HEALTHCHECK so Docker runs them inside the container.
func (d *deploy) applyTemplateFeatures(spec *docker.ContainerSpec) {
	p := d.p
	if p.OneShot {
		spec.RestartPolicy = "no"
		spec.Labels[agentprotocol.LabelOneShot] = "true"
	}
	if h := p.Healthcheck; h.Type == "exec" && len(h.Command) > 0 {
		sec := func(v, def int) time.Duration {
			if v <= 0 {
				v = def
			}
			return time.Duration(v) * time.Second
		}
		spec.Healthcheck = &docker.HealthSpec{
			Test: h.Command, Interval: sec(h.IntervalSeconds, 5), Timeout: sec(h.TimeoutSeconds, 5),
			StartPeriod: time.Duration(max(h.StartPeriodSeconds, 0)) * time.Second, Retries: 3,
		}
	}
}

// writeFiles copies the payload's files into the (created, not yet started)
// container. Parent directories are created by the extraction.
func (d *deploy) writeFiles(ctx context.Context, id string) error {
	if len(d.p.Files) == 0 {
		return nil
	}
	tarball, err := filesTar(d.p.Files)
	if err != nil {
		return err
	}
	return d.e.cfg.Docker.CopyToContainer(ctx, id, "/", tarball)
}

// filesTar builds a tar archive rooted at "/" with one regular file per spec.
func filesTar(files []agentprotocol.FileSpec) (*bytes.Reader, error) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, f := range files {
		name := strings.TrimPrefix(path.Clean(f.Path), "/")
		if !path.IsAbs(f.Path) || strings.Contains(f.Path, "..") || path.Clean(f.Path) != f.Path || name == "" {
			return nil, fmt.Errorf("invalid file path %q", f.Path)
		}
		mode := int64(0o644)
		if f.Mode != "" {
			m, err := strconv.ParseInt(f.Mode, 8, 32)
			if err != nil {
				return nil, fmt.Errorf("file %s: invalid mode %q", f.Path, f.Mode)
			}
			mode = m
		}
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: mode, Size: int64(len(f.Content)), Typeflag: tar.TypeReg}); err != nil {
			return nil, err
		}
		if _, err := tw.Write([]byte(f.Content)); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	return bytes.NewReader(buf.Bytes()), nil
}
