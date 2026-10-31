package hardening

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/lynis"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// scratch is a fake host: a root directory for NC_ROOT and a fake `lynis` on
// the PATH that "audits" by copying a recorded report into place.
type scratch struct {
	t      *testing.T
	root   string
	report string
	runner DirectRunner
}

func newScratch(t *testing.T, withLynis bool) *scratch {
	t.Helper()
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	dir := t.TempDir()
	s := &scratch{t: t, root: filepath.Join(dir, "root"), report: filepath.Join(dir, "lynis-report.dat")}
	require.NoError(t, os.MkdirAll(filepath.Join(s.root, "tmp"), 0o755))
	bin := filepath.Join(dir, "bin")
	require.NoError(t, os.MkdirAll(bin, 0o755))
	fixture, err := filepath.Abs("../../lynis/testdata/report-debian13-before.dat")
	require.NoError(t, err)
	if withLynis {
		fake := "#!/bin/sh\ncase \"$1 $2\" in\n  \"show version\") echo 3.1.7;;\n  \"audit system\") [ -f \"$NC_FAKE_FAIL\" ] && exit 1; cp '" + fixture + "' \"$NC_LYNIS_REPORT\";;\nesac\nexit 0\n"
		require.NoError(t, os.WriteFile(filepath.Join(bin, "lynis"), []byte(fake), 0o755))
	}
	s.runner = DirectRunner{Env: []string{
		"PATH=" + bin + ":" + os.Getenv("PATH"), "NC_ROOT=" + s.root, "TMPDIR=" + filepath.Join(s.root, "tmp"),
		"NC_LYNIS_REPORT=" + s.report, "NC_FAKE_FAIL=" + filepath.Join(dir, "fail"),
	}}
	return s
}

func (s *scratch) write(p, content string) {
	s.t.Helper()
	full := filepath.Join(s.root, p)
	require.NoError(s.t, os.MkdirAll(filepath.Dir(full), 0o755))
	require.NoError(s.t, os.WriteFile(full, []byte(content), 0o644))
}

func (s *scratch) read(p string) string {
	s.t.Helper()
	b, err := os.ReadFile(filepath.Join(s.root, p))
	require.NoError(s.t, err)
	return string(b)
}

func TestAuditParsesReportAndReportsEveryFix(t *testing.T) {
	s := newScratch(t, true)
	res, err := New(s.runner, nil).Handle(context.Background(), agentprotocol.HardeningPayload{Action: agentprotocol.HardeningActionAudit})
	require.NoError(t, err)
	require.True(t, res.LynisInstalled)
	require.Equal(t, "3.1.7", res.LynisVersion)
	require.Equal(t, ModeHost, res.Mode)
	require.NotNil(t, res.Report)
	require.Equal(t, 62, res.Report.HardeningIndex)
	require.Len(t, res.Fixes, len(lynis.Catalog()))
}

func TestAuditWithoutLynisIsNotAnError(t *testing.T) {
	if _, err := exec.LookPath("lynis"); err == nil {
		t.Skip("this machine has Lynis installed")
	}
	s := newScratch(t, false)
	res, err := New(s.runner, nil).Handle(context.Background(), agentprotocol.HardeningPayload{Action: agentprotocol.HardeningActionAudit})
	require.NoError(t, err)
	require.False(t, res.LynisInstalled)
	require.Nil(t, res.Report)
	require.Contains(t, res.Message, "not installed")
	require.Len(t, res.Fixes, len(lynis.Catalog()), "fix states are still reported")
}

func TestApplyThenRollbackOnAScratchHost(t *testing.T) {
	s := newScratch(t, true)
	s.write("etc/login.defs", "UMASK\t022\nPASS_MAX_DAYS\t99999\nPASS_MIN_DAYS\t0\n")
	s.write("proc/sys/kernel/kptr_restrict", "0\n")
	s.write("proc/sys/net/ipv4/ip_forward", "1\n")
	svc := New(s.runner, nil)

	res, err := svc.Handle(context.Background(), agentprotocol.HardeningPayload{Action: agentprotocol.HardeningActionApply})
	require.NoError(t, err)
	outcome := map[string]string{}
	for _, o := range res.Outcomes {
		outcome[o.ID] = o.Outcome
	}
	require.Equal(t, agentprotocol.HardeningOutcomeApplied, outcome["umask"])
	require.Equal(t, agentprotocol.HardeningOutcomeApplied, outcome["sysctl-kernel"])
	require.Equal(t, agentprotocol.HardeningOutcomeApplied, outcome["password-ageing"])
	require.NotContains(t, outcome, "auditd", "optional fixes are not part of the default set")
	require.NotContains(t, outcome, "tmp-mount")
	require.NotNil(t, res.Report, "apply audits again so the new index is known")
	require.False(t, res.RebootRequired)
	require.Contains(t, s.read("etc/login.defs"), "UMASK\t027")
	require.Equal(t, "2", strings.TrimSpace(s.read("proc/sys/kernel/kptr_restrict")))
	require.Equal(t, "1", strings.TrimSpace(s.read("proc/sys/net/ipv4/ip_forward")))
	states := map[string]string{}
	for _, f := range res.Fixes {
		states[f.ID] = f.State
	}
	require.Equal(t, agentprotocol.HardeningFixApplied, states["umask"])
	require.Equal(t, agentprotocol.HardeningFixApplied, states["sysctl-kernel"])

	res, err = svc.Handle(context.Background(), agentprotocol.HardeningPayload{Action: agentprotocol.HardeningActionRollback, FixIDs: []string{"umask", "sysctl-kernel"}})
	require.NoError(t, err)
	require.Len(t, res.Outcomes, 2)
	for _, o := range res.Outcomes {
		require.Equal(t, agentprotocol.HardeningOutcomeRolledBack, o.Outcome, o.ID+": "+o.Message)
	}
	require.Contains(t, s.read("etc/login.defs"), "UMASK\t022")
	require.Equal(t, "0", strings.TrimSpace(s.read("proc/sys/kernel/kptr_restrict")))
}

func TestFailedFollowUpAuditKeepsTheOutcomes(t *testing.T) {
	s := newScratch(t, true)
	s.write("etc/login.defs", "UMASK\t022\n")
	require.NoError(t, os.WriteFile(filepath.Join(filepath.Dir(s.report), "fail"), nil, 0o644))
	res, err := New(s.runner, nil).Handle(context.Background(), agentprotocol.HardeningPayload{
		Action: agentprotocol.HardeningActionApply, FixIDs: []string{"umask"},
	})
	require.NoError(t, err, "the fix was applied; the command must not fail")
	require.Len(t, res.Outcomes, 1)
	require.Equal(t, agentprotocol.HardeningOutcomeApplied, res.Outcomes[0].Outcome)
	require.Contains(t, res.Message, "follow-up audit failed")
	require.Nil(t, res.Report)
}

func TestBadRequests(t *testing.T) {
	s := newScratch(t, true)
	svc := New(s.runner, nil)
	for name, p := range map[string]agentprotocol.HardeningPayload{
		"unknown action":      {Action: "explode"},
		"unknown fix":         {Action: agentprotocol.HardeningActionApply, FixIDs: []string{"disable-ssh-auth"}},
		"rollback without id": {Action: agentprotocol.HardeningActionRollback},
		"rollback unknown":    {Action: agentprotocol.HardeningActionRollback, FixIDs: []string{"nope"}},
	} {
		_, err := svc.Handle(context.Background(), p)
		require.Error(t, err, name)
	}
}

// fakeRunner answers with canned output.
type fakeRunner struct {
	out     []Output
	scripts []string
}

func (f *fakeRunner) Mode() string { return ModeHelper }
func (f *fakeRunner) Run(_ context.Context, script string) (Output, error) {
	f.scripts = append(f.scripts, script)
	o := f.out[0]
	if len(f.out) > 1 {
		f.out = f.out[1:]
	}
	return o, nil
}

func TestInstallingLynisIsReportedAndFailuresSurface(t *testing.T) {
	fr := &fakeRunner{out: []Output{
		{Stdout: "Installed Lynis from CISOfy's repository\nLynis 3.1.7\n"}, // install
		{Stdout: "@@NOLYNIS\n"}, // audit (pretend the PATH is wrong)
		{Stdout: "@@FIX umask pending\n"},
	}}
	res, err := New(fr, nil).Handle(context.Background(), agentprotocol.HardeningPayload{Action: agentprotocol.HardeningActionAudit, InstallLynis: true})
	require.NoError(t, err)
	require.True(t, res.LynisInstalledNow)
	require.Equal(t, ModeHelper, res.Mode)
	require.Contains(t, fr.scripts[0], "packages.cisofy.com")

	fr = &fakeRunner{out: []Output{{Stderr: "E: Unable to locate package lynis\n", ExitCode: 100}}}
	_, err = New(fr, nil).Handle(context.Background(), agentprotocol.HardeningPayload{Action: agentprotocol.HardeningActionAudit, InstallLynis: true})
	require.ErrorContains(t, err, "Unable to locate package lynis")

	fr = &fakeRunner{out: []Output{{Stdout: "@@VERSION 3.1.7\n@@RC 1\n@@REPORT\n"}}}
	_, err = New(fr, nil).Handle(context.Background(), agentprotocol.HardeningPayload{Action: agentprotocol.HardeningActionAudit})
	require.ErrorContains(t, err, "status 1")
}

func TestInstallScriptIsValidShell(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	for name, script := range map[string]string{"install": installLynisScript, "audit": auditScript} {
		cmd := exec.Command(sh, "-n")
		cmd.Stdin = strings.NewReader(script)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "%s: %s", name, out)
	}
}

func TestDirectRunnerCancellationKillsTheScript(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := DirectRunner{}.Run(ctx, "sleep 30"); done <- err }()
	cancel()
	require.ErrorContains(t, <-done, "script stopped")
	out, err := DirectRunner{}.Run(context.Background(), "echo hi; echo oops >&2; exit 7")
	require.NoError(t, err)
	require.Equal(t, 7, out.ExitCode)
	require.Equal(t, "hi\n", out.Stdout)
	require.Equal(t, "oops\n", out.Stderr)
}
