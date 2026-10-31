package docker

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/client"
)

// ContainerExec implements Engine. The command runs without a TTY, so the
// daemon multiplexes stdout and stderr; both are demultiplexed with stdcopy.
func (e *SDKEngine) ContainerExec(ctx context.Context, containerID string, argv []string, spec ExecSpec, stdout, stderr io.Writer) (int, error) {
	if len(argv) == 0 {
		return -1, errors.New("exec: empty argv")
	}
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	created, err := e.cli.ExecCreate(ctx, containerID, client.ExecCreateOptions{
		User: spec.User, Env: spec.Env, Cmd: argv,
		AttachStdin: spec.Stdin != nil, AttachStdout: true, AttachStderr: true,
	})
	if err != nil {
		return -1, wrap(err, "exec create in "+containerID)
	}
	att, err := e.cli.ExecAttach(ctx, created.ID, client.ExecAttachOptions{})
	if err != nil {
		return -1, wrap(err, "exec attach in "+containerID)
	}
	defer att.Close()

	// Unblock the demux loop when ctx ends: closing the hijacked connection
	// makes StdCopy return.
	stop := context.AfterFunc(ctx, att.Close)
	defer stop()

	if spec.Stdin != nil {
		go func() {
			_, _ = io.Copy(att.Conn, spec.Stdin)
			_ = att.CloseWrite()
		}()
	}
	if _, err := stdcopy.StdCopy(stdout, stderr, att.Reader); err != nil {
		if ctx.Err() != nil {
			return -1, ctx.Err()
		}
		return -1, fmt.Errorf("exec stream in %s: %w", containerID, err)
	}
	insp, err := e.cli.ExecInspect(ctx, created.ID, client.ExecInspectOptions{})
	if err != nil {
		return -1, wrap(err, "exec inspect in "+containerID)
	}
	return insp.ExitCode, nil
}
