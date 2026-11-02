package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agent/hardening"
	"github.com/nebulactrl/nebulactrl/internal/lynis"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// scriptRunner is a fake host for the hardening service: it recognises the
// install, audit and fix-driver scripts by their content and answers each with
// canned output, recording what ran in order.
type scriptRunner struct {
	audits  []hardening.Output // answered in order; the last one repeats
	install hardening.Output
	apply   hardening.Output
	status  hardening.Output
	err     error // returned for every Run
	ran     []string
}

func (r *scriptRunner) Mode() string { return hardening.ModeHost }

func (r *scriptRunner) Run(_ context.Context, script string) (hardening.Output, error) {
	if r.err != nil {
		return hardening.Output{}, r.err
	}
	switch {
	case strings.Contains(script, "lynis audit system"):
		r.ran = append(r.ran, "audit")
		o := r.audits[0]
		if len(r.audits) > 1 {
			r.audits = r.audits[1:]
		}
		return o, nil
	case strings.Contains(script, "packages.cisofy.com"):
		r.ran = append(r.ran, "install")
		return r.install, nil
	case strings.Contains(script, "nc_apply '"):
		r.ran = append(r.ran, "apply")
		return r.apply, nil
	case strings.Contains(script, "nc_rollback '"):
		r.ran = append(r.ran, "rollback")
		return r.apply, nil
	default:
		r.ran = append(r.ran, "status")
		return r.status, nil
	}
}

// auditOutput is what the audit script prints for a finished audit.
func auditOutput(index int) hardening.Output {
	return hardening.Output{Stdout: "@@VERSION 3.1.4\n@@RC 0\n@@REPORT\n" +
		"lynis_version=3.1.4\nhostname=box\nhardening_index=" + itoa(index) + "\nlynis_tests_done=210\n" +
		"warning[]=FIRE-4512|iptables is empty|-|-|\nsuggestion[]=KRNL-6000|sysctl differs|-|-|\nsuggestion[]=SSH-7408|ssh option|-|-|\n"}
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

const statusOut = "@@FIX sysctl-kernel pending would change 4 settings\n@@FIX umask compliant ok\n"

func newScript() *scriptRunner {
	return &scriptRunner{
		audits:  []hardening.Output{auditOutput(55)},
		install: hardening.Output{Stdout: "Installed Lynis\n"},
		apply:   hardening.Output{Stdout: "@@OUT sysctl-kernel applied wrote /etc/sysctl.d/x.conf\n@@OUT umask unchanged\n@@REBOOT sysctl-kernel\n"},
		status:  hardening.Output{Stdout: statusOut},
	}
}

func TestRunOnePrintsAuditSummary(t *testing.T) {
	r := newScript()
	var out bytes.Buffer
	err := runOne(context.Background(), &out, hardening.New(r, nil), agentprotocol.HardeningPayload{Action: agentprotocol.HardeningActionAudit}, false)
	require.NoError(t, err)
	assert.Equal(t, []string{"audit", "status"}, r.ran)
	assert.Contains(t, out.String(), "Lynis 3.1.4: hardening index 55 (210 tests, 1 warnings, 2 suggestions)")
	// No outcomes, so the fix states are listed.
	assert.Regexp(t, `sysctl-kernel\s+pending\s+would change 4 settings`, out.String())
	assert.Regexp(t, `umask\s+compliant\s+ok`, out.String())
}

func TestRunOneJSON(t *testing.T) {
	r := newScript()
	var out bytes.Buffer
	err := runOne(context.Background(), &out, hardening.New(r, nil), agentprotocol.HardeningPayload{Action: agentprotocol.HardeningActionAudit}, true)
	require.NoError(t, err)
	var res agentprotocol.HardeningResult
	require.NoError(t, json.Unmarshal(out.Bytes(), &res))
	assert.Equal(t, agentprotocol.HardeningActionAudit, res.Action)
	assert.Equal(t, hardening.ModeHost, res.Mode)
	assert.True(t, res.LynisInstalled)
	require.NotNil(t, res.Report)
	assert.Equal(t, 55, res.Report.HardeningIndex)
	assert.Len(t, res.Fixes, 2)
}

func TestRunOneApplyPrintsOutcomesInsteadOfFixStates(t *testing.T) {
	r := newScript()
	var out bytes.Buffer
	err := runOne(context.Background(), &out, hardening.New(r, nil),
		agentprotocol.HardeningPayload{Action: agentprotocol.HardeningActionApply, FixIDs: []string{"sysctl-kernel", "umask"}}, false)
	require.NoError(t, err)
	assert.Equal(t, []string{"apply", "audit", "status"}, r.ran)
	assert.Regexp(t, `sysctl-kernel\s+applied\s+wrote /etc/sysctl.d/x.conf`, out.String())
	assert.Regexp(t, `umask\s+unchanged`, out.String())
	assert.NotContains(t, out.String(), "compliant", "fix states are printed only when there are no outcomes")
}

func TestRunOnePropagatesServiceErrors(t *testing.T) {
	var out bytes.Buffer
	err := runOne(context.Background(), &out, hardening.New(&scriptRunner{err: errors.New("sh: not found")}, nil),
		agentprotocol.HardeningPayload{Action: agentprotocol.HardeningActionAudit}, false)
	require.ErrorContains(t, err, "sh: not found")
	assert.Empty(t, out.String())

	err = runOne(context.Background(), &out, hardening.New(newScript(), nil),
		agentprotocol.HardeningPayload{Action: agentprotocol.HardeningActionApply, FixIDs: []string{"nope"}}, true)
	require.ErrorContains(t, err, "unknown fix nope")
	assert.Empty(t, out.String())
}

func TestRunAllApplyReport(t *testing.T) {
	r := newScript()
	r.audits = []hardening.Output{auditOutput(55), auditOutput(72)} // before, then the re-audit
	var out bytes.Buffer
	err := runAll(context.Background(), &out, hardening.New(r, nil), []string{"sysctl-kernel"}, false, false, false)
	require.NoError(t, err)
	assert.Equal(t, []string{"install", "audit", "status", "apply", "audit", "status"}, r.ran, "installs and audits first, then applies and re-audits")

	s := out.String()
	assert.Contains(t, s, "Lynis 3.1.4 audited this host.")
	assert.Contains(t, s, "Hardening index before: 55")
	assert.Regexp(t, `sysctl-kernel\s+applied`, s)
	assert.Contains(t, s, "Hardening index after:  72 (+17)")
	assert.Contains(t, s, "Remaining findings: 2 suggestions, 1 warnings.")
	assert.Contains(t, s, "A reboot is needed")
}

func TestRunAllNoInstallSkipsInstallScript(t *testing.T) {
	r := newScript()
	var out bytes.Buffer
	require.NoError(t, runAll(context.Background(), &out, hardening.New(r, nil), nil, false, true, false))
	assert.NotContains(t, r.ran, "install")
}

func TestRunAllNoApplyAuditsOnly(t *testing.T) {
	r := newScript()
	var out bytes.Buffer
	require.NoError(t, runAll(context.Background(), &out, hardening.New(r, nil), nil, true, false, false))
	assert.NotContains(t, r.ran, "apply")
	assert.Contains(t, out.String(), "hardening index 55")
	assert.NotContains(t, out.String(), "Hardening index before")
}

func TestRunAllNoApplyJSONIsTheAuditResult(t *testing.T) {
	r := newScript()
	var out bytes.Buffer
	require.NoError(t, runAll(context.Background(), &out, hardening.New(r, nil), nil, true, false, true))
	var res agentprotocol.HardeningResult
	require.NoError(t, json.Unmarshal(out.Bytes(), &res))
	assert.Equal(t, agentprotocol.HardeningActionAudit, res.Action)
	assert.Equal(t, 55, res.Report.HardeningIndex)
}

func TestRunAllJSONHasBeforeAndAfter(t *testing.T) {
	r := newScript()
	r.audits = []hardening.Output{auditOutput(40), auditOutput(60)}
	var out bytes.Buffer
	require.NoError(t, runAll(context.Background(), &out, hardening.New(r, nil), nil, false, false, true))
	var got struct {
		Before, After agentprotocol.HardeningResult
	}
	require.NoError(t, json.Unmarshal(out.Bytes(), &got))
	assert.Equal(t, 40, got.Before.Report.HardeningIndex)
	assert.Equal(t, agentprotocol.HardeningActionApply, got.After.Action)
	assert.Equal(t, 60, got.After.Report.HardeningIndex)
}

func TestRunAllFailsWithoutLynis(t *testing.T) {
	r := newScript()
	r.audits = []hardening.Output{{Stdout: "@@NOLYNIS\n"}}
	var out bytes.Buffer
	err := runAll(context.Background(), &out, hardening.New(r, nil), nil, false, true, false)
	require.EqualError(t, err, "Lynis is not installed (rerun without --no-install)")
	assert.NotContains(t, r.ran, "apply")
	assert.Empty(t, out.String())
}

func TestRunAllErrors(t *testing.T) {
	t.Run("audit fails", func(t *testing.T) {
		err := runAll(context.Background(), io.Discard, hardening.New(&scriptRunner{err: errors.New("boom")}, nil), nil, false, false, false)
		require.ErrorContains(t, err, "boom")
	})
	t.Run("apply fails", func(t *testing.T) {
		r := newScript()
		r.apply = hardening.Output{Stderr: "permission denied", ExitCode: 1} // no @@OUT lines
		var out bytes.Buffer
		err := runAll(context.Background(), &out, hardening.New(r, nil), nil, false, false, false)
		require.ErrorContains(t, err, "apply fixes")
		require.ErrorContains(t, err, "permission denied")
		assert.Empty(t, out.String(), "nothing is printed for a failed run")
	})
	t.Run("unknown fix id", func(t *testing.T) {
		err := runAll(context.Background(), io.Discard, hardening.New(newScript(), nil), []string{"bogus"}, false, false, false)
		require.ErrorContains(t, err, "unknown fix bogus")
	})
}

func TestRunAllFollowUpAuditFailureIsShownAsMessage(t *testing.T) {
	r := newScript()
	// The re-audit finishes with a failing exit code: the fixes still count.
	r.audits = []hardening.Output{auditOutput(50), {Stdout: "@@VERSION 3.1.4\n@@RC 1\n@@REPORT\n"}}
	var out bytes.Buffer
	require.NoError(t, runAll(context.Background(), &out, hardening.New(r, nil), nil, false, false, false))
	s := out.String()
	assert.Contains(t, s, "The fixes were applied, but the follow-up audit failed")
	assert.NotContains(t, s, "Hardening index after")
	assert.Regexp(t, `sysctl-kernel\s+applied`, s)
}

func TestPrintResultMessageOnly(t *testing.T) {
	var out bytes.Buffer
	printResult(&out, agentprotocol.HardeningResult{Message: "Lynis is not installed on this server."})
	assert.Equal(t, "Lynis is not installed on this server.\n", out.String())

	out.Reset()
	printResult(&out, agentprotocol.HardeningResult{})
	assert.Empty(t, out.String())
}

func TestPrintOutcomesAlignsColumns(t *testing.T) {
	var out bytes.Buffer
	printOutcomes(&out, agentprotocol.HardeningResult{Outcomes: []agentprotocol.HardeningFixOutcome{
		{ID: "a", Outcome: "applied", Message: "m1"},
		{ID: "longer-id", Outcome: "skipped", Message: "m2"},
	}})
	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	require.Len(t, lines, 2)
	assert.Equal(t, strings.Index(lines[0], "applied"), strings.Index(lines[1], "skipped"))
	assert.True(t, strings.HasPrefix(lines[0], "  a"))
}

// execHardening runs `hardening <args>` on a fresh root command.
func execHardening(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := newHardeningCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs(args)
	err := cmd.ExecuteContext(context.Background())
	return out.String(), err
}

func TestHardeningListPrintsTheCatalog(t *testing.T) {
	out, err := execHardening(t, "list")
	require.NoError(t, err)
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	require.Len(t, lines, len(lynis.Catalog())+1)
	assert.Regexp(t, `^FIX\s+DEFAULT\s+LYNIS\s+TITLE$`, lines[0])
	for i, f := range lynis.Catalog() {
		line := lines[i+1]
		assert.True(t, strings.HasPrefix(line, f.ID), line)
		assert.Contains(t, line, f.Title)
		assert.Contains(t, line, strings.Join(f.Tests, ","))
	}
	// The default column carries true/false per fix.
	for _, f := range lynis.Catalog() {
		if f.Default {
			assert.Regexp(t, `(?m)^`+f.ID+`\s+true\s`, out)
			break
		}
	}
}

func TestHardeningCommandsNeedRoot(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: the root guard cannot be exercised")
	}
	for _, args := range [][]string{
		{"run"}, {"run", "--no-apply"}, {"audit"}, {"audit", "--install", "--json"}, {"apply"}, {"apply", "--fix", "umask"}, {"rollback", "--fix", "umask"},
	} {
		_, err := execHardening(t, args...)
		require.EqualError(t, err, "hardening changes system files: run it as root (sudo)", strings.Join(args, " "))
	}
}

func TestHardeningRollbackRequiresFix(t *testing.T) {
	_, err := execHardening(t, "rollback")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `required flag(s) "fix" not set`)
}

func TestHardeningRejectsExtraArgsAndUnknownFlags(t *testing.T) {
	_, err := execHardening(t, "audit", "extra")
	require.ErrorContains(t, err, `unknown command "extra"`)
	_, err = execHardening(t, "run", "--bogus")
	require.ErrorContains(t, err, "unknown flag: --bogus")
}
