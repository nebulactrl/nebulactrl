package docker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

var _ HostCommandRunner = (*SDKEngine)(nil)

const defaultHostCommandOutput = 4 << 20

// capWriter keeps the first max bytes written and drops the rest.
type capWriter struct {
	buf       bytes.Buffer
	max       int64
	truncated bool
}

func (w *capWriter) Write(p []byte) (int, error) {
	if room := w.max - int64(w.buf.Len()); room > 0 {
		if int64(len(p)) <= room {
			w.buf.Write(p)
		} else {
			w.buf.Write(p[:room])
			w.truncated = true
		}
	} else if len(p) > 0 {
		w.truncated = true
	}
	return len(p), nil // never fail: a full buffer must not stall the container
}

// RunHostCommand implements HostCommandRunner.
func (e *SDKEngine) RunHostCommand(ctx context.Context, spec HostCommandSpec) (HostCommandResult, error) {
	if len(spec.Argv) == 0 {
		return HostCommandResult{}, errors.New("host command: empty command")
	}
	limit := spec.MaxOutput
	if limit <= 0 {
		limit = defaultHostCommandOutput
	}
	cfg := &container.Config{
		Image: spec.Image, Env: spec.Env, Labels: spec.Labels,
		AttachStdout: true, AttachStderr: true,
		Entrypoint: spec.Argv[:1], Cmd: spec.Argv[1:],
	}
	host := &container.HostConfig{
		Privileged: true, PidMode: "host", AutoRemove: true,
		RestartPolicy: container.RestartPolicy{Name: container.RestartPolicyDisabled},
	}
	res, err := e.cli.ContainerCreate(ctx, client.ContainerCreateOptions{Config: cfg, HostConfig: host, Name: spec.Name})
	if err != nil {
		return HostCommandResult{}, wrap(err, "create host command container "+spec.Name)
	}
	id := res.ID
	remove := func() {
		_, _ = e.cli.ContainerRemove(context.WithoutCancel(ctx), id, client.ContainerRemoveOptions{Force: true})
	}
	// The exit code must be read before the daemon removes the container.
	waitCtx, stopWait := context.WithCancel(context.WithoutCancel(ctx))
	defer stopWait()
	wait := e.cli.ContainerWait(waitCtx, id, client.ContainerWaitOptions{Condition: container.WaitConditionRemoved})
	exit := make(chan int, 1)
	go func() {
		select {
		case r := <-wait.Result:
			exit <- int(r.StatusCode)
		case <-wait.Error:
			exit <- -1
		case <-waitCtx.Done():
		}
	}()
	att, err := e.cli.ContainerAttach(ctx, id, client.ContainerAttachOptions{Stream: true, Stdout: true, Stderr: true})
	if err != nil {
		remove()
		return HostCommandResult{}, wrap(err, "attach to host command container "+spec.Name)
	}
	defer att.Close()
	if _, err := e.cli.ContainerStart(ctx, id, client.ContainerStartOptions{}); err != nil {
		remove()
		return HostCommandResult{}, wrap(err, "start host command container "+spec.Name)
	}
	// Closing the hijacked connection unblocks the demux loop when ctx ends; the
	// helper is killed with it.
	stop := context.AfterFunc(ctx, func() { remove(); att.Close() })
	defer stop()

	out, errw := &capWriter{max: limit}, &capWriter{max: limit}
	if _, err := stdcopy.StdCopy(out, errw, att.Reader); err != nil && !errors.Is(err, io.EOF) {
		if ctx.Err() != nil {
			return HostCommandResult{}, ctx.Err()
		}
		remove()
		return HostCommandResult{}, fmt.Errorf("read host command output: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return HostCommandResult{}, err
	}
	code := -1
	select {
	case code = <-exit:
	case <-ctx.Done():
		return HostCommandResult{}, ctx.Err()
	}
	return HostCommandResult{
		Stdout: out.buf.String(), Stderr: errw.buf.String(), ExitCode: code, Truncated: out.truncated || errw.truncated,
	}, nil
}
