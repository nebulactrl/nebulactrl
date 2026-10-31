package docker

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

var _ HostShellRunner = (*SDKEngine)(nil)

// sdkAttachTTY is a TTY attached to a container's main process.
type sdkAttachTTY struct {
	e    *SDKEngine
	id   string
	att  client.ContainerAttachResult
	exit chan int // receives the exit code once the container is gone
}

// RunHostShell implements HostShellRunner.
func (e *SDKEngine) RunHostShell(ctx context.Context, spec HostShellSpec) (string, TTYSession, error) {
	if len(spec.Argv) == 0 {
		return "", nil, errors.New("host shell: empty command")
	}
	cfg := &container.Config{
		Image: spec.Image, Env: spec.Env, Labels: spec.Labels,
		Tty: true, OpenStdin: true, StdinOnce: true,
		AttachStdin: true, AttachStdout: true, AttachStderr: true,
		// The program replaces the image's entrypoint (the agent binary must not
		// run): the entrypoint is its argv[0], the command the rest.
		Entrypoint: spec.Argv[:1], Cmd: spec.Argv[1:],
	}
	host := &container.HostConfig{
		Privileged: true, PidMode: "host", AutoRemove: true,
		RestartPolicy: container.RestartPolicy{Name: container.RestartPolicyDisabled},
	}
	res, err := e.cli.ContainerCreate(ctx, client.ContainerCreateOptions{Config: cfg, HostConfig: host, Name: spec.Name})
	if err != nil {
		return "", nil, wrap(err, "create host shell container "+spec.Name)
	}
	id := res.ID
	cleanup := func() {
		_, _ = e.cli.ContainerRemove(context.WithoutCancel(ctx), id, client.ContainerRemoveOptions{Force: true})
	}
	// Learn the exit code before the daemon removes the container on exit.
	wait := e.cli.ContainerWait(context.WithoutCancel(ctx), id, client.ContainerWaitOptions{Condition: container.WaitConditionRemoved})
	exit := make(chan int, 1)
	go func() {
		select {
		case r := <-wait.Result:
			exit <- int(r.StatusCode)
		case <-wait.Error:
			exit <- -1
		}
	}()
	att, err := e.cli.ContainerAttach(ctx, id, client.ContainerAttachOptions{Stream: true, Stdin: true, Stdout: true, Stderr: true})
	if err != nil {
		cleanup()
		return "", nil, wrap(err, "attach to host shell container "+spec.Name)
	}
	if _, err := e.cli.ContainerStart(ctx, id, client.ContainerStartOptions{}); err != nil {
		att.Close()
		cleanup()
		return "", nil, wrap(err, "start host shell container "+spec.Name)
	}
	t := &sdkAttachTTY{e: e, id: id, att: att, exit: exit}
	if spec.Cols > 0 && spec.Rows > 0 {
		_ = t.Resize(ctx, spec.Cols, spec.Rows)
	}
	return id, t, nil
}

func (s *sdkAttachTTY) Read(p []byte) (int, error)  { return s.att.Reader.Read(p) }
func (s *sdkAttachTTY) Write(p []byte) (int, error) { return s.att.Conn.Write(p) }

func (s *sdkAttachTTY) Resize(ctx context.Context, cols, rows int) error {
	_, err := s.e.cli.ContainerResize(ctx, s.id, client.ContainerResizeOptions{Height: uint(max(rows, 1)), Width: uint(max(cols, 1))})
	return wrap(err, "resize host shell")
}

// Wait returns the shell's exit code once the container is gone.
func (s *sdkAttachTTY) Wait(ctx context.Context) (int, error) {
	select {
	case code := <-s.exit:
		return code, nil
	case <-ctx.Done():
		return -1, fmt.Errorf("wait for host shell exit: %w", ctx.Err())
	case <-time.After(time.Hour):
		return -1, errors.New("wait for host shell exit: timed out")
	}
}

func (s *sdkAttachTTY) Close() error {
	s.att.Close()
	return nil
}
