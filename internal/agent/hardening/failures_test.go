package hardening

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// scriptedRunner fails the calls whose index is in errs and answers the others
// from out (the last answer repeats).
type scriptedRunner struct {
	out   []Output
	errs  map[int]error
	calls int
}

func (r *scriptedRunner) Mode() string { return ModeHost }
func (r *scriptedRunner) Run(context.Context, string) (Output, error) {
	i := r.calls
	r.calls++
	if err := r.errs[i]; err != nil {
		return Output{}, err
	}
	return r.out[min(i, len(r.out)-1)], nil
}

func TestLimitBufferKeepsTheFirstMaxOutputBytes(t *testing.T) {
	var b limitBuffer
	n, err := b.Write([]byte(strings.Repeat("a", maxOutput-3)))
	require.NoError(t, err)
	require.Equal(t, maxOutput-3, n)
	require.False(t, b.truncated)

	n, err = b.Write([]byte("bcdefg"))
	require.NoError(t, err)
	require.Equal(t, 6, n, "the writer is told everything was taken so the script keeps running")
	require.True(t, b.truncated)
	require.Equal(t, maxOutput, b.Len())
	require.True(t, strings.HasSuffix(b.String(), "bcd"), "only the bytes that fit are kept")

	n, err = b.Write([]byte("more"))
	require.NoError(t, err)
	require.Equal(t, 4, n)
	require.Equal(t, maxOutput, b.Len())
}

func TestDirectRunnerDoesNotMarkSmallOutputTruncated(t *testing.T) {
	out, err := DirectRunner{}.Run(context.Background(), "echo small")
	require.NoError(t, err)
	require.False(t, out.Truncated)
	require.Equal(t, "small\n", out.Stdout)
}

// The cap must hold for real script output: exec copies the pipe with
// io.Copy, which prefers the destination's ReadFrom over Write.
func TestDirectRunnerCapsLargeOutput(t *testing.T) {
	out, err := DirectRunner{}.Run(context.Background(), "head -c 4300000 /dev/zero | tr '\\0' a")
	require.NoError(t, err)
	require.True(t, out.Truncated)
	require.Len(t, out.Stdout, maxOutput)
}

func TestDirectRunnerReportsAMissingShell(t *testing.T) {
	_, err := DirectRunner{Shell: "/nonexistent/sh"}.Run(context.Background(), "true")
	require.ErrorContains(t, err, "run /nonexistent/sh")
}

func TestDirectRunnerPassesProxySettingsButNotNewlineInjection(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://proxy.corp:3128")
	t.Setenv("http_proxy", "http://bad\nINJECTED=1")
	out, err := DirectRunner{}.Run(context.Background(), `printf '%s|%s|%s' "$HTTPS_PROXY" "${http_proxy-unset}" "${INJECTED-unset}"`)
	require.NoError(t, err)
	require.Equal(t, "http://proxy.corp:3128|unset|unset", out.Stdout)
}

func TestTailLine(t *testing.T) {
	long := strings.Repeat("y", 500)
	for name, tc := range map[string]struct {
		out  Output
		want string
	}{
		"last stderr line":    {Output{Stdout: "ignored", Stderr: "one\ntwo\n"}, "two"},
		"stdout as fallback":  {Output{Stdout: "a\nb"}, "b"},
		"truncated":           {Output{Stderr: long}, long[:300]},
		"no output":           {Output{ExitCode: 9}, "no output, exit 9"},
		"whitespace only":     {Output{Stderr: " \n ", Stdout: "\n"}, "no output, exit 0"},
		"single line trimmed": {Output{Stderr: "  boom  \n"}, "boom"},
	} {
		require.Equal(t, tc.want, tailLine(tc.out), name)
	}
}

func TestFixStateCheckFailuresAreReported(t *testing.T) {
	audit := agentprotocol.HardeningPayload{Action: agentprotocol.HardeningActionAudit}

	r := &scriptedRunner{out: []Output{{Stdout: "@@NOLYNIS\n"}, {Stderr: "sh: lynis-driver: not found\n", ExitCode: 127}}}
	_, err := New(r, nil).Handle(context.Background(), audit)
	require.ErrorContains(t, err, "check fixes: the host returned no result (exit 127): sh: lynis-driver: not found")

	r = &scriptedRunner{out: []Output{{Stdout: "@@NOLYNIS\n"}}, errs: map[int]error{1: errors.New("container gone")}}
	_, err = New(r, nil).Handle(context.Background(), audit)
	require.ErrorContains(t, err, "check fixes: container gone")
}

func TestChangeFailuresAreReported(t *testing.T) {
	apply := agentprotocol.HardeningPayload{Action: agentprotocol.HardeningActionApply, FixIDs: []string{"umask"}}

	r := &scriptedRunner{out: []Output{{}}, errs: map[int]error{0: errors.New("helper failed")}}
	_, err := New(r, nil).Handle(context.Background(), apply)
	require.ErrorContains(t, err, "apply fixes: helper failed")

	r = &scriptedRunner{out: []Output{{Stderr: "permission denied", ExitCode: 126}}}
	_, err = New(r, nil).Handle(context.Background(), apply)
	require.ErrorContains(t, err, "apply fixes: the host returned no result (exit 126): permission denied")

	rb := agentprotocol.HardeningPayload{Action: agentprotocol.HardeningActionRollback, FixIDs: []string{"umask"}}
	r = &scriptedRunner{out: []Output{{}}, errs: map[int]error{0: errors.New("helper failed")}}
	_, err = New(r, nil).Handle(context.Background(), rb)
	require.ErrorContains(t, err, "rollback fixes: helper failed")
}

func TestInstallAndAuditRunnerFailuresAreReported(t *testing.T) {
	withInstall := agentprotocol.HardeningPayload{Action: agentprotocol.HardeningActionAudit, InstallLynis: true}
	r := &scriptedRunner{out: []Output{{}}, errs: map[int]error{0: errors.New("no helper image")}}
	_, err := New(r, nil).Handle(context.Background(), withInstall)
	require.ErrorContains(t, err, "install Lynis: no helper image")

	r = &scriptedRunner{out: []Output{{Stdout: "Lynis is already installed\n"}, {}}, errs: map[int]error{1: errors.New("timeout")}}
	_, err = New(r, nil).Handle(context.Background(), withInstall)
	require.ErrorContains(t, err, "run Lynis: timeout")

	r = &scriptedRunner{out: []Output{{Stdout: "Lynis is already installed\n"}, {Stderr: "lynis: crashed", ExitCode: 2}}}
	_, err = New(r, nil).Handle(context.Background(), withInstall)
	require.ErrorContains(t, err, "run Lynis: lynis: crashed")

	// An install that finds Lynis already there does not claim to have installed it.
	r = &scriptedRunner{out: []Output{{Stdout: "Lynis is already installed\n"}, {Stdout: "@@NOLYNIS\n"}, {Stdout: "@@FIX umask pending\n"}}}
	res, err := New(r, nil).Handle(context.Background(), withInstall)
	require.NoError(t, err)
	require.False(t, res.LynisInstalledNow)
	require.Equal(t, "Lynis is not installed on this server.", res.Message)
}

func TestSplitAuditSeparatesMarkersFromTheReport(t *testing.T) {
	version, rc, report := splitAudit("@@VERSION  3.1.7 \r\n@@RC 0\n@@REPORT\nhardening_index=61\n@@RC 9\nwarning[]=x\n")
	require.Equal(t, "3.1.7", version)
	require.Equal(t, 0, rc)
	require.Contains(t, report, "hardening_index=61\n@@RC 9\nwarning[]=x\n", "markers inside the report are report text, not verdicts")
}
