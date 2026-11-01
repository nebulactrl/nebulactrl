// Package hardening runs Lynis audits and the curated remediations of
// internal/lynis on the host the agent manages.
//
// Everything is a shell program handed to a Runner. DirectRunner runs it as the
// agent's own user (root) when the agent is installed on the host; the engine's
// helper runner runs it through a short-lived privileged container that enters
// PID 1's namespaces (the host shell's mechanism) when the agent runs in a
// container. Both give the program the host's own filesystem and tools.
package hardening

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/nebulactrl/nebulactrl/internal/agent/hostpty"
)

// Modes a Runner reports (HardeningResult.Mode).
const (
	ModeHost   = "host"
	ModeHelper = "helper"
)

// maxOutput bounds what a script may print back (a Lynis report subset is a
// few hundred kilobytes at most).
const maxOutput = 4 << 20

// Output is what a script printed and its exit code.
type Output struct {
	Stdout, Stderr string
	ExitCode       int
	Truncated      bool
}

// Runner runs a POSIX shell program on the host.
type Runner interface {
	Run(ctx context.Context, script string) (Output, error)
	// Mode is ModeHost or ModeHelper.
	Mode() string
}

// DirectRunner runs scripts with /bin/sh on the machine the agent runs on.
// The script runs as the agent's user (root), so it gets the same minimal
// environment as a host shell, never the agent's own (enrolment token,
// control-plane URL).
type DirectRunner struct {
	// Shell defaults to /bin/sh.
	Shell string
	// Env is added to the script's environment.
	Env []string
}

// proxyKeys are the only settings besides the host-shell basics taken from the
// agent's environment: the Lynis installer downloads through curl and apt,
// which honour them on a host that is behind a proxy.
var proxyKeys = []string{
	"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "no_proxy",
}

// scriptEnv builds the environment of a script from hostpty.Env.
func scriptEnv(shell string) []string {
	env := hostpty.Env("dumb", shell)
	for _, k := range proxyKeys {
		if v := os.Getenv(k); v != "" && !strings.ContainsAny(v, "\n\x00") {
			env = append(env, k+"="+v)
		}
	}
	return env
}

// Mode implements Runner.
func (DirectRunner) Mode() string { return ModeHost }

type limitBuffer struct {
	bytes.Buffer
	truncated bool
}

func (b *limitBuffer) Write(p []byte) (int, error) {
	if room := maxOutput - b.Len(); room < len(p) {
		if room > 0 {
			b.Buffer.Write(p[:room])
		}
		b.truncated = true
		return len(p), nil
	}
	return b.Buffer.Write(p)
}

// Run implements Runner. A script that exits non-zero is not an error: the
// exit code is in the Output. Cancelling ctx kills the script and everything it
// started.
func (r DirectRunner) Run(ctx context.Context, script string) (Output, error) {
	shell := r.Shell
	if shell == "" {
		shell = "/bin/sh"
	}
	cmd := exec.CommandContext(ctx, shell, "-c", script)
	cmd.Env = append(scriptEnv(shell), r.Env...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = 5 * time.Second
	var out, errb limitBuffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	res := Output{Stdout: out.String(), Stderr: errb.String(), Truncated: out.truncated || errb.truncated}
	var ee *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &ee) && ctx.Err() == nil:
		res.ExitCode = ee.ExitCode()
	case ctx.Err() != nil:
		return res, fmt.Errorf("script stopped: %w", ctx.Err())
	default:
		return res, fmt.Errorf("run %s: %w", shell, err)
	}
	return res, nil
}
