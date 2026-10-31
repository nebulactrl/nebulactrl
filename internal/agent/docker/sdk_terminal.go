package docker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/moby/moby/client"
)

var _ Terminal = (*SDKEngine)(nil)

// execPollInterval is how often Wait inspects a finished exec for its exit code.
const execPollInterval = 50 * time.Millisecond

type sdkTTY struct {
	e      *SDKEngine
	execID string
	att    client.ExecAttachResult
}

// ContainerExecTTY implements Terminal.
func (e *SDKEngine) ContainerExecTTY(ctx context.Context, containerID string, spec TTYSpec) (TTYSession, error) {
	if len(spec.Argv) == 0 {
		return nil, errors.New("exec: empty argv")
	}
	size := client.ConsoleSize{Height: uint(max(spec.Rows, 0)), Width: uint(max(spec.Cols, 0))}
	created, err := e.cli.ExecCreate(ctx, containerID, client.ExecCreateOptions{
		User: spec.User, Env: spec.Env, Cmd: spec.Argv, TTY: true, ConsoleSize: size,
		AttachStdin: true, AttachStdout: true, AttachStderr: true,
	})
	if err != nil {
		return nil, wrap(err, "exec create in "+containerID)
	}
	att, err := e.cli.ExecAttach(ctx, created.ID, client.ExecAttachOptions{TTY: true, ConsoleSize: size})
	if err != nil {
		return nil, wrap(err, "exec attach in "+containerID)
	}
	return &sdkTTY{e: e, execID: created.ID, att: att}, nil
}

func (s *sdkTTY) Read(p []byte) (int, error)  { return s.att.Reader.Read(p) }
func (s *sdkTTY) Write(p []byte) (int, error) { return s.att.Conn.Write(p) }

func (s *sdkTTY) Resize(ctx context.Context, cols, rows int) error {
	_, err := s.e.cli.ExecResize(ctx, s.execID, client.ExecResizeOptions{Height: uint(max(rows, 1)), Width: uint(max(cols, 1))})
	return wrap(err, "exec resize")
}

func (s *sdkTTY) Wait(ctx context.Context) (int, error) {
	t := time.NewTicker(execPollInterval)
	defer t.Stop()
	for {
		insp, err := s.e.cli.ExecInspect(ctx, s.execID, client.ExecInspectOptions{})
		if err != nil {
			return -1, wrap(err, "exec inspect")
		}
		if !insp.Running {
			return insp.ExitCode, nil
		}
		select {
		case <-ctx.Done():
			return -1, ctx.Err()
		case <-t.C:
		}
	}
}

func (s *sdkTTY) Close() error {
	s.att.Close()
	return nil
}

// ContainerStatPath implements Terminal.
func (e *SDKEngine) ContainerStatPath(ctx context.Context, id, path string) (PathStat, error) {
	res, err := e.cli.ContainerStatPath(ctx, id, client.ContainerStatPathOptions{Path: path})
	if err != nil {
		return PathStat{}, wrap(err, fmt.Sprintf("stat %s in %s", path, id))
	}
	st := res.Stat
	return PathStat{Name: st.Name, Size: st.Size, Mode: st.Mode, MTime: st.Mtime, LinkTarget: st.LinkTarget}, nil
}

// CopyFromContainer implements Terminal.
func (e *SDKEngine) CopyFromContainer(ctx context.Context, id, path string) (io.ReadCloser, PathStat, error) {
	res, err := e.cli.CopyFromContainer(ctx, id, client.CopyFromContainerOptions{SourcePath: path})
	if err != nil {
		return nil, PathStat{}, wrap(err, fmt.Sprintf("copy %s from %s", path, id))
	}
	st := res.Stat
	return res.Content, PathStat{Name: st.Name, Size: st.Size, Mode: st.Mode, MTime: st.Mtime, LinkTarget: st.LinkTarget}, nil
}
